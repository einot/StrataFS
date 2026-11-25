# 0028. A group asked for at creation is applied only for root or a member of it, and is otherwise left unapplied

**Status:** Accepted
**Date:** 2026-11-24
**Revised:** 2026-11-24 — the same day, before merge, after test-author's
commit `32d4cee` and coder's commit `de0a151`, read and not run. No rule
moved. One sentence of §8 was wrong and is corrected: G3's bullet on
`FS.changes` spoke of a call that returned nil where it meant a call that
made an object, against its own twelfth row and the facts above it. The
rest is record. By the session's report the owner decided on 2026-11-24 the
four calls this ADR put to the owner, each as recommended; *Context*, the
section that was *For the owner* and is now *Put to the owner, and decided*,
and Assumptions 1, 5, 10, 11 and 17 say so, §3 to §6, §12 and *Alternatives
considered* name the section by its new heading, and §6 says who decided
the question it left. §3 records what coder chose where the pins left room:
`allocInode` takes the two ids. §8 settles the other points test-author's
report raised, in G3 and G4, each as the committed test has it, lists the
names its wire test uses beyond those given, and records the red run the
session took (Assumption 20, which is new); its list of what only review
can show gains one clause from coder's choice. §9's pass is made:
`trace.tsv`'s row for `FS.createEntry` was read against `de0a151` and
holds, its `decided` now reads `ADR 0028 section 3`, and the module's
`Checked against` line, `docs/tla/README.md`'s statement of it and
vocabulary V3's now read `de0a151` (Assumptions 15 and 19, the second new,
which says how far the pass went). The module changed in one line of its
header comment, so TLC's outcome cannot differ, and CLAUDE.md's merge bar
asks the session for `tla.sh full` on the PR. §12's `CHANGES` line was read
against `de0a151` and stands as proposed. §1's rule and table, §2's
requests, §3's order, what §4 to §7 decide, G1's grid, the tables of G2 and
G3, what each call of G4 expects and what a client sees are unchanged, and
no test changes.
**Revised:** 2026-11-25 — after this ADR's PR was merged, to record one
decision of the owner's and the one change it asks for. No rule moved, and
nothing `blobfs` does. Assumption 13 and its entry under *What this does
not decide* left to the session whether `vfs.FS`'s doc comments should say
that a creation leaves unapplied an owner or group its caller may not
give. By the session's brief the owner decided on 2026-11-25 that they
should (*Context*, decision 5, which is new). So the doc comment above
`Create` in `internal/vfs/vfs.go` gains one paragraph, for `Create`,
`Mkdir` and `Symlink`, which Assumption 13 quotes and whose words are the
architect's. No declaration, signature, type or exported name of
`internal/vfs` changes. By the session's second brief of that day, the row
that section 16 of `docs/DESIGN.md` has for this ADR now says that the ADR
adds this to the `vfs.FS` contract, as the rows for ADR 0007, ADR 0017 and
ADR 0024 say of theirs (Assumption 13). *Context* counts the decision, and
its *What this constrains* names the comment; Assumption 13 is rewritten,
and says what it said; and the entry under *What this does not decide*
says the question is decided. §1 to §12, every test, every other
assumption and what a client sees are unchanged, and no other ADR is
revised: ADR 0014 and ADR 0024 need no cross-reference (Assumption 13).
There is no `CHANGES` line, a doc comment and a row of that table being
nothing a client or an operator can observe, and nothing under `docs/tla/`
changes, since `trace.tsv` names no symbol of `internal/vfs`.
**Issue:** #98

## Context

**The owner's decisions.** Three, as ADR 0014 and ADR 0018 record them: the
first two in the owner's words, and the third in the wording the owner
approved; a fourth the session reported; and a fifth, made after this ADR
was merged, which the session's brief for the revision of 2026-11-25 gave.
The brief this ADR was written from gave the second in substance. Nothing
here claims any approval beyond the five.

1. On 2026-10-10: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0027 were, and its design is the architect's (Assumption 1). *Put to the
   owner, and decided* lists the judgement calls an owner is most likely to
   weigh differently, each with its alternatives and with who decided it.
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." By the brief, a change a client can see still
   gets a `CHANGES` line, prefixed `BREAKING:` only where a running
   deployment must act (§12). Whether this one is breaking was
   Assumption 17's judgement, until the owner decided it (decision 4).
3. On 2026-10-22, the rules CLAUDE.md carries under "State machines and
   policies", which ADR 0018 records: a decision about whether a caller may
   do something is a pure policy in `internal/policy`, not an inline
   condition, and only an ADR exempts code from that.
4. On 2026-11-24, by the session's report, which gave each answer as the
   option this ADR recommended and not in the owner's words: the owner
   answered the four calls this ADR put to the owner, each as recommended. A
   group the caller may not set is left unapplied, and the creation succeeds
   (§4). The default group stays the caller's own (§5). A mode asked for
   keeps its set-ID bits (§6). And the `CHANGES` line takes no `BREAKING:`
   (§12; Assumption 17). All four were the architect's judgement until then.
   The report says nothing of any other assumption here, and each of those
   stays the architect's.
5. On 2026-11-25, by the session's brief for that day's revision, in the
   brief's words and not the owner's: "On 2026-11-25 the owner decided: add
   it." What is added is what Assumption 13 had left to the session to
   raise: that `vfs.FS`'s doc comments say that a creation leaves unapplied
   an owner or group its caller may not give. The brief also said what the
   comment is to carry and what line it keeps, which Assumption 13 records
   with the comment's words. Those words are the architect's, and the
   brief says nothing of any other assumption here.

**The defect.** #98, "CREATE, MKDIR and SYMLINK apply a requested group from
any caller", as the session gave its text (Assumption 2). "`createEntry`
applies `sa.GID` unconditionally (fs.go:711-713). SetAttr checks membership
(:492-498)." "Any caller can create files, including setgid executables,
owned by a group it is not in. ADR 0010 left this open." It was "Found by
`architect` while reconciling the TLA+ and FSM/policy plans, by reading the
code at `8377902`. Not reproduced by a test. Severity: low; medium while
mounts lack nosuid." Its done-when: "A requested group is applied only for
root or a member, and is otherwise ignored or refused (decided)." By the
brief, the security audit of #94's fix noted the same from the other side: a
creation's requested uid goes through `OpChown`, and its requested gid is
applied unchecked.

**What the code has.** Every statement about the code here was checked by
reading it at `344a95b`, the commit this ADR's branch was cut from, which is
`origin/main` with ADR 0027 merged. Nothing was run (Assumption 2). Code is
cited by symbol; the issue's line numbers are from `8377902` and name code
that has since moved.

- `createEntry`, in `internal/blobfs/fs.go`, is the body of `Create`, `Mkdir`
  and `Symlink`, and the one caller of `allocInode`. It calls `mutable()` and
  `validName`; fails a regular file's `sa.Size` above `maxFileSize` with
  `vfs.ErrFBig`; takes `FS.mu` for writing; resolves the directory; and asks
  `f.perm` one `OpAddEntry` on it, returning a refusal.
- If the directory has the name, it makes nothing: a non-exclusive `Create`
  returns the object the name names, or `vfs.ErrNoEnt` if the entry names no
  inode, or `vfs.ErrExist` if it names a directory, and every other call
  returns `vfs.ErrExist`. None of `sa` is applied on these paths.
- Otherwise it picks the mode, `*sa.Mode` if one is given and else `0o644`,
  `0o755` for a directory or `0o777` for a symlink, and calls `allocInode`.
  `allocInode` builds the inode with that mode masked to `0o7777`, with `UID`
  `c.UID` and `GID` `c.GID`, puts it in the inode table, advances `nextIno`,
  and calls `markDirty`.
- Then, with the inode already in the table: if `sa.UID` is set it asks
  `f.perm` an `OpChown` on `objectOf` of the new inode with `UID` `*sa.UID`,
  and sets the uid only if the answer is nil, ignoring a refusal (ADR 0014
  §6). And `if sa.GID != nil { n.GID = *sa.GID }`: whatever the caller, and
  whatever the group. That statement is the defect.
- It then sets a symlink's target and size, adds the name to the directory,
  raises the directory's link count for a new directory, sets the
  directory's mtime, calls `markDirty` again, and returns the new handle and
  attributes. It applies no `sa.ATime`, `sa.MTime` or `sa.Size`.
- `SetAttr`, through `setAttrRequests`, asks `f.perm` an `OpChgrp` with `GID`
  `*sa.GID` on the inode as it stands, before it applies anything, and a
  refusal fails the call with nothing applied (ADR 0024 §2).
- `policy.Unix()`'s `OpChgrp` row, in `internal/policy/policy.go`, refuses
  with `vfs.ErrPerm` unless the caller is root or owns `Object`, and then
  refuses with `vfs.ErrPerm` a caller that is not root unless
  `InGroup(Caller, r.GID)`. `InGroup(c, gid)` is true exactly when `gid` is
  `c.GID` or is in `c.GIDs`. The row reads nothing else of `Object`: not its
  type, its mode or its group.
- In `internal/nfs`, `getSattr` decodes a `sattr3` into a `vfs.SetAttr`,
  `set_gid3` into `GID`. The `create` handler passes it to `Create` for
  UNCHECKED and GUARDED, and an empty one for EXCLUSIVE; after an UNCHECKED
  create it applies `sa.Size` alone, through `SetAttr`, and drops that
  call's error. `mkdir` and `symlink` pass theirs to `Mkdir` and `Symlink`.
  MKNOD and LINK answer `NFS3ERR_NOTSUPP` without reaching the filesystem.
  Each of the three replies is the status, then for `NFS3_OK` the new
  handle, the new object's attributes and the directory's `wcc_data`, and
  for any other status the `wcc_data` alone. `callerFrom` copies the
  credential's uid, gid and gids into the `vfs.Caller`, and `readCred`, in
  `internal/sunrpc`, keeps at most 16 gids.
- No other path makes an inode for a caller. `initEmpty` makes the root
  directory, owned as `Config` says (ADR 0010).

So the issue's premise holds at `344a95b`, with one nuance in what it says of
ADR 0010. That ADR's *What this does not decide* names the unchecked gid as a
fact, among the ways a client can give an object the id 4294967295, and
leaves open whether the server should refuse that value. The unchecked group
as a decision to make is first named by ADR 0014 (its Assumption 20). Both
are answered below: the first by §1, and the second is still open (*What
this does not decide*).

**What RFC 1813 says** (*Sources*).

- Of `sattr3`, §2.5: "The sattr3 structure contains the file attributes that
  can be set from the client." "Each union indicates whether the
  corresponding attribute is to be updated, and if so, how."
- Of CREATE, §3.3.8: "UNCHECKED means that the file should be created
  without checking for the existence of a duplicate file in the same
  directory. In this case, how.obj_attributes is a sattr3 describing the
  initial attributes for the file." Of EXCLUSIVE: "No attributes may be
  provided in this case", and "Once the client has performed a successful
  exclusive create, it must issue a SETATTR to set the correct file
  attributes."
- Of MKDIR, §3.3.9, `attributes`: "The initial attributes for the
  subdirectory." Of SYMLINK, §3.3.10, `symlink_attributes`: "The initial
  attributes for the symbolic link."
- The ERRORS lists of CREATE, MKDIR and SYMLINK hold the same twelve codes,
  `NFS3ERR_ACCES` among them and `NFS3ERR_PERM` not. Of every such list, §3:
  "These lists are not intended to be the definitive statement of all of the
  errors which can be returned by any specific procedure, but as a guide for
  the more common errors which may be returned."
- Of the codes, §2.6: `NFS3ERR_PERM` is "Not owner. The operation was not
  allowed because the caller is either not a privileged user (root) or not
  the owner of the target of the operation."
- Asked directly, the tool found nothing in the 200,000 characters read on
  how a server chooses a new object's owner or group, on what it does with a
  uid or gid a creation asks for, or on set-group-ID directories. So the
  protocol leaves all three to the server.

**What POSIX says**, in Issue 8 (*Sources*). Of `open()` with `O_CREAT`:
"the group ID of the file shall be set to the group ID of the file's parent
directory or to the effective group ID of the process;" and "Implementations
shall provide a way to initialize the file's group ID to the group ID of the
parent directory. Implementations may, but need not, provide an
implementation-defined way to initialize the file's group ID to the
effective group ID of the calling process." Its RATIONALE: "Conforming
applications should not assume which group ID will be used." Of the mode:
"When bits other than the file permission bits are set, the effect is
unspecified." `mkdir()` says the same of a directory's group, and of its
mode: "When bits in mode other than the file permission bits and S_ISVTX are
set, the meaning of these additional bits is implementation-defined."
`symlink()` says the same of a symbolic link's group, and: "The values of
the file mode bits for the created symbolic link are unspecified." None of
the three takes a group from its caller: a POSIX process sets a group
afterwards, with `chown()`, whose rule for a caller without privilege is the
one `OpChgrp` has. And of `chmod()`, as ADR 0025's *Sources* quote it: "If
the calling process does not have appropriate privileges, and if the group
ID of the file does not match the effective group ID or one of the
supplementary group IDs and if the file is a regular file, bit S_ISGID
(set-group-ID on execution) in the file's mode shall be cleared upon
successful return from chmod()."

**How Linux's server answers**, `fs/nfsd` at the tag `v6.6` (*Sources*).
`nfsd_create_setattr` is called for CREATE, from `nfsd3_create_file`, and by
`nfsd_create_locked` and `nfsd_symlink`, which MKDIR and SYMLINK reach. It
clears the mode from what is still to set, under the comment "Mode has
already been set by file creation.", and then: "Setting uid/gid works only
for root. Irix appears to send along the gid on create when it tries to
implement setgid directories via NFS:", over
`if (!uid_eq(current_fsuid(), GLOBAL_ROOT_UID)) iap->ia_valid &= ~(ATTR_UID|ATTR_GID);`.
So a uid or gid asked for at creation is dropped, silently, for every caller
but root, a member of the group included, and the creation succeeds. The
group the object then has is the VFS's, from `inode_init_owner`: the
directory's, if the directory's mode has `S_ISGID`, where a new directory
also inherits the bit, and the caller's otherwise. `mode_strip_sgid` clears
`S_ISGID` from the mode of a new non-directory that has both `S_ISGID` and
group execute, in such a directory, unless the caller is in the directory's
group or holds `CAP_FSETID`. That is the fix of the commit "Fix up
non-directory creation in SGID directories", whose message says: "group
directories writable by non-group members should not imply that such
non-group members can magically join the group, so make sure to clear the
sgid bit on non-directories for non-members".

**What the two kernel clients send** (*Sources*; Assumption 16).

- *Linux*, at `v6.6`. `nfs_create`, `nfs_mkdir` and `nfs_symlink` set
  `attr.ia_valid = ATTR_MODE` and nothing of a uid or gid, and
  `encode_sattr3` sends a uid or gid only where `ATTR_UID` or `ATTR_GID` is
  set. So, as read, a Linux client asks for no group at creation.
- *macOS*, at the `main` branch of Apple's NFS sources. `nfs3_vnop_create`,
  `nfs3_vnop_mkdir` and `nfs3_vnop_symlink` each call
  `nfs_avoid_needless_id_setting_on_create`, whose comment reads "Don't
  bother setting UID if it's the same as the credential performing the
  create." and "Don't bother setting GID if it's the same as the directory
  or credential.", and `nfsm_chain_add_v3sattr_f` sends a gid only if one is
  still active. After the reply, under "clear ID bits if server didn't use
  them (or we can't tell)", the client marks a uid or gid it sent as
  unsupported where the new object's attributes do not show it. So, as
  read, a macOS client asks for a group at creation only when the group its
  kernel proposes for the new object is neither the directory's nor the
  caller's own, and it reads the reply to learn whether the server used it.
  Which group the kernel proposes, and what it does with an id marked
  unsupported, were not read.

**What the ADRs say.**

- ADR 0014 §5 pins `Request`, `InGroup` and the rows of `Unix()`, `OpChgrp`'s
  among them. Its §6 gives the stages A to E and the requests each method
  makes. For `Create`, `Mkdir` and `Symlink`: `OpAddEntry` on the directory;
  then, when a new inode is made and `sa.UID` is set, `OpChown` on the new
  inode, where "`sa.UID` is applied only if that is allowed, and a refusal
  is ignored"; and "A requested gid is applied as now, with no check, until
  #98's fix." Its *The wiring at `a6cff50`* places the `OpChown` "after
  `allocInode` has made the new inode `n` and before `sa.GID` is applied",
  with `Object` "`n` as `allocInode` made it", and its Assumption 46 calls
  that place right "since a refusal is ignored and the create succeeds
  either way". Its Assumption 20: "giving it an `OpChgrp` request now would
  refuse groups that are applied today, which is #98's decision to make."
  Its §11 has W2 for the requested uid, and says that no test relies "on how
  `sa.GID` at creation is handled (#98)".
- ADR 0018 §1 to §3 make the policy rule standing: a change to what an
  existing decision allows makes that decision a pure policy in
  `internal/policy`. Its §2 keeps what a request names, and what an
  operation is, outside the rule.
- ADR 0024 §1: a `vfs.FS` method of `blobfs` that returns an error has
  changed nothing in the namespace, and `markDirty` is called exactly where
  a change is made. Its §2: `SetAttr` asks every request before it applies
  any attribute, each with `Object` from the inode as it stands at the call,
  and applies all of `sa` or none, which `vfs.FS.SetAttr` promises. It
  leaves "`createEntry`'s handling of `sa` (#98)" untouched.
- ADR 0015 records the trust model: `readCred` believes the uid, the gid and
  the gids of an AUTH_SYS credential, and by default any local process can
  connect "and claim any uid, root included". Its *Context* counts the
  unchecked gid among the links of its chain, and its §1 puts `nosuid` on
  every mount command strata prints, documents or scripts.
- ADR 0025 puts to #155, "A WRITE or a size change leaves a file's
  set-user-ID and set-group-ID bits set", in a comment, that this server
  also leaves `S_ISGID` set on a change of mode by an owner outside the
  file's group (its Assumption 22). ADR 0025, ADR 0026 and ADR 0027 each
  leave #98 out.
- ADR 0010 §2 refuses 4294967295 for `-uid` and `-gid`, and leaves open
  whether the server should refuse it through the protocol.
- ADR 0007 §4 pins the order of errors, and its §3 that `Create` fails a
  size above the limit before it creates anything.
- ADR 0013 §1 gives permission rules to exhaustive tests of the policy and
  to security review, not to TLA+, and its §8 says what a refactor of traced
  code changes under `docs/tla/`. `docs/tla/trace.tsv` names
  `FS.createEntry`.

**What this constrains.** `createEntry`, and `allocInode` as far as coder
changes how an owner reaches a new inode, in `internal/blobfs/fs.go`;
ADR 0014 §6's bullet for the requests `Create`, `Mkdir` and `Symlink` make,
what its *The wiring at `a6cff50`* and its Assumption 46 say of where the
creation-time `OpChown` is asked, and §11's statement about `sa.GID`, by
cross-reference; ADR 0010's bullet on 4294967295, by cross-reference; the
tests of `internal/policy`, `internal/blobfs` and `internal/nfs`; and, after
the code lands, one row of `docs/tla/trace.tsv` and the commit the
CommitProtocol module is checked against (§9). No non-test code of
`internal/policy`, `internal/nfs` or `internal/vfs` changes, and nothing in
the bucket, the flags or the wire formats. On the wire, three procedures
answer `NFS3_OK` as before, and the new object's attributes can report
another group than they did. Since the revision of 2026-11-25 this ADR also
constrains the `vfs.FS` contract, through one paragraph of the doc comment
above `Create` in `internal/vfs/vfs.go`, the one change to that package, in
which no declaration moves (Assumption 13).

## Decision

### 1. The rule

**Terms.** ADR 0014 §5's stand: *root* is `Caller.UID == 0`. A caller is *in*
a group when `policy.InGroup(Caller, gid)` holds: the gid is the caller's
`GID` or one of its `GIDs`. A *creating call* is a `Create`, `Mkdir` or
`Symlink` that makes a new object; one whose name the directory already has
makes none. `sa` is the call's `vfs.SetAttr`.

**The rule.** A creating call gives its new object the group `*sa.GID` only
if the installed policy allows an `OpChgrp` for that group on the new
object (§2). `Unix()` allows it to root and to a caller in that group, and
to no one else. A group it does not allow is left unapplied: the object is
made in the group it would have had if none had been asked for, which is the
caller's `GID` (§5), and the call returns what it would have returned (§4).

**One rule.** `OpChgrp` is the request `SetAttr` makes for `sa.GID`, and one
row of `Unix()` answers both. So one rule decides a group change, whether a
`SETATTR` or a creation carries it, and a change to that row changes both.
What differs between the two calls is what each does with a refusal (§4).

For a directory that gives every caller write and search, and the callers
`502:30`; `504:30` with the gids 40 and 41; and root, `0:0`:

| Caller | Group asked for | The new object's group | At `344a95b` |
|---|---|---|---|
| `502:30` | none | 30 | the same |
| `502:30` | 30, its own | 30 | the same |
| `502:30` | 20, which it is not in | 30: left unapplied | 20 |
| `502:30` | 40, which it is not in | 30: left unapplied | 40 |
| `504:30`, gids 40 and 41 | 41 | 41 | the same |
| `504:30`, gids 40 and 41 | 20 | 30: left unapplied | 20 |
| root | 20 | 20 | the same |
| root | 999, which no caller is in | 999 | the same |
| root | none | 0 | the same |

Each row holds for `Create`, `Mkdir` and `Symlink` alike, and every call in
it returns nil.

**What a client can observe.** One thing. A CREATE that is UNCHECKED or
GUARDED, a MKDIR or a SYMLINK whose `sattr3` has `set_gid3` TRUE, from a
caller that has write and search on the directory, sends a uid other than 0,
and names a gid that is neither its credential's gid nor among its
credential's gids, answers `NFS3_OK` as before, and the attributes of the
new object in the reply report the caller's gid, where they reported the gid
asked for. Nothing else changes: no status, no reply's form, no other
attribute of the object, and nothing for root or for a caller in the group.
An EXCLUSIVE create carries no attributes. A `SETATTR` answers as it did.

### 2. The requests a creation makes

A `Create`, `Mkdir` or `Symlink` asks `f.perm`, holding `FS.mu` for writing,
in this order:

1. `OpAddEntry` on the directory, as ADR 0014 §6 has it. A refusal is
   returned unchanged, and nothing after it is asked.
2. Then, only if the call is about to make a new object:
   1. if `sa.UID` is set, `OpChown` with `UID` `*sa.UID`;
   2. if `sa.GID` is set, `OpChgrp` with `GID` `*sa.GID`.

**The object asked about.** Both requests of step 2 carry the same `Object`,
`N`: the new object as the call makes it when no owner and no group is asked
for.

```go
policy.Object{Type: t, Mode: mode & 0o7777, UID: c.UID, GID: c.GID}
```

`t` is the kind the call makes, `mode` the mode `createEntry` chose, and `c`
the caller. `N` does not depend on either answer: the `OpChgrp` is asked
whether the `OpChown` was allowed or not, and its `Object` has the caller's
uid even where a uid asked for will be applied. So each verdict stands
alone, and the group's is a verdict about the caller that made the call
(Assumption 6). This is ADR 0024 §2's rule for `SetAttr`, that every request
is made before any attribute is applied, each against the object as it
stands. For `Unix()` it changes no verdict: `OpChgrp` reads `Object.UID`
only to ask whether the caller is root or the owner, and a caller whose
`OpChown` to another uid was allowed is root.

**The fields.** Each request sets `Op`, `Caller` and `Object`, and `UID` for
the `OpChown` or `GID` for the `OpChgrp`, and nothing else: `Dst` is the
zero `Object`, `Entry` and `Replaced` are nil, and `Advisory` is false.

**The answers.** nil applies the attribute. Any other answer, whatever its
status, leaves the attribute unapplied, is not returned, is not logged, and
ends nothing (Assumption 9).

**A call that makes nothing asks step 1 alone.** A `Create`, `Mkdir` or
`Symlink` of a name the directory has makes no object and applies none of
`sa`, so it asks neither request of step 2, as ADR 0014 §11's W2 for the
requested uid already pins for the `OpChown`.

**No request for the mode, the times or the size** (Assumption 12).
`createEntry` stores the mode asked for, masked to `0o7777`, on an object
its caller owns, and there is no caller it refuses that to. It applies no
time and no size, at `344a95b` and since (*What this does not decide*).

**`internal/policy` does not change.** No `Op`, no field of `Request` and no
row is added or altered, so ADR 0014 §5's *Growth* rule has nothing to say.
`OpChgrp`'s row is what it was, and at creation its first line always holds,
since `N` is the caller's own. So under `Unix()` the creation-time `OpChgrp`
is allowed exactly to root and to a caller in the group, which is the
issue's done-when. G1 (§8) pins that reading of the row.

Step 2.1 is ADR 0014 §6's as it was, but for its place (§3): the same `Op`,
the same fields with the same values, and the same ignored refusal.

### 3. The order in `createEntry`

| Stage | What `createEntry` does | What it can return |
|---|---|---|
| A, the mount gate | `mutable()` | `vfs.ErrROFS`, `vfs.ErrStale` |
| B, the arguments | `validName`; a regular file's `sa.Size` above the limit | `vfs.ErrInval`, `vfs.ErrNameTooLong`; `vfs.ErrFBig` |
| C, the handle | takes `FS.mu` for writing; resolves the directory | `vfs.ErrBadHandle`, `vfs.ErrStale` |
| D, the directory's row | `OpAddEntry` | under `Unix()`, `vfs.ErrNotDir`, `vfs.ErrAcces` |
| E, the namespace | the name exists: a non-exclusive `Create` returns what it names; anything else is refused | `vfs.ErrNoEnt`, `vfs.ErrExist` |
| the attributes | chooses the mode; asks `OpChown` and `OpChgrp` (§2) | nothing: no status comes from either |
| the change | makes the object and its entry; `markDirty` | nil |

`Symlink`'s check of an empty target still comes before stage A.

**Every request is asked before the call changes anything.** The two
requests of §2's step 2 are made after stage E and before `allocInode`,
which is the call's first change: it puts the inode in the table, advances
`nextIno` and calls `markDirty`. So when the policy is asked either of them,
the inode table, the allocator, the directory, `FS.dirty` and `FS.changes`
are as they were when the call took the lock, as they already are when it is
asked the `OpAddEntry`.

This moves the `OpChown`, which ADR 0014 §6 placed after `allocInode`
(Assumption 7). Under this ADR nothing a client or a recording policy sees
depends on the move: a refusal of either request fails nothing, and the
`Object` each carries has the values the inode will have. It is pinned so
that the function has one shape, every verdict and then the change, and so
that no answer a policy gives, and no later decision about what a refusal
does (*Put to the owner, and decided*, 1), can leave an inode in the table
with no entry that names it. G3 (§8) tests it, through `FS.changes` as the
policy itself finds it.

**The new object.** In the same hold, `createEntry` then makes the object
with:

- the type and the mode chosen, masked to `0o7777`, as now;
- `UID` `*sa.UID` if the `OpChown` was asked and allowed, and `c.UID`
  otherwise;
- `GID` `*sa.GID` if the `OpChgrp` was asked and allowed, and `c.GID`
  otherwise;
- everything else as now: the link count, the parent, the times, a
  directory's entry map, and a symlink's target and size.

How the two ids reach the inode is coder's: `allocInode` may keep its
signature, with the two fields set on the inode it returns, or may take
them. Either is one hold of `FS.mu` for writing, in which nothing reads the
inode between the two steps. The pass after coder's commit (§9) records
which, below.

**What coder's commit chose**, recorded against `de0a151` where the pins
above left room (Assumption 19):

- `allocInode` takes the ids. Its signature is
  `func (f *FS) allocInode(t vfs.FileType, mode, uid, gid uint32, parent uint64) *inode`,
  with the two ids in place of the caller it took at `344a95b`, and it
  makes the inode with `UID: uid` and `GID: gid`. It still masks the mode
  to `0o7777`, puts the inode in the table, advances `nextIno` and calls
  `markDirty`, and `createEntry` is still its one caller. Its doc comment
  says why it takes the ids, and cites this section.
- `createEntry` builds `N` once, after it has chosen the mode, as
  `policy.Object{Type: t, Mode: mode & 0o7777, UID: c.UID, GID: c.GID}`,
  which is §2's expression, and both requests carry that one value.
- It keeps the two ids in two locals, which start as `c.UID` and `c.GID`.
  Each is replaced by the id asked for only where that id's request
  returned nil, and the two are then passed to `allocInode`. So the inode
  enters the table with the owner and the group the call gives it, and
  `createEntry` writes neither field after `allocInode` returns.
- Each request is built in place, with `Op`, `Caller`, `Object` and its one
  id, and asked by `f.perm.Check`. Neither goes through `checkPerm`, which
  takes an inode, and none exists yet. `checkPerm` and `objectOf` are
  unchanged, and the `OpAddEntry` is still asked through `checkPerm`. No
  helper was added.
- The mask is written twice, once for `N` in `createEntry` and once for the
  inode in `allocInode`, each on the mode `createEntry` chose. So that
  `N`'s mode is the inode's rests on the two expressions staying the same,
  which is review's to keep (§8).
- `createEntry`'s doc comment gives §2's requests and their order, cites
  ADR 0014 §6 and this ADR's §2 and §3, and keeps its `TLA+:` line. No
  other line of `internal/blobfs/fs.go` changed, and no other non-test file
  did, by the session's diff (§9).

**ADR 0024 §1's rule holds, with no exception.** A creating call that
returns an error returns it from stages A to E, before any change. One that
returns nil has made one object and one entry and marked the namespace in
the hold that made them. Neither request of step 2 can fail the call, so
neither can leave half of one.

### 4. A group the caller may not set is left unapplied

**The disposition.** A refused `OpChgrp` at creation is treated as a refused
`OpChown` there has been since ADR 0014 §6: the attribute is not applied,
and the call goes on (Assumption 5; *Put to the owner, and decided*, 1).

**Why not refuse the call.**

1. **One `sattr3`, one disposition.** A uid the caller may not give is
   already left unapplied. A group that failed the call beside it would give
   two attributes of one `sattr3` two dispositions, for no reason a client
   could state.
2. **It is what the most-used server does.** Linux's server drops a uid or
   gid asked for at creation, for every caller but root, and succeeds
   (*Context*). A client written against it expects the creation to
   succeed, and reads the reply's attributes to learn the group.
3. **The clients that send a group at creation send it to place the object,
   not to assert a right.** The comment in Linux's server names the case, a
   client that sends "the gid on create when it tries to implement setgid
   directories via NFS", and Apple's client sends one only when its kernel
   proposes a group that is neither the directory's nor the caller's
   (*Context*). Refusing would turn every such creation by a user outside
   the group into a failure, in a directory whose mode lets that user
   create.
4. **The protocol gives the refusal no status.** `NFS3ERR_PERM` is in none
   of the three procedures' ERRORS lists. The lists are not definitive, and
   ADR 0026 §3 answers that code for `REMOVE` anyway, so this is the weakest
   of the four reasons.

**What it costs.** The caller is not told by the status. A client learns the
group from the reply's attributes, which report it, as it must on any server
that picks a new object's group itself. A client that asked for a group it
is not in, and does not read the reply, holds an object in its own group
with the group bits of the mode it asked for: the audience of those bits is
the caller's own group, where it meant another. That is the object it would
have made by asking for no group. §6 says what it means for a mode with a
set-ID bit.

**The same rule, another disposition, for `SETATTR`.** A `SETATTR` with a
group the caller may not set fails with `NFS3ERR_PERM` and applies none of
its attributes (ADR 0024 §2). So the attributes that follow an EXCLUSIVE
create, which a client sends by `SETATTR`, are all refused where the same
attributes in an UNCHECKED create would have made the object without the
group. The difference is in what each call is for. A `SETATTR` is asked to
set attributes, and one it may not set is its failure. A creation is asked
to make an object, and makes it, with the attributes its caller may give it.
Linux's server differs between the two in the same way.

**The rest of `sa`.** Each attribute goes by its own rule, whatever becomes
of the group:

- the mode asked for is stored, masked to `0o7777`;
- a uid asked for is applied or left by its own `OpChown`;
- a size is not applied by `createEntry`, and `internal/nfs` still applies an
  UNCHECKED create's size afterwards, through `SetAttr`;
- times asked for are not applied, as before.

**A refusal is not logged**, as no refusal of a policy is (ADR 0018 §5).

### 5. The default group stays the caller's

A new object's group, when none is asked for or the one asked for is left
unapplied, is the caller's `GID`, as at `344a95b` (Assumption 10; *Put to
the owner, and decided*, 2). POSIX allows either the directory's group or
the process's (*Context*). A caller is always in its own `GID`, which is
what §6 rests on.

**What it leaves undone.** POSIX also asks that an implementation "provide a
way to initialize the file's group ID to the group ID of the parent
directory". This server has none of its own: it stores and reports a
directory's set-group-ID bit, and reads it nowhere. Until this ADR a client
could have the directory's group by asking for it at creation, whoever its
user was. Since this ADR it can have it only for root or for a user in that
group, and a user outside it gets the user's own. A Linux client, which asks
for no group at creation, got the caller's own before and gets it now.
Honouring a directory's set-group-ID bit is a change of the default for
every client, with decisions of its own (§6), and is left to an issue of its
own (*What this does not decide*).

### 6. Set-ID bits in a mode asked for

**No rule is added** (Assumption 11; *Put to the owner, and decided*, 3). A
creating call stores the mode it is given, masked to `0o7777`, the
set-user-ID, set-group-ID and sticky bits included, as at `344a95b`.

**Why none is needed to close the issue's case.** The issue names "setgid
executables, owned by a group it is not in". By §1 and §5, under `Unix()`,
the object a creating call makes for a caller that is not root is in the
caller's own `GID`, or in a group that caller is in. So no creation gives a
caller that is not root an object in a group it is not in, and a
set-group-ID bit on a new file names a group its creator belongs to, which
that creator may give a file of its own on any POSIX system. The owner is
the same: only root makes an object another uid owns, so a set-user-ID bit
on a new file names its creator, or a uid root chose.

**What a mode with the bit and a group left unapplied makes.** A caller
outside group 20 that asks for mode `0o2755` and group 20 gets a file of
mode `0o2755` in its own group. That is not the file it asked for, and it
is not an escalation: the caller could have asked for that file outright.
Linux's server makes the same file from the same call. Whether such a call
should lose the bit was put to the owner, who decided that it keeps it
(*Put to the owner, and decided*, 3).

**What stays open, and where.**

- *A `SETATTR` of the mode.* An owner outside its file's group, which it can
  be by root's gift or by a change in the groups its credential lists, can
  still set `S_ISGID` on it, where POSIX has `chmod()` clear the bit. That
  is not a creation, and ADR 0025 put it to #155.
- *A default group that is the directory's.* Once a new object can take a
  group its creator is not in, by a set-group-ID directory, the bit on a new
  regular file is the defect Linux fixed in "Fix up non-directory creation
  in SGID directories". The ADR that changes §5's default decides it.
- *A mount without `nosuid`.* Every mount command strata gives carries it
  (ADR 0015 §1), so a set-ID bit has no effect through such a mount whatever
  group the file is in.

### 7. Security

**What narrows**, and nothing widens. Under `Unix()`, a creating call from a
caller that is not root and not in the group it asks for makes its object in
the caller's own group, where it made it in the group asked for. No call
that was refused is allowed, no call that was allowed is refused, and no
status changes. Root is as it was.

**What holds since.** Under `Unix()`, the object a creating call makes is
owned by its caller, or by the uid root asked for; and its group is its
caller's `GID`, or a group asked for by root or by a caller in it. Both
halves are decided by `internal/policy`, in the hold that makes the object,
before anything is changed (§3).

**The ids are the client's claim.** `callerFrom` takes the uid, the gid and
the gids from the call's AUTH_SYS credential, which `readCred` believes, and
by default any process on the machine can connect "and claim any uid, root
included" (ADR 0015 §8). So "root" and "in the group" are whoever sends that
uid or that list. The rule is a rule about a claimed identity, as every
other row is.

**What it gives each kind of caller** (Assumption 16).

- *A caller that speaks the protocol itself and chooses its credential.*
  Nothing binds it. It can send uid 0, or put the group in its list. #6, #90
  and #95 own that, and root squash alone would not change it, since the
  list is the caller's too.
- *A user behind a Linux kernel client.* As read, that client asks for no
  group at creation, so nothing changes for such a user.
- *A user behind a macOS kernel client.* As read, that client asks for a
  group only when its kernel proposes one that is neither the directory's
  nor the caller's own. Where it does, and the user is outside the group,
  the object is now made in the user's own group, and the client sees from
  the reply that the server did not use the group it sent.
- *A caller under a stricter trust model*, should #6 bring one. The rule is
  then already where that model needs it, read in the hold that makes the
  object.

**What the rule does not protect**, with or without #6:

- *The group of an existing object.* A creation of a name that exists
  applies nothing, and a `SETATTR` goes by the same row.
- *A set-ID bit set by `SETATTR`* (§6; #155).
- *A caller in more than 16 groups.* AUTH_SYS carries at most 16, so a
  group the client left out is one the caller is not in here, as for
  `SETATTR`.
- *The value 4294967295.* A caller that claims it as its gid, or as one of
  its groups, or uid 0, can still give an object that group (*What this
  does not decide*).

**The seam.** `createEntry` now reads `f.perm` once more per creation that
asks for a group. `New` sets the field and nothing else writes it (ADR 0014
§6), and a test's replacement follows ADR 0014 §11's safety rule.

**Why it is worth deciding now.** It is the last attribute `createEntry`
applied on a caller's word with no rule behind it, so after it every
identity an object is given at creation is `internal/policy`'s decision. It
costs one request on a path that already makes one beside it, and no row.

### 8. The test surface

Every name is spelled as in Go. test-author cannot read the non-test files
of `internal/policy`, `internal/blobfs` or `internal/nfs`, so what a test
may use is here, in ADR 0014 §5 and §11, in ADR 0007 §5, in
`internal/vfs/vfs.go`, or in a `_test.go` file. The tests are G1 to G4, in
three new files. Each file uses only names the code has at `344a95b`, so
that it builds on that commit's tree and the session can take a red run.

**What becomes pinned.** How `sa.GID` at creation is handled, which ADR 0014
§11 left to #98: that it is asked as `OpChgrp`, of which object, in which
order, and what each answer does. The rows U3 and U6 of ADR 0014 §11 stay
unpinned, and no test here relies on them: no `Read`, `Write` or size is
asked of a symlink, and no time is set.

**Facts a test may rely on**, beside those of ADR 0014 §11, ADR 0024 §7,
ADR 0026 §8 and ADR 0027 §10, which stand:

- `Create` with no `Mode` makes a regular file of mode `0o644`, `Mkdir` a
  directory of `0o755` and `Symlink` a symlink of `0o777`. A `Mode` given is
  stored masked to `0o7777`, set-ID bits included, and `GetAttr` reports
  it.
- The object a creating call makes has its caller's `UID`, unless a uid
  asked for is allowed, and its caller's `GID`, unless a group asked for is
  allowed (§1, §3). Under `Unix()` a uid asked for is applied for root, and
  for another caller only if it is that caller's own; a group asked for is
  applied for root and for a caller in it. `vfs.Caller{UID: 0}` is root,
  and its `GID` is 0.
- A creating call that returns nil has marked the namespace: `FS.dirty` is
  true after it, and `FS.changes` is above what it was. By how much is not
  pinned. A `Create`, `Mkdir` or `Symlink` that returns an error has changed
  nothing, and neither has a non-exclusive `Create` that returns an object
  the name already names.
- Every method asks its requests holding `FS.mu`, for reading or for
  writing, and `createEntry` for writing (ADR 0014 §6, stage D). So a
  test's policy runs in a goroutine that holds that lock, whichever call
  asks it, the calls of a test's setup included: it may read `FS.changes`
  and `FS.dirty` of the `FS` it is installed in without taking a lock, and
  it takes no lock of that `FS` and calls no method of it, either of which
  could deadlock. What it notes it keeps under a mutex of its own.
- With a recording policy, a creating call makes exactly §2's requests in
  §2's order, each with the fields §2 gives and no other field set.
  `vfs.ErrNXIO` is a status no row of `Unix()` returns.
- A mount made with `uc43Config` has a root directory of mode `0o755` owned
  by `501:20`. `startServer`'s is the same, and `dial`'s client sends
  `501:20`.

**`internal/policy`** (package `policy_test`), in one new file,
`internal/policy/create_group_test.go`, with helpers of its own prefixed
`cg`.

- **G1. `OpChgrp` on an object its caller owns**
  (`TestUnixAllowsAGroupOnAnObjectItsCallerOwnsOnlyToRootOrAMember`).
  `Unix().Check` of `Request{Op: OpChgrp, Caller: c, Object: o, GID: g}` is
  compared with an oracle written from §1, nil exactly when `c.UID == 0`,
  `g == c.GID` or `g` is among `c.GIDs`, and otherwise the bare
  `vfs.ErrPerm`, compared with `==`. The oracle is written out, not taken
  from `policy.InGroup`. It runs over at least:
  - the callers `vfs.Caller{UID: 0}`; `{UID: 0, GID: 0, GIDs: [5]}`;
    `{UID: 502, GID: 30}`; `{UID: 504, GID: 30, GIDs: [40, 41]}`; and
    `{UID: 505, GID: 30, GIDs: [100, 101, …, 115]}`, sixteen of them;
  - as `Object`, one the caller owns, `UID: c.UID`, of each type
    `vfs.TypeReg`, `vfs.TypeDir` and `vfs.TypeLnk`, each mode of 0, `0o644`,
    `0o755`, `0o2755`, `0o6755` and `0o7777`, and each `GID` of `c.GID`, 20
    and 41;
  - as `g`, each of 0, 5, 20, 30, 40, 41, 100, 115, 116, 999 and
    4294967295.

  It is a characterisation: the row does not change, so it passes at
  `344a95b` and after. It pins what §2 rests on, that for an object the
  caller owns the row reads the caller and the group asked for and nothing
  of the object, so that a later change to the row cannot loosen a creation
  unseen.

**`internal/blobfs`** (package `blobfs`), in one new file,
`internal/blobfs/create_group_test.go`, with helpers of its own prefixed
`cg`. The harness is ADR 0011 §7's, as ADR 0014 §11 and ADR 0026 §8 apply
it: a `*store.Local` under `t.TempDir()` behind an unarmed `uc43Store`,
through `uc43Setup`; `uc43Config`; every `New` and every `FS` call in
`uc43Go`, waited for under `uc43Bound`; nothing sleeps, polls or runs in
parallel; `FS.dirty` and `FS.changes` read holding `FS.mu` for reading,
through `sa24Read`, or by a policy as the facts above allow. A test may
reuse `uc43Env` and its methods, `remount` among them; the `pw` helpers,
with `pwW2Tree`, `pwW2D`, `pwDir`, `pwReg` and `pwLnk`; the `sa24` helpers
and `sa24Other`; `sp24Recorder`, `sp24Mount` and `sp24Root`; and
`testCaller`. `FS.perm` is replaced only as ADR 0014 §11's W1 allows, once,
right after `New` returns, through `sp24Mount`. No other field of `FS` is
written, and no existing test file changes but in two comments (below).

The callers are `testCaller` (`vfs.Caller{UID: 501, GID: 20}`), who owns the
root directory; `sa24Other` (`vfs.Caller{UID: 502, GID: 30}`); `cgMulti`,
new, `vfs.Caller{UID: 504, GID: 30, GIDs: []uint32{40, 41}}`; and `sp24Root`
(`vfs.Caller{UID: 0}`).

- **G2. The rule through the `FS`**
  (`TestARequestedGroupAtCreationIsAppliedOnlyForRootOrAMember`). `e.newFS`,
  with `Unix()` left in place. `testCaller` makes `pub`, a directory of mode
  `0o777`, in the root, and in it `x`, a regular file with no `Mode`. For
  each row the call returns nil; the attributes it returns and `GetAttr` of
  the handle it returns report the owner and the group the row gives, the
  type of the kind made, and the mode the row gives or, where it gives
  none, the kind's own, `0o644`, `0o755` or `0o777`; and `testCaller`'s
  `Lookup` of the name in `pub` finds the same file id.
  - Each of these for `Create`, exclusive, for `Mkdir`, and for `Symlink` to
    `"x"`, under a name of its own in `pub`:

    | Caller | `sa` | Owner and group |
    |---|---|---|
    | `sa24Other` | `{}` | `502:30` |
    | `sa24Other` | `{GID: 30}` | `502:30` |
    | `sa24Other` | `{GID: 20}` | `502:30` |
    | `sa24Other` | `{GID: 40}` | `502:30` |
    | `cgMulti` | `{GID: 40}` | `504:40` |
    | `cgMulti` | `{GID: 41}` | `504:41` |
    | `cgMulti` | `{GID: 20}` | `504:30` |
    | `sp24Root` | `{GID: 20}` | `0:20` |
    | `sp24Root` | `{GID: 999}` | `0:999` |
    | `sp24Root` | `{}` | `0:0` |

  - With a mode and with a uid:

    | Call | Caller | `sa` | Owner, group and mode |
    |---|---|---|---|
    | `Create` | `sa24Other` | `{Mode: 0o2775, GID: 20}` | `502:30`, `0o2775` |
    | `Mkdir` | `sa24Other` | `{Mode: 0o2775, GID: 20}` | `502:30`, `0o2775` |
    | `Create` | `cgMulti` | `{Mode: 0o2755, GID: 41}` | `504:41`, `0o2755` |
    | `Create` | `sa24Other` | `{UID: 7, GID: 20}` | `502:30`, `0o644` |
    | `Create` | `cgMulti` | `{UID: 7, GID: 41}` | `504:41`, `0o644` |
    | `Create` | `sa24Other` | `{UID: 502, GID: 30}` | `502:30`, `0o644` |
    | `Create` | `sp24Root` | `{UID: 7, GID: 8}` | `7:8`, `0o644` |

    The first is the issue's case: the file with the set-group-ID bit is in
    its creator's group, not in group 20. The fifth shows that each
    attribute goes by its own verdict.
  - A name that exists. `sa24Other`'s `Create(pub, "x", {GID: 30}, false)`
    and `sp24Root`'s `Create(pub, "x", {UID: 7, GID: 999}, false)` each
    return nil with `x`'s file id, still `501:20`, and `GetAttr` of `x`
    agrees. `sa24Other`'s `Create(pub, "x", {GID: 30}, true)`,
    `Mkdir(pub, "x", {GID: 30})` and `Symlink(pub, "x", "x", {GID: 30})`
    each return `vfs.ErrExist`, compared with `==`, and `x` is still
    `501:20`.
  - A directory the caller may not write. `sa24Other`'s
    `Create(root, "n", {GID: 30}, true)` returns `vfs.ErrAcces`, and
    `Lookup(root, "n")` is `vfs.ErrNoEnt`.
  - What is committed. A `Sync` returns nil, and on `e.remount` a `Lookup`
    in `pub` of the names made by the third and sixth rows of the first
    table for `Create`, by its eighth for `Mkdir`, and by the first row of
    the second table reports the same owner, group and mode.
- **G3. The requests, and what each answer does**
  (`TestARequestedGroupAtCreationGoesThroughOpChgrpAndARefusalIsIgnored`).
  `sp24Mount` with a policy that, for each request, first notes `FS.changes`
  as it finds it and then calls an `sp24Recorder`'s `check`; `pwW2Tree`,
  whose `d` has the `Object` `D`, `pwW2D`. The note is switched on once
  `pwW2Tree` has returned: the setup's requests are recorded and not noted,
  since nothing is asserted of them. Every call is on `d`, as ADR 0014
  §11's safety rule asks. For each row the recorder is reset to refuse the
  index given, −1 for none; `FS.changes` is read, c; the call is made, by
  `testCaller` unless the row says otherwise; and:
  - the recorder holds exactly the row's requests, in order, each with the
    row's caller as `Caller`, its `GIDs` included, the `Object` given, `UID`
    as given for an `OpChown` and 0 otherwise, `GID` as given for an
    `OpChgrp` and 0 otherwise, `Dst` the zero `Object`, `Entry` and
    `Replaced` nil, and `Advisory` false;
  - at every one of those requests the policy found `FS.changes` equal to c:
    the call had changed nothing when it asked (§3);
  - after a call that made an object, `FS.changes` is above c, and the
    attributes returned and `GetAttr` report the owner and group given;
    after a call that made none, `FS.changes` is c. A call that made none
    is one that returned an error, or the twelfth row's, which returns nil
    with the object its name already names; where the name names an object,
    that object is as it was.

  `N` is `{vfs.TypeReg, 0o644, 501, 20}`.

  | Call | Refusing | Returns | Owner and group | Requests |
  |---|---|---|---|---|
  | `Create(d, "g1", {GID: 8}, true)` | −1 | nil | `501:8` | `OpAddEntry` on `D`; `OpChgrp` on `N` with `GID` 8 |
  | `Create(d, "g2", {GID: 8}, true)` | 1 | nil | `501:20` | the same two |
  | `Mkdir(d, "g3", {GID: 8})` | −1 | nil | `501:8` | `OpAddEntry` on `D`; `OpChgrp` on `{vfs.TypeDir, 0o755, 501, 20}` with `GID` 8 |
  | `Symlink(d, "g4", "f", {GID: 8})` | −1 | nil | `501:8` | `OpAddEntry` on `D`; `OpChgrp` on `{vfs.TypeLnk, 0o777, 501, 20}` with `GID` 8 |
  | `Create(d, "g5", {Mode: 0o2750, GID: 8}, true)` | −1 | nil | `501:8`, mode `0o2750` | `OpAddEntry` on `D`; `OpChgrp` on `{vfs.TypeReg, 0o2750, 501, 20}` with `GID` 8 |
  | `Create(d, "g6", {UID: 7, GID: 8}, true)` | −1 | nil | `7:8` | `OpAddEntry` on `D`; `OpChown` on `N` with `UID` 7; `OpChgrp` on `N` with `GID` 8 |
  | `Create(d, "g7", {UID: 7, GID: 8}, true)` | 1 | nil | `501:8` | the same three |
  | `Create(d, "g8", {UID: 7, GID: 8}, true)` | 2 | nil | `7:20` | the same three |
  | `Create(d, "g9", {UID: 7, GID: 8}, true)` | 0 | `vfs.ErrNXIO` | none: `Lookup(d, "g9")` is `vfs.ErrNoEnt` | `OpAddEntry` on `D` |
  | `sp24Root`'s `Create(d, "g10", {GID: 8}, true)` | −1 | nil | `0:8` | `OpAddEntry` on `D`; `OpChgrp` on `{vfs.TypeReg, 0o644, 0, 0}` with `GID` 8 |
  | `cgMulti`'s `Create(d, "g11", {GID: 8}, true)` | −1 | nil | `504:8` | `OpAddEntry` on `D`; `OpChgrp` on `{vfs.TypeReg, 0o644, 504, 30}` with `GID` 8 |
  | `Create(d, "g1", {UID: 7, GID: 9}, false)` | −1 | nil, `g1`'s file id | still `501:8` | `OpAddEntry` on `D` |
  | `Create(d, "g1", {GID: 9}, true)` | −1 | `vfs.ErrExist` | still `501:8` | `OpAddEntry` on `D` |
  | `Mkdir(d, "g3", {GID: 9})` | −1 | `vfs.ErrExist` | still `501:8` | `OpAddEntry` on `D` |
  | `Symlink(d, "g4", "f", {GID: 9})` | −1 | `vfs.ErrExist` | still `501:8` | `OpAddEntry` on `D` |

  The first two rows show that the decision is the policy's: `testCaller` is
  not in group 8, and `Unix()` would have refused it. The sixth to eighth
  show that the `OpChgrp`'s `Object` has uid 501 whatever becomes of the uid
  asked for, and that the `OpChgrp` is asked after a refused `OpChown`. The
  twelfth shows that a call that makes nothing changes nothing.

**`internal/nfs`** (package `nfs`), in one new file,
`internal/nfs/create_group_test.go`, with helpers of its own prefixed `cg`.
It drives the server over a TCP socket with `startServer`, as
`sticky_test.go` does, and may reuse `dial`, `mountRoot`, `rpcClient`,
`skipWccData` and `skipPostOpAttr`; `stkMkdir` and `stkClient`; the `nmfs`
helpers; and, of unexported non-test names, the procedure numbers
`procGetAttr`, `procCreate`, `procMkdir` and `procSymlink`, `createUnchecked`
and `createGuarded`, and `dontChange`. Of `rpcClient`, which
`integration_test.go` declares, it may use the fields `t`, `conn`, `xid`,
`uid` and `gid` and the methods `call` and `readRecord`, which a helper
that frames a call as `call` does needs. And it may name `ProgramNFS` and
`VersionNFS`, the exported constants of `internal/nfs` that the package's
wire tests pass to `call`.

The encodings, as `internal/nfs` reads and writes them at `344a95b`:

- CREATE's arguments are the directory's handle, an opaque; the name, a
  string; the mode, a uint32, 0 for UNCHECKED and 1 for GUARDED; and a
  `sattr3`. MKDIR's are the handle, the name and a `sattr3`. SYMLINK's are
  the handle, the name, a `sattr3` and the target, a string.
- A `sattr3` is `set_mode3`, a bool and then a uint32 if true; `set_uid3`
  and `set_gid3`, the same; `set_size3`, a bool and then a uint64 if true;
  and two times, each a uint32 that is `dontChange`, 0, when none is set.
- Each of the three replies is the status; then for `NFS3_OK` a
  `post_op_fh3`, a bool and then an opaque; a `post_op_attr`, a bool and
  then a `fattr3`; and one `wcc_data`; and for any other status one
  `wcc_data`. Nothing follows. GETATTR's argument is the handle, and its
  reply the status and, for `NFS3_OK`, a `fattr3`.
- A `fattr3` is the type, the mode, the link count, the uid and the gid, a
  uint32 each; the size and the space used, a uint64 each; two uint32 of
  `rdev`; the fsid and the fileid, a uint64 each; and three times of two
  uint32 each. The types are 1 for a regular file, 2 for a directory and 5
  for a symlink.
- An AUTH_SYS credential's body is a stamp, a uint32; the machine's name, a
  string; the uid and the gid, a uint32 each; and the gids, a count and
  that many uint32. `rpcClient.call` sends one gid in the list, the
  client's own, so a client that sends another list needs a helper of this
  file's own, which frames a call as `call` does.

- **G4. Over the wire**
  (`TestNFSAGroupAskedForAtCreationIsAppliedOnlyForRootOrAMember`). The
  owner, `501:20`, makes `pub` in the root with a MKDIR of mode `0o777`.
  Then, each answering `NFS3_OK` with a handle and with attributes present
  in the reply, which report the type, the mode, the uid and the gid given,
  as a GETATTR of the handle then does too, and each reply ending where its
  form ends:
  - from a client sending `502:30`: a GUARDED CREATE of `pub/a` with mode
    `0o2775` and gid 20, `502:30` and `0o2775`; a MKDIR of `pub/da` with
    mode `0o755` and gid 20, `502:30`; a SYMLINK of `pub/la` to `a` with gid
    20, `502:30`; and an UNCHECKED CREATE of `pub/b` with mode `0o644` and
    gid 30, `502:30`;
  - from a client sending uid 504, gid 30 and the gids 40 and 41: a GUARDED
    CREATE of `pub/m` with mode `0o644` and gid 41, `504:41`; and one of
    `pub/m2` with mode `0o644` and gid 20, `504:30`;
  - from a client sending `0:0`: a GUARDED CREATE of `pub/r` with mode
    `0o644` and gid 20, `0:20`;
  - from the client sending `502:30`: an UNCHECKED CREATE of `pub/r`, which
    exists, with gid 30, whose reply and whose GETATTR still report `0:20`.

  Each `sattr3` sets the gid given, the mode where one is given, and nothing
  else. Where no mode is given, `set_mode3` is FALSE, and the mode reported
  is the one the object has without it: `0o777` for `pub/la`, by the facts
  above, and for the second `pub/r` the `0o644` it was made with. A client
  given as a uid and a gid alone sends that gid as its one gid, as
  `rpcClient.call` does.

**Existing tests.** No assertion of an existing test changes. Two comments
of `internal/blobfs/policy_wiring_test.go` do: its header lists "how sa.GID
at creation is handled (#98)" among what is not covered, and the doc comment
of W2 for the requested uid ends "none sets a GID (#98)". Each is to say
that the group is pinned since this ADR and tested in
`create_group_test.go`. These were read against §2 and §3 and stand as they
are: W2 for the requested uid, whose recorded `OpChown` has the `Object` it
had; W2's creation rows and W3 and W4's, none of which asks for a group; and
every test of `internal/nfs`, none of which sets `set_gid3` at a creation.

**Red run.** On `344a95b`'s tree, with the three new files and no other
file of this PR, read from the code and not run:

- G1 passes, being a characterisation.
- G2 fails on every row whose caller is not in the group it asks for, the
  third, fourth and seventh rows of its first table and the first, second
  and fourth of its second, each of which reports the group asked for
  there; and on the remount's `Lookup`s of those. Its other rows, its names
  that exist and its `vfs.ErrAcces` pass.
- G3 fails on every row that asks for a group and makes an object: no
  `OpChgrp` is recorded there, so a row that refuses its `OpChgrp` reports
  group 8; and on every row that asks for a uid and makes an object, since
  the `OpChown` is asked there after `allocInode` has marked the namespace,
  and so finds `FS.changes` above c. Its row that refuses the `OpAddEntry`
  and its rows for a name that exists pass.
- G4 fails on the three creations by `502:30` with gid 20 and on `pub/m2`,
  each of which reports gid 20 there, and passes the rest.

After coder's change every test passes.

**The red run was taken**, and was as read. By the session's report,
test-author's run and the session's own, each on `344a95b`'s tree with the
three new files, gave the same:

- G1 passes.
- G2 fails on the third, fourth and seventh rows of its first table, for
  each of the three calls; on the first, second and fourth rows of its
  second; and on the remount's `Lookup`s of `c3` and `s1`, the two of its
  four names whose caller is outside the group asked for.
- G3 fails on its first to eighth, tenth and eleventh rows, the second
  reporting group 8, and the sixth to eighth failing on the note of
  `FS.changes` as well. Its ninth and its twelfth to fifteenth pass.
- G4 fails on `pub/a`, `pub/da`, `pub/la` and `pub/m2`.

And by the session's report of its merge bar on `de0a151`, with the four
test files this PR adds or changes in place, every test passes.

**What test-author's commit settled**, recorded against `32d4cee`, read on
2026-11-24 (Assumption 20). Its report raised four points where the text
above was at odds with itself or left room, and named what its wire test
uses beyond the names given. Each is decided in the text it bears on, and
the committed tests match every one, so no test changes:

1. *G3's rule for `FS.changes`, against its twelfth row.* The bullet was
   wrong, and is corrected. What raises `FS.changes` is making an object,
   not returning nil: the twelfth row returns nil and makes none, so it
   leaves `FS.changes` at c, as the facts and the sentence under the table
   already said, and as committed.
2. *`pub/m2`'s mode.* `0o644`, as `pub/m`'s, as committed.
3. *A call G4 gives no mode.* It sends none, as committed: `pub/la`
   reports `0o777`, and the second `pub/r` the `0o644` it has.
4. *When G3's note is switched on.* Once the setup has returned, as
   committed. The facts above would let it note the setup's requests too,
   and G3 does not use that.
5. *The names.* `ProgramNFS`, `VersionNFS`, and `rpcClient`'s fields and
   its `readRecord`, which the file's own framing helper needs, are now in
   the list above. Each of G4's three clients sends its credential through
   that helper, the two that send one gid with the list `rpcClient.call`
   would have sent, which G4's text now says.

**What only review can show:**

- that `internal/blobfs/fs.go` is the only non-test file changed, and in it
  only `createEntry`, `allocInode` if the ids are passed to it, and their
  comments;
- that no path gives a new object a uid or gid from `sa` without the
  policy's nil for it;
- that both requests are made before `allocInode`, which G3 samples through
  `FS.changes`, in the hold of `FS.mu` for writing that makes the object,
  and that `N` is built from the values the inode is then made with, its
  mode by the mask `allocInode` applies, which coder's commit writes once
  for each (§3);
- that a refusal of either is neither returned nor logged;
- that `createEntry` still performs `Mutate` holding the lock its
  `trace.tsv` row gives (§9);
- how a kernel client behaves, which needs a mount and a human
  (Assumption 16).

### 9. The model, the trace and the vocabulary

No TLA+ module is affected, and none is added (Assumption 15). ADR 0013 §1
gives permission rules to exhaustive tests of the policy and to security
review, and G1 is that test. `docs/tla/CommitProtocol.tla` models no
permission: its `Mutate` is guarded by `Mutable`, the mount gate, which
stays `createEntry`'s first statement, and a request the policy refuses is
no step of it.

`docs/tla/trace.tsv` names `FS.createEntry`, as `Mutate` holding `FS.mu` for
writing, with `-` under `decided`, and coder's commit edits that function.
That is ADR 0013 §8's refactor of traced code. Nothing under `docs/tla/`
changes with this ADR's first commit, which has no commit of coder's to
read. The pass after coder's commit, which the architect makes on the same
branch, records here what it read and what coder chose where §3 left room,
and outside this ADR does this and no more:

- It reads the row against coder's commit: that `createEntry` is still
  declared on `*FS` in `internal/blobfs/fs.go`, keeps its
  `TLA+: docs/tla/CommitProtocol.tla Mutate` line, calls `mutable()` before
  its lock, takes `FS.mu` for writing with a deferred unlock, and makes its
  change and its `markDirty` in that hold, after every request.
- **The row** for `FS.createEntry`, the one row of `trace.tsv` this PR
  touches, changes in `decided` alone, from `-` to `ADR 0028 section 3`.
  §3 is from this ADR what decides how the function performs the action:
  whole, in one hold, after every verdict. At `344a95b` a request was asked
  between the two halves of the change. Its `locks` column stands. No row
  is added or removed: `allocInode` has no row, and `FS.markDirty`'s is not
  touched.
- **`Checked against`.** The module's line, `docs/tla/README.md`'s
  statement of it and `docs/vocabulary.md` V3's move from `afbda93` to
  coder's commit. The module changes in that one line of its header comment
  and gains no `Revised:` line, as it gained none for the passes of
  ADR 0024, ADR 0014's wiring and ADR 0026. No configuration, no row of
  `docs/tla/manifest.tsv` and no property changes.
- **`tla.sh full` is due.** `trace.tsv` and the module's header are under
  `docs/tla/`, so CLAUDE.md's merge bar asks the session for
  `bash scripts/tla.sh full`, unpiped, with `git status --porcelain` the
  same before and after and a last line that reads
  `tla.sh: N of N passed`, N being the rows of `docs/tla/manifest.tsv`. The
  module's actions do not change, so TLC's outcome cannot differ from the
  run that calibrated those rows.

**The pass after coder's commit** was made on 2026-11-24 against `de0a151`,
read and not run (Assumption 19). The row holds:

- `FS.createEntry` is declared on `*FS` in `internal/blobfs/fs.go` and keeps
  its `TLA+: docs/tla/CommitProtocol.tla Mutate` line. It calls `mutable()`,
  then `validName` and the check of a regular file's size, before its lock.
  It takes `FS.mu` for writing by `Lock` with a deferred `Unlock`, and in
  that hold resolves the directory, asks the `OpAddEntry`, answers a name
  the directory has, chooses the mode, and asks the `OpChown` and the
  `OpChgrp`. Only then does it make its change: `allocInode`, a symlink's
  target and size, the entry, the directory's link count for a new
  directory, the directory's `touchM`, and `markDirty`. Every return before
  `allocInode` leaves the namespace as it was.
- So the row's `locks` stands, and its `decided` moved from `-` to
  `ADR 0028 section 3`, which is the one change to `trace.tsv`.
- `allocInode`'s new signature bears on no row. It has none and gains
  none: it is a step of `createEntry`'s change, called from nowhere else,
  in `createEntry`'s hold, as it was. `FS.markDirty`'s row stands: the
  function is untouched, and each of its eight call sites, `allocInode`'s
  among them, is in a hold of `FS.mu` for writing.
- By the session's `git diff --stat 344a95b..de0a151`, over `internal` less
  its test files, coder's commit changes `internal/blobfs/fs.go` alone, by
  28 insertions and 11 deletions in three hunks. Read against the file,
  the three are `allocInode` with its doc comment; `createEntry`'s doc
  comment; and `createEntry` from the mode it chose to its call of
  `allocInode`. The other eleven functions of `fs.go` that the trace
  names, `New`, `FS.mutable`, `FS.SetAttr`, `FS.unlink`, `FS.Rename`,
  `FS.bufferWrite`, `FS.truncate`, `FS.flushAll`, `FS.settleAll`,
  `FS.flushOpen` and `FS.putChunk`, were each read whole at `de0a151`.
  Each lies outside the three hunks, keeps its `TLA+:` line, and takes in
  its own body the locks its rows give it; a lock a row gives as held by a
  caller in `commit.go` was not read there (Assumption 19).
- Between `afbda93` and `de0a151` the branch's history holds one other
  change to `internal`, ADR 0027's, whose own pass found that it edits no
  function the trace names (ADR 0027 §11). That nothing else lies between
  them rests on that history as the session gave it (Assumption 19).

The module's `Checked against` line, `docs/tla/README.md`'s statement of it
and `docs/vocabulary.md` V3's moved from `afbda93` to `de0a151`. The module
changed in that one line of its header comment and nowhere else, and gained
no `Revised:` line. So TLC's outcome cannot differ from the run that
calibrated the forty-five rows, and CLAUDE.md's merge bar asks the session
for `tla.sh full` on the PR, as above. The manifest has 45 rows, so the
run's last line must read `tla.sh: 45 of 45 passed`.

`docs/vocabulary.md` does not otherwise change. V7 lists `OpChgrp`,
`OpChown` and `InGroup` already, and `UnixAllows(r)` is still
`policy.Unix().Check(r) == nil`, with the same answers.

### 10. State, the link and exemptions

- **No machine, and no transition.** An object's group is an attribute, a
  value a caller sets whole, and ADR 0018 §2 keeps values outside the state
  rule. Nothing else is added: no field, no flag, no cache.
- **The policy rule is kept, and one inline apply goes.** Whether a caller
  may give its new object a group was decided by no rule at all; it is now
  `Unix()`'s `OpChgrp` row. `blobfs` adds no condition on who the caller
  is. What it decides for itself is what the operation is: which group an
  object has when none is asked for, and that a creation goes on without an
  attribute its caller may not give (ADR 0018 §2).
- **No link.** No machine's transition depends on either request, so
  ADR 0018 §5 asks nothing here.
- **The register**, `docs/adr/exemptions.md`, does not change. No exemption
  is granted, restated or revoked.

### 11. What stays out of this PR

Each of these is its own PR. Titles are as ADR 0014's *Context* and ADR 0025
give them (Assumption 2).

- **#155**, "A WRITE or a size change leaves a file's set-user-ID and
  set-group-ID bits set", with what ADR 0025 put to it about a change of
  mode (§6). `SetAttr`, `Write` and `truncate` are untouched.
- **#69**, **#99**, **#100** and **#101**. No file row, no `OpSetTimes`, no
  `SETATTR` handler and no `Write` changes.
- **#6**, **#90** and **#95**, the AUTH_SYS trust boundary and root squash
  (§7).
- **#154**, whose title and text this ADR was not given. Nothing here
  touches `Write`, `truncate`, `SetAttr` or a row of `Unix()`.

### 12. Cross-references, and `CHANGES`

This ADR replaces two parts of ADR 0014, under `docs/adr/README.md`'s rule
for a part a later ADR replaces: §6's rule that a requested gid at creation
is applied with no check; and the place §6's *The wiring at `a6cff50`* and
Assumption 46 give the creation-time `OpChown`. §11's statement that no test
relies on how `sa.GID` at creation is handled becomes the record only.
ADR 0014 gains a `Revised` line, cross-references only, and one sentence in
each of §6, §11 and *Assumptions*. Its text stays as the record, and every
test it pins stands.

ADR 0010's bullet on 4294967295, under *What this does not decide*, says
that CREATE, MKDIR and SYMLINK "apply a requested gid from any caller". It
gains a sentence and the ADR a `Revised` line, cross-references only, as it
gained one when ADR 0011 decided #68.

ADR 0015, ADR 0018 and ADRs 0024 to 0027 are not revised (Assumption 18).
ADR 0015's *Context* describes the code at `60da89f`, and the chain it draws
still holds without the second half of its link 3, a gid from any caller.
The others list #98 among what they leave, which stays true of them. `docs/DESIGN.md`'s table of ADRs
gains this ADR's row, and `docs/adr/README.md`'s index does.

One `CHANGES` line, proposed for the session to record, without `BREAKING:`
(Assumption 17; *Put to the owner, and decided*, 4). By the session's
report the owner decided on 2026-11-24 that it takes none (*Context*,
decision 4). The pass of that day read the line against `de0a151` and
changed nothing in it: what it says of the new behaviour is what
`createEntry` does at that commit. At that commit the session had not yet
recorded it:

> CREATE, MKDIR and SYMLINK apply a group asked for in their attributes
> only when the caller is root or a member of that group, the rule SETATTR
> already follows; a group asked for by anyone else is left unapplied and
> the object is made in the caller's own group, still with NFS3_OK, where
> before any caller with write and search permission on the directory
> could make an object in any group.

## Put to the owner, and decided

This section was headed *For the owner* until the pass of 2026-11-24. It
listed four judgement calls with a trade-off a client can see, each decided
above as the architect's recommendation, for the session to put to the
owner. None is open now. By the session's report the owner decided all four
on 2026-11-24, each as recommended (*Context*, decision 4). Each item is
kept as it was put, with the alternatives that were weighed, and ends with
who decided it. A different answer to the first would have changed §4, G2
to G4 and the `CHANGES` line. A different answer to any of them would have
changed a table here and its pins, not the mechanism: §2's requests and
§3's order stand under every alternative.

1. **A group the caller may not set is left unapplied, and the creation
   succeeds** (§4). *Recommended:* leave it unapplied, as a uid the caller
   may not give already is, and as Linux's server does.
   *Alternatives:*
   - refuse the call with `NFS3ERR_PERM`, making nothing. It is loud, and
     no object is ever made in a group its client did not mean. It fails
     every creation by a user outside the group from a client that sends
     the directory's group to place the object; it answers a status none of
     the three procedures lists; it gives the uid and the gid of one
     `sattr3` two dispositions, unless a uid the caller may not give is
     refused too, which is a second change; and it needs a rule for a name
     that exists, where no group would have been applied.
   - apply only root's, and leave a member's unapplied too, as Linux's
     server does. The issue's done-when asks for root or a member, and
     `SETATTR` already gives a member the group, so this would refuse at
     creation what the next call allows.

   *Decided* by the owner: the group is left unapplied, and the creation
   succeeds.
2. **The default group stays the caller's own** (§5). *Recommended:* no
   change in this PR, and an issue for a directory's set-group-ID bit.
   *Alternatives:*
   - give a new object the directory's group where the directory's mode has
     the set-group-ID bit, and have a new directory inherit the bit, as
     Linux does. It is what POSIX asks an implementation to provide a way
     to, and what a shared group directory needs from a client that asks
     for no group. It changes the group of new objects for every client in
     such directories, puts an object in a group its creator may not be in,
     and so has to decide §6's set-group-ID bit on a new regular file with
     it.
   - always give the directory's group, as BSD does. That changes every
     creation.

   *Decided* by the owner: the default group stays the caller's own.
3. **A mode asked for keeps its set-ID bits at creation** (§6).
   *Recommended:* store the mode as asked, as now, since no creation puts
   an object in a group its creator is outside. *Alternative:* clear
   `S_ISGID` from a new non-directory whose group asked for was left
   unapplied, so that a call that asked for a set-group-ID file in one
   group never makes one in another. It guards a client's mistake, not an
   escalation, departs from Linux's server, and adds a rule about the mode
   to a path that has none.
   *Decided* by the owner: a mode asked for keeps its set-ID bits.
4. **The `CHANGES` line takes no `BREAKING:`** (§12; Assumption 17).
   *Recommended:* none, since no running deployment has to act to keep
   working. *Alternative:* prefix it, on the reading that a deployment
   whose client places objects by sending a group its users are outside
   has to add those users to the group to keep the placement.
   *Decided* by the owner: no prefix.

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation, and Issue #98**, under the owner's
   2026-10-10 words, as ADRs 0015 to 0027 were. The issue's done-when gives
   "root or a member" and leaves "ignored or refused" to be decided. The
   rest is mine: the request (§2), the order (§3), the disposition (§4),
   the default (§5), that no set-ID rule is added (§6), and the `CHANGES`
   judgement. *Put to the owner, and decided* lists the four an owner is
   most likely to weigh differently. Those four were mine until 2026-11-24,
   when by the session's report the owner decided each as this ADR has it
   (*Context*, decision 4).
2. **Read, not run, at `344a95b`.** Every statement about the code was
   checked by reading it there; nothing was run. #98's text is as the
   session gave it, and so is what the audit of #94's fix noted. The titles
   of #69, #99, #100 and #101 are as ADR 0014's *Context* gives them, and
   #155's as ADR 0025 gives it. Their texts were not given, nor were #6's,
   #90's, #95's or #154's.
3. **`OpChgrp`, the request `SetAttr` makes, and no new `Op` or field**
   (§2). The task asks that one rule decide a group change wherever it
   arrives, and one row asked by both calls is the strongest form of that:
   there is no second rule to keep in step. The cost is that a policy
   cannot tell a creation's request from a `SETATTR`'s but by its `Object`.
   A rule that needs to, such as one for a set-group-ID directory, adds a
   field under ADR 0014 §5's *Growth* rule.
4. **Membership is `InGroup`**: the credential's gid and its gids, of which
   the server keeps 16. It is the test `OpChgrp` already makes, with
   AUTH_SYS's limit.
5. **Left unapplied** (§4). The done-when leaves the choice, and §4 gives
   the four reasons. It would be the wrong call if a client that asks for a
   group at creation, and does not read the reply, were common, and none
   was found that does. By the session's report the owner decided on
   2026-11-24 that the group is left unapplied and the creation succeeds
   (*Context*, decision 4).
6. **One `Object` for both requests, the caller's own** (§2). ADR 0014 §6
   gave the `OpChown` the new inode as `allocInode` made it, and said
   nothing of a second request. Giving the `OpChgrp` an object with the uid
   already applied would make its verdict turn on another verdict, for a
   policy that reads the owner; under `Unix()` no answer differs.
7. **Both requests before `allocInode`** (§3). ADR 0014's Assumption 46 put
   the `OpChown` after it, rightly for a refusal that is ignored. Asking
   first costs nothing, changes no recorded request, and keeps ADR 0024
   §1's rule whatever a refusal does. It means `N` is built from the
   caller and the mode and not read back from an inode, which review holds
   to the values the inode is made with.
8. **Asked only of a call that will make an object, after stage E** (§2,
   §3), as ADR 0014 §11 pins for the `OpChown`. A request about an
   attribute that will not be applied would ask the policy about nothing.
   It puts these two requests after stage E, where ADR 0014 §6 has stage D
   before it; no status comes from them, so no order of errors moves.
9. **Any refusal is ignored, whatever its status** (§2), `vfs.ErrServerFault`
   included, as `createEntry` treats the `OpChown`'s at `344a95b`. `Unix()`
   returns only `vfs.ErrPerm` for either request.
10. **The default group is unchanged** (§5). The brief asks whether it
    changes. Keeping it keeps this PR to the issue, and keeps §6's
    argument, which rests on a caller being in its own gid. By the
    session's report the owner decided on 2026-11-24 that the default group
    stays the caller's own (*Context*, decision 4).
11. **No rule for set-ID bits** (§6), for the reason §6 gives. It would be
    the wrong call under a default group that can be one the creator is
    outside. By the session's report the owner decided on 2026-11-24 that a
    mode asked for keeps its set-ID bits (*Context*, decision 4).
12. **No request for the mode at creation** (§2). `OpChmod` on `N` would be
    allowed to every caller by construction, a request with no rule behind
    it. A policy that wants to judge a new object's mode has no request for
    it; none does.
13. **Since 2026-11-25 `vfs.FS`'s doc comment says what a creation does
    with an owner or group its caller may not give, and nothing else in
    `internal/vfs` changes.** Until that day's revision this assumption
    read that nothing in `internal/vfs` changes: that `vfs.FS`'s doc
    comments say nothing of permission rules, which are `internal/policy`'s
    (ADR 0026, Assumption 16), and no signature moves; that a creation's
    leaving unapplied an owner or group its caller may not give is
    `blobfs`'s behaviour, pinned here and in ADR 0014 §6, and is not in
    the contract's words; and that whether it should be is the session's
    to raise. So a backend that replaced `blobfs` would have found the
    disposition in the ADRs, and in `docs/DESIGN.md`'s row for this one,
    and not in the interface it implements. By the session's brief the
    owner decided on 2026-11-25 that the contract says it (*Context*,
    decision 5). The doc comment above `Create` in `internal/vfs/vfs.go`
    gains a second paragraph, after its sentence on `ErrFBig`:

    > Create, Mkdir and Symlink each leave unapplied an owner or group that
    > sa sets and c may not give to the object they make, and do not fail for
    > it. The object has the owner sa sets if c may give it that owner, and
    > otherwise c.UID; it has the group sa sets if c may give it that group,
    > and otherwise c.GID. The call does fail if c may not add name to dir.
    > SetAttr differs: given an owner or group that c may not set, it fails
    > and applies none of sa.

    The file is the contract, and this is its restatement; `c`, `sa`,
    `name` and `dir` are the three methods' parameters. The brief gave what
    the paragraph must carry: that such an owner or group is left
    unapplied, that the object is made with the caller's own uid or gid
    instead, that the call does not fail for it, and where a refusal does
    fail the call, which is the directory's own permission. It asked for
    comments only; for what the contract is and not which policy row
    decides it, the line ADR 0026 kept (its Assumption 16); and for a
    contrast with `SetAttr` only if that helps a reader. The rest is mine:

    - *One comment for the three*, which the brief allows where the file
      groups methods so, and it does: one comment above `Remove` speaks
      for `Remove`, `Rmdir` and `Rename` (ADR 0017). `Mkdir` and `Symlink`
      gain none of their own. The paragraph follows the sentence on
      `ErrFBig`, as ADR 0024's sentence does in `SetAttr`'s comment.
    - *Both halves, and the default.* The paragraph's second sentence is
      §3's *The new object* in the contract's words, with "c may give it"
      where §3 has "asked and allowed". That is more than the brief's
      list. It says that an owner or group the caller may give is applied,
      whatever becomes of the other (§2), and that what stands in for one
      left unapplied is what the object has when none is asked for (§1).
      Without it "left unapplied" would not say what the object is left
      with. So the default is in the contract's words too: the caller's
      own uid and gid, as §3 pins them for `blobfs`. The group's is §5's,
      which the owner decided to keep (*Context*, decision 4), and an ADR
      that changes it (*What this does not decide*) revises the comment.
    - *No rule.* The paragraph does not say who may give which owner or
      group, or which request or row decides it. "c may give" is the
      verdict of whatever rules a backend has, which for `blobfs` are §2's
      requests to `f.perm`. So ADR 0025's Assumption 17, ADR 0026's
      Assumption 16 and ADR 0027's Assumption 20 stand: `vfs.FS`'s doc
      comments still state no permission rule.
    - *The sentence on `dir`* says where a refusal does fail the call. It
      names no status, which is the policy's (§3, stage D). It is true of
      every `Create`, `Mkdir` and `Symlink` as `createEntry` stands, a
      `Create` of a name that exists included, since stage D comes before
      stage E.
    - *The contrast with `SetAttr`* is kept, as one sentence. Both calls
      take a `vfs.SetAttr`, and they treat an owner or group the caller
      may not give in opposite ways (§4), which a backend that built its
      creation on its `SetAttr` would get wrong. The sentence says nothing
      of `SetAttr` that its own comment does not imply: a call that cannot
      apply all of `sa` cannot return nil, and one that returns an error
      has changed nothing (ADR 0024 §2).
    - *What the paragraph leaves out*: the mode, the times and the size
      asked for at creation, and what a `Create` of a name that exists
      does with `sa`. The owner's decision covers none of them, and
      `vfs.FS` says of them what it said.
    - *§10 stands.* It says that `blobfs` decides for itself which group
      an object has when none is asked for, and that a creation goes on
      without an attribute its caller may not give. That is said against
      the policy, which decides neither (ADR 0018 §2). Since this revision
      the contract asks both of every backend.
    - *No test, no `CHANGES` line, no other ADR.* G2 to G4 (§8), ADR 0014
      §11's W2, its W2 for the requested uid and its W4, and ADR 0024 §7's
      S1 pin for `blobfs` what the paragraph says, but for one case: no
      test refuses an owner asked of `Mkdir` or of `Symlink`. `createEntry`
      is one body for the three, so by reading that case holds as
      `Create`'s does. No test is added or changed, the brief asking for
      none, and whether one should be is the session's to raise. A doc
      comment is nothing a client or an operator can observe. Nothing that
      ADR 0014 §6 or ADR 0024 says becomes untrue, so neither gains a
      cross-reference.
    - *`docs/DESIGN.md`'s row says it too.* The first brief of 2026-11-25
      had this revision change `internal/vfs/vfs.go` and this ADR, one
      sentence of ADR 0014 §6 or of ADR 0024 at most, and nothing else. So
      the first pass left the row that section 16 has for this ADR as it
      was, and put it to the session. The row said that the ADR constrains
      how `internal/blobfs` builds a new object, and that "the backend
      replacing it asks the same requests and treats a refusal the same
      way". It did not say, as the rows for ADR 0007, ADR 0017 and
      ADR 0024 say of theirs, that the ADR adds to the `vfs.FS` contract.
      By its second brief of that day the session had the row changed, in
      the words the first pass's report proposed, unaltered. It now says
      that the ADR "adds to the `vfs.FS` contract that a creation leaves
      unapplied an owner or group its caller may not give and does not
      fail for it, which the backend replacing `blobfs` implements too,
      asking the same requests". The words are mine, in the form of
      ADR 0017's and ADR 0024's rows, and keep the old row's clause on the
      requests. That the row changes in this PR is the session's call, and
      nothing here claims the owner's for it. Nothing else in
      `docs/DESIGN.md` changes. Of it section 16 was read whole, and
      nothing else but one line that a search for `internal/vfs` matched.
    - *Read, not run.* `internal/vfs/vfs.go`, `createEntry` and
      `docs/tla/trace.tsv` were read whole in the session's worktree,
      which by its brief is clean at `3c35d70`, `main` just after this
      ADR's PR was merged. That no test refuses an owner asked of `Mkdir`
      or of `Symlink` is from a search of the test files under `internal/`
      for a creation given a uid, and from reading the rows it found.
      Nothing was run, and that the file is still gofmt-clean is by
      reading, for the session's merge bar to show. No source outside this
      repository was fetched for this revision, which says nothing new of
      anything outside it.
14. **The tests** (§8). Three new files, so that a red run exists; `cg` as
    the prefix; G1 as a characterisation of a row that does not change; the
    ids 504, 40 and 41 and the modes, which no requirement gives; and G3's
    reading of `FS.changes` from inside the policy, which is new to this
    repository's tests. It is sound because every method asks under
    `FS.mu`, held for reading at least and by `createEntry` for writing,
    which ADR 0014 §6 pins, so the read is made by a goroutine that holds
    the lock every writer of the two fields needs.
15. **The trace** (§9). `decided` gains this ADR for `FS.createEntry`,
    where ADR 0026 left `FS.unlink`'s and `FS.Rename`'s as they were,
    because here a request moves from inside the change to before it, and
    §3 is what keeps it there. `Checked against` moves and `tla.sh full` is
    due, as for every edit of a traced function since ADR 0024. The pass
    was made on 2026-11-24, against `de0a151` (§9; Assumption 19).
16. **What clients send is read, not run** (*Context*; §7). Linux's client
    at `v6.6` and Apple's at `main`, in the functions named, through a tool
    that summarises. Other clients, other versions and other paths through
    those two were not read, and neither was mounted.
17. **Not breaking** (§12). A deployment keeps working with no action: the
    calls succeed as before. Objects made by a caller outside the group it
    asks for land in another group than before, which a deployment that
    relied on it would see as files its group can no longer reach. As read,
    neither kernel client relies on it in ordinary use. By the session's
    report the owner decided on 2026-11-24 that the line takes no
    `BREAKING:` (*Context*, decision 4), so the judgement above is no
    longer mine alone. The pass of that day read the line against `de0a151`
    and left it as proposed (§12).
18. **Which ADRs are revised** (§12). ADR 0014, two of whose parts are
    replaced, and ADR 0010, one of whose statements about the code stops
    being true. An ADR that only lists #98 among what it leaves is not
    revised, as ADR 0025 was not when ADR 0026 decided #94, which it
    lists.
19. **The pass after the two commits was read, not run, and how far it
    went** (§3, §8, §9). Nothing was run by me, and I cannot run git. At
    `de0a151` I read `internal/blobfs/fs.go` whole, 2155 lines. From it
    come what §3 says coder chose, what §9 says of the row and of the other
    functions the trace names there, and that every request any method
    makes is asked in a hold of `FS.mu`, which §8's facts rest on. That
    coder's commit changes that file alone, by 28 insertions and 11
    deletions in three hunks, is the session's `git diff --stat` and its
    list of the hunks. The length agrees: ADR 0027's Assumption 25 counted
    2138 lines at `729360f`, and 2138 + 28 − 11 is 2155. What each hunk
    holds is my reading of the file against the hunks' line numbers, not a
    diff I saw. The session's account gave the second hunk as
    `allocInode`'s body; by its lines it is `createEntry`'s doc comment,
    and `allocInode`'s two changed fields are inside the first. I did not
    read `internal/blobfs/commit.go` or the three files of `internal/store`
    that the trace names, beyond a search that found their `TLA+:` lines.
    Their rows stand on the passes that last read them (ADR 0023
    Assumption 23; ADR 0024 §5; ADR 0014 Assumption 49; ADR 0026
    Assumption 25) and on the session's stat. `344a95b` and `4ded55e` are
    the merges of ADR 0027's and ADR 0026's PRs. That they hold the code of
    `729360f` and of `afbda93`, and that nothing else lies between
    `afbda93` and `de0a151`, I did not compare. A `git diff afbda93 de0a151`
    of the five files `trace.tsv` names would confirm the whole stretch,
    and is the session's to run if it wants more than this. Because the
    line moves past the change ADR 0027's pass covered, and "for every
    action" rests on that pass too, `docs/tla/README.md`'s and vocabulary
    V3's statements of the line now cite it beside this one. Of the tests I
    read the three `create_group_test.go` files whole; in
    `internal/blobfs/policy_wiring_test.go` its two revised comments,
    `pwW2Tree` and the helpers G3 calls after a row; in
    `internal/blobfs/setattr_policy_test.go` `sp24Recorder` and
    `sp24Mount`; in `internal/nfs/integration_test.go` `rpcClient`, `dial`,
    `call`, `readRecord`, `startServer`, `mountRoot` and the skip helpers;
    and in `internal/nfs/sticky_test.go` `stkMkdir` and `stkClient`. I also
    read `InGroup` and the `OpChown` and `OpChgrp` rows of
    `internal/policy/policy.go`, which are as *Context* has them, and
    `internal/blobfs/tla_trace_test.go` whole: the tripwire asks of a row's
    `decided` only that it is not empty. That the suite passes, and what
    the red run gave, are the session's reports.
    That `tla.sh full` must end `tla.sh: 45 of 45 passed` is from
    `docs/tla/manifest.tsv`, which has 45 rows, none of them uncalibrated,
    and from `run_suite` in `scripts/tla.sh`. By the session's brief the
    pass also recorded the owner's decisions (*Context*, decision 4),
    settled the points test-author's report raised (§8; Assumption 20), and
    read the `CHANGES` line again (§12).
20. **The points of the tests** (§8). Each was open in the text, or wrong
    in it, and each is settled as the committed test has it. G3's bullet
    was my error: it said "returned nil" where it meant "made an object",
    against the twelfth row, the facts and the sentence under the table,
    and test-author pinned the row by those. `pub/m2` takes `pub/m`'s mode
    because "one of" stood for the same call with another name and group,
    and the attributes expected are the same with a mode of `0o644` or with
    none. A call that G4 gives no mode sends none, because a `sattr3` that
    set what the text does not name would test something the text does not
    say. G3's note is switched on after the setup because nothing is
    asserted of the setup's requests: noting them would add the holds of
    `Write` and of the other calls the setup makes to what the test rests
    on, and a race reported from there would hide what G3 is for. While the
    note is on, the policy is asked by the rows' own calls, in
    `createEntry`'s hold for writing, and by the `Lookup` that checks the
    ninth row, in a hold for reading. The fact that every method asks
    holding `FS.mu` stands as written, and is true of every method at
    `de0a151`, as read (Assumption 19); G3 uses less of it than it allows.
    The names test-author found missing are ones a helper that frames a
    call as `call` does cannot do without. The text asked for that helper
    and did not list them. The fields and `readRecord` are in a test file,
    and `ProgramNFS` and `VersionNFS` are exported. That a client given as
    a uid and a gid sends that gid as its one gid is what `rpcClient.call`
    does, and is now said, since the rule turns on the list.

## Alternatives considered

- **Refusing the call**, with `NFS3ERR_PERM` or `NFS3ERR_ACCES` (§4; *Put
  to the owner, and decided*, 1).
- **Applying only root's**, as Linux's server does (*Put to the owner, and
  decided*, 1).
- **An inline condition in `createEntry`**,
  `c.UID == 0 || policy.InGroup(c, *sa.GID)`. It is the rule, stated a
  second time, in the place ADR 0018 §1 says a rule does not go. A policy a
  later ADR installs would not see the request, and a change to `OpChgrp`'s
  row would leave creation behind.
- **A new `Op` for a group at creation.** It would need its own row, which
  would be `OpChgrp`'s with its first line removed, and the two could drift
  (Assumption 3).
- **Dropping `set_gid3` in `internal/nfs`.** The handler is not the
  filesystem: another front end, or a test that calls `Create`, would meet
  the unchecked apply still there.
- **Asking the `OpChgrp` of the inode once the uid is applied**
  (Assumption 6).
- **Asking after `allocInode`**, where the `OpChown` was (Assumption 7).
- **The directory's group as the default**, by its set-group-ID bit or
  always (§5; *Put to the owner, and decided*, 2).
- **Clearing `S_ISGID` where the group was left unapplied** (§6; *Put to
  the owner, and decided*, 3).
- **All of `sa` or none at creation**, as ADR 0024 §2 has `SetAttr`. It is
  the refusal above under another name: a creation that applied none of
  `sa` would make an object with none of the attributes asked for, the
  mode included, and one that made nothing has refused.
- **An `OpChmod` for the mode at creation** (Assumption 12).

## Consequences

- A group asked for at creation is decided by the row that decides a
  `SETATTR`'s, and no caller but root gets, from a creation, an object in a
  group it is not in.
- Every request a creation makes is asked before it changes anything, so
  `createEntry` has one shape: the verdicts, then the change.
- A caller outside the group it asks for is not told by the status. It
  reads the reply's attributes, or holds an object in its own group without
  knowing.
- Asking for the directory's group no longer places an object there for a
  user outside that group, and this server still gives a directory's
  set-group-ID bit no effect. Until that is decided, a shared directory
  takes its group only from clients whose users are in it.
- A creation and a `SETATTR` answer the same refused group differently: the
  first goes on without it, and the second fails whole.
- Hard to change later: what a refusal does at creation, once clients are
  written against it; and that both requests carry the object as made with
  neither applied, once a policy reads it.

## What this does not decide

- **Whether a new object takes its directory's group** where the
  directory's mode has the set-group-ID bit, whether a new directory then
  inherits the bit, and what becomes of `S_ISGID` on a new regular file
  whose creator is outside that group (§5, §6). An issue is proposed for
  it.
- **Whether the server refuses the id 4294967295**, which ADR 0010 left
  open. Since this ADR a creation gives an object that gid only for a
  caller that sends uid 0 or claims the gid, in its credential's gid or its
  list. A caller that claims it as its own gid gets it as the default group
  of everything it creates, with no group asked for, and one that claims
  the uid gets the uid. An issue is proposed for it.
- **Times asked for at creation**, and a size on a GUARDED create.
  `createEntry` applies neither, for any caller, and `internal/nfs` applies
  a size only after an UNCHECKED create. RFC 1813 calls the `sattr3` the
  object's initial attributes. An issue is proposed for it.
- **A set-ID bit set by `SETATTR`** on a file whose owner is outside its
  group, which ADR 0025 put to #155.
- **What `vfs.FS` says of an owner or group a creation's caller may not
  give** (Assumption 13). Decided since: by the session's brief the owner
  decided on 2026-11-25 that `vfs.FS` says it (*Context*, decision 5), and
  the doc comment above `Create` does, for `Create`, `Mkdir` and
  `Symlink`. What `vfs.FS`'s doc comments say of permission rules is as
  ADR 0026 left it (its Assumption 16): they state none.
- **Root squash and the AUTH_SYS trust boundary** (#6, #90, #95), and
  whether a member's group at creation should narrow under them.
- **Whether a `CREATE` of a name that exists should be judged as the open
  it stands for**, which ADR 0025 left open. It still applies none of
  `sa` but the size.
- **How a kernel client behaves** when the group it sent is not used
  (Assumption 16).

## Sources

Fetched on 2026-11-24, while this ADR was written, through a tool that
summarises what it fetches and quotes at most about 120 characters at once,
so a longer passage was asked for as consecutive pieces, which are joined
here with a space. It reads a long page 100,000 characters at a time. Each
is evidence of how something outside this repository behaves, and none was
taken as direction.

- **RFC 1813**, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>, 229,793 characters, of
  which characters 0 to 200,000 were read and the last 29,793 were not.
  §2.5's paragraph on `sattr3` and the unions `set_uid3` and `set_gid3`;
  §2.6's `NFS3ERR_PERM`; §3's sentence on the ERRORS lists; §3.3.8's
  sentences on UNCHECKED and on EXCLUSIVE, the second of which the tool cut
  at "since the server may use", and its ERRORS list; §3.3.9's `attributes`
  and ERRORS list; §3.3.10's `symlink_attributes` and ERRORS list. That the
  three lists hold the same twelve codes, without `NFS3ERR_PERM`, is the
  tool's listing of each. That the RFC says nothing of a new object's owner
  or group, of a uid or gid asked for at creation, or of set-group-ID
  directories is the tool's answer to that question over the same 200,000
  characters, not a reading of every line. (*Context*; §4)
- **POSIX.1-2024, `open()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/open.html>,
  which gives itself as The Open Group Base Specifications Issue 8, IEEE
  Std 1003.1-2024. Under `O_CREAT`: "the user ID of the file shall be set
  to the effective user ID of the process;" and the sentences on the group
  and on the mode quoted in *Context*. From its RATIONALE: "FIPS 151-2
  required that implementations provide a way to have the group ID be set
  to the group ID of the containing directory, but did not prohibit
  implementations also supporting a way to set the group ID to the
  effective group ID of the creating process.", and the sentence quoted in
  *Context*. (*Context*; §5)
- **POSIX.1-2024, `mkdir()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/mkdir.html>,
  the same edition. "The directory's group ID shall be set to the group ID
  of the parent directory or to the effective group ID of the process.
  Implementations shall provide a way to initialize the directory's group
  ID to the group ID of the parent directory.", and the sentence on the
  mode quoted in *Context*. (*Context*; §5)
- **POSIX.1-2024, `symlink()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/symlink.html>,
  the same edition. "The symbolic link's group ID shall be set to the group
  ID of the parent directory or to the effective group ID of the process.",
  and the sentence on the mode quoted in *Context*. (*Context*)
- **Linux `fs/nfsd/vfs.c`**, at the tag `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/nfsd/vfs.c>.
  `nfsd_create_setattr`'s two comments and its two statements on
  `iap->ia_valid`, as quoted in *Context*. That `nfsd_create_locked` and
  `nfsd_symlink` call it is from a second reading, of
  <https://gitlab.com/linux-kernel/stable/-/raw/v6.6/fs/nfsd/vfs.c>, a
  mirror, which gave the same comments and statements. **Honesty note:** the
  first address answered `429 Too Many Requests` twice before it answered;
  <https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/plain/fs/nfsd/vfs.c?h=v6.6>
  answered `403 Forbidden`; and GitHub's rendering of the file showed only
  its first 1000 lines. That `nfsd_create`, which MKDIR calls, reaches
  `nfsd_create_locked` is by the names and was not quoted. (*Context*; §4)
- **Linux `fs/nfsd/nfs3proc.c`**, at `v6.6`, from the mirror alone:
  <https://gitlab.com/linux-kernel/stable/-/raw/v6.6/fs/nfsd/nfs3proc.c>.
  `nfsd3_create_file` has
  `status = nfsd_create_setattr(rqstp, fhp, resfhp, &attrs);`, and for an
  UNCHECKED create of a file that exists `iap->ia_valid &= ATTR_SIZE;`;
  `nfsd3_proc_mkdir` calls `nfsd_create`, and `nfsd3_proc_symlink`
  `nfsd_symlink`. (*Context*)
- **Linux `fs/inode.c`**, at `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/inode.c>, and
  the mirror's copy, which agreed. `inode_init_owner` has
  `if (dir && dir->i_mode & S_ISGID) {`, `inode->i_gid = dir->i_gid;`,
  `/* Directories are special, and always inherit S_ISGID */` and, in the
  other branch, `inode_fsgid_set(inode, idmap);`. `mode_strip_sgid` returns
  the mode unchanged unless
  `(mode & (S_ISGID | S_IXGRP)) == (S_ISGID | S_IXGRP)`, for a directory,
  where the parent lacks `S_ISGID`, and where
  `in_group_or_capable(idmap, dir, i_gid_into_vfsgid(idmap, dir))`, and
  otherwise returns `mode & ~S_ISGID`. `in_group_or_capable`'s capability
  is `CAP_FSETID`, from the mirror's copy alone. (*Context*; §6)
- **Linux commit `0fa3ecd87848`**, "Fix up non-directory creation in SGID
  directories":
  <https://github.com/torvalds/linux/commit/0fa3ecd87848c9c93c2c828ef4c3a8ca36ce46c7>.
  Its message, as quoted in *Context*. **Honesty note:** by my recall this
  is the fix for CVE-2018-13405; NVD's page for that number returned only
  its title to the tool, so the number is not verified here and nothing
  rests on it. (*Context*; §6)
- **Linux `fs/nfs/dir.c`**, at `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/nfs/dir.c>, and
  the mirror's copy, which agreed. `nfs_create` has `attr.ia_mode = mode;`
  and `attr.ia_valid = ATTR_MODE;`; `nfs_mkdir` has
  `attr.ia_valid = ATTR_MODE;` and `attr.ia_mode = mode | S_IFDIR;`;
  `nfs_symlink` has `attr.ia_mode = S_IFLNK | S_IRWXUGO;` and
  `attr.ia_valid = ATTR_MODE;`. The tool found no `ATTR_UID`, `ATTR_GID`,
  `ia_uid` or `ia_gid` in the three. (*Context*; §7)
- **Linux `fs/nfs/nfs3xdr.c`**, at `v6.6`, from the mirror alone:
  <https://gitlab.com/linux-kernel/stable/-/raw/v6.6/fs/nfs/nfs3xdr.c>.
  `encode_sattr3` sends a uid under `if (attr->ia_valid & ATTR_UID) {` and a
  gid under `if (attr->ia_valid & ATTR_GID) {`, and `*p++ = xdr_zero;`
  otherwise. (*Context*)
- **Apple's NFS kext, `nfs_vnops.c`**, at the branch `main`, which moves:
  <https://raw.githubusercontent.com/apple-oss-distributions/NFS/main/kext/nfs_vnops.c>,
  286,611 characters, of which characters 100,000 to 200,000 were read.
  `nfs3_vnop_create` has
  `nfs_avoid_needless_id_setting_on_create(dnp, vap, ctx);`,
  `gotuid = VATTR_IS_ACTIVE(vap, va_uid);`,
  `gotgid = VATTR_IS_ACTIVE(vap, va_gid);`, and, after the reply, a test
  that ends `(gotgid && (nvattr->nva_gid != vap->va_gid)))) {` over
  `/* clear ID bits if server didn't use them (or we can't tell) */`,
  `VATTR_CLEAR_SUPPORTED(vap, va_uid);` and
  `VATTR_CLEAR_SUPPORTED(vap, va_gid);`. By the tool, `nfs3_vnop_mkdir` and
  `nfs3_vnop_symlink` hold the same statements. (*Context*; §7)
- **Apple's NFS kext, `nfs_subs.c`**, at `main`:
  <https://raw.githubusercontent.com/apple-oss-distributions/NFS/main/kext/nfs_subs.c>.
  `nfs_avoid_needless_id_setting_on_create`'s comment, as quoted in
  *Context*, and its body: it clears an active `va_uid` that equals
  `kauth_cred_getuid(vfs_context_ucred(ctx))`, and an active `va_gid` where
  `(vap->va_gid == dnp->n_vattr.nva_gid) || (kauth_cred_getgid(vfs_context_ucred(ctx)) == vap->va_gid)`.
  `nfsm_chain_add_v3sattr_f` adds `TRUE` and `vap->va_gid` under
  `if (VATTR_IS_ACTIVE(vap, va_gid)) {`, and `FALSE` otherwise. The tool
  gave no count of the file's characters and no note of a part unread.
  (*Context*; §7)
- **Not fetched again.** POSIX `chmod()`, quoted as ADR 0025's *Sources*
  give it; ADR 0015's sources for `nosuid`; and ADR 0010's for the id
  4294967295.
