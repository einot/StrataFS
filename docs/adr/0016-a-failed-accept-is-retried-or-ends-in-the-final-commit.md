# 0016. A failed Accept is retried or ends in the final commit

**Status:** Accepted
**Date:** 2026-10-18
**Issue:** #92

## Context

**The owner's decisions.** Two, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So recommendation 13 stands, as delegated: retry
   temporary Accept errors with a net/http-style bounded backoff, with the
   values re-read from its source, and route every exit after `New` through
   the final commit.
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." This change is not breaking (Assumption 15),
   so nothing here relies on it.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `9f97408`, the commit this ADR's branch was cut from
(Assumption 18). Code is cited by symbol.

**The defect.**

- `Serve`, in `internal/sunrpc/rpc.go`, returns on any Accept error: nil when
  its context is done, and the error otherwise.
- `run`, in `cmd/strata/main.go`, then returns before its final `Sync`, and
  `main` exits with status 1, which cuts off the committer's final `Sync`:
  `run`'s deferred `stop` cancels the signal context on the way out, and the
  committer starts that `Sync` when it sees it done. So every change since the
  last commit is lost, namespace changes the server had acknowledged included.
- Any local process can exhaust the server's descriptors, because connections
  are unbounded and an idle one is never closed: `Serve` accepts every
  connection, and `serveConn` reads each with no deadline. Accept then fails
  with EMFILE, which `Serve` returns, so any local user can stop the server,
  and lose those changes, on demand.

**The issues.** #92 is "A failed Accept stops the server without its final
commit", the title ADR 0014 records for it. Its body says "The server loses
what a crash loses, including namespace changes that were acknowledged
(#83)." and "#82 owns the full shutdown design." This ADR cites #82 and #83
only as those two sentences do (Assumption 19).

**What the ADRs say.** ADR 0013 Assumption 7 names both of these exits,
`run` returning on a `Serve` error and the committer's final `Sync` cut off,
among the exits that skip the final commit, which its crash model counts as
crashes (ADR 0013 §4). ADR 0014 §8 gives the server, connection and process
lifecycle machines to the lifecycle ADR.

**What this constrains.** `Serve`'s accept loop in `internal/sunrpc`, and `run`
in `cmd/strata`, whose package gains `finalCommit` and `shutdownTimeout`.
Nothing on the wire changes, and no flag or format.

## Decision

### 1. `Serve`'s accept loop

- A successful Accept resets the wait to 0, and the connection is served as
  now.
- On an Accept error:
  - if `ctx` is done, `Serve` returns nil;
  - otherwise, if `errors.As` finds a `net.Error` whose `Temporary()` is true,
    it:
    - takes the next wait in the schedule below;
    - logs one Warn record, with the error and the wait;
    - waits for the timer or for `ctx`, whichever comes first;
    - returns nil if `ctx` is done, and otherwise calls Accept again;
  - any other error, it returns, as now.
- **The schedule.** Let w be the last wait, which is 0 at the start and after a
  success. The next wait is 5 ms if w ≤ 0. Otherwise it is the smaller of 1 s
  and the larger of 5 ms and 2·w, computed so that it cannot overflow. So
  consecutive temporary errors wait 5, 10, 20, 40, 80, 160, 320 and 640 ms,
  then 1 s from then on, and a success starts the sequence again at 5 ms.
- So `Serve` returns nil exactly when `ctx` is done.
- **What counts as temporary** is Go's own classification:
  - EMFILE, ENFILE, EINTR, EAGAIN/EWOULDBLOCK and ETIMEDOUT, through
    `syscall.Errno.Temporary`;
  - ECONNRESET and ECONNABORTED, through `OpError`'s accept case, as its
    comment describes;
  - ENOBUFS, ENOMEM and a closed listener are not temporary.

  These are what `Temporary()` answers. Fewer reach `Serve` as temporary from
  the listener `run` makes, a `*net.TCPListener`. On Linux and macOS,
  `internal/poll` retries EINTR and ECONNABORTED, and waits out EAGAIN, before
  `Accept` returns. Every errno the accept path does return reaches the
  `OpError` inside an `*os.SyscallError`, which the accept case does not
  match, since it tests for a bare `syscall.Errno`. So from that listener
  ECONNRESET is not temporary, and `Serve` returns on it. This note restates
  Go's behaviour and changes no rule above (*Sources*).
- An unexported helper may compute the schedule. Its name is not part of §4's
  test surface.

### 2. `run`

- Right after `New` returns, and its error has been checked as now:
  `serveCtx, endServing := context.WithCancel(ctx)`.
- `fs.Run` and `rpc.Serve` run under `serveCtx`.
- Immediately after that, `run` defers
  `err = finalCommit(err, endServing, fs.Sync)`, using a named result. So
  every return after `New` makes the final commit, whether it comes from a
  signal, a Serve error, a failure to listen, or a path added later.
- `stop` stays deferred where it is, so it runs after the final commit. A
  second signal therefore stays caught until then, as today.
- On a Serve error, `run` logs it at Error and returns
  `fmt.Errorf("serve: %w", err)`.
- On a signal, or a nil from `Serve`, it logs "shutting down, committing" and
  returns nil.
- The "clean shutdown" record is logged, as now, only when the final result is
  nil.
- The exit status stays 1 for any error.

### 3. `finalCommit`

```go
// cmd/strata
const shutdownTimeout = 60 * time.Second
func finalCommit(cause error, endServing func(), sync func(context.Context) error) error
```

- It calls `endServing` once and then `sync` once, whatever `cause` is.
- `sync`'s context comes from
  `context.WithTimeout(context.Background(), shutdownTimeout)`, and is
  cancelled before `finalCommit` returns.
- It returns `errors.Join(cause, fmt.Errorf("final commit: %w", syncErr))`
  when `sync` failed, and `errors.Join(cause)` otherwise. So the result is nil
  exactly when both are nil.
- It logs nothing.

### 4. The test surface

- **Names a test may use**, restated from the code at `9f97408`:

  ```go
  // internal/sunrpc
  func NewServer(log *slog.Logger) *Server
  func (s *Server) Register(prog, vers uint32, h Handler)
  func (s *Server) Serve(ctx context.Context, ln net.Listener) error
  type Handler interface {
      Call(ctx context.Context, proc uint32, cred Cred, args *xdr.Reader, res *xdr.Writer) error
      HasProc(proc uint32) bool
      Idempotent(proc uint32) bool
  }
  // cmd/strata: shutdownTimeout and finalCommit, as in §3
  ```

  Every `sunrpc` name above exists before this ADR. So the `sunrpc` tests fail
  against the old `Serve`, rather than failing to build.
- In S1 to S6, the temporary error is the one a listener gives when it runs
  out of descriptors:
  `&net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}`.
- **S1.** Three temporary errors, then a real connection. `Serve` does not
  return, and the call is answered. The gaps between the first four Accept
  calls are at least 5, 10 and 20 ms. On cancel, `Serve` returns nil.
- **S2.** A non-temporary error is returned, matched with `errors.Is`, after
  exactly one Accept.
- **S3.** With every Accept failing temporarily, a cancel makes `Serve` return
  nil.
- **S4.** A temporary error wrapped with `%w` is retried.
- **S5.** Two temporary errors and then a non-temporary one: that one is
  returned, after exactly three Accepts.
- **S6.** Seven temporary errors, one success, then failures: the gap between
  the 9th and 10th Accept calls is under 320 ms. With the reset it is 5 ms;
  without it, 640 ms.
- **C1.** `endServing` and then `sync`, once each, for `cause` nil and non-nil,
  with `sync` failing or not.
- **C2.** `sync`'s context:
  - is not done when `sync` is entered;
  - has a deadline in [t0 + `shutdownTimeout`, t1 + `shutdownTimeout`], where
    t0 and t1 are taken just before and just after the call;
  - is done after `finalCommit` returns.
- **C3.** The result:
  - (nil, nil) gives nil;
  - otherwise `errors.Is` holds for each non-nil input;
  - the text contains each input's text, and "final commit" when `sync`
    failed.
- **C4.** `shutdownTimeout == 60 * time.Second`.
- **Safety:** every wait is bounded at 5 s or less, and contexts and listeners
  are released in cleanup.
- **What only review can show:**
  - the schedule beyond its first three waits, including the 1 s cap, which no
    test reaches, so that the suite stays fast;
  - that the defer is placed right after `New`;
  - `serveCtx`;
  - `stop`'s position;
  - that the wait ends on `ctx`;
  - the Warn record;
  - that the committer and `Commit` still go through `Sync`, which stays
    ADR 0012 §9's review item.

## Assumptions

Each is mine unless it says otherwise.

1. Status Accepted on creation, delegated by the owner's 2026-10-10 words
   above.
2. Scope is recommendation 13, delegated by the same words.
3. A small fix gets an ADR, so that `Serve`'s new contract and the names its
   tests use are pinned in a doc test-author reads. Today no doc pins them:
   ADR 0002 §3 pins `Handler`, earlier ADRs name `Serve` and `NewServer` only
   in prose, and none names `Register`.
4. The values are net/http's, as recommendation 13 asks. The 5 ms floor for a
   small wait, and the overflow-free form, are mine.
5. The schedule's helper is not pinned, so that the sunrpc tests use only names
   that already exist and show the defect against the old code. The cost is
   that the cap is checked by review.
6. `errors.As`, where net/http uses a type assertion. They agree for
   `*net.TCPListener`, whose Accept returns the `*OpError` itself, and
   `errors.As` also covers a listener that wraps its errors.
7. `Temporary()` is used although it is deprecated, as net/http still does for
   accept errors.
8. The wait ends on `ctx`, where net/http sleeps.
9. One Warn record per retry, as net/http logs one. A local user can already
   cause a Warn record per request, by asking MOUNT for a path that is not the
   export (`MountServer.Call`'s "mount refused for unknown export"), so this
   adds no new way to flood the log.
10. The child serving context, and `stop`'s place, keep a second signal caught
    through the final commit (`signal.NotifyContext`'s documentation).
11. A failure to listen goes through the final commit too, although nothing is
    dirty right after `New`: `initEmpty` commits, and `loadOrInit` loads
    clean.
12. `finalCommit`'s signature, and `errors.Join`.
13. 60 s is today's value. The committer's own 30 s is unchanged.
14. The exit status is 1 on a Serve error, because the server did fail.
15. Not breaking: no deployment needs to act.
16. ENOBUFS and ENOMEM are not retried, following Go's classification.
17. No change to `docs/vocabulary.md`. V5's names stay reserved for the
    lifecycle ADR. `finalCommit` is the Go site its `FinalCommit` action can
    trace to, which that ADR decides.
18. Read, not run. Every statement about the code was checked by reading it at
    `9f97408`, and nothing was run.
19. #82 and #83 are cited only as #92 cites them, in #92's body as the session
    gave it, verbatim from the tracker: "The server loses what a crash loses,
    including namespace changes that were acknowledged (#83)." and "#82 owns
    the full shutdown design." The texts of #82 and #83 were not read. #92's
    title is as ADR 0014 records it from the session. The parts of the
    shutdown design listed under *What this does not decide* are the brief's
    account, not #92's.

## Alternatives considered

- **An explicit list of errnos.** It diverges from net/http, and needs a list
  per platform.
- **`time.Sleep`.** A shutdown would then wait out the sleep.
- **Calling `stop` before the commit.** NotifyContext's documentation shows
  that a second signal would then exit the process in the middle of the
  commit.
- **Moving Listen before `New`.** It reorders start-up, and the defer makes it
  unnecessary.
- **A lifecycle machine now.** ADR 0014 §8 gives that to the lifecycle ADR.
- **Retrying every error.** A closed listener would spin.
- **Stopping on a temporary error, after committing.** A local user could
  still stop the server on demand.
- **A table test of a pinned schedule helper.** It is deterministic, but it
  would make the sunrpc tests fail to build against the old code, which hides
  whether they catch the defect.

## Consequences

- Running out of descriptors no longer stops the server.
- While it lasts, no new connection is served.
- A Serve failure costs the time of the final commit before strata exits.

## What this does not decide

- The full shutdown design, which #82 owns, as #92 says: draining
  connections, calls answered after the final commit, and the committer's
  final `Sync`, which can still be cut off after `run` returns.
  When `run`'s own commit has succeeded, that `Sync` holds only what it left
  out: changes made while `run`'s commit ran, or after it.
- Bounding connections, and idle timeouts, which are a follow-up.
- The lifecycle machines.
- ENOBUFS and ENOMEM, and the network errors that Linux's accept(2) says to
  retry like EAGAIN: ENETDOWN, EPROTO, ENOPROTOOPT, EHOSTDOWN, ENONET,
  EHOSTUNREACH, EOPNOTSUPP and ENETUNREACH. Go classes none of them as
  temporary, so `Serve` returns on each.
- A panic in `run`.
- §2 and §3 are interim: the lifecycle ADR may replace them with its machines.
  §1 is expected to stand.

## Sources

Fetched on 2026-10-18, while this ADR was written, through a tool that
summarises what it fetches, and quoted as it returned them when asked for
verbatim text. It quoted at most about 125 characters at once, so a longer
passage was asked for in pieces, which are joined here, and it dropped the
pages' Markdown escapes. The tool keeps a page for 15 minutes, so a fetch may
have come from its cache; I could not tell whether any did. The brief this ADR
was written from cites the first eight pages below from fetches earlier that
day. Each of them was fetched again, and matched the brief's quotations. The
last four entries, five pages in all, were fetched for this ADR alone. Asked
for the Go release they show,
the pages for `net/http/server.go` and `net/net.go` named none.

- **net/http's `Server.Serve`**: <https://go.dev/src/net/http/server.go>, read
  from character 100,000 of its 181,712, since the first 100,000 do not hold
  the method. Its accept loop declares
  `var tempDelay time.Duration // how long to sleep on accept failure`. After
  `if s.shuttingDown() {` has returned `ErrServerClosed`, it tests
  `if ne, ok := err.(net.Error); ok && ne.Temporary() {`, sets
  `tempDelay = 5 * time.Millisecond` when `tempDelay == 0` and otherwise
  `tempDelay *= 2`, caps it with `if max := 1 * time.Second; tempDelay > max {`,
  logs `s.logf("http: Accept error: %v; retrying in %v", err, tempDelay)`,
  calls `time.Sleep(tempDelay)` and continues; it returns any other error with
  `return err`. After a successful Accept it sets `tempDelay = 0`. (§1;
  Assumptions 4, 6, 7, 8 and 9)
- **`net.Error` and `OpError`**: <https://go.dev/src/net/net.go>. `Error`'s
  `Temporary` carries "Deprecated: Temporary errors are not well-defined. Most
  "temporary" errors are timeouts, and the few exceptions are surprising. Do
  not use this method." `OpError.Temporary` opens with the comment "Treat
  ECONNRESET and ECONNABORTED as temporary errors when they come from calling
  accept. See issue 6163." and `if e.Op == "accept" && isConnError(e.Err) {`.
  It then unwraps an `*os.SyscallError`, with
  `if ne, ok := e.Err.(*os.SyscallError); ok {` and
  `t, ok := ne.Err.(temporary)`, and otherwise asks `e.Err` itself. (§1;
  Assumption 7)
- **`syscall.Errno`** on Unix: <https://go.dev/src/syscall/syscall_unix.go>.
  `Temporary` is `return e == EINTR || e == EMFILE || e == ENFILE || e.Timeout()`,
  and `Timeout` is `return e == EAGAIN || e == EWOULDBLOCK || e == ETIMEDOUT`.
  (§1; Assumption 16)
- **`TCPListener.Accept`**: <https://go.dev/src/net/tcpsock.go>. After
  `if !l.ok() {` has returned `syscall.EINVAL`, a failed accept returns
  `return nil, &OpError{Op: "accept", Net: l.fd.net, Source: nil, Addr: l.fd.laddr, Err: err}`.
  (§1; Assumption 6)
- **`netFD.accept`**: <https://go.dev/src/net/fd_unix.go>, `//go:build unix`.
  `d, rsa, errcall, err := fd.pfd.Accept()`, and then, when `errcall != ""`,
  `err = wrapSyscallError(errcall, err)`. (§1)
- **`wrapSyscallError`**: <https://go.dev/src/net/error_posix.go>. Its doc
  comment: "wrapSyscallError takes an error and a syscall name. If the error
  is a syscall.Errno, it wraps it in an os.SyscallError using the syscall
  name." Its body is `if _, ok := err.(syscall.Errno); ok {`,
  `err = os.NewSyscallError(name, err)` and `return err`. **Note:** the brief
  gave this page as a summary; the body here was asked for one line at a time.
  (§1)
- **`errNetClosing`**: <https://go.dev/src/internal/poll/fd.go>.
  `func (e errNetClosing) Temporary() bool { return false }`, and of
  `ErrNetClosing`: "ErrNetClosing is returned when a network descriptor is
  used after it has been closed." (§1)
- **`signal.NotifyContext`**: <https://go.dev/src/os/signal/signal.go>. Its
  documentation: "The stop function unregisters the signal behavior, which,
  like [signal.Reset], may restore the default behavior for a given signal.
  For example, the default behavior of a Go program receiving [os.Interrupt]
  is to exit." and "Future interrupts received will not trigger the default
  (exit) behavior until the returned stop function is called."
  `signalCtx.stop` runs `c.cancel(nil)` and then `Stop(c.ch)`. (§2;
  Assumption 10; *Alternatives considered*)
- **`isConnError`** on Unix: <https://go.dev/src/net/error_unix.go>,
  `//go:build unix || js || wasip1`. Its body is
  `if se, ok := err.(syscall.Errno); ok {`,
  `return se == syscall.ECONNRESET || se == syscall.ECONNABORTED`, `}` and
  `return false`, so it matches only a bare `syscall.Errno`. (§1)
- **`FD.Accept`** in `internal/poll`:
  <https://go.dev/src/internal/poll/fd_unix.go>,
  `//go:build unix || (js && wasm) || wasip1`. Its loop's `switch err {` has
  `case syscall.EINTR:`, which continues; `case syscall.EAGAIN:`, which waits
  with `fd.pd.waitRead(fd.isFile)` when the descriptor is pollable and then
  continues; and `case syscall.ECONNABORTED:`, under the comment "This means
  that a socket on the listen queue was closed before we Accept()ed it; it's a
  silly error, so try again.", which continues. Any other error ends in
  `return -1, nil, errcall, err`. (§1)
- **`internal/poll`'s `accept`**:
  <https://go.dev/src/internal/poll/sock_cloexec.go>,
  `//go:build dragonfly || freebsd || linux || netbsd || openbsd`, returns
  `return -1, nil, "accept4", err` on any error; and
  <https://go.dev/src/internal/poll/sys_cloexec.go>,
  `//go:build aix || darwin || (js && wasm) || wasip1`, returns
  `return -1, nil, "accept", err`, or `return -1, nil, "setnonblock", err`.
  So on Linux and macOS every errno of the accept path comes with a call's
  name, and `netFD.accept` wraps it. That from a `*net.TCPListener` the
  accept case of `OpError.Temporary` therefore never matches is my reading of
  these two files together with `OpError.Temporary`, `netFD.accept`,
  `wrapSyscallError`, `isConnError` and `FD.Accept`, above. (§1)
- **Linux accept(2)**: <https://man7.org/linux/man-pages/man2/accept.2.html>,
  whose footer reads "Linux man-pages 6.19 2025-10-29 accept(2)", with the
  tool's whitespace condensed. Under RETURN VALUE, *Error handling*: "Linux
  accept() (and accept4()) passes already-pending network errors on the new
  socket as an error code from accept()." "For reliable operation the
  application should detect the network errors defined for the protocol after
  accept() and treat them like EAGAIN by retrying." "In the case of TCP/IP,
  these are ENETDOWN, EPROTO, ENOPROTOOPT, EHOSTDOWN, ENONET, EHOSTUNREACH,
  EOPNOTSUPP, and ENETUNREACH." Under ERRORS, of EMFILE: "The per-process
  limit on the number of open file descriptors has been reached." ECONNRESET
  is not on the page. (*Context*; *What this does not decide*)
