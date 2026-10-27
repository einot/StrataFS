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
	"time"

	"strata/internal/store"
)

// loadOrInit reads the current namespace from the bucket. A bucket with no
// root pointer holds no filesystem: a writable mount creates an empty one
// there, and a read-only mount refuses it, having written nothing (ADR 0011
// §1, §2).
//
// TLA+: docs/tla/CommitProtocol.tla GetRoot, RefuseEmpty, InitEmpty, GetSnapshot
func (f *FS) loadOrInit(ctx context.Context) error {
	// Name the store as the embedder gave it, not New's wrapper, whose name
	// only adds " (read-only)" to it.
	storeName := func() string {
		if ro, ok := f.store.(store.ReadOnly); ok {
			return ro.Store.Name()
		}
		return f.store.Name()
	}

	// The body and the ETag come from one response (ADR 0020 §1). The next
	// commit compare-and-swaps against this ETag, so it must be the ETag of
	// the root pointer this namespace descends from: one taken from a second
	// read could belong to a newer root pointer another mount swapped in
	// meanwhile, and our swap would then silently replace that mount's commit
	// instead of failing and diverging.
	raw, etag, err := f.store.GetWithETag(ctx, rootKey)
	if errors.Is(err, store.ErrNotFound) {
		if f.readOnly {
			return fmt.Errorf("blobfs: %s holds no filesystem, and a read-only mount does not create one", storeName())
		}
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

	// An empty rootETag means "no root", so keeping an empty ETag here would
	// make the first commit create-only against a root that exists,
	// misreporting a second writer. Nothing is at stake yet, so refuse and name the cause
	// (ADR 0020 §2, Assumption 3).
	if etag == "" {
		return fmt.Errorf("blobfs: %s returned no ETag for the root pointer, so commits cannot be compare-and-swapped", storeName())
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
	f.rootETag = etag

	if _, ok := f.inodes[rootInodeID]; !ok {
		return errors.New("snapshot has no root inode")
	}
	f.log.Info("mounted existing filesystem",
		"epoch", f.epoch, "inodes", len(f.inodes), "chunk_size", f.chunkSize)
	return nil
}

// initEmpty creates a brand-new filesystem: a single root directory, committed
// with If-None-Match so that two processes racing to initialize the same bucket
// cannot both win. Only a writable mount reaches it (ADR 0011 §2).
//
// TLA+: docs/tla/CommitProtocol.tla InitEmpty
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
//
// It runs in three steps (ADR 0012 §2): a first pass, flushAll, holding no
// gate; then, holding the commit gate for writing, a settle pass that flushes
// every file a truncate left unsettled, and the commit itself. The gate is
// held from the end of the first pass until Sync returns, its commit phase,
// so a truncate is in the commit whole if it returned before the phase began
// and not at all otherwise (§1, Rule D). An error from either pass commits
// nothing.
//
// TLA+: docs/tla/CommitProtocol.tla Flush
func (f *FS) Sync(ctx context.Context) error {
	// Buffered file data must be stored before the namespace that
	// references it. This happens before mu is taken because flushing acquires
	// per-file locks, which must never be taken while holding mu. It happens
	// before the gate is taken so that size changes do not wait for it
	// (ADR 0012 Assumption 7).
	if err := f.flushAll(ctx); err != nil {
		return fmt.Errorf("flush pending writes: %w", err)
	}

	// Taken holding no lock, as the lock order requires (ADR 0012 §5), and
	// released after mu, since its defer runs last.
	f.commitGate.Lock()
	defer f.commitGate.Unlock()
	if err := f.settleAll(ctx); err != nil {
		return fmt.Errorf("settle truncated files: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirty {
		return nil
	}
	return f.commitLocked(ctx)
}

// commitLocked writes the namespace as it stands and swaps the root pointer to
// it. Caller must hold mu for writing, except initEmpty, which runs before the
// FS is shared.
//
// It refuses, storing nothing, while any file is unsettled (ADR 0012 §1, Rule
// C; §4). Sync's settle pass makes that impossible, so a refusal means a path
// changed a chunk list without the commit gate, and is logged as the bug it is
// rather than committed as a silent exposure of removed bytes.
//
// TLA+: docs/tla/CommitProtocol.tla PutSnapshot, SwapOK, SwapReplyLost, SwapFailed, SwapConflict, SwapUnsupported, FallbackPut, FallbackHead, Diverge
func (f *FS) commitLocked(ctx context.Context) error {
	if f.diverged.Load() {
		return errors.New("filesystem diverged: another writer committed to this bucket; remount to continue")
	}
	for id, of := range f.open {
		if of.unsettled {
			f.log.Error("commit refused: a truncated file is unsettled",
				"inode", id, "epoch", f.epoch)
			return fmt.Errorf("commit refused: inode %d is unsettled since a truncate, so the snapshot could name bytes it removed", id)
		}
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
		f.diverged.Store(true)
		f.log.Error("commit conflict: another writer owns this bucket",
			"our_epoch", f.epoch, "attempted", epoch)
		return errors.New("commit conflict: another writer owns this bucket; remount to continue")
	case errors.Is(err, store.ErrUnsupported) && !f.fallbackAllowed():
		// One 501 says nothing about the next request, and only the operator
		// can assert that no second writer exists, so without the opt-in the
		// commit fails here and writes nothing more. The mount stays writable
		// and dirty, and its next commit tries the swap again (ADR 0021 §1, §2).
		//
		// The snapshot uploaded above is then named by no root pointer, and no
		// prune pass reaches it while the root pointer stands still, so it is
		// reclaimed: otherwise a store that answers every swap 501 gains an
		// orphan per attempt. A 501 is taken to change nothing, but the Delete
		// waits on one read of the root pointer rather than on that assumption,
		// since deleting a snapshot the root names would leave a bucket no mount
		// can load. The reclaim changes neither the error returned nor any field
		// of the FS. It is the module's R12 (ADR 0021 §2, the reclaim;
		// Assumptions 21, 22).
		f.log.Error("commit refused: the store does not support conditional writes, and this mount may not write the root pointer unconditionally",
			"our_epoch", f.epoch, "attempted", epoch)
		raw, rerr := f.store.Get(ctx, rootKey)
		var cur rootPointer
		if rerr == nil {
			// A body that does not decode is treated as a failed read: nothing
			// it says can show that the root names another key.
			rerr = json.Unmarshal(raw, &cur)
		}
		switch {
		case errors.Is(rerr, store.ErrNotFound) || (rerr == nil && cur.Snapshot != key):
			if derr := f.store.Delete(ctx, key); derr != nil {
				// The orphan stays for a later prune pass (ADR 0019 §3).
				f.log.Debug("could not reclaim a refused commit's snapshot", "key", key, "err", derr)
			}
		case rerr == nil:
			// The store applied a write it answered 501. The mount is left as it
			// is: its next swap, made with the old ETag, fails its condition and
			// diverges it, which is #5's to resolve.
			f.log.Error("a root-pointer swap the store answered as unsupported took effect: the root pointer names the refused commit's snapshot, which is kept",
				"snapshot", key, "attempted", epoch)
		default:
			f.log.Debug("could not read the root pointer, so a refused commit's snapshot is kept", "key", key, "err", rerr)
		}
		return fmt.Errorf("swap root pointer: the store does not support conditional writes, and this mount may not write the root pointer unconditionally: %w", err)
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
	f.schedulePrune(key)
	return nil
}

// fallbackAllowed reports whether a commit whose swap the store answered
// store.ErrUnsupported may write the root pointer unconditionally: only when
// the operator set Config.AllowUnconditionalRoot. It is a function of the
// mount's configuration alone, and not a policy: no caller asks for the write
// (ADR 0021 §2, §5).
//
// TLA+: docs/tla/CommitProtocol.tla SwapUnsupported
func (f *FS) fallbackAllowed() bool {
	return f.allowUnconditionalRoot
}

// schedulePrune starts a background pass that deletes superseded snapshots,
// unless one is already running. current is the snapshot key the calling
// commit swapped the root pointer to; the pass deletes only below it (ADR 0019
// §1, §2). Caller must hold mu.
//
// TLA+: docs/tla/CommitProtocol.tla StartPrune, PruneEnd
func (f *FS) schedulePrune(current string) {
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

// pruneSnapshots deletes superseded snapshots for a pass begun by the commit
// that swapped the root pointer to the key current. It deletes only keys that
// sort below current, keeping the newest retention of them, and never deletes
// current or any key above it (ADR 0019 §1, §2).
//
// That cannot delete the snapshot the root pointer names, at any retention and
// however commits, failed swaps and lost replies overlap the pass. When the
// pass begins the root pointer names current, and from then on the key it
// names never falls below current: a conditional write of the root pointer
// takes effect only against the ETag of the root it matched, and the writer's
// epoch is that root's, so the key it names is at the next epoch and sorts
// above current. Every key the pass deletes therefore sorts below the key the
// root pointer names, then and afterwards. The guarantee on the unconditional
// fallback is a single writer's, as the fallback's own is (ADR 0019 §1).
//
// So the pass never reads the root pointer: the committing mount already knows
// the key it named, and a key read later could only be higher.
//
// TLA+: docs/tla/CommitProtocol.tla PruneList, PruneDelete
func (f *FS) pruneSnapshots(ctx context.Context, current string) error {
	var keys []string
	after := ""
	for {
		batch, err := f.store.List(ctx, snapshotPrefix, after, 1000)
		if err != nil {
			return fmt.Errorf("list snapshots: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, o := range batch {
			keys = append(keys, o.Key)
		}
		after = batch[len(batch)-1].Key
		if len(batch) < 1000 {
			break
		}
	}

	doomed := prunable(keys, current, f.retention)
	deleted := 0
	for _, key := range doomed {
		if err := f.store.Delete(ctx, key); err != nil {
			// A failed delete only leaves garbage below current for a later
			// pass; it is not a correctness problem, so log and keep going.
			f.log.Debug("could not delete superseded snapshot", "key", key, "err", err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		f.log.Debug("pruned superseded snapshots",
			"deleted", deleted, "below", current, "retention", f.retention)
	}
	return nil
}

// prunable returns the keys a prune pass begun for the snapshot key current
// may delete, in ascending order: of the keys that sort below current, every
// one but the newest keep (ADR 0019 §2). String order is epoch order, then
// nonce order, since the epoch is zero-padded and the nonce has a fixed width.
// A negative keep counts as 0. keys are taken as distinct, as a listing's are,
// and neither keys nor its elements are modified.
//
// TLA+: docs/tla/CommitProtocol.tla PruneList
func prunable(keys []string, current string, keep int) []string {
	if keep < 0 {
		keep = 0
	}
	var below []string
	for _, k := range keys {
		if k < current {
			below = append(below, k)
		}
	}
	if len(below) <= keep {
		return nil
	}
	sort.Strings(below)
	return below[:len(below)-keep]
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

// TLA+: docs/tla/CommitProtocol.tla GetSnapshot
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
