package blobfs

// Clean-room tests for #94, "The sticky bit on a directory is not honoured",
// written without reading the implementation, from ADR 0026
// (docs/adr/0026-in-a-sticky-directory-only-an-owner-removes-or-renames-an-entry.md)
// sections 1, 2, 4 and 8, ADR 0014 sections 5 and 11 (the facts and the safety
// rule), ADR 0025 section 6 (the harness as its tests use it, and A9's recipe
// for a diverged mount), ADR 0024 sections 3 and 7 (an entry that names no
// inode, and the facts), ADR 0011 section 7 (the safety rules), and
// internal/vfs/vfs.go.
//
// ADR 0026 section 1's rule, through the FS: in a directory whose mode has
// 0o1000 set, Remove, Rmdir and Rename take an entry's name out of it only for
// a caller who is root (uid 0), owns the directory, or owns the object the
// entry names; any other caller that the directory's mode gives write and
// search is refused with the bare vfs.ErrPerm, after the directory's mode and
// before every check of stage E. Rename is judged in its source directory for
// the entry it moves and in its target directory for the entry it replaces; a
// rename into a sticky directory that replaces nothing, a rename of a name onto
// itself, and a name that names no object take nothing out (section 2). A
// refused call changes nothing (section 4), and the mount gate comes first.
//
// Covered, with the names ADR 0026 section 8 pins:
//   - K5, TestAStickyDirectorysEntriesAreRemovedOnlyByRootOrAnOwner;
//   - K6, TestARenameInAStickyDirectoryIsHeldToTheStickyRule;
//   - K7, TestTheStickyRuleReadsTheModeAndTheOwnersAsTheyStand;
//   - K8, TestTheMountGateComesBeforeTheStickyRule;
//   - K9, TestAnEntryThatNamesNoInodeIsAbsentToTheStickyRule, a
//     characterisation that passes before the change and after.
//
// This file builds on the tree of 273f010 alone, so that the session can take
// its red run there (ADR 0026 section 8): it names no field of policy.Request
// and calls no helper that ADR 0026's other test changes add or alter. It
// reuses, as they stand at 273f010: uc43Setup, uc43Env and its methods
// (newFS, newFSWith, root, sync, commit, read, mode, lookup, remount) and its
// fields ctx and h (h's records, and h's inner *store.Local, which K8 gives
// uc43WriteForeignRoot as A9 does), uc43Config, uc43WriteForeignRoot and
// uc43Writer, and uc43Go and uc43Bound through the helpers below, which run
// every call in them (unlocked_commit_test.go); testCaller
// (fs_test.go); sa24Other, sa24U32, sa24Read, sa24SetAttr and sa24GetAttr
// (setattr_atomic_test.go); sp24Root (setattr_policy_test.go); pwMember,
// pwEntry, pwCreate, pwMkdir, pwSymlink, pwMust, pwRemove, pwRmdir, pwRename,
// pwGetAttr, pwStatus, pwWho, pwSetupObject, pwWantFound and pwWantNoEnt
// (policy_wiring_test.go); oaMode, oaStatus and oaWant (owner_access_test.go);
// and dg24Setup, dg24Env and dg24Ghost (dangling_entry_test.go). Helpers of
// this file's own are prefixed stk.
//
// The callers are testCaller, vfs.Caller{UID: 501, GID: 20}, who owns the root
// directory; sa24Other, vfs.Caller{UID: 502, GID: 30}; pwMember,
// vfs.Caller{UID: 503, GID: 20}; and sp24Root, vfs.Caller{UID: 0}. An object
// is owned by the uid and gid of the caller that created it, and sp24Root's
// creation with a UID makes an object of that owner (ADR 0026 section 8, the
// facts). Every status is compared with == against the bare vfs.Status.
//
// The safety rules (ADR 0026 section 8; ADR 0025 section 6; ADR 0014 section
// 11; ADR 0011 section 7): every bucket is a *store.Local under the test's
// t.TempDir(), behind an unarmed uc43Store, through uc43Setup (dg24Setup uses
// it too); every mount is made with uc43Config, through e.newFS, e.newFSWith,
// e.remount or dg24Setup; every New and every FS call runs in uc43Go and is
// waited for under uc43Bound; nothing sleeps, polls or runs in parallel.
// Unix() is left in place on every mount: f.perm is never replaced, and no FS
// field is written. FS.dirty and FS.changes are read only through sa24Read,
// holding FS.mu for reading; K8 reads FS.epoch through uc43Env.read, as A9's
// recipe does, and f.mode.State() through uc43Env.mode, which is how
// mountDiverged, the one unexported non-test name used here, is reached. A
// mount that is replaced, by a read-only mount or by a remount, is not used
// again.
//
// Not covered, on purpose: ACCESS in a sticky directory (ADR 0026 section 5),
// which no test here asks; a Rename of a directory into its own subtree; and
// the rows ADR 0014 section 11 leaves unpinned (U3 where Permits allows, U5
// and U6), which nothing here reaches.

import (
	"fmt"
	"strings"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// stkRule is why a sticky refusal is vfs.ErrPerm.
const stkRule = "in a directory whose mode has 0o1000 set, a caller whom the directory's mode gives write and " +
	"search, and who is neither root, nor the directory's owner, nor the owner of the object the entry names, is " +
	"refused with the bare vfs.ErrPerm (ADR 0026 sections 1 and 3)"

// stkCase is one call by one caller, and what it must return.
type stkCase struct {
	c    vfs.Caller
	call string
	fn   func(what string) error
	want error
	why  string
}

// stkWhat names a case for failure messages.
func stkWhat(c stkCase) string { return fmt.Sprintf("%s's %s", pwWho(c.c), c.call) }

// stkTry makes one call that must return want, compared with ==, and change
// nothing: FS.changes and FS.dirty are what they were before the call (ADR
// 0026 section 4; ADR 0024 section 1). The tests that use it start from a
// Sync that left FS.dirty false, and check afterwards that it still is.
func stkTry(e *uc43Env, fs *FS, k stkCase, where string) {
	e.t.Helper()
	what := stkWhat(k)
	before := sa24Read(e, fs, "reading FS.dirty and FS.changes before "+what, where)
	oaWant(e.t, what, k.fn(what), k.want, k.why, where)
	after := sa24Read(e, fs, "reading FS.dirty and FS.changes after "+what, where)
	if after.changes != before.changes {
		e.t.Errorf("after %s, FS.changes is %d, want %d, its value before the call: a call that returns an error, "+
			"or renames a name onto itself, changes nothing (ADR 0026 section 4; ADR 0024 section 1; %s)", what,
			after.changes, before.changes, where)
	}
	if after.dirty != before.dirty {
		e.t.Errorf("after %s, FS.dirty is %v, want %v, as it was before the call: a call that returns an error, or "+
			"renames a name onto itself, marks nothing (ADR 0026 section 4; ADR 0024 section 1; %s)", what,
			after.dirty, before.dirty, where)
	}
}

// stkRemoveFn, stkRmdirFn and stkRenameFn are the calls, as c, on fs.
func stkRemoveFn(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, name, where string) func(string) error {
	return func(what string) error { return pwRemove(e, fs, c, dir, name, what, where) }
}

func stkRmdirFn(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, name, where string) func(string) error {
	return func(what string) error { return pwRmdir(e, fs, c, dir, name, what, where) }
}

func stkRenameFn(e *uc43Env, fs *FS, c vfs.Caller, from vfs.Handle, fromName string, to vfs.Handle, toName,
	where string) func(string) error {
	return func(what string) error { return pwRename(e, fs, c, from, fromName, to, toName, what, where) }
}

// stkMade requires r, c's creation of path as setup, to have succeeded and
// made an object of type typ and mode mode owned by c's uid and gid, and
// returns its handle and file id (ADR 0026 section 8 and ADR 0014 section 11,
// the facts).
func stkMade(e *uc43Env, fs *FS, r pwEntry, c vfs.Caller, path string, typ vfs.FileType, mode uint32, what,
	where string) (vfs.Handle, uint64) {
	e.t.Helper()
	h := pwMust(e, r, what, where)
	pwSetupObject(e, fs, h, path, policy.Object{Type: typ, Mode: mode, UID: c.UID, GID: c.GID}, -1, where)
	return h, r.a.FileID
}

// stkDir is c's Mkdir of name in dir with sa, as setup, which must make a
// directory of mode mode owned by c.
func stkDir(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, path, name string, sa vfs.SetAttr, mode uint32,
	where string) (vfs.Handle, uint64) {
	e.t.Helper()
	what := fmt.Sprintf("setup: %s's Mkdir(%q)", pwWho(c), path)
	return stkMade(e, fs, pwMkdir(e, fs, c, dir, name, sa, what, where), c, path, vfs.TypeDir, mode, what, where)
}

// stkFile is c's exclusive Create of name in dir with no Mode, as setup, which
// must make a regular file of mode 0o644 owned by c.
func stkFile(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, path, name, where string) (vfs.Handle, uint64) {
	e.t.Helper()
	what := fmt.Sprintf("setup: %s's Create(%q, exclusive)", pwWho(c), path)
	r := pwCreate(e, fs, c, dir, name, vfs.SetAttr{}, true, what, where)
	return stkMade(e, fs, r, c, path, vfs.TypeReg, 0o644, what, where)
}

// stkEntry is one entry of a tree: the directory that holds it, its path and
// name, and the file id of the object it names.
type stkEntry struct {
	dir  vfs.Handle
	path string
	name string
	id   uint64
}

// stkTree is the tree K5 and K6 each build afresh (ADR 0026 section 8).
type stkTree struct {
	e                    *uc43Env
	fs                   *FS
	root                 vfs.Handle
	tmp, open, grp, full vfs.Handle
	entries              []stkEntry
	ids                  map[string]uint64 // by path
	c0                   uint64            // FS.changes after the setup's Sync
	mark                 int               // the harness's record length when the setup's Sync returned
}

// add records an entry the setup made.
func (s *stkTree) add(dir vfs.Handle, path, name string, id uint64) {
	s.entries = append(s.entries, stkEntry{dir: dir, path: path, name: name, id: id})
	s.ids[path] = id
}

// stkBuild makes the tree of ADR 0026 section 8. testCaller makes, in the
// root, tmp, a directory 0o1777; open, a directory 0o777; and grp, a
// directory 0o1770. Then sa24Other makes, in tmp, the file a, the empty
// directory da, the symlink la to "a", and the directory full holding a file
// x; pwMember makes the file b in tmp; testCaller makes the file t in tmp and
// the file g1 in grp; sp24Root makes the file g2 in grp with UID 502;
// sa24Other makes the files o1 and o2 in open, and pwMember the file m1 there.
// Then Sync, after which FS.dirty is false. It records FS.changes then, c0,
// and the harness's record length, mark.
func stkBuild(t *testing.T, where string) *stkTree {
	t.Helper()
	e := uc43Setup(t)
	fs := e.newFS(where)
	s := &stkTree{e: e, fs: fs, root: e.root(fs, where), ids: map[string]uint64{}}
	tc, o, m := testCaller, sa24Other, pwMember

	var id uint64
	s.tmp, id = stkDir(e, fs, tc, s.root, "tmp", "tmp", oaMode(0o1777), 0o1777, where)
	s.add(s.root, "tmp", "tmp", id)
	s.open, id = stkDir(e, fs, tc, s.root, "open", "open", oaMode(0o777), 0o777, where)
	s.add(s.root, "open", "open", id)
	s.grp, id = stkDir(e, fs, tc, s.root, "grp", "grp", oaMode(0o1770), 0o1770, where)
	s.add(s.root, "grp", "grp", id)

	_, id = stkFile(e, fs, o, s.tmp, "tmp/a", "a", where)
	s.add(s.tmp, "tmp/a", "a", id)
	_, id = stkDir(e, fs, o, s.tmp, "tmp/da", "da", vfs.SetAttr{}, 0o755, where)
	s.add(s.tmp, "tmp/da", "da", id)
	what := `setup: sa24Other's Symlink("tmp/la", "a")`
	_, id = stkMade(e, fs, pwSymlink(e, fs, o, s.tmp, "la", "a", vfs.SetAttr{}, what, where), o, "tmp/la",
		vfs.TypeLnk, 0o777, what, where)
	s.add(s.tmp, "tmp/la", "la", id)
	s.full, id = stkDir(e, fs, o, s.tmp, "tmp/full", "full", vfs.SetAttr{}, 0o755, where)
	s.add(s.tmp, "tmp/full", "full", id)
	_, id = stkFile(e, fs, o, s.full, "tmp/full/x", "x", where)
	s.add(s.full, "tmp/full/x", "x", id)

	_, id = stkFile(e, fs, m, s.tmp, "tmp/b", "b", where)
	s.add(s.tmp, "tmp/b", "b", id)
	_, id = stkFile(e, fs, tc, s.tmp, "tmp/t", "t", where)
	s.add(s.tmp, "tmp/t", "t", id)
	_, id = stkFile(e, fs, tc, s.grp, "grp/g1", "g1", where)
	s.add(s.grp, "grp/g1", "g1", id)

	what = `setup: root's Create("grp/g2", {UID 502}, exclusive)`
	g2 := pwCreate(e, fs, sp24Root, s.grp, "g2", vfs.SetAttr{UID: sa24U32(502)}, true, what, where)
	g2h := pwMust(e, g2, what, where)
	if a := sa24GetAttr(e, fs, g2h, `setup: GetAttr of "grp/g2"`, where); a.Type != vfs.TypeReg || a.UID != 502 {
		t.Fatalf("setup: \"grp/g2\" is of type %d owned by uid %d, want a regular file owned by uid 502: root's "+
			"creation with a UID makes an object of that owner (ADR 0026 section 8, the facts; %s)", a.Type, a.UID,
			where)
	}
	s.add(s.grp, "grp/g2", "g2", g2.a.FileID)

	_, id = stkFile(e, fs, o, s.open, "open/o1", "o1", where)
	s.add(s.open, "open/o1", "o1", id)
	_, id = stkFile(e, fs, o, s.open, "open/o2", "o2", where)
	s.add(s.open, "open/o2", "o2", id)
	_, id = stkFile(e, fs, m, s.open, "open/m1", "m1", where)
	s.add(s.open, "open/m1", "m1", id)

	if err := e.sync(fs, "setup: the Sync that commits the tree", where); err != nil {
		t.Fatalf("setup: the Sync that commits the tree = %v, want nil against a healthy store (%s)", err, where)
	}
	s.mark = len(e.h.records())
	f := sa24Read(e, fs, "setup: reading FS.dirty and FS.changes after the Sync", where)
	if f.dirty {
		t.Fatalf("setup: FS.dirty is true after a Sync that returned nil with no other call running, want false "+
			"(ADR 0023 section 3; %s)", where)
	}
	s.c0 = f.changes
	return s
}

// wantAllFound requires testCaller's Lookup of every entry the setup made to
// find the object it named, by its file id.
func (s *stkTree) wantAllFound(after, why, where string) {
	s.e.t.Helper()
	for _, en := range s.entries {
		pwWantFound(s.e, s.fs, en.dir, en.path, en.name, en.id, after, why, where)
	}
}

// wantUnchangedSinceSetup requires FS.changes to be c0 and FS.dirty false,
// and a Sync to return nil with no store call recorded since the setup's Sync
// returned (ADR 0026 section 4; ADR 0024 section 7, the facts).
func (s *stkTree) wantUnchangedSinceSetup(after, where string) {
	e := s.e
	e.t.Helper()
	f := sa24Read(e, s.fs, "reading FS.dirty and FS.changes "+after, where)
	if f.changes != s.c0 || f.dirty {
		e.t.Errorf("%s, FS.changes is %d and FS.dirty %v, want %d, as after the setup's Sync, and false: a refused "+
			"call changes nothing (ADR 0026 section 4; %s)", after, f.changes, f.dirty, s.c0, where)
	}
	if err := e.sync(s.fs, "the Sync "+after, where); err != nil {
		e.t.Errorf("the Sync %s = %v, want nil (%s)", after, err, where)
	}
	if calls := e.h.records(); len(calls) != s.mark {
		e.t.Errorf("the Sync %s: the harness recorded %v since the setup's Sync returned, want no store call: "+
			"nothing changed, so there is nothing to commit (ADR 0026 section 4; ADR 0024 section 7, the facts; %s)",
			after, calls[s.mark:], where)
	}
}

// TestAStickyDirectorysEntriesAreRemovedOnlyByRootOrAnOwner is K5 of ADR 0026
// section 8 (section 1; #94's first done-when, for Remove and Rmdir), on
// stkBuild's tree. Every call below that is refused is made, and shown to
// change nothing, before any call that is allowed.
//
//   - vfs.ErrPerm: pwMember's Remove(tmp, "a"); sa24Other's Remove(tmp, "b");
//     sa24Other's Remove(tmp, "t"), the directory owner's own file;
//     pwMember's Rmdir(tmp, "da"); pwMember's Remove(tmp, "la"); and
//     pwMember's Remove(grp, "g1"), a group member with write and search.
//   - vfs.ErrPerm, before stage E: pwMember's Rmdir(tmp, "a"), not
//     vfs.ErrNotDir; Remove(tmp, "da"), not vfs.ErrIsDir; and
//     Rmdir(tmp, "full"), not vfs.ErrNotEmpty.
//   - Another status: sa24Other's Remove(grp, "g1") and its Remove(grp, "g2"),
//     of the entry it owns, vfs.ErrAcces; pwMember's Remove(tmp, "nope"),
//     vfs.ErrNoEnt.
//   - After all of those: Lookup still finds every entry, by its file id;
//     FS.changes is c0 and FS.dirty false; and a Sync returns nil with no
//     store call recorded since the setup's Sync returned (section 4).
//   - nil, each followed by vfs.ErrNoEnt from Lookup of the name: sa24Other's
//     Remove(tmp, "la") and Rmdir(tmp, "da"), the entry's owner; testCaller's
//     Remove(tmp, "b"), the directory's owner; sp24Root's Remove(tmp, "a");
//     and pwMember's Remove(open, "o1"), another's entry in a directory
//     without the bit.
func TestAStickyDirectorysEntriesAreRemovedOnlyByRootOrAnOwner(t *testing.T) {
	const where = "K5, ADR 0026 sections 1, 2, 4 and 8"
	s := stkBuild(t, where)
	e, fs := s.e, s.fs
	tc, o, m, rt := testCaller, sa24Other, pwMember, sp24Root
	rm := func(c vfs.Caller, dir vfs.Handle, name string) func(string) error {
		return stkRemoveFn(e, fs, c, dir, name, where)
	}
	rd := func(c vfs.Caller, dir vfs.Handle, name string) func(string) error {
		return stkRmdirFn(e, fs, c, dir, name, where)
	}
	const beforeE = "; and the row is asked at stage D, before stage E's checks of the entry's kind and contents " +
		"(ADR 0026 section 2, the order of errors)"

	refused := []stkCase{
		{m, `Remove(tmp, "a")`, rm(m, s.tmp, "a"), vfs.ErrPerm, stkRule},
		{o, `Remove(tmp, "b")`, rm(o, s.tmp, "b"), vfs.ErrPerm, stkRule},
		{o, `Remove(tmp, "t")`, rm(o, s.tmp, "t"), vfs.ErrPerm,
			stkRule + "; t is the directory owner's own file, which makes sa24Other neither owner"},
		{m, `Rmdir(tmp, "da")`, rd(m, s.tmp, "da"), vfs.ErrPerm,
			stkRule + "; the rule holds for an entry of every type, a directory included (ADR 0026 section 1)"},
		{m, `Remove(tmp, "la")`, rm(m, s.tmp, "la"), vfs.ErrPerm,
			stkRule + "; the rule holds for an entry of every type, a symlink included (ADR 0026 section 1)"},
		{m, `Remove(grp, "g1")`, rm(m, s.grp, "g1"), vfs.ErrPerm,
			stkRule + "; a group member whom 0o1770 gives write and search is no owner (ADR 0026 section 1)"},
		{m, `Rmdir(tmp, "a")`, rd(m, s.tmp, "a"), vfs.ErrPerm, stkRule + ", not vfs.ErrNotDir" + beforeE},
		{m, `Remove(tmp, "da")`, rm(m, s.tmp, "da"), vfs.ErrPerm, stkRule + ", not vfs.ErrIsDir" + beforeE},
		{m, `Rmdir(tmp, "full")`, rd(m, s.tmp, "full"), vfs.ErrPerm, stkRule + ", not vfs.ErrNotEmpty" + beforeE},
		{o, `Remove(grp, "g1")`, rm(o, s.grp, "g1"), vfs.ErrAcces,
			"0o1770 gives the other class no write or search, and the directory's mode is asked before the sticky " +
				"rule (ADR 0026 section 1, line 2)"},
		{o, `Remove(grp, "g2")`, rm(o, s.grp, "g2"), vfs.ErrAcces,
			"owning the entry gives no permission to change the directory: 0o1770 gives the other class no write " +
				"or search, which is asked first (ADR 0026 section 1, line 2)"},
		{m, `Remove(tmp, "nope")`, rm(m, s.tmp, "nope"), vfs.ErrNoEnt,
			"a name that names no object describes no entry, so the row judges the directory alone and stage E " +
				"answers vfs.ErrNoEnt (ADR 0026 section 2)"},
	}
	for _, k := range refused {
		stkTry(e, fs, k, where)
	}

	const kept = "a refused call takes no name away (ADR 0026 section 4)"
	s.wantAllFound("the refused calls", kept, where)
	s.wantUnchangedSinceSetup("after the refused calls", where)

	allowed := []stkCase{
		{o, `Remove(tmp, "la")`, rm(o, s.tmp, "la"), nil, "the entry's owner may remove it (ADR 0026 section 1)"},
		{o, `Rmdir(tmp, "da")`, rd(o, s.tmp, "da"), nil, "the entry's owner may remove it (ADR 0026 section 1)"},
		{tc, `Remove(tmp, "b")`, rm(tc, s.tmp, "b"), nil,
			"the directory's owner may remove any entry of it (ADR 0026 section 1)"},
		{rt, `Remove(tmp, "a")`, rm(rt, s.tmp, "a"), nil, "root may remove any entry (ADR 0026 section 1)"},
		{m, `Remove(open, "o1")`, rm(m, s.open, "o1"), nil,
			"a directory without the bit is judged by its mode alone, as before (ADR 0026 section 1)"},
	}
	names := []struct{ dir, path, name string }{
		{"tmp", "tmp/la", "la"}, {"tmp", "tmp/da", "da"}, {"tmp", "tmp/b", "b"}, {"tmp", "tmp/a", "a"},
		{"open", "open/o1", "o1"},
	}
	dirs := map[string]vfs.Handle{"tmp": s.tmp, "open": s.open}
	for i, k := range allowed {
		what := stkWhat(k)
		oaWant(t, what, k.fn(what), nil, k.why, where)
		n := names[i]
		pwWantNoEnt(e, fs, dirs[n.dir], n.path, n.name, what, "an allowed removal takes the name away", where)
	}
}

// TestARenameInAStickyDirectoryIsHeldToTheStickyRule is K6 of ADR 0026
// section 8 (sections 1 and 2; #94's done-when, "for the moved entry and for
// an existing target"), on stkBuild's tree. Every call below that is refused,
// and every other answer, is made, and shown to change nothing, before any
// call that is allowed.
//
//   - vfs.ErrPerm: pwMember's Rename(tmp, "a", tmp, "a9");
//     Rename(tmp, "a", open, "x"), out of a sticky source;
//     Rename(tmp, "b", tmp, "a"), its own entry onto another's;
//     Rename(open, "m1", tmp, "a"), its own entry from a plain source onto
//     another's in a sticky target; and Rename(tmp, "a", tmp, "da"), not
//     vfs.ErrIsDir. After them every entry named is still found where it
//     was, a9 and open/x are not, and FS.changes is unchanged.
//   - Other answers, each with FS.changes unchanged: pwMember's
//     Rename(tmp, "t", tmp, "t"), another's entry onto itself, nil; its
//     Rename(tmp, "nope", tmp, "t"), no source and another's target,
//     vfs.ErrNoEnt; sa24Other's Rename(tmp, "a", grp, "y"), its own entry
//     into a directory it cannot write, vfs.ErrAcces.
//   - nil: pwMember's Rename(open, "o1", tmp, "in"), into a sticky target
//     that replaces nothing, which is the second refused call with its
//     directories exchanged, after which Lookup(tmp, "in") finds o1's file
//     id; pwMember's Rename(tmp, "b", open, "o2"), its own entry onto
//     another's in a plain target; sa24Other's Rename(tmp, "a", tmp, "a2"),
//     the entry's owner; testCaller's Rename(tmp, "la", tmp, "la2"), the
//     directory's owner; and sp24Root's Rename(tmp, "la2", open, "r"). After
//     each the target name finds the object moved, which keeps its file id,
//     and the source name is gone.
func TestARenameInAStickyDirectoryIsHeldToTheStickyRule(t *testing.T) {
	const where = "K6, ADR 0026 sections 1, 2, 4 and 8"
	s := stkBuild(t, where)
	e, fs := s.e, s.fs
	tc, o, m, rt := testCaller, sa24Other, pwMember, sp24Root
	mv := func(c vfs.Caller, from vfs.Handle, fromName string, to vfs.Handle, toName string) func(string) error {
		return stkRenameFn(e, fs, c, from, fromName, to, toName, where)
	}
	const moved = stkRule + "; Rename is judged in its source directory for the entry it moves (ADR 0026 " +
		"section 1, line 3)"
	const replaced = stkRule + "; Rename is judged in its target directory for the entry it replaces, whoever " +
		"owns the entry it moves (ADR 0026 section 1, line 4)"

	refused := []stkCase{
		{m, `Rename(tmp, "a", tmp, "a9")`, mv(m, s.tmp, "a", s.tmp, "a9"), vfs.ErrPerm, moved},
		{m, `Rename(tmp, "a", open, "x")`, mv(m, s.tmp, "a", s.open, "x"), vfs.ErrPerm,
			moved + "; out of a sticky source into a plain target"},
		{m, `Rename(tmp, "b", tmp, "a")`, mv(m, s.tmp, "b", s.tmp, "a"), vfs.ErrPerm,
			replaced + "; b is pwMember's own"},
		{m, `Rename(open, "m1", tmp, "a")`, mv(m, s.open, "m1", s.tmp, "a"), vfs.ErrPerm,
			replaced + "; m1 is pwMember's own, from a source without the bit"},
		{m, `Rename(tmp, "a", tmp, "da")`, mv(m, s.tmp, "a", s.tmp, "da"), vfs.ErrPerm,
			moved + ", not vfs.ErrIsDir: the row is asked at stage D, before stage E's checks of the target " +
				"(ADR 0026 section 2, the order of errors)"},
	}
	for _, k := range refused {
		stkTry(e, fs, k, where)
	}
	const kept = "a refused Rename moves, replaces and makes nothing (ADR 0026 section 4)"
	s.wantAllFound("the refused renames", kept, where)
	pwWantNoEnt(e, fs, s.tmp, "tmp/a9", "a9", "the refused renames", kept, where)
	pwWantNoEnt(e, fs, s.open, "open/x", "x", "the refused renames", kept, where)

	others := []stkCase{
		{m, `Rename(tmp, "t", tmp, "t")`, mv(m, s.tmp, "t", s.tmp, "t"), nil,
			"a rename of a name onto itself takes no name out of any directory, so its request describes no " +
				"entry, and it returns nil having changed nothing (ADR 0026 section 2; Assumption 11)"},
		{m, `Rename(tmp, "nope", tmp, "t")`, mv(m, s.tmp, "nope", s.tmp, "t"), vfs.ErrNoEnt,
			"a missing source describes no entry and no replaced entry, so the row judges the directories alone " +
				"and stage E answers vfs.ErrNoEnt (ADR 0026 section 2; Assumption 13)"},
		{o, `Rename(tmp, "a", grp, "y")`, mv(o, s.tmp, "a", s.grp, "y"), vfs.ErrAcces,
			"0o1770 gives the other class no write or search on the target, and the directories' modes are " +
				"asked before the sticky rule (ADR 0026 section 1, line 2)"},
	}
	for _, k := range others {
		stkTry(e, fs, k, where)
	}
	s.wantAllFound("the renames that change nothing", kept, where)

	allowed := []struct {
		k                  stkCase
		from, to           vfs.Handle
		fromPath, fromName string
		toPath, toName     string
		id                 uint64
	}{
		{stkCase{m, `Rename(open, "o1", tmp, "in")`, mv(m, s.open, "o1", s.tmp, "in"), nil,
			"a rename into a sticky directory that replaces nothing takes nothing out of it, and its source has " +
				"no bit (ADR 0026 section 1)"},
			s.open, s.tmp, "open/o1", "o1", "tmp/in", "in", s.ids["open/o1"]},
		{stkCase{m, `Rename(tmp, "b", open, "o2")`, mv(m, s.tmp, "b", s.open, "o2"), nil,
			"its own entry may leave the sticky source, and a target without the bit is judged by its mode " +
				"alone (ADR 0026 section 1)"},
			s.tmp, s.open, "tmp/b", "b", "open/o2", "o2", s.ids["tmp/b"]},
		{stkCase{o, `Rename(tmp, "a", tmp, "a2")`, mv(o, s.tmp, "a", s.tmp, "a2"), nil,
			"the entry's owner may rename it (ADR 0026 section 1)"},
			s.tmp, s.tmp, "tmp/a", "a", "tmp/a2", "a2", s.ids["tmp/a"]},
		{stkCase{tc, `Rename(tmp, "la", tmp, "la2")`, mv(tc, s.tmp, "la", s.tmp, "la2"), nil,
			"the directory's owner may rename any entry of it (ADR 0026 section 1)"},
			s.tmp, s.tmp, "tmp/la", "la", "tmp/la2", "la2", s.ids["tmp/la"]},
		{stkCase{rt, `Rename(tmp, "la2", open, "r")`, mv(rt, s.tmp, "la2", s.open, "r"), nil,
			"root may rename any entry (ADR 0026 section 1)"},
			s.tmp, s.open, "tmp/la2", "la2", "open/r", "r", s.ids["tmp/la"]},
	}
	for _, a := range allowed {
		what := stkWhat(a.k)
		oaWant(t, what, a.k.fn(what), nil, a.k.why, where)
		pwWantFound(e, fs, a.to, a.toPath, a.toName, a.id, what,
			"an allowed Rename moves the object, which keeps its handle and file id (internal/vfs/vfs.go)", where)
		pwWantNoEnt(e, fs, a.from, a.fromPath, a.fromName, what, "an allowed Rename takes the source name away",
			where)
	}
}

// TestTheStickyRuleReadsTheModeAndTheOwnersAsTheyStand is K7 of ADR 0026
// section 8 (sections 1 and 4): the rule reads the directory's mode and the
// owners as they stand when the call is made, and the bit is committed.
//
// testCaller makes tmp, 0o1777; sa24Other makes a and c in it, and testCaller
// makes t. pwMember's Remove(tmp, "a") returns vfs.ErrPerm. testCaller's
// SetAttr(tmp, {Mode: 0o777}) returns nil, and pwMember's Remove(tmp, "a")
// then returns nil. testCaller's SetAttr(tmp, {Mode: 0o1777}) returns nil,
// and pwMember's Remove(tmp, "c") returns vfs.ErrPerm. sp24Root's
// SetAttr(c, {UID: 503}) returns nil, and pwMember's Remove(tmp, "c") then
// returns nil. Then Sync returns nil, and on a remount GetAttr of tmp reports
// mode 0o1777, sa24Other's Remove(tmp, "t") returns vfs.ErrPerm, and
// testCaller's returns nil.
func TestTheStickyRuleReadsTheModeAndTheOwnersAsTheyStand(t *testing.T) {
	const where = "K7, ADR 0026 sections 1, 4 and 8"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	tc, o, m, rt := testCaller, sa24Other, pwMember, sp24Root

	tmp, _ := stkDir(e, fs, tc, root, "tmp", "tmp", oaMode(0o1777), 0o1777, where)
	stkFile(e, fs, o, tmp, "tmp/a", "a", where)
	c, _ := stkFile(e, fs, o, tmp, "tmp/c", "c", where)
	stkFile(e, fs, tc, tmp, "tmp/t", "t", where)

	remove := func(fs *FS, c vfs.Caller, dir vfs.Handle, name string, want error, why string) {
		t.Helper()
		what := fmt.Sprintf("%s's Remove(tmp, %q)", pwWho(c), name)
		oaWant(t, what, pwRemove(e, fs, c, dir, name, what, where), want, why, where)
	}
	setAttr := func(c vfs.Caller, h vfs.Handle, name string, sa vfs.SetAttr, desc string) {
		t.Helper()
		what := fmt.Sprintf("%s's SetAttr(%s, %s)", pwWho(c), name, desc)
		if r := sa24SetAttr(e, fs, c, h, sa, what, where); r.err != nil {
			t.Errorf("%s = %s, want nil (ADR 0014 section 5's OpChmod and OpChown rows; %s)", what, oaStatus(r.err),
				where)
		}
	}

	remove(fs, m, tmp, "a", vfs.ErrPerm, stkRule)
	setAttr(tc, tmp, "tmp", oaMode(0o777), "{Mode: 0o777}")
	remove(fs, m, tmp, "a", nil, "the directory's mode no longer has 0o1000 set, and the rule reads the mode as "+
		"it stands when the call is made (ADR 0026 sections 1 and 4)")
	setAttr(tc, tmp, "tmp", oaMode(0o1777), "{Mode: 0o1777}")
	remove(fs, m, tmp, "c", vfs.ErrPerm, stkRule+"; the bit set again by SETATTR is read as it stands")
	setAttr(rt, c, "tmp/c", vfs.SetAttr{UID: sa24U32(503)}, "{UID: 503}")
	remove(fs, m, tmp, "c", nil, "c is now pwMember's own, and the rule reads the entry's owner as it stands when "+
		"the call is made (ADR 0026 sections 1 and 4)")

	if err := e.sync(fs, "the Sync before the remount", where); err != nil {
		t.Fatalf("the Sync before the remount = %v, want nil against a healthy store (%s)", err, where)
	}
	rm := e.remount(where + ", the remount")
	if rm == nil {
		return
	}
	rroot := e.root(rm, where)
	lt := e.lookup(rm, rroot, "tmp", where)
	if lt.err != nil {
		t.Fatalf("on the remount, Lookup of \"tmp\" = %s, want nil: the Sync committed it (%s)", oaStatus(lt.err),
			where)
	}
	if g := pwGetAttr(e, rm, lt.h, `GetAttr of "tmp" on the remount`, where); g.err != nil || g.v.Mode != 0o1777 {
		t.Errorf("on the remount, GetAttr of \"tmp\" = (mode %#o, %s), want (mode 0o1777, nil): the bit is stored, "+
			"committed and reported (ADR 0026, What the code has; %s)", g.v.Mode, oaStatus(g.err), where)
	}
	remove(rm, o, lt.h, "t", vfs.ErrPerm, stkRule+"; on a remount, the committed bit is read as it was set")
	remove(rm, tc, lt.h, "t", nil, "the directory's owner may remove any entry of it (ADR 0026 section 1)")
}

// stkGate is K8's setup: a writable mount on which testCaller made tmp,
// 0o1777, and sa24Other made a and p in it, and which has been Synced.
type stkGate struct {
	e    *uc43Env
	fs   *FS
	root vfs.Handle
	tmp  vfs.Handle
	aID  uint64
}

// stkGateSetup makes K8's setup. pwMember's Remove(tmp, "p") returns
// vfs.ErrPerm there, which shows that the row would refuse pwMember's calls
// on a, before a Sync that must return nil. The first is checked with
// Errorf, not Fatalf, so that a build without the rule fails on it alone
// (ADR 0026 section 8, Red run).
func stkGateSetup(t *testing.T, where string) stkGate {
	t.Helper()
	e := uc43Setup(t)
	g := stkGate{e: e}
	g.fs = e.newFS(where)
	g.root = e.root(g.fs, where)
	g.tmp, _ = stkDir(e, g.fs, testCaller, g.root, "tmp", "tmp", oaMode(0o1777), 0o1777, where)
	_, g.aID = stkFile(e, g.fs, sa24Other, g.tmp, "tmp/a", "a", where)
	stkFile(e, g.fs, sa24Other, g.tmp, "tmp/p", "p", where)
	what := `pwMember's Remove(tmp, "p") on the writable mount`
	oaWant(t, what, pwRemove(e, g.fs, pwMember, g.tmp, "p", what, where), vfs.ErrPerm,
		stkRule+"; this shows that the row would refuse pwMember, so that what the gate answers below is the "+
			"gate's", where)
	if err := e.sync(g.fs, "setup: the Sync of the writable mount", where); err != nil {
		t.Fatalf("setup: the Sync of the writable mount = %v, want nil against a healthy store (%s)", err, where)
	}
	return g
}

// TestTheMountGateComesBeforeTheStickyRule is K8 of ADR 0026 section 8
// (section 2: the mount gates come first), in two subtests, each from
// stkGateSetup, on the pattern of ADR 0025 section 6's A9.
//
// Read-only: a second mount of the same bucket is made with Config.ReadOnly
// set, and the first is not used again. On it, pwMember's Remove(tmp, "a"),
// Rmdir(tmp, "a") and Rename(tmp, "a", tmp, "z"), and sa24Other's
// Remove(tmp, "a"), the entry's owner, return vfs.ErrROFS, and a is still
// found.
//
// Diverged: as A9 diverges a mount, a foreign root pointer is written on the
// inner store with uc43WriteForeignRoot at the mount's epoch plus one; the
// commit of c2 returns an error whose text contains "commit conflict"; and
// f.mode.State() is mountDiverged. Then pwMember's Remove(tmp, "a") and
// Rename(tmp, "a", tmp, "z"), and sa24Other's Remove(tmp, "a"), return
// vfs.ErrStale.
func TestTheMountGateComesBeforeTheStickyRule(t *testing.T) {
	t.Run("read-only", func(t *testing.T) {
		const where = "K8, read-only, ADR 0026 sections 2 and 8; ADR 0011 section 4"
		g := stkGateSetup(t, where)
		e, m, o := g.e, pwMember, sa24Other

		cfg := uc43Config(e.h)
		cfg.ReadOnly = true
		rofs := e.newFSWith(cfg, where)
		rroot := e.root(rofs, where)
		lt := e.lookup(rofs, rroot, "tmp", where)
		if lt.err != nil {
			t.Fatalf("on the read-only mount, Lookup of \"tmp\" = %s, want nil: the setup's Sync committed it (%s)",
				oaStatus(lt.err), where)
		}
		tmp := lt.h

		const rofsWhy = "a read-only mount takes no change, for every caller, the entry's owner included: Remove, " +
			"Rmdir and Rename call mutable() at stage A, before the row is asked (ADR 0026 section 2; ADR 0011 " +
			"section 4; ADR 0014 section 7)"
		cases := []stkCase{
			{m, `Remove(tmp, "a") on the read-only mount`, stkRemoveFn(e, rofs, m, tmp, "a", where), vfs.ErrROFS,
				rofsWhy},
			{m, `Rmdir(tmp, "a") on the read-only mount`, stkRmdirFn(e, rofs, m, tmp, "a", where), vfs.ErrROFS,
				rofsWhy},
			{m, `Rename(tmp, "a", tmp, "z") on the read-only mount`, stkRenameFn(e, rofs, m, tmp, "a", tmp, "z",
				where), vfs.ErrROFS, rofsWhy},
			{o, `Remove(tmp, "a") on the read-only mount`, stkRemoveFn(e, rofs, o, tmp, "a", where), vfs.ErrROFS,
				rofsWhy},
		}
		for _, k := range cases {
			what := stkWhat(k)
			oaWant(t, what, k.fn(what), k.want, k.why, where)
		}
		pwWantFound(e, rofs, tmp, "tmp/a", "a", g.aID, "the refused calls on the read-only mount", rofsWhy, where)
	})

	t.Run("diverged", func(t *testing.T) {
		const where = "K8, diverged, ADR 0026 sections 2 and 8; ADR 0025 section 6, A9's recipe"
		g := stkGateSetup(t, where)
		e, fs, m, o := g.e, g.fs, pwMember, sa24Other

		epoch := e.read(fs, "reading FS.epoch after the setup's Sync", where).epoch
		if _, err := uc43WriteForeignRoot(e.ctx, e.h.inner, epoch+1, uc43Writer); err != nil {
			t.Fatalf("setup: writing a foreign root pointer on the inner store: %v (%s)", err, where)
		}
		if err := e.commit(fs, "c2", where); err == nil || !strings.Contains(err.Error(), "commit conflict") {
			t.Fatalf("setup: the Sync of the commit of c2, after another writer moved the root pointer, = %v, want "+
				"an error whose text contains %q (ADR 0022 section 4; %s)", err, "commit conflict", where)
		}
		if got := e.mode(fs, where); got != mountDiverged {
			t.Fatalf("setup: after the commit of c2, f.mode.State() = %v, want %v (ADR 0022 section 3; %s)", got,
				mountDiverged, where)
		}

		const staleWhy = "a diverged mount takes no change, for every caller, the entry's owner included: Remove " +
			"and Rename call mutable() at stage A, before the row is asked (ADR 0026 section 2; ADR 0014 section 7)"
		cases := []stkCase{
			{m, `Remove(tmp, "a") on the diverged mount`, stkRemoveFn(e, fs, m, g.tmp, "a", where), vfs.ErrStale,
				staleWhy},
			{m, `Rename(tmp, "a", tmp, "z") on the diverged mount`, stkRenameFn(e, fs, m, g.tmp, "a", g.tmp, "z",
				where), vfs.ErrStale, staleWhy},
			{o, `Remove(tmp, "a") on the diverged mount`, stkRemoveFn(e, fs, o, g.tmp, "a", where), vfs.ErrStale,
				staleWhy},
		}
		for _, k := range cases {
			what := stkWhat(k)
			oaWant(t, what, k.fn(what), k.want, k.why, where)
		}
	})
}

// TestAnEntryThatNamesNoInodeIsAbsentToTheStickyRule is K9 of ADR 0026
// section 8 (section 2: a name that names no object describes no entry), a
// characterisation that passes before the change and after. On dg24Setup's
// mount, whose root holds ghost, naming no inode: testCaller's
// SetAttr(root, {Mode: 0o1777}) returns nil, and sa24Other makes the file mine
// in the root. Then sa24Other's Remove(root, "ghost"), Rmdir(root, "ghost")
// and Rename(root, "ghost", root, "x") return vfs.ErrNoEnt, with FS.changes
// unchanged across the three; and its Rename(root, "mine", root, "ghost")
// returns nil, after which Lookup(root, "ghost") finds mine's file id.
func TestAnEntryThatNamesNoInodeIsAbsentToTheStickyRule(t *testing.T) {
	const where = "K9, ADR 0026 sections 2 and 8; ADR 0024 sections 3 and 7"
	dg := dg24Setup(t, where)
	e, fs, root, o := dg.e, dg.fs, dg.root, sa24Other

	what := `setup: testCaller's SetAttr(root, {Mode: 0o1777})`
	if r := sa24SetAttr(e, fs, testCaller, root, oaMode(0o1777), what, where); r.err != nil || r.v.Mode != 0o1777 {
		t.Fatalf("%s = (mode %#o, %s), want (mode 0o1777, nil): OpChmod allows the owner any mode, and a Mode is "+
			"applied masked to 0o7777 (ADR 0014 section 5; ADR 0026 section 8, the facts; %s)", what, r.v.Mode,
			oaStatus(r.err), where)
	}
	what = `setup: sa24Other's Create("mine", exclusive)`
	mine := pwCreate(e, fs, o, root, "mine", vfs.SetAttr{}, true, what, where)
	pwMust(e, mine, what, where)

	c := sa24Read(e, fs, "reading FS.changes before the calls on ghost", where).changes
	const absent = "a name whose entry names an inode the table lacks names no object, so it describes no entry, " +
		"the row judges the directory alone, and stage E answers vfs.ErrNoEnt (ADR 0026 section 2; ADR 0024 " +
		"section 3)"
	cases := []stkCase{
		{o, `Remove(root, "ghost")`, stkRemoveFn(e, fs, o, root, dg24Ghost, where), vfs.ErrNoEnt, absent},
		{o, `Rmdir(root, "ghost")`, stkRmdirFn(e, fs, o, root, dg24Ghost, where), vfs.ErrNoEnt, absent},
		{o, `Rename(root, "ghost", root, "x")`, stkRenameFn(e, fs, o, root, dg24Ghost, root, "x", where),
			vfs.ErrNoEnt, absent},
	}
	for _, k := range cases {
		what := stkWhat(k)
		oaWant(t, what, k.fn(what), k.want, k.why, where)
	}
	if got := sa24Read(e, fs, "reading FS.changes after the calls on ghost", where).changes; got != c {
		t.Errorf("after sa24Other's Remove, Rmdir and Rename of ghost, FS.changes is %d, want %d, its value before "+
			"them: each returned an error and changed nothing (ADR 0026 section 4; ADR 0024 sections 1 and 3; %s)",
			got, c, where)
	}

	what = `sa24Other's Rename(root, "mine", root, "ghost")`
	oaWant(t, what, pwRename(e, fs, o, root, "mine", root, dg24Ghost, what, where), nil,
		"a Rename onto a name whose entry names no inode replaces that entry with no replaced entry for the row "+
			"to judge, in a sticky directory as in any other, and mine is sa24Other's own (ADR 0026 section 2; "+
			"Assumption 12; ADR 0017 section 2)", where)
	pwWantFound(e, fs, root, dg24Ghost, dg24Ghost, mine.a.FileID, what,
		"the Rename put mine's object under the name ghost (ADR 0024 section 3; ADR 0017 section 2)", where)
}
