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
  `f102570`.
- **reserved**, with the ADR named: it is not in the code, and that ADR
  introduces it. A name only the specs use, with no Go name, such as an
  action, a ghost, a property or a string a variable holds, is reserved by
  ADR 0013, which introduces it, unless its entry names another ADR.

A status given to a group, a list or a section holds for every entry in it.
ADR 0014, the lifecycle ADR, the load-race ADR and the ADR for #5 are not yet
written; their entries hold the names those ADRs will use. The operators of
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
variables (ADR 0013, Assumption 13). So `pruning[m]`'s three strings, which refine the
bool `FS.pruning`, are no machine's states, and its row records them.

## V1. Mount mode

Go type `mountMode`, field `FS.mode` (read without a lock; ADR 0014 §7);
today `FS.readOnly` and `FS.diverged`. TLA+ `mode[m]`.

| Name | Kind | Go | TLA+ | Meaning | Status |
|---|---|---|---|---|---|
| `Writable` | state | `mountWritable` | `"Writable"` | accepts changes | reserved, ADR 0014 |
| `ReadOnly` | state | `mountReadOnly` | `"ReadOnly"` | `Config.ReadOnly`; no edge leaves it | reserved, ADR 0014 |
| `Diverged` | state | `mountDiverged` | `"Diverged"` | lost a swap; terminal | reserved, ADR 0014 |
| `Closed` | state | `mountClosed` | `"Closed"` | final commit done | reserved, the lifecycle ADR |
| `Diverge` | event | `mountDiverge` | `Diverge(m)` | `Writable` to `Diverged` | reserved, ADR 0014 |
| `Close` | event | `mountClose` | `Close(m)` | its edges are the lifecycle ADR's | reserved, the lifecycle ADR |
| `Mutable` | predicate | `(*FS).mutable` | `Mutable(m)` | nil exactly when `Writable` | exists, `internal/blobfs/fs.go` |

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

`docs/tla/CommitProtocol.tla`. The Go sites exist: `Sync`, `loadOrInit`,
`initEmpty`, `commitLocked`, `schedulePrune` and `pruneSnapshots` are in
`internal/blobfs/commit.go`, and `flushAll`, `settleAll` and `putChunk` in
`internal/blobfs/fs.go`.

**Actions.** All reserved, ADR 0013: an action's name is the spec's, whatever
Go performs it. The Go column is indicative (rule 7).

| Action | Go | Meaning |
|---|---|---|
| `GetRoot` | `loadOrInit`'s `Get(rootKey)` | reads the root pointer; with the load-race fix, one read of its body and its ETag |
| `HeadRoot` | `loadOrInit`'s `Head(rootKey)` | the load-race window, which that fix removes (`Fixes` member `AtomicLoad`) |
| `GetSnapshot` | `loadOrInit`'s `Get` of the snapshot the root names, and `decodeSnapshot` | |
| `RefuseEmpty` | `loadOrInit`'s read-only branch | a read-only mount refuses a bucket with no root (ADR 0011 §2) |
| `InitEmpty` | `initEmpty` | then `PutSnapshot` and an If-None-Match swap |
| `Mutate` | any namespace-changing `vfs.FS` method | guarded by `Mutable` |
| `Flush` | `Sync`'s `flushAll` and `settleAll`; `putChunk` | |
| `PutSnapshot` | `commitLocked`'s snapshot `Put` | |
| `SwapOK` | `commitLocked`'s `PutIfMatch` | applied, and the reply carries the ETag |
| `SwapReplyLost` | the same | applied, and an error is returned |
| `SwapFailed` | the same | not applied, and an error is returned |
| `SwapConflict` | the same | `store.ErrPrecondition`; fires `Diverge` |
| `SwapUnsupported` | the same | `store.ErrUnsupported` |
| `FallbackPut` | `commitLocked`'s unconditional root `Put` after it | may merge with `SwapUnsupported` |
| `FallbackHead` | the `Head` after `FallbackPut` | |
| `StartPrune` | `schedulePrune` | |
| `PruneList` | `pruneSnapshots`' `List` loop | |
| `PruneDelete` | `pruneSnapshots`' `Delete` loop | |
| `Crash` | none | process death, or any exit skipping the final commit |

**Variables.** Named as rule 3 says: `mode[m]` and `rootKnowledge[m]` hold
machine states, and the rest are named here.

| Variable | Go | Meaning | Status |
|---|---|---|---|
| `bucketRoot` | the object at `rootKey` | `[key, etag, writer]`: `rootPointer.Snapshot`, its ETag, `rootPointer.Writer` | exists |
| `snapshots` | the objects under `snapshotPrefix` | | exists |
| `chunks` | the objects under `chunkPrefix` | | exists |
| `pc[m]` | none | | reserved, ADR 0013 |
| `mode[m]` | `FS.mode` (V1) | | reserved, ADR 0014 |
| `rootKnowledge[m]` | `FS.rootKnowledge` (V2) | | reserved, the ADR for #5 |
| `rootETag[m]` | `FS.rootETag` | | exists |
| `epoch[m]` | `FS.epoch` | | exists |
| `nsDirty[m]` | `FS.dirty` | | exists |
| `attempt[m]` | none | the key the current attempt wrote | reserved, ADR 0013 |
| `pruning[m]` | `FS.pruning` | in {`"Idle"`, `"Listing"`, `"Deleting"`}, which refine the bool: `FS.pruning` is true unless `"Idle"`. The three strings are reserved, ADR 0013 | exists |
| `pruneCurrent[m]`, `pruneDoomed[m]` | `pruneSnapshots`' `current` and `doomed` | | exists |
| `acked`, `used` | none | ghosts | reserved, ADR 0013 |

**Constant.** Reserved, ADR 0013, with its values. `Capability` in
{`"Conditional"`, `"Unconditional"`, `"Flaky"`}: what the backend does with a
conditional write. `Conditional` and `Unconditional` are `commitLocked`'s two
branches, the swap and the fallback after `store.ErrUnsupported`. What `Flaky`
does is the CommitProtocol spec's to define.

**Properties.** All reserved, ADR 0013: `TypeOK`, `RootSnapshotExists`,
`NoLostAck`, `ChunksBeforeRoot`, `ReadOnlyNeverWrites`, `NoSelfDivergence`.

**Fixes.** Later ones are named by each fix's ADR.

| Member | What the fix does | Status |
|---|---|---|
| `IfNoneMatchInit` | `initEmpty` commits with an empty `rootETag`, so its swap is If-None-Match and two first mounts cannot both create a filesystem | exists |
| `Nonce` | `commitLocked` puts six random bytes in every snapshot key, so two writers committing one epoch cannot overwrite each other's snapshot | exists |
| `ChunksFirst` | `Sync` stores every chunk before the snapshot that names it, and the snapshot before the swap | exists |
| `ReadOnlyFence` | `New` puts a read-only mount's store behind `store.ReadOnly` (ADR 0011 §3) | exists |
| `AtomicLoad` | the load-race fix: one read of the root pointer's body and its ETag | reserved, the load-race ADR |

## V4. Sync and file data

`docs/tla/FileData.tla`; not Go machines. The Go column is indicative
(rule 7); every site is in `internal/blobfs/fs.go` but `Sync` and
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

`docs/tla/Budget.tla`; not a Go machine; ADR 0003 §4.

**Variables.** `charge`, `parked`, `draining`, `ended`, `failure`, `failedAt`,
the fields of `budget` in `internal/blobfs/budget.go`, which exist; and per
writer `start`, `await`'s local, which exists, and `pc` and `ret`, reserved,
ADR 0013.

**Settle outcomes.** All reserved, ADR 0013, under the names ADR 0003 §4
gives them: `Succeeded`, `Failed`, `Abandoned`, `Panicked`, with the actions
`SettleSucceeded`, `SettleFailed`, `SettleAbandoned`, `SettlePanicked`.

**Drain events.** All reserved, ADR 0013: `DrainReturnsNil`,
`DrainReturnsError`, `DrainPanics`, `DrainGoexits`.

**Returns.** Reserved, ADR 0013: `ret[w]` in {`"Admitted"`, `"DrainError"`,
`"PanicError"`, `"CtxError"`}.

**Actions.** All reserved, ADR 0013: `Enter`, `Park`, `Wake`,
`BecomeDrainer`, `Release`, `Add`, `Cancel`.

**Properties.** All reserved, ADR 0013: `AtMostOneDrain`, `NoStrandedDrain`,
`G1`, `G2`, `G3` (ADR 0003 §4's failure contract), `NoLostWakeup`.

**Fixes.** Named by the Budget spec's PR; none yet.

## V7. Policies

- `Mutable`: V1; exists.
- `ReadOnlyNeverWrites`: an invariant (V3); reserved, ADR 0013. The fence,
  `store.ReadOnly`, exists (ADR 0011 §3).
- Reserved, ADR 0014:
  - `Permits` = `policy.Permits(c, o, want)`;
  - `UnixAllows(r)` = `policy.Unix().Check(r)` returns nil;
  - `AccessBits` = `policy.AccessBits`;
  - `OpRead`, `OpWrite`, `OpTruncate`, `OpChmod`, `OpChown`, `OpChgrp`,
    `OpSetTimes`, `OpLookup`, `OpReadDir`, `OpAddEntry`, `OpRemoveEntry`,
    `OpRename`;
  - `PermExec`, `PermWrite`, `PermRead`.

  `UnixAllows` departs from rule 4, under which it would be `Check`; ADR 0014,
  which introduces the policy, settles which of the two gives way.
- `EffectAuthorized`: an optional FileData invariant; reserved, ADR 0014.
- Commit-protocol decisions, such as deleting a snapshot or writing the root
  unconditionally, are named by their ADRs, under rule 4.

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
