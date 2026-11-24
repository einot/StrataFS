package policy_test

// Clean-room test for #98, "CREATE, MKDIR and SYMLINK apply a requested
// group from any caller", written without reading the implementation, from
// ADR 0028
// (docs/adr/0028-a-group-asked-for-at-creation-is-applied-only-for-root-or-a-member.md)
// sections 1, 2 and 8 (G1), ADR 0014 section 5 (Request, and the OpChgrp row
// of Unix()), and internal/vfs/vfs.go for vfs.Caller, vfs.FileType and
// vfs.Status.
//
// Covered, with the name ADR 0028 section 8 pins: G1,
// TestUnixAllowsAGroupOnAnObjectItsCallerOwnsOnlyToRootOrAMember. It is a
// characterisation of the OpChgrp row for an object its caller owns, which
// ADR 0028 does not change, so it passes before and after #98's fix. It pins
// what section 2 rests on: at creation the OpChgrp is asked of an object the
// caller owns, and for such an object the row reads the caller and the group
// asked for and nothing of the object, neither its type, its mode nor its
// group.
//
// The oracle, cgOracle, is written out from ADR 0028 section 1 and ADR 0014
// section 5's terms, and does not call policy.InGroup. Every answer is
// compared with == against nil or the bare vfs.ErrPerm, since ADR 0014
// section 5 says a refusal is never wrapped. Helpers here are prefixed cg.

import (
	"fmt"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// cgOracle is what Unix() answers an OpChgrp for group g on an object its
// caller c owns: nil exactly when c is root (UID 0), g is c's GID, or g is
// among c's GIDs, and otherwise the bare vfs.ErrPerm (ADR 0028 section 1;
// ADR 0014 section 5, OpChgrp's row).
func cgOracle(c vfs.Caller, g uint32) error {
	if c.UID == 0 || g == c.GID {
		return nil
	}
	for _, x := range c.GIDs {
		if x == g {
			return nil
		}
	}
	return vfs.ErrPerm
}

// cgStatus names an answer for failure messages: nil, vfs.ErrPerm, any other
// bare vfs.Status by its number, or anything else as it is, marked as not a
// bare status.
func cgStatus(err error) string {
	if err == nil {
		return "nil"
	}
	s, ok := err.(vfs.Status)
	if !ok {
		return fmt.Sprintf("%v (a %T, not a bare vfs.Status)", err, err)
	}
	if s == vfs.ErrPerm {
		return "vfs.ErrPerm"
	}
	return fmt.Sprintf("vfs.Status(%d)", uint32(s))
}

// cgTypeName names the three types G1 asks about.
func cgTypeName(typ vfs.FileType) string {
	switch typ {
	case vfs.TypeReg:
		return "vfs.TypeReg"
	case vfs.TypeDir:
		return "vfs.TypeDir"
	case vfs.TypeLnk:
		return "vfs.TypeLnk"
	}
	return fmt.Sprintf("vfs.FileType(%d)", typ)
}

// TestUnixAllowsAGroupOnAnObjectItsCallerOwnsOnlyToRootOrAMember is G1 of ADR
// 0028 section 8. Unix().Check of Request{Op: OpChgrp, Caller: c, Object: o,
// GID: g}, with o owned by c, is compared with cgOracle over the callers
// vfs.Caller{UID: 0}; {UID: 0, GID: 0, GIDs: [5]}; {UID: 502, GID: 30};
// {UID: 504, GID: 30, GIDs: [40, 41]}; and {UID: 505, GID: 30, GIDs: [100 to
// 115]}, sixteen of them; as o, each of the types vfs.TypeReg, vfs.TypeDir
// and vfs.TypeLnk, each of the modes 0, 0o644, 0o755, 0o2755, 0o6755 and
// 0o7777, and each GID of c.GID, 20 and 41, with UID c.UID; and as g, each of
// 0, 5, 20, 30, 40, 41, 100, 115, 116, 999 and 4294967295.
func TestUnixAllowsAGroupOnAnObjectItsCallerOwnsOnlyToRootOrAMember(t *testing.T) {
	const where = "G1, ADR 0028 sections 1, 2 and 8; ADR 0014 section 5, OpChgrp's row"

	sixteen := make([]uint32, 0, 16)
	for g := uint32(100); g <= 115; g++ {
		sixteen = append(sixteen, g)
	}
	callers := []struct {
		name string
		c    vfs.Caller
	}{
		{"vfs.Caller{UID: 0}", vfs.Caller{UID: 0}},
		{"vfs.Caller{UID: 0, GID: 0, GIDs: [5]}", vfs.Caller{UID: 0, GID: 0, GIDs: []uint32{5}}},
		{"vfs.Caller{UID: 502, GID: 30}", vfs.Caller{UID: 502, GID: 30}},
		{"vfs.Caller{UID: 504, GID: 30, GIDs: [40, 41]}", vfs.Caller{UID: 504, GID: 30, GIDs: []uint32{40, 41}}},
		{"vfs.Caller{UID: 505, GID: 30, GIDs: [100 to 115]}", vfs.Caller{UID: 505, GID: 30, GIDs: sixteen}},
	}
	types := []vfs.FileType{vfs.TypeReg, vfs.TypeDir, vfs.TypeLnk}
	modes := []uint32{0, 0o644, 0o755, 0o2755, 0o6755, 0o7777}
	groups := []uint32{0, 5, 20, 30, 40, 41, 100, 115, 116, 999, 4294967295}

	u := policy.Unix()
	asked, allowed, refused := 0, 0, 0
	for _, cc := range callers {
		for _, typ := range types {
			for _, mode := range modes {
				for _, ogid := range []uint32{cc.c.GID, 20, 41} {
					o := policy.Object{Type: typ, Mode: mode, UID: cc.c.UID, GID: ogid}
					for _, g := range groups {
						want := cgOracle(cc.c, g)
						got := u.Check(policy.Request{Op: policy.OpChgrp, Caller: cc.c, Object: o, GID: g})
						asked++
						if want == nil {
							allowed++
						} else {
							refused++
						}
						if got != want {
							t.Errorf("Unix().Check(OpChgrp, Caller %s, Object {Type %s, Mode %#o, UID %d, GID %d}, "+
								"GID %d) = %s, want %s, compared with ==: for an object its caller owns, the row "+
								"allows a group exactly to root and to a caller in it, the group being its GID or "+
								"among its GIDs, reads nothing else of the object, and refuses with the bare "+
								"vfs.ErrPerm (%s)", cc.name, cgTypeName(typ), mode, o.UID, o.GID, g, cgStatus(got),
								cgStatus(want), where)
						}
					}
				}
			}
		}
	}
	if want := len(callers) * len(types) * len(modes) * 3 * len(groups); asked != want {
		t.Errorf("test bug: G1 asked %d requests, want %d, every caller, type, mode, object group and group "+
			"asked for (%s)", asked, want, where)
	}
	if allowed == 0 || refused == 0 {
		t.Errorf("test bug: the oracle allowed %d and refused %d of G1's requests, want some of each (%s)", allowed,
			refused, where)
	}
}
