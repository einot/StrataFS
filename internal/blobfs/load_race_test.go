package blobfs

// Clean-room regression tests for #89, "A mount's compare-and-swap ETag can
// belong to a newer root than the namespace it loaded", written without
// reading the implementation, from ADR 0020
// (docs/adr/0020-a-mount-loads-the-root-pointer-and-its-etag-in-one-read.md)
// sections 1, 2 and 6, ADR 0011 section 7, ADR 0012 section 9,
// docs/tla/CommitProtocol.tla (GetRoot, HeadRoot, GetSnapshot, SwapOK and
// SwapConflict), docs/tla/CommitProtocol_LoadRace.cfg,
// docs/tla/CommitProtocol_NoAtomicLoad.cfg, the counterexample TLC reported
// for CommitProtocol_LoadRace at 55fc666, and internal/vfs/vfs.go.
//
// ADR 0020 section 1's rule: the ETag a mount compare-and-swaps the root
// pointer against is always the ETag of the root pointer body its namespace
// descends from; before its first commit, the ETag that came in the same
// response as the body loadOrInit loaded. At 55fc666 loadOrInit reads the
// body with Get and the ETag with a later Head, so another writer's swap
// between the two gives the mount the older namespace and the newer ETag, and
// the mount's next swap replaces that writer's acknowledged commit
// (NoLostAck). ADR 0020 section 2 also makes New refuse a root pointer read
// with no ETag.
//
// Covered:
//   - TestCommitBetweenMountReadsIsRefusedNotOverwritten: ADR 0020 section
//     6's route test, steps 1 to 8, from CommitProtocol_LoadRace's
//     counterexample.
//   - TestNewRefusesRootPointerWithoutETag: section 6's refusal (section 2,
//     step 3), with Config.ReadOnly false and true.
//
// Not covered, on purpose:
//   - S1, S2 and S3 of ADR 0020 section 6
//     (TestLocalGetWithETagReturnsBodyAndItsETag,
//     TestS3GetWithETagTakesETagFromGetResponse and
//     TestReadOnlyPassesGetWithETagThrough). They test GetWithETag on a
//     *store.Local, a *store.S3 and a store.ReadOnly, which is
//     internal/store's to test, and they belong in
//     internal/store/get_with_etag_test.go.
//   - What ADR 0020 section 6 leaves to review: that loadOrInit takes the body
//     and the ETag from one GetWithETag call, makes no Head of the root
//     pointer on any path, and refuses an empty ETag before its snapshot read;
//     that S3.GetWithETag takes the ETag from the response that carried the
//     body, and Local.GetWithETag hashes the bytes it returns; that ReadOnly
//     overrides nothing new; that commitLocked, initEmpty and the fallback are
//     unchanged; and the TLA+ lines, the trace.tsv rows, and that GetRoot with
//     AtomicLoad describes loadOrInit.
//   - Logs, and the error's text beyond the two parts ADR 0020 section 2 pins
//     ("no ETag" and the store's name). Which store methods New calls is not
//     pinned beyond no Head of rootKey in the route test and no Put, Delete or
//     PutIfMatch in the refusal.
//
// The harness, lr89Store, is ADR 0020 section 6's: a store.Store over a
// *store.Local that does not embed store.Store and passes every call through
// to it. In addition it:
//   - passes GetWithETag through to inner.GetWithETag, so that the ETag a
//     mount loads is the store's own answer and not a copy of its rule
//     (ADR 0020 Assumption 14);
//   - when armed, holds the next read of rootKey, by Get or by GetWithETag,
//     once, after the inner call has returned and before the harness returns:
//     it records the loaded ETag, signals that it is held, and waits until
//     released or until the test's context ends. For GetWithETag the loaded
//     ETag is the ETag inner.GetWithETag returned. For Get, which returns no
//     ETag, it is the lowercase hexadecimal MD5 of the body Get returned,
//     *store.Local's ETag for that body (section 6's first fact), so that a
//     build which still loads the root pointer by Get is held there too and
//     its swap is compared with the body it loaded;
//   - records the etag argument of every PutIfMatch of rootKey, in call order,
//     counts every Head of rootKey, and records every Put, Delete and
//     PutIfMatch of any key;
//   - in its no-ETag mode, answers Head(rootKey) with the inner store's
//     ObjectInfo and an empty ETag, and GetWithETag(rootKey) with the inner
//     body and an empty ETag, and passes every other key through.
//
// The safety rules, as ADR 0011 section 7 and ADR 0012 section 9 give them and
// ADR 0020 section 6 restates them; every test here follows them:
//   - every New and every FS call runs in lr89Go, which recovers a panic, and
//     is waited for under lr89Bound, 10 s; goroutines never touch t, and
//     nothing here is parallel;
//   - every hold the harness takes selects on the test's context, which
//     t.Cleanup cancels, and a cleanup releases every hold, so no build can
//     hang the suite;
//   - every mount has SnapshotRetention -1 and MaxDirtyBytes -1, and no Write
//     is made, so no prune pass and no drain runs;
//   - there is no time.Sleep and no polling: every wait is on a channel, under
//     lr89Bound;
//   - every bucket is a local directory under the test's t.TempDir();
//   - the bucket is inspected through the inner *store.Local only after the
//     harness's records have been read, never through the harness;
//   - an FS that New returned where it should have refused is never used; the
//     case is reported failed instead.

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// lr89Bound is how long a test waits for any one step before it reports the
// step as hung.
const lr89Bound = 10 * time.Second

// lr89ETag is *store.Local's ETag for body: the lowercase hexadecimal MD5 of
// it (ADR 0020 section 6, the first fact). Only the hold on a read of rootKey
// by Get uses it, since Get returns no ETag.
func lr89ETag(body []byte) string {
	sum := md5.Sum(body)
	return hex.EncodeToString(sum[:])
}

// lr89Hold is the harness's one-shot hold on a read of rootKey. method,
// loaded and readErr are written by the held call before it closes entered,
// and may be read once entered is closed.
type lr89Hold struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	// method is the store method the held read was made with, "Get" or
	// "GetWithETag".
	method string

	// loaded is the loaded ETag: for GetWithETag, the ETag the inner store's
	// GetWithETag returned; for Get, the lowercase hexadecimal MD5 of the body
	// the held read is about to return; "" if the read failed.
	loaded string

	// readErr is what the inner store returned as the held read's error.
	readErr error
}

// Release lets the held call go on. It may be called more than once.
func (h *lr89Hold) Release() {
	h.once.Do(func() { close(h.release) })
}

// lr89Store is ADR 0020 section 6's harness. It does not embed store.Store, so
// that every method it has is one written here.
type lr89Store struct {
	inner   *store.Local
	testCtx context.Context

	// noETag selects the no-ETag mode. It is set before the first call and
	// never changed.
	noETag bool

	// mu guards every field below.
	mu sync.Mutex

	armed     *lr89Hold
	holds     []*lr89Hold
	swapETags []string
	rootHeads int
	writes    []string
}

var _ store.Store = (*lr89Store)(nil)

// arm makes the next read of rootKey, by Get or by GetWithETag, the one that
// is held, and returns its hold.
func (s *lr89Store) arm() *lr89Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &lr89Hold{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	s.armed = h
	s.holds = append(s.holds, h)
	return h
}

// releaseAll releases every hold the harness has made.
func (s *lr89Store) releaseAll() {
	s.mu.Lock()
	holds := append([]*lr89Hold(nil), s.holds...)
	s.mu.Unlock()
	for _, h := range holds {
		h.Release()
	}
}

// records returns the etag argument of every PutIfMatch of rootKey in call
// order, the number of Heads of rootKey, and every Put, Delete and PutIfMatch
// the harness has received.
func (s *lr89Store) records() (swapETags []string, rootHeads int, writes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.swapETags...), s.rootHeads, append([]string(nil), s.writes...)
}

// noteWrite records one call of a method that writes.
func (s *lr89Store) noteWrite(method, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, fmt.Sprintf("%s(%q)", method, key))
}

// readRoot is run by every read of rootKey by Get or GetWithETag, after the
// inner call has returned and before the harness returns, with the loaded
// ETag and the error the inner call returned. If the hold is armed it disarms
// it and holds the read: it records the loaded ETag, signals entered, and
// waits until the hold is released or testCtx ends.
func (s *lr89Store) readRoot(method, loaded string, err error) {
	s.mu.Lock()
	h := s.armed
	s.armed = nil
	s.mu.Unlock()
	if h == nil {
		return
	}
	h.method = method
	if err != nil {
		h.readErr = err
	} else {
		h.loaded = loaded
	}
	close(h.entered)
	select {
	case <-h.release:
	case <-s.testCtx.Done():
	}
}

// Get passes through. A read of rootKey goes through the hold, whose loaded
// ETag is then the MD5 of the body Get returned, since Get returns no ETag.
func (s *lr89Store) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.inner.Get(ctx, key)
	if key == rootKey {
		loaded := ""
		if err == nil {
			loaded = lr89ETag(data)
		}
		s.readRoot("Get", loaded, err)
	}
	return data, err
}

// GetWithETag passes through to the inner store's GetWithETag (ADR 0020
// Assumption 14), errors included. A read of rootKey goes through the hold,
// whose loaded ETag is the one the inner store returned; in the no-ETag mode
// the harness then answers it with an empty ETag.
func (s *lr89Store) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	data, etag, err := s.inner.GetWithETag(ctx, key)
	if key == rootKey {
		s.readRoot("GetWithETag", etag, err)
		if s.noETag {
			etag = ""
		}
	}
	return data, etag, err
}

func (s *lr89Store) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	return s.inner.GetRange(ctx, key, off, n)
}

func (s *lr89Store) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	info, err := s.inner.Head(ctx, key)
	if key == rootKey {
		s.mu.Lock()
		s.rootHeads++
		s.mu.Unlock()
		if s.noETag && err == nil {
			info.ETag = ""
		}
	}
	return info, err
}

func (s *lr89Store) Put(ctx context.Context, key string, data []byte) error {
	s.noteWrite("Put", key)
	return s.inner.Put(ctx, key, data)
}

func (s *lr89Store) Delete(ctx context.Context, key string) error {
	s.noteWrite("Delete", key)
	return s.inner.Delete(ctx, key)
}

func (s *lr89Store) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	return s.inner.List(ctx, prefix, after, max)
}

func (s *lr89Store) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) {
	s.noteWrite("PutIfMatch", key)
	if key == rootKey {
		s.mu.Lock()
		s.swapETags = append(s.swapETags, etag)
		s.mu.Unlock()
	}
	return s.inner.PutIfMatch(ctx, key, data, etag)
}

func (s *lr89Store) Name() string {
	return s.inner.Name()
}

// lr89Call is one call running in its own goroutine. val, returned, panicked
// and pval may be read only once done is closed.
type lr89Call[T any] struct {
	done     chan struct{}
	val      T
	returned bool
	panicked bool
	pval     any
}

// lr89Go runs fn in its own goroutine, recovering and recording a panic out of
// it. fn must not touch t: after a timeout its goroutine is abandoned.
func lr89Go[T any](fn func() T) *lr89Call[T] {
	c := &lr89Call[T]{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		defer func() {
			if r := recover(); r != nil {
				c.panicked = true
				c.pval = r
			}
		}()
		c.val = fn()
		c.returned = true
	}()
	return c
}

// wait waits for the call under lr89Bound and returns its value. A hang, a
// panic or a runtime.Goexit fails the test.
func (c *lr89Call[T]) wait(t *testing.T, what string) T {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(lr89Bound):
		t.Fatalf("%s has not returned after %v, so it is hung", what, lr89Bound)
	}
	if c.panicked {
		t.Fatalf("%s panicked with %v", what, c.pval)
	}
	if !c.returned {
		t.Fatalf("%s ended without returning (runtime.Goexit)", what)
	}
	return c.val
}

// lr89NewResult is what one New returned.
type lr89NewResult struct {
	fs  *FS
	err error
}

// lr89NewHarness makes a harness over a fresh local bucket under the test's
// temporary directory, in the no-ETag mode if asked, and the test's context,
// which a cleanup cancels. Another cleanup, which runs first, releases every
// hold.
func lr89NewHarness(t *testing.T, noETag bool) (*lr89Store, context.Context) {
	t.Helper()
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &lr89Store{
		inner:   local,
		testCtx: ctx,
		noETag:  noETag,
	}
	t.Cleanup(h.releaseAll)
	return h, ctx
}

// lr89Config is every mount's Config: the root directory owned by testCaller,
// no pruning and no wait on the write budget (ADR 0020 section 6).
func lr89Config(st store.Store, readOnly bool) Config {
	return Config{
		Store:             st,
		ChunkSize:         4096,
		OwnerUID:          501,
		OwnerGID:          20,
		ReadOnly:          readOnly,
		SnapshotRetention: -1,
		MaxDirtyBytes:     -1,
		Log:               quietLog(),
	}
}

// lr89Mode names a mount's mode for test names and failure messages.
func lr89Mode(readOnly bool) string {
	if readOnly {
		return "read-only"
	}
	return "writable"
}

// lr89StartNew starts New in lr89Go.
func lr89StartNew(ctx context.Context, cfg Config) *lr89Call[lr89NewResult] {
	return lr89Go(func() lr89NewResult {
		fs, err := New(ctx, cfg)
		return lr89NewResult{fs, err}
	})
}

// lr89MustNew runs New and requires it to succeed.
func lr89MustNew(t *testing.T, ctx context.Context, what string, cfg Config) *FS {
	t.Helper()
	r := lr89StartNew(ctx, cfg).wait(t, what)
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	if r.fs == nil {
		t.Fatalf("%s returned a nil *FS and a nil error", what)
	}
	return r.fs
}

// lr89WaitHeld waits until hold is entered by the New that c runs.
func lr89WaitHeld(t *testing.T, hold *lr89Hold, c *lr89Call[lr89NewResult], what string) {
	t.Helper()
	select {
	case <-hold.entered:
	case <-c.done:
		t.Fatalf("%s returned (a non-nil *FS: %v; error %v; panicked %v with %v) without a read of the root "+
			"pointer %q by Get or GetWithETag, so the test never held it", what, c.val.fs != nil, c.val.err,
			c.panicked, c.pval, rootKey)
	case <-time.After(lr89Bound):
		t.Fatalf("%s has made no read of the root pointer %q by Get or GetWithETag after %v", what, rootKey,
			lr89Bound)
	}
}

// lr89TryCreate creates name in the root directory of fs, as testCaller, a
// regular file, exclusively, and returns what Create returned as its error.
func lr89TryCreate(t *testing.T, ctx context.Context, fs *FS, name string) error {
	t.Helper()
	return lr89Go(func() error {
		_, _, err := fs.Create(ctx, testCaller, fs.Root(), name, vfs.SetAttr{}, true)
		return err
	}).wait(t, fmt.Sprintf("Create %q in the root directory", name))
}

// lr89Commit is ADR 0020 section 6's commit: a Create of a fresh name in the
// root directory, by testCaller, which must succeed, then Sync. It returns
// what Sync returned.
func lr89Commit(t *testing.T, ctx context.Context, fs *FS, name, what string) error {
	t.Helper()
	if err := lr89TryCreate(t, ctx, fs, name); err != nil {
		t.Fatalf("Create %q in the root directory as its owner (%d, %d) = %v, want success", name,
			testCaller.UID, testCaller.GID, err)
	}
	return lr89Go(func() error { return fs.Sync(ctx) }).wait(t, what)
}

// lr89CheckRootSnapshot requires, through local only, the root pointer to
// decode as rootPointer, the snapshot it names to decode with decodeSnapshot,
// and that snapshot's root directory to hold want and not notWant.
func lr89CheckRootSnapshot(t *testing.T, ctx context.Context, local *store.Local, want, notWant string) {
	t.Helper()
	raw, err := local.Get(ctx, rootKey)
	if err != nil {
		t.Errorf("Get(%q) on the bucket = %v, want the root pointer", rootKey, err)
		return
	}
	var rp rootPointer
	if err := json.Unmarshal(raw, &rp); err != nil {
		t.Errorf("the root pointer %q does not decode as rootPointer: %v", raw, err)
		return
	}
	snapRaw, err := local.Get(ctx, rp.Snapshot)
	if err != nil {
		t.Errorf("Get of the snapshot the root pointer names, %q, on the bucket = %v", rp.Snapshot, err)
		return
	}
	snap, err := decodeSnapshot(snapRaw)
	if err != nil {
		t.Errorf("decodeSnapshot of the snapshot the root pointer names, %q = %v", rp.Snapshot, err)
		return
	}
	if snap == nil {
		t.Errorf("decodeSnapshot of the snapshot the root pointer names, %q, returned a nil snapshot and a "+
			"nil error", rp.Snapshot)
		return
	}
	dir := snap.Inodes[rootInodeID]
	if dir == nil {
		t.Errorf("the snapshot the root pointer names, %q, has no root directory (Inodes[%d])", rp.Snapshot,
			rootInodeID)
		return
	}
	if _, ok := dir.Entries[want]; !ok {
		t.Errorf("the root directory in the snapshot the root pointer names, %q, has no %q: A's acknowledged "+
			"commit is not in the snapshot the root pointer names (NoLostAck; ADR 0020 section 1; #89). Its "+
			"entries: %v", rp.Snapshot, want, dir.Entries)
	}
	if _, ok := dir.Entries[notWant]; ok {
		t.Errorf("the root directory in the snapshot the root pointer names, %q, has %q: B's swap, against "+
			"a root pointer it never loaded, took effect (ADR 0020 section 1; #89). Its entries: %v",
			rp.Snapshot, notWant, dir.Entries)
	}
}

// lr89CheckRemount requires New on local to succeed and Lookup of name in its
// root directory to succeed.
func lr89CheckRemount(t *testing.T, ctx context.Context, local *store.Local, name string) {
	t.Helper()
	what := "the remount: New on the bucket with SnapshotRetention -1"
	r := lr89StartNew(ctx, lr89Config(local, false)).wait(t, what)
	if r.err != nil {
		t.Errorf("%s = %v, want success", what, r.err)
		return
	}
	if r.fs == nil {
		t.Errorf("%s returned a nil *FS and a nil error", what)
		return
	}
	fs := r.fs
	err := lr89Go(func() error {
		_, _, err := fs.Lookup(ctx, testCaller, fs.Root(), name)
		return err
	}).wait(t, fmt.Sprintf("Lookup of %q in the remount's root directory", name))
	if err != nil {
		t.Errorf("Lookup of %q in the remount's root directory = %v, want success: A's acknowledged commit "+
			"stands (ADR 0020 section 1; #89)", name, err)
	}
}

// TestCommitBetweenMountReadsIsRefusedNotOverwritten is ADR 0020 section 6's
// route test, from the counterexample TLC reported for
// docs/tla/CommitProtocol_LoadRace.cfg at 55fc666 (the route
// docs/tla/CommitProtocol_NoAtomicLoad.cfg shows): a commit by mount A that
// lands while mount B is loading is refused at B's next swap, not overwritten
// by it.
//
// The trace's thirteen states, and the test's steps:
//   - states 2 to 4, A's InitEmpty, PutSnapshot [1,1] and SwapOK: step 1, New
//     of A over the empty bucket;
//   - state 7, B's GetRoot: step 2, the hold armed and New of B started; the
//     harness holds B's first read of the root pointer once it has returned
//     the epoch-1 root pointer, and records its loaded ETag;
//   - states 5, 6 and 8, A's Mutate, PutSnapshot [2,1] and SwapOK: step 3, A
//     commits a2 while B is held, and Sync returns nil;
//   - states 9 and 10, B's HeadRoot and GetSnapshot: step 4, the hold
//     released and B's New returning. At 55fc666 B's Head of the root pointer
//     takes A's ETag here; with ADR 0020 there is no HeadRoot, and B holds the
//     ETag of the body it loaded;
//   - states 11 to 13, B's Mutate, PutSnapshot [2,2] and SwapOK: step 5, B
//     commits b2. With ADR 0020 the swap fails its condition (SwapConflict,
//     not SwapOK): Sync returns an error, and a Create of b3 on B returns
//     vfs.ErrStale;
//   - NoLostAck, which state 13 violates: steps 6 to 8. B swapped against the
//     loaded ETag, the etag of the last PutIfMatch of rootKey; no Head of
//     rootKey was made; the snapshot the root pointer names holds a2 and not
//     b2; and a remount finds a2.
//
// Step 6 compares the loaded ETag with the last PutIfMatch of rootKey, which
// the test requires to be one of B's Sync, and not with any matching one: A's
// own swap in step 3 carries the same ETag, the epoch-1 root pointer's.
//
// Where it deviates from the trace:
//   - The hold sits on the way out of the first read of the root pointer,
//     whichever method a build makes it with (Get at 55fc666, GetWithETag
//     with ADR 0020), and not between GetRoot and HeadRoot: a test cannot hold
//     HeadRoot, which the fix removes (ADR 0020 Assumption 14).
//   - The loaded ETag is the one inner.GetWithETag returned when the held
//     read is by GetWithETag. When it is by Get, which returns no ETag, the
//     harness takes *store.Local's ETag for the body Get returned, its MD5
//     (ADR 0020 section 6, the first fact).
//   - A's Mutate and PutSnapshot come after B's GetRoot here, where the trace
//     has them before it (states 5 and 6). What the route rests on is that
//     A's SwapOK falls between B's GetRoot and HeadRoot, which it does.
//   - Nonces come from crypto/rand, so the snapshot keys are not [2,1] and
//     [2,2]; nothing here depends on them.
//
// At 55fc666 steps 5 to 8 fail: B's Sync returns nil, a Create of b3 succeeds,
// the last swap carries A's ETag, Head of rootKey was called, and the root
// pointer's snapshot holds b2 and not a2, so the remount finds no a2.
func TestCommitBetweenMountReadsIsRefusedNotOverwritten(t *testing.T) {
	h, ctx := lr89NewHarness(t, false)

	// 1. A initialises the bucket: InitEmpty, PutSnapshot [1,1], SwapOK.
	a := lr89MustNew(t, ctx, "New of mount A over the empty bucket", lr89Config(h, false))

	// 2. Arm the hold, start New of B, and wait until B's first read of the
	// root pointer has returned and is held (GetRoot).
	hold := h.arm()
	newB := lr89StartNew(ctx, lr89Config(h, false))
	lr89WaitHeld(t, hold, newB, "New of mount B")
	if hold.readErr != nil {
		t.Fatalf("B's %s of the root pointer %q, which the harness holds, = %v, want the epoch-1 root pointer "+
			"A's New wrote", hold.method, rootKey, hold.readErr)
	}
	loaded := hold.loaded

	// 3. A commits a2 while B is held: Mutate, PutSnapshot [2,1], SwapOK.
	if err := lr89Commit(t, ctx, a, "a2", "Sync of A's commit of a2 while B's New is held"); err != nil {
		t.Fatalf("Sync of A's commit of a2, while B's New is held after its read of the root pointer = %v, "+
			"want success: nothing else has moved the root pointer since A wrote it", err)
	}

	// 4. Release the hold; B's New returns.
	hold.Release()
	rb := newB.wait(t, "New of mount B, once its read of the root pointer is released")
	if rb.err != nil {
		t.Fatalf("New of mount B, once its %s of the root pointer is released = %v, want success", hold.method,
			rb.err)
	}
	if rb.fs == nil {
		t.Fatalf("New of mount B returned a nil *FS and a nil error")
	}
	b := rb.fs

	// 5. B commits b2: its swap fails its condition, and B has diverged.
	swapsBefore, _, _ := h.records()
	mark := len(swapsBefore)
	if err := lr89Commit(t, ctx, b, "b2", "Sync of B's commit of b2"); err == nil {
		t.Errorf("Sync of B's commit of b2 = nil, want an error: A moved the root pointer after B read it, so " +
			"B's swap must fail its condition (ADR 0020 section 1: the ETag swapped against is that of the " +
			"body B loaded; #89)")
	}
	if err := lr89TryCreate(t, ctx, b, "b3"); !errors.Is(err, vfs.ErrStale) {
		t.Errorf("Create %q on B after its failed swap = %v, want vfs.ErrStale: a mount whose swap failed its "+
			"condition has diverged, and refuses every change (ADR 0003 section 4; ADR 0020 section 6)",
			"b3", err)
	}

	// 6 and 7. The harness's records, read before the bucket is inspected.
	swaps, rootHeads, _ := h.records()
	if len(swaps) <= mark {
		t.Errorf("B's Sync made no PutIfMatch of the root pointer %q (the harness saw %d, all before it: %q), "+
			"so B never swapped against the ETag it loaded", rootKey, len(swaps), swaps)
	} else if last := swaps[len(swaps)-1]; last != loaded {
		t.Errorf("the last PutIfMatch of the root pointer %q, B's, carried the ETag %q, want %q, the ETag of "+
			"the root pointer body B's %s loaded: a mount swaps against the ETag of the body its namespace "+
			"descends from, never one from another read (ADR 0020 section 1; #89). Every PutIfMatch of %q, "+
			"in call order: %q", rootKey, last, loaded, hold.method, rootKey, swaps)
	}
	if rootHeads != 0 {
		t.Errorf("the harness counted %d Head(s) of the root pointer %q, want none: a mount takes the root "+
			"pointer's ETag from the read that gave it the body, and makes no Head of it (ADR 0020 section 2)",
			rootHeads, rootKey)
	}

	// 8. Through the inner store: A's acknowledged commit is in the snapshot
	// the root pointer names, and a remount finds it.
	lr89CheckRootSnapshot(t, ctx, h.inner, "a2", "b2")
	lr89CheckRemount(t, ctx, h.inner, "a2")
}

// TestNewRefusesRootPointerWithoutETag is ADR 0020 section 6's refusal, of
// section 2 step 3: a root pointer read with no ETag fails the mount, before
// it writes anything. For Config.ReadOnly false and true, over a bucket that
// holds a filesystem seeded through the inner *store.Local, New on the
// harness in its no-ETag mode returns a nil *FS and a non-nil error whose text
// contains "no ETag" and cfg.Store.Name(); the harness saw no Put, Delete or
// PutIfMatch; and the root pointer's bytes on the inner store are as they
// were. An FS that New returns here is never used.
//
// At 55fc666 New returns an *FS and no error in both cases.
func TestNewRefusesRootPointerWithoutETag(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(lr89Mode(readOnly), func(t *testing.T) {
			h, ctx := lr89NewHarness(t, true)
			lr89MustNew(t, ctx, "setup: New on the inner store over an empty bucket, to seed a filesystem",
				lr89Config(h.inner, false))
			rootBefore, err := h.inner.Get(ctx, rootKey)
			if err != nil {
				t.Fatalf("setup: Get(%q) on the bucket after the seeding mount = %v, want the root pointer",
					rootKey, err)
			}

			cfg := lr89Config(h, readOnly)
			name := cfg.Store.Name()
			what := fmt.Sprintf("New, %s, on a store that gives the root pointer no ETag", lr89Mode(readOnly))
			r := lr89StartNew(ctx, cfg).wait(t, what)
			_, _, writes := h.records()

			if r.fs != nil {
				t.Errorf("%s returned a non-nil *FS, want nil: a root pointer read with no ETag fails the "+
					"mount (ADR 0020 section 2, step 3). The FS is not used", what)
			}
			if r.err == nil {
				t.Errorf("%s returned a nil error, want a non-nil one (ADR 0020 section 2, step 3)", what)
			} else {
				msg := r.err.Error()
				if !strings.Contains(msg, "no ETag") {
					t.Errorf("%s: error %q, want it to contain %q (ADR 0020 section 2, step 3)", what, msg,
						"no ETag")
				}
				if !strings.Contains(msg, name) {
					t.Errorf("%s: error %q, want it to contain the store's name, cfg.Store.Name(), %q "+
						"(ADR 0020 section 2, step 3)", what, msg, name)
				}
			}
			for _, w := range writes {
				t.Errorf("%s made the call %s on cfg.Store, want no Put, Delete or PutIfMatch: New refuses "+
					"having written nothing (ADR 0020 section 2, step 3)", what, w)
			}

			rootAfter, err := h.inner.Get(ctx, rootKey)
			if err != nil {
				t.Errorf("Get(%q) on the bucket after %s = %v, want the root pointer as it was", rootKey, what,
					err)
			} else if !bytes.Equal(rootAfter, rootBefore) {
				t.Errorf("after %s the root pointer is %q, want it as it was, %q: a refused New writes "+
					"nothing (ADR 0020 section 2, step 3)", what, rootAfter, rootBefore)
			}
		})
	}
}
