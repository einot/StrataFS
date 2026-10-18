// Package policy decides what a caller may do to a filesystem object, as one
// table of rules that can be tested exhaustively without a filesystem.
//
// Every decision here is pure: it takes no lock, does no I/O, logs nothing,
// modifies nothing it is given and allocates nothing when it allows, so a
// filesystem may evaluate a policy inside whatever lock it holds. A refusal is
// a bare vfs.Status, which the NFS layer passes to the client unchanged.
//
// Spec: docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md §4, §5
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
func (unixRules) Check(r Request) error {
	c, o := r.Caller, r.Object
	switch r.Op {
	case OpRead:
		return fileAccess(c, o, PermRead)
	case OpWrite, OpTruncate:
		return fileAccess(c, o, PermWrite)
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
	case OpAddEntry, OpRemoveEntry:
		return dirAccess(c, o, PermWrite|PermExec)
	case OpRename:
		if o.Type != vfs.TypeDir || r.Dst.Type != vfs.TypeDir {
			return vfs.ErrNotDir
		}
		if !Permits(c, o, PermWrite|PermExec) || !Permits(c, r.Dst, PermWrite|PermExec) {
			return vfs.ErrAcces
		}
		return nil
	}
	return vfs.ErrServerFault
}

// fileAccess is the row of an operation on a file's contents.
func fileAccess(c vfs.Caller, o Object, want Perm) error {
	if o.Type == vfs.TypeDir {
		return vfs.ErrIsDir
	}
	if !Permits(c, o, want) {
		return vfs.ErrAcces
	}
	return nil
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

// rootOrOwner reports whether c is root or owns o.
func rootOrOwner(c vfs.Caller, o Object) bool {
	return c.UID == 0 || c.UID == o.UID
}

// AccessBits is the ACCESS answer the mode bits give c for o, as the six
// vfs.Access bits. It carries no mount mask: masking a read-only mount's
// write bits is the mount's business, and so is the caller's want.
func AccessBits(c vfs.Caller, o Object) uint32 {
	dir := o.Type == vfs.TypeDir
	read := Permits(c, o, PermRead)
	exec := Permits(c, o, PermExec)
	var bits uint32
	if read {
		bits |= vfs.AccessRead
	}
	if dir && (read || exec) {
		bits |= vfs.AccessLookup
	}
	if Permits(c, o, PermWrite) {
		bits |= vfs.AccessModify | vfs.AccessExtend
		if dir {
			bits |= vfs.AccessDelete
		}
	}
	if !dir && o.Mode&0o111 != 0 && exec {
		bits |= vfs.AccessExecute
	}
	return bits
}
