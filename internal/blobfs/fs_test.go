package blobfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var testCaller = vfs.Caller{UID: 501, GID: 20}

// newTestFS builds a filesystem over two local buckets in a temp dir.
func newTestFS(t *testing.T) (*FS, *store.Local, *store.Local, string) {
	t.Helper()
	dir := t.TempDir()
	data, err := store.NewLocal(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.NewLocal(filepath.Join(dir, "meta"))
	if err != nil {
		t.Fatal(err)
	}
	fs, err := New(context.Background(), Config{
		Data: data, Meta: meta,
		ChunkSize: 4096,
		OwnerUID:  501, OwnerGID: 20,
		Log: quietLog(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return fs, data, meta, dir
}

func mustCreate(t *testing.T, fs *FS, dir vfs.Handle, name string) vfs.Handle {
	t.Helper()
	h, _, err := fs.Create(context.Background(), testCaller, dir, name, vfs.SetAttr{}, true)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return h
}

func mustWrite(t *testing.T, fs *FS, h vfs.Handle, off uint64, data []byte) {
	t.Helper()
	n, _, err := fs.Write(context.Background(), testCaller, h, off, data, vfs.Unstable)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if int(n) != len(data) {
		t.Fatalf("short write: %d of %d", n, len(data))
	}
}

func readAll(t *testing.T, fs *FS, h vfs.Handle, size int) []byte {
	t.Helper()
	out := make([]byte, 0, size)
	var off uint64
	for {
		chunk, eof, err := fs.Read(context.Background(), testCaller, h, off, 1024)
		if err != nil {
			t.Fatalf("read at %d: %v", off, err)
		}
		out = append(out, chunk...)
		off += uint64(len(chunk))
		if eof || len(chunk) == 0 {
			break
		}
	}
	return out
}

// TestWriteReadRemount is the end-to-end durability check: data written through
// one mount must come back byte-identical through a fresh mount that shares
// nothing but the two buckets.
func TestWriteReadRemount(t *testing.T) {
	ctx := context.Background()
	fs, data, meta, _ := newTestFS(t)

	// Span several chunks and a partial final chunk.
	payload := bytes.Repeat([]byte("strata!"), 3000) // 21000 bytes over 4096-byte chunks
	h := mustCreate(t, fs, fs.Root(), "hello.txt")
	mustWrite(t, fs, h, 0, payload)

	if err := fs.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Remount from the buckets alone.
	fs2, err := New(ctx, Config{Data: data, Meta: meta, ChunkSize: 4096, Log: quietLog()})
	if err != nil {
		t.Fatalf("remount: %v", err)
	}
	h2, attr, err := fs2.Lookup(ctx, testCaller, fs2.Root(), "hello.txt")
	if err != nil {
		t.Fatalf("lookup after remount: %v", err)
	}
	if attr.Size != uint64(len(payload)) {
		t.Errorf("size after remount = %d, want %d", attr.Size, len(payload))
	}
	if got := readAll(t, fs2, h2, len(payload)); !bytes.Equal(got, payload) {
		t.Errorf("contents differ after remount: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestDataBucketLeaksNoNames is the central privacy claim of the design: the
// data bucket must contain nothing but content-addressed chunks, so sharing it
// reveals no filenames or directory structure.
func TestDataBucketLeaksNoNames(t *testing.T) {
	ctx := context.Background()
	fs, data, _, _ := newTestFS(t)

	dirH, _, err := fs.Mkdir(ctx, testCaller, fs.Root(), "secret-project", vfs.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	h := mustCreate(t, fs, dirH, "acquisition-memo.txt")
	mustWrite(t, fs, h, 0, []byte("confidential contents"))
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	objs, err := data.List(ctx, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) == 0 {
		t.Fatal("expected at least one chunk in the data bucket")
	}
	for _, o := range objs {
		if !strings.HasPrefix(o.Key, chunkPrefix) {
			t.Errorf("data bucket holds a non-chunk object: %q", o.Key)
		}
		// The key must be the prefix plus a 64-char hex digest and nothing else.
		name := strings.TrimPrefix(o.Key, chunkPrefix)
		if len(name) != 64 {
			t.Errorf("chunk key %q is not a bare sha256 digest", o.Key)
		}
		for _, leak := range []string{"secret-project", "acquisition-memo", ".txt"} {
			if strings.Contains(o.Key, leak) {
				t.Errorf("data bucket key %q leaks namespace detail %q", o.Key, leak)
			}
		}
	}
}

// TestDedupAcrossFiles checks that identical content is stored once, which is
// what content addressing buys.
func TestDedupAcrossFiles(t *testing.T) {
	ctx := context.Background()
	fs, data, _, _ := newTestFS(t)

	content := bytes.Repeat([]byte("x"), 4096)
	for _, name := range []string{"a.bin", "b.bin", "c.bin"} {
		h := mustCreate(t, fs, fs.Root(), name)
		mustWrite(t, fs, h, 0, content)
	}
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	objs, err := data.List(ctx, chunkPrefix, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Errorf("three identical files should share one chunk, got %d objects", len(objs))
	}
}

// TestSharedReadOnlyDataBucket exercises the scenario the design exists for:
// a second party mounts the same data bucket read-only, with their own copy of
// the metadata, and can read everything but write nothing.
func TestSharedReadOnlyDataBucket(t *testing.T) {
	ctx := context.Background()
	fs, data, meta, dir := newTestFS(t)

	payload := []byte("shared bytes, private namespace")
	h := mustCreate(t, fs, fs.Root(), "shared.txt")
	mustWrite(t, fs, h, 0, payload)
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	// The second party receives a copy of the namespace out of band and mounts
	// the shared data bucket read-only.
	metaB := filepath.Join(dir, "meta-b")
	if err := copyTree(filepath.Join(dir, "meta"), metaB); err != nil {
		t.Fatal(err)
	}
	_ = meta
	storeB, err := store.NewLocal(metaB)
	if err != nil {
		t.Fatal(err)
	}

	fsB, err := New(ctx, Config{
		Data:      store.ReadOnly{Store: data},
		Meta:      storeB,
		ChunkSize: 4096,
		Log:       quietLog(),
	})
	if err != nil {
		t.Fatalf("mount shared: %v", err)
	}

	hB, _, err := fsB.Lookup(ctx, testCaller, fsB.Root(), "shared.txt")
	if err != nil {
		t.Fatalf("lookup in shared mount: %v", err)
	}
	if got := readAll(t, fsB, hB, len(payload)); !bytes.Equal(got, payload) {
		t.Errorf("shared read got %q, want %q", got, payload)
	}

	// Writing new content must fail: the data bucket rejects the chunk upload.
	hNew := mustCreate(t, fsB, fsB.Root(), "attempt.txt")
	mustWrite(t, fsB, hNew, 0, []byte("this should not reach the shared bucket"))
	err = fsB.Sync(ctx)
	if err == nil {
		t.Error("expected commit to fail against a read-only data bucket")
	}
}

// TestSparseFileCostsNothing checks that extending a file by truncate stores no
// chunks and reads back as zeros.
func TestSparseFileCostsNothing(t *testing.T) {
	ctx := context.Background()
	fs, data, _, _ := newTestFS(t)

	h := mustCreate(t, fs, fs.Root(), "sparse.bin")
	size := uint64(4096 * 10)
	if _, err := fs.SetAttr(ctx, testCaller, h, vfs.SetAttr{Size: &size}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	objs, err := data.List(ctx, chunkPrefix, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 0 {
		t.Errorf("sparse file should store no chunks, got %d", len(objs))
	}

	got, _, err := fs.Read(ctx, testCaller, h, 0, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8192 {
		t.Fatalf("read %d bytes, want 8192", len(got))
	}
	for i, b := range got {
		if b != 0 {
			t.Fatalf("byte %d of a hole is %d, want 0", i, b)
		}
	}
}

// TestTruncateShortensFile checks the read-modify-write path for a truncate
// that lands inside a stored chunk.
func TestTruncateShortensFile(t *testing.T) {
	ctx := context.Background()
	fs, _, _, _ := newTestFS(t)

	h := mustCreate(t, fs, fs.Root(), "trunc.txt")
	mustWrite(t, fs, h, 0, bytes.Repeat([]byte("abcd"), 2000)) // 8000 bytes
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	size := uint64(5000)
	if _, err := fs.SetAttr(ctx, testCaller, h, vfs.SetAttr{Size: &size}); err != nil {
		t.Fatal(err)
	}
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	got := readAll(t, fs, h, 5000)
	if len(got) != 5000 {
		t.Fatalf("after truncate read %d bytes, want 5000", len(got))
	}
	want := bytes.Repeat([]byte("abcd"), 2000)[:5000]
	if !bytes.Equal(got, want) {
		t.Error("truncated contents do not match the original prefix")
	}
}

// TestOverwriteMiddle checks partial writes into existing chunks.
func TestOverwriteMiddle(t *testing.T) {
	ctx := context.Background()
	fs, _, _, _ := newTestFS(t)

	h := mustCreate(t, fs, fs.Root(), "patch.bin")
	base := bytes.Repeat([]byte("."), 10000)
	mustWrite(t, fs, h, 0, base)
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	mustWrite(t, fs, h, 5000, []byte("PATCHED"))
	if err := fs.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	want := append([]byte(nil), base...)
	copy(want[5000:], "PATCHED")
	if got := readAll(t, fs, h, 10000); !bytes.Equal(got, want) {
		t.Error("in-place patch did not land correctly")
	}
}

// TestRenameAndRmdir covers the namespace edge cases that are easy to get wrong.
func TestRenameAndRmdir(t *testing.T) {
	ctx := context.Background()
	fs, _, _, _ := newTestFS(t)
	root := fs.Root()

	a, _, err := fs.Mkdir(ctx, testCaller, root, "a", vfs.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := fs.Mkdir(ctx, testCaller, root, "b", vfs.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, fs, a, "file.txt")

	// A non-empty directory cannot be removed.
	if err := fs.Rmdir(ctx, testCaller, root, "a"); !errors.Is(err, vfs.ErrNotEmpty) {
		t.Errorf("rmdir on non-empty dir = %v, want ErrNotEmpty", err)
	}

	// Moving a directory into its own subtree must be refused.
	if err := fs.Rename(ctx, testCaller, root, "a", a, "self"); !errors.Is(err, vfs.ErrInval) {
		t.Errorf("rename into own subtree = %v, want ErrInval", err)
	}

	if err := fs.Rename(ctx, testCaller, a, "file.txt", b, "moved.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, _, err := fs.Lookup(ctx, testCaller, a, "file.txt"); !errors.Is(err, vfs.ErrNoEnt) {
		t.Errorf("source still present after rename: %v", err)
	}
	if _, _, err := fs.Lookup(ctx, testCaller, b, "moved.txt"); err != nil {
		t.Errorf("target missing after rename: %v", err)
	}
	if err := fs.Rmdir(ctx, testCaller, root, "a"); err != nil {
		t.Errorf("rmdir on now-empty dir: %v", err)
	}
}

// TestStaleHandle checks that a handle to a deleted file reports ESTALE rather
// than addressing whatever inode number gets reused.
func TestStaleHandle(t *testing.T) {
	ctx := context.Background()
	fs, _, _, _ := newTestFS(t)

	h := mustCreate(t, fs, fs.Root(), "doomed.txt")
	if err := fs.Remove(ctx, testCaller, fs.Root(), "doomed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.GetAttr(ctx, h); !errors.Is(err, vfs.ErrStale) {
		t.Errorf("getattr on removed file = %v, want ErrStale", err)
	}
}

// TestSecondWriterIsDetected checks the compare-and-swap that protects the
// metadata bucket from two mounts silently overwriting each other.
func TestSecondWriterIsDetected(t *testing.T) {
	ctx := context.Background()
	fsA, data, meta, _ := newTestFS(t)

	fsB, err := New(ctx, Config{Data: data, Meta: meta, ChunkSize: 4096, Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}

	// A commits, moving the root pointer out from under B.
	mustCreate(t, fsA, fsA.Root(), "from-a.txt")
	if err := fsA.Sync(ctx); err != nil {
		t.Fatalf("A commit: %v", err)
	}

	mustCreate(t, fsB, fsB.Root(), "from-b.txt")
	if err := fsB.Sync(ctx); err == nil {
		t.Error("expected B's commit to be refused after A moved the root pointer")
	}

	// B must now refuse further writes rather than corrupt the namespace.
	if _, _, err := fsB.Create(ctx, testCaller, fsB.Root(), "another.txt", vfs.SetAttr{}, true); err == nil {
		t.Error("expected diverged filesystem to refuse writes")
	}

	// A is unaffected and its file survives a remount.
	fsC, err := New(ctx, Config{Data: data, Meta: meta, ChunkSize: 4096, Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fsC.Lookup(ctx, testCaller, fsC.Root(), "from-a.txt"); err != nil {
		t.Errorf("A's committed file missing after remount: %v", err)
	}
}

// TestPermissionsEnforced checks that mode bits actually gate access.
func TestPermissionsEnforced(t *testing.T) {
	ctx := context.Background()
	fs, _, _, _ := newTestFS(t)

	mode := uint32(0o600)
	h, _, err := fs.Create(ctx, testCaller, fs.Root(), "private.txt", vfs.SetAttr{Mode: &mode}, true)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, fs, h, 0, []byte("owner only"))

	other := vfs.Caller{UID: 999, GID: 999}
	if _, _, err := fs.Read(ctx, other, h, 0, 10); !errors.Is(err, vfs.ErrAcces) {
		t.Errorf("read by another user = %v, want ErrAcces", err)
	}
	if _, _, err := fs.Write(ctx, other, h, 0, []byte("nope"), vfs.Unstable); !errors.Is(err, vfs.ErrAcces) {
		t.Errorf("write by another user = %v, want ErrAcces", err)
	}
	// Root bypasses the check.
	if _, _, err := fs.Read(ctx, vfs.Caller{UID: 0}, h, 0, 10); err != nil {
		t.Errorf("root read = %v, want success", err)
	}
}

// TestReadDirPaging walks a directory larger than one reply.
func TestReadDirPaging(t *testing.T) {
	ctx := context.Background()
	fs, _, _, _ := newTestFS(t)

	const n = 50
	for i := 0; i < n; i++ {
		mustCreate(t, fs, fs.Root(), string(rune('a'+i%26))+string(rune('a'+i/26))+".txt")
	}

	seen := map[string]bool{}
	var cookie uint64
	var verf [8]byte
	for iter := 0; iter < 100; iter++ {
		entries, eof, outVerf, err := fs.ReadDir(ctx, testCaller, fs.Root(), cookie, verf, 512, false)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		verf = outVerf
		for _, e := range entries {
			if e.Name == "." || e.Name == ".." {
				continue
			}
			if seen[e.Name] {
				t.Errorf("entry %q returned twice", e.Name)
			}
			seen[e.Name] = true
			cookie = e.Cookie
		}
		if eof {
			break
		}
		if len(entries) == 0 {
			t.Fatal("readdir made no progress")
		}
	}
	if len(seen) != n {
		t.Errorf("walked %d entries, want %d", len(seen), n)
	}
}

// TestStaleCookieRejected checks the readdir verifier.
func TestStaleCookieRejected(t *testing.T) {
	ctx := context.Background()
	fs, _, _, _ := newTestFS(t)
	for i := 0; i < 10; i++ {
		mustCreate(t, fs, fs.Root(), string(rune('a'+i))+".txt")
	}

	entries, _, verf, err := fs.ReadDir(ctx, testCaller, fs.Root(), 0, [8]byte{}, 256, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries")
	}
	cookie := entries[len(entries)-1].Cookie

	// Change the directory, invalidating the outstanding cookie.
	mustCreate(t, fs, fs.Root(), "zzz-new.txt")

	if _, _, _, err := fs.ReadDir(ctx, testCaller, fs.Root(), cookie, verf, 256, false); !errors.Is(err, vfs.ErrBadCookie) {
		t.Errorf("readdir with stale cookie = %v, want ErrBadCookie", err)
	}
}

// TestSymlink round-trips a symlink.
func TestSymlink(t *testing.T) {
	ctx := context.Background()
	fs, _, _, _ := newTestFS(t)

	h, attr, err := fs.Symlink(ctx, testCaller, fs.Root(), "link", "../target/path", vfs.SetAttr{})
	if err != nil {
		t.Fatal(err)
	}
	if attr.Type != vfs.TypeLnk {
		t.Errorf("type = %v, want symlink", attr.Type)
	}
	target, err := fs.ReadLink(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	if target != "../target/path" {
		t.Errorf("readlink = %q", target)
	}
}

// copyTree duplicates a directory, standing in for handing someone a copy of
// the metadata bucket.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

var _ = time.Now

// TestSnapshotsArePruned checks that a long-running mount does not fill the
// metadata bucket with superseded namespace snapshots. A live smoke test
// produced 440 snapshots in one run before retention existed.
func TestSnapshotsArePruned(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	data, err := store.NewLocal(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.NewLocal(filepath.Join(dir, "meta"))
	if err != nil {
		t.Fatal(err)
	}
	fs, err := New(ctx, Config{
		Data: data, Meta: meta,
		ChunkSize:         4096,
		SnapshotRetention: 5,
		OwnerUID:          501,
		OwnerGID:          20,
		Log:               quietLog(),
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 40; i++ {
		mustCreate(t, fs, fs.Root(), fmt.Sprintf("file-%02d.txt", i))
		if err := fs.Sync(ctx); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	// Pruning runs in the background and skips while a pass is already in
	// flight, so the guarantee is that snapshots stay bounded near the
	// retention count, not that they land on it exactly. What matters is that
	// 40 commits do not leave 40 snapshots.
	const bound = 12
	var snaps []store.ObjectInfo
	for attempt := 0; attempt < 60; attempt++ {
		snaps, err = meta.List(ctx, snapshotPrefix, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(snaps) <= bound {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(snaps) > bound {
		t.Errorf("after 40 commits the bucket holds %d snapshots, want at most %d", len(snaps), bound)
	}
	if len(snaps) == 0 {
		t.Fatal("pruning removed every snapshot")
	}

	// The namespace must still be intact and mountable from what survived.
	fs2, err := New(ctx, Config{Data: data, Meta: meta, ChunkSize: 4096, Log: quietLog()})
	if err != nil {
		t.Fatalf("remount after pruning: %v", err)
	}
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("file-%02d.txt", i)
		if _, _, err := fs2.Lookup(ctx, testCaller, fs2.Root(), name); err != nil {
			t.Fatalf("%s missing after pruning: %v", name, err)
		}
	}
}
