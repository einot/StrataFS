package blobfs

// Clean-room tests for ADR 0014 section 7, the mount mode machine, as revised
// on 2026-10-27 against 37abe5d, written without reading the implementation,
// from ADR 0014
// (docs/adr/0014-state-machines-are-tables-and-permission-checks-are-policies.md)
// sections 2, 3, 7, 9 and 11 (the blobfs block M1 to M9 and its harness
// paragraph), docs/vocabulary.md V1, ADR 0011 sections 2, 4 and 7, ADR 0003
// section 1, ADR 0021 sections 2 and 6, ADR 0009 section 5, ADR 0022
// (docs/adr/0022-a-failed-root-swap-is-settled-by-one-read-of-the-root-pointer.md)
// sections 2, 3, 5 and 8, and internal/vfs/vfs.go.
//
// The machine (ADR 0014 section 7): the mount mode is FS.mode, an
// *fsm.Atomic[mountMode, mountEvent] over the table mountModes, named "mode",
// whose one edge is Writable to Diverged on Diverge. New makes it ReadOnly
// when Config.ReadOnly is set and Writable otherwise, before loadOrInit.
// Since ADR 0022 its one Fire is made by resolveRoot, which only commitLocked
// calls, when one read of the root pointer after a failed write of it shows
// another writer's root pointer (ProbeTheirs; ADR 0022 sections 2 and 3); a
// swap's error alone, store.ErrPrecondition included, no longer fires it.
// ADR 0021 section 2's refusal of a swap answered store.ErrUnsupported fires
// nothing while its read shows another root pointer. mutable() is nil exactly
// when the mode is Writable.
//
// Covered:
//   - M1: TestNewSetsTheMountModeFromConfigReadOnly.
//   - M2: TestMountModesTableHasOnlyTheDivergeEdge.
//   - M3, writable half, and M7:
//     TestASwapLostToAnotherWriterDivergesTheMountAndItStoresNothingMore.
//   - M3, read-only half: TestReadOnlyMountStaysReadOnlyThroughWriteAndSync.
//   - M5: TestMountModeAndEventStrings.
//   - M6: TestRefusedUnconditionalSwapLeavesTheMountWritable.
//   - M8: TestNewWhoseFirstSwapLosesFailsWithoutPanicking.
//   - M9: TestMutableAnswersForEveryMountMode.
//
// Elsewhere: M4 is TestWriteRacingADivergingCommitIsNotADataRace in
// internal/blobfs/diverged_race_test.go, whose fake now writes a foreign root
// pointer before it refuses the swap (ADR 0022 section 8).
//
// The tests that diverge a mount (M3, M7) do so by a real second writer: a
// foreign root pointer written on the inner store, as ADR 0022 section 8's
// facts describe, before the swap the fake refuses, so that the read that
// follows the refusal finds another writer (ADR 0022 section 5).
//
// Not covered, on purpose: what ADR 0014 section 11 and ADR 0022 section 8
// leave to review (that FS.readOnly and FS.diverged are gone, that
// resolveRoot's Fire is the only writer, that a failed Fire is logged, where
// the mode is read, the field's
// doc comment, the TLA+ and trace.tsv changes); every log record; what the
// bucket holds after M8's refused New; and the Get and Delete of ADR 0021
// section 2's reclaim in M6, which the fake passes through and nothing here
// pins.
//
// The safety rules, ADR 0011 section 7 and ADR 0014 section 11's harness;
// every test here follows them:
//   - every bucket is a *store.Local under the test's t.TempDir(), behind
//     mm14Store where a test needs a fake;
//   - every Config has SnapshotRetention -1, ChunkSize 4096, a discarding
//     logger, a non-zero OwnerUID and OwnerGID (ADR 0010), and MaxDirtyBytes
//     -1, which disables the write budget's wait (ADR 0003 section 1), so no
//     Write starts a commit of its own;
//   - every New and every FS call, f.mode.State() included, runs in mm14Go,
//     which recovers a panic, and is waited for under mm14Bound; goroutines
//     never touch t, and nothing here is parallel;
//   - an *FS that New returned where it should have refused is never used;
//   - nothing calls Run, and there is no time.Sleep.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"strata/internal/fsm"
	"strata/internal/store"
	"strata/internal/vfs"
)

// mm14Bound is how long a test waits for any one step before it reports the
// step as hung (ADR 0011 section 7).
const mm14Bound = 10 * time.Second

// mm14Op is one write call the fake saw: Put, PutIfMatch or Delete, with its
// key. injected marks the armed PutIfMatch of rootKey that the fake answered
// with its chosen error without calling the inner store.
type mm14Op struct {
	op       string
	key      string
	injected bool
}

// String describes the call for failure messages.
func (o mm14Op) String() string {
	if o.injected {
		return fmt.Sprintf("%s(%q), failed by the fake", o.op, o.key)
	}
	return fmt.Sprintf("%s(%q)", o.op, o.key)
}

// mm14Store is a store.Store over a *store.Local that passes every call
// through, and records every Put, PutIfMatch and Delete, in call order, with
// its key. Once armed with an error, its next PutIfMatch of rootKey returns ""
// and that error without calling the inner store, and disarms it. It does not
// embed store.Store, so that every method it has is one written here. mu
// guards armErr and calls.
type mm14Store struct {
	inner *store.Local

	mu     sync.Mutex
	armErr error
	calls  []mm14Op
}

var _ store.Store = (*mm14Store)(nil)

// arm makes the next PutIfMatch of rootKey return "" and err.
func (s *mm14Store) arm(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armErr = err
}

// ops returns a copy of the recorded write calls, in call order.
func (s *mm14Store) ops() []mm14Op {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mm14Op(nil), s.calls...)
}

func (s *mm14Store) record(op, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, mm14Op{op: op, key: key})
}

func (s *mm14Store) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *mm14Store) GetWithETag(ctx context.Context, key string) ([]byte, string, error) {
	return s.inner.GetWithETag(ctx, key)
}

func (s *mm14Store) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	return s.inner.GetRange(ctx, key, off, n)
}

func (s *mm14Store) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	return s.inner.Head(ctx, key)
}

func (s *mm14Store) Put(ctx context.Context, key string, data []byte) error {
	s.record("Put", key)
	return s.inner.Put(ctx, key, data)
}

func (s *mm14Store) Delete(ctx context.Context, key string) error {
	s.record("Delete", key)
	return s.inner.Delete(ctx, key)
}

func (s *mm14Store) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	return s.inner.List(ctx, prefix, after, max)
}

func (s *mm14Store) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) {
	s.mu.Lock()
	inject := key == rootKey && s.armErr != nil
	var injected error
	if inject {
		injected = s.armErr
		s.armErr = nil
	}
	s.calls = append(s.calls, mm14Op{op: "PutIfMatch", key: key, injected: inject})
	s.mu.Unlock()
	if inject {
		return "", injected
	}
	return s.inner.PutIfMatch(ctx, key, data, etag)
}

func (s *mm14Store) Name() string {
	return s.inner.Name()
}

// mm14Injected returns the index in ops of the PutIfMatch the fake failed, or
// -1 if it failed none.
func mm14Injected(ops []mm14Op) int {
	for i, o := range ops {
		if o.injected {
			return i
		}
	}
	return -1
}

// mm14Call is one call running in its own goroutine. val, returned, panicked
// and pval may be read only once done is closed.
type mm14Call[T any] struct {
	done     chan struct{}
	val      T
	returned bool
	panicked bool
	pval     any
}

// mm14Go runs fn in its own goroutine, recovering and recording a panic out of
// it. fn must not touch t: after a timeout its goroutine is abandoned.
func mm14Go[T any](fn func() T) *mm14Call[T] {
	c := &mm14Call[T]{done: make(chan struct{})}
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

// wait waits for the call under mm14Bound and returns its value. A hang, a
// panic or a runtime.Goexit fails the test.
func (c *mm14Call[T]) wait(t *testing.T, what string) T {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(mm14Bound):
		t.Fatalf("%s has not returned after %v, so it is hung; want it to return (ADR 0011 section 7)",
			what, mm14Bound)
	}
	if c.panicked {
		t.Fatalf("%s panicked with %v, want it to return (ADR 0011 section 7; ADR 0014 section 3: an "+
			"illegal transition is an error, never a panic)", what, c.pval)
	}
	if !c.returned {
		t.Fatalf("%s ended without returning (runtime.Goexit), want it to return (ADR 0011 section 7)", what)
	}
	return c.val
}

// mm14Local makes a local bucket under the test's temporary directory.
func mm14Local(t *testing.T) *store.Local {
	t.Helper()
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	return local
}

// mm14Config is every mount's Config here (ADR 0011 section 7, ADR 0014
// section 11, ADR 0003 section 1, ADR 0010).
func mm14Config(st store.Store, readOnly bool) Config {
	return Config{
		Store:             st,
		ChunkSize:         4096,
		OwnerUID:          501,
		OwnerGID:          20,
		ReadOnly:          readOnly,
		SnapshotRetention: -1,
		MaxDirtyBytes:     -1,
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// mm14Owner is the root directory's owner under cfg (ADR 0010).
func mm14Owner(cfg Config) vfs.Caller {
	return vfs.Caller{UID: cfg.OwnerUID, GID: cfg.OwnerGID}
}

// mm14NewResult is what one New returned.
type mm14NewResult struct {
	fs  *FS
	err error
}

// mm14TryNew runs New in mm14Go and waits for it.
func mm14TryNew(t *testing.T, ctx context.Context, what string, cfg Config) mm14NewResult {
	t.Helper()
	return mm14Go(func() mm14NewResult {
		fs, err := New(ctx, cfg)
		return mm14NewResult{fs, err}
	}).wait(t, what)
}

// mm14MustNew is mm14TryNew for a New that must succeed.
func mm14MustNew(t *testing.T, ctx context.Context, what string, cfg Config) *FS {
	t.Helper()
	r := mm14TryNew(t, ctx, what, cfg)
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	if r.fs == nil {
		t.Fatalf("%s returned a nil *FS and a nil error, want a non-nil *FS", what)
	}
	return r.fs
}

// mm14Create creates a regular file name in fs's root directory as c,
// exclusively, and returns its handle and Create's error.
func mm14Create(t *testing.T, ctx context.Context, fs *FS, c vfs.Caller, name string) (vfs.Handle, error) {
	t.Helper()
	type res struct {
		h   vfs.Handle
		err error
	}
	r := mm14Go(func() res {
		h, _, err := fs.Create(ctx, c, fs.Root(), name, vfs.SetAttr{}, true)
		return res{h, err}
	}).wait(t, fmt.Sprintf("Create %q in the root directory", name))
	return r.h, r.err
}

// mm14WriteResult is what one Write returned.
type mm14WriteResult struct {
	n   uint32
	how vfs.Stability
	err error
}

// mm14Write writes one byte at offset 0 of h, UNSTABLE, as c.
func mm14Write(t *testing.T, ctx context.Context, fs *FS, c vfs.Caller, h vfs.Handle, what string) mm14WriteResult {
	t.Helper()
	return mm14Go(func() mm14WriteResult {
		n, how, err := fs.Write(ctx, c, h, 0, []byte{'x'}, vfs.Unstable)
		return mm14WriteResult{n, how, err}
	}).wait(t, what)
}

// mm14Sync runs fs.Sync and returns what it returned.
func mm14Sync(t *testing.T, ctx context.Context, fs *FS, what string) error {
	t.Helper()
	return mm14Go(func() error { return fs.Sync(ctx) }).wait(t, what)
}

// mm14Mode returns fs.mode.State().
func mm14Mode(t *testing.T, fs *FS, what string) mountMode {
	t.Helper()
	return mm14Go(func() mountMode { return fs.mode.State() }).wait(t, what)
}

// mm14Seed puts a filesystem into the empty bucket local, through a writable
// New on it, holding a regular file named "a" in the root directory,
// committed by a Sync. The mount is not used again.
func mm14Seed(t *testing.T, ctx context.Context, local *store.Local) {
	t.Helper()
	cfg := mm14Config(local, false)
	fs := mm14MustNew(t, ctx, "setup: New, writable, over an empty bucket", cfg)
	if _, err := mm14Create(t, ctx, fs, mm14Owner(cfg), "a"); err != nil {
		t.Fatalf("setup: Create %q in the root directory as its owner = %v, want success (ADR 0010)", "a", err)
	}
	if err := mm14Sync(t, ctx, fs, "setup: Sync of the seeded filesystem"); err != nil {
		t.Fatalf("setup: Sync of the seeded filesystem = %v, want success", err)
	}
}

// TestNewSetsTheMountModeFromConfigReadOnly is M1 of ADR 0014 section 11: New
// makes f.mode with the initial state mountReadOnly when Config.ReadOnly is
// set, on a bucket that holds a filesystem (ADR 0011 section 2), and
// mountWritable otherwise, on an empty bucket and on one that holds a
// filesystem (ADR 0014 section 7).
func TestNewSetsTheMountModeFromConfigReadOnly(t *testing.T) {
	cases := []struct {
		name     string
		seed     bool
		readOnly bool
		want     mountMode
	}{
		{"read-only, a bucket that holds a filesystem", true, true, mountReadOnly},
		{"writable, an empty bucket", false, false, mountWritable},
		{"writable, a bucket that holds a filesystem", true, false, mountWritable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			local := mm14Local(t)
			if tc.seed {
				mm14Seed(t, ctx, local)
			}
			what := "New, " + tc.name
			fs := mm14MustNew(t, ctx, what, mm14Config(local, tc.readOnly))
			if got := mm14Mode(t, fs, "f.mode.State() after "+what); got != tc.want {
				t.Errorf("after %s (Config.ReadOnly %v), f.mode.State() = %v (%d), want %v (%d): New's initial "+
					"mode is mountReadOnly if Config.ReadOnly is set and mountWritable otherwise (ADR 0014 "+
					"section 7; section 11, M1)", what, tc.readOnly, got, uint32(got), tc.want, uint32(tc.want))
			}
		})
	}
}

// TestMountModesTableHasOnlyTheDivergeEdge is M2 of ADR 0014 section 11: the
// table is named "mode", after the field FS.mode (section 3), and has exactly
// one edge, Writable to Diverged on Diverge (section 7), so no edge leaves
// ReadOnly or Diverged.
func TestMountModesTableHasOnlyTheDivergeEdge(t *testing.T) {
	if got := mountModes.Name(); got != "mode" {
		t.Errorf("mountModes.Name() = %q, want %q: a table is named after the field that holds its state, "+
			"FS.mode (ADR 0014 section 3, section 7; section 11, M2)", got, "mode")
	}
	got := mountModes.Edges()
	want := []fsm.Edge[mountMode, mountEvent]{{From: mountWritable, On: mountDiverge, To: mountDiverged}}
	if len(got) != len(want) {
		t.Fatalf("mountModes.Edges() = %v (length %d), want %v (length %d): the mount mode has one edge, "+
			"Writable to Diverged on Diverge (ADR 0014 section 7; section 11, M2)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i].From != want[i].From {
			t.Errorf("mountModes.Edges()[%d].From = %v (%d), want %v (%d) (ADR 0014 section 7; section 11, M2)",
				i, got[i].From, uint32(got[i].From), want[i].From, uint32(want[i].From))
		}
		if got[i].On != want[i].On {
			t.Errorf("mountModes.Edges()[%d].On = %v (%d), want %v (%d) (ADR 0014 section 7; section 11, M2)",
				i, got[i].On, uint8(got[i].On), want[i].On, uint8(want[i].On))
		}
		if got[i].To != want[i].To {
			t.Errorf("mountModes.Edges()[%d].To = %v (%d), want %v (%d) (ADR 0014 section 7; section 11, M2)",
				i, got[i].To, uint32(got[i].To), want[i].To, uint32(want[i].To))
		}
	}
}

// TestASwapLostToAnotherWriterDivergesTheMountAndItStoresNothingMore is M3's
// writable half and M7 of ADR 0014 section 11. On a writable mount with one
// commit behind it (initEmpty's) and a change since (a Create), another
// writer moves the root pointer: a foreign root pointer, with Writer
// "other-host/4242", is written on the inner store (ADR 0022 section 8, the
// facts). A Sync whose swap of rootKey the fake then fails with
// store.ErrPrecondition returns an error and leaves f.mode.State() ==
// mountDiverged: the read of the root pointer that follows the refusal shows
// another writer's root pointer, and resolveRoot fires mountDiverge (ADR 0022
// sections 2, 3 and 5; at edcfc15 commitLocked fired it on the refusal
// alone, ADR 0014 section 7). A Write and a Create after it answer
// vfs.ErrStale, since mutable() maps mountDiverged so. Then a second Sync
// fails too, and from the swap that failed onwards no Put, PutIfMatch or
// Delete of any key reaches the store: a diverged mount's commit is refused at
// commitLocked's top before it stores anything (section 7; M7). The read is a
// GetWithETag, which the fake does not record.
func TestASwapLostToAnotherWriterDivergesTheMountAndItStoresNothingMore(t *testing.T) {
	ctx := t.Context()
	fake := &mm14Store{inner: mm14Local(t)}
	cfg := mm14Config(fake, false)
	owner := mm14Owner(cfg)

	// One commit behind it, initEmpty's; the fake is not armed yet.
	fs := mm14MustNew(t, ctx, "New, writable, over an empty bucket behind the fake", cfg)

	// A change since.
	h, err := mm14Create(t, ctx, fs, owner, "a")
	if err != nil {
		t.Fatalf("Create %q in the root directory as its owner (%d, %d) = %v, want success (ADR 0010)", "a",
			owner.UID, owner.GID, err)
	}

	// Another writer moves the root pointer (ADR 0022 section 8, the foreign
	// root pointer), at the mount's epoch plus one: 2, since New over an empty
	// bucket initialises epoch 1 (ADR 0021 section 6, F1 step 1) and no commit
	// has followed. The epoch is not read from FS.epoch, since ADR 0014
	// section 11 lets this file read no unexported field but f.mode.
	if _, err := rk5WriteForeignRoot(ctx, fake.inner, 2, rk5Writer); err != nil {
		t.Fatalf("setup: writing a foreign root pointer on the inner store: %v", err)
	}

	// The swap is lost to another writer.
	fake.arm(store.ErrPrecondition)
	errSync := mm14Sync(t, ctx, fs, "Sync whose swap of the root pointer fails with store.ErrPrecondition")
	ops := fake.ops()
	failed := mm14Injected(ops)
	if failed < 0 {
		t.Fatalf("Sync, with a file created since the last commit, made no PutIfMatch of the root pointer %q, so "+
			"the fake's store.ErrPrecondition was never delivered (it returned %v). Write calls, in order: %v",
			rootKey, errSync, ops)
	}
	if errSync == nil {
		t.Errorf("Sync whose swap of the root pointer %q failed with store.ErrPrecondition = nil, want an error: "+
			"another writer moved the root pointer (ADR 0014 section 7; section 11, M3)", rootKey)
	}
	if got := mm14Mode(t, fs, "f.mode.State() after the lost swap"); got != mountDiverged {
		t.Errorf("after a Sync whose swap failed with store.ErrPrecondition, f.mode.State() = %v (%d), want %v "+
			"(%d): resolveRoot fires mountDiverge, Writable to Diverged, on the read that shows another writer's "+
			"root pointer (ADR 0022 sections 2 and 3; ADR 0014 section 11, M3)",
			got, uint32(got), mountDiverged, uint32(mountDiverged))
	}

	// Changes are refused with vfs.ErrStale.
	w := mm14Write(t, ctx, fs, owner, h, "Write of one byte at offset 0 to \"a\" after the lost swap")
	if !errors.Is(w.err, vfs.ErrStale) {
		t.Errorf("Write of one byte at offset 0, UNSTABLE, to %q after the lost swap = (%d, %d, %v), want "+
			"vfs.ErrStale: mutable() answers vfs.ErrStale for mountDiverged (ADR 0014 section 7; section 11, M3)",
			"a", w.n, uint32(w.how), w.err)
	}
	if _, err := mm14Create(t, ctx, fs, owner, "b"); !errors.Is(err, vfs.ErrStale) {
		t.Errorf("Create %q in the root directory after the lost swap = %v, want vfs.ErrStale: mutable() "+
			"answers vfs.ErrStale for mountDiverged (ADR 0014 section 7; section 11, M3)", "b", err)
	}

	// M7: a diverged mount's commit stores nothing.
	beforeSecond := len(fake.ops())
	if err := mm14Sync(t, ctx, fs, "a second Sync on the diverged mount"); err == nil {
		t.Errorf("a second Sync on the diverged mount = nil, want an error: Sync refuses at the top of its " +
			"commit, before any probe, when f.mode.State() is mountDiverged (ADR 0014 section 7; section 11, " +
			"M7; ADR 0023 section 2, step 3)")
	}
	ops = fake.ops()
	if after := ops[failed+1:]; len(after) != 0 {
		t.Errorf("after the PutIfMatch of %q that failed with store.ErrPrecondition, the fake saw %d write "+
			"call(s), %v, of which the first %d came before the second Sync began; want none: a diverged mount's "+
			"commit is refused before it stores anything (ADR 0014 section 7; section 11, M7)", rootKey,
			len(after), after, beforeSecond-(failed+1))
	}
}

// TestReadOnlyMountStaysReadOnlyThroughWriteAndSync is M3's read-only half
// of ADR 0014 section 11: on a read-only mount of a bucket that holds a
// filesystem, a Write answers vfs.ErrROFS with a count of 0 (ADR 0011 section
// 4), a Sync returns nil (ADR 0011 section 4), and f.mode.State() is
// mountReadOnly after both, since no edge leaves ReadOnly (ADR 0014 section
// 7).
func TestReadOnlyMountStaysReadOnlyThroughWriteAndSync(t *testing.T) {
	ctx := t.Context()
	local := mm14Local(t)
	mm14Seed(t, ctx, local)
	cfg := mm14Config(local, true)
	owner := mm14Owner(cfg)
	fs := mm14MustNew(t, ctx, "New, read-only, over a bucket that holds a filesystem", cfg)

	type lookupRes struct {
		h   vfs.Handle
		err error
	}
	lr := mm14Go(func() lookupRes {
		h, _, err := fs.Lookup(ctx, owner, fs.Root(), "a")
		return lookupRes{h, err}
	}).wait(t, "Lookup of \"a\" in the read-only mount's root directory")
	if lr.err != nil {
		t.Fatalf("Lookup of %q in the read-only mount's root directory = %v, want success: the seeding commit "+
			"holds it", "a", lr.err)
	}

	w := mm14Write(t, ctx, fs, owner, lr.h, "Write of one byte at offset 0 to \"a\" on the read-only mount")
	if !errors.Is(w.err, vfs.ErrROFS) || w.n != 0 {
		t.Errorf("Write of one byte at offset 0, UNSTABLE, to %q on the read-only mount = (%d, %d, %v), want "+
			"vfs.ErrROFS with a count of 0: mutable() answers vfs.ErrROFS for mountReadOnly (ADR 0011 section 4; "+
			"ADR 0014 section 7; section 11, M3)", "a", w.n, uint32(w.how), w.err)
	}
	if err := mm14Sync(t, ctx, fs, "Sync on the read-only mount"); err != nil {
		t.Errorf("Sync on the read-only mount = %v, want nil: nothing is ever changed, so there is nothing to "+
			"commit (ADR 0011 section 4; ADR 0014 section 11, M3)", err)
	}
	if got := mm14Mode(t, fs, "f.mode.State() on the read-only mount after Write and Sync"); got != mountReadOnly {
		t.Errorf("after a refused Write and a Sync on the read-only mount, f.mode.State() = %v (%d), want %v "+
			"(%d): no edge leaves ReadOnly (ADR 0014 section 7; section 11, M3)", got, uint32(got),
			mountReadOnly, uint32(mountReadOnly))
	}
}

// TestMountModeAndEventStrings is M5 of ADR 0014 section 11: String gives
// the bare vocabulary names (docs/vocabulary.md V1), and "<type>(<n>)", n in
// decimal, for a value with no name (section 3, section 7).
func TestMountModeAndEventStrings(t *testing.T) {
	cases := []struct {
		expr string
		got  string
		want string
	}{
		{"mountWritable.String()", mountWritable.String(), "Writable"},
		{"mountReadOnly.String()", mountReadOnly.String(), "ReadOnly"},
		{"mountDiverged.String()", mountDiverged.String(), "Diverged"},
		{"mountDiverge.String()", mountDiverge.String(), "Diverge"},
		{"mountMode(0).String()", mountMode(0).String(), "mountMode(0)"},
		{"mountMode(4).String()", mountMode(4).String(), "mountMode(4)"},
		{"mountEvent(0).String()", mountEvent(0).String(), "mountEvent(0)"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q (ADR 0014 section 3, section 7; section 11, M5; docs/vocabulary.md V1)",
				tc.expr, tc.got, tc.want)
		}
	}
}

// TestRefusedUnconditionalSwapLeavesTheMountWritable is M6 of ADR 0014
// section 11: on a writable mount with one commit behind it and a change
// since, without Config.AllowUnconditionalRoot, a Sync whose swap of rootKey
// the fake answers store.ErrUnsupported returns ADR 0021 section 2's
// refusal, an error for which errors.Is(err, store.ErrUnsupported) holds;
// that refusal fires nothing, so f.mode.State() stays mountWritable, and a
// Create of a fresh name afterwards succeeds (ADR 0014 section 7, "Its one
// writer"). The fake passes the reclaim's Get of rootKey and any Delete
// through; nothing here pins them.
func TestRefusedUnconditionalSwapLeavesTheMountWritable(t *testing.T) {
	ctx := t.Context()
	fake := &mm14Store{inner: mm14Local(t)}
	cfg := mm14Config(fake, false)
	cfg.AllowUnconditionalRoot = false
	owner := mm14Owner(cfg)

	fs := mm14MustNew(t, ctx, "New, writable, without AllowUnconditionalRoot, over an empty bucket behind the fake", cfg)
	if _, err := mm14Create(t, ctx, fs, owner, "a"); err != nil {
		t.Fatalf("Create %q in the root directory as its owner (%d, %d) = %v, want success (ADR 0010)", "a",
			owner.UID, owner.GID, err)
	}

	fake.arm(store.ErrUnsupported)
	errSync := mm14Sync(t, ctx, fs, "Sync whose swap of the root pointer is answered store.ErrUnsupported")
	if ops := fake.ops(); mm14Injected(ops) < 0 {
		t.Fatalf("Sync, with a file created since the last commit, made no PutIfMatch of the root pointer %q, so "+
			"the fake's store.ErrUnsupported was never delivered (it returned %v). Write calls, in order: %v",
			rootKey, errSync, ops)
	}
	if !errors.Is(errSync, store.ErrUnsupported) {
		t.Errorf("Sync, without AllowUnconditionalRoot, whose swap of the root pointer %q was answered "+
			"store.ErrUnsupported = %v, want an error for which errors.Is(err, store.ErrUnsupported) holds "+
			"(ADR 0021 section 2; ADR 0014 section 11, M6)", rootKey, errSync)
	}
	if got := mm14Mode(t, fs, "f.mode.State() after the refused swap"); got != mountWritable {
		t.Errorf("after a Sync whose swap was answered store.ErrUnsupported, without AllowUnconditionalRoot, "+
			"f.mode.State() = %v (%d), want %v (%d): ADR 0021's refusal fires nothing while its read shows "+
			"another root pointer, and only a read of the root pointer that shows another writer's fires "+
			"mountDiverge (ADR 0014 section 7; section 11, M6; ADR 0021 section 2; ADR 0022 sections 2 to 4)",
			got, uint32(got), mountWritable, uint32(mountWritable))
	}
	if _, err := mm14Create(t, ctx, fs, owner, "b"); err != nil {
		t.Errorf("Create %q in the root directory after the refused swap = %v, want success: the mount stays "+
			"writable, and mutable() is nil for mountWritable (ADR 0014 section 7; section 11, M6; ADR 0021 "+
			"section 2)", "b", err)
	}
}

// TestNewWhoseFirstSwapLosesFailsWithoutPanicking is M8 of ADR 0014 section
// 11: a writable New over an empty bucket behind a fake whose first
// PutIfMatch of rootKey fails with store.ErrPrecondition returns a nil *FS
// and a non-nil error, and does not panic. Since ADR 0022 the refusal is
// followed by one read of the root pointer, which finds none while the mount
// descends from none, so the root is settled as unchanged and the commit, and
// New, fail without the mode moving (ADR 0022 section 3, classifyRoot's rule
// 1; section 5). What the bucket then holds is not pinned, and the *FS, if
// any, is never used (ADR 0011 section 7).
func TestNewWhoseFirstSwapLosesFailsWithoutPanicking(t *testing.T) {
	ctx := t.Context()
	fake := &mm14Store{inner: mm14Local(t)}
	fake.arm(store.ErrPrecondition)
	cfg := mm14Config(fake, false)

	what := "New, writable, over an empty bucket, whose first PutIfMatch of the root pointer fails with " +
		"store.ErrPrecondition"
	c := mm14Go(func() mm14NewResult {
		fs, err := New(ctx, cfg)
		return mm14NewResult{fs, err}
	})
	select {
	case <-c.done:
	case <-time.After(mm14Bound):
		t.Fatalf("%s has not returned after %v, so it is hung; want it to return (ADR 0011 section 7)", what,
			mm14Bound)
	}
	if c.panicked {
		t.Fatalf("%s panicked with %v, want it to return a nil *FS and an error: New makes f.mode and "+
			"f.rootKnowledge before loadOrInit, since initEmpty's commit fires on them (ADR 0014 section 7; "+
			"section 11, M8; ADR 0022 section 2)", what, c.pval)
	}
	if !c.returned {
		t.Fatalf("%s ended without returning (runtime.Goexit), want it to return (ADR 0011 section 7)", what)
	}
	if ops := fake.ops(); mm14Injected(ops) < 0 {
		t.Errorf("%s made no PutIfMatch of the root pointer %q, so the fake's store.ErrPrecondition was never "+
			"delivered (it returned a non-nil *FS: %v, and error %v). Write calls, in order: %v", what, rootKey,
			c.val.fs != nil, c.val.err, ops)
	}
	if c.val.fs != nil {
		t.Errorf("%s returned a non-nil *FS, want nil: initEmpty's commit lost its swap, so New fails (ADR 0014 "+
			"section 11, M8). The FS is not used (ADR 0011 section 7)", what)
	}
	if c.val.err == nil {
		t.Errorf("%s returned a nil error, want a non-nil one (ADR 0014 section 11, M8)", what)
	}
}

// TestMutableAnswersForEveryMountMode is M9 of ADR 0014 section 11: mutable()
// reads f.mode and nothing else, and returns nil for mountWritable,
// vfs.ErrROFS for mountReadOnly, vfs.ErrStale for mountDiverged and
// vfs.ErrServerFault for any other value, so a state added later is refused
// until its ADR maps it (section 7; Assumption 24). Since it reads nothing
// else, an *FS literal that sets only mode is enough.
func TestMutableAnswersForEveryMountMode(t *testing.T) {
	cases := []struct {
		m    mountMode
		want error
	}{
		{mountWritable, nil},
		{mountReadOnly, vfs.ErrROFS},
		{mountDiverged, vfs.ErrStale},
		{mountMode(0), vfs.ErrServerFault},
		{mountMode(4), vfs.ErrServerFault},
	}
	for _, tc := range cases {
		what := fmt.Sprintf("(&FS{mode: fsm.NewAtomic(mountModes, %v)}).mutable()", tc.m)
		got := mm14Go(func() error {
			f := &FS{mode: fsm.NewAtomic(mountModes, tc.m)}
			return f.mutable()
		}).wait(t, what)
		if tc.want == nil {
			if got != nil {
				t.Errorf("%s = %v, want nil: mutable() is nil exactly when the mode is Writable (ADR 0014 "+
					"section 7; section 11, M9; docs/vocabulary.md V1)", what, got)
			}
			continue
		}
		if !errors.Is(got, tc.want) {
			t.Errorf("%s = %v, want %v (mode %d) (ADR 0014 section 7; section 11, M9)", what, got, tc.want,
				uint32(tc.m))
		}
	}
}
