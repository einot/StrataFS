package blobfs

// Clean-room tests for #97, written without reading the implementation, from
// ADR 0027 (docs/adr/0027-access-grants-only-what-the-operations-allow.md)
// sections 1 (the tables of the requests each bit names), 2 (OpExecute,
// Request.Advisory and AccessBits' procedure), 7 (how blobfs asks) and 10
// (X11), ADR 0014 sections 5 and 11 (W1 and the harness), ADR 0026 section 7
// (W2's tree), and internal/vfs/vfs.go.
//
// Covered, with the name ADR 0027 section 10 pins: X11,
// TestAccessAsksThePolicyTheOperationsRequests. It names policy.OpExecute and
// policy.Request's Advisory, so it builds only with coder's change; X8 to X10,
// which build on 4ded55e's tree, are in access_test.go. Helpers of this file's
// own are prefixed aw.
//
// The safety rules (ADR 0027 section 10; ADR 0014 section 11; ADR 0011
// section 7): every bucket is a *store.Local under the test's t.TempDir(),
// behind an unarmed uc43Store, through uc43Setup; every mount is made with
// uc43Config, through sp24Mount or e.newFSWith; every New and every FS call
// runs in uc43Go and is waited for under uc43Bound; nothing sleeps, polls or
// runs in parallel; FS.dirty and FS.changes are read only holding FS.mu for
// reading, through sa24Read. FS.perm is written as W1 allows: once on each
// mount, right after New returns and before any other call on it, in uc43Go.
// The recorders allow more than Unix(); every call made under them but Access
// is a Lookup in a directory, made as setup, and Access may be asked of any
// object under any policy (ADR 0027 section 7).

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// awWant is one request X11 expects: its Op and its Object. Everything else
// about it is ADR 0027 section 2's: Caller the caller, Dst the Object on an
// OpRename and the zero Object on every other, Advisory true, UID and GID
// zero, and Entry and Replaced nil.
type awWant struct {
	op  policy.Op
	obj policy.Object
}

// awOpName names an Op, OpExecute included, for failure messages.
func awOpName(op policy.Op) string {
	if op == policy.OpExecute {
		return "OpExecute"
	}
	return pwOpName(op)
}

// awDescribe spells a list of requests, every field, for failure messages.
func awDescribe(reqs []policy.Request) string {
	if len(reqs) == 0 {
		return "nothing"
	}
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = fmt.Sprintf("%s{Caller uid %d gid %d gids %v, Object %s, Dst %s, UID %d, GID %d, Entry %s, "+
			"Replaced %s, Advisory %v}", awOpName(r.Op), r.Caller.UID, r.Caller.GID, r.Caller.GIDs,
			pwObjString(r.Object), pwObjString(r.Dst), r.UID, r.GID, pwObjPtrString(r.Entry),
			pwObjPtrString(r.Replaced), r.Advisory)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// awDescribeWant spells a list of expected requests for failure messages.
func awDescribeWant(want []awWant) string {
	if len(want) == 0 {
		return "nothing"
	}
	parts := make([]string, len(want))
	for i, w := range want {
		parts[i] = awOpName(w.op) + " on " + pwObjString(w.obj)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// awWantRequests requires got to be exactly want, in order, each as ADR 0027
// section 2 builds it, made by c.
func awWantRequests(t *testing.T, what string, got []policy.Request, want []awWant, c vfs.Caller, why,
	where string) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(want); i++ {
		g, w := got[i], want[i]
		var dst policy.Object
		if w.op == policy.OpRename {
			dst = w.obj
		}
		switch {
		case g.Op != w.op, g.Object != w.obj, g.Dst != dst:
			ok = false
		case g.Caller.UID != c.UID, g.Caller.GID != c.GID, !slices.Equal(g.Caller.GIDs, c.GIDs):
			ok = false
		case !g.Advisory, g.UID != 0, g.GID != 0, g.Entry != nil, g.Replaced != nil:
			ok = false
		}
	}
	if ok {
		return
	}
	t.Errorf("%s asked the policy %s; want exactly %s, in that order, each with Caller uid %d gid %d gids %v, Dst "+
		"the Object on the OpRename and the zero Object on every other, Advisory true, UID and GID zero, and Entry "+
		"and Replaced nil: %s (%s)", what, awDescribe(got), awDescribeWant(want), c.UID, c.GID, c.GIDs, why, where)
}

// awRow is one row of X11: an Access by testCaller of h, asking for ask, with
// the recorder refusing the request at index refuse; the mask it must return,
// with a nil error; and the requests it must make.
type awRow struct {
	what   string
	h      vfs.Handle
	ask    uint32
	refuse int
	mask   uint32
	reqs   []awWant
}

// awMountReadOnly runs New over e's bucket with Config.ReadOnly set and, right
// after New returns and before any other call on the FS, replaces FS.perm with
// p (ADR 0014 section 11, W1). It returns the FS and its root's handle.
func awMountReadOnly(e *uc43Env, p policy.Policy, where string) (*FS, vfs.Handle) {
	e.t.Helper()
	cfg := uc43Config(e.h)
	cfg.ReadOnly = true
	fs := e.newFSWith(cfg, where)
	uc43Go(e, func() struct{} {
		fs.perm = p
		return struct{}{}
	}).wait(e.t, "replacing FS.perm right after New returned, on the read-only mount", where)
	return fs, e.root(fs, where)
}

// TestAccessAsksThePolicyTheOperationsRequests is X11 of ADR 0027 section 10
// (sections 1, 2 and 7). sp24Mount with an sp24Recorder, and W2's tree
// (pwW2Tree), whose Objects are ADR 0026 section 7's D (d, {TypeDir, 0o750}),
// D2 (d2, {TypeDir, 0o1770}), F (d/f, {TypeReg, 0o644}) and LN (d/ln,
// {TypeLnk, 0o777}), each owned by 501:20. For each row the recorder is reset
// to refuse the index given; FS.changes and FS.dirty are read; testCaller
// calls Access; the mask is the one given, with a nil error; the recorder
// holds exactly the requests listed, each as X2 describes them; and FS.changes
// and FS.dirty are as they were, since Access changes nothing.
//
// Access(d, every bit), index -1: R L M E D, with OpReadDir, OpLookup,
// OpRename, OpAddEntry and OpRemoveEntry, each on D; index 0: all but R; index
// 2: all but M; index 4: all but D; the same five requests each time.
// Access(f, every bit), index -1: R M E X, with OpRead, OpWrite, OpTruncate,
// OpWrite and OpExecute, each on F; index 1: R E X, with OpRead, OpWrite,
// OpWrite and OpExecute; index 2: R E X, with the five; index 3: R M X; index
// 4: R M E. Access(f, R): R, with OpRead alone. Access(f, L|D), Access(d, X)
// and Access(f, 0): 0, with no request. Access(d2, D): D, with one
// OpRemoveEntry on D2 and no Entry. Access(ln, every bit): R M E X, with the
// five requests on LN.
//
// Then a read-only mount of the same bucket, with a recorder installed the
// same way: Access(d, every bit) gives R L, with OpReadDir and OpLookup alone,
// and Access(f, every bit) gives R X, with OpRead and OpExecute alone.
func TestAccessAsksThePolicyTheOperationsRequests(t *testing.T) {
	const where = "X11, ADR 0027 sections 1, 2, 7 and 10"
	e := uc43Setup(t)
	rec := &sp24Recorder{refuseAt: -1}
	fs, root := sp24Mount(e, policy.Func(rec.check), where)
	s := pwW2Tree(e, fs, root, where)
	tc := testCaller

	R, L, M, E, D, X := vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute
	all := pwAllAccess
	dObj, d2Obj, fObj, lnObj := pwW2D, pwW2D2, pwReg, pwLnk
	dirFive := []awWant{
		{policy.OpReadDir, dObj}, {policy.OpLookup, dObj}, {policy.OpRename, dObj}, {policy.OpAddEntry, dObj},
		{policy.OpRemoveEntry, dObj},
	}
	fileFive := func(o policy.Object) []awWant {
		return []awWant{
			{policy.OpRead, o}, {policy.OpWrite, o}, {policy.OpTruncate, o}, {policy.OpWrite, o},
			{policy.OpExecute, o},
		}
	}
	fFive := fileFive(fObj)

	rows := []awRow{
		{"Access(d, every bit)", s.d, all, -1, R | L | M | E | D, dirFive},
		{"Access(d, every bit)", s.d, all, 0, L | M | E | D, dirFive},
		{"Access(d, every bit)", s.d, all, 2, R | L | E | D, dirFive},
		{"Access(d, every bit)", s.d, all, 4, R | L | M | E, dirFive},
		{"Access(f, every bit)", s.f, all, -1, R | M | E | X, fFive},
		{"Access(f, every bit)", s.f, all, 1, R | E | X,
			[]awWant{{policy.OpRead, fObj}, {policy.OpWrite, fObj}, {policy.OpWrite, fObj}, {policy.OpExecute, fObj}}},
		{"Access(f, every bit)", s.f, all, 2, R | E | X, fFive},
		{"Access(f, every bit)", s.f, all, 3, R | M | X, fFive},
		{"Access(f, every bit)", s.f, all, 4, R | M | E, fFive},
		{"Access(f, R)", s.f, R, -1, R, []awWant{{policy.OpRead, fObj}}},
		{"Access(f, L|D)", s.f, L | D, -1, 0, nil},
		{"Access(d, X)", s.d, X, -1, 0, nil},
		{"Access(f, 0)", s.f, 0, -1, 0, nil},
		{"Access(d2, D)", s.d2, D, -1, D, []awWant{{policy.OpRemoveEntry, d2Obj}}},
		{"Access(ln, every bit)", s.ln, all, -1, R | M | E | X, fileFive(lnObj)},
	}
	const why = "Access asks f.perm, through AccessBits, the requests ADR 0027 section 1 lists for each bit asked, " +
		"in order, each marked Advisory and describing no entry; a bit's requests stop at its first refusal; and a " +
		"bit is granted only if all of them were allowed (ADR 0027 sections 1, 2 and 7; section 10, X11)"

	for _, r := range rows {
		what := fmt.Sprintf("testCaller's %s, %s", r.what, pwRefusing(r.refuse))
		rec.reset(r.refuse)
		before := sa24Read(e, fs, "reading FS.dirty and FS.changes before "+what, where)
		got := pwAccess(e, fs, tc, r.h, r.ask, what, where)
		taken := rec.taken()
		if got.err != nil || got.v != r.mask {
			t.Errorf("%s = (%#x, %s), want (%#x, nil): %s (%s)", what, got.v, pwStatus(got.err), r.mask, why, where)
		}
		awWantRequests(t, what, taken, r.reqs, tc, why, where)
		after := sa24Read(e, fs, "reading FS.dirty and FS.changes after "+what, where)
		if after.changes != before.changes || after.dirty != before.dirty {
			t.Errorf("after %s, FS.changes is %d and FS.dirty %v, want %d and %v, as they were before the call: "+
				"Access changes nothing (ADR 0027 section 7; section 10, X11; %s)", what, after.changes, after.dirty,
				before.changes, before.dirty, where)
		}
	}

	// A read-only mount of the same bucket, which W2's tree's Sync committed.
	rec2 := &sp24Recorder{refuseAt: -1}
	rofs, rroot := awMountReadOnly(e, policy.Func(rec2.check), where)
	ld := e.lookup(rofs, rroot, "d", where)
	if ld.err != nil {
		t.Fatalf("on the read-only mount, Lookup of \"d\" = %s, want nil: W2's tree's Sync committed it (%s)",
			pwStatus(ld.err), where)
	}
	lf := e.lookup(rofs, ld.h, "f", where)
	if lf.err != nil {
		t.Fatalf("on the read-only mount, Lookup of \"d/f\" = %s, want nil: W2's tree's Sync committed it (%s)",
			pwStatus(lf.err), where)
	}
	const roWhy = "on a mount that takes no change AccessBits asks nothing for M, E or D and grants none of them, " +
		"and asks the rest as on a writable mount (ADR 0027 sections 2 and 4; section 10, X11)"
	roRows := []awRow{
		{"Access(d, every bit) on the read-only mount", ld.h, all, -1, R | L,
			[]awWant{{policy.OpReadDir, dObj}, {policy.OpLookup, dObj}}},
		{"Access(f, every bit) on the read-only mount", lf.h, all, -1, R | X,
			[]awWant{{policy.OpRead, fObj}, {policy.OpExecute, fObj}}},
	}
	for _, r := range roRows {
		what := fmt.Sprintf("testCaller's %s, %s", r.what, pwRefusing(r.refuse))
		rec2.reset(r.refuse)
		got := pwAccess(e, rofs, tc, r.h, r.ask, what, where)
		taken := rec2.taken()
		if got.err != nil || got.v != r.mask {
			t.Errorf("%s = (%#x, %s), want (%#x, nil): %s (%s)", what, got.v, pwStatus(got.err), r.mask, roWhy, where)
		}
		awWantRequests(t, what, taken, r.reqs, tc, roWhy, where)
	}
}
