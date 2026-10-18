package sunrpc

// Tests for Serve's accept loop, written from
// docs/adr/0016-a-failed-accept-is-retried-or-ends-in-the-final-commit.md:
//
//	§1 a temporary Accept error is retried after a bounded backoff, any other
//	   error is returned, and Serve returns nil exactly when ctx is done
//	§4 the test surface, S1 to S6
//
// They drive Serve through a listener that plays a script of Accept results
// over a real loopback listener, so that a scripted pass-through hands Serve a
// real TCP connection a client can call on. They use only names that existed
// before ADR 0016 (§4), so they build against the old Serve and fail against
// it. None of them names, or tests, a helper that computes the schedule (§1).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"strata/internal/xdr"
)

// acceptServeReturned is the failure for a Serve that returns while a test is
// waiting for anything else. So every script below that starts with a
// temporary error fails at once against a Serve that returns on it.
const acceptServeReturned = "Serve returned %v after a temporary Accept error; ADR 0016 §1 retries it"

// acceptWait bounds every wait in this file (ADR 0016 §4, Safety).
const acceptWait = 5 * time.Second

// acceptNoResetBound separates the two outcomes S6 tells apart: after a
// successful Accept the next temporary error waits 5 ms, and without the reset
// it would wait 640 ms (ADR 0016 §1, §4 S6).
const acceptNoResetBound = 320 * time.Millisecond

// acceptTemporary is the error a listener gives when the process runs out of
// descriptors, as ADR 0016 §4 pins it. EMFILE is temporary through
// syscall.Errno.Temporary, which OpError reaches by unwrapping the
// *os.SyscallError (§1).
func acceptTemporary() error {
	return &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
}

// acceptScriptListener wraps a real loopback listener and plays a script for
// successive Accept calls. It records when each call starts.
type acceptScriptListener struct {
	inner net.Listener

	// steps is the result of each Accept call in turn. A nil entry passes the
	// call through to inner.
	steps []error

	// then is the result of every call past the end of steps. Nil passes the
	// call through to inner.
	then error

	// called is signalled, without blocking, after each call is recorded.
	called chan struct{}

	mu     sync.Mutex
	starts []time.Time
}

var _ net.Listener = (*acceptScriptListener)(nil)

// acceptListen makes an acceptScriptListener over a fresh loopback listener,
// which is closed in cleanup.
func acceptListen(t *testing.T, steps []error, then error) *acceptScriptListener {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { inner.Close() })
	return &acceptScriptListener{
		inner:  inner,
		steps:  steps,
		then:   then,
		called: make(chan struct{}, 1),
	}
}

func (l *acceptScriptListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	n := len(l.starts)
	l.starts = append(l.starts, time.Now())
	err := l.then
	if n < len(l.steps) {
		err = l.steps[n]
	}
	l.mu.Unlock()

	select {
	case l.called <- struct{}{}:
	default:
	}

	if err != nil {
		return nil, err
	}
	return l.inner.Accept()
}

func (l *acceptScriptListener) Close() error { return l.inner.Close() }

func (l *acceptScriptListener) Addr() net.Addr { return l.inner.Addr() }

// callStarts returns a copy of the start time of every Accept call so far.
func (l *acceptScriptListener) callStarts() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Time(nil), l.starts...)
}

// waitCalls waits until there have been at least n Accept calls. It fails at
// once if Serve returns first, and after acceptWait if the calls never come.
func (l *acceptScriptListener) waitCalls(t *testing.T, n int, served <-chan error) {
	t.Helper()
	timeout := time.NewTimer(acceptWait)
	defer timeout.Stop()
	for {
		if got := len(l.callStarts()); got >= n {
			return
		}
		select {
		case <-l.called:
		case err := <-served:
			t.Fatalf(acceptServeReturned, err)
		case <-timeout.C:
			t.Fatalf("after %v there had been %d Accept calls, want at least %d: ADR 0016 §1 calls Accept again once a temporary error's wait is over", acceptWait, len(l.callStarts()), n)
		}
	}
}

// acceptServe registers a testHandler whose call writes one Uint32, and runs
// Serve on ln in a goroutine. It returns the channel Serve's result arrives on
// and the cancel function of Serve's context, which cleanup also calls.
func acceptServe(t *testing.T, ln net.Listener) (<-chan error, context.CancelFunc) {
	t.Helper()
	s := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Register(testProg, testVers, &testHandler{
		idempotent: func(proc uint32) bool { return proc == testProcIdem },
		call: func(_ context.Context, _ uint32, _ Cred, _ *xdr.Reader, res *xdr.Writer) error {
			res.Uint32(1)
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, ln) }()
	return served, cancel
}

// acceptDial connects to the real listener under ln, and closes the
// connection in cleanup.
func acceptDial(t *testing.T, ln *acceptScriptListener) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), acceptWait)
	if err != nil {
		t.Fatalf("dial %s: %v", ln.Addr(), err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// acceptRequireServing fails if Serve has returned, without waiting.
func acceptRequireServing(t *testing.T, served <-chan error) {
	t.Helper()
	select {
	case err := <-served:
		t.Fatalf(acceptServeReturned, err)
	default:
	}
}

// acceptRequireAnswer reads the reply to the call with the given xid, and
// requires it to be SUCCESS.
func acceptRequireAnswer(t *testing.T, conn net.Conn, xid uint32) {
	t.Helper()
	rec := testReadReply(t, conn, acceptWait)
	if stat, _ := testReplyHeader(t, rec, xid); stat != testAcceptSuccess {
		t.Fatalf("accept_stat = %d, want SUCCESS(%d): ADR 0016 §1 serves the connection a successful Accept returns as now", stat, testAcceptSuccess)
	}
}

// acceptAwaitReturn waits up to acceptWait for Serve's result.
func acceptAwaitReturn(t *testing.T, served <-chan error, why string) error {
	t.Helper()
	select {
	case err := <-served:
		return err
	case <-time.After(acceptWait):
		t.Fatalf("Serve had not returned after %v: %s", acceptWait, why)
		return nil
	}
}

// acceptRequireNilAfterCancel waits for Serve to return after its context has
// been cancelled, and requires nil (ADR 0016 §1: "So `Serve` returns nil
// exactly when `ctx` is done.").
func acceptRequireNilAfterCancel(t *testing.T, served <-chan error) {
	t.Helper()
	err := acceptAwaitReturn(t, served, "ADR 0016 §1 ends the wait on ctx, and returns nil on an Accept error once ctx is done")
	if err != nil {
		t.Fatalf("Serve returned %v after its context was cancelled, want nil: ADR 0016 §1 has Serve return nil exactly when ctx is done", err)
	}
}

// acceptShutDownServing closes the client connection, cancels Serve's
// context, and requires Serve to return nil within acceptWait. The test does
// not close the listener itself: a Serve blocked in a pass-through Accept must
// end on the cancel alone (ADR 0016 §1: "So `Serve` returns nil exactly when
// `ctx` is done."), and one that ignores the cancel fails at the bound rather
// than hanging. The listener is still closed in cleanup. The client
// connection is closed first so that the result does not depend on whether
// Serve waits for its connections, which ADR 0016 leaves to #82.
func acceptShutDownServing(t *testing.T, conn net.Conn, cancel context.CancelFunc, ln net.Listener, served <-chan error) {
	t.Helper()
	conn.Close()
	cancel()
	acceptRequireNilAfterCancel(t, served)
}

// TestServeSurvivesTemporaryAcceptErrors is S1 (ADR 0016 §4): three temporary
// errors, then a real connection. Serve does not return, and the call is
// answered. The gaps between the first four Accept calls are at least 5, 10
// and 20 ms, the first three waits of §1's schedule. These are lower bounds
// only. On cancel, Serve returns nil.
func TestServeSurvivesTemporaryAcceptErrors(t *testing.T) {
	temp := acceptTemporary()
	ln := acceptListen(t, []error{temp, temp, temp}, nil)
	served, cancel := acceptServe(t, ln)

	conn := acceptDial(t, ln)
	const xid uint32 = 0x16001
	testSend(t, conn, testCallRecord(xid, testProg, testVers, testProcIdem, nil))

	ln.waitCalls(t, 4, served)
	acceptRequireAnswer(t, conn, xid)
	acceptRequireServing(t, served)

	starts := ln.callStarts()
	if len(starts) < 4 {
		t.Fatalf("there were %d Accept calls, want at least 4: three temporary errors and the Accept that returned the connection (ADR 0016 §1, §4 S1)", len(starts))
	}
	for i, atLeast := range []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond} {
		if gap := starts[i+1].Sub(starts[i]); gap < atLeast {
			t.Errorf("Accept call %d started %v after call %d, want at least %v: ADR 0016 §1's schedule waits 5, 10 and 20 ms after the first three consecutive temporary errors", i+2, gap, i+1, atLeast)
		}
	}

	acceptShutDownServing(t, conn, cancel, ln, served)
}

// TestServeReturnsPermanentAcceptError is S2 (ADR 0016 §4): a non-temporary
// error is returned, matched with errors.Is, after exactly one Accept (§1:
// "any other error, it returns, as now").
func TestServeReturnsPermanentAcceptError(t *testing.T) {
	broke := errors.New("listener broke")
	ln := acceptListen(t, []error{broke}, nil)
	served, _ := acceptServe(t, ln)

	err := acceptAwaitReturn(t, served, "ADR 0016 §1 returns an Accept error that is not temporary")
	if !errors.Is(err, broke) {
		t.Errorf("Serve returned %v, want an error matching %q with errors.Is: ADR 0016 §1 returns any Accept error that is not temporary", err, broke)
	}
	if n := len(ln.callStarts()); n != 1 {
		t.Errorf("Serve made %d Accept calls before returning, want exactly 1: ADR 0016 §1 does not retry an error that is not temporary (§4 S2)", n)
	}
}

// TestServeReturnsNilWhenCancelledWhileRetrying is S3 (ADR 0016 §4): with
// every Accept failing temporarily, a cancel makes Serve return nil (§1: it
// "returns nil if `ctx` is done").
func TestServeReturnsNilWhenCancelledWhileRetrying(t *testing.T) {
	ln := acceptListen(t, nil, acceptTemporary())
	served, cancel := acceptServe(t, ln)

	ln.waitCalls(t, 3, served)
	cancel()
	acceptRequireNilAfterCancel(t, served)
}

// TestServeRetriesWrappedTemporaryError is S4 (ADR 0016 §4): a temporary error
// wrapped with %w is retried, because §1 finds the net.Error with errors.As
// (Assumption 6).
func TestServeRetriesWrappedTemporaryError(t *testing.T) {
	wrapped := fmt.Errorf("listener: %w", acceptTemporary())
	ln := acceptListen(t, []error{wrapped, wrapped}, nil)
	served, cancel := acceptServe(t, ln)

	conn := acceptDial(t, ln)
	const xid uint32 = 0x16004
	testSend(t, conn, testCallRecord(xid, testProg, testVers, testProcIdem, nil))

	ln.waitCalls(t, 3, served)
	acceptRequireAnswer(t, conn, xid)
	acceptRequireServing(t, served)

	acceptShutDownServing(t, conn, cancel, ln, served)
}

// TestServeReturnsPermanentErrorAfterTemporaryOnes is S5 (ADR 0016 §4): two
// temporary errors and then a non-temporary one, which is returned after
// exactly three Accepts (§1).
func TestServeReturnsPermanentErrorAfterTemporaryOnes(t *testing.T) {
	temp := acceptTemporary()
	permanent := errors.New("permanent")
	ln := acceptListen(t, []error{temp, temp, permanent}, nil)
	served, _ := acceptServe(t, ln)

	err := acceptAwaitReturn(t, served, "ADR 0016 §1 retries the two temporary errors and then returns the one that is not temporary")
	n := len(ln.callStarts())
	if !errors.Is(err, permanent) {
		t.Errorf("Serve returned %v after %d Accept calls, want an error matching %q with errors.Is: ADR 0016 §1 retries the two temporary errors and returns the one that is not temporary", err, n, permanent)
	}
	if n != 3 {
		t.Errorf("Serve made %d Accept calls before returning, want exactly 3: two temporary errors retried, then the one that is not temporary returned (ADR 0016 §1, §4 S5)", n)
	}
}

// TestServeResetsWaitAfterSuccessfulAccept is S6 (ADR 0016 §4): seven
// temporary errors, one success, then failures. §1: "A successful Accept
// resets the wait to 0", so the 9th call's temporary error waits 5 ms. Without
// the reset it would wait 640 ms, the eighth wait of the schedule, so the gap
// between the 9th and 10th Accept calls must be under 320 ms.
func TestServeResetsWaitAfterSuccessfulAccept(t *testing.T) {
	temp := acceptTemporary()
	steps := []error{temp, temp, temp, temp, temp, temp, temp, nil}
	ln := acceptListen(t, steps, temp)

	// Dial before Serve starts, so that the pass-through, the 8th call,
	// returns at once.
	acceptDial(t, ln)
	served, cancel := acceptServe(t, ln)

	ln.waitCalls(t, 10, served)

	starts := ln.callStarts()
	if gap := starts[9].Sub(starts[8]); gap >= acceptNoResetBound {
		t.Errorf("Accept call 10 started %v after call 9, want under %v: ADR 0016 §1 resets the wait after a successful Accept, so the temporary error after it waits 5 ms, where without the reset it would wait 640 ms", gap, acceptNoResetBound)
	}

	cancel()
	acceptRequireNilAfterCancel(t, served)
}
