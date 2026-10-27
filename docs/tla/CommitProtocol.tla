---------------------------- MODULE CommitProtocol ----------------------------
\* How a mount of this filesystem reads the root pointer, commits, prunes
\* snapshots and diverges, against other mounts, failing store calls and
\* crashes. ADR 0013 section 1 names this module and what it must show: that
\* no acknowledged commit is lost (NoLostAck); that the root pointer never
\* names a snapshot or a chunk the bucket does not hold (RootSnapshotExists,
\* ChunksBeforeRoot); that a read-only mount writes nothing, read as ADR 0011
\* reads it (ReadOnlyNeverWrites, Assumption J16); and that a mount diverges
\* only after another writer (NoSelfDivergence). docs/tla/trace.tsv gives the
\* Go symbols that perform each action, and docs/vocabulary.md, V3, gives
\* every name.
\*
\* Checked against faeba5b, for every action but those ADR 0022 adds or
\*   changes (SwapNoETag, SwapUnknown, the four Probe actions, and the swap
\*   outcomes that conjoin Unresolved), whose trace awaits coder's commit for
\*   that ADR; this line is bumped once it is on the branch (ADR 0022,
\*   Assumption 20).
\* Revised: 2026-10-24 -- ADR 0019 (#88): with the Fixes member
\*   PruneBelowCurrent, a prune pass deletes only the keys that sort below the
\*   one its commit swapped the root pointer to, and keeps the Retention newest
\*   of them (Below, Prunable; PruneList consults the member). pruneCurrent now
\*   holds that key, where it held its epoch, so StartPrune takes a key and
\*   PassApartFromOwnCommits compares epochs through it. Without the member the
\*   module describes pruneSnapshots as it is at 13aebc4, as before. J11, J14
\*   and J15 are rewritten for the fix. Nothing else moved.
\* Revised: 2026-10-24 -- ADR 0020 (#89): with the Fixes member AtomicLoad,
\*   GetRoot records the root pointer's ETag with the key it names, from one
\*   read, and goes straight to GetSnapshot, so HeadRoot, #89's window, is
\*   never due. Without the member the module describes loadOrInit as it is
\*   at 55fc666, as before. R2, J11 and J13 are reworded, and the Leaves-out
\*   list names a read that returns no ETag. Nothing else moved.
\* Revised: 2026-10-25 -- ADR 0021 (#91): with the Fixes member
\*   FallbackOptIn, a swap the store answers 501 fails the commit and writes
\*   nothing more (SwapUnsupported's first unfenced branch), and the
\*   fallback's Put and Head follow a 501 only where FallbackAllowed, which
\*   is the member's absence: a configuration without it describes both
\*   commitLocked as it is at 247a276 and a mount whose operator set
\*   Config.AllowUnconditionalRoot, which behave alike (J17). The Scope and
\*   Leaves-out lists and R7 say so. Nothing else moved.
\* Revised: 2026-10-25 -- ADR 0021, on review: SwapUnsupported's refusal
\*   branch also removes the attempt's key from snapshots, the Delete by which
\*   commitLocked reclaims the snapshot a refused commit uploaded, after a
\*   read of the root pointer shows it names another key (ADR 0021 section
\*   2); the read and the Delete fold into the action (R12), and the
\*   Leaves-out list names a 501 that took effect. Nothing else moved.
\* Revised: 2026-10-27 -- ADR 0014 section 7, the mount mode's conversion
\*   (#114): Diverge moves a mount from "Writable" to "Diverged" and leaves
\*   any other mode as it is, as the Go table mountModes has that one edge
\*   and Fire refuses the rest, where it made a read-only mount "Diverged"
\*   too. R11 is rewritten for it, J18 says why it gets no Fixes member, and
\*   the comments of Init, Diverge and SwapConflict name FS.mode in place of
\*   FS.readOnly and FS.diverged. No configuration changes. Nothing else
\*   moved.
\* Revised: 2026-10-27 -- ADR 0022 (#5, #103): with the Fixes member
\*   RootProbe, a write of the root pointer whose reply does not establish
\*   its ETag leaves the mount's knowledge of the root pointer in doubt
\*   (SwapUnknown, the machine of docs/vocabulary.md V2, now modelled as
\*   rootKnowledge with doubtKey and rootSnapshot beside it), and one read of
\*   the root pointer settles it before any further write: ProbeOurs adopts
\*   the attempt, ProbeUnchanged takes the root pointer's ETag, ProbeTheirs
\*   is where Diverge is fired, and ProbeFailed leaves the doubt for the next
\*   commit. SwapReplyLost, SwapFailed, SwapConflict and the fallback's two
\*   failing branches conjoin Unresolved, which is SwapUnknown with the member
\*   and the old CommitFails, with Diverge for SwapConflict, without it. The
\*   faults ProbeFailed and SwapNoETag, the action SwapNoETag (#103) and the
\*   pc string "Probe" are added; NextStep gives "Probe" while the root is in
\*   doubt, so no snapshot Put or swap is enabled then; Committed records
\*   rootSnapshot. R11 is rewritten, R13 and R14 are added, J13 is rewritten,
\*   J19 and J20 are added, and the Leaves-out list says what Go's rule
\*   covers that the model does not reach. Without the member the module
\*   describes commitLocked as it is at edcfc15, as before. Nothing else
\*   moved.
\*
\* Scope
\*   - One bucket, and the mount processes in Mounts.
\*   - Loading: GetRoot, HeadRoot, GetSnapshot, RefuseEmpty and InitEmpty.
\*   - Namespace changes: Mutate, and StrayDirty (Assumption J6).
\*   - Commits: chunks, then the snapshot, then a conditional swap of the
\*     root pointer, with six store outcomes (SwapOK, SwapReplyLost,
\*     SwapFailed, SwapConflict, SwapUnsupported, SwapNoETag) and, after a
\*     501, either the refusal that FallbackOptIn adds, which writes nothing
\*     more, or the fallback where FallbackAllowed (FallbackPut, FallbackHead).
\*     The snapshot Put and the fallback's Put may each fail without taking
\*     effect (PutFailed), or take effect and fail (SnapshotReplyLost,
\*     FallbackReplyLost).
\*   - Root-pointer knowledge (ADR 0022): a write whose reply did not
\*     establish the root pointer's ETag leaves it in doubt (SwapUnknown),
\*     and one read settles it (ProbeOurs, ProbeUnchanged, ProbeTheirs) or
\*     fails to (ProbeFailed).
\*   - Divergence (Diverge).
\*   - Each mount's prune passes: StartPrune, PruneList, PruneDelete and
\*     PruneEnd.
\*   - Crashes.
\*
\* Leaves out
\*   - File data, the commit gate and its settle pass: FileData's.
\*   - The write budget, and NFS acknowledgements: NfsDurability's.
\*   - A read of the root pointer that returns no ETag at load, which with
\*     AtomicLoad makes New fail (ADR 0020 section 2). Here every root
\*     pointer has an ETag (Assumption J3); a swap reply that withholds it is
\*     SwapNoETag, and a probe's read that withholds it is, in Go, a failed
\*     probe (ADR 0022 section 3), which ProbeFailed stands for.
\*   - Failed reads at load: a Get or a Head of the root pointer, or a Get of
\*     the snapshot it names, failing other than with not-found. Each only
\*     makes New fail. A failed read of the root pointer by the probe is in
\*     (ProbeFailed).
\*   - Failed List and Delete calls (Assumption J14).
\*   - A Head failing after the fallback's Put. In Go that leaves the root in
\*     doubt and the probe follows, as a swap reply with no ETag does (ADR
\*     0022 section 4); here the Head always answers.
\*   - A bucket whose mounts differ in Config.AllowUnconditionalRoot: here
\*     every mount of a configuration is opted in or none is (J17).
\*   - A 501 answered for a reason other than the condition: here a 501 is
\*     the store's answer to the condition, and commitLocked treats every
\*     501 on the swap alike (ADR 0021).
\*   - A swap answered 501 that took effect: here a 501 changes no object
\*     (J4), so the read that guards the refused commit's Delete (ADR 0021
\*     section 2) always finds the root pointer naming another key, and the
\*     Delete always follows (R12). In Go, a guard read that shows the
\*     attempt's key, fails, or returns a body the mount does not understand
\*     leaves the root in doubt and the probe follows (ADR 0022 section 4),
\*     which this model does not reach.
\*   - A Put or a Delete taking effect more than once through the retries
\*     of S3.do, with other calls in between: here a call takes effect at
\*     most once, at its own step (ADR 0013 Assumption 5).
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
\*   R2.  GetRoot, with AtomicLoad, or HeadRoot, without it, assigns
\*        rootETag[m], which Go does at the end of loadOrInit. Nothing reads
\*        FS.rootETag before New returns.
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
\*        501 changes no object (docs/vocabulary.md, V3, allows this). With
\*        FallbackOptIn and no opt-in the action ends at that call, and no
\*        Put follows (ADR 0021 section 4).
\*   R8.  StartPrune runs in the same hold of FS.mu as the swap's success
\*        path, so it is part of SwapOK and of FallbackHead, and, with
\*        RootProbe, of ProbeOurs at the failure site, whose success path is
\*        the same code (ADR 0022 section 4).
\*   R9.  The List loop is one step: it is exactly one call below 1,000 keys,
\*        and every configuration stays far below that.
\*   R10. PruneEnd stays a step of its own, because it does not commute with
\*        schedulePrune's read of FS.pruning.
\*   R11. Diverge moves a "Writable" mount only, and leaves any other mode as
\*        it is: commitLocked, or since ADR 0022 resolveRoot, fires
\*        mountDiverge on FS.mode, whose table mountModes has one edge, from
\*        "Writable", so a read-only mount's mode is left "ReadOnly" and Go's
\*        Fire refuses, which commitLocked logs; the mount, still dirty,
\*        tries its commit again as a writable mount does after any failed
\*        swap. Only a configuration without ReadOnlyFence reaches the case,
\*        since store.ReadOnly answers every write of a fenced mount
\*        store.ErrUnsupported, never store.ErrPrecondition. Without
\*        RootProbe, SwapConflict conjoins Diverge, as commitLocked's
\*        store.ErrPrecondition case fired it at edcfc15; with it, only
\*        ProbeTheirs does, since no swap's error alone diverges a mount
\*        (ADR 0022 section 1). Before the conversion, at 043927f,
\*        commitLocked set FS.diverged on any store.ErrPrecondition, whatever
\*        FS.readOnly was, and this module made a read-only mount "Diverged"
\*        too.
\*   R12. With FallbackOptIn and no opt-in, the swap's 501, commitLocked's
\*        read of the root pointer and its Delete of the attempt's snapshot
\*        are one action, SwapUnsupported's refusal branch, by the direct
\*        argument ADR 0013 section 4 allows. The read's answer is fixed: the
\*        501 changed nothing (J4), and no write of another mount can make
\*        the root pointer name this mount's key, which carries its own
\*        nonce, so the Delete always follows. Of the steps another mount can
\*        take between the calls, only a PruneList that lists the key fails
\*        to commute with the Delete, and moving the Delete before it changes
\*        only whether that key and, through Kept, at most one older key
\*        below that pass's own key appear in its doomed set; no root pointer
\*        names either, so no property here tells the two orders apart. A
\*        crash between the calls leaves the orphan, as a crash before the
\*        swap would, and so does a failed Delete (J14).
\*   R13. With RootProbe, the probe's read of the root pointer is a store call
\*        of its own, so it is a step (ADR 0013 section 4): pc "Probe" lies
\*        between a failed write and the read, and another mount or this
\*        mount's prune goroutine may act between them, which is exactly what
\*        the verdict has to survive. classifyRoot and the Fire that follows
\*        fold into the read: they touch only state FS.mu protects, which
\*        commitLocked holds throughout (R4), so the read is the one access
\*        outside that protection.
\*   R14. At the top of a commit the read runs in the same hold as the
\*        snapshot Put that follows it, and the model keeps them as two steps
\*        from "Ready" with nsDirty set, which only keeps Mutate out (R4, and
\*        Mutate needs ~nsDirty), while other mounts and the prune goroutine
\*        may act between them, as they may in Go between any two store calls.
\*        On ProbeFailed at "Ready" nothing moves, which is a stuttering step.
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
\*        one key twice, which takes an unconditional write: the fallback's
\*        Put, or initEmpty's swap without IfNoneMatchInit (J8). The one
\*        configuration that removes Nonce, CommitProtocol_NoNonce, is
\*        "Conditional" and keeps IfNoneMatchInit, so it makes neither.
\*   J4.  The fault meanings given with the constants below. The 409
\*        over-approximates the 409 Conflict that AWS documents for
\*        concurrent requests, as ADR 0013 Assumption 5 asks.
\*        SnapshotReplyLost and FallbackReplyLost are S3.Put's: an attempt
\*        can take effect and the call still return an error, since its
\*        reply can be lost and S3.do sends it again after a transport
\*        error, a 5xx or a 429. Local.Put cannot, since its last step is
\*        the rename that applies it. SwapNoETag is S3.PutIfMatch's: a 200
\*        whose ETag header is missing (#103). ProbeFailed is GetWithETag's,
\*        from a transport error, a 5xx after S3.do's retries, or a body the
\*        mount does not understand (ADR 0022 section 3).
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
\*   J11. The MustHold envelope is a reading, not a measurement. Without
\*        PruneBelowCurrent, pruning is shown safe only for one writer at
\*        Retention 2 with no fault at all, within the bounds of
\*        CommitProtocol_PruneSingle, and a fault that leaves a snapshot the
\*        root pointer does not name sorting above the one it names reached
\*        #88 by five routes, which eight configurations showed until ADR
\*        0019 flipped them: a failed swap's orphan (route 1,
\*        CommitProtocol_PruneOrphan); a losing writer's attempt (route 2,
\*        CommitProtocol_PruneTwoWriters); the mount's own unswapped next
\*        attempt (route 3, CommitProtocol_PruneOverlap); routes 2 and 3 at
\*        once (CommitProtocol_PruneOverlapTwoWriters); the orphan of a
\*        snapshot Put that takes effect and fails (route 4,
\*        CommitProtocol_PruneSnapshotReply), also at Retention 2 within
\*        PruneSingle's own bounds together with route 3's overlap
\*        (CommitProtocol_PruneSnapshotReplyRetention2), so that no fix could
\*        rest on a Retention above 1; and a root write that takes effect
\*        and fails, whose retry sorts above it (route 5,
\*        CommitProtocol_PruneSwapReply, CommitProtocol_PruneFallbackReply).
\*        With PruneBelowCurrent, the eight hold, and pruning is also shown
\*        safe with every property for one writer at Retention 1 with failed
\*        swaps, failed Puts, a snapshot Put that takes effect and fails, and
\*        a crash (CommitProtocol_PruneFaultsSingle), and for two writers that
\*        loaded one root pointer with the same faults
\*        (CommitProtocol_PruneTwoWritersFaults). The argument behind the
\*        member is ADR 0019 section 1: the root pointer's key only rises
\*        while every swap is conditional, or while one writer uses the
\*        fallback, so nothing below the key a pass was begun for is named
\*        again. Where that fails, two writers on the fallback or #89's
\*        window, the model shows the loss by NoLostAck first. AtomicLoad
\*        closes the window (ADR 0020): CommitProtocol_PruneTwoWritersFromEmpty
\*        shows every property for two writers from an empty bucket, each
\*        pruning at Retention 1, with three nonces, failed swaps, failed
\*        Puts, a snapshot Put that takes effect and fails, and a crash; and
\*        CommitProtocol_NoAtomicLoadPrune, without the member, shows the
\*        residual ADR 0019 Assumption 4 left: a swap through the window
\*        names a key below the one a pass was begun for, a failed attempt's
\*        orphan above it fills the retention, and the pass deletes the root
\*        pointer's snapshot. With RootProbe (ADR 0022), the envelope also
\*        covers lost swap replies, failed reads and a crash for one writer
\*        pruning at Retention 1 (CommitProtocol_PruneProbe): an adopted
\*        attempt is the key the root pointer already names, so it is at or
\*        above every pass's key, and the swap after it is one epoch higher.
\*   J12. The configurations' bounds and tiers are judgement calls.
\*   J13. rootKnowledge is modelled since ADR 0022, with doubtKey and
\*        rootSnapshot beside it, and the four Probe actions consult
\*        RootProbe through NextStep: only with the member can a mount reach
\*        "InDoubt". AtomicLoad is, since ADR 0020: GetRoot consults it.
\*   J14. Failed List and Delete calls are left out, and ADR 0019's argument
\*        does not need them: a List or a Delete that fails deletes nothing,
\*        a Delete that takes effect and is sent again deletes the same key,
\*        and no property here is harmed by a key that stays, since only a
\*        Delete can make RootSnapshotExists false. A failed Delete leaves a
\*        key below the one its pass was begun for, which a later pass
\*        deletes. So no configuration widens for them (ADR 0019 section 4).
\*   J15. CommitProtocol_PruneTwoWriters sets the state constraint
\*        PassApartFromOwnCommits, and no other configuration sets one. Its
\*        two writers also admit, at the same length as #88's route 2,
\*        behaviours in which a pass overlaps its own mount's next commit,
\*        the trigger of routes 1 and 3, and TLC reported one of them, which
\*        also ran through #89's window. No bound removes them, since the
\*        mount whose pass it is can always commit at epoch 2. The
\*        constraint only stops TLC taking a step from a state that breaks
\*        it. CommitProtocol_PruneOverlapTwoWriters is the same configuration
\*        without the constraint, so it checks what the constraint cuts, a
\*        pass that overlaps its own mount's commit and another mount's
\*        attempt at once. Both showed #88 and flipped together with ADR
\*        0019, PruneBelowCurrent added to both: once a MustHold
\*        configuration sets a constraint, the runner fails the gate unless a
\*        MustHold configuration whose lines are its own without the
\*        constraint exists too (docs/tla/README.md, Configuration files),
\*        and the pair is what that rule was written for.
\*   J16. ReadOnlyNeverWrites is about calls, as ADR 0011 sections 3 and 4
\*        put "writes nothing": no Put, PutIfMatch or Delete of a read-only
\*        mount reaches the bucket. A call that reaches it breaks the
\*        property whether or not it changes an object, and a call that
\*        store.ReadOnly refuses reaches nothing. Every object write is such
\*        a call, so it also implies that no object holds a read-only
\*        mount's write.
\*   J17. The operator's opt-in to the unconditional fallback,
\*        Config.AllowUnconditionalRoot (ADR 0021 section 2), is modelled as
\*        the absence of FallbackOptIn, not as a constant naming the opted-in
\*        mounts. With the opt-in commitLocked behaves after a 501 exactly as
\*        it did at 247a276, so a configuration without the member describes
\*        both a build without the fix and a build with it whose mounts all
\*        have the opt-in, and a configuration with the member a build with
\*        the fix whose mounts all lack it. A constant would have made every
\*        existing configuration assign it, which ADR 0013 section 7 does not
\*        allow. A bucket whose mounts differ in the opt-in is left out: it
\*        is one where the operator's assertion of a single writer is false,
\*        and CommitProtocol_NoFallbackOptIn and
\*        CommitProtocol_UnconditionalTwoWriters already show what that loses.
\*   J18. The conversion's Diverge gets no Fixes member. A member describes
\*        the code with and without a fix and gets a MustFail configuration
\*        that removes it (ADR 0013 section 7); the guarded Diverge fixes no
\*        defect, and no property here tells it from the old form, since the
\*        one configuration that reaches a read-only mount's conflict,
\*        CommitProtocol_NoReadOnlyFence, has violated ReadOnlyNeverWrites
\*        at its first write call, before any swap, and NoSelfDivergence
\*        looks only at "Diverged". A member would add a configuration pair
\*        and a calibration that show nothing (ADR 0014, Assumption 40).
\*   J19. The probe's verdict is by the key the root pointer names, not by
\*        its ETag, and the ETag the read returns is what the mount takes
\*        (ADR 0022 section 3, Assumption 3). ProbeOurs needs
\*        bucketRoot.key = doubtKey[m], which with Nonce only this mount's
\*        write can have made; ProbeUnchanged needs bucketRoot.key =
\*        rootSnapshot[m], which no other mount's write can restore, since
\*        every write names a fresh key, so it never takes another writer's
\*        ETag; ProbeTheirs is the rest. NoLostAck and NoSelfDivergence
\*        together pin the three in CommitProtocol_ProbeTwoWriters: a verdict
\*        too eager to adopt or to proceed breaks the first, one too eager to
\*        diverge breaks the second.
\*   J20. ProbeOurs at the failure site is Committed, the same success path
\*        as SwapOK's, since the snapshot the root pointer names holds the
\*        namespace as it stood in the same hold (ADR 0022 Assumption 9). At
\*        the top of a later commit it adopts only: epoch, rootETag and base
\*        take the attempt, and nsDirty, acked and the pass are left to the
\*        commit that follows. A clean in-doubt mount is unreachable, since
\*        every path to "InDoubt" leaves nsDirty set, so NextStep's "Probe"
\*        needs nsDirty as a commit does.

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
\*   Fault "SwapNoETag": a swap is applied, and a 200 is returned whose ETag
\*     header is missing, so S3.PutIfMatch returns "" (#103).
\*   Fault "PutFailed": a snapshot Put, with the chunk uploads folded before
\*     it, or the fallback's root Put, is not applied.
\*   Fault "SnapshotReplyLost": a snapshot Put is applied, with the chunk
\*     uploads folded before it, and an error is returned: an attempt took
\*     effect, but its reply was lost, or S3.do's later attempts failed, or
\*     the context ended.
\*   Fault "FallbackReplyLost": the fallback's root Put is applied, and an
\*     error is returned, in the same ways.
\*   Fault "ProbeFailed": the probe's read of the root pointer, GetWithETag,
\*     fails, or returns a body the mount does not understand (ADR 0022
\*     section 3), so the root stays in doubt.

\* What the Fixes members mean (docs/vocabulary.md, V3, gives each one's Go):
\*   IfNoneMatchInit, Nonce, ChunksFirst, ReadOnlyFence: in the code since
\*     before this module (their configuration comments give each source).
\*   PruneBelowCurrent: ADR 0019 section 2. A prune pass deletes only the
\*     snapshot keys that sort below the one its commit swapped the root
\*     pointer to, and keeps the Retention newest of them (Prunable). Without
\*     it the pass deletes every listed key but the Retention newest, sparing
\*     only the keys of the epoch it was begun for, as at 13aebc4.
\*   AtomicLoad: ADR 0020 section 2. loadOrInit reads the root pointer's body
\*     and its ETag with one call, GetWithETag, so GetRoot records both and
\*     HeadRoot is never due. Without it loadOrInit reads the body with Get
\*     and the ETag with a later Head, as at 55fc666.
\*   FallbackOptIn: ADR 0021 section 2. commitLocked, after
\*     store.ErrUnsupported from the swap, writes the root pointer
\*     unconditionally only on a mount whose operator set
\*     Config.AllowUnconditionalRoot, and otherwise fails the commit with
\*     nothing more written. Without it, or on an opted-in mount, which
\*     behave alike (J17), the fallback's Put and Head follow every 501, as
\*     at 247a276.
\*   RootProbe: ADR 0022 sections 1 to 4. A write of the root pointer whose
\*     reply did not establish its ETag leaves the mount in doubt, and it
\*     writes the root pointer no further until one read of it has shown the
\*     root pointer naming the attempt (adopted), the key the namespace
\*     descends from (its ETag taken), or another key (the mount diverges).
\*     Without it commitLocked keeps its old ETag after such a write and
\*     diverges on store.ErrPrecondition, as at edcfc15.

AllFixes  == {"IfNoneMatchInit", "Nonce", "ChunksFirst", "ReadOnlyFence",
              "PruneBelowCurrent", "AtomicLoad", "FallbackOptIn", "RootProbe"}
AllFaults == {"SwapReplyLost", "SwapFailed", "SwapConflict409", "SwapNoETag",
              "PutFailed", "SnapshotReplyLost", "FallbackReplyLost",
              "ProbeFailed"}

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
  rootKnowledge, \* FS.rootKnowledge: "Known" or "InDoubt" (ADR 0022)
  doubtKey,      \* FS.doubtKey: the attempt in doubt, or NoKey
  rootSnapshot,  \* FS.rootSnapshot: the key the root pointer named when
                 \* rootETag was last set, or NoKey
  \* Each mount's prune pass.
  pruning,       \* "Idle", "Listing" or "Deleting"; FS.pruning is the last two
  pruneCurrent,  \* pruneSnapshots' current: the key the pass's commit swapped
                 \* the root pointer to (ADR 0019 section 2), or NoKey
  pruneDoomed,   \* pruneSnapshots' doomed, still to delete
  \* Ghosts, which trace to no Go symbol, and the crash bound.
  acked,         \* commits whose commitLocked returned nil
  used,          \* snapshot keys a write that took effect has named
  overtaken,     \* another mount wrote the root since m last set rootETag[m]
  roWrites,      \* read-only mounts a write call of which reached the bucket
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

\* The keys of L that sort below k.
Below(L, k) == {j \in L : KeyLess(j, k)}

\* The keys a prune pass begun for k may delete, with PruneBelowCurrent (ADR
\* 0019 section 2): those of the listing L that sort below k, less the
\* Retention newest of them. Nothing at or above k is ever among them.
\* Go (trace.tsv): prunable.
Prunable(L, k) == Below(L, k) \ Kept(Below(L, k))

-----------------------------------------------------------------------------
\* Predicates and helpers.

\* mutable() returns nil (docs/vocabulary.md, V1).
Mutable(m) == mode[m] = "Writable"

\* New put m's store behind store.ReadOnly, which refuses every write.
Fenced(m) == m \in ReadOnlyMounts /\ "ReadOnlyFence" \in Fixes

\* The unconditional fallback follows a swap the store answered 501: in a
\* build without FallbackOptIn, or on a mount whose operator set
\* Config.AllowUnconditionalRoot, which behave alike (J17; ADR 0021 section
\* 4). Go (trace.tsv): FS.fallbackAllowed, which reads the opt-in.
FallbackAllowed == "FallbackOptIn" \notin Fixes

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
\* While the root pointer is in doubt the step due is the probe's read, which
\* commitLocked makes before it writes anything (ADR 0022 section 3), so no
\* snapshot Put and no swap is enabled then (J13, J20).
NextStep(m) ==
  IF pc[m] = "Ready"
    THEN IF nsDirty[m] /\ mode[m] # "Diverged"
           THEN IF rootKnowledge[m] = "InDoubt" THEN "Probe" ELSE FirstStep
           ELSE "None"
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
MountVars  == <<pc, mode, rootETag, epoch, nsDirty, attempt, rpSnapshot, base,
                rootKnowledge, doubtKey, rootSnapshot>>
PruneVars  == <<pruning, pruneCurrent, pruneDoomed>>
GhostVars  == <<acked, used, overtaken, roWrites>>
Vars       == <<BucketVars, MountVars, PruneVars, GhostVars, crashes>>

\* A Put, PutIfMatch or Delete call of m's reaches the bucket, whether or not
\* it changes anything (Assumption J16).
WriteCall(m) ==
  roWrites' = IF m \in ReadOnlyMounts THEN roWrites \cup {m} ELSE roWrites

\* m writes the root pointer, naming k.
WriteRoot(m, k) ==
  /\ bucketRoot' = [key |-> k, etag |-> ETagOf(k, m), writer |-> m]
  /\ used' = used \cup {k}

\* commitLocked returns an error. initEmpty's commit failing makes New fail.
CommitFails(m, k) ==
  /\ pc' = [pc EXCEPT ![m] = IF k.epoch = 1 THEN "Failed" ELSE "Ready"]
  /\ attempt' = [attempt EXCEPT ![m] = NoKey]

\* SwapUnknown: the event of docs/vocabulary.md, V2: commitLocked's Fire of
\* rootSwapUnknown on FS.rootKnowledge after a write of the root pointer
\* whose reply did not establish its ETag, with doubtKey recording the
\* attempt. The read that settles it is due next (ADR 0022 sections 2 to 4).
\* Go (trace.tsv): FS.commitLocked.
SwapUnknown(m, k) ==
  /\ rootKnowledge' = [rootKnowledge EXCEPT ![m] = "InDoubt"]
  /\ doubtKey' = [doubtKey EXCEPT ![m] = k]
  /\ pc' = [pc EXCEPT ![m] = "Probe"]
  /\ attempt' = [attempt EXCEPT ![m] = NoKey]

\* Unresolved: what a write of the root pointer whose reply did not establish
\* its ETag does to the mount: with RootProbe, SwapUnknown; without it,
\* commitLocked's old return, which keeps rootETag as it was (ADR 0022
\* section 4; before it, commitLocked's "case err != nil" at edcfc15).
Unresolved(m, k) ==
  IF "RootProbe" \in Fixes
    THEN SwapUnknown(m, k)
    ELSE /\ CommitFails(m, k)
         /\ UNCHANGED <<rootKnowledge, doubtKey>>

\* The chunk of m's commit at k's epoch is stored (Assumption J2).
FlushEffect(m, k) ==
  chunks' = IF k.epoch = 1 THEN chunks ELSE chunks \cup {CommitIdOf(m, k)}

\* m's snapshot Put of k takes effect: the snapshot holds m's namespace and
\* its commit at k's epoch, and with ChunksFirst the chunk uploads folded
\* before it took effect too (R6).
SnapshotEffect(m, k) ==
  /\ snapshots' = [x \in DOMAIN snapshots \cup {k} |->
                     IF x = k THEN base[m] \cup {CommitIdOf(m, k)}
                              ELSE snapshots[x]]
  /\ used' = used \cup {k}
  /\ IF "ChunksFirst" \in Fixes THEN FlushEffect(m, k) ELSE UNCHANGED chunks

\* Diverge: the event of docs/vocabulary.md, V1: the Fire of mountDiverge on
\* FS.mode (ADR 0014 section 7), made by resolveRoot on ProbeTheirs with
\* RootProbe, and by commitLocked's store.ErrPrecondition case, which
\* SwapConflict conjoins, without it (R11; ADR 0022 section 2). The table
\* mountModes has one edge, "Writable" to "Diverged", so a mount in any other
\* mode is left as it is.
\* Go (trace.tsv): FS.resolveRoot; FS.commitLocked at edcfc15.
Diverge(m) ==
  mode' = [mode EXCEPT ![m] = IF @ = "Writable" THEN "Diverged" ELSE @]

\* StartPrune: schedulePrune, in the hold of FS.mu of the success path that
\* calls it (R8): unless pruning is off or a pass is running, a pass starts
\* for k, the key the commit swapped the root pointer to (ADR 0019 section
\* 2; at 13aebc4 schedulePrune is given k's epoch, which is all the pass then
\* uses, so the variable holds the key either way).
\* Go (trace.tsv): FS.schedulePrune.
StartPrune(m, k) ==
  IF Retention > 0 /\ pruning[m] = "Idle"
    THEN /\ pruning' = [pruning EXCEPT ![m] = "Listing"]
         /\ pruneCurrent' = [pruneCurrent EXCEPT ![m] = k]
         /\ UNCHANGED pruneDoomed
    ELSE UNCHANGED PruneVars

\* commitLocked's success path, with the root pointer's new ETag et. Since
\* ADR 0022 it also records the key the root pointer now names, which the
\* probe's verdict compares against (J19).
Committed(m, k, et) ==
  /\ epoch' = [epoch EXCEPT ![m] = k.epoch]
  /\ rootETag' = [rootETag EXCEPT ![m] = et]
  /\ rootSnapshot' = [rootSnapshot EXCEPT ![m] = k]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = FALSE]
  /\ base' = [base EXCEPT ![m] = @ \cup {CommitIdOf(m, k)}]
  /\ acked' = acked \cup {CommitIdOf(m, k)}
  /\ StartPrune(m, k)
  /\ IF "ChunksFirst" \in Fixes
       THEN /\ pc' = [pc EXCEPT ![m] = "Ready"]
            /\ attempt' = [attempt EXCEPT ![m] = NoKey]
       ELSE /\ pc' = [pc EXCEPT ![m] = "PutSnapshot"]
            /\ attempt' = [attempt EXCEPT ![m] = k]

\* FallbackPut: commitLocked's unconditional Put of the root pointer after
\* store.ErrUnsupported, merged with SwapUnsupported (R7).
\* Go (trace.tsv): FS.commitLocked; ReadOnly.Put; S3.Put.
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
  /\ DOMAIN rootKnowledge = Mounts
  /\ DOMAIN doubtKey = Mounts
  /\ DOMAIN rootSnapshot = Mounts
  /\ DOMAIN pruning = Mounts
  /\ DOMAIN pruneCurrent = Mounts
  /\ DOMAIN pruneDoomed = Mounts
  /\ DOMAIN overtaken = Mounts
  /\ \A m \in Mounts :
       /\ pc[m] \in {"GetRoot", "HeadRoot", "GetSnapshot", "Ready",
                     "PutSnapshot", "Swap", "Probe", "FallbackHead", "Flush",
                     "Failed"}
       /\ mode[m] \in {"Writable", "ReadOnly", "Diverged"}
       /\ rootETag[m] \in ETags
       /\ epoch[m] \in 0..MaxEpoch
       /\ nsDirty[m] \in BOOLEAN
       /\ attempt[m] \in Keys \cup {NoKey}
       /\ rpSnapshot[m] \in Keys \cup {NoKey}
       /\ base[m] \subseteq CommitIds
       /\ rootKnowledge[m] \in {"Known", "InDoubt"}
       /\ doubtKey[m] \in Keys \cup {NoKey}
       /\ rootSnapshot[m] \in Keys \cup {NoKey}
       /\ pruning[m] \in {"Idle", "Listing", "Deleting"}
       /\ pruneCurrent[m] \in Keys \cup {NoKey}
       /\ pruneDoomed[m] \subseteq Keys
       /\ overtaken[m] \in BOOLEAN
  /\ acked \subseteq CommitIds
  /\ used \subseteq Keys
  /\ roWrites \subseteq ReadOnlyMounts
  /\ crashes \in 0..MaxCrashes

-----------------------------------------------------------------------------
\* The initial state.

\* Init: New, before its first store call. New makes FS.mode at
\* InitialMode(m), "ReadOnly" when Config.ReadOnly is set and "Writable"
\* otherwise (ADR 0014 section 7), makes FS.rootKnowledge at "Known" with no
\* attempt in doubt (ADR 0022 section 2), puts a read-only mount's store
\* behind store.ReadOnly, and sets FS.allowUnconditionalRoot from
\* Config.AllowUnconditionalRoot, which FallbackAllowed stands for (J17).
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
  /\ rootKnowledge = [m \in Mounts |-> "Known"]
  /\ doubtKey = [m \in Mounts |-> NoKey]
  /\ pruning = [m \in Mounts |-> "Idle"]
  /\ pruneCurrent = [m \in Mounts |-> NoKey]
  /\ pruneDoomed = [m \in Mounts |-> {}]
  /\ acked = {}
  /\ overtaken = [m \in Mounts |-> FALSE]
  /\ roWrites = {}
  /\ crashes = 0
  /\ IF Start = "Empty"
       THEN /\ bucketRoot = [key |-> NoKey, etag |-> NoETag, writer |-> ""]
            /\ snapshots = [k \in {} |-> {}]
            /\ used = {}
            /\ pc = [m \in Mounts |-> "GetRoot"]
            /\ rootETag = [m \in Mounts |-> NoETag]
            /\ rootSnapshot = [m \in Mounts |-> NoKey]
            /\ epoch = [m \in Mounts |-> 0]
            /\ base = [m \in Mounts |-> {}]
       ELSE /\ bucketRoot = [key |-> LoadedKey,
                             etag |-> ETagOf(LoadedKey, ""),
                             writer |-> ""]
            /\ snapshots = [k \in {LoadedKey} |-> {LoadedCommit}]
            /\ used = {LoadedKey}
            /\ pc = [m \in Mounts |-> "Ready"]
            /\ rootETag = [m \in Mounts |-> ETagOf(LoadedKey, "")]
            /\ rootSnapshot = [m \in Mounts |-> LoadedKey]
            /\ epoch = [m \in Mounts |-> 1]
            /\ base = [m \in Mounts |-> {LoadedCommit}]

-----------------------------------------------------------------------------
\* Loading.

\* GetRoot: loadOrInit's read of the root pointer, and its parse, when the
\* bucket holds one. It records the snapshot key the root pointer names, as
\* rpSnapshot for GetSnapshot and, since ADR 0022, as rootSnapshot for the
\* probe's verdict. With AtomicLoad the read is GetWithETag, whose ETag comes
\* in the same response as the body, so it becomes FS.rootETag here (R2) and
\* the next read is the snapshot's. Without it the read is Get, and the ETag
\* waits for HeadRoot: #89's window.
\* Go (trace.tsv): FS.loadOrInit; S3.GetWithETag; Local.GetWithETag.
GetRoot(m) ==
  /\ pc[m] = "GetRoot"
  /\ bucketRoot.key # NoKey
  /\ rpSnapshot' = [rpSnapshot EXCEPT ![m] = bucketRoot.key]
  /\ rootSnapshot' = [rootSnapshot EXCEPT ![m] = bucketRoot.key]
  /\ IF "AtomicLoad" \in Fixes
       THEN /\ rootETag' = [rootETag EXCEPT ![m] = bucketRoot.etag]
            /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
            /\ pc' = [pc EXCEPT ![m] = "GetSnapshot"]
       ELSE /\ pc' = [pc EXCEPT ![m] = "HeadRoot"]
            /\ UNCHANGED <<rootETag, overtaken>>
  /\ UNCHANGED <<BucketVars, mode, epoch, nsDirty, attempt, base,
                 rootKnowledge, doubtKey, PruneVars, acked, used, roWrites,
                 crashes>>

\* RefuseEmpty: loadOrInit's not-found branch on a read-only mount, which
\* makes New fail, having written nothing (ADR 0011 section 2).
\* Go (trace.tsv): FS.loadOrInit.
RefuseEmpty(m) ==
  /\ pc[m] = "GetRoot"
  /\ bucketRoot.key = NoKey
  /\ m \in ReadOnlyMounts
  /\ pc' = [pc EXCEPT ![m] = "Failed"]
  /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty, attempt,
                 rpSnapshot, base, rootKnowledge, doubtKey, rootSnapshot,
                 PruneVars, GhostVars, crashes>>

\* InitEmpty: loadOrInit's not-found branch on a writable mount, calling
\* initEmpty, which sets epoch 0, an empty rootETag and no root snapshot, and
\* then commits.
\* Go (trace.tsv): FS.loadOrInit; FS.initEmpty.
InitEmpty(m) ==
  /\ pc[m] = "GetRoot"
  /\ bucketRoot.key = NoKey
  /\ m \notin ReadOnlyMounts
  /\ pc' = [pc EXCEPT ![m] = FirstStep]
  /\ epoch' = [epoch EXCEPT ![m] = 0]
  /\ rootETag' = [rootETag EXCEPT ![m] = NoETag]
  /\ rootSnapshot' = [rootSnapshot EXCEPT ![m] = NoKey]
  /\ base' = [base EXCEPT ![m] = {}]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = FALSE]
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ UNCHANGED <<BucketVars, mode, attempt, rpSnapshot, rootKnowledge,
                 doubtKey, PruneVars, acked, used, roWrites, crashes>>

\* HeadRoot: without AtomicLoad, loadOrInit's Head of the root pointer, whose
\* ETag becomes FS.rootETag (R2). Another writer's swap between GetRoot and
\* HeadRoot is #89's window. With AtomicLoad, GetRoot never leaves pc at
\* "HeadRoot", so this action is never enabled, and since ADR 0020 no Go
\* symbol performs it: loadOrInit makes no Head of the root pointer.
\* Go: none since ADR 0020 (FS.loadOrInit at 55fc666 and before); trace.tsv
\* has no row for it.
HeadRoot(m) ==
  /\ pc[m] = "HeadRoot"
  /\ rootETag' = [rootETag EXCEPT ![m] = bucketRoot.etag]
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ pc' = [pc EXCEPT ![m] = "GetSnapshot"]
  /\ UNCHANGED <<BucketVars, mode, epoch, nsDirty, attempt, rpSnapshot, base,
                 rootKnowledge, doubtKey, rootSnapshot, PruneVars, acked,
                 used, roWrites, crashes>>

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
  /\ UNCHANGED <<BucketVars, mode, rootETag, nsDirty, attempt, rootKnowledge,
                 doubtKey, rootSnapshot, PruneVars, GhostVars, crashes>>

-----------------------------------------------------------------------------
\* Namespace changes.

\* Mutate: mutable(), folded with the hold of FS.mu in which a change sets
\* FS.dirty (R3, R4). mutable() reads the mode alone: a mount whose root
\* pointer is in doubt still accepts changes (ADR 0022 section 6).
\* Go (trace.tsv): FS.mutable; FS.SetAttr; FS.createEntry; FS.unlink;
\* FS.Rename; FS.bufferWrite; FS.truncate.
Mutate(m) ==
  /\ pc[m] = "Ready"
  /\ Mutable(m)
  /\ ~nsDirty[m]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = TRUE]
  /\ UNCHANGED <<BucketVars, pc, mode, rootETag, epoch, attempt, rpSnapshot,
                 base, rootKnowledge, doubtKey, rootSnapshot, PruneVars,
                 GhostVars, crashes>>

\* StrayDirty: a path that dirties a read-only mount (Assumption J6).
\* Go: none; trace.tsv has no row for it.
StrayDirty(m) ==
  /\ pc[m] = "Ready"
  /\ m \in ReadOnlyMounts
  /\ ~nsDirty[m]
  /\ nsDirty' = [nsDirty EXCEPT ![m] = TRUE]
  /\ UNCHANGED <<BucketVars, pc, mode, rootETag, epoch, attempt, rpSnapshot,
                 base, rootKnowledge, doubtKey, rootSnapshot, PruneVars,
                 GhostVars, crashes>>

-----------------------------------------------------------------------------
\* Commits.

\* PutSnapshot: commitLocked's refusal once diverged, its encode and nonce,
\* and its Put of the snapshot. With ChunksFirst it includes Flush (R6).
\* store.ReadOnly refuses the Put when the mount is fenced, and the call
\* reaches nothing. Otherwise it reaches the bucket, and is not applied
\* (PutFailed), is applied, or is applied and answers with an error
\* (SnapshotReplyLost); each failure is commitLocked returning an error.
\* commitLocked's refusal while a file is unsettled (ADR 0012 section 4) is
\* FileData's. It is never due while the root pointer is in doubt (NextStep).
\* Go (trace.tsv): FS.commitLocked; ReadOnly.Put; S3.Put.
PutSnapshot(m) ==
  /\ NextStep(m) = "PutSnapshot"
  /\ \E k \in KeyFor(m) :
       \/ /\ Fenced(m)
          /\ CommitFails(m, k)
          /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty,
                         rpSnapshot, base, rootKnowledge, doubtKey,
                         rootSnapshot, PruneVars, GhostVars, crashes>>
       \/ /\ ~Fenced(m)
          /\ "PutFailed" \in Faults
          /\ WriteCall(m)
          /\ CommitFails(m, k)
          /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty,
                         rpSnapshot, base, rootKnowledge, doubtKey,
                         rootSnapshot, PruneVars, acked, used, overtaken,
                         crashes>>
       \/ /\ ~Fenced(m)
          /\ WriteCall(m)
          /\ SnapshotEffect(m, k)
          /\ IF "ChunksFirst" \in Fixes
               THEN /\ pc' = [pc EXCEPT ![m] = "Swap"]
                    /\ attempt' = [attempt EXCEPT ![m] = k]
               ELSE /\ pc' = [pc EXCEPT ![m] =
                                IF k.epoch = 1 THEN "Ready" ELSE "Flush"]
                    /\ attempt' = [attempt EXCEPT ![m] =
                                     IF k.epoch = 1 THEN NoKey ELSE k]
          /\ UNCHANGED <<bucketRoot, mode, rootETag, epoch, nsDirty,
                         rpSnapshot, base, rootKnowledge, doubtKey,
                         rootSnapshot, PruneVars, acked, overtaken, crashes>>
       \/ /\ ~Fenced(m)
          /\ "SnapshotReplyLost" \in Faults
          /\ WriteCall(m)
          /\ SnapshotEffect(m, k)
          /\ CommitFails(m, k)
          /\ UNCHANGED <<bucketRoot, mode, rootETag, epoch, nsDirty,
                         rpSnapshot, base, rootKnowledge, doubtKey,
                         rootSnapshot, PruneVars, acked, overtaken, crashes>>

\* Flush: Sync's first pass and settle pass, flushOpen, and putChunk, as a
\* step of its own only without ChunksFirst (Assumption J7).
\* Go (trace.tsv): FS.Sync; FS.flushAll; FS.settleAll; FS.flushOpen;
\* FS.putChunk.
Flush(m) ==
  /\ pc[m] = "Flush"
  /\ WriteCall(m)
  /\ FlushEffect(m, attempt[m])
  /\ pc' = [pc EXCEPT ![m] = "Ready"]
  /\ attempt' = [attempt EXCEPT ![m] = NoKey]
  /\ UNCHANGED <<bucketRoot, snapshots, mode, rootETag, epoch, nsDirty,
                 rpSnapshot, base, rootKnowledge, doubtKey, rootSnapshot,
                 PruneVars, acked, used, overtaken, crashes>>

\* SwapOK: commitLocked's PutIfMatch succeeds with an ETag, then its success
\* path and schedulePrune (R8). It is also V2's SwapOK event, "Known" to
\* "Known", which changes no variable of the machine (ADR 0022 section 4).
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
  /\ WriteCall(m)
  /\ UNCHANGED <<snapshots, chunks, mode, rpSnapshot, rootKnowledge, doubtKey,
                 crashes>>

\* SwapReplyLost: the swap is applied, but an error is returned. With
\* RootProbe the root is in doubt and the probe's read is due (Unresolved);
\* without it commitLocked takes its "case err != nil" branch and keeps its
\* rootETag.
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
       /\ Unresolved(m, k)
  /\ WriteCall(m)
  /\ UNCHANGED <<snapshots, chunks, mode, rootETag, epoch, nsDirty,
                 rpSnapshot, rootSnapshot, base, PruneVars, acked, crashes>>

\* SwapFailed: the swap is not applied, and an error that is not typed is
\* returned. With RootProbe the root is in doubt all the same, since the
\* mount cannot tell this reply from a lost one (Unresolved).
\* Go (trace.tsv): FS.commitLocked; S3.PutIfMatch; errorFor;
\* Local.PutIfMatch.
SwapFailed(m) ==
  /\ NextStep(m) = "Swap"
  /\ "SwapFailed" \in Faults
  /\ ~Fenced(m)
  /\ \E k \in KeyFor(m) : Unresolved(m, k)
  /\ WriteCall(m)
  /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty, rpSnapshot,
                 rootSnapshot, base, PruneVars, acked, used, overtaken,
                 crashes>>

\* SwapNoETag: the swap is applied and answers 200 with no ETag header, so
\* S3.PutIfMatch returns "" (#103). With RootProbe the reply did not
\* establish the root pointer's ETag, so the root is in doubt and the probe's
\* read is due (SwapUnknown; ADR 0022 section 4). Without it commitLocked
\* stores the empty ETag on its success path, as at edcfc15, so its next
\* swap is If-None-Match against a root pointer that exists and conflicts.
\* Go (trace.tsv): FS.commitLocked; S3.PutIfMatch.
SwapNoETag(m) ==
  /\ NextStep(m) = "Swap"
  /\ "SwapNoETag" \in Faults
  /\ Capability \in {"Conditional", "Flaky"}
  /\ ~Fenced(m)
  /\ \E k \in KeyFor(m) :
       /\ SwapCondition(m, k)
       /\ WriteRoot(m, k)
       /\ IF "RootProbe" \in Fixes
            THEN /\ overtaken' = [x \in Mounts |->
                                    IF x = m THEN overtaken[m] ELSE TRUE]
                 /\ SwapUnknown(m, k)
                 /\ UNCHANGED <<rootETag, epoch, nsDirty, rootSnapshot, base,
                                acked, PruneVars>>
            ELSE /\ overtaken' = [x \in Mounts |-> x # m]
                 /\ Committed(m, k, NoETag)
                 /\ UNCHANGED <<rootKnowledge, doubtKey>>
  /\ WriteCall(m)
  /\ UNCHANGED <<snapshots, chunks, mode, rpSnapshot, crashes>>

\* SwapConflict: store.ErrPrecondition, from errorFor's 412 or 409 or from
\* Local's ETag check. With SwapConflict409 it may also come while the
\* condition holds. With RootProbe no swap's error alone diverges a mount:
\* the root is in doubt and the probe's read decides (SwapUnknown; ADR 0022
\* section 1). Without it commitLocked fires mountDiverge here, which is
\* Diverge, and a read-only mount's mode is left as it is (R11).
\* Go (trace.tsv): FS.commitLocked; errorFor; Local.PutIfMatch.
SwapConflict(m) ==
  /\ NextStep(m) = "Swap"
  /\ Capability \in {"Conditional", "Flaky"}
  /\ ~Fenced(m)
  /\ \E k \in KeyFor(m) :
       /\ (~SwapCondition(m, k) \/ "SwapConflict409" \in Faults)
       /\ IF "RootProbe" \in Fixes
            THEN /\ SwapUnknown(m, k)
                 /\ UNCHANGED mode
            ELSE /\ Diverge(m)
                 /\ CommitFails(m, k)
                 /\ UNCHANGED <<rootKnowledge, doubtKey>>
  /\ WriteCall(m)
  /\ UNCHANGED <<BucketVars, rootETag, epoch, nsDirty, rpSnapshot,
                 rootSnapshot, base, PruneVars, acked, used, overtaken,
                 crashes>>

\* SwapUnsupported: store.ErrUnsupported, from errorFor's 501 or from
\* ReadOnly.PutIfMatch. store.ReadOnly refuses the swap when the mount is
\* fenced, and the fallback's Put with it, so nothing reaches the bucket
\* either way. Otherwise the call that answered 501 reached the bucket and
\* changed nothing, and then, unless FallbackAllowed, commitLocked reclaims
\* the attempt's snapshot, which no root pointer names, and returns an error
\* (ADR 0021 section 2; R12); or, where FallbackAllowed, the fallback's
\* unconditional Put of the root pointer
\* follows (FallbackPut, R7), which reaches the bucket and is not applied
\* (PutFailed), is applied, or is applied and answers with an error
\* (FallbackReplyLost). With RootProbe either failure of that Put leaves the
\* root in doubt, since S3.do retries it and it may have taken effect
\* (Unresolved; ADR 0022 section 4); without it rootETag is left as it was.
\* Go (trace.tsv): FS.commitLocked; FS.fallbackAllowed; errorFor;
\* ReadOnly.PutIfMatch.
SwapUnsupported(m) ==
  /\ NextStep(m) = "Swap"
  /\ (Capability \in {"Unconditional", "Flaky"} \/ Fenced(m))
  /\ \E k \in KeyFor(m) :
       \/ /\ Fenced(m)
          /\ CommitFails(m, k)
          /\ UNCHANGED <<BucketVars, GhostVars, rootKnowledge, doubtKey>>
       \/ /\ ~Fenced(m)
          /\ ~FallbackAllowed
          /\ WriteCall(m)
          /\ snapshots' = [x \in DOMAIN snapshots \ {k} |-> snapshots[x]]
          /\ CommitFails(m, k)
          /\ UNCHANGED <<bucketRoot, chunks, acked, used, overtaken,
                         rootKnowledge, doubtKey>>
       \/ /\ ~Fenced(m)
          /\ FallbackAllowed
          /\ "PutFailed" \in Faults
          /\ WriteCall(m)
          /\ Unresolved(m, k)
          /\ UNCHANGED <<BucketVars, acked, used, overtaken>>
       \/ /\ ~Fenced(m)
          /\ FallbackAllowed
          /\ WriteCall(m)
          /\ FallbackPut(m, k)
          /\ pc' = [pc EXCEPT ![m] = "FallbackHead"]
          /\ attempt' = [attempt EXCEPT ![m] = k]
          /\ UNCHANGED <<snapshots, chunks, acked, rootKnowledge, doubtKey>>
       \/ /\ ~Fenced(m)
          /\ FallbackAllowed
          /\ "FallbackReplyLost" \in Faults
          /\ WriteCall(m)
          /\ FallbackPut(m, k)
          /\ Unresolved(m, k)
          /\ UNCHANGED <<snapshots, chunks, acked>>
  /\ UNCHANGED <<mode, rootETag, epoch, nsDirty, rpSnapshot, rootSnapshot,
                 base, PruneVars, crashes>>

\* FallbackHead: the Head of the root pointer after the fallback's Put, then
\* the success path. The Head may return another writer's ETag. It is also
\* V2's SwapOK event, which changes no variable of the machine; a Head that
\* fails is left out (Leaves out).
\* Go (trace.tsv): FS.commitLocked.
FallbackHead(m) ==
  /\ pc[m] = "FallbackHead"
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ Committed(m, attempt[m], bucketRoot.etag)
  /\ UNCHANGED <<BucketVars, mode, rpSnapshot, rootKnowledge, doubtKey, used,
                 roWrites, crashes>>

-----------------------------------------------------------------------------
\* The probe (ADR 0022 section 3). Each action is one read of the root
\* pointer, GetWithETag of rootKey, made by resolveRoot while the root is in
\* doubt: at pc "Probe", straight after the write that raised the doubt, or
\* at "Ready" at the top of a later commit, before it writes anything
\* (NextStep; R13, R14). The verdict is by the key the root pointer names,
\* and the ETag the read returns is what the mount takes (J19).

\* ProbeOurs: the root pointer names the attempt in doubt, so the write took
\* effect, and the mount adopts it. At the failure site that is the success
\* path whole, Committed, with StartPrune (R8); at the top of a later commit
\* it adopts only, and the commit that follows writes the namespace as it
\* stands (J20).
\* Go (trace.tsv): FS.resolveRoot; classifyRoot; FS.commitLocked;
\* S3.GetWithETag; Local.GetWithETag.
ProbeOurs(m) ==
  /\ NextStep(m) = "Probe"
  /\ bucketRoot.key = doubtKey[m]
  /\ rootKnowledge' = [rootKnowledge EXCEPT ![m] = "Known"]
  /\ doubtKey' = [doubtKey EXCEPT ![m] = NoKey]
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ IF pc[m] = "Probe"
       THEN Committed(m, doubtKey[m], bucketRoot.etag)
       ELSE /\ epoch' = [epoch EXCEPT ![m] = doubtKey[m].epoch]
            /\ rootETag' = [rootETag EXCEPT ![m] = bucketRoot.etag]
            /\ rootSnapshot' = [rootSnapshot EXCEPT ![m] = doubtKey[m]]
            /\ base' = [base EXCEPT ![m] = @ \cup {CommitIdOf(m, doubtKey[m])}]
            /\ UNCHANGED <<pc, attempt, nsDirty, acked, PruneVars>>
  /\ UNCHANGED <<BucketVars, mode, rpSnapshot, used, roWrites, crashes>>

\* ProbeUnchanged: the root pointer names the key the mount's namespace
\* descends from, so the write did not take effect; the mount takes the root
\* pointer's ETag, which is the one it should have swapped against, and its
\* next swap proceeds. At the failure site the commit fails, with no retry in
\* line (ADR 0022 Assumption 7); at the top of a later commit the commit
\* continues.
\* Go (trace.tsv): FS.resolveRoot; classifyRoot; FS.commitLocked;
\* S3.GetWithETag; Local.GetWithETag.
ProbeUnchanged(m) ==
  /\ NextStep(m) = "Probe"
  /\ bucketRoot.key = rootSnapshot[m]
  /\ bucketRoot.key # doubtKey[m]
  /\ rootKnowledge' = [rootKnowledge EXCEPT ![m] = "Known"]
  /\ doubtKey' = [doubtKey EXCEPT ![m] = NoKey]
  /\ rootETag' = [rootETag EXCEPT ![m] = bucketRoot.etag]
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ IF pc[m] = "Probe"
       THEN CommitFails(m, doubtKey[m])
       ELSE UNCHANGED <<pc, attempt>>
  /\ UNCHANGED <<BucketVars, mode, epoch, nsDirty, rpSnapshot, rootSnapshot,
                 base, PruneVars, acked, used, roWrites, crashes>>

\* ProbeTheirs: the root pointer names neither, or is gone: another writer
\* owns the bucket. The mount diverges (Diverge, R11), the machine stays
\* "InDoubt", and the commit fails. The record that names the writer is
\* Go's (ADR 0022 section 3).
\* Go (trace.tsv): FS.resolveRoot; classifyRoot; FS.commitLocked;
\* S3.GetWithETag; Local.GetWithETag.
ProbeTheirs(m) ==
  /\ NextStep(m) = "Probe"
  /\ bucketRoot.key # doubtKey[m]
  /\ bucketRoot.key # rootSnapshot[m]
  /\ Diverge(m)
  /\ IF pc[m] = "Probe"
       THEN CommitFails(m, doubtKey[m])
       ELSE UNCHANGED <<pc, attempt>>
  /\ UNCHANGED <<BucketVars, rootKnowledge, doubtKey, rootSnapshot, rootETag,
                 epoch, nsDirty, rpSnapshot, base, PruneVars, GhostVars,
                 crashes>>

\* ProbeFailed: the read fails, or returns a body the mount does not
\* understand (J4), so nothing is decided: the root stays in doubt, the
\* commit fails, and the next commit reads again before it writes anything.
\* At "Ready" nothing moves (R14).
\* Go (trace.tsv): FS.resolveRoot; classifyRoot; FS.commitLocked;
\* S3.GetWithETag; Local.GetWithETag; errorFor.
ProbeFailed(m) ==
  /\ NextStep(m) = "Probe"
  /\ "ProbeFailed" \in Faults
  /\ IF pc[m] = "Probe"
       THEN CommitFails(m, doubtKey[m])
       ELSE UNCHANGED <<pc, attempt>>
  /\ UNCHANGED <<BucketVars, mode, rootETag, epoch, nsDirty, rpSnapshot, base,
                 rootKnowledge, doubtKey, rootSnapshot, PruneVars, GhostVars,
                 crashes>>

-----------------------------------------------------------------------------
\* Prune passes.

\* PruneList: pruneSnapshots' List loop (R9), then what the pass will delete.
\* With PruneBelowCurrent that is Prunable, which prunable computes (ADR 0019
\* section 2). Without it, it is every listed key but the Retention newest,
\* less the keys of the epoch the pass was begun for, which Go at 13aebc4
\* skips in its Delete loop after an early return when the listing holds no
\* more than Retention keys.
\* Go (trace.tsv): FS.pruneSnapshots; prunable.
PruneList(m) ==
  /\ pruning[m] = "Listing"
  /\ pruning' = [pruning EXCEPT ![m] = "Deleting"]
  /\ LET L == DOMAIN snapshots
     IN  pruneDoomed' = [pruneDoomed EXCEPT ![m] =
                           IF "PruneBelowCurrent" \in Fixes
                             THEN Prunable(L, pruneCurrent[m])
                             ELSE IF Cardinality(L) <= Retention
                                    THEN {}
                                    ELSE {k \in L \ Kept(L) :
                                            k.epoch # pruneCurrent[m].epoch}]
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
  /\ WriteCall(m)
  /\ UNCHANGED <<bucketRoot, chunks, MountVars, pruning, pruneCurrent,
                 acked, used, overtaken, crashes>>

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
\* loadOrInit's first read (Assumption J9). A root in doubt is forgotten
\* with the rest: the restart loads whatever the bucket holds.
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
  /\ rootKnowledge' = [rootKnowledge EXCEPT ![m] = "Known"]
  /\ doubtKey' = [doubtKey EXCEPT ![m] = NoKey]
  /\ rootSnapshot' = [rootSnapshot EXCEPT ![m] = NoKey]
  /\ pruning' = [pruning EXCEPT ![m] = "Idle"]
  /\ pruneCurrent' = [pruneCurrent EXCEPT ![m] = NoKey]
  /\ pruneDoomed' = [pruneDoomed EXCEPT ![m] = {}]
  /\ overtaken' = [overtaken EXCEPT ![m] = FALSE]
  /\ UNCHANGED <<BucketVars, acked, used, roWrites>>

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
    \/ SwapNoETag(m)
    \/ SwapConflict(m)
    \/ SwapUnsupported(m)
    \/ FallbackHead(m)
    \/ ProbeOurs(m)
    \/ ProbeUnchanged(m)
    \/ ProbeTheirs(m)
    \/ ProbeFailed(m)
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

\* No Put, PutIfMatch or Delete call of a read-only mount reaches the bucket,
\* whether or not it would change anything (ADR 0011 sections 3 and 4;
\* Assumption J16).
ReadOnlyNeverWrites == roWrites = {}

\* A mount diverges only after another mount has written the root pointer
\* since it last set rootETag.
NoSelfDivergence ==
  \A m \in Mounts : mode[m] = "Diverged" => overtaken[m]

-----------------------------------------------------------------------------
\* State constraints.

\* PassApartFromOwnCommits: while a mount's prune pass runs, the mount
\* completes no commit, and the pass has not deleted the snapshot of an
\* attempt the mount has still to swap. So only other mounts' commits
\* overlap a pass. Only CommitProtocol_PruneTwoWriters sets it, and
\* CommitProtocol_PruneOverlapTwoWriters is its twin without it (Assumption
\* J15). TLC checks the invariants of a state that breaks a constraint, and
\* takes no step from it, so the second conjunct stops the mount's swap to a
\* snapshot its own pass deleted before TLC can take that swap. The epoch is
\* read through the key the pass was begun for (ADR 0019).
PassApartFromOwnCommits ==
  \A m \in Mounts :
    pruning[m] # "Idle" =>
      /\ epoch[m] = pruneCurrent[m].epoch
      /\ ((pc[m] = "Swap" /\ attempt[m] # NoKey) =>
            attempt[m] \in DOMAIN snapshots)

=============================================================================
