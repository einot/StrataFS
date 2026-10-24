package blobfs

// Clean-room regression tests for #88, "Pruning can delete the snapshot the
// root pointer names", written without reading the implementation, from
// ADR 0019 (docs/adr/0019-a-prune-pass-deletes-only-below-its-own-commit.md)
// sections 1, 2, 3 and 6, ADR 0011 section 7, ADR 0012 section 9,
// docs/tla/CommitProtocol.tla and the comments of its eight prune
// configurations, the counterexample traces TLC reported for those
// configurations at 13aebc4, and internal/vfs/vfs.go.
//
// ADR 0019 section 1's rule: a prune pass is begun by the commit that swapped
// the root pointer to the snapshot key it wrote, K, and deletes only keys that
// sort below K, keeping the newest `retention` of those. So no pass deletes
// the snapshot the root pointer names (the module's RootSnapshotExists). The
// code at 13aebc4 spares only the epoch the pass was begun for, so once
// `retention` keys sort above the root's snapshot it deletes that snapshot,
// and the bucket can no longer be mounted.
//
// Covered:
//   - R1 to R8 of ADR 0019 section 6, one per TLA+ configuration that showed
//     the defect. Each builds the bucket state its trace describes through
//     New, Create and Sync on the harness, pr88Store, with the harness's holds
//     and faults, plus a seeded key where its row says so, and never by
//     calling the code's internals. Each asserts the oracle's record is empty
//     (RootSnapshotExists at every PruneDelete), that the root pointer names a
//     snapshot the bucket holds, and that a remount succeeds.
//   - R9: -snapshot-retention N keeps N snapshots below the current one
//     (ADR 0019 section 3).
//
// Not covered, on purpose:
//   - TestPrunableDecidesWhatAPassDeletes (P1 to P6): prunable does not exist
//     at 13aebc4, and one reference to it would stop the package's tests from
//     building, hiding the red run these tests exist to show. It belongs in
//     its own file once the code exists.
//   - What only review can show (ADR 0019 section 6): that commitLocked passes
//     its key to schedulePrune on both success paths, that pruneSnapshots
//     makes no store call but List and Delete, that prunable is pure, the doc
//     comments, the TLA+ line, and that schedulePrune's guard and its writes
//     of FS.pruning are unchanged.
//   - Logs, which ADR 0019 section 2 does not pin.
//
// The oracle. pr88Store reads the root pointer from the inner store, decoded
// as rootPointer, before it passes each Delete under snapshotPrefix through,
// and records the key if the root pointer's Snapshot is that key. It holds one
// mutex, pr88Store.oracle, around that read and the Delete, and around every
// write of rootKey it passes through, and only around the inner calls, never
// while a hold waits. So the record holds exactly the keys a pass was asked to
// delete while the root pointer named them: the steps at which
// RootSnapshotExists is false.
//
// The safety rules, as ADR 0011 section 7 and ADR 0012 section 9 give them
// and ADR 0019 section 6 restates and adapts them; every test here follows
// them:
//   - every New and every FS call runs in pr88Go, which recovers a panic, and
//     is waited for under pr88Bound, 10 s; goroutines never touch t, and
//     nothing here is parallel;
//   - every hold the harness takes selects on the test's context, which
//     t.Cleanup cancels, and a cleanup releases every hold, so no build can
//     hang the suite;
//   - MaxDirtyBytes is -1 on every mount, and no Write is made, so nothing
//     drains the write budget or starts a Sync of its own;
//   - there is no time.Sleep; the only waits on time are polling FS.pruning,
//     holding FS.mu for reading, under pr88Bound, and the one fixed grace
//     period, pr88Grace, 2 s, used only as the limit on waiting for a Delete
//     that the fixed code never makes (ADR 0019 Assumption 13);
//   - every bucket is a local directory under the test's t.TempDir();
//   - ADR 0012 section 9's rule that every committing mount has
//     SnapshotRetention -1 is replaced, as ADR 0019 section 6 says, by this
//     one: a test whose mount prunes waits, under pr88Bound, for FS.pruning
//     to be false before it returns (inline on success, and in a cleanup on
//     every path), so that no pass outlives the test; and every other mount
//     in the test, the remount included, has SnapshotRetention -1;
//   - the bucket is inspected through the inner *store.Local only after the
//     harness's record has been read, never through the harness. Before that
//     a test knows the bucket only from what the harness saw pass through it,
//     and writes to the inner store only to seed the key its row names.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// pr88Bound is how long a test waits for any one step before it reports the
// step as hung.
const pr88Bound = 10 * time.Second

// pr88Grace is the one fixed grace period (ADR 0019 Assumption 13): how long
// a test waits, after a held pass's List has returned, for the first Delete
// before it releases the held write of the root pointer.
const pr88Grace = 2 * time.Second

// pr88Poll is the interval at which FS.pruning is polled.
const pr88Poll = 10 * time.Millisecond

// pr88Caller is the root directory's owner, as every mount here is configured.
var pr88Caller = vfs.Caller{UID: 501, GID: 20}

// pr88ErrInjected is the error the harness's faults return. It is neither
// store.ErrPrecondition nor store.ErrUnsupported.
var pr88ErrInjected = errors.New("pr88: injected store fault")

// pr88Hold is one held call. entered is closed when the call is held,
// returned when the inner call it passed through has returned.
type pr88Hold struct {
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
	once     sync.Once
}

func pr88NewHold() *pr88Hold {
	return &pr88Hold{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
}

// Release lets the held call go on. It may be called more than once.
func (h *pr88Hold) Release() {
	h.once.Do(func() { close(h.release) })
}

// hold is run by the held call: it signals entered, then waits until the hold
// is released or testCtx ends.
func (h *pr88Hold) hold(testCtx context.Context) {
	close(h.entered)
	select {
	case <-h.release:
	case <-testCtx.Done():
	}
}

// pr88Store is ADR 0019 section 6's harness: a store.Store over a
// *store.Local that passes every call through, except for the one-shot holds
// and faults it is armed with, and that keeps the oracle's record. It does not
// embed store.Store, so that every method it has is one written here.
type pr88Store struct {
	inner   *store.Local
	testCtx context.Context

	// unconditional answers every PutIfMatch with store.ErrUnsupported
	// without calling the inner store. It is set before the first call and
	// never changed.
	unconditional bool

	// oracle serialises each Delete's read of the root pointer and the Delete
	// itself against every write of rootKey the harness passes through. It is
	// held only around inner calls.
	oracle sync.Mutex

	// mu guards every field below.
	mu sync.Mutex

	listHold    *pr88Hold
	swapFail    bool
	swapLost    bool
	swapHold    *pr88Hold
	snapLost    bool
	rootPutLost bool
	rootPutHold *pr88Hold
	holds       []*pr88Hold

	firstDelete chan struct{}
	deleteSeen  bool
	deleted     []string
	oracleErrs  []string

	snapOrder []string
	snapData  map[string][]byte
	roots     []string
	rootCalls int
}

var _ store.Store = (*pr88Store)(nil)

// newHold makes a hold and remembers it for releaseAll. The caller holds s.mu.
func (s *pr88Store) newHold() *pr88Hold {
	h := pr88NewHold()
	s.holds = append(s.holds, h)
	return h
}

// armList holds the next List whose prefix is snapshotPrefix and whose after
// is empty.
func (s *pr88Store) armList() *pr88Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listHold = s.newHold()
	return s.listHold
}

// armSwapFail fails the next PutIfMatch of rootKey with pr88ErrInjected,
// without calling the inner store (SwapFailed).
func (s *pr88Store) armSwapFail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swapFail = true
}

// armSwapLost applies the next PutIfMatch of rootKey on the inner store and
// returns pr88ErrInjected (SwapReplyLost).
func (s *pr88Store) armSwapLost() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swapLost = true
}

// armSwapHold holds the next PutIfMatch of rootKey, then passes it through (a
// pending swap).
func (s *pr88Store) armSwapHold() *pr88Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swapHold = s.newHold()
	return s.swapHold
}

// armSnapshotLost applies the next Put under snapshotPrefix on the inner store
// and returns pr88ErrInjected (SnapshotReplyLost).
func (s *pr88Store) armSnapshotLost() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapLost = true
}

// armRootPutLost applies the next Put of rootKey on the inner store and
// returns pr88ErrInjected (FallbackReplyLost).
func (s *pr88Store) armRootPutLost() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rootPutLost = true
}

// armRootPutHold holds the next Put of rootKey, then passes it through (a
// pending fallback Put).
func (s *pr88Store) armRootPutHold() *pr88Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rootPutHold = s.newHold()
	return s.rootPutHold
}

// releaseAll releases every hold the harness has made.
func (s *pr88Store) releaseAll() {
	s.mu.Lock()
	holds := append([]*pr88Hold(nil), s.holds...)
	s.mu.Unlock()
	for _, h := range holds {
		h.Release()
	}
}

// record returns the keys deleted while the root pointer named them, and the
// failures to read the root pointer at a Delete.
func (s *pr88Store) record() (deleted, oracleErrs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...), append([]string(nil), s.oracleErrs...)
}

// snapshotCount is how many distinct snapshot keys the harness has passed a
// successful Put of through to the inner store.
func (s *pr88Store) snapshotCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.snapOrder)
}

// snapshotsSince returns the snapshot keys first put after the first mark of
// them, in the order they were put.
func (s *pr88Store) snapshotsSince(mark int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.snapOrder[mark:]...)
}

// snapshotBytes returns the bytes last put under key through the harness.
func (s *pr88Store) snapshotBytes(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.snapData[key]
	return append([]byte(nil), data...), ok
}

// rootNow returns the Snapshot of the last write of rootKey the inner store
// applied through the harness, or "" if there has been none.
func (s *pr88Store) rootNow() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.roots) == 0 {
		return ""
	}
	return s.roots[len(s.roots)-1]
}

// rootWriteCalls is how many calls of Put or PutIfMatch of rootKey the harness
// has received.
func (s *pr88Store) rootWriteCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rootCalls
}

// waitDeleteOrGrace waits for the first Delete under snapshotPrefix, or for
// pr88Grace, whichever comes first.
func (s *pr88Store) waitDeleteOrGrace() {
	select {
	case <-s.firstDelete:
	case <-time.After(pr88Grace):
	}
}

// writeRoot runs one write of rootKey on the inner store holding s.oracle, and
// records which snapshot the written root pointer names if it was applied.
func (s *pr88Store) writeRoot(data []byte, write func() (string, error)) (string, error) {
	s.oracle.Lock()
	defer s.oracle.Unlock()
	etag, err := write()
	if err == nil {
		named := "(a root pointer that does not decode as rootPointer)"
		var rp rootPointer
		if json.Unmarshal(data, &rp) == nil {
			named = rp.Snapshot
		}
		s.mu.Lock()
		s.roots = append(s.roots, named)
		s.mu.Unlock()
	}
	return etag, err
}

func (s *pr88Store) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

// GetWithETag passes the call through to the inner store, as ADR 0020
// Assumption 14 asks once the method exists.
func (s *pr88Store) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	return s.inner.GetWithETag(ctx, key)
}

func (s *pr88Store) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	return s.inner.GetRange(ctx, key, off, n)
}

func (s *pr88Store) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	return s.inner.Head(ctx, key)
}

func (s *pr88Store) Put(ctx context.Context, key string, data []byte) error {
	switch {
	case key == rootKey:
		s.mu.Lock()
		s.rootCalls++
		lost := s.rootPutLost
		s.rootPutLost = false
		var hold *pr88Hold
		if !lost {
			hold = s.rootPutHold
			s.rootPutHold = nil
		}
		s.mu.Unlock()
		if hold != nil {
			hold.hold(s.testCtx)
		}
		_, err := s.writeRoot(data, func() (string, error) {
			return "", s.inner.Put(ctx, key, data)
		})
		if hold != nil {
			close(hold.returned)
		}
		if lost && err == nil {
			return pr88ErrInjected
		}
		return err
	case strings.HasPrefix(key, snapshotPrefix):
		s.mu.Lock()
		lost := s.snapLost
		s.snapLost = false
		s.mu.Unlock()
		err := s.inner.Put(ctx, key, data)
		if err == nil {
			s.mu.Lock()
			if _, seen := s.snapData[key]; !seen {
				s.snapOrder = append(s.snapOrder, key)
			}
			s.snapData[key] = append([]byte(nil), data...)
			s.mu.Unlock()
		}
		if lost && err == nil {
			return pr88ErrInjected
		}
		return err
	default:
		return s.inner.Put(ctx, key, data)
	}
}

func (s *pr88Store) Delete(ctx context.Context, key string) error {
	if !strings.HasPrefix(key, snapshotPrefix) {
		return s.inner.Delete(ctx, key)
	}
	s.oracle.Lock()
	raw, getErr := s.inner.Get(ctx, rootKey)
	err := s.inner.Delete(ctx, key)
	s.oracle.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if getErr != nil {
		s.oracleErrs = append(s.oracleErrs, fmt.Sprintf("Get(%q) before Delete(%q): %v", rootKey, key, getErr))
	} else {
		var rp rootPointer
		if uerr := json.Unmarshal(raw, &rp); uerr != nil {
			s.oracleErrs = append(s.oracleErrs, fmt.Sprintf("decoding %q as rootPointer before Delete(%q): %v", rootKey, key, uerr))
		} else if rp.Snapshot == key {
			s.deleted = append(s.deleted, key)
		}
	}
	if !s.deleteSeen {
		s.deleteSeen = true
		close(s.firstDelete)
	}
	return err
}

func (s *pr88Store) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	var hold *pr88Hold
	if prefix == snapshotPrefix && after == "" {
		s.mu.Lock()
		hold = s.listHold
		s.listHold = nil
		s.mu.Unlock()
	}
	if hold == nil {
		return s.inner.List(ctx, prefix, after, max)
	}
	hold.hold(s.testCtx)
	infos, err := s.inner.List(ctx, prefix, after, max)
	close(hold.returned)
	return infos, err
}

func (s *pr88Store) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) {
	if s.unconditional {
		if key == rootKey {
			s.mu.Lock()
			s.rootCalls++
			s.mu.Unlock()
		}
		return "", store.ErrUnsupported
	}
	if key != rootKey {
		return s.inner.PutIfMatch(ctx, key, data, etag)
	}
	s.mu.Lock()
	s.rootCalls++
	fail := s.swapFail
	s.swapFail = false
	lost := !fail && s.swapLost
	if lost {
		s.swapLost = false
	}
	var hold *pr88Hold
	if !fail && !lost {
		hold = s.swapHold
		s.swapHold = nil
	}
	s.mu.Unlock()
	if fail {
		return "", pr88ErrInjected
	}
	if hold != nil {
		hold.hold(s.testCtx)
	}
	newETag, err = s.writeRoot(data, func() (string, error) {
		return s.inner.PutIfMatch(ctx, key, data, etag)
	})
	if hold != nil {
		close(hold.returned)
	}
	if lost && err == nil {
		return "", pr88ErrInjected
	}
	return newETag, err
}

func (s *pr88Store) Name() string {
	return s.inner.Name()
}

// pr88Call is one call running in its own goroutine. val, returned, panicked
// and pval may be read only once done is closed.
type pr88Call[T any] struct {
	done     chan struct{}
	val      T
	returned bool
	panicked bool
	pval     any
}

// pr88Go runs fn in its own goroutine, recovering and recording a panic out of
// it. fn must not touch t: after a timeout its goroutine is abandoned.
func pr88Go[T any](fn func() T) *pr88Call[T] {
	c := &pr88Call[T]{done: make(chan struct{})}
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

// problem waits for the call under pr88Bound, and says what went wrong, or ""
// if it returned.
func (c *pr88Call[T]) problem(what string) string {
	select {
	case <-c.done:
	case <-time.After(pr88Bound):
		return fmt.Sprintf("%s has not returned after %v, so it is hung", what, pr88Bound)
	}
	if c.panicked {
		return fmt.Sprintf("%s panicked with %v", what, c.pval)
	}
	if !c.returned {
		return fmt.Sprintf("%s ended without returning (runtime.Goexit)", what)
	}
	return ""
}

// wait waits for the call under pr88Bound and returns its value. A hang, a
// panic or a runtime.Goexit fails the test.
func (c *pr88Call[T]) wait(t *testing.T, what string) T {
	t.Helper()
	if p := c.problem(what); p != "" {
		t.Fatal(p)
	}
	return c.val
}

// pr88NewResult is what one New returned.
type pr88NewResult struct {
	fs  *FS
	err error
}

// pr88Local makes a local bucket under the test's temporary directory.
func pr88Local(t *testing.T) *store.Local {
	t.Helper()
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	return local
}

// pr88NewHarness makes a harness over a fresh local bucket, in unconditional
// mode if asked, and the test's context, which a cleanup cancels. Another
// cleanup, which runs first, releases every hold.
func pr88NewHarness(t *testing.T, unconditional bool) (*pr88Store, context.Context) {
	t.Helper()
	local := pr88Local(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &pr88Store{
		inner:         local,
		testCtx:       ctx,
		unconditional: unconditional,
		firstDelete:   make(chan struct{}),
		snapData:      make(map[string][]byte),
	}
	t.Cleanup(h.releaseAll)
	return h, ctx
}

// pr88Config is a mount's Config: the root directory owned by pr88Caller, the
// write budget's wait disabled, and the given SnapshotRetention.
func pr88Config(st store.Store, retention int) Config {
	return Config{
		Store:             st,
		ChunkSize:         4096,
		OwnerUID:          501,
		OwnerGID:          20,
		SnapshotRetention: retention,
		MaxDirtyBytes:     -1,
		Log:               quietLog(),
	}
}

// pr88TryNew runs New in pr88Go and waits for it under pr88Bound.
func pr88TryNew(t *testing.T, ctx context.Context, what string, cfg Config) pr88NewResult {
	t.Helper()
	return pr88Go(func() pr88NewResult {
		fs, err := New(ctx, cfg)
		return pr88NewResult{fs, err}
	}).wait(t, what)
}

// pr88MustNew is pr88TryNew for a New that must succeed.
func pr88MustNew(t *testing.T, ctx context.Context, what string, cfg Config) *FS {
	t.Helper()
	r := pr88TryNew(t, ctx, what, cfg)
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	if r.fs == nil {
		t.Fatalf("%s returned a nil *FS and a nil error", what)
	}
	return r.fs
}

// pr88PassProblem polls FS.pruning, holding FS.mu for reading, until it is
// false, and waits for that under pr88Bound. It says what went wrong, or "".
func pr88PassProblem(fs *FS, what string) string {
	stop := make(chan struct{})
	defer close(stop)
	return pr88Go(func() bool {
		for {
			fs.mu.RLock()
			pruning := fs.pruning
			fs.mu.RUnlock()
			if !pruning {
				return true
			}
			select {
			case <-stop:
				return false
			case <-time.After(pr88Poll):
			}
		}
	}).problem(what)
}

// pr88WaitPass is ADR 0019 section 6's "wait for the pass".
func pr88WaitPass(t *testing.T, fs *FS, what string) {
	t.Helper()
	if p := pr88PassProblem(fs, what+" to end (FS.pruning false)"); p != "" {
		t.Fatal(p)
	}
}

// pr88AwaitPassAtCleanup registers a cleanup that releases every hold of h, if
// there is a harness, and waits for fs's pass to end, so that no pass outlives
// the test on any path.
func pr88AwaitPassAtCleanup(t *testing.T, h *pr88Store, fs *FS, what string) {
	t.Helper()
	t.Cleanup(func() {
		if h != nil {
			h.releaseAll()
		}
		if p := pr88PassProblem(fs, "at cleanup, "+what+" to end (FS.pruning false)"); p != "" {
			t.Errorf("%s; no pass may outlive its test (ADR 0019 section 6)", p)
		}
	})
}

// pr88Pruner starts the route tests' pruning mount: New on h with the given
// SnapshotRetention and the List hold armed, then waits for the hold's
// signal, so the init commit's pass is listing.
func pr88Pruner(t *testing.T, ctx context.Context, h *pr88Store, retention int, what string) (*FS, *pr88Hold) {
	t.Helper()
	list := h.armList()
	fs := pr88MustNew(t, ctx, what, pr88Config(h, retention))
	pr88AwaitPassAtCleanup(t, h, fs, "the prune pass of "+what)
	pr88WaitSignal(t, list.entered, "the List of "+snapshotPrefix+" by the prune pass that "+what+"'s init commit begins")
	return fs, list
}

// pr88WaitSignal waits for ch to be closed under pr88Bound.
func pr88WaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(pr88Bound):
		t.Fatalf("waited %v for %s, which never came", pr88Bound, what)
	}
}

// pr88WaitHeld waits for hold to be entered by the Sync that c runs.
func pr88WaitHeld(t *testing.T, hold *pr88Hold, c *pr88Call[error], what string) {
	t.Helper()
	select {
	case <-hold.entered:
	case <-c.done:
		t.Fatalf("Sync returned (err %v, panicked %v with %v) without reaching %s, so the test never held it",
			c.val, c.panicked, c.pval, what)
	case <-time.After(pr88Bound):
		t.Fatalf("Sync has not reached %s after %v", what, pr88Bound)
	}
}

// pr88Create creates name in the root directory of fs, as pr88Caller.
func pr88Create(t *testing.T, ctx context.Context, fs *FS, name string) {
	t.Helper()
	what := fmt.Sprintf("Create %q in the root directory", name)
	err := pr88Go(func() error {
		_, _, err := fs.Create(ctx, pr88Caller, fs.Root(), name, vfs.SetAttr{}, true)
		return err
	}).wait(t, what)
	if err != nil {
		t.Fatalf("%s as its owner (%d, %d) = %v, want success", what, pr88Caller.UID, pr88Caller.GID, err)
	}
}

// pr88StartSync starts fs.Sync in pr88Go.
func pr88StartSync(ctx context.Context, fs *FS) *pr88Call[error] {
	return pr88Go(func() error { return fs.Sync(ctx) })
}

// pr88Commit is ADR 0019 section 6's commit: a Create of a fresh name in the
// root directory, then Sync. It returns what Sync returned.
func pr88Commit(t *testing.T, ctx context.Context, fs *FS, name, what string) error {
	t.Helper()
	pr88Create(t, ctx, fs, name)
	return pr88StartSync(ctx, fs).wait(t, what)
}

// pr88Epoch returns the epoch in a snapshot key: snapshotPrefix, the epoch as
// twenty decimal digits, "-", the nonce, ".json.gz".
func pr88Epoch(key string) (uint64, bool) {
	name, ok := strings.CutPrefix(key, snapshotPrefix)
	if !ok || len(name) < 21 || name[20] != '-' {
		return 0, false
	}
	e, err := strconv.ParseUint(name[:20], 10, 64)
	return e, err == nil
}

// pr88HighKey is the key an attempt at epoch with the highest nonce leaves.
func pr88HighKey(epoch uint64) string {
	return fmt.Sprintf("%s%020d-ffffffffffff.json.gz", snapshotPrefix, epoch)
}

// pr88RootAt requires the root pointer, as the harness last applied a write
// of it, to name a snapshot key at epoch, and returns that key.
func pr88RootAt(t *testing.T, h *pr88Store, epoch uint64, what string) string {
	t.Helper()
	k := h.rootNow()
	if e, ok := pr88Epoch(k); !ok || e != epoch {
		t.Fatalf("%s, the root pointer names %q, want a snapshot key at epoch %d", what, k, epoch)
	}
	return k
}

// pr88OneNewKeyAt requires exactly one of the snapshot keys put through the
// harness since mark to be at epoch, and returns it.
func pr88OneNewKeyAt(t *testing.T, h *pr88Store, mark int, epoch uint64, what string) string {
	t.Helper()
	since := h.snapshotsSince(mark)
	var at []string
	for _, k := range since {
		if e, ok := pr88Epoch(k); ok && e == epoch {
			at = append(at, k)
		}
	}
	if len(at) != 1 {
		t.Fatalf("%s wrote the snapshot keys %q at epoch %d, want exactly one (every snapshot key it wrote: %q)",
			what, at, epoch, since)
	}
	return at[0]
}

// pr88Seed puts, on the inner store, the bytes the harness saw put under from
// at pr88HighKey(epoch), which must sort above from, and returns that key.
func pr88Seed(t *testing.T, ctx context.Context, h *pr88Store, from string, epoch uint64) string {
	t.Helper()
	data, ok := h.snapshotBytes(from)
	if !ok {
		t.Fatalf("setup: the harness saw no Put of %q, so it has no bytes to seed from", from)
	}
	key := pr88HighKey(epoch)
	if !(from < key) {
		t.Fatalf("setup: the seeded key %q does not sort above %q", key, from)
	}
	if err := h.inner.Put(ctx, key, data); err != nil {
		t.Fatalf("setup: seeding %q on the inner store: %v", key, err)
	}
	return key
}

// pr88CheckEnd makes the route tests' end-state checks: the oracle's record
// is empty, read first; then, through the inner store only, the root pointer
// names a snapshot the bucket holds, and New on the bucket succeeds.
func pr88CheckEnd(t *testing.T, ctx context.Context, h *pr88Store) {
	t.Helper()
	deleted, oracleErrs := h.record()
	for _, e := range oracleErrs {
		t.Errorf("the harness could not read the root pointer at a Delete, so RootSnapshotExists went unchecked there: %s", e)
	}
	for _, k := range deleted {
		t.Errorf("a prune pass deleted %q while the root pointer named it: RootSnapshotExists is false at that "+
			"PruneDelete (ADR 0019 section 1: a pass deletes only keys below the key its own commit wrote)", k)
	}
	pr88CheckRoot(t, ctx, h.inner)
	pr88CheckRemount(t, ctx, h.inner)
}

// pr88CheckRoot requires the root pointer in local to decode as rootPointer
// and to name a snapshot local holds.
func pr88CheckRoot(t *testing.T, ctx context.Context, local *store.Local) {
	t.Helper()
	raw, err := local.Get(ctx, rootKey)
	if err != nil {
		t.Errorf("Get(%q) on the bucket = %v, want the root pointer", rootKey, err)
		return
	}
	var rp rootPointer
	if err := json.Unmarshal(raw, &rp); err != nil {
		t.Errorf("the root pointer does not decode as rootPointer: %v", err)
		return
	}
	if _, err := local.Head(ctx, rp.Snapshot); err != nil {
		t.Errorf("the root pointer names the snapshot %q, but Head of it on the bucket = %v: the snapshot the "+
			"root pointer names is gone (RootSnapshotExists; #88)", rp.Snapshot, err)
	}
}

// pr88CheckRemount requires New on local, with SnapshotRetention -1, to
// succeed.
func pr88CheckRemount(t *testing.T, ctx context.Context, local *store.Local) {
	t.Helper()
	what := "the remount: New on the bucket with SnapshotRetention -1"
	r := pr88TryNew(t, ctx, what, pr88Config(local, -1))
	if r.err != nil {
		t.Errorf("%s = %v, want success: no prune pass may leave the bucket unmountable (#88)", what, r.err)
	} else if r.fs == nil {
		t.Errorf("%s returned a nil *FS and a nil error", what)
	}
}

// TestPruneKeepsRootSnapshotAfterFailedSwap is R1 of ADR 0019 section 6, from
// docs/tla/CommitProtocol_PruneOrphan.cfg: #88's route 1, a failed swap's
// orphan.
//
// The trace's steps, through the code path:
//   - InitEmpty, PutSnapshot [1,1] and SwapOK, which starts the pass: New over
//     the empty bucket, with SnapshotRetention 1; the harness holds the pass's
//     List;
//   - Mutate, PutSnapshot and SwapOK: a commit, at epoch 2, which the root
//     pointer then names;
//   - Mutate, PutSnapshot and SwapFailed: a commit whose swap the harness
//     fails without applying it, leaving its snapshot, at epoch 3, as an
//     orphan above the root's;
//   - PruneList and PruneDelete: the List is released, and the pass runs.
//
// Where it deviates (ADR 0019 Assumption 12): the trace's orphan is [2,2],
// above the retry [2,1] at the same epoch by nonce. Nonces come from
// crypto/rand, so the test moves the failed swap one epoch up, after the
// successful commit: the shape, one orphan sorting above the root's snapshot
// while the pass begun at epoch 1 lists, is the same.
//
// At 13aebc4 the pass keeps the orphan as the newest key and deletes the
// epoch-2 snapshot the root pointer names: the record holds it, and the
// remount fails.
func TestPruneKeepsRootSnapshotAfterFailedSwap(t *testing.T) {
	h, ctx := pr88NewHarness(t, false)
	a, list := pr88Pruner(t, ctx, h, 1, "New over an empty bucket with SnapshotRetention 1")

	if err := pr88Commit(t, ctx, a, "c2", "Sync of the commit at epoch 2"); err != nil {
		t.Fatalf("Sync of the commit at epoch 2 = %v, want success", err)
	}
	root2 := pr88RootAt(t, h, 2, "after the commit at epoch 2")

	h.armSwapFail()
	mark := h.snapshotCount()
	if err := pr88Commit(t, ctx, a, "c3", "Sync of the commit whose swap fails"); err == nil {
		t.Fatalf("Sync of a commit whose swap of the root pointer failed = nil, want an error")
	}
	pr88OneNewKeyAt(t, h, mark, 3, "the commit whose swap failed")
	if got := h.rootNow(); got != root2 {
		t.Fatalf("after the failed swap the root pointer names %q, want %q still", got, root2)
	}

	list.Release()
	pr88WaitPass(t, a, "the pass the init commit began")
	pr88CheckEnd(t, ctx, h)
}

// TestPruneKeepsRootSnapshotWithTwoWriters is R2 of ADR 0019 section 6, from
// docs/tla/CommitProtocol_PruneTwoWriters.cfg: #88's route 2, a losing
// writer's attempt.
//
// The trace's steps, through the code path:
//   - InitEmpty, PutSnapshot [1,1] and SwapOK, which starts A's pass: New of
//     mount A over the empty bucket, with SnapshotRetention 1; the harness
//     holds the pass's List;
//   - B's GetRoot, HeadRoot and GetSnapshot: New of mount B on the same
//     harness, with SnapshotRetention -1, at epoch 1;
//   - B's Mutate, PutSnapshot and SwapOK: a commit on B at epoch 2, which the
//     root pointer then names;
//   - A's Mutate and PutSnapshot at epoch 2: a commit on A, whose swap fails
//     its condition, since B moved the root pointer;
//   - PruneList and PruneDelete: the List is released, and A's pass runs.
//
// Where it deviates (ADR 0019 Assumption 12): in the trace A's attempt [2,2]
// sorts above B's [2,1] by nonce, and B's swap lands after A's pass has
// deleted [2,1]. Nonces come from crypto/rand, so the test seeds, on the
// inner store, the object an attempt at epoch 2 with the highest nonce
// leaves, under nonce ffffffffffff, with the bytes of B's epoch-2 snapshot:
// it stands for A's attempt sorting above B's. ADR 0019 section 6's row puts
// B's swap before the pass lists, so the root pointer names B's snapshot at
// the deletion itself.
//
// At 13aebc4 the pass keeps the seeded key as the newest and deletes every
// other key off epoch 1, B's among them: the record holds B's key, and the
// remount fails.
func TestPruneKeepsRootSnapshotWithTwoWriters(t *testing.T) {
	h, ctx := pr88NewHarness(t, false)
	a, list := pr88Pruner(t, ctx, h, 1, "New of mount A over an empty bucket with SnapshotRetention 1")
	b := pr88MustNew(t, ctx, "New of mount B on the same bucket with SnapshotRetention -1", pr88Config(h, -1))

	if err := pr88Commit(t, ctx, b, "b2", "Sync of B's commit at epoch 2"); err != nil {
		t.Fatalf("Sync of B's commit at epoch 2 = %v, want success", err)
	}
	rootB := pr88RootAt(t, h, 2, "after B's commit at epoch 2")

	mark := h.snapshotCount()
	if err := pr88Commit(t, ctx, a, "a2", "Sync of A's commit after B's"); err == nil {
		t.Fatalf("Sync of A's commit after B moved the root pointer = nil, want an error: A's swap fails its " +
			"condition")
	}
	pr88OneNewKeyAt(t, h, mark, 2, "A's commit after B's")
	if got := h.rootNow(); got != rootB {
		t.Fatalf("after A's failed commit the root pointer names %q, want B's %q still", got, rootB)
	}
	pr88Seed(t, ctx, h, rootB, 2)

	list.Release()
	pr88WaitPass(t, a, "A's pass, which A's init commit began")
	pr88CheckEnd(t, ctx, h)
}

// TestPruneKeepsRootSnapshotWhenListingOverlapsCommits is R3 of ADR 0019
// section 6, from docs/tla/CommitProtocol_PruneOverlap.cfg: #88's route 3, the
// mount's own unswapped next attempt.
//
// The trace's steps, through the code path:
//   - InitEmpty, PutSnapshot [1,1] and SwapOK, which starts the pass: New over
//     the empty bucket, with SnapshotRetention 1; the harness holds the pass's
//     List;
//   - Mutate, PutSnapshot [2,1] and SwapOK: a commit at epoch 2, which the
//     root pointer then names;
//   - Mutate and PutSnapshot [3,1]: a commit at epoch 3, its swap held
//     pending by the harness;
//   - PruneList and PruneDelete: the List is released and returns, and the
//     test waits for the first Delete or the grace period before it releases
//     the swap.
//
// It follows the trace without deviating.
//
// At 13aebc4 the pass keeps the epoch-3 key as the newest and deletes the
// epoch-2 one while the root pointer still names it: the record holds it.
func TestPruneKeepsRootSnapshotWhenListingOverlapsCommits(t *testing.T) {
	h, ctx := pr88NewHarness(t, false)
	a, list := pr88Pruner(t, ctx, h, 1, "New over an empty bucket with SnapshotRetention 1")

	if err := pr88Commit(t, ctx, a, "c2", "Sync of the commit at epoch 2"); err != nil {
		t.Fatalf("Sync of the commit at epoch 2 = %v, want success", err)
	}
	root2 := pr88RootAt(t, h, 2, "after the commit at epoch 2")

	swap := h.armSwapHold()
	mark := h.snapshotCount()
	pr88Create(t, ctx, a, "c3")
	sync3 := pr88StartSync(ctx, a)
	pr88WaitHeld(t, swap, sync3, "the swap of the root pointer of the commit at epoch 3")
	key3 := pr88OneNewKeyAt(t, h, mark, 3, "the commit whose swap is held")
	if got := h.rootNow(); got != root2 {
		t.Fatalf("while the swap at epoch 3 is held the root pointer names %q, want %q", got, root2)
	}

	list.Release()
	pr88WaitSignal(t, list.returned, "the held List to return")
	h.waitDeleteOrGrace()
	swap.Release()
	if err := sync3.wait(t, "Sync of the commit at epoch 3"); err != nil {
		t.Errorf("Sync of the commit at epoch 3, once its swap is released = %v, want success: nothing else "+
			"moved the root pointer", err)
	}
	if got := h.rootNow(); got != key3 {
		t.Errorf("after the commit at epoch 3 the root pointer names %q, want %q", got, key3)
	}

	pr88WaitPass(t, a, "the prune pass")
	pr88CheckEnd(t, ctx, h)
}

// TestPruneKeepsRootSnapshotWhenAnotherMountsAttemptIsPending is R4 of
// ADR 0019 section 6, from docs/tla/CommitProtocol_PruneOverlapTwoWriters.cfg:
// #88's routes 2 and 3 at once, a pass overlapping its own mount's commit and
// another mount's attempt.
//
// The trace's steps, through the code path:
//   - A inits [1,1], which starts A's pass: New of mount A over the empty
//     bucket, with SnapshotRetention 1; the harness holds the pass's List;
//   - A commits [2,1] with Mutate, PutSnapshot and SwapOK while the pass is
//     still listing: a commit on A at epoch 2, which the root pointer then
//     names;
//   - B's GetRoot, HeadRoot and GetSnapshot: New of mount B on the same
//     harness, with SnapshotRetention -1, after A's commit;
//   - B's Mutate and PutSnapshot: a commit on B, its swap held pending by the
//     harness, its snapshot above A's;
//   - A's PruneList and PruneDelete: the List is released and returns, and
//     the test waits for the first Delete or the grace period before it
//     releases B's swap.
//
// Where it deviates (ADR 0019 Assumption 12): the trace reaches B's attempt
// [2,2], above A's [2,1] by nonce, through #89's window, B's GetRoot before
// A's swap and HeadRoot after it. That window is #89's to reproduce, and
// nonces come from crypto/rand, so here B loads after A's commit and its
// attempt is at the next epoch, 3, which sorts above A's snapshot with the
// same shape.
//
// At 13aebc4 A's pass keeps B's epoch-3 key as the newest and deletes A's
// epoch-2 one while the root pointer names it: the record holds it.
func TestPruneKeepsRootSnapshotWhenAnotherMountsAttemptIsPending(t *testing.T) {
	h, ctx := pr88NewHarness(t, false)
	a, list := pr88Pruner(t, ctx, h, 1, "New of mount A over an empty bucket with SnapshotRetention 1")

	if err := pr88Commit(t, ctx, a, "a2", "Sync of A's commit at epoch 2"); err != nil {
		t.Fatalf("Sync of A's commit at epoch 2 = %v, want success", err)
	}
	rootA := pr88RootAt(t, h, 2, "after A's commit at epoch 2")
	b := pr88MustNew(t, ctx, "New of mount B on the same bucket, after A's commit, with SnapshotRetention -1",
		pr88Config(h, -1))

	swap := h.armSwapHold()
	mark := h.snapshotCount()
	pr88Create(t, ctx, b, "b3")
	syncB := pr88StartSync(ctx, b)
	pr88WaitHeld(t, swap, syncB, "the swap of the root pointer of B's commit")
	keyB := pr88OneNewKeyAt(t, h, mark, 3, "B's commit, whose swap is held")
	if got := h.rootNow(); got != rootA {
		t.Fatalf("while B's swap is held the root pointer names %q, want A's %q", got, rootA)
	}

	list.Release()
	pr88WaitSignal(t, list.returned, "the held List to return")
	h.waitDeleteOrGrace()
	swap.Release()
	if err := syncB.wait(t, "Sync of B's commit"); err != nil {
		t.Errorf("Sync of B's commit, once its swap is released = %v, want success: nothing moved the root "+
			"pointer since B loaded it", err)
	}
	if got := h.rootNow(); got != keyB {
		t.Errorf("after B's commit the root pointer names %q, want B's %q", got, keyB)
	}

	pr88WaitPass(t, a, "A's pass, which A's init commit began")
	pr88CheckEnd(t, ctx, h)
}

// TestPruneKeepsRootSnapshotAfterLostSnapshotReply is R5 of ADR 0019 section
// 6, from docs/tla/CommitProtocol_PruneSnapshotReply.cfg: #88's route 4, a
// snapshot Put that took effect and returned an error.
//
// The trace's steps, through the code path:
//   - InitEmpty, PutSnapshot [1,1] and SwapOK, which starts the pass: New over
//     the empty bucket, with SnapshotRetention 1; the harness holds the pass's
//     List;
//   - Mutate, PutSnapshot and SwapOK: a commit at epoch 2, which the root
//     pointer then names;
//   - Mutate and a PutSnapshot whose reply is lost: a commit whose snapshot
//     Put the harness applies and then fails, so that its snapshot, at epoch
//     3, is in the bucket and no swap is attempted;
//   - PruneList and PruneDelete: the List is released, and the pass runs.
//
// Where it deviates (ADR 0019 Assumption 12): the trace's lost-reply snapshot
// is [2,2], above the retry [2,1] at the same epoch by nonce. Nonces come
// from crypto/rand, so the test moves the lost reply one epoch up, after the
// successful commit, with the same shape.
//
// At 13aebc4 the pass keeps the epoch-3 key as the newest and deletes the
// epoch-2 snapshot the root pointer names: the record holds it, and the
// remount fails.
func TestPruneKeepsRootSnapshotAfterLostSnapshotReply(t *testing.T) {
	h, ctx := pr88NewHarness(t, false)
	a, list := pr88Pruner(t, ctx, h, 1, "New over an empty bucket with SnapshotRetention 1")

	if err := pr88Commit(t, ctx, a, "c2", "Sync of the commit at epoch 2"); err != nil {
		t.Fatalf("Sync of the commit at epoch 2 = %v, want success", err)
	}
	root2 := pr88RootAt(t, h, 2, "after the commit at epoch 2")

	h.armSnapshotLost()
	mark := h.snapshotCount()
	swaps := h.rootWriteCalls()
	if err := pr88Commit(t, ctx, a, "c3", "Sync of the commit whose snapshot Put's reply is lost"); err == nil {
		t.Fatalf("Sync of a commit whose snapshot Put returned an error = nil, want an error")
	}
	pr88OneNewKeyAt(t, h, mark, 3, "the commit whose snapshot Put's reply was lost")
	if got := h.rootWriteCalls(); got != swaps {
		t.Fatalf("the commit whose snapshot Put returned an error wrote the root pointer %d time(s), want none",
			got-swaps)
	}
	if got := h.rootNow(); got != root2 {
		t.Fatalf("after the lost snapshot reply the root pointer names %q, want %q still", got, root2)
	}

	list.Release()
	pr88WaitPass(t, a, "the pass the init commit began")
	pr88CheckEnd(t, ctx, h)
}

// TestPruneKeepsRootSnapshotAtRetentionTwo is R6 of ADR 0019 section 6, from
// docs/tla/CommitProtocol_PruneSnapshotReplyRetention2.cfg: #88's route 4
// together with route 3, at retention 2, so that no fix can rest on a
// retention above 1.
//
// The trace's steps, through the code path:
//   - InitEmpty, PutSnapshot [1,1] and SwapOK, which starts the pass: New over
//     the empty bucket, with SnapshotRetention 2; the harness holds the pass's
//     List;
//   - Mutate, PutSnapshot [2,1] and SwapOK: a commit at epoch 2, which the
//     root pointer then names;
//   - Mutate and PutSnapshot [3,1], whose reply is lost: a commit whose
//     snapshot Put the harness applies and then fails;
//   - PutSnapshot [3,2]: Sync again, the namespace still dirty, its swap held
//     pending by the harness;
//   - PruneList, keeping both epoch-3 keys, and PruneDelete: the List is
//     released and returns, and the test waits for the first Delete or the
//     grace period before it releases the swap.
//
// It follows the trace without deviating.
//
// At 13aebc4 the pass keeps both epoch-3 keys as the newest two and deletes
// the epoch-2 one while the root pointer names it: the record holds it.
func TestPruneKeepsRootSnapshotAtRetentionTwo(t *testing.T) {
	h, ctx := pr88NewHarness(t, false)
	a, list := pr88Pruner(t, ctx, h, 2, "New over an empty bucket with SnapshotRetention 2")

	if err := pr88Commit(t, ctx, a, "c2", "Sync of the commit at epoch 2"); err != nil {
		t.Fatalf("Sync of the commit at epoch 2 = %v, want success", err)
	}
	root2 := pr88RootAt(t, h, 2, "after the commit at epoch 2")

	h.armSnapshotLost()
	mark := h.snapshotCount()
	if err := pr88Commit(t, ctx, a, "c3", "Sync of the commit whose snapshot Put's reply is lost"); err == nil {
		t.Fatalf("Sync of a commit whose snapshot Put returned an error = nil, want an error")
	}
	lost := pr88OneNewKeyAt(t, h, mark, 3, "the commit whose snapshot Put's reply was lost")

	swap := h.armSwapHold()
	mark = h.snapshotCount()
	retry := pr88StartSync(ctx, a)
	pr88WaitHeld(t, swap, retry, "the swap of the root pointer of the retry at epoch 3")
	key3 := pr88OneNewKeyAt(t, h, mark, 3, "the retry, whose swap is held")
	if key3 == lost {
		t.Fatalf("the retry wrote the same snapshot key %q as the attempt before it", key3)
	}
	if got := h.rootNow(); got != root2 {
		t.Fatalf("while the retry's swap is held the root pointer names %q, want %q", got, root2)
	}

	list.Release()
	pr88WaitSignal(t, list.returned, "the held List to return")
	h.waitDeleteOrGrace()
	swap.Release()
	if err := retry.wait(t, "Sync of the retry at epoch 3"); err != nil {
		t.Errorf("Sync of the retry at epoch 3, once its swap is released = %v, want success: nothing else "+
			"moved the root pointer", err)
	}
	if got := h.rootNow(); got != key3 {
		t.Errorf("after the retry the root pointer names %q, want %q", got, key3)
	}

	pr88WaitPass(t, a, "the prune pass")
	pr88CheckEnd(t, ctx, h)
}

// TestPruneKeepsRootSnapshotAfterLostSwapReply is R7 of ADR 0019 section 6,
// from docs/tla/CommitProtocol_PruneSwapReply.cfg: #88's route 5, a swap of
// the root pointer that took effect and returned an error, whose retry sorts
// above it.
//
// The trace's steps, through the code path:
//   - InitEmpty, PutSnapshot [1,1] and SwapOK, which starts the pass: New over
//     the empty bucket, with SnapshotRetention 1; the harness holds the pass's
//     List;
//   - Mutate, PutSnapshot [2,1] and SwapReplyLost: a commit whose swap the
//     harness applies and then fails, so that the root pointer names its
//     epoch-2 snapshot while Sync returns an error;
//   - PutSnapshot [2,2]: Sync again, writing a second epoch-2 snapshot of the
//     mount's own;
//   - PruneList, keeping [2,2], and PruneDelete [2,1]: the List is released,
//     and the pass runs.
//
// Where it deviates (ADR 0019 Assumption 12): in the trace the retry's [2,2]
// sorts above the root's [2,1] by nonce, and its swap is pending. Nonces come
// from crypto/rand, so the test seeds, on the inner store, the object an
// attempt at epoch 2 with the highest nonce leaves, under nonce
// ffffffffffff, with the bytes of the root pointer's snapshot: it stands for
// the retry's snapshot sorting above. The mount's own retry fails its swap's
// condition on the ETag the lost reply left stale, as ADR 0019 section 6's row
// says.
//
// At 13aebc4 the pass keeps the seeded key as the newest and deletes the
// snapshot the root pointer names: the record holds it, and the remount fails.
func TestPruneKeepsRootSnapshotAfterLostSwapReply(t *testing.T) {
	h, ctx := pr88NewHarness(t, false)
	a, list := pr88Pruner(t, ctx, h, 1, "New over an empty bucket with SnapshotRetention 1")

	h.armSwapLost()
	mark := h.snapshotCount()
	if err := pr88Commit(t, ctx, a, "c2", "Sync of the commit whose swap's reply is lost"); err == nil {
		t.Fatalf("Sync of a commit whose swap of the root pointer returned an error = nil, want an error")
	}
	lost := pr88OneNewKeyAt(t, h, mark, 2, "the commit whose swap's reply was lost")
	if got := h.rootNow(); got != lost {
		t.Fatalf("after the lost swap reply the root pointer names %q, want %q: the swap took effect", got, lost)
	}
	pr88Seed(t, ctx, h, lost, 2)

	mark = h.snapshotCount()
	if err := pr88StartSync(ctx, a).wait(t, "Sync again after the lost swap reply"); err == nil {
		t.Fatalf("Sync again after the lost swap reply = nil, want an error: its swap fails its condition on " +
			"the stale ETag")
	}
	pr88OneNewKeyAt(t, h, mark, 2, "the Sync after the lost swap reply")
	if got := h.rootNow(); got != lost {
		t.Fatalf("after the retry's failed swap the root pointer names %q, want %q still", got, lost)
	}

	list.Release()
	pr88WaitPass(t, a, "the pass the init commit began")
	pr88CheckEnd(t, ctx, h)
}

// TestPruneKeepsRootSnapshotAfterLostFallbackReply is R8 of ADR 0019 section
// 6, from docs/tla/CommitProtocol_PruneFallbackReply.cfg: #88's route 5 by
// the unconditional fallback, a Put of the root pointer that took effect and
// returned an error, whose retry sorts above it.
//
// The trace's steps, through the code path:
//   - InitEmpty, PutSnapshot [1,1], SwapUnsupported and FallbackHead, which
//     start the pass: New over the empty bucket on the harness in
//     unconditional mode from the start, with SnapshotRetention 1, so the
//     init commit goes through the fallback; the harness holds the pass's
//     List;
//   - Mutate, PutSnapshot [2,1] and SwapUnsupported, whose fallback Put takes
//     effect and fails: a commit whose root Put the harness applies and then
//     fails, so that the root pointer names its epoch-2 snapshot while Sync
//     returns an error;
//   - PutSnapshot [2,2]: Sync again, its fallback Put of the root pointer
//     held pending by the harness;
//   - PruneList, keeping [2,2], and PruneDelete [2,1]: the List is released
//     and returns, and the test waits for the first Delete or the grace
//     period before it releases the root Put.
//
// Where it deviates (ADR 0019 Assumption 12): in the trace the retry's [2,2]
// sorts above the root's [2,1] by nonce. Nonces come from crypto/rand, so the
// test seeds, on the inner store, the object an attempt at epoch 2 with the
// highest nonce leaves, under nonce ffffffffffff, with the bytes of the root
// pointer's snapshot: it stands for the retry's snapshot sorting above. The
// retry's own key sorts either side of the root's.
//
// At 13aebc4 the pass keeps the seeded key as the newest and deletes the key
// the root pointer named before the retry's Put, and the retry's own. Which
// it deletes first depends on their random nonces, and the root Put is
// released after the first: so either the record holds the root's key, or the
// retry's Put leaves the root pointer naming a snapshot already deleted. In
// both, the test fails, naming the key, and the remount fails.
func TestPruneKeepsRootSnapshotAfterLostFallbackReply(t *testing.T) {
	h, ctx := pr88NewHarness(t, true)
	a, list := pr88Pruner(t, ctx, h, 1, "New over an empty bucket with SnapshotRetention 1, on a store with no "+
		"conditional writes")

	h.armRootPutLost()
	mark := h.snapshotCount()
	if err := pr88Commit(t, ctx, a, "c2", "Sync of the commit whose fallback root Put's reply is lost"); err == nil {
		t.Fatalf("Sync of a commit whose fallback Put of the root pointer returned an error = nil, want an error")
	}
	lost := pr88OneNewKeyAt(t, h, mark, 2, "the commit whose fallback root Put's reply was lost")
	if got := h.rootNow(); got != lost {
		t.Fatalf("after the lost fallback reply the root pointer names %q, want %q: the Put took effect", got, lost)
	}
	pr88Seed(t, ctx, h, lost, 2)

	put := h.armRootPutHold()
	mark = h.snapshotCount()
	retry := pr88StartSync(ctx, a)
	pr88WaitHeld(t, put, retry, "the fallback Put of the root pointer of the retry")
	retryKey := pr88OneNewKeyAt(t, h, mark, 2, "the retry, whose root Put is held")
	if got := h.rootNow(); got != lost {
		t.Fatalf("while the retry's root Put is held the root pointer names %q, want %q", got, lost)
	}

	list.Release()
	pr88WaitSignal(t, list.returned, "the held List to return")
	h.waitDeleteOrGrace()
	put.Release()
	if err := retry.wait(t, "Sync of the retry"); err != nil {
		t.Errorf("Sync of the retry, once its root Put is released = %v, want success", err)
	}
	if got := h.rootNow(); got != retryKey {
		t.Errorf("after the retry the root pointer names %q, want the retry's %q", got, retryKey)
	}

	pr88WaitPass(t, a, "the prune pass")
	pr88CheckEnd(t, ctx, h)
}

// TestSnapshotRetentionKeepsExactlyNBelowCurrent is R9 of ADR 0019 section 6:
// -snapshot-retention N keeps N superseded snapshots, as its help text says
// (ADR 0019 section 3). On a plain *store.Local, New with SnapshotRetention 3
// commits epoch 1; five commits make epochs 2 to 6; the test waits for the
// pass; one more commit makes epoch 7, and the test waits for its pass, so
// that it is not skipped (ADR 0019 Assumption 17). The bucket then holds
// exactly the snapshots of epochs 4, 5, 6 and 7, and the remount succeeds.
//
// At 13aebc4 the window counts the current snapshot, so the last pass keeps
// three keys in all, epochs 5 to 7.
func TestSnapshotRetentionKeepsExactlyNBelowCurrent(t *testing.T) {
	local := pr88Local(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	what := "New over an empty bucket with SnapshotRetention 3"
	fs := pr88MustNew(t, ctx, what, pr88Config(local, 3))
	pr88AwaitPassAtCleanup(t, nil, fs, "the prune pass of "+what)

	for epoch := 2; epoch <= 6; epoch++ {
		desc := fmt.Sprintf("Sync of the commit at epoch %d", epoch)
		if err := pr88Commit(t, ctx, fs, fmt.Sprintf("c%d", epoch), desc); err != nil {
			t.Fatalf("%s = %v, want success", desc, err)
		}
	}
	pr88WaitPass(t, fs, "the prune pass after the commit at epoch 6")
	if err := pr88Commit(t, ctx, fs, "c7", "Sync of the commit at epoch 7"); err != nil {
		t.Fatalf("Sync of the commit at epoch 7 = %v, want success", err)
	}
	pr88WaitPass(t, fs, "the prune pass the commit at epoch 7 began")

	infos, err := local.List(ctx, snapshotPrefix, "", 0)
	if err != nil {
		t.Fatalf("List(%q) on the bucket = %v", snapshotPrefix, err)
	}
	var epochs []uint64
	for _, info := range infos {
		e, ok := pr88Epoch(info.Key)
		if !ok {
			t.Errorf("the bucket holds %q under %q, which is not a snapshot key", info.Key, snapshotPrefix)
			continue
		}
		epochs = append(epochs, e)
	}
	slices.Sort(epochs)
	want := []uint64{4, 5, 6, 7}
	if !slices.Equal(epochs, want) {
		t.Errorf("after the pass the commit at epoch 7 began, with SnapshotRetention 3, the bucket's snapshots "+
			"are at epochs %v, want exactly %v: the current snapshot and the 3 newest below it (ADR 0019 "+
			"section 3: -snapshot-retention N keeps N superseded snapshots)", epochs, want)
	}
	pr88CheckRemount(t, ctx, local)
}
