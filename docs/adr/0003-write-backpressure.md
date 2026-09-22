# 0003 — Bounding buffered writes, and the blocking discipline that makes it safe

- **Status:** Accepted
- **Date:** 2026-09-22
- **Revised:** 2026-09-22 — §2 is rewritten to account `cap(v)` rather than
  `len(v)` over each open file's `dirty` map. The buffers are allocated at full
  chunk capacity, so a `len`-shaped charge does not bound resident memory,
  which is the thing the budget exists to bound; §1's `Config` doc comment,
  §6's bound and §8's `DirtyBytes()` are restated in those terms. §5's claim
  that a diverged filesystem surfaces as `NFS3ERR_STALE` was false and is
  corrected: the commit path returns bare errors, so the write whose drain
  *discovers* divergence reports `NFS3ERR_IO` and only the next one reports
  `NFS3ERR_STALE`. Assumption 7 now accounts for retransmitted stalled writes
  taking further in-flight slots; Assumptions 1, 2 and 6 follow the accounting
  change; §9 notes that its doc comment has since landed; and a *What this does
  not decide* section is added. Nothing else changed: the decision itself — a
  global byte budget with a check-then-proceed blocking discipline — §1's flag
  and 256 MiB default, §3's lock ordering, §4's `budget` API and waiting rules,
  §7's logging and the Consequences are as first written.
- **Issue:** #4 — *Buffered writes are unbounded: add backpressure*
- **Affects:** `internal/blobfs`, `cmd/strata`, doc comment on `vfs.FS.Write`

## Context

`blobfs.Write` appends into `openFile.dirty` unconditionally. The only thing
that drains it is a commit — the `-commit-interval` ticker, an NFS `COMMIT`, or
a `FILE_SYNC` write. A client that writes faster than the bucket ingests grows
resident memory without bound until the process is killed, losing every
uncommitted write: data the client was told had been accepted.

NFSv3 already provides the mechanism, so nothing has to be invented at the
protocol level. RFC 1813 §4.12 and §3.3.7 make an `UNSTABLE` write a promise the
server may keep later: the server is not required to commit before replying, and
the client must retain the data until a `COMMIT` succeeds. Nothing in the
protocol obliges the server to reply quickly. Holding the reply until we are
ready is therefore legitimate backpressure, and much better than accepting data
we cannot store. This is the same argument `docs/DESIGN.md` §9 makes for not
needing NVRAM: the client's memory is the log.

The hard part is not the limit. It is not deadlocking. The write path holds
`openFile.mu` across its whole buffering loop and the commit path needs exactly
that lock in order to drain. Any design where a writer waits while holding a
lock the commit needs is a deadlock, and it is the obvious way to write this.

A second, less obvious hazard: **the drain cannot be delegated to something
outside the write path.** `sunrpc.serveConn` bounds in-flight requests per
connection (`maxInFlight`, 64) and blocks *reading the next record* once the
bound is reached. If 64 writes are stalled, the connection stops reading
altogether, so the client's own `COMMIT` — the thing that would drain the
backlog — is never even decoded. A design that waits for a client `COMMIT`, or
that signals a background committer that an embedder may not have started
(`FS.Run` is optional; the test suites do not call it), deadlocks or hangs. The
stalled writer has to be able to perform the drain itself.

## Decision

### 1. A global byte budget, configured in MiB

```go
// Config
//
// MaxDirtyBytes bounds the memory held by unflushed writes across all open
// files, measured as the capacity of their buffers rather than the count of
// logically dirty bytes (see the accounting rule below). Zero selects the
// default (256 MiB); a negative value disables backpressure and lets the
// buffer grow without limit.
MaxDirtyBytes int64
```

```go
// cmd/strata
maxDirty = flag.Int("max-dirty", 256,
    "maximum buffered write data in MiB before writes are held for a commit (0 or less: no limit)")
```

`cmd/strata` maps `*maxDirty <= 0` to `MaxDirtyBytes: -1` and anything else to
`int64(*maxDirty) << 20`. The unit is MiB for consistency with `-cache` (MiB)
and `-chunk-size` (KiB). The default of 256 MiB is the issue's suggestion.

There is **no minimum**. With the check-then-proceed discipline in §4 any
positive limit makes progress, because a writer is admitted whenever the current
charge is below the limit and a successful drain takes the charge to zero.

### 2. Accounting: the quantity counted, and the five sites that count it

`openFile` gains `bytes atomic.Int64`, the sum of **`cap(v)`** — not `len(v)` —
over its `dirty` map. `FS` gains the budget, whose `cur` is the sum over all
live open files. `atomic.Int64` rather than a field under `openFile.mu`, because
one of the five sites runs under `FS.mu` where taking `openFile.mu` would invert
the lock order (§3).

#### Why the accounted quantity is `cap`, not `len`

The budget is a statement about **resident memory**, not about logical dirty
bytes, and a dirty buffer is resident at its capacity however little of it is
written. The buffers are allocated at full chunk capacity throughout:
`internal/blobfs/fs.go:1092` is `make([]byte, 0, cs)`, `fs.go:1103` is
`append(make([]byte, 0, cs), loaded...)`, and `fs.go:1194` is
`of.dirty[lastIdx] = chunk[:tail]`, a reslice that shrinks `len` while the
backing array stays at `cap`.

Under `len` accounting a client issuing one small write per chunk index is
charged only what it wrote — a few kilobytes per index — while the process
holds a full `cs` per index, a megabyte at the default chunk size. The charge
is then wrong by nearly the whole of the memory it is supposed to be measuring:
`DirtyBytes()` would report a number comfortably inside the limit, no writer
would ever stall, and the process would die of exactly the OOM this ADR exists
to prevent. `len` accounting does not bound what the operator asked to bound.

The consequence, accepted knowingly: **on a sparsely written file
`DirtyBytes()` reports more than the count of logically dirty bytes** — for a
file with one byte written into each of three chunk indices, `3 × cs`. That is
the honest number for a memory budget, it is the number §6's bound is about,
and it is what §8's accessor returns. A test asserting that a 10-byte write
adds 10 to `DirtyBytes()` is asserting the defect.

Capacity is `cs` exactly for any buffer the write path allocates: `make([]byte,
0, cs)` yields `cap == cs` — the language guarantees the capacity `make` was
asked for, whatever the allocator rounds up to underneath — and the subsequent
`append` at `fs.go:1111-1112` never needs more than `cs`, so it never grows.
The exception is a buffer that `truncate` or `pendingTrim` staged at a partial
length (`fs.go:1216`, `fs.go:1272`) and a later write then grows: there
`append` chooses the new capacity, which is at least what the write needs and
may round above `cs`. Computing every delta as a difference of `cap` keeps the
accounting exact in that case too, which is why the rule below is stated as a
`cap` delta rather than as "add `cs` per new index".

#### The five sites

1. **`Write`**, under `openFile.mu`, per chunk index touched: the delta is
   `cap(new) - cap(old)`, sampling `cap(old)` from the map entry *before* the
   buffer is mutated, and `cap(old)` is zero when the index was absent.
   Accumulate over the loop, then charge once. A single `Write` call touches
   each index at most once — each iteration either runs to the next chunk
   boundary or exhausts the data, so the indices it visits strictly increase —
   and therefore no index can be double-counted. An index that was already
   dirty at full capacity contributes zero.
2. **`Write`'s tail block**, holding `openFile.mu` and `FS.mu`, where it
   re-resolves the handle: if the inode has gone (unlinked, or a generation
   mismatch) nothing will ever flush this buffer. Release the file's whole
   remaining charge with `of.bytes.Swap(0)` and reset `of.dirty` to an empty map.
   Without this a write racing an unlink of the same file leaks budget
   permanently.
3. **`truncate`**, under `openFile.mu` and `FS.mu`: subtract `cap` for every map
   entry it deletes; add `cap` for the chunk it stages from the cache
   (`fs.go:1216`, a fresh allocation of the truncated length). The **shortened
   tail chunk contributes no delta**: `of.dirty[lastIdx] = chunk[:tail]`
   (`fs.go:1194`) is a reslice, so `len` falls and `cap` does not, and the
   backing array is still resident. Not subtracting there is accuracy, not
   conservatism — the memory really is still held. (The first version of this
   ADR subtracted for it, which was wrong for the same reason `len` accounting
   as a whole was.) Charged **without waiting** — see §6 and Assumption 6.
4. **`flushOpen`**, under `openFile.mu`: add `cap` for each entry it
   materialises while resolving `pendingTrim` (`fs.go:1272`); when it replaces
   `of.dirty` with a fresh map (`fs.go:1311`), release `of.bytes.Swap(0)`.
5. **`unlink` and `Rename`'s victim removal**, under `FS.mu`, wherever
   `delete(f.open, id)` happens today: release `of.bytes.Swap(0)` first. This is
   the easiest site to miss and the most damaging to miss — a long-running mount
   doing create/write/delete cycles would ratchet the charge upward until it sat
   permanently at the limit with nothing left that could drain it. `Swap` makes
   the release exactly-once even if a flush is releasing concurrently.

Every site is a `cap` delta or a full release, so the invariant — `of.bytes`
equals the sum of `cap(v)` over `of.dirty` — holds once each site returns. It is
directly assertable, and asserting it after each kind of operation is a better
test than checking any single total.

### 3. Lock ordering

The existing rule stands and is extended by one line:

> Lock ordering is `openFile.mu` before `FS.mu`. Nothing may acquire an
> `openFile.mu` while holding `FS.mu`.
>
> **The budget's mutex is a leaf: it may be taken while holding either of the
> others, but no other lock may be acquired while it is held, and nothing may
> *wait* on its condition variable while holding either of the others.**

That single sentence is what makes the design deadlock-free, and it is the thing
a reviewer should check first.

### 4. Where a writer waits, and what it does while waiting

`Write` waits **after** the resolve-and-permission block (so a write that is
going to fail with `EACCES` never stalls) and **before** `getOpen` and
`openFile.mu` — holding no filesystem lock at all.

The wait is check-then-proceed, not reserve-then-write: a writer is admitted
whenever the charge is below the limit. The budget's algorithm, pinned:

```go
func newBudget(limit int64, log *slog.Logger) *budget

// add charges n without waiting. release returns n and wakes waiters.
func (b *budget) add(n int64)
func (b *budget) release(n int64)
func (b *budget) used() int64

// await blocks until the charge is below the limit, electing one waiter at a
// time to run drain. It must be called holding no other lock. limit <= 0
// disables waiting; accounting continues regardless.
func (b *budget) await(ctx context.Context, drain func(context.Context) error) error
```

`await`, with `b.mu` held except where noted:

1. Record `start := b.gen`.
2. Loop:
   - `cur < limit`, or `limit <= 0`, or `ctx.Err() != nil` → return
     (`ctx.Err()` if cancelled, otherwise nil).
   - **No drain in flight** → become the drainer: set `draining = true`, unlock,
     call `drain(ctx)` **holding no lock at all**, relock, set
     `draining = false`, `gen++`, `err = <result>`, `cond.Broadcast()`. If the
     drain failed, return its error. Otherwise continue the loop.
   - **A drain is in flight** → `cond.Wait()`. On waking, if `b.gen > start` and
     `b.err != nil`, return `b.err`; otherwise continue the loop.

`release` broadcasts, so a waiter also wakes when the ticker committer, an NFS
`COMMIT`, a `FILE_SYNC` write or an unlink frees space.

The generation counter exists so a writer that arrives *after* a failed drain
does not inherit that drain's error: it records the current generation and will
only ever consume an error produced by a later one.

`drain` is `FS.Sync`. One drain runs at a time; the alternative — every stalled
writer calling `Sync` — produces one redundant namespace snapshot per stalled
writer, precisely when the bucket is already the bottleneck.

### 5. What happens when the commit fails, and whether there is a timeout

**On failure:** the drain's error is broadcast, and every waiter — including the
one that ran it — abandons the wait and returns that error from `Write`. The
NFS layer maps it through the existing `statusOf` (`internal/nfs/wire.go:52`),
which `errors.As`-unwraps a `vfs.Status` from anywhere in the chain and passes
it through unchanged, and turns anything else into `NFS3ERR_IO`. Concretely,
for the two failures worth naming:

- **A read-only store reports `NFS3ERR_ROFS`.** `putChunk` returns
  `vfs.ErrROFS` (`internal/blobfs/fs.go:960`); it propagates through
  `flushOpen` → `flushAll` → `Sync`'s wrapping `fmt.Errorf`
  (`internal/blobfs/commit.go:126`) and survives the unwrap.
- **A diverged filesystem reports `NFS3ERR_IO`, not `NFS3ERR_STALE`.** Both
  divergence paths return a bare error rather than a `vfs.Status` —
  `commit.go:139` (`filesystem diverged: …`) and `commit.go:189` (`commit
  conflict: another writer owns this bucket; …`) — so `statusOf` falls through
  to `vfs.ErrIO`. The write whose drain *discovers* divergence therefore gets
  `NFS3ERR_IO`; the *next* write gets `NFS3ERR_STALE`, from `f.mutable()`
  (`internal/blobfs/fs.go:271-273`), because the failed drain set `f.diverged`.
  This ADR describes that behaviour rather than changing it — `NFS3ERR_IO` for
  "the commit failed" is defensible, and changing an error code is
  client-visible and deserves its own decision rather than arriving inside a
  backpressure ADR. Recorded as open in *What this does not decide*.

No data was buffered and no charge was taken, so a failed drain cannot leak
budget. The writer does not retry the commit itself; a client on a hard mount
will retransmit the `WRITE`, which attempts a fresh drain. Retry policy stays
with the client, which is where NFS puts it.

**There is no wall-clock timeout on the stall.** The wait ends when the budget
drains, when a drain fails, or when `ctx` is cancelled — nothing else. A timeout
would convert "slow bucket" into "failed write", and a client is required to
tolerate a slow reply but is not required to tolerate a spurious error. The
cases where waiting really would be forever are all covered by an explicit
signal instead: a failing bucket fails the drain, a diverged filesystem fails
the drain, and shutdown cancels `ctx`. `Write` already receives a `ctx` and must
honour it.

### 6. The bound this actually guarantees

The issue's done-when says buffered bytes never exceed the configured limit. I
am implementing that as **bounded by** rather than as a hard ceiling, and — per
§2 — against buffer *capacity* rather than buffer length, which is the stricter
of those two readings: the same `-max-dirty` admits less data than a literal
reading would. Saying so rather than quietly moving either one:

> At any moment, `DirtyBytes() <= MaxDirtyBytes + k × W`, where `k` is the
> number of `Write` calls admitted concurrently and `W` is the most capacity one
> call can add: for a call of `n` bytes at offset `off` with chunk size `cs`,
> the number of chunk indices `[off/cs, (off+n-1)/cs]` it spans, times `cs`. A
> single writer issuing calls of at most `cs` bytes spans at most two indices
> and therefore sees at most `MaxDirtyBytes + 2×cs`. `truncate` may add one
> further partial chunk per call.

`W` is `cs` per spanned index because `cs` is what the write path allocates for
an index it has to create, whether the call puts one byte in it or a full chunk
(§2); an index already dirty at full capacity adds nothing. The one case that
is not exactly `cs` is an index staged at a partial length by `truncate` or
`pendingTrim` and then grown by a write, where `append` picks the capacity and
may round above `cs`. That is per-index allocator rounding rather than a term
that scales with the workload, so it is left inside "bounded by" instead of
being given a symbol.

What the bound is a bound *on*: bytes of buffer capacity resident in the
`openFile.dirty` maps. It does not cover the chunk cache (a separate pool — see
*Consequences*), the snapshot body a commit encodes, or per-entry map and
slice-header overhead.

An exact ceiling would require reserving worst-case bytes before buffering and
reconciling afterwards, plus restructuring `truncate`, which holds both locks and
must not block. That is more machinery for a distinction with no operational
meaning: what matters is that memory is bounded by a configured number plus a
small constant, not that the constant is zero.

### 7. Logging the stall

Two lines, both naturally rate-limited, because a line per stalled `WRITE` would
be thousands per second:

- The elected drainer logs once per drain, at `Info`:
  `"commit triggered by write backpressure"` with `dirty_bytes` and `limit`.
- A waiter that has been waiting more than 5 s logs at `Warn`:
  `"writes stalled waiting for commit"` with `waited`, `dirty_bytes` and `limit`.
- A failed drain logs at `Error` with the error.

### 8. Observability

```go
// DirtyBytes reports the memory currently held by unflushed writes across all
// open files: the sum of cap(b) over every buffered chunk b, which is what is
// resident, not the count of logically dirty bytes. A file with one byte
// written into each of three chunk indices reports three chunk sizes.
func (f *FS) DirtyBytes() int64
```

This is the quantity §6 bounds and the quantity §2 accounts; the doc comment
spells out the `cap`/`len` distinction because a caller reading only the name
would assume the other one.

A separate method rather than a sixth return from `Stats()`, which would ripple
into `cmd/strata` for no benefit — at shutdown the number is zero.

Accounting runs even when backpressure is disabled (`MaxDirtyBytes < 0`), so
`DirtyBytes()` is meaningful either way and a test can show that the bound is
caused by the feature rather than by incidental flushing.

### 9. The `vfs` contract

`vfs.FS.Write` carries a doc comment saying an implementation may delay its
reply as backpressure and must honour `ctx` while doing so. This is not a new
method or a changed signature; it records an expectation that the NFS layer and
any future backend both depend on. Without it, someone could reasonably wrap
`Write` in a short per-call timeout and silently break backpressure.

As of this revision that comment is in the tree, on `vfs.FS.Write` in
`internal/vfs/vfs.go` — so §9 is the one part of this ADR already satisfied,
and nothing else here is implemented. It is cited by symbol rather than by line
number, because line numbers are not stable.

## Assumptions

Recorded because the issue did not specify them.

1. **"Never exceed" is read as "bounded by", and "buffered bytes" as buffered
   *capacity*.** §6 and §2. These are the two largest interpretive steps in this
   ADR and the ones most worth pushing back on, separately: the first loosens
   the issue's wording, the second tightens it, and neither is stated by the
   issue.
2. **256 MiB default, unmeasured.** Taken from the issue. I cannot run or
   measure this server. Note that it stacks with `-cache` (default 256 MiB), so
   the out-of-the-box ceiling for these two pools together is ~512 MiB plus
   per-entry overhead; an operator sizing a small host should turn one of them
   down. With §2's `cap` accounting the dirty half of that figure is a real
   resident bound rather than a logical one — what stays uncounted is map and
   slice-header overhead, not unused buffer capacity.
3. **No timeout.** §5. The issue asked only that the commit path make progress.
4. **Fairness is not guaranteed.** `sync.Cond.Broadcast` wakes everyone and the
   winner is whoever is scheduled first, so a writer can in principle be starved
   by others under sustained pressure. A FIFO ticket queue would fix it and is
   not worth the machinery here.
5. **The drain is a full `Sync`**, not a partial flush of the largest buffers.
   `Sync` is what exists, it is what `COMMIT` already does, and a partial drain
   would need a policy for which files to flush.
6. **`truncate` and the `pendingTrim` path charge without waiting** and may
   therefore push the charge above the limit. They hold both locks, so they
   cannot wait; and a truncate is bounded by client SETATTR traffic rather than
   by write throughput. Note also that shrinking a file does not necessarily
   lower the charge: a tail chunk shortened in place is a reslice, and the
   backing array stays resident (§2, site 3).
7. **Stalled writes hold RPC slots.** Once `maxInFlight` (64) requests on a
   connection are stalled writes, other operations on that connection queue
   behind them. That is intended backpressure for writes, but it also delays
   reads on the same connection. Fixing it would need per-procedure concurrency
   classes; out of scope, recorded as a known consequence.

   The arithmetic is more optimistic than "64 stalled writes" suggests, because
   the slots do not correspond one-to-one with distinct client `WRITE`s. In the
   design this ADR and ADR 0002 jointly describe — ADR 0002 is not implemented
   yet, so this is a property of the design rather than of today's running code
   — `WRITE` is classified idempotent (ADR 0002 §4) and so is neither cached
   nor dropped as a duplicate. A client that times out on a stalled write and
   retransmits it therefore gets a *second* handler that also stalls, taking
   another of the 64 slots (`internal/sunrpc/rpc.go:96` sets the bound,
   `rpc.go:132` enforces it). Under sustained backpressure the in-flight window
   fills with retransmissions of writes already waiting, so the read stall
   above arrives sooner than a per-`WRITE` count would predict. It is not a
   deadlock — the drain completes and releases every waiter, retransmissions
   included — but the effective window is narrower than 64 distinct writes.

## Consequences

- A client copying a large file onto a slow bucket now goes as fast as the
  bucket, instead of as fast as memory allows and then dying.
- One failing commit fails every write that is waiting on it, at once. That is
  deliberate: they would all fail in turn anyway, and failing them together
  gives the client a prompt, honest answer.
- `FS.Run` remains optional. Backpressure works without it because the stalled
  writer drains; the ticker is an optimisation, not a dependency.

### On a shared memory budget in phase 1

I flagged previously that if this introduces a global memory budget, the
phase-1 block cache (`docs/DESIGN.md` §14, step 1) should eventually draw from
the same place. **It changes one thing now and nothing else:** keep `budget` a
self-contained type with no knowledge of `FS` internals — which the `await(ctx,
drain func)` shape above already forces — so it can be hoisted or reused later
without touching the write path.

Do **not** unify it with the chunk cache now. The two pools are not the same
kind of memory: cached chunks are *reclaimable* (evict and refetch), dirty
bytes are *unreclaimable* (they must be uploaded before they can be freed). A
single pool over both needs a policy for which one yields under pressure, and
getting that wrong turns a cache-pressure event into a write stall. That policy
is a phase-1 design question with the block cache in front of it, not a question
to answer speculatively now.

## What this does not decide

- **Whether `commitLocked` should return `vfs.Status`-typed errors.** §5
  records that a drain which discovers divergence surfaces as `NFS3ERR_IO`
  while the *next* write surfaces as `NFS3ERR_STALE` — two answers for what a
  client experiences as one condition. Wrapping the two divergence errors
  (`commit.go:139`, `commit.go:189`) in `vfs.ErrStale` would make both report
  `NFS3ERR_STALE`, and is plausibly the right end state. It is left open
  deliberately: it changes a status code a client can observe, which is a
  decision of its own and should not arrive inside a backpressure ADR. What
  would settle it: deciding what a client should do differently on each code —
  `NFS3ERR_STALE` invites a remount, `NFS3ERR_IO` invites a retry, and
  divergence is only recoverable by the first. Whoever takes it up must keep
  `NFS3ERR_IO` for a commit that fails for any *other* reason, since today both
  leave `Sync` through the same return.
- **Whether the dirty budget and the chunk cache draw on one pool.** Left open
  with reasons under *Consequences*; the block cache of `docs/DESIGN.md` §14,
  step 1 is what would settle it.
- **Per-procedure concurrency classes** for the per-connection in-flight
  window. Out of scope here; Assumption 7 records what their absence costs
  under sustained backpressure.

## References

- RFC 1813 §4.12 *Stable versus unstable writes* and §3.3.7 `WRITE` — an
  `UNSTABLE` write need not reach stable storage before the server replies, and
  the client follows up with `COMMIT`. Neither section places any bound on how
  long a server may take to answer.
  <https://www.rfc-editor.org/rfc/rfc1813.txt>
  **Honesty note:** my fetches of this RFC were truncated and returned
  summaries rather than the sections verbatim. Nothing above is quoted as the
  RFC's exact words; a reviewer should check the original.
- `docs/DESIGN.md` §5 (dirty state and consistency points) and §9 (why the
  client's memory is the NVRAM) — the same argument, for the design that
  replaces this code. No edit was made to that document.
- `internal/blobfs/fs.go`, the `FS.mu` doc comment — the existing lock-ordering
  rule this ADR extends.
