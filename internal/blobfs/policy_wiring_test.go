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
//     call having changed nothing. Its tree and its rows for Remove, Rmdir and
//     Rename are ADR 0026 section 7's, which replaces ADR 0014's for #148: no
//     two objects a row names are equal, and each request's Entry and
//     Replaced (ADR 0026 section 2) are checked;
//   - W2 for the requested uid,
//     TestARequestedUIDAtCreationGoesThroughOpChownAndARefusalIsIgnored;
//   - W3, TestNoCheckRemainsOutsideThePolicy: with an allow-all policy no
//     check of these methods remains to refuse a caller;
//   - W4, TestTheUnixRowsAreUnchangedThroughTheFS: a characterisation of
//     Unix() through the FS, which passes before and after the wiring, with
//     ADR 0026 section 7's two Rmdir rows for #148;
//   - W5, TestGetAttrReadLinkFSStatAndCommitConsultNoPolicy, which since ADR
//     0027 section 10's X12 no longer names Access: access_test.go's X10 and
//     access_wiring_test.go's X11 show that Access asks f.perm.
//
// Since ADR 0027 section 10's X12, the comparison W2's rows use,
// pwCompareRequests, also requires every request's Advisory to be false, so
// that W2 shows that no call marks its request (ADR 0027 section 2).
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
// Not covered, on purpose: ADR 0014 section 11's rows U1 to U6 (an owner
// lacking its own bits, read through execute alone, Read or Write of a
// symlink, the sticky bit, AccessBits for a directory with read but not
// search or write but not search, times set by a non-owner), how sa.GID at
// creation is handled (#98), and the Dst field of any request but OpRename's
// and the UID field of any request but OpChown's, which ADR 0014 section 5
// says no row reads. Of those rows, an owner lacking its own bits (U1) and
// read through execute alone (U2) are pinned for a regular file since ADR
// 0025 section 6, and are tested in owner_access_test.go; W4 still leaves
// them out. The sticky bit (U4) is pinned since ADR 0026 section 8, and is
// tested in sticky_test.go; W4 still has no sticky row. AccessBits for a
// directory with read but not search or write but not search (U5) is pinned
// since ADR 0027 section 10, and is tested in access_test.go; W4 still has no
// such row. U3 and U6 stay unpinned. W2's tree gives d2 the mode 0o1770, but
// under the recorder, which allows whatever it does not refuse, no mode bears
// on a verdict (ADR 0026 section 7).

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

// pwW2D, pwW2D2, pwW2E, pwW2DD, pwW2H and pwW2V are the Objects ADR 0026
// section 7 gives W2's tree for #148: D, D2, E, DD, H and V. With pwDir as R,
// the root, pwReg as F and pwLnk as LN, no two of the nine are equal.
var (
	pwW2D  = policy.Object{Type: vfs.TypeDir, Mode: 0o750, UID: 501, GID: 20}
	pwW2D2 = policy.Object{Type: vfs.TypeDir, Mode: 0o1770, UID: 501, GID: 20}
	pwW2E  = policy.Object{Type: vfs.TypeReg, Mode: 0o664, UID: 501, GID: 20}
	pwW2DD = policy.Object{Type: vfs.TypeDir, Mode: 0o711, UID: 501, GID: 20}
	pwW2H  = policy.Object{Type: vfs.TypeReg, Mode: 0o640, UID: 501, GID: 20}
	pwW2V  = policy.Object{Type: vfs.TypeReg, Mode: 0o600, UID: 501, GID: 20}
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

// pwObjPtrString spells an *Object for failure messages: nil, or what it
// points to.
func pwObjPtrString(o *policy.Object) string {
	if o == nil {
		return "nil"
	}
	return "&" + pwObjString(*o)
}

// pwSameObjPtr reports whether a and b are both nil, or both point to equal
// Objects.
func pwSameObjPtr(a, b *policy.Object) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// pwDescribe spells a list of requests for failure messages.
func pwDescribe(reqs []policy.Request) string {
	parts := make([]string, len(reqs))
	for i, r := range reqs {
		parts[i] = fmt.Sprintf("%s{Caller uid %d gid %d gids %v, Object %s, Dst %s, UID %d, GID %d, Entry %s, "+
			"Replaced %s, Advisory %v}", pwOpName(r.Op), r.Caller.UID, r.Caller.GID, r.Caller.GIDs,
			pwObjString(r.Object), pwObjString(r.Dst), r.UID, r.GID, pwObjPtrString(r.Entry),
			pwObjPtrString(r.Replaced), r.Advisory)
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
// an OpRename, and its UID if it is an OpChown; and, where the test checks
// them (pwWantW2Requests), its Entry and Replaced, nil for none (ADR 0026
// section 2).
type pwWant struct {
	op       policy.Op
	obj      policy.Object
	dst      policy.Object
	uid      uint32
	entry    *policy.Object
	replaced *policy.Object
}

// pwDescribeWant spells a list of expected requests for failure messages.
func pwDescribeWant(want []pwWant) string {
	parts := make([]string, len(want))
	for i, w := range want {
		s := pwOpName(w.op) + " on " + pwObjString(w.obj)
		switch w.op {
		case policy.OpRemoveEntry:
			s += " with Entry " + pwObjPtrString(w.entry)
		case policy.OpRename:
			s += " with Dst " + pwObjString(w.dst) + ", Entry " + pwObjPtrString(w.entry) + ", Replaced " +
				pwObjPtrString(w.replaced)
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
	pwCompareRequests(t, what, got, want, caller, false, why, where)
}

// pwWantW2Requests is pwWantRequests for W2's rows, which also checks Entry
// and Replaced on every request: nil where want gives none, and otherwise
// non-nil and pointing to an Object equal to the one want gives (ADR 0026
// section 7). So a request of any Op that sets a field want leaves nil, an
// OpAddEntry, OpLookup, OpReadDir, OpRead or OpWrite among them, fails.
func pwWantW2Requests(t *testing.T, what string, got []policy.Request, want []pwWant, caller vfs.Caller, why,
	where string) {
	t.Helper()
	pwCompareRequests(t, what, got, want, caller, true, why, where)
}

// pwCompareRequests is pwWantRequests and pwWantW2Requests; entries says
// whether Entry and Replaced are checked. Since ADR 0027 section 10's X12 it
// also requires every request's Advisory to be false: only ACCESS marks a
// request, and no call does (ADR 0027 section 2).
func pwCompareRequests(t *testing.T, what string, got []policy.Request, want []pwWant, caller vfs.Caller,
	entries bool, why, where string) {
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
		case entries && !pwSameObjPtr(g.Entry, w.entry):
			ok = false
		case entries && !pwSameObjPtr(g.Replaced, w.replaced):
			ok = false
		case g.Advisory:
			ok = false
		}
	}
	if ok {
		return
	}
	fields := ""
	if entries {
		fields = ", and Entry and Replaced as given, nil where none is given (ADR 0026 section 7)"
	}
	t.Errorf("%s asked the policy %s; want exactly %s, in that order, each with Caller uid %d gid %d%s, and "+
		"Advisory false, since no call marks its request (ADR 0027 section 2): %s (%s)", what, pwDescribe(got),
		pwDescribeWant(want), caller.UID, caller.GID, fields, why, where)
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

// pwTree is W2's tree, as ADR 0026 section 7 gives it for #148: the root; the
// directories d, of mode 0o750, and d2, of mode 0o1770, in it; in d the file
// f, with payload written at 0, the file e, of mode 0o664, never written, the
// symlink ln, to "f", and the empty directory dd, of mode 0o711; in d2 the
// files h, of mode 0o640, and v, of mode 0o600, never written; all committed
// by a Sync.
type pwTree struct {
	root, d, d2, f, fe, ln, dd, h, v vfs.Handle

	// The attributes GetAttr reported in setup.
	fAttr, eAttr, lnAttr, d2Attr, ddAttr, hAttr, vAttr vfs.Attr

	payload []byte
}

// pwW2Tree makes W2's tree as testCaller, under whatever policy fs has, and
// requires FS.dirty to be false after its Sync and every object to be the
// Object ADR 0026 section 7 gives it: R (pwDir), D (pwW2D), D2 (pwW2D2), F
// (pwReg), E (pwW2E), LN (pwLnk), DD (pwW2DD), H (pwW2H) and V (pwW2V).
func pwW2Tree(e *uc43Env, fs *FS, root vfs.Handle, where string) pwTree {
	e.t.Helper()
	s := pwTree{root: root, payload: pwBytes(100, 3)}
	tc := testCaller
	mode := func(m uint32) vfs.SetAttr { return vfs.SetAttr{Mode: sa24U32(m)} }
	mkdir := func(dir vfs.Handle, path, name string, m uint32) vfs.Handle {
		e.t.Helper()
		what := fmt.Sprintf("setup: testCaller's Mkdir(%q, {Mode %#o})", path, m)
		return pwMust(e, pwMkdir(e, fs, tc, dir, name, mode(m), what, where), what, where)
	}
	create := func(dir vfs.Handle, path, name string, m uint32) vfs.Handle {
		e.t.Helper()
		what := fmt.Sprintf("setup: testCaller's Create(%q, {Mode %#o}, exclusive)", path, m)
		return pwMust(e, pwCreate(e, fs, tc, dir, name, mode(m), true, what, where), what, where)
	}

	s.d = mkdir(root, "d", "d", 0o750)
	s.d2 = mkdir(root, "d2", "d2", 0o1770)
	s.f = e.create(fs, s.d, "f", where)
	e.write(fs, s.f, 0, s.payload, `setup: Write of 100 bytes at 0 of "d/f", vfs.Unstable`, where)
	s.fe = create(s.d, "d/e", "e", 0o664)
	s.ln = e.symlink(fs, s.d, "ln", "f", where)
	s.dd = mkdir(s.d, "d/dd", "dd", 0o711)
	s.h = create(s.d2, "d2/h", "h", 0o640)
	s.v = create(s.d2, "d2/v", "v", 0o600)
	if err := e.sync(fs, "setup: the Sync that commits W2's tree", where); err != nil {
		e.t.Fatalf("setup: the Sync that commits W2's tree = %v, want nil against a healthy store (%s)", err, where)
	}
	if f := sa24Read(e, fs, "setup: reading FS.dirty after the Sync", where); f.dirty {
		e.t.Fatalf("setup: FS.dirty is true after a Sync that returned nil with no other call running, want false "+
			"(ADR 0023 section 3; %s)", where)
	}
	pwSetupObject(e, fs, root, "the root", pwDir, -1, where)
	pwSetupObject(e, fs, s.d, "d", pwW2D, -1, where)
	s.d2Attr = pwSetupObject(e, fs, s.d2, "d2", pwW2D2, -1, where)
	s.fAttr = pwSetupObject(e, fs, s.f, "d/f", pwReg, 100, where)
	s.eAttr = pwSetupObject(e, fs, s.fe, "d/e", pwW2E, 0, where)
	s.lnAttr = pwSetupObject(e, fs, s.ln, "d/ln", pwLnk, -1, where)
	s.ddAttr = pwSetupObject(e, fs, s.dd, "d/dd", pwW2DD, -1, where)
	s.hAttr = pwSetupObject(e, fs, s.h, "d2/h", pwW2H, 0, where)
	s.vAttr = pwSetupObject(e, fs, s.v, "d2/v", pwW2V, 0, where)
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
// 11, for every method but SetAttr, whose W2 is ADR 0024 section 7's S4, with
// its tree and its rows for Remove, Rmdir and Rename as ADR 0026 section 7
// revises them for #148: each method asks f.perm for the requests listed, in
// order, each with Caller the caller, Object the inode as it stood, and Entry
// and Replaced naming what the call would take out (ADR 0026 section 2);
// returns the first refusal bare (ADR 0014 section 5) and asks nothing after
// it; and, refused, changes nothing, so FS.changes is what it was and
// FS.dirty is still false.
//
// sp24Mount installs an sp24Recorder; refusing nothing, testCaller makes W2's
// tree (pwW2Tree), whose nine Objects are R {TypeDir, 0o755}, D {TypeDir,
// 0o750}, D2 {TypeDir, 0o1770}, F {TypeReg, 0o644}, E {TypeReg, 0o664}, LN
// {TypeLnk, 0o777}, DD {TypeDir, 0o711}, H {TypeReg, 0o640} and V {TypeReg,
// 0o600}, each owned by 501:20, no two equal. Then, for each row: the
// recorder is reset to refuse the row's index; FS.changes is read, c;
// testCaller makes the call; it returns vfs.ErrNXIO, a status no row of
// Unix() returns, unless the row says otherwise; the recorder holds exactly
// the row's requests, Entry and Replaced included, nil where the row gives
// none; FS.changes is c and FS.dirty is false; and the row's check that
// nothing changed holds, made after reset(-1).
//
// The rows are ADR 0014 section 11's, but for Remove, Rmdir and Rename,
// whose rows are ADR 0026 section 7's. Of those, the recorder allows three: a
// rename onto itself and two calls that stage E answers vfs.ErrNoEnt, none of
// which changes the tree. They run in that order except that the two Reads
// the policy allows whole come last, after every refused call, so that a
// failure of either cannot make a later refused row's FS.dirty check fail
// with it. Every call is on an object of the type its operation needs (ADR
// 0014 section 11's safety rule): Rmdir is asked of dd and d2, directories,
// and the Renames that name a directory move one onto another.
func TestEveryMethodAsksThePolicyAndStopsAtItsRefusal(t *testing.T) {
	const where = "W2, ADR 0014 sections 5, 6 and 11; ADR 0026 sections 2 and 7"
	e := uc43Setup(t)
	rec := &sp24Recorder{refuseAt: -1}
	fs, root := sp24Mount(e, policy.Func(rec.check), where)
	s := pwW2Tree(e, fs, root, where)
	tc := testCaller
	R, D, D2, F, E, LN, DD, H, V := pwDir, pwW2D, pwW2D2, pwReg, pwW2E, pwLnk, pwW2DD, pwW2H, pwW2V
	ten := pwBytes(10, 151)

	const refused = "the call is refused at its first request, which section 6 says it makes before anything else"
	addEntry := func(name string) func(string) {
		return func(what string) {
			pwWantNoEnt(e, fs, s.d, "d/"+name, name, what, "a refused OpAddEntry makes no entry (ADR 0014 section 6)",
				where)
		}
	}

	// found requires testCaller's Lookup of name in dir to find id;
	// absent, to return vfs.ErrNoEnt; all runs several such checks.
	const kept = "a call that is refused, that returns an error, or that renames a name onto itself takes no name " +
		"away, moves nothing and replaces nothing (ADR 0026 sections 2 and 4; ADR 0024 section 1)"
	found := func(dir vfs.Handle, path, name string, id uint64) func(string) {
		return func(what string) { pwWantFound(e, fs, dir, path, name, id, what, kept, where) }
	}
	absent := func(dir vfs.Handle, path, name string) func(string) {
		return func(what string) {
			pwWantNoEnt(e, fs, dir, path, name, what, "a refused or failed Rename makes no name (ADR 0026 section 4)",
				where)
		}
	}
	all := func(checks ...func(string)) func(string) {
		return func(what string) {
			for _, c := range checks {
				c(what)
			}
		}
	}

	nx := vfs.ErrNXIO
	lookD := []pwWant{{op: policy.OpLookup, obj: D}}
	listD := []pwWant{{op: policy.OpReadDir, obj: D}}
	addD := []pwWant{{op: policy.OpAddEntry, obj: D}}
	remove := func(dir policy.Object, entry *policy.Object) []pwWant {
		return []pwWant{{op: policy.OpRemoveEntry, obj: dir, entry: entry}}
	}
	rename := func(src, dst policy.Object, entry, replaced *policy.Object) []pwWant {
		return []pwWant{{op: policy.OpRename, obj: src, dst: dst, entry: entry, replaced: replaced}}
	}
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
			nx, remove(D, &F),
			"Remove asks one OpRemoveEntry with Object the directory and Entry the object the name names (ADR " +
				"0026 section 2), " + refused,
			func(what string) {
				if r := pwGetAttr(e, fs, s.f, `GetAttr of "d/f" after `+what, where); r.err != nil {
					t.Errorf("after %s, GetAttr of \"d/f\"'s handle = %s, want nil: a refused Remove takes no name away "+
						"(ADR 0014 section 6; %s)", what, pwStatus(r.err), where)
				}
				found(s.d, "d/f", "f", s.fAttr.FileID)(what)
			},
		},
		{
			`testCaller's Remove(d, "ln")`, 0,
			func(what string) error { return pwRemove(e, fs, tc, s.d, "ln", what, where) },
			nx, remove(D, &LN),
			"Remove of a symlink asks one OpRemoveEntry with Entry the symlink (ADR 0026 section 2), " + refused,
			found(s.d, "d/ln", "ln", s.lnAttr.FileID),
		},
		{
			`testCaller's Rmdir(root, "d2")`, 0,
			func(what string) error { return pwRmdir(e, fs, tc, s.root, "d2", what, where) },
			nx, remove(R, &D2),
			"Rmdir asks one OpRemoveEntry with Object the parent, the root, and Entry the directory it removes, " +
				"not the other way round (ADR 0026 sections 2 and 7), " + refused,
			found(s.root, "d2", "d2", s.d2Attr.FileID),
		},
		{
			`testCaller's Rmdir(d, "dd")`, 0,
			func(what string) error { return pwRmdir(e, fs, tc, s.d, "dd", what, where) },
			nx, remove(D, &DD),
			"Rmdir asks one OpRemoveEntry with Object the parent and Entry the directory it removes (ADR 0026 " +
				"sections 2 and 7), " + refused,
			found(s.d, "d/dd", "dd", s.ddAttr.FileID),
		},
		{
			`testCaller's Remove(d, "nope")`, 0,
			func(what string) error { return pwRemove(e, fs, tc, s.d, "nope", what, where) },
			nx, remove(D, nil),
			"a name that names no object describes no entry, so Remove asks one OpRemoveEntry with no Entry (ADR " +
				"0026 section 2), " + refused,
			nil,
		},
		{
			`testCaller's Remove(d, "nope")`, -1,
			func(what string) error { return pwRemove(e, fs, tc, s.d, "nope", what, where) },
			vfs.ErrNoEnt, remove(D, nil),
			"a name that names no object describes no entry, so Remove asks one OpRemoveEntry with no Entry, and " +
				"then stage E answers vfs.ErrNoEnt (ADR 0026 section 2)",
			nil,
		},
		{
			`testCaller's Rename(d, "f", d2, "g")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.d, "f", s.d2, "g", what, where) },
			nx, rename(D, D2, &F, nil),
			"Rename asks one OpRename, with Object the source directory, Dst the target directory, Entry the " +
				"object it moves, and no Replaced, since g names nothing (ADR 0026 section 2), " + refused,
			all(found(s.d, "d/f", "f", s.fAttr.FileID), absent(s.d2, "d2/g", "g")),
		},
		{
			`testCaller's Rename(d2, "h", d, "i")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.d2, "h", s.d, "i", what, where) },
			nx, rename(D2, D, &H, nil),
			"a cross-directory Rename in the other direction asks one OpRename with Object the source and Dst " +
				"the target (ADR 0026 sections 2 and 7), " + refused,
			all(found(s.d2, "d2/h", "h", s.hAttr.FileID), absent(s.d, "d/i", "i")),
		},
		{
			`testCaller's Rename(d, "f", d2, "v")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.d, "f", s.d2, "v", what, where) },
			nx, rename(D, D2, &F, &V),
			"a Rename onto a name that names an object asks one OpRename with Entry the object moved and " +
				"Replaced the object replaced (ADR 0026 section 2), " + refused,
			all(found(s.d2, "d2/v", "v", s.vAttr.FileID), found(s.d, "d/f", "f", s.fAttr.FileID)),
		},
		{
			`testCaller's Rename(d2, "v", d, "e")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.d2, "v", s.d, "e", what, where) },
			nx, rename(D2, D, &V, &E),
			"a Rename onto a name that names an object, in the other direction, asks one OpRename with Entry " +
				"the object moved and Replaced the object replaced (ADR 0026 section 2), " + refused,
			all(found(s.d2, "d2/v", "v", s.vAttr.FileID), found(s.d, "d/e", "e", s.eAttr.FileID)),
		},
		{
			`testCaller's Rename(d, "f", d, "g")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.d, "f", s.d, "g", what, where) },
			nx, rename(D, D, &F, nil),
			"Rename within one directory asks one OpRename with that directory as both Object and Dst (ADR 0026 " +
				"section 2), " + refused,
			all(found(s.d, "d/f", "f", s.fAttr.FileID), absent(s.d, "d/g", "g")),
		},
		{
			`testCaller's Rename(root, "d2", d, "dd")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.root, "d2", s.d, "dd", what, where) },
			nx, rename(R, D, &D2, &DD),
			"a directory moved onto an empty directory asks one OpRename with Entry the directory moved and " +
				"Replaced the directory replaced (ADR 0026 sections 2 and 7), " + refused,
			all(found(s.root, "d2", "d2", s.d2Attr.FileID), found(s.d, "d/dd", "dd", s.ddAttr.FileID)),
		},
		{
			`testCaller's Rename(d, "f", d, "f")`, 0,
			func(what string) error { return pwRename(e, fs, tc, s.d, "f", s.d, "f", what, where) },
			nx, rename(D, D, nil, nil),
			"a rename of a name onto itself takes no name out of any directory, so its request describes no " +
				"entry (ADR 0026 section 2), " + refused,
			found(s.d, "d/f", "f", s.fAttr.FileID),
		},
		{
			`testCaller's Rename(d, "f", d, "f")`, -1,
			func(what string) error { return pwRename(e, fs, tc, s.d, "f", s.d, "f", what, where) },
			nil, rename(D, D, nil, nil),
			"a rename of a name onto itself asks one OpRename that describes no entry, and returns nil having " +
				"changed nothing (ADR 0026 section 2)",
			found(s.d, "d/f", "f", s.fAttr.FileID),
		},
		{
			`testCaller's Rename(d, "nope", d2, "v")`, -1,
			func(what string) error { return pwRename(e, fs, tc, s.d, "nope", s.d2, "v", what, where) },
			vfs.ErrNoEnt, rename(D, D2, nil, nil),
			"a source that names no object describes no entry, and no replaced entry either, since the call " +
				"will replace nothing; stage E then answers vfs.ErrNoEnt (ADR 0026 section 2)",
			found(s.d2, "d2/v", "v", s.vAttr.FileID),
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
			switch {
			case r.wantErr != nil && r.refuse < 0:
				why = "the policy allowed every request, and the call's own check of the namespace, at stage E, " +
					"returns this bare vfs.Status (ADR 0026 section 2; ADR 0014 section 6)"
			case r.wantErr != nil:
				why = "the policy's refusal is returned as the bare vfs.Status, unwrapped, and no check of the FS's " +
					"own returns anything else first (ADR 0014 sections 5 and 6)"
			}
			t.Errorf("%s = %s, want %s, compared with ==: %s (%s)", what, pwStatus(err), pwStatus(r.wantErr), why,
				where)
		}
		pwWantW2Requests(t, what, rec.taken(), r.want, tc, r.why, where)
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
// W2's mount, recorder and d (pwW2Tree), whose Object D is, since ADR 0026
// section 7, {TypeDir, 0o750, 501, 20} (pwW2D). sp24Root's Create of r1 with
// {UID 7}, refusing index 1, returns nil, owned by 0:0, and asks [OpAddEntry on D,
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
	D := pwW2D
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
// lacking its own bits: ro is a directory, whose owner is held to its mode
// (ADR 0025 section 1). Since ADR 0026 section 7 the setup also makes wide, a
// directory 0o777, in ro before ro is set to 0o555; and open, a directory
// 0o777, holding shut, a directory 0o700. Each row is a call, its caller, and
// the status it must return, compared with == against the bare vfs.Status.
// Then the requested uid under Unix(), and Access with every bit asked for.
//
// ADR 0026 section 7's two rows, for #148, are sa24Other's Rmdir(ro, "wide"),
// vfs.ErrAcces, and its Rmdir(open, "shut"), nil: the parent's mode decides,
// where the entry's own would answer the other way. Both pass before and after
// ADR 0026's change.
//
// Rows that would pin U1 to U6 are left out: no owner lacking its own bits, no
// read through execute alone, no Read or Write of a symlink, no sticky bit, no
// directory with read but not search, or write but not search, asked of
// Access, and no times set by a non-owner. The first two, U1 and U2, are
// pinned for a regular file since ADR 0025 section 6 and tested in
// owner_access_test.go; W4 still leaves them out. The sticky bit, U4, is
// pinned since ADR 0026 section 8 and tested in sticky_test.go; W4 still has
// no sticky row. Access for a directory with read but not search, or write but
// not search, U5, is pinned since ADR 0027 section 10 and tested in
// access_test.go; W4 still has no such row, and its ten Access rows stand.
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
	mkdir(ro, "wide", 0o777) // ADR 0026 section 7: made in ro before ro is set to 0o555
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
	open := mkdir(root, "open", 0o777) // ADR 0026 section 7
	mkdir(open, "shut", 0o700)

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
		// ADR 0026 section 7, #148: an entry whose own verdict differs from its
		// parent's.
		{o, `Rmdir(ro, "wide")`, rmdir(ro, "wide"), vfs.ErrAcces,
			"the parent's mode decides: 0o555 gives no write, where wide's own 0o777 would allow (ADR 0026 section 7)"},
		{o, `Rmdir(open, "shut")`, rmdir(open, "shut"), nil,
			"the parent's mode decides: 0o777 gives write and search, where shut's own 0o700 would refuse (ADR 0026 " +
				"section 7)"},

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

// TestGetAttrReadLinkFSStatAndCommitConsultNoPolicy is W5 of ADR 0014
// section 11, as ADR 0027 section 10's X12 revises it: GetAttr, ReadLink,
// FSStat and Commit ask f.perm nothing. Access is no longer among them: it
// asks f.perm the operations' own requests, which access_test.go's X10 and
// access_wiring_test.go's X11 show (ADR 0027 section 7).
//
// W2's mount and recorder, with W2's tree (pwW2Tree); the recorder reset to
// refuse index 0 before each call: GetAttr(f) returns f's attributes with
// nil; ReadLink(ln) returns "f" with nil; FSStat(root) returns nil;
// Commit(f, 0, 0) returns nil; and after each the recorder has recorded
// nothing.
func TestGetAttrReadLinkFSStatAndCommitConsultNoPolicy(t *testing.T) {
	const where = "W5, ADR 0014 sections 6 and 11; ADR 0027 section 10, X12"
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
}
