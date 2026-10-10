# 0013. Data-integrity protocols are specified in TLA+ and checked by TLC

**Status:** Accepted
**Date:** 2026-10-10
**Issue:** #109

## Context

**The instruction.** The repository owner's instruction, in their words:
"Have architect write a TLA+ specification for critical code parts.
Everything concerning data integrity or security." #109 is a tracking issue
for the owner's instruction (one per instruction, as the architect
recommended; the owner delegated this on 2026-10-10). Every statement about
the code in this ADR was checked against `3deecf1`, and code is cited by
symbol.

**Defects found by reading, after the code existed.** Six defects in how this
filesystem keeps its data were found by reading code that had already been
written. Each is recorded in the ADR that decided its fix:

- **#37** (ADR 0005 *Context*; ADR 0006 Assumption 7; the trace is
  ADR 0003 §4's): `Write` copied a file's chunk list before it took the
  file's lock, so a flush in between made it rebuild a chunk from old
  contents and drop bytes a client had been told were written. `5479fe2`
  removed it.
- **#40** and **#56** (ADR 0006): bytes a truncate removed came back by five
  routes, and the queue of pending trims had no bound.
- **#41** (ADR 0005): a flush that fell between a `Read`'s two lock holds made
  it return an acknowledged write's previous contents.
- **#46** (ADR 0004): a chunk the cache held changed in place after a failed
  flush, and the wrong bytes were then stored under their own hash, where no
  verification could object.
- **#64** (ADR 0012): a commit could store a truncate's new size together with
  bytes the truncate removed, with or without a crash.

Each needed one interleaving or one sequence of calls that no test had tried,
and the test that reaches each came after it was found: ADR 0003 §4 names
#37's, and ADR 0004 §7, ADR 0005 §7, ADR 0006 §8 and ADR 0012 §9 specify the
others.

**What only review can show.** The latest ADRs close their test surfaces with
what tests cannot show. ADR 0012 §9: that the commit gate is held across the
whole commit phase on every path, the fail-safe of its §4, that every commit
path goes through `Sync`, and its lock-order rules. ADR 0011 §7: that
`store.ReadOnly` carries every call a read-only FS makes on its store, and
that the refusal is on `loadOrInit`'s not-found branch and nowhere else. Each
is a claim about every path or every interleaving. A test samples them, and a
reader checks them by hand, once, at one commit.

**The tools.** TLC, the model checker of the TLA+ tools, explores every
behaviour of a model within the bounds a configuration sets. The session
records the tooling state in these words:

> The owner approved the install on 2026-10-10, and the session has done it.
> In `$HOME/.local/share/tla/`: the Eclipse Temurin 21.0.12.1+1 JRE from
> `OpenJDK21U-jre_x64_mac_hotspot_21.0.12.1_1.tar.gz`
> (<https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.12.1%2B1/>;
> SHA-256 `6717ec641fd9ce0bb209ca083ee23b42202ac68cb6fcc5753496e0e4a0f41989`,
> which matched the GitHub release API's digest), unpacked as
> `jdk-21.0.12.1+1-jre`; and `tla2tools.jar` v1.7.4 from
> <https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar>,
> saved as `tla2tools-1.7.4.jar` (2,274,532 bytes; SHA-256
> `936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88`,
> recorded at download after the release notes' SHA-1
> `bee4a54f3ee3d4afc347c3240ec2d9e93b075104` matched). A smoke-test model
> checked cleanly. Nothing is vendored. `$TLA2TOOLS_JAR` names the jar, and
> defaults to `$HOME/.local/share/tla/tla2tools-1.7.4.jar`.

Checked again while this ADR was written (*Sources*): GitHub's release API
gives the v1.7.4 `tla2tools.jar` 2,274,532 bytes, and the release notes give
its SHA-1 as `bee4a54f3ee3d4afc347c3240ec2d9e93b075104`, both as recorded.
The API holds no digest for that asset, so the SHA-256 recorded at download
could not be checked here. The API gives the JRE archive the digest
`sha256:6717ec641fd9ce0bb209ca083ee23b42202ac68cb6fcc5753496e0e4a0f41989`, as
recorded. Nothing differed. That the install is in place and that the smoke
test passed are the session's report, which nothing here could check.

**Who can run what.** The architect writes specs and has no shell. `coder` and
`test-author` have none either, and `security-auditor`'s shell is fenced to
read-only inspection tools (`.claude/settings.json`). The session runs every
command, so TLC runs only where the session runs it.

**What this constrains.** `docs/tla/`, which this ADR creates; the doc
comments of the Go functions a spec traces, and the review of changes to
them; and the redesign's consistency point and sweeper (`docs/DESIGN.md` §5,
§8), which get specs before their code. CLAUDE.md's rule that the project uses
nothing outside the Go standard library, and that adding a module is the
owner's decision, bears on the tools (§9).

## Decision

### 1. What is specified, and in what order

The owner delegated this to the architect's recommendation on 2026-10-10:
CommitProtocol, FileData, NfsDurability and Budget, in that order; the
redesign's consistency point and sweeper are specified before their code; the
other areas go to the tools §1 names.

1. **CommitProtocol**: how a mount reads the root pointer, commits, prunes
   snapshots and diverges, against other mounts, failing store calls and
   crashes. Go: `loadOrInit`, `initEmpty`, `Sync`, `commitLocked`,
   `schedulePrune` and `pruneSnapshots` in `internal/blobfs/commit.go`;
   `S3.PutIfMatch` and `errorFor` in `internal/store/s3.go`; and
   `Local.PutIfMatch` in `internal/store/local.go`. It must show that no
   acknowledged commit is lost, that the root pointer never names a snapshot
   or a chunk the bucket does not hold, that a read-only mount writes nothing
   (ADR 0011), and that a mount diverges only after another writer.
2. **FileData**: one file's bytes through `Write`, `truncate`, `Read`, a flush
   and a commit, as ADR 0005, ADR 0006 and ADR 0012 decide them: a read takes
   one view, a pending trim is part of the file, and a commit stores a
   truncate whole or not at all. Go: `Read` and `readBuffered`, `Write` and
   `bufferWrite`, `truncate`, `flushOpen`, `flushAll`, `settleAll`, `Sync` and
   `commitLocked`. It models one file (Assumption 11).
3. **NfsDurability**: the write verifier, `UNSTABLE` and `FILE_SYNC` writes,
   `COMMIT`, the procedures RFC 1813 §1.6 lists as synchronous (as ADR 0012's
   *Sources* quote it), and shutdown; #44, #82 and #83. Its names are reserved
   for the lifecycle ADR (`docs/vocabulary.md`, V5).
4. **Budget**: ADR 0003 §4's `await`, with `settle`, `add` and `release`, in
   `internal/blobfs/budget.go`: at most one budget drain at a time, every
   drain settled, no lost wake-up, and the failure contract G1 to G3. It is
   specified for availability, not for data integrity: a drain left
   unsettled, or a wake-up lost, parks every writer over the limit for good
   (ADR 0003 §4).

**The redesign.** The consistency point and the sweeper (`docs/DESIGN.md` §5,
§8; phases 4 and 6 of §14) are specified before their code. The sweeper's
spec covers deduplication: once anything deletes chunk objects, whether
skipping the upload of a chunk already known or present is safe depends on
what the sweeper may delete meanwhile, which today's code, deleting no chunk,
never faces (Assumption 12).

**Not in TLA+.** Each other area goes to a tool that fits what can go wrong in
it:

| Area | Tool | Why not TLA+ |
|---|---|---|
| The duplicate request cache (ADR 0002) | Go tests | every transition is one hold of `replyCache.mu`, which `begin`, `finish` and `abandon` each take for their whole run, so its behaviours are orders of whole transitions, which tests enumerate; and its decisive property, that an entry is returned only to the connection that made it, is in the key (ADR 0002 §2) |
| Permissions, `AUTH_SYS`, the mount table | exhaustive tests of ADR 0014's policy; security review | per-request rules, not interleavings |
| SigV4, and what the logs carry (#21) | test vectors, log capture, review | per-request computations |
| XDR and RPC records | fuzzing | functions of untrusted bytes |
| Chunk-cache ownership (ADR 0004) | tests under `-race` | a rule about who may write a buffer, not a protocol between processes |
| Chunk verification and deduplication (#30, #31) | Go tests with store fakes | each compares one object's bytes with its key inside one call (Assumption 12) |
| Size and flag arithmetic (ADR 0007 to ADR 0010) | boundary tests, as those ADRs specify them | functions of their inputs (Assumption 12) |
| The namespace tree | model-based tests | `FS.mu` serialises every namespace operation, so each is one critical section |

### 2. Where specs live, and who writes them

`docs/tla/` holds one module per protocol, `<Module>.tla`, with its TLC
configurations. The path guards in `.claude/settings.json` already give it the
owners it needs: the architect may write `docs/*`, test-author may read `docs`
and `docs/*`, and coder's guard denies `docs/*`. The guard matches `*` across
`/` (`.claude/hooks/path-guard.sh`), so each of those covers `docs/tla/`. So
the architect writes the specs, test-author reads them (§8), and coder cannot
change them.

### 3. The language

- **Plain TLA+, no PlusCal.** The PlusCal translator writes the TLA+
  translation into the module (*Sources*), so the text written would not be
  the text checked, and no agent that writes a spec can run the translator.
- **ASCII** throughout, in modules and configurations alike.
- **`EXTENDS` names only `Naturals`, `FiniteSets`, `Sequences` and `TLC`.**
- **No equality across types.** Every comparison is between values of one
  kind: a variable holds one kind of value, and absence is a value of that
  kind, as `""` is for an ETag in `FS.rootETag`.
- **No definition takes the name of an operator those four modules define**,
  whether or not the module extends that one. `docs/vocabulary.md` lists
  them. So adding a module to `EXTENDS` never forces a rename.

### 4. What one action is, and what the model assumes

- **An action is one critical section or one store call**: one hold of one
  lock, or one call of a `store.Store` method.
- **`FS.mu` sections are atomic.** Nothing acquires the commit gate or an
  `openFile.mu` while holding `FS.mu` (the `FS.mu` doc comment in
  `internal/blobfs/fs.go`; ADR 0003 §3; ADR 0012 §5). The locks taken under it
  are leaves: the budget's mutex, which `dropOpen`, `truncate`, `dropIfGone`
  and `bufferWrite`'s tail block take to release a charge (ADR 0003 §3), and
  whatever a store call or a log call locks inside itself, as
  `Local.PutIfMatch` locks `Local.mu` when `commitLocked` calls it. A leaf
  waits for nothing that waits for `FS.mu`, so a hold of `FS.mu` that makes no
  store call is one action, however much it does.
- **A store call made holding a lock splits the hold only where another
  process interleaves.** `commitLocked` holds `FS.mu` across its snapshot
  `Put`, its root-pointer swap and, after `store.ErrUnsupported`, an
  unconditional `Put` and a `Head` (#43). In CommitProtocol other mounts, and
  this mount's prune goroutine, reach the bucket while it does, so each of
  those calls is an action, and the work between them joins the call before
  or after it, since no other process of the mount can observe it. In
  FileData, which has one mount and chunks that nothing deletes, nothing
  interleaves with a commit's or a flush's store calls, so a commit is one
  action (`SyncCommit`) and so is the body of a flush (`FlushBody`).
- **A read made holding no lock is a step of its own**, unless the spec says
  why folding it into the next critical section changes no property the spec
  checks. Today that read is `mutable()`, which `Write`, `SetAttr` and the
  namespace operations call before they take `FS.mu`, and which reads
  `FS.diverged`, which `commitLocked` writes holding `FS.mu`.
- **A spec may split an action further.** A finer split adds interleavings,
  so an invariant that holds of it holds of the coarser one; a liveness
  property then needs fairness on each part.
- **The bucket is a set of per-key linearizable objects.** A store call on one
  key takes effect at one instant between its call and its return, retries
  included, and nothing is atomic across keys. A listing, every page of it,
  is one step.
- **ETags and nonces are fresh.** A write gives its object an ETag that no
  earlier write of that object had, and a commit attempt writes its snapshot
  under a key no earlier attempt used.
- **Ghost variables hold the promised state.** What a caller was told, that a
  commit or a write was done or what a read returned, is held in variables
  only the model has, so that a property can compare the bucket and the
  replies with the promise. Ghosts trace to no Go symbol.
- **A crash** is process death, or any exit that skips the final commit. It
  loses all of a mount's memory, its prune goroutine included, keeps every
  store call that took effect, and restarts the mount at `loadOrInit`'s first
  read.

How far each of these rests on evidence, and where it does not hold, is in
Assumptions 4 to 10.

### 5. One vocabulary

`docs/vocabulary.md` holds the one set of names that Go state machines,
policies and specs share. Its rules are part of this decision:

1. A bare name is UpperCamel ASCII letters and digits starting with a letter;
   never a Go keyword or predeclared identifier, a TLA+ reserved word, or an
   operator of `Naturals`, `FiniteSets`, `Sequences` or `TLC`.
2. Go constants are the machine's lowerCamel prefix plus the bare name;
   `String()` returns the bare name, so log from/to/event values are the
   spec's names.
3. TLA+ holds a state as the string of its bare name in a variable named after
   the Go field (exceptions: `nsDirty` = `FS.dirty`; `bucketRoot` = the object
   at `rootKey`); actions and modelled events are operators with the bare
   name.
4. A Go predicate a spec uses keeps its name with the first letter
   upper-cased.
5. States may repeat across machines; events and actions are unique within a
   module.
6. `Fixes` members follow rule 1 and say what the fix does; the ADR or commit
   goes in the config comment.
7. For actions the Go column is indicative; `trace.tsv` is authoritative once
   it exists.

An entry is marked *exists*, in the code now, or *reserved*, introduced by the
ADR named. A later ADR that adds a machine adds its names to
`docs/vocabulary.md`. A rename changes that file, the specs and the Go names
in one PR.

### 6. The trace from model to code

- **`trace.tsv`.** `docs/tla/trace.tsv` arrives with the first spec and says
  which Go symbol performs each action: one row per action and symbol,
  giving the spec, the action, the Go file, the Go symbol, the locks held, and
  the ADR section that decides the behaviour. Its exact layout arrives with
  it.
- **Module headers.** Each module opens by stating its scope, what it models
  and what it leaves out; a `Checked against <commit>` line, naming the commit
  at which its trace was last checked against the Go code; and `Revised:`
  lines, dated, each saying what moved and why, as an ADR's do.
- **`TLA+:` lines in Go.** A Go function that performs an action carries, in
  its doc comment, a line `TLA+: docs/tla/<Module>.tla <Action>[, …]`, added
  by the PR that first traces it or changes it. The line is kept apart from
  `Spec:`, because a spec is a model: `docs/DESIGN.md` outranks the ADRs and
  the ADRs outrank the specs, and where a spec disagrees with either, the
  disagreement is a bug in one of them.
- **The tripwire.** An optional Go test, using only the standard library,
  fails when a symbol `trace.tsv` names is no longer in the file it names. It
  shows that the trace points at something, not that the code does what the
  action says; that stays review's (§8, §11).

### 7. Configurations: MustHold and MustFail

- Every module declares `CONSTANT Fixes`, the set of fixes a configuration
  includes, named as §5's rule 6 says. Its actions consult it, so one module
  describes the code with and without each fix.
- A **MustHold** configuration passes: TLC checks it and finds no violation.
- A **MustFail** configuration checks `TypeOK` and one property it is
  expected to violate. It passes when TLC reports that property violated, and
  fails on any other outcome: no violation, a violation of `TypeOK` or of
  anything else, or an error.
- Every fix in the code gets a MustFail configuration that removes it, so
  that each fix is shown to be needed.
- Every open defect gets a MustFail configuration that names its issue.
- A fix that lands flips its defect's configuration to MustHold, with the fix
  added to its `Fixes`, and adds a MustFail configuration that removes it.
- Configurations are append-only: none is deleted or renamed, and the flip is
  the only change an existing one takes.
- The exit statuses and log texts by which the runner tells these outcomes
  apart go in `docs/tla/manifest.tsv`, after the session has confirmed them
  against the installed TLC, and not in this ADR.

### 8. How a change is made

- **A refactor** of traced Go code changes `trace.tsv` and the module's
  `Checked against` line, and nothing else in `docs/tla/`. Coder cannot write
  there (§2), so the architect makes those changes on the same branch.
- **A change of behaviour in a traced protocol** goes spec first: the spec,
  then a TLC run, then the code, all on one branch.
- **Review.** A reviewer checks every changed function that `trace.tsv` names
  against its rows: that it still performs those actions, holding those
  locks.
- **A defect's counterexample becomes its Go regression test**, which
  test-author writes from the counterexample and the spec, never from the
  code.

### 9. The tools

- TLC runs from a pinned `tla2tools.jar` on Java 11 or later: "The TLA⁺ tools
  require Java 11+ to run." (the tlaplus README; *Sources*).
- Nothing is vendored. `$TLA2TOOLS_JAR` names the jar, and defaults to
  `$HOME/.local/share/tla/tla2tools-1.7.4.jar`.
- `docs/tla/tools.tsv`, which holds the pins, and the runner,
  `scripts/tla.sh`, arrive with the first spec.
- The session runs TLC.
- TLC writes nothing in the tree: whatever it writes while it runs, or when it
  finds a violation, goes outside the repository, so a run leaves the working
  tree as it found it.
- The owner delegated this to the architect's recommendation on 2026-10-10:
  the TLA+ tools are a development tool, not a dependency under CLAUDE.md's
  rule, because nothing in go.mod or the binary changes. ADR 0013 records
  them as the project's one non-Go development tool. Adding another is also
  the owner's decision.

### 10. When TLC runs

The owner delegated this to the architect's and the session's recommendation
on 2026-10-10. `bash scripts/tla.sh full` gates every PR that changes
`docs/tla/`, and other PRs skip TLC, since its outcome depends only on those
files. The runner fails, rather than skipping, when the tools are missing. The
trace tripwire runs in `go test`. The session records the rule in CLAUDE.md's
merge bar when `scripts/tla.sh` lands.

So the rule binds from the PR that brings `scripts/tla.sh`. A PR before it
that changes `docs/tla/`, as the one that adds this ADR and
`docs/tla/README.md` does, has nothing for TLC to check (Assumption 19).

### 11. What a spec does not show

- **Nothing about the Go code.** TLC checks the model. That the model
  describes the code rests on the trace (§6), and on reviewing it (§8).
- **Nothing past its bounds.** A configuration fixes how many mounts, files,
  chunks and steps TLC explores, and a property that holds within them may
  fail beyond them.
- So a check that passes narrows what review has to find, and does not
  replace it.

## Assumptions

Each is a judgement call, not something the issue, the spec or an earlier ADR
required, except where it says otherwise. Two record a decision the owner
delegated to the architect's recommendation, and say so: Assumptions 1 and
23. The owner's other decisions are recorded where they apply, in the words
the session gave: the tooling state in *Context*, the dependency rule in §9,
when TLC runs in §10, and the issue in *Context*.

1. **§1's scope and order.** The owner delegated this to the architect's
   recommendation on 2026-10-10: CommitProtocol, FileData, NfsDurability and
   Budget, in that order; the redesign's consistency point and sweeper are
   specified before their code; the other areas go to the tools §1 names. The
   judgement behind the order: CommitProtocol decides whether the bucket holds
   a filesystem at all, and the vocabulary already reserves names for two
   fixes in it. One is `AtomicLoad`, for the window between `loadOrInit`'s
   `Get` and its `Head` of the root pointer. The other is the root-pointer
   knowledge of #5: `commitLocked` keeps its old `rootETag` when `PutIfMatch`
   fails with any error but `store.ErrPrecondition` or
   `store.ErrUnsupported`, so a swap that took effect after all makes the
   mount's next commit fail its `If-Match`, and diverge, with no other writer.
   FileData is where three ADRs' rules meet. NfsDurability's machine waits for
   the lifecycle ADR. Budget is about availability. The instruction also names
   security. §1 sends the security areas to exhaustive tests, fuzzing and
   review, because each is decided one request at a time, and interleavings,
   which are what a model checker explores, do not decide them.
2. **Plain TLA+, no PlusCal** (§3). PlusCal reads more like code, and would
   make some specs shorter. Its translator decided it: the TLA+ that TLC
   checks would be text nobody wrote by hand, which the architect could not
   regenerate.
3. **Four standard modules** (§3). They are the tlaplus repository's own, and
   TLC implements `Naturals`' operators in Java (that module's own comment,
   *Sources*), so a module that extends only them needs nothing more
   downloaded or pinned. Each module is self-contained: none extends another
   of this repository's. A spec that needs another standard module, such as
   `Integers` or `Bags`, adds it by revising this ADR.
4. **`FS.mu` sections are atomic, and why** (§4). The reason is the lock
   order: `FS.mu` is last in it but for leaves, and a leaf waits for nothing.
   The stronger statement, that nothing takes a lock under `FS.mu`, would be
   false: the budget's mutex is taken under it by design (ADR 0003 §3), and so
   is `Local.mu`, inside `commitLocked`'s swap. What atomicity needs is the
   weaker one, which holds: nothing taken under `FS.mu` waits for anything
   that waits for `FS.mu`. The hold that makes store calls, `commitLocked`'s,
   is split at them (§4).
5. **Per-key linearizability** (§4). AWS documents strong read-after-write
   consistency for PUT and DELETE, atomic updates to a single key, a listing
   begun after a successful PUT that shows it, and no atomic update across
   keys (*Sources*). That is close to linearizability, but AWS does not call
   it that; reading it as linearizability per key is the judgement. It has
   four limits:
   - **ECS**, only as far as `-check` shows. Its ten probes (`runCheck`, in
     `cmd/strata/check.go`) passed on one cluster (`docs/DESIGN.md` §2), but
     each is one client's sequence of calls: round trips, a ranged read, an
     ETag from `Head`, a missing key's not-found, a listing of three objects
     just written, a read after a delete, and a conditional create, update
     and stale update. They show nothing about concurrent calls.
   - **`Local`.** `Local.PutIfMatch` is atomic only among calls on one
     `*store.Local`, because `Local.mu` lives in memory. Two processes on one
     directory can both pass its ETag check and both win. That deployment is
     outside the model.
   - **A `409 Conflict`.** AWS documents one "in the case of concurrent
     requests" to an `If-Match` write (*Sources*), an answer a linearizable
     register would not give while its condition holds. `errorFor` maps it to
     `store.ErrPrecondition`, which `commitLocked` takes for divergence. The
     CommitProtocol spec models that answer, or says why it leaves it out.
   - **A request that takes effect after its call has returned**, as a timed-out
     request can, is outside the model.
6. **Fresh ETags and nonces** (§4). The nonce is six bytes from `crypto/rand`
   in each snapshot key (`commitLocked`), which the model takes as never
   repeating. No two writes of the root pointer have the same body, since
   each names a snapshot key with its nonce and carries its writer and the
   nanosecond it was written (`rootPointer`). `Local`'s ETag is the MD5 of the
   body (`Local.etag`, `Local.PutIfMatch`), so two bodies' ETags differ unless
   MD5 collides, and AWS calls an ETag "unique to the object" and says it
   "reflects a change to the object's content" (*Sources*). Without
   freshness, a root pointer written back to an earlier body would match a
   stale ETag, and a stale swap could win.
7. **The crash model** (§4). Everything a mount holds in memory goes,
   including the prune goroutine, whose context `schedulePrune` derives from
   `context.Background`. Every store call that took effect stays, and a call
   in flight either took effect or never does. The mount comes back at
   `loadOrInit`'s first read: `New`'s checks of its `Config` make no store
   call (ADR 0009 §2). "Any exit that skips the final commit" covers `run`
   returning on a `Serve` error without its shutdown `Sync`, and the
   committer's final `Sync` cut off because `run` returned first (ADR 0012
   *Context*).
8. **Atomic listings** (§4). `S3.List` reads pages, `pruneSnapshots` reads
   1,000 keys a page, and `Local.List` walks a directory; none is one
   instant's view, and AWS promises only that a listing begun after a
   write's success shows it. The judgement is that this changes which old
   snapshots a pass deletes, and not whether it can delete the one the root
   pointer names: `pruneSnapshots` keeps the newest keys it saw, skips every
   key of the epoch it was started for, and deletes nothing it did not list.
   A spec that finds a property resting on the listing being atomic splits
   `PruneList` per page.
9. **A read made holding no lock** (§4). Folding `mutable()` into the critical
   section after it would hide that a commit can diverge between the two, so
   that a namespace operation is accepted, and acknowledged, after the
   divergence. None of the CommitProtocol properties `docs/vocabulary.md`
   names depends on that, since `commitLocked` checks `FS.diverged` again
   under `FS.mu` before it writes anything, and that is the kind of reason §4
   asks a spec to give; whether a namespace operation acknowledged then is
   durable is NfsDurability's question (§1). Separately,
   `mutable()` reads `FS.diverged` while `commitLocked` may be writing it,
   which the Go memory model defines as a data race (as ADR 0005's *Sources*
   quote it). `FS.mode`, read without a lock (`docs/vocabulary.md`, V1), is
   ADR 0014's.
10. **A finer split** (§4). It lets a spec keep apart a step the vocabulary
    names, such as `StartPrune` after a swap, where the rule would let it
    merge, at the price of more states.
11. **One file in FileData** (§1). ADR 0005's view, ADR 0006's trim and its
    invariants, and ADR 0012's Rules C and D and its mark are each about one
    file. The commit gate and `FS.mu`, which every file shares, are in the
    model whole. A second file adds the waits of ADR 0012 §7, which are
    delays. What one file cannot show is a deadlock that needs two files;
    ADR 0012 §5's argument, and review, cover that.
12. **The tools for three of §1's areas.** For chunk verification and
    deduplication, and for size and flag arithmetic, the tool and the reason
    in §1's table are mine, from the code and from the test surfaces of
    ADR 0007 to ADR 0010. That the sweeper's spec covers deduplication is
    mine too: `docs/DESIGN.md` §8's safety rules say what must not be swept,
    and nothing about an upload skipped because its chunk was present.
13. **The naming rules** (§5). One name per concept across Go, the specs and
    the logs, so that a log line, a row of `trace.tsv` and a step of a
    counterexample read the same. Rule 1's exclusions keep a name usable in
    both languages and in every module a spec may extend. The cost is that a
    rename touches three places, and log values (rule 2).
14. **An action that is an event's occurrence shares its operator** (§5).
    `SwapOK` is an event of the root-pointer knowledge machine and an action
    of CommitProtocol (`docs/vocabulary.md`, V2 and V3). It is one operator,
    since the action is the event happening. Rule 5's uniqueness is read as no
    two different operators sharing a name, not as forbidding that.
15. **The `TLA+:` prefix** (§6). A line of its own, so that a search finds
    every traced function, and so that nobody takes a model for a `Spec:`
    citation, which names normative text.
16. **Configurations are append-only, and a flip edits one** (§7). The flip
    is the only change an existing configuration takes: its expectation, and
    the fix added to its `Fixes`. What each fix was shown to be needed for is
    never lost.
17. **Pins in `docs/tla/tools.tsv`** (§9). Versions, sources, sizes and
    checksums live there, not here, so a new pin changes a data file and does
    not revise an ADR. The values in *Context* are a record of 2026-10-10.
18. **The session runs TLC** (§9). It is the only layer that runs commands.
    Giving an agent a shell for it would be agent configuration, which changes
    only on the owner's direct instruction (CLAUDE.md).
19. **The gate begins with the runner** (§10). The rule's own last sentence
    has the session record it in CLAUDE.md when `scripts/tla.sh` lands, and
    the runner fails when the tools are missing; read together, a PR that
    changes `docs/tla/` before the runner exists is not gated, and has no
    spec to check.
20. **Issues cited by number.** #5, #11, #21, #30, #31, #82 and #83 are cited
    as the session gave them. Their text is not in the repository, I could not
    read it, and nothing here depends on more than which area each belongs to.
21. **ADRs cited before they exist.** ADR 0014, the lifecycle ADR, the ADR for
    #5 and the load-race ADR are planned, not written. ADR 0014 is cited by
    the number the session's plan gives it; if that number goes to another
    decision, these citations, and `docs/vocabulary.md`'s, move with it.
22. **No `CHANGES` entry.** Nothing a user or a running deployment sees
    changes: no code, flag, format or behaviour (CLAUDE.md's rule for
    `CHANGES`).
23. **Status is Accepted on creation.** The owner delegated this to the
    architect's recommendation on 2026-10-10.
24. **Read, not run.** The code was read at `3deecf1`, nothing was run, and no
    spec exists yet. The tooling state is the session's report, checked
    against its sources as far as they allow.

## Alternatives considered

- **Go tests alone.** What the repository has relied on. The six defects in
  *Context* passed the tests that existed, and ADR 0011 and ADR 0012 list what
  their tests cannot show. A test reaches an interleaving someone thought of,
  through holds in store fakes or a grace period (ADR 0005 §7). Go tests stay:
  §1 gives them most areas, and every counterexample becomes one (§8). What
  lost is relying on them alone for §1's four protocols.
- **Property-based and fuzz tests alone.** They widen the inputs a test tries,
  not the interleavings: the scheduler still picks which interleaving runs,
  and the race detector finds only races that happen in the run (ADR 0005
  §7). Fuzzing keeps XDR and RPC records (§1).
- **Apalache**, "A symbolic model checker for TLA+", which "translates TLA+
  into the logic supported by SMT solvers such as Microsoft Z3 and CVC5" (its
  README; *Sources*). It would bring an SMT solver into the toolchain, a
  further tool, which §9 makes the owner's decision, for checking that TLC
  already does within bounds.
- **PlusCal.** Lost on §3's grounds (Assumption 2).
- **Specs beside the Go code**, such as `internal/blobfs/CommitProtocol.tla`.
  That is outside the architect's write scope (`docs/* README.md CHANGES
  internal/vfs/*`), inside coder's, whose guard denies nothing under
  `internal/` but `internal/vfs/*`, and unreadable by test-author, whose read
  guard admits only `*_test.go`, `internal/vfs`, `docs`, `README.md`,
  `CLAUDE.md` and `go.mod`, and what lies under `internal/vfs` and `docs`
  (`.claude/settings.json`). Changing those scopes is agent configuration.
- **Quint, or P.** Quint is installed with npm, Homebrew, Nix or a binary;
  "The model checker requires Java Development Kit >= 17", and `quint verify`
  runs Apalache, "automatically downloaded", or TLC as an alternative backend.
  P needs the .NET SDK 8.0, and its compiler needs Java 11 or later
  (*Sources*). Each adds a language and a toolchain that the install the owner
  approved does not cover, and its spec is not TLA+.
- **TLAPS**, which "mechanically checks TLA+ proofs" through backend verifiers
  its page does not name (*Sources*). A proof holds without bounds, which §11
  says a model check does not, but every proof step is written by hand, and
  the verifiers are tools beyond the one §9 records. If a property ever has to
  hold past every
  bound TLC can reach, a later ADR can take it up.
- **Vendoring `tla2tools.jar`.** A 2,274,532-byte binary in git. The pin in
  `tools.tsv`, with its size and checksums, makes a run reproducible without
  it.
- **TLC on every PR.** Its outcome depends only on the files in `docs/tla/`
  (§10), so on a PR that changes none of them a run repeats a known result, in
  the session's time.
- **The vocabulary inside an ADR.** ADRs are append-only in spirit
  (`docs/adr/README.md`), while the vocabulary grows with every machine and a
  rename changes it in place (§5). It lives in its own file, under this ADR's
  rules.

## Consequences

- **A protocol change starts with its spec.** A change of behaviour in a
  traced protocol is specified and checked before it is coded, on one branch
  (§8). Each such change costs more, and the ordering is the one whose absence
  let *Context*'s defects reach code first.
- **The project has its first non-Go tool**: a JRE and a jar, outside the
  tree, which §9 records as a development tool and not a dependency.
- **Open defects stay visible.** An open defect's MustFail configuration stays
  MustFail, and the gate stays green, until its fix lands and flips it (§7),
  so the manifest lists every defect the models show.
- **Reviewers gain a trace check**: a diff that changes a traced function is
  read against `trace.tsv` (§8).
- **Go and spec names are bound together** (§5). A rename changes the
  vocabulary, the specs and the Go code in one PR and, since a machine's
  `String()` gives the bare name, the values in its log lines too.
- **Every check is the session's to run.** The architect writes specs it
  cannot check, so each spec change is a round trip through the session (§9).
- **Hard to change later**: a name, once logs carry it; and how a protocol is
  cut into actions, once `trace.tsv` and the `TLA+:` lines record it.

## What this does not decide

- **The fixes.** Each open defect a spec shows is decided in its own ADR: the
  load race (`AtomicLoad`), the root-pointer knowledge of #5, and whatever
  else the specs find.
- **CI** (#11). TLC runs where the session runs it (§10).
- **The redesign's specs themselves**, whose order §1 fixes and whose content
  waits for their phases.
- **Agent configuration.** §2 rests on today's guards, and any change to them
  is the owner's.
- **`docs/DESIGN.md` §5 step 4's wording**, "the ETag read at mount", which
  the load-race ADR settles.
- **The Go mechanism for a state machine**: the `mountMode` type, its
  `String()`, and how a transition is logged, which ADR 0014 decides.
- **Whether a PR that changes only `scripts/tla.sh` runs TLC.** §10's trigger
  is a change to `docs/tla/`, and the runner lives outside it. The session
  settles it when it records the rule in CLAUDE.md.
- **`trace.tsv`'s exact layout, and where the tripwire test lives.** Both
  arrive with the first spec.

## Sources

Fetched on 2026-10-10 through a tool that summarises what it fetches, and
quoted as it returned them when asked for verbatim text. It reads a long page
100,000 characters at a time, and for some files it declined to quote more
than 125 characters at once. Each URL below was fetched while this ADR was
written; none is cited from an earlier fetch.

- The tlaplus README:
  <https://raw.githubusercontent.com/tlaplus/tlaplus/master/README.md>. Under
  *Use*: "The TLA⁺ tools require Java 11+ to run." Under *Overview*:
  "Versioned releases can be found on the Releases page." and "Currently,
  every commit to the master branch is built & uploaded to the 1.8.0 Clarke
  pre-release." (§9; the pin is a versioned release, not a build of `master`)
- GitHub's release API for tlaplus v1.7.4:
  <https://api.github.com/repos/tlaplus/tlaplus/releases/tags/v1.7.4>.
  `tag_name` v1.7.4, `name` "The Xenophanes release", `published_at`
  2024-08-05T20:16:41Z, not a pre-release. The asset `tla2tools.jar`: `size`
  2274532, `digest` null, `browser_download_url`
  <https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar>.
  The release notes' lines "### Checksums", "sha1sum|file" and
  "bee4a54f3ee3d4afc347c3240ec2d9e93b075104|tla2tools.jar"; they name no
  SHA-256. (*Context*)
- GitHub's release API for Temurin 21:
  <https://api.github.com/repos/adoptium/temurin21-binaries/releases/tags/jdk-21.0.12.1%2B1>.
  `tag_name` jdk-21.0.12.1+1, `published_at` 2026-08-19T12:10:19Z, not a
  pre-release. The asset `OpenJDK21U-jre_x64_mac_hotspot_21.0.12.1_1.tar.gz`:
  `size` 42131876, `digest`
  `sha256:6717ec641fd9ce0bb209ca083ee23b42202ac68cb6fcc5753496e0e4a0f41989`.
  (*Context*) **Honesty note:** the page is 538,359 characters long and was
  read in three parts, from characters 0, 100,000 and 200,000; the asset was
  in the third. Adoptium's own checksum file,
  <https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.12.1%2B1/OpenJDK21U-jre_x64_mac_hotspot_21.0.12.1_1.tar.gz.sha256.txt>,
  answered with a redirect to another host that the tool could not follow, so
  only GitHub's digest was compared.
- The standard modules, from the tlaplus repository's
  `tlatools/org.lamport.tlatools/src/tla2sany/StandardModules/`, at the tag
  v1.7.4 and at `master`:
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/tla2sany/StandardModules/Naturals.tla>,
  and the same path for `FiniteSets.tla`, `Sequences.tla` and `TLC.tla`, and
  for all four under `master` in place of `v1.7.4`. The operators each
  defines, which `docs/vocabulary.md` lists; the two versions agree, and none
  is `LOCAL`. From `Naturals`' comment: "These definitions are all overridden
  by TLC in the Java class tlc2.module.Naturals." (§3; §5's rule 1)
  **Honesty note:** the tool returned `Naturals` whole, but for the other
  three it declined to reproduce the file and listed its definitions instead,
  so those lists are its reading of the primary source.
- The TLA+ grammar at v1.7.4:
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/javacc/tla%2B.jj>.
  Its word tokens, all in capitals, among them `ASSUME`, `CASE`, `CHOOSE`,
  `CONSTANT`, `EXTENDS`, `INSTANCE`, `LET`, `IN`, `LOCAL`, `MODULE`, `THEOREM`,
  `VARIABLE`, the proof words, and the operators `DOMAIN`, `ENABLED`, `SUBSET`,
  `UNCHANGED` and `UNION`. (§5's rule 1) **Honesty note:** the file is 203,243
  characters long, and only its first 100,000, which the tool said hold every
  token declaration, were read.
- *The Go Programming Language Specification*: <https://go.dev/ref/spec>. Its
  keywords, from `break` to `var`, and its predeclared identifiers, from `any`
  to `recover`, every one in lower case. (§5's rule 1) **Honesty note:** read
  from the first 100,000 of its 263,947 characters, which hold both lists.
- AWS, *What is Amazon S3?*, section *Amazon S3 data consistency model*:
  <https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html>.
  "Amazon S3 provides strong read-after-write consistency for PUT and DELETE
  requests of objects in your Amazon S3 bucket in all AWS Regions." "Updates
  to a single key are atomic." "Any read (GET or LIST request) that is
  initiated following the receipt of a successful PUT response will return
  the data written by the PUT request." "Updates are key-based. There is no
  way to make atomic updates across keys." And, under *Concurrent
  applications*, "Amazon S3 internally uses last-writer-wins semantics to
  determine which write takes precedence." (§4; Assumptions 5 and 8)
- AWS, *How to prevent object overwrites with conditional writes*:
  <https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html>.
  "An object's ETag is a string that's unique to the object and reflects a
  change to the object's content." Under *Conditional write behavior*, for
  `If-None-Match`: "If multiple conditional writes or copies occur for the
  same object name, the first write operation to finish succeeds. Amazon S3
  then fails subsequent writes with a 412 Precondition Failed response." For
  `If-Match`: "If the ETag doesn't match, the write operation fails with a
  412 Precondition Failed response." and "You can also receive a 409
  Conflict response in the case of concurrent requests." (Assumptions 5
  and 6)
- The Apalache README:
  <https://raw.githubusercontent.com/apalache-mc/apalache/main/README.md>.
  "A symbolic model checker for TLA+" and "Apalache translates TLA+ into the
  logic supported by SMT solvers such as Microsoft Z3 and CVC5." The page
  states no Java version and does not say whether a solver ships with it.
  (*Alternatives considered*)
- Quint, *Getting started*: <https://quint.sh/docs/getting-started>, reached
  by a redirect from <https://quint-lang.org/docs/getting-started>. "Install
  node/npm if you don't have it already", with Homebrew, Nix and binaries as
  the other routes, and "The model checker requires Java Development Kit >=
  17." Quint, *Model checkers*: <https://quint.sh/docs/model-checkers>.
  "Quint specifications can be verified by two model checkers: TLC and
  Apalache.", and Apalache "will be automatically downloaded and invoked when
  a `quint verify` is run." (*Alternatives considered*)
- P, *Installing P*: <https://p-org.github.io/P/getstarted/install/>. "P
  currently uses the specific version of .Net SDK 8.0" and "The P compiler
  also requires Java (`java` version 11 or higher)." (*Alternatives
  considered*)
- TLAPS: <https://proofs.tlapl.us/doc/web/content/Home.html>, reached from the
  redirection page at <https://proofs.tlapl.us/>. "The TLA+ Proof System
  (TLAPS) mechanically checks TLA+ proofs." and "It provides a consistent
  abstraction over the various "backend" verifiers." The page names none of
  them. (*Alternatives considered*)
- *The PlusCal Translator*, from the TLA+ Toolbox's documentation:
  <https://nightly.tlapl.us/doc/pluscal/pluscal.html>. "The PlusCal translator
  writes the TLA+ translation of the algorithm into the module." (§3)

Cited as earlier ADRs quote them, and not fetched for this ADR: RFC 1813
§1.6's list of synchronous procedures (ADR 0012's *Sources*), and the Go
memory model's definition of a data race (ADR 0005's *Sources*).
