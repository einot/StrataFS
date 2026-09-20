package blobfs

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"

	"strata/internal/store"
)

// loadOrInit reads the current namespace from the bucket, creating an
// empty filesystem if the bucket has no root pointer yet.
func (f *FS) loadOrInit(ctx context.Context) error {
	raw, err := f.store.Get(ctx, rootKey)
	if errors.Is(err, store.ErrNotFound) {
		return f.initEmpty(ctx)
	}
	if err != nil {
		return fmt.Errorf("read root pointer: %w", err)
	}

	var rp rootPointer
	if err := json.Unmarshal(raw, &rp); err != nil {
		return fmt.Errorf("parse root pointer: %w", err)
	}
	if rp.Version != snapshotVersion {
		return fmt.Errorf("bucket holds format version %d, this build speaks %d", rp.Version, snapshotVersion)
	}

	// Record the ETag we loaded from; the next commit compare-and-swaps against
	// it, so a second mount writing concurrently is detected rather than
	// silently clobbering us.
	info, err := f.store.Head(ctx, rootKey)
	if err != nil {
		return fmt.Errorf("stat root pointer: %w", err)
	}

	snapRaw, err := f.store.Get(ctx, rp.Snapshot)
	if err != nil {
		return fmt.Errorf("read snapshot %s: %w", rp.Snapshot, err)
	}
	snap, err := decodeSnapshot(snapRaw)
	if err != nil {
		return fmt.Errorf("decode snapshot %s: %w", rp.Snapshot, err)
	}
	if snap.ChunkSize != f.chunkSize {
		// The chunk size is baked into how offsets map to chunks, so an
		// existing filesystem's value wins over the flag.
		f.log.Info("adopting chunk size from existing filesystem",
			"existing", snap.ChunkSize, "requested", f.chunkSize)
		f.chunkSize = snap.ChunkSize
	}

	f.inodes = snap.Inodes
	f.nextIno = snap.NextIno
	f.epoch = snap.Epoch
	f.fsid = rp.FSID
	f.rootETag = info.ETag

	if _, ok := f.inodes[rootInodeID]; !ok {
		return errors.New("snapshot has no root inode")
	}
	f.log.Info("mounted existing filesystem",
		"epoch", f.epoch, "inodes", len(f.inodes), "chunk_size", f.chunkSize)
	return nil
}

// initEmpty creates a brand-new filesystem: a single root directory, committed
// with If-None-Match so that two processes racing to initialize the same bucket
// cannot both win.
func (f *FS) initEmpty(ctx context.Context) error {
	now := time.Now()
	root := &inode{
		ID:      rootInodeID,
		Gen:     1,
		Type:    2, // vfs.TypeDir
		Mode:    0o755,
		UID:     f.ownerUID,
		GID:     f.ownerGID,
		NLink:   2,
		Entries: map[string]uint64{},
		ATimeNS: now.UnixNano(),
		MTimeNS: now.UnixNano(),
		CTimeNS: now.UnixNano(),
	}
	f.inodes = map[uint64]*inode{rootInodeID: root}
	f.nextIno = rootInodeID + 1
	f.epoch = 0
	f.fsid = fsidFor(f.store.Name())
	f.rootETag = "" // If-None-Match: * on first commit

	f.log.Info("initializing new filesystem", "bucket", f.store.Name())
	return f.commitLocked(ctx)
}

// fsidFor derives a stable filesystem ID from the bucket name, so the
// same bucket presents the same fsid across mounts.
func fsidFor(name string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(name))
	return h.Sum64()
}

// Sync writes the current namespace to the bucket and atomically
// swaps the root pointer to it.
//
// The ordering matters and is the whole durability argument: every chunk a
// snapshot mentions is uploaded before the snapshot is written, and the
// snapshot is written before the root pointer moves. A crash at any point
// leaves unreferenced objects behind, never a root pointer that names data
// which does not exist. Readers only ever see the old tree or the new one.
func (f *FS) Sync(ctx context.Context) error {
	// Buffered file data must be stored before the namespace that
	// references it. This happens before mu is taken because flushing acquires
	// per-file locks, which must never be taken while holding mu.
	if err := f.flushAll(ctx); err != nil {
		return fmt.Errorf("flush pending writes: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirty {
		return nil
	}
	return f.commitLocked(ctx)
}

func (f *FS) commitLocked(ctx context.Context) error {
	if f.diverged {
		return errors.New("filesystem diverged: another writer committed to this bucket; remount to continue")
	}

	epoch := f.epoch + 1
	snap := &snapshot{
		Version:   snapshotVersion,
		Epoch:     epoch,
		NextIno:   f.nextIno,
		ChunkSize: f.chunkSize,
		Inodes:    f.inodes,
		CreatedNS: time.Now().UnixNano(),
	}
	body, err := encodeSnapshot(snap)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}

	// The snapshot key includes random bytes so that two writers racing to
	// commit the same epoch cannot overwrite each other's snapshot; only one
	// of them will win the root-pointer swap.
	var nonce [6]byte
	rand.Read(nonce[:])
	key := fmt.Sprintf("%s%020d-%s.json.gz", snapshotPrefix, epoch, hex.EncodeToString(nonce[:]))

	if err := f.store.Put(ctx, key, body); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	rp := rootPointer{
		Version:   snapshotVersion,
		Epoch:     epoch,
		Snapshot:  key,
		FSID:      f.fsid,
		WrittenNS: time.Now().UnixNano(),
		Writer:    f.writerID,
	}
	rpBody, err := json.Marshal(rp)
	if err != nil {
		return err
	}

	newETag, err := f.store.PutIfMatch(ctx, rootKey, rpBody, f.rootETag)
	switch {
	case errors.Is(err, store.ErrPrecondition):
		// Someone else moved the root pointer. Our in-memory tree is built on a
		// namespace that is no longer current, so continuing would discard
		// their work. Refuse to write any further rather than corrupt.
		f.diverged = true
		f.log.Error("commit conflict: another writer owns this bucket",
			"our_epoch", f.epoch, "attempted", epoch)
		return errors.New("commit conflict: another writer owns this bucket; remount to continue")
	case errors.Is(err, store.ErrUnsupported):
		// A backend without conditional writes cannot give us safe single-writer
		// detection. Fall back to an unconditional write and say so clearly.
		f.log.Warn("backend lacks conditional writes; concurrent mounts are unsafe")
		if err := f.store.Put(ctx, rootKey, rpBody); err != nil {
			return fmt.Errorf("write root pointer: %w", err)
		}
		if info, herr := f.store.Head(ctx, rootKey); herr == nil {
			newETag = info.ETag
		}
	case err != nil:
		return fmt.Errorf("swap root pointer: %w", err)
	}

	// The commit is now durable. Only after the swap succeeds do we advance our
	// own notion of the current epoch.
	f.epoch = epoch
	f.rootETag = newETag
	f.dirty = false
	f.lastCommit = time.Now()
	f.log.Debug("committed", "epoch", epoch, "inodes", len(f.inodes), "snapshot", key)

	// Each commit writes a complete namespace snapshot, so without pruning the
	// bucket grows without bound -- a busy mount can produce hundreds
	// of snapshots in a minute. Older ones are kept only as a short rollback
	// window, and reclaiming them happens off the commit path so a slow bucket
	// never stalls a write.
	f.schedulePrune(epoch)
	return nil
}

// schedulePrune starts a background pass that deletes superseded snapshots,
// unless one is already running. Caller must hold mu.
func (f *FS) schedulePrune(current uint64) {
	if f.retention == 0 || f.pruning {
		return
	}
	f.pruning = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := f.pruneSnapshots(ctx, current); err != nil {
			f.log.Warn("snapshot pruning failed", "err", err)
		}
		f.mu.Lock()
		f.pruning = false
		f.mu.Unlock()
	}()
}

// pruneSnapshots deletes snapshots older than the retention window.
//
// Snapshot keys are zero-padded with the epoch, so lexicographic order is
// epoch order and the newest entries are simply the last ones listed. The
// snapshot the root pointer currently names is never a candidate: it is inside
// the retention window by construction, and the check below is belt and braces.
func (f *FS) pruneSnapshots(ctx context.Context, current uint64) error {
	var all []store.ObjectInfo
	after := ""
	for {
		batch, err := f.store.List(ctx, snapshotPrefix, after, 1000)
		if err != nil {
			return fmt.Errorf("list snapshots: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		after = batch[len(batch)-1].Key
		if len(batch) < 1000 {
			break
		}
	}

	if len(all) <= f.retention {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
	doomed := all[:len(all)-f.retention]

	currentPrefix := fmt.Sprintf("%s%020d-", snapshotPrefix, current)
	deleted := 0
	for _, o := range doomed {
		if strings.HasPrefix(o.Key, currentPrefix) {
			continue // never delete the snapshot the root pointer names
		}
		if err := f.store.Delete(ctx, o.Key); err != nil {
			// A failed delete only leaves garbage behind; it is not a
			// correctness problem, so log and keep going.
			f.log.Debug("could not delete superseded snapshot", "key", o.Key, "err", err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		f.log.Debug("pruned superseded snapshots", "deleted", deleted, "kept", f.retention)
	}
	return nil
}

func encodeSnapshot(s *snapshot) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(zw).Encode(s); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeSnapshot(b []byte) (*snapshot, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var s snapshot
	if err := json.NewDecoder(zr).Decode(&s); err != nil {
		return nil, err
	}
	if s.Inodes == nil {
		return nil, errors.New("snapshot contains no inodes")
	}
	// An empty directory serializes with its entry map omitted, so it decodes
	// as nil. Restore the invariant that every directory has a usable map
	// before the tree is handed to anything that writes to it.
	for id, n := range s.Inodes {
		if n == nil {
			return nil, fmt.Errorf("snapshot has a nil inode at %d", id)
		}
		if n.isDir() && n.Entries == nil {
			n.Entries = make(map[string]uint64)
		}
	}
	return &s, nil
}

// committer periodically flushes dirty state so that a long-running mount does
// not keep an unbounded amount of work only in memory.
func (f *FS) committer(ctx context.Context) {
	t := time.NewTicker(f.commitInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Best effort final commit on shutdown, with a fresh context since
			// ctx is already cancelled.
			shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := f.Sync(shutCtx); err != nil {
				f.log.Error("final commit failed", "err", err)
			}
			return
		case <-t.C:
			if err := f.Sync(ctx); err != nil {
				f.log.Error("periodic commit failed", "err", err)
			}
		}
	}
}
