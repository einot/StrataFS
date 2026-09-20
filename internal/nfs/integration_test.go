package nfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"strata/internal/blobfs"
	"strata/internal/store"
	"strata/internal/sunrpc"
	"strata/internal/vfs"
	"strata/internal/xdr"
)

// rpcClient is a minimal ONC RPC client used to drive the server over a real
// TCP connection, so these tests exercise the actual wire encoding rather than
// calling the handlers directly.
type rpcClient struct {
	t    *testing.T
	conn net.Conn
	xid  uint32
	uid  uint32
	gid  uint32
}

func dial(t *testing.T, addr string) *rpcClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &rpcClient{t: t, conn: conn, xid: 1, uid: 501, gid: 20}
}

// call sends one RPC and returns a reader positioned at the reply body.
func (c *rpcClient) call(prog, vers, proc uint32, args func(*xdr.Writer)) *xdr.Reader {
	c.t.Helper()
	c.xid++

	w := xdr.NewWriter()
	w.Uint32(c.xid)
	w.Uint32(0) // CALL
	w.Uint32(2) // RPC version
	w.Uint32(prog)
	w.Uint32(vers)
	w.Uint32(proc)

	// AUTH_SYS credentials.
	cred := xdr.NewWriter()
	cred.Uint32(0) // stamp
	cred.String("test-client")
	cred.Uint32(c.uid)
	cred.Uint32(c.gid)
	cred.Uint32(1)
	cred.Uint32(c.gid)
	w.Uint32(1) // AUTH_SYS
	w.Opaque(cred.Bytes())

	w.Uint32(0) // verifier flavor AUTH_NONE
	w.Uint32(0) // verifier length

	if args != nil {
		args(w)
	}

	body := w.Bytes()
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body))|0x80000000)
	c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.conn.Write(append(hdr[:], body...)); err != nil {
		c.t.Fatalf("write call: %v", err)
	}

	reply := c.readRecord()
	r := xdr.NewReader(reply)
	if got := r.Uint32(); got != c.xid {
		c.t.Fatalf("reply xid = %d, want %d", got, c.xid)
	}
	if got := r.Uint32(); got != 1 {
		c.t.Fatalf("reply mtype = %d, want REPLY", got)
	}
	if got := r.Uint32(); got != 0 {
		c.t.Fatalf("reply rejected, reply_stat = %d", got)
	}
	r.Uint32() // verifier flavor
	r.Opaque() // verifier body
	if got := r.Uint32(); got != 0 {
		c.t.Fatalf("accept_stat = %d (not SUCCESS) for prog %d proc %d", got, prog, proc)
	}
	return r
}

func (c *rpcClient) readRecord() []byte {
	c.t.Helper()
	var out []byte
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
			c.t.Fatalf("read record header: %v", err)
		}
		h := binary.BigEndian.Uint32(hdr[:])
		n := int(h & 0x7fffffff)
		frag := make([]byte, n)
		if _, err := io.ReadFull(c.conn, frag); err != nil {
			c.t.Fatalf("read record body: %v", err)
		}
		out = append(out, frag...)
		if h&0x80000000 != 0 {
			return out
		}
	}
}

// startServer brings up the full stack on a random loopback port.
func startServer(t *testing.T) (addr string, bucketDir string, fs *blobfs.FS) {
	t.Helper()
	dir := t.TempDir()
	bucketDir = filepath.Join(dir, "bucket")

	st, err := store.NewLocal(bucketDir)
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fs, err = blobfs.New(context.Background(), blobfs.Config{
		Store:     st,
		ChunkSize: 8192,
		OwnerUID:  501, OwnerGID: 20,
		CommitInterval: time.Hour, // commits in these tests are explicit
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

	return ln.Addr().String(), bucketDir, fs
}

// mountRoot performs a MOUNT MNT and returns the root file handle.
func mountRoot(t *testing.T, c *rpcClient) []byte {
	t.Helper()
	r := c.call(ProgramMount, VersionMount, mountProcMnt, func(w *xdr.Writer) {
		w.String("/")
	})
	if st := r.Uint32(); st != mnt3OK {
		t.Fatalf("MNT status = %d", st)
	}
	fh := r.Opaque()
	nflavors := r.Uint32()
	if nflavors == 0 {
		t.Error("server advertised no auth flavors")
	}
	for i := uint32(0); i < nflavors; i++ {
		r.Uint32()
	}
	if r.Err() != nil {
		t.Fatalf("decoding MNT reply: %v", r.Err())
	}
	if len(fh) == 0 || len(fh) > maxHandleLen {
		t.Fatalf("root handle has bad length %d", len(fh))
	}
	return fh
}

// skipPostOpAttr consumes a post_op_attr.
func skipPostOpAttr(r *xdr.Reader) {
	if r.Bool() {
		skipFattr(r)
	}
}

func skipFattr(r *xdr.Reader) {
	for i := 0; i < 5; i++ {
		r.Uint32() // type, mode, nlink, uid, gid
	}
	r.Uint64() // size
	r.Uint64() // used
	r.Uint32() // rdev major
	r.Uint32() // rdev minor
	r.Uint64() // fsid
	r.Uint64() // fileid
	for i := 0; i < 6; i++ {
		r.Uint32() // atime, mtime, ctime (sec+nsec each)
	}
}

func skipWccData(r *xdr.Reader) {
	if r.Bool() {
		r.Uint64() // size
		r.Uint32()
		r.Uint32() // mtime
		r.Uint32()
		r.Uint32() // ctime
	}
	skipPostOpAttr(r)
}

// nfsCreate issues a CREATE and returns the new file's handle.
func nfsCreate(t *testing.T, c *rpcClient, dir []byte, name string) []byte {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procCreate, func(w *xdr.Writer) {
		w.Opaque(dir)
		w.String(name)
		w.Uint32(createGuarded)
		// sattr3: set mode only.
		w.Bool(true)
		w.Uint32(0o644)
		w.Bool(false) // uid
		w.Bool(false) // gid
		w.Bool(false) // size
		w.Uint32(dontChange)
		w.Uint32(dontChange)
	})
	if st := r.Uint32(); st != uint32(vfs.OK) {
		t.Fatalf("CREATE %s status = %d", name, st)
	}
	var fh []byte
	if r.Bool() {
		fh = r.Opaque()
	} else {
		t.Fatalf("CREATE returned no handle")
	}
	skipPostOpAttr(r)
	skipWccData(r)
	if r.Err() != nil {
		t.Fatalf("decoding CREATE reply: %v", r.Err())
	}
	return fh
}

// TestNFSEndToEnd drives a full create/write/commit/read cycle over the real
// protocol and then verifies the bytes landed in the buckets.
func TestNFSEndToEnd(t *testing.T) {
	addr, bucketDir, _ := startServer(t)
	c := dial(t, addr)

	// MOUNT NULL must succeed before anything else.
	c.call(ProgramMount, VersionMount, mountProcNull, nil)
	root := mountRoot(t, c)

	// GETATTR on the root should report a directory.
	r := c.call(ProgramNFS, VersionNFS, procGetAttr, func(w *xdr.Writer) { w.Opaque(root) })
	if st := r.Uint32(); st != uint32(vfs.OK) {
		t.Fatalf("GETATTR status = %d", st)
	}
	if ftype := r.Uint32(); ftype != uint32(vfs.TypeDir) {
		t.Errorf("root type = %d, want directory", ftype)
	}

	// FSINFO is what a client uses to size its transfers.
	r = c.call(ProgramNFS, VersionNFS, procFSInfo, func(w *xdr.Writer) { w.Opaque(root) })
	if st := r.Uint32(); st != uint32(vfs.OK) {
		t.Fatalf("FSINFO status = %d", st)
	}
	skipPostOpAttr(r)
	rtmax := r.Uint32()
	if rtmax == 0 {
		t.Error("FSINFO advertises rtmax of 0")
	}

	// Create, write across a chunk boundary, then commit.
	fh := nfsCreate(t, c, root, "report.txt")
	payload := bytes.Repeat([]byte("nfs over object storage. "), 1000) // ~25 KB over 8 KB chunks

	written := 0
	for written < len(payload) {
		n := 4096
		if rem := len(payload) - written; rem < n {
			n = rem
		}
		off := written
		chunk := payload[written : written+n]
		r = c.call(ProgramNFS, VersionNFS, procWrite, func(w *xdr.Writer) {
			w.Opaque(fh)
			w.Uint64(uint64(off))
			w.Uint32(uint32(len(chunk)))
			w.Uint32(uint32(vfs.Unstable))
			w.Opaque(chunk)
		})
		if st := r.Uint32(); st != uint32(vfs.OK) {
			t.Fatalf("WRITE at %d status = %d", off, st)
		}
		skipWccData(r)
		got := r.Uint32()
		if int(got) != len(chunk) {
			t.Fatalf("WRITE at %d wrote %d of %d", off, got, len(chunk))
		}
		r.Uint32() // committed
		r.Fixed(8) // write verifier
		written += n
	}

	// COMMIT makes it durable.
	r = c.call(ProgramNFS, VersionNFS, procCommit, func(w *xdr.Writer) {
		w.Opaque(fh)
		w.Uint64(0)
		w.Uint32(0)
	})
	if st := r.Uint32(); st != uint32(vfs.OK) {
		t.Fatalf("COMMIT status = %d", st)
	}

	// Read it all back through LOOKUP + READ.
	r = c.call(ProgramNFS, VersionNFS, procLookup, func(w *xdr.Writer) {
		w.Opaque(root)
		w.String("report.txt")
	})
	if st := r.Uint32(); st != uint32(vfs.OK) {
		t.Fatalf("LOOKUP status = %d", st)
	}
	fh2 := r.Opaque()

	var readBack []byte
	for {
		off := uint64(len(readBack))
		r = c.call(ProgramNFS, VersionNFS, procRead, func(w *xdr.Writer) {
			w.Opaque(fh2)
			w.Uint64(off)
			w.Uint32(4096)
		})
		if st := r.Uint32(); st != uint32(vfs.OK) {
			t.Fatalf("READ at %d status = %d", off, st)
		}
		skipPostOpAttr(r)
		count := r.Uint32()
		eof := r.Bool()
		data := r.Opaque()
		if int(count) != len(data) {
			t.Errorf("READ count %d disagrees with data length %d", count, len(data))
		}
		readBack = append(readBack, data...)
		if eof || len(data) == 0 {
			break
		}
	}
	if !bytes.Equal(readBack, payload) {
		t.Errorf("read back %d bytes, wrote %d; contents differ", len(readBack), len(payload))
	}

	// The bucket must hold chunks, snapshots and the root pointer, and nothing else.
	entries, err := os.ReadDir(filepath.Join(bucketDir, "chunks"))
	if err != nil {
		t.Fatalf("bucket has no chunks directory: %v", err)
	}
	if len(entries) == 0 {
		t.Error("no chunks were written")
	}
	if _, err := os.Stat(filepath.Join(bucketDir, "root")); err != nil {
		t.Errorf("bucket has no root pointer: %v", err)
	}
	top, err := os.ReadDir(bucketDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range top {
		switch e.Name() {
		case "chunks", "snapshots", "root":
		default:
			t.Errorf("unexpected object in bucket: %q (all: %v)", e.Name(), names(top))
		}
	}
}

func names(es []os.DirEntry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// TestNFSDirectoryOps covers MKDIR, READDIRPLUS, RENAME, REMOVE and RMDIR
// over the wire.
func TestNFSDirectoryOps(t *testing.T) {
	addr, _, _ := startServer(t)
	c := dial(t, addr)
	root := mountRoot(t, c)

	// MKDIR
	r := c.call(ProgramNFS, VersionNFS, procMkdir, func(w *xdr.Writer) {
		w.Opaque(root)
		w.String("projects")
		w.Bool(true)
		w.Uint32(0o755)
		w.Bool(false)
		w.Bool(false)
		w.Bool(false)
		w.Uint32(dontChange)
		w.Uint32(dontChange)
	})
	if st := r.Uint32(); st != uint32(vfs.OK) {
		t.Fatalf("MKDIR status = %d", st)
	}
	var dirFH []byte
	if r.Bool() {
		dirFH = r.Opaque()
	}

	for _, n := range []string{"alpha.txt", "beta.txt", "gamma.txt"} {
		nfsCreate(t, c, dirFH, n)
	}

	// READDIRPLUS should return every entry plus "." and "..".
	found := map[string]bool{}
	var cookie uint64
	var verf []byte = make([]byte, 8)
	for iter := 0; iter < 20; iter++ {
		r = c.call(ProgramNFS, VersionNFS, procReaddirPlus, func(w *xdr.Writer) {
			w.Opaque(dirFH)
			w.Uint64(cookie)
			w.Fixed(verf)
			w.Uint32(4096)  // dircount
			w.Uint32(32768) // maxcount
		})
		if st := r.Uint32(); st != uint32(vfs.OK) {
			t.Fatalf("READDIRPLUS status = %d", st)
		}
		skipPostOpAttr(r)
		verf = r.Fixed(8)
		count := 0
		for r.Bool() {
			r.Uint64() // fileid
			name := r.String()
			cookie = r.Uint64()
			skipPostOpAttr(r)
			if r.Bool() {
				r.Opaque() // handle
			}
			found[name] = true
			count++
		}
		eof := r.Bool()
		if r.Err() != nil {
			t.Fatalf("decoding READDIRPLUS: %v", r.Err())
		}
		if eof {
			break
		}
		if count == 0 {
			t.Fatal("READDIRPLUS made no progress")
		}
	}
	for _, want := range []string{".", "..", "alpha.txt", "beta.txt", "gamma.txt"} {
		if !found[want] {
			t.Errorf("READDIRPLUS did not return %q (got %v)", want, keys(found))
		}
	}

	// RENAME across directories.
	r = c.call(ProgramNFS, VersionNFS, procRename, func(w *xdr.Writer) {
		w.Opaque(dirFH)
		w.String("alpha.txt")
		w.Opaque(root)
		w.String("moved.txt")
	})
	if st := r.Uint32(); st != uint32(vfs.OK) {
		t.Fatalf("RENAME status = %d", st)
	}

	r = c.call(ProgramNFS, VersionNFS, procLookup, func(w *xdr.Writer) {
		w.Opaque(root)
		w.String("moved.txt")
	})
	if st := r.Uint32(); st != uint32(vfs.OK) {
		t.Errorf("LOOKUP after rename status = %d", st)
	}

	// RMDIR on a non-empty directory must fail with NOTEMPTY.
	r = c.call(ProgramNFS, VersionNFS, procRmdir, func(w *xdr.Writer) {
		w.Opaque(root)
		w.String("projects")
	})
	if st := r.Uint32(); st != uint32(vfs.ErrNotEmpty) {
		t.Errorf("RMDIR on non-empty dir status = %d, want %d", st, vfs.ErrNotEmpty)
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestNFSErrorsAreProtocolErrors checks that failures come back as NFS status
// codes in a well-formed reply, not as RPC-level errors.
func TestNFSErrorsAreProtocolErrors(t *testing.T) {
	addr, _, _ := startServer(t)
	c := dial(t, addr)
	root := mountRoot(t, c)

	// LOOKUP of something that does not exist.
	r := c.call(ProgramNFS, VersionNFS, procLookup, func(w *xdr.Writer) {
		w.Opaque(root)
		w.String("nope.txt")
	})
	if st := r.Uint32(); st != uint32(vfs.ErrNoEnt) {
		t.Errorf("LOOKUP of missing file = %d, want NOENT", st)
	}

	// A malformed handle must produce a status, not a dropped connection.
	r = c.call(ProgramNFS, VersionNFS, procGetAttr, func(w *xdr.Writer) {
		w.Opaque([]byte{1, 2, 3})
	})
	if st := r.Uint32(); st != uint32(vfs.ErrBadHandle) && st != uint32(vfs.ErrStale) {
		t.Errorf("GETATTR with bad handle = %d, want BADHANDLE or STALE", st)
	}

	// MKNOD is honestly unsupported.
	r = c.call(ProgramNFS, VersionNFS, procMknod, func(w *xdr.Writer) {
		w.Opaque(root)
		w.String("dev")
	})
	if st := r.Uint32(); st != uint32(vfs.ErrNotSupp) {
		t.Errorf("MKNOD = %d, want NOTSUPP", st)
	}

	// An unknown procedure number must be refused at the RPC layer, which the
	// client helper turns into a fatal error, so check it separately below.
}

// TestUnknownProgramRejected checks PROG_UNAVAIL handling.
func TestUnknownProgramRejected(t *testing.T) {
	addr, _, _ := startServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	w := xdr.NewWriter()
	w.Uint32(99)
	w.Uint32(0)
	w.Uint32(2)
	w.Uint32(123456) // no such program
	w.Uint32(1)
	w.Uint32(0)
	w.Uint32(0)
	w.Uint32(0)
	w.Uint32(0)
	w.Uint32(0)

	body := w.Bytes()
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body))|0x80000000)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(append(hdr[:], body...)); err != nil {
		t.Fatal(err)
	}

	var rhdr [4]byte
	if _, err := io.ReadFull(conn, rhdr[:]); err != nil {
		t.Fatalf("no reply to unknown program: %v", err)
	}
	n := int(binary.BigEndian.Uint32(rhdr[:]) & 0x7fffffff)
	buf := make([]byte, n)
	io.ReadFull(conn, buf)

	r := xdr.NewReader(buf)
	r.Uint32() // xid
	r.Uint32() // mtype
	r.Uint32() // reply_stat
	r.Uint32() // verf flavor
	r.Opaque() // verf body
	if st := r.Uint32(); st != 1 {
		t.Errorf("accept_stat for unknown program = %d, want PROG_UNAVAIL(1)", st)
	}
}

var _ = fmt.Sprintf
