package policy_test

// Clean-room tests for #97, "ACCESS grants what the operations then refuse",
// written from ADR 0027,
// docs/adr/0027-access-grants-only-what-the-operations-allow.md, §1 (the rule
// and its two tables), §2 (OpExecute, Request.Advisory, AccessBits and the two
// laws), §3 to §6, §8 (the property) and §10 (X1 to X5), from ADR 0014 §5,
// ADR 0025 §1 and ADR 0026 §1 and §2 for the rows AccessBits asks, and from
// internal/vfs/vfs.go, without reading the implementation.
//
// ADR 0027 §1's tables: the requests each bit names, in order. A bit with none
// is never granted and asks nothing, and a bit that names a change (M, E and
// D) asks nothing and is never granted when mutable is false:
//
//	a directory:    R OpReadDir; L OpLookup; M OpRename, with the directory
//	                as Dst too; E OpAddEntry; D OpRemoveEntry; X none.
//	anything else:  R OpRead; L none; M OpWrite, then OpTruncate; E OpWrite;
//	                D none; X OpExecute.
//
// Each request is Request{Op: op, Caller: c, Object: o, Advisory: true}, with
// Dst o on the OpRename and nothing else set. AccessBits goes through R, L, M,
// E, D and X in that order, asks each wanted bit's requests in order, stops a
// bit at its first refusal, whatever the error, and grants the bit only if
// every one of them allowed (ADR 0027 §2).
//
// Covered, with the names ADR 0027 §10 pins:
//   - X1, TestAccessBitsExhaustively;
//   - X2, TestAccessBitsAsksTheOperationsOwnRequests;
//   - X3, TestWhatAccessBitsGrantsTheRowsAllow;
//   - X4, TestAnAdvisoryRequestIsAnsweredByTheModeAlone;
//   - X5, TestTheExecuteRowExhaustively.
//
// X6's rows are in policy_test.go's unixCases() and p5Cases(), so that P4, P5
// and P10 to P13 ask them; X7 is the rest of policy_test.go's changes, P13's
// loop over Advisory among them; and A4, in owner_access_test.go, calls the
// new AccessBits.
//
// These reuse policy_test.go's fixtures: the callers and p2Callers; p2Modes;
// mkObj, foreignDir, mkReq, mkRename, stkOwned, stkRemove, stkRename and
// stkCloneObject; oraclePermits, which stands for Permits; limiter; opName;
// and allAccess; and owner_access_test.go's oaAllTypes, oaFileOps and
// oaRowPerm. Helpers of this file's own are prefixed ac. Every refusal a test
// pins is compared with == against the bare vfs.Status.
//
// Safety: the tests here call pure functions only. None touches a store or a
// file, sleeps, polls or runs in parallel; the recording policy keeps what it
// is asked under its own lock; and each exhaustive loop reports through
// limiter, so that a wrong rule does not print every case.
//
// Not relied on: U6. U3 is pinned whole since ADR 0029 §9: a request that is
// not advisory, of an object that is neither a regular file nor a directory,
// is vfs.ErrInval (owner_access_test.go's A1, ADR 0029's N1), and the same
// request marked is answered by the mode, nil or vfs.ErrAcces (X4 here, ADR
// 0029's N2). The law that an advisory request only narrows is asked here of
// regular files and directories alone, and stays there for good: for any
// other type it is false since ADR 0029 §3, where an advisory request is
// allowed wherever the mode allows it and the same request unmarked is
// always refused.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// acBits are the six vfs.Access bits in the order AccessBits goes through
// them: READ, LOOKUP, MODIFY, EXTEND, DELETE, EXECUTE (ADR 0027 §2).
var acBits = []uint32{
	vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete, vfs.AccessExecute,
}

// acLetters are the letters ADR 0027 §10 writes acBits with.
var acLetters = []string{"R", "L", "M", "E", "D", "X"}

// acChange is the bits that name a change: MODIFY, EXTEND and DELETE (ADR
// 0027 §1).
const acChange = vfs.AccessModify | vfs.AccessExtend | vfs.AccessDelete

// acOps is ADR 0027 §1's table: the Ops of the requests bit names for an
// object of type typ, in the table's order, or nil for a bit with none. The
// directory's table is taken for vfs.TypeDir, and the other for every other
// type, valid or not.
func acOps(typ vfs.FileType, bit uint32) []policy.Op {
	if typ == vfs.TypeDir {
		switch bit {
		case vfs.AccessRead:
			return []policy.Op{policy.OpReadDir}
		case vfs.AccessLookup:
			return []policy.Op{policy.OpLookup}
		case vfs.AccessModify:
			return []policy.Op{policy.OpRename}
		case vfs.AccessExtend:
			return []policy.Op{policy.OpAddEntry}
		case vfs.AccessDelete:
			return []policy.Op{policy.OpRemoveEntry}
		}
		return nil
	}
	switch bit {
	case vfs.AccessRead:
		return []policy.Op{policy.OpRead}
	case vfs.AccessModify:
		return []policy.Op{policy.OpWrite, policy.OpTruncate}
	case vfs.AccessExtend:
		return []policy.Op{policy.OpWrite}
	case vfs.AccessExecute:
		return []policy.Op{policy.OpExecute}
	}
	return nil
}

// acAsks is ADR 0027 §2's procedure, run against a policy that refuses the Op
// refused and allows every other (refused 0 refuses none): the Ops it asks,
// in order, and the mask it grants, for an object of type typ, the mask want
// and mutable.
func acAsks(typ vfs.FileType, want uint32, mutable bool, refused policy.Op) ([]policy.Op, uint32) {
	var ops []policy.Op
	var mask uint32
	for _, bit := range acBits {
		if want&bit == 0 || (!mutable && bit&acChange != 0) {
			continue
		}
		reqs := acOps(typ, bit)
		if len(reqs) == 0 {
			continue
		}
		granted := true
		for _, op := range reqs {
			ops = append(ops, op)
			if op == refused {
				granted = false
				break
			}
		}
		if granted {
			mask |= bit
		}
	}
	return ops, mask
}

// acUnixOracle is ADR 0027 §1's table "Under Unix()", with oraclePermits
// standing for Permits. A directory: R with read, L with search, and M, E and
// D with write and search on a mount that takes changes. Anything else: R with
// read, M and E with write on a mount that takes changes, and X with execute.
// No other bit.
func acUnixOracle(c vfs.Caller, o policy.Object, mutable bool) uint32 {
	var m uint32
	if oraclePermits(c, o, policy.PermRead) {
		m |= vfs.AccessRead
	}
	if o.Type == vfs.TypeDir {
		if oraclePermits(c, o, policy.PermExec) {
			m |= vfs.AccessLookup
		}
		if mutable && oraclePermits(c, o, policy.PermWrite|policy.PermExec) {
			m |= acChange
		}
		return m
	}
	if mutable && oraclePermits(c, o, policy.PermWrite) {
		m |= vfs.AccessModify | vfs.AccessExtend
	}
	if oraclePermits(c, o, policy.PermExec) {
		m |= vfs.AccessExecute
	}
	return m
}

// acOldOracle is ADR 0014 §5's AccessBits rule, the one in force at 4ded55e,
// with oraclePermits standing for Permits: R with read; L for a directory with
// read or execute; M and E with write; D for a directory with write; X for a
// non-directory with execute and some execute bit set.
func acOldOracle(c vfs.Caller, o policy.Object) uint32 {
	read := oraclePermits(c, o, policy.PermRead)
	write := oraclePermits(c, o, policy.PermWrite)
	exec := oraclePermits(c, o, policy.PermExec)
	var m uint32
	if read {
		m |= vfs.AccessRead
	}
	if write {
		m |= vfs.AccessModify | vfs.AccessExtend
	}
	if o.Type == vfs.TypeDir {
		if read || exec {
			m |= vfs.AccessLookup
		}
		if write {
			m |= vfs.AccessDelete
		}
		return m
	}
	if exec && o.Mode&0o111 != 0 {
		m |= vfs.AccessExecute
	}
	return m
}

// acMask spells a mask by ADR 0027 §10's letters, and any bit outside the six
// in hex.
func acMask(m uint32) string {
	if m == 0 {
		return "nothing (0)"
	}
	var parts []string
	for i, b := range acBits {
		if m&b != 0 {
			parts = append(parts, acLetters[i])
		}
	}
	if rest := m &^ allAccess; rest != 0 {
		parts = append(parts, fmt.Sprintf("%#x", rest))
	}
	return fmt.Sprintf("%s (%#x)", strings.Join(parts, " "), m)
}

// acObj spells an Object for failure messages.
func acObj(o policy.Object) string {
	return fmt.Sprintf("{Type %d, Mode %#o, UID %d, GID %d}", o.Type, o.Mode, o.UID, o.GID)
}

// acPtr spells an *Object for failure messages: nil, or what it points to.
func acPtr(o *policy.Object) string {
	if o == nil {
		return "nil"
	}
	return "&" + acObj(*o)
}

// acOpName names an Op, OpExecute included, for failure messages.
func acOpName(op policy.Op) string {
	if op == policy.OpExecute {
		return "OpExecute"
	}
	return opName(op)
}

// acOpNames spells a list of Ops for failure messages.
func acOpNames(ops []policy.Op) string {
	if len(ops) == 0 {
		return "nothing"
	}
	names := make([]string, len(ops))
	for i, op := range ops {
		names[i] = acOpName(op)
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// acDescribe spells a list of requests, every field, for failure messages.
func acDescribe(reqs []policy.Request) string {
	if len(reqs) == 0 {
		return "nothing"
	}
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = fmt.Sprintf("%s{Caller %+v, Object %s, Dst %s, UID %d, GID %d, Entry %s, Replaced %s, Advisory %v}",
			acOpName(r.Op), r.Caller, acObj(r.Object), acObj(r.Dst), r.UID, r.GID, acPtr(r.Entry), acPtr(r.Replaced),
			r.Advisory)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// acObjects are the objects X1 and X3 judge for one type and mode:
// mkObj(typ, mode), and for a directory foreignDir(mode) too, whose owner and
// group no fixture caller has.
func acObjects(typ vfs.FileType, mode uint32) []policy.Object {
	if typ == vfs.TypeDir {
		return []policy.Object{mkObj(typ, mode), foreignDir(mode)}
	}
	return []policy.Object{mkObj(typ, mode)}
}

// acMutables are both values of AccessBits' mutable.
var acMutables = []bool{true, false}

// TestAccessBitsExhaustively is X1 of ADR 0027 §10 (§1, §2, §6 and §9). For
// every caller of p2Callers(), every type of oaAllTypes, every mode of
// p2Modes(), mutable true and false, and the objects mkObj(type, mode) and,
// for a directory, foreignDir(mode) too: AccessBits(Unix(), c, o, allAccess,
// mutable) equals, compared with ==, acUnixOracle, written from §1's table for
// Unix(). It replaces P9 and pins every bit for every type, U5 included.
//
// It also shows that nothing widens (§9): with mutable true, the mask has no
// bit outside acOldOracle, ADR 0014 §5's rule. And the mask asked for: over a
// smaller grid of modes, the types vfs.TypeReg, vfs.TypeDir and vfs.TypeLnk,
// every caller and both values of mutable, the answer for every want from 0
// to 63 is the answer for allAccess, & want; and for ^uint32(0), allAccess |
// 0x40 and allAccess | 1<<31 it is the answer for allAccess, since a bit
// outside the six is never granted and nothing is asked for it.
func TestAccessBitsExhaustively(t *testing.T) {
	u := policy.Unix()
	l := &limiter{t: t, limit: 20}
	defer l.done()
	modes := p2Modes()
	for _, nc := range p2Callers() {
		for _, typ := range oaAllTypes {
			for _, mode := range modes {
				for _, o := range acObjects(typ, mode) {
					for _, mutable := range acMutables {
						got := policy.AccessBits(u, nc.c, o, allAccess, mutable)
						if want := acUnixOracle(nc.c, o, mutable); got != want {
							l.errorf("AccessBits(Unix(), %s %+v, %s, every bit, mutable %v) = %s, want %s: under "+
								"Unix() a directory is granted R with read, L with search, and M, E and D with write "+
								"and search on a mount that takes changes; anything else R with read, M and E with "+
								"write on a mount that takes changes, and X with execute; and no other bit (ADR 0027 "+
								"§1 and §2, §10 X1)", nc.name, nc.c, acObj(o), mutable, acMask(got), acMask(want))
						}
						if !mutable {
							continue
						}
						if wider := got &^ acOldOracle(nc.c, o); wider != 0 {
							l.errorf("AccessBits(Unix(), %s %+v, %s, every bit, mutable true) = %s, which grants %s "+
								"beyond what ADR 0014 §5's rule granted: nothing widens (ADR 0027 §9, §10 X1)",
								nc.name, nc.c, acObj(o), acMask(got), acMask(wider))
						}
					}
				}
			}
		}
	}

	small := []uint32{0, 0o111, 0o222, 0o444, 0o333, 0o555, 0o666, 0o700, 0o070, 0o007, 0o644, 0o755, 0o777, 0o1777,
		0o4711, 0o7000}
	outside := []uint32{^uint32(0), allAccess | 0x40, allAccess | 1<<31}
	for _, nc := range p2Callers() {
		for _, typ := range []vfs.FileType{vfs.TypeReg, vfs.TypeDir, vfs.TypeLnk} {
			for _, mode := range small {
				for _, o := range acObjects(typ, mode) {
					for _, mutable := range acMutables {
						full := policy.AccessBits(u, nc.c, o, allAccess, mutable)
						for want := uint32(0); want <= 63; want++ {
							if got := policy.AccessBits(u, nc.c, o, want, mutable); got != full&want {
								l.errorf("AccessBits(Unix(), %s, %s, %#x, mutable %v) = %s, want %s, the answer for "+
									"every bit, %s, & %#x: a bit not asked for is never granted, and each bit asked "+
									"for is answered as it is when every bit is asked (ADR 0027 §2, §10 X1)", nc.name,
									acObj(o), want, mutable, acMask(got), acMask(full&want), acMask(full), want)
							}
						}
						for _, want := range outside {
							if got := policy.AccessBits(u, nc.c, o, want, mutable); got != full {
								l.errorf("AccessBits(Unix(), %s, %s, %#x, mutable %v) = %s, want %s, the answer for "+
									"every bit: a bit outside the six vfs.Access constants is never granted, and is "+
									"otherwise ignored (ADR 0027 §2, §10 X1)", nc.name, acObj(o), want, mutable,
									acMask(got), acMask(full))
							}
						}
					}
				}
			}
		}
	}
}

// acRefusal is an error of a pointer type of X2's own.
type acRefusal struct{ why string }

func (r *acRefusal) Error() string { return r.why }

// acRecorder is X2's recording policy, a Func that a test writes as ADR 0014
// §5 allows: its check keeps a copy of each request, Caller.GIDs and what
// Entry and Replaced point to included, under its own lock, and refuses the
// Ops it is told to with the error it is given for each.
type acRecorder struct {
	mu     sync.Mutex
	reqs   []policy.Request
	refuse map[policy.Op]error
}

// check records a copy of r and answers the error refuse gives r.Op, nil for
// an Op it gives none.
func (rec *acRecorder) check(r policy.Request) error {
	r.Caller.GIDs = slices.Clone(r.Caller.GIDs)
	r.Entry = stkCloneObject(r.Entry)
	r.Replaced = stkCloneObject(r.Replaced)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.reqs = append(rec.reqs, r)
	return rec.refuse[r.Op]
}

// reset forgets every request recorded and sets the Ops to refuse.
func (rec *acRecorder) reset(refuse map[policy.Op]error) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.reqs = nil
	rec.refuse = refuse
}

// taken returns a copy of the requests recorded since the last reset.
func (rec *acRecorder) taken() []policy.Request {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.reqs)
}

// acRequestsAre reports whether got is exactly one request for each of ops,
// in that order, each as ADR 0027 §2 builds it: Op the one given; Caller c,
// its GIDs compared by content; Object o; Dst o on an OpRename and the zero
// Object on every other; Advisory true; UID and GID zero; and Entry and
// Replaced nil.
func acRequestsAre(got []policy.Request, ops []policy.Op, c vfs.Caller, o policy.Object) bool {
	if len(got) != len(ops) {
		return false
	}
	for i, r := range got {
		var dst policy.Object
		if ops[i] == policy.OpRename {
			dst = o
		}
		switch {
		case r.Op != ops[i], r.Object != o, r.Dst != dst:
			return false
		case r.Caller.UID != c.UID, r.Caller.GID != c.GID, !slices.Equal(r.Caller.GIDs, c.GIDs):
			return false
		case !r.Advisory, r.UID != 0, r.GID != 0, r.Entry != nil, r.Replaced != nil:
			return false
		}
	}
	return true
}

// TestAccessBitsAsksTheOperationsOwnRequests is X2 of ADR 0027 §10 (§1 and
// §2). With acRecorder, for a directory, a regular file, a symlink and an
// object of type vfs.FileType(0), and a caller with supplementary groups:
//   - allowing everything, mutable true, every bit asked: a directory is
//     answered R, L, M, E and D, having been asked exactly OpReadDir,
//     OpLookup, OpRename, OpAddEntry and OpRemoveEntry, in that order; each of
//     the other three R, M, E and X, having been asked exactly OpRead,
//     OpWrite, OpTruncate, OpWrite and OpExecute;
//   - every request has Caller the caller, its GIDs by content, Object the
//     object, Advisory true, UID and GID zero, and Entry and Replaced nil; Dst
//     is the object on the OpRename and the zero Object on every other;
//   - one bit asked at a time, mutable true and false: exactly that bit's
//     requests, none for a directory's X or another object's L or D, and none
//     for a change when mutable is false;
//   - mutable false, every bit asked: a directory is answered R and L, asked
//     OpReadDir and OpLookup alone; anything else R and X, asked OpRead and
//     OpExecute alone;
//   - one Op refused at a time, every Op from 1 to OpExecute, with each of
//     vfs.ErrNXIO, vfs.ErrAcces, an errors.New value, a wrapped error and a
//     pointer error: the answer lacks exactly the bits whose requests hold
//     that Op, and a bit's requests stop at its first refusal. Refusing
//     OpTruncate takes M from a file and leaves E; refusing OpWrite takes M and
//     E, and the requests are then OpRead, OpWrite, OpWrite and OpExecute;
//   - want 0, and a want with no bit of the six: the answer is 0 and nothing
//     is asked.
func TestAccessBitsAsksTheOperationsOwnRequests(t *testing.T) {
	l := &limiter{t: t, limit: 30}
	defer l.done()
	c := vfs.Caller{UID: 4000, GID: 400, GIDs: []uint32{402, fileGID, 401}}
	objs := []struct {
		name string
		o    policy.Object
	}{
		{"a directory", mkObj(vfs.TypeDir, 0o750)},
		{"a regular file", mkObj(vfs.TypeReg, 0o640)},
		{"a symlink", mkObj(vfs.TypeLnk, 0o777)},
		{"an object of type vfs.FileType(0)", mkObj(vfs.FileType(0), 0o604)},
	}
	rec := &acRecorder{}
	p := policy.Func(rec.check)
	R, L, M, E, D, X := vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute
	dirAll := []policy.Op{policy.OpReadDir, policy.OpLookup, policy.OpRename, policy.OpAddEntry, policy.OpRemoveEntry}
	fileAll := []policy.Op{policy.OpRead, policy.OpWrite, policy.OpTruncate, policy.OpWrite, policy.OpExecute}

	// ask runs AccessBits(p, c, o, want, mutable) with the recorder refusing
	// as refuse says, and requires the mask mask and exactly the requests ops.
	ask := func(what string, o policy.Object, want uint32, mutable bool, refuse map[policy.Op]error, mask uint32,
		ops []policy.Op, why string) {
		t.Helper()
		rec.reset(refuse)
		got := policy.AccessBits(p, c, o, want, mutable)
		asked := rec.taken()
		if got != mask {
			l.errorf("%s: AccessBits(the recorder, %+v, %s, %#x, mutable %v) = %s, want %s: %s (ADR 0027 §1 and §2, "+
				"§10 X2)", what, c, acObj(o), want, mutable, acMask(got), acMask(mask), why)
		}
		if !acRequestsAre(asked, ops, c, o) {
			l.errorf("%s: AccessBits(the recorder, %+v, %s, %#x, mutable %v) asked the policy %s; want exactly %s, in "+
				"that order, each with Caller the caller, its GIDs by content, Object the object, Dst the object on "+
				"the OpRename and the zero Object on every other, Advisory true, UID and GID zero, and Entry and "+
				"Replaced nil: %s (ADR 0027 §1 and §2, §10 X2)", what, c, acObj(o), want, mutable, acDescribe(asked),
				acOpNames(ops), why)
		}
	}

	for _, ob := range objs {
		ops, mask := fileAll, R|M|E|X
		if ob.o.Type == vfs.TypeDir {
			ops, mask = dirAll, R|L|M|E|D
		}
		ask(ob.name+", allowing everything, every bit asked", ob.o, allAccess, true, nil, mask, ops,
			"each bit asks the requests of the operations it names, in the table's order, and is granted when the "+
				"policy allows them all")
	}

	for _, ob := range objs {
		for _, bit := range acBits {
			for _, mutable := range acMutables {
				ops, mask := acAsks(ob.o.Type, bit, mutable, 0)
				ask(fmt.Sprintf("%s, allowing everything, %s alone asked", ob.name, acMask(bit)), ob.o, bit, mutable,
					nil, mask, ops, "a bit asks its own requests and no other's; a bit the table gives no request, a "+
						"directory's X or another object's L or D, asks nothing and is never granted; and with "+
						"mutable false a bit that names a change asks nothing and is not granted")
			}
		}
	}

	for _, ob := range objs {
		ops, mask := []policy.Op{policy.OpRead, policy.OpExecute}, R|X
		if ob.o.Type == vfs.TypeDir {
			ops, mask = []policy.Op{policy.OpReadDir, policy.OpLookup}, R|L
		}
		ask(ob.name+", allowing everything, every bit asked, mutable false", ob.o, allAccess, false, nil, mask, ops,
			"on a mount that takes no change, M, E and D ask nothing and are not granted, and the rest are as on a "+
				"mount that does")
	}

	errs := []error{
		vfs.ErrNXIO,
		vfs.ErrAcces,
		errors.New("a refusal of X2's own"),
		fmt.Errorf("a wrapped refusal: %w", vfs.ErrPerm),
		&acRefusal{why: "a refusal of a pointer type of X2's own"},
	}
	for _, ob := range objs {
		for op := policy.Op(1); op <= policy.OpExecute; op++ {
			for _, err := range errs {
				ops, mask := acAsks(ob.o.Type, allAccess, true, op)
				ask(fmt.Sprintf("%s, every bit asked, %s refused with %#v", ob.name, acOpName(op), err), ob.o, allAccess,
					true, map[policy.Op]error{op: err}, mask, ops, "a refusal, whatever its error, withholds exactly "+
						"the bits whose requests hold that Op, and a bit's requests stop at its first refusal")
			}
		}
		if ob.o.Type == vfs.TypeDir {
			continue
		}
		ask(ob.name+", every bit asked, OpTruncate refused with vfs.ErrNXIO", ob.o, allAccess, true,
			map[policy.Op]error{policy.OpTruncate: vfs.ErrNXIO}, R|E|X, fileAll,
			"refusing OpTruncate takes M, which asks it, and leaves E, which does not")
		ask(ob.name+", every bit asked, OpWrite refused with vfs.ErrNXIO", ob.o, allAccess, true,
			map[policy.Op]error{policy.OpWrite: vfs.ErrNXIO}, R|X,
			[]policy.Op{policy.OpRead, policy.OpWrite, policy.OpWrite, policy.OpExecute},
			"refusing OpWrite takes M and E, and M stops at its first refusal, so its OpTruncate is not asked")
	}

	for _, ob := range objs {
		for _, want := range []uint32{0, 0x40, ^allAccess, 1 << 31} {
			for _, mutable := range acMutables {
				ask(fmt.Sprintf("%s, allowing everything, %#x asked", ob.name, want), ob.o, want, mutable, nil, 0, nil,
					"a want with no bit of the six is granted nothing and asks nothing")
			}
		}
	}
}

// TestWhatAccessBitsGrantsTheRowsAllow is X3 of ADR 0027 §10 (§8's S2 and S3;
// the issue's done-when). For every caller of p2Callers() and every mode of
// p2Modes(), with u a Unix() and requests that are not advisory:
//   - a regular file mkObj(vfs.TypeReg, mode), mutable true: R in the mask
//     means u allows OpRead; M, OpWrite and OpTruncate; E, OpWrite; X, OpRead;
//   - a directory, mkObj(vfs.TypeDir, mode) and foreignDir(mode), mutable
//     true: R is in the mask exactly when u allows OpReadDir; L, OpLookup; E,
//     OpAddEntry; D, OpRemoveEntry with no Entry; M, mkRename with that
//     directory twice. And D in the mask means u allows stkRemove with
//     stkOwned of the caller's uid, and M means it allows stkRename within the
//     directory with that entry and nothing replaced;
//   - with mutable false, M, E and D are in no mask.
func TestWhatAccessBitsGrantsTheRowsAllow(t *testing.T) {
	u := policy.Unix()
	l := &limiter{t: t, limit: 20}
	defer l.done()
	R, L, M, E, D, X := vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute
	allows := func(r policy.Request) bool { return u.Check(r) == nil }
	modes := p2Modes()
	for _, nc := range p2Callers() {
		c := nc.c
		for _, mode := range modes {
			f := mkObj(vfs.TypeReg, mode)
			m := policy.AccessBits(u, c, f, allAccess, true)
			fileRows := []struct {
				bit  uint32
				reqs []policy.Request
				what string
			}{
				{R, []policy.Request{mkReq(policy.OpRead, c, f)}, "OpRead, by which Read reads it"},
				{M, []policy.Request{mkReq(policy.OpWrite, c, f), mkReq(policy.OpTruncate, c, f)},
					"OpWrite and OpTruncate, by which Write rewrites it and a SetAttr sets its size"},
				{E, []policy.Request{mkReq(policy.OpWrite, c, f)}, "OpWrite, by which Write writes new data"},
				{X, []policy.Request{mkReq(policy.OpRead, c, f)}, "OpRead, by which a client pages it in"},
			}
			for _, row := range fileRows {
				if m&row.bit == 0 {
					continue
				}
				for _, r := range row.reqs {
					if err := u.Check(r); err != nil {
						l.errorf("AccessBits(Unix(), %s %+v, a regular file %#o, every bit, mutable true) = %s, which "+
							"has %s, but Unix().Check(%s) = %v: what ACCESS grants on a regular file, the operations "+
							"allow, here %s (ADR 0027 §3 and §8, S2; §10 X3)", nc.name, c, mode, acMask(m),
							acMask(row.bit), acOpName(r.Op), err, row.what)
					}
				}
			}
			if mf := policy.AccessBits(u, c, f, allAccess, false); mf&acChange != 0 {
				l.errorf("AccessBits(Unix(), %s, a regular file %#o, every bit, mutable false) = %s, want no M, E or D: "+
					"a mount that takes no change is granted none (ADR 0027 §4, §10 X3)", nc.name, mode, acMask(mf))
			}

			for _, d := range []policy.Object{mkObj(vfs.TypeDir, mode), foreignDir(mode)} {
				m := policy.AccessBits(u, c, d, allAccess, true)
				dirRows := []struct {
					bit  uint32
					req  policy.Request
					what string
				}{
					{R, mkReq(policy.OpReadDir, c, d), "OpReadDir, which ReadDir asks"},
					{L, mkReq(policy.OpLookup, c, d), "OpLookup, which Lookup asks"},
					{E, mkReq(policy.OpAddEntry, c, d), "OpAddEntry, which Create, Mkdir and Symlink ask"},
					{D, stkRemove(c, d, nil), "OpRemoveEntry with no Entry"},
					{M, mkRename(c, d, d), "OpRename with the directory as Object and as Dst, and no Entry"},
				}
				for _, row := range dirRows {
					if granted, allowed := m&row.bit != 0, allows(row.req); granted != allowed {
						l.errorf("AccessBits(Unix(), %s %+v, a directory %s, every bit, mutable true) = %s, so %s is "+
							"granted: %v; but Unix() allows %s: %v. On a directory the bit is granted exactly when "+
							"the row allows (ADR 0027 §5 and §8, S3; §10 X3)", nc.name, c, acObj(d), acMask(m),
							acMask(row.bit), granted, row.what, allowed)
					}
				}
				if m&D != 0 {
					if err := u.Check(stkRemove(c, d, stkOwned(c.UID))); err != nil {
						l.errorf("AccessBits(Unix(), %s, a directory %s, every bit, mutable true) = %s, which has D, "+
							"but Unix().Check(OpRemoveEntry of an entry the caller owns) = %v: DELETE says the caller "+
							"may remove an entry it owns (ADR 0027 §5 and §8, S2; §10 X3)", nc.name, acObj(d),
							acMask(m), err)
					}
				}
				if m&M != 0 {
					if err := u.Check(stkRename(c, d, d, stkOwned(c.UID), nil)); err != nil {
						l.errorf("AccessBits(Unix(), %s, a directory %s, every bit, mutable true) = %s, which has M, "+
							"but Unix().Check(OpRename within it of an entry the caller owns, nothing replaced) = %v: "+
							"MODIFY says the caller may rename an entry it owns within the directory (ADR 0027 §5 "+
							"and §8, S2; §10 X3)", nc.name, acObj(d), acMask(m), err)
					}
				}
				if mf := policy.AccessBits(u, c, d, allAccess, false); mf&acChange != 0 {
					l.errorf("AccessBits(Unix(), %s, a directory %s, every bit, mutable false) = %s, want no M, E or "+
						"D: a mount that takes no change is granted none (ADR 0027 §4, §10 X3)", nc.name, acObj(d),
						acMask(mf))
				}
			}
		}
	}
}

// TestAnAdvisoryRequestIsAnsweredByTheModeAlone is X4 of ADR 0027 §10 (§2 and
// §3). Over the grid of ADR 0025 §6's A1, every caller of p2Callers(), every
// type of oaAllTypes, every mode of p2Modes() and each of oaFileOps, with
// Request.Advisory set:
//   - a directory is vfs.ErrIsDir;
//   - a regular file is nil exactly when oraclePermits gives the row's
//     permission, and vfs.ErrAcces otherwise, so neither the owner override nor
//     the read that execute gives reaches an advisory request;
//   - any other type is nil exactly when oraclePermits gives it, and
//     vfs.ErrAcces otherwise, compared with ==. That refusal is pinned since
//     ADR 0029 §9's N2: an advisory request of such an object is answered by
//     the mode whatever its type, and line 2 of ADR 0029 §1, which refuses the
//     same request unmarked with vfs.ErrInval, does not reach it (ADR 0029 §1
//     and §3).
//
// For a regular file and a directory it also asks §2's first law, that an
// advisory request only narrows: where the marked request is allowed, the same
// request unmarked is allowed too. It does not ask it of another type, where
// the law is false since ADR 0029 §3.
func TestAnAdvisoryRequestIsAnsweredByTheModeAlone(t *testing.T) {
	u := policy.Unix()
	l := &limiter{t: t, limit: 20}
	defer l.done()
	modes := p2Modes()
	for _, nc := range p2Callers() {
		for _, typ := range oaAllTypes {
			for _, mode := range modes {
				o := mkObj(typ, mode)
				for _, op := range oaFileOps {
					r := acAdvisory(mkReq(op, nc.c, o))
					err := u.Check(r)
					permits := oraclePermits(nc.c, o, oaRowPerm(op))
					what := "Unix().Check(advisory " + acOpName(op) + ", by " + nc.name + ")"
					switch typ {
					case vfs.TypeDir:
						if err != vfs.ErrIsDir {
							l.errorf("%s of a directory %#o = %#v (%v), want the bare vfs.ErrIsDir, compared with ==: "+
								"line 1 refuses a directory first (ADR 0027 §2, §10 X4)", what, mode, err, err)
						}
					case vfs.TypeReg:
						var want error = vfs.ErrAcces
						if permits {
							want = nil
						}
						if err != want {
							l.errorf("%s of a regular file %#o = %#v (%v), want %v, compared with ==: an advisory "+
								"request is answered by the type and then the mode alone, with neither the owner "+
								"override nor the read that execute gives (ADR 0027 §2 and §3, §10 X4)", what, mode, err,
								err, want)
						}
					default:
						var want error = vfs.ErrAcces
						if permits {
							want = nil
						}
						if err != want {
							l.errorf("%s of a type %d, mode %#o, = %#v (%v), want %v, compared with ==: an advisory "+
								"request of an object that is neither a regular file nor a directory is nil exactly "+
								"when Permits gives the row's permission, which it does: %v, and the bare "+
								"vfs.ErrAcces otherwise, whatever its type (ADR 0027 §2 and §6, §10 X4; ADR 0029 §1 "+
								"and §3, §9 N2)", what, typ, mode, err, err, want, permits)
						}
					}
					if err != nil || (typ != vfs.TypeReg && typ != vfs.TypeDir) {
						continue
					}
					if plain := u.Check(mkReq(op, nc.c, o)); plain != nil {
						l.errorf("%s of a type %d, mode %#o, = nil, but the same request unmarked = %v: an advisory "+
							"request only narrows (ADR 0027 §2, the first law; §10 X4)", what, typ, mode, plain)
					}
				}
			}
		}
	}
}

// TestTheExecuteRowExhaustively is X5 of ADR 0027 §10 (§2's OpExecute row).
// Over every caller of p2Callers(), every type of oaAllTypes and every mode of
// p2Modes(), with Request.Advisory unset and set: a directory is
// vfs.ErrIsDir; anything else is nil exactly when oraclePermits(c, o,
// PermExec), and vfs.ErrAcces otherwise, each compared with ==. So root
// executes a non-directory only if some execute bit is set.
//
// It also asks §2's second law, that execution is backed by a read: for a
// regular file, where OpExecute is allowed, OpRead unmarked is allowed too.
func TestTheExecuteRowExhaustively(t *testing.T) {
	u := policy.Unix()
	l := &limiter{t: t, limit: 20}
	defer l.done()
	modes := p2Modes()
	for _, nc := range p2Callers() {
		for _, typ := range oaAllTypes {
			for _, mode := range modes {
				o := mkObj(typ, mode)
				for _, advisory := range []bool{false, true} {
					r := mkReq(policy.OpExecute, nc.c, o)
					r.Advisory = advisory
					err := u.Check(r)
					var want error = vfs.ErrAcces
					switch {
					case typ == vfs.TypeDir:
						want = vfs.ErrIsDir
					case oraclePermits(nc.c, o, policy.PermExec):
						want = nil
					}
					if err != want {
						l.errorf("Unix().Check(OpExecute, by %s, of a type %d, mode %#o, Advisory %v) = %#v (%v), want "+
							"%v, compared with ==: vfs.ErrIsDir for a directory, nil if Permits gives execute, and "+
							"vfs.ErrAcces otherwise; the row reads Op, Caller and Object alone (ADR 0027 §2, §10 X5)",
							nc.name, typ, mode, advisory, err, err, want)
					}
					if err != nil || typ != vfs.TypeReg {
						continue
					}
					if rd := u.Check(mkReq(policy.OpRead, nc.c, o)); rd != nil {
						l.errorf("Unix().Check(OpExecute, by %s, of a regular file %#o) = nil, but Unix().Check(OpRead) "+
							"unmarked = %v: execution is backed by a read (ADR 0027 §2, the second law; ADR 0025 §3; "+
							"§10 X5)", nc.name, mode, rd)
					}
				}
			}
		}
	}
}
