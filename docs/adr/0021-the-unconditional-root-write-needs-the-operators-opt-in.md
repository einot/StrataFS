# 0021. The unconditional root write needs the operator's opt-in

**Status:** Accepted
**Date:** 2026-10-25
**Revised:** 2026-10-25 — on review, before merge, three findings. §2's
`-check` rule makes a conditional-write probe required, without the flag,
whenever *that* probe is answered 501, not only the first, with every row's
name and detail as they were, so an endpoint that honours `If-None-Match` but
answers `If-Match` 501 fails `-check` as its mount fails to commit (reviewer;
Assumption 10). §2's refusal now reclaims the
snapshot the refused commit uploaded, after one read of the root pointer
confirms it names another key, so a store that answers every swap 501 does not
fill the bucket with orphans (security-auditor; Assumptions 21 to 24; the
module's R12, and a rerun of `CommitProtocol_Fallback`). `README.md`'s wording
of what the opt-in covers is the reviewer's (its second finding). §3, §4, §5
and §6 follow; §6 adds tests G1 to G5 and C4. The rule in §1, the flag, and
the rest of the Go change are unchanged.
**Revised:** 2026-10-27 — on re-review, before merge, three low findings. The
reclaim's guard (§2) now deletes only on a positive showing: a not-found, or
a decoded root pointer of the current format version whose `Snapshot` is
non-empty and differs from the attempt's key, so a root body that is JSON but
no root pointer no longer counts as naming another (security-auditor;
Assumption 26); its dependence on a read of `rootKey` showing the last write
is stated, with its cost, in Assumption 22. The reclaim stays under the locks
`commitLocked` holds, with #43 cited and the reclaim listed among the calls
#43's fix moves (reviewer; Assumption 25). §6's first review item is
reworded to match §2 (reviewer), and §6 adds test G6. No model change.
**Revised:** 2026-10-27 — cross-reference only, for ADR 0014 §7's conversion
of the mount mode: the `FS.diverged` that §2 and §5 name, and the
`FS.readOnly` that §5 names, are since that conversion the one field
`FS.mode`, whose `Diverged` and `ReadOnly` states they were. §2's "leaves
`FS.diverged` as they were" reads as "leaves the mode as it is, and fires
nothing", which ADR 0014 §7 now states and its M6 tests; §2's words stay,
since a test cites them, and a parenthesis at §2 and at §5 says this. The
rule, the flag, the Go change and the model are unchanged.
**Revised:** 2026-10-27 — cross-reference only: ADR 0022 decided #5, which
§2's step 3, §4, Assumption 22 and *What this does not decide* leave to it.
The reclaim's step 3, a guard read showing the root pointer naming the
attempt's key, and its step 4's read-failed and not-understood cases now
leave the root pointer in doubt and read it again, so a 501 that took effect
is adopted in the same call and `Sync` returns nil there, which changes G3's
assertion on that error as ADR 0022 §8 says; its positive cases, the
`Delete` and the error are unchanged, and the guard read stays a `Get`. An
opted-in fallback `Put` or `Head` that fails leaves the root in doubt too.
The rule, the flag, the model and the rest of the Go change are unchanged.
**Revised:** 2026-10-28 — cross-reference only, to make the line above
plain: §6's G3 says it "must stay green: it guards the reclaim". It stays
green, and it still guards the reclaim, with one assertion changed: since
ADR 0022 the `Sync` of its commit `c2` returns nil, not F3's error, because
the 501 that took effect is adopted; its other assertions, no `Delete`, the
root pointer naming the attempt's key, which exists, and the remount with
`c1` and `c2`, stand (ADR 0022 §8, K12). Nothing else moved.
**Issue:** #91

## Context

**The owner's decisions.** Three, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0020 were (Assumption 1).
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." This change is breaking for a deployment on a
   store without conditional writes (§3, Assumption 3), and relies on this.
3. On 2026-10-24, of the architect's reconciliation report, the owner said:
   "Go on with your recommendations. You're the project manager". That
   report's decision 15, on this defect, recommended: "fail closed. Without an
   explicit operator flag, a 501 on the swap is a failed commit. This is
   BREAKING for no-CAS backends. The alternative latches the first outcome
   for the mount." So failing closed behind an explicit operator opt-in is
   decided. The flag's name and meaning, what a refused swap does to the
   mount, and how `-check` and the flag interact, are this ADR's
   (Assumption 2).

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `247a276`, the commit this ADR's branch was cut from
(Assumption 19). Code is cited by symbol.

- `commitLocked`, in `internal/blobfs/commit.go`, writes the snapshot with
  `Put`, then swaps the root pointer with
  `PutIfMatch(ctx, rootKey, rpBody, f.rootETag)` and switches on the error.
  `store.ErrPrecondition` sets `FS.diverged`, logs `"commit conflict: another
  writer owns this bucket"` at `Error` and returns an error.
  `store.ErrUnsupported` logs `"backend lacks conditional writes; concurrent
  mounts are unsafe"` at `Warn`, writes the root pointer with `Put`, returning
  `write root pointer: %w` if that fails, and then reads its ETag back with
  `Head`, keeping the ETag only when the `Head` succeeds. Any other error is
  returned as `swap root pointer: %w`. On either success path it advances
  `FS.epoch`, stores the ETag as `FS.rootETag`, clears `FS.dirty`, logs
  `"committed"` at `Debug` and calls `schedulePrune(key)`. The decision to
  write unconditionally is made per call: nothing records that a 501 was
  seen, and nothing the operator sets bears on it.
- `errorFor`, in `internal/store/s3.go`, maps HTTP 501 to
  `store.ErrUnsupported` for any method, and is the only place the S3 store
  produces that error. `ReadOnly.Put`, `ReadOnly.Delete` and
  `ReadOnly.PutIfMatch`, in `internal/store/store.go`, return it too. `Local`
  never does. `S3.Put` is sent again by `S3.do` after a transport error, a
  5xx or a 429; `S3.PutIfMatch` is never retried.
- `New`, in `internal/blobfs/fs.go`, copies `Config` into the `FS`'s fields,
  puts a read-only mount's store behind `store.ReadOnly`, and calls
  `loadOrInit`, whose `initEmpty` commits through `commitLocked` before the
  FS is shared (ADR 0011 §1, §3). `mutable()` answers `vfs.ErrROFS` on a
  read-only mount and `vfs.ErrStale` once `FS.diverged` is set, and nil
  otherwise (ADR 0003 §4).
- `runCheck`, in `cmd/strata/check.go`, is
  `runCheck(ctx context.Context, s store.Store, out io.Writer) error`. Its
  `checkConditionalWrites` makes three probes, named `conditional create
  (If-None-Match)`, `conditional update (If-Match)` and `stale If-Match is
  REJECTED`; when the first conditional `PutIfMatch` answers
  `store.ErrUnsupported`, all three fail with `required` false and the
  detail `not supported by this endpoint`. The summary prints `This endpoint
  supports everything strata needs.` when nothing failed; `This endpoint is
  missing behaviour strata requires. It will not work correctly.` and
  returns an error when a required probe failed; and otherwise `This
  endpoint works, with the caveats noted above.`, `Without conditional
  writes strata cannot detect a second writer:`, `two mounts of the same
  bucket will silently overwrite each other.` and `Run exactly one mount per
  bucket.`, returning nil. The doc comment says the probes "are reported as
  optional, because strata falls back to an unconditional write". `run`, in
  `cmd/strata/main.go`, defines no flag about the fallback and calls
  `runCheck(context.Background(), bucket, os.Stdout)`.
- `README.md`, *Checking an endpoint before you trust it*, says "strata still
  runs there — it falls back to an unconditional write — but you must
  guarantee a single writer yourself", and `docs/DESIGN.md` §2's note on
  where this runs says the fallback gives up detection and "Such a
  deployment must be operated single-writer by policy, and should say so".
- One test reaches the fallback: `TestPruneKeepsRootSnapshotAfterLostFallbackReply`,
  R8 of ADR 0019 §6, in `internal/blobfs/prune_root_test.go`, starts `New`
  over an empty bucket on `pr88Store` in its unconditional mode, which
  answers every `PutIfMatch` with `store.ErrUnsupported`, and expects the
  init commit to go through the fallback. The tests in `internal/blobfs` and
  `internal/nfs` that mount through `store.ReadOnly` fail at the snapshot
  `Put`, before any swap, so none reaches it.

**The defect.** #91, "One HTTP 501 turns a root-pointer swap into a blind
overwrite": a single 501 on the swap, from an endpoint that has honoured every
conditional write and passed `-check`, makes `commitLocked` write the root
pointer unconditionally, and another writer's acknowledged commit can be
overwritten undetected; the `Head` can even hand this mount that writer's
ETag. `docs/tla/CommitProtocol_Fallback.cfg` shows it. TLC's counterexample at
`247a276`, from the session's run, has seven states and found 49 distinct: on a
backend that honours conditional writes but may answer any one of them 501
(`Capability` `"Flaky"`), with both mounts loaded at epoch 1, A runs `Mutate`,
`PutSnapshot` `[2,1]` and `SwapOK`, which acknowledges A's epoch-2 commit; B
runs `Mutate`, `PutSnapshot` `[2,2]` and `SwapUnsupported`, whose fallback
`Put` makes the root pointer name `[2,2]`, a snapshot that holds the loaded
commit and B's but not A's; `NoLostAck` fails. For context only,
`docs/tla/CommitProtocol_SelfDivergeFallback.cfg`, #5's, shows the fallback's
other defect in six states: a single writer's fallback `Put` takes effect and
fails (`FallbackReplyLost`), `commitLocked` keeps its old `rootETag`, and the
retry's swap fails its `If-Match`, so the mount diverges with no other writer.
That route is #5's to close, not this ADR's (§4).

**What a 501 says, and who says it.** RFC 9110 §15.6.2: "The 501 (Not
Implemented) status code indicates that the server does not support the
functionality required to fulfill the request." (*Sources*). It is an answer
about one request, and it does not say which part of the request the server
could not support. Of the stores strata is meant to run against:

- AWS S3 supports `If-Match` and `If-None-Match` on `PutObject`,
  `CompleteMultipartUpload` and `CopyObject`, answering a failed condition
  with 412 and a concurrent request with 409. It answers 501 Not Implemented
  to a `CopyObject` that carries those headers when a bucket policy enforces
  conditional writes (*Sources*): the same headers, honoured on one
  operation and answered 501 on another, from the same store.
- OpenStack Swift's S3 API raised `S3NotImplemented` for a `PUT` that carried
  `If-Match`, `If-Modified-Since`, `If-Unmodified-Since` or an `If-None-Match`
  other than `*`, and a 2024 change allowed `If-None-Match: *` while leaving
  the others refused (*Sources*). So on such a store `initEmpty`'s swap,
  which sends `If-None-Match: *`, is honoured, and every later swap, which
  sends `If-Match`, is answered 501: at `247a276` the first commit is
  conditional and every commit after it is a blind write.
- MinIO says it honours both headers on `PutObject`, answering a mismatch
  with 412 (*Sources*; a vendor blog, not a specification).
- Ceph's RADOS Gateway documents `if-match` and `if-none-match` as request
  headers of `GET Object` and lists neither for `PUT Object` (*Sources*).
  What it does with one on a `PUT` was not established.
- Dell ECS: Dell's own article says the AWS SDK's `withIfMatch()` and
  `withIfNoneMatch()` send `if-match` and `if-none-match` on the `PUT`
  request and recommends them for checking existence, without giving the
  status a failed condition returns (*Sources*); the target cluster passed
  all three of `-check`'s conditional-write probes (`README.md`, *Verified
  against*; `docs/DESIGN.md` §2).

So whether a store honours strata's conditional writes is a property of the
store and of the request's shape, which the operator can establish with
`-check` before mounting; a reply in the middle of one commit cannot establish
it, and a store that honours conditions can still answer one request 501, for
a reason the code cannot see. The code must therefore treat one 501 as one
failed call, and leave the decision to write unconditionally to the operator,
who alone can assert that no second writer exists.

**What the ADRs and documents say.** ADR 0013 §7 and §8 say how a fix lands:
spec first; the defect's configuration flips to MustHold with the fix added to
`Fixes`; a MustFail configuration removes the fix; and the counterexample
becomes a Go test that test-author writes from the trace and the spec. ADR 0013
Assumptions 5 and 6 note that the retried fallback `Put` sits "on a branch
where concurrent mounts are unsafe already". ADR 0019 §1 says "The rule's
guarantee on the fallback is a single writer's, as the fallback's own is" and
leaves two writers on the fallback to #91; ADR 0020 §2 leaves "the fallback's
policy" to it. `docs/vocabulary.md` V7 expects "writing the root
unconditionally" to be a decision named by its ADR. ADR 0018 §2, §3 and §5
bind a change that adds state, a decision about who may act, or a transition
(§5). The recon's decision 15 is in *The owner's decisions*.

**What this constrains.** `Config`, `FS`, `New` and `commitLocked` in
`internal/blobfs`, and the method this ADR adds; `run` and `runCheck` in
`cmd/strata`; `docs/tla/CommitProtocol.tla`, its configurations and the tables
beside it; `docs/vocabulary.md`; `docs/DESIGN.md` §2's note and §16; and
`README.md`. Nothing on the wire, no object format and no `store.Store` method
changes.

## Decision

### 1. The rule

A mount writes the root pointer with a condition, always, unless its operator
has said that exactly one mount writes the bucket. A store's reply never
changes that. A swap the store answers `store.ErrUnsupported` fails the
commit: nothing more is written, the mount stays writable, and its next commit
tries the swap again. With the operator's opt-in, `Config.AllowUnconditionalRoot`,
which `cmd/strata` sets from `-allow-unconditional-root`, a swap answered
`store.ErrUnsupported` is followed by the unconditional write and the `Head`,
as at `247a276`, and the mount's safety against a second writer is the
operator's assertion, not the store's.

**Why that closes #91.** The blind write existed only on the fallback, and the
fallback ran on a store's one reply. Without the opt-in there is no fallback:
every write of the root pointer carries a condition, so another mount's commit
can only be refused, never replaced (ADR 0020 §1, `docs/DESIGN.md` §5). With
the opt-in the operator has asserted that no other writer exists, so there is
no acknowledged commit of another mount to lose; what the opt-in gives up is
detection of a broken assertion, which `docs/DESIGN.md` §2 has always said of
the fallback and `CommitProtocol_UnconditionalTwoWriters` now shows (§4). A
reply cannot grant the opt-in, so one 501, or any number of them, never turns a
conditional writer into an unconditional one (the issue's first done-when).

### 2. The Go change

**`internal/blobfs`.** `Config` gains a field, pinned:

```go
// AllowUnconditionalRoot lets a commit write the root pointer without a
// condition when the store answers its compare-and-swap with
// store.ErrUnsupported (HTTP 501). Left false, such a commit fails and writes
// nothing more: the mount stays writable, and its next commit tries the swap
// again. An unconditional write cannot detect a second mount of the bucket and
// overwrites that mount's commits unseen, so set this only where exactly one
// mount writes the bucket (ADR 0021).
AllowUnconditionalRoot bool
```

`FS` gains `allowUnconditionalRoot bool`, which `New` sets from the field,
with no check and no store call, and which nothing writes afterwards, so it is
read without a lock. `FS` gains one method, in `internal/blobfs/commit.go`:

```go
// fallbackAllowed reports whether a commit whose swap the store answered
// store.ErrUnsupported may write the root pointer unconditionally: only when
// the operator set Config.AllowUnconditionalRoot. It is a function of the
// mount's configuration alone, and not a policy: no caller asks for the write
// (ADR 0021 §2, §5).
//
// TLA+: docs/tla/CommitProtocol.tla SwapUnsupported
func (f *FS) fallbackAllowed() bool
```

It returns `f.allowUnconditionalRoot`, takes no lock, makes no call and logs
nothing (Assumption 6).

**`commitLocked`.** Its `store.ErrPrecondition` case, its `case err != nil`
and its success path are unchanged. Its `store.ErrUnsupported` case becomes:

- **Without the opt-in** (`!f.fallbackAllowed()`): `commitLocked` writes the
  root pointer no further, in particular it makes no `Put` and no `Head` of
  `rootKey`; leaves `FS.epoch`, `FS.rootETag`, `FS.dirty`, `FS.lastCommit` and
  `FS.diverged` as they were (since ADR 0014 §7's conversion, `FS.mode`: the
  mode stays as it is, and no event fires); does not call `schedulePrune`;
  logs one record
  at `Error` whose message contains `conditional writes` (Assumption 8);
  **reclaims the snapshot** the commit uploaded before the swap, as the next
  paragraph says; and returns an error for which
  `errors.Is(err, store.ErrUnsupported)` holds and whose text contains
  `unconditional`, whatever the reclaim did, for example:

  ```text
  swap root pointer: the store does not support conditional writes, and this mount may not write the root pointer unconditionally: store: operation not supported by backend
  ```

  The rest of the text is not pinned.

  **The reclaim.** A refused commit has already uploaded a full snapshot
  under `key`, which no root pointer names, since a 501 changes no object
  (ADR 0013 Assumption 5; the module's J4 and R7). Left there it would never
  be pruned while the root pointer stands still, and a store that answers
  every swap 501 would gain one orphan per attempt, on every
  `-commit-interval` and on every client `COMMIT` (Assumption 21). So, after
  the record and before returning, `commitLocked`:
  1. reads the root pointer once, `f.store.Get(ctx, rootKey)`;
  2. calls `f.store.Delete(ctx, key)` once, only when the read positively
     shows a root pointer other than this attempt's: it returned
     `store.ErrNotFound`, or it returned a body that decodes as `rootPointer`
     with `Version == snapshotVersion`, `Snapshot != ""` and
     `Snapshot != key`. A failed `Delete` is logged at `Debug` and leaves the
     orphan, which ADR 0019 §3 and the module's J14 cover;
  3. if the body decodes with `Version == snapshotVersion` and `Snapshot ==
     key`, deletes nothing and logs one record at `Error` whose message
     contains `took effect`: the store applied a write it answered 501,
     outside every assumption here (Assumption 22). The mount is left as it
     is, and its next swap, made with the old ETag, fails its condition and
     diverges the mount with no other writer, which is the route #5 owns for
     a swap whose reply was lost;
  4. in every other case, deletes nothing and logs at `Debug`: the read
     failed otherwise; the body does not decode as JSON; or it decodes but
     its `Version` is not `snapshotVersion` or its `Snapshot` is empty, as
     `null`, `{}`, an error document served with 200, or a root pointer of
     another format version do (Assumption 26). Nothing is deleted against a
     root pointer the mount does not positively understand.

  The read and the `Delete` are made holding the locks `commitLocked` holds,
  as its other store calls are; #43 records those calls under `FS.mu` as a
  defect to fix for all of them, and when its fix moves them off the lock the
  reclaim moves with them (Assumption 25). The reclaim never writes
  `rootKey`, never deletes any key but `key`, and changes neither the error
  returned nor any field of the `FS`.
- **With the opt-in** (`f.fallbackAllowed()`): exactly as at `247a276`: the
  `Warn` record `"backend lacks conditional writes; concurrent mounts are
  unsafe"`, the `Put` of `rootKey`, returning `write root pointer: %w` on
  failure, the `Head` whose ETag is kept when it succeeds, and the success
  path.

`commitLocked`'s `TLA+:` line is unchanged: it still performs
`SwapUnsupported`, `FallbackPut` and `FallbackHead`, the last two only with
the opt-in. `New`'s `TLA+:` line is unchanged too. `fallbackAllowed` carries
the line above. No other `TLA+:` line in Go changes.

**`cmd/strata/main.go`.** `run` gains a flag, pinned:

```go
allowUnconditional = flag.Bool("allow-unconditional-root", false, "let a commit write the root pointer unconditionally when the store answers its conditional write with HTTP 501; a second mount of the bucket then goes undetected, so run exactly one")
```

Its value goes to `blobfs.Config` as `AllowUnconditionalRoot: *allowUnconditional`
and to `runCheck` as its last argument. Nothing checks it: it combines with
every other flag, `-read-only` and `-check` included (§3), and it is judged
nowhere, since a boolean has no value to refuse (Assumption 9).

**`cmd/strata/check.go`.** `runCheck`'s signature becomes

```go
func runCheck(ctx context.Context, s store.Store, out io.Writer, allowUnconditional bool) error
```

- The three conditional-write probes run in order, as at `247a276`: the
  create with `If-None-Match`, the update with `If-Match` and the current
  ETag, and the stale `If-Match`. Only severity changes: no row's name or
  detail changes from what it is at `247a276`. A probe whose `PutIfMatch`
  answers `store.ErrUnsupported` has `required` equal to
  `!allowUnconditional`, whichever of the three it is, and so does every
  probe after it, which is not run because of it. The details are today's:
  when the first probe is answered so, all three rows read `not supported by
  this endpoint`; when the second is, its row reads `failed with a correct
  ETag: ` followed by the error's text, which for `store.ErrUnsupported`
  ends `operation not supported by backend`, and the third row reads
  `skipped`; when the third is, its row reads `failed with an unexpected
  error: ` followed by the error's text. Every other outcome of the three
  probes keeps the severity and the text it has at `247a276`: a probe
  failing with another error, the probes skipped after it, and a stale
  `If-Match` that is accepted stay optional (Assumption 10). So an endpoint
  that honours
  `If-None-Match` but answers `If-Match` 501, which creates the filesystem
  and then commits nothing (§3), fails `-check` without the flag, as its
  mount fails to commit.
- The summary is unchanged except in one case: when `required` failures
  exist and a conditional-write result is among them, these lines follow
  the existing `This endpoint is missing behaviour strata requires. It will
  not work correctly.`:

  ```text
  Without conditional writes strata cannot detect a second writer, so it refuses
  to commit here rather than overwrite another mount's commits unseen. To run on
  this endpoint anyway, guarantee a single writer yourself and pass
  -allow-unconditional-root.
  ```

  `runCheck` then returns an error, as it does for any required failure.
  With `allowUnconditional` true the three results are optional, and the
  summary is the existing caveat text, unchanged, with a nil return.
- `checkConditionalWrites`'s doc comment no longer says the probes are
  optional "because strata falls back to an unconditional write"; it says
  they are required unless the operator allowed the unconditional write
  (§1). The helper's own signature is not pinned.

**What does not change.** `loadOrInit`, `initEmpty`, `Sync`, `schedulePrune`
and `pruneSnapshots`; `mutable()` and the mount's mode (§5); `store.Store`,
`errorFor`'s mapping of 501, `store.ReadOnly` and `Local`; the formats of the
root pointer and the snapshot; every other flag and every other probe of
`-check`; the printed mount instructions.

### 3. What the operator sees

- **A store that answers every conditional write 501, without the flag.**
  `strata -bucket s3://b ...` on a bucket with no filesystem exits with status
  1: `initEmpty`'s commit fails at its swap, `New` returns §2's error, and
  `main` prints `strata: ` and it; the snapshot the commit uploaded is
  reclaimed, so the bucket is left as it was. On a bucket that holds a
  filesystem, strata mounts and serves: loading writes nothing. Every commit
  then fails at its swap, so the committer logs `"periodic commit failed"`
  at `Error` every `-commit-interval` beside §2's record, nothing is ever
  committed, each attempt's snapshot is reclaimed so the bucket does not
  grow, and the final commit fails too, so strata exits with status 1 and
  `final commit: ...`, losing every change since the last commit the bucket
  holds, as it does when the store is down (Assumption 4). `-check` reports
  the three conditional-write probes as `FAIL`, prints §2's guidance, and
  exits with status 1.
- **A store that honours `If-None-Match` but answers `If-Match` 501, without
  the flag** (the OpenStack Swift shape in *Context*). On a bucket with no
  filesystem, `initEmpty`'s swap, `If-None-Match: *`, is honoured, so the
  filesystem is created and strata serves it; every later commit is refused
  as above, with its snapshot reclaimed, so the bucket holds the empty
  filesystem and nothing more. `-check` reports the create probe `ok`, the
  update probe `FAIL` with today's detail, `failed with a correct ETag: ` and
  the error's text, and the stale probe `FAIL` as `skipped`, prints §2's
  guidance, and exits with status 1 (§2; Assumption 10).
- **The same store, with `-allow-unconditional-root`.** Everything behaves
  as at `247a276`: the filesystem is created, commits go through the
  unconditional write with the `Warn` record each time, and `-check` exits 0
  with the caveats it printed before.
- **A store that honours conditional writes but answers one swap 501, without
  the flag.** That commit fails and is logged; the namespace stays dirty;
  the next commit, one `-commit-interval` later, tries the swap again, with
  the same ETag, and succeeds if the store honours it. No blind write
  happens, and a second mount that committed meanwhile is detected by the
  retry's `If-Match`, as `docs/DESIGN.md` §5 intends (§6, the route test).
- **`-read-only` with the flag.** Accepted; nothing changes. A read-only
  mount never commits (ADR 0011 §4), and its store is `store.ReadOnly`,
  whose `Put` refuses the unconditional write anyway (ADR 0011 §3), so the
  opt-in has nothing to allow (Assumption 11).
- **A local directory.** `Local.PutIfMatch` never answers
  `store.ErrUnsupported`, so neither the refusal nor the fallback is
  reached, with or without the flag.

### 4. The model

In `docs/tla/CommitProtocol.tla`, under two `Revised: 2026-10-25` lines, the
second for the reclaim:

- `Fixes` gains the member **`FallbackOptIn`** (`docs/vocabulary.md`, V3), and
  `AllFixes` lists it. The module gains the definition
  `FallbackAllowed == "FallbackOptIn" \notin Fixes`: the unconditional
  fallback runs after a 501.
- **The opt-in is modelled as the member's absence** (Assumption J17 of the
  module; Assumption 12 here). `Config.AllowUnconditionalRoot` makes
  `commitLocked` behave after a 501 exactly as it did at `247a276`, so a
  configuration without `FallbackOptIn` describes both a build without this
  fix and a build with it whose mounts all have the opt-in, and a
  configuration with the member describes a build with the fix whose mounts
  all lack it. A constant naming the opted-in mounts, as `ReadOnlyMounts`
  names the read-only ones, would have made every existing configuration
  assign it, and ADR 0013 §7 lets an existing configuration take no change
  but its flip. A bucket whose mounts differ in the opt-in is left out: it is
  a bucket where the operator's assertion is false, and the two-writer
  configurations without the member already show what that loses.
- `SwapUnsupported(m)` gains a branch: when the mount is not fenced and
  `~FallbackAllowed`, the call that answered 501 reached the bucket
  (`WriteCall`) and changed nothing, the attempt's key leaves `snapshots`,
  which is the reclaim's `Delete` (§2), and `commitLocked` returns an error
  (`CommitFails`), with no `FallbackPut`. The reclaim's read of the root
  pointer and its `Delete` are folded into the action by the direct argument
  ADR 0013 §4 allows, which the module's R12 gives: no other mount's write
  can make the root pointer name this mount's key, so the read's answer is
  fixed and the `Delete` always follows; of the steps another mount can take
  between the calls, only a `PruneList` that lists the key fails to commute
  with the `Delete`, and the two orders differ only in which garbage that
  pass dooms, which no property reads (Assumption 23). `used` keeps the key,
  since Go never reuses a nonce. Its three fallback branches, the `Put` not
  applied, applied, and applied with an error, now require
  `FallbackAllowed`. The fenced branch is unchanged: `store.ReadOnly` refuses
  the swap, and the `Put` with the opt-in, so nothing reaches the bucket
  either way. R7 is unchanged, and notes that the action ends at the 501
  without the fallback. The Leaves-out list names a 501 that took effect,
  which J4 excludes and §2's guard read exists for.
- **`CommitProtocol_Fallback` flips to MustHold**, `FallbackOptIn` added to
  its `Fixes`, its two invariants and every other line unchanged (ADR 0013
  §7). Its comment records the flip and the route TLC reported at `247a276`.
  The session's run of the flipped configuration found no violation (exit 0,
  96 distinct states); the reclaim changes the states a refused commit
  leaves, so the configuration is run again, and the prediction is that it
  still holds (Assumption 23).
- **`CommitProtocol_NoFallbackOptIn`**, MustFail on `NoLostAck`, is
  `CommitProtocol_Fallback` without the member: the configuration that
  removes the fix (ADR 0013 §7), and the one that TLC checked at `247a276`
  under the other name. It also reads, under the point above, as two writers
  with the opt-in on a backend that may answer one swap 501.
- **`CommitProtocol_UnconditionalTwoWriters`**, MustFail on `NoLostAck`: two
  writers that loaded one root pointer, `Capability` `"Unconditional"`, no
  fault, `MaxEpoch` 2, `Nonces` `{1, 2}`, no crash, without the member. It
  states what the opt-in gives up: each writer's fallback `Put` replaces the
  other's acknowledged commit undetected. With the member it would hold, since
  neither mount could commit at all, so it also removes the fix from a
  configuration on a backend that never honours a condition (Assumption 13).
  The route I expect: A runs `Mutate`, `PutSnapshot` `[2,1]`,
  `SwapUnsupported`, whose fallback `Put` moves the root pointer, and
  `FallbackHead`, which acknowledges A's epoch-2 commit; B runs `Mutate`,
  `PutSnapshot` `[2,2]` and `SwapUnsupported`, whose fallback `Put` makes the
  root pointer name `[2,2]`; `NoLostAck` fails. TLC may report A's
  `FallbackHead` after B's `Put` instead, at the same depth; the session's
  run decides.
- **The configurations that reach the fallback and keep it.**
  `CommitProtocol_UnconditionalSingle` (MustHold) and
  `CommitProtocol_PruneFallbackReply` (MustHold) have no member, and now read
  as a single writer with the opt-in; they are what the opt-in is shown safe
  for, one writer. `CommitProtocol_SelfDivergeFallback` (MustFail,
  `NoSelfDivergence`, #5) has no member either, and now reads as an opted-in
  mount whose fallback `Put` takes effect and fails. With the member and no
  opt-in that route is closed, because no fallback `Put` is made, not
  because #5 is fixed; the configuration therefore keeps the member out and
  stays MustFail, #5's to flip (Assumption 14). Their comments say so;
  their constants are unchanged, as ADR 0013 §7 requires and ADR 0020 §4
  read it.
- **No other configuration changes.** Only these four reach
  `SwapUnsupported` unfenced: every other configuration has `Capability`
  `"Conditional"`, and a fenced mount's `PutSnapshot` fails before any swap
  with `ChunksFirst`.
- In `docs/tla/manifest.tsv` the flipped row and the two new rows have `exit`
  and `log` `-` until the session has run them (ADR 0013 §7). In
  `docs/tla/trace.tsv`, `SwapUnsupported` gains a row for `FS.fallbackAllowed`,
  and the rows of `FS.commitLocked` for `SwapUnsupported`, `FallbackPut` and
  `FallbackHead`, and of `New` for `Init`, cite this section. The tripwire
  fails on the new row until coder's change is on the branch, which is the
  order ADR 0013 §8 sets (Assumption 15). The module's `Checked against` is
  `247a276`.

### 5. ADR 0018

- **No state the standing rule reaches is added or changed.**
  `FS.allowUnconditionalRoot` has one value for the mount's life, set by `New`
  from `Config` and written by nothing else: there is no legal next step that
  depends on where it is now, so it has no named states (ADR 0018 §2), as
  `FS.readOnly` has none until ADR 0014 §7 folds it into the mode's initial
  state (done with that section's conversion: the mode starts `ReadOnly`
  from `Config.ReadOnly`). The mount mode is untouched: a refused swap leaves
  the mount `Writable`, with `FS.diverged` unset (since the conversion,
  `FS.mode` left as it is, with no `Fire`), and no event is added to any
  machine (Assumption 16).
- **The decision is a configuration value, not a policy.** ADR 0018 §2
  reaches "a decision that allows or refuses a well-formed request". No
  caller's request asks for the root pointer to be written unconditionally:
  a `COMMIT`, a `Sync` or the committer's tick is answered the same way
  whichever branch is taken, and only the server's own store call differs.
  The branch is "a step the server takes on a store's answer", which §2
  keeps outside the rule, as ADR 0019 §5 read it for `prunable` and ADR 0020
  §5 for the refusal of an empty ETag. The operator who set the flag is a
  caller in §2's sense at another layer, but what the flag decides is read
  as `fallbackAllowed` reads it, a pure function of a flag, which ADR 0018
  Assumption 8 says the policy rule does not reach, as it does not reach the
  flag checks of E8. `docs/vocabulary.md` V7 asks that the decision be
  named, and `fallbackAllowed` and `FallbackAllowed` name it (Assumption 7).
- **Nothing is security-relevant under ADR 0018 §5**: no transition is added
  or changed, so none asks a policy. `-check`'s change is a report, and E9
  already exempts `runCheck` from the state rule. The reclaim (§2) is a step
  the server takes on a store's answer, as a prune pass is (ADR 0019 §5): no
  caller asks for the deletion, and it reads no caller's identity, so the
  policy rule does not reach it either.
- So nothing is converted and no exemption is granted;
  `docs/adr/exemptions.md` is unchanged (Assumption 16).

### 6. The test surface

Every name is spelled as in Go. test-author cannot read `internal/blobfs`'s
or `cmd/strata`'s non-test files, so everything a test may use is here.

**Names a test may use**, restated from the code at `247a276` unless marked
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
    AllowUnconditionalRoot bool  // new in this ADR (section 2)
    // ... fields this ADR does not use
}
func (f *FS) Sync(ctx context.Context) error
func (f *FS) Root() vfs.Handle
// Create, Lookup and the other vfs.FS methods, as internal/vfs/vfs.go declares them
func quietLog() *slog.Logger // internal/blobfs/fs_test.go: a logger that discards everything

// unexported
type FS struct {
    mu    sync.RWMutex
    dirty bool // the namespace has changes no commit has stored; written only under mu for writing
    // ...
}
func (f *FS) fallbackAllowed() bool // new in this ADR (section 2)
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
type snapshot struct {
    Version   int
    Epoch     uint64
    NextIno   uint64
    ChunkSize uint32
    Inodes    map[uint64]*inode
    CreatedNS int64
}
type inode struct {
    ID      uint64
    Entries map[string]uint64 // a directory's names to inode IDs; the root directory is Inodes[rootInodeID]
    // ...
}
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
type ReadOnly struct{ Store }               // implements Store; refuses Put, Delete and PutIfMatch with ErrUnsupported
```

```go
// cmd/strata (package main)
func runCheck(ctx context.Context, s store.Store, out io.Writer, allowUnconditional bool) error // new signature (section 2)
```

**Facts a test may rely on.**

- `New` on a `*store.Local` that holds no root pointer creates the filesystem
  with one `PutIfMatch` of `rootKey` whose `etag` argument is `""`, and no
  `Put` of `rootKey`; `New` on one that holds a filesystem makes no `Put`,
  `PutIfMatch` or `Delete` at all (ADR 0011 §1; ADR 0020 §2).
- A *commit* is a `Create` of a fresh name in the root directory, by
  `vfs.Caller{UID: 501, GID: 20}`, the root directory's owner when `Config`
  has `OwnerUID` 501 and `OwnerGID` 20, then `Sync`. A commit that succeeds
  writes one object under `snapshotPrefix` and then one `PutIfMatch` of
  `rootKey` whose `etag` argument is the ETag the mount last loaded or was
  returned; a commit whose swap fails leaves `FS.dirty` true, and the next
  `Sync` writes a new snapshot and swaps again with the same `etag` argument.
- On a `*store.Local`, an object's ETag is the lowercase hexadecimal MD5 of
  its body, the same from `Head`, from `GetWithETag` and as `PutIfMatch`
  returns it (ADR 0020 §6).
- Once a mount has diverged, which `Sync` reports by returning a non-nil
  error from a swap whose condition failed, every change on it, `Create`
  included, returns `vfs.ErrStale` (ADR 0003 §4). A mount that has not
  diverged accepts a `Create` of a fresh name.
- A test may read `FS.dirty` holding `FS.mu` for reading, and may set it as
  ADR 0011 §7 allows.
- The root pointer at `rootKey` decodes as `rootPointer`; the object at its
  `Snapshot` decodes with `decodeSnapshot`; the root directory's names are
  `Inodes[rootInodeID].Entries`.

**The harness.** `sf91Store`, in `internal/blobfs/swap_fallback_test.go`: a
`store.Store` over a `*store.Local` under `t.TempDir()`, written without
embedding `store.Store`, that passes every call through to the inner store
and, in addition:

- when armed, one-shot, answers the next `PutIfMatch` of `rootKey` with
  `store.ErrUnsupported` without calling the inner store: one 501 from a
  store that honours every other conditional write (`Capability` `"Flaky"`);
- in its **unconditional mode**, set before the first call and never
  changed, answers every `PutIfMatch` of any key with `store.ErrUnsupported`
  without calling the inner store (`Capability` `"Unconditional"`), as
  `pr88Store`'s mode does;
- records, in call order, every `PutIfMatch` of `rootKey`, with its `etag`
  argument and whether it was answered 501 or passed through; records every
  `Put` of `rootKey`, keeping each one's bytes, so that O2 can compute the
  ETag `Local` gave the body; counts every `Head` of `rootKey`; and counts
  every `Put`, `Delete` and `PutIfMatch` of any key;
- and, for the tests added on review: in its **create-only mode**, set
  before the first call, passes every `PutIfMatch` whose `etag` is `""`
  through and answers every other `PutIfMatch` with `store.ErrUnsupported`
  without calling the inner store (the Swift shape of *Context*); when
  armed, one-shot, **applies then answers 501**: passes the next
  `PutIfMatch` of `rootKey` through to the inner store and, if it succeeded,
  returns `""` and `store.ErrUnsupported` (a store that applies a write it
  answers 501); when armed, one-shot, **fails the next `Delete`** under
  `snapshotPrefix` with an error of the test's own, without calling the
  inner store; counts every `Get` of `rootKey`; records every `Delete` with
  its key, in call order; records the keys of every `Put` under
  `snapshotPrefix`, in call order; and, for G6, when armed, one-shot,
  **answers the next `Get` of `rootKey`** with bytes the test gives and no
  error, without calling the inner store.

It takes no hold: every route here is sequential, and the 501 is injected
synchronously. Two mounts in one test share one harness.

**Tests that compile at `247a276` and fail there** (the red run):

- **F1, the route test** (`TestOne501AfterHonouredSwapsMakesNoUnconditionalRootWrite`),
  from `CommitProtocol_Fallback`'s counterexample; the issue's second
  done-when. Every mount has `SnapshotRetention` −1 and `MaxDirtyBytes` −1.
  1. `New` of mount A on the harness over an empty bucket (A initialises
     epoch 1 with a conditional swap), then `New` of mount B on the harness
     (B loads epoch 1): the trace's `Start = "Loaded"`.
  2. Commit on A, name `a2`: `Sync` returns nil (A's `Mutate`, `PutSnapshot`
     `[2,1]`, `SwapOK`). A's commit is acknowledged.
  3. Arm the one-shot 501. Commit on B, name `b2` (B's `Mutate`,
     `PutSnapshot` `[2,2]`, `SwapUnsupported`). **`Sync` returns a non-nil
     error** for which `errors.Is(err, store.ErrUnsupported)` holds and whose
     text contains `unconditional`.
  4. The harness counted no `Put` of `rootKey` and no `Head` of `rootKey`,
     in the whole test so far. Its record of `PutIfMatch` of `rootKey` ends
     with B's, answered 501.
  5. B has not diverged: a `Create` of `b3` on B returns nil. B's `FS.dirty`
     is true.
  6. Through the inner `*store.Local`: `Get(rootKey)` decodes as
     `rootPointer`, `Get` of its `Snapshot` decodes with `decodeSnapshot`,
     and the root directory's `Entries` hold `a2` and not `b2`: A's
     acknowledged commit survives.
  7. `Sync` on B again, with the store honouring conditions: it returns a
     non-nil error, B's swap having failed its `If-Match` against the root
     pointer A moved (`SwapConflict`), and a `Create` of `b4` on B then
     returns `vfs.ErrStale`: the second writer is detected, as
     `docs/DESIGN.md` §5 intends. The harness still counted no `Put` and no
     `Head` of `rootKey`.
  8. A remount, `New` on the inner store with `SnapshotRetention` −1,
     succeeds, and `Lookup` of `a2` in its root directory succeeds while
     `Lookup` of `b2` fails.

  At `247a276` step 3 fails: B's `Sync` returns nil, the harness counted one
  `Put` and one `Head` of `rootKey`, and the root pointer's snapshot holds
  `b2` and not `a2`.
- **F2** (`TestNewOnAStoreWithoutConditionalWritesIsRefused`). The harness in
  unconditional mode over an empty bucket; `New` without the opt-in (no
  field is set, so the test compiles at `247a276`). `New` returns a nil
  `*FS` and a non-nil error for which `errors.Is(err, store.ErrUnsupported)`
  holds and whose text contains `unconditional`; the harness counted no `Put`
  of `rootKey`; the inner store answers `Get(rootKey)` with
  `store.ErrNotFound`. At `247a276` `New` returns an `*FS` and the bucket
  holds a root pointer. An `*FS` that `New` returned where it should have
  refused is never used (ADR 0011 §7).
- **F3** (`TestCommitOnAStoreWithoutConditionalWritesFailsWithoutOptIn`).
  Seed a filesystem on the inner `*store.Local` with `New` and one commit,
  name `c1`. Then `New` on the harness in unconditional mode, without the
  opt-in: it succeeds, since loading writes nothing. Record the bytes of
  `rootKey` on the inner store. Commit, name `c2`: `Sync` returns a non-nil
  error as F2's; the harness counted no `Put` and no `Head` of `rootKey`;
  the bytes of `rootKey` are unchanged; the mount has not diverged, so a
  `Create` of `c3` returns nil, and `FS.dirty` is true. `Sync` again: the
  same error, no `Put`, no `Head`, `rootKey` unchanged: a second reply
  decides nothing more than the first. A remount on the inner store has `c1`
  and neither `c2` nor `c3`. At `247a276` the first `Sync` returns nil
  through a `Put` and a `Head` of `rootKey`, and the remount has `c2`.

**Tests that need the new field or method**, which cannot compile at
`247a276` and are written once the code exists, in the same file unless said
otherwise:

- **O1** (`TestOptInRestoresTheUnconditionalFallback`). The harness in
  unconditional mode over an empty bucket; `New` with
  `AllowUnconditionalRoot` true succeeds, and the harness counted one `Put`
  and one `Head` of `rootKey`. A commit, name `c2`: `Sync` returns nil, the
  counts are two and two, and the root pointer on the inner store names a
  snapshot whose root directory holds `c2`. A remount has `c2`.
- **O2** (`TestOptInFallsBackAfterOne501`). One mount with
  `AllowUnconditionalRoot` true on the harness over an empty bucket
  (initialised conditionally). Arm the one-shot 501; commit `c2`: `Sync`
  returns nil, and the harness counted exactly one `Put` and one `Head` of
  `rootKey`. Commit `c3`, with the store honouring conditions: `Sync` returns
  nil, the counts are still one and one, and the last `PutIfMatch` of
  `rootKey` passed through with an `etag` argument equal to the lowercase
  hexadecimal MD5 of the root pointer body the `Put` wrote: the `Head`'s
  ETag was adopted.
- **O3** (`TestOptInOnAReadOnlyMountWritesNothing`). Seed a filesystem on
  the inner store. `New` on the harness with `ReadOnly` true and
  `AllowUnconditionalRoot` true succeeds; set `FS.dirty` true holding
  `FS.mu` for writing (ADR 0011 §7); `Sync`: the harness counted no `Put`,
  `Delete` or `PutIfMatch` of any key, and the bytes of `rootKey` are
  unchanged. What `Sync` returns is not pinned.
- **O4** (`TestFallbackAllowedFollowsConfig`). `New` on a plain
  `*store.Local` with `AllowUnconditionalRoot` false: `fallbackAllowed()` is
  false; with true: true.
- **O5, the R8 update.** `TestPruneKeepsRootSnapshotAfterLostFallbackReply`
  in `internal/blobfs/prune_root_test.go` gives its pruning mount
  `AllowUnconditionalRoot` true, by a change to `pr88Config` or `pr88Pruner`
  that leaves every other R-test's `Config` as it is, and its doc comment
  says the mount is opted in (this ADR) and that the trace's
  `SwapUnsupported` and `FallbackPut` are the opted-in fallback's. Nothing
  else in it changes. Without this change the test fails once the fix is in,
  since `New` over an empty bucket on a store that answers every swap 501 is
  refused (F2).
- **C1 to C3**, in `cmd/strata/check_test.go`, over a `store.Store` written
  there that passes every call through to a `*store.Local` under
  `t.TempDir()` and answers every `PutIfMatch` with `store.ErrUnsupported`
  without calling the inner store; `out` is a `bytes.Buffer`.
  - **C1** (`TestCheckRefusesAnEndpointWithoutConditionalWrites`):
    `runCheck(ctx, fake, &out, false)` returns a non-nil error, and the
    output contains `[FAIL] conditional create (If-None-Match)`,
    `[FAIL] conditional update (If-Match)`, `[FAIL] stale If-Match is
    REJECTED`, `It will not work correctly.` and `-allow-unconditional-root`;
    and, added on review, each of the three `[FAIL]` lines holds `not
    supported by this endpoint`, which pins that the rows keep today's text.
  - **C2** (`TestCheckAllowsAnEndpointWithoutConditionalWritesWithTheOptIn`):
    `runCheck(ctx, fake, &out, true)` returns nil, and the output contains
    the same three `[FAIL]` rows, `with the caveats noted above` and
    `Run exactly one mount per bucket.`.
  - **C3** (`TestCheckPassesAnEndpointWithConditionalWritesEitherWay`):
    `runCheck` on a plain `*store.Local`, with `false` and with `true`,
    returns nil, and the output contains
    `This endpoint supports everything strata needs.` and no `[FAIL]`.

  `-check` writes and deletes probe objects under `strata-check/`, so each
  case uses its own `*store.Local`.

**Tests added on review**, for the reclaim and the `-check` rule (the
Revised line), written against the branch at `53abeb7`, where the fix of §2
exists but neither change does. None of F1 to F3, O1 to O5, C2 and C3
changes its assertions: the reclaim makes a `Get` and a `Delete`, which no
existing assertion counts except O3's, on a read-only mount, which never
reaches the swap. C1 gains the one assertion its entry above names, that each
`[FAIL]` row reads `not supported by this endpoint`, since the `-check` rule
changes severity only. One failure message, `sf91CheckNoRootFallback`'s, which
says a refused swap "is followed by no further store call in that commit", is
reworded to say no further write of the root pointer.

- **G1** (`TestRefusedCommitReclaimsItsSnapshot`; red at `53abeb7`). F3's
  setup: seed `c1` on the inner store, then `New` on the harness in
  unconditional mode without the opt-in. Record the inner store's keys under
  `snapshotPrefix`. Commit `c2`: `Sync` returns F3's error; since the mount,
  the harness recorded exactly one `Put` under `snapshotPrefix`, under a key
  K, then exactly one `Get` of `rootKey`, then exactly one `Delete`, of K;
  the inner store's keys under `snapshotPrefix` are as recorded; `rootKey`'s
  bytes are unchanged. `Sync` again: one more `Put` under `snapshotPrefix`,
  under a new key K2, one more `Get` of `rootKey`, one more `Delete`, of K2;
  the keys are still as recorded. A remount has `c1` only. At `53abeb7` no
  `Get` and no `Delete` is made, and the listing grows by one key per
  `Sync`.
- **G2** (`TestRefusedInitLeavesTheBucketEmpty`; red at `53abeb7`). F2's
  setup. `New` returns a nil `*FS` and F2's error; the harness recorded one
  `Delete`, of the key of its one `Put` under `snapshotPrefix`; the inner
  store's `List("", "", 0)` returns no object. At `53abeb7` the snapshot is
  left behind and no `Delete` is made.
- **G3** (`TestRefusedCommitKeepsASnapshotTheRootPointerNames`; green at
  `53abeb7`, and it must stay green: it guards the reclaim). Seed `c1`;
  `New` on the harness, not in unconditional mode, without the opt-in; arm
  apply-then-501; commit `c2`: `Sync` returns F3's error; the harness
  recorded no `Delete`; the root pointer on the inner store decodes as
  `rootPointer` whose `Snapshot` is the key of the last `Put` under
  `snapshotPrefix`, and that object exists; a remount has `c1` and `c2`.
  What the mount does next is #5's, and is not asserted.
- **G4** (`TestRefusedCommitIgnoresAFailedReclaim`; red at `53abeb7` on the
  `Delete` count). G1's setup with fail-next-`Delete` armed before the
  commit. `Sync` returns F3's error, wrapping `store.ErrUnsupported` with
  `unconditional` in its text; the harness recorded one `Delete`, of K,
  which it failed; the inner store still holds K; `rootKey` is unchanged.
- **G5** (`TestStoreHonouringOnlyIfNoneMatchCreatesThenCommitsNothing`; red
  at `53abeb7` on the listing). The harness in create-only mode over an empty
  bucket, without the opt-in. `New` succeeds: the harness recorded one
  `PutIfMatch` of `rootKey` with `etag` `""` passed through, and no `Put` of
  `rootKey`. Record `rootKey`'s bytes and the keys under `snapshotPrefix`.
  Commit `c2`: `Sync` returns F3's error; no `Put` and no `Head` of
  `rootKey`; `rootKey` unchanged; the keys under `snapshotPrefix` as
  recorded; a `Create` of `c3` returns nil; a remount has neither `c2` nor
  `c3`. At `53abeb7` the refusal happens but the attempt's snapshot stays.
- **G6** (`TestRefusedCommitReclaimsOnlyAgainstAPositiveRootPointer`; added
  on re-review, against the branch at `c57ef58`). One subtest per body, each
  on a fresh harness: seed `c1` on the inner store, `New` on the harness in
  unconditional mode without the opt-in, record the inner store's keys under
  `snapshotPrefix`, arm the next `Get` of `rootKey` to answer the body, then
  commit `c2`. The bodies: `null`; `{}`; an error document served with 200,
  `{"Code":"InternalError","Message":"boom"}`; a document that is not JSON,
  `<Error/>`; and a root pointer of another format version,
  `{"version":99,"epoch":7,"snapshot":"snapshots/00000000000000000007-ffffffffffff.json.gz","fsid":1,"written":1}`.
  In every subtest `Sync` returns F3's error; the harness recorded exactly
  one `Get` of `rootKey` since the mount and no `Delete`; the inner store's
  keys under `snapshotPrefix` are the recorded ones plus exactly one, the
  attempt's, which it still holds; and `rootKey`'s bytes on the inner store
  are unchanged. At `c57ef58` the `null`, `{}`, error-document and
  other-version subtests are red, since the code deletes on any decoded
  body whose `Snapshot` is not `key`; the `<Error/>` subtest is green, and
  stays green. G1, G2 and G3 cover the positive cases: a root pointer of the
  current version naming another key, a not-found, and one naming the
  attempt's key.
- **C4** (`TestCheckRefusesAnEndpointHonouringOnlyIfNoneMatch`, in
  `cmd/strata/check_test.go`; red at `53abeb7`). A fake over a `*store.Local`
  under `t.TempDir()` that passes every `PutIfMatch` whose `etag` is `""`
  through and answers every other `PutIfMatch` with `store.ErrUnsupported`
  without calling the inner store. `runCheck(ctx, fake, &out, false)`
  returns a non-nil error, and the output contains
  `[ok  ] conditional create (If-None-Match)`, a line holding
  `[FAIL] conditional update (If-Match)`, `failed with a correct ETag` and
  `not supported`, a line holding both `[FAIL] stale If-Match is REJECTED`
  and `skipped`, `It will not work correctly.` and
  `-allow-unconditional-root`. `runCheck(ctx, fake, &out, true)` returns
  nil, and the output contains the same `ok` row, the same two `FAIL` rows,
  `with the caveats noted above` and `Run exactly one mount per bucket.`. At
  `53abeb7` the rows read the same, and the first call returns nil with
  `with the caveats noted above` in its output.

**Safety rules**, as ADR 0011 §7, ADR 0012 §9 and ADR 0020 §6 give them:

- every `New` and every FS call runs in a goroutine that recovers a panic and
  is waited for under a bound of 10 s; goroutines never touch `t`; nothing is
  parallel;
- every mount has `SnapshotRetention` −1 and `MaxDirtyBytes` −1, except O5's
  pruning mount, which keeps ADR 0019 §6's rules, and no `Write` is made, so
  no pass and no drain runs;
- no sleep, and no polling; no test calls `Run`, so no committer runs;
- every bucket is under `t.TempDir()`;
- the bucket is inspected through the inner `*store.Local` after the
  harness's records have been read, never through the harness;
- an `*FS` that `New` returned where it should have refused is never used;
  the case is reported failed instead.

**What only review can show:**

- that `commitLocked` writes the root pointer no further after a swap
  answered `store.ErrUnsupported` on a mount without the opt-in, no `Put` and
  no `Head` of `rootKey`, on every path, `initEmpty`'s included, and leaves
  the fields §2 names as they were;
- that the opt-in branch is the code of `247a276`, unchanged;
- that nothing writes `FS.allowUnconditionalRoot` after `New`, and nothing
  but `fallbackAllowed` reads it;
- that `run` passes the flag to both `Config` and `runCheck`, and the help
  text is §2's;
- that `-check`'s severity turns on `store.ErrUnsupported` from any of the
  three conditional-write probes, with the probes skipped after it, and on
  nothing else, and every other outcome keeps its severity;
- that the reclaim's `Delete` is made only after a `Get` of `rootKey`
  returned not-found, or a body that decodes as a root pointer of the
  current format version whose `Snapshot` is non-empty and differs from the
  key, only for the key the commit wrote, and that no path deletes a key the
  root pointer names, or deletes against a body the mount does not
  positively understand;
- that the reclaim changes no field of the `FS` and not the error returned;
- the `TLA+:` line on `fallbackAllowed`, the `trace.tsv` rows, and that
  `SwapUnsupported` with the member describes `commitLocked` (ADR 0013 §8);
- that `fallbackAllowed` is a pure function of the mount's configuration
  (§5).

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation**, under the owner's 2026-10-10 words, as
   ADRs 0015 to 0020 were, and under the owner's 2026-10-24 approval of the
   recommendation this ADR carries out (*Context*, decision 3).
2. **What the owner decided and what is mine.** The owner approved failing
   closed behind an explicit operator flag. The flag's name and help text,
   that the opt-in lives in `Config` and `FS`, that a refused swap leaves the
   mount writable and retries (§3), that `-check`'s severity follows the
   flag (§2), and every name here are mine.
3. **`BREAKING:`, and the two `CHANGES` entries.** A running deployment on a
   store that answers conditional writes 501 commits nothing until it is
   restarted with the flag, and a new one there cannot create a filesystem;
   that is action a deployment needs to keep working, so the entry is
   prefixed (CLAUDE.md). A deployment on a store that honours conditions
   needs nothing. `-check`'s changed exit status and report is a second,
   user-visible change, so it gets a line of its own, unprefixed: nothing a
   deployment runs depends on it.
4. **A refused swap leaves the mount writable and retrying** (§1, §3), rather
   than diverging it, making it read-only, or failing `New`. Divergence would
   name a second writer that does not exist and answer clients
   `vfs.ErrStale` for a condition that may be transient; a read-only turn
   would need a new mode edge and a way back; and `New` cannot tell a store
   that answers 501 from one that does not without a write probe, which
   ADR 0011 keeps out of a mount and `-check` already offers. A failed
   commit that is retried every interval is what the mount already does when
   the store is down, and the final commit's failure is reported the same
   way (ADR 0016 §3). The cost is that a store that never honours conditions
   makes the mount log two `Error` records per interval until the operator
   acts; whether the committer should go quiet after repeated identical
   failures is the question the issue filed for the architect's N15 asks of
   divergence, and is not decided here.
5. **The conditional attempt comes first even with the opt-in** (§1). The
   opt-in allows the fallback; it does not skip the swap. On a store that
   honours most conditions, the swap still detects a second writer most of
   the time, and the opted-in code is exactly the code of `247a276`, which
   is what lets the model read a configuration without the member two ways
   (§4).
6. **A method, not a field read** (§2). `fallbackAllowed` returns one field,
   which a reader may find thin. It gives the decision the name
   `docs/vocabulary.md` V7 asks for, under its rule 4, a doc comment that
   says why it is not a policy, a `trace.tsv` row, and a test (O4), as
   `prunable` did for ADR 0019 with more to compute.
7. **Not a policy, and no exemption** (§5). This is my reading of ADR 0018
   §2 and of its Assumption 8. If review reads the policy rule as reaching a
   decision the operator's configuration makes about the mount's own writes,
   the right answer is an exemption with E8's reason, a pure function of a
   flag, and a row in `docs/adr/exemptions.md`, not a `policy.Policy`:
   `policy.Request` has no form for a decision with no caller, and
   ADR 0018 §5 says such a decision is exempted or the request type grown.
8. **The record and the error** (§2). The error wraps `store.ErrUnsupported`
   so that a test, and `-check`'s reasoning, can tell the refusal from any
   other failed swap, and its text says `unconditional` so that the operator
   reading the committer's record sees what was refused and why. The record
   at `Error` doubles the committer's own `"periodic commit failed"` record,
   as the conflict record does today; the pair is existing practice, and
   ADR 0019 Assumption 9's reasoning on noise is left to the issue
   Assumption 4 names. The record's attributes are not pinned, so that coder
   may give it `our_epoch`, `attempted` and the store's name as the conflict
   record has.
9. **The flag's name and that nothing checks it** (§2).
   `-allow-unconditional-root` says exactly what strata will do with it,
   which is what an operator must consent to; the help text says when that
   is safe. `-single-writer` and `-assume-single-writer` would name the
   operator's promise instead, and read as though strata were otherwise
   multi-writer, which `README.md` says it is not. A boolean flag has no
   value to refuse, so unlike ADR 0008 to ADR 0010's flags it is judged
   nowhere, and it is not refused with `-read-only` (Assumption 11).
10. **`-check`'s severity turns on a 501 alone, from any probe** (§2). A
    probe is required without the flag when its own `PutIfMatch` answers
    `store.ErrUnsupported`, and so are the probes skipped after it, because
    that is the one outcome this ADR changes the mount's behaviour on:
    `-check`'s exit status then says what the mount will do with the same
    flags. The first version keyed the rule to the first probe alone, and
    review found the gap: an endpoint that honours `If-None-Match` but
    answers `If-Match` 501, the Swift shape of *Context*, passed `-check`
    while its mount created the filesystem and then committed nothing (§3).
    Only the severity changes: every row keeps the name and detail it has at
    `247a276`, so the report reads as before, the all-501 case's three `not
    supported by this endpoint` rows included, which the first version
    described and a draft of this revision would have changed to `skipped`
    without saying so (supervisor's finding). An endpoint that accepts a
    stale `If-Match` keeps its optional severity,
    since strata still commits there, with no detection, as before; whether
    `-check` should fail it without the flag is left open (*What this does
    not decide*). The guidance lines are mine; they name the flag so that the
    operator learns the remedy from the tool that found the problem.
11. **`-read-only` with the flag is accepted** (§3). It allows nothing a
    read-only mount could do, since `store.ReadOnly` refuses the `Put`
    (ADR 0011 §3), and refusing the combination would add a rule for no
    safety. O3 shows that nothing reaches the store.
12. **The opt-in as the member's absence** (§4). The alternative, a constant
    `UnconditionalMounts`, would be the faithful model of a per-mount field,
    and would have cost a line in every existing configuration, against
    ADR 0013 §7. The reading is exact because the opted-in code is the code
    of `247a276` (Assumption 5). Its cost is that a bucket with mounts both
    ways is not modelled, and the ADR says why that loses nothing.
13. **`CommitProtocol_UnconditionalTwoWriters`' bounds** (§4). The bounds of
    `CommitProtocol_Fallback` with `Capability` `"Unconditional"`: the
    smallest in which two opted-in writers can each complete a commit. Tier
    `smoke`. It is a MustFail of the kind ADR 0013 §7 allows, one that
    removes a fix, read also as a statement of what the opt-in gives up; it
    does not name an open defect, since two opted-in writers are the operator
    breaking an assertion, not a bug.
14. **#5's configuration is not touched** (§4). `CommitProtocol_SelfDivergeFallback`
    would hold with the member added, but for the wrong reason: the fallback
    `Put` that takes effect and fails would no longer be made, while an
    opted-in mount still diverges alone by that route. Adding the member
    would flip #5's configuration without fixing #5, so it stays as it is,
    with a comment that says how it now reads.
15. **The `fallbackAllowed` trace row goes in now** (§4), so the tripwire
    fails until coder's change is on the branch, as ADR 0020 Assumption 12
    accepted for `GetWithETag`: the trace describes the code this branch
    lands, spec first.
16. **No state, no transition** (§5). The field is a value; the mode does not
    move. If a later ADR makes a refused swap change the mode, that
    transition is its to judge under ADR 0018 §5.
17. **The flaky store's cost** (§3). One 501 now costs one commit's delay, one
    interval, and two `Error` records, where it cost a blind write. A store
    that answers 501 often enough to matter is one the operator should run
    `-check` against and then decide about, with the flag or another store.
18. **`Local` is never affected** (§3), since `Local.PutIfMatch` never returns
    `store.ErrUnsupported`; the demo and every test on a local bucket run as
    before.
19. **Read, not run.** Every statement about the code was checked by reading
    it at `247a276`; nothing was run: not TLC, not `go test`. The flip, the
    two new rows' outcomes and the route in §4 are predictions the session's
    runs confirm or refute. `Checked against` is `247a276` because that is
    the code the trace was checked against; once coder's change is on the
    branch, the architect bumps it to that commit and marks `FallbackOptIn`
    and `FallbackAllowed` *exists* in `docs/vocabulary.md`, as was done for
    ADR 0019 and ADR 0020.
20. **What the fetched sources establish** (*Context*). The RFC's definition
    of 501, AWS's 501 for `CopyObject`, Swift's `S3NotImplemented` and Ceph's
    header table were read from primary sources; MinIO's support is its own
    blog's claim; ECS's support is Dell's article plus `-check`'s run on the
    target cluster. AWS's error-code reference, which gives `NotImplemented`
    its description, could not be fetched (*Sources*), so this ADR does not
    quote it. None of this decides the design: the decision rests on 501
    being an answer about one request, which the RFC gives, and on the
    operator being the only party who can assert a single writer.
21. **The refused commit reclaims its snapshot** (§2). Security review found
    that without it a store answering every swap 501 gains an orphan per
    attempt, on every interval and on every client `COMMIT`, which checks no
    credential, and that ADR 0019's passes never reclaim them while the root
    pointer stands still. The first version said ADR 0019 §3 covers the
    orphan; it does so only once the root pointer passes it, which here
    happens only after the operator acts, and then two commits later. A
    `Delete` of a key no root pointer names is what ADR 0019 §1 already
    calls safe. The alternatives, reusing the uploaded snapshot on the retry
    and backing the committer off, are in *Alternatives considered*.
22. **A read guards the `Delete`** (§2). The spec assumes a 501 applies
    nothing (ADR 0013 Assumption 5; the module's J4), and the refusal already
    rests on it: a 501 that took effect would make the mount diverge alone
    at its next swap, #5's shape. A `Delete` on the strength of that
    assumption alone would turn that bad outcome into a worse one, a root
    pointer naming a deleted snapshot and a bucket no mount can load, which
    is the failure ADR 0019 exists to prevent; one `Get` of the root pointer
    rules it out by observation, at one read per refused commit, on a path
    where latency no longer matters. The read's failure leaves the orphan,
    which costs space and nothing else. The took-effect case itself is left
    to #5: the guard logs it at `Error` and changes no mount state, so that
    #5's in-doubt resolution, not this ADR, decides whether to adopt the
    root. **What the guard rests on**, stated on re-review: that a `Get` of
    `rootKey` returns the root pointer as last written, which ADR 0013
    Assumption 5 already assumes of every store call (AWS documents strong
    read-after-write consistency; the model takes every key as
    linearizable; `Local`'s `Put` is a rename). A store that both applies a
    write it answered 501 and then serves a stale root pointer, from a
    replica or before the write lands, defeats the guard: the read shows the
    previous root, the attempt's snapshot is deleted, and the root pointer
    names a snapshot the bucket no longer holds. A fresh `New` then fails at
    `read snapshot ...: not found`, and on a bucket whose mounts all lack
    the opt-in no commit lands on its own, for a reason that differs by
    case. If the store goes on answering 501, every swap is refused and each
    mount stays writable and dirty, so nothing lands, unless the store
    applies a later 501'd write of some such mount's and answers that
    attempt's guard read fresh, so that its snapshot is kept and the root
    pointer names it: chance. If the store has started honouring
    conditions, every mount loaded before holds the previous ETag, which the
    applied write replaced, so its next swap fails its condition and it
    diverges. A mount with the opt-in is the exception: its fallback `Put`
    lands, and the root pointer again names a snapshot the bucket holds; a
    bucket whose mounts differ in the opt-in is otherwise left open
    (Assumption 12). Short of that, the repair is by hand: a root pointer,
    written with an S3 client in the format `rootPointer` gives, that names
    the snapshot the previous root pointer named. Only that target drops
    nothing acknowledged: what it drops, the refused attempt's changes, was
    never acknowledged, since `Sync` returned an error. No other key is safe
    to pick by its position. The newest key below the deleted one may be a
    refused attempt whose guard read failed, kept at the same epoch under
    another nonce, or a losing writer's attempt at the previous epoch, which
    ADR 0019 §3 keeps while it sorts above the root; either lacks
    acknowledged changes. Strata keeps no record that identifies the target:
    the root pointer object that named it has been overwritten, and no rule
    pinned in these ADRs identifies it safely from what the bucket or a
    mount holds. Finding it reliably is #10's, and strata has no tool for
    any of this. On `initEmpty`'s
    first commit the same route runs through the not-found case, a stale 404
    after the 501'd create took effect, and the repair is to delete the root
    pointer, since there is no earlier snapshot and nothing was
    acknowledged. Before the reclaim the same two faults left an orphan and
    a false divergence. The rule the auditor offered as
    the other remedy, deleting only when the root pointer the read names
    sorts below the attempt's key, does not close this: a stale read names
    the previous root, which sorts below the attempt's key, so it passes that
    rule too, and no read can tell "nothing happened" from "applied, read
    stale". Only a delete conditioned on another key's content would, and
    none exists (ADR 0019 *Alternatives*). Two faults the spec assumes away,
    at once, is the accepted price; no test can show it without a store that
    lies, so none is written.
23. **The model folds the reclaim into `SwapUnsupported`** (§4), by the
    direct argument of ADR 0013 §4 that R12 gives, rather than adding a pc
    string and an action for the `Delete`. The only step that fails to
    commute with it is another mount's `PruneList`, and the two orders differ
    only in which garbage that pass dooms. The guard read is not modelled at
    all, since its answer is fixed in the model; the Leaves-out list says
    so. `CommitProtocol_Fallback` is the one configuration that takes the
    branch, and its rerun is the check of this reading; I predict that it
    still holds.
24. **What the reclaim does not bound** (§2, §3). Each refused commit still
    uploads a full snapshot and makes two more calls, holding the commit
    gate and `FS.mu` across them, and a client `COMMIT` on a dirty namespace
    can cause one on demand; so a store that refuses every swap costs
    bandwidth and latency per attempt, as a client that changes a file and
    `COMMIT`s in a loop already costs today. The bucket no longer grows, and
    `-check` and the `Error` records point at the misconfiguration. Not
    uploading again while the namespace is unchanged would remove the
    bandwidth too, and is left open (*Alternatives considered*).
25. **The reclaim stays under the locks** (§2). `commitLocked` makes its
    store calls holding the commit gate and `FS.mu`, which #43 records as a
    defect, and the repository's guidance to coder says that pattern is not
    a precedent. The reclaim is kept there for this PR all the same: the
    path is a refused commit's, the two calls are bounded like the swap and
    the snapshot `Put` beside them, and the reviewer judged the cost
    acceptable. When #43's fix moves `commitLocked`'s store calls off the
    lock, the reclaim is among them, with the snapshot `Put`, the swap, and
    the opted-in fallback's `Put` and `Head`. The reviewer's alternative,
    having `commitLocked` return the key for `Sync` to reclaim after it
    releases `FS.mu`, under the commit gate, would split the refusal between
    two functions and change `commitLocked`'s signature, `initEmpty`'s call
    included, to save lock time on a misconfiguration path; declined
    (*Alternatives considered*).
26. **The guard deletes only on a positive showing** (§2). The first form
    deleted whenever a decoded body's `Snapshot` was not `key`, and a body
    that is JSON but no root pointer, `null`, `{}` or an error document
    served with 200, decodes without error with an empty `Snapshot`; review
    found that, with a 501 that took effect, such a body would delete the
    snapshot the root pointer names. The guard now requires the current
    format version and a non-empty `Snapshot` that differs from the key, or
    a not-found. A root pointer of another format version is not understood,
    so nothing is deleted against it either. G6 pins each case.

## Alternatives considered

- **Latch the first outcome for the mount**, the recon's alternative: the
  first swap's answer, honoured or 501, decides for the mount's life. A store
  whose first swap happens to be answered 501, `initEmpty`'s included, then
  writes blind for good, and a store that honours conditions after one 501
  loses detection it has. It also makes safety depend on the order of
  replies, which the operator cannot see.
- **Probe the store at `New`** and refuse a store without conditional writes
  before serving. A write probe at mount is what ADR 0011 keeps out of a
  read-only mount and what `-check` already does for the operator; and a
  probe that passes does not stop one 501 later, so the per-call decision
  would still need this ADR's rule.
- **Diverge, or turn read-only, on a refused swap** (Assumption 4).
- **Make the opt-in skip the conditional attempt** (Assumption 5).
- **A `-single-writer` or `-assume-single-writer` flag** (Assumption 9).
- **Fail `-check` on any missing or weak conditional write without the flag**,
  the stale-accept row included (Assumption 10).
- **Model the opt-in as a constant** (Assumption 12).
- **A `policy.Policy` for the decision** (Assumption 7).
- **Keep the fallback as it is and document it harder.** The issue's first
  done-when rules it out: one reply still decides.
- **Leave the refused commit's snapshot in the bucket.** ADR 0019's passes
  reclaim it only once the operator has acted and two commits have landed;
  until then every interval and every client `COMMIT` adds one
  (Assumption 21).
- **Delete it without reading the root pointer first** (Assumption 22).
- **Reuse the uploaded snapshot on the retry** when the namespace has not
  changed since. The commit would have to know that no change landed between
  two attempts, which `FS.dirty` cannot tell, so every path that sets
  `FS.dirty` would gain a counter or a reset, which brings `FS.dirty` under
  ADR 0018's state rule (its §3, *Not yet moved*), and the protocol would
  swap to a key an earlier attempt wrote; left open (Assumption 24).
- **Back the committer off after a refusal.** It changes the committer's
  schedule, does nothing for a client's `COMMIT`, and delays a flaky store's
  recovery; the issue filed for the architect's N15 owns the committer's
  behaviour after repeated failures.
- **Reclaim after `commitLocked` releases `FS.mu`**, under the commit gate
  (Assumption 25).
- **Delete only when the root pointer the read names sorts below the
  attempt's key.** A stale read names the previous root, which sorts below
  the key, so the rule admits the very case it was meant to stop
  (Assumption 22).

## Consequences

- One 501, or any number, never makes a mount write the root pointer without
  a condition unless its operator said it may; another mount's acknowledged
  commit survives a 501 on this mount's swap, and this mount's next swap
  detects it.
- A deployment on a store without conditional writes must be started with
  `-allow-unconditional-root` to commit at all, and `-check` tells it so with
  exit status 1 until it is. With the flag, such a deployment runs exactly as
  before, and carries the risk it always carried, now stated on its command
  line.
- A mount whose store refuses its swaps logs two `Error` records per interval
  until the operator acts (Assumption 4), and uploads a snapshot per attempt
  that it then reclaims, so it costs bandwidth but no storage
  (Assumption 24).
- A refused commit leaves no object behind: its snapshot is deleted once one
  read shows the root pointer names another key, and is kept, with an
  `Error` record, if a store applied the write it answered 501
  (Assumption 22).
- `Config` has one more field, `FS` one more field and one more method, and
  `runCheck` one more argument.
- The model states the opt-in's cost, `CommitProtocol_UnconditionalTwoWriters`,
  beside what it is safe for, `CommitProtocol_UnconditionalSingle` and
  `CommitProtocol_PruneFallbackReply`.
- Hard to change later: that the opt-in is the operator's and never the
  store's, which the redesign's `fsinfo` swap (`docs/DESIGN.md` §5) inherits;
  and the flag's name, once operators' command lines carry it.

## What this does not decide

- A fallback `Put` that takes effect and fails, and the rest of #5's in-doubt
  knowledge, which stays an opted-in mount's route (§4). A swap reply with no
  ETag (#103).
- Whether `-check` should fail, without the flag, an endpoint that accepts a
  write conditioned on a superseded ETag (Assumption 10). Strata still
  commits there without detection.
- Whether the committer goes quiet after repeated identical failures
  (Assumption 4).
- A bucket whose mounts differ in the opt-in (Assumption 12).
- A 501 answered for a reason other than the condition: strata treats every
  501 on the swap alike, and with the opt-in falls back on any of them.
- A store that applies a write it answers 501. The reclaim's guard read
  keeps the snapshot and logs the case; the mount then diverges alone at its
  next swap, as after a lost reply, which #5's in-doubt knowledge resolves
  (Assumption 22).
- Not uploading a snapshot again while the namespace is unchanged since a
  refused swap (Assumption 24).
- Moving `commitLocked`'s store calls, the reclaim's included, off `FS.mu`
  (#43; Assumption 25).
- A `-check` probe that conditions a `PUT` on a `GET`'s ETag (#133).
- Multi-writer operation beyond detection (`docs/DESIGN.md` §15).

## Sources

Each was fetched on 2026-10-24 or 2026-10-25, while this ADR was written,
through a tool that summarises what it fetches, reads a long page 100,000
characters at a time, and quotes at most about 125 characters at once when
asked for verbatim text; where it split a sentence, the parts are joined
here as it gave them.

- RFC 9110, *HTTP Semantics*, §15.6.2:
  <https://www.rfc-editor.org/rfc/rfc9110.html>, read from character 400,000
  of 564,890. "The 501 (Not Implemented) status code indicates that the
  server does not support the functionality required to fulfill the request.
  This is the appropriate response when the server does not recognize the
  request method and is not capable of supporting it for any resource."
  §15.5.13: "The 412 (Precondition Failed) status code indicates that one or
  more conditions given in the request header fields evaluated to false when
  tested on the server". (*Context*)
- AWS, *How to prevent object overwrites with conditional writes*:
  <https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html>,
  read whole. `If-None-Match` and `If-Match` may each be used with
  `PutObject`, `CompleteMultipartUpload` and `CopyObject`; under *Conditional
  write behavior*, for `If-Match`: "If the ETag doesn't match, the write
  operation fails with a `412 Precondition Failed` response." and "You can
  also receive a `409 Conflict` response in the case of concurrent
  requests."; for `If-None-Match`: "When using conditional writes with
  `PutObject`, uploads may be retried after receiving a `409 Conflict`
  error." (*Context*)
- AWS, *Enforce conditional writes on Amazon S3 buckets*:
  <https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes-enforce.html>,
  read whole. "`CopyObject` requests without an `If-None-Match` or `If-Match`
  HTTP header fail with a `403 Access Denied` error. `CopyObject` requests
  made with those HTTP headers fail with a `501 Not Implemented` response."
  (*Context*)
- AWS, *Amazon S3 API Reference*, *Error responses*:
  <https://docs.aws.amazon.com/AmazonS3/latest/API/ErrorResponses.html>.
  **Honesty note:** fetched three times, once through
  <https://docs.amazonaws.cn/en_us/AmazonS3/latest/API/ErrorResponses.html>,
  and each time the tool received only the page's title, "Amazon S3"; the
  markdown route, `ErrorResponses.md`, answered 404. So the description AWS
  gives the `NotImplemented` code is not quoted here (Assumption 20).
- OpenStack Swift, commit `edd5eb29d7d6041ae0b16b78cbcb89f9dd95309f`, "s3api:
  Allow PUT with `if-none-match: *`":
  <https://opendev.org/openstack/swift/commit/edd5eb29d7d6041ae0b16b78cbcb89f9dd95309f>.
  Its message: "Swift already supports that much, at least. AWS used to not
  support any conditional PUTs, but that's changed somewhat recently", citing
  AWS's announcements of 2024-08 and 2024-11. Its diff, in
  `s3request.py`: `'If-Match', 'If-None-Match',` ... `'If-Modified-Since',
  'If-Unmodified-Since')):` becoming `'If-Match', 'If-Modified-Since',
  'If-Unmodified-Since'))` with `or self.headers.get('If-None-Match', '*') !=
  '*'):`, each followed by `raise S3NotImplemented(`; and a functional test
  asserting `self.assertEqual(get_error_code(body), 'NotImplemented')`.
  (*Context*) **Honesty note:** the rendered page the tool read omits the
  diff's `+` and `-` markers, so which lines are the old and which the new is
  my reading of the two conditions; both refuse `If-Match` on a `PUT`.
- Ceph, *S3 object operations*, RADOS Gateway documentation:
  <https://docs.ceph.com/en/latest/radosgw/s3/objectops/>. The `PUT OBJECT`
  request headers table lists `content-md5`, `content-type`,
  `x-amz-meta-<...>` and `x-amz-acl` and nothing else; the `GET OBJECT` table
  lists "| **if-match** | Gets only if object ETag matches ETag. | Entity Tag
  | No |" and "| **if-none-match** | Gets only if object ETag doesn't match.
  | Entity Tag | No |". (*Context*)
- MinIO, *Leading the Way: MinIO's Conditional Write Feature for Modern Data
  Workloads*:
  <https://www.min.io/blog/leading-the-way-minios-conditional-write-feature-for-modern-data-workloads>,
  published 2024-09-17, modified 2025-02-05. "While AWS only supports the
  If-None-Match condition, MinIO supports both If-None-Match and If-Match"
  and, of a mismatch, "Instead, it will return an HTTP status code 412
  (Precondition Failed)." A vendor blog; it names no release, and its
  statement about AWS predates AWS's `If-Match` support above. (*Context*)
- Dell, *ECS: Checking S3 Object existence*, knowledge base article
  000014759, last modified 22 May 2025, version 4:
  <https://www.dell.com/support/kbdoc/en-ai/000014759/ecs-checking-s3-object-existence>.
  "consider using PutObjectRequests conditional methods:" "withIfMatch() and
  withIfNoneMatch()." "Both of these methods need an object etag value and
  uses the headers "if-match," "if-none-match" in the PUT request." The
  article gives no status code for a failed condition. (*Context*)
- Web searches, through the same tool, on 2026-10-24, for how S3-compatible
  stores answer an unsupported conditional header, for MinIO's and Ceph's
  support, and for ECS's, were used only to find the pages above; nothing is
  cited from their summaries.

Cited as earlier ADRs quote them, and not fetched again: AWS's statements
that updates to a single key are atomic and that a failed `If-Match` is
answered 412 (ADR 0013 *Sources*), and that a `GET`'s ETag is the one
`If-Match` compares against (ADR 0020 *Sources*).
