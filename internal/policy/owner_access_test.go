package policy_test

// Clean-room tests for #93, written from ADR 0025,
// docs/adr/0025-a-files-owner-may-read-and-write-it-whatever-its-mode.md, §1,
// §2, §3 and §6, from ADR 0014 §5 and §11 for the policy API, Permits,
// AccessBits and the unpinned rows, and from internal/vfs/vfs.go, without
// reading the implementation.
//
// ADR 0025 §1's rows, each read from the top, the first line that holds giving
// the answer; a regular file is Type == vfs.TypeReg, and the owner is
// Caller.UID == Object.UID:
//
//	OpRead:               (1) vfs.ErrIsDir for a directory; (2) nil if
//	                      Permits(c, o, PermRead); (3) nil for a regular file
//	                      and its owner; (4) nil for a regular file if
//	                      Permits(c, o, PermExec); (5) vfs.ErrAcces.
//	OpWrite, OpTruncate:  (1) vfs.ErrIsDir for a directory; (2) nil if
//	                      Permits(c, o, PermWrite); (3) nil for a regular file
//	                      and its owner; (4) vfs.ErrAcces.
//
// Since ADR 0029 §1
// (docs/adr/0029-only-a-regular-file-is-read-written-or-given-a-size.md), each
// of the three rows has one more line after the directory's: vfs.ErrInval for
// a request that is not advisory whose Object is neither a regular file nor a
// directory, for every caller, root included, before any permission is looked
// at. Every Type but vfs.TypeReg and vfs.TypeDir, valid or not, is such an
// object. The requests A1 makes are not advisory.
//
// Covered, with the names ADR 0025 §6 pins:
//   - A1, TestTheFileRowsExhaustively;
//   - A3, TestADirectorysOwnerIsHeldToItsMode, a characterisation that passes
//     before the change and after;
//   - A4, TestAccessBitsStayApartFromTheFileRows.
//
// A2's rows are in policy_test.go's unixCases() and p5Cases(), so that P4,
// P5 and P10 to P13 ask them.
//
// These reuse policy_test.go's fixtures: the callers, p2Callers, p2Modes,
// p2Types, mkObj, mkReq, mkRename, oraclePermits, limiter and opName. Helpers
// of this file's own are prefixed oa. U3 is pinned whole since ADR 0029 §9:
// A1's branch for a type that is neither a regular file nor a directory is
// that ADR's N1, and asserts vfs.ErrInval for every caller, mode and Op. U4,
// U5 and U6 are not relied on: no directory here has 0o1000 set, no
// AccessBits is asked of a directory, and no time is set (ADR 0025 §6).

import (
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// oaFileOps are the three rows ADR 0025 §1 changes.
var oaFileOps = []policy.Op{policy.OpRead, policy.OpWrite, policy.OpTruncate}

// oaRowPerm is the permission line 2 of op's row asks Permits for.
func oaRowPerm(op policy.Op) policy.Perm {
	if op == policy.OpRead {
		return policy.PermRead
	}
	return policy.PermWrite
}

// oaRegularAllows is ADR 0025 §1's verdict for op on a regular file o, with
// oraclePermits standing for Permits: line 2, then the owner override of line
// 3, then, for OpRead alone, read with execute (line 4, §2).
func oaRegularAllows(op policy.Op, c vfs.Caller, o policy.Object) bool {
	if oraclePermits(c, o, oaRowPerm(op)) {
		return true
	}
	if c.UID == o.UID {
		return true
	}
	return op == policy.OpRead && oraclePermits(c, o, policy.PermExec)
}

// oaAllTypes is every vfs.FileType that A1 asks about: p2Types, then every
// other type internal/vfs/vfs.go names, and one value beyond them all. An
// inode's type is decoded from the bucket unchecked (ADR 0025 Assumption 3),
// so any of these can reach the rows.
var oaAllTypes = append(append([]vfs.FileType(nil), p2Types...),
	vfs.TypeBlk, vfs.TypeChr, vfs.TypeSock, vfs.TypeFifo, vfs.FileType(99))

// TestTheFileRowsExhaustively is A1 of ADR 0025 §6: over every mode of
// p2Modes(), every type of oaAllTypes, every caller of p2Callers() and each of
// OpRead, OpWrite and OpTruncate, Unix().Check agrees with an oracle written
// from §1's rows, with oraclePermits standing for Permits. The types are every
// one internal/vfs/vfs.go names, vfs.FileType(0) and vfs.FileType(99), not
// p2Types alone: an inode's type is decoded from the bucket unchecked, so the
// owner override and read with execute must reach the regular file and nothing
// else, whatever type an inode claims (ADR 0025 §1 and §4, Assumption 3):
//   - a directory: vfs.ErrIsDir, compared with ==;
//   - a regular file: nil or vfs.ErrAcces exactly as the lines give, compared
//     with ==;
//   - any other type: vfs.ErrInval, compared with ==, for every caller, root
//     included, every mode and every Op, whatever Permits would give. That
//     branch is ADR 0029 §9's N1, and pins ADR 0014 §11's U3 for a request
//     that is not advisory (ADR 0029 §1, line 2); before ADR 0029 it asserted
//     a refusal only where oraclePermits refused, and nothing where it
//     allowed.
func TestTheFileRowsExhaustively(t *testing.T) {
	u := policy.Unix()
	l := &limiter{t: t, limit: 20}
	defer l.done()
	modes := p2Modes()
	for _, nc := range p2Callers() {
		for _, typ := range oaAllTypes {
			for _, mode := range modes {
				o := mkObj(typ, mode)
				for _, op := range oaFileOps {
					err := u.Check(mkReq(op, nc.c, o))
					what := "Unix().Check(" + opName(op) + ", by " + nc.name + ")"
					switch typ {
					case vfs.TypeDir:
						if err != vfs.ErrIsDir {
							l.errorf("%s of a directory %#o = %#v (%v), want the bare vfs.ErrIsDir, compared with ==: "+
								"line 1 refuses a directory before any permission is looked at (ADR 0025 §1, §6 A1)",
								what, mode, err, err)
						}
					case vfs.TypeReg:
						var want error = vfs.ErrAcces
						if oaRegularAllows(op, nc.c, o) {
							want = nil
						}
						if err != want {
							l.errorf("%s of a regular file %#o = %#v (%v), want %v, compared with ==: line 2 allows "+
								"what Permits allows, line 3 the file's owner, line 4 OpRead to a caller whose "+
								"class has execute, and anything else is the bare vfs.ErrAcces (ADR 0025 §1 and "+
								"§2, §6 A1)", what, mode, err, err, want)
						}
					default:
						if err != vfs.ErrInval {
							l.errorf("%s of a type %d, mode %#o, = %#v (%v), want the bare vfs.ErrInval, compared "+
								"with ==: a request that is not advisory, of an object that is neither a regular "+
								"file nor a directory, is refused for its type, for every caller, root included, "+
								"before any permission is looked at (ADR 0029 §1, line 2; §9 N1; ADR 0014 §11 U3)",
								what, typ, mode, err, err)
						}
					}
				}
			}
		}
	}
}

// TestADirectorysOwnerIsHeldToItsMode is A3 of ADR 0025 §6, a
// characterisation that passes before the change and after: for every mode
// from 0 to 0o777, with no higher bit set (U4), a directory's owner, and its
// owner who is also in its group, are judged by the owner bits alone. OpLookup
// is allowed exactly when they have execute; OpReadDir, read; OpAddEntry,
// OpRemoveEntry and OpRename, with that directory as Object and a directory of
// mode 0o700 as Dst and again the other way round, write and execute; every
// refusal is the bare vfs.ErrAcces; and OpRead, OpWrite and OpTruncate are
// vfs.ErrIsDir for every mode (ADR 0025 §1: no directory row changes, and the
// override is a regular file's alone).
func TestADirectorysOwnerIsHeldToItsMode(t *testing.T) {
	u := policy.Unix()
	l := &limiter{t: t, limit: 20}
	defer l.done()
	owners := []namedCaller{
		{name: "the owner", c: callerOwner()},
		{name: "the owner, also in the group", c: callerOwnerInGroup()},
	}
	other := mkObj(vfs.TypeDir, 0o700)
	verdict := func(allowed bool) error {
		if allowed {
			return nil
		}
		return vfs.ErrAcces
	}
	for _, nc := range owners {
		for mode := uint32(0); mode <= 0o777; mode++ {
			d := mkObj(vfs.TypeDir, mode)
			bits := (mode >> 6) & 7
			search, read, writeSearch := bits&1 != 0, bits&4 != 0, bits&3 == 3
			cases := []struct {
				what string
				req  policy.Request
				want error
				rule string
			}{
				{"OpLookup", mkReq(policy.OpLookup, nc.c, d), verdict(search), "execute"},
				{"OpReadDir", mkReq(policy.OpReadDir, nc.c, d), verdict(read), "read"},
				{"OpAddEntry", mkReq(policy.OpAddEntry, nc.c, d), verdict(writeSearch), "write and execute"},
				{"OpRemoveEntry", mkReq(policy.OpRemoveEntry, nc.c, d), verdict(writeSearch), "write and execute"},
				{"OpRename with it as Object and a 0o700 directory as Dst", mkRename(nc.c, d, other),
					verdict(writeSearch), "write and execute"},
				{"OpRename with a 0o700 directory as Object and it as Dst", mkRename(nc.c, other, d),
					verdict(writeSearch), "write and execute"},
				{"OpRead", mkReq(policy.OpRead, nc.c, d), vfs.ErrIsDir, ""},
				{"OpWrite", mkReq(policy.OpWrite, nc.c, d), vfs.ErrIsDir, ""},
				{"OpTruncate", mkReq(policy.OpTruncate, nc.c, d), vfs.ErrIsDir, ""},
			}
			for _, tc := range cases {
				err := u.Check(tc.req)
				if err == tc.want {
					continue
				}
				if tc.rule == "" {
					l.errorf("Unix().Check(%s, by %s, of a directory %#o) = %#v (%v), want the bare vfs.ErrIsDir, "+
						"compared with ==: a directory is refused at line 1 whoever owns it (ADR 0025 §1, §6 A3)",
						tc.what, nc.name, mode, err, err)
					continue
				}
				l.errorf("Unix().Check(%s, by %s, of a directory %#o) = %#v (%v), want %v, compared with ==: a "+
					"directory's owner is judged by the owner bits, %#o, and is allowed exactly when they have %s; "+
					"no directory row gives an owner override (ADR 0025 §1, §6 A3; ADR 0014 §5)",
					tc.what, nc.name, mode, err, err, tc.want, bits, tc.rule)
			}
		}
	}
}

// oaAccessRow is one row of A4's table: a caller, a regular file's mode, the
// AccessBits pinned, and what Unix().Check answers OpRead and both of OpWrite
// and OpTruncate.
type oaAccessRow struct {
	name  string
	c     vfs.Caller
	mode  uint32
	bits  uint32
	read  error
	write error
}

// TestAccessBitsStayApartFromTheFileRows is A4 of ADR 0025 §6: AccessBits is
// built on Permits alone, with neither the owner override nor read with
// execute (§3), while the rows carry both. For each row of §6's table, of a
// regular file, AccessBits is exactly the bits given and Unix().Check answers
// the three operations as given. Then, over every mode of p2Modes() and every
// caller of p2Callers(), for a regular file: whenever AccessBits has
// vfs.AccessExecute, Unix().Check of OpRead is nil (§3: what ACCESS grants,
// the rows allow). Since ADR 0027 §2 and §10, AccessBits is called as
// AccessBits(Unix(), c, o, allAccess, true), and every assertion stands: it is
// now the pin that Request.Advisory keeps the two departures out of ACCESS.
func TestAccessBitsStayApartFromTheFileRows(t *testing.T) {
	u := policy.Unix()
	ae, ar := vfs.AccessExecute, vfs.AccessRead
	mw := vfs.AccessModify | vfs.AccessExtend
	acces := vfs.ErrAcces
	rows := []oaAccessRow{
		{"the owner", callerOwner(), 0o000, 0, nil, nil},
		{"the owner", callerOwner(), 0o444, ar, nil, nil},
		{"the owner", callerOwner(), 0o200, mw, nil, nil},
		{"the owner", callerOwner(), 0o100, ae, nil, nil},
		{"the owner, also in the group", callerOwnerInGroup(), 0o070, 0, nil, nil},
		{"a primary-group member", callerGroup(), 0o010, ae, nil, acces},
		{"a supplementary-group member", callerSupp(), 0o010, ae, nil, acces},
		{"other", callerOther(), 0o001, ae, nil, acces},
		{"other", callerOther(), 0o000, 0, acces, acces},
		{"root", callerRoot(), 0o000, ar | mw, nil, nil},
	}
	for _, r := range rows {
		o := mkObj(vfs.TypeReg, r.mode)
		if got := policy.AccessBits(policy.Unix(), r.c, o, allAccess, true); got != r.bits {
			t.Errorf("AccessBits(%s, a regular file %#o) = %#x, want exactly %#x: ACCESS reports what the mode bits "+
				"give the caller's class, with neither the owner override nor read with execute (ADR 0025 §3, §6 A4)",
				r.name, r.mode, got, r.bits)
		}
		want := map[policy.Op]error{policy.OpRead: r.read, policy.OpWrite: r.write, policy.OpTruncate: r.write}
		for _, op := range oaFileOps {
			if err := u.Check(mkReq(op, r.c, o)); err != want[op] {
				t.Errorf("Unix().Check(%s, by %s, of a regular file %#o) = %#v (%v), want %v, compared with ==: the "+
					"rows carry the owner override and read with execute, which AccessBits does not (ADR 0025 §1 to "+
					"§3, §6 A4)", opName(op), r.name, r.mode, err, err, want[op])
			}
		}
	}

	l := &limiter{t: t, limit: 20}
	defer l.done()
	for _, nc := range p2Callers() {
		for _, mode := range p2Modes() {
			o := mkObj(vfs.TypeReg, mode)
			bits := policy.AccessBits(policy.Unix(), nc.c, o, allAccess, true)
			if bits&vfs.AccessExecute == 0 {
				continue
			}
			if err := u.Check(mkReq(policy.OpRead, nc.c, o)); err != nil {
				l.errorf("AccessBits(%s, a regular file %#o) = %#x, which has vfs.AccessExecute, but Unix().Check("+
					"OpRead) = %v, want nil: every bit ACCESS grants names something the rows allow, and execute is "+
					"backed by OpRead (ADR 0025 §3, §6 A4)", nc.name, mode, bits, err)
			}
		}
	}
}
