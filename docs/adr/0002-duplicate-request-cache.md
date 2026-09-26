# 0002 — Duplicate request cache for non-idempotent NFS procedures

- **Status:** Accepted
- **Date:** 2026-09-22
- **Revised:** 2026-09-22 — §2 now keys on a server-assigned connection serial
  instead of the client's `address:port`, which recurs once a connection is
  gone. Review found that the earlier key could serve one client another
  client's cached reply after an ephemeral port was reused; see §2 and
  *Alternatives considered*. Nothing else changed: §6's three states, §7's
  bounds and §8's cache API are as first written.
- **Revised:** 2026-09-22 — an amendment, not a supersession: the decision is
  unchanged and the corners a clean-room test author found under-specified are
  now pinned. §5 gains the lower bound of the cacheable region — nothing
  produced before a handler is resolved is cached. §6 gains the exact position
  of the cache in `dispatch`, and what a client sees when a handler panics
  versus when anything else in `dispatch` does. §7 resolves "FIFO by insertion
  time" to the `finish` that stores the reply rather than the `begin` that
  installs the marker, states that a lookup refreshes nothing, and exempts
  in-flight entries from expiry as well as from eviction. §8 fixes the
  `cacheMiss` comment that claimed the caller always owns an entry (§7's
  degraded path is the counter-example), pins `reply` to nil for every state
  but `cacheHit`, defines a repeated `finish` or a late `abandon` as a no-op,
  and names the two production constants so a test can reference them.
  Assumptions 10–15 record the judgement calls this took. Unchanged: §2's key,
  §3's interface, §4's classification, §5's byte-identical replay of replies
  whatever their status, §6's three states and its dropped mid-flight
  duplicate, and §7's 4096 entries and 120 s.
- **Revised:** 2026-09-23 — a correction to the 2026-09-22 amendment, which
  left §8 asserting two incompatible things about one call. **§8's `finish`
  doc comment** said a `finish` with an empty reply "does nothing", which
  leaves the in-flight marker installed and makes the next `begin` report
  `cacheInFlight`; **§8's closing prose** and **Assumption 12** said such a
  call is treated as `abandon`, which removes the marker and makes the next
  `begin` report `cacheMiss`. The `abandon` reading wins and all three now say
  it: an empty reply is never stored, such a `finish` is exactly `abandon(k)`,
  and a done entry is left alone because `abandon` leaves one alone. The
  rejected reading would strand a marker for a call that is no longer
  executing — the one thing §6 says is never observable — and §7 neither
  evicts nor expires markers, so that entry would never be reclaimed. Two
  smaller fixes in the same pass. **§6's Panics bullet** said a handler panic
  becomes `SYSTEM_ERR` "before any cache interaction", which is false against
  §6 step 4's `begin`; it now says what it meant, that the `recover` completes
  before the deferred `finish`/`abandon` resolves the key. **§7 step 1** pins
  "exceeds `maxAge`" to a strict `>`, making `maxAge` an inclusive bound like
  `maxEntries`, with that judgement recorded as the new **Assumption 16**; and
  **Assumption 12** now gives the real reason the empty-reply rule is `abandon`
  rather than a no-op. **§7's in-flight bullet**, which says a marker is
  removed by `finish`, "which promotes it to done", gains a parenthesis
  pointing at §8 so the empty-reply case is not read as a third behaviour.
  Those seven places — §6's Panics bullet, §7's step 1, §7's in-flight bullet,
  §8's `finish` comment, §8's closing prose, Assumption 12, and the added
  Assumption 16 — are the whole of this revision. Nothing downstream moves: no
  caller in this design finishes with an empty reply and no test exercises one,
  so this corrects what the document asserts and no behaviour. Unchanged: §2's
  key, §3's interface, §4's classification, §5 entire, §6's three states, its
  dropped mid-flight duplicate and its position in `dispatch`, §7's 4096
  entries, 120 s, FIFO ordered by `finish`, refresh-nothing lookup and degraded
  path, §8's three states, `begin`, `abandon`, `len` and named constants, and
  Assumptions 1–11 and 13–15. Status stays `Accepted`.
- **Revised:** 2026-09-23 — answers two findings from the review of the
  implementation. The decision and every behaviour are unchanged, so no code
  and no test moves; this corrects what the document says about capacity and
  memory. **The shared budget, stated and kept.** The security review showed
  that one connection can push out every other connection's *done* entries
  with about 4096 *completed* calls sent one after another. The ADR's only
  degraded case had been "more than 4096 concurrent non-idempotent calls",
  which is a different mechanism (every entry in flight), and §7's bullet for
  that case stands. Per-connection fairness was weighed and deferred on cost.
  **§7's "Nothing reclaims" bullet** no longer implies that connection churn
  is how a live connection's entries go early. **§7 gains a last bullet**,
  "One budget for all connections", which makes the shared budget an
  explicit decision. ***Alternatives considered*** gains "Give each
  connection a fair share of the 4096 entries". **Assumption 8** is reframed,
  because it presented churn as the mechanism and a purge as the remedy, and
  the added **Assumption 17** records the judgement. **The *Consequences*
  bullet "Under sustained overload"** is replaced by two, one per mechanism:
  "Any connection can flush every other connection's done entries" and "When
  every entry is in flight". **The memory arithmetic, corrected.** The
  *Consequences* bullet **"Memory"** counted reply lengths and said "under
  2 MiB". Under §5's no-copy rule an entry retains its reply's whole 512-byte
  `xdr.Writer` array, so the buffers alone are 2 MiB, and with the map and the
  queue the ceiling is about 2.7 MiB. The bullet also named CREATE as the
  longest reply, but RENAME's is longer, 260 bytes to 256. It now counts
  capacity, itemises the bookkeeping, and says that on Go 1.24 the arithmetic
  does not bound the map under this cache's churn. It also weighs the no-copy
  rule against copying, and the rule stays. **§5** gains one sentence pointing
  at that cost. The added **Assumption 18** records what the figure rests on,
  and ***References*** gains the sources for both parts. Those places — §5's
  no-copy paragraph, §7's "Nothing reclaims" bullet and its new last bullet,
  the new *Alternatives considered* entry, Assumptions 8, 17 and 18 with the
  line introducing 17 and 18, the *Consequences* bullets "Memory", "Any
  connection can flush every other connection's done entries" and "When
  every entry is in flight", the *References* additions with the line
  introducing them, and this entry — are the whole of this revision.
  Unchanged: §1–§4, §6, §7's bounds, eviction order, four-step `begin`,
  refresh-nothing lookup, in-flight bullet and degraded-case bullet, §8
  entire, Assumptions 1–7 and 9–16, and the three earlier revisions. Status
  stays `Accepted`.
- **Revised:** 2026-09-23 — narrows an overclaim introduced by the fourth
  revision, directly above. The decision and every behaviour are unchanged,
  so no code and no test moves. That revision said in four places that a
  retransmission arriving while its original is still executing is always
  dropped, whatever the load. Review found that this contradicts §6's table,
  §7's degraded-case bullet and the implementation, which follows them: an
  original that arrives when all 4096 entries are already in flight gets no
  marker, and a retransmission of it runs again. What stays true, and is
  kept, is that nothing evicts an installed marker, however many other calls
  complete. Each of the four sentences now makes the guarantee conditional on
  the original having a marker, and points at the degraded case rather than
  restating it. **§7's last bullet**: the sentence ending "whatever else is
  happening" becomes three, the third saying that no number of completed
  calls can leave an original without a marker. ***Alternatives considered***,
  sub-bullet "What it protects is a narrow tail": the sentence saying the
  retransmission "is safe from any amount of other traffic" becomes two.
  **Assumption 17**, sub-bullet "The exposed tail is narrow over TCP": the
  last sentence, saying the retransmission "is protected by a marker",
  becomes two. ***Consequences***, bullet "Any connection can flush every
  other connection's done entries": the sentence ending "meets a marker,
  which nothing evicts" becomes two, the second naming the degraded case as
  the next bullet's mechanism and not this one's. No assumption is added,
  because each corrected sentence follows from §7's step 3 and its in-flight
  bullet. Those four replacements and this entry are the whole of this
  revision. Unchanged: everything else, including §6, §7's degraded-case
  bullet, the *Consequences* bullet "When every entry is in flight", every
  other assumption, and the four earlier revisions. Status stays `Accepted`.
- **Revised:** 2026-09-23 — corrects **Assumption 7**, which said a
  size-setting `SETATTR` "may fetch a chunk". Truncation never fetches one: it
  consults only the in-memory chunk cache and on a miss defers the trim to the
  next flush, so no non-idempotent procedure fetches anything. What can hold
  one up is waiting for a lock, and Assumption 7 now names both waits and which
  procedures meet each. Every non-idempotent procedure can wait on `FS.mu`
  while a commit holds it across its snapshot PUT and root-pointer swap (#43).
  The mutating ones take `FS.mu` for writing, and every non-idempotent handler
  reads attributes under it for its reply, including MKNOD's and LINK's, which
  change nothing. A size-setting `SETATTR`, and an UNCHECKED `CREATE` that
  carries a size and so truncates through the same path, can also wait on the
  file's `openFile.mu` while a flush uploads that file's chunks. ADR 0003's
  drains make both waits more frequent. The assumption's conclusion, that
  dropping an in-flight duplicate is acceptable on TCP, is unchanged. The
  decision and every behaviour are unchanged, so no code and no test moves.
  Assumption 7 and this entry are the whole of this revision. Nothing else
  moved: §1–§8, *Alternatives considered*, *Consequences*, *References*, every
  other assumption, and the five earlier revisions. Status stays `Accepted`.
- **Revised:** 2026-09-25 — **Assumption 7** follows ADR 0006: a size-setting
  `SETATTR` no longer consults the chunk cache either, because a truncate into
  a stored chunk always leaves the trim to the next flush. The sentence that
  said it consults only the cache, and cited the code by line, now says so,
  without line numbers. The assumption's conclusion, the decision and every
  behaviour of this ADR are unchanged. Assumption 7 and this entry are the
  whole of this revision. Status stays `Accepted`.
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

The zero-`conn` claim is an internal invariant and nothing more: no code
branches on it, the serial never leaves the package and never reaches the wire,
and there is no supported way to observe it from outside. It is a debugging
aid — a key with `conn == 0` was built wrong — not a check. A test should pin
the property the key exists to provide, that two connections never share an
entry, and not the serial's value.

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
byte-identical with no rewriting.

The cached slice is never mutated after insertion. `finish` stores the caller's
slice as given rather than copying it, and `begin` returns that same slice on a
hit; both directions are read-only by contract, so a caller must not modify a
slice it has handed to `finish` or received from `begin`. That is what lets
concurrent replays of one entry share the bytes without copying and without
racing. Storing the slice as given also keeps its whole backing array alive
for the entry's life, not just the bytes the reply uses; *Consequences* counts
the memory that costs and weighs it against copying.

Replies are cached **whatever their status**, including `GARBAGE_ARGS` and
`SYSTEM_ERR`. Byte-identical replay is the whole point, and carving out statuses
introduces judgement at exactly the place where judgement caused the bug. A
client that actually received an error reply will not retransmit that xid; one
that did not receive it is entitled to the same answer it missed.

**Where the cacheable region begins.** "Whatever their status" is bounded
below. It covers only what `dispatch` produces *after* it has resolved a
handler for `(prog, vers)` and confirmed with `HasProc` that the procedure
exists. Everything `dispatch` resolves before that point — a message dropped
because it is not a decodable CALL, and the replies `RPC_MISMATCH`,
`AUTH_ERROR`, `PROG_MISMATCH`, `PROG_UNAVAIL` and `PROC_UNAVAIL` — is never
cached and never consults the cache. Mechanically it could not be: classification runs through
`h.Idempotent(proc)`, and at those points there is no `h`, or no such `proc` on
it. Substantively it should not be: each of those replies is a pure function of
the call header, so re-deriving one for a retransmission costs a comparison and
produces identical bytes, there is no side effect to avoid, and caching them
would let a client that sprays unknown programs, versions or procedure numbers
spend the 4096-entry budget (§7) that is protecting real mutations. Entries are
only ever spent on calls that reached a handler.

### 6. Three states, and duplicates that arrive mid-flight

An entry is *in flight* or *done*. The lookup that misses installs the in-flight
marker — §7 has the one case where it cannot — so the three outcomes are:

| Lookup result | Action |
|---|---|
| absent | install in-flight marker if there is room (§7), execute, then store the reply |
| in flight | **send nothing** — drop the duplicate |
| done | write the cached bytes; do not execute |

Dropping the mid-flight duplicate is required, not optional: the failure the
issue describes is a client timing out *while the server is still working*, so
the naive "check the cache, then execute" loses the race it exists to win. The
client retransmits again; by then the entry is *done* and it gets the cached
reply. Dropping leaves the client no worse off than today, where a slow request
also produces no reply until it finishes.

**Where the cache sits in `dispatch`.** Pinned, because a test written from
this ADR and an implementation written from it have to agree on it without
meeting:

1. Decode the call header. A message that is not a CALL, or that will not
   decode, returns `nil` as today, with no cache interaction.
2. RPC version, credentials, verifier, the `(prog, vers)` lookup and
   `h.HasProc(proc)`, as today. Each failure returns its own reply directly,
   with no cache interaction (§5).
3. Ask `h.Idempotent(proc)`. If it answers true, execute exactly as today: the
   cache is not consulted, nothing is installed, nothing is stored.
4. Otherwise build `k` from the connection serial and the call header and call
   `begin(k)`:
   - `cacheHit` — return those bytes. The handler is not entered.
   - `cacheInFlight` — return `nil`. Nothing is sent (row 2 above).
   - `cacheMiss` — execute, then resolve `k` exactly once: `finish(k, reply)`
     with the complete reply record `dispatch` is about to return, or
     `abandon(k)` if it will return `nil` or leave without one.

The resolution in step 4 is what the `defer` is for, and the obligation is
stated in terms of `dispatch` *leaving* rather than succeeding: **when
`dispatch` leaves after a `begin` that reported `cacheMiss` — by returning a
reply, by returning `nil`, or by unwinding — exactly one of `finish` or
`abandon` has been called for that key.** The caller cannot tell whether
`begin` actually installed a marker, since §7's degraded path installs none,
and does not need to: both calls are defined no-ops for a key with no entry
(§8), so the obligation and the `defer` that discharges it are the same on
either path.

**An entry becomes *done* when the reply is encoded, not when it is sent.**
`finish` runs inside `dispatch`; `serveConn` writes the bytes afterwards.
Requests on one connection are served concurrently, so a duplicate can reach
`begin` inside that window and be answered from the cache, and the same reply
then goes out twice for one xid. That is the same situation a client already
creates for itself whenever it retransmits and both replies arrive, and RFC
5531 §9 gives the xid no meaning beyond matching a reply to a call, so the
second copy has no outstanding call to match. Finishing after the write instead
would mean holding the marker across a blocking socket write and dropping
duplicates for longer, to no end.

**Panics.** The `defer` above keeps the cache consistent while the stack
unwinds. It does not catch anything, and this ADR changes nothing about which
panics are caught, which is worth spelling out because the two sources have
different client-visible outcomes:

- **A panic inside `Handler.Call`** is converted into a `SYSTEM_ERR` reply by
  the `recover` that `dispatch` already wraps the handler call in, which
  completes before the deferred `finish`/`abandon` resolves `k` — step 4's
  `begin` has necessarily already run by then. That reply is an ordinary reply
  here: `finish` stores it, and a retransmission is answered with the same
  `SYSTEM_ERR` bytes without re-entering the handler. A call whose handler
  panicked is therefore executed exactly once — re-running a handler that
  panicked is no safer than re-running one that succeeded, and §5 declines to
  carve out statuses. This is existing behaviour of `dispatch`; the ADR pins it
  because the cache now depends on it.
- **A panic anywhere else in `dispatch`** — reply encoding, the cache itself —
  is not recovered, today or under this ADR, and takes the process down. The
  `defer` still runs as the stack unwinds, and that is the whole of what it
  buys: the cache is not left holding a marker for a call that has stopped
  executing, in this case or in any future one where such a panic is recovered
  higher up.

Stated as the observable a test can hold the implementation to: **a key is
never left in flight by a call that is no longer executing.** A retransmission
is always either answered — from the cache, or by executing again — or dropped
only while an attempt is genuinely still running. `cacheInFlight` is a
statement about a live call, never a tombstone for a dead one.

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
- **Eviction: FIFO, ordered by `finish`.** An entry takes its place in the
  queue, and starts its clock, when `finish` stores its reply — not when
  `begin` installs its marker. "FIFO by insertion time", as this bullet first
  read, did not distinguish the two; it is resolved to the completion, and the
  reading it rules out is "ordered by `begin`". Three reasons. An entry exists
  to answer a retransmission of a *completed* call, so the one to evict first
  is the one that has been answerable longest, not the one that started
  earliest — under the other reading a call that took 30 s would be evicted
  ahead of one that started later and finished in a millisecond. A slow call is
  exactly the case that provokes a retransmission (*Context*), and stamping at
  `begin` would hand it a shortened entry, or in the limit one that is already
  expired when its reply arrives, which is the opposite of what the age bound
  is for. And RFC 5531 §5 frames the technique the same way: "The server may
  choose to remember this ID *after executing a call*". Out-of-order completion
  is therefore well defined — the queue is in `finish` order as the cache
  observed it, which also makes the expired entries a prefix of it.
- **Purging is lazy, and happens in `begin`,** which is the cache's only lookup
  and its only insertion point — no background goroutine and so no lifecycle to
  get wrong. `begin` takes these steps in this order, so that two
  implementations of it cannot differ observably:
  1. Remove every done entry whose age exceeds `maxAge`. "Exceeds" is strict:
     an entry whose age is exactly `maxAge` stays, one a nanosecond past it
     goes. `maxAge` bounds an entry's life inclusively, the way `maxEntries`
     bounds the cache's size inclusively in step 3 — the bound is a value the
     cache may reach and may not pass. The boundary is not reachable in
     practice against a nanosecond monotonic clock, so a test should stay well
     clear of it in both directions rather than try to pin it (Assumption 16).
  2. Classify `k`. A done entry that survived step 1 is `cacheHit`; an
     in-flight entry is `cacheInFlight`. In both cases `begin` stops here and
     changes nothing.
  3. Otherwise the key is absent. Below `maxEntries`, install an in-flight
     marker for `k`. At `maxEntries`, evict the head of the done queue and
     install. At `maxEntries` with no done entry to evict, install nothing.
  4. Report `cacheMiss`, whether or not step 3 installed anything.

  Step 1 has to precede step 2, or an expired entry would be replayed. Ages are
  measured against a monotonic reading — keep the `time.Time` that `finish`
  recorded and compare with `time.Since`/`Sub` — so that a step of the system
  clock can neither expire the cache nor resurrect it. `finish` and `abandon`
  need purge nothing; no path depends on their doing so.
- **A lookup refreshes nothing.** This is a FIFO, not an LRU: a `cacheHit` does
  not restamp an entry or move it in the queue, and neither does a `begin` that
  finds an entry in flight. That is not just tidiness. Refreshing on hit would
  let a client keep one entry alive indefinitely by retransmitting it, and
  Assumption 6 — that a client does not reuse an xid for a different call within
  120 s on one connection — is only tolerable because 120 s is a ceiling on an
  entry's life rather than a window that a busy client can keep sliding.
- **Nothing reclaims a connection's entries when it closes.** They age out or
  are evicted like any other. With a per-connection-instance key that is a
  capacity question and not a correctness one, and the 4096 bound holds either
  way; a connection-churning workload fills the budget with entries that can
  never match again and so pushes a live connection's entries out early, which
  degrades dedup towards today's behaviour and nothing worse. Churn is one way
  for a live connection to lose its entries early, and neither the only way
  nor the likeliest: the last bullet of this section has the other, which a
  purge would not touch. See *Alternatives considered*.
- **In-flight entries occupy capacity, and are never evicted or expired.** They
  count towards `maxEntries`, and towards `len` (§8), exactly as done entries
  do: the bound is on entries held, not on replies stored. That is what makes
  "the bound is absolute" mean anything, and what makes the degraded case below
  exist at all. An in-flight entry has no clock and no queue position, so it
  cannot expire and cannot be evicted; only its owner removes it, by `finish`,
  which promotes it to done, or `abandon`, which deletes it (a `finish` with an
  empty reply is `abandon`, §8). Purging one would be a correctness bug rather
  than a capacity decision: the next duplicate would see a miss, install a
  *second* marker for a key already executing, and re-execute the operation the
  cache exists to protect. Nothing in the cache times a marker out either, so a
  handler that never returns pins its entry for the life of the process. That
  is bounded by the server's own per-connection concurrency limit — one
  connection may hold 64 calls in flight against the cache's 4096 entries — and
  a wedged handler is already holding a goroutine and one of those slots, which
  is the scarcer resource (Assumption 11).
- **The degraded case.** If the cache is at capacity and every entry is in
  flight, the new call **runs uncached** rather than growing the cache past its
  bound or evicting a marker it needs. That degrades to today's behaviour under
  an absurd load (more than 4096 concurrent non-idempotent calls) and keeps the
  bound absolute. `begin` still reports a miss in that case, so the caller does
  not need to know; `finish` completes only an entry that exists and is in
  flight, and does nothing for a key that was never installed.
- **One budget for all connections, with no per-connection share.** There is
  one done queue, and step 3 evicts its head whichever connection owns it. A
  done entry therefore survives until it is `maxAge` old or until roughly
  `maxEntries` later calls, from *any* connection, have been installed behind
  it, whichever comes first. The count is only rough for two reasons: markers
  in flight hold places outside the queue, and an abandoned call gives its
  place back. The count comes first whenever the whole server sustains more
  than about 34 non-idempotent calls a second (4096 / 120 s). So every
  connection's traffic shortens every other connection's dedup memory, and a
  single connection can push out all the others' done entries without churn
  and without concurrency, by completing about 4096 calls one after another
  (*Consequences*). Two things bound the loss. Once `begin` has installed a
  marker for an original, nothing evicts it, so a retransmission that arrives
  while that original is still executing is dropped as §6 says, whatever
  other traffic follows. Only an original that arrives when all 4096 entries
  are already in flight gets no marker, and a retransmission of it runs again
  (the degraded case above). No number of completed calls can bring that
  about, because `begin` evicts a done entry to make room. And an entry
  pushed out costs its own connection one re-execution of that call, which is
  today's behaviour, and never another connection's reply (§2). The shared
  budget is deliberate. Per-connection fairness was considered and deferred
  (*Alternatives considered*, Assumption 17).

**Neither bound is a flag.** Both are dictated by client retransmission
behaviour, which is a protocol fact rather than a property of the host, and the
memory involved is small and bounded (see Consequences). Contrast `-max-dirty`
in ADR 0003, where the right value genuinely depends on the machine.

### 8. Shape of the cache type

Pinned so the tests and the implementation agree:

```go
type cacheState int

const (
    cacheMiss     cacheState = iota // nothing to replay: execute the call
    cacheInFlight                   // an identical call is still executing
    cacheHit                        // reply holds the bytes sent for the original call
)

func newReplyCache(maxEntries int, maxAge time.Duration) *replyCache

// begin reports what the cache knows about k, and reserves k for execution
// when it knows nothing about it.
//
// On cacheMiss the caller must execute the call and afterwards call exactly
// one of finish or abandon for k. begin normally installs an in-flight marker
// for k, so that a duplicate arriving before the call completes is reported as
// cacheInFlight; when the cache has no room for one it reports cacheMiss
// having installed nothing (§7). The caller cannot tell those two apart and
// does not need to: finish and abandon are no-ops for a key with no entry, so
// the obligation — and the defer that discharges it — is the same either way.
//
// reply is non-nil only for cacheHit. It aliases the stored bytes and must not
// be modified.
func (c *replyCache) begin(k replyKey) (reply []byte, state cacheState)

// finish completes an in-flight entry for k with reply, which must be the
// complete encoded reply record and must not be modified afterwards. It does
// nothing if k has no entry or if k's entry is already done.
//
// An empty reply — len(reply) == 0, nil or not — is never stored: finish(k,
// reply) for such a reply is exactly abandon(k). An in-flight entry for k is
// removed, so a later begin(k) reports cacheMiss and not cacheInFlight, and a
// done entry is left alone, because abandon leaves one alone.
func (c *replyCache) finish(k replyKey, reply []byte)

// abandon removes an in-flight entry for k. It does nothing if k has no entry
// or if k's entry is already done.
func (c *replyCache) abandon(k replyKey)

// len reports the number of entries held, in flight and done alike, including
// any that have expired but have not yet been purged (§7). It is for tests and
// diagnostics; no dispatch path calls it.
func (c *replyCache) len() int

const (
    replyCacheMaxEntries = 4096
    replyCacheMaxAge     = 120 * time.Second
)
```

The constructor takes its bounds as arguments so a test can use small ones;
`sunrpc.NewServer` passes `replyCacheMaxEntries` and `replyCacheMaxAge`. The
two constants are named here, rather than left as "the package constants", so
that a test written from this ADR can reference them and fail if §7's two
numbers are quietly changed — otherwise the ADR pins two values that nothing
can check. They stay unexported: the package's tests are in package `sunrpc`,
so naming them costs nothing outside it. How `NewServer` holds its cache is not
pinned, and that these are the constants it passes is a review item rather than
something a test can assert.

`len` is a bound, not an expiry oracle. Because only `begin` purges, `len` can
count entries that are past `maxAge`; a test that wants to observe expiry must
do it through `begin`.

**Resolving a key twice.** "Exactly one of `finish` or `abandon`" stays the
caller's obligation, and a second resolve is a bug in the caller. The cache
defines the behaviour anyway — the second call does nothing — because the state
check that makes it a no-op is the same check §7's degraded path already forces
`finish` to do, so defining it costs nothing and turns a caller's mistake into
a lost dedup opportunity instead of a corrupted entry. Specifically, `finish`
never overwrites a stored reply and never restamps a done entry, and `abandon`
never deletes one.

That second rule carries more weight than it looks. §7's degraded path makes it
possible for a call that installed nothing to later `finish` a marker installed
by a *duplicate of itself*, so a done entry can exist while a call for that key
is still running. The safety invariant that survives it is that the bytes
stored under a key are always a reply to a call with that key, which holds
because two calls sharing a key are the same call (Assumption 6) — the shape
above carries no ownership token, so `finish` cannot check who installed the
marker it completes, and does not need to. An `abandon` that could delete a done entry
would break no invariant but would throw away a valid cached reply; one that
deletes only markers cannot.

**`finish` with an empty reply.** It is never stored: the call is exactly
`abandon(k)`, so the in-flight entry is removed and the next `begin(k)` reports
`cacheMiss`. Two things rule out the alternative of leaving the marker
installed and returning. `cacheHit` must always carry bytes, which is the whole
reason the rule exists — so that "`reply` is non-nil exactly for `cacheHit`"
has no exception to reason about. And a `finish` that returned without
resolving the key would strand a marker for a call that has stopped executing,
which is precisely what §6 says is never observable; since §7 neither evicts
nor expires a marker, that entry would never be reclaimed and every later
retransmission of that key would be dropped for the life of the process.
`dispatch` never does this — §6 has it call `abandon` when it has no reply
record — so the rule decides only what an out-of-contract caller observes.

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

**Give each connection a fair share of the 4096 entries.** Proposed by the
security review of the implementation, after it showed that one connection
can push out every other connection's done entries (§7's last bullet,
*Consequences*). The review suggested two shapes: cap each connection at N
entries, or evict the requesting connection's own oldest done entry before
anyone else's. Deferred rather than rejected outright. It is not unsafe; it
costs more than it returns today:

- *It does nothing for the common deployment, and a cap makes that deployment
  worse.* One mount is one long-lived connection (Assumption 17). There, the
  traffic that ages a connection's entries is its own, and no division of the
  budget changes that. A cap below 4096 shortens that connection's memory
  outright.
- *What it protects is a narrow tail.* The case *Context* leads with, a
  retransmission that meets a still-executing original, is safe from any
  amount of other traffic once `begin` has installed a marker for that
  original, because nothing evicts an installed marker. An original that
  arrives when all 4096 entries are already in flight gets no marker, and a
  retransmission of it runs again (§7's degraded case; *Consequences*, "When
  every entry is in flight").
  Fairness protects only a quiet connection's *done* entry. That entry is
  needed by a retransmission the server reads after the original completed,
  either because it crossed the reply or because it sat unread behind its own
  connection's 64 in-flight calls. Fairness helps only if about 4096 other
  calls complete in that gap.
- *It costs the structure §7 is built on.* To take one particular
  connection's oldest done entry while still expiring in global `finish`
  order, the cache must remove entries from the middle of its queue. Today
  entries leave the queue only at its head, which is what lets an
  implementation keep it in a plain slice, as this one does. Fairness needs
  per-connection queues threaded through the global one, which means two sets
  of links per entry. The alternative is lazy deletion, and it must compact:
  otherwise stale queue references pile up at the flood rate for up to 120 s
  and the memory bound in *Consequences* is gone. Fairness also
  rewrites §7 step 3, the part of `begin` that two implementations must agree
  on. And it adds per-connection bookkeeping whose life has to end with the
  connection's last entry, because the cache deliberately has no close hook.
- *The mainstream precedent is a shared cache.* Linux's NFS server keeps one
  cache with no per-client limit. Its source describes itself as "currently a
  global cache, but this may change in the future and be a per-client cache".
  It keeps flushing rare by size, not by partitioning: it scales the entry
  count with RAM, up to 256K entries (*References*).

This is a different objection from the one against purging on close. The purge
lost because its correctness hung on the order of three events in `serveConn`.
A quota would run inside `begin` under the cache's lock, as eviction does
today. It would need no API method and has no ordering hazard, so the only
case against it is cost. Nor is either one a substitute for the other. A purge
frees entries that can never match again, while a quota restrains a
connection that is still open. A purge would do nothing about the flooding
above, because the connection doing it has not closed.

Fairness can be added later without reopening anything else. Take a victim
rule that lets a lone connection use all 4096 entries and keeps each
connection's own entries in `finish` order, for example "evict from the
connection holding the most done entries". It changes §7's eviction rule and
the queue's structure, and nothing in §2, §5, §6 or §8. The API, the
constructor and the named constants stay. Every test written against this ADR
that exercises eviction today uses one connection, so none of them would
become wrong, though the new rule would need tests of its own. What would
reopen the question is any of these: several long-lived clients per server
becoming a supported deployment, a non-loopback listener, or a re-execution
traced to cross-connection eviction.

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
   this filesystem do in-memory namespace work and fetch nothing. That includes
   a size-setting `SETATTR`, which this assumption first said "may fetch a
   chunk": truncation never does, and since ADR 0006 it does not read the chunk
   cache either: a truncate into a stored chunk always leaves the trim to the
   next flush. What can hold one of these calls up is waiting for a lock:
   - Any of them can wait on `FS.mu` while a commit holds it for writing across
     its snapshot PUT and root-pointer swap (`internal/blobfs/commit.go:129-134`,
     with the PUT at `commit.go:163` and the swap at `:180`; #43). The mutating
     procedures take `FS.mu` for writing, and every non-idempotent handler,
     MKNOD's and LINK's included, reads attributes under it for its reply
     (`internal/nfs/nfs3.go:59-68`).
   - A size-setting `SETATTR`, and an UNCHECKED `CREATE` that carries a size
     (`nfs3.go:341-345`), can also wait on the file's `openFile.mu`, which a
     flush holds while it uploads that file's chunks (`fs.go:1297`, uploads at
     `:1330`).

   ADR 0003's drains are commits, so they make both waits more frequent. Each
   wait ends when the commits or flushes ahead of it do, it is a delay and not
   a leak, and §7 already accepts a slow call holding its marker, so the
   conclusion stands.
8. **Letting entries outlive their connection is acceptable.** Nothing reclaims
   a closed connection's entries before they age out (§7). That is a capacity
   judgement, not a correctness one, and it is unmeasured. I am assuming a
   mount holds one long-lived connection, so that entries left behind by
   closed connections, which can never match again, are a small share of the
   4096. If that proves wrong the remedy is a purge, which would change no
   other part of this design. A purge is a remedy for dead entries only,
   though. Connection churn is neither the only way nor the likeliest way for
   a live connection's entries to be pushed out early. The shared budget (§7's
   last bullet) lets one connection that never closes push out every other
   connection's done entries by completing about 4096 non-idempotent calls.
   Those are *completed* calls, issued one after another, not 4096 at once. No
   churn and no concurrency is needed, and a busy honest client does it as
   readily as a hostile one. A purge would not touch that, because the
   connection doing it is still open. Accepting it is a separate judgement,
   Assumption 17.
9. **The connection serial needs no unpredictability, and 64 bits is enough.**
   It is process-local, never leaves the process, and is not derived from
   anything a client sends, so starting at 1 leaks nothing and no client can aim
   at another connection's key space by guessing. At an implausible one
   connection per microsecond a `uint64` takes ~585,000 years to wrap, and a
   pre-wrap entry could not survive the 120 s age bound regardless. Both choices
   are mine; the issue says nothing about either.

Assumptions 10–15 come from the 2026-09-22 amendment, and are judgement calls
about corners the first draft left open rather than anything the issue, the
RFCs or a prior ADR settles.

10. **Both of an entry's clocks are set at `finish`, and a lookup refreshes
    neither.** §7 now says so; nothing outside this ADR requires it. The
    strongest external support is RFC 5531 §5's "remember this ID after
    executing a call", which is suggestive rather than normative — it says
    *may* throughout — and RFC 1813 §4.5 might well say something about
    in-progress entries, but I still could not obtain its text (see
    *References*). The costs I accepted: eviction order and expiry order both
    ignore when a call *started*, so a burst of slow calls that all complete at
    once is evicted in completion order and not in the order the clients sent
    them; and an entry's life is measured from a moment the client cannot
    observe, so a client whose call took 30 s gets 120 s of dedup counted from
    30 s after it asked.
11. **An in-flight marker has no timeout.** A handler that never returns pins
    one entry for the life of the process. I judged the marker not to be the
    binding resource: a wedged handler is already holding a goroutine and one
    of its connection's `maxInFlight` slots, of which there are 64 against the
    cache's 4096 entries. And a timeout would reintroduce exactly the failure
    §7's "never evict a marker" rule exists to prevent — a second owner for one
    key, and a re-execution. Nothing in the issue addresses a wedged handler.
12. **A caller mistake is defined rather than undefined.** A second `finish`,
    or an `abandon` after `finish`, does nothing (§8), instead of being called
    a programming error the cache need not defend against. The deciding
    argument is that the check costs nothing, because §7's degraded path
    already forces `finish` to tolerate a key it never installed. The same
    reasoning covers `finish` with an empty reply, which is treated as
    `abandon` and not merely ignored: ignoring it would leave a marker for a
    call that has stopped executing, which §6's observable forbids and which
    §7's "never evict or expire a marker" would make permanent. No caller in
    this design does that.
13. **`len` counts in-flight entries and does not purge.** The counting part
    follows from §7 — a bound that markers did not count against would make
    the degraded path incoherent — but the accessor's exact semantics are
    mine. It is a bound and a diagnostic, not an expiry oracle.
14. **Writing a second copy of one reply is acceptable.** Because an entry is
    done when the reply is encoded rather than when it is sent (§6), a
    duplicate landing in that window is answered from the cache and the same
    bytes go out twice for one xid. I am assuming a client discards a reply it
    has no outstanding call for; RFC 5531 §9 says only what the xid is *used
    for*, not what a client does with a second reply, so this is inference from
    the fact that a retransmitting client already produces this case for
    itself.
15. **A panic-derived `SYSTEM_ERR` is cached like any other error reply.**
    Follows from §5 once you notice that the existing `recover` turns a handler
    panic into an ordinary reply, but the first draft did not say it. The
    judgement is that replaying the error a client was already told beats
    re-entering a handler that panicked, even though a panic mid-operation may
    have left the namespace partly mutated and the replay tells the client
    nothing about that. The alternative — never cache a panic-derived reply —
    would re-execute exactly the calls least likely to survive re-execution.

Assumption 16 comes from the 2026-09-23 revision.

16. **The age bound is inclusive: an entry is purged when its age is strictly
    greater than `maxAge`.** §7 step 1 said "exceeds" and nothing said whether
    that meant `>` or `>=`. Nothing outside this ADR settles it, and the two
    readings differ only for an entry whose age equals `maxAge` to the
    nanosecond, which a monotonic clock makes unreachable in practice. I pinned
    `>` rather than declaring both conforming, on two grounds that are style
    rather than substance: "exceeds" already means strictly greater in English,
    and it makes `maxAge` an inclusive bound like `maxEntries`, which step 3
    lets the cache reach but not pass. A reader who prefers `>=` gives up
    nothing observable by it. The value of pinning is only that an
    implementation and a clean-room test cannot disagree about a case neither
    of them can reach.

Assumptions 17 and 18 come from the second 2026-09-23 revision, which answered
two findings from the review of the implementation.

17. **One shared budget is acceptable, and per-connection fairness is not yet
    worth its cost.** The security review showed that any connection can push
    out every other connection's done entries (§7's last bullet). I kept the
    single queue anyway, on three judgements. The first two are mine and
    unmeasured. The third is the security review's, and I agree with it:
    - *The common deployment is one mount, and so one long-lived connection.*
      There, no division of the budget helps, and a cap would hurt.
    - *The exposed tail is narrow over TCP.* On a live connection the
      original's reply is always written, so a client stops retransmitting
      once that reply arrives. A done entry therefore matters only for a
      retransmission that was already on its way, and that the server reads
      after the original completed. That happens when it crossed the reply, or
      when it sat unread behind its connection's 64 in-flight calls. The
      retransmission that meets a still-executing original is protected by
      that original's marker, which nothing evicts, provided `begin` installed
      one. An original that arrives when all 4096 entries are already in
      flight gets none, and a retransmission of it runs again (§7's degraded
      case).
    - *No one gains from doing it on purpose.* AUTH_SYS lets anyone who can
      reach the socket perform any operation as any uid, which is more than
      defeating another client's dedup. The argument does not depend on the
      loopback binding, only on AUTH_SYS.

    If the first two prove wrong, per-connection fairness is the remedy.
    That would mean several long-lived clients per server becoming a
    supported deployment, or a re-execution traced to cross-connection
    eviction. *Alternatives considered* says what fairness would and would not
    disturb. Raising `maxEntries` is the other lever, and it trades memory at
    the rate *Consequences* gives.
18. **The memory figure rests on the Go runtime's internals as read from its
    source, and on a Go 1.25 or later toolchain.** The 2 MiB of reply buffers
    follows from this repo's code alone. The map and queue figures do not. They
    come from reading Go's source and documentation (*References*): groups of
    8 slots behind an 8-byte control word, tables that grow past 7/8 full and
    split at 1024 slots, slices that grow by about a quarter past 256
    elements, and large allocations rounded up to 8 KiB pages. None of that was
    measured, and a runtime change can move it. The map figure also needs Go
    1.25 or later, because Go 1.24 gets back the capacity deletions tie up as
    tombstones only by growing a table (golang/go#70886). Whether to require
    1.25 in `go.mod` is not this ADR's call. Three more judgements are mine
    alone:
    - that 512 bytes per entry holds only while every reply the cache can hold
      fits in `xdr.NewWriter`'s initial buffer, a fact of today's code and not
      a rule, so a non-idempotent procedure with longer replies would mean
      redoing the arithmetic;
    - that the default 256 MiB chunk cache is the right yardstick for "a user
      would not notice";
    - and that under 1 MiB of difference does not justify changing a tested
      rule.

## Consequences

- **Memory.** Bounded by arithmetic, not measured: about 2.7 MiB at the
  4096-entry ceiling, and under 3 MiB. A done entry holds three things.
  - *A reply buffer, counted by capacity and not by length.* §5 stores the
    slice `dispatch` returns without copying it, so an entry keeps that
    slice's whole backing array alive. Every reply `dispatch` can cache,
    success or error, is built in an `xdr.Writer`, which starts with a
    512-byte array, and no cached reply outgrows it. With this filesystem's
    16-byte handles the longest is RENAME's, at most 260 bytes: the 24-byte
    RPC reply header, a status and two `wcc_data`. Next come successful
    CREATE, MKDIR and SYMLINK replies, at most 256 bytes, and LINK's, 232.
    Every other cached NFS reply is at most 144 bytes, and `GARBAGE_ARGS`
    and `SYSTEM_ERR` replies are 24. Even NFSv3's 64-byte maximum handle
    would take CREATE's only to 304. So every done entry holds exactly one
    512-byte array. That is a Go allocator size class, so nothing rounds it
    up, and 4096 of them come to **2 MiB** however short the replies are.
    This bullet first counted reply lengths and arrived at "under 2 MiB" for
    the whole cache. Length is the wrong measure, because a 24-byte reply
    retains as much as a 260-byte one.
  - *A map slot:* a 24-byte key and a 24-byte slice header. Go's maps (Swiss
    tables since Go 1.24) keep 8 slots per group behind an 8-byte control
    word, which comes to 49 bytes a slot here. They grow a table once it is
    more than 7/8 full and split it at 1024 slots. A 1024-slot table
    therefore splits in two once it passes 896 entries, and with an even hash
    the tables split at about the same time, so 4096 entries sit in about 8
    tables of 1024 slots, each about half full. Each table is 128 groups of
    392 bytes, and because that allocation is over 32 KiB it is rounded up to
    whole 8 KiB pages, which makes 56 KiB a table and about **448 KiB** in
    all. A Go map does not shrink when entries are deleted, so the map
    reaches that size the first time the cache fills and stays there.
  - *A queue reference:* the key again and a 24-byte `time.Time`, 48 bytes,
    in a slice used as a FIFO. Past 256 elements Go grows a slice by about a
    quarter at a time and rounds a large allocation up to whole pages, so the
    queue's array peaks at 5461 references, **256 KiB**.

  2 MiB + 448 KiB + 256 KiB is 2,818,048 bytes. An in-flight entry has a map
  slot but no buffer and no queue reference, so any mix of in-flight and done
  entries is inside that figure. Two costs are left out, because they belong
  to the runtime and not to this cache: the old array a queue reallocation
  leaves for the collector, and the headroom Go's collector keeps over the
  live heap. The map's share also assumes Go 1.25 or later (Assumption 18).
  Go 1.24, which `go.mod` still admits, gets back the capacity that deletions
  tie up as tombstones only by growing a table. Once this cache is full it
  deletes one entry for every one it inserts. golang/go#70886 reports that
  same churn growing a constant-size map eightfold. On that toolchain this
  arithmetic does not bound the map.

  **The no-copy rule stays.** If each reply were copied into an allocation of
  its own length, an entry would retain at most 288 bytes (RENAME's 260,
  rounded up to its size class), or 1.125 MiB in all. That saves at most
  0.875 MiB, about a third of the total, and costs an allocation and a copy
  on every non-idempotent call. The process's chunk cache defaults to 256 MiB
  (`-cache`), and against that neither figure is one a user would notice. If
  the entry bound ever grows enough for the buffers to matter, a copy is not
  the cheapest fix. `dispatch` could instead allocate each reply record at its
  exact length in the first place. That saves the same memory with no copy
  and no extra allocation, and it changes no rule in this ADR.
- **Not covered, deliberately:** a duplicate that arrives after a server restart
  (the cache is in memory; RFC 1813 §4.5 notes this limitation of the technique
  generally), and a duplicate that arrives on a new connection — including a
  reconnect by the same client (Assumption 1).
- **Any connection can flush every other connection's done entries.** §7's
  budget is shared, so each connection's dedup memory is whatever the whole
  server's traffic leaves it. A busy honest client does this without meaning
  to. A build, an `rm -rf` or an untar on a second mount can complete
  thousands of non-idempotent calls a second over loopback, by estimate, and
  that cycles all 4096 entries within a second or two. Doing it on purpose
  takes about 4096 *completed* calls, sent one after another. It needs no
  concurrency, and at those rates it takes a second or two over loopback, or
  less. The cheapest such calls change nothing. MKNOD always answers
  `NFS3ERR_NOTSUPP` (§4), and a non-idempotent call whose arguments do not
  decode is cached as `GARBAGE_ARGS` (§5). Neither needs a valid file handle.
  What the victim loses is exactly what this ADR adds and no more: a
  retransmission the server reads after its original completed is executed
  again, which is today's behaviour for that one call. No reply ever reaches
  another connection (§2). A retransmission that arrives while its original
  is still executing meets the original's marker, which nothing evicts,
  provided `begin` installed one. An original that arrives when all 4096
  entries are already in flight gets none, and a retransmission of it runs
  again; that is the next bullet's mechanism, not this one's. None of this
  crosses a security boundary: AUTH_SYS already lets anyone who can reach the
  socket perform any operation as any uid, which is more than defeating
  another client's dedup. The shared budget is deliberate (§7,
  *Alternatives considered*, Assumptions 8 and 17).
- **When every entry is in flight,** a new call runs uncached (§7's degraded
  case). That takes more than 4096 concurrent non-idempotent calls, which at
  64 per connection means at least 65 connections, and `Serve` does not cap
  connections. The result is today's behaviour for that call. It is never a
  failure, and the cache never grows past its bound.
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
  reviewer should check the original before relying on any wording. The
  2026-09-22 amendment tried again, against
  <https://www.rfc-editor.org/rfc/rfc1813.html> and
  <https://datatracker.ietf.org/doc/html/rfc1813>: both truncated at §3.3.7,
  the same wall. §4.5 is still secondhand here, which matters most for §7 —
  if the RFC says anything about how long an in-progress entry is held, this
  ADR decided it without that input.
- RFC 5531 §5, *Transports and Semantics* — a server may remember previously
  granted requests in order to get execute-at-most-once semantics. Fetched
  2026-09-22 from <https://www.rfc-editor.org/rfc/rfc5531.txt>: "A server may
  wish to remember previously granted requests from a client and not regrant
  them, in order to insure some degree of execute-at-most-once semantics", and
  "The server may choose to remember this ID after executing a call and not
  execute calls with the same ID, in order to achieve some degree of
  execute-at-most-once semantics." The second sentence is cited in §7 for
  starting an entry's clock at completion rather than at arrival; note that it
  is permissive ("may choose"), so it supports that reading without requiring
  it. **Honesty note:** both sentences are quoted as that fetch returned them
  from the canonical text, against a request to quote and not paraphrase; I did
  not read the surrounding section end to end.
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

The entries below were added by the second 2026-09-23 revision.

- Linux `fs/nfsd/nfscache.c`, `torvalds/linux` master as fetched 2026-09-23.
  Cited in *Alternatives considered* as precedent for a shared cache. Three
  things were taken from it:
  - the file's header comment, "Request reply cache. This is currently a
    global cache, but this may change in the future and be a per-client
    cache.";
  - `nfsd_cache_size_limit`, which computes `limit = (16 *
    int_sqrt(low_pages)) << (PAGE_SHIFT-10)` and returns `min_t(unsigned
    int, limit, 256*1024)`;
  - `nfsd_prune_bucket_locked`, which walks a bucket's LRU "ordered
    oldest-first" and stops once the entry count is within the maximum and
    the entry is younger than `RC_EXPIRE`.

  No per-client limit appears in the file.
  <https://github.com/torvalds/linux/blob/master/fs/nfsd/nfscache.c>
  **Honesty note:** the raw-file fetch returned HTTP 429, so these quotes came
  through the fetch tool's rendering of the GitHub page, asked for verbatim
  text. I did not read the file end to end, and "no per-client limit" is that
  rendering's answer, not my own reading of every line.
- Go runtime source, `release-branch.go1.24`, fetched 2026-09-23, for the
  memory figure in *Consequences* and Assumption 18:
  - `src/internal/runtime/maps/table.go` has `const maxTableCapacity = 1024`,
    commented "Maximum size of a table before it is split at the directory
    level". Its `rehash` grows or splits the table. It carries a TODO that
    tombstones are not reclaimed in place, and `split` creates two tables of
    `maxTableCapacity` slots each.
  - `src/internal/runtime/maps/group.go` has `maxAvgGroupLoad = 7`,
    commented "Maximum load factor prior to growing. 7/8 is the same load
    factor used by Abseil".
  - `src/runtime/slice.go` has `nextslicecap`, which doubles below 256
    elements and then grows by `newcap += (newcap + 3*threshold) >> 2`.
    `growslice` then rounds the result up with `roundupsize`.
  - `src/runtime/sizeclasses.go` lists 512 bytes as size class 26 and 288 as
    class 19, with `_MaxSmallSize = 32768` and `_PageShift = 13`.

  <https://github.com/golang/go/tree/release-branch.go1.24/src>
- Go tip, `src/internal/runtime/maps/table.go`, fetched 2026-09-23. When a
  table's `growthLeft` reaches 0 on insert it first calls `pruneTombstones`
  ("If we have no space left, first try to remove some tombstones"). That
  function declines unless tombstones are at least 10% of capacity. Go 1.24's
  source has no such step.
  <https://tip.golang.org/src/internal/runtime/maps/table.go>
- *Faster Go maps with Swiss Tables*, the Go blog. Taken from it: groups "of
  8 slots each", "a 64-bit control word for metadata", and "An individual
  table stores a maximum of 1024 entries." <https://go.dev/blog/swisstable>
- golang/go#70886, *runtime: Swiss Table maps can double size multiple times
  when deleting/adding elements*. The reporter's map, held at a constant
  count, grew from 128 to 1024 table slots under delete/add churn. The issue
  is closed in the Go 1.25 milestone. **Honesty note:** the fetched page did
  not show which change closed it. Linking it to `pruneTombstones` is my
  inference from the tip source above.
  <https://github.com/golang/go/issues/70886>
- golang/go#20135, *runtime: shrink map as elements are deleted*, open as
  fetched 2026-09-23. Cited for the fact that a Go map does not give storage
  back on delete. <https://github.com/golang/go/issues/20135>
