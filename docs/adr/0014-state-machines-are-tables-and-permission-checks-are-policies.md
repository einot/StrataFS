# 0014. State machines are explicit tables, and permission checks are policies

**Status:** Accepted
**Date:** 2026-10-12
**Revised:** 2026-10-24 — cross-references only. ADR 0018 makes the rules this
ADR applied to a fixed scope standing for new and changed code, lists every
exemption in the register `exemptions.md`, and links each security-relevant
transition to a policy. It replaces §1's scope for new and changed code: §1's
ten places are that register's E1 to E10, and "Everything else keeps its
present form" holds for each thing it covers until a change brings that thing
under the rules (ADR 0018 §2, §3). §1, §3 and §9 each gain a sentence pointing
to ADR 0018. Nothing else here changes, §1's text
included, and every test this ADR pins stands.
**Revised:** 2026-10-27 — §7, for the conversion it plans, against the code
at `37abe5d`: ADR 0021's refusal of a swap answered 501 fires nothing, and
the fallback and `case err != nil` fire nothing either; `commitLocked`'s one
`Fire`, its failed-`Fire` record and what it returns are stated; the field's
doc comment, the file `internal/blobfs/mode.go`, and the model's change
(`Diverge` leaves any mode but `Writable` as it is, R11 rewritten, no
`Fixes` member) are pinned; the tests that name the old fields are listed as
of `37abe5d`; §11 adds M6 to M9, names the test file and extends what review
checks; Assumptions 36 to 42 are added. The type, the table, the `String`
values, the initial state, `New`'s ordering and `mutable()`'s mapping are
unchanged, and every test this ADR pins stands.
**Revised:** 2026-10-27 — cross-reference only, for ADR 0022, which decides
the root-pointer knowledge machine §8 gave it. Two of §7's statements move
with it: the mode's one `Fire` of `mountDiverge` is now made by
`resolveRoot`, on the probe's verdict `ProbeTheirs`, called from
`commitLocked` under the same hold, and `commitLocked`'s
`store.ErrPrecondition` case fires `rootSwapUnknown` instead; and M3, M4
and M7, which diverged a mount by a refusal against an unchanged root
pointer, now diverge it by a second writer, as ADR 0022 §8 says. The type,
the table, the `String` values, `mutable()`'s mapping and every other test
are unchanged; a sentence at §7 and at §8 says so.
**Revised:** 2026-10-28 — cross-reference only, for ADR 0022 §2, revised
on 2026-10-28, which replaces one comment of §7's pinned `mode.go` block,
`mountDiverge`'s. Pinned as `// a swap failed with store.ErrPrecondition`,
it is since ADR 0022, and in the code since `b9c819f`,
`// a read of the root pointer showed another writer's root pointer, or none (ADR 0022 §3)`,
because the event's one `Fire` is now `resolveRoot`'s, on the verdict
`ProbeTheirs` of its read of the root pointer, as the line above records,
and a swap's failure no longer fires it. The block stays as the record, as
`docs/adr/README.md` has it for a part a later ADR replaces;
`mountDiverged`'s comment, `lost a swap to another writer; terminal`,
stands (ADR 0022 Assumption 27); and no test changes for it.
**Issue:** #114

## Context

**The instruction.** The repository owner's instruction, as the session
recorded it: "make sure that the code uses proper FSM state machines
everywhere where there is anything that has a state or state transitions. Use
policy pattern everywhere when the code needs to check authorisation or if
some operation is allowed." #114 is a tracking issue for it, as #109 is for
the instruction behind ADR 0013.

**The owner's decisions.** Three, recorded in the owner's words as the session
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

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `a607410`, the commit this ADR's branch was cut from
(Assumption 30). Code is cited by symbol.

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
- how `internal/blobfs`, `internal/sunrpc`, `internal/nfs` and `cmd/strata`
  express state and permission checks from now on;
- `docs/vocabulary.md`.

The PR that carries this ADR also carries the two packages and their tests
(§2, §5, §11), and changes no other code. Two later PRs wire them in: one
converts the mount mode (§7), and one has `blobfs` evaluate the policy (§6).

## Decision

### 1. Scope

Decision 1, in the owner's words: "Go with architect's recommendations. I took
a look on the report and it was well argued." It accepts the narrower set,
which this table and the two lists after it give.

| Item | Form | Why | Decided in |
|---|---|---|---|
| The mount mode (`FS.readOnly`, `FS.diverged`) | an `fsm.Atomic` | it is read without a lock ahead of every change, and the table states that a read-only mount never diverges and that `Diverged` is terminal | §7 |
| Root-pointer knowledge | an `fsm.Machine` under `FS.mu` | #5 is a missing state, `InDoubt` | §8 |
| The server, connection and process lifecycle | machines | #82 and #92 are missing transitions | §8 |
| Link counts | the lighter form, one transition function | the counts are arithmetic, and #50 is an arithmetic defect | §8 |
| Every check of what a caller may do: `permitted`, `inGroup`, the per-operation checks, `SetAttr`'s ownership and time rules, a requested uid at creation | a policy | one table of rules, tested exhaustively, with an extension point | §5, §6 |
| `ACCESS` | `policy.AccessBits`, with the mount's mask | `ACCESS` becomes derivable from the same rules (#97 later) | §5, §6 |
| The mount gate, `mutable()` | a predicate over the mount mode | V1's `Mutable`: nil exactly when the mode is `Writable` | §7 |

**The ten places left as they are**, each with its reason. The reasons are
mine (Assumption 1).

1. **The write budget.** It is already the most precisely specified machine
   here (ADR 0003 §4, with its failure contract G1 to G3). Rewriting it risks
   that contract for nothing, and the Budget spec checks it (ADR 0013 §1).
2. **The duplicate request cache.** ADR 0002 §6 pins its three states and §8
   its API. Another representation would change the memory bound its
   *Consequences* compute.
3. **`Sync`'s phases and the commit gate.** The sequential code already is the
   machine, and the gate is a lock. FileData models both.
4. **The per-index buffered state.** It is a hot path, and invariants I1 to I4
   are pinned and tested (ADR 0006 §2). A table lookup per index would catch
   nothing new.
5. **The unsettled mark.** Every (state, event) pair is legal, so a table
   would refuse nothing (ADR 0012 §3).
6. **The XDR reader's sticky error.** It is an idiomatic sticky error over a
   bounded reader.
7. **The store's retries.** They are a bounded loop (`S3.do`).
8. **The flag validators.** They are pure checks that ADRs 0008 to 0010 pin.
9. **`-check`.** It is a linear script of probes.
10. **The `MaxFileSize` clamp.** A clamp is not an allow or a deny (ADR 0007
    §4).

**Everything else keeps its present form.** This is my judgement, recorded as
Assumption 1, and not the owner's decision. It covers:

- `FS.dirty`, the pruner's flag, the chunk cache, the known-chunk set and the
  open-file entry (#42);
- credential parsing, the export match, the idempotency list, RPC admission
  and `store.ReadOnly`;
- `validName`, the empty-write rule and `Symlink`'s empty-target check;
- the namespace preconditions;
- `COMMIT`'s lack of a permission check (`vfs.FS.Commit` has no `Caller`).

These are flags or sets with one rule each, checks of arguments rather than
callers, or pure checks already pinned by their ADRs.

Since ADR 0018, the rules this section applies bind all new and changed code,
the ten places above are exemptions E1 to E10 in the register
`exemptions.md` (ADR 0018 §4), and what the paragraph above keeps as it is
keeps it until a change brings it under the rules (ADR 0018 §2, §3).

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
  lock. ADR 0018 §5 says where the owner asks its policy for a
  security-relevant transition.
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

This is for the PR that converts the mount mode: #114's first conversion.
The section was revised on 2026-10-27 against the code at `37abe5d`, which
has ADR 0019, ADR 0020 and ADR 0021 in it (Assumption 36); the `Revised`
line above says what moved. Pinned, in a new file `internal/blobfs/mode.go`,
whose `Spec:` line cites §3 and §7 (Assumption 37):

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

- `String()`, with value receivers (§3), gives `"Writable"`, `"ReadOnly"`,
  `"Diverged"` and `"Diverge"` (vocabulary V1). Any other value gives
  `"mountMode(<n>)"` or `"mountEvent(<n>)"`, `n` in decimal.
- `FS` gains `mode *fsm.Atomic[mountMode, mountEvent]`, read without a lock:
  - `New` makes it with `fsm.NewAtomic(mountModes, initial)` after its checks
    on `cfg` and before `loadOrInit`, since `initEmpty`'s commit can diverge
    (M8). Making it calls nothing on the store, so ADR 0009 §2 and
    ADR 0011 §2 are unaffected; it sits where `allowUnconditionalRoot` is
    set, for the same reason (ADR 0021 §2).
  - `initial` is `mountReadOnly` if `Config.ReadOnly` is set, and
    `mountWritable` otherwise (Assumption 23).
  - Nothing reassigns it. Its doc comment says that it is read without a
    lock; that every `Fire` on it is made in `commitLocked`, holding `FS.mu`
    for writing from `Sync` or from `initEmpty` before the FS is shared; and
    that `Diverge` needs no policy (ADR 0018 §5).
- `FS.readOnly` and `FS.diverged` are removed. No test reads either field.
  At `37abe5d` the tests name them only in text no compiler reads:
  `f.diverged` in a failure message of `internal/blobfs/backpressure_test.go`
  and in a comment and a failure message of
  `internal/nfs/backpressure_test.go`, each quoting or citing ADR 0003 §5,
  and `FS.diverged` in a comment of `internal/blobfs/swap_fallback_test.go`
  that lists what ADR 0021 §6 leaves to review. None changes.
- **Its one writer.** `commitLocked`'s `store.ErrPrecondition` case fires
  `mountDiverge`, and nothing else fires anything. In particular the other
  cases of its switch on the swap's error leave the mode as it is and fire
  nothing: `store.ErrUnsupported` without the opt-in, which is ADR 0021 §2's
  refusal (M6); `store.ErrUnsupported` with it, the fallback; and
  `case err != nil`. So does every success path, and every other function.
  Since ADR 0022 the one `Fire` is `resolveRoot`'s, on the verdict
  `ProbeTheirs` of its read of the root pointer, made from `commitLocked`
  under the same hold; the `store.ErrPrecondition` case leaves the root in
  doubt instead (ADR 0022 §2, §4), and nothing else fires on the mode.
- **Its readers:**
  - `mutable()` reads `f.mode` and nothing else, and returns:
    - nil for `mountWritable`;
    - `vfs.ErrROFS` for `mountReadOnly`;
    - `vfs.ErrStale` for `mountDiverged`;
    - `vfs.ErrServerFault` for any other value.

    So it is nil exactly when the mode is `Writable` (vocabulary V1), and a
    state added later is refused until its ADR maps it (Assumption 24).
  - `commitLocked` refuses at its top, as now, when `f.mode.State()` is
    `mountDiverged`, storing nothing (M7). On `store.ErrPrecondition` it
    calls `f.mode.Fire(mountDiverge)`. A `Fire` that fails is logged at
    `Error` with §9's keys `machine`, `from`, `event` and `err`, and nothing
    else happens on it: `commitLocked` then logs its `"commit conflict"`
    record and returns the error it returns today, whether the `Fire`
    succeeded or not, since the swap failed whatever the mode was
    (Assumption 38). In this build the failed `Fire` is unreachable: a
    read-only mount's swap is refused by `store.ReadOnly` with
    `store.ErrUnsupported`, never `store.ErrPrecondition` (ADR 0011 §3), and
    a diverged mount is refused at the top of the same hold. It is kept
    because §3 asks it of every owner, and a later edge or a later store
    could reach it.
  - `Access` masks on `mountReadOnly` only, as now: `f.mode.State() !=
    mountReadOnly` where `!f.readOnly` stood, so a `Diverged` mount is not
    masked, as now. Whether it should be is #97's to decide.
  - `Run` starts no committer when `f.mode.State()` is `mountReadOnly`.
  - `loadOrInit` refuses a bucket with no root pointer when `f.mode.State()`
    is `mountReadOnly` (ADR 0011 §2). Its refusal of a root pointer with no
    ETag (ADR 0020 §2) reads no mode.
  - `New` still puts the store behind `store.ReadOnly`, decided from
    `Config.ReadOnly` and not from the mode (ADR 0011 §3).
- **Logging** is §9's: the mode adds no record of a legal `Fire`, and a
  failed one is logged as above.
- **ADR 0018.** The mount mode is security-relevant, since `mutable()` and
  `Access`'s mask read it, and its one edge only narrows and is caused by a
  store's answer, so no policy guards it and no request is named
  (ADR 0018 §5, which says so at `2d7ff34`; nothing here changes it). No
  edge widens: `ReadOnly` and `Diverged` have no edge out. Every `Fire` is
  made holding `FS.mu` for writing or before the FS is shared, which is the
  hold ADR 0018 §5 would ask of a widening edge added later. The conversion
  grants no exemption, and E11 and E12 stand, since the gate and the mask
  still judge the mount and not the caller (ADR 0018 §4). ADR 0018 itself
  needs no revision: its §4 and §5 were written for the converted form.
- **The model.** `docs/tla/CommitProtocol.tla`'s `mode[m]` already holds the
  machine (vocabulary V1, V3). The conversion changes one operator and the
  comments that named the fields, under a `Revised` line of 2026-10-27:
  - `Diverge(m)` moves a mount from `"Writable"` to `"Diverged"` and leaves
    any other mode as it is, as `mountModes` has that one edge and Go's
    `Fire` refuses the rest. Before, it made a read-only mount `"Diverged"`
    too, as `commitLocked` did at `043927f` (the module's old R11, which
    deferred the change to this PR). R11 is rewritten for it.
  - It gets no `Fixes` member (the module's J18; Assumption 40). Only a
    configuration without `ReadOnlyFence` reaches a read-only mount's
    conflict, since a fenced mount's swap never answers
    `store.ErrPrecondition`, and the one such configuration,
    `CommitProtocol_NoReadOnlyFence`, is MustFail on `ReadOnlyNeverWrites`,
    which it violates at its first write call, before any swap. So no row's
    outcome changes, and no configuration is added or flipped.
  - The comments of `Init`, `Diverge` and `SwapConflict` name `FS.mode`
    where they named `FS.readOnly` and `FS.diverged`.
  - `docs/tla/trace.tsv`: the `Mutate` row for `FS.mutable` says its locks
    are none because the mode is an `fsm.Atomic` read in one atomic load,
    and that row, the `Diverge` row and the `Init` row add
    `ADR 0014 section 7` to what decides them. No row is added or removed:
    the table is no action, and `FS.commitLocked`, `FS.mutable` and `New`
    keep their rows.
  - The module's `Checked against` line, and the commits at which
    vocabulary V1 and V3 record the Go names as confirmed, are set once the
    conversion's code is on the branch, by the architect, as ADR 0013 §8
    has it for a refactor of traced code (Assumption 41).
- **Cross-references.** This PR adds `Revised` lines to ADR 0003 (§5's
  `f.diverged`), ADR 0013 (§4's and Assumption 9's `FS.diverged`) and
  ADR 0021 (§2's and §5's `FS.diverged` and `FS.readOnly`), each saying that
  the field named is now the mode's state. They are cross-references only,
  and the quoted words stay, so the tests that quote ADR 0003 §5 and
  ADR 0021 §2 stay valid. `docs/vocabulary.md` V1's entries move to exists,
  and V3's rows that described the two fields now describe `FS.mode`.
- **Not user-visible.** No client or deployment sees a change: the codes
  `mutable()` answers, the records `commitLocked` makes on a conflict, and
  `Config.ReadOnly`'s meaning (ADR 0011 §4) are as they were. No `CHANGES`
  entry (Assumption 39).

### 8. Machines decided elsewhere

- **Root-pointer knowledge**:
  - an `fsm.Machine` under `FS.mu`, which guards every read and write of it;
  - its states and events are the ones `docs/vocabulary.md` V2 reserves:
    `rootKnown`, `rootInDoubt`; `rootSwapOK`, `rootSwapUnknown`,
    `rootProbeOurs`, `rootProbeUnchanged`;
  - `ProbeTheirs` fires `mountDiverge` on the mount mode;
  - its ADR, for #5, names its type, decides its guards and the probe, and
    specifies it in TLA+ first (ADR 0013 §8). That ADR is ADR 0022, which
    names the type `rootState`, keeps V2's names with two meanings widened,
    and decides that the machine is not security-relevant, since
    `mutable()` does not read it.
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
- ADR 0018 §5 says how a refused security-relevant transition is logged.

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

**`blobfs`, for the PR that does §7** (package `blobfs`, in one new file,
`internal/blobfs/mount_mode_test.go`; Assumption 42). The harness is
ADR 0011 §7's: a `*store.Local` under the test's temporary directory, behind
a fake where a test needs one; `SnapshotRetention` −1; `MaxDirtyBytes` −1
where a `Write` is made (ADR 0003 §1); every `New` and every call in a
goroutine that recovers a panic and is waited for under a bound. A test may
use `rootKey` and `snapshotPrefix` (ADR 0011 §7), `f.mode`, `mountModes`,
and the types and constants §7 pins; it may not read or write any other
unexported field. No existing test file changes.

- **M1.** `New` with `Config.ReadOnly` gives `f.mode.State() == mountReadOnly`,
  on a bucket that holds a filesystem (ADR 0011 §2); without it,
  `mountWritable`, on an empty bucket and on one that holds a filesystem.
- **M2.** `mountModes.Name()` is `"mode"`, and `mountModes.Edges()` is exactly
  `[]fsm.Edge[mountMode, mountEvent]{{From: mountWritable, On: mountDiverge, To: mountDiverged}}`.
- **M3.** On a writable mount with one commit behind it and a change since,
  a `Sync` whose swap of `rootKey` the fake fails with
  `store.ErrPrecondition` returns a non-nil error and leaves
  `f.mode.State() == mountDiverged`; a `Write` after it answers
  `vfs.ErrStale`, and a `Create` of a fresh name does too. On a read-only
  mount of a bucket that holds a filesystem, a `Write` answers
  `vfs.ErrROFS`, a `Sync` returns nil (ADR 0011 §4), and `f.mode.State()`
  is `mountReadOnly` after both.
- **M4.** `TestWriteRacingADivergingCommitIsNotADataRace`, in
  `internal/blobfs/diverged_race_test.go` (which landed with #48's fix), is
  unchanged and passes under `-race`.
- **M5.** `String()` gives `"Writable"`, `"ReadOnly"`, `"Diverged"` and
  `"Diverge"`; `mountMode(0).String()` is `"mountMode(0)"`,
  `mountMode(4).String()` is `"mountMode(4)"`, and `mountEvent(0).String()`
  is `"mountEvent(0)"`.
- **M6.** On a writable mount with one commit behind it and a change since,
  without `Config.AllowUnconditionalRoot`, a `Sync` whose swap of `rootKey`
  the fake answers with `store.ErrUnsupported` returns a non-nil error for
  which `errors.Is(err, store.ErrUnsupported)` holds (ADR 0021 §2), and
  leaves `f.mode.State() == mountWritable`; a `Create` of a fresh name after
  it returns nil. The fake passes every other call through, the `Get` of
  `rootKey` and the `Delete` of the refused commit's snapshot included.
- **M7.** After M3's divergence, a second `Sync` returns a non-nil error, and
  the fake counted no `Put`, `PutIfMatch` or `Delete` of any key after the
  swap that failed: a diverged mount's commit is refused before it stores
  anything.
- **M8.** `New`, writable, on an empty bucket behind a fake whose first
  `PutIfMatch` of `rootKey` fails with `store.ErrPrecondition`, returns a nil
  `*FS` and a non-nil error, and does not panic: the machine exists before
  `initEmpty`'s commit can fire `mountDiverge`. What the bucket then holds
  is not pinned. An `*FS` that such a `New` returned is never used
  (ADR 0011 §7).
- **M9.** `mutable()` over every value. For an `*FS` built by a literal that
  sets only `mode`, to `fsm.NewAtomic(mountModes, m)`, `mutable()` returns
  nil for `mountWritable`, `vfs.ErrROFS` for `mountReadOnly`,
  `vfs.ErrStale` for `mountDiverged`, and `vfs.ErrServerFault` for
  `mountMode(0)` and `mountMode(4)`, each compared with `errors.Is`.
  `mutable()` reads nothing else (§7), so the literal is enough.

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
- that a failed `Fire` is logged.

For the PR that does §7, review also shows:

- that `FS.readOnly` and `FS.diverged` are gone, and that nothing writes
  the mode but `commitLocked`'s one `Fire`: ADR 0021's refusal, the
  fallback and `case err != nil` fire nothing (M6 samples the first);
- that a failed `Fire` is logged with §9's keys and the `"commit conflict"`
  record and the error still follow, since no test reaches it;
- that `mutable()`, `Access`, `Run` and `loadOrInit` read the mode where §7
  says, and nothing else reads it;
- that `f.mode` is made before `loadOrInit` and never reassigned, and that
  its doc comment says what §7 asks;
- that `FS.mutable`, `FS.commitLocked` and `New` still perform `Mutate`,
  `Diverge` and `Init` holding the locks their `trace.tsv` rows give
  (ADR 0013 §8), and that the module's `Diverge` is `mountModes`.

## Assumptions

Each is a judgement call, not something the issue, the spec or an earlier ADR
required, except where it says otherwise. Assumption 1 records the owner's
decision 1 and says which part of it is mine. Assumptions 2 to 5 record the
choices the owner delegated to the architect's recommendation (decision 2),
and say so. The owner's decision 3 is recorded in *Context*, and Assumption 31
says how it bears on this ADR.

1. **The scope.** Decision 1, in the owner's words: "Go with architect's
   recommendations. I took a look on the report and it was well argued." The
   reasons in §1's table and for each place left as it is are mine, and so is
   the list of other things left as they are.
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
36. **Read, not run, at `37abe5d`** (§7). The 2026-10-27 revision of §7 was
    checked by reading the code at `37abe5d`, the commit the conversion's
    branch was cut from; nothing was run. The session's own reading of that
    code, given with the task, agreed with mine on every reader and writer
    of the two fields.
37. **One new file, `internal/blobfs/mode.go`** (§7), for the types, their
    `String` methods and the table, next to each other as §3 asks. The name
    is pinned so that coder, test-author and review agree on where the
    machine is; nothing else depends on it.
38. **A failed `Fire` changes nothing that `commitLocked` returns** (§7).
    The swap failed with `store.ErrPrecondition` whatever the mode was, so
    the error the caller needs is the commit conflict, as today; the failed
    `Fire` is a second fact, logged as §3 and §9 ask, not a replacement for
    the first. Its message text is not pinned (Assumption 35).
39. **No `CHANGES` entry for the conversion** (§7): nothing a client or a
    deployment sees changes. The one new log record, for a failed `Fire`,
    is unreachable in this build.
40. **No `Fixes` member for the model's `Diverge`** (§7). A member describes
    the code with and without a fix, and gets a MustFail configuration that
    removes it (ADR 0013 §7). The guarded `Diverge` fixes no defect and is
    told from the old form by no property: the only configuration that
    reaches a read-only mount's conflict has violated `ReadOnlyNeverWrites`
    before any swap, and `NoSelfDivergence` looks only at `"Diverged"`. A
    member would add a configuration pair and a calibration that show
    nothing. The module's J18 says the same.
41. **The commits are recorded after the code lands** (§7). `Checked
    against` names the commit at which the trace was checked against the
    Go code (ADR 0013 §6), and vocabulary V1's exists names the commit the
    name was confirmed at; neither exists while this revision is written,
    so the entries say what they will be, and the architect's pass after
    coder's commit sets them, before review, as ADR 0021's did at
    `8415f61`.
42. **The test file, and M9's form** (§11). The M-tests go in one file,
    named so that coder finds them. M9 builds an `*FS` by a literal, which
    is a unit test of `mutable()` over a machine made once, not the
    reassignment §3 forbids, and is sound because `mutable()` reads nothing
    but `f.mode` (§7).

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

## Consequences

- Two pure standard-library packages arrive, and they change no behaviour in
  this PR.
- Every later machine and permission rule follows this ADR.
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
- NFSv4 state.
- Any TLA+ spec.

## Sources

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
