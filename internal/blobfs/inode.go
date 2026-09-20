// Package blobfs implements a filesystem whose bytes live in one S3 bucket and
// whose namespace lives in another.
//
// The split is the point of the design. The data bucket holds nothing but
// immutable, content-addressed chunks named by the SHA-256 of their contents:
// no filenames, no directory structure, no sizes of logical files. It can
// therefore be shared read-only with other people, or made public, without
// revealing the shape of anyone's tree. The metadata bucket holds the
// namespace -- inodes, directory entries and the chunk lists that reassemble
// files -- and is private to whoever mounts it.
//
// Two consequences fall out for free. Several people can mount the same data
// bucket with different metadata buckets, each seeing a private tree over
// shared bytes. And because chunks are addressed by content, identical data
// written by anyone is stored once.
package blobfs

import (
	"time"

	"strata/internal/vfs"
)

// rootInodeID is the inode number of the filesystem root. NFS clients treat
// fileid 0 as invalid, so numbering starts at 1.
const rootInodeID = 1

// chunkRef points at one chunk of a file's contents.
//
// A ref with an empty Hash is a hole: Size zero bytes that were never written
// and are not stored anywhere. Sparse files therefore cost nothing.
type chunkRef struct {
	Hash string `json:"h,omitempty"`
	Size uint32 `json:"s"`
}

func (c chunkRef) isHole() bool { return c.Hash == "" }

// inode is one file, directory or symlink.
//
// Directories store their entries inline. That is a deliberate PoC
// simplification: it keeps a directory's contents atomic with the rest of the
// snapshot, at the cost of scaling to directories of millions of entries.
type inode struct {
	ID    uint64       `json:"id"`
	Gen   uint64       `json:"gen"`
	Type  vfs.FileType `json:"t"`
	Mode  uint32       `json:"m"`
	UID   uint32       `json:"u"`
	GID   uint32       `json:"g"`
	NLink uint32       `json:"n"`
	Size  uint64       `json:"sz"`

	ATimeNS int64 `json:"at"`
	MTimeNS int64 `json:"mt"`
	CTimeNS int64 `json:"ct"`

	// Entries maps a name to an inode ID. Only set for directories.
	Entries map[string]uint64 `json:"e,omitempty"`
	// Parent lets us answer ".." and detect rename-into-own-subtree.
	Parent uint64 `json:"p,omitempty"`

	// Chunks is the ordered chunk list of a regular file. Chunk i covers
	// logical bytes [i*chunkSize, i*chunkSize+len).
	Chunks []chunkRef `json:"c,omitempty"`

	// Target is the link contents of a symlink.
	Target string `json:"lt,omitempty"`
}

func (n *inode) isDir() bool  { return n.Type == vfs.TypeDir }
func (n *inode) isFile() bool { return n.Type == vfs.TypeReg }
func (n *inode) isLink() bool { return n.Type == vfs.TypeLnk }

func (n *inode) atime() time.Time { return time.Unix(0, n.ATimeNS) }
func (n *inode) mtime() time.Time { return time.Unix(0, n.MTimeNS) }
func (n *inode) ctime() time.Time { return time.Unix(0, n.CTimeNS) }

func (n *inode) touchM(t time.Time) { n.MTimeNS = t.UnixNano(); n.CTimeNS = n.MTimeNS }
func (n *inode) touchC(t time.Time) { n.CTimeNS = t.UnixNano() }
func (n *inode) touchA(t time.Time) { n.ATimeNS = t.UnixNano() }

// used reports the bytes actually stored for this inode, which for a sparse
// file is less than its size.
func (n *inode) used() uint64 {
	var total uint64
	for _, c := range n.Chunks {
		if !c.isHole() {
			total += uint64(c.Size)
		}
	}
	return total
}

// attr renders the inode as NFS attributes.
func (n *inode) attr(fsid uint64) vfs.Attr {
	return vfs.Attr{
		Type:   n.Type,
		Mode:   n.Mode & 0o7777,
		NLink:  n.NLink,
		UID:    n.UID,
		GID:    n.GID,
		Size:   n.Size,
		Used:   n.used(),
		FSID:   fsid,
		FileID: n.ID,
		ATime:  n.atime(),
		MTime:  n.mtime(),
		CTime:  n.ctime(),
	}
}

// snapshot is the serialized form of the entire namespace, stored as one
// gzipped JSON object in the metadata bucket.
//
// Writing the whole namespace on every commit is the PoC's main scaling limit
// and its main simplicity win: a snapshot is self-contained, so a reader needs
// exactly one GET to have a consistent view, and there is no log to replay.
type snapshot struct {
	Version   int               `json:"version"`
	Epoch     uint64            `json:"epoch"`
	NextIno   uint64            `json:"next_ino"`
	ChunkSize uint32            `json:"chunk_size"`
	Inodes    map[uint64]*inode `json:"inodes"`
	CreatedNS int64             `json:"created"`
}

// rootPointer is the tiny object whose atomic replacement commits a snapshot.
// It is the only mutable object in either bucket.
type rootPointer struct {
	Version   int    `json:"version"`
	Epoch     uint64 `json:"epoch"`
	Snapshot  string `json:"snapshot"`
	FSID      uint64 `json:"fsid"`
	WrittenNS int64  `json:"written"`
	// Writer identifies the mount that last committed, purely for diagnostics.
	Writer string `json:"writer,omitempty"`
}

const (
	// rootKey is the metadata object that is compare-and-swapped to commit.
	rootKey = "root"
	// snapshotPrefix is where committed namespace snapshots accumulate.
	snapshotPrefix = "snapshots/"
	// chunkPrefix is where content-addressed data lives in the data bucket.
	chunkPrefix = "chunks/"

	snapshotVersion = 1
)
