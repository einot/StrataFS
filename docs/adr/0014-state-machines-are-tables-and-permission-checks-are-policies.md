# 0014. State machines are explicit tables, and permission checks are policies

**Status:** Accepted
**Date:** 2026-10-12
**Revised:** 2026-10-24 — the owner's decision 4 (*Context*) makes the rules
standing. §1 now states them for new and changed code, with exemptions only by
ADR. It keeps its table; makes the ten places it left as they were exemptions
E1 to E10, each with its reason and now its code; adds E11 and E12 for the
mount gate and `ACCESS`'s read-only mask, which §6 and §7 already keep out of
`internal/policy`; replaces "Everything else keeps its present form" with what
the rules do not reach and what has not moved yet; and says how an ADR grants
or revokes an exemption. A new §12 says which transitions are
security-relevant and links each to a policy by where its owner asks, with no
change to `fsm` or `policy`. §3 and §9 point to it, and §11 adds T1 to T3 and
three review items. *Context* gains decision 4 and the commit the revision was
checked against, its *What this constrains* now covers every package and §12,
and it says the revision changes no code; the preamble to the Assumptions and
Assumption 1 follow, and Assumptions 36 to 46 are new. *Consequences* replaces
"Every later machine and permission rule follows this ADR" and gains entries,
*What this does not decide* widens its NFSv4 entry and gains entries, and
*Alternatives considered* and *Sources* gain entries. §2, §4 to §8 and §10 are
unchanged, and so is every test the first version pins.
**Issue:** #114

## Context

**The instruction.** The repository owner's instruction, as the session
recorded it: "make sure that the code uses proper FSM state machines
everywhere where there is anything that has a state or state transitions. Use
policy pattern everywhere when the code needs to check authorisation or if
some operation is allowed." #114 is a tracking issue for it, as #109 is for
the instruction behind ADR 0013.

**The owner's decisions.** Four, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. **Scope.** On 2026-10-10 the owner said: "Go with architect's
   recommendations. I took a look on the report and it was well argued." That
   accepts the narrower set: FSMs for the mount mode, root-pointer knowledge
   and lifecycle; a lighter form for link counts; policies for every
   caller-permission check, ACCESS and the mount gate; and the ten places left
   as they are.
2. **Four choices**: `internal/fsm` rather than a hand-written switch;
   `internal/policy` rather than code in `blobfs` or `vfs`; the status
   Accepted; and the Issue line. The owner said: "Otherwise, follow
   architect's and your own recommendations." So each is delegated to the
   architect's recommendation, on 2026-10-10. The Issue line is #114, a
   tracking issue for the instruction, as #109 is for ADR 0013.
3. **Breaking changes.** The owner said: "We don't have any clients yet so any
   breaking changes are allowed until release."
4. **The rules are standing.** On 2026-10-22 the owner said: "1. Propose a
   wording that will keep the idea that state machines ought to be used for
   any stateful code unless there's a good reason to do it otherwise. All
   exceptions should be backed with a relevat ADR. 2. Same applies here.
   Whenever the state machine is security or related to authorisations of
   some kind the transitions should be linked to a policy engine unless
   there's a solid reason to do it differently. Exceptions need to be backed
   up with a relevant ADR. 3. Use some generalized wording that would allow
   exceptions whenever architect has a good reason for it as I wrote
   earlier." Of the wording the session then proposed, the owner said: "Go
   ahead with that wording." The session gave this revision that wording, as
   the text it adds to CLAUDE.md under "State machines and policies". §1
   restates its rules, and §12 is the link the third of them leaves to this
   ADR. "Same applies here" answers a question of the session's that this
   revision was not given, so the revision follows the approved wording, not
   a reading of that question.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `a607410`, the commit this ADR's branch was cut from
(Assumption 30). The revision's own statements were checked by reading it at
`2d7ff34` (Assumption 36). Code is cited by symbol.

- The mount mode is two fields of `FS`:
  - `readOnly`, a `bool` that `New` sets from `Config.ReadOnly`;
  - `diverged`, an `atomic.Bool` since #48's fix (`bdc7b58`). `commitLocked`
    sets it on `store.ErrPrecondition`, and `mutable()` reads it holding no
    lock.
- Every check of what a caller may do is inline in `internal/blobfs/fs.go`:
  - `permitted` and `inGroup`;
  - `readable` (both steps of `Read`), `Write`, `truncate`, `Lookup`,
    `ReadDir`, `createEntry`, `unlink` and `Rename`;
  - `SetAttr`'s ownership and time rules;
  - `createEntry`'s requested uid and gid;
  - `Access`.
- Nothing outside `fs.go` decides it. `sunrpc`'s `Cred.Root` is never called.

**The issues it bears on.** They were filed from the inventory behind the
scope, and are cited by number and title as the session gave them
(Assumption 32).

- Rows §11 leaves unpinned:
  - #93 "Owners cannot read or write their own files when the mode denies
    them"
  - #94 "The sticky bit on a directory is not honoured"
  - #97 "ACCESS grants what the operations then refuse"
  - #100 "Any caller with write permission can set a file's times to any
    value"
  - #69 "SETATTR with a size, and WRITE, accept a symlink's handle"
- Behaviour §6 keeps as it is:
  - #96 "A refused SETATTR can keep part of its change, uncommitted"
  - #98 "CREATE, MKDIR and SYMLINK apply a requested group from any caller"
  - #99 "A guarded SETATTR checks its guard outside the filesystem's lock"
  - #101 "Write and truncate check permission before a wait, not where they
    apply the change"
- Machines §8 leaves to other decisions:
  - #5 "A transient commit failure is treated as a second writer"
  - #82 "A clean shutdown answers calls after its last commit, and then loses
    them"
  - #92 "A failed Accept stops the server without its final commit"
  - #50 "Removing a directory never deletes its inode"
  - #102 "A diverged mount retries its commit and logs an Error every
    interval"

**What ADR 0013 left to this ADR.** ADR 0013 §5 made `docs/vocabulary.md` the
one set of names that Go machines, policies and specs share. It reserved for
this ADR:

- the mount mode's names (V1);
- the policy names (V7);
- settling `UnixAllows` against rule 4 (V7);
- the Go mechanism for a machine (ADR 0013, *What this does not decide*).

**What this constrains.**

- `internal/fsm` and `internal/policy`, which it adds;
- how new and changed code in every package expresses state and decisions
  about who may act (§1), and how a security-relevant transition asks a
  policy (§12);
- `docs/vocabulary.md`.

The PR that carries this ADR also carries the two packages and their tests
(§2, §5, §11), and changes no other code. Two later PRs wire them in: one
converts the mount mode (§7), and one has `blobfs` evaluate the policy (§6).
The revision of 2026-10-24 changes no code and no test.

## Decision

### 1. Scope

**The rules.** By the owner's decision 4, they bind new and changed code.
Here they are in the wording the owner approved, with its references made
local:

- **State is a machine.** Code that holds state and moves it between named
  states expresses that state as an explicit transition table run by
  `internal/fsm` (§2, §3). That covers a mode, a lifecycle, a connection, a
  protocol phase, or anything whose legal next step depends on where it is
  now. Every write of the state goes through the machine, and an illegal
  transition is an error, never a silent overwrite.
- **Who may act is a policy.** Every decision about whether a caller may do
  something is a pure policy in `internal/policy` (§5), not an inline
  condition. That covers a permission check, an ownership or time rule, an
  access mask, and a gate on an operation.
- **A security transition asks a policy.** When a machine is
  security-relevant, or one of its transitions depends on an authorization
  decision, that transition is linked to a policy. The policy's verdict
  guards the transition, and the event fires only if the policy permits it.
  §12 defines how the two connect.
- **Exceptions are decided, not assumed.** The architect may exempt code from
  any of these rules when it has a good reason. Examples: a hot path where a
  table costs more than it catches, state that another mechanism already
  pins and tests, or a flag with a single rule. Each exemption is recorded in
  an ADR that names the code and the reason. *Exemptions*, below, lists the
  current ones. Code without an exemption follows the rules.

The rest of this ADR calls the first three the state rule, the policy rule
and the link.

**What they reach.**

- A *caller* is whoever a request comes from: the `vfs.Caller` of a
  filesystem call, or, at another layer, the credential a call carries or the
  operator who gives a command. A step the server takes on a store's answer,
  a signal or a timer comes from no caller, even when a call led to it.
- The state rule reaches state that has named states. A count, a size, an
  epoch or a time is a value, not a named state. A cache, which may gain or
  lose any entry at any step, has no state of its own.
- The policy rule reaches a decision that allows or refuses a well-formed
  request, or for `ACCESS` says what it would allow:
  - by who the caller is or what it holds: a permission check, an ownership
    or time rule, an access mask; or
  - by what the mount allows, whoever asks: a gate on an operation, as
    `mutable()` is at §6's stage A, and as `ACCESS`'s read-only mask is
    (E11, E12).

  A check of whether a request is well formed (§6's stage B), or of what it
  names (stage E), says what the operation is, not who may perform it, and
  the policy rule does not reach it (Assumption 18).
- §12 says what the link reaches.

**New and changed code.** The rules bind a change that adds state or a
decision about who may act, or that changes how existing state moves (its
states, its events, its transitions, or the places that write it) or what an
existing decision allows. Such a change brings that state, or that decision,
under the rules whole, so every write of the state goes through its machine
from then on. A change that only moves, renames or reformats such code, or
edits the code beside it, does not (Assumption 37).

**Existing code** moves over as #114 plans. This ADR plans §6 and §7, and
gives §8's machines to their own ADRs. Until code moves, it is not exempt by
that fact: a change to it, as above, brings it under the rules, unless an ADR
exempts it first.

**What the first version converts.** Decision 1, in the owner's words: "Go
with architect's recommendations. I took a look on the report and it was well
argued." It accepted the narrower set that this table and E1 to E10 give.

| Item | Form | Why | Decided in |
|---|---|---|---|
| The mount mode (`FS.readOnly`, `FS.diverged`) | an `fsm.Atomic` | it is read without a lock ahead of every change, and the table states that a read-only mount never diverges and that `Diverged` is terminal | §7 |
| Root-pointer knowledge | an `fsm.Machine` under `FS.mu` | #5 is a missing state, `InDoubt` | §8 |
| The server, connection and process lifecycle | machines | #82 and #92 are missing transitions | §8 |
| Link counts | the lighter form, one transition function | the counts are arithmetic, and #50 is an arithmetic defect | §8 |
| Every check of what a caller may do: `permitted`, `inGroup`, the per-operation checks, `SetAttr`'s ownership and time rules, a requested uid at creation | a policy | one table of rules, tested exhaustively, with an extension point | §5, §6 |
| `ACCESS` | `policy.AccessBits`, with the mount's mask | `ACCESS` becomes derivable from the same rules (#97 later) | §5, §6 |
| The mount gate, `mutable()` | a predicate over the mount mode | V1's `Mutable`: nil exactly when the mode is `Writable` | §7 |

Link counts are values, not named states, so the state rule asks no table of
them: §8's lighter form is this ADR's decision for them until pins add
states. The mount gate, and `ACCESS`'s read-only mask, stay in `blobfs`,
which E11 and E12 record.

**Exemptions.** Each names its code, the rule it is exempt from, and the
reason. E1 to E10 are the ten places decision 1 accepted leaving as they
were, with the reasons the first version gave, which are mine
(Assumption 1); their rules and code are named as they stand at `2d7ff34`
(Assumption 39). E11 and E12 record forms that §6 and §7 already decide, so
that this ADR keeps its own rules (Assumption 40). None of them is exempt
from the link, which none of them needs (§12).

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
- **E8. The flag validators**: `chunkSizeBytes` and `cacheBytes`
  (ADR 0008), `commitInterval` (ADR 0009) and `ownerID` (ADR 0010), in
  `cmd/strata/main.go`; from the policy rule. They are pure checks that those
  ADRs pin.
- **E9. `-check`**: `runCheck` and its probes, in `cmd/strata/check.go`;
  from the state rule. It is a linear script of probes.
- **E10. The `MaxFileSize` clamp**: in `(*FS).Write`, in
  `internal/blobfs/fs.go`, which shortens a write that crosses
  `FS.maxFileSize`; from the policy rule. A clamp is not an allow or a deny
  (ADR 0007 §4).
- **E11. The mount gate**: `(*FS).mutable`, in `internal/blobfs/fs.go`; from
  the policy rule's "in `internal/policy`". It judges the mount, not the
  caller, so it has no `Request` to read. Its one input is the mount mode,
  whose type `blobfs` keeps unexported (§7), and `policy` could name that
  type only by importing `blobfs`, which imports `policy` once §6 is done. It
  fails closed, as a policy does (Assumption 24). Its form is §7's.
- **E12. `ACCESS`'s read-only mask**: in `(*FS).Access`, in
  `internal/blobfs/fs.go`, which on a read-only mount drops
  `vfs.AccessModify`, `vfs.AccessExtend` and `vfs.AccessDelete` from what the
  mode bits give; from the policy rule's "in `internal/policy`". The mask is the mount's, not the caller's, and the
  caller's part is `policy.AccessBits` (Assumption 21). Its form is §6's.

**Granting and revoking** (Assumption 41):

- Only an ADR grants an exemption. It names the code, by symbol and file; the
  rule it is exempt from; and the reason. The PR that carries it adds the
  exemption to the list above under the next E number, citing that ADR, with
  a `Revised` line in this ADR. E numbers are never reused.
- An ADR revokes an exemption by saying so, and either moves the code in the
  same PR or says which change will. The entry stays above, marked "revoked
  by ADR NNNN", with a `Revised` line in this ADR.
- An exemption holds while its reason does. A change that removes the
  reason, such as one that supersedes the ADR an exemption cites, restates
  the exemption in its own ADR with a reason that still holds, or revokes it.

**What the rules do not reach.** These are the things the first version kept
as they were, apart from the three under *Not yet moved*. None is an
exemption. A change that gives one of them named states, or makes its answer
turn on who asks or on what the mount allows, brings it under the rules
(Assumption 38).

- `validName`, the empty-write rule and `Symlink`'s empty-target check:
  whether a request is well formed.
- The namespace preconditions: what a request names, against the namespace
  (Assumption 18).
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

**Not yet moved.** These are in the state rule's reach, are not exempt, and
are not among the conversions of §6 to §8:

- `FS.dirty`: a flag that every change sets and a successful commit clears.
- `FS.pruning`: set by `schedulePrune` and cleared by its goroutine, both
  under `FS.mu`. CommitProtocol refines it into three phases (`pruning[m]`,
  vocabulary V3).
- The open-file entry (#42): an `openFile`'s life in `FS.open`, which it
  enters only through `getOpen` and leaves only through `dropOpen`.

Each moves when a change reaches it, as *New and changed code* says, or as
#114 plans if that comes first, unless an ADR exempts it.

### 2. The `fsm` API

Pinned:

```go
package fsm // standard library only

type Edge[S, E comparable] struct {
    From S
    On   E
    To   S
}

type Table[S, E comparable] struct{ /* unexported: name, edges in order, map from (from, on) to to */ }
func NewTable[S, E comparable](name string, edges ...Edge[S, E]) *Table[S, E]
func (t *Table[S, E]) Name() string
func (t *Table[S, E]) Next(from S, on E) (S, bool)
func (t *Table[S, E]) Edges() []Edge[S, E]

var ErrIllegal = errors.New("fsm: illegal transition")

type IllegalError[S, E comparable] struct {
    Machine string
    From    S
    On      E
}
func (e *IllegalError[S, E]) Error() string
func (e *IllegalError[S, E]) Unwrap() error

type Machine[S, E comparable] struct{ /* unexported: a noCopy field, the table, the current state */ }
func NewMachine[S, E comparable](t *Table[S, E], initial S) *Machine[S, E]
func (m *Machine[S, E]) State() S
func (m *Machine[S, E]) Fire(on E) (from, to S, err error)

type Atomic[S ~uint32, E comparable] struct{ /* unexported: the table, a sync/atomic.Uint32 */ }
func NewAtomic[S ~uint32, E comparable](t *Table[S, E], initial S) *Atomic[S, E]
func (a *Atomic[S, E]) State() S
func (a *Atomic[S, E]) Fire(on E) (from, to S, err error)
```

Every rule below is normative:

- **`NewTable`**:
  - It keeps its own copy of the edges, in the order given.
  - It panics if two edges share `From` and `On`, whatever their `To`. Tables
    are built once from constants, so a duplicate is a programming error.
    Edges that share only `From`, or only `On`, are fine.
  - It accepts any name, `""` included.
  - A table with no edges is valid. Without edges, write the type arguments
    out: `NewTable[S, E]("name")`.
- A `Table` never changes after `NewTable` returns, so it is safe for
  concurrent use.
- **`Name`, `Next` and `Edges`**:
  - `Name` returns the name `NewTable` was given.
  - `Next` returns `(To, true)` for an edge's `(From, On)`, and
    `(zero S, false)` for every other pair.
  - `Edges` returns a new slice on each call, in declaration order. It has
    length 0 for a table with no edges; whether that slice is nil or empty is
    unspecified. Changing it changes nothing in the table.
- **`NewMachine` and `NewAtomic`**:
  - They panic if `t` is nil.
  - They do not check `initial` against the table. A state that no edge
    names, such as a terminal or a read-only state, is a valid initial state.
- `State` returns the current state. Until a `Fire` succeeds, that is the
  initial state.
- A legal `Fire` is one where `Next(current, on)` is `(to, true)`. It sets the
  state to `to` and returns `(current, to, nil)`. A self-loop gives
  `from == to`.
- An illegal `Fire` changes nothing and returns `(current, current, err)`,
  where `err` is an `*IllegalError[S, E]`:
  - `Machine` is the table's `Name()`, `From` the current state, and `On` the
    event fired.
  - `Error()` returns exactly
    `"fsm: " + Machine + ": illegal transition from " + fmt.Sprint(From) + " on " + fmt.Sprint(On)`.
    A state or event with a `String` method prints by it.
  - `Unwrap` returns `ErrIllegal`, so `errors.Is(err, ErrIllegal)` holds.
- `Machine` takes no lock and is not safe for concurrent use. Its owner guards
  it with the lock that guarded the field it replaces, and its doc comment
  names that lock.
- `Atomic` is for state read without the lock its writers hold:
  - `State` is one atomic load.
  - `Fire` is a compare-and-swap loop. It loads the state, looks up the edge,
    and swaps only from the state it loaded. It retries when another `Fire`
    changed the state in between.
  - So every `Fire` is linearizable: a legal one at its successful swap, an
    illegal one at the load that found no edge. No transition is lost or
    applied twice.
  - Its `S ~uint32` satisfies the `comparable` that `Table` asks of `S`, since
    every type in its type set is an integer type, and so strictly comparable
    (*Sources*).
- Neither `Machine` nor `Atomic` is ever copied:
  - `Machine` holds a `noCopy` field: an unexported struct type whose pointer
    has no-op `Lock` and `Unlock` methods. It is a named field, not an
    embedded one, so `Machine` gains no `Lock` or `Unlock` method of its own
    (Assumption 33).
  - `Atomic` holds a `sync/atomic.Uint32`.
  - So `go vet`'s copylocks check reports a copy of either (*Sources*).
  - Every method has a pointer receiver.
  - The zero `Machine` and the zero `Atomic` are not usable; `NewMachine` and
    `NewAtomic` are the only constructors.
- `fsm` logs nothing, and exports nothing beyond the above.

### 3. Conventions for a machine

These bind owners; `fsm` does not check them.

- A state type starts at 1 (`iota + 1`), so its zero value means "never
  initialised".
- States and events have `String` methods with value receivers. `String`
  returns the bare name (`docs/vocabulary.md`, rule 2), or `"<type>(<n>)"`,
  `n` in decimal, for a value with no name.
- The table is a package-level variable declared next to the types, and named
  after its machine.
- The table's name, the string passed to `NewTable`, is the name of the field
  that holds the machine's state. That is also the TLA+ variable rule 3 names
  after that field (`"mode"` for `FS.mode`).
- Every write of the state goes through `Fire`. The `Machine` or `Atomic` is
  made once, by the constructor of what owns it, and is never reassigned.
- Guards belong to the caller: it fires an event only when the condition the
  event names holds. Side effects follow a successful `Fire`, under the same
  lock. When the condition is a policy's permit, §12 says where the owner
  asks for it.
- An illegal transition is an error, which the owner logs at `Error` (§9) and
  returns. It is never a panic: a panic under `FS.mu`, in the committer or in
  `New` would take down the mount or leave a lock held.

### 4. Locks, `-race` and cost

- **Locks.**
  - Neither package adds a lock. A `Machine` is guarded by the lock that
    guarded the field it replaces. An `Atomic` uses one atomic word. A policy
    takes no lock.
  - So the lock order stays as the `FS.mu` doc comment, ADR 0003 §3 and
    ADR 0012 §5 give it: the commit gate, then `openFile.mu`, then `FS.mu`.
  - The leaves stay: the budget's mutex, `knownMu` and the chunk cache's
    mutex (ADR 0013 §4 restates them; ADR 0004 §3).
  - `Fire` waits for nothing. `Machine`'s `Fire` is one map lookup, and
    `Atomic`'s retries only after another `Fire` has succeeded.
  - A policy waits for nothing either, so `blobfs` may evaluate one inside
    whatever lock it holds at that point, without changing any lock order.
- **Under `-race`.**
  - State read without its writers' lock is an `Atomic`; every other machine
    is read only under its documented lock.
  - A policy shares nothing between calls.
  - The Go memory model's atomic operations behave as if executed in one
    sequentially consistent order, and accesses that are all atomic are no
    data race (*Sources*).
- **Cost**, none of it measured (Assumption 29):
  - `Machine.Fire` is one map lookup. `Atomic.Fire` is a load, a lookup and a
    swap.
  - Neither allocates, except the `IllegalError` on the illegal path.
  - The mount mode's `State` is one atomic load, where `mutable()` now makes a
    plain load and an atomic load.
  - A policy check is a call through an interface, in place of inline
    comparisons.

### 5. The `policy` API

Pinned:

```go
package policy // imports strata/internal/vfs; standard library otherwise

type Perm uint32

const (
    PermExec  Perm = 1
    PermWrite Perm = 2
    PermRead  Perm = 4
)

type Object struct {
    Type     vfs.FileType
    Mode     uint32
    UID, GID uint32
}

func InGroup(c vfs.Caller, gid uint32) bool
func Permits(c vfs.Caller, o Object, want Perm) bool

type Op uint8

const (
    OpRead Op = iota + 1
    OpWrite
    OpTruncate
    OpChmod
    OpChown
    OpChgrp
    OpSetTimes
    OpLookup
    OpReadDir
    OpAddEntry
    OpRemoveEntry
    OpRename
)

type Request struct {
    Op     Op
    Caller vfs.Caller
    Object Object // what the call acts on; for OpLookup, OpReadDir, OpAddEntry and OpRemoveEntry the directory; for OpRename the source directory
    Dst    Object // OpRename: the target directory
    UID    uint32 // OpChown: the owner asked for
    GID    uint32 // OpChgrp: the group asked for
}

type Policy interface {
    Check(r Request) error
}

type Func func(r Request) error
func (f Func) Check(r Request) error

func All(ps ...Policy) Policy
func Unix() Policy
func AccessBits(c vfs.Caller, o Object) uint32
```

So `OpRead` is 1 and `OpRename` is 12. Terms used in the rules:

- "root" means `c.UID == 0`;
- "the owner" means `c.UID == o.UID`;
- "a directory" means `Type == vfs.TypeDir`. Every other `Type`, valid or
  not, is a non-directory.

Every rule below is normative:

- `InGroup(c, gid)` is true exactly when `gid == c.GID` or `gid` is in
  `c.GIDs`.
- `Permits(c, o, want)`:
  - It reads only the low nine bits of `o.Mode` (`o.Mode & 0o777`), so
    setuid, setgid, sticky and any higher bit change nothing.
  - If `want` has a bit outside `PermRead|PermWrite|PermExec`, it is false for
    every caller, root included.
  - For root, it is true unless `want` includes `PermExec`, `o` is not a
    directory, and `o.Mode & 0o111 == 0`.
  - For anyone else, it judges by exactly one class:
    - the owner's bits, `(Mode>>6)&7`, if `c.UID == o.UID`;
    - else the group's bits, `(Mode>>3)&7`, if `InGroup(c, o.GID)`;
    - else the other bits, `Mode&7`.

    It is true exactly when those bits include every bit of `want`. So
    `want == 0` is true for everyone.
  - For `want` in 0 to 7, this is `internal/blobfs/fs.go`'s `permitted()`,
    exactly.
  - For `want` outside that range, `permitted()` would give root true. No
    caller can reach that, since every caller passes 1, 2, 4 or 3. `Permits`
    fails closed there instead.
- `Unix().Check(r)` applies this table. In each row the first check that fails
  gives the refusal; nil means allowed.
  - `OpRead`: `ErrIsDir` if `Object` is a directory; `ErrAcces` unless
    `Permits(Caller, Object, PermRead)`.
  - `OpWrite` and `OpTruncate`: `ErrIsDir` if `Object` is a directory;
    `ErrAcces` unless `Permits(Caller, Object, PermWrite)`.
  - `OpChmod`: `ErrPerm` unless root or the owner.
  - `OpChown`: `ErrPerm` unless root or the owner; then `ErrPerm` if not root
    and `r.UID != Object.UID`.
  - `OpChgrp`: `ErrPerm` unless root or the owner; then `ErrPerm` if not root
    and `!InGroup(Caller, r.GID)`.
  - `OpSetTimes`: `ErrPerm` unless root, the owner, or
    `Permits(Caller, Object, PermWrite)`.
  - `OpLookup`: `ErrNotDir` unless `Object` is a directory; `ErrAcces` unless
    `Permits(Caller, Object, PermExec)`.
  - `OpReadDir`: `ErrNotDir` unless `Object` is a directory; `ErrAcces` unless
    `Permits(Caller, Object, PermRead)`.
  - `OpAddEntry` and `OpRemoveEntry`: `ErrNotDir` unless `Object` is a
    directory; `ErrAcces` unless `Permits(Caller, Object, PermWrite|PermExec)`.
  - `OpRename`: `ErrNotDir` unless `Object` and `Dst` are both directories;
    `ErrAcces` unless both `Permits(Caller, Object, PermWrite|PermExec)` and
    `Permits(Caller, Dst, PermWrite|PermExec)`.
  - Any other `Op`, 0 included: `ErrServerFault`, for every caller, root
    included.

  Each refusal is the bare `vfs.Status` (`vfs.ErrIsDir` and so on), never
  wrapped. `Check` reads only the fields its row names: `Op`, `Caller` and
  `Object` always; `UID` for `OpChown`; `GID` for `OpChgrp`; `Dst` for
  `OpRename`. The other fields change nothing. For every `Op`, and every
  request `blobfs` makes today, this is exactly the inline check it replaces,
  the rows §11 leaves unpinned included.
- `Unix` is a function, not an exported variable, so there is no package-level
  authority anyone can reassign. It returns a value that holds no state, and
  two calls decide alike. `policy` exports no variable at all.
- `Func(f).Check(r)` calls `f(r)` once and returns its result.
- `All(ps...)`:
  - It returns a `Policy` that asks each of `ps` in order, and returns the
    first non-nil error unchanged, asking none after it.
  - With no policies, it allows everything.
  - It keeps its own copy of `ps`, so changing the caller's slice afterwards
    changes nothing.
  - It panics if any of `ps` is nil, or is a `Func` whose value is nil. The
    first means a nil interface value: a non-nil `Policy` that holds a nil
    pointer is not caught, since catching it would need `reflect`, and a typed
    nil `*T` whose `Check` never dereferences it is a legitimate policy
    (Assumption 16). A nil policy is a programming error; caught at
    construction, it cannot surface as a panic inside a lock at request time.
- `AccessBits(c, o)` is the OR of:
  - `vfs.AccessRead`, if `Permits(c, o, PermRead)`;
  - `vfs.AccessLookup`, if `o` is a directory and either
    `Permits(c, o, PermRead)` or `Permits(c, o, PermExec)`;
  - `vfs.AccessModify|vfs.AccessExtend`, if `Permits(c, o, PermWrite)`;
  - `vfs.AccessDelete`, if `o` is a directory and `Permits(c, o, PermWrite)`;
  - `vfs.AccessExecute`, if `o` is not a directory, `o.Mode & 0o111 != 0`,
    and `Permits(c, o, PermExec)`.

  No other bit is ever set. It is `(*FS).Access` without the read-only mask
  and without `& want`.
- **Purity.** This covers `InGroup`, `Permits`, `AccessBits`, and the `Check`
  of `Unix()`, `All(...)` and `Func`:
  - each takes no lock, makes no store call and logs nothing;
  - each modifies nothing it is given, `Caller.GIDs` included;
  - each allocates nothing when it allows;
  - each decision is a function of its arguments alone, and is safe for
    concurrent use.

  A `Func` that a test writes may record what it is asked, under its own
  lock: `Func`'s `Check` adds nothing to what its function does.
- **Mapping.** A refusal is a `vfs.Status`, which `statusOf`
  (`internal/nfs/wire.go`) passes to the client unchanged. Refusals at the RPC
  level, credentials and versions, stay in `internal/sunrpc`.
- **Growth.** A field added to `Request` later is defined so that a request
  that leaves it zero means what the same request meant before the field
  existed. The ADR that adds it says what changes, if anything, for such a
  request.

### 6. Where `blobfs` evaluates policies

This is for the PR that wires the policy. It changes no behaviour.

- `FS` gains `perm policy.Policy`:
  - `New` sets it to `policy.Unix()`, and nothing else writes it.
  - A test may replace it after `New` returns and before first use, as
    ADR 0007 §7 allows for `maxFileSize`.
- `objectOf(n)` builds an `Object` from an inode's `Type`, `Mode`, `UID` and
  `GID`.
- **Stages**, in ADR 0007 §4's order of errors:
  - **A.** The mount gate, `mutable()`, in the methods that change the
    filesystem. No lock held. Errors: `ErrROFS`, `ErrStale`.
  - **B.** The arguments: names, the empty write, size and offset limits. No
    lock held. Errors: `ErrInval`, `ErrNameTooLong`, `ErrFBig`.
  - **C.** Resolving the handles. Held: `FS.mu`, for reading or writing as
    now. Errors: `ErrBadHandle`, `ErrStale`.
  - **D.** `f.perm.Check`. Held: the same hold, plus `openFile.mu` in `Read`'s
    step 2. Errors: `ErrIsDir`, `ErrNotDir`, `ErrAcces`, `ErrPerm`.
  - **E.** Namespace preconditions. Held: the hold of `FS.mu` that C and D
    ran in, which is for writing in the methods that change the namespace and
    for reading in `Lookup` and `ReadDir`. Errors: `ErrNoEnt`, `ErrExist`,
    `ErrNotEmpty`, `ErrIsDir`, `ErrNotDir`, `ErrInval`, and `ReadDir`'s
    `ErrBadCookie`.

  `Read`, `Lookup`, `ReadDir` and `Access` have no stage A. `Read`'s step 2
  runs C and D again. `truncate` resolves its handle a second time, under its
  `openFile.mu` and `FS.mu` once it holds the commit gate, and that resolve
  can still answer `ErrStale` (ADR 0012 §2); no policy is asked there, and
  whether one should be is #101's question.
- Three exceptions stay as they are:
  - `Symlink`'s empty-target `ErrInval` comes before stage A;
  - an empty write returns success after stage A;
  - `SetAttr` checks and applies each attribute in turn (the size through
    `truncate`, then mode, uid, gid and times), until the ADR that settles
    #96 decides otherwise.
- **The requests each method makes.** Each `Object` is built from the inode as
  it stands at that check.
  - `Read`: `OpRead` on the file in step 1, and again in step 2 when the file
    has an `FS.open` entry.
  - `Write`: `OpWrite` on the file, once, before the budget's wait, as now.
    Whether it is repeated where the write lands is #101's question.
  - `SetAttr`:
    - first `OpTruncate`, in `truncate`, for `sa.Size`;
    - then, holding `FS.mu` for writing, and in this order: `OpChmod` for
      `sa.Mode`; `OpChown` with `UID` `*sa.UID`; `OpChgrp` with `GID`
      `*sa.GID`; one `OpSetTimes` for each of `sa.ATime` and `sa.MTime`;
    - each before the attribute it guards is applied, so a later check sees
      the earlier attributes already applied.
  - `Lookup`: `OpLookup` on the directory.
  - `ReadDir`: `OpReadDir` on the directory.
  - `Create`, `Mkdir`, `Symlink`:
    - `OpAddEntry` on the directory;
    - then, when it makes a new inode and `sa.UID` is set, `OpChown` on the
      new inode with `UID` `*sa.UID`. `sa.UID` is applied only if that is
      allowed, and a refusal is ignored. That is today's behaviour, since only
      root's requested uid is applied: the new inode is the caller's, and
      `OpChown` allows its owner only the uid it already has. A non-exclusive
      `Create` of a name that exists makes no inode and no `OpChown` request,
      as today it applies no `sa.UID`.
    - A requested gid is applied as now, with no check, until #98's fix.
  - `Remove`, `Rmdir`: `OpRemoveEntry` on the directory.
  - `Rename`: `OpRename`, with `Object` the source directory and `Dst` the
    target.
  - `Access`: `AccessBits(c, objectOf(n))`; then, only when the mount mode is
    `ReadOnly` (`f.readOnly` until §7 is done), minus `vfs.AccessModify`,
    `vfs.AccessExtend` and `vfs.AccessDelete`; then `& want`. It does not go
    through `f.perm` until #97's fix.
  - `GetAttr`, `ReadLink`, `FSStat` and `Commit` consult no policy.
- **What `blobfs` relies on** (Assumption 34). `Unix()`'s type rows guard more
  than permission. Past them, `createEntry` and `Rename` add a name to the
  entry map of the inode they were given as the directory, which only a
  directory has: on a non-directory that write panics, after `createEntry`
  has already put the new inode in the table and after `Rename` has already
  taken the name out of its source directory. And `Write` and `truncate`
  would give a directory a size and a chunk list, `Write`'s through the flush
  that follows it. So every policy
  `blobfs` installs refuses at least what `Unix()` refuses with
  `vfs.ErrIsDir` and `vfs.ErrNotDir`. `New` installs `Unix()` alone; a later
  ADR that installs more keeps `Unix()` in front, as
  `policy.All(policy.Unix(), p)` does. A test that replaces `f.perm` with a
  policy that allows more makes its calls only on objects of the type each
  operation needs (§11).

### 7. The mount mode machine

This is for the PR that converts the mount mode. Pinned:

```go
type mountMode uint32

const (
    mountWritable mountMode = iota + 1 // accepts changes
    mountReadOnly                      // Config.ReadOnly; no edge leaves it
    mountDiverged                      // lost a swap to another writer; terminal
)

type mountEvent uint8

const mountDiverge mountEvent = 1 // a swap failed with store.ErrPrecondition

var mountModes = fsm.NewTable("mode",
    fsm.Edge[mountMode, mountEvent]{From: mountWritable, On: mountDiverge, To: mountDiverged})
```

- `String()` gives `"Writable"`, `"ReadOnly"`, `"Diverged"` and `"Diverge"`
  (vocabulary V1). Any other value gives `"mountMode(<n>)"` or
  `"mountEvent(<n>)"`.
- `FS` gains `mode *fsm.Atomic[mountMode, mountEvent]`, read without a lock:
  - `New` makes it before `loadOrInit`, since `initEmpty`'s commit can
    diverge;
  - it starts at `mountReadOnly` if `Config.ReadOnly` is set, and at
    `mountWritable` otherwise.
- `FS.readOnly` and `FS.diverged` are removed. No test reads either field:
  the tests name `f.diverged` only in a comment and in failure messages that
  quote or cite ADR 0003 §5.
- Its readers:
  - `mutable()` returns:
    - nil for `mountWritable`;
    - `vfs.ErrROFS` for `mountReadOnly`;
    - `vfs.ErrStale` for `mountDiverged`;
    - `vfs.ErrServerFault` for any other value.

    So it is nil exactly when the mode is `Writable` (vocabulary V1), and a
    state added later is refused until its ADR maps it.
  - `commitLocked` refuses, as now, when the mode is `mountDiverged`. On
    `store.ErrPrecondition` it fires `mountDiverge`, logs a failed `Fire` at
    `Error` (§9), then logs its `"commit conflict"` record and returns as now.
  - `Access` masks on `mountReadOnly` only, as now. Whether `Diverged` masks
    too is #97's to decide.
  - `Run` starts no committer on `mountReadOnly`.
  - `loadOrInit` refuses a bucket with no root pointer on `mountReadOnly`.
  - `New` still puts the store behind `store.ReadOnly`, decided from
    `Config.ReadOnly`.
- That PR also adds `Revised` lines to ADR 0003 (for §5's `f.diverged`) and to
  ADR 0013 (for §4's and Assumption 9's `FS.diverged`). They are
  cross-references only, so the tests that quote ADR 0003 §5 stay valid.

### 8. Machines decided elsewhere

- **Root-pointer knowledge**:
  - an `fsm.Machine` under `FS.mu`, which guards every read and write of it;
  - its states and events are the ones `docs/vocabulary.md` V2 reserves:
    `rootKnown`, `rootInDoubt`; `rootSwapOK`, `rootSwapUnknown`,
    `rootProbeOurs`, `rootProbeUnchanged`;
  - `ProbeTheirs` fires `mountDiverge` on the mount mode;
  - its ADR, for #5, names its type, decides its guards and the probe, and
    specifies it in TLA+ first (ADR 0013 §8).
- **The server, connection and process lifecycle**:
  - machines with the names V5 reserves, plus `mountClosed` and `mountClose`
    on the mount mode;
  - all decided by the lifecycle ADR (#82, and the rest of #92).
- **Link counts** take the lighter form:
  - one function through which `unlink` and `Rename` make every link-count
    change; it applies the change and says whether the inode has left the
    namespace;
  - a table-driven machine replaces it once something implements
    `vfs.Pinner`, whose pins add states;
  - which change introduces it is not decided here. #50's fix is the natural
    one.

### 9. Observability

- `fsm` logs nothing.
- An owner that logs a transition uses the keys `machine`, `from`, `to` and
  `event`. Their values are the table's `Name()` and the `String()` of the
  states and the event, logged as strings, so they are the vocabulary's names
  (Assumption 35).
- An owner whose `Fire` fails logs at `Error`, with the keys `machine`, `from`
  and `event` and the error, under the key `err`, and returns an error. The
  record's message is not pinned (Assumption 35).
- The mount mode adds no record of its own: `commitLocked`'s
  `"commit conflict"` `Error` record stays the record of `Diverge`.
- A policy logs nothing.
- A refused security-relevant transition is logged at `Debug` at most, with
  the keys above (§12).

### 10. Names

- `docs/vocabulary.md` holds them, under ADR 0013 §5's rules. This ADR adds
  its own names there: V1's Go types `mountMode` and `mountEvent`, its table
  `mountModes` and that table's name, `"mode"`; a section on Go machines; and
  the statuses and settlements of V7. It adds no bare name.
- The TLA+ doc-line format is ADR 0013 §6's.
- No TLA+ names are listed for the duplicate request cache, which ADR 0013 §1
  gives no spec.
- The two questions the vocabulary leaves to this ADR are settled:
  - **`UnixAllows` stands.**
    - Rule 4 gives the spec a Go predicate's own name.
    - The predicate here is `policy.Unix().Check(r) == nil`: the `Check`
      method that every `Policy` has, reached through a function that returns
      an interface.
    - So the name rule 4 would give it, `Check`, would name every policy at
      once.
    - The vocabulary records `UnixAllows` with that Go expression. Rule 4 is
      not changed for any other name.
  - **`EffectAuthorized`** names an invariant a spec may state: every change a
    call makes to a file's bytes or size is one that `UnixAllows` allows, for
    that call's caller, against the file's attributes at the instant the
    change is made.
    - Whether FileData states it, and how its configurations use it, is that
      spec's business.
    - Under #101 it fails today.

### 11. The test surface

Every name is spelled as in Go.

**`fsm`** (`internal/fsm/fsm_test.go`, package `fsm_test`):

- **F1.** `Next` returns `(To, true)` for each edge's `(From, On)`, and
  `(zero, false)` for every other pair, the zero values included.
- **F2.** `NewTable` panics on two edges with the same `(From, On)`, whatever
  their `To`. It does not panic on edges that share only `From` or only `On`.
- **F3.** `Edges` keeps declaration order and returns a copy. Changing what it
  returned, or the slice passed to `NewTable`, changes neither a later `Edges`
  nor `Next`.
- **F4.** `Name` returns the name given. A table with no edges, built with
  explicit type arguments, is valid: `Next` is `(zero, false)` for every pair,
  and `Edges` has length 0.
- **F5.** The initial `State`, of a `Machine` and of an `Atomic`.
- **F6.** A legal `Fire` returns `(from, to, nil)` and moves the state to
  `to`. A self-loop gives `from == to`.
- **F7.** An illegal `Fire`:
  - returns `(cur, cur, err)`, with `errors.Is(err, fsm.ErrIllegal)`;
  - `errors.As` gives an `*fsm.IllegalError[S, E]`, for the test's `S` and
    `E`, with `Machine`, `From` and `On` set;
  - `Error()` is exactly §2's text;
  - the state is unchanged.
- **F8.** `Atomic`, as F5 to F7.
- **F9.** At least 32 goroutines are released together on one `Atomic` over
  {A to B on one event}:
  - exactly one `Fire` returns `(A, B, nil)`;
  - every other returns `(B, B, err)`, with `err` an `*IllegalError` whose
    `From` is B;
  - the state ends at B.

  Repeated over at least 100 rounds, each with a new `Atomic`.
- **F10.** {A to B, B to C}, both on one event, with two concurrent `Fire`
  calls on one `Atomic`:
  - one returns `(A, B, nil)`, the other `(B, C, nil)`;
  - the state ends at C.

  Repeated over at least 100 rounds.
- **F11.** `State` and `Fire`, called concurrently on one `Atomic`, are
  race-free under `go test -race`, and every `State` returned is one the table
  can reach.
- **F12.** `NewMachine` and `NewAtomic` panic on a nil table.

**`policy`** (`internal/policy/policy_test.go`, package `policy_test`):

- **P1.** `InGroup`: the primary group, a supplementary group, neither, nil
  and empty `GIDs`, and a list of 16 or more.
- **P2.** `Permits`, exhaustively, over:
  - every `Mode` from 0 to 0o777, each also with 0o7000 set, and also with
    every bit above the low nine set;
  - `Type` `vfs.TypeReg`, `vfs.TypeDir`, `vfs.TypeLnk` and `vfs.FileType(0)`;
  - the callers root, the owner, a primary-group member, a supplementary-group
    member, and other;
  - every `want` from 0 to 7, against an oracle written from §5's rule;
  - `want` values with a bit outside 7, which give false for every caller.
- **P3.** Class exclusivity: the owner of 0o077, and a group member of 0o707,
  are refused read, write and execute.
- **P4.** Every pinned `Unix()` row, allowing and refusing, with `err ==` the
  `vfs.Status` itself; for the one refusal U6 pins as a refusal only, `err` is
  non-nil.
- **P5.** The type check comes before the permission check. For example:
  - `OpRead` on a directory with mode 0 is `vfs.ErrIsDir`;
  - `OpLookup` on a regular file with mode 0 is `vfs.ErrNotDir`;
  - `OpRename` with one directory unsearchable and the other not a directory
    is `vfs.ErrNotDir`.
- **P6.** `policy.Op(0)`, and every `Op` from 13 to 255, give
  `vfs.ErrServerFault`, root included.
- **P7.** `All`:
  - with no policies it allows;
  - it asks in order;
  - it stops at the first refusal and returns that error unchanged;
  - it keeps its own copy of its arguments;
  - it panics on a nil `Policy` and on a nil `Func`.
- **P8.** `Func`: `Check` calls the function once, with the request unchanged,
  and returns its result.
- **P9.** `AccessBits`' pinned rows:
  - `vfs.AccessRead` exactly when `Permits(c, o, PermRead)`.
  - A non-directory never gets `vfs.AccessLookup` or `vfs.AccessDelete`. It
    gets `vfs.AccessModify` and `vfs.AccessExtend` exactly when
    `Permits(c, o, PermWrite)`, and `vfs.AccessExecute` exactly when
    `o.Mode & 0o111 != 0` and `Permits(c, o, PermExec)`.
  - A directory never gets `vfs.AccessExecute`. It gets `vfs.AccessLookup`
    when `Permits(c, o, PermExec)`, and no `vfs.AccessLookup` when it has
    neither `Permits(c, o, PermRead)` nor `Permits(c, o, PermExec)`. It gets
    all of `vfs.AccessModify`, `vfs.AccessExtend` and `vfs.AccessDelete` when
    `Permits(c, o, PermWrite|PermExec)`, and none of them without
    `Permits(c, o, PermWrite)`.
  - No bit is set outside the six `vfs.Access` constants.
- **P10.** `InGroup`, `Permits`, `AccessBits` and `Unix().Check`, called
  concurrently on shared arguments, are race-free and agree with a sequential
  run.
- **P11.** Two `Unix()` values decide every P4 request alike.
- **P12.** No call modifies `Caller.GIDs`, neither its contents nor its
  length.
- **P13.** Fields an `Op` does not read do not change its answer: `Dst`, `UID`
  and `GID` may be set to anything, except `UID` for `OpChown`, `GID` for
  `OpChgrp` and `Dst` for `OpRename`.

**Unpinned rows.** Tests must not rely on these. Each is today's behaviour,
which a filed defect will change:

- **U1.** `OpRead`, `OpWrite` or `OpTruncate` by an owner who is not root,
  whose owner bits lack the permission (#93; RFC 1813 §4.4's owner override).
- **U2.** `OpRead` by a caller who is not root, whose class has execute but
  not read (#93; RFC 1813 §4.4's read with execute).
- **U3.** `OpRead`, `OpWrite` or `OpTruncate` on an `Object` whose `Type` is
  neither `vfs.TypeReg` nor `vfs.TypeDir`, symlinks included (#69).
- **U4.** `OpRemoveEntry` or `OpRename` where the directory has 0o1000 set
  (`Object`, or for `OpRename` either `Object` or `Dst`), for every caller
  (#94; POSIX XBD 4.5). `Permits` itself ignores the bit, and P2 pins that.
- **U5.** `AccessBits` for a directory with `Permits(c, o, PermRead)` but not
  `Permits(c, o, PermExec)`, which today gets `vfs.AccessLookup`; and for one
  with `Permits(c, o, PermWrite)` but not `Permits(c, o, PermExec)`, which
  today gets `vfs.AccessModify`, `vfs.AccessExtend` and `vfs.AccessDelete`
  (#97).
- **U6.** `OpSetTimes` by a caller who is neither root nor the owner (#100):
  - with `Permits(Caller, Object, PermWrite)` it is allowed today, and that
    row is unpinned;
  - without it, it is refused today with `vfs.ErrPerm`. Only the fact that it
    is refused is pinned, not the code.

  POSIX `futimens` gives [EACCES] for a time set to now without write access,
  and [EPERM] for any other time.

**`blobfs`, for the PR that does §7** (package `blobfs`):

- **M1.** `New` with `Config.ReadOnly` gives `f.mode.State() == mountReadOnly`,
  on a bucket that holds a filesystem (ADR 0011 §2); without it,
  `mountWritable`.
- **M2.** `mountModes.Name()` is `"mode"`, and `mountModes.Edges()` is exactly
  `[]fsm.Edge[mountMode, mountEvent]{{From: mountWritable, On: mountDiverge, To: mountDiverged}}`.
- **M3.** A root-pointer swap that fails with `store.ErrPrecondition` leaves
  the mode at `mountDiverged`, and a later `Write` answers `vfs.ErrStale`. A
  read-only mount's mode stays `mountReadOnly`, and its `Write` answers
  `vfs.ErrROFS`.
- **M4.** `TestWriteRacingADivergingCommitIsNotADataRace`, in
  `internal/blobfs/diverged_race_test.go` (which landed with #48's fix), is
  unchanged and passes under `-race`.
- **M5.** `String()` gives `"Writable"`, `"ReadOnly"`, `"Diverged"` and
  `"Diverge"`; `mountMode(0).String()` is `"mountMode(0)"`, and
  `mountEvent(0).String()` is `"mountEvent(0)"`.

**`blobfs`, for the PR that does §6** (package `blobfs`):

- **W1.** `New` sets `f.perm`; a test may replace it after `New` and before
  first use.
- **W2.** With a recording policy that refuses with `vfs.ErrNXIO`, each method
  that §6 says makes a request returns `vfs.ErrNXIO`, changes nothing, and has
  recorded exactly the requests §6 lists for it, up to the first.
- **W3.** With an allow-all policy, a caller whom mode bits would refuse
  succeeds, on objects of the type each operation needs. So no check remains
  outside `f.perm`.
- **W4.** A characterisation table of every pinned row, through `vfs.FS` and
  in the stage order, which passes both before the wiring and after it.
- **W5.** `GetAttr`, `ReadLink`, `FSStat`, `Commit` and `Access` never consult
  `f.perm`.

**For a PR that adds or changes a security-relevant transition** that a
caller's request causes (§12), in its owner's package (Assumption 46):

- **T1.** With a recording policy that refuses with a status no row of
  `Unix()` returns, such as `vfs.ErrNXIO` (as in W2), the request gets that
  status. The machine's `State()` is unchanged, nothing else changes, and the
  policy has recorded exactly the request the transition's ADR names.
- **T2.** With a policy that allows everything, the same request makes the
  transition: `State()` is the edge's `To`. Its objects are of the type the
  operation needs, as the safety rule below asks of `blobfs`.
- **T3.** A refused request makes no log record at `Info` or above.

**Safety rules for `blobfs` tests**: ADR 0011 §7 and ADR 0012 §9, and one
more. A test that replaces `f.perm` with a policy that allows what `Unix()`
refuses with `vfs.ErrIsDir` or `vfs.ErrNotDir` makes no call on an object of
the wrong type, since `blobfs` relies on those refusals (§6).

**What only review can show:**

- that neither package takes a lock;
- that `Atomic.Fire` is a compare-and-swap loop. F9 and F10 only sample it,
  and a scheduler may hide a wrong one;
- that copies are caught. A passing `go vet` shows only that nothing copies
  today;
- that a policy allocates nothing when it allows;
- that nothing is exported beyond §2 and §5;
- that `f.perm` is written only by `New`;
- that a failed `Fire` is logged;
- that a security-relevant transition's policy is asked in the hold that
  fires it (§12);
- that no path fires its event without asking;
- that every `Fire` on an `Atomic` with a security-relevant transition holds
  the lock its doc comment names.

### 12. A security-relevant transition asks a policy

This is the link of §1, which the owner's decision 4 asks for. It changes
neither `fsm` nor `policy`: it says where and when an owner asks its policy
(Assumption 43).

**What is security-relevant** (Assumption 42).

- A machine is security-relevant when a policy's request or a gate (§1)
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
    change's own check guards it, where §6 places that check; whether that
    place is right for `Write` and `truncate` is #101's question;
  - one that no caller causes, and that only narrows what callers may do,
    fails safe. It can only take something away, and there is no caller
    whose right to it a policy could judge.

**The link.** For each security-relevant transition:

- **Its ADR names the request.** The ADR that adds or changes the transition
  names the request that guards it: the `Op`, and where each field of the
  `Request` comes from, as §6 does for each method. A transition of the third
  kind that no caller causes has no caller to judge, so its ADR names the
  policy that decides it, or exempts it (§1).
- **The owner asks in the hold that fires.** It builds the request from the
  state as it stands, asks its policy, and fires the event only if the
  policy returns nil. All three happen in one hold of the lock that
  serializes the machine's `Fire` calls and guards what the request reads, so
  the verdict still holds when the event fires (Assumption 44).
  - For a `Machine`, that lock is the one its doc comment names (§2).
  - For an `Atomic`, every `Fire` on it, of any event, holds one lock, which
    its doc comment names. Its readers may still read it without that lock.
- **In a `vfs.FS` method**, the request is made at stage D (§6), and the
  event fires with the change that follows stage E, in the hold that C to E
  ran in. So a refusal comes before the namespace preconditions, in
  ADR 0007 §4's order of errors.
- **On a refusal**, the owner fires nothing and changes nothing, and returns
  the policy's error unchanged: for `Unix()`'s rows, the bare `vfs.Status`
  (§5).
- **On a permit**, it checks its other guards (§3), fires, and applies the
  side effects in the same hold. A `Fire` that fails then is an illegal
  transition (§3, §9).
- **The policy** is the owner's installed one: `f.perm` in `blobfs`, with
  `Unix()` in front (§6).
- **Cost.** A policy takes no lock and waits for nothing (§4, §5), so asking
  it inside the hold changes no lock order. Where the call already makes its
  request at stage D, the link adds nothing to it.

**Logs** (Assumption 45). A refusal is the caller's answer, not the server's
fault. The owner makes no record of it at `Info` or above. If it makes one at
`Debug`, the record carries §9's keys `machine`, `from` and `event`, and the
policy's error under `err`, and no `to`, since nothing moved. A policy still
logs nothing.

**Specs.** A spec that models such a machine conjoins the policy's predicate,
under its rule-4 name (`UnixAllows` for `Unix()`, vocabulary V7), with the
event's action, so the action is enabled only where the policy permits. A
refusal is no step of the machine, and leaves its variable as it was. Whether
the machine is specified in TLA+ at all is its ADR's question under
ADR 0013 §1, which gives permission rules to exhaustive tests of the policy
and to review. This section adds no name to `docs/vocabulary.md`.

**The machines this ADR names.**

- **The mount mode** is security-relevant, since `mutable()` and `Access`'s
  mask read it. Its one edge, `Diverge`, only narrows, and a store's answer
  causes it, so it needs no policy. An edge added later that leaves
  `ReadOnly` or `Diverged` widens, and is linked. Its one transition today is
  made in `commitLocked`, which runs holding `FS.mu` for writing, or from
  `initEmpty` before the FS is shared, so the one lock such an edge would need
  is already held.
- **Root-pointer knowledge and the lifecycle machines**: their ADRs (§8)
  apply this section. Root-pointer knowledge is security-relevant if the ADR
  for #5 has `mutable()` read it, and `ProbeOurs` and `ProbeUnchanged` would
  then widen.
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

## Assumptions

Each is a judgement call, not something the issue, the spec or an earlier ADR
required, except where it says otherwise. Assumption 1 records the owner's
decision 1 and says which part of it is mine. Assumptions 2 to 5 record the
choices the owner delegated to the architect's recommendation (decision 2),
and say so. The owner's decision 3 is recorded in *Context*, and Assumption 31
says how it bears on this ADR. Decision 4 is recorded in *Context* too, and
Assumptions 36 to 46 are the revision's judgement calls.

1. **The scope.** Decision 1, in the owner's words: "Go with architect's
   recommendations. I took a look on the report and it was well argued." It
   accepted §1's table and the ten places that are now E1 to E10. The
   reasons in the table and for E1 to E10 are mine. Decision 4 made the rules
   standing: §1 gives them in the wording the owner approved, and the rest of
   §1, and §12, are mine (Assumptions 37 to 45). The first version also kept
   a list of other things as they were, as my judgement. That list is gone:
   §1 says instead which of them the rules do not reach and which have not
   moved yet.
2. **`internal/fsm` rather than a switch per machine.** Delegated (decision
   2). My reasons: one tested mechanism; a table that can be listed and
   compared with a spec's Next relation; and a place for the atomic form.
3. **`internal/policy` rather than code in `blobfs` or `vfs`.** Delegated
   (decision 2). My reasons:
   - it can be tested exhaustively without an `FS`;
   - the backend that replaces `blobfs` can reuse it (`docs/DESIGN.md` §14);
   - it does not go in `internal/vfs`, which would widen the contract and put
     implementation in a package test-author reads.
4. **Status Accepted.** Delegated (decision 2).
5. **Issue #114.** Delegated (decision 2).
6. **Tables are pure, and owners do the logging** (§2, §9). A table knows
   nothing of its owner's context: the lock it holds, or the epoch or inode a
   record needs. A package that logged would also need a logger handed to it.
   So `fsm` returns an error and the owner records it.
7. **Illegal transitions are errors, not panics.** Tables panic only when
   built, on a duplicate edge; constructors panic on a nil table. Both are
   programming errors that show when a package's variables are initialised or
   when the owner is made, before it is shared. An illegal `Fire` happens at
   request time, under `FS.mu` or in the committer, where a panic would take
   down the mount or leave a lock held (§3).
8. **State types start at 1** (§3), so a field never initialised holds a value
   no machine names: `mutable()` refuses it (Assumption 24), and `String`
   prints it as `"mountMode(0)"`.
9. **`Atomic` is only for state read without a lock.** Its states are
   `~uint32` so that one atomic word holds them, and such a type parameter is
   comparable, so an `Atomic` can hold a `Table` (*Sources*). A machine read
   only under a lock gains nothing from an atomic word but its cost.
10. **Copies are a `go vet` error**, through `noCopy` and `atomic.Uint32`
    (*Sources*). A copied machine would carry on from the state it was copied
    in while the original moved on. `go vet ./...` is part of the merge bar
    (CLAUDE.md), so a copy fails the merge rather than a run.
11. **`IllegalError`'s message text.** It names the package, the machine, the
    state and the event, in the vocabulary's names wherever the types have
    `String` methods, so the message alone says which machine refused what.
    The exact text is pinned so that F7 can check it.
12. **A table is named after the field that holds its state**, which is also
    its TLA+ variable (`"mode"`). So the `machine` value in a log line reads
    as the spec's variable.
13. **Refusals are bare `vfs.Status` values.** An unknown `Op` fails closed
    with `vfs.ErrServerFault`, the code RFC 1813 §2.6 gives for an error that
    maps to no other value (*Sources*). Bare, because that is what `blobfs`
    returns today and what `statusOf` passes through. An unknown `Op` is a
    fault in the code that built the request, not a judgement of the caller,
    so neither `ErrAcces` nor `ErrPerm` describes it.
14. **`Permits` reads only the low nine bits.** A `want` outside 0 to 7 fails
    closed for root too, where `permitted()` would allow root. This departs
    from `permitted()` only for inputs no caller passes.
15. **Policies are pure** (§5). So a policy can be evaluated inside any lock
    (§4), tested exhaustively without an `FS` (§11), and run concurrently
    with nothing to coordinate.
16. **`All`**:
    - an empty `All` allows, as a conjunction of no checks does, so that
      `All(All(), p)` decides as `p` does;
    - the first refusal wins;
    - it copies its arguments;
    - it panics on a nil policy: a nil interface value, or a nil `Func`, which
      a type assertion finds. It does not catch a non-nil `Policy` that holds
      a nil pointer. Only `reflect` could tell such a value apart for a type
      `policy` does not know, and a typed nil `*T` whose `Check` never
      dereferences it is a legitimate policy, which `All` would then refuse.

    `blobfs` never builds an empty `All`; `New` installs `Unix()`.
17. **`Unix()` is a function, and `policy` exports no variable.** An exported
    variable would be a package-level authority that any importer could
    reassign for every caller at once.
18. **Namespace preconditions are not policies**: they define the operation,
    not who may perform it. The validators stay validators: they judge
    arguments, not callers.
19. **This PR's rules reproduce today's exactly**, disputed rows included, and
    the tests pin only the undisputed ones (U1 to U6 stay unpinned). U6's
    refusal is pinned as a refusal only, since #100's fix may change its code.
    Changing a rule in the same step as the form of every rule would leave no
    way to tell a regression from a decision.
20. **A requested uid at creation goes through `OpChown`**, with a refusal
    ignored. A requested gid stays inline until #98's fix: giving it an
    `OpChgrp` request now would refuse groups that are applied today, which
    is #98's decision to make.
21. **`Access` does not go through `f.perm` until #97's fix**, and
    `AccessBits` carries no mount mask. RFC 1813 §4.4 keeps `ACCESS`'s answer
    apart from the server's own overrides (*Sources*), so deriving `ACCESS`
    from the operations' rows is a decision of its own, #97's. The mask is the
    mount's, not the caller's, so it stays with the mount (§6).
22. **`f.perm` is a test seam**, written only by `New`, as `FS.maxFileSize` is
    (ADR 0007 §7). It is what lets W2 and W3 show that every check goes
    through it.
23. **Read-only is a state of the mount mode, not a separate flag.** A mount's
    mode follows `Config.ReadOnly` alone, so a `store.ReadOnly` passed with
    `Config.ReadOnly` unset (ADR 0011 §5) leaves the mode `Writable`.
24. **`mutable()` fails closed** with `vfs.ErrServerFault` for a mode it does
    not name, so a state added later refuses changes until its ADR maps it,
    and so does a mode never set.
25. **A field added to `Request` later keeps the meaning of a request that
    leaves it zero** (§5), so a caller written before the field still asks
    what it meant to ask.
26. **Each check's `Object` is the inode as it stands at that check** (§6),
    which is what each inline check reads today.
27. **The settlements of `UnixAllows` and `EffectAuthorized`** (§10). Keeping
    `UnixAllows` costs rule 4 one exception, where the alternative would name
    every policy `Check`. `EffectAuthorized` is stated against the attributes
    at the instant of the change, because that is where a caller's right to
    the change is spent, and that is the instant #101 says the code does not
    check.
28. **The link-count form** (§8). A count is arithmetic on one field of an
    inode under `FS.mu`, with nothing beyond the number to name as a state.
    One function puts every change in one place, which is where #50's
    arithmetic goes wrong. A table earns its place once pins make a count of
    zero mean something other than gone.
29. **The costs are not measured.** I cannot run this server.
30. **Read, not run.** Every statement about the code was checked by reading
    it at `a607410`. Nothing was run.
31. **Not breaking, and no `CHANGES` entry**: nothing a client or a deployment
    sees changes in this PR. Decision 3 bears on the later fixes, not on this
    PR.
32. **The issue numbers and titles in *Context* are as the session gave
    them**; their texts were not read.
33. **`noCopy` is a named field, not an embedded one** (§2). Embedded, it
    would promote its `Lock` and `Unlock` to `Machine`: two exported methods
    that §2 does not list, which would also make `*Machine` a `sync.Locker`.
    `sync/atomic`'s own `noCopy` says it "must not be embedded, due to the
    Lock and Unlock methods" (*Sources*).
34. **`blobfs` relies on `Unix()`'s type rows** (§6). Putting the type checks
    in the policy keeps every refusal of a call, and its order, in one table
    that P5 can test without an `FS` (*Alternatives considered*). The cost is
    that the rows guard `blobfs`'s own invariants too, so a policy installed
    in place of `Unix()` must keep them. Today that costs nothing, since `New`
    installs `Unix()` alone. It binds a later ADR that installs more, and a
    test that replaces `f.perm` (§11).
35. **Log values are strings, and the error's key is `err`** (§9). A value
    logged as the state itself would print as whatever a handler makes of a
    `uint32` with a `String` method; a string leaves nothing to the handler.
    `err` is the key the repository's other `Error` records use for an error
    (ADR 0003 §7; the committer's records). The message is left open because
    only review checks the record (§11).
36. **The revision: read, not run.** Every statement the revision of
    2026-10-24 makes about the code was checked by reading it at `2d7ff34`,
    the commit its branch was cut from. Nothing was run. The first version's
    statements stand as checked at `a607410` (Assumption 30). The revision
    changes no code and no test, so nothing a client or a deployment sees
    changes, and it needs no `CHANGES` entry.
37. **What "new and changed code" means** (§1). The approved wording does not
    say. I took a change to count when it alters the state or the decision
    itself: its states, events, transitions or writers, or what it allows. So
    a fix that changes how a flag moves cannot avoid the rules, and a
    refactor that only moves code cannot force a conversion. Such a change
    takes the whole state, since the state rule has every write go through
    the machine, and half a machine would leave writes that bypass it.
38. **What the rules reach** (§1). The state rule's "named states" leaves out
    values and caches. The policy rule's "whether a caller may do something"
    leaves out checks of whether a request is well formed and of what it
    names, which §6 already puts in stages B and E, apart from the gate in
    stage A and the policy in stage D; Assumption 18 already kept the
    namespace preconditions and the validators out. The first version's list
    of other things left as they were is sorted on those lines. Where each
    falls is my reading of the rules' words, and nothing on either side is
    exempt.
39. **E1 to E10** (§1). The first version left them as they were, each with a
    reason and no rule named. I gave each the rule its reason answers: the
    state rule for the machines and the state, and the policy rule for the
    validators and the clamp. By §1's terms the policy rule does not reach
    those two at all. They stay listed so that the list is the one decision 1
    accepted. Their code is named as it stands at `2d7ff34`.
40. **E11 and E12** (§1). §6 and §7 keep the mount gate and `ACCESS`'s
    read-only mask in `blobfs`, and the policy rule names "an access mask" and
    "a gate on an operation", so without these two this ADR would break its
    own rule. I recorded them rather than move either into `policy`: both
    judge the mount, not the caller (Assumption 21), and `policy` cannot name
    the mount mode's type without an import cycle. They are the only
    exemptions the revision adds. It grants none to the three things *Not yet
    moved*, which the approved wording leaves to #114's plan.
41. **Granting and revoking** (§1). One list, kept whole, is what makes the
    approved wording's "ADR 0014 §1 lists the current ones" true, so an
    exemption granted by another ADR is listed here as well. E numbers are
    never reused, as ADR numbers are not, and a revoked entry stays, as a
    superseded ADR does. An exemption whose reason a change removes is
    restated or revoked by that change's ADR, so that none outlives its
    reason.
42. **What is security-relevant** (§12). The approved wording leaves the
    definition to this ADR. I based it on the owner's "security or related to
    authorisations of some kind": a machine whose state a policy's request or
    a gate reads, or which records authority; a transition a caller causes in
    such a machine, or asks for under a decision about who may have it; and
    one that widens what callers may do. Two things are left out on purpose:
    - a transition that only follows an allowed change, in a machine that is
      not security-relevant, such as one of `FS.dirty`, a link count or an
      open-file entry. Its change's check already guards it. Counting it in
      would make almost every piece of state security-relevant, and its
      question, where that check is made, is #101's;
    - a transition that no caller causes and that only narrows, such as
      `Diverge`. It fails safe, and there is no caller to judge.

    The locks, leases and share reservations of §12 are illustrations: no
    such machine exists.
43. **The link is a convention, not an API** (§12). An owner asks its policy
    where it fires, as §6's methods ask theirs at stage D. So `fsm` and
    `policy` stay as §2 and §5 pin them, and no code or test changes. It is
    enforced by T1 to T3 and by review, not by construction; *Alternatives
    considered* gives the forms that would enforce it, and why each lost.
44. **One hold, and one lock for an `Atomic`** (§12). A verdict is about the
    state and the attributes as they stood when it was given, and firing in
    the same hold keeps them so. An `Atomic` lets one `Fire` race another, so
    one with a security-relevant transition has every `Fire` hold one lock,
    which leaves its readers lock-free. The mount mode's one transition is
    already made holding `FS.mu`, or before the FS is shared (§12).
45. **A refusal is not logged above `Debug`** (§12). A client can send refused
    requests at will, and a record of each at `Info` or above would let it
    fill the log, as ADR 0016 Assumption 9 weighed for its own record. §9's
    keys are reused so that a record that is made reads like a transition's.
    An audit record is not decided (*What this does not decide*).
46. **T1 to T3** (§11) carry W2 and W3 over from a method to a transition.
    `vfs.ErrNXIO` serves for the reason it does in W2: no row of `Unix()`
    returns it, so it can only be the recording policy's.

## Alternatives considered

- **A hand-written switch per machine.** Each machine would encode its edges
  in its own code, with its own tests, nothing that can be listed and compared
  with a spec's Next relation, and no shared atomic form. The mount mode's two
  fields are such an encoding today (Assumption 2).
- **A third-party FSM library.** CLAUDE.md's dependency rule forbids it: the
  project uses nothing outside the Go standard library, and adding a module is
  the owner's decision.
- **Generating Go from TLA+.** ADR 0013 §6 ties the model to the code by the
  trace and by review, and a generator would be a tool outside the standard
  library, which ADR 0013 §9 leaves to the owner.
- **Machines for the budget, the DRC, the per-index state, `Sync`'s phases and
  the XDR reader.** §1 gives each its reason.
- **One check at the top of each method.** It breaks ADR 0007 §4's order, and
  the inode is not resolved yet at that point.
- **Policies in `internal/vfs`.** That would widen the contract every backend
  implements, and put implementation in a package test-author reads
  (Assumption 3).
- **Policies as `FS` methods.** They could not be tested without an `FS`, nor
  reused by the backend that replaces `blobfs`.
- **Keeping `FS.readOnly` and the atomic `FS.diverged` as they are since #48's
  fix.** Two fields encode three states, they leave "read-only never diverges"
  implicit, and the lifecycle adds a fourth state.
- **Panicking on an illegal transition.** A panic under `FS.mu`, in the
  committer or in `New` would take down the mount or leave a lock held (§3).
- **An empty `All` that refuses.** `All` is a conjunction, and one of no checks
  allows. A refusing empty `All` would make `All(All(), p)` refuse everything
  `p` allows (Assumption 16).
- **The type checks outside the policy, in `blobfs`.** No policy could then be
  tested for the order of a call's refusals (P5), and the backend replacing
  `blobfs` would have to repeat the rows. What it would buy, a policy that
  cannot break `blobfs`'s invariants, costs one rule in §6 instead
  (Assumption 34).
- **An edge that names its policy, which `Fire` asks.** A request is the
  call's, not the edge's: its `Object`, `Dst`, `UID` and `GID` come from the
  call, so `Fire` would need the whole request, and `fsm` would import
  `policy` and, through it, `vfs`, where §2 gives it the standard library
  alone. Asked inside `Fire`, the policy would answer after stage E's
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
- **Linking every transition of a security-relevant machine.** A transition
  that no caller causes and that only narrows has no caller to judge, and the
  mount mode's `Diverge` would need a policy that always allows.
- **Counting every transition that an allowed change causes.** `FS.dirty`,
  link counts and buffers would each need a request of their own for a change
  already checked, and #101 would become a breach of §12 before its own
  decision (Assumption 42).
- **Keeping the first version's fixed list.** Decision 4 makes the rules
  standing.
- **Treating code that has not moved as exempt until it moves.** It would keep
  the old blanket as an exemption that no ADR names, which decision 4 rules
  out.

## Consequences

- Two pure standard-library packages arrive, and they change no behaviour in
  this PR.
- New and changed state, and new and changed decisions about who may act,
  follow this ADR unless an ADR exempts them, and §1 lists every exemption in
  force.
- A change to state that has not moved, such as `FS.dirty`, can grow into its
  conversion, or wait on an ADR that exempts it (§1).
- A security-relevant transition is checked in the hold that fires it, at a
  call's stage D and before its preconditions, so a refusal never leaves a
  machine moved (§12).
- The link is enforced by T1 to T3 and by review, not by `fsm`
  (Assumption 43).
- The vocabulary binds Go, logs and specs together.
- The fixes for #93, #94, #97, #100 and #69 turn unpinned rows into pinned
  ones, each under its own decision. Decision 3 allows those changes to be
  breaking until release.
- Hard to change later: `fsm`'s API, once machines use it; `Request`'s shape,
  once policies use it; and a table's name, once logs carry it.

## What this does not decide

- The fixes for #93, #94, #96, #97, #98, #99, #100, #101, #69, #50 and #102.
- The root-pointer machine (#5), and the lifecycle (#82, #92).
- Root squash and the `AUTH_SYS` trust boundary (#6, #90, #95).
- `COMMIT`'s permission check.
- Whether the commit protocol's decisions become named functions (their ADRs;
  vocabulary V7).
- The codes a diverged mount answers (ADR 0003 §5).
- NFSv4 state. Its open state, share reservations and locks stay in
  `internal/nfs` (`vfs.Pinner`'s doc comment), so a security-relevant machine
  there whose request reads state behind `vfs.FS` cannot hold the backend's
  lock (§12); its ADR decides where the check is made.
- A request for a caller that is not a `vfs.Caller` acting on a filesystem
  object, such as one at the RPC layer. The ADR of the first machine that
  needs one extends §5, under its *Growth* rule.
- Whether each thing *Not yet moved* (§1) is converted or exempted.
- An audit record of refusals.
- Limits on what one caller may hold or use up, such as the budget,
  connections and calls in flight. They decide nothing about who may act, and
  §12 does not cover them.
- Any TLA+ spec.

## Sources

The revision of 2026-10-24 fetched nothing. It cites only this repository's
own code and documents, and the entries below are the first version's.

Fetched on 2026-10-12 through a tool that summarises what it fetches, and
quoted as it returned them when asked for verbatim text. It reads a long page
100,000 characters at a time, and it quoted at most about 125 characters at
once, so a longer passage was asked for as consecutive pieces, which are
joined here with a space. Each URL was fetched while this ADR was written;
none is cited from an earlier fetch.

- **RFC 1813**, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>. The tool gave the text as
  229,793 characters. Characters 0 to 100,000 were read twice, and 170,000 to
  the end twice; 100,000 to 170,000 were not read.
  - §2.6: `NFS3ERR_PERM = 1`, "Not owner. The operation was not allowed
    because the caller is either not a privileged user (root) or not the owner
    of the target of the operation."; `NFS3ERR_ACCES = 13`, "Permission
    denied. The caller does not have the correct permission to perform the
    requested operation. Contrast this with NFS3ERR_PERM, which restricts
    itself to owner or privileged user permission failures.";
    `NFS3ERR_NOTDIR = 20`, "Not a directory. The caller specified a
    non-directory in a directory operation."; `NFS3ERR_ISDIR = 21`, "Is a
    directory. The caller specified a directory in a non-directory
    operation."; and `NFS3ERR_SERVERFAULT = 10006`, "An error occurred on the
    server which does not map to any of the legal NFS version 3 protocol error
    values. The client should translate this into an appropriate error. UNIX
    clients may choose to translate this to EIO." (§5; Assumption 13)
  - §3.3.4, ACCESS: the six bits, from `const ACCESS3_READ = 0x0001;` to
    `const ACCESS3_EXECUTE = 0x0020;`, which match `vfs.AccessRead` to
    `vfs.AccessExecute`; of `ACCESS3_LOOKUP`, "Look up a name in a directory
    (no meaning for non-directory objects)."; of `ACCESS3_DELETE`, "Delete an
    existing directory entry."; of `ACCESS3_EXECUTE`, "Execute file (no
    meaning for a directory)."; "The results of this procedure are necessarily
    advisory in nature."; and "a return status of NFS3_OK and the appropriate
    bit set in the bit mask does not imply that such access will be allowed to
    the file system object in the future, as access rights can be revoked by
    the server at any time." Asked how its IMPLEMENTATION computes the bits
    under a UNIX permission model, the tool found nothing on it but the
    `DELETE` bit on a non-directory. (§5, §6; U5)
  - §4.4, "Permission issues", which the tool placed about 182,000 characters
    in: "To get around this problem, the server's permission checking algorithm
    should allow the owner of a file to access it regardless of the permission
    setting. This is needed in a practical NFS version 3 protocol server
    implementation, but it does depart from correct local file system
    semantics. This should not affect the return result of access permissions
    as returned by the ACCESS procedure, however."; and, of paging in an
    executable, "To make this work, the server allows reading of files if the
    uid given in the call has either execute or read permission on the file,
    through ownership, group membership or public access. Again, this departs
    from correct local file system semantics." (U1, U2, U5; Assumption 21)
- **POSIX.1-2024, `futimens()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/futimens.html>,
  which gives itself as The Open Group Base Specifications Issue 8, IEEE Std
  1003.1-2024. DESCRIPTION: "Only a process with the effective user ID equal
  to the user ID of the file, or with write access to the file, or with
  appropriate privileges may use futimens() or utimensat() with a null pointer
  as the times argument or with both tv_nsec fields set to the special value
  UTIME_NOW." and "Only a process with the effective user ID equal to the user
  ID of the file or with appropriate privileges may use futimens() or
  utimensat() with a non-null times argument that does not have both tv_nsec
  fields set to UTIME_NOW and does not have both tv_nsec fields set to
  UTIME_OMIT." ERRORS: [EACCES] "The times argument is a null pointer, or both
  tv_nsec values are UTIME_NOW, and the effective user ID of the process does
  not match the owner of the file and write access is denied."; [EPERM] "The
  times argument is not a null pointer, does not have both tv_nsec fields set
  to UTIME_NOW, does not have both tv_nsec fields set to UTIME_OMIT, the
  calling process' effective user ID does not match the owner of the file, and
  the calling process does not have appropriate privileges." The tool dropped
  the page's italic markup. (U6)
- **POSIX.1-2024, `unlink()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/unlink.html>.
  ERRORS, under "[EPERM] or [EACCES]" and marked XSI: "The S_ISVTX flag is set
  on the directory containing the file referred to by the path argument and
  the process does not satisfy the criteria specified in XBD 4.5 Directory
  Protection." (U4)
- **POSIX.1-2024, XBD 4.5**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/basedefs/V1_chap04.html>,
  "4.5 Directory Protection": "If a directory is writable and the mode bit
  S_ISVTX is set on the directory, a process may remove or rename files within
  that directory only if one or more of the following is true:" "The effective
  user ID of the process is the same as that of the owner ID of the file."
  "The effective user ID of the process is the same as that of the owner ID of
  the directory." "The process has appropriate privileges." "Optionally, the
  file is writable by the process. Whether or not files that are writable by
  the process can be removed or renamed is implementation-defined." (U4)
- **The Go memory model**, "Version of June 6, 2022": <https://go.dev/ref/mem>.
  Under *Informal Overview*: "A data race is defined as a write to a memory
  location happening concurrently with another read or write to that same
  location, unless all the accesses involved are atomic data accesses as
  provided by the sync/atomic package." Under *Atomic Values*: "If the effect
  of an atomic operation A is observed by atomic operation B, then A is
  synchronized before B." and "All the atomic operations executed in a program
  behave as though executed in some sequentially consistent order." (§2, §4)
- **`sync/atomic`'s `type.go`**: <https://go.dev/src/sync/atomic/type.go>; the
  page names no Go version. `Uint32`'s doc comment, "A Uint32 is an atomic
  uint32. The zero value is zero." and "Uint32 must not be copied after first
  use.", with its fields `_ noCopy` and `v uint32`; `noCopy`'s, "noCopy may be
  added to structs which must not be copied after the first use." and "Note
  that it must not be embedded, due to the Lock and Unlock methods.", with
  `func (*noCopy) Lock() {}` and `func (*noCopy) Unlock() {}` under the
  comment "Lock is a no-op used by -copylocks checker from `go vet`."; and
  `CompareAndSwap`, which "executes the compare-and-swap operation for x". (§2;
  Assumptions 10 and 33)
- **`go vet`'s copylock analyzer**, at `master`:
  <https://raw.githubusercontent.com/golang/tools/master/go/analysis/passes/copylock/copylock.go>.
  "Package copylock defines an Analyzer that checks for locks erroneously
  passed by value." `lockPath` "returns a typePath describing the location of
  a lock value contained in typ"; its test, under the comment "We're looking
  for cases in which a pointer to this type is a sync.Locker, but a value is
  not.", is
  `if types.Implements(types.NewPointer(typ), lockerType) && !types.Implements(typ, lockerType) {`;
  it recurses into each field of a struct
  (`subpath := lockPath(tpkg, ftyp, seen)`), and, for a `*types.TypeParam`,
  into each of `typeparams.StructuralTerms(tpar)`. That a copy of a `Machine`
  or an `Atomic` is therefore reported, through its `noCopy` field or through
  the one inside `atomic.Uint32`, is my reading of this code; nothing was run.
  (§2; Assumption 10)
- **The Go specification**, "Language version go1.27 (May 26, 2026)":
  <https://go.dev/ref/spec>, read from character 0 to 200,000 of 263,947.
  "The predeclared interface type comparable denotes the set of all
  non-interface types that are strictly comparable" (*Type constraints*); "A
  type T implements an interface I if T is not an interface and is an element
  of the type set of I; or T is an interface and the type set of T is a subset
  of the type set of I." (*Implementing an interface*); "A type T satisfies a
  constraint C if T implements C; or C can be written in the form interface{
  comparable; E }, where E is a basic interface and T is comparable and
  implements E." (*Satisfying a type constraint*); and, under *Comparison
  operators*, "Integer types are comparable and ordered.", "Type parameters
  are comparable if they are strictly comparable (see below).", "Type
  parameters are strictly comparable if all types in their type set are
  strictly comparable." and "A type is strictly comparable if it is comparable
  and not an interface type nor composed of interface types." That a type
  parameter constrained by `~uint32` satisfies `comparable` is my reading of
  these: every type in its type set has the underlying type `uint32`, an
  integer type and not an interface, so the type parameter is strictly
  comparable. No sentence the tool returned says so in one. (§2;
  Assumption 9)
- **Linux `fs/nfsd/vfs.c`**, at `master`:
  <https://raw.githubusercontent.com/torvalds/linux/master/fs/nfsd/vfs.c>,
  which the tool said it read whole. `nfs3_diraccess` maps
  `NFS3_ACCESS_LOOKUP` to `NFSD_MAY_EXEC`, `NFS3_ACCESS_MODIFY` to
  `NFSD_MAY_EXEC|NFSD_MAY_WRITE|NFSD_MAY_TRUNC`, `NFS3_ACCESS_EXTEND` to
  `NFSD_MAY_EXEC|NFSD_MAY_WRITE` and `NFS3_ACCESS_DELETE` to
  `NFSD_MAY_REMOVE`. In `nfsd_permission`, under the comment "The file owner
  always gets access permission for accesses that would normally be checked
  at open time. This is to make file access work even when the client has
  done a fchmod(fd, 0).", the owner returns early with
  `if ((acc & NFSD_MAY_OWNER_OVERRIDE) && uid_eq(inode->i_uid, current_fsuid())) return 0;`;
  and under `/* Allow read access to binaries even when mode 111 */`, a read
  refused with `-EACCES` on a regular file (`S_ISREG`), when it carries
  `NFSD_MAY_OWNER_OVERRIDE` or `NFSD_MAY_READ_IF_EXEC`, is asked again as
  `inode_permission(&nop_mnt_idmap, inode, MAY_EXEC)`. (U1, U2, U5)
