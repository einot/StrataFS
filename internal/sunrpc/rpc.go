// Package sunrpc implements the ONC RPC v2 (RFC 5531) server framing that
// NFSv3 and the MOUNT protocol ride on: record marking over TCP, call and
// reply headers, and AUTH_SYS credentials.
package sunrpc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"strata/internal/xdr"
)

// Message types and reply states from RFC 5531.
const (
	callMsg  = 0
	replyMsg = 1

	msgAccepted = 0
	msgDenied   = 1

	// accept_stat
	success      = 0
	progUnavail  = 1
	progMismatch = 2
	procUnavail  = 3
	garbageArgs  = 4
	systemErr    = 5

	// reject_stat
	rpcMismatch = 0
	authError   = 1

	authNone = 0
	authSys  = 1

	rpcVersion = 2
)

// maxRecord bounds a single RPC record. NFS writes cap out well under this;
// anything larger is a malformed or hostile client.
const maxRecord = 8 << 20

// Cred carries the AUTH_SYS identity a client asserts. NFS auth is advisory by
// design: the client states who it is and the server trusts it. Because this
// server listens on loopback only, that is the same trust boundary as the
// local user account.
type Cred struct {
	UID, GID uint32
	GIDs     []uint32
	Machine  string
}

// Root reports whether the caller claims uid 0, which NFS servers conventionally
// treat specially.
func (c Cred) Root() bool { return c.UID == 0 }

// Handler serves one procedure of one program version. It decodes arguments
// from args and writes its reply body into res. Returning ErrGarbageArgs makes
// the server reply GARBAGE_ARGS; any other error becomes SYSTEM_ERR.
type Handler interface {
	Call(ctx context.Context, proc uint32, cred Cred, args *xdr.Reader, res *xdr.Writer) error
	// HasProc reports whether a procedure number exists, so the server can
	// answer PROC_UNAVAIL rather than mis-decoding.
	HasProc(proc uint32) bool

	// Idempotent reports whether executing proc more than once is equivalent
	// to executing it once. The server caches and replays replies for
	// procedures where this is false, so that a retransmission does not
	// re-execute the operation. Implementations should answer false for any
	// procedure they are unsure of: a needless cache entry is cheap, a
	// wrongly re-executed operation is not.
	Idempotent(proc uint32) bool
}

var ErrGarbageArgs = errors.New("sunrpc: garbage arguments")

type progKey struct {
	prog, vers uint32
}

// Server dispatches RPC calls to registered programs over TCP.
type Server struct {
	mu       sync.RWMutex
	programs map[progKey]Handler
	versions map[uint32][]uint32 // prog -> supported versions, for PROG_MISMATCH
	log      *slog.Logger

	// maxInFlight bounds concurrent request goroutines per connection so a
	// client cannot make the server spawn without limit.
	maxInFlight int

	// cache replays replies to retransmitted non-idempotent calls. connSerial
	// names each accepted connection for its keys; it starts at zero and is
	// incremented before use, so no connection is ever 0 and none is reused.
	cache      *replyCache
	connSerial atomic.Uint64
}

func NewServer(log *slog.Logger) *Server {
	return &Server{
		programs:    make(map[progKey]Handler),
		versions:    make(map[uint32][]uint32),
		log:         log,
		maxInFlight: 64,
		cache:       newReplyCache(replyCacheMaxEntries, replyCacheMaxAge),
	}
}

func (s *Server) Register(prog, vers uint32, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.programs[progKey{prog, vers}] = h
	s.versions[prog] = append(s.versions[prog], vers)
}

// Serve accepts connections until the listener closes or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, conn)
	}
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	connID := s.connSerial.Add(1)
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	var writeMu sync.Mutex
	sem := make(chan struct{}, s.maxInFlight)
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		rec, err := readRecord(conn)
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				s.log.Debug("connection closed", "peer", conn.RemoteAddr(), "err", err)
			}
			return
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		// Requests are served concurrently: NFS clients pipeline several calls
		// over one connection and match replies by xid, so ordering is not
		// required and a slow call must not stall the rest.
		go func(rec []byte) {
			defer wg.Done()
			defer func() { <-sem }()
			reply := s.dispatch(ctx, connID, rec)
			if reply == nil {
				return
			}
			writeMu.Lock()
			defer writeMu.Unlock()
			if err := writeRecord(conn, reply); err != nil {
				s.log.Debug("write failed", "peer", conn.RemoteAddr(), "err", err)
				conn.Close()
			}
		}(rec)
	}
}

// dispatch decodes one call and produces the complete reply record, or nil if
// the message is not a call we should answer at all. connID is the serial of
// the connection rec arrived on, and scopes the duplicate request cache to it.
//
// The result is named so that the deferred cache resolution below sees
// whatever each return statement hands back, without every exit path having
// to remember to record it.
func (s *Server) dispatch(ctx context.Context, connID uint64, rec []byte) (reply []byte) {
	r := xdr.NewReader(rec)
	xid := r.Uint32()
	mtype := r.Uint32()
	if r.Err() != nil || mtype != callMsg {
		return nil
	}

	rpcvers := r.Uint32()
	prog := r.Uint32()
	vers := r.Uint32()
	proc := r.Uint32()
	if r.Err() != nil {
		return nil
	}
	if rpcvers != rpcVersion {
		return rejectMismatch(xid, rpcVersion, rpcVersion)
	}

	cred, err := readCred(r)
	if err != nil {
		return rejectAuth(xid)
	}
	// Verifier: we accept whatever the client sends. AUTH_SYS has no real
	// verifier, and we do not support RPCSEC_GSS.
	if _, err := readOpaqueAuth(r); err != nil {
		return rejectAuth(xid)
	}

	s.mu.RLock()
	h, ok := s.programs[progKey{prog, vers}]
	known := s.versions[prog]
	s.mu.RUnlock()

	if !ok {
		if len(known) > 0 {
			lo, hi := known[0], known[0]
			for _, v := range known {
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
			return acceptedErr(xid, progMismatch, func(w *xdr.Writer) {
				w.Uint32(lo)
				w.Uint32(hi)
			})
		}
		return acceptedErr(xid, progUnavail, nil)
	}
	if !h.HasProc(proc) {
		return acceptedErr(xid, procUnavail, nil)
	}

	// Everything above is a pure function of the call header and is never
	// cached. From here on a non-idempotent call's reply is cached whatever
	// its status, so a retransmission gets the original's bytes.
	if !h.Idempotent(proc) {
		k := replyKey{conn: connID, xid: xid, prog: prog, vers: vers, proc: proc}
		prior, state := s.cache.begin(k)
		switch state {
		case cacheHit:
			return prior
		case cacheInFlight:
			// The original is still executing; its reply will answer the
			// client, and the client's own retransmission covers a loss.
			return nil
		}
		// Runs on every way out, including a panic in the encoding below that
		// nothing recovers, so a key is never left in flight by a call that
		// has stopped executing.
		defer func() {
			if len(reply) == 0 {
				s.cache.abandon(k)
			} else {
				s.cache.finish(k, reply)
			}
		}()
	}

	body := xdr.NewWriter()
	callErr := func() (err error) {
		// A panic in a procedure must not take down the whole server; turn it
		// into SYSTEM_ERR for this one call.
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic in RPC handler", "prog", prog, "proc", proc, "panic", p)
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		return h.Call(ctx, proc, cred, r, body)
	}()

	switch {
	case errors.Is(callErr, ErrGarbageArgs):
		return acceptedErr(xid, garbageArgs, nil)
	case callErr != nil:
		s.log.Error("rpc handler failed", "prog", prog, "proc", proc, "err", callErr)
		return acceptedErr(xid, systemErr, nil)
	}

	w := xdr.NewWriter()
	replyHeader(w, xid, success)
	w.Fixed(body.Bytes())
	return w.Bytes()
}

func replyHeader(w *xdr.Writer, xid uint32, stat uint32) {
	w.Uint32(xid)
	w.Uint32(replyMsg)
	w.Uint32(msgAccepted)
	// Verifier returned to the client: always AUTH_NONE with an empty body.
	w.Uint32(authNone)
	w.Uint32(0)
	w.Uint32(stat)
}

func acceptedErr(xid, stat uint32, extra func(*xdr.Writer)) []byte {
	w := xdr.NewWriter()
	replyHeader(w, xid, stat)
	if extra != nil {
		extra(w)
	}
	return w.Bytes()
}

func rejectAuth(xid uint32) []byte {
	w := xdr.NewWriter()
	w.Uint32(xid)
	w.Uint32(replyMsg)
	w.Uint32(msgDenied)
	w.Uint32(authError)
	w.Uint32(1) // AUTH_BADCRED
	return w.Bytes()
}

func rejectMismatch(xid, lo, hi uint32) []byte {
	w := xdr.NewWriter()
	w.Uint32(xid)
	w.Uint32(replyMsg)
	w.Uint32(msgDenied)
	w.Uint32(rpcMismatch)
	w.Uint32(lo)
	w.Uint32(hi)
	return w.Bytes()
}

func readOpaqueAuth(r *xdr.Reader) (flavor uint32, err error) {
	flavor = r.Uint32()
	body := r.Opaque()
	if r.Err() != nil {
		return 0, r.Err()
	}
	_ = body
	return flavor, nil
}

func readCred(r *xdr.Reader) (Cred, error) {
	flavor := r.Uint32()
	body := r.Opaque()
	if r.Err() != nil {
		return Cred{}, r.Err()
	}
	if flavor != authSys {
		// AUTH_NONE and anything else map to nobody; the mount is still usable
		// for world-readable paths.
		return Cred{UID: 65534, GID: 65534}, nil
	}
	br := xdr.NewReader(body)
	br.Uint32() // stamp
	machine := br.String()
	uid := br.Uint32()
	gid := br.Uint32()
	n := br.Uint32()
	if br.Err() != nil {
		return Cred{}, br.Err()
	}
	if n > 16 { // RFC 5531 caps the auxiliary group list at 16
		n = 16
	}
	gids := make([]uint32, 0, n)
	for i := uint32(0); i < n; i++ {
		gids = append(gids, br.Uint32())
	}
	if br.Err() != nil {
		return Cred{}, br.Err()
	}
	return Cred{UID: uid, GID: gid, GIDs: gids, Machine: machine}, nil
}

// readRecord reads one complete RPC record, reassembling continuation fragments.
func readRecord(r io.Reader) ([]byte, error) {
	var out []byte
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, err
		}
		h := binary.BigEndian.Uint32(hdr[:])
		last := h&0x80000000 != 0
		n := int(h & 0x7fffffff)
		if len(out)+n > maxRecord {
			return nil, fmt.Errorf("sunrpc: record exceeds %d bytes", maxRecord)
		}
		frag := make([]byte, n)
		if _, err := io.ReadFull(r, frag); err != nil {
			return nil, err
		}
		out = append(out, frag...)
		if last {
			return out, nil
		}
	}
}

// writeRecord writes a reply as a single final fragment.
func writeRecord(w io.Writer, body []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body))|0x80000000)
	if _, err := w.Write(append(hdr[:], body...)); err != nil {
		return err
	}
	return nil
}
