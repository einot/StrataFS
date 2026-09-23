package blobfs

// Clean-room tests for the write-backpressure budget, written from
// docs/adr/0003-write-backpressure.md as revised 2026-09-23: §3 (the lock
// rules), §4 (*The budget* and *The test surface*), §7 (the three log records)
// and Assumptions 8-14. They exercise the budget type on its own. drain is a
// plain func(context.Context) error that each test supplies, and no FS is
// built.
//
// Every call to await runs in its own goroutine (btAwait) and is waited for
// under btHangBound (btWait), so a hang fails the test instead of the package.
// Tests reach a state by polling with a deadline (btEventually), never by
// sleeping. The one fixed wait, in btScenarioWarn, checks that something does
// NOT happen. Reads of the budget from the test goroutine while calls are in
// flight also go through a bound (btBounded), because a budget that deadlocks
// on its own mutex would otherwise hang the test goroutine too.
//
// Not covered: Assumption 9's "a parked call fails even when the failed drain
// freed enough room for it". A drain frees room through release, which
// broadcasts (§4, *Admission while a drain runs*), so a parked call can wake
// and be admitted before the drain settles. Returning nil and returning the
// failure are then both correct, and no test can force one without a hook
// inside await.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// btHangBound is how long a call may take before it counts as hung. Nothing
// here waits on anything slower than a channel the test controls, so a
// correct budget answers in milliseconds.
const btHangBound = 10 * time.Second

// btDeadlineAfter is the timeout on the parked call in
// TestBudgetParkedCallWakesOnDeadline. It only has to outlast the call's trip
// from btAwait to parking, which takes microseconds.
const btDeadlineAfter = 250 * time.Millisecond

// btHerdSize is how many callers TestBudgetOneDrainForManyWaiters and
// TestBudgetFailedDrainFailsEveryWaiter start at once.
const btHerdSize = 8

// The three records of ADR 0003 §7.
const (
	btMsgInfo  = "commit triggered by write backpressure"
	btMsgWarn  = "writes stalled waiting for commit"
	btMsgError = "backpressure commit failed"
)

// btCtxKey tags a context so a drain can tell whose ctx it was given.
type btCtxKey struct{}

func btCtx(parent context.Context, tag string) context.Context {
	return context.WithValue(parent, btCtxKey{}, tag)
}

func btTag(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(btCtxKey{}).(string)
	return s
}

// btCall is one call to await running in its own goroutine. err, panicked and
// pval may be read only once done is closed.
type btCall struct {
	done     chan struct{}
	err      error
	panicked bool
	pval     any
}

// btAwait starts b.await(ctx, drain) in a goroutine. A panic out of await is
// recovered there and recorded, so a test can compare it.
func btAwait(ctx context.Context, b *budget, drain func(context.Context) error) *btCall {
	c := &btCall{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		defer func() {
			if r := recover(); r != nil {
				c.panicked = true
				c.pval = r
			}
		}()
		c.err = b.await(ctx, drain)
	}()
	return c
}

// finished reports, without blocking, whether the call has returned.
func (c *btCall) finished() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// btWait waits for c under btHangBound and fails the test if it is hung.
func btWait(t *testing.T, what string, c *btCall) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(btHangBound):
		t.Fatalf("%s has not returned after %v, so it is hung", what, btHangBound)
	}
}

// btResult waits for c and returns what await returned. A panic out of await
// fails the test: await panics only when its own drain does (ADR 0003 §4), and
// the one test that expects that reads c.pval itself.
func btResult(t *testing.T, what string, c *btCall) error {
	t.Helper()
	btWait(t, what, c)
	if c.panicked {
		t.Fatalf("%s panicked with %v; await lets a panic out only when its own "+
			"drain panics (ADR 0003 §4)", what, c.pval)
	}
	return c.err
}

// btBounded runs fn in its own goroutine and reports whether it returned
// within limit. After a timeout the goroutine is abandoned, still blocked.
func btBounded[T any](limit time.Duration, fn func() T) (T, bool) {
	ch := make(chan T, 1)
	go func() { ch <- fn() }()
	select {
	case v := <-ch:
		return v, true
	case <-time.After(limit):
		var zero T
		return zero, false
	}
}

// btDo runs fn, which takes the budget's lock, under btHangBound.
func btDo(t *testing.T, what string, fn func()) {
	t.Helper()
	_, ok := btBounded(btHangBound, func() struct{} {
		fn()
		return struct{}{}
	})
	if !ok {
		t.Fatalf("%s has not returned after %v: the budget's lock is held and "+
			"never released", what, btHangBound)
	}
}

// btUsed reads b.used() under btHangBound.
func btUsed(t *testing.T, b *budget) int64 {
	t.Helper()
	v, ok := btBounded(btHangBound, b.used)
	if !ok {
		t.Fatalf("used() has not returned after %v: the budget's lock is held "+
			"and never released", btHangBound)
	}
	return v
}

// btWaiters reads b.waiters() under btHangBound.
func btWaiters(t *testing.T, b *budget) int {
	t.Helper()
	v, ok := btBounded(btHangBound, b.waiters)
	if !ok {
		t.Fatalf("waiters() has not returned after %v: the budget's lock is "+
			"held and never released", btHangBound)
	}
	return v
}

// btEventually polls cond until it holds, failing the test after btHangBound.
// Each evaluation of cond is itself bounded, because cond usually reads the
// budget, and a budget that never releases its lock would hang the read.
// state describes the situation for the failure message.
func btEventually(t *testing.T, what string, cond func() bool, state func() string) {
	t.Helper()
	deadline := time.Now().Add(btHangBound)
	for {
		ok, returned := btBounded(max(time.Until(deadline), 100*time.Millisecond), cond)
		if !returned {
			t.Fatalf("waiting for %s: reading the budget has not returned after "+
				"%v, so its lock is held and never released", what, btHangBound)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			s, ok := btBounded(time.Second, state)
			if !ok {
				s = "state unreadable: the budget's lock is held"
			}
			t.Fatalf("%s did not happen within %v; %s", what, btHangBound, s)
		}
		time.Sleep(time.Millisecond)
	}
}

// btGate blocks a drain until the test opens it. The test's cleanup opens it
// too, so a drain left blocked by a failing test can finish.
type btGate struct {
	ch   chan struct{}
	once sync.Once
}

func newBTGate(t *testing.T) *btGate {
	g := &btGate{ch: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *btGate) open() { g.once.Do(func() { close(g.ch) }) }

// btDrainLog records every call to a test's drain and the ctx it was given.
type btDrainLog struct {
	mu   sync.Mutex
	ctxs []context.Context
}

// enter records a call and returns its number, counting from 1.
func (d *btDrainLog) enter(ctx context.Context) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ctxs = append(d.ctxs, ctx)
	return len(d.ctxs)
}

func (d *btDrainLog) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.ctxs)
}

// ctxAt returns the ctx given to call i, counting from 1, or nil.
func (d *btDrainLog) ctxAt(i int) context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i < 1 || i > len(d.ctxs) {
		return nil
	}
	return d.ctxs[i-1]
}

// btState describes b and d for a failure message.
func btState(b *budget, d *btDrainLog) func() string {
	return func() string {
		return fmt.Sprintf("used() = %d, waiters() = %d, drain calls = %d",
			b.used(), b.waiters(), d.count())
	}
}

// btFreeAll releases whatever b holds. A test's drain uses it to stand in for
// a successful Sync. Only the drain changes the charge while it runs, so
// reading and then releasing is exact.
func btFreeAll(b *budget) {
	if u := b.used(); u > 0 {
		b.release(u)
	}
}

// btCapture collects the records a budget logs. With reentrant set, its
// handler also calls back into the budget under test before storing each
// record, which deadlocks a budget that logs while holding its own lock (ADR
// 0003 §3: "Nothing is logged while the budget's mutex is held").
type btCapture struct {
	reentrant bool
	target    atomic.Pointer[budget]

	mu   sync.Mutex
	recs []slog.Record
}

func newBTCapture(reentrant bool) *btCapture { return &btCapture{reentrant: reentrant} }

func (c *btCapture) logger() *slog.Logger { return slog.New(&btHandler{c: c}) }

// attach names the budget a reentrant handler calls back into.
func (c *btCapture) attach(b *budget) { c.target.Store(b) }

func (c *btCapture) filter(keep func(slog.Record) bool) []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []slog.Record
	for _, r := range c.recs {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func (c *btCapture) withMessage(msg string) []slog.Record {
	return c.filter(func(r slog.Record) bool { return r.Message == msg })
}

func (c *btCapture) atLevel(level slog.Level) []slog.Record {
	return c.filter(func(r slog.Record) bool { return r.Level == level })
}

// btHandler is the slog.Handler behind btCapture. Attributes preset through
// WithAttrs are merged into each captured record, so a budget may log through
// Logger.With or pass attributes per call. Attributes under WithGroup are
// captured nested in that group, as slog's own handlers would render them, so
// they do not show up as top-level keys: ADR 0003 §7 says "Attributes are
// top-level key–value pairs, not grouped".
type btHandler struct {
	c      *btCapture
	preset []slog.Attr
	groups []string
}

func (h *btHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *btHandler) Handle(_ context.Context, r slog.Record) error {
	if h.c.reentrant {
		if b := h.c.target.Load(); b != nil {
			_ = b.used()
			_ = b.waiters()
		}
	}
	rec := r.Clone()
	if len(h.preset) > 0 || len(h.groups) > 0 {
		var own []slog.Attr
		r.Attrs(func(a slog.Attr) bool {
			own = append(own, a)
			return true
		})
		rec = slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		rec.AddAttrs(h.preset...)
		rec.AddAttrs(btNest(h.groups, own)...)
	}
	h.c.mu.Lock()
	h.c.recs = append(h.c.recs, rec)
	h.c.mu.Unlock()
	return nil
}

func (h *btHandler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.preset = append(append([]slog.Attr(nil), h.preset...), btNest(h.groups, as)...)
	return &n
}

func (h *btHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.groups = append(append([]string(nil), h.groups...), name)
	return &n
}

// btNest wraps attrs in the given groups, outermost first.
func btNest(groups []string, attrs []slog.Attr) []slog.Attr {
	if len(attrs) == 0 {
		return nil
	}
	for i := len(groups) - 1; i >= 0; i-- {
		attrs = []slog.Attr{{Key: groups[i], Value: slog.GroupValue(attrs...)}}
	}
	return attrs
}

// btAttr returns r's top-level attribute key, resolved.
func btAttr(r slog.Record, key string) (slog.Value, bool) {
	var (
		v     slog.Value
		found bool
	)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v = a.Value.Resolve()
			found = true
			return false
		}
		return true
	})
	return v, found
}

// btWantInt64 checks that r has a top-level int64 attribute key equal to want
// (ADR 0003 §7: "dirty_bytes and limit are int64 (slog.KindInt64)").
func btWantInt64(t *testing.T, what string, r slog.Record, key string, want int64) {
	t.Helper()
	v, ok := btAttr(r, key)
	switch {
	case !ok:
		t.Errorf("%s has no top-level attribute %q; ADR 0003 §7 gives it one, "+
			"and its attributes are top-level, not grouped", what, key)
	case v.Kind() != slog.KindInt64:
		t.Errorf("%s: attribute %q is of kind %v, want %v (ADR 0003 §7)",
			what, key, v.Kind(), slog.KindInt64)
	case v.Int64() != want:
		t.Errorf("%s: attribute %q = %d, want %d", what, key, v.Int64(), want)
	}
}

// btWantLevel checks r's level.
func btWantLevel(t *testing.T, what string, r slog.Record, want slog.Level) {
	t.Helper()
	if r.Level != want {
		t.Errorf("%s is logged at %v, want %v (ADR 0003 §7)", what, r.Level, want)
	}
}

// TestBudgetLogCaptureHandler checks the test harness rather than the budget.
// The capturing handler must accept attributes passed per call and through
// Logger.With alike, and must not report an attribute logged under WithGroup
// as top-level, because ADR 0003 §7 says "Attributes are top-level key–value
// pairs, not grouped".
func TestBudgetLogCaptureHandler(t *testing.T) {
	c := newBTCapture(false)
	lg := c.logger()
	lg.With("preset", int64(1)).Info("flat", "own", int64(2))
	lg.WithGroup("g").Info("grouped", "own", int64(3))
	lg.WithGroup("g").With("preset", int64(4)).Info("grouped preset")

	flat := c.withMessage("flat")
	if len(flat) != 1 {
		t.Fatalf("harness captured %d records for \"flat\", want 1", len(flat))
	}
	btWantInt64(t, "harness: a record logged through With", flat[0], "preset", 1)
	btWantInt64(t, "harness: a record logged through With", flat[0], "own", 2)

	for _, msg := range []string{"grouped", "grouped preset"} {
		recs := c.withMessage(msg)
		if len(recs) != 1 {
			t.Fatalf("harness captured %d records for %q, want 1", len(recs), msg)
		}
		for _, key := range []string{"own", "preset"} {
			if _, ok := btAttr(recs[0], key); ok {
				t.Errorf("harness: record %q logged through WithGroup exposes %q "+
					"as a top-level attribute", msg, key)
			}
		}
		if _, ok := btAttr(recs[0], "g"); !ok {
			t.Errorf("harness: record %q logged through WithGroup lacks its group", msg)
		}
	}
}

// TestBudgetUsedIsExactSum (B1) pins ADR 0003 §4: add and release "Both
// require n >= 0 and neither clamps, so used() is exactly Σadd − Σrelease."
// It checks after every step of a mixed sequence that stays at or above zero
// and goes far past the limit, which add charges "without waiting".
func TestBudgetUsedIsExactSum(t *testing.T) {
	b := newBudget(1<<20, quietLog())
	steps := []struct {
		add bool
		n   int64
	}{
		{true, 4096},
		{true, 1},
		{false, 97},
		{true, 1 << 30},
		{false, 0},
		{true, 0},
		{false, 1 << 30},
		{true, 12345},
		{false, 4000},
		{false, 12345},
	}
	var want int64
	for i, s := range steps {
		op := "release"
		if s.add {
			op = "add"
			b.add(s.n)
			want += s.n
		} else {
			b.release(s.n)
			want -= s.n
		}
		if got := b.used(); got != want {
			t.Fatalf("after step %d, %s(%d): used() = %d, want Σadd − Σrelease = %d",
				i+1, op, s.n, got, want)
		}
	}
}

// TestBudgetDisabledNeverWaits (B2) pins ADR 0003 §4: "limit <= 0 disables
// waiting; accounting continues regardless", step 1, "If limit <= 0 or
// used() < limit, return nil at once, without looking at ctx (Assumption 8)",
// and "limit reports the limit the budget was built with, unchanged."
func TestBudgetDisabledNeverWaits(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			b := newBudget(limit, quietLog())
			if got := b.limit(); got != limit {
				t.Errorf("limit() = %d, want %d unchanged", got, limit)
			}
			b.add(1 << 40)
			if got := b.used(); got != 1<<40 {
				t.Errorf("after add(1<<40) on a disabled budget, used() = %d, want %d; "+
					"accounting continues regardless", got, int64(1<<40))
			}

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			d := &btDrainLog{}
			drain := func(ctx context.Context) error {
				d.enter(ctx)
				return errors.New("budget test: a disabled budget must not drain")
			}
			err := btResult(t, "await on a disabled budget", btAwait(ctx, b, drain))
			if err != nil {
				t.Errorf("await with limit %d, used() far above it and a cancelled "+
					"ctx = %v, want nil: limit <= 0 returns nil at once, without "+
					"looking at ctx", limit, err)
			}
			if n := d.count(); n != 0 {
				t.Errorf("await with limit %d called drain %d times, want 0", limit, n)
			}

			b.release(1 << 39)
			b.add(7)
			if got, want := b.used(), int64(1<<40-1<<39+7); got != want {
				t.Errorf("used() = %d, want %d; accounting continues on a disabled budget",
					got, want)
			}
			if got := b.limit(); got != limit {
				t.Errorf("limit() after use = %d, want %d unchanged", got, limit)
			}
		})
	}
}

// TestBudgetFastPathIgnoresCtx (B3) pins ADR 0003 §4 step 1, "If limit <= 0 or
// used() < limit, return nil at once, without looking at ctx", and Assumption
// 8: "A call that finds room ... returns nil even when its ctx is already
// done."
func TestBudgetFastPathIgnoresCtx(t *testing.T) {
	for _, charge := range []int64{0, 999} {
		t.Run(fmt.Sprintf("used=%d", charge), func(t *testing.T) {
			b := newBudget(1000, quietLog())
			b.add(charge)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			d := &btDrainLog{}
			drain := func(ctx context.Context) error {
				d.enter(ctx)
				return errors.New("budget test: a call with room must not drain")
			}
			err := btResult(t, "await below the limit", btAwait(ctx, b, drain))
			if err != nil {
				t.Errorf("await with used() = %d < limit 1000 and a cancelled ctx = "+
					"%v, want nil (fast path, Assumption 8)", charge, err)
			}
			if n := d.count(); n != 0 {
				t.Errorf("await with room called drain %d times, want 0", n)
			}
		})
	}
}

// TestBudgetOverLimitDrainsWithCallerCtx (B4) pins ADR 0003 §4 step 3.4: the
// elected drainer calls "drain(ctx)", the caller's own ctx, and a drain that
// succeeds goes round the loop, where "The drainer is admitted by step 3.2
// like anyone else." used() equal to the limit is not room: step 1 and step
// 3.2 both test used() < limit.
func TestBudgetOverLimitDrainsWithCallerCtx(t *testing.T) {
	b := newBudget(1000, quietLog())
	b.add(1000)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		d.enter(ctx)
		btFreeAll(b)
		return nil
	}
	ctx := btCtx(context.Background(), "caller")
	err := btResult(t, "await at the limit", btAwait(ctx, b, drain))
	if err != nil {
		t.Errorf("await at the limit with a drain that frees everything = %v, want nil", err)
	}
	if n := d.count(); n != 1 {
		t.Fatalf("await with used() == limit called drain %d times, want 1; "+
			"used() < limit is the only room", n)
	}
	if tag := btTag(d.ctxAt(1)); tag != "caller" {
		t.Errorf("drain was given a ctx tagged %q, want the caller's ctx (tagged "+
			"\"caller\"); ADR 0003 §4 step 3.4 calls drain(ctx)", tag)
	}
	if u := btUsed(t, b); u != 0 {
		t.Errorf("used() after the drain = %d, want 0", u)
	}
}

// TestBudgetDrainErrorReturnedUnchanged (B5) pins ADR 0003 §4, G3: "The
// drainer returns its own drain's failure: the error value drain returned,
// unchanged."
func TestBudgetDrainErrorReturnedUnchanged(t *testing.T) {
	errE := errors.New("budget test: drain failed (E)")
	b := newBudget(1000, quietLog())
	b.add(2000)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		d.enter(ctx)
		return errE
	}
	err := btResult(t, "await with a failing drain", btAwait(context.Background(), b, drain))
	if err != errE {
		t.Errorf("await = %v (%T), want the drain's error value itself, unwrapped (G3)", err, err)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1: a failed drain returns, it "+
			"does not loop", n)
	}
	if u := btUsed(t, b); u != 2000 {
		t.Errorf("used() = %d, want 2000: nothing was released", u)
	}
}

// TestBudgetDrainsAgainAfterSuccessWithoutRoom (B6) pins ADR 0003 §4,
// "Succeeded (it returned nil): go round the loop. The drainer is admitted by
// step 3.2 like anyone else. If other writers refilled the budget while it
// drained, it drains again", and "Admitting it outright instead would let a
// drain that freed nothing admit a writer over the limit".
func TestBudgetDrainsAgainAfterSuccessWithoutRoom(t *testing.T) {
	b := newBudget(1000, quietLog())
	b.add(2000)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) >= 2 {
			btFreeAll(b)
		}
		return nil
	}
	err := btResult(t, "await with a drain that frees nothing the first time",
		btAwait(context.Background(), b, drain))
	if err != nil {
		t.Errorf("await = %v, want nil once the second drain frees everything", err)
	}
	if n := d.count(); n != 2 {
		t.Errorf("drain was called %d times, want 2: a successful drain that "+
			"leaves no room must be followed by another", n)
	}
	if u := btUsed(t, b); u != 0 {
		t.Errorf("used() = %d, want 0", u)
	}
}

// btRunHerd starts btHerdSize calls to await, each with its own tagged ctx, on
// a budget holding five times its limit. The first call to drain blocks until
// one drain has been entered and every other call has parked, then returns
// drainErr, having freed everything if drainErr is nil and nothing otherwise.
// A second call to drain would be a failure the caller reports; it frees
// everything so that a wrong budget still terminates. btRunHerd returns once
// every call has finished, with the tag of the call that ran the first drain.
func btRunHerd(t *testing.T, drainErr error) (*budget, *btDrainLog, []*btCall, string) {
	t.Helper()
	const limit, charge = 1000, 5000
	b := newBudget(limit, quietLog())
	b.add(charge)
	gate := newBTGate(t)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-gate.ch
			if drainErr != nil {
				return drainErr
			}
			b.release(charge)
			return nil
		}
		btFreeAll(b)
		return nil
	}
	calls := make([]*btCall, btHerdSize)
	for i := range calls {
		calls[i] = btAwait(btCtx(context.Background(), fmt.Sprintf("caller %d", i)), b, drain)
	}
	btEventually(t, fmt.Sprintf("one drain entered and %d calls parked", btHerdSize-1),
		func() bool { return d.count() == 1 && b.waiters() == btHerdSize-1 },
		btState(b, d))
	for i, c := range calls {
		if c.finished() {
			t.Fatalf("caller %d returned while the only drain was still blocked "+
				"and used() was over the limit", i)
		}
	}
	gate.open()
	for i, c := range calls {
		btWait(t, fmt.Sprintf("caller %d", i), c)
	}
	return b, d, calls, btTag(d.ctxAt(1))
}

// TestBudgetOneDrainForManyWaiters (B7) pins ADR 0003 §4, "At most one drain
// started by the budget is in progress at a time", step 3.4, "Otherwise park
// on the condition variable", and Assumption 13 via waiters(): "The drainer is
// not counted", so "one drain in progress and seven parked" reads 7.
func TestBudgetOneDrainForManyWaiters(t *testing.T) {
	b, d, calls, _ := btRunHerd(t, nil)
	for i, c := range calls {
		if err := btResult(t, fmt.Sprintf("caller %d", i), c); err != nil {
			t.Errorf("caller %d = %v, want nil after the drain freed everything", i, err)
		}
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times for %d callers, want exactly 1", n, btHerdSize)
	}
	if w := btWaiters(t, b); w != 0 {
		t.Errorf("waiters() = %d after every call returned, want 0", w)
	}
	if u := btUsed(t, b); u != 0 {
		t.Errorf("used() = %d, want 0", u)
	}
}

// TestBudgetFailedDrainFailsEveryWaiter (B8) pins ADR 0003 §4, G2: "A call
// that is parked when a drain fails ... returns that failure", and G3: "The
// drainer returns its own drain's failure: the error value drain returned,
// unchanged." Also Consequences: "One failing commit fails every write that is
// waiting on it, at once."
func TestBudgetFailedDrainFailsEveryWaiter(t *testing.T) {
	errE := errors.New("budget test: drain failed (E)")
	b, d, calls, drainer := btRunHerd(t, errE)
	for i, c := range calls {
		tag := fmt.Sprintf("caller %d", i)
		err := btResult(t, tag, c)
		if !errors.Is(err, errE) {
			t.Errorf("%s = %v, want the failed drain's error (G2, G3)", tag, err)
			continue
		}
		if tag == drainer && err != errE {
			t.Errorf("%s ran the drain and returned %v (%T), want the drain's "+
				"error value itself, unwrapped (G3)", tag, err, err)
		}
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1: a parked call checks for a "+
			"failure since it began before anything else (§4 step 3.1)", n)
	}
	if u := btUsed(t, b); u != 5000 {
		t.Errorf("used() = %d, want 5000: the failed drain freed nothing", u)
	}
}

// TestBudgetFailureDoesNotOutliveItsDrain (B9) pins ADR 0003 §4, G1: "A call
// never returns a failure from a drain that ended before the call began", and
// Assumption 11's "a call that arrives later can still drain (G1)".
func TestBudgetFailureDoesNotOutliveItsDrain(t *testing.T) {
	errE := errors.New("budget test: drain failed (E)")

	t.Run("over the limit", func(t *testing.T) {
		b := newBudget(1000, quietLog())
		b.add(2000)
		d := &btDrainLog{}
		drain := func(ctx context.Context) error {
			if d.enter(ctx) == 1 {
				return errE
			}
			btFreeAll(b)
			return nil
		}
		err := btResult(t, "the first await", btAwait(context.Background(), b, drain))
		if err != errE {
			t.Fatalf("the first await = %v, want its failing drain's error (G3)", err)
		}

		// Step 3.1 finds no drain ended since this call began, 3.2 no room,
		// and 3.3 a done ctx.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err = btResult(t, "an await with a cancelled ctx after the failure",
			btAwait(ctx, b, drain))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("an over-limit await with a cancelled ctx, started after the "+
				"failed drain ended = %v, want context.Canceled; the earlier "+
				"failure predates it (G1)", err)
		}
		if n := d.count(); n != 1 {
			t.Errorf("drain was called %d times, want still 1", n)
		}

		err = btResult(t, "a later over-limit await", btAwait(context.Background(), b, drain))
		if err != nil {
			t.Errorf("a later over-limit await = %v, want nil: it must become the "+
				"next drainer rather than return the earlier failure (G1)", err)
		}
		if n := d.count(); n != 2 {
			t.Errorf("drain was called %d times in total, want 2: the later call "+
				"must drain", n)
		}
	})

	t.Run("below the limit", func(t *testing.T) {
		b := newBudget(1000, quietLog())
		b.add(2000)
		d := &btDrainLog{}
		drain := func(ctx context.Context) error {
			d.enter(ctx)
			return errE
		}
		err := btResult(t, "the first await", btAwait(context.Background(), b, drain))
		if err != errE {
			t.Fatalf("the first await = %v, want its failing drain's error (G3)", err)
		}
		b.release(1500)
		err = btResult(t, "an await below the limit after the failure",
			btAwait(context.Background(), b, drain))
		if err != nil {
			t.Errorf("an await with used() = 500 < limit 1000 after a failed drain "+
				"= %v, want nil (G1)", err)
		}
		if n := d.count(); n != 1 {
			t.Errorf("drain was called %d times, want 1", n)
		}
	})
}

// btParkedCallGivesUp runs B10 and B11. Call A drains, blocked; call B parks
// with the ctx from newCtx. B's ctx becomes done, by cancelling it if
// byCancel is set and by its own deadline otherwise. B must then return an
// error matching want while A is still blocked, and A must still succeed.
func btParkedCallGivesUp(t *testing.T, newCtx func() (context.Context, context.CancelFunc), byCancel bool, want error) {
	t.Helper()
	b := newBudget(1000, quietLog())
	b.add(2000)
	gate := newBTGate(t)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-gate.ch
		}
		btFreeAll(b)
		return nil
	}
	a := btAwait(context.Background(), b, drain)
	btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))

	ctxB, cancelB := newCtx()
	defer cancelB()
	parked := btAwait(ctxB, b, drain)
	btEventually(t, "B to park",
		func() bool { return b.waiters() == 1 || parked.finished() }, btState(b, d))
	if parked.finished() {
		if byCancel {
			t.Fatalf("B returned %v before its ctx was cancelled, while A's drain "+
				"was still blocked; over the limit with a drain in progress, B "+
				"must park (ADR 0003 §4 step 3.4)", parked.err)
		}
		t.Logf("B's deadline passed before B was seen parked, so this run did not " +
			"exercise waking a parked call; the result is still checked")
	}
	if byCancel {
		cancelB()
	}

	err := btResult(t, "B's await after its ctx was done", parked)
	if !errors.Is(err, want) {
		t.Errorf("B's await = %v, want %v: a parked call returns ctx.Err() "+
			"promptly once its ctx is done, whether or not any drain ends", err, want)
	}
	if a.finished() {
		t.Fatalf("A returned while its drain was still blocked")
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1", n)
	}
	if w := btWaiters(t, b); w != 0 {
		t.Errorf("waiters() = %d after B returned, want 0", w)
	}

	gate.open()
	if err := btResult(t, "A's await", a); err != nil {
		t.Errorf("A's await = %v, want nil after its drain freed everything", err)
	}
}

// TestBudgetParkedCallWakesOnCancel (B10) pins ADR 0003 §4, *Waking on
// cancellation*: "A parked call returns ctx.Err() promptly once its ctx is
// done, whether or not any drain ends", which await achieves by registering
// context.AfterFunc(ctx, f) before it first parks.
func TestBudgetParkedCallWakesOnCancel(t *testing.T) {
	btParkedCallGivesUp(t, func() (context.Context, context.CancelFunc) {
		return context.WithCancel(context.Background())
	}, true, context.Canceled)
}

// TestBudgetParkedCallWakesOnDeadline (B11) pins the same sentence as B10 for
// a ctx that is done because its deadline passed.
func TestBudgetParkedCallWakesOnDeadline(t *testing.T) {
	btParkedCallGivesUp(t, func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), btDeadlineAfter)
	}, false, context.DeadlineExceeded)
}

// TestBudgetAbandonedDrainPublishesNothing (B12) pins ADR 0003 §4, "Abandoned
// (it returned an error, and the drainer's ctx is done by then): record
// nothing and log nothing, and return ctx.Err(). The parked calls go round
// again, and one of them becomes the next drainer, with its own ctx", and
// Assumption 10: broadcasting the error "would fail other writers with someone
// else's cancellation". §7: "There is no Error record for an abandoned drain".
func TestBudgetAbandonedDrainPublishesNothing(t *testing.T) {
	c := newBTCapture(false)
	b := newBudget(1000, c.logger())
	b.add(2000)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		btFreeAll(b)
		return nil
	}

	ctxA, cancelA := context.WithCancel(btCtx(context.Background(), "A"))
	defer cancelA()
	a := btAwait(ctxA, b, drain)
	btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))
	parked := btAwait(btCtx(context.Background(), "B"), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))

	cancelA()
	if err := btResult(t, "A's await", a); !errors.Is(err, context.Canceled) {
		t.Errorf("A's await after its ctx was cancelled mid-drain = %v, want "+
			"context.Canceled", err)
	}
	if err := btResult(t, "B's await", parked); err != nil {
		t.Errorf("B's await = %v, want nil: an abandoned drain publishes nothing, "+
			"so B must drain in turn rather than get A's cancellation", err)
	}
	if n := d.count(); n != 2 {
		t.Fatalf("drain was called %d times, want 2: B must become the next drainer", n)
	}
	if tag := btTag(d.ctxAt(2)); tag != "B" {
		t.Errorf("the second drain was given a ctx tagged %q, want B's own ctx", tag)
	}
	if recs := c.atLevel(slog.LevelError); len(recs) != 0 {
		t.Errorf("an abandoned drain logged %d Error records (first: %q), want none",
			len(recs), recs[0].Message)
	}
	if u := btUsed(t, b); u != 0 {
		t.Errorf("used() = %d, want 0", u)
	}
}

// TestBudgetAdmitsWhileDrainRuns (B13) pins ADR 0003 §4, *Admission while a
// drain runs*: "release broadcasts, so a parked call also wakes when ... [any
// release] frees space ... A woken call that finds room is admitted there and
// then, usually while the drain that made the room is still running. It does
// not wait for that drain's outcome".
func TestBudgetAdmitsWhileDrainRuns(t *testing.T) {
	b := newBudget(1000, quietLog())
	b.add(2000)
	gate := newBTGate(t)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-gate.ch
			return nil
		}
		btFreeAll(b)
		return nil
	}
	a := btAwait(context.Background(), b, drain)
	btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))
	parked := btAwait(context.Background(), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))

	btDo(t, "release(1500) while A drains", func() { b.release(1500) })
	if err := btResult(t, "B's await after a release made room", parked); err != nil {
		t.Errorf("B's await = %v, want nil: used() = 500 < limit 1000", err)
	}
	if a.finished() {
		t.Fatalf("A returned while its drain was still blocked")
	}

	gate.open()
	if err := btResult(t, "A's await", a); err != nil {
		t.Errorf("A's await = %v, want nil: its drain succeeded and used() is "+
			"below the limit (step 3.2)", err)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1", n)
	}
	if u := btUsed(t, b); u != 500 {
		t.Errorf("used() = %d, want 500", u)
	}
}

// btPanicValue is what the drain in TestBudgetDrainPanic panics with.
type btPanicValue struct{ why string }

// TestBudgetDrainPanic (B14) pins ADR 0003 §4, "Panicked: record
// errDrainPanicked as the latest failure, log nothing, and let the panic
// continue out of await unchanged ... A later call can become the next
// drainer (Assumption 11)", and G2's "errDrainPanicked for a panic". §7:
// there is no Error record for "a panicked one".
func TestBudgetDrainPanic(t *testing.T) {
	c := newBTCapture(false)
	b := newBudget(1000, c.logger())
	b.add(2000)
	gate := newBTGate(t)
	d := &btDrainLog{}
	p := &btPanicValue{why: "budget test: drain panics"}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-gate.ch
			panic(p)
		}
		btFreeAll(b)
		return nil
	}
	a := btAwait(context.Background(), b, drain)
	btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))
	parked := btAwait(context.Background(), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))
	gate.open()

	btWait(t, "A's await", a)
	switch {
	case !a.panicked:
		t.Errorf("A's await returned %v, want the drain's panic to continue out "+
			"of await unchanged", a.err)
	case a.pval != p:
		t.Errorf("A's await panicked with %v (%T), want the drain's own panic "+
			"value %v, unchanged", a.pval, a.pval, p)
	}

	err := btResult(t, "B's await", parked)
	if err == nil || !errors.Is(err, errDrainPanicked) {
		t.Errorf("B's await, parked when the drain panicked, = %v, want "+
			"errDrainPanicked (G2)", err)
	}
	if n := d.count(); n != 1 {
		t.Fatalf("drain was called %d times before the later call, want 1: B "+
			"returns the failure rather than draining again", n)
	}

	later := btAwait(context.Background(), b, drain)
	if err := btResult(t, "a later over-limit await", later); err != nil {
		t.Errorf("a later over-limit await = %v, want nil: it becomes the next "+
			"drainer (Assumption 11, G1)", err)
	}
	if n := d.count(); n != 2 {
		t.Errorf("drain was called %d times in total, want 2", n)
	}
	if recs := c.atLevel(slog.LevelError); len(recs) != 0 {
		t.Errorf("a panicked drain logged %d Error records (first: %q), want none",
			len(recs), recs[0].Message)
	}
}

// btScenarioInfo is B15, parameterised by the capture so B19 can rerun it.
// Call A drains twice: its first drain frees 300 of 1500 and succeeds, which
// leaves no room, so a second drain follows and frees the rest. Call B is
// parked throughout the first drain, so it must not log an Info record of its
// own.
func btScenarioInfo(t *testing.T, c *btCapture) {
	t.Helper()
	const limit = 1000
	b := newBudget(limit, c.logger())
	c.attach(b)
	b.add(1500)
	gate := newBTGate(t)
	d := &btDrainLog{}
	var (
		mu   sync.Mutex
		seen []int
	)
	drain := func(ctx context.Context) error {
		n := d.enter(ctx)
		infos := len(c.withMessage(btMsgInfo))
		mu.Lock()
		seen = append(seen, infos)
		mu.Unlock()
		if n == 1 {
			<-gate.ch
			b.release(300)
			return nil
		}
		btFreeAll(b)
		return nil
	}
	a := btAwait(context.Background(), b, drain)
	btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))
	parked := btAwait(context.Background(), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))
	gate.open()
	if err := btResult(t, "A's await", a); err != nil {
		t.Errorf("A's await = %v, want nil", err)
	}
	if err := btResult(t, "B's await", parked); err != nil {
		t.Errorf("B's await = %v, want nil", err)
	}

	if n := d.count(); n != 2 {
		t.Fatalf("drain was called %d times, want 2: the first drain left "+
			"used() = 1200, still over the limit", n)
	}
	mu.Lock()
	got := append([]int(nil), seen...)
	mu.Unlock()
	for i, n := range got {
		if n != i+1 {
			t.Errorf("when drain call %d was entered, %d Info records had been "+
				"captured, want %d: the record is out before drain is called", i+1, n, i+1)
		}
	}

	infos := c.withMessage(btMsgInfo)
	if len(infos) != 2 {
		t.Fatalf("captured %d %q records for 2 drains, want exactly one per drain",
			len(infos), btMsgInfo)
	}
	wantDirty := []int64{1500, 1200}
	for i, r := range infos {
		what := fmt.Sprintf("Info record %d", i+1)
		btWantLevel(t, what, r, slog.LevelInfo)
		btWantInt64(t, what, r, "dirty_bytes", wantDirty[i])
		btWantInt64(t, what, r, "limit", limit)
	}
}

// TestBudgetInfoRecordPerDrain (B15) pins ADR 0003 §7: "Info | commit
// triggered by write backpressure | dirty_bytes, limit | by the elected
// drainer, once per drain, before it calls drain", "dirty_bytes is used() when
// the drainer was elected. The record is out before drain is called", and the
// value kinds, int64.
func TestBudgetInfoRecordPerDrain(t *testing.T) {
	btScenarioInfo(t, newBTCapture(false))
}

// btScenarioError is B16, parameterised by the capture so B19 can rerun it.
// Call A drains, blocked, with call B parked; the drain then fails.
func btScenarioError(t *testing.T, c *btCapture) {
	t.Helper()
	errE := errors.New("budget test: drain failed (E)")
	b := newBudget(1000, c.logger())
	c.attach(b)
	b.add(2000)
	gate := newBTGate(t)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-gate.ch
			return errE
		}
		btFreeAll(b)
		return nil
	}
	a := btAwait(context.Background(), b, drain)
	btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))
	parked := btAwait(context.Background(), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))
	gate.open()
	if err := btResult(t, "A's await", a); err != errE {
		t.Errorf("A's await = %v, want the drain's error unchanged (G3)", err)
	}
	if err := btResult(t, "B's await", parked); !errors.Is(err, errE) {
		t.Errorf("B's await = %v, want the drain's error (G2)", err)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1", n)
	}

	recs := c.withMessage(btMsgError)
	if len(recs) != 1 {
		t.Fatalf("captured %d %q records for one failed drain, want exactly 1",
			len(recs), btMsgError)
	}
	r := recs[0]
	btWantLevel(t, "the Error record", r, slog.LevelError)
	if v, ok := btAttr(r, "err"); !ok {
		t.Errorf("the Error record has no top-level attribute \"err\"")
	} else if v.Any() != errE {
		t.Errorf("the Error record's err attribute is %v (kind %v), want the "+
			"error value drain returned", v.Any(), v.Kind())
	}
	if all := c.atLevel(slog.LevelError); len(all) != 1 {
		t.Errorf("captured %d Error-level records for one failed drain, want 1", len(all))
	}
}

// TestBudgetErrorRecordPerFailedDrain (B16) pins ADR 0003 §7: "Error |
// backpressure commit failed | err | once per failed drain" and "err is the
// error drain returned ... the error value itself"; Assumption 14 names the
// message and key.
func TestBudgetErrorRecordPerFailedDrain(t *testing.T) {
	btScenarioError(t, newBTCapture(false))
}

// btScenarioWarn is B17, parameterised by the capture so B19 can rerun it. A
// single call is blocked in its own drain, past a warnAfter of 1 ms.
func btScenarioWarn(t *testing.T, c *btCapture) {
	t.Helper()
	const limit, charge = 1000, 2000
	b := newBudget(limit, c.logger())
	b.warnAfter = time.Millisecond
	c.attach(b)
	b.add(charge)
	gate := newBTGate(t)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		d.enter(ctx)
		<-gate.ch
		btFreeAll(b)
		return nil
	}
	a := btAwait(context.Background(), b, drain)
	btEventually(t, fmt.Sprintf("a %q record while the call is blocked in its own drain", btMsgWarn),
		func() bool { return len(c.withMessage(btMsgWarn)) > 0 }, btState(b, d))
	if a.finished() {
		t.Fatalf("the call returned while its drain was still blocked")
	}

	warns := c.withMessage(btMsgWarn)
	if len(warns) != 1 {
		t.Errorf("captured %d %q records for one blocked call, want 1", len(warns), btMsgWarn)
	}
	r := warns[0]
	btWantLevel(t, "the Warn record", r, slog.LevelWarn)
	if v, ok := btAttr(r, "waited"); !ok {
		t.Errorf("the Warn record has no top-level attribute \"waited\"")
	} else if v.Kind() != slog.KindDuration {
		t.Errorf("the Warn record's waited is of kind %v, want %v",
			v.Kind(), slog.KindDuration)
	} else if v.Duration() < time.Millisecond {
		t.Errorf("the Warn record's waited = %v, want at least warnAfter = 1ms",
			v.Duration())
	}
	btWantInt64(t, "the Warn record", r, "dirty_bytes", charge)
	btWantInt64(t, "the Warn record", r, "limit", limit)

	// A fixed wait, to check that something does not happen: the record is
	// logged at most once per call, however long the call stays blocked.
	time.Sleep(50 * time.Millisecond)
	if n := len(c.withMessage(btMsgWarn)); n != 1 {
		t.Errorf("50ms later, %d %q records for one blocked call, want still 1 "+
			"(at most once per await call)", n, btMsgWarn)
	}

	gate.open()
	if err := btResult(t, "the call's await", a); err != nil {
		t.Errorf("await = %v, want nil after its drain freed everything", err)
	}
	if n := len(c.withMessage(btMsgWarn)); n != 1 {
		t.Errorf("after the call returned, %d %q records, want 1", n, btMsgWarn)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1", n)
	}
}

// TestBudgetWarnRecordWhileBlocked (B17) pins ADR 0003 §7: "Warn | writes
// stalled waiting for commit | waited, dirty_bytes, limit | at most once per
// await call, while that call is still blocked", "counted from when it entered
// await and including any time it spends draining. So the drainer counts",
// "waited is a time.Duration (slog.KindDuration)", and §4's warnAfter, which
// "A test may lower ... before the budget is first used". Assumption 12.
func TestBudgetWarnRecordWhileBlocked(t *testing.T) {
	btScenarioWarn(t, newBTCapture(false))
}

// TestBudgetNoWarnForShortWait (B18) pins ADR 0003 §7: "A call logs it once it
// has been blocked longer than warnAfter". A call that drains at once, with
// warnAfter an hour, is never blocked that long.
func TestBudgetNoWarnForShortWait(t *testing.T) {
	c := newBTCapture(false)
	b := newBudget(1000, c.logger())
	b.warnAfter = time.Hour
	b.add(2000)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		d.enter(ctx)
		btFreeAll(b)
		return nil
	}
	if err := btResult(t, "await", btAwait(context.Background(), b, drain)); err != nil {
		t.Errorf("await = %v, want nil", err)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1", n)
	}
	if n := len(c.withMessage(btMsgWarn)); n != 0 {
		t.Errorf("captured %d %q records for a call that was never blocked for "+
			"warnAfter, want 0", n, btMsgWarn)
	}
}

// TestBudgetLogsOutsideItsLock (B19) pins ADR 0003 §3, "Nothing is logged
// while the budget's mutex is held ... one that calls back into the budget —
// to read used(), say — would deadlock on it", and §7, "Nothing is logged
// while b.mu is held". It reruns B15-B17 with a handler that calls used() and
// waiters() on the budget before storing each record. A budget that logs under
// its lock deadlocks, and the scenario's deadline fails.
func TestBudgetLogsOutsideItsLock(t *testing.T) {
	t.Run("info", func(t *testing.T) { btScenarioInfo(t, newBTCapture(true)) })
	t.Run("error", func(t *testing.T) { btScenarioError(t, newBTCapture(true)) })
	t.Run("warn", func(t *testing.T) { btScenarioWarn(t, newBTCapture(true)) })
}

// TestBudgetDefaults (B20) pins ADR 0003 §4's pinned declarations: "const
// stallWarnAfter = 5 * time.Second", "newBudget sets it [warnAfter] to
// stallWarnAfter", "limit reports the limit the budget was built with,
// unchanged", and errDrainPanicked, "a sentinel made with errors.New". A fresh
// budget holds nothing, since used() is exactly Σadd − Σrelease, and has no
// parked calls.
func TestBudgetDefaults(t *testing.T) {
	if stallWarnAfter != 5*time.Second {
		t.Errorf("stallWarnAfter = %v, want 5s", stallWarnAfter)
	}
	if errDrainPanicked == nil {
		t.Errorf("errDrainPanicked is nil, want a sentinel error")
	}
	for _, limit := range []int64{1, 4096, 256 << 20, math.MaxInt64, 0, -1} {
		b := newBudget(limit, quietLog())
		if b.warnAfter != stallWarnAfter {
			t.Errorf("newBudget(%d, log).warnAfter = %v, want stallWarnAfter (%v)",
				limit, b.warnAfter, stallWarnAfter)
		}
		if got := b.limit(); got != limit {
			t.Errorf("newBudget(%d, log).limit() = %d, want %d unchanged", limit, got, limit)
		}
		if got := b.used(); got != 0 {
			t.Errorf("newBudget(%d, log).used() = %d, want 0", limit, got)
		}
		if got := b.waiters(); got != 0 {
			t.Errorf("newBudget(%d, log).waiters() = %d, want 0", limit, got)
		}
	}
}
