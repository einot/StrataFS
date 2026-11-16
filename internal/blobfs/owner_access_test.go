package blobfs

// Clean-room tests for #93, written without reading the implementation, from
// ADR 0025 (docs/adr/0025-a-files-owner-may-read-and-write-it-whatever-its-mode.md)
// sections 1, 2, 3 and 6, ADR 0014 sections 5 and 11, internal/vfs/vfs.go, and,
// for the safety rules, ADR 0025 section 6 and the headers of the existing test
// files policy_wiring_test.go and unlocked_commit_test.go.
//
// ADR 0025 section 1's rows, through the FS: the owner of a regular file
// (Caller.UID == the inode's UID) may Read, Write and truncate it whatever its
// mode; section 2: a caller whose one class of the mode has execute may Read a
// regular file; section 3: Access still reports the mode bits alone. A
// directory's owner gets no override, and the mount gate comes before the
// rows.
//
// Covered, with the names ADR 0025 section 6 pins:
//   - A5, TestTheOwnerReadsWritesAndTruncatesItsFileWhateverItsMode;
//   - A6, TestACallerWithExecutePermissionReadsARegularFile;
//   - A7, TestAccessReportsTheModeWhateverTheOperationsAllow;
//   - A8, TestADirectorysOwnerGetsNoOverride, a characterisation that passes
//     before the change and after;
//   - A9, TestTheMountGateComesBeforeTheOwnerOverride.
//
// The safety rules (ADR 0025 section 6; ADR 0014 section 11; as
// policy_wiring_test.go's and unlocked_commit_test.go's headers give them):
// every bucket is a *store.Local under the test's t.TempDir(), behind an
// unarmed uc43Store, through uc43Setup; every mount is
// made with uc43Config, so SnapshotRetention and MaxDirtyBytes are -1, through
// e.newFS, e.newFSWith or e.remount; every New and every FS call runs in uc43Go
// and is waited for under uc43Bound; nothing sleeps, polls or runs in
// parallel. Unix() is left in place on every mount: f.perm is never replaced,
// and no FS field is written. The only FS fields read are those uc43Env.read
// reads, and f.mode.State() through uc43Env.mode, which is how mountDiverged,
// the one unexported non-test name used here, is reached.
//
// The callers are testCaller, vfs.Caller{UID: 501, GID: 20}, who owns the root
// directory and everything it creates (uc43Caller, which uc43Env's methods
// use, is the same identity); pwMember, vfs.Caller{UID: 503, GID: 20}, in the
// group; sa24Other, vfs.Caller{UID: 502, GID: 30}, neither; sp24Root,
// vfs.Caller{UID: 0}; and oaSupp, in the group through a supplementary gid
// alone. Every refusal is compared with == against the bare vfs.Status. A
// Mode given to Create is applied as given, so Mode 0 makes a file of mode 0
// (ADR 0014 section 11, the facts).
//
// Not covered, on purpose: ADR 0014 section 11's U3 where Permits allows, U4,
// U5 and U6. No directory here has 0o1000 set; Access is asked of no
// directory with read but not search or write but not search; no time is set;
// and no Read, Write or size is asked of a symlink.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// oaSupp is ADR 0025 section 6's caller in the group of testCaller's files
// through a supplementary gid alone.
var oaSupp = vfs.Caller{UID: 504, GID: 40, GIDs: []uint32{20}}

// oaMode is a SetAttr that sets the mode alone.
func oaMode(m uint32) vfs.SetAttr { return vfs.SetAttr{Mode: sa24U32(m)} }

// oaSize is a SetAttr that sets the size alone.
func oaSize(n uint64) vfs.SetAttr { return vfs.SetAttr{Size: sa24U64(n)} }

// oaStatus names what a call returned, as pwStatus does, naming vfs.ErrROFS
// too.
func oaStatus(err error) string {
	if s, ok := err.(vfs.Status); ok && s == vfs.ErrROFS {
		return "vfs.ErrROFS"
	}
	return pwStatus(err)
}

// oaWant requires err to be want, compared with ==.
func oaWant(t *testing.T, what string, err, want error, why, where string) {
	t.Helper()
	if err == want {
		return
	}
	if want == nil {
		t.Errorf("%s = %s, want nil: %s (%s)", what, oaStatus(err), why, where)
		return
	}
	t.Errorf("%s = %s, want %s, compared with == against the bare vfs.Status: %s (%s)", what, oaStatus(err),
		oaStatus(want), why, where)
}

// oaCreate is testCaller's exclusive Create of the regular file name in dir
// with Mode mode, which must succeed, made as asked: of that mode, owned by
// 501:20, and empty.
func oaCreate(e *uc43Env, fs *FS, dir vfs.Handle, name string, mode uint32, where string) vfs.Handle {
	e.t.Helper()
	what := fmt.Sprintf("setup: testCaller's Create(%q, {Mode %#o}, exclusive)", name, mode)
	h := pwMust(e, pwCreate(e, fs, testCaller, dir, name, oaMode(mode), true, what, where), what, where)
	pwSetupObject(e, fs, h, name, policy.Object{Type: vfs.TypeReg, Mode: mode, UID: 501, GID: 20}, 0, where)
	return h
}

// oaChmod is testCaller's SetAttr of h's mode alone, as setup, which must
// succeed: OpChmod allows the owner any mode (ADR 0014 section 5), so it rests
// on no override.
func oaChmod(e *uc43Env, fs *FS, h vfs.Handle, name string, mode uint32, where string) {
	e.t.Helper()
	what := fmt.Sprintf("setup: testCaller's SetAttr(%s, {Mode %#o})", name, mode)
	r := sa24SetAttr(e, fs, testCaller, h, oaMode(mode), what, where)
	if r.err != nil || r.v.Mode != mode {
		e.t.Fatalf("%s = (mode %#o, %s), want (mode %#o, nil): OpChmod allows the owner any mode (ADR 0014 section 5; "+
			"%s)", what, r.v.Mode, oaStatus(r.err), mode, where)
	}
}

// oaWantBytes requires c's Read of count bytes at off of h to return want and
// nil, and returns what it returned.
func oaWantBytes(e *uc43Env, fs *FS, c vfs.Caller, h vfs.Handle, name string, off uint64, count uint32, want []byte,
	why, where string) sa24ReadResult {
	e.t.Helper()
	what := fmt.Sprintf("%s's Read(%s, %d, %d)", pwWho(c), name, off, count)
	r := pwRead(e, fs, c, h, off, count, what, where)
	if r.err != nil || !bytes.Equal(r.data, want) {
		e.t.Errorf("%s = (%d bytes %x, %s), want the %d bytes %x and nil: %s (%s)", what, len(r.data), r.data,
			oaStatus(r.err), len(want), want, why, where)
	}
	return r
}

// oaWantAttr requires GetAttr of h to report size and mode.
func oaWantAttr(e *uc43Env, fs *FS, h vfs.Handle, name string, size uint64, mode uint32, after, why, where string) {
	e.t.Helper()
	g := pwGetAttr(e, fs, h, fmt.Sprintf("GetAttr of %q after %s", name, after), where)
	if g.err != nil || g.v.Size != size || g.v.Mode != mode {
		e.t.Errorf("after %s, GetAttr of %q = (size %d, mode %#o, %s), want (size %d, mode %#o, nil): %s (%s)", after,
			name, g.v.Size, g.v.Mode, oaStatus(g.err), size, mode, why, where)
	}
}

// oaSizeOf returns the size GetAttr reports for h, which must succeed.
func oaSizeOf(e *uc43Env, fs *FS, h vfs.Handle, name, where string) uint64 {
	e.t.Helper()
	return sa24GetAttr(e, fs, h, fmt.Sprintf("GetAttr of %q", name), where).Size
}

// TestTheOwnerReadsWritesAndTruncatesItsFileWhateverItsMode is A5 of ADR 0025
// section 6 (section 1; the issue's first done-when).
//
// testCaller creates, in the root, ro with Mode 0o444, none with Mode 0, wo
// with Mode 0o200 and e0 with Mode 0, each exclusive. Then, by testCaller:
// Write(ro, 0, p), 100 bytes, returns 100 and nil, GetAttr reports size 100 and
// mode 0o444, and Read(ro, 0, 100) returns p; SetAttr(ro, {Size: 10}) returns
// nil with size 10 and mode 0o444, and a Read returns p[:10] with eof;
// Write(none, 0, q), 50 bytes, returns 50 and nil, Read(none, 0, 50) returns q,
// and SetAttr(none, {Size: 20}) returns nil with size 20; Write(wo, 0, r)
// returns nil and Read(wo, 0, len(r)) returns r; Read(e0, 0, 10) returns no
// bytes, eof true and nil, e0 having no FS.open entry; and SetAttr(ro, {Size:
// 5, Mode: 0o400}) returns nil with both applied.
//
// The override is the owner's alone: sa24Other's and pwMember's Write(ro, ...)
// and sa24Other's SetAttr(ro, {Size: 0}) return vfs.ErrAcces, as do
// sa24Other's and pwMember's Read(none, ...), and ro's and none's sizes are
// afterwards what they were; sp24Root's Read(none, 0, 20) returns q[:20].
// Then Sync returns nil, and on a remount testCaller's Read of none, which has
// no FS.open entry there, returns q[:20], with none of mode 0 and size 20 and
// ro of mode 0o400 and size 5.
func TestTheOwnerReadsWritesAndTruncatesItsFileWhateverItsMode(t *testing.T) {
	const where = "A5, ADR 0025 sections 1 and 6"
	const owner = "the owner of a regular file may read, write and truncate it whatever its mode (ADR 0025 " +
		"section 1, line 3)"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	tc := testCaller

	ro := oaCreate(e, fs, root, "ro", 0o444, where)
	none := oaCreate(e, fs, root, "none", 0, where)
	wo := oaCreate(e, fs, root, "wo", 0o200, where)
	e0 := oaCreate(e, fs, root, "e0", 0, where)
	p, q, r := pwBytes(100, 11), pwBytes(50, 23), pwBytes(30, 37)

	// ro, 0o444: the owner writes and truncates it.
	what := "testCaller's Write(ro, 0, 100 bytes, vfs.Unstable)"
	w := pwWrite(e, fs, tc, ro, 0, p, what, where)
	if w.err != nil || w.v != 100 {
		t.Errorf("%s = (%d, %s), want (100, nil): ro is 0o444, and %s (%s)", what, w.v, oaStatus(w.err), owner, where)
	}
	oaWantAttr(e, fs, ro, "ro", 100, 0o444, what, "the write was applied, and the mode is unchanged: "+owner, where)
	oaWantBytes(e, fs, tc, ro, "ro", 0, 100, p, "the owner's read returns what its write wrote", where)

	what = "testCaller's SetAttr(ro, {Size: 10})"
	sa := sa24SetAttr(e, fs, tc, ro, oaSize(10), what, where)
	if sa.err != nil || sa.v.Size != 10 || sa.v.Mode != 0o444 {
		t.Errorf("%s = (size %d, mode %#o, %s), want (size 10, mode 0o444, nil): %s (%s)", what, sa.v.Size, sa.v.Mode,
			oaStatus(sa.err), owner, where)
	}
	rd := oaWantBytes(e, fs, tc, ro, "ro", 0, 100, p[:10], "the size change to 10 was applied: "+owner, where)
	if rd.err == nil && !rd.eof {
		t.Errorf("testCaller's Read(ro, 0, 100) of a file of size 10 returned eof false, want true: the read reaches "+
			"the end of the file (%s)", where)
	}

	// none, 0: the owner writes, reads and truncates it.
	what = "testCaller's Write(none, 0, 50 bytes, vfs.Unstable)"
	w = pwWrite(e, fs, tc, none, 0, q, what, where)
	if w.err != nil || w.v != 50 {
		t.Errorf("%s = (%d, %s), want (50, nil): none is mode 0, and %s (%s)", what, w.v, oaStatus(w.err), owner, where)
	}
	oaWantBytes(e, fs, tc, none, "none", 0, 50, q, "none is mode 0, and "+owner, where)
	what = "testCaller's SetAttr(none, {Size: 20})"
	sa = sa24SetAttr(e, fs, tc, none, oaSize(20), what, where)
	if sa.err != nil || sa.v.Size != 20 {
		t.Errorf("%s = (size %d, %s), want (size 20, nil): none is mode 0, and %s (%s)", what, sa.v.Size,
			oaStatus(sa.err), owner, where)
	}

	// wo, 0o200: the owner reads it.
	what = "testCaller's Write(wo, 0, 30 bytes, vfs.Unstable)"
	w = pwWrite(e, fs, tc, wo, 0, r, what, where)
	if w.err != nil {
		t.Errorf("%s = (%d, %s), want nil: the owner bits of 0o200 give write (ADR 0014 section 5; %s)", what, w.v,
			oaStatus(w.err), where)
	}
	oaWantBytes(e, fs, tc, wo, "wo", 0, uint32(len(r)), r, "wo is 0o200, and "+owner, where)

	// e0, 0, never written: Read's step 1 alone.
	what = "testCaller's Read(e0, 0, 10)"
	er := pwRead(e, fs, tc, e0, 0, 10, what, where)
	if er.err != nil || len(er.data) != 0 || !er.eof {
		t.Errorf("%s = (%d bytes, eof %v, %s), want (no bytes, eof true, nil): e0 is mode 0 and empty, and was "+
			"never written, so it has no FS.open entry and the read is step 1's alone; %s (%s)", what, len(er.data),
			er.eof, oaStatus(er.err), owner, where)
	}

	// A size and a mode, applied whole.
	what = "testCaller's SetAttr(ro, {Size: 5, Mode: 0o400})"
	sa = sa24SetAttr(e, fs, tc, ro, vfs.SetAttr{Size: sa24U64(5), Mode: sa24U32(0o400)}, what, where)
	if sa.err != nil || sa.v.Size != 5 || sa.v.Mode != 0o400 {
		t.Errorf("%s = (size %d, mode %#o, %s), want (size 5, mode 0o400, nil): a SetAttr that sets a size and "+
			"other attributes is applied whole (ADR 0024 section 2), and %s (%s)", what, sa.v.Size, sa.v.Mode,
			oaStatus(sa.err), owner, where)
	}

	// The override is the owner's alone.
	const notOwner = "the override is the owner's alone, and this caller's class of the mode has neither the " +
		"permission nor, for a read, execute (ADR 0025 sections 1 and 2)"
	roSize, noneSize := oaSizeOf(e, fs, ro, "ro", where), oaSizeOf(e, fs, none, "none", where)
	ten := pwBytes(10, 41)
	for _, c := range []vfs.Caller{sa24Other, pwMember} {
		what := fmt.Sprintf("%s's Write(ro, 0, 10 bytes, vfs.Unstable)", pwWho(c))
		oaWant(t, what, pwWrite(e, fs, c, ro, 0, ten, what, where).err, vfs.ErrAcces, notOwner, where)
	}
	what = "sa24Other's SetAttr(ro, {Size: 0})"
	oaWant(t, what, sa24SetAttr(e, fs, sa24Other, ro, oaSize(0), what, where).err, vfs.ErrAcces, notOwner, where)
	for _, c := range []vfs.Caller{sa24Other, pwMember} {
		what := fmt.Sprintf("%s's Read(none, 0, 20)", pwWho(c))
		oaWant(t, what, pwRead(e, fs, c, none, 0, 20, what, where).err, vfs.ErrAcces, notOwner, where)
	}
	if got := oaSizeOf(e, fs, ro, "ro", where); got != roSize {
		t.Errorf("after the other callers' refused calls, ro's size is %d, want %d, what it was: a refused call "+
			"changes nothing (%s)", got, roSize, where)
	}
	if got := oaSizeOf(e, fs, none, "none", where); got != noneSize {
		t.Errorf("after the other callers' refused calls, none's size is %d, want %d, what it was: a refused call "+
			"changes nothing (%s)", got, noneSize, where)
	}
	oaWantBytes(e, fs, sp24Root, none, "none", 0, 20, q[:20], "root may read any regular file (ADR 0014 section 5)",
		where)

	// Durable, and the override holds on a remount.
	if err := e.sync(fs, "testCaller's Sync", where); err != nil {
		t.Errorf("Sync = %v, want nil against a healthy store (%s)", err, where)
	}
	rm := e.remount(where)
	if rm == nil {
		return
	}
	rroot := e.root(rm, where)
	ln := e.lookup(rm, rroot, "none", where)
	lr := e.lookup(rm, rroot, "ro", where)
	if ln.err != nil || lr.err != nil {
		t.Fatalf("on the remount, Lookup of \"none\" = %s and of \"ro\" = %s, want nil for both: the Sync committed "+
			"them (%s)", oaStatus(ln.err), oaStatus(lr.err), where)
	}
	if ln.attr.Mode != 0 || ln.attr.Size != 20 {
		t.Errorf("on the remount, none is mode %#o size %d, want mode 0 size 20 (%s)", ln.attr.Mode, ln.attr.Size,
			where)
	}
	if lr.attr.Mode != 0o400 || lr.attr.Size != 5 {
		t.Errorf("on the remount, ro is mode %#o size %d, want mode 0o400 size 5 (%s)", lr.attr.Mode, lr.attr.Size,
			where)
	}
	oaWantBytes(e, rm, tc, ln.h, "none, on the remount", 0, 100, q[:20], "none has no FS.open entry on the "+
		"remount, and "+owner, where)
}

// oaFile is one of A6's files.
type oaFile struct {
	name string
	mode uint32
	data []byte
	h    vfs.Handle
}

// TestACallerWithExecutePermissionReadsARegularFile is A6 of ADR 0025 section
// 6 (section 2; the second done-when).
//
// testCaller creates x711, x750, x701 and x710, of those modes, and writes 100
// bytes to each. A Read of 100 bytes at 0: by sa24Other, of x711 and of x701,
// the bytes, of x750 and of x710, vfs.ErrAcces; by pwMember, of x711, of x750
// and of x710, the bytes, of x701, vfs.ErrAcces, since one class judges; by
// oaSupp, of x710, the bytes. Execute gives no write: sa24Other's Write(x711,
// ...) and SetAttr(x711, {Size: 0}) return vfs.ErrAcces, and x711 is still 100
// bytes. Access with every bit asked for gives sa24Other and pwMember
// vfs.AccessExecute alone on x711, pwMember vfs.AccessRead and
// vfs.AccessExecute on x750, and sa24Other nothing on x750. After a Sync, on a
// remount, sa24Other's Read of x711 returns the bytes.
func TestACallerWithExecutePermissionReadsARegularFile(t *testing.T) {
	const where = "A6, ADR 0025 sections 2 and 6"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)

	x711 := &oaFile{name: "x711", mode: 0o711, data: pwBytes(100, 51)}
	x750 := &oaFile{name: "x750", mode: 0o750, data: pwBytes(100, 53)}
	x701 := &oaFile{name: "x701", mode: 0o701, data: pwBytes(100, 57)}
	x710 := &oaFile{name: "x710", mode: 0o710, data: pwBytes(100, 59)}
	for _, f := range []*oaFile{x711, x750, x701, x710} {
		f.h = oaCreate(e, fs, root, f.name, f.mode, where)
		e.write(fs, f.h, 0, f.data, fmt.Sprintf("setup: testCaller's Write of 100 bytes at 0 of %q", f.name), where)
		pwSetupObject(e, fs, f.h, f.name, policy.Object{Type: vfs.TypeReg, Mode: f.mode, UID: 501, GID: 20}, 100,
			where)
	}

	o, m := sa24Other, pwMember
	reads := []struct {
		c       vfs.Caller
		f       *oaFile
		allowed bool
		why     string
	}{
		{o, x711, true, "the other bits of 0o711 have execute, which allows a read of a regular file"},
		{o, x701, true, "the other bits of 0o701 have execute, which allows a read of a regular file"},
		{o, x750, false, "the other bits of 0o750 have neither read nor execute"},
		{o, x710, false, "the other bits of 0o710 have neither read nor execute"},
		{m, x711, true, "the group bits of 0o711 have execute, which allows a read of a regular file"},
		{m, x750, true, "the group bits of 0o750 have read"},
		{m, x710, true, "the group bits of 0o710 have execute, which allows a read of a regular file"},
		{m, x701, false, "one class judges: the group bits of 0o701 are empty, and the other bits' execute does " +
			"not help a member of the group"},
		{oaSupp, x710, true, "the group bits of 0o710 have execute, and a supplementary gid puts the caller in " +
			"the group"},
	}
	for _, rr := range reads {
		why := rr.why + " (ADR 0025 section 2, line 4; ADR 0014 section 5)"
		if rr.allowed {
			oaWantBytes(e, fs, rr.c, rr.f.h, rr.f.name, 0, 100, rr.f.data, why, where)
			continue
		}
		what := fmt.Sprintf("%s's Read(%s, 0, 100)", pwWho(rr.c), rr.f.name)
		oaWant(t, what, pwRead(e, fs, rr.c, rr.f.h, 0, 100, what, where).err, vfs.ErrAcces, why, where)
	}

	// Execute gives no write.
	const noWrite = "execute gives no write and no size change: read with execute is for reads only (ADR 0025 " +
		"section 2)"
	what := "sa24Other's Write(x711, 0, 10 bytes, vfs.Unstable)"
	oaWant(t, what, pwWrite(e, fs, o, x711.h, 0, pwBytes(10, 61), what, where).err, vfs.ErrAcces, noWrite, where)
	what = "sa24Other's SetAttr(x711, {Size: 0})"
	oaWant(t, what, sa24SetAttr(e, fs, o, x711.h, oaSize(0), what, where).err, vfs.ErrAcces, noWrite, where)
	if got := oaSizeOf(e, fs, x711.h, "x711", where); got != 100 {
		t.Errorf("after sa24Other's refused Write and SetAttr, x711's size is %d, want 100: %s (%s)", got, noWrite,
			where)
	}

	// Access reports the mode, not the read execute allows.
	acc := []struct {
		c    vfs.Caller
		f    *oaFile
		want uint32
	}{
		{o, x711, vfs.AccessExecute},
		{m, x711, vfs.AccessExecute},
		{m, x750, vfs.AccessRead | vfs.AccessExecute},
		{o, x750, 0},
	}
	for _, a := range acc {
		what := fmt.Sprintf("%s's Access(%s, every bit)", pwWho(a.c), a.f.name)
		got := pwAccess(e, fs, a.c, a.f.h, pwAllAccess, what, where)
		if got.err != nil || got.v != a.want {
			t.Errorf("%s = (%#x, %s), want (%#x, nil): ACCESS reports what the mode bits give the caller's class, "+
				"and not the read that execute allows (ADR 0025 sections 2 and 3; %s)", what, got.v,
				oaStatus(got.err), a.want, where)
		}
	}

	// Durable, and the rule holds on a remount.
	if err := e.sync(fs, "testCaller's Sync", where); err != nil {
		t.Errorf("Sync = %v, want nil against a healthy store (%s)", err, where)
	}
	rm := e.remount(where)
	if rm == nil {
		return
	}
	l := e.lookup(rm, e.root(rm, where), "x711", where)
	if l.err != nil {
		t.Fatalf("on the remount, Lookup of \"x711\" = %s, want nil: the Sync committed it (%s)", oaStatus(l.err),
			where)
	}
	oaWantBytes(e, rm, o, l.h, "x711, on the remount", 0, 100, x711.data, "the other bits of 0o711 have execute, "+
		"which allows a read of a regular file (ADR 0025 section 2, line 4)", where)
}

// TestAccessReportsTheModeWhateverTheOperationsAllow is A7 of ADR 0025 section
// 6 (section 3; the third done-when).
//
// testCaller creates a444, a200, a100, a000 and a644 with no Mode, writes bytes
// to each, and then sets each one's mode to the one its name gives, which
// OpChmod allows its owner, so that the setup does not rest on the override.
// For testCaller, with every bit asked for, Access gives vfs.AccessRead on
// a444; vfs.AccessModify and vfs.AccessExtend on a200; vfs.AccessExecute on
// a100; nothing on a000; and vfs.AccessRead, vfs.AccessModify and
// vfs.AccessExtend on a644. Asked for vfs.AccessModify alone on a444, it gives
// nothing. Then testCaller's Read and Write of each of the five return nil,
// and the same Access calls give the same answers again. sp24Root's Access of
// a000 gives vfs.AccessRead, vfs.AccessModify and vfs.AccessExtend.
func TestAccessReportsTheModeWhateverTheOperationsAllow(t *testing.T) {
	const where = "A7, ADR 0025 sections 3 and 6"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	tc := testCaller

	mw := vfs.AccessModify | vfs.AccessExtend
	files := []struct {
		name   string
		mode   uint32
		access uint32
		h      vfs.Handle
	}{
		{name: "a444", mode: 0o444, access: vfs.AccessRead},
		{name: "a200", mode: 0o200, access: mw},
		{name: "a100", mode: 0o100, access: vfs.AccessExecute},
		{name: "a000", mode: 0, access: 0},
		{name: "a644", mode: 0o644, access: vfs.AccessRead | mw},
	}
	for i := range files {
		f := &files[i]
		f.h = e.create(fs, root, f.name, where)
		e.write(fs, f.h, 0, pwBytes(40, byte(i)), fmt.Sprintf("setup: testCaller's Write of 40 bytes at 0 of %q",
			f.name), where)
		oaChmod(e, fs, f.h, f.name, f.mode, where)
	}
	a444 := files[0].h

	checkAccess := func(when string) {
		t.Helper()
		for _, f := range files {
			what := fmt.Sprintf("testCaller's Access(%s, every bit), %s", f.name, when)
			got := pwAccess(e, fs, tc, f.h, pwAllAccess, what, where)
			if got.err != nil || got.v != f.access {
				t.Errorf("%s = (%#x, %s), want (%#x, nil): ACCESS reports what the owner bits of %#o give, and not "+
					"the owner override (ADR 0025 section 3; %s)", what, got.v, oaStatus(got.err), f.access, f.mode,
					where)
			}
		}
		what := fmt.Sprintf("testCaller's Access(a444, vfs.AccessModify), %s", when)
		got := pwAccess(e, fs, tc, a444, vfs.AccessModify, what, where)
		if got.err != nil || got.v != 0 {
			t.Errorf("%s = (%#x, %s), want (0, nil): the owner bits of 0o444 give no write, and ACCESS does not "+
				"report the override, which would let a client open a read-only file for writing (ADR 0025 section "+
				"3; %s)", what, got.v, oaStatus(got.err), where)
		}
	}
	checkAccess("before the owner's reads and writes")

	for i, f := range files {
		what := fmt.Sprintf("testCaller's Read(%s, 0, 40)", f.name)
		oaWant(t, what, pwRead(e, fs, tc, f.h, 0, 40, what, where).err, nil, fmt.Sprintf("the owner of a regular "+
			"file may read it whatever its mode, %#o here (ADR 0025 section 1, line 3)", f.mode), where)
		what = fmt.Sprintf("testCaller's Write(%s, 0, 10 bytes, vfs.Unstable)", f.name)
		oaWant(t, what, pwWrite(e, fs, tc, f.h, 0, pwBytes(10, byte(100+i)), what, where).err, nil,
			fmt.Sprintf("the owner of a regular file may write it whatever its mode, %#o here (ADR 0025 section 1, "+
				"line 3)", f.mode), where)
	}
	checkAccess("after the owner's reads and writes")

	what := "root's Access(a000, every bit)"
	got := pwAccess(e, fs, sp24Root, files[3].h, pwAllAccess, what, where)
	if want := vfs.AccessRead | mw; got.err != nil || got.v != want {
		t.Errorf("%s = (%#x, %s), want (%#x, nil): root is granted read and write, and no execute where no execute "+
			"bit is set (ADR 0014 section 5; ADR 0025 section 3; %s)", what, got.v, oaStatus(got.err), want, where)
	}
}

// oaCall is one of A8's calls: what it is, the call, and what it must return.
type oaCall struct {
	what string
	fn   func(what string) error
	want error
}

// TestADirectorysOwnerGetsNoOverride is A8 of ADR 0025 section 6 (section 1),
// a characterisation that passes before the change and after.
//
// testCaller makes a directory d, and in it a file k and a directory sub, and
// a file top in the root. It then sets d's mode, and makes these calls itself:
// at 0o555, Create, Mkdir and Symlink of "n" in d, Remove(d, "k"), Rmdir(d,
// "sub"), Rename(d, "k", d, "k2"), Rename(d, "k", root, "k2") and Rename(root,
// "top", d, "in") return vfs.ErrAcces, and Lookup(d, "k") and ReadDir(d, ...)
// nil; at 0o655, Lookup(d, "k") and Create(d, "n", ...) return vfs.ErrAcces,
// and ReadDir(d, ...) nil; at 0o355, ReadDir(d, ...) returns vfs.ErrAcces, and
// Lookup(d, "k") nil; at 0, Lookup, ReadDir and Create return vfs.ErrAcces,
// Access with every bit gives nothing, and Read(d, 0, 10), Write(d, 0, ...)
// and SetAttr(d, {Size: 0}) return vfs.ErrIsDir, the type before the
// override; at 0o755, Remove(d, "k") returns nil. Access is not asked of d at
// 0o655 or 0o355 (U5), and no mode has 0o1000 set (U4).
func TestADirectorysOwnerGetsNoOverride(t *testing.T) {
	const where = "A8, ADR 0025 sections 1 and 6"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	tc := testCaller

	d := e.mkdir(fs, root, "d", where)
	e.create(fs, d, "k", where)
	e.mkdir(fs, d, "sub", where)
	e.create(fs, root, "top", where)

	lookup := func(what string) error { return pwLookup(e, fs, tc, d, "k", what, where).err }
	readDir := func(what string) error { return pwReadDir(e, fs, tc, d, false, what, where).err }
	create := func(what string) error {
		return pwCreate(e, fs, tc, d, "n", vfs.SetAttr{}, true, what, where).err
	}
	acces := vfs.ErrAcces

	run := func(mode uint32, calls []oaCall) {
		t.Helper()
		oaChmod(e, fs, d, "d", mode, where)
		for _, c := range calls {
			what := fmt.Sprintf("testCaller's %s, d being %#o", c.what, mode)
			oaWant(t, what, c.fn(what), c.want, fmt.Sprintf("a directory's owner is held to the owner bits, %#o, "+
				"and gets no override (ADR 0025 section 1; ADR 0014 section 5)", (mode>>6)&7), where)
		}
	}

	run(0o555, []oaCall{
		{`Create(d, "n", vfs.SetAttr{}, exclusive)`, create, acces},
		{`Mkdir(d, "n", vfs.SetAttr{})`, func(what string) error {
			return pwMkdir(e, fs, tc, d, "n", vfs.SetAttr{}, what, where).err
		}, acces},
		{`Symlink(d, "n", "k", vfs.SetAttr{})`, func(what string) error {
			return pwSymlink(e, fs, tc, d, "n", "k", vfs.SetAttr{}, what, where).err
		}, acces},
		{`Remove(d, "k")`, func(what string) error { return pwRemove(e, fs, tc, d, "k", what, where) }, acces},
		{`Rmdir(d, "sub")`, func(what string) error { return pwRmdir(e, fs, tc, d, "sub", what, where) }, acces},
		{`Rename(d, "k", d, "k2")`, func(what string) error {
			return pwRename(e, fs, tc, d, "k", d, "k2", what, where)
		}, acces},
		{`Rename(d, "k", root, "k2")`, func(what string) error {
			return pwRename(e, fs, tc, d, "k", root, "k2", what, where)
		}, acces},
		{`Rename(root, "top", d, "in")`, func(what string) error {
			return pwRename(e, fs, tc, root, "top", d, "in", what, where)
		}, acces},
		{`Lookup(d, "k")`, lookup, nil},
		{"ReadDir(d, 0, zero verifier, 4096, plus false)", readDir, nil},
	})

	run(0o655, []oaCall{
		{`Lookup(d, "k")`, lookup, acces},
		{`Create(d, "n", vfs.SetAttr{}, exclusive)`, create, acces},
		{"ReadDir(d, 0, zero verifier, 4096, plus false)", readDir, nil},
	})

	run(0o355, []oaCall{
		{"ReadDir(d, 0, zero verifier, 4096, plus false)", readDir, acces},
		{`Lookup(d, "k")`, lookup, nil},
	})

	const isDir = "a directory is refused Read, Write and a size change by its type, before the override is " +
		"looked at (ADR 0025 section 1, line 1)"
	run(0, []oaCall{
		{`Lookup(d, "k")`, lookup, acces},
		{"ReadDir(d, 0, zero verifier, 4096, plus false)", readDir, acces},
		{`Create(d, "n", vfs.SetAttr{}, exclusive)`, create, acces},
	})
	what := "testCaller's Access(d, every bit), d being 0"
	a := pwAccess(e, fs, tc, d, pwAllAccess, what, where)
	if a.err != nil || a.v != 0 {
		t.Errorf("%s = (%#x, %s), want (0, nil): the owner bits of a directory of mode 0 give nothing, and ACCESS "+
			"reports the mode bits alone (ADR 0025 section 3; ADR 0014 section 5; %s)", what, a.v, oaStatus(a.err),
			where)
	}
	what = "testCaller's Read(d, 0, 10), d being 0"
	oaWant(t, what, pwRead(e, fs, tc, d, 0, 10, what, where).err, vfs.ErrIsDir, isDir, where)
	what = "testCaller's Write(d, 0, 10 bytes, vfs.Unstable), d being 0"
	oaWant(t, what, pwWrite(e, fs, tc, d, 0, pwBytes(10, 71), what, where).err, vfs.ErrIsDir, isDir, where)
	what = "testCaller's SetAttr(d, {Size: 0}), d being 0"
	oaWant(t, what, sa24SetAttr(e, fs, tc, d, oaSize(0), what, where).err, vfs.ErrIsDir, isDir, where)

	run(0o755, []oaCall{
		{`Remove(d, "k")`, func(what string) error { return pwRemove(e, fs, tc, d, "k", what, where) }, nil},
	})
}

// oaGate is A9's setup: a writable mount on which testCaller made ro and none,
// with 100 bytes each, then set their modes to 0o444 and 0, and Synced.
type oaGate struct {
	e                *uc43Env
	fs               *FS
	root, ro, none   vfs.Handle
	roData, noneData []byte
}

// oaGateSetup makes A9's setup. ro and none are created with no Mode and
// written before their modes are set, so the setup does not rest on the
// override.
func oaGateSetup(t *testing.T, where string) oaGate {
	t.Helper()
	e := uc43Setup(t)
	g := oaGate{e: e, roData: pwBytes(100, 81), noneData: pwBytes(100, 83)}
	g.fs = e.newFS(where)
	g.root = e.root(g.fs, where)
	g.ro = e.create(g.fs, g.root, "ro", where)
	g.none = e.create(g.fs, g.root, "none", where)
	e.write(g.fs, g.ro, 0, g.roData, `setup: testCaller's Write of 100 bytes at 0 of "ro"`, where)
	e.write(g.fs, g.none, 0, g.noneData, `setup: testCaller's Write of 100 bytes at 0 of "none"`, where)
	oaChmod(e, g.fs, g.ro, "ro", 0o444, where)
	oaChmod(e, g.fs, g.none, "none", 0, where)
	if err := e.sync(g.fs, "setup: testCaller's Sync", where); err != nil {
		t.Fatalf("setup: Sync = %v, want nil against a healthy store (%s)", err, where)
	}
	return g
}

// TestTheMountGateComesBeforeTheOwnerOverride is A9 of ADR 0025 section 6
// (section 1: the mount gate comes first), in two subtests, each from
// oaGateSetup.
//
// Read-only: a second mount of the same bucket is made with Config.ReadOnly
// set, and the first is not used again. On it, testCaller's Write(ro, ...),
// SetAttr(ro, {Size: 0}), Write(none, ...) and SetAttr(none, {Mode: 0o600})
// return vfs.ErrROFS; testCaller's Read(none, 0, 100) returns the bytes;
// sa24Other's returns vfs.ErrAcces; Access with every bit gives testCaller
// vfs.AccessRead on ro and nothing on none; and both files are still 100
// bytes.
//
// Diverged: as ADR 0023 section 11's U9 diverges a mount
// (TestADivergedMountsSyncMakesNoStoreCall), a foreign root pointer is written
// on the inner store with uc43WriteForeignRoot at the mount's epoch plus one;
// the commit of c2 returns an error whose text contains "commit conflict"; and
// f.mode.State() is mountDiverged. Then testCaller's Write(ro, ...),
// SetAttr(ro, {Size: 0}) and Write(none, ...) return vfs.ErrStale, and its
// Read(none, 0, 100) returns the bytes.
func TestTheMountGateComesBeforeTheOwnerOverride(t *testing.T) {
	t.Run("read-only", func(t *testing.T) {
		const where = "A9, read-only, ADR 0025 sections 1 and 6; ADR 0011 section 4"
		g := oaGateSetup(t, where)
		e, tc := g.e, testCaller

		cfg := uc43Config(e.h)
		cfg.ReadOnly = true
		rofs := e.newFSWith(cfg, where)
		rroot := e.root(rofs, where)
		lro := e.lookup(rofs, rroot, "ro", where)
		lnone := e.lookup(rofs, rroot, "none", where)
		if lro.err != nil || lnone.err != nil {
			t.Fatalf("on the read-only mount, Lookup of \"ro\" = %s and of \"none\" = %s, want nil for both: the "+
				"setup's Sync committed them (%s)", oaStatus(lro.err), oaStatus(lnone.err), where)
		}
		ro, none := lro.h, lnone.h

		const rofsWhy = "a read-only mount takes no change, the owner's included: Write and SetAttr call mutable() " +
			"at stage A, before the row is asked (ADR 0025 section 1; ADR 0011 section 4; ADR 0014 section 7)"
		ten := pwBytes(10, 91)
		what := "testCaller's Write(ro, 0, 10 bytes, vfs.Unstable) on the read-only mount"
		oaWant(t, what, pwWrite(e, rofs, tc, ro, 0, ten, what, where).err, vfs.ErrROFS, rofsWhy, where)
		what = "testCaller's SetAttr(ro, {Size: 0}) on the read-only mount"
		oaWant(t, what, sa24SetAttr(e, rofs, tc, ro, oaSize(0), what, where).err, vfs.ErrROFS, rofsWhy, where)
		what = "testCaller's Write(none, 0, 10 bytes, vfs.Unstable) on the read-only mount"
		oaWant(t, what, pwWrite(e, rofs, tc, none, 0, ten, what, where).err, vfs.ErrROFS, rofsWhy, where)
		what = "testCaller's SetAttr(none, {Mode: 0o600}) on the read-only mount"
		oaWant(t, what, sa24SetAttr(e, rofs, tc, none, oaMode(0o600), what, where).err, vfs.ErrROFS, rofsWhy, where)

		oaWantBytes(e, rofs, tc, none, "none, on the read-only mount", 0, 100, g.noneData, "Read has no stage A, "+
			"so a read-only mount serves the owner's read of its mode-0 file as a writable mount does (ADR 0025 "+
			"section 1)", where)
		what = "sa24Other's Read(none, 0, 100) on the read-only mount"
		oaWant(t, what, pwRead(e, rofs, sa24Other, none, 0, 100, what, where).err, vfs.ErrAcces,
			"the override is the owner's alone (ADR 0025 section 1)", where)

		acc := []struct {
			name string
			h    vfs.Handle
			want uint32
		}{
			{"ro", ro, vfs.AccessRead},
			{"none", none, 0},
		}
		for _, a := range acc {
			what := fmt.Sprintf("testCaller's Access(%s, every bit) on the read-only mount", a.name)
			got := pwAccess(e, rofs, tc, a.h, pwAllAccess, what, where)
			if got.err != nil || got.v != a.want {
				t.Errorf("%s = (%#x, %s), want (%#x, nil): ACCESS reports the mode bits alone, less a read-only "+
					"mount's write bits (ADR 0025 section 3; %s)", what, got.v, oaStatus(got.err), a.want, where)
			}
		}
		for _, f := range []struct {
			name string
			h    vfs.Handle
		}{{"ro", ro}, {"none", none}} {
			if got := oaSizeOf(e, rofs, f.h, f.name, where); got != 100 {
				t.Errorf("on the read-only mount, after the refused calls, %s's size is %d, want 100: %s (%s)", f.name,
					got, rofsWhy, where)
			}
		}
	})

	t.Run("diverged", func(t *testing.T) {
		const where = "A9, diverged, ADR 0025 sections 1 and 6; ADR 0023 section 11, U9's recipe"
		g := oaGateSetup(t, where)
		e, fs, tc := g.e, g.fs, testCaller

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

		const staleWhy = "a diverged mount takes no change, the owner's included: Write and SetAttr call mutable() " +
			"at stage A, before the row is asked (ADR 0025 section 1; ADR 0014 section 7)"
		ten := pwBytes(10, 93)
		what := "testCaller's Write(ro, 0, 10 bytes, vfs.Unstable) on the diverged mount"
		oaWant(t, what, pwWrite(e, fs, tc, g.ro, 0, ten, what, where).err, vfs.ErrStale, staleWhy, where)
		what = "testCaller's SetAttr(ro, {Size: 0}) on the diverged mount"
		oaWant(t, what, sa24SetAttr(e, fs, tc, g.ro, oaSize(0), what, where).err, vfs.ErrStale, staleWhy, where)
		what = "testCaller's Write(none, 0, 10 bytes, vfs.Unstable) on the diverged mount"
		oaWant(t, what, pwWrite(e, fs, tc, g.none, 0, ten, what, where).err, vfs.ErrStale, staleWhy, where)

		oaWantBytes(e, fs, tc, g.none, "none, on the diverged mount", 0, 100, g.noneData, "Read has no stage A, "+
			"so a diverged mount serves the owner's read of its mode-0 file as a writable mount does (ADR 0025 "+
			"section 1)", where)
	})
}
