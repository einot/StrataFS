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

	// SnapshotRetention is how many superseded namespace snapshots to keep.
	// Zero selects the default; a negative value disables pruning entirely.
	SnapshotRetention int

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

	// mu guards the namespace: the inode table, the allocator, the epoch and
	// the dirty flags.
	//
	// Lock ordering is openFile.mu before mu. Nothing may acquire an
	// openFile.mu while holding mu, which is why committing flushes file data
	// before taking mu rather than underneath it.
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

	// open holds buffered writes per inode.
	open map[uint64]*openFile

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
	// pendingTrim holds truncates that still need the original chunk fetched
	// before it can be shortened; the fetch cannot happen under the namespace
	// lock, so it is deferred to the flush.
	pendingTrim []trimReq
}

const defaultChunkSize = 1 << 20

// defaultSnapshotRetention keeps a short rollback window without letting the
// bucket grow without bound.
const defaultSnapshotRetention = 10

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
		retention:      cfg.SnapshotRetention,
		open:           make(map[uint64]*openFile),
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
		delete(f.open, id)
	}
	f.dirty = true
	return nil
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
				delete(f.open, victimID)
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

func (f *FS) Read(ctx context.Context, c vfs.Caller, h vfs.Handle, off uint64, count uint32) ([]byte, bool, error) {
	// Take a consistent view of the chunk list, then release the namespace lock
	// before any network I/O.
	f.mu.RLock()
	n, err := f.resolve(h)
	if err != nil {
		f.mu.RUnlock()
		return nil, false, err
	}
	if n.isDir() {
		f.mu.RUnlock()
		return nil, false, vfs.ErrIsDir
	}
	if !permitted(n, c, 4) {
		f.mu.RUnlock()
		return nil, false, vfs.ErrAcces
	}
	size := n.Size
	id := n.ID
	refs := append([]chunkRef(nil), n.Chunks...)
	of := f.open[id]
	f.mu.RUnlock()

	if off >= size {
		return nil, true, nil
	}
	end := off + uint64(count)
	if end > size {
		end = size
	}
	out := make([]byte, 0, end-off)

	// Copy any buffered chunks so the per-file lock is not held across reads.
	var staged map[uint64][]byte
	if of != nil {
		of.mu.Lock()
		if len(of.dirty) > 0 {
			staged = make(map[uint64][]byte, len(of.dirty))
			for k, v := range of.dirty {
				staged[k] = v
			}
		}
		of.mu.Unlock()
	}

	cs := uint64(f.chunkSize)
	for pos := off; pos < end; {
		idx := pos / cs
		within := pos % cs

		var chunk []byte
		if d, ok := staged[idx]; ok {
			chunk = d
		} else if idx < uint64(len(refs)) {
			chunk, err = f.loadChunk(ctx, refs[idx])
			if err != nil {
				return nil, false, err
			}
		}

		// Bytes past the end of a stored chunk but inside the file's size are
		// holes, which read as zeros.
		avail := cs - within
		if remaining := end - pos; remaining < avail {
			avail = remaining
		}
		piece := make([]byte, avail)
		if within < uint64(len(chunk)) {
			copy(piece, chunk[within:])
		}
		out = append(out, piece...)
		pos += avail
	}

	// Reading updates atime, but doing so would dirty the namespace on every
	// read and force a commit; the cost is not worth it for a PoC, so atime is
	// left to writes. This matches a Linux "relatime"-style tradeoff.
	return out, end >= size, nil
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
	refs := append([]chunkRef(nil), n.Chunks...)
	f.mu.RUnlock()

	of := f.getOpen(id)
	of.mu.Lock()
	defer of.mu.Unlock()

	cs := uint64(f.chunkSize)
	written := 0
	for written < len(data) {
		pos := off + uint64(written)
		idx := pos / cs
		within := int(pos % cs)

		chunk, ok := of.dirty[idx]
		if !ok {
			// Load the existing chunk only when this write does not cover it
			// completely; a full-chunk overwrite needs no read.
			fullOverwrite := within == 0 && len(data)-written >= int(cs)
			if fullOverwrite || idx >= uint64(len(refs)) {
				chunk = make([]byte, 0, cs)
			} else {
				loaded, err := f.loadChunk(ctx, refs[idx])
				if err != nil {
					if written > 0 {
						break // report the partial write rather than losing it
					}
					return 0, 0, err
				}
				// Copy: the cache hands out shared slices that must not be
				// mutated in place.
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
		written += nw
	}

	now := time.Now()
	f.mu.Lock()
	if n2, err := f.resolve(h); err == nil {
		if endOff := off + uint64(written); endOff > n2.Size {
			n2.Size = endOff
		}
		n2.touchM(now)
		n2.touchA(now)
	}
	f.dirty = true
	f.mu.Unlock()

	// Report honestly what was made durable. Buffered data is not stable, so
	// unless the client asked for FILE_SYNC and we committed, we answer
	// UNSTABLE and let the client send COMMIT.
	if how == vfs.FileSync {
		if err := f.syncFileAndNamespace(ctx, id); err != nil {
			return uint32(written), vfs.Unstable, err
		}
		return uint32(written), vfs.FileSync, nil
	}
	return uint32(written), vfs.Unstable, nil
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

	// Drop whole chunks past the new end.
	for idx := range of.dirty {
		if idx > lastIdx || (idx == lastIdx && tail == 0) {
			delete(of.dirty, idx)
		}
	}
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
	// Truncating into the middle of a stored chunk needs that chunk rewritten;
	// stage it so the flush re-uploads a shortened copy.
	if tail != 0 && uint64(len(n.Chunks)) == lastIdx+1 {
		if _, staged := of.dirty[lastIdx]; !staged {
			ref := n.Chunks[lastIdx]
			if uint64(ref.Size) > tail {
				// Load outside the lock is not possible here; fetch from cache
				// if we can, otherwise mark the chunk for the flush to shorten.
				if data, ok := f.cache.get(ref.Hash); ok {
					of.dirty[lastIdx] = append([]byte(nil), data[:tail]...)
				} else {
					of.pendingTrim = append(of.pendingTrim, trimReq{idx: lastIdx, to: uint32(tail), ref: ref})
				}
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

// trimReq records a truncate that still needs the original chunk fetched.
type trimReq struct {
	idx uint64
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

// flushOpen uploads buffered chunks for one inode. It takes the per-file lock
// and then briefly the namespace lock, never the other way around.
func (f *FS) flushOpen(ctx context.Context, id uint64, of *openFile) error {
	of.mu.Lock()
	defer of.mu.Unlock()

	// Resolve any truncates that needed the original chunk contents.
	for _, t := range of.pendingTrim {
		if _, staged := of.dirty[t.idx]; staged {
			continue
		}
		data, err := f.loadChunk(ctx, t.ref)
		if err != nil {
			return err
		}
		if uint32(len(data)) > t.to {
			data = data[:t.to]
		}
		of.dirty[t.idx] = append([]byte(nil), data...)
	}
	of.pendingTrim = nil

	if len(of.dirty) == 0 {
		return nil
	}

	idxs := make([]uint64, 0, len(of.dirty))
	for idx := range of.dirty {
		idxs = append(idxs, idx)
	}
	sort.Slice(idxs, func(i, j int) bool { return idxs[i] < idxs[j] })

	// Upload first, update the namespace second. If an upload fails the
	// namespace still describes the previous, intact contents.
	uploaded := make(map[uint64]chunkRef, len(idxs))
	for _, idx := range idxs {
		ref, err := f.putChunk(ctx, of.dirty[idx])
		if err != nil {
			return err
		}
		uploaded[idx] = ref
	}

	f.mu.Lock()
	n := f.inodes[id]
	if n != nil {
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
	return nil
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
