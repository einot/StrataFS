package blobfs

// Clean-room tests for the write-backpressure budget, written from
// docs/adr/0003-write-backpressure.md as revised 2026-09-23, including its
// second pass: §3 (the lock rules), §4 (*The budget* and *The test surface*),
// §7 (the three log records) and Assumptions 8-14. They exercise the budget type on its own. drain is a
// plain func(context.Context) error that each test supplies, and no FS is
// built.
//
// Every call to await runs in its own goroutine (btAwait) and is waited for
// under btHangBound (btWait), so a hang fails the test instead of the package.
// Tests reach a state by polling with a deadline (btEventually), never by
// sleeping. The fixed waits, in btScenarioWarn, btScenarioParkedWarn and
// TestBudgetNoWarnAfterReturn, only check that something does NOT happen.
// Reads of the budget from the test goroutine while calls are in flight also
// go through a bound (btBounded), because a budget that deadlocks on its own
// mutex would otherwise hang the test goroutine too.
//
// One test is probabilistic: TestBudgetFailureSurvivesLaterSuccess. Its doc
// comment explains why it can never fail on a correct budget.
//
// Not covered: Assumption 9's "a call still parked when a failed drain is
// settled fails, even if that drain freed enough room for it before it
// failed", when the room was freed through release. Assumption 9 itself says
// why: "when a failing drain frees room before it fails, whether a woken call
// is admitted or failed depends on whether it looks before the settle or after
// it. That is scheduling: both outcomes conform, and a test cannot force
// either without a hook inside await." What it says a test can pin, "If
// nothing frees room before the settle, every call still parked then fails",
// is covered by TestBudgetFailedDrainFailsEveryWaiter and others.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime"
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

// btCall is one call to await running in its own goroutine. err, returned,
// panicked and pval may be read only once done is closed. A call that is done
// with neither returned nor panicked set ended in runtime.Goexit.
type btCall struct {
	done     chan struct{}
	err      error
	returned bool
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
		c.returned = true
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
// fails the test. ADR 0003 §4 lets a panic continue out of await only when the
// drainer's window ends in one, when its drain panics or the Info record's log
// call does, and when the Error record's log call panics after the settle
// (G3). The tests that set those up wait for the drainer with btWait and read
// c.pval themselves.
func btResult(t *testing.T, what string, c *btCall) error {
	t.Helper()
	btWait(t, what, c)
	if c.panicked {
		t.Fatalf("%s panicked with %v; await lets a panic out only for the "+
			"drainer, whose window or Error record's log call panicked (ADR "+
			"0003 §4, G3)", what, c.pval)
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
// "Succeeded (drain returned nil): go round the loop. The drainer is admitted
// by step 3.2 like anyone else. If other writers refilled the budget while it
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
// still parked when a drain is settled as Failed or Panicked returns that
// failure", which here, with nothing freed before the settle, is every parked
// call (Assumption 9: "If nothing frees room before the settle, every call
// still parked then fails, and that is what a test can pin"), and G3: "The
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
// (drain returned an error, and the drainer's ctx is done by then): record
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

// TestBudgetDrainPanic (B14) pins ADR 0003 §4, "Panicked (the window ended
// without drain returning: drain panicked ...): record errDrainPanicked as the
// latest failure, log nothing, and let the panic or the Goexit continue
// unchanged, so await does not return ... A later call can become the next
// drainer (Assumption 11)", and G2's "errDrainPanicked for Panicked". §7:
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
// "A test may set it to any value before the budget is first used".
// Assumption 12.
func TestBudgetWarnRecordWhileBlocked(t *testing.T) {
	btScenarioWarn(t, newBTCapture(false))
}

// TestBudgetNoWarnForShortWait (B18) pins ADR 0003 §7: "A call logs it once it
// has been blocked longer than warnAfter". A call that drains at once, with
// warnAfter an hour, is never blocked that long. Raising warnAfter is allowed:
// Assumption 12 lets a test set it "higher, so that a short wait is sure to
// stay under it".
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
// unchanged", and errDrainPanicked, declared "var errDrainPanicked =
// errors.New(...)": "It is a sentinel, compared with errors.Is. The
// declaration's form is pinned; the text given to errors.New is not." A fresh
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

// TestBudgetAbandonedIsDecidedByCtxNotError pins ADR 0003 §4, "Abandoned
// (drain returned an error, and the drainer's ctx is done by then): record nothing
// and log nothing, and return ctx.Err(). The parked calls go round again, and
// one of them becomes the next drainer, with its own ctx", and Assumption 10,
// "a genuine store failure that coincides with the drainer's cancellation is
// not published either". Unlike TestBudgetAbandonedDrainPublishesNothing, the
// drain returns a store error, not ctx.Err(). A budget that decided between
// failed and abandoned by looking at the error value would publish it.
func TestBudgetAbandonedIsDecidedByCtxNotError(t *testing.T) {
	errStore := errors.New("budget test: store failed while the drainer was cancelled")
	c := newBTCapture(false)
	b := newBudget(1000, c.logger())
	b.add(2000)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-ctx.Done()
			return errStore
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
	errA := btResult(t, "A's await", a)
	if !errors.Is(errA, context.Canceled) || errors.Is(errA, errStore) {
		t.Errorf("A's await = %v, want context.Canceled: a drain that fails "+
			"after its drainer's ctx is done is abandoned, and the drainer "+
			"returns ctx.Err(), not the drain's error", errA)
	}
	if err := btResult(t, "B's await", parked); err != nil {
		t.Errorf("B's await = %v, want nil: an abandoned drain publishes "+
			"nothing, so B must drain in turn", err)
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
}

// TestBudgetCanceledFromLiveDrainerIsFailure pins ADR 0003 §4, "Failed (drain
// returned an error, and the drainer's ctx is not done): record the error as
// the latest failure, unlock, log the Error record (§7), and return the error
// unchanged (G3)", together with G2. Here the drain returns context.Canceled
// while its drainer's ctx is still live. What decides failed against abandoned
// is the drainer's ctx, so this is a failure like any other, even though the
// value looks like a cancellation.
func TestBudgetCanceledFromLiveDrainerIsFailure(t *testing.T) {
	c := newBTCapture(false)
	b := newBudget(1000, c.logger())
	b.add(2000)
	gate := newBTGate(t)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-gate.ch
			return context.Canceled
		}
		btFreeAll(b)
		return nil
	}
	a := btAwait(context.Background(), b, drain)
	btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))
	parked := btAwait(context.Background(), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))
	gate.open()

	if err := btResult(t, "A's await", a); err != context.Canceled {
		t.Errorf("A's await = %v, want context.Canceled, the value its drain "+
			"returned, unchanged (G3)", err)
	}
	// B's own ctx is never done, so context.Canceled can only be the failure
	// of the drain it was parked on.
	if err := btResult(t, "B's await", parked); !errors.Is(err, context.Canceled) {
		t.Errorf("B's await = %v, want context.Canceled, the failure of the "+
			"drain it was parked on (G2)", err)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1: the failure is published, "+
			"so B must not drain again", n)
	}
	recs := c.withMessage(btMsgError)
	if len(recs) != 1 {
		t.Fatalf("captured %d %q records, want exactly 1: a drain that fails "+
			"with its drainer's ctx live is a failed drain", len(recs), btMsgError)
	}
	if v, ok := btAttr(recs[0], "err"); !ok {
		t.Errorf("the Error record has no top-level attribute \"err\"")
	} else if v.Any() != context.Canceled {
		t.Errorf("the Error record's err attribute is %v (kind %v), want "+
			"context.Canceled, the error value drain returned", v.Any(), v.Kind())
	}
}

// TestBudgetDrainGoexit pins ADR 0003 §4, "A drain is settled on every way out
// of the drainer's window ... it can end in a return from drain, a panic, or a
// runtime.Goexit. Settling clears the in-progress mark, counts the drain as
// ended, and broadcasts", with a drain that calls runtime.Goexit, and
// "Panicked (the window ended without drain returning: drain panicked or
// called runtime.Goexit ...): record errDrainPanicked as the latest failure,
// log nothing, and let the panic or the Goexit continue unchanged, so await
// does not return ... A later call can become the next drainer". Also §4:
// "During a runtime.Goexit, recover returns nil (*References*), so a settle
// that runs only when recover reports a panic never runs for one: the
// in-progress mark stays set, and every later call over the limit parks for
// good." B, parked on the drain, returns errDrainPanicked (G2).
func TestBudgetDrainGoexit(t *testing.T) {
	b := newBudget(1000, quietLog())
	b.add(2000)
	gate := newBTGate(t)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-gate.ch
			runtime.Goexit()
		}
		btFreeAll(b)
		return nil
	}

	var (
		aReturned atomic.Bool
		aPanicked atomic.Bool
	)
	aExited := make(chan struct{})
	go func() {
		defer close(aExited)
		defer func() {
			if r := recover(); r != nil {
				aPanicked.Store(true)
			}
		}()
		_ = b.await(context.Background(), drain)
		aReturned.Store(true)
	}()
	btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))
	parked := btAwait(context.Background(), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))
	gate.open()

	select {
	case <-aExited:
	case <-time.After(btHangBound):
		t.Fatalf("A's goroutine has not ended %v after its drain called "+
			"runtime.Goexit, so await is hung", btHangBound)
	}
	if aReturned.Load() {
		t.Errorf("A's await returned, but its drain called runtime.Goexit, which " +
			"ends the goroutine without returning")
	}
	if aPanicked.Load() {
		t.Errorf("A's await panicked, but its drain called runtime.Goexit, which " +
			"is not a panic")
	}

	err := btResult(t, "B's await, parked on a drain that called runtime.Goexit", parked)
	if err == nil || !errors.Is(err, errDrainPanicked) {
		t.Errorf("B's await = %v, want errDrainPanicked: the drain it was "+
			"parked on ended without returning (§4, Panicked; G2)", err)
	}
	if n := d.count(); n != 1 {
		t.Fatalf("drain was called %d times before the later call, want 1", n)
	}

	later := btAwait(context.Background(), b, drain)
	if err := btResult(t, "a later over-limit await", later); err != nil {
		t.Errorf("a later over-limit await = %v, want nil: the drain that "+
			"called runtime.Goexit must have been settled, so this call can "+
			"become the next drainer (Assumption 11, G1)", err)
	}
	if n := d.count(); n != 2 {
		t.Errorf("drain was called %d times in total, want 2", n)
	}
}

// btScenarioParkedWarn runs TestBudgetWarnRecordForParkedCall with capture c.
// Call A drains, blocked, and is let log its own Warn record first. Only then
// is call B started and parked, so the second Warn record is B's. The ADR
// gives a record no field that names its call, so apart from that ordering
// the records are checked only for their count, kinds and values.
func btScenarioParkedWarn(t *testing.T, c *btCapture) {
	t.Helper()
	const limit, charge = 1000, 2000
	b := newBudget(limit, c.logger())
	b.warnAfter = time.Millisecond
	c.attach(b)
	b.add(charge)
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
	btEventually(t, "the drainer's Warn record",
		func() bool { return len(c.withMessage(btMsgWarn)) >= 1 }, btState(b, d))
	parked := btAwait(context.Background(), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))
	btEventually(t, "a second Warn record, the parked call's",
		func() bool { return len(c.withMessage(btMsgWarn)) >= 2 }, btState(b, d))
	if a.finished() || parked.finished() {
		t.Fatalf("a call returned while the only drain was still blocked")
	}

	// A fixed wait, to check that something does not happen: neither call
	// logs a second Warn record, however long it stays blocked.
	time.Sleep(50 * time.Millisecond)
	warns := c.withMessage(btMsgWarn)
	if len(warns) != 2 {
		t.Errorf("50ms later, %d %q records for two blocked calls, want 2 (at "+
			"most once per await call, the parked call included)", len(warns), btMsgWarn)
	}
	for i, r := range warns {
		what := fmt.Sprintf("Warn record %d", i+1)
		btWantLevel(t, what, r, slog.LevelWarn)
		if v, ok := btAttr(r, "waited"); !ok {
			t.Errorf("%s has no top-level attribute \"waited\"", what)
		} else if v.Kind() != slog.KindDuration {
			t.Errorf("%s: waited is of kind %v, want %v", what, v.Kind(), slog.KindDuration)
		} else if v.Duration() < time.Millisecond {
			t.Errorf("%s: waited = %v, want at least warnAfter = 1ms", what, v.Duration())
		}
		btWantInt64(t, what, r, "dirty_bytes", charge)
		btWantInt64(t, what, r, "limit", limit)
	}

	gate.open()
	if err := btResult(t, "A's await", a); err != nil {
		t.Errorf("A's await = %v, want nil", err)
	}
	if err := btResult(t, "B's await", parked); err != nil {
		t.Errorf("B's await = %v, want nil", err)
	}
	if n := len(c.withMessage(btMsgWarn)); n != 2 {
		t.Errorf("after both calls returned, %d %q records, want 2", n, btMsgWarn)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1", n)
	}
}

// TestBudgetWarnRecordForParkedCall pins ADR 0003 §7's Warn row, "at most once
// per await call, while that call is still blocked", for a call that is parked
// rather than draining, and Assumption 12. TestBudgetWarnRecordWhileBlocked
// covers only the drainer. The reentrant run pins §3's "Nothing is logged
// while the budget's mutex is held" on the parked call's path too.
func TestBudgetWarnRecordForParkedCall(t *testing.T) {
	t.Run("plain", func(t *testing.T) { btScenarioParkedWarn(t, newBTCapture(false)) })
	t.Run("reentrant", func(t *testing.T) { btScenarioParkedWarn(t, newBTCapture(true)) })
}

// TestBudgetNoWarnAfterReturn pins ADR 0003 §7: the Warn timer is "stopped on
// every way out", and "a callback that loses the race with its call's return
// logs nothing". The call drains at once. If the timer outlived the call it
// would fire warnAfter after entry, well inside the wait below.
func TestBudgetNoWarnAfterReturn(t *testing.T) {
	const warnAfter = 200 * time.Millisecond
	c := newBTCapture(false)
	b := newBudget(1000, c.logger())
	b.warnAfter = warnAfter
	b.add(2000)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		d.enter(ctx)
		btFreeAll(b)
		return nil
	}
	start := time.Now()
	err := btResult(t, "await", btAwait(context.Background(), b, drain))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("await = %v, want nil", err)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times, want 1", n)
	}
	if elapsed >= warnAfter {
		t.Skipf("await took %v, at least warnAfter = %v, so a Warn record would "+
			"be legitimate and this run cannot check the timer is stopped",
			elapsed, warnAfter)
	}

	// A fixed wait, to check that something does not happen.
	time.Sleep(2 * warnAfter)
	if n := len(c.withMessage(btMsgWarn)); n != 0 {
		t.Errorf("captured %d %q records after a call that returned within %v; "+
			"the timer must be stopped on every way out, and a callback that "+
			"loses the race with the call's return logs nothing", n, btMsgWarn, elapsed)
	}
}

// btAFCtx is a context that implements AfterFunc(func()) func() bool. The
// standard library documents that context.AfterFunc uses such a method to
// schedule its call when ctx has one. btAFCtx counts every registration, and
// how many of the stop functions it handed out have been called.
//
// It is not built on a context from the context package, because
// context.AfterFunc registers with such a context directly and would never
// call this method. It embeds context.Background only for Deadline and Value.
//
// cancelWithoutCallbacks makes it done without running what was registered.
// If the registered functions ran, context.AfterFunc's own stop function would
// never reach the one btAFCtx returned, and a missing stop could not be seen.
type btAFCtx struct {
	context.Context

	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	err     error
	pending map[int]func()
	regs    int
	stopped int
}

func newBTAFCtx() *btAFCtx {
	return &btAFCtx{
		Context: context.Background(),
		done:    make(chan struct{}),
		pending: map[int]func(){},
	}
}

func (c *btAFCtx) Done() <-chan struct{} { return c.done }

func (c *btAFCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *btAFCtx) AfterFunc(f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.regs
	c.regs++
	c.pending[id] = f
	var once sync.Once
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		once.Do(func() { c.stopped++ })
		_, ok := c.pending[id]
		delete(c.pending, id)
		return ok
	}
}

func (c *btAFCtx) cancelWithoutCallbacks() {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = context.Canceled
		c.mu.Unlock()
		close(c.done)
	})
}

// counts reports how many functions were registered and how many distinct
// stop functions were called.
func (c *btAFCtx) counts() (regs, stopped int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.regs, c.stopped
}

// TestBudgetAfterFuncStoppedOnReturn pins ADR 0003 §4, *Waking on
// cancellation*: "await registers context.AfterFunc(ctx, f) before it first
// parks ... and it calls the returned stop function on every way out". Call A
// drains, blocked; call B parks with a btAFCtx. B then leaves in three ways:
// woken by a release that makes room; woken by A's drain settling, which fails
// without freeing anything, so that B returns its failure (G2); and cancelled.
// When B's await has returned, every registration made on its ctx must have
// been stopped. The number of registrations is not asserted.
//
// In the cancelled case the ctx is made done without running the registered
// functions (see btAFCtx), and a release(0) wakes B instead. Its wake-up is
// then not the one AfterFunc provides, but its way out is still the
// cancellation return, step 3.3, and that return must stop the registration.
// release(0) is a valid call: release takes n >= 0 and "wakes every parked
// call".
func TestBudgetAfterFuncStoppedOnReturn(t *testing.T) {
	errE := errors.New("budget test: drain failed (E)")
	cases := []struct {
		name     string
		drainErr error
		leave    func(t *testing.T, b *budget, gate *btGate, actx *btAFCtx)
		want     error
	}{
		{"woken by release", nil, func(t *testing.T, b *budget, _ *btGate, _ *btAFCtx) {
			btDo(t, "release(2000)", func() { b.release(2000) })
		}, nil},
		// The drain fails without freeing anything, so no release wakes B
		// first: the settle's broadcast is what wakes it.
		{"woken by a drain that settles", errE, func(t *testing.T, _ *budget, gate *btGate, _ *btAFCtx) {
			gate.open()
		}, errE},
		{"cancelled", nil, func(t *testing.T, b *budget, _ *btGate, actx *btAFCtx) {
			actx.cancelWithoutCallbacks()
			btDo(t, "release(0)", func() { b.release(0) })
		}, context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBudget(1000, quietLog())
			b.add(2000)
			gate := newBTGate(t)
			d := &btDrainLog{}
			drain := func(ctx context.Context) error {
				if d.enter(ctx) == 1 {
					<-gate.ch
					if tc.drainErr != nil {
						return tc.drainErr
					}
				}
				btFreeAll(b)
				return nil
			}
			a := btAwait(context.Background(), b, drain)
			btEventually(t, "A to enter drain", func() bool { return d.count() == 1 }, btState(b, d))
			actx := newBTAFCtx()
			parked := btAwait(actx, b, drain)
			btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))

			tc.leave(t, b, gate, actx)
			err := btResult(t, "B's await", parked)
			if !errors.Is(err, tc.want) {
				t.Errorf("B's await = %v, want %v", err, tc.want)
			}
			regs, stopped := actx.counts()
			if regs != stopped {
				t.Errorf("B's await returned with %d functions registered through "+
					"context.AfterFunc on its ctx and %d stopped; await calls the "+
					"returned stop function on every way out", regs, stopped)
			}
			if regs == 0 {
				t.Logf("await registered nothing on B's ctx through context.AfterFunc, " +
					"so this run checks nothing about stopping")
			}

			gate.open()
			if err := btResult(t, "A's await", a); err != tc.drainErr {
				t.Errorf("A's await = %v, want %v, its drain's result", err, tc.drainErr)
			}
		})
	}
}

// TestBudgetDrainerCtxCancelledDuringSuccessfulDrain pins ADR 0003 §4,
// "Succeeded (drain returned nil): go round the loop ... If other writers
// refilled the budget while it drained, it drains again, unless its ctx is
// done by then", and step 3's order: 3.2 "Room" is checked before 3.3
// "Cancellation". The drain cancels its caller's ctx and then succeeds. If it
// freed nothing, the call returns ctx.Err() without draining again. If it freed
// everything, the call returns nil, because room comes first.
func TestBudgetDrainerCtxCancelledDuringSuccessfulDrain(t *testing.T) {
	cases := []struct {
		name string
		free bool
		want error
	}{
		{"frees nothing", false, context.Canceled},
		{"frees everything", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBudget(1000, quietLog())
			b.add(2000)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := &btDrainLog{}
			drain := func(dctx context.Context) error {
				if d.enter(dctx) == 1 {
					cancel()
					if tc.free {
						btFreeAll(b)
					}
					return nil
				}
				// A second drain is itself a failure, reported below. It
				// frees everything so that a wrong budget still terminates.
				btFreeAll(b)
				return nil
			}
			err := btResult(t, "await", btAwait(ctx, b, drain))
			if !errors.Is(err, tc.want) {
				t.Errorf("await = %v, want %v", err, tc.want)
			}
			if n := d.count(); n != 1 {
				t.Errorf("drain was called %d times, want exactly 1", n)
			}
		})
	}
}

// btHookHandler passes every record to onRecord and captures nothing.
type btHookHandler struct{ onRecord func(slog.Record) }

func (h *btHookHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *btHookHandler) Handle(_ context.Context, r slog.Record) error {
	h.onRecord(r)
	return nil
}

func (h *btHookHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *btHookHandler) WithGroup(string) slog.Handler { return h }

// Bounds for TestBudgetFailureSurvivesLaterSuccess: at most btFailRaceIters
// iterations, and no new iteration once btFailRaceBudget has passed.
const (
	btFailRaceIters  = 200
	btFailRaceBudget = 3 * time.Second
)

// TestBudgetFailureSurvivesLaterSuccess pins ADR 0003 §4, G2: "A call still
// parked when a drain is settled as Failed or Panicked returns that failure
// ... or the failure of a later drain", and §4's reason for "Keeping the
// latest failure apart from the count of drains that have ended": "The first
// version kept only the latest drain's result, so a failed drain followed by a
// successful one before a parked call ran would admit that call instead of
// failing it." Assumption 9 records the mechanism.
//
// Each iteration parks X on drain D1, run by A, which fails. The moment the
// budget logs D1's Error record, which it does after settling D1, a hook
// starts Z over the limit. Z's drain D2 frees everything and succeeds, and it
// may run before X gets the lock back to re-check.
//
// The test is probabilistic, because nothing in the pinned surface can force
// D2 to finish before X re-checks. A budget with the bug fails only in runs
// where that ordering happens. The assertion itself is not probabilistic: D1
// frees nothing, so X is still parked when D1 is settled as Failed, and no
// later drain fails, so on a correct budget
// X returns D1's failure however the goroutines are scheduled. A failure here
// is always a real violation of G2.
func TestBudgetFailureSurvivesLaterSuccess(t *testing.T) {
	start := time.Now()
	iters := 0
	for iters < btFailRaceIters {
		if iters > 0 && time.Since(start) >= btFailRaceBudget {
			break
		}
		btFailureThenSuccess(t, iters)
		iters++
	}
	t.Logf("%d iterations in %v", iters, time.Since(start).Round(time.Millisecond))
}

// btFailureThenSuccess runs one iteration of
// TestBudgetFailureSurvivesLaterSuccess on a fresh budget.
func btFailureThenSuccess(t *testing.T, iter int) {
	t.Helper()
	errD1 := errors.New("budget test: drain D1 failed")
	gate := newBTGate(t)
	d := &btDrainLog{}
	var b *budget
	drain := func(ctx context.Context) error {
		if d.enter(ctx) == 1 {
			<-gate.ch
			return errD1
		}
		btFreeAll(b)
		return nil
	}
	zc := make(chan *btCall, 1)
	var zOnce sync.Once
	hook := &btHookHandler{onRecord: func(r slog.Record) {
		if r.Level == slog.LevelError {
			zOnce.Do(func() { zc <- btAwait(context.Background(), b, drain) })
		}
	}}
	b = newBudget(1000, slog.New(hook))
	b.add(2000)

	a := btAwait(context.Background(), b, drain)
	btEventually(t, "A to enter D1", func() bool { return d.count() == 1 }, btState(b, d))
	x := btAwait(context.Background(), b, drain)
	btEventually(t, "X to park", func() bool { return b.waiters() == 1 }, btState(b, d))
	gate.open()

	if err := btResult(t, "X's await", x); !errors.Is(err, errD1) {
		t.Fatalf("iteration %d: X, parked when D1 failed, returned %v, want D1's "+
			"failure (G2); a successful drain after the failure must not hide it "+
			"from a call that was parked on the failed one", iter, err)
	}
	if err := btResult(t, "A's await", a); err != errD1 {
		t.Fatalf("iteration %d: A, which ran D1, returned %v, want D1's error "+
			"unchanged (G3)", iter, err)
	}
	var z *btCall
	select {
	case z = <-zc:
	case <-time.After(btHangBound):
		t.Fatalf("iteration %d: no Error-level record was logged within %v of "+
			"D1 failing", iter, btHangBound)
	}
	if err := btResult(t, "Z's await", z); err != nil {
		t.Fatalf("iteration %d: Z, started after D1 had ended, returned %v, want "+
			"nil after its own drain freed everything (G1)", iter, err)
	}
}

// btWaitChan waits under btHangBound for ch to be closed.
func btWaitChan(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(btHangBound):
		t.Fatalf("%s did not happen within %v", what, btHangBound)
	}
}

// btFault describes a log handler fault. It fires once, on the first record
// whose message is msg: reached is closed, the handler waits for gate if gate
// is set, and then fault runs, panicking or calling runtime.Goexit. Every
// other record, including later ones with the same message, is logged
// normally.
type btFault struct {
	msg     string
	fault   func()
	gate    *btGate
	reached chan struct{}
	fired   atomic.Bool
}

func newBTFault(msg string, fault func(), gate *btGate) *btFault {
	return &btFault{msg: msg, fault: fault, gate: gate, reached: make(chan struct{})}
}

// logger returns a logger that applies f and passes every record it does not
// fault on to c.
func (f *btFault) logger(c *btCapture) *slog.Logger {
	return slog.New(&btFaultHandler{f: f, inner: &btHandler{c: c}})
}

// btFaultHandler is the slog.Handler behind btFault.logger. Handlers derived
// through WithAttrs and WithGroup share its btFault, so the fault fires once
// however the budget logs.
type btFaultHandler struct {
	f     *btFault
	inner slog.Handler
}

func (h *btFaultHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *btFaultHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == h.f.msg && h.f.fired.CompareAndSwap(false, true) {
		close(h.f.reached)
		if h.f.gate != nil {
			<-h.f.gate.ch
		}
		h.f.fault()
	}
	return h.inner.Handle(ctx, r)
}

func (h *btFaultHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &btFaultHandler{f: h.f, inner: h.inner.WithAttrs(as)}
}

func (h *btFaultHandler) WithGroup(name string) slog.Handler {
	return &btFaultHandler{f: h.f, inner: h.inner.WithGroup(name)}
}

// btInfoHandlerFaults runs TestBudgetInfoHandlerPanic and
// TestBudgetInfoHandlerGoexit. The drainer's Info record reaches a handler
// that holds it until a second call has parked, and then panics or, with
// goexit set, calls runtime.Goexit. drain is never reached.
func btInfoHandlerFaults(t *testing.T, goexit bool) {
	t.Helper()
	c := newBTCapture(false)
	gate := newBTGate(t)
	p := &btPanicValue{why: "budget test: the Info record's handler panics"}
	fault := func() { panic(p) }
	if goexit {
		fault = runtime.Goexit
	}
	f := newBTFault(btMsgInfo, fault, gate)
	b := newBudget(1000, f.logger(c))
	b.add(2000)
	d := &btDrainLog{}
	drain := func(ctx context.Context) error {
		d.enter(ctx)
		btFreeAll(b)
		return nil
	}

	a := btAwait(context.Background(), b, drain)
	btWaitChan(t, "the drainer's Info record reaching the log handler", f.reached)
	parked := btAwait(context.Background(), b, drain)
	btEventually(t, "B to park", func() bool { return b.waiters() == 1 }, btState(b, d))
	if n := d.count(); n != 0 {
		t.Fatalf("drain was called %d times before the Info record was out, "+
			"want 0: the record is out before drain is called (§7)", n)
	}
	gate.open()

	btWait(t, "the drainer's await", a)
	switch {
	case goexit && a.returned:
		t.Errorf("the drainer's await returned %v, but its Info record's log "+
			"call called runtime.Goexit, which must continue unchanged, so "+
			"await does not return", a.err)
	case goexit && a.panicked:
		t.Errorf("the drainer's await panicked with %v, but its Info record's "+
			"log call called runtime.Goexit, which is not a panic", a.pval)
	case !goexit && !a.panicked:
		t.Errorf("the drainer's await did not panic (returned: %v, err: %v); "+
			"a panic in the Info record's log call must continue unchanged, so "+
			"await does not return", a.returned, a.err)
	case !goexit && a.pval != p:
		t.Errorf("the drainer's await panicked with %v (%T), want the log "+
			"handler's own panic value %v, unchanged", a.pval, a.pval, p)
	}

	err := btResult(t, "B's await", parked)
	if err == nil || !errors.Is(err, errDrainPanicked) {
		t.Errorf("B's await, parked when the drainer's window ended in its Info "+
			"record's log call, = %v, want errDrainPanicked (§4, Panicked; G2)", err)
	}
	if n := d.count(); n != 0 {
		t.Errorf("drain was called %d times, want 0: the drainer's window ended "+
			"before drain, and B must not drain in its place", n)
	}
	if recs := c.atLevel(slog.LevelError); len(recs) != 0 {
		t.Errorf("a window settled as Panicked logged %d Error records (first: "+
			"%q), want none: Panicked logs nothing", len(recs), recs[0].Message)
	}

	later := btAwait(context.Background(), b, drain)
	if err := btResult(t, "a later over-limit await", later); err != nil {
		t.Errorf("a later over-limit await, with the log handler no longer "+
			"faulting, = %v, want nil: it becomes the next drainer "+
			"(Assumption 11, G1)", err)
	}
	if n := d.count(); n != 1 {
		t.Errorf("drain was called %d times in total, want 1, by the later call", n)
	}
}

// TestBudgetInfoHandlerPanic pins ADR 0003 §4, "A drain is settled on every
// way out of the drainer's window: the part of step 3.4 that runs from its
// election, when it marks a drain in progress, to its relock. The window
// covers the Info record's log call as well as drain", and "Panicked (the
// window ended without drain returning: ... or the Info record's log call did
// so before drain was called): record errDrainPanicked as the latest failure,
// log nothing, and let the panic or the Goexit continue unchanged, so await
// does not return ... A later call can become the next drainer". Also "the
// deferred settle must already be in place when it is logged, so that a log
// handler that panics is settled exactly as a drain that panics is", and
// Assumption 11. A budget that armed its settle after the Info record would
// leave the in-progress mark set, and B would park for good.
func TestBudgetInfoHandlerPanic(t *testing.T) {
	btInfoHandlerFaults(t, false)
}

// TestBudgetInfoHandlerGoexit pins the same sentences as
// TestBudgetInfoHandlerPanic for a log handler that calls runtime.Goexit on
// the Info record: "Panicked (the window ended without drain returning: drain
// panicked or called runtime.Goexit, or the Info record's log call did so
// before drain was called)", and §4's "During a runtime.Goexit, recover
// returns nil (*References*), so a settle that runs only when recover reports
// a panic never runs for one".
func TestBudgetInfoHandlerGoexit(t *testing.T) {
	btInfoHandlerFaults(t, true)
}

// TestBudgetErrorHandlerPanicAfterSettle pins ADR 0003 §4, "Failed (drain
// returned an error, and the drainer's ctx is not done): record the error as
// the latest failure, unlock, log the Error record (§7), and return the error
// unchanged (G3)", and "The Error record is logged after the settle (Failed),
// so a handler that panics there leaves nothing unsettled", with G2. The
// drain fails, and the Error record's log call panics. By then the drain has
// been settled as Failed, so the parked call returns the drain's error, not
// errDrainPanicked, and a later call can drain.
//
// The drainer's own call is pinned by G3: "This holds when the Error record's
// log call returns. If that call panics or calls runtime.Goexit, the panic or
// Goexit continues out of await unchanged in place of the return, and the
// calls still parked get the drain's error all the same, because the settle
// recorded it before the call (Assumption 11)." So the drainer's await panics
// with the handler's own value and does not return.
func TestBudgetErrorHandlerPanicAfterSettle(t *testing.T) {
	btErrorHandlerFaults(t, false)
}

// TestBudgetErrorHandlerGoexitAfterSettle is TestBudgetErrorHandlerPanicAfterSettle
// with a handler that calls runtime.Goexit on the Error record. It pins the
// same sentences, and G3's "If that call panics or calls runtime.Goexit, the
// panic or Goexit continues out of await unchanged in place of the return":
// the drainer's goroutine ends without returning or panicking. The parked call
// still gets the drain's error, not errDrainPanicked, "because the settle
// recorded it before the call".
func TestBudgetErrorHandlerGoexitAfterSettle(t *testing.T) {
	btErrorHandlerFaults(t, true)
}

// btErrorHandlerFaults runs TestBudgetErrorHandlerPanicAfterSettle and, with
// goexit set, TestBudgetErrorHandlerGoexitAfterSettle. A drains and fails with
// E while B is parked, and the Error record's log call panics or calls
// runtime.Goexit.
func btErrorHandlerFaults(t *testing.T, goexit bool) {
	t.Helper()
	errE := errors.New("budget test: drain failed (E)")
	c := newBTCapture(false)
	p := &btPanicValue{why: "budget test: the Error record's handler panics"}
	fault := func() { panic(p) }
	if goexit {
		fault = runtime.Goexit
	}
	f := newBTFault(btMsgError, fault, nil)
	b := newBudget(1000, f.logger(c))
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

	err := btResult(t, "B's await", parked)
	if !errors.Is(err, errE) || errors.Is(err, errDrainPanicked) {
		t.Errorf("B's await = %v, want the drain's error E (G2): the drain was "+
			"settled as Failed before its Error record was logged, so a panic "+
			"in that log call is not a Panicked window", err)
	}

	btWait(t, "the drainer's await", a)
	if !f.fired.Load() {
		t.Errorf("no %q record reached the log handler, so the scenario this "+
			"test is about did not happen; a failed drain logs the Error "+
			"record (§4, Failed)", btMsgError)
	}
	switch {
	case goexit && a.returned:
		t.Errorf("the drainer's await returned %v, but its Error record's log "+
			"call called runtime.Goexit, which continues out of await "+
			"unchanged in place of the return (G3)", a.err)
	case goexit && a.panicked:
		t.Errorf("the drainer's await panicked with %v, but its Error record's "+
			"log call called runtime.Goexit, which is not a panic (G3)", a.pval)
	case !goexit && !a.panicked:
		t.Errorf("the drainer's await did not panic (returned: %v, err: %v); a "+
			"panic in the Error record's log call continues out of await "+
			"unchanged in place of the return (G3)", a.returned, a.err)
	case !goexit && a.pval != p:
		t.Errorf("the drainer's await panicked with %v (%T), want the log "+
			"handler's own panic value %v, unchanged (G3)", a.pval, a.pval, p)
	}

	later := btAwait(context.Background(), b, drain)
	if err := btResult(t, "a later over-limit await", later); err != nil {
		t.Errorf("a later over-limit await = %v, want nil: the failed drain "+
			"was settled, so this call becomes the next drainer (G1)", err)
	}
	if n := d.count(); n != 2 {
		t.Errorf("drain was called %d times in total, want 2", n)
	}
}
