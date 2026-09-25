package blobfs

// Clean-room tests for the wiring of ADR 0003's write-backpressure budget into
// blobfs, written from docs/adr/0003-write-backpressure.md as revised through
// 2026-09-25, for ADR 0006, from docs/adr/0006-a-pending-trim-is-part-of-the-file.md
// §3, §5, §7 and §8, and from internal/vfs/vfs.go, without reading the
// implementation. They cover §1 (New's mapping of Config.MaxDirtyBytes onto
// the budget); §2 (the five accounting sites; ordering, preconditions and the
// invariant; "Why site 4 releases on a failed flush"; and the table "What each
// operation does to DirtyBytes()", with each truncate row reached as its
// closing list says); §3 (every call that a lock held across the wait could
// deadlock is bounded); §4 (where Write waits, the paths that never wait, the
// chunk list read after the wait and its trace, if the inode goes away, stable
// writes, what Write returns when the wait fails, and the test surface); §5 (a
// read-only store, and divergence); §6 (the bound, for one writer and at every
// instant); §7 (the three records, seen through Write); §8 (DirtyBytes);
// Assumptions 6 and 15-18; and the Pinner bullet of "What this does not
// decide". The budget type on its own is covered by budget_test.go, whose
// helpers this file reuses.
//
// Since ADR 0006, truncate never stages a chunk from the cache and never
// charges, and a flush applies a pending trim through a copy of its own that
// never enters of.dirty and is never charged, so sites 3 and 4 only release
// and site 1 alone adds (ADR 0003 §2; ADR 0006 §7). The truncate rows of F2,
// the site-4 rows for a file whose inode is still there, and F11 pin that.
// openFile.pendingTrim is read only through its length (bpSnapshot). What a
// pending trim means to Read, Write and a flush is covered by
// pending_trim_test.go.
//
// Every call that could hang runs in its own goroutine (bpGo) and is waited
// for under btHangBound, and the failure names the call. States are reached by
// polling with a deadline and through channels the store fake closes, never by
// sleeping. The one fixed wait, in TestBackpressureWarnRecordThroughWrite,
// only checks that something does NOT happen.
//
// bpStore is the one store fake. It wraps a real store and intercepts Get and
// Put of chunk keys, or of one given key, to announce the call, hold it at a
// gate, and then fail it, panic, or pass it through.
//
// Not covered, on purpose:
//   - A WRITE parked at shutdown is answered NFS3ERR_IO (Consequences). It
//     needs pipelined calls on one connection and a fake that honours
//     cancellation, and its parts are pinned elsewhere: budget_test.go for the
//     abandoned drain, TestBackpressureParkedWriteGivesUp for Write returning
//     ctx.Err(), and statusOf for the mapping.
//   - flushAll failing partway (§2's table). Its file order is map order, so a
//     test cannot choose which files were flushed before the failing one.
//   - used() >= Σ of.bytes while accounting steps are in flight (§2, the whole
//     budget). Two unsynchronised reads cannot observe it; bpCheckAccounting
//     checks the equality at rest.
//
// Avoided, because it is pre-existing and out of scope, and trips -race or
// flakes: divergence while other goroutines write. A Read concurrent with a
// Write to the same file is covered by read_consistency_test.go (ADR 0005).
// cache_test.go now covers writing into a file again after its flush failed
// partway through its uploads (#46).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// bpCS is the chunk size every FS in this file uses.
const bpCS = 4096

// bpSeq makes each call to bpUnique return different bytes.
var bpSeq atomic.Uint64

// bpUnique returns n bytes that no other call in this process returns. A flush
// of them is content new to the bucket and to the FS, so it reaches Head and
// then Put, and a successful upload caches it.
func bpUnique(n int) []byte {
	seq := bpSeq.Add(1)
	out := make([]byte, 0, n+sha256.Size)
	for ctr := uint64(0); len(out) < n; ctr++ {
		var in [16]byte
		binary.BigEndian.PutUint64(in[:8], seq)
		binary.BigEndian.PutUint64(in[8:], ctr)
		sum := sha256.Sum256(in[:])
		out = append(out, sum[:]...)
	}
	return out[:n]
}

// bpHow names a Stability the way RFC 1813 spells stable_how.
func bpHow(s vfs.Stability) string {
	switch s {
	case vfs.Unstable:
		return "UNSTABLE"
	case vfs.DataSync:
		return "DATA_SYNC"
	case vfs.FileSync:
		return "FILE_SYNC"
	}
	return fmt.Sprintf("Stability(%d)", uint32(s))
}

// bpLocal returns an empty local bucket in a temp dir.
func bpLocal(t *testing.T) *store.Local {
	t.Helper()
	st, err := store.NewLocal(filepath.Join(t.TempDir(), "bucket"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// bpNew mounts an FS on st with the given MaxDirtyBytes and logger. Pruning is
// off, because several tests commit many times and some run over a fake.
func bpNew(t *testing.T, st store.Store, limit int64, lg *slog.Logger) *FS {
	t.Helper()
	fs, err := New(context.Background(), Config{
		Store:             st,
		ChunkSize:         bpCS,
		OwnerUID:          501,
		OwnerGID:          20,
		SnapshotRetention: -1,
		MaxDirtyBytes:     limit,
		Log:               lg,
	})
	if err != nil {
		t.Fatalf("New with MaxDirtyBytes %d: %v", limit, err)
	}
	return fs
}

// bpCall is one call running in its own goroutine. val, returned, panicked and
// pval may be read only once done is closed.
type bpCall[T any] struct {
	done     chan struct{}
	val      T
	returned bool
	panicked bool
	pval     any
}

// bpGo runs fn in its own goroutine. A panic out of fn is recovered there and
// recorded. fn must not touch t: after a timeout its goroutine is abandoned.
func bpGo[T any](fn func() T) *bpCall[T] {
	c := &bpCall[T]{done: make(chan struct{})}
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

// finished reports, without blocking, whether the call has ended.
func (c *bpCall[T]) finished() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// end waits for the call under btHangBound and fails the test if it is hung.
func (c *bpCall[T]) end(t *testing.T, what string) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(btHangBound):
		t.Fatalf("%s has not returned after %v, so it is hung", what, btHangBound)
	}
}

// wait waits for the call and returns its value. A panic fails the test.
func (c *bpCall[T]) wait(t *testing.T, what string) T {
	t.Helper()
	c.end(t, what)
	if c.panicked {
		t.Fatalf("%s panicked with %v", what, c.pval)
	}
	if !c.returned {
		t.Fatalf("%s ended without returning (runtime.Goexit)", what)
	}
	return c.val
}

// bpWR is what one Write returned.
type bpWR struct {
	n   uint32
	how vfs.Stability
	err error
}

func (r bpWR) String() string {
	return fmt.Sprintf("(%d, %s, %v)", r.n, bpHow(r.how), r.err)
}

// bpStartWrite starts fs.Write in its own goroutine.
func bpStartWrite(ctx context.Context, fs *FS, c vfs.Caller, h vfs.Handle, off uint64, data []byte, how vfs.Stability) *bpCall[bpWR] {
	return bpGo(func() bpWR {
		n, got, err := fs.Write(ctx, c, h, off, data, how)
		return bpWR{n, got, err}
	})
}

// bpWrite runs fs.Write as testCaller under the hang bound.
func bpWrite(t *testing.T, fs *FS, what string, h vfs.Handle, off uint64, data []byte, how vfs.Stability) bpWR {
	t.Helper()
	return bpStartWrite(context.Background(), fs, testCaller, h, off, data, how).wait(t, what)
}

// bpMustWrite runs an UNSTABLE write under the hang bound and requires it to
// accept every byte.
func bpMustWrite(t *testing.T, fs *FS, what string, h vfs.Handle, off uint64, data []byte) {
	t.Helper()
	r := bpWrite(t, fs, what, h, off, data, vfs.Unstable)
	if r.err != nil || int(r.n) != len(data) {
		t.Fatalf("%s = %v, want (%d, UNSTABLE, nil)", what, r, len(data))
	}
}

// bpDo runs fn under the hang bound and returns its error.
func bpDo(t *testing.T, what string, fn func() error) error {
	t.Helper()
	return bpGo(fn).wait(t, what)
}

// bpSync runs fs.Sync under the hang bound and returns its error.
func bpSync(t *testing.T, fs *FS, what string) error {
	t.Helper()
	return bpDo(t, what, func() error { return fs.Sync(context.Background()) })
}

// bpMustSync runs fs.Sync under the hang bound and requires it to succeed.
func bpMustSync(t *testing.T, fs *FS, what string) {
	t.Helper()
	if err := bpSync(t, fs, what); err != nil {
		t.Fatalf("%s = %v; a Sync against a healthy writable store must succeed", what, err)
	}
}

// bpCreate creates name in the root directory, with mode if it is not zero,
// under the hang bound.
func bpCreate(t *testing.T, fs *FS, name string, mode uint32) (vfs.Handle, vfs.Attr) {
	t.Helper()
	var sa vfs.SetAttr
	if mode != 0 {
		sa.Mode = &mode
	}
	type res struct {
		h    vfs.Handle
		attr vfs.Attr
		err  error
	}
	r := bpGo(func() res {
		h, attr, err := fs.Create(context.Background(), testCaller, fs.Root(), name, sa, true)
		return res{h, attr, err}
	}).wait(t, "Create "+name)
	if r.err != nil {
		t.Fatalf("Create %s: %v", name, r.err)
	}
	return r.h, r.attr
}

// bpLookup looks name up in the root directory under the hang bound.
func bpLookup(t *testing.T, fs *FS, name string) (vfs.Handle, vfs.Attr) {
	t.Helper()
	type res struct {
		h    vfs.Handle
		attr vfs.Attr
		err  error
	}
	r := bpGo(func() res {
		h, attr, err := fs.Lookup(context.Background(), testCaller, fs.Root(), name)
		return res{h, attr, err}
	}).wait(t, "Lookup "+name)
	if r.err != nil {
		t.Fatalf("Lookup %s: %v", name, r.err)
	}
	return r.h, r.attr
}

// bpGetAttr runs GetAttr under the hang bound.
func bpGetAttr(t *testing.T, fs *FS, h vfs.Handle, what string) vfs.Attr {
	t.Helper()
	type res struct {
		attr vfs.Attr
		err  error
	}
	r := bpGo(func() res {
		attr, err := fs.GetAttr(context.Background(), h)
		return res{attr, err}
	}).wait(t, what)
	if r.err != nil {
		t.Fatalf("%s: %v", what, r.err)
	}
	return r.attr
}

// bpTruncate sets h's size through SetAttr under the hang bound.
func bpTruncate(t *testing.T, fs *FS, h vfs.Handle, size uint64, what string) {
	t.Helper()
	err := bpDo(t, what, func() error {
		_, err := fs.SetAttr(context.Background(), testCaller, h, vfs.SetAttr{Size: &size})
		return err
	})
	if err != nil {
		t.Fatalf("%s = %v, want success", what, err)
	}
}

// bpRemove removes name from the root directory under the hang bound.
func bpRemove(t *testing.T, fs *FS, name, what string) {
	t.Helper()
	err := bpDo(t, what, func() error {
		return fs.Remove(context.Background(), testCaller, fs.Root(), name)
	})
	if err != nil {
		t.Fatalf("%s = %v, want success", what, err)
	}
}

// bpReadAll reads h to EOF under the hang bound.
func bpReadAll(t *testing.T, fs *FS, h vfs.Handle, what string) []byte {
	t.Helper()
	type res struct {
		b   []byte
		err error
	}
	r := bpGo(func() res {
		var out []byte
		var off uint64
		for {
			chunk, eof, err := fs.Read(context.Background(), testCaller, h, off, 1024)
			if err != nil {
				return res{out, fmt.Errorf("read at %d: %w", off, err)}
			}
			out = append(out, chunk...)
			off += uint64(len(chunk))
			if eof || len(chunk) == 0 {
				return res{out, nil}
			}
		}
	}).wait(t, what)
	if r.err != nil {
		t.Fatalf("%s: %v", what, r.err)
	}
	return r.b
}

// bpWantFile requires h to read back as want.
func bpWantFile(t *testing.T, fs *FS, h vfs.Handle, want []byte, what string) {
	t.Helper()
	if d := wsDiff(bpReadAll(t, fs, h, "reading back: "+what), want, 512); d != "" {
		t.Errorf("%s: %s", what, d)
	}
}

// bpWantRemount requires a fresh mount of st to find name with size len(want)
// and contents want.
func bpWantRemount(t *testing.T, st *store.Local, name string, want []byte, what string) {
	t.Helper()
	fs2, h2, attr := wsRemountLookup(t, st, name)
	if attr.Size != uint64(len(want)) {
		t.Errorf("%s: size on a fresh mount = %d, want %d", what, attr.Size, len(want))
	}
	bpWantFile(t, fs2, h2, want, what+", on a fresh mount")
}

// bpSeedFile commits a file of n unique bytes to st through a mount of its
// own, and returns the bytes.
func bpSeedFile(t *testing.T, st *store.Local, name string, n int) []byte {
	t.Helper()
	fs := bpNew(t, st, 0, quietLog())
	h, _ := bpCreate(t, fs, name, 0)
	data := bpUnique(n)
	bpMustWrite(t, fs, "seeding "+name, h, 0, data)
	bpMustSync(t, fs, "Sync seeding "+name)
	return data
}

// bpChunkKey returns the key of the stored chunk whose bytes are content.
func bpChunkKey(t *testing.T, st *store.Local, content []byte) string {
	t.Helper()
	ctx := context.Background()
	objs, err := st.List(ctx, chunkPrefix, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		b, err := st.Get(ctx, o.Key)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(b, content) {
			return o.Key
		}
	}
	t.Fatalf("no stored chunk holds the %d bytes looked for", len(content))
	return ""
}

// bpOpen returns FS.open[id], read under FS.mu.RLock (ADR 0003 §4, the test
// surface), or nil.
func bpOpen(t *testing.T, fs *FS, id uint64, what string) *openFile {
	t.Helper()
	of, ok := btBounded(btHangBound, func() *openFile {
		fs.mu.RLock()
		defer fs.mu.RUnlock()
		return fs.open[id]
	})
	if !ok {
		t.Fatalf("%s: FS.mu.RLock has not returned after %v, so FS.mu is held and "+
			"never released", what, btHangBound)
	}
	return of
}

// bpMustOpen is bpOpen for a file that must have an openFile.
func bpMustOpen(t *testing.T, fs *FS, id uint64, what string) *openFile {
	t.Helper()
	of := bpOpen(t, fs, id, what)
	if of == nil {
		t.Fatalf("%s: FS.open has no entry for inode %d", what, id)
	}
	return of
}

// bpOFSnap is an openFile's accounting state, read under its mu.
type bpOFSnap struct {
	bytes   int64
	caps    map[uint64]int
	pending int
}

// indices returns the chunk indices present in of.dirty, in order.
func (s bpOFSnap) indices() []uint64 {
	out := make([]uint64, 0, len(s.caps))
	for k := range s.caps {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// bpSnapshot reads of's fields holding of.mu (ADR 0003 §4, the test surface).
func bpSnapshot(t *testing.T, of *openFile, what string) bpOFSnap {
	t.Helper()
	s, ok := btBounded(btHangBound, func() bpOFSnap {
		of.mu.Lock()
		defer of.mu.Unlock()
		s := bpOFSnap{bytes: of.bytes.Load(), caps: map[uint64]int{}, pending: len(of.pendingTrim)}
		for k, v := range of.dirty {
			s.caps[k] = cap(v)
		}
		return s
	})
	if !ok {
		t.Fatalf("%s: locking the openFile's mu has not returned after %v, so it is "+
			"held and never released", what, btHangBound)
	}
	return s
}

// bpCheckAccounting asserts §2's invariant and the budget statement at rest.
// For each openFile that FS.open holds, observed holding its mu and then
// FS.mu (the order §3 permits, which keeps site 5 out while it looks), of.bytes
// must equal Σ cap(v) over of.dirty. DirtyBytes() must then equal the sum of
// of.bytes over FS.open, and must not be negative.
func bpCheckAccounting(t *testing.T, fs *FS, step string) {
	t.Helper()
	problems, ok := btBounded(btHangBound, func() []string {
		type entry struct {
			id uint64
			of *openFile
		}
		fs.mu.RLock()
		entries := make([]entry, 0, len(fs.open))
		for id, of := range fs.open {
			entries = append(entries, entry{id, of})
		}
		fs.mu.RUnlock()

		var (
			problems []string
			sum      int64
		)
		for _, e := range entries {
			e.of.mu.Lock()
			fs.mu.RLock()
			if fs.open[e.id] == e.of {
				var caps int64
				for _, v := range e.of.dirty {
					caps += int64(cap(v))
				}
				b := e.of.bytes.Load()
				if b != caps {
					problems = append(problems, fmt.Sprintf("inode %d: of.bytes = %d but "+
						"Σ cap(v) over of.dirty = %d over %d buffers; ADR 0003 §2: for an "+
						"openFile that FS.open holds, of.bytes equals the sum of cap(v) "+
						"over of.dirty whenever it is observed holding its mu",
						e.id, b, caps, len(e.of.dirty)))
				}
				sum += b
			}
			fs.mu.RUnlock()
			e.of.mu.Unlock()
		}
		if d := fs.DirtyBytes(); d != sum {
			problems = append(problems, fmt.Sprintf("DirtyBytes() = %d but Σ of.bytes over "+
				"FS.open = %d; ADR 0003 §2: when no accounting step is in flight, used() "+
				"equals the sum over FS.open alone, and §8: DirtyBytes() returns used()",
				d, sum))
		} else if d < 0 {
			problems = append(problems, fmt.Sprintf("DirtyBytes() = %d is negative", d))
		}
		return problems
	})
	if !ok {
		t.Fatalf("%s: checking the accounting has not returned after %v, so FS.mu or "+
			"an openFile.mu is held and never released", step, btHangBound)
	}
	for _, p := range problems {
		t.Errorf("%s: %s", step, p)
	}
}

// bpWantDirty requires DirtyBytes() to be want.
func bpWantDirty(t *testing.T, fs *FS, what string, want int64) {
	t.Helper()
	if got := fs.DirtyBytes(); got != want {
		t.Errorf("%s: DirtyBytes() = %d, want %d", what, got, want)
	}
}

// bpStep runs fn and requires it to change DirtyBytes() by want, then checks
// the accounting (ADR 0003 §2's table, row by row).
func bpStep(t *testing.T, fs *FS, what string, want int64, fn func()) {
	t.Helper()
	before := fs.DirtyBytes()
	fn()
	if got := fs.DirtyBytes() - before; got != want {
		t.Errorf("%s changed DirtyBytes() by %d, want %d (ADR 0003 §2, \"What each "+
			"operation does to DirtyBytes()\")", what, got, want)
	}
	bpCheckAccounting(t, fs, what)
}

// bpState describes an FS's budget for a failure message.
func bpState(fs *FS) func() string {
	return func() string {
		return fmt.Sprintf("DirtyBytes() = %d, waiters() = %d", fs.DirtyBytes(), fs.budget.waiters())
	}
}

// bpWaitEntered waits for a store call to be entered while c is still
// running.
func bpWaitEntered[T any](t *testing.T, what string, entered <-chan struct{}, c *bpCall[T]) {
	t.Helper()
	select {
	case <-entered:
	case <-c.done:
		t.Fatalf("%s did not happen: the call ended first, returning %v (panic: %v)",
			what, c.val, c.pval)
	case <-time.After(btHangBound):
		t.Fatalf("%s did not happen within %v", what, btHangBound)
	}
}

// bpWaitParked polls until c is parked in the budget.
func bpWaitParked[T any](t *testing.T, fs *FS, what string, c *bpCall[T]) {
	t.Helper()
	btEventually(t, what+" to park in the budget (waiters() == 1)",
		func() bool { return fs.budget.waiters() == 1 || c.finished() }, bpState(fs))
	if c.finished() {
		t.Fatalf("%s returned %v instead of parking; over the limit with another "+
			"call's drain in progress it must park (ADR 0003 §4, step 3.4)", what, c.val)
	}
}

// bpSameErr reports whether a holds the error value b itself.
func bpSameErr(a any, b error) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == any(b)
}

// bpWantErrorRecord requires exactly one Error record with §7's message whose
// err attribute is want itself.
func bpWantErrorRecord(t *testing.T, c *btCapture, want error, what string) {
	t.Helper()
	recs := c.withMessage(btMsgError)
	if len(recs) != 1 {
		t.Errorf("%s: captured %d %q records, want exactly 1 (ADR 0003 §7: once per "+
			"failed drain)", what, len(recs), btMsgError)
		return
	}
	btWantLevel(t, "the Error record", recs[0], slog.LevelError)
	v, ok := btAttr(recs[0], "err")
	if !ok {
		t.Errorf("%s: the Error record has no top-level attribute \"err\" (ADR 0003 §7)", what)
		return
	}
	if !bpSameErr(v.Any(), want) {
		t.Errorf("%s: the Error record's err attribute is %v, want the error value Write "+
			"returned, %v (ADR 0003 §7: \"err is the error drain returned\"; §4: Write "+
			"returns it \"not wrapped\")", what, v.Any(), want)
	}
}

// bpHook is how bpStore treats one kind of call. key, if set, limits it to one
// object key; otherwise it applies to every chunk key. entered, if set, is
// closed on the first call it applies to. gate, if set, holds the call until
// it is closed or the call's ctx ends. Once through the gate, the call panics
// with panicVal if it is set, fails with err if that is set, and otherwise
// passes through. panicVal and err are read when the gate opens, under the
// store's mutex, so a test can change them while a call is held. The first
// passFirst calls the hook applies to pass through after the gate whatever
// err and panicVal say, so a test can fail whichever call comes after them
// without knowing which object it is for. calls counts the calls the hook has
// applied to.
type bpHook struct {
	key       string
	entered   chan struct{}
	gate      <-chan struct{}
	err       error
	panicVal  any
	passFirst int
	closed    bool
	calls     int
}

// bpStore is a store.Store that intercepts Get and Put of chunk keys. Its hook
// state is guarded by mu.
type bpStore struct {
	store.Store
	mu  sync.Mutex
	get *bpHook
	put *bpHook
}

// setGet arms h for Get; nil disarms.
func (s *bpStore) setGet(h *bpHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.get = h
}

// setPut arms h for Put; nil disarms.
func (s *bpStore) setPut(h *bpHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.put = h
}

// update runs fn under the store's mutex, to change a hook.
func (s *bpStore) update(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

// callsOf reads h.calls under the store's mutex.
func (s *bpStore) callsOf(h *bpHook) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return h.calls
}

// run applies the hook pick returns to a call on key. handled is false when
// the call should pass through to the wrapped store.
func (s *bpStore) run(ctx context.Context, pick func() *bpHook, key string) (handled bool, err error) {
	s.mu.Lock()
	h := pick()
	if h == nil || (h.key != "" && key != h.key) || (h.key == "" && !bpIsChunkKey(key)) {
		s.mu.Unlock()
		return false, nil
	}
	if h.entered != nil && !h.closed {
		h.closed = true
		close(h.entered)
	}
	h.calls++
	n := h.calls
	gate := h.gate
	s.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}

	s.mu.Lock()
	injected, pv, pass := h.err, h.panicVal, n <= h.passFirst
	s.mu.Unlock()
	if pass {
		return false, nil
	}
	if pv != nil {
		panic(pv)
	}
	if injected != nil {
		return true, injected
	}
	return false, nil
}

func bpIsChunkKey(key string) bool {
	return len(key) > len(chunkPrefix) && key[:len(chunkPrefix)] == chunkPrefix
}

func (s *bpStore) Get(ctx context.Context, key string) ([]byte, error) {
	if handled, err := s.run(ctx, func() *bpHook { return s.get }, key); handled {
		return nil, err
	}
	return s.Store.Get(ctx, key)
}

func (s *bpStore) Put(ctx context.Context, key string, data []byte) error {
	if handled, err := s.run(ctx, func() *bpHook { return s.put }, key); handled {
		return err
	}
	return s.Store.Put(ctx, key, data)
}

// bpBlocked is an FS whose budget is at its limit of one chunk, with writer A
// running a drain that is held inside its chunk Put.
type bpBlocked struct {
	fs    *FS
	bps   *bpStore
	put   *bpHook
	gate  *btGate
	held  vfs.Handle
	a     *bpCall[bpWR]
	aData []byte
}

// bpBlockDrain builds the blocked state. setup runs straight after New, before
// anything else touches the FS. File "held.bin" is written with 100 bytes of
// content new to the bucket, which charges one chunk and so fills the budget.
// Chunk Put is then gated, and writer A writes 10 bytes at 200 of the same
// file: over the limit, A becomes the drainer, and its Sync reaches the gated
// Put. bpBlockDrain returns once that Put has been entered.
func bpBlockDrain(t *testing.T, lg *slog.Logger, setup func(fs *FS)) *bpBlocked {
	t.Helper()
	bps := &bpStore{Store: bpLocal(t)}
	fs := bpNew(t, bps, bpCS, lg)
	if setup != nil {
		setup(fs)
	}
	f, _ := bpCreate(t, fs, "held.bin", 0)
	bpMustWrite(t, fs, "the write that fills the budget (100 bytes at 0 of held.bin)", f, 0, bpUnique(100))
	if d := fs.DirtyBytes(); d < bpCS {
		t.Fatalf("after 100 bytes into a new index, DirtyBytes() = %d, want at least the "+
			"limit, %d: a Write to an index absent from of.dirty is +cs exactly (ADR 0003 §2)",
			d, bpCS)
	}
	b := &bpBlocked{fs: fs, bps: bps, gate: newBTGate(t), held: f, aData: bpUnique(10)}
	b.put = &bpHook{entered: make(chan struct{}), gate: b.gate.ch}
	bps.setPut(b.put)
	b.a = bpStartWrite(context.Background(), fs, testCaller, f, 200, b.aData, vfs.Unstable)
	bpWaitEntered(t, "writer A, over the limit, becoming the drainer and entering its chunk Put",
		b.put.entered, b.a)
	return b
}

// stillBlocked fails the test if A has already returned.
func (b *bpBlocked) stillBlocked(t *testing.T, when string) {
	t.Helper()
	if b.a.finished() {
		t.Fatalf("%s: writer A has returned (%v) although its drain's Put was still held",
			when, b.a.val)
	}
}

// release opens the gate and requires A's write to succeed.
func (b *bpBlocked) release(t *testing.T) {
	t.Helper()
	b.gate.open()
	r := b.a.wait(t, "writer A's Write, after its drain's Put was released")
	if r.err != nil || int(r.n) != len(b.aData) {
		t.Errorf("writer A's Write = %v, want (%d, UNSTABLE, nil): its drain succeeded, so "+
			"it is admitted by step 3.2 like anyone else (ADR 0003 §4)", r, len(b.aData))
	}
}

// TestBackpressureConfiguredLimit (F1) pins ADR 0003 §1: "New substitutes
// defaultMaxDirtyBytes for a MaxDirtyBytes of zero and passes every other
// value, positive or negative, to the budget unchanged. The budget's limit()
// (§4) therefore reports 256 MiB for zero and the configured value for
// anything else", and "const defaultMaxDirtyBytes = 256 << 20". A fresh FS,
// and a fresh mount of a bucket holding committed data, charge nothing (§8:
// DirtyBytes reports "the memory currently held by unflushed writes").
func TestBackpressureConfiguredLimit(t *testing.T) {
	if defaultMaxDirtyBytes != 256<<20 {
		t.Errorf("defaultMaxDirtyBytes = %d, want 256 << 20 (ADR 0003 §1)", int64(defaultMaxDirtyBytes))
	}
	st := bpLocal(t)
	for _, tc := range []struct{ cfg, want int64 }{
		{0, 256 << 20},
		{-1, -1},
		{-5, -5},
		{12345, 12345},
	} {
		fs := bpNew(t, st, tc.cfg, quietLog())
		if got := fs.budget.limit(); got != tc.want {
			t.Errorf("Config.MaxDirtyBytes %d: budget.limit() = %d, want %d", tc.cfg, got, tc.want)
		}
		if got := fs.budget.limit(); tc.cfg == 0 && got != int64(defaultMaxDirtyBytes) {
			t.Errorf("Config.MaxDirtyBytes 0: budget.limit() = %d, want defaultMaxDirtyBytes", got)
		}
		bpWantDirty(t, fs, fmt.Sprintf("a fresh FS with MaxDirtyBytes %d", tc.cfg), 0)
	}

	fs := bpNew(t, st, 0, quietLog())
	h, _ := bpCreate(t, fs, "committed.bin", 0)
	bpMustWrite(t, fs, "writing committed.bin", h, 0, bpUnique(3*bpCS))
	bpMustSync(t, fs, "Sync of committed.bin")
	fs2 := bpNew(t, st, 0, quietLog())
	bpLookup(t, fs2, "committed.bin")
	bpWantDirty(t, fs2, "a fresh mount of a bucket holding committed data", 0)
}

// TestBackpressureAccountingTable (F2) walks ADR 0003 §2's table, "What each
// operation does to DirtyBytes()", row by row, with MaxDirtyBytes -1 so that
// nothing drains ("Accounting runs even when backpressure is disabled", §8).
// After every step it asserts the invariant: "For an openFile that FS.open
// holds, of.bytes equals the sum of cap(v) over of.dirty whenever it is
// observed holding that openFile's mu", which §2 calls "directly assertable,
// and asserting it after each kind of operation is a better test than
// checking any single total". The truncate rows are reached as §2's closing
// list says.
func TestBackpressureAccountingTable(t *testing.T) {
	t.Run("writes, Sync and Commit", func(t *testing.T) {
		fs := bpNew(t, bpLocal(t), -1, quietLog())
		h, _ := bpCreate(t, fs, "rows.bin", 0)
		bpWantDirty(t, fs, "a freshly created file", 0)
		bpCheckAccounting(t, fs, "a freshly created file")

		bpStep(t, fs, "10 bytes at 0 of a new file (index 0 absent: +cs exactly)", bpCS, func() {
			bpMustWrite(t, fs, "Write of 10 bytes at 0", h, 0, bpUnique(10))
		})
		bpStep(t, fs, "10 bytes at 20 (index 0 already dirty, with room: 0)", 0, func() {
			bpMustWrite(t, fs, "Write of 10 bytes at 20", h, 20, bpUnique(10))
		})
		bpStep(t, fs, "100 bytes at 4050 (index 0 dirty, index 1 absent: +cs)", bpCS, func() {
			bpMustWrite(t, fs, "Write of 100 bytes at 4050", h, 4050, bpUnique(100))
		})
		bpStep(t, fs, "a whole chunk at 8192 (index 2 absent: +cs)", bpCS, func() {
			bpMustWrite(t, fs, "Write of 4096 bytes at 8192", h, 2*bpCS, bpUnique(bpCS))
		})

		bpMustSync(t, fs, "Sync of three dirty indices")
		bpWantDirty(t, fs, "after a successful Sync (every file on flushAll's list drops to 0)", 0)
		bpCheckAccounting(t, fs, "after a successful Sync")

		bpStep(t, fs, "10 bytes at 5 into a committed chunk (index 0 absent again: +cs)", bpCS, func() {
			bpMustWrite(t, fs, "Write of 10 bytes at 5", h, 5, bpUnique(10))
		})
		h2, _ := bpCreate(t, fs, "rows2.bin", 0)
		bpStep(t, fs, "10 bytes at 0 of a second file", bpCS, func() {
			bpMustWrite(t, fs, "Write of 10 bytes at 0 of rows2.bin", h2, 0, bpUnique(10))
		})
		err := bpDo(t, "Commit(h, 0, 0) with two files dirty", func() error {
			return fs.Commit(context.Background(), h, 0, 0)
		})
		if err != nil {
			t.Fatalf("Commit(h, 0, 0) = %v, want success", err)
		}
		bpWantDirty(t, fs, "after Commit(h, 0, 0) with two files dirty (Commit reaches "+
			"flushOpen for every open file, §3)", 0)
		bpCheckAccounting(t, fs, "after Commit")
	})

	t.Run("Remove and Rename over a victim", func(t *testing.T) {
		fs := bpNew(t, bpLocal(t), -1, quietLog())
		a, _ := bpCreate(t, fs, "a.bin", 0)
		bpMustWrite(t, fs, "10 bytes at 0 of a.bin", a, 0, bpUnique(10))
		bpMustWrite(t, fs, "10 bytes at 5000 of a.bin", a, 5000, bpUnique(10))
		b, _ := bpCreate(t, fs, "b.bin", 0)
		bpMustWrite(t, fs, "10 bytes at 0 of b.bin", b, 0, bpUnique(10))
		bpCheckAccounting(t, fs, "a.bin and b.bin dirty")

		bpStep(t, fs, "Remove of a.bin, charged two chunks (minus that file's whole charge)", -2*bpCS, func() {
			bpRemove(t, fs, "a.bin", "Remove a.bin")
		})

		c, _ := bpCreate(t, fs, "c.bin", 0)
		bpMustWrite(t, fs, "10 bytes at 0 of c.bin", c, 0, bpUnique(10))
		d, _ := bpCreate(t, fs, "d.bin", 0)
		bpMustWrite(t, fs, "10 bytes at 0 of d.bin", d, 0, bpUnique(10))
		bpMustWrite(t, fs, "10 bytes at 4096 of d.bin", d, bpCS, bpUnique(10))
		bpStep(t, fs, "Rename of c.bin over d.bin, the victim charged two chunks (minus the victim's charge only)", -2*bpCS, func() {
			err := bpDo(t, "Rename c.bin over d.bin", func() error {
				return fs.Rename(context.Background(), testCaller, fs.Root(), "c.bin", fs.Root(), "d.bin")
			})
			if err != nil {
				t.Fatalf("Rename c.bin over d.bin = %v, want success", err)
			}
		})
		bpWantDirty(t, fs, "b.bin and the renamed c.bin still dirty", 2*bpCS)
	})

	t.Run("truncate of dirty buffers", func(t *testing.T) {
		fs := bpNew(t, bpLocal(t), -1, quietLog())
		h, _ := bpCreate(t, fs, "trunc.bin", 0)
		bpStep(t, fs, "three whole chunks", 3*bpCS, func() {
			bpMustWrite(t, fs, "Write of three whole chunks", h, 0, bpUnique(3*bpCS))
		})
		bpStep(t, fs, "truncate to 6000, deleting dirty index 2 (−cap) and shortening dirty index 1 in place (0)", -bpCS, func() {
			bpTruncate(t, fs, h, 6000, "SetAttr(size 6000)")
		})
		bpStep(t, fs, "truncate to 5000, shortening the dirty tail in place (a reslice: 0)", 0, func() {
			bpTruncate(t, fs, h, 5000, "SetAttr(size 5000)")
		})
		bpStep(t, fs, "truncate to 20000, extending the file (0)", 0, func() {
			bpTruncate(t, fs, h, 20000, "SetAttr(size 20000)")
		})
	})

	t.Run("truncate into a cached chunk, then a write into it", func(t *testing.T) {
		st := bpLocal(t)
		fs := bpNew(t, st, -1, quietLog())
		const name = "cached.bin"
		h, attr := bpCreate(t, fs, name, 0)
		data := bpUnique(6000)
		bpMustWrite(t, fs, "6000 bytes of content new to the bucket", h, 0, data)
		bpMustSync(t, fs, "Sync uploading both chunks for the first time, which caches them "+
			"(ADR 0004 §5)")
		bpWantDirty(t, fs, "after that Sync", 0)

		truncWhat := "truncate to 5000, into chunk 1, which this FS's cache holds (0: a truncate " +
			"into a stored chunk that is not dirty, cached or not, adds nothing; ADR 0006 §3(d))"
		bpStep(t, fs, truncWhat, 0, func() {
			bpTruncate(t, fs, h, 5000, "SetAttr(size 5000), into cached chunk 1")
		})
		of := bpMustOpen(t, fs, attr.FileID, "after the truncate")
		s := bpSnapshot(t, of, "after the truncate")
		if _, dirty := s.caps[1]; dirty || s.pending != 1 {
			t.Errorf("after truncating into chunk 1, which this FS's cache holds, of.dirty holds "+
				"indices %v and len(of.pendingTrim) = %d, want index 1 absent and one pending "+
				"trim: truncate never reads the chunk cache and never stages a buffer in "+
				"of.dirty (ADR 0006 §3(b), (d); ADR 0003 §2's table)", s.indices(), s.pending)
		}

		w := bpUnique(2000)
		writeWhat := "2000 bytes at 5000, into index 1, absent from of.dirty with a pending trim " +
			"(+cs exactly, ADR 0003 §2's table)"
		bpStep(t, fs, writeWhat, bpCS, func() {
			bpMustWrite(t, fs, "Write of 2000 bytes at 5000", h, 5000, w)
		})
		if s := bpSnapshot(t, of, "after the write"); s.pending != 0 {
			t.Errorf("after a write into index 1, len(of.pendingTrim) = %d, want 0: a Write into "+
				"an index that has an entry removes the entry (ADR 0006 §4, §8)", s.pending)
		}

		want := append(append([]byte(nil), data[:5000]...), w...)
		bpWantFile(t, fs, h, want, "the file truncated into its cached chunk 1 and written at 5000")
		bpMustSync(t, fs, "Sync of the truncate and the write")
		bpWantDirty(t, fs, "after that Sync", 0)
		bpWantFile(t, fs, h, want, "the file truncated into its cached chunk 1 and written at 5000, "+
			"after a Sync")
		bpWantRemount(t, st, name, want, "the file truncated into its cached chunk 1 and written at 5000")
	})

	t.Run("truncate deferring to pendingTrim", func(t *testing.T) {
		st := bpLocal(t)
		const name = "trim.bin"
		data := bpSeedFile(t, st, name, 6000)
		fs := bpNew(t, st, -1, quietLog())
		h, attr := bpLookup(t, fs, name)
		bpStep(t, fs, "truncate to 5000, into a stored chunk that this fresh FS has not read (0 now)", 0, func() {
			bpTruncate(t, fs, h, 5000, "SetAttr(size 5000) on a fresh mount")
		})
		of := bpMustOpen(t, fs, attr.FileID, "after the deferred truncate")
		if s := bpSnapshot(t, of, "after the deferred truncate"); s.pending != 1 {
			t.Errorf("len(of.pendingTrim) = %d, want 1: a fresh FS whose cache is empty, "+
				"truncating into a stored chunk it has not read, defers to pendingTrim (ADR "+
				"0003 §2)", s.pending)
		}
		bpMustSync(t, fs, "Sync resolving the pending trim")
		bpWantDirty(t, fs, "after that Sync (0 at the next flush too: a flush applies the trim "+
			"through a copy it never charges, ADR 0006 §5)", 0)
		bpCheckAccounting(t, fs, "after the Sync that resolved the pending trim")
		bpWantFile(t, fs, h, data[:5000], "the truncated file on the FS that truncated it")
		bpWantRemount(t, st, name, data[:5000], "the truncated file")
	})

	t.Run("a write whose chunk fetch fails", func(t *testing.T) {
		st := bpLocal(t)
		const name = "fetch.bin"
		data := bpSeedFile(t, st, name, 2*bpCS)
		key1 := bpChunkKey(t, st, data[bpCS:])
		bps := &bpStore{Store: st}
		fs := bpNew(t, bps, -1, quietLog())
		h, attr := bpLookup(t, fs, name)
		errGet := errors.New("backpressure test: injected Get failure for chunk 1")
		bps.setGet(&bpHook{key: key1, err: errGet})

		bpStep(t, fs, "a 200-byte write at 4000 whose fetch of chunk 1 fails after index 0 is stored (the charge for the indices it stored)", bpCS, func() {
			r := bpWrite(t, fs, "Write of 200 bytes at 4000", h, 4000, bpUnique(200), vfs.Unstable)
			if r.n != 96 || r.how != vfs.Unstable || r.err != nil {
				t.Errorf("Write of 200 bytes at 4000, with chunk 1's fetch failing, = %v, want "+
					"(96, UNSTABLE, nil): the partial-write break keeps what it stored (ADR "+
					"0003 §2, site 1)", r)
			}
		})
		of := bpMustOpen(t, fs, attr.FileID, "after the partial write")
		if idx := bpSnapshot(t, of, "after the partial write").indices(); len(idx) != 1 || idx[0] != 0 {
			t.Errorf("after the partial write, of.dirty holds indices %v, want [0] only", idx)
		}

		bpStep(t, fs, "a 10-byte write at 5000 whose only fetch fails (stores nothing: 0)", 0, func() {
			r := bpWrite(t, fs, "Write of 10 bytes at 5000", h, 5000, bpUnique(10), vfs.Unstable)
			if r.n != 0 || r.how != vfs.Unstable || !errors.Is(r.err, errGet) {
				t.Errorf("Write of 10 bytes at 5000, whose only fetch fails, = %v, want (0, "+
					"UNSTABLE, the injected error) (ADR 0003 §2: \"a failed first fetch\" "+
					"stores nothing and charges nothing)", r)
			}
		})
	})

	t.Run("two truncates leave one pending trim", func(t *testing.T) {
		bpTwoTruncates(t)
	})
	t.Run("site 4, inode still there, pending-trim fetch fails", func(t *testing.T) {
		bpSite4Kept(t, true)
	})
	t.Run("site 4, inode still there, upload fails", func(t *testing.T) {
		bpSite4Kept(t, false)
	})
	t.Run("pending-trim fetch fails after a dirty index", func(t *testing.T) {
		bpTrimFetchFailsAfterDirty(t)
	})
}

// bpTwoTruncates pins ADR 0003 §2's row "truncate into a stored chunk that is
// not dirty, cached or not | 0, now and at the next flush", for two truncates
// of a stored three-chunk file on a fresh mount that has not read it: first
// into chunk 2, then into chunk 1. ADR 0006 §3(a) drops the entry at index 2,
// past the new end, and (b) makes one at index 1, so the file holds one
// pending trim (§2 I4; §8: "After any truncate a file has at most one
// entry"). Before ADR 0006 each truncate appended an entry and the file held
// two.
func bpTwoTruncates(t *testing.T) {
	t.Helper()
	st := bpLocal(t)
	const name = "two-truncates.bin"
	data := bpSeedFile(t, st, name, 3*bpCS)
	fs := bpNew(t, st, -1, quietLog())
	h, attr := bpLookup(t, fs, name)
	bpStep(t, fs, "truncate to 10000, into stored chunk 2, on a fresh mount (0)", 0, func() {
		bpTruncate(t, fs, h, 10000, "SetAttr(size 10000) on a fresh mount")
	})
	bpStep(t, fs, "truncate to 5000, into stored chunk 1 (0)", 0, func() {
		bpTruncate(t, fs, h, 5000, "SetAttr(size 5000) on a fresh mount")
	})
	of := bpMustOpen(t, fs, attr.FileID, "after the two truncates")
	if s := bpSnapshot(t, of, "after the two truncates"); s.pending != 1 || len(s.caps) != 0 {
		t.Errorf("after truncating an unread file on a fresh mount into chunk 2 and then into "+
			"chunk 1, len(of.pendingTrim) = %d and of.dirty holds indices %v, want 1 and none: "+
			"the second truncate drops the entry past its end and makes one at index 1 (ADR "+
			"0006 §3(a), (b); §2 I4), and truncate never stages (§3(d))", s.pending, s.indices())
	}
	bpWantDirty(t, fs, "after the two truncates", 0)

	bpWantFile(t, fs, h, data[:5000], "the twice-truncated file, before the Sync")
	bpMustSync(t, fs, "Sync applying the pending trim")
	bpWantDirty(t, fs, "after the Sync (a flush applies the trim through a copy it never "+
		"charges, ADR 0006 §5)", 0)
	bpCheckAccounting(t, fs, "after the Sync")
	bpWantFile(t, fs, h, data[:5000], "the twice-truncated file after the Sync")
	bpWantRemount(t, st, name, data[:5000], "the twice-truncated file after the Sync")
}

// bpTrimFetchFailsAfterDirty pins ADR 0003 §2's rows "Write to an index absent
// from of.dirty | +cs exactly" and "flushOpen failing while it fetches a
// pending trim's chunk, its inode still there | 0: a trim is never charged,
// and it stays pending", and site 4: "If the inode is still there, leave
// everything charged: those buffers really are held, and the file's next
// flush retries them" (Assumption 18); with ADR 0006 §5: "A fetch or an
// upload that fails repoints nothing, and leaves of.dirty and of.pendingTrim
// as they were". The file is truncated into chunk 1, which queues a trim, and
// then written at index 0, which makes that index dirty; only then is every
// chunk Get made to fail, so the flush's fetch of the trimmed chunk fails. The
// dirty index stays charged, and the trim stays pending. The order in which
// the flush uploads and fetches is not pinned (ADR 0006 §5), so the index-0
// upload may or may not have happened; either way nothing is repointed.
//
// It replaces a case with two pending trims, the first materialised and
// charged before the second fetch failed, which ADR 0006 made unreachable: a
// file has at most one entry (§2 I4), and a flush never charges one (§5).
func bpTrimFetchFailsAfterDirty(t *testing.T) {
	t.Helper()
	st := bpLocal(t)
	const name = "dirty-then-trim.bin"
	data := bpSeedFile(t, st, name, 2*bpCS)
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, -1, quietLog())
	h, attr := bpLookup(t, fs, name)
	bpStep(t, fs, "truncate to 5000 on a fresh mount, deferring to pendingTrim (0)", 0, func() {
		bpTruncate(t, fs, h, 5000, "SetAttr(size 5000) on a fresh mount")
	})
	w := bpUnique(10)
	bpStep(t, fs, "10 bytes at 100, into index 0, absent from of.dirty (+cs)", bpCS, func() {
		bpMustWrite(t, fs, "Write of 10 bytes at 100", h, 100, w)
	})
	of := bpMustOpen(t, fs, attr.FileID, "after the truncate and the write")

	injected := errors.New("backpressure test: injected failure of every chunk Get")
	get := &bpHook{err: injected}
	bps.setGet(get)
	if err := bpSync(t, fs, "Sync whose pending-trim fetch fails"); err == nil {
		t.Fatalf("Sync with every chunk Get failing = nil, want an error: the flush has a pending "+
			"trim of a chunk this FS has not read (it made %d chunk fetches)", bps.callsOf(get))
	}
	s := bpSnapshot(t, of, "after the failed Sync")
	if idx := s.indices(); len(idx) != 1 || idx[0] != 0 {
		t.Errorf("after a flush that failed fetching the pending trim's chunk, of.dirty holds "+
			"indices %v, want [0]: a failed fetch leaves of.dirty as it was (ADR 0006 §5)", idx)
	}
	if s.pending != 1 {
		t.Errorf("after a flush that failed fetching the pending trim's chunk, "+
			"len(of.pendingTrim) = %d, want 1: with the inode still there, a failed fetch leaves "+
			"of.pendingTrim as it was, and the file's next flush applies it (ADR 0006 §5)", s.pending)
	}
	bpWantDirty(t, fs, "after the failed Sync (index 0 stays charged, the trim is never charged)", bpCS)
	bpCheckAccounting(t, fs, "after the failed Sync")

	bps.setGet(nil)
	want := append([]byte(nil), data[:5000]...)
	copy(want[100:], w)
	bpWantFile(t, fs, h, want, "the truncated and written file, before the retried Sync")
	bpMustSync(t, fs, "Sync after the store recovered")
	bpWantDirty(t, fs, "after the retried flush succeeded", 0)
	bpCheckAccounting(t, fs, "after the retried flush")
	bpWantFile(t, fs, h, want, "the truncated and written file after the retried flush")
	bpWantRemount(t, st, name, want, "the truncated and written file after the retried flush")
}

// bpSite4Kept is F2's two site-4 rows for a file whose inode is still there,
// as ADR 0006 restated them: "flushOpen failing while it fetches a pending
// trim's chunk, its inode still there | 0: a trim is never charged, and it
// stays pending", and "flushOpen failing during upload, its inode still there
// | unchanged; trims stay pending"; with ADR 0006 §5: the flush's copy of the
// trimmed chunk "never enters of.dirty and is never charged", and "A fetch or
// an upload that fails repoints nothing, and leaves of.dirty and
// of.pendingTrim as they were, so the file's next flush applies the trims
// again". The file's only state is one pending trim, so either failure leaves
// DirtyBytes() unchanged at 0, of.dirty empty and the trim pending. Before
// ADR 0006 the upload case had materialised the trim into of.dirty and
// charged it.
//
// Nothing reads the file before the failing Sync, so that the flush has to
// fetch the trimmed chunk from the store rather than find it in the cache.
func bpSite4Kept(t *testing.T, failGet bool) {
	t.Helper()
	st := bpLocal(t)
	const name = "kept.bin"
	data := bpSeedFile(t, st, name, 2*bpCS)
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, -1, quietLog())
	h, attr := bpLookup(t, fs, name)
	bpStep(t, fs, "truncate to 5000 on a fresh mount, deferring to pendingTrim", 0, func() {
		bpTruncate(t, fs, h, 5000, "SetAttr(size 5000) on a fresh mount")
	})
	of := bpMustOpen(t, fs, attr.FileID, "after the deferred truncate")
	if s := bpSnapshot(t, of, "after the deferred truncate"); s.pending != 1 {
		t.Fatalf("len(of.pendingTrim) = %d, want 1 (ADR 0003 §2, \"Deferring to pendingTrim\")", s.pending)
	}

	injected := errors.New("backpressure test: injected store failure")
	failing, failed := "Put", "upload"
	if failGet {
		failing, failed = "Get", "fetch"
		bps.setGet(&bpHook{err: injected})
	} else {
		bps.setPut(&bpHook{err: injected})
	}
	before := fs.DirtyBytes()
	if err := bpSync(t, fs, "Sync with the store failing"); err == nil {
		t.Fatalf("Sync with every chunk %s failing = nil, want an error", failing)
	}
	s := bpSnapshot(t, of, "after the failed Sync")
	if got := fs.DirtyBytes() - before; got != 0 {
		t.Errorf("a flush whose %s of its only pending trim failed changed DirtyBytes() by %d, "+
			"want 0: a trim is never charged (ADR 0006 §5; ADR 0003 §2's table)", failed, got)
	}
	if len(s.caps) != 0 {
		t.Errorf("after a flush whose %s of its only pending trim failed, of.dirty holds indices "+
			"%v, want none: the flush's copy of the trimmed chunk never enters of.dirty (ADR 0006 "+
			"§5)", failed, s.indices())
	}
	if s.pending != 1 {
		t.Errorf("after a flush whose %s of its only pending trim failed, len(of.pendingTrim) = "+
			"%d, want 1: with the inode still there, a failed %s leaves of.pendingTrim as it was, "+
			"and the file's next flush applies it again (ADR 0006 §5)", failed, s.pending, failed)
	}
	bpCheckAccounting(t, fs, "after the failed Sync")

	bps.setGet(nil)
	bps.setPut(nil)
	bpWantFile(t, fs, h, data[:5000], "the truncated file after the failed flush, before the retry")
	bpMustSync(t, fs, "Sync after the store recovered")
	bpWantDirty(t, fs, "after the retried flush succeeded", 0)
	bpCheckAccounting(t, fs, "after the retried flush")
	bpWantFile(t, fs, h, data[:5000], "the truncated file after the retried flush")
	bpWantRemount(t, st, name, data[:5000], "the truncated file after the retried flush")
}

// TestBackpressureChunkListReadAfterWait (F3) pins ADR 0003 §4, "The chunk
// list is read after the wait, under openFile.mu ... never from a copy taken
// before the wait", and replays its trace with cs = MaxDirtyBytes = 4096: A×100
// at 0, then B×100 at 100, which drains before it is admitted. If the second
// write rebuilt chunk 0 from a stale list, "Chunk 0 is now 100 zero bytes
// followed by the Bs ... The As are gone". The variant is "With an older chunk
// in the stale list instead of none". Every call is bounded, because §3's rule
// that await is called holding no lock is what keeps the second write from
// hanging here.
func TestBackpressureChunkListReadAfterWait(t *testing.T) {
	as := bytes.Repeat([]byte{'A'}, 100)
	bs := bytes.Repeat([]byte{'B'}, 100)

	t.Run("empty file", func(t *testing.T) {
		st := bpLocal(t)
		c := newBTCapture(false)
		fs := bpNew(t, st, bpCS, c.logger())
		const name = "trace.bin"
		h, _ := bpCreate(t, fs, name, 0)
		bpMustWrite(t, fs, "step 1, Write of A×100 at 0", h, 0, as)
		bpMustWrite(t, fs, "step 2, Write of B×100 at 100, over the limit (a lock held "+
			"across the wait hangs here)", h, 100, bs)

		infos := c.withMessage(btMsgInfo)
		if len(infos) != 1 {
			t.Fatalf("captured %d %q records, want exactly 1: the second write found the "+
				"charge at the limit and drained (ADR 0003 §7)", len(infos), btMsgInfo)
		}
		btWantInt64(t, "the Info record", infos[0], "dirty_bytes", bpCS)
		btWantInt64(t, "the Info record", infos[0], "limit", bpCS)

		// The drain is FS.Sync: before this test syncs anything, a fresh mount
		// sees what it committed.
		bpWantRemount(t, st, name, as, "what the drain committed, before any Sync by the test")

		want := append(append([]byte(nil), as...), bs...)
		bpWantFile(t, fs, h, want, "this FS after the drain: A×100 then B×100 (the As lost "+
			"means the chunk list was read before the wait)")
		bpMustSync(t, fs, "Sync after the trace")
		bpWantRemount(t, st, name, want, "A×100 then B×100 after Sync")
	})

	t.Run("older chunk", func(t *testing.T) {
		st := bpLocal(t)
		c := newBTCapture(false)
		fs := bpNew(t, st, bpCS, c.logger())
		const name = "trace-older.bin"
		h, _ := bpCreate(t, fs, name, 0)
		xs := bytes.Repeat([]byte{'X'}, bpCS)
		bpMustWrite(t, fs, "Write of X×4096 at 0", h, 0, xs)
		bpMustSync(t, fs, "Sync committing X×4096")
		bpMustWrite(t, fs, "Write of A×100 at 0 over the committed chunk", h, 0, as)
		bpMustWrite(t, fs, "Write of B×100 at 200, over the limit (a lock held across the "+
			"wait hangs here)", h, 200, bs)
		if n := len(c.withMessage(btMsgInfo)); n != 1 {
			t.Fatalf("captured %d %q records, want exactly 1: the third write drained", n, btMsgInfo)
		}

		drained := append([]byte(nil), xs...)
		copy(drained, as)
		bpWantRemount(t, st, name, drained, "what the drain committed: X with A at [0,100)")

		want := append([]byte(nil), drained...)
		copy(want[200:], bs)
		bpWantFile(t, fs, h, want, "this FS after the drain: X with [0,100) = A and "+
			"[200,300) = B (X at [0,100) means the write rebuilt from the older chunk)")
		bpMustSync(t, fs, "Sync after the trace")
		bpWantRemount(t, st, name, want, "X with A and B after Sync")
	})
}

// bpOneWriter writes 64 KiB of wsPattern data to one file as sequential
// 1000-byte writes. With limit > 0 it checks ADR 0003 §6 after each call: "A
// single writer issuing calls of at most cs bytes spans at most two indices
// and therefore sees at most MaxDirtyBytes + 2×cs".
func bpOneWriter(t *testing.T, limit int64) (*FS, *btCapture, vfs.Handle, []byte) {
	t.Helper()
	c := newBTCapture(false)
	fs := bpNew(t, bpLocal(t), limit, c.logger())
	h, _ := bpCreate(t, fs, "one-writer.bin", 0)
	want := wsPattern(64<<10, 'W')
	for off := 0; off < len(want); off += 1000 {
		end := min(off+1000, len(want))
		bpMustWrite(t, fs, fmt.Sprintf("Write of [%d,%d)", off, end), h, uint64(off), want[off:end])
		if limit > 0 {
			if d := fs.DirtyBytes(); d > limit+2*bpCS {
				t.Errorf("after the write of [%d,%d), DirtyBytes() = %d, want at most "+
					"MaxDirtyBytes + 2×cs = %d (ADR 0003 §6)", off, end, d, limit+2*bpCS)
			}
		}
	}
	return fs, c, h, want
}

// TestBackpressureOneWriterBound (F4) pins ADR 0003 §6 for one writer: "For a
// single writer, with one Write in flight at a time and so k = 1, that means
// DirtyBytes() <= MaxDirtyBytes + W after each call returns", with a limit of
// three chunks. At least one drain must have run (§7's Info record), the data
// must be intact, and a Sync takes the charge to zero (§2's table).
func TestBackpressureOneWriterBound(t *testing.T) {
	fs, c, h, want := bpOneWriter(t, 3*bpCS)
	if n := len(c.withMessage(btMsgInfo)); n == 0 {
		t.Errorf("64 KiB through a limit of %d logged no %q record: nothing drained", 3*bpCS, btMsgInfo)
	}
	bpWantFile(t, fs, h, want, "64 KiB written through a three-chunk budget")
	bpMustSync(t, fs, "Sync after the writes")
	bpWantDirty(t, fs, "after Sync", 0)
}

// TestBackpressureAccountingWhenDisabled (F5) pins ADR 0003 §8: "Accounting
// runs even when backpressure is disabled (MaxDirtyBytes < 0), so DirtyBytes()
// is meaningful either way and a test can show that the bound is caused by the
// feature rather than by incidental flushing". The same writes as F4 hold 16
// chunks, and nothing drains.
func TestBackpressureAccountingWhenDisabled(t *testing.T) {
	fs, c, h, want := bpOneWriter(t, -1)
	bpWantDirty(t, fs, "64 KiB written with backpressure disabled (16 chunk indices)", 16*bpCS)
	if n := len(c.withMessage(btMsgInfo)); n != 0 {
		t.Errorf("with MaxDirtyBytes -1, captured %d %q records, want 0: limit <= 0 "+
			"disables waiting (ADR 0003 §4)", n, btMsgInfo)
	}
	bpWantFile(t, fs, h, want, "64 KiB written with backpressure disabled")
	bpMustSync(t, fs, "Sync after the writes")
	bpWantDirty(t, fs, "after Sync", 0)
}

// bpWaitProgress waits for done, failing the test if progress stops advancing
// for btHangBound.
func bpWaitProgress(t *testing.T, what string, done <-chan struct{}, progress *atomic.Int64) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	last, lastAt := progress.Load(), time.Now()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			if p := progress.Load(); p != last {
				last, lastAt = p, time.Now()
			} else if time.Since(lastAt) > btHangBound {
				t.Fatalf("%s: none has returned for %v (%d returned so far), so one is hung",
					what, btHangBound, p)
			}
		}
	}
}

// TestBackpressureBoundAtEveryInstant (F6) pins ADR 0003 §6, "At every instant
// — not only between calls — DirtyBytes() <= MaxDirtyBytes + k × W", with k =
// 4 writers and W = 2×cs for 1000-byte calls, and §2's whole-budget statement,
// "At every instant, used() >= Σ of.bytes >= 0", whose visible half is that
// DirtyBytes() is never negative. A monitor samples DirtyBytes() while four
// goroutines write their own files. The files are read back and synced only
// after every goroutine has stopped.
func TestBackpressureBoundAtEveryInstant(t *testing.T) {
	const (
		writers   = 4
		perWriter = 200
		piece     = 1000
	)
	limit := int64(4 * bpCS)
	bound := limit + writers*2*bpCS
	fs := bpNew(t, bpLocal(t), limit, quietLog())
	hs := make([]vfs.Handle, writers)
	wants := make([][]byte, writers)
	for i := range hs {
		hs[i], _ = bpCreate(t, fs, fmt.Sprintf("writer-%d.bin", i), 0)
		wants[i] = wsPattern(perWriter*piece, byte('a'+i))
	}

	var (
		mu       sync.Mutex
		failures []string
		progress atomic.Int64
	)
	fail := func(format string, args ...any) {
		mu.Lock()
		failures = append(failures, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	type stats struct {
		n      int
		lo, hi int64
	}
	stop := make(chan struct{})
	monitor := make(chan stats, 1)
	go func() {
		s := stats{lo: math.MaxInt64, hi: math.MinInt64}
		for {
			select {
			case <-stop:
				monitor <- s
				return
			default:
			}
			d := fs.DirtyBytes()
			s.n++
			s.lo = min(s.lo, d)
			s.hi = max(s.hi, d)
			runtime.Gosched()
		}
	}()

	begin := make(chan struct{})
	allDone := make(chan struct{})
	var wg sync.WaitGroup
	for i := range hs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-begin
			for k := 0; k < perWriter; k++ {
				off := k * piece
				data := wants[i][off : off+piece]
				n, _, err := fs.Write(context.Background(), testCaller, hs[i], uint64(off), data, vfs.Unstable)
				if err != nil {
					fail("writer %d, Write at %d = %v; it must succeed", i, off, err)
					return
				}
				if n != piece {
					fail("writer %d, Write at %d wrote %d of %d bytes", i, off, n, piece)
					return
				}
				progress.Add(1)
			}
		}(i)
	}
	go func() {
		wg.Wait()
		close(allDone)
	}()
	close(begin)
	bpWaitProgress(t, "the four writers' Write calls", allDone, &progress)
	close(stop)
	var s stats
	select {
	case s = <-monitor:
	case <-time.After(btHangBound):
		t.Fatalf("the DirtyBytes() monitor has not stopped after %v", btHangBound)
	}

	mu.Lock()
	errs := append([]string(nil), failures...)
	mu.Unlock()
	for _, e := range errs {
		t.Error(e)
	}
	if s.n == 0 {
		t.Fatalf("the monitor took no samples")
	}
	if s.lo < 0 {
		t.Errorf("DirtyBytes() was sampled at %d; ADR 0003 §2: at every instant used() >= "+
			"Σ of.bytes >= 0", s.lo)
	}
	if s.hi > bound {
		t.Errorf("DirtyBytes() was sampled at %d, over MaxDirtyBytes + k × W = %d + %d × %d "+
			"= %d (ADR 0003 §6, at every instant)", s.hi, limit, writers, 2*bpCS, bound)
	}
	for i := range hs {
		bpWantFile(t, fs, hs[i], wants[i], fmt.Sprintf("writer %d's file", i))
	}
	bpMustSync(t, fs, "Sync after the writers stopped")
	bpWantDirty(t, fs, "after Sync", 0)
}

// TestBackpressureWriteRacingRemoveNetsZero (F7) is a stress test of sites 1,
// 2 and 5 racing. ADR 0003 §2's table: "Write whose inode goes while it
// buffers ... 0 net: site 2 releases the file's whole remaining charge, this
// write's included", and site 5: "Without this a write racing an unlink of
// the same file leaks budget permanently". However they interleave, once the
// Write and the Remove have both returned the file holds no charge. A correct
// implementation cannot fail this.
func TestBackpressureWriteRacingRemoveNetsZero(t *testing.T) {
	const iters = 50
	fs := bpNew(t, bpLocal(t), -1, quietLog())
	for i := 0; i < iters; i++ {
		name := fmt.Sprintf("race-%02d.bin", i)
		h, _ := bpCreate(t, fs, name, 0)
		bpMustWrite(t, fs, fmt.Sprintf("iteration %d, one chunk at 0", i), h, 0,
			bytes.Repeat([]byte{byte(i + 1)}, bpCS))

		begin := make(chan struct{})
		data := bpUnique(100)
		w := bpGo(func() bpWR {
			<-begin
			n, how, err := fs.Write(context.Background(), testCaller, h, bpCS, data, vfs.Unstable)
			return bpWR{n, how, err}
		})
		rm := bpGo(func() error {
			<-begin
			return fs.Remove(context.Background(), testCaller, fs.Root(), name)
		})
		close(begin)
		r := w.wait(t, fmt.Sprintf("iteration %d, the Write racing Remove", i))
		if err := rm.wait(t, fmt.Sprintf("iteration %d, the Remove racing a Write", i)); err != nil {
			t.Fatalf("iteration %d: Remove = %v, want success", i, err)
		}
		switch {
		case r.err == nil:
			if r.n != 100 {
				t.Fatalf("iteration %d: the racing Write = %v, want its full length (ADR 0003 "+
					"§4, \"If the inode goes away\")", i, r)
			}
		case errors.Is(r.err, vfs.ErrStale):
		default:
			t.Fatalf("iteration %d: the racing Write = %v, want success or ErrStale", i, r)
		}
		if d := fs.DirtyBytes(); d != 0 {
			t.Fatalf("iteration %d: after a Write racing Remove of the file's last name, "+
				"DirtyBytes() = %d, want 0; sites 1 and 2 net to zero, and site 5 releases "+
				"the rest (ADR 0003 §2)", i, d)
		}
	}
}

// TestBackpressureParkedWriteToRemovedFile (F8) pins ADR 0003 §4, "Admission
// while a drain runs": "a parked call also wakes when ... an unlink frees
// space ... A woken call that finds room is admitted there and then, usually
// while the drain that made the room is still running", and "If the inode goes
// away": "Gone when bufferWrite first resolves it, after the wait: the write
// reports its full length and success and buffers nothing". It also exercises
// "getOpen can recreate an openFile entry for an inode unlinked between
// Write's checks and the call (#42). The entry is empty and never charged".
func TestBackpressureParkedWriteToRemovedFile(t *testing.T) {
	limit := int64(2 * bpCS)
	bps := &bpStore{Store: bpLocal(t)}
	fs := bpNew(t, bps, limit, quietLog())
	const fname = "f.bin"
	f, _ := bpCreate(t, fs, fname, 0)
	e, _ := bpCreate(t, fs, "e.bin", 0)
	bpMustWrite(t, fs, "10 bytes at 0 of F", f, 0, bpUnique(10))
	bpMustWrite(t, fs, "10 bytes at 4096 of F", f, bpCS, bpUnique(10))
	if d := fs.DirtyBytes(); d != limit {
		t.Fatalf("F holds DirtyBytes() = %d, want %d", d, limit)
	}

	gate := newBTGate(t)
	put := &bpHook{entered: make(chan struct{}), gate: gate.ch}
	bps.setPut(put)
	aData := bpUnique(10)
	a := bpStartWrite(context.Background(), fs, testCaller, e, 0, aData, vfs.Unstable)
	bpWaitEntered(t, "writer A, over the limit, becoming the drainer and entering its chunk Put", put.entered, a)

	bData := bpUnique(10)
	bw := bpStartWrite(context.Background(), fs, testCaller, f, 200, bData, vfs.Unstable)
	bpWaitParked(t, fs, "writer B's Write to F", bw)

	bpRemove(t, fs, fname, "Remove of F while B is parked and A's drain is held in Put")
	r := bw.wait(t, "writer B's Write to F, after F was removed and its charge released")
	if r.err != nil || int(r.n) != len(bData) || r.how != vfs.Unstable {
		t.Errorf("writer B's Write to the removed F = %v, want (%d, UNSTABLE, nil): gone "+
			"when bufferWrite first resolves it, the write reports its full length and "+
			"success (ADR 0003 §4)", r, len(bData))
	}
	if a.finished() {
		t.Fatalf("writer A returned (%v) while its drain's Put was still held, so B was not "+
			"shown to be admitted while the drain ran", a.val)
	}
	bpWantDirty(t, fs, "after F's removal released its charge and B buffered nothing", 0)

	gate.open()
	ra := a.wait(t, "writer A's Write, after its drain's Put was released")
	if ra.err != nil || int(ra.n) != len(aData) {
		t.Errorf("writer A's Write = %v, want (%d, UNSTABLE, nil)", ra, len(aData))
	}
	bpCheckAccounting(t, fs, "after A returned")
}

// bpParkedGivesUp is F9 with the parked call's ctx made done by cancel or by
// its own deadline.
func bpParkedGivesUp(t *testing.T, newCtx func() (context.Context, context.CancelFunc), byCancel bool, want error) {
	t.Helper()
	var g vfs.Handle
	b := bpBlockDrain(t, quietLog(), func(fs *FS) {
		g, _ = bpCreate(t, fs, "g.bin", 0)
	})
	fs := b.fs
	before := bpGetAttr(t, fs, g, "GetAttr of G before B's write")
	openBefore := bpOpen(t, fs, before.FileID, "FS.open before B's write")
	dirtyBefore := fs.DirtyBytes()

	ctx, cancel := newCtx()
	defer cancel()
	bw := bpStartWrite(ctx, fs, testCaller, g, 0, bpUnique(10), vfs.Unstable)
	btEventually(t, "writer B to park in the budget (waiters() == 1)",
		func() bool { return fs.budget.waiters() == 1 || bw.finished() }, bpState(fs))
	if bw.finished() {
		if byCancel {
			t.Fatalf("writer B returned %v before its ctx was cancelled, while A's drain was "+
				"held; over the limit with a drain in progress, B must park (ADR 0003 §4)", bw.val)
		}
		t.Logf("B's deadline passed before B was seen parked, so this run did not exercise " +
			"waking a parked call; the result is still checked")
	}
	if byCancel {
		cancel()
	}

	r := bw.wait(t, "writer B's Write, after its ctx was done")
	if r.n != 0 || r.how != vfs.Unstable || r.err != want {
		t.Errorf("writer B's Write = %v, want (0, UNSTABLE, %v) with the context error "+
			"itself, not wrapped (ADR 0003 §4, \"What Write returns when the wait fails\": "+
			"Write returns (0, 0, err) ... not wrapped)", r, want)
	}
	b.stillBlocked(t, "after B returned")

	after := bpGetAttr(t, fs, g, "GetAttr of G after B's failed write")
	if after.Size != before.Size || !after.MTime.Equal(before.MTime) {
		t.Errorf("G's size and mtime went from (%d, %v) to (%d, %v), want unchanged: a write "+
			"whose wait fails \"changes neither size nor mtime\" (ADR 0003 §4)",
			before.Size, before.MTime, after.Size, after.MTime)
	}
	openAfter := bpOpen(t, fs, before.FileID, "FS.open after B's failed write")
	switch {
	case openBefore == nil && openAfter != nil:
		t.Errorf("FS.open has an entry for G after B's failed write, want none: \"the call " +
			"creates no openFile, because the wait comes before getOpen\" (ADR 0003 §4)")
	case openBefore != nil && openAfter != openBefore:
		t.Errorf("G's openFile entry was replaced by B's failed write")
	}
	bpWantDirty(t, fs, "after B's failed wait (it adds no charge)", dirtyBefore)
	if w := fs.budget.waiters(); w != 0 {
		t.Errorf("waiters() = %d after B returned, want 0", w)
	}
	b.release(t)
}

// TestBackpressureParkedWriteGivesUp (F9) pins ADR 0003 §4, "Waking on
// cancellation": "A parked call returns ctx.Err() promptly once its ctx is
// done, whether or not any drain ends, as vfs.FS.Write's contract requires",
// and "What Write returns when the wait fails": "Write returns (0, 0, err)
// with that err, not wrapped ... Nothing is touched: the call creates no
// openFile ...; it buffers nothing; it changes neither size nor mtime; and it
// adds no charge". vfs.FS.Write: "an implementation that delays must return
// promptly once ctx is done".
func TestBackpressureParkedWriteGivesUp(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		bpParkedGivesUp(t, func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}, true, context.Canceled)
	})
	t.Run("deadline", func(t *testing.T) {
		bpParkedGivesUp(t, func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), btDeadlineAfter)
		}, false, context.DeadlineExceeded)
	})
}

// TestBackpressurePathsThatNeverWait (F10) pins ADR 0003 §4, "Paths that never
// wait. These return before the wait point, whatever the budget holds": a
// zero-length write, which returns (0, vfs.FileSync, nil); "a bad or stale
// handle"; "a directory, vfs.ErrIsDir"; and "EACCES". Each runs while the
// budget is at its limit and a drain is held, so a call that waited would
// park there and hang.
func TestBackpressurePathsThatNeverWait(t *testing.T) {
	var plain, private, gone vfs.Handle
	b := bpBlockDrain(t, quietLog(), func(fs *FS) {
		plain, _ = bpCreate(t, fs, "plain.bin", 0)
		private, _ = bpCreate(t, fs, "private.bin", 0o600)
		gone, _ = bpCreate(t, fs, "gone.bin", 0)
		bpRemove(t, fs, "gone.bin", "Remove gone.bin")
	})
	fs := b.fs
	cases := []struct {
		name   string
		caller vfs.Caller
		h      vfs.Handle
		data   []byte
		want   error
	}{
		{"a zero-length write", testCaller, plain, []byte{}, nil},
		{"a write by a caller that the file's mode 0600 excludes", vfs.Caller{UID: 999, GID: 999}, private, bpUnique(10), vfs.ErrAcces},
		{"a write through a removed file's handle", testCaller, gone, bpUnique(10), vfs.ErrStale},
		{"a write through a malformed handle", testCaller, vfs.Handle{1, 2, 3}, bpUnique(10), vfs.ErrBadHandle},
		{"a write to the root directory", testCaller, fs.Root(), bpUnique(10), vfs.ErrIsDir},
	}
	for _, tc := range cases {
		dirty := fs.DirtyBytes()
		what := tc.name + ", with the budget at its limit and a drain held (ADR 0003 §4 " +
			"lists it among the paths that return before the wait point, so a hang means it parked)"
		r := bpStartWrite(context.Background(), fs, tc.caller, tc.h, 0, tc.data, vfs.Unstable).wait(t, what)
		if tc.want == nil {
			if r.n != 0 || r.how != vfs.FileSync || r.err != nil {
				t.Errorf("%s = %v, want (0, FILE_SYNC, nil) (ADR 0003 §4)", tc.name, r)
			}
		} else if r.n != 0 || !errors.Is(r.err, tc.want) {
			t.Errorf("%s = %v, want (0, _, %v) (ADR 0003 §4)", tc.name, r, tc.want)
		}
		if w := fs.budget.waiters(); w != 0 {
			t.Errorf("%s: waiters() = %d afterwards, want 0", tc.name, w)
		}
		bpWantDirty(t, fs, tc.name+" (charges nothing)", dirty)
	}
	b.stillBlocked(t, "after the paths that never wait")
	b.release(t)
}

// TestBackpressureTruncateNeverChargesOrWaits (F11) pins ADR 0003 §2 site 3,
// "subtract cap for every map entry it deletes, and add nothing ... It never
// waits", and its table's row "truncate into a stored chunk that is not dirty,
// cached or not | 0"; with ADR 0006 §3(d), truncate "never reads the chunk
// cache, never stages a buffer in of.dirty, never charges the budget and never
// waits", and §7, "truncate does not wait on the budget, because it adds
// nothing for a wait to bound". With the budget at its limit and a drain held,
// a truncate of T into its chunk 1, which this FS's cache holds, returns
// without parking, leaves DirtyBytes() unchanged, and queues a trim at index 1
// without making it dirty. Before ADR 0006 it staged the cached chunk in
// of.dirty and charged it without waiting.
func TestBackpressureTruncateNeverChargesOrWaits(t *testing.T) {
	var (
		th    vfs.Handle
		tid   uint64
		tdata []byte
	)
	b := bpBlockDrain(t, quietLog(), func(fs *FS) {
		var attr vfs.Attr
		th, attr = bpCreate(t, fs, "t.bin", 0)
		tid = attr.FileID
		tdata = bpUnique(6000)
		bpMustWrite(t, fs, "6000 bytes of T, new to the bucket", th, 0, tdata)
		bpMustSync(t, fs, "Sync of T, which uploads and caches its chunks")
	})
	fs := b.fs
	before := fs.DirtyBytes()
	bpTruncate(t, fs, th, 5000, "SetAttr(T, size 5000), into its cached chunk 1, with the budget "+
		"at its limit and a drain held (a hang here means truncate waited on the budget, ADR 0006 "+
		"§3(d), §7)")
	bpWantDirty(t, fs, "after truncating T into its cached chunk 1 with the budget at its limit "+
		"(ADR 0006 §3(d): truncate never charges the budget)", before)
	if w := fs.budget.waiters(); w != 0 {
		t.Errorf("waiters() = %d after the truncate, want 0: truncate never waits", w)
	}
	s := bpSnapshot(t, bpMustOpen(t, fs, tid, "T after the truncate"), "T after the truncate")
	if _, dirty := s.caps[1]; dirty || s.pending != 1 {
		t.Errorf("after truncating T into its cached chunk 1, of.dirty holds indices %v and "+
			"len(of.pendingTrim) = %d, want index 1 absent and one pending trim: truncate never "+
			"reads the chunk cache and never stages a buffer in of.dirty (ADR 0006 §3(b), (d))",
			s.indices(), s.pending)
	}
	b.stillBlocked(t, "after the truncate")
	b.release(t)

	want := tdata[:5000]
	bpWantFile(t, fs, th, want, "T after the truncate, before the Sync")
	bpMustSync(t, fs, "Sync after the drain")
	bpWantFile(t, fs, th, want, "T after the truncate and a Sync")
	st, ok := b.bps.Store.(*store.Local)
	if !ok {
		t.Fatalf("bpBlockDrain's store wraps a %T, want *store.Local", b.bps.Store)
	}
	bpWantRemount(t, st, "t.bin", want, "T after the truncate and a Sync")
}

// bpReadOnlyStore is F12 with the second write sent as how.
func bpReadOnlyStore(t *testing.T, how vfs.Stability) {
	t.Helper()
	_, st, _ := newTestFS(t)
	c := newBTCapture(false)
	fs := bpNew(t, store.ReadOnly{Store: st}, bpCS, c.logger())
	h, _ := bpCreate(t, fs, "ro.bin", 0)
	bpMustWrite(t, fs, "write 1, 100 bytes of new content at 0", h, 0, bpUnique(100))
	r := bpWrite(t, fs, "write 2, "+bpHow(how)+" at 200, over the limit on a read-only store",
		h, 200, bpUnique(10), how)
	if r.n != 0 || r.how != vfs.Unstable || r.err == nil {
		t.Errorf("write 2 = %v, want (0, UNSTABLE, an error): Write returns (0, 0, err) when "+
			"the wait fails, and \"A stable write whose wait fails returns the same way, "+
			"without syncing\" (ADR 0003 §4)", r)
	}
	if !errors.Is(r.err, vfs.ErrROFS) {
		t.Errorf("write 2's error = %v, want one that unwraps to vfs.ErrROFS: \"A read-only "+
			"store reports NFS3ERR_ROFS, when the drain has a chunk to upload\", and write 1's "+
			"content was new to the bucket (ADR 0003 §5)", r.err)
	}
	if attr := bpGetAttr(t, fs, h, "GetAttr after write 2"); attr.Size != 100 {
		t.Errorf("size after the failed write 2 = %d, want 100: it changes neither size nor "+
			"mtime (ADR 0003 §4)", attr.Size)
	}
	bpWantDirty(t, fs, "after the failed drain (the file's inode is still there, so its "+
		"buffer stays charged)", bpCS)
	if r.err != nil {
		bpWantErrorRecord(t, c, r.err, "a drain failing on a read-only store")
	}
}

// TestBackpressureReadOnlyStoreFailsTheWait (F12) pins ADR 0003 §5, "A
// read-only store reports NFS3ERR_ROFS, when the drain has a chunk to upload.
// putChunk returns vfs.ErrROFS; it propagates through flushOpen → flushAll →
// Sync's wrapping fmt.Errorf ... and survives the unwrap", §4's "What Write
// returns when the wait fails", including "A stable write whose wait fails
// returns the same way, without syncing", and §7's Error record, "err is the
// error drain returned". The buffered content is new to the bucket, so the
// drain has a chunk to upload; TestBackpressureReadOnlyStoreSkippedUpload
// covers the case where it has none.
func TestBackpressureReadOnlyStoreFailsTheWait(t *testing.T) {
	t.Run("UNSTABLE", func(t *testing.T) { bpReadOnlyStore(t, vfs.Unstable) })
	t.Run("FILE_SYNC", func(t *testing.T) { bpReadOnlyStore(t, vfs.FileSync) })
}

// TestBackpressureReadOnlyStoreSkippedUpload pins ADR 0003 §5's condition on
// the read-only case: "putChunk uploads only content that is new: it skips a
// chunk whose hash this FS has already stored or fetched, or that the HEAD
// before the upload finds in the bucket ... When every chunk the drain flushes
// is skipped that way, flushAll succeeds and the commit's snapshot Put fails
// instead, with store.ErrUnsupported wrapped by fmt.Errorf ... rather than
// mapped to vfs.ErrROFS as putChunk maps it. With no vfs.Status in the chain,
// the client gets NFS3ERR_IO." A writable mount commits a file; a read-only
// mount of the same bucket then writes identical content into a new file, so
// the drain's only chunk is found by its HEAD and not uploaded. Because
// flushAll succeeds, the flushed file stays released (§2's row "Sync failing
// after flushAll succeeded").
func TestBackpressureReadOnlyStoreSkippedUpload(t *testing.T) {
	st := bpLocal(t)
	content := bpSeedFile(t, st, "seed.bin", 100)
	c := newBTCapture(false)
	fs := bpNew(t, store.ReadOnly{Store: st}, bpCS, c.logger())
	h, _ := bpCreate(t, fs, "copy.bin", 0)
	bpMustWrite(t, fs, "write 1, 100 bytes identical to a chunk already in the bucket", h, 0, content)
	r := bpWrite(t, fs, "write 2, over the limit on a read-only store whose drain has no chunk "+
		"to upload", h, 200, bpUnique(10), vfs.Unstable)
	if r.n != 0 || r.how != vfs.Unstable || r.err == nil {
		t.Fatalf("write 2 = %v, want (0, UNSTABLE, an error): Write returns (0, 0, err) when "+
			"the wait fails (ADR 0003 §4)", r)
	}
	var s vfs.Status
	if errors.As(r.err, &s) {
		t.Errorf("write 2's error = %v, which unwraps to vfs.Status %d; ADR 0003 §5: when every "+
			"chunk the drain flushes is already in the bucket, the snapshot Put fails with "+
			"store.ErrUnsupported wrapped by fmt.Errorf, and with no vfs.Status in the chain "+
			"the client gets NFS3ERR_IO (a vfs.ErrROFS here means the drain tried to upload a "+
			"chunk the bucket already holds)", r.err, uint32(s))
	}
	if !errors.Is(r.err, store.ErrUnsupported) {
		t.Errorf("write 2's error = %v, want one that unwraps to store.ErrUnsupported: the "+
			"commit's snapshot Put fails \"with store.ErrUnsupported wrapped by fmt.Errorf\" "+
			"(ADR 0003 §5)", r.err)
	}
	if attr := bpGetAttr(t, fs, h, "GetAttr after write 2"); attr.Size != 100 {
		t.Errorf("size after the failed write 2 = %d, want 100: it changes neither size nor "+
			"mtime (ADR 0003 §4)", attr.Size)
	}
	bpWantDirty(t, fs, "after a drain whose flushAll succeeded and whose commit failed (the "+
		"flushed file stays released)", 0)
	bpWantErrorRecord(t, c, r.err, "a drain whose snapshot Put failed on a read-only store")
}

// TestBackpressureDivergedDrain (F13) pins ADR 0003 §5, "A diverged filesystem
// reports NFS3ERR_IO, not NFS3ERR_STALE. Both divergence paths return a bare
// error rather than a vfs.Status ... The write whose drain discovers
// divergence therefore gets NFS3ERR_IO; the next write gets NFS3ERR_STALE,
// from f.mutable()", and §2's row "Sync failing after flushAll succeeded — a
// commit error, divergence included | the flushed files stay released".
func TestBackpressureDivergedDrain(t *testing.T) {
	fsA, st, _ := newTestFS(t)
	c := newBTCapture(false)
	fsB := bpNew(t, st, bpCS, c.logger())
	h, _ := bpCreate(t, fsB, "diverge.bin", 0)
	bpCreate(t, fsA, "from-a.bin", 0)
	bpMustSync(t, fsA, "fsA's Sync, which moves the root pointer out from under fsB")

	bpMustWrite(t, fsB, "fsB's first write", h, 0, bpUnique(100))
	r := bpWrite(t, fsB, "fsB's second write, whose drain discovers divergence", h, 200, bpUnique(10), vfs.Unstable)
	if r.n != 0 || r.how != vfs.Unstable || r.err == nil {
		t.Fatalf("fsB's second write = %v, want (0, UNSTABLE, the drain's error)", r)
	}
	var s vfs.Status
	if errors.As(r.err, &s) {
		t.Errorf("fsB's second write failed with %v, which unwraps to vfs.Status %d; ADR "+
			"0003 §5: both divergence paths return a bare error, so statusOf maps it to "+
			"NFS3ERR_IO", r.err, uint32(s))
	}
	bpWantDirty(t, fsB, "after a drain whose flushAll succeeded and whose commit diverged", 0)
	bpWantErrorRecord(t, c, r.err, "a drain that discovered divergence")

	r3 := bpWrite(t, fsB, "fsB's third write, after divergence", h, 300, bpUnique(10), vfs.Unstable)
	if r3.n != 0 || !errors.Is(r3.err, vfs.ErrStale) {
		t.Errorf("fsB's third write = %v, want (0, _, vfs.ErrStale): the failed drain set "+
			"f.diverged, and mutable() reports it (ADR 0003 §5)", r3)
	}
}

// TestBackpressureStableWriteWaitsThenSyncs (F14) pins ADR 0003 §4, "Stable
// writes. A FILE_SYNC or DATA_SYNC write waits exactly as an UNSTABLE one does,
// in await before bufferWrite, and syncs only after bufferWrite has returned
// and released openFile.mu ... Since 5479fe2 both get the same full sync and
// are answered FILE_SYNC."
func TestBackpressureStableWriteWaitsThenSyncs(t *testing.T) {
	for _, how := range []vfs.Stability{vfs.FileSync, vfs.DataSync} {
		t.Run(bpHow(how), func(t *testing.T) {
			st := bpLocal(t)
			c := newBTCapture(false)
			fs := bpNew(t, st, bpCS, c.logger())
			const name = "stable.bin"
			h, _ := bpCreate(t, fs, name, 0)
			first := bpUnique(100)
			second := bpUnique(100)
			bpMustWrite(t, fs, "an UNSTABLE write of 100 bytes at 0", h, 0, first)
			r := bpWrite(t, fs, "a "+bpHow(how)+" write of 100 bytes at 200, over the limit "+
				"(a lock held across the wait, or a sync under openFile.mu, hangs here)",
				h, 200, second, how)
			if r.err != nil || int(r.n) != len(second) || r.how != vfs.FileSync {
				t.Fatalf("the %s write = %v, want (%d, FILE_SYNC, nil)", bpHow(how), r, len(second))
			}
			if n := len(c.withMessage(btMsgInfo)); n != 1 {
				t.Errorf("captured %d %q records, want exactly 1: the stable write waited and "+
					"drained", n, btMsgInfo)
			}
			bpWantDirty(t, fs, "after the "+bpHow(how)+" write's own sync", 0)
			want := make([]byte, 300)
			copy(want, first)
			copy(want[200:], second)
			bpWantRemount(t, st, name, want, "both writes, with no Sync after the "+bpHow(how)+" write")
		})
	}
}

// TestBackpressureWarnRecordThroughWrite (F15) pins ADR 0003 §4, "FS gains
// budget *budget, built in New from the limit §1 describes and from f.log, the
// logger New has already defaulted", and §7's Warn record through Write: "at
// most once per await call, while that call is still blocked", "the drainer
// counts, and a single writer stuck behind a hung bucket logs it", "waited is a
// time.Duration (slog.KindDuration)", and "dirty_bytes and limit are int64".
// warnAfter is set straight after New, as §4's test surface allows.
func TestBackpressureWarnRecordThroughWrite(t *testing.T) {
	t.Run("warn while a drain is held", func(t *testing.T) {
		c := newBTCapture(false)
		b := bpBlockDrain(t, c.logger(), func(fs *FS) { fs.budget.warnAfter = time.Millisecond })
		btEventually(t, fmt.Sprintf("a %q record while writer A is held in its drain", btMsgWarn),
			func() bool { return len(c.withMessage(btMsgWarn)) > 0 }, bpState(b.fs))
		b.stillBlocked(t, "when the Warn record appeared")
		warns := c.withMessage(btMsgWarn)
		if len(warns) != 1 {
			t.Errorf("captured %d %q records for one blocked call, want 1", len(warns), btMsgWarn)
		}
		r := warns[0]
		btWantLevel(t, "the Warn record", r, slog.LevelWarn)
		if v, ok := btAttr(r, "waited"); !ok {
			t.Errorf("the Warn record has no top-level attribute \"waited\" (ADR 0003 §7)")
		} else if v.Kind() != slog.KindDuration {
			t.Errorf("the Warn record's waited is of kind %v, want %v", v.Kind(), slog.KindDuration)
		} else if v.Duration() < time.Millisecond {
			t.Errorf("the Warn record's waited = %v, want at least warnAfter = 1ms", v.Duration())
		}
		btWantInt64(t, "the Warn record", r, "dirty_bytes", bpCS)
		btWantInt64(t, "the Warn record", r, "limit", bpCS)

		// A fixed wait, to check that something does not happen: the record
		// is logged at most once per await call.
		time.Sleep(50 * time.Millisecond)
		if n := len(c.withMessage(btMsgWarn)); n != 1 {
			t.Errorf("50ms later, %d %q records for one blocked call, want still 1", n, btMsgWarn)
		}
		b.release(t)
	})

	t.Run("nil Config.Log", func(t *testing.T) {
		fs := bpNew(t, bpLocal(t), bpCS, nil)
		h, _ := bpCreate(t, fs, "nil-log.bin", 0)
		bpMustWrite(t, fs, "the first write, with Config.Log nil", h, 0, bpUnique(100))
		bpMustWrite(t, fs, "the second write, with Config.Log nil, which drains and logs "+
			"through the logger New defaulted (a nil logger would panic here)", h, 200, bpUnique(10))
		bpWantDirty(t, fs, "after the drain and the second write", bpCS)
	})
}

// TestBackpressureFSIsNotAPinner (F16) is the tripwire that ADR 0003's "What
// this does not decide" asks for: "A test that fails once *blobfs.FS satisfies
// vfs.Pinner, pointing here, would turn this note into a tripwire." It passes
// today by design.
func TestBackpressureFSIsNotAPinner(t *testing.T) {
	var fs any = (*FS)(nil)
	if _, ok := fs.(vfs.Pinner); ok {
		t.Errorf("*FS now satisfies vfs.Pinner. ADR 0003, \"What this does not decide\", " +
			"bullet \"How vfs.Pinner interacts with the budget\": Pinner contradicts the " +
			"rationale of §2's sites 2 and 5, \"nothing will ever flush this buffer\". " +
			"Whoever implements Pinner must keep a pinned, unlinked file's charge; keep that " +
			"file flushable, since flushOpen records uploaded chunks only for an inode still " +
			"in the table; and keep site 2 from firing for a pinned file. Settle that in an " +
			"ADR, then replace this tripwire with tests of the pinned behaviour.")
	}
}

// TestBackpressureWriteRacingUnlinkReleases (F17) pins ADR 0003 §2 site 1, "A
// charge placed after site 2's check would leak on a write racing an unlink:
// the check would find the inode gone and release, and the charge would land
// afterwards on buffers nothing will flush", and site 2: "if the inode has gone
// ... Release the file's whole remaining charge with of.bytes.Swap(0) and
// reset of.dirty to an empty map", deterministically. The Write is held in its
// chunk fetch, under openFile.mu, while the file is removed; §4, "If the inode
// goes away": "Gone by the tail block, unlinked while it buffered: it reports
// what it buffered ... and success, and site 2 discards the buffers and
// releases the charge". With no site 2, or with the charge placed after its
// check, one chunk stays charged for good.
func TestBackpressureWriteRacingUnlinkReleases(t *testing.T) {
	st := bpLocal(t)
	const name = "unlinked.bin"
	bpSeedFile(t, st, name, 1000)

	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, 0, quietLog())
	h, attr := bpLookup(t, fs, name)
	gate := newBTGate(t)
	get := &bpHook{entered: make(chan struct{}), gate: gate.ch}
	bps.setGet(get)

	data := bpUnique(50)
	w := bpStartWrite(context.Background(), fs, testCaller, h, 100, data, vfs.Unstable)
	bpWaitEntered(t, "the Write's fetch of chunk 0", get.entered, w)
	of := bpMustOpen(t, fs, attr.FileID, "while the Write is held in its fetch")
	bpRemove(t, fs, name, "Remove of the file while its Write is held in a chunk fetch "+
		"(site 5 runs under FS.mu and takes no openFile.mu, ADR 0003 §3)")

	gate.open()
	r := w.wait(t, "the Write, after its fetch was released")
	if r.err != nil || int(r.n) != len(data) || r.how != vfs.Unstable {
		t.Errorf("the Write whose file was removed while it buffered = %v, want (%d, "+
			"UNSTABLE, nil) (ADR 0003 §4, \"If the inode goes away\")", r, len(data))
	}
	bpWantDirty(t, fs, "after a Write whose inode went while it buffered (0 net)", 0)
	s := bpSnapshot(t, of, "the removed file's openFile")
	if s.bytes != 0 || len(s.caps) != 0 {
		t.Errorf("the removed file's openFile holds of.bytes = %d and dirty indices %v, want "+
			"0 and none: site 2 swaps of.bytes to 0 and resets of.dirty (ADR 0003 §2)",
			s.bytes, s.indices())
	}
}

// bpSite4Gone is F18: a flush of a file whose inode goes while the flush is
// held fetching its pending trim, which then fails in the upload or in that
// fetch.
func bpSite4Gone(t *testing.T, failGet bool) {
	t.Helper()
	st := bpLocal(t)
	const name = "trimmed.bin"
	bpSeedFile(t, st, name, 2*bpCS)
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, 0, quietLog())
	h, attr := bpLookup(t, fs, name)
	bpTruncate(t, fs, h, 5000, "SetAttr(size 5000) on a fresh mount")
	of := bpMustOpen(t, fs, attr.FileID, "after the deferred truncate")
	if s := bpSnapshot(t, of, "after the deferred truncate"); s.pending != 1 {
		t.Fatalf("len(of.pendingTrim) = %d, want 1 (ADR 0003 §2, \"Deferring to pendingTrim\")", s.pending)
	}

	gate := newBTGate(t)
	get := &bpHook{entered: make(chan struct{}), gate: gate.ch}
	bps.setGet(get)
	syncCall := bpGo(func() error { return fs.Sync(context.Background()) })
	bpWaitEntered(t, "the flush's fetch of the pending-trim chunk", get.entered, syncCall)
	bpRemove(t, fs, name, "Remove of the file while its flush is held fetching the "+
		"pending-trim chunk")

	injected := errors.New("backpressure test: injected store failure")
	if failGet {
		bps.update(func() { get.err = injected })
	} else {
		bps.setPut(&bpHook{err: injected})
	}
	gate.open()
	if err := syncCall.wait(t, "Sync, after the fetch was released"); err == nil {
		t.Errorf("Sync = nil, want an error: the flush's %s failed",
			map[bool]string{true: "fetch", false: "upload"}[failGet])
	}
	bpWantDirty(t, fs, "after a failed flush of a file whose inode has gone", 0)
	s := bpSnapshot(t, of, "the removed file's openFile after the failed flush")
	if s.bytes != 0 || len(s.caps) != 0 || s.pending != 0 {
		t.Errorf("the removed file's openFile holds of.bytes = %d, dirty indices %v and %d "+
			"pending trims, want 0, none and 0: on a failed return with the inode gone, site "+
			"4 releases the file's whole remaining charge with of.bytes.Swap(0), resets "+
			"of.dirty to an empty map and of.pendingTrim to nil (ADR 0003 §2)",
			s.bytes, s.indices(), s.pending)
	}
}

// TestBackpressureFailedFlushOfRemovedFileReleases (F18) pins ADR 0003 §2 site
// 4, "On each of its two failed returns — a pendingTrim fetch or an upload —
// take FS.mu as well ... and check whether the inode has gone ... If it has,
// release the file's whole remaining charge with of.bytes.Swap(0), reset
// of.dirty to an empty map and of.pendingTrim to nil", "Why site 4 releases on
// a failed flush", and Assumption 18. Since ADR 0006 a flush applies a trim
// through a copy it never charges (§5), so a failed flush of a removed file
// finds nothing charged to it; the check stays for its reset (ADR 0006
// Assumption 9), which these cases see as an empty of.dirty and no pending
// trims, and for keeping DirtyBytes() at 0.
//
// A third case used to be here: two pending trims, the file removed while the
// flush was held in the first fetch, that trim materialised and charged, and
// the second fetch failing. It is gone because it cannot happen any more, not
// because it went untested: a file has at most one pending trim (ADR 0006 §2,
// I4), and a flush never charges one (§5), so nothing can be charged to the
// removed file when a fetch fails. bpSite4Gone(t, true) covers the fetch
// failure that remains.
func TestBackpressureFailedFlushOfRemovedFileReleases(t *testing.T) {
	t.Run("upload fails", func(t *testing.T) { bpSite4Gone(t, false) })
	t.Run("fetch fails", func(t *testing.T) { bpSite4Gone(t, true) })
}

// bpPanic is what the drain's Put panics with in F19.
type bpPanic struct{ why string }

// TestBackpressureDrainPanicThroughWrite (F19) pins ADR 0003 §4, "Panicked
// ...: record errDrainPanicked as the latest failure, log nothing, and let the
// panic or the Goexit continue unchanged, so await does not return ... A later
// call can become the next drainer (Assumption 11)", G2, "errDrainPanicked for
// Panicked", and "What Write returns when the wait fails": Write returns the
// error "not wrapped". A later write over the limit must drain and succeed,
// which also requires the panicking flush to have left no filesystem lock
// held.
func TestBackpressureDrainPanicThroughWrite(t *testing.T) {
	c := newBTCapture(false)
	var g vfs.Handle
	b := bpBlockDrain(t, c.logger(), func(fs *FS) {
		g, _ = bpCreate(t, fs, "g.bin", 0)
	})
	fs := b.fs
	bw := bpStartWrite(context.Background(), fs, testCaller, g, 0, bpUnique(10), vfs.Unstable)
	bpWaitParked(t, fs, "writer B's Write to G", bw)

	p := &bpPanic{why: "backpressure test: the drain's Put panics"}
	b.bps.update(func() { b.put.panicVal = p })
	b.gate.open()

	b.a.end(t, "writer A's Write, whose drain's Put panics")
	switch {
	case !b.a.panicked:
		t.Errorf("writer A's Write returned %v, want the drain's panic to continue out of "+
			"Write unchanged (ADR 0003 §4, Panicked)", b.a.val)
	case b.a.pval != any(p):
		t.Errorf("writer A's Write panicked with %v (%T), want the drain's own panic value "+
			"%v, unchanged", b.a.pval, b.a.pval, p)
	}

	r := bw.wait(t, "writer B's Write, parked when the drain panicked")
	if r.n != 0 || r.how != vfs.Unstable || r.err != errDrainPanicked {
		t.Errorf("writer B's Write = %v, want (0, UNSTABLE, errDrainPanicked), not wrapped "+
			"(ADR 0003 §4, G2)", r)
	}

	b.bps.setPut(nil)
	if fs.DirtyBytes() < bpCS {
		x, _ := bpCreate(t, fs, "top-up.bin", 0)
		bpMustWrite(t, fs, "a write bringing the charge back to the limit", x, 0, bpUnique(10))
	}
	infos := len(c.withMessage(btMsgInfo))
	later := bpWrite(t, fs, "a later write over the limit, after the drain panicked (a hang "+
		"here means the panicking flush left a lock held, or the drain was never settled)",
		b.held, 300, bpUnique(10), vfs.Unstable)
	if later.err != nil || later.n != 10 {
		t.Errorf("a later write over the limit = %v, want (10, UNSTABLE, nil): it becomes the "+
			"next drainer (ADR 0003 §4, Assumption 11, G1)", later)
	}
	if n := len(c.withMessage(btMsgInfo)); n != infos+1 {
		t.Errorf("the later write logged %d %q records, want 1: it was over the limit and "+
			"had to drain", n-infos, btMsgInfo)
	}
}
