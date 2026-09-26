package blobfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// Config configures a blobfs mount.
type Config struct {
	// Store is the bucket holding the entire filesystem: content-addressed
	// chunks under one key prefix, the namespace under others.
	Store store.Store

	ChunkSize      uint32
	CacheBytes     int64
	CommitInterval time.Duration

	// OwnerUID and OwnerGID own the root directory of a freshly created
	// filesystem.
	OwnerUID, OwnerGID uint32

	// ReadOnly refuses every mutating operation at the VFS layer.
	ReadOnly bool

	// SkipChunkVerification stops fetched chunks from being checked against
	// the hash that names them. Verification is on unless this is set: the
	// negative sense is deliberate, so that the zero value is the safe one
	// for every caller that never thinks about it.
	SkipChunkVerification bool

	// SnapshotRetention is how many superseded namespace snapshots to keep.
	// Zero selects the default; a negative value disables pruning entirely.
	SnapshotRetention int

	// MaxDirtyBytes bounds the memory held by unflushed writes across all open
	// files, measured as the capacity of their buffers rather than the count of
	// logically dirty bytes (see the accounting rule below). Zero selects the
	// default (256 MiB); a negative value disables backpressure and lets the
	// buffer grow without limit.
	MaxDirtyBytes int64

	Log *slog.Logger
}

// FS is an S3-backed filesystem implementing vfs.FS.
type FS struct {
	store store.Store
	log   *slog.Logger

	chunkSize      uint32
	commitInterval time.Duration
	ownerUID       uint32
	ownerGID       uint32
	readOnly       bool
	skipVerify     bool

	// mu guards the namespace: the inode table, the allocator, the epoch and
	// the dirty flags.
	//
	// Lock ordering is openFile.mu before mu. Nothing may acquire an
	// openFile.mu while holding mu, which is why committing flushes file data
	// before taking mu rather than underneath it.
	//
	// The budget's mutex is a leaf: it may be taken while holding either of
	// the others, but no other lock may be acquired while it is held, and
	// nothing may wait on its condition variable while holding either of the
	// others (ADR 0003 §3). Write's wait for the budget therefore happens
	// before it takes any of them.
	mu         sync.RWMutex
	inodes     map[uint64]*inode
	nextIno    uint64
	epoch      uint64
	fsid       uint64
	rootETag   string
	dirty      bool
	diverged   bool
	lastCommit time.Time

	// retention is how many namespace snapshots to keep behind the current
	// one; pruning guards against a busy mount filling the bucket.
	retention int
	pruning   bool

	// open holds buffered writes per inode. An entry enters only through
	// getOpen and leaves only through dropOpen as its inode leaves the table,
	// and an inode with no entry has no dirty buffers or pending trims, which
	// is what lets Read take its view under mu alone for such a file (ADR 0005
	// §3, invariants 2 and 3).
	open map[uint64]*openFile

	// budget counts the buffer capacity held across open and holds writers
	// back once it reaches Config.MaxDirtyBytes (ADR 0003). Every FS has one;
	// with backpressure disabled it still counts, so DirtyBytes stays
	// meaningful.
	budget *budget

	// cache keeps its own copies of the chunks it holds (ADR 0004), so no
	// dirty buffer is ever shared with it. New sets it once and nothing
	// reassigns it.
	cache    *chunkCache
	writerID string

	// knownChunks remembers hashes already present in the bucket so a
	// rewrite of identical content costs nothing.
	knownMu     sync.Mutex
	knownChunks map[string]struct{}

	// writeVerf lets clients detect that the server restarted and that
	// unstable writes must be replayed. It is regenerated every boot.
	writeVerf [8]byte
}

// openFile buffers not-yet-uploaded chunks for one inode.
type openFile struct {
	mu    sync.Mutex
	dirty map[uint64][]byte // chunk index -> full chunk contents
	// pendingTrim holds, by chunk index, the length a truncate cut a stored
	// chunk to. truncate cannot fetch the chunk under the namespace lock, so
	// the next flush applies the trim, and until then Read and bufferWrite
	// honour it: the index holds the chunk's first to bytes, then zeros (ADR
	// 0006 §1, §2). It is never buffered and never charged. An index is never
	// both here and in dirty (I1), and a file has at most one entry (I4). It
	// may be nil after a reset; only truncate inserts, and it allocates first.
	pendingTrim map[uint64]trimReq

	// bytes is the sum of cap(v) over dirty, this file's share of FS.budget:
	// capacity rather than length, because a buffer is resident at its
	// capacity however little of it is written (ADR 0003 §2). It is atomic
	// rather than guarded by mu because removing the file from FS.open
	// releases it under FS.mu alone, where taking mu would invert the lock
	// order.
	bytes atomic.Int64
}

const defaultChunkSize = 1 << 20

// defaultSnapshotRetention keeps a short rollback window without letting the
// bucket grow without bound.
const defaultSnapshotRetention = 10

// defaultMaxDirtyBytes is the budget for buffered writes when Config leaves
// MaxDirtyBytes zero (ADR 0003 §1).
const defaultMaxDirtyBytes = 256 << 20

// FS implements the full mountable filesystem contract.
var _ vfs.FS = (*FS)(nil)

// New opens or creates a filesystem in the bucket.
func New(ctx context.Context, cfg Config) (*FS, error) {
	if cfg.Store == nil {
		return nil, errors.New("blobfs: a store is required")
	}
	if cfg.ChunkSize == 0 {
		cfg.ChunkSize = defaultChunkSize
	}
	if cfg.ChunkSize < 4096 {
		return nil, fmt.Errorf("blobfs: chunk size %d is too small", cfg.ChunkSize)
	}
	if cfg.CommitInterval == 0 {
		cfg.CommitInterval = 5 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	// Only zero is replaced: a negative limit, which disables waiting, reaches
	// the budget as it is, so limit() reports what the embedder asked for
	// (ADR 0003 §1).
	if cfg.MaxDirtyBytes == 0 {
		cfg.MaxDirtyBytes = defaultMaxDirtyBytes
	}
	switch {
	case cfg.SnapshotRetention == 0:
		cfg.SnapshotRetention = defaultSnapshotRetention
	case cfg.SnapshotRetention < 0:
		cfg.SnapshotRetention = 0 // keep everything
	}

	host, _ := os.Hostname()
	f := &FS{
		store:          cfg.Store,
		log:            cfg.Log,
		chunkSize:      cfg.ChunkSize,
		commitInterval: cfg.CommitInterval,
		ownerUID:       cfg.OwnerUID,
		ownerGID:       cfg.OwnerGID,
		readOnly:       cfg.ReadOnly,
		skipVerify:     cfg.SkipChunkVerification,
		retention:      cfg.SnapshotRetention,
		open:           make(map[uint64]*openFile),
		budget:         newBudget(cfg.MaxDirtyBytes, cfg.Log),
		cache:          newChunkCache(cfg.CacheBytes),
		knownChunks:    make(map[string]struct{}),
		writerID:       fmt.Sprintf("%s/%d", host, os.Getpid()),
	}
	rand.Read(f.writeVerf[:])

	if err := f.loadOrInit(ctx); err != nil {
		return nil, err
	}
	return f, nil
}

// Run drives background commits until ctx is cancelled.
func (f *FS) Run(ctx context.Context) {
	if f.readOnly {
		<-ctx.Done()
		return
	}
	f.committer(ctx)
}

// WriteVerf returns the per-boot write verifier reported in WRITE and COMMIT.
func (f *FS) WriteVerf() [8]byte { return f.writeVerf }

// Stats reports counters for the status line.
func (f *FS) Stats() (epoch uint64, inodes int, cacheHits, cacheMisses, cacheBytes int64) {
	f.mu.RLock()
	epoch, inodes = f.epoch, len(f.inodes)
	f.mu.RUnlock()
	h, m, b := f.cache.stats()
	return epoch, inodes, h, m, b
}

// DirtyBytes reports the memory currently held by unflushed writes across all
// open files: the sum of cap(b) over every buffered chunk b, which is what is
// resident, not the count of logically dirty bytes. A file with one byte
// written into each of three chunk indices reports three chunk sizes. It is
// safe for concurrent use.
func (f *FS) DirtyBytes() int64 { return f.budget.used() }

// ---- handles ----

// Root returns the handle of the root directory.
func (f *FS) Root() vfs.Handle {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return encodeHandle(rootInodeID, f.inodes[rootInodeID].Gen)
}

// encodeHandle packs an inode number and generation into a 16-byte handle. The
// generation makes a handle to a deleted-and-recreated inode report ESTALE
// instead of silently addressing a different file.
func encodeHandle(id, gen uint64) vfs.Handle {
	h := make([]byte, 16)
	binary.BigEndian.PutUint64(h[0:8], id)
	binary.BigEndian.PutUint64(h[8:16], gen)
	return h
}

func decodeHandle(h vfs.Handle) (id, gen uint64, ok bool) {
	if len(h) != 16 {
		return 0, 0, false
	}
	return binary.BigEndian.Uint64(h[0:8]), binary.BigEndian.Uint64(h[8:16]), true
}

// resolve looks up the inode a handle names. Callers must hold at least RLock.
func (f *FS) resolve(h vfs.Handle) (*inode, error) {
	id, gen, ok := decodeHandle(h)
	if !ok {
		return nil, vfs.ErrBadHandle
	}
	n, ok := f.inodes[id]
	if !ok || n.Gen != gen {
		return nil, vfs.ErrStale
	}
	return n, nil
}

// ---- permissions ----

// permitted applies classic Unix mode checks. want is a bitmask of 4 (read),
// 2 (write) and 1 (execute/search).
func permitted(n *inode, c vfs.Caller, want uint32) bool {
	if c.UID == 0 {
		// Root may read and write anything. Execute still requires that some
		// execute bit is set, matching Unix behaviour.
		if want&1 != 0 && n.Mode&0o111 == 0 && !n.isDir() {
			return false
		}
		return true
	}
	var bits uint32
	switch {
	case c.UID == n.UID:
		bits = (n.Mode >> 6) & 7
	case inGroup(c, n.GID):
		bits = (n.Mode >> 3) & 7
	default:
		bits = n.Mode & 7
	}
	return bits&want == want
}

func inGroup(c vfs.Caller, gid uint32) bool {
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

// mutable reports whether the mount accepts changes at all.
func (f *FS) mutable() error {
	if f.readOnly {
		return vfs.ErrROFS
	}
	if f.diverged {
		return vfs.ErrStale
	}
	return nil
}

const maxNameLen = 255

func validName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return vfs.ErrInval
	case len(name) > maxNameLen:
		return vfs.ErrNameTooLong
	case strings.ContainsAny(name, "/\x00"):
		return vfs.ErrInval
	}
	return nil
}

// ---- attribute operations ----

func (f *FS) GetAttr(ctx context.Context, h vfs.Handle) (vfs.Attr, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	n, err := f.resolve(h)
	if err != nil {
		return vfs.Attr{}, err
	}
	return n.attr(f.fsid), nil
}

func (f *FS) SetAttr(ctx context.Context, c vfs.Caller, h vfs.Handle, sa vfs.SetAttr) (vfs.Attr, error) {
	if err := f.mutable(); err != nil {
		return vfs.Attr{}, err
	}

	// A size change is a truncate, which touches file data and therefore needs
	// the per-file lock taken before the namespace lock.
	if sa.Size != nil {
		if err := f.truncate(ctx, c, h, *sa.Size); err != nil {
			return vfs.Attr{}, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.resolve(h)
	if err != nil {
		return vfs.Attr{}, err
	}

	now := time.Now()
	// Only the owner or root may change mode or ownership.
	if sa.Mode != nil || sa.UID != nil || sa.GID != nil {
		if c.UID != 0 && c.UID != n.UID {
			return vfs.Attr{}, vfs.ErrPerm
		}
	}
	if sa.Mode != nil {
		n.Mode = *sa.Mode & 0o7777
		n.touchC(now)
	}
	if sa.UID != nil {
		// Only root may give a file away.
		if c.UID != 0 && *sa.UID != n.UID {
			return vfs.Attr{}, vfs.ErrPerm
		}
		n.UID = *sa.UID
		n.touchC(now)
	}
	if sa.GID != nil {
		if c.UID != 0 && !inGroup(c, *sa.GID) {
			return vfs.Attr{}, vfs.ErrPerm
		}
		n.GID = *sa.GID
		n.touchC(now)
	}
	if sa.ATime != nil {
		if c.UID != 0 && c.UID != n.UID && !permitted(n, c, 2) {
			return vfs.Attr{}, vfs.ErrPerm
		}
		n.ATimeNS = sa.ATime.UnixNano()
		n.touchC(now)
	}
	if sa.MTime != nil {
		if c.UID != 0 && c.UID != n.UID && !permitted(n, c, 2) {
			return vfs.Attr{}, vfs.ErrPerm
		}
		n.MTimeNS = sa.MTime.UnixNano()
		n.touchC(now)
	}
	f.dirty = true
	return n.attr(f.fsid), nil
}

func (f *FS) Access(ctx context.Context, c vfs.Caller, h vfs.Handle, want uint32) (uint32, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	n, err := f.resolve(h)
	if err != nil {
		return 0, err
	}

	var granted uint32
	if permitted(n, c, 4) {
		granted |= vfs.AccessRead
		if n.isDir() {
			granted |= vfs.AccessLookup
		}
	}
	if n.isDir() && permitted(n, c, 1) {
		granted |= vfs.AccessLookup
	}
	if !f.readOnly && permitted(n, c, 2) {
		granted |= vfs.AccessModify | vfs.AccessExtend
		if n.isDir() {
			granted |= vfs.AccessDelete
		}
	}
	if !n.isDir() && n.Mode&0o111 != 0 && permitted(n, c, 1) {
		granted |= vfs.AccessExecute
	}
	return granted & want, nil
}

func (f *FS) Lookup(ctx context.Context, c vfs.Caller, dir vfs.Handle, name string) (vfs.Handle, vfs.Attr, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	d, err := f.resolve(dir)
	if err != nil {
		return nil, vfs.Attr{}, err
	}
	if !d.isDir() {
		return nil, vfs.Attr{}, vfs.ErrNotDir
	}
	if !permitted(d, c, 1) {
		return nil, vfs.Attr{}, vfs.ErrAcces
	}

	var target *inode
	switch name {
	case ".":
		target = d
	case "..":
		p := d.Parent
		if p == 0 {
			p = rootInodeID
		}
		target = f.inodes[p]
	default:
		id, ok := d.Entries[name]
		if !ok {
			return nil, vfs.Attr{}, vfs.ErrNoEnt
		}
		target = f.inodes[id]
	}
	if target == nil {
		return nil, vfs.Attr{}, vfs.ErrNoEnt
	}
	return encodeHandle(target.ID, target.Gen), target.attr(f.fsid), nil
}

func (f *FS) ReadLink(ctx context.Context, h vfs.Handle) (string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	n, err := f.resolve(h)
	if err != nil {
		return "", err
	}
	if !n.isLink() {
		return "", vfs.ErrInval
	}
	return n.Target, nil
}

func (f *FS) FSStat(ctx context.Context, h vfs.Handle) (vfs.Stat, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if _, err := f.resolve(h); err != nil {
		return vfs.Stat{}, err
	}
	var used uint64
	for _, n := range f.inodes {
		used += n.used()
	}
	// Object storage has no meaningful capacity, but clients and Finder insist
	// on a number. Report what is actually stored plus a large headroom so
	// nothing believes the volume is full.
	const headroom = 1 << 50 // 1 PiB
	return vfs.Stat{
		TotalBytes: used + headroom,
		FreeBytes:  headroom,
		AvailBytes: headroom,
		TotalFiles: uint64(len(f.inodes)) + 1<<30,
		FreeFiles:  1 << 30,
	}, nil
}

// ---- namespace mutation ----

// allocInode creates a new inode. Caller must hold mu for writing.
func (f *FS) allocInode(t vfs.FileType, mode uint32, c vfs.Caller, parent uint64) *inode {
	now := time.Now()
	n := &inode{
		ID:      f.nextIno,
		Gen:     uint64(now.UnixNano()),
		Type:    t,
		Mode:    mode & 0o7777,
		UID:     c.UID,
		GID:     c.GID,
		NLink:   1,
		Parent:  parent,
		ATimeNS: now.UnixNano(),
		MTimeNS: now.UnixNano(),
		CTimeNS: now.UnixNano(),
	}
	if t == vfs.TypeDir {
		n.Entries = map[string]uint64{}
		n.NLink = 2
	}
	f.nextIno++
	f.inodes[n.ID] = n
	f.dirty = true
	return n
}

// createEntry is the shared body of CREATE, MKDIR and SYMLINK.
func (f *FS) createEntry(ctx context.Context, c vfs.Caller, dir vfs.Handle, name string, t vfs.FileType, sa vfs.SetAttr, target string, excl bool) (vfs.Handle, vfs.Attr, error) {
	if err := f.mutable(); err != nil {
		return nil, vfs.Attr{}, err
	}
	if err := validName(name); err != nil {
		return nil, vfs.Attr{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	d, err := f.resolve(dir)
	if err != nil {
		return nil, vfs.Attr{}, err
	}
	if !d.isDir() {
		return nil, vfs.Attr{}, vfs.ErrNotDir
	}
	if !permitted(d, c, 2|1) {
		return nil, vfs.Attr{}, vfs.ErrAcces
	}

	if existing, ok := d.Entries[name]; ok {
		if t == vfs.TypeReg && !excl {
			// Non-exclusive CREATE on an existing file is a plain open; NFS
			// expects success, with truncation handled by the size attribute.
			n := f.inodes[existing]
			if n == nil {
				return nil, vfs.Attr{}, vfs.ErrNoEnt
			}
			if n.isDir() {
				return nil, vfs.Attr{}, vfs.ErrExist
			}
			return encodeHandle(n.ID, n.Gen), n.attr(f.fsid), nil
		}
		return nil, vfs.Attr{}, vfs.ErrExist
	}

	mode := uint32(0o644)
	if t == vfs.TypeDir {
		mode = 0o755
	}
	if t == vfs.TypeLnk {
		mode = 0o777
	}
	if sa.Mode != nil {
		mode = *sa.Mode
	}

	n := f.allocInode(t, mode, c, d.ID)
	if sa.UID != nil && c.UID == 0 {
		n.UID = *sa.UID
	}
	if sa.GID != nil {
		n.GID = *sa.GID
	}
	if t == vfs.TypeLnk {
		n.Target = target
		n.Size = uint64(len(target))
	}

	d.Entries[name] = n.ID
	if t == vfs.TypeDir {
		d.NLink++
	}
	d.touchM(time.Now())
	f.dirty = true
	return encodeHandle(n.ID, n.Gen), n.attr(f.fsid), nil
}

func (f *FS) Create(ctx context.Context, c vfs.Caller, dir vfs.Handle, name string, sa vfs.SetAttr, excl bool) (vfs.Handle, vfs.Attr, error) {
	return f.createEntry(ctx, c, dir, name, vfs.TypeReg, sa, "", excl)
}

func (f *FS) Mkdir(ctx context.Context, c vfs.Caller, dir vfs.Handle, name string, sa vfs.SetAttr) (vfs.Handle, vfs.Attr, error) {
	return f.createEntry(ctx, c, dir, name, vfs.TypeDir, sa, "", true)
}

func (f *FS) Symlink(ctx context.Context, c vfs.Caller, dir vfs.Handle, name, target string, sa vfs.SetAttr) (vfs.Handle, vfs.Attr, error) {
	if target == "" {
		return nil, vfs.Attr{}, vfs.ErrInval
	}
	return f.createEntry(ctx, c, dir, name, vfs.TypeLnk, sa, target, true)
}

// unlink removes a name, requiring the target to be (or not be) a directory.
func (f *FS) unlink(ctx context.Context, c vfs.Caller, dir vfs.Handle, name string, wantDir bool) error {
	if err := f.mutable(); err != nil {
		return err
	}
	if err := validName(name); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	d, err := f.resolve(dir)
	if err != nil {
		return err
	}
	if !d.isDir() {
		return vfs.ErrNotDir
	}
	if !permitted(d, c, 2|1) {
		return vfs.ErrAcces
	}

	id, ok := d.Entries[name]
	if !ok {
		return vfs.ErrNoEnt
	}
	n := f.inodes[id]
	if n == nil {
		delete(d.Entries, name)
		return vfs.ErrNoEnt
	}
	switch {
	case wantDir && !n.isDir():
		return vfs.ErrNotDir
	case !wantDir && n.isDir():
		return vfs.ErrIsDir
	case wantDir && len(n.Entries) > 0:
		return vfs.ErrNotEmpty
	}

	delete(d.Entries, name)
	if n.isDir() {
		d.NLink--
	}
	d.touchM(time.Now())

	n.NLink--
	if n.NLink == 0 {
		// The inode is gone from the namespace. Its chunks stay in the data
		// bucket: they are content-addressed and may be shared with other
		// files or other people's trees, so reclaiming them is a garbage
		// collection problem, handled separately and never inline.
		delete(f.inodes, id)
		f.dropOpen(id)
	}
	f.dirty = true
	return nil
}

// dropOpen removes id's buffered writes from f.open, first releasing their
// charge: nothing will flush them once the entry is gone, so a charge left
// behind would hold the budget up for good (ADR 0003 §2, site 5). Every
// removal from f.open goes through here. Caller must hold mu for writing. It
// takes no openFile.mu, which would invert the lock order, so it zeroes
// of.bytes without clearing of.dirty; Swap keeps the release exactly-once
// against a flush releasing the same file concurrently.
func (f *FS) dropOpen(id uint64) {
	if of := f.open[id]; of != nil {
		f.budget.release(of.bytes.Swap(0))
	}
	delete(f.open, id)
}

func (f *FS) Remove(ctx context.Context, c vfs.Caller, dir vfs.Handle, name string) error {
	return f.unlink(ctx, c, dir, name, false)
}

func (f *FS) Rmdir(ctx context.Context, c vfs.Caller, dir vfs.Handle, name string) error {
	return f.unlink(ctx, c, dir, name, true)
}

func (f *FS) Rename(ctx context.Context, c vfs.Caller, fromDir vfs.Handle, fromName string, toDir vfs.Handle, toName string) error {
	if err := f.mutable(); err != nil {
		return err
	}
	if err := validName(fromName); err != nil {
		return err
	}
	if err := validName(toName); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	src, err := f.resolve(fromDir)
	if err != nil {
		return err
	}
	dst, err := f.resolve(toDir)
	if err != nil {
		return err
	}
	if !src.isDir() || !dst.isDir() {
		return vfs.ErrNotDir
	}
	if !permitted(src, c, 2|1) || !permitted(dst, c, 2|1) {
		return vfs.ErrAcces
	}

	id, ok := src.Entries[fromName]
	if !ok {
		return vfs.ErrNoEnt
	}
	moving := f.inodes[id]
	if moving == nil {
		return vfs.ErrNoEnt
	}

	// Moving a directory into its own subtree would detach that subtree from
	// the root and leak it.
	if moving.isDir() && f.isAncestor(id, dst.ID) {
		return vfs.ErrInval
	}

	if victimID, exists := dst.Entries[toName]; exists {
		if victimID == id {
			return nil // renaming a name to itself
		}
		victim := f.inodes[victimID]
		if victim != nil {
			switch {
			case victim.isDir() && !moving.isDir():
				return vfs.ErrIsDir
			case !victim.isDir() && moving.isDir():
				return vfs.ErrNotDir
			case victim.isDir() && len(victim.Entries) > 0:
				return vfs.ErrNotEmpty
			}
			victim.NLink--
			if victim.isDir() {
				dst.NLink--
			}
			if victim.NLink == 0 {
				delete(f.inodes, victimID)
				f.dropOpen(victimID)
			}
		}
	}

	delete(src.Entries, fromName)
	dst.Entries[toName] = id
	if moving.isDir() && src.ID != dst.ID {
		src.NLink--
		dst.NLink++
	}
	moving.Parent = dst.ID
	moving.touchC(time.Now())
	src.touchM(time.Now())
	dst.touchM(time.Now())
	f.dirty = true
	return nil
}

// isAncestor reports whether ancestor is at or above node in the tree.
// Caller must hold mu.
func (f *FS) isAncestor(ancestor, node uint64) bool {
	seen := 0
	for id := node; id != 0; {
		if id == ancestor {
			return true
		}
		n := f.inodes[id]
		if n == nil || id == rootInodeID {
			return false
		}
		id = n.Parent
		// Defend against a cycle in corrupt metadata rather than spinning.
		if seen++; seen > len(f.inodes)+1 {
			return false
		}
	}
	return false
}

// ---- directory reading ----

func (f *FS) ReadDir(ctx context.Context, c vfs.Caller, dir vfs.Handle, cookie uint64, verf [8]byte, max uint32, plus bool) ([]vfs.DirEntry, bool, [8]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	var zero [8]byte
	d, err := f.resolve(dir)
	if err != nil {
		return nil, false, zero, err
	}
	if !d.isDir() {
		return nil, false, zero, vfs.ErrNotDir
	}
	if !permitted(d, c, 4) {
		return nil, false, zero, vfs.ErrAcces
	}

	names := make([]string, 0, len(d.Entries))
	for name := range d.Entries {
		names = append(names, name)
	}
	sort.Strings(names)

	// The cookie is an index into this sorted listing, so it is only valid
	// while the listing is unchanged. The verifier detects any change and makes
	// the client restart rather than silently skip or repeat entries.
	cur := dirVerifier(names)
	if cookie != 0 && verf != zero && verf != cur {
		return nil, false, zero, vfs.ErrBadCookie
	}

	parent := d.Parent
	if parent == 0 {
		parent = rootInodeID
	}
	// "." and ".." occupy cookies 1 and 2; real entries start at 3.
	type pending struct {
		name string
		id   uint64
	}
	all := make([]pending, 0, len(names)+2)
	all = append(all, pending{".", d.ID}, pending{"..", parent})
	for _, name := range names {
		all = append(all, pending{name, d.Entries[name]})
	}

	if cookie > uint64(len(all)) {
		return nil, false, cur, vfs.ErrBadCookie
	}

	// A rough per-entry cost keeps the reply inside the client's byte budget.
	// READDIRPLUS entries carry a handle and full attributes and so cost more.
	perEntry := uint32(32)
	if plus {
		perEntry = 160
	}
	budget := max
	if budget < perEntry*2 {
		budget = perEntry * 2
	}

	out := make([]vfs.DirEntry, 0, 16)
	var used uint32
	i := int(cookie)
	for ; i < len(all); i++ {
		e := all[i]
		n := f.inodes[e.id]
		if n == nil {
			continue // entry raced with a delete; skip it
		}
		cost := perEntry + uint32(len(e.name))
		if used+cost > budget && len(out) > 0 {
			break
		}
		de := vfs.DirEntry{
			FileID: n.ID,
			Name:   e.name,
			Cookie: uint64(i + 1),
		}
		if plus {
			a := n.attr(f.fsid)
			de.Handle = encodeHandle(n.ID, n.Gen)
			de.Attr = &a
		}
		out = append(out, de)
		used += cost
	}
	return out, i >= len(all), cur, nil
}

// dirVerifier hashes a directory listing so that any change invalidates
// outstanding cookies.
func dirVerifier(names []string) [8]byte {
	h := fnv.New64a()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], h.Sum64())
	return out
}

// ---- data path ----

// chunkIndex and chunkOffset split a logical file offset.
func (f *FS) chunkIndex(off uint64) (idx uint64, within uint32) {
	cs := uint64(f.chunkSize)
	return off / cs, uint32(off % cs)
}

// getOpen returns the write buffer for an inode, creating it if needed.
func (f *FS) getOpen(id uint64) *openFile {
	f.mu.Lock()
	defer f.mu.Unlock()
	of, ok := f.open[id]
	if !ok {
		of = &openFile{dirty: make(map[uint64][]byte)}
		f.open[id] = of
	}
	return of
}

// loadChunk fetches one chunk's contents, consulting the cache first. It must
// not be called while holding mu.
//
// Its result may be the cache's shared slice, and a caller cannot tell a hit
// from a miss, so every result falls under the cache's rule: a caller must not
// modify it or append to it, directly or through a reslice, and must copy it
// before changing it (ADR 0004 §3).
func (f *FS) loadChunk(ctx context.Context, ref chunkRef) ([]byte, error) {
	if ref.isHole() {
		return make([]byte, ref.Size), nil
	}
	if data, ok := f.cache.get(ref.Hash); ok {
		return data, nil
	}
	data, err := f.store.Get(ctx, chunkPrefix+ref.Hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The namespace references a chunk the bucket does not hold. A
			// commit always stores chunks before the namespace that names
			// them, so this means the bucket has lost objects.
			f.log.Error("missing chunk referenced by namespace", "hash", ref.Hash)
			return nil, vfs.ErrIO
		}
		return nil, fmt.Errorf("read chunk %s: %w", ref.Hash, err)
	}
	// The key is the SHA-256 of the contents, so checking what came back costs
	// one hash of bytes already in memory and catches bit rot, a truncated
	// transfer that still returned 200, and our own mixed-up references. Bad
	// data must not reach the cache or the known-chunk set: a hash recorded as
	// present would let a later putChunk of the correct content skip its
	// upload, cementing the corruption.
	if !f.skipVerify {
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if got != ref.Hash {
			f.log.Error("chunk failed verification",
				"key", chunkPrefix+ref.Hash,
				"expected", ref.Hash,
				"got", got,
				"bytes", len(data))
			return nil, fmt.Errorf("chunk %s failed verification: bucket returned %d bytes hashing to %s: %w", ref.Hash, len(data), got, vfs.ErrIO)
		}
	}
	f.cache.put(ref.Hash, data)
	f.markKnown(ref.Hash)
	return data, nil
}

func (f *FS) markKnown(hash string) {
	f.knownMu.Lock()
	f.knownChunks[hash] = struct{}{}
	f.knownMu.Unlock()
}

func (f *FS) isKnown(hash string) bool {
	f.knownMu.Lock()
	defer f.knownMu.Unlock()
	_, ok := f.knownChunks[hash]
	return ok
}

// putChunk stores a chunk under its content hash, skipping the upload when the
// bucket already holds that content.
//
// The cache stores its own copy of what is uploaded (ADR 0004 §1), so data
// stays the caller's. That matters because a flush that fails later leaves
// data in of.dirty, where the next write changes it in place.
func (f *FS) putChunk(ctx context.Context, data []byte) (chunkRef, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	ref := chunkRef{Hash: hash, Size: uint32(len(data))}

	if f.isKnown(hash) {
		return ref, nil
	}
	key := chunkPrefix + hash
	// A HEAD before the PUT turns a rewrite of identical content into one cheap
	// round trip instead of re-uploading the bytes. Because the key is the hash
	// of the contents, an object that exists is necessarily the right one.
	if _, err := f.store.Head(ctx, key); err == nil {
		f.markKnown(hash)
		return ref, nil
	} else if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrUnsupported) {
		f.log.Debug("chunk existence probe failed, uploading anyway", "hash", hash, "err", err)
	}

	if err := f.store.Put(ctx, key, data); err != nil {
		if errors.Is(err, store.ErrUnsupported) {
			return chunkRef{}, vfs.ErrROFS
		}
		return chunkRef{}, fmt.Errorf("write chunk %s: %w", hash, err)
	}
	f.markKnown(hash)
	f.cache.put(hash, data)
	return ref, nil
}

// Read returns the bytes the file holds at one instant between the call and its
// return, its view, clamped to the size at that instant, with eof decided by
// that size (ADR 0005 §1). It takes the view in three steps (§2):
//
//  1. Holding mu for reading, it resolves the handle and applies its checks. A
//     file with no FS.open entry has no buffered state and no pending trims
//     (§3), so the size and the references in range, taken in this same hold,
//     are the whole view.
//  2. Otherwise, holding the entry's openFile.mu, and mu for reading inside it,
//     it checks again and answers from there, then takes the size and the
//     references of the indices in range that are not dirty, each with the
//     length of its index's pending trim, if any (ADR 0006 §6). It releases mu
//     and copies the dirty bytes in range into the reply before releasing
//     openFile.mu, since writes change those buffers in place.
//  3. Holding no lock, it fetches the references, and copies from each only
//     the bytes below its trim's length. Each names bytes that cannot change
//     (§5), so a slow bucket holds up no write, flush or namespace operation.
//
// Each lock is held for work proportional to the range, not the file (§4).
// Read never calls getOpen, so it adds no FS.open entry, and it never touches
// the budget.
func (f *FS) Read(ctx context.Context, c vfs.Caller, h vfs.Handle, off uint64, count uint32) ([]byte, bool, error) {
	f.mu.RLock()
	n, err := f.readable(c, h)
	if err != nil {
		f.mu.RUnlock()
		return nil, false, err
	}
	of := f.open[n.ID]
	var (
		size, end uint64
		out       []byte
		refs      []readRef
	)
	if of == nil {
		size = n.Size
		if off < size {
			end = readEnd(off, size, count)
			refs = f.readRefs(n, nil, nil, off, end)
		}
		f.mu.RUnlock()
	} else {
		f.mu.RUnlock()
		size, end, out, refs, err = f.readBuffered(c, h, of, off, count)
		if err != nil {
			return nil, false, err
		}
	}
	if off >= size {
		return nil, true, nil
	}
	if of == nil {
		out = make([]byte, end-off)
	}

	// Holes, indices past the end of the chunk list, bytes past the end of a
	// chunk and bytes at or past a pending trim's length were never recorded or
	// copied, and read as the zeros out starts with (ADR 0006 §6). A loadChunk
	// result may be the cache's own slice, so it is only resliced and copied
	// from (ADR 0004 §3).
	for _, r := range refs {
		data, err := f.loadChunk(ctx, r.ref)
		if err != nil {
			return nil, false, err
		}
		if r.trimmed && uint64(r.trim) < uint64(len(data)) {
			data = data[:r.trim]
		}
		within, dst := f.readSpan(out, off, end, r.idx)
		if within < uint64(len(data)) {
			copy(dst, data[within:])
		}
	}

	// Reading updates atime, but doing so would dirty the namespace on every
	// read and force a commit; the cost is not worth it for a PoC, so atime is
	// left to writes. This matches a Linux "relatime"-style tradeoff.
	return out, end >= size, nil
}

// readRef is a chunk reference a Read's view names, with the index it is for
// and, when trimmed is set, the length of that index's pending trim, below
// which alone the chunk's bytes are part of the view (ADR 0006 §6).
type readRef struct {
	idx     uint64
	ref     chunkRef
	trim    uint32
	trimmed bool
}

// readable resolves h for a Read and applies its checks, in the order Read has
// always applied them. Caller must hold mu.
func (f *FS) readable(c vfs.Caller, h vfs.Handle) (*inode, error) {
	n, err := f.resolve(h)
	if err != nil {
		return nil, err
	}
	if n.isDir() {
		return nil, vfs.ErrIsDir
	}
	if !permitted(n, c, 4) {
		return nil, vfs.ErrAcces
	}
	return n, nil
}

// readBuffered is step 2 of Read (ADR 0005 §2), for a file with an FS.open
// entry. It returns the size, the end of the range, the reply with the dirty
// bytes in range already in it, and the references still to fetch, each with
// its index's pending trim (ADR 0006 §6); for off >= size it returns only the
// size. It releases of.mu by defer, as bufferWrite does, so that a panic
// cannot leave the lock held.
//
// Nothing checks that FS.open still holds of: a successful resolve here means
// it does, because an entry leaves FS.open only with its inode (ADR 0005 §3,
// invariant 2; Assumption 6).
func (f *FS) readBuffered(c vfs.Caller, h vfs.Handle, of *openFile, off uint64, count uint32) (size, end uint64, out []byte, refs []readRef, err error) {
	of.mu.Lock()
	defer of.mu.Unlock()

	// The answer comes from this hold, not step 1's: the handle may have gone,
	// or the mode changed, while no lock was held.
	f.mu.RLock()
	n, err := f.readable(c, h)
	if err != nil {
		f.mu.RUnlock()
		return 0, 0, nil, nil, err
	}
	size = n.Size
	if off >= size {
		f.mu.RUnlock()
		return size, 0, nil, nil, nil
	}
	end = readEnd(off, size, count)
	refs = f.readRefs(n, of.dirty, of.pendingTrim, off, end)
	// The copy below needs only of.mu, under which of.dirty changes (ADR 0005
	// §3, invariant 1), and releasing mu first keeps the namespace lock's hold
	// independent of how many bytes it copies.
	f.mu.RUnlock()

	out = make([]byte, end-off)
	if end > off {
		cs := uint64(f.chunkSize)
		for idx, last := off/cs, (end-1)/cs; idx <= last; idx++ {
			buf, ok := of.dirty[idx]
			if !ok {
				continue
			}
			within, dst := f.readSpan(out, off, end, idx)
			if within < uint64(len(buf)) {
				copy(dst, buf[within:])
			}
		}
	}
	return size, end, out, refs, nil
}

// readEnd returns the end of a Read's range clamped to size, as
// off + min(count, size-off), so that an offset near 2^64 cannot wrap it
// (ADR 0005 §2). It needs off < size.
func readEnd(off, size uint64, count uint32) uint64 {
	if rem := size - off; uint64(count) < rem {
		return off + uint64(count)
	}
	return size
}

// readRefs returns the references of the chunk indices that [off, end)
// touches, leaving out indices present in dirty, holes, and indices past the
// end of the chunk list, none of which a Read fetches. Each carries the length
// of the pending trim in trims at its index, if any, and a reference whose part
// of the range lies wholly at or past that length is left out too, since it
// would contribute only zeros (ADR 0006 §6). It visits only those indices,
// looking trims up by index, never walking the list or the map (ADR 0005 §4).
// Caller must hold mu, and, when it passes dirty and trims, the openFile.mu
// that guards them.
func (f *FS) readRefs(n *inode, dirty map[uint64][]byte, trims map[uint64]trimReq, off, end uint64) []readRef {
	if end <= off {
		return nil
	}
	var refs []readRef
	cs := uint64(f.chunkSize)
	for idx, last := off/cs, (end-1)/cs; idx <= last && idx < uint64(len(n.Chunks)); idx++ {
		if _, ok := dirty[idx]; ok || n.Chunks[idx].isHole() {
			continue
		}
		r := readRef{idx: idx, ref: n.Chunks[idx]}
		if t, ok := trims[idx]; ok {
			within := uint64(0)
			if base := idx * cs; off > base {
				within = off - base
			}
			if within >= uint64(t.to) {
				continue
			}
			r.trim, r.trimmed = t.to, true
		}
		refs = append(refs, r)
	}
	return refs
}

// readSpan returns where chunk index idx meets a Read's range [off, end): the
// offset into the chunk, and the part of out, which holds the range from off,
// that the chunk fills. idx must be one the range touches. The chunk's end,
// base + cs, is computed only when it lies below end, so it cannot wrap.
func (f *FS) readSpan(out []byte, off, end, idx uint64) (within uint64, dst []byte) {
	cs := uint64(f.chunkSize)
	base := idx * cs
	lo, hi := base, end
	if lo < off {
		lo = off
	}
	if end-base > cs {
		hi = base + cs
	}
	return lo - base, out[lo-off : hi-off]
}

func (f *FS) Write(ctx context.Context, c vfs.Caller, h vfs.Handle, off uint64, data []byte, how vfs.Stability) (uint32, vfs.Stability, error) {
	if err := f.mutable(); err != nil {
		return 0, 0, err
	}
	if len(data) == 0 {
		return 0, vfs.FileSync, nil
	}

	f.mu.RLock()
	n, err := f.resolve(h)
	if err != nil {
		f.mu.RUnlock()
		return 0, 0, err
	}
	if n.isDir() {
		f.mu.RUnlock()
		return 0, 0, vfs.ErrIsDir
	}
	if !permitted(n, c, 2) {
		f.mu.RUnlock()
		return 0, 0, vfs.ErrAcces
	}
	id := n.ID
	f.mu.RUnlock()

	// Hold the write back while buffered data is over budget (ADR 0003 §4).
	// The wait comes here, after the checks that can refuse the write and
	// before getOpen, holding no lock at all: the drain it may run is Sync,
	// which takes FS.mu and every openFile.mu. A failed wait therefore leaves
	// nothing behind, not even an openFile, and its error goes back as it is.
	if err := f.budget.await(ctx, f.Sync); err != nil {
		return 0, 0, err
	}

	// bufferWrite has released of.mu by the time it returns, which the sync
	// below depends on: flushOpen takes of.mu itself, and sync.Mutex is not
	// reentrant. Another write may slip in before the flush and be made
	// durable along with this one; that costs nothing, since the promise is
	// only that this write's bytes are stable before the reply.
	written, err := f.bufferWrite(ctx, h, f.getOpen(id), off, data)
	if err != nil {
		return 0, 0, err
	}

	// Report honestly what was made durable. Buffered data is not stable, so
	// unless the client asked for a stable write and we committed, we answer
	// UNSTABLE and let the client send COMMIT. DATA_SYNC gets the full sync:
	// the chunk list that makes the data retrievable lives in the namespace,
	// so there is no cheaper DATA_SYNC to offer, and FILE_SYNC is a permitted
	// answer to it.
	if how == vfs.FileSync || how == vfs.DataSync {
		if err := f.syncFileAndNamespace(ctx, id); err != nil {
			return uint32(written), vfs.Unstable, err
		}
		return uint32(written), vfs.FileSync, nil
	}
	return uint32(written), vfs.Unstable, nil
}

// bufferWrite copies data into the file's dirty chunks and extends the file to
// cover it, returning how many bytes it took. An index it fills that has a
// pending trim starts from the chunk's first to bytes only, and loses its
// trim when the buffer is stored (ADR 0006 §4). It holds of.mu throughout,
// fetches included, and releases it by defer, so that a panic underneath (a
// store fetch, the cache) cannot leave the lock held: every later flush, and
// so every Sync, would hang behind it.
func (f *FS) bufferWrite(ctx context.Context, h vfs.Handle, of *openFile, off uint64, data []byte) (int, error) {
	of.mu.Lock()
	defer of.mu.Unlock()

	// The chunk list must be read under of.mu, not before it. flushOpen
	// repoints n.Chunks and empties of.dirty while holding of.mu, so a list
	// copied before taking it can predate a flush that has already consumed
	// the dirty chunk, and rebuilding from it would silently drop bytes an
	// earlier write was acknowledged for. A write that waited for the budget
	// has just run such a flush itself (ADR 0003 §4).
	var refs []chunkRef
	f.mu.RLock()
	n, err := f.resolve(h)
	gone := err != nil
	if !gone {
		refs = append([]chunkRef(nil), n.Chunks...)
	}
	f.mu.RUnlock()

	cs := uint64(f.chunkSize)
	written := 0
	if gone {
		// Unlinked since Write's checks. Like the size update below, treat
		// the write as having landed just before the unlink: it succeeds, and
		// its bytes go wherever the rest of the file went. Buffering them
		// would only upload chunks nothing references.
		written = len(data)
	}
	// charge is the capacity this call adds to of.dirty, as the difference of
	// cap before and after each index, which stays exact whatever capacity a
	// buffer has: an index new to of.dirty adds its buffer's, and an index
	// already dirty at full capacity adds nothing (ADR 0003 §2, site 1).
	// Indices only increase, so none counts twice.
	var charge int64
	for written < len(data) {
		pos := off + uint64(written)
		idx := pos / cs
		within := int(pos % cs)

		chunk, ok := of.dirty[idx]
		oldCap := cap(chunk) // zero for an absent index
		if !ok {
			// Load the existing chunk only when this write does not cover it
			// completely; a full-chunk overwrite needs no read.
			fullOverwrite := within == 0 && len(data)-written >= int(cs)
			if fullOverwrite || idx >= uint64(len(refs)) {
				chunk = make([]byte, 0, cs)
			} else {
				loaded, err := f.loadChunk(ctx, refs[idx])
				if err != nil {
					// The index stays as it was, pending trim included
					// (ADR 0006 §4).
					if written > 0 {
						break // report the partial write rather than losing it
					}
					return 0, err
				}
				// A pending trim means only the chunk's first to bytes are
				// the file's; the rest must read as zeros unless this write
				// puts something there (ADR 0006 §1, §4). Cut by reslicing
				// and copy out: loadChunk may have returned the cache's own
				// slice, which must not be written or appended to (ADR 0004
				// §3).
				if t, ok := of.pendingTrim[idx]; ok && uint64(t.to) < uint64(len(loaded)) {
					loaded = loaded[:t.to]
				}
				chunk = append(make([]byte, 0, cs), loaded...)
			}
		}

		nw := int(cs) - within
		if nw > len(data)-written {
			nw = len(data) - written
		}
		if need := within + nw; need > len(chunk) {
			chunk = append(chunk, make([]byte, need-len(chunk))...)
		}
		copy(chunk[within:within+nw], data[written:written+nw])
		of.dirty[idx] = chunk
		// The buffer now holds everything the index is, so its trim, applied
		// above or covered by a whole-chunk write, goes in the same hold of
		// of.mu, keeping an index out of both maps at once (ADR 0006 §2 I1,
		// §4). delete on a nil map is a no-op.
		delete(of.pendingTrim, idx)
		charge += int64(cap(chunk) - oldCap)
		written += nw
	}
	// Charged once, including after a partial write's break, and before the
	// check below: charged after it, a write racing an unlink would land its
	// charge on buffers that check had already discarded, and leak it.
	if charge > 0 {
		f.budget.add(charge)
		of.bytes.Add(charge)
	}

	now := time.Now()
	f.mu.Lock()
	if n2, err := f.resolve(h); err == nil {
		if endOff := off + uint64(written); endOff > n2.Size {
			n2.Size = endOff
		}
		n2.touchM(now)
		n2.touchA(now)
	} else {
		// Unlinked while this write buffered: nothing will flush these
		// buffers, so drop them and return the file's whole charge, this
		// write's included (ADR 0003 §2, site 2).
		released := of.bytes.Swap(0)
		of.dirty = make(map[uint64][]byte)
		f.budget.release(released)
	}
	f.dirty = true
	f.mu.Unlock()
	return written, nil
}

// syncFileAndNamespace makes one file's data and the namespace durable, which
// is what a client asking for FILE_SYNC is entitled to assume happened.
func (f *FS) syncFileAndNamespace(ctx context.Context, id uint64) error {
	if err := f.commitFile(ctx, id); err != nil {
		return err
	}
	return f.Sync(ctx)
}

// truncate changes a file's length, dropping or extending chunks as needed.
// It holds of.mu and then mu for writing across the whole change, and so never
// fetches: a cut into a stored chunk it cannot shorten becomes a pending trim
// (ADR 0006 §3). It makes no store call, charges nothing and never waits.
func (f *FS) truncate(ctx context.Context, c vfs.Caller, h vfs.Handle, size uint64) error {
	f.mu.RLock()
	n, err := f.resolve(h)
	if err != nil {
		f.mu.RUnlock()
		return err
	}
	if n.isDir() {
		f.mu.RUnlock()
		return vfs.ErrIsDir
	}
	if !permitted(n, c, 2) {
		f.mu.RUnlock()
		return vfs.ErrAcces
	}
	id := n.ID
	f.mu.RUnlock()

	of := f.getOpen(id)
	of.mu.Lock()
	defer of.mu.Unlock()

	cs := uint64(f.chunkSize)
	lastIdx := size / cs
	tail := size % cs

	f.mu.Lock()
	defer f.mu.Unlock()
	n, err = f.resolve(h)
	if err != nil {
		return err
	}

	// Drop whole chunks past the new end, and their pending trims by the same
	// rule (ADR 0006 §3(a)). The capacity freed is released at once, because
	// only release wakes parked writers. Nothing here waits: both locks are
	// held (ADR 0003 §2, site 3).
	var freed int64
	for idx, chunk := range of.dirty {
		if idx > lastIdx || (idx == lastIdx && tail == 0) {
			freed += int64(cap(chunk))
			delete(of.dirty, idx)
		}
	}
	if freed > 0 {
		of.bytes.Add(-freed)
		f.budget.release(freed)
	}
	for idx := range of.pendingTrim {
		if idx > lastIdx || (idx == lastIdx && tail == 0) {
			delete(of.pendingTrim, idx)
		}
	}
	// Shortening the tail in place is a reslice: the backing array stays
	// resident at the same capacity, so the charge does not change.
	if tail != 0 {
		if chunk, ok := of.dirty[lastIdx]; ok && uint64(len(chunk)) > tail {
			of.dirty[lastIdx] = chunk[:tail]
		}
	}

	if uint64(len(n.Chunks)) > lastIdx {
		keep := lastIdx
		if tail != 0 {
			keep = lastIdx + 1
		}
		if uint64(len(n.Chunks)) > keep {
			n.Chunks = n.Chunks[:keep]
		}
	}
	// A new end inside a stored chunk that is not dirty cuts that chunk, which
	// cannot be fetched under mu: record the cut as a pending trim for the
	// next flush to apply, and for Read and bufferWrite to honour until then
	// (ADR 0006 §1, §3(b)). Only the smallest length is kept, so growing the
	// file within the chunk leaves the bytes an earlier truncate removed
	// reading as zeros. A hole takes no trim, since it reads as zeros past any
	// length (§3(c)). Nothing is read from the cache, staged in of.dirty or
	// charged (§3(d)).
	if _, dirty := of.dirty[lastIdx]; tail != 0 && !dirty && uint64(len(n.Chunks)) == lastIdx+1 {
		if ref := n.Chunks[lastIdx]; !ref.isHole() {
			v := uint64(ref.Size)
			if t, ok := of.pendingTrim[lastIdx]; ok {
				v = uint64(t.to)
			}
			if tail < v {
				if of.pendingTrim == nil {
					of.pendingTrim = make(map[uint64]trimReq)
				}
				of.pendingTrim[lastIdx] = trimReq{to: uint32(tail), ref: ref}
			}
		}
	}

	if size > n.Size {
		// Growing a file creates a hole; no data is stored for it.
		for uint64(len(n.Chunks)) < lastIdx {
			n.Chunks = append(n.Chunks, chunkRef{Size: uint32(cs)})
		}
	}
	n.Size = size
	n.touchM(time.Now())
	f.dirty = true
	return nil
}

// trimReq is a pending trim: its index holds the first to bytes of the chunk
// ref names, then zeros (ADR 0006 §1, §2).
type trimReq struct {
	to  uint32
	ref chunkRef
}

// commitFile uploads one inode's buffered chunks and updates its chunk list.
func (f *FS) commitFile(ctx context.Context, id uint64) error {
	f.mu.RLock()
	of := f.open[id]
	f.mu.RUnlock()
	if of == nil {
		return nil
	}
	return f.flushOpen(ctx, id, of)
}

// flushOpen uploads buffered chunks for one inode, and applies its pending
// trims. It takes the per-file lock and then briefly the namespace lock, never
// the other way around, and holds the per-file lock throughout, so nothing
// changes of.dirty or of.pendingTrim underneath it.
func (f *FS) flushOpen(ctx context.Context, id uint64, of *openFile) error {
	of.mu.Lock()
	defer of.mu.Unlock()

	if len(of.dirty) == 0 && len(of.pendingTrim) == 0 {
		return nil
	}

	// Upload first, update the namespace second. If a fetch or an upload
	// fails, the namespace still describes the previous, intact contents, and
	// of.dirty and of.pendingTrim stay as they were for the next flush to
	// retry; a shortened chunk already uploaded is then found known (ADR 0006
	// §5).
	//
	// Each trim is applied one at a time through a transient copy that never
	// enters of.dirty and is never charged, so a flush adds nothing to the
	// budget (ADR 0006 §5, §7; ADR 0003 §2, site 4).
	trimmed := make(map[uint64]chunkRef, len(of.pendingTrim))
	for idx, t := range of.pendingTrim {
		ref, err := f.applyTrim(ctx, t)
		if err != nil {
			f.dropIfGone(id, of)
			return err
		}
		trimmed[idx] = ref
	}

	idxs := make([]uint64, 0, len(of.dirty))
	for idx := range of.dirty {
		idxs = append(idxs, idx)
	}
	sort.Slice(idxs, func(i, j int) bool { return idxs[i] < idxs[j] })

	uploaded := make(map[uint64]chunkRef, len(idxs))
	for _, idx := range idxs {
		ref, err := f.putChunk(ctx, of.dirty[idx])
		if err != nil {
			f.dropIfGone(id, of)
			return err
		}
		uploaded[idx] = ref
	}

	f.mu.Lock()
	n := f.inodes[id]
	if n != nil {
		// A trim's index is in the chunk list while its entry exists (ADR 0006
		// §2, I2), so the check never fails; it keeps a broken invariant from
		// putting a chunk back past the end of the file.
		for idx, ref := range trimmed {
			if idx < uint64(len(n.Chunks)) {
				n.Chunks[idx] = ref
			}
		}
		for _, idx := range idxs {
			for uint64(len(n.Chunks)) <= idx {
				// Fill any gap with holes so indices stay aligned.
				n.Chunks = append(n.Chunks, chunkRef{Size: f.chunkSize})
			}
			n.Chunks[idx] = uploaded[idx]
		}
		f.dirty = true
	}
	f.mu.Unlock()

	of.dirty = make(map[uint64][]byte)
	of.pendingTrim = nil
	f.budget.release(of.bytes.Swap(0))
	return nil
}

// applyTrim uploads the first t.to bytes of the chunk t.ref names, and returns
// the reference to them (ADR 0006 §5). It copies into a buffer of its own
// rather than uploading a reslice, because loadChunk may return the cache's
// own slice and store.Store does not promise that Put leaves its argument
// alone (ADR 0004 §1, §3). Nothing of the copy outlives the call. Caller must
// not hold mu.
func (f *FS) applyTrim(ctx context.Context, t trimReq) (chunkRef, error) {
	data, err := f.loadChunk(ctx, t.ref)
	if err != nil {
		return chunkRef{}, err
	}
	n := len(data)
	if uint64(t.to) < uint64(n) {
		n = int(t.to)
	}
	buf := make([]byte, n)
	copy(buf, data)
	return f.putChunk(ctx, buf)
}

// dropIfGone is flushOpen's cleanup on a failed return, called holding of.mu.
// If the inode has gone, nothing will flush this file again. A flush charges
// nothing (ADR 0006 §5), and what a write charges to a removed file it
// releases in the same hold of of.mu, so this finds nothing charged in
// practice; the release is kept so the accounting stays exact if a flush ever
// charges again (ADR 0006 Assumption 9). Checking under mu orders this against
// dropOpen. Resetting of.dirty and pendingTrim keeps another flush of the same
// openFile from uploading them again; pendingTrim is reset to nil, which
// truncate allocates again before inserting. A file whose inode is still there
// keeps everything: its buffers are really held, and its next flush retries
// them and its trims (ADR 0003 §2, site 4; Assumption 18).
func (f *FS) dropIfGone(id uint64, of *openFile) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.inodes[id] != nil {
		return
	}
	released := of.bytes.Swap(0)
	of.dirty = make(map[uint64][]byte)
	of.pendingTrim = nil
	f.budget.release(released)
}

// flushAll uploads every inode's buffered data. It must be called without mu.
func (f *FS) flushAll(ctx context.Context) error {
	f.mu.RLock()
	ids := make([]uint64, 0, len(f.open))
	files := make([]*openFile, 0, len(f.open))
	for id, of := range f.open {
		ids = append(ids, id)
		files = append(files, of)
	}
	f.mu.RUnlock()

	for i, of := range files {
		if err := f.flushOpen(ctx, ids[i], of); err != nil {
			return err
		}
	}
	return nil
}

func (f *FS) Commit(ctx context.Context, h vfs.Handle, off uint64, count uint32) error {
	f.mu.RLock()
	n, err := f.resolve(h)
	if err != nil {
		f.mu.RUnlock()
		return err
	}
	id := n.ID
	f.mu.RUnlock()

	if err := f.commitFile(ctx, id); err != nil {
		return err
	}
	// COMMIT promises the data is on stable storage. For this filesystem that
	// means both the chunks and the namespace that names them are stored, so
	// a full commit is required.
	return f.Sync(ctx)
}
