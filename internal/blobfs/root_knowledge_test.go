package blobfs

// Clean-room regression tests for #5, "A transient commit failure is treated
// as a second writer", written without reading the implementation, from
// ADR 0022
// (docs/adr/0022-a-failed-root-swap-is-settled-by-one-read-of-the-root-pointer.md)
// sections 1 to 5 and 8 and Assumptions 2 to 6, 9 to 12, 15 and 23,
// docs/vocabulary.md V2, ADR 0014 section 11, ADR 0021 section 6, ADR 0019
// section 6, ADR 0011 section 7, ADR 0012 section 9, the safety rules and
// facts of ADR 0020 section 6, ADR 0003 section 4 and internal/vfs/vfs.go.
//
// ADR 0022 section 1's rule: a mount decides that another writer owns the
// bucket only on a positive showing from a read of the root pointer, never
// from a swap's error alone. After any write of the root pointer whose reply
// did not give the mount the root pointer's current ETag, its knowledge of the
// root pointer is in doubt, and it writes the root pointer no further until
// one read, GetWithETag of rootKey, has shown that the root pointer names the
// attempt (ours: adopt it), names the snapshot the namespace descends from
// (unchanged: take the read's ETag), or names neither or is gone (theirs:
// diverge, and log the writer). A read that decides nothing leaves the root in
// doubt, fails the commit, and is made again at the top of the next commit,
// before anything is written (section 3). At edcfc15 every such failure either
// diverges the mount at once or leaves it holding a stale ETag, so that its
// next swap diverges it with no other writer.
//
// Covered: ADR 0022 section 8's tests of the machine and the verdict,
//   - K1, TestRootStatesTableAndStrings;
//   - K2, TestClassifyRootDecidesWhatOneReadShows;
//   - K3, TestNewStartsKnownWithTheRootPointersKey;
//
// and its route tests, each written in two parts (Assumption 23): its calls,
// its records and its assertions on Sync's result, f.mode.State(), FS.dirty,
// FS.epoch, FS.rootETag, the inner store, the log and a remount, which use
// only names that exist at edcfc15 and fail there as each test's doc comment
// says; and its assertions on f.rootKnowledge.State(), f.doubtKey and
// f.rootSnapshot, added once the code existed.
//   - K4, TestLostSwapReplyIsAdoptedNotTakenForASecondWriter
//     (CommitProtocol_SelfDiverge; route 1).
//   - K5, TestSwapRefusedWhileTheRootIsUnchangedRetriesAtTheNextCommit
//     (CommitProtocol_SelfDiverge409; route 2).
//   - K6, TestASwapLostToAnotherWriterDivergesAndNamesTheWriter (ProbeTheirs;
//     the issue's second done-when).
//   - K7, TestInDoubtMountSettlesTheRootBeforeItsNextCommit
//     (CommitProtocol_ProbeFailed).
//   - K8, TestInDoubtMountDivergesWhenTheNextReadShowsAnotherWriter.
//   - K9, TestSwapReplyWithoutETagIsSettledByOneRead (CommitProtocol_SwapNoETag;
//     #103; route 5).
//   - K10, TestSwapReplyWithoutETagAndAFailedReadRetriesTheCommit.
//   - K11, TestLostFallbackReplyIsAdopted (CommitProtocol_SelfDivergeFallback;
//     route 3).
//   - K12, TestA501ThatTookEffectIsAdopted (ADR 0021 section 2 step 3; route
//     4). G3 in internal/blobfs/swap_fallback_test.go asserts the same Sync
//     result.
//   - K13, TestLostReplyOnTheFirstCommitIsAdoptedByNew.
//
// Elsewhere, changed by ADR 0022 section 8: M3 and M7 in
// internal/blobfs/mount_mode_test.go, M4 in
// internal/blobfs/diverged_race_test.go, R7 and R8 in
// internal/blobfs/prune_root_test.go, and G3 in
// internal/blobfs/swap_fallback_test.go.
//
// Not covered, on purpose: what ADR 0022 section 8 leaves to review (that no
// swap is made while the root is in doubt on every path, the Fire sites, that
// classifyRoot is pure, the records' attributes other than the conflict
// record's writer, their_epoch and their_snapshot, a failed Fire, the TLA+
// lines and the trace rows); every other log record; and the error texts
// beyond the words section 4 pins.
//
// The harness, rk5Store, is ADR 0022 section 8's: a store.Store over a
// *store.Local under t.TempDir(), which does not embed store.Store and passes
// every call through to it, and in addition, when armed, one-shot:
//   - loses the next swap's reply: applies the next PutIfMatch of rootKey on
//     the inner store and returns "" and rk5ErrInjected (SwapReplyLost);
//   - fails the next swap without calling the inner store, with an error the
//     test chooses (SwapConflict409, SwapFailed, or a 501);
//   - drops the next swap reply's ETag: passes the next PutIfMatch of rootKey
//     through and returns "" and nil (SwapNoETag);
//   - applies then answers 501: passes the next PutIfMatch of rootKey through
//     and, if it succeeded, returns "" and store.ErrUnsupported;
//   - loses the next root Put's reply: applies the next Put of rootKey and
//     returns rk5ErrInjected (FallbackReplyLost);
//   - fails the next read of the root pointer: answers the next GetWithETag of
//     rootKey with nil, "" and rk5ErrInjected without calling the inner store
//     (ProbeFailed); or arms that failure only at the next PutIfMatch of
//     rootKey, so that the read it fails is the first one after the swap,
//     whatever reads come before it (New's own load reads the root pointer,
//     and ADR 0011 section 7 lets no test rely on which reads New makes); or
//     answers the next GetWithETag of rootKey with bytes and an ETag the test
//     gives;
//   - in its unconditional mode, set before the first call and never changed,
//     answers every PutIfMatch of any key with store.ErrUnsupported without
//     calling the inner store;
//   - records, in call order, every GetWithETag, Get and Head of rootKey,
//     every Put, PutIfMatch and Delete of any key (so every PutIfMatch of
//     rootKey with its etag argument and how it was answered, every Put of
//     rootKey and every Put under snapshotPrefix with its key), and the bytes
//     of every Put and PutIfMatch of rootKey.
//
// It takes no hold: every route here is sequential.
//
// The safety rules, as ADR 0011 section 7, ADR 0012 section 9, ADR 0020
// section 6, ADR 0021 section 6 and ADR 0022 section 8 give them; every test
// here follows them:
//   - every New and every FS call, f.mode.State(), every read of an FS field
//     and every call of classifyRoot included, runs in rk5Go, which recovers
//     a panic, and is waited for under rk5Bound, 10 s; goroutines never touch
//     t, and nothing here is parallel;
//   - every mount has SnapshotRetention -1 and MaxDirtyBytes -1, and no Write
//     is made, so no prune pass and no drain runs;
//   - there is no time.Sleep and no polling, and no test calls Run;
//   - every bucket is a local directory under the test's t.TempDir();
//   - where a test has a harness, the bucket is inspected through the inner
//     *store.Local only after the harness's records have been read, never
//     through the harness; the inner store is written to only to seed a
//     filesystem or to stand for another writer (the foreign root pointer of
//     section 8);
//   - an FS field is read only holding FS.mu for reading, and only the fields
//     section 8 names: FS.dirty, FS.epoch, FS.rootETag, FS.rootSnapshot,
//     FS.doubtKey and f.rootKnowledge.State(); f.mode.State() is read without
//     a lock (ADR 0014 section 11); no field is written;
//   - an *FS that New returned where it should have refused is never used.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"strata/internal/fsm"
	"strata/internal/store"
	"strata/internal/vfs"
)

// rk5Bound is how long a test waits for any one step before it reports the
// step as hung (ADR 0022 section 8, the safety rules).
const rk5Bound = 10 * time.Second

// rk5Caller is the root directory's owner, as every mount here is configured
// (ADR 0022 section 8: a commit's Create is by vfs.Caller{UID: 501, GID: 20}).
var rk5Caller = vfs.Caller{UID: 501, GID: 20}

// rk5ErrInjected is the error of the harness's lost replies and failed reads,
// and the untyped error K5 fails a swap with. It is none of store.ErrNotFound,
// store.ErrPrecondition and store.ErrUnsupported.
var rk5ErrInjected = errors.New("rk5: injected store fault")

// rk5Writer is the Writer of the foreign root pointer that stands for another
// writer (ADR 0022 section 8).
const rk5Writer = "other-host/4242"

// How the harness answered a call, as rk5Call.how records it.
const (
	rk5Passed         = "passed through"
	rk5ReplyLost      = "applied by the inner store, its reply lost"
	rk5Failed         = "failed by the harness without calling the inner store"
	rk5ETagDropped    = "passed through, the ETag of its reply dropped"
	rk5AppliedThen501 = "applied by the inner store, then answered store.ErrUnsupported"
	rk5Unconditional  = "answered store.ErrUnsupported by the harness in its unconditional mode"
	rk5Answered       = "answered with the test's bytes and ETag without calling the inner store"
)

// rk5Call is the harness's record of one call: its method, its key, the etag
// argument of a PutIfMatch, the bytes of a Put or PutIfMatch of rootKey, how
// the harness answered it, and the error it returned.
type rk5Call struct {
	op   string
	key  string
	etag string
	data []byte
	how  string
	err  error
}

// String describes the call for failure messages.
func (c rk5Call) String() string {
	if c.op == "PutIfMatch" {
		return fmt.Sprintf("PutIfMatch(%q, etag %q): %s, returned %v", c.key, c.etag, c.how, c.err)
	}
	return fmt.Sprintf("%s(%q): %s, returned %v", c.op, c.key, c.how, c.err)
}

// rk5Store is ADR 0022 section 8's harness. It does not embed store.Store, so
// that every method it has is one written here.
type rk5Store struct {
	inner *store.Local

	// unconditional answers every PutIfMatch of any key with
	// store.ErrUnsupported without calling the inner store. It is set before
	// the first call and never changed.
	unconditional bool

	// mu guards every field below. It is never held across an inner call.
	mu sync.Mutex

	// The one-shot arms of the next PutIfMatch of rootKey: at most one of
	// them is taken by a call, in the order failSwap, loseSwap, dropSwapETag,
	// applyThen501.
	failSwap     error
	loseSwap     bool
	dropSwapETag bool
	applyThen501 bool

	// loseRootPut applies the next Put of rootKey and returns rk5ErrInjected.
	loseRootPut bool

	// failRead answers the next GetWithETag of rootKey with rk5ErrInjected;
	// failReadAfterSwap sets failRead at the next PutIfMatch of rootKey.
	failRead          bool
	failReadAfterSwap bool

	// answerRead answers the next GetWithETag of rootKey with answerBody and
	// answerETag and a nil error.
	answerRead bool
	answerBody []byte
	answerETag string

	calls []rk5Call
}

var _ store.Store = (*rk5Store)(nil)

// armLoseSwap makes the next PutIfMatch of rootKey one the inner store applies
// and whose reply the harness loses (SwapReplyLost).
func (s *rk5Store) armLoseSwap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseSwap = true
}

// armFailSwap makes the next PutIfMatch of rootKey return "" and err without
// calling the inner store.
func (s *rk5Store) armFailSwap(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failSwap = err
}

// armDropSwapETag makes the next PutIfMatch of rootKey pass through and return
// "" and nil (SwapNoETag).
func (s *rk5Store) armDropSwapETag() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropSwapETag = true
}

// armApplyThen501 makes the next PutIfMatch of rootKey pass through and, if
// the inner store applied it, return "" and store.ErrUnsupported.
func (s *rk5Store) armApplyThen501() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyThen501 = true
}

// armLoseRootPut makes the next Put of rootKey one the inner store applies and
// whose reply the harness loses (FallbackReplyLost).
func (s *rk5Store) armLoseRootPut() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loseRootPut = true
}

// armFailRead makes the next GetWithETag of rootKey return nil, "" and
// rk5ErrInjected without calling the inner store (ProbeFailed).
func (s *rk5Store) armFailRead() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failRead = true
}

// armFailReadAfterSwap arms armFailRead's failure at the next PutIfMatch of
// rootKey, so that a test can arm it before New, whose load reads the root
// pointer with GetWithETag before its first swap.
func (s *rk5Store) armFailReadAfterSwap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failReadAfterSwap = true
}

// armAnswerRead makes the next GetWithETag of rootKey return body, etag and a
// nil error without calling the inner store.
func (s *rk5Store) armAnswerRead(body []byte, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answerRead = true
	s.answerBody = append([]byte(nil), body...)
	s.answerETag = etag
}

// records returns a copy of every call the harness has recorded, in call
// order.
func (s *rk5Store) records() []rk5Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]rk5Call, len(s.calls))
	for i, c := range s.calls {
		c.data = append([]byte(nil), c.data...)
		out[i] = c
	}
	return out
}

func (s *rk5Store) record(c rk5Call) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
}

// Get passes through, recording every Get of rootKey.
func (s *rk5Store) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.inner.Get(ctx, key)
	if key == rootKey {
		s.record(rk5Call{op: "Get", key: key, how: rk5Passed, err: err})
	}
	return data, err
}

// GetWithETag passes through, recording every GetWithETag of rootKey, except
// that an armed failure or answer is given for the next one of rootKey
// instead, without calling the inner store.
func (s *rk5Store) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	if key != rootKey {
		return s.inner.GetWithETag(ctx, key)
	}
	s.mu.Lock()
	fail := s.failRead
	s.failRead = false
	answer := !fail && s.answerRead
	body, etag := s.answerBody, s.answerETag
	if answer {
		s.answerRead, s.answerBody, s.answerETag = false, nil, ""
	}
	s.mu.Unlock()
	switch {
	case fail:
		s.record(rk5Call{op: "GetWithETag", key: key, how: rk5Failed, err: rk5ErrInjected})
		return nil, "", rk5ErrInjected
	case answer:
		s.record(rk5Call{op: "GetWithETag", key: key, how: rk5Answered})
		return append([]byte(nil), body...), etag, nil
	}
	data, gotETag, err := s.inner.GetWithETag(ctx, key)
	s.record(rk5Call{op: "GetWithETag", key: key, how: rk5Passed, err: err})
	return data, gotETag, err
}

func (s *rk5Store) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	return s.inner.GetRange(ctx, key, off, n)
}

// Head passes through, recording every Head of rootKey.
func (s *rk5Store) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	info, err := s.inner.Head(ctx, key)
	if key == rootKey {
		s.record(rk5Call{op: "Head", key: key, how: rk5Passed, err: err})
	}
	return info, err
}

// Put passes through and records every Put, keeping the bytes of a Put of
// rootKey. When armed, it returns rk5ErrInjected for the next Put of rootKey
// that the inner store applied.
func (s *rk5Store) Put(ctx context.Context, key string, data []byte) error {
	lost := false
	if key == rootKey {
		s.mu.Lock()
		lost = s.loseRootPut
		s.loseRootPut = false
		s.mu.Unlock()
	}
	err := s.inner.Put(ctx, key, data)
	c := rk5Call{op: "Put", key: key, how: rk5Passed, err: err}
	if key == rootKey {
		c.data = append([]byte(nil), data...)
	}
	if lost && err == nil {
		c.how, c.err, err = rk5ReplyLost, rk5ErrInjected, rk5ErrInjected
	}
	s.record(c)
	return err
}

// Delete passes through, recording every Delete.
func (s *rk5Store) Delete(ctx context.Context, key string) error {
	err := s.inner.Delete(ctx, key)
	s.record(rk5Call{op: "Delete", key: key, how: rk5Passed, err: err})
	return err
}

func (s *rk5Store) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	return s.inner.List(ctx, prefix, after, max)
}

// PutIfMatch records every call, with its etag argument and how it was
// answered, keeping the bytes of a call for rootKey. In the unconditional mode
// it answers every call store.ErrUnsupported without calling the inner store;
// otherwise the next call for rootKey takes at most one of the swap arms, and
// every other call passes through.
func (s *rk5Store) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (string, error) {
	c := rk5Call{op: "PutIfMatch", key: key, etag: etag}
	if key == rootKey {
		c.data = append([]byte(nil), data...)
	}
	if s.unconditional {
		c.how, c.err = rk5Unconditional, store.ErrUnsupported
		s.record(c)
		return "", store.ErrUnsupported
	}
	var failErr error
	lose, drop, apply501 := false, false, false
	if key == rootKey {
		s.mu.Lock()
		switch {
		case s.failSwap != nil:
			failErr, s.failSwap = s.failSwap, nil
		case s.loseSwap:
			lose, s.loseSwap = true, false
		case s.dropSwapETag:
			drop, s.dropSwapETag = true, false
		case s.applyThen501:
			apply501, s.applyThen501 = true, false
		}
		if s.failReadAfterSwap {
			s.failReadAfterSwap, s.failRead = false, true
		}
		s.mu.Unlock()
	}
	if failErr != nil {
		c.how, c.err = rk5Failed, failErr
		s.record(c)
		return "", failErr
	}
	newETag, err := s.inner.PutIfMatch(ctx, key, data, etag)
	c.how, c.err = rk5Passed, err
	if err == nil {
		switch {
		case lose:
			c.how, c.err = rk5ReplyLost, rk5ErrInjected
			newETag, err = "", rk5ErrInjected
		case drop:
			c.how = rk5ETagDropped
			newETag = ""
		case apply501:
			c.how, c.err = rk5AppliedThen501, store.ErrUnsupported
			newETag, err = "", store.ErrUnsupported
		}
	}
	s.record(c)
	return newETag, err
}

func (s *rk5Store) Name() string {
	return s.inner.Name()
}

// rk5IsRootSwap and the predicates below select calls from the harness's
// records.
func rk5IsRootSwap(c rk5Call) bool { return c.op == "PutIfMatch" && c.key == rootKey }
func rk5IsRootRead(c rk5Call) bool { return c.op == "GetWithETag" && c.key == rootKey }
func rk5IsRootGet(c rk5Call) bool  { return c.op == "Get" && c.key == rootKey }
func rk5IsRootPut(c rk5Call) bool  { return c.op == "Put" && c.key == rootKey }
func rk5IsRootHead(c rk5Call) bool { return c.op == "Head" && c.key == rootKey }
func rk5IsDelete(c rk5Call) bool   { return c.op == "Delete" }
func rk5IsSnapPut(c rk5Call) bool {
	return c.op == "Put" && strings.HasPrefix(c.key, snapshotPrefix)
}
func rk5IsWrite(c rk5Call) bool {
	return c.op == "Put" || c.op == "PutIfMatch" || c.op == "Delete"
}

// rk5Filter returns the calls keep selects, in order.
func rk5Filter(calls []rk5Call, keep func(rk5Call) bool) []rk5Call {
	var out []rk5Call
	for _, c := range calls {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// rk5First returns the index of the first call match selects, or -1.
func rk5First(calls []rk5Call, match func(rk5Call) bool) int {
	for i, c := range calls {
		if match(c) {
			return i
		}
	}
	return -1
}

// rk5Named returns the snapshot key a root pointer's bytes name, or "" if
// they do not decode as rootPointer.
func rk5Named(data []byte) string {
	var rp rootPointer
	if json.Unmarshal(data, &rp) != nil {
		return ""
	}
	return rp.Snapshot
}

// rk5KeyEpoch returns the epoch in a snapshot key: snapshotPrefix, the epoch
// as twenty decimal digits, "-", the nonce, ".json.gz".
func rk5KeyEpoch(key string) (uint64, bool) {
	name, ok := strings.CutPrefix(key, snapshotPrefix)
	if !ok || len(name) < 21 || name[20] != '-' {
		return 0, false
	}
	e, err := strconv.ParseUint(name[:20], 10, 64)
	return e, err == nil
}

// rk5ETag is *store.Local's ETag for body: the lowercase hexadecimal MD5 of it
// (ADR 0022 section 8, the facts; ADR 0020 section 6).
func rk5ETag(body []byte) string {
	sum := md5.Sum(body)
	return hex.EncodeToString(sum[:])
}

// rk5WriteForeignRoot writes, on local, a foreign root pointer, which is how a
// test stands for another writer (ADR 0022 section 8, the facts): it reads
// rootKey, decodes it as rootPointer, sets Writer to writer, Epoch to epoch
// and Snapshot to a key under snapshotPrefix at that epoch with the nonce
// ffffffffffff, copies the bytes of the snapshot the root pointer named to
// that key, and puts the marshalled root pointer at rootKey. It returns the
// root pointer it wrote. It touches no *testing.T, so that a store fake may
// call it.
func rk5WriteForeignRoot(ctx context.Context, local *store.Local, epoch uint64, writer string) (rootPointer, error) {
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

// rk5Done is one call running in its own goroutine. val, returned, panicked
// and pval may be read only once done is closed.
type rk5Done[T any] struct {
	done     chan struct{}
	val      T
	returned bool
	panicked bool
	pval     any
}

// rk5Go runs fn in its own goroutine, recovering and recording a panic out of
// it. fn must not touch t: after a timeout its goroutine is abandoned.
func rk5Go[T any](fn func() T) *rk5Done[T] {
	c := &rk5Done[T]{done: make(chan struct{})}
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

// wait waits for the call under rk5Bound and returns its value. A hang, a
// panic or a runtime.Goexit fails the test.
func (c *rk5Done[T]) wait(t *testing.T, what string) T {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(rk5Bound):
		t.Fatalf("%s has not returned after %v, so it is hung", what, rk5Bound)
	}
	if c.panicked {
		t.Fatalf("%s panicked with %v", what, c.pval)
	}
	if !c.returned {
		t.Fatalf("%s ended without returning (runtime.Goexit)", what)
	}
	return c.val
}

// rk5NewHarness makes a harness over a fresh local bucket under the test's
// temporary directory, in the unconditional mode if asked, and the test's
// context, which a cleanup cancels.
func rk5NewHarness(t *testing.T, unconditional bool) (*rk5Store, context.Context) {
	t.Helper()
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &rk5Store{inner: local, unconditional: unconditional}, ctx
}

// rk5Config is every mount's Config: the root directory owned by rk5Caller,
// no pruning and no wait on the write budget (ADR 0022 section 8), no opt-in,
// and log, or a discarding logger if log is nil.
func rk5Config(st store.Store, log *slog.Logger) Config {
	if log == nil {
		log = quietLog()
	}
	return Config{
		Store:             st,
		ChunkSize:         4096,
		OwnerUID:          501,
		OwnerGID:          20,
		SnapshotRetention: -1,
		MaxDirtyBytes:     -1,
		Log:               log,
	}
}

// rk5NewResult is what one New returned.
type rk5NewResult struct {
	fs  *FS
	err error
}

// rk5TryNew runs New in rk5Go and waits for it under rk5Bound.
func rk5TryNew(t *testing.T, ctx context.Context, what string, cfg Config) rk5NewResult {
	t.Helper()
	return rk5Go(func() rk5NewResult {
		fs, err := New(ctx, cfg)
		return rk5NewResult{fs, err}
	}).wait(t, what)
}

// rk5MustNew is rk5TryNew for a New that must succeed.
func rk5MustNew(t *testing.T, ctx context.Context, what string, cfg Config) *FS {
	t.Helper()
	r := rk5TryNew(t, ctx, what, cfg)
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	if r.fs == nil {
		t.Fatalf("%s returned a nil *FS and a nil error", what)
	}
	return r.fs
}

// rk5Create creates name in the root directory of fs, as rk5Caller, a regular
// file, exclusively, and returns what Create returned as its error.
func rk5Create(t *testing.T, ctx context.Context, fs *FS, name string) error {
	t.Helper()
	return rk5Go(func() error {
		_, _, err := fs.Create(ctx, rk5Caller, fs.Root(), name, vfs.SetAttr{}, true)
		return err
	}).wait(t, fmt.Sprintf("Create %q in the root directory", name))
}

// rk5Sync runs fs.Sync and returns what it returned.
func rk5Sync(t *testing.T, ctx context.Context, fs *FS, what string) error {
	t.Helper()
	return rk5Go(func() error { return fs.Sync(ctx) }).wait(t, what)
}

// rk5Commit is ADR 0022 section 8's commit: a Create of a fresh name in the
// root directory, by rk5Caller, which must succeed, then Sync. It returns what
// Sync returned.
func rk5Commit(t *testing.T, ctx context.Context, fs *FS, name, what string) error {
	t.Helper()
	if err := rk5Create(t, ctx, fs, name); err != nil {
		t.Fatalf("%s: Create %q in the root directory as its owner (%d, %d) = %v, want success", what, name,
			rk5Caller.UID, rk5Caller.GID, err)
	}
	return rk5Sync(t, ctx, fs, what)
}

// rk5Fields is what rk5Read reads of an FS.
type rk5Fields struct {
	dirty        bool
	epoch        uint64
	rootETag     string
	knowledge    rootState
	doubtKey     string
	rootSnapshot string
}

// rk5Read reads FS.dirty, FS.epoch, FS.rootETag, f.rootKnowledge.State(),
// FS.doubtKey and FS.rootSnapshot in one hold of FS.mu for reading (ADR 0022
// section 8, the facts).
func rk5Read(t *testing.T, fs *FS, what string) rk5Fields {
	t.Helper()
	return rk5Go(func() rk5Fields {
		fs.mu.RLock()
		defer fs.mu.RUnlock()
		return rk5Fields{
			dirty:        fs.dirty,
			epoch:        fs.epoch,
			rootETag:     fs.rootETag,
			knowledge:    fs.rootKnowledge.State(),
			doubtKey:     fs.doubtKey,
			rootSnapshot: fs.rootSnapshot,
		}
	}).wait(t, what)
}

// rk5CheckKnowledge requires the knowledge f records to be want, and, when
// checkDoubt is set, FS.doubtKey to be doubtKey (ADR 0022 sections 2 to 4).
func rk5CheckKnowledge(t *testing.T, f rk5Fields, want rootState, checkDoubt bool, doubtKey, when, where string) {
	t.Helper()
	if f.knowledge != want {
		t.Errorf("%s, f.rootKnowledge.State() = %v, want %v (ADR 0022 sections 2 to 4; %s)", when, f.knowledge,
			want, where)
	}
	if checkDoubt && f.doubtKey != doubtKey {
		t.Errorf("%s, FS.doubtKey = %q, want %q: doubtKey names the attempt in doubt while the root is in "+
			"doubt, and is \"\" otherwise (ADR 0022 section 2; %s)", when, f.doubtKey, doubtKey, where)
	}
}

// rk5Mode returns fs.mode.State(), which needs no lock (ADR 0014 section 11).
func rk5Mode(t *testing.T, fs *FS, what string) mountMode {
	t.Helper()
	return rk5Go(func() mountMode { return fs.mode.State() }).wait(t, what)
}

// rk5Root reads the root pointer through local and decodes it as rootPointer.
// ok is false, and the test has failed, if either step fails.
func rk5Root(t *testing.T, ctx context.Context, local *store.Local, where string) (raw []byte, rp rootPointer, ok bool) {
	t.Helper()
	raw, err := local.Get(ctx, rootKey)
	if err != nil {
		t.Errorf("Get(%q) on the inner store = %v, want the root pointer (%s)", rootKey, err, where)
		return nil, rootPointer{}, false
	}
	if err := json.Unmarshal(raw, &rp); err != nil {
		t.Errorf("the root pointer %q on the inner store does not decode as rootPointer: %v (%s)", raw, err, where)
		return raw, rootPointer{}, false
	}
	return raw, rp, true
}

// rk5CheckRemount requires New on local, with SnapshotRetention -1, to
// succeed, and Lookup of every name in present in its root directory to
// succeed.
func rk5CheckRemount(t *testing.T, ctx context.Context, local *store.Local, present []string, where string) {
	t.Helper()
	what := "the remount, New on the inner store with SnapshotRetention -1"
	r := rk5TryNew(t, ctx, what, rk5Config(local, nil))
	if r.err != nil {
		t.Errorf("%s = %v, want success (%s)", what, r.err, where)
		return
	}
	if r.fs == nil {
		t.Errorf("%s returned a nil *FS and a nil error (%s)", what, where)
		return
	}
	fs := r.fs
	for _, name := range present {
		err := rk5Go(func() error {
			_, _, err := fs.Lookup(ctx, rk5Caller, fs.Root(), name)
			return err
		}).wait(t, fmt.Sprintf("Lookup of %q in the remount's root directory", name))
		if err != nil {
			t.Errorf("Lookup of %q in the remount's root directory = %v, want success: the commit that holds it "+
				"reached the root pointer (%s)", name, err, where)
		}
	}
}

// rk5Record is one captured log record, with its attributes, those preset
// through WithAttrs first, keyed under any group the logger was given.
type rk5Record struct {
	level slog.Level
	msg   string
	attrs []slog.Attr
}

// attr returns the value of the record's attribute key.
func (r rk5Record) attr(key string) (slog.Value, bool) {
	for _, a := range r.attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return slog.Value{}, false
}

// String describes the record for failure messages.
func (r rk5Record) String() string {
	return fmt.Sprintf("%v %q %v", r.level, r.msg, r.attrs)
}

// rk5Capture collects the records a mount logs (ADR 0022 section 8, the facts;
// btCapture in internal/blobfs/budget_test.go).
type rk5Capture struct {
	mu   sync.Mutex
	recs []rk5Record
}

func (c *rk5Capture) logger() *slog.Logger { return slog.New(&rk5Handler{c: c}) }

// records returns a copy of the captured records.
func (c *rk5Capture) records() []rk5Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]rk5Record(nil), c.recs...)
}

// rk5Handler is the slog.Handler behind rk5Capture. An attribute under a group
// is keyed "group.key", so it does not match a top-level key.
type rk5Handler struct {
	c      *rk5Capture
	preset []slog.Attr
	prefix string
}

func (h *rk5Handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *rk5Handler) Handle(_ context.Context, r slog.Record) error {
	rec := rk5Record{level: r.Level, msg: r.Message, attrs: append([]slog.Attr(nil), h.preset...)}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs = append(rec.attrs, slog.Attr{Key: h.prefix + a.Key, Value: a.Value.Resolve()})
		return true
	})
	h.c.mu.Lock()
	h.c.recs = append(h.c.recs, rec)
	h.c.mu.Unlock()
	return nil
}

func (h *rk5Handler) WithAttrs(as []slog.Attr) slog.Handler {
	n := &rk5Handler{c: h.c, prefix: h.prefix, preset: append([]slog.Attr(nil), h.preset...)}
	for _, a := range as {
		n.preset = append(n.preset, slog.Attr{Key: h.prefix + a.Key, Value: a.Value.Resolve()})
	}
	return n
}

func (h *rk5Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &rk5Handler{c: h.c, prefix: h.prefix + name + ".", preset: append([]slog.Attr(nil), h.preset...)}
}

// rk5CheckConflictRecord requires the captured records to hold one at
// slog.LevelError whose message contains "commit conflict" and whose
// attribute writer has the string value foreign.Writer, their_epoch the value
// foreign.Epoch and their_snapshot the value foreign.Snapshot: the record of
// ProbeTheirs, which names the writer that took the bucket (ADR 0022 section
// 3; Assumption 15; the issue's second done-when).
func rk5CheckConflictRecord(t *testing.T, c *rk5Capture, foreign rootPointer, where string) {
	t.Helper()
	all := c.records()
	var conflicts []rk5Record
	for _, r := range all {
		if r.level == slog.LevelError && strings.Contains(r.msg, "commit conflict") {
			conflicts = append(conflicts, r)
		}
	}
	if len(conflicts) == 0 {
		t.Errorf("the mount logged no record at %v whose message contains %q, want one naming the writer that "+
			"took the bucket (ADR 0022 section 3; %s). Every record: %v", slog.LevelError, "commit conflict", where,
			all)
		return
	}
	wantEpoch := strconv.FormatUint(foreign.Epoch, 10)
	for _, r := range conflicts {
		w, wok := r.attr("writer")
		e, eok := r.attr("their_epoch")
		s, sok := r.attr("their_snapshot")
		if wok && w.Kind() == slog.KindString && w.String() == foreign.Writer &&
			eok && e.String() == wantEpoch && sok && s.String() == foreign.Snapshot {
			return
		}
	}
	t.Errorf("no record at %v whose message contains %q carries the attribute writer with the string value %q, "+
		"their_epoch %s and their_snapshot %q, want one: the record of ProbeTheirs names the writer, epoch and "+
		"snapshot of the root pointer the read found (ADR 0022 section 3; Assumption 15; %s). The conflict "+
		"records: %v", slog.LevelError, "commit conflict", foreign.Writer, wantEpoch, foreign.Snapshot, where,
		conflicts)
}

// TestRootStatesTableAndStrings is K1 of ADR 0022 section 8: the table of
// root-pointer knowledge is named "rootKnowledge", after the field that holds
// its state (ADR 0014 section 3), and has exactly section 2's four edges, in
// order; String gives docs/vocabulary.md V2's names, and "<type>(<n>)", n in
// decimal, for a value with no name (ADR 0022 section 2).
func TestRootStatesTableAndStrings(t *testing.T) {
	const where = "ADR 0022 section 2; section 8, K1"
	if got := rootStates.Name(); got != "rootKnowledge" {
		t.Errorf("rootStates.Name() = %q, want %q: a table is named after the field that holds its state, "+
			"FS.rootKnowledge (ADR 0014 section 3; %s)", got, "rootKnowledge", where)
	}
	want := []fsm.Edge[rootState, rootEvent]{
		{From: rootKnown, On: rootSwapOK, To: rootKnown},
		{From: rootKnown, On: rootSwapUnknown, To: rootInDoubt},
		{From: rootInDoubt, On: rootProbeOurs, To: rootKnown},
		{From: rootInDoubt, On: rootProbeUnchanged, To: rootKnown},
	}
	got := rootStates.Edges()
	if len(got) != len(want) {
		t.Fatalf("rootStates.Edges() = %v (length %d), want %v (length %d): the table gains no other edge (%s)",
			got, len(got), want, len(want), where)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rootStates.Edges()[%d] = %v (From %d, On %d, To %d), want %v (From %d, On %d, To %d) (%s)",
				i, got[i], uint8(got[i].From), uint8(got[i].On), uint8(got[i].To), want[i],
				uint8(want[i].From), uint8(want[i].On), uint8(want[i].To), where)
		}
	}

	cases := []struct {
		expr string
		got  string
		want string
	}{
		{"rootKnown.String()", rootKnown.String(), "Known"},
		{"rootInDoubt.String()", rootInDoubt.String(), "InDoubt"},
		{"rootSwapOK.String()", rootSwapOK.String(), "SwapOK"},
		{"rootSwapUnknown.String()", rootSwapUnknown.String(), "SwapUnknown"},
		{"rootProbeOurs.String()", rootProbeOurs.String(), "ProbeOurs"},
		{"rootProbeUnchanged.String()", rootProbeUnchanged.String(), "ProbeUnchanged"},
		{"rootState(0).String()", rootState(0).String(), "rootState(0)"},
		{"rootState(3).String()", rootState(3).String(), "rootState(3)"},
		{"rootEvent(0).String()", rootEvent(0).String(), "rootEvent(0)"},
		{"rootEvent(5).String()", rootEvent(5).String(), "rootEvent(5)"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q (docs/vocabulary.md V2; %s)", tc.expr, tc.got, tc.want, where)
		}
	}
}

// rk5Verdict names a probeVerdict for failure messages.
func rk5Verdict(v probeVerdict) string {
	switch v {
	case probeOurs:
		return "probeOurs"
	case probeUnchanged:
		return "probeUnchanged"
	case probeTheirs:
		return "probeTheirs"
	case probeFailed:
		return "probeFailed"
	}
	return fmt.Sprintf("probeVerdict(%d)", uint8(v))
}

// TestClassifyRootDecidesWhatOneReadShows is K2 of ADR 0022 section 8:
// classifyRoot over section 3's rules, in order, the first that applies
// deciding, with a case for each:
//  1. a not-found while the mount descends from no root: probeUnchanged;
//  2. a not-found while it descends from one: probeTheirs;
//  3. any other error: probeFailed, even beside a body that names the attempt;
//  4. a body that does not decode as rootPointer, or decodes with another
//     Version or an empty Snapshot: probeFailed, for each of ADR 0021 G6's
//     five bodies, for this build's version with an empty Snapshot, and for
//     another version naming the attempt;
//  5. a body naming doubtKey: probeOurs, with the decoded body returned, and
//     probeFailed with an empty etag;
//  6. a body naming rootSnapshot: probeUnchanged, and probeFailed with an
//     empty etag;
//  7. a body naming a third key: probeTheirs, with or without an etag.
//
// A root pointer of this build's version is made from one read off a New's
// bucket, as section 8 says, since the version number is not a name a test
// may use. Where section 3 says what rootPointer is returned, the zero value
// where nothing decoded and the decoded body for a positive verdict, it is
// asserted; for a body that decoded but decided nothing it is not.
func TestClassifyRootDecidesWhatOneReadShows(t *testing.T) {
	const where = "ADR 0022 section 3; section 8, K2"
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rk5MustNew(t, ctx, "setup: New over an empty bucket, for a root pointer of this build's version",
		rk5Config(local, nil))
	_, base, ok := rk5Root(t, ctx, local, where)
	if !ok {
		t.Fatalf("setup: no root pointer of this build's version could be read off the bucket (%s)", where)
	}

	const (
		rootSnap = snapshotPrefix + "00000000000000000003-aaaaaaaaaaaa.json.gz"
		doubt    = snapshotPrefix + "00000000000000000004-bbbbbbbbbbbb.json.gz"
		third    = snapshotPrefix + "00000000000000000004-cccccccccccc.json.gz"
	)
	// body returns base naming key at epoch, with version, and its bytes.
	body := func(key string, epoch uint64, version int) (rootPointer, []byte) {
		rp := base
		rp.Snapshot, rp.Epoch, rp.Version = key, epoch, version
		b, err := json.Marshal(rp)
		if err != nil {
			t.Fatalf("setup: marshalling a root pointer: %v", err)
		}
		return rp, b
	}
	ours, oursBody := body(doubt, 4, base.Version)
	unchanged, unchangedBody := body(rootSnap, 3, base.Version)
	theirs, theirsBody := body(third, 4, base.Version)
	_, emptySnapBody := body("", 1, base.Version)
	_, otherVersionOursBody := body(doubt, 4, base.Version+1)
	zero := &rootPointer{}

	cases := []struct {
		name         string
		raw          []byte
		etag         string
		err          error
		rootSnapshot string
		want         probeVerdict
		wantRP       *rootPointer // nil: not asserted
	}{
		{"rule 1: store.ErrNotFound, no root descended from", nil, "", store.ErrNotFound, "",
			probeUnchanged, zero},
		{"rule 1: a wrapped store.ErrNotFound, no root descended from", nil, "",
			fmt.Errorf("get %q: %w", rootKey, store.ErrNotFound), "", probeUnchanged, zero},
		{"rule 2: store.ErrNotFound, a root descended from", nil, "", store.ErrNotFound, rootSnap,
			probeTheirs, zero},
		{"rule 3: another error", nil, "", rk5ErrInjected, rootSnap, probeFailed, zero},
		{"rule 3 before rule 5: another error beside a body naming the attempt", oursBody, "e", rk5ErrInjected,
			rootSnap, probeFailed, nil},
		{"rule 4: null", []byte(`null`), "e", nil, rootSnap, probeFailed, zero},
		{"rule 4: {}", []byte(`{}`), "e", nil, rootSnap, probeFailed, zero},
		{"rule 4: an error document served with 200", []byte(`{"Code":"InternalError","Message":"boom"}`), "e",
			nil, rootSnap, probeFailed, zero},
		{"rule 4: not JSON, <Error/>", []byte(`<Error/>`), "e", nil, rootSnap, probeFailed, zero},
		{"rule 4: a root pointer of version 99",
			[]byte(`{"version":99,"epoch":7,"snapshot":"snapshots/00000000000000000007-ffffffffffff.json.gz","fsid":1,"written":1}`),
			"e", nil, rootSnap, probeFailed, nil},
		{"rule 4 before rule 6: this build's version with an empty Snapshot, no root descended from",
			emptySnapBody, "e", nil, "", probeFailed, nil},
		{"rule 4 before rule 5: another version naming the attempt", otherVersionOursBody, "e", nil, rootSnap,
			probeFailed, nil},
		{"rule 5: naming the attempt", oursBody, "e5", nil, rootSnap, probeOurs, &ours},
		{"rule 5: naming the attempt, with no ETag", oursBody, "", nil, rootSnap, probeFailed, nil},
		{"rule 6: naming the snapshot descended from", unchangedBody, "e6", nil, rootSnap, probeUnchanged,
			&unchanged},
		{"rule 6: naming the snapshot descended from, with no ETag", unchangedBody, "", nil, rootSnap,
			probeFailed, nil},
		{"rule 7: naming a third key", theirsBody, "e7", nil, rootSnap, probeTheirs, &theirs},
		{"rule 7: naming a third key, with no ETag", theirsBody, "", nil, rootSnap, probeTheirs, &theirs},
	}
	type result struct {
		v  probeVerdict
		rp rootPointer
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			what := fmt.Sprintf("classifyRoot(%q, %q, %v, %q, %q)", tc.raw, tc.etag, tc.err, tc.rootSnapshot,
				doubt)
			r := rk5Go(func() result {
				v, rp := classifyRoot(tc.raw, tc.etag, tc.err, tc.rootSnapshot, doubt)
				return result{v, rp}
			}).wait(t, what)
			if r.v != tc.want {
				t.Errorf("%s = %s, want %s (%s)", what, rk5Verdict(r.v), rk5Verdict(tc.want), where)
			}
			if tc.wantRP != nil && r.rp != *tc.wantRP {
				t.Errorf("%s returned the root pointer %+v, want %+v: the decoded body where one decoded, and the "+
					"zero value otherwise (%s)", what, r.rp, *tc.wantRP, where)
			}
		})
	}
}

// TestNewStartsKnownWithTheRootPointersKey is K3 of ADR 0022 section 8: after
// New, over an empty bucket and over a bucket that holds a filesystem,
// f.rootKnowledge.State() is rootKnown, FS.doubtKey is "", and FS.rootSnapshot
// is the key the root pointer at rootKey names, which for a fresh bucket is
// the key of the one object under snapshotPrefix (ADR 0022 section 2, section
// 8's facts).
func TestNewStartsKnownWithTheRootPointersKey(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		name := "an empty bucket"
		if seeded {
			name = "a bucket that holds a filesystem"
		}
		t.Run(name, func(t *testing.T) {
			where := "ADR 0022 section 8, K3, " + name
			local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
			if err != nil {
				t.Fatalf("setup: store.NewLocal: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if seeded {
				s := rk5MustNew(t, ctx, "setup: New over an empty bucket", rk5Config(local, nil))
				if err := rk5Commit(t, ctx, s, "c1", "setup: Sync of the seeding commit of c1"); err != nil {
					t.Fatalf("setup: Sync of the seeding commit of c1 = %v, want success", err)
				}
			}

			fs := rk5MustNew(t, ctx, where+": New", rk5Config(local, nil))
			f := rk5Read(t, fs, "reading the FS's fields after New")
			rk5CheckKnowledge(t, f, rootKnown, true, "", "after New over "+name, where)

			_, rp, ok := rk5Root(t, ctx, local, where)
			if ok && f.rootSnapshot != rp.Snapshot {
				t.Errorf("after New over %s, FS.rootSnapshot = %q, want %q, the key the root pointer names (ADR "+
					"0022 section 2; %s)", name, f.rootSnapshot, rp.Snapshot, where)
			}
			if !seeded {
				infos, err := local.List(ctx, snapshotPrefix, "", 0)
				if err != nil {
					t.Fatalf("List(%q) on the bucket = %v (%s)", snapshotPrefix, err, where)
				}
				if len(infos) != 1 || f.rootSnapshot != infos[0].Key {
					t.Errorf("after New over %s the bucket holds %v under %q and FS.rootSnapshot = %q, want one "+
						"key, which FS.rootSnapshot names (%s)", name, infos, snapshotPrefix, f.rootSnapshot, where)
				}
			}
		})
	}
}

// TestLostSwapReplyIsAdoptedNotTakenForASecondWriter is K4 of ADR 0022
// section 8, from CommitProtocol_SelfDiverge's route (route 1; the issue's
// first done-when): a swap that took effect and lost its reply is settled by
// one read of the root pointer, which names the attempt, and the mount adopts
// it: Sync returns nil, the mount stays writable, and its next swap is made
// against the ETag the read gave (ADR 0022 sections 1, 3 and 4).
//
// New over an empty bucket on the harness; the lost reply armed; a commit of
// c2: Sync returns nil; the harness recorded, after the PutIfMatch it applied,
// exactly one GetWithETag of rootKey and no further PutIfMatch of rootKey;
// f.mode.State() is mountWritable, f.rootKnowledge.State() rootKnown,
// FS.doubtKey "", FS.rootSnapshot the key the applied PutIfMatch wrote,
// FS.epoch 2, FS.rootETag the MD5 of the root pointer's bytes on the inner
// store, which names the applied attempt's key, and FS.dirty false. A commit of c3: Sync returns nil, its PutIfMatch
// passed through with that MD5 as its etag argument, and its key carries
// epoch 3. A remount on the inner store has c2 and c3.
//
// At edcfc15 the first Sync returns an error, and the second's swap fails
// with store.ErrPrecondition and diverges the mount.
func TestLostSwapReplyIsAdoptedNotTakenForASecondWriter(t *testing.T) {
	const where = "ADR 0022 section 8, K4"
	h, ctx := rk5NewHarness(t, false)
	fs := rk5MustNew(t, ctx, where+": New over an empty bucket on the harness", rk5Config(h, nil))

	// c2: the swap is applied and its reply lost.
	mark := len(h.records())
	h.armLoseSwap()
	if err := rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2"); err != nil {
		t.Errorf("%s: Sync of the commit of c2, whose swap of the root pointer took effect and lost its reply, = "+
			"%v, want nil: the mount reads the root pointer once, finds it naming its attempt, and adopts it (ADR "+
			"0022 sections 1, 3 and 4; route 1)", where, err)
	}
	all := h.records()
	calls := all[mark:]
	swaps := rk5Filter(calls, rk5IsRootSwap)
	applied := rk5First(calls, func(c rk5Call) bool { return rk5IsRootSwap(c) && c.how == rk5ReplyLost })
	var appliedKey string
	if applied < 0 || len(swaps) != 1 {
		t.Errorf("the commit of c2 made the PutIfMatch calls of the root pointer %q %v, want exactly one, the one "+
			"the harness applied and lost the reply of: no further swap is made once the read has shown the root "+
			"pointer naming the attempt (ADR 0022 section 3; %s)", rootKey, swaps, where)
	}
	if applied >= 0 {
		appliedKey = rk5Named(calls[applied].data)
		if reads := rk5Filter(calls[applied+1:], rk5IsRootRead); len(reads) != 1 {
			t.Errorf("after the PutIfMatch whose reply was lost the harness recorded the GetWithETag calls of %q "+
				"%v, want exactly one: the one read that settles the doubt (ADR 0022 section 3; %s). Every call "+
				"since the commit began: %v", rootKey, reads, where, calls)
		}
	}

	if got := rk5Mode(t, fs, "f.mode.State() after the commit of c2"); got != mountWritable {
		t.Errorf("after the commit of c2, f.mode.State() = %v, want %v: a lost reply is not a second writer "+
			"(ADR 0022 section 1; %s)", got, mountWritable, where)
	}
	f := rk5Read(t, fs, "reading the FS's fields after the commit of c2")
	rk5CheckKnowledge(t, f, rootKnown, true, "", "after the commit of c2", where)
	if appliedKey != "" && f.rootSnapshot != appliedKey {
		t.Errorf("after the commit of c2, FS.rootSnapshot = %q, want %q, the key the applied PutIfMatch wrote: "+
			"the attempt is adopted (ADR 0022 section 3, probeOurs; %s)", f.rootSnapshot, appliedKey, where)
	}
	if f.epoch != 2 {
		t.Errorf("after the commit of c2, FS.epoch = %d, want 2: the adopted attempt's epoch (ADR 0022 section "+
			"3, probeOurs; %s)", f.epoch, where)
	}
	if f.dirty {
		t.Errorf("after the commit of c2, FS.dirty is true, want false: an adoption at the failure site takes "+
			"the success path whole (ADR 0022 section 4; Assumption 9; %s)", where)
	}

	// Through the inner store, after the records were read.
	raw, rp, ok := rk5Root(t, ctx, h.inner, where)
	rootTag := ""
	if ok {
		rootTag = rk5ETag(raw)
		if appliedKey != "" && rp.Snapshot != appliedKey {
			t.Errorf("the root pointer on the inner store names %q, want %q, the key the applied PutIfMatch "+
				"wrote (%s)", rp.Snapshot, appliedKey, where)
		}
		if f.rootETag != rootTag {
			t.Errorf("after the commit of c2, FS.rootETag = %q, want %q, the MD5 of the root pointer's bytes on "+
				"the inner store: the mount takes the read's ETag (ADR 0022 section 3, probeOurs; %s)", f.rootETag,
				rootTag, where)
		}
	}

	// c3: a swap against the ETag the read gave.
	mark = len(all)
	if err := rk5Commit(t, ctx, fs, "c3", where+": Sync of the commit of c3"); err != nil {
		t.Errorf("%s: Sync of the commit of c3 = %v, want nil: its swap is conditioned on the ETag the read gave, "+
			"which is the root pointer's", where, err)
	}
	swaps = rk5Filter(h.records()[mark:], rk5IsRootSwap)
	if len(swaps) != 1 {
		t.Errorf("the commit of c3 made the PutIfMatch calls of the root pointer %q %v, want exactly one (%s)",
			rootKey, swaps, where)
	} else {
		s := swaps[0]
		if s.how != rk5Passed || s.err != nil {
			t.Errorf("the swap of the commit of c3 is %v, want one passed through and applied (%s)", s, where)
		}
		if ok && s.etag != rootTag {
			t.Errorf("the swap of the commit of c3 has the etag argument %q, want %q, the MD5 of the root "+
				"pointer the lost reply wrote (ADR 0022 section 8, K4; %s)", s.etag, rootTag, where)
		}
		key := rk5Named(s.data)
		if e, eok := rk5KeyEpoch(key); !eok || e != 3 {
			t.Errorf("the swap of the commit of c3 names the snapshot key %q, want one at epoch 3, the adopted "+
				"epoch plus one (ADR 0022 section 5; %s)", key, where)
		}
	}

	rk5CheckRemount(t, ctx, h.inner, []string{"c2", "c3"}, where)
}

// TestSwapRefusedWhileTheRootIsUnchangedRetriesAtTheNextCommit is K5 of
// ADR 0022 section 8, from CommitProtocol_SelfDiverge409's route (route 2): a
// swap refused with store.ErrPrecondition while the root pointer is in fact
// unchanged (a 409, or a 412 whose condition holds), or failed with an
// untyped error that did not reach the store, is settled by one read that
// shows the root pointer naming the snapshot the namespace descends from. The
// commit fails, the mount stays writable and dirty, takes the read's ETag,
// and its next commit succeeds (ADR 0022 sections 1, 3 and 4; Assumptions 2
// and 7).
//
// Two subtests, each on a fresh harness with one commit behind it, New's.
// The swap of c2 fails with the subtest's error: Sync returns a non-nil
// error, for which errors.Is(err, store.ErrPrecondition) holds in the first;
// the harness recorded exactly one GetWithETag of rootKey after the refused
// PutIfMatch; f.mode.State() is mountWritable; f.rootKnowledge.State() is
// rootKnown and FS.doubtKey ""; FS.rootSnapshot and FS.epoch are unchanged,
// FS.rootETag is the MD5 of the root pointer's bytes on the inner store, and
// FS.dirty is true. A Create of c3 returns nil; Sync returns nil, its
// PutIfMatch passed through with that MD5 as its etag argument; and a remount
// has c2 and c3.
//
// At edcfc15 the first subtest's mount diverges and the Create returns
// vfs.ErrStale. The second subtest passes at edcfc15 but for the GetWithETag
// count (Assumption 23): its purpose is the single read.
func TestSwapRefusedWhileTheRootIsUnchangedRetriesAtTheNextCommit(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		precondition bool
	}{
		{"store.ErrPrecondition", store.ErrPrecondition, true},
		{"an untyped error", rk5ErrInjected, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			where := "ADR 0022 section 8, K5, the swap failed with " + tc.name
			h, ctx := rk5NewHarness(t, false)
			fs := rk5MustNew(t, ctx, where+": New over an empty bucket on the harness", rk5Config(h, nil))
			before := rk5Read(t, fs, "reading the FS's fields before the commit of c2")

			// c2: the swap fails, and the root pointer is unchanged.
			mark := len(h.records())
			h.armFailSwap(tc.err)
			err := rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2")
			if err == nil {
				t.Errorf("%s: Sync of the commit of c2, whose swap failed, = nil, want an error: no in-line retry "+
					"(ADR 0022 section 3; Assumption 7)", where)
			} else if tc.precondition && !errors.Is(err, store.ErrPrecondition) {
				t.Errorf("%s: Sync of the commit of c2 = %v, want an error for which errors.Is(err, "+
					"store.ErrPrecondition) holds: swap root pointer: %%w, wrapping the swap's error (ADR 0022 "+
					"section 4)", where, err)
			}
			all := h.records()
			calls := all[mark:]
			refused := rk5First(calls, func(c rk5Call) bool { return rk5IsRootSwap(c) && c.how == rk5Failed })
			if refused < 0 {
				t.Errorf("the commit of c2 made no PutIfMatch of the root pointer %q that the harness failed, so "+
					"the armed error was never delivered (%s). Every call since the commit began: %v", rootKey,
					where, calls)
			} else if reads := rk5Filter(calls[refused+1:], rk5IsRootRead); len(reads) != 1 {
				t.Errorf("after the refused PutIfMatch the harness recorded the GetWithETag calls of %q %v, want "+
					"exactly one: no swap's error alone decides anything, and one read settles it (ADR 0022 "+
					"section 1, section 3; Assumption 2; %s)", rootKey, reads, where)
			}

			if got := rk5Mode(t, fs, "f.mode.State() after the commit of c2"); got != mountWritable {
				t.Errorf("after the commit of c2, f.mode.State() = %v, want %v: the read shows the root pointer "+
					"unchanged, so no other writer took the bucket (ADR 0022 section 1; %s)", got, mountWritable,
					where)
			}
			f := rk5Read(t, fs, "reading the FS's fields after the commit of c2")
			rk5CheckKnowledge(t, f, rootKnown, true, "", "after the commit of c2", where)
			if f.rootSnapshot != before.rootSnapshot {
				t.Errorf("after the commit of c2, FS.rootSnapshot = %q, want %q, as it was: the read shows the "+
					"root pointer unchanged (ADR 0022 section 3, probeUnchanged; %s)", f.rootSnapshot,
					before.rootSnapshot, where)
			}
			if f.epoch != before.epoch {
				t.Errorf("after the commit of c2, FS.epoch = %d, want %d, as it was: the read shows the root "+
					"pointer unchanged (ADR 0022 section 3, probeUnchanged; %s)", f.epoch, before.epoch, where)
			}
			if !f.dirty {
				t.Errorf("after the commit of c2, FS.dirty is false, want true: the commit failed (ADR 0022 "+
					"section 3; %s)", where)
			}

			// Through the inner store, after the records were read.
			raw, _, ok := rk5Root(t, ctx, h.inner, where)
			tag := ""
			if ok {
				tag = rk5ETag(raw)
				if f.rootETag != tag {
					t.Errorf("after the commit of c2, FS.rootETag = %q, want %q, the MD5 of the root pointer's "+
						"bytes on the inner store: the mount takes the read's ETag (ADR 0022 section 3, "+
						"probeUnchanged; %s)", f.rootETag, tag, where)
				}
			}

			// c3: the mount accepts changes, and its next commit succeeds.
			if err := rk5Create(t, ctx, fs, "c3"); err != nil {
				t.Fatalf("Create %q after the commit of c2 = %v, want success: the mount did not diverge (ADR "+
					"0022 section 1; %s)", "c3", err, where)
			}
			mark = len(h.records())
			if err := rk5Sync(t, ctx, fs, where+": Sync after the Create of c3"); err != nil {
				t.Errorf("%s: Sync after the Create of c3 = %v, want nil: its swap is conditioned on the ETag the "+
					"read gave (ADR 0022 section 3)", where, err)
			}
			swaps := rk5Filter(h.records()[mark:], rk5IsRootSwap)
			if len(swaps) != 1 || swaps[0].how != rk5Passed || swaps[0].err != nil || (ok && swaps[0].etag != tag) {
				t.Errorf("the Sync after the Create of c3 made the PutIfMatch calls of %q %v, want exactly one, "+
					"passed through and applied, with the etag argument %q (%s)", rootKey, swaps, tag, where)
			}

			rk5CheckRemount(t, ctx, h.inner, []string{"c2", "c3"}, where)
		})
	}
}

// TestASwapLostToAnotherWriterDivergesAndNamesTheWriter is K6 of ADR 0022
// section 8 (the issue's second done-when; CommitProtocol_TwoWriters' loser,
// through ProbeTheirs): a swap refused because another writer moved the root
// pointer is followed by one read, which shows that writer's root pointer;
// the mount diverges in the same call, as at edcfc15, writes nothing more,
// and its log names the writer, its epoch and its snapshot (ADR 0022 sections
// 1 and 3; Assumption 15).
//
// New on the harness over an empty bucket, with a capturing logger; a foreign
// root pointer with Writer "other-host/4242" written on the inner store; a
// commit of c2: Sync returns an error whose text contains "commit conflict";
// the harness recorded the PutIfMatch passed through and refused by the inner
// store, then exactly one GetWithETag of rootKey, and no Put, PutIfMatch or
// Delete after it. f.mode.State() is mountDiverged, f.rootKnowledge.State()
// is rootInDoubt and FS.doubtKey is the key the refused attempt wrote, and a
// Create of c3 returns vfs.ErrStale. The captured records hold one at
// slog.LevelError whose message contains "commit conflict" with writer
// "other-host/4242",
// their_epoch the foreign root pointer's Epoch and their_snapshot its
// Snapshot. A second Sync returns an error, and the harness recorded no Put,
// PutIfMatch or Delete since the first (ADR 0014 section 11, M7).
//
// At edcfc15 the mount diverges without a read, and the record carries no
// writer.
func TestASwapLostToAnotherWriterDivergesAndNamesTheWriter(t *testing.T) {
	const where = "ADR 0022 section 8, K6"
	h, ctx := rk5NewHarness(t, false)
	logs := &rk5Capture{}
	fs := rk5MustNew(t, ctx, where+": New over an empty bucket on the harness, with a capturing logger",
		rk5Config(h, logs.logger()))
	before := rk5Read(t, fs, "reading the FS's fields before the foreign root pointer is written")

	// The harness's records are read before the inner store is touched.
	mark := len(h.records())
	foreign, err := rk5WriteForeignRoot(ctx, h.inner, before.epoch+1, rk5Writer)
	if err != nil {
		t.Fatalf("setup: writing a foreign root pointer on the inner store: %v", err)
	}

	// c2: the swap fails its condition on the inner store.
	err = rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2")
	if err == nil || !strings.Contains(err.Error(), "commit conflict") {
		t.Errorf("%s: Sync of the commit of c2, after another writer moved the root pointer, = %v, want an error "+
			"whose text contains %q (ADR 0022 section 4, the conflict error)", where, err, "commit conflict")
	}
	first := h.records()
	calls := first[mark:]
	refused := rk5First(calls, func(c rk5Call) bool {
		return rk5IsRootSwap(c) && c.how == rk5Passed && errors.Is(c.err, store.ErrPrecondition)
	})
	attemptKey := ""
	if refused < 0 {
		t.Errorf("the commit of c2 made no PutIfMatch of the root pointer %q that the inner store refused with "+
			"store.ErrPrecondition (%s). Every call since the commit began: %v", rootKey, where, calls)
	} else {
		attemptKey = rk5Named(calls[refused].data)
		after := calls[refused+1:]
		if reads := rk5Filter(after, rk5IsRootRead); len(reads) != 1 {
			t.Errorf("after the refused PutIfMatch the harness recorded the GetWithETag calls of %q %v, want "+
				"exactly one: a mount decides that another writer owns the bucket only on a positive showing from "+
				"a read of the root pointer (ADR 0022 section 1; %s)", rootKey, reads, where)
		}
		if writes := rk5Filter(after, rk5IsWrite); len(writes) != 0 {
			t.Errorf("after the refused PutIfMatch the harness recorded the writes %v, want none (ADR 0022 "+
				"section 3, probeTheirs; %s)", writes, where)
		}
	}
	f := rk5Read(t, fs, "reading the FS's fields after the commit of c2")
	// ProbeTheirs fires no event on the machine, which stays InDoubt, and
	// leaves FS.doubtKey naming the refused attempt (ADR 0022 section 3).
	rk5CheckKnowledge(t, f, rootInDoubt, attemptKey != "", attemptKey, "after the commit of c2", where)

	if got := rk5Mode(t, fs, "f.mode.State() after the commit of c2"); got != mountDiverged {
		t.Errorf("after the commit of c2, f.mode.State() = %v, want %v: the read showed another writer's root "+
			"pointer (ADR 0022 section 3, probeTheirs; %s)", got, mountDiverged, where)
	}
	if err := rk5Create(t, ctx, fs, "c3"); !errors.Is(err, vfs.ErrStale) {
		t.Errorf("Create %q after the commit of c2 = %v, want vfs.ErrStale: once a mount has diverged every change "+
			"on it returns vfs.ErrStale (ADR 0003 section 4; %s)", "c3", err, where)
	}
	rk5CheckConflictRecord(t, logs, foreign, where)

	// M7: a diverged mount's commit stores nothing.
	if err := rk5Sync(t, ctx, fs, where+": a second Sync on the diverged mount"); err == nil {
		t.Errorf("%s: a second Sync on the diverged mount = nil, want an error (ADR 0014 section 11, M7)", where)
	}
	if writes := rk5Filter(h.records()[len(first):], rk5IsWrite); len(writes) != 0 {
		t.Errorf("since the first Sync the harness recorded the writes %v, want none: a diverged mount's commit is "+
			"refused before it stores anything (ADR 0014 section 11, M7; %s)", writes, where)
	}
}

// TestInDoubtMountSettlesTheRootBeforeItsNextCommit is K7 of ADR 0022 section
// 8, from CommitProtocol_ProbeFailed: a lost reply whose read fails leaves the
// root in doubt and the commit failed; the mount keeps accepting changes; and
// its next commit reads the root pointer before it writes anything, adopts
// the attempt, and commits at the adopted epoch plus one (ADR 0022 sections
// 3 and 4; Assumptions 6, 8 and 9).
//
// One commit behind it, New's; the lost reply and the failed read armed; a
// commit of c2: Sync returns an error whose text contains "in doubt";
// f.mode.State() is mountWritable; f.rootKnowledge.State() is rootInDoubt
// and FS.doubtKey the key the applied PutIfMatch wrote; FS.epoch and
// FS.rootETag are unchanged and FS.dirty is true. A Create of c3 returns nil.
// Sync returns nil, and the harness recorded since the failed read one
// GetWithETag of rootKey before any Put under snapshotPrefix, then one Put
// under snapshotPrefix whose key carries epoch 3, then one PutIfMatch of
// rootKey whose etag argument is the MD5 of the root pointer the lost reply
// wrote; f.rootKnowledge.State() is rootKnown, FS.doubtKey "" and FS.epoch 3.
// A remount has c2 and c3.
//
// The failed read is armed to take effect at the swap, so that it is the
// first read after it, whatever reads come before.
//
// At edcfc15 the second Sync diverges the mount.
func TestInDoubtMountSettlesTheRootBeforeItsNextCommit(t *testing.T) {
	const where = "ADR 0022 section 8, K7"
	h, ctx := rk5NewHarness(t, false)
	fs := rk5MustNew(t, ctx, where+": New over an empty bucket on the harness", rk5Config(h, nil))
	before := rk5Read(t, fs, "reading the FS's fields before the commit of c2")

	// c2: the swap is applied, its reply lost, and the read fails.
	mark := len(h.records())
	h.armLoseSwap()
	h.armFailReadAfterSwap()
	err := rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2")
	if err == nil || !strings.Contains(err.Error(), "in doubt") {
		t.Errorf("%s: Sync of the commit of c2, whose swap lost its reply and whose read failed, = %v, want an "+
			"error whose text contains %q (ADR 0022 section 4)", where, err, "in doubt")
	}
	first := h.records()
	calls := first[mark:]
	var lostData []byte
	if applied := rk5First(calls, func(c rk5Call) bool { return rk5IsRootSwap(c) && c.how == rk5ReplyLost }); applied < 0 {
		t.Errorf("the commit of c2 made no PutIfMatch of %q whose reply the harness lost (%s). Every call since "+
			"the commit began: %v", rootKey, where, calls)
	} else {
		lostData = calls[applied].data
	}
	if rk5First(calls, func(c rk5Call) bool { return rk5IsRootRead(c) && c.how == rk5Failed }) < 0 {
		t.Errorf("the commit of c2 made no GetWithETag of %q that the harness failed, want the one read after the "+
			"lost reply (ADR 0022 section 3; %s). Every call since the commit began: %v", rootKey, where, calls)
	}

	if got := rk5Mode(t, fs, "f.mode.State() after the commit of c2"); got != mountWritable {
		t.Errorf("after the commit of c2, f.mode.State() = %v, want %v: a read that fails decides nothing (ADR "+
			"0022 section 1; %s)", got, mountWritable, where)
	}
	f := rk5Read(t, fs, "reading the FS's fields after the commit of c2")
	rk5CheckKnowledge(t, f, rootInDoubt, lostData != nil, rk5Named(lostData), "after the commit of c2", where)
	if f.epoch != before.epoch || f.rootETag != before.rootETag {
		t.Errorf("after the commit of c2, FS.epoch and FS.rootETag = %d and %q, want %d and %q, as they were: "+
			"probeFailed changes no field (ADR 0022 section 3; %s)", f.epoch, f.rootETag, before.epoch,
			before.rootETag, where)
	}
	if !f.dirty {
		t.Errorf("after the commit of c2, FS.dirty is false, want true: the commit failed (ADR 0022 section 3; %s)",
			where)
	}

	// c3: doubt does not gate changes, and the next commit settles it first.
	if err := rk5Create(t, ctx, fs, "c3"); err != nil {
		t.Fatalf("Create %q on the mount in doubt = %v, want success: doubt does not gate changes (ADR 0022 "+
			"section 6; Assumption 8; %s)", "c3", err, where)
	}
	if err := rk5Sync(t, ctx, fs, where+": Sync after the Create of c3"); err != nil {
		t.Errorf("%s: Sync after the Create of c3 = %v, want nil: its read finds the root pointer naming the "+
			"attempt, adopts it, and the commit continues (ADR 0022 section 4, at the top)", where, err)
	}
	since := h.records()[len(first):]
	seq := rk5Filter(since, func(c rk5Call) bool { return rk5IsRootRead(c) || rk5IsSnapPut(c) || rk5IsRootSwap(c) })
	okSeq := len(seq) == 3 && rk5IsRootRead(seq[0]) && rk5IsSnapPut(seq[1]) && rk5IsRootSwap(seq[2])
	if okSeq {
		if e, eok := rk5KeyEpoch(seq[1].key); !eok || e != 3 {
			okSeq = false
		}
		if lostData != nil && seq[2].etag != rk5ETag(lostData) {
			okSeq = false
		}
	}
	if !okSeq {
		want := "(unknown: the lost reply was not recorded)"
		if lostData != nil {
			want = fmt.Sprintf("%q", rk5ETag(lostData))
		}
		t.Errorf("since the failed read the harness recorded the GetWithETag and PutIfMatch calls of %q and the "+
			"Puts under %q %v, want exactly: one GetWithETag of %q, then one Put under %q at epoch 3, then one "+
			"PutIfMatch of %q with the etag argument %s, the MD5 of the root pointer the lost reply wrote: an "+
			"in-doubt mount writes nothing until the read has settled the doubt (ADR 0022 section 3; %s)",
			rootKey, snapshotPrefix, seq, rootKey, snapshotPrefix, rootKey, want, where)
	}
	f = rk5Read(t, fs, "reading the FS's fields after the Sync of c3")
	rk5CheckKnowledge(t, f, rootKnown, true, "", "after the Sync of c3", where)
	if f.epoch != 3 {
		t.Errorf("after the Sync of c3, FS.epoch = %d, want 3: the adopted epoch, 2, plus one (ADR 0022 section 4; "+
			"%s)", f.epoch, where)
	}

	rk5CheckRemount(t, ctx, h.inner, []string{"c2", "c3"}, where)
}

// TestInDoubtMountDivergesWhenTheNextReadShowsAnotherWriter is K8 of
// ADR 0022 section 8: a mount left in doubt by a failed read, whose next read
// shows another writer's root pointer, diverges before it writes anything,
// and its log names that writer (ADR 0022 sections 3 and 4; Assumption 8).
//
// K7's setup through the failed read, with a capturing logger; a foreign root
// pointer written on the inner store; a Create of c3 returns nil; Sync returns
// an error whose text contains "commit conflict"; the harness recorded since
// the failed read exactly one GetWithETag of rootKey, no Put under
// snapshotPrefix and no PutIfMatch; f.mode.State() is mountDiverged; and the
// captured "commit conflict" record names "other-host/4242".
//
// At edcfc15 the second Sync puts a snapshot and swaps, and its swap's
// failure diverges the mount without a read.
func TestInDoubtMountDivergesWhenTheNextReadShowsAnotherWriter(t *testing.T) {
	const where = "ADR 0022 section 8, K8"
	h, ctx := rk5NewHarness(t, false)
	logs := &rk5Capture{}
	fs := rk5MustNew(t, ctx, where+": New over an empty bucket on the harness, with a capturing logger",
		rk5Config(h, logs.logger()))
	before := rk5Read(t, fs, "reading the FS's fields before the commit of c2")

	// c2: the swap is applied, its reply lost, and the read fails.
	mark := len(h.records())
	h.armLoseSwap()
	h.armFailReadAfterSwap()
	if err := rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2"); err == nil {
		t.Errorf("%s: Sync of the commit of c2, whose swap lost its reply and whose read failed, = nil, want an "+
			"error (ADR 0022 section 4)", where)
	}
	first := h.records()
	if rk5First(first[mark:], func(c rk5Call) bool { return rk5IsRootRead(c) && c.how == rk5Failed }) < 0 {
		t.Errorf("the commit of c2 made no GetWithETag of %q that the harness failed, want the one read after the "+
			"lost reply (ADR 0022 section 3; %s). Every call since the commit began: %v", rootKey, where,
			first[mark:])
	}

	// Another writer moves the root pointer.
	foreign, err := rk5WriteForeignRoot(ctx, h.inner, before.epoch+1, rk5Writer)
	if err != nil {
		t.Fatalf("setup: writing a foreign root pointer on the inner store: %v", err)
	}

	if err := rk5Create(t, ctx, fs, "c3"); err != nil {
		t.Fatalf("Create %q on the mount in doubt = %v, want success: doubt does not gate changes (ADR 0022 "+
			"section 6; %s)", "c3", err, where)
	}
	err = rk5Sync(t, ctx, fs, where+": Sync after the Create of c3")
	if err == nil || !strings.Contains(err.Error(), "commit conflict") {
		t.Errorf("%s: Sync after the Create of c3, with another writer's root pointer in the bucket, = %v, want an "+
			"error whose text contains %q (ADR 0022 section 4, at the top)", where, err, "commit conflict")
	}
	since := h.records()[len(first):]
	reads := rk5Filter(since, rk5IsRootRead)
	snapPuts := rk5Filter(since, rk5IsSnapPut)
	swaps := rk5Filter(since, func(c rk5Call) bool { return c.op == "PutIfMatch" })
	if len(reads) != 1 || len(snapPuts) != 0 || len(swaps) != 0 {
		t.Errorf("since the failed read the harness recorded the GetWithETag calls of %q %v, the Puts under %q %v "+
			"and the PutIfMatch calls %v, want exactly one GetWithETag and no Put under %q and no PutIfMatch: an "+
			"in-doubt mount writes nothing until the doubt is settled (ADR 0022 section 3; %s)", rootKey, reads,
			snapshotPrefix, snapPuts, swaps, snapshotPrefix, where)
	}
	if got := rk5Mode(t, fs, "f.mode.State() after the Sync of c3"); got != mountDiverged {
		t.Errorf("after the Sync of c3, f.mode.State() = %v, want %v: the read showed another writer's root "+
			"pointer (ADR 0022 section 3, probeTheirs; %s)", got, mountDiverged, where)
	}
	rk5CheckConflictRecord(t, logs, foreign, where)
}

// TestSwapReplyWithoutETagIsSettledByOneRead is K9 of ADR 0022 section 8, from
// CommitProtocol_SwapNoETag (#103; route 5): a swap applied and answered with
// no ETag leaves the root pointer's ETag unknown, so one read supplies it,
// and the next swap is conditioned on the root pointer's ETag rather than
// sent as a create-only write (ADR 0022 sections 1, 4 and 5; Assumption 10).
//
// One commit behind it, New's; the dropped ETag armed; a commit of c2: Sync
// returns nil; the harness recorded exactly one GetWithETag of rootKey after
// the PutIfMatch; f.rootKnowledge.State() is rootKnown and FS.doubtKey "";
// FS.rootETag is the MD5 of the root pointer's bytes. A commit
// of c3: Sync returns nil, its PutIfMatch passed through with that MD5 as its
// etag argument. A remount has c2 and c3.
//
// At edcfc15 the second Sync sends an empty etag and diverges the mount.
func TestSwapReplyWithoutETagIsSettledByOneRead(t *testing.T) {
	const where = "ADR 0022 section 8, K9"
	h, ctx := rk5NewHarness(t, false)
	fs := rk5MustNew(t, ctx, where+": New over an empty bucket on the harness", rk5Config(h, nil))

	// c2: the swap is applied, and its reply carries no ETag.
	mark := len(h.records())
	h.armDropSwapETag()
	if err := rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2"); err != nil {
		t.Errorf("%s: Sync of the commit of c2, whose swap was applied and answered with no ETag, = %v, want nil: "+
			"the read shows the root pointer naming the attempt (ADR 0022 section 4)", where, err)
	}
	all := h.records()
	calls := all[mark:]
	if dropped := rk5First(calls, func(c rk5Call) bool { return rk5IsRootSwap(c) && c.how == rk5ETagDropped }); dropped < 0 {
		t.Errorf("the commit of c2 made no PutIfMatch of %q whose ETag the harness dropped (%s). Every call since "+
			"the commit began: %v", rootKey, where, calls)
	} else if reads := rk5Filter(calls[dropped+1:], rk5IsRootRead); len(reads) != 1 {
		t.Errorf("after the PutIfMatch whose reply carried no ETag the harness recorded the GetWithETag calls of "+
			"%q %v, want exactly one: a reply that did not establish the ETag leaves the root in doubt, and one "+
			"read settles it (ADR 0022 section 1, section 4; %s)", rootKey, reads, where)
	}
	f := rk5Read(t, fs, "reading the FS's fields after the commit of c2")
	rk5CheckKnowledge(t, f, rootKnown, true, "", "after the commit of c2", where)

	// Through the inner store, after the records were read.
	raw, _, ok := rk5Root(t, ctx, h.inner, where)
	tag := ""
	if ok {
		tag = rk5ETag(raw)
		if f.rootETag != tag {
			t.Errorf("after the commit of c2, FS.rootETag = %q, want %q, the MD5 of the root pointer's bytes on "+
				"the inner store: the read supplies the ETag the reply withheld (ADR 0022 section 3; %s)",
				f.rootETag, tag, where)
		}
	}

	// c3: a conditional swap against that ETag.
	mark = len(all)
	if err := rk5Commit(t, ctx, fs, "c3", where+": Sync of the commit of c3"); err != nil {
		t.Errorf("%s: Sync of the commit of c3 = %v, want nil: its swap is conditioned on the root pointer's "+
			"ETag (ADR 0022 section 5, #103)", where, err)
	}
	swaps := rk5Filter(h.records()[mark:], rk5IsRootSwap)
	if len(swaps) != 1 || swaps[0].how != rk5Passed || swaps[0].err != nil || (ok && swaps[0].etag != tag) {
		t.Errorf("the commit of c3 made the PutIfMatch calls of %q %v, want exactly one, passed through and "+
			"applied, with the etag argument %q (%s)", rootKey, swaps, tag, where)
	}

	rk5CheckRemount(t, ctx, h.inner, []string{"c2", "c3"}, where)
}

// TestSwapReplyWithoutETagAndAFailedReadRetriesTheCommit is K10 of ADR 0022
// section 8: a swap applied and answered with no ETag whose read fails fails
// the commit and leaves the namespace dirty and the root in doubt, although
// the commit is durable; a Sync without a change then reads the root pointer
// before it writes, and commits again (ADR 0022 section 4; Assumption 10).
//
// K9's setup with the failed read armed too: the first Sync returns an error
// whose text contains "no ETag"; f.rootKnowledge.State() is rootInDoubt and
// FS.doubtKey the attempt's key; FS.dirty is true; the root pointer on the
// inner store names the attempt's key. Sync again, without a change: it
// returns nil; the harness recorded a GetWithETag of rootKey before a Put
// under snapshotPrefix whose key carries epoch 3; and a remount has c2.
//
// At edcfc15 the first Sync returns nil and clears FS.dirty, so the second
// makes no call.
func TestSwapReplyWithoutETagAndAFailedReadRetriesTheCommit(t *testing.T) {
	const where = "ADR 0022 section 8, K10"
	h, ctx := rk5NewHarness(t, false)
	fs := rk5MustNew(t, ctx, where+": New over an empty bucket on the harness", rk5Config(h, nil))

	// c2: the swap is applied with no ETag, and the read fails.
	mark := len(h.records())
	h.armDropSwapETag()
	h.armFailReadAfterSwap()
	err := rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2")
	if err == nil || !strings.Contains(err.Error(), "no ETag") {
		t.Errorf("%s: Sync of the commit of c2, whose swap reply carried no ETag and whose read failed, = %v, want "+
			"an error whose text contains %q (ADR 0022 section 4)", where, err, "no ETag")
	}
	first := h.records()
	calls := first[mark:]
	attemptKey := ""
	if dropped := rk5First(calls, func(c rk5Call) bool { return rk5IsRootSwap(c) && c.how == rk5ETagDropped }); dropped < 0 {
		t.Errorf("the commit of c2 made no PutIfMatch of %q whose ETag the harness dropped (%s). Every call since "+
			"the commit began: %v", rootKey, where, calls)
	} else {
		attemptKey = rk5Named(calls[dropped].data)
	}
	f := rk5Read(t, fs, "reading the FS's fields after the commit of c2")
	rk5CheckKnowledge(t, f, rootInDoubt, attemptKey != "", attemptKey, "after the commit of c2", where)
	if !f.dirty {
		t.Errorf("after the commit of c2, FS.dirty is false, want true: the commit failed, so the next commit "+
			"resolves the doubt first (ADR 0022 section 4; Assumption 10; %s)", where)
	}

	// Through the inner store, after the records were read.
	if _, rp, ok := rk5Root(t, ctx, h.inner, where); ok && attemptKey != "" && rp.Snapshot != attemptKey {
		t.Errorf("the root pointer on the inner store names %q, want %q, the attempt's key: the swap was applied "+
			"(%s)", rp.Snapshot, attemptKey, where)
	}

	// Sync again, without a change.
	if err := rk5Sync(t, ctx, fs, where+": Sync again, without a change"); err != nil {
		t.Errorf("%s: Sync again, without a change, = %v, want nil: its read adopts the attempt and the commit "+
			"writes the namespace again (ADR 0022 section 4; Assumption 10)", where, err)
	}
	since := h.records()[len(first):]
	read := rk5First(since, rk5IsRootRead)
	snapPuts := rk5Filter(since, rk5IsSnapPut)
	put := rk5First(since, rk5IsSnapPut)
	okSeq := read >= 0 && put >= 0 && read < put && len(snapPuts) == 1
	if okSeq {
		if e, eok := rk5KeyEpoch(snapPuts[0].key); !eok || e != 3 {
			okSeq = false
		}
	}
	if !okSeq {
		t.Errorf("the second Sync made the calls %v, want a GetWithETag of %q, then exactly one Put under %q, at "+
			"epoch 3: the read comes before the snapshot Put (ADR 0022 section 3; %s)", since, rootKey,
			snapshotPrefix, where)
	}

	rk5CheckRemount(t, ctx, h.inner, []string{"c2"}, where)
}

// TestLostFallbackReplyIsAdopted is K11 of ADR 0022 section 8, from
// CommitProtocol_SelfDivergeFallback (route 3): with the operator's opt-in, an
// unconditional Put of the root pointer that took effect and returned an
// error is settled by one read, which names the attempt, and the mount adopts
// it (ADR 0022 section 4; Assumption 12).
//
// The harness in unconditional mode; New with AllowUnconditionalRoot over an
// empty bucket, so the init commit goes through the fallback; the lost root
// Put armed; a commit of c2: Sync returns nil; the harness recorded the Put of
// rootKey it applied, no Head of rootKey after it, and exactly one GetWithETag
// of rootKey; f.rootKnowledge.State() is rootKnown, FS.doubtKey "",
// FS.rootSnapshot the key the Put named, and FS.epoch 2. A commit of c3: Sync returns nil, through one Put
// and one Head of rootKey. A remount has c2 and c3.
//
// At edcfc15 the first Sync returns "write root pointer: ..." and FS.epoch
// stays 1. The second commit then succeeds there through the fallback, since
// an unconditional Put has no condition to fail, so the mount does not
// diverge in this test at edcfc15.
func TestLostFallbackReplyIsAdopted(t *testing.T) {
	const where = "ADR 0022 section 8, K11"
	h, ctx := rk5NewHarness(t, true)
	cfg := rk5Config(h, nil)
	cfg.AllowUnconditionalRoot = true
	fs := rk5MustNew(t, ctx, where+": New with AllowUnconditionalRoot over an empty bucket, on a store that "+
		"answers every PutIfMatch store.ErrUnsupported", cfg)
	before := rk5Read(t, fs, "reading the FS's fields before the commit of c2")

	// c2: the fallback Put is applied and its reply lost.
	mark := len(h.records())
	h.armLoseRootPut()
	if err := rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2"); err != nil {
		t.Errorf("%s: Sync of the commit of c2, whose fallback Put of the root pointer took effect and lost its "+
			"reply, = %v, want nil: the read shows the root pointer naming the attempt (ADR 0022 section 4; "+
			"Assumption 12)", where, err)
	}
	all := h.records()
	calls := all[mark:]
	put := rk5First(calls, func(c rk5Call) bool { return rk5IsRootPut(c) && c.how == rk5ReplyLost })
	putKey := ""
	if put < 0 {
		t.Errorf("the commit of c2 made no Put of %q whose reply the harness lost (%s). Every call since the "+
			"commit began: %v", rootKey, where, calls)
	} else {
		putKey = rk5Named(calls[put].data)
		after := calls[put+1:]
		if heads := rk5Filter(after, rk5IsRootHead); len(heads) != 0 {
			t.Errorf("after the Put whose reply was lost the harness recorded the Heads of %q %v, want none (ADR "+
				"0022 section 4; %s)", rootKey, heads, where)
		}
		if reads := rk5Filter(after, rk5IsRootRead); len(reads) != 1 {
			t.Errorf("after the Put whose reply was lost the harness recorded the GetWithETag calls of %q %v, "+
				"want exactly one: S3.Put is retried, so the Put may have taken effect, and one read settles it "+
				"(ADR 0022 section 4; Assumption 12; %s)", rootKey, reads, where)
		}
	}
	f := rk5Read(t, fs, "reading the FS's fields after the commit of c2")
	rk5CheckKnowledge(t, f, rootKnown, true, "", "after the commit of c2", where)
	if putKey != "" && f.rootSnapshot != putKey {
		t.Errorf("after the commit of c2, FS.rootSnapshot = %q, want %q, the key the lost Put named: the attempt "+
			"is adopted (ADR 0022 section 3, probeOurs; %s)", f.rootSnapshot, putKey, where)
	}
	if f.epoch != before.epoch+1 {
		t.Errorf("after the commit of c2, FS.epoch = %d, want %d: the adopted attempt's epoch (ADR 0022 section "+
			"3, probeOurs; %s)", f.epoch, before.epoch+1, where)
	}

	// c3: the fallback again, this time answered.
	mark = len(all)
	if err := rk5Commit(t, ctx, fs, "c3", where+": Sync of the commit of c3"); err != nil {
		t.Errorf("%s: Sync of the commit of c3 = %v, want nil through the opted-in fallback (ADR 0021 section 2)",
			where, err)
	}
	since := h.records()[mark:]
	puts := rk5Filter(since, rk5IsRootPut)
	heads := rk5Filter(since, rk5IsRootHead)
	if len(puts) != 1 || puts[0].how != rk5Passed || puts[0].err != nil || len(heads) != 1 {
		t.Errorf("the commit of c3 made the Puts of %q %v and the Heads %v, want exactly one of each, passed "+
			"through: the opted-in fallback (ADR 0021 section 2; %s)", rootKey, puts, heads, where)
	}

	rk5CheckRemount(t, ctx, h.inner, []string{"c2", "c3"}, where)
}

// TestA501ThatTookEffectIsAdopted is K12 of ADR 0022 section 8 (ADR 0021
// section 2 step 3; the issue's comment of 2026-10-27; route 4): a swap the
// store applied and answered store.ErrUnsupported, on a mount without the
// opt-in, is found by the reclaim's read naming the attempt; no Delete is
// made, and the doubt it raises is settled by one read, which adopts the
// attempt, so Sync returns nil and the next swap is made against the
// adopted root pointer (ADR 0022 section 4; Assumption 11). G3 in
// internal/blobfs/swap_fallback_test.go
// (TestRefusedCommitKeepsASnapshotTheRootPointerNames) asserts the same Sync
// result.
//
// c1 seeded on the inner store; New on the harness, not in its unconditional
// mode, without the opt-in; apply-then-501 armed; a commit of c2: Sync returns
// nil; the harness recorded no Delete, exactly one Get of rootKey (the
// reclaim's read) and then exactly one GetWithETag of rootKey (the probe);
// the root pointer on the inner store names the key of the last Put under
// snapshotPrefix, which exists; f.rootKnowledge.State() is rootKnown and
// FS.doubtKey ""; FS.epoch is the loaded epoch plus one. A
// commit of c3, with the store honouring conditions: Sync returns nil, its
// PutIfMatch passed through and applied. A remount has c1, c2 and c3.
//
// At edcfc15 the first Sync returns ADR 0021's error and the second diverges
// the mount.
func TestA501ThatTookEffectIsAdopted(t *testing.T) {
	const where = "ADR 0022 section 8, K12"
	h, ctx := rk5NewHarness(t, false)
	seed := rk5MustNew(t, ctx, "setup: New on the inner store over an empty bucket", rk5Config(h.inner, nil))
	if err := rk5Commit(t, ctx, seed, "c1", "setup: Sync of the seeding commit of c1"); err != nil {
		t.Fatalf("setup: Sync of the seeding commit of c1 on the inner store = %v, want success", err)
	}

	fs := rk5MustNew(t, ctx, where+": New, without the opt-in, on the harness over the seeded bucket",
		rk5Config(h, nil))
	before := rk5Read(t, fs, "reading the FS's fields before the commit of c2")

	// c2: the store applies the swap and answers 501.
	mark := len(h.records())
	h.armApplyThen501()
	if err := rk5Commit(t, ctx, fs, "c2", where+": Sync of the commit of c2"); err != nil {
		t.Errorf("%s: Sync of the commit of c2, whose swap the store applied and answered store.ErrUnsupported, = "+
			"%v, want nil: a 501 that took effect is adopted in the same call (ADR 0022 section 4; Assumption "+
			"11)", where, err)
	}
	all := h.records()
	calls := all[mark:]
	if dels := rk5Filter(calls, rk5IsDelete); len(dels) != 0 {
		t.Errorf("the commit of c2 made the Deletes %v, want none: the root pointer names the attempt's snapshot "+
			"(ADR 0021 section 2 step 3; %s)", dels, where)
	}
	gets := rk5Filter(calls, rk5IsRootGet)
	reads := rk5Filter(calls, rk5IsRootRead)
	if len(gets) != 1 || len(reads) != 1 || rk5First(calls, rk5IsRootGet) > rk5First(calls, rk5IsRootRead) {
		t.Errorf("the commit of c2 made the Gets of %q %v and the GetWithETag calls %v, want exactly one Get, the "+
			"reclaim's read, and then exactly one GetWithETag, the probe (ADR 0022 section 4; Assumption 11; %s). "+
			"Every call since the commit began: %v", rootKey, gets, reads, where, calls)
	}
	snapPuts := rk5Filter(calls, rk5IsSnapPut)
	last := ""
	if len(snapPuts) == 0 {
		t.Errorf("the commit of c2 made no Put under %q (%s)", snapshotPrefix, where)
	} else {
		last = snapPuts[len(snapPuts)-1].key
	}
	f := rk5Read(t, fs, "reading the FS's fields after the commit of c2")
	rk5CheckKnowledge(t, f, rootKnown, true, "", "after the commit of c2", where)
	if f.epoch != before.epoch+1 {
		t.Errorf("after the commit of c2, FS.epoch = %d, want %d, the loaded epoch plus one: the attempt is "+
			"adopted (ADR 0022 section 3, probeOurs; %s)", f.epoch, before.epoch+1, where)
	}

	// Through the inner store, after the records were read.
	if _, rp, ok := rk5Root(t, ctx, h.inner, where); ok && last != "" {
		if rp.Snapshot != last {
			t.Errorf("the root pointer on the inner store names %q, want %q, the key of the last Put under %q "+
				"(%s)", rp.Snapshot, last, snapshotPrefix, where)
		}
		if _, err := h.inner.Head(ctx, last); err != nil {
			t.Errorf("Head(%q) on the inner store = %v, want the snapshot the root pointer names to exist (%s)",
				last, err, where)
		}
	}

	// c3, with the store honouring conditions.
	mark = len(all)
	if err := rk5Commit(t, ctx, fs, "c3", where+": Sync of the commit of c3"); err != nil {
		t.Errorf("%s: Sync of the commit of c3 = %v, want nil: its swap is conditioned on the adopted root "+
			"pointer's ETag (ADR 0022 section 3)", where, err)
	}
	swaps := rk5Filter(h.records()[mark:], rk5IsRootSwap)
	if len(swaps) != 1 || swaps[0].how != rk5Passed || swaps[0].err != nil {
		t.Errorf("the commit of c3 made the PutIfMatch calls of %q %v, want exactly one, passed through and "+
			"applied (%s)", rootKey, swaps, where)
	}

	rk5CheckRemount(t, ctx, h.inner, []string{"c1", "c2", "c3"}, where)
}

// TestLostReplyOnTheFirstCommitIsAdoptedByNew is K13 of ADR 0022 section 8:
// the same paths run from initEmpty before the FS is shared. A lost reply on
// the create is adopted and New succeeds; a lost reply whose read fails makes
// New return a nil *FS and an error, as any commit error does, and does not
// panic (ADR 0022 section 4, from initEmpty; ADR 0011 section 7).
//
// Over an empty bucket, the lost reply armed before New: New returns a
// non-nil *FS and a nil error; the bucket's root pointer names the one key
// under snapshotPrefix; f.rootKnowledge.State() is rootKnown, FS.doubtKey ""
// and FS.rootSnapshot that key; FS.epoch is 1. In a second subtest, the lost reply and
// the failed read armed: New returns a nil *FS and a non-nil error. The
// failed read is armed to take effect at the swap, since New's own load reads
// the root pointer with GetWithETag first.
//
// At edcfc15 the first subtest's New returns an error.
func TestLostReplyOnTheFirstCommitIsAdoptedByNew(t *testing.T) {
	t.Run("lost reply", func(t *testing.T) {
		const where = "ADR 0022 section 8, K13, the lost reply"
		h, ctx := rk5NewHarness(t, false)
		h.armLoseSwap()
		what := "New over an empty bucket whose first swap is applied and loses its reply"
		r := rk5TryNew(t, ctx, what, rk5Config(h, nil))
		if r.err != nil {
			t.Fatalf("%s = %v, want success: initEmpty's read finds the root pointer naming its attempt and "+
				"adopts it (ADR 0022 section 4, from initEmpty; %s). Any *FS returned is not used", what, r.err,
				where)
		}
		if r.fs == nil {
			t.Fatalf("%s returned a nil *FS and a nil error (%s)", what, where)
		}
		calls := h.records()
		if rk5First(calls, func(c rk5Call) bool { return rk5IsRootSwap(c) && c.how == rk5ReplyLost }) < 0 {
			t.Errorf("%s made no PutIfMatch of %q whose reply the harness lost (%s). Every call: %v", what, rootKey,
				where, calls)
		}
		f := rk5Read(t, r.fs, "reading the FS's fields after New")
		rk5CheckKnowledge(t, f, rootKnown, true, "", "after "+what, where)
		if f.epoch != 1 {
			t.Errorf("after %s, FS.epoch = %d, want 1 (%s)", what, f.epoch, where)
		}

		// Through the inner store, after the records were read.
		infos, err := h.inner.List(ctx, snapshotPrefix, "", 0)
		if err != nil {
			t.Fatalf("List(%q) on the inner store = %v (%s)", snapshotPrefix, err, where)
		}
		if _, rp, ok := rk5Root(t, ctx, h.inner, where); ok {
			if len(infos) != 1 || rp.Snapshot != infos[0].Key {
				t.Errorf("the inner store holds %d key(s) under %q, %v, and the root pointer names %q, want one "+
					"key, which the root pointer names (%s)", len(infos), snapshotPrefix, infos, rp.Snapshot, where)
			}
			if f.rootSnapshot != rp.Snapshot {
				t.Errorf("after %s, FS.rootSnapshot = %q, want %q, the key the root pointer names: the attempt is "+
					"adopted (ADR 0022 section 3, probeOurs; %s)", what, f.rootSnapshot, rp.Snapshot, where)
			}
		}
	})

	t.Run("lost reply and a failed read", func(t *testing.T) {
		const where = "ADR 0022 section 8, K13, the lost reply and the failed read"
		h, ctx := rk5NewHarness(t, false)
		h.armLoseSwap()
		h.armFailReadAfterSwap()
		what := "New over an empty bucket whose first swap loses its reply and whose read then fails"
		r := rk5TryNew(t, ctx, what, rk5Config(h, nil))
		if r.fs != nil {
			t.Errorf("%s returned a non-nil *FS, want nil: a verdict that fails the commit makes New return its "+
				"error and a nil *FS (ADR 0022 section 4, from initEmpty; ADR 0011 section 7; %s). The FS is not "+
				"used", what, where)
		}
		if r.err == nil {
			t.Errorf("%s returned a nil error, want a non-nil one (%s)", what, where)
		}
	})
}
