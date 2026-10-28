# 0022. A failed root-pointer swap is settled by one read of the root pointer, never taken for a second writer

**Status:** Accepted
**Date:** 2026-10-27
**Revised:** 2026-10-28 — after coder's commit `71fee34` and the tests' two
rounds, against the code at that commit, read and not run. §4's
top-of-commit `probeFailed` error no longer "wraps the read's error", which
§3's pinned signature cannot carry and which a read that decided nothing
need not have (Assumption 25); §4's `TLA+:` line for `commitLocked` loses
`Diverge`, which it no longer performs (Assumption 26); §2's pinned doc
comment for `rootKnowledge` no longer names `loadOrInit`, which neither
reads nor fires it, and §2 gives `mode.go` the corrected comment for
`mountDiverge` (Assumption 27); §3 pins the record attributes, the second
conflict message and the `attempted` value coder chose (Assumption 28); §8
corrects K5's, K11's and K12's text, adds the harness's after-swap arm that
K13 needs, gives K4 the assertion that a commit begun `Known` reads nothing
before its swap, and says why K7, K8 and K10 keep that arm (Assumption 29);
§7 and Assumption 20 record the module's `Checked against` bump to
`71fee34`. The rule, the machine, the verdict, the model and the
configurations are unchanged, and no code changes but the two comments.
**Revised:** 2026-10-28 — on review of the whole branch, before merge, the
five of its low findings that are this ADR's. §4's "Doubt" said a failed
`Fire(rootSwapUnknown)` leaves the mount "as the old code left it", which
for `store.ErrPrecondition` would have moved the mode; it now says the
path moves no mode, since only `resolveRoot` does (§2; Assumption 32).
`errorFor` gains the trace rows `ProbeUnchanged` and `ProbeTheirs`, which
its 404 mapping decides as its 412 mapping decides `SwapConflict` (§4;
Assumption 33; its `TLA+:` line follows by a comment-only coder change).
`CommitProtocol_SwapNoETag` gains the three members it lacked against §7's
rule, its MustFail twin gains them with it, and the twin's comment and §7
say why the twin keeps two invariants; both rows return to uncalibrated
(Assumption 31). A residual the review named, a conditional `PUT` that
applies after the probe's read was served, is recorded in §3, Assumption
30 and *What this does not decide*, with the issue it gets. §8 gains the
harness's two `Head` arms and K14 to K19, for the paths no Go test
reached, and its entry for M3 names the failure message that still says
`commitLocked` fires the mode (Assumption 34). §9's proposed `CHANGES`
text gains two lines for the behaviours the review found it omits
(Assumption 19). `docs/vocabulary.md`'s `Faults` row names the two values
that are this ADR's. The rule, the machine, the verdict, the model and the
code are unchanged.
**Issue:** #5

## Context

**The owner's decisions.** Two, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0021 were, and its design is the architect's (Assumption 1).
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." This change is not breaking (Assumption 19),
   so nothing here relies on it.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `edcfc15`, the commit this ADR's branch was cut from, which
is `origin/main` with ADR 0014 §7's conversion of the mount mode in it
(Assumption 20). Code is cited by symbol.

- `commitLocked`, in `internal/blobfs/commit.go`, refuses at its top when
  `f.mode.State()` is `mountDiverged`; writes the snapshot under a fresh key,
  `snapshotPrefix`, the epoch as twenty digits, `-`, six random bytes as
  twelve hex digits and `.json.gz`; builds a `rootPointer` naming it, with
  `Version` `snapshotVersion`, `Epoch`, `Snapshot`, `FSID`, `WrittenNS` and
  `Writer` `f.writerID`, which `New` sets to the host name, `/` and the
  process id; and swaps the root pointer with
  `PutIfMatch(ctx, rootKey, rpBody, f.rootETag)`. It switches on the error:
  - `store.ErrPrecondition` fires `mountDiverge` on `f.mode`, logs
    `"commit conflict: another writer owns this bucket"` at `Error` with
    `our_epoch` and `attempted`, and returns an error. Nothing names the
    writer that took the bucket.
  - `store.ErrUnsupported` without the opt-in refuses the commit and reclaims
    its snapshot after one `Get` of `rootKey`, as ADR 0021 §2 decides. Its
    step 3, a read that shows the root pointer naming the attempt's key, a
    501 that took effect, logs an `Error` whose message contains `took effect`
    and leaves the mount as it is, so that "its next swap, made with the old
    ETag, fails its condition and diverges the mount with no other writer,
    which is the route #5 owns".
  - `store.ErrUnsupported` with the opt-in writes the root pointer with `Put`,
    returning `write root pointer: %w` on failure, then reads its ETag with
    `Head`, keeping `info.ETag` only when the `Head` succeeds; otherwise
    `newETag` stays `""`.
  - any other error is returned as `swap root pointer: %w`, with `FS.rootETag`
    kept as it was.
  - On success it stores the reply's ETag, empty or not, as `FS.rootETag`,
    advances `FS.epoch`, clears `FS.dirty`, and calls `schedulePrune(key)`.
- `S3.PutIfMatch`, in `internal/store/s3.go`, makes one attempt: `S3.do`
  sends every other call again after a transport error, a 5xx or a 429, up
  to `maxRetries` more times, but never a conditional write. It returns
  `cleanETag(resp.Header.Get("ETag"))`, which is `""` when the reply carries
  none (#103). `errorFor` maps 404 and the codes `NoSuchKey` and
  `NoSuchBucket` to `store.ErrNotFound`; 412, 409 and the codes
  `PreconditionFailed` and `ConditionalRequestConflict` to
  `store.ErrPrecondition`; 501 to `store.ErrUnsupported`; and anything else
  to an untyped error carrying the status and the code. `S3.Put` is retried,
  so one attempt can take effect while `Put` returns an error.
- `S3.GetWithETag` and `Local.GetWithETag` return a body and the ETag of that
  body from one response (ADR 0020 §2). `Local.PutIfMatch` compares the
  current body's MD5 with the ETag given and answers `store.ErrPrecondition`
  on a mismatch; `Local` never answers 409 or 501, and `Local.Put` cannot take
  effect and fail, since its last step is the rename that applies it.
- `loadOrInit` sets `FS.rootETag` from the one read; `initEmpty` sets it to
  `""`, which `S3.PutIfMatch` sends as `If-None-Match: *`. Nothing records
  the snapshot key the root pointer named: `loadOrInit`'s `rp.Snapshot` and
  `commitLocked`'s `key` are locals.
- The mount mode is `FS.mode`, an `fsm.Atomic` over `mountModes`, whose one
  edge is `Writable` to `Diverged` on `Diverge` (ADR 0014 §7). `mutable()`
  reads it alone, and answers `vfs.ErrStale` once it is `Diverged`.
  `docs/vocabulary.md` V2 reserves the root-pointer knowledge machine, with
  `Known`, `InDoubt`, `SwapOK`, `SwapUnknown`, `ProbeOurs`, `ProbeUnchanged`
  and the action `ProbeTheirs`, for this ADR; `docs/tla/CommitProtocol.tla`
  leaves `rootKnowledge` out (its J13).

**The defect.** #5, "A transient commit failure is treated as a second
writer", as the session gave its body and comments (Assumption 20): a lost
root compare-and-swap marks the mount diverged until a restart, whether
another mount took the bucket or the failure was transient, and the two can be
told apart by re-reading the root pointer. The routes to a false second
writer, each shown by a configuration of `docs/tla/CommitProtocol.tla`
(`manifest.tsv`, MustFail on `NoSelfDivergence`) or found by reading:

1. **A lost reply** (`CommitProtocol_SelfDiverge`). The swap took effect and
   its reply was lost, so `commitLocked` keeps its old `rootETag`; the next
   swap's `If-Match` is refused with 412, and the mount diverges. RFC 9110
   §9.2.2 says exactly this of a `PUT`: a client that does not receive the
   response "can establish a new connection and retry the idempotent
   request", knowing that "repeating the request will have the same intended
   effect, even if the original request succeeded" (*Sources*). A
   conditional `PUT` is the one write strata may not repeat blind, since the
   condition that held for the lost attempt may not hold for the retry.
2. **A 409** (`CommitProtocol_SelfDiverge409`). AWS documents, for an
   `If-Match` write, "You can also receive a `409 Conflict` response in the
   case of concurrent requests", and for `PutObject`, "On a 409 failure you
   should fetch the object's ETag and retry the upload" (*Sources*).
   `errorFor` maps it to `store.ErrPrecondition`, so the mount diverges on a
   reply whose own documentation says to read and retry.
3. **An opted-in fallback `Put` that took effect and failed**
   (`CommitProtocol_SelfDivergeFallback`). `S3.do` retries the unconditional
   `Put`, so it can take effect and still return an error; `commitLocked`
   keeps its old `rootETag`, and the next swap diverges with no other writer
   (the issue's comment of 2026-10-24).
4. **A 501 that took effect** (ADR 0021 §2, step 3; the issue's comment of
   2026-10-27). The reclaim's read shows the root pointer naming the refused
   commit's snapshot; the snapshot is kept, an `Error` is logged, and the
   mount self-diverges at its next swap.
5. **A swap reply with no ETag** (#103; ADR 0020 Assumption 3). The commit is
   durable, `FS.rootETag` becomes `""`, the next swap is `If-None-Match: *`
   against a root pointer that exists, and the mount diverges. The same
   happens when the opted-in fallback's `Head` fails after its `Put`, and
   when a proxy hands the mount an ETag the store does not recognise
   (ADR 0020, *What this does not decide*): the mount diverges "with the
   wrong cause named".

Every route has one shape: a write of the root pointer whose reply did not
tell the mount the root pointer's current ETag, followed by a swap against
the ETag it still held, whose refusal the code reads as another writer. The
issue's done-when, as filed: a transient failure with an unchanged root
recovers without diverging; a real second writer still diverges, and the log
names it; a test simulates both with a store that can fail a conditional
write on demand. Its comment of 2026-10-10 adds: an unknown outcome leaves the
mount in doubt; before its next swap the mount makes one read returning the
root's body and ETag; if the root names its attempt it adopts it, if the root
is unchanged it proceeds, otherwise it diverges; and the fix is specified in
TLA+ first. The issue's third ask, a recovery path for the diverged case that
needs no restart, is split out (§9).

**What the ADRs and documents say.** ADR 0013 Assumption 1 names this defect
as one of the two the vocabulary reserved fixes for, and its §7 and §8 say how
a fix lands: spec first; the defect's configurations flip to MustHold with the
fix added to `Fixes`; a MustFail configuration removes the fix; and the
counterexample becomes a Go test that test-author writes from the trace and
the spec. ADR 0014 §8 gives root-pointer knowledge an `fsm.Machine` under
`FS.mu`, the names V2 reserves, `ProbeTheirs` firing `mountDiverge`, and
leaves its type, its guards and the probe to this ADR; its §7 pins that
`commitLocked`'s `store.ErrPrecondition` case is the mode's one writer, which
this ADR moves (§2, §4). ADR 0018 §5 says root-pointer knowledge "is
security-relevant if the ADR for #5 has `mutable()` read it", and that
`ProbeOurs` and `ProbeUnchanged` would then widen with no caller causing them.
ADR 0019 §1 rests on the key the root pointer names never falling below a
pass's, and its R7 and R8 observe a lost reply's `Sync` returning an error.
ADR 0020 Assumption 3 keeps a swap reply's empty ETag "as it is", naming "#5's
in-doubt knowledge the likely tool" for #103, and calls the asymmetry
deliberate. ADR 0021 §2 leaves its took-effect case to #5, and its Assumption
22 names the residual its reclaim's guard read leaves: a 501 that took effect
whose guard read failed or could not be understood. `docs/DESIGN.md` §5 says
"A failed CAS means another mount advanced the filesystem", which this ADR
refines: a failed CAS whose reply does not show another mount's root pointer
leaves the outcome in doubt, and one read settles it (Assumption 24).

**What this constrains.** `FS`, `New`, `loadOrInit`, `initEmpty` and
`commitLocked` in `internal/blobfs`, and the file, the functions and the
fields this ADR adds; `docs/tla/CommitProtocol.tla`, its configurations and
the tables beside it; `docs/vocabulary.md`; `docs/DESIGN.md` §5 and §16; and,
by cross-reference only, ADRs 0013, 0014, 0018, 0019, 0020 and 0021. Nothing
on the wire, no flag, no object format and no `store.Store` method changes;
`errorFor` is unchanged (Assumption 13).

## Decision

### 1. The rule

A mount decides that another writer owns the bucket only on a positive
showing from a read of the root pointer, never from a swap's error alone.

After any write of the root pointer whose reply did not give the mount the
root pointer's current ETag, the mount's knowledge of the root pointer is *in
doubt*, and it writes the root pointer no further until one read of it, its
body and its ETag from one response, has shown which of three things is true:

- **ours**: the root pointer names the attempt's snapshot. The write took
  effect; the mount adopts it, taking the read's ETag.
- **unchanged**: the root pointer names the snapshot the mount's namespace
  descends from. The write did not take effect; the mount takes the read's
  ETag, which is the one it should have swapped against, and its next swap
  proceeds.
- **theirs**: the root pointer names neither, or is gone. Another writer owns
  the bucket; the mount diverges, as it does today, and its log names that
  writer.

A read that fails, returns no ETag where one is needed, or returns a body the
mount does not positively understand decides nothing: the root stays in doubt,
the commit fails, and the next commit reads again before it writes anything.

**Why that closes #5.** Each route in *Context* is a write of the root pointer
whose reply withheld the root's ETag; today the mount swaps against the ETag
it still holds and reads the refusal as a second writer. Under the rule no
swap is made until the read has said what the root pointer holds. A lost
reply, a lost fallback reply, a 501 that took effect and a reply with no ETag
each leave the root pointer naming the attempt: the read shows *ours*, and the
commit is done. A 409, a transport error whose request never arrived, and a
failed unconditional `Put` leave the root pointer as it was: the read shows
*unchanged*, and the next commit swaps against an ETag the read confirmed. An
ETag a proxy altered is replaced by the read's, which is the same proxy's, so
the mount keeps failing its swap there, logging that the root pointer is
unchanged, rather than diverging with the wrong cause named (Assumption 3).
Only a root pointer that names a key the mount neither wrote nor descends from
diverges it, and the body that shows it carries `Writer`, `Epoch` and
`Snapshot`, which is what the log names. A real second writer is still
detected in the same call that detects it today (§4), since the read follows
the failed swap at once.

### 2. The machine

Pinned, in a new file `internal/blobfs/rootknowledge.go`, whose `Spec:` line
cites ADR 0014 §3 and this section (Assumption 14):

```go
type rootState uint8

const (
    rootKnown   rootState = iota + 1 // FS.rootETag is the current ETag of the root pointer that names FS.rootSnapshot
    rootInDoubt                      // a write of the root pointer left its outcome, or the root pointer's ETag, unknown; FS.doubtKey names the attempt
)

type rootEvent uint8

const (
    rootSwapOK         rootEvent = iota + 1 // a write's reply gave the root pointer's ETag: Known to Known
    rootSwapUnknown                         // a write's reply did not: Known to InDoubt
    rootProbeOurs                           // the read showed the root pointer naming the attempt: InDoubt to Known
    rootProbeUnchanged                      // the read showed it naming the snapshot the namespace descends from: InDoubt to Known
)

var rootStates = fsm.NewTable("rootKnowledge",
    fsm.Edge[rootState, rootEvent]{From: rootKnown, On: rootSwapOK, To: rootKnown},
    fsm.Edge[rootState, rootEvent]{From: rootKnown, On: rootSwapUnknown, To: rootInDoubt},
    fsm.Edge[rootState, rootEvent]{From: rootInDoubt, On: rootProbeOurs, To: rootKnown},
    fsm.Edge[rootState, rootEvent]{From: rootInDoubt, On: rootProbeUnchanged, To: rootKnown})
```

- `String()`, with value receivers (ADR 0014 §3), gives `"Known"`,
  `"InDoubt"`, `"SwapOK"`, `"SwapUnknown"`, `"ProbeOurs"` and
  `"ProbeUnchanged"` (vocabulary V2). Any other value gives
  `"rootState(<n>)"` or `"rootEvent(<n>)"`, `n` in decimal.
- **V2's names stand**, with two meanings widened (Assumption 2). `InDoubt`
  means that a write of the root pointer left its outcome *or the root
  pointer's ETag* unknown, so that a reply with no ETag (#103) is in doubt as
  a lost reply is. `SwapUnknown` follows *every* write of the root pointer
  whose reply did not establish its ETag, a 412 included, and not only a lost
  reply or an untyped error: V2 said "after `SwapReplyLost` or `SwapFailed`",
  and this ADR revises it. `ProbeTheirs` stays an action and no event: the
  machine stays `InDoubt` and the mode moves. The table gains no other edge.
- `FS` gains three fields, every one guarded by `FS.mu` as `FS.rootETag` is:
  ```go
  // rootKnowledge is whether rootETag is known to be the current ETag of the
  // root pointer that names rootSnapshot. Every read and every Fire is made
  // in commitLocked or resolveRoot, holding mu for writing from Sync, or
  // before the FS is shared, from initEmpty. No policy's request and no gate
  // reads it, and every transition is caused by a store's answer, so no
  // transition asks a policy (ADR 0018 §5; ADR 0022 §6). New makes it and
  // nothing reassigns it.
  rootKnowledge *fsm.Machine[rootState, rootEvent]
  // rootSnapshot is the snapshot key the root pointer named when rootETag was
  // last set: by loadOrInit, by a commit's success path, or by resolveRoot.
  // "" means no root, as "" does in rootETag.
  rootSnapshot string
  // doubtKey is the snapshot key of the attempt whose write of the root
  // pointer left the root in doubt, while rootKnowledge is InDoubt; ""
  // otherwise.
  doubtKey string
  ```
  `New` makes `rootKnowledge` with `fsm.NewMachine(rootStates, rootKnown)`
  where it makes `f.mode`, before `loadOrInit`, since `initEmpty`'s commit
  fires on it; making it calls nothing on the store (ADR 0009 §2, ADR 0011
  §2). `loadOrInit` sets `f.rootSnapshot = rp.Snapshot` beside
  `f.rootETag = etag`; `initEmpty` sets both to `""`. `rootSnapshot` and
  `doubtKey` are values, not named states (ADR 0018 §2), as `rootETag` is.
  While `rootKnowledge` is `InDoubt`, `doubtKey` is at epoch `FS.epoch + 1`:
  the attempt was written at that epoch, and no field moves while the root
  is in doubt, since no commit succeeds before the doubt is settled and
  `probeOurs` clears it (§3, §4). The doc comment above named `loadOrInit`
  as a site until 2026-10-28; `loadOrInit` neither reads nor fires the
  machine, and the pinned text is corrected (Assumption 27).
- **Every write of the state goes through `Fire`** (ADR 0014 §3): the sites
  are those §4 lists, and nothing else fires anything. A `Fire` that fails is
  logged at `Error` with ADR 0014 §9's keys `machine`, `from`, `event` and
  `err`; what `commitLocked` then does is pinned per site in §4. Every such
  failure is unreachable in this build, and each is kept because ADR 0014 §3
  asks it of every owner.
- **Composition with the mount mode.** `ProbeTheirs` fires `mountDiverge` on
  `f.mode`, exactly as `commitLocked`'s `store.ErrPrecondition` case does at
  `edcfc15`, with the same `Error` record for a `Fire` that fails and the
  same indifference to it (ADR 0014 §7, Assumption 38). Since the mode's
  `Fire` moves into `resolveRoot`, which only `commitLocked` calls, every
  `Fire` on the mode is still made under `commitLocked`'s hold of `FS.mu`
  for writing, or before the FS is shared, as the mode's doc comment says
  (ADR 0014 §7; ADR 0018 §5); that comment now names `resolveRoot` too. The
  `store.ErrPrecondition` case of `commitLocked` fires `rootSwapUnknown`
  instead, and nothing else moves the mode. In `internal/blobfs/mode.go` the
  comment on `mountDiverge`, which ADR 0014 §7 pinned as
  `// a swap failed with store.ErrPrecondition`, is since this ADR
  `// a read of the root pointer showed another writer's root pointer, or none (ADR 0022 §3)`;
  this replaces that one comment of ADR 0014 §7's pinned block, and
  `mountDiverged`'s, `lost a swap to another writer; terminal`, stands
  (Assumption 27).
- **The lock.** `rootKnowledge` is an `fsm.Machine`, read and fired only
  under `FS.mu` for writing or before the FS is shared, as ADR 0014 §8
  decided; `mutable()` does not read it (§6), so no `fsm.Atomic` is needed.
  `Fire` takes no lock and waits for nothing (ADR 0014 §4), so the lock order
  is as it was.

### 3. The probe

**When it runs.** Both at the failure and before the next swap
(Assumption 6):

1. **At the failure**, in the same call of `commitLocked` and under the same
   hold, straight after a write of the root pointer whose reply did not
   establish its ETag. So in the common case, a store that answers the read,
   the mount knows the outcome before `commitLocked` returns: a lost reply's
   commit returns nil, and a real second writer diverges the mount in the
   same call, as today.
2. **Before the next swap**, at the top of `commitLocked`, whenever
   `rootKnowledge` is `InDoubt` when a commit begins, which happens only when
   the read at the failure itself failed or decided nothing. The read comes
   before the snapshot `Put`, so an in-doubt mount writes nothing to the
   bucket until the doubt is settled. A mount in doubt whose namespace is
   clean is unreachable, since every path that leaves the root in doubt also
   leaves the commit failed and the namespace dirty (§4).

It never runs on a timer, and never while `rootKnowledge` is `Known`.

**One read.** The probe is one call, `f.store.GetWithETag(ctx, rootKey)`, the
read ADR 0020 §2 added, so that the body and the ETag describe one root
pointer. It is made holding the locks `commitLocked` holds, as its other store
calls are (§5, lock discipline).

**The verdict** is a pure function, pinned in `internal/blobfs/commit.go`:

```go
// probeVerdict is what one read of the root pointer shows a mount whose
// knowledge of it is in doubt (ADR 0022 §3).
type probeVerdict uint8

const (
    probeOurs      probeVerdict = iota + 1 // the root pointer names the attempt in doubt: adopt it
    probeUnchanged                         // it names the snapshot the namespace descends from: take its ETag and proceed
    probeTheirs                            // it names another key, or is gone: another writer owns the bucket
    probeFailed                            // the read failed, gave no ETag, or gave a body this build does not understand: still in doubt
)

// classifyRoot decides what one read of the root pointer shows, given the
// snapshot key the mount's namespace descends from and the key of the attempt
// in doubt. It takes no lock, makes no store call and logs nothing: a step the
// server takes on a store's answer, not a policy (ADR 0022 §3, §6).
//
// TLA+: docs/tla/CommitProtocol.tla ProbeOurs, ProbeUnchanged, ProbeTheirs, ProbeFailed
func classifyRoot(raw []byte, etag string, err error, rootSnapshot, doubtKey string) (probeVerdict, rootPointer)
```

Its rules, in order; the first that applies decides, and the `rootPointer`
returned is the decoded body where one decoded and the zero value otherwise:

1. `errors.Is(err, store.ErrNotFound)` and `rootSnapshot == ""`:
   `probeUnchanged`. There is no root pointer, and the mount descends from
   none: the create did not happen. (Only `initEmpty`'s commit reaches it.)
2. `errors.Is(err, store.ErrNotFound)` and `rootSnapshot != ""`:
   `probeTheirs`. The root pointer the mount descends from is gone.
3. any other `err`: `probeFailed`.
4. `raw` does not decode as `rootPointer`, or decodes with `Version` other
   than `snapshotVersion`, or with `Snapshot == ""`: `probeFailed`. As
   ADR 0021 Assumption 26 has it for the reclaim, `null`, `{}`, an error
   document served with 200 and a root pointer of another format version are
   bodies the mount does not positively understand, and nothing is decided
   against them (Assumption 5).
5. `Snapshot == doubtKey`: `probeOurs` if `etag != ""`, else `probeFailed`.
6. `Snapshot == rootSnapshot`: `probeUnchanged` if `etag != ""`, else
   `probeFailed`.
7. otherwise: `probeTheirs`, whatever `etag` is.

So *ours* and *unchanged* are decided by the key the body names, never by the
ETag (Assumptions 3 and 4): the attempt's key carries a nonce no other attempt
has (ADR 0013 Assumption 6), and the key the namespace descends from is the one
`loadOrInit` loaded or the last success or adoption set. The ETag is what the
verdict *takes*, for the next swap's condition; a body that is ours or
unchanged but comes with no ETag is a failed read, since the mount would
otherwise hold an ETag that means "no root" for a root pointer that exists
(ADR 0020 Assumption 3). *Theirs* needs no ETag, since the mount will make no
further swap.

The read shows the root pointer as the store served it, and *theirs* is
decided against the two keys the mount holds at that moment. A write of this
mount's own that applies after the read was served, which a conditional
`PUT` can do when the client's timeout gives up on it while it is still in
flight, is outside what one read can show: the next swap then fails its
condition, the read after it shows a key the mount no longer holds, and the
verdict is *theirs*, with the mount's own writer id named. Nothing
acknowledged is lost, and the window is the one between that read and the
next swap; it is a residual of this rule, not a case of it, recorded in
Assumption 30 with the issue it gets.

**What `resolveRoot` does** with each verdict, pinned as a method of `FS` in
`internal/blobfs/commit.go`, called only by `commitLocked`, holding what
`commitLocked` holds, and only while `rootKnowledge` is `InDoubt`:

```go
// resolveRoot settles a root pointer in doubt with one read, GetWithETag of
// rootKey, and applies the verdict: ProbeOurs adopts the attempt, ProbeUnchanged
// takes the root pointer's ETag, ProbeTheirs diverges the mount, and a read
// that decides nothing leaves the root in doubt. Caller must hold mu for
// writing, or run before the FS is shared, and rootKnowledge must be InDoubt
// (ADR 0022 §3).
//
// TLA+: docs/tla/CommitProtocol.tla ProbeOurs, ProbeUnchanged, ProbeTheirs, ProbeFailed, Diverge
func (f *FS) resolveRoot(ctx context.Context) probeVerdict
```

- **`probeOurs`.** Fire `rootProbeOurs`. Then `f.epoch = rp.Epoch`, which for
  a body that names the attempt's key is the attempt's epoch (Assumption 4);
  `f.rootETag = etag`; `f.rootSnapshot = f.doubtKey`; `f.doubtKey = ""`. Log
  one record at `Info` whose message contains `adopted`, with the key and the
  epoch. It touches neither `f.dirty` nor `f.lastCommit` and schedules no
  pass: what follows is the caller's (§4).
- **`probeUnchanged`.** Fire `rootProbeUnchanged`. Then `f.rootETag = etag`;
  `f.doubtKey = ""`; `f.epoch` and `f.rootSnapshot` as they were. Log one
  record at `Warn` whose message contains `unchanged`, with the ETag the mount
  held and the one it took, as `held_etag` and `etag`, and the key both name
  as `snapshot`, so that a store that refuses a condition that holds is
  visible in the log (Assumption 3; Assumption 28).
- **`probeTheirs`.** Fire `mountDiverge` on `f.mode`, with ADR 0014 §7's
  record if that fails; `rootKnowledge` stays `InDoubt` and `f.doubtKey` as
  it is. Log one record at `Error` whose message contains `commit conflict`,
  carrying the attributes `writer` (the body's `Writer`; `""` when the root
  pointer is missing), `their_epoch` (its `Epoch`), `their_snapshot` (its
  `Snapshot`), `our_epoch` and `attempted`, which is `f.epoch + 1`, the
  attempt's epoch (§2). When the root pointer is missing the message is a
  second one, also containing `commit conflict`, which says that the root
  pointer the mount descends from is gone, and `their_epoch` and
  `their_snapshot` are zero, as `writer` is (Assumption 5; Assumption 28).
  This is the record ADR 0014 §9 calls the record of `Diverge`, with the
  writer named, which is the issue's second done-when (Assumption 15).
- **`probeFailed`.** Fire nothing; change no field. Log one record at `Error`
  whose message contains `in doubt`, with `attempt`, the key in doubt, and
  the read's error as `err`; or, where the read returned a body the mount
  does not understand, or one it understands with no ETag, with `version`,
  `snapshot`, `etag` and `bytes`, the decoded fields, the ETag and the
  body's length, rather than the body itself, as ADR 0021's record of a body
  not understood carries its fields (Assumption 28). A body that does not
  decode shows as version 0 with an empty snapshot.
- A `Fire` of `rootProbeOurs` or `rootProbeUnchanged` that fails is logged
  as §2 says, nothing is adopted or taken, and the verdict returned is
  `probeFailed`.

**No in-line retry.** After `probeUnchanged` at the failure, `commitLocked`
does not swap again in the same call: the commit fails with the swap's error,
the mount stays `Known` and dirty, and its next commit, one `-commit-interval`
later or at a client's `COMMIT`, swaps against the ETag the read gave
(Assumption 7).

### 4. `commitLocked`'s paths

Every path, pinned. "Success" means the success path as at `edcfc15`:
`f.epoch = epoch`, `f.rootETag = <the ETag>`, `f.dirty = false`,
`f.lastCommit = time.Now()`, the `"committed"` `Debug` record, and
`schedulePrune(key)`, plus, now, `f.rootSnapshot = key`. "Doubt" means:
`f.doubtKey = key`, then `Fire(rootSwapUnknown)`; a `Fire` that fails is
logged as §2 says, `f.doubtKey` is cleared again, no read is made, and
`commitLocked` answers the outcome as it did at `edcfc15`, except that it
moves no mode: the conflict error for `store.ErrPrecondition`, with the
mount left `Writable` where `edcfc15` diverged it, since only `resolveRoot`
fires `mountDiverge` (§2); `swap root pointer: %w` for an untyped or
transport error; `write root pointer: %w` for a failed fallback `Put`;
ADR 0021's error for its non-positive cases; and, for a reply with no ETag
or a `Head` that fails or returns `""`, the success path with `""` as
`FS.rootETag`, as `edcfc15` took it. The path is unreachable in this build,
since the top of the commit settles any doubt before the swap, and it is
pinned so that §2 and this section agree on who moves the mode. Until
2026-10-28 this said "so that the mount is left as the old code left it",
which for `store.ErrPrecondition` would have moved the mode (Assumption 2;
Assumption 32).

**At the top**, in this order: the `mountDiverged` refusal, storing nothing,
as now; the unsettled-file refusal, as now; then, **only if `rootKnowledge`
is `InDoubt`**, `resolveRoot`:
- `probeFailed`: return an error whose text contains `in doubt`. It wraps
  nothing: `resolveRoot` returns the verdict alone (§3); the read's error,
  where there was one, is in the `in doubt` record of §3; and a read that
  decided nothing may have returned no error at all, a body not understood
  or a reply with no ETag, so there is not always one to wrap. Until
  2026-10-28 this said "wrapping the read's error" (Assumption 25). Nothing
  is written.
- `probeTheirs`: return the conflict error (below). Nothing is written.
- `probeOurs`: the attempt is adopted, and the commit continues: the snapshot
  it writes is at the adopted epoch plus one, and holds the namespace as it
  stands, which may have changed since the attempt. `f.dirty` is true, since
  that is why `Sync` called, so nothing is skipped.
- `probeUnchanged`: the commit continues, swapping against the ETag the read
  gave.

**After the swap**, `PutIfMatch(ctx, rootKey, rpBody, f.rootETag)` returning
`(newETag, err)`:

- `err == nil` and `newETag != ""`: `Fire(rootSwapOK)`, a self-loop, then
  success with `newETag`. A `Fire` that fails is logged and success follows
  anyway, since the store applied the write (ADR 0014 Assumption 38's
  reasoning).
- `err == nil` and `newETag == ""` (#103): doubt, then `resolveRoot`:
  - `probeOurs`: success, with the ETag the read gave; return nil.
  - `probeFailed`: return an error whose text contains `no ETag` and
    `in doubt`. `f.dirty` stays true, so the next commit resolves the doubt
    first; the commit is durable, and the retry it causes writes the same
    namespace again (Assumption 10).
  - `probeUnchanged`: return an error whose text contains `no ETag`. The
    store said it applied the write and then showed the root pointer as it
    was; the mount takes the read's ETag and tries again next time.
  - `probeTheirs`: return the conflict error.
- `errors.Is(err, store.ErrPrecondition)`, 412 or 409: doubt, then
  `resolveRoot`:
  - `probeTheirs`: return the conflict error, as at `edcfc15` (the mode is
    `Diverged`).
  - `probeUnchanged`: return `swap root pointer: %w` wrapping `err`. A 409,
    or a refusal of a condition that holds, costs one failed commit and no
    divergence.
  - `probeOurs`: success; return nil. A store that applied a write it
    answered 412 or 409 is outside every assumption here, and the adoption is
    still right: the root pointer names the attempt.
  - `probeFailed`: return `swap root pointer: %w` wrapping `err`, with
    `in doubt` in its text.
- `errors.Is(err, store.ErrUnsupported)` and `!f.fallbackAllowed()`:
  ADR 0021 §2's refusal, unchanged in its record, its read of `rootKey` with
  `Get`, its `Delete` and its error, except in the cases where that read did
  not positively show a root pointer other than the attempt's (Assumption
  11):
  - a not-found, or a decoded root pointer of this version whose `Snapshot`
    is non-empty and differs from `key`: the reclaim's `Delete`, and
    ADR 0021's error; `rootKnowledge` stays `Known`, and nothing fires.
  - the body names `key` (ADR 0021's step 3, the 501 that took effect), the
    read failed, or the body is not understood (its step 4): ADR 0021's
    records as they are, no `Delete`, then doubt and `resolveRoot`:
    `probeOurs` is success and returns nil; `probeTheirs` returns the
    conflict error; `probeUnchanged` and `probeFailed` return ADR 0021's
    error, which wraps `store.ErrUnsupported` and contains `unconditional`.
    So a 501 that took effect is adopted in the same call, and a refused
    commit whose guard read failed is settled at the next commit rather than
    diverging the mount later (Assumption 11).
- `errors.Is(err, store.ErrUnsupported)` and `f.fallbackAllowed()`: the
  `Warn` record and the unconditional `Put`, as at `edcfc15`; then:
  - the `Put` returns an error: doubt, then `resolveRoot`: `probeOurs` is
    success and returns nil; `probeTheirs` the conflict error;
    `probeUnchanged` and `probeFailed` return `write root pointer: %w`
    wrapping the `Put`'s error. `S3.Put` is retried, so the `Put` may have
    taken effect (Assumption 12).
  - the `Put` succeeds and the `Head` returns a non-empty ETag:
    `Fire(rootSwapOK)`, then success with it, as at `edcfc15`.
  - the `Put` succeeds and the `Head` fails or returns `""`: doubt, then
    `resolveRoot`, with the outcomes of the `newETag == ""` case above.
- any other `err`, an untyped error from `errorFor` or a transport error:
  doubt, then `resolveRoot`, with the outcomes of the `store.ErrPrecondition`
  case: `probeOurs` is success and returns nil (the lost reply, route 1);
  `probeUnchanged` and `probeFailed` return `swap root pointer: %w`;
  `probeTheirs` the conflict error.

**The conflict error** is the one `commitLocked` returns at `edcfc15`, whose
text contains `commit conflict`; the rest of the text is not pinned. One
value serves both sites, the top of a commit and the read after a write
(Assumption 28).

**From `initEmpty`**, the same paths run before the FS is shared: a lost
reply on the create is adopted and `New` succeeds; a verdict that fails the
commit makes `New` return that error and a nil `*FS`, as any commit error
does today (ADR 0011 §7's rule that such an `*FS` is never used stands).
Rule 1 of `classifyRoot` is the only one an empty `rootSnapshot` reaches.

**`TLA+:` lines** (ADR 0013 §6). `commitLocked`'s gains `SwapNoETag`,
`SwapUnknown`, `ProbeOurs`, `ProbeUnchanged`, `ProbeTheirs` and
`ProbeFailed`, and loses `Diverge`, which it no longer performs: `trace.tsv`
has one row per action and symbol, and its `Diverge` row names `resolveRoot`
alone (Assumption 26); `resolveRoot` and `classifyRoot` carry the lines above;
`S3.GetWithETag` and `Local.GetWithETag` gain `ProbeOurs, ProbeUnchanged,
ProbeTheirs, ProbeFailed`; `S3.PutIfMatch` gains `SwapNoETag`; `errorFor`
gains `ProbeUnchanged, ProbeTheirs, ProbeFailed`, since its mapping of a
404 and `NoSuchKey` to `store.ErrNotFound` decides `classifyRoot`'s rules 1
and 2 as its mapping of 412 and 409 decides `SwapConflict`, and its untyped
error rule 3; until 2026-10-28 it gained `ProbeFailed` alone (Assumption
33). `docs/tla/trace.tsv` gains the rows §7 lists, an `errorFor` row under
each of those three actions among them, and the `Diverge` row names
`FS.resolveRoot` where it named `FS.commitLocked`.

### 5. Interactions

- **Pruning (#88, ADR 0019).** ADR 0019 §1's step 2 rests on the key the root
  pointer names never falling below the key a pass was begun for. Adoption
  keeps it: the key adopted is the one the root pointer already names, so it
  is at or above every pass's key; and the swap that follows an adoption is
  at the adopted epoch plus one. The success path at the failure site calls
  `schedulePrune(key)` for the adopted key, as a swap's success does; an
  adoption at the top of a commit schedules no pass, since the commit that
  follows schedules one for a higher key if it succeeds, and
  `schedulePrune` is a no-op while a pass runs. `CommitProtocol_PruneSwapReply`,
  the configuration the issue names, is MustHold already and takes no change
  (ADR 0013 §7); `CommitProtocol_PruneProbe` (§7) checks every property with
  lost replies, failed reads and a pass at retention 1. In R7 and R8 of
  ADR 0019 §6 a lost reply's `Sync` now returns nil, so those tests change
  as §8 says; the rule, the Go change and the configurations of ADR 0019 do
  not.
- **ADR 0021's reclaim.** Its "took effect" case becomes a probe, and the
  attempt is adopted: `Sync` returns nil there, and G3 changes as §8 says.
  Its positive cases, a not-found or a root pointer naming another key, are
  unchanged: a 501 is taken to change nothing (the module's J4), and the
  guard read confirmed it, so no doubt is raised and no further read is
  made. Its read stays a `Get`, not a `GetWithETag` (Assumption 11), so G1,
  G2, G4, G5 and G6 stand.
- **#103, a swap reply with no ETag**, is in scope and closed by the same
  rule: the reply did not establish the ETag, so the root is in doubt and the
  read supplies it (Assumption 10). ADR 0020 Assumption 3's asymmetry, a
  refusal at mount and nothing at a swap, is closed, and that ADR gains a
  cross-reference. The opted-in fallback's `Head` failing after its `Put`,
  which `CommitProtocol.tla` leaves out, is handled by the same case.
- **Lock discipline (#43; ADR 0014 §4).** The read is one more store call
  under the commit gate and `FS.mu`, beside the snapshot `Put`, the swap, the
  opted-in fallback's `Put` and `Head`, and ADR 0021's reclaim. #43 records
  those calls as a defect to fix for all of them, and when its fix moves them
  off the lock the probe moves with them (Assumption 22). The probe adds no
  lock and takes none: `Machine.Fire` is a map lookup, and the lock order,
  the commit gate, then `openFile.mu`, then `FS.mu`, is unchanged.
- **The log record that names the other writer** is the `probeTheirs` record
  of §3. Two mounts in one process share a `writerID`, since it is the host
  name and the process id; whether it should carry more is not decided here
  (Assumption 15).
- **The tests of the mount mode** (ADR 0014 §11, M3, M4, M7) induce
  divergence by a fake that refuses the swap with `store.ErrPrecondition`
  while the root pointer is in fact unchanged. Under the rule that is
  *unchanged*, not a second writer, and such a mount no longer diverges;
  those tests change to a real second writer, as §8 says. M6 and M8 stand.

### 6. ADR 0018

- **The machine is not security-relevant.** No policy's request and no gate
  reads `rootKnowledge`: `mutable()` reads `f.mode` alone (ADR 0014 §7), and
  so does `Access`'s mask. It records no authority a caller holds. So none of
  its transitions is security-relevant under ADR 0018 §5, and none asks a
  policy. In particular an in-doubt mount keeps accepting changes: doubt is
  the store being unreachable or unclear, which is the condition under which
  the mount already buffers changes and commits them later (Assumption 8).
- **The mode's `Diverge` is as ADR 0018 §5 has it**: it only narrows, and a
  store's answer causes it, the probe's answer now rather than the swap's.
  It needs no policy, and every `Fire` of it is still made under
  `commitLocked`'s hold of `FS.mu` for writing, or before the FS is shared
  (§2). ADR 0018 gains a cross-reference and no new rule.
- **`classifyRoot` is not a policy.** It decides a step the server takes on a
  store's answer, as `prunable` and `fallbackAllowed` do (ADR 0019 §5,
  ADR 0021 §5): no caller asks for a divergence or an adoption, and it reads
  no caller. `docs/vocabulary.md` V7 names it, as it asks of a
  commit-protocol decision.
- So nothing is converted beyond the machine this ADR adds, no exemption is
  granted, and `docs/adr/exemptions.md` is unchanged.

### 7. The model

In `docs/tla/CommitProtocol.tla`, under a `Revised: 2026-10-27` line
(Assumption 16):

- `Fixes` gains the member **`RootProbe`** (`docs/vocabulary.md`, V3), and
  `AllFixes` lists it. `Faults` gains **`"ProbeFailed"`**, the probe's read
  failing, and **`"SwapNoETag"`**, a swap applied and answered 200 with no
  ETag (#103); `AllFaults` lists both.
- Three variables join `MountVars`: `rootKnowledge[m]`, in
  `{"Known", "InDoubt"}` (V2); `doubtKey[m]`, the key of the attempt in
  doubt, or `NoKey`; and `rootSnapshot[m]`, the key the root pointer named
  when `rootETag[m]` was last set, or `NoKey`. `pc[m]` gains `"Probe"`.
  `Init` gives every mount `"Known"`, `NoKey`, and `NoKey` or `LoadedKey` by
  `Start`; `GetRoot` records `rootSnapshot[m]`, `InitEmpty` and `Crash`
  reset the three, and `Committed` sets `rootSnapshot[m]` to the key it
  commits.
- Two operators: `SwapUnknown(m, k)`, V2's event, which moves `m` to
  `"InDoubt"` with `doubtKey[m] = k`, `pc[m] = "Probe"` and no attempt; and
  `Unresolved(m, k)`, which is `SwapUnknown(m, k)` with the member and
  `CommitFails(m, k)` without it. `SwapReplyLost`, `SwapFailed`, and
  `SwapUnsupported`'s two fallback branches that fail, the `Put` not applied
  and the `Put` applied with its reply lost, conjoin `Unresolved` where they
  conjoined `CommitFails`. `SwapConflict` conjoins `SwapUnknown` with the
  member and `Diverge` with `CommitFails` without it, so with the member no
  swap diverges a mount. `SwapOK` is also V2's `SwapOK` event, which changes
  no variable of the machine.
- A new action **`SwapNoETag(m)`**: the swap is applied and answers with no
  ETag. Without the member it is `Committed(m, k, NoETag)`, as `commitLocked`
  stores an empty reply ETag at `edcfc15`, so the next swap is
  `If-None-Match` and conflicts; with the member it is `SwapUnknown(m, k)`.
- `NextStep(m)` returns `"Probe"` at `"Ready"` when the namespace is dirty,
  the mount has not diverged and `rootKnowledge[m]` is `"InDoubt"`, so no
  snapshot `Put` and no swap is enabled while the root is in doubt; it
  returns `pc[m]` itself at `"Probe"`.
- Four probe actions, each enabled when `NextStep(m) = "Probe"` and each one
  read of the root pointer:
  - **`ProbeOurs(m)`**: `bucketRoot.key = doubtKey[m]`. The machine returns
    to `"Known"`, `rootSnapshot[m]` becomes the key, and `overtaken[m]` is
    cleared. At `"Probe"`, the failure site, it is `Committed(m, doubtKey[m],
    bucketRoot.etag)`: the commit is acknowledged, the namespace clean, and a
    pass may begin. At `"Ready"`, the top of a later commit, it adopts only:
    `epoch[m]`, `rootETag[m]` and `base[m]` take the attempt, and `nsDirty`,
    `acked` and the pass are untouched.
  - **`ProbeUnchanged(m)`**: `bucketRoot.key = rootSnapshot[m]`. The machine
    returns to `"Known"`, `rootETag[m]` takes `bucketRoot.etag`, and
    `overtaken[m]` is cleared. At `"Probe"` the commit fails
    (`CommitFails`); at `"Ready"` nothing else moves.
  - **`ProbeTheirs(m)`**: `bucketRoot.key` is neither. `Diverge(m)`; the
    machine stays `"InDoubt"`; at `"Probe"` the commit fails.
  - **`ProbeFailed(m)`**: with the fault, nothing moves but, at `"Probe"`,
    the commit failing. At `"Ready"` it is a stuttering step.
- `TypeOK` checks the new variables and the new `pc` string. R11 and the
  comments of `Diverge`, `SwapConflict`, `SwapReplyLost`, `SwapFailed` and
  `SwapUnsupported` say where `Diverge` is fired with and without the member.
  New reductions R13 and R14 say why the read at the failure site is a step
  of its own and why `classifyRoot` and the `Fire` fold into it. The
  Leaves-out list names what Go handles and the model does not reach: a 501
  that took effect (J4), and a fallback `Head` that fails, both of which
  Go's rule turns into a probe.
- **The three MustFail configurations of #5 flip to MustHold**, `RootProbe`
  added to each one's `Fixes`, their two invariants and every other line
  unchanged (ADR 0013 §7): `CommitProtocol_SelfDiverge`,
  `CommitProtocol_SelfDiverge409` and `CommitProtocol_SelfDivergeFallback`.
  Each comment records the flip and the route it showed before it.
- **Four MustFail configurations remove the fix**, each on
  `NoSelfDivergence`: `CommitProtocol_NoRootProbe`,
  `CommitProtocol_NoRootProbe409` and `CommitProtocol_NoRootProbeFallback`,
  which are the three above without the member, so that each route the fix
  closes keeps a row that shows the member load-bearing for it; and
  `CommitProtocol_NoRootProbeNoETag`, which shows #103 without it. The
  first three are their parents' lines less the member and nothing else,
  since a flipped configuration keeps the two invariants it had; the
  fourth's parent is a new MustHold configuration with every property, and
  a MustFail configuration checks `TypeOK` and its one property (ADR 0013
  §7; `docs/tla/README.md`), so the fourth differs from its parent in its
  invariants too, which its comment says since 2026-10-28 (Assumption 31).
- **Four MustHold configurations** check every property of the module with
  the member: `CommitProtocol_SwapNoETag`, one writer with the fault
  `SwapNoETag`, the fix for #103 shown (its MustFail twin above removes it);
  `CommitProtocol_ProbeFailed`, one writer from an empty bucket with every
  swap fault, both snapshot faults, `ProbeFailed` and a crash, so that a
  mount left in doubt by a failed read resolves it at its next commit before
  it writes anything; `CommitProtocol_ProbeTwoWriters`, two writers that
  loaded one root pointer with the same faults, so that a read adopts only
  this mount's attempt, takes an ETag only against the key its namespace
  descends from, and diverges a mount only after another writer: `NoLostAck`
  and `NoSelfDivergence` together pin the three verdicts, since a verdict
  too eager to adopt or to proceed breaks the first and one too eager to
  diverge breaks the second; and `CommitProtocol_PruneProbe`, one writer
  pruning at retention 1 with lost replies, failed swaps, lost snapshot
  replies, failed reads and a crash, for §5's argument on pruning. The new
  MustHold configurations carry every member the code has, since each is new
  and describes the code as it is with this ADR (Assumption 16);
  `CommitProtocol_SwapNoETag` lacked `PruneBelowCurrent`, `AtomicLoad` and
  `FallbackOptIn` until 2026-10-28, each inert under its constants, and
  gained them then, with its twin (Assumption 31).
- In `docs/tla/manifest.tsv` the three flipped rows and the eight new rows
  have `exit` and `log` `-` until the session has run them (ADR 0013 §7,
  §10); the session ran all eleven, and on 2026-10-28 the two rows whose
  `Fixes` changed, `CommitProtocol_SwapNoETag` and
  `CommitProtocol_NoRootProbeNoETag`, returned to `-` until it runs them
  again (Assumption 31). The module's `Checked against` stayed `faeba5b` for every action but
  those this ADR adds or changes until coder's commit, `71fee34`, was on the
  branch; on 2026-10-28 the architect checked every row this ADR added or
  changed against that commit, its symbol, the locks held and the action
  performed, found each to hold, and set the line to `71fee34` for every
  action (Assumption 20).

### 8. The test surface

Every name is spelled as in Go. test-author cannot read `internal/blobfs`'s
or `internal/store`'s non-test files, so everything a test may use is here.

**Names a test may use**, restated from the code at `edcfc15` unless marked
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
// Create, Lookup, Write and the other vfs.FS methods, as internal/vfs/vfs.go declares them
func quietLog() *slog.Logger // internal/blobfs/fs_test.go: a logger that discards everything

// unexported, at edcfc15
type FS struct {
    mu       sync.RWMutex
    epoch    uint64 // under mu
    rootETag string // under mu: the root pointer's ETag as last read or written; "" = no root
    dirty    bool   // under mu for writing
    mode     *fsm.Atomic[mountMode, mountEvent] // ADR 0014 §7
    // ...
}
type mountMode uint32      // mountWritable, mountReadOnly, mountDiverged; String() "Writable", "ReadOnly", "Diverged"
type mountEvent uint8      // mountDiverge; String() "Diverge"
var mountModes *fsm.Table[mountMode, mountEvent]
const (
    rootKey        = "root"
    snapshotPrefix = "snapshots/"
    rootInodeID    = 1
)
type rootPointer struct {
    Version   int    `json:"version"`
    Epoch     uint64 `json:"epoch"`
    Snapshot  string `json:"snapshot"`
    FSID      uint64 `json:"fsid"`
    WrittenNS int64  `json:"written"`
    Writer    string `json:"writer,omitempty"`
}
func decodeSnapshot(b []byte) (*snapshot, error) // the bytes of an object under snapshotPrefix
type snapshot struct { Version int; Epoch uint64; NextIno uint64; ChunkSize uint32; Inodes map[uint64]*inode; CreatedNS int64 }
type inode struct { ID uint64; Entries map[string]uint64 /* ... */ } // the root directory is Inodes[rootInodeID]

// new in this ADR (sections 2 and 3)
type rootState uint8   // rootKnown, rootInDoubt; String() "Known", "InDoubt"
type rootEvent uint8   // rootSwapOK, rootSwapUnknown, rootProbeOurs, rootProbeUnchanged; String() "SwapOK", "SwapUnknown", "ProbeOurs", "ProbeUnchanged"
var rootStates *fsm.Table[rootState, rootEvent] // named "rootKnowledge"
// FS gains, under mu: rootKnowledge *fsm.Machine[rootState, rootEvent]; rootSnapshot string; doubtKey string
type probeVerdict uint8 // probeOurs, probeUnchanged, probeTheirs, probeFailed
func classifyRoot(raw []byte, etag string, err error, rootSnapshot, doubtKey string) (probeVerdict, rootPointer)
```

```go
// internal/store, unchanged
type Store interface {
    Get(ctx context.Context, key string) ([]byte, error)
    GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error)
    Head(ctx context.Context, key string) (ObjectInfo, error)
    Put(ctx context.Context, key string, data []byte) error
    Delete(ctx context.Context, key string) error // a missing key is not an error
    List(ctx context.Context, prefix, after string, max int) ([]ObjectInfo, error)
    PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) // etag "" means create-only
    Name() string
    GetWithETag(ctx context.Context, key string) (data []byte, etag string, err error)
}
type ObjectInfo struct { Key string; Size int64; ETag string }
var ErrNotFound, ErrPrecondition, ErrUnsupported error
func NewLocal(root string) (*Local, error) // *Local implements Store; its PutIfMatch honours its condition and never returns ErrUnsupported
```

```go
// internal/fsm (ADR 0014 §2)
type Edge[S, E comparable] struct{ From S; On E; To S }
func (t *Table[S, E]) Name() string
func (t *Table[S, E]) Edges() []Edge[S, E]
func (m *Machine[S, E]) State() S
```

**Facts a test may rely on.**

- `New` on a `*store.Local` that holds no root pointer creates the
  filesystem with one `PutIfMatch` of `rootKey` whose `etag` argument is
  `""`; `New` on one that holds a filesystem makes no `Put`, `PutIfMatch` or
  `Delete` (ADR 0011 §1; ADR 0020 §2). After `New`, `f.rootKnowledge.State()`
  is `rootKnown`, `f.doubtKey` is `""`, and `f.rootSnapshot` is the key the
  root pointer at `rootKey` names, which for a fresh bucket is the key of the
  one object under `snapshotPrefix`.
- A *commit* is a `Create` of a fresh name in the root directory, by
  `vfs.Caller{UID: 501, GID: 20}`, the root directory's owner when `Config`
  has `OwnerUID` 501 and `OwnerGID` 20, then `Sync`. A commit that succeeds
  writes one object under `snapshotPrefix` and then one `PutIfMatch` of
  `rootKey` whose `etag` argument is the ETag the mount last loaded, was
  returned, or took from a read of the root pointer; its epoch is the mount's
  epoch plus one, and the key it writes carries that epoch as twenty digits.
- A commit that begins with `rootKnowledge` `Known` makes no `GetWithETag`
  of `rootKey` before its `PutIfMatch` of `rootKey`, and one whose swap's
  reply gives the ETag makes none at all: the probe runs only after a write
  whose reply did not, and never while `Known` (§3). `New` makes one
  `GetWithETag` of `rootKey`, its load, before `initEmpty`'s swap
  (ADR 0020 §2).
- On a `*store.Local`, an object's ETag is the lowercase hexadecimal MD5 of
  its body, the same from `Head`, from `GetWithETag` and as `PutIfMatch`
  returns it; `PutIfMatch` with an `etag` that is not the current body's MD5
  returns `store.ErrPrecondition` (ADR 0020 §6), and so does one with a
  non-empty `etag` of a key that holds no object; a `GetWithETag` of such a
  key returns `store.ErrNotFound`.
- A test may read `FS.dirty`, `FS.epoch`, `FS.rootETag`, `FS.rootSnapshot`
  and `FS.doubtKey`, and call `f.rootKnowledge.State()`, holding `FS.mu` for
  reading; it may read `f.mode.State()` without a lock (ADR 0014 §11). It
  may use `rootStates`, `mountModes`, and the types and constants §2 and §3
  pin. It may not write any unexported field but `FS.dirty` as ADR 0011 §7
  allows.
- Once a mount has diverged, every change on it returns `vfs.ErrStale`
  (ADR 0003 §4). A mount that has not, in doubt or not, accepts a `Create`
  of a fresh name (§6).
- A *foreign root pointer* is how a test stands for another writer: read
  `rootKey` from the inner `*store.Local`, decode it as `rootPointer`, set
  `Writer` to a string of the test's own, such as `"other-host/4242"`, `Epoch`
  to the mount's epoch plus one, `Snapshot` to a key under `snapshotPrefix`
  at that epoch with the nonce `ffffffffffff`, copy the bytes of the current
  snapshot to that key with `Put`, marshal and `Put` the root pointer at
  `rootKey`. Its `Version` is then this build's. The mount's own
  `PutIfMatch`, passed through, then fails its condition on the inner store,
  and the probe reads that root pointer.
- Log records can be captured with a `slog.Handler` of the test's own, as
  `btCapture` in `internal/blobfs/budget_test.go` does; a record's attributes
  are visited with `slog.Record.Attrs`.

**The harness**, `rk5Store`, in a new file `internal/blobfs/root_knowledge_test.go`
(Assumption 23): a `store.Store` over a `*store.Local` under `t.TempDir()`,
written without embedding `store.Store`, that passes every call through to the
inner store and, when armed, one-shot, in addition:

- **loses the next swap's reply**: applies the next `PutIfMatch` of `rootKey`
  on the inner store and returns `""` and an error of the test's own,
  `rk5ErrInjected` (`SwapReplyLost`);
- **fails the next swap** without calling the inner store, with an error the
  test chooses: `store.ErrPrecondition` (a 409, or a 412 while the condition
  holds: `SwapConflict409`), or an untyped error (`SwapFailed`), or
  `store.ErrUnsupported` (a 501);
- **drops the next swap reply's ETag**: passes the next `PutIfMatch` of
  `rootKey` through and returns `""` and nil (`SwapNoETag`);
- **applies then answers 501**: passes the next `PutIfMatch` of `rootKey`
  through and, if it succeeded, returns `""` and `store.ErrUnsupported`, as
  `sf91Store` does (ADR 0021 §6);
- **loses the next root `Put`'s reply**: applies the next `Put` of `rootKey`
  and returns `rk5ErrInjected` (`FallbackReplyLost`);
- **fails the next `Head` of `rootKey`**: answers it with a zero
  `store.ObjectInfo` and `rk5ErrInjected` without calling the inner store;
  and **drops the next `Head`'s ETag**: passes the next `Head` of `rootKey`
  through and returns its `ObjectInfo` with `ETag` `""` and a nil error.
  Either is the opted-in fallback's `Head` that fails or returns `""` (§4),
  which the model leaves out and K16 and K17 test; both arms were added on
  2026-10-28 (Assumption 34);
- **fails the next read of the root pointer**: answers the next
  `GetWithETag` of `rootKey` with `nil`, `""` and `rk5ErrInjected` without
  calling the inner store (`ProbeFailed`); or **answers it** with bytes and
  an ETag the test gives;
- **fails the first read of the root pointer after the next swap**: at the
  next `PutIfMatch` of `rootKey`, however that call is answered, arms the
  failed read above, so that the first `GetWithETag` of `rootKey` after it
  fails and every one before it passes through. K13's second subtest needs
  it: `New`'s load reads `rootKey` with `GetWithETag` before `initEmpty`'s
  swap, and a failure armed before `New` would be spent on the load. K7, K8
  and K10 use it too, so that the arm names the read it fails; that a commit
  begun `Known` makes no read of `rootKey` before its swap is K4's
  assertion, not theirs (Assumption 29);
- in its **unconditional mode**, set before the first call and never changed,
  answers every `PutIfMatch` of any key with `store.ErrUnsupported` without
  calling the inner store;
- records, in call order, every `GetWithETag` of `rootKey`, every `Get` of
  `rootKey`, every `PutIfMatch` of `rootKey` with its `etag` argument and how
  it was answered, every `Put` and `Head` of `rootKey` and how each was
  answered, every `Put` under `snapshotPrefix` with its key, and every
  `Delete` with its key.

It takes no hold: every route here is sequential. Two mounts in one test
share one harness.

**Tests of the machine and the verdict**, which cannot compile at `edcfc15`
and are written once the code exists:

- **K1** (`TestRootStatesTableAndStrings`). `rootStates.Name()` is
  `"rootKnowledge"`; `rootStates.Edges()` is exactly §2's four edges, in
  that order; the `String()` values are §2's; `rootState(0).String()` is
  `"rootState(0)"` and `rootEvent(0).String()` is `"rootEvent(0)"`.
- **K2** (`TestClassifyRootDecidesWhatOneReadShows`). `classifyRoot` over
  §3's rules, each with a case: a not-found with an empty and with a set
  `rootSnapshot`; another error; the five bodies of ADR 0021 G6 (`null`,
  `{}`, an error document served with 200, `<Error/>`, a root pointer of
  version 99), each `probeFailed`; a root pointer of this build's version
  naming `doubtKey`, `probeOurs`, and the returned `rootPointer` equal to
  the decoded body; naming `rootSnapshot`, `probeUnchanged`; naming a third
  key, `probeTheirs`; the first two with an empty `etag`, each
  `probeFailed`; the third with an empty `etag`, still `probeTheirs`. A
  root pointer of this build's version is made from one read off a `New`'s
  bucket, as the foreign-root-pointer fact says.
- **K3** (`TestNewStartsKnownWithTheRootPointersKey`). After `New` over an
  empty bucket and over a bucket that holds a filesystem: `rootKnown`,
  `doubtKey` `""`, and `rootSnapshot` the key the root pointer names.

**Route tests**, each from its configuration's counterexample or route
(ADR 0013 §8), in the same file. Every mount has `SnapshotRetention` −1 and
`MaxDirtyBytes` −1 unless said otherwise, and every assertion on an `FS`
field holds `FS.mu` for reading. Each is written in two parts, as ADR 0021
§6 wrote its F- and O-tests (Assumption 23): its calls, its records and its
assertions on `Sync`'s result, `f.mode.State()`, `FS.dirty`, `FS.epoch`,
`FS.rootETag`, the inner store, the captured log and a remount use only
names that exist at `edcfc15`, so that part compiles there and fails there
as the test's last sentence says, which is the red run; its assertions on
`f.rootKnowledge.State()`, `f.doubtKey` and `f.rootSnapshot`, and K1 to K3
whole, cannot compile at `edcfc15` and are added once coder's change is on
the branch.

- **K4, a lost reply** (`TestLostSwapReplyIsAdoptedNotTakenForASecondWriter`;
  `CommitProtocol_SelfDiverge`, the issue's first done-when). `New` over an
  empty bucket on the harness; arm the lost reply; commit `c2`. **`Sync`
  returns nil.** The harness recorded, since the commit began, no
  `GetWithETag` of `rootKey` before the `PutIfMatch` it applied, and after
  it exactly one `GetWithETag` of `rootKey` and no further `PutIfMatch` of
  `rootKey`: the read follows the write that raised the doubt and never
  precedes it (§3; the fact above). `f.mode.State()` is `mountWritable`,
  `rootKnown`, `doubtKey` `""`, `rootSnapshot` the key the applied
  `PutIfMatch` wrote, `epoch` 2, `rootETag` the MD5 of the root pointer's
  bytes on the inner store, and `FS.dirty` false. Commit `c3`: `Sync`
  returns nil, its `PutIfMatch` passed through with an `etag` argument equal
  to that MD5, its key carries epoch 3, and the harness recorded no
  `GetWithETag` of `rootKey` in that commit at all: the probe never runs
  while `Known` (§3). A remount on the inner store has `c2` and `c3`. At
  `edcfc15` the first `Sync` returns an error, and the second's swap fails
  with `store.ErrPrecondition` and diverges the mount. The two assertions on
  reads, before `c2`'s swap and in `c3`, were added on 2026-10-28
  (Assumption 29).
- **K5, a 409, and an untyped error**
  (`TestSwapRefusedWhileTheRootIsUnchangedRetriesAtTheNextCommit`;
  `CommitProtocol_SelfDiverge409`). Two subtests, one arming
  `store.ErrPrecondition` and one an untyped error, each on a fresh harness
  with one commit behind it. Commit `c2`: `Sync` returns a non-nil error,
  for which `errors.Is(err, store.ErrPrecondition)` holds in the first
  subtest. The harness recorded one `GetWithETag` of `rootKey` after the
  refused `PutIfMatch`. `mountWritable`; `rootKnown`; `doubtKey` `""`;
  `rootSnapshot` and `epoch` unchanged; `rootETag` equal to the MD5 of the
  root pointer's bytes on the inner store; `FS.dirty` true. A `Create` of
  `c3` returns nil. `Sync`: returns nil; its `PutIfMatch` passed through with
  that MD5 as its `etag` argument. A remount has `c2` and `c3`. At `edcfc15`
  the first subtest's mount diverges and the `Create` returns
  `vfs.ErrStale`; the second subtest's `Sync` after `c3` is not refused,
  since the first attempt's error left the ETag as it was and the store
  honours the retry, so at `edcfc15` the second subtest passes its `Sync`
  and fails only on the `GetWithETag` count (until 2026-10-28 this sentence
  began "is refused as a conflict too", which its own end contradicted;
  Assumption 29). Its purpose is the `rootKnown` and the single read.
- **K6, a real second writer**
  (`TestASwapLostToAnotherWriterDivergesAndNamesTheWriter`; the issue's
  second done-when; `CommitProtocol_TwoWriters`' loser, through
  `ProbeTheirs`). `New` on the harness, with a capturing logger, over an
  empty bucket; write a foreign root pointer with `Writer`
  `"other-host/4242"` on the inner store; commit `c2`: `Sync` returns a
  non-nil error whose text contains `commit conflict`. The harness recorded
  the `PutIfMatch` passed through and refused by the inner store, then one
  `GetWithETag` of `rootKey`, and no `Put`, `PutIfMatch` or `Delete` after
  it. `f.mode.State()` is `mountDiverged`; `rootInDoubt`; `doubtKey` is the
  key the refused attempt wrote. A `Create` of `c3` returns `vfs.ErrStale`.
  The captured records hold one at `slog.LevelError` whose message contains
  `commit conflict` and whose attribute `writer` has the string value
  `"other-host/4242"`, `their_epoch` the foreign root pointer's `Epoch`, and
  `their_snapshot` its `Snapshot`. A second `Sync` returns a non-nil error
  and the harness recorded no `Put`, `PutIfMatch` or `Delete` since the
  first (ADR 0014 §11, M7). At `edcfc15` the mount diverges without a read,
  and the record carries no `writer`.
- **K7, a failed read, settled at the next commit**
  (`TestInDoubtMountSettlesTheRootBeforeItsNextCommit`;
  `CommitProtocol_ProbeFailed`). One commit behind it; arm the lost reply and
  the failed read; commit `c2`: `Sync` returns a non-nil error whose text
  contains `in doubt`. `mountWritable`; `rootInDoubt`; `doubtKey` the key
  the applied `PutIfMatch` wrote; `epoch` and `rootETag` unchanged;
  `FS.dirty` true. A `Create` of `c3` returns nil: doubt does not gate
  changes. `Sync`: returns nil; the harness recorded, since the failed read,
  one `GetWithETag` of `rootKey` **before** any `Put` under
  `snapshotPrefix`, then one `Put` under `snapshotPrefix` whose key carries
  epoch 3, then one `PutIfMatch` of `rootKey` whose `etag` argument is the
  MD5 of the root pointer the lost reply wrote. `rootKnown`; `epoch` 3. A
  remount has `c2` and `c3`. At `edcfc15` the second `Sync` diverges the
  mount.
- **K8, a failed read, then a second writer**
  (`TestInDoubtMountDivergesWhenTheNextReadShowsAnotherWriter`). K7's
  setup through the failed read; then write a foreign root pointer on the
  inner store; `Create` `c3` returns nil; `Sync` returns a non-nil error
  whose text contains `commit conflict`; the harness recorded one
  `GetWithETag` of `rootKey` and **no** `Put` under `snapshotPrefix` and no
  `PutIfMatch` since the failed read: an in-doubt mount writes nothing until
  the doubt is settled. `mountDiverged`, and the captured `commit conflict`
  record names `"other-host/4242"`.
- **K9, a swap reply with no ETag** (`TestSwapReplyWithoutETagIsSettledByOneRead`;
  `CommitProtocol_SwapNoETag`, #103). One commit behind it; arm the dropped
  ETag; commit `c2`: `Sync` returns nil; the harness recorded one
  `GetWithETag` of `rootKey` after the `PutIfMatch`; `rootKnown`; `rootETag`
  the MD5 of the root pointer's bytes. Commit `c3`: `Sync` returns nil, its
  `PutIfMatch` passed through with that MD5 as its `etag` argument. A
  remount has `c2` and `c3`. At `edcfc15` the second `Sync` sends an empty
  `etag` and diverges the mount.
- **K10, a swap reply with no ETag and a failed read**
  (`TestSwapReplyWithoutETagAndAFailedReadRetriesTheCommit`). K9's setup
  with the failed read armed too: the first `Sync` returns a non-nil error
  whose text contains `no ETag`; `rootInDoubt`; `FS.dirty` true; the root
  pointer on the inner store names the attempt's key. `Sync` again, without
  a change: returns nil; the harness recorded a `GetWithETag` of `rootKey`
  before a `Put` under `snapshotPrefix` whose key carries epoch 3; a remount
  has `c2`.
- **K11, an opted-in fallback `Put` whose reply is lost**
  (`TestLostFallbackReplyIsAdopted`; `CommitProtocol_SelfDivergeFallback`).
  The harness in unconditional mode; `New` with `AllowUnconditionalRoot`
  true over an empty bucket (the init commit goes through the fallback); arm
  the lost root `Put`; commit `c2`: `Sync` returns nil; the harness recorded
  the `Put` of `rootKey` it applied, no `Head` of `rootKey` after it, and one
  `GetWithETag` of `rootKey`; `rootKnown`; `rootSnapshot` the key the `Put`
  named; `epoch` 2. Commit `c3`: `Sync` returns nil, through a `Put` and a
  `Head` of `rootKey`. A remount has `c2` and `c3`. At `edcfc15` the first
  `Sync` returns `write root pointer: ...`, `FS.epoch` stays 1 and no read
  is made; the second succeeds through the fallback, whose `Put` has no
  condition to fail, so this test diverges no mount there (until 2026-10-28
  this said the second diverges it, which is untrue in the harness's
  unconditional mode; Assumption 29).
- **K12, a 501 that took effect** (`TestA501ThatTookEffectIsAdopted`; ADR 0021
  §2 step 3; the issue's comment of 2026-10-27). Seed `c1` on the inner
  store; `New` on the harness, not in unconditional mode, without the
  opt-in; arm apply-then-501; commit `c2`: **`Sync` returns nil**; the
  harness recorded no `Delete`, one `Get` of `rootKey` (the reclaim's read)
  and then one `GetWithETag` of `rootKey` (the probe); the root pointer on
  the inner store names the key of the last `Put` under `snapshotPrefix`,
  which exists; `rootKnown`; `epoch` the loaded epoch plus one, which is 3
  when `c1` is seeded by a `New` on the inner store and one commit, since
  `New`'s create is epoch 1 (until 2026-10-28 this said 2; Assumption 29).
  Commit `c3` with the store honouring conditions: `Sync` returns nil, its
  `PutIfMatch` passed through. A remount has `c1`, `c2` and `c3`. At `edcfc15` the first `Sync` returns ADR 0021's
  error and the second diverges the mount. **G3** in
  `internal/blobfs/swap_fallback_test.go`
  (`TestRefusedCommitKeepsASnapshotTheRootPointerNames`) asserts the old
  `Sync` error and changes with it: its `Sync` now returns nil, and its
  other assertions, no `Delete`, the root pointer naming the attempt's key,
  which exists, and a remount with `c1` and `c2`, stand; its doc comment
  cites this ADR. Whether K12 is written as a new test or as G3's revision
  is test-author's choice; both files must agree.
- **K13, a lost reply on the create** (`TestLostReplyOnTheFirstCommitIsAdoptedByNew`).
  Over an empty bucket, arm the lost reply before `New`: `New` returns a
  non-nil `*FS` and a nil error; the bucket's root pointer names the one key
  under `snapshotPrefix`; `rootKnown`; `epoch` 1. In a second subtest, arm
  the lost reply and the failed read after the next swap, the harness's
  after-swap arm, since `New`'s load would spend a failure armed before it:
  `New` returns a nil `*FS` and a non-nil error (ADR 0011 §7's rule stands),
  and does not panic. At `edcfc15` the first subtest's `New` returns an
  error.

**Route tests added on review**, 2026-10-28, for the paths of §4 that no
test above reaches (Assumption 34). Each is in the same file, under the same
rules, and "one commit behind it" means `New` over an empty bucket on the
harness, as K5 to K10 have it, so the mount is at epoch 1 with the create
behind it and `c1`'s key is `rootSnapshot`.

- **K14, two failed reads in a row**
  (`TestInDoubtMountStaysInDoubtWhileItsReadsKeepFailing`; §4's
  top-of-commit `probeFailed`). K7's setup through the failed read, with a
  capturing logger; then arm the failed read again, with the plain arm,
  since `New` has returned and the next `GetWithETag` of `rootKey` is the
  top-of-commit probe; `Create` `c3` returns nil; `Sync` returns a non-nil
  error whose text contains `in doubt`. The harness recorded, since the
  first failed read, exactly one `GetWithETag` of `rootKey`, the one it
  failed, and **no** `Put` under `snapshotPrefix` and no `PutIfMatch` of
  `rootKey`: an in-doubt mount writes nothing until a read decides.
  `mountWritable`; `rootInDoubt`; `doubtKey` still the key the lost reply
  wrote; `epoch` 1 and `rootETag` as after `c1`; `FS.dirty` true. The
  captured records hold two at `slog.LevelError` whose message contains
  `in doubt`, one per failed read, each with the attribute `attempt` equal
  to that key and `err` equal to `rk5ErrInjected`. Then `Sync` once more,
  with no arm: returns nil; the harness recorded one `GetWithETag` of
  `rootKey` before one `Put` under `snapshotPrefix` whose key carries epoch
  3, then one `PutIfMatch` of `rootKey` whose `etag` argument is the MD5 of
  the root pointer the lost reply wrote; `rootKnown`; `epoch` 3; a remount
  has `c2` and `c3`. At `edcfc15` the second `Sync` diverges the mount.
- **K15, the root found unchanged at the top of a commit**
  (`TestInDoubtMountFindsTheRootUnchangedAndCommitsOverIt`; §4's
  top-of-commit `probeUnchanged`). One commit behind it, with a capturing
  logger; arm the untyped error on the next swap and the failed read after
  it; commit `c2`: `Sync` returns a non-nil error whose text contains
  `in doubt`; the harness recorded the `PutIfMatch` of `rootKey` it failed
  and then one `GetWithETag` of `rootKey` it failed; `mountWritable`;
  `rootInDoubt`; `doubtKey` the `Snapshot` of the root pointer the failed
  `PutIfMatch` carried, which the harness recorded, and which the root
  pointer on the inner store does not name; `epoch` 1 and `rootETag` as
  after `c1`; `FS.dirty` true. `Create` `c3` returns nil. `Sync`, with no
  arm: returns nil; the harness recorded, since the failed read, one
  `GetWithETag` of `rootKey` **before** any `Put` under `snapshotPrefix`,
  then one `Put` under `snapshotPrefix` whose key carries epoch 2, the
  epoch the failed attempt carried, under a key that is not the failed
  attempt's, then one `PutIfMatch` of `rootKey` whose `etag` argument is
  the MD5 of the root pointer's bytes on the inner store as they were
  before this commit, which is the ETag the mount held throughout.
  `rootKnown`; `doubtKey` `""`; `epoch` 2; `rootSnapshot` the key that
  `Put` wrote; `FS.dirty` false. The captured records hold one at
  `slog.LevelWarn` whose message contains `unchanged`, with `held_etag`
  and `etag` both equal to that MD5 and `snapshot` equal to `c1`'s key. A
  remount has `c2` and `c3`. At `edcfc15` the first `Sync` fails with the
  untyped error and no read, and the second succeeds with no read, so the
  test fails there on `in doubt` and on the read counts.
- **K16, an opted-in fallback whose `Head` withholds the ETag**
  (`TestFallbackHeadThatWithholdsTheETagIsSettledByOneRead`; §4's `Head`
  that fails or returns `""`). Two subtests, each on a fresh harness in
  unconditional mode, `New` with `AllowUnconditionalRoot` true over an empty
  bucket, as K11: the first arms the failed `Head`, the second the dropped
  `Head` ETag; commit `c2`: **`Sync` returns nil**; the harness recorded
  the `Put` of `rootKey` it applied, then the `Head` of `rootKey` answered
  as armed, then exactly one `GetWithETag` of `rootKey`, and no second
  `Put` of `rootKey`; `mountWritable`; `rootKnown`; `doubtKey` `""`;
  `rootSnapshot` the key the `Put` named; `epoch` 2; `rootETag` the MD5 of
  the root pointer's bytes on the inner store; `FS.dirty` false. Commit
  `c3`: `Sync` returns nil, through a `Put` and a `Head` of `rootKey` and
  no `GetWithETag` of `rootKey`. A remount has `c2` and `c3`. At `edcfc15`
  the first `Sync` returns nil with `FS.rootETag` `""` and makes no read,
  so the test fails there on the read count and on `rootETag`.
- **K17, that `Head`, and a failed read**
  (`TestFallbackHeadThatFailsAndAFailedReadRetriesTheCommit`). K16's first
  subtest with the failed read armed too, by the plain arm, since in
  unconditional mode no `PutIfMatch` passes through to arm it after: the
  first `Sync` returns a non-nil error whose text contains `no ETag` and
  `in doubt`; `rootInDoubt`; `doubtKey` the key the `Put` named; `epoch` 1;
  `FS.dirty` true; the root pointer on the inner store names that key.
  `Sync` again, without a change: returns nil; the harness recorded one
  `GetWithETag` of `rootKey` before a `Put` under `snapshotPrefix` whose
  key carries epoch 3, then a `Put` and a `Head` of `rootKey`; `rootKnown`;
  `epoch` 3; a remount has `c2`. At `edcfc15` the first `Sync` returns nil,
  with `FS.rootETag` `""` and no read, as K16's does there, and clears
  `FS.dirty`, so the test fails there on the first `Sync`'s result and on
  the read.
- **K18, a refused swap whose read fails**
  (`TestRefusedSwapWhoseReadFailsLeavesTheMountInDoubtNotDiverged`; §4's
  `store.ErrPrecondition` case, `probeFailed`). One commit behind it; arm
  `store.ErrPrecondition` on the next swap and the failed read after it;
  commit `c2`: `Sync` returns a non-nil error for which
  `errors.Is(err, store.ErrPrecondition)` holds and whose text contains
  `in doubt`; the harness recorded the refused `PutIfMatch` and then one
  `GetWithETag` of `rootKey` it failed. **`f.mode.State()` is
  `mountWritable`**: no swap's refusal alone diverges the mount (§1;
  Assumption 2). `rootInDoubt`; `doubtKey` the `Snapshot` the refused
  `PutIfMatch` carried; `epoch` 1 and `rootETag` as after `c1`; `FS.dirty`
  true. `Create` `c3` returns nil. `Sync`, with no arm: returns nil, as
  K15's second `Sync` does, the read showing the root pointer unchanged;
  the harness recorded one `GetWithETag` of `rootKey` before any `Put`
  under `snapshotPrefix`; `rootKnown`; `epoch` 2; a remount has `c2` and
  `c3`. At `edcfc15` the first `Sync` diverges the mount without a read,
  and the `Create` returns `vfs.ErrStale`.
- **K19, a root pointer deleted under a running mount**
  (`TestADeletedRootPointerDivergesTheMountAndSaysItIsGone`; §3 rule 2;
  Assumption 5; Assumption 28's second message; beyond the four paths the
  review named, Assumption 34). Two subtests, each with one commit behind
  it and a capturing logger, and `rootKey` deleted on the inner store
  before the commit. The first arms the next swap to fail with
  `store.ErrNotFound`, which is what a conditional write of a deleted
  object is answered on S3, a 404 (*Sources*, AWS); the second arms
  nothing, and the inner store refuses the swap with
  `store.ErrPrecondition`, as the fact above has it for a non-empty `etag`
  with no object. In both, commit `c2`: `Sync` returns a non-nil error
  whose text contains `commit conflict`; the harness recorded the
  `PutIfMatch` of `rootKey`, then one `GetWithETag` of `rootKey` passed
  through, whose recorded error is `store.ErrNotFound`, and no `Put`,
  `PutIfMatch` or `Delete` after it. `f.mode.State()` is `mountDiverged`;
  `rootInDoubt`; `doubtKey` the `Snapshot` the `PutIfMatch` carried. The
  captured records hold one at `slog.LevelError` whose message contains
  `commit conflict`, whose attributes `writer` and `their_snapshot` have
  the string value `""`, whose `their_epoch` is 0 and whose `attempted` is
  2. A `Create` of `c3` returns `vfs.ErrStale`. At `edcfc15` the first
  subtest's `Sync` returns an error that is not the conflict and leaves
  the mount `mountWritable`, retrying every interval; the second's
  diverges the mount on the refusal alone, without a read and without the
  three attributes.

**Changes to existing tests**, each the smallest that keeps the test's
purpose (Assumption 23):

- `internal/blobfs/mount_mode_test.go`, M3 and M7
  (`TestASwapLostToAnotherWriterDivergesTheMountAndItStoresNothingMore`):
  before the `Sync` that must diverge, write a foreign root pointer on the
  inner `*store.Local`, and let the swap pass through, or keep the armed
  `store.ErrPrecondition`; either way the probe finds another writer. Its
  assertions stand: `Sync` returns an error, `mountDiverged`, `vfs.ErrStale`
  on a `Write` and a `Create`, and no `Put`, `PutIfMatch` or `Delete` after
  the failed swap (the probe is a `GetWithETag`, which M7 does not count).
  Its doc comment, and the file's header, cite this ADR for where the mode's
  `Fire` is made, and so does its failure message for a mode that is not
  `mountDiverged` after that `Sync`: until 2026-10-28 it said that
  `commitLocked` fires `mountDiverge`, and it now says that `resolveRoot`
  does, on the read that shows another writer's root pointer, citing §2
  and §3 of this ADR beside ADR 0014 §11's M3 (Assumption 34).
- `internal/blobfs/mount_mode_test.go`, M8
  (`TestNewWhoseFirstSwapLosesFailsWithoutPanicking`): unchanged in its
  assertions, since an armed `store.ErrPrecondition` on an empty bucket is
  settled as *unchanged* and `New` still fails; its doc comment no longer
  says the commit fires `mountDiverge`.
- `internal/blobfs/diverged_race_test.go`, M4
  (`TestWriteRacingADivergingCommitIsNotADataRace`): `dr48Store`'s armed
  `PutIfMatch`, once released, first writes a foreign root pointer on the
  inner store and then returns `store.ErrPrecondition`, so that the probe
  that follows finds another writer and the `Fire` of `mountDiverge` the
  `Write` races still happens inside the held `Sync`. Its assertions stand.
- `internal/blobfs/prune_root_test.go`, R7
  (`TestPruneKeepsRootSnapshotAfterLostSwapReply`): after `armSwapLost`,
  `Sync` returns nil and the root pointer names the applied key, as before;
  the seeded key stands for a losing attempt sorting above; the second
  `Sync`, now on a clean namespace, returns nil and writes no key, or, if the
  test makes a change first, writes one at epoch 3 and succeeds; the pass
  still deletes nothing the root pointer names, and `pr88CheckEnd` stands.
  R8 (`TestPruneKeepsRootSnapshotAfterLostFallbackReply`): after
  `armRootPutLost`, `Sync` returns nil and the root pointer names the applied
  key; the retry it then starts, held at its root `Put`, needs a change made
  first, since the namespace is clean; the rest stands. Their doc comments
  say which step this ADR changed.
- `internal/blobfs/swap_fallback_test.go`, G3: as K12 says.
- Every other test stands. In particular ADR 0020's route test and ADR 0021's
  F1 diverge their mount by a real second mount, which the probe confirms,
  and count no `GetWithETag`; `TestNFSBackpressureDivergedDrain` and
  `TestBackpressureDivergedDrain` do the same; and `ciMethods` already lists
  `GetWithETag`.

**Safety rules**, as ADR 0011 §7, ADR 0012 §9, ADR 0020 §6 and ADR 0021 §6
give them: every `New` and every FS call runs in a goroutine that recovers a
panic and is waited for under a bound of 10 s; goroutines never touch `t`;
nothing is parallel; no sleep and no polling; no test calls `Run`; every
bucket is under `t.TempDir()`; the bucket is inspected through the inner
`*store.Local` after the harness's records have been read; an `*FS` that
`New` returned where it should have refused is never used.

**What only review can show:**

- that `commitLocked` makes no swap while `rootKnowledge` is `InDoubt`: the
  top-of-commit read comes before the snapshot `Put` on every path, and the
  four `Fire` sites are exactly those §4 names, with nothing else firing on
  `rootKnowledge` or on `f.mode`;
- that every outcome of a write of the root pointer other than a reply with a
  non-empty ETag raises doubt and reads, the opted-in fallback's `Put` and
  `Head` and ADR 0021's non-positive cases included, and that its positive
  cases raise none;
- that `classifyRoot` is a pure function of its arguments, and that
  `resolveRoot` applies each verdict exactly as §3 says, firing the
  machine before it writes the fields and the mode before it logs;
- that adoption at the failure site takes the success path whole, and at the
  top of a commit touches neither `f.dirty` nor the pass;
- that the records of §3 carry the attributes named, and that a failed `Fire`
  is logged with ADR 0014 §9's keys and leaves what §4 says;
- the `TLA+:` lines, the `trace.tsv` rows, and that each probe action with
  the member describes `resolveRoot` and `classifyRoot` (ADR 0013 §8);
- that `errorFor`, `store.Store`, `loadOrInit`'s refusal of an empty ETag,
  `mutable()` and `Access` are unchanged, and that `mutable()` still reads
  the mode alone.

### 9. Scope: the split, and `CHANGES`

**The third ask is split out.** A recovery path for a diverged mount that
needs no restart adds an edge out of `Diverged`, which widens what callers
may do with no caller causing it, so under ADR 0018 §5 its ADR must exempt it
or extend ADR 0014 §5's `Request`; it decides what happens to buffered
writes, open handles and inode numbers across a reload; it needs an operator
interface; and it touches the lifecycle ADR's `Closed` state. None of that is
needed to stop a transient failure from diverging a mount, which is this
ADR's defect (Assumption 18). The new issue's text and labels are in the
report that carries this ADR, and are repeated here so that the record is in
the tree:

> **A diverged mount can rejoin the bucket without a restart.** #5's third
> ask, split out by ADR 0022, which settles the first two. Today
> `mountDiverged` is terminal (ADR 0014 §7): once another writer has moved
> the root pointer, the mount answers every change `NFS3ERR_STALE` until it
> is remounted, and the changes it accepted since its last commit are lost
> with the process. Wanted: a recovery path that needs no restart. The mount
> re-reads the root pointer and the namespace it names, and either discards
> its uncommitted changes and rejoins as a fresh mount of that namespace, or
> reports which paths would be lost so the operator can decide. Done when:
> an operator action, whose form its ADR decides, makes a diverged mount
> reload the bucket's current namespace and resume committing against it;
> before discarding anything, the mount can report which paths hold
> uncommitted changes the reload would drop; handles held across the reload
> answer `NFS3ERR_STALE` where the inode they name is gone or changed, and
> nothing acknowledged on the new lineage is lost; the edge out of
> `Diverged` is decided under ADR 0018 §5 and specified in TLA+ first
> (ADR 0013). Depends on ADR 0022, the lifecycle ADR (#82, #92) and #43.
> Labels: `enhancement`, `design`, `ops`.

**`CHANGES`**, one line, not `BREAKING:` (Assumption 19), proposed for the
session to record:

> A commit whose swap of the root pointer fails without the store showing
> another mount's root pointer no longer marks the mount diverged: the mount
> reads the root pointer once and, if it names the attempt, counts the
> commit as done, if it is unchanged, retries at its next commit, and only
> if another mount moved or removed it diverges, logging that mount's writer
> id, epoch and snapshot; before, a lost reply, a 409 Conflict, an
> unconditional root write that took effect and returned an error, a swap
> reply without an ETag, or a 501 that took effect left the mount refusing
> every change as if a second writer had taken the bucket, until it was
> remounted.

On review, on 2026-10-28, two more lines, each a behaviour the review found
the line above omits and this ADR intends (Assumption 19):

> A root pointer deleted from the bucket under a running mount now marks
> the mount diverged at its next commit, with the log saying that the root
> pointer the mount descends from is gone; before, on S3, that commit
> failed and was retried every interval.

> A commit whose write of the root pointer the store applied without
> returning its ETag can now fail, when the read that would settle it also
> fails; the namespace stays dirty, and the next commit adopts the write
> and commits the namespace again.

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation**, under the owner's 2026-10-10 words, as
   ADRs 0015 to 0021 were. The design is mine: the issue and its comments ask
   for an in-doubt state, one read before the next swap, the three verdicts
   and a TLA+ spec first, and this ADR decides their form, the fourth
   verdict, the 412, #103 and every name.
2. **Every swap failure raises doubt, a 412 included** (§1, §4). The issue's
   comment named a lost reply and a 409; V2's `SwapUnknown` named
   `SwapReplyLost` and `SwapFailed`, leaving a 412 to diverge the mount at
   once, as ADR 0014 §7 pinned. One rule instead: no swap's error alone
   decides divergence. It names the writer for free, since the read that
   decides returns the body; it settles a 412 a proxy or a store answered
   while the condition held, which ADR 0020 left as a false divergence with
   the wrong cause named; and it leaves `commitLocked` with one path after a
   failed write instead of three. The costs: one `GetWithETag` on every
   failed swap, a real conflict included, under the locks #43 names; the
   tests that diverge a mount by a fake 412 against an unchanged root (M3,
   M4, M7) become tests with a real second writer, which is what they meant
   to test; and ADR 0014 §7's "its one writer" sentence moves, by
   cross-reference. A 412 whose read then fails leaves the mount accepting
   changes until its next commit, where today it diverges at once; that is
   the one interval's delay the rule costs, and only when the store is
   unreachable right after refusing the swap (Assumption 8).
3. **"Unchanged" is decided by the key the body names, and refreshes the
   ETag** (§3). The ETag the mount holds is what it would swap against; the
   key is what its namespace descends from. A body naming that key shows no
   other writer has committed since, whoever wrote it, so swapping against
   that body's ETag is sound: it is the condition ADR 0020 §1 asks for,
   taken from one response. Deciding by ETag instead would make a reply with
   no ETag (#103), a proxy's rewritten ETag, or a hand-rewritten root
   pointer naming the same snapshot look like another writer. The cost is
   that a proxy that rewrites ETags makes the mount fail every commit and
   log `unchanged` each time, instead of diverging once; the log says what
   is wrong, and #133's probe is the place to catch it.
4. **"Ours" is decided by the key alone, and adoption takes the body's
   epoch** (§3). The attempt's key carries six random bytes (ADR 0013
   Assumption 6), so a root pointer naming it is the one this mount wrote,
   and its `Epoch` is the attempt's; checking `rp.Writer` or the ETag would
   add nothing, since the mount never received the ETag. `doubtKey` is kept
   as one string rather than with an epoch of its own, because the key
   carries the epoch and the body repeats it.
5. **A body the mount does not understand decides nothing, and a missing
   root pointer with a known key is theirs** (§3). Divergence is terminal,
   so it needs a positive showing, as ADR 0021 Assumption 26 asks of the
   reclaim's `Delete`: `null`, `{}`, an error document served with 200 or a
   root pointer of another version leaves the root in doubt, the commit
   failing and the log saying what was read, rather than refusing every
   change on a body that may be a transient fault. A root pointer that is
   gone while the mount descends from one is not transient in that way:
   something deleted it, and nothing the mount can do is safe against that,
   so it diverges with `writer` empty and the log saying the root pointer is
   missing. A body that is ours or unchanged with no ETag is a failed read,
   so that the mount never holds `""` for a root pointer that exists
   (ADR 0020 Assumption 3).
6. **The read runs at the failure and, if still in doubt, before the next
   swap** (§3). At the failure, so that the common case resolves inside the
   call: a lost reply becomes a successful commit, and a real second writer
   diverges the mount in the same `Sync` as today, which keeps every
   existing test that diverges by a real writer. Before the next swap, so
   that a mount whose read failed never writes a snapshot or swaps while in
   doubt, which is the issue's comment's "before its next swap". Not on a
   timer, since nothing is lost by waiting for the next commit, and a timer
   would be a second writer of the fields under `FS.mu`. A clean in-doubt
   mount is unreachable, so `Sync`'s early return on a clean namespace needs
   no change.
7. **No in-line retry after "unchanged"** (§3). The issue's body asked for
   "refresh and retry, bounded"; the refresh is the ETag the read gives, and
   the retry is the next commit's. A retry inside `commitLocked` would hold
   the commit gate and `FS.mu` across a further `PutIfMatch` (#43), add a
   bound to decide and a model state to explore, and gain one interval of
   latency on a transient fault that the committer's interval already
   bounds; a client `COMMIT` that fails is retried by the client. If the
   interval proves too slow in practice, a later change can add one retry
   under this rule without touching the machine.
8. **Doubt does not gate changes, so the machine is not security-relevant**
   (§6). ADR 0018 §5 left the choice to this ADR. A mount is in doubt only
   while its store is unreachable or answers unclearly, which is when it
   already buffers changes and commits them later; refusing changes then
   would turn a transient store fault into client errors. A real second
   writer is confirmed by the read at the failure in every case but a store
   that refuses the swap and then fails the read, and even then by the next
   commit's read before anything is written. Reading `rootKnowledge` in
   `mutable()` would also make it an `fsm.Atomic` and would make `ProbeOurs`
   and `ProbeUnchanged` widening transitions with no caller, which
   ADR 0018 §5 says would need an exemption or a new `Request` form.
9. **Adoption at the failure site acknowledges the commit** (§3, §4). The
   snapshot the root pointer names is the one this call encoded under the
   same hold, so the namespace in memory is exactly what the bucket holds;
   clearing `f.dirty` and returning nil is what the success path does for a
   swap that answered. At the top of a later commit the namespace may have
   changed since the attempt, so adoption there advances the epoch and the
   key and leaves `f.dirty` to the commit that follows, which writes the
   namespace as it stands; the attempt's commit is never acknowledged to
   anyone then, since its `Sync` returned an error, and its changes reach
   the bucket again in the snapshot that follows. The model keeps the two
   apart by `pc`, and `NoLostAck` checks the acknowledgement.
10. **#103 is in scope, and a durable commit may still return an error**
    (§4, §5). A reply with no ETag withholds the same thing a lost reply
    does, and the one read supplies it at the cost of one `GET` only when a
    store omits the header, so closing it here costs nothing beyond the rule.
    When that read fails too, `commitLocked` returns an error for a commit
    the bucket holds, which ADR 0020 Assumption 3 called wrong for a refusal
    at the swap; here it is not a refusal: `f.dirty` stays true, the next
    commit adopts the attempt and writes the namespace again, and a client
    whose `COMMIT` failed does what its implementation does on a `COMMIT`
    error, which RFC 1813 §3.3.21 leaves to it: it specifies retransmission
    only for an unexpected verifier (*Sources*), and at worst a client
    resends writes the bucket already holds, which harms nothing. Alternatives
    were acknowledging with `""` kept, which is the defect, or acknowledging
    and leaving the root in doubt with a clean namespace, which would need a
    read outside `commitLocked` and a way to reach it.
11. **ADR 0021's non-positive cases raise doubt; its read stays a `Get`**
    (§4, §5). Its step 3 is the route the issue's comment of 2026-10-27
    named, and reading the root pointer again with `GetWithETag` is what the
    probe is; its step 4's read-failed and not-understood cases are the
    residual its Assumption 22 names, a 501 that took effect whose guard
    read did not show it, which today diverges the mount falsely at its
    next swap because the next attempt's key is not the one the root pointer
    names. Raising doubt there costs one further read, likely to fail at
    once when the first did, and settles the case at the next commit
    instead. The positive cases raise none, since the guard read already
    showed the root pointer naming another key, which is what *unchanged*
    would show again. The reclaim's own read stays a `Get` rather than
    becoming the probe: merging them would change what ADR 0021 decided for
    a not-found and for a body not understood, and G1 to G6 count that
    `Get`.
12. **The opted-in fallback's failures raise doubt** (§4). `S3.Put` is
    retried, so a `Put` that returns an error may have taken effect (the
    issue's comment of 2026-10-24; ADR 0013 Assumption 5), and a `Head` that
    fails or returns no ETag withholds the ETag as #103 does. On `Local`
    neither happens, so the demo and every test on a local bucket run as
    before.
13. **`errorFor` is unchanged** (*What this constrains*). The issue's comment
    named its mapping of 409 to `store.ErrPrecondition` as a route; under
    the rule a 409 and a 412 take the same path, so separating them would
    change nothing the mount does, only the text of an error the read then
    explains. `-check`'s stale-`If-Match` probe reads `store.ErrPrecondition`
    as a rejection, and a 409 there would become "an unexpected error", which
    is a judgement for #133's round, not this one.
14. **The names and the file** (§2, §3). `rootState` and `rootEvent` with
    the Go constants V2 reserved; the table `rootStates`, named after the
    field `rootKnowledge` as ADR 0014 §3 asks, as `mountModes` is named for
    `mode`; `rootSnapshot` beside `rootETag`, since the two describe one root
    pointer; `doubtKey`, which says what it is; `probeVerdict` with constants
    named after the four actions; `classifyRoot` and `resolveRoot`, the pure
    decision and the method that applies it, kept apart so that the
    decision is testable without an `FS` (K2) and the trace names each. The
    machine goes in `internal/blobfs/rootknowledge.go`, beside `mode.go`, so
    that coder, test-author and review agree on where it is; the verdict and
    the two functions go in `commit.go`, beside the one caller.
15. **The records, and `writerID`** (§3). Only the words `adopted`,
    `unchanged`, `commit conflict` and `in doubt`, and the `writer`,
    `their_epoch` and `their_snapshot` attributes of the conflict record,
    are pinned, so that K6 can show the writer named and the rest of each
    message is coder's. `writer` is the root pointer's `Writer`, the host
    name and process id of the mount that wrote it, which is what the bucket
    holds; two mounts in one process share it, and whether it should carry
    more, a random suffix or the mount's start time, is left open, since it
    would change every root pointer written and is not needed to tell a
    second process apart.
16. **The model** (§7). One member, `RootProbe`, since the fix is one rule
    and the code has it whole or not at all; a configuration per route the
    fix closes removes it, so that each route keeps a MustFail row, which is
    how the brief read ADR 0013 §7's "shown to be needed" for a fix that
    closes several routes. `"ProbeFailed"` is a fault because the read is a
    store call that can fail; `"SwapNoETag"` is a fault because #103 is a
    reply the store gives, and it is the only new fault that changes the
    model without the member, where it shows #103 as a self-divergence.
    `"Probe"` is a `pc` string rather than a phase of its own since the read
    is one store call. `rootSnapshot[m]` is needed because the verdict is by
    key (Assumption 3) and `rpSnapshot[m]` is cleared at `GetSnapshot`. The
    new MustHold configurations carry every member because each is new, and
    a configuration written after a fix describes the code with it; the
    flipped ones take only the flip, as ADR 0013 §7 allows them. Their
    bounds are the smallest that admit each route and one further commit
    after it: `MaxEpoch` 3 so that an adopted attempt is followed by a swap,
    `Nonces` `{1, 2}` so that a retry can take a fresh key, one crash where
    a restart matters. Tiers: `smoke` for the single-writer rows, `full` for
    `CommitProtocol_ProbeTwoWriters`, which explores two writers under seven
    faults; if any row runs too long, the session reports it and its bounds
    shrink in a later dispatch, as ADR 0019 Assumption 11 provided. The 501
    that took effect is left out of the model, as J4 takes a 501 to change
    nothing, and so is a fallback `Head` that fails; §7's Leaves-out list
    says Go's rule covers both, and K12 and K11 test them.
17. **The orphan after "unchanged" is not reclaimed** (§3). A swap that did
    not take effect leaves its snapshot as a failed swap does today, and
    ADR 0019 §3 prunes it once the root pointer passes it. ADR 0021 reclaims
    on its path because a store that answers every swap 501 would otherwise
    grow one orphan per interval for good; a transient fault is bounded by
    its own rate, and a store that fails every swap with a 5xx is a store
    that fails, which the committer's `Error` record every interval already
    reports. Reusing the uploaded snapshot on the retry is ADR 0021's open
    alternative, with the same cost it names.
18. **The split** (§9). The third ask is a feature with its own machine edge,
    operator interface and lifecycle interaction, none of which the defect
    needs; splitting keeps this ADR to the defect and its spec. The labels
    proposed follow CLAUDE.md's definitions: `enhancement` for something new,
    `design` for an architectural decision an ADR makes, `ops` for running it
    in production; not `correctness`, since nothing that exists is wrong by
    staying diverged, and not `bug`.
19. **Not `BREAKING:`, and three `CHANGES` lines** (§9; revised 2026-10-28).
    No client, flag, wire format or object format changes, and no deployment
    needs action: a mount that would have diverged keeps running, and one
    that should diverge still does, with more in its log. CLAUDE.md
    prefixes `BREAKING:` when a running deployment needs action to keep
    working, and none does. #103's closure is the same mechanism and the
    same line. On review, two behaviours the one line omits got lines of
    their own in §9, since each is observable and CLAUDE.md asks one line
    per change: a root pointer deleted under a running mount now diverges
    it, which §3's rule 2 and Assumption 5 decide, where `edcfc15` on S3
    failed and retried the commit, since a conditional write of a missing
    object is answered 404 there (*Sources*, AWS), `errorFor` makes that
    `store.ErrNotFound`, and `commitLocked` returned `swap root pointer: %w`
    for it; and a durable write of the root pointer whose read fails now
    returns an error, which Assumption 10 decides. Both are intended, and
    neither is `BREAKING:`: a deleted root pointer is an act against the
    bucket, not a deployment that keeps working, and the failed commit is
    retried by the next.
20. **Read, not run, at `edcfc15`**. Every statement about the code was
    checked by reading it there; nothing was run: not TLC, not `go test`. The
    issue's text is as the session gave it in the brief, which quoted the
    body and three comments; it was not read from the tracker. The flips,
    the eight new rows' outcomes and the routes in §7 are predictions the
    session's runs confirm or refute, and the architect records them in
    `manifest.tsv` (ADR 0013 §10). `Checked against` stays `faeba5b`, the
    commit the rest of the trace was checked at, with the new rows marked as
    awaiting coder's commit, as ADR 0014 Assumption 41 had it; once coder's
    change is on the branch, the architect bumps it and marks V2's entries,
    `RootProbe` and `ClassifyRoot` *exists* in `docs/vocabulary.md`. Done on
    2026-10-28, against `71fee34`, coder's commit: the line reads `71fee34`
    for every action, and `docs/vocabulary.md` marks V2's entries,
    `RootProbe`, `SwapUnknown`, the variables `doubtKey[m]` and
    `rootSnapshot[m]` and `ClassifyRoot` as existing as of that commit, with
    `Unresolved`, the `pc` string `"Probe"` and the faults `"ProbeFailed"`
    and `"SwapNoETag"`, which only the spec uses, still reserved, as `Below`
    is by ADR 0019. That check was by reading, as the rest of this ADR's
    were; the session's runs at `aee963f`, which it reported as `gofmt`,
    `go vet` and `go test -race ./...` clean, with every K-, M-, G- and
    R-test passing 30 repeats, are the session's evidence and not the
    architect's.
21. **One trace row per probe action and symbol** (§4, §7), as the swap's
    outcomes each have a row for `S3.PutIfMatch` and `errorFor`; the read
    that the four actions share is one call, and `trace.tsv` has one row per
    action and symbol by ADR 0013 §6. The rows exist before the code, so
    the tripwire fails on `FS.resolveRoot` and `classifyRoot` until coder's
    change is on the branch, which is the order ADR 0013 §8 sets, as
    ADR 0020 Assumption 12 and ADR 0021 Assumption 15 accepted.
22. **The read stays under the locks** (§5). `commitLocked` makes its store
    calls holding the commit gate and `FS.mu`, which #43 records as a defect
    for all of them, and ADR 0021 Assumption 25 kept the reclaim there for
    the same reason: one bounded call on a path that is already a failed
    commit's, moved with the rest when #43 is fixed. Reading outside the
    lock would need the verdict applied in a later hold against fields that
    could have moved, which is the shape ADR 0018 §5 warns of.
23. **The tests** (§8). `rk5Store` follows `sf91Store` and `pr88Store` in
    shape and naming, with the arms the routes need. K4 to K13 follow the
    configurations' routes; where a route's second attempt sorts above or
    below by nonce, the tests do not depend on it, since every assertion is
    on the key the harness recorded. K5's untyped subtest passes at
    `edcfc15` but for the read count, which is said so that a red run is
    read right. The tests are written in two parts because the machine's
    names do not exist at `edcfc15`, and one reference to them would stop
    the package's tests from building and hide the red run, as ADR 0020
    Assumption 14 found for `GetWithETag`. The existing tests that change are the five that diverge a
    mount by a refusal the store did not mean (M3, M4, M7) or observe a lost
    reply as an error (R7, R8, G3); each keeps its purpose, and its doc
    comment says what moved. The harness records reads of `rootKey` by both
    `Get` and `GetWithETag` so that K12 can tell the reclaim's read from the
    probe.
24. **Cross-references** (*What this constrains*). `docs/DESIGN.md` §5's
    sentence "A failed CAS means another mount advanced the filesystem" is
    corrected in place, since it states as a fact what this ADR shows is a
    reading of one answer; §16's table gains this ADR's row. ADRs 0013, 0014,
    0018, 0019, 0020 and 0021 each gain a `Revised` line, cross-reference
    only, where a sentence of theirs described the code or a test this ADR
    changes; none of their decisions moves. `README.md`'s sentence that a
    losing mount marks itself diverged stays true and is not touched.
25. **The top-of-commit error wraps nothing** (§4; revised 2026-10-28). §3
    pins `resolveRoot(ctx) probeVerdict`, so the read's error never reaches
    `commitLocked`, and §4's first text, which said the error "wraps the
    read's error", asked for what that signature cannot do, as coder found.
    Two ways out: widen the signature to return the error beside the
    verdict, or drop the wrap. The wrap is dropped. The read's error, where
    there is one, is in the `in doubt` record of §3, which every path logs,
    so the operator loses nothing; a read that decided nothing need not have
    returned an error at all, a body not understood or a reply with no ETag,
    so there is not always one to wrap; §4's failure-site errors wrap the
    write's error, not the read's, so the top-of-commit error now matches
    them; and nothing calls `errors.Is` on `Sync`'s error for the read's
    cause, since the committer logs it and the NFS `COMMIT` path maps it to
    a status. The cost is that the committer's `periodic commit failed`
    record names the doubt and not its cause, which the record logged one
    line earlier does. Coder kept the signature and returns an error whose
    text contains `in doubt` and wraps nothing, which is what §4 now says;
    no code and no test changes for this.
26. **`Diverge` leaves `commitLocked`'s `TLA+:` line** (§4; revised
    2026-10-28). §4 said the line "gains" six actions and did not say what it
    loses. `commitLocked` no longer fires on the mode; `trace.tsv` has one
    row per action and symbol (ADR 0013 §6), and its `Diverge` row names
    `resolveRoot` alone, as §4 asked; a line that kept `Diverge` would name
    an action the function does not perform and the trace does not give it.
    Coder removed it, which is confirmed.
27. **Two comments** (§2; revised 2026-10-28). The pinned doc comment for
    `rootKnowledge` named `loadOrInit` as a site. `loadOrInit` sets
    `rootSnapshot` and `rootETag` and neither reads nor fires the machine;
    `initEmpty`, which it calls, reaches the machine only through
    `commitLocked`. The text is corrected to name `commitLocked` and
    `resolveRoot`, the two sites, with the hold each runs under. Coder kept
    the pinned text, as it should have, so the correction is a coder change
    to `internal/blobfs/fs.go`. And `mode.go`'s comment on `mountDiverge`,
    "a swap failed with store.ErrPrecondition", which ADR 0014 §7 pinned, is
    untrue since §2 moved the `Fire` into `resolveRoot`; §2 gives the new
    text, which replaces that one comment of ADR 0014 §7's block under
    `docs/adr/README.md`'s rule that a later ADR may replace one part of an
    earlier decision and say which. ADR 0014's `Revised` line of 2026-10-27
    already records that the `Fire` moved; its pinned block is left as the
    record, and this ADR was not allowed to touch it in this pass.
28. **Coder's recorded choices, confirmed** (§3, §4; revised 2026-10-28).
    Six, each confirmed and pinned where a test might come to use it:
    (1) the conflict record's `attempted` is `f.epoch + 1`, which is the
    attempt's epoch on both paths, since `doubtKey` is written at that epoch
    and no field moves while the root is in doubt (§2); parsing the epoch
    out of `doubtKey` would add code for the same number. (2) A missing root
    pointer gets a second conflict message, that the root pointer the mount
    descends from is gone, which is what Assumption 5 asked the log to say;
    both messages contain `commit conflict`, which is all §3 and K6 pin.
    (3) The `unchanged` record carries `held_etag` and `etag`; §3 had named
    the values and not the keys. (4) The `in doubt` record for a body not
    understood carries `version`, `snapshot`, `etag` and `bytes`, not the
    body: such a body may be an error document or anything else the store
    served, its decoded fields and length say what was read, and ADR 0021's
    record of the same case has that shape; a bounded quotation of the body
    is the alternative, not taken, so that the two records agree. (5) A
    fallback `Head` that succeeds with an empty ETag raises doubt: that is
    §4's own text, "the `Head` fails or returns `""`", and not a choice.
    (6) One `errCommitConflict`, with `edcfc15`'s text, serves the top of a
    commit and the read after a write; §4 pins only that the text contains
    `commit conflict`.
29. **The tests, after two rounds** (§8; revised 2026-10-28). Three
    sentences of §8 were wrong against the tests the routes need, and are
    corrected: K12 said `epoch` 2, where seeding `c1` by a `New` on the
    inner store and one commit loads epoch 2, so `c2` is 3, and the test
    rightly asserts the loaded epoch plus one; K11 said the second `Sync`
    diverges the mount at `edcfc15`, where in the harness's unconditional
    mode the second commit goes through the fallback, whose `Put` has no
    condition to fail, and succeeds; K5 said the untyped subtest's second
    `Sync` "is refused as a conflict too" in a sentence whose own end says it
    passes, and the end is right. K13's second subtest needs an arm §8 did
    not list, since `New`'s load is a `GetWithETag` of `rootKey` and would
    spend a failure armed before `New`; the harness gains it. In round 2
    test-author moved K7, K8 and K10 to that arm too, giving ADR 0011 §7 as
    the reason; that rule, that a test does not rely on which reads `New`
    makes, is about `New`'s reads, and the three arm after `New` has
    returned, so it does not apply, as supervisor found. They keep the arm
    all the same, for a reason that holds: it names the read it fails, the
    first after the swap, so that a failure in those tests reads as what it
    is. What the move gave up, that a commit begun `Known` makes no read of
    `rootKey` before its swap, is a property of this ADR, §3's "never while
    `Known`", and is worth one assertion of its own: an implementation that
    read the root pointer before every swap would be a pre-check rather than
    a probe, at a read per commit; with `armFailRead` the three tests pinned
    it only by accident, and a breach would have failed them at a later
    assertion with a message about the swap. So K4 pins it, in both its
    commits, with a message that names it, and the three tests' doc
    comments and the file's header give the reason above in place of
    ADR 0011 §7.
30. **A write that applies after the probe's read was served is a residual,
    and gets an issue** (§3; revised 2026-10-28, on review). The review
    found a case nothing named. `NewS3` gives its `http.Client` a 30 s
    `Timeout` unless `S3Config.Timeout` sets one, and Go's `Client`
    documents that the timeout "includes connection time, any redirects,
    and reading the response body" and that on it "The Client cancels
    requests to the underlying Transport as if the Request's Context ended"
    (*Sources*): the cancellation is the client's, and a conditional `PUT`
    already sent may still reach the store and apply after `PutIfMatch` has
    returned its untyped error, which ADR 0013 §4, where a call takes
    effect between its call and its return, and the module's Leaves-out
    list, "Requests that take effect after they return, and timeouts", put
    outside the model. The route: a swap of `K2a` with `If-Match E(K1)`
    outlives the timeout, so the root is in doubt; the read shows `K1`
    unchanged, the mount takes `E(K1)` and is `Known`; the delayed `PUT`
    then applies, its condition still holding; the next swap, `K2b` with
    `If-Match E(K1)`, is answered 412; the read shows `K2a`, which is
    neither `doubtKey` nor `rootSnapshot`, so the verdict is *theirs*, and
    the mount diverges with no other writer. The window is the interval
    between the probe's read being served and the next swap: a `PUT` that
    applies before the read is served shows as *ours* and is adopted, and
    one that applies after the next swap fails its condition, since the
    swap moved the ETag. `NoLostAck` holds throughout: `K2a` was never
    acknowledged, the bucket holds it whole with its snapshot, and `K2b`
    was not acknowledged either; the cost is a false divergence until a
    remount, which is the defect's shape in a window the rule cannot see,
    since a read shows the root pointer only as it is when served. No code
    changes in this pass: the window needs the store, or the path to it,
    to hold a request past the client's timeout and apply it after a later
    read was served; the side taken is the safe one; and a fix needs a
    decision on what the mount remembers of attempts it has settled as
    *unchanged*, for how long, and how the model shows it, which is an
    ADR's. The operator can recognise it today only in part: the conflict
    record's `writer` is the body's `Writer`, which in this window is the
    mount's own `writerID`, the host name and process id, and
    `their_epoch` equals `attempted`; but nothing logs the mount's own
    `writerID`, so the comparison needs the host and the pid from outside
    the log, and two mounts of one process share the id (Assumption 15).
    Naming the mount's own writer id in the record, as `our_writer`, would
    put the signature on one line and be enough to recognise the window,
    though not to prove it; that is a code change and goes with the issue,
    drafted here so that the record is in the tree:

    > **A conditional root write that applies after the probe's read
    > diverges the mount with its own writer id named.** Residual of
    > ADR 0022 (Assumption 30). `S3.PutIfMatch` is one attempt under the
    > `http.Client`'s timeout, 30 s by default; a conditional `PUT` the
    > client gives up on can still apply at the store. If it applies after
    > ADR 0022's read of the root pointer has shown the root unchanged and
    > before the mount's next swap, that swap is answered 412, the read
    > after it shows the delayed attempt's key, which the mount no longer
    > holds, and the mount diverges with no other writer: the `commit
    > conflict` record names the mount's own writer id as `writer`, and
    > `their_epoch` equals `attempted`. Nothing acknowledged is lost; the
    > mount refuses every change until it is remounted. Wanted: first, the
    > record carries the mount's own writer id, as `our_writer`, so that an
    > operator sees the signature on one line; then a decision, by ADR and
    > in TLA+ first (ADR 0013), on whether the mount remembers the keys of
    > attempts it settled as *unchanged* and adopts one the root pointer
    > later names, on how long it remembers them, and on what a key it
    > remembers but did not write (two mounts of one process, ADR 0022
    > Assumption 15) must not be taken for. Done when: the conflict record
    > carries `our_writer`; the ADR decides the window, and
    > `CommitProtocol.tla`'s Leaves-out list either admits a request that
    > applies after it returns or says why it stays out; a test shows the
    > route with a harness that applies a swap after answering it. Depends
    > on ADR 0022. Labels: `bug`, `design`, `correctness`, `ops`.

    The labels follow CLAUDE.md: `bug`, since a mount that exists diverges
    wrongly; `design`, since the fix is an ADR's decision; `correctness`,
    since the condition is one the code reaches; `ops`, for the record the
    operator reads. The session checks them against the definitions.
31. **`CommitProtocol_SwapNoETag` and its twin** (§7; revised 2026-10-28,
    on review). §7 and Assumption 16 say a MustHold configuration written
    after its fix carries every member the code has;
    `CommitProtocol_SwapNoETag` carried five of the eight, lacking
    `PruneBelowCurrent`, `AtomicLoad` and `FallbackOptIn`, each inert under
    its constants: `Retention` 0 runs no pass, `Start` `"Loaded"` with
    `MaxCrashes` 0 reaches no load, and `Capability` `"Conditional"` answers
    no 501. The review found the drift. Two ways out: make §7 say the
    configuration carries the members its route reaches, or make the file
    carry every member, as `CommitProtocol_ProbeFailed`,
    `CommitProtocol_ProbeTwoWriters` and `CommitProtocol_PruneProbe` do,
    marking the inert ones in its comment as those do. The file changes, so
    that the four new MustHold configurations follow one rule, and its
    twin, `CommitProtocol_NoRootProbeNoETag`, gains the same three, since it
    is the parent without the member and nothing else. Each row's `exit`
    and `log` return to `-` until the session runs it (ADR 0013 §7, §10);
    the outcome expected of each is the one it had, since the members are
    inert there: `CommitProtocol_SwapNoETag` exit 0 with `Model checking
    completed. No error has been found.`, and
    `CommitProtocol_NoRootProbeNoETag` exit 12 with `Error: Invariant
    NoSelfDivergence is violated.`; the architect records what the runs
    show. On ADR 0013 §7's rule that the flip is the only change an
    existing configuration takes: the rule keeps the record of what was
    checked stable once a configuration is on `main` and calibrated; these
    two were written on this branch, were found short by the review before
    merge, and are finished rather than changed. If that reading is
    refused, the alternative is to leave both files as they were and have
    §7 record the three members as left out inert. The twin's invariants
    are not a drift: a MustFail configuration checks `TypeOK` and its one
    property and nothing else (ADR 0013 §7; `docs/tla/README.md`,
    *Configuration files*), so it cannot carry its parent's six; the other
    three MustFail twins of this ADR differ from their parents in the
    member alone because those parents are flipped configurations that
    kept two invariants, and this one's parent is not. Its comment said
    only that it removes the member; it now says this too, as §7 does.
32. **A failed `Fire(rootSwapUnknown)` moves no mode** (§4; revised
    2026-10-28, on review). §4's "Doubt" said the mount is left "as the old
    code left it", and at `edcfc15` the `store.ErrPrecondition` case also
    fired `mountDiverge`; §2 says only `resolveRoot` moves the mode, and
    the code follows §2, returning the conflict error with the mode
    `Writable`. §2 is right: moving the mode on a swap's error alone is the
    rule's one prohibition (§1), and a `Fire` that fails on an in-doubt
    machine is unreachable, since the top of the commit settles any doubt
    before the swap, so nothing is gained by diverging there. §4 now says
    the path moves no mode and lists what each outcome returns, nil for a
    reply with no ETag or a `Head` that withholds the ETag, where `edcfc15`
    took the success path with `""`. No code changes.
33. **`errorFor`'s probe rows** (§4; revised 2026-10-28, on review). §4
    gave `errorFor` the `ProbeFailed` row alone, for its untyped error,
    which is `classifyRoot`'s rule 3. Its mapping of a 404, `NoSuchKey` and
    `NoSuchBucket` to `store.ErrNotFound` decides rules 1 and 2,
    `probeUnchanged` with no `rootSnapshot` and `probeTheirs` with one, in
    the way its mapping of 412 and 409 to `store.ErrPrecondition` decides
    `SwapConflict`, for which the trace already names it. One row per
    action and symbol (ADR 0013 §6; Assumption 21) gives it
    `ProbeUnchanged` and `ProbeTheirs` rows too, `decided` by §3, and its
    `TLA+:` line gains the two names; `ProbeOurs` needs a body, which a
    non-2xx reply never carries, so it gains no row. The line is a
    comment, and the tripwire checks only that the symbol exists, so the
    trace rows land with this revision and the line follows by a coder
    change. The same reading would give `errorFor` rows under
    `RefuseEmpty` and `InitEmpty`, whose not-found at load it decides;
    those rows predate this ADR, name `loadOrInit` alone, and are not
    changed here.
34. **The paths no Go test reached, and M3's message** (§8; revised
    2026-10-28, on review). The review found four paths of §4 that no test
    reaches: the top-of-commit `probeFailed`, a second failed read in a
    row; the top-of-commit `probeUnchanged`; the opted-in fallback's `Head`
    that fails or returns `""`, for which the harness had no arm; and the
    `store.ErrPrecondition` case whose read fails. K14 to K18 cover them,
    one each and one more for the `Head` with a failed read, and the
    harness gains two `Head` arms, shaped as its `Head` passes through
    today, since a `Head` of `rootKey` is already recorded. K19 is mine
    beyond the four: §3's rule 2 and Assumption 28's second conflict
    message had no route test, and the behaviour is one `CHANGES` now
    records (§9), so it gets one; its first subtest arms
    `store.ErrNotFound` on the swap, which is what `errorFor` gives a
    conditional write of a deleted object on S3 (*Sources*, AWS), since
    `Local.PutIfMatch` answers `store.ErrPrecondition` there, and its
    second lets that answer through. The tests are specified from §3 and
    §4, not from the code: each names the records §3 pins, the calls the
    harness records and the fields §8 lets a test read. Every K-test
    before them stands. M3's failure message for the mode after the `Sync`
    still said `commitLocked` fires `mountDiverge`, which §8's entry for M3
    did not mention when it moved the test to a real second writer; the
    entry now does, and the message changes with it.

## Alternatives considered

- **Diverge at once on a 412, and read only after other failures**, V2's
  original shape and ADR 0014 §7's. It keeps M3, M4 and M7 as they are and
  saves one read on a real conflict, but it leaves a 412 answered while the
  condition held, by a proxy or a store, as a false second writer with the
  wrong cause named, and it cannot name the writer without a read anyway
  (Assumption 2).
- **Decide "unchanged" by the ETag** the read returns equalling the one held.
  Sound where it applies, but it leaves #103, a proxy's ETag and a root
  pointer rewritten by hand as "theirs" (Assumption 3).
- **Retry the swap in line after "unchanged"**, once or a bounded number of
  times (Assumption 7).
- **Refuse changes while in doubt**, by having `mutable()` read the machine.
  It turns a transient store fault into client errors and makes the machine
  security-relevant with two widening transitions no caller causes
  (Assumption 8).
- **Probe on a timer, or from the committer, outside `commitLocked`.** A
  second writer of the fields under `FS.mu`, with a verdict applied in a hold
  other than the one that read; the next commit is soon enough
  (Assumptions 6 and 22).
- **Separate 409 from 412 in `errorFor`** with a new sentinel. Nothing the
  mount does would differ (Assumption 13).
- **Keep #103 for its own ADR.** The mechanism is the same read, and leaving
  the empty ETag in place would keep a self-divergence the model now shows
  (`CommitProtocol_NoRootProbeNoETag`) (Assumption 10).
- **Treat a 501 that took effect as #5's without raising doubt**, as
  ADR 0021 left it: the mount diverges falsely at its next swap, which is the
  defect (Assumption 11).
- **Reclaim the orphan after "unchanged"**, as ADR 0021 does after a 501
  (Assumption 17).
- **Recover from divergence in this ADR** (Assumption 18).
- **Make `rootKnowledge` an `fsm.Atomic`.** Nothing reads it without
  `FS.mu` (§2, §6).
- **Widen `resolveRoot` to return the read's error** beside the verdict, so
  that the top-of-commit error could wrap it (Assumption 25).

## Consequences

- A lost reply, a 409, a swap reply without an ETag, an unconditional write
  that took effect and failed, and a 501 that took effect each end in a
  commit the bucket holds and the mount knows about, or in one failed commit
  and a retry, and never in a mount that refuses every change with no second
  writer. A real second writer still diverges the mount in the same `Sync`,
  and the log names it.
- Every failed write of the root pointer costs one read of it, under the
  locks #43 names, and a mount whose read fails stays in doubt, accepting
  changes and writing nothing, until a read succeeds.
- `FS` has one more machine and two more fields, `commitLocked` has one path
  after a failed write instead of three, and the mode's `Fire` moves into
  `resolveRoot`.
- Three configurations of #5 hold, four MustHold configurations widen the
  envelope the module's J11 records to lost replies, failed reads and a
  reply with no ETag, and four MustFail configurations keep the member shown
  needed; `manifest.tsv` has eleven rows for the session to calibrate.
- The tests that diverged a mount by a refusal the store did not mean now
  diverge it by a second writer, which is what they described.
- Hard to change later: that divergence needs a positive showing from a read
  of the root pointer, which the redesign's `fsinfo` swap (`docs/DESIGN.md`
  §5) inherits; the names in V2, once logs carry them; and that an in-doubt
  mount writes nothing to the bucket, which the lifecycle ADR's final commit
  must respect.

## What this does not decide

- Recovery from divergence without a restart: the issue split out in §9.
- An in-line retry of the swap after "unchanged" (Assumption 7).
- Whether `writerID` should tell two mounts of one process apart
  (Assumption 15).
- Whether the committer should go quiet after repeated identical failures,
  an in-doubt mount whose reads keep failing included (#102; ADR 0021
  Assumption 4).
- Reclaiming the orphan a swap that did not take effect leaves
  (Assumption 17), and reusing it on the retry (ADR 0021 *Alternatives*).
- Separating 409 from 412 in `errorFor`, and what `-check`'s stale-`If-Match`
  probe should make of a 409 (Assumption 13; #133's round).
- Moving `commitLocked`'s store calls, the read included, off `FS.mu` (#43).
- A store that applies a write it answered 501 *and* serves a stale root
  pointer to the reclaim's read: the reclaim's `Delete` still runs on the
  stale showing (ADR 0021 Assumption 22), and no read afterwards can undo
  it.
- A root pointer deleted by hand while a mount runs: the mount diverges, as
  §3 says, and #10 is where a repair tool would go.
- A conditional write of this mount's own that applies after the probe's
  read was served, so that the next swap's read shows it as another
  writer's and the mount diverges alone, naming its own writer id: a
  residual of the rule, with the issue Assumption 30 drafts.
- Multi-writer operation beyond detection (`docs/DESIGN.md` §15).

## Sources

Each was fetched on 2026-10-27, while this ADR was written, through a tool
that summarises what it fetches, reads a long page 100,000 characters at a
time, and quotes at most about 125 characters at once when asked for verbatim
text; where it split a sentence, the parts are joined here as it gave them.
Every source is cited as evidence for what a store or a protocol does, and
none directed any part of this design.

- AWS, *How to prevent object overwrites with conditional writes*:
  <https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html>,
  read whole. Under *Conditional write behavior*, for `If-Match`: "If the
  ETag doesn't match, the write operation fails with a `412 Precondition
  Failed` response." and "You can also receive a `409 Conflict` response in
  the case of concurrent requests."; for `If-None-Match`: "When using
  conditional writes with `PutObject`, uploads may be retried after
  receiving a `409 Conflict` error." Also, for `If-Match`: "You will receive
  a `404 Not Found` response if a concurrent delete request to an object
  succeeds before a conditional write operation on that object completes".
  (*Context*, route 2; §3 rule 2)
- AWS, *Amazon S3 API Reference*, `PutObject`:
  <https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html>. Of
  the `If-Match` request header: "If a conflicting operation occurs during
  the upload S3 returns a `409 ConditionalRequestConflict` response. On a
  409 failure you should fetch the object's ETag and retry the upload."; of
  `If-None-Match`: "On a 409 failure you should retry the upload."
  (*Context*, route 2; §1) **Honesty note:** the page is 69 KB; these lines
  were found by searching the saved text for `If-Match`, and the rest was
  not read.
- RFC 9110, *HTTP Semantics*: <https://www.rfc-editor.org/rfc/rfc9110.html>,
  read in parts from characters 180,000 and 380,000 of 564,890. §9.2.2:
  "Idempotent methods are distinguished because the request can be repeated
  automatically if a communication failure occurs before the client is able
  to read the server's response."; "For example, if a client sends a PUT
  request and the underlying connection is closed before any response is
  received, then the client can establish a new connection and retry the
  idempotent request."; "It knows that repeating the request will have the
  same intended effect, even if the original request succeeded, though the
  response might differ." §15.5.10: "The 409 (Conflict) status code
  indicates that the request could not be completed due to a conflict with
  the current state of the target resource. This code is used in situations
  where the user might be able to resolve the conflict and resubmit the
  request." §15.5.13: "The 412 (Precondition Failed) status code indicates
  that one or more conditions given in the request header fields evaluated
  to false when tested on the server". (*Context*, route 1 and route 2)
- RFC 1813, *NFS Version 3 Protocol Specification*:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>, read from character 160,000
  of about 230,000, which holds §3.3.21, `COMMIT`. Its DESCRIPTION:
  "Procedure COMMIT forces or flushes data to stable storage that was
  previously written with a WRITE procedure call with the stable field set
  to UNSTABLE." Its IMPLEMENTATION: "When a response comes back from either
  a WRITE or a COMMIT operation that contains an unexpected verf, the client
  will need to retransmit all of the buffers containing uncommitted cached
  data to the server." and "The timing of these retransmissions is left to
  the implementor." The tool found no sentence there about a `COMMIT` that
  returns an error status, so what a client does then is read as the
  client's (Assumption 10). **Honesty note:** only that part of the RFC was
  read for this ADR.
- Go, *package http*, `Client`: <https://pkg.go.dev/net/http#Client>,
  fetched on 2026-10-28, while Assumption 30 was written, through the same
  kind of tool, which read the page's first 100,000 of 234,634 characters,
  where the type's documentation is. Of `Timeout`: "Timeout specifies a
  time limit for requests made by this Client."; "The timeout includes
  connection time, any redirects, and reading the response body."; "A
  Timeout of zero means no timeout."; "The Client cancels requests to the
  underlying Transport as if the Request's Context ended." The tool found
  no sentence in the type's documentation about what the server does with
  a request the client cancelled, so that a cancelled `PUT` may still
  apply is read from RFC 9110 §9.2.2 above, not from this page.
  (Assumption 30) **Honesty note:** the rest of the page was not read.

Cited as earlier ADRs quote them, and not fetched again: that updates to a
single key are atomic and a listing begun after a successful write shows it
(ADR 0013 *Sources*); that an object's ETag reflects a change to its content
and that a `GET`'s ETag is the one `If-Match` compares against (ADR 0013 and
ADR 0020 *Sources*); and RFC 9110 §15.6.2 on 501 (ADR 0021 *Sources*).
