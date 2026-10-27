package blobfs

// Clean-room regression tests for #91, "One HTTP 501 turns a root-pointer
// swap into a blind overwrite", written without reading the implementation,
// from ADR 0021
// (docs/adr/0021-the-unconditional-root-write-needs-the-operators-opt-in.md)
// sections 1, 2, 3 and 6 and Assumptions 4, 5 and 8, ADR 0011 section 7,
// ADR 0012 section 9, ADR 0019 section 6, ADR 0020 section 6,
// docs/tla/CommitProtocol.tla (SwapOK, SwapConflict, SwapUnsupported,
// FallbackPut, FallbackHead, Committed and CommitFails),
// docs/tla/CommitProtocol_Fallback.cfg,
// docs/tla/CommitProtocol_NoFallbackOptIn.cfg, the counterexample TLC
// reported for CommitProtocol_Fallback at 247a276, and internal/vfs/vfs.go.
//
// ADR 0021 section 1's rule: a mount writes the root pointer with a condition,
// always, unless its operator has said that exactly one mount writes the
// bucket (Config.AllowUnconditionalRoot). A store's reply never changes that.
// A swap the store answers store.ErrUnsupported fails the commit: nothing more
// is written, the mount stays writable, and its next commit tries the swap
// again (section 2; Assumption 4). At 247a276 commitLocked answers any 501 on
// the swap with an unconditional Put of the root pointer and a Head, so one
// 501 from a store that honours every other conditional write replaces
// another mount's acknowledged commit (NoLostAck).
//
// Covered: the three tests of ADR 0021 section 6 that compile at 247a276 and
// fail there, and the four that need Config.AllowUnconditionalRoot or
// (*FS).fallbackAllowed.
//   - TestOne501AfterHonouredSwapsMakesNoUnconditionalRootWrite: F1, the route
//     test, steps 1 to 8, from CommitProtocol_Fallback's counterexample.
//   - TestNewOnAStoreWithoutConditionalWritesIsRefused: F2.
//   - TestCommitOnAStoreWithoutConditionalWritesFailsWithoutOptIn: F3.
//   - TestOptInRestoresTheUnconditionalFallback: O1.
//   - TestOptInFallsBackAfterOne501: O2.
//   - TestOptInOnAReadOnlyMountWritesNothing: O3.
//   - TestFallbackAllowedFollowsConfig: O4.
//   - TestRefusedCommitReclaimsItsSnapshot, TestRefusedInitLeavesTheBucketEmpty,
//     TestRefusedCommitKeepsASnapshotTheRootPointerNames,
//     TestRefusedCommitIgnoresAFailedReclaim and
//     TestStoreHonouringOnlyIfNoneMatchCreatesThenCommitsNothing: G1 to G5,
//     section 6's tests added on review, for the reclaim of section 2
//     (Assumptions 21, 22 and 24; the module's R12 and J14).
//
// Elsewhere:
//   - O5, the R8 update, is in internal/blobfs/prune_root_test.go.
//   - C1 to C3, which call runCheck, are in cmd/strata/check_test.go.
//
// Not covered, on purpose:
//   - What ADR 0021 section 6 leaves to review: that commitLocked writes the
//     root pointer no further after a refused swap on every path and leaves
//     FS.epoch, FS.rootETag, FS.lastCommit and FS.diverged as they were; that
//     the reclaim's Delete follows only a Get of rootKey that returned
//     not-found or a root pointer naming another key, and only of the key
//     the commit wrote; that the reclaim changes no field and not the error;
//     what the mount does after a 501 that took effect (#5's); that the
//     opt-in branch is the code of 247a276; who reads and writes
//     FS.allowUnconditionalRoot; the flag's wiring and help text; -check's
//     severity; the TLA+ lines and trace.tsv rows.
//   - Logs, whose attributes ADR 0021 Assumption 8 does not pin, and the
//     error's text beyond "unconditional" (section 2).
//
// The harness, sf91Store, is ADR 0021 section 6's: a store.Store over a
// *store.Local under t.TempDir(), which does not embed store.Store and passes
// every call through to it, and in addition:
//   - when armed, one-shot, answers the next PutIfMatch of rootKey with
//     store.ErrUnsupported without calling the inner store: one 501 from a
//     store that honours every other conditional write (Capability "Flaky");
//   - in its unconditional mode, set before the first call and never changed,
//     answers every PutIfMatch of any key with store.ErrUnsupported without
//     calling the inner store (Capability "Unconditional");
//   - records, in call order, every PutIfMatch of rootKey with its etag
//     argument and whether it was answered 501 or passed through (and, when
//     passed through, what the inner store returned); records every Put of
//     rootKey with its bytes; counts every Head of rootKey; and counts every
//     Put, Delete and PutIfMatch of any key;
//   - and, for the tests added on review: in its create-only mode, set before
//     the first call, passes every PutIfMatch whose etag is "" through and
//     answers every other PutIfMatch with store.ErrUnsupported without
//     calling the inner store; when armed, one-shot, applies then answers
//     501: passes the next PutIfMatch of rootKey through and, if it
//     succeeded, returns "" and store.ErrUnsupported; when armed, one-shot,
//     fails the next Delete under snapshotPrefix with sf91ErrInjected without
//     calling the inner store; and logs, in one call order, every Get of
//     rootKey (counted), every Delete with its key, and the key of every Put
//     under snapshotPrefix, so that a test can see the order of the three.
//
// It takes no hold: every route here is sequential, and the 501 is injected
// synchronously. Two mounts in one test share one harness.
//
// The safety rules, as ADR 0011 section 7, ADR 0012 section 9 and ADR 0020
// section 6 give them and ADR 0021 section 6 restates them; every test here
// follows them:
//   - every New and every FS call runs in sf91Go, which recovers a panic, and
//     is waited for under sf91Bound, 10 s; goroutines never touch t, and
//     nothing here is parallel;
//   - every mount has SnapshotRetention -1 and MaxDirtyBytes -1, and no Write
//     is made, so no prune pass and no drain runs;
//   - there is no time.Sleep and no polling, and no test calls Run, so no
//     committer runs;
//   - every bucket is a local directory under the test's t.TempDir();
//   - the bucket is inspected through the inner *store.Local only after the
//     harness's records have been read, never through the harness;
//   - an *FS that New returned where it should have refused is never used;
//     the case is reported failed instead.

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// sf91Bound is how long a test waits for any one step before it reports the
// step as hung (ADR 0021 section 6, the safety rules).
const sf91Bound = 10 * time.Second

// sf91Caller is the root directory's owner, as every mount here is configured
// (ADR 0021 section 6: a commit's Create is by vfs.Caller{UID: 501, GID: 20}).
var sf91Caller = vfs.Caller{UID: 501, GID: 20}

// sf91ErrInjected is the error the harness's fail-next-Delete returns. It is
// neither store.ErrNotFound nor store.ErrUnsupported.
var sf91ErrInjected = errors.New("sf91: injected Delete failure")

// sf91Swap is the harness's record of one PutIfMatch of rootKey: answered501
// when the harness answered it store.ErrUnsupported without calling the inner
// store, appliedThen501 when the inner store applied it and the harness then
// answered store.ErrUnsupported, and otherwise passed through, with what the
// inner store returned in err.
type sf91Swap struct {
	etag           string
	answered501    bool
	appliedThen501 bool
	err            error
}

// String describes the call for failure messages.
func (s sf91Swap) String() string {
	switch {
	case s.answered501:
		return fmt.Sprintf("PutIfMatch(%q, etag %q): answered store.ErrUnsupported by the harness", rootKey, s.etag)
	case s.appliedThen501:
		return fmt.Sprintf("PutIfMatch(%q, etag %q): applied by the inner store, then answered "+
			"store.ErrUnsupported by the harness", rootKey, s.etag)
	}
	return fmt.Sprintf("PutIfMatch(%q, etag %q): passed through, inner store returned %v", rootKey, s.etag, s.err)
}

// sf91Event is one call in the harness's ordered log: a Put under
// snapshotPrefix, a Get of rootKey, or a Delete of any key. failed marks a
// Delete the harness failed with sf91ErrInjected.
type sf91Event struct {
	op     string
	key    string
	failed bool
}

// String describes the call for failure messages.
func (e sf91Event) String() string {
	if e.failed {
		return fmt.Sprintf("%s(%q), failed by the harness", e.op, e.key)
	}
	return fmt.Sprintf("%s(%q)", e.op, e.key)
}

// sf91Records is a copy of what the harness has recorded.
type sf91Records struct {
	swaps        []sf91Swap
	rootPuts     [][]byte
	rootHeads    int
	puts         int
	deletes      int
	putIfMatches int

	// events is every Put under snapshotPrefix, every Get of rootKey and
	// every Delete, in call order. snapPuts is the keys of the Puts, rootGets
	// the number of the Gets, and deleted the Deletes, each drawn from it.
	events   []sf91Event
	snapPuts []string
	rootGets int
	deleted  []sf91Event
}

// sf91Store is ADR 0021 section 6's harness. It does not embed store.Store,
// so that every method it has is one written here.
type sf91Store struct {
	inner *store.Local

	// unconditional answers every PutIfMatch of any key with
	// store.ErrUnsupported without calling the inner store. It is set before
	// the first call and never changed.
	unconditional bool

	// createOnly passes every PutIfMatch whose etag is "" through and answers
	// every other PutIfMatch with store.ErrUnsupported without calling the
	// inner store (the Swift shape of ADR 0021's Context). It is set before
	// the first call and never changed.
	createOnly bool

	// mu guards every field below.
	mu sync.Mutex

	// armed answers the next PutIfMatch of rootKey with store.ErrUnsupported
	// without calling the inner store, once.
	armed bool

	// applyThen501 passes the next PutIfMatch of rootKey through to the inner
	// store and, if it succeeded, answers "" and store.ErrUnsupported, once.
	applyThen501 bool

	// failDelete fails the next Delete under snapshotPrefix with
	// sf91ErrInjected without calling the inner store, once.
	failDelete bool

	// The records: every PutIfMatch of rootKey in call order, the bytes of
	// every Put of rootKey in call order, the number of Heads of rootKey, and
	// the numbers of Puts, Deletes and PutIfMatches of any key.
	swaps        []sf91Swap
	rootPuts     [][]byte
	rootHeads    int
	puts         int
	deletes      int
	putIfMatches int

	// events is every Put under snapshotPrefix, every Get of rootKey and
	// every Delete of any key, in call order.
	events []sf91Event
}

var _ store.Store = (*sf91Store)(nil)

// arm501 makes the next PutIfMatch of rootKey the one answered
// store.ErrUnsupported (Capability "Flaky").
func (s *sf91Store) arm501() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed = true
}

// armApplyThen501 makes the next PutIfMatch of rootKey one that the inner
// store applies and the harness then answers store.ErrUnsupported: a store
// that applies a write it answers 501.
func (s *sf91Store) armApplyThen501() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyThen501 = true
}

// armFailDelete makes the next Delete under snapshotPrefix fail with
// sf91ErrInjected, without calling the inner store.
func (s *sf91Store) armFailDelete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDelete = true
}

// records returns a copy of everything the harness has recorded.
func (s *sf91Store) records() sf91Records {
	s.mu.Lock()
	defer s.mu.Unlock()
	puts := make([][]byte, len(s.rootPuts))
	for i, b := range s.rootPuts {
		puts[i] = append([]byte(nil), b...)
	}
	rec := sf91Records{
		swaps:        append([]sf91Swap(nil), s.swaps...),
		rootPuts:     puts,
		rootHeads:    s.rootHeads,
		puts:         s.puts,
		deletes:      s.deletes,
		putIfMatches: s.putIfMatches,
		events:       append([]sf91Event(nil), s.events...),
	}
	for _, e := range rec.events {
		switch e.op {
		case "Put":
			rec.snapPuts = append(rec.snapPuts, e.key)
		case "Get":
			rec.rootGets++
		case "Delete":
			rec.deleted = append(rec.deleted, e)
		}
	}
	return rec
}

// Get passes through, logging every Get of rootKey.
func (s *sf91Store) Get(ctx context.Context, key string) ([]byte, error) {
	if key == rootKey {
		s.mu.Lock()
		s.events = append(s.events, sf91Event{op: "Get", key: key})
		s.mu.Unlock()
	}
	return s.inner.Get(ctx, key)
}

func (s *sf91Store) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	return s.inner.GetRange(ctx, key, off, n)
}

// Head passes through, counting every Head of rootKey.
func (s *sf91Store) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	if key == rootKey {
		s.mu.Lock()
		s.rootHeads++
		s.mu.Unlock()
	}
	return s.inner.Head(ctx, key)
}

// Put passes through, counting every Put, keeping the bytes of every Put of
// rootKey, and logging every Put under snapshotPrefix.
func (s *sf91Store) Put(ctx context.Context, key string, data []byte) error {
	s.mu.Lock()
	s.puts++
	if key == rootKey {
		s.rootPuts = append(s.rootPuts, append([]byte(nil), data...))
	}
	if strings.HasPrefix(key, snapshotPrefix) {
		s.events = append(s.events, sf91Event{op: "Put", key: key})
	}
	s.mu.Unlock()
	return s.inner.Put(ctx, key, data)
}

// Delete counts and logs every Delete. When armed it fails the next Delete
// under snapshotPrefix with sf91ErrInjected without calling the inner store,
// once; every other Delete passes through.
func (s *sf91Store) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	s.deletes++
	fail := s.failDelete && strings.HasPrefix(key, snapshotPrefix)
	if fail {
		s.failDelete = false
	}
	s.events = append(s.events, sf91Event{op: "Delete", key: key, failed: fail})
	s.mu.Unlock()
	if fail {
		return sf91ErrInjected
	}
	return s.inner.Delete(ctx, key)
}

func (s *sf91Store) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	return s.inner.List(ctx, prefix, after, max)
}

// PutIfMatch counts every call. In the unconditional mode it answers every
// call store.ErrUnsupported without calling the inner store, and in the
// create-only mode every call whose etag is not ""; otherwise, when armed, it
// answers the next call for rootKey so, once, and when armed to apply then
// answer 501, it passes the next call for rootKey through and, if the inner
// store applied it, answers "" and store.ErrUnsupported, once. Every other
// call passes through. Every call for rootKey is recorded with its etag
// argument and its outcome.
func (s *sf91Store) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) {
	s.mu.Lock()
	s.putIfMatches++
	answer501 := s.unconditional || (s.createOnly && etag != "")
	if key == rootKey && !answer501 && s.armed {
		s.armed = false
		answer501 = true
	}
	applyThen501 := false
	if key == rootKey && !answer501 && s.applyThen501 {
		s.applyThen501 = false
		applyThen501 = true
	}
	if answer501 {
		if key == rootKey {
			s.swaps = append(s.swaps, sf91Swap{etag: etag, answered501: true})
		}
		s.mu.Unlock()
		return "", store.ErrUnsupported
	}
	s.mu.Unlock()

	newETag, err = s.inner.PutIfMatch(ctx, key, data, etag)
	if key == rootKey {
		s.mu.Lock()
		if applyThen501 && err == nil {
			s.swaps = append(s.swaps, sf91Swap{etag: etag, appliedThen501: true})
		} else {
			s.swaps = append(s.swaps, sf91Swap{etag: etag, err: err})
		}
		s.mu.Unlock()
	}
	if applyThen501 && err == nil {
		return "", store.ErrUnsupported
	}
	return newETag, err
}

func (s *sf91Store) Name() string {
	return s.inner.Name()
}

// GetWithETag passes through to the inner store's GetWithETag (ADR 0020
// Assumption 14).
func (s *sf91Store) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	return s.inner.GetWithETag(ctx, key)
}

// sf91Call is one call running in its own goroutine. val, returned, panicked
// and pval may be read only once done is closed.
type sf91Call[T any] struct {
	done     chan struct{}
	val      T
	returned bool
	panicked bool
	pval     any
}

// sf91Go runs fn in its own goroutine, recovering and recording a panic out of
// it. fn must not touch t: after a timeout its goroutine is abandoned.
func sf91Go[T any](fn func() T) *sf91Call[T] {
	c := &sf91Call[T]{done: make(chan struct{})}
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

// wait waits for the call under sf91Bound and returns its value. A hang, a
// panic or a runtime.Goexit fails the test.
func (c *sf91Call[T]) wait(t *testing.T, what string) T {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(sf91Bound):
		t.Fatalf("%s has not returned after %v, so it is hung", what, sf91Bound)
	}
	if c.panicked {
		t.Fatalf("%s panicked with %v", what, c.pval)
	}
	if !c.returned {
		t.Fatalf("%s ended without returning (runtime.Goexit)", what)
	}
	return c.val
}

// sf91NewResult is what one New returned.
type sf91NewResult struct {
	fs  *FS
	err error
}

// sf91NewHarness makes a harness over a fresh local bucket under the test's
// temporary directory, in the unconditional mode if asked, and the test's
// context, which a cleanup cancels.
func sf91NewHarness(t *testing.T, unconditional bool) (*sf91Store, context.Context) {
	t.Helper()
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &sf91Store{inner: local, unconditional: unconditional}, ctx
}

// sf91Config is every mount's Config: the root directory owned by sf91Caller,
// no pruning and no wait on the write budget (ADR 0021 section 6), and no
// opt-in. A test that needs the opt-in or a read-only mount sets
// AllowUnconditionalRoot or ReadOnly on the value it returns.
func sf91Config(st store.Store) Config {
	return Config{
		Store:             st,
		ChunkSize:         4096,
		OwnerUID:          501,
		OwnerGID:          20,
		SnapshotRetention: -1,
		MaxDirtyBytes:     -1,
		Log:               quietLog(),
	}
}

// sf91TryNew runs New in sf91Go and waits for it under sf91Bound.
func sf91TryNew(t *testing.T, ctx context.Context, what string, cfg Config) sf91NewResult {
	t.Helper()
	return sf91Go(func() sf91NewResult {
		fs, err := New(ctx, cfg)
		return sf91NewResult{fs, err}
	}).wait(t, what)
}

// sf91MustNew is sf91TryNew for a New that must succeed.
func sf91MustNew(t *testing.T, ctx context.Context, what string, cfg Config) *FS {
	t.Helper()
	r := sf91TryNew(t, ctx, what, cfg)
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	if r.fs == nil {
		t.Fatalf("%s returned a nil *FS and a nil error", what)
	}
	return r.fs
}

// sf91TryCreate creates name in the root directory of fs, as sf91Caller, a
// regular file, exclusively, and returns what Create returned as its error.
func sf91TryCreate(t *testing.T, ctx context.Context, fs *FS, name string) error {
	t.Helper()
	return sf91Go(func() error {
		_, _, err := fs.Create(ctx, sf91Caller, fs.Root(), name, vfs.SetAttr{}, true)
		return err
	}).wait(t, fmt.Sprintf("Create %q in the root directory", name))
}

// sf91Sync runs fs.Sync and returns what it returned.
func sf91Sync(t *testing.T, ctx context.Context, fs *FS, what string) error {
	t.Helper()
	return sf91Go(func() error { return fs.Sync(ctx) }).wait(t, what)
}

// sf91Commit is ADR 0021 section 6's commit: a Create of a fresh name in the
// root directory, by sf91Caller, which must succeed, then Sync. It returns
// what Sync returned.
func sf91Commit(t *testing.T, ctx context.Context, fs *FS, name, what string) error {
	t.Helper()
	if err := sf91TryCreate(t, ctx, fs, name); err != nil {
		t.Fatalf("%s: Create %q in the root directory as its owner (%d, %d) = %v, want success", what, name,
			sf91Caller.UID, sf91Caller.GID, err)
	}
	return sf91Sync(t, ctx, fs, what)
}

// sf91Dirty reads FS.dirty holding FS.mu for reading (ADR 0021 section 6).
func sf91Dirty(t *testing.T, fs *FS, what string) bool {
	t.Helper()
	return sf91Go(func() bool {
		fs.mu.RLock()
		defer fs.mu.RUnlock()
		return fs.dirty
	}).wait(t, what)
}

// sf91CheckRefused requires err to be ADR 0021 section 2's refusal of a swap
// the store answered store.ErrUnsupported: non-nil, with errors.Is(err,
// store.ErrUnsupported), and with text that contains "unconditional".
func sf91CheckRefused(t *testing.T, err error, what, where string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s = nil, want an error: without the operator's opt-in a swap the store answers "+
			"store.ErrUnsupported fails the commit, and the root pointer is not written unconditionally "+
			"(ADR 0021 section 1, section 2; %s)", what, where)
		return
	}
	if !errors.Is(err, store.ErrUnsupported) {
		t.Errorf("%s = %v, want an error for which errors.Is(err, store.ErrUnsupported) holds (ADR 0021 "+
			"section 2, Assumption 8; %s)", what, err, where)
	}
	if !strings.Contains(err.Error(), "unconditional") {
		t.Errorf("%s: error %q, want its text to contain %q (ADR 0021 section 2, Assumption 8; %s)", what,
			err.Error(), "unconditional", where)
	}
}

// sf91CheckNoRootFallback requires the harness to have counted no Put and no
// Head of rootKey: the unconditional write and the Head that follows it at
// 247a276 (ADR 0021 section 2).
func sf91CheckNoRootFallback(t *testing.T, rec sf91Records, when, where string) {
	t.Helper()
	if n := len(rec.rootPuts); n != 0 {
		t.Errorf("%s the harness counted %d Put(s) of the root pointer %q, want none: a mount without the "+
			"operator's opt-in never writes the root pointer unconditionally, whatever the store answers "+
			"(ADR 0021 section 1, section 2; %s). Every PutIfMatch of %q, in call order: %v", when, n, rootKey,
			where, rootKey, rec.swaps)
	}
	if rec.rootHeads != 0 {
		t.Errorf("%s the harness counted %d Head(s) of the root pointer %q, want none: after a refused swap the "+
			"commit writes the root pointer no further, and in particular makes no Head of %q (ADR 0021 section "+
			"2; %s)", when, rec.rootHeads, rootKey, rootKey, where)
	}
}

// sf91CheckRootEntries requires, through local only, the root pointer to
// decode as rootPointer, the snapshot it names to decode with decodeSnapshot,
// and that snapshot's root directory to hold every name in present and none
// in absent.
func sf91CheckRootEntries(t *testing.T, ctx context.Context, local *store.Local, present, absent []string, where string) {
	t.Helper()
	raw, err := local.Get(ctx, rootKey)
	if err != nil {
		t.Errorf("Get(%q) on the inner store = %v, want the root pointer (%s)", rootKey, err, where)
		return
	}
	var rp rootPointer
	if err := json.Unmarshal(raw, &rp); err != nil {
		t.Errorf("the root pointer %q does not decode as rootPointer: %v (%s)", raw, err, where)
		return
	}
	snapRaw, err := local.Get(ctx, rp.Snapshot)
	if err != nil {
		t.Errorf("Get of the snapshot the root pointer names, %q, on the inner store = %v (%s)", rp.Snapshot,
			err, where)
		return
	}
	snap, err := decodeSnapshot(snapRaw)
	if err != nil {
		t.Errorf("decodeSnapshot of the snapshot the root pointer names, %q = %v (%s)", rp.Snapshot, err, where)
		return
	}
	if snap == nil {
		t.Errorf("decodeSnapshot of the snapshot the root pointer names, %q, returned a nil snapshot and a nil "+
			"error (%s)", rp.Snapshot, where)
		return
	}
	dir := snap.Inodes[rootInodeID]
	if dir == nil {
		t.Errorf("the snapshot the root pointer names, %q, has no root directory (Inodes[%d]) (%s)", rp.Snapshot,
			rootInodeID, where)
		return
	}
	for _, name := range present {
		if _, ok := dir.Entries[name]; !ok {
			t.Errorf("the root directory in the snapshot the root pointer names, %q, has no %q: an "+
				"acknowledged commit is no longer in the snapshot the root pointer names (NoLostAck; ADR 0021 "+
				"section 1; %s). Its entries: %v", rp.Snapshot, name, where, dir.Entries)
		}
	}
	for _, name := range absent {
		if _, ok := dir.Entries[name]; ok {
			t.Errorf("the root directory in the snapshot the root pointer names, %q, has %q: a commit whose "+
				"swap the store answered store.ErrUnsupported reached the root pointer (ADR 0021 section 1; "+
				"%s). Its entries: %v", rp.Snapshot, name, where, dir.Entries)
		}
	}
}

// sf91CheckRemount requires New on local, with SnapshotRetention -1, to
// succeed, Lookup of every name in present in its root directory to succeed,
// and Lookup of every name in absent to fail.
func sf91CheckRemount(t *testing.T, ctx context.Context, local *store.Local, present, absent []string, where string) {
	t.Helper()
	what := "the remount, New on the inner store with SnapshotRetention -1"
	r := sf91TryNew(t, ctx, what, sf91Config(local))
	if r.err != nil {
		t.Errorf("%s = %v, want success (%s)", what, r.err, where)
		return
	}
	if r.fs == nil {
		t.Errorf("%s returned a nil *FS and a nil error (%s)", what, where)
		return
	}
	fs := r.fs
	lookup := func(name string) error {
		return sf91Go(func() error {
			_, _, err := fs.Lookup(ctx, sf91Caller, fs.Root(), name)
			return err
		}).wait(t, fmt.Sprintf("Lookup of %q in the remount's root directory", name))
	}
	for _, name := range present {
		if err := lookup(name); err != nil {
			t.Errorf("Lookup of %q in the remount's root directory = %v, want success: that commit was "+
				"acknowledged and stands (ADR 0021 section 1; %s)", name, err, where)
		}
	}
	for _, name := range absent {
		if err := lookup(name); err == nil {
			t.Errorf("Lookup of %q in the remount's root directory succeeded, want it to fail: no commit "+
				"that holds %q reached the root pointer (ADR 0021 section 1; %s)", name, name, where)
		}
	}
}

// TestOne501AfterHonouredSwapsMakesNoUnconditionalRootWrite is F1 of ADR 0021
// section 6, the route test, from the counterexample TLC reported for
// docs/tla/CommitProtocol_Fallback.cfg at 247a276 (the route
// docs/tla/CommitProtocol_NoFallbackOptIn.cfg shows): one 501 on a swap, from
// a store that has honoured every conditional write, does not make a mount
// without the opt-in write the root pointer unconditionally. Another mount's
// acknowledged commit survives, and the next swap detects that mount.
//
// The trace's seven states, and the test's steps:
//   - state 1, both mounts loaded at epoch 1 (Start = "Loaded"): step 1, New
//     of A over the empty bucket, which initialises epoch 1 with a
//     conditional swap, then New of B, which loads epoch 1;
//   - states 2 to 4, A's Mutate, PutSnapshot [2,1] and SwapOK: step 2, A
//     commits a2 and Sync returns nil, so A's commit is acknowledged;
//   - states 5 to 7, B's Mutate, PutSnapshot [2,2] and SwapUnsupported: step
//     3, the one-shot 501 armed and B committing b2. At 247a276 the fallback
//     Put of state 7 overwrites the root pointer. With ADR 0021,
//     SwapUnsupported takes its ~FallbackAllowed branch, CommitFails: Sync
//     returns the refusal, and no Put and no Head of rootKey is made (step
//     4);
//   - NoLostAck, which state 7 violates: steps 5 and 6. B stays writable and
//     dirty, and the snapshot the root pointer names holds a2 and not b2;
//   - beyond the trace, steps 7 and 8: B's next Sync swaps against the same
//     ETag, which A's SwapOK made stale, so it fails its If-Match
//     (SwapConflict) and B diverges, and a remount finds a2 and not b2.
//
// Where it deviates from the trace: nonces come from crypto/rand, so the
// snapshot keys are not [2,1] and [2,2]; nothing here depends on them.
//
// At 247a276 step 3 fails: B's Sync returns nil, the harness counted one Put
// and one Head of rootKey, and the root pointer's snapshot holds b2 and not
// a2.
func TestOne501AfterHonouredSwapsMakesNoUnconditionalRootWrite(t *testing.T) {
	h, ctx := sf91NewHarness(t, false)

	// 1. A initialises the bucket at epoch 1; B loads epoch 1.
	a := sf91MustNew(t, ctx, "ADR 0021 section 6, F1 step 1: New of mount A over the empty bucket", sf91Config(h))
	b := sf91MustNew(t, ctx, "ADR 0021 section 6, F1 step 1: New of mount B on the same bucket", sf91Config(h))

	// 2. A commits a2: Mutate, PutSnapshot [2,1], SwapOK.
	if err := sf91Commit(t, ctx, a, "a2", "ADR 0021 section 6, F1 step 2: Sync of A's commit of a2"); err != nil {
		t.Fatalf("ADR 0021 section 6, F1 step 2: Sync of A's commit of a2 = %v, want success: the store honours "+
			"every conditional write, and nothing else has moved the root pointer since A wrote it", err)
	}

	// 3. One 501 on B's swap: Mutate, PutSnapshot [2,2], SwapUnsupported.
	mark := len(h.records().swaps)
	h.arm501()
	errB := sf91Commit(t, ctx, b, "b2", "ADR 0021 section 6, F1 step 3: Sync of B's commit of b2")
	sf91CheckRefused(t, errB, "Sync of B's commit of b2, whose swap the store answered store.ErrUnsupported once",
		"ADR 0021 section 6, F1 step 3")

	// 4. The harness's records, read before the bucket is inspected.
	rec := h.records()
	sf91CheckNoRootFallback(t, rec, "after B's commit of b2, in the whole test so far,", "ADR 0021 section 6, F1 step 4")
	if len(rec.swaps) <= mark {
		t.Errorf("B's Sync of b2 made no PutIfMatch of the root pointer %q (the harness saw %d, all before it: "+
			"%v), want its swap, answered store.ErrUnsupported (ADR 0021 section 6, F1 step 4)", rootKey,
			len(rec.swaps), rec.swaps)
	} else if last := rec.swaps[len(rec.swaps)-1]; !last.answered501 {
		t.Errorf("the last PutIfMatch of the root pointer %q is %v, want B's, answered store.ErrUnsupported by "+
			"the harness (ADR 0021 section 6, F1 step 4). Every PutIfMatch of %q, in call order: %v", rootKey,
			last, rootKey, rec.swaps)
	}

	// 5. B has not diverged, and its namespace is still dirty.
	if err := sf91TryCreate(t, ctx, b, "b3"); err != nil {
		t.Errorf("Create %q on B after its refused swap = %v, want success: a swap the store answered "+
			"store.ErrUnsupported fails the commit and leaves the mount writable, not diverged (ADR 0021 "+
			"section 1, section 5, Assumption 4; section 6, F1 step 5)", "b3", err)
	}
	if !sf91Dirty(t, b, "reading B's FS.dirty") {
		t.Errorf("B's FS.dirty is false after its refused swap, want true: a commit whose swap fails leaves the " +
			"namespace dirty for the next commit (ADR 0021 section 2; section 6, F1 step 5)")
	}

	// 6. Through the inner store: A's acknowledged commit survives.
	sf91CheckRootEntries(t, ctx, h.inner, []string{"a2"}, []string{"b2"}, "ADR 0021 section 6, F1 step 6")

	// 7. B's next Sync, with the store honouring conditions: SwapConflict.
	if err := sf91Sync(t, ctx, b, "ADR 0021 section 6, F1 step 7: Sync on B again"); err == nil {
		t.Errorf("ADR 0021 section 6, F1 step 7: Sync on B again, with the store honouring conditions, = nil, "+
			"want an error: B swaps against the ETag it loaded, which A's commit made stale, so its If-Match "+
			"fails and the second writer is detected (ADR 0021 section 3; docs/DESIGN.md section 5). Every "+
			"PutIfMatch of %q, in call order: %v", rootKey, h.records().swaps)
	}
	if err := sf91TryCreate(t, ctx, b, "b4"); !errors.Is(err, vfs.ErrStale) {
		t.Errorf("Create %q on B after its swap failed its If-Match = %v, want vfs.ErrStale: a mount whose swap "+
			"failed its condition has diverged (ADR 0003 section 4; ADR 0021 section 6, F1 step 7)", "b4", err)
	}
	rec = h.records()
	sf91CheckNoRootFallback(t, rec, "after B's second Sync, in the whole test,", "ADR 0021 section 6, F1 step 7")

	// 8. A remount finds a2 and not b2.
	sf91CheckRemount(t, ctx, h.inner, []string{"a2"}, []string{"b2"}, "ADR 0021 section 6, F1 step 8")
}

// TestNewOnAStoreWithoutConditionalWritesIsRefused is F2 of ADR 0021 section
// 6: New over an empty bucket, on a store that answers every PutIfMatch
// store.ErrUnsupported, without the opt-in, is refused, since initEmpty's
// commit fails at its swap (ADR 0021 section 3, the first case). New returns
// a nil *FS and the refusal of section 2, the harness counted no Put of
// rootKey, and the bucket holds no root pointer. An *FS that New returns here
// is never used (ADR 0011 section 7).
//
// In the model: InitEmpty, PutSnapshot [1,n] and SwapUnsupported under
// Capability "Unconditional", whose ~FallbackAllowed branch is CommitFails
// at epoch 1, the "Failed" that makes New fail.
//
// At 247a276 New returns an *FS and the bucket holds a root pointer.
func TestNewOnAStoreWithoutConditionalWritesIsRefused(t *testing.T) {
	h, ctx := sf91NewHarness(t, true)

	what := "New, without the opt-in, over an empty bucket on a store that answers every PutIfMatch " +
		"store.ErrUnsupported"
	r := sf91TryNew(t, ctx, what, sf91Config(h))
	rec := h.records()

	if r.fs != nil {
		t.Errorf("%s returned a non-nil *FS, want nil: initEmpty's commit fails at its swap, so New fails (ADR "+
			"0021 section 3; section 6, F2). The FS is not used", what)
	}
	sf91CheckRefused(t, r.err, what, "ADR 0021 section 6, F2")
	if n := len(rec.rootPuts); n != 0 {
		t.Errorf("%s: the harness counted %d Put(s) of the root pointer %q, want none: without the opt-in the "+
			"root pointer is never written unconditionally (ADR 0021 section 1, section 2; section 6, F2)", what,
			n, rootKey)
	}

	// Through the inner store, after the records were read.
	if _, err := h.inner.Get(ctx, rootKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after %s, Get(%q) on the inner store = %v, want store.ErrNotFound: a refused New leaves no "+
			"root pointer (ADR 0021 section 3; section 6, F2)", what, rootKey, err)
	}
}

// TestCommitOnAStoreWithoutConditionalWritesFailsWithoutOptIn is F3 of ADR
// 0021 section 6: on a bucket that holds a filesystem, a mount without the
// opt-in on a store that answers every PutIfMatch store.ErrUnsupported mounts,
// since loading writes nothing, but every commit fails at its swap and leaves
// the root pointer as it was; the mount stays writable and dirty; and a
// second reply decides nothing more than the first (ADR 0021 section 1,
// section 3 the first case, Assumption 4).
//
// In the model: Mutate, PutSnapshot and SwapUnsupported under Capability
// "Unconditional", twice, each taking the ~FallbackAllowed branch,
// CommitFails, with no FallbackPut and no FallbackHead.
//
// The root pointer's bytes are read from the inner store after the seeding
// commit and before the harness's mount, when the harness has received no
// call, so that the bucket is inspected only once the harness's records, then
// empty, could have been read; loading writes nothing (ADR 0021 section 6,
// the first fact), so they are the bytes the harness's mount loads.
//
// At 247a276 the first Sync returns nil through a Put and a Head of rootKey,
// and the remount has c2.
func TestCommitOnAStoreWithoutConditionalWritesFailsWithoutOptIn(t *testing.T) {
	h, ctx := sf91NewHarness(t, true)

	// Seed a filesystem on the inner store, with one commit, c1.
	seed := sf91MustNew(t, ctx, "setup: New on the inner store over an empty bucket", sf91Config(h.inner))
	if err := sf91Commit(t, ctx, seed, "c1", "setup: Sync of the seeding commit of c1"); err != nil {
		t.Fatalf("setup: Sync of the seeding commit of c1 on the inner store = %v, want success", err)
	}
	rootBefore, err := h.inner.Get(ctx, rootKey)
	if err != nil {
		t.Fatalf("setup: Get(%q) on the inner store after the seeding commit = %v, want the root pointer",
			rootKey, err)
	}

	fs := sf91MustNew(t, ctx, "ADR 0021 section 6, F3: New, without the opt-in, on the harness in its "+
		"unconditional mode, over the seeded bucket", sf91Config(h))

	checkRoot := func(when string) {
		t.Helper()
		rootNow, err := h.inner.Get(ctx, rootKey)
		if err != nil {
			t.Errorf("%s, Get(%q) on the inner store = %v, want the root pointer as it was (ADR 0021 section 6, "+
				"F3)", when, rootKey, err)
		} else if !bytes.Equal(rootNow, rootBefore) {
			t.Errorf("%s the root pointer is %q, want it as it was, %q: a refused swap writes nothing more (ADR "+
				"0021 section 2; section 6, F3)", when, rootNow, rootBefore)
		}
	}

	// The first commit, c2: refused.
	err = sf91Commit(t, ctx, fs, "c2", "ADR 0021 section 6, F3: Sync of the commit of c2")
	sf91CheckRefused(t, err, "Sync of the commit of c2, on a store that answers every PutIfMatch "+
		"store.ErrUnsupported", "ADR 0021 section 6, F3, the first Sync")
	sf91CheckNoRootFallback(t, h.records(), "after the first Sync,", "ADR 0021 section 6, F3, the first Sync")
	checkRoot("after the first Sync")

	// The mount has not diverged, and its namespace is still dirty.
	if err := sf91TryCreate(t, ctx, fs, "c3"); err != nil {
		t.Errorf("Create %q after the refused swap = %v, want success: a swap the store answered "+
			"store.ErrUnsupported fails the commit and leaves the mount writable, not diverged (ADR 0021 "+
			"section 1, section 5, Assumption 4; section 6, F3)", "c3", err)
	}
	if !sf91Dirty(t, fs, "reading FS.dirty") {
		t.Errorf("FS.dirty is false after the refused swap, want true: a commit whose swap fails leaves the " +
			"namespace dirty for the next commit (ADR 0021 section 2; section 6, F3)")
	}

	// Sync again: the same refusal, and still nothing written.
	err = sf91Sync(t, ctx, fs, "ADR 0021 section 6, F3: Sync again")
	sf91CheckRefused(t, err, "Sync again, on a store that answers every PutIfMatch store.ErrUnsupported",
		"ADR 0021 section 6, F3, the second Sync: a second reply decides nothing more than the first")
	sf91CheckNoRootFallback(t, h.records(), "after the second Sync,", "ADR 0021 section 6, F3, the second Sync")
	checkRoot("after the second Sync")

	// A remount on the inner store has c1, and neither c2 nor c3.
	sf91CheckRemount(t, ctx, h.inner, []string{"c1"}, []string{"c2", "c3"}, "ADR 0021 section 6, F3")
}

// sf91ETag is *store.Local's ETag for body: the lowercase hexadecimal MD5 of
// it (ADR 0021 section 6, the facts; ADR 0020 section 6).
func sf91ETag(body []byte) string {
	sum := md5.Sum(body)
	return hex.EncodeToString(sum[:])
}

// sf91CheckRootFallbacks requires the harness to have counted exactly want
// Puts and want Heads of rootKey: the opted-in fallback's unconditional write
// and the Head after it, once per commit that took it (ADR 0021 section 2).
func sf91CheckRootFallbacks(t *testing.T, rec sf91Records, want int, when, where string) {
	t.Helper()
	if n := len(rec.rootPuts); n != want {
		t.Errorf("%s the harness counted %d Put(s) of the root pointer %q, want %d: with the operator's opt-in a "+
			"swap the store answers store.ErrUnsupported is followed by the unconditional write, as at 247a276 "+
			"(ADR 0021 section 1, section 2; %s). Every PutIfMatch of %q, in call order: %v", when, n, rootKey,
			want, where, rootKey, rec.swaps)
	}
	if rec.rootHeads != want {
		t.Errorf("%s the harness counted %d Head(s) of the root pointer %q, want %d: with the operator's opt-in "+
			"the unconditional write is followed by the Head, as at 247a276 (ADR 0021 section 2; %s)", when,
			rec.rootHeads, rootKey, want, where)
	}
}

// TestOptInRestoresTheUnconditionalFallback is O1 of ADR 0021 section 6: on a
// store that answers every PutIfMatch store.ErrUnsupported, a mount with
// Config.AllowUnconditionalRoot behaves as at 247a276 (section 3, the second
// case). New over an empty bucket succeeds through one unconditional Put and
// one Head of rootKey; a commit, c2, succeeds through a second of each; the
// root pointer on the inner store names a snapshot whose root directory holds
// c2; and a remount has c2.
//
// In the model: docs/tla/CommitProtocol_UnconditionalSingle.cfg, without
// FallbackOptIn, which reads as one opted-in writer (ADR 0021 section 4):
// SwapUnsupported's FallbackAllowed branch, FallbackPut, then FallbackHead
// and Committed, for the init commit and again for c2.
func TestOptInRestoresTheUnconditionalFallback(t *testing.T) {
	h, ctx := sf91NewHarness(t, true)
	cfg := sf91Config(h)
	cfg.AllowUnconditionalRoot = true

	fs := sf91MustNew(t, ctx, "ADR 0021 section 6, O1: New with AllowUnconditionalRoot over an empty bucket, on "+
		"a store that answers every PutIfMatch store.ErrUnsupported", cfg)
	sf91CheckRootFallbacks(t, h.records(), 1, "after New,", "ADR 0021 section 6, O1, the init commit")

	if err := sf91Commit(t, ctx, fs, "c2", "ADR 0021 section 6, O1: Sync of the commit of c2"); err != nil {
		t.Errorf("ADR 0021 section 6, O1: Sync of the commit of c2, with AllowUnconditionalRoot, = %v, want "+
			"success: with the opt-in a swap answered store.ErrUnsupported is followed by the unconditional "+
			"write and the Head, and the commit succeeds (ADR 0021 section 1, section 2)", err)
	}
	sf91CheckRootFallbacks(t, h.records(), 2, "after the commit of c2,", "ADR 0021 section 6, O1, the commit of c2")

	// Through the inner store, after the records were read.
	sf91CheckRootEntries(t, ctx, h.inner, []string{"c2"}, nil, "ADR 0021 section 6, O1")
	sf91CheckRemount(t, ctx, h.inner, []string{"c2"}, nil, "ADR 0021 section 6, O1")
}

// TestOptInFallsBackAfterOne501 is O2 of ADR 0021 section 6: a mount with
// Config.AllowUnconditionalRoot on a store that honours conditional writes
// and answers one swap 501 takes the fallback for that commit only, and
// adopts the ETag its Head returned. New over an empty bucket initialises
// conditionally; with the one-shot 501 armed, a commit of c2 succeeds through
// exactly one Put and one Head of rootKey; a commit of c3, with the store
// honouring conditions, succeeds with no further Put or Head, and its swap
// passed through with an etag argument equal to *store.Local's ETag of the
// root pointer body the fallback Put wrote, the lowercase hexadecimal MD5 of
// it.
//
// In the model: CommitProtocol_NoFallbackOptIn read as opted-in writers
// (ADR 0021 section 4) with one writer: SwapUnsupported's FallbackAllowed
// branch, FallbackPut and FallbackHead, whose Committed keeps the Head's
// ETag, then SwapOK against it.
func TestOptInFallsBackAfterOne501(t *testing.T) {
	h, ctx := sf91NewHarness(t, false)
	cfg := sf91Config(h)
	cfg.AllowUnconditionalRoot = true

	fs := sf91MustNew(t, ctx, "ADR 0021 section 6, O2: New with AllowUnconditionalRoot over an empty bucket", cfg)

	// One 501 on the swap of c2: the fallback.
	h.arm501()
	if err := sf91Commit(t, ctx, fs, "c2", "ADR 0021 section 6, O2: Sync of the commit of c2"); err != nil {
		t.Errorf("ADR 0021 section 6, O2: Sync of the commit of c2, its swap answered store.ErrUnsupported once, "+
			"with AllowUnconditionalRoot, = %v, want success through the fallback (ADR 0021 section 1, section 2)",
			err)
	}
	sf91CheckRootFallbacks(t, h.records(), 1, "after the commit of c2,", "ADR 0021 section 6, O2, the commit of c2")

	// c3, with the store honouring conditions: a conditional swap against the
	// Head's ETag.
	mark := len(h.records().swaps)
	if err := sf91Commit(t, ctx, fs, "c3", "ADR 0021 section 6, O2: Sync of the commit of c3"); err != nil {
		t.Errorf("ADR 0021 section 6, O2: Sync of the commit of c3, with the store honouring conditions, = %v, "+
			"want success: its swap is conditioned on the ETag the fallback's Head returned, which is the root "+
			"pointer's (ADR 0021 section 2, Assumption 5)", err)
	}
	rec := h.records()
	sf91CheckRootFallbacks(t, rec, 1, "after the commit of c3,", "ADR 0021 section 6, O2, the commit of c3")

	if len(rec.rootPuts) == 0 {
		t.Errorf("the harness kept no Put of the root pointer %q, so the ETag adopted from the fallback's Head "+
			"cannot be checked (ADR 0021 section 6, O2)", rootKey)
		return
	}
	want := sf91ETag(rec.rootPuts[len(rec.rootPuts)-1])
	if len(rec.swaps) <= mark {
		t.Errorf("the commit of c3 made no PutIfMatch of the root pointer %q (the harness saw %d, all before it: "+
			"%v), want its swap (ADR 0021 section 6, O2)", rootKey, len(rec.swaps), rec.swaps)
	} else if last := rec.swaps[len(rec.swaps)-1]; last.answered501 || last.etag != want {
		t.Errorf("the last PutIfMatch of the root pointer %q is %v, want one passed through with the etag "+
			"argument %q, the lowercase hexadecimal MD5 of the root pointer body the fallback Put wrote: the "+
			"Head's ETag is adopted (ADR 0021 section 2; section 6, O2). Every PutIfMatch of %q, in call "+
			"order: %v", rootKey, last, want, rootKey, rec.swaps)
	}
}

// TestOptInOnAReadOnlyMountWritesNothing is O3 of ADR 0021 section 6: with
// Config.ReadOnly and Config.AllowUnconditionalRoot both set, New over a
// bucket that holds a filesystem succeeds (ADR 0021 section 3; Assumption
// 11), and a Sync of a mount whose FS.dirty a test set true, holding FS.mu
// for writing (ADR 0011 section 7), makes no Put, Delete or PutIfMatch of any
// key and leaves the root pointer's bytes as they were: the opt-in has
// nothing to allow on a read-only mount. What Sync returns is not pinned.
//
// In the model: a fenced mount's SwapUnsupported branch, or its PutSnapshot
// failing first with ChunksFirst, neither of which reaches the bucket.
//
// The root pointer's bytes are read from the inner store after the seeding
// mount and before the harness's mount, when the harness has received no
// call, as in F3.
func TestOptInOnAReadOnlyMountWritesNothing(t *testing.T) {
	h, ctx := sf91NewHarness(t, false)

	sf91MustNew(t, ctx, "setup: New on the inner store over an empty bucket, to seed a filesystem",
		sf91Config(h.inner))
	rootBefore, err := h.inner.Get(ctx, rootKey)
	if err != nil {
		t.Fatalf("setup: Get(%q) on the inner store after the seeding mount = %v, want the root pointer",
			rootKey, err)
	}

	cfg := sf91Config(h)
	cfg.ReadOnly = true
	cfg.AllowUnconditionalRoot = true
	fs := sf91MustNew(t, ctx, "ADR 0021 section 6, O3: New with ReadOnly and AllowUnconditionalRoot over the "+
		"seeded bucket", cfg)

	sf91Go(func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		fs.dirty = true
		return true
	}).wait(t, "setting FS.dirty true holding FS.mu for writing")

	if err := sf91Sync(t, ctx, fs, "ADR 0021 section 6, O3: Sync of the dirty read-only mount"); err != nil {
		t.Logf("Sync of the dirty read-only mount returned %v; what it returns is not pinned (ADR 0021 "+
			"section 6, O3)", err)
	}

	rec := h.records()
	for _, c := range []struct {
		method string
		n      int
	}{{"Put", rec.puts}, {"Delete", rec.deletes}, {"PutIfMatch", rec.putIfMatches}} {
		if c.n != 0 {
			t.Errorf("the harness counted %d %s call(s) on a read-only mount with AllowUnconditionalRoot, want "+
				"none: a read-only mount writes nothing, and the opt-in has nothing to allow (ADR 0011 section "+
				"3; ADR 0021 section 3, Assumption 11; section 6, O3)", c.n, c.method)
		}
	}

	// Through the inner store, after the records were read.
	rootAfter, err := h.inner.Get(ctx, rootKey)
	if err != nil {
		t.Errorf("Get(%q) on the inner store after the Sync = %v, want the root pointer as it was (ADR 0021 "+
			"section 6, O3)", rootKey, err)
	} else if !bytes.Equal(rootAfter, rootBefore) {
		t.Errorf("after the Sync of the read-only mount the root pointer is %q, want it as it was, %q (ADR 0011 "+
			"section 3; ADR 0021 section 6, O3)", rootAfter, rootBefore)
	}
}

// TestFallbackAllowedFollowsConfig is O4 of ADR 0021 section 6:
// (*FS).fallbackAllowed reports Config.AllowUnconditionalRoot, which New
// copies into the FS (ADR 0021 section 2). New on a plain *store.Local with
// the field false gives false, and with it true gives true.
//
// In the model: FallbackAllowed, which docs/tla/trace.tsv maps to
// FS.fallbackAllowed.
func TestFallbackAllowedFollowsConfig(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprintf("AllowUnconditionalRoot=%v", allow), func(t *testing.T) {
			local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
			if err != nil {
				t.Fatalf("setup: store.NewLocal: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			cfg := sf91Config(local)
			cfg.AllowUnconditionalRoot = allow
			what := fmt.Sprintf("ADR 0021 section 6, O4: New on a plain *store.Local with AllowUnconditionalRoot %v",
				allow)
			fs := sf91MustNew(t, ctx, what, cfg)
			got := sf91Go(func() bool { return fs.fallbackAllowed() }).wait(t, "fallbackAllowed()")
			if got != allow {
				t.Errorf("fallbackAllowed() on a mount whose Config has AllowUnconditionalRoot %v = %v, want %v: it "+
					"reports the operator's opt-in and nothing else (ADR 0021 section 2; section 6, O4)", allow, got,
					allow)
			}
		})
	}
}

// sf91SnapPutsOf returns the keys of the Puts under snapshotPrefix in events.
func sf91SnapPutsOf(events []sf91Event) []string {
	var keys []string
	for _, e := range events {
		if e.op == "Put" {
			keys = append(keys, e.key)
		}
	}
	return keys
}

// sf91DeletesOf returns the Deletes in events.
func sf91DeletesOf(events []sf91Event) []sf91Event {
	var dels []sf91Event
	for _, e := range events {
		if e.op == "Delete" {
			dels = append(dels, e)
		}
	}
	return dels
}

// sf91SnapshotKeys lists, through local only, the keys under snapshotPrefix,
// sorted.
func sf91SnapshotKeys(t *testing.T, ctx context.Context, local *store.Local, when string) []string {
	t.Helper()
	infos, err := local.List(ctx, snapshotPrefix, "", 0)
	if err != nil {
		t.Fatalf("%s, List(%q) on the inner store = %v", when, snapshotPrefix, err)
	}
	keys := make([]string, 0, len(infos))
	for _, info := range infos {
		keys = append(keys, info.Key)
	}
	slices.Sort(keys)
	return keys
}

// sf91CheckSnapshotKeys requires, through local only, the keys under
// snapshotPrefix to be exactly want: a refused commit leaves no orphan
// (ADR 0021 section 2, the reclaim).
func sf91CheckSnapshotKeys(t *testing.T, ctx context.Context, local *store.Local, want []string, when, where string) {
	t.Helper()
	if got := sf91SnapshotKeys(t, ctx, local, when); !slices.Equal(got, want) {
		t.Errorf("%s the inner store's keys under %q are %q, want them as recorded before the commit, %q: a "+
			"refused commit reclaims the snapshot it uploaded, so the bucket does not grow (ADR 0021 section 2, "+
			"the reclaim; Assumption 21; %s)", when, snapshotPrefix, got, want, where)
	}
}

// sf91CheckRootBytes requires, through local only, the root pointer's bytes to
// be before.
func sf91CheckRootBytes(t *testing.T, ctx context.Context, local *store.Local, before []byte, when, where string) {
	t.Helper()
	now, err := local.Get(ctx, rootKey)
	if err != nil {
		t.Errorf("%s, Get(%q) on the inner store = %v, want the root pointer as it was (%s)", when, rootKey, err,
			where)
	} else if !bytes.Equal(now, before) {
		t.Errorf("%s the root pointer is %q, want it as it was, %q: a refused commit writes the root pointer no "+
			"further, and the reclaim never writes it (ADR 0021 section 2; %s)", when, now, before, where)
	}
}

// sf91CheckReclaim requires events, the harness's log of one refused commit,
// to be exactly one Put under snapshotPrefix, of a key K, then one Get of
// rootKey, then one Delete, of K, passed through (ADR 0021 section 2, the
// reclaim, steps 1 and 2). It returns K, or "" if events holds no Put.
func sf91CheckReclaim(t *testing.T, events []sf91Event, what, where string) string {
	t.Helper()
	var key string
	if puts := sf91SnapPutsOf(events); len(puts) > 0 {
		key = puts[0]
	}
	want := []sf91Event{{op: "Put", key: key}, {op: "Get", key: rootKey}, {op: "Delete", key: key}}
	if key == "" || !slices.Equal(events, want) {
		t.Errorf("%s: the harness logged %v, want exactly %v: the commit's one Put of its snapshot under %q, "+
			"then, its swap refused, one Get of the root pointer %q, which names another key, then one Delete "+
			"of the snapshot's key (ADR 0021 section 2, the reclaim; %s)", what, events, want, snapshotPrefix,
			rootKey, where)
	}
	return key
}

// sf91Seed seeds a filesystem with one commit, of name, through local, with a
// mount of its own, and returns the root pointer's bytes after it.
func sf91Seed(t *testing.T, ctx context.Context, local *store.Local, name string) []byte {
	t.Helper()
	seed := sf91MustNew(t, ctx, "setup: New on the inner store over an empty bucket", sf91Config(local))
	if err := sf91Commit(t, ctx, seed, name, "setup: Sync of the seeding commit of "+name); err != nil {
		t.Fatalf("setup: Sync of the seeding commit of %s on the inner store = %v, want success", name, err)
	}
	root, err := local.Get(ctx, rootKey)
	if err != nil {
		t.Fatalf("setup: Get(%q) on the inner store after the seeding commit = %v, want the root pointer",
			rootKey, err)
	}
	return root
}

// TestRefusedCommitReclaimsItsSnapshot is G1 of ADR 0021 section 6: a commit
// whose swap the store answers store.ErrUnsupported, on a mount without the
// opt-in, reclaims the snapshot it uploaded, after one read of the root
// pointer shows that it names another key (ADR 0021 section 2, the reclaim;
// Assumptions 21 and 22). F3's setup: a filesystem seeded with c1 on the
// inner store, then New on the harness in its unconditional mode. Each of two
// refused commits logs exactly one Put under snapshotPrefix, of a key K, one
// Get of rootKey and one Delete, of K, and leaves the keys under
// snapshotPrefix as they were before the first and the root pointer's bytes
// unchanged. The second's key is new. A remount has c1 only.
//
// In the model: SwapUnsupported's refusal branch with FallbackOptIn, which
// folds the 501, the read and the Delete into one action (R12).
//
// The root pointer's bytes are read after the seeding commit and before the
// harness's mount, as in F3; the keys under snapshotPrefix after the
// harness's records have been read.
//
// At 53abeb7 no Get and no Delete is made, and the listing grows by one key
// per Sync.
func TestRefusedCommitReclaimsItsSnapshot(t *testing.T) {
	h, ctx := sf91NewHarness(t, true)
	rootBefore := sf91Seed(t, ctx, h.inner, "c1")

	fs := sf91MustNew(t, ctx, "ADR 0021 section 6, G1: New, without the opt-in, on the harness in its "+
		"unconditional mode, over the seeded bucket", sf91Config(h))
	mark := len(h.records().events)
	keysBefore := sf91SnapshotKeys(t, ctx, h.inner, "before the first commit")

	// The first commit, c2: refused, and its snapshot reclaimed.
	err := sf91Commit(t, ctx, fs, "c2", "ADR 0021 section 6, G1: Sync of the commit of c2")
	sf91CheckRefused(t, err, "Sync of the commit of c2, on a store that answers every PutIfMatch "+
		"store.ErrUnsupported", "ADR 0021 section 6, G1, the first Sync")
	rec := h.records()
	k1 := sf91CheckReclaim(t, rec.events[mark:], "the first Sync, since the mount",
		"ADR 0021 section 6, G1, the first Sync")
	sf91CheckSnapshotKeys(t, ctx, h.inner, keysBefore, "after the first Sync", "ADR 0021 section 6, G1")
	sf91CheckRootBytes(t, ctx, h.inner, rootBefore, "after the first Sync", "ADR 0021 section 6, G1")

	// Sync again: a new key, reclaimed the same way.
	mark = len(rec.events)
	err = sf91Sync(t, ctx, fs, "ADR 0021 section 6, G1: Sync again")
	sf91CheckRefused(t, err, "Sync again, on a store that answers every PutIfMatch store.ErrUnsupported",
		"ADR 0021 section 6, G1, the second Sync")
	rec = h.records()
	k2 := sf91CheckReclaim(t, rec.events[mark:], "the second Sync", "ADR 0021 section 6, G1, the second Sync")
	if k1 != "" && k2 == k1 {
		t.Errorf("the second Sync put its snapshot under %q, the first Sync's key, want a new key: each attempt "+
			"writes a new snapshot (ADR 0021 section 6, G1)", k2)
	}
	sf91CheckSnapshotKeys(t, ctx, h.inner, keysBefore, "after the second Sync", "ADR 0021 section 6, G1")

	sf91CheckRemount(t, ctx, h.inner, []string{"c1"}, []string{"c2"}, "ADR 0021 section 6, G1")
}

// TestRefusedInitLeavesTheBucketEmpty is G2 of ADR 0021 section 6: New over
// an empty bucket on a store that answers every PutIfMatch
// store.ErrUnsupported, without the opt-in, is refused as in F2, and the
// snapshot initEmpty's commit uploaded is reclaimed, so the bucket is left as
// it was, empty (ADR 0021 section 2, the reclaim; section 3, the first case).
// New returns a nil *FS and F2's error; the harness logged one Delete, of the
// key of its one Put under snapshotPrefix; and the inner store's List("", "",
// 0) returns no object. An *FS that New returns here is never used.
//
// At 53abeb7 the snapshot is left behind and no Delete is made.
func TestRefusedInitLeavesTheBucketEmpty(t *testing.T) {
	h, ctx := sf91NewHarness(t, true)

	what := "New, without the opt-in, over an empty bucket on a store that answers every PutIfMatch " +
		"store.ErrUnsupported"
	r := sf91TryNew(t, ctx, what, sf91Config(h))
	rec := h.records()

	if r.fs != nil {
		t.Errorf("%s returned a non-nil *FS, want nil: initEmpty's commit fails at its swap, so New fails (ADR "+
			"0021 section 3; section 6, G2). The FS is not used", what)
	}
	sf91CheckRefused(t, r.err, what, "ADR 0021 section 6, G2")
	if len(rec.snapPuts) != 1 {
		t.Errorf("%s: the harness logged the Puts under %q %q, want exactly one, initEmpty's snapshot (ADR 0021 "+
			"section 6, G2). Its log: %v", what, snapshotPrefix, rec.snapPuts, rec.events)
	} else if want := []sf91Event{{op: "Delete", key: rec.snapPuts[0]}}; !slices.Equal(rec.deleted, want) {
		t.Errorf("%s: the harness logged the Deletes %v, want exactly %v, of the key of its one Put under %q: "+
			"the refused init commit reclaims its snapshot (ADR 0021 section 2, the reclaim; section 6, G2). "+
			"Its log: %v", what, rec.deleted, want, snapshotPrefix, rec.events)
	}

	// Through the inner store, after the records were read.
	infos, err := h.inner.List(ctx, "", "", 0)
	if err != nil {
		t.Errorf("after %s, List(\"\", \"\", 0) on the inner store = %v, want no object (ADR 0021 section 6, G2)",
			what, err)
	} else if len(infos) != 0 {
		keys := make([]string, 0, len(infos))
		for _, info := range infos {
			keys = append(keys, info.Key)
		}
		t.Errorf("after %s the inner store holds %q, want no object: the refused init leaves the bucket as it "+
			"was (ADR 0021 section 3; section 6, G2)", what, keys)
	}
}

// TestRefusedCommitKeepsASnapshotTheRootPointerNames is G3 of ADR 0021
// section 6, the reclaim's guard: when the store applies a swap it answers
// store.ErrUnsupported, the root pointer names the refused commit's snapshot,
// and the reclaim's read of it sees so and deletes nothing (ADR 0021 section
// 2, the reclaim, step 3; Assumption 22). A filesystem is seeded with c1 on
// the inner store; New on the harness, not in its unconditional mode, without
// the opt-in; apply-then-501 armed; a commit of c2 returns F3's error; the
// harness logged no Delete; the root pointer on the inner store names the
// key of the last Put under snapshotPrefix, and that object exists; and a
// remount has c1 and c2. What the mount does next is #5's, and is not
// asserted.
//
// It is green at 53abeb7, which makes no Delete, and must stay green.
func TestRefusedCommitKeepsASnapshotTheRootPointerNames(t *testing.T) {
	h, ctx := sf91NewHarness(t, false)
	sf91Seed(t, ctx, h.inner, "c1")

	fs := sf91MustNew(t, ctx, "ADR 0021 section 6, G3: New, without the opt-in, on the harness over the "+
		"seeded bucket", sf91Config(h))
	h.armApplyThen501()
	err := sf91Commit(t, ctx, fs, "c2", "ADR 0021 section 6, G3: Sync of the commit of c2")
	sf91CheckRefused(t, err, "Sync of the commit of c2, whose swap the store applied and answered "+
		"store.ErrUnsupported", "ADR 0021 section 6, G3")

	rec := h.records()
	if len(rec.deleted) != 0 {
		t.Errorf("the harness logged the Deletes %v, want none: the root pointer names the refused commit's "+
			"snapshot, so the reclaim must not delete it (ADR 0021 section 2, the reclaim, step 3; Assumption "+
			"22; section 6, G3). Its log: %v", rec.deleted, rec.events)
	}
	if len(rec.snapPuts) == 0 {
		t.Fatalf("the harness logged no Put under %q, so the commit of c2 uploaded no snapshot (ADR 0021 "+
			"section 6, G3)", snapshotPrefix)
	}
	last := rec.snapPuts[len(rec.snapPuts)-1]

	// Through the inner store, after the records were read.
	raw, err := h.inner.Get(ctx, rootKey)
	if err != nil {
		t.Errorf("Get(%q) on the inner store = %v, want the root pointer (ADR 0021 section 6, G3)", rootKey, err)
	} else {
		var rp rootPointer
		if err := json.Unmarshal(raw, &rp); err != nil {
			t.Errorf("the root pointer %q does not decode as rootPointer: %v (ADR 0021 section 6, G3)", raw, err)
		} else if rp.Snapshot != last {
			t.Errorf("the root pointer names %q, want %q, the key of the last Put under %q: the store applied "+
				"the swap it answered store.ErrUnsupported (ADR 0021 section 6, G3)", rp.Snapshot, last,
				snapshotPrefix)
		}
	}
	if _, err := h.inner.Head(ctx, last); err != nil {
		t.Errorf("Head(%q) on the inner store = %v, want the snapshot the root pointer names to exist: the "+
			"reclaim never deletes a key the root pointer names (ADR 0021 section 2, the reclaim, step 3; "+
			"section 6, G3)", last, err)
	}
	sf91CheckRemount(t, ctx, h.inner, []string{"c1", "c2"}, nil, "ADR 0021 section 6, G3")
}

// TestRefusedCommitIgnoresAFailedReclaim is G4 of ADR 0021 section 6: a
// reclaim whose Delete fails leaves the orphan and changes nothing else; the
// commit still returns the refusal (ADR 0021 section 2, the reclaim, step 2;
// the module's J14). G1's setup, with fail-next-Delete armed before the
// commit of c2: Sync returns F3's error, wrapping store.ErrUnsupported with
// "unconditional" in its text; the harness logged one Delete, of K, the key
// of the commit's one Put under snapshotPrefix, which it failed; the inner
// store still holds K; and the root pointer's bytes are unchanged.
//
// At 53abeb7 it fails on the Delete count: no Delete is made.
func TestRefusedCommitIgnoresAFailedReclaim(t *testing.T) {
	h, ctx := sf91NewHarness(t, true)
	rootBefore := sf91Seed(t, ctx, h.inner, "c1")

	fs := sf91MustNew(t, ctx, "ADR 0021 section 6, G4: New, without the opt-in, on the harness in its "+
		"unconditional mode, over the seeded bucket", sf91Config(h))
	mark := len(h.records().events)
	h.armFailDelete()
	err := sf91Commit(t, ctx, fs, "c2", "ADR 0021 section 6, G4: Sync of the commit of c2")
	sf91CheckRefused(t, err, "Sync of the commit of c2, whose reclaim's Delete fails", "ADR 0021 section 6, G4")

	rec := h.records()
	since := rec.events[mark:]
	puts := sf91SnapPutsOf(since)
	if len(puts) != 1 {
		t.Fatalf("since the mount the harness logged the Puts under %q %q, want exactly one, the commit's "+
			"snapshot (ADR 0021 section 6, G4). Its log: %v", snapshotPrefix, puts, since)
	}
	k := puts[0]
	if dels, want := sf91DeletesOf(since), []sf91Event{{op: "Delete", key: k, failed: true}}; !slices.Equal(dels, want) {
		t.Errorf("since the mount the harness logged the Deletes %v, want exactly %v: one Delete of the "+
			"refused commit's snapshot, which the harness failed (ADR 0021 section 2, the reclaim; section 6, "+
			"G4). Its log: %v", dels, want, since)
	}

	// Through the inner store, after the records were read.
	if _, err := h.inner.Head(ctx, k); err != nil {
		t.Errorf("Head(%q) on the inner store = %v, want the orphan still there: a failed Delete leaves it "+
			"(ADR 0021 section 2, the reclaim, step 2; section 6, G4)", k, err)
	}
	sf91CheckRootBytes(t, ctx, h.inner, rootBefore, "after the Sync", "ADR 0021 section 6, G4")
}

// TestStoreHonouringOnlyIfNoneMatchCreatesThenCommitsNothing is G5 of ADR
// 0021 section 6: on a store that honours If-None-Match but answers If-Match
// 501, the Swift shape of ADR 0021's Context, a mount without the opt-in
// creates the filesystem, then refuses every commit and reclaims its snapshot,
// so the bucket holds the empty filesystem and nothing more (ADR 0021 section
// 3, the second case). The harness in its create-only mode over an empty
// bucket: New succeeds through one PutIfMatch of rootKey with etag "", passed
// through, and no Put of rootKey. A commit of c2 returns F3's error, with no
// Put and no Head of rootKey, the root pointer's bytes unchanged and the keys
// under snapshotPrefix as they were; a Create of c3 returns nil; and a
// remount has neither c2 nor c3.
//
// At 53abeb7 it fails on the listing: the refusal happens, but the attempt's
// snapshot stays.
func TestStoreHonouringOnlyIfNoneMatchCreatesThenCommitsNothing(t *testing.T) {
	h, ctx := sf91NewHarness(t, false)
	h.createOnly = true

	fs := sf91MustNew(t, ctx, "ADR 0021 section 6, G5: New, without the opt-in, over an empty bucket on a "+
		"store that honours only If-None-Match", sf91Config(h))
	rec := h.records()
	if len(rec.swaps) != 1 || rec.swaps[0].etag != "" || rec.swaps[0].answered501 || rec.swaps[0].appliedThen501 {
		t.Errorf("New over the empty bucket made the PutIfMatch calls of the root pointer %q %v, want exactly "+
			"one, with etag \"\", passed through: initEmpty's create-only swap is honoured (ADR 0021 section 3; "+
			"section 6, G5)", rootKey, rec.swaps)
	}
	if n := len(rec.rootPuts); n != 0 {
		t.Errorf("New over the empty bucket made %d Put(s) of the root pointer %q, want none (ADR 0021 section "+
			"6, G5)", n, rootKey)
	}

	// Through the inner store, after the records were read.
	rootBefore, err := h.inner.Get(ctx, rootKey)
	if err != nil {
		t.Fatalf("Get(%q) on the inner store after New = %v, want the root pointer initEmpty wrote (ADR 0021 "+
			"section 6, G5)", rootKey, err)
	}
	keysBefore := sf91SnapshotKeys(t, ctx, h.inner, "before the commit of c2")

	// The commit of c2: refused, and its snapshot reclaimed.
	err = sf91Commit(t, ctx, fs, "c2", "ADR 0021 section 6, G5: Sync of the commit of c2")
	sf91CheckRefused(t, err, "Sync of the commit of c2, on a store that answers If-Match store.ErrUnsupported",
		"ADR 0021 section 6, G5")
	sf91CheckNoRootFallback(t, h.records(), "after the commit of c2, in the whole test,", "ADR 0021 section 6, G5")
	sf91CheckRootBytes(t, ctx, h.inner, rootBefore, "after the commit of c2", "ADR 0021 section 6, G5")
	sf91CheckSnapshotKeys(t, ctx, h.inner, keysBefore, "after the commit of c2", "ADR 0021 section 6, G5")

	// The mount has not diverged.
	if err := sf91TryCreate(t, ctx, fs, "c3"); err != nil {
		t.Errorf("Create %q after the refused swap = %v, want success: a refused swap leaves the mount writable "+
			"(ADR 0021 section 1, Assumption 4; section 6, G5)", "c3", err)
	}

	sf91CheckRemount(t, ctx, h.inner, nil, []string{"c2", "c3"}, "ADR 0021 section 6, G5")
}
