# Vocabulary

This file holds the one set of names that the Go state machines, the policies
and the TLA+ specifications in [`docs/tla/`](tla/README.md) share, so that a
state in a log line, a row of `docs/tla/trace.tsv` and a step of a
counterexample read the same.
[ADR 0013 §5](adr/0013-data-integrity-protocols-are-specified-in-tla.md)
decides that it exists and sets its rules, which are restated below; where the
two disagree, the ADR wins and the disagreement is a bug in one of them. A
later ADR that adds a machine adds its names here. A rename changes this file,
the specs and the Go names in one PR.

Every entry has a status (ADR 0013 §5):

- **exists**: its Go name is in the code now, or, for a `Fixes` member, the fix
  it names is. Every entry marked exists was confirmed in the code at
  `043927f`, or at the later commit its section or its entry names.
- **reserved**, with the ADR named: it is not in the code, and that ADR
  introduces it. A name only the specs use, with no Go name, such as an
  action, a ghost, a property or a string a variable holds, is reserved by
  ADR 0013, which introduces it, unless its entry names another ADR.

A status given to a group, a list or a section holds for every entry in it.
The lifecycle ADR and the ADR for #5 are not yet written; their entries hold
the names those ADRs will use. The load-race ADR is ADR 0020. The operators of
the standard modules, listed at the end, are not entries: they are the names
rule 1 rules out.

## Rules

1. A bare name is UpperCamel ASCII letters and digits starting with a letter;
   never a Go keyword or predeclared identifier, a TLA+ reserved word, or an
   operator of `Naturals`, `FiniteSets`, `Sequences` or `TLC`.
2. Go constants are the machine's lowerCamel prefix plus the bare name;
   `String()` returns the bare name, so log from/to/event values are the
   spec's names.
3. TLA+ holds a machine's state as the string of its bare name, in a variable
   named after the Go field that holds it; actions and modelled events are
   operators with the bare name. A machine is a set of states with Go
   constants under rule 2. Any other variable is named as this file gives,
   with the Go symbol it traces to, if any.
4. A Go predicate a spec uses keeps its name with the first letter
   upper-cased.
5. States may repeat across machines; events and actions are unique within a
   module. An action that is the occurrence of an event shares the event's
   name and is one operator: `SwapOK` below is both (ADR 0013, Assumption 14).
6. `Fixes` members follow rule 1 and say what the fix does; the ADR or commit
   goes in the config comment.
7. For actions the Go column is indicative; `trace.tsv` is authoritative once
   it exists.

**Why rule 3 covers only machine states.** The machines are V1, V2 and V5's
server and connection, whose states have Go constants. Rule 3 was narrowed to
them, rather than kept for every variable with a list of exceptions. A
machine's state is what a log line carries (rule 2), and the rule exists so
that it reads the same there. No other variable, a bool such as `FS.dirty`, a
lock, a function's locals, the bucket's objects or a ghost, appears in a log
as a state, and the tables below give the Go symbol each one traces to. Kept
for every variable, the rule would have needed ten of V3's and V4's variables
as exceptions, about half of those that trace to Go, and more as specs add
variables (ADR 0013, Assumption 13). So `pruning[m]`'s three strings, which
refine the bool `FS.pruning`, are no machine's states, and its row records
them.

## Go machines

Every Go machine is an `internal/fsm` table (ADR 0014 §2, §3), named after the
field that holds its state, as rule 3 names its TLA+ variable. That name is
the `machine` value in log lines (ADR 0014 §9).

## V1. Mount mode

Go types `mountMode` and `mountEvent`, in `internal/blobfs/mode.go`. Table
`mountModes`, named `"mode"`, which is the `machine` value in log lines and
the `Machine` of an `fsm.IllegalError`. Field `FS.mode`, an
`*fsm.Atomic[mountMode, mountEvent]`, read without a lock, which ADR 0014
§7's conversion put in place of `FS.readOnly` and `FS.diverged`. TLA+
`mode[m]`.

| Name | Kind | Go | TLA+ | Meaning | Status |
|---|---|---|---|---|---|
| `Writable` | state | `mountWritable` | `"Writable"` | accepts changes | exists, with ADR 0014 §7's conversion (the note below) |
| `ReadOnly` | state | `mountReadOnly` | `"ReadOnly"` | `Config.ReadOnly`; no edge leaves it | exists, with ADR 0014 §7's conversion (the note below) |
| `Diverged` | state | `mountDiverged` | `"Diverged"` | lost a swap; terminal | exists, with ADR 0014 §7's conversion (the note below) |
| `Closed` | state | `mountClosed` | `"Closed"` | final commit done | reserved, the lifecycle ADR |
| `Diverge` | event | `mountDiverge` | `Diverge(m)` | `Writable` to `Diverged` | exists, with ADR 0014 §7's conversion (the note below) |
| `Close` | event | `mountClose` | `Close(m)` | its edges are the lifecycle ADR's | reserved, the lifecycle ADR |
| `Mutable` | predicate | `(*FS).mutable` | `Mutable(m)` | nil exactly when `Writable` | exists, `internal/blobfs/fs.go` |

Before the conversion, at `043927f`, the mode was `FS.readOnly` and
`FS.diverged`, and `commitLocked` set `FS.diverged` on any
`store.ErrPrecondition`, a read-only mount's included; the module held that
as `"Diverged"` either way, so its `Diverge(m)` could leave `ReadOnly`, which
this table does not allow. The conversion settled it as the table has it:
`Diverge(m)` moves only a `"Writable"` mount and leaves any other mode as it
is, since `mountModes` has that one edge and Go's `Fire` refuses the rest
(the module's R11 and its `Revised` line of 2026-10-27). Only a read-only
mount without `store.ReadOnly` beneath it could reach the case, so only a
configuration without `ReadOnlyFence` does. The four entries marked exists
were confirmed against ADR 0014 §7 as revised on 2026-10-27, which the
conversion's PR implements, and in the code at `faeba5b`:
`internal/blobfs/mode.go` declares `mountWritable`, `mountReadOnly`,
`mountDiverged` and `mountDiverge`, whose `String` values are `"Writable"`,
`"ReadOnly"`, `"Diverged"` and `"Diverge"`, and the table `mountModes` with
its one edge, and `internal/blobfs/fs.go` holds `FS.mode` (ADR 0014,
Assumption 41).

## V2. Root-pointer knowledge

Go field `FS.rootKnowledge`, under `FS.mu`, of a type its ADR names; TLA+
`rootKnowledge[m]`. All reserved, by the ADR for #5.

| Name | Kind | Go | Meaning |
|---|---|---|---|
| `Known` | state | `rootKnown` | `FS.rootETag` is the root's ETag as last read or written (`""` = no root) |
| `InDoubt` | state | `rootInDoubt` | a swap's outcome is unknown |
| `SwapOK` | event | `rootSwapOK` | `Known` to `Known` |
| `SwapUnknown` | event | `rootSwapUnknown` | `Known` to `InDoubt`, after `SwapReplyLost` or `SwapFailed` |
| `ProbeOurs` | event | `rootProbeOurs` | `InDoubt` to `Known`: the root names this mount's attempt; adopt it |
| `ProbeUnchanged` | event | `rootProbeUnchanged` | `InDoubt` to `Known`: the root is unchanged, so the swap was not applied |
| `ProbeTheirs` | action | none | another writer's root: fires `mountDiverge`, and stays `InDoubt` |

## V3. Commit protocol

`docs/tla/CommitProtocol.tla`, checked against `faeba5b`. Its Go sites exist:
`Sync`, `loadOrInit`, `initEmpty`, `decodeSnapshot`, `commitLocked`,
`fallbackAllowed`, `schedulePrune` and `pruneSnapshots` in
`internal/blobfs/commit.go`; `New`,
`mutable`, `SetAttr`, `createEntry`, `unlink`, `Rename`, `bufferWrite`,
`truncate`, `flushAll`, `settleAll`, `flushOpen` and `putChunk` in
`internal/blobfs/fs.go`; `ReadOnly.Put` and `ReadOnly.PutIfMatch` in
`internal/store/store.go`; `S3.Put`, `S3.PutIfMatch`, `S3.GetWithETag` and
`errorFor` in `internal/store/s3.go`; and `Local.PutIfMatch` and
`Local.GetWithETag` in `internal/store/local.go`.
`docs/tla/trace.tsv` gives each action's symbols. Of the entries ADR 0019
added, `PruneBelowCurrent`, `Prunable`, and `prunable` in V7 exist, as of
`cc38ca6`, `prunable` in `internal/blobfs/commit.go`; `Below`, which only the
spec uses, is reserved by that ADR. ADR 0020 added no entry: `AtomicLoad`,
which it lands, was reserved here already and exists as of `c5068a0`;
`GetWithETag`, the `store.Store` method that `GetRoot` traces to, is a Go
symbol `trace.tsv` names, not an entry. ADR 0021 added `FallbackOptIn` and
`FallbackAllowed`: `FallbackAllowed` exists as of `85dbfcf`, and
`FallbackOptIn`, whose refusal branch includes the reclaim ADR 0021's revisions
added, with its guard, as of `8415f61`; the Go names behind them,
`Config.AllowUnconditionalRoot`, `FS.allowUnconditionalRoot` and
`(*FS).fallbackAllowed`, are what `FallbackAllowed` traces to.

**Actions.** All reserved, ADR 0013: an action's name is the spec's, whatever
Go performs it. The Go column is indicative, and `trace.tsv` is authoritative
(rule 7).

| Action | Go | Meaning |
|---|---|---|
| `Init` | `New` | the initial state. `New` makes `FS.mode` at `InitialMode(m)`, `ReadOnly` from `Config.ReadOnly` and `Writable` otherwise (ADR 0014 §7), puts a read-only mount's store behind `store.ReadOnly`, and sets `FS.allowUnconditionalRoot` from `Config.AllowUnconditionalRoot`, which `FallbackAllowed` stands for (ADR 0021 §4). With `Start` `"Empty"` every mount is at `loadOrInit`'s first read; with `"Loaded"` every mount has loaded a filesystem at epoch 1 |
| `GetRoot` | `loadOrInit`'s read of the root pointer: `GetWithETag(rootKey)` with `AtomicLoad`, `Get(rootKey)` without it | reads the root pointer. With `AtomicLoad` the body and its ETag come from one response, and the ETag becomes `rootETag[m]` here (ADR 0020 §2, §4); without it the ETag waits for `HeadRoot` |
| `HeadRoot` | `loadOrInit`'s `Head(rootKey)`, without `AtomicLoad` only | the load-race window (#89), which `AtomicLoad` closes (ADR 0020): with the member the action is never enabled, and no Go symbol performs it, so `trace.tsv` has no row for it |
| `GetSnapshot` | `loadOrInit`'s `Get` of the snapshot the root names, and `decodeSnapshot` | a missing snapshot makes `New` fail, and `pc[m]` becomes `"Failed"` |
| `RefuseEmpty` | `loadOrInit`'s read-only branch | a read-only mount refuses a bucket with no root (ADR 0011 §2) |
| `InitEmpty` | `initEmpty` | then `PutSnapshot` and an If-None-Match swap |
| `Mutate` | any namespace-changing `vfs.FS` method | guarded by `Mutable` |
| `StrayDirty` | none | a path that dirties a read-only mount: ADR 0011 §7's seam, through which a test sets `FS.dirty` on a read-only FS. No path does so at `043927f` |
| `Flush` | `Sync`'s `flushAll` and `settleAll`; `flushOpen`; `putChunk` | a step of its own only without `ChunksFirst`; with it, part of `PutSnapshot` |
| `PutSnapshot` | `commitLocked`'s snapshot `Put` | refused by `store.ReadOnly` when the mount is fenced; otherwise it reaches the bucket, and is applied, not applied (`"PutFailed"`), or applied with an error returned (`"SnapshotReplyLost"`) |
| `SwapOK` | `commitLocked`'s `PutIfMatch` | applied, and the reply carries the ETag |
| `SwapReplyLost` | the same | applied, and an error is returned |
| `SwapFailed` | the same | not applied, and an error is returned |
| `SwapConflict` | the same | `store.ErrPrecondition`; fires `Diverge` (V1), which moves only a `"Writable"` mount (the module's R11) |
| `SwapUnsupported` | the same, and `fallbackAllowed` | `store.ErrUnsupported`. Unless `FallbackAllowed`, `commitLocked` reclaims the attempt's snapshot, which no root pointer names, once a read of the root pointer has confirmed that, and returns an error (ADR 0021 §2; the module's R12). Otherwise `FallbackPut` follows, which `store.ReadOnly` refuses when the mount is fenced, and which otherwise is applied, not applied (`"PutFailed"`), or applied with an error returned (`"FallbackReplyLost"`) |
| `FallbackPut` | `commitLocked`'s unconditional root `Put` after it | merged with `SwapUnsupported`: an operator of that action's fallback branches, taken only where `FallbackAllowed` (ADR 0021 §4), not a step of its own |
| `FallbackHead` | the `Head` after `FallbackPut` | only where `FallbackAllowed` |
| `StartPrune` | `schedulePrune`'s guard, and its setting of `FS.pruning` | an operator that `SwapOK` and `FallbackHead` conjoin, since it runs in the same hold of `FS.mu`; not a step of its own. The pass is begun for the key the commit swapped the root pointer to (ADR 0019 §2) |
| `PruneList` | `pruneSnapshots`' `List` loop, then `prunable` | with `PruneBelowCurrent`, what the pass deletes is `Prunable` (ADR 0019 §2) |
| `PruneDelete` | `pruneSnapshots`' `Delete` loop | one `Delete` per step, in ascending key order |
| `PruneEnd` | the final hold of `FS.mu` in `schedulePrune`'s goroutine, which clears `FS.pruning` | |
| `Crash` | none; the restart is `New` | process death, or any exit skipping the final commit. The mount's memory and its prune goroutine are lost, and it restarts at `GetRoot` |

**Variables.** Named as rule 3 says: `mode[m]` and `rootKnowledge[m]` hold
machine states, and the rest are named here. CommitProtocol leaves
`rootKnowledge[m]` out until the ADR for #5 adds it.

| Variable | Go | Meaning | Status |
|---|---|---|---|
| `bucketRoot` | the object at `rootKey` | `[key, etag, writer]`: `rootPointer.Snapshot`, its ETag, `rootPointer.Writer` | exists |
| `snapshots` | the objects under `snapshotPrefix` | a function from the keys present to sets of commit ids: a key's set stands for that snapshot's `snapshot.Inodes`, as the commits it includes | exists |
| `chunks` | the objects under `chunkPrefix` | a set of commit ids: a commit is in it once its chunk is stored | exists |
| `pc[m]` | none | where `New`, `loadOrInit` or `commitLocked` has got to, as one of the strings below | reserved, ADR 0013 |
| `mode[m]` | `FS.mode` (V1) | the mode's state, as `FS.mode.State()` gives it. Before ADR 0014 §7's conversion, at `043927f`, it stood for `FS.readOnly` and `FS.diverged`: `"Diverged"` while `FS.diverged` was set, `"ReadOnly"` while only `FS.readOnly` was, and `"Writable"` otherwise (V1's note) | exists, with ADR 0014 §7's conversion (V1's note) |
| `rootKnowledge[m]` | `FS.rootKnowledge` (V2) | | reserved, the ADR for #5 |
| `rootETag[m]` | `FS.rootETag` | | exists |
| `epoch[m]` | `FS.epoch` | | exists |
| `nsDirty[m]` | `FS.dirty` | | exists |
| `attempt[m]` | `commitLocked`'s `key` | the key of the commit attempt in progress, from its first write that takes effect until the attempt ends; `NoKey` otherwise | exists |
| `rpSnapshot[m]` | `loadOrInit`'s `rp.Snapshot` | the snapshot key the root pointer named when `GetRoot` read it, until `GetSnapshot`; `NoKey` otherwise | exists |
| `base[m]` | `FS.inodes` | the set of commit ids the namespace in memory includes | exists |
| `pruning[m]` | `FS.pruning` | in {`"Idle"`, `"Listing"`, `"Deleting"`}, which refine the bool: `FS.pruning` is true unless `"Idle"`. The three strings are reserved, ADR 0013 | exists |
| `pruneCurrent[m]` | `schedulePrune`'s and `pruneSnapshots`' `current` | the snapshot key the pass's commit swapped the root pointer to, or `NoKey` while no pass has begun since `New` (ADR 0019 §2). At `13aebc4` Go's `current` is that key's epoch, which is all the pass then uses | exists |
| `pruneDoomed[m]` | `pruneSnapshots`' `doomed` | the keys the pass has still to delete. With `PruneBelowCurrent` it is `Prunable`, which `prunable` computes; without it, `doomed` less the keys of the current epoch, which Go at `13aebc4` skips in its `Delete` loop and the spec at `PruneList` | exists |
| `acked` | none: a ghost | the commits whose `commitLocked` returned nil, from `Sync` or from `initEmpty` | reserved, ADR 0013 |
| `used` | none: a ghost | the snapshot keys named by a snapshot `Put` or a write of the root pointer that took effect. A fresh key is one not in it, which is how the spec takes the nonce never to repeat (ADR 0013 §4) | reserved, ADR 0013 |
| `overtaken[m]` | none: a ghost | another mount has written the root pointer since this mount last set `rootETag[m]` | reserved, ADR 0013 |
| `roWrites` | none: a ghost | the read-only mounts from which a `Put`, `PutIfMatch` or `Delete` call has reached the bucket, whether or not it changed an object; a call `store.ReadOnly` refuses reaches nothing | reserved, ADR 0013 |
| `crashes` | none | how many `Crash` steps have been taken, at most `MaxCrashes` | reserved, ADR 0013 |

**`pc[m]`'s strings.** All reserved, ADR 0013. Like `pruning[m]`'s, they are no
machine's states (rule 3).

| String | Meaning |
|---|---|
| `"GetRoot"`, `"HeadRoot"`, `"GetSnapshot"` | the read of `loadOrInit` that the action of that name makes is due; `"HeadRoot"` is reached only without `AtomicLoad` (ADR 0020 §4) |
| `"Ready"` | loaded, with no commit in progress; `NextStep(m)` says whether one is due |
| `"PutSnapshot"` | a commit's snapshot `Put` is due: `initEmpty`'s first, or without `ChunksFirst` any commit's, after its swap |
| `"Swap"` | a commit's swap is due, after its snapshot `Put`, or without `ChunksFirst` as `initEmpty`'s first call. `SwapOK`, `SwapReplyLost`, `SwapFailed`, `SwapConflict` or `SwapUnsupported` takes it |
| `"FallbackHead"` | the `Head` after the fallback's `Put` is due; reached only where `FallbackAllowed` (ADR 0021 §4) |
| `"Flush"` | without `ChunksFirst`, a commit's flush is due, after its swap and its snapshot `Put` |
| `"Failed"` | `New` returned an error; only `Crash` leaves it |

**Constants.** Every configuration assigns all ten (`docs/tla/README.md`).

| Constant | Values | Go | Meaning | Status |
|---|---|---|---|---|
| `Mounts` | a set of strings, not including `""` | `FS.writerID`, which `rootPointer.Writer` records | the mount processes | exists |
| `ReadOnlyMounts` | a subset of `Mounts` | `Config.ReadOnly` | the mounts that have it set | exists |
| `Fixes` | a subset of the `Fixes` members below | none | the fixes a configuration includes (ADR 0013 §7) | reserved, ADR 0013 |
| `Capability` | `"Conditional"`, `"Unconditional"` or `"Flaky"` | none | what the backend does with a conditional write (below) | reserved, ADR 0013 |
| `Faults` | a subset of `"SwapReplyLost"`, `"SwapFailed"`, `"SwapConflict409"`, `"PutFailed"`, `"SnapshotReplyLost"` and `"FallbackReplyLost"` | none | the failure outcomes the bucket may give (below) | reserved, ADR 0013 |
| `Start` | `"Empty"` or `"Loaded"` | none | the bucket as a behaviour begins (below) | reserved, ADR 0013 |
| `Retention` | a natural number | `FS.retention` | how many snapshots a prune pass keeps; 0 turns pruning off | exists |
| `MaxEpoch` | a positive natural number | none | a bound: the highest epoch a commit may reach | reserved, ADR 0013 |
| `Nonces` | a non-empty set of positive natural numbers | none | a bound: the nonces a snapshot key may carry, standing for `commitLocked`'s six random bytes; nonce 0 means none | reserved, ADR 0013 |
| `MaxCrashes` | a natural number | none | a bound on how many `Crash` steps a behaviour takes | reserved, ADR 0013 |

- **`Capability`.** `"Conditional"` and `"Unconditional"` are `commitLocked`'s
  two branches, the swap and what follows `store.ErrUnsupported`: the
  fallback where `FallbackAllowed`, and otherwise the refusal ADR 0021 adds.
  `"Conditional"` honours every conditional write, and `"Unconditional"`
  answers every one 501. `"Flaky"` honours them, but may answer any one of
  them 501.
- **`Faults`' values.** All reserved, ADR 0013.
  - `"SwapReplyLost"`: a swap is applied, and an error is returned, from a
    transport error or a 5xx other than 501. It enables the action of the
    same name.
  - `"SwapFailed"`: a swap is not applied, and an error that is not typed is
    returned. It enables the action of the same name.
  - `"SwapConflict409"`: a swap is not applied, and a 409 is returned while
    its condition holds, which `errorFor` maps to `store.ErrPrecondition`, so
    that `SwapConflict` may then fire.
  - `"PutFailed"`: a snapshot `Put`, with the chunk uploads folded before it,
    or the fallback's root `Put`, is not applied.
  - `"SnapshotReplyLost"`: a snapshot `Put` is applied, with the chunk
    uploads folded before it, and an error is returned: an attempt took
    effect, but its reply was lost, or `S3.do`'s later attempts failed, or
    the context ended. Only `S3.Put` gives it.
  - `"FallbackReplyLost"`: the fallback's root `Put` is applied, and an error
    is returned, in the same ways, so `commitLocked` keeps its old
    `rootETag`.
- **`Start`'s values.** Both reserved, ADR 0013. `"Empty"`: an empty bucket,
  with every mount at `loadOrInit`'s first read. `"Loaded"`: a filesystem at
  epoch 1, written outside the model, which every mount has loaded.

**Definitions.** The module's other definitions. All reserved, ADR 0013, but
`Mutable`, `Prunable`, `FallbackAllowed` and `Diverge` (V1), which exist,
and `Below`, which ADR 0019 reserves.

| Definition | Meaning |
|---|---|
| `AllFixes`, `AllFaults` | the seven `Fixes` members, and the six `Faults` values |
| `Writers` | `Mounts` and `""`, a writer outside the model |
| `Keys` | the snapshot keys, `snapshots/<epoch>-<nonce>.json.gz`, as records `[epoch, nonce]` whose nonce is in `Nonces` or is 0 |
| `NoKey` | no key: epoch 0, nonce 0 |
| `CommitIds` | the commits, as records `[writer, epoch]`: the changes one mount committed at one epoch |
| `ETagOf(k, w)` | the ETag that a write by `w` of a root pointer naming `k` gives it |
| `NoETag` | no ETag, as `""` is in `FS.rootETag` |
| `ETags` | every ETag, `NoETag` included |
| `LoadedKey`, `LoadedCommit` | the key and the commit of the filesystem at epoch 1 that `Start` `"Loaded"` begins with |
| `InitialMode(m)` | the mode `New` gives a mount: `"ReadOnly"` or `"Writable"` |
| `KeyLess(a, b)` | the order of snapshot keys, by epoch and then by nonce, in which `pruneSnapshots` sorts them |
| `Kept(L)` | the keys of `L` that a prune pass keeps: the `Retention` that sort last |
| `Least(S)` | the lowest key of `S`: the next one a prune pass deletes |
| `Below(L, k)` | the keys of `L` that sort below `k`; reserved, ADR 0019 |
| `Prunable(L, k)` | the keys a prune pass begun for `k` may delete, with `PruneBelowCurrent`: `Below(L, k)` less the `Retention` newest of them. Go `prunable`, under rule 4 (V7); exists |
| `Mutable(m)` | V1 |
| `Fenced(m)` | `m` is read-only and `ReadOnlyFence` is in `Fixes`: `store.ReadOnly` refuses `m`'s writes |
| `FallbackAllowed` | the unconditional fallback follows a swap the store answered 501: `FallbackOptIn` is not in `Fixes`, which reads as a build without ADR 0021's fix or as mounts whose operator set `Config.AllowUnconditionalRoot`, which behave alike (the module's J17). Go `(*FS).fallbackAllowed`, under rule 4 (V7); exists, as of `85dbfcf` |
| `CommitIdOf(m, k)` | the commit that `m` makes at `k`'s epoch |
| `FreshKeys(m)` | the keys a new attempt by `m` may write: at the next epoch, with a nonce no write has named (`Nonce`), or with nonce 0 |
| `KeyFor(m)` | the key of `m`'s attempt: `attempt[m]`, or else a fresh one |
| `FirstStep` | a commit's first call: `"PutSnapshot"`, or without `ChunksFirst` `"Swap"` |
| `NextStep(m)` | the commit step due at `m`, or `"None"`. At `"Ready"` it is a commit's first call when `nsDirty[m]` is set, as `Sync` requires, and `m` has not diverged, as `commitLocked` requires |
| `SwapCondition(m, k)` | whether the bucket would apply `m`'s swap to the root pointer naming `k`: If-None-Match for an empty `rootETag`, which is unconditional without `IfNoneMatchInit`, and If-Match otherwise |
| `WriteCall(m)` | a `Put`, `PutIfMatch` or `Delete` call of `m`'s reaches the bucket: a read-only `m` joins `roWrites` |
| `WriteRoot(m, k)` | `m` writes the root pointer, naming `k` |
| `CommitFails(m, k)` | `commitLocked` returns an error: back to `"Ready"`, or to `"Failed"` for `initEmpty`'s commit |
| `FlushEffect(m, k)` | the chunk of `m`'s commit at `k`'s epoch is stored; an epoch-1 commit has none |
| `SnapshotEffect(m, k)` | `m`'s snapshot `Put` of `k` takes effect, and with `ChunksFirst` the chunk uploads folded before it too |
| `Diverge(m)` | V1's event, which `SwapConflict` conjoins: `commitLocked`'s `Fire` of `mountDiverge` on `FS.mode`, which makes a `"Writable"` mount's `mode[m]` `"Diverged"` and leaves any other mode as it is (the module's R11). Before the conversion, at `043927f`, it was `commitLocked`'s `diverged.Store(true)`, which made a read-only mount's `"Diverged"` too (V1's note) |
| `Committed(m, k, et)` | `commitLocked`'s success path, with the root pointer's new ETag `et` |
| `BucketVars`, `MountVars`, `PruneVars`, `GhostVars`, `Vars` | the variables, in groups, for `UNCHANGED` and for `Spec` |
| `Next`, `Spec` | the next-state relation, and the specification, `Init /\ [][Next]_Vars` |
| `PassApartFromOwnCommits` | a state constraint, which only `CommitProtocol_PruneTwoWriters` sets: while a mount's prune pass runs, the mount completes no commit, and the pass has not deleted the snapshot of an attempt the mount has still to swap. TLC takes no step from a state that breaks it. `CommitProtocol_PruneOverlapTwoWriters` is the same configuration without it |

**Properties.** All reserved, ADR 0013: `TypeOK`, `RootSnapshotExists`,
`NoLostAck`, `ChunksBeforeRoot`, `ReadOnlyNeverWrites`, `NoSelfDivergence`.

**Fixes.** Later ones are named by each fix's ADR.

| Member | What the fix does | Status |
|---|---|---|
| `IfNoneMatchInit` | `initEmpty` commits with an empty `rootETag`, so its swap is If-None-Match and two first mounts cannot both create a filesystem | exists |
| `Nonce` | `commitLocked` puts six random bytes in every snapshot key, so two writers committing one epoch cannot overwrite each other's snapshot | exists |
| `ChunksFirst` | `Sync` stores every chunk before the snapshot that names it, and the snapshot before the swap | exists |
| `ReadOnlyFence` | `New` puts a read-only mount's store behind `store.ReadOnly` (ADR 0011 §3) | exists |
| `PruneBelowCurrent` | a prune pass deletes only the snapshot keys that sort below the one its commit swapped the root pointer to, and keeps the `Retention` newest of them, so the snapshot the root pointer names is never deleted (ADR 0019 §1, §2) | exists |
| `AtomicLoad` | `loadOrInit` reads the root pointer's body and its ETag with one call, `store.Store.GetWithETag`, so the ETag its first commit swaps against is the ETag of the body it loaded, and a commit by another mount between a mount's reads is refused, not overwritten (ADR 0020 §1, §2) | exists, as of `c5068a0` |
| `FallbackOptIn` | `commitLocked`, after `store.ErrUnsupported` from the swap, writes the root pointer unconditionally only on a mount whose operator set `Config.AllowUnconditionalRoot`, and otherwise fails the commit, writing nothing more and reclaiming the snapshot it uploaded, so one 501 never turns a conditional writer into an unconditional one (ADR 0021 §1, §2) | exists, as of `8415f61` |

**Checked against rule 1.** Every name the CommitProtocol spec added here was
checked against rule 1 and against the operators listed at the end of this
file. The bare names are `Init`, `StrayDirty` and `PruneEnd`; the constants
from `Mounts` to `MaxCrashes`; the values `"SwapConflict409"`, `"PutFailed"`,
`"SnapshotReplyLost"`, `"FallbackReplyLost"`, `"Empty"` and `"Loaded"`;
`pc[m]`'s `"Ready"`, `"Swap"` and `"Failed"`; `NextStep`'s `"None"`; and the
definitions above, `WriteCall` and `SnapshotEffect` among them. Each is
UpperCamel ASCII letters and digits starting with a letter, and none equals an
operator of the four modules, a Go keyword or predeclared identifier, all of
which are lower case, or a TLA+ reserved word, all of which are upper case.
The new variables, `rpSnapshot`, `base`, `overtaken`, `roWrites` and
`crashes`, are lowerCamel like every variable here, and none is a reserved
word or an operator. `Diverge` is V1's, and was checked there. No name was
renamed. ADR 0019 added `PruneBelowCurrent`, `Below` and `Prunable`, each
UpperCamel ASCII letters, none equal to an operator above (`Below` and
`Prunable` are not among them), a Go keyword or predeclared identifier, or a
TLA+ reserved word; and the Go name `prunable`, lowerCamel, which is no Go
keyword or predeclared identifier either. ADR 0020 added no bare name;
`AtomicLoad` was checked when it was reserved. Its Go name `GetWithETag` is
an exported method of `store.Store`, UpperCamel, and no Go keyword or
predeclared identifier. ADR 0021 added `FallbackOptIn` and
`FallbackAllowed`, each UpperCamel ASCII letters, neither equal to an
operator above (`Head` is contained in neither), a Go keyword or
predeclared identifier, or a TLA+ reserved word; and the Go names
`AllowUnconditionalRoot`, `allowUnconditionalRoot` and `fallbackAllowed`,
none a Go keyword or predeclared identifier.

## V4. Sync and file data

`docs/tla/FileData.tla`; one file of one mount, with no prune goroutine and
no removal of the file (ADR 0013 §1); not Go machines. The Go column is
indicative (rule 7); every site is in `internal/blobfs/fs.go` but `Sync` and
`commitLocked`, which are in `internal/blobfs/commit.go`.

**Actions.** All reserved, ADR 0013. ADR 0013 §4 says why `SyncCommit`,
`FlushBody` and `WriteBody` may each be one action.

| Action | Go | Meaning |
|---|---|---|
| `SyncList` | `flushAll`'s listing | |
| `FlushLock` | `flushOpen` taking the file's `openFile.mu` | |
| `FlushBody` | `flushOpen`'s work under it | one action (ADR 0013 §4); its outcomes are `FlushEarly`, `FlushOK` and `FlushFail` |
| `FlushEarly` | `flushOpen`'s early return | nothing to apply |
| `FlushOK` | `flushOpen` succeeding | |
| `FlushFail` | `flushOpen` failing in a fetch or an upload | |
| `SyncGateWait` | `Sync`'s `commitGate.Lock`, waiting | |
| `SyncGateTake` | `Sync`'s `commitGate.Lock`, held | |
| `SyncSettleList` | `settleAll`'s listing | |
| `SyncCommit` | `commitLocked` | one action (ADR 0013 §4) |
| `SyncRelease` | `Sync` releasing what it holds | |
| `WriteGetOpen` | `Write`'s `getOpen` | |
| `WriteLock` | `bufferWrite` taking `openFile.mu` | |
| `WriteBody` | `bufferWrite`'s work under it | one action (ADR 0013 §4) |
| `WriteCopyList` | `Write` copying the chunk list before it takes the lock | only without `ListUnderLock` |
| `TruncateGate` | `truncate`'s `commitGate.RLock` | |
| `TruncateGetOpen` | `truncate`'s `getOpen` | |
| `TruncateLock` | `truncate` taking `openFile.mu` | |
| `TruncateBody` | `truncate`'s change under `openFile.mu` and `FS.mu` | |
| `ReadStep1` | `Read`'s step 1 (ADR 0005 §2) | |
| `ReadLock` | `readBuffered` taking `openFile.mu` | |
| `ReadStep2` | `readBuffered`'s step 2 | |
| `ReadFetch` | `Read`'s step 3, its fetches | |
| `Crash` | none | |

**Variables.** None holds a machine's state, so all are named here (rule 3).

| Variable | Go | Status |
|---|---|---|
| `size`, `chunks` | `inode.Size`, `inode.Chunks` | exists |
| `open` | `FS.open` | exists |
| `dirty` | `openFile.dirty` | exists |
| `pendingTrim` | `openFile.pendingTrim` | exists |
| `unsettled` | `openFile.unsettled` | exists |
| `nsDirty` | `FS.dirty` | exists |
| `committed` | none | reserved, ADR 0013 |
| `gateR`, `gateW`, `gateQ` | `FS.commitGate`'s readers, writer and waiting writers | exists |
| `ofMu` | `openFile.mu` | exists |
| `pc` | none | reserved, ADR 0013 |
| `ideal`, `removed`, `removedCommitted`, `rHist`, `reply`, `leaked`, `refused` | none: ghosts | reserved, ADR 0013 |

**Properties.** All reserved, ADR 0013: `TypeOK`, `ViewIsIdeal`,
`NoCommitLeak`, `NoRefusedCommit`, `UnsettledIsMarked`, `GateExclusive`, `I1`,
`I2`, `I3`, `I4` (ADR 0006 §2's invariants), `ReadIsLinearizable`,
`SyncReturns`, `TruncReturns`.

**Fixes.**

| Member | Source | Status |
|---|---|---|
| `ReadOneView` | ADR 0005 | exists |
| `TrimHonoured` | ADR 0006 | exists |
| `TrimKeepsMinimum` | ADR 0006's smallest length | exists |
| `CommitGate` | ADR 0012 | exists |
| `StickyMark` | ADR 0012 Assumption 9 | exists |
| `ListUnderLock` | `5479fe2` | exists |

## V5. Lifecycle

`docs/tla/NfsDurability.tla`. Every entry is reserved, by the lifecycle ADR,
but `FreshVerifier`, which exists.

**Server states.** `Serving`, `Draining`, `Stopped`; Go `serverServing`,
`serverDraining`, `serverStopped`; TLA+ `server`.

**Connection states.** `Open`, `Draining`, `Closed`; Go `connOpen`,
`connDraining`, `connClosed`; TLA+ `conn[c]`.

**Actions.** `Signal`, `AcceptFail`, `StopDispatch`, `DrainCalls`,
`FinalCommit`, `Exit`, `Crash`, `UnstableWrite`, `FileSyncWrite`, `NfsCommit`,
`NamespaceOp`, `SetAttrSize`, `Tick`, `ClientResend`. The Go events are named
by the lifecycle ADR.

**Variables.** `mem`, `committed`, `verf`, `retained`, `released`,
`ackedSync`.

**Properties.** `StableWritesDurable`, `SyncOpsDurable`,
`ShutdownLosesNothing`.

**Fixes.** `FreshVerifier`, which exists: `New` fills `FS.writeVerf` from
`crypto/rand` on every start (`docs/DESIGN.md` §9).

## V6. Write budget

`docs/tla/Budget.tla`; not a Go machine; ADR 0003 §4's wait, and ADR 0003
§2's five accounting sites with a file removed under them (ADR 0013 §1).

**Variables.** `charge`, `parked`, `draining`, `ended`, `failure`, `failedAt`,
the fields of `budget` in `internal/blobfs/budget.go`, which exist; and per
writer `start`, `await`'s local, which exists, and `pc` and `ret`, reserved,
ADR 0013. The accounting sites' variables, such as a file's `of.bytes` and
whether `FS.open` holds it, are named by the Budget spec's PR.

**Settle outcomes.** All reserved, ADR 0013, under the names ADR 0003 §4
gives them: `Succeeded`, `Failed`, `Abandoned`, `Panicked`, with the actions
`SettleSucceeded`, `SettleFailed`, `SettleAbandoned`, `SettlePanicked`.

**Drain events.** All reserved, ADR 0013: `DrainReturnsNil`,
`DrainReturnsError`, `DrainPanics`, `DrainGoexits`.

**Returns.** Reserved, ADR 0013: `ret[w]` in {`"Admitted"`, `"DrainError"`,
`"PanicError"`, `"CtxError"`}.

**Actions.** All reserved, ADR 0013: `Enter`, `Park`, `Wake`,
`BecomeDrainer`, `Release`, `Add`, `Cancel`. The accounting sites' actions
are named by the Budget spec's PR.

**Properties.** All reserved, ADR 0013: `AtMostOneDrain`, `NoStrandedDrain`,
`G1`, `G2`, `G3` (ADR 0003 §4's failure contract), `NoLostWakeup`; and ADR
0003 §2's two statements about the whole budget, `ChargeCoversBytes` (the
charge is never below the sum of every file's `of.bytes`, which is never
below zero) and `ChargeMatchesOpen` (with no accounting step in flight, the
charge equals that sum over the files `FS.open` holds; a site is in flight
from its first access to the charge or to `of.bytes` until its last, and a
`bufferWrite` from its charge, site 1, until its tail block, site 2, ends).

**Fixes.** Named by the Budget spec's PR; none yet.

## V7. Policies

- `Mutable`: V1; exists.
- `ReadOnlyNeverWrites`: an invariant (V3): no `Put`, `PutIfMatch` or
  `Delete` call of a read-only mount reaches the bucket, whether or not it
  would change anything (ADR 0011 §3, §4); reserved, ADR 0013. The fence,
  `store.ReadOnly`, exists (ADR 0011 §3).
- Exists, `internal/policy/policy.go`, added with ADR 0014:
  - `Permits` = `policy.Permits(c, o, want)`;
  - `UnixAllows(r)` = `policy.Unix().Check(r) == nil`;
  - `AccessBits` = `policy.AccessBits`;
  - `OpRead`, `OpWrite`, `OpTruncate`, `OpChmod`, `OpChown`, `OpChgrp`,
    `OpSetTimes`, `OpLookup`, `OpReadDir`, `OpAddEntry`, `OpRemoveEntry`,
    `OpRename`;
  - `PermExec`, `PermWrite`, `PermRead`.

  `UnixAllows` stands (ADR 0014 §10). Rule 4 gives the spec a Go predicate's
  own name. This predicate is `policy.Unix().Check(r) == nil`: the `Check`
  method that every `Policy` has, reached through a function that returns an
  interface. So the name rule 4 would give it, `Check`, would name every
  policy at once. Rule 4 is not changed for any other name.
- `EffectAuthorized`: an optional FileData invariant; reserved, ADR 0014. It
  says that every change a call makes to a file's bytes or size is one that
  `UnixAllows` allows, for that call's caller, against the file's attributes
  at the instant the change is made. Whether FileData states it, and how its
  configurations use it, is that spec's business. Under #101 it fails today
  (ADR 0014 §10).
- Commit-protocol decisions, such as deleting a snapshot or writing the root
  unconditionally, are named by their ADRs, under rule 4.
- `Prunable` = `prunable(keys, current, keep)`, a plain function in
  `internal/blobfs/commit.go`: the one place that decides what a prune pass
  may delete (ADR 0019 §2). It is not a `policy.Policy`: no caller asks for a
  deletion, since a pass is a step the server takes on its own commit's
  success, which ADR 0018 §2 keeps outside the policy rule. Exists.
- `FallbackAllowed` = `(*FS).fallbackAllowed()`, a method in
  `internal/blobfs/commit.go` that returns the mount's
  `allowUnconditionalRoot`, set once by `New` from
  `Config.AllowUnconditionalRoot`: the one place that decides whether a
  swap the store answered 501 is followed by the unconditional write (ADR
  0021 §2). It is not a `policy.Policy`: no caller asks for the write, and
  the branch is a step the server takes on a store's answer, decided by the
  operator's configuration, which ADR 0018 §2 keeps outside the policy rule
  (ADR 0021 §5). Exists, as of `85dbfcf`.

## Operators of the standard modules

Rule 1 rules out a bare name equal to any of these, and ADR 0013 §3 a
definition named as one. They are every operator the four modules define,
fetched on 2026-10-10 from the tlaplus repository's
`tlatools/org.lamport.tlatools/src/tla2sany/StandardModules/`, both at the
tag `v1.7.4`, which the tools are pinned to, and at `master`. The two lists
agree, and no operator in them is `LOCAL`.

| Module | Operators |
|---|---|
| `Naturals` | `Nat`, `+`, `-`, `*`, `^`, `<`, `>`, `\leq`, `\geq`, `%`, `\div`, `..` |
| `FiniteSets` | `IsFiniteSet`, `Cardinality` |
| `Sequences` | `Seq`, `Len`, `\o`, `Append`, `Head`, `Tail`, `SubSeq`, `SelectSeq` |
| `TLC` | `Print`, `PrintT`, `Assert`, `JavaTime`, `TLCGet`, `TLCSet`, `:>`, `@@`, `Permutations`, `SortSeq`, `RandomElement`, `Any`, `ToString`, `TLCEval` |

`FiniteSets`, `Sequences` and `TLC` bring in the modules they use with `LOCAL
INSTANCE`, so extending one adds only the operators in its own row.

**Checked against rule 1.** No name in this file equals any operator above:
the nearest are `HeadRoot` and `FallbackHead`, which contain `Head` without
equalling it. No name is a Go keyword or predeclared identifier, since every
one of those is written in lower case (*The Go Programming Language
Specification*), and every bare name here begins with a capital. No name is a
TLA+ reserved word: every word token of the TLA+ grammar at `v1.7.4` is
written in capitals, and the only names here with no lower-case letter, `I1`
to `I4` and `G1` to `G3`, hold a digit, which no reserved word does. No name
was renamed.

Sources, each fetched on 2026-10-10:
<https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/tla2sany/StandardModules/Naturals.tla>,
and the same path for `FiniteSets.tla`, `Sequences.tla` and `TLC.tla`, and for
all four with `master` in place of `v1.7.4`;
<https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/javacc/tla%2B.jj>
for the reserved words; and <https://go.dev/ref/spec> for Go's keywords and
predeclared identifiers. ADR 0013's *Sources* say what was taken from each,
and how far each was read.
