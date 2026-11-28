package blobfs

// Clean-room tests for #69, "SETATTR with a size, and WRITE, accept a
// symlink's handle", written without reading the implementation, from ADR
// 0029 (docs/adr/0029-only-a-regular-file-is-read-written-or-given-a-size.md)
// sections 1, 2, 3, 5, 6 and 9, ADR 0014 sections 5 and 11, ADR 0007 sections
// 5 to 7, ADR 0024 section 7 (the recipe for a snapshot changed by hand), ADR
// 0025 section 6 (A9's read-only and diverged mounts), ADR 0027 section 10,
// and internal/vfs/vfs.go.
//
// ADR 0029 section 1: a request for OpRead, OpWrite or OpTruncate that is not
// advisory, of an object that is neither a regular file nor a directory, is
// refused with vfs.ErrInval, for every caller, root included, before any
// permission is looked at. Through the FS, under the installed Unix(), a Read,
// a Write of at least one byte and a SetAttr that sets a size of a symlink's
// handle each return vfs.ErrInval and change nothing. Section 2 keeps the
// order of errors: the mount gate, then vfs.ErrFBig, then the handle, then
// the type. Section 3: Access still reports a symlink's mode.
//
// Covered, with the names ADR 0029 section 9 pins:
//   - N4, TestASymlinkIsNeitherReadNorWrittenNorGivenASize;
//   - N5, TestTheTypeRefusalKeepsItsPlaceAmongTheChecks;
//   - N6, TestAccessStillReportsASymlinksMode, a characterisation;
//   - N7, TestTheMountRecordCountsRegularFilesOnly, a characterisation, which
//     exercises ADR 0007 section 6's filter (the issue's third done-when);
//   - N8, TestASymlinkAnEarlierBuildResizedIsRefusedAsAnyOther.
//
// The safety rules (ADR 0029 section 9; ADR 0014 section 11; ADR 0011 section
// 7): every bucket is a *store.Local under the test's t.TempDir(), behind an
// unarmed uc43Store, through uc43Setup; every mount is made with uc43Config,
// through nrConfig, which also sets a commit interval of an hour so that no
// background commit runs while a test counts store calls or reads FS.dirty;
// every New and every FS call runs in uc43Go and is waited for under
// uc43Bound; nothing sleeps, polls or runs in parallel; FS.dirty and
// FS.changes are read through sa24Read, and FS.open through bpOpen. Unix() is
// left in place on every mount: no test here writes FS.perm, or any other
// field of FS. No write here starts just below MaxFileSize(): a build without
// ADR 0029's line 2 would accept it, and its next flush would build 2^20
// chunk references (ADR 0007 section 7). The over-limit requests are the
// cheapest, a write at MaxFileSize() and a size of MaxFileSize() + 1, and a
// wrong answer to either ends the test at once. A snapshot is changed by hand
// only as ADR 0024 section 7's recipe says, after a Sync, on the inner store,
// leaving the root pointer as it is; the mount that wrote it is never used
// again, and every size set builds no chunk list.
//
// The callers are testCaller (vfs.Caller{UID: 501, GID: 20}), who owns the
// root directory and what it makes (uc43Caller, which uc43Env's methods use,
// is the same identity); pwMember (vfs.Caller{UID: 503, GID: 20}), in the
// group; sa24Other (vfs.Caller{UID: 502, GID: 30}), neither; and sp24Root
// (vfs.Caller{UID: 0}). Every refusal is compared with == against the bare
// vfs.Status. The bits are written R, L, M, E, D and X, as in ADR 0027
// section 10.
//
// Not covered, on purpose: ADR 0014 section 11's U6 (no time is set here);
// what COMMIT answers for a symlink's handle, and what a Create that is not
// exclusive answers for a name that is a symlink (ADR 0029, "What this does
// not decide"); and the wire, which internal/nfs/symlink_handle_test.go
// covers.

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"strata/internal/vfs"
)

// nrTarget is the target of every symlink here: 14 bytes long.
const nrTarget = "dir/target.txt"

// nrCaller is a caller with a name for failure messages.
type nrCaller struct {
	name string
	c    vfs.Caller
}

// nrCallers are ADR 0029 section 9's four callers, in the order of section
// 3's table: the owner, a member of the group, neither, and root.
var nrCallers = []nrCaller{
	{name: "testCaller (501:20, the owner)", c: testCaller},
	{name: "pwMember (503:20, in the group)", c: pwMember},
	{name: "sa24Other (502:30, neither)", c: sa24Other},
	{name: "sp24Root (uid 0)", c: sp24Root},
}

// nrOwner is testCaller as an nrCaller.
var nrOwner = nrCaller{name: "testCaller (501:20, the owner)", c: testCaller}

// nrConfig is uc43Config over the harness's recording store, with a commit
// interval of an hour, so that nothing commits unless a test calls Sync.
func nrConfig(e *uc43Env) Config {
	cfg := uc43Config(e.h)
	cfg.CommitInterval = time.Hour
	return cfg
}

// nrStatus names what a call returned, as pwStatus does, naming vfs.ErrFBig
// and vfs.ErrROFS too.
func nrStatus(err error) string {
	if s, ok := err.(vfs.Status); ok {
		switch s {
		case vfs.ErrFBig:
			return "vfs.ErrFBig"
		case vfs.ErrROFS:
			return "vfs.ErrROFS"
		}
	}
	return pwStatus(err)
}

// nrHow names a vfs.Stability.
func nrHow(how vfs.Stability) string {
	switch how {
	case vfs.Unstable:
		return "vfs.Unstable"
	case vfs.DataSync:
		return "vfs.DataSync"
	case vfs.FileSync:
		return "vfs.FileSync"
	}
	return fmt.Sprintf("vfs.Stability(%d)", uint32(how))
}

// nrMask spells an Access mask by ADR 0027 section 10's letters.
func nrMask(m uint32) string {
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
	return fmt.Sprintf("%s (%#x)", strings.Join(parts, " "), m)
}

// nrWriteResult is what one Write returned.
type nrWriteResult struct {
	n   uint32
	how vfs.Stability
	err error
}

// nrWrite runs one fs.Write of data at off of h, as c, with the stability
// asked for.
func nrWrite(e *uc43Env, fs *FS, c vfs.Caller, h vfs.Handle, off uint64, data []byte, how vfs.Stability, what,
	where string) nrWriteResult {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() nrWriteResult {
		n, st, err := fs.Write(ctx, c, h, off, data, how)
		return nrWriteResult{n: n, how: st, err: err}
	}).wait(e.t, what, where)
}

// nrReadLink runs fs.ReadLink of h.
func nrReadLink(e *uc43Env, fs *FS, h vfs.Handle, what, where string) uc43R[string] {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() uc43R[string] {
		s, err := fs.ReadLink(ctx, h)
		return uc43R[string]{s, err}
	}).wait(e.t, what, where)
}

// nrDirtyBytes returns fs.DirtyBytes(), whatever its type, for comparison
// with ==.
func nrDirtyBytes(e *uc43Env, fs *FS, what, where string) any {
	e.t.Helper()
	return uc43Go(e, func() any { return fs.DirtyBytes() }).wait(e.t, what, where)
}

// nrMaxFileSize returns fs.MaxFileSize().
func nrMaxFileSize(e *uc43Env, fs *FS, where string) uint64 {
	e.t.Helper()
	return uc43Go(e, func() uint64 { return fs.MaxFileSize() }).wait(e.t, "MaxFileSize()", where)
}

// nrWantSymlink requires h, a symlink made in setup by testCaller to
// nrTarget, to be as ADR 0029 section 9's facts give it: type vfs.TypeLnk, of
// mode mode, owned by 501:20, of size 14, the length of its target, with
// Used 0; ReadLink returns the target; and it has no FS.open entry. It
// returns GetAttr's attributes.
func nrWantSymlink(e *uc43Env, fs *FS, h vfs.Handle, name string, mode uint32, where string) vfs.Attr {
	e.t.Helper()
	a := sa24GetAttr(e, fs, h, fmt.Sprintf("setup: GetAttr of %q", name), where)
	if a.Type != vfs.TypeLnk || a.Mode != mode || a.UID != 501 || a.GID != 20 || a.Size != uint64(len(nrTarget)) ||
		a.Used != 0 {
		e.t.Fatalf("setup: GetAttr of %q = (type %d, mode %#o, uid %d, gid %d, size %d, used %d), want (type %d, mode "+
			"%#o, uid 501, gid 20, size %d, used 0): Symlink makes a vfs.TypeLnk of mode 0o777, or the Mode given "+
			"masked to 0o7777, owned by its caller, whose size is its target's length and which uses no space "+
			"(ADR 0029 section 9, the facts; %s)", name, a.Type, a.Mode, a.UID, a.GID, a.Size, a.Used, vfs.TypeLnk,
			mode, len(nrTarget), where)
	}
	rl := nrReadLink(e, fs, h, fmt.Sprintf("setup: ReadLink of %q", name), where)
	if rl.err != nil || rl.v != nrTarget {
		e.t.Fatalf("setup: ReadLink of %q = (%q, %s), want (%q, nil) (ADR 0029 section 9, the facts; %s)", name, rl.v,
			nrStatus(rl.err), nrTarget, where)
	}
	if of := bpOpen(e.t, fs, a.FileID, fmt.Sprintf("setup: FS.open[%d], %q's entry", a.FileID, name)); of != nil {
		e.t.Fatalf("setup: %q has an FS.open entry, want none: a symlink has none (ADR 0029 section 9, the facts; "+
			"%s)", name, where)
	}
	return a
}

// nrState is everything ADR 0029 section 9's facts say a refused call leaves
// as it was, for one symlink: every attribute GetAttr reports, what ReadLink
// returns, DirtyBytes(), whether it has an FS.open entry, FS.dirty and
// FS.changes.
type nrState struct {
	attr       vfs.Attr
	target     string
	targetErr  error
	dirtyBytes any
	open       bool
	dirty      bool
	changes    uint64
}

// nrCapture reads h's nrState. GetAttr is asked first, so that what ReadLink
// does cannot show in the attributes read in the same capture.
func nrCapture(e *uc43Env, fs *FS, h vfs.Handle, name, when, where string) nrState {
	e.t.Helper()
	var s nrState
	s.attr = sa24GetAttr(e, fs, h, fmt.Sprintf("GetAttr of %q %s", name, when), where)
	rl := nrReadLink(e, fs, h, fmt.Sprintf("ReadLink of %q %s", name, when), where)
	s.target, s.targetErr = rl.v, rl.err
	s.dirtyBytes = nrDirtyBytes(e, fs, "DirtyBytes() "+when, where)
	s.open = bpOpen(e.t, fs, s.attr.FileID, fmt.Sprintf("FS.open[%d], %q's entry, %s", s.attr.FileID, name,
		when)) != nil
	f := sa24Read(e, fs, "reading FS.dirty and FS.changes "+when, where)
	s.dirty, s.changes = f.dirty, f.changes
	return s
}

// nrDiff describes how after differs from before, or returns "". An FS.open
// entry is reported even if before had one, since a symlink never has one.
func nrDiff(before, after nrState) string {
	var d []string
	if a := sa24Diff(before.attr, after.attr); a != "" {
		d = append(d, "GetAttr reports "+a)
	}
	if after.attr.Used != before.attr.Used {
		d = append(d, fmt.Sprintf("GetAttr reports Used %d, was %d", after.attr.Used, before.attr.Used))
	}
	if after.target != before.target || after.targetErr != before.targetErr {
		d = append(d, fmt.Sprintf("ReadLink returns (%q, %s), returned (%q, %s)", after.target,
			nrStatus(after.targetErr), before.target, nrStatus(before.targetErr)))
	}
	if after.dirtyBytes != before.dirtyBytes {
		d = append(d, fmt.Sprintf("DirtyBytes() is %v, was %v", after.dirtyBytes, before.dirtyBytes))
	}
	if after.open {
		d = append(d, "the symlink has an FS.open entry")
	}
	if after.dirty != before.dirty {
		d = append(d, fmt.Sprintf("FS.dirty is %v, was %v", after.dirty, before.dirty))
	}
	if after.changes != before.changes {
		d = append(d, fmt.Sprintf("FS.changes is %d, was %d", after.changes, before.changes))
	}
	return strings.Join(d, "; ")
}

// nrCall is one call on a handle, made by whichever caller do is given: what
// it is, without its caller; the form the answer must take, for failure
// messages; and do, which makes it and reports whether it returned that form,
// and what it returned.
type nrCall struct {
	what string
	want string
	do   func(c vfs.Caller, what string) (ok bool, got string)
}

// nrReadCall is a Read of count bytes at off of h, which must return
// (nil, false, want), the form ADR 0029 section 9's facts give a refused Read.
func nrReadCall(e *uc43Env, fs *FS, h vfs.Handle, name string, off uint64, count uint32, want error,
	where string) nrCall {
	return nrCall{
		what: fmt.Sprintf("Read(%s, %d, %d)", name, off, count),
		want: fmt.Sprintf("(nil, false, %s)", nrStatus(want)),
		do: func(c vfs.Caller, what string) (bool, string) {
			r := pwRead(e, fs, c, h, off, count, what, where)
			return r.data == nil && !r.eof && r.err == want,
				fmt.Sprintf("(%d bytes %x, %v, %s)", len(r.data), r.data, r.eof, nrStatus(r.err))
		},
	}
}

// nrWriteCall is a Write of data at off of h with how, which must return
// (0, 0, want), the form ADR 0029 section 9's facts give a refused Write, a
// zero vfs.Stability being vfs.Unstable.
func nrWriteCall(e *uc43Env, fs *FS, h vfs.Handle, name string, off uint64, data []byte, how vfs.Stability,
	want error, where string) nrCall {
	return nrCall{
		what: fmt.Sprintf("Write(%s, %d, %d bytes, %s)", name, off, len(data), nrHow(how)),
		want: fmt.Sprintf("(0, 0, %s)", nrStatus(want)),
		do: func(c vfs.Caller, what string) (bool, string) {
			w := nrWrite(e, fs, c, h, off, data, how, what, where)
			return w.n == 0 && w.how == 0 && w.err == want, fmt.Sprintf("(%d, %s, %s)", w.n, nrHow(w.how),
				nrStatus(w.err))
		},
	}
}

// nrSetAttrCall is a SetAttr of h with sa, which must return the zero
// vfs.Attr and want, the form ADR 0029 section 9's facts give a refused
// SetAttr.
func nrSetAttrCall(e *uc43Env, fs *FS, h vfs.Handle, name string, sa vfs.SetAttr, want error,
	where string) nrCall {
	return nrCall{
		what: fmt.Sprintf("SetAttr(%s, %s)", name, sa24Describe(sa)),
		want: fmt.Sprintf("(the zero vfs.Attr, %s)", nrStatus(want)),
		do: func(c vfs.Caller, what string) (bool, string) {
			r := sa24SetAttr(e, fs, c, h, sa, what, where)
			attr := "the zero vfs.Attr"
			if r.v != (vfs.Attr{}) {
				attr = fmt.Sprintf("an Attr of type %d, mode %#o and size %d", r.v.Type, r.v.Mode, r.v.Size)
			}
			return r.v == (vfs.Attr{}) && r.err == want, fmt.Sprintf("(%s, %s)", attr, nrStatus(r.err))
		},
	}
}

// nrCheck makes call as nc and requires the form it must take; fatal says
// whether a wrong answer ends the test. It reports whether the answer was
// right.
func nrCheck(t *testing.T, call nrCall, nc nrCaller, fatal bool, why, where string) bool {
	t.Helper()
	what := nc.name + "'s " + call.what
	ok, got := call.do(nc.c, what)
	if ok {
		return true
	}
	msg := fmt.Sprintf("%s = %s, want %s: %s (%s)", what, got, call.want, why, where)
	if fatal {
		t.Fatal(msg)
	}
	t.Error(msg)
	return false
}

// nrSize is a size a doctoring sets, and the type the inode must have.
type nrSize struct {
	typ  vfs.FileType
	size uint64
}

// nrDoctor is ADR 0024 section 7's recipe, as ADR 0029 section 9's facts
// apply it to sizes: after a Sync, dg24Named on the inner *store.Local returns
// the root pointer and the decoded snapshot it names; for each inode number
// in sizes, the inode, which must be of the type given, gets the size given;
// encodeSnapshot encodes the snapshot, and the bytes are Put under
// rp.Snapshot on the inner store, leaving the root pointer as it is. The
// mount that wrote the snapshot must not be used again; a test mounts afresh.
func nrDoctor(e *uc43Env, sizes map[uint64]nrSize, what, where string) {
	e.t.Helper()
	ctx, local := e.ctx, e.h.inner
	err := uc43Go(e, func() error {
		rp, snap, err := dg24Named(ctx, local)
		if err != nil {
			return err
		}
		for id, s := range sizes {
			ino := snap.Inodes[id]
			if ino == nil {
				return fmt.Errorf("the snapshot %q holds no inode %d", rp.Snapshot, id)
			}
			if ino.Type != s.typ {
				return fmt.Errorf("inode %d in %q is of type %d, want %d", id, rp.Snapshot, ino.Type, s.typ)
			}
			ino.Size = s.size
		}
		body, err := encodeSnapshot(snap)
		if err != nil {
			return fmt.Errorf("encodeSnapshot: %w", err)
		}
		if err := local.Put(ctx, rp.Snapshot, body); err != nil {
			return fmt.Errorf("Put(%q): %w", rp.Snapshot, err)
		}
		return nil
	}).wait(e.t, what, where)
	if err != nil {
		e.t.Fatalf("setup: %s, by ADR 0024 section 7's recipe: %v (ADR 0029 section 9, the facts; %s)", what, err,
			where)
	}
}

// nrLinks are N5's and N6's symlinks, each to nrTarget, made by testCaller in
// the root: ln with no Mode, so 0o777; ln700 with Mode 0o700; and ln000 with
// Mode 0.
type nrLinks struct {
	ln, ln700, ln000 vfs.Handle
}

// nrMakeLinks makes nrLinks on fs, and requires each to be as ADR 0029
// section 9's facts give it.
func nrMakeLinks(e *uc43Env, fs *FS, root vfs.Handle, where string) nrLinks {
	e.t.Helper()
	var l nrLinks
	l.ln = e.symlink(fs, root, "ln", nrTarget, where)
	nrWantSymlink(e, fs, l.ln, "ln", 0o777, where)
	what := fmt.Sprintf("setup: testCaller's Symlink(root, %q, %q, {Mode 0o700})", "ln700", nrTarget)
	l.ln700 = pwMust(e, pwSymlink(e, fs, testCaller, root, "ln700", nrTarget, oaMode(0o700), what, where), what,
		where)
	nrWantSymlink(e, fs, l.ln700, "ln700", 0o700, where)
	what = fmt.Sprintf("setup: testCaller's Symlink(root, %q, %q, {Mode 0})", "ln000", nrTarget)
	l.ln000 = pwMust(e, pwSymlink(e, fs, testCaller, root, "ln000", nrTarget, oaMode(0), what, where), what, where)
	nrWantSymlink(e, fs, l.ln000, "ln000", 0, where)
	return l
}

// TestASymlinkIsNeitherReadNorWrittenNorGivenASize is N4 of ADR 0029 section 9
// (sections 1 and 2; the issue's first two done-whens).
//
// testCaller makes f, a regular file with 100 bytes, and ln, a symlink to
// "dir/target.txt", with no Mode, and Syncs. GetAttr of ln reports
// vfs.TypeLnk, mode 0o777 and size 14. Then each of these calls, by each of
// the four callers, returns vfs.ErrInval in the form the facts give:
// Write(ln, 0, 3 bytes, vfs.Unstable) and Write(ln, 100, 3 bytes,
// vfs.Unstable); SetAttr(ln, {Size: 0}), {Size: 14}, {Size: 15}, {Size:
// 12288} and {Size: 0, Mode: 0o600}; and Read(ln, 0, 10), Read(ln, 0, 0) and
// Read(ln, 1000, 10). After each, every attribute GetAttr reports, what
// ReadLink returns, DirtyBytes(), the absence of an FS.open entry, FS.dirty
// and FS.changes are as they were, and the harness recorded no store call.
// Then testCaller creates a file, so that the namespace is dirty, and its
// Write(ln, 0, 3 bytes, vfs.FileSync) returns (0, 0, vfs.ErrInval) with no
// store call recorded and FS.dirty still true: a refused stable write syncs
// nothing. Then Sync returns nil, and on a remount ln is a symlink of size 14
// whose target is unchanged, and f reads its 100 bytes. The first wrong answer
// ends the test.
func TestASymlinkIsNeitherReadNorWrittenNorGivenASize(t *testing.T) {
	const where = "N4, ADR 0029 sections 1, 2 and 9"
	const rule = "a Read, a Write of at least one byte and a SetAttr that sets a size are for a regular file, and " +
		"of an object that is neither a regular file nor a directory are refused with vfs.ErrInval, for every " +
		"caller, root included (ADR 0029 section 1, line 2; section 2)"
	e := uc43Setup(t)
	fs := e.newFSWith(nrConfig(e), where)
	root := e.root(fs, where)

	p := pwBytes(100, 7)
	f := e.create(fs, root, "f", where)
	e.write(fs, f, 0, p, `setup: testCaller's Write of 100 bytes at 0 of "f"`, where)
	ln := e.symlink(fs, root, "ln", nrTarget, where)
	if err := e.sync(fs, "setup: testCaller's Sync", where); err != nil {
		t.Fatalf("setup: Sync = %v, want nil against a healthy store (%s)", err, where)
	}
	nrWantSymlink(e, fs, ln, "ln", 0o777, where)

	before := nrCapture(e, fs, ln, "ln", "before the refused calls", where)
	if before.dirty {
		t.Fatalf("setup: FS.dirty is true after a Sync that returned nil, want false (ADR 0023 section 3; %s)", where)
	}
	mark := len(e.h.records())

	three := pwBytes(3, 13)
	calls := []nrCall{
		nrWriteCall(e, fs, ln, "ln", 0, three, vfs.Unstable, vfs.ErrInval, where),
		nrWriteCall(e, fs, ln, "ln", 100, three, vfs.Unstable, vfs.ErrInval, where),
		nrSetAttrCall(e, fs, ln, "ln", oaSize(0), vfs.ErrInval, where),
		nrSetAttrCall(e, fs, ln, "ln", oaSize(14), vfs.ErrInval, where),
		nrSetAttrCall(e, fs, ln, "ln", oaSize(15), vfs.ErrInval, where),
		nrSetAttrCall(e, fs, ln, "ln", oaSize(12288), vfs.ErrInval, where),
		nrSetAttrCall(e, fs, ln, "ln", vfs.SetAttr{Size: sa24U64(0), Mode: sa24U32(0o600)}, vfs.ErrInval, where),
		nrReadCall(e, fs, ln, "ln", 0, 10, vfs.ErrInval, where),
		nrReadCall(e, fs, ln, "ln", 0, 0, vfs.ErrInval, where),
		nrReadCall(e, fs, ln, "ln", 1000, 10, vfs.ErrInval, where),
	}
	for _, call := range calls {
		for _, nc := range nrCallers {
			nrCheck(t, call, nc, true, rule, where)
			what := nc.name + "'s " + call.what
			after := nrCapture(e, fs, ln, "ln", "after "+what, where)
			if d := nrDiff(before, after); d != "" {
				t.Fatalf("after %s: %s; want everything as it was: a refused call changes nothing (ADR 0029 section "+
					"2, \"What a refused call leaves\"; %s)", what, d, where)
			}
			if got := e.h.records()[mark:]; len(got) != 0 {
				t.Fatalf("the harness recorded %v since the setup's Sync, after %s, want no store call: a refused "+
					"call changes nothing and makes no store call (ADR 0029 section 2; %s)", got, what, where)
			}
		}
	}

	// A refused stable write syncs nothing.
	e.create(fs, root, "n4.bin", where)
	dirtyBefore := nrCapture(e, fs, ln, "ln", "after testCaller's Create of \"n4.bin\"", where)
	if !dirtyBefore.dirty {
		t.Fatalf("setup: FS.dirty is false after testCaller's Create of \"n4.bin\", want true: a creating call that "+
			"returns nil marks the namespace (ADR 0028 section 8, the facts; %s)", where)
	}
	mark = len(e.h.records())
	stable := nrWriteCall(e, fs, ln, "ln", 0, three, vfs.FileSync, vfs.ErrInval, where)
	nrCheck(t, stable, nrOwner, true, rule, where)
	if got := e.h.records()[mark:]; len(got) != 0 {
		t.Fatalf("the harness recorded %v during testCaller's %s on a dirty namespace, want no store call: a "+
			"refused stable write syncs nothing (ADR 0029 section 2; ADR 0007 section 4; %s)", got, stable.what, where)
	}
	after := nrCapture(e, fs, ln, "ln", "after testCaller's "+stable.what, where)
	if d := nrDiff(dirtyBefore, after); d != "" {
		t.Fatalf("after testCaller's %s: %s; want everything as it was, FS.dirty still true: a refused stable write "+
			"changes nothing and syncs nothing (ADR 0029 section 2; %s)", stable.what, d, where)
	}

	if err := e.sync(fs, "testCaller's Sync after the refused calls", where); err != nil {
		t.Fatalf("Sync = %v, want nil against a healthy store (%s)", err, where)
	}
	rm := e.remount(where)
	if rm == nil {
		t.FailNow()
	}
	rroot := e.root(rm, where)
	l := e.lookup(rm, rroot, "ln", where)
	if l.err != nil || l.attr.Type != vfs.TypeLnk || l.attr.Size != uint64(len(nrTarget)) {
		t.Fatalf("on a remount, Lookup of \"ln\" = (type %d, size %d, %s), want (type %d, size %d, nil): no refused "+
			"call changed the symlink (ADR 0029 section 2; %s)", l.attr.Type, l.attr.Size, nrStatus(l.err),
			vfs.TypeLnk, len(nrTarget), where)
	}
	if rl := nrReadLink(e, rm, l.h, "ReadLink of \"ln\" on a remount", where); rl.err != nil || rl.v != nrTarget {
		t.Fatalf("on a remount, ReadLink of \"ln\" = (%q, %s), want (%q, nil) (ADR 0029 section 2; %s)", rl.v,
			nrStatus(rl.err), nrTarget, where)
	}
	lf := e.lookup(rm, rroot, "f", where)
	if lf.err != nil {
		t.Fatalf("on a remount, Lookup of \"f\" = %s, want nil (%s)", nrStatus(lf.err), where)
	}
	r := pwRead(e, rm, testCaller, lf.h, 0, 100, "testCaller's Read(f, 0, 100) on a remount", where)
	if r.err != nil || !bytes.Equal(r.data, p) {
		t.Fatalf("on a remount, testCaller's Read(f, 0, 100) = (%d bytes, %s), want the 100 bytes written in setup "+
			"and nil (%s)", len(r.data), nrStatus(r.err), where)
	}
}

// TestTheTypeRefusalKeepsItsPlaceAmongTheChecks is N5 of ADR 0029 section 9
// (sections 1 and 2, the order of errors).
//
// On a writable mount testCaller makes ln, with no Mode; ln700 and ln000,
// symlinks made with Mode 0o700 and Mode 0; and gone, a symlink it then
// removes, keeping the handle.
//   - The type before the permission: sa24Other's and pwMember's Read, Write
//     and SetAttr of a size on ln700, and testCaller's on ln000, return
//     vfs.ErrInval, not vfs.ErrAcces.
//   - The size before the other attributes: testCaller's SetAttr(ln, {Size: 0,
//     UID: 0}) returns vfs.ErrInval, not vfs.ErrPerm.
//   - The arguments before the type, a characterisation: Write(ln,
//     MaxFileSize(), 1 byte, vfs.Unstable) and SetAttr(ln, {Size:
//     MaxFileSize() + 1}) return vfs.ErrFBig. No write is made that starts
//     just below the limit.
//   - The handle before the type, a characterisation: Read, Write and SetAttr
//     of a size on gone's handle return vfs.ErrStale.
//   - An empty write, a characterisation: Write(ln, 0, no bytes,
//     vfs.Unstable) returns (0, vfs.FileSync, nil).
//   - The mount gate before the type. After a Sync, on a read-only mount of
//     the bucket: Write(ln, ...) and SetAttr(ln, {Size: 0}) return
//     vfs.ErrROFS, a characterisation, and Read(ln, 0, 10) returns
//     vfs.ErrInval. On the writable mount, diverged as ADR 0025 section 6's A9
//     diverges one: the two changes return vfs.ErrStale, and the Read
//     vfs.ErrInval.
//
// Every call here is testCaller's unless it says otherwise.
func TestTheTypeRefusalKeepsItsPlaceAmongTheChecks(t *testing.T) {
	const where = "N5, ADR 0029 sections 1, 2 and 9"
	e := uc43Setup(t)
	fs := e.newFSWith(nrConfig(e), where)
	root := e.root(fs, where)
	l := nrMakeLinks(e, fs, root, where)
	gone := e.symlink(fs, root, "gone", nrTarget, where)
	if err := pwRemove(e, fs, testCaller, root, "gone", `setup: testCaller's Remove(root, "gone")`, where); err != nil {
		t.Fatalf("setup: testCaller's Remove(root, \"gone\") = %s, want nil (%s)", nrStatus(err), where)
	}
	three := pwBytes(3, 17)

	// The type before the permission.
	const typeFirst = "the type is checked before any permission: an object that is neither a regular file nor a " +
		"directory is refused with vfs.ErrInval whether or not the mode gives the caller the permission (ADR 0029 " +
		"section 1, line 2, and \"Why the type comes before the permission\")"
	byType := func(h vfs.Handle, name string) []nrCall {
		return []nrCall{
			nrReadCall(e, fs, h, name, 0, 10, vfs.ErrInval, where),
			nrWriteCall(e, fs, h, name, 0, three, vfs.Unstable, vfs.ErrInval, where),
			nrSetAttrCall(e, fs, h, name, oaSize(0), vfs.ErrInval, where),
		}
	}
	for _, nc := range []nrCaller{nrCallers[2], nrCallers[1]} {
		for _, call := range byType(l.ln700, "ln700") {
			nrCheck(t, call, nc, false, typeFirst+"; "+nc.name+" has no permission on ln700, 0o700", where)
		}
	}
	for _, call := range byType(l.ln000, "ln000") {
		nrCheck(t, call, nrOwner, false, typeFirst+"; the owner bits of ln000, 0, give its owner nothing", where)
	}

	// The size before the other attributes.
	nrCheck(t, nrSetAttrCall(e, fs, l.ln, "ln", vfs.SetAttr{Size: sa24U64(0), UID: sa24U32(0)}, vfs.ErrInval, where),
		nrOwner, false, "a SetAttr that sets a size and other attributes answers vfs.ErrInval, whatever the other "+
			"attributes would answer, and applies none of them; here the uid 0 alone would be vfs.ErrPerm (ADR 0029 "+
			"section 2; ADR 0024 section 2)", where)

	// The arguments before the type. The cheapest over-limit values, and a
	// wrong answer ends the test (ADR 0007 section 7).
	limit := nrMaxFileSize(e, fs, where)
	if limit != mfsMax {
		t.Fatalf("setup: MaxFileSize() = %d, want %d: a mount made with uc43Config has 4096-byte chunks (ADR 0029 "+
			"section 9, the facts; ADR 0007 section 1; %s)", limit, mfsMax, where)
	}
	const fbigFirst = "vfs.ErrFBig reads the arguments alone and comes before the handle is resolved, and so before " +
		"the type (ADR 0029 section 2; ADR 0007 section 4, Assumption 7)"
	nrCheck(t, nrWriteCall(e, fs, l.ln, "ln", limit, []byte{1}, vfs.Unstable, vfs.ErrFBig, where), nrOwner, true,
		fbigFirst, where)
	nrCheck(t, nrSetAttrCall(e, fs, l.ln, "ln", oaSize(limit+1), vfs.ErrFBig, where), nrOwner, true, fbigFirst,
		where)

	// The handle before the type.
	const staleFirst = "a handle whose symlink has been removed answers vfs.ErrStale, at stage C, before the type " +
		"is looked at (ADR 0029 section 2; internal/vfs/vfs.go, Remove)"
	nrCheck(t, nrReadCall(e, fs, gone, "gone", 0, 10, vfs.ErrStale, where), nrOwner, false, staleFirst, where)
	nrCheck(t, nrWriteCall(e, fs, gone, "gone", 0, three, vfs.Unstable, vfs.ErrStale, where), nrOwner, false,
		staleFirst, where)
	nrCheck(t, nrSetAttrCall(e, fs, gone, "gone", oaSize(0), vfs.ErrStale, where), nrOwner, false, staleFirst,
		where)

	// An empty write.
	what := "testCaller's Write(ln, 0, no bytes, vfs.Unstable)"
	w := nrWrite(e, fs, testCaller, l.ln, 0, []byte{}, vfs.Unstable, what, where)
	if w.n != 0 || w.how != vfs.FileSync || w.err != nil {
		t.Errorf("%s = (%d, %s, %s), want (0, vfs.FileSync, nil): a Write of no bytes returns after stage A, "+
			"whatever its handle names (ADR 0029 section 2, Assumption 9; ADR 0007 section 4; %s)", what, w.n,
			nrHow(w.how), nrStatus(w.err), where)
	}

	if a := sa24GetAttr(e, fs, l.ln, "GetAttr of \"ln\" after the calls above", where); a.Size != uint64(len(nrTarget)) {
		t.Errorf("after the calls above, ln's size is %d, want %d: none of them changes a symlink (ADR 0029 section "+
			"2; %s)", a.Size, len(nrTarget), where)
	}

	// The mount gate before the type: a read-only mount.
	if err := e.sync(fs, "testCaller's Sync before the read-only mount", where); err != nil {
		t.Fatalf("Sync = %v, want nil against a healthy store (%s)", err, where)
	}
	cfg := nrConfig(e)
	cfg.ReadOnly = true
	rofs := e.newFSWith(cfg, where+", the read-only mount")
	lr := e.lookup(rofs, e.root(rofs, where), "ln", where)
	if lr.err != nil {
		t.Fatalf("on the read-only mount, Lookup of \"ln\" = %s, want nil: the Sync committed it (%s)",
			nrStatus(lr.err), where)
	}
	const rofsFirst = "a read-only mount takes no change: Write and SetAttr answer vfs.ErrROFS at stage A, before " +
		"the type (ADR 0029 section 2; ADR 0011 section 4)"
	nrCheck(t, nrWriteCall(e, rofs, lr.h, "ln, on the read-only mount", 0, three, vfs.Unstable, vfs.ErrROFS, where),
		nrOwner, false, rofsFirst, where)
	nrCheck(t, nrSetAttrCall(e, rofs, lr.h, "ln, on the read-only mount", oaSize(0), vfs.ErrROFS, where), nrOwner,
		false, rofsFirst, where)
	nrCheck(t, nrReadCall(e, rofs, lr.h, "ln, on the read-only mount", 0, 10, vfs.ErrInval, where), nrOwner, false,
		"Read has no stage A, so a read-only mount refuses a symlink's Read for its type, as any mount does (ADR "+
			"0029 section 2)", where)

	// The mount gate before the type: the writable mount, diverged as ADR 0025
	// section 6's A9 diverges one. The read-only mount committed nothing.
	epoch := e.read(fs, "reading FS.epoch after the Sync", where).epoch
	if _, err := uc43WriteForeignRoot(e.ctx, e.h.inner, epoch+1, uc43Writer); err != nil {
		t.Fatalf("setup: writing a foreign root pointer on the inner store: %v (%s)", err, where)
	}
	if err := e.commit(fs, "c2", where); err == nil || !strings.Contains(err.Error(), "commit conflict") {
		t.Fatalf("setup: the Sync of the commit of c2, after another writer moved the root pointer, = %v, want an "+
			"error whose text contains %q (ADR 0025 section 6, A9; %s)", err, "commit conflict", where)
	}
	if got := e.mode(fs, where); got != mountDiverged {
		t.Fatalf("setup: after the commit of c2, f.mode.State() = %v, want %v (ADR 0025 section 6, A9; %s)", got,
			mountDiverged, where)
	}
	const staleGate = "a diverged mount takes no change: Write and SetAttr answer vfs.ErrStale at stage A, before " +
		"the type (ADR 0029 section 2; ADR 0014 section 7)"
	nrCheck(t, nrWriteCall(e, fs, l.ln, "ln, on the diverged mount", 0, three, vfs.Unstable, vfs.ErrStale, where),
		nrOwner, false, staleGate, where)
	nrCheck(t, nrSetAttrCall(e, fs, l.ln, "ln, on the diverged mount", oaSize(0), vfs.ErrStale, where), nrOwner,
		false, staleGate, where)
	nrCheck(t, nrReadCall(e, fs, l.ln, "ln, on the diverged mount", 0, 10, vfs.ErrInval, where), nrOwner, false,
		"Read has no stage A, so a diverged mount refuses a symlink's Read for its type, as any mount does (ADR "+
			"0029 section 2)", where)
}

// TestAccessStillReportsASymlinksMode is N6 of ADR 0029 section 9 (section
// 3), a characterisation that passes before the change and after, pinned so
// that a change which carried ADR 0029 section 1's line 2 into an advisory
// request fails a test that names the decision.
//
// With nrMakeLinks' ln, ln700 and ln000, as N5 makes them, Access with every
// bit asked gives exactly section 3's table, for the four callers:
//
//	ln, 0o777:     R M E X for each of the four;
//	ln700, 0o700:  R M E X for the owner and root, nothing for the others;
//	ln000, 0:      nothing for the owner and the others, R M E for root.
//
// sa24Other's Access of ln, asked one bit at a time, returns that bit for R,
// M, E and X, and 0 for L and D. On a read-only mount of the bucket every
// caller is given R and X on ln.
func TestAccessStillReportsASymlinksMode(t *testing.T) {
	const where = "N6, ADR 0029 sections 3 and 9"
	const rule = "ACCESS reports an object that is neither a regular file nor a directory by its caller's class of " +
		"the mode bits: R with read, M and E with write on a mount that takes changes, X with execute, and never L " +
		"or D (ADR 0029 section 3; ADR 0027 section 6)"
	e := uc43Setup(t)
	fs := e.newFSWith(nrConfig(e), where)
	root := e.root(fs, where)
	l := nrMakeLinks(e, fs, root, where)

	R, L, M, E, D, X := vfs.AccessRead, vfs.AccessLookup, vfs.AccessModify, vfs.AccessExtend, vfs.AccessDelete,
		vfs.AccessExecute
	rmex := R | M | E | X
	rows := []struct {
		name string
		h    vfs.Handle
		want [4]uint32 // in nrCallers' order: the owner, the group member, neither, root
	}{
		{"ln, 0o777", l.ln, [4]uint32{rmex, rmex, rmex, rmex}},
		{"ln700, 0o700", l.ln700, [4]uint32{rmex, 0, 0, rmex}},
		{"ln000, 0", l.ln000, [4]uint32{0, 0, 0, R | M | E}},
	}
	for _, row := range rows {
		for i, nc := range nrCallers {
			what := fmt.Sprintf("%s's Access(%s, every bit)", nc.name, row.name)
			got := pwAccess(e, fs, nc.c, row.h, pwAllAccess, what, where)
			if got.err != nil || got.v != row.want[i] {
				t.Errorf("%s = (%s, %s), want (%s, nil): %s (%s)", what, nrMask(got.v), nrStatus(got.err),
					nrMask(row.want[i]), rule, where)
			}
		}
	}

	for _, bit := range []uint32{R, L, M, E, D, X} {
		want := bit
		if bit == L || bit == D {
			want = 0
		}
		what := fmt.Sprintf("sa24Other's Access(ln, %s)", nrMask(bit))
		got := pwAccess(e, fs, sa24Other, l.ln, bit, what, where)
		if got.err != nil || got.v != want {
			t.Errorf("%s = (%s, %s), want (%s, nil): %s (%s)", what, nrMask(got.v), nrStatus(got.err), nrMask(want),
				rule, where)
		}
	}

	if err := e.sync(fs, "testCaller's Sync before the read-only mount", where); err != nil {
		t.Fatalf("Sync = %v, want nil against a healthy store (%s)", err, where)
	}
	cfg := nrConfig(e)
	cfg.ReadOnly = true
	rofs := e.newFSWith(cfg, where+", the read-only mount")
	lr := e.lookup(rofs, e.root(rofs, where), "ln", where)
	if lr.err != nil {
		t.Fatalf("on the read-only mount, Lookup of \"ln\" = %s, want nil: the Sync committed it (%s)",
			nrStatus(lr.err), where)
	}
	for _, nc := range nrCallers {
		what := fmt.Sprintf("%s's Access(ln, every bit) on the read-only mount", nc.name)
		got := pwAccess(e, rofs, nc.c, lr.h, pwAllAccess, what, where)
		if got.err != nil || got.v != R|X {
			t.Errorf("%s = (%s, %s), want (%s, nil): a mount that takes no change withholds M and E, whatever the "+
				"mode (ADR 0029 section 3; ADR 0027 sections 4 and 6; %s)", what, nrMask(got.v), nrStatus(got.err),
				nrMask(R|X), where)
		}
	}
}

// TestTheMountRecordCountsRegularFilesOnly is N7 of ADR 0029 section 9
// (section 6; ADR 0007 section 6; the issue's third done-when), a
// characterisation that passes before the change and after.
//
// testCaller makes ln, a symlink; d, a directory; big.bin, an empty regular
// file; and small.bin, with 10 bytes; and Syncs. The first doctoring sets ln's
// size to mfsMax + 7 and d's to mfsMax + 9. A mount of the bucket, with a
// capturing logger, logs no record with the message mfsMsgLarger, and GetAttr
// reports those two sizes. The second doctoring sets big.bin's size to
// mfsMax + 1. A mount then logs exactly one such record, at slog.LevelWarn,
// whose count is 1, whose largest is mfsMax + 1 and whose max_file_size is
// mfsMax, each of the kind ADR 0007 section 6 gives. A record that counted
// every inode would have a count of 3 and a largest of mfsMax + 9. No size
// here builds a chunk list: a regular file given a size with no chunk list is
// a sparse file.
func TestTheMountRecordCountsRegularFilesOnly(t *testing.T) {
	const where = "N7, ADR 0029 sections 6 and 9; ADR 0007 section 6"
	e := uc43Setup(t)

	var lnID, dID, bigID uint64
	func() {
		// The first mount lives only in this scope, so that nothing uses it
		// after the doctoring (ADR 0024 section 7; ADR 0011 section 7).
		first := e.newFSWith(nrConfig(e), where+", the first mount")
		root := e.root(first, where)
		ln := e.symlink(first, root, "ln", nrTarget, where)
		d := e.mkdir(first, root, "d", where)
		big := e.create(first, root, "big.bin", where)
		small := e.create(first, root, "small.bin", where)
		e.write(first, small, 0, pwBytes(10, 29), `setup: testCaller's Write of 10 bytes at 0 of "small.bin"`, where)
		if err := e.sync(first, "setup: testCaller's Sync on the first mount", where); err != nil {
			t.Fatalf("setup: Sync = %v, want nil against a healthy store (%s)", err, where)
		}
		lnID = sa24GetAttr(e, first, ln, `setup: GetAttr of "ln"`, where).FileID
		dID = sa24GetAttr(e, first, d, `setup: GetAttr of "d"`, where).FileID
		bigID = sa24GetAttr(e, first, big, `setup: GetAttr of "big.bin"`, where).FileID
	}()

	nrDoctor(e, map[uint64]nrSize{
		lnID: {typ: vfs.TypeLnk, size: mfsMax + 7},
		dID:  {typ: vfs.TypeDir, size: mfsMax + 9},
	}, "the first doctoring, of ln's and d's sizes", where)

	cA := newBTCapture(false)
	cfgA := nrConfig(e)
	cfgA.Log = cA.logger()
	a := e.newFSWith(cfgA, where+", the mount after the first doctoring")
	if recs := cA.withMessage(mfsMsgLarger); len(recs) != 0 {
		t.Errorf("New of a bucket holding a symlink and a directory above the limit, and no regular file above it, "+
			"logged %d %q records, want none: the record counts regular files only (ADR 0007 section 6; ADR 0029 "+
			"section 6; %s)", len(recs), mfsMsgLarger, where)
	}
	aroot := e.root(a, where)
	for _, s := range []struct {
		name string
		size uint64
	}{{"ln", mfsMax + 7}, {"d", mfsMax + 9}} {
		lk := e.lookup(a, aroot, s.name, where)
		if lk.err != nil {
			t.Fatalf("after the first doctoring, Lookup of %q = %s, want nil (%s)", s.name, nrStatus(lk.err), where)
		}
		if got := sa24GetAttr(e, a, lk.h, fmt.Sprintf("GetAttr of %q after the first doctoring", s.name),
			where); got.Size != s.size {
			t.Errorf("after the first doctoring, GetAttr of %q reports size %d, want %d: the snapshot decodes and "+
				"mounts, and nothing checks an inode's size at load (ADR 0029 section 9, the facts; %s)", s.name,
				got.Size, s.size, where)
		}
	}

	// The mount above only read, so it committed nothing, and is not used
	// again.
	nrDoctor(e, map[uint64]nrSize{
		bigID: {typ: vfs.TypeReg, size: mfsMax + 1},
	}, "the second doctoring, of big.bin's size", where)

	cB := newBTCapture(false)
	cfgB := nrConfig(e)
	cfgB.Log = cB.logger()
	b := e.newFSWith(cfgB, where+", the mount after the second doctoring")
	recs := cB.withMessage(mfsMsgLarger)
	if len(recs) != 1 {
		t.Errorf("New of a bucket holding one regular file above the limit, beside a symlink and a directory above "+
			"it, logged %d %q records, want exactly 1 (ADR 0007 section 6; %s)", len(recs), mfsMsgLarger, where)
	}
	if len(recs) > 0 {
		rec := recs[0]
		if rec.Level != slog.LevelWarn {
			t.Errorf("the %q record is logged at %v, want %v (ADR 0007 section 6; %s)", mfsMsgLarger, rec.Level,
				slog.LevelWarn, where)
		}
		if v, ok := mfsWantKind(t, rec, "count", slog.KindInt64); ok && v.Int64() != 1 {
			t.Errorf("the %q record's count = %d, want 1: it counts the regular file big.bin alone, and not the "+
				"symlink or the directory above the limit; a record that counted every inode would have 3 (ADR 0007 "+
				"section 6; ADR 0029 section 6; %s)", mfsMsgLarger, v.Int64(), where)
		}
		if v, ok := mfsWantKind(t, rec, "largest", slog.KindUint64); ok && v.Uint64() != mfsMax+1 {
			t.Errorf("the %q record's largest = %d, want %d, big.bin's size; a record that counted every inode "+
				"would have %d, the directory's (ADR 0007 section 6; ADR 0029 section 6; %s)", mfsMsgLarger,
				v.Uint64(), mfsMax+1, mfsMax+9, where)
		}
		if v, ok := mfsWantKind(t, rec, "max_file_size", slog.KindUint64); ok && v.Uint64() != mfsMax {
			t.Errorf("the %q record's max_file_size = %d, want %d (ADR 0007 section 6; %s)", mfsMsgLarger,
				v.Uint64(), mfsMax, where)
		}
	}
	lb := e.lookup(b, e.root(b, where), "big.bin", where)
	if lb.err != nil || lb.attr.Size != mfsMax+1 {
		t.Errorf("after the second doctoring, Lookup of \"big.bin\" = (size %d, %s), want (size %d, nil) (ADR 0029 "+
			"section 9, the facts; %s)", lb.attr.Size, nrStatus(lb.err), mfsMax+1, where)
	}
}

// TestASymlinkAnEarlierBuildResizedIsRefusedAsAnyOther is N8 of ADR 0029
// section 9 (section 5).
//
// ln is made to "dir/target.txt" and committed, and a doctoring sets its size
// to 100, standing for a symlink an earlier build resized. On a mount of the
// bucket: GetAttr reports vfs.TypeLnk and size 100; ReadLink returns the
// target; Read(ln, 0, 10), Write(ln, 0, 3 bytes, vfs.Unstable) and
// SetAttr(ln, {Size: 14}) return vfs.ErrInval, and the size is still 100 with
// FS.dirty false; Remove of ln returns nil; and a Symlink of the same name to
// the same target reports size 14, as it does on a remount after a Sync.
// Every call is testCaller's.
func TestASymlinkAnEarlierBuildResizedIsRefusedAsAnyOther(t *testing.T) {
	const where = "N8, ADR 0029 sections 5 and 9"
	const rule = "a symlink an earlier build resized answers vfs.ErrInval to a Read, a Write and a SetAttr that " +
		"sets a size, whatever size is asked for, the target's length included (ADR 0029 section 5)"
	e := uc43Setup(t)

	var lnID uint64
	func() {
		// The first mount lives only in this scope, so that nothing uses it
		// after the doctoring (ADR 0024 section 7; ADR 0011 section 7).
		first := e.newFSWith(nrConfig(e), where+", the first mount")
		root := e.root(first, where)
		ln := e.symlink(first, root, "ln", nrTarget, where)
		if err := e.sync(first, "setup: testCaller's Sync on the first mount", where); err != nil {
			t.Fatalf("setup: Sync = %v, want nil against a healthy store (%s)", err, where)
		}
		lnID = nrWantSymlink(e, first, ln, "ln", 0o777, where).FileID
	}()
	nrDoctor(e, map[uint64]nrSize{lnID: {typ: vfs.TypeLnk, size: 100}}, "the doctoring of ln's size to 100", where)

	fs := e.newFSWith(nrConfig(e), where+", the mount after the doctoring")
	root := e.root(fs, where)
	lk := e.lookup(fs, root, "ln", where)
	if lk.err != nil {
		t.Fatalf("after the doctoring, Lookup of \"ln\" = %s, want nil (%s)", nrStatus(lk.err), where)
	}
	ln := lk.h
	if a := sa24GetAttr(e, fs, ln, "GetAttr of \"ln\" after the doctoring", where); a.Type != vfs.TypeLnk ||
		a.Size != 100 {
		t.Fatalf("after the doctoring, GetAttr of \"ln\" = (type %d, size %d), want (type %d, size 100): such a "+
			"symlink is mounted, and reports the size the earlier call left (ADR 0029 section 5; %s)", a.Type,
			a.Size, vfs.TypeLnk, where)
	}
	if rl := nrReadLink(e, fs, ln, "ReadLink of \"ln\" after the doctoring", where); rl.err != nil ||
		rl.v != nrTarget {
		t.Errorf("after the doctoring, ReadLink of \"ln\" = (%q, %s), want (%q, nil): READLINK returns its target, "+
			"which no earlier build changed (ADR 0029 section 5; %s)", rl.v, nrStatus(rl.err), nrTarget, where)
	}

	three := pwBytes(3, 31)
	for _, call := range []nrCall{
		nrReadCall(e, fs, ln, "ln", 0, 10, vfs.ErrInval, where),
		nrWriteCall(e, fs, ln, "ln", 0, three, vfs.Unstable, vfs.ErrInval, where),
		nrSetAttrCall(e, fs, ln, "ln", oaSize(14), vfs.ErrInval, where),
	} {
		nrCheck(t, call, nrOwner, false, rule, where)
	}
	if a := sa24GetAttr(e, fs, ln, "GetAttr of \"ln\" after the refused calls", where); a.Size != 100 {
		t.Errorf("after the refused calls, ln's size is %d, want 100: its size cannot be put right through the "+
			"mount, and a refused call changes nothing (ADR 0029 sections 2 and 5; %s)", a.Size, where)
	}
	if f := sa24Read(e, fs, "reading FS.dirty after the refused calls", where); f.dirty {
		t.Errorf("after the refused calls, FS.dirty is true, want false: a refused call marks nothing (ADR 0029 "+
			"section 2; %s)", where)
	}

	if err := pwRemove(e, fs, testCaller, root, "ln", `testCaller's Remove(root, "ln")`, where); err != nil {
		t.Fatalf("testCaller's Remove(root, \"ln\") = %s, want nil: such a symlink can be removed (ADR 0029 section "+
			"5; %s)", nrStatus(err), where)
	}
	what := fmt.Sprintf("testCaller's Symlink(root, %q, %q, {})", "ln", nrTarget)
	made := pwSymlink(e, fs, testCaller, root, "ln", nrTarget, vfs.SetAttr{}, what, where)
	if made.err != nil || made.a.Type != vfs.TypeLnk || made.a.Size != uint64(len(nrTarget)) {
		t.Fatalf("%s = (type %d, size %d, %s), want (type %d, size %d, nil): removing such a symlink and making it "+
			"again gives a symlink whose size is its target's length (ADR 0029 section 5; %s)", what, made.a.Type,
			made.a.Size, nrStatus(made.err), vfs.TypeLnk, len(nrTarget), where)
	}
	nrWantSymlink(e, fs, made.h, "ln, made again", 0o777, where)

	if err := e.sync(fs, "testCaller's Sync after the Symlink", where); err != nil {
		t.Fatalf("Sync = %v, want nil against a healthy store (%s)", err, where)
	}
	rm := e.remount(where)
	if rm == nil {
		t.FailNow()
	}
	rl := e.lookup(rm, e.root(rm, where), "ln", where)
	if rl.err != nil || rl.attr.Type != vfs.TypeLnk || rl.attr.Size != uint64(len(nrTarget)) {
		t.Errorf("on a remount, Lookup of \"ln\" = (type %d, size %d, %s), want (type %d, size %d, nil) (ADR 0029 "+
			"section 5; %s)", rl.attr.Type, rl.attr.Size, nrStatus(rl.err), vfs.TypeLnk, len(nrTarget), where)
	}
	if rl.err == nil {
		if got := nrReadLink(e, rm, rl.h, "ReadLink of \"ln\" on a remount", where); got.err != nil ||
			got.v != nrTarget {
			t.Errorf("on a remount, ReadLink of \"ln\" = (%q, %s), want (%q, nil) (ADR 0029 section 5; %s)", got.v,
				nrStatus(got.err), nrTarget, where)
		}
	}
}
