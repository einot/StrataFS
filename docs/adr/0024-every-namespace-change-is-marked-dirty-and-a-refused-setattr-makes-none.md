# 0024. Every change to the namespace is marked dirty in the hold that makes it, and a refused SETATTR makes none

**Status:** Accepted
**Date:** 2026-10-30
**Revised:** 2026-10-31 — after coder's commit `369cbad`, against the code
at that commit, read and not run. §2 records what coder chose where this
ADR left room: the helpers `setAttrRequests` and `applySetAttr`, the latter
reporting whether it applied anything, which `SetAttr` uses to mark nothing
for an empty call, and `truncate`'s last lines, one `time.Now()` for its
`touchM` and the rest of `sa` (Assumption 22). §5's pass is made: the three
`trace.tsv` rows hold at `369cbad`, and the module's `Checked against`,
`docs/tla/README.md` and vocabulary V3 say so (Assumption 23). §7's D1 sentence is
corrected: at `f8398c4` the dropped entry was never marked, so `FS.dirty`
stays false after the `Remove`, and D1 fails on the verifier and the
exclusive `Create` instead; and the *Red runs* paragraph says, for each
test, which assertion fails there and why, from the code and the tests'
own doc comments. For S1's refused cases `FS.dirty` stays false, but its
empty call and every S2 case do mark it, since `f8398c4`'s `SetAttr` marks
an empty call and its `truncate` marks the size before the refusal; a first
version of this revision wrongly said otherwise of S1 and S2, which
supervisor found (Assumption 24). `vfs.FS.SetAttr`'s doc comment
gained §2's sentence, under the session's authorisation (Assumption 17).
The rule, the checks, the holds, the choice for #144 and the tests'
assertions are unchanged, and the model needs no run for the header's
change.
**Revised:** 2026-10-31, second — after reviewer's review of
`f8398c4..2ef1eb4`, in `docs/` alone (Assumptions 25 to 29). §8's first
`CHANGES` line is corrected: at `f8398c4` a refused call with a size had its
size change, and anything applied with it, committed at the next commit,
since `truncate` marked the namespace before `SetAttr` refused, and a refusal
on the mode occurs too. *Context*'s sentence on the guard is qualified by
#99. §1 brings `bufferWrite`'s tail block under its rule: its unlinked branch
changes nothing and from this ADR marks nothing, which B1 (§7) tests, and
`trace.tsv`'s `FS.bufferWrite` row gains this ADR in `decided`. §6 lists
ADR 0014 §6's stages paragraph, which said no policy is asked at `truncate`'s
second resolve. §7 strengthens S4 (iii), adds S7, a refusal in the exclusive
hold on a file with a buffered write and a pending trim, asks
`setattr_atomic_test.go`'s header to stop saying the second check is not
covered, and asks for `dangling_entry_test.go`'s duplicated helpers to be
replaced by `setattr_atomic_test.go`'s. The rule, the checks, the holds and
every existing assertion are unchanged, and the model needs no run.
**Revised:** 2026-10-31, third — the `Checked against` pass for coder's
`bufferWrite` commit `f6d20e1`: `trace.tsv` row 17 read against it, and the
module's `Checked against` line, `docs/tla/README.md`'s and vocabulary V3's
statements moved to it (§5, Assumption 29). §7 records one accepted
exception to its harness rule, that S7's `bpMustOpen` and `bpSnapshot`
recover no panic (Assumption 30). Nothing else moved; the module changed in
its header comment alone.
**Revised:** 2026-10-31, fourth — reviewer's follow-up. §7's S7 gains a
third refused call, `Size` 0, which reaches the drop loop and its release
that 10 and 4500 do not, and Assumption 28 lists what S7 detects and what
stays review's, the unsettled mark and the growth hole fill (F2);
Assumption 30 is reworded to what `btBounded` does on a timeout (F1).
Nothing else moved.
**Issue:** #143, #144

## Context

**The owner's decisions.** Two, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0023 were, and its design is the architect's (Assumption 1).
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." Neither change here is breaking
   (Assumption 12), so nothing here relies on it.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `f8398c4`, the commit this ADR's branch was cut from, which
is `origin/main` with ADRs 0022 and 0023 merged (Assumption 2). Code is cited
by symbol.

- `SetAttr`, in `internal/blobfs/fs.go`, calls `mutable()`; if `sa.Size` is
  set, calls `truncate`, which applies the size change and returns; then
  takes `FS.mu` for writing, resolves the handle, and for each of `Mode`,
  `UID`, `GID`, `ATime` and `MTime` that is set, checks the field's
  permission inline and applies it at once, so that a refusal on a later
  field returns `vfs.ErrPerm` with the earlier fields applied, and without
  `markDirty`. On success it calls `markDirty` once and returns `n.attr`.
  Its checks are the inline ones ADR 0014 *Context* lists: `c.UID != 0 &&
  c.UID != n.UID` for mode and ownership, `*sa.UID != n.UID` for a give-away,
  `inGroup(c, *sa.GID)` for a group, and `c.UID != n.UID &&
  !permitted(n, c, 2)` for a time. `blobfs` does not import
  `internal/policy`: ADR 0014 §6's wiring, `FS.perm` and `objectOf`, has not
  happened, and `permitted` and `inGroup` are still the package's checks.
- `truncate` fails a size above `maxFileSize` with `vfs.ErrFBig` as its
  first statement (ADR 0007 §4); holding `FS.mu` for reading, resolves the
  handle and refuses a directory with `vfs.ErrIsDir` and a caller without
  write permission with `vfs.ErrAcces`; releases; takes the commit gate for
  reading, `getOpen`, the file's `openFile.mu` and `FS.mu` for writing;
  resolves again; applies the size change under ADR 0006 §3's rules; sets the
  unsettled mark when it cuts into a stored chunk (ADR 0012 §3); calls
  `touchM` and `markDirty`; and returns. Only `SetAttr` calls it.
- `unlink`, when the directory's entry names an inode the table lacks
  (`n == nil`), runs `delete(d.Entries, name)` and returns `vfs.ErrNoEnt`,
  holding `FS.mu` for writing, with no `markDirty` and no `touchM`. `Rename`
  returns `vfs.ErrNoEnt` for such a source entry and changes nothing, and
  replaces such a target entry, with no `relink`, as ADR 0017 §2 pins.
  `createEntry` answers an exclusive create, a `Mkdir` or a `Symlink` of such
  a name with `vfs.ErrExist`, and a non-exclusive create with `vfs.ErrNoEnt`.
  `Lookup` answers `vfs.ErrNoEnt`. `ReadDir` skips the entry and hashes its
  name into the listing's verifier. `decodeSnapshot` checks that no inode is
  nil and that every directory has an entry map, and nothing else, so a
  snapshot another build or a corruption wrote can hold such an entry; no
  operation in this build makes one (ADR 0017, Assumption 14).
- `markDirty` sets `FS.dirty` and advances `FS.changes` in one hold of
  `FS.mu` for writing (ADR 0023 §3); `Sync` returns nil at its top when
  `FS.dirty` is false, making no store call (ADR 0023 §2).
- `internal/policy` has `Unix()`, whose rows `OpTruncate`, `OpChmod`,
  `OpChown`, `OpChgrp` and `OpSetTimes` are, for every request `blobfs`
  would make, exactly the inline checks above (ADR 0014 §5), tested
  exhaustively by its P-tests.
- `internal/nfs`'s `setattr` handler checks a guard's ctime against
  `GetAttr` before it calls `SetAttr`, and answers a refused call with the
  status `statusOf` gives and `wcc_data` read before and after the call.

**The defects.** As the session gave them (Assumption 2):

- **#143.** `SetAttr` applies each requested field in turn and checks each
  just before applying it, so a later refusal leaves the earlier fields
  applied, uncommitted: an owner who is not root sends mode `0600` and uid
  `0`; the mode is set, the give-away is refused with `NFS3ERR_PERM`, and
  `GETATTR` then shows `0600`, which reaches the bucket only if an unrelated
  change later dirties the namespace and is otherwise lost at restart. The
  same for `GID`, `ATime` and `MTime` after an earlier field, and a size is
  applied earlier still, by `truncate`, before any mode or ownership check.
  Its done-when: a `SETATTR` refused on any field leaves every attribute as
  it was; a test shows the mode-then-uid case leaves the mode unchanged and
  the namespace clean; whether a size change can be checked before the
  truncate is decided, or the ordering documented.
- **#144.** After `unlink`'s removal of an entry that names no inode, the
  in-memory namespace differs from the one the root pointer names, but
  `FS.dirty` does not say so: a `Sync` with nothing else dirty commits
  nothing, the removal is undone at the next mount, and the listing's
  verifier changes while the directory's mtime does not. Its done-when: the
  chosen fix is in, and `unlink` makes no change to the namespace without
  `markDirty` in the same hold; a test loads a snapshot holding such an
  entry, calls `Remove` of the name, asserts `ErrNoEnt`, and then what the
  choice implies; the test's setup has a way to write such a snapshot;
  ADR 0023 §8 strikes the path under a `Revised` line, and E13's row still
  restates §8; `CHANGES` gains a line only if the fix is observable.

ADR 0014 *Context* lists #96, "A refused SETATTR can keep part of its change,
uncommitted", among the behaviours its §6 keeps as they are, "until the ADR
that settles #96 decides otherwise". That is #143's defect under an earlier
number, so far as the two titles say; this ADR is the one ADR 0014 §6 waits
for, and the session reconciles the two issues (Assumption 16).

**What RFC 1813 says, and what it does not.** #143's *Expected* says that
RFC 1813 §3.3.2 "says SETATTR either succeeds or leaves the attributes
unchanged". It does not. Its IMPLEMENTATION paragraph says the opposite:
"SETATTR is not guaranteed atomic. A failed SETATTR may partially change a
file's attributes." (*Sources*). The one case the RFC does make atomic is the
guard's: "If guard.check is TRUE, the server must compare the value of
guard.obj_ctime to the current ctime of the object." and "If the values are
different, the server must preserve the object attributes and must return a
status of NFS3ERR_NOT_SYNC." (*Sources*), which `internal/nfs` checks before
it calls `SetAttr`, against a `GetAttr` of its own in a hold of its own, so
that case preserves the attributes modulo #99: a change that lands between
the handler's `GetAttr` and its `SetAttr` passes the guard, which #99 owns
and §2 leaves untouched (Assumption 27). ADR 0012
§2 cited the same sentence to allow a `SETATTR`'s size and other attributes
to land in two commits. So the atomicity this ADR decides rests on this
repository's own rule, not on the RFC, which permits either; a server may do
better than the minimum the RFC allows, and this one does, for the reasons §2
gives (Assumption 3). This is a finding against the issue's stated rationale,
not against its ask, and the session may want to correct the issue's text.

**What the ADRs say.** ADR 0023 §8 exempts `FS.dirty` from the state rule,
E13, on three facts, the third being that every hold of `FS.mu` for writing
that changes the namespace calls `markDirty`, which it states as holding "at
every site but two", names both with their issues, and asks the PR that
fixes each to strike its site under a `Revised` line (its Assumption 38; its
*What this does not decide* lists #144's question). ADR 0014 §6 keeps
`SetAttr`'s per-field check and apply as one of three exceptions until this
ADR, and pins the requests `SetAttr` makes, "each before the attribute it
guards is applied, so a later check sees the earlier attributes already
applied"; its §11 gives W1 to W3 for the PR that wires `f.perm`. ADR 0017 §2
pins what becomes of an entry that names no inode, "which `unlink` deletes,
answering `vfs.ErrNoEnt`, and `Rename` replaces" (its Assumption 14), and its
§5 decides no repair of what earlier builds wrote. ADR 0012 §2 pins that
`truncate` takes the gate after the checks it makes holding `FS.mu` for
reading, so that a refused size change never waits for it, and allows the
two-commit case above. ADR 0007 §4 pins the order of errors and that
`truncate`'s first statement is the size check. ADR 0018 §1 to §3 make the
policy rule standing for new and changed code, and its §5 gives the shape of
a check in a method whose change lands in a later hold: the permit from a
read hold "does not carry over", and the later exclusive hold "resolves its
handles and asks the policy again". ADR 0011 §7 and ADR 0022 §8 give the
test safety rules and the names a test may use, which ADR 0023 §11 extends.
ADR 0006's trim rules are untouched.

**What this constrains.** `SetAttr`, `truncate`, `unlink` and
`bufferWrite`'s tail block in `internal/blobfs/fs.go`; `FS`, which gains
`perm`; `blobfs`, which from this ADR imports `internal/policy`;
`vfs.FS.SetAttr`'s contract (§2); ADR 0014 §6's `SetAttr` bullets and its
stages paragraph, ADR 0017 §2's bullet and ADR 0012 §2's paragraph, by
cross-reference; ADR 0023 §8 and the register's E13 (§4);
`docs/tla/trace.tsv`'s `decided` column for four rows and, after the code
lands, the module's `Checked against` line (§5). Nothing on the wire, no
object format, no flag and no `store.Store` method changes.

## Decision

### 1. The rule

**Every change to the namespace is made in one hold of `FS.mu` for writing
that calls `markDirty`, whatever the call then returns.** So `FS.dirty` and
`FS.changes` record every change, and a `Sync` that finds the flag clear is
right that the bucket holds the namespace as it stands. This is ADR 0023 §8's
third fact, stated without qualification, and from this ADR it is the
package's rule rather than a fact review happens to find: a path that changes
the inode table, an entry map, an attribute, a size or a chunk list and does
not call `markDirty` in the same hold is a defect.

Two things follow for the methods that change the namespace by a caller's
request. **A `vfs.FS` method of `blobfs` that returns an error has changed
nothing in the namespace**, with the exceptions that already keep the rule:
a `Write` with `vfs.FileSync` or `vfs.DataSync`, and a `Commit`, can return
an error after their bytes were buffered, their size set or their chunk list
repointed by a flush, each of which was marked dirty in the hold that made
it, which is what the next commit needs (ADR 0023 §3). And **`markDirty` is
called exactly where a change is made**: a call that applies nothing marks
nothing (§2, the empty call; Assumption 9).

§2 and §3 bring the two paths ADR 0023 §8 named under the rule: `SetAttr`
by applying nothing when it refuses, and `unlink` by making no change on the
path that returns `vfs.ErrNoEnt`. §4 strikes them from ADR 0023 §8.

One more site marks where nothing changes. `bufferWrite`'s tail block,
holding the file's `openFile.mu` and `FS.mu` for writing, resolves the handle
again: if the inode is there it sets the size and the times, and if it is
gone, unlinked while the write buffered, it drops the `openFile`'s buffers
and releases their charge (ADR 0003 §2, site 2), which changes no inode,
entry, attribute, size or chunk list. At `f8398c4` and at `369cbad` it calls
`markDirty` after both branches, so the second marks a change that is not
one, and the next `Sync` can commit for nothing. From this ADR the call is
made on the first branch only: the second changes nothing and marks nothing
(Assumption 25). The over-mark is reachable, not only in principle: `unlink`
drops the file's `FS.open` entry as it deletes the inode, so a `Sync` begun
between the unlink and the tail block flushes nothing of that file, commits
the removal and clears the flag before the block runs, and the block's mark
then costs an empty commit, which B1 (§7) shows. The branch is a hold of
`FS.mu` for writing that changes nothing, so ADR 0023 §8's third fact, on
which E13 rests and which §4 restates, is untouched by the move.

### 2. `SetAttr` applies all of `sa` or none

**The contract.** `SetAttr` either applies every attribute `sa` sets, in one
hold of `FS.mu` for writing, or applies none: a call that returns an error has
changed no attribute, no size, no time and no chunk list, and has left
`FS.dirty` and `FS.changes` as they were. A call that returns nil has applied
all of `sa`, and the attributes it returns are the inode's after the change.
`vfs.FS.SetAttr`'s doc comment says so, in these words, after its sentence on
`ErrFBig` (Assumption 17):

> SetAttr applies every attribute sa sets or none of them: a call that
> returns an error has changed nothing, and one that returns nil has applied
> all of sa.

**The checks are the policy's.** `FS` gains the field ADR 0014 §6 pins,
`perm policy.Policy`, which `New` sets to `policy.Unix()` and nothing else
writes, and which a test may replace after `New` returns and before first
use; and `objectOf(n *inode) policy.Object`, which builds an `Object` from an
inode's `Type`, `Mode`, `UID` and `GID`. `SetAttr` asks `f.perm` for every
attribute it sets, in this order, and returns the first refusal unchanged,
making no request after it (Assumption 6):

1. `OpTruncate`, if `sa.Size` is set;
2. `OpChmod`, if `sa.Mode` is set;
3. `OpChown` with `UID` `*sa.UID`, if `sa.UID` is set;
4. `OpChgrp` with `GID` `*sa.GID`, if `sa.GID` is set;
5. `OpSetTimes`, if `sa.ATime` is set;
6. `OpSetTimes`, if `sa.MTime` is set.

Every request's `Object` is `objectOf(n)` for the inode as it stands at the
call, since nothing is applied until every request has been allowed; its
`Caller` is the call's. The list is ADR 0014 §6's, in its order, with the
sentence "each before the attribute it guards is applied, so a later check
sees the earlier attributes already applied" replaced by this one: every
request is made before any attribute is applied. For `Unix()` that changes
no verdict (Assumption 20): `OpChmod`, `OpChown` and `OpChgrp` read the
owner alone, which only root may change, and root passes every row;
`OpSetTimes` reads the owner and the mode, and a caller who passed `OpChmod`
is root or the owner, whom `OpSetTimes` allows whatever the mode; so no
attribute an earlier request guards can change a later request's answer. The
inline checks at `f8398c4` are gone from `SetAttr` and `truncate`; `permitted`
and `inGroup` stay for the methods ADR 0014 §6's wiring has not reached
(§4). No policy is asked for an attribute `sa` does not set.

**Without a size.** `SetAttr` calls `mutable()` (stage A); takes `FS.mu` for
writing; resolves the handle (stage C); makes the requests (stage D); applies
every attribute `sa` sets; calls `markDirty`; and returns `n.attr`. One hold,
as at `f8398c4`, with the requests moved ahead of every apply.

**With a size.** `SetAttr` calls `mutable()` and then `truncate`, which keeps
its name, its locks and the `TLA+: … Mutate` line of its doc comment, and
takes `sa` whole, with `sa.Size` set, returning the attributes
(Assumption 5):

```go
// truncate is SetAttr's path for a call that sets a size: it checks every
// attribute sa sets, applies the size change and then the rest of sa in one
// hold of mu for writing, under the commit gate and the file's openFile.mu,
// and marks the namespace dirty once (ADR 0024 §2). sa.Size must be set.
func (f *FS) truncate(ctx context.Context, c vfs.Caller, h vfs.Handle, sa vfs.SetAttr) (vfs.Attr, error)
```

In order:

1. Its first statement fails `*sa.Size` above `maxFileSize` with
   `vfs.ErrFBig`, before any lock, as ADR 0007 §4 pins (stage B).
2. **The pre-check**, holding `FS.mu` for reading: resolve the handle and
   make every request of the list above. A refusal returns here, holding
   nothing, so that a call `SetAttr` would refuse, for the size or for any
   other attribute, never waits for the commit gate and opens no buffer,
   which is ADR 0012 §2's pin for the size's own checks, now for every
   attribute (Assumption 4). The permit does not carry over (ADR 0018 §5).
3. Take the commit gate for reading, `getOpen`, the file's `openFile.mu` and
   `FS.mu` for writing, as at `f8398c4` and in that order (ADR 0012 §2, §5).
4. **The check that counts**, in that hold: resolve the handle again, which
   can still answer `vfs.ErrStale`, and make every request of the list
   again, against the inode as it now stands. A refusal returns from this
   hold with nothing applied. This is the shape ADR 0018 §5 gives a method
   whose change lands in a later hold, and it answers #101 for `SetAttr`
   alone (Assumption 4).
5. Apply the size change as `truncate` applies it at `f8398c4`: ADR 0006
   §3's rules on the dirty chunks, the trims and the chunk list, the budget's
   release, the unsettled mark (ADR 0012 §3), `n.Size` and `touchM`. Nothing
   here moves.
6. Apply every other attribute `sa` sets, in the hold, as the no-size path
   does.
7. Call `markDirty` once; return `n.attr`.

So the size and the rest of `sa` land in one commit or none: a `SETATTR`
with a size returned before a commit phase began is in that commit whole,
under ADR 0012 §1's Rule D, which replaces ADR 0012 §2's two-commit case.
`SetAttr` itself takes no lock on this path and applies nothing after
`truncate` returns.

**The applies, and the times** (Assumption 8). In the hold, in this order,
each as at `f8398c4`: the size, with `touchM`; `Mode`, masked to `0o7777`,
with `touchC`; `UID` with `touchC`; `GID` with `touchC`; `ATime`, set to the
value given, with `touchC`; `MTime`, set to the value given, with `touchC`.
One `time.Now()` serves the hold. So an `MTime` the call sets wins over the
mtime the size change sets, as it did, and a call that sets a size and no
`MTime` leaves the size change's mtime.

**The empty call** (Assumption 9). A `SetAttr` that sets no attribute runs
stages A and C, makes no request, applies nothing, calls no `markDirty`, and
returns the attributes with nil. At `f8398c4` it marked the namespace dirty
for nothing.

**The order of errors** is ADR 0007 §4's: `mutable()`'s `vfs.ErrROFS` or
`vfs.ErrStale`; `vfs.ErrFBig`; the resolve's `vfs.ErrBadHandle` or
`vfs.ErrStale`; then the requests in the order above, so for `Unix()` a
size's `vfs.ErrIsDir` or `vfs.ErrAcces` comes before a mode's or an owner's
`vfs.ErrPerm`. `SetAttr` has no stage E. A refusal is the policy's bare
`vfs.Status`, unwrapped, and is not logged (ADR 0018 §5).

**What a refused call leaves.** Nothing changed: not the attributes, the
size, the times, the chunk list, any dirty buffer or pending trim, the
unsettled mark, `FS.dirty` or `FS.changes`; and the next `Sync`, with nothing
else dirty, makes no store call. A call refused in the pre-check, or without
a size, took no gate and opened no buffer. A call refused in the exclusive
hold, which only a change made between the two checks can cause, may leave
the `FS.open` entry `getOpen` made, holding nothing, as a second resolve
that answers `vfs.ErrStale` leaves one at `f8398c4`; ADR 0005 §3's invariants
allow an entry with nothing in it, and `flushOpen` returns early on it
(Assumption 19).

**What is unchanged.** `Unix()`'s rows, pinned and unpinned, and so every
verdict (Assumption 20): the unpinned rows U1, U3 and U6 of ADR 0014 §11 are
as they were, a symlink's handle is still accepted with a size (#69), and a
non-owner with write permission may still set the times (#100). The NFS
layer's guard check (#99), `createEntry`'s handling of `sa` (#98), `Write`'s
check before its wait (#101) and `Access` are untouched. ADR 0006's trim
rules, ADR 0012's gate, mark and settle pass, and ADR 0003 §2's accounting
at `truncate` (site 3) are untouched.

**What coder's commit chose**, recorded against `369cbad` where the pins
above left room (Assumption 22):

- `setAttrRequests(c vfs.Caller, n *inode, sa vfs.SetAttr) error`, a method
  on `*FS` that builds one `Object` with `objectOf(n)` and asks `f.perm`
  the six requests in order, returning the first refusal; and
  `applySetAttr(n *inode, sa vfs.SetAttr, now time.Time) bool`, a plain
  function that applies every attribute but the size, in the order above,
  and reports whether it applied any. `SetAttr`'s no-size path calls
  `markDirty` only when it reports true, which is how the empty call marks
  nothing; `truncate` calls `markDirty` unconditionally, since a size set
  is a change.
- `truncate`'s last lines are `now := time.Now()`, `n.touchM(now)`,
  `applySetAttr(n, sa, now)`, `f.markDirty()`, `return n.attr(f.fsid), nil`,
  in place of `n.touchM(time.Now())`, `f.markDirty()`, `return nil`: the one
  edit inside the size-change body, made so that one time serves the hold.
  Everything above those lines in the body is as at `f8398c4`.
- `FS.perm`'s doc comment says that `SetAttr` is the first method that asks
  it and that the rest still check inline until #114 moves them; `New`
  sets it in the literal that builds the `FS`, before `loadOrInit`.

**The policy rule.** This is changed code that decides what a caller may do,
so ADR 0018 §1's policy rule binds it, and the checks go to `f.perm` rather
than being exempted (Assumption 6). It is the first step of ADR 0014 §6's
wiring, which #114 plans for the other methods; `New` installs `Unix()`
alone, and ADR 0014 §6's rule that every installed policy keeps `Unix()`'s
type rows binds from here. No machine is involved and no transition is
security-relevant, so ADR 0018 §5's link adds nothing. `unlink`'s permission
check is not changed code: §3 edits a branch beside it, which ADR 0018 §3
does not count, so it stays inline until #114's PR.

### 3. An entry that names no inode is reported absent and left as it is

**The rule.** `unlink`, for `Remove` and `Rmdir`, when the directory's entry
names an inode the table lacks, returns `vfs.ErrNoEnt` and changes nothing:
no `delete`, no `touchM`, no `markDirty`. This is #144's choice (b), the
behaviour `Rename` has for such a source entry, and from this ADR the two
agree (Assumption 10). ADR 0017 §2's "which `unlink` deletes" is replaced by
this section; "`Rename` replaces" stands.

**What a client sees**, for a name whose entry names no inode, which only a
snapshot another build or a corruption wrote can hold:

| Call | At `f8398c4` | Since this ADR |
|---|---|---|
| `LOOKUP` | `NFS3ERR_NOENT` | the same |
| `READDIR` | the name is not listed, and is hashed into the verifier | the same, and the verifier is stable across a refused `REMOVE` |
| `CREATE` exclusive, `MKDIR`, `SYMLINK` | `NFS3ERR_EXIST` | the same |
| `CREATE` unchecked or guarded | `NFS3ERR_NOENT` | the same |
| `REMOVE`, `RMDIR` | `NFS3ERR_NOENT`, and the entry is dropped in memory, uncommitted, with the directory's mtime untouched and its verifier changed | `NFS3ERR_NOENT`, and nothing changes |
| `RENAME` from the name | `NFS3ERR_NOENT`, nothing changes | the same |
| `RENAME` onto the name | the entry is replaced by the moved object, the change marked dirty and committed | the same (ADR 0017 §2) |

**How a client makes the name usable.** A `RENAME` of another object onto
the name replaces the entry, is marked dirty, and is in the next commit, as
ADR 0017 §2 pins and D2 (§7) tests. So the recovery is: create the object
under another name and rename it onto this one. Nothing else clears such an
entry in this build, and no operator tool does (*What this does not decide*).

**Why (b).** Three reasons (Assumption 10). First, one rule for the package:
§1 holds that a method that returns an error has changed nothing, and with
(a) `unlink` would be the one method whose `vfs.ErrNoEnt` meant "I changed
the directory", an exception in the ADR that states the rule, and one review
would have to carry. Second, a repair made as the side effect of a refused
call decides, in passing, that a mount repairs what a snapshot holds, which
ADR 0017 §5 decided against for directories earlier builds leaked; if such
entries are to be repaired, the place is the load, once, logged, with the
read-only mount answered (*What this does not decide*), not whichever call a
client happened to make. Third, (a)'s one argument, that `Remove` was "the
only way a client can make the name creatable again", does not hold:
`Rename` onto the name already replaces it, by ADR 0017 §2, and that path
marks the namespace dirty.

**What (b) costs.** The entry persists until renamed over: an exclusive
create, a `Mkdir` or a `Symlink` of the name keeps answering
`NFS3ERR_EXIST` while `LOOKUP` answers `NFS3ERR_NOENT`, as at `f8398c4`
between a restart and a client's `rm`. Whether `createEntry` should treat
such an entry as absent, as `Rename` does, is left open (*What this does not
decide*).

### 4. ADR 0023 §8, E13 and the register

With §2 and §3, every hold of `FS.mu` for writing that changes the namespace
calls `markDirty`, at every site: ADR 0023 §8's third fact holds without
qualification, which §1 states as the rule. ADR 0023 §8 strikes both paths
from its list under a `Revised` line, as it asked the fixing PR to do, and
its Assumption 38, *Consequences* and *What this does not decide* gain a
sentence each pointing here. E13's row in `docs/adr/exemptions.md` gains
this ADR under *Granted by*, as ADR 0018 §4 has a row gain the ADR that
restates its exemption with a reason that still holds: the three reasons
ADR 0023 §8 gives stand, and the third fact they rest on now holds whole.
E13 is neither revoked nor widened; `FS.dirty` is still exempt from the state
rule, `markDirty` is still its one setter and `applyLocked` its one clearer.

Nothing here adds state with named states, so the state rule reaches
nothing new (ADR 0018 §2). The policy rule reaches `SetAttr`'s checks, which
§2 moves (Assumption 6). No exemption is granted.

### 5. The model

No module models either defect, and none is added (Assumption 13).
`docs/tla/CommitProtocol.tla`'s `Mutate` folds `mutable()` with the hold of
`FS.mu` in which a change sets `FS.dirty` (its R3, R4), and every action of
the module that changes the namespace sets `nsDirty`: a change that leaves
the flag clear is no step of the model, so the model already describes the
code as §1 has it, and the gap at `f8398c4` was between the trace and the
code, which ADR 0013 §8 gives to review. `StrayDirty` models a read-only
mount's stray write (its J6), not this. No `Fixes` member, no configuration
and no property is added; `DirtyUntilCommitted`, the Unlocked rows and
U1 to U12 are about commits and stand. The `docs/tla/manifest.tsv` rows
stand, every one calibrated.

`docs/tla/trace.tsv` changes in its `decided` column only, now: the rows for
`FS.SetAttr`, `FS.unlink` and `FS.truncate` cite this ADR, and their `locks`
columns stand, since `SetAttr` still performs `Mutate` holding `FS.mu` for
writing, `unlink` likewise, and `truncate` holding the gate for reading, the
file's `openFile.mu` and `FS.mu` for writing. The pre-check's read hold is
before the action, not around it. The module's `Checked against` line,
`docs/tla/README.md`'s and `docs/vocabulary.md` V3's statements of it are set
once coder's commit is on the branch, by the architect, who reads those
three rows against it (ADR 0013 §8; ADR 0023 Assumption 23), and that pass,
not this one, is what makes the PR's `tla.sh full` run due for the header's
change; the `decided` edit alone already makes it due under CLAUDE.md's
merge bar. The pass was made on 2026-10-31 against `369cbad`: `SetAttr`
performs `Mutate` holding `FS.mu` for writing, through `markDirty`, on its
no-size path, and hands a sized call to `truncate`; `unlink` holds `FS.mu`
for writing from its resolve to its `markDirty`, and its dangling branch
returns before any change; `truncate` makes its change and its `markDirty`
holding the gate for reading, the file's `openFile.mu` and `FS.mu` for
writing, taken in that order after its pre-check's read hold has been
released; and all three keep their `TLA+: … Mutate` lines. The three lines
read `369cbad` from that pass until the second pass below moved them
(Assumption 23).

`docs/tla/trace.tsv`'s row for `FS.bufferWrite` gains this ADR in its
`decided` column too, beside ADR 0003 §3, since §1 decides that its unlinked
branch marks nothing; its `locks` column stands, the `Mutate` still being
performed holding `openFile.mu` and then `FS.mu` for writing, on the branch
that resolves the inode (Assumption 29). Coder's commit for that branch is a
refactor of traced code under ADR 0013 §8, so the architect reads the row
against it and moves the module's `Checked against` line, `docs/tla/README.md`'s
and vocabulary V3's statements to that commit, a second pass of the kind
above; `tla.sh full` is due on the PR either way. That pass was made on
2026-10-31 against `f6d20e1`, read and not run: `bufferWrite` takes the
file's `openFile.mu` at its top and releases it by defer, takes `FS.mu` for
writing for its tail block, and in that hold calls `markDirty` once, on the
branch whose second resolve succeeds, after the size and the times, and on
no other; the unlinked branch drops the buffers, releases the charge and
reaches the unlock with no mark, its comment saying so; the function's
`TLA+: … Mutate` line stands, and nothing else in it moved. The three
lines read `f6d20e1` (Assumption 29).

### 6. Cross-references

This ADR replaces, under `docs/adr/README.md`'s rule for a part a later ADR
replaces, with a `Revised` line and one sentence each:

- **ADR 0014 §6**: the exception "`SetAttr` checks and applies each attribute
  in turn … until the ADR that settles #96 decides otherwise", and the
  `SetAttr` bullet under *The requests each method makes*, whose "each before
  the attribute it guards is applied" is since this ADR "every request before
  any attribute is applied", twice for a call with a size; and its stages
  paragraph's sentence that at `truncate`'s second resolve "no policy is
  asked there, and whether one should be is #101's question", since this ADR
  every request is made there again (§2, step 4), which answers #101 for
  `SetAttr` alone and leaves `Write`'s check to #101.
- **ADR 0017 §2**: "An entry that names no inode is removed or replaced as
  now": `unlink` leaves one, `Rename` replaces one.
- **ADR 0012 §2**: the paragraph allowing a `SETATTR`'s size and other
  attributes to land in two commits; since this ADR they land in one.
- **ADR 0023 §8**, by the strike §4 describes, and its Assumption 38,
  *Consequences* and *What this does not decide*.

ADR 0007 §4 stands as written: `truncate`'s first statement is still the
size check, and `SetAttr` still calls it "after `mutable()` and before it
changes any attribute", which is why `truncate` keeps the check and the name
(Assumption 5). ADR 0018 is not revised: nothing in it becomes false, its §3
anticipates a change bringing one method's checks under the policy rule
before #114's PR, and its §5's holds for `SetAttr` are the ones §2 makes
(Assumption 18). ADR 0006, ADR 0003 and ADR 0005 are unchanged.
`docs/DESIGN.md`'s table of ADRs gains this ADR's row, and `docs/adr/README.md`'s
index does.

### 7. The test surface

Every name is spelled as in Go. test-author cannot read `internal/blobfs`'s,
`internal/policy`'s or `internal/store`'s non-test files, so everything a
test may use is here, or in a `_test.go` file it may read.

**Names a test may use**, restated from the code at `f8398c4` unless marked
new, beside those ADR 0023 §11, ADR 0022 §8 and ADR 0011 §7 list, which
stand:

```go
// internal/blobfs
func New(ctx context.Context, cfg Config) (*FS, error) // Config as ADR 0023 §11 gives it
// GetAttr, SetAttr, Lookup, Access, Create, Mkdir, Symlink, Remove, Rmdir,
// Rename, ReadDir, Read, Write, Sync and MaxFileSize, as internal/vfs/vfs.go declares them
func quietLog() *slog.Logger          // internal/blobfs/fs_test.go
var testCaller = vfs.Caller{UID: 501, GID: 20} // internal/blobfs/fs_test.go: the root directory's owner

// unexported, at f8398c4
type FS struct {
    mu      sync.RWMutex
    dirty   bool   // under mu; a test reads it holding mu for reading (ADR 0022 §8)
    changes uint64 // under mu; the same (ADR 0023 §11)
    // ...
}
const (
    rootKey        = "root"
    snapshotPrefix = "snapshots/"
    chunkPrefix    = "chunks/"
    rootInodeID    = 1
)
type rootPointer struct { Version int; Epoch uint64; Snapshot string; FSID uint64; WrittenNS int64; Writer string } // json tags as ADR 0022 §8 gives them
func decodeSnapshot(b []byte) (*snapshot, error)
func encodeSnapshot(s *snapshot) ([]byte, error) // new to this list: the inverse of decodeSnapshot, the bytes of an object under snapshotPrefix
type snapshot struct { Version int; Epoch uint64; NextIno uint64; ChunkSize uint32; Inodes map[uint64]*inode; CreatedNS int64 }
type inode struct { ID uint64; Type vfs.FileType; Mode, UID, GID uint32; Size uint64; Entries map[string]uint64 /* ... */ }

// new in this ADR (§2)
// FS gains perm policy.Policy: New sets it to policy.Unix(); a test may
// replace it after New returns and before any other call on the FS, and
// may not write it afterwards.
```

```go
// internal/policy (ADR 0014 §5), unchanged
type Perm uint32
type Object struct { Type vfs.FileType; Mode uint32; UID, GID uint32 }
type Op uint8
const ( OpRead Op = iota + 1; OpWrite; OpTruncate; OpChmod; OpChown; OpChgrp; OpSetTimes; OpLookup; OpReadDir; OpAddEntry; OpRemoveEntry; OpRename )
type Request struct { Op Op; Caller vfs.Caller; Object Object; Dst Object; UID uint32; GID uint32 }
type Policy interface { Check(r Request) error }
type Func func(r Request) error // a Func's Check calls it once with the request unchanged
func Unix() Policy
```

A test may also read and reuse `uc43Store` and its helpers in
`internal/blobfs/unlocked_commit_test.go`, which record every store call,
and the harness of `internal/blobfs/commit_window_test.go` and
`backpressure_test.go`: `bpLocal`, `bpSeedFile`, `bpStore` and its `setPut`,
`bpNew`, `bpLookup`, `cwMount` with `createHeld`, `holdSync` and
`truncateA`, `newBTGate`, `bpHook`, `cwKey`, `cwCut` and `bpCS`, which
between them hold a `Sync` in its first pass or its settle pass, as
`TestCommitWindowTruncateDuringSettleIsWholeOrAbsent` does; and, for S7 and
B1, `bpOpen`, `bpMustOpen`, `bpSnapshot` and `bpOFSnap` with its `indices`,
which read an `openFile`'s buffer indices and capacities, its charge and the
count of its pending trims under its `mu` (ADR 0003 §4's test surface), and
`bpStore` with `setGet`, `bpHook`'s `key`, `entered` and `gate` fields, and
`bpIsChunkKey`, which hold a `Get` of a chunk. `bpStore` embeds a
`store.Store`, so it can wrap a `*uc43Store`, which records what passes
through it.

**Facts a test may rely on.**

- The facts of ADR 0022 §8 and ADR 0023 §11 stand: what a *commit* is; that
  one that succeeds writes one object under `snapshotPrefix` and then one
  `PutIfMatch` of `rootKey`; a `*store.Local`'s ETags; that a `Sync` on a
  mount with no change since the last seal returns nil and makes no store
  call; what a test may read holding `FS.mu` for reading.
- A refused `SetAttr` leaves `FS.dirty` and `FS.changes` as they were, and
  every attribute `GetAttr` reports (§2). A `SetAttr` that returns nil has
  applied every attribute it set, and advanced `FS.changes` by at least one.
  An empty `SetAttr` returns nil and advances nothing.
- A regular file `Create` makes with no `Mode` has mode `0o644`; a `Mkdir`
  with none, `0o755`; the owner is the caller's uid and gid.
  `vfs.Caller{UID: 501, GID: 20}` owns the root directory of every mount
  with `OwnerUID` 501 and `OwnerGID` 20. `vfs.Caller{UID: 0}` is root.
  `vfs.Caller{UID: 502, GID: 30}` is neither owner nor group member of such
  a file and has read permission on it only.
- With `f.perm` replaced by a `policy.Func` that records what it is asked, a
  `SetAttr` makes exactly §2's requests in §2's order, stopping at the first
  refusal, each with `Object` built from the inode's `Type`, `Mode`, `UID`
  and `GID` as they stood when the call began; for a call with a size the
  list is made twice, the second time after the first has been allowed
  whole. `vfs.ErrNXIO` is a status no row of `Unix()` returns, so a
  `SetAttr` that returns it was refused by the test's policy (ADR 0014 §11,
  W2; ADR 0018 §6, T1). A test that replaces `f.perm` with a policy that
  allows what `Unix()` refuses makes its calls on a regular file only
  (ADR 0014 §11's safety rule).
- **A snapshot holding an entry that names no inode** is made so: on a
  fresh mount, `Create` a file `ghost` by `testCaller` and `Sync`; read the
  root pointer with `Get(rootKey)` on the `*store.Local` and decode it as
  `rootPointer`; `Get(rp.Snapshot)` and `decodeSnapshot`; take `ghost`'s
  inode number from `snap.Inodes[rootInodeID].Entries["ghost"]`; `delete`
  that number from `snap.Inodes`; `encodeSnapshot(snap)`; `Put` the bytes
  under `rp.Snapshot` on the `*store.Local`, which overwrites the object the
  root pointer names, so the root pointer and its ETag are unchanged; drop
  the first `*FS` and never use it again (ADR 0011 §7); `New` on the same
  store. The new mount holds the entry: `Lookup` of `ghost` answers
  `vfs.ErrNoEnt` and an exclusive `Create` of `ghost` answers
  `vfs.ErrExist` (§3's table). The doctored snapshot decodes, since
  `decodeSnapshot` checks only nil inodes and directory entry maps
  (*Context*).
- A `Rename` of a file onto such a name returns nil and replaces the entry,
  marking the namespace dirty (§3; ADR 0017 §2).
- While a `Sync`'s settle pass holds its upload of a shortened chunk, the
  commit gate is held for writing (ADR 0012 §2; ADR 0023 §2), and a
  `SetAttr` with a size that passes its checks waits for it; one the policy
  refuses does not (§2).
- `uc43Config` sets `ChunkSize` 4096, so a file of 6000 bytes written at 0
  and committed is stored as two chunks, of 4096 and 1904 bytes. A `Write`
  that covers part of a stored chunk loads that chunk before it overlays the
  bytes, from the mount's cache if it holds it (ADR 0004) and otherwise with
  one `Get` under `chunkPrefix`; a fresh mount's cache is empty, so on a
  mount of a bucket another mount wrote, such a `Write` makes that `Get`. A
  `Write` with `vfs.Unstable` that returns nil leaves its bytes in the file's
  `openFile` buffers until the next flush, marks the namespace dirty, and
  reads back through `Read`.
- A `SetAttr` that shortens a file to a size inside a stored chunk that holds
  no buffered write records a pending trim for that chunk rather than
  fetching it (ADR 0006 §3(b)): afterwards `bpSnapshot` of the file's
  `openFile` counts one more pending trim, and a `Read` to the end of the
  file returns the bytes below the new size. `Attr.FileID` is the inode
  number `bpOpen` takes, as D2 relies on.
- A `Remove` of a file drops its `FS.open` entry with its inode, so a `Sync`
  that follows flushes nothing of that file and does not wait for a `Write`
  of it still holding the file's lock (§1; ADR 0003 §2, site 5). A `Write`
  whose file is removed while it buffers returns its full length and nil
  (ADR 0003, *If the inode goes away*).

**Tests**, each from §1 to §3, in four new files:
`internal/blobfs/setattr_atomic_test.go` for S1, S2, S3 and S6, which use
only names that exist at `f8398c4`, so that the package builds there and the
session can take a red run; `internal/blobfs/setattr_policy_test.go` for S4,
S5 and S7, which need `FS.perm` and are written once coder's code is on the
branch, as ADR 0023 §11's second part was;
`internal/blobfs/dangling_entry_test.go` for D1 and D2 (Assumption 14); and
`internal/blobfs/unlinked_write_test.go` for B1 (Assumption 25). The
harness is ADR 0011 §7's and ADR 0023 §11's: a `*store.Local` under
`t.TempDir()`, behind `uc43Store` where the test counts store calls;
`SnapshotRetention` −1; `MaxDirtyBytes` −1; every `New` and every call in a
goroutine that recovers a panic and is waited for under a bound of 10 s;
nothing sleeps. One accepted exception: S7's `bpMustOpen` and `bpSnapshot`,
which make no `FS` call but read `FS.open` and an `openFile`'s fields under
their locks, run under `btBounded`, which bounds them at 10 s and recovers
no panic (Assumption 30).

- **S1, the issue's example** (`TestARefusedSetAttrAppliesNothing`; §2).
  `New` over an empty bucket; `testCaller` creates `f.txt`; `Sync`; read
  `FS.changes` holding `FS.mu` for reading, c0. Then, each from the same
  state and each asserted the same way: `testCaller`'s `SetAttr` of `f.txt`
  with `Mode` `0o600` and `UID` `0` returns an error for which
  `errors.Is(err, vfs.ErrPerm)`; with `Mode` `0o600` and `GID` `999`, the
  same; with `Mode` `0o600`, `UID` `501` and `GID` `999`, the same. After
  each: `GetAttr` reports `Mode` `0o644`, `UID` `501`, `GID` `20` and the
  ctime it reported before the call; `FS.dirty` is false and `FS.changes` is
  c0; `Sync` returns nil and the harness recorded no call since the
  previous `Sync` returned; a remount of the inner store reports the same
  mode, uid and gid for `f.txt`. Then an empty `SetAttr` (`vfs.SetAttr{}`)
  returns nil with the same attributes, and `FS.dirty` is still false and
  `FS.changes` still c0. Then the control: `testCaller`'s `SetAttr` with
  `Mode` `0o600`, `UID` `501`, `GID` `20`, `ATime` t1 and `MTime` t2, two
  distinct times a second apart in the past, returns nil with those values;
  `GetAttr` agrees; `FS.dirty` is true and `FS.changes` is above c0; after a
  `Sync`, a remount agrees. At `f8398c4` the first case leaves `Mode` `0o600`
  with `FS.dirty` false, which is #143.
- **S2, a size refused with the rest** (`TestARefusedSetAttrWithASizeAppliesNothing`;
  §2). As S1, with `f.bin` holding 100 bytes written at 0, `vfs.Unstable`,
  and committed by the `Sync`. `testCaller`'s `SetAttr` with `Size` `0` and
  `UID` `0` returns `vfs.ErrPerm`; with `Size` `10`, `Mode` `0o600` and
  `GID` `999`, the same. After each: `GetAttr` reports `Size` 100 and `Mode`
  `0o644`; `Read` of 100 bytes at 0 returns the 100 bytes; `FS.dirty` is
  false and `FS.changes` unchanged; `Sync` makes no call; a remount reads
  the 100 bytes. The control: `Size` `10`, `Mode` `0o600`, `UID` `501`,
  `GID` `20`, `ATime` t1 and `MTime` t2 returns nil with `Size` 10, `Mode`
  `0o600`, `ATime` t1 and `MTime` t2, the given mtime and not the size
  change's; `Read` returns the first 10 bytes with eof; after a `Sync` a
  remount agrees. At `f8398c4` the first case leaves `Size` 0, which is
  #143's size half.
- **S3, the order of refusals** (`TestSetAttrRefusesInTheOrderSizeModeOwnerGroupTimes`;
  §2, the order of errors). On `testCaller`'s `f.txt` of mode `0o644`, a
  caller `vfs.Caller{UID: 502, GID: 30}`'s `SetAttr` with `Size` `0` and
  `Mode` `0o600` returns `vfs.ErrAcces`, not `vfs.ErrPerm`; its `SetAttr`
  with `Mode` `0o600` and `UID` `0` returns `vfs.ErrPerm`. On a directory
  `d` of `testCaller`'s, `testCaller`'s `SetAttr` with `Size` `0` and `Mode`
  `0o700` returns `vfs.ErrIsDir`. On `f.txt`, `testCaller`'s `SetAttr` with
  `Size` `MaxFileSize() + 1` and `UID` `0` returns `vfs.ErrFBig`, not
  `vfs.ErrPerm`. After each, `GetAttr` reports what it reported before, and
  `FS.dirty` is false. This is a characterisation: it passes at `f8398c4`
  too, since the order is unchanged (Assumption 15).
- **S4, every request before any apply** (`TestSetAttrAsksThePolicyForEveryAttributeBeforeApplyingAny`;
  §2; ADR 0014 §11's W2 for `SetAttr`). After `New` and before any other
  call, replace `f.perm` with a `policy.Func` that appends each `Request` to
  a slice under a mutex of its own and returns `vfs.ErrNXIO` for the request
  at an index the test sets, nil otherwise. `testCaller` creates `f.txt`;
  `Sync`. Root's `SetAttr` with `Mode` `0o600`, `UID` `7`, `GID` `8`, `ATime`
  t1 and `MTime` t2:
  - refusing index 2: returns `vfs.ErrNXIO`; the recorded requests are
    exactly `OpChmod`, `OpChown` with `UID` 7, `OpChgrp` with `GID` 8, in
    that order, each with `Caller` root and `Object` `{Type: vfs.TypeReg,
    Mode: 0o644, UID: 501, GID: 20}`; `GetAttr` reports `Mode` `0o644`,
    `UID` 501, `GID` 20 and the times it reported before; `FS.dirty` is
    false;
  - refusing nothing: returns nil; the recorded requests are exactly
    `OpChmod`, `OpChown` with `UID` 7, `OpChgrp` with `GID` 8, `OpSetTimes`,
    `OpSetTimes`, in that order, every one with that same `Object`, the
    `OpSetTimes` requests included, which shows that nothing was applied
    before the last request was made; `GetAttr` reports `Mode` `0o600`,
    `UID` 7, `GID` 8, `ATime` t1 and `MTime` t2.

  Then `f.bin` with 100 bytes committed, `FS.changes` read after that
  commit, c1, and the harness's record marked there; root's `SetAttr` with
  `Size` `10` and `Mode` `0o600`:
  - refusing index 2, which is the exclusive hold's first request: returns
    `vfs.ErrNXIO`; the recorded requests are exactly `OpTruncate`,
    `OpChmod`, `OpTruncate`; `GetAttr` reports `Size` 100, `Mode` `0o644`
    and every other attribute as before the call; `FS.dirty` is false and
    `FS.changes` is c1; a `Read` of 100 bytes at 0 returns the 100 bytes;
    `Sync` returns nil and the harness recorded no call since the mark. This
    is S2's assertion set for a refusal in the exclusive hold, which under
    `Unix()` only a change between the two checks reaches and which this
    policy reaches directly; S7 makes the same refusal on a file whose
    buffers and trim are unflushed;
  - refusing nothing: returns nil; the recorded requests are exactly
    `OpTruncate`, `OpChmod`, `OpTruncate`, `OpChmod`, every one with `Object`
    `{Type: vfs.TypeReg, Mode: 0o644, UID: 501, GID: 20}`; `Size` is 10 and
    `Mode` `0o600`.

  The requests are made on a regular file, as the safety rule asks. This
  test does not build at `f8398c4`, where `FS.perm` does not exist.
- **S5, no check outside the policy** (`TestEverySetAttrCheckIsBehindThePolicy`;
  §2; W3 for `SetAttr`). Replace `f.perm` with a `policy.Func` that returns
  nil for every request. On `testCaller`'s `f.bin` of mode `0o644` with 100
  bytes, `vfs.Caller{UID: 502, GID: 30}`'s `SetAttr` with `Size` `5`, `Mode`
  `0o600`, `UID` `502`, `GID` `30`, `ATime` t1 and `MTime` t2 returns nil,
  and `GetAttr` reports every one of those values. Does not build at
  `f8398c4`.
- **S7, a refusal in the exclusive hold leaves a file's buffers and trim**
  (`TestARefusalInTheExclusiveHoldLeavesTheFilesBuffersAndTrim`; §2, steps 4
  and 5, and *What a refused call leaves*). S4's recorder, refusing nothing,
  behind an unarmed `uc43Store`. `testCaller` creates `f.bin`, writes 6000
  bytes p at 0, `vfs.Unstable`, and `Sync`s, so the file is two stored
  chunks and `FS.dirty` is false. Then the unflushed state: `testCaller`
  writes 100 bytes q at 0, `vfs.Unstable`, which buffers them over the first
  chunk; `testCaller`'s `SetAttr` with `Size` 5000 returns nil with `Size`
  5000, which records a pending trim on the second chunk. Now a `Read` of
  the whole file returns 5000 bytes, q followed by p[100:5000], b1; after
  that read, `GetAttr`, a1, reports `Size` 5000 and `Mode` `0o644`;
  `bpSnapshot` of `bpMustOpen(fs, a1.FileID)`, s1, has `indices` [0] and
  `pending` 1; `FS.dirty` is true, and `FS.changes` is read, c1. Then three
  refused calls by root, the recorder reset to refuse index 2 before each,
  and each asserted the same way: `SetAttr` with `Size` 10 and `Mode`
  `0o600`, whose size-change body would shorten the buffered chunk in place,
  drop the second chunk's trim and cut the chunk list to one chunk;
  `SetAttr` with `Size` 4500 and `Mode` `0o600`, whose body would record a
  shorter trim on the second chunk; and `SetAttr` with `Size` 0 and `Mode`
  `0o600`, whose body would drop the buffered chunk whole and release its
  charge, drop the second chunk's trim and cut the chunk list to none.
  Before each, `GetAttr` is read again, after any `Read`, as a. After each:
  the call returned `vfs.ErrNXIO` with exactly `OpTruncate`, `OpChmod`,
  `OpTruncate` recorded; the harness recorded no call during it; `GetAttr`
  equals a, with `Size` 5000 and `Mode` `0o644`; `FS.dirty` is true and
  `FS.changes` is c1; `bpSnapshot` equals s1 in `bytes`, `caps` and
  `pending`; and, last, a `Read` of the whole file returns b1. Then `Sync`
  returns nil, and a remount of the inner store reads b1, 5000 bytes, with
  `Size` 5000 and `Mode` `0o644`: the buffers and the trim were applied as
  they stood, after three refused calls left them as they were. The remount
  commits nothing, so the first mount is used again for the control, the
  recorder reset to refuse nothing: root's `SetAttr` with `Size` 10 and
  `Mode` `0o600` returns nil with `Size` 10 and `Mode` `0o600`, and a
  `Read` of 100 bytes at 0 returns q[:10] with eof. Between them the three
  sizes exercise, of the size-change body, the drop of whole buffered
  chunks past the new end and the release of their charge (`Size` 0), the
  drop of trims past the new end (0 and 10), the shortening of the buffered
  chunk at the new end (10), the cut of the chunk list (0 and 10), the
  record of a trim on a stored chunk at the new end (4500), and the size,
  the times and the rest of `sa` (all three); each would show in a, s1 or
  b1 if it ran before the second check. Two statements no size here reaches
  stay review's: the unsettled mark, which the `Size` 5000 call has already
  set and which no test can tell from a spurious set, and the hole fill of
  a file that grows, since every size here shrinks (Assumption 28). The
  calls are on a regular file (ADR 0014 §11's safety
  rule); the atime a `Read` may or may not set is read into a after each
  read and before each refused call, so no assertion depends on it. Passes
  at `369cbad`, read from `truncate`, whose second check returns before the
  body; does not build at `f8398c4`.
- **S6, a refused call with a size does not wait for the gate**
  (`TestARefusedSetAttrWithASizeDoesNotWaitForTheCommitGate`; §2, step 2;
  ADR 0012 §2). With `commit_window_test.go`'s harness: a file A truncated
  into a stored chunk while a `Sync` is held in its first pass, the first
  hold opened, and the settle pass's upload of A's shortened chunk held by a
  second gate, as steps 1 to 3 of
  `TestCommitWindowTruncateDuringSettleIsWholeOrAbsent` do, so that the
  `Sync` holds the commit gate for writing. While it is held, `testCaller`'s
  `SetAttr` of another file B with `Size` `0` and `UID` `0` returns
  `vfs.ErrPerm` under the bound, with B's size unchanged; then release, and
  the `Sync` returns nil. At `f8398c4` the call hangs until the bound, since
  `truncate`'s own checks pass and it then waits for the gate before
  `SetAttr` refuses the uid.
- **D1, a removal of an entry that names no inode changes nothing**
  (`TestRemoveOfAnEntryThatNamesNoInodeChangesNothing`; §3; #144's
  done-when for (b)). Build the doctored snapshot as the facts above say,
  behind `uc43Store`, and `New` on it. Mark the harness's record. `Lookup`
  of `ghost` answers `vfs.ErrNoEnt`; `ReadDir` of the root from cookie 0
  lists no `ghost` and returns a verifier v. `Remove` of `ghost` returns
  `vfs.ErrNoEnt`; `Rmdir` of `ghost` returns `vfs.ErrNoEnt`. After both:
  `FS.dirty` is false and `FS.changes` is what it was after `New`; `ReadDir`
  returns the verifier v again; `GetAttr` of the root reports the mtime it
  reported before; an exclusive `Create` of `ghost` answers `vfs.ErrExist`;
  `Sync` returns nil and the harness recorded no call since the mark; a
  remount of the inner store answers `vfs.ErrNoEnt` to `Lookup` of `ghost`
  and `vfs.ErrExist` to an exclusive `Create` of it. At `f8398c4` the
  `Remove` drops the entry in memory without `markDirty`, so `FS.dirty`
  stays false there and that assertion passes; the test fails on the
  verifier, which changes, and on the exclusive `Create`, which succeeds,
  and that create is a change, so the `Sync` that follows commits it, with
  store calls, and the remount finds a real `ghost`. That is #144 and the
  repair this ADR declines (Assumption 24).
- **D2, a rename onto such an entry replaces it**
  (`TestARenameOntoAnEntryThatNamesNoInodeReplacesIt`; §3; ADR 0017 §2). The
  same setup; `testCaller` creates `donor`; `Rename` of `donor` to `ghost`
  in the root returns nil; `Lookup` of `ghost` returns `donor`'s `FileID`
  and `Lookup` of `donor` answers `vfs.ErrNoEnt`; `FS.dirty` is true; `Sync`
  returns nil with one `Put` under `snapshotPrefix` and one `PutIfMatch` of
  `rootKey` recorded; a remount's `Lookup` of `ghost` returns that `FileID`,
  and the snapshot the root pointer then names, decoded, has
  `Inodes[rootInodeID].Entries["ghost"]` equal to it and no `donor`. This is
  a characterisation of the recovery path: it passes at `f8398c4` too
  (Assumption 15).
- **B1, a write that lands after an unlink marks nothing**
  (`TestAWriteThatLandsAfterAnUnlinkMarksNothing`; §1, `bufferWrite`'s tail
  block; ADR 0003 §2, site 2; Assumption 25). A first mount on the harness's
  bucket: `testCaller` creates `f.bin`, writes 4096 bytes p at 0,
  `vfs.Unstable`, and `Sync`s; the mount is dropped and not used again.
  Then `New` over a `bpStore` wrapping the `uc43Store`, so that the mount's
  cache is empty and a `Get` of a chunk can be held: `setGet` armed with a
  `bpHook` whose `key` is empty, with `entered` and a `gate` the test closes.
  In a goroutine under the bound, `testCaller`'s `Write` of 10 bytes at 100
  of `f.bin`, `vfs.Unstable`: it covers part of the stored chunk, so it
  fetches it, and the hook's `entered` closes while the `Write` runs, which
  the test waits for under the bound. While the `Get` is held: `testCaller`'s
  `Remove` of `f.bin` returns nil; `Sync` returns nil, committing the
  removal; `FS.dirty` is false and `FS.changes` is c2. Close the gate; the
  `Write` returns 10 and nil under the bound. Then: `FS.dirty` is false and
  `FS.changes` is c2, since the write's tail block found the inode gone and
  changed nothing; mark the harness's record; `Sync` returns nil and the
  harness recorded no call since the mark; a remount answers `vfs.ErrNoEnt`
  to `Lookup` of `f.bin`. At `369cbad` and at `2ef1eb4` the tail block marks
  on that branch, so `FS.dirty` is true after the `Write`, `FS.changes` is
  above c2, and the `Sync` records a `Put` under `snapshotPrefix` and a
  `PutIfMatch` of `rootKey` for a commit that changes nothing: the over-mark
  §1 removes. The `Sync` made while the `Get` is held does not wait for the
  write, since `Remove` dropped the file's `FS.open` entry (the facts); if
  it did, the bound would fail the test rather than hang it. The held `Get`
  passes through once the gate opens and is recorded then, before the mark.

**Red runs.** At `f8398c4`, from the code as *Context* quotes it and from
the tests' own doc comments (Assumption 24):

- **S1** fails in its three refused cases on the attribute left applied,
  mode `0o600`, and on the ctime its apply moved, while `FS.dirty` stays
  false there: the refusal returns before `SetAttr`'s one `markDirty`,
  which is the defect (`internal/blobfs/setattr_atomic_test.go` lines
  355–357, and the shared assertion at lines 299–301). Its empty-call case
  fails on `FS.dirty` and on `FS.changes`, since `f8398c4`'s `SetAttr`
  reaches that `markDirty` with nothing set (the same file, lines 356–357
  and 400–407).
- **S2** fails on the size left applied, 0 or 10, and on `FS.dirty`, which
  `truncate` set with its `touchM` and `markDirty` before `SetAttr` refused
  the uid or the group (the same file, lines 469–470 and 299–301); and,
  because the namespace is then dirty, its `Sync` commits and its remount
  loads the shortened file, so those assertions fail too.
- **S6** hangs to the bound: `truncate`'s own checks pass, it waits for the
  gate, and only once the gate opens does it apply size 0 and `SetAttr`
  refuse the uid (the same file, lines 667–669).
- **D1** fails on the verifier, which changes once the entry is dropped,
  and on the exclusive `Create`, which succeeds; that create is a change,
  so the `Sync` that follows commits it and the remount finds a real
  `ghost`. `FS.dirty` after the `Remove` is false there, since the dropped
  entry was never marked (`internal/blobfs/dangling_entry_test.go` lines
  254–258).
- **S4 and S5** do not build, since `FS.perm` does not exist there.
- **S3 and D2** pass, and are pinned so that the fix cannot move the order
  or the recovery path.

The session's red run at `f8398c4` showed only the outcomes: S1, S2 and D1
failed, S6 hung, S3 and D2 passed. It did not show the mechanisms, which
are read from the code and the test files as cited above. At `369cbad` all
six pass, thirty repeats each under `-race`, by the session's run. S7 and
B1 are later (the second `Revised` line): S7 passes at `369cbad` and at
`2ef1eb4`, read from the code, and B1 fails at both on `FS.dirty` after the
`Write`, on `FS.changes` and on the calls its last `Sync` makes, as its
entry says, until coder moves the call (Assumption 25). Neither is run at
`f8398c4`: S7 needs `FS.perm`, and B1 uses only names that exist there but
sits in a package that no longer builds there, so B1's red run is taken at
`2ef1eb4`, before coder's commit.

**Changes to existing tests.** None in their assertions.
`setattr_atomic_test.go`'s header lists, as not covered, "the exclusive
hold's second check, which only a change made between the two checks can
reach", which S4 (iii) and S7 in `setattr_policy_test.go` make false: the
header is to say that under `Unix()` only such a change reaches it
(Assumption 19) and that S4 (iii) and S7 reach it with a policy that refuses
it. `dangling_entry_test.go`'s `dg24Fields`, `dg24Read` and `dg24GetAttr`
duplicate `setattr_atomic_test.go`'s `sa24Fields`, `sa24Read` and
`sa24GetAttr` in the same package and are replaced by them, with no
assertion changed (reviewer's R6). `TestMaxFileSizeSetAttrAboveTheLimit`
and `TestMaxFileSizeSetAttrTakesNoLock` (ADR 0007 §7) stand: `vfs.ErrFBig`
is still `truncate`'s first statement, before any lock. ADR 0023 §11's U1,
U2, U8, U11 and U12 make a `SetAttr` of a size while a store call is held,
each by the owner, and still return. U10 reads `FS.changes` before and after
a `SetAttr` of a mode and of a size: each still advances it, by one where it
advanced by two. ADR 0017 §6's L1 to L5 and ADR 0012 §9's tests stand.

### 8. Scope, and `CHANGES`

Nothing is split out: #143 and #144 are fixed in one PR, since both are
what lets ADR 0023 §8 strike its two exceptions (§4), and neither depends
on the other. Two `CHANGES` lines, neither `BREAKING:` (Assumption 12),
proposed for the session to record, newest first:

> A SETATTR that is refused for any attribute it sets now changes none of
> them: every attribute's permission is checked before any is applied, so a
> call refused on the mode, the owner, the group or the times leaves the
> size and every other attribute as they were. Before, it had already
> applied the attributes it checked first: a call with a size had its size
> change, and anything applied with it, committed at the next commit, and
> one without left the change uncommitted until some other change was
> committed.

The first version of this line said every refused call had left its change
uncommitted; that was true without a size and false with one, and the line
named no refusal on the mode (Assumption 26).

> A REMOVE or RMDIR of a name whose directory entry names no inode, which
> only a snapshot written by another build or a corruption can hold, now
> leaves the entry in place and answers NFS3ERR_NOENT, as a RENAME of the
> name does; before, it dropped the entry in memory without recording the
> change, so the entry came back at the next mount unless another change
> was committed meanwhile. Renaming a file onto such a name still replaces
> it.

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation**, under the owner's 2026-10-10 words, as
   ADRs 0015 to 0023 were. The design is mine: the issues ask for atomicity
   and for a choice, and this ADR decides the hold, the checks' form, the
   two-pass shape, the choice and every name.
2. **Read, not run, at `f8398c4`.** Every statement about the code was
   checked by reading it there; nothing was run. The issues' texts are as
   the session gave them in the brief, quoted or summarised above; neither
   was read from the tracker.
3. **Atomicity is this repository's rule, not the RFC's** (*Context*; §2).
   RFC 1813 §3.3.2 allows a failed `SETATTR` to change attributes partly,
   and #143's reading of it is not supported. The fix is made anyway: a
   half-applied change that the caller was told failed, and that the
   namespace does not record, is worse than either outcome the RFC allows;
   the `setattr` handler in `internal/nfs` answers a refused call with
   `wcc_data` whose after-attributes it reads from `GetAttr` after the
   call, so the reply itself carried the half-applied mode as the file's;
   and the repository's own rule (§1) wants every change recorded, which a
   refused path that applies half of `sa` and marks nothing breaks whatever
   the RFC says. The RFC permits a server to do better, and nothing in it is
   contradicted.
4. **Two checks for a call with a size, one hold for the change** (§2).
   The pre-check keeps ADR 0012 §2's pin that a refused size change never
   waits for the gate, and extends it to the other attributes, since
   otherwise a call refused for its owner would wait for a commit's settle
   pass and seal first. The check in the exclusive hold is what makes the
   verdict fresh: without it the mode, owner, group and times, which at
   `f8398c4` are checked and applied in one hold, would be checked under a
   read hold and applied later on a permit that does not carry over, which
   would widen #101 from `Write` and the size to every attribute. Making
   both is ADR 0018 §5's shape for a method whose change lands in a later
   hold, costs two policy calls per attribute, which take no lock, and
   answers #101 for `SetAttr` alone. The alternative of checking once in the
   exclusive hold and dropping the pre-check would have every refused size
   change wait for the gate, against ADR 0012 §2.
5. **`truncate` keeps its name and carries the rest of `sa`** (§2). Only
   `SetAttr` calls it, so it has always been `SetAttr`'s path for a size;
   `trace.tsv`, vocabulary V3 and FileData's reserved names `TruncateGate`,
   `TruncateGetOpen`, `TruncateLock` and `TruncateBody` name it, and
   ADR 0007 §4's sentences about its first statement and about `SetAttr`
   calling it before it changes any attribute hold literally. Splitting the
   size's apply out as a function of its own is coder's choice inside the
   hold, and changes no row.
6. **`SetAttr`'s checks go through `f.perm` now** (§2). The checks are
   changed code under ADR 0018 §3: they move ahead of every apply and are
   made twice for a call with a size, so their form is this ADR's to decide,
   and the policy rule says what the form is. An exemption would need a
   reason ADR 0018 §1 names, and none applies: the path is not hot, nothing
   else pins the verdicts, and five rules are not one. The policy rows exist
   and are tested (ADR 0014 §11's P4), so the cost is `FS.perm`,
   `objectOf` and an import, which ADR 0014 §6 pins already; wiring every
   other method now would be #114's PR under another issue's number, with
   W1 to W5 whole, and is not this ADR's. `unlink`'s check is left: §3 edits
   the branch after it and not the check, which ADR 0018 §3's Assumption 5
   does not count as a change to the decision.
7. **The requests are made twice for a call with a size, and S4 pins both
   lists** (§2, §7). A test that sees one list would pass a build that
   dropped the check in the exclusive hold, which is the check that counts.
   Pinning the second list makes it load-bearing, as T1 pins a transition's
   request by its place (ADR 0018 §6).
8. **The applies and the times are as at `f8398c4`** (§2): the same fields,
   masks and `touch` calls, in the same order, so that nothing a client sees
   of a successful call changes, the given mtime winning over the size's
   included. One `time.Now()` for the hold is a simplification that no
   test can tell from two.
9. **An empty `SetAttr` marks nothing** (§2). At `f8398c4` it called
   `markDirty`, costing a needless commit at the next tick. §1's rule reads
   `markDirty` as the record of a change, and a call that made none has
   nothing to record. No client can observe the difference, so no `CHANGES`
   line.
10. **(b) for #144** (§3), for the three reasons §3 gives. The recovery path
    through `Rename` is pinned by D2 because this ADR relies on it: without
    the test a later change to `Rename`'s victim handling could remove the
    one way a client has to reclaim such a name, with nothing failing.
11. **No load-time repair, and `createEntry` unchanged** (§3). A repair at
    load would be a decision about what a mount does with a snapshot it did
    not write, which ADR 0017 §5 made the other way for leaked directories;
    it would also have to say what a read-only mount does, which cannot
    commit the repair (ADR 0011 §4). Treating a dangling entry as absent in
    `createEntry` would give `touch` as a second recovery path, but changes
    an answer #144 did not ask about. Both are left open.
12. **Not breaking, and two `CHANGES` lines** (*Context*; §8). No deployment
    needs to act: nothing on the wire, in the bucket or in the flags changes.
    Both fixes are client-visible, so each gets a line (§8). The empty call
    (Assumption 9) and the refused call's shorter wait get none.
13. **No model, and `trace.tsv` now** (§5). `Mutate` is abstract and the
    model has no action that changes the namespace without setting
    `nsDirty`, so a member for the defect would describe a step the model
    cannot take, and a MustFail row for it would have nothing to violate but
    the trace's honesty, which TLC does not check. The `decided` column is
    edited in this pass, since it says which ADR decides the three rows'
    behaviour and that is settled here; `Checked against` waits for the
    code, as ADR 0023 Assumption 23 had it, and the session runs
    `tla.sh full` for the PR either way.
14. **The test surface** (§7). `encodeSnapshot` joins the names because D1
    and D2 need a snapshot no operation can make, and ADR 0023 Assumption 38
    found the names short of it. The doctoring recipe overwrites the
    snapshot object the root pointer names rather than writing a root
    pointer, so that the test needs no ETag handling and the root pointer's
    own invariants are untouched. Three new files, named for coder and
    review to find, with S4 and S5 apart so that the rest build at
    `f8398c4` for a red run; no existing test file changes. S6 reuses
    `commit_window_test.go`'s harness because holding the gate needs a held
    settle pass, which that harness already builds.
15. **S3 and D2 are characterisations** (§7). Each passes before and after.
    S3 pins the order of errors so that the move of the checks cannot
    reorder them; D2 pins the recovery path (Assumption 10). Both say so in
    their doc comments.
16. **#96 and #143** (*Context*). ADR 0014 lists #96 by title only, and I
    cannot read the tracker. If #96 is open it is this ADR's too, and the
    session closes or links it; if it was closed as a duplicate, nothing
    changes. ADR 0014 §6's "the ADR that settles #96" is read as this ADR
    either way.
17. **`vfs.FS.SetAttr`'s doc comment was proposed first, and edited on
    2026-10-31** (§2). The contract is this ADR's and the package is the
    architect's, but the brief that produced this ADR forbade editing code,
    and `internal/vfs/vfs.go` is code, so the sentence was pinned above and
    added only once the session authorised it, in the dispatch after
    coder's commit: §2's words, wrapped to the file's width, after the
    sentence on `ErrFBig`, and nothing else in the file.
18. **ADR 0018 is not revised** (§6). Its §5 sentence that `SetAttr`, "for
    the attributes it sets after the size", holds `FS.mu` for writing from
    stage C to its change still holds, and for a call with a size now holds
    for the size too; nothing there becomes false, so a `Revised` line would
    record nothing.
19. **A refusal in the exclusive hold may leave an empty `FS.open` entry**
    (§2). It can happen only when the inode changed between the two checks,
    and it is what a second resolve that answers `vfs.ErrStale` leaves at
    `f8398c4`; the entry holds nothing, costs nothing but `Read`'s second
    step, and leaves with the inode. Removing it on refusal would add a
    `dropOpen` on a path that cannot tell its entry from one a concurrent
    `Write` just made. #42 owns the entry's life.
20. **The move changes no verdict** (§2), checked by reading `Unix()`'s rows
    rather than run: the only row that reads an attribute another row sets
    is `OpSetTimes`, which reads the owner and the mode, and every caller
    who reaches it past `OpChmod`, `OpChown` or `OpChgrp` is root or the
    owner, whom it allows. A policy installed later that reads more could
    see a difference, and would be judged under its own ADR.
21. **One `markDirty` per call** (§2). A call with a size at `f8398c4` made
    two, `truncate`'s and `SetAttr`'s. `FS.changes` is compared, not
    counted (ADR 0023 §3), so nothing depends on the number, and U10 asks
    only that it move.
22. **Coder's choices at `369cbad` are accepted** (§2). The helpers' names
    and `applySetAttr`'s bool are forms this ADR left open; the bool is the
    plainest way to have the empty call mark nothing, and `truncate`'s
    unconditional `markDirty` is right, since a size set is a change even
    to the same size, its mtime moving. The one edit inside the size-change
    body, its last lines, is what "one `time.Now()` serves the hold" asked
    for; everything above those lines was read against `f8398c4` and is as
    it was.
23. **The `Checked against` pass is read, not run** (§5). The three rows
    were read against `369cbad` by reading `SetAttr`, `unlink` and
    `truncate` whole: each symbol is declared on `*FS` in
    `internal/blobfs/fs.go`, each keeps its `TLA+: … Mutate` line, and each
    makes its change and its `markDirty` under the locks its row names. The
    module changed in its header line alone, a comment, so TLC's outcome
    for the forty-five calibrated rows cannot differ from the run that
    calibrated them; CLAUDE.md's merge bar still asks for `tla.sh full` on
    this PR, which the session runs. `docs/tla/README.md` and vocabulary V3
    name the commit as coder's commit for ADR 0024, in the form ADR 0023's
    bumps used.
24. **The red-run record was wrong twice, and is now read from the tests
    and the code** (§7). The first draft said `FS.dirty` is true after D1's
    `Remove` at `f8398c4`; it is not, since `unlink`'s dangling branch
    dropped the entry without `markDirty`, which test-author found against
    this ADR's own *Context*. The first version of the 2026-10-31 revision
    then extended that correction to S1 and S2, saying `FS.dirty` stays
    false for them too; that was wrong, as supervisor found from the test
    files: at `f8398c4` `SetAttr` called `markDirty` unconditionally at its
    end, which an empty call reached, so S1's empty-call case fails on
    `FS.dirty` and `FS.changes` (`setattr_atomic_test.go` lines 356–357,
    400–407); and `truncate` called `touchM` and `markDirty` before
    returning to a `SetAttr` that then refused, so every S2 case fails on
    `FS.dirty` (lines 469–470, 299–301). Only S1's three refused cases, the
    mode-then-uid path and its two siblings, leave the flag false there
    (lines 355–356, 299–301). The first draft's "on `FS.dirty`" for S1 and
    S2 had been right. The session's red run at `f8398c4` showed only that
    S1, S2 and D1 failed, S6 hung and S3 and D2 passed; every mechanism in
    §7's *Red runs* is read from the code at `f8398c4`, as *Context* quotes
    `SetAttr` and `truncate`, and from the test files at the lines cited.
    The tests' assertions were right throughout and are unchanged; only
    this ADR's record of the red run was wrong, twice.
25. **(b) for `bufferWrite`'s over-mark, with a test** (§1, §7). Reviewer
    found the mark on the unlinked branch (its R3) and offered narrowing §1's
    wording instead. The move is chosen because §1 reads `markDirty` as the
    record of a change (Assumption 9) and the branch records none; the
    alternative would have the ADR that states the rule carry an exception
    for a one-line move. The over-mark is reachable, by reading `unlink`,
    which calls `dropOpen` as it deletes the inode, and `flushAll`, which
    lists `FS.open` under `FS.mu` for reading and takes each listed file's
    `openFile.mu`: a file unlinked during its write's fetch is not listed,
    so a `Sync` passes the write's hold of `openFile.mu`, and the mark then
    lands on a namespace the commit has just made clean. B1 holds the fetch
    with `bpStore`'s `Get` hook on a mount whose cache is cold, which is why
    its file is written by a mount that is dropped first, and it is in a
    file of its own since it is §1's, not `SetAttr`'s. E13's third fact is
    about holds that change the namespace, and this one does not, so it is
    untouched. No `CHANGES` line: a client cannot observe an empty commit,
    as Assumption 9 has it for the empty call. The branch's other work, the
    drop and the release, is ADR 0003 §2's and does not move.
26. **The corrected `CHANGES` line** (§8). The first version said a refused
    call had "left that change uncommitted until some other change was
    committed", which is true without a size and false with one: at
    `f8398c4` `truncate` called `markDirty` before returning to the
    `SetAttr` that refused, so the size change and whatever was applied
    with it were in the next commit, as §7's S2 red-run paragraph already
    said, and the line now says both. "Mode" joins the list of refusals: a
    caller with write permission who is not the owner, as a member of the
    group of a file of mode `0o664`, had the size applied and the mode
    refused at `f8398c4`. Reviewer found both (its R1).
27. **The guard is the handler's own read** (*Context*). The `setattr`
    handler compares the guard's ctime with a `GetAttr` it makes before
    `SetAttr`, in a hold of its own, so a change between the two passes the
    guard; that is #99, which §2 lists as untouched, and the sentence says
    "modulo #99" rather than "already preserves". Reviewer's R4.
28. **S7's shape** (§7). The file is 6000 bytes so that a buffered write
    sits on the first chunk and a trim on the second, and the three refused
    sizes are chosen against `truncate`'s size-change body, read at
    `f6d20e1`, so that each statement a size reaches would show if it ran
    before the second check. S7's state is `of.dirty` holding index 0 only,
    `pendingTrim` holding the second chunk's trim to 904, a chunk list of
    two and a size of 5000, with the chunk size 4096. `Size` 0, with
    `lastIdx` 0 and `tail` 0: the drop loop takes an index equal to
    `lastIdx` only when `tail` is 0, so it drops the buffered chunk whole
    and the release that follows takes its 4096 from `of.bytes` and the
    budget, which `bpSnapshot`'s `bytes` and `caps` show; the trim loop
    drops the second chunk's trim, which `pending` shows; the chunk list is
    cut to none, which the read shows; the reslice, the trim record and
    the unsettled mark are skipped, since `tail` is 0. `Size` 10, with
    `lastIdx` 0 and `tail` 10: the drop loop and its release do nothing,
    since index 0 is `lastIdx` and `tail` is not 0; the trim loop drops the
    second chunk's trim; the buffered chunk is shortened to 10 bytes in
    place, which the read shows and `caps` cannot, a reslice keeping its
    capacity; the chunk list is cut to one; the trim record is skipped,
    index 0 being dirty. `Size` 4500, with `lastIdx` 1 and `tail` 404:
    nothing is dropped or cut, and a trim to 404 is recorded on the second
    chunk in place of the one to 904, which the read shows and the trim
    count cannot. The first version of this assumption said 10 and 4500
    exposed every statement but the unsettled mark; reviewer found that
    they leave the drop loop and its release untouched, which `Size` 0 now
    reaches, and that the hole fill of a file that grows runs at no size
    here (its F2). So two statements stay review's: the unsettled mark,
    which the allowed `Size` 5000 call sets and which cannot be told from a
    spurious set, since a test that cleared it first would need the settle
    pass, which is ADR 0012's; and the growth hole fill, since S7 only
    shrinks. `FS.dirty` is true throughout, since the state is unflushed,
    so S7 asserts it unchanged, and the remount after the `Sync` is what
    shows the buffers and the trim were kept; a `Sync` that made no store
    call, S2's assertion, is impossible here and is S4 (iii)'s.
    `bpSnapshot` reads an `openFile` under its `mu` as the `bp` tests do,
    and `FileID` is the inode number, as D2 relies on. The remount commits
    nothing, so the first mount is used again for the control: no ADR
    forbids it, and the doctoring recipe's "never use it again" is for a
    mount whose bucket is then written behind its back. Reviewer's R5 and
    F2.
29. **Row 17's `decided` now, `Checked against` after the code** (§5), as
    Assumption 13 had it for the three rows: the column names the ADR that
    decides the row's behaviour, and §1 now decides the tail block's; the
    `locks` column is untouched; the line moves when the architect reads the
    row against coder's commit (ADR 0013 §8). That reading was made on
    2026-10-31 against `f6d20e1`, coder's commit for the move, by reading
    `bufferWrite`'s tail block whole: `markDirty` is called in the `if`
    branch after `touchA`, under `FS.mu` for writing with `openFile.mu` held
    from the function's top; the `else` branch has the drop and the release
    and no mark; nothing else in the function changed, and the session
    reports that supervisor confirmed the commit holds nothing else. Read,
    not run; the session's gate and its thirty repeats of B1 at `f6d20e1`
    are the run. The module changed in its header comment alone, so TLC's
    outcome for the forty-five calibrated rows cannot differ from the run
    that calibrated them; CLAUDE.md's merge bar still asks for `tla.sh full`
    on the PR, which the session runs.
30. **S7's two field-reading helpers recover no panic and are not drained**
    (§7), which supervisor noted against ADR 0023 §11's rule for every
    `New` and `FS` call, and whose first wording reviewer corrected (its
    F1). `bpMustOpen` and `bpSnapshot` make no `FS` call: `bpOpen` reads
    `FS.open[id]` holding `FS.mu` for reading, and `bpSnapshot` reads an
    `openFile`'s `bytes`, the capacity of each buffer in `dirty` and the
    length of `pendingTrim` holding its `mu`, each in `btBounded`
    (`internal/blobfs/budget_test.go`), which runs its function in a
    goroutine with a one-slot channel and waits for it only up to
    `btHangBound`, 10 s. On a timeout it abandons the goroutine, with no
    cleanup to drain it as `uc43Go`'s has, and the helper fails the test
    with `t.Fatalf`. The rule's point is that no call the test starts can
    hang the binary or outlive the test holding a lock, and that holds: a
    goroutine abandoned there is blocked acquiring `FS.mu` or the
    `openFile`'s `mu` and holds neither, so it blocks nothing else; if the
    lock is later released it takes it, reads, sends into the one-slot
    channel without blocking and exits, and if not, it is leaked until the
    test binary exits, holding nothing. Neither helper holds a lock across
    a wait, and neither recovers a panic: a panic in them could only be the
    test's own, which crashing the binary reports as plainly as a recovered
    one would. The exception is accepted rather than closed by wrapping
    them in `uc43Go`; no test changes for it.

## Alternatives considered

- **Undo on refusal.** Apply each field as now and roll the earlier ones
  back when a later check fails. The size's rollback would have to undo a
  trim, a dropped dirty buffer and a released charge, which ADR 0006 and
  ADR 0003 pin as one-way. Checking first costs nothing to undo.
- **Check every attribute in the pre-check only, apply in the later hold.**
  Atomic, but on a stale permit for attributes that are checked fresh today
  (Assumption 4).
- **One check, in the exclusive hold, with no pre-check.** Every refused
  size change would wait for the gate, against ADR 0012 §2 (Assumption 4).
- **Exempt `SetAttr`'s checks from the policy rule.** No reason ADR 0018 §1
  names applies (Assumption 6).
- **Wire all of ADR 0014 §6 in this PR.** It is #114's, with its own test
  surface, and would make a fix for two error paths carry every method's
  change (Assumption 6).
- **Rename `truncate`.** It would change a trace row, the vocabulary and
  FileData's reserved names for no gain (Assumption 5).
- **(a) Mark the repair in `unlink`.** One line, but an error path that
  changes the namespace, a repair decided by side effect, and an argument
  that `Rename` already answers (§3; Assumption 10).
- **Repair at load.** A decision ADR 0017 §5 made the other way for leaked
  directories, with the read-only mount unanswered (Assumption 11).
- **Refuse to load a snapshot holding such an entry.** It would make a
  bucket unmountable for one name, where today every other name serves.
- **Treat such an entry as absent in `createEntry`.** Left open
  (Assumption 11).
- **A `Fixes` member and a MustFail row.** The model has no step for the
  defect (Assumption 13).

## Consequences

- A `SETATTR` is all or nothing: a refused one changes no attribute, no
  size and nothing the next commit would carry, and a successful one with a
  size lands in one commit whole.
- Every change to the namespace is recorded, and ADR 0023 §8's third fact
  holds without qualification; E13 rests on it whole.
- `blobfs` imports `internal/policy`, `FS` holds `perm`, and `SetAttr` is
  the first method whose checks are the policy's; the rest follow under
  #114, with `permitted` and `inGroup` kept until then.
- A `SETATTR` with a size asks the policy twice, which takes no lock.
- A directory entry that names no inode stays until a `RENAME` replaces it,
  and `REMOVE` of it is honest: it changes nothing and says so.
- `bufferWrite` marks the namespace only where it changed it: a write that
  lands after its file's removal costs no commit.
- `truncate`'s signature changes and it applies the rest of `sa`; the three
  trace rows gain this ADR, and the module's `Checked against` moves once
  the code lands.
- Hard to change later: the contract that `SetAttr` applies all of `sa` or
  none, which a client may now rely on and the redesign's backend
  (`docs/DESIGN.md` §14) implements too; and §1's rule, which every later
  change to the namespace keeps.

## What this does not decide

- Whether `Write`'s check moves to where its change lands (#101), which
  this ADR answers for `SetAttr` only.
- The guard's check, made in `internal/nfs` outside the filesystem's lock
  (#99).
- `createEntry`'s requested uid and gid (#98), a symlink's handle with a
  size (#69), and the unpinned rows U1 to U6 of ADR 0014 §11.
- The rest of ADR 0014 §6's wiring, and when `permitted` and `inGroup` go
  (#114).
- A repair of a snapshot's dangling entries at load, and what a read-only
  mount does with one; a mount-time warning about them; an operator tool.
- Whether `createEntry` should treat an entry that names no inode as absent.
- Whether `ReadDir` should hash into its verifier a name it does not list.
- #96's status (Assumption 16).

## Sources

Fetched on 2026-10-30 through a tool that summarises what it fetches and
quotes at most about 125 characters at once, from the RFC Editor, twice: the
HTML rendering, <https://www.rfc-editor.org/rfc/rfc1813.html>, and the text,
<https://www.rfc-editor.org/rfc/rfc1813.txt>, each read from character 0 to
100,000, which holds §3.3.2 whole. Quoted as the tool returned them.

- **RFC 1813 §3.3.2, SETATTR, IMPLEMENTATION**: "SETATTR is not guaranteed
  atomic." and "A failed SETATTR may partially change a file's attributes."
  Both fetches returned the two sentences alike. (*Context*; Assumption 3)
- **RFC 1813 §3.3.2, DESCRIPTION, the guard**: "If guard.check is TRUE, the
  server must compare the value of guard.obj_ctime to the current ctime of
  the object." and "If the values are different, the server must preserve
  the object attributes and must return a status of NFS3ERR_NOT_SYNC." The
  second sentence was returned by the first fetch; the second fetch, asked
  for the IMPLEMENTATION subsection, said that "preserve" appears only in
  the DESCRIPTION, which agrees. (*Context*)
- **RFC 1813 §3.3.2, IMPLEMENTATION, the guard**: "The guard.check mechanism
  allows the client to avoid changing the attributes of an object on the
  basis of stale attributes." and "It does not guarantee exactly-once
  semantics." (*Context*)
- **RFC 1813 §3.3.2, ERRORS**: the tool listed NFS3ERR_PERM, NFS3ERR_IO,
  NFS3ERR_ACCES, NFS3ERR_INVAL, NFS3ERR_NOSPC, NFS3ERR_ROFS, NFS3ERR_DQUOT,
  NFS3ERR_NOT_SYNC, NFS3ERR_STALE, NFS3ERR_BADHANDLE and NFS3ERR_SERVERFAULT,
  and said the section states the conditions for few of them. `ErrFBig`'s
  place is ADR 0007 §5's. (§2)

Nothing else was fetched. Everything else here is read from this
repository's own code and documents, cited by symbol and section.
