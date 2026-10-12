package policy_test

// Clean-room tests for internal/policy, written from ADR 0014,
// docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md,
// and from internal/vfs/vfs.go for vfs.Caller, vfs.FileType, vfs.Status and the
// vfs.Access constants, without reading the implementation. Section and item
// references are to that ADR:
//
//	§5  the policy API, the Permits rule, the Unix table, AccessBits, All, Func
//	    and purity
//	§11 the test surface, P1 to P13, which each test cites, and the unpinned
//	    rows U1 to U6, which no test here relies on
//
// The oracles here, oracleInGroup and oraclePermits, are written from §5's
// rules alone. Every refusal Unix() gives is compared with == against the bare
// vfs.Status, since §5 says it is never wrapped. No test sleeps: the
// concurrent test releases its goroutines together with a start channel, waits
// for them with a sync.WaitGroup, has each goroutine record its results in its
// own slot, and asserts only after the wait. It is meant to run under go test
// -race.

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// The objects most tests judge are owned by fileUID and in group fileGID.
const (
	fileUID uint32 = 1000
	fileGID uint32 = 100
)

// The callers are made afresh on each call, each with a GIDs slice of its own.
// Only callerOwner and callerOwnerInGroup have fileUID; only
// callerOwnerInGroup, callerGroup and callerSupp are in fileGID; callerSupp is
// in it through GIDs alone, which are deliberately out of order.

func callerRoot() vfs.Caller {
	return vfs.Caller{UID: 0, GID: 0, GIDs: []uint32{10, 20}}
}

func callerOwner() vfs.Caller {
	return vfs.Caller{UID: fileUID, GID: 500, GIDs: []uint32{501, 502}}
}

func callerOwnerInGroup() vfs.Caller {
	return vfs.Caller{UID: fileUID, GID: fileGID, GIDs: []uint32{501}}
}

func callerGroup() vfs.Caller {
	return vfs.Caller{UID: 2000, GID: fileGID, GIDs: []uint32{201, 202}}
}

func callerSupp() vfs.Caller {
	return vfs.Caller{UID: 3000, GID: 300, GIDs: []uint32{302, fileGID, 301}}
}

func callerOther() vfs.Caller {
	return vfs.Caller{UID: 4000, GID: 400, GIDs: []uint32{401, 402}}
}

// namedCaller is a caller with a name for failure messages.
type namedCaller struct {
	name string
	c    vfs.Caller
}

// p2Callers are P2's callers: root, the owner, a primary-group member, a
// supplementary-group member and other, and also an owner who is in the
// object's group, whom §5 judges by the owner's bits alone.
func p2Callers() []namedCaller {
	return []namedCaller{
		{name: "root", c: callerRoot()},
		{name: "the owner", c: callerOwner()},
		{name: "the owner, also in the group", c: callerOwnerInGroup()},
		{name: "a primary-group member", c: callerGroup()},
		{name: "a supplementary-group member", c: callerSupp()},
		{name: "other", c: callerOther()},
	}
}

// p2Types are P2's types: two that are valid, one other valid non-directory,
// and one that is not valid. §5: every Type but vfs.TypeDir is a non-directory.
var p2Types = []vfs.FileType{vfs.TypeReg, vfs.TypeDir, vfs.TypeLnk, vfs.FileType(0)}

// p2Modes is every mode from 0 to 0o777, each also with 0o7000 set, and also
// with every bit above the low nine set (§11 P2).
func p2Modes() []uint32 {
	var ms []uint32
	for m := uint32(0); m <= 0o777; m++ {
		ms = append(ms, m, m|0o7000, m|^uint32(0o777))
	}
	return ms
}

// outOfRangeWants are want values with a bit outside PermRead|PermWrite|PermExec.
var outOfRangeWants = []policy.Perm{8, 9, 0xf, 0x10, 0x17, 0x80, 0x100, 1 << 31, ^policy.Perm(0)}

// mkObj is an object of type typ and mode mode, owned by fileUID and in
// fileGID.
func mkObj(typ vfs.FileType, mode uint32) policy.Object {
	return policy.Object{Type: typ, Mode: mode, UID: fileUID, GID: fileGID}
}

// foreignDir is a directory whose owner and group no fixture caller has, so
// that every caller but root is judged by its other bits.
func foreignDir(mode uint32) policy.Object {
	return policy.Object{Type: vfs.TypeDir, Mode: mode, UID: 9999, GID: 999}
}

func mkReq(op policy.Op, c vfs.Caller, o policy.Object) policy.Request {
	return policy.Request{Op: op, Caller: c, Object: o}
}

func mkChown(c vfs.Caller, o policy.Object, uid uint32) policy.Request {
	return policy.Request{Op: policy.OpChown, Caller: c, Object: o, UID: uid}
}

func mkChgrp(c vfs.Caller, o policy.Object, gid uint32) policy.Request {
	return policy.Request{Op: policy.OpChgrp, Caller: c, Object: o, GID: gid}
}

func mkRename(c vfs.Caller, src, dst policy.Object) policy.Request {
	return policy.Request{Op: policy.OpRename, Caller: c, Object: src, Dst: dst}
}

// opName is the Go name of a pinned Op, for failure messages.
func opName(op policy.Op) string {
	switch op {
	case policy.OpRead:
		return "OpRead"
	case policy.OpWrite:
		return "OpWrite"
	case policy.OpTruncate:
		return "OpTruncate"
	case policy.OpChmod:
		return "OpChmod"
	case policy.OpChown:
		return "OpChown"
	case policy.OpChgrp:
		return "OpChgrp"
	case policy.OpSetTimes:
		return "OpSetTimes"
	case policy.OpLookup:
		return "OpLookup"
	case policy.OpReadDir:
		return "OpReadDir"
	case policy.OpAddEntry:
		return "OpAddEntry"
	case policy.OpRemoveEntry:
		return "OpRemoveEntry"
	case policy.OpRename:
		return "OpRename"
	}
	return fmt.Sprintf("Op(%d)", uint8(op))
}

// oracleInGroup is §5's InGroup rule: true exactly when gid == c.GID or gid is
// in c.GIDs.
func oracleInGroup(c vfs.Caller, gid uint32) bool {
	return gid == c.GID || slices.Contains(c.GIDs, gid)
}

// oraclePermits is §5's Permits rule, written from its text. Only the low nine
// bits of the mode count; a want with a bit outside 7 is false for everyone;
// root is refused only an exec of a non-directory with no exec bit; anyone
// else is judged by exactly one class, and allowed when that class's bits
// include every bit of want.
func oraclePermits(c vfs.Caller, o policy.Object, want policy.Perm) bool {
	if want&^(policy.PermRead|policy.PermWrite|policy.PermExec) != 0 {
		return false
	}
	mode := o.Mode & 0o777
	if c.UID == 0 {
		return !(want&policy.PermExec != 0 && o.Type != vfs.TypeDir && mode&0o111 == 0)
	}
	var bits uint32
	switch {
	case c.UID == o.UID:
		bits = (mode >> 6) & 7
	case oracleInGroup(c, o.GID):
		bits = (mode >> 3) & 7
	default:
		bits = mode & 7
	}
	return uint32(want)&bits == uint32(want)
}

// limiter reports the first limit failures of an exhaustive loop, and then a
// count of the rest, so that a wrong rule does not print every case.
type limiter struct {
	t     *testing.T
	limit int
	n     int
}

func (l *limiter) errorf(format string, args ...any) {
	l.t.Helper()
	l.n++
	if l.n <= l.limit {
		l.t.Errorf(format, args...)
	}
}

func (l *limiter) done() {
	l.t.Helper()
	if l.n > l.limit {
		l.t.Errorf("... and %d more failures like those above", l.n-l.limit)
	}
}

// panicked runs f and reports whether it panicked, and with what value.
func panicked(f func()) (did bool, value any) {
	defer func() {
		if r := recover(); r != nil {
			did, value = true, r
		}
	}()
	f()
	return false, nil
}

// TestConstantsArePinned checks the values §5 pins: PermExec 1, PermWrite 2,
// PermRead 4, and OpRead 1 to OpRename 12 in §5's order. The oracles and P6
// rely on them. This is beyond §11's list, but §5 pins every value.
func TestConstantsArePinned(t *testing.T) {
	if policy.PermExec != 1 || policy.PermWrite != 2 || policy.PermRead != 4 {
		t.Errorf("PermExec, PermWrite, PermRead = %d, %d, %d, want 1, 2, 4 (ADR 0014 §5)", policy.PermExec, policy.PermWrite, policy.PermRead)
	}
	ops := []policy.Op{
		policy.OpRead, policy.OpWrite, policy.OpTruncate, policy.OpChmod,
		policy.OpChown, policy.OpChgrp, policy.OpSetTimes, policy.OpLookup,
		policy.OpReadDir, policy.OpAddEntry, policy.OpRemoveEntry, policy.OpRename,
	}
	for i, op := range ops {
		if want := policy.Op(i + 1); op != want {
			t.Errorf("%s = %d, want %d: OpRead is iota + 1, so OpRead is 1 and OpRename is 12 (ADR 0014 §5)", opName(want), uint8(op), uint8(want))
		}
	}
}

// TestInGroup covers P1 (ADR 0014 §11): the primary group, a supplementary
// group, neither, nil and empty GIDs, and a list of 20, against §5's rule that
// InGroup(c, gid) is true exactly when gid == c.GID or gid is in c.GIDs.
func TestInGroup(t *testing.T) {
	many := make([]uint32, 0, 20)
	for g := uint32(1000); g < 1020; g++ {
		many = append(many, g)
	}
	cases := []struct {
		name string
		c    vfs.Caller
		gid  uint32
		want bool
	}{
		{name: "the primary group", c: vfs.Caller{UID: 7, GID: 100, GIDs: []uint32{200, 300}}, gid: 100, want: true},
		{name: "the first supplementary group", c: vfs.Caller{UID: 7, GID: 100, GIDs: []uint32{200, 300}}, gid: 200, want: true},
		{name: "the last supplementary group", c: vfs.Caller{UID: 7, GID: 100, GIDs: []uint32{200, 300}}, gid: 300, want: true},
		{name: "neither", c: vfs.Caller{UID: 7, GID: 100, GIDs: []uint32{200, 300}}, gid: 400, want: false},
		{name: "gid 0, in neither", c: vfs.Caller{UID: 7, GID: 100, GIDs: []uint32{200, 300}}, gid: 0, want: false},
		{name: "the primary group, nil GIDs", c: vfs.Caller{UID: 7, GID: 100}, gid: 100, want: true},
		{name: "another group, nil GIDs", c: vfs.Caller{UID: 7, GID: 100}, gid: 200, want: false},
		{name: "gid 0, nil GIDs", c: vfs.Caller{UID: 7, GID: 100}, gid: 0, want: false},
		{name: "the primary group, empty GIDs", c: vfs.Caller{UID: 7, GID: 100, GIDs: []uint32{}}, gid: 100, want: true},
		{name: "another group, empty GIDs", c: vfs.Caller{UID: 7, GID: 100, GIDs: []uint32{}}, gid: 200, want: false},
		{name: "root, a group it is not in", c: vfs.Caller{UID: 0, GID: 0}, gid: 100, want: false},
		{name: "the primary group, 20 GIDs", c: vfs.Caller{UID: 7, GID: 100, GIDs: many}, gid: 100, want: true},
		{name: "one below the 20 GIDs", c: vfs.Caller{UID: 7, GID: 100, GIDs: many}, gid: 999, want: false},
		{name: "one above the 20 GIDs", c: vfs.Caller{UID: 7, GID: 100, GIDs: many}, gid: 1020, want: false},
	}
	for _, tc := range cases {
		if got := policy.InGroup(tc.c, tc.gid); got != tc.want {
			t.Errorf("InGroup(%+v, %d), %s, = %v, want %v: true exactly when gid == c.GID or gid is in c.GIDs (ADR 0014 §5, §11 P1)",
				tc.c, tc.gid, tc.name, got, tc.want)
		}
	}
	c := vfs.Caller{UID: 7, GID: 100, GIDs: many}
	for i, g := range many {
		if !policy.InGroup(c, g) {
			t.Errorf("InGroup(caller with 20 GIDs, %d), the GID at index %d, = false, want true (ADR 0014 §5, §11 P1)", g, i)
		}
	}
}

// TestPermitsExhaustively covers P2 (ADR 0014 §11): every mode from 0 to
// 0o777, also with 0o7000 set and with every bit above the low nine set; the
// types vfs.TypeReg, vfs.TypeDir, vfs.TypeLnk and vfs.FileType(0); P2's
// callers; every want from 0 to 7 against oraclePermits, written from §5's
// rule; and wants with a bit outside 7, which are false for every caller, root
// included.
func TestPermitsExhaustively(t *testing.T) {
	l := &limiter{t: t, limit: 20}
	defer l.done()
	modes := p2Modes()
	for _, nc := range p2Callers() {
		for _, typ := range p2Types {
			for _, mode := range modes {
				o := policy.Object{Type: typ, Mode: mode, UID: fileUID, GID: fileGID}
				for want := policy.Perm(0); want <= 7; want++ {
					got := policy.Permits(nc.c, o, want)
					if exp := oraclePermits(nc.c, o, want); got != exp {
						l.errorf("Permits(%s %+v, {Type: %d, Mode: %#o}, %d) = %v, want %v (ADR 0014 §5's Permits rule, §11 P2)",
							nc.name, nc.c, typ, mode, want, got, exp)
					}
				}
				for _, want := range outOfRangeWants {
					if policy.Permits(nc.c, o, want) {
						l.errorf("Permits(%s %+v, {Type: %d, Mode: %#o}, %#x) = true, want false: a want with a bit outside PermRead|PermWrite|PermExec is false for every caller, root included (ADR 0014 §5, §11 P2)",
							nc.name, nc.c, typ, mode, want)
					}
				}
			}
		}
	}
}

// TestPermitsJudgesByExactlyOneClass covers P3 (ADR 0014 §11): the owner of
// 0o077, and a group member of 0o707, are refused read, write and execute,
// since §5 judges anyone but root by exactly one class.
func TestPermitsJudgesByExactlyOneClass(t *testing.T) {
	cases := []struct {
		name string
		c    vfs.Caller
		mode uint32
	}{
		{name: "the owner", c: callerOwner(), mode: 0o077},
		{name: "the owner, also in the group", c: callerOwnerInGroup(), mode: 0o077},
		{name: "a primary-group member", c: callerGroup(), mode: 0o707},
		{name: "a supplementary-group member", c: callerSupp(), mode: 0o707},
	}
	perms := []policy.Perm{policy.PermRead, policy.PermWrite, policy.PermExec}
	for _, tc := range cases {
		for _, typ := range []vfs.FileType{vfs.TypeReg, vfs.TypeDir} {
			o := mkObj(typ, tc.mode)
			for _, p := range perms {
				if policy.Permits(tc.c, o, p) {
					t.Errorf("Permits(%s, {Type: %d, Mode: %#o}, %d) = true, want false: anyone but root is judged by exactly one class, so the other classes' bits do not help (ADR 0014 §5, §11 P3)",
						tc.name, typ, tc.mode, p)
				}
			}
		}
	}
}

// unixCase is one request to Unix().Check and its pinned answer: nil to
// allow, the bare vfs.Status to refuse, or, when refusedOnly is set, only that
// it is refused (U6's refusal, §11).
type unixCase struct {
	name        string
	req         policy.Request
	want        error
	refusedOnly bool
}

// unixCases is P4's table: for each pinned row of §5's Unix table, at least one
// allowing and one refusing request for each check in the row. It avoids
// every row U1 to U6 leave unpinned (§11):
//
// U1: no OpRead, OpWrite or OpTruncate refusal involves the owner, so no owner
// lacking the permission is asked; U2: no OpRead refusal has a class with
// execute but not read; U3: OpRead, OpWrite and OpTruncate are asked only of
// vfs.TypeReg and vfs.TypeDir; U4: no directory has 0o1000 set; U5 is
// AccessBits', not these; U6: no OpSetTimes asks for a caller who is neither
// root nor the owner but may write, and where such a caller may not write the
// case is refusedOnly.
func unixCases() []unixCase {
	reg, dir, lnk := vfs.TypeReg, vfs.TypeDir, vfs.TypeLnk
	var cs []unixCase
	add := func(name string, r policy.Request, want error) {
		cs = append(cs, unixCase{name: name, req: r, want: want})
	}
	refused := func(name string, r policy.Request) {
		cs = append(cs, unixCase{name: name, req: r, refusedOnly: true})
	}

	// OpRead: ErrIsDir if Object is a directory; ErrAcces unless
	// Permits(Caller, Object, PermRead).
	add("OpRead: the owner, of 0o400", mkReq(policy.OpRead, callerOwner(), mkObj(reg, 0o400)), nil)
	add("OpRead: a primary-group member, of 0o040", mkReq(policy.OpRead, callerGroup(), mkObj(reg, 0o040)), nil)
	add("OpRead: a supplementary-group member, of 0o040", mkReq(policy.OpRead, callerSupp(), mkObj(reg, 0o040)), nil)
	add("OpRead: other, of 0o004", mkReq(policy.OpRead, callerOther(), mkObj(reg, 0o004)), nil)
	add("OpRead: root, of 0o000", mkReq(policy.OpRead, callerRoot(), mkObj(reg, 0)), nil)
	add("OpRead: the owner, of a directory 0o777", mkReq(policy.OpRead, callerOwner(), mkObj(dir, 0o777)), vfs.ErrIsDir)
	add("OpRead: root, of a directory 0o777", mkReq(policy.OpRead, callerRoot(), mkObj(dir, 0o777)), vfs.ErrIsDir)
	add("OpRead: a primary-group member, of 0o604", mkReq(policy.OpRead, callerGroup(), mkObj(reg, 0o604)), vfs.ErrAcces)
	add("OpRead: a supplementary-group member, of 0o620", mkReq(policy.OpRead, callerSupp(), mkObj(reg, 0o620)), vfs.ErrAcces)
	add("OpRead: other, of 0o662", mkReq(policy.OpRead, callerOther(), mkObj(reg, 0o662)), vfs.ErrAcces)

	// OpWrite and OpTruncate: ErrIsDir if Object is a directory; ErrAcces
	// unless Permits(Caller, Object, PermWrite).
	for _, op := range []policy.Op{policy.OpWrite, policy.OpTruncate} {
		n := opName(op)
		add(n+": the owner, of 0o200", mkReq(op, callerOwner(), mkObj(reg, 0o200)), nil)
		add(n+": a primary-group member, of 0o020", mkReq(op, callerGroup(), mkObj(reg, 0o020)), nil)
		add(n+": a supplementary-group member, of 0o020", mkReq(op, callerSupp(), mkObj(reg, 0o020)), nil)
		add(n+": other, of 0o002", mkReq(op, callerOther(), mkObj(reg, 0o002)), nil)
		add(n+": root, of 0o000", mkReq(op, callerRoot(), mkObj(reg, 0)), nil)
		add(n+": the owner, of a directory 0o777", mkReq(op, callerOwner(), mkObj(dir, 0o777)), vfs.ErrIsDir)
		add(n+": root, of a directory 0o777", mkReq(op, callerRoot(), mkObj(dir, 0o777)), vfs.ErrIsDir)
		add(n+": a primary-group member, of 0o747", mkReq(op, callerGroup(), mkObj(reg, 0o747)), vfs.ErrAcces)
		add(n+": a supplementary-group member, of 0o757", mkReq(op, callerSupp(), mkObj(reg, 0o757)), vfs.ErrAcces)
		add(n+": other, of 0o775", mkReq(op, callerOther(), mkObj(reg, 0o775)), vfs.ErrAcces)
	}

	// OpChmod: ErrPerm unless root or the owner.
	add("OpChmod: root, of 0o000", mkReq(policy.OpChmod, callerRoot(), mkObj(reg, 0)), nil)
	add("OpChmod: root, of a symlink", mkReq(policy.OpChmod, callerRoot(), mkObj(lnk, 0o777)), nil)
	add("OpChmod: the owner, of 0o000", mkReq(policy.OpChmod, callerOwner(), mkObj(reg, 0)), nil)
	add("OpChmod: the owner, of a directory 0o000", mkReq(policy.OpChmod, callerOwner(), mkObj(dir, 0)), nil)
	add("OpChmod: the owner, of a symlink", mkReq(policy.OpChmod, callerOwner(), mkObj(lnk, 0o777)), nil)
	add("OpChmod: a primary-group member, of 0o777", mkReq(policy.OpChmod, callerGroup(), mkObj(reg, 0o777)), vfs.ErrPerm)
	add("OpChmod: a supplementary-group member, of a directory 0o777", mkReq(policy.OpChmod, callerSupp(), mkObj(dir, 0o777)), vfs.ErrPerm)
	add("OpChmod: other, of 0o777", mkReq(policy.OpChmod, callerOther(), mkObj(reg, 0o777)), vfs.ErrPerm)

	// OpChown: ErrPerm unless root or the owner; then ErrPerm if not root
	// and r.UID != Object.UID.
	add("OpChown: root, to another uid", mkChown(callerRoot(), mkObj(reg, 0), 5555), nil)
	add("OpChown: root, to uid 0", mkChown(callerRoot(), mkObj(dir, 0), 0), nil)
	add("OpChown: root, to the uid the object has", mkChown(callerRoot(), mkObj(reg, 0), fileUID), nil)
	add("OpChown: the owner, to the uid the object has", mkChown(callerOwner(), mkObj(reg, 0), fileUID), nil)
	add("OpChown: the owner, of a directory, to the uid it has", mkChown(callerOwner(), mkObj(dir, 0o777), fileUID), nil)
	add("OpChown: a primary-group member, to the uid the object has", mkChown(callerGroup(), mkObj(reg, 0o777), fileUID), vfs.ErrPerm)
	add("OpChown: other, to its own uid", mkChown(callerOther(), mkObj(reg, 0o777), callerOther().UID), vfs.ErrPerm)
	add("OpChown: the owner, to another uid", mkChown(callerOwner(), mkObj(reg, 0o777), 5555), vfs.ErrPerm)
	add("OpChown: the owner, to uid 0", mkChown(callerOwner(), mkObj(reg, 0o777), 0), vfs.ErrPerm)

	// OpChgrp: ErrPerm unless root or the owner; then ErrPerm if not root and
	// !InGroup(Caller, r.GID). callerOwner's groups are 500, 501 and 502.
	add("OpChgrp: root, to a group it is not in", mkChgrp(callerRoot(), mkObj(reg, 0), 7777), nil)
	add("OpChgrp: the owner, to its primary group", mkChgrp(callerOwner(), mkObj(reg, 0), 500), nil)
	add("OpChgrp: the owner, to a supplementary group", mkChgrp(callerOwner(), mkObj(dir, 0), 502), nil)
	add("OpChgrp: the owner, also in the group, to the group the object has", mkChgrp(callerOwnerInGroup(), mkObj(reg, 0), fileGID), nil)
	add("OpChgrp: the owner, to a group it is not in", mkChgrp(callerOwner(), mkObj(reg, 0o777), 7777), vfs.ErrPerm)
	add("OpChgrp: a primary-group member, to its own group", mkChgrp(callerGroup(), mkObj(reg, 0o777), fileGID), vfs.ErrPerm)
	add("OpChgrp: other, to its own group", mkChgrp(callerOther(), mkObj(reg, 0o777), callerOther().GID), vfs.ErrPerm)

	// OpSetTimes: ErrPerm unless root, the owner, or Permits(Caller, Object,
	// PermWrite). A caller who is neither root nor the owner is U6: its
	// allowing case is unpinned and not asked, and its refusal is pinned only
	// as a refusal.
	add("OpSetTimes: root, of 0o000", mkReq(policy.OpSetTimes, callerRoot(), mkObj(reg, 0)), nil)
	add("OpSetTimes: the owner, of 0o000", mkReq(policy.OpSetTimes, callerOwner(), mkObj(reg, 0)), nil)
	add("OpSetTimes: the owner, of a directory 0o000", mkReq(policy.OpSetTimes, callerOwner(), mkObj(dir, 0)), nil)
	refused("OpSetTimes: a primary-group member, of 0o757", mkReq(policy.OpSetTimes, callerGroup(), mkObj(reg, 0o757)))
	refused("OpSetTimes: a supplementary-group member, of 0o000", mkReq(policy.OpSetTimes, callerSupp(), mkObj(reg, 0)))
	refused("OpSetTimes: other, of 0o775", mkReq(policy.OpSetTimes, callerOther(), mkObj(reg, 0o775)))

	// OpLookup: ErrNotDir unless Object is a directory; ErrAcces unless
	// Permits(Caller, Object, PermExec).
	add("OpLookup: the owner, of a directory 0o100", mkReq(policy.OpLookup, callerOwner(), mkObj(dir, 0o100)), nil)
	add("OpLookup: a primary-group member, of a directory 0o010", mkReq(policy.OpLookup, callerGroup(), mkObj(dir, 0o010)), nil)
	add("OpLookup: a supplementary-group member, of a directory 0o010", mkReq(policy.OpLookup, callerSupp(), mkObj(dir, 0o010)), nil)
	add("OpLookup: other, of a directory 0o001", mkReq(policy.OpLookup, callerOther(), mkObj(dir, 0o001)), nil)
	add("OpLookup: root, of a directory 0o000", mkReq(policy.OpLookup, callerRoot(), mkObj(dir, 0)), nil)
	add("OpLookup: the owner, of a regular file 0o777", mkReq(policy.OpLookup, callerOwner(), mkObj(reg, 0o777)), vfs.ErrNotDir)
	add("OpLookup: root, of a regular file 0o777", mkReq(policy.OpLookup, callerRoot(), mkObj(reg, 0o777)), vfs.ErrNotDir)
	add("OpLookup: the owner, of a symlink 0o777", mkReq(policy.OpLookup, callerOwner(), mkObj(lnk, 0o777)), vfs.ErrNotDir)
	add("OpLookup: the owner, of a FileType(0) 0o777", mkReq(policy.OpLookup, callerOwner(), mkObj(vfs.FileType(0), 0o777)), vfs.ErrNotDir)
	add("OpLookup: the owner, of a directory 0o677", mkReq(policy.OpLookup, callerOwner(), mkObj(dir, 0o677)), vfs.ErrAcces)
	add("OpLookup: a primary-group member, of a directory 0o767", mkReq(policy.OpLookup, callerGroup(), mkObj(dir, 0o767)), vfs.ErrAcces)
	add("OpLookup: a supplementary-group member, of a directory 0o707", mkReq(policy.OpLookup, callerSupp(), mkObj(dir, 0o707)), vfs.ErrAcces)
	add("OpLookup: other, of a directory 0o776", mkReq(policy.OpLookup, callerOther(), mkObj(dir, 0o776)), vfs.ErrAcces)

	// OpReadDir: ErrNotDir unless Object is a directory; ErrAcces unless
	// Permits(Caller, Object, PermRead).
	add("OpReadDir: the owner, of a directory 0o400", mkReq(policy.OpReadDir, callerOwner(), mkObj(dir, 0o400)), nil)
	add("OpReadDir: a primary-group member, of a directory 0o040", mkReq(policy.OpReadDir, callerGroup(), mkObj(dir, 0o040)), nil)
	add("OpReadDir: a supplementary-group member, of a directory 0o040", mkReq(policy.OpReadDir, callerSupp(), mkObj(dir, 0o040)), nil)
	add("OpReadDir: other, of a directory 0o004", mkReq(policy.OpReadDir, callerOther(), mkObj(dir, 0o004)), nil)
	add("OpReadDir: root, of a directory 0o000", mkReq(policy.OpReadDir, callerRoot(), mkObj(dir, 0)), nil)
	add("OpReadDir: the owner, of a regular file 0o777", mkReq(policy.OpReadDir, callerOwner(), mkObj(reg, 0o777)), vfs.ErrNotDir)
	add("OpReadDir: root, of a symlink 0o777", mkReq(policy.OpReadDir, callerRoot(), mkObj(lnk, 0o777)), vfs.ErrNotDir)
	add("OpReadDir: the owner, of a directory 0o377", mkReq(policy.OpReadDir, callerOwner(), mkObj(dir, 0o377)), vfs.ErrAcces)
	add("OpReadDir: a primary-group member, of a directory 0o737", mkReq(policy.OpReadDir, callerGroup(), mkObj(dir, 0o737)), vfs.ErrAcces)
	add("OpReadDir: other, of a directory 0o773", mkReq(policy.OpReadDir, callerOther(), mkObj(dir, 0o773)), vfs.ErrAcces)

	// OpAddEntry and OpRemoveEntry: ErrNotDir unless Object is a directory;
	// ErrAcces unless Permits(Caller, Object, PermWrite|PermExec). No mode
	// has 0o1000 set (U4).
	for _, op := range []policy.Op{policy.OpAddEntry, policy.OpRemoveEntry} {
		n := opName(op)
		add(n+": the owner, of a directory 0o300", mkReq(op, callerOwner(), mkObj(dir, 0o300)), nil)
		add(n+": a primary-group member, of a directory 0o030", mkReq(op, callerGroup(), mkObj(dir, 0o030)), nil)
		add(n+": a supplementary-group member, of a directory 0o030", mkReq(op, callerSupp(), mkObj(dir, 0o030)), nil)
		add(n+": other, of a directory 0o003", mkReq(op, callerOther(), mkObj(dir, 0o003)), nil)
		add(n+": root, of a directory 0o000", mkReq(op, callerRoot(), mkObj(dir, 0)), nil)
		add(n+": the owner, of a regular file 0o777", mkReq(op, callerOwner(), mkObj(reg, 0o777)), vfs.ErrNotDir)
		add(n+": root, of a symlink 0o777", mkReq(op, callerRoot(), mkObj(lnk, 0o777)), vfs.ErrNotDir)
		add(n+": the owner, of a directory 0o577", mkReq(op, callerOwner(), mkObj(dir, 0o577)), vfs.ErrAcces)
		add(n+": the owner, of a directory 0o677", mkReq(op, callerOwner(), mkObj(dir, 0o677)), vfs.ErrAcces)
		add(n+": a primary-group member, of a directory 0o757", mkReq(op, callerGroup(), mkObj(dir, 0o757)), vfs.ErrAcces)
		add(n+": a supplementary-group member, of a directory 0o020", mkReq(op, callerSupp(), mkObj(dir, 0o020)), vfs.ErrAcces)
		add(n+": other, of a directory 0o775", mkReq(op, callerOther(), mkObj(dir, 0o775)), vfs.ErrAcces)
	}

	// OpRename: ErrNotDir unless Object and Dst are both directories; ErrAcces
	// unless both Permits(Caller, Object, PermWrite|PermExec) and
	// Permits(Caller, Dst, PermWrite|PermExec). No mode has 0o1000 set (U4).
	add("OpRename: the owner, of both 0o300", mkRename(callerOwner(), mkObj(dir, 0o300), mkObj(dir, 0o300)), nil)
	add("OpRename: root, of both 0o000", mkRename(callerRoot(), mkObj(dir, 0), mkObj(dir, 0)), nil)
	add("OpRename: other, of both 0o003", mkRename(callerOther(), mkObj(dir, 0o003), mkObj(dir, 0o003)), nil)
	add("OpRename: a supplementary-group member, of both 0o030", mkRename(callerSupp(), mkObj(dir, 0o030), mkObj(dir, 0o030)), nil)
	add("OpRename: the owner of the source 0o300, other of a foreign target 0o003", mkRename(callerOwner(), mkObj(dir, 0o300), foreignDir(0o003)), nil)
	add("OpRename: the owner, a regular-file source", mkRename(callerOwner(), mkObj(reg, 0o777), mkObj(dir, 0o777)), vfs.ErrNotDir)
	add("OpRename: the owner, a regular-file target", mkRename(callerOwner(), mkObj(dir, 0o777), mkObj(reg, 0o777)), vfs.ErrNotDir)
	add("OpRename: root, a symlink target", mkRename(callerRoot(), mkObj(dir, 0), mkObj(lnk, 0o777)), vfs.ErrNotDir)
	add("OpRename: root, both regular files", mkRename(callerRoot(), mkObj(reg, 0o777), mkObj(reg, 0o777)), vfs.ErrNotDir)
	add("OpRename: the owner, a source 0o500", mkRename(callerOwner(), mkObj(dir, 0o500), mkObj(dir, 0o300)), vfs.ErrAcces)
	add("OpRename: the owner, a target 0o600", mkRename(callerOwner(), mkObj(dir, 0o300), mkObj(dir, 0o600)), vfs.ErrAcces)
	add("OpRename: the owner of the source 0o300, other of a foreign target 0o370", mkRename(callerOwner(), mkObj(dir, 0o300), foreignDir(0o370)), vfs.ErrAcces)
	add("OpRename: other of a foreign source 0o001, the owner of the target 0o300", mkRename(callerOwner(), foreignDir(0o001), mkObj(dir, 0o300)), vfs.ErrAcces)
	add("OpRename: a primary-group member, of both 0o050", mkRename(callerGroup(), mkObj(dir, 0o050), mkObj(dir, 0o050)), vfs.ErrAcces)

	return cs
}

// p5Cases are requests where both a row's type check and its permission check
// fail, so the type check's refusal must be the one given (P5). None is asked
// of the owner for OpRead, OpWrite or OpTruncate (U1), and OpRead,
// OpWrite and OpTruncate are asked only of directories (U3).
func p5Cases() []unixCase {
	reg, dir, lnk := vfs.TypeReg, vfs.TypeDir, vfs.TypeLnk
	return []unixCase{
		{name: "OpRead: other, of a directory 0o000", req: mkReq(policy.OpRead, callerOther(), mkObj(dir, 0)), want: vfs.ErrIsDir},
		{name: "OpRead: a primary-group member, of a directory 0o000", req: mkReq(policy.OpRead, callerGroup(), mkObj(dir, 0)), want: vfs.ErrIsDir},
		{name: "OpRead: root, of a directory 0o000", req: mkReq(policy.OpRead, callerRoot(), mkObj(dir, 0)), want: vfs.ErrIsDir},
		{name: "OpWrite: other, of a directory 0o000", req: mkReq(policy.OpWrite, callerOther(), mkObj(dir, 0)), want: vfs.ErrIsDir},
		{name: "OpTruncate: a supplementary-group member, of a directory 0o000", req: mkReq(policy.OpTruncate, callerSupp(), mkObj(dir, 0)), want: vfs.ErrIsDir},
		{name: "OpLookup: other, of a regular file 0o000", req: mkReq(policy.OpLookup, callerOther(), mkObj(reg, 0)), want: vfs.ErrNotDir},
		{name: "OpLookup: the owner, of a regular file 0o000", req: mkReq(policy.OpLookup, callerOwner(), mkObj(reg, 0)), want: vfs.ErrNotDir},
		{name: "OpLookup: other, of a symlink 0o000", req: mkReq(policy.OpLookup, callerOther(), mkObj(lnk, 0)), want: vfs.ErrNotDir},
		{name: "OpReadDir: other, of a regular file 0o000", req: mkReq(policy.OpReadDir, callerOther(), mkObj(reg, 0)), want: vfs.ErrNotDir},
		{name: "OpAddEntry: other, of a regular file 0o000", req: mkReq(policy.OpAddEntry, callerOther(), mkObj(reg, 0)), want: vfs.ErrNotDir},
		{name: "OpRemoveEntry: a primary-group member, of a regular file 0o000", req: mkReq(policy.OpRemoveEntry, callerGroup(), mkObj(reg, 0)), want: vfs.ErrNotDir},
		{name: "OpRename: the owner, an unsearchable source directory 0o000 and a regular-file target", req: mkRename(callerOwner(), mkObj(dir, 0), mkObj(reg, 0o777)), want: vfs.ErrNotDir},
		{name: "OpRename: the owner, a regular-file source and an unsearchable target directory 0o666", req: mkRename(callerOwner(), mkObj(reg, 0o777), mkObj(dir, 0o666)), want: vfs.ErrNotDir},
		{name: "OpRename: other, an unsearchable source directory 0o000 and a symlink target 0o000", req: mkRename(callerOther(), mkObj(dir, 0), mkObj(lnk, 0)), want: vfs.ErrNotDir},
	}
}

// unknownOps is Op(0) and every Op from 13 to 255 (§11 P6).
func unknownOps() []policy.Op {
	ops := []policy.Op{0}
	for op := 13; op <= 255; op++ {
		ops = append(ops, policy.Op(op))
	}
	return ops
}

// checkUnix asserts one unixCase's pinned answer. A refusal must be the bare
// vfs.Status, compared with == (§5: never wrapped).
func checkUnix(t *testing.T, what string, tc unixCase, err error) {
	t.Helper()
	switch {
	case tc.refusedOnly:
		if err == nil {
			t.Errorf("%s: %s: Check(%+v) = nil, want a refusal: neither root nor the owner, and no write permission (ADR 0014 §5's OpSetTimes row; §11 U6 pins only that it is refused)",
				what, tc.name, tc.req)
		}
	case tc.want == nil:
		if err != nil {
			t.Errorf("%s: %s: Check(%+v) = %v, want nil (ADR 0014 §5's Unix table, §11 P4)", what, tc.name, tc.req, err)
		}
	default:
		if err != tc.want {
			t.Errorf("%s: %s: Check(%+v) = %#v (%v), want the bare %v, compared with ==: each refusal is the bare vfs.Status, never wrapped (ADR 0014 §5's Unix table, §11 P4)",
				what, tc.name, tc.req, err, err, tc.want)
		}
	}
}

// TestUnixPinnedRows covers P4 (ADR 0014 §11): every pinned row of §5's Unix
// table, allowing and refusing, with err == the vfs.Status itself, and, for
// the refusal U6 pins as a refusal only, err non-nil.
func TestUnixPinnedRows(t *testing.T) {
	u := policy.Unix()
	for _, tc := range unixCases() {
		checkUnix(t, "Unix()", tc, u.Check(tc.req))
	}
}

// TestUnixChecksTypeBeforePermission covers P5 (ADR 0014 §11): the type check
// comes before the permission check, so a request that fails both gets the
// type check's refusal (§5: in each row the first check that fails gives the
// refusal).
func TestUnixChecksTypeBeforePermission(t *testing.T) {
	u := policy.Unix()
	for _, tc := range p5Cases() {
		checkUnix(t, "Unix()", tc, u.Check(tc.req))
	}
}

// TestUnixRefusesAnUnknownOp covers P6 (ADR 0014 §11): Op(0), and every Op
// from 13 to 255, give vfs.ErrServerFault, for every caller, root included
// (§5).
func TestUnixRefusesAnUnknownOp(t *testing.T) {
	u := policy.Unix()
	objs := []policy.Object{
		mkObj(vfs.TypeDir, 0o777),
		mkObj(vfs.TypeReg, 0o777),
		mkObj(vfs.TypeDir, 0),
		{},
	}
	l := &limiter{t: t, limit: 20}
	defer l.done()
	for _, op := range unknownOps() {
		for _, nc := range p2Callers() {
			for _, o := range objs {
				r := policy.Request{Op: op, Caller: nc.c, Object: o, Dst: mkObj(vfs.TypeDir, 0o777), UID: fileUID, GID: fileGID}
				if err := u.Check(r); err != vfs.ErrServerFault {
					l.errorf("Unix().Check(%s, by %s, of %+v) = %v, want the bare vfs.ErrServerFault: any other Op, 0 included, fails closed for every caller, root included (ADR 0014 §5, §11 P6)",
						opName(op), nc.name, o, err)
				}
			}
		}
	}
}

// recorder is a Policy a test writes, as §5 allows: a Func that records what
// it is asked, under its own lock.
type recorder struct {
	mu  sync.Mutex
	log []string
}

// step returns a Policy that records name and answers err.
func (rec *recorder) step(name string, err error) policy.Policy {
	return policy.Func(func(policy.Request) error {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.log = append(rec.log, name)
		return err
	})
}

// asked returns the names recorded so far, in order.
func (rec *recorder) asked() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.log)
}

// refusal is an error type of the test's own, so that All's returning an
// error unchanged is checked for a pointer type too.
type refusal struct{ why string }

func (r *refusal) Error() string { return r.why }

// sampleRequest is a request with every field set.
func sampleRequest() policy.Request {
	return policy.Request{
		Op:     policy.OpRename,
		Caller: vfs.Caller{UID: 4000, GID: 400, GIDs: []uint32{402, fileGID, 401}},
		Object: mkObj(vfs.TypeDir, 0o755),
		Dst:    foreignDir(0o3),
		UID:    1234,
		GID:    5678,
	}
}

// allRequests is every request P4, P5 and P6 ask about.
func allRequests() []policy.Request {
	var rs []policy.Request
	for _, tc := range unixCases() {
		rs = append(rs, tc.req)
	}
	for _, tc := range p5Cases() {
		rs = append(rs, tc.req)
	}
	for _, op := range unknownOps() {
		rs = append(rs, mkReq(op, callerOther(), mkObj(vfs.TypeReg, 0)))
	}
	return rs
}

// TestAllWithNoPoliciesAllows covers P7's first point (ADR 0014 §11): All
// with no policies allows everything (§5), whatever Unix() would answer.
func TestAllWithNoPoliciesAllows(t *testing.T) {
	empties := []struct {
		name string
		p    policy.Policy
	}{
		{name: "All()", p: policy.All()},
		{name: "All of an empty slice", p: policy.All([]policy.Policy{}...)},
		{name: "All of a nil slice", p: policy.All([]policy.Policy(nil)...)},
	}
	for _, e := range empties {
		for _, r := range allRequests() {
			if err := e.p.Check(r); err != nil {
				t.Errorf("%s.Check(%+v) = %v, want nil: with no policies, All allows everything (ADR 0014 §5, §11 P7)", e.name, r, err)
			}
		}
	}
}

// TestAllAsksInOrder covers P7's second point (ADR 0014 §11): All asks each
// of its policies in order (§5).
func TestAllAsksInOrder(t *testing.T) {
	rec := &recorder{}
	p := policy.All(rec.step("first", nil), rec.step("second", nil), rec.step("third", nil))
	for i := range 2 {
		if err := p.Check(sampleRequest()); err != nil {
			t.Errorf("All of three allowing policies, call %d: Check = %v, want nil (ADR 0014 §5, §11 P7)", i, err)
		}
	}
	want := []string{"first", "second", "third", "first", "second", "third"}
	if got := rec.asked(); !slices.Equal(got, want) {
		t.Errorf("All of first, second and third, asked twice, asked %q, want %q: All asks each of its policies in order (ADR 0014 §5, §11 P7)", got, want)
	}
}

// TestAllStopsAtTheFirstRefusal covers P7's third point (ADR 0014 §11): All
// returns the first non-nil error unchanged, and asks none after it (§5).
func TestAllStopsAtTheFirstRefusal(t *testing.T) {
	refusals := []error{
		vfs.ErrNXIO,
		vfs.ErrAcces,
		errors.New("a refusal of the test's own"),
		fmt.Errorf("a wrapped refusal: %w", vfs.ErrPerm),
		&refusal{why: "a refusal of a pointer type of the test's own"},
	}
	for _, want := range refusals {
		rec := &recorder{}
		later := &refusal{why: "a later refusal, which All must not reach"}
		p := policy.All(rec.step("first", nil), rec.step("second", want), rec.step("third", later), rec.step("fourth", nil))
		if err := p.Check(sampleRequest()); err != want {
			t.Errorf("All(allow, refuse with %q, refuse, allow).Check = %v, want the second policy's error itself: All returns the first non-nil error unchanged (ADR 0014 §5, §11 P7)", want, err)
		}
		if got, wantAsked := rec.asked(), []string{"first", "second"}; !slices.Equal(got, wantAsked) {
			t.Errorf("All(allow, refuse with %q, refuse, allow) asked %q, want %q: All asks none after the first refusal (ADR 0014 §5, §11 P7)", want, got, wantAsked)
		}

		rec = &recorder{}
		p = policy.All(rec.step("first", want), rec.step("second", nil))
		if err := p.Check(sampleRequest()); err != want {
			t.Errorf("All(refuse with %q, allow).Check = %v, want the first policy's error itself (ADR 0014 §5, §11 P7)", want, err)
		}
		if got, wantAsked := rec.asked(), []string{"first"}; !slices.Equal(got, wantAsked) {
			t.Errorf("All(refuse with %q, allow) asked %q, want %q (ADR 0014 §5, §11 P7)", want, got, wantAsked)
		}
	}

	// Unix() in front, as §6 has a later ADR install more.
	rec := &recorder{}
	p := policy.All(policy.Unix(), rec.step("after Unix", nil))
	if err := p.Check(mkReq(policy.OpLookup, callerOther(), mkObj(vfs.TypeReg, 0o777))); err != vfs.ErrNotDir {
		t.Errorf("All(Unix(), allow).Check(OpLookup of a regular file) = %v, want Unix()'s bare vfs.ErrNotDir, unchanged (ADR 0014 §5, §11 P7)", err)
	}
	if got := rec.asked(); len(got) != 0 {
		t.Errorf("All(Unix(), allow), after Unix() refused, asked %q, want nothing (ADR 0014 §5, §11 P7)", got)
	}
	if err := p.Check(mkReq(policy.OpLookup, callerOwner(), mkObj(vfs.TypeDir, 0o700))); err != nil {
		t.Errorf("All(Unix(), allow).Check(OpLookup by the owner of a directory 0o700) = %v, want nil (ADR 0014 §5, §11 P7)", err)
	}
	if got, want := rec.asked(), []string{"after Unix"}; !slices.Equal(got, want) {
		t.Errorf("All(Unix(), allow), after Unix() allowed, asked %q, want %q (ADR 0014 §5, §11 P7)", got, want)
	}
}

// TestAllKeepsItsOwnCopy covers P7's fourth point (ADR 0014 §11): All keeps
// its own copy of its arguments, so changing the caller's slice afterwards
// changes nothing (§5).
func TestAllKeepsItsOwnCopy(t *testing.T) {
	allow := policy.Func(func(policy.Request) error { return nil })
	refuse := policy.Func(func(policy.Request) error { return vfs.ErrNXIO })

	ps := []policy.Policy{allow, allow}
	p := policy.All(ps...)
	ps[0], ps[1] = refuse, refuse
	if err := p.Check(sampleRequest()); err != nil {
		t.Errorf("All(allow, allow), after the caller's slice was changed to two refusals, Check = %v, want nil: All keeps its own copy of ps (ADR 0014 §5, §11 P7)", err)
	}

	qs := []policy.Policy{refuse}
	q := policy.All(qs...)
	qs[0] = allow
	if err := q.Check(sampleRequest()); err != vfs.ErrNXIO {
		t.Errorf("All(refuse), after the caller's slice was changed to an allow, Check = %v, want vfs.ErrNXIO: All keeps its own copy of ps (ADR 0014 §5, §11 P7)", err)
	}
}

// TestAllPanicsOnANilPolicy covers P7's fifth point (ADR 0014 §11): All
// panics, when it is called and not later at Check, if any of its policies is
// nil, or is a Func whose value is nil (§5).
func TestAllPanicsOnANilPolicy(t *testing.T) {
	allow := policy.Func(func(policy.Request) error { return nil })
	var nilFunc policy.Func
	var nilFuncAsPolicy policy.Policy = policy.Func(nil)
	cases := []struct {
		name string
		ps   []policy.Policy
	}{
		{name: "a nil Policy alone", ps: []policy.Policy{nil}},
		{name: "a nil Policy after another", ps: []policy.Policy{allow, nil}},
		{name: "a nil Policy before another", ps: []policy.Policy{nil, allow}},
		{name: "a nil Func alone", ps: []policy.Policy{nilFunc}},
		{name: "a nil Func after another", ps: []policy.Policy{allow, policy.Func(nil)}},
		{name: "a nil Func held as a Policy, between two others", ps: []policy.Policy{allow, nilFuncAsPolicy, allow}},
	}
	for _, tc := range cases {
		ps := tc.ps
		if did, _ := panicked(func() { policy.All(ps...) }); !did {
			t.Errorf("All with %s did not panic, want a panic at construction: a nil policy is a programming error (ADR 0014 §5, §11 P7)", tc.name)
		}
	}
	if did, _ := panicked(func() { policy.All(nil) }); !did {
		t.Errorf("All(nil) did not panic, want a panic (ADR 0014 §5, §11 P7)")
	}
	if did, _ := panicked(func() { policy.All(policy.Func(nil)) }); !did {
		t.Errorf("All(Func(nil)) did not panic, want a panic (ADR 0014 §5, §11 P7)")
	}
}

// cloneRequest is a deep copy of r.
func cloneRequest(r policy.Request) policy.Request {
	c := r
	c.Caller.GIDs = slices.Clone(r.Caller.GIDs)
	return c
}

// sameRequest reports whether a and b have the same fields, comparing GIDs by
// content.
func sameRequest(a, b policy.Request) bool {
	return a.Op == b.Op && a.Caller.UID == b.Caller.UID && a.Caller.GID == b.Caller.GID &&
		slices.Equal(a.Caller.GIDs, b.Caller.GIDs) && a.Object == b.Object && a.Dst == b.Dst &&
		a.UID == b.UID && a.GID == b.GID
}

// TestFuncCallsItsFunctionOnce covers P8 (ADR 0014 §11): Func(f).Check(r)
// calls f once, with the request unchanged, and returns its result (§5).
func TestFuncCallsItsFunctionOnce(t *testing.T) {
	results := []error{nil, vfs.ErrNXIO, errors.New("a refusal of the test's own"), &refusal{why: "a pointer refusal"}}
	for _, ret := range results {
		calls := 0
		var got policy.Request
		f := policy.Func(func(r policy.Request) error {
			calls++
			got = cloneRequest(r)
			return ret
		})
		r := sampleRequest()
		want := cloneRequest(r)
		err := f.Check(r)
		if calls != 1 {
			t.Errorf("Func(f).Check called f %d times, want 1 (ADR 0014 §5, §11 P8)", calls)
		}
		if !sameRequest(got, want) {
			t.Errorf("Func(f).Check passed f %+v, want the request unchanged, %+v (ADR 0014 §5, §11 P8)", got, want)
		}
		if err != ret {
			t.Errorf("Func(f).Check = %v, want f's result itself, %v (ADR 0014 §5, §11 P8)", err, ret)
		}
	}
}

// allAccess is the OR of the six vfs.Access constants.
const allAccess = vfs.AccessRead | vfs.AccessLookup | vfs.AccessModify | vfs.AccessExtend | vfs.AccessDelete | vfs.AccessExecute

// TestAccessBitsPinnedRows covers P9 (ADR 0014 §11), exactly as it lists the
// rows, over P2's grid, with oraclePermits standing for Permits. Where a
// directory may read but not search, or write but not search, it asserts
// nothing about vfs.AccessLookup or the write bits, since U5 leaves that
// unpinned.
func TestAccessBitsPinnedRows(t *testing.T) {
	l := &limiter{t: t, limit: 20}
	defer l.done()
	modes := p2Modes()
	for _, nc := range p2Callers() {
		for _, typ := range p2Types {
			for _, mode := range modes {
				o := policy.Object{Type: typ, Mode: mode, UID: fileUID, GID: fileGID}
				bits := policy.AccessBits(nc.c, o)
				read := oraclePermits(nc.c, o, policy.PermRead)
				write := oraclePermits(nc.c, o, policy.PermWrite)
				exec := oraclePermits(nc.c, o, policy.PermExec)
				writeExec := oraclePermits(nc.c, o, policy.PermWrite|policy.PermExec)
				has := func(bit uint32) bool { return bits&bit != 0 }
				what := fmt.Sprintf("AccessBits(%s %+v, {Type: %d, Mode: %#o}) = %#x", nc.name, nc.c, typ, mode, bits)

				if bits&^allAccess != 0 {
					l.errorf("%s: it sets a bit outside the six vfs.Access constants (ADR 0014 §5, §11 P9)", what)
				}
				if has(vfs.AccessRead) != read {
					l.errorf("%s: vfs.AccessRead is set exactly when Permits(c, o, PermRead), which is %v (ADR 0014 §5, §11 P9)", what, read)
				}
				if typ != vfs.TypeDir {
					if has(vfs.AccessLookup) || has(vfs.AccessDelete) {
						l.errorf("%s: a non-directory never gets vfs.AccessLookup or vfs.AccessDelete (ADR 0014 §5, §11 P9)", what)
					}
					if has(vfs.AccessModify) != write || has(vfs.AccessExtend) != write {
						l.errorf("%s: a non-directory gets vfs.AccessModify and vfs.AccessExtend exactly when Permits(c, o, PermWrite), which is %v (ADR 0014 §5, §11 P9)", what, write)
					}
					if wantExec := mode&0o111 != 0 && exec; has(vfs.AccessExecute) != wantExec {
						l.errorf("%s: a non-directory gets vfs.AccessExecute exactly when o.Mode & 0o111 != 0 and Permits(c, o, PermExec), which is %v (ADR 0014 §5, §11 P9)", what, wantExec)
					}
					continue
				}
				if has(vfs.AccessExecute) {
					l.errorf("%s: a directory never gets vfs.AccessExecute (ADR 0014 §5, §11 P9)", what)
				}
				if exec && !has(vfs.AccessLookup) {
					l.errorf("%s: a directory gets vfs.AccessLookup when Permits(c, o, PermExec) (ADR 0014 §5, §11 P9)", what)
				}
				if !read && !exec && has(vfs.AccessLookup) {
					l.errorf("%s: a directory gets no vfs.AccessLookup with neither Permits(c, o, PermRead) nor Permits(c, o, PermExec) (ADR 0014 §5, §11 P9)", what)
				}
				if writeExec && !(has(vfs.AccessModify) && has(vfs.AccessExtend) && has(vfs.AccessDelete)) {
					l.errorf("%s: a directory gets all of vfs.AccessModify, vfs.AccessExtend and vfs.AccessDelete when Permits(c, o, PermWrite|PermExec) (ADR 0014 §5, §11 P9)", what)
				}
				if !write && (has(vfs.AccessModify) || has(vfs.AccessExtend) || has(vfs.AccessDelete)) {
					l.errorf("%s: a directory gets none of vfs.AccessModify, vfs.AccessExtend and vfs.AccessDelete without Permits(c, o, PermWrite) (ADR 0014 §5, §11 P9)", what)
				}
			}
		}
	}
}

// p10Result is everything one run of P10's calls returned, in call order.
type p10Result struct {
	inGroup []bool
	permits []bool
	access  []uint32
	checks  []error
}

// TestConcurrentCallsAgreeWithASequentialRun covers P10 (ADR 0014 §11):
// InGroup, Permits, AccessBits and Unix().Check, called concurrently on shared
// arguments, one shared Unix() value included, are race-free under go test
// -race, and agree with a sequential run (§5: each decision is a function of
// its arguments alone, and is safe for concurrent use).
func TestConcurrentCallsAgreeWithASequentialRun(t *testing.T) {
	callers := p2Callers()
	gids := []uint32{0, fileGID, 300, 301, 400, 500, 502, 999}
	var objs []policy.Object
	for _, typ := range p2Types {
		for _, mode := range []uint32{0, 0o007, 0o070, 0o700, 0o644, 0o755, 0o777, 0o1777, 0o4711} {
			objs = append(objs, policy.Object{Type: typ, Mode: mode, UID: fileUID, GID: fileGID})
		}
	}
	reqs := allRequests()
	u := policy.Unix()

	run := func() p10Result {
		var r p10Result
		for _, nc := range callers {
			for _, g := range gids {
				r.inGroup = append(r.inGroup, policy.InGroup(nc.c, g))
			}
			for _, o := range objs {
				for want := policy.Perm(0); want <= 8; want++ {
					r.permits = append(r.permits, policy.Permits(nc.c, o, want))
				}
				r.access = append(r.access, policy.AccessBits(nc.c, o))
			}
		}
		for _, req := range reqs {
			r.checks = append(r.checks, u.Check(req))
		}
		return r
	}

	want := run()
	const workers = 8
	got := make([]p10Result, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i] = run()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, g := range got {
		if !slices.Equal(g.inGroup, want.inGroup) {
			t.Errorf("goroutine %d: concurrent InGroup answers differ from a sequential run (ADR 0014 §5, §11 P10)", i)
		}
		if !slices.Equal(g.permits, want.permits) {
			t.Errorf("goroutine %d: concurrent Permits answers differ from a sequential run (ADR 0014 §5, §11 P10)", i)
		}
		if !slices.Equal(g.access, want.access) {
			t.Errorf("goroutine %d: concurrent AccessBits answers differ from a sequential run (ADR 0014 §5, §11 P10)", i)
		}
		if !slices.Equal(g.checks, want.checks) {
			t.Errorf("goroutine %d: concurrent Unix().Check answers differ from a sequential run (ADR 0014 §5, §11 P10)", i)
		}
	}
}

// TestTwoUnixValuesDecideAlike covers P11 (ADR 0014 §11): two Unix() values
// decide every P4 request alike (§5: two calls decide alike). It also asks
// them P5's and P6's requests.
func TestTwoUnixValuesDecideAlike(t *testing.T) {
	u1, u2 := policy.Unix(), policy.Unix()
	for _, r := range allRequests() {
		if e1, e2 := u1.Check(r), u2.Check(r); e1 != e2 {
			t.Errorf("two Unix() values decide %+v as %v and %v, want alike (ADR 0014 §5, §11 P11)", r, e1, e2)
		}
	}
}

// TestNoCallModifiesCallerGIDs covers P12 (ADR 0014 §11): no call of InGroup,
// Permits, AccessBits, or the Check of Unix(), All(...) or Func, modifies
// Caller.GIDs, neither its contents nor its length (§5's purity rule). The
// GIDs are out of order and hold a repeat, so that a sort or a dedupe in place
// would show.
func TestNoCallModifiesCallerGIDs(t *testing.T) {
	bases := []vfs.Caller{
		{UID: 0, GID: 0, GIDs: []uint32{902, fileGID, 901, 902, 7}},
		{UID: fileUID, GID: 500, GIDs: []uint32{902, fileGID, 901, 902, 7}},
		{UID: 3000, GID: 300, GIDs: []uint32{902, fileGID, 901, 902, 7}},
		{UID: 4000, GID: 400, GIDs: []uint32{1019, 1003, 1017, 1001, 1015, 1005, 1013, 1007, 1011, 1009, 1010, 1008, 1012, 1006, 1014, 1004, 1016, 1002, 1018, 1000}},
	}
	objs := []policy.Object{mkObj(vfs.TypeReg, 0o754), mkObj(vfs.TypeDir, 0o751), mkObj(vfs.TypeLnk, 0o777), mkObj(vfs.TypeDir, 0)}
	u := policy.Unix()
	all := policy.All(policy.Unix(), policy.Func(func(policy.Request) error { return nil }))
	fn := policy.Func(func(policy.Request) error { return nil })

	run := func(what string, base vfs.Caller, call func(c vfs.Caller)) {
		t.Helper()
		c := base
		c.GIDs = slices.Clone(base.GIDs)
		n := len(c.GIDs)
		call(c)
		if len(c.GIDs) != n || !slices.Equal(c.GIDs, base.GIDs) {
			t.Errorf("%s with GIDs %v left them %v: no call modifies Caller.GIDs, neither its contents nor its length (ADR 0014 §5, §11 P12)",
				what, base.GIDs, c.GIDs)
		}
	}

	for _, base := range bases {
		for _, g := range []uint32{0, fileGID, 7, 902, 1000, 1019, 5555} {
			run(fmt.Sprintf("InGroup(c, %d)", g), base, func(c vfs.Caller) { policy.InGroup(c, g) })
		}
		for _, o := range objs {
			for want := policy.Perm(0); want <= 8; want++ {
				run(fmt.Sprintf("Permits(c, %+v, %d)", o, want), base, func(c vfs.Caller) { policy.Permits(c, o, want) })
			}
			run(fmt.Sprintf("AccessBits(c, %+v)", o), base, func(c vfs.Caller) { policy.AccessBits(c, o) })
			for op := 0; op <= 13; op++ {
				r := policy.Request{Op: policy.Op(op), Object: o, Dst: o, UID: 5555, GID: fileGID}
				run(fmt.Sprintf("Unix().Check(%s of %+v)", opName(policy.Op(op)), o), base, func(c vfs.Caller) {
					r.Caller = c
					_ = u.Check(r)
				})
				run(fmt.Sprintf("All(Unix(), allow).Check(%s of %+v)", opName(policy.Op(op)), o), base, func(c vfs.Caller) {
					r.Caller = c
					_ = all.Check(r)
				})
				run(fmt.Sprintf("Func(allow).Check(%s of %+v)", opName(policy.Op(op)), o), base, func(c vfs.Caller) {
					r.Caller = c
					_ = fn.Check(r)
				})
			}
		}
	}

	for _, tc := range unixCases() {
		ref := slices.Clone(tc.req.Caller.GIDs)
		_ = u.Check(tc.req)
		if !slices.Equal(tc.req.Caller.GIDs, ref) || len(tc.req.Caller.GIDs) != len(ref) {
			t.Errorf("%s: Unix().Check left the caller's GIDs %v, want %v (ADR 0014 §5, §11 P12)", tc.name, tc.req.Caller.GIDs, ref)
		}
	}
}

// TestUnreadFieldsChangeNothing covers P13 (ADR 0014 §11): Dst, UID and GID
// may be set to anything, except UID for OpChown, GID for OpChgrp and Dst for
// OpRename, without changing Unix()'s answer (§5: Check reads only the fields
// its row names). It asks every P4 and P5 request.
func TestUnreadFieldsChangeNothing(t *testing.T) {
	u := policy.Unix()
	dsts := []policy.Object{
		{},
		mkObj(vfs.TypeDir, 0o777),
		mkObj(vfs.TypeReg, 0),
		{Type: vfs.TypeLnk, Mode: 0o7777, UID: 0, GID: 0},
		foreignDir(0o003),
		{Type: vfs.FileType(99), Mode: ^uint32(0), UID: ^uint32(0), GID: ^uint32(0)},
	}
	ids := []uint32{0, fileUID, fileGID, 500, 5555, 7777, ^uint32(0)}
	cases := append(unixCases(), p5Cases()...)
	for _, tc := range cases {
		base := u.Check(tc.req)
		op := tc.req.Op
		if op != policy.OpRename {
			for _, d := range dsts {
				r := tc.req
				r.Dst = d
				if err := u.Check(r); err != base {
					t.Errorf("%s: with Dst %+v, Unix().Check = %v, want %v as without it: %s does not read Dst (ADR 0014 §5, §11 P13)", tc.name, d, err, base, opName(op))
				}
			}
		}
		if op != policy.OpChown {
			for _, id := range ids {
				r := tc.req
				r.UID = id
				if err := u.Check(r); err != base {
					t.Errorf("%s: with UID %d, Unix().Check = %v, want %v as without it: %s does not read UID (ADR 0014 §5, §11 P13)", tc.name, id, err, base, opName(op))
				}
			}
		}
		if op != policy.OpChgrp {
			for _, id := range ids {
				r := tc.req
				r.GID = id
				if err := u.Check(r); err != base {
					t.Errorf("%s: with GID %d, Unix().Check = %v, want %v as without it: %s does not read GID (ADR 0014 §5, §11 P13)", tc.name, id, err, base, opName(op))
				}
			}
		}
	}
}
