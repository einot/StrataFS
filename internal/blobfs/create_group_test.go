package blobfs

// Clean-room tests for #98, "CREATE, MKDIR and SYMLINK apply a requested
// group from any caller", written without reading the implementation, from
// ADR 0028
// (docs/adr/0028-a-group-asked-for-at-creation-is-applied-only-for-root-or-a-member.md)
// sections 1 to 4 and 8 (G2 and G3, and the facts a test may rely on), ADR
// 0014 sections 5 and 11 (Request's fields, W1, W2 and the safety rule), the
// safety rules of ADR 0011 section 7 and ADR 0023 section 11, and
// internal/vfs/vfs.go.
//
// Covered, with the names ADR 0028 section 8 pins:
//   - G2, TestARequestedGroupAtCreationIsAppliedOnlyForRootOrAMember: with
//     Unix() in place, a Create, Mkdir or Symlink that makes an object gives
//     it the group asked for only if the caller is root or in that group, and
//     otherwise makes it in the caller's own group and succeeds; a mode asked
//     for keeps its set-ID bits; a uid asked for goes by its own verdict; a
//     name that exists is left as it is; and what is made is committed so;
//   - G3, TestARequestedGroupAtCreationGoesThroughOpChgrpAndARefusalIsIgnored:
//     a creating call asks OpAddEntry on the directory and then, only if it
//     is about to make an object, OpChown for a uid asked for and OpChgrp for
//     a group asked for, each on the object as it would be made with neither
//     (N), each before the call changes anything; an allowed one is applied,
//     a refused one is left unapplied and fails nothing.
//
// G1 is in internal/policy/create_group_test.go and G4 in
// internal/nfs/create_group_test.go. Helpers of this file's own are prefixed
// cg; it reuses uc43Env and its methods, the pw helpers, the sa24 helpers and
// sa24Other, sp24Recorder, sp24Mount and sp24Root, and testCaller.
//
// The safety rules (ADR 0028 section 8; ADR 0014 section 11; ADR 0023 section
// 11; ADR 0011 section 7): every bucket is a *store.Local under the test's
// t.TempDir(), behind an unarmed uc43Store, through uc43Setup; every mount is
// made with uc43Config; every New and every FS call runs in uc43Go and is
// waited for under uc43Bound; nothing sleeps, polls or runs in parallel.
// FS.perm is replaced only through sp24Mount, once, right after New returns,
// and no other FS field is written. FS.dirty and FS.changes are read holding
// FS.mu for reading, through sa24Read, or, in G3, by the policy itself, which
// runs in the goroutine that holds FS.mu, reads FS.changes without taking a
// lock, takes no lock of the FS and calls no method of it (ADR 0028 section
// 8, the facts). Every call made under G3's recorder, which allows more than
// Unix(), is a Create, Mkdir, Symlink or Lookup of the directory d, or a
// GetAttr, which consults no policy (ADR 0014 section 11, W5).
//
// Not covered, on purpose: ADR 0014 section 11's rows U3 and U6, which stay
// unpinned (no Read, Write or size of a symlink is asked, and no time is set);
// and by how much FS.changes advances on a creation, which ADR 0028 section 8
// does not pin.

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// cgMulti is ADR 0028 section 8's caller in the groups 40 and 41 beside its
// own, 30.
var cgMulti = vfs.Caller{UID: 504, GID: 30, GIDs: []uint32{40, 41}}

// cgNone, given to cgSA, leaves a field of the SetAttr unset.
const cgNone int64 = -1

// cgSA returns a SetAttr that sets the mode, the uid and the gid given, each
// unless it is cgNone, and nothing else. Each call makes new pointers, so no
// two calls share a value.
func cgSA(mode, uid, gid int64) vfs.SetAttr {
	var sa vfs.SetAttr
	if mode != cgNone {
		sa.Mode = sa24U32(uint32(mode))
	}
	if uid != cgNone {
		sa.UID = sa24U32(uint32(uid))
	}
	if gid != cgNone {
		sa.GID = sa24U32(uint32(gid))
	}
	return sa
}

// cgShape is what G2 and G3 check of an object: its type, its mode, its owner
// and its group.
type cgShape struct {
	typ      vfs.FileType
	mode     uint32
	uid, gid uint32
}

func cgShapeOf(a vfs.Attr) cgShape { return cgShape{a.Type, a.Mode, a.UID, a.GID} }

func (s cgShape) String() string {
	return fmt.Sprintf("{Type %d, Mode %#o, owned by %d:%d}", s.typ, s.mode, s.uid, s.gid)
}

// cgWho names a caller for failure messages.
func cgWho(c vfs.Caller) string {
	switch {
	case c.UID == testCaller.UID && c.GID == testCaller.GID && len(c.GIDs) == 0:
		return "testCaller (501:20)"
	case c.UID == sa24Other.UID && c.GID == sa24Other.GID && len(c.GIDs) == 0:
		return "sa24Other (502:30)"
	case c.UID == cgMulti.UID && c.GID == cgMulti.GID && slices.Equal(c.GIDs, cgMulti.GIDs):
		return "cgMulti (504:30, in 40 and 41)"
	case c.UID == 0 && c.GID == 0 && len(c.GIDs) == 0:
		return "sp24Root (0:0)"
	}
	return fmt.Sprintf("vfs.Caller{UID: %d, GID: %d, GIDs: %v}", c.UID, c.GID, c.GIDs)
}

// cgCall is one of the three creating calls.
type cgCall int

const (
	cgCreate cgCall = iota
	cgMkdir
	cgSymlink
)

func (k cgCall) String() string {
	switch k {
	case cgCreate:
		return "Create"
	case cgMkdir:
		return "Mkdir"
	}
	return "Symlink"
}

// shape returns the type k makes and the mode it gives with no Mode asked
// for: 0o644 for a regular file, 0o755 for a directory and 0o777 for a
// symlink (ADR 0028 section 8, the facts), owned by uid:gid.
func (k cgCall) shape(uid, gid uint32) cgShape {
	switch k {
	case cgCreate:
		return cgShape{vfs.TypeReg, 0o644, uid, gid}
	case cgMkdir:
		return cgShape{vfs.TypeDir, 0o755, uid, gid}
	}
	return cgShape{vfs.TypeLnk, 0o777, uid, gid}
}

// spell spells k's call of name in the directory dir, for failure messages:
// an exclusive Create, or a Symlink to target.
func (k cgCall) spell(dir, name, target string, sa vfs.SetAttr) string {
	switch k {
	case cgCreate:
		return fmt.Sprintf("Create(%s, %q, %s, exclusive)", dir, name, sa24Describe(sa))
	case cgMkdir:
		return fmt.Sprintf("Mkdir(%s, %q, %s)", dir, name, sa24Describe(sa))
	}
	return fmt.Sprintf("Symlink(%s, %q, %q, %s)", dir, name, target, sa24Describe(sa))
}

// cgMake runs k's call of name in dir, as c, with sa: an exclusive Create, a
// Mkdir, or a Symlink to target.
func cgMake(e *uc43Env, fs *FS, k cgCall, c vfs.Caller, dir vfs.Handle, name, target string, sa vfs.SetAttr,
	what, where string) pwEntry {
	e.t.Helper()
	switch k {
	case cgCreate:
		return pwCreate(e, fs, c, dir, name, sa, true, what, where)
	case cgMkdir:
		return pwMkdir(e, fs, c, dir, name, sa, what, where)
	}
	return pwSymlink(e, fs, c, dir, name, target, sa, what, where)
}

// cgWantMade requires r, a creating call, to have returned nil and attributes
// of the shape want, and GetAttr of the handle it returned to report the same
// shape and file id. It reports whether all of that held.
func cgWantMade(e *uc43Env, fs *FS, r pwEntry, want cgShape, what, why, where string) bool {
	e.t.Helper()
	if r.err != nil {
		e.t.Errorf("%s = %s, want nil, an object %s: %s (%s)", what, pwStatus(r.err), want, why, where)
		return false
	}
	ok := true
	if got := cgShapeOf(r.a); got != want {
		e.t.Errorf("%s returned attributes %s, want %s: %s (%s)", what, got, want, why, where)
		ok = false
	}
	g := pwGetAttr(e, fs, r.h, "GetAttr of the handle returned by "+what, where)
	if g.err != nil {
		e.t.Errorf("after %s, GetAttr of the handle it returned = %s, want nil: the object exists (%s)", what,
			pwStatus(g.err), where)
		return false
	}
	if got := cgShapeOf(g.v); got != want || g.v.FileID != r.a.FileID {
		e.t.Errorf("after %s, GetAttr of the handle it returned reports %s with file id %d, want %s with file id %d, "+
			"as the call returned: %s (%s)", what, got, g.v.FileID, want, r.a.FileID, why, where)
		ok = false
	}
	return ok
}

// cgWantStill requires GetAttr of h, the object path names, to report the
// shape want, after what.
func cgWantStill(e *uc43Env, fs *FS, h vfs.Handle, path string, want cgShape, after, why, where string) {
	e.t.Helper()
	g := pwGetAttr(e, fs, h, fmt.Sprintf("GetAttr of %q after %s", path, after), where)
	if g.err != nil || cgShapeOf(g.v) != want {
		e.t.Errorf("after %s, GetAttr of %q = (%s, %s), want (%s, nil): %s (%s)", after, path, cgShapeOf(g.v),
			pwStatus(g.err), want, why, where)
	}
}

// TestARequestedGroupAtCreationIsAppliedOnlyForRootOrAMember is G2 of ADR
// 0028 section 8: the rule of section 1 through the FS, with Unix() in place.
//
// testCaller makes pub, a directory of mode 0o777, in the root, and in it x,
// a regular file with no Mode. Then each row of section 8's first table is
// made as an exclusive Create, as a Mkdir and as a Symlink to "x", under a
// name of its own in pub, and each row of its second table as the call it
// names: each returns nil; the attributes it returns and GetAttr of its handle
// report the type of the kind made, the mode the row gives or else the kind's
// own, and the owner and group the row gives; and testCaller's Lookup of the
// name in pub finds the same file id. The issue's case is the second table's
// first row: sa24Other's Create with {Mode 0o2775, GID 20} makes a file of
// mode 0o2775 in its own group, 502:30, not in group 20.
//
// A name that exists: sa24Other's non-exclusive Create of x with {GID 30},
// and sp24Root's with {UID 7, GID 999}, return nil with x's file id, still
// 501:20; sa24Other's exclusive Create, Mkdir and Symlink of x with {GID 30}
// return vfs.ErrExist; and x is still 501:20. A directory the caller may not
// write: sa24Other's Create of n in the root, of mode 0o755, returns
// vfs.ErrAcces, and n is not there. Last, a Sync returns nil, and on a
// remount Lookup in pub of the names the first table's third and sixth rows
// made as Create, its eighth as Mkdir, and the second table's first, report
// the same type, mode, owner and group.
func TestARequestedGroupAtCreationIsAppliedOnlyForRootOrAMember(t *testing.T) {
	const where = "G2, ADR 0028 sections 1, 3, 4, 5, 6 and 8"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)

	const pubWhat = `setup: testCaller's Mkdir(root, "pub", {Mode 0o777})`
	pub := pwMust(e, pwMkdir(e, fs, testCaller, root, "pub", cgSA(0o777, cgNone, cgNone), pubWhat, where), pubWhat,
		where)
	pwSetupObject(e, fs, pub, "pub", policy.Object{Type: vfs.TypeDir, Mode: 0o777, UID: 501, GID: 20}, -1, where)
	const xWhat = `setup: testCaller's Create(pub, "x", {}, exclusive)`
	x := pwMust(e, pwCreate(e, fs, testCaller, pub, "x", vfs.SetAttr{}, true, xWhat, where), xWhat, where)
	xAttr := pwSetupObject(e, fs, x, "pub/x", pwReg, 0, where)
	xShape := cgShapeOf(xAttr)

	// committed holds what each name in pub must be on the remount.
	committed := map[string]cgShape{}
	const found = "the name made is in the directory and names the object made (ADR 0028 section 3)"

	// The first table: each row for Create, Mkdir and Symlink alike.
	const none = "with no group asked for, the object is in its caller's own GID (ADR 0028 section 5)"
	const own = "a caller is always in its own GID, so the group asked for is applied (ADR 0028 sections 1 and 5)"
	const member = "the group asked for is among the caller's GIDs, so the caller is in it and the group is " +
		"applied (ADR 0028 section 1)"
	const outside = "the caller is neither root nor in the group asked for, so the group is left unapplied: the " +
		"object is made in the caller's own GID and the call succeeds (ADR 0028 sections 1, 4 and 5)"
	const rootAny = "root may give a new object any group (ADR 0028 section 1)"
	const rootNone = "root asking for no group makes the object in its own GID, which for vfs.Caller{UID: 0} is " +
		"0 (ADR 0028 sections 5 and 8, the facts)"
	first := []struct {
		c        vfs.Caller
		ask      int64
		uid, gid uint32
		why      string
	}{
		{sa24Other, cgNone, 502, 30, none},
		{sa24Other, 30, 502, 30, own},
		{sa24Other, 20, 502, 30, outside},
		{sa24Other, 40, 502, 30, outside},
		{cgMulti, 40, 504, 40, member},
		{cgMulti, 41, 504, 41, member},
		{cgMulti, 20, 504, 30, outside + "; its GID, 30, not one of its GIDs"},
		{sp24Root, 20, 0, 20, rootAny},
		{sp24Root, 999, 0, 999, rootAny + ", one no caller is in"},
		{sp24Root, cgNone, 0, 0, rootNone},
	}
	prefixes := map[cgCall]string{cgCreate: "c", cgMkdir: "m", cgSymlink: "l"}
	for _, k := range []cgCall{cgCreate, cgMkdir, cgSymlink} {
		for i, r := range first {
			name := fmt.Sprintf("%s%d", prefixes[k], i+1)
			sa := cgSA(cgNone, cgNone, r.ask)
			what := fmt.Sprintf("the first table's row %d, %s's %s", i+1, cgWho(r.c), k.spell("pub", name, "x", sa))
			want := k.shape(r.uid, r.gid)
			committed[name] = want
			res := cgMake(e, fs, k, r.c, pub, name, "x", sa, what, where)
			if cgWantMade(e, fs, res, want, what, r.why, where) {
				pwWantFound(e, fs, pub, "pub/"+name, name, res.a.FileID, what, found, where)
			}
		}
	}

	// The second table: with a mode, and with a uid.
	second := []struct {
		k    cgCall
		c    vfs.Caller
		sa   vfs.SetAttr
		want cgShape
		why  string
	}{
		{cgCreate, sa24Other, cgSA(0o2775, cgNone, 20), cgShape{vfs.TypeReg, 0o2775, 502, 30},
			"the issue's case: the file with the set-group-ID bit is made in its creator's group, not in group 20, " +
				"which its creator is not in, and the mode asked for keeps its set-ID bits (ADR 0028 sections 1, 4 " +
				"and 6)"},
		{cgMkdir, sa24Other, cgSA(0o2775, cgNone, 20), cgShape{vfs.TypeDir, 0o2775, 502, 30},
			"a directory with the set-group-ID bit asked for by a caller outside group 20 is made in the caller's " +
				"group, with the mode asked for (ADR 0028 sections 1, 4 and 6)"},
		{cgCreate, cgMulti, cgSA(0o2755, cgNone, 41), cgShape{vfs.TypeReg, 0o2755, 504, 41},
			"a caller in group 41 may make a set-group-ID file in it (ADR 0028 sections 1 and 6)"},
		{cgCreate, sa24Other, cgSA(cgNone, 7, 20), cgShape{vfs.TypeReg, 0o644, 502, 30},
			"a uid that is not the caller's own is left unapplied for a caller that is not root, and so is a group " +
				"it is not in; the call succeeds (ADR 0028 sections 1 and 4; ADR 0014 section 6)"},
		{cgCreate, cgMulti, cgSA(cgNone, 7, 41), cgShape{vfs.TypeReg, 0o644, 504, 41},
			"each attribute goes by its own verdict: the uid 7 is left unapplied, and the group 41, which the " +
				"caller is in, is applied (ADR 0028 sections 2 and 4)"},
		{cgCreate, sa24Other, cgSA(cgNone, 502, 30), cgShape{vfs.TypeReg, 0o644, 502, 30},
			"the caller's own uid and own group are both its to give (ADR 0028 sections 1 and 8, the facts)"},
		{cgCreate, sp24Root, cgSA(cgNone, 7, 8), cgShape{vfs.TypeReg, 0o644, 7, 8},
			"root may give a new object any owner and any group, and the OpChgrp is judged on the object as it " +
				"would be made with neither, whatever the uid asked for (ADR 0028 sections 1 and 2)"},
	}
	for i, r := range second {
		name := fmt.Sprintf("s%d", i+1)
		what := fmt.Sprintf("the second table's row %d, %s's %s", i+1, cgWho(r.c), r.k.spell("pub", name, "x", r.sa))
		committed[name] = r.want
		res := cgMake(e, fs, r.k, r.c, pub, name, "x", r.sa, what, where)
		if cgWantMade(e, fs, res, r.want, what, r.why, where) {
			pwWantFound(e, fs, pub, "pub/"+name, name, res.a.FileID, what, found, where)
		}
	}

	// A name that exists.
	const keep = "a creating call of a name the directory has makes no object and applies none of sa, so the " +
		"object keeps its owner and its group (ADR 0028 sections 2 and 7; ADR 0014 section 6)"
	nonExcl := []struct {
		c  vfs.Caller
		sa vfs.SetAttr
	}{
		{sa24Other, cgSA(cgNone, cgNone, 30)},
		{sp24Root, cgSA(cgNone, 7, 999)},
	}
	for _, r := range nonExcl {
		what := fmt.Sprintf("%s's Create(pub, %q, %s, non-exclusive) of a name that exists", cgWho(r.c), "x",
			sa24Describe(r.sa))
		res := pwCreate(e, fs, r.c, pub, "x", r.sa, false, what, where)
		switch {
		case res.err != nil:
			t.Errorf("%s = %s, want nil with x's handle: a non-exclusive Create of a name that names a regular "+
				"file returns it (ADR 0028 section 8; %s)", what, pwStatus(res.err), where)
		case res.a.FileID != xAttr.FileID || cgShapeOf(res.a) != xShape:
			t.Errorf("%s returned file id %d, %s, want x's, file id %d, %s: %s (%s)", what, res.a.FileID,
				cgShapeOf(res.a), xAttr.FileID, xShape, keep, where)
		}
		cgWantStill(e, fs, x, "pub/x", xShape, what, keep, where)
	}
	exist := []struct {
		k   cgCall
		how string
	}{
		{cgCreate, `Create(pub, "x", {GID 30}, exclusive)`},
		{cgMkdir, `Mkdir(pub, "x", {GID 30})`},
		{cgSymlink, `Symlink(pub, "x", "x", {GID 30})`},
	}
	for _, r := range exist {
		what := "sa24Other's " + r.how + " of a name that exists"
		res := cgMake(e, fs, r.k, sa24Other, pub, "x", "x", cgSA(cgNone, cgNone, 30), what, where)
		if res.err != vfs.ErrExist {
			t.Errorf("%s = %s, want vfs.ErrExist, compared with ==: an exclusive Create, a Mkdir or a Symlink of a "+
				"name the directory has is refused at stage E (ADR 0028 section 3; %s)", what, pwStatus(res.err),
				where)
		}
		cgWantStill(e, fs, x, "pub/x", xShape, what, keep, where)
	}

	// A directory the caller may not write.
	const nWhat = `sa24Other's Create(root, "n", {GID 30}, exclusive), the root of mode 0o755 owned by 501:20`
	nRes := pwCreate(e, fs, sa24Other, root, "n", cgSA(cgNone, cgNone, 30), true, nWhat, where)
	if nRes.err != vfs.ErrAcces {
		t.Errorf("%s = %s, want vfs.ErrAcces, compared with ==: OpAddEntry is asked first, and its refusal is "+
			"returned unchanged, whatever group is asked for (ADR 0028 sections 2 and 3; %s)", nWhat,
			pwStatus(nRes.err), where)
	}
	pwWantNoEnt(e, fs, root, "n", "n", nWhat, "a creating call that returns an error has changed nothing (ADR 0028 "+
		"section 3)", where)

	// What is committed.
	if err := e.sync(fs, "the Sync after G2's calls", where); err != nil {
		t.Fatalf("the Sync after G2's calls = %v, want nil against a healthy store (%s)", err, where)
	}
	rm := e.remount(where)
	if rm == nil {
		return
	}
	rmRoot := e.root(rm, where)
	l := pwLookup(e, rm, testCaller, rmRoot, "pub", `testCaller's Lookup of "pub" in the remount's root`, where)
	if l.err != nil {
		t.Errorf("testCaller's Lookup of %q in the remount's root = %s, want nil: the Sync committed it (%s)", "pub",
			pwStatus(l.err), where)
		return
	}
	for _, name := range []string{"c3", "c6", "m8", "s1"} {
		want := committed[name]
		what := fmt.Sprintf("testCaller's Lookup of %q in the remount", "pub/"+name)
		r := pwLookup(e, rm, testCaller, l.h, name, what, where)
		if r.err != nil || cgShapeOf(r.a) != want {
			t.Errorf("%s = (%s, %s), want (%s, nil): the object is committed with the owner, group and mode it was "+
				"made with (ADR 0028 sections 3 and 8; ADR 0023 section 3; %s)", what, cgShapeOf(r.a), pwStatus(r.err),
				want, where)
		}
	}
}

// cgChanges is G3's note of FS.changes as the policy finds it. note runs in
// the policy, in the goroutine that holds FS.mu for the call that asks it,
// so it reads FS.changes of the FS it is armed with directly, taking no lock
// of that FS and calling no method of it (ADR 0028 section 8, the facts);
// what it notes it keeps under its own mutex. Until arm is called it notes
// nothing.
type cgChanges struct {
	mu    sync.Mutex
	fs    *FS
	notes []uint64
}

func (n *cgChanges) note() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fs != nil {
		n.notes = append(n.notes, n.fs.changes)
	}
}

// arm sets the FS whose FS.changes note reads, and forgets every note.
func (n *cgChanges) arm(fs *FS) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fs = fs
	n.notes = nil
}

// reset forgets every note.
func (n *cgChanges) reset() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notes = nil
}

// taken returns a copy of the notes made since the last reset, one for each
// request, in the order asked.
func (n *cgChanges) taken() []uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]uint64(nil), n.notes...)
}

// cgReq is one request G3 expects: its Op and Object, and the UID of an
// OpChown or the GID of an OpChgrp, 0 for any other.
type cgReq struct {
	op  policy.Op
	obj policy.Object
	uid uint32
	gid uint32
}

// cgDescribe spells a list of expected requests for failure messages.
func cgDescribe(want []cgReq) string {
	parts := make([]string, len(want))
	for i, w := range want {
		parts[i] = fmt.Sprintf("%s on %s with UID %d and GID %d", pwOpName(w.op), pwObjString(w.obj), w.uid, w.gid)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// cgWantRequests requires got to be exactly want, in order: each with the Op,
// Object, UID and GID want gives; Caller caller, its UID, GID and every one
// of its GIDs; Dst the zero Object; Entry and Replaced nil; and Advisory
// false (ADR 0028 section 2, "The fields").
func cgWantRequests(t *testing.T, what string, got []policy.Request, want []cgReq, caller vfs.Caller, why,
	where string) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(want); i++ {
		g, w := got[i], want[i]
		switch {
		case g.Op != w.op, g.Object != w.obj, g.UID != w.uid, g.GID != w.gid:
			ok = false
		case g.Dst != (policy.Object{}), g.Entry != nil, g.Replaced != nil, g.Advisory:
			ok = false
		case g.Caller.UID != caller.UID, g.Caller.GID != caller.GID, !slices.Equal(g.Caller.GIDs, caller.GIDs):
			ok = false
		}
	}
	if ok {
		return
	}
	t.Errorf("%s asked the policy %s; want exactly %s, in that order, each with Caller uid %d gid %d gids %v, Dst "+
		"the zero Object, Entry and Replaced nil, and Advisory false (ADR 0028 section 2): %s (%s)", what,
		pwDescribe(got), cgDescribe(want), caller.UID, caller.GID, caller.GIDs, why, where)
}

// cgRow is one row of G3: a call on d, its caller, the index the recorder
// refuses, what the call must return, the requests it must make, whether it
// makes an object, and the shape of the object it makes or, for a name that
// exists, of the object name already names.
type cgRow struct {
	what    string
	c       vfs.Caller
	refuse  int
	call    func(what string) pwEntry
	wantErr error
	name    string
	makes   bool
	shape   cgShape
	want    []cgReq
	why     string
}

// TestARequestedGroupAtCreationGoesThroughOpChgrpAndARefusalIsIgnored is G3
// of ADR 0028 section 8: the requests a creating call makes, and what each
// answer does.
//
// sp24Mount installs a policy.Func that, for each request, first notes
// FS.changes as it finds it (cgChanges) and then returns an sp24Recorder's
// check. Refusing nothing, testCaller makes W2's tree (pwW2Tree), whose d has
// the Object D, {TypeDir, 0o750, 501, 20} (pwW2D), and the note is armed.
// Every call is on d, as ADR 0014 section 11's safety rule asks. For each of
// section 8's fifteen rows the recorder is reset to refuse the index given, or
// none for -1; FS.changes is read, c; the call is made, by testCaller unless
// the row names another caller; and then: the recorder holds exactly the
// row's requests, in order, with every field section 2 gives and no other set;
// at every one of them the policy found FS.changes equal to c; and after a
// call that made an object FS.changes is above c and the attributes returned
// and GetAttr report the type, mode, owner and group given, while after a
// call that made nothing, an error or a non-exclusive Create of a name that
// exists, FS.changes is c and the object the name names is as it was. N is
// {TypeReg, 0o644, 501, 20}.
func TestARequestedGroupAtCreationGoesThroughOpChgrpAndARefusalIsIgnored(t *testing.T) {
	const where = "G3, ADR 0028 sections 2, 3, 4 and 8; ADR 0014 sections 5 and 11"
	e := uc43Setup(t)
	rec := &sp24Recorder{refuseAt: -1}
	notes := &cgChanges{}
	fs, root := sp24Mount(e, policy.Func(func(r policy.Request) error {
		notes.note()
		return rec.check(r)
	}), where)
	s := pwW2Tree(e, fs, root, where)
	notes.arm(fs)

	tc := testCaller
	D := pwW2D
	N := policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: 501, GID: 20}
	add := cgReq{op: policy.OpAddEntry, obj: D}
	chgrp := func(o policy.Object, gid uint32) cgReq { return cgReq{op: policy.OpChgrp, obj: o, gid: gid} }
	chown := func(o policy.Object, uid uint32) cgReq { return cgReq{op: policy.OpChown, obj: o, uid: uid} }
	create := func(c vfs.Caller, name string, sa vfs.SetAttr, excl bool) func(string) pwEntry {
		return func(what string) pwEntry { return pwCreate(e, fs, c, s.d, name, sa, excl, what, where) }
	}
	mkdir := func(name string, sa vfs.SetAttr) func(string) pwEntry {
		return func(what string) pwEntry { return pwMkdir(e, fs, tc, s.d, name, sa, what, where) }
	}
	symlink := func(name string, sa vfs.SetAttr) func(string) pwEntry {
		return func(what string) pwEntry { return pwSymlink(e, fs, tc, s.d, name, "f", sa, what, where) }
	}
	g8 := cgSA(cgNone, cgNone, 8)
	u7g8 := func() vfs.SetAttr { return cgSA(cgNone, 7, 8) }
	three := []cgReq{add, chown(N, 7), chgrp(N, 8)}
	const policys = "the decision is the policy's: testCaller is not in group 8 and Unix() would refuse it, but the " +
		"policy installed allows it, and the group is applied (ADR 0028 sections 1 and 2)"
	const exists = "a call that makes no object asks OpAddEntry alone and applies none of sa (ADR 0028 section 2)"

	rows := []cgRow{
		{`testCaller's Create(d, "g1", {GID 8}, exclusive)`, tc, -1, create(tc, "g1", g8, true), nil, "g1", true,
			cgShape{vfs.TypeReg, 0o644, 501, 8}, []cgReq{add, chgrp(N, 8)}, policys},
		{`testCaller's Create(d, "g2", {GID 8}, exclusive)`, tc, 1, create(tc, "g2", cgSA(cgNone, cgNone, 8), true),
			nil, "g2", true, cgShape{vfs.TypeReg, 0o644, 501, 20}, []cgReq{add, chgrp(N, 8)},
			"a refused OpChgrp leaves the group unapplied and fails nothing: the file is made in the caller's own " +
				"group (ADR 0028 section 4)"},
		{`testCaller's Mkdir(d, "g3", {GID 8})`, tc, -1, mkdir("g3", cgSA(cgNone, cgNone, 8)), nil, "g3", true,
			cgShape{vfs.TypeDir, 0o755, 501, 8},
			[]cgReq{add, chgrp(policy.Object{Type: vfs.TypeDir, Mode: 0o755, UID: 501, GID: 20}, 8)},
			"Mkdir asks OpChgrp as Create does, on the directory as it would be made (ADR 0028 section 2)"},
		{`testCaller's Symlink(d, "g4", "f", {GID 8})`, tc, -1, symlink("g4", cgSA(cgNone, cgNone, 8)), nil, "g4",
			true, cgShape{vfs.TypeLnk, 0o777, 501, 8},
			[]cgReq{add, chgrp(policy.Object{Type: vfs.TypeLnk, Mode: 0o777, UID: 501, GID: 20}, 8)},
			"Symlink asks OpChgrp as Create does, on the symlink as it would be made (ADR 0028 section 2)"},
		{`testCaller's Create(d, "g5", {Mode 0o2750, GID 8}, exclusive)`, tc, -1,
			create(tc, "g5", cgSA(0o2750, cgNone, 8), true), nil, "g5", true, cgShape{vfs.TypeReg, 0o2750, 501, 8},
			[]cgReq{add, chgrp(policy.Object{Type: vfs.TypeReg, Mode: 0o2750, UID: 501, GID: 20}, 8)},
			"the OpChgrp's Object has the mode the call chose, masked to 0o7777, set-ID bits included (ADR 0028 " +
				"sections 2 and 6)"},
		{`testCaller's Create(d, "g6", {UID 7, GID 8}, exclusive)`, tc, -1, create(tc, "g6", u7g8(), true), nil,
			"g6", true, cgShape{vfs.TypeReg, 0o644, 7, 8}, three,
			"OpChown then OpChgrp, both on N, and both allowed, so both are applied (ADR 0028 sections 2 and 3)"},
		{`testCaller's Create(d, "g7", {UID 7, GID 8}, exclusive)`, tc, 1, create(tc, "g7", u7g8(), true), nil,
			"g7", true, cgShape{vfs.TypeReg, 0o644, 501, 8}, three,
			"the OpChgrp is asked after a refused OpChown, on N, whose uid is the caller's whatever becomes of the " +
				"uid asked for; the uid is left unapplied and the group applied (ADR 0028 sections 2 and 4)"},
		{`testCaller's Create(d, "g8", {UID 7, GID 8}, exclusive)`, tc, 2, create(tc, "g8", u7g8(), true), nil,
			"g8", true, cgShape{vfs.TypeReg, 0o644, 7, 20}, three,
			"the OpChgrp's Object is N, with uid 501, though the uid asked for is applied; the group is left " +
				"unapplied and the uid applied (ADR 0028 sections 2 and 4)"},
		{`testCaller's Create(d, "g9", {UID 7, GID 8}, exclusive)`, tc, 0, create(tc, "g9", u7g8(), true),
			vfs.ErrNXIO, "g9", false, cgShape{}, []cgReq{add},
			"a refused OpAddEntry is returned unchanged, and nothing after it is asked (ADR 0028 section 2)"},
		{`sp24Root's Create(d, "g10", {GID 8}, exclusive)`, sp24Root, -1,
			create(sp24Root, "g10", cgSA(cgNone, cgNone, 8), true), nil, "g10", true,
			cgShape{vfs.TypeReg, 0o644, 0, 8},
			[]cgReq{add, chgrp(policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: 0, GID: 0}, 8)},
			"N is the object as root would make it, owned by 0:0 (ADR 0028 section 2)"},
		{`cgMulti's Create(d, "g11", {GID 8}, exclusive)`, cgMulti, -1,
			create(cgMulti, "g11", cgSA(cgNone, cgNone, 8), true), nil, "g11", true,
			cgShape{vfs.TypeReg, 0o644, 504, 8},
			[]cgReq{add, chgrp(policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: 504, GID: 30}, 8)},
			"N is owned by the caller's UID and GID, and the request's Caller carries its GIDs (ADR 0028 section 2)"},
		{`testCaller's Create(d, "g1", {UID 7, GID 9}, non-exclusive) of a name that exists`, tc, -1,
			create(tc, "g1", cgSA(cgNone, 7, 9), false), nil, "g1", false, cgShape{vfs.TypeReg, 0o644, 501, 8},
			[]cgReq{add}, exists + ": it returns the object the name names, as it is"},
		{`testCaller's Create(d, "g1", {GID 9}, exclusive) of a name that exists`, tc, -1,
			create(tc, "g1", cgSA(cgNone, cgNone, 9), true), vfs.ErrExist, "g1", false,
			cgShape{vfs.TypeReg, 0o644, 501, 8}, []cgReq{add}, exists},
		{`testCaller's Mkdir(d, "g3", {GID 9}) of a name that exists`, tc, -1, mkdir("g3", cgSA(cgNone, cgNone, 9)),
			vfs.ErrExist, "g3", false, cgShape{vfs.TypeDir, 0o755, 501, 8}, []cgReq{add}, exists},
		{`testCaller's Symlink(d, "g4", "f", {GID 9}) of a name that exists`, tc, -1,
			symlink("g4", cgSA(cgNone, cgNone, 9)), vfs.ErrExist, "g4", false, cgShape{vfs.TypeLnk, 0o777, 501, 8},
			[]cgReq{add}, exists},
	}

	made := map[string]pwEntry{}
	for i, r := range rows {
		what := fmt.Sprintf("row %d, %s, %s", i+1, r.what, pwRefusing(r.refuse))
		rec.reset(r.refuse)
		notes.reset()
		c := sa24Read(e, fs, "reading FS.changes before "+what, where).changes
		res := r.call(what)
		got := rec.taken()
		seen := notes.taken()

		if res.err != r.wantErr {
			t.Errorf("%s = %s, want %s, compared with ==: %s (%s)", what, pwStatus(res.err), pwStatus(r.wantErr),
				r.why, where)
		}
		cgWantRequests(t, what, got, r.want, r.c, r.why, where)
		if len(seen) != len(got) {
			t.Errorf("%s: the policy noted FS.changes %d times but recorded %d requests, want one note for each "+
				"request (%s)", what, len(seen), len(got), where)
		}
		for j, v := range seen {
			if v != c {
				t.Errorf("%s: at its request %d the policy found FS.changes %d, want %d, its value before the call: "+
					"every request is asked before the call changes anything, allocInode included (ADR 0028 "+
					"section 3; %s)", what, j, v, c, where)
			}
		}
		after := sa24Read(e, fs, "reading FS.changes after "+what, where).changes
		switch {
		case r.makes && after <= c:
			t.Errorf("after %s, FS.changes is %d, want above %d, its value before the call: a creating call that "+
				"returns nil has marked the namespace (ADR 0028 section 8, the facts; %s)", what, after, c, where)
		case !r.makes && after != c:
			t.Errorf("after %s, FS.changes is %d, want %d, its value before the call: a call that returns an error, "+
				"or a non-exclusive Create that returns an object the name already names, changes nothing (ADR "+
				"0028 section 8, the facts; %s)", what, after, c, where)
		}

		rec.reset(-1)
		switch {
		case r.makes:
			if cgWantMade(e, fs, res, r.shape, what, r.why, where) {
				made[r.name] = res
			}
		case r.wantErr == vfs.ErrNXIO:
			pwWantNoEnt(e, fs, s.d, "d/"+r.name, r.name, what, "a refused OpAddEntry makes no entry (ADR 0028 "+
				"sections 2 and 3)", where)
		default:
			prev, ok := made[r.name]
			if !ok {
				t.Errorf("%s: cannot check %q, which an earlier row failed to make (%s)", what, "d/"+r.name, where)
				continue
			}
			if r.wantErr == nil && res.err == nil && (res.a.FileID != prev.a.FileID || cgShapeOf(res.a) != r.shape) {
				t.Errorf("%s returned file id %d, %s, want %q's, file id %d, %s: %s (%s)", what, res.a.FileID,
					cgShapeOf(res.a), "d/"+r.name, prev.a.FileID, r.shape, r.why, where)
			}
			cgWantStill(e, fs, prev.h, "d/"+r.name, r.shape, what, r.why, where)
		}
	}
}
