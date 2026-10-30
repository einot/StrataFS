package blobfs

// Clean-room tests for #43, "Sync holds the namespace lock across object-store
// round trips", written without reading the implementation, from ADR 0023
// (docs/adr/0023-a-commit-seals-the-namespace-under-the-lock-and-uploads-outside-it.md)
// sections 1 to 5 and 11 and Assumptions 7 and 28, the facts and harness shape
// of ADR 0022 section 8, the safety rules of ADR 0019 section 6 and ADR 0011
// section 7, and internal/vfs/vfs.go.
//
// ADR 0023 section 1's rule: a commit holds FS.mu only to seal the namespace
// (encode it and fix the attempt) and to apply an outcome. Every store call a
// commit makes, the snapshot Put, the root-pointer swap and the probe's read
// among them, is made holding neither FS.mu nor the commit gate, and commits
// are serialised by a lock of their own. A commit's success clears FS.dirty
// only if nothing changed since its seal, so a change made while a commit is
// in flight is carried by the next commit (section 3). At d75d14a every one of
// those store calls is made holding the commit gate and FS.mu for writing, so
// every namespace operation waits for them.
//
// Covered: ADR 0023 section 11's tests, with the names it pins:
//   - U1, TestNamespaceOperationsDoNotWaitForACommitsStoreCalls (the issue's
//     done-when);
//   - U2, TestNamespaceOperationsDoNotWaitForTheRootPointerSwap;
//   - U3, TestAChangeMadeWhileACommitIsInFlightStaysDirty
//     (CommitProtocol_NoClearIfUnchanged's route);
//   - U4, TestAChangeMadeWhileALostSwapIsSettledStaysDirty;
//   - U5, TestASecondSyncWaitsForTheCommitInFlightAndCommitsOnlyTheRest;
//   - U6, TestASecondSyncWithNothingNewMakesNoStoreCall;
//   - U7, TestStableWriteDuringACommitIsDurableWhenItReturns;
//   - U8, TestTheTopOfCommitProbeHoldsNoLock;
//   - U9, TestADivergedMountsSyncMakesNoStoreCall;
//   - U10, TestEveryChangeAdvancesTheChangeCount;
//   - U11, TestTheReclaimOfARefusedCommitHoldsNoLock (ADR 0021 section 2's
//     reclaim; Assumption 37);
//   - U12, TestTheUnconditionalFallbackHoldsNoLock (ADR 0021 section 2's
//     opted-in fallback; Assumption 37).
//
// Written in two parts. The first used only names that exist at d75d14a, so
// that the package built there and the session could take a red run. The
// second, written once ADR 0023's code was on the branch, added every
// assertion section 11 makes on FS.changes, which is new in ADR 0023
// (sections 2 and 3): U3's two reads of the count, and U10 whole.
//
// Not covered, on purpose: what section 11 does not pin, namely the lock
// order itself, that FS.commitMu is the lock that serialises commits (no test
// touches FS.commitMu or FS.commitGate), the log records, and the error texts
// beyond the words sections 2 and 11 pin.
//
// The harness, uc43Store, is section 11's: a store.Store over a *store.Local
// under t.TempDir(), which does not embed store.Store and passes every call
// through to it, and, when armed, one-shot:
//   - holds the next Put under snapshotPrefix: closes its hold's entered
//     channel when the call is made, waits until the test releases it or the
//     test's context ends, then passes the call through;
//   - holds the next PutIfMatch of rootKey in the same way, then passes it
//     through;
//   - holds the next PutIfMatch of rootKey, then, once released, applies it on
//     the inner store and returns "" and uc43ErrInjected (SwapReplyLost);
//   - fails the first GetWithETag of rootKey after the next swap, without
//     calling the inner store, as rk5Store's arm of that name does (ADR 0022
//     section 8), so that a mount is left in doubt;
//   - holds the next GetWithETag of rootKey in the same way as the Put, then
//     passes it through;
//   - answers the next PutIfMatch of rootKey with store.ErrUnsupported without
//     calling the inner store, as sf91Store's one-shot arm of that kind does
//     (ADR 0021 section 6): one 501 from a store that honours every other
//     conditional write, recorded as answered by the harness;
//   - holds the next Get of rootKey in the same way, then passes it through:
//     the reclaim's read (ADR 0021 section 2);
//   - holds the next Delete under snapshotPrefix in the same way, then passes
//     it through: the reclaim's Delete;
//   - holds the next Put of rootKey in the same way, then passes it through:
//     the opted-in fallback's write, an arm of its own beside the held Put
//     under snapshotPrefix, the two told apart by key;
//   - holds the next Head of rootKey in the same way, then passes it through:
//     the fallback's Head;
//   - records, in call order (each call is recorded when it is made, and its
//     outcome filled in when it returns), every call of every method but Name:
//     so every Put under snapshotPrefix with its key, every PutIfMatch of
//     rootKey with its etag argument, its bytes and how it was answered, every
//     GetWithETag of rootKey and every Delete with its key, as section 11
//     lists, and every other call too, which U9's "no call of any kind" needs.
//
// The safety rules, as ADR 0023 section 11, ADR 0022 section 8, ADR 0019
// section 6 and ADR 0011 section 7 give them; every test here follows them:
//   - every New and every FS call, fs.Root() and every read of an FS field
//     included, runs in uc43Go, which recovers a panic, and is waited for
//     under uc43Bound, 10 s; goroutines never touch t, and nothing here is
//     parallel;
//   - every hold selects on the test's context, which a cleanup cancels, and
//     a cleanup that runs before it releases every hold; a third cleanup, which
//     runs after both and before t.TempDir's removal, waits under uc43Bound for
//     every call the test started, so that none outlives it on a failing path;
//   - "returns while held" means the call returned under the bound while the
//     hold's release had not been closed and the Sync goroutine that is held
//     had not finished (uc43Held checks both);
//   - every mount has SnapshotRetention -1 and MaxDirtyBytes -1, and no test
//     calls Run, so no prune pass, drain or committer runs;
//   - there is no time.Sleep and no polling: every wait is on a channel, under
//     uc43Bound;
//   - every bucket is a local directory under the test's t.TempDir(); the
//     bucket is inspected through the inner *store.Local only after the
//     harness's records have been read, and the inner store is written to only
//     to stand for another writer (U9's foreign root pointer);
//   - an FS field is read only holding FS.mu for reading, inside uc43Go, and
//     only the fields section 11 names: FS.dirty, FS.epoch, FS.rootSnapshot,
//     FS.rootETag, FS.changes and f.rootKnowledge.State(); f.mode.State() is
//     read without a lock (ADR 0014 section 11; ADR 0023 section 11); no
//     field is written.

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// uc43Bound is how long a test waits for any one step before it reports the
// step as hung (ADR 0023 section 11, the safety rules).
const uc43Bound = 10 * time.Second

// uc43Caller is the root directory's owner, as every mount here is configured
// (ADR 0022 section 8: a commit's Create is by vfs.Caller{UID: 501, GID: 20}).
var uc43Caller = vfs.Caller{UID: 501, GID: 20}

// uc43ErrInjected is the error of the harness's lost reply and failed read. It
// is none of store.ErrNotFound, store.ErrPrecondition and store.ErrUnsupported.
var uc43ErrInjected = errors.New("uc43: injected store fault")

// uc43Writer is the Writer of the foreign root pointer that stands for another
// writer (ADR 0022 section 8).
const uc43Writer = "other-host/4242"

// How the harness answered a call, as uc43Call.how records it.
const (
	uc43InProgress  = "made, not yet returned"
	uc43Passed      = "passed through"
	uc43HeldPassed  = "held, then passed through"
	uc43HeldLost    = "held, then applied by the inner store, its reply lost"
	uc43Failed      = "failed by the harness without calling the inner store"
	uc43Answered501 = "answered store.ErrUnsupported by the harness without calling the inner store"
)

// uc43Hold is one held call. entered is closed when the call is made and
// held; release is closed by the test, or by a cleanup, to let it go on.
type uc43Hold struct {
	what    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// Release lets the held call go on. It may be called more than once.
func (h *uc43Hold) Release() {
	h.once.Do(func() { close(h.release) })
}

// released reports whether Release has been called.
func (h *uc43Hold) released() bool {
	select {
	case <-h.release:
		return true
	default:
		return false
	}
}

// hold is run by the held call: it signals entered, then waits until the hold
// is released or testCtx ends.
func (h *uc43Hold) hold(testCtx context.Context) {
	close(h.entered)
	select {
	case <-h.release:
	case <-testCtx.Done():
	}
}

// uc43Call is the harness's record of one call: its method, its key (a List's
// prefix), the etag argument of a PutIfMatch, the bytes of a PutIfMatch of
// rootKey, how the harness answered it, and the error it returned.
type uc43Call struct {
	op   string
	key  string
	etag string
	data []byte
	how  string
	err  error
}

// String describes the call for failure messages.
func (c uc43Call) String() string {
	if c.op == "PutIfMatch" {
		return fmt.Sprintf("PutIfMatch(%q, etag %q): %s, returned %v", c.key, c.etag, c.how, c.err)
	}
	return fmt.Sprintf("%s(%q): %s, returned %v", c.op, c.key, c.how, c.err)
}

// uc43Store is ADR 0023 section 11's harness. It does not embed store.Store,
// so that every method it has is one written here.
type uc43Store struct {
	inner   *store.Local
	testCtx context.Context

	// mu guards every field below. It is never held across an inner call or
	// while a hold waits.
	mu sync.Mutex

	// snapPutHold holds the next Put under snapshotPrefix.
	snapPutHold *uc43Hold

	// swapLostHold holds the next PutIfMatch of rootKey, then applies it and
	// loses its reply; swapHold holds it, then passes it through. At most one
	// is taken by a call, swapLostHold first.
	swapLostHold *uc43Hold
	swapHold     *uc43Hold

	// failRead fails the next GetWithETag of rootKey; failReadAfterSwap sets
	// failRead at the next PutIfMatch of rootKey, however it is answered.
	failRead          bool
	failReadAfterSwap bool

	// readHold holds the next GetWithETag of rootKey, then passes it
	// through. A failRead is taken before it.
	readHold *uc43Hold

	// answer501 answers the next PutIfMatch of rootKey with
	// store.ErrUnsupported without calling the inner store. It is taken
	// before either swap hold.
	answer501 bool

	// rootGetHold holds the next Get of rootKey, snapDeleteHold the next
	// Delete under snapshotPrefix, rootPutHold the next Put of rootKey and
	// rootHeadHold the next Head of rootKey; each then passes the call
	// through.
	rootGetHold    *uc43Hold
	snapDeleteHold *uc43Hold
	rootPutHold    *uc43Hold
	rootHeadHold   *uc43Hold

	holds []*uc43Hold
	calls []uc43Call
}

var _ store.Store = (*uc43Store)(nil)

// newHold makes a hold and remembers it for releaseAll. The caller holds s.mu.
func (s *uc43Store) newHold(what string) *uc43Hold {
	h := &uc43Hold{what: what, entered: make(chan struct{}), release: make(chan struct{})}
	s.holds = append(s.holds, h)
	return h
}

// armHoldSnapshotPut holds the next Put under snapshotPrefix: a commit's
// snapshot Put.
func (s *uc43Store) armHoldSnapshotPut() *uc43Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapPutHold = s.newHold("the commit's snapshot Put under " + snapshotPrefix)
	return s.snapPutHold
}

// armHoldSwap holds the next PutIfMatch of rootKey, then passes it through: a
// commit's root-pointer swap.
func (s *uc43Store) armHoldSwap() *uc43Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swapHold = s.newHold("the commit's PutIfMatch of " + rootKey)
	return s.swapHold
}

// armHoldSwapThenLose holds the next PutIfMatch of rootKey, then, once
// released, applies it on the inner store and returns "" and uc43ErrInjected
// (SwapReplyLost).
func (s *uc43Store) armHoldSwapThenLose() *uc43Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swapLostHold = s.newHold("the commit's PutIfMatch of " + rootKey + ", whose reply the harness will lose")
	return s.swapLostHold
}

// armFailReadAfterSwap fails the first GetWithETag of rootKey after the next
// PutIfMatch of rootKey, without calling the inner store (ProbeFailed).
func (s *uc43Store) armFailReadAfterSwap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failReadAfterSwap = true
}

// armHoldRead holds the next GetWithETag of rootKey, then passes it through:
// the probe's read of the root pointer.
func (s *uc43Store) armHoldRead() *uc43Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readHold = s.newHold("the GetWithETag of " + rootKey + ", the probe's read of the root pointer")
	return s.readHold
}

// armAnswer501 answers the next PutIfMatch of rootKey with
// store.ErrUnsupported without calling the inner store, as sf91Store's
// one-shot arm does (ADR 0021 section 6).
func (s *uc43Store) armAnswer501() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answer501 = true
}

// armHoldRootGet holds the next Get of rootKey, then passes it through: the
// reclaim's read (ADR 0021 section 2).
func (s *uc43Store) armHoldRootGet() *uc43Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rootGetHold = s.newHold("the reclaim's Get of " + rootKey)
	return s.rootGetHold
}

// armHoldSnapshotDelete holds the next Delete under snapshotPrefix, then
// passes it through: the reclaim's Delete (ADR 0021 section 2).
func (s *uc43Store) armHoldSnapshotDelete() *uc43Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapDeleteHold = s.newHold("the reclaim's Delete under " + snapshotPrefix)
	return s.snapDeleteHold
}

// armHoldRootPut holds the next Put of rootKey, then passes it through: the
// opted-in fallback's write (ADR 0021 section 2).
func (s *uc43Store) armHoldRootPut() *uc43Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rootPutHold = s.newHold("the fallback's Put of " + rootKey)
	return s.rootPutHold
}

// armHoldRootHead holds the next Head of rootKey, then passes it through: the
// opted-in fallback's Head (ADR 0021 section 2).
func (s *uc43Store) armHoldRootHead() *uc43Hold {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rootHeadHold = s.newHold("the fallback's Head of " + rootKey)
	return s.rootHeadHold
}

// releaseAll releases every hold the harness has made.
func (s *uc43Store) releaseAll() {
	s.mu.Lock()
	holds := append([]*uc43Hold(nil), s.holds...)
	s.mu.Unlock()
	for _, h := range holds {
		h.Release()
	}
}

// records returns a copy of every call the harness has recorded, in the order
// the calls were made.
func (s *uc43Store) records() []uc43Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]uc43Call, len(s.calls))
	for i, c := range s.calls {
		c.data = append([]byte(nil), c.data...)
		out[i] = c
	}
	return out
}

// begin records a call as it is made and returns its index.
func (s *uc43Store) begin(c uc43Call) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.how = uc43InProgress
	s.calls = append(s.calls, c)
	return len(s.calls) - 1
}

// end records how the call begin returned i for was answered.
func (s *uc43Store) end(i int, how string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[i].how, s.calls[i].err = how, err
}

// take returns the hold in *slot and empties the slot when match holds, and
// returns nil otherwise: a hold is one-shot.
func (s *uc43Store) take(match bool, slot **uc43Hold) *uc43Hold {
	if !match {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := *slot
	*slot = nil
	return h
}

// await holds the call when hold is set, and returns how the call is
// answered if it then passes through.
func (s *uc43Store) await(hold *uc43Hold) string {
	if hold == nil {
		return uc43Passed
	}
	hold.hold(s.testCtx)
	return uc43HeldPassed
}

// Get passes through, except that when armed the next Get of rootKey is held
// first.
func (s *uc43Store) Get(ctx context.Context, key string) ([]byte, error) {
	hold := s.take(key == rootKey, &s.rootGetHold)
	i := s.begin(uc43Call{op: "Get", key: key})
	how := s.await(hold)
	data, err := s.inner.Get(ctx, key)
	s.end(i, how, err)
	return data, err
}

func (s *uc43Store) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	i := s.begin(uc43Call{op: "GetRange", key: key})
	data, err := s.inner.GetRange(ctx, key, off, n)
	s.end(i, uc43Passed, err)
	return data, err
}

// Head passes through, except that when armed the next Head of rootKey is
// held first.
func (s *uc43Store) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	hold := s.take(key == rootKey, &s.rootHeadHold)
	i := s.begin(uc43Call{op: "Head", key: key})
	how := s.await(hold)
	info, err := s.inner.Head(ctx, key)
	s.end(i, how, err)
	return info, err
}

// Put passes through, except that when armed the next Put under
// snapshotPrefix, and the next Put of rootKey, are each held first; the two
// arms are told apart by key.
func (s *uc43Store) Put(ctx context.Context, key string, data []byte) error {
	var hold *uc43Hold
	switch {
	case strings.HasPrefix(key, snapshotPrefix):
		hold = s.take(true, &s.snapPutHold)
	case key == rootKey:
		hold = s.take(true, &s.rootPutHold)
	}
	i := s.begin(uc43Call{op: "Put", key: key})
	how := s.await(hold)
	err := s.inner.Put(ctx, key, data)
	s.end(i, how, err)
	return err
}

// Delete passes through, except that when armed the next Delete under
// snapshotPrefix is held first.
func (s *uc43Store) Delete(ctx context.Context, key string) error {
	hold := s.take(strings.HasPrefix(key, snapshotPrefix), &s.snapDeleteHold)
	i := s.begin(uc43Call{op: "Delete", key: key})
	how := s.await(hold)
	err := s.inner.Delete(ctx, key)
	s.end(i, how, err)
	return err
}

func (s *uc43Store) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	i := s.begin(uc43Call{op: "List", key: prefix})
	out, err := s.inner.List(ctx, prefix, after, max)
	s.end(i, uc43Passed, err)
	return out, err
}

// PutIfMatch passes through, keeping the bytes of a call for rootKey, except
// that the next call for rootKey is answered store.ErrUnsupported when that
// is armed, or else takes at most one of the swap holds; either way it arms
// the failed read if it was armed to follow the next swap.
func (s *uc43Store) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (string, error) {
	c := uc43Call{op: "PutIfMatch", key: key, etag: etag}
	var hold *uc43Hold
	lose, unsupported := false, false
	if key == rootKey {
		c.data = append([]byte(nil), data...)
		s.mu.Lock()
		switch {
		case s.answer501:
			unsupported, s.answer501 = true, false
		case s.swapLostHold != nil:
			hold, s.swapLostHold, lose = s.swapLostHold, nil, true
		case s.swapHold != nil:
			hold, s.swapHold = s.swapHold, nil
		}
		if s.failReadAfterSwap {
			s.failReadAfterSwap, s.failRead = false, true
		}
		s.mu.Unlock()
	}
	i := s.begin(c)
	if unsupported {
		s.end(i, uc43Answered501, store.ErrUnsupported)
		return "", store.ErrUnsupported
	}
	how := uc43Passed
	if hold != nil {
		hold.hold(s.testCtx)
		how = uc43HeldPassed
	}
	newETag, err := s.inner.PutIfMatch(ctx, key, data, etag)
	if lose && err == nil {
		how, newETag, err = uc43HeldLost, "", uc43ErrInjected
	}
	s.end(i, how, err)
	return newETag, err
}

func (s *uc43Store) Name() string {
	return s.inner.Name()
}

// GetWithETag passes through, except that an armed failure answers the next
// GetWithETag of rootKey without calling the inner store, and an armed hold
// holds it first.
func (s *uc43Store) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	fail := false
	var hold *uc43Hold
	if key == rootKey {
		s.mu.Lock()
		if s.failRead {
			fail, s.failRead = true, false
		} else {
			hold, s.readHold = s.readHold, nil
		}
		s.mu.Unlock()
	}
	i := s.begin(uc43Call{op: "GetWithETag", key: key})
	if fail {
		s.end(i, uc43Failed, uc43ErrInjected)
		return nil, "", uc43ErrInjected
	}
	how := uc43Passed
	if hold != nil {
		hold.hold(s.testCtx)
		how = uc43HeldPassed
	}
	data, etag, err := s.inner.GetWithETag(ctx, key)
	s.end(i, how, err)
	return data, etag, err
}

// uc43IsSnapPut and the predicates below select calls from the harness's
// records.
func uc43IsSnapPut(c uc43Call) bool {
	return c.op == "Put" && strings.HasPrefix(c.key, snapshotPrefix)
}
func uc43IsRootSwap(c uc43Call) bool { return c.op == "PutIfMatch" && c.key == rootKey }
func uc43IsRootRead(c uc43Call) bool { return c.op == "GetWithETag" && c.key == rootKey }
func uc43IsRootGet(c uc43Call) bool  { return c.op == "Get" && c.key == rootKey }
func uc43IsRootPut(c uc43Call) bool  { return c.op == "Put" && c.key == rootKey }
func uc43IsRootHead(c uc43Call) bool { return c.op == "Head" && c.key == rootKey }
func uc43IsDelete(c uc43Call) bool   { return c.op == "Delete" }

// uc43Filter returns the calls keep selects, in order.
func uc43Filter(calls []uc43Call, keep func(uc43Call) bool) []uc43Call {
	var out []uc43Call
	for _, c := range calls {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// uc43First returns the index of the first call match selects, or -1.
func uc43First(calls []uc43Call, match func(uc43Call) bool) int {
	for i, c := range calls {
		if match(c) {
			return i
		}
	}
	return -1
}

// uc43Named returns the snapshot key a root pointer's bytes name, or "" if
// they do not decode as rootPointer.
func uc43Named(data []byte) string {
	var rp rootPointer
	if json.Unmarshal(data, &rp) != nil {
		return ""
	}
	return rp.Snapshot
}

// uc43KeyEpoch returns the epoch in a snapshot key: snapshotPrefix, the epoch
// as twenty decimal digits, "-", the nonce, ".json.gz" (ADR 0022 section 8;
// ADR 0019 section 6).
func uc43KeyEpoch(key string) (uint64, bool) {
	name, ok := strings.CutPrefix(key, snapshotPrefix)
	if !ok || len(name) < 21 || name[20] != '-' {
		return 0, false
	}
	e, err := strconv.ParseUint(name[:20], 10, 64)
	return e, err == nil
}

// uc43HasEpoch reports whether a snapshot key carries epoch.
func uc43HasEpoch(key string, epoch uint64) bool {
	e, ok := uc43KeyEpoch(key)
	return ok && e == epoch
}

// uc43ETag is *store.Local's ETag for body: the lowercase hexadecimal MD5 of it
// (ADR 0022 section 8, the facts).
func uc43ETag(body []byte) string {
	sum := md5.Sum(body)
	return hex.EncodeToString(sum[:])
}

// uc43WriteForeignRoot writes, on local, a foreign root pointer, which is how a
// test stands for another writer (ADR 0022 section 8, the facts): it reads
// rootKey, decodes it as rootPointer, sets Writer to writer, Epoch to epoch and
// Snapshot to a key under snapshotPrefix at that epoch with the nonce
// ffffffffffff, copies the bytes of the snapshot the root pointer named to that
// key, and puts the marshalled root pointer at rootKey. It touches no
// *testing.T.
func uc43WriteForeignRoot(ctx context.Context, local *store.Local, epoch uint64, writer string) (rootPointer, error) {
	raw, err := local.Get(ctx, rootKey)
	if err != nil {
		return rootPointer{}, fmt.Errorf("Get(%q): %w", rootKey, err)
	}
	var rp rootPointer
	if err := json.Unmarshal(raw, &rp); err != nil {
		return rootPointer{}, fmt.Errorf("decoding the root pointer %q as rootPointer: %w", raw, err)
	}
	snap, err := local.Get(ctx, rp.Snapshot)
	if err != nil {
		return rootPointer{}, fmt.Errorf("Get(%q), the snapshot the root pointer names: %w", rp.Snapshot, err)
	}
	rp.Writer = writer
	rp.Epoch = epoch
	rp.Snapshot = fmt.Sprintf("%s%020d-ffffffffffff.json.gz", snapshotPrefix, epoch)
	if err := local.Put(ctx, rp.Snapshot, snap); err != nil {
		return rootPointer{}, fmt.Errorf("Put(%q): %w", rp.Snapshot, err)
	}
	body, err := json.Marshal(rp)
	if err != nil {
		return rootPointer{}, fmt.Errorf("marshalling the foreign root pointer: %w", err)
	}
	if err := local.Put(ctx, rootKey, body); err != nil {
		return rootPointer{}, fmt.Errorf("Put(%q): %w", rootKey, err)
	}
	return rp, nil
}

// uc43Env is one test's harness, context and running calls.
type uc43Env struct {
	t   *testing.T
	ctx context.Context
	h   *uc43Store

	// mu guards running, the done channels of every call uc43Go started.
	mu      sync.Mutex
	running []chan struct{}
}

// uc43Setup makes a harness over a fresh local bucket under the test's
// temporary directory and the test's context. Its cleanups, in the order they
// run: release every hold; cancel the context; wait, under uc43Bound, for
// every call the test started, before t.TempDir's own cleanup removes the
// bucket.
func uc43Setup(t *testing.T) *uc43Env {
	t.Helper()
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &uc43Env{t: t, ctx: ctx, h: &uc43Store{inner: local, testCtx: ctx}}
	t.Cleanup(e.drain)
	t.Cleanup(cancel)
	t.Cleanup(e.h.releaseAll)
	return e
}

// drain waits, under one uc43Bound, for every call the test started to end.
// It runs after every hold is released and the context cancelled, so a call
// still running then is one a failing test abandoned; it is logged, not
// failed, since the test has failed already.
func (e *uc43Env) drain() {
	e.mu.Lock()
	running := append([]chan struct{}(nil), e.running...)
	e.mu.Unlock()
	deadline := time.NewTimer(uc43Bound)
	defer deadline.Stop()
	for _, done := range running {
		select {
		case <-done:
		case <-deadline.C:
			left := 0
			for _, d := range running {
				select {
				case <-d:
				default:
					left++
				}
			}
			e.t.Logf("at cleanup, %d of the %d calls this test started had not returned %v after every hold was "+
				"released and the test's context cancelled; they are abandoned (ADR 0023 section 11, the safety "+
				"rules)", left, len(running), uc43Bound)
			return
		}
	}
}

// uc43Done is one call running in its own goroutine. val, returned, panicked
// and pval may be read only once done is closed.
type uc43Done[T any] struct {
	done     chan struct{}
	val      T
	returned bool
	panicked bool
	pval     any
}

// uc43Go runs fn in its own goroutine, recovering and recording a panic out of
// it, and remembers it for e's cleanup. fn must not touch t.
func uc43Go[T any](e *uc43Env, fn func() T) *uc43Done[T] {
	c := &uc43Done[T]{done: make(chan struct{})}
	e.mu.Lock()
	e.running = append(e.running, c.done)
	e.mu.Unlock()
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

// finished reports, without waiting, whether the call has ended.
func (c *uc43Done[T]) finished() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// wait waits for the call under uc43Bound and returns its value. A hang, a
// panic or a runtime.Goexit fails the test; why says what a hang means.
func (c *uc43Done[T]) wait(t *testing.T, what, why string) T {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(uc43Bound):
		t.Fatalf("%s has not returned after %v, so it is hung: %s", what, uc43Bound, why)
	}
	if c.panicked {
		t.Fatalf("%s panicked with %v (%s)", what, c.pval, why)
	}
	if !c.returned {
		t.Fatalf("%s ended without returning (runtime.Goexit) (%s)", what, why)
	}
	return c.val
}

// uc43Watch names a running call that must not have finished.
type uc43Watch struct {
	name string
	done chan struct{}
}

func (c *uc43Done[T]) watch(name string) uc43Watch { return uc43Watch{name: name, done: c.done} }

// uc43Held runs fn while hold is held and returns what it returned: fn must
// return under uc43Bound, and when it has, hold must still be unreleased and
// none of watches, the Sync that is held, finished. That is section 11's
// "returns while held".
func uc43Held[T any](e *uc43Env, hold *uc43Hold, what, where string, fn func() T, watches ...uc43Watch) T {
	e.t.Helper()
	why := fmt.Sprintf("it waits for a commit's store call, which a commit makes holding neither FS.mu nor the "+
		"commit gate (ADR 0023 sections 1 and 4; %s)", where)
	v := uc43Go(e, fn).wait(e.t, what+", while "+hold.what+" is held", why)
	if hold.released() {
		e.t.Fatalf("test bug: %s was released before %s returned (%s)", hold.what, what, where)
	}
	for _, w := range watches {
		select {
		case <-w.done:
			e.t.Fatalf("%s returned, but %s had already returned, so %s did not hold it and the call did not run "+
				"while held (ADR 0023 section 11, \"returns while held\"; %s)", what, w.name, hold.what, where)
		default:
		}
	}
	return v
}

// uc43NotReturned requires c not to have finished: a call that queues behind
// a commit in flight (ADR 0023 section 4).
func uc43NotReturned[T any](t *testing.T, c *uc43Done[T], who, why string) {
	t.Helper()
	if c.finished() {
		t.Errorf("%s has returned (%+v, panicked %v), want it still waiting: %s", who, c.val, c.panicked, why)
	}
}

// uc43Config is every mount's Config: the root directory owned by uc43Caller,
// no pruning and no wait on the write budget (ADR 0023 section 11).
func uc43Config(st store.Store) Config {
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

// uc43R is a value and an error, as one call returns them.
type uc43R[T any] struct {
	v   T
	err error
}

// newFS runs New on the harness over its empty bucket, which must succeed.
func (e *uc43Env) newFS(where string) *FS {
	e.t.Helper()
	return e.newFSWith(uc43Config(e.h), where)
}

// newFSWith is newFS with the Config given whole, for U12's
// AllowUnconditionalRoot (ADR 0021 section 2).
func (e *uc43Env) newFSWith(cfg Config, where string) *FS {
	e.t.Helper()
	ctx := e.ctx
	r := uc43Go(e, func() uc43R[*FS] {
		fs, err := New(ctx, cfg)
		return uc43R[*FS]{fs, err}
	}).wait(e.t, "New over an empty bucket on the harness", where)
	if r.err != nil {
		e.t.Fatalf("New over an empty bucket on the harness = %v, want success (%s)", r.err, where)
	}
	if r.v == nil {
		e.t.Fatalf("New over an empty bucket on the harness returned a nil *FS and a nil error (%s)", where)
	}
	return r.v
}

// root returns fs.Root().
func (e *uc43Env) root(fs *FS, where string) vfs.Handle {
	e.t.Helper()
	return uc43Go(e, func() vfs.Handle { return fs.Root() }).wait(e.t, "Root", where)
}

// create creates name in dir, as uc43Caller, a regular file, exclusively; it
// must succeed.
func (e *uc43Env) create(fs *FS, dir vfs.Handle, name, where string) vfs.Handle {
	e.t.Helper()
	ctx := e.ctx
	what := fmt.Sprintf("Create %q", name)
	r := uc43Go(e, func() uc43R[vfs.Handle] {
		h, _, err := fs.Create(ctx, uc43Caller, dir, name, vfs.SetAttr{}, true)
		return uc43R[vfs.Handle]{h, err}
	}).wait(e.t, what, where)
	if r.err != nil {
		e.t.Fatalf("%s as the directory's owner (%d, %d) = %v, want success (%s)", what, uc43Caller.UID,
			uc43Caller.GID, r.err, where)
	}
	return r.v
}

// mkdir makes the directory name in dir, as uc43Caller; it must succeed.
func (e *uc43Env) mkdir(fs *FS, dir vfs.Handle, name, where string) vfs.Handle {
	e.t.Helper()
	ctx := e.ctx
	what := fmt.Sprintf("Mkdir %q", name)
	r := uc43Go(e, func() uc43R[vfs.Handle] {
		h, _, err := fs.Mkdir(ctx, uc43Caller, dir, name, vfs.SetAttr{})
		return uc43R[vfs.Handle]{h, err}
	}).wait(e.t, what, where)
	if r.err != nil {
		e.t.Fatalf("%s as the directory's owner = %v, want success (%s)", what, r.err, where)
	}
	return r.v
}

// symlink makes the symbolic link name in dir, to target, as uc43Caller; it
// must succeed.
func (e *uc43Env) symlink(fs *FS, dir vfs.Handle, name, target, where string) vfs.Handle {
	e.t.Helper()
	ctx := e.ctx
	what := fmt.Sprintf("Symlink %q to %q", name, target)
	r := uc43Go(e, func() uc43R[vfs.Handle] {
		h, _, err := fs.Symlink(ctx, uc43Caller, dir, name, target, vfs.SetAttr{})
		return uc43R[vfs.Handle]{h, err}
	}).wait(e.t, what, where)
	if r.err != nil {
		e.t.Fatalf("%s as the directory's owner = %v, want success (%s)", what, r.err, where)
	}
	return r.v
}

// write writes data at off of h, unstable, as uc43Caller; it must write all
// of data.
func (e *uc43Env) write(fs *FS, h vfs.Handle, off uint64, data []byte, what, where string) {
	e.t.Helper()
	ctx := e.ctx
	r := uc43Go(e, func() uc43R[uint32] {
		n, _, err := fs.Write(ctx, uc43Caller, h, off, data, vfs.Unstable)
		return uc43R[uint32]{n, err}
	}).wait(e.t, what, where)
	if r.err != nil || int(r.v) != len(data) {
		e.t.Fatalf("%s = (%d, %v), want (%d, nil) (%s)", what, r.v, r.err, len(data), where)
	}
}

// startSync starts fs.Sync in its own goroutine.
func (e *uc43Env) startSync(fs *FS) *uc43Done[error] {
	ctx := e.ctx
	return uc43Go(e, func() error { return fs.Sync(ctx) })
}

// sync runs fs.Sync and returns what it returned.
func (e *uc43Env) sync(fs *FS, what, where string) error {
	e.t.Helper()
	return e.startSync(fs).wait(e.t, what, where)
}

// commit is ADR 0022 section 8's commit: a Create of a fresh name in the root
// directory, by uc43Caller, which must succeed, then Sync. It returns what
// Sync returned.
func (e *uc43Env) commit(fs *FS, name, where string) error {
	e.t.Helper()
	e.create(fs, e.root(fs, where), name, where)
	return e.sync(fs, fmt.Sprintf("Sync of the commit of %q", name), where)
}

// waitEntered waits for hold to be entered by the Sync that c runs.
func (e *uc43Env) waitEntered(hold *uc43Hold, c *uc43Done[error], who, where string) {
	e.t.Helper()
	select {
	case <-hold.entered:
	case <-c.done:
		e.t.Fatalf("%s returned (err %v, panicked %v with %v) without reaching %s, so the test never held it (%s)",
			who, c.val, c.panicked, c.pval, hold.what, where)
	case <-time.After(uc43Bound):
		e.t.Fatalf("%s has not reached %s after %v (%s)", who, hold.what, uc43Bound, where)
	}
}

// uc43Fields is what read reads of an FS.
type uc43Fields struct {
	dirty        bool
	epoch        uint64
	rootSnapshot string
	rootETag     string
	changes      uint64
	knowledge    rootState
}

// read reads FS.dirty, FS.epoch, FS.rootSnapshot, FS.rootETag, FS.changes and
// f.rootKnowledge.State() in one hold of FS.mu for reading (ADR 0022 section
// 8; ADR 0023 section 11, the facts).
func (e *uc43Env) read(fs *FS, what, where string) uc43Fields {
	e.t.Helper()
	return uc43Go(e, func() uc43Fields {
		fs.mu.RLock()
		defer fs.mu.RUnlock()
		return uc43Fields{
			dirty:        fs.dirty,
			epoch:        fs.epoch,
			rootSnapshot: fs.rootSnapshot,
			rootETag:     fs.rootETag,
			changes:      fs.changes,
			knowledge:    fs.rootKnowledge.State(),
		}
	}).wait(e.t, what, where)
}

// mode returns fs.mode.State(), which needs no lock (ADR 0014 section 11).
func (e *uc43Env) mode(fs *FS, where string) mountMode {
	e.t.Helper()
	return uc43Go(e, func() mountMode { return fs.mode.State() }).wait(e.t, "f.mode.State()", where)
}

// uc43Lookup is what one Lookup returned.
type uc43Lookup struct {
	h    vfs.Handle
	attr vfs.Attr
	err  error
}

// lookup runs Lookup of name in dir as uc43Caller.
func (e *uc43Env) lookup(fs *FS, dir vfs.Handle, name, where string) uc43Lookup {
	e.t.Helper()
	ctx := e.ctx
	return uc43Go(e, func() uc43Lookup {
		h, a, err := fs.Lookup(ctx, uc43Caller, dir, name)
		return uc43Lookup{h, a, err}
	}).wait(e.t, fmt.Sprintf("Lookup of %q", name), where)
}

// dirNames lists dir with ReadDir, as uc43Caller, and returns its names other
// than "." and "..", in the order listed.
func (e *uc43Env) dirNames(fs *FS, dir vfs.Handle, what, where string) ([]string, error) {
	e.t.Helper()
	ctx := e.ctx
	r := uc43Go(e, func() uc43R[[]string] {
		var names []string
		var cookie uint64
		var verf [8]byte
		for range 1000 {
			ents, eof, outVerf, err := fs.ReadDir(ctx, uc43Caller, dir, cookie, verf, 1<<16, false)
			if err != nil {
				return uc43R[[]string]{names, err}
			}
			for _, de := range ents {
				if de.Name != "." && de.Name != ".." {
					names = append(names, de.Name)
				}
				cookie = de.Cookie
			}
			verf = outVerf
			if eof || len(ents) == 0 {
				return uc43R[[]string]{names, nil}
			}
		}
		return uc43R[[]string]{names, errors.New("ReadDir did not reach the end of the directory in 1000 calls")}
	}).wait(e.t, what, where)
	return r.v, r.err
}

// readFile reads h whole, as uc43Caller.
func (e *uc43Env) readFile(fs *FS, h vfs.Handle, what, where string) ([]byte, error) {
	e.t.Helper()
	ctx := e.ctx
	r := uc43Go(e, func() uc43R[[]byte] {
		var out []byte
		var off uint64
		for range 1000 {
			data, eof, err := fs.Read(ctx, uc43Caller, h, off, 4096)
			if err != nil {
				return uc43R[[]byte]{out, err}
			}
			out = append(out, data...)
			off += uint64(len(data))
			if eof || len(data) == 0 {
				return uc43R[[]byte]{out, nil}
			}
		}
		return uc43R[[]byte]{out, errors.New("Read did not reach the end of the file in 1000 calls")}
	}).wait(e.t, what, where)
	return r.v, r.err
}

// remount runs New on the inner store with SnapshotRetention -1 (ADR 0023
// section 11, the facts) and returns it, or nil, with the test failed, if New
// failed.
func (e *uc43Env) remount(where string) *FS {
	e.t.Helper()
	ctx, local := e.ctx, e.h.inner
	what := "the remount, New on the inner store with SnapshotRetention -1"
	r := uc43Go(e, func() uc43R[*FS] {
		fs, err := New(ctx, uc43Config(local))
		return uc43R[*FS]{fs, err}
	}).wait(e.t, what, where)
	if r.err != nil {
		e.t.Errorf("%s = %v, want success (%s)", what, r.err, where)
		return nil
	}
	if r.v == nil {
		e.t.Errorf("%s returned a nil *FS and a nil error (%s)", what, where)
		return nil
	}
	return r.v
}

// checkRemount requires a remount's root directory to hold every name in
// present and none in absent, and returns the remount, or nil.
func (e *uc43Env) checkRemount(present, absent []string, where string) *FS {
	e.t.Helper()
	rm := e.remount(where)
	if rm == nil {
		return nil
	}
	root := e.root(rm, where)
	for _, name := range present {
		if l := e.lookup(rm, root, name, where); l.err != nil {
			e.t.Errorf("Lookup of %q in the remount's root directory = %v, want success: the commit that holds it "+
				"reached the root pointer (ADR 0023 section 3; %s)", name, l.err, where)
		}
	}
	for _, name := range absent {
		if l := e.lookup(rm, root, name, where); !errors.Is(l.err, vfs.ErrNoEnt) {
			e.t.Errorf("Lookup of %q in the remount's root directory = %v, want vfs.ErrNoEnt: a change made while "+
				"a commit is in flight is not in that commit's snapshot (ADR 0023 section 3; %s)", name, l.err, where)
		}
	}
	return rm
}

// readsWhileHeld makes the read-only calls of U1's kind that U5, U6 and U7
// make after starting the call that must queue: GetAttr of the root
// directory, Lookup of name in it and ReadDir of it. Each must return while
// hold is held, and succeed.
func (e *uc43Env) readsWhileHeld(fs *FS, root vfs.Handle, name string, hold *uc43Hold, where string,
	watches ...uc43Watch) {
	e.t.Helper()
	ctx := e.ctx
	what := "GetAttr of the root directory"
	if err := uc43Held(e, hold, what, where, func() error {
		_, err := fs.GetAttr(ctx, root)
		return err
	}, watches...); err != nil {
		e.t.Errorf("%s, while %s is held, = %v, want success (ADR 0023 section 11; %s)", what, hold.what, err, where)
	}
	what = fmt.Sprintf("Lookup of %q in the root directory", name)
	if err := uc43Held(e, hold, what, where, func() error {
		_, _, err := fs.Lookup(ctx, uc43Caller, root, name)
		return err
	}, watches...); err != nil {
		e.t.Errorf("%s, while %s is held, = %v, want success (ADR 0023 section 11; %s)", what, hold.what, err, where)
	}
	what = "ReadDir of the root directory"
	if err := uc43Held(e, hold, what, where, func() error {
		_, _, _, err := fs.ReadDir(ctx, uc43Caller, root, 0, [8]byte{}, 1<<16, false)
		return err
	}, watches...); err != nil {
		e.t.Errorf("%s, while %s is held, = %v, want success (ADR 0023 section 11; %s)", what, hold.what, err, where)
	}
}

// uc43NamespaceWhileHeld is U1 with the snapshot Put held, and U2 with the
// root-pointer swap held in its place (ADR 0023 section 11).
func uc43NamespaceWhileHeld(t *testing.T, heldSwap bool, where string) {
	e := uc43Setup(t)
	ctx := e.ctx
	fs := e.newFS(where)
	root := e.root(fs, where)

	payload := bytes.Repeat([]byte("uc43"), 25) // 100 bytes
	aH := e.create(fs, root, "a.bin", where)
	e.write(fs, aH, 0, payload, `Write of 100 bytes at 0 of "a.bin", vfs.Unstable`, where)
	dH := e.mkdir(fs, root, "d", where)
	sH := e.symlink(fs, root, "s", "a.bin", where)

	var hold *uc43Hold
	if heldSwap {
		hold = e.h.armHoldSwap()
	} else {
		hold = e.h.armHoldSnapshotPut()
	}
	mark := len(e.h.records())
	syncA := e.startSync(fs)
	e.waitEntered(hold, syncA, "the first Sync", where)
	a := syncA.watch("the first Sync")

	// Each of these returns while held, and succeeds unless said otherwise
	// (ADR 0023 section 11, U1).
	heldOK := func(what string, fn func() error) {
		t.Helper()
		if err := uc43Held(e, hold, what, where, fn, a); err != nil {
			t.Errorf("%s, while %s is held, = %v, want success (ADR 0023 section 11; %s)", what, hold.what, err,
				where)
		}
	}
	heldOK("GetAttr of the root directory", func() error {
		_, err := fs.GetAttr(ctx, root)
		return err
	})
	heldOK(`Lookup of "a.bin"`, func() error {
		_, _, err := fs.Lookup(ctx, uc43Caller, root, "a.bin")
		return err
	})
	heldOK("ReadDir of the root directory", func() error {
		_, _, _, err := fs.ReadDir(ctx, uc43Caller, root, 0, [8]byte{}, 1<<16, false)
		return err
	})
	heldOK("Access of the root directory", func() error {
		_, err := fs.Access(ctx, uc43Caller, root, vfs.AccessRead|vfs.AccessLookup)
		return err
	})
	heldOK("FSStat", func() error {
		_, err := fs.FSStat(ctx, root)
		return err
	})
	link := uc43Held(e, hold, `ReadLink of "s"`, where, func() uc43R[string] {
		s, err := fs.ReadLink(ctx, sH)
		return uc43R[string]{s, err}
	}, a)
	if link.err != nil || link.v != "a.bin" {
		t.Errorf("ReadLink of %q, while %s is held, = (%q, %v), want (%q, nil) (ADR 0023 section 11; %s)", "s",
			hold.what, link.v, link.err, "a.bin", where)
	}
	got := uc43Held(e, hold, `Read of "a.bin"'s 100 bytes`, where, func() uc43R[[]byte] {
		data, _, err := fs.Read(ctx, uc43Caller, aH, 0, 100)
		return uc43R[[]byte]{data, err}
	}, a)
	if got.err != nil || !bytes.Equal(got.v, payload) {
		t.Errorf("Read of %q's 100 bytes, while %s is held, = (%d bytes %q, %v), want the 100 bytes written (ADR "+
			"0023 section 11; ADR 0005 section 2; %s)", "a.bin", hold.what, len(got.v), got.v, got.err, where)
	}
	heldOK(`Create "b" in the root directory`, func() error {
		_, _, err := fs.Create(ctx, uc43Caller, root, "b", vfs.SetAttr{}, true)
		return err
	})
	heldOK(`Mkdir "e" in "d"`, func() error {
		_, _, err := fs.Mkdir(ctx, uc43Caller, dH, "e", vfs.SetAttr{})
		return err
	})
	heldOK(`Rename of "e" to "f" in "d"`, func() error {
		return fs.Rename(ctx, uc43Caller, dH, "e", dH, "f")
	})
	heldOK(`Remove of "b"`, func() error {
		return fs.Remove(ctx, uc43Caller, root, "b")
	})
	mode := uint32(0o640)
	heldOK(`SetAttr of "a.bin"'s mode`, func() error {
		_, err := fs.SetAttr(ctx, uc43Caller, aH, vfs.SetAttr{Mode: &mode})
		return err
	})
	wr := uc43Held(e, hold, `Write of 10 bytes at 100 of "a.bin", vfs.Unstable`, where, func() uc43R[uint32] {
		n, _, err := fs.Write(ctx, uc43Caller, aH, 100, []byte("0123456789"), vfs.Unstable)
		return uc43R[uint32]{n, err}
	}, a)
	if wr.err != nil || wr.v != 10 {
		t.Errorf("Write of 10 bytes at 100 of %q, while %s is held, = (%d, %v), want (10, nil) (ADR 0023 section "+
			"11; %s)", "a.bin", hold.what, wr.v, wr.err, where)
	}
	attr := uc43Held(e, hold, `GetAttr of "a.bin" after that Write`, where, func() uc43R[vfs.Attr] {
		at, err := fs.GetAttr(ctx, aH)
		return uc43R[vfs.Attr]{at, err}
	}, a)
	if attr.err != nil || attr.v.Size != 110 {
		t.Errorf("GetAttr of %q after the Write at 100, while %s is held, = (size %d, %v), want (size 110, nil) "+
			"(ADR 0023 section 11; %s)", "a.bin", hold.what, attr.v.Size, attr.err, where)
	}
	size := uint64(50)
	heldOK(`SetAttr of "a.bin"'s size to 50, a truncate`, func() error {
		_, err := fs.SetAttr(ctx, uc43Caller, aH, vfs.SetAttr{Size: &size})
		return err
	})

	hold.Release()
	if err := syncA.wait(t, "the first Sync", where); err != nil {
		t.Errorf("the first Sync, once %s was released, = %v, want nil (ADR 0023 section 11; %s)", hold.what, err,
			where)
	}
	first := e.h.records()
	calls := first[mark:]
	if snaps, swaps := uc43Filter(calls, uc43IsSnapPut), uc43Filter(calls, uc43IsRootSwap); len(snaps) != 1 ||
		len(swaps) != 1 {
		t.Errorf("in the first Sync the harness recorded the Puts under %q %v and the PutIfMatch calls of %q %v, "+
			"want exactly one of each (ADR 0022 section 8, a commit; ADR 0023 section 11; %s)", snapshotPrefix,
			snaps, rootKey, swaps, where)
	}
	if f := e.read(fs, "reading FS.dirty after the first Sync", where); !f.dirty {
		t.Errorf("after the first Sync, FS.dirty is false, want true: the changes made while %s was held are not "+
			"in that commit, so the namespace stays dirty (ADR 0023 section 3; %s)", hold.what, where)
	}

	// The changes made while held are not in that commit.
	firstWhere := where + ", the remount after the first Sync"
	if rm := e.checkRemount([]string{"a.bin", "d"}, nil, firstWhere); rm != nil {
		rmRoot := e.root(rm, firstWhere)
		if l := e.lookup(rm, rmRoot, "a.bin", firstWhere); l.err == nil && l.attr.Size != 100 {
			t.Errorf("in the remount after the first Sync %q has size %d, want 100: the Write at 100 and the "+
				"truncate made while held are not in that commit (ADR 0023 section 3; %s)", "a.bin", l.attr.Size,
				firstWhere)
		}
		if l := e.lookup(rm, rmRoot, "d", firstWhere); l.err == nil {
			names, err := e.dirNames(rm, l.h, `ReadDir of "d" in the remount`, firstWhere)
			if err != nil || len(names) != 0 {
				t.Errorf("in the remount after the first Sync, %q lists %q (err %v), want no entry: the Mkdir and "+
					"Rename made while held are not in that commit (ADR 0023 section 3; %s)", "d", names, err,
					firstWhere)
			}
		}
	}

	// The next commit carries them.
	if err := e.sync(fs, "the second Sync", where); err != nil {
		t.Errorf("the second Sync = %v, want nil (ADR 0023 section 3; %s)", err, where)
	}
	calls = e.h.records()[len(first):]
	if snaps, swaps := uc43Filter(calls, uc43IsSnapPut), uc43Filter(calls, uc43IsRootSwap); len(snaps) != 1 ||
		len(swaps) != 1 {
		t.Errorf("in the second Sync the harness recorded the Puts under %q %v and the PutIfMatch calls of %q %v, "+
			"want exactly one more of each: the namespace was dirty, so the second Sync commits (ADR 0023 section "+
			"3; %s)", snapshotPrefix, snaps, rootKey, swaps, where)
	}
	secondWhere := where + ", the remount after the second Sync"
	if rm := e.checkRemount([]string{"a.bin", "d"}, []string{"b"}, secondWhere); rm != nil {
		rmRoot := e.root(rm, secondWhere)
		if l := e.lookup(rm, rmRoot, "a.bin", secondWhere); l.err == nil && l.attr.Size != 50 {
			t.Errorf("in the remount after the second Sync %q has size %d, want 50: the truncate made while held "+
				"is in the next commit (ADR 0023 section 3; %s)", "a.bin", l.attr.Size, secondWhere)
		}
		if l := e.lookup(rm, rmRoot, "d", secondWhere); l.err == nil {
			names, err := e.dirNames(rm, l.h, `ReadDir of "d" in the remount`, secondWhere)
			if err != nil || !slices.Equal(names, []string{"f"}) {
				t.Errorf("in the remount after the second Sync, %q lists %q (err %v), want exactly [\"f\"]: the "+
					"Mkdir and Rename made while held are in the next commit (ADR 0023 section 3; %s)", "d", names,
					err, secondWhere)
			}
		}
	}
}

// TestNamespaceOperationsDoNotWaitForACommitsStoreCalls is U1 of ADR 0023
// section 11, the issue's done-when: while a commit's snapshot Put is held by
// the store, every namespace operation, a size change included, returns
// without waiting for it (sections 1 and 4); the changes made then are not in
// that commit, leave FS.dirty true, and are in the next (section 3).
//
// New over an empty bucket; a.bin created and 100 bytes written at 0, unstable;
// the directory d and the symlink s to "a.bin" made. The snapshot Put held;
// Sync started, and the hold entered. While held each returns, and succeeds:
// GetAttr of the root, Lookup of a.bin, ReadDir of the root, Access, FSStat,
// ReadLink of s, Read of a.bin's 100 bytes (which returns them), Create of b,
// Mkdir of e in d, Rename of e to f, Remove of b, SetAttr of a.bin's mode, a
// Write of 10 bytes at 100 of a.bin (after which GetAttr reports size 110),
// and SetAttr of a.bin's size to 50. Released: Sync returns nil, with one Put
// under snapshotPrefix and one PutIfMatch of rootKey; FS.dirty is true; a
// remount has a.bin at size 100 and d with no entry. A second Sync returns
// nil, with one more of each; a remount has a.bin at size 50, f under d, and
// no b.
//
// At d75d14a it hangs to the 10 s bound at its first call made while held,
// GetAttr of the root, since FS.mu is held across the snapshot Put.
func TestNamespaceOperationsDoNotWaitForACommitsStoreCalls(t *testing.T) {
	uc43NamespaceWhileHeld(t, false, "ADR 0023 section 11, U1")
}

// TestNamespaceOperationsDoNotWaitForTheRootPointerSwap is U2 of ADR 0023
// section 11: U1 with the commit's PutIfMatch of rootKey held in place of its
// snapshot Put, and the same calls made while held, SetAttr of a size
// included, each returning while held (sections 1 and 4). After the release,
// U1's assertions on FS.dirty, the remounts and the second Sync.
//
// At d75d14a it hangs to the 10 s bound at its first call made while held,
// GetAttr of the root, since FS.mu is held across the swap.
func TestNamespaceOperationsDoNotWaitForTheRootPointerSwap(t *testing.T) {
	uc43NamespaceWhileHeld(t, true, "ADR 0023 section 11, U2")
}

// TestAChangeMadeWhileACommitIsInFlightStaysDirty is U3 of ADR 0023 section 11,
// from CommitProtocol_NoClearIfUnchanged's route: a commit's success clears
// FS.dirty only if nothing changed since its seal (section 3, the guard).
//
// One commit behind it, New's; the snapshot Put held; c2 created; Sync
// started, and the hold entered; c3 created while held, returning while held.
// Released: Sync returns nil; FS.dirty is true; FS.epoch is 2; a remount has
// c2 and not c3. Sync again: returns nil, with a second Put under
// snapshotPrefix whose key carries epoch 3 and a second PutIfMatch of rootKey;
// FS.dirty is false; a remount has c2 and c3. FS.changes, read holding FS.mu
// for reading before the Sync started and again after c3's Create returned
// while held, is greater the second time.
//
// At d75d14a it hangs to the 10 s bound at the Create of c3 while held, since
// FS.mu is held across the snapshot Put. A build that runs the store calls off
// the lock but clears the flag unconditionally fails on FS.dirty after the
// first Sync and on the second Sync's Put count, which is the route TLC
// reports for the MustFail row (section 11).
func TestAChangeMadeWhileACommitIsInFlightStaysDirty(t *testing.T) {
	const where = "ADR 0023 section 11, U3"
	e := uc43Setup(t)
	ctx := e.ctx
	fs := e.newFS(where)
	root := e.root(fs, where)

	hold := e.h.armHoldSnapshotPut()
	e.create(fs, root, "c2", where)
	beforeSync := e.read(fs, "reading FS.changes before the Sync started", where)
	mark := len(e.h.records())
	syncA := e.startSync(fs)
	e.waitEntered(hold, syncA, "the Sync of the commit of c2", where)
	a := syncA.watch("the Sync of the commit of c2")
	if err := uc43Held(e, hold, `Create "c3" in the root directory`, where, func() error {
		_, _, err := fs.Create(ctx, uc43Caller, root, "c3", vfs.SetAttr{}, true)
		return err
	}, a); err != nil {
		t.Fatalf("Create %q, while %s is held, = %v, want success (ADR 0023 section 11; %s)", "c3", hold.what, err,
			where)
	}
	afterC3 := uc43Held(e, hold, "reading FS.changes after c3's Create returned", where, func() uint64 {
		fs.mu.RLock()
		defer fs.mu.RUnlock()
		return fs.changes
	}, a)
	if afterC3 <= beforeSync.changes {
		t.Errorf("FS.changes after c3's Create returned, while %s is held, = %d, want greater than %d, its value "+
			"before the Sync started: every change that dirties the namespace advances the count, in the same "+
			"hold of FS.mu (ADR 0023 section 3, the change count; section 11, U3; %s)", hold.what, afterC3,
			beforeSync.changes, where)
	}

	hold.Release()
	if err := syncA.wait(t, "the Sync of the commit of c2", where); err != nil {
		t.Errorf("the Sync of the commit of c2, once %s was released, = %v, want nil (ADR 0023 section 3; %s)",
			hold.what, err, where)
	}
	first := e.h.records()
	if snaps := uc43Filter(first[mark:], uc43IsSnapPut); len(snaps) != 1 || !uc43HasEpoch(snaps[0].key, 2) {
		t.Errorf("in the Sync of the commit of c2 the harness recorded the Puts under %q %v, want exactly one, at "+
			"epoch 2 (ADR 0022 section 8, a commit; %s)", snapshotPrefix, snaps, where)
	}
	f := e.read(fs, "reading the FS's fields after the first Sync", where)
	if !f.dirty {
		t.Errorf("after the first Sync, FS.dirty is false, want true: c3 was created after that commit's seal, so "+
			"it is not in the snapshot the root pointer now names (ADR 0023 section 3, the guard; %s)", where)
	}
	if f.epoch != 2 {
		t.Errorf("after the first Sync, FS.epoch = %d, want 2 (ADR 0023 section 3; %s)", f.epoch, where)
	}
	e.checkRemount([]string{"c2"}, []string{"c3"}, where+", the remount after the first Sync")

	if err := e.sync(fs, "the second Sync", where); err != nil {
		t.Errorf("the second Sync = %v, want nil: it commits what the first did not seal (ADR 0023 section 3; %s)",
			err, where)
	}
	all := e.h.records()[mark:]
	snaps, swaps := uc43Filter(all, uc43IsSnapPut), uc43Filter(all, uc43IsRootSwap)
	if len(snaps) != 2 || !uc43HasEpoch(snaps[1].key, 3) || len(swaps) != 2 {
		t.Errorf("since the first Sync began the harness recorded the Puts under %q %v and the PutIfMatch calls of "+
			"%q %v, want two of each, the second Put at epoch 3: the namespace was left dirty, so the second Sync "+
			"commits (ADR 0023 section 3; %s)", snapshotPrefix, snaps, rootKey, swaps, where)
	}
	if f := e.read(fs, "reading FS.dirty after the second Sync", where); f.dirty {
		t.Errorf("after the second Sync, FS.dirty is true, want false: nothing changed since its seal (ADR 0023 "+
			"section 3, the guard; %s)", where)
	}
	e.checkRemount([]string{"c2", "c3"}, nil, where+", the remount after the second Sync")
}

// TestAChangeMadeWhileALostSwapIsSettledStaysDirty is U4 of ADR 0023 section
// 11: the guard on FS.dirty holds at the failure-site adoption too, where a
// swap whose reply was lost is settled by one read that finds the attempt
// (sections 2, 3 and 5; ADR 0022 section 4, probeOurs).
//
// One commit behind it; the held-then-lost swap armed; c2 created; Sync
// started, and the hold entered; c3 created while held, returning while held.
// Released: the swap is applied and answered with uc43ErrInjected, the read
// that follows finds the root pointer naming the attempt, and Sync returns
// nil. After that PutIfMatch the harness recorded exactly one GetWithETag of
// rootKey and no further PutIfMatch; FS.dirty is true; rootKnowledge is
// rootKnown; FS.epoch is 2; FS.rootSnapshot is the key the swap wrote; a
// remount has c2 and not c3. Sync again: returns nil, its PutIfMatch passed
// through with the MD5 of that root pointer as its etag argument and its
// snapshot key at epoch 3; a remount has c2 and c3.
//
// At d75d14a it hangs to the 10 s bound at the Create of c3 while held, since
// FS.mu is held across the swap. A build that runs the store calls off the
// lock but clears the flag unconditionally on this path fails on FS.dirty
// after the first Sync (section 11; Assumption 13).
func TestAChangeMadeWhileALostSwapIsSettledStaysDirty(t *testing.T) {
	const where = "ADR 0023 section 11, U4"
	e := uc43Setup(t)
	ctx := e.ctx
	fs := e.newFS(where)
	root := e.root(fs, where)

	hold := e.h.armHoldSwapThenLose()
	e.create(fs, root, "c2", where)
	mark := len(e.h.records())
	syncA := e.startSync(fs)
	e.waitEntered(hold, syncA, "the Sync of the commit of c2", where)
	a := syncA.watch("the Sync of the commit of c2")
	if err := uc43Held(e, hold, `Create "c3" in the root directory`, where, func() error {
		_, _, err := fs.Create(ctx, uc43Caller, root, "c3", vfs.SetAttr{}, true)
		return err
	}, a); err != nil {
		t.Fatalf("Create %q, while %s is held, = %v, want success (ADR 0023 section 11; %s)", "c3", hold.what, err,
			where)
	}

	hold.Release()
	if err := syncA.wait(t, "the Sync of the commit of c2", where); err != nil {
		t.Errorf("the Sync of the commit of c2, whose swap was applied and its reply lost, = %v, want nil: the "+
			"read finds the root pointer naming the attempt and adopts it (ADR 0022 section 4, probeOurs; ADR 0023 "+
			"section 2; %s)", err, where)
	}
	first := e.h.records()
	calls := first[mark:]
	var lostData []byte
	lost := uc43First(calls, func(c uc43Call) bool { return uc43IsRootSwap(c) && c.how == uc43HeldLost })
	if lost < 0 {
		t.Errorf("the commit of c2 made no PutIfMatch of %q that the harness held, applied and lost the reply of "+
			"(%s). Every call since the commit began: %v", rootKey, where, calls)
	} else {
		lostData = calls[lost].data
		after := calls[lost+1:]
		if reads := uc43Filter(after, uc43IsRootRead); len(reads) != 1 {
			t.Errorf("after the lost swap the harness recorded the GetWithETag calls of %q %v, want exactly one, the "+
				"read that settles the doubt (ADR 0022 section 3; ADR 0023 section 5; %s)", rootKey, reads, where)
		}
		if swaps := uc43Filter(after, uc43IsRootSwap); len(swaps) != 0 {
			t.Errorf("after the lost swap the harness recorded the PutIfMatch calls of %q %v, want none (ADR 0022 "+
				"section 4, probeOurs; %s)", rootKey, swaps, where)
		}
	}
	f := e.read(fs, "reading the FS's fields after the first Sync", where)
	if !f.dirty {
		t.Errorf("after the first Sync, FS.dirty is false, want true: c3 was created after that commit's seal, and "+
			"the adoption at the failure site takes the guard too (ADR 0023 sections 3 and 5; %s)", where)
	}
	if f.knowledge != rootKnown {
		t.Errorf("after the first Sync, f.rootKnowledge.State() = %v, want %v (ADR 0022 section 4, probeOurs; %s)",
			f.knowledge, rootKnown, where)
	}
	if f.epoch != 2 {
		t.Errorf("after the first Sync, FS.epoch = %d, want 2: the adopted attempt's (ADR 0023 section 3; %s)",
			f.epoch, where)
	}
	if lostData != nil && f.rootSnapshot != uc43Named(lostData) {
		t.Errorf("after the first Sync, FS.rootSnapshot = %q, want %q, the key the swap wrote (ADR 0022 section 4, "+
			"probeOurs; ADR 0023 section 5; %s)", f.rootSnapshot, uc43Named(lostData), where)
	}
	e.checkRemount([]string{"c2"}, []string{"c3"}, where+", the remount after the first Sync")

	if err := e.sync(fs, "the second Sync", where); err != nil {
		t.Errorf("the second Sync = %v, want nil: it commits what the first did not seal (ADR 0023 section 3; %s)",
			err, where)
	}
	calls = e.h.records()[len(first):]
	snaps, swaps := uc43Filter(calls, uc43IsSnapPut), uc43Filter(calls, uc43IsRootSwap)
	ok := len(snaps) == 1 && uc43HasEpoch(snaps[0].key, 3) && len(swaps) == 1 && swaps[0].how == uc43Passed &&
		swaps[0].err == nil
	if ok && lostData != nil && swaps[0].etag != uc43ETag(lostData) {
		ok = false
	}
	if !ok {
		want := "(unknown: the lost swap was not recorded)"
		if lostData != nil {
			want = fmt.Sprintf("%q", uc43ETag(lostData))
		}
		t.Errorf("in the second Sync the harness recorded the Puts under %q %v and the PutIfMatch calls of %q %v, "+
			"want one Put at epoch 3 and one PutIfMatch passed through with the etag argument %s, the MD5 of the "+
			"root pointer the lost swap wrote (ADR 0022 section 4; ADR 0023 section 3; %s)", snapshotPrefix, snaps,
			rootKey, swaps, want, where)
	}
	e.checkRemount([]string{"c2", "c3"}, nil, where+", the remount after the second Sync")
}

// TestASecondSyncWaitsForTheCommitInFlightAndCommitsOnlyTheRest is U5 of ADR
// 0023 section 11: a second Sync queues behind a commit in flight, and then
// commits what that commit did not seal, at the next epoch, against the ETag
// the first recorded (sections 3 and 4).
//
// One commit behind it; the snapshot Put held; c2 created; Sync A started, and
// the hold entered; c3 created while held; Sync B started; then GetAttr of the
// root, Lookup of c3 and ReadDir of the root each return while held, after
// which B has not returned (Assumption 28: so that the assertion is not a race
// against B's first pass alone). Released: A returns nil; B returns nil. The
// harness recorded two Puts under snapshotPrefix, at epochs 2 and 3 in that
// order, and two PutIfMatch calls of rootKey, the second's etag argument the
// MD5 of the root pointer the first wrote. A remount has c2 and c3; FS.dirty
// is false.
//
// At d75d14a it hangs to the 10 s bound at the Create of c3 while held, since
// FS.mu is held across the snapshot Put. A build that unlocks the store calls
// but lets B through without a commit of its own fails on the Put count and
// the remount (section 11; Assumption 28).
func TestASecondSyncWaitsForTheCommitInFlightAndCommitsOnlyTheRest(t *testing.T) {
	const where = "ADR 0023 section 11, U5"
	e := uc43Setup(t)
	ctx := e.ctx
	fs := e.newFS(where)
	root := e.root(fs, where)

	hold := e.h.armHoldSnapshotPut()
	e.create(fs, root, "c2", where)
	mark := len(e.h.records())
	syncA := e.startSync(fs)
	e.waitEntered(hold, syncA, "Sync A", where)
	a := syncA.watch("Sync A")
	if err := uc43Held(e, hold, `Create "c3" in the root directory`, where, func() error {
		_, _, err := fs.Create(ctx, uc43Caller, root, "c3", vfs.SetAttr{}, true)
		return err
	}, a); err != nil {
		t.Fatalf("Create %q, while %s is held, = %v, want success (ADR 0023 section 11; %s)", "c3", hold.what, err,
			where)
	}
	syncB := e.startSync(fs)
	e.readsWhileHeld(fs, root, "c3", hold, where, a)
	uc43NotReturned(t, syncB, "Sync B, started while A's snapshot Put was held",
		"a Sync queues behind a commit in flight and returns after it (ADR 0023 section 4; section 11, U5)")

	hold.Release()
	if err := syncA.wait(t, "Sync A", where); err != nil {
		t.Errorf("Sync A, once %s was released, = %v, want nil (ADR 0023 section 3; %s)", hold.what, err, where)
	}
	if err := syncB.wait(t, "Sync B", where); err != nil {
		t.Errorf("Sync B = %v, want nil: it commits what A did not seal (ADR 0023 sections 3 and 4; %s)", err, where)
	}
	calls := e.h.records()[mark:]
	snaps, swaps := uc43Filter(calls, uc43IsSnapPut), uc43Filter(calls, uc43IsRootSwap)
	ok := len(snaps) == 2 && uc43HasEpoch(snaps[0].key, 2) && uc43HasEpoch(snaps[1].key, 3) && len(swaps) == 2 &&
		swaps[1].etag == uc43ETag(swaps[0].data)
	if !ok {
		t.Errorf("since Sync A began the harness recorded the Puts under %q %v and the PutIfMatch calls of %q %v, "+
			"want two Puts, at epochs 2 and 3 in that order, and two PutIfMatch calls, the second's etag argument "+
			"the MD5 of the root pointer the first wrote: B seals after A has applied, at the next epoch, against "+
			"the ETag A's success recorded (ADR 0023 section 4; %s)", snapshotPrefix, snaps, rootKey, swaps, where)
	}
	if f := e.read(fs, "reading FS.dirty after both Syncs", where); f.dirty {
		t.Errorf("after both Syncs, FS.dirty is true, want false: B committed everything changed since A's seal "+
			"(ADR 0023 section 3; %s)", where)
	}
	e.checkRemount([]string{"c2", "c3"}, nil, where+", the remount after both Syncs")
}

// TestASecondSyncWithNothingNewMakesNoStoreCall is U6 of ADR 0023 section 11:
// a Sync queued behind a commit in flight that sealed everything finds the
// namespace clean and returns nil without a store call (sections 3 and 4;
// Assumption 7).
//
// U5 without c3: one commit behind it; the snapshot Put held; c2 created; Sync
// A started, and the hold entered; Sync B started; GetAttr of the root, Lookup
// of c2 and ReadDir of the root each return while held, after which B has not
// returned. Released: A returns nil, then B returns nil. Since A began, the
// harness recorded one Put under snapshotPrefix, one PutIfMatch of rootKey and
// no GetWithETag of rootKey in all.
//
// At d75d14a it hangs to the 10 s bound at GetAttr of the root while held,
// since FS.mu is held across the snapshot Put; its counts would hold there,
// since at d75d14a B waits for A behind the commit gate and FS.mu and then
// finds the namespace clean.
func TestASecondSyncWithNothingNewMakesNoStoreCall(t *testing.T) {
	const where = "ADR 0023 section 11, U6"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)

	hold := e.h.armHoldSnapshotPut()
	e.create(fs, root, "c2", where)
	mark := len(e.h.records())
	syncA := e.startSync(fs)
	e.waitEntered(hold, syncA, "Sync A", where)
	a := syncA.watch("Sync A")
	syncB := e.startSync(fs)
	e.readsWhileHeld(fs, root, "c2", hold, where, a)
	uc43NotReturned(t, syncB, "Sync B, started while A's snapshot Put was held",
		"a Sync queues behind a commit in flight and returns after it (ADR 0023 section 4; section 11, U6)")

	hold.Release()
	if err := syncA.wait(t, "Sync A", where); err != nil {
		t.Errorf("Sync A, once %s was released, = %v, want nil (ADR 0023 section 3; %s)", hold.what, err, where)
	}
	if err := syncB.wait(t, "Sync B", where); err != nil {
		t.Errorf("Sync B = %v, want nil: A sealed everything and nothing changed since, so the namespace is clean "+
			"(ADR 0023 section 3; %s)", err, where)
	}
	calls := e.h.records()[mark:]
	snaps := uc43Filter(calls, uc43IsSnapPut)
	swaps := uc43Filter(calls, uc43IsRootSwap)
	reads := uc43Filter(calls, uc43IsRootRead)
	if len(snaps) != 1 || len(swaps) != 1 || len(reads) != 0 {
		t.Errorf("since Sync A began the harness recorded the Puts under %q %v, the PutIfMatch calls of %q %v and "+
			"the GetWithETag calls of %q %v, want one Put, one PutIfMatch and no GetWithETag in all: a Sync on a "+
			"mount with no change since the last seal returns nil and makes no store call (ADR 0023 section 3; %s)",
			snapshotPrefix, snaps, rootKey, swaps, rootKey, reads, where)
	}
}

// uc43WriteR is what one Write returned.
type uc43WriteR struct {
	n   uint32
	how vfs.Stability
	err error
}

// TestStableWriteDuringACommitIsDurableWhenItReturns is U7 of ADR 0023 section
// 11: a FILE_SYNC Write that lands while a commit is in flight is not
// acknowledged until its own Sync has committed it (section 3, "What a change
// during the flight does"; section 4).
//
// a.bin created and committed; the snapshot Put held; c2 created; Sync A
// started, and the hold entered; W, a Write of 100 bytes at 0 of a.bin with
// vfs.FileSync, started; GetAttr of the root, Lookup of c2 and ReadDir of the
// root each return while held, after which W has not returned. Released: A
// returns nil; W returns (100, vfs.FileSync, nil). A remount of the inner
// store, with no further Sync, reads those 100 bytes from a.bin.
//
// At d75d14a it hangs to the 10 s bound at GetAttr of the root while held,
// since FS.mu is held across the snapshot Put. A build without the guard can
// answer W with FS.dirty cleared under it and no commit of its bytes, so the
// remount reads an empty file there (section 11).
func TestStableWriteDuringACommitIsDurableWhenItReturns(t *testing.T) {
	const where = "ADR 0023 section 11, U7"
	e := uc43Setup(t)
	ctx := e.ctx
	fs := e.newFS(where)
	root := e.root(fs, where)

	aH := e.create(fs, root, "a.bin", where)
	if err := e.sync(fs, `Sync of the commit of "a.bin"`, where); err != nil {
		t.Fatalf("Sync of the commit of %q = %v, want nil (%s)", "a.bin", err, where)
	}
	hold := e.h.armHoldSnapshotPut()
	e.create(fs, root, "c2", where)
	syncA := e.startSync(fs)
	e.waitEntered(hold, syncA, "Sync A", where)
	a := syncA.watch("Sync A")
	payload := bytes.Repeat([]byte("W43!"), 25) // 100 bytes
	w := uc43Go(e, func() uc43WriteR {
		n, how, err := fs.Write(ctx, uc43Caller, aH, 0, payload, vfs.FileSync)
		return uc43WriteR{n, how, err}
	})
	e.readsWhileHeld(fs, root, "c2", hold, where, a)
	const wWhy = "a stable Write's own Sync queues behind a commit in flight and returns after it (ADR 0023 " +
		"section 4; section 11, U7)"
	uc43NotReturned(t, w, "W, the vfs.FileSync Write started while A's snapshot Put was held", wWhy)

	hold.Release()
	if err := syncA.wait(t, "Sync A", where); err != nil {
		t.Errorf("Sync A, once %s was released, = %v, want nil (ADR 0023 section 3; %s)", hold.what, err, where)
	}
	wr := w.wait(t, "W, the vfs.FileSync Write", where)
	if wr.err != nil || wr.n != 100 || wr.how != vfs.FileSync {
		t.Errorf("W, a Write of 100 bytes at 0 of %q with vfs.FileSync, = (%d, %v, %v), want (100, %v, nil) (ADR "+
			"0023 section 3; %s)", "a.bin", wr.n, wr.how, wr.err, vfs.FileSync, where)
	}

	// No further Sync: what W acknowledged must already be in the bucket.
	rmWhere := where + ", the remount after W returned, with no further Sync"
	if rm := e.remount(rmWhere); rm != nil {
		l := e.lookup(rm, e.root(rm, rmWhere), "a.bin", rmWhere)
		if l.err != nil {
			t.Errorf("Lookup of %q in the remount = %v, want success (%s)", "a.bin", l.err, rmWhere)
		} else {
			data, err := e.readFile(rm, l.h, `reading "a.bin" in the remount`, rmWhere)
			if err != nil || !bytes.Equal(data, payload) {
				t.Errorf("in the remount %q reads (%d bytes %q, %v), want the 100 bytes W wrote: a stable Write is "+
					"durable when it returns, since its Sync commits after the commit in flight, which left the "+
					"namespace dirty (ADR 0023 section 3; %s)", "a.bin", len(data), data, err, rmWhere)
			}
		}
	}
}

// TestTheTopOfCommitProbeHoldsNoLock is U8 of ADR 0023 section 11: the probe
// a commit makes at its top, while the root is in doubt, is a store call made
// holding no lock, so namespace operations and a size change do not wait for
// it (sections 2 and 5).
//
// ADR 0022 section 8's K7 setup through the failed read: one commit behind
// it; the held-then-lost swap armed with the failed read after it; c2
// created; Sync started, the hold entered and released at once; Sync returns
// an error whose text contains "in doubt", and rootKnowledge is rootInDoubt.
// Then the held GetWithETag of rootKey armed; c3 created; Sync started, and
// the hold entered, before any Put under snapshotPrefix: the read is the
// top-of-commit probe. While held, GetAttr of the root, Lookup of c3, Create of
// c4 and SetAttr of c3's size each return. Released: Sync returns nil;
// rootKnowledge is rootKnown; after the held read the harness recorded one
// Put under snapshotPrefix at epoch 3 and one PutIfMatch of rootKey; a remount
// has c2, c3 and c4.
//
// At d75d14a it hangs to the 10 s bound at GetAttr of the root while held,
// since FS.mu is held across the probe's read (ADR 0022 section 3).
func TestTheTopOfCommitProbeHoldsNoLock(t *testing.T) {
	const where = "ADR 0023 section 11, U8"
	e := uc43Setup(t)
	ctx := e.ctx
	fs := e.newFS(where)
	root := e.root(fs, where)

	// K7's setup: a lost reply whose read fails leaves the root in doubt.
	lostHold := e.h.armHoldSwapThenLose()
	e.h.armFailReadAfterSwap()
	e.create(fs, root, "c2", where)
	first := e.startSync(fs)
	e.waitEntered(lostHold, first, "the Sync of the commit of c2", where)
	lostHold.Release()
	if err := first.wait(t, "the Sync of the commit of c2", where); err == nil || !strings.Contains(err.Error(), "in doubt") {
		t.Fatalf("the Sync of the commit of c2, whose swap lost its reply and whose read failed, = %v, want an error "+
			"whose text contains %q (ADR 0022 section 4; %s)", err, "in doubt", where)
	}
	if f := e.read(fs, "reading the FS's fields after the commit of c2", where); f.knowledge != rootInDoubt {
		t.Fatalf("after the commit of c2, f.rootKnowledge.State() = %v, want %v (ADR 0022 section 3, probeFailed; "+
			"%s)", f.knowledge, rootInDoubt, where)
	}

	// The top-of-commit probe, held.
	readHold := e.h.armHoldRead()
	c3H := e.create(fs, root, "c3", where)
	mark := len(e.h.records())
	syncB := e.startSync(fs)
	e.waitEntered(readHold, syncB, "the Sync after the Create of c3", where)
	if snaps := uc43Filter(e.h.records()[mark:], uc43IsSnapPut); len(snaps) != 0 {
		t.Errorf("when the read of %q was held, the Sync had already made the Puts under %q %v, want none: the "+
			"top-of-commit probe comes before the snapshot Put (ADR 0023 section 2, step 3; ADR 0022 section 4; %s)",
			rootKey, snapshotPrefix, snaps, where)
	}
	b := syncB.watch("the Sync after the Create of c3")
	heldOK := func(what string, fn func() error) {
		t.Helper()
		if err := uc43Held(e, readHold, what, where, fn, b); err != nil {
			t.Errorf("%s, while %s is held, = %v, want success (ADR 0023 section 11; %s)", what, readHold.what, err,
				where)
		}
	}
	heldOK("GetAttr of the root directory", func() error {
		_, err := fs.GetAttr(ctx, root)
		return err
	})
	heldOK(`Lookup of "c3"`, func() error {
		_, _, err := fs.Lookup(ctx, uc43Caller, root, "c3")
		return err
	})
	heldOK(`Create "c4" in the root directory`, func() error {
		_, _, err := fs.Create(ctx, uc43Caller, root, "c4", vfs.SetAttr{}, true)
		return err
	})
	size := uint64(10)
	heldOK(`SetAttr of "c3"'s size to 10`, func() error {
		_, err := fs.SetAttr(ctx, uc43Caller, c3H, vfs.SetAttr{Size: &size})
		return err
	})

	readHold.Release()
	if err := syncB.wait(t, "the Sync after the Create of c3", where); err != nil {
		t.Errorf("the Sync after the Create of c3, once %s was released, = %v, want nil: its read finds the root "+
			"pointer naming the attempt, adopts it, and the commit continues (ADR 0022 section 4; ADR 0023 section "+
			"2; %s)", readHold.what, err, where)
	}
	calls := e.h.records()[mark:]
	held := uc43First(calls, func(c uc43Call) bool { return uc43IsRootRead(c) && c.how == uc43HeldPassed })
	if held < 0 {
		t.Errorf("the Sync after the Create of c3 made no GetWithETag of %q that the harness held (%s). Every call "+
			"since it began: %v", rootKey, where, calls)
	} else {
		after := calls[held+1:]
		snaps, swaps := uc43Filter(after, uc43IsSnapPut), uc43Filter(after, uc43IsRootSwap)
		if len(snaps) != 1 || !uc43HasEpoch(snaps[0].key, 3) || len(swaps) != 1 {
			t.Errorf("after the held read the harness recorded the Puts under %q %v and the PutIfMatch calls of %q "+
				"%v, want one Put at epoch 3 and one PutIfMatch (ADR 0022 section 4; ADR 0023 section 2; %s)",
				snapshotPrefix, snaps, rootKey, swaps, where)
		}
	}
	if f := e.read(fs, "reading the FS's fields after the Sync of c3", where); f.knowledge != rootKnown {
		t.Errorf("after the Sync of c3, f.rootKnowledge.State() = %v, want %v (ADR 0022 section 4; %s)",
			f.knowledge, rootKnown, where)
	}
	e.checkRemount([]string{"c2", "c3", "c4"}, nil, where+", the remount after the Sync of c3")
}

// TestADivergedMountsSyncMakesNoStoreCall is U9 of ADR 0023 section 11: a
// diverged mount's Sync is refused at the top of its commit, before the probe,
// with an error whose text begins "filesystem diverged", and makes no store
// call at all, reads included (section 2, step 3; ADR 0014 section 11's M7).
//
// ADR 0022 section 8's K6: a foreign root pointer written on the inner store;
// a commit of c2 whose Sync returns an error whose text contains
// "commit conflict", after which f.mode.State() is mountDiverged. Then Sync
// again: it returns an error whose text begins "filesystem diverged", and the
// harness recorded no call of any kind since the first Sync returned, no
// GetWithETag of rootKey included.
//
// Section 11 gives no red behaviour for U9, and none is expected at d75d14a:
// by ADR 0023's Context, commitLocked there refuses a diverged mount at its
// top, before it would call resolveRoot, with the same error, so this test is
// expected to pass at d75d14a. It pins that the refusal stays ahead of every
// store call when it moves to Sync's step 3.
func TestADivergedMountsSyncMakesNoStoreCall(t *testing.T) {
	const where = "ADR 0023 section 11, U9"
	e := uc43Setup(t)
	fs := e.newFS(where)
	before := e.read(fs, "reading the FS's fields before the foreign root pointer is written", where)

	// The harness's records need not be read before this write: the test
	// counts only calls made after the first Sync returned.
	if _, err := uc43WriteForeignRoot(e.ctx, e.h.inner, before.epoch+1, uc43Writer); err != nil {
		t.Fatalf("setup: writing a foreign root pointer on the inner store: %v (%s)", err, where)
	}
	if err := e.commit(fs, "c2", where); err == nil || !strings.Contains(err.Error(), "commit conflict") {
		t.Fatalf("the Sync of the commit of c2, after another writer moved the root pointer, = %v, want an error "+
			"whose text contains %q (ADR 0022 section 4, the conflict error; %s)", err, "commit conflict", where)
	}
	if got := e.mode(fs, where); got != mountDiverged {
		t.Fatalf("after the commit of c2, f.mode.State() = %v, want %v (ADR 0022 section 3, probeTheirs; %s)", got,
			mountDiverged, where)
	}

	mark := len(e.h.records())
	err := e.sync(fs, "the second Sync, on the diverged mount", where)
	if err == nil || !strings.HasPrefix(err.Error(), "filesystem diverged") {
		t.Errorf("the second Sync, on the diverged mount, = %v, want an error whose text begins %q (ADR 0023 "+
			"section 2, step 3; %s)", err, "filesystem diverged", where)
	}
	if calls := e.h.records()[mark:]; len(calls) != 0 {
		t.Errorf("since the first Sync returned the harness recorded the calls %v, want none of any kind, no "+
			"GetWithETag of %q included: a diverged mount's Sync makes no store call, not even a read (ADR 0023 "+
			"section 2, step 3; ADR 0014 section 11, M7; %s)", calls, rootKey, where)
	}
}

// TestEveryChangeAdvancesTheChangeCount is U10 of ADR 0023 section 11: every
// change that dirties the namespace advances FS.changes, and a read advances
// nothing (section 3, the change count).
//
// On a plain *store.Local, FS.changes is read holding FS.mu for reading before
// and after each of: Create of a file; Mkdir; Symlink; SetAttr of a mode; a
// Write of one byte, vfs.Unstable; Commit of that file, whose flush stores the
// byte; SetAttr of a size; Rename; Remove; Rmdir. Each pair's second value is
// greater than its first. A GetAttr, a Lookup and a Read advance nothing.
//
// The mount is New on the harness's inner *store.Local, the plain store, over
// its empty bucket; the harness itself is not used. The reads are made after
// the Commit, of the file it flushed. Each step's value is read in its own
// hold of FS.mu for reading, and nothing else runs on the mount meanwhile.
//
// It cannot build at d75d14a, where FS.changes does not exist.
func TestEveryChangeAdvancesTheChangeCount(t *testing.T) {
	const where = "ADR 0023 section 11, U10"
	e := uc43Setup(t)
	ctx, local := e.ctx, e.h.inner
	nr := uc43Go(e, func() uc43R[*FS] {
		fs, err := New(ctx, uc43Config(local))
		return uc43R[*FS]{fs, err}
	}).wait(t, "New over an empty bucket on a plain *store.Local", where)
	if nr.err != nil || nr.v == nil {
		t.Fatalf("New over an empty bucket on a plain *store.Local = (%v, %v), want a *FS and nil (%s)", nr.v,
			nr.err, where)
	}
	fs := nr.v
	root := e.root(fs, where)

	// count reads FS.changes holding FS.mu for reading (ADR 0023 section 11,
	// the facts).
	count := func(when string) uint64 {
		t.Helper()
		return e.read(fs, "reading FS.changes "+when, where).changes
	}
	// advances runs a change, which must succeed, and requires it to advance
	// FS.changes.
	advances := func(what string, fn func() error) {
		t.Helper()
		before := count("before " + what)
		if err := uc43Go(e, fn).wait(t, what, where); err != nil {
			t.Fatalf("%s = %v, want success (%s)", what, err, where)
		}
		if after := count("after " + what); after <= before {
			t.Errorf("FS.changes after %s = %d, want greater than %d, its value before: every change that dirties "+
				"the namespace advances the count (ADR 0023 section 3, the change count; %s)", what, after, before,
				where)
		}
	}
	// still runs a read, which must succeed, and requires it to leave
	// FS.changes as it was.
	still := func(what string, fn func() error) {
		t.Helper()
		before := count("before " + what)
		if err := uc43Go(e, fn).wait(t, what, where); err != nil {
			t.Fatalf("%s = %v, want success (%s)", what, err, where)
		}
		if after := count("after " + what); after != before {
			t.Errorf("FS.changes after %s = %d, want %d, its value before: a read changes nothing, so it advances "+
				"nothing (ADR 0023 section 3; %s)", what, after, before, where)
		}
	}

	// fileH is written by the Create's goroutine and read only after wait
	// has returned, which the goroutine's close of its done channel orders.
	var fileH vfs.Handle
	advances(`Create of the file "f1"`, func() error {
		h, _, err := fs.Create(ctx, uc43Caller, root, "f1", vfs.SetAttr{}, true)
		fileH = h
		return err
	})
	advances(`Mkdir of "dir"`, func() error {
		_, _, err := fs.Mkdir(ctx, uc43Caller, root, "dir", vfs.SetAttr{})
		return err
	})
	advances(`Symlink of "lnk" to "f1"`, func() error {
		_, _, err := fs.Symlink(ctx, uc43Caller, root, "lnk", "f1", vfs.SetAttr{})
		return err
	})
	mode := uint32(0o640)
	advances(`SetAttr of "f1"'s mode`, func() error {
		_, err := fs.SetAttr(ctx, uc43Caller, fileH, vfs.SetAttr{Mode: &mode})
		return err
	})
	advances(`a Write of one byte at 0 of "f1", vfs.Unstable`, func() error {
		n, _, err := fs.Write(ctx, uc43Caller, fileH, 0, []byte{'x'}, vfs.Unstable)
		if err == nil && n != 1 {
			return fmt.Errorf("wrote %d bytes, want 1", n)
		}
		return err
	})
	advances(`Commit of "f1", whose flush stores the byte`, func() error {
		return fs.Commit(ctx, fileH, 0, 0)
	})

	still(`GetAttr of "f1"`, func() error {
		_, err := fs.GetAttr(ctx, fileH)
		return err
	})
	still(`Lookup of "f1"`, func() error {
		_, _, err := fs.Lookup(ctx, uc43Caller, root, "f1")
		return err
	})
	still(`Read of "f1"`, func() error {
		data, _, err := fs.Read(ctx, uc43Caller, fileH, 0, 1)
		if err == nil && !bytes.Equal(data, []byte{'x'}) {
			return fmt.Errorf("read %q, want %q", data, "x")
		}
		return err
	})

	size := uint64(0)
	advances(`SetAttr of "f1"'s size to 0`, func() error {
		_, err := fs.SetAttr(ctx, uc43Caller, fileH, vfs.SetAttr{Size: &size})
		return err
	})
	advances(`Rename of "f1" to "f2"`, func() error {
		return fs.Rename(ctx, uc43Caller, root, "f1", root, "f2")
	})
	advances(`Remove of "f2"`, func() error {
		return fs.Remove(ctx, uc43Caller, root, "f2")
	})
	advances(`Rmdir of "dir"`, func() error {
		return fs.Rmdir(ctx, uc43Caller, root, "dir")
	})
}

// twoHolds is U11's and U12's flight (ADR 0023 section 11): it starts fs.Sync,
// waits for first to be entered, and while first is held makes GetAttr of the
// root, Lookup of c2, Create of c3 and SetAttr of c2's size to 10; then it
// releases first, waits for second to be entered, and while second is held
// makes GetAttr of the root, Create of c4 and SetAttr of c2's size to 0; then
// it releases second. Each call must return while its hold is held, in the
// sense uc43Held checks, and succeed. It returns the Sync's call, for the
// caller to wait for.
func (e *uc43Env) twoHolds(fs *FS, root, c2H vfs.Handle, first, second *uc43Hold, where string) *uc43Done[error] {
	e.t.Helper()
	ctx := e.ctx
	syncA := e.startSync(fs)
	e.waitEntered(first, syncA, "the Sync of the commit of c2", where)
	a := syncA.watch("the Sync of the commit of c2")
	heldOK := func(hold *uc43Hold, what string, fn func() error) {
		e.t.Helper()
		if err := uc43Held(e, hold, what, where, fn, a); err != nil {
			e.t.Errorf("%s, while %s is held, = %v, want success (ADR 0023 sections 1 and 6; section 11; %s)", what,
				hold.what, err, where)
		}
	}
	getAttr := func() error {
		_, err := fs.GetAttr(ctx, root)
		return err
	}
	create := func(name string) func() error {
		return func() error {
			_, _, err := fs.Create(ctx, uc43Caller, root, name, vfs.SetAttr{}, true)
			return err
		}
	}
	setSize := func(n uint64) func() error {
		return func() error {
			_, err := fs.SetAttr(ctx, uc43Caller, c2H, vfs.SetAttr{Size: &n})
			return err
		}
	}

	heldOK(first, "GetAttr of the root directory", getAttr)
	heldOK(first, `Lookup of "c2"`, func() error {
		_, _, err := fs.Lookup(ctx, uc43Caller, root, "c2")
		return err
	})
	heldOK(first, `Create "c3" in the root directory`, create("c3"))
	heldOK(first, `SetAttr of "c2"'s size to 10`, setSize(10))
	first.Release()

	e.waitEntered(second, syncA, "the Sync of the commit of c2", where)
	heldOK(second, "GetAttr of the root directory", getAttr)
	heldOK(second, `Create "c4" in the root directory`, create("c4"))
	heldOK(second, `SetAttr of "c2"'s size to 0`, setSize(0))
	second.Release()
	return syncA
}

// innerGet reads key from the harness's inner *store.Local, under uc43Bound.
// The caller reads the harness's records first.
func (e *uc43Env) innerGet(key, where string) ([]byte, error) {
	e.t.Helper()
	ctx, local := e.ctx, e.h.inner
	r := uc43Go(e, func() uc43R[[]byte] {
		data, err := local.Get(ctx, key)
		return uc43R[[]byte]{data, err}
	}).wait(e.t, fmt.Sprintf("Get(%q) on the inner store", key), where)
	return r.v, r.err
}

// innerList lists prefix on the harness's inner *store.Local, under
// uc43Bound. The caller reads the harness's records first.
func (e *uc43Env) innerList(prefix, where string) ([]store.ObjectInfo, error) {
	e.t.Helper()
	ctx, local := e.ctx, e.h.inner
	r := uc43Go(e, func() uc43R[[]store.ObjectInfo] {
		out, err := local.List(ctx, prefix, "", 0)
		return uc43R[[]store.ObjectInfo]{out, err}
	}).wait(e.t, fmt.Sprintf("List(%q) on the inner store", prefix), where)
	return r.v, r.err
}

// checkSize requires name, in fs's root directory, to have the size want.
func (e *uc43Env) checkSize(fs *FS, name string, want uint64, where string) {
	e.t.Helper()
	l := e.lookup(fs, e.root(fs, where), name, where)
	if l.err != nil {
		e.t.Errorf("Lookup of %q in the remount's root directory = %v, want success (%s)", name, l.err, where)
		return
	}
	if l.attr.Size != want {
		e.t.Errorf("in the remount %q has size %d, want %d: the size change made while held is in the next commit "+
			"(ADR 0023 section 3; %s)", name, l.attr.Size, want, where)
	}
}

// TestTheReclaimOfARefusedCommitHoldsNoLock is U11 of ADR 0023 section 11: the
// reclaim of a commit whose swap the store answered store.ErrUnsupported, on a
// mount without the opt-in, makes its read of the root pointer and its Delete
// of the attempt's snapshot holding neither FS.mu nor the commit gate, so a
// namespace read, a namespace change and a size change made while either is
// held return (sections 1 and 6; ADR 0021 section 2; Assumption 37).
//
// One commit behind it, New's, with AllowUnconditionalRoot false. The 501 on
// the next PutIfMatch of rootKey, the held Get of rootKey and the held Delete
// under snapshotPrefix armed; c2 created; Sync started, and the Get's hold
// entered. While it is held, GetAttr of the root, Lookup of c2, Create of c3
// and SetAttr of c2's size to 10 each return; the Get released, and the
// Delete's hold entered; while it is held, GetAttr of the root, Create of c4
// and SetAttr of c2's size to 0 each return; released. Sync returns an error
// for which errors.Is(err, store.ErrUnsupported) holds and whose text contains
// "unconditional". The harness recorded, in order, one Put under
// snapshotPrefix at epoch 2, one PutIfMatch of rootKey answered by the
// harness, one Get of rootKey and one Delete of that Put's key; and after the
// PutIfMatch no GetWithETag, Put or Head of rootKey. FS.dirty is true, FS.epoch
// 1, FS.rootSnapshot as before the Sync, f.rootKnowledge.State() rootKnown and
// f.mode.State() mountWritable. The inner store holds no snapshot at epoch 2,
// and a remount has an empty root directory. Sync again, with no arm: nil,
// with one more Put under snapshotPrefix at epoch 2 whose key is not the
// reclaimed one, and one PutIfMatch of rootKey passed through; a remount has
// c2, c3 and c4, with c2 at size 0.
//
// No red run is asked for (section 11, Red runs; Assumption 37): it was
// written after d0fdcdf. At d75d14a it would hang to the 10 s bound at GetAttr
// of the root while the reclaim's Get is held, as U1 hangs, since the gate and
// FS.mu were held across the reclaim; so would a build that moved the reclaim
// back under either.
func TestTheReclaimOfARefusedCommitHoldsNoLock(t *testing.T) {
	const where = "ADR 0023 section 11, U11"
	e := uc43Setup(t)
	fs := e.newFS(where)
	root := e.root(fs, where)
	before := e.read(fs, "reading the FS's fields before the Sync", where)

	e.h.armAnswer501()
	getHold := e.h.armHoldRootGet()
	delHold := e.h.armHoldSnapshotDelete()
	c2H := e.create(fs, root, "c2", where)
	mark := len(e.h.records())
	syncA := e.twoHolds(fs, root, c2H, getHold, delHold, where)
	err := syncA.wait(t, "the Sync of the commit of c2", where)
	if !errors.Is(err, store.ErrUnsupported) || !strings.Contains(err.Error(), "unconditional") {
		t.Errorf("the Sync of the commit of c2, whose swap was answered store.ErrUnsupported on a mount without the "+
			"opt-in, = %v, want an error for which errors.Is(err, store.ErrUnsupported) holds and whose text "+
			"contains %q (ADR 0021 section 2; ADR 0023 section 11; %s)", err, "unconditional", where)
	}

	first := e.h.records()
	calls := first[mark:]
	seq := uc43Filter(calls, func(c uc43Call) bool {
		return uc43IsSnapPut(c) || uc43IsRootSwap(c) || uc43IsRootGet(c) || uc43IsDelete(c)
	})
	reclaimed := ""
	if len(seq) > 0 && uc43IsSnapPut(seq[0]) {
		reclaimed = seq[0].key
	}
	ok := len(seq) == 4 && uc43IsSnapPut(seq[0]) && uc43HasEpoch(seq[0].key, 2) && uc43IsRootSwap(seq[1]) &&
		seq[1].how == uc43Answered501 && uc43IsRootGet(seq[2]) && uc43IsDelete(seq[3]) && seq[3].key == reclaimed
	if !ok {
		t.Errorf("in the Sync of the commit of c2 the harness recorded the Puts under %q, the PutIfMatch calls and "+
			"Gets of %q and the Deletes %v, want exactly, in order: one Put under %q at epoch 2, one PutIfMatch of "+
			"%q answered by the harness, one Get of %q and one Delete of that Put's key (ADR 0021 section 2, the "+
			"reclaim; ADR 0023 section 11; %s)", snapshotPrefix, rootKey, seq, snapshotPrefix, rootKey, rootKey,
			where)
	}
	if swapAt := uc43First(calls, uc43IsRootSwap); swapAt >= 0 {
		after := calls[swapAt+1:]
		if bad := uc43Filter(after, func(c uc43Call) bool {
			return uc43IsRootRead(c) || uc43IsRootPut(c) || uc43IsRootHead(c)
		}); len(bad) != 0 {
			t.Errorf("after the PutIfMatch answered store.ErrUnsupported the harness recorded %v, want no "+
				"GetWithETag, Put or Head of %q: without the opt-in the mount writes the root pointer no further, "+
				"and a reclaim that deletes raises no doubt (ADR 0021 section 2; ADR 0023 section 11; %s)", bad,
				rootKey, where)
		}
	}

	f := e.read(fs, "reading the FS's fields after the refused commit", where)
	if !f.dirty {
		t.Errorf("after the refused commit, FS.dirty is false, want true: a commit that fails leaves the flag as "+
			"it was (ADR 0023 section 3; %s)", where)
	}
	if f.epoch != 1 {
		t.Errorf("after the refused commit, FS.epoch = %d, want 1 (ADR 0021 section 2; %s)", f.epoch, where)
	}
	if f.rootSnapshot != before.rootSnapshot {
		t.Errorf("after the refused commit, FS.rootSnapshot = %q, want %q, as before the Sync (ADR 0021 section 2; "+
			"%s)", f.rootSnapshot, before.rootSnapshot, where)
	}
	if f.knowledge != rootKnown {
		t.Errorf("after the refused commit, f.rootKnowledge.State() = %v, want %v (ADR 0022 section 4; ADR 0023 "+
			"section 11; %s)", f.knowledge, rootKnown, where)
	}
	if got := e.mode(fs, where); got != mountWritable {
		t.Errorf("after the refused commit, f.mode.State() = %v, want %v: a refused commit fires nothing (ADR 0021 "+
			"section 2; %s)", got, mountWritable, where)
	}

	objs, lerr := e.innerList(snapshotPrefix, where)
	if lerr != nil {
		t.Errorf("List(%q) on the inner store = %v, want success (%s)", snapshotPrefix, lerr, where)
	}
	for _, o := range objs {
		if uc43HasEpoch(o.Key, 2) {
			t.Errorf("the inner store holds %q, a snapshot at epoch 2, want none: the reclaim deleted the refused "+
				"commit's snapshot (ADR 0021 section 2; ADR 0023 section 11; %s)", o.Key, where)
		}
	}
	firstWhere := where + ", the remount after the refused commit"
	if rm := e.remount(firstWhere); rm != nil {
		names, err := e.dirNames(rm, e.root(rm, firstWhere), "ReadDir of the remount's root directory", firstWhere)
		if err != nil || len(names) != 0 {
			t.Errorf("the remount's root directory lists %q (err %v), want no entry: the refused commit reached no "+
				"root pointer (ADR 0021 section 2; %s)", names, err, firstWhere)
		}
	}

	if err := e.sync(fs, "the second Sync, with no arm", where); err != nil {
		t.Errorf("the second Sync, with no arm, = %v, want nil: a refused commit leaves the mount writable, and its "+
			"next commit tries the swap again (ADR 0021 section 2; %s)", err, where)
	}
	calls = e.h.records()[len(first):]
	snaps, swaps := uc43Filter(calls, uc43IsSnapPut), uc43Filter(calls, uc43IsRootSwap)
	if len(snaps) != 1 || !uc43HasEpoch(snaps[0].key, 2) || snaps[0].key == reclaimed || len(swaps) != 1 ||
		swaps[0].how != uc43Passed || swaps[0].err != nil {
		t.Errorf("in the second Sync the harness recorded the Puts under %q %v and the PutIfMatch calls of %q %v, "+
			"want one Put at epoch 2 whose key is not the reclaimed %q, and one PutIfMatch passed through and "+
			"accepted (ADR 0021 section 2; ADR 0023 section 11; %s)", snapshotPrefix, snaps, rootKey, swaps,
			reclaimed, where)
	}
	secondWhere := where + ", the remount after the second Sync"
	if rm := e.checkRemount([]string{"c2", "c3", "c4"}, nil, secondWhere); rm != nil {
		e.checkSize(rm, "c2", 0, secondWhere)
	}
}

// TestTheUnconditionalFallbackHoldsNoLock is U12 of ADR 0023 section 11: on a
// mount with the opt-in, the fallback that follows a swap answered
// store.ErrUnsupported makes its Put of rootKey and its Head of rootKey holding
// neither FS.mu nor the commit gate, so a namespace read, a namespace change
// and a size change made while either is held return (sections 1 and 6; ADR
// 0021 section 2; Assumption 37).
//
// One commit behind it, New's, with AllowUnconditionalRoot true. The 501 on
// the next PutIfMatch of rootKey, the held Put of rootKey and the held Head of
// rootKey armed; c2 created; Sync started, and the Put's hold entered. While
// it is held, GetAttr of the root, Lookup of c2, Create of c3 and SetAttr of
// c2's size to 10 each return; the Put released, and the Head's hold entered;
// while it is held, GetAttr of the root, Create of c4 and SetAttr of c2's size
// to 0 each return; released. Sync returns nil. The harness recorded, in
// order, one Put under snapshotPrefix at epoch 2, one PutIfMatch of rootKey
// answered by the harness, one Put of rootKey and one Head of rootKey; and no
// GetWithETag or Get of rootKey and no Delete. FS.dirty is true, FS.epoch 2,
// FS.rootSnapshot the key that Put under snapshotPrefix wrote, FS.rootETag the
// MD5 of the root pointer's bytes on the inner store, and
// f.rootKnowledge.State() rootKnown. A remount has c2 and neither c3 nor c4.
// Sync again, with no arm: nil, its PutIfMatch passed through with that MD5 as
// its etag argument and its snapshot Put at epoch 3; a remount has c2, c3 and
// c4, with c2 at size 0.
//
// No red run is asked for (section 11, Red runs; Assumption 37): it was
// written after d0fdcdf. At d75d14a it would hang to the 10 s bound at GetAttr
// of the root while the fallback's Put is held, as U1 hangs, since the gate
// and FS.mu were held across the fallback; so would a build that moved the
// fallback back under either.
func TestTheUnconditionalFallbackHoldsNoLock(t *testing.T) {
	const where = "ADR 0023 section 11, U12"
	e := uc43Setup(t)
	cfg := uc43Config(e.h)
	cfg.AllowUnconditionalRoot = true
	fs := e.newFSWith(cfg, where)
	root := e.root(fs, where)

	e.h.armAnswer501()
	putHold := e.h.armHoldRootPut()
	headHold := e.h.armHoldRootHead()
	c2H := e.create(fs, root, "c2", where)
	mark := len(e.h.records())
	syncA := e.twoHolds(fs, root, c2H, putHold, headHold, where)
	if err := syncA.wait(t, "the Sync of the commit of c2", where); err != nil {
		t.Errorf("the Sync of the commit of c2, whose swap was answered store.ErrUnsupported on a mount with the "+
			"opt-in, = %v, want nil: the fallback's Put and Head succeed (ADR 0021 section 2; ADR 0023 section 11; "+
			"%s)", err, where)
	}

	first := e.h.records()
	calls := first[mark:]
	seq := uc43Filter(calls, func(c uc43Call) bool {
		return uc43IsSnapPut(c) || uc43IsRootSwap(c) || uc43IsRootPut(c) || uc43IsRootHead(c)
	})
	snapKey := ""
	if len(seq) > 0 && uc43IsSnapPut(seq[0]) {
		snapKey = seq[0].key
	}
	ok := len(seq) == 4 && uc43IsSnapPut(seq[0]) && uc43HasEpoch(seq[0].key, 2) && uc43IsRootSwap(seq[1]) &&
		seq[1].how == uc43Answered501 && uc43IsRootPut(seq[2]) && uc43IsRootHead(seq[3])
	if !ok {
		t.Errorf("in the Sync of the commit of c2 the harness recorded the Puts under %q and the PutIfMatch, Put and "+
			"Head calls of %q %v, want exactly, in order: one Put under %q at epoch 2, one PutIfMatch of %q "+
			"answered by the harness, one Put of %q and one Head of %q (ADR 0021 section 2, the fallback; ADR 0023 "+
			"section 11; %s)", snapshotPrefix, rootKey, seq, snapshotPrefix, rootKey, rootKey, rootKey, where)
	}
	if bad := uc43Filter(calls, func(c uc43Call) bool {
		return uc43IsRootRead(c) || uc43IsRootGet(c) || uc43IsDelete(c)
	}); len(bad) != 0 {
		t.Errorf("in the Sync of the commit of c2 the harness recorded %v, want no GetWithETag or Get of %q and no "+
			"Delete: a fallback whose Head gives the ETag raises no doubt and reclaims nothing (ADR 0021 section 2; "+
			"ADR 0022 section 4; ADR 0023 section 11; %s)", bad, rootKey, where)
	}

	f := e.read(fs, "reading the FS's fields after the fallback commit", where)
	raw, gerr := e.innerGet(rootKey, where)
	if gerr != nil {
		t.Errorf("Get(%q) on the inner store = %v, want the root pointer (%s)", rootKey, gerr, where)
	}
	if !f.dirty {
		t.Errorf("after the fallback commit, FS.dirty is false, want true: c3, c4 and the size changes were made "+
			"after its seal (ADR 0023 section 3; %s)", where)
	}
	if f.epoch != 2 {
		t.Errorf("after the fallback commit, FS.epoch = %d, want 2 (ADR 0021 section 2; %s)", f.epoch, where)
	}
	if snapKey != "" && f.rootSnapshot != snapKey {
		t.Errorf("after the fallback commit, FS.rootSnapshot = %q, want %q, the key its Put under %q wrote (ADR 0022 "+
			"section 4; ADR 0023 section 11; %s)", f.rootSnapshot, snapKey, snapshotPrefix, where)
	}
	if gerr == nil && f.rootETag != uc43ETag(raw) {
		t.Errorf("after the fallback commit, FS.rootETag = %q, want %q, the MD5 of the root pointer's bytes on the "+
			"inner store, which the Head gave (ADR 0021 section 2; ADR 0022 section 8; %s)", f.rootETag,
			uc43ETag(raw), where)
	}
	if f.knowledge != rootKnown {
		t.Errorf("after the fallback commit, f.rootKnowledge.State() = %v, want %v (ADR 0022 section 4; ADR 0023 "+
			"section 11; %s)", f.knowledge, rootKnown, where)
	}
	e.checkRemount([]string{"c2"}, []string{"c3", "c4"}, where+", the remount after the fallback commit")

	if err := e.sync(fs, "the second Sync, with no arm", where); err != nil {
		t.Errorf("the second Sync, with no arm, = %v, want nil (ADR 0021 section 2; ADR 0023 section 3; %s)", err,
			where)
	}
	calls = e.h.records()[len(first):]
	snaps, swaps := uc43Filter(calls, uc43IsSnapPut), uc43Filter(calls, uc43IsRootSwap)
	ok = len(snaps) == 1 && uc43HasEpoch(snaps[0].key, 3) && len(swaps) == 1 && swaps[0].how == uc43Passed &&
		swaps[0].err == nil
	if ok && gerr == nil && swaps[0].etag != uc43ETag(raw) {
		ok = false
	}
	if !ok {
		t.Errorf("in the second Sync the harness recorded the Puts under %q %v and the PutIfMatch calls of %q %v, "+
			"want one Put at epoch 3 and one PutIfMatch passed through and accepted with the etag argument %q, the "+
			"MD5 of the root pointer the fallback wrote (ADR 0021 section 2; ADR 0023 section 11; %s)",
			snapshotPrefix, snaps, rootKey, swaps, uc43ETag(raw), where)
	}
	secondWhere := where + ", the remount after the second Sync"
	if rm := e.checkRemount([]string{"c2", "c3", "c4"}, nil, secondWhere); rm != nil {
		e.checkSize(rm, "c2", 0, secondWhere)
	}
}
