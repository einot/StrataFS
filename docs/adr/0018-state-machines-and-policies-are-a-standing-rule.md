# 0018. State machines and policies are a standing rule

**Status:** Accepted
**Date:** 2026-10-24
**Issue:** #114

## Context

**What ADR 0014 decided.** ADR 0014 added `internal/fsm` and
`internal/policy`, pinned their APIs, and applied them to a fixed scope, its
§1: the mount mode, root-pointer knowledge and the lifecycle as machines, link
counts in a lighter form, every check of what a caller may do as a policy, and
ten places left as they were, each with its reason. Its §1 also said
"Everything else keeps its present form", which its Assumption 1 recorded as
the architect's judgement, not the owner's decision.

**The owner's decisions.** Three, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. **The rules are standing.** On 2026-10-22 the owner said: "1. Propose a
   wording that will keep the idea that state machines ought to be used for
   any stateful code unless there's a good reason to do it otherwise. All
   exceptions should be backed with a relevat ADR. 2. Same applies here.
   Whenever the state machine is security or related to authorisations of
   some kind the transitions should be linked to a policy engine unless
   there's a solid reason to do it differently. Exceptions need to be backed
   up with a relevant ADR. 3. Use some generalized wording that would allow
   exceptions whenever architect has a good reason for it as I wrote
   earlier." Of the wording the session then proposed, the owner said: "Go
   ahead with that wording." That wording is what CLAUDE.md carries under
   "State machines and policies", with the third rule as decision 2 changed
   it. "Same applies here" answers a question of the session's that the
   architect was not given, so this ADR follows the approved wording, not a
   reading of that question.
2. **Three rulings on the draft.** This decision was first drafted as a
   revision of ADR 0014, and the architect's report on that draft raised four
   points. On 2026-10-24, when the session asked the owner about them, the
   owner, as the session reported:
   - accepted a new text for the third rule, which CLAUDE.md now carries:
     "**A security transition asks a policy.** When a transition is
     security-relevant, as ADR 0014 §12 defines it, it is linked to a policy.
     The policy's verdict guards the transition, and the event fires only if
     the policy permits it. ADR 0014 §12 defines how the two connect." The
     section that text cites as ADR 0014 §12 is §5 of this ADR
     (Assumption 3);
   - accepted that `reviewer`'s check of these rules covers code "that a
     change adds or alters", which is how §3 reads a change;
   - kept CLAUDE.md's lead-in to the rules: "By the repo owner's order of
     2026-10-22, these rules bind every agent's design and code, and
     `reviewer` checks them."

   The session reported no ruling on the draft's remaining point, about the
   exemptions E11 and E12, which stay the architect's (Assumption 9).
3. **The status and the Issue line.** On 2026-10-10 the owner said:
   "Otherwise, follow architect's and your own recommendations." (ADR 0014,
   decision 2). ADRs 0015 to 0017 were Accepted on creation under those
   words, and so is this one (Assumption 1).

**Why a new ADR.** The draft rewrote ADR 0014's §1 in place, and every later
exemption would have edited it again. `docs/adr/README.md` keeps an Accepted
decision append-only in spirit, and corrects in place only a restatement whose
fact has moved; review found the conflict. So the standing rules are a
decision of their own, here. ADR 0014 keeps its decision and its text, and
gains cross-references to this ADR. The exemptions are listed in a register,
`docs/adr/exemptions.md`, which ADRs add to and which no ADR's text holds
(Assumption 2).

**What the code has.** Every statement about the code here was checked by
reading it at `2d7ff34`, the commit this branch was cut from. Those about the
holds of `Lookup`, `ReadDir`, `Read`, `SetAttr`, `createEntry`, `unlink`,
`Rename`, `Write`, `bufferWrite` and `truncate` were read again in this
branch's worktree at `43460cd`, whose two later commits change no code by the
session's account. Nothing was run (Assumption 4). Code is cited by symbol.

**What this constrains.**

- how new and changed code in every package expresses state and decisions
  about who may act (§1 to §3), the code of each phase of `docs/DESIGN.md`
  §14 included;
- what an ADR that grants or revokes an exemption does, and the register
  (§4);
- how a security-relevant transition asks a policy (§5).

It replaces ADR 0014 §1's scope for new and changed code, and leaves ADR 0014
§2 to §11 as decided: the APIs, the conventions for a machine, the locks, the
PRs that wire the packages in, the machines given to other ADRs,
observability, names and the test surface. It changes no code and no test.

## Decision

### 1. The rules

In the wording the owner approved (decisions 1 and 2), with its references
made local and the list of exemptions given its place (§4):

- **State is a machine.** Code that holds state and moves it between named
  states expresses that state as an explicit transition table run by
  `internal/fsm` (ADR 0014 §2, §3). That covers a mode, a lifecycle, a
  connection, a protocol phase, or anything whose legal next step depends on
  where it is now. Every write of the state goes through the machine, and an
  illegal transition is an error, never a silent overwrite.
- **Who may act is a policy.** Every decision about whether a caller may do
  something is a pure policy in `internal/policy` (ADR 0014 §5), not an
  inline condition. That covers a permission check, an ownership or time
  rule, an access mask, and a gate on an operation.
- **A security transition asks a policy.** When a transition is
  security-relevant, as §5 defines it, it is linked to a policy. The policy's
  verdict guards the transition, and the event fires only if the policy
  permits it. §5 defines how the two connect.
- **Exceptions are decided, not assumed.** The architect may exempt code from
  any of these rules when it has a good reason. Examples: a hot path where a
  table costs more than it catches, state that another mechanism already
  pins and tests, or a flag with a single rule. Each exemption is recorded in
  an ADR that names the code and the reason. The register,
  `docs/adr/exemptions.md`, lists the current ones. Code without an exemption
  follows the rules.

These rules apply to new and changed code. Existing code moves over as #114
plans (§3).

The rest of this ADR calls the first three the state rule, the policy rule
and the link.

### 2. What they reach

- A *caller* is whoever a request comes from: the `vfs.Caller` of a
  filesystem call, or, at another layer, the credential a call carries or the
  operator who gives a command. A step the server takes on a store's answer,
  a signal or a timer comes from no caller, even when a call led to it.
- The state rule reaches state that has named states. A count, a size, an
  epoch or a time is a value, not a named state; ADR 0014 §8 gives link
  counts a lighter form of their own. A cache, which may gain or lose any
  entry at any step, has no state of its own.
- The policy rule reaches a decision that allows or refuses a well-formed
  request, or for `ACCESS` says what it would allow:
  - by who the caller is or what it holds: a permission check, an ownership
    or time rule, an access mask; or
  - by what the mount allows, whoever asks: a gate on an operation, as
    `mutable()` is at ADR 0014 §6's stage A, and as `ACCESS`'s read-only mask
    is (E11, E12).

  A check of whether a request is well formed (ADR 0014 §6's stage B), or of
  what it names (stage E), says what the operation is, not who may perform
  it, and the policy rule does not reach it (ADR 0014, Assumption 18).
- §5 says what the link reaches.

**What the rules do not reach.** These are the things ADR 0014 §1 kept as
they were, apart from the three that §3 lists as not yet moved. None is an
exemption. A change that gives one of them named states, or makes its answer
turn on who asks or on what the mount allows, brings it under the rules
(Assumption 6).

- `validName`, the empty-write rule and `Symlink`'s empty-target check:
  whether a request is well formed.
- The namespace preconditions: what a request names, against the namespace
  (ADR 0014, Assumption 18).
- Credential parsing, `readCred` in `internal/sunrpc/rpc.go` and
  `callerFrom` in `internal/nfs/nfs3.go`: who a call says it comes from. It
  decides nothing about what that caller may do. Root squash is not decided
  here (*What this does not decide*).
- The export match, `(*MountServer).accepts` in `internal/nfs/mount.go`: a
  path that names no export gets `MNT3ERR_NOENT`, whoever asks.
- RPC admission, `(*Server).dispatch`'s checks of the RPC version, the
  program, its version and the procedure, and of the encoding of the
  credential and the verifier, in `internal/sunrpc/rpc.go`: whether a call is
  well formed for this server, whoever sends it.
- The idempotency list, `Handler.Idempotent`: a fixed property of each
  procedure.
- `store.ReadOnly`: it refuses every write the mount itself makes, whatever a
  caller asked, so no caller's request reaches it as a decision. A caller's
  change is refused above it, at the mount gate (E11; ADR 0011 §3).
- The chunk cache, `chunkCache`, and the known-chunk set, `FS.knownChunks`:
  caches.
- `COMMIT`'s lack of a permission check (`vfs.FS.Commit` has no `Caller`):
  there is no check to express. One added later is new code.

### 3. New, changed and existing code

**New and changed code.** The rules bind a change that adds state or a
decision about who may act, or that changes how existing state moves (its
states, its events, its transitions, or the places that write it) or what an
existing decision allows. Such a change brings that state, or that decision,
under the rules whole, so every write of the state goes through its machine
from then on. A change that only moves, renames or reformats such code, or
edits the code beside it, does not (Assumption 5).

**Existing code** moves over as #114 plans. ADR 0014 plans its §6 and §7,
and gives the machines of its §8 to their own ADRs. Until code moves, it is
not exempt by that fact: a change to it, as above, brings it under the rules,
unless an ADR exempts it first.

**Not yet moved.** At `2d7ff34` these are in the state rule's reach, are not
exempt, and are not among ADR 0014's conversions:

- `FS.dirty`: a flag that every change sets and a successful commit clears.
- `FS.pruning`: set by `schedulePrune`, and cleared by the goroutine it
  starts, under `FS.mu`. `schedulePrune` runs under `FS.mu` too, except from
  `initEmpty`'s commit, which runs before the FS is shared. CommitProtocol
  refines the flag into three phases (`pruning[m]`, vocabulary V3).
- The open-file entry (#42): an `openFile`'s life in `FS.open`, which it
  enters only through `getOpen` and leaves only through `dropOpen`.

Each moves when a change reaches it, or as #114 plans if that comes first,
unless an ADR exempts it.

### 4. Exemptions

**The first twelve.** This ADR grants E1 to E12. Each names its code, the
rule it is exempt from, and the reason. E1 to E10 are the ten places that the
owner's decision 1 in ADR 0014 accepted leaving as they were, with the reasons
ADR 0014 §1 gives them, which are the architect's; this ADR adds their code
and their rules (Assumption 8). E11 and E12 record forms that ADR 0014 §6 and
§7 already decide, so that those forms keep the policy rule (Assumption 9).
None of them is exempt from the link, which none of them needs (§5).

- **E1. The write budget**: `budget`, in `internal/blobfs/budget.go`; from
  the state rule. It is already the most precisely specified machine here
  (ADR 0003 §4, with its failure contract G1 to G3). Rewriting it risks that
  contract for nothing, and the Budget spec checks it (ADR 0013 §1).
- **E2. The duplicate request cache**: `replyCache` and its `cacheState`, in
  `internal/sunrpc/cache.go`; from the state rule. ADR 0002 §6 pins its three
  states and §8 its API. Another representation would change the memory
  bound its *Consequences* compute.
- **E3. `Sync`'s phases and the commit gate**: `(*FS).Sync`, in
  `internal/blobfs/commit.go`, and `FS.commitGate`, in
  `internal/blobfs/fs.go`; from the state rule. The sequential code already
  is the machine, and the gate is a lock. FileData models both.
- **E4. The per-index buffered state**: `openFile.dirty` and
  `openFile.pendingTrim`, in `internal/blobfs/fs.go`; from the state rule. It
  is a hot path, and invariants I1 to I4 are pinned and tested (ADR 0006 §2).
  A table lookup per index would catch nothing new.
- **E5. The unsettled mark**: `openFile.unsettled`, in
  `internal/blobfs/fs.go`; from the state rule. Every (state, event) pair is
  legal, so a table would refuse nothing (ADR 0012 §3).
- **E6. The XDR reader's sticky error**: `xdr.Reader`'s `err`, read through
  `Err`, in `internal/xdr/xdr.go`; from the state rule. It is an idiomatic
  sticky error over a bounded reader.
- **E7. The store's retries**: `(*S3).do`, in `internal/store/s3.go`; from
  the state rule. They are a bounded loop.
- **E8. The flag checks**: `chunkSizeBytes` and `cacheBytes` (ADR 0008),
  `commitInterval` (ADR 0009) and `ownerID` (ADR 0010), in
  `cmd/strata/main.go`; from the policy rule. They are pure functions of their
  flags that those ADRs pin: three refuse a value, and `cacheBytes` gives one
  too large to express the largest bound there is.
- **E9. `-check`**: `runCheck` and its probes, in `cmd/strata/check.go`;
  from the state rule. It is a linear script of probes.
- **E10. The `MaxFileSize` clamp**: in `(*FS).Write`, in
  `internal/blobfs/fs.go`, which shortens a write that crosses
  `FS.maxFileSize`; from the policy rule. A clamp is not an allow or a deny
  (ADR 0007 §4).
- **E11. The mount gate**: `(*FS).mutable`, in `internal/blobfs/fs.go`; from
  the policy rule's "in `internal/policy`". It judges the mount, not the
  caller, so it has no `Request` to read. At `2d7ff34` it reads `FS.readOnly`
  and `FS.diverged`. Once ADR 0014 §7 is done, its one input is the mount
  mode, whose type `blobfs` keeps unexported, and `policy` could name that
  type only by importing `blobfs`, which imports `policy` once ADR 0014 §6 is
  done; from then on it fails closed on a mode it does not name, as a policy
  would (ADR 0014, Assumption 24). Its form is ADR 0014 §7's.
- **E12. `ACCESS`'s read-only mask**: in `(*FS).Access`, in
  `internal/blobfs/fs.go`, which on a read-only mount leaves out
  `vfs.AccessModify`, `vfs.AccessExtend` and `vfs.AccessDelete`; from the
  policy rule's "in `internal/policy`". The mask is the mount's, not the
  caller's, and once ADR 0014 §6 is done the caller's part is
  `policy.AccessBits` (ADR 0014, Assumption 21). Its form is ADR 0014 §6's.

**The register.** `docs/adr/exemptions.md` lists every exemption an ADR has
granted, one row each: its number, what it is, its code, the rule it is
exempt from, the ADR that granted it, and whether it is in force. It is not an
ADR, and editing it revises none (Assumption 7).

- Only an ADR grants an exemption. It names the code, by symbol and file; the
  rule it is exempt from; and the reason. The PR that carries it adds a row
  under the next number. No number is reused, and no row is removed.
- An ADR revokes an exemption by saying so, and either moves the code in the
  same PR or says which change will. That PR marks the row "revoked by
  ADR NNNN". The text that granted the exemption stays as the record.
- An exemption holds while its reason does. A change that removes the
  reason, such as one that supersedes the ADR an exemption cites, either
  restates the exemption in its own ADR with a reason that still holds, and
  its PR adds that ADR to the row, or revokes it.
- A row's ADR is the authority for its code and its reason. Where the two
  disagree, the row is the bug.

### 5. A security-relevant transition asks a policy

This is the link. It changes neither `fsm` nor `policy`: it says where and
when an owner asks its policy (Assumption 11).

**What is security-relevant** (Assumption 10).

- A machine is security-relevant when a policy's request or a gate (§2)
  reads its state, or when its state records authority a caller holds, such
  as a lock, a lease or a share reservation.
- A transition is security-relevant when:
  1. a caller's request causes it, in a security-relevant machine;
  2. a caller's request asks for it, and whether that caller may have it
     depends on who the caller is or on what it holds; or
  3. it widens what callers may do: in the state it enters, a policy or a
     gate allows a request that it refuses in the state it leaves, or a
     caller holds authority it did not hold before.
- No other transition needs a policy. In particular:
  - one that only follows a change a caller's request was allowed to make, in
    a machine that is not security-relevant, such as setting a dirty flag,
    dropping a link count or opening a buffer, is part of that change. The
    change's own check guards it, where ADR 0014 §6 places that check;
    whether that place is right for `Write` and `truncate` is #101's
    question;
  - one that no caller causes, and that only narrows what callers may do,
    fails safe. It can only take something away, and there is no caller
    whose right to it a policy could judge.

**The link.** For each security-relevant transition:

- **Its ADR names the request.** The ADR that adds or changes the transition
  names the request that guards it, as ADR 0014 §6 does for each method: the
  `Op`, and where each field of the `Request` comes from. It also names the
  hold the request is made in.
- **One exclusive hold.** The owner builds the request from the state as it
  stands, asks its policy, and fires the event only if the policy returns
  nil, all in one hold in which:
  - the lock that serializes the machine's `Fire` calls is held exclusively:
    a `sync.Mutex`, or a `sync.RWMutex` held for writing. For a `Machine`,
    that is the lock its doc comment names (ADR 0014 §2). An `Atomic` with a
    security-relevant transition names such a lock in its doc comment too,
    and every `Fire` on it, of any event, is made holding that lock
    exclusively; its `State` may still be read without it;
  - every lock that guards what the request reads is held, for reading at
    least.

  So no other `Fire` can move the machine, and nothing the verdict read can
  change, between the verdict and the `Fire`. A read hold of the serializing
  lock is never enough: two holders could each be permitted against one
  state, and the second would then fire from the state the first left
  (Assumption 12).
- **In a `vfs.FS` method**, the request comes after stages A to C and before
  the namespace preconditions of stage E, so a refusal comes in ADR 0007 §4's
  order of errors, and the event fires with the change that follows stage E.
  How the method makes the hold depends on the holds it has (Assumption 13):
  - `createEntry` (for `Create`, `Mkdir` and `Symlink`), `unlink` (for
    `Remove` and `Rmdir`), `Rename`, and `SetAttr` for the attributes it sets
    after the size, each hold `FS.mu` for writing from stage C to their
    change. The request is made at stage D, in that hold, which is exclusive
    for a machine that `FS.mu` serializes.
  - `Lookup`, `ReadDir` and `Read` make their checks holding `FS.mu` for
    reading. Under that hold they may fire a machine with a serializing lock
    of its own, held exclusively and taken where ADR 0014 §4's lock order
    allows, since the read hold keeps what the request reads. A machine that
    `FS.mu` serializes needs `FS.mu` for writing: such a method either holds
    it for writing from stage C, or takes it for writing once its read hold
    ends, resolves its handles and asks the policy again there (stages C and
    D), and fires only on that second permit.
  - `Write` and `truncate` check under a read hold of `FS.mu`, release it, and
    apply their change in a later hold, after the budget's wait or under the
    commit gate (#101). The permit from that check does not carry over. A
    transition tied to their change fires in a later exclusive hold, in which
    the owner resolves its handles and asks the policy again, and the side
    effects its ADR ties to the transition follow in that hold (ADR 0014 §3).
    `truncate` already has such a hold: under the commit gate, `openFile.mu`
    and `FS.mu` held for writing, it resolves its handle again before its
    change (ADR 0014 §6). `Write`'s change spans two holds, since
    `bufferWrite` copies the bytes under `openFile.mu` before it takes
    `FS.mu` for writing to set the size. So the ADR of a transition tied to
    it says which hold carries the verdict, the `Fire` and the side effects,
    or exempts it. Whether every check moves to where its change lands is
    #101's question; this settles it only for a security-relevant
    transition.
- **On a refusal**, the owner fires nothing, makes none of the side effects
  tied to the transition, and returns the policy's error unchanged: for
  `Unix()`'s rows, the bare `vfs.Status` (ADR 0014 §5).
- **On a permit**, it checks its other guards (ADR 0014 §3), fires, and makes
  the side effects in the same hold. A `Fire` that fails then is an illegal
  transition, logged at `Error` and returned (ADR 0014 §3, §9).
- **The policy** is the owner's installed one, held in one field as `blobfs`
  holds `f.perm` (ADR 0014 §6). The owner's constructor sets it; nothing else
  writes it; and a test in the owner's package may replace it after
  construction and before first use. In `blobfs`, `Unix()` stays in front of
  whatever is installed (ADR 0014 §6). The transition's ADR names the field
  (Assumption 14).
- **A widening transition that no caller causes** has no caller for a policy
  to judge, and ADR 0014 §5's `Request` has no form for it. Its ADR does one
  of two things. It exempts the transition (§4), saying what decides it
  instead and what checks that, such as a TLA+ spec. Or it extends
  ADR 0014 §5 under that section's *Growth* rule with a request the owner can
  make, and links the transition as above (Assumption 15).
- **Cost.** A policy takes no lock and waits for nothing (ADR 0014 §4, §5),
  so asking it inside the hold changes no lock order. Where the call already
  makes the request at stage D in an exclusive hold, the link adds nothing to
  it.

**Logs** (Assumption 16). A refusal is the caller's answer, not the server's
fault. The owner makes no record of it at `Info` or above. If it makes one at
`Debug`, the record carries ADR 0014 §9's keys `machine`, `from` and `event`,
and the policy's error under `err`, and no `to`, since nothing moved. A policy
still logs nothing.

**Specs.** A spec that models such a machine conjoins the policy's predicate,
under its rule-4 name (`UnixAllows` for `Unix()`, vocabulary V7), with the
event's action, so the action is enabled only where the policy permits. A
refusal is no step of the machine, and leaves its variable as it was. Whether
the machine is specified in TLA+ at all is its ADR's question under
ADR 0013 §1, which gives permission rules to exhaustive tests of the policy
and to review. This section adds no name to `docs/vocabulary.md`.

**The machines ADR 0014 names**, at `2d7ff34`:

- **The mount mode** is security-relevant, since `mutable()` and `Access`'s
  mask read it. Its one edge, `Diverge`, only narrows, and a store's answer
  causes it, so it needs no policy. An edge added later that leaves
  `ReadOnly` or `Diverged` widens, and is linked. Its one transition is made
  in `commitLocked`, which runs holding `FS.mu` for writing, or from
  `initEmpty` before the FS is shared, so the lock such an edge would need is
  already held exclusively.
- **Root-pointer knowledge and the lifecycle machines**: their ADRs
  (ADR 0014 §8) apply this section. Root-pointer knowledge is
  security-relevant if the ADR for #5 has `mutable()` read it. `ProbeOurs`
  and `ProbeUnchanged` would then widen, with no caller causing them, so
  that ADR would exempt them or extend ADR 0014 §5, as above.
- **Link counts** are not security-relevant. `relink`'s changes only follow
  the removals and renames that `OpRemoveEntry` and `OpRename` allow, and run
  after stage E in the hold of `FS.mu` in which that check is made
  (ADR 0017 §2).
- **E1 to E10** have no security-relevant transition. No policy's request or
  gate reads their state, none records authority a caller holds, and nothing
  about who a caller is guards any of their transitions. The duplicate
  request cache (E2) does tell connections apart, but only to give a
  connection the reply to its own call (ADR 0002 §2), which grants it nothing
  its call did not.

### 6. The test surface

**For a PR that adds or changes a security-relevant transition** that a
caller's request causes, in the owner's package, when the owner holds its
policy in a field a test can replace (§5). An owner without such a field
gets its tests from the transition's ADR instead of T1 and T2
(Assumption 17).

- **T1.** The test installs a recording policy that allows each request the
  call makes before the transition's own, and refuses that one with a status
  no row of `Unix()` returns, such as `vfs.ErrNXIO`, as ADR 0014 §11's W2
  does. It picks
  the transition's request out by its place in the list of requests that the
  transition's ADR gives for the call. The call returns that status. The
  machine's `State()` is unchanged, none of the side effects tied to the
  transition has happened, and the policy has recorded exactly the requests
  that ADR lists for the call, up to and including the transition's own.
- **T2.** With a policy that allows everything, the same call makes the
  transition: `State()` is the edge's `To`. Its objects are of the type the
  operation needs, as ADR 0014 §11's safety rule asks of `blobfs`.
- **T3.** A refused request makes no log record at `Info` or above.

**What only review can show:**

- that a security-relevant transition's policy is asked, and its event fired,
  in one hold that holds the machine's serializing lock exclusively (§5);
- that no path fires the event without asking;
- that every `Fire` on an `Atomic` with a security-relevant transition holds
  the lock its doc comment names, exclusively.

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required, except where it says otherwise. The owner's decisions are in
*Context*.

1. **Status Accepted, and Issue #114**, under the owner's 2026-10-10 words
   (decision 3), as ADRs 0015 to 0017 were. #114 is the tracking issue for
   the instruction ADR 0014 records, which this ADR carries on. The rules are
   the owner's (decisions 1 and 2); the form of this ADR, and every call
   below, are the architect's.
2. **A new ADR and a register, not a revision of ADR 0014** (*Context*).
   `docs/adr/README.md` corrects in place only a restatement whose fact has
   moved, and keeps a decision append-only. Making a fixed scope standing is
   a new decision, so it is a new ADR, and ADR 0014's text stays as the
   record of what was decided on 2026-10-12. A list that grows with every
   exemption is not a decision, so it is a register, as the README's index is
   a list of ADRs: kept in an ADR, every exemption would revise that ADR and
   stack another `Revised` line on it. ADR 0014 is not superseded
   whole, because its APIs, its wiring and its tests stand, and the code and
   the vocabulary cite them.
3. **The third rule's citation** (decision 2). The owner accepted a text that
   cites "ADR 0014 §12", where the draft held the definition and the link.
   This ADR holds them in §5. So CLAUDE.md's two citations of ADR 0014 §12,
   and its "ADR 0014 §1 lists the current ones", no longer point where they
   did, and the session, which edits CLAUDE.md, updates them. Their meaning
   is unchanged, and this ADR claims no approval of the new citations.
4. **Read, not run** (*Context*). Nothing a client or a deployment sees
   changes, so this is not breaking and needs no `CHANGES` entry.
5. **What "new and changed code" means** (§3). The approved wording does not
   say. A change counts when it alters the state or the decision itself: its
   states, events, transitions or writers, or what it allows. So a fix that
   changes how a flag moves cannot avoid the rules, and a refactor that only
   moves code cannot force a conversion. The owner's ruling that `reviewer`
   checks code "that a change adds or alters" reads the same way
   (decision 2). Such a change takes the whole state, since the state rule
   has every write go through the machine, and half a machine would leave
   writes that bypass it.
6. **What the rules reach** (§2). The state rule's "named states" leaves out
   values and caches. The policy rule's "whether a caller may do something"
   leaves out checks of whether a request is well formed and of what it
   names, which ADR 0014 §6 already puts in stages B and E, apart from the
   gate in stage A and the policy in stage D; ADR 0014's Assumption 18
   already kept the namespace preconditions and the validators out.
   ADR 0014 §1's list of other things left as they were is sorted on those
   lines. Where each falls is the architect's reading of the rules' words,
   and nothing on either side is exempt.
7. **The register** (§4). One list, kept whole, is what makes "the register
   lists the current ones" true, so an exemption that any ADR grants is
   listed there. Numbers are never reused, as ADR numbers are not, and a
   revoked row stays, as a superseded ADR does. A row restates its ADR rather
   than replacing it, so the ADR governs where they differ. An exemption
   whose reason a change removes is restated or revoked by that change's ADR,
   so that none outlives its reason.
8. **E1 to E10** (§4). ADR 0014 left them as they were, each with a reason
   and no rule named. Each is given the rule its reason answers: the state
   rule for the machines and the state, and the policy rule for the flag
   checks and the clamp. By §2's terms the policy rule does not reach those
   two at all; they stay listed so that the list is the one ADR 0014's
   decision 1 accepted. E8's reason now says what `cacheBytes` does, since it
   saturates rather than refuses, and review found "validators" too narrow
   for it. Their code is named as it stands at `2d7ff34`.
9. **E11 and E12** (§4). ADR 0014 §6 and §7 keep the mount gate and
   `ACCESS`'s read-only mask in `blobfs`, and the policy rule names "an
   access mask" and "a gate on an operation", so without these two ADR 0014
   would break the rule. They are recorded rather than moved into `policy`:
   both judge the mount, not the caller (ADR 0014, Assumption 21), and
   `policy` cannot name the mount mode's type without an import cycle. The
   session reported no ruling of the owner's on them (decision 2). They are
   the only exemptions this ADR adds to ADR 0014's ten, and it grants none to
   the three things *Not yet moved*, which the approved wording leaves to
   #114's plan.
10. **What is security-relevant** (§5). The approved wording leaves the
    definition to this ADR. It is based on the owner's "security or related
    to authorisations of some kind": a machine whose state a policy's request
    or a gate reads, or which records authority; a transition a caller causes
    in such a machine, or asks for under a decision about who may have it;
    and one that widens what callers may do. The owner's 2026-10-24 text
    links per transition, as this does (decision 2). Two things are left out
    on purpose:
    - a transition that only follows an allowed change, in a machine that is
      not security-relevant, such as one of `FS.dirty`, a link count or an
      open-file entry. Its change's check already guards it. Counting it in
      would make almost every piece of state security-relevant, and its
      question, where that check is made, is #101's;
    - a transition that no caller causes and that only narrows, such as
      `Diverge`. It fails safe, and there is no caller to judge.

    The locks, leases and share reservations of §5 are illustrations: no
    such machine exists.
11. **The link is a convention, not an API** (§5). An owner asks its policy
    where it fires, as ADR 0014 §6's methods ask theirs at stage D. So `fsm`
    and `policy` stay as ADR 0014 §2 and §5 pin them, and no code or test
    changes. It is enforced by T1 to T3 and by review, not by construction;
    *Alternatives considered* gives the forms that would enforce it, and why
    each lost.
12. **One exclusive hold** (§5). A verdict is about the state and the
    attributes as they stood when it was given. Holding the machine's
    serializing lock exclusively from the verdict to the `Fire` keeps the
    state, and holding what guards the request's inputs keeps the
    attributes. A read hold keeps the inputs but not the state: two readers
    could each be permitted against one state, and both fire. An `Atomic`
    lets one `Fire` race another, so one with a security-relevant transition
    serializes every `Fire` on one lock, which leaves its readers lock-free.
    The mount mode's one transition is already made holding `FS.mu` for
    writing, or before the FS is shared. Review found the draft silent on
    whether the hold was exclusive.
13. **Read holds and later holds** (§5). The methods are sorted by the holds
    ADR 0014 §6 gives them and the code at `2d7ff34` makes. For a read hold,
    the verdict is made again under the write hold, since nothing a read hold
    decided says anything about the state once that hold ends. For a later
    hold, checking again where the change lands is what #101 asks of every
    change, here asked only of a security-relevant transition. `Write`'s two
    holds are left to the ADR of any transition tied to its change, since no
    such transition exists, and any answer reshapes `bufferWrite`.
14. **The policy field** (§5). `f.perm`'s seam (ADR 0014 §6, Assumption 22)
    is the one the repository already has, and it is what lets W2 and W3,
    and T1 and T2, show that the check goes through the policy. An owner with
    no constructor, or no field to hold a policy, cannot be tested that way,
    so its transition's ADR says how it is tested instead.
15. **A widening transition no caller causes** (§5). ADR 0014 §5 makes a
    request out of an `Op` and a `Caller`, so a step a store's answer causes
    has no request to make. Its ADR either exempts it, which the rules'
    fourth part provides for, or makes a request it can make, under
    ADR 0014 §5's *Growth* rule. Review found that the draft implied a policy
    existed.
16. **A refusal is not logged above `Debug`** (§5). A client can send refused
    requests at will, and a record of each at `Info` or above would let it
    fill the log, as ADR 0016 Assumption 9 weighed for its own record.
    ADR 0014 §9's keys are reused so that a record that is made reads like a
    transition's. An audit record is not decided (*What this does not
    decide*).
17. **T1 to T3** (§6) carry W2 and W3 over from a method to a transition.
    `vfs.ErrNXIO` serves for the reason it does in W2: no row of `Unix()`
    returns it, so it can only be the recording policy's. T1 refuses the
    transition's own request, so that the requests the call makes before it
    pass and the transition's check is the one exercised; refusing the first,
    as W2 does, would stop the call before it. It picks that request by its
    place, not by its `Op`, so that it covers a transition whose request
    repeats an earlier `Op`, as §5's second check does. Review found the
    draft's T1 impossible to meet when the call makes another request first.

## Alternatives considered

- **Revising ADR 0014 in place**, as the draft did. It rewrote an Accepted
  decision, and every exemption would have revised ADR 0014 again
  (Assumption 2).
- **Keeping the list in ADR 0014 §1, under an exception in
  `docs/adr/README.md`.** It would still edit an Accepted ADR for every
  exemption, either stacking a `Revised` line on it or excusing one file from
  the README's rule for them, and the scope change itself would still be a
  decision rewritten in place.
- **Superseding ADR 0014 whole.** Its APIs, its wiring and its tests stand,
  and the code and the vocabulary cite them; a superseded status would tell a
  reader otherwise.
- **An edge that names its policy, which `Fire` asks.** A request is the
  call's, not the edge's: its `Object`, `Dst`, `UID` and `GID` come from the
  call, so `Fire` would need the whole request, and `fsm` would import
  `policy` and, through it, `vfs`, where ADR 0014 §2 gives it the standard
  library alone. Asked inside `Fire`, the policy would answer after stage E's
  preconditions, or the event would fire before them with no way back, and
  either breaks ADR 0007 §4's order of errors. In `Atomic`'s loop it could be
  asked more than once.
- **A guard function passed to `Fire`.** It needs no import, but the order of
  errors and the loop are as above, and it enforces nothing the convention
  does not unless the table also marks the guarded edges, which is the
  alternative above again.
- **A permit value that `Fire` requires**, which only a policy's allow could
  make. Within one package anything can make one, a kept permit could be
  spent in a later hold, and no machine needs one yet. If review finds a
  security-relevant `Fire` without its check, this is the next step.
- **A read hold for the verdict and the `Fire`.** Two holders could be
  permitted against one state, and a `Machine` is not safe for concurrent
  use (ADR 0014 §2).
- **Linking every transition of a security-relevant machine.** A transition
  that no caller causes and that only narrows has no caller to judge, and the
  mount mode's `Diverge` would need a policy that always allows. The owner's
  2026-10-24 text links per transition (decision 2).
- **Counting every transition that an allowed change causes.** `FS.dirty`,
  link counts and buffers would each need a request of their own for a change
  already checked, and #101 would become a breach of §5 before its own
  decision (Assumption 10).
- **Keeping ADR 0014's fixed list.** Decision 1 makes the rules standing.
- **Treating code that has not moved as exempt until it moves.** It would keep
  the old blanket as an exemption that no ADR names, which decision 1 rules
  out.

## Consequences

- New and changed state, and new and changed decisions about who may act,
  follow these rules unless an ADR exempts them, and the register lists every
  exemption.
- An exemption costs an ADR and a register row, and revises no ADR.
- A change to state that has not moved, such as `FS.dirty`, can grow into its
  conversion, or wait on an ADR that exempts it (§3).
- A security-relevant transition is checked and fired in one exclusive hold,
  after the checks of stages A to C and before its preconditions, so a
  refusal never leaves a machine moved (§5). A method that checks under a
  read hold, or changes in a later hold, pays for a second check or a write
  hold when it fires one.
- The link is enforced by T1 to T3 and by review, not by `fsm`
  (Assumption 11).
- ADR 0014 stays the record of its own decision, and its §1 is read with this
  ADR.

## What this does not decide

- Whether each thing *Not yet moved* (§3) is converted or exempted.
- NFSv4 state. Its open state, share reservations and locks stay in
  `internal/nfs` (`vfs.Pinner`'s doc comment), so a security-relevant machine
  there whose request reads state behind `vfs.FS` cannot hold the backend's
  lock (§5); its ADR decides where the check is made.
- A request for a caller that is not a `vfs.Caller` acting on a filesystem
  object, such as one at the RPC layer. The ADR of the first machine that
  needs one extends ADR 0014 §5, under its *Growth* rule.
- The hold that carries a transition tied to `Write`'s change (§5), and
  whether every check moves to where its change lands (#101).
- Root squash and the `AUTH_SYS` trust boundary (#6, #90, #95), which
  ADR 0014 leaves open too.
- An audit record of refusals.
- Limits on what one caller may hold or use up, such as the budget,
  connections and calls in flight. They decide nothing about who may act, and
  §5 does not cover them.

## Sources

Nothing was fetched for this ADR. It cites only this repository's own code and
documents.
