// Package nfs implements the NFS version 3 and MOUNT version 3 programs
// (RFC 1813) on top of a vfs.FS.
package nfs

import (
	"errors"
	"time"

	"strata/internal/vfs"
	"strata/internal/xdr"
)

// NFSv3 program and version numbers.
const (
	ProgramNFS   = 100003
	VersionNFS   = 3
	ProgramMount = 100005
	VersionMount = 3
)

// NFSv3 procedure numbers.
const (
	procNull = iota
	procGetAttr
	procSetAttr
	procLookup
	procAccess
	procReadlink
	procRead
	procWrite
	procCreate
	procMkdir
	procSymlink
	procMknod
	procRemove
	procRmdir
	procRename
	procLink
	procReaddir
	procReaddirPlus
	procFSStat
	procFSInfo
	procPathconf
	procCommit
	procCount
)

// maxHandleLen is the NFSv3 limit on file handle size.
const maxHandleLen = 64

// statusOf maps a filesystem error to an NFSv3 status code.
func statusOf(err error) vfs.Status {
	if err == nil {
		return vfs.OK
	}
	var s vfs.Status
	if errors.As(err, &s) {
		return s
	}
	// Anything not already a protocol status is an unexpected failure of the
	// backing store; the client should treat it as an I/O error rather than
	// retry forever.
	return vfs.ErrIO
}

// ---- primitive encoders ----

// putTime writes an nfstime3.
func putTime(w *xdr.Writer, t time.Time) {
	if t.IsZero() {
		w.Uint32(0)
		w.Uint32(0)
		return
	}
	w.Uint32(uint32(t.Unix()))
	w.Uint32(uint32(t.Nanosecond()))
}

// putFattr writes a fattr3.
func putFattr(w *xdr.Writer, a vfs.Attr) {
	w.Uint32(uint32(a.Type))
	w.Uint32(a.Mode)
	w.Uint32(a.NLink)
	w.Uint32(a.UID)
	w.Uint32(a.GID)
	w.Uint64(a.Size)
	w.Uint64(a.Used)
	w.Uint32(a.RDev[0])
	w.Uint32(a.RDev[1])
	w.Uint64(a.FSID)
	w.Uint64(a.FileID)
	putTime(w, a.ATime)
	putTime(w, a.MTime)
	putTime(w, a.CTime)
}

// putPostOpAttr writes a post_op_attr: attributes if we have them, otherwise
// an explicit "not present", which clients handle by asking again.
func putPostOpAttr(w *xdr.Writer, a *vfs.Attr) {
	if a == nil {
		w.Bool(false)
		return
	}
	w.Bool(true)
	putFattr(w, *a)
}

// wccAttr is the subset of attributes NFS uses for weak cache consistency.
type wccAttr struct {
	size         uint64
	mtime, ctime time.Time
	valid        bool
}

func wccFrom(a *vfs.Attr) wccAttr {
	if a == nil {
		return wccAttr{}
	}
	return wccAttr{size: a.Size, mtime: a.MTime, ctime: a.CTime, valid: true}
}

// putWccData writes a wcc_data: the attributes before and after an operation.
// Clients use the pair to decide whether their cached copy of a directory or
// file is still valid without re-reading it.
func putWccData(w *xdr.Writer, before wccAttr, after *vfs.Attr) {
	if before.valid {
		w.Bool(true)
		w.Uint64(before.size)
		putTime(w, before.mtime)
		putTime(w, before.ctime)
	} else {
		w.Bool(false)
	}
	putPostOpAttr(w, after)
}

// putHandle writes an nfs_fh3.
func putHandle(w *xdr.Writer, h vfs.Handle) {
	w.Opaque(h)
}

// putPostOpHandle writes a post_op_fh3.
func putPostOpHandle(w *xdr.Writer, h vfs.Handle) {
	if h == nil {
		w.Bool(false)
		return
	}
	w.Bool(true)
	putHandle(w, h)
}

// ---- primitive decoders ----

func getHandle(r *xdr.Reader) (vfs.Handle, error) {
	h := r.Opaque()
	if r.Err() != nil {
		return nil, r.Err()
	}
	if len(h) > maxHandleLen {
		return nil, errors.New("nfs: file handle too long")
	}
	return vfs.Handle(h), nil
}

// dirOpArgs is the (directory, name) pair many procedures start with.
type dirOpArgs struct {
	dir  vfs.Handle
	name string
}

func getDirOpArgs(r *xdr.Reader) (dirOpArgs, error) {
	h, err := getHandle(r)
	if err != nil {
		return dirOpArgs{}, err
	}
	name := r.String()
	if r.Err() != nil {
		return dirOpArgs{}, r.Err()
	}
	return dirOpArgs{dir: h, name: name}, nil
}

// time_how values for sattr3.
const (
	dontChange      = 0
	setToServerTime = 1
	setToClientTime = 2
)

// getSattr decodes a sattr3, in which every field is individually optional.
func getSattr(r *xdr.Reader) (vfs.SetAttr, error) {
	var sa vfs.SetAttr

	if r.Bool() {
		v := r.Uint32()
		sa.Mode = &v
	}
	if r.Bool() {
		v := r.Uint32()
		sa.UID = &v
	}
	if r.Bool() {
		v := r.Uint32()
		sa.GID = &v
	}
	if r.Bool() {
		v := r.Uint64()
		sa.Size = &v
	}

	switch r.Uint32() {
	case setToServerTime:
		now := time.Now()
		sa.ATime = &now
	case setToClientTime:
		t := getTime(r)
		sa.ATime = &t
	}
	switch r.Uint32() {
	case setToServerTime:
		now := time.Now()
		sa.MTime = &now
	case setToClientTime:
		t := getTime(r)
		sa.MTime = &t
	}

	if r.Err() != nil {
		return vfs.SetAttr{}, r.Err()
	}
	return sa, nil
}

func getTime(r *xdr.Reader) time.Time {
	sec := r.Uint32()
	nsec := r.Uint32()
	return time.Unix(int64(sec), int64(nsec))
}
