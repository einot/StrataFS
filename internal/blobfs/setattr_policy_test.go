package blobfs

// Clean-room tests for ADR 0024
// (docs/adr/0024-every-namespace-change-is-marked-dirty-and-a-refused-setattr-makes-none.md)
// section 2, "The checks are the policy's", written without reading the
// implementation, from ADR 0024 sections 2 and 7, ADR 0014 section 5 (the
// policy API) and section 11 (W2, W3 and the safety rule), the facts and
// harness of ADR 0023 section 11, the safety rules of ADR 0011 section 7, and
// internal/vfs/vfs.go.
//
// ADR 0024 section 2: SetAttr asks f.perm for every attribute sa sets, in the
// order OpTruncate (a size), OpChmod (a mode), OpChown with UID *sa.UID,
// OpChgrp with GID *sa.GID, OpSetTimes (an atime), OpSetTimes (an mtime);
// returns the first refusal unchanged and makes no request after it; makes
// every request before it applies any attribute, each with Object built from
// the inode's Type, Mode, UID and GID as they stood when the call began; and,
// for a call with a size, makes the list twice, the second time, in the hold
// that applies the change, after the first has been allowed whole. So no check
// of SetAttr's is made outside f.perm.
//
// Covered, with the names section 7 pins:
//   - S4, TestSetAttrAsksThePolicyForEveryAttributeBeforeApplyingAny (ADR 0014
//     section 11, W2, for SetAttr);
//   - S5, TestEverySetAttrCheckIsBehindThePolicy (W3, for SetAttr).
//
// S1, S2, S3 and S6 are in setattr_atomic_test.go, whose helpers (sa24...)
// this file reuses.
//
// FS.perm is the one FS field this file writes, and only as ADR 0024 section 2
// and ADR 0014 section 11's W1 allow: once, right after New returns and before
// any other call on the FS, Root() included, inside uc43Go. Every policy
// installed here allows what Unix() refuses, so, by ADR 0014 section 11's
// safety rule, every SetAttr here is made on a regular file; the only other
// calls are testCaller's Create and Write of a regular file in the root
// directory, which it owns, and Sync.
//
// The other safety rules are part 1's (ADR 0023 section 11; ADR 0011 section
// 7): buckets under the test's t.TempDir(), behind an unarmed uc43Store; every
// New and every FS call in uc43Go, which recovers a panic, waited for under
// uc43Bound, 10 s; no sleeps and no polling; nothing parallel; FS.dirty read
// only holding FS.mu for reading.
//
// Not covered, on purpose: the Dst field of any request, and UID and GID on
// requests other than OpChown and OpChgrp, which ADR 0014 section 5 says no
// row reads and ADR 0024 does not pin.

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

// sp24Root is root, the caller of S4's SetAttr calls (ADR 0024 section 7).
var sp24Root = vfs.Caller{UID: 0}

// sp24Recorder is S4's recording policy. Its check appends each request, with
// a copy of Caller.GIDs, to reqs under its own mutex, and returns vfs.ErrNXIO
// for the request at index refuseAt, counting from 0 since the last reset, and
// nil for every other; refuseAt -1 refuses nothing.
type sp24Recorder struct {
	mu       sync.Mutex
	reqs     []policy.Request
	refuseAt int
}

func (r *sp24Recorder) check(req policy.Request) error {
	req.Caller.GIDs = slices.Clone(req.Caller.GIDs)
	r.mu.Lock()
	defer r.mu.Unlock()
	i := len(r.reqs)
	r.reqs = append(r.reqs, req)
	if i == r.refuseAt {
		return vfs.ErrNXIO
	}
	return nil
}

// reset forgets every request recorded and sets the index to refuse.
func (r *sp24Recorder) reset(refuseAt int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = nil
	r.refuseAt = refuseAt
}

// taken returns a copy of the requests recorded since the last reset.
func (r *sp24Recorder) taken() []policy.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.reqs)
}

// sp24OpName names the operations SetAttr asks for, for failure messages.
func sp24OpName(op policy.Op) string {
	switch op {
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
	}
	return fmt.Sprintf("Op(%d)", op)
}

// sp24Describe spells a list of requests for failure messages.
func sp24Describe(reqs []policy.Request) string {
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = fmt.Sprintf("%s{Caller uid %d gid %d gids %v, Object {Type %d, Mode %#o, UID %d, GID %d}, UID %d, "+
			"GID %d}", sp24OpName(r.Op), r.Caller.UID, r.Caller.GID, r.Caller.GIDs, r.Object.Type, r.Object.Mode,
			r.Object.UID, r.Object.GID, r.UID, r.GID)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// sp24Want is one request S4 expects: its Op, and the UID of an OpChown or the
// GID of an OpChgrp.
type sp24Want struct {
	op  policy.Op
	uid uint32
	gid uint32
}

// sp24WantRequests requires got to be exactly want, in order, each made by
// caller with Object obj.
func sp24WantRequests(t *testing.T, what string, got []policy.Request, want []sp24Want, caller vfs.Caller,
	obj policy.Object, why, where string) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(want); i++ {
		g, w := got[i], want[i]
		switch {
		case g.Op != w.op:
			ok = false
		case w.op == policy.OpChown && g.UID != w.uid:
			ok = false
		case w.op == policy.OpChgrp && g.GID != w.gid:
			ok = false
		case g.Caller.UID != caller.UID || g.Caller.GID != caller.GID || len(g.Caller.GIDs) != len(caller.GIDs):
			ok = false
		case g.Object != obj:
			ok = false
		}
	}
	if ok {
		return
	}
	var ws []string
	for _, w := range want {
		s := sp24OpName(w.op)
		switch w.op {
		case policy.OpChown:
			s += fmt.Sprintf(" with UID %d", w.uid)
		case policy.OpChgrp:
			s += fmt.Sprintf(" with GID %d", w.gid)
		}
		ws = append(ws, s)
	}
	t.Errorf("%s asked the policy %s; want exactly [%s], in that order, each with Caller uid %d gid %d and Object "+
		"{Type %d, Mode %#o, UID %d, GID %d}: %s (%s)", what, sp24Describe(got), strings.Join(ws, ", "), caller.UID,
		caller.GID, obj.Type, obj.Mode, obj.UID, obj.GID, why, where)
}

// sp24Mount runs New over an empty bucket on the harness and, before any other
// call on the FS, replaces FS.perm with p (ADR 0024 section 2; ADR 0014
// section 11, W1). It returns the FS and its root directory's handle.
func sp24Mount(e *uc43Env, p policy.Policy, where string) (*FS, vfs.Handle) {
	e.t.Helper()
	fs := e.newFS(where)
	uc43Go(e, func() struct{} {
		fs.perm = p
		return struct{}{}
	}).wait(e.t, "replacing FS.perm right after New returned", where)
	return fs, e.root(fs, where)
}

// sp24File creates name as testCaller, writes data at 0 if it is not nil, and
// commits with a Sync, which must return nil and leave FS.dirty false. It
// returns the file's handle and attributes, which must be a regular file of
// mode 0o644 owned by testCaller, of len(data) bytes.
func sp24File(e *uc43Env, fs *FS, root vfs.Handle, name string, data []byte, where string) (vfs.Handle, vfs.Attr) {
	e.t.Helper()
	h := e.create(fs, root, name, where)
	if data != nil {
		e.write(fs, h, 0, data, fmt.Sprintf("Write of %d bytes at 0 of %q, vfs.Unstable", len(data), name), where)
	}
	if err := e.sync(fs, fmt.Sprintf("the Sync that commits %q", name), where); err != nil {
		e.t.Fatalf("setup: the Sync that commits %q = %v, want nil against a healthy store (%s)", name, err, where)
	}
	if f := sa24Read(e, fs, "reading FS.dirty after the Sync that commits "+name, where); f.dirty {
		e.t.Fatalf("setup: FS.dirty is true after a Sync that returned nil with no other call running, want false "+
			"(ADR 0023 section 3; %s)", where)
	}
	a := sa24GetAttr(e, fs, h, fmt.Sprintf("GetAttr of %q after its commit", name), where)
	if a.Type != vfs.TypeReg || a.Mode != 0o644 || a.UID != testCaller.UID || a.GID != testCaller.GID ||
		a.Size != uint64(len(data)) {
		e.t.Fatalf("setup: GetAttr of %q = type %d, mode %#o, uid %d, gid %d, size %d; want a regular file of mode "+
			"0644 owned by %d:%d of %d bytes (ADR 0024 section 7, the facts; %s)", name, a.Type, a.Mode, a.UID,
			a.GID, a.Size, testCaller.UID, testCaller.GID, len(data), where)
	}
	return h, a
}

// TestSetAttrAsksThePolicyForEveryAttributeBeforeApplyingAny is S4 of ADR 0024
// section 7, and pins section 2's requests: which, in what order, stopping at
// the first refusal, every one before any attribute is applied, and for a call
// with a size twice (ADR 0014 section 11, W2, for SetAttr; ADR 0024
// Assumption 7).
//
// After New, and before any other call, f.perm is replaced with a policy.Func
// that records each request and returns vfs.ErrNXIO for the one at a set
// index, nil otherwise. testCaller creates f.txt; Sync. Root's SetAttr of
// f.txt with {Mode 0o600, UID 7, GID 8, ATime t1, MTime t2}:
//   - (i) refusing index 2 returns vfs.ErrNXIO, having asked exactly OpChmod,
//     OpChown with UID 7, OpChgrp with GID 8, each with Caller root and Object
//     {TypeReg, 0o644, 501, 20}; GetAttr reports every attribute as before;
//     FS.dirty is false;
//   - (ii) refusing nothing returns nil, having asked exactly OpChmod, OpChown
//     with UID 7, OpChgrp with GID 8, OpSetTimes, OpSetTimes, every one with
//     that same Object, which shows that nothing was applied before the last
//     request was made; GetAttr reports 0o600, 7, 8, t1 and t2.
//
// Then testCaller creates f.bin and writes 100 bytes; Sync. Root's SetAttr of
// f.bin with {Size 10, Mode 0o600}:
//   - (iii) refusing index 2 returns vfs.ErrNXIO, having asked exactly
//     OpTruncate, OpChmod, OpTruncate; size 100 and mode 0o644, and every
//     other attribute as before; FS.dirty is false;
//   - (iv) refusing nothing returns nil, having asked exactly OpTruncate,
//     OpChmod, OpTruncate, OpChmod, each with Object {TypeReg, 0o644, 501,
//     20}; size 10 and mode 0o600.
//
// Every request in (i) to (iv) is also required to carry Caller root and that
// Object, which ADR 0024 section 7's facts state for every request. All the
// SetAttr calls are on regular files (ADR 0014 section 11's safety rule).
func TestSetAttrAsksThePolicyForEveryAttributeBeforeApplyingAny(t *testing.T) {
	const where = "S4, ADR 0024 sections 2 and 7; ADR 0014 section 11, W2"
	e := uc43Setup(t)
	rec := &sp24Recorder{refuseAt: -1}
	fs, root := sp24Mount(e, policy.Func(rec.check), where)
	obj := policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: testCaller.UID, GID: testCaller.GID}

	// f.txt, without a size.
	txt, before := sp24File(e, fs, root, "f.txt", nil, where)
	t1, t2 := sa24Times()
	sa := vfs.SetAttr{Mode: sa24U32(0o600), UID: sa24U32(7), GID: sa24U32(8), ATime: sa24Time(t1),
		MTime: sa24Time(t2)}

	what := fmt.Sprintf(`(i) root's SetAttr %s of "f.txt", the policy refusing its request at index 2`,
		sa24Describe(sa))
	rec.reset(2)
	r := sa24SetAttr(e, fs, sp24Root, txt, sa, what, where)
	if !errors.Is(r.err, vfs.ErrNXIO) {
		t.Errorf("%s = %v, want vfs.ErrNXIO, the policy's refusal returned unchanged (ADR 0024 section 2; %s)", what,
			r.err, where)
	}
	sp24WantRequests(t, what, rec.taken(), []sp24Want{{op: policy.OpChmod}, {op: policy.OpChown, uid: 7},
		{op: policy.OpChgrp, gid: 8}}, sp24Root, obj, "SetAttr asks for each attribute sa sets in the order mode, "+
		"owner, group, times, and makes no request after the first refusal (ADR 0024 section 2)", where)
	if got := sa24GetAttr(e, fs, txt, `GetAttr of "f.txt" after `+what, where); sa24Diff(before, got) != "" {
		t.Errorf("after %s, GetAttr of %q reports %s; want every attribute as it was: a refused SetAttr applies "+
			"nothing (ADR 0024 section 2; %s)", what, "f.txt", sa24Diff(before, got), where)
	}
	if f := sa24Read(e, fs, "reading FS.dirty after "+what, where); f.dirty {
		t.Errorf("after %s, FS.dirty is true, want false: a refused SetAttr leaves FS.dirty as it was (ADR 0024 "+
			"section 2; %s)", what, where)
	}

	what = fmt.Sprintf(`(ii) root's SetAttr %s of "f.txt", the policy refusing nothing`, sa24Describe(sa))
	rec.reset(-1)
	r = sa24SetAttr(e, fs, sp24Root, txt, sa, what, where)
	if r.err != nil {
		t.Errorf("%s = %v, want nil (ADR 0024 section 2; %s)", what, r.err, where)
	} else if d := sa24Unapplied(r.v, sa); d != "" {
		t.Errorf("%s returned attributes with %s: a SetAttr that returns nil has applied all of sa (ADR 0024 section "+
			"2; %s)", what, d, where)
	}
	sp24WantRequests(t, what, rec.taken(), []sp24Want{{op: policy.OpChmod}, {op: policy.OpChown, uid: 7},
		{op: policy.OpChgrp, gid: 8}, {op: policy.OpSetTimes}, {op: policy.OpSetTimes}}, sp24Root, obj,
		"every request is made before any attribute is applied, so every one, the two OpSetTimes included, sees "+
			"the inode as it stood when the call began (ADR 0024 section 2)", where)
	if got := sa24GetAttr(e, fs, txt, `GetAttr of "f.txt" after `+what, where); sa24Unapplied(got, sa) != "" {
		t.Errorf("after %s, GetAttr of %q reports %s (ADR 0024 section 2; %s)", what, "f.txt",
			sa24Unapplied(got, sa), where)
	}

	// f.bin, with a size.
	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i*5 + 1)
	}
	bin, beforeBin := sp24File(e, fs, root, "f.bin", payload, where)
	sz := vfs.SetAttr{Size: sa24U64(10), Mode: sa24U32(0o600)}

	what = fmt.Sprintf(`(iii) root's SetAttr %s of "f.bin", the policy refusing its request at index 2`,
		sa24Describe(sz))
	rec.reset(2)
	r = sa24SetAttr(e, fs, sp24Root, bin, sz, what, where)
	if !errors.Is(r.err, vfs.ErrNXIO) {
		t.Errorf("%s = %v, want vfs.ErrNXIO, the policy's refusal returned unchanged (ADR 0024 section 2; %s)", what,
			r.err, where)
	}
	sp24WantRequests(t, what, rec.taken(), []sp24Want{{op: policy.OpTruncate}, {op: policy.OpChmod},
		{op: policy.OpTruncate}}, sp24Root, obj, "a call with a size makes the list in the pre-check and, once "+
		"that is allowed whole, again in the hold that applies the change, stopping at the first refusal (ADR "+
		"0024 section 2, steps 2 and 4)", where)
	if got := sa24GetAttr(e, fs, bin, `GetAttr of "f.bin" after `+what, where); sa24Diff(beforeBin, got) != "" {
		t.Errorf("after %s, GetAttr of %q reports %s; want size 100, mode 0644 and every other attribute as it "+
			"was: a call refused in the hold that applies the change applies nothing (ADR 0024 section 2, step 4; "+
			"%s)", what, "f.bin", sa24Diff(beforeBin, got), where)
	}
	if f := sa24Read(e, fs, "reading FS.dirty after "+what, where); f.dirty {
		t.Errorf("after %s, FS.dirty is true, want false (ADR 0024 section 2; %s)", what, where)
	}

	what = fmt.Sprintf(`(iv) root's SetAttr %s of "f.bin", the policy refusing nothing`, sa24Describe(sz))
	rec.reset(-1)
	r = sa24SetAttr(e, fs, sp24Root, bin, sz, what, where)
	if r.err != nil {
		t.Errorf("%s = %v, want nil (ADR 0024 section 2; %s)", what, r.err, where)
	} else if d := sa24Unapplied(r.v, sz); d != "" {
		t.Errorf("%s returned attributes with %s (ADR 0024 section 2; %s)", what, d, where)
	}
	sp24WantRequests(t, what, rec.taken(), []sp24Want{{op: policy.OpTruncate}, {op: policy.OpChmod},
		{op: policy.OpTruncate}, {op: policy.OpChmod}}, sp24Root, obj, "a call with a size makes the list twice, "+
		"each time against the inode as it stood when the call began, since nothing is applied until the second "+
		"list has been allowed (ADR 0024 section 2, steps 2, 4 and 5)", where)
	if got := sa24GetAttr(e, fs, bin, `GetAttr of "f.bin" after `+what, where); sa24Unapplied(got, sz) != "" {
		t.Errorf("after %s, GetAttr of %q reports %s (ADR 0024 section 2; %s)", what, "f.bin",
			sa24Unapplied(got, sz), where)
	}
}

// TestEverySetAttrCheckIsBehindThePolicy is S5 of ADR 0024 section 7, and pins
// section 2's "The checks are the policy's" (ADR 0014 section 11, W3, for
// SetAttr): with f.perm replaced by a policy that allows everything, a caller
// whom the mode bits and ownership would refuse every attribute succeeds, so
// no check of SetAttr's remains outside f.perm.
//
// After New, and before any other call, f.perm is replaced with a policy.Func
// that returns nil for every request. testCaller creates f.bin, mode 0o644,
// and writes 100 bytes; Sync. vfs.Caller{UID: 502, GID: 30}, neither its owner
// nor in its group, sets {Size 5, Mode 0o600, UID 502, GID 30, ATime t1, MTime
// t2}: the call returns nil, and both what it returns and GetAttr report every
// one of those values. The call is on a regular file (ADR 0014 section 11's
// safety rule).
func TestEverySetAttrCheckIsBehindThePolicy(t *testing.T) {
	const where = "S5, ADR 0024 sections 2 and 7; ADR 0014 section 11, W3"
	e := uc43Setup(t)
	fs, root := sp24Mount(e, policy.Func(func(policy.Request) error { return nil }), where)
	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i*3 + 2)
	}
	h, _ := sp24File(e, fs, root, "f.bin", payload, where)

	t1, t2 := sa24Times()
	sa := vfs.SetAttr{Size: sa24U64(5), Mode: sa24U32(0o600), UID: sa24U32(502), GID: sa24U32(30),
		ATime: sa24Time(t1), MTime: sa24Time(t2)}
	what := fmt.Sprintf(`vfs.Caller{UID: 502, GID: 30}'s SetAttr %s of "f.bin", with an allow-all policy`,
		sa24Describe(sa))
	r := sa24SetAttr(e, fs, sa24Other, h, sa, what, where)
	if r.err != nil {
		t.Fatalf("%s = %v, want nil: with f.perm allowing everything, no check of SetAttr's remains to refuse "+
			"it (ADR 0024 section 2, \"The checks are the policy's\"; ADR 0014 section 11, W3; %s)", what, r.err,
			where)
	}
	if d := sa24Unapplied(r.v, sa); d != "" {
		t.Errorf("%s returned attributes with %s; want every value sa sets (ADR 0024 section 2; %s)", what, d, where)
	}
	if got := sa24GetAttr(e, fs, h, `GetAttr of "f.bin" after `+what, where); sa24Unapplied(got, sa) != "" {
		t.Errorf("after %s, GetAttr reports %s; want every value sa sets (ADR 0024 section 2; %s)", what,
			sa24Unapplied(got, sa), where)
	}
}
