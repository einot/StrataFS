package blobfs

// Clean-room tests for the PR that completes ADR 0014
// (docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md)
// section 6, wiring every permission check of the FS through f.perm, written
// without reading the implementation, from ADR 0014 sections 5 (the policy
// API), 6 and 11 (W2 to W5 in detail, and the safety rule), the facts of ADR
// 0024 section 7 and ADR 0023 section 11, the safety rules of ADR 0011 section
// 7, and internal/vfs/vfs.go.
//
// Covered, with the names ADR 0014 section 11 pins:
//   - W2, TestEveryMethodAsksThePolicyAndStopsAtItsRefusal: every method but
//     SetAttr (whose W2 is ADR 0024 section 7's S4) asks f.perm for the
//     requests listed, in order, and a refusal is returned bare and stops the
//     call having changed nothing;
//   - W2 for the requested uid,
//     TestARequestedUIDAtCreationGoesThroughOpChownAndARefusalIsIgnored;
//   - W3, TestNoCheckRemainsOutsideThePolicy: with an allow-all policy no
//     check of these methods remains to refuse a caller;
//   - W4, TestTheUnixRowsAreUnchangedThroughTheFS: a characterisation of
//     Unix() through the FS, which passes before and after the wiring;
//   - W5, TestGetAttrReadLinkFSStatCommitAndAccessConsultNoPolicy.
//
// W1, the seam, is shown by how these tests install a policy and has no test
// of its own. FS.perm is the one FS field this file writes, and only as W1
// allows: once, right after New returns and before any other call on the FS,
// in uc43Go, through sp24Mount.
//
// The safety rules (ADR 0014 section 11; ADR 0024 section 7; ADR 0023 section
// 11; ADR 0011 section 7): every bucket is a *store.Local under the test's
// t.TempDir(), behind an unarmed uc43Store, through uc43Setup; every mount is
// made with uc43Config, so SnapshotRetention and MaxDirtyBytes are -1; every
// New and every FS call runs in uc43Go and is waited for under uc43Bound;
// nothing sleeps, polls or runs in parallel; FS.dirty and FS.changes are read
// only holding FS.mu for reading, through sa24Read. Every call made under a
// policy that allows more than Unix() (every recorder, and the allow-all) is
// on an object of the type its operation needs: directories for Lookup,
// ReadDir, Create, Mkdir, Symlink, Remove, Rmdir and Rename, regular files for
// Read and Write.
//
// The setup's Create, Mkdir, Symlink and Write calls through uc43Env's
// methods are made as uc43Caller, which is vfs.Caller{UID: 501, GID: 20}, the
// same identity as testCaller.
//
// Not covered, on purpose: ADR 0014 section 11's unpinned rows U1 to U6 (an
// owner lacking its own bits, read through execute alone, Read or Write of a
// symlink, the sticky bit, AccessBits for a directory with read but not
// search or write but not search, times set by a non-owner), how sa.GID at
// creation is handled (#98), and the Dst field of any request but OpRename's
// and the UID field of any request but OpChown's, which ADR 0014 section 5
// says no row reads.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"strata/internal/policy"
	"strata/internal/vfs"
)

// pwMember is ADR 0014 section 11's primary-group member: in group 20, the
// group of everything testCaller makes, and the owner of none of it.
var pwMember = vfs.Caller{UID: 503, GID: 20}

// pwAllAccess is every ACCESS3 bit vfs declares.
const pwAllAccess = vfs.AccessRead | vfs.AccessLookup | vfs.AccessModify | vfs.AccessExtend | vfs.AccessDelete |
	vfs.AccessExecute

// pwDir, pwReg and pwLnk are the Objects ADR 0014 section 11 names: D, F and
// E, and the symlinks, as testCaller makes them with no Mode (ADR 0014
// section 11, the facts).
var (
	pwDir = policy.Object{Type: vfs.TypeDir, Mode: 0o755, UID: 501, GID: 20}
	pwReg = policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: 501, GID: 20}
	pwLnk = policy.Object{Type: vfs.TypeLnk, Mode: 0o777, UID: 501, GID: 20}
)

// pwBytes returns n bytes that differ from one seed to another.
func pwBytes(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)*7 + seed
	}
	return b
}

// pwOpName names a policy.Op for failure messages.
func pwOpName(op policy.Op) string {
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
	return fmt.Sprintf("Op(%d)", op)
}

// pwObjString spells an Object for failure messages.
func pwObjString(o policy.Object) string {
	return fmt.Sprintf("{Type %d, Mode %#o, UID %d, GID %d}", o.Type, o.Mode, o.UID, o.GID)
}

// pwDescribe spells a list of requests for failure messages.
func pwDescribe(reqs []policy.Request) string {
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = fmt.Sprintf("%s{Caller uid %d gid %d gids %v, Object %s, Dst %s, UID %d, GID %d}", pwOpName(r.Op),
			r.Caller.UID, r.Caller.GID, r.Caller.GIDs, pwObjString(r.Object), pwObjString(r.Dst), r.UID, r.GID)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// pwStatus names what a call returned: nil, a bare vfs.Status by its name,
// or anything else as it is, marked as not a bare status.
func pwStatus(err error) string {
	if err == nil {
		return "nil"
	}
	s, ok := err.(vfs.Status)
	if !ok {
		return fmt.Sprintf("%v (a %T, not a bare vfs.Status)", err, err)
	}
	switch s {
	case vfs.ErrPerm:
		return "vfs.ErrPerm"
	case vfs.ErrNoEnt:
		return "vfs.ErrNoEnt"
	case vfs.ErrIO:
		return "vfs.ErrIO"
	case vfs.ErrNXIO:
		return "vfs.ErrNXIO"
	case vfs.ErrAcces:
		return "vfs.ErrAcces"
	case vfs.ErrExist:
		return "vfs.ErrExist"
	case vfs.ErrNotDir:
		return "vfs.ErrNotDir"
	case vfs.ErrIsDir:
		return "vfs.ErrIsDir"
	case vfs.ErrInval:
		return "vfs.ErrInval"
	case vfs.ErrStale:
		return "vfs.ErrStale"
	case vfs.ErrNotEmpty:
		return "vfs.ErrNotEmpty"
	}
	return fmt.Sprintf("vfs.Status(%d)", uint32(s))
}

// pwWho names a caller for failure messages.
func pwWho(c vfs.Caller) string {
	switch {
	case c.UID == testCaller.UID && c.GID == testCaller.GID:
		return "testCaller"
	case c.UID == sa24Other.UID && c.GID == sa24Other.GID:
		return "sa24Other (vfs.Caller{UID: 502, GID: 30})"
	case c.UID == pwMember.UID && c.GID == pwMember.GID:
		return "the group member (vfs.Caller{UID: 503, GID: 20})"
	case c.UID == 0:
		return "root"
	}
	return fmt.Sprintf("vfs.Caller{UID: %d, GID: %d}", c.UID, c.GID)
}

// pwRefusing says which request a recorder refuses, for failure messages.
func pwRefusing(i int) string {
	if i < 0 {
		return "the policy refusing nothing"
	}
	return fmt.Sprintf("the policy refusing its request at index %d", i)
}

// pwWant is one request a test expects: its Op and Object, its Dst if it is
// an OpRename, and its UID if it is an OpChown.
type pwWant struct {
	op  policy.Op
	obj policy.Object
	dst policy.Object
	uid uint32
}

// pwDescribeWant spells a list of expected requests for failure messages.
func pwDescribeWant(want []pwWant) string {
	parts := make([]string, len(want))
	for i, w := range want {
		s := pwOpName(w.op) + " on " + pwObjString(w.obj)
		switch w.op {
		case policy.OpRename:
			s += " with Dst " + pwObjString(w.dst)
		case policy.OpChown:
			s += fmt.Sprintf(" with UID %d", w.uid)
		}
		parts[i] = s
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// pwWantRequests requires got to be exactly want, in order, each made by
// caller with the Object want gives, and with its Dst for an OpRename and its
// UID for an OpChown (ADR 0014 section 5).
func pwWantRequests(t *testing.T, what string, got []policy.Request, want []pwWant, caller vfs.Caller, why,
	where string) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(want); i++ {
		g, w := got[i], want[i]
		switch {
		case g.Op != w.op, g.Object != w.obj:
			ok = false
		case w.op == policy.OpRename && g.Dst != w.dst:
			ok = false
		case w.op == policy.OpChown && g.UID != w.uid:
			ok = false
		case g.Caller.UID != caller.UID || g.Caller.GID != caller.GID || len(g.Caller.GIDs) != len(caller.GIDs):
			ok = false
		}
	}
	if ok {
		return
	}
	t.Errorf("%s asked the policy %s; want exactly %s, in that order, each with Caller uid %d gid %d: %s (%s)", what,
		pwDescribe(got), pwDescribeWant(want), caller.UID, caller.GID, why, where)
}

// pwEntry is what one Create, Mkdir, Symlink or Lookup returned.
type pwEntry struct {
	h   vfs.Handle
	a   vfs.Attr
	err error
}

// pwCreate runs fs.Create of name in dir, as c, with sa.
func pwCreate(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, name string, sa vfs.SetAttr, excl bool, what,
	where string) pwEntry {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() pwEntry {
		h, a, err := fs.Create(ctx, c, dir, name, sa, excl)
		return pwEntry{h, a, err}
	}).wait(e.t, what, where)
}

// pwMkdir runs fs.Mkdir of name in dir, as c, with sa.
func pwMkdir(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, name string, sa vfs.SetAttr, what,
	where string) pwEntry {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() pwEntry {
		h, a, err := fs.Mkdir(ctx, c, dir, name, sa)
		return pwEntry{h, a, err}
	}).wait(e.t, what, where)
}

// pwSymlink runs fs.Symlink of name in dir, to target, as c, with sa.
func pwSymlink(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, name, target string, sa vfs.SetAttr, what,
	where string) pwEntry {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() pwEntry {
		h, a, err := fs.Symlink(ctx, c, dir, name, target, sa)
		return pwEntry{h, a, err}
	}).wait(e.t, what, where)
}

// pwLookup runs fs.Lookup of name in dir, as c.
func pwLookup(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, name, what, where string) pwEntry {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() pwEntry {
		h, a, err := fs.Lookup(ctx, c, dir, name)
		return pwEntry{h, a, err}
	}).wait(e.t, what, where)
}

// pwMust requires r to have succeeded, as setup, and returns its handle.
func pwMust(e *uc43Env, r pwEntry, what, where string) vfs.Handle {
	e.t.Helper()
	if r.err != nil {
		e.t.Fatalf("setup: %s = %s, want nil (%s)", what, pwStatus(r.err), where)
	}
	return r.h
}

// pwReadDir runs one fs.ReadDir of dir, as c, from cookie 0 with the zero
// verifier and a limit of 4096 bytes.
func pwReadDir(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, plus bool, what, where string) uc43R[[]vfs.DirEntry] {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() uc43R[[]vfs.DirEntry] {
		var verf [8]byte
		ents, _, _, err := fs.ReadDir(ctx, c, dir, 0, verf, 4096, plus)
		return uc43R[[]vfs.DirEntry]{ents, err}
	}).wait(e.t, what, where)
}

// pwErr runs fn, an FS call returning only an error, in uc43Go.
func pwErr(e *uc43Env, what, where string, fn func() error) error {
	e.t.Helper()
	return uc43Go(e, fn).wait(e.t, what, where)
}

// pwRemove runs fs.Remove of name in dir, as c.
func pwRemove(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, name, what, where string) error {
	e.t.Helper()
	ctx := e.ctx
	return pwErr(e, what, where, func() error { return fs.Remove(ctx, c, dir, name) })
}

// pwRmdir runs fs.Rmdir of name in dir, as c.
func pwRmdir(e *uc43Env, fs *FS, c vfs.Caller, dir vfs.Handle, name, what, where string) error {
	e.t.Helper()
	ctx := e.ctx
	return pwErr(e, what, where, func() error { return fs.Rmdir(ctx, c, dir, name) })
}

// pwRename runs fs.Rename of fromName in fromDir to toName in toDir, as c.
func pwRename(e *uc43Env, fs *FS, c vfs.Caller, fromDir vfs.Handle, fromName string, toDir vfs.Handle,
	toName, what, where string) error {
	e.t.Helper()
	ctx := e.ctx
	return pwErr(e, what, where, func() error { return fs.Rename(ctx, c, fromDir, fromName, toDir, toName) })
}

// pwRead runs one fs.Read of count bytes at off of h, as c.
func pwRead(e *uc43Env, fs *FS, c vfs.Caller, h vfs.Handle, off uint64, count uint32, what,
	where string) sa24ReadResult {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() sa24ReadResult {
		data, eof, err := fs.Read(ctx, c, h, off, count)
		return sa24ReadResult{data, eof, err}
	}).wait(e.t, what, where)
}

// pwWrite runs one fs.Write of data at off of h, as c, vfs.Unstable.
func pwWrite(e *uc43Env, fs *FS, c vfs.Caller, h vfs.Handle, off uint64, data []byte, what,
	where string) uc43R[uint32] {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() uc43R[uint32] {
		n, _, err := fs.Write(ctx, c, h, off, data, vfs.Unstable)
		return uc43R[uint32]{n, err}
	}).wait(e.t, what, where)
}

// pwAccess runs fs.Access of h, as c, asking for want.
func pwAccess(e *uc43Env, fs *FS, c vfs.Caller, h vfs.Handle, want uint32, what, where string) uc43R[uint32] {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() uc43R[uint32] {
		got, err := fs.Access(ctx, c, h, want)
		return uc43R[uint32]{got, err}
	}).wait(e.t, what, where)
}

// pwGetAttr runs fs.GetAttr of h and returns what it returned.
func pwGetAttr(e *uc43Env, fs *FS, h vfs.Handle, what, where string) uc43R[vfs.Attr] {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() uc43R[vfs.Attr] {
		a, err := fs.GetAttr(ctx, h)
		return uc43R[vfs.Attr]{a, err}
	}).wait(e.t, what, where)
}

// pwSetupObject requires h, made in setup, to be the Object want, and its
// size to be size unless size is negative, and returns its attributes (ADR
// 0014 section 11, the facts).
func pwSetupObject(e *uc43Env, fs *FS, h vfs.Handle, name string, want policy.Object, size int,
	where string) vfs.Attr {
	e.t.Helper()
	a := sa24GetAttr(e, fs, h, fmt.Sprintf("setup: GetAttr of %q", name), where)
	got := policy.Object{Type: a.Type, Mode: a.Mode, UID: a.UID, GID: a.GID}
	if got != want || (size >= 0 && a.Size != uint64(size)) {
		e.t.Fatalf("setup: %q is %s of size %d, want %s of size %d: a Mode given is applied masked to 0o7777, and "+
			"with none Create makes 0o644, Mkdir 0o755 and Symlink 0o777, owned by the caller (ADR 0014 section 11, "+
			"the facts; %s)", name, pwObjString(got), a.Size, pwObjString(want), size, where)
	}
	return a
}

// pwWantFound requires testCaller's Lookup of name in dir to find the object
// whose file id is id.
func pwWantFound(e *uc43Env, fs *FS, dir vfs.Handle, path, name string, id uint64, after, why, where string) {
	e.t.Helper()
	l := pwLookup(e, fs, testCaller, dir, name, fmt.Sprintf("testCaller's Lookup of %q after %s", path, after), where)
	if l.err != nil || l.a.FileID != id {
		e.t.Errorf("after %s, testCaller's Lookup of %q = (file id %d, %s), want (file id %d, nil): %s (%s)", after,
			path, l.a.FileID, pwStatus(l.err), id, why, where)
	}
}

// pwWantNoEnt requires testCaller's Lookup of name in dir to return
// vfs.ErrNoEnt.
func pwWantNoEnt(e *uc43Env, fs *FS, dir vfs.Handle, path, name, after, why, where string) {
	e.t.Helper()
	l := pwLookup(e, fs, testCaller, dir, name, fmt.Sprintf("testCaller's Lookup of %q after %s", path, after), where)
	if l.err != vfs.ErrNoEnt {
		e.t.Errorf("after %s, testCaller's Lookup of %q = %s, want vfs.ErrNoEnt: %s (%s)", after, path,
			pwStatus(l.err), why, where)
	}
}

// pwTree is W2's tree (ADR 0014 section 11): the root; the directories d and
// d2 in it; in d the file f, with payload written at 0, the file e, never
// written, and the symlink ln, to "f"; all committed by a Sync.
type pwTree struct {
	root, d, d2, f, fe, ln vfs.Handle
	fAttr, eAttr, d2Attr   vfs.Attr
	payload                []byte
}

// pwW2Tree makes W2's tree as testCaller, under whatever policy fs has, and
// requires FS.dirty to be false after its Sync and every object to be as ADR
// 0014 section 11's facts say.
func pwW2Tree(e *uc43Env, fs *FS, root vfs.Handle, where string) pwTree {
	e.t.Helper()
	s := pwTree{root: root, payload: pwBytes(100, 3)}
	s.d = e.mkdir(fs, root, "d", where)
	s.d2 = e.mkdir(fs, root, "d2", where)
	s.f = e.create(fs, s.d, "f", where)
	e.write(fs, s.f, 0, s.payload, `setup: Write of 100 bytes at 0 of "d/f", vfs.Unstable`, where)
	s.fe = e.create(fs, s.d, "e", where)
	s.ln = e.symlink(fs, s.d, "ln", "f", where)
	if err := e.sync(fs, "setup: the Sync that commits W2's tree", where); err != nil {
		e.t.Fatalf("setup: the Sync that commits W2's tree = %v, want nil against a healthy store (%s)", err, where)
	}
	if f := sa24Read(e, fs, "setup: reading FS.dirty after the Sync", where); f.dirty {
		e.t.Fatalf("setup: FS.dirty is true after a Sync that returned nil with no other call running, want false "+
			"(ADR 0023 section 3; %s)", where)
	}
	pwSetupObject(e, fs, root, "the root", pwDir, -1, where)
	pwSetupObject(e, fs, s.d, "d", pwDir, -1, where)
	s.d2Attr = pwSetupObject(e, fs, s.d2, "d2", pwDir, -1, where)
	s.fAttr = pwSetupObject(e, fs, s.f, "d/f", pwReg, 100, where)
	s.eAttr = pwSetupObject(e, fs, s.fe, "d/e", pwReg, 0, where)
	pwSetupObject(e, fs, s.ln, "d/ln", pwLnk, -1, where)
	return s
}

// pwRow is one row of W2: a call by testCaller, the index the recorder
// refuses, what the call must return, the requests it must make, and the
// row's check that nothing changed, which runs with the recorder refusing
// nothing.
type pwRow struct {
	what    string
	refuse  int
	call    func(what string) error
	wantErr error
	want    []pwWant
	why     string
	after   func(what string)
}

// TestEveryMethodAsksThePolicyAndStopsAtItsRefusal is W2 of ADR 0014 section
// 11, for every method but SetAttr, whose W2 is ADR 0024 section 7's S4: each
// asks f.perm for the requests section 6 lists, in order, each with Caller
// the caller and Object the inode as it stood; returns the first refusal bare
// (section 5) and asks nothing after it; and, refused, changes nothing, so
// FS.changes is what it was and FS.dirty is still false.
//
// sp24Mount installs an sp24Recorder; refusing nothing, testCaller makes W2's
// tree (pwW2Tree). D, F and E are the Objects {TypeDir, 0o755, 501, 20},
// {TypeReg, 0o644, 501, 20} and the same as F; d2's and the root's Object
// equal D. Then, for each row: the recorder is reset to refuse the row's
// index; FS.changes is read, c; testCaller makes the call; it returns
// vfs.ErrNXIO, a status no row of Unix() returns, unless the row says
// otherwise; the recorder holds exactly the row's requests; FS.changes is c
// and FS.dirty is false; and the row's check that nothing changed holds,
// made after reset(-1).
//
// The rows are ADR 0014 section 11's. They run in its order except that the
// two Reads the policy allows whole come last, after every refused call, so
// that a failure of either cannot make a later refused row's FS.dirty check
// fail with it. Every call is on an object of the type its operation needs
// (ADR 0014 section 11's safety rule).
func TestEveryMethodAsksThePolicyAndStopsAtItsRefusal(t *testing.T) {
	const where = "W2, ADR 0014 sections 5, 6 and 11"
	e := uc43Setup(t)
	rec := &sp24Recorder{refuseAt: -1}
	fs, root := sp24Mount(e, policy.Func(rec.check), where)
	s := pwW2Tree(e, fs, root, where)
	tc := testCaller
	D, F, E := pwDir, pwReg, pwReg
	ten := pwBytes(10, 151)

	const refused = "the call is refused at its first request, which section 6 says it makes before anything else"
	addEntry := func(name string) func(string) {
		return func(what string) {
			pwWantNoEnt(e, fs, s.d, "d/"+name, name, what, "a refused OpAddEntry makes no entry (ADR 0014 section 6)",
				where)
		}
	}

	nx := vfs.ErrNXIO
	lookD := []pwWant{{op: policy.OpLookup, obj: D}}
	listD := []pwWant{{op: policy.OpReadDir, obj: D}}
	addD := []pwWant{{op: policy.OpAddEntry, obj: D}}
	removeD := []pwWant{{op: policy.OpRemoveEntry, obj: D}}
	renameDD := []pwWant{{op: policy.OpRename, obj: D, dst: D}}
	readF := []pwWant{{op: policy.OpRead, obj: F}}
	readFF := []pwWant{{op: policy.OpRead, obj: F}, {op: policy.OpRead, obj: F}}
	readE := []pwWant{{op: policy.OpRead, obj: E}}
	writeF := []pwWant{{op: policy.OpWrite, obj: F}}

	// Each row is pwRow{what, refuse, call, wantErr, want, why, after}.
	rows := []pwRow{
		{
			`testCaller's Lookup(d, "f")`, 0,
			func(what string) error { return pwLookup(e, fs, tc, s.d, "f", what, where).err },
			nx, lookD,
			"Lookup asks OpLookup on the directory (ADR 0014 sections 5 and 6)",
			nil,
		},
		{
			`testCaller's ReadDir(d, 0, zero verifier, 4096, plus false)`, 0,
			func(what string) error { return pwReadDir(e, fs, tc, s.d, false, what, where).err },
			nx, listD,
			"ReadDir asks OpReadDir on the directory (ADR 0014 sections 5 and 6)",
			nil,
		},
		{
			`testCaller's ReadDir(d, 0, zero verifier, 4096, plus true)`, 0,
			func(what string) error { return pwReadDir(e, fs, tc, s.d, true, what, where).err },
			nx, listD,
			"ReadDir asks OpReadDir on the directory, with plus as without (ADR 0014 sections 5 and 6)",
			nil,
		},
		{
			`testCaller's Create(d, "new", vfs.SetAttr{}, exclusive)`, 0,
			func(what string) error { return pwCreate(e, fs, tc, s.d, "new", vfs.SetAttr{}, true, what, where).err },
			nx, addD,
			"Create asks OpAddEntry on the directory, " + refused,
			addEntry("new"),
		},
		{
			`testCaller's Mkdir(d, "sub", vfs.SetAttr{})`, 0,
			func(what string) error { return pwMkdir(e, fs, tc, s.d, "sub", vfs.SetAttr{}, what, where).err },
			nx, addD,
			"Mkdir asks OpAddEntry on the directory, " + refused,
			addEntry("sub"),
		},
		{
			`testCaller's Symlink(d, "s", "f", vfs.SetAttr{})`, 0,
			func(what string) error { return pwSymlink(e, fs, tc, s.d, "s", "f", vfs.SetAttr{}, what, where).err },
			nx, addD,
			"Symlink asks OpAddEntry on the directory, " + refused,
			addEntry("s"),
		},
		{
			`testCaller's Remove(d, "f")`, 0,
			func(what string) error { return pwRemove(e, fs, tc, s.d, "f", what, where) },
			nx, removeD,
			"Remove asks OpRemoveEntry on the directory, " + refused,
			func(what string) {
				if r := pwGetAttr(e, fs, s.f, `GetAttr of "d/f" after `+what, where); r.err != nil {
					t.Errorf("after %s, GetAttr of \"d/f\"'s handle = %s, want nil: a refused Remove takes no name away "+
						"(ADR 0014 section 6; %s)", what, pwStatus(r.err), where)
				}
				pwWantFound(e, fs, s.d, "d/f", "f", s.fAttr.FileID, what,
					"a refused Remove takes no name away (ADR 0014 section 6)", where)
			},
		},
		{
			`testCaller's Rmdir(root, "d2")`, 0,
			func(what string) error { return pwRmdir(e, fs, tc, s.root, "d2", what, where) },
			nx, removeD,
			"Rmdir asks OpRemoveEntry on the directory, the root, whose Object equals D, " + refused,
			func(what string) {
				pwWantFound(e, fs, s.root, "d2", "d2", s.d2Attr.FileID, what,
					"a refused Rmdir takes no name away (ADR 0014 section 6)", where)
			},
		},
		{
			`testCaller's Rename(d, "f", d2, "g")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.d, "f", s.d2, "g", what, where) },
			nx, renameDD,
			"Rename asks one OpRename, with Object the source directory and Dst the target directory, " + refused,
			func(what string) {
				pwWantFound(e, fs, s.d, "d/f", "f", s.fAttr.FileID, what,
					"a refused Rename moves nothing (ADR 0014 section 6)", where)
				pwWantNoEnt(e, fs, s.d2, "d2/g", "g", what, "a refused Rename makes no name (ADR 0014 section 6)",
					where)
			},
		},
		{
			`testCaller's Rename(d, "f", d, "g")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.d, "f", s.d, "g", what, where) },
			nx, renameDD,
			"Rename within one directory asks one OpRename with that directory as both Object and Dst, " + refused,
			func(what string) {
				pwWantFound(e, fs, s.d, "d/f", "f", s.fAttr.FileID, what,
					"a refused Rename moves nothing (ADR 0014 section 6)", where)
				pwWantNoEnt(e, fs, s.d, "d/g", "g", what, "a refused Rename makes no name (ADR 0014 section 6)", where)
			},
		},
		{
			`testCaller's Read(f, 0, 10)`, 0,
			func(what string) error { return pwRead(e, fs, tc, s.f, 0, 10, what, where).err },
			nx, readF,
			"Read asks OpRead on the file at its step 1 (ADR 0014 sections 5 and 6)",
			nil,
		},
		{
			`testCaller's Read(f, 0, 10)`, 1,
			func(what string) error { return pwRead(e, fs, tc, s.f, 0, 10, what, where).err },
			nx, readFF,
			"f has an FS.open entry, since it was written, so Read asks OpRead again at its step 2, and that " +
				"refusal is returned too (ADR 0014 sections 6 and 11; ADR 0005 section 3)",
			nil,
		},
		{
			`testCaller's Read(e, 0, 10)`, 0,
			func(what string) error { return pwRead(e, fs, tc, s.fe, 0, 10, what, where).err },
			nx, readE,
			"Read asks OpRead on the file at its step 1 (ADR 0014 sections 5 and 6)",
			nil,
		},
		{
			`testCaller's Write(f, 0, 10 bytes, vfs.Unstable)`, 0,
			func(what string) error { return pwWrite(e, fs, tc, s.f, 0, ten, what, where).err },
			nx, writeF,
			"Write asks OpWrite on the file, " + refused,
			func(what string) {
				if r := pwGetAttr(e, fs, s.f, `GetAttr of "d/f" after `+what, where); r.err != nil || r.v.Size != 100 {
					t.Errorf("after %s, GetAttr of \"d/f\" = (size %d, %s), want (size 100, nil): a refused Write "+
						"writes nothing (ADR 0014 section 6; %s)", what, r.v.Size, pwStatus(r.err), where)
				}
				rd := pwRead(e, fs, tc, s.f, 0, 100, `Read of 100 bytes at 0 of "d/f" after `+what, where)
				if rd.err != nil || !bytes.Equal(rd.data, s.payload) {
					t.Errorf("after %s, a Read of 100 bytes at 0 of \"d/f\", the policy refusing nothing, = (%x, %s), "+
						"want the 100 bytes written in setup, %x: a refused Write writes nothing (ADR 0014 section 6; "+
						"%s)", what, rd.data, pwStatus(rd.err), s.payload, where)
				}
			},
		},
		{
			`testCaller's Read(f, 0, 10)`, -1,
			func(what string) error {
				rd := pwRead(e, fs, tc, s.f, 0, 10, what, where)
				if rd.err == nil && !bytes.Equal(rd.data, s.payload[:10]) {
					t.Errorf("%s returned %x, want the first 10 bytes written, %x (ADR 0014 section 11, W2; %s)", what,
						rd.data, s.payload[:10], where)
				}
				return rd.err
			},
			nil, readFF,
			"f has an FS.open entry, so an allowed Read asks OpRead at step 1 and again at step 2 (ADR 0014 " +
				"sections 6 and 11)",
			nil,
		},
		{
			`testCaller's Read(e, 0, 10)`, -1,
			func(what string) error {
				rd := pwRead(e, fs, tc, s.fe, 0, 10, what, where)
				if rd.err == nil && (len(rd.data) != 0 || !rd.eof) {
					t.Errorf("%s = (%x, eof %v, nil), want no bytes, eof true and nil: e is empty, and a Read at or "+
						"past the size returns no bytes with eof (ADR 0014 section 11, the facts; %s)", what, rd.data,
						rd.eof, where)
				}
				return rd.err
			},
			nil, readE,
			"e was never written, so it has no FS.open entry, and an allowed Read asks OpRead at step 1 alone " +
				"(ADR 0014 sections 6 and 11)",
			nil,
		},
	}

	for _, r := range rows {
		what := r.what + ", " + pwRefusing(r.refuse)
		rec.reset(r.refuse)
		c := sa24Read(e, fs, "reading FS.changes before "+what, where).changes
		err := r.call(what)
		if err != r.wantErr {
			why := "the policy allowed every request, so the call succeeds"
			if r.wantErr != nil {
				why = "the policy's refusal is returned as the bare vfs.Status, unwrapped, and no check of the FS's " +
					"own returns anything else first (ADR 0014 sections 5 and 6)"
			}
			t.Errorf("%s = %s, want %s, compared with ==: %s (%s)", what, pwStatus(err), pwStatus(r.wantErr), why,
				where)
		}
		pwWantRequests(t, what, rec.taken(), r.want, tc, r.why, where)
		f := sa24Read(e, fs, "reading FS.dirty and FS.changes after "+what, where)
		if f.changes != c {
			t.Errorf("after %s, FS.changes is %d, want %d, its value before the call: the call changes nothing (ADR "+
				"0014 section 11, W2 and the facts; %s)", what, f.changes, c, where)
		}
		if f.dirty {
			t.Errorf("after %s, FS.dirty is true, want false, as it was after setup's Sync: the call changes nothing "+
				"(ADR 0014 section 11, W2 and the facts; %s)", what, where)
		}
		if r.after != nil {
			rec.reset(-1)
			r.after(what)
		}
	}
}

// pwWantOwner requires r, a creation that must have succeeded, to have
// returned attributes owned by uid, and gid unless gid is negative, and
// GetAttr of its handle to agree.
func pwWantOwner(e *uc43Env, fs *FS, r pwEntry, uid uint32, gid int64, what, why, where string) {
	e.t.Helper()
	if r.err != nil {
		e.t.Errorf("%s = %s, want nil: %s (%s)", what, pwStatus(r.err), why, where)
		return
	}
	bad := func(a vfs.Attr) bool { return a.UID != uid || (gid >= 0 && a.GID != uint32(gid)) }
	if bad(r.a) {
		e.t.Errorf("%s returned attributes owned by %d:%d, want uid %d (gid %d, -1 for any): %s (%s)", what, r.a.UID,
			r.a.GID, uid, gid, why, where)
	}
	if g := pwGetAttr(e, fs, r.h, "GetAttr after "+what, where); g.err != nil || bad(g.v) {
		e.t.Errorf("after %s, GetAttr of its handle = (owned by %d:%d, %s), want uid %d (gid %d, -1 for any) and "+
			"nil: %s (%s)", what, g.v.UID, g.v.GID, pwStatus(g.err), uid, gid, why, where)
	}
}

// TestARequestedUIDAtCreationGoesThroughOpChownAndARefusalIsIgnored is W2 for
// the requested uid, of ADR 0014 section 11: a Create, Mkdir or Symlink that
// is given a UID asks OpAddEntry on the directory and then OpChown with that
// UID on the new object as made, owned by the caller; an allowed OpChown sets
// the uid, whoever the caller; a refused one is ignored, and the object is
// made owned by the caller. A non-exclusive Create of a name that exists, and
// an exclusive one, ask OpAddEntry alone.
//
// W2's mount, recorder and d (pwW2Tree). sp24Root's Create of r1 with {UID 7},
// refusing index 1, returns nil, owned by 0:0, and asks [OpAddEntry on D,
// OpChown with Object {TypeReg, 0o644, 0, 0} and UID 7]; sp24Root's of r2,
// refusing nothing, gives uid 7 with the same two requests; testCaller's of
// r3 gives uid 7 too, with Object {TypeReg, 0o644, 501, 20}, which shows the
// decision is the policy's and not c.UID == 0's; testCaller's Mkdir of r4 and
// Symlink of r5 give uid 7 with Objects {TypeDir, 0o755, 501, 20} and
// {TypeLnk, 0o777, 501, 20}; its Create of r6 with {Mode 0o600, UID 7} asks
// OpChown on an Object of Mode 0o600. Then testCaller's non-exclusive Create
// of r1 with {UID 7} returns r1, still uid 0, asking [OpAddEntry on D], and
// its exclusive one vfs.ErrExist, asking the same. Every call is on the
// directory d (ADR 0014 section 11's safety rule); none sets a GID (#98).
func TestARequestedUIDAtCreationGoesThroughOpChownAndARefusalIsIgnored(t *testing.T) {
	const where = "W2 for the requested uid, ADR 0014 sections 5, 6 and 11"
	e := uc43Setup(t)
	rec := &sp24Recorder{refuseAt: -1}
	fs, root := sp24Mount(e, policy.Func(rec.check), where)
	s := pwW2Tree(e, fs, root, where)
	D := pwDir
	uid7 := vfs.SetAttr{UID: sa24U32(7)}
	chown := func(o policy.Object) []pwWant {
		return []pwWant{{op: policy.OpAddEntry, obj: D}, {op: policy.OpChown, obj: o, uid: 7}}
	}
	const reqWhy = "a creation given a UID asks OpAddEntry on the directory, then OpChown with that UID on the new " +
		"object as made, owned by the caller (ADR 0014 section 6)"

	// r1: root, the OpChown refused.
	what := `root's Create(d, "r1", {UID 7}, exclusive), ` + pwRefusing(1)
	rec.reset(1)
	r1 := pwCreate(e, fs, sp24Root, s.d, "r1", uid7, true, what, where)
	got := rec.taken()
	pwWantOwner(e, fs, r1, 0, 0, what, "a refused OpChown at creation is ignored: the file is made, owned by the "+
		"caller, uid 0 and gid 0 (ADR 0014 section 6)", where)
	pwWantRequests(t, what, got, chown(policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: 0, GID: 0}), sp24Root,
		reqWhy, where)

	// r2 to r5: the OpChown allowed.
	cases := []struct {
		what   string
		caller vfs.Caller
		call   func(what string) pwEntry
		obj    policy.Object
		why    string
	}{
		{`root's Create(d, "r2", {UID 7}, exclusive)`, sp24Root,
			func(what string) pwEntry { return pwCreate(e, fs, sp24Root, s.d, "r2", uid7, true, what, where) },
			policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: 0, GID: 0},
			"an allowed OpChown at creation sets the uid asked for"},
		{`testCaller's Create(d, "r3", {UID 7}, exclusive)`, testCaller,
			func(what string) pwEntry { return pwCreate(e, fs, testCaller, s.d, "r3", uid7, true, what, where) },
			pwReg, "an allowed OpChown sets the uid whoever the caller: the decision is the policy's, not c.UID == 0's"},
		{`testCaller's Mkdir(d, "r4", {UID 7})`, testCaller,
			func(what string) pwEntry { return pwMkdir(e, fs, testCaller, s.d, "r4", uid7, what, where) },
			pwDir, "Mkdir asks OpChown as Create does, on the directory as made"},
		{`testCaller's Symlink(d, "r5", "f", {UID 7})`, testCaller,
			func(what string) pwEntry { return pwSymlink(e, fs, testCaller, s.d, "r5", "f", uid7, what, where) },
			pwLnk, "Symlink asks OpChown as Create does, on the symlink as made"},
	}
	for _, tc := range cases {
		what := tc.what + ", " + pwRefusing(-1)
		rec.reset(-1)
		r := tc.call(what)
		got := rec.taken()
		pwWantOwner(e, fs, r, 7, -1, what, tc.why+" (ADR 0014 section 6)", where)
		pwWantRequests(t, what, got, chown(tc.obj), tc.caller, reqWhy, where)
	}

	// r6: a Mode given is applied before the OpChown is asked.
	what = `testCaller's Create(d, "r6", {Mode 0o600, UID 7}, exclusive), ` + pwRefusing(-1)
	rec.reset(-1)
	r6 := pwCreate(e, fs, testCaller, s.d, "r6", vfs.SetAttr{Mode: sa24U32(0o600), UID: sa24U32(7)}, true, what,
		where)
	got = rec.taken()
	if r6.err != nil {
		t.Errorf("%s = %s, want nil (ADR 0014 section 6; %s)", what, pwStatus(r6.err), where)
	}
	var chowns []policy.Request
	for _, q := range got {
		if q.Op == policy.OpChown {
			chowns = append(chowns, q)
		}
	}
	want6 := policy.Object{Type: vfs.TypeReg, Mode: 0o600, UID: 501, GID: 20}
	if len(chowns) != 1 || chowns[0].Object != want6 || chowns[0].UID != 7 {
		t.Errorf("%s asked the policy %s; want exactly one OpChown, with UID 7 and Object %s: the OpChown's Object "+
			"is the file as made, with the Mode given applied (ADR 0014 section 11, W2 for the requested uid; %s)",
			what, pwDescribe(got), pwObjString(want6), where)
	}

	// The name exists: OpAddEntry alone.
	what = `testCaller's Create(d, "r1", {UID 7}, non-exclusive) of a name that exists, ` + pwRefusing(-1)
	rec.reset(-1)
	r := pwCreate(e, fs, testCaller, s.d, "r1", uid7, false, what, where)
	got = rec.taken()
	if r.err != nil {
		t.Errorf("%s = %s, want nil, the handle of r1: a non-exclusive Create of a name that exists returns it (ADR "+
			"0014 section 11, W2 for the requested uid; %s)", what, pwStatus(r.err), where)
	} else {
		if r.a.FileID != r1.a.FileID || r.a.UID != 0 {
			t.Errorf("%s returned file id %d owned by uid %d, want r1's, file id %d, still uid 0: a Create of a name "+
				"that exists makes nothing and asks no OpChown (ADR 0014 section 11, W2 for the requested uid; %s)",
				what, r.a.FileID, r.a.UID, r1.a.FileID, where)
		}
		if g := pwGetAttr(e, fs, r.h, "GetAttr after "+what, where); g.err != nil || g.v.FileID != r1.a.FileID ||
			g.v.UID != 0 {
			t.Errorf("after %s, GetAttr of the handle it returned = (file id %d, uid %d, %s), want (file id %d, uid "+
				"0, nil): r1's uid is unchanged (ADR 0014 section 11, W2 for the requested uid; %s)", what,
				g.v.FileID, g.v.UID, pwStatus(g.err), r1.a.FileID, where)
		}
	}
	pwWantRequests(t, what, got, []pwWant{{op: policy.OpAddEntry, obj: D}}, testCaller,
		"a Create of a name that exists asks OpAddEntry on the directory and no OpChown (ADR 0014 section 11)", where)

	what = `testCaller's Create(d, "r1", {UID 7}, exclusive) of a name that exists, ` + pwRefusing(-1)
	rec.reset(-1)
	r = pwCreate(e, fs, testCaller, s.d, "r1", uid7, true, what, where)
	got = rec.taken()
	if r.err != vfs.ErrExist {
		t.Errorf("%s = %s, want vfs.ErrExist, compared with ==: an exclusive Create of a name that exists is "+
			"refused once OpAddEntry is allowed (ADR 0014 section 11, W2 for the requested uid; %s)", what,
			pwStatus(r.err), where)
	}
	pwWantRequests(t, what, got, []pwWant{{op: policy.OpAddEntry, obj: D}}, testCaller,
		"an exclusive Create of a name that exists asks OpAddEntry on the directory and no OpChown (ADR 0014 "+
			"section 11)", where)
}

// TestNoCheckRemainsOutsideThePolicy is W3 of ADR 0014 section 11: with f.perm
// replaced by a policy that allows everything, a caller whom every Unix() row
// would refuse succeeds at every method section 6 wires, so no check of
// theirs remains outside f.perm.
//
// sp24Mount installs policy.Func(func(policy.Request) error { return nil }).
// testCaller makes p, a directory of mode 0o700, and in it x, a file of mode
// 0o600 with 100 bytes written; Sync. sa24Other, neither owner nor group
// member of either: Lookup(p, "x") finds x; ReadDir(p, ...) lists x; Create
// of y, Mkdir of q and Symlink of s in p return nil; Read(x, 0, 100) returns
// the 100 bytes; Write(x, 100, 10 bytes) returns 10 and nil; Rename(p, "y",
// p, "z"), Remove(p, "z"), Remove(p, "s") and Rmdir(p, "q") return nil; and
// GetAttr of x reports size 110. Every object is of the type its operation
// needs (ADR 0014 section 11's safety rule).
func TestNoCheckRemainsOutsideThePolicy(t *testing.T) {
	const where = "W3, ADR 0014 sections 6 and 11"
	e := uc43Setup(t)
	fs, root := sp24Mount(e, policy.Func(func(policy.Request) error { return nil }), where)
	p := pwMust(e, pwMkdir(e, fs, testCaller, root, "p", vfs.SetAttr{Mode: sa24U32(0o700)}, `Mkdir "p" 0o700`,
		where), `testCaller's Mkdir "p" 0o700`, where)
	x := pwMust(e, pwCreate(e, fs, testCaller, p, "x", vfs.SetAttr{Mode: sa24U32(0o600)}, true, `Create "p/x" 0o600`,
		where), `testCaller's Create "p/x" 0o600`, where)
	payload := pwBytes(100, 41)
	e.write(fs, x, 0, payload, `setup: Write of 100 bytes at 0 of "p/x", vfs.Unstable`, where)
	if err := e.sync(fs, `setup: the Sync that commits "p" and "p/x"`, where); err != nil {
		t.Fatalf("setup: the Sync that commits %q and %q = %v, want nil against a healthy store (%s)", "p", "p/x", err,
			where)
	}
	pwSetupObject(e, fs, p, "p", policy.Object{Type: vfs.TypeDir, Mode: 0o700, UID: 501, GID: 20}, -1, where)
	xa := pwSetupObject(e, fs, x, "p/x", policy.Object{Type: vfs.TypeReg, Mode: 0o600, UID: 501, GID: 20}, 100, where)

	o := sa24Other
	const why = "with f.perm allowing everything, no check of the method's remains outside the policy to refuse " +
		"a caller whom Unix() would (ADR 0014 sections 6 and 11, W3)"
	wantNil := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Errorf("sa24Other's %s = %s, want nil: %s (%s)", what, pwStatus(err), why, where)
		}
	}

	what := `Lookup(p, "x")`
	l := pwLookup(e, fs, o, p, "x", what, where)
	wantNil(what, l.err)
	if l.err == nil && l.a.FileID != xa.FileID {
		t.Errorf("sa24Other's %s found file id %d, want x's, %d (%s)", what, l.a.FileID, xa.FileID, where)
	}

	what = "ReadDir(p, 0, zero verifier, 4096, plus false)"
	rd := pwReadDir(e, fs, o, p, false, what, where)
	wantNil(what, rd.err)
	if rd.err == nil {
		found := false
		for _, de := range rd.v {
			found = found || de.Name == "x"
		}
		if !found {
			t.Errorf("sa24Other's %s listed %d entries without \"x\", want x listed (ADR 0014 section 11, W3; %s)",
				what, len(rd.v), where)
		}
	}

	what = `Create(p, "y", vfs.SetAttr{}, exclusive)`
	wantNil(what, pwCreate(e, fs, o, p, "y", vfs.SetAttr{}, true, what, where).err)
	what = `Mkdir(p, "q", vfs.SetAttr{})`
	wantNil(what, pwMkdir(e, fs, o, p, "q", vfs.SetAttr{}, what, where).err)
	what = `Symlink(p, "s", "x", vfs.SetAttr{})`
	wantNil(what, pwSymlink(e, fs, o, p, "s", "x", vfs.SetAttr{}, what, where).err)

	what = "Read(x, 0, 100)"
	rr := pwRead(e, fs, o, x, 0, 100, what, where)
	wantNil(what, rr.err)
	if rr.err == nil && !bytes.Equal(rr.data, payload) {
		t.Errorf("sa24Other's %s returned %x, want the 100 bytes written, %x (ADR 0014 section 11, W3; %s)", what,
			rr.data, payload, where)
	}

	what = "Write(x, 100, 10 bytes, vfs.Unstable)"
	w := pwWrite(e, fs, o, x, 100, pwBytes(10, 97), what, where)
	wantNil(what, w.err)
	if w.err == nil && w.v != 10 {
		t.Errorf("sa24Other's %s wrote %d bytes, want 10 (ADR 0014 section 11, W3; %s)", what, w.v, where)
	}

	what = `Rename(p, "y", p, "z")`
	wantNil(what, pwRename(e, fs, o, p, "y", p, "z", what, where))
	what = `Remove(p, "z")`
	wantNil(what, pwRemove(e, fs, o, p, "z", what, where))
	what = `Remove(p, "s")`
	wantNil(what, pwRemove(e, fs, o, p, "s", what, where))
	what = `Rmdir(p, "q")`
	wantNil(what, pwRmdir(e, fs, o, p, "q", what, where))

	if g := pwGetAttr(e, fs, x, `GetAttr of "p/x" after sa24Other's calls`, where); g.err != nil || g.v.Size != 110 {
		t.Errorf("after sa24Other's calls, GetAttr of \"p/x\" = (size %d, %s), want (size 110, nil): its Write of 10 "+
			"bytes at 100 was applied (ADR 0014 section 11, W3; %s)", g.v.Size, pwStatus(g.err), where)
	}
}

// pwCheck is one row of W4: a caller, a call, and the status it must return.
type pwCheck struct {
	c    vfs.Caller
	call string
	fn   func(c vfs.Caller, what string) error
	want error
	why  string
}

// TestTheUnixRowsAreUnchangedThroughTheFS is W4 of ADR 0014 section 11, a
// characterisation that passes before and after the wiring: with Unix() left
// in place, each method returns, through the FS, the status its row of
// Unix() gives, and the checks that come before or after the policy (B, D
// and E of section 6) keep their order.
//
// e.newFS, with Unix() in place. testCaller makes, in the root: d, 0o755,
// holding f (0o644, with bytes), sub (a directory, 0o755) and x (a regular
// file, 0o755); priv, a directory 0o700; ro, a directory 0o555 holding k
// (0o644); wo, 0o333; rd, 0o644; g750 and g705, directories; secret, a
// regular file 0o600; fg640 and fg604, regular files of those modes with
// bytes; and fo, a regular file 0o664. ro is made 0o755, k made in it, and ro
// then set to 0o555 by its owner, so that no setup step relies on an owner
// lacking its own bits (U1). Each row is a call, its caller, and the status
// it must return, compared with == against the bare vfs.Status. Then the
// requested uid under Unix(), and Access with every bit asked for.
//
// Rows that would pin U1 to U6 are left out: no owner lacking its own bits, no
// read through execute alone, no Read or Write of a symlink, no sticky bit, no
// directory with read but not search, or write but not search, asked of
// Access, and no times set by a non-owner.
func TestTheUnixRowsAreUnchangedThroughTheFS(t *testing.T) {
	const where = "W4, ADR 0014 sections 5, 6 and 11"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)

	mode := func(m uint32) vfs.SetAttr { return vfs.SetAttr{Mode: sa24U32(m)} }
	mkdir := func(dir vfs.Handle, name string, m uint32) vfs.Handle {
		t.Helper()
		what := fmt.Sprintf("testCaller's Mkdir %q %#o", name, m)
		h := pwMust(e, pwMkdir(e, fs, testCaller, dir, name, mode(m), what, where), what, where)
		pwSetupObject(e, fs, h, name, policy.Object{Type: vfs.TypeDir, Mode: m, UID: 501, GID: 20}, -1, where)
		return h
	}
	create := func(dir vfs.Handle, name string, m uint32, data []byte) vfs.Handle {
		t.Helper()
		what := fmt.Sprintf("testCaller's Create %q %#o", name, m)
		h := pwMust(e, pwCreate(e, fs, testCaller, dir, name, mode(m), true, what, where), what, where)
		if data != nil {
			e.write(fs, h, 0, data, fmt.Sprintf("setup: Write of %d bytes at 0 of %q", len(data), name), where)
		}
		pwSetupObject(e, fs, h, name, policy.Object{Type: vfs.TypeReg, Mode: m, UID: 501, GID: 20}, len(data), where)
		return h
	}

	d := mkdir(root, "d", 0o755)
	f := create(d, "f", 0o644, pwBytes(20, 5))
	mkdir(d, "sub", 0o755)
	x := create(d, "x", 0o755, nil)
	priv := mkdir(root, "priv", 0o700)
	ro := mkdir(root, "ro", 0o755)
	create(ro, "k", 0o644, nil)
	if r := sa24SetAttr(e, fs, testCaller, ro, mode(0o555), `setup: testCaller's SetAttr {Mode 0o555} of "ro"`,
		where); r.err != nil || r.v.Mode != 0o555 {
		t.Fatalf("setup: testCaller's SetAttr {Mode 0o555} of \"ro\", which it owns, = (mode %#o, %s), want (mode "+
			"0555, nil) (%s)", r.v.Mode, pwStatus(r.err), where)
	}
	wo := mkdir(root, "wo", 0o333)
	rd := mkdir(root, "rd", 0o644)
	g750 := mkdir(root, "g750", 0o750)
	g705 := mkdir(root, "g705", 0o705)
	secret := create(root, "secret", 0o600, nil)
	fg640 := create(root, "fg640", 0o640, pwBytes(20, 9))
	fg604 := create(root, "fg604", 0o604, pwBytes(20, 13))
	fo := create(root, "fo", 0o664, nil)

	b := pwBytes(10, 77)
	lookup := func(dir vfs.Handle, name string) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error { return pwLookup(e, fs, c, dir, name, what, where).err }
	}
	readDir := func(dir vfs.Handle) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error { return pwReadDir(e, fs, c, dir, false, what, where).err }
	}
	createIn := func(dir vfs.Handle, name string) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error {
			return pwCreate(e, fs, c, dir, name, vfs.SetAttr{}, true, what, where).err
		}
	}
	mkdirIn := func(dir vfs.Handle, name string) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error {
			return pwMkdir(e, fs, c, dir, name, vfs.SetAttr{}, what, where).err
		}
	}
	symlinkIn := func(dir vfs.Handle, name, target string) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error {
			return pwSymlink(e, fs, c, dir, name, target, vfs.SetAttr{}, what, where).err
		}
	}
	remove := func(dir vfs.Handle, name string) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error { return pwRemove(e, fs, c, dir, name, what, where) }
	}
	rmdir := func(dir vfs.Handle, name string) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error { return pwRmdir(e, fs, c, dir, name, what, where) }
	}
	rename := func(from vfs.Handle, fromName string, to vfs.Handle, toName string) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error {
			return pwRename(e, fs, c, from, fromName, to, toName, what, where)
		}
	}
	read := func(h vfs.Handle) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error { return pwRead(e, fs, c, h, 0, 10, what, where).err }
	}
	write := func(h vfs.Handle) func(vfs.Caller, string) error {
		return func(c vfs.Caller, what string) error { return pwWrite(e, fs, c, h, 0, b, what, where).err }
	}

	o, m, rt, tc := sa24Other, pwMember, sp24Root, testCaller
	rows := []pwCheck{
		// OpLookup.
		{o, `Lookup(priv, "nope")`, lookup(priv, "nope"), vfs.ErrAcces,
			"a caller without search permission is refused before the name is looked for (D before E)"},
		{o, `Lookup(f, "nope")`, lookup(f, "nope"), vfs.ErrNotDir, "a Lookup in a non-directory"},
		{o, `Lookup(rd, "nope")`, lookup(rd, "nope"), vfs.ErrAcces, "read without search does not look up"},
		{m, `Lookup(g750, "nope")`, lookup(g750, "nope"), vfs.ErrNoEnt,
			"the group class may search 0o750, so the call is allowed and then finds no name (E)"},
		{m, `Lookup(g705, "nope")`, lookup(g705, "nope"), vfs.ErrAcces,
			"one class only: the group member is judged by the group bits, which 0o705 leaves empty"},
		{rt, `Lookup(priv, "nope")`, lookup(priv, "nope"), vfs.ErrNoEnt, "root may search any directory"},

		// OpReadDir.
		{o, "ReadDir(wo, ...)", readDir(wo), vfs.ErrAcces, "0o333 gives the other class no read"},
		{o, "ReadDir(f, ...)", readDir(f), vfs.ErrNotDir, "a ReadDir of a non-directory"},
		{o, "ReadDir(rd, ...)", readDir(rd), nil, "read without search lists"},

		// OpAddEntry.
		{o, `Create(ro, "n", vfs.SetAttr{}, exclusive)`, createIn(ro, "n"), vfs.ErrAcces,
			"no write permission on the directory"},
		{o, `Mkdir(ro, "n", vfs.SetAttr{})`, mkdirIn(ro, "n"), vfs.ErrAcces, "no write permission on the directory"},
		{o, `Symlink(ro, "n", "k", vfs.SetAttr{})`, symlinkIn(ro, "n", "k"), vfs.ErrAcces,
			"no write permission on the directory"},
		{o, `Create(f, "n", vfs.SetAttr{}, exclusive)`, createIn(f, "n"), vfs.ErrNotDir, "f is not a directory"},
		{o, `Mkdir(f, "n", vfs.SetAttr{})`, mkdirIn(f, "n"), vfs.ErrNotDir, "f is not a directory"},
		{o, `Symlink(f, "n", "k", vfs.SetAttr{})`, symlinkIn(f, "n", "k"), vfs.ErrNotDir, "f is not a directory"},
		{o, `Mkdir(ro, "k", vfs.SetAttr{})`, mkdirIn(ro, "k"), vfs.ErrAcces,
			"the permission check comes before the name's existence: vfs.ErrAcces, not vfs.ErrExist (D before E)"},
		{o, `Create(ro, "", vfs.SetAttr{}, exclusive)`, createIn(ro, ""), vfs.ErrInval,
			"an empty name is refused before the permission check (B before D)"},
		{rt, `Create(ro, "n", vfs.SetAttr{}, exclusive)`, createIn(ro, "n"), nil,
			"root may add an entry to any directory"},

		// OpRemoveEntry.
		{o, `Remove(ro, "k")`, remove(ro, "k"), vfs.ErrAcces, "no write permission on the directory"},
		{o, `Rmdir(ro, "k")`, rmdir(ro, "k"), vfs.ErrAcces, "no write permission on the directory"},
		{o, `Remove(ro, "nope")`, remove(ro, "nope"), vfs.ErrAcces,
			"the permission check comes before the name is looked for: vfs.ErrAcces, not vfs.ErrNoEnt (D before E)"},
		{o, `Remove(f, "k")`, remove(f, "k"), vfs.ErrNotDir, "f is not a directory"},
		{tc, `Remove(d, "sub")`, remove(d, "sub"), vfs.ErrIsDir,
			"the owner may remove from d, and then Remove refuses a directory (E, after D allowed)"},
		{tc, `Rmdir(d, "f")`, rmdir(d, "f"), vfs.ErrNotDir,
			"the owner may remove from d, and then Rmdir refuses a non-directory (E, after D allowed)"},

		// OpRename.
		{o, `Rename(ro, "k", d, "k2")`, rename(ro, "k", d, "k2"), vfs.ErrAcces,
			"no write permission on the source directory"},
		{o, `Rename(d, "f", ro, "k2")`, rename(d, "f", ro, "k2"), vfs.ErrAcces,
			"no write permission on the target directory"},
		{o, `Rename(ro, "nope", ro, "k2")`, rename(ro, "nope", ro, "k2"), vfs.ErrAcces,
			"the permission check comes before the name is looked for: vfs.ErrAcces, not vfs.ErrNoEnt"},
		{o, `Rename(f, "k", ro, "k2")`, rename(f, "k", ro, "k2"), vfs.ErrNotDir,
			"a non-directory source is refused before ro's refusal"},
		{o, `Rename(ro, "k", f, "k2")`, rename(ro, "k", f, "k2"), vfs.ErrNotDir, "a non-directory target"},

		// OpRead.
		{o, "Read(secret, 0, 10)", read(secret), vfs.ErrAcces, "0o600 gives the other class no read"},
		{o, "Read(d, 0, 10)", read(d), vfs.ErrIsDir, "a Read of a directory"},
		{o, "Read(fg604, 0, 10)", read(fg604), nil, "the other bits of 0o604 give read"},
		{m, "Read(fg640, 0, 10)", read(fg640), nil, "the group bits of 0o640 give read"},
		{m, "Read(fg604, 0, 10)", read(fg604), vfs.ErrAcces,
			"one class only: the group member is judged by the group bits, which 0o604 leaves empty"},

		// OpWrite.
		{o, "Write(f, 0, 10 bytes, vfs.Unstable)", write(f), vfs.ErrAcces, "0o644 gives the other class no write"},
		{o, "Write(d, 0, 10 bytes, vfs.Unstable)", write(d), vfs.ErrIsDir, "a Write of a directory"},
		{m, "Write(fo, 0, 10 bytes, vfs.Unstable)", write(fo), nil, "the group bits of 0o664 give write"},
		{rt, "Write(secret, 0, 10 bytes, vfs.Unstable)", write(secret), nil, "root may write any regular file"},
	}
	for _, r := range rows {
		what := fmt.Sprintf("%s's %s", pwWho(r.c), r.call)
		if err := r.fn(r.c, what); err != r.want {
			t.Errorf("%s = %s, want %s, compared with == against the bare vfs.Status: %s (ADR 0014 sections 5 and "+
				"6; section 11, W4; %s)", what, pwStatus(err), pwStatus(r.want), r.why, where)
		}
	}

	// The requested uid under Unix().
	uid7 := vfs.SetAttr{UID: sa24U32(7)}
	what := `testCaller's Create(d, "u1", {UID 7}, exclusive)`
	pwWantOwner(e, fs, pwCreate(e, fs, testCaller, d, "u1", uid7, true, what, where), 501, -1, what,
		"Unix() refuses a non-root caller's OpChown, and a refused OpChown at creation is ignored, so the file is "+
			"made owned by the caller (ADR 0014 sections 6 and 11, W4)", where)
	what = `root's Create(d, "u2", {UID 7}, exclusive)`
	pwWantOwner(e, fs, pwCreate(e, fs, sp24Root, d, "u2", uid7, true, what, where), 7, -1, what,
		"Unix() allows root's OpChown, so the file is made owned by the uid asked for (ADR 0014 sections 6 and 11, "+
			"W4)", where)

	// Access.
	acc := []struct {
		c    vfs.Caller
		name string
		h    vfs.Handle
		ask  uint32
		want uint32
		why  string
	}{
		{o, "d/f", f, pwAllAccess, vfs.AccessRead, "the other bits of 0o644 give read only"},
		{o, "secret", secret, pwAllAccess, 0, "0o600 gives the other class nothing"},
		{o, "d/x", x, pwAllAccess, vfs.AccessRead | vfs.AccessExecute, "the other bits of 0o755 give read and execute"},
		{o, "d", d, pwAllAccess, vfs.AccessRead | vfs.AccessLookup,
			"the other bits of a 0o755 directory give read and search"},
		{tc, "d/f", f, pwAllAccess, vfs.AccessRead | vfs.AccessModify | vfs.AccessExtend,
			"the owner bits of 0o644 give read and write"},
		{tc, "d", d, pwAllAccess,
			vfs.AccessRead | vfs.AccessLookup | vfs.AccessModify | vfs.AccessExtend | vfs.AccessDelete,
			"the owner bits of a 0o755 directory give read, search and write"},
		{rt, "d/f", f, pwAllAccess, vfs.AccessRead | vfs.AccessModify | vfs.AccessExtend,
			"root is granted read and write, and execute only where some execute bit is set, which 0o644 has none of"},
		{rt, "d/x", x, pwAllAccess, vfs.AccessRead | vfs.AccessModify | vfs.AccessExtend | vfs.AccessExecute,
			"root is granted read and write, and execute since 0o755 has execute bits"},
		{rt, "d", d, pwAllAccess, pwAllAccess &^ vfs.AccessExecute,
			"root is granted everything on a directory but AccessExecute, which no directory has"},
		{tc, "d/f", f, vfs.AccessRead, vfs.AccessRead, "Access answers only for the bits asked for"},
	}
	for _, a := range acc {
		what := fmt.Sprintf("%s's Access(%s, %#x)", pwWho(a.c), a.name, a.ask)
		r := pwAccess(e, fs, a.c, a.h, a.ask, what, where)
		if r.err != nil || r.v != a.want {
			t.Errorf("%s = (%#x, %s), want (%#x, nil): %s (ADR 0014 section 11, W4; %s)", what, r.v, pwStatus(r.err),
				a.want, a.why, where)
		}
	}
}

// TestGetAttrReadLinkFSStatCommitAndAccessConsultNoPolicy is W5 of ADR 0014
// section 11: GetAttr, ReadLink, FSStat, Commit and Access ask f.perm
// nothing, and Access answers from the mode bits and the mount, not from
// f.perm.
//
// W2's mount and recorder, with W2's tree (pwW2Tree); the recorder reset to
// refuse index 0 before each call: GetAttr(f) returns f's attributes with
// nil; ReadLink(ln) returns "f" with nil; FSStat(root) returns nil;
// Commit(f, 0, 0) returns nil; testCaller's Access(f, every bit) returns
// AccessRead|AccessModify|AccessExtend with nil; and after each the recorder
// has recorded nothing. Then, on a second sp24Mount, over a bucket of its
// own, with W3's allow-all policy, testCaller makes y, a regular file of mode
// 0o600, and sa24Other's Access(y, every bit) returns 0.
func TestGetAttrReadLinkFSStatCommitAndAccessConsultNoPolicy(t *testing.T) {
	const where = "W5, ADR 0014 sections 6 and 11"
	e := uc43Setup(t)
	rec := &sp24Recorder{refuseAt: -1}
	fs, root := sp24Mount(e, policy.Func(rec.check), where)
	s := pwW2Tree(e, fs, root, where)
	ctx := e.ctx

	asksNothing := func(what string) {
		t.Helper()
		if got := rec.taken(); len(got) != 0 {
			t.Errorf("%s asked the policy %s, want nothing: it consults no policy (ADR 0014 sections 6 and 11, W5; %s)",
				what, pwDescribe(got), where)
		}
	}

	what := `GetAttr of "d/f", ` + pwRefusing(0)
	rec.reset(0)
	g := pwGetAttr(e, fs, s.f, what, where)
	if g.err != nil || sa24Diff(s.fAttr, g.v) != "" {
		t.Errorf("%s = (%s), want f's attributes and nil: GetAttr consults no policy (ADR 0014 section 11, W5; %s)",
			what, sa24Diff(s.fAttr, g.v)+"; "+pwStatus(g.err), where)
	}
	asksNothing(what)

	what = `ReadLink of "d/ln", ` + pwRefusing(0)
	rec.reset(0)
	rl := uc43Go(e, func() uc43R[string] {
		v, err := fs.ReadLink(ctx, s.ln)
		return uc43R[string]{v, err}
	}).wait(t, what, where)
	if rl.err != nil || rl.v != "f" {
		t.Errorf("%s = (%q, %s), want (\"f\", nil): ReadLink consults no policy (ADR 0014 section 11, W5; %s)", what,
			rl.v, pwStatus(rl.err), where)
	}
	asksNothing(what)

	what = "FSStat of the root, " + pwRefusing(0)
	rec.reset(0)
	st := uc43Go(e, func() uc43R[vfs.Stat] {
		v, err := fs.FSStat(ctx, s.root)
		return uc43R[vfs.Stat]{v, err}
	}).wait(t, what, where)
	if st.err != nil {
		t.Errorf("%s = %s, want nil: FSStat consults no policy (ADR 0014 section 11, W5; %s)", what, pwStatus(st.err),
			where)
	}
	asksNothing(what)

	what = `Commit(f, 0, 0), ` + pwRefusing(0)
	rec.reset(0)
	if err := pwErr(e, what, where, func() error { return fs.Commit(ctx, s.f, 0, 0) }); err != nil {
		t.Errorf("%s = %s, want nil: Commit consults no policy (ADR 0014 section 11, W5; %s)", what, pwStatus(err),
			where)
	}
	asksNothing(what)

	what = `testCaller's Access(f, every bit), ` + pwRefusing(0)
	rec.reset(0)
	a := pwAccess(e, fs, testCaller, s.f, pwAllAccess, what, where)
	if want := vfs.AccessRead | vfs.AccessModify | vfs.AccessExtend; a.err != nil || a.v != want {
		t.Errorf("%s = (%#x, %s), want (%#x, nil): Access consults no policy, and answers the owner of a 0o644 file "+
			"read and write (ADR 0014 section 11, W5; %s)", what, a.v, pwStatus(a.err), want, where)
	}
	asksNothing(what)

	// A second mount, over a bucket of its own, with the allow-all policy.
	e2 := uc43Setup(t)
	fs2, root2 := sp24Mount(e2, policy.Func(func(policy.Request) error { return nil }), where+", the second mount")
	y := pwMust(e2, pwCreate(e2, fs2, testCaller, root2, "y", vfs.SetAttr{Mode: sa24U32(0o600)}, true,
		`testCaller's Create "y" 0o600`, where), `testCaller's Create "y" 0o600`, where)
	pwSetupObject(e2, fs2, y, "y", policy.Object{Type: vfs.TypeReg, Mode: 0o600, UID: 501, GID: 20}, 0, where)
	what = `sa24Other's Access(y, every bit), the policy allowing everything`
	a = pwAccess(e2, fs2, sa24Other, y, pwAllAccess, what, where)
	if a.err != nil || a.v != 0 {
		t.Errorf("%s = (%#x, %s), want (0, nil): Access answers from the mode bits and the mount, not from f.perm, "+
			"and 0o600 gives the other class nothing (ADR 0014 section 11, W5; %s)", what, a.v, pwStatus(a.err), where)
	}
}
