// Package vfs defines the filesystem contract the NFS server talks to.
//
// It is deliberately narrow and NFS-shaped: every method maps to one or two
// NFSv3 procedures, handles are opaque, and errors are NFS status codes. That
// keeps the protocol layer free of storage concerns and lets a backend be
// swapped without touching the wire code.
package vfs

import (
	"context"
	"fmt"
	"time"
)

// Status is an NFSv3 error code (RFC 1813 nfsstat3).
type Status uint32

const (
	OK             Status = 0
	ErrPerm        Status = 1
	ErrNoEnt       Status = 2
	ErrIO          Status = 5
	ErrNXIO        Status = 6
	ErrAcces       Status = 13
	ErrExist       Status = 17
	ErrXDev        Status = 18
	ErrNoDev       Status = 19
	ErrNotDir      Status = 20
	ErrIsDir       Status = 21
	ErrInval       Status = 22
	ErrFBig        Status = 27
	ErrNoSpc       Status = 28
	ErrROFS        Status = 30
	ErrMLink       Status = 31
	ErrNameTooLong Status = 63
	ErrNotEmpty    Status = 66
	ErrDQuot       Status = 69
	ErrStale       Status = 70
	ErrRemote      Status = 71
	ErrBadHandle   Status = 10001
	ErrNotSync     Status = 10002
	ErrBadCookie   Status = 10003
	ErrNotSupp     Status = 10004
	ErrTooSmall    Status = 10005
	ErrServerFault Status = 10006
	ErrBadType     Status = 10007
	ErrJukebox     Status = 10008
)

func (s Status) Error() string { return fmt.Sprintf("nfs status %d", uint32(s)) }

// FileType is the NFSv3 ftype3 enumeration.
type FileType uint32

const (
	TypeReg  FileType = 1
	TypeDir  FileType = 2
	TypeBlk  FileType = 3
	TypeChr  FileType = 4
	TypeLnk  FileType = 5
	TypeSock FileType = 6
	TypeFifo FileType = 7
)

// Attr is a file's metadata, mirroring NFSv3 fattr3.
type Attr struct {
	Type   FileType
	Mode   uint32 // permission bits only, no type bits
	NLink  uint32
	UID    uint32
	GID    uint32
	Size   uint64
	Used   uint64 // bytes actually consumed by storage
	RDev   [2]uint32
	FSID   uint64
	FileID uint64
	ATime  time.Time
	MTime  time.Time
	CTime  time.Time
}

// SetAttr carries an NFSv3 sattr3: every field is optional.
type SetAttr struct {
	Mode  *uint32
	UID   *uint32
	GID   *uint32
	Size  *uint64
	ATime *time.Time
	MTime *time.Time
}

// DirEntry is one entry returned by ReadDir.
type DirEntry struct {
	FileID uint64
	Name   string
	Cookie uint64
	// Handle and Attr are filled for READDIRPLUS; Handle is nil when the
	// backend cannot cheaply produce one, which clients tolerate.
	Handle Handle
	Attr   *Attr
}

// Handle is an opaque NFS file handle. NFSv3 caps these at 64 bytes.
type Handle []byte

// Stat is the filesystem-wide information behind FSSTAT.
type Stat struct {
	TotalBytes uint64
	FreeBytes  uint64
	AvailBytes uint64
	TotalFiles uint64
	FreeFiles  uint64
}

// Stability is the NFSv3 stable_how a client requests for a WRITE.
type Stability uint32

const (
	Unstable Stability = 0
	DataSync Stability = 1
	FileSync Stability = 2
)

// Caller is the identity behind a request, taken from AUTH_SYS.
type Caller struct {
	UID, GID uint32
	GIDs     []uint32
}

// Access permission bits (NFSv3 ACCESS3).
const (
	AccessRead    uint32 = 0x0001
	AccessLookup  uint32 = 0x0002
	AccessModify  uint32 = 0x0004
	AccessExtend  uint32 = 0x0008
	AccessDelete  uint32 = 0x0010
	AccessExecute uint32 = 0x0020
)

// FS is a mountable filesystem. Implementations must be safe for concurrent use.
type FS interface {
	Root() Handle

	GetAttr(ctx context.Context, h Handle) (Attr, error)
	SetAttr(ctx context.Context, c Caller, h Handle, sa SetAttr) (Attr, error)
	Lookup(ctx context.Context, c Caller, dir Handle, name string) (Handle, Attr, error)
	Access(ctx context.Context, c Caller, h Handle, want uint32) (uint32, error)

	Read(ctx context.Context, c Caller, h Handle, off uint64, count uint32) (data []byte, eof bool, err error)

	// Write may delay its return as backpressure when the implementation is
	// holding more unwritten data than it is willing to buffer. NFSv3 permits
	// this: an UNSTABLE write need not reach stable storage before the server
	// replies, and nothing bounds how long the server may take to answer, so
	// holding the reply is a legitimate way to slow a client down. A caller
	// must therefore not impose its own per-call deadline on Write, and an
	// implementation that delays must return promptly once ctx is done.
	Write(ctx context.Context, c Caller, h Handle, off uint64, data []byte, how Stability) (uint32, Stability, error)

	Commit(ctx context.Context, h Handle, off uint64, count uint32) error

	Create(ctx context.Context, c Caller, dir Handle, name string, sa SetAttr, excl bool) (Handle, Attr, error)
	Mkdir(ctx context.Context, c Caller, dir Handle, name string, sa SetAttr) (Handle, Attr, error)
	Symlink(ctx context.Context, c Caller, dir Handle, name, target string, sa SetAttr) (Handle, Attr, error)
	ReadLink(ctx context.Context, h Handle) (string, error)
	Remove(ctx context.Context, c Caller, dir Handle, name string) error
	Rmdir(ctx context.Context, c Caller, dir Handle, name string) error
	Rename(ctx context.Context, c Caller, fromDir Handle, fromName string, toDir Handle, toName string) error

	ReadDir(ctx context.Context, c Caller, dir Handle, cookie uint64, verf [8]byte, max uint32, plus bool) (entries []DirEntry, eof bool, outVerf [8]byte, err error)

	FSStat(ctx context.Context, h Handle) (Stat, error)
}

// WeakCache is implemented by backends that can report the pre-operation
// attributes NFS uses for weak cache consistency. It is optional.
type WeakCache interface {
	PreOp(ctx context.Context, h Handle) (size uint64, mtime, ctime time.Time, ok bool)
}
