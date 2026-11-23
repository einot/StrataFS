package policy_test

// Clean-room tests for #94, "The sticky bit on a directory is not honoured",
// written from ADR 0026,
// docs/adr/0026-in-a-sticky-directory-only-an-owner-removes-or-renames-an-entry.md,
// §1 (the rule, the two rows, and what they read), §2 (Request.Entry and
// Request.Replaced, Growth and purity) and §8 (the test surface), from ADR 0014
// §5 for the policy API, Permits and the other rows, and from
// internal/vfs/vfs.go, without reading the implementation.
//
// ADR 0026 §1's rows, each read from the top, the first line that holds giving
// the answer; a directory is sticky when Mode & 0o1000 != 0, root is
// Caller.UID == 0, and a caller owns an object when Caller.UID equals its UID:
//
//	OpRemoveEntry: (1) vfs.ErrNotDir unless Object is a directory;
//	               (2) vfs.ErrAcces unless Permits(Caller, Object,
//	               PermWrite|PermExec); (3) vfs.ErrPerm if Entry is not nil,
//	               Object is sticky, and the caller is not root, does not own
//	               Object and does not own *Entry; (4) nil.
//	OpRename:      (1) vfs.ErrNotDir unless Object and Dst are both
//	               directories; (2) vfs.ErrAcces unless Permits gives write
//	               and search on both; (3) as OpRemoveEntry's line 3; (4)
//	               vfs.ErrPerm if Replaced is not nil, Dst is sticky, and the
//	               caller is not root, does not own Dst and does not own
//	               *Replaced; (5) nil.
//
// Covered, with the names ADR 0026 §8 pins:
//   - K1, TestTheStickyRowsExhaustively;
//   - K3's first part, TestTheStickyRuleReadsOnlyTheEntrysOwner;
//   - K4, TestNoCheckWritesThroughEntryOrReplaced.
//
// K2's rows are in policy_test.go's unixCases() and p5Cases(), so that P4, P5
// and P10 to P13 ask them, and K3's second part is two loops in
// policy_test.go's TestUnreadFieldsChangeNothing.
//
// These reuse policy_test.go's fixtures: the callers, whose uids are 0, 1000,
// 1000, 2000, 3000 and 4000; fileUID and fileGID; p2Callers and p2Modes;
// mkObj, whose objects are owned by 1000:100, and foreignDir, whose
// directories are owned by 9999:999; oraclePermits, which stands for Permits;
// limiter; opName; unixCases and p5Cases; and owner_access_test.go's
// oaAllTypes. Helpers of this file's own are prefixed stk. Every refusal is
// compared with == against the bare vfs.Status. Each request's Entry and
// Replaced point to a value of the request's own, so that no check can change
// what another check is asked.
//
// Safety: the tests here call pure functions only. None touches a store or a
// file, sleeps, polls or runs in parallel, and each exhaustive loop reports
// through limiter, so that a wrong rule does not print every case.
//
// Not relied on: U5 and U6, and U3 where Permits allows, which ADR 0026 §8
// leaves unpinned; no test here asks AccessBits or a file row.

import (
	"fmt"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// stkEntryUIDs are the owners K1 gives Entry and Replaced: root, the
// fixture callers' uids, foreignDir's owner, and the largest uid (ADR 0026
// §8, K1).
var stkEntryUIDs = []uint32{0, 1000, 2000, 3000, 4000, 9999, 4294967295}

// stkEntries are K1's values for Entry and Replaced: nil, and a regular file
// of mode 0o644 in fileGID owned by each of stkEntryUIDs. A nil *Object in
// the result is "none".
func stkEntries() []*policy.Object {
	es := []*policy.Object{nil}
	for _, u := range stkEntryUIDs {
		es = append(es, &policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: u, GID: fileGID})
	}
	return es
}

// stkOwn returns a pointer to a copy of what o points to, or nil for nil, so
// that each request is given a value of its own. It is policy_test.go's
// stkCloneObject.
func stkOwn(o *policy.Object) *policy.Object { return stkCloneObject(o) }

// stkIsSticky is ADR 0026 §1's "sticky": Mode & 0o1000 != 0.
func stkIsSticky(o policy.Object) bool { return o.Mode&0o1000 != 0 }

// stkMayTakeOut is the negation of the condition of ADR 0026 §1's lines 3
// and 4: with no entry, or in a directory that is not sticky, a name may be
// taken out; otherwise only by root, by the directory's owner, or by the
// entry's owner.
func stkMayTakeOut(c vfs.Caller, dir policy.Object, entry *policy.Object) bool {
	if entry == nil || !stkIsSticky(dir) {
		return true
	}
	return c.UID == 0 || c.UID == dir.UID || c.UID == entry.UID
}

// stkOracle is ADR 0026 §1's two rows, written from their text, with
// oraclePermits standing for Permits. It answers OpRemoveEntry and OpRename
// only.
func stkOracle(r policy.Request) error {
	const wx = policy.PermWrite | policy.PermExec
	switch r.Op {
	case policy.OpRemoveEntry:
		if r.Object.Type != vfs.TypeDir {
			return vfs.ErrNotDir
		}
		if !oraclePermits(r.Caller, r.Object, wx) {
			return vfs.ErrAcces
		}
		if !stkMayTakeOut(r.Caller, r.Object, r.Entry) {
			return vfs.ErrPerm
		}
		return nil
	case policy.OpRename:
		if r.Object.Type != vfs.TypeDir || r.Dst.Type != vfs.TypeDir {
			return vfs.ErrNotDir
		}
		if !oraclePermits(r.Caller, r.Object, wx) || !oraclePermits(r.Caller, r.Dst, wx) {
			return vfs.ErrAcces
		}
		if !stkMayTakeOut(r.Caller, r.Object, r.Entry) {
			return vfs.ErrPerm
		}
		if !stkMayTakeOut(r.Caller, r.Dst, r.Replaced) {
			return vfs.ErrPerm
		}
		return nil
	}
	panic(fmt.Sprintf("stkOracle asked %s, which it does not answer", opName(r.Op)))
}

// stkPtr spells an *Object for failure messages.
func stkPtr(o *policy.Object) string {
	if o == nil {
		return "nil"
	}
	return "&" + stkObj(*o)
}

// stkObj spells an Object for failure messages.
func stkObj(o policy.Object) string {
	return fmt.Sprintf("{Type %d, Mode %#o, UID %d, GID %d}", o.Type, o.Mode, o.UID, o.GID)
}

// stkRenameModes are K1's modes for OpRename's directories: 0o777, 0o770,
// 0o707, 0o077, 0o300, 0o030, 0o003, 0o555 and 0, each with and without
// 0o1000, and 0o7777 and 0o6777 (ADR 0026 §8, K1).
func stkRenameModes() []uint32 {
	var ms []uint32
	for _, m := range []uint32{0o777, 0o770, 0o707, 0o077, 0o300, 0o030, 0o003, 0o555, 0} {
		ms = append(ms, m, m|0o1000)
	}
	return append(ms, 0o7777, 0o6777)
}

// TestTheStickyRowsExhaustively is K1 of ADR 0026 §8: Unix().Check is
// compared, with ==, against stkOracle, an oracle written from §1's rows with
// oraclePermits standing for Permits, over:
//
//   - OpRemoveEntry: every caller of p2Callers(); as Object, for every mode of
//     p2Modes(), mkObj(vfs.TypeDir, mode) and foreignDir(mode), and for every
//     type of oaAllTypes but vfs.TypeDir, mkObj(type, 0o1777) and
//     mkObj(type, 0o777); as Entry, nil and a pointer to a regular file of
//     mode 0o644 in fileGID owned by each of 0, 1000, 2000, 3000, 4000, 9999
//     and 4294967295.
//   - OpRename: every caller of p2Callers(); as Object and as Dst,
//     independently, mkObj(vfs.TypeDir, mode) and foreignDir(mode) for each
//     of stkRenameModes(); as Entry and as Replaced, independently, nil and
//     the seven pointers above.
func TestTheStickyRowsExhaustively(t *testing.T) {
	u := policy.Unix()
	l := &limiter{t: t, limit: 20}
	defer l.done()
	entries := stkEntries()

	check := func(r policy.Request) {
		t.Helper()
		want := stkOracle(r)
		if err := u.Check(r); err != want {
			l.errorf("Unix().Check(%s by %+v, Object %s, Dst %s, Entry %s, Replaced %s) = %#v (%v), want %v, "+
				"compared with ==: ADR 0026 §1's rows, read from the top (ADR 0026 §8, K1)", opName(r.Op), r.Caller,
				stkObj(r.Object), stkObj(r.Dst), stkPtr(r.Entry), stkPtr(r.Replaced), err, err, want)
		}
	}

	var removeDirs []policy.Object
	for _, mode := range p2Modes() {
		removeDirs = append(removeDirs, mkObj(vfs.TypeDir, mode), foreignDir(mode))
	}
	for _, typ := range oaAllTypes {
		if typ == vfs.TypeDir {
			continue
		}
		removeDirs = append(removeDirs, mkObj(typ, 0o1777), mkObj(typ, 0o777))
	}
	for _, nc := range p2Callers() {
		for _, o := range removeDirs {
			for _, en := range entries {
				check(policy.Request{Op: policy.OpRemoveEntry, Caller: nc.c, Object: o, Entry: stkOwn(en)})
			}
		}
	}

	var renameDirs []policy.Object
	for _, mode := range stkRenameModes() {
		renameDirs = append(renameDirs, mkObj(vfs.TypeDir, mode), foreignDir(mode))
	}
	for _, nc := range p2Callers() {
		for _, src := range renameDirs {
			for _, dst := range renameDirs {
				for _, en := range entries {
					for _, rp := range entries {
						check(policy.Request{Op: policy.OpRename, Caller: nc.c, Object: src, Dst: dst,
							Entry: stkOwn(en), Replaced: stkOwn(rp)})
					}
				}
			}
		}
	}
}

// stkVariants are K3's Entries of owner uid: a pointer to an Object for every
// type of oaAllTypes, every mode of 0, 0o644, 0o1777, 0o7777 and 4294967295,
// and every gid of 0, fileGID, 400 and 4294967295 (ADR 0026 §8, K3).
func stkVariants(uid uint32) []policy.Object {
	var objs []policy.Object
	for _, typ := range oaAllTypes {
		for _, mode := range []uint32{0, 0o644, 0o1777, 0o7777, 4294967295} {
			for _, gid := range []uint32{0, fileGID, 400, 4294967295} {
				objs = append(objs, policy.Object{Type: typ, Mode: mode, UID: uid, GID: gid})
			}
		}
	}
	return objs
}

// TestTheStickyRuleReadsOnlyTheEntrysOwner is K3's first part, of ADR 0026
// §8 (§1, "What the rows read": of *Entry and *Replaced the rows read the UID
// and nothing else, not the type, the mode or the group). For every caller of
// p2Callers(), each directory of mkObj(vfs.TypeDir, 0o1777),
// foreignDir(0o1777) and foreignDir(0o777), and each owner uid of 1000, 4000
// and 9999, the answer is the same for every Entry of that uid, over
// stkVariants(uid), as for a regular file of mode 0o644 in fileGID owned by
// it:
//
//   - OpRemoveEntry with that directory as Object;
//   - OpRename's Entry, with that directory as Object, foreignDir(0o777) as
//     Dst and no Replaced;
//   - OpRename's Replaced, with that directory as Dst, foreignDir(0o777) as
//     Object and no Entry.
func TestTheStickyRuleReadsOnlyTheEntrysOwner(t *testing.T) {
	u := policy.Unix()
	l := &limiter{t: t, limit: 20}
	defer l.done()
	dirs := []policy.Object{mkObj(vfs.TypeDir, 0o1777), foreignDir(0o1777), foreignDir(0o777)}
	plain := foreignDir(0o777)

	for _, nc := range p2Callers() {
		for _, dir := range dirs {
			for _, uid := range []uint32{1000, 4000, 9999} {
				ref := policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: uid, GID: fileGID}
				shapes := []struct {
					name string
					mk   func(o *policy.Object) policy.Request
				}{
					{"OpRemoveEntry's Entry", func(o *policy.Object) policy.Request {
						return policy.Request{Op: policy.OpRemoveEntry, Caller: nc.c, Object: dir, Entry: o}
					}},
					{"OpRename's Entry, with Dst foreignDir(0o777)", func(o *policy.Object) policy.Request {
						return policy.Request{Op: policy.OpRename, Caller: nc.c, Object: dir, Dst: plain, Entry: o}
					}},
					{"OpRename's Replaced, with Object foreignDir(0o777)", func(o *policy.Object) policy.Request {
						return policy.Request{Op: policy.OpRename, Caller: nc.c, Object: plain, Dst: dir, Replaced: o}
					}},
				}
				for _, sh := range shapes {
					base := u.Check(sh.mk(stkOwn(&ref)))
					for _, v := range stkVariants(uid) {
						if err := u.Check(sh.mk(stkOwn(&v))); err != base {
							l.errorf("Unix().Check, %s, by %s, in %s: with %s it is %v, and with %s it is %v, want "+
								"the same: of the entry the rows read the UID alone, not the type, the mode or the "+
								"group (ADR 0026 §1, What the rows read; §8, K3)", sh.name, nc.name, stkObj(dir),
								stkPtr(&v), err, stkPtr(&ref), base)
						}
					}
				}
			}
		}
	}
}

// TestNoCheckWritesThroughEntryOrReplaced is K4 of ADR 0026 §8 (§2, Purity: a
// policy modifies nothing it is given, what Entry and Replaced point to
// included). For every K2 request, which are the requests of unixCases() and
// p5Cases() that set Entry or Replaced: after the Check of Unix(), of
// All(Unix(), Func(allow)) and of Func(allow), what Entry and Replaced point
// to equals a copy taken before the call.
func TestNoCheckWritesThroughEntryOrReplaced(t *testing.T) {
	allow := policy.Func(func(policy.Request) error { return nil })
	checks := []struct {
		name string
		p    policy.Policy
	}{
		{"Unix()", policy.Unix()},
		{"All(Unix(), Func(allow))", policy.All(policy.Unix(), allow)},
		{"Func(allow)", allow},
	}
	n := 0
	for _, tc := range append(unixCases(), p5Cases()...) {
		if tc.req.Entry == nil && tc.req.Replaced == nil {
			continue
		}
		n++
		for _, c := range checks {
			r := tc.req
			r.Entry, r.Replaced = stkOwn(tc.req.Entry), stkOwn(tc.req.Replaced)
			entry, replaced := stkOwn(r.Entry), stkOwn(r.Replaced)
			_ = c.p.Check(r)
			if !stkSameObject(r.Entry, entry) {
				t.Errorf("%s: %s.Check left what Entry points to %s, want %s, as before the call: a policy "+
					"modifies nothing it is given (ADR 0026 §2, Purity; §8, K4)", tc.name, c.name, stkPtr(r.Entry),
					stkPtr(entry))
			}
			if !stkSameObject(r.Replaced, replaced) {
				t.Errorf("%s: %s.Check left what Replaced points to %s, want %s, as before the call: a policy "+
					"modifies nothing it is given (ADR 0026 §2, Purity; §8, K4)", tc.name, c.name,
					stkPtr(r.Replaced), stkPtr(replaced))
			}
		}
	}
	if n == 0 {
		t.Fatalf("test bug: unixCases() and p5Cases() hold no request that sets Entry or Replaced, so K4 asked " +
			"nothing (ADR 0026 §8, K2 and K4)")
	}
}
