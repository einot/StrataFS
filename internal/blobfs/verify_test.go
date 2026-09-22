package blobfs

// Chunk verification on read (issue #3).
//
// A chunk's key is the SHA-256 of its bytes, so the store hands back something
// that can be checked against the name it was fetched under for the cost of one
// hash of data already in memory. These tests pin that the check happens, that
// it happens *before* anything is cached, and that the escape hatch that turns
// it off is real. They complement TestWriteReadRemount in fs_test.go, which
// covers the uncorrupted path.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"

	"strata/internal/store"
	"strata/internal/vfs"
)

// hexDigest matches a bare 64-character hex string, which is how both the
// expected and the received hash appear in a verification failure message.
var hexDigest = regexp.MustCompile(`[0-9a-fA-F]{64}`)

// corruptFixture is a bucket holding one committed single-chunk file whose
// chunk object has been overwritten with different bytes of the same length.
// Same length matters: it keeps the file's size and every chunk boundary
// unchanged, so the only thing wrong with the filesystem is the chunk's
// contents.
type corruptFixture struct {
	st      *store.Local
	name    string // the file's name in the root directory
	key     string // full object key of the corrupted chunk
	hash    string // the 64-hex digest that key names, i.e. the expected hash
	payload []byte // the file's true contents
	orig    []byte // the correct bytes of the corrupted chunk
	bad     []byte // the bytes now sitting at key
}

// newCorruptFixture writes a file whose contents are exactly one stored chunk,
// commits it, and then corrupts that chunk in the bucket behind the
// filesystem's back — standing in for bit rot, a truncated transfer that still
// returned 200, or a misbehaving cache in front of the cluster.
func newCorruptFixture(t *testing.T) corruptFixture {
	t.Helper()
	ctx := context.Background()
	fs, st, _ := newTestFS(t)

	// 4096 bytes at a 4096-byte chunk size: exactly one stored chunk, so a read
	// anywhere in the file must touch the chunk we corrupt.
	payload := bytes.Repeat([]byte("strata!"), 586)[:4096]
	const name = "victim.bin"
	h := mustCreate(t, fs, fs.Root(), name)
	mustWrite(t, fs, h, 0, payload)
	if err := fs.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	chunks, err := st.List(ctx, chunkPrefix, "", 0)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected exactly one stored chunk, got %d", len(chunks))
	}
	key := chunks[0].Key
	hash := strings.TrimPrefix(key, chunkPrefix)
	if len(hash) != 64 {
		t.Fatalf("chunk key %q is not %q plus a sha256 digest", key, chunkPrefix)
	}

	orig, err := st.Get(ctx, key)
	if err != nil {
		t.Fatalf("get chunk %q: %v", key, err)
	}
	if len(orig) == 0 {
		t.Fatalf("chunk %q is empty", key)
	}

	// Flip every bit: same length, guaranteed different contents, and therefore
	// a different hash from the one the key names.
	bad := make([]byte, len(orig))
	for i, b := range orig {
		bad[i] = ^b
	}
	if err := st.Put(ctx, key, bad); err != nil {
		t.Fatalf("corrupt chunk %q: %v", key, err)
	}

	return corruptFixture{st: st, name: name, key: key, hash: hash, payload: payload, orig: orig, bad: bad}
}

// mountAndOpen mounts a fresh filesystem over the fixture's bucket and looks up
// the file. Freshness is the point: a filesystem that just wrote a chunk still
// holds it in memory, so only a mount that shares nothing but the bucket
// actually exercises the fetch-and-verify path.
func (f corruptFixture) mountAndOpen(t *testing.T, skipVerification bool) (*FS, vfs.Handle) {
	t.Helper()
	ctx := context.Background()
	fs, err := New(ctx, Config{
		Store:                 f.st,
		ChunkSize:             4096,
		SkipChunkVerification: skipVerification,
		Log:                   quietLog(),
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	h, _, err := fs.Lookup(ctx, testCaller, fs.Root(), f.name)
	if err != nil {
		t.Fatalf("lookup %s: %v", f.name, err)
	}
	return fs, h
}

// TestCorruptChunkIsRefused pins the central promise of issue #3: a chunk whose
// contents do not match the hash in its key is never delivered to the
// application. Without verification the corrupt bytes are returned as though
// they were the file's contents, which is the silent-corruption failure mode
// content addressing lets us rule out for free. The error must be loud and
// specific: ErrIO, naming both the hash the chunk was fetched under and the
// hash the returned bytes actually have, so an operator can identify the
// damaged object.
func TestCorruptChunkIsRefused(t *testing.T) {
	ctx := context.Background()
	f := newCorruptFixture(t)
	fs, h := f.mountAndOpen(t, false)

	data, _, err := fs.Read(ctx, testCaller, h, 0, uint32(len(f.payload)))
	if err == nil {
		t.Fatalf("read of a corrupt chunk succeeded, returning %d bytes", len(data))
	}
	if !errors.Is(err, vfs.ErrIO) {
		t.Errorf("read of a corrupt chunk = %v, want vfs.ErrIO", err)
	}
	if len(data) != 0 {
		t.Errorf("read returned %d bytes alongside the error; corrupt data must never reach the caller", len(data))
	}

	// The message must name the chunk and both hashes.
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), strings.ToLower(f.hash)) {
		t.Errorf("error %q does not name the chunk's own hash %s", msg, f.hash)
	}
	got := sha256.Sum256(f.bad)
	gotHash := hex.EncodeToString(got[:])
	if !strings.Contains(strings.ToLower(msg), gotHash) {
		t.Errorf("error %q does not name the hash the received bytes actually have (%s)", msg, gotHash)
	}

	// Independently of which hash is which, the message must carry at least two
	// distinct digests: one identifying the chunk, one describing what came back.
	seen := map[string]bool{}
	for _, m := range hexDigest.FindAllString(msg, -1) {
		seen[strings.ToLower(m)] = true
	}
	if !seen[strings.ToLower(f.hash)] {
		t.Errorf("error %q contains no digest equal to the chunk's hash", msg)
	}
	if len(seen) < 2 {
		t.Errorf("error %q names %d distinct sha256 digests, want both the expected and the received hash", msg, len(seen))
	}
}

// TestCorruptChunkIsNeverCached pins that verification happens before the chunk
// cache is populated. If bad bytes were cached on the way in, every later read
// of that chunk would be answered from memory — either failing forever or, if
// the cache is trusted on hit, serving the corruption the first read refused.
// Repairing the object in the bucket must be enough to make the filesystem
// healthy again, with no remount.
func TestCorruptChunkIsNeverCached(t *testing.T) {
	ctx := context.Background()
	f := newCorruptFixture(t)
	fs, h := f.mountAndOpen(t, false)

	// First read fails, and is the read that would have populated the cache.
	if _, _, err := fs.Read(ctx, testCaller, h, 0, uint32(len(f.payload))); !errors.Is(err, vfs.ErrIO) {
		t.Fatalf("read of a corrupt chunk = %v, want vfs.ErrIO", err)
	}

	// Repair the object. The chunk's bytes now match its key again.
	if err := f.st.Put(ctx, f.key, f.orig); err != nil {
		t.Fatalf("restore chunk %q: %v", f.key, err)
	}

	// Same filesystem instance, no remount: the repaired chunk must be fetched
	// afresh and the file must read back byte for byte.
	if got := readAll(t, fs, h, len(f.payload)); !bytes.Equal(got, f.payload) {
		t.Errorf("after repairing the chunk the file read back wrong: got %d bytes, want the original %d", len(got), len(f.payload))
	}
}

// TestSkipChunkVerification pins the escape hatch. Verification defaults on —
// every other test here relies on that — but a bucket whose chunks are known to
// be corrupt is exactly the situation where an operator may need to read what
// is left, so the check must be switchable off. Getting the corrupt bytes back
// here also proves the negative the other two tests rest on: the ErrIO above is
// produced by verification and not by some unrelated failure to read a modified
// object.
func TestSkipChunkVerification(t *testing.T) {
	ctx := context.Background()
	f := newCorruptFixture(t)
	fs, h := f.mountAndOpen(t, true)

	data, _, err := fs.Read(ctx, testCaller, h, 0, uint32(len(f.payload)))
	if err != nil {
		t.Fatalf("read with verification disabled = %v, want success", err)
	}
	if len(data) != len(f.bad) {
		t.Fatalf("read %d bytes, want %d", len(data), len(f.bad))
	}
	if !bytes.Equal(data, f.bad) {
		t.Error("with verification disabled the read must return the bytes actually stored, unchecked")
	}
	if bytes.Equal(data, f.payload) {
		t.Error("read returned the original payload; the chunk in the bucket is corrupt, so this test is not exercising what it claims")
	}
}
