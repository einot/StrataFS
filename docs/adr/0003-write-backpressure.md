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
- **Revised:** 2026-09-23 — a revision, not a supersession: the decision
  stands, and is now specified closely enough to implement and test against
  without guessing. A pass over the code before implementation found that §4
  as written would lose data a client had been told was written, could not
  honour `ctx` while a call was parked, and would leave every later writer over
  the limit parked for good after a drain panicked; that a flush racing a
  removal could leave a charge on the budget for good; that several other
  statements were false; and that `5479fe2` had since moved `Write`'s buffering
  into a new `bufferWrite`, which reads the chunk list under `openFile.mu`, and
  removed a self-deadlock that hung a `FILE_SYNC` write. Every code citation is
  re-verified against `5479fe2`. What moved, and why:
  **Context** — stable writes, `FILE_SYNC` and `DATA_SYNC`, are draining paths
  only since `5479fe2`; and at its in-flight bound `serveConn` reads one record
  more before it stops, which the old wording hid.
  **§1** — the progress argument, because a successful drain does not take the
  charge to zero while others write; `defaultMaxDirtyBytes` and `New`'s
  mapping, pinned; and `cmd/strata`'s `maxDirtyBytes`, pinned with an overflow
  rule, because the old shift wrapped.
  **§2** — the citations in *Why the accounted quantity is `cap`*; site 1's
  single charge placed before site 2's check, both now in `bufferWrite`; site 5
  worded by what it does rather than where; the invariant, which site 5 made
  false as written, restated for open files that `FS.open` holds, observed
  under their `mu`, with two statements about the whole budget; an ordering
  rule for the budget and `of.bytes`; the preconditions of `add` and
  `release`; site 4 refined, so that a failed flush of a file whose inode has
  gone releases that file's charge instead of stranding it; and a table of what
  each operation does to `DirtyBytes()`, with how a test reaches each
  `truncate` path.
  **§3** — a paragraph showing that every lock the five sites take, site 4's
  new `FS.mu` included, follows the existing order; and two rules: nothing
  reaches `flushOpen` holding the `openFile.mu` it takes, which the code before
  `5479fe2` broke, and nothing is logged under the budget's lock.
  **§4** — where `Write` waits, now with the requirement that the chunk list is
  read after the wait under `openFile.mu`, and the data-loss trace that follows
  if it is not; the paths that never wait; an inode that goes away; stable
  writes; the algorithm, rewritten, with a fast path, a fixed check order, the
  failure contract G1–G3, abandoned and panicking drains, waking on
  cancellation, and one budget drain at a time; the pinned surface, extended; a
  test surface; and what `Write` returns when the wait fails.
  **§5** — the claim that a hard mount retransmits a failed `WRITE`, removed,
  because an error reply answers the request; and how context errors and
  `errDrainPanicked` reach a client.
  **§6** — `k` defined; the bound holds at every instant; the single-writer
  case; and stalled `WRITE` payloads added to what the bound does not cover.
  **§7** — three records, where it said "two lines" and listed three, with
  their messages, keys, value kinds and timing.
  **§8** — `DirtyBytes()` returns the budget's `used()` and is safe for
  concurrent use.
  **§9** — its closing paragraph, which called §9 the only part of this ADR the
  code already satisfied; since `5479fe2` the code keeps a rule of §3's and one
  of §4's as well, and without this change §3 and §4 would contradict it.
  **Assumptions** — 2 corrected, because the two memory pools do not add up to
  ~512 MiB; 4 and 6 extended; 7 brought up to date now that ADR 0002 is
  implemented; and 8–18 added, one for each judgement call this revision makes.
  ***Consequences*** — two bullets. ***What this does not decide*** — five
  bullets. ***References*** — three Go documentation sources.
  Unchanged: the decision itself — a global budget counted as buffer capacity,
  admission while the charge is below the limit, a waiting writer that drains
  itself holding no lock, and no wall-clock timeout; §1's flag name and 256 MiB
  default; that there are five accounting sites, and where each one is, site
  4's behaviour on a failed flush being refined as above; the form of §6's
  bound; the choice not to share a pool with the chunk cache; §3's existing
  rule; §5's two named failures (`NFS3ERR_ROFS` and divergence); §9's contract;
  Assumptions 1, 3 and 5; the first three *Consequences* bullets, and the
  subsection *On a shared memory budget in phase 1*; the three earlier *What
  this does not decide* bullets; the earlier *References*; the title; and the
  2026-09-22 revision. Status stays `Accepted`.
- **Revised:** 2026-09-24 — clarifications from findings made while
  implementing and testing the budget, and one testability claim corrected. The
  decision did not move, and neither did §4's steps, their check order, what
  each of the four outcomes does, or the meaning of G1 and G2; no name,
  signature or number changed, and `errDrainPanicked` gained its initializer.
  One requirement is stated more widely rather than changed: §4 required a
  settle on every way out of `drain`, and now requires it on every way out of
  the drainer's window, which also covers the Info record's log call, with a
  panic there and a `runtime.Goexit` settled as *Panicked*. And G3 is
  qualified: it holds when the Error record's log call returns, and a panic or
  `Goexit` in that call continues out of `await` in its place. What changed:
  **§4** — `warnAfter`'s pinned comment and the test surface agree, with §7,
  that a test may set it to any value before the budget is first used, where
  two of the three said "lower"; `errDrainPanicked` is declared with
  `errors.New`, its text unpinned, so that the declaration agrees with its
  comment; the settle paragraph and *Panicked* say that the settle covers the
  window from election to relock, that a panic in the Info record's log call
  and a `runtime.Goexit` are settled as *Panicked*, and that the settle cannot
  rely on `recover`; G2 says that a failure reaches the calls still parked
  when the drain is settled, not a call admitted before it; and G3 says what
  the drainer gets when the Error record's log call panics or calls
  `runtime.Goexit`.
  **§7** — a `runtime.Goexit` is logged by nothing.
  **Assumptions** — 2 cites #47; 9 says which calls a failure reaches, that a
  call admitted before the settle is not failed retroactively, and that which
  of the two a woken call gets is scheduling, and it no longer claims that the
  other check order would be neither predictable nor testable, because this
  one leaves a race of its own; 11 records the judgement calls behind the
  settle's reach, and why G3's exception is pinned rather than left open; 12
  says a test may raise `warnAfter` as well as lower it.
  ***What this does not decide*** — the chunk-cache bullet cites #47 and #46.
  ***References*** — `runtime.Goexit` and the built-in `recover`.
- **Revised:** 2026-09-24 — a second pass the same day, documentation only,
  made before the wiring, recording what the review before the wiring found
  stated wrongly or not at all. The decision, §4's algorithm, G1–G3, the five
  sites' rules, every pinned name, signature and comment, and all numbering
  are unchanged; three assumptions are appended. The citations this pass adds
  were checked against `98bfad9`. What changed:
  **§5** — `NFS3ERR_ROFS` depends on the drain having a chunk to upload; when
  every chunk it flushes is already in the bucket, the commit's snapshot `Put`
  fails instead, and the client gets `NFS3ERR_IO`.
  **§6** — the term above the limit, which the text called a small constant,
  is quantified: admission reserves nothing and a release wakes every parked
  call, so `k` reaches 64 per connection, connections are uncapped, and `W` is
  up to `2 × cs` at the default chunk size. That is up to 128 MiB per
  connection at the defaults, with no bound in total. The design is kept, by
  the repository owner's decision (Assumption 19).
  **§9** — its closing paragraph said nothing of the budget was implemented;
  the budget type has been on `main` since #52, and the wiring lands in the
  same change as this revision.
  **Assumptions** — 19, the overshoot, with the two alternatives weighed; 20, a
  panic part-way through an accounting site, accepted; 21, site 2 leaving
  `pendingTrim` alone, which costs I/O and no charge.
- **Revised:** 2026-09-24 — a third pass the same day, documentation only,
  after the security audit of the budget and the wiring together. The audit's
  findings are all in pre-existing code and are tracked as issues rather than
  fixed here; this pass stops the ADR overstating what the budget bounds. The
  decision, §4's algorithm, G1–G3, the five sites' rules, every pinned name,
  signature and comment, and all numbering are unchanged, and no assumption is
  added. The citations this pass adds were checked against `8baf977`. What
  changed:
  **§6** — the bound's closing sentence says `truncate`'s partial chunks come
  per `SETATTR` and without limit between flushes; *Why at every instant* says
  a flush charges a file's whole `pendingTrim` queue at once, without regard to
  the limit (#56); the files' chunk lists join what the budget does not cover
  (#55); the payload figures note that the RPC record may double them (#58);
  and the payload and overshoot figures both note that a connection's parked
  calls outlive it, because they wait on the server's context (#57).
  **§9** — the NFS server's context is cancelled only at shutdown, so a
  disconnect does not end a parked `WRITE` (#57).
  **Assumption 6** — the `pendingTrim` queue is uncharged until a flush,
  neither pruned nor deduplicated, and charged whole by the next flush, with an
  example at the defaults; the same queue can bring truncated bytes back
  (#56). The earlier text covered only the cached case.
  ***What this does not decide*** — enforcing the advertised maximum file size
  (#55).
- **Revised:** 2026-09-24 — a fourth pass the same day, cross-references only.
  Assumption 2 now ends by saying that ADR 0004 decides #47, and the
  chunk-cache bullet under *What this does not decide* by saying that ADR 0004
  decides both #47 and #46; Assumption 17's citation moves to `cache.go:44-46`,
  where the fix moved the code. Nothing else changed.
- **Issue:** #4 — *Buffered writes are unbounded: add backpressure*
- **Affects:** `internal/blobfs`, `cmd/strata`, doc comment on `vfs.FS.Write`

## Context

`blobfs.Write` appends into `openFile.dirty` unconditionally, through
`bufferWrite` since `5479fe2`. The only thing that drains it is a commit — the
`-commit-interval` ticker, an NFS `COMMIT`, or a stable write, one sent
`FILE_SYNC` or `DATA_SYNC`. Stable writes have drained only since `5479fe2`.
Before it, a `FILE_SYNC` write deadlocked on its own file's lock instead of
flushing (§3 has the trace), and a `DATA_SYNC` write was answered `UNSTABLE`
without flushing. A client that writes faster than the bucket ingests grows
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
connection (`maxInFlight`, 64). Once the bound is reached it reads one more
record (`internal/sunrpc/rpc.go:154`), blocks before dispatching it
(`rpc.go:162-166`), and reads nothing further until a slot frees. If 64 writes
are stalled, the connection stops reading altogether, so the client's own
`COMMIT` — the thing that would drain the backlog — is at best read and never
dispatched, and otherwise never read at all. A design that waits for a client
`COMMIT`, or that signals a background committer that an embedder may not have
started (`FS.Run` is optional; the test suites do not call it), deadlocks or
hangs. The stalled writer has to be able to perform the drain itself.

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

```go
// internal/blobfs
const defaultMaxDirtyBytes = 256 << 20

// cmd/strata
//
// maxDirtyBytes converts -max-dirty, in MiB, to Config.MaxDirtyBytes.
func maxDirtyBytes(mib int) int64
```

`New` substitutes `defaultMaxDirtyBytes` for a `MaxDirtyBytes` of zero and
passes every other value, positive or negative, to the budget unchanged. The
budget's `limit()` (§4) therefore reports 256 MiB for zero and the configured
value for anything else.

`maxDirtyBytes` returns `-1` for `mib <= 0`; `-1` for `int64(mib) >
math.MaxInt64>>20`, a limit too large to express in bytes and so one no
workload could reach; and `int64(mib) << 20` otherwise. Converting before
comparing keeps the constant representable where `int` is 32 bits.
`cmd/strata` passes `MaxDirtyBytes: maxDirtyBytes(*maxDirty)`. The first version
mapped `*maxDirty <= 0` to `-1` and anything else to `int64(*maxDirty) << 20`,
which wraps: 2^44 + 1 MiB became a 1 MiB limit (Assumption 16). A flag of zero
means no limit, while `Config`'s zero means the default (Assumption 17). The
unit is MiB for consistency with `-cache` (MiB) and `-chunk-size` (KiB). The
default of 256 MiB is the issue's suggestion.

There is **no minimum**. With the check-then-proceed discipline in §4 any
positive limit keeps the system moving: a writer is admitted whenever the
current charge is below the limit, and a drain that succeeds releases every file
on the list `flushAll` took when it began (`internal/blobfs/fs.go:1357-1364`).
The first version said a successful drain takes the charge to zero. That holds
only while nothing else writes. Releases during a drain wake parked calls, which
are admitted while it is still running; files opened after it began are not on
its list and are not flushed by it; and `truncate` charges without waiting. So
the guarantee is progress for the system, not for any one writer: whatever
refills the budget during a drain is itself work that completed, but a
particular writer can be starved — including a drainer that finds the budget
refilled when its drain returns, and has to drain again (Assumption 4).

### 2. Accounting: the quantity counted, and the five sites that count it

`openFile` gains `bytes atomic.Int64`, the sum of **`cap(v)`** — not `len(v)` —
over its `dirty` map. `FS` gains the budget, whose `used()` tracks the sum over
the live open files; the statements after the five sites say exactly how.
`atomic.Int64` rather than a field under `openFile.mu`, because one of the five
sites runs under `FS.mu` where taking `openFile.mu` would invert the lock order
(§3).

#### Why the accounted quantity is `cap`, not `len`

The budget is a statement about **resident memory**, not about logical dirty
bytes, and a dirty buffer is resident at its capacity however little of it is
written. The buffers are allocated at full chunk capacity throughout:
`internal/blobfs/fs.go:1142` is `make([]byte, 0, cs)`, `fs.go:1153` is
`append(make([]byte, 0, cs), loaded...)`, and `fs.go:1234` is
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
`append` at `fs.go:1161-1163` never needs more than `cs`, so it never grows.
The exception is a buffer that `truncate` or `pendingTrim` staged at a partial
length (`fs.go:1256`, `fs.go:1312`) and a later write then grows: there
`append` chooses the new capacity, which is at least what the write needs and
may round above `cs`. Computing every delta as a difference of `cap` keeps the
accounting exact in that case too, which is why the rule below is stated as a
`cap` delta rather than as "add `cs` per new index".

#### The five sites

1. **`Write`**, in `bufferWrite` under `openFile.mu`, per chunk index touched:
   the delta is `cap(new) - cap(old)`, sampling `cap(old)` from the map entry
   *before* the buffer is mutated, and `cap(old)` is zero when the index was
   absent. Accumulate over the loop (`fs.go:1131-1167`), then charge once:
   after the loop exits, including through the partial-write `break`
   (`fs.go:1146-1147`), which keeps what it stored, and before site 2's check,
   still under `openFile.mu`. A charge placed after site 2's check would leak on
   a write racing an unlink: the check would find the inode gone and release,
   and the charge would land afterwards on buffers nothing will flush. The
   paths out of `bufferWrite` that store nothing charge nothing: an inode
   already gone when `bufferWrite` first resolves it (`fs.go:1113-1130`), and a
   failed first fetch (`fs.go:1149`). A single `Write` call touches each index
   at most once — each iteration either runs to the next chunk boundary or
   exhausts the data, so the indices it visits strictly increase — and
   therefore no index can be double-counted. An index that was already dirty at
   full capacity contributes zero.
2. **`bufferWrite`'s tail block** (`fs.go:1169-1179`), holding `openFile.mu`
   and `FS.mu`, where it re-resolves the handle (`fs.go:1171`): if the inode
   has gone (unlinked, or a generation mismatch) nothing will ever flush this
   buffer. Release the file's whole remaining charge with `of.bytes.Swap(0)`
   and reset `of.dirty` to an empty map. Without this a write racing an unlink
   of the same file leaks budget permanently. The block was `Write`'s own until
   `5479fe2` moved the buffering into `bufferWrite`. Today it skips the size
   update for a vanished inode and leaves the buffers where they are; the reset
   is this site's addition.
3. **`truncate`**, under `openFile.mu` and `FS.mu`: subtract `cap` for every map
   entry it deletes (`fs.go:1227-1231`); add `cap` for the chunk it stages from
   the cache (`fs.go:1256`, a fresh allocation of the truncated length). The
   **shortened tail chunk contributes no delta**: `of.dirty[lastIdx] =
   chunk[:tail]` (`fs.go:1234`) is a reslice, so `len` falls and `cap` does
   not, and the backing array is still resident. Not subtracting there is
   accuracy, not conservatism — the memory really is still held. (The first
   version of this ADR subtracted for it, which was wrong for the same reason
   `len` accounting as a whole was.) Charged **without waiting** — see §6 and
   Assumption 6.
4. **`flushOpen`**, under `openFile.mu`: add `cap` for each entry it
   materialises while resolving `pendingTrim` (`fs.go:1312`); when it replaces
   `of.dirty` with a fresh map (`fs.go:1351`), release `of.bytes.Swap(0)`. On
   each of its two failed returns — a `pendingTrim` fetch (`fs.go:1305-1308`)
   or an upload (`fs.go:1330-1333`) — take `FS.mu` as well, after
   `openFile.mu` as §3 permits, and check whether the inode has gone:
   `f.inodes[id]` is nil, the test its namespace update already makes
   (`fs.go:1338-1339`). If it has, release the file's whole remaining charge
   with `of.bytes.Swap(0)`, reset `of.dirty` to an empty map and
   `of.pendingTrim` to nil, and return the error as before. If the inode is
   still there, leave everything charged: those buffers really are held, and
   the file's next flush retries them. Why, below.
5. **Immediately before every removal of an `openFile` from `FS.open`**, under
   `FS.mu`: release `of.bytes.Swap(0)`. Today the removals are the
   `delete(f.open, id)` in `unlink` (`fs.go:639`) and the one in `Rename`'s
   victim path (`fs.go:717`); the site is named by what it does rather than by
   where, so a removal added later carries the release with it. This is the
   easiest site to miss and the most damaging to miss — a long-running mount
   doing create/write/delete cycles would ratchet the charge upward until it
   sat permanently at the limit with nothing left that could drain it. `Swap`
   makes the release exactly-once even if a flush is releasing concurrently.

#### Ordering, preconditions and the invariant

**Ordering.** Every increase reaches the budget before it reaches `of.bytes`:
`add(n)`, then `of.bytes.Add(n)`. Every decrease leaves `of.bytes` before it
leaves the budget: `of.bytes.Add(-n)` or `of.bytes.Swap(0)`, then `release`
(Assumption 15).

**`add` and `release`** both take `n >= 0`, and neither clamps, so `used()` is
exactly Σ`add` − Σ`release`. Every decrease goes through `release`, because
`release` is what wakes parked writers: a `truncate` that frees more than it
stages releases the difference rather than `add`ing a negative.

**The invariant.** For an `openFile` that `FS.open` holds, `of.bytes` equals the
sum of `cap(v)` over `of.dirty` whenever it is observed holding that
`openFile`'s `mu`. Sites 1 to 4 run entirely under `openFile.mu`, so such an
observer never sees one of them half-done. Site 5 is why both qualifiers are
there. It runs under `FS.mu` alone and may not take `openFile.mu` (§3), so it
zeroes `of.bytes` without clearing `of.dirty`, and an `openFile` it has removed
can hold buffers its `bytes` no longer counts. And it can run while someone
holds `openFile.mu`, so an observer that needs the equality exactly also holds
`FS.mu`, taken after `openFile.mu` as §3 permits, which keeps site 5 out while
it looks. The first version said the invariant "holds once each site returns",
which site 5 makes false. It is directly assertable, and asserting it after
each kind of operation is a better test than checking any single total.

**The whole budget.** Two statements hold across all open files:

- At every instant, `used() >= Σ of.bytes >= 0`, summed over every `openFile`,
  whether `FS.open` still holds it or not. This is what the ordering rule buys:
  an increase is in `used()` before it is in any `bytes`, and a decrease is out
  of a `bytes` before it is out of `used()`.
- When no accounting step is in flight, `used()` equals that sum. It also
  equals the sum over `FS.open` alone, because an `openFile` that has left
  `FS.open` holds no charge. Sites 1 and 2 net to zero on one, site 3 leaves
  one alone, and whatever site 4 charges to one, the same flush releases:
  a success when it replaces the map, a failure through its check for a gone
  inode.

**Why site 4 releases on a failed flush.** `flushAll` lists the open files
before it flushes any (`fs.go:1357-1364`), so a flush can run on an `openFile`
that site 5 has already removed and released. Site 4 then charges whatever it
materialises from `pendingTrim` to a file that nothing will flush again. A flush
that succeeds returns that charge when it replaces the map. One that fails would
strand it for good without the check. Each occurrence would be small, about a
chunk per pending trim, but the charge would never come back, and enough of them
would hold the budget at its limit with nothing able to drain it, which is the
failure site 5 exists to prevent. Making the check under `FS.mu` orders it
against site 5, which runs under `FS.mu` too. If the removal came first, the
check sees the inode gone and releases what site 5 could not see. If the
removal comes after, site 5's own `Swap` takes everything charged so far.
Resetting `pendingTrim` as well stops a second flush of the same `openFile`,
listed by another `flushAll` before the removal, from materialising the same
trims again (Assumption 18).

#### What each operation does to `DirtyBytes()`

The change each operation makes, which is what tests assert against:

| Operation | Change to `DirtyBytes()` |
|---|---|
| `Write` to an index absent from `of.dirty` | `+cs` exactly: the buffer is `make([]byte, 0, cs)` or a copy into one (`fs.go:1142`, `:1153`), and the growth `append` (`fs.go:1161-1163`) stays within it |
| `Write` to an index already dirty, with room | 0 |
| `Write` that grows a buffer `truncate` or `pendingTrim` staged at a partial length | `+(new cap − old cap)`; `append` picks the new capacity, so read it from the map |
| `Write` whose chunk fetch fails after it has stored something (`fs.go:1146-1147`) | the charge for the indices it stored before the failure, and nothing for the rest |
| `Write` that stores nothing because it failed first: a path that never waits (§4), a failed wait, or a failed first fetch (`fs.go:1149`) | 0 |
| `Write` whose inode has gone when `bufferWrite` first resolves it (`fs.go:1113-1130`) | 0: it stores nothing |
| `Write` whose inode goes while it buffers, found at the tail block (`fs.go:1171`) | 0 net: site 2 releases the file's whole remaining charge, this write's included |
| `Sync`, `Commit`, a stable write's sync, or a budget drain, succeeding | every file on `flushAll`'s list drops to 0 (site 4); with nothing else running, the total is 0 |
| The same, with `flushAll` failing partway (`fs.go:1366-1370`) | files flushed before the failing one drop to 0; the failing file changes as the next three rows say; files after it keep their charge |
| `flushOpen` failing while it fetches a `pendingTrim` chunk (`fs.go:1305-1308`), its inode still there | what it had materialised stays charged, until the file's next successful flush |
| `flushOpen` failing during upload (`fs.go:1330-1333`), its inode still there | unchanged, plus anything it materialised from `pendingTrim` |
| Either failure, once the file's inode has gone (removed from `FS.open` after `flushAll` listed it) | site 4 releases the file's whole remaining charge, so nothing stays charged to it |
| `Sync` failing after `flushAll` succeeded — a commit error, divergence included (`internal/blobfs/commit.go:138-139`, `:180-189`) | the flushed files stay released |
| `truncate` deleting dirty indices (`fs.go:1227-1231`) | `−cap` of each |
| `truncate` shortening a dirty tail in place (`fs.go:1232-1236`) | 0 |
| `truncate` staging a cached chunk (`fs.go:1255-1256`) | `+cap` of the staged copy; read it from the map |
| `truncate` deferring to `pendingTrim` (`fs.go:1258`) | 0 now; charged, then released, inside the next flush |
| `truncate` extending the file | 0 |
| `Remove` of the last name, or `Rename` over a victim (`fs.go:639`, `:717`) | minus that file's whole charge |

The two `truncate` rows that add a buffer differ only in whether this `FS`'s
chunk cache holds the chunk, which a test cannot see. How a test reaches each
`truncate` row:

- **Staging from the cache.** The chunk must be in this `FS`'s cache. A flush on
  the same `FS` that uploads the content for the first time puts it there
  (`putChunk`, `fs.go:958-965`), and so does a `Read` on the same `FS` that
  fetches it (`loadChunk`, `fs.go:919`). An upload that is skipped caches
  nothing — content the `FS` already knew (`fs.go:944-946`), or content the
  pre-upload `HEAD` found in the bucket (`fs.go:951-953`) — so the content must
  be new to the bucket.
- **Deferring to `pendingTrim`.** A fresh `FS` mounted on the bucket, whose
  cache is empty, truncating into a stored chunk it has not read.
- **Deleting indices and shortening a tail** need only buffered, unflushed
  writes on the same `FS`.
- A staged buffer's `cap` is whatever `append` chose (`fs.go:1256`, and
  `fs.go:1312` for a materialised trim), so a test reads it from `of.dirty`
  rather than predicting it.

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

Every lock the five accounting sites take follows that order. Sites 2 and 3
take `FS.mu` while holding `openFile.mu`, as `bufferWrite`'s tail block and
`truncate` already do. Site 4's check on a failed flush does the same, as
`flushOpen`'s namespace update already does (`fs.go:1337`). Site 5 runs under
`FS.mu` and takes no `openFile.mu`. Every `add` and `release` takes the budget's
mutex as a leaf, and none of the sites waits on the budget.

Two further rules close the ways around the leaf rule:

- **Nothing may reach `flushOpen` while holding the `openFile.mu` that
  `flushOpen` takes** — directly, or through `commitFile`,
  `syncFileAndNamespace`, `Commit`, `Sync` or `flushAll`. `sync.Mutex` is not
  reentrant. Because `flushAll`, and so `Sync` and `Commit`, reach `flushOpen`
  for every open file, nothing may call those holding any `openFile.mu` at all;
  `await`'s drain is `Sync`, which is one reason `await` must be called holding
  no lock (§4). `Write` has kept this rule since `5479fe2`: `bufferWrite` takes
  `openFile.mu` with a deferred unlock (`fs.go:1105-1106`), so it has released
  it by the time `Write` calls `syncFileAndNamespace` for a stable write
  (`fs.go:1090-1091`), which reaches `flushOpen` through `commitFile`
  (`fs.go:1186`, `:1291`) and locks the same mutex (`fs.go:1297`). The code
  before that commit broke the rule: `Write` locked `openFile.mu` itself with a
  deferred unlock and, for a `FILE_SYNC` write, made the same calls while still
  holding it, so the write never returned. Every later flush of that file
  blocked behind it — the ticker, `COMMIT`, and the shutdown `Sync`
  (`cmd/strata/main.go:147`), whose 60-second context cannot interrupt a mutex
  wait — and under this ADR every drain would have blocked too, stalling every
  writer on the server for good.
- **Nothing is logged while the budget's mutex is held.** A log handler may take
  locks of its own, which the leaf rule forbids, and one that calls back into
  the budget — to read `used()`, say — would deadlock on it. The budget notes
  what it will log while it holds its mutex, and logs after releasing it (§7).

### 4. Where a writer waits, and what it does while waiting

#### Where `Write` waits

`Write` waits **after** the resolve-and-permission block (`fs.go:1057-1072`; so
a write that is going to fail with `EACCES` never stalls) and **before**
`getOpen` and `bufferWrite` (`fs.go:1079`) — holding no filesystem lock at all,
neither `FS.mu` nor any `openFile.mu`. It never waits inside `bufferWrite`,
which holds `openFile.mu` throughout. The call is `f.budget.await(ctx, f.Sync)`.

**Paths that never wait.** These return before the wait point, whatever the
budget holds:

- a `mutable()` failure: `vfs.ErrROFS` on a read-only mount, `vfs.ErrStale` once
  the filesystem has diverged (`fs.go:1050-1052`);
- a zero-length write, which returns `(0, vfs.FileSync, nil)`
  (`fs.go:1053-1055`);
- a bad or stale handle (`fs.go:1058-1062`);
- a directory, `vfs.ErrIsDir` (`fs.go:1063-1066`);
- `EACCES` (`fs.go:1067-1070`).

**The chunk list is read after the wait, under `openFile.mu`.** When
`bufferWrite` fills an index that is not in `of.dirty`, it starts from the
file's current chunk for that index: a fetch of `n.Chunks[idx]`, or zeros past
the end of the list. That list must be read inside `bufferWrite`, while holding
`openFile.mu`, after the wait (`fs.go:1113-1120`) — never from a copy taken
before the wait. This is a requirement the wiring must preserve, not a detail
of today's code. It works because `flushOpen` repoints `n.Chunks` at what it
uploaded and empties `of.dirty` while holding `openFile.mu` (`fs.go:1297`,
`:1337-1351`): to anyone holding that lock, an index missing from `of.dirty`
has its current contents in `n.Chunks`. A copy taken before the wait can
predate a drain that has already consumed the dirty chunk, and a write that
waited is exactly a write that has just run one. With
`cs = MaxDirtyBytes = 4096`, on an empty file:

1. Write `A`×100 at offset 0. Index 0 is new, so its buffer has `cs` of
   capacity and the charge becomes 4096.
2. Write `B`×100 at offset 100. Suppose it copies the chunk list, which is
   empty, before waiting. The charge is at the limit, so it drains: `flushOpen`
   uploads chunk 0 holding the `A`s, points `n.Chunks[0]` at it and empties
   `of.dirty`.
3. It is admitted. Index 0 is no longer in `of.dirty`, and its stale list has
   no index 0, so it starts from zeros instead of fetching the chunk just
   uploaded.
4. Chunk 0 is now 100 zero bytes followed by the `B`s, and the next flush
   replaces the chunk holding the `A`s with it. The `A`s are gone, and the
   client had already been told they were written.

With an older chunk in the stale list instead of none, the rebuild starts from
the older contents and loses the `A`s the same way. Any write that waits and
then partly overwrites a chunk the drain flushed is exposed. Only a write that
covers whole chunks escapes, because it never starts from the old contents
(`fs.go:1140-1142`).

The same rule already closed the same race with a flush that is not the
budget's — the ticker, a `COMMIT`, another writer's stable write — in
`5479fe2`. Before it, `Write` copied the chunk list before taking `openFile.mu`,
so a flush of the same file that ran while it waited for the lock could lose
data the same way. `TestWriteConcurrentFlushLosesNothing` pins the fix,
probabilistically. Backpressure would have turned that possibility into a
certainty for every write that has to wait.

**If the inode goes away.** This is what the code does, with site 2's release
added:

- Gone when `bufferWrite` first resolves it, after the wait
  (`fs.go:1113-1130`): the write reports its full length and success and
  buffers nothing, as though it had landed just before the unlink. It charges
  nothing.
- Gone by the tail block (`fs.go:1171`), unlinked while it buffered: it reports
  what it buffered — its full length, unless a fetch failed partway — and
  success, and site 2 discards the buffers and releases the charge.
- `getOpen` can recreate an `openFile` entry for an inode unlinked between
  `Write`'s checks and the call (#42). The entry is empty and never charged, so
  it does not affect the budget.

**Stable writes.** A `FILE_SYNC` or `DATA_SYNC` write waits exactly as an
`UNSTABLE` one does, in `await` before `bufferWrite`, and syncs only after
`bufferWrite` has returned and released `openFile.mu` (`fs.go:1090-1094`; §3).
Since `5479fe2` both get the same full sync and are answered `FILE_SYNC`.

#### The budget

The wait is check-then-proceed, not reserve-then-write: a writer is admitted
whenever the charge is below the limit. The budget, pinned:

```go
// newBudget returns a budget that admits a writer while used() < limit. A
// limit of zero or less disables waiting. log must not be nil.
func newBudget(limit int64, log *slog.Logger) *budget

// add charges n without waiting. release returns n and wakes every parked
// call. Both require n >= 0 and neither clamps, so used() is exactly
// Σadd − Σrelease.
func (b *budget) add(n int64)
func (b *budget) release(n int64)
func (b *budget) used() int64

// limit reports the limit the budget was built with, unchanged.
func (b *budget) limit() int64

// waiters reports how many await calls are parked waiting for a drain that
// another call is running. The drainer is not counted.
func (b *budget) waiters() int

// await blocks until the charge is below the limit, electing one waiter at a
// time to run drain. It must be called holding no other lock. limit <= 0
// disables waiting; accounting continues regardless.
func (b *budget) await(ctx context.Context, drain func(context.Context) error) error

type budget struct {
	// warnAfter is how long an await call may stay blocked before it logs
	// the Warn record (§7). newBudget sets it to stallWarnAfter. A test may
	// set it to any value before the budget is first used; nothing writes it
	// after that.
	warnAfter time.Duration

	// The rest of the struct is not pinned.
}

const stallWarnAfter = 5 * time.Second

// errDrainPanicked is what a call returns when the drain it was parked on is
// settled as Panicked (see below, and G2). It is a sentinel, compared with
// errors.Is. The declaration's form is pinned; the text given to errors.New
// is not.
var errDrainPanicked = errors.New("...")
```

`FS` gains `budget *budget`, built in `New` from the limit §1 describes and from
`f.log`, the logger `New` has already defaulted (`fs.go:135-137`). The budget
may assume its logger is not nil.

`await(ctx, drain)` runs with `b.mu` held except where noted:

1. **Fast path.** If `limit <= 0` or `used() < limit`, return nil at once,
   without looking at `ctx` (Assumption 8).
2. Note where the call starts: how many budget drains have ended so far.
3. Loop. Each time round, check in this order:
   1. **A failure since the call began.** If a budget drain that ended after
      the call's start failed or panicked, return the latest such failure (G1,
      G2).
   2. **Room.** If `used() < limit`, return nil.
   3. **Cancellation.** If `ctx` is done, return `ctx.Err()`.
   4. **Drain or park.** If no budget drain is in progress, become the
      drainer: mark a drain in progress, note `used()` for the Info record,
      unlock, log the Info record (§7), call `drain(ctx)` **holding no lock at
      all**, relock, and settle the drain as below. Otherwise park on the
      condition variable until woken, and go round again.

A drain is settled on every way out of the drainer's window: the part of step
3.4 that runs from its election, when it marks a drain in progress, to its
relock. The window covers the Info record's log call as well as `drain`, and it
can end in a return from `drain`, a panic, or a `runtime.Goexit`. Settling
clears the in-progress mark, counts the drain as ended, and broadcasts, in the
same hold of `b.mu` as whatever the outcome below records, so no call sees one
without the other. What follows depends on how the window ended:

- **Succeeded** (`drain` returned nil): go round the loop. The drainer is
  admitted by step 3.2 like anyone else. If other writers refilled the budget
  while it drained, it drains again, unless its `ctx` is done by then. Admitting
  it outright instead would let a drain that freed nothing admit a writer over
  the limit, which would break §6's bound.
- **Failed** (`drain` returned an error, and the drainer's `ctx` is not done):
  record the error as the latest failure, unlock, log the Error record (§7), and
  return the error unchanged (G3).
- **Abandoned** (`drain` returned an error, and the drainer's `ctx` is done by
  then): record nothing and log nothing, and return `ctx.Err()`. The parked
  calls go round again, and one of them becomes the next drainer, with its own
  `ctx` (Assumption 10).
- **Panicked** (the window ended without `drain` returning: `drain` panicked or
  called `runtime.Goexit`, or the Info record's log call did so before `drain`
  was called): record `errDrainPanicked` as the latest failure, log nothing,
  and let the panic or the `Goexit` continue unchanged, so `await` does not
  return. A panic still reaches the `recover` in `dispatch` (ADR 0002 §6), which
  turns it into `SYSTEM_ERR` for the drainer's `WRITE`. A later call can become
  the next drainer (Assumption 11).

The settle for a window that ends without `drain` returning has to be deferred,
and it cannot rely on `recover`. During a `runtime.Goexit`, `recover` returns
nil (*References*), so a settle that runs only when `recover` reports a panic
never runs for one: the in-progress mark stays set, and every later call over
the limit parks for good. During a panic, calling `recover` stops it, which
Assumption 11 rules out. A deferred settle that asks whether `drain` returned
covers both. The Info record stays inside the window, where step 3.4 and §7 put
it, and the deferred settle must already be in place when it is logged, so that
a log handler that panics is settled exactly as a drain that panics is. The
Error record is logged after the settle (*Failed*), so a handler that panics
there, or calls `runtime.Goexit`, leaves nothing unsettled; G3 says what the
drainer gets.

**The failure contract.** Three rules, observable from outside, fix which calls
see a drain's failure:

- **G1.** A call never returns a failure from a drain that ended before the call
  began.
- **G2.** A call still parked when a drain is settled as *Failed* or *Panicked*
  returns that failure — `errDrainPanicked` for *Panicked* — or the failure of a
  later drain. Still parked means waiting in step 3.4 at the settle, including
  a call that a broadcast has woken but that has not yet reacquired `b.mu`. A
  call that a release woke while the drain ran, and that found room at step 3.2
  before the settle, has been admitted with nil, and the failure does not reach
  it.
- **G3.** The drainer returns its own drain's failure: the error value `drain`
  returned, unchanged. This holds when the Error record's log call returns. If
  that call panics or calls `runtime.Goexit`, the panic or `Goexit` continues
  out of `await` unchanged in place of the return, and the calls still parked
  get the drain's error all the same, because the settle recorded it before
  the call (Assumption 11).

The first version kept only the latest drain's result, so a failed drain
followed by a successful one before a parked call ran would admit that call
instead of failing it. Keeping the latest failure apart from the count of
drains that have ended is what G2 needs (Assumption 9).

**One budget drain at a time.** `drain` is `FS.Sync`. At most one drain
*started by the budget* is in progress at a time; the alternative — every
stalled writer calling `Sync` — produces one redundant namespace snapshot per
stalled writer, precisely when the bucket is already the bottleneck. `Sync`s
started elsewhere — the ticker, `COMMIT`, a stable write, shutdown — are
independent of it: they can overlap it and each other, as they already do, and
the budget neither waits for them nor counts them as its drain.

**Admission while a drain runs.** `release` broadcasts, so a parked call also
wakes when the ticker, a `COMMIT`, a stable write, a `truncate` that drops
buffers or an unlink frees space — and when the budget's own drain does,
because `flushOpen` releases each file as it finishes it (site 4). A woken call
that finds room is admitted there and then, usually while the drain that made
the room is still running. It does not wait for that drain's outcome; if the
drain then fails, the failure reaches the calls still parked (G2) and the
drainer (G3).

**Waking on cancellation.** A parked call returns `ctx.Err()` promptly once its
`ctx` is done, whether or not any drain ends, as `vfs.FS.Write`'s contract
requires. `sync.Cond.Wait` cannot return unless awoken by `Broadcast` or
`Signal` (*References*), so checking `ctx` only at the top of the loop, as the
first version did, is not enough. `await` registers `context.AfterFunc(ctx, f)`
before it first parks, where `f` takes `b.mu` and broadcasts, and it calls the
returned stop function on every way out. Taking the lock in `f` is what makes
the wake-up impossible to miss: the broadcast cannot fall between a call's
check of `ctx` and its `Wait`. The standard library's own "AfterFunc (Cond)"
example uses exactly this pattern (*References*). A broadcast wakes every
parked call, not just the cancelled one; the others go round the loop and park
again, which is why each wake-up is followed by the full check order. The
drainer is not parked. It returns when `drain` returns, and `FS.Sync` notices
cancellation in its store calls but not while it waits for `FS.mu`, or for an
`openFile.mu`, that another commit or flush holds (#43).

#### What `Write` returns when the wait fails

`await` returns the drain's error value unchanged (G2, G3), `ctx.Err()`, or
`errDrainPanicked`, and `Write` returns `(0, 0, err)` with that `err`, not
wrapped, as its other early returns do (`fs.go:1061`, `:1065`, `:1069`); a zero
`vfs.Stability` is `vfs.Unstable`. A stable write whose wait fails returns the
same way, without syncing. Nothing is touched: the call creates no `openFile`,
because the wait comes before `getOpen`; it buffers nothing; it changes neither
size nor mtime; and it adds no charge. `DirtyBytes()` can nonetheless have gone
*down* in the meantime, because a drain that failed partway may have flushed
some files first (§2's table).

#### The test surface

ADR 0002 §8 set the precedent of naming unexported identifiers so that a test
written from the ADR can reference them. Following it, these unexported names
are part of this ADR's interface, so a clean-room test in package `blobfs` can
reach them without reading `fs.go`:

- `FS.budget`; `newBudget`; the budget's `add`, `release`, `used`, `limit`,
  `waiters`, `await` and `warnAfter`; and `stallWarnAfter`, `errDrainPanicked`
  and `defaultMaxDirtyBytes`.
- `FS.mu`, the namespace lock, a `sync.RWMutex`.
- `FS.open`, a `map[uint64]*openFile` keyed by inode number, which is the
  `vfs.Attr.FileID` that `GetAttr` reports (`internal/blobfs/inode.go:104`).
  Read it holding `FS.mu` (a read lock is enough), and release `FS.mu` before
  locking an `openFile.mu` (§3).
- `openFile.mu`, a `sync.Mutex`; `openFile.dirty`, a `map[uint64][]byte` from
  chunk index to buffer; `openFile.pendingTrim`, a slice whose length is all a
  test needs; and `openFile.bytes`, the `atomic.Int64` of §2.

A test may read all of these. It may write only `warnAfter`, to any value, and
only before the budget is first used. Renaming any of them changes this ADR's
interface.

### 5. What happens when the commit fails, and whether there is a timeout

**On failure:** the drain's error is broadcast, and every call still parked on
it — and the one that ran it — abandons the wait and returns that error from
`Write` (§4's G2 and G3). The NFS layer maps it through the existing `statusOf`
(`internal/nfs/wire.go:52`), which `errors.As`-unwraps a `vfs.Status` from
anywhere in the chain and passes it through unchanged, and turns anything else
into `NFS3ERR_IO`. Concretely, for the two failures worth naming:

- **A read-only store reports `NFS3ERR_ROFS`, when the drain has a chunk to
  upload.** `putChunk` returns `vfs.ErrROFS` (`internal/blobfs/fs.go:960`); it
  propagates through `flushOpen` → `flushAll` → `Sync`'s wrapping `fmt.Errorf`
  (`internal/blobfs/commit.go:126`) and survives the unwrap. But `putChunk`
  uploads only content that is new: it skips a chunk whose hash this `FS` has
  already stored or fetched, or that the `HEAD` before the upload finds in the
  bucket (`fs.go:944-946`, `:951-953`). When every chunk the drain flushes is
  skipped that way, `flushAll` succeeds and the commit's snapshot `Put` fails
  instead, with `store.ErrUnsupported` wrapped by `fmt.Errorf`
  (`commit.go:163-165`) rather than mapped to `vfs.ErrROFS` as `putChunk` maps
  it. With no `vfs.Status` in the chain, the client gets `NFS3ERR_IO`. The
  earlier text stated `NFS3ERR_ROFS` without the condition.
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

**Context errors and `errDrainPanicked` report `NFS3ERR_IO`.** A call that gives
up because its `ctx` is done returns `context.Canceled` or
`context.DeadlineExceeded`, and a call parked on a drain that panicked returns
`errDrainPanicked` (§4). None of them carries a `vfs.Status`, so `statusOf`
(`wire.go:52-64`) falls through to `vfs.ErrIO` for each (`wire.go:63`).

No data was buffered and no charge was taken, so a failed drain cannot leak
budget (§4 lists what `Write` leaves untouched). The writer does not retry the
commit itself, and the client does not resend the `WRITE` either: an error reply
answers the request, and a client retransmits only a request it has had no reply
to, on a hard mount or a soft one. The first version said a hard mount would
retransmit, which was wrong. Whether the write is tried again is up to the
application. Retry policy stays with the client, which is where NFS puts it.

**There is no wall-clock timeout on the stall.** The wait ends when the budget
drains, when a drain fails or panics, or when `ctx` is cancelled — nothing
else. A timeout would convert "slow bucket" into "failed write", and a client is
required to tolerate a slow reply but is not required to tolerate a spurious
error. The cases where waiting really would be forever are all covered by an
explicit signal instead: a failing bucket fails the drain, a diverged filesystem
fails the drain, and shutdown cancels `ctx`. `Write` already receives a `ctx`
and must honour it.

### 6. The bound this actually guarantees

The issue's done-when says buffered bytes never exceed the configured limit. I
am implementing that as **bounded by** rather than as a hard ceiling, and — per
§2 — against buffer *capacity* rather than buffer length, which is the stricter
of those two readings: the same `-max-dirty` admits less data than a literal
reading would. Saying so rather than quietly moving either one:

> At every instant — not only between calls — `DirtyBytes() <= MaxDirtyBytes +
> k × W`, where `k` is the largest number of `Write` calls that are, at one
> moment, admitted by `await` but not yet charged at site 1, and `W` is the most
> capacity one call can add: for a call of `n` bytes at offset `off` with chunk
> size `cs`, the number of chunk indices `[off/cs, (off+n-1)/cs]` it spans,
> times `cs`. A single writer issuing calls of at most `cs` bytes spans at most
> two indices and therefore sees at most `MaxDirtyBytes + 2×cs`. `truncate` may
> add one further partial chunk per `SETATTR`, outside this bound and without
> limit between flushes (Assumption 6).

`W` is `cs` per spanned index because `cs` is what the write path allocates for
an index it has to create, whether the call puts one byte in it or a full chunk
(§2); an index already dirty at full capacity adds nothing. The one case that
is not exactly `cs` is an index staged at a partial length by `truncate` or
`pendingTrim` and then grown by a write, where `append` picks the capacity and
may round above `cs`. That is per-index allocator rounding rather than a term
that scales with the workload, so it is left inside "bounded by" instead of
being given a symbol.

Why at every instant: a call is admitted only while the charge is below the
limit, and once admitted it charges at most `W`. While the charge is at or above
the limit nobody is admitted, so everything charged since it last stood below
the limit came from calls admitted before then, and at most `k` of those can be
admitted and not yet charged. `truncate` and `pendingTrim` charges fall outside
that argument, which is why the bound names them separately, and nothing else
bounds them. Between flushes, every size-setting `SETATTR` can stage a cached
chunk, charged at once, or queue a `pendingTrim` entry, not charged at all; the
next flush then materialises a file's whole queue in one pass, charging it at
site 4 without waiting and without regard to the limit (Assumption 6). #56
tracks the fix.

For a single writer, with one `Write` in flight at a time and so `k = 1`, that
means `DirtyBytes() <= MaxDirtyBytes + W` after each call returns, `W` being
that call's. `W` is then exact: the call charges precisely `cs` for each index
it spans that was absent from `of.dirty`, unless the index was staged at a
partial length by `truncate` or `pendingTrim`.

What the bound is a bound *on*: bytes of buffer capacity resident in the
`openFile.dirty` maps. It does not cover the chunk cache (a separate pool — see
*Consequences*), the snapshot body a commit encodes, per-entry map and
slice-header overhead, or the files' chunk lists, which one `SETATTR`, or one
`WRITE` at a huge offset, can lengthen without bound (#55; *What this does not
decide*). Nor does it cover the `WRITE` payloads that backpressure
itself holds. A stalled call keeps its decoded data across the wait: `Opaque`
copies it out of the RPC record (`internal/xdr/xdr.go:139-140`, called at
`internal/nfs/nfs3.go:269`), and the handler reslices it to the count without
shrinking it (`nfs3.go:280`), so the call holds the whole opaque it was sent. A
connection can hold 64 such calls (`internal/sunrpc/rpc.go:111`), and one more
record read past the bound (*Context*): about 64 MiB when the client keeps to
the advertised `wtmax` of 1 MiB (`nfs3.go:16`, `:560`), and up to about 512 MiB
when it sends records at the 8 MiB maximum (`rpc.go:48`). Those figures count
the payload copies only; whether the RPC record each call was decoded from also
stays reachable across the wait, which would double them, is yet to be
measured (#58). Connections are not capped (`rpc.go:129-138`), so neither is
the total. Nor are the figures bounded by the connections that are open. A
parked call waits on the context `Serve` was given, which only shutdown
cancels (`rpc.go:137`, `:174`, `:287`), so a call parked when its client
disconnects stays parked, holding its payload, until a drain lets it through or
fails, or the server shuts down (#57).

An exact ceiling would require reserving worst-case bytes before buffering and
reconciling afterwards, plus restructuring `truncate`, which holds both locks and
must not block. This ADR does not take that on, so what it guarantees is a
configured number plus `k × W`, and the first version was wrong to call that
term a small constant. Nothing bounds `k` but the server's concurrency.
Admission reserves nothing: a call is admitted when it finds the charge below
the limit, and it charges only later, at site 1. And `release` wakes every
parked call (§4), so a release that takes the charge below the limit can admit
every call parked at that moment, one after another, before any of them has
charged. Over NFS a connection runs at most 64 calls at once
(`internal/sunrpc/rpc.go:111`, `:162-166`), so `k` can reach 64 per
connection; connections are not capped (`rpc.go:129-138`), and, as for the
payloads, a connection's parked calls outlive it (#57). The server
truncates a `WRITE` to 1 MiB (`internal/nfs/nfs3.go:16`, `:277-280`), so `W`
is at most `2 × cs` when `cs` is at least 1 MiB, the default included, and less
than 1 MiB plus `2 × cs` for a smaller chunk size. At the defaults that is up
to 64 × 2 MiB, 128 MiB, per connection above the limit. It grows with the chunk
size, and, like the payloads above, it has no bound in total. It is documented
here and kept (Assumption 19).

### 7. Logging the stall

Three records, each naturally rate-limited, because a line per stalled `WRITE`
would be thousands per second. The first version said "two lines" and listed
three.

| Level | Message | Attributes | Logged |
|---|---|---|---|
| `Info` | `commit triggered by write backpressure` | `dirty_bytes`, `limit` | by the elected drainer, once per drain, before it calls `drain` |
| `Warn` | `writes stalled waiting for commit` | `waited`, `dirty_bytes`, `limit` | at most once per `await` call, while that call is still blocked |
| `Error` | `backpressure commit failed` | `err` | once per failed drain |

- **Info.** `dirty_bytes` is `used()` when the drainer was elected. The record is
  out before `drain` is called, so a drain that hangs has already been
  announced.
- **Warn.** A call logs it once it has been blocked longer than `warnAfter`,
  counted from when it entered `await` and including any time it spends
  draining. So the drainer counts, and a single writer stuck behind a hung
  bucket logs it. It is logged while the call is still blocked, not when it
  wakes, which during a hang would be never, the case the record exists for.
  `waited` is the time since entry at that moment, and `dirty_bytes` and
  `limit` are read then too. `warnAfter` is a budget field that `newBudget` sets
  to `stallWarnAfter`, five seconds; a test may set it to any value before the
  budget is first used (§4). A natural
  mechanism is one `time.AfterFunc` timer per call that gets past the fast
  path, stopped on every way out. `Timer.Stop` does not wait for a callback that
  has already started (*References*), so the callback checks under `b.mu` that
  its call is still blocked, notes `used()`, releases `b.mu`, and only then
  logs; a callback that loses the race with its call's return logs nothing.
- **Error.** `err` is the error `drain` returned. There is no Error record for
  an abandoned drain, whose error is only the drainer's cancellation, or for a
  panicked one, which `dispatch` already logs when it recovers the panic
  (`internal/sunrpc/rpc.go:283`). A `runtime.Goexit`, which §4 also settles as
  *Panicked*, is logged by nothing (Assumption 11).

Attributes are top-level key–value pairs, not grouped. `dirty_bytes` and
`limit` are `int64` (`slog.KindInt64`), `waited` is a `time.Duration`
(`slog.KindDuration`), and `err` is the error value itself. Nothing is logged
while `b.mu` is held (§3).

The Info and Error records are one per drain, and budget drains run one at a
time. The Warn record is one per stalled call, and stalled calls are bounded by
the in-flight window, 64 per connection, not by write throughput.

### 8. Observability

```go
// DirtyBytes reports the memory currently held by unflushed writes across all
// open files: the sum of cap(b) over every buffered chunk b, which is what is
// resident, not the count of logically dirty bytes. A file with one byte
// written into each of three chunk indices reports three chunk sizes. It is
// safe for concurrent use.
func (f *FS) DirtyBytes() int64
```

`DirtyBytes()` returns the budget's `used()`: one read of what §2 accounts, not
a walk over `FS.open`, which would need every file's lock and could still miss a
charge in flight. §2's statements about the whole budget say how the two relate.

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

`blobfs` honours `ctx` while a write waits (§4), but the NFS server passes every
call the context `Serve` was given (`internal/sunrpc/rpc.go:137`, `:174`,
`:287`), which only shutdown cancels. So today `ctx` ends a parked `WRITE` at
shutdown and not when its client disconnects: such a call stays parked until a
drain lets it through or fails (#57).

As of the 2026-09-22 revision that comment is in the tree, on `vfs.FS.Write` in
`internal/vfs/vfs.go`, so §9 is already satisfied. Since `5479fe2` so are two
rules this ADR states about the write path's locking — §3's rule that nothing
reaches `flushOpen` holding the `openFile.mu` it takes, and §4's rule that the
chunk list is read under `openFile.mu` — because the code already keeps them.
The budget type of §4 has been on `main` since #52, as
`internal/blobfs/budget.go`, and it is wired into `FS` — §1's configuration,
§2's five sites, §4's wait in `Write` and §8's `DirtyBytes()` — by the change
that carries the second 2026-09-24 revision. The comment is cited by symbol
rather than by line number, because line numbers are not stable.

## Assumptions

Recorded because the issue did not specify them.

1. **"Never exceed" is read as "bounded by", and "buffered bytes" as buffered
   *capacity*.** §6 and §2. These are the two largest interpretive steps in this
   ADR and the ones most worth pushing back on, separately: the first loosens
   the issue's wording, the second tightens it, and neither is stated by the
   issue.
2. **256 MiB default, unmeasured.** Taken from the issue. I cannot run or
   measure this server. It stacks with `-cache` (default 256 MiB), and an
   operator sizing a small host should turn one of them down. This assumption
   first said the two pools together come to ~512 MiB plus per-entry overhead.
   They do not, and the cause is the cache, not this ADR. The dirty half is a
   real resident bound, up to §6's terms: with §2's `cap` accounting, what stays
   uncounted is map and slice-header overhead, not unused buffer capacity. The
   cache half is not. `putChunk` hands the cache the flushed dirty buffer itself
   (`internal/blobfs/fs.go:965`, reached from `flushOpen` at `fs.go:1330`), and
   the cache charges an entry by `len` (`internal/blobfs/cache.go:53`, `:64`)
   while the buffer stays resident at its full capacity. A flushed buffer
   holding 4 KiB of data in 1 MiB of capacity is charged 4 KiB and holds 1 MiB —
   §2's argument, applied to the other pool — so the cache can hold many times
   its configured size. That is left to #47 (*What this does not decide*).
   ADR 0004 decides it: the cache keeps its own exact-length copy of each chunk
   and charges what it holds, so the cache half is a real bound too, up to
   per-entry overhead.
3. **No timeout.** §5. The issue asked only that the commit path make progress.
4. **Fairness is not guaranteed.** `sync.Cond.Broadcast` wakes everyone and the
   winner is whoever is scheduled first, so a writer can in principle be starved
   by others under sustained pressure. A FIFO ticket queue would fix it and is
   not worth the machinery here. That includes the drainer. When other writers
   refill the budget while it drains, it drains again (§4), and under sustained
   pressure it can go on doing so while every drain it runs admits others. What
   is guaranteed is progress for the system, not for a writer (§1).
5. **The drain is a full `Sync`**, not a partial flush of the largest buffers.
   `Sync` is what exists, it is what `COMMIT` already does, and a partial drain
   would need a policy for which files to flush.
6. **`truncate` and the `pendingTrim` path charge without waiting** and may
   therefore push the charge above the limit. They hold both locks, so they
   cannot wait. Note also that shrinking a file does not necessarily lower the
   charge: a tail chunk shortened in place is a reslice, and the backing array
   stays resident (§2, site 3).

   Nothing bounds what they add between flushes. Each size-setting `SETATTR`
   can stage one cached tail chunk (`internal/blobfs/fs.go:1255-1256`), charged
   at once, or queue one `pendingTrim` entry, charged not at all until a flush.
   A workload that only truncates never triggers a drain, because only `Write`
   waits, so both are flushed only when something else commits: the ticker, a
   `COMMIT`, a stable write, or a writer's drain. Between flushes the staged
   excess grows with the number of `SETATTR`s. The queue is neither pruned nor
   deduplicated, and the next flush materialises its whole backlog in one pass,
   charging each entry at site 4 without waiting and without regard to the
   limit. An entry that truncates into a hole costs that flush no fetch, so at
   the defaults 1,000 `SETATTR`s in one commit interval, each truncating into a
   different hole, can hold about 1 GiB at that flush with nothing written. The
   same queue can also bring truncated bytes back without a `WRITE`. Both are
   pre-existing, and #56 tracks the fix; this ADR does not decide it. The
   earlier text covered only the cached case. The alternative is to wait in
   `truncate` before it takes its locks, as `Write` does, and ADR 0002 would
   tolerate that: `SETATTR` is non-idempotent, so a stalled one holds its
   duplicate-cache marker only for the length of the stall, which ADR 0002 §7
   accepts of any slow call, and a retransmission meanwhile is dropped as a
   duplicate still in flight. I did not choose it. Whether a truncate will stage
   anything is known only under both locks, so waiting before them would hold
   every size-setting `SETATTR` behind a drain, including the common case,
   which shrinks a file and frees memory.
7. **Stalled writes hold RPC slots.** Once `maxInFlight` (64) requests on a
   connection are stalled writes, other operations on that connection queue
   behind them. That is intended backpressure for writes, but it also delays
   reads on the same connection. Fixing it would need per-procedure concurrency
   classes; out of scope, recorded as a known consequence.

   The arithmetic is more optimistic than "64 stalled writes" suggests, because
   the slots do not correspond one-to-one with distinct client `WRITE`s. ADR
   0002 is implemented, and `WRITE` is classified idempotent (ADR 0002 §4;
   `internal/nfs/nfs3.go:45`), so it is neither cached nor dropped as a
   duplicate: `dispatch` consults the cache only for procedures that are not
   idempotent (`internal/sunrpc/rpc.go:254`). A client that times out on a
   stalled write and retransmits it therefore gets a *second* handler that also
   stalls, taking another of the 64 slots. The bound is the `maxInFlight` field
   (`rpc.go:97`), set to 64 in `NewServer` (`rpc.go:111`); `serveConn` takes a
   slot before it dispatches a record (`rpc.go:162-166`) and gives it back when
   the call finishes (`rpc.go:173`). Under sustained backpressure the in-flight
   window fills with retransmissions of writes already waiting, so the read
   stall above arrives sooner than a per-`WRITE` count would predict. It is not
   a deadlock — the drain completes and releases every waiter, retransmissions
   included — but the effective window is narrower than 64 distinct writes.

   None of this spends ADR 0002's 4096 cache entries. A stalled `WRITE` holds no
   marker, being idempotent, and a call queued behind stalled writes — the
   record read past the bound, or anything still unread in the socket — holds
   none until it is dispatched, because `dispatch` installs the marker
   (`rpc.go:256`) only after `serveConn` has given the call a slot.

Assumptions 8–18 come from the 2026-09-23 revision, and record the judgement
calls it made while pinning what the first version left open.

8. **Fast path first: `ctx` counts only when a call would otherwise block.** §4.
   A call that finds room, or a disabled budget, returns nil even when its `ctx`
   is already done. The other reading, in which a done `ctx` fails the call
   whatever the budget holds, is equally consistent with `vfs.FS.Write`'s
   contract, which asks only that an implementation that *delays* return once
   `ctx` is done. I chose this one because it keeps a disabled budget invisible
   and leaves a call that does not wait exactly as it is today. `Write` never
   looks at `ctx` itself; only the store calls it makes can, and of the two
   stores only the S3 client does (`internal/store/s3.go:117-118`, `:126`).
9. **Failure generations, with failure checked before room.** §4's G1–G3. That a
   later successful drain must not hide an earlier failure from the calls parked
   on it follows from *Consequences* ("fails every write that is waiting on
   it"); keeping the latest failure apart from the count of drains is my
   mechanism for it. Checking for a failure before checking for room is a
   judgement of its own: a call still parked when a failed drain is settled
   fails, even if that drain freed enough room for it before it failed. The
   failure reaches no call but those and the drainer (G2, G3). A drain frees
   room through `release`, which broadcasts (§4, *Admission while a drain
   runs*), so a parked call can wake, find room and be admitted with nil while
   the drain is still running. It is not failed retroactively when the drain
   then fails, and its write goes ahead. So when a failing drain frees room
   before it fails, whether a woken call is admitted or failed depends on
   whether it looks before the settle or after it. That is scheduling: both
   outcomes conform, and a test cannot force either without a hook inside
   `await`. If nothing frees room before the settle, every call still parked
   then fails, and that is what a test can pin. I chose this order because the
   first version's §5 has every waiter return the failure, and *Consequences*
   gives the reason: they would all fail in turn anyway, and failing them
   together gives the client a prompt, honest answer. The cost is that a partly
   successful drain fails some writes that could have been admitted, and which
   ones is not determined.
10. **An abandoned drain publishes nothing.** §4. When `drain` fails and the
    drainer's own `ctx` is done by then, the error is most likely that
    cancellation, not a verdict on the bucket, and broadcasting it would fail
    other writers with someone else's cancellation. So the drainer returns
    `ctx.Err()`, no failure is recorded or logged, and a parked call becomes the
    next drainer with its own `ctx`. The cost: a genuine store failure that
    coincides with the drainer's cancellation is not published either; the next
    drainer meets it again and publishes it then.
11. **A panicking drain wakes everyone with `errDrainPanicked`, and the budget
    does not recover the panic.** §4. That the budget must clean up after a
    panic is not a judgement: `dispatch` recovers a handler's panic (ADR 0002
    §6), and without cleanup the in-progress mark would stay set and every later
    writer over the limit would park forever. The rest are judgements. Parked
    calls get a distinct sentinel rather than going round again, because a panic
    means a bug, and failing them promptly and recognisably is more honest than
    having them re-run it; a call that arrives later can still drain (G1). The
    panic itself propagates unchanged, so that whoever would have handled it
    still does, rather than being turned into an error value the drainer's
    caller would misread as a store failure. And the budget logs nothing for it,
    because `dispatch` logs the recovered panic itself.

    The same goes for the rest of the drainer's window (§4). A panic in the Info
    record's log call, before `drain` is called, and a `runtime.Goexit` anywhere
    in the window are settled as *Panicked* too. That they are settled at all is
    again not a judgement, since left unsettled either would leave the
    in-progress mark set, just as a panicking drain would. Settling them as
    *Panicked*, rather than as abandoned drains whose parked calls elect
    another drainer, is a judgement: neither leaves an error to publish, and
    each means a bug that the next drainer would likely meet again, because it
    logs the same Info record and runs the same `drain`. It has two costs. The
    sentinel says the drain panicked when the fault may have been the log
    handler's, or a `Goexit`. And a `Goexit` is logged by nothing: the budget
    logs nothing for a *Panicked* drain, and `dispatch` logs only a panic that
    its `recover` returns (`internal/sunrpc/rpc.go:282-283`). Nothing in this
    repository calls `runtime.Goexit` outside its tests, so I left that silence
    alone rather than give it a record of its own.

    A panic or `Goexit` in the Error record's log call comes after the settle,
    so it needs none, and it continues out of `await` in place of G3's return
    (§4). I pinned that rather than leave it open. A `Goexit` leaves no choice,
    since `await` cannot return once one has begun. For a panic, the
    alternative is to recover it and return the drain's error, which would make
    that log call the one place the budget recovers, and would treat a handler
    that breaks there unlike one that breaks on the Info record. The cost falls
    on the drainer's `WRITE`: `dispatch` answers it `SYSTEM_ERR` rather than
    `NFS3ERR_IO`, and logs the handler's panic rather than the drain's error
    (`internal/sunrpc/rpc.go:282-285`, `:293-295`).
12. **The Warn record is per call, fires while the call is blocked, counts the
    drainer, and its threshold is a field.** §7. The first version's "a waiter
    that has been waiting more than 5 s" could mean logging on wake-up, which is
    silent during a hung drain, the case the record exists for; at the moment
    the threshold is crossed; or repeatedly. And "waiter" could exclude the
    drainer, in which case a single stalled writer never logs anything. I chose
    once, at the crossing, drainer included, measured from entry. `warnAfter` is
    a field rather than a bare constant so that a test can choose the
    threshold: lower, so that it need not wait five seconds, or higher, so that
    a short wait is sure to stay under it. The five seconds are the first
    version's.
13. **`waiters()` excludes the drainer.** §4. A test needs to know when calls
    have parked. Excluding the drainer makes "one drain in progress and seven
    parked" read 7, and makes the count change only when a call parks or wakes,
    not when the drainer finishes. Nothing required either reading.
14. **The Error record's message and key, and the records' other details.** §7.
    The first version said only that a failed drain logs at `Error` with the
    error. The message `backpressure commit failed` is mine; the key `err`
    follows the commit path's own failure records
    (`internal/blobfs/commit.go:345`, `:350`). Also mine: the value kinds,
    top-level attributes rather than a group, and logging the Info record before
    `drain` is called rather than after, so that a drain that hangs has already
    been announced.
15. **Charge ordering.** §2. Nothing required an order between the budget and
    `of.bytes`, or a place for site 1's charge. Increases reaching the budget
    first and decreases leaving `of.bytes` first keeps
    `DirtyBytes() >= Σ of.bytes >= 0` at every instant; the other order would
    let site 5's `Swap` release bytes the budget had not yet received, briefly
    driving `DirtyBytes()` negative. Charging before site 2's check makes a
    write racing an unlink net to zero; charging after it would leak.
16. **`maxDirtyBytes`'s overflow rule.** §1. A `-max-dirty` above
    `math.MaxInt64>>20` MiB, about 8.8 trillion MiB, maps to `-1`, no limit,
    rather than wrapping, as the first version's shift did, or clamping to
    `math.MaxInt64`. A limit that large can never be reached, so no limit is
    what the operator gets either way; `-1` says so in `limit()` rather than
    disguising it as a number.
17. **`-max-dirty 0` means no limit, unlike `-cache 0` and
    `-snapshot-retention 0`.** §1. Both of those mean "the default": `-cache 0`
    reaches `newChunkCache` as zero, which selects 256 MiB
    (`internal/blobfs/cache.go:44-46`), and `-snapshot-retention 0` reaches
    `New` as zero, which selects 10 (`internal/blobfs/fs.go:139-140`).
    `-max-dirty` follows its own help text, "0 or less: no limit". I kept that
    rather than match its neighbours, because an operator who sets a memory
    limit to 0 more plausibly means "none" than "the default", and because §1's
    help text has said so since the first version. The inconsistency is
    recorded, not fixed. `Config.MaxDirtyBytes` differs again: zero there
    selects the default, as it must for an embedder who never sets the field.
18. **Site 4 releases on a failed flush only when the inode has gone.** §2.
    That a failed flush must not strand a charge follows from what site 5 is
    for; how site 4 avoids it is mine. A failed flush of a file that is still
    there keeps its charge, because those buffers really are resident and the
    file's next flush retries them; releasing them would under-count memory
    that is held. The test is `f.inodes[id] == nil`, the one `flushOpen`'s
    namespace update already makes, rather than whether `FS.open` still holds
    this `openFile`. The two agree on every flush that can fail today: site 5
    removes the inode and the `openFile` in one critical section, inode numbers
    are never reused, and the entry `getOpen` can recreate for a gone inode
    (#42) is always empty, so its flush returns before it can fail. Resetting
    `of.dirty` and `of.pendingTrim` along with the release is mine too; it
    keeps another flush of the same `openFile` from uploading and charging it
    again. The alternatives: leaving the leak, which is permanent and ratchets
    as §2 describes; checking before the flush starts, in `flushAll` or at the
    top of `flushOpen`, which misses a removal made during the flush; and
    checking on every return rather than only on failure, which gains nothing,
    because a successful flush already releases everything when it replaces the
    map. The cost is one more acquisition of `FS.mu` on a failed flush, made
    while holding `openFile.mu`, which can wait behind a commit that holds
    `FS.mu` (#43), as the namespace update already can.

Assumptions 19–21 come from the second 2026-09-24 revision, which documents
what the review before the wiring found. Assumption 19 records a decision of
the repository owner's, not a judgement call of mine.

19. **The overshoot above the limit is documented, not bounded.** §6. `k` is
    limited only by how many calls the server runs at once, 64 per connection
    with connections uncapped, so the dirty pool can exceed `-max-dirty` by up
    to 128 MiB per connection at the defaults, and by no fixed amount in total.
    Keeping that is the repository owner's decision, made on 2026-09-24 when
    the review before the wiring raised it. The reason given is that the
    server is loopback-only: `-listen` defaults to `127.0.0.1:20490`
    (`cmd/strata/main.go:46`), the README documents it as binding to loopback,
    and so every client is a process on the same host, inside the trust
    boundary `sunrpc` already assumes when it believes `AUTH_SYS`
    (`internal/sunrpc/rpc.go:50-53`). An operator who points `-listen`
    elsewhere leaves that boundary for authentication as much as for this.
    Two alternatives were weighed and not taken. **Reservation at
    admission**: charge a call's `W` when `await` admits it and settle the
    difference once it has buffered, so that calls admitted but not yet
    charged count against the limit. It would change `await`, whose signature
    §4 pins, and every way out of `Write` would have to settle a reservation.
    **A connection cap** in `sunrpc`: it bounds `k` directly, but it refuses
    clients at the RPC layer, whatever they are doing, to protect one memory
    pool.
20. **A panic part-way through an accounting site is not accounted for.** §2.
    The five sites' rules assume each site runs to completion. Two can be cut
    short by a panic in a store or cache call that `dispatch` recovers
    (ADR 0002 §6). Site 1 charges once, after `bufferWrite`'s loop, so a panic
    in a fetch after an earlier index was stored leaves that buffer uncharged:
    `of.bytes` falls below the sum of `cap` over `of.dirty`, and `DirtyBytes()`
    under-counts, until the file's next successful flush replaces the map or
    its removal discards the `openFile`. Site 4 checks for a gone inode only on
    `flushOpen`'s two failed returns, so a panic in a fetch or an upload after
    a `pendingTrim` entry was materialised skips the check, and if the inode
    had already gone, that entry's charge stays on an `openFile` nothing will
    flush again. Each occurrence is small: at most one call's `W`,
    under-counted for a while, or about a chunk per pending trim, stranded.
    Each also needs a store or cache call that panics, which already fails the
    call it runs in. I accepted both rather than extend the rules with a
    deferred charge at site 1 and a deferred check at site 4. The budget
    settles a drain that panics (§4); the sites make no such promise, and if
    store calls are ever expected to panic, those are the two places to
    change.
21. **Site 2 leaves `pendingTrim` alone.** §2. Site 4 resets `pendingTrim`
    along with `dirty` when it finds the inode gone (Assumption 18); site 2,
    written before that refinement, resets `dirty` only. The difference costs
    no charge. A flush that listed the `openFile` before the removal and has
    yet to run materialises the trims, reading each original chunk and
    charging the copy, uploads the shortened chunks, which nothing references,
    and releases the charge when it replaces the map, or through site 4's
    check if it fails. So the cost is that I/O, for a removed file with a
    pending trim, and only while such a flush is still to run. I kept site 2's
    rule as it is.

## Consequences

- A client copying a large file onto a slow bucket now goes as fast as the
  bucket, instead of as fast as memory allows and then dying.
- One failing commit fails every write that is waiting on it, at once. That is
  deliberate: they would all fail in turn anyway, and failing them together
  gives the client a prompt, honest answer.
- `FS.Run` remains optional. Backpressure works without it because the stalled
  writer drains; the ticker is an optimisation, not a dependency.
- **A `WRITE` parked at shutdown is answered `NFS3ERR_IO`.** Shutdown cancels
  the server's `ctx`, so every parked call returns `ctx.Err()`, and the
  drainer's drain, cut short by the same cancellation, is abandoned and returns
  it too (§4); `statusOf` maps it to `NFS3ERR_IO` (§5). If that reply reaches
  the socket before the process exits, the application sees an I/O error for a
  write the client would otherwise have retransmitted to the restarted server.
  Withholding the reply would avoid that, but a handler cannot ask for no
  reply: its only outcomes are a reply body or an error, which becomes
  `GARBAGE_ARGS` or `SYSTEM_ERR` (`internal/sunrpc/rpc.go:64-66`, `:290-296`).
  Left open under *What this does not decide*.
- **Every stable write runs a full-mount `Sync`** (#44). `syncFileAndNamespace`
  flushes the file and then calls `Sync`, which flushes every open file and
  commits the namespace (`internal/blobfs/fs.go:1185-1190`). So a stable write
  can pay a drain's full cost with the budget nowhere near its limit, and one
  file's flush error, such as a failed upload of another file's chunk, fails a
  different file's stable write. That predates this ADR; it matters here because
  stable writes are among the draining paths (*Context*).

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
- **How `vfs.Pinner` interacts with the budget.** `Pinner`
  (`internal/vfs/vfs.go:308-328`) is declared and not implemented, and
  `docs/nfsv4-assessment.md:449-450` says `unlink` and `Rename`'s victim path
  "must stop dropping inodes unconditionally" once it is. It contradicts the
  rationale of sites 2 and 5, "nothing will ever flush this buffer", not their
  mechanism: site 5 is worded as a release immediately before every removal
  from `FS.open` so that an implementation which delays the removal until the
  last unpin carries the release with it. Whoever implements `Pinner` must keep
  a pinned, unlinked file's charge; keep that file flushable, since `flushOpen`
  records uploaded chunks only for an inode still in the table
  (`internal/blobfs/fs.go:1338-1348`); and keep site 2 from firing for a pinned
  file. A test that fails once `*blobfs.FS` satisfies `vfs.Pinner`, pointing
  here, would turn this note into a tripwire.
- **A no-reply path for `WRITE`s cut short by shutdown** (*Consequences*). What
  would settle it is `sunrpc` gaining a way for a handler to decline to reply,
  and a decision about which errors should use it.
- **`wcc_data` pre-operation attributes captured before a stall.** The `WRITE`
  handler reads them (`internal/nfs/nfs3.go:282`) before it calls `Write`, so a
  stall makes them as old as the stall, and they can predate changes other
  clients made in the meantime. Capturing them after the wait needs them to come
  from inside `Write`; this ADR does not decide how.
- **Chunk-cache accounting** (#47; Assumption 2). The cache charges an entry by
  `len` while holding a flushed buffer at its full capacity. Caching a copy
  would fix it, and would also stop the cache holding buffers that a write can
  still mutate in place after a flush fails partway, changing a cached chunk's
  bytes (#46). ADR 0004 decides both.
- **Stale reads and resurrected truncated bytes** (#41, #40). `Read` copies the
  chunk list before the dirty buffers (`internal/blobfs/fs.go:988`,
  `:1004-1011`), so a flush in between can make it return older contents than
  an acknowledged write (#41). A write into a chunk with a pending trim starts
  from the untrimmed chunk that `n.Chunks` still names (`fs.go:1144`), so
  truncated bytes can come back (#40). Backpressure neither causes nor fixes
  either one, though its drains are flushes and so add to #41's windows.
- **Enforcing the advertised maximum file size** (#55). `FSINFO` advertises a
  `maxfilesize` of 2^62 (`internal/nfs/nfs3.go:564`), and nothing enforces it.
  One `SETATTR` to a huge size, or one `WRITE` at a huge offset, makes
  `truncate`, or the next flush's namespace update, append a hole to the file's
  chunk list for every chunk index up to it, while holding `FS.mu`. That list is
  namespace memory, which the budget neither counts nor bounds: it bounds buffer
  capacity in the dirty maps (§6). #55 tracks the fix.

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

The entries below were added by the 2026-09-23 revision. Each was fetched on
2026-09-23 through a tool that summarises what it fetches; the quotes are what
it returned when asked for verbatim text.

- Go standard library, `context.AfterFunc`.
  <https://pkg.go.dev/context#AfterFunc> Cited in §4 for waking a parked call
  on cancellation. Taken from it: "AfterFunc arranges to call f in its own
  goroutine after ctx is canceled. If ctx is already canceled, AfterFunc calls f
  immediately in its own goroutine"; the returned stop function "does not wait
  for f to complete before returning"; and that `AfterFunc` was added in Go
  1.21, which `go.mod`'s `go 1.24` admits. From the page's "AfterFunc (Cond)"
  example, `ExampleAfterFunc_cond` in
  <https://go.dev/src/context/example_test.go>: the callback acquires `cond.L`
  before `cond.Broadcast()` "to be sure that the Broadcast below won't occur
  before the call to Wait, which would result in a missed signal (and
  deadlock)"; the stop function is deferred; and the waiter loops, checking
  `ctx.Err()` after each `cond.Wait()`, because that Wait "may unblock due to
  some other goroutine's context being canceled". **Honesty note:** the tool
  declined to reproduce the example whole, so those are short quotes it
  returned line by line; I did not read the example end to end.
- Go standard library, `sync.Cond.Wait`. <https://pkg.go.dev/sync#Cond.Wait>
  Cited in §4 for why checking `ctx` only at the top of the loop is not enough:
  "Wait atomically unlocks c.L and suspends execution of the calling goroutine.
  After later resuming execution, Wait locks c.L before returning. Unlike in
  other systems, Wait cannot return unless awoken by Cond.Broadcast or
  Cond.Signal."
- Go standard library, `time.Timer.Stop`, in
  <https://go.dev/src/time/sleep.go>. Cited in §7 for why the Warn callback
  re-checks its call under the budget's lock: "For a func-based timer created
  with AfterFunc(d, f), if t.Stop returns false, then the timer has already
  expired and the function f has been started in its own goroutine; Stop does
  not wait for f to complete before returning."

The two entries below were added by the 2026-09-24 revision and fetched while it
ran, through the same tool; they are quoted on the same terms.

- Go standard library, `runtime.Goexit`. <https://pkg.go.dev/runtime#Goexit>
  Cited in §4 and Assumption 11 for why the settle cannot rely on `recover`:
  "Goexit runs all deferred calls before terminating the goroutine. Because
  Goexit is not a panic, any recover calls in those deferred functions will
  return nil."
- Go standard library, the built-in `recover`.
  <https://pkg.go.dev/builtin#recover> Cited in §4 for what calling it during a
  panic does: "Executing a call to recover inside a deferred function (but not
  any function called by it) stops the panicking sequence by restoring normal
  execution and retrieves the error value passed to the call of panic."
  **Honesty note:** this is the `builtin` package's doc comment, not the
  language specification. My fetch of the specification's *Handling panics*
  section came back truncated, so I cite the doc comment in its place.
