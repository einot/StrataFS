package blobfs

// The write-backpressure budget: a global count of the buffer capacity held
// by unflushed writes, and the wait a writer performs once that count has
// reached its limit. The budget knows nothing about FS. The writer that waits
// is handed the drain to run, so a stalled writer makes progress itself
// rather than depending on a COMMIT its own connection may never read.
//
// Spec: docs/adr/0003-write-backpressure.md §3, §4, §7 (the lock rules; the
// budget, its algorithm and failure contract; the three log records) and
// Assumptions 8-14.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// stallWarnAfter is how long an await call may stay blocked before it logs
// that writes are stalled (ADR 0003 §7).
const stallWarnAfter = 5 * time.Second

// errDrainPanicked is what a call returns when the drain it was parked on is
// settled as Panicked (ADR 0003 §4, G2). It is a sentinel, compared with
// errors.Is. The declaration's form is pinned; the text given to errors.New
// is not.
var errDrainPanicked = errors.New("blobfs: backpressure drain panicked")

// budget bounds the memory held by buffered writes. The wait is
// check-then-proceed: a writer is admitted while the charge is below the
// limit and charges afterwards, which is why the bound is the limit plus what
// admitted writers add rather than a hard ceiling (ADR 0003 §6).
//
// mu is a leaf lock. It may be taken while holding FS.mu or an openFile.mu,
// but no other lock is acquired under it, nothing waits on cond while holding
// either of those, and nothing is logged under it: a log handler may take
// locks of its own, or call back into the budget and deadlock on mu.
type budget struct {
	// warnAfter is how long an await call may stay blocked before it logs
	// the Warn record (ADR 0003 §7). newBudget sets it to stallWarnAfter. A
	// test may set it to any value before the budget is first used; nothing
	// writes it after that.
	warnAfter time.Duration

	log      *slog.Logger
	maxBytes int64 // the limit as built; <= 0 disables waiting

	mu     sync.Mutex
	cond   *sync.Cond // on mu; broadcast whenever a parked call may proceed
	charge int64      // Σadd − Σrelease, never clamped
	parked int        // calls waiting on cond; the drainer is not one

	// draining is set while a drain started by the budget runs, so that at
	// most one does: every stalled writer calling Sync would take a namespace
	// snapshot each, just when the bucket is already the bottleneck.
	draining bool

	// ended counts budget drains that have ended, however they ended.
	// failure is the latest that failed or panicked, and failedAt the value
	// of ended when it did. Keeping the failure apart from the count lets a
	// call that began when ended was s see a failure with failedAt > s even
	// after a later drain succeeded (G2), and ignore one from before it began
	// (G1).
	ended    uint64
	failure  error
	failedAt uint64
}

// newBudget returns a budget that admits a writer while used() < limit. A
// limit of zero or less disables waiting. log must not be nil.
func newBudget(limit int64, log *slog.Logger) *budget {
	b := &budget{
		warnAfter: stallWarnAfter,
		log:       log,
		maxBytes:  limit,
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// add charges n without waiting. release returns n and wakes every parked
// call. Both require n >= 0 and neither clamps, so used() is exactly
// Σadd − Σrelease.
func (b *budget) add(n int64) {
	b.mu.Lock()
	b.charge += n
	b.mu.Unlock()
}

// release broadcasts whatever it frees, because room can come from anywhere —
// the ticker, a COMMIT, a truncate, an unlink, or the budget's own drain as
// each file is flushed — and a parked call is admitted as soon as it finds
// room, not when the drain that made it settles (ADR 0003 §4).
func (b *budget) release(n int64) {
	b.mu.Lock()
	b.charge -= n
	b.cond.Broadcast()
	b.mu.Unlock()
}

func (b *budget) used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.charge
}

// limit reports the limit the budget was built with, unchanged.
func (b *budget) limit() int64 { return b.maxBytes }

// waiters reports how many await calls are parked waiting for a drain that
// another call is running. The drainer is not counted.
func (b *budget) waiters() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.parked
}

// await blocks until the charge is below the limit, electing one waiter at a
// time to run drain. It must be called holding no other lock. limit <= 0
// disables waiting; accounting continues regardless.
//
// It returns nil once admitted; the error drain returned, unchanged, when
// this call's own drain failed (G3); the latest failure of a budget drain
// that ended after this call began, errDrainPanicked for one settled as
// Panicked (G1, G2); or ctx.Err(). If this call is the drainer and a panic or
// runtime.Goexit ends its window before drain returns, in drain or in the
// Info record's log call, the drain is settled as Panicked and the panic or
// Goexit continues unchanged, so await does not return.
func (b *budget) await(ctx context.Context, drain func(context.Context) error) error {
	b.mu.Lock()
	// ctx counts only for a call that would otherwise block (Assumption 8),
	// which leaves a disabled budget, and a write that finds room, exactly
	// as they would be without backpressure.
	if b.maxBytes <= 0 || b.charge < b.maxBytes {
		b.mu.Unlock()
		return nil
	}
	start := b.ended
	entered := time.Now()

	// blocked is guarded by mu, and cleared in the same critical section that
	// decides how the call ends. Timer.Stop does not wait for a callback that
	// has already started, so the callback needs this to see that its call
	// has returned and log nothing.
	blocked := true
	warn := time.AfterFunc(b.warnAfter, func() {
		b.mu.Lock()
		if !blocked {
			b.mu.Unlock()
			return
		}
		waited := time.Since(entered)
		dirty := b.charge
		b.mu.Unlock()
		b.log.Warn("writes stalled waiting for commit",
			slog.Duration("waited", waited),
			slog.Int64("dirty_bytes", dirty),
			slog.Int64("limit", b.maxBytes))
	})
	defer warn.Stop()

	// stopWake is registered just before the call first parks. Cond.Wait
	// returns only when someone broadcasts, so without it a parked call would
	// not notice its ctx ending until some drain did. Taking mu in the
	// callback keeps the broadcast from falling between a check of ctx and
	// the Wait that follows it.
	var stopWake func() bool
	defer func() {
		if stopWake != nil {
			stopWake()
		}
	}()

	// inDrain is set from the moment this call is elected drainer until it
	// relocks mu after drain returns. It is still set on the way out only if
	// something in that window panicked or called runtime.Goexit: the Info
	// record as well as drain itself, so the deferred settle covers the whole
	// window and the Info record must stay inside it. Settling then is what
	// keeps the in-progress mark from staying set and parking every later
	// writer for good; the panic itself continues, so dispatch's recover still
	// answers SYSTEM_ERR. Nothing is logged for it, because dispatch logs the
	// panic.
	inDrain := false
	defer func() {
		if inDrain {
			b.mu.Lock()
			b.settle(errDrainPanicked)
			blocked = false
			b.mu.Unlock()
		}
	}()

	for {
		// A failure is checked before room, so every call parked on a failed
		// drain fails, however much that drain freed first (Assumption 9).
		if b.failure != nil && b.failedAt > start {
			err := b.failure
			blocked = false
			b.mu.Unlock()
			return err
		}
		if b.charge < b.maxBytes {
			blocked = false
			b.mu.Unlock()
			return nil
		}
		if err := ctx.Err(); err != nil {
			blocked = false
			b.mu.Unlock()
			return err
		}
		if b.draining {
			if stopWake == nil {
				stopWake = context.AfterFunc(ctx, func() {
					b.mu.Lock()
					b.cond.Broadcast()
					b.mu.Unlock()
				})
			}
			b.parked++
			b.cond.Wait()
			b.parked--
			continue
		}

		b.draining = true
		inDrain = true
		dirty := b.charge
		b.mu.Unlock()
		// Logged before drain is called, so that a drain that hangs has
		// already been announced.
		b.log.Info("commit triggered by write backpressure",
			slog.Int64("dirty_bytes", dirty),
			slog.Int64("limit", b.maxBytes))
		// drain is FS.Sync, which takes FS.mu and every openFile.mu, so it
		// runs holding no lock at all.
		err := drain(ctx)
		b.mu.Lock()
		inDrain = false

		switch {
		case err == nil:
			// Not admitted outright: a drain that freed nothing, or whose room
			// others took, would otherwise admit this call over the limit and
			// break the bound of §6.
			b.settle(nil)
		case ctx.Err() != nil:
			// Abandoned: the error is most likely this call's own
			// cancellation, and publishing it would fail other writers with
			// it (Assumption 10). A parked call drains next, with its own ctx.
			b.settle(nil)
			blocked = false
			b.mu.Unlock()
			return ctx.Err()
		default:
			b.settle(err)
			blocked = false
			b.mu.Unlock()
			b.log.Error("backpressure commit failed", slog.Any("err", err))
			return err
		}
	}
}

// settle ends the budget's drain, recording failure as the latest failure if
// it is not nil, and wakes every parked call to go round again. Called with
// mu held.
func (b *budget) settle(failure error) {
	b.draining = false
	b.ended++
	if failure != nil {
		b.failure = failure
		b.failedAt = b.ended
	}
	b.cond.Broadcast()
}
