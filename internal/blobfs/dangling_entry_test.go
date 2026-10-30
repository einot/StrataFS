package blobfs

// Clean-room tests for #144, "a REMOVE of an entry that names no inode drops it
// without marking the namespace dirty", written without reading the
// implementation, from ADR 0024
// (docs/adr/0024-every-namespace-change-is-marked-dirty-and-a-refused-setattr-makes-none.md)
// sections 1, 3 and 7, ADR 0017 section 2 (Rename replaces an entry that names
// no inode), the facts and harness of ADR 0023 section 11 and ADR 0022 section
// 8, the safety rules of ADR 0011 section 7, and internal/vfs/vfs.go.
//
// ADR 0024 section 3's rule: Remove and Rmdir of a name whose directory entry
// names an inode the table lacks return vfs.ErrNoEnt and change nothing: no
// delete of the entry, no touch of the directory's mtime, no markDirty. The
// entry stays until a Rename onto the name replaces it, which is marked dirty
// and committed (ADR 0017 section 2), and is the one way a client has to make
// the name usable again.
//
// Covered, with the names section 7 pins:
//   - D1, TestRemoveOfAnEntryThatNamesNoInodeChangesNothing (#144's done-when
//     for choice (b));
//   - D2, TestARenameOntoAnEntryThatNamesNoInodeReplacesIt (a
//     characterisation of the recovery path).
//
// No operation in this build makes such an entry, so each test writes one, by
// section 7's recipe (dg24Doctor): on a fresh mount testCaller creates ghost
// and Syncs; the snapshot the root pointer names is read from the
// *store.Local, decoded with decodeSnapshot, ghost's inode deleted from the
// table while its entry in the root directory stays, encoded with
// encodeSnapshot and put back under the same key, so that the root pointer and
// its ETag are unchanged; the first *FS is dropped and never used again (ADR
// 0011 section 7); and the store is mounted afresh behind the harness.
//
// Both tests run on ADR 0023 section 11's harness, uc43Store, unarmed, as a
// pass-through recorder of every store call. The safety rules (ADR 0023
// section 11; ADR 0011 section 7): every bucket is under the test's
// t.TempDir(); every New, every FS call and the doctoring run in uc43Go, which
// recovers a panic, and are waited for under uc43Bound, 10 s; nothing sleeps
// or polls, and nothing is parallel; FS.dirty and FS.changes are read only
// holding FS.mu for reading, inside uc43Go, and no field is written; the
// bucket is written to directly only by the doctoring, before the mount under
// test exists.
//
// Not covered, on purpose: Mkdir, Symlink and a non-exclusive Create of such a
// name, and a Rename from it, which section 3's table restates as unchanged
// and section 7 does not ask for; whether ReadDir should hash into its
// verifier a name it does not list (What this does not decide).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"strata/internal/store"
	"strata/internal/vfs"
)

// dg24Ghost is the name whose entry, after the doctoring, names no inode.
const dg24Ghost = "ghost"

// dg24Named reads the root pointer from local, decodes it as rootPointer, and
// returns it with the decoded snapshot it names. It touches no *testing.T.
func dg24Named(ctx context.Context, local *store.Local) (rootPointer, *snapshot, error) {
	raw, err := local.Get(ctx, rootKey)
	if err != nil {
		return rootPointer{}, nil, fmt.Errorf("Get(%q): %w", rootKey, err)
	}
	var rp rootPointer
	if err := json.Unmarshal(raw, &rp); err != nil {
		return rootPointer{}, nil, fmt.Errorf("decoding the root pointer %q as rootPointer: %w", raw, err)
	}
	body, err := local.Get(ctx, rp.Snapshot)
	if err != nil {
		return rootPointer{}, nil, fmt.Errorf("Get(%q), the snapshot the root pointer names: %w", rp.Snapshot, err)
	}
	snap, err := decodeSnapshot(body)
	if err != nil {
		return rootPointer{}, nil, fmt.Errorf("decodeSnapshot of %q: %w", rp.Snapshot, err)
	}
	if snap == nil || snap.Inodes[rootInodeID] == nil {
		return rootPointer{}, nil, fmt.Errorf("the snapshot %q has no root directory, Inodes[%d]", rp.Snapshot,
			rootInodeID)
	}
	return rp, snap, nil
}

// dg24Doctor is ADR 0024 section 7's recipe for a snapshot holding an entry
// that names no inode: it deletes the inode the root directory's entry name
// names from the snapshot the root pointer names, keeps the entry, and puts
// the re-encoded snapshot back under the same key, leaving the root pointer
// as it is. It returns the deleted inode's number. It touches no *testing.T.
func dg24Doctor(ctx context.Context, local *store.Local, name string) (uint64, error) {
	rp, snap, err := dg24Named(ctx, local)
	if err != nil {
		return 0, err
	}
	id, ok := snap.Inodes[rootInodeID].Entries[name]
	if !ok {
		return 0, fmt.Errorf("the root directory in %q has no entry %q", rp.Snapshot, name)
	}
	if _, ok := snap.Inodes[id]; !ok {
		return 0, fmt.Errorf("the entry %q in %q already names no inode (%d)", name, rp.Snapshot, id)
	}
	delete(snap.Inodes, id)
	body, err := encodeSnapshot(snap)
	if err != nil {
		return 0, fmt.Errorf("encodeSnapshot: %w", err)
	}
	if err := local.Put(ctx, rp.Snapshot, body); err != nil {
		return 0, fmt.Errorf("Put(%q): %w", rp.Snapshot, err)
	}
	return id, nil
}

// dg24Env is the mount under test: New, behind the harness, on a bucket whose
// root directory holds the entry ghost, naming no inode.
type dg24Env struct {
	e    *uc43Env
	fs   *FS
	root vfs.Handle
}

// dg24Setup builds the doctored snapshot as ADR 0024 section 7 gives it and
// mounts it behind the harness.
func dg24Setup(t *testing.T, where string) *dg24Env {
	t.Helper()
	e := uc43Setup(t)
	func() {
		// The first mount lives only in this scope, so that nothing uses it
		// after the doctoring (ADR 0011 section 7).
		first := e.newFS(where + ", the first mount")
		e.create(first, e.root(first, where), dg24Ghost, where)
		if err := e.sync(first, fmt.Sprintf("the Sync that commits %q on the first mount", dg24Ghost),
			where); err != nil {
			t.Fatalf("setup: the Sync that commits %q on the first mount = %v, want nil against a healthy store (%s)",
				dg24Ghost, err, where)
		}
	}()

	ctx, local := e.ctx, e.h.inner
	doc := uc43Go(e, func() uc43R[uint64] {
		id, err := dg24Doctor(ctx, local, dg24Ghost)
		return uc43R[uint64]{id, err}
	}).wait(t, "doctoring the snapshot the root pointer names", where)
	if doc.err != nil {
		t.Fatalf("setup: doctoring the snapshot the root pointer names (ADR 0024 section 7): %v (%s)", doc.err, where)
	}

	h := e.h
	r := uc43Go(e, func() uc43R[*FS] {
		fs, err := New(ctx, uc43Config(h))
		return uc43R[*FS]{fs, err}
	}).wait(t, "New on the doctored bucket, behind the harness", where)
	if r.err != nil {
		t.Fatalf("setup: New on the doctored bucket = %v, want success: the doctored snapshot decodes, since "+
			"decodeSnapshot checks only nil inodes and directory entry maps (ADR 0024 section 7, the facts; %s)",
			r.err, where)
	}
	if r.v == nil {
		t.Fatalf("setup: New on the doctored bucket returned a nil *FS and a nil error (%s)", where)
	}
	return &dg24Env{e: e, fs: r.v, root: e.root(r.v, where)}
}

// dg24Fields is what dg24Read reads of an FS.
type dg24Fields struct {
	dirty   bool
	changes uint64
}

// dg24Read reads FS.dirty and FS.changes in one hold of FS.mu for reading,
// inside uc43Go (ADR 0022 section 8; ADR 0023 section 11).
func dg24Read(e *uc43Env, fs *FS, what, where string) dg24Fields {
	e.t.Helper()
	return uc43Go(e, func() dg24Fields {
		fs.mu.RLock()
		defer fs.mu.RUnlock()
		return dg24Fields{dirty: fs.dirty, changes: fs.changes}
	}).wait(e.t, what, where)
}

// dg24GetAttr runs fs.GetAttr of h, which must succeed.
func dg24GetAttr(e *uc43Env, fs *FS, h vfs.Handle, what, where string) vfs.Attr {
	e.t.Helper()
	ctx := e.ctx
	r := uc43Go(e, func() uc43R[vfs.Attr] {
		a, err := fs.GetAttr(ctx, h)
		return uc43R[vfs.Attr]{a, err}
	}).wait(e.t, what, where)
	if r.err != nil {
		e.t.Fatalf("%s = %v, want success (%s)", what, r.err, where)
	}
	return r.v
}

// dg24Listing is what one ReadDir returned: the names, eof and the verifier.
type dg24Listing struct {
	names []string
	eof   bool
	verf  [8]byte
	err   error
}

// dg24ReadDir runs one ReadDir of dir, as testCaller, from cookie 0 with the
// zero verifier.
func dg24ReadDir(e *uc43Env, fs *FS, dir vfs.Handle, what, where string) dg24Listing {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() dg24Listing {
		ents, eof, verf, err := fs.ReadDir(ctx, testCaller, dir, 0, [8]byte{}, 1<<16, false)
		var names []string
		for _, de := range ents {
			names = append(names, de.Name)
		}
		return dg24Listing{names: names, eof: eof, verf: verf, err: err}
	}).wait(e.t, what, where)
}

// dg24Do runs fn in uc43Go and returns its error.
func dg24Do(e *uc43Env, what, where string, fn func(ctx context.Context) error) error {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() error { return fn(ctx) }).wait(e.t, what, where)
}

// dg24CreateExcl runs testCaller's exclusive Create of name in dir, with no
// attributes, and returns its error.
func dg24CreateExcl(e *uc43Env, fs *FS, dir vfs.Handle, name, what, where string) error {
	e.t.Helper()
	return dg24Do(e, what, where, func(ctx context.Context) error {
		_, _, err := fs.Create(ctx, testCaller, dir, name, vfs.SetAttr{}, true)
		return err
	})
}

// TestRemoveOfAnEntryThatNamesNoInodeChangesNothing is D1 of ADR 0024 section
// 7, #144's done-when for choice (b), and pins section 3's rule: Remove and
// Rmdir of a name whose entry names no inode return vfs.ErrNoEnt and change
// nothing.
//
// The doctored snapshot is built and mounted behind an unarmed uc43Store, and
// the harness's record marked. Lookup of ghost answers vfs.ErrNoEnt; ReadDir
// of the root from cookie 0 with the zero verifier lists no ghost and returns
// a verifier v. Remove of ghost and Rmdir of ghost each return vfs.ErrNoEnt.
// After both: FS.dirty is false and FS.changes is what it was after New;
// ReadDir returns v again; GetAttr of the root reports the mtime it reported
// before; an exclusive Create of ghost answers vfs.ErrExist; Sync returns nil
// and the harness recorded no call since the mark; and a remount of the inner
// store answers vfs.ErrNoEnt to Lookup of ghost and vfs.ErrExist to an
// exclusive Create of it.
//
// At f8398c4 the Remove drops the entry in memory without marking it: the
// verifier changes, and FS.dirty stays false; the exclusive Create then
// succeeds and marks the namespace dirty, so FS.dirty is true, the Sync
// commits, and the remount finds ghost. That is #144, and the repair this ADR
// declines (ADR 0024 section 3, "Why (b)").
func TestRemoveOfAnEntryThatNamesNoInodeChangesNothing(t *testing.T) {
	const where = "D1, ADR 0024 sections 3 and 7"
	s := dg24Setup(t, where)
	e, fs, root := s.e, s.fs, s.root
	mark := len(e.h.records())
	f0 := dg24Read(e, fs, "reading FS.dirty and FS.changes after New", where)
	if f0.dirty {
		t.Fatalf("setup: FS.dirty is true on a fresh mount that has changed nothing, want false (%s)", where)
	}

	if l := e.lookup(fs, root, dg24Ghost, where); !errors.Is(l.err, vfs.ErrNoEnt) {
		t.Fatalf("setup: Lookup of %q on the doctored mount = %v, want vfs.ErrNoEnt: the mount holds an entry that "+
			"names no inode (ADR 0024 section 3's table; section 7, the facts; %s)", dg24Ghost, l.err, where)
	}
	l1 := dg24ReadDir(e, fs, root, "ReadDir of the root directory before the Remove", where)
	if l1.err != nil || !l1.eof {
		t.Fatalf("setup: ReadDir of the root directory from cookie 0 = (%q, eof %v, %v), want the whole directory "+
			"in one reply with no error (%s)", l1.names, l1.eof, l1.err, where)
	}
	if slices.Contains(l1.names, dg24Ghost) {
		t.Errorf("ReadDir of the root directory lists %q (%q), want it not listed: an entry that names no inode is "+
			"skipped (ADR 0024 section 3's table; %s)", dg24Ghost, l1.names, where)
	}
	v := l1.verf
	rootBefore := dg24GetAttr(e, fs, root, "GetAttr of the root directory before the Remove", where)

	what := fmt.Sprintf("testCaller's Remove of %q", dg24Ghost)
	if err := dg24Do(e, what, where, func(ctx context.Context) error {
		return fs.Remove(ctx, testCaller, root, dg24Ghost)
	}); !errors.Is(err, vfs.ErrNoEnt) {
		t.Errorf("%s = %v, want vfs.ErrNoEnt (ADR 0024 section 3; %s)", what, err, where)
	}
	what = fmt.Sprintf("testCaller's Rmdir of %q", dg24Ghost)
	if err := dg24Do(e, what, where, func(ctx context.Context) error {
		return fs.Rmdir(ctx, testCaller, root, dg24Ghost)
	}); !errors.Is(err, vfs.ErrNoEnt) {
		t.Errorf("%s = %v, want vfs.ErrNoEnt (ADR 0024 section 3; %s)", what, err, where)
	}

	after := "after the Remove and the Rmdir"
	f := dg24Read(e, fs, "reading FS.dirty and FS.changes "+after, where)
	if f.dirty {
		t.Errorf("%s, FS.dirty is true, want false: a Remove or Rmdir that returns vfs.ErrNoEnt has changed nothing "+
			"(ADR 0024 sections 1 and 3; %s)", after, where)
	}
	if f.changes != f0.changes {
		t.Errorf("%s, FS.changes is %d, want %d, its value after New: nothing changed (ADR 0024 sections 1 and 3; "+
			"%s)", after, f.changes, f0.changes, where)
	}
	l2 := dg24ReadDir(e, fs, root, "ReadDir of the root directory "+after, where)
	if l2.err != nil {
		t.Errorf("ReadDir of the root directory %s = %v, want success (%s)", after, l2.err, where)
	} else {
		if l2.verf != v {
			t.Errorf("ReadDir of the root directory %s returns the verifier %x, want %x, the one it returned "+
				"before: the verifier is stable across a refused Remove, since the entry is left as it is (ADR "+
				"0024 section 3's table; %s)", after, l2.verf, v, where)
		}
		if slices.Contains(l2.names, dg24Ghost) {
			t.Errorf("ReadDir of the root directory %s lists %q (%q), want it not listed (ADR 0024 section 3's "+
				"table; %s)", after, dg24Ghost, l2.names, where)
		}
	}
	rootAfter := dg24GetAttr(e, fs, root, "GetAttr of the root directory "+after, where)
	if !rootAfter.MTime.Equal(rootBefore.MTime) {
		t.Errorf("%s, the root directory's mtime is %v, want %v, as it was: nothing changed, so nothing touched "+
			"it (ADR 0024 section 3; %s)", after, rootAfter.MTime, rootBefore.MTime, where)
	}
	what = fmt.Sprintf("testCaller's exclusive Create of %q %s", dg24Ghost, after)
	if err := dg24CreateExcl(e, fs, root, dg24Ghost, what, where); !errors.Is(err, vfs.ErrExist) {
		t.Errorf("%s = %v, want vfs.ErrExist: the entry is still there, naming no inode, so an exclusive create of "+
			"the name answers vfs.ErrExist (ADR 0024 section 3's table and \"What (b) costs\"; %s)", what, err,
			where)
	}
	if err := e.sync(fs, "the Sync "+after, where); err != nil {
		t.Errorf("the Sync %s = %v, want nil (ADR 0024 section 7; %s)", after, err, where)
	}
	if calls := e.h.records()[mark:]; len(calls) != 0 {
		t.Errorf("the harness recorded %v since New returned, want no call: nothing changed the namespace, so the "+
			"Sync with nothing dirty makes no store call (ADR 0024 sections 1 and 3; ADR 0023 section 3; %s)",
			calls, where)
	}

	rwhere := where + ", the remount"
	rm := e.remount(rwhere)
	if rm == nil {
		return
	}
	rmRoot := e.root(rm, rwhere)
	if l := e.lookup(rm, rmRoot, dg24Ghost, rwhere); !errors.Is(l.err, vfs.ErrNoEnt) {
		t.Errorf("Lookup of %q on a remount of the inner store = %v, want vfs.ErrNoEnt: the bucket holds the entry, "+
			"naming no inode, as the doctoring left it (ADR 0024 section 3; %s)", dg24Ghost, l.err, rwhere)
	}
	what = fmt.Sprintf("testCaller's exclusive Create of %q on the remount", dg24Ghost)
	if err := dg24CreateExcl(e, rm, rmRoot, dg24Ghost, what, rwhere); !errors.Is(err, vfs.ErrExist) {
		t.Errorf("%s = %v, want vfs.ErrExist: the entry is still in the bucket (ADR 0024 section 3; %s)", what, err,
			rwhere)
	}
}

// TestARenameOntoAnEntryThatNamesNoInodeReplacesIt is D2 of ADR 0024 section
// 7, and pins section 3's recovery path, ADR 0017 section 2's "Rename
// replaces": a Rename of a file onto a name whose entry names no inode
// replaces the entry, is marked dirty, and is in the next commit.
//
// This is a characterisation: it passes at f8398c4 too, and is pinned because
// ADR 0024 section 3 relies on it as the one way a client has to make such a
// name usable again (ADR 0024 Assumptions 10 and 15).
//
// The doctored snapshot is built and mounted behind an unarmed uc43Store;
// testCaller creates donor; Rename of donor to ghost in the root directory
// returns nil; Lookup of ghost returns donor's FileID and Lookup of donor
// answers vfs.ErrNoEnt; FS.dirty is true; Sync returns nil with exactly one
// Put under snapshotPrefix and one PutIfMatch of rootKey recorded; a remount's
// Lookup of ghost returns that FileID; and the snapshot the root pointer then
// names, decoded, has Inodes[rootInodeID].Entries["ghost"] equal to it and no
// donor.
func TestARenameOntoAnEntryThatNamesNoInodeReplacesIt(t *testing.T) {
	const where = "D2, ADR 0024 sections 3 and 7; ADR 0017 section 2"
	const donor = "donor"
	s := dg24Setup(t, where)
	e, fs, root := s.e, s.fs, s.root

	donorH := e.create(fs, root, donor, where)
	id := dg24GetAttr(e, fs, donorH, fmt.Sprintf("GetAttr of %q", donor), where).FileID

	what := fmt.Sprintf("testCaller's Rename of %q to %q in the root directory", donor, dg24Ghost)
	if err := dg24Do(e, what, where, func(ctx context.Context) error {
		return fs.Rename(ctx, testCaller, root, donor, root, dg24Ghost)
	}); err != nil {
		t.Fatalf("%s = %v, want nil: a Rename onto a name whose entry names no inode replaces the entry (ADR 0024 "+
			"section 3; ADR 0017 section 2; %s)", what, err, where)
	}
	if l := e.lookup(fs, root, dg24Ghost, where); l.err != nil {
		t.Errorf("Lookup of %q after %s = %v, want success (ADR 0017 section 2; %s)", dg24Ghost, what, l.err, where)
	} else if l.attr.FileID != id {
		t.Errorf("Lookup of %q after %s returns FileID %d, want %d, %q's: the moved object keeps its handle and "+
			"replaces the entry (ADR 0017 section 2; %s)", dg24Ghost, what, l.attr.FileID, id, donor, where)
	}
	if l := e.lookup(fs, root, donor, where); !errors.Is(l.err, vfs.ErrNoEnt) {
		t.Errorf("Lookup of %q after %s = %v, want vfs.ErrNoEnt (%s)", donor, what, l.err, where)
	}
	if f := dg24Read(e, fs, "reading FS.dirty after "+what, where); !f.dirty {
		t.Errorf("after %s, FS.dirty is false, want true: the Rename changed the namespace and is marked dirty "+
			"(ADR 0024 sections 1 and 3; %s)", what, where)
	}

	mark := len(e.h.records())
	if err := e.sync(fs, "the Sync after "+what, where); err != nil {
		t.Fatalf("the Sync after %s = %v, want nil against a healthy store (%s)", what, err, where)
	}
	calls := e.h.records()[mark:]
	if snaps, swaps := uc43Filter(calls, uc43IsSnapPut), uc43Filter(calls, uc43IsRootSwap); len(snaps) != 1 ||
		len(swaps) != 1 {
		t.Errorf("in the Sync after %s the harness recorded the Puts under %q %v and the PutIfMatch calls of %q %v, "+
			"want exactly one of each: the Rename is in the next commit (ADR 0024 section 3; ADR 0022 section 8, a "+
			"commit; %s)", what, snapshotPrefix, snaps, rootKey, swaps, where)
	}

	rwhere := where + ", the remount"
	if rm := e.remount(rwhere); rm != nil {
		if l := e.lookup(rm, e.root(rm, rwhere), dg24Ghost, rwhere); l.err != nil {
			t.Errorf("Lookup of %q on a remount of the inner store = %v, want success: the Rename was committed (ADR "+
				"0024 section 3; %s)", dg24Ghost, l.err, rwhere)
		} else if l.attr.FileID != id {
			t.Errorf("Lookup of %q on a remount of the inner store returns FileID %d, want %d (ADR 0024 section 3; "+
				"%s)", dg24Ghost, l.attr.FileID, id, rwhere)
		}
	}

	ctx, local := e.ctx, e.h.inner
	type named struct {
		rp   rootPointer
		snap *snapshot
		err  error
	}
	n := uc43Go(e, func() named {
		rp, snap, err := dg24Named(ctx, local)
		return named{rp, snap, err}
	}).wait(t, "decoding the snapshot the root pointer names", where)
	if n.err != nil {
		t.Fatalf("decoding the snapshot the root pointer names after the Sync: %v (%s)", n.err, where)
	}
	entries := n.snap.Inodes[rootInodeID].Entries
	if got, ok := entries[dg24Ghost]; !ok || got != id {
		t.Errorf("in the snapshot %q the root pointer names, the root directory's entry %q is %d (present %v), "+
			"want %d, %q's inode (ADR 0024 section 7; %s)", n.rp.Snapshot, dg24Ghost, got, ok, id, donor, where)
	}
	if _, ok := entries[donor]; ok {
		t.Errorf("in the snapshot %q the root pointer names, the root directory still has an entry %q, want none "+
			"(ADR 0024 section 7; %s)", n.rp.Snapshot, donor, where)
	}
}
