// Package policy decides what a caller may do to a filesystem object, as one
// table of rules that can be tested exhaustively without a filesystem.
//
// Every decision here is pure: it takes no lock, does no I/O, logs nothing,
// modifies nothing it is given and allocates nothing when it allows, so a
// filesystem may evaluate a policy inside whatever lock it holds. A refusal is
// a bare vfs.Status, which the NFS layer passes to the client unchanged.
//
// Spec: docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md §4, §5;
// docs/adr/0025-a-files-owner-may-read-and-write-it-whatever-its-mode.md §1, §2, §3;
// docs/adr/0026-in-a-sticky-directory-only-an-owner-removes-or-renames-an-entry.md §1, §2;
// docs/adr/0027-access-grants-only-what-the-operations-allow.md §1, §2, §3
package policy

import "strata/internal/vfs"

// Perm is a set of the three Unix permission bits.
type Perm uint32

const (
	PermExec  Perm = 1
	PermWrite Perm = 2
	PermRead  Perm = 4
)

// permAll is every bit a Perm may have; Permits refuses a want with any other.
const permAll = PermRead | PermWrite | PermExec

// Object is the part of an inode a permission check reads.
type Object struct {
	Type     vfs.FileType
	Mode     uint32
	UID, GID uint32
}

// InGroup reports whether gid is c's primary group or one of its
// supplementary groups.
func InGroup(c vfs.Caller, gid uint32) bool {
	if c.GID == gid {
		return true
	}
	for _, g := range c.GIDs {
		if g == gid {
			return true
		}
	}
	return false
}

// Permits applies the classic Unix mode check. Only the low nine bits of the
// mode count. Root may read and write anything, but execute still needs some
// execute bit on a non-directory, as on Unix. Anyone else is judged by exactly
// one class: the owner's bits, else the group's, else the other bits.
//
// A want with a bit outside permAll fails closed for every caller, root
// included. No caller passes one; refusing it keeps a mistaken mask from
// reading as root's blanket permission.
func Permits(c vfs.Caller, o Object, want Perm) bool {
	if want&^permAll != 0 {
		return false
	}
	mode := o.Mode & 0o777
	if c.UID == 0 {
		return want&PermExec == 0 || o.Type == vfs.TypeDir || mode&0o111 != 0
	}
	var bits uint32
	switch {
	case c.UID == o.UID:
		bits = (mode >> 6) & 7
	case InGroup(c, o.GID):
		bits = (mode >> 3) & 7
	default:
		bits = mode & 7
	}
	return Perm(bits)&want == want
}

// Op is the operation a Request asks about.
type Op uint8

const (
	OpRead Op = iota + 1
	OpWrite
	OpTruncate
	OpChmod
	OpChown
	OpChgrp
	OpSetTimes
	OpLookup
	OpReadDir
	OpAddEntry
	OpRemoveEntry
	OpRename

	// OpExecute asks whether a caller may execute an object. No operation
	// makes it, since a client executes a file by reading it; only ACCESS
	// asks it, for its EXECUTE bit (ADR 0027 §1, §2).
	OpExecute
)

// Request is one question to a Policy. A field added later must mean, when
// left zero, what the request meant before the field existed.
type Request struct {
	Op     Op
	Caller vfs.Caller

	// Object is what the call acts on: for OpLookup, OpReadDir, OpAddEntry and
	// OpRemoveEntry the directory, and for OpRename the source directory.
	Object Object

	// Dst is OpRename's target directory.
	Dst Object

	// UID is the owner OpChown asks for.
	UID uint32

	// GID is the group OpChgrp asks for.
	GID uint32

	// Entry is the object whose name the call takes out of Object: the one
	// OpRemoveEntry removes, and the one OpRename moves. Nil when the call
	// takes none out.
	Entry *Object

	// Replaced is the object whose name OpRename takes out of Dst, to put the
	// object it moves in its place. Nil when it replaces none.
	Replaced *Object

	// Advisory marks a request made for ACCESS: it asks what a client should
	// let the caller open or attempt, ahead of any call, and nothing is done
	// on its answer. A file row answers it by the mode alone, with neither the
	// owner override nor the read that execute permission gives, which stand
	// in for a descriptor a client already holds open. Every other row
	// answers it as it answers the same request without the mark. It is false
	// on every request a call makes (ADR 0027 §2, §3).
	Advisory bool
}

// Policy decides a Request: nil allows it, and any other error refuses it.
type Policy interface {
	Check(r Request) error
}

// Func adapts a function to a Policy.
type Func func(r Request) error

// Check calls f once and returns its result.
func (f Func) Check(r Request) error {
	return f(r)
}

// allOf is the conjunction All returns.
type allOf []Policy

// All returns a Policy that asks each of ps in order and returns the first
// refusal unchanged, asking none after it. With no policies it allows
// everything, as a conjunction of no checks does. It keeps its own copy of ps.
//
// It panics if any of ps is nil or a nil Func: caught here, a nil policy
// cannot surface as a panic inside a lock at request time.
func All(ps ...Policy) Policy {
	own := make(allOf, len(ps))
	for i, p := range ps {
		if p == nil {
			panic("policy: All given a nil Policy")
		}
		if f, ok := p.(Func); ok && f == nil {
			panic("policy: All given a nil Func")
		}
		own[i] = p
	}
	return own
}

func (a allOf) Check(r Request) error {
	for _, p := range a {
		if err := p.Check(r); err != nil {
			return err
		}
	}
	return nil
}

// unixRules is the Policy Unix returns. It holds no state, so every value of
// it decides alike.
type unixRules struct{}

// Unix returns the classic Unix rules. It is a function rather than an
// exported variable so that there is no package-level authority an importer
// could reassign for every caller at once.
func Unix() Policy {
	return unixRules{}
}

// Check applies the row for r.Op, in which the first check that fails gives
// the refusal. The type checks come first, and blobfs relies on them: past
// them it treats Object, and for OpRename Dst, as the type the operation
// needs. An Op with no row fails closed with vfs.ErrServerFault, for every
// caller, since it is a fault in the code that built the request rather than
// a judgement of the caller.
//
// The three file rows, OpRead, OpWrite and OpTruncate, are ADR 0025 §1's, not
// ADR 0014 §5's: they allow a regular file's owner, and OpRead a caller whom
// the mode gives execute, past what Permits gives. ADR 0027 §2 keeps both
// departures from a request marked Advisory, which these rows alone read, so
// ACCESS gets the mode's verdict from them.
//
// The OpExecute row is ADR 0027 §2's: vfs.ErrIsDir for a directory, else
// Permits with PermExec. Only ACCESS asks it.
//
// The OpRemoveEntry and OpRename rows are ADR 0026 §1's: past ADR 0014 §5's
// type and mode checks, which they keep as their first lines, they refuse with
// vfs.ErrPerm a caller who would take an entry's name out of a sticky
// directory and is neither root nor an owner. A request that sets neither
// Entry nor Replaced is answered as before those fields existed. OpAddEntry's
// row is still ADR 0014 §5's.
func (unixRules) Check(r Request) error {
	c, o := r.Caller, r.Object
	switch r.Op {
	case OpRead:
		return fileAccess(c, o, PermRead, r.Advisory)
	case OpWrite, OpTruncate:
		return fileAccess(c, o, PermWrite, r.Advisory)
	case OpChmod:
		if !rootOrOwner(c, o) {
			return vfs.ErrPerm
		}
		return nil
	case OpChown:
		if !rootOrOwner(c, o) {
			return vfs.ErrPerm
		}
		// Only root may give a file away.
		if c.UID != 0 && r.UID != o.UID {
			return vfs.ErrPerm
		}
		return nil
	case OpChgrp:
		if !rootOrOwner(c, o) {
			return vfs.ErrPerm
		}
		if c.UID != 0 && !InGroup(c, r.GID) {
			return vfs.ErrPerm
		}
		return nil
	case OpSetTimes:
		if !rootOrOwner(c, o) && !Permits(c, o, PermWrite) {
			return vfs.ErrPerm
		}
		return nil
	case OpLookup:
		return dirAccess(c, o, PermExec)
	case OpReadDir:
		return dirAccess(c, o, PermRead)
	case OpAddEntry:
		return dirAccess(c, o, PermWrite|PermExec)
	case OpRemoveEntry:
		if err := dirAccess(c, o, PermWrite|PermExec); err != nil {
			return err
		}
		return stickyTakeOut(c, o, r.Entry)
	case OpRename:
		if o.Type != vfs.TypeDir || r.Dst.Type != vfs.TypeDir {
			return vfs.ErrNotDir
		}
		if !Permits(c, o, PermWrite|PermExec) || !Permits(c, r.Dst, PermWrite|PermExec) {
			return vfs.ErrAcces
		}
		// The source's bit guards the entry moved, then the target's the
		// entry replaced, so a rename across directories is judged in each
		// for what it takes out of that one.
		if err := stickyTakeOut(c, o, r.Entry); err != nil {
			return err
		}
		return stickyTakeOut(c, r.Dst, r.Replaced)
	case OpExecute:
		if o.Type == vfs.TypeDir {
			return vfs.ErrIsDir
		}
		if !Permits(c, o, PermExec) {
			return vfs.ErrAcces
		}
		return nil
	}
	return vfs.ErrServerFault
}

// fileAccess is the row of an operation on a file's contents: want is PermRead
// for OpRead, and PermWrite for OpWrite and OpTruncate. The first line that
// holds decides (ADR 0025 §1):
//
//  1. vfs.ErrIsDir, if o is a directory, before any permission is looked at;
//  2. nil, if Permits(c, o, want);
//  3. nil, if the request is not advisory, o is a regular file and c is its
//     owner, c.UID == o.UID. A stateless server cannot see that the owner
//     already holds the file open, so it lets the owner past the mode (RFC
//     1813 §4.4);
//  4. for a read alone, nil, if the request is not advisory, o is a regular
//     file and Permits(c, o, PermExec), since a server cannot tell a page-in
//     from a read (§2);
//  5. vfs.ErrAcces otherwise.
//
// Lines 3 and 4 live here rather than in Permits so that every other row
// still answers from the mode alone. An advisory request skips them, since
// ACCESS is a client's check at open, which neither departure speaks for, so
// AccessBits gets the mode's verdict from this row (ADR 0027 §2, §3).
func fileAccess(c vfs.Caller, o Object, want Perm, advisory bool) error {
	if o.Type == vfs.TypeDir {
		return vfs.ErrIsDir
	}
	if Permits(c, o, want) {
		return nil
	}
	if !advisory && o.Type == vfs.TypeReg {
		if c.UID == o.UID {
			return nil
		}
		if want == PermRead && Permits(c, o, PermExec) {
			return nil
		}
	}
	return vfs.ErrAcces
}

// dirAccess is the row of an operation on a directory's entries.
func dirAccess(c vfs.Caller, o Object, want Perm) error {
	if o.Type != vfs.TypeDir {
		return vfs.ErrNotDir
	}
	if !Permits(c, o, want) {
		return vfs.ErrAcces
	}
	return nil
}

// stickyTakeOut is lines 3 and 4 of ADR 0026 §1's rows: in a directory whose
// mode has 0o1000 set, only root, the directory's owner or the owner of the
// object the entry names may take the entry's name out of it. A nil entry
// takes nothing out. Of the entry it reads the UID alone, since the rule is
// about owners: not its type, its mode or its group, and no group of c makes
// c an owner. It neither writes through entry nor keeps it.
func stickyTakeOut(c vfs.Caller, dir Object, entry *Object) error {
	if entry == nil || dir.Mode&0o1000 == 0 {
		return nil
	}
	if rootOrOwner(c, dir) || c.UID == entry.UID {
		return nil
	}
	return vfs.ErrPerm
}

// rootOrOwner reports whether c is root or owns o.
func rootOrOwner(c vfs.Caller, o Object) bool {
	return c.UID == 0 || c.UID == o.UID
}

// accessRow is one bit of an ACCESS table: the requests the bit names, asked
// in order, and whether the bit names a change. A zero Op ends the list, so a
// row whose first Op is zero has no requests and is never granted.
type accessRow struct {
	bit    uint32
	change bool
	ops    [2]Op
}

// accessDir is ADR 0027 §1's table for a directory, and accessOther its table
// for every other type, valid or not. Each is in ascending order of its bits,
// the order AccessBits asks in, and is a fixed array so that walking it
// allocates nothing. Nothing writes either.
var accessDir = [...]accessRow{
	{bit: vfs.AccessRead, ops: [2]Op{OpReadDir}},
	{bit: vfs.AccessLookup, ops: [2]Op{OpLookup}},
	{bit: vfs.AccessModify, change: true, ops: [2]Op{OpRename}},
	{bit: vfs.AccessExtend, change: true, ops: [2]Op{OpAddEntry}},
	{bit: vfs.AccessDelete, change: true, ops: [2]Op{OpRemoveEntry}},
	{bit: vfs.AccessExecute},
}

var accessOther = [...]accessRow{
	{bit: vfs.AccessRead, ops: [2]Op{OpRead}},
	{bit: vfs.AccessLookup},
	{bit: vfs.AccessModify, change: true, ops: [2]Op{OpWrite, OpTruncate}},
	{bit: vfs.AccessExtend, change: true, ops: [2]Op{OpWrite}},
	{bit: vfs.AccessDelete},
	{bit: vfs.AccessExecute, ops: [2]Op{OpExecute}},
}

// AccessBits is the ACCESS answer p gives c for o, as the bits of want among
// the six vfs.Access bits. It grants a bit only if p allows every request the
// operations the bit names would make, so ACCESS has no predicate of its own,
// and only if the bit names no change or mutable is true. mutable is what the
// caller knows of the mount, and its zero value grants no change (ADR 0027
// §1, §2, §4).
//
// Every request it asks is marked Advisory, so the file rows answer by the
// mode alone, with neither the owner override nor the read that execute
// permission gives, which ACCESS must not report (ADR 0027 §3; RFC 1813 §4.4).
// A bit that has no requests, is not wanted, or names a change on a mount that
// takes none is not asked about at all, and a bit stops at its first refusal,
// so one call asks p at most five requests. It returns no error: a refusal
// withholds a bit and fails nothing.
func AccessBits(p Policy, c vfs.Caller, o Object, want uint32, mutable bool) uint32 {
	table := &accessOther
	if o.Type == vfs.TypeDir {
		table = &accessDir
	}
	var granted uint32
	for i := range table {
		row := &table[i]
		if want&row.bit == 0 || row.ops[0] == 0 || (row.change && !mutable) {
			continue
		}
		allowed := true
		for _, op := range row.ops {
			if op == 0 {
				break
			}
			r := Request{Op: op, Caller: c, Object: o, Advisory: true}
			if op == OpRename {
				// The one rename a directory's MODIFY names is within it.
				r.Dst = o
			}
			if p.Check(r) != nil {
				allowed = false
				break
			}
		}
		if allowed {
			granted |= row.bit
		}
	}
	return granted
}
