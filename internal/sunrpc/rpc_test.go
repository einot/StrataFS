package sunrpc

// Server-level tests for the duplicate request cache, written against
// docs/adr/0002-duplicate-request-cache.md:
//
//	§2 the key names the connection instance, so entries never cross connections
//	§3 Handler.Idempotent is what the RPC layer asks the program
//	§5 the cached artefact is the complete encoded reply record, whatever its status
//	§6 three states, and duplicates that arrive mid-flight
//
// These drive the server over a real TCP socket rather than calling dispatch
// directly, so the record marking, the reply encoding and the cache are all
// exercised the way a client would exercise them.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"strata/internal/xdr"
)

const (
	testProg        uint32 = 987654
	testVers        uint32 = 1
	testProcNonIdem uint32 = 1
	testProcIdem    uint32 = 2
	testProcHighest uint32 = 2

	testAcceptSuccess     uint32 = 0 // RFC 5531 §9 accept_stat SUCCESS
	testAcceptGarbageArgs uint32 = 4 // RFC 5531 §9 accept_stat GARBAGE_ARGS
	testAcceptSystemErr   uint32 = 5 // RFC 5531 §9 accept_stat SYSTEM_ERR
)

// testHandler is a Handler whose behaviour each test supplies directly. §3
// pins the three methods.
type testHandler struct {
	idempotent func(proc uint32) bool
	call       func(ctx context.Context, proc uint32, cred Cred, args *xdr.Reader, res *xdr.Writer) error
}

var _ Handler = (*testHandler)(nil)

func (h *testHandler) HasProc(proc uint32) bool { return proc <= testProcHighest }

func (h *testHandler) Idempotent(proc uint32) bool { return h.idempotent(proc) }

func (h *testHandler) Call(ctx context.Context, proc uint32, cred Cred, args *xdr.Reader, res *xdr.Writer) error {
	return h.call(ctx, proc, cred, args, res)
}

// testServe registers h and serves it on a random loopback port.
func testServe(t *testing.T, h Handler) string {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer(log)
	s.Register(testProg, testVers, h)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ln.Close() })
	go s.Serve(ctx, ln)
	return ln.Addr().String()
}

func testDial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// testCallRecord builds one complete RPC call record: the 4-byte record
// marker followed by a CALL message with AUTH_NONE credentials and verifier.
func testCallRecord(xid, prog, vers, proc uint32, args []byte) []byte {
	w := xdr.NewWriter()
	w.Uint32(xid)
	w.Uint32(0) // mtype CALL
	w.Uint32(2) // RPC version
	w.Uint32(prog)
	w.Uint32(vers)
	w.Uint32(proc)
	w.Uint32(0) // cred flavor AUTH_NONE
	w.Uint32(0) // cred body length
	w.Uint32(0) // verifier flavor AUTH_NONE
	w.Uint32(0) // verifier body length

	body := append(append([]byte(nil), w.Bytes()...), args...)
	rec := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(rec[:4], uint32(len(body))|0x80000000)
	copy(rec[4:], body)
	return rec
}

func testSend(t *testing.T, conn net.Conn, rec []byte) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if _, err := conn.Write(rec); err != nil {
		t.Fatalf("write call record: %v", err)
	}
}

// testTryReadReply reads one complete reply record, reassembling fragments
// until the one whose high bit is set. Every read carries a deadline so a
// missing reply fails the test instead of hanging the suite.
func testTryReadReply(conn net.Conn, within time.Duration) ([]byte, error) {
	deadline := time.Now().Add(within)
	var out []byte
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		var hdr [4]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return nil, err
		}
		h := binary.BigEndian.Uint32(hdr[:])
		frag := make([]byte, int(h&0x7fffffff))
		if _, err := io.ReadFull(conn, frag); err != nil {
			return nil, err
		}
		out = append(out, frag...)
		if h&0x80000000 != 0 {
			return out, nil
		}
	}
}

func testReadReply(t *testing.T, conn net.Conn, within time.Duration) []byte {
	t.Helper()
	rec, err := testTryReadReply(conn, within)
	if err != nil {
		t.Fatalf("reading reply: %v", err)
	}
	return rec
}

// testExpectNoReply asserts that nothing arrives within the given window.
func testExpectNoReply(t *testing.T, conn net.Conn, within time.Duration, why string) {
	t.Helper()
	rec, err := testTryReadReply(conn, within)
	if err == nil {
		t.Fatalf("a %d-byte reply arrived when none should have: %s", len(rec), why)
	}
	var nerr net.Error
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("reading the connection failed with %v, want the read to time out with nothing to read: %s", err, why)
	}
}

// testReplyHeader checks the fixed part of an accepted reply and returns the
// accept_stat with a reader positioned at the reply body.
func testReplyHeader(t *testing.T, rec []byte, wantXID uint32) (uint32, *xdr.Reader) {
	t.Helper()
	r := xdr.NewReader(rec)
	if got := r.Uint32(); got != wantXID {
		t.Fatalf("reply xid = %d, want %d: RFC 5531 §9 requires the xid of a REPLY to match its CALL", got, wantXID)
	}
	if got := r.Uint32(); got != 1 {
		t.Fatalf("reply mtype = %d, want 1 (REPLY)", got)
	}
	if got := r.Uint32(); got != 0 {
		t.Fatalf("reply_stat = %d, want 0 (MSG_ACCEPTED)", got)
	}
	r.Uint32() // verifier flavor
	r.Opaque() // verifier body
	return r.Uint32(), r
}

// TestRetransmittedNonIdempotentCallIsReplayed covers §6 row 3 and §5: a
// second call with the same (xid, prog, vers, proc) on the same connection is
// answered from the cache, not executed again, and the bytes are identical.
func TestRetransmittedNonIdempotentCallIsReplayed(t *testing.T) {
	var executions atomic.Uint32
	h := &testHandler{
		idempotent: func(uint32) bool { return false },
		call: func(_ context.Context, _ uint32, _ Cred, _ *xdr.Reader, res *xdr.Writer) error {
			res.Uint32(executions.Add(1))
			return nil
		},
	}
	conn := testDial(t, testServe(t, h))

	const xid uint32 = 0x1234
	rec := testCallRecord(xid, testProg, testVers, testProcNonIdem, nil)

	testSend(t, conn, rec)
	first := testReadReply(t, conn, 5*time.Second)
	testSend(t, conn, rec)
	second := testReadReply(t, conn, 5*time.Second)

	if got := executions.Load(); got != 1 {
		t.Errorf("the handler ran %d times for one xid retransmitted once, want 1: ADR 0002 §6 row 3 says a done entry is written back without executing", got)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("the reply to the retransmission is not the reply to the original:\n first = %x\nsecond = %x\nADR 0002 §5 caches the complete encoded reply record, and because the xid is part of the key the replay is byte-identical with no rewriting", first, second)
	}

	stat, body := testReplyHeader(t, second, xid)
	if stat != testAcceptSuccess {
		t.Fatalf("accept_stat of the replayed reply = %d, want SUCCESS(%d)", stat, testAcceptSuccess)
	}
	if n := body.Uint32(); n != 1 {
		t.Errorf("the replayed reply reports execution %d, want 1: it must be the bytes sent for the original call (ADR 0002 §5)", n)
	}
}

// TestIdempotentCallIsNotCached covers §3 and §4's rationale: the RPC layer
// caches only what the program reports non-idempotent, so a procedure that
// answers true is executed every time it arrives.
func TestIdempotentCallIsNotCached(t *testing.T) {
	var executions atomic.Uint32
	h := &testHandler{
		idempotent: func(proc uint32) bool { return proc == testProcIdem },
		call: func(_ context.Context, _ uint32, _ Cred, _ *xdr.Reader, res *xdr.Writer) error {
			res.Uint32(executions.Add(1))
			return nil
		},
	}
	conn := testDial(t, testServe(t, h))

	const xid uint32 = 0x2345
	rec := testCallRecord(xid, testProg, testVers, testProcIdem, nil)

	testSend(t, conn, rec)
	testReadReply(t, conn, 5*time.Second)
	testSend(t, conn, rec)
	testReadReply(t, conn, 5*time.Second)

	if got := executions.Load(); got != 2 {
		t.Errorf("the handler ran %d times for two identical idempotent calls, want 2: ADR 0002 §3 has the server cache and replay replies only for procedures Idempotent reports false for, so an idempotent procedure must simply run again", got)
	}
}

// TestCacheIsPerConnection covers §2 and Assumption 1's second bullet: an
// entry can only ever be returned to the connection that created it. This is
// the direction that must not fail — a reply served across connections is
// accepted by a client as the result of an operation it never issued.
func TestCacheIsPerConnection(t *testing.T) {
	var executions atomic.Uint32
	h := &testHandler{
		idempotent: func(uint32) bool { return false },
		call: func(_ context.Context, _ uint32, _ Cred, _ *xdr.Reader, res *xdr.Writer) error {
			res.Uint32(executions.Add(1))
			return nil
		},
	}
	addr := testServe(t, h)

	const xid uint32 = 1 // this repo's own client starts every connection at 1
	rec := testCallRecord(xid, testProg, testVers, testProcNonIdem, nil)

	connA := testDial(t, addr)
	testSend(t, connA, rec)
	replyA := testReadReply(t, connA, 5*time.Second)

	connB := testDial(t, addr)
	testSend(t, connB, rec)
	replyB := testReadReply(t, connB, 5*time.Second)

	if got := executions.Load(); got != 2 {
		t.Errorf("the handler ran %d times for the same xid on two connections, want 2: ADR 0002 §2 keys on a server-assigned connection serial, so two connections have disjoint key spaces and neither call is a duplicate of the other", got)
	}

	statA, bodyA := testReplyHeader(t, replyA, xid)
	statB, bodyB := testReplyHeader(t, replyB, xid)
	if statA != testAcceptSuccess || statB != testAcceptSuccess {
		t.Fatalf("accept_stat = %d and %d, want SUCCESS(%d) for both", statA, statB, testAcceptSuccess)
	}
	nA, nB := bodyA.Uint32(), bodyB.Uint32()
	if nA != 1 || nB != 2 {
		t.Errorf("the two connections reported executions %d and %d, want 1 and 2: ADR 0002 §2 requires that an entry is only ever returned to the connection that created it, so the second connection must get its own execution and not the first's cached reply", nA, nB)
	}
	if bytes.Equal(replyA, replyB) {
		t.Errorf("both connections received byte-identical replies (%x): the second connection was served the first's cached entry, which ADR 0002 §2 point 4 calls silent, one-sided corruption", replyA)
	}
}

// TestErrorReplyIsCached covers §5's second paragraph: replies are cached
// whatever their status, including GARBAGE_ARGS. A client that never received
// the error reply is entitled to the same answer it missed.
func TestErrorReplyIsCached(t *testing.T) {
	var executions atomic.Uint32
	h := &testHandler{
		idempotent: func(uint32) bool { return false },
		call: func(_ context.Context, _ uint32, _ Cred, _ *xdr.Reader, res *xdr.Writer) error {
			if executions.Add(1) == 1 {
				return ErrGarbageArgs
			}
			res.Uint32(0)
			return nil
		},
	}
	conn := testDial(t, testServe(t, h))

	const xid uint32 = 0x3456
	rec := testCallRecord(xid, testProg, testVers, testProcNonIdem, nil)

	testSend(t, conn, rec)
	first := testReadReply(t, conn, 5*time.Second)
	testSend(t, conn, rec)
	second := testReadReply(t, conn, 5*time.Second)

	if got := executions.Load(); got != 1 {
		t.Errorf("the handler ran %d times, want 1: ADR 0002 §5 caches replies whatever their status, so the retransmission must not reach the handler at all", got)
	}
	if stat, _ := testReplyHeader(t, first, xid); stat != testAcceptGarbageArgs {
		t.Fatalf("accept_stat of the first reply = %d, want GARBAGE_ARGS(%d)", stat, testAcceptGarbageArgs)
	}
	if stat, _ := testReplyHeader(t, second, xid); stat != testAcceptGarbageArgs {
		t.Errorf("accept_stat of the replayed reply = %d, want GARBAGE_ARGS(%d): ADR 0002 §5 declines to carve out statuses, because that introduces judgement at exactly the place where judgement caused the bug", stat, testAcceptGarbageArgs)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("the replayed error reply differs from the original:\n first = %x\nsecond = %x\nADR 0002 §5 requires byte-identical replay for error replies too", first, second)
	}
}

// TestMidFlightDuplicateIsDropped covers §6 row 2: a duplicate that arrives
// while the original is still executing gets no reply at all. Dropping it is
// required, not optional — the failure the issue describes is a client timing
// out while the server is still working, so a naive check-then-execute loses
// exactly the race the cache exists to win.
func TestMidFlightDuplicateIsDropped(t *testing.T) {
	var executions atomic.Uint32
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	h := &testHandler{
		idempotent: func(uint32) bool { return false },
		call: func(_ context.Context, _ uint32, _ Cred, _ *xdr.Reader, res *xdr.Writer) error {
			n := executions.Add(1)
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			res.Uint32(n)
			return nil
		},
	}
	conn := testDial(t, testServe(t, h))

	const xid uint32 = 0x4567
	rec := testCallRecord(xid, testProg, testVers, testProcNonIdem, nil)

	testSend(t, conn, rec)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never started for the first call")
	}

	// The duplicate arrives while the original is still in the handler.
	testSend(t, conn, rec)
	testExpectNoReply(t, conn, 500*time.Millisecond,
		"the original call is still executing, so nothing can be answered yet and ADR 0002 §6 row 2 requires the duplicate to be dropped rather than answered or errored")

	close(release)
	first := testReadReply(t, conn, 5*time.Second)

	// Only the original is answered. The duplicate produced nothing.
	testExpectNoReply(t, conn, 500*time.Millisecond,
		"ADR 0002 §6 row 2 says send nothing for a duplicate that arrives in flight; the client's own retransmission timer is the recovery mechanism (Assumption 7)")

	// The client retransmits again; by now the entry is done.
	testSend(t, conn, rec)
	third := testReadReply(t, conn, 5*time.Second)

	if got := executions.Load(); got != 1 {
		t.Errorf("the handler ran %d times, want 1: neither the mid-flight duplicate nor the later retransmission may re-execute the operation (ADR 0002 §6)", got)
	}
	if !bytes.Equal(first, third) {
		t.Errorf("the retransmission after completion was not answered with the original's bytes:\n first = %x\n third = %x\nADR 0002 §6 says the client retransmits again and by then the entry is done and it gets the cached reply", first, third)
	}
	if stat, body := testReplyHeader(t, third, xid); stat != testAcceptSuccess {
		t.Errorf("accept_stat of the cached reply = %d, want SUCCESS(%d)", stat, testAcceptSuccess)
	} else if n := body.Uint32(); n != 1 {
		t.Errorf("the cached reply reports execution %d, want 1 (ADR 0002 §5)", n)
	}
}

// TestPanickingHandlerIsAnsweredAndCached covers §6's "Panics" bullet, §5 and
// Assumption 15: a panic inside Handler.Call is converted into a SYSTEM_ERR
// reply by the recover dispatch already wraps the handler call in, that reply
// is an ordinary reply as far as the cache is concerned, and a retransmission
// is answered with the same bytes without re-entering the handler. §6 states
// the observable as "a key is never left in flight by a call that is no longer
// executing" — a call whose handler panicked has stopped executing, so its
// retransmission must be answered rather than dropped as a mid-flight
// duplicate.
//
// The handler panics on its first invocation only and writes an ordinary
// success body on any later one, so a wrongly re-executed call fails the
// byte-equality assertion loudly instead of panicking a second time. Nothing
// here tests a panic outside Handler.Call: §6 says that path is deliberately
// not recovered and takes the process down.
func TestPanickingHandlerIsAnsweredAndCached(t *testing.T) {
	const laterBody uint32 = 0xfeedface

	var executions atomic.Uint32
	h := &testHandler{
		idempotent: func(uint32) bool { return false },
		call: func(_ context.Context, _ uint32, _ Cred, _ *xdr.Reader, res *xdr.Writer) error {
			if executions.Add(1) == 1 {
				panic("test handler panics on its first invocation")
			}
			res.Uint32(laterBody)
			return nil
		},
	}
	conn := testDial(t, testServe(t, h))

	const xid uint32 = 0x5678
	rec := testCallRecord(xid, testProg, testVers, testProcNonIdem, nil)

	testSend(t, conn, rec)
	first, err := testTryReadReply(conn, 5*time.Second)
	if err != nil {
		t.Fatalf("no reply arrived for a call whose handler panicked: %v. ADR 0002 §6 says a panic inside Handler.Call is converted into a SYSTEM_ERR reply by the recover dispatch already wraps the handler call in", err)
	}
	if stat, _ := testReplyHeader(t, first, xid); stat != testAcceptSystemErr {
		t.Fatalf("accept_stat for a call whose handler panicked = %d, want SYSTEM_ERR(%d): ADR 0002 §6 pins the conversion of a handler panic into a SYSTEM_ERR reply, because the cache now depends on it", stat, testAcceptSystemErr)
	}

	// The retransmission must be answered — not dropped as a mid-flight
	// duplicate — and must not reach the handler.
	testSend(t, conn, rec)
	second, err := testTryReadReply(conn, 5*time.Second)
	if err != nil {
		t.Fatalf("no reply arrived for a retransmission of a call whose handler panicked: %v. ADR 0002 §6 requires that a key is never left in flight by a call that is no longer executing, so the retransmission must be answered from the cache and never dropped", err)
	}
	if got := executions.Load(); got != 1 {
		t.Errorf("the handler ran %d times for one xid retransmitted once, want 1: ADR 0002 §6 says finish stores the SYSTEM_ERR reply and a retransmission is answered with the same bytes without re-entering the handler, so a call whose handler panicked is executed exactly once", got)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("the reply to the retransmission is not the reply to the original:\n first = %x\nsecond = %x\nADR 0002 §5 caches replies whatever their status, including SYSTEM_ERR, and requires byte-identical replay (Assumption 15)", first, second)
	}
	if stat, _ := testReplyHeader(t, second, xid); stat != testAcceptSystemErr {
		t.Errorf("accept_stat of the replayed reply = %d, want SYSTEM_ERR(%d): ADR 0002 §5 declines to carve out statuses, and Assumption 15 judges that replaying the error a client was already told beats re-entering a handler that panicked", stat, testAcceptSystemErr)
	}

	// A different xid on the same connection still gets a normal reply: the
	// panic neither killed the server nor wedged the connection.
	const otherXID uint32 = 0x5679
	testSend(t, conn, testCallRecord(otherXID, testProg, testVers, testProcNonIdem, nil))
	third, err := testTryReadReply(conn, 5*time.Second)
	if err != nil {
		t.Fatalf("no reply arrived for a later call with a different xid on the same connection: %v. ADR 0002 §6 converts a panic inside Handler.Call into a reply, so the panic may neither take the server down nor wedge the connection it arrived on", err)
	}
	stat, body := testReplyHeader(t, third, otherXID)
	if stat != testAcceptSuccess {
		t.Fatalf("accept_stat for a later call with a different xid = %d, want SUCCESS(%d): a recovered handler panic must not affect the calls that follow it (ADR 0002 §6)", stat, testAcceptSuccess)
	}
	if n := body.Uint32(); n != laterBody {
		t.Errorf("the later call replied with body %#x, want %#x: it must be this call's own reply and not a replay of the panicking call's entry, which ADR 0002 §2 keys on the xid precisely to prevent", n, laterBody)
	}
	if got := executions.Load(); got != 2 {
		t.Errorf("the handler ran %d times in total, want 2: one panicking invocation, no re-execution for the retransmission (ADR 0002 §6), and one for the later call with a different xid", got)
	}
}
