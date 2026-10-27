# 0019. A prune pass deletes only below its own commit's snapshot

**Status:** Accepted
**Date:** 2026-10-24
**Revised:** 2026-10-24 — cross-reference only: ADR 0020 landed `AtomicLoad`,
closing the load race §1 leaves to it; its `CommitProtocol_NoAtomicLoadPrune`
shows the residual Assumption 4 names, and
`CommitProtocol_PruneTwoWritersFromEmpty` shows it closed. The rule, the Go
change and the configurations here are unchanged.
**Revised:** 2026-10-25 — cross-reference only: ADR 0021 decided #91, which
§1 leaves to it. The unconditional fallback now follows a swap answered 501
only on a mount whose operator set `Config.AllowUnconditionalRoot`, so the
case §1 names, two writers on the fallback, needs two such mounts; without
the opt-in such a swap fails the commit and writes nothing. The rule, the Go
change and the configurations here are unchanged.
**Revised:** 2026-10-27 — cross-reference only: ADR 0022 decided #5. A swap
or a fallback `Put` that takes effect and returns an error no longer leaves
the mount holding a stale ETag: the mount reads the root pointer, finds it
naming the attempt, and adopts it, so `Sync` returns nil there. §6's R7 and
R8, which observe that `Sync` returning an error, change as ADR 0022 §8
says; §1's argument stands, since an adopted key is the one the root pointer
already names (ADR 0022 §5). The rule, the Go change and the configurations
here are unchanged.
**Issue:** #88

## Context

**The owner's decisions.** Two, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0018 were, and its design is the architect's (Assumption 1).
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." This change is not breaking (Assumption 14),
   so nothing here relies on it.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `13aebc4`, the commit this ADR's branch was cut from
(Assumption 15). Code is cited by symbol, in `internal/blobfs/commit.go`
unless it says otherwise.

- `commitLocked` writes the namespace under a fresh key, `snapshotPrefix`,
  then the epoch as twenty zero-padded decimal digits, `-`, six random bytes
  as twelve hex digits, and `.json.gz`; swaps the root pointer to it with
  `PutIfMatch`, or, after `store.ErrUnsupported`, writes it with `Put` and
  reads the ETag back with `Head`; and on success calls
  `schedulePrune(epoch)`.
- `schedulePrune(current uint64)` starts a goroutine running
  `pruneSnapshots(ctx, current)` unless `FS.retention` is 0 or `FS.pruning`
  is set. It sets `FS.pruning` first, and the goroutine clears it under
  `FS.mu` when it ends. The goroutine runs under a 60 s context of its own,
  holding no lock.
- `pruneSnapshots(ctx, current uint64)` lists every key under
  `snapshotPrefix`, returns if there are no more than `FS.retention`, sorts
  them, takes every key but the newest `FS.retention` as doomed, and deletes
  each in ascending order, skipping only keys whose epoch is `current`. Its
  doc comment says the snapshot the root pointer names is inside the window
  "by construction".
- String order of these keys is epoch order, then nonce order, since the
  epoch is zero-padded and the nonce has a fixed width; `KeyLess` in
  `docs/tla/CommitProtocol.tla` is that order.
- `New`, in `internal/blobfs/fs.go`, maps `Config.SnapshotRetention` 0 to
  the default, 10, and a negative value to 0, which turns pruning off.
  `-snapshot-retention`, in `cmd/strata/main.go`, defaults to 10, with the
  help text "superseded namespace snapshots to keep (-1 keeps all)".
- `S3.Put` and `S3.Delete` are sent again by `S3.do` after a transport
  error, a 5xx or a 429, so an attempt can take effect while the call
  returns an error; `S3.PutIfMatch` is never retried (`internal/store/s3.go`).
  `Local.PutIfMatch` compares the current body's MD5 with the ETag it was
  given and answers `store.ErrPrecondition` on a mismatch
  (`internal/store/local.go`).

**The defect.** #88, "Pruning can delete the snapshot the root pointer
names". A pass spares only the *epoch* it was begun for, and deletes every
other listed key outside the newest `retention`. So once `retention` keys
sort above the snapshot the root pointer names, and that snapshot's epoch is
not the one the pass was begun for, the pass deletes it. The next `loadOrInit`
then fails with "read snapshot ...: not found", and the bucket cannot be
mounted; no tool repoints the root (#10). `docs/tla/CommitProtocol.tla`
showed it by eight configurations, which the issue's comment of 2026-10-24
lists. Every route has one shape: a pass begun for epoch e lists after the
root pointer has moved past e, and the keys above the root's come from a
failed swap's orphan (route 1, `CommitProtocol_PruneOrphan`), a losing
writer's attempt (route 2, `CommitProtocol_PruneTwoWriters`), the mount's own
unswapped next attempt (route 3, `CommitProtocol_PruneOverlap`), both at once
(`CommitProtocol_PruneOverlapTwoWriters`), a snapshot `Put` that took effect
and returned an error (route 4, `CommitProtocol_PruneSnapshotReply`; also at
retention 2 together with route 3, `CommitProtocol_PruneSnapshotReplyRetention2`,
so that no fix can rest on a retention above 1), or a write of the root
pointer that took effect and returned an error, whose retry sorts above it
(route 5, `CommitProtocol_PruneSwapReply` and `CommitProtocol_PruneFallbackReply`).
Separately, the window counts the current snapshot, so `-snapshot-retention N`
keeps at most N−1 superseded snapshots while its help text says N.

**What the ADRs say.** ADR 0013 Assumption 8 records that the "by
construction" comment is wrong once commits overlap a pass. ADR 0013 §7 and §8
say how a fix lands: spec first; the defect's configurations flip to MustHold
with the fix added to `Fixes`; a MustFail configuration removes the fix; and
the counterexamples become Go tests that test-author writes from the traces
and the spec. The module's Assumption J14 leaves failed `List` and `Delete`
calls to this ADR, and its J15 ties `CommitProtocol_PruneTwoWriters`' flip to
its twin. ADR 0018 §3 lists `FS.pruning` as not yet moved to a machine, and
binds a change that changes how it moves. `docs/vocabulary.md`, V7, expects a
commit-protocol decision, such as deleting a snapshot, to be named by its ADR.

**What this constrains.** `schedulePrune`, `pruneSnapshots`, `commitLocked`'s
call to the first, and the function this ADR adds, all in
`internal/blobfs/commit.go`; `docs/tla/CommitProtocol.tla`, its
configurations and the three tables beside it; and `docs/vocabulary.md`.
Nothing on the wire, no flag, no format and no store method changes.

## Decision

### 1. The rule

A prune pass is begun by the commit that swapped the root pointer to the
snapshot key it wrote; call that key K. The pass deletes only keys that sort
below K, and of those keeps the newest `retention`. It never deletes K or any
key that sorts above K, and it never reads the root pointer.

**Why that cannot delete the snapshot the root pointer names.**

1. When the pass begins, the root pointer names K: the swap is one write that
   took effect (ADR 0013 §4), and on the fallback a single writer's `Put` is
   the last write of the root pointer.
2. From then on, the key the root pointer names never falls below K. A
   conditional write of the root pointer takes effect only against the ETag
   of the root pointer as the writer last read or wrote it, and that writer's
   epoch is the epoch of the snapshot that root pointer named, because
   `loadOrInit` takes both from one root pointer and `commitLocked`'s success
   path sets both from the swap it made. So the key such a write names is at
   the next epoch, which sorts above every key at the root's epoch, K
   included. A write that took effect and returned an error (route 5) names a
   key above the root it matched for the same reason.
3. So every key the pass deletes sorts below the key the root pointer names
   at the deletion, and below every key it will name afterwards.
   `RootSnapshotExists` holds at every `PruneDelete`, whatever the order of
   commits, failed swaps, lost replies and listings, and at any retention,
   1 included.

**Where step 2 rests, and what lies outside it.**

- The load race (#89). `loadOrInit`'s `Head` after another writer's swap
  pairs an older snapshot's epoch with the newer ETag, so that mount's
  attempt is at the root's own epoch and may sort below it; its swap then
  succeeds, and the root pointer falls to a key a pass may have deleted. That
  behaviour loses an acknowledged commit first (`NoLostAck`,
  `CommitProtocol_LoadRace`), and `AtomicLoad`, the fix the vocabulary
  reserves for #89, closes it. Within the bounds of the eight configurations
  it does not reach `RootSnapshotExists` (Assumption 4). ADR 0020 landed
  `AtomicLoad`: `loadOrInit` takes the body and the ETag from one read,
  `CommitProtocol_NoAtomicLoadPrune` shows the residual without the member,
  and `CommitProtocol_PruneTwoWritersFromEmpty` shows it closed with it.
- Two writers on the unconditional fallback overwrite each other's root
  pointer at will (`docs/DESIGN.md` §2; #91), so the key the root pointer
  names can fall there. The rule's guarantee on the fallback is a single
  writer's, as the fallback's own is. Since ADR 0021 the fallback follows a
  swap answered 501 only on a mount whose operator set
  `Config.AllowUnconditionalRoot`; without the opt-in such a swap fails the
  commit and writes nothing, so the case needs two opted-in mounts.
- A key below K that no root pointer names is garbage, whatever made it: a
  failed attempt's, a losing writer's, or an attempt whose swap can never
  succeed because its ETag is stale. Deleting it harms nothing.

### 2. The Go change

In `internal/blobfs/commit.go`:

```go
func (f *FS) schedulePrune(current string)                              // was (current uint64)
func (f *FS) pruneSnapshots(ctx context.Context, current string) error  // was (ctx, current uint64)
func prunable(keys []string, current string, keep int) []string         // new
```

- **`commitLocked`** calls `f.schedulePrune(key)` on both success paths, the
  swap's and the fallback's, where `key` is the snapshot key this commit
  wrote and the root pointer now names. Nothing else about `commitLocked`
  changes.
- **`schedulePrune(current)`** keeps its guard and its writes of `FS.pruning`
  exactly: it returns if `f.retention == 0` or `f.pruning`; otherwise it sets
  `f.pruning` and starts the goroutine, which runs
  `pruneSnapshots(ctx, current)` under its own 60 s context, logs a failure
  at Warn, and clears `f.pruning` under `FS.mu`. The caller holds `FS.mu` for
  writing, except `initEmpty`'s commit, which runs before the FS is shared,
  as now.
- **`pruneSnapshots(ctx, current)`**:
  1. lists every key under `snapshotPrefix`, 1,000 a page, as now; a `List`
     error is returned, and nothing is deleted;
  2. computes `doomed := prunable(keys, current, f.retention)`, where `keys`
     are the listed keys;
  3. calls `f.store.Delete` on each key of `doomed`, in order; a failed
     `Delete` is logged at Debug and skipped, as now;
  4. makes no other store call: in particular no `Get` or `Head` of the root
     pointer.

  Its doc comment states §1's rule and argument, and the sentence that calls
  the root pointer's snapshot safe "by construction" goes.
- **`prunable(keys, current, keep)`** is a plain function: no lock, no store
  call, no log, no `FS`, a function of its arguments alone; it modifies
  neither `keys` nor its elements. It returns the keys a pass begun for
  `current` may delete:
  - let `below` be every element of `keys` that is less than `current` in Go
    string order (`<` on strings), which for snapshot keys is epoch order,
    then nonce order;
  - a `keep` below 0 counts as 0;
  - if `below` has at most `keep` elements, the result has length 0;
  - otherwise the result is `below` sorted ascending with its last `keep`
    elements removed: the oldest `len(below) − keep` keys, in ascending order.

  So the result never holds `current`, any key at or above it, or any of the
  `keep` newest keys below it; with `keep` 0 it holds every key below
  `current`. `keys` are taken as distinct, as a listing's are. Only the
  result's length and contents are pinned, not whether an empty one is nil.
- **`TLA+:` lines** (ADR 0013 §6): `schedulePrune` keeps
  `StartPrune, PruneEnd`; `pruneSnapshots` keeps `PruneList, PruneDelete`;
  `prunable` gets `TLA+: docs/tla/CommitProtocol.tla PruneList`.
  `docs/tla/trace.tsv` gains the row for `prunable`, and the three prune
  actions' rows cite this section.
- **Logs** are not pinned. The Debug record `pruneSnapshots` makes after
  deleting may say what was deleted and what was kept.

### 3. What a pass keeps

- A *superseded* snapshot is one whose key sorts below the one the root
  pointer names: an older commit's, or a failed or losing attempt's at a
  lower key (Assumption 7).
- After a pass begun for K that no commit, crash or other pass disturbed,
  the bucket holds K, every key above K, and exactly the newest
  `min(retention, number below K)` keys below K. So `-snapshot-retention N`
  keeps N superseded snapshots, as its help text says, where before it kept
  at most N−1. The help text, and `README.md`'s sentence on pruning, stand.
- A key above K is kept until a pass is begun for a key above it. Such keys
  are a failed swap's orphan, a losing or unfinished attempt, or a lost-reply
  attempt that is in fact the root's. A mount prunes them one epoch later,
  once its root pointer has passed them; a mount that diverges leaves them
  until another mount commits past them (*Consequences*).
- A pass begun for an older K than the key the root pointer now names keeps
  more than `retention` keys below the current one; the next pass catches
  up. Two passes from two mounts together keep at least the `retention`
  newest keys below the higher K.
- `Config.SnapshotRetention`'s mapping in `New`, and the flag's default and
  its meaning for 0 and −1, do not change.

### 4. The model

In `docs/tla/CommitProtocol.tla`, under a `Revised: 2026-10-24` line:

- `Fixes` gains the member **`PruneBelowCurrent`** (`docs/vocabulary.md`,
  V3), and `AllFixes` lists it.
- `pruneCurrent[m]` holds the key the pass was begun for, in
  `Keys ∪ {NoKey}`, where it held that key's epoch; `StartPrune(m, k)` takes
  the key, `Committed` passes `k`, `Init` and `Crash` give `NoKey`, and
  `TypeOK` checks the new type.
- New definitions: `Below(L, k)`, the keys of `L` that sort below `k`, and
  `Prunable(L, k) == Below(L, k) \ Kept(Below(L, k))`, which is `prunable`
  under the vocabulary's rule 4.
- `PruneList` dooms `Prunable(L, pruneCurrent[m])` when the member is in
  `Fixes`, and otherwise what it doomed before, read through the key's epoch.
  Without the member the module describes the code at `13aebc4`, so the
  configurations that do not carry it, `CommitProtocol_PruneSingle` among
  them, check what they checked.
- `PassApartFromOwnCommits` compares `epoch[m]` with `pruneCurrent[m].epoch`.
- Assumptions J11, J14 and J15 are rewritten. **Failed `List` and `Delete`
  calls are not added** (J14, Assumption 10): a `List` or a `Delete` that
  fails deletes nothing; a `Delete` that takes effect and is sent again by
  `S3.do` deletes the same key; and no property of the module is harmed by a
  key that stays, since only a `Delete` can make `RootSnapshotExists` false.
  A failed `Delete` leaves a key below its pass's K for a later pass. So no
  existing MustHold configuration widens for them.
- **The eight configurations flip to MustHold**, `PruneBelowCurrent` added to
  each one's `Fixes`, their two invariants and every other line unchanged
  (ADR 0013 §7); each comment records the flip and the route it showed.
  `CommitProtocol_PruneTwoWriters` keeps its `CONSTRAINT`, and
  `CommitProtocol_PruneOverlapTwoWriters` is its twin with the same `Fixes`
  line, so the runner's twin rule holds (`docs/tla/README.md`, *Configuration
  files*).
- **`CommitProtocol_NoPruneBelowCurrent`**, MustFail on `RootSnapshotExists`,
  is `CommitProtocol_PruneOverlap` without the member: the configuration that
  removes the fix (ADR 0013 §7).
- **`CommitProtocol_PruneFaultsSingle`** and
  **`CommitProtocol_PruneTwoWritersFaults`**, MustHold on every property,
  widen the envelope J11 records: the bounds of `CommitProtocol` and of
  `CommitProtocol_TwoWriters` with `Retention` 1 and the member
  (Assumption 11).
- In `docs/tla/manifest.tsv` the eleven changed or new rows have `exit` and
  `log` `-` until the session has run them (ADR 0013 §7). The module's
  `Checked against` is `13aebc4`.

### 5. ADR 0018

- **`FS.pruning` is neither converted nor exempted here.** ADR 0018 §3 binds
  a change "that changes how existing state moves (its states, its events,
  its transitions, or the places that write it)". This fix changes none of
  those: the flag's two values, its set in `schedulePrune` and its clear in
  the goroutine, and the guard that reads it, are untouched. What changes is
  `schedulePrune`'s argument and `pruneSnapshots`' body, which §3 calls "the
  code beside it". So the flag stays in §3's *Not yet moved* list for #114's
  plan, and no exemption is granted (Assumption 9).
- **`prunable` is not a policy.** ADR 0018 §2's policy rule reaches "a
  decision that allows or refuses a well-formed request" of a caller. No
  caller asks for a snapshot's deletion: a pass is "a step the server takes
  on a store's answer", its own commit's success, which §2 keeps outside the
  rule. `prunable` is the named, pure, exhaustively tested decision that
  `docs/vocabulary.md` V7 expects of a commit-protocol decision, kept in
  `blobfs`, the one package that knows snapshot keys.
- **Nothing here is security-relevant** under ADR 0018 §5. No policy's
  request or gate reads `FS.pruning`; it records no caller's authority;
  `StartPrune`, `PruneDelete` and `PruneEnd` are caused by a store's answer,
  not by a request; and deleting a snapshot widens nothing a caller may do.
  So no transition the fix touches asks a policy.

### 6. The test surface

Every name is spelled as in Go. The tests are in package `blobfs`.

**Names a test may use**, restated from the code at `13aebc4` unless marked
new:

```go
// internal/blobfs
func New(ctx context.Context, cfg Config) (*FS, error)
type Config struct {
    Store              store.Store
    ChunkSize          uint32
    OwnerUID, OwnerGID uint32
    SnapshotRetention  int   // 0 selects the default, 10; negative turns pruning off
    MaxDirtyBytes      int64 // negative disables the budget's wait
    Log                *slog.Logger
    // ... fields this ADR does not use
}
func (f *FS) Sync(ctx context.Context) error
func (f *FS) Root() vfs.Handle
// Create, Lookup and the other vfs.FS methods, as internal/vfs/vfs.go declares them

// unexported
type FS struct {
    mu      sync.RWMutex
    pruning bool // set by schedulePrune when a pass begins; cleared by its goroutine's final hold
    // ...
}
type rootPointer struct {
    Version   int    `json:"version"`
    Epoch     uint64 `json:"epoch"`
    Snapshot  string `json:"snapshot"`
    FSID      uint64 `json:"fsid"`
    WrittenNS int64  `json:"written"`
    Writer    string `json:"writer,omitempty"`
}
const (
    rootKey        = "root"
    snapshotPrefix = "snapshots/"
)
// A snapshot key is snapshotPrefix + fmt.Sprintf("%020d-%s.json.gz", epoch, nonce),
// the nonce being twelve lower-case hex digits.

// new in this ADR (section 2)
func (f *FS) schedulePrune(current string)
func (f *FS) pruneSnapshots(ctx context.Context, current string) error
func prunable(keys []string, current string, keep int) []string
```

```go
// internal/store, unchanged
type Store interface {
    Get(ctx context.Context, key string) ([]byte, error)
    GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error)
    Head(ctx context.Context, key string) (ObjectInfo, error)
    Put(ctx context.Context, key string, data []byte) error
    Delete(ctx context.Context, key string) error // a missing key is not an error
    List(ctx context.Context, prefix, after string, max int) ([]ObjectInfo, error) // keys after `after`, in key order; max 0 means all
    PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) // etag "" means create-only
    Name() string
}
type ObjectInfo struct { Key string; Size int64; ETag string }
var ErrNotFound, ErrPrecondition, ErrUnsupported error
func NewLocal(root string) (*Local, error) // *Local implements Store; its PutIfMatch answers ErrPrecondition on a stale etag
```

A test may read `FS.pruning` holding `FS.mu` for reading; it is written only
under `FS.mu` for writing. A test may call `pruneSnapshots` and `prunable`
directly once the code exists. The route tests below use only names that
exist at `13aebc4`, so they fail at that commit rather than failing to build.

**The harness.** The route tests share a `store.Store` over a `*store.Local`
under `t.TempDir()`, which passes every call through, and, each one-shot when
armed:

- **holds the next `List`** whose prefix is `snapshotPrefix` and whose
  `after` is empty: it signals that it is held, waits until released or until
  the test's context ends, passes the call through, then signals that it has
  returned;
- **fails the next `PutIfMatch` of `rootKey`** with an error that is neither
  `store.ErrPrecondition` nor `store.ErrUnsupported`, without calling the
  inner store (a swap that is not applied: `SwapFailed`);
- **applies the next `PutIfMatch` of `rootKey`** on the inner store and
  returns such an error (a swap applied, its reply lost: `SwapReplyLost`);
- **holds the next `PutIfMatch` of `rootKey`**: signals, waits until
  released, then passes it through (a pending swap);
- **applies the next `Put` under `snapshotPrefix`** on the inner store and
  returns an error (`SnapshotReplyLost`);
- in its **unconditional mode**, answers every `PutIfMatch` with
  `store.ErrUnsupported` without calling the inner store, and can then
  **apply the next `Put` of `rootKey`** and return an error
  (`FallbackReplyLost`), or **hold the next `Put` of `rootKey`** until
  released (a pending fallback `Put`).

On every `Delete` under `snapshotPrefix` it reads the root pointer from the
inner store (`Get(rootKey)`, decoded as `rootPointer`) before passing the call
through, and records the key if `Snapshot` equals it. It serialises that
read-and-delete against every `PutIfMatch` and `Put` of `rootKey` it passes
through with one mutex, held only around the inner calls and never while a
hold waits. So the record holds exactly the keys a pass deleted while the
root pointer named them: the steps at which `RootSnapshotExists` is false. It
also signals the first `Delete` under `snapshotPrefix` it passes through. Two
mounts in one test share one harness. Below, a *commit* is a `Create` of a
fresh name in the root directory, by `vfs.Caller{UID: 501, GID: 20}`, then
`Sync`; *wait for the pass* is polling `FS.pruning` under `FS.mu` for reading
until it is false, under the test's bound; a key's *epoch* is the number in
it.

**The route tests.** Each starts `New` on the harness with
`SnapshotRetention` 1 (R6: 2), `MaxDirtyBytes` −1 and the List hold armed,
and waits for the hold's signal: the init commit's pass is listing. Each ends
by asserting that the record is empty; that `Get(rootKey)` on the inner store
decodes to a `rootPointer` whose `Snapshot` the inner store holds; and that
`New` on the inner store with `SnapshotRetention` −1 succeeds (the remount).
Each fails at `13aebc4` as its row says.

- **R1, `CommitProtocol_PruneOrphan`**
  (`TestPruneKeepsRootSnapshotAfterFailedSwap`). Commit (epoch 2). Arm the
  failing swap; commit: `Sync` returns an error, the bucket holds an epoch-3
  key, and the root pointer names the epoch-2 one. Release the List; wait
  for the pass. At `13aebc4` the pass keeps the epoch-3 key and deletes the
  epoch-2 one: the record holds it, and the remount fails. The
  configuration's route has its orphan at epoch 2, above the retry by nonce;
  the test puts the failed swap at epoch 3, since nonces are random
  (Assumption 12).
- **R2, `CommitProtocol_PruneTwoWriters`**
  (`TestPruneKeepsRootSnapshotWithTwoWriters`). B is `New` on the same
  harness with `SnapshotRetention` −1. Commit on B (epoch 2; the root pointer
  is B's). Commit on A: `Sync` returns an error, A's swap having failed its
  condition, and A's epoch-2 key is in the bucket. Seed, on the inner store,
  the key `snapshotPrefix + "00000000000000000002-ffffffffffff.json.gz"` with
  the bytes of B's epoch-2 snapshot: the object an attempt at epoch 2 with
  the highest nonce leaves, standing for A's attempt sorting above B's, as
  the configuration's route has it (Assumption 12). Release the List; wait
  for A's pass. At `13aebc4` the pass keeps the seeded key and deletes B's:
  the record holds it.
- **R3, `CommitProtocol_PruneOverlap`**
  (`TestPruneKeepsRootSnapshotWhenListingOverlapsCommits`). Commit
  (epoch 2). Arm the swap hold; start a commit in a goroutine; wait for the
  hold's signal: an epoch-3 key is in the bucket, and the root pointer names
  the epoch-2 one. Release the List; wait for the List to return; wait for
  the first `Delete` or the grace period; release the swap: `Sync` returns
  nil, and the root pointer names the epoch-3 key. Wait for the pass. At
  `13aebc4` the pass deletes the epoch-2 key while the root pointer names
  it: the record holds it.
- **R4, `CommitProtocol_PruneOverlapTwoWriters`**
  (`TestPruneKeepsRootSnapshotWhenAnotherMountsAttemptIsPending`). Commit on
  A (epoch 2). B is `New` on the same harness with `SnapshotRetention` −1,
  after that commit. Arm the swap hold; start a commit on B in a goroutine;
  wait for the hold's signal: B's epoch-3 key is in the bucket, and the root
  pointer names A's epoch-2 one. Release the List; wait for the List to
  return; wait for the first `Delete` or the grace period; release the swap:
  B's `Sync` returns nil. Wait for A's pass. At `13aebc4` A's pass deletes
  A's epoch-2 key while the root pointer names it. The configuration's route
  reaches this shape through #89's window and a nonce order; the test uses
  B's attempt at the next epoch instead (Assumption 12).
- **R5, `CommitProtocol_PruneSnapshotReply`**
  (`TestPruneKeepsRootSnapshotAfterLostSnapshotReply`). As R1, but arm the
  applied-with-error snapshot `Put` instead of the failing swap: `Sync`
  returns an error from writing the snapshot, the epoch-3 key is in the
  bucket, and no swap was attempted. The same assertions, and the same
  failure at `13aebc4`.
- **R6, `CommitProtocol_PruneSnapshotReplyRetention2`**
  (`TestPruneKeepsRootSnapshotAtRetentionTwo`). `SnapshotRetention` 2.
  Commit (epoch 2). Arm the applied-with-error snapshot `Put`; commit: `Sync`
  returns an error, and an epoch-3 key is in the bucket. Arm the swap hold;
  start `Sync` again in a goroutine (the namespace is still dirty); wait for
  the hold's signal: a second epoch-3 key is in the bucket. Release the List;
  wait for the List to return; wait for the first `Delete` or the grace
  period; release the swap: `Sync` returns nil. Wait for the pass. At
  `13aebc4` the pass keeps both epoch-3 keys and deletes the epoch-2 one
  while the root pointer names it.
- **R7, `CommitProtocol_PruneSwapReply`**
  (`TestPruneKeepsRootSnapshotAfterLostSwapReply`). Arm the
  applied-with-error swap; commit: `Sync` returns an error, but the root
  pointer names the epoch-2 key. Seed the epoch-2 key with nonce
  `ffffffffffff`, as in R2, from the bytes of the root pointer's snapshot:
  the retry's snapshot sorting above (Assumption 12). `Sync` again: it
  returns an error, the swap having failed its condition on the stale ETag,
  and the bucket holds a second epoch-2 key of the mount's own. Release the
  List; wait for the pass. At `13aebc4` the pass keeps the seeded key and
  deletes the root pointer's.
- **R8, `CommitProtocol_PruneFallbackReply`**
  (`TestPruneKeepsRootSnapshotAfterLostFallbackReply`). The harness is in
  unconditional mode from the start, so `New`'s init commit goes through the
  fallback. Arm the applied-with-error root `Put`; commit: `Sync` returns an
  error, but the root pointer names the epoch-2 key. Seed as in R7. Arm the
  root `Put` hold; start `Sync` again in a goroutine; wait for the hold's
  signal: the retry's epoch-2 key is in the bucket. Release the List; wait
  for the List to return; wait for the first `Delete` or the grace period;
  release the root `Put`: `Sync` returns nil, and the root pointer names the
  retry's key. Wait for the pass. At `13aebc4` the pass deletes the key the
  root pointer named before the retry's `Put`.
- **R9, the count** (`TestSnapshotRetentionKeepsExactlyNBelowCurrent`). On a
  plain `*store.Local`, `New` with `SnapshotRetention` 3; five commits
  (epochs 2 to 6); wait for the pass; one more commit (epoch 7); wait for the
  pass. The keys under `snapshotPrefix` are exactly those of epochs 4, 5, 6
  and 7, and the remount succeeds. At `13aebc4` the last pass keeps three
  keys in all, epochs 5 to 7 (Assumption 17).

**The decision function**, once the code exists
(`TestPrunableDecidesWhatAPassDeletes`):

- **P1.** No key below `current`, or at most `keep` of them: a result of
  length 0.
- **P2.** More than `keep` below: the oldest `len(below) − keep`, ascending,
  whatever order `keys` came in.
- **P3.** `current` itself and every key above it are never in the result;
  a key at `current`'s epoch with a lower nonce is below it.
- **P4.** `keep` 0 returns every key below `current`; a negative `keep` acts
  as 0.
- **P5.** `keys` is unchanged by the call.
- **P6.** Exhaustively, for a universe of at least five snapshot keys
  spanning three epochs with two nonces, every subset of it in a shuffled
  order, every `current` in the universe plus one key below all of them and
  one above all of them, and every `keep` from −1 to the universe's size:
  the result equals an oracle written from §2.

**Safety rules**, as ADR 0011 §7 and ADR 0012 §9 give them, with the one
change this ADR's subject needs:

- every `New` and every FS call runs in a goroutine that recovers a panic
  and is waited for under a bound of 10 s; goroutines never touch `t`;
  nothing is parallel;
- every hold the harness takes selects on the test's context, which cleanup
  cancels, so no build can hang the suite;
- `MaxDirtyBytes` is −1 on every mount, and no `Write` is made, so nothing
  drains the budget or starts a `Sync` of its own;
- no sleep except polling with a deadline for `FS.pruning`, and the one fixed
  grace period, 2 s, used only as the limit on waiting for a `Delete` that
  the fixed code never makes (Assumption 13);
- every bucket is under `t.TempDir()`;
- ADR 0012 §9's rule that every committing mount has `SnapshotRetention` −1
  is replaced here by: a test whose mount prunes waits, under its bound, for
  `FS.pruning` to be false before it returns, so that no pass outlives the
  test; and every other mount in the test, the remount included, has −1;
- the bucket is inspected through the inner `*store.Local` after the
  harness's record has been read, never through the harness.

**What only review can show:**

- that `commitLocked` passes `key` to `schedulePrune` on both success paths;
- that `pruneSnapshots` makes no store call but `List` and `Delete`, and
  deletes nothing outside `prunable`'s result;
- that `prunable` is pure;
- that the doc comments say what §1 says, and the "by construction" sentence
  is gone;
- the `TLA+:` line on `prunable`, and that `prunable` and `Prunable` agree
  (ADR 0013 §8);
- that `schedulePrune`'s guard and its writes of `FS.pruning` are unchanged
  (§5).

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation**, under the owner's 2026-10-10 words, as
   ADRs 0015 to 0018 were. The design is mine: the brief asked for a fix that
   closes the eight routes without resting on a retention above 1, and chose
   none.
2. **The rule's form** (§1): delete only below the committed key, by key
   order, with no read of the root pointer. The argument from the root
   pointer's key only rising is mine; it rests on ADR 0013 §4's per-key
   atomicity and on `loadOrInit` and `commitLocked` taking the epoch and the
   ETag from one root pointer, which I checked at `13aebc4`.
3. **Key order, not epoch order** (§1, §2). A pass could spare every key at
   the root's epoch and delete only lower epochs; both are safe. Key order is
   what `pruneSnapshots` already sorts by and `KeyLess` already models, and
   it prunes same-epoch orphans one epoch sooner.
4. **The load race is #89's** (§1). Within the eight configurations' bounds I
   traced every behaviour in which a mount's attempt can sort below the root
   pointer's key and still be swapped in, and none deletes it: with
   `Nonces = {1, 2}` and `Retention` 1 such an attempt is the newest key
   below the root and so is kept. Beyond those bounds, with three nonces, the
   residual exists, and `AtomicLoad` removes it. TLC, not this tracing, is
   the check; the session's runs decide. ADR 0020 §4 traced the residual
   exactly: it needs a key between the swapped-in one and the pass's, so
   three nonces *and* a failed attempt, or a third mount, and not three
   nonces alone. `CommitProtocol_NoAtomicLoadPrune` shows it: TLC reported a
   route in which the orphan is the pruning writer's own failed attempt and
   the pass deletes the other writer's snapshot before that writer's swap
   names it, and ADR 0020 §4 records the hand-traced route beside it.
   `AtomicLoad` closes both.
5. **No read of the root pointer in the pass** (§2). The committing mount
   knows the key the root pointer names at the instant the pass begins, so a
   `Get` would add a call whose failure needs a rule, for no safety; and a
   key read later is only higher, which the rule already tolerates by
   keeping more.
6. **`prunable`'s name and shape** (§2): a function of `[]string`, the key
   and the count, returning the keys to delete in ascending order, so that it
   can be tested exhaustively without an `FS`. It takes `keep` as an argument
   rather than reading `FS.retention` for the same reason. A negative `keep`
   as 0, and a length-0 result rather than a pinned nil, are mine; `FS` never
   calls it with `keep` 0, since `schedulePrune` returns first.
7. **What "superseded" means** (§3): below the root pointer's key, orphans
   included. The help text is kept rather than reworded, since an orphan
   below the root is a complete snapshot of a namespace that lost a race, and
   "superseded" fits it; counting only keys that were once the root's would
   need the pass to know a history it does not have.
8. **Keys above K are left to a later pass** (§3). A pass could re-read the
   root pointer and prune up to its newer key, at the cost of Assumption 5's
   call. The garbage is bounded by the attempts above the root at any time,
   and a diverged mount's leftovers are the price of #5 being open.
9. **ADR 0018 §3's reading** (§5): "the places that write it" names the set
   of writers, and editing a writer's other lines is editing the code beside
   the state. If review reads it otherwise, the right answer is an exemption
   as "a flag with a single rule", which §1's examples name, not a two-state
   table that refuses nothing the guard does not; granting it is left to
   #114's plan, since this change does not need it.
10. **Not adding failed `List` and `Delete` to the model** (§4), for the
    reason §4 gives. The cost is that nothing checks the garbage a failed
    `Delete` leaves, which no property states.
11. **The two new MustHold configurations' bounds** (§4) are those of
    `CommitProtocol` and `CommitProtocol_TwoWriters` with `Retention` 1, so
    that their run times are near known ones; the two-writer one is tier
    `full`, the other `smoke`. If either runs too long, the session reports
    it and the bounds shrink in a later dispatch.
12. **Where the Go tests deviate from the traces** (§6). Nonces come from
    `crypto/rand`, so a test cannot make one attempt's key sort above
    another's at the same epoch. Where a trace needs that (R2, R7, R8), the
    test seeds the object such an attempt leaves, under the highest nonce;
    where the trace can be moved one epoch up with the same shape (R1, R5),
    it is; R4 reaches the trace's shape without #89's window, which is that
    issue's to reproduce. Each test says so in its comment.
13. **The grace period** (§6), 2 s, is used only to let a build without the
    fix show its `Delete` before the held swap moves the root pointer; the
    fixed code waits it out. ADR 0012 §9 allows one such period as a limit.
14. **Not breaking.** No deployment needs to act: the format, the flags and
    the wire are unchanged, and a bucket an earlier build pruned is served as
    it is.
15. **Read, not run.** Every statement about the code was checked by reading
    it at `13aebc4`, and nothing was run: not TLC, not `go test`. The flips
    are predictions that the session's TLC runs confirm or refute.
16. **The names.** `PruneBelowCurrent` says what the pass does; the title
    says it of the commit's snapshot. Neither was given.
17. **The retention test's shape** (R9) waits for `FS.pruning` between the
    last two commits so that the final pass is not skipped; without that, a
    pass still running would make the end state depend on timing.

## Alternatives considered

- **Re-read the root pointer after listing, and spare the key it names.** It
  closes routes 1, 3, 4 and 5, but a pending attempt whose swap lands between
  the read and the delete is still deleted, which route 2 reaches with no
  fault at all, and re-reading before each delete only narrows that window.
  It also costs a `Get` per pass.
- **A retention above 1.** `CommitProtocol_PruneSnapshotReplyRetention2`
  shows the window fills at any count.
- **Spare every key at or above the root pointer as re-read.** Safe for the
  same reason as the rule, and it prunes a little more when the pass is
  stale; but the committing mount already knows the key, so the read buys
  little and adds a failure to handle (Assumption 5).
- **Conditional deletes.** S3 has no condition on another key: "There is no
  way to make atomic updates across keys" (ADR 0013's *Sources*).
- **Mark pending attempts**, in the root pointer or a side object, so a pass
  can skip them. More protocol and a format change, for nothing once the root
  pointer's key is known to rise.
- **Run the pass inside the commit, under `FS.mu`.** Every commit would wait
  on a listing and up to `retention` deletes, which `commitLocked`'s comment
  moved off the commit path for a reason.
- **Prune by epoch only** (Assumption 3).
- **Convert `FS.pruning` to an `fsm.Machine` in this PR.** Not required
  (§5), and a two-state table refuses nothing the guard does not.

## Consequences

- No prune pass deletes the snapshot the root pointer names, at any
  retention, with failed swaps, lost replies, overlapping commits and a
  second writer, within the envelope §1 states; the eight configurations
  hold, and two wider ones join them.
- `-snapshot-retention N` keeps N superseded snapshots, where it kept at most
  N−1.
- Snapshots above the current one, a failed attempt's or a losing writer's,
  are kept one epoch longer than before, and a diverged mount's last attempt
  stays until another mount commits past it. The bucket holds the current
  snapshot, `retention` below it, and whatever sorts above.
- A pass begun by an older commit prunes less; the next catches up. No pass
  makes an extra store call.
- A lost root pointer's higher-epoch snapshots (#79) survive a new
  filesystem's early passes, which delete only below their own key, until
  the new filesystem's epoch passes them.
- `prunable` is the one place that decides what a pass deletes, tested apart
  from any `FS`.
- Hard to change later: the key order as the deletion order, and the rule
  that a pass never reaches above its own commit's key, which later tooling
  for #79 and #10 can rely on.

## What this does not decide

- Repairing a bucket whose root pointer names a deleted snapshot, or has been
  lost (#79, #10). This ADR only stops making more of them.
- The lost-reply and 409 routes of #5, whose InDoubt resolution is its own
  ADR, and the snapshots such a mount leaves above the root (§3).
- `AtomicLoad` for #89, and the residual §1 names until it lands. Decided by
  ADR 0020.
- Whether `FS.pruning` becomes a machine or an exemption under #114 (§5,
  Assumption 9).
- Chunk garbage collection (`docs/DESIGN.md` §8), and whether the redesign
  keeps a retention count at all.
- A tool or a log record that reports the orphans a bucket holds.

## Sources

Nothing was fetched for this ADR. It cites the repository's code and
documents, and relies on two statements that ADR 0013's *Sources* quote from
AWS's documentation: that updates to a single key are atomic, and that there
is no way to make atomic updates across keys.
