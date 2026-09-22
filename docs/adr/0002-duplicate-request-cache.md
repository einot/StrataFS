# 0002 — Duplicate request cache for non-idempotent NFS procedures

- **Status:** Accepted
- **Date:** 2026-09-22
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

The identity a duplicate is recognised by — caller, xid, program, version,
procedure — is entirely RPC-level, and the artefact being replayed is the
encoded RPC reply record. `internal/nfs` contributes exactly one fact: whether a
given procedure number is idempotent.

### 2. The key is `(client address including port, xid, prog, vers, proc)`

```go
type replyKey struct {
    addr             string // conn.RemoteAddr().String(), e.g. "127.0.0.1:54321"
    xid              uint32
    prog, vers, proc uint32
}
```

**This departs from the issue's "client address".** The issue's reasoning — two
clients can pick the same xid — is right, but an *IP* does not disambiguate
anything on this server: `strata` binds loopback only, so every client is
`127.0.0.1`. Two independent RPC stacks on the host (a second mount, the
conformance client, `showmount`) would share one key space, and a collision
there does not merely miss a duplicate: it replays *the wrong reply* for a
different operation. Including the port makes the key per-TCP-connection, which
is the strongest identity available and cannot collide across callers.

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
  timeout plus a retransmit with room to spare.
- **Eviction:** FIFO by insertion time. Expired entries are purged lazily on
  lookup and insert — no background goroutine and so no lifecycle to get wrong.
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

## Assumptions

Recorded because the issue did not specify them and a reader should be able to
push back on them individually.

1. **Per-connection identity is the right trade.** Keying on address *and port*
   means a duplicate arriving on a *new* connection (client reconnected after
   the TCP connection broke, then retransmitted the same xid) is not recognised
   and is re-executed. I accepted that because the alternative — keying on IP
   alone — is actively unsafe on a loopback-only server, and the common case the
   issue describes (a slow or lost reply on a live connection) is fully covered.
   A future non-loopback listener should revisit this, probably by adding a
   checksum of the call body to the key the way Linux's nfsd does.
2. **4096 and 120 s are judgement, not measurement.** I have no way to run this
   server and no retransmission traces. They come from the issue's "a few
   thousand entries over a couple of minutes" and from client timeout defaults.
3. **Error replies are cached.** See §5. Not stated by the issue.
4. **SETATTR is treated as non-idempotent** although only its guarded form is.
   Not stated by the issue's list.
5. **MOUNT is entirely idempotent** in this implementation, based on reading
   `internal/nfs/mount.go`: the mount table is informational.
6. **A client does not reuse an xid for a different call within 120 s.** RFC 5531
   §9 forbids the server from reading anything into the xid beyond equality, so
   this is a property of client implementations, not something we can enforce.
   Exhausting a 32-bit xid space in two minutes requires ~35 million calls per
   second; treating that as impossible is a deliberate assumption.
7. **Dropping an in-flight duplicate is acceptable on TCP.** The client's own
   retransmission timer is the recovery mechanism. Non-idempotent procedures in
   this filesystem are short (in-memory namespace work) with the exception of a
   size-setting `SETATTR`, which may fetch a chunk.

## Consequences

- **Memory.** Bounded by arithmetic, not measured: the largest cached reply is a
  `CREATE`/`MKDIR`/`SYMLINK` reply — status, `post_op_fh3` over a 16-byte handle,
  `post_op_attr`, `wcc_data`, plus the 24-byte RPC reply header — which is under
  300 bytes. 4096 entries at under 300 bytes plus a ~64-byte key is under 2 MiB.
- **Not covered, deliberately:** a duplicate that arrives after a server restart
  (the cache is in memory; RFC 1813 §4.5 notes this limitation of the technique
  generally), and a duplicate that arrives on a new connection.
- **Under sustained overload** (more than 4096 concurrent non-idempotent calls)
  protection degrades gracefully to today's behaviour rather than failing.
- `sunrpc.Server.dispatch` needs the connection's remote address, which today it
  does not receive; `serveConn` computes it once per connection and passes it in.

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
