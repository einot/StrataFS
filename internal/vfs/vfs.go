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
//
// NFSv4 additionally requires a change attribute — a value that differs
// whenever the object's data or metadata changes — and this struct
// deliberately does not carry one yet. It could not be an optional interface:
// it has to be read in the same snapshot as the fields below, or a client can
// pair a change value from before a mutation with a size from after it, which
// is exactly the tearing the attribute exists to prevent. And a field here that
// no backend populates would report zero for every object, indistinguishable
// from a real value, where a missing optional interface is unambiguous. So it
// arrives in the same change that teaches a backend to populate it. The
// on-disk field is already reserved: see docs/DESIGN.md section 4, and
// docs/nfsv4-assessment.md section 4 for why it was reserved early.
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
	// Cookie identifies this entry's position for a follow-up ReadDir, and is
	// opaque to the client: it is valid only while the verifier returned
	// beside it still matches.
	//
	// Values 0, 1 and 2 are reserved and must never be issued for a real
	// entry. 0 means "start of directory" in both protocol versions, and
	// NFSv4's READDIR reserves 1 and 2 as well (RFC 8881 — recalled, not
	// verified against the section text). A backend that synthesises "." and
	// ".." gives them cookies 1 and 2 and returns them first, because NFSv4
	// does not list them and a caller serving it drops them. Satisfying this
	// by accident is not the same as satisfying it, which is why it is written
	// here.
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

	// MaxFileSize is the largest size, in bytes, that a regular file may
	// reach, which the NFS server advertises as FSINFO's maxfilesize (RFC
	// 1813 §3.3.19). It is fixed for the life of the FS value.
	//
	// No method writes a byte at an offset at or past MaxFileSize, or sets a
	// size above it; SetAttr, Create and Write say what each does instead,
	// and a method added later that can extend a file must say the same. A
	// file already larger, left by a build that allowed more, can still be
	// read whole, written below the limit, shrunk to it, renamed and removed.
	MaxFileSize() uint64

	GetAttr(ctx context.Context, h Handle) (Attr, error)

	// SetAttr changes the attributes sa sets. If sa.Size is above
	// MaxFileSize it fails with ErrFBig, whatever the file's current size,
	// and applies none of sa.
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
	//
	// Write never writes a byte at an offset at or past MaxFileSize. A write
	// of at least one byte that starts there fails with ErrFBig and changes
	// nothing. One that starts below MaxFileSize and would run past it
	// writes only the bytes below MaxFileSize and returns how many that was:
	// a short write, which RFC 1813 §3.3.7 permits, after which a client
	// sends the rest in another WRITE and is refused. An empty write is not
	// refused for its offset.
	Write(ctx context.Context, c Caller, h Handle, off uint64, data []byte, how Stability) (uint32, Stability, error)

	Commit(ctx context.Context, h Handle, off uint64, count uint32) error

	// Create fails with ErrFBig, before it creates or changes anything and
	// whether or not name exists, if sa.Size is set and above MaxFileSize.
	Create(ctx context.Context, c Caller, dir Handle, name string, sa SetAttr, excl bool) (Handle, Attr, error)
	Mkdir(ctx context.Context, c Caller, dir Handle, name string, sa SetAttr) (Handle, Attr, error)
	Symlink(ctx context.Context, c Caller, dir Handle, name, target string, sa SetAttr) (Handle, Attr, error)
	ReadLink(ctx context.Context, h Handle) (string, error)
	Remove(ctx context.Context, c Caller, dir Handle, name string) error
	Rmdir(ctx context.Context, c Caller, dir Handle, name string) error
	Rename(ctx context.Context, c Caller, fromDir Handle, fromName string, toDir Handle, toName string) error

	// ReadDir lists dir starting at cookie. See DirEntry.Cookie for the cookie
	// values a backend may and may not issue.
	ReadDir(ctx context.Context, c Caller, dir Handle, cookie uint64, verf [8]byte, max uint32, plus bool) (entries []DirEntry, eof bool, outVerf [8]byte, err error)

	FSStat(ctx context.Context, h Handle) (Stat, error)
}

// WeakCache is implemented by backends that can report the pre-operation
// attributes NFS uses for weak cache consistency. It is optional.
type WeakCache interface {
	PreOp(ctx context.Context, h Handle) (size uint64, mtime, ctime time.Time, ok bool)
}

// ---- optional NFSv4.2 capabilities ----
//
// The interfaces below are optional in the same sense as WeakCache: a caller
// discovers one with a type assertion, and a backend that does not implement it
// is simply a backend whose server answers NFS4ERR_NOTSUPP for the operation it
// serves. Nothing in this repository implements them and no NFSv4 server exists
// yet. They are here because the filesystem that will have to satisfy them is
// being written now, and a backend shaped without them in view forecloses them
// cheaply and silently; docs/nfsv4-assessment.md records that reasoning and what
// else an NFSv4 server would need.
//
// On the citations: the operation numbers and the XDR enumerations come from
// RFC 7863, which was read directly. RFC 7862 section 15, which fixes each
// operation's exact error conventions, could not be retrieved when these were
// written — so every "returns X" below is a reading of the protocol rather than
// a quote from it, and must be checked against the RFC before the first
// implementation ships.

// Extent is one run of a file that is either stored data or a hole.
//
// Extents describing a range are ordered by Off, do not overlap, leave no gap
// between them, and never place two extents of the same kind side by side.
type Extent struct {
	Off  uint64
	Len  uint64
	Hole bool
}

// Sparse is implemented by backends that know where a file's holes are. It
// serves NFSv4.2 SEEK (operation 69) and READ_PLUS (operation 68), which let a
// client skip a sparse region instead of transferring zeros for it.
//
// Every method must answer from metadata alone and perform no data I/O. A
// backend that would have to fetch file contents to answer must not implement
// this interface at all: the caller's fallback, treating the whole file as
// data, is cheaper than the fetch and no less correct.
//
// Reporting data where there is in fact a hole is always permitted, because a
// server may return data in place of a hole. Reporting a hole where there is
// data is not: the client would read zeros in place of bytes that exist. A
// backend tracking holes at a coarse granularity therefore rounds towards data,
// and a run of zeros that was explicitly written need not be reported as a hole.
type Sparse interface {
	// SeekData returns the offset of the first byte of data at or after off.
	// ErrNXIO reports that there is none before the end of the file, which is
	// also the answer when off is at or past the end of the file.
	SeekData(ctx context.Context, h Handle, off uint64) (uint64, error)

	// SeekHole returns the offset of the first hole at or after off. A file
	// always ends in a virtual hole, so this succeeds for any off inside the
	// file; ErrNXIO reports that off is at or past the end of the file.
	SeekHole(ctx context.Context, h Handle, off uint64) (uint64, error)

	// Extents describes [off, off+length) as an ordered run of data and hole
	// extents, clipped to the end of the file. A range that is empty after
	// clipping yields no extents and no error.
	Extents(ctx context.Context, h Handle, off, length uint64) ([]Extent, error)
}

// Deallocator punches a hole in a file, serving NFSv4.2 DEALLOCATE
// (operation 62).
//
// Afterwards the range reads back as zeros and Attr.Used no longer counts it.
// The file's size must not change, which is what separates this from a
// truncate: deallocating the tail of a file leaves the file the same length. A
// backend that can only release storage in whole blocks zero-fills the
// remainder, and one that releases nothing at all still satisfies the contract,
// pointlessly — the observable behaviour is identical in every case.
type Deallocator interface {
	Deallocate(ctx context.Context, c Caller, h Handle, off, length uint64) error
}

// Cloner copies a range between files without moving the bytes through the
// caller. It serves NFSv4.2 CLONE (operation 71) and the intra-server,
// synchronous form of COPY (operation 60).
//
// A backend implements this only if the copy really is a metadata operation —
// splicing references in a content-addressed store, say. One that would read
// and rewrite the data must not implement it, because the caller's
// read-then-write fallback does the same work without claiming otherwise, and
// the claim is the whole value of the operation.
type Cloner interface {
	// CloneRange makes dst's [dstOff, dstOff+length) hold what src's
	// [srcOff, srcOff+length) holds, extending dst if it is shorter. It either
	// applies to the whole range or returns an error having applied nothing,
	// and it is atomic with respect to other operations on this server;
	// durability remains whatever the backend's commit contract already says.
	//
	// Any offsets are accepted, aligned or not: CloneBlockSize is advice, not
	// a precondition. ErrInval reports overlapping ranges within one file, a
	// source range running past the end of src, or a handle that is not a
	// regular file.
	CloneRange(ctx context.Context, c Caller, src Handle, srcOff uint64, dst Handle, dstOff, length uint64) error

	// CloneBlockSize is the alignment at which CloneRange moves no data at
	// all; an unaligned range costs a rewrite of the partial block at each
	// end. It is reported to clients as NFSv4.2's clone_blksize attribute so
	// that they can align their requests, and must be a power of two.
	CloneBlockSize() uint32
}

// Pinner keeps an inode addressable after its last name is removed. It serves
// NFSv4's OPEN and CLOSE: a file that is open when it is unlinked must stay
// readable and writable through the handles already held, until the last open
// is closed. An NFSv3 client fakes this for itself by renaming the file aside
// before unlinking it; an NFSv4 server owes it.
//
// This is the only part of open state that reaches the filesystem. Share
// reservations, stateids, lock owners and byte-range locks are protocol
// bookkeeping and stay in the NFS layer, where no storage backend has to
// reimplement them and nothing outside this server can violate them.
type Pinner interface {
	// Pin holds the inode that h names until the returned function is called.
	// While it is pinned, removing every name for that inode must not make h
	// stale: GetAttr, Read and Write keep working, and the attributes report
	// the true link count, which may be zero. The backend reclaims the inode
	// once the last pin is released and no name refers to it.
	//
	// Pin fails with ErrStale if h is already stale. The returned function is
	// idempotent, so a caller may release from more than one error path.
	Pin(ctx context.Context, h Handle) (release func(), err error)
}
