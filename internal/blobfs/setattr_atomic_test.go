package blobfs

// Clean-room tests for #143, "a refused SETATTR keeps part of its change,
// uncommitted", written without reading the implementation, from ADR 0024
// (docs/adr/0024-every-namespace-change-is-marked-dirty-and-a-refused-setattr-makes-none.md)
// sections 1, 2 and 7, the facts and harness of ADR 0023 section 11 and ADR
// 0022 section 8, the safety rules of ADR 0011 section 7, the order of errors
// of ADR 0007 section 4, ADR 0012 section 2 (a refused size change never waits
// for the commit gate), and internal/vfs/vfs.go.
//
// ADR 0024 section 2's contract: SetAttr applies every attribute sa sets, in
// one hold of FS.mu for writing, or applies none. A call that returns an error
// has changed no attribute, no size, no time and no chunk list, and has left
// FS.dirty and FS.changes as they were, so the next Sync with nothing else
// dirty makes no store call. A call that returns nil has applied all of sa. An
// empty call applies nothing and marks nothing. A call refused for any
// attribute, with a size or without, is refused before it takes the commit
// gate.
//
// Covered, with the names section 7 pins:
//   - S1, TestARefusedSetAttrAppliesNothing (the issue's example);
//   - S2, TestARefusedSetAttrWithASizeAppliesNothing;
//   - S3, TestSetAttrRefusesInTheOrderSizeModeOwnerGroupTimes (a
//     characterisation);
//   - S6, TestARefusedSetAttrWithASizeDoesNotWaitForTheCommitGate.
//
// S4 and S5, which need FS.perm, belong to setattr_policy_test.go and are not
// here: this file uses only names that exist at f8398c4, so that the package
// builds there and a red run can be taken (ADR 0024 section 7, Assumption 14).
//
// S1, S2 and S3 run on ADR 0023 section 11's harness, uc43Store, unarmed, as a
// pass-through recorder of every store call; S6 runs on the harness of
// commit_window_test.go and backpressure_test.go, exactly as steps 1 to 3 of
// TestCommitWindowTruncateDuringSettleIsWholeOrAbsent use it. The safety rules
// (ADR 0023 section 11; ADR 0011 section 7): every bucket is under the test's
// t.TempDir(); in S1 to S3 every New and every FS call runs in uc43Go, which
// recovers a panic, and is waited for under uc43Bound, 10 s; S6's calls run in
// bpGo under btHangBound, 10 s, as that harness does; every hold is opened by a
// cleanup; nothing sleeps or polls, and nothing is parallel; FS.dirty and
// FS.changes are read only holding FS.mu for reading, inside a goroutine that
// recovers a panic, and no field is written.
//
// Not covered, on purpose: GetAttr's Used, which no SetAttr here sets and
// whose value the spec does not pin; the requests made of the policy, which
// are S4's; the exclusive hold's second check, which under Unix() only a
// change made between the two checks can reach (ADR 0024 section 2,
// Assumption 19), and which S4 (iii) and S7 in setattr_policy_test.go reach
// with a policy that refuses it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"strata/internal/vfs"
)

// sa24Other is ADR 0024 section 7's caller who is neither the owner nor a
// member of the group of testCaller's files, and has read permission only on
// one of mode 0o644.
var sa24Other = vfs.Caller{UID: 502, GID: 30}

// sa24B is the name of S6's file B.
const sa24B = "b.bin"

func sa24U32(v uint32) *uint32        { return &v }
func sa24U64(v uint64) *uint64        { return &v }
func sa24Time(v time.Time) *time.Time { return &v }

// sa24Times returns t1 and t2: two distinct times in the past, a second apart,
// each a whole second so that no store of times can round them.
func sa24Times() (time.Time, time.Time) {
	t1 := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	return t1, t1.Add(time.Second)
}

// sa24Describe spells the fields sa sets, for failure messages.
func sa24Describe(sa vfs.SetAttr) string {
	var parts []string
	if sa.Size != nil {
		parts = append(parts, fmt.Sprintf("Size %d", *sa.Size))
	}
	if sa.Mode != nil {
		parts = append(parts, fmt.Sprintf("Mode %#o", *sa.Mode))
	}
	if sa.UID != nil {
		parts = append(parts, fmt.Sprintf("UID %d", *sa.UID))
	}
	if sa.GID != nil {
		parts = append(parts, fmt.Sprintf("GID %d", *sa.GID))
	}
	if sa.ATime != nil {
		parts = append(parts, "ATime "+sa.ATime.Format(time.RFC3339Nano))
	}
	if sa.MTime != nil {
		parts = append(parts, "MTime "+sa.MTime.Format(time.RFC3339Nano))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// sa24Diff describes how after differs from before in every attribute GetAttr
// reports but Used, or returns "".
func sa24Diff(before, after vfs.Attr) string {
	var d []string
	if after.Type != before.Type {
		d = append(d, fmt.Sprintf("Type %d, was %d", after.Type, before.Type))
	}
	if after.Mode != before.Mode {
		d = append(d, fmt.Sprintf("Mode %#o, was %#o", after.Mode, before.Mode))
	}
	if after.NLink != before.NLink {
		d = append(d, fmt.Sprintf("NLink %d, was %d", after.NLink, before.NLink))
	}
	if after.UID != before.UID {
		d = append(d, fmt.Sprintf("UID %d, was %d", after.UID, before.UID))
	}
	if after.GID != before.GID {
		d = append(d, fmt.Sprintf("GID %d, was %d", after.GID, before.GID))
	}
	if after.Size != before.Size {
		d = append(d, fmt.Sprintf("Size %d, was %d", after.Size, before.Size))
	}
	if after.FileID != before.FileID {
		d = append(d, fmt.Sprintf("FileID %d, was %d", after.FileID, before.FileID))
	}
	if !after.ATime.Equal(before.ATime) {
		d = append(d, fmt.Sprintf("ATime %s, was %s", after.ATime.Format(time.RFC3339Nano),
			before.ATime.Format(time.RFC3339Nano)))
	}
	if !after.MTime.Equal(before.MTime) {
		d = append(d, fmt.Sprintf("MTime %s, was %s", after.MTime.Format(time.RFC3339Nano),
			before.MTime.Format(time.RFC3339Nano)))
	}
	if !after.CTime.Equal(before.CTime) {
		d = append(d, fmt.Sprintf("CTime %s, was %s", after.CTime.Format(time.RFC3339Nano),
			before.CTime.Format(time.RFC3339Nano)))
	}
	return strings.Join(d, "; ")
}

// sa24Unapplied describes every attribute sa sets that got does not hold, or
// returns "".
func sa24Unapplied(got vfs.Attr, sa vfs.SetAttr) string {
	var d []string
	if sa.Size != nil && got.Size != *sa.Size {
		d = append(d, fmt.Sprintf("Size %d, want %d", got.Size, *sa.Size))
	}
	if sa.Mode != nil && got.Mode != *sa.Mode {
		d = append(d, fmt.Sprintf("Mode %#o, want %#o", got.Mode, *sa.Mode))
	}
	if sa.UID != nil && got.UID != *sa.UID {
		d = append(d, fmt.Sprintf("UID %d, want %d", got.UID, *sa.UID))
	}
	if sa.GID != nil && got.GID != *sa.GID {
		d = append(d, fmt.Sprintf("GID %d, want %d", got.GID, *sa.GID))
	}
	if sa.ATime != nil && !got.ATime.Equal(*sa.ATime) {
		d = append(d, fmt.Sprintf("ATime %s, want %s", got.ATime.Format(time.RFC3339Nano),
			sa.ATime.Format(time.RFC3339Nano)))
	}
	if sa.MTime != nil && !got.MTime.Equal(*sa.MTime) {
		d = append(d, fmt.Sprintf("MTime %s, want %s", got.MTime.Format(time.RFC3339Nano),
			sa.MTime.Format(time.RFC3339Nano)))
	}
	return strings.Join(d, "; ")
}

// sa24Fields is what sa24Read reads of an FS.
type sa24Fields struct {
	dirty   bool
	changes uint64
}

// sa24Read reads FS.dirty and FS.changes in one hold of FS.mu for reading,
// inside uc43Go (ADR 0022 section 8; ADR 0023 section 11).
func sa24Read(e *uc43Env, fs *FS, what, where string) sa24Fields {
	e.t.Helper()
	return uc43Go(e, func() sa24Fields {
		fs.mu.RLock()
		defer fs.mu.RUnlock()
		return sa24Fields{dirty: fs.dirty, changes: fs.changes}
	}).wait(e.t, what, where)
}

// sa24SetAttr runs fs.SetAttr of h, as c, with sa.
func sa24SetAttr(e *uc43Env, fs *FS, c vfs.Caller, h vfs.Handle, sa vfs.SetAttr, what, where string) uc43R[vfs.Attr] {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() uc43R[vfs.Attr] {
		a, err := fs.SetAttr(ctx, c, h, sa)
		return uc43R[vfs.Attr]{a, err}
	}).wait(e.t, what, where)
}

// sa24GetAttr runs fs.GetAttr of h, which must succeed.
func sa24GetAttr(e *uc43Env, fs *FS, h vfs.Handle, what, where string) vfs.Attr {
	e.t.Helper()
	ctx := e.ctx
	r := uc43Go(e, func() uc43R[vfs.Attr] {
		a, err := fs.GetAttr(ctx, h)
		return uc43R[vfs.Attr]{a, err}
	}).wait(e.t, what, where)
	if r.err != nil {
		e.t.Fatalf("%s = %v, want success (%s)", what, r.err, where)
	}
	return r.v
}

// sa24ReadResult is what one Read returned.
type sa24ReadResult struct {
	data []byte
	eof  bool
	err  error
}

// sa24ReadOnce runs one fs.Read of count bytes at off of h, as testCaller.
func sa24ReadOnce(e *uc43Env, fs *FS, h vfs.Handle, off uint64, count uint32, what, where string) sa24ReadResult {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() sa24ReadResult {
		data, eof, err := fs.Read(ctx, testCaller, h, off, count)
		return sa24ReadResult{data, eof, err}
	}).wait(e.t, what, where)
}

// sa24Env is one S1 or S2 case's state: a mount on the harness over a fresh
// bucket, testCaller's file on it, committed, and what was true right after
// that commit.
type sa24Env struct {
	e      *uc43Env
	fs     *FS
	name   string
	h      vfs.Handle
	data   []byte
	before vfs.Attr // GetAttr of the file after the commit
	c0     uint64   // FS.changes after the commit
	mark   int      // the harness's record length when the commit's Sync returned
}

// sa24Setup is S1's and S2's starting state (ADR 0024 section 7): New over an
// empty bucket behind an unarmed uc43Store; testCaller creates name, and, if
// data is not nil, writes it at 0, vfs.Unstable; Sync. It reads FS.changes,
// c0, and the file's attributes, which must be a regular file of mode 0o644
// owned by 501:20 of len(data) bytes, and FS.dirty, which must be false.
func sa24Setup(t *testing.T, name string, data []byte, where string) *sa24Env {
	t.Helper()
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	h := e.create(fs, root, name, where)
	if data != nil {
		e.write(fs, h, 0, data, fmt.Sprintf("Write of %d bytes at 0 of %q, vfs.Unstable", len(data), name), where)
	}
	if err := e.sync(fs, fmt.Sprintf("the Sync that commits %q", name), where); err != nil {
		t.Fatalf("setup: the Sync that commits %q = %v, want nil against a healthy store (%s)", name, err, where)
	}
	mark := len(e.h.records())
	f := sa24Read(e, fs, "reading FS.dirty and FS.changes after the setup's Sync", where)
	if f.dirty {
		t.Fatalf("setup: FS.dirty is true after a Sync that returned nil with no other call running, want false "+
			"(ADR 0023 section 3; %s)", where)
	}
	before := sa24GetAttr(e, fs, h, fmt.Sprintf("GetAttr of %q after the setup's Sync", name), where)
	if before.Type != vfs.TypeReg || before.Mode != 0o644 || before.UID != testCaller.UID ||
		before.GID != testCaller.GID || before.Size != uint64(len(data)) {
		t.Fatalf("setup: GetAttr of %q = type %d, mode %#o, uid %d, gid %d, size %d; want a regular file of mode "+
			"0644 owned by %d:%d of %d bytes: a Create with no Mode makes a regular file of mode 0644 owned by the "+
			"caller (ADR 0024 section 7, the facts; %s)", name, before.Type, before.Mode, before.UID, before.GID,
			before.Size, testCaller.UID, testCaller.GID, len(data), where)
	}
	return &sa24Env{e: e, fs: fs, name: name, h: h, data: data, before: before, c0: f.changes, mark: mark}
}

// wantRefusedApplyingNothing runs testCaller's SetAttr of the file with sa,
// requires it to be refused with vfs.ErrPerm, and then requires that it
// applied nothing: GetAttr reports every attribute as before, FS.dirty is
// false and FS.changes is c0, and a Sync returns nil having made no store
// call since the setup's Sync returned (ADR 0024 section 2, "What a refused
// call leaves"). It returns what it calls the SetAttr.
func (s *sa24Env) wantRefusedApplyingNothing(t *testing.T, sa vfs.SetAttr, where string) string {
	t.Helper()
	e := s.e
	what := fmt.Sprintf("testCaller's SetAttr %s of %q", sa24Describe(sa), s.name)
	r := sa24SetAttr(e, s.fs, testCaller, s.h, sa, what, where)
	if !errors.Is(r.err, vfs.ErrPerm) {
		t.Errorf("%s = %v, want an error for which errors.Is(err, vfs.ErrPerm): testCaller owns the file but is not "+
			"root, so it may neither give the file away nor set a group it is not in (ADR 0024 section 7; %s)", what,
			r.err, where)
	}
	after := sa24GetAttr(e, s.fs, s.h, fmt.Sprintf("GetAttr of %q after %s", s.name, what), where)
	if d := sa24Diff(s.before, after); d != "" {
		t.Errorf("after %s, GetAttr of %q reports %s; want every attribute as it was before the call: a SetAttr "+
			"that returns an error has changed no attribute, no size and no time (ADR 0024 section 2; #143; %s)",
			what, s.name, d, where)
	}
	f := sa24Read(e, s.fs, "reading FS.dirty and FS.changes after "+what, where)
	if f.dirty {
		t.Errorf("after %s, FS.dirty is true, want false: a refused SetAttr leaves FS.dirty as it was (ADR 0024 "+
			"section 2; %s)", what, where)
	}
	if f.changes != s.c0 {
		t.Errorf("after %s, FS.changes is %d, want %d, its value before the call: a refused SetAttr leaves "+
			"FS.changes as it was (ADR 0024 section 2; %s)", what, f.changes, s.c0, where)
	}
	if err := e.sync(s.fs, "the Sync after "+what, where); err != nil {
		t.Errorf("the Sync after %s = %v, want nil: with nothing changed since the last seal a Sync returns nil "+
			"(ADR 0023 section 3; ADR 0024 section 7, the facts; %s)", what, err, where)
	}
	if calls := e.h.records()[s.mark:]; len(calls) != 0 {
		t.Errorf("the harness recorded %v between the setup's Sync and the return of the Sync after %s, want no "+
			"call: a refused SetAttr changes nothing, so the next Sync with nothing else dirty makes no store call "+
			"(ADR 0024 section 2, \"What a refused call leaves\"; %s)", calls, what, where)
	}
	return what
}

// remountAttr mounts the inner store afresh and returns the file's handle,
// its attributes there, and the remount, or false with the test failed.
func (s *sa24Env) remountAttr(t *testing.T, where string) (*FS, vfs.Handle, vfs.Attr, bool) {
	t.Helper()
	e := s.e
	rm := e.remount(where)
	if rm == nil {
		return nil, nil, vfs.Attr{}, false
	}
	l := e.lookup(rm, e.root(rm, where), s.name, where)
	if l.err != nil {
		t.Errorf("Lookup of %q on a remount of the inner store = %v, want success (%s)", s.name, l.err, where)
		return nil, nil, vfs.Attr{}, false
	}
	a := sa24GetAttr(e, rm, l.h, fmt.Sprintf("GetAttr of %q on a remount of the inner store", s.name), where)
	return rm, l.h, a, true
}

// TestARefusedSetAttrAppliesNothing is S1 of ADR 0024 section 7, the issue's
// example, and pins section 2's contract for a call without a size.
//
// Each refused case starts from its own state: New over an empty bucket behind
// an unarmed uc43Store; testCaller creates f.txt; Sync; FS.changes read, c0.
// testCaller's SetAttr of f.txt with {Mode 0o600, UID 0}, with {Mode 0o600,
// GID 999} and with {Mode 0o600, UID 501, GID 999} each returns vfs.ErrPerm,
// and after each GetAttr reports mode 0o644, uid 501, gid 20 and the ctime and
// every other attribute it reported before; FS.dirty is false and FS.changes
// is c0; Sync returns nil and the harness recorded no call since the previous
// Sync; a remount of the inner store reports mode 0o644, uid 501 and gid 20.
// Then, from that state again: an empty SetAttr returns nil with the same
// attributes, FS.dirty still false and FS.changes still c0; and the control,
// {Mode 0o600, UID 501, GID 20, ATime t1, MTime t2} with two distinct past
// times a second apart, returns nil with those values, GetAttr agrees,
// FS.dirty is true and FS.changes above c0, and after a Sync a remount agrees.
//
// At f8398c4 the first case leaves mode 0o600 applied with FS.dirty false,
// which is #143; so do the other two; and the empty call marks the namespace
// dirty (ADR 0024 section 2, "The empty call").
func TestARefusedSetAttrAppliesNothing(t *testing.T) {
	cases := []struct {
		name string
		sa   vfs.SetAttr
	}{
		{"a mode, then a give-away to root", vfs.SetAttr{Mode: sa24U32(0o600), UID: sa24U32(0)}},
		{"a mode, then a group the caller is not in", vfs.SetAttr{Mode: sa24U32(0o600), GID: sa24U32(999)}},
		{"a mode, the owner's own uid, then a group the caller is not in",
			vfs.SetAttr{Mode: sa24U32(0o600), UID: sa24U32(501), GID: sa24U32(999)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			where := "S1, " + tc.name + "; ADR 0024 sections 2 and 7"
			s := sa24Setup(t, "f.txt", nil, where)
			what := s.wantRefusedApplyingNothing(t, tc.sa, where)
			if _, _, a, ok := s.remountAttr(t, where); ok &&
				(a.Mode != 0o644 || a.UID != testCaller.UID || a.GID != testCaller.GID) {
				t.Errorf("after %s, a remount of the inner store reports %q with mode %#o, uid %d, gid %d; want mode "+
					"0644, uid %d, gid %d: a refused SetAttr changes nothing in the bucket either (ADR 0024 section "+
					"2; %s)", what, s.name, a.Mode, a.UID, a.GID, testCaller.UID, testCaller.GID, where)
			}
		})
	}

	t.Run("the empty call, then the control", func(t *testing.T) {
		where := "S1, the empty call and the control; ADR 0024 sections 2 and 7"
		s := sa24Setup(t, "f.txt", nil, where)
		e := s.e

		what := `testCaller's SetAttr {} of "f.txt"`
		r := sa24SetAttr(e, s.fs, testCaller, s.h, vfs.SetAttr{}, what, where)
		if r.err != nil {
			t.Errorf("%s = %v, want nil (ADR 0024 section 2, \"The empty call\"; %s)", what, r.err, where)
		} else if d := sa24Diff(s.before, r.v); d != "" {
			t.Errorf("%s returned attributes with %s; want the file's attributes as they were: an empty SetAttr "+
				"applies nothing (ADR 0024 section 2, \"The empty call\"; %s)", what, d, where)
		}
		after := sa24GetAttr(e, s.fs, s.h, `GetAttr of "f.txt" after `+what, where)
		if d := sa24Diff(s.before, after); d != "" {
			t.Errorf("after %s, GetAttr reports %s; want every attribute as it was (ADR 0024 section 2, \"The empty "+
				"call\"; %s)", what, d, where)
		}
		f := sa24Read(e, s.fs, "reading FS.dirty and FS.changes after "+what, where)
		if f.dirty {
			t.Errorf("after %s, FS.dirty is true, want false: a call that applies nothing marks nothing (ADR 0024 "+
				"section 1; section 2, \"The empty call\"; %s)", what, where)
		}
		if f.changes != s.c0 {
			t.Errorf("after %s, FS.changes is %d, want %d: an empty SetAttr advances nothing (ADR 0024 section 7, "+
				"the facts; %s)", what, f.changes, s.c0, where)
		}

		t1, t2 := sa24Times()
		sa := vfs.SetAttr{Mode: sa24U32(0o600), UID: sa24U32(501), GID: sa24U32(20), ATime: sa24Time(t1),
			MTime: sa24Time(t2)}
		what = fmt.Sprintf(`testCaller's SetAttr %s of "f.txt", the control`, sa24Describe(sa))
		r = sa24SetAttr(e, s.fs, testCaller, s.h, sa, what, where)
		if r.err != nil {
			t.Fatalf("%s = %v, want nil: the owner may set the mode, its own uid, a group it is in and the times "+
				"(ADR 0024 section 7; %s)", what, r.err, where)
		}
		if d := sa24Unapplied(r.v, sa); d != "" {
			t.Errorf("%s returned attributes with %s: a SetAttr that returns nil has applied all of sa, and returns "+
				"the attributes after the change (ADR 0024 section 2; %s)", what, d, where)
		}
		got := sa24GetAttr(e, s.fs, s.h, `GetAttr of "f.txt" after `+what, where)
		if d := sa24Unapplied(got, sa); d != "" {
			t.Errorf("after %s, GetAttr reports %s (ADR 0024 section 2; %s)", what, d, where)
		}
		f = sa24Read(e, s.fs, "reading FS.dirty and FS.changes after "+what, where)
		if !f.dirty {
			t.Errorf("after %s, FS.dirty is false, want true: every change to the namespace is marked dirty in the "+
				"hold that makes it (ADR 0024 section 1; %s)", what, where)
		}
		if f.changes <= s.c0 {
			t.Errorf("after %s, FS.changes is %d, want above %d: a SetAttr that returns nil advances FS.changes "+
				"(ADR 0024 section 7, the facts; %s)", what, f.changes, s.c0, where)
		}
		if err := e.sync(s.fs, "the Sync after "+what, where); err != nil {
			t.Fatalf("the Sync after %s = %v, want nil against a healthy store (%s)", what, err, where)
		}
		if _, _, a, ok := s.remountAttr(t, where); ok {
			if d := sa24Unapplied(a, sa); d != "" {
				t.Errorf("after %s and a Sync, a remount of the inner store reports %s: the change was committed "+
					"(ADR 0024 section 1; %s)", what, d, where)
			}
		}
	})
}

// TestARefusedSetAttrWithASizeAppliesNothing is S2 of ADR 0024 section 7, and
// pins section 2's contract for a call with a size: the size is checked with
// the rest, and a call refused for any attribute leaves the size as it was.
//
// Each refused case starts from its own state, S1's with f.bin holding 100
// bytes written at 0, vfs.Unstable, and committed by the Sync. testCaller's
// SetAttr with {Size 0, UID 0}, and with {Size 10, Mode 0o600, GID 999}, each
// returns vfs.ErrPerm; after each GetAttr reports size 100, mode 0o644 and
// every other attribute as before; FS.dirty is false and FS.changes
// unchanged; Sync returns nil having made no store call; f.bin reads back its
// 100 bytes; and a remount reads the 100 bytes. The control, {Size 10, Mode
// 0o600, UID 501, GID 20, ATime t1, MTime t2}, returns nil with size 10, mode
// 0o600, atime t1 and mtime t2, the given mtime and not the size change's;
// GetAttr agrees; FS.dirty is true; after a Sync a remount agrees and reads
// the first 10 bytes; and one Read of 100 bytes at 0 returns the first 10
// bytes with eof.
//
// The Reads come after the Sync and the remount in each case, so that nothing
// here depends on whether a Read changes the access time, which the spec does
// not say.
//
// At f8398c4 the first case leaves size 0 applied, which is #143's size half;
// the second leaves size 10 and mode 0o600.
func TestARefusedSetAttrWithASizeAppliesNothing(t *testing.T) {
	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i*7 + 3)
	}

	cases := []struct {
		name string
		sa   vfs.SetAttr
	}{
		{"a size, then a give-away to root", vfs.SetAttr{Size: sa24U64(0), UID: sa24U32(0)}},
		{"a size, a mode, then a group the caller is not in",
			vfs.SetAttr{Size: sa24U64(10), Mode: sa24U32(0o600), GID: sa24U32(999)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			where := "S2, " + tc.name + "; ADR 0024 sections 2 and 7"
			s := sa24Setup(t, "f.bin", payload, where)
			e := s.e
			what := s.wantRefusedApplyingNothing(t, tc.sa, where)
			if rm, rh, a, ok := s.remountAttr(t, where); ok {
				if a.Size != uint64(len(payload)) || a.Mode != 0o644 {
					t.Errorf("after %s, a remount of the inner store reports %q with size %d and mode %#o, want size "+
						"%d and mode 0644 (ADR 0024 section 2; %s)", what, s.name, a.Size, a.Mode, len(payload),
						where)
				}
				got, err := e.readFile(rm, rh, fmt.Sprintf("reading %q whole on the remount", s.name), where)
				if err != nil || !bytes.Equal(got, payload) {
					t.Errorf("after %s, a remount reads %d bytes of %q (err %v), want the 100 bytes written: a "+
						"refused SetAttr changes no size and no chunk list (ADR 0024 section 2; %s)", what,
						len(got), s.name, err, where)
				}
			}
			got, err := e.readFile(s.fs, s.h, fmt.Sprintf("reading %q whole after %s", s.name, what), where)
			if err != nil || !bytes.Equal(got, payload) {
				t.Errorf("after %s, %q reads back %d bytes (err %v), want the 100 bytes written: a refused SetAttr "+
					"changes no size, no chunk list and no dirty buffer (ADR 0024 section 2; %s)", what, s.name,
					len(got), err, where)
			}
		})
	}

	t.Run("the control", func(t *testing.T) {
		where := "S2, the control; ADR 0024 sections 2 and 7"
		s := sa24Setup(t, "f.bin", payload, where)
		e := s.e

		t1, t2 := sa24Times()
		sa := vfs.SetAttr{Size: sa24U64(10), Mode: sa24U32(0o600), UID: sa24U32(501), GID: sa24U32(20),
			ATime: sa24Time(t1), MTime: sa24Time(t2)}
		what := fmt.Sprintf(`testCaller's SetAttr %s of "f.bin", the control`, sa24Describe(sa))
		r := sa24SetAttr(e, s.fs, testCaller, s.h, sa, what, where)
		if r.err != nil {
			t.Fatalf("%s = %v, want nil: the owner may set the size, the mode, its own uid, a group it is in and "+
				"the times (ADR 0024 section 7; %s)", what, r.err, where)
		}
		if d := sa24Unapplied(r.v, sa); d != "" {
			t.Errorf("%s returned attributes with %s: a SetAttr that returns nil has applied all of sa, the given "+
				"mtime winning over the size change's (ADR 0024 section 2, \"The applies, and the times\"; %s)", what,
				d, where)
		}
		got := sa24GetAttr(e, s.fs, s.h, `GetAttr of "f.bin" after `+what, where)
		if d := sa24Unapplied(got, sa); d != "" {
			t.Errorf("after %s, GetAttr reports %s (ADR 0024 section 2, \"The applies, and the times\"; %s)", what, d,
				where)
		}
		f := sa24Read(e, s.fs, "reading FS.dirty and FS.changes after "+what, where)
		if !f.dirty {
			t.Errorf("after %s, FS.dirty is false, want true (ADR 0024 section 1; %s)", what, where)
		}
		if f.changes <= s.c0 {
			t.Errorf("after %s, FS.changes is %d, want above %d (ADR 0024 section 7, the facts; %s)", what,
				f.changes, s.c0, where)
		}
		if err := e.sync(s.fs, "the Sync after "+what, where); err != nil {
			t.Fatalf("the Sync after %s = %v, want nil against a healthy store (%s)", what, err, where)
		}
		if rm, rh, a, ok := s.remountAttr(t, where); ok {
			if d := sa24Unapplied(a, sa); d != "" {
				t.Errorf("after %s and a Sync, a remount of the inner store reports %s: the size and the rest of sa "+
					"land in one commit (ADR 0024 section 2; %s)", what, d, where)
			}
			data, err := e.readFile(rm, rh, `reading "f.bin" whole on the remount`, where)
			if err != nil || !bytes.Equal(data, payload[:10]) {
				t.Errorf("after %s and a Sync, a remount reads %d bytes %x of \"f.bin\" (err %v), want its first 10 "+
					"bytes %x (ADR 0024 section 2; %s)", what, len(data), data, err, payload[:10], where)
			}
		}
		rd := sa24ReadOnce(e, s.fs, s.h, 0, 100, `one Read of 100 bytes at 0 of "f.bin"`, where)
		if rd.err != nil || !bytes.Equal(rd.data, payload[:10]) || !rd.eof {
			t.Errorf("after %s, one Read of 100 bytes at 0 of \"f.bin\" = (%x, eof %v, %v), want (%x, eof true, nil) "+
				"(ADR 0024 section 7; %s)", what, rd.data, rd.eof, rd.err, payload[:10], where)
		}
	})
}

// TestSetAttrRefusesInTheOrderSizeModeOwnerGroupTimes is S3 of ADR 0024
// section 7, and pins section 2's order of errors, which is ADR 0007 section
// 4's: vfs.ErrFBig, then the handle, then the size's vfs.ErrIsDir or
// vfs.ErrAcces, then a mode's, owner's or group's vfs.ErrPerm, then the times.
//
// This is a characterisation: it passes at f8398c4 too, since the order is
// unchanged, and is pinned so that moving the checks ahead of every apply
// cannot reorder them (ADR 0024 Assumption 15).
//
// New over an empty bucket; testCaller creates f.txt, mode 0o644, and the
// directory d; Sync. vfs.Caller{UID: 502, GID: 30}'s SetAttr of f.txt with
// {Size 0, Mode 0o600} returns vfs.ErrAcces, not vfs.ErrPerm, and with {Mode
// 0o600, UID 0} vfs.ErrPerm. testCaller's SetAttr of d with {Size 0, Mode
// 0o700} returns vfs.ErrIsDir. testCaller's SetAttr of f.txt with {Size
// MaxFileSize()+1, UID 0} returns vfs.ErrFBig, not vfs.ErrPerm. After each,
// GetAttr reports what it reported before, and FS.dirty is false.
func TestSetAttrRefusesInTheOrderSizeModeOwnerGroupTimes(t *testing.T) {
	const where = "S3, ADR 0024 section 2, the order of errors; ADR 0007 section 4"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	fh := e.create(fs, root, "f.txt", where)
	dh := e.mkdir(fs, root, "d", where)
	if err := e.sync(fs, `the Sync that commits "f.txt" and "d"`, where); err != nil {
		t.Fatalf("setup: the Sync that commits %q and %q = %v, want nil against a healthy store (%s)", "f.txt", "d",
			err, where)
	}
	if f := sa24Read(e, fs, "reading FS.dirty after the setup's Sync", where); f.dirty {
		t.Fatalf("setup: FS.dirty is true after a Sync that returned nil with no other call running, want false "+
			"(ADR 0023 section 3; %s)", where)
	}
	if a := sa24GetAttr(e, fs, fh, `GetAttr of "f.txt"`, where); a.Mode != 0o644 || a.UID != testCaller.UID {
		t.Fatalf("setup: %q has mode %#o and uid %d, want 0644 and %d (ADR 0024 section 7, the facts; %s)", "f.txt",
			a.Mode, a.UID, testCaller.UID, where)
	}
	maxSize := uc43Go(e, func() uint64 { return fs.MaxFileSize() }).wait(t, "MaxFileSize", where)
	if maxSize == math.MaxUint64 {
		t.Fatalf("setup: MaxFileSize() is the largest uint64, so no size above it can be asked for (%s)", where)
	}

	const other = "vfs.Caller{UID: 502, GID: 30}"
	cases := []struct {
		who    string
		c      vfs.Caller
		target string
		h      vfs.Handle
		sa     vfs.SetAttr
		want   vfs.Status
		wname  string
		why    string
	}{
		{other, sa24Other, "f.txt", fh, vfs.SetAttr{Size: sa24U64(0), Mode: sa24U32(0o600)}, vfs.ErrAcces,
			"vfs.ErrAcces, not vfs.ErrPerm",
			"the size's check comes before the mode's, and a caller without write permission may not set the size"},
		{other, sa24Other, "f.txt", fh, vfs.SetAttr{Mode: sa24U32(0o600), UID: sa24U32(0)}, vfs.ErrPerm,
			"vfs.ErrPerm", "only root or the owner may set the mode or the owner"},
		{"testCaller", testCaller, "d", dh, vfs.SetAttr{Size: sa24U64(0), Mode: sa24U32(0o700)}, vfs.ErrIsDir,
			"vfs.ErrIsDir", "the size's check comes before the mode's, and a directory has no size to set"},
		{"testCaller", testCaller, "f.txt", fh, vfs.SetAttr{Size: sa24U64(maxSize + 1), UID: sa24U32(0)},
			vfs.ErrFBig, "vfs.ErrFBig, not vfs.ErrPerm",
			"a size above MaxFileSize is refused first, before any other check (ADR 0007 section 4)"},
	}
	for _, tc := range cases {
		before := sa24GetAttr(e, fs, tc.h, fmt.Sprintf("GetAttr of %q before the call", tc.target), where)
		what := fmt.Sprintf("%s's SetAttr %s of %q", tc.who, sa24Describe(tc.sa), tc.target)
		r := sa24SetAttr(e, fs, tc.c, tc.h, tc.sa, what, where)
		if !errors.Is(r.err, tc.want) {
			t.Errorf("%s = %v, want %s: %s (%s)", what, r.err, tc.wname, tc.why, where)
		}
		after := sa24GetAttr(e, fs, tc.h, fmt.Sprintf("GetAttr of %q after %s", tc.target, what), where)
		if d := sa24Diff(before, after); d != "" {
			t.Errorf("after %s, GetAttr of %q reports %s; want every attribute as it was: a refused SetAttr applies "+
				"nothing (ADR 0024 section 2; %s)", what, tc.target, d, where)
		}
		if f := sa24Read(e, fs, "reading FS.dirty after "+what, where); f.dirty {
			t.Errorf("after %s, FS.dirty is true, want false: a refused SetAttr leaves FS.dirty as it was (ADR 0024 "+
				"section 2; %s)", what, where)
		}
	}
}

// TestARefusedSetAttrWithASizeDoesNotWaitForTheCommitGate is S6 of ADR 0024
// section 7, and pins section 2's step 2 and ADR 0012 section 2: a SetAttr
// with a size that is refused, for the size or for any other attribute, is
// refused in the pre-check, holding nothing, and never waits for the commit
// gate.
//
// With commit_window_test.go's harness, exactly as steps 1 to 3 of
// TestCommitWindowTruncateDuringSettleIsWholeOrAbsent: A is seeded at 2cs and
// B with 100 bytes, each through a mount of its own, and the bucket mounted
// afresh behind a bpStore; held.bin (C) is created and written. (1) Hold 1 is
// armed on the Put of C's chunk and a Sync started; while it is held in its
// first pass, A is truncated to T, into its stored chunk 1. (2) Hold 2 is
// armed on the Put of A's shortened chunk 1. (3) Hold 1 is opened, and the
// Sync enters hold 2, its settle pass's upload, where it holds the commit gate
// for writing (ADR 0012 section 2; ADR 0023 section 2). While it is held,
// testCaller's SetAttr of B with {Size 0, UID 0} must return vfs.ErrPerm under
// the bound, and B's size is unchanged. Then hold 2 is opened, and the Sync
// returns nil; B's size is still 100.
//
// At f8398c4 the call hangs to the bound: truncate's own checks pass, since
// testCaller owns B, and it then waits for the gate before SetAttr refuses the
// uid. Once hold 2 is opened it applies size 0 and returns vfs.ErrPerm.
func TestARefusedSetAttrWithASizeDoesNotWaitForTheCommitGate(t *testing.T) {
	st := bpLocal(t)
	aData := bpSeedFile(t, st, cwA, 2*bpCS)
	bData := bpSeedFile(t, st, sa24B, 100)
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, -1, quietLog())
	a, aAttr := bpLookup(t, fs, cwA)
	b, bAttr := bpLookup(t, fs, sa24B)
	if bAttr.Size != uint64(len(bData)) || bAttr.UID != testCaller.UID {
		t.Fatalf("setup: %s has size %d and uid %d, want %d and %d", sa24B, bAttr.Size, bAttr.UID, len(bData),
			testCaller.UID)
	}
	m := &cwMount{st: st, bps: bps, fs: fs, a: a, aID: aAttr.FileID, aData: aData}
	m.createHeld(t)

	// Step 1.
	sc := m.holdSync(t)
	m.truncateA(t, cwCut, "while a Sync is held in its first pass")

	// Step 2.
	gate2 := newBTGate(t)
	put2 := &bpHook{key: cwKey(aData[bpCS:cwCut]), entered: make(chan struct{}), gate: gate2.ch}
	bps.setPut(put2)

	// Step 3.
	m.gate.open()
	select {
	case <-put2.entered:
	case <-sc.done:
		t.Fatalf("setup: the Sync ended, returning %v (panic: %v), without uploading a.bin's shortened chunk 1, so "+
			"the test cannot hold it in its settle pass (ADR 0012 §9 item 5)", sc.val, sc.pval)
	case <-time.After(btHangBound):
		t.Fatalf("setup: the Sync neither entered the Put of a.bin's shortened chunk 1 nor returned within %v "+
			"after hold 1 was opened (ADR 0012 §9 item 5)", btHangBound)
	}

	// While the Sync holds the commit gate for writing.
	sa := vfs.SetAttr{Size: sa24U64(0), UID: sa24U32(0)}
	what := fmt.Sprintf("testCaller's SetAttr %s of %s", sa24Describe(sa), sa24B)
	call := bpGo(func() error {
		_, err := fs.SetAttr(context.Background(), testCaller, b, sa)
		return err
	})
	returned := true
	select {
	case <-call.done:
	case <-time.After(btHangBound):
		returned = false
		t.Errorf("%s has not returned after %v while a Sync holds the commit gate for writing in its settle pass's "+
			"upload: a SetAttr with a size that is refused, for any attribute, is refused in the pre-check holding "+
			"nothing and never waits for the gate (ADR 0024 section 2, step 2; ADR 0012 section 2)", what,
			btHangBound)
	}
	if returned {
		if sc.finished() {
			t.Errorf("the Sync had returned (%v) before hold 2 was opened, so %s did not run while the gate was held",
				sc.val, what)
		}
		if got := bpGetAttr(t, fs, b, "GetAttr of "+sa24B+" while the Sync is held, after "+what); got.Size !=
			uint64(len(bData)) {
			t.Errorf("after %s, while the Sync is held, %s has size %d, want %d: a refused SetAttr changes no size "+
				"(ADR 0024 section 2)", what, sa24B, got.Size, len(bData))
		}
	}

	gate2.open()
	err := call.wait(t, what+", once hold 2 was opened")
	if !errors.Is(err, vfs.ErrPerm) {
		t.Errorf("%s = %v, want an error for which errors.Is(err, vfs.ErrPerm): testCaller owns %s but is not root, "+
			"so it may not give it away (ADR 0024 section 7)", what, err, sa24B)
	}
	if err := sc.wait(t, "the Sync, after hold 2 was opened"); err != nil {
		t.Errorf("the Sync, after hold 2 was opened, = %v, want nil against a healthy writable store (ADR 0024 "+
			"section 7, S6)", err)
	}
	if got := bpGetAttr(t, fs, b, "GetAttr of "+sa24B+" after the Sync returned"); got.Size != uint64(len(bData)) {
		t.Errorf("after %s and the Sync, %s has size %d, want %d: a refused SetAttr changes no size (ADR 0024 "+
			"section 2)", what, sa24B, got.Size, len(bData))
	}
}
