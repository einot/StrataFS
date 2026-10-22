# 0017. Removing a directory removes its inode

**Status:** Accepted
**Date:** 2026-10-22
**Issue:** #50

## Context

**The owner's decisions.** Two, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So recommendation 14 stands, as delegated: fix it, with
   no repair of existing buckets.
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release."

Assumption 11 says how the second bears on this ADR.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `acc8719`, the commit this ADR's branch was cut from
(Assumption 13). Code is cited by symbol, in `internal/blobfs/fs.go` unless it
says otherwise.

**The defect.**

- A directory starts with a link count of 2 (`allocInode`). `initEmpty`, in
  `internal/blobfs/commit.go`, gives the root the same, and `createEntry` adds
  1 to the parent of each directory it makes.
- `unlink`, and `Rename` for the entry it replaces, take one from the count,
  and delete the inode only at 0. So a removed directory stays in the
  namespace. Only an empty directory can be removed, and an empty directory's
  count is 2, so removing it leaves 1: it stays in `FS.inodes` with no name,
  and `commitLocked` writes it into every later snapshot.
- Its handle still serves every method, `Create`, `Mkdir` and `Symlink`
  included. `resolve` still finds it, so each method answers as it would for a
  live directory, and what is made inside it is stored where no path reaches.
- FSSTAT counts it. `FSStat` reports `TotalFiles` as `len(f.inodes)` plus
  2^30, and `FreeFiles` as 2^30.
- ADR 0014 §8 leaves the link-count function to "#50's fix" as "the natural
  one". Its words are "which change introduces it is not decided here. #50's
  fix is the natural one."

**The issue.** #50 is "Removing a directory never deletes its inode", the title
ADR 0014 records for it. Its text was not read for this ADR.

**What the ADRs say.**

- ADR 0014 §8 gives link counts "the lighter form": "one function through which
  `unlink` and `Rename` make every link-count change; it applies the change and
  says whether the inode has left the namespace". Once something implements
  `vfs.Pinner`, "a table-driven machine replaces it". The lighter form is not a
  machine, so it adds no name to `docs/vocabulary.md`, which takes the names of
  each machine a later ADR adds.
- ADR 0013 §1 gives the namespace tree model-based tests, since `Create`,
  `Mkdir`, `Symlink`, `Remove`, `Rmdir`, `Rename` and `SetAttr` each make their
  change in one hold of `FS.mu`.
- `vfs.Pinner` already says what a pin keeps: a `Pinner` "keeps an inode
  addressable after its last name is removed", and while an inode is pinned,
  "removing every name for that inode must not make h stale". Until this ADR,
  nothing in `FS`'s own doc comments said what becomes of an object with no
  name and no pin.

**What this constrains.** `unlink` and `Rename`, and the `relink` this ADR adds
to `internal/blobfs`; and the `vfs.FS` contract, through one doc comment above
`Remove`. No type, field or format changes, and nothing on the wire changes but
the status a removed directory's handle answers.

## Decision

**The `vfs` sentence.** `FS` in `internal/vfs/vfs.go` gains this doc comment,
above `Remove`:

```go
// Remove, Rmdir and Rename each take a name away: Remove and Rmdir the
// one they are given, and Rename the target name it replaces, if one
// exists. An object left with no name is gone, and a directory always
// is once its name goes, since it has no other. From then on every
// method given the object's handle fails, with ErrStale unless it
// refuses the call for another reason first, except that a Write of no
// bytes may succeed without looking at the handle. A Pinner can hold an
// object back from going; nothing else does. The object Rename moves
// keeps its handle.
```

### 1. The rule

- A directory's only name is its entry in its parent.
- `Rmdir`, and a `Rename` onto an empty directory, take that name away. The
  directory then leaves `FS.inodes`, whatever count it carries, and the `vfs`
  sentence above applies.
- A non-directory leaves when its count reaches 0, as now.
- `Rmdir` refuses a directory that has entries, and `Rename` refuses to replace
  one, both as now. So a directory that leaves takes nothing with it.
- RFC 1813 allows a server to remove a non-empty directory. This one does not,
  and this rule relies on that.

### 2. `relink`

`func relink(n, from, to *inode) (gone bool)`, in `internal/blobfs`. It is a
plain function: the caller holds `FS.mu` for writing, and `relink` takes no
lock and logs nothing.

- **When `to == nil`** (a name is removed):
  - for a directory: `from.NLink` drops by 1, `n.NLink` becomes 0, and it is
    gone;
  - for anything else: `n.NLink` drops by 1, never below 0, and it is gone
    exactly when `n.NLink == 0`.
- **When `to != nil`** (a name is moved): a directory moving between different
  directories (`from.ID != to.ID`) takes 1 from `from.NLink` and adds 1 to
  `to.NLink`. `n`'s own count is unchanged, and it is never gone.
- **Callers:**
  - `unlink` calls `relink(n, d, nil)`;
  - `Rename` calls `relink(victim, dst, nil)` for an entry it replaces, and
    `relink(moving, src, dst)` for the entry it moves;
  - when `relink` reports gone, the caller deletes the inode from `f.inodes`
    and calls `f.dropOpen`, under the same hold;
  - `unlink` and `Rename` make no other change to a link count. `createEntry`
    and `allocInode` keep theirs.
  - An entry that names no inode is removed or replaced as now, with no call
    to `relink`.

### 3. Link counts

These are blobfs behaviour. The `vfs` contract does not pin directory counts.

- **R1.** A regular file or a symlink has a count of 1. A directory has 2, plus
  1 for each subdirectory.
- **R2.**
  - `Mkdir` adds 1 to the parent, and `Rmdir` takes 1 from it.
  - Renaming a directory between different parents takes 1 from the source and
    adds 1 to the target.
  - A directory replaced by `Rename` takes 1 from its parent.
  - Creating, removing or renaming a file or symlink changes no directory's
    count.
- **R3.** The object `Rename` moves keeps its handle and its own count.

### 4. FSSTAT

`TotalFiles − FreeFiles` is the number of inodes in the namespace, root
included. Nothing else about `FSStat` is pinned.

### 5. No repair

- A bucket written by an earlier version keeps the directories it removed in
  memory and in every later snapshot, and what was created inside them until a
  call through such a directory's handle removes it or moves it out.
- Their handles still resolve, and serve calls as before.
- FSSTAT counts them.
- `New` reports nothing about them. A follow-up issue is recommended.

### 6. The test surface

- **L1.** After `Rmdir` of an empty directory, each of these calls on its
  handle returns `vfs.ErrStale`:
  - `GetAttr`; `SetAttr` with a `Mode`, by the owner; `Access`;
  - `Lookup`; `ReadDir`, plain and plus; `ReadLink`; `FSStat`; `Commit`;
  - `Create`; `Mkdir`; `Symlink` with a non-empty target;
  - `Remove`; `Rmdir`;
  - `Rename` with the handle as the source directory;
  - `Rename` with the handle as the target directory, which leaves the name
    being moved where it was;
  - `Read`; `Write` of one byte.

  The mount is writable and every name is valid.
- **L2.** `Rename` onto an empty directory, in the same parent and across
  parents:
  - the replaced directory's handle answers `vfs.ErrStale` to `GetAttr`,
    `Lookup`, `ReadDir` and `Create`;
  - the moved directory keeps its handle and its `FileID`;
  - `Lookup` of the target name returns the moved directory.
- **L3.** R1 to R3, checked through `GetAttr`'s `NLink`.
- **L4.** §4's count, across `Mkdir`, `Rmdir` and a rename onto an empty
  directory, and unchanged after `Sync` and `New` on the same bucket.
- **L5.** A model-based test, the tool ADR 0013 §1 gives the namespace tree.
  - It runs seeded random sequences of `Mkdir`, `Create`, `Symlink`, `Rmdir`,
    `Remove` and `Rename` over a small set of names, against a model of the
    tree.
  - Every call it makes is one whose outcome is determined.
    - **These succeed:** making a new name; `Rmdir` of an empty directory;
      `Remove` of a file or a symlink; `Rename` to a new name, or onto a
      compatible target (both non-directories, or both directories with the
      target empty), never onto itself and never into its own subtree.
    - **These fail, with some error and no change:** `Mkdir` or `Symlink` of a
      name that exists; `Rmdir` of a directory with entries; `Rename` onto a
      non-empty directory, or onto an entry of the other kind.
  - After every step:
    - the tree read through `ReadDir` and `Lookup` equals the model, with each
      `FileID`;
    - every count follows §3;
    - §4's count equals the model's number of objects;
    - every removed object's handle answers `GetAttr` with `vfs.ErrStale`, and
      every live object's handle answers with its `FileID`.
  - After the last step, it runs `Sync` and `New` on the same bucket, then
    checks the tree, the counts and §4's count again.
  - Every failure message gives the seed and the step.
- **Safety:** every bucket is under `t.TempDir()`, and every FS is built with
  `SnapshotRetention` −1 (ADR 0012 §9).
- **What only review can show:**
  - that every link-count change in `unlink` and `Rename` goes through
    `relink`;
  - the delete and the `dropOpen` when an inode is gone;
  - that nothing is repaired.

## Assumptions

Each is mine unless it says otherwise.

1. Status Accepted on creation, delegated by the owner's 2026-10-10 words
   above.
2. No repair: recommendation 14, as delegated by the same words.
3. A short ADR for a small fix. It records the no-repair decision and what
   follows from it, `relink`'s shape, which ADR 0014 §8 expects #50's fix to
   introduce, and a test surface that test-author can read.
4. A directory leaves whatever its stored count. This holds up even if some
   defect left a count wrong.
5. `relink`'s name and shape.
6. A removed directory's count is set to 0. That matters only once a Pinner
   exists.
7. The counts and FSSTAT are pinned for blobfs, not in `vfs`, so that a later
   backend may report directory counts differently. The redesign's inode
   record (`docs/DESIGN.md` §4) has no count field.
8. Keeping the refusal of a non-empty `Rmdir` as a rule (§1), though RFC 1813
   allows otherwise.
9. L5 is where ADR 0013 §1's model-based tests for the namespace tree begin.
   Its failure cases assert only that the call fails, because RFC 1813's codes
   for an incompatible RENAME (NFS3ERR_EXIST) are not the ones blobfs returns,
   and RFC 1813 says nothing about a rename into a directory's own subtree,
   which L5 therefore never makes. `Rename` answers an incompatible target with
   `vfs.ErrIsDir`, `vfs.ErrNotDir` or `vfs.ErrNotEmpty`, and a rename into a
   directory's own subtree with `vfs.ErrInval`.
10. The mount-time warning is left to a follow-up.
11. Not breaking: no deployment needs to act. So nothing here relies on the
    owner's second decision.
12. The wording of the `vfs` sentence is mine.
13. Read, not run. Every statement about the code was checked by reading it at
    `acc8719`, and nothing was run.
14. **Four passages changed at the session's ruling, after review.** The brief
    this ADR was written from pinned its decision text. Reviewing that text
    against the code found four passages to change, and the session ruled to
    change each as I recommended:
    - the `vfs` sentence excepts a `Write` of no bytes. `Write` answers one
      with success after `mutable()` and before it resolves the handle, and
      ADR 0014 §6 keeps that as one of its exceptions: "an empty write returns
      success after stage A", which comes before stage C, the resolve;
    - §2's callers say what becomes of an entry that names no inode, which
      `unlink` deletes, answering `vfs.ErrNoEnt`, and `Rename` replaces. No
      path in the code at `acc8719` makes one: an entry is added only for an
      inode in the table, and an inode leaves the table only with its entry;
    - §5's first bullet keeps only the leaked directories for good, since what
      was made inside one can still be removed or moved out through its
      handle;
    - the `CHANGES` line says a removed directory's handle answers
      NFS3ERR_STALE "as a removed file's does", since a few procedures answer
      otherwise for both, a WRITE of no bytes among them.

## Alternatives considered

- **Taking 2 from the count instead.** A wrong count would still leak.
- **Repairing on load.** Recommendation 14, as delegated, rules it out.
- **Treating a directory whose count is below 2 as gone.** That would be a
  repair of how old state is served.
- **A table-driven machine now.** ADR 0014 §8 keeps the lighter form until
  something implements `vfs.Pinner`.
- **Starting directories at a count of 1.** It breaks the Unix convention for
  counts.

## Consequences

- Making and removing directories no longer grows the inode table or the
  snapshot.
- A directory removed from now on has a stale handle, as a removed file already
  has, so nothing new can be stored under it.
- The directories an earlier build leaked stay in its bucket (§5) until a
  repair is decided.
- Every link-count change `unlink` and `Rename` make is in one function, which
  ADR 0014 §8's table-driven machine replaces once something implements
  `vfs.Pinner`.

## What this does not decide

- Repairing old buckets (§5).
- The mount-time warning (Assumption 10).
- Pinner's states (ADR 0014 §8).
- Hard links, which `vfs.FS` has no method for.
- FSSTAT's other fields (§4).
- The redesign's link counts (Assumption 7).
- Which error codes RENAME's refusals should use (Assumption 9).

## Sources

Fetched on 2026-10-22, while this ADR was written, through a tool that
summarises what it fetches, and quoted as it returned them when asked for
verbatim text. It reads a long page 100,000 characters at a time, and quoted
at most about 125 characters at once, so it returned a longer passage in
pieces, which are joined here with a space. The tool keeps a page for 15
minutes, so a fetch may have come from the cache of an earlier one made the
same day. The brief this ADR was written from quotes §2.6, §3.3.13, §3.3.14
and §3.3.18 from a fetch on 2026-10-18. Each of those passages was fetched
again, and matched the brief's quotations. The two RENAME sentences on stale
handles, and the search for a rule on renaming into a subtree, are this ADR's
own.

- **RFC 1813**, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>. The tool gave the text as
  229,793 characters, and read it in three portions, from characters 0,
  100,000 and 200,000.
  - §2.6, NFS3ERR_STALE: "Invalid file handle. The file handle given in the
    arguments was invalid." and, with nothing between them, "The file referred
    to by that file handle no longer exists or access to it has been revoked."
    (the `vfs` sentence; §1; L1)
  - §3.3.13, RMDIR, IMPLEMENTATION: "Note that on some servers, removal of a
    non-empty directory is disallowed." That RFC 1813 therefore allows a
    server to remove a non-empty directory is my reading of this sentence.
    (§1; Assumption 8)
  - §3.3.14, RENAME, DESCRIPTION: "If the directory, to.dir, already contains
    an entry with the name, to.name, the source object must be compatible with
    the target: either both are non-directories or both are directories and
    the target must be empty."; "If compatible, the existing target is removed
    before the rename occurs."; and "If they are not compatible or if the
    target is a directory but not empty, the server should return the error,
    NFS3ERR_EXIST." (§1; L2; L5; Assumption 9)
  - §3.3.14, RENAME, IMPLEMENTATION: "A file handle may or may not become
    stale on a rename." and "However, server implementors are strongly
    encouraged to attempt to keep file handles from becoming stale in this
    fashion." (the last sentence of the `vfs` comment; R3; L2)
  - Asked of each portion whether any sentence discusses renaming a directory
    into one of its own subdirectories, or a rename that would make a cycle in
    the tree, the tool found none. The nearest it found is in RENAME's
    IMPLEMENTATION: "In addition, neither from.name nor to.name can be an
    alias for from.dir." (Assumption 9)
  - §3.3.18, FSSTAT: tfiles is "The total number of file slots in the file
    system." "(On a UNIX server, this often corresponds to the number of
    inodes configured.)" ffiles is "The number of free file slots in the file
    system." (§4)
