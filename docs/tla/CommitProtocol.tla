---------------------------- MODULE CommitProtocol ----------------------------
\* How a mount of this filesystem reads the root pointer, commits, prunes
\* snapshots and diverges, against other mounts, failing store calls and
\* crashes. ADR 0013 section 1 names this module and what it must show: that
\* no acknowledged commit is lost (NoLostAck); that the root pointer never
\* names a snapshot or a chunk the bucket does not hold (RootSnapshotExists,
\* ChunksBeforeRoot); that a read-only mount writes nothing
\* (ReadOnlyNeverWrites, ADR 0011); and that a mount diverges only after
\* another writer (NoSelfDivergence). docs/tla/trace.tsv gives the Go symbols
\* that perform each action, and docs/vocabulary.md, V3, gives every name.
\*
\* Checked against 043927f
\*
\* Scope
\*   - One bucket, and the mount processes in Mounts.
\*   - Loading: GetRoot, HeadRoot, GetSnapshot, RefuseEmpty and InitEmpty.
\*   - Namespace changes: Mutate, and StrayDirty (Assumption J6).
\*   - Commits: chunks, then the snapshot, then a conditional swap of the
\*     root pointer, with five store outcomes (SwapOK, SwapReplyLost,
\*     SwapFailed, SwapConflict, SwapUnsupported) and the fallback after a
\*     501 (FallbackPut, FallbackHead).
\*   - Divergence.
\*   - Each mount's prune passes: StartPrune, PruneList, PruneDelete and
\*     PruneEnd.
\*   - Crashes.
\*
\* Leaves out
\*   - File data, the commit gate and its settle pass: FileData's.
\*   - The write budget, and NFS acknowledgements: NfsDurability's.
\*   - rootKnowledge, which the ADR for #5 adds, and AtomicLoad, which the
\*     load-race ADR adds (Assumption J13).
\*   - Failed reads: a Get or a Head of the root pointer, or a Get of the
\*     snapshot it names, failing other than with not-found. Each only makes
\*     New fail.
\*   - Failed List and Delete calls (Assumption J14).
\*   - A swap reply with no ETag (#103), and a Head failing after the
\*     fallback's Put.
\*   - One call taking effect more than once through the retries of S3.do
\*     (ADR 0013 Assumption 5).
\*   - Two processes on one Local directory.
\*   - Requests that take effect after they return, and timeouts.
\*   - Paged listings: a listing is exact below 1,000 keys (R9).
\*   - Chunk deletion: nothing deletes a chunk.
\*   - The root pointer's parse and version checks.
\*   - The Closed mode, which the lifecycle ADR adds.
\*
\* Reductions (ADR 0013 section 4)
\*   R1.  InitEmpty and RefuseEmpty are outcomes of GetRoot's Get, and their
\*        work is local, so each is one action with that Get.
\*   R2.  HeadRoot assigns rootETag[m], which Go does at the end of
\*        loadOrInit. Nothing reads FS.rootETag before New returns.
\*   R3.  mutable() is folded into Mutate (ADR 0013 Assumption 9).
\*   R4.  Mutate is enabled only at "Ready", because FS.mu is held across
\*        commitLocked (#43).
\*   R5.  A mount runs one commit at a time, since the commit gate
\*        serialises commit phases.
\*   R6.  With ChunksFirst, Flush folds into PutSnapshot. No step of this
\*        model reads or deletes a chunk, and uploads only grow chunks, which
\*        harms no invariant. Folding also drops early flushes (Commit and
\*        FILE_SYNC writes), which is sound by the same monotonicity.
\*   R7.  SwapUnsupported merges with FallbackPut, because the call answered
\*        501 changes no object (docs/vocabulary.md, V3, allows this).
\*   R8.  StartPrune runs in the same hold of FS.mu as the swap's success
\*        path, so it is part of SwapOK and of FallbackHead.
\*   R9.  The List loop is one step: it is exactly one call below 1,000 keys,
\*        and every configuration stays far below that.
\*   R10. PruneEnd stays a step of its own, because it does not commute with
\*        schedulePrune's read of FS.pruning.
\*   R11. A read-only mount's conflict leaves mode[m] at "ReadOnly". At
\*        043927f mutable() checks FS.readOnly first, and with ReadOnlyFence
\*        the case cannot be reached.
\*
\* Assumptions (judgement calls, not requirements)
\*   J1.  Commit ids are lineage ids, [writer, epoch]. They are unique,
\*        because a mount commits at most once at each epoch above the one
\*        it loaded. They collide only after the root pointer has gone back,
\*        which happens only after a violation has already been reported.
\*   J2.  An epoch-1 commit is initEmpty's, or with Start = "Loaded" the
\*        outside writer's, and names no chunk. Every later commit names one
\*        chunk, which its commit id identifies.
\*   J3.  An ETag is [key, writer]. With Nonce it is fresh, since every key
\*        is (ADR 0013 section 4 and Assumption 6). Without Nonce, two
\*        writes of the root pointer share an ETag only when one mount writes
\*        one key twice. That needs a crash and an unconditional write, and
\*        no configuration that removes Nonce allows either.
\*   J4.  The fault meanings given with the constants below. The 409
\*        over-approximates the 409 Conflict that AWS documents for
\*        concurrent requests, as ADR 0013 Assumption 5 asks.
\*   J5.  Start = "Loaded" is a reachable state, used to isolate a defect
\*        without assuming a fix that does not exist. Its commit was made
\*        outside the model and is no mount's acknowledgement, so acked
\*        starts empty.
\*   J6.  StrayDirty stands for a path that dirties a read-only mount: ADR
\*        0011 section 7's seam, through which a test sets FS.dirty on a
\*        read-only FS. No path does so at 043927f.
\*   J7.  Without ChunksFirst a commit runs its swap, then its snapshot Put,
\*        then its flush, and is acknowledged at the swap.
\*   J8.  Without IfNoneMatchInit, initEmpty's swap is unconditional.
\*   J9.  Crash also restarts a mount whose New failed, and every restart
\*        counts against MaxCrashes.
\*   J10. Deletes run in ascending key order, as pruneSnapshots sorts them.
\*   J11. The MustHold envelope is a reading, not a measurement: pruning is
\*        shown safe only without failed swaps and with Retention at least 2
\*        (CommitProtocol_PruneSingle).
\*   J12. The configurations' bounds and tiers are judgement calls.
\*   J13. Neither rootKnowledge nor AtomicLoad is modelled. Their ADRs add
\*        them.
\*   J14. Failed List and Delete calls are left out. A failed Delete can
\*        leave an orphan above the root pointer's snapshot, so the ADR for
\*        #88's fix adds them if its argument needs them.
\*   J15. CommitProtocol_PruneTwoWriters sets the state constraint
\*        PassApartFromOwnCommits, and no other configuration sets one. Its
\*        two writers also admit, at the same length as #88's route 2,
\*        behaviours in which a pass overlaps its own mount's next commit,
\*        the trigger of routes 1 and 3, and TLC reported one of them, which
\*        also ran through #89's window. No bound removes them, since the
\*        mount whose pass it is can always commit at epoch 2. The
\*        constraint assumes no fix: it only stops TLC taking a step from a
\*        state that breaks it. Once the configuration flips, it leaves
\*        unchecked a pass that overlaps its own mount's commit and another
\*        mount's attempt at once, which no configuration covers, so the ADR
\*        for #88's fix adds one.

EXTENDS Naturals, FiniteSets

CONSTANTS
  Mounts,          \* the mount processes, each named as its FS.writerID is
  ReadOnlyMounts,  \* the mounts with Config.ReadOnly set
  Fixes,           \* the fixes a configuration includes (ADR 0013 section 7)
  Capability,      \* what the bucket does with a conditional write
  Faults,          \* the failure outcomes the bucket may give
  Start,           \* "Empty" or "Loaded": the bucket as a behaviour begins
  Retention,       \* FS.retention; 0 turns pruning off
  MaxEpoch,        \* bound: the highest epoch a commit may reach
  Nonces,          \* bound: the nonces a snapshot key may carry
  MaxCrashes       \* bound: how many Crash steps a behaviour may take

\* What Capability and Faults mean (Assumption J4):
\*   Capability "Conditional": the bucket honours every conditional write.
\*   Capability "Unconditional": it answers every conditional write 501.
\*   Capability "Flaky": it honours them, but may answer any one of them 501.
\*   Fault "SwapReplyLost": a swap is applied, and an error is returned, from
\*     a transport error or a 5xx other than 501. S3.PutIfMatch is never
\*     retried.
\*   Fault "SwapFailed": a swap is not applied, and an error that is not
\*     typed is returned.
\*   Fault "SwapConflict409": a swap is not applied, and a 409 is returned
\*     while its condition holds; errorFor maps it to ErrPrecondition.
\*   Fault "PutFailed": a snapshot Put, with the chunk uploads folded before
\*     it, or the fallback's root Put, is not applied.

AllFixes  == {"IfNoneMatchInit", "Nonce", "ChunksFirst", "ReadOnlyFence"}
AllFaults == {"SwapReplyLost", "SwapFailed", "SwapConflict409", "PutFailed"}

ASSUME
  /\ Mounts # {}
  /\ "" \notin Mounts
  /\ ReadOnlyMounts \subseteq Mounts
  /\ Fixes \subseteq AllFixes
  /\ Faults \subseteq AllFaults
  /\ Capability \in {"Conditional", "Unconditional", "Flaky"}
  /\ Start \in {"Empty", "Loaded"}
  /\ Retention \in Nat
  /\ MaxEpoch \in Nat
  /\ MaxEpoch > 0
  /\ MaxCrashes \in Nat
  /\ Nonces # {}
  /\ \A n \in Nonces : n \in Nat /\ n > 0

VARIABLES
  \* The bucket.
  bucketRoot,    \* the object at rootKey: [key, etag, writer]
  snapshots,     \* the objects under snapshotPrefix: key -> set of commit ids
  chunks,        \* the objects under chunkPrefix, as the commits they serve
  \* Each mount's memory.
  pc,            \* where New, loadOrInit or commitLocked has got to
  mode,          \* "Writable", "ReadOnly" or "Diverged"
  rootETag,      \* FS.rootETag
  epoch,         \* FS.epoch
  nsDirty,       \* FS.dirty
  attempt,       \* commitLocked's key: the attempt in progress, or NoKey
  rpSnapshot,    \* loadOrInit's rp.Snapshot, between GetRoot and GetSnapshot
  base,          \* the commits FS.inodes includes
  \* Each mount's prune pass.
  pruning,       \* "Idle", "Listing" or "Deleting"; FS.pruning is the last two
  pruneCurrent,  \* pruneSnapshots' current
  pruneDoomed,   \* pruneSnapshots' doomed, still to delete
  \* Ghosts, which trace to no Go symbol, and the crash bound.
  acked,         \* commits whose commitLocked returned nil
  used,          \* snapshot keys a write that took effect has named
  overtaken,     \* another mount wrote the root since m last set rootETag[m]
  crashes        \* Crash steps taken so far

-----------------------------------------------------------------------------
\* Values.

\* Every writer of the root pointer: the mounts, and "" for a writer outside
\* the model.
Writers == Mounts \cup {""}

\* A snapshot key, snapshots/<epoch>-<nonce>.json.gz. Nonce 0 means no nonce.
Keys == [epoch : 1..MaxEpoch, nonce : Nonces \cup {0}]

\* No key.
NoKey == [epoch |-> 0, nonce |-> 0]

\* A commit: the changes one mount committed at one epoch (Assumption J1).
CommitIds == [writer : Writers, epoch : 1..MaxEpoch]

\* The ETag that a write by w of a root pointer naming k gives it
\* (Assumption J3).
ETagOf(k, w) == [key |-> k, writer |-> w]

\* No ETag: what "" is in FS.rootETag.
NoETag == ETagOf(NoKey, "")

ETags == {ETagOf(k, w) : k \in Keys, w \in Writers} \cup {NoETag}

\* With Start = "Loaded", the key and the commit of the filesystem at epoch
\* 1 that a writer outside the model wrote.
LoadedKey    == [epoch |-> 1, nonce |-> 0]
LoadedCommit == [writer |-> "", epoch |-> 1]

\* The mode New gives a mount.
InitialMode(m) == IF m \in ReadOnlyMounts THEN "ReadOnly" ELSE "Writable"

\* The order of snapshot keys: by epoch, then by nonce, as their zero-padded
\* epochs and hex nonces sort.
KeyLess(a, b) ==
  \/ a.epoch < b.epoch
  \/ a.epoch = b.epoch /\ a.nonce < b.nonce

\* The keys of L that a prune pass keeps: the Retention that sort last.
Kept(L) == {k \in L : Cardinality({j \in L : KeyLess(k, j)}) < Retention}

\* The lowest key of S: the next one a prune pass deletes (Assumption J10).
Least(S) == CHOOSE k \in S : \A j \in S : ~KeyLess(j, k)

-----------------------------------------------------------------------------
\* Predicates and helpers.

\* mutable() returns nil (docs/vocabulary.md, V1).
Mutable(m) == mode[m] = "Writable"

\* New put m's store behind store.ReadOnly, which refuses every write.
Fenced(m) == m \in ReadOnlyMounts /\ "ReadOnlyFence" \in Fixes

\* The commit that m makes at k's epoch.
CommitIdOf(m, k) == [writer |-> m, epoch |-> k.epoch]

\* The keys a new attempt by m may write: at the next epoch, with a nonce no
\* write has named (Nonce), or with none.
FreshKeys(m) ==
  IF epoch[m] + 1 > MaxEpoch
    THEN {}
    ELSE IF "Nonce" \in Fixes
           THEN {[epoch |-> epoch[m] + 1, nonce |-> n] : n \in Nonces} \ used
           ELSE {[epoch |-> epoch[m] + 1, nonce |-> 0]}

\* The key of m's attempt: the one in progress, or a fresh one.
KeyFor(m) == IF attempt[m] # NoKey THEN {attempt[m]} ELSE FreshKeys(m)

\* A commit's first call: its snapshot Put, or without ChunksFirst its swap
\* (Assumption J7).
FirstStep == IF "ChunksFirst" \in Fixes THEN "PutSnapshot" ELSE "Swap"

\* The commit step due at m, or "None". At "Ready" a commit starts when the
\* namespace is dirty, unless the mount has diverged: commitLocked's refusal.
NextStep(m) ==
  IF pc[m] = "Ready"
    THEN IF nsDirty[m] /\ mode[m] # "Diverged" THEN FirstStep ELSE "None"
    ELSE pc[m]

\* Whether the bucket would apply m's swap to the root pointer naming k. An
\* empty rootETag is If-None-Match (IfNoneMatchInit, or unconditional
\* without it: Assumption J8); any other is If-Match.
SwapCondition(m, k) ==
  IF rootETag[m] = NoETag
    THEN \/ bucketRoot.key = NoKey
         \/ "IfNoneMatchInit" \notin Fixes /\ k.epoch = 1
    ELSE bucketRoot.etag = rootETag[m]

BucketVars == <<bucketRoot, snapshots, chunks>>
MountVars  == <<pc, mode, rootETag, epoch, nsDirty, attempt, rpSnapshot, base>>
PruneVars  == <<pruning, pruneCurrent, pruneDoomed>>
GhostVars  == <<acked, used, overtaken>>
Vars       == <<BucketVars, MountVars, PruneVars, GhostVars, crashes>>

\* m writes the root pointer, naming k.
WriteRoot(m, k) ==
  /\ bucketRoot' = [key |-> k, etag |-> ETagOf(k, m), writer |-> m]
  /\ used' = used \cup {k}

\* commitLocked returns an error. initEmpty's commit failing makes New fail.
CommitFails(m, k) ==
  /\ pc' = [pc EXCEPT ![m] = IF k.epoch = 1 THEN "Failed" ELSE "Ready"]
  /\ attempt' = [attempt EXCEPT ![m] = NoKey]

\* The chunk of m's commit at k's epoch is stored (Assumption J2).
FlushEffect(m, k) ==
  chunks' = IF k.epoch = 1 THEN chunks ELSE chunks \cup {CommitIdOf(m, k)}

\* StartPrune: schedulePrune, in the hold of FS.mu of the success path that
\* calls it (R8): unless pruning is off or a pass is running, a pass starts
\* for epoch e.
\* Go (trace.tsv): FS.schedulePrune.
StartPrune(m, e) ==
  IF Retention > 0 /\ pruning[m] = "Idle"
    THEN /\ pruning' = [pruning EXCEPT ![m] = "Listing"]
         /\ pruneCurrent' = [pruneCurrent EXCEPT ![m] = e]
         /\ UNCHANGED pruneDoomed
    ELSE UNCHANGED PruneVars

\* commitLocked's success path, with the root pointer's new ETag et.
Committed(m, k, et) ==
  /\ epoch' = [epoch EXCEPT ![m] = k.epoch]
  /\ rootETag' = [rootETag EXCEPT ![m] = et]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = FALSE]
  /\ base' = [base EXCEPT ![m] = @ \cup {CommitIdOf(m, k)}]
  /\ acked' = acked \cup {CommitIdOf(m, k)}
  /\ StartPrune(m, k.epoch)
  /\ IF "ChunksFirst" \in Fixes
       THEN /\ pc' = [pc EXCEPT ![m] = "Ready"]
            /\ attempt' = [attempt EXCEPT ![m] = NoKey]
       ELSE /\ pc' = [pc EXCEPT ![m] = "PutSnapshot"]
            /\ attempt' = [attempt EXCEPT ![m] = k]

\* FallbackPut: commitLocked's unconditional Put of the root pointer after
\* store.ErrUnsupported, merged with SwapUnsupported (R7).
\* Go (trace.tsv): FS.commitLocked; ReadOnly.Put.
FallbackPut(m, k) ==
  /\ WriteRoot(m, k)
  /\ overtaken' = [x \in Mounts |-> IF x = m THEN overtaken[m] ELSE TRUE]

-----------------------------------------------------------------------------
\* Types.

TypeOK ==
  /\ bucketRoot \in [key : Keys \cup {NoKey}, etag : ETags, writer : Writers]
  /\ bucketRoot.etag = IF bucketRoot.key = NoKey
                         THEN NoETag
                         ELSE ETagOf(bucketRoot.key, bucketRoot.writer)
  /\ DOMAIN snapshots \subseteq Keys
  /\ \A k \in DOMAIN snapshots : snapshots[k] \subseteq CommitIds
  /\ chunks \subseteq CommitIds
  /\ DOMAIN pc = Mounts
  /\ DOMAIN mode = Mounts
  /\ DOMAIN rootETag = Mounts
  /\ DOMAIN epoch = Mounts
  /\ DOMAIN nsDirty = Mounts
  /\ DOMAIN attempt = Mounts
  /\ DOMAIN rpSnapshot = Mounts
  /\ DOMAIN base = Mounts
  /\ DOMAIN pruning = Mounts
  /\ DOMAIN pruneCurrent = Mounts
  /\ DOMAIN pruneDoomed = Mounts
  /\ DOMAIN overtaken = Mounts
  /\ \A m \in Mounts :
       /\ pc[m] \in {"GetRoot", "HeadRoot", "GetSnapshot", "Ready",
                     "PutSnapshot", "Swap", "FallbackHead", "Flush", "Failed"}
       /\ mode[m] \in {"Writable", "ReadOnly", "Diverged"}
       /\ rootETag[m] \in ETags
       /\ epoch[m] \in 0..MaxEpoch
       /\ nsDirty[m] \in BOOLEAN
       /\ attempt[m] \in Keys \cup {NoKey}
       /\ rpSnapshot[m] \in Keys \cup {NoKey}
       /\ base[m] \subseteq CommitIds
       /\ pruning[m] \in {"Idle", "Listing", "Deleting"}
       /\ pruneCurrent[m] \in 0..MaxEpoch
       /\ pruneDoomed[m] \subseteq Keys
       /\ overtaken[m] \in BOOLEAN
  /\ acked \subseteq CommitIds
  /\ used \subseteq Keys
  /\ crashes \in 0..MaxCrashes

-----------------------------------------------------------------------------
\* The initial state.

\* Init: New, before its first store call. New sets FS.readOnly from
\* Config.ReadOnly and puts a read-only mount's store behind store.ReadOnly.
\* With Start = "Empty" the bucket is empty and every mount is at
\* loadOrInit's first read. With Start = "Loaded" the bucket holds a
\* filesystem at epoch 1, written outside the model, which every mount has
\* loaded (Assumption J5).
\* Go (trace.tsv): New.
Init ==
  /\ chunks = {}
  /\ mode = [m \in Mounts |-> InitialMode(m)]
  /\ nsDirty = [m \in Mounts |-> FALSE]
  /\ attempt = [m \in Mounts |-> NoKey]
  /\ rpSnapshot = [m \in Mounts |-> NoKey]
  /\ pruning = [m \in Mounts |-> "Idle"]
  /\ pruneCurrent = [m \in Mounts |-> 0]
  /\ pruneDoomed = [m \in Mounts |-> {}]
  /\ acked = {}
  /\ overtaken = [m \in Mounts |-> FALSE]
  /\ crashes = 0
  /\ IF Start = "Empty"
       THEN /\ bucketRoot = [key |-> NoKey, etag |-> NoETag, writer |-> ""]
            /\ snapshots = [k \in {} |-> {}]
            /\ used = {}
            /\ pc = [m \in Mounts |-> "GetRoot"]
            /\ rootETag = [m \in Mounts |-> NoETag]
            /\ epoch = [m \in Mounts |-> 0]
            /\ base = [m \in Mounts |-> {}]
       ELSE /\ bucketRoot = [key |-> LoadedKey,
                             etag |-> ETagOf(LoadedKey, ""),
                             writer |-> ""]
            /\ snapshots = [k \in {LoadedKey} |-> {LoadedCommit}]
            /\ used = {LoadedKey}
            /\ pc = [m \in Mounts |-> "Ready"]
            /\ rootETag = [m \in Mounts |-> ETagOf(LoadedKey, "")]
            /\ epoch = [m \in Mounts |-> 1]
            /\ base = [m \in Mounts |-> {LoadedCommit}]

-----------------------------------------------------------------------------
\* Loading.

\* GetRoot: loadOrInit's Get of the root pointer, and its parse, when the
\* bucket holds one. It records the snapshot key the root pointer names.
\* Go (trace.tsv): FS.loadOrInit.
GetRoot(m) ==
  /\ pc[m] = "GetRoot"
  /\ bucketRoot.key # NoKey
  /\ rpSnapshot' = [rpSnapshot EXCEPT ![m] = bucketRoot.key]
  /\ pc' = [pc EXCEPT ![m] = "HeadRoot"]
  /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty, attempt, base,
                 PruneVars, GhostVars, crashes>>

\* RefuseEmpty: loadOrInit's not-found branch on a read-only mount, which
\* makes New fail, having written nothing (ADR 0011 section 2).
\* Go (trace.tsv): FS.loadOrInit.
RefuseEmpty(m) ==
  /\ pc[m] = "GetRoot"
  /\ bucketRoot.key = NoKey
  /\ m \in ReadOnlyMounts
  /\ pc' = [pc EXCEPT ![m] = "Failed"]
  /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty, attempt,
                 rpSnapshot, base, PruneVars, GhostVars, crashes>>

\* InitEmpty: loadOrInit's not-found branch on a writable mount, calling
\* initEmpty, which sets epoch 0 and an empty rootETag and then commits.
\* Go (trace.tsv): FS.loadOrInit; FS.initEmpty.
InitEmpty(m) ==
  /\ pc[m] = "GetRoot"
  /\ bucketRoot.key = NoKey
  /\ m \notin ReadOnlyMounts
  /\ pc' = [pc EXCEPT ![m] = FirstStep]
  /\ epoch' = [epoch EXCEPT ![m] = 0]
  /\ rootETag' = [rootETag EXCEPT ![m] = NoETag]
  /\ base' = [base EXCEPT ![m] = {}]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = FALSE]
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ UNCHANGED <<BucketVars, mode, attempt, rpSnapshot, PruneVars, acked,
                 used, crashes>>

\* HeadRoot: loadOrInit's Head of the root pointer, whose ETag becomes
\* FS.rootETag (R2). Another writer's swap between GetRoot and HeadRoot is
\* #89's window.
\* Go (trace.tsv): FS.loadOrInit.
HeadRoot(m) ==
  /\ pc[m] = "HeadRoot"
  /\ rootETag' = [rootETag EXCEPT ![m] = bucketRoot.etag]
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ pc' = [pc EXCEPT ![m] = "GetSnapshot"]
  /\ UNCHANGED <<BucketVars, mode, epoch, nsDirty, attempt, rpSnapshot, base,
                 PruneVars, acked, used, crashes>>

\* GetSnapshot: loadOrInit's Get of the snapshot the root pointer named, and
\* decodeSnapshot. A missing snapshot makes New fail with its "read snapshot"
\* error, which is #88's consequence.
\* Go (trace.tsv): FS.loadOrInit; decodeSnapshot.
GetSnapshot(m) ==
  /\ pc[m] = "GetSnapshot"
  /\ rpSnapshot' = [rpSnapshot EXCEPT ![m] = NoKey]
  /\ IF rpSnapshot[m] \in DOMAIN snapshots
       THEN /\ base' = [base EXCEPT ![m] = snapshots[rpSnapshot[m]]]
            /\ epoch' = [epoch EXCEPT ![m] = rpSnapshot[m].epoch]
            /\ pc' = [pc EXCEPT ![m] = "Ready"]
       ELSE /\ pc' = [pc EXCEPT ![m] = "Failed"]
            /\ UNCHANGED <<base, epoch>>
  /\ UNCHANGED <<BucketVars, mode, rootETag, nsDirty, attempt, PruneVars,
                 GhostVars, crashes>>

-----------------------------------------------------------------------------
\* Namespace changes.

\* Mutate: mutable(), folded with the hold of FS.mu in which a change sets
\* FS.dirty (R3, R4).
\* Go (trace.tsv): FS.mutable; FS.SetAttr; FS.createEntry; FS.unlink;
\* FS.Rename; FS.bufferWrite; FS.truncate.
Mutate(m) ==
  /\ pc[m] = "Ready"
  /\ Mutable(m)
  /\ ~nsDirty[m]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = TRUE]
  /\ UNCHANGED <<BucketVars, pc, mode, rootETag, epoch, attempt, rpSnapshot,
                 base, PruneVars, GhostVars, crashes>>

\* StrayDirty: a path that dirties a read-only mount (Assumption J6).
\* Go: none; trace.tsv has no row for it.
StrayDirty(m) ==
  /\ pc[m] = "Ready"
  /\ m \in ReadOnlyMounts
  /\ ~nsDirty[m]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = TRUE]
  /\ UNCHANGED <<BucketVars, pc, mode, rootETag, epoch, attempt, rpSnapshot,
                 base, PruneVars, GhostVars, crashes>>

-----------------------------------------------------------------------------
\* Commits.

\* PutSnapshot: commitLocked's refusal once diverged, its encode and nonce,
\* and its Put of the snapshot. With ChunksFirst it includes Flush (R6).
\* store.ReadOnly refuses the Put when the mount is fenced. commitLocked's
\* refusal while a file is unsettled (ADR 0012 section 4) is FileData's.
\* Go (trace.tsv): FS.commitLocked; ReadOnly.Put.
PutSnapshot(m) ==
  /\ NextStep(m) = "PutSnapshot"
  /\ \E k \in KeyFor(m) :
       \/ /\ (Fenced(m) \/ "PutFailed" \in Faults)
          /\ CommitFails(m, k)
          /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty,
                         rpSnapshot, base, PruneVars, GhostVars, crashes>>
       \/ /\ ~Fenced(m)
          /\ snapshots' = [x \in DOMAIN snapshots \cup {k} |->
                             IF x = k THEN base[m] \cup {CommitIdOf(m, k)}
                                      ELSE snapshots[x]]
          /\ used' = used \cup {k}
          /\ IF "ChunksFirst" \in Fixes
               THEN /\ FlushEffect(m, k)
                    /\ pc' = [pc EXCEPT ![m] = "Swap"]
                    /\ attempt' = [attempt EXCEPT ![m] = k]
               ELSE /\ UNCHANGED chunks
                    /\ pc' = [pc EXCEPT ![m] =
                                IF k.epoch = 1 THEN "Ready" ELSE "Flush"]
                    /\ attempt' = [attempt EXCEPT ![m] =
                                     IF k.epoch = 1 THEN NoKey ELSE k]
          /\ UNCHANGED <<bucketRoot, mode, rootETag, epoch, nsDirty,
                         rpSnapshot, base, PruneVars, acked, overtaken,
                         crashes>>

\* Flush: Sync's first pass and settle pass, flushOpen, and putChunk, as a
\* step of its own only without ChunksFirst (Assumption J7).
\* Go (trace.tsv): FS.Sync; FS.flushAll; FS.settleAll; FS.flushOpen;
\* FS.putChunk.
Flush(m) ==
  /\ pc[m] = "Flush"
  /\ FlushEffect(m, attempt[m])
  /\ pc' = [pc EXCEPT ![m] = "Ready"]
  /\ attempt' = [attempt EXCEPT ![m] = NoKey]
  /\ UNCHANGED <<bucketRoot, snapshots, mode, rootETag, epoch, nsDirty,
                 rpSnapshot, base, PruneVars, GhostVars, crashes>>

\* SwapOK: commitLocked's PutIfMatch succeeds, then its success path and
\* schedulePrune (R8).
\* Go (trace.tsv): FS.commitLocked; S3.PutIfMatch; Local.PutIfMatch.
SwapOK(m) ==
  /\ NextStep(m) = "Swap"
  /\ Capability \in {"Conditional", "Flaky"}
  /\ ~Fenced(m)
  /\ \E k \in KeyFor(m) :
       /\ SwapCondition(m, k)
       /\ WriteRoot(m, k)
       /\ overtaken' = [x \in Mounts |-> x # m]
       /\ Committed(m, k, ETagOf(k, m))
  /\ UNCHANGED <<snapshots, chunks, mode, rpSnapshot, crashes>>

\* SwapReplyLost: the swap is applied, but an error is returned, so
\* commitLocked takes its "case err != nil" branch and keeps its rootETag.
\* Go (trace.tsv): FS.commitLocked; S3.PutIfMatch; errorFor.
SwapReplyLost(m) ==
  /\ NextStep(m) = "Swap"
  /\ "SwapReplyLost" \in Faults
  /\ Capability \in {"Conditional", "Flaky"}
  /\ ~Fenced(m)
  /\ \E k \in KeyFor(m) :
       /\ SwapCondition(m, k)
       /\ WriteRoot(m, k)
       /\ overtaken' = [x \in Mounts |-> IF x = m THEN overtaken[m] ELSE TRUE]
       /\ CommitFails(m, k)
  /\ UNCHANGED <<snapshots, chunks, mode, rootETag, epoch, nsDirty,
                 rpSnapshot, base, PruneVars, acked, crashes>>

\* SwapFailed: the swap is not applied, and an error that is not typed is
\* returned.
\* Go (trace.tsv): FS.commitLocked; S3.PutIfMatch; errorFor;
\* Local.PutIfMatch.
SwapFailed(m) ==
  /\ NextStep(m) = "Swap"
  /\ "SwapFailed" \in Faults
  /\ ~Fenced(m)
  /\ \E k \in KeyFor(m) : CommitFails(m, k)
  /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty, rpSnapshot, base,
                 PruneVars, GhostVars, crashes>>

\* SwapConflict: store.ErrPrecondition, from errorFor's 412 or 409 or from
\* Local's ETag check, and commitLocked's diverged.Store(true). With
\* SwapConflict409 it may also come while the condition holds. A read-only
\* mount's mode stays "ReadOnly" (R11).
\* Go (trace.tsv): FS.commitLocked; errorFor; Local.PutIfMatch.
SwapConflict(m) ==
  /\ NextStep(m) = "Swap"
  /\ Capability \in {"Conditional", "Flaky"}
  /\ ~Fenced(m)
  /\ \E k \in KeyFor(m) :
       /\ (~SwapCondition(m, k) \/ "SwapConflict409" \in Faults)
       /\ mode' = [mode EXCEPT ![m] = IF @ = "Writable" THEN "Diverged" ELSE @]
       /\ CommitFails(m, k)
  /\ UNCHANGED <<BucketVars, rootETag, epoch, nsDirty, rpSnapshot, base,
                 PruneVars, GhostVars, crashes>>

\* SwapUnsupported: store.ErrUnsupported, from errorFor's 501 or from
\* ReadOnly.PutIfMatch, then the fallback's unconditional Put of the root
\* pointer (FallbackPut, R7), which store.ReadOnly refuses when the mount is
\* fenced.
\* Go (trace.tsv): FS.commitLocked; errorFor; ReadOnly.PutIfMatch.
SwapUnsupported(m) ==
  /\ NextStep(m) = "Swap"
  /\ (Capability \in {"Unconditional", "Flaky"} \/ Fenced(m))
  /\ \E k \in KeyFor(m) :
       \/ /\ (Fenced(m) \/ "PutFailed" \in Faults)
          /\ CommitFails(m, k)
          /\ UNCHANGED <<BucketVars, GhostVars>>
       \/ /\ ~Fenced(m)
          /\ FallbackPut(m, k)
          /\ pc' = [pc EXCEPT ![m] = "FallbackHead"]
          /\ attempt' = [attempt EXCEPT ![m] = k]
          /\ UNCHANGED <<snapshots, chunks, acked>>
  /\ UNCHANGED <<mode, rootETag, epoch, nsDirty, rpSnapshot, base, PruneVars,
                 crashes>>

\* FallbackHead: the Head of the root pointer after the fallback's Put, then
\* the success path. The Head may return another writer's ETag.
\* Go (trace.tsv): FS.commitLocked.
FallbackHead(m) ==
  /\ pc[m] = "FallbackHead"
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ Committed(m, attempt[m], bucketRoot.etag)
  /\ UNCHANGED <<BucketVars, mode, rpSnapshot, used, crashes>>

-----------------------------------------------------------------------------
\* Prune passes.

\* PruneList: pruneSnapshots' List loop and its early return, with the skip
\* of the current epoch's keys, which Go makes in its Delete loop (R9).
\* Go (trace.tsv): FS.pruneSnapshots.
PruneList(m) ==
  /\ pruning[m] = "Listing"
  /\ pruning' = [pruning EXCEPT ![m] = "Deleting"]
  /\ LET L == DOMAIN snapshots
     IN  pruneDoomed' = [pruneDoomed EXCEPT ![m] =
                           IF Cardinality(L) <= Retention
                             THEN {}
                             ELSE {k \in L \ Kept(L) :
                                     k.epoch # pruneCurrent[m]}]
  /\ UNCHANGED <<BucketVars, MountVars, pruneCurrent, GhostVars, crashes>>

\* PruneDelete: one Delete of pruneSnapshots' Delete loop, in ascending order
\* (Assumption J10).
\* Go (trace.tsv): FS.pruneSnapshots.
PruneDelete(m) ==
  /\ pruning[m] = "Deleting"
  /\ pruneDoomed[m] # {}
  /\ LET k == Least(pruneDoomed[m])
     IN  /\ snapshots' = [x \in DOMAIN snapshots \ {k} |-> snapshots[x]]
         /\ pruneDoomed' = [pruneDoomed EXCEPT ![m] = @ \ {k}]
  /\ UNCHANGED <<bucketRoot, chunks, MountVars, pruning, pruneCurrent,
                 GhostVars, crashes>>

\* PruneEnd: the goroutine's final hold of FS.mu, which clears FS.pruning
\* (R10).
\* Go (trace.tsv): FS.schedulePrune.
PruneEnd(m) ==
  /\ pruning[m] = "Deleting"
  /\ pruneDoomed[m] = {}
  /\ pruning' = [pruning EXCEPT ![m] = "Idle"]
  /\ UNCHANGED <<BucketVars, MountVars, pruneCurrent, pruneDoomed, GhostVars,
                 crashes>>

-----------------------------------------------------------------------------
\* Crashes.

\* Crash: process death, or an exit that skips the final commit (ADR 0013
\* section 4). The mount's memory and its prune goroutine are lost, every
\* store call that took effect stays, and the restart is New, at
\* loadOrInit's first read (Assumption J9).
\* Go (trace.tsv): New.
Crash(m) ==
  /\ crashes < MaxCrashes
  /\ crashes' = crashes + 1
  /\ pc' = [pc EXCEPT ![m] = "GetRoot"]
  /\ mode' = [mode EXCEPT ![m] = InitialMode(m)]
  /\ rootETag' = [rootETag EXCEPT ![m] = NoETag]
  /\ epoch' = [epoch EXCEPT ![m] = 0]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = FALSE]
  /\ attempt' = [attempt EXCEPT ![m] = NoKey]
  /\ rpSnapshot' = [rpSnapshot EXCEPT ![m] = NoKey]
  /\ base' = [base EXCEPT ![m] = {}]
  /\ pruning' = [pruning EXCEPT ![m] = "Idle"]
  /\ pruneCurrent' = [pruneCurrent EXCEPT ![m] = 0]
  /\ pruneDoomed' = [pruneDoomed EXCEPT ![m] = {}]
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ UNCHANGED <<BucketVars, acked, used>>

-----------------------------------------------------------------------------
\* The specification.

Next ==
  \E m \in Mounts :
    \/ GetRoot(m)
    \/ RefuseEmpty(m)
    \/ InitEmpty(m)
    \/ HeadRoot(m)
    \/ GetSnapshot(m)
    \/ Mutate(m)
    \/ StrayDirty(m)
    \/ PutSnapshot(m)
    \/ Flush(m)
    \/ SwapOK(m)
    \/ SwapReplyLost(m)
    \/ SwapFailed(m)
    \/ SwapConflict(m)
    \/ SwapUnsupported(m)
    \/ FallbackHead(m)
    \/ PruneList(m)
    \/ PruneDelete(m)
    \/ PruneEnd(m)
    \/ Crash(m)

Spec == Init /\ [][Next]_Vars

-----------------------------------------------------------------------------
\* Properties.

\* The root pointer names a snapshot the bucket holds.
RootSnapshotExists ==
  bucketRoot.key # NoKey => bucketRoot.key \in DOMAIN snapshots

\* Every acknowledged commit is in the snapshot the root pointer names.
NoLostAck ==
  bucketRoot.key \in DOMAIN snapshots =>
    acked \subseteq snapshots[bucketRoot.key]

\* Every commit in the snapshot the root pointer names has its chunk stored;
\* an epoch-1 commit names none (Assumption J2).
ChunksBeforeRoot ==
  bucketRoot.key \in DOMAIN snapshots =>
    \A c \in snapshots[bucketRoot.key] : c.epoch = 1 \/ c \in chunks

\* No root pointer, snapshot or chunk holds a read-only mount's write. A
\* read-only mount could delete only through a prune pass, which starts only
\* after one of its writes of the root pointer, and that write already
\* breaks this.
ReadOnlyNeverWrites ==
  /\ bucketRoot.writer \notin ReadOnlyMounts
  /\ \A k \in DOMAIN snapshots :
       \A c \in snapshots[k] : c.writer \notin ReadOnlyMounts
  /\ \A c \in chunks : c.writer \notin ReadOnlyMounts

\* A mount diverges only after another mount has written the root pointer
\* since it last set rootETag.
NoSelfDivergence ==
  \A m \in Mounts : mode[m] = "Diverged" => overtaken[m]

-----------------------------------------------------------------------------
\* State constraints.

\* PassApartFromOwnCommits: while a mount's prune pass runs, the mount
\* completes no commit, and the pass has not deleted the snapshot of an
\* attempt the mount has still to swap. So only other mounts' commits
\* overlap a pass. Only CommitProtocol_PruneTwoWriters sets it (Assumption
\* J15). TLC checks the invariants of a state that breaks a constraint, and
\* takes no step from it, so the second conjunct stops the mount's swap to a
\* snapshot its own pass deleted before TLC can take that swap.
PassApartFromOwnCommits ==
  \A m \in Mounts :
    pruning[m] # "Idle" =>
      /\ epoch[m] = pruneCurrent[m]
      /\ ((pc[m] = "Swap" /\ attempt[m] # NoKey) =>
            attempt[m] \in DOMAIN snapshots)

=============================================================================
