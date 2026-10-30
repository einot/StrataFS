# 0023. A commit seals the namespace under the lock and runs its store calls outside it

**Status:** Accepted
**Date:** 2026-10-30
**Issue:** #43

## Context

**The owner's decisions.** Two, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0022 were, and its design is the architect's (Assumption 1).
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." This change is not breaking (Assumption 19),
   so nothing here relies on it.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `d75d14a`, the commit this ADR's branch was cut from, which
is `origin/main` with ADR 0022 (#5) merged (Assumption 2). Code is cited by
symbol.

- `Sync`, in `internal/blobfs/commit.go`, runs `flushAll` holding no lock;
  takes `FS.commitGate` for writing and runs `settleAll`; takes `FS.mu` for
  writing; returns nil if `f.dirty` is false; and calls `commitLocked`. Its
  two defers release `FS.mu` and then the gate when it returns, so the gate
  and `FS.mu` are held from the end of the settle pass until `Sync` returns.
- `commitLocked` refuses once the mode is `mountDiverged`; refuses while any
  `openFile` is unsettled (ADR 0012 §4); if `rootKnowledge` is `InDoubt`,
  calls `resolveRoot`, whose `GetWithETag` of `rootKey` is a store call
  (ADR 0022 §3); encodes the namespace with `encodeSnapshot`; picks the key;
  `Put`s the snapshot; marshals the root pointer; and calls `PutIfMatch` of
  `rootKey` against `f.rootETag`. On its outcomes it makes more store calls:
  `resolveRoot`'s read after any write whose reply did not establish the
  ETag (ADR 0022 §4); ADR 0021's reclaim, a `Get` of `rootKey` and a `Delete`
  of the attempt's key, after a 501 without the opt-in; and the opted-in
  fallback's `Put` of `rootKey` and `Head` of it. Its success path sets
  `f.epoch`, `f.rootETag`, `f.rootSnapshot`, `f.dirty = false`,
  `f.lastCommit`, logs `"committed"` and calls `schedulePrune(key)`. Every
  one of those store calls is made holding the commit gate and `FS.mu` for
  writing. `initEmpty` calls `commitLocked` holding nothing, before the FS is
  shared.
- `FS.mu`, in `internal/blobfs/fs.go`, "guards the namespace: the inode
  table, the allocator, the epoch and the dirty flags", and its doc comment
  gives the lock order: the commit gate, then `openFile.mu`, then `mu`.
  Every reader of the namespace takes it for reading: `GetAttr`, `Access`,
  `Lookup`, `ReadLink`, `FSStat`, `ReadDir`, `Root`, `Stats`, `Read`'s step 1
  and `readBuffered`'s step 2 (ADR 0005 §2), `Write`'s and `truncate`'s
  checks, `commitFile`, `Commit`'s resolve, `bufferWrite`'s copy of the chunk
  list, `flushAll`'s and `settleAll`'s listings, `dropIfGone` and `New`'s
  scan of file sizes. Every change takes it for writing: `SetAttr`,
  `createEntry`, `unlink`, `Rename`, `bufferWrite`'s tail block, which sets
  the size at the end of every `Write`, `truncate`, `flushOpen`'s repoint of
  the chunk list and its early-return clearing of the unsettled mark,
  `getOpen`, and `schedulePrune`'s goroutine when its pass ends. Each of
  them waits while a commit holds `FS.mu` across its store calls.
- `FS.commitGate` is a `sync.RWMutex` that `truncate` takes for reading from
  after its checks until it returns, and `Sync` takes for writing from the
  end of its first pass until it returns, its commit phase (ADR 0012 §2). A
  `sync.RWMutex` excludes new readers once a writer waits, so every size
  change waits for a commit phase whole, its store calls included
  (ADR 0012 §7).
- `f.dirty = true` is written at eight sites, each holding `FS.mu` for
  writing: `SetAttr`, `allocInode`, `createEntry`, `unlink`, `Rename`,
  `bufferWrite`'s tail block, `truncate` and `flushOpen`'s repoint. `Sync` is
  its only reader outside tests; ADR 0011 §7 lets a test set it on a
  read-only FS, and ADR 0022 §8 lets a test read it holding `FS.mu` for
  reading.
- `FS.rootKnowledge` is an `fsm.Machine` under `FS.mu`, with `rootSnapshot`
  and `doubtKey` beside it, read and fired only in `commitLocked` and
  `resolveRoot` (ADR 0022 §2). `FS.mode` is an `fsm.Atomic`, fired only by
  `resolveRoot` on `probeTheirs` (ADR 0022 §2; ADR 0014 §7).
- `docs/tla/CommitProtocol.tla` folds each store call of a commit with the
  work around it into one action, on the ground that `FS.mu` is held across
  `commitLocked` (its R4, R13 and R14; ADR 0013 §4, *Store calls made holding
  a lock*), and `trace.tsv`'s lock column records "commitGate (write) and
  FS.mu (write) from Sync" for every CommitProtocol row that names
  `commitLocked`.

**The defect.** #43, "Sync holds the namespace lock across object-store round
trips", as the session gave its substance (Assumption 2): every other
namespace operation waits for the commit's round trips: `GETATTR`, `LOOKUP`,
`READDIR`, `CREATE`, `REMOVE`, and the size update at the end of every
`WRITE`. Against real S3 that is tens to hundreds of milliseconds per commit,
paid by every client. It matters more now: #37 makes every `FILE_SYNC` and
`DATA_SYNC` write run a full `Sync`, so this happens once per stable write
(#44), and `docs/DESIGN.md` §5 and ADR 0003's backpressure both rely on
commits being cheap enough to run often. The issue's sketch: the snapshot is
immutable once encoded, and the swap is a compare-and-swap, so encode under
the lock, upload outside it, and retake the lock to swap and clear `f.dirty`
only if nothing changed in between, or loop. What must be pinned is what
"nothing changed" means and how it interacts with divergence detection. Its
done-when: namespace operations do not wait on object-store latency during a
commit, shown by a test with a store whose `Put` blocks until the test
releases it; and divergence detection and "a later commit supersedes an
earlier one" are unchanged, and stated in `docs/DESIGN.md`.

**What the ADRs say.** ADR 0012 *What this does not decide* anticipated this
change: "#43. Once it is fixed, a `Sync` can release the gate as soon as the
snapshot is encoded." ADR 0021 Assumption 25 kept the reclaim under the locks
"for this PR", listing it among the calls #43's fix moves. ADR 0022 §5 and
Assumption 22 did the same for the probe's read, and said why it was not
moved alone: "Reading outside the lock would need the verdict applied in a
later hold against fields that could have moved, which is the shape
ADR 0018 §5 warns of." ADR 0013 §4 says a store call made holding a lock is
an action, and that the work between calls joins the call before or after it
because it touches only state `FS.mu` protects; its R4 enables `Mutate` only
at `Ready` for the same reason. ADR 0014 §4 keeps every machine under the
lock that guards the field it replaced, and ADR 0018 §5 asks that a verdict
and the `Fire` it guards be made in one exclusive hold. ADR 0018 §4's E3
exempts `Sync`'s phases and the commit gate from the state rule. ADRs 0002,
0003, 0005, 0006, 0007 and 0012 each cite #43 for a wait behind a commit
holding `FS.mu`; §9 says, for each, what this ADR changes.

**What this constrains.** `Sync`, `initEmpty`, `commitLocked`, `resolveRoot`
and `schedulePrune` in `internal/blobfs/commit.go`; `FS`'s fields and lock
order, and the eight sites that set `f.dirty`, in `internal/blobfs/fs.go`;
`docs/tla/CommitProtocol.tla`, its configurations and the tables beside it;
`docs/vocabulary.md`; `docs/DESIGN.md` §5 and §16; `docs/adr/exemptions.md`'s
E3; and, by cross-reference only, the ADRs §9 and Assumption 24 list. Nothing
on the wire, no flag, no object format, no `store.Store` method and no
`vfs.FS` method changes.

## Decision

### 1. The rule

A commit holds `FS.mu` only to **seal** the namespace, which is to encode it
and fix the attempt, and to **apply** an outcome. Every store call a commit
makes, the snapshot `Put`, the root-pointer swap, the probe's read, ADR 0021's
reclaim and the opted-in fallback's `Put` and `Head`, is made holding neither
`FS.mu` nor the commit gate. The gate is held from the end of `Sync`'s first
pass until the seal, and no longer. Commits are serialised by a lock of their
own, `FS.commitMu`, so that one commit of a mount is in flight at a time. A
commit's success clears `FS.dirty` only if no change was made to the
namespace since the seal; a change made while a commit is in flight leaves
the namespace dirty, and the next commit carries it. A commit that fails
leaves `FS.dirty` as it was, which is true.

**Why that closes #43.** Every operation that waits for `FS.mu` now waits for
two bounded holds of it, the seal and the apply, neither of which makes a
store call; and every size change, which waits for the gate, waits for the
settle pass and the seal only. A store that takes a second to answer a `Put`
holds up nothing but the commit itself and a `Sync` queued behind it. The
done-when's second half holds because nothing the swap decides reads the
namespace: the snapshot is fixed at the seal, the swap's condition is the
ETag captured at the seal, and the verdicts of ADR 0022 compare keys that
only commits write, which this lock serialises (§5). "A later commit
supersedes an earlier one" is §3's guard: a `Sync` queued behind a commit in
flight finds the namespace dirty exactly when a change was made since that
commit's seal, and commits it then.

### 2. The pipeline

**`Sync`**, pinned step by step. The locks named are the only ones it holds
at each step.

1. `flushAll`, holding no lock, as today (ADR 0012 §2).
2. Take `FS.commitMu`, holding no lock, and hold it until `Sync` returns
   (§4).
3. **The top of the commit**, holding neither the gate nor `FS.mu` across any
   store call, in this order:
   - read `f.dirty` and `f.rootKnowledge.State()` holding `FS.mu` for
     reading, and release it; if the namespace is clean, return nil
     (Assumption 7);
   - if `f.mode.State()` is `mountDiverged`, return the error whose text
     begins `filesystem diverged`, as `commitLocked` returned at `d75d14a`,
     making no store call, not even a read (ADR 0014 §11's M7);
   - if the state read was `rootInDoubt`, call `resolveRoot` (§5), and act
     on its verdict as `commitLocked`'s top did at `d75d14a` (ADR 0022 §4):
     `probeFailed` returns the error whose text contains `in doubt`;
     `probeTheirs` returns the conflict error; `probeOurs` and
     `probeUnchanged` continue.
4. Take the commit gate for writing, holding `FS.commitMu` alone, and run
   `settleAll`; an error releases the gate and returns, committing nothing
   (ADR 0012 §2).
5. **The seal**, `f.seal()`, which takes `FS.mu` for writing itself (§2,
   *The functions*). Then release the gate, before any store call, whether
   the seal succeeded or not. The commit phase of ADR 0012 §2 is since this
   ADR the span from the end of the first pass to the end of the seal
   (Assumption 4).
6. **The flight**, `f.commitSealed(ctx, s)`: the store calls, holding
   `FS.commitMu` and nothing else, and the holds of `FS.mu` for writing in
   which it fires the machines and applies the outcome.
7. Return what `commitSealed` returned; the defer releases `FS.commitMu`.

**`initEmpty`** sets its fields as today, then calls `f.seal()` and
`f.commitSealed(ctx, s)` holding nothing, before the FS is shared; it takes
neither `FS.commitMu` nor the gate, since nothing runs beside it
(Assumption 26). ADR 0022 §4's "From `initEmpty`" paragraph stands.

**The functions**, pinned in `internal/blobfs/commit.go`. `commitLocked` is
removed; its paths are these three functions', unchanged in what they decide
(Assumption 11).

```go
// sealedCommit is one commit attempt as the seal fixed it: the snapshot of
// the namespace as it stood under FS.mu, the key and the root pointer that
// name it, and the two values the flight needs from that hold: the ETag the
// swap conditions on, and the change count the guard compares against
// (ADR 0023 §2, §3).
type sealedCommit struct {
    epoch    uint64 // FS.epoch + 1 at the seal
    key      string // the snapshot key, with its nonce
    body     []byte // the encoded snapshot
    rpBody   []byte // the encoded root pointer naming key
    rootETag string // FS.rootETag at the seal: the swap's condition
    changes  uint64 // FS.changes at the seal: the guard's reference (§3)
}

// seal takes mu for writing and fixes one commit attempt: it refuses while
// any file is unsettled (ADR 0012 §4), encodes the namespace as it stands,
// picks the attempt's key, and copies rootETag and changes out of the hold.
// It makes no store call. Caller must hold commitMu and the commit gate for
// writing, or run before the FS is shared, and must not hold mu.
//
// TLA+: docs/tla/CommitProtocol.tla PutSnapshot
func (f *FS) seal() (*sealedCommit, error)

// commitSealed is a commit's flight: its snapshot Put, its swap of the root
// pointer, and on the swap's outcome the probe, the reclaim or the fallback,
// every one made holding no mu and no commit gate (ADR 0023 §1, §2); then,
// holding mu for writing, the Fire the outcome calls for and applyLocked on
// success. Caller must hold commitMu, or run before the FS is shared, and
// must hold neither mu nor the gate.
//
// TLA+: docs/tla/CommitProtocol.tla PutSnapshot, SwapOK, SwapReplyLost, SwapFailed, SwapConflict, SwapNoETag, SwapUnsupported, FallbackPut, FallbackHead, SwapUnknown, ProbeOurs, ProbeUnchanged, ProbeTheirs, ProbeFailed
func (f *FS) commitSealed(ctx context.Context, s *sealedCommit) error

// applyLocked is the success path of a commit whose root pointer now names
// s.key with the ETag etag: it advances the epoch, records the ETag and the
// key, clears dirty only if nothing changed since the seal (ADR 0023 §3),
// sets lastCommit, logs "committed" and schedules a prune pass. Caller must
// hold mu for writing.
//
// TLA+: docs/tla/CommitProtocol.tla SwapOK, FallbackHead, ProbeOurs
func (f *FS) applyLocked(s *sealedCommit, etag string)
```

And in `internal/blobfs/fs.go`:

```go
// markDirty records a change to the namespace: it sets dirty and advances
// changes in one hold, so that a commit's success can tell whether anything
// changed since its seal (ADR 0023 §3). Caller must hold mu for writing.
//
// TLA+: docs/tla/CommitProtocol.tla Mutate
func (f *FS) markDirty()
```

`FS` gains two fields:

```go
// commitMu serialises commits: Sync holds it from after its first pass until
// it returns, so one commit of this mount is in flight at a time, and a Sync
// queued behind it finds the namespace dirty exactly when a change was made
// since that commit's seal. Taken holding no other lock; initEmpty's commit
// does not take it, since nothing runs beside it (ADR 0023 §2, §4).
commitMu sync.Mutex

// changes counts the changes made to the namespace: every write of dirty =
// true advances it, through markDirty, in the same hold of mu. A commit
// copies it at its seal and, on success, clears dirty only if it is
// unchanged since (ADR 0023 §3). Under mu, like dirty.
changes uint64
```

**What `seal` checks, and what moved.** `commitLocked`'s diverged refusal is
since this ADR `Sync`'s, at step 3, so that a diverged mount makes no store
call at all, the probe's read included (Assumption 6). Its unsettled-file
refusal stays at the seal, under the gate after the settle pass, where its
argument lives (ADR 0012 §4); from `Sync` it is unreachable, as before, and
from `initEmpty` `FS.open` is empty. The encode, the nonce and the key are
as at `d75d14a`; `rp.WrittenNS` and `snap.CreatedNS` are taken at the seal.
`rootETag` is copied at the seal and the swap conditions on the copy
(Assumption 9): between the seal and the swap nothing writes `FS.rootETag`,
since only a commit's apply and a probe's verdict write it and `commitMu`
serialises both.

**What `commitSealed` does**, path by path, is ADR 0022 §4 with the holds
named, and nothing else moves:

- The snapshot `Put`, then the swap, each holding no `FS.mu` and no gate. A
  `Put` that fails returns `write snapshot: %w` with nothing more done.
- A swap whose reply gave the ETag: take `FS.mu` for writing;
  `Fire(rootSwapOK)`, logged if it fails, with success following anyway
  (ADR 0022 §4); `applyLocked(s, newETag)`; release; return nil.
- A swap whose reply did not establish the ETag, in every case ADR 0022 §4
  lists, a 412 or 409, an untyped or transport error, a reply with no ETag,
  the reclaim's non-positive cases and the fallback's failures: first the
  store calls that case makes, holding no lock, which are the reclaim's `Get`
  of `rootKey` and `Delete` of `s.key` (ADR 0021 §2) and the fallback's `Put`
  of `rootKey` and `Head` of it (ADR 0021 §2; ADR 0022 §4); then take `FS.mu`
  for writing, set `f.doubtKey = s.key` and `Fire(rootSwapUnknown)`, and
  release. A `Fire` that fails is as ADR 0022 §4's "Doubt" paragraph pins it,
  unreachable in this build (Assumption 27). Then `resolveRoot` (§5), and on
  its verdict: `probeOurs` takes `FS.mu` for writing again, calls
  `applyLocked(s, f.rootETag)`, the ETag the read gave, releases and returns
  nil; `probeTheirs` returns the conflict error; `probeUnchanged` and
  `probeFailed` return the errors ADR 0022 §4 pins for that case.
- The errors returned, their texts and `errors.Is` behaviour, the records
  logged and their attributes are ADR 0022 §3 and §4's, ADR 0021 §2's and
  `d75d14a`'s, unchanged (Assumption 18).

### 3. "Nothing changed"

**The change count.** `FS.changes` advances by one at every change that sets
`FS.dirty`, in the same hold of `FS.mu` for writing, through `markDirty`. The
eight sites that write `f.dirty = true` at `d75d14a`, `SetAttr`, `allocInode`,
`createEntry`, `unlink`, `Rename`, `bufferWrite`'s tail block, `truncate` and
`flushOpen`'s repoint, each call `f.markDirty()` instead, and no non-test
code writes `f.dirty = true` otherwise (Assumption 10). A site that is
reached inside another's hold, as `allocInode` is inside `createEntry`'s,
advances the count twice, which changes nothing: the guard asks only whether
it moved. ADR 0011 §7's seam, a test setting `FS.dirty` on a read-only FS,
stands and advances nothing; such a commit fails at its `Put`, and the flag
stays set.

**The guard.** `seal` copies `f.changes` into `s.changes`. `applyLocked` sets
`f.dirty = f.changes != s.changes`. That is the whole of "nothing changed"
(Assumption 3): a change made after the seal is not in the snapshot the root
pointer now names, so the namespace is dirty; one made before it is, so if
none followed, it is clean. `FS.dirty` keeps its meaning throughout: the
namespace in memory holds changes no acknowledged commit holds. It stays true
through the flight, and a commit that fails writes it no more than
`commitLocked` did, which is not at all.

**What a change during the flight does to the commit in flight.** Nothing.
The commit commits what it sealed: the snapshot is bytes already encoded, the
swap's condition is `s.rootETag`, and no verdict reads the namespace. The
change is acknowledged to its caller as every change is, before any commit
holds it (ADR 0012 *What this does not decide*, unchanged here), and it is in
the next commit, which the committer's next tick, the next client `COMMIT` or
the next stable write makes. A `FILE_SYNC` or `DATA_SYNC` write that lands
during a flight is not acknowledged until its own `Sync` has committed it:
its `Sync` queues on `FS.commitMu` (§4), and when it runs, the write's
`markDirty` calls, from `bufferWrite` and from `flushOpen`, have left the
namespace dirty, so it commits.

**How `dirty`, `epoch` and `changes` move**, per outcome:

| Outcome | `epoch` | `dirty` | `changes` |
|---|---|---|---|
| the seal | unchanged | unchanged (true) | copied into `s.changes` |
| a change during the flight (`markDirty`) | unchanged | true | +1 |
| success: swap OK, fallback `Head` OK, or `probeOurs` at the failure site | `s.epoch` | `changes != s.changes` | unchanged |
| failure: `write snapshot`, `probeUnchanged`, `probeFailed`, `probeTheirs`, the reclaim's refusal | unchanged | unchanged (true) | unchanged |
| `probeOurs` at the top of a commit (adopt only) | the attempt's | unchanged (true) | unchanged |

**A concurrent second commit** cannot happen: `FS.commitMu` serialises
commits (§4). A second `Sync` that arrives while one is in flight runs its
first pass, which overlaps as it does today, then waits for the lock, then
reads `f.dirty` at step 3. If the commit in flight sealed everything the
second `Sync`'s caller changed and nothing changed since, `dirty` is false
and the second `Sync` returns nil, which is right: what its caller wanted
durable is in the snapshot the root pointer names. Otherwise it commits what
the first did not seal. That is "a later commit supersedes an earlier one",
as the issue's done-when asks: the second commit's snapshot holds everything
the first's held and the changes since.

### 4. Serialisation, and the lock order

**`FS.commitMu`** is a `sync.Mutex`. `Sync` takes it holding no lock, after
its first pass, and holds it until it returns; nothing else takes it
(Assumption 5). It exists because the gate no longer serialises commits once
it is released at the seal (§2), and because a second commit in flight at the
same epoch, against the same ETag, would lose its swap to the first and read
the refusal as another writer's root pointer, diverging the mount alone: with
`commitMu`, the second commit seals after the first has applied, at the next
epoch, against the ETag the first's success recorded.

**The lock order** is since this ADR `FS.commitMu`, then the commit gate,
then `openFile.mu`, then `FS.mu`; the budget's mutex, `knownMu` and the chunk
cache's mutex stay leaves (ADR 0003 §3; ADR 0012 §5; ADR 0013 §4; ADR 0014
§4). The `FS.mu` doc comment gives the new order, and `commitGate`'s doc
comment says that `Sync` holds the gate from the end of its first pass until
the seal.

- Nothing takes `commitMu` while holding any other lock: `Sync` is its only
  taker, and every caller of `Sync`, the committer, `Commit`,
  `syncFileAndNamespace`, `budget.await`'s drain and the final commit, calls
  it holding nothing (ADR 0003 §4; ADR 0012 §2).
- A holder of `commitMu` waits for the gate, which a `truncate` holds for
  reading while it waits for its file's `openFile.mu` and for `FS.mu`,
  neither of whose holders waits for `commitMu`; for `FS.mu`, whose holders
  wait for leaves only; and for the store. So no cycle.
- `Sync` never calls `truncate`, and `truncate` never reaches `Sync`,
  `flushAll` or `flushOpen`, as ADR 0012 §5 has it.

**What waits for what**, before and after this ADR:

| Operation | At `d75d14a`, during a commit | Since this ADR |
|---|---|---|
| `GETATTR`, `LOOKUP`, `READDIR`, `ACCESS`, `READLINK`, `FSSTAT`, `READ`'s steps 1 and 2, the attribute reads NFS handlers make for `wcc_data` | the whole commit, store calls included | the seal and the apply: two holds of `FS.mu` with no store call in them |
| `CREATE`, `MKDIR`, `SYMLINK`, `REMOVE`, `RMDIR`, `RENAME`, `SETATTR` without a size, `WRITE`'s size update, a flush's repoint | the same | the same two holds |
| `SETATTR` with a size, a `CREATE` with a size (a truncate) | the settle pass and the whole commit, through the gate | the settle pass and the seal, through the gate |
| a second `Sync`: a client `COMMIT`, a stable write's sync, a drain, the committer's tick | the whole commit, through the gate and `FS.mu` | the whole commit, through `commitMu`; its first pass overlaps as before |
| a read-only mount's `Sync` | nothing to commit | unchanged |

A stable write's own `Sync` still waits for a commit in flight, and so does
the drain of ADR 0003 §4; what no longer waits is the write's buffering and
its size update. Batching stable writes is #44's, not this ADR's
(Assumption 20).

### 5. Root-pointer knowledge and the mode, off the lock

**What may be read or written where.** `FS.rootKnowledge`, `FS.doubtKey`,
`FS.rootSnapshot`, `FS.rootETag` and `FS.epoch` stay under `FS.mu`: every
read and every write of them is made holding `FS.mu` for writing, or
before the FS is shared, as ADR 0022 §2 pins. No store call reads them: the
swap conditions on `s.rootETag`, a copy made at the seal (§2); the probe's
read takes `rootKey` alone; and `classifyRoot` is called in the hold that
applies the verdict, with `f.rootSnapshot` and `f.doubtKey` read there. The
mode is an `fsm.Atomic` (ADR 0014 §7); its `Fire` of `mountDiverge` is made
by `resolveRoot` in its hold of `FS.mu` for writing, as before, and its
`State` is read without a lock by `mutable()` and now by `Sync`'s step 3.
So ADR 0014 §4's discipline holds: `Machine.Fire` is made only under the
lock its doc comment names, held for writing; no new lock is added; `Fire`
waits for nothing.

**`resolveRoot`** keeps ADR 0022 §3's signature,
`func (f *FS) resolveRoot(ctx context.Context) probeVerdict`, and its verdicts
and records. What changes is its holds (Assumption 8): it makes its one read,
`GetWithETag(ctx, rootKey)`, holding no lock; then takes `FS.mu` for writing;
then `classifyRoot(raw, etag, err, f.rootSnapshot, f.doubtKey)`, the `Fire`
its verdict calls for, and the fields it sets, all in that one hold, as
ADR 0022 §3 pins them; and releases before it returns. Its doc comment says
so: caller must hold `commitMu`, or run before the FS is shared, and must
hold neither `mu` nor the gate; `rootKnowledge` must be `InDoubt` when it is
called, and is still `InDoubt` in its hold, since only a commit's flight or a
probe moves it and `commitMu` serialises both.

**Why the verdict is applied against fields that have not moved.** ADR 0022
Assumption 22 declined to read outside the lock because the verdict would
then be "applied in a later hold against fields that could have moved". They
cannot. The fields the verdict reads and writes, `rootKnowledge`, `doubtKey`,
`rootSnapshot`, `rootETag` and `epoch`, are written by three things only: a
commit's apply, a probe's verdict, and `loadOrInit` before the FS is shared.
The first two run only inside `Sync`'s hold of `commitMu` or inside
`initEmpty`, which nothing runs beside. So between the read's return and the
verdict's hold the only writers of `FS.mu`-guarded state that can run are
changes, which write the inode table, `dirty` and `changes`, and prune's
`PruneEnd`, which writes `pruning`: none of them touches a field the verdict
reads or writes. ADR 0018 §5's "one exclusive hold" rule binds a
security-relevant transition with a policy's verdict; `rootKnowledge` is not
security-relevant and `Diverge` needs no policy (ADR 0022 §6), and the
verdict and the `Fire` are in one hold of `FS.mu` for writing all the same.
The store's answer is data read outside the hold, as a swap's reply is.

**The second hold at the failure site.** On `probeOurs` after a failed swap,
`resolveRoot`'s hold adopts the attempt, `epoch`, `rootETag`, `rootSnapshot`
and `doubtKey`, and fires `rootProbeOurs`; `commitSealed` then takes `FS.mu`
again for `applyLocked`, which sets the same `epoch` and `rootSnapshot`
again, the guard on `dirty`, `lastCommit`, the record and the prune pass.
Between the two holds only changes and `PruneEnd` can run (above); a change
there advances `changes`, and the guard sees it, which is the order it must
see (Assumption 25). A reader between the holds sees the adopted root with
`dirty` still true, which is what it sees after a top-of-commit adoption
too (ADR 0022 §4).

**A change racing the flight can neither adopt, overwrite nor lose a
commit.** A change writes the inode table, `dirty` and `changes`, and reads
nothing of the root. The swap applies only against `s.rootETag`, the ETag of
the root pointer the sealed namespace descends from, so it replaces no other
mount's commit (ADR 0020 §1): `NoLostAck`. It names `s.key`, whose snapshot
the `Put` before it stored, with every chunk flushed before the seal
(ADR 0012 §2): `RootSnapshotExists`, `ChunksBeforeRoot`. The verdicts compare
`bucketRoot.key` with `doubtKey` and `rootSnapshot`, which no change writes:
*ours* adopts only this mount's attempt, *unchanged* takes an ETag only
against the key the namespace descends from, and *theirs* needs a key the
mount neither wrote nor descends from, so a change can make none of them:
`NoSelfDivergence`. The sealed snapshot holds the namespace as of the seal;
the change is in the next one (§3): nothing is lost, which `DirtyUntilCommitted`
states and §10's configurations check with `Mutate` enabled throughout the
flight.

### 6. Prune and reclaim

- **`schedulePrune`** is called by `applyLocked`, in the hold of `FS.mu` for
  writing in which the success path runs, with `s.key`, on both success
  paths, the swap's and the failure-site adoption's (ADR 0019 §2; ADR 0022
  §5); the top-of-commit adoption schedules none, as before. `pruneSnapshots`
  runs in its goroutine holding nothing, and its final hold clears
  `FS.pruning`, as before (the module's R8 and R10).
- **ADR 0019 §1's rule stands.** Its step 2 rests on the swap being
  conditional on the ETag of the root pointer the mount last read or wrote,
  and on the attempt's epoch being that root's plus one. `s.rootETag` and
  `s.epoch` are copied at the seal from `FS.rootETag` and `FS.epoch`, which
  `loadOrInit`, the apply and the probe set together, as ADR 0019 §1 argues;
  and the pass begun for `s.key` is begun by the apply of the swap that named
  it, so the root pointer names `s.key` when the pass begins, and every later
  swap of this mount is sealed after that apply, at a higher epoch. The
  rule's guarantee on the fallback is a single writer's, as before.
- **ADR 0021's reclaim** makes its `Get` of `rootKey` and its `Delete` of
  `s.key` holding no lock, as its Assumption 25 said #43's fix would have it.
  It reads no field: `s.key` is the attempt's, and the guard that decides the
  `Delete` is a function of the body read (ADR 0021 §2, revised 2026-10-27).
  Its positive cases change neither the error returned nor any field, as
  before; its non-positive cases raise doubt and probe, as ADR 0022 §4 has
  them, with the doubt-raising `Fire` in the hold §2 names. The module's R12
  stands, since it rests on the 501 changing nothing and on the key's nonce,
  not on the lock.

### 7. Readers, and what clients see

A reader takes `FS.mu` for reading and sees one namespace state, as today.
During a flight that state may hold changes made since the seal, which the
in-flight snapshot lacks; but every state a reader sees between commits is
already ahead of the bucket in the same way, since changes are acknowledged
before any commit holds them (ADR 0012 *What this does not decide*), and a
reader during a commit at `d75d14a` simply waited and then saw the state the
commit left, which is this state with the commit's changes in it. No reader
sees a mixture: the seal is one hold, the apply is one hold, and neither
rewrites an inode. So for a client nothing changes but latency: `GETATTR`
and the attributes a handler reads before and after an operation for its
`wcc_data` describe the in-memory inode at the instant of the read, as they
did; `READDIR`'s cookies and verifier are computed under one read hold, as
they were; `Read` takes its one view under the file's lock and `FS.mu` and
fetches holding nothing (ADR 0005 §2), as it did. A `COMMIT` and a stable
`WRITE` are acknowledged only after their own `Sync` has committed what they
asked for (§3). What a client could observe is only that an operation issued
while a commit is in flight is answered before that commit's swap rather
than after it, which no NFS semantics forbids: the reply describes the
server's state at the time, and the operation's durability was never tied to
that commit (Assumption 17).

### 8. ADR 0014 and ADR 0018

- **The commit's phases are not a machine.** Sealed, in flight, swapped and
  applied are the progress of one `Sync` call through `seal` and
  `commitSealed`, pinned by the sequential code and by `commitMu`, which lets
  one commit run at a time. No field holds the phase, no other goroutine
  reads it, and no transition could be refused: each phase follows the one
  before it by control flow. ADR 0018 §4's E3 already exempts `Sync`'s
  phases and the commit gate from the state rule, with ADR 0014 §1's reason
  that the sequential code is the machine and the gate is a lock. This ADR
  restates E3 with that reason, which still holds, extending its code to
  `seal`, `commitSealed`, `applyLocked` and `FS.commitMu`, and the register's
  row gains this ADR (ADR 0018 §4; Assumption 22). No new exemption is
  granted.
- **`FS.changes` is a value, not a state** (ADR 0018 §2), as `rootETag` is:
  it names no legal next step.
- **No policy.** Nothing here decides what a caller may do. The guard is a
  comparison of two counts the server keeps; `Sync`'s clean return and
  diverged refusal are `commitLocked`'s checks moved, with ADR 0018 §5's
  reasoning for `Diverge` unchanged.
- **The machines.** `rootKnowledge` is fired only under `FS.mu` for writing,
  by `commitSealed` and `resolveRoot`, or before the FS is shared (§5); the
  mode only by `resolveRoot` under the same hold. ADR 0022 §6 stands: the
  machine is not security-relevant, and no transition asks a policy.

### 9. The ADRs that cite #43

Each of these cites #43 for something that waits behind a commit holding
`FS.mu`. For each, whether its statement changes, and what gains a
cross-reference `Revised` line (Assumption 24):

- **ADR 0002**, *Context* and Assumption 7: "Every non-idempotent procedure
  can wait on `FS.mu` while a commit holds it across its snapshot PUT and
  root-pointer swap." Changes: a procedure now waits for the seal and the
  apply, with no store call in either; the waits Assumption 7 lists lose
  the commit's store calls. The decision, the cache and its bounds are
  unchanged.
- **ADR 0003**, §4 ("`FS.Sync` notices cancellation in its store calls but
  not while it waits for `FS.mu`, or for an `openFile.mu`, that another
  commit or flush holds (#43), nor ... the commit gate") and Assumption 18
  (a failed flush's acquisition of `FS.mu` "can wait behind a commit that
  holds `FS.mu`"). Changes: the drain's `Sync` now waits for `FS.commitMu`
  behind a commit in flight, and `Sync` does not notice cancellation there
  either; the failed flush's hold waits for the seal and the apply only. The
  budget, the waiting rules and the failure contract are unchanged.
- **ADR 0005**, §6 ("a commit holding `FS.mu` can" hold up a `SetAttr`;
  "Step 2 can wait for `FS.mu` behind a commit that holds it across its
  store calls, while holding the file's `openFile.mu`, so a flush or a write
  of that file queues behind the `Read`") and *Consequences*. Changes: the
  wait is now for the seal or the apply; the single view and the three steps
  are unchanged.
- **ADR 0006**, *What this does not decide* (#42, #43 and #55: "A commit
  holding `FS.mu` across its store calls still holds up a truncate").
  Changes: a truncate waits for the settle pass and the seal, through the
  gate, and for `FS.mu` with no store call under it. The trim's rules are
  unchanged.
- **ADR 0007**, *Context* ("a commit holds the uncompressed snapshot in
  memory, holding `FS.mu` (#43)"), §4 ("a refused request still waits
  behind a commit that holds `FS.mu`") and *What this does not decide*.
  Changes: the encode still runs under `FS.mu`, holding the uncompressed
  snapshot, so that sentence stands; a refused request's handler waits for
  the seal and the apply, not the store calls. The limit and its checks are
  unchanged.
- **ADR 0012**, *Context*, §5 ("the commit's own store calls hold `FS.mu` as
  before (#43)"), §7 (a truncate "waits for the commit gate while a `Sync` is
  in its commit phase: for the settle pass's flushes, and for the commit's
  store calls") and *What this does not decide* ("Once it is fixed, a `Sync`
  can release the gate as soon as the snapshot is encoded"). Changes: §2's
  commit phase ends at the seal, which is what its *What this does not
  decide* anticipated; §5's sentence describes the code before this ADR;
  §7's truncate waits for the settle pass and the seal. Rules C and D, the
  gate, the mark and the settle pass are unchanged, and §9's tests stand.
- **ADR 0013**, §4 ("`commitLocked` holds `FS.mu` across its snapshot `Put`,
  its root-pointer swap and ... (#43)"), which grounds the module's R4. The
  rules of §4 are unchanged; the sentence describes the code before this
  ADR, and the module's R4, R5, R13 and R14 are rewritten (§10). Its FileData
  fold of `SyncCommit` is noted under *What this does not decide*.
- **ADR 0020** does not cite #43; the brief listed it, and a search of its
  text finds no citation. It names `commitLocked`'s success path, which is
  since this ADR `applyLocked`'s, unchanged in what it sets; it gains a
  cross-reference line for the name alone.
- **ADR 0021**, §2 and Assumption 25: the reclaim "stays under the locks" for
  that PR, moved "when #43's fix moves `commitLocked`'s store calls off the
  lock". Changes: moved now, as §6 says. The rule, the flag and the reclaim's
  guard are unchanged.
- **ADR 0022**, §3 ("holding the locks `commitLocked` holds"), §5 and
  Assumption 22. Changes: the read holds no lock and the verdict is applied
  in a hold of its own, as §5 argues; the rule, the machine, the verdicts and
  the records are unchanged, and its K-tests stand (§11).

### 10. The model

In `docs/tla/CommitProtocol.tla`, under a `Revised: 2026-10-30` line
(Assumptions 12 to 16):

- `Fixes` gains two members, and `AllFixes` lists both:
  - **`UnlockedCommit`**: the commit's store calls run holding no `FS.mu`
    and no commit gate, so a change can be made while a commit is in flight.
    With it, `Mutate` is enabled in flight; without it, only at `"Ready"`, as
    `FS.mu` was held across `commitLocked` at `d75d14a`.
  - **`ClearIfUnchanged`**: a commit's success clears the dirty flag only if
    nothing changed since the seal. Without it, success clears the flag
    unconditionally, as `commitLocked`'s success path did; without
    `UnlockedCommit` it is inert, since no change can fall in the flight.
- Two variables: **`changedSinceSeal[m]`**, in `MountVars`, whether a change
  was made since the seal of the commit in flight, which is Go's
  `FS.changes != s.changes`; and **`uncommitted[m]`**, a ghost in
  `GhostVars`, whether the mount holds a change that no acknowledged commit
  includes. `Init` and `Crash` give both `FALSE`.
- **`InFlight(m)`**: `"ChunksFirst" \in Fixes` and `pc[m]` in `{"Swap",
  "Probe", "FallbackHead"}`: after the seal, until the commit ends.
- **`Mutate(m)`** is enabled at `"Ready"` when `~nsDirty[m]`, as before, and
  with `UnlockedCommit` also when `InFlight(m)` and `~changedSinceSeal[m]`;
  it sets `nsDirty`, sets `changedSinceSeal` when in flight, and sets
  `uncommitted`.
- **`PutSnapshot(m)`** is the seal and the `Put` (R15): every branch resets
  `changedSinceSeal[m]` to `FALSE`.
- **`Committed(m, k, et)`** sets `nsDirty'` to `changedSinceSeal[m]` with
  `ClearIfUnchanged` and to `FALSE` without it, and `uncommitted'` to
  `changedSinceSeal[m]`.
- A new property, **`DirtyUntilCommitted`**: `\A m \in Mounts :
  uncommitted[m] => nsDirty[m]`. It holds in every configuration without
  `UnlockedCommit`, where `changedSinceSeal` is never set, so the
  configurations written before this ADR need not list it and, being
  append-only, do not (Assumption 13).
- R4, R5, R13 and R14 are rewritten; R15 (the seal folds into
  `PutSnapshot`) and R16 (the apply folds into the store call before it) are
  added; J21 to J24 record the judgement calls; the Scope and Leaves-out lists
  name `FS.commitMu` and the count.
- **Configurations.** Every existing row stands as it is (ADR 0013 §7).
  Four MustHold configurations carry every member the code has, the two new
  ones included, with every property, `DirtyUntilCommitted` included:
  `CommitProtocol_Unlocked`, one writer from an empty bucket with every swap
  fault, both snapshot faults, failed probes and a crash, at
  `CommitProtocol_ProbeFailed`'s bounds; `CommitProtocol_UnlockedProbeTwoWriters`,
  `CommitProtocol_ProbeTwoWriters` with the members, `full` tier;
  `CommitProtocol_UnlockedPruneProbe`, `CommitProtocol_PruneProbe` with the
  members; and `CommitProtocol_UnlockedUnconditional`, one opted-in writer on
  a backend with no conditional writes, without `FallbackOptIn` as J17 reads
  that (ADR 0021), so that the fallback's `Put` and `Head` and the probe
  after a lost fallback reply run with changes in flight. One MustFail
  configuration, `CommitProtocol_NoClearIfUnchanged`, on
  `DirtyUntilCommitted`: one writer, no fault, `UnlockedCommit` without
  `ClearIfUnchanged`. Its expected route: `Mutate`, `PutSnapshot [2,1]`,
  `Mutate` in flight, `SwapOK`, after which `nsDirty` is `FALSE` and
  `uncommitted` `TRUE`. No MustFail configuration removes `UnlockedCommit`,
  since no property fails without it; the row that lacks it is the existing
  MustHold row of the same bounds (Assumption 12).
- In `docs/tla/manifest.tsv` the five new rows have `exit` and `log` `-`
  until the session has run them (ADR 0013 §7, §10). The module's `Checked
  against` stays `71fee34` for every action but those this ADR changes,
  which await coder's commit, as ADR 0022 Assumption 20 had it
  (Assumption 23).

### 11. The test surface

Every name is spelled as in Go. test-author cannot read `internal/blobfs`'s
or `internal/store`'s non-test files, so everything a test may use is here.

**Names a test may use**, restated from the code at `d75d14a` unless marked
new:

```go
// internal/blobfs
func New(ctx context.Context, cfg Config) (*FS, error)
type Config struct {
    Store                  store.Store
    ChunkSize              uint32
    OwnerUID, OwnerGID     uint32
    ReadOnly               bool  // ADR 0011
    SnapshotRetention      int   // 0 selects the default, 10; negative turns pruning off
    MaxDirtyBytes          int64 // negative disables the budget's wait
    Log                    *slog.Logger
    AllowUnconditionalRoot bool  // ADR 0021
    // ... fields this ADR does not use
}
func (f *FS) Sync(ctx context.Context) error
func (f *FS) Root() vfs.Handle
// GetAttr, SetAttr, Access, Lookup, ReadLink, FSStat, Create, Mkdir, Symlink,
// Remove, Rmdir, Rename, ReadDir, Read, Write and Commit, as internal/vfs/vfs.go declares them
func quietLog() *slog.Logger // internal/blobfs/fs_test.go: a logger that discards everything

// unexported, at d75d14a
type FS struct {
    mu           sync.RWMutex
    epoch        uint64 // under mu
    rootETag     string // under mu
    rootSnapshot string // under mu
    doubtKey     string // under mu
    dirty        bool   // under mu for writing
    rootKnowledge *fsm.Machine[rootState, rootEvent] // under mu; State() may be called holding mu for reading
    mode         *fsm.Atomic[mountMode, mountEvent]  // State() needs no lock
    // ...
}
const (
    rootKey        = "root"
    snapshotPrefix = "snapshots/"
    chunkPrefix    = "chunks/"
    rootInodeID    = 1
)
type rootPointer struct { Version int; Epoch uint64; Snapshot string; FSID uint64; WrittenNS int64; Writer string } // json tags as ADR 0022 §8 gives them
func decodeSnapshot(b []byte) (*snapshot, error)
type snapshot struct { Version int; Epoch uint64; NextIno uint64; ChunkSize uint32; Inodes map[uint64]*inode; CreatedNS int64 }
type inode struct { ID uint64; Size uint64; Entries map[string]uint64 /* ... */ }
type rootState uint8 // rootKnown, rootInDoubt
type mountMode uint32 // mountWritable, mountReadOnly, mountDiverged

// new in this ADR (sections 2 and 3)
// FS gains, under mu: changes uint64; and commitMu sync.Mutex, which no test touches
```

```go
// internal/store, unchanged
type Store interface {
    Get(ctx context.Context, key string) ([]byte, error)
    GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error)
    Head(ctx context.Context, key string) (ObjectInfo, error)
    Put(ctx context.Context, key string, data []byte) error
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix, after string, max int) ([]ObjectInfo, error)
    PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error)
    Name() string
    GetWithETag(ctx context.Context, key string) (data []byte, etag string, err error)
}
type ObjectInfo struct { Key string; Size int64; ETag string }
var ErrNotFound, ErrPrecondition, ErrUnsupported error
func NewLocal(root string) (*Local, error)
```

**Facts a test may rely on.**

- The facts of ADR 0022 §8 stand: what `New` does on an empty bucket and on
  one that holds a filesystem; what a *commit* is (a `Create` of a fresh name
  in the root directory by `vfs.Caller{UID: 501, GID: 20}`, then `Sync`), and
  that one that succeeds writes one object under `snapshotPrefix` and then
  one `PutIfMatch` of `rootKey`; a `*store.Local`'s ETags; the foreign root
  pointer; capturing log records; and what a test may read holding `FS.mu`
  for reading, which now includes `FS.changes`.
- A commit's `Put` under `snapshotPrefix` and its `PutIfMatch` of `rootKey`
  are made holding neither `FS.mu` nor the commit gate (§1): while either is
  held by the store, every `vfs.FS` method but `Sync`, `Commit` and a `Write`
  with `vfs.FileSync` or `vfs.DataSync` returns without waiting for it, a
  `SetAttr` that sets a size included (§4).
- `Sync`, `Commit` and a stable `Write` queue behind a commit in flight, and
  return after it (§4).
- A change made while a commit is in flight is not in that commit's
  snapshot, leaves `FS.dirty` true after that `Sync` returns nil, and is in
  the next commit's snapshot (§3). A `Sync` on a mount with no change since
  the last seal returns nil and makes no store call (§3).
- `FS.changes` advances by at least one at every change that dirties the
  namespace: a `Create`, `Mkdir`, `Symlink`, `Remove`, `Rmdir`, `Rename` or
  `SetAttr` that succeeds, a `Write` that buffers, and a flush that stores
  something, which a `Commit` of a written file makes (§3).
- A diverged mount's `Sync` returns an error whose text begins
  `filesystem diverged` and makes no store call at all, reads included (§2).
- A snapshot's bytes decode with `decodeSnapshot`; the root directory's
  entries are `Inodes[rootInodeID].Entries`, and a file's size is its
  inode's `Size`. A remount is `New` on the inner `*store.Local` with
  `SnapshotRetention` -1.

**The harness**, `uc43Store`, in a new file
`internal/blobfs/unlocked_commit_test.go` (Assumption 28): a `store.Store`
over a `*store.Local` under `t.TempDir()`, written without embedding
`store.Store`, that passes every call through and, when armed, one-shot:

- **holds the next `Put` under `snapshotPrefix`**: closes an `entered`
  channel when the call is made, waits until the test releases it or the
  test's context ends, then passes the call through;
- **holds the next `PutIfMatch` of `rootKey`** in the same way, then passes
  it through;
- **holds the next `PutIfMatch` of `rootKey`, then loses its reply**: once
  released, applies the swap on the inner store and returns `""` and an
  error of the test's own, `uc43ErrInjected` (`SwapReplyLost`);
- **fails the first read of the root pointer after the next swap**: as
  `rk5Store`'s arm of that name (ADR 0022 §8), so that a mount is left in
  doubt;
- **holds the next `GetWithETag` of `rootKey`** in the same way as the
  `Put`, then passes it through;
- records, in call order, every `Put` under `snapshotPrefix` with its key,
  every `PutIfMatch` of `rootKey` with its `etag` argument and how it was
  answered, every `GetWithETag` of `rootKey`, and every `Delete` with its
  key.

Every hold selects on the test's context, which a cleanup cancels, and a
cleanup releases every hold (ADR 0019 §6's rules). Every mount has
`SnapshotRetention` −1 and `MaxDirtyBytes` −1; every `New` and every `FS`
call runs in a goroutine that recovers a panic and is waited for under a
bound of 10 s; "returns while held" means the call returned under the bound
while the hold's `release` had not been closed and the `Sync` goroutine had
not finished; nothing sleeps. `Create`, `Sync` and the field reads follow
`rk5Commit`, `rk5Sync` and `rk5Read` in shape.

**Tests**, each from §1 to §5:

- **U1, the issue's done-when**
  (`TestNamespaceOperationsDoNotWaitForACommitsStoreCalls`). `New` over an
  empty bucket; create `a.bin` and write 100 bytes at 0 to it, `vfs.Unstable`;
  create `d`, a directory. Arm the held snapshot `Put`; start `Sync` in a
  goroutine; wait for `entered`. While held, each of these returns, and
  succeeds unless said otherwise: `GetAttr` of the root; `Lookup` of `a.bin`;
  `ReadDir` of the root; `Access`; `FSStat`; `ReadLink` of a symlink created
  before the `Sync`; `Read` of `a.bin`'s 100 bytes, which returns them;
  `Create` of `b`; `Mkdir` of `e` in `d`; `Rename` of `e` to `f`; `Remove` of
  `b`; `SetAttr` of `a.bin`'s mode; a `Write` of 10 bytes at 100 of `a.bin`,
  `vfs.Unstable`, after which `GetAttr` reports size 110; and `SetAttr` of
  `a.bin`'s size to 50, a truncate. Then release; `Sync` returns nil. The
  harness recorded exactly one `Put` under `snapshotPrefix` and one
  `PutIfMatch` of `rootKey` in that `Sync`. Under `FS.mu` for reading,
  `FS.dirty` is true. A remount of the inner store has `a.bin` at size 100
  and `d` with no entry, and no `f`: the changes made while held are not in
  that commit. `Sync` again: returns nil, with one more `Put` under
  `snapshotPrefix` and one more `PutIfMatch`; a remount has `a.bin` at size
  50, `f` under `d`, and no `b`. At `d75d14a` every call while held hangs
  until the test's bound, since `FS.mu` is held across the `Put`.
- **U2, the same with the swap held**
  (`TestNamespaceOperationsDoNotWaitForTheRootPointerSwap`). U1 with the
  held `PutIfMatch` in place of the held `Put`, and the same calls while
  held, `SetAttr` of a size included: each returns while held. After the
  release the assertions on `FS.dirty`, the remounts and the second `Sync`
  are U1's.
- **U3, the guard** (`TestAChangeMadeWhileACommitIsInFlightStaysDirty`;
  `CommitProtocol_NoClearIfUnchanged`'s route). One commit behind it, as
  ADR 0022 §8 has it; arm the held `Put`; create `c2`; start `Sync`; wait for
  `entered`; create `c3` while held; read `FS.changes` holding `FS.mu` for
  reading before the `Sync` started and after `c3`'s `Create` returned: the
  second is greater. Release; `Sync` returns nil; `FS.dirty` is true;
  `FS.epoch` is 2. A remount has `c2` and not `c3`. `Sync` again: returns nil,
  and the harness recorded a second `Put` under `snapshotPrefix` whose key
  carries epoch 3 and a second `PutIfMatch`; `FS.dirty` is false; a remount
  has `c2` and `c3`. A build that clears the flag unconditionally fails on
  `FS.dirty` after the first `Sync` and on the second `Sync`'s `Put` count,
  which is the route TLC reports for the MustFail row.
- **U4, the guard at the failure-site adoption**
  (`TestAChangeMadeWhileALostSwapIsSettledStaysDirty`). One commit behind
  it; arm the held-then-lost swap; create `c2`; start `Sync`; wait for
  `entered`; create `c3` while held; release: the swap is applied and
  answered with `uc43ErrInjected`, the read that follows shows the root
  pointer naming the attempt, and `Sync` returns nil (ADR 0022 §4,
  `probeOurs`). The harness recorded, after the `PutIfMatch`, exactly one
  `GetWithETag` of `rootKey` and no further `PutIfMatch`. `FS.dirty` is true;
  `f.rootKnowledge.State()` is `rootKnown`; `FS.epoch` is 2; `FS.rootSnapshot`
  is the key the swap wrote. A remount has `c2` and not `c3`. `Sync` again:
  returns nil, its `PutIfMatch` passed through with the root pointer's MD5
  as its `etag` argument, its key at epoch 3; a remount has `c2` and `c3`.
- **U5, a second `Sync` waits, then commits what the first missed**
  (`TestASecondSyncWaitsForTheCommitInFlightAndCommitsOnlyTheRest`). One
  commit behind it; arm the held `Put`; create `c2`; start `Sync` A; wait
  for `entered`; create `c3`; start `Sync` B. B has not returned while A's
  `Put` is held: assert it after the calls of U1's kind have returned, so
  that the assertion is not a race against B's first pass alone. Release;
  A returns nil; B returns nil. The harness recorded two `Put`s under
  `snapshotPrefix`, at epochs 2 and 3 in that order, and two `PutIfMatch`
  calls, the second's `etag` argument equal to the MD5 of the root pointer
  the first wrote. A remount has `c2` and `c3`; `FS.dirty` is false.
- **U6, a second `Sync` with nothing new makes no store call**
  (`TestASecondSyncWithNothingNewMakesNoStoreCall`). U5 without `c3`: B
  returns nil after A, and the harness recorded one `Put` under
  `snapshotPrefix`, one `PutIfMatch` and no `GetWithETag` of `rootKey` in
  all.
- **U7, a stable write during a flight is durable when it returns**
  (`TestStableWriteDuringACommitIsDurableWhenItReturns`). Create `a.bin`;
  commit; arm the held `Put`; create `c2`; start `Sync` A; wait for
  `entered`; start a `Write` of 100 bytes at 0 of `a.bin` with
  `vfs.FileSync` in a goroutine, W. W has not returned while A's `Put` is
  held, asserted as U5 asserts B. Release; A returns nil; W returns
  `(100, vfs.FileSync, nil)`. A remount of the inner store, with no further
  `Sync`, reads those 100 bytes from `a.bin`. A build without the guard can
  answer W with `FS.dirty` cleared under it and no commit of its bytes, so
  the remount reads an empty file there.
- **U8, the top-of-commit probe holds no lock**
  (`TestTheTopOfCommitProbeHoldsNoLock`). ADR 0022 §8's K7 setup through the
  failed read: one commit behind it, the held-then-lost swap released at
  once, with the failed read after the swap armed, so that `Sync` returns an
  error whose text contains `in doubt` and `f.rootKnowledge.State()` is
  `rootInDoubt`. Arm the held `GetWithETag` of `rootKey`; create `c3`; start
  `Sync`; wait for `entered`: the read is the top-of-commit probe, made
  before any `Put` under `snapshotPrefix`. While held, `GetAttr` of the root,
  `Lookup` of `c3`, `Create` of `c4` and `SetAttr` of a size each return.
  Release; `Sync` returns nil; `rootKnown`; the harness recorded, after the
  held read, one `Put` under `snapshotPrefix` at epoch 3 and one
  `PutIfMatch`; a remount has `c2`, `c3` and `c4`.
- **U9, a diverged mount's `Sync` makes no store call**
  (`TestADivergedMountsSyncMakesNoStoreCall`). ADR 0022 §8's K6: a foreign
  root pointer, then a commit whose `Sync` returns the conflict error, after
  which `f.mode.State()` is `mountDiverged`. Then `Sync` again: returns an
  error whose text begins `filesystem diverged`, and the harness recorded no
  call of any kind since the first `Sync` returned, no `GetWithETag` of
  `rootKey` included.
- **U10, every change advances the count**
  (`TestEveryChangeAdvancesTheChangeCount`). On a plain `*store.Local`, read
  `FS.changes` holding `FS.mu` for reading before and after each of: `Create`
  of a file; `Mkdir`; `Symlink`; `SetAttr` of a mode; a `Write` of one byte,
  `vfs.Unstable`; `Commit` of that file, whose flush stores the byte;
  `SetAttr` of a size; `Rename`; `Remove`; `Rmdir`. Each pair's second value
  is greater than its first. A `GetAttr`, a `Lookup` and a `Read` advance
  nothing.

**Changes to existing tests.** Every test at `d75d14a` stands in its
assertions (Assumption 21); three may change their words:

- `internal/blobfs/mount_mode_test.go`, M7: its failure message says
  `commitLocked refuses at its top`; since this ADR the refusal is `Sync`'s,
  before the probe (§2). The message may say so; the assertion stands.
- `internal/blobfs/commit_window_test.go`: its header's "Not covered, on
  purpose" names "that the gate is held across the whole commit phase on
  every path"; since this ADR the phase ends at the seal (§2). The sentence
  may say so; every test there holds its `Sync` in the first pass or the
  settle pass, where the gate is still held, so its assertions stand.
- `internal/blobfs/diverged_race_test.go`, M4: its `Write` racing a held
  swap now returns while the swap is held rather than waiting for `FS.mu`;
  the test pins neither, and its doc comment already says the `Write` "may
  land before the commit fails and succeed". It stands.

`internal/blobfs/root_knowledge_test.go`'s K4 to K19, `swap_fallback_test.go`'s
G-tests, `prune_root_test.go`'s R-tests, which hold a swap and make no call
on the same mount meanwhile, `read_only_test.go`, `load_race_test.go`,
`write_sync_test.go`'s concurrent writers and the NFS tests stand unchanged.
`internal/blobfs/tla_trace_test.go` fails until coder's commit, since
`trace.tsv` names `FS.seal`, `FS.commitSealed`, `FS.applyLocked` and
`FS.markDirty` before they exist, as ADR 0022 Assumption 21 accepted
(Assumption 23).

### 12. Scope, and `CHANGES`

Nothing is split out (Assumption 20). The adjacent questions, batching stable
writes (#44) and the encode's own cost under `FS.mu` (ADR 0007), have their
issue or their place already.

**`CHANGES`**, one line, not `BREAKING:` (Assumption 19), proposed for the
session to record:

> Namespace operations no longer wait for a commit's uploads and
> root-pointer swap: a commit encodes the namespace under the lock, runs its
> store calls outside it, and clears the dirty flag only if nothing changed
> meanwhile, so a change made while a commit is in flight goes in the next
> one; a size change waits only until the snapshot is encoded, and a second
> `Sync` waits for the commit in flight and then commits only what it
> missed.

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation**, under the owner's 2026-10-10 words, as
   ADRs 0015 to 0022 were. The design is mine: the issue's sketch asks for
   encode, upload, retake-and-swap with a guard, and this ADR decides the
   guard's form, the lock that serialises commits, the gate's release at the
   seal, the probe's split and every name.
2. **Read, not run, at `d75d14a`.** Every statement about the code was
   checked by reading it there; nothing was run: not TLC, not `go test`. The
   issue's text is as the session gave it in the brief; it was not read from
   the tracker, and the brief's summary is what "the issue's sketch" and
   "done-when" above quote. The new rows' outcomes and the route in §10 are
   predictions the session's runs confirm or refute, and the architect
   records them in `manifest.tsv` (ADR 0013 §10).
3. **A change count, not a dirty epoch and not clearing the flag at the
   seal** (§3). The issue named three possibilities. Clearing `FS.dirty` at
   the seal and restoring it on failure would need no new field, but makes
   the flag false while a commit the bucket does not yet hold is in flight,
   which every reader of the flag, the lifecycle ADR's final commit among
   them, would have to know, and it needs two guards, one on success and one
   on failure. A dirty epoch is the count under another name. The count
   keeps the flag's meaning, needs one comparison, and costs one increment
   at eight sites that already hold the lock, through one helper that review
   can check every site calls. Comparing what was encoded would cost a second
   encode per commit.
4. **The gate is released at the seal, not held across the flight** (§2).
   ADR 0012 anticipated exactly this, and the alternative, keeping the gate
   across the store calls, would leave every size change waiting for the
   round trips #43 names, which is the defect for one operation. Rule D's
   argument needs the gate only until the encode, since the snapshot is
   fixed there.
5. **A new mutex serialises commits** (§4). Once the gate is released at the
   seal, something else must keep a second commit from sealing at the same
   epoch against the same ETag while the first is in flight, which would
   diverge the mount alone. A machine with a waiting state would be a lock
   under another name, and a condition variable would reinvent one; coalescing
   a second `Sync` into the first's result would answer a stable write's
   `Sync` for changes the first commit did not seal, which is the loss U7
   shows. The mutex is the plain tool, and E3 already exempts a lock in this
   role.
6. **The clean return and the diverged refusal come before the probe, and
   the probe before the gate** (§2). The probe is a store call, so it cannot
   sit between the settle pass and the seal without holding the gate across
   it, and cannot sit inside the seal's hold at all. Putting it before the
   gate keeps ADR 0012 §2's shape: the settle pass runs immediately before
   the seal under one hold of the gate. The diverged refusal precedes the
   probe so that a diverged mount reads nothing, as it stored nothing
   before; the clean return precedes both as it did.
7. **Reading `dirty` before the gate is sound** (§2). Only a commit's apply
   clears the flag, and commits are serialised by the lock `Sync` already
   holds, so a flag read true stays true until this `Sync`'s own apply. A
   flag read false means no change is uncommitted, and then no file is
   unsettled either: a truncate that cut a stored chunk set the flag in the
   same hold, and that flag was cleared only by a commit whose seal came
   after the truncate returned, which the gate orders, and whose settle pass
   flushed the file (ADR 0012 §2, §3). So a clean namespace has nothing for
   the settle pass to do, and returning before the gate skips nothing.
8. **`resolveRoot` takes `FS.mu` itself, after its read, and keeps its
   signature** (§5). A split into a read and an apply would give the two
   call sites, the top of a commit and the failure site, a second function
   each to call in the right order; one function that reads then applies
   cannot be called in the wrong order. The cost is the second hold at the
   failure site (Assumption 25).
9. **The swap conditions on the ETag copied at the seal** (§2). Reading
   `FS.rootETag` at the swap would need a hold of `FS.mu` for a value that
   cannot have changed, and would read as though it could.
10. **`markDirty`, and the eight sites** (§3). A helper makes the invariant
    "every write of `dirty = true` advances `changes`" a thing review checks
    by grepping for `f.dirty = true` in non-test code, which must find
    nothing. The seam of ADR 0011 §7 writes the field from a test and is
    kept, since it dirties a read-only FS whose commit cannot succeed.
11. **The names, and the removal of `commitLocked`** (§2). `seal`,
    `commitSealed` and `applyLocked` say what each does and what it holds, by
    the package's convention that a `Locked` suffix means the caller holds
    `FS.mu`; a function named `commitLocked` that released the lock would
    say the opposite of what it does, so the name goes. `sealedCommit`,
    `changes` and `commitMu` follow the fields beside them. `trace.tsv`'s
    rows that named `commitLocked` name the three functions, and the ADRs
    that name it gain a cross-reference line (Assumption 24).
12. **Two `Fixes` members, and no MustFail row for `UnlockedCommit`** (§10).
    The brief asked for a member the configurations can switch, so that
    every existing configuration keeps describing `d75d14a` and new ones
    describe the code with this ADR, and for a MustFail row that shows the
    guard load-bearing. One member could not do both: removing it gives the
    locked code, in which every property holds, so no row could show the
    guard. So the atomicity and the guard are two members, and the MustFail
    row keeps the first and removes the second. ADR 0013 §7 asks a MustFail
    row of every fix; `UnlockedCommit` fixes a latency, which no invariant
    states, and the module's J18 already reads §7 as not asking a row that
    shows nothing. The row that lacks the member is the existing MustHold
    row of the same bounds, and `CommitProtocol_Unlocked` with the member
    holds beside it.
13. **`DirtyUntilCommitted` and the ghost** (§10). The harm the guard
    prevents is a change the mount holds that no commit will carry, which
    none of the five properties sees: `NoLostAck` is about acknowledged
    commits, and the model's `Mutate` adds nothing to `base`. The ghost
    `uncommitted[m]` holds the promise, as `acked` does, and the property
    says the mount knows it is dirty while it holds such a change, which is
    what makes the next `Sync` commit it. It is not vacuous: without the
    guard the two differ after one `Committed`. Only one MustFail row is
    written, though the guard also runs on the failure-site adoption, because
    that route is five steps where the swap's is four, and TLC reports the
    shortest, so a row meant for it would report the swap's route; the Go
    two-hold form of the adoption is pinned by U4 and by review instead.
14. **`changedSinceSeal` is a boolean, and `InFlight` needs `ChunksFirst`**
    (§10). The guard asks only whether the count moved, so a boolean is the
    count's whole content for the model. The seal is the encode before the
    snapshot `Put`, which is the commit's first call only with `ChunksFirst`;
    every configuration has it, and the conjunct keeps the member from
    describing a commit order the code never had.
15. **R15 and R16** (§10). The seal folds into `PutSnapshot` because a
    change between the seal and the `Put` in Go maps to a `Mutate` after
    `PutSnapshot` in the model, with the same state after both: the change
    touches no key, and the `Put` reads no field the change writes. The
    apply folds into the store call before it because the only mount-local
    step that can fall between the call's return and the apply, a change,
    commutes with the call and moves before it, which is the order in which
    the guard then reads the count; and the fields the apply writes are
    written by nothing else meanwhile (§5). A finer split would add
    interleavings that no property tells apart (ADR 0013 §4).
16. **Bounds, tiers and run time** (§10). The four MustHold configurations
    take their parents' bounds, so that each explores with the members what
    its parent explored without them; `changedSinceSeal` adds one bit per
    mount and `Mutate` one enabled step per in-flight state, so each should
    run in the same order of magnitude as its parent, up to a few times its
    states. `CommitProtocol_UnlockedProbeTwoWriters` is `full`, as its parent
    is; if any row runs too long, the session reports it and its bounds
    shrink in a later dispatch, as ADR 0019 Assumption 11 provided. The
    MustFail row's bounds are the smallest that admit its route: `MaxEpoch`
    2, `Nonces` `{1}`, no fault.
17. **Nothing changes for clients but latency** (§7). NFS's attribute replies
    describe the server's state at the time of the call, and close-to-open
    consistency is the client's; the reply to an operation issued during a
    flight was never tied to that flight's swap, since the operation was
    acknowledged before any commit held it (ADR 0012 *What this does not
    decide*). The one promise a commit backs, `COMMIT` and a stable `WRITE`,
    is kept by §3 and shown by U7.
18. **Logs unchanged.** No record is added: the `"committed"` record, the
    probe's records and the reclaim's are as pinned, and a commit whose
    success leaves the namespace dirty is the ordinary case under load,
    which `Debug` would only repeat.
19. **Not `BREAKING:`, and one `CHANGES` line** (§12). Nothing a deployment
    must act on changes; the line records changed behaviour a user observes,
    lower latency during commits and the ordering of a change made during a
    flight, which CLAUDE.md's rule for `CHANGES` counts as changed behaviour
    rather than a refactor. The session judges the line against that rule.
20. **No split** (§12). The brief asked what to split; nothing here needs an
    issue of its own: batching stable writes is #44, and the encode's cost
    under `FS.mu` is ADR 0007's known cost, unchanged.
21. **Existing tests stand** (§11). Each test that holds a commit mid-flight
    was read: the commit-window tests hold the first pass or the settle
    pass, where the gate is still held; the prune tests hold a swap and make
    no call on that mount meanwhile; the race test pins neither of its
    `Write`'s outcomes. The three that mention the old shape in words may
    change those words.
22. **E3 restated, nothing new granted** (§8). The commit's phases were
    already the sequential code E3 names; this ADR adds a lock and two
    functions to the same shape, with the same reason, and ADR 0018 §4 says a
    restatement adds the ADR to the row.
23. **`Checked against`, the trace and the tripwire** (§10, §11). As
    ADR 0022 Assumptions 20 and 21: the rows this ADR changes exist before
    the code, the tripwire fails on the new symbols until coder's commit is
    on the branch, and the architect bumps the line and marks the vocabulary's
    entries *exists* then. The session's runs are the session's evidence.
24. **Cross-references** (§9). ADRs 0002, 0003, 0005, 0006, 0007, 0012, 0013,
    0014, 0018, 0019, 0020, 0021 and 0022 each gain a `Revised` line, cross-
    reference only, and ADR 0011 one for §7's list of checks that names
    `commitLocked`; none of their decisions moves. ADR 0017's one passing
    mention of `commitLocked` is left, since it describes what a snapshot
    holds, which is as true of `seal`. `docs/DESIGN.md` §5 gains the
    statement the issue's done-when asks for, and §16's table this ADR's
    row.
25. **The second hold at the failure site is harmless** (§5). Only changes
    and `PruneEnd` can run between `resolveRoot`'s hold and `applyLocked`'s,
    and a reader there sees a state the top-of-commit adoption already
    exposes. Folding the two into one hold would need `resolveRoot` to take
    the sealed commit and finish it, coupling the probe to the commit's
    success path at both sites for a window no property can see.
26. **`initEmpty` takes neither lock** (§2). Nothing runs beside it, as
    ADR 0022 §2 and ADR 0018 §5 have it for its `Fire`; taking `commitMu` or
    the gate there would say otherwise.
27. **A failed `Fire(rootSwapUnknown)`** (§2) is pinned as ADR 0022 §4's
    "Doubt" paragraph has it, unreachable, and now runs in `commitSealed`'s
    hold; the success path it falls back to for a reply with no ETag is
    `applyLocked(s, "")`.
28. **The harness and the tests** (§11). `uc43Store` follows `pr88Store`'s
    holds and `rk5Store`'s records and arms; the held `Put` and the held
    swap are the two store calls the issue names, and the held read is the
    probe ADR 0022 §5 said would move with them. "Returns while held" is
    asserted against the hold's channel and the `Sync`'s goroutine rather
    than a timer, so no test passes by a slow store. U5's and U7's assertion
    that a queued call has not returned is made after unrelated calls have
    returned, which bounds the race against the queued call's first pass
    without a sleep; a build that lets the second commit through would still
    be caught by the `Put` count and the remount.

## Alternatives considered

- **Keep the commit gate across the flight, release only `FS.mu`.** No new
  lock; commits serialised as today. Every size change would keep waiting
  for the store round trips, which is #43 for one operation, and ADR 0012
  had already said the gate can go at the encode. Rejected (Assumption 4).
- **Clear `FS.dirty` at the seal; restore it on failure.** Fewer lines, but
  the flag would mean "changed since the last seal" rather than "holds
  changes no commit has acknowledged", with a false reading during every
  flight, and two guards instead of one. Rejected (Assumption 3).
- **Compare the encoded bytes** at the apply against a fresh encode. One
  encode per commit more, under `FS.mu`, which is the cost ADR 0007 already
  names. Rejected (Assumption 3).
- **Loop until nothing changed**, as the issue's sketch offers. A second
  commit at once, holding the lock, for a change that the next tick or the
  next stable write carries anyway, and a loop that never ends under steady
  writes. The guard without the loop is what "a later commit supersedes an
  earlier one" asks.
- **Coalesce a queued `Sync` into the commit in flight.** It would answer a
  stable write's `Sync` for a change the flight did not seal. Rejected
  (Assumption 5).
- **Split `resolveRoot` into a read and an apply.** Two functions to call in
  order at two sites, for the same holds. Rejected (Assumption 8).
- **One `Fixes` member.** Could not show the guard load-bearing. Rejected
  (Assumption 12).
- **Model the seal as a step of its own.** Honest without a reduction, at the
  cost of a `pc` string and more states for every configuration with the
  member; R15 gives the commutation instead (Assumption 15).

## Consequences

- During a commit, every namespace operation and every size change is
  answered after at most the settle pass and two holds of `FS.mu` that make
  no store call; a store that answers slowly holds up only the commit and the
  `Sync`s queued behind it.
- A change made while a commit is in flight is in the next commit, and the
  flag says so; a stable write's acknowledgement is still backed by its own
  commit.
- `FS` has one more lock and one more counter; `commitLocked` is three
  functions; the lock order has one more level at its top.
- The probe's read, the reclaim and the fallback's calls run off the lock,
  closing what ADRs 0021 and 0022 left to #43.
- Four MustHold configurations widen the module's envelope to changes made
  during a flight, one MustFail configuration keeps the guard shown needed,
  and `manifest.tsv` has five rows for the session to calibrate.
- Hard to change later: that a commit's snapshot is fixed at the seal and
  the swap decides nothing about the namespace, which the redesign's
  consistency point (`docs/DESIGN.md` §5) inherits; and that `FS.dirty`
  means "holds changes no acknowledged commit holds", which the lifecycle
  ADR's final commit relies on.

## What this does not decide

- Batching stable writes so that several share one commit (#44).
- Releasing `FS.mu` during the encode itself, which still holds the
  uncompressed snapshot under the lock (ADR 0007 *Context*).
- Whether a `Sync` queued behind a commit in flight should run its first
  pass again after it takes `commitMu`; today it does not, and the settle
  pass covers what Rule C needs.
- How `FileData.tla`, not yet written, folds `SyncCommit`: ADR 0013 §4's
  argument that "all of `commitLocked`" is one action rested on `FS.mu` being
  held throughout; with this ADR its seal is the one access to `FS.mu`, and
  its store calls touch keys no FileData step reaches, so the fold may stand
  or split at the seal, which that spec's PR decides.
- Whether the committer should go quiet after repeated identical failures
  (#102).
- Moving the encode or the chunk uploads off the gate (ADR 0012 *What this
  does not decide*, the unlocked pre-settle pass).
- Multi-writer operation beyond detection (`docs/DESIGN.md` §15).

## Sources

No external source was fetched for this ADR: the interface it settles is the
lock discipline of one Go package, and every external behaviour it rests on
is cited by an earlier ADR and not read again. Cited as those ADRs quote
them: that a `sync.RWMutex` excludes new readers once a writer waits
(ADR 0012 *Sources*); that updates to a single key are atomic (ADR 0013
*Sources*); that a `GET`'s ETag is the one `If-Match` compares against
(ADR 0013 and ADR 0020 *Sources*); RFC 1813 §3.3.21 on `COMMIT`, and that a
client retransmits only a request it has had no reply to (ADR 0022 *Sources*;
ADR 0003 §5); and RFC 1813 §3.3.2, that `SETATTR` is not guaranteed atomic
(ADR 0012 *Sources*).
