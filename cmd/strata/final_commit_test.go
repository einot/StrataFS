package main

// Clean-room tests of cmd/strata's finalCommit and shutdownTimeout, written
// from docs/adr/0016-a-failed-accept-is-retried-or-ends-in-the-final-commit.md:
// §3 (finalCommit) and §4 (the test surface, C1 to C4).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// finalCommitRecorder records the calls finalCommit makes. Its sync returns
// syncErr, and keeps the context it was given, that context's Err at entry,
// and its deadline.
type finalCommitRecorder struct {
	syncErr error

	mu          sync.Mutex
	events      []string
	ctx         context.Context
	entryErr    error
	deadline    time.Time
	hasDeadline bool
}

func (r *finalCommitRecorder) endServing() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "end")
}

func (r *finalCommitRecorder) sync(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "sync")
	r.ctx = ctx
	r.entryErr = ctx.Err()
	r.deadline, r.hasDeadline = ctx.Deadline()
	return r.syncErr
}

func (r *finalCommitRecorder) eventList() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.events, ", ")
}

// TestFinalCommitCommitsWhateverEndedServing is C1 (ADR 0016 §4): §3 says
// finalCommit "calls `endServing` once and then `sync` once, whatever `cause`
// is", and that holds whether sync fails or not.
func TestFinalCommitCommitsWhateverEndedServing(t *testing.T) {
	for _, cause := range []error{nil, errors.New("serve: boom")} {
		for _, syncErr := range []error{nil, errors.New("disk full")} {
			r := &finalCommitRecorder{syncErr: syncErr}
			_ = finalCommit(cause, r.endServing, r.sync)
			if got, want := r.eventList(), "end, sync"; got != want {
				t.Errorf("finalCommit(%v, ...) with sync returning %v made the calls [%s], want [%s]: ADR 0016 §3 calls endServing once and then sync once, whatever cause is", cause, syncErr, got, want)
			}
		}
	}
}

// TestFinalCommitContext is C2 (ADR 0016 §4): §3 says sync's context "comes
// from `context.WithTimeout(context.Background(), shutdownTimeout)`, and is
// cancelled before `finalCommit` returns". So it is not done when sync is
// entered, its deadline lies in [t0 + shutdownTimeout, t1 + shutdownTimeout],
// where t0 and t1 are taken just before and just after the call, and it is
// done once finalCommit has returned.
func TestFinalCommitContext(t *testing.T) {
	r := &finalCommitRecorder{}
	t0 := time.Now()
	_ = finalCommit(nil, r.endServing, r.sync)
	t1 := time.Now()

	r.mu.Lock()
	ctx, entryErr, deadline, hasDeadline := r.ctx, r.entryErr, r.deadline, r.hasDeadline
	r.mu.Unlock()

	if ctx == nil {
		t.Fatalf("finalCommit never called sync: ADR 0016 §3 calls sync once, whatever cause is")
	}
	if entryErr != nil {
		t.Errorf("sync's context was already done when sync was entered, with %v: ADR 0016 §3 derives it from context.Background with a timeout, so nothing has cancelled it yet (§4 C2)", entryErr)
	}
	if !hasDeadline {
		t.Errorf("sync's context had no deadline: ADR 0016 §3 makes it with context.WithTimeout and shutdownTimeout (§4 C2)")
	} else {
		earliest, latest := t0.Add(shutdownTimeout), t1.Add(shutdownTimeout)
		if deadline.Before(earliest) || deadline.After(latest) {
			t.Errorf("sync's context has deadline %v, want it in [%v, %v]: ADR 0016 §3 makes it with context.WithTimeout(context.Background(), shutdownTimeout) during the call (§4 C2)", deadline, earliest, latest)
		}
	}
	if ctx.Err() == nil {
		t.Errorf("sync's context was not done after finalCommit returned: ADR 0016 §3 cancels it before finalCommit returns (§4 C2)")
	}
}

// TestFinalCommitResult is C3 (ADR 0016 §4): §3 says finalCommit "returns
// `errors.Join(cause, fmt.Errorf("final commit: %w", syncErr))` when `sync`
// failed, and `errors.Join(cause)` otherwise. So the result is nil exactly
// when both are nil." errors.Is holds for each non-nil input, and the text
// contains each input's text, and "final commit" when sync failed.
func TestFinalCommitResult(t *testing.T) {
	x := errors.New("serve: boom")
	y := errors.New("disk full")
	cases := []struct {
		cause   error
		syncErr error
	}{
		{nil, nil},
		{x, nil},
		{nil, y},
		{x, y},
	}
	for _, tc := range cases {
		r := &finalCommitRecorder{syncErr: tc.syncErr}
		err := finalCommit(tc.cause, r.endServing, r.sync)
		if tc.cause == nil && tc.syncErr == nil {
			if err != nil {
				t.Errorf("finalCommit(nil, ...) with sync returning nil = %v, want nil: ADR 0016 §3's result is nil exactly when both are nil", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("finalCommit(%v, ...) with sync returning %v = nil, want an error: ADR 0016 §3's result is nil exactly when both are nil", tc.cause, tc.syncErr)
			continue
		}
		msg := err.Error()
		if tc.cause != nil {
			if !errors.Is(err, tc.cause) {
				t.Errorf("finalCommit(%v, ...) with sync returning %v = %q, want errors.Is to hold for the cause: ADR 0016 §3 joins cause into the result (§4 C3)", tc.cause, tc.syncErr, msg)
			}
			if !strings.Contains(msg, tc.cause.Error()) {
				t.Errorf("finalCommit(%v, ...) with sync returning %v = %q, want the text to contain %q: ADR 0016 §3 joins cause into the result (§4 C3)", tc.cause, tc.syncErr, msg, tc.cause.Error())
			}
		}
		if tc.syncErr != nil {
			if !errors.Is(err, tc.syncErr) {
				t.Errorf("finalCommit(%v, ...) with sync returning %v = %q, want errors.Is to hold for sync's error: ADR 0016 §3 wraps it with %%w (§4 C3)", tc.cause, tc.syncErr, msg)
			}
			if !strings.Contains(msg, "final commit") {
				t.Errorf("finalCommit(%v, ...) with sync returning %v = %q, want the text to contain %q: ADR 0016 §3 wraps sync's error as \"final commit: %%w\" (§4 C3)", tc.cause, tc.syncErr, msg, "final commit")
			}
			if !strings.Contains(msg, tc.syncErr.Error()) {
				t.Errorf("finalCommit(%v, ...) with sync returning %v = %q, want the text to contain %q: ADR 0016 §3 wraps sync's error into the result (§4 C3)", tc.cause, tc.syncErr, msg, tc.syncErr.Error())
			}
		}
	}
}

// TestShutdownTimeout is C4 (ADR 0016 §4): §3 pins
// `const shutdownTimeout = 60 * time.Second`, today's value (Assumption 13).
func TestShutdownTimeout(t *testing.T) {
	want := 60 * time.Second
	if shutdownTimeout != want {
		t.Errorf("shutdownTimeout = %v, want %v: ADR 0016 §3 (§4 C4)", shutdownTimeout, want)
	}
}
