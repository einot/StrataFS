package blobfs

// Clean-room tests for #97, "ACCESS grants what the operations then refuse",
// written without reading the implementation, from ADR 0027
// (docs/adr/0027-access-grants-only-what-the-operations-allow.md) sections 1
// (the rule and its tables), 3 to 5, 7 (how blobfs asks), 8 (the property)
// and 10 (X8 to X10, the harness, the callers and the facts), ADR 0014
// sections 5 and 11, ADR 0025 section 6, ADR 0026 section 8, and
// internal/vfs/vfs.go.
//
// ADR 0027 section 1, through the FS: Access grants a bit only if, for a bit
// that names a change (MODIFY, EXTEND, DELETE), the mount takes changes, and
// the installed policy allows every request the bit names, which are the
// operations' own. Under Unix() a directory is granted READ with read, LOOKUP
// with search, and MODIFY, EXTEND and DELETE with write and search; anything
// else READ with read, MODIFY and EXTEND with write, and EXECUTE with execute.
// A regular file's bits are the mode's, with neither the owner override nor
// the read that execute gives (section 3), and in a sticky directory DELETE
// and MODIFY are granted by the directory's mode (section 5).
//
// Covered, with the names ADR 0027 section 10 pins:
//   - X8, TestAccessReportsWhatTheOperationsAllow;
//   - X9, TestAccessGrantsNoChangeOnAMountThatTakesNone;
//   - X10, TestNoAccessCheckRemainsOutsideThePolicy.
//
// X11 is in access_wiring_test.go, which names policy.OpExecute and
// policy.Request's Advisory. This file names neither, nor the new
// policy.AccessBits, and uses no helper that ADR 0027's PR adds to another
// test file, so that it builds on 4ded55e's tree and carries the red run
// (ADR 0027 section 10). Its helpers are prefixed ac.
//
// The safety rules (ADR 0027 section 10; ADR 0014 section 11; ADR 0011
// section 7): every bucket is a *store.Local under the test's t.TempDir(),
// behind an unarmed uc43Store, through uc43Setup; every mount is made with
// uc43Config, so SnapshotRetention and MaxDirtyBytes are -1, through e.newFS,
// e.newFSWith or sp24Mount; every New and every FS call runs in uc43Go and is
// waited for under uc43Bound; nothing sleeps, polls or runs in parallel.
// FS.perm is written only by X10, through sp24Mount, once, right after New
// returns, as ADR 0014 section 11's W1 allows; X8 and X9 leave Unix() in
// place. Of unexported non-test names this file uses mountDiverged, through
// uc43Env.mode, and FS.perm, through sp24Mount. Under X10's policy, which
// allows more than Unix(), every call but Access is a Create or a Mkdir in
// the root directory; Access may be asked of any object under any policy (ADR
// 0027 section 7).
//
// The callers are testCaller, vfs.Caller{UID: 501, GID: 20}, who owns the
// root directory and what it makes (uc43Caller, which uc43Env's methods use,
// is the same identity); pwMember, vfs.Caller{UID: 503, GID: 20}, in the
// group; sa24Other, vfs.Caller{UID: 502, GID: 30}, neither; and sp24Root,
// vfs.Caller{UID: 0}. Every refusal is compared with == against the bare
// vfs.Status.
//
// Not relied on: U3 where Permits allows, and U6, which ADR 0027 section 10
// leaves unpinned. No Read, Write or size is asked of a symlink, and no time
// is set.

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// acCallers are X8's callers, in the order of its tables' columns.
var acCallers = []vfs.Caller{testCaller, pwMember, sa24Other, sp24Root}

// acGateCallers are X9's callers.
var acGateCallers = []vfs.Caller{testCaller, sa24Other, sp24Root}

// acMask spells a mask by the letters ADR 0027 section 10 uses: R, L, M, E,
// D and X for vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify,
// vfs.AccessExtend, vfs.AccessDelete and vfs.AccessExecute.
func acMask(m uint32) string {
	if m == 0 {
		return "nothing (0)"
	}
	bits := []uint32{vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute}
	letters := []string{"R", "L", "M", "E", "D", "X"}
	var parts []string
	for i, b := range bits {
		if m&b != 0 {
			parts = append(parts, letters[i])
		}
	}
	if rest := m &^ pwAllAccess; rest != 0 {
		parts = append(parts, fmt.Sprintf("%#x", rest))
	}
	return fmt.Sprintf("%s (%#x)", strings.Join(parts, " "), m)
}

// acAsked spells the mask a call asks for.
func acAsked(want uint32) string {
	if want == pwAllAccess {
		return "every bit"
	}
	return acMask(want)
}

// acVerdict is nil if allowed, and vfs.ErrAcces otherwise.
func acVerdict(allowed bool) error {
	if allowed {
		return nil
	}
	return vfs.ErrAcces
}

// acWantAccess requires c's Access of h, asking for want, to return mask and
// nil.
func acWantAccess(e *uc43Env, fs *FS, c vfs.Caller, h vfs.Handle, name string, want, mask uint32, why,
	where string) {
	e.t.Helper()
	what := fmt.Sprintf("%s's Access(%s, %s)", pwWho(c), name, acAsked(want))
	got := pwAccess(e, fs, c, h, want, what, where)
	if got.err != nil || got.v != mask {
		e.t.Errorf("%s = (%s, %s), want (%s, nil): %s (%s)", what, acMask(got.v), oaStatus(got.err), acMask(mask),
			why, where)
	}
}

// acObject is one object of X8's: its name, its mode, the mask each of
// acCallers is granted with every bit asked, and its handle.
type acObject struct {
	name  string
	mode  uint32
	masks [4]uint32
	h     vfs.Handle
}

// TestAccessReportsWhatTheOperationsAllow is X8 of ADR 0027 section 10
// (sections 1, 3, 5 and 8), under Unix(). testCaller makes, in the root, each
// directory below with the mode given and a file k in it, and each file below
// with no Mode, 20 bytes written, and then its mode set, so that the setup
// rests on no override; d555 and d000 are made 0o755, filled, and then set.
// Access with every bit asked gives, for testCaller, pwMember, sa24Other and
// sp24Root in that order:
//
//	d755  0o755   R L M E D | R L       | R L       | R L M E D
//	d744  0o744   R L M E D | R         | R         | R L M E D
//	d722  0o722   R L M E D | nothing   | nothing   | R L M E D
//	d766  0o766   R L M E D | R         | R         | R L M E D
//	d733  0o733   R L M E D | L M E D   | L M E D   | R L M E D
//	d711  0o711   R L M E D | L         | L         | R L M E D
//	d777  0o777   R L M E D | R L M E D | R L M E D | R L M E D
//	d700  0o700   R L M E D | nothing   | nothing   | R L M E D
//	d750  0o750   R L M E D | R L       | nothing   | R L M E D
//	d705  0o705   R L M E D | nothing   | R L       | R L M E D
//	t1777 0o1777  R L M E D | R L M E D | R L M E D | R L M E D
//	d555  0o555   R L       | R L       | R L       | R L M E D
//	d000  0       nothing   | nothing   | nothing   | R L M E D
//	f644  0o644   R M E     | R         | R         | R M E
//	f600  0o600   R M E     | nothing   | nothing   | R M E
//	f755  0o755   R M E X   | R X       | R X       | R M E X
//	f711  0o711   R M E X   | X         | X         | R M E X
//	f444  0o444   R         | R         | R         | R M E
//	f000  0       nothing   | nothing   | nothing   | R M E
//	f070  0o070   nothing   | R M E X   | nothing   | R M E X
//
// And for each cell the operations agree, as the cell, not Access's answer,
// says:
//   - a directory's R granted: the caller's ReadDir returns nil; not granted,
//     vfs.ErrAcces. Its L: Lookup of k returns nil, or vfs.ErrAcces;
//   - a directory's E, M and D granted: the caller's Create of a fresh name
//     returns nil, its Rename of that name to another within the directory
//     returns nil, and its Remove of that returns nil. Not granted: its Create
//     of a fresh name, its Rename of k within the directory and its Remove of
//     k each return vfs.ErrAcces;
//   - in t1777, for pwMember and sa24Other, who are granted D and M: after the
//     three calls above on a name of their own, Remove of k and Rename of k
//     within the directory return vfs.ErrPerm (section 5);
//   - a file's R or X granted: the caller's Read returns nil. Its M: a Write of
//     one byte at 0 and a SetAttr of the size it has return nil. Its E: a
//     Write of one byte at its end returns nil;
//   - the two departures, seen once each: testCaller, granted no M on f444,
//     writes it; and sa24Other, granted no R on f711, reads it (section 3).
//
// And sa24Other's Access of d777, asked one bit at a time, returns that bit
// for each of R, L, M, E and D, and 0 for X.
func TestAccessReportsWhatTheOperationsAllow(t *testing.T) {
	const where = "X8, ADR 0027 sections 1, 3, 5, 8 and 10"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	tc := testCaller

	R, L, M, E, D, X := vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute
	all := R | L | M | E | D
	rme, rmex := R|M|E, R|M|E|X
	med := M | E | D
	rx := R | X

	dirs := []acObject{
		{name: "d755", mode: 0o755, masks: [4]uint32{all, R | L, R | L, all}},
		{name: "d744", mode: 0o744, masks: [4]uint32{all, R, R, all}},
		{name: "d722", mode: 0o722, masks: [4]uint32{all, 0, 0, all}},
		{name: "d766", mode: 0o766, masks: [4]uint32{all, R, R, all}},
		{name: "d733", mode: 0o733, masks: [4]uint32{all, L | M | E | D, L | M | E | D, all}},
		{name: "d711", mode: 0o711, masks: [4]uint32{all, L, L, all}},
		{name: "d777", mode: 0o777, masks: [4]uint32{all, all, all, all}},
		{name: "d700", mode: 0o700, masks: [4]uint32{all, 0, 0, all}},
		{name: "d750", mode: 0o750, masks: [4]uint32{all, R | L, 0, all}},
		{name: "d705", mode: 0o705, masks: [4]uint32{all, 0, R | L, all}},
		{name: "t1777", mode: 0o1777, masks: [4]uint32{all, all, all, all}},
		{name: "d555", mode: 0o555, masks: [4]uint32{R | L, R | L, R | L, all}},
		{name: "d000", mode: 0, masks: [4]uint32{0, 0, 0, all}},
	}
	files := []acObject{
		{name: "f644", mode: 0o644, masks: [4]uint32{rme, R, R, rme}},
		{name: "f600", mode: 0o600, masks: [4]uint32{rme, 0, 0, rme}},
		{name: "f755", mode: 0o755, masks: [4]uint32{rmex, R | X, R | X, rmex}},
		{name: "f711", mode: 0o711, masks: [4]uint32{rmex, X, X, rmex}},
		{name: "f444", mode: 0o444, masks: [4]uint32{R, R, R, rme}},
		{name: "f000", mode: 0, masks: [4]uint32{0, 0, 0, rme}},
		{name: "f070", mode: 0o070, masks: [4]uint32{0, rmex, 0, rmex}},
	}

	// Setup, as testCaller, whose owner bits allow each step.
	for i := range dirs {
		d := &dirs[i]
		made := d.mode
		if d.name == "d555" || d.name == "d000" {
			made = 0o755
		}
		what := fmt.Sprintf("setup: testCaller's Mkdir(%q, {Mode %#o})", d.name, made)
		d.h = pwMust(e, pwMkdir(e, fs, tc, root, d.name, oaMode(made), what, where), what, where)
		pwSetupObject(e, fs, d.h, d.name, policy.Object{Type: vfs.TypeDir, Mode: made, UID: 501, GID: 20}, -1, where)
		e.create(fs, d.h, "k", where)
		if made != d.mode {
			oaChmod(e, fs, d.h, d.name, d.mode, where)
		}
	}
	for i := range files {
		f := &files[i]
		f.h = e.create(fs, root, f.name, where)
		e.write(fs, f.h, 0, pwBytes(20, byte(31+i)), fmt.Sprintf("setup: testCaller's Write of 20 bytes at 0 of %q",
			f.name), where)
		oaChmod(e, fs, f.h, f.name, f.mode, where)
	}
	handle := func(objs []acObject, name string) vfs.Handle {
		t.Helper()
		for _, o := range objs {
			if o.name == name {
				return o.h
			}
		}
		t.Fatalf("test bug: no object %q (%s)", name, where)
		return nil
	}

	const agree = "what Access grants, the operation it names allows, and on a directory what it withholds, the " +
		"operation refuses (ADR 0027 section 8, S2 and S3; section 10, X8)"
	const sticky = "in a sticky directory DELETE and MODIFY are granted by the directory's mode, and say that the " +
		"caller may remove or rename an entry it owns; another's entry is refused vfs.ErrPerm (ADR 0027 section 5; " +
		"ADR 0026 section 1)"

	for _, d := range dirs {
		for i, c := range acCallers {
			who, want := pwWho(c), d.masks[i]
			acWantAccess(e, fs, c, d.h, d.name, pwAllAccess, want, fmt.Sprintf("under Unix() a directory of mode %#o "+
				"grants R with read, L with search, and M, E and D with write and search (ADR 0027 sections 1 and "+
				"5; section 10, X8)", d.mode), where)

			what := fmt.Sprintf("%s's ReadDir(%s, 0, zero verifier, 4096, plus false)", who, d.name)
			oaWant(t, what, pwReadDir(e, fs, c, d.h, false, what, where).err, acVerdict(want&R != 0), agree, where)
			what = fmt.Sprintf("%s's Lookup(%s, \"k\")", who, d.name)
			oaWant(t, what, pwLookup(e, fs, c, d.h, "k", what, where).err, acVerdict(want&L != 0), agree, where)

			fresh, moved := fmt.Sprintf("n%d", c.UID), fmt.Sprintf("m%d", c.UID)
			switch want & med {
			case med:
				what = fmt.Sprintf("%s's Create(%s, %q, vfs.SetAttr{}, exclusive)", who, d.name, fresh)
				oaWant(t, what, pwCreate(e, fs, c, d.h, fresh, vfs.SetAttr{}, true, what, where).err, nil, agree, where)
				what = fmt.Sprintf("%s's Rename(%s, %q, %s, %q)", who, d.name, fresh, d.name, moved)
				oaWant(t, what, pwRename(e, fs, c, d.h, fresh, d.h, moved, what, where), nil, agree, where)
				what = fmt.Sprintf("%s's Remove(%s, %q)", who, d.name, moved)
				oaWant(t, what, pwRemove(e, fs, c, d.h, moved, what, where), nil, agree, where)
			case 0:
				what = fmt.Sprintf("%s's Create(%s, %q, vfs.SetAttr{}, exclusive)", who, d.name, fresh)
				oaWant(t, what, pwCreate(e, fs, c, d.h, fresh, vfs.SetAttr{}, true, what, where).err, vfs.ErrAcces,
					agree, where)
				what = fmt.Sprintf("%s's Rename(%s, \"k\", %s, \"k2\")", who, d.name, d.name)
				oaWant(t, what, pwRename(e, fs, c, d.h, "k", d.h, "k2", what, where), vfs.ErrAcces, agree, where)
				what = fmt.Sprintf("%s's Remove(%s, \"k\")", who, d.name)
				oaWant(t, what, pwRemove(e, fs, c, d.h, "k", what, where), vfs.ErrAcces, agree, where)
			}

			if d.mode&0o1000 == 0 || (i != 1 && i != 2) {
				continue
			}
			what = fmt.Sprintf("%s's Remove(%s, \"k\"), testCaller's entry", who, d.name)
			oaWant(t, what, pwRemove(e, fs, c, d.h, "k", what, where), vfs.ErrPerm, sticky, where)
			what = fmt.Sprintf("%s's Rename(%s, \"k\", %s, \"k2\"), testCaller's entry", who, d.name, d.name)
			oaWant(t, what, pwRename(e, fs, c, d.h, "k", d.h, "k2", what, where), vfs.ErrPerm, sticky, where)
		}
	}

	for _, f := range files {
		for i, c := range acCallers {
			who, want := pwWho(c), f.masks[i]
			acWantAccess(e, fs, c, f.h, f.name, pwAllAccess, want, fmt.Sprintf("under Unix() a regular file of mode "+
				"%#o grants R with read, M and E with write, and X with execute, by the mode alone, with neither "+
				"the owner override nor the read that execute gives (ADR 0027 sections 1 and 3; section 10, X8)",
				f.mode), where)

			if want&rx != 0 {
				what := fmt.Sprintf("%s's Read(%s, 0, 10)", who, f.name)
				oaWant(t, what, pwRead(e, fs, c, f.h, 0, 10, what, where).err, nil, agree, where)
			}
			if want&M != 0 {
				what := fmt.Sprintf("%s's Write(%s, 0, 1 byte, vfs.Unstable)", who, f.name)
				oaWant(t, what, pwWrite(e, fs, c, f.h, 0, []byte{byte(0x40 + i)}, what, where).err, nil, agree, where)
				size := sa24GetAttr(e, fs, f.h, fmt.Sprintf("GetAttr of %q before %s's SetAttr", f.name, who),
					where).Size
				what = fmt.Sprintf("%s's SetAttr(%s, {Size: %d}), the size it has", who, f.name, size)
				oaWant(t, what, sa24SetAttr(e, fs, c, f.h, oaSize(size), what, where).err, nil, agree, where)
			}
			if want&E != 0 {
				size := sa24GetAttr(e, fs, f.h, fmt.Sprintf("GetAttr of %q before %s's Write at its end", f.name, who),
					where).Size
				what := fmt.Sprintf("%s's Write(%s, %d, 1 byte, vfs.Unstable), at its end", who, f.name, size)
				oaWant(t, what, pwWrite(e, fs, c, f.h, size, []byte{byte(0x60 + i)}, what, where).err, nil, agree,
					where)
			}
		}
	}

	what := "testCaller's Write(f444, 0, 1 byte, vfs.Unstable), granted no M"
	oaWant(t, what, pwWrite(e, fs, tc, handle(files, "f444"), 0, []byte{0x7e}, what, where).err, nil,
		"the operations allow more than ACCESS reports on a regular file: its owner writes it whatever its mode "+
			"(ADR 0027 section 3; ADR 0025 section 1)", where)
	what = "sa24Other's Read(f711, 0, 10), granted no R"
	oaWant(t, what, pwRead(e, fs, sa24Other, handle(files, "f711"), 0, 10, what, where).err, nil,
		"the operations allow more than ACCESS reports on a regular file: a caller whose class has execute reads "+
			"it (ADR 0027 section 3; ADR 0025 section 2)", where)

	d777 := handle(dirs, "d777")
	for _, bit := range []uint32{R, L, M, E, D, X} {
		want := bit
		if bit == X {
			want = 0
		}
		acWantAccess(e, fs, sa24Other, d777, "d777", bit, want, "Access answers each bit asked for alone as it "+
			"does among the rest, and never grants a directory EXECUTE (ADR 0027 sections 1 and 2; section 10, X8)",
			where)
	}
}

// acGateSetup is X9's setup, on a writable mount: testCaller makes d, a
// directory 0o777 holding k; f, a file 0o666 with 20 bytes; and x, a file
// 0o777; and Syncs. On it Access with every bit gives each of testCaller,
// sa24Other and sp24Root R L M E D on d, R M E on f and R M E X on x. It
// returns the harness and the mount.
func acGateSetup(t *testing.T, where string) (*uc43Env, *FS) {
	t.Helper()
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	tc := testCaller

	what := `setup: testCaller's Mkdir("d", {Mode 0o777})`
	d := pwMust(e, pwMkdir(e, fs, tc, root, "d", oaMode(0o777), what, where), what, where)
	pwSetupObject(e, fs, d, "d", policy.Object{Type: vfs.TypeDir, Mode: 0o777, UID: 501, GID: 20}, -1, where)
	e.create(fs, d, "k", where)
	what = `setup: testCaller's Create("f", {Mode 0o666}, exclusive)`
	f := pwMust(e, pwCreate(e, fs, tc, root, "f", oaMode(0o666), true, what, where), what, where)
	e.write(fs, f, 0, pwBytes(20, 61), `setup: testCaller's Write of 20 bytes at 0 of "f"`, where)
	pwSetupObject(e, fs, f, "f", policy.Object{Type: vfs.TypeReg, Mode: 0o666, UID: 501, GID: 20}, 20, where)
	what = `setup: testCaller's Create("x", {Mode 0o777}, exclusive)`
	x := pwMust(e, pwCreate(e, fs, tc, root, "x", oaMode(0o777), true, what, where), what, where)
	pwSetupObject(e, fs, x, "x", policy.Object{Type: vfs.TypeReg, Mode: 0o777, UID: 501, GID: 20}, 0, where)
	if err := e.sync(fs, "setup: testCaller's Sync", where); err != nil {
		t.Fatalf("setup: Sync = %v, want nil against a healthy store (%s)", err, where)
	}

	R, L, M, E, D, X := vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute
	const why = "on a writable mount, the mode gives each of these callers every bit the object's type names (ADR " +
		"0027 sections 1 and 4; section 10, X9)"
	for _, c := range acGateCallers {
		acWantAccess(e, fs, c, d, "d", pwAllAccess, R|L|M|E|D, why, where)
		acWantAccess(e, fs, c, f, "f", pwAllAccess, R|M|E, why, where)
		acWantAccess(e, fs, c, x, "x", pwAllAccess, R|M|E|X, why, where)
	}
	return e, fs
}

// acGateHandles looks up d, f and x in fs's root directory, as uc43Caller,
// and returns their handles.
func acGateHandles(e *uc43Env, fs *FS, where string) (d, f, x vfs.Handle) {
	e.t.Helper()
	root := e.root(fs, where)
	ld := e.lookup(fs, root, "d", where)
	lf := e.lookup(fs, root, "f", where)
	lx := e.lookup(fs, root, "x", where)
	if ld.err != nil || lf.err != nil || lx.err != nil {
		e.t.Fatalf("Lookup of \"d\", \"f\" and \"x\" = %s, %s and %s, want nil for each: the setup's Sync committed "+
			"them (%s)", oaStatus(ld.err), oaStatus(lf.err), oaStatus(lx.err), where)
	}
	return ld.h, lf.h, lx.h
}

// acGateCheck is X9's check of a mount that takes no change, whose changes
// answer refusal: for each of testCaller, sa24Other and sp24Root, Access with
// every bit gives R L on d, R on f and R X on x, and asked for M, E and D
// alone gives 0 on each; Create in d, Remove of k and Write of f return
// refusal; and Lookup of k, ReadDir of d and Read of f return nil.
func acGateCheck(e *uc43Env, fs *FS, d, f, x vfs.Handle, refusal error, mount, where string) {
	e.t.Helper()
	t := e.t
	R, L, M, E, D, X := vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute
	why := fmt.Sprintf("%s takes no change, so Access grants no M, E or D, whoever asks, and READ, LOOKUP and "+
		"EXECUTE as the policy alone says (ADR 0027 section 4; section 10, X9)", mount)
	gate := fmt.Sprintf("%s takes no change: every change calls mutable() first, before the handle or the row, "+
		"for every caller, root included (ADR 0027 section 4; ADR 0014 section 7)", mount)
	reads := fmt.Sprintf("Read, Lookup and ReadDir have no mount gate, so %s still serves them (ADR 0027 section "+
		"4)", mount)
	for _, c := range acGateCallers {
		who := pwWho(c)
		acWantAccess(e, fs, c, d, "d on "+mount, pwAllAccess, R|L, why, where)
		acWantAccess(e, fs, c, f, "f on "+mount, pwAllAccess, R, why, where)
		acWantAccess(e, fs, c, x, "x on "+mount, pwAllAccess, R|X, why, where)
		acWantAccess(e, fs, c, d, "d on "+mount, M|E|D, 0, why, where)
		acWantAccess(e, fs, c, f, "f on "+mount, M|E|D, 0, why, where)
		acWantAccess(e, fs, c, x, "x on "+mount, M|E|D, 0, why, where)

		fresh := fmt.Sprintf("n%d", c.UID)
		what := fmt.Sprintf("%s's Create(d, %q, vfs.SetAttr{}, exclusive) on %s", who, fresh, mount)
		oaWant(t, what, pwCreate(e, fs, c, d, fresh, vfs.SetAttr{}, true, what, where).err, refusal, gate, where)
		what = fmt.Sprintf("%s's Remove(d, \"k\") on %s", who, mount)
		oaWant(t, what, pwRemove(e, fs, c, d, "k", what, where), refusal, gate, where)
		what = fmt.Sprintf("%s's Write(f, 0, 1 byte, vfs.Unstable) on %s", who, mount)
		oaWant(t, what, pwWrite(e, fs, c, f, 0, []byte{0x2a}, what, where).err, refusal, gate, where)

		what = fmt.Sprintf("%s's Lookup(d, \"k\") on %s", who, mount)
		oaWant(t, what, pwLookup(e, fs, c, d, "k", what, where).err, nil, reads, where)
		what = fmt.Sprintf("%s's ReadDir(d, 0, zero verifier, 4096, plus false) on %s", who, mount)
		oaWant(t, what, pwReadDir(e, fs, c, d, false, what, where).err, nil, reads, where)
		what = fmt.Sprintf("%s's Read(f, 0, 10) on %s", who, mount)
		oaWant(t, what, pwRead(e, fs, c, f, 0, 10, what, where).err, nil, reads, where)
	}
}

// TestAccessGrantsNoChangeOnAMountThatTakesNone is X9 of ADR 0027 section 10
// (section 4), in two subtests, each from acGateSetup.
//
// Read-only: a second mount of the same bucket is made with Config.ReadOnly
// set, and the first is not used again. On it, for each of testCaller,
// sa24Other and sp24Root, Access with every bit gives R L on d, R on f and R X
// on x, and asked for M, E and D alone gives 0; Create in d, Remove of k and
// Write of f return vfs.ErrROFS; and Lookup of k, ReadDir of d and Read of f
// return nil.
//
// Diverged: as ADR 0025 section 6's A9 diverges a mount, a foreign root
// pointer is written on the inner store with uc43WriteForeignRoot at the
// mount's epoch plus one; the commit of c2 returns an error whose text
// contains "commit conflict"; and f.mode.State() is mountDiverged. Then the
// same masks, and Create in d, Remove of k and Write of f return
// vfs.ErrStale, with the three reads returning nil.
func TestAccessGrantsNoChangeOnAMountThatTakesNone(t *testing.T) {
	t.Run("read-only", func(t *testing.T) {
		const where = "X9, read-only, ADR 0027 sections 4 and 10; ADR 0011 section 4"
		e, _ := acGateSetup(t, where)
		cfg := uc43Config(e.h)
		cfg.ReadOnly = true
		rofs := e.newFSWith(cfg, where)
		d, f, x := acGateHandles(e, rofs, where)
		acGateCheck(e, rofs, d, f, x, vfs.ErrROFS, "the read-only mount", where)
	})

	t.Run("diverged", func(t *testing.T) {
		const where = "X9, diverged, ADR 0027 sections 4 and 10; ADR 0025 section 6, A9's recipe"
		e, fs := acGateSetup(t, where)
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
		d, f, x := acGateHandles(e, fs, where)
		acGateCheck(e, fs, d, f, x, vfs.ErrStale, "the diverged mount", where)
	})
}

// acSwitch is X10's policy, of the test's own: it allows every request until
// refuse is called, and from then on refuses every request with vfs.ErrNXIO.
// Its state is held under its own lock.
type acSwitch struct {
	mu      sync.Mutex
	refused bool
}

func (s *acSwitch) check(policy.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refused {
		return vfs.ErrNXIO
	}
	return nil
}

// refuse switches s to refuse everything.
func (s *acSwitch) refuse() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused = true
}

// TestNoAccessCheckRemainsOutsideThePolicy is X10 of ADR 0027 section 10
// (section 7): Access asks f.perm, and keeps no check of its own. sp24Mount
// installs acSwitch, allowing. testCaller makes y, a regular file of mode
// 0o600, and p, a directory of mode 0o700. Allowing: sa24Other's Access with
// every bit gives R M E X on y and R L M E D on p, which the mode bits would
// not. Then acSwitch refuses everything with vfs.ErrNXIO, and sp24Root's and
// testCaller's Access with every bit give 0 on both, which the mode bits would
// not either, each with a nil error.
func TestNoAccessCheckRemainsOutsideThePolicy(t *testing.T) {
	const where = "X10, ADR 0027 sections 7 and 10"
	e := uc43Setup(t)
	sw := &acSwitch{}
	fs, root := sp24Mount(e, policy.Func(sw.check), where)
	tc := testCaller

	what := `setup: testCaller's Create("y", {Mode 0o600}, exclusive)`
	y := pwMust(e, pwCreate(e, fs, tc, root, "y", oaMode(0o600), true, what, where), what, where)
	pwSetupObject(e, fs, y, "y", policy.Object{Type: vfs.TypeReg, Mode: 0o600, UID: 501, GID: 20}, 0, where)
	what = `setup: testCaller's Mkdir("p", {Mode 0o700})`
	p := pwMust(e, pwMkdir(e, fs, tc, root, "p", oaMode(0o700), what, where), what, where)
	pwSetupObject(e, fs, p, "p", policy.Object{Type: vfs.TypeDir, Mode: 0o700, UID: 501, GID: 20}, -1, where)

	R, L, M, E, D, X := vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute
	const allowing = "with f.perm allowing everything, Access grants every bit the object's type names, whatever " +
		"the mode: no check of Access's own remains outside the policy (ADR 0027 section 7; section 10, X10)"
	acWantAccess(e, fs, sa24Other, y, "y", pwAllAccess, R|M|E|X, allowing, where)
	acWantAccess(e, fs, sa24Other, p, "p", pwAllAccess, R|L|M|E|D, allowing, where)

	sw.refuse()
	const refusing = "with f.perm refusing everything, Access grants nothing, even to root and to the owner, and " +
		"returns no error: a policy's refusal withholds a bit and fails nothing (ADR 0027 sections 2 and 7; " +
		"section 10, X10)"
	for _, c := range []vfs.Caller{sp24Root, tc} {
		acWantAccess(e, fs, c, y, "y", pwAllAccess, 0, refusing, where)
		acWantAccess(e, fs, c, p, "p", pwAllAccess, 0, refusing, where)
	}
}
