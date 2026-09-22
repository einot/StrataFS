# 0002 — Duplicate request cache for non-idempotent NFS procedures

- **Status:** Accepted
- **Date:** 2026-09-22
- **Revised:** 2026-09-22 — §2 now keys on a server-assigned connection serial
  instead of the client's `address:port`, which recurs once a connection is
  gone. Review found that the earlier key could serve one client another
  client's cached reply after an ephemeral port was reused; see §2 and
  *Alternatives considered*. Nothing else changed: §6's three states, §7's
  bounds and §8's cache API are as first written.
- **Issue:** #2 — *Retransmitted requests are re-executed: no duplicate request cache*
- **Affects:** `internal/sunrpc`, `internal/nfs`

## Context

`sunrpc.Server.dispatch` executes every call record it decodes. Nothing
remembers what was answered, so a retransmission — which is byte-for-byte
indistinguishable from a first transmission — is executed a second time.

For idempotent procedures that is harmless. For the rest the client is told the
result of the *second* attempt, which is wrong:

| Procedure | Result of re-execution |
|---|---|
| `REMOVE` / `RMDIR` | `NFS3ERR_NOENT` for a delete that succeeded |
| `RENAME` | `NFS3ERR_NOENT`, or worse if a new file took the old name |
| `CREATE` (GUARDED) | `NFS3ERR_EXIST` for a create that succeeded |
| `MKDIR` / `SYMLINK` | `NFS3ERR_EXIST` |
| `SETATTR` (guarded) | `NFS3ERR_NOT_SYNC` for a setattr that succeeded |

This gets more likely exactly when the system is under stress: a client
retransmits when a reply is slow, and replies are slow here when a commit is in
flight against a loaded bucket.

RFC 1813 §4.5 ("Duplicate request cache") describes this as an implementation
issue for NFSv3 servers and the duplicate request cache as the usual answer: a
short-term memory of recently executed non-idempotent requests, so the operation
is attempted only once and the remembered completion status is returned to the
duplicate. RFC 5531 §5 gives the RPC-level licence for it — a server may
remember previously granted requests in order to get some degree of
execute-at-most-once semantics — and RFC 5531 §9 fixes what the identifier is
good for: the `xid` field is only for clients matching replies to calls and for
servers detecting retransmissions, and the service side cannot treat it as any
kind of sequence number. That is why the design below only ever tests the xid
for equality.

## Decision

### 1. The cache lives in `internal/sunrpc`

The identity a duplicate is recognised by — the connection it arrived on, xid,
program, version, procedure — belongs entirely to the RPC and transport layers,
and the artefact being replayed is the encoded RPC reply record. `internal/nfs` contributes exactly one fact: whether a
given procedure number is idempotent.

### 2. The key is `(connection serial, xid, prog, vers, proc)`

```go
type replyKey struct {
    conn             uint64 // serial of the connection the call arrived on
    xid              uint32
    prog, vers, proc uint32
}
```

`conn` is assigned by the server, not sent by the client. `Server` holds an
`atomic.Uint64`; `serveConn` takes the next value once per accepted connection
and passes it with every call record read from that connection:

```go
func (s *Server) dispatch(ctx context.Context, connID uint64, rec []byte) []byte
```

The first connection a process accepts gets 1, so a zero `conn` is never a
valid key. The counter is process-local, never appears on the wire and is never
reused, so two connections — however they are addressed, whenever they happen —
have disjoint key spaces. **An entry can only ever be returned to the
connection that created it.** That is the whole property the key exists to
provide.

**This departs from the issue's "client address".** The issue's reasoning — two
clients can pick the same xid — is right, but an *IP* does not disambiguate
anything on this server: `strata` binds loopback only, so every client is
`127.0.0.1`. Two independent RPC stacks on the host (a second mount, the
conformance client, `showmount`) would share one key space, and a collision
there does not merely miss a duplicate: it replays *the wrong reply* for a
different operation.

#### Why the remote address and port are not enough either

Adding the port (`conn.RemoteAddr().String()`, e.g. `"127.0.0.1:54321"`) is the
obvious repair and was this ADR's first answer. It distinguishes connections
that are alive at the same time, but not connections separated in time, and the
entries outlive the connection by up to 120 s (§7).

1. **An ephemeral port identifies a connection only while that connection
   exists.** It is drawn from a finite range (RFC 6056 §2.1) and is reused. RFC
   9293 §3.4.1 puts it exactly: "A connection is defined by a pair of sockets",
   and a later connection with the same pair is an *incarnation* of the earlier
   one, not the same connection. A key built from the address and port
   therefore outlives the thing it names: the next process assigned that port
   inherits the previous one's cache entries.
2. **TIME-WAIT does not close the window.** RFC 9293 §3.6 requires the 2×MSL
   lingering only of the side that closes *actively*. When the server closes
   first — which `serveConn` does on a write error, and on shutdown — the
   client's port is free as soon as the client's socket is gone, with no
   client-side TIME-WAIT at all. When the client does close actively, real
   stacks use far less than the RFC's nominal 2-minute MSL: Linux fixes the wait
   at 60 seconds (`TCP_TIMEWAIT_LEN`). The 120 s retention in §7 was chosen to
   outlast a client's retransmission window, and outlasts these too.
3. **Colliding xids need no bad luck.** This repo's own RPC test client starts
   every connection at `xid: 1` and increments per call
   (`internal/nfs/integration_test.go`), so two of its connections issuing the
   same sequence of calls issue the same xids. Any client that is restarted
   rather than long-lived — a harness, a one-shot tool, a remount — behaves the
   same way, and RFC 5531 §9 forbids the server from reading anything into an
   xid that would let it tell the difference.
4. **The two failures are not comparable.** Failing to recognise a duplicate
   costs one re-execution: the bug this ADR exists to fix, no worse than
   today's behaviour. Serving one client another client's reply is silent,
   one-sided corruption — the receiving client accepts a result for an
   operation it never issued (the xid matches one it did issue), and the
   operation it actually issued is never executed and never reported. A cache
   that can do that is worse than no cache.
5. **One case needs no port reuse at all.** `Server.Serve` takes a listener and
   may be called for more than one, while the cache belongs to the `Server`
   (`cmd/strata` calls it once today). Because a connection is unique by its
   *pair* of sockets, one client port can hold simultaneous connections to two
   different listener ports; keyed on the remote address those two live
   connections would share entries.

A server-assigned serial removes all five at once, because it never recurs.

`prog` and `vers` are in the key although a client uses one xid space across
both programs on a connection. They cost nothing and remove a class of
confusion.

### 3. `sunrpc.Handler` gains a required method

```go
type Handler interface {
    Call(ctx context.Context, proc uint32, cred Cred, args *xdr.Reader, res *xdr.Writer) error
    HasProc(proc uint32) bool

    // Idempotent reports whether executing proc more than once is equivalent
    // to executing it once. The server caches and replays replies for
    // procedures where this is false, so that a retransmission does not
    // re-execute the operation. Implementations should answer false for any
    // procedure they are unsure of: a needless cache entry is cheap, a
    // wrongly re-executed operation is not.
    Idempotent(proc uint32) bool
}
```

Required rather than an optional type assertion (the `vfs.WeakCache` idiom):
there are two implementations, both in this repo, and a missing method should be
a compile error rather than silently unprotected procedures.

### 4. Classification

`nfs.Server.Idempotent` returns **true** for `NULL`, `GETATTR`, `LOOKUP`,
`ACCESS`, `READLINK`, `READ`, `WRITE`, `READDIR`, `READDIRPLUS`, `FSSTAT`,
`FSINFO`, `PATHCONF`, `COMMIT`, and **false for everything else**, which in
practice is `SETATTR`, `CREATE`, `MKDIR`, `SYMLINK`, `MKNOD`, `REMOVE`, `RMDIR`,
`RENAME`, `LINK`.

- `WRITE` is idempotent by offset — a replayed write puts the same bytes at the
  same place — and it is the hottest mutating path, so caching it would be pure
  overhead. It relies instead on the write verifier being stable for the life of
  the process, which it already is (`FS.writeVerf`, randomised per boot).
- `COMMIT` is idempotent; committing twice is a no-op and returns the same
  verifier.
- `SETATTR` is cached because a guarded `SETATTR` (the `guard.check` ctime form,
  which `nfs3.go` implements) fails with `NFS3ERR_NOT_SYNC` on replay. Procedure
  granularity is all the RPC layer has; it does not decode arguments.
- `MKNOD` and `LINK` answer `NFS3ERR_NOTSUPP` unconditionally today, so caching
  them changes nothing — but they are namespace mutations in RFC 1813's model,
  and the classification should not have to be revisited if they are ever
  implemented.
- The **default for an unlisted procedure is "not idempotent"**, so a procedure
  added later is protected until someone deliberately says otherwise.

`nfs.MountServer.Idempotent` returns **true for every procedure**. MOUNT keeps
only an informational client list; replaying `MNT`, `UMNT` or `UMNTALL` changes
nothing a client can observe.

### 5. The cached artefact is the complete encoded reply record

The bytes cached are exactly the `[]byte` `dispatch` returns to `serveConn` —
the whole RPC reply message (xid, mtype, reply_stat, verifier, accept_stat,
body), not the reply body and not the decoded outcome. Because the xid is part
of the key, the cached bytes already carry the correct xid, so a replay is
byte-identical with no rewriting. The cached slice is never mutated after
insertion.

Replies are cached **whatever their status**, including `GARBAGE_ARGS` and
`SYSTEM_ERR`. Byte-identical replay is the whole point, and carving out statuses
introduces judgement at exactly the place where judgement caused the bug. A
client that actually received an error reply will not retransmit that xid; one
that did not receive it is entitled to the same answer it missed.

### 6. Three states, and duplicates that arrive mid-flight

An entry is *in flight* or *done*. The lookup that misses installs the in-flight
marker, so the three outcomes are:

| Lookup result | Action |
|---|---|
| absent | install in-flight marker, execute, then store the reply |
| in flight | **send nothing** — drop the duplicate |
| done | write the cached bytes; do not execute |

Dropping the mid-flight duplicate is required, not optional: the failure the
issue describes is a client timing out *while the server is still working*, so
the naive "check the cache, then execute" loses the race it exists to win. The
client retransmits again; by then the entry is *done* and it gets the cached
reply. Dropping leaves the client no worse off than today, where a slow request
also produces no reply until it finishes.

`dispatch` must guarantee via `defer` that an installed in-flight marker is
always resolved — completed with the reply bytes, or removed — including on a
panic.

### 7. Bounds: 4096 entries, 120 seconds, no flags

- **Entries: 4096.** The issue asks for "a few thousand".
- **Age: 120 s.** RFC 1813 §4.5 describes the cache as short-term memory, and
  the retention has to outlast a client's retransmission window. Common NFS
  client defaults put a TCP timeout in the tens of seconds; two minutes covers a
  timeout plus a retransmit with room to spare. Because §2's key names a
  connection *instance*, retention now trades dedup coverage against memory and
  nothing else: it does not have to be weighed against how fast the host
  recycles ephemeral ports, because an entry that outlives its connection can
  never match again whoever is assigned that port next.
- **Eviction:** FIFO by insertion time. Expired entries are purged lazily on
  lookup and insert — no background goroutine and so no lifecycle to get wrong.
- **Nothing reclaims a connection's entries when it closes.** They age out or
  are evicted like any other. With a per-connection-instance key that is a
  capacity question and not a correctness one, and the 4096 bound holds either
  way; a connection-churning workload can push a live connection's entries out
  early, which degrades dedup towards today's behaviour and nothing worse. See
  *Alternatives considered*.
- **In-flight entries are never evicted.** If the cache is at capacity and every
  entry is in flight, the new call **runs uncached** rather than growing the
  cache past its bound or evicting a marker it needs. That degrades to today's
  behaviour under an absurd load (more than 4096 concurrent non-idempotent calls)
  and keeps the bound absolute. `begin` still reports a miss in that case, so
  the caller does not need to know; `finish` completes only an entry that
  exists and is in flight, and does nothing for a key that was never installed.

**Neither bound is a flag.** Both are dictated by client retransmission
behaviour, which is a protocol fact rather than a property of the host, and the
memory involved is small and bounded (see Consequences). Contrast `-max-dirty`
in ADR 0003, where the right value genuinely depends on the machine.

### 8. Shape of the cache type

Pinned so the tests and the implementation agree:

```go
type cacheState int

const (
    cacheMiss     cacheState = iota // no entry; the caller now owns an in-flight entry
    cacheInFlight                   // an identical call is still executing
    cacheHit                        // reply holds the bytes sent for the original call
)

func newReplyCache(maxEntries int, maxAge time.Duration) *replyCache

// begin reserves k for execution. On cacheMiss the caller must afterwards call
// exactly one of finish or abandon for k.
func (c *replyCache) begin(k replyKey) (reply []byte, state cacheState)
func (c *replyCache) finish(k replyKey, reply []byte)
func (c *replyCache) abandon(k replyKey)
func (c *replyCache) len() int
```

The constructor takes its bounds as arguments so a test can use small ones;
`sunrpc.NewServer` passes the package constants.

## Alternatives considered

**Keep the `address:port` key, and purge a connection's entries when `serveConn`
exits.** This is where the revision started, and it does bound the hazard: the
kernel will not establish a second connection with the same pair of sockets
while ours is open, so entries removed before the socket closes cannot be
inherited. Rejected because the bound is assembled from three things that must
all hold — the purge must run *after* `wg.Wait()` (or it can delete an in-flight
marker a running handler still owns), it must run *before* `conn.Close()`, and
no other path may close the socket first — and the third is already false
today: a handler goroutine closes the connection itself on a write error, ahead
of any of `serveConn`'s deferred cleanup. A serial makes the same guarantee a
property of the key, where there is nothing left to hold. Purging also costs a
method on `replyCache` (§8) and couples cache hygiene to handler liveness,
since a purge sequenced after `wg.Wait()` inherits the wait for a handler that
is slow to return.

This is not an argument against purging *as well*. With a per-connection serial
a purge would be a pure capacity optimisation with no correctness content, so it
can be added later without revisiting any of this. It does not earn an API
method today (§7).

**Add a checksum of the call body to the key.** An approach associated with
Linux's nfsd — I have not read its source — and the direction Assumption 1
points at for a future non-loopback listener. It would make an accidental
collision far less likely without making it impossible: two clients issuing a
genuinely identical call with the same xid still collide, and that is precisely
the dangerous case, because the reply they get is plausible. It is also a
different feature — recognising duplicates *across* connections, which
per-connection identity deliberately does not attempt — and it costs a hash on
every non-idempotent call. Orthogonal to this fix.

## Assumptions

Recorded because the issue did not specify them and a reader should be able to
push back on them individually.

1. **Per-connection-instance identity is the right trade, in both directions.**
   The key names *this* connection, not the client behind it, which cuts two
   ways and I accepted both:
   - *A duplicate on a new connection is missed.* A client that reconnected
     after the TCP connection broke and then retransmitted the same xid is not
     recognised, and the call is re-executed. That now holds for **every**
     reconnect. Under the `address:port` key a reconnect that happened to be
     assigned the same ephemeral port would have been recognised — but that was
     luck, not capability: nothing in such a key distinguishes "same client,
     reconnected" from "different client, recycled port", so the hit was never
     safe to act on, and the design had no way to make it safe. Keying on a
     serial makes the miss total, and so makes an already-accepted cost
     explicit rather than probabilistic; it does not make it worse. The cost
     itself is one re-execution — this ADR's own bug, on a path it does not
     claim to cover.
   - *A call is never answered from another connection's entry.* This is the
     direction that must not fail, because the harm is not symmetric (§2, point
     4): a missed duplicate costs a re-execution, a wrong reply is accepted by
     a client as the result of an operation it never issued.
   The common case the issue describes — a slow or lost reply on a live
   connection — is fully covered either way. A future non-loopback listener
   should revisit recognising duplicates *across* connections (see
   *Alternatives considered*); per-connection identity is a floor, not a
   ceiling.
2. **4096 and 120 s are judgement, not measurement.** I have no way to run this
   server and no retransmission traces. They come from the issue's "a few
   thousand entries over a couple of minutes" and from client timeout defaults.
3. **Error replies are cached.** See §5. Not stated by the issue.
4. **SETATTR is treated as non-idempotent** although only its guarded form is.
   Not stated by the issue's list.
5. **MOUNT is entirely idempotent** in this implementation, based on reading
   `internal/nfs/mount.go`: the mount table is informational.
6. **A client does not reuse an xid for a different call within 120 s *on one
   connection*.** RFC 5531 §9 forbids the server from reading anything into the
   xid beyond equality, so this is a property of client implementations, not
   something we can enforce. §2's key narrows what has to be true: only a single
   connection's own xid stream matters, never the host's as a whole. Exhausting
   a 32-bit xid space on one connection in two minutes requires ~35 million
   calls per second; treating that as impossible is a deliberate assumption.
7. **Dropping an in-flight duplicate is acceptable on TCP.** The client's own
   retransmission timer is the recovery mechanism. Non-idempotent procedures in
   this filesystem are short (in-memory namespace work) with the exception of a
   size-setting `SETATTR`, which may fetch a chunk.
8. **Letting entries outlive their connection is acceptable.** Nothing reclaims
   a closed connection's entries before they age out (§7). That is a capacity
   judgement, not a correctness one, and it is unmeasured: I am assuming a mount
   holds one long-lived connection, and that nothing else on the host opens
   connections fast enough, with enough non-idempotent traffic, to evict a live
   connection's entries inside 120 s. If that proves wrong the remedy is a
   purge, which would change no other part of this design.
9. **The connection serial needs no unpredictability, and 64 bits is enough.**
   It is process-local, never leaves the process, and is not derived from
   anything a client sends, so starting at 1 leaks nothing and no client can aim
   at another connection's key space by guessing. At an implausible one
   connection per microsecond a `uint64` takes ~585,000 years to wrap, and a
   pre-wrap entry could not survive the 120 s age bound regardless. Both choices
   are mine; the issue says nothing about either.

## Consequences

- **Memory.** Bounded by arithmetic, not measured: the largest cached reply is a
  `CREATE`/`MKDIR`/`SYMLINK` reply — status, `post_op_fh3` over a 16-byte handle,
  `post_op_attr`, `wcc_data`, plus the 24-byte RPC reply header — which is under
  300 bytes. 4096 entries at under 300 bytes plus a 24-byte key is under 2 MiB.
- **Not covered, deliberately:** a duplicate that arrives after a server restart
  (the cache is in memory; RFC 1813 §4.5 notes this limitation of the technique
  generally), and a duplicate that arrives on a new connection — including a
  reconnect by the same client (Assumption 1).
- **Under sustained overload** (more than 4096 concurrent non-idempotent calls)
  protection degrades gracefully to today's behaviour rather than failing.
- `sunrpc.Server.dispatch` needs to know which connection a call arrived on,
  which today it does not; `serveConn` takes a serial from the `Server`'s
  counter once per accepted connection and passes it in. Nothing outside
  `serveConn` and `dispatch` needs to know the counter exists.
- **One `Server` may serve several listeners safely.** `Serve` takes a listener
  and can be called more than once against the same `Server` and so the same
  cache. Connections are told apart by serial rather than by address, so this
  needs no further thought if a second listener is ever added.

## References

- RFC 1813 §4.5, *Duplicate request cache* — the purpose of the cache, that it
  is short-term in-RAM memory of the completion status of non-idempotent
  requests, that it is lost across server restarts, and that a long network
  partition can outlive an entry.
  <https://www.rfc-editor.org/rfc/rfc1813.txt>
  **Honesty note:** my fetch of the canonical text was truncated before §4.5 and
  a fetch of the third-party mirror <https://www.freesoft.org/CIE/RFC/1813/47.htm>
  returned a summary rather than the section verbatim. The points above are what
  that summary asserted; nothing here is quoted as the RFC's exact words, and a
  reviewer should check the original before relying on any wording.
- RFC 5531 §5, *Transports and Semantics* — a server may remember previously
  granted requests in order to get execute-at-most-once semantics.
- RFC 5531 §9, *The RPC Message Protocol* — "The xid of a REPLY message always
  matches that of the initiating CALL message. NB: The 'xid' field is only used
  for clients matching reply messages with call messages or for servers
  detecting retransmissions; the service side cannot treat this id as any type
  of sequence number." <https://www.rfc-editor.org/rfc/rfc5531.txt>
- RFC 1813 §4.5 cites Juszczak, *Improving the Performance and Correctness of an
  NFS Server*, USENIX Winter 1989, for the implementation. I did not obtain that
  paper; the key composition here was derived from this server's own properties,
  not from it.
- RFC 9293 (TCP), for §2's argument that an address and port are not a stable
  identity. §3.4.1 *Initial Sequence Number Selection* — "A connection is
  defined by a pair of sockets", with later connections over the same pair being
  *incarnations* of the earlier one. §3.6 *Closing a Connection* — "When a
  connection is closed actively, it MUST linger in the TIME-WAIT state for a
  time 2xMSL". §3.4.2 *Knowing When to Keep Quiet* — "For this specification the
  MSL is taken to be 2 minutes."
  <https://www.rfc-editor.org/rfc/rfc9293.html>
  **Honesty note:** these sentences and their section numbers come from a fetch
  of the HTML rendering, not from reading the document end to end. That fetch
  placed the TIME-WAIT sentence in §3.6.1 (*Half-Closed Connections*), which
  reads oddly for that subsection; treat §3.6 as the citation and check the
  subsection before quoting it elsewhere.
- RFC 6056 §2.1, *Ephemeral Port Number Range* — used only for the fact that
  ephemeral ports come from a bounded slice of the 16-bit port space (IANA's
  dynamic range 49152–65535; Appendix A lists narrower ranges in shipped
  operating systems) and therefore recur.
  <https://www.rfc-editor.org/rfc/rfc6056.html>
- Linux `include/net/tcp.h`, `torvalds/linux` master as fetched 2026-09-22 —
  `#define TCP_TIMEWAIT_LEN (60*HZ) /* how long to wait to destroy TIME-WAIT
  state, about 60 seconds */`. Cited for the 60-second figure in §2 only. It is
  the kernel's compiled-in constant, not a measurement: I have no way to run or
  observe anything here.
  <https://raw.githubusercontent.com/torvalds/linux/master/include/net/tcp.h>
