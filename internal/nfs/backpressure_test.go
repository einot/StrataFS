package nfs

// Clean-room tests for how ADR 0003's write backpressure reaches the wire,
// written from docs/adr/0003-write-backpressure.md as revised through
// 2026-09-24, §5 ("What happens when the commit fails"): a WRITE whose wait
// fails is answered with the status statusOf maps the drain's error to. They
// drive the server over a real TCP socket, as integration_test.go does, with a
// blobfs.FS whose MaxDirtyBytes is one chunk, so the second WRITE into a new
// file has to drain before it is admitted (§4, "Where Write waits").
//
// Each call is bounded by the 10 s deadline rpcClient.call sets on the
// connection, and a hang fails at the line that made the call.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"strata/internal/blobfs"
	"strata/internal/store"
	"strata/internal/sunrpc"
	"strata/internal/vfs"
	"strata/internal/xdr"
)

// nbpChunkSize is the chunk size, and the MaxDirtyBytes, of every server here.
const nbpChunkSize = 8192

func nbpLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// nbpBucket returns an empty local bucket in a temp dir.
func nbpBucket(t *testing.T) *store.Local {
	t.Helper()
	st, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// nbpStartServer brings up the full stack on a random loopback port, as
// startServer does, over st and with MaxDirtyBytes of one chunk.
func nbpStartServer(t *testing.T, st store.Store) string {
	t.Helper()
	log := nbpLog()
	fs, err := blobfs.New(context.Background(), blobfs.Config{
		Store:          st,
		ChunkSize:      nbpChunkSize,
		OwnerUID:       501,
		OwnerGID:       20,
		CommitInterval: time.Hour,
		MaxDirtyBytes:  nbpChunkSize,
		Log:            log,
	})
	if err != nil {
		t.Fatal(err)
	}

	rpc := sunrpc.NewServer(log)
	rpc.Register(ProgramNFS, VersionNFS, NewServer(fs, fs.WriteVerf(), log))
	rpc.Register(ProgramMount, VersionMount, NewMountServer(fs, "/", log))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ln.Close() })
	go rpc.Serve(ctx, ln)
	return ln.Addr().String()
}

// nbpWrite sends an UNSTABLE WRITE, encoded as TestNFSEndToEnd encodes it, and
// returns the reply's nfsstat3.
func nbpWrite(t *testing.T, c *rpcClient, fh []byte, off uint64, data []byte) uint32 {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procWrite, func(w *xdr.Writer) {
		w.Opaque(fh)
		w.Uint64(off)
		w.Uint32(uint32(len(data)))
		w.Uint32(uint32(vfs.Unstable))
		w.Opaque(data)
	})
	st := r.Uint32()
	if err := r.Err(); err != nil {
		t.Fatalf("decoding the WRITE reply at %d: %v", off, err)
	}
	return st
}

// TestNFSBackpressureReadOnlyStore (N1) pins ADR 0003 §5: "A read-only store
// reports NFS3ERR_ROFS, when the drain has a chunk to upload. putChunk returns
// vfs.ErrROFS; it propagates through flushOpen → flushAll → Sync's wrapping
// fmt.Errorf ... and survives the unwrap" that statusOf performs. WRITE 1
// fills the budget with content new to the bucket; WRITE 2 drains, and the
// drain's upload fails on the read-only store.
// TestNFSBackpressureReadOnlyStoreSkippedUpload covers a drain with nothing
// to upload.
func TestNFSBackpressureReadOnlyStore(t *testing.T) {
	st := nbpBucket(t)
	if _, err := blobfs.New(context.Background(), blobfs.Config{
		Store:     st,
		ChunkSize: nbpChunkSize,
		OwnerUID:  501,
		OwnerGID:  20,
		Log:       nbpLog(),
	}); err != nil {
		t.Fatalf("initialising the bucket: %v", err)
	}

	addr := nbpStartServer(t, store.ReadOnly{Store: st})
	c := dial(t, addr)
	root := mountRoot(t, c)
	fh := nfsCreate(t, c, root, "ro.bin")

	if got := nbpWrite(t, c, fh, 0, bytes.Repeat([]byte("new content for a read-only bucket. "), 3)); got != uint32(vfs.OK) {
		t.Fatalf("WRITE 1, below the limit, status = %d, want NFS3_OK(0): it is admitted "+
			"without draining", got)
	}
	if got := nbpWrite(t, c, fh, 200, []byte("over the limit")); got != uint32(vfs.ErrROFS) {
		t.Errorf("WRITE 2, whose drain fails on a read-only store, status = %d, want "+
			"NFS3ERR_ROFS(%d): \"A read-only store reports NFS3ERR_ROFS, when the drain has "+
			"a chunk to upload\" (ADR 0003 §5)", got, uint32(vfs.ErrROFS))
	}
}

// TestNFSBackpressureReadOnlyStoreSkippedUpload pins the condition in ADR 0003
// §5's read-only case: "When every chunk the drain flushes is skipped that
// way, flushAll succeeds and the commit's snapshot Put fails instead, with
// store.ErrUnsupported wrapped by fmt.Errorf ... rather than mapped to
// vfs.ErrROFS as putChunk maps it. With no vfs.Status in the chain, the
// client gets NFS3ERR_IO." A writable mount commits a file; the server, on a
// read-only view of the same bucket, is then sent the same bytes into a new
// file, so the drain's only chunk is found by the HEAD before the upload and
// is not uploaded.
func TestNFSBackpressureReadOnlyStoreSkippedUpload(t *testing.T) {
	st := nbpBucket(t)
	ctx := context.Background()
	content := bytes.Repeat([]byte("content the bucket already holds. "), 3)
	seed, err := blobfs.New(ctx, blobfs.Config{
		Store:     st,
		ChunkSize: nbpChunkSize,
		OwnerUID:  501,
		OwnerGID:  20,
		Log:       nbpLog(),
	})
	if err != nil {
		t.Fatalf("initialising the bucket: %v", err)
	}
	caller := vfs.Caller{UID: 501, GID: 20}
	h, _, err := seed.Create(ctx, caller, seed.Root(), "seed.bin", vfs.SetAttr{}, true)
	if err != nil {
		t.Fatalf("seeding: Create: %v", err)
	}
	if n, _, err := seed.Write(ctx, caller, h, 0, content, vfs.Unstable); err != nil || int(n) != len(content) {
		t.Fatalf("seeding: Write = (%d, %v), want (%d, nil)", n, err, len(content))
	}
	if err := seed.Sync(ctx); err != nil {
		t.Fatalf("seeding: Sync: %v", err)
	}

	addr := nbpStartServer(t, store.ReadOnly{Store: st})
	c := dial(t, addr)
	root := mountRoot(t, c)
	fh := nfsCreate(t, c, root, "copy.bin")

	if got := nbpWrite(t, c, fh, 0, content); got != uint32(vfs.OK) {
		t.Fatalf("WRITE 1, below the limit, status = %d, want NFS3_OK(0): it is admitted "+
			"without draining", got)
	}
	if got := nbpWrite(t, c, fh, 200, []byte("over the limit")); got != uint32(vfs.ErrIO) {
		t.Errorf("WRITE 2, whose drain has no chunk to upload on a read-only store, status = "+
			"%d, want NFS3ERR_IO(%d): the snapshot Put fails with store.ErrUnsupported, and "+
			"with no vfs.Status in the chain the client gets NFS3ERR_IO (ADR 0003 §5; "+
			"NFS3ERR_ROFS here means the drain tried to upload a chunk the bucket already "+
			"holds)", got, uint32(vfs.ErrIO))
	}
}

// TestNFSBackpressureDivergedDrain (N2) pins ADR 0003 §5: "A diverged
// filesystem reports NFS3ERR_IO, not NFS3ERR_STALE ... The write whose drain
// discovers divergence therefore gets NFS3ERR_IO; the next write gets
// NFS3ERR_STALE, from f.mutable(), because the failed drain set f.diverged."
func TestNFSBackpressureDivergedDrain(t *testing.T) {
	st := nbpBucket(t)
	addr := nbpStartServer(t, st)
	c := dial(t, addr)
	root := mountRoot(t, c)
	fh := nfsCreate(t, c, root, "diverge.bin")

	ctx := context.Background()
	other, err := blobfs.New(ctx, blobfs.Config{
		Store:     st,
		ChunkSize: nbpChunkSize,
		OwnerUID:  501,
		OwnerGID:  20,
		Log:       nbpLog(),
	})
	if err != nil {
		t.Fatalf("mounting a second FS on the bucket: %v", err)
	}
	if _, _, err := other.Create(ctx, vfs.Caller{UID: 501, GID: 20}, other.Root(), "from-other.txt", vfs.SetAttr{}, true); err != nil {
		t.Fatalf("the second FS's Create: %v", err)
	}
	if err := other.Sync(ctx); err != nil {
		t.Fatalf("the second FS's Sync, which moves the root pointer out from under the server: %v", err)
	}

	if got := nbpWrite(t, c, fh, 0, []byte("the write that fills the budget")); got != uint32(vfs.OK) {
		t.Fatalf("WRITE 1, below the limit, status = %d, want NFS3_OK(0)", got)
	}
	if got := nbpWrite(t, c, fh, 200, []byte("the write whose drain diverges")); got != uint32(vfs.ErrIO) {
		t.Errorf("WRITE 2, whose drain discovers divergence, status = %d, want NFS3ERR_IO(%d), "+
			"not NFS3ERR_STALE: the divergence paths return a bare error (ADR 0003 §5)",
			got, uint32(vfs.ErrIO))
	}
	if got := nbpWrite(t, c, fh, 300, []byte("the write after divergence")); got != uint32(vfs.ErrStale) {
		t.Errorf("WRITE 3, after the failed drain set f.diverged, status = %d, want "+
			"NFS3ERR_STALE(%d) (ADR 0003 §5)", got, uint32(vfs.ErrStale))
	}
}
