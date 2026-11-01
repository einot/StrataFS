package blobfs

// Clean-room tests for #105, whose done-when is that an unverified fetch does
// not mark its hash known, written without reading the implementation, from the
// issue's text, ADR 0004 section 1 (who caches a chunk, and that
// SkipChunkVerification leaves a fetched chunk unchecked), and
// internal/vfs/vfs.go.
//
// The rule: a hash is known to a mount only when the mount has evidence that
// the bucket holds that hash's bytes. A fetch it verified is such evidence. A
// fetch it did not verify is not, since the bytes may not match the hash, so it
// must not spare a later write of those bytes from asking the store. Storing
// the hash itself, or a Head that found it, still counts on a mount with
// verification off.
//
// What the tests observe is the store's Head of the chunk's key, which the
// harness records, beside FS.isKnown. A Head is what a write of bytes whose hash
// is not known makes, and a write of a known hash makes none.
//
// Not covered, on purpose:
//   - that a corrupt object gets repaired. The Head still finds the object and
//     the upload is still skipped, which is #30's separate defect, so nothing
//     here asserts on a Put of the chunk's key;
//   - whether an unverified chunk enters the chunk cache.
//
// Every fetch is therefore made by a fresh mount: the seeding mount cached the
// chunk when it stored it (ADR 0004 section 1), and a read served by a cache
// hit never reaches the store, so it would test nothing. A fresh mount shares
// only the bucket with the mount that wrote it, and its first read of the chunk
// must come from the store, which uk105FetchOnFreshMount requires of the
// records rather than assumes.
//
// Every write of the bytes goes to a new file rather than over the file that
// holds them, so that the write is of a whole chunk and owes nothing to what
// the file held before.
//
// The safety rules are ADR 0011 section 7's as ADR 0023 section 11 applies them:
// a *store.Local under t.TempDir() behind an unarmed uc43Store; every New and
// every FS call in uc43Go, waited for under uc43Bound; no time.Sleep, no polling
// and no t.Parallel. The bucket is inspected through the inner store only after
// the harness's records have been read.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

// uk105Chunk returns the bytes every test here writes: exactly one chunk of
// uc43Config's ChunkSize, 4096 bytes (16 times 256), and not all zero, so that
// a file holding them is one whole chunk and no hole.
func uk105Chunk() []byte {
	return bytes.Repeat([]byte("unverified-known"), 256)
}

// uk105Hash returns the hash of uk105Chunk: the lowercase hexadecimal SHA-256
// of its bytes.
func uk105Hash() string {
	sum := sha256.Sum256(uk105Chunk())
	return hex.EncodeToString(sum[:])
}

// uk105Key returns the key of uk105Chunk's object in the store.
func uk105Key() string {
	return chunkPrefix + uk105Hash()
}

// uk105IsHead selects a Head of uk105Chunk's key.
func uk105IsHead(c uc43Call) bool {
	return c.op == "Head" && c.key == uk105Key()
}

// uk105IsFetch selects a successful read of uk105Chunk's object from the store.
func uk105IsFetch(c uc43Call) bool {
	if c.key != uk105Key() || c.err != nil {
		return false
	}
	return c.op == "Get" || c.op == "GetRange" || c.op == "GetWithETag"
}

// uk105Known returns fs.isKnown of uk105Chunk's hash.
func uk105Known(e *uc43Env, fs *FS, where string) bool {
	e.t.Helper()
	hash := uk105Hash()
	return uc43Go(e, func() bool { return fs.isKnown(hash) }).wait(e.t, "isKnown of the chunk's hash", where)
}

// uk105Mount runs New on the harness, with SkipChunkVerification as given and
// the rest of the Config uc43Config's; it must succeed.
func uk105Mount(e *uc43Env, skipVerification bool, where string) *FS {
	e.t.Helper()
	ctx, cfg := e.ctx, uc43Config(e.h)
	cfg.SkipChunkVerification = skipVerification
	what := fmt.Sprintf("New on the harness with SkipChunkVerification %v", skipVerification)
	r := uc43Go(e, func() uc43R[*FS] {
		fs, err := New(ctx, cfg)
		return uc43R[*FS]{fs, err}
	}).wait(e.t, what, where)
	if r.err != nil {
		e.t.Fatalf("%s = %v, want success (%s)", what, r.err, where)
	}
	if r.v == nil {
		e.t.Fatalf("%s returned a nil *FS and a nil error (%s)", what, where)
	}
	return r.v
}

// uk105WriteAndSync creates name in fs's root directory, writes uk105Chunk to
// it and commits with Sync, which must succeed. It returns every store call
// made from before the Create to after the Sync, so that a Head counts wherever
// in that span the mount makes it.
func uk105WriteAndSync(e *uc43Env, fs *FS, name, where string) []uc43Call {
	e.t.Helper()
	mark := len(e.h.records())
	h := e.create(fs, e.root(fs, where), name, where)
	e.write(fs, h, 0, uk105Chunk(), fmt.Sprintf("Write of the chunk's bytes to %q", name), where)
	what := fmt.Sprintf("Sync of the write to %q", name)
	if err := e.sync(fs, what, where); err != nil {
		e.t.Fatalf("%s = %v, want nil: nothing here makes a commit fail (%s)", what, err, where)
	}
	return e.h.records()[mark:]
}

// uk105FetchOnFreshMount stores uk105Chunk through one mount, makes a fresh
// mount with SkipChunkVerification as given, and has it read the chunk. It
// requires what the tests stand on: the fresh mount does not know the hash
// before the read, and the read returned the chunk's bytes from the store. It
// returns the fresh mount.
func uk105FetchOnFreshMount(e *uc43Env, skipVerification bool, where string) *FS {
	e.t.Helper()
	const name = "seed"
	uk105WriteAndSync(e, e.newFS(where), name, where)

	fs := uk105Mount(e, skipVerification, where)
	if uk105Known(e, fs, where) {
		e.t.Fatalf("a fresh mount knows the chunk's hash before it has read the chunk, want it not known: the test "+
			"rests on the read being the first evidence this mount has of the hash (#105; %s)", where)
	}

	mark := len(e.h.records())
	l := e.lookup(fs, e.root(fs, where), name, where)
	if l.err != nil {
		e.t.Fatalf("Lookup of %q on the fresh mount = %v, want success: the seeding mount committed it (%s)",
			name, l.err, where)
	}
	got, err := e.readFile(fs, l.h, fmt.Sprintf("Read of %q on the fresh mount", name), where)
	if err != nil {
		e.t.Fatalf("Read of %q on the fresh mount = %v, want success: the object in the bucket is intact (%s)",
			name, err, where)
	}
	if !bytes.Equal(got, uk105Chunk()) {
		e.t.Fatalf("Read of %q on the fresh mount returned %d bytes that are not the %d written (%s)",
			name, len(got), len(uk105Chunk()), where)
	}
	calls := e.h.records()[mark:]
	if len(uc43Filter(calls, uk105IsFetch)) == 0 {
		e.t.Fatalf("the fresh mount's Read made no successful read of %q from the store, want one: it shares only the "+
			"bucket with the mount that wrote the chunk, and a test of what a fetch marks needs a fetch (%s); store "+
			"calls made: %v", uk105Key(), where, calls)
	}
	return fs
}

// TestUnverifiedFetchDoesNotMarkItsHashKnown pins #105's done-when: a mount with
// SkipChunkVerification that fetches a chunk from the store has not marked the
// chunk's hash known, since nothing checked the bytes against the hash, so a
// later write of the same bytes, committed by Sync, makes a Head of the chunk's
// key.
func TestUnverifiedFetchDoesNotMarkItsHashKnown(t *testing.T) {
	const where = "#105: an unverified fetch is no evidence that the bucket holds the hash's bytes"
	e := uc43Setup(t)
	fs := uk105FetchOnFreshMount(e, true, where)

	if uk105Known(e, fs, where) {
		t.Errorf("after a fetch with SkipChunkVerification, isKnown of the chunk's hash = true, want false: the fetch "+
			"did not verify the bytes, so it must not mark the hash known (%s)", where)
	}

	calls := uk105WriteAndSync(e, fs, "after-fetch", where)
	if len(uc43Filter(calls, uk105IsHead)) == 0 {
		t.Errorf("a write of the fetched bytes, committed by Sync, made no Head of %q, want at least one: the hash "+
			"came only from an unverified fetch, so the mount must ask the store about it rather than treat it as "+
			"known (%s); store calls made: %v", uk105Key(), where, calls)
	}
}

// TestVerifiedFetchMarksItsHashKnown is the control for #105: the same sequence
// on a mount with verification on. Its fetch checked the bytes against the hash,
// so the hash is known and the same later write makes no Head of the chunk's
// key. It keeps the fix for #105 scoped to the unverified path.
func TestVerifiedFetchMarksItsHashKnown(t *testing.T) {
	const where = "#105's control: a verified fetch is evidence that the bucket holds the hash's bytes"
	e := uc43Setup(t)
	fs := uk105FetchOnFreshMount(e, false, where)

	if !uk105Known(e, fs, where) {
		t.Errorf("after a verified fetch, isKnown of the chunk's hash = false, want true: the fetch checked the bytes "+
			"against the hash, so the mount knows the bucket holds them (%s)", where)
	}

	calls := uk105WriteAndSync(e, fs, "after-fetch", where)
	if heads := uc43Filter(calls, uk105IsHead); len(heads) != 0 {
		t.Errorf("a write of the fetched bytes, committed by Sync, made the Heads %v of %q, want none: the verified "+
			"fetch marked the hash known, and a known hash needs no question to the store (%s)",
			heads, uk105Key(), where)
	}
}

// TestUnverifiedMountKnowsAHashItsHeadFound pins the part of #105's behaviour
// that a fix must keep: with verification off, a Head that found the object is
// still evidence. After the write and Sync of
// TestUnverifiedFetchDoesNotMarkItsHashKnown, whose Head found the object, a
// further write of the same bytes makes no further Head of the chunk's key.
//
// It requires the first write to have made that Head, so that it cannot pass
// merely because the unverified fetch marked the hash known.
func TestUnverifiedMountKnowsAHashItsHeadFound(t *testing.T) {
	const where = "#105: with verification off, a Head that found the object still marks its hash known"
	e := uc43Setup(t)
	fs := uk105FetchOnFreshMount(e, true, where)

	first := uk105WriteAndSync(e, fs, "after-fetch", where)
	heads := uc43Filter(first, uk105IsHead)
	if len(heads) == 0 {
		t.Fatalf("setup: the first write of the fetched bytes made no Head of %q, want one: without it this test "+
			"would pass with the hash marked known by the unverified fetch, which is #105's defect and not what it "+
			"pins (%s); store calls made: %v", uk105Key(), where, first)
	}
	if heads[len(heads)-1].err != nil {
		t.Fatalf("setup: the first write's Head of %q returned %v, want nil: the seeding mount stored the object, so "+
			"the Head must find it (%s)", uk105Key(), heads[len(heads)-1].err, where)
	}
	if !uk105Known(e, fs, where) {
		t.Errorf("after a Head that found the object, isKnown of the chunk's hash = false, want true: the store "+
			"answered for the hash (%s)", where)
	}

	second := uk105WriteAndSync(e, fs, "after-head", where)
	if again := uc43Filter(second, uk105IsHead); len(again) != 0 {
		t.Errorf("a further write of the same bytes made the Heads %v of %q, want none: the first write's Head found "+
			"the object, so the hash is known and the store need not be asked twice (%s)", again, uk105Key(), where)
	}
}

// TestUnverifiedMountKnowsAHashItStored pins the other part of #105's behaviour
// that a fix must keep: with verification off, a mount that stored a hash itself
// knows it, with no fetch involved, and a further write of the same bytes makes
// no Head of the chunk's key.
func TestUnverifiedMountKnowsAHashItStored(t *testing.T) {
	const where = "#105: with verification off, a hash the mount stored itself is known"
	e := uc43Setup(t)
	fs := uk105Mount(e, true, where)

	if uk105Known(e, fs, where) {
		t.Fatalf("a mount over an empty bucket knows the chunk's hash before anything wrote it, want it not known: "+
			"the test rests on the write being the mount's first evidence of the hash (%s)", where)
	}

	uk105WriteAndSync(e, fs, "first", where)
	stored, err := e.innerGet(uk105Key(), where)
	if err != nil {
		t.Fatalf("after the commit, Get(%q) on the inner store = %v, want the chunk: the bucket was empty, so only "+
			"this mount can have stored it, and the test rests on its having done so (%s)", uk105Key(), err, where)
	}
	if !bytes.Equal(stored, uk105Chunk()) {
		t.Fatalf("after the commit, the object at %q is %d bytes that are not the %d written, want the chunk: its key "+
			"is the hash of its bytes (%s)", uk105Key(), len(stored), len(uk105Chunk()), where)
	}
	if !uk105Known(e, fs, where) {
		t.Errorf("after the mount stored the chunk, isKnown of its hash = false, want true: the mount put those bytes "+
			"there itself, whatever SkipChunkVerification says about what it fetches (%s)", where)
	}

	second := uk105WriteAndSync(e, fs, "second", where)
	if heads := uc43Filter(second, uk105IsHead); len(heads) != 0 {
		t.Errorf("a further write of the same bytes made the Heads %v of %q, want none: the mount stored the hash "+
			"itself, so it is known and the store need not be asked (%s)", heads, uk105Key(), where)
	}
}
