package blobfs

// A clean-room regression test for #48, "mutable() reads diverged without a
// lock", written without reading the implementation, from #48, the Go memory
// model (https://go.dev/ref/mem), ADR 0003 §1 and §5, ADR 0009 §5, ADR 0010,
// ADR 0011 §7 and internal/vfs/vfs.go.
//
// ADR 0003 §5: a commit that finds the filesystem has diverged fails, and sets
// the FS's diverged state, so that the next Write is answered vfs.ErrStale by
// mutable(). #48: mutable() reads that state with no lock, while the failing
// commit writes it. Under the Go memory model a write and a read of the same
// memory location that no happens-before relation orders, and that are not
// both sync/atomic accesses, are a data race, and a program with one has no
// defined behaviour; go test -race reports it and fails the test.
//
// So the test drives exactly that interleaving. A Sync is held inside the
// root pointer's swap, the PutIfMatch of rootKey, which the store fake then
// fails with store.ErrPrecondition: another writer moved the root pointer, so
// this FS has diverged (ADR 0003 §5). While it is held a Write starts, with no
// synchronisation between the two beyond the store fake's own channels, which
// order only the test goroutine against the Sync. What that Write returns is
// not pinned: it may land before the commit fails and succeed, or see the
// divergence and answer vfs.ErrStale. What is pinned is that it is not a data
// race, that the Sync fails, and that a Write after both have returned is
// answered vfs.ErrStale.
//
// The test follows ADR 0011 §7's rules for staying safe against a build that
// misbehaves: SnapshotRetention is -1, so no commit starts a pruning goroutine
// that outlives the test; every New and every FS call runs in a goroutine that
// recovers a panic and is waited for under dr48Bound; and the bucket is a local
// directory under the test's temporary directory. MaxDirtyBytes is -1, which
// disables waiting on the write budget (ADR 0003 §1), so neither Write can
// start a Sync of its own. There is no time.Sleep: the interleaving is built
// from channels alone.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// dr48Bound is how long the test waits for any one step before it reports the
// step as hung.
const dr48Bound = 10 * time.Second

// dr48Store is a store.Store over a *store.Local that passes every call
// through, except that once armed, its next PutIfMatch of rootKey closes
// entered, waits until release is closed or testCtx ends, and then fails with
// store.ErrPrecondition without calling the inner store. It disarms itself as
// it takes that call, so only one swap is held. It does not embed store.Store,
// so that every method it has is one written here.
type dr48Store struct {
	inner   *store.Local
	testCtx context.Context
	entered chan struct{}
	release chan struct{}

	mu    sync.Mutex
	armed bool
}

var _ store.Store = (*dr48Store)(nil)

// arm makes the next PutIfMatch of rootKey the one that is held and failed.
func (s *dr48Store) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed = true
}

// takeArm reports whether the fake was armed, and disarms it.
func (s *dr48Store) takeArm() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.armed
	s.armed = false
	return was
}

func (s *dr48Store) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *dr48Store) GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error) {
	return s.inner.GetRange(ctx, key, off, n)
}

func (s *dr48Store) Head(ctx context.Context, key string) (store.ObjectInfo, error) {
	return s.inner.Head(ctx, key)
}

func (s *dr48Store) Put(ctx context.Context, key string, data []byte) error {
	return s.inner.Put(ctx, key, data)
}

func (s *dr48Store) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

func (s *dr48Store) List(ctx context.Context, prefix, after string, max int) ([]store.ObjectInfo, error) {
	return s.inner.List(ctx, prefix, after, max)
}

func (s *dr48Store) PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) {
	if key == rootKey && s.takeArm() {
		close(s.entered)
		select {
		case <-s.release:
		case <-s.testCtx.Done():
		}
		return "", store.ErrPrecondition
	}
	return s.inner.PutIfMatch(ctx, key, data, etag)
}

func (s *dr48Store) Name() string {
	return s.inner.Name()
}

// dr48Call is one call running in its own goroutine. val, returned, panicked
// and pval may be read only once done is closed.
type dr48Call[T any] struct {
	done     chan struct{}
	val      T
	returned bool
	panicked bool
	pval     any
}

// dr48Go runs fn in its own goroutine, recovering and recording a panic out of
// it. fn must not touch t: after a timeout its goroutine is abandoned.
func dr48Go[T any](fn func() T) *dr48Call[T] {
	c := &dr48Call[T]{done: make(chan struct{})}
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

// wait waits for the call under dr48Bound and returns its value. A hang, a
// panic or a runtime.Goexit fails the test.
func (c *dr48Call[T]) wait(t *testing.T, what string) T {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(dr48Bound):
		t.Fatalf("%s has not returned after %v, so it is hung", what, dr48Bound)
	}
	if c.panicked {
		t.Fatalf("%s panicked with %v", what, c.pval)
	}
	if !c.returned {
		t.Fatalf("%s ended without returning (runtime.Goexit)", what)
	}
	return c.val
}

// dr48WR is what one Write returned.
type dr48WR struct {
	n   uint32
	how vfs.Stability
	err error
}

// TestWriteRacingADivergingCommitIsNotADataRace pins #48: a Write that starts
// while a Sync of the same FS is inside the root pointer's swap that makes the
// FS diverge, unordered against that swap, completes with no data race under
// go test -race (the Go memory model, https://go.dev/ref/mem). What that Write
// returns is not pinned. The Sync fails, and a Write after both have returned
// is answered vfs.ErrStale (ADR 0003 §5).
func TestWriteRacingADivergingCommitIsNotADataRace(t *testing.T) {
	ctx := t.Context()

	// 1. The store fake, over a local bucket in the test's temp dir (ADR 0011
	// §7; store.Store as ADR 0009 §5 restates it).
	local, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatalf("setup: store.NewLocal: %v", err)
	}
	fake := &dr48Store{
		inner:   local,
		testCtx: ctx,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(fake.release) }) }
	defer release()

	// 2. New, with pruning off and the write budget disabled (ADR 0011 §7,
	// ADR 0003 §1), and one regular file in the root directory, created by
	// the root directory's owner (ADR 0010).
	cfg := Config{
		Store:             fake,
		ChunkSize:         4096,
		OwnerUID:          501,
		OwnerGID:          20,
		SnapshotRetention: -1,
		MaxDirtyBytes:     -1,
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	caller := vfs.Caller{UID: cfg.OwnerUID, GID: cfg.OwnerGID}

	type newRes struct {
		fs  *FS
		err error
	}
	nr := dr48Go(func() newRes {
		fs, err := New(ctx, cfg)
		return newRes{fs, err}
	}).wait(t, "New, writable, over an empty bucket")
	if nr.err != nil {
		t.Fatalf("New, writable, over an empty bucket = %v, want success", nr.err)
	}
	if nr.fs == nil {
		t.Fatalf("New, writable, over an empty bucket returned a nil *FS and a nil error")
	}
	fs := nr.fs

	type createRes struct {
		h   vfs.Handle
		err error
	}
	cr := dr48Go(func() createRes {
		h, _, err := fs.Create(ctx, caller, fs.Root(), "race.bin", vfs.SetAttr{}, true)
		return createRes{h, err}
	}).wait(t, "Create race.bin in the root directory")
	if cr.err != nil {
		t.Fatalf("Create race.bin in the root directory as its owner (%d, %d) = %v, want success "+
			"(ADR 0010)", caller.UID, caller.GID, cr.err)
	}

	// 3. Arm the fake only now, since New's first commit also swaps rootKey;
	// start Sync in goroutine A, and wait until it is inside the swap.
	fake.arm()
	syncA := dr48Go(func() error { return fs.Sync(ctx) })
	select {
	case <-fake.entered:
	case <-syncA.done:
		t.Fatalf("Sync, with a file created since the last commit, returned (err %v, panicked %v "+
			"with %v) without calling PutIfMatch of the root pointer %q, so the test never held "+
			"a commit inside its root-pointer swap", syncA.val, syncA.panicked, syncA.pval, rootKey)
	case <-time.After(dr48Bound):
		t.Fatalf("Sync has not called PutIfMatch of the root pointer %q after %v", rootKey, dr48Bound)
	}

	// 4. Start goroutine B, a one-byte UNSTABLE Write at offset 0, with no
	// synchronisation against A.
	writeB := dr48Go(func() dr48WR {
		n, how, err := fs.Write(ctx, caller, cr.h, 0, []byte{'b'}, vfs.Unstable)
		return dr48WR{n, how, err}
	})

	// 5. Let A's swap fail with store.ErrPrecondition, and wait for both.
	release()
	errA := syncA.wait(t, "Sync whose root-pointer swap fails with store.ErrPrecondition")
	_ = writeB.wait(t, "Write that started while that Sync was inside its root-pointer swap")

	// 6. A's Sync failed, and the FS has diverged. B's result is not pinned.
	if errA == nil {
		t.Errorf("Sync whose root-pointer swap failed with store.ErrPrecondition = nil, want an " +
			"error: another writer moved the root pointer, so the filesystem has diverged " +
			"(ADR 0003 §5)")
	}
	after := dr48Go(func() dr48WR {
		n, how, err := fs.Write(ctx, caller, cr.h, 0, []byte{'c'}, vfs.Unstable)
		return dr48WR{n, how, err}
	}).wait(t, "Write after the diverging Sync and the racing Write have both returned")
	if !errors.Is(after.err, vfs.ErrStale) {
		t.Errorf("Write after the diverging Sync and the racing Write have both returned = (%d, %d, "+
			"%v), want vfs.ErrStale: the failed commit set the FS's diverged state, and mutable() "+
			"reports it (ADR 0003 §5)", after.n, uint32(after.how), after.err)
	}
}
