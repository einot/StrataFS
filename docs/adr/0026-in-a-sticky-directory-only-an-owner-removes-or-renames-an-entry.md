# 0026. In a sticky directory only root, the directory's owner or an entry's owner may remove, rename or replace that entry

**Status:** Accepted
**Date:** 2026-11-16
**Revised:** 2026-11-20 — before merge, after coder's commit `afbda93`, read
and not run, and to record the owner's decision on two of this ADR's
judgement calls. §9's `Checked against` pass is made: `trace.tsv`'s rows for
`FS.unlink` and `FS.Rename` were read against `afbda93` and hold, so the
module's `Checked against` line, `docs/tla/README.md`'s statement of it and
vocabulary V3's now read `afbda93`, and `trace.tsv` is unchanged
(Assumption 25, which is new and says how far the pass went). §2 records
what coder chose where the pins left room: the helper `stickyTakeOut`, which
is lines 3 and 4 of both rows, and that `Rename`'s stage E uses the two
inodes it looked up before the request. By the session's report, the owner
decided on 2026-11-16 that a rename of a name onto itself describes no
entry, and that the `CHANGES` line takes no `BREAKING:`; both were the
architect's judgement until then, and *Context*, §2, §12 and Assumptions 1,
11 and 21 now say so. Assumption 19 names the pass. The rows, the fields,
the status, the stages and every test this ADR pins are unchanged. The
module changed in one line of its header comment, so TLC's outcome cannot
differ, and CLAUDE.md's merge bar still asks the session for `tla.sh full`
on the PR.
**Issue:** #94, #148

## Context

**The owner's decisions.** Two, as ADR 0014 and ADR 0024 record them in the
owner's words, and a third the session reported. The brief this ADR was
written from gave the second in substance and neither in words, and nothing
here claims any approval beyond the three.

1. On 2026-10-10: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0025 were, and its design is the architect's (Assumption 1).
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." By the brief, such a change is still marked.
   Whether this one is breaking was Assumption 21's judgement, the owner's
   to overrule, until decision 3.
3. On 2026-11-16, by the session's report, which gave the owner's choice of
   an option on each of two judgement calls and not the owner's words, in
   answer to a question of the session's that the architect was not given:
   a rename of a name onto itself describes no entry, so that it keeps the
   answer it had at `273f010` (§2; Assumption 11); and the `CHANGES` line
   takes no `BREAKING:` (§12; Assumption 21). Both were the architect's
   judgement until then. The report says nothing of any other assumption
   here, and each of those stays the architect's.

**The defects.** Both as the session gave their texts (Assumption 2).

- **#94**, "The sticky bit on a directory is not honoured". `unlink` and
  `Rename` "check only write and search permission. S_ISVTX is stored but
  never read", so "Any user can remove or replace other users' files in a
  shared sticky directory." It cites POSIX XBD §4.5 secondhand, and says:
  "This matters fully only once #6 is fixed." Its done-when: "Remove, Rmdir
  and Rename, for the moved entry and for an existing target, are refused in
  an S_ISVTX directory unless the caller is root or owns the directory or the
  entry", and "The error code is decided."
- **#148**, which the session filed. ADR 0014 §11's W2 gives the root, `d`
  and `d2` the same `Object`, `{TypeDir, 0o755, 501, 20}`, "So nothing in the
  suite can tell which directory a request was made on": not whether
  `Rename` put the source in `Object` and the target in `Dst`, and not
  whether `Rmdir(root, "d2")` asked about the parent or about `d2`. W4 cannot
  see either, "because `Unix()`'s `OpRename` row treats `Object` and `Dst`
  alike, and its `Rmdir` rows never use a target directory whose verdict
  differs from the parent's". "The code is correct today", and a swap "would
  show only once a policy treats source and target differently, and #94's
  sticky-bit fix will do that". Its done-when: W2 is revised so that every
  directory whose `Object` a row names is distinguishable from the others,
  "the root, `d`, `d2`, and the entry an `Rmdir` removes"; a row or check
  tells source from target for `Rename`, "in both directions of a
  cross-directory rename", and the parent from the entry removed for `Remove`
  and `Rmdir`; and "This lands before or with #94's fix."

**What the code has.** Every statement about the code here was checked by
reading it at `273f010`, the commit this ADR's branch was cut from, which is
`origin/main` with ADR 0025 merged. Nothing was run (Assumption 2). Code is
cited by symbol; #94's line numbers are from `8377902` and name code that has
since moved.

- `policy.Request`, in `internal/policy/policy.go`, has `Op`, `Caller`,
  `Object`, `Dst`, `UID` and `GID`. Nothing in it describes the entry a call
  removes, moves or replaces.
- `policy.Unix()`'s `Check` answers `OpAddEntry` and `OpRemoveEntry` with
  `vfs.ErrNotDir` unless `Object` is a directory, and `vfs.ErrAcces` unless
  `Permits(Caller, Object, PermWrite|PermExec)`. It answers `OpRename` with
  `vfs.ErrNotDir` unless `Object` and `Dst` are both directories, and
  `vfs.ErrAcces` unless `Permits` gives write and search on both. `Permits`
  reads `Mode & 0o777`, so no row reads `0o1000`.
- In `internal/blobfs/fs.go`, `unlink`, which is `Remove` and `Rmdir`, calls
  `mutable()` and `validName`, takes `FS.mu` for writing, resolves the
  directory, asks `OpRemoveEntry` of it through `checkPerm`, and only then
  looks the name up. It answers `vfs.ErrNoEnt` for a name the directory
  lacks, or whose entry names an inode the table lacks (ADR 0024 §3); then
  `vfs.ErrNotDir`, `vfs.ErrIsDir` or `vfs.ErrNotEmpty`; and then removes the
  entry, calls `relink` and `markDirty`.
- `Rename` calls `mutable()` and `validName` twice, takes `FS.mu` for
  writing, resolves both directories, asks one `OpRename` with
  `Object: objectOf(src)` and `Dst: objectOf(dst)`, and only then looks the
  names up. It answers `vfs.ErrNoEnt` for a source name that names no inode;
  `vfs.ErrInval` for a directory moved into its own subtree; nil, having
  changed nothing, when the target name already names the inode it would
  move; `vfs.ErrIsDir`, `vfs.ErrNotDir` or `vfs.ErrNotEmpty` for a target it
  cannot replace; and then replaces the target, moves the entry and calls
  `markDirty`. A target entry that names no inode is replaced, with no
  `relink` (ADR 0017 §2).
- `createEntry` removes and replaces nothing. A non-exclusive `Create` of a
  name that names a non-directory returns that object, and every other
  creation of a name the directory has is refused: with `vfs.ErrExist`, or,
  for a non-exclusive `Create` of a name whose entry names no inode, with
  `vfs.ErrNoEnt`.
- `allocInode` stores a new inode's mode as `mode & 0o7777` and makes the
  caller its owner. `applySetAttr` stores `*sa.Mode & 0o7777`, once `OpChmod`
  has allowed root or the owner. `attr` reports `Mode & 0o7777`. So `0o1000`
  is set by a creation or by `SETATTR`, stored, committed and reported by
  `GETATTR`, and read by no decision.
- `policy.AccessBits` gives a directory `vfs.AccessDelete` when
  `Permits(c, o, PermWrite)`, from the low nine bits.
- `internal/nfs/nfs3.go`'s `remove` and `rename` handlers answer with
  `statusOf(err)` followed by one `wcc_data`, or two for `RENAME`, whatever
  the status, and `statusOf` passes a `vfs.Status` through unchanged. `LINK`
  answers `NFS3ERR_NOTSUPP`, so no call of this build gives an inode a second
  name.

**What POSIX says.** XBD §4.5, "Directory Protection", in Issue 8, which is
the edition read (*Sources*): "If a directory is writable and the mode bit
S_ISVTX is set on the directory, a process may remove or rename files within
that directory only if one or more of the following is true:" "The effective
user ID of the process is the same as that of the owner ID of the file." "The
effective user ID of the process is the same as that of the owner ID of the
directory." "The process has appropriate privileges." "Optionally, the file is
writable by the process. Whether or not files that are writable by the
process can be removed or renamed is implementation-defined." And: "If the
S_ISVTX bit is set on a non-directory file, the behavior is unspecified."

`unlink()`, `rmdir()` and `rename()` each list the failure under "[EPERM] or
[EACCES]", marked XSI, among the errors on which the function "shall fail".
For `unlink()` and `rmdir()`: "The S_ISVTX flag is set on the directory
containing the file referred to by the path argument and the process does not
satisfy the criteria specified in XBD 4.5 Directory Protection." For
`rename()`: "The S_ISVTX flag is set on the directory containing the file
referred to by old and the process does not satisfy the criteria specified in
XBD 4.5 Directory Protection with respect to old; or new refers to an
existing file, the S_ISVTX flag is set on the directory containing this file,
and the process does not satisfy the criteria specified in XBD 4.5 Directory
Protection with respect to this file." Each lists write permission on the
directory apart, under "[EACCES]". `rename()` also says: "If the old argument
and the new argument resolve to either the same existing directory entry or
different directory entries for the same existing file, rename() shall return
successfully and perform no other action."

**What RFC 1813 says** (*Sources*).

- Of the bit: `fattr3`'s mode bits give `0x00200`, which is `0o1000`, as
  "Save swapped text (not defined in POSIX)." The RFC gives it no meaning for
  a directory, so the meaning here is POSIX's.
- Of the codes, §2.6: `NFS3ERR_PERM` is "Not owner. The operation was not
  allowed because the caller is either not a privileged user (root) or not
  the owner of the target of the operation." `NFS3ERR_ACCES` is "Permission
  denied. The caller does not have the correct permission to perform the
  requested operation. Contrast this with NFS3ERR_PERM, which restricts
  itself to owner or privileged user permission failures."
- Of each procedure's ERRORS list, §3: "These lists are not intended to be
  the definitive statement of all of the errors which can be returned by any
  specific procedure, but as a guide for the more common errors which may be
  returned. Client implementations should be prepared to deal with unexpected
  errors coming from a server." The lists of `REMOVE`, `RMDIR` and `RENAME`
  each hold `NFS3ERR_ACCES`, and none holds `NFS3ERR_PERM`.
- Of `RENAME`, IMPLEMENTATION: "If from and to both refer to the same file
  (they might be hard links of each other), then RENAME should perform no
  action and return NFS3_OK."

**How Linux reads it**, at the tag `v6.6` (*Sources*).

- The rule is `fs/namei.c`'s `may_delete`, which `vfs_unlink` and `vfs_rmdir`
  call first, and `vfs_rename` calls for the entry it moves and again for a
  target it replaces. Its comment lists: "5. If the sticky bit on dir is set
  we should either a. be owner of dir, or b. be owner of victim, or c. have
  CAP_FOWNER capability". Its body answers `-ENOENT` for a name that names
  nothing; then `inode_permission(idmap, dir, MAY_WRITE | MAY_EXEC)`; then
  `if (check_sticky(idmap, dir, inode) || …) return -EPERM;`; and only then
  `-ENOTDIR` or `-EISDIR` for an entry of the wrong kind.
- `check_sticky` returns 0 unless `dir->i_mode & S_ISVTX`, and
  `__check_sticky` returns 0 for the owner of the inode, 0 for the owner of
  the directory, and otherwise
  `!capable_wrt_inode_uidgid(idmap, inode, CAP_FOWNER)`. It reads no type and
  no mode of the entry, and it has no clause for a file the caller may
  write.
- `vfs_rename` begins `if (source == target) return 0;`, before either
  `may_delete`. A rename into a directory that replaces nothing is judged by
  `may_create`, which asks write and search of the directory and nothing of
  the sticky bit.
- The server, `fs/nfsd/vfs.c`: `nfsd_unlink` and `nfsd_rename` verify the
  directory handles with `NFSD_MAY_REMOVE` and `NFSD_MAY_CREATE`, look the
  names up, call `vfs_unlink`, `vfs_rmdir` or `vfs_rename`, and map the result
  with `nfserrno`, whose table has `{ nfserr_perm, -EPERM }`. So Linux's
  server answers a sticky refusal `NFS3ERR_PERM`, after the directories'
  permission, and has no sticky check of its own. Its `ACCESS` maps
  `NFS3_ACCESS_DELETE` to `NFSD_MAY_REMOVE` on the directory, with no entry
  in view.
- The client: `may_delete` is the VFS's, so a Linux NFS client applies the
  rule itself, from the mode and the owners the server reported, before it
  sends `REMOVE`, `RMDIR` or `RENAME` (Assumption 15). Its
  `fs/nfs/nfs3xdr.c` maps `NFSERR_PERM` to `-EPERM`.

**How macOS's client reads it**, at the `main` branch of Apple's sources
(*Sources*; Assumption 15). The NFS kext's mount code, under the comment "For
NFSv3 and greater, there is a (relatively) reliable ACCESS call.", calls
`vfs_setauthopaqueaccess(mp)` and `vfs_setauthopaque(mp)` unless the mount
asks otherwise. XNU's `vnode_authorize_callback_int` then has
`if ((vp->v_mount->mnt_kern_flag & MNTK_AUTH_OPAQUE) && vnode_authorize_opaque(vp, &result, action, ctx)) { goto out; }`,
and `vnode_authorize_opaque`, whose comment reads "Handle authorization
actions for filesystems that advertise that the server will be enforcing.",
asks the filesystem through `VNOP_ACCESS`. The kernel's own sticky check is
`vnode_authorize_delete`'s, under `/* enforce sticky bit behaviour */`, which
returns `EACCES`. So, as read, a macOS client leaves the rule to the server
on an NFSv3 mount, and where it applies the rule itself, on a local
filesystem, that check's answer is `EACCES` where Linux's is `EPERM`.

**What the ADRs say.**

- ADR 0014 §5 pins `Request`, `Permits`, the rows of `Unix()` and
  `AccessBits`, and its *Growth* rule: "A field added to `Request` later is
  defined so that a request that leaves it zero means what the same request
  meant before the field existed. The ADR that adds it says what changes, if
  anything, for such a request." Its §6 gives the stages A to E, the requests
  each method makes, and what `blobfs` relies on in `Unix()`'s type rows. Its
  §11 lists U4 as unpinned: "`OpRemoveEntry` or `OpRename` where the
  directory has 0o1000 set (`Object`, or for `OpRename` either `Object` or
  `Dst`), for every caller (#94; POSIX XBD 4.5). `Permits` itself ignores the
  bit, and P2 pins that." Its §11 also holds W2 and W4, which #148 is about.
- ADR 0018 §1 to §3 make the policy rule standing: a change to what an
  existing decision allows keeps that decision a pure policy in
  `internal/policy`, with no inline condition. Its §2 keeps a check "of what
  it names (stage E)" outside the rule: it "says what the operation is, not
  who may perform it".
- ADR 0024 §1: a `vfs.FS` method of `blobfs` that returns an error has
  changed nothing in the namespace, and `markDirty` is called exactly where a
  change is made. Its §3: a name whose entry names no inode is reported
  absent by `unlink`, and replaced by a `Rename` onto it.
- ADR 0025 §1 holds a directory's owner to the directory's mode, and its §3
  says what #97 will have to reconcile between `ACCESS` and the operations.
- ADR 0011 §4 and ADR 0014 §7 pin what `mutable()` answers on a read-only
  and on a diverged mount. ADR 0007 §4 pins the order of errors.
- ADR 0015 records the trust model: `readCred` believes the uid of an
  AUTH_SYS credential, and by default any local process can connect "and
  claim any uid, root included". It gives #6 as authenticating the peer and
  #95 as root squash (its Assumption 11).
- ADR 0017 §2 gives `relink` its callers, and ADR 0010 the owner of a new
  filesystem's root directory. Neither says who may remove an entry.
- ADR 0013 §1 gives permission rules to exhaustive tests of the policy and
  to security review, not to TLA+, and its §8 says what a refactor of traced
  code changes under `docs/tla/`.

**What this constrains.** `policy.Request` and two rows of `policy.Unix()`,
in `internal/policy/policy.go`; how `unlink` and `Rename`, in
`internal/blobfs/fs.go`, build the request each makes; ADR 0014 §5's bullets
for those rows, §6's bullets for the requests of `Remove`, `Rmdir` and
`Rename`, and §11's U4, W2 and W4, by cross-reference; and the tests of
`internal/policy`, `internal/blobfs` and `internal/nfs`. No non-test code of
`internal/nfs` or `internal/vfs` changes, and nothing in the bucket or the
flags. On the wire, three procedures can answer a status they could not
answer before, `NFS3ERR_PERM`, and nothing else changes.

## Decision

### 1. The rule

**Terms.** ADR 0014 §5's stand: *root* is `Caller.UID == 0`, and *a
directory* is `Type == vfs.TypeDir`. Two are added. A directory is *sticky*
when its mode has `0o1000` set: `Mode & 0o1000 != 0`. A caller *owns* an
object when `Caller.UID` equals the object's `UID`.

**The rule.** In a sticky directory, a call takes an entry's name out of the
directory only if its caller is root, owns the directory, or owns the object
the entry names. A call takes a name out when it removes it, which `Remove`
and `Rmdir` do; when it moves it, to another name or to another directory,
which `Rename` does to its source; and when it replaces it, which `Rename`
does to a target name that names another object. Any other caller is refused
with `vfs.ErrPerm` (§3). The rule is asked only of a caller the directory's
mode gives write and search, since one it does not is refused
`vfs.ErrAcces` first, as before.

**The rows.** `Unix().Check` answers `OpRemoveEntry` and `OpRename` as below,
in place of ADR 0014 §5's bullets for them. `OpAddEntry`, which shared
`OpRemoveEntry`'s bullet, keeps it. `Entry` and `Replaced` are §2's fields.
Each row is read from the top, and the first line whose condition holds
gives the answer.

- `OpRemoveEntry`:
  1. `vfs.ErrNotDir`, unless `Object` is a directory;
  2. `vfs.ErrAcces`, unless
     `Permits(Caller, Object, PermWrite|PermExec)`;
  3. `vfs.ErrPerm`, if `Entry` is not nil, `Object` is sticky, and the
     caller is not root, does not own `Object` and does not own `*Entry`;
  4. nil otherwise.
- `OpRename`:
  1. `vfs.ErrNotDir`, unless `Object` and `Dst` are both directories;
  2. `vfs.ErrAcces`, unless both
     `Permits(Caller, Object, PermWrite|PermExec)` and
     `Permits(Caller, Dst, PermWrite|PermExec)`;
  3. `vfs.ErrPerm`, if `Entry` is not nil, `Object` is sticky, and the
     caller is not root, does not own `Object` and does not own `*Entry`;
  4. `vfs.ErrPerm`, if `Replaced` is not nil, `Dst` is sticky, and the
     caller is not root, does not own `Dst` and does not own `*Replaced`;
  5. nil otherwise.

Lines 1 and 2 of each row are ADR 0014 §5's bullets, word for word, so a
request that sets neither field is answered as it was (§2, *Growth*).
Everything else in ADR 0014 §5 and ADR 0025 §1 stands as written: `InGroup`,
`Permits`, `AccessBits`, every other row, the bare `vfs.Status` of a refusal,
purity, `All` and `Func`. No function, type or constant is added to
`internal/policy`'s exported names; `Request` gains two fields (§2).

**What the rows read.** `Op`, `Caller`, `Object` and `Entry`, and for
`OpRename` `Dst` and `Replaced` too. Of `Object` and `Dst` they read the
type, the low nine mode bits and the `GID` through `Permits`, the bit
`0o1000`, and the
`UID`. Of `*Entry` and `*Replaced` they read the `UID` and nothing else: not
the type, the mode or the group. `OpRemoveEntry` does not read `Replaced`,
and no other row reads either field.

For a directory `tmp` of mode `0o1777` owned by `501:20`, which holds `a`,
owned by uid 502, and `b`, owned by uid 503; a directory `plain` of mode
`0o777`, which holds `p`, owned by uid 502; and a directory `grp` of mode
`0o1770` owned by `501:20`, which holds `g`, owned by uid 501:

| Call | Caller | Answer | At `273f010` |
|---|---|---|---|
| `Remove(tmp, "a")` | `503:20` | `vfs.ErrPerm` | nil, and `a` is gone |
| `Remove(tmp, "a")` | `502:30`, the entry's owner | nil | the same |
| `Remove(tmp, "a")` | `501:20`, the directory's owner | nil | the same |
| `Remove(tmp, "a")` | uid 0, root | nil | the same |
| `Rename(tmp, "a", tmp, "c")` | `503:20` | `vfs.ErrPerm` | nil |
| `Rename(tmp, "a", plain, "c")` | `503:20` | `vfs.ErrPerm`, by line 3 | nil |
| `Rename(plain, "p", tmp, "c")` | `503:20` | nil: the source is not sticky, and nothing is replaced | the same |
| `Rename(tmp, "b", tmp, "a")` | `503:20`, who owns `b` | `vfs.ErrPerm`, by line 4 | nil, and `a` is replaced |
| `Rename(tmp, "b", plain, "p")` | `503:20` | nil: the target is not sticky | the same |
| `Rename(tmp, "a", tmp, "a")` | `503:20` | nil, and nothing changes (§2) | the same |
| `Remove(tmp, "nope")` | `503:20` | `vfs.ErrNoEnt` (§2) | the same |
| `Remove(grp, "g")` | `503:20`, in the group | `vfs.ErrPerm` | nil |
| `Remove(grp, "g")` | `502:30` | `vfs.ErrAcces`, by line 2 | the same |

**Three may, and no fourth** (Assumption 3). Root, the directory's owner and
the entry's owner are XBD §4.5's first three conditions. Its fourth,
"Optionally, the file is writable by the process", is implementation-defined
and is not taken: Linux does not take it, and with it one user could remove
another's file from a shared directory whenever that file's mode gave it
write, which is not what a directory is made sticky for.

**Root and the owners are uids** (Assumption 4). "Root" is `Caller.UID == 0`
and "owns" is an equality of two uids, the call's against the inode's as it
stands when the request is made. No group makes a caller an owner, and no
uid is special but 0: an entry owned by uid 0 is protected from everyone but
root and the directory's owner, as any other entry is.

**For every type of entry** (Assumption 5). XBD §4.5 speaks of "files", which
in POSIX is every type, and the rows read no type of the entry. So the rule
holds for a regular file, a directory, a symlink, and an inode whose type no
operation of this server makes, alike: `Rmdir` of another's directory, and
`Remove` of another's symlink, are refused as `Remove` of another's file is.

**Only `0o1000`, and only on a directory.** Set-user-ID and set-group-ID
bits do not make a directory sticky. The bit on anything but a directory is
stored and reported as before and is read by no row: `Object`'s and `Dst`'s
type is checked at line 1, before the bit is looked at, and `*Entry`'s mode
is not read. POSIX leaves that case unspecified (Assumption 23).

**The directory's mode comes first, and binds the owners too**
(Assumption 6). Line 2 is asked before lines 3 and 4. So a caller who owns an
entry and lacks write or search on its directory is refused `vfs.ErrAcces`:
owning an entry is no permission to change the directory. And a directory's
owner whose own bits lack write or search is refused `vfs.ErrAcces` too, as
ADR 0025 §1 has it. XBD §4.5 says the same by its opening words, "If a
directory is writable".

**A rename that crosses directories** is judged in each directory for what
it takes out of that one:

- the source directory's bit guards the entry moved (line 3);
- the target directory's bit guards the entry replaced, if there is one
  (line 4);
- a rename into a sticky directory that replaces nothing takes nothing out
  of it, so it needs only write and search there, as a creation does;
- if both are sticky, both lines apply, the source's first;
- a rename within one directory is the same request with that directory as
  `Object` and as `Dst`, so both lines read the same directory.

That is `rename()`'s error as POSIX words it, "with respect to old; or new
refers to an existing file … with respect to this file", and Linux's two
`may_delete` calls.

**What a client can observe.** In a directory whose mode has `0o1000` set,
from a caller that has write and search there and is neither root, nor the
directory's owner, nor the owner of the entry named:

- `REMOVE` and `RMDIR` of that entry answer `NFS3ERR_PERM`, where they
  answered `NFS3_OK` and removed it;
- `RENAME` of that entry, within the directory or out of it, and `RENAME`
  of any entry onto it, answer `NFS3ERR_PERM`, where they answered
  `NFS3_OK`;
- the reply has the form it has for any refusal: the status and the
  directory's `wcc_data`, or both directories' for `RENAME`, with the
  attributes unchanged (§4).

For every other call nothing changes: `GETATTR` reported the bit before and
reports it now, `ACCESS` answers as it did (§5), and a directory without the
bit is as it was.

### 2. What a request carries, and how `blobfs` fills it

**The fields.** `policy.Request` gains two, after `GID` (Assumption 9):

```go
type Request struct {
    Op     Op
    Caller vfs.Caller
    Object Object // as ADR 0014 §5 has it
    Dst    Object // OpRename: the target directory
    UID    uint32 // OpChown: the owner asked for
    GID    uint32 // OpChgrp: the group asked for

    // Entry is the object whose name the call takes out of Object: the one
    // OpRemoveEntry removes, and the one OpRename moves. Nil when the call
    // takes none out.
    Entry *Object

    // Replaced is the object whose name OpRename takes out of Dst, to put
    // the object it moves in its place. Nil when it replaces none.
    Replaced *Object
}
```

The names, the types and the order are pinned; the doc comments are pinned
in substance. `Object` itself is unchanged.

**Growth.** This is ADR 0014 §5's *Growth* rule applied: a request that
leaves both fields nil means what it meant before they existed. For such a
request `OpRemoveEntry` and `OpRename` judge the directories alone, by lines
1 and 2, whatever bits the directories' modes have. So every request built
before this ADR, by `blobfs` or by a test, is answered as it was.

**Purity.** ADR 0014 §5's rule stands and is extended by one clause: a
policy modifies nothing it is given, `Caller.GIDs` and what `Entry` and
`Replaced` point to included, and keeps neither pointer after it returns.
It still takes no lock, makes no store call, logs nothing and allocates
nothing when it allows.

**One request per call, as before** (Assumption 8). `Remove` and `Rmdir`
still make exactly one `OpRemoveEntry`, and `Rename` exactly one `OpRename`.
What changes is that `blobfs` looks the names up before it asks, so that the
request can say what the call would take out. The lookup is a read of the
directory's entry map and of the inode table, in the hold of `FS.mu` for
writing that the method already has. It returns no error and changes
nothing: `vfs.ErrNoEnt` is still stage E's, after the request.

**A name names an object** when the directory's entry map has the name and
the inode table holds the inode that entry gives. A name the directory
lacks, and a name whose entry names an inode the table lacks, name none
(Assumption 12; ADR 0024 §3).

**`unlink`, for `Remove` and `Rmdir`.** Holding `FS.mu` for writing, after
the resolve of stage C:

1. look `name` up in the directory;
2. ask `f.perm` one request: `Op` `OpRemoveEntry`, `Caller` the call's,
   `Object` `objectOf` of the directory, and `Entry` pointing to `objectOf`
   of the object the name names, or nil if it names none;
3. return a refusal unchanged;
4. then stage E, as now and in the order it has now: `vfs.ErrNoEnt` if the
   name names no object; `vfs.ErrNotDir`, `vfs.ErrIsDir` or
   `vfs.ErrNotEmpty`; and the change.

**`Rename`.** Holding `FS.mu` for writing, after both resolves:

1. look `fromName` up in the source directory and `toName` in the target
   directory;
2. the rename is *onto itself* when `fromName` names an object and `toName`
   names the same inode;
3. ask `f.perm` one request: `Op` `OpRename`, `Caller` the call's, `Object`
   `objectOf` of the source directory, `Dst` `objectOf` of the target
   directory, and:
   - `Entry` pointing to `objectOf` of the object `fromName` names, if it
     names one and the rename is not onto itself; nil otherwise;
   - `Replaced` pointing to `objectOf` of the object `toName` names, if
     `Entry` is set and `toName` names one; nil otherwise;
4. return a refusal unchanged;
5. then stage E, as now and in the order it has now: `vfs.ErrNoEnt` if
   `fromName` names no object; `vfs.ErrInval` for a directory moved into its
   own subtree; nil, with nothing changed, for a rename onto itself;
   `vfs.ErrIsDir`, `vfs.ErrNotDir` or `vfs.ErrNotEmpty`; and the change.

Each `Object` is built from the inode as it stands at the request, in the
hold that makes the change, and each pointer is to a value of the request's
own, which `blobfs` writes nothing to once it has asked. `Entry` and
`Replaced` never point to the same variable.

**A rename of a name onto itself describes no entry** (Assumption 11). Such
a call takes no name out of any directory: it removes, moves and replaces
nothing, and returns nil having changed nothing. So its request sets neither
field, the rows judge the two directories alone, and its answer is what it
was at `273f010`. RFC 1813 says of it that "RENAME should perform no action
and return NFS3_OK", POSIX that `rename()` "shall return successfully and
perform no other action", and Linux returns before either sticky check.
Whether a rename is onto itself is a fact about what the request names
against the namespace, which is stage E's kind of fact and not a judgement
of the caller (ADR 0018 §2), so `blobfs` deciding it is no inline permission
check; the judgement of the caller is still the row's. By the session's
report, the owner decided on 2026-11-16 that such a rename describes no
entry and so keeps its answer (*Context*, decision 3).

**A name that names no object describes no entry** (Assumptions 12 and 13).
There is no owner to compare. So:

- `Remove`, `Rmdir` and `Rename` of a name that names none are asked about
  the directories alone and then answer `vfs.ErrNoEnt`, as before;
- a `Rename` onto a name whose entry names an inode the table lacks replaces
  that entry, as ADR 0017 §2 and ADR 0024 §3 have it, with no `Replaced` for
  the row to judge, in a sticky directory as in any other;
- a `Rename` whose source names none describes no `Replaced` either, since
  it will replace nothing.

**`createEntry` does not change.** A creation takes no name out of a
directory, in a sticky directory as in any other, so `OpAddEntry` carries
neither field and its row is ADR 0014 §5's.

**No other method changes**, and `checkPerm` keeps the callers that set no
field but `Op`, `Caller` and `Object`.

**The order of errors** is ADR 0007 §4's and ADR 0014 §6's stages, with one
status added at stage D:

| Stage | `Remove`, `Rmdir` | `Rename` |
|---|---|---|
| A, the mount gate | `vfs.ErrROFS`, `vfs.ErrStale` | the same |
| B, the names | `vfs.ErrInval`, `vfs.ErrNameTooLong` | the same |
| C, the handles | `vfs.ErrBadHandle`, `vfs.ErrStale` | the same |
| D, the row | `vfs.ErrNotDir`; `vfs.ErrAcces`; `vfs.ErrPerm` | `vfs.ErrNotDir`; `vfs.ErrAcces`; `vfs.ErrPerm` for the source, then for the target |
| E, the namespace | `vfs.ErrNoEnt`; `vfs.ErrNotDir` or `vfs.ErrIsDir`; `vfs.ErrNotEmpty` | `vfs.ErrNoEnt`; `vfs.ErrInval`; nil onto itself; `vfs.ErrIsDir` or `vfs.ErrNotDir`; `vfs.ErrNotEmpty` |

So (Assumption 10):

- **The mount gates come first.** `unlink` and `Rename` call `mutable()`
  before anything else. On a read-only mount every `Remove`, `Rmdir` and
  `Rename` answers `vfs.ErrROFS`, and on a diverged mount `vfs.ErrStale`,
  for every caller, an owner and root included, as before.
- **`vfs.ErrPerm` and `vfs.ErrNoEnt` never compete.** The first needs the
  name to name an object, and the second that it names none.
- **`vfs.ErrPerm` comes before stage E's other statuses.** `Rmdir` of
  another's regular file in a sticky directory answers `vfs.ErrPerm`, not
  `vfs.ErrNotDir`; `Rmdir` of another's directory that holds entries answers
  `vfs.ErrPerm`, not `vfs.ErrNotEmpty`; and a `Rename` of another's
  directory into its own subtree answers `vfs.ErrPerm`, not `vfs.ErrInval`.
  That is the stages' order, permission before preconditions, as a `Mkdir`
  of a name that exists in a directory the caller cannot write answers
  `vfs.ErrAcces` and not `vfs.ErrExist`.

**What coder's commit chose**, recorded against `afbda93` where the pins
above left room (Assumption 25):

- In `internal/policy/policy.go`, lines 3 and 4 of both rows are one
  unexported function,
  `stickyTakeOut(c vfs.Caller, dir Object, entry *Object) error`. It returns
  nil for a nil `entry`, and for a `dir` whose mode lacks `0o1000`; nil
  where `rootOrOwner(c, dir)`, the function `OpChmod`, `OpChown`, `OpChgrp`
  and `OpSetTimes` already use, or where `c.UID == entry.UID`; and the bare
  `vfs.ErrPerm` otherwise. `OpRemoveEntry` returns `dirAccess`'s refusal,
  or else `stickyTakeOut(c, o, r.Entry)`. `OpRename`, past its two
  unchanged checks, returns the refusal of `stickyTakeOut(c, o, r.Entry)`,
  or else `stickyTakeOut(c, r.Dst, r.Replaced)`. `OpAddEntry` has a case of
  its own, which is `dirAccess` alone.
- In `internal/blobfs/fs.go`, `unlink` builds its request in place and no
  longer calls `checkPerm`, which is unchanged and keeps its other callers.
  `unlink` and `Rename` index the inode table only for a name the entry map
  has, and pass `objectOf` no inode the table lacks.
- `Rename` keeps the two inodes it looked up before the request, the one it
  moves and the one it replaces, and its stage E uses them and does not read
  the inode table a second time, where at `273f010` stage E made the only
  read. The lookup and stage E are in the one hold of `FS.mu` for writing,
  and nothing between them writes the table: the request is pure, and
  `isAncestor` only reads. So stage E sees what a second read would see, and
  its statuses and their order are as they were. Whether the rename is onto
  itself is one condition, that the target name is in its entry map with
  the inode number the source name gave, asked once the source name is
  known to name an object.

### 3. The refusal is `vfs.ErrPerm`

A sticky refusal is the bare `vfs.ErrPerm`, which `statusOf` passes to the
client as `NFS3ERR_PERM`, 1. It differs from the refusal of a caller the
directory's mode denies, which stays `vfs.ErrAcces`, `NFS3ERR_ACCES`, 13
(Assumption 7).

1. **It is the code RFC 1813 defines for this.** `NFS3ERR_PERM` is "Not
   owner. The operation was not allowed because the caller is either not a
   privileged user (root) or not the owner of the target of the operation",
   and `NFS3ERR_ACCES` is contrasted with it as the code for a missing
   permission. The sticky rule is a rule about owners and root, and nothing
   else.
2. **POSIX allows either**, "[EPERM] or [EACCES]", so the choice is this
   server's.
3. **It is what a client of Linux's server sees**, since that server maps
   `-EPERM` from `may_delete` to `NFS3ERR_PERM`, and Linux's client maps it
   back to `EPERM`, the errno a local Linux filesystem gives. A macOS
   kernel's own local check returns `EACCES`; a server that followed it
   would give Linux clients a code no Linux filesystem gives, and POSIX
   makes neither wrong.
4. **It is this policy's own usage.** `OpChmod`, `OpChown` and `OpChgrp`
   refuse a caller who is neither root nor the owner with `vfs.ErrPerm`,
   and every refusal by mode bits is `vfs.ErrAcces`. A sticky refusal is of
   the first kind.
5. **The two can be told apart**, by a user, by a client's logs and by a
   test: `vfs.ErrAcces` says "this directory's mode does not let you change
   it", and `vfs.ErrPerm` says "it does, but this entry is not yours".

`NFS3ERR_PERM` is not in the ERRORS lists RFC 1813 gives `REMOVE`, `RMDIR`
and `RENAME`. Those lists are "not intended to be the definitive statement
of all of the errors which can be returned by any specific procedure", the
code is one of the protocol's own, and Linux's server answers it for these
procedures, so clients already meet it.

Nothing in `internal/vfs` changes: `vfs.ErrPerm` exists, and `vfs.FS`'s doc
comments say nothing of permission rules, which are `internal/policy`'s
(Assumption 16). Nothing in `internal/nfs` changes either.

### 4. A refused call changes nothing

ADR 0024 §1's rule holds on both paths this ADR touches, and nothing has to
move for it: at `273f010` each already makes its request before its first
change, and the lookup this ADR puts before the request is a read.

- **`unlink`.** The lookup and the request are made in the hold of `FS.mu`
  for writing, before `delete(d.Entries, name)`, the directory's `touchM`,
  `relink`, the inode's removal from the table, `dropOpen` and `markDirty`.
  A refusal returns from that hold with the directory's entries and times,
  the entry's inode and link count, `FS.open`, `FS.dirty` and `FS.changes`
  as they were.
- **`Rename`.** The same: both lookups and the request come before the
  replaced object's `relink` and removal, the two entry-map writes, the
  moved object's `relink`, `Parent` and `touchC`, the directories' `touchM`
  and `markDirty`. A refusal for the source or for the target leaves both
  directories, the object that would have moved and the one that would have
  been replaced as they were.

So after a sticky refusal a `Sync` with nothing else dirty makes no store
call, and the reply's `wcc_data` shows the directories unchanged. A call
that is allowed marks the namespace dirty in the hold that makes its change,
as before, and a rename onto itself, which changes nothing, still marks
nothing.

**The verdict and the change share one hold.** The owners and the modes the
row reads are read in the hold that removes or moves the entry, so nothing
can change them between the verdict and the change. This is not `Write`'s
case (#101).

**A refusal is not logged**, as no refusal of a policy is (ADR 0018 §5).

### 5. `ACCESS` reports the directory's mode

**Neither `AccessBits` nor `Access` changes** (Assumption 14).
`vfs.AccessDelete` is granted on a directory when
`Permits(c, o, PermWrite)`, as ADR 0014 §5 pins it, in a sticky directory as
in any other.

**Why.** `ACCESS` is asked of one object, the directory, and names no entry.
RFC 1813 gives `ACCESS3_DELETE` as "Delete an existing directory entry.",
whichever entry that is, and calls the procedure's results "necessarily
advisory"; both are as ADR 0014's *Sources* quote them, and were not fetched
again. The sticky rule's answer depends on which entry, so no answer about
the directory can carry it. Granting the bit says that the caller may delete
from this directory, which is true of its own entries; withholding it would
tell a caller that it cannot delete entries it owns. Linux's server grants
it the same way, from the directory alone.

**What #97 inherits.** ADR 0025 §3 left #97 four things, the first of them a
direction: what `ACCESS` grants, the operations allow. This ADR adds a
fifth, which qualifies that one. In a sticky directory `ACCESS` grants
`vfs.AccessDelete` to a caller with write permission, and `Remove`, `Rmdir`
and `Rename` then refuse that caller, unless it is root or owns the
directory, every entry that is not its own. That is not a defect for #97 to
remove, since `ACCESS` cannot see the entry. So the property #97 states
holds for `vfs.AccessDelete` only as "some entry", or excepts sticky
directories.

### 6. Security

**What narrows**, and nothing widens. In a directory whose mode has `0o1000`
set, a caller that has write and search there, and is neither root, nor the
directory's owner, nor the owner of the entry concerned, is refused:

- (a) `Remove` or `Rmdir` of that entry;
- (b) `Rename` of that entry, to another name there or out of the directory;
- (c) `Rename` of any entry onto it.

No request that was refused is allowed: lines 1 and 2 of both rows are what
they were, no other row moves, and `ACCESS` reports nothing more and nothing
less. Root is as it was, and no call gets past the mount gate that did not.

**The uid is the client's claim.** `callerFrom` takes the uid from the
call's AUTH_SYS credential, which `readCred` believes, and by default any
process on the machine can connect "and claim any uid, root included"
(ADR 0015 §8; README, *Honest limitations*). So "root", "the directory's
owner" and "the entry's owner" are whoever sends that uid, and a caller can
read the two owners' uids from `GETATTR` or `READDIRPLUS`. The rule is a rule
about a claimed uid, as every other row is. It does not say the caller is
who it claims to be.

**What it gives each kind of caller** (Assumption 15).

- *A caller that speaks the protocol itself and chooses its uid.* Nothing
  binds it. It can send uid 0, which every line allows, or the uid of either
  owner. This is the limitation the README states first, and #6, #90 and #95
  own it. Root squash alone would not change it: it takes uid 0 away and
  leaves both owners' uids to claim (ADR 0015, *Alternatives considered*,
  says the same of set-ID files).
- *A user behind a Linux kernel client.* Already refused, by its own
  kernel. `may_delete` runs in the client's VFS before the call is sent, on
  the owners and the mode this server reported, and that mode has always
  carried `0o1000`. The server's rule changes what such a user is refused
  only where the client's cached attributes are stale, and then gives the
  errno the client would have given.
- *A user behind a macOS kernel client.* As read, the kernel leaves the
  decision on an NFSv3 mount to the server, so until this ADR nothing
  applied the rule for such a user: `rm` and `mv` of another user's entry in
  a sticky directory succeeded. For that user the rule is new, and it is the
  protection the bit is set for: against another user's accident, and
  against another user's ordinary tools.
- *A caller under a stricter trust model*, should #6 bring one. The rule is
  then a boundary between the users of a shared directory, and it is already
  where that model needs it: in the row, read in the hold that makes the
  change (§4).

**How #6 bears on it.** #94 says: "This matters fully only once #6 is
fixed." This ADR was not given #6's text; ADR 0015 gives its subject as
authenticating the peer (its Assumption 11). The sentence is right, and this
ADR does not wait for #6: until the server can tell who is calling, the rule
protects users from each other through honest clients only, which is what
every permission of this server does today. After #6 the same row protects
them from each other as far as #6 lets the server trust the uid it is sent:
outright only if #6 authenticates the user, and not the peer host alone.

**What the rule does not protect**, with or without #6:

- *An entry's contents.* The rule is about names. A file in a sticky
  directory whose mode gives a caller write can still be written and
  truncated by that caller, by the file's own rows.
- *The directory from additions.* Any caller with write and search can
  create in it and rename into it.
- *A file reached by another name.* None exists in this build, which makes
  no hard links.
- *An entry that names no inode* (§2), which no caller owns.
- *The bit.* The directory's owner, or root, can clear it (`OpChmod`), and
  the owner needs no bit to remove anything in its own directory.
- *The caller's expectation from `ACCESS`* (§5).

**Why it is worth deciding now.**

1. The bit is stored, committed and shown to every client, and a user who
   sets it, or copies a tree that has it, is told by `ls` that the directory
   is protected. On a client that leaves the check to the server, it was
   not.
2. It is one row, tested exhaustively, and it removes U4, the last
   directory row that ADR 0014 left unpinned for a filed defect.
3. A design that tightens the trust model inherits a rule that is already
   in place and already read in the hold that makes the change.

### 7. W2 and W4, for #148

**Why this ADR holds the revision** (Assumption 17). #148 asks that ADR 0014
§11's W2 be revised. It is revised by replacement, under
`docs/adr/README.md`'s rule for a part a later ADR replaces: the tree and
the rows for `Remove`, `Rmdir` and `Rename` are this section's, ADR 0014
keeps its text as the record of what was decided on 2026-10-31, and it gains
a `Revised` line and a pointer sentence in each section touched (§12). Three
reasons. The revised rows
name `Entry` and `Replaced`, which this ADR adds, so they cannot be stated
in ADR 0014 without restating §2 there. The README corrects in place only a
restatement whose fact has moved, and W2 as written was not wrong: it was
too weak. And coder, test-author and review then have one section to work
from.

**The harness, the callers, the facts and the safety rule** are ADR 0014
§11's, as its *W2 to W5 in detail* gives them, and are not restated. W2's
test keeps its name, `TestEveryMethodAsksThePolicyAndStopsAtItsRefusal`, its
file, and its form: for each row the recorder is reset to refuse the index
given; `FS.changes` is read; `testCaller` makes the call; it returns
`vfs.ErrNXIO` unless the row says otherwise; the recorder holds exactly the
row's requests, each with `Caller` `testCaller`; `FS.changes` is what it was
and `FS.dirty` is false; and the row's check that nothing changed holds.

**W2's tree.** With the recorder refusing nothing, `testCaller` makes, and
then `Sync`s:

| Name | Made by | Its `Object` |
|---|---|---|
| the root | `uc43Config` | `R`: `{vfs.TypeDir, 0o755, 501, 20}` |
| `d` | `Mkdir(root, "d", {Mode: 0o750})` | `D`: `{vfs.TypeDir, 0o750, 501, 20}` |
| `d2` | `Mkdir(root, "d2", {Mode: 0o1770})` | `D2`: `{vfs.TypeDir, 0o1770, 501, 20}` |
| `d/f` | `Create`, no `Mode`, 100 bytes written at 0 | `F`: `{vfs.TypeReg, 0o644, 501, 20}` |
| `d/e` | `Create(d, "e", {Mode: 0o664}, true)`, never written | `E`: `{vfs.TypeReg, 0o664, 501, 20}` |
| `d/ln` | `Symlink` to `"f"`, no `Mode` | `LN`: `{vfs.TypeLnk, 0o777, 501, 20}` |
| `d/dd` | `Mkdir(d, "dd", {Mode: 0o711})`, left empty | `DD`: `{vfs.TypeDir, 0o711, 501, 20}` |
| `d2/h` | `Create(d2, "h", {Mode: 0o640}, true)` | `H`: `{vfs.TypeReg, 0o640, 501, 20}` |
| `d2/v` | `Create(d2, "v", {Mode: 0o600}, true)` | `V`: `{vfs.TypeReg, 0o600, 501, 20}` |

No two of the nine are equal, so a request that names the wrong one cannot
pass for the right one. `d2`'s mode carries `0o1000`, so the rows also show
that the bit reaches the policy in `Object`, in `Dst` and in `Entry`. The
recorder allows whatever it does not refuse, so no mode here bears on a
verdict.

**W2's rows.** Below, "no `Entry`" and "no `Replaced`" mean the field is
nil, and a field given by a letter points to an `Object` equal to it. Every
request not listed with one has both fields nil.

- Unchanged but for `D`'s value: `Lookup(d, "f")`, `[OpLookup on D]`; both
  `ReadDir(d, …)` rows, `[OpReadDir on D]`; `Create(d, "new", …)`,
  `Mkdir(d, "sub", …)` and `Symlink(d, "s", "f", …)`, `[OpAddEntry on D]`,
  each with its `Lookup` then `vfs.ErrNoEnt`; the `Read` rows, on `F` and on
  `E`; and `Write(f, …)`, `[OpWrite on F]`.
- `Remove(d, "f")`, index 0: `[OpRemoveEntry on D with Entry F]`; `f` is
  still found, and `GetAttr` of its handle succeeds.
- `Remove(d, "ln")`, index 0: `[OpRemoveEntry on D with Entry LN]`.
- `Rmdir(root, "d2")`, index 0: `[OpRemoveEntry on R with Entry D2]`; `d2`
  is still found.
- `Rmdir(d, "dd")`, index 0: `[OpRemoveEntry on D with Entry DD]`.
- `Remove(d, "nope")`, index 0: `vfs.ErrNXIO` with
  `[OpRemoveEntry on D, no Entry]`. And index −1: `vfs.ErrNoEnt`, with the
  same one request.
- `Rename(d, "f", d2, "g")`, index 0:
  `[OpRename with Object D, Dst D2, Entry F, no Replaced]`; `f` is still
  found in `d`, and `Lookup(d2, "g")` is `vfs.ErrNoEnt`.
- `Rename(d2, "h", d, "i")`, index 0:
  `[OpRename with Object D2, Dst D, Entry H, no Replaced]`: the other
  direction.
- `Rename(d, "f", d2, "v")`, index 0:
  `[OpRename with Object D, Dst D2, Entry F, Replaced V]`; `Lookup(d2, "v")`
  still finds `v`, by its file id, and `f` is still found in `d`.
- `Rename(d2, "v", d, "e")`, index 0:
  `[OpRename with Object D2, Dst D, Entry V, Replaced E]`.
- `Rename(d, "f", d, "g")`, index 0:
  `[OpRename with Object D, Dst D, Entry F, no Replaced]`.
- `Rename(root, "d2", d, "dd")`, index 0:
  `[OpRename with Object R, Dst D, Entry D2, Replaced DD]`: a directory
  moved onto an empty directory.
- `Rename(d, "f", d, "f")`, index 0: `vfs.ErrNXIO` with
  `[OpRename with Object D, Dst D, no Entry, no Replaced]`. And index −1:
  nil, with the same one request.
- `Rename(d, "nope", d2, "v")`, index −1: `vfs.ErrNoEnt` with
  `[OpRename with Object D, Dst D2, no Entry, no Replaced]`; `v` is still
  found.

No row changes the tree: every refused call changes nothing, and of the
removal and rename rows the three that the recorder allows are a rename
onto itself and two calls that stage E answers `vfs.ErrNoEnt`. `SetAttr` is
still ADR 0024 §7's S4.

The two tests that share W2's tree,
`TestARequestedUIDAtCreationGoesThroughOpChownAndARefusalIsIgnored` and
`TestGetAttrReadLinkFSStatCommitAndAccessConsultNoPolicy`, take `d`'s
`Object`, `D`, from it; nothing else in them changes.

**What a swap now fails.**

- `Object` and `Dst` swapped in `Rename`: both cross-directory rows, each of
  which names `D` and `D2` in a different order; and K6 (§8), under
  `Unix()`.
- `Entry` and `Replaced` swapped, or one built from the other's inode: the
  two rows that replace `v` and `e`.
- `Rmdir`, or `Remove`, asking about the entry in place of the parent:
  `Rmdir(root, "d2")`, whose `Object` must be `R` and not `D2`, and every
  other removal row, whose `Object` and `Entry` differ.
- The directory given as its own entry, or the entry left out: every
  removal and rename row that names an `Entry`.
- Any of them under `Unix()`, where it changes what a caller is allowed:
  K5 and K6, and W4's two new rows.

**W4 gains two rows**, both characterisations, which pass at `273f010` and
after the change, by reading (Assumption 17). Its setup gains, by
`testCaller`: `open`, a directory `0o777`, holding `shut`, a directory
`0o700`; and `wide`, a directory `0o777`, made in `ro` before `ro` is set to
`0o555`. Under `OpRemoveEntry`:

- `sa24Other`'s `Rmdir(ro, "wide")` returns `vfs.ErrAcces`: the parent's
  mode decides, where `wide`'s own would allow.
- `sa24Other`'s `Rmdir(open, "shut")` returns nil: the parent's mode
  decides, where `shut`'s own would refuse.

They close #148's second gap, that W4's `Rmdir` rows never used an entry
whose verdict differs from its parent's. Its first gap, source against
target, cannot be closed by a row of W4, since without the sticky bit
`Unix()`'s `OpRename` row is the same for both directories; K6 closes it,
through `vfs.FS` under `Unix()`, with the bit. W4 gains no sticky row and
keeps its name: it stays the characterisation of the rows as ADR 0014 wired
them, with #148's two.

### 8. The test surface

Every name is spelled as in Go. test-author cannot read the non-test files
of `internal/policy`, `internal/blobfs` or `internal/nfs`, so what a test
may use is here, in ADR 0014 §5 and §11, in ADR 0007 §5, in
`internal/vfs/vfs.go`, or in a `_test.go` file. The tests are K1 to K10,
with W2 and W4 as §7 gives them.

**What becomes pinned.** ADR 0014 §11's U4, whole: with `Entry` and
`Replaced` nil, as the directory check alone, whatever the bit; and with
either set, as §1's rows. U3 where `Permits` allows, U5 and U6 stay
unpinned, and no test here relies on them: no `Read`, `Write` or size is
asked of a symlink; `Access` and `AccessBits` are asked of no directory that
has read without search or write without search; and no time is set by a
caller who is not the owner.

**`internal/policy`** (package `policy_test`). A new file,
`internal/policy/sticky_test.go`, with helpers of its own prefixed `stk`,
and additions to `policy_test.go`. Both use `policy_test.go`'s fixtures: the
callers `callerRoot`, `callerOwner`, `callerOwnerInGroup`, `callerGroup`,
`callerSupp` and `callerOther`, whose uids are 0, 1000, 1000, 2000, 3000 and
4000; `fileUID` and `fileGID`; `p2Callers` and `p2Modes`; `mkObj`, whose
objects are owned by `1000:100`, and `foreignDir`, whose directories are
owned by `9999:999`; `mkReq` and `mkRename`; `oraclePermits`; `limiter`;
`opName`; `unixCase`, `unixCases`, `p5Cases` and `checkUnix`; and
`owner_access_test.go`'s `oaAllTypes`. Every refusal is compared with `==`
against the bare `vfs.Status`.

- **K1. The two rows, exhaustively** (`TestTheStickyRowsExhaustively`).
  `Unix().Check` is compared with an oracle written from §1's rows, with
  `oraclePermits` standing for `Permits`, over at least:
  - *`OpRemoveEntry`*: every caller of `p2Callers()`; as `Object`, for every
    mode of `p2Modes()`, `mkObj(vfs.TypeDir, mode)` and `foreignDir(mode)`,
    and for every type of `oaAllTypes` but `vfs.TypeDir`,
    `mkObj(type, 0o1777)` and `mkObj(type, 0o777)`; as `Entry`, nil, and a
    pointer to `policy.Object{Type: vfs.TypeReg, Mode: 0o644, UID: u, GID:
    fileGID}` for each `u` of 0, 1000, 2000, 3000, 4000, 9999 and
    4294967295.
  - *`OpRename`*: every caller of `p2Callers()`; as `Object` and as `Dst`,
    independently, `mkObj(vfs.TypeDir, mode)` and `foreignDir(mode)` for
    each mode of `0o777`, `0o770`, `0o707`, `0o077`, `0o300`, `0o030`,
    `0o003`, `0o555` and 0, each with and without `0o1000`, and of `0o7777`
    and `0o6777`; as `Entry` and as `Replaced`, independently, nil and the
    seven pointers above.
- **K2. The rows by name.** `unixCases()`, P4's table, gains the requests
  below, so that P10 to P13 ask them too. An entry "of uid u" is a pointer
  to a regular file of mode `0o644` owned by `u`.
  - `OpRemoveEntry`, nil: `callerOther`, `foreignDir(0o1777)`, an entry of
    uid 4000 (its own); `callerOwner`, `mkObj(vfs.TypeDir, 0o1700)`, an
    entry of uid 4000 (the directory's owner); `callerRoot`,
    `foreignDir(0o1000)`, an entry of uid 9999; `callerOther`,
    `foreignDir(0o777)`, an entry of uid 9999 (no bit); `callerOther`,
    `foreignDir(0o1777)`, no entry; `callerGroup`,
    `mkObj(vfs.TypeDir, 0o1770)`, an entry of uid 2000; `callerOther`,
    `foreignDir(0o6777)`, an entry of uid 9999 (set-ID bits are not the
    bit).
  - `OpRemoveEntry`, `vfs.ErrPerm`: `callerOther`, `foreignDir(0o1777)`, an
    entry of uid 9999; `callerOther`, `mkObj(vfs.TypeDir, 0o1777)`, an entry
    of uid 1000; `callerGroup`, `mkObj(vfs.TypeDir, 0o1770)`, an entry of
    uid 1000; `callerSupp`, `mkObj(vfs.TypeDir, 0o1770)`, an entry of uid
    2000; `callerOther`, `foreignDir(0o1777)`, an entry of uid 0;
    `callerOther`, `foreignDir(0o7777)`, an entry of uid 9999.
  - `OpRemoveEntry`, `vfs.ErrAcces`, the mode before the rule:
    `callerOther`, `mkObj(vfs.TypeDir, 0o1770)`, an entry of uid 1000;
    `callerOther`, `foreignDir(0o1775)`, an entry of uid 4000 (owning the
    entry gives no write); `callerOwner`, `mkObj(vfs.TypeDir, 0o1577)`, an
    entry of uid 1000 (the directory's owner is held to its mode).
  - `OpRename`, nil, with source, target, entry and replaced in that order:
    `callerOther`, `foreignDir(0o1777)`, `foreignDir(0o777)`, uid 4000,
    none; `callerOther`, `foreignDir(0o777)`, `foreignDir(0o1777)`, uid
    9999, none; `callerOther`, `foreignDir(0o1777)` twice, uid 4000, uid
    4000; `callerOwner`, `foreignDir(0o777)`, `mkObj(vfs.TypeDir, 0o1700)`,
    uid 1000, uid 4000 (the target's owner); `callerRoot`,
    `foreignDir(0o1000)` twice, uid 9999, uid 9999; `callerOther`,
    `foreignDir(0o777)` twice, uid 4000, uid 9999 (no bit);
    `callerOther`, `foreignDir(0o1777)` twice, none, none.
  - `OpRename`, `vfs.ErrPerm`: `callerOther`, `foreignDir(0o1777)`,
    `foreignDir(0o777)`, uid 9999, none, which is the second nil row with
    its directories exchanged; `callerOther`, `foreignDir(0o777)`,
    `foreignDir(0o1777)`, uid 4000, uid 9999; `callerOther`,
    `foreignDir(0o1777)` twice, uid 9999, none; `callerGroup`,
    `mkObj(vfs.TypeDir, 0o1770)` twice, uid 2000, uid 1000; `callerOther`,
    `foreignDir(0o1777)` twice, uid 9999, uid 9999.
  - `OpRename`, `vfs.ErrAcces`, the mode before the rule: `callerOther`,
    `foreignDir(0o1777)`, `foreignDir(0o755)`, uid 9999, none;
    `callerOther`, `foreignDir(0o777)`, `foreignDir(0o1775)`, uid 4000, uid
    9999.

  `p5Cases()`, P5's table, gains three, each `vfs.ErrNotDir`:
  `OpRemoveEntry` by `callerOther` on `mkObj(vfs.TypeReg, 0o1777)` with an
  entry of uid 9999; `OpRename` by `callerOther` from `foreignDir(0o1777)`,
  with an entry of uid 9999, to `mkObj(vfs.TypeReg, 0o777)`; and `OpRename`
  by `callerOther` from `mkObj(vfs.TypeReg, 0o777)` to `foreignDir(0o1777)`,
  with a replaced entry of uid 9999.
- **K3. What the rule reads, and what the other rows do not.** Two parts.
  - `TestTheStickyRuleReadsOnlyTheEntrysOwner`, in `sticky_test.go`. For
    every caller of `p2Callers()`, each of `mkObj(vfs.TypeDir, 0o1777)`,
    `foreignDir(0o1777)` and `foreignDir(0o777)`, and each owner uid of
    1000, 4000 and 9999: `OpRemoveEntry`'s answer is the same for every
    `Entry` of that uid, over every type of `oaAllTypes`, the modes 0,
    `0o644`, `0o1777`, `0o7777` and 4294967295, and the gids 0, `fileGID`,
    400 and 4294967295. The same for `OpRename`'s `Entry`, with that
    directory as `Object` and `foreignDir(0o777)` as `Dst`, and for its
    `Replaced`, with that directory as `Dst`, `foreignDir(0o777)` as
    `Object` and no `Entry`.
  - P13, `TestUnreadFieldsChangeNothing`, gains two loops over every P4
    and P5 request: `Entry` set to nil and to pointers to several objects
    changes no answer unless the `Op` is `OpRemoveEntry` or `OpRename`, and
    `Replaced` likewise unless the `Op` is `OpRename`.
- **K4. No check writes through the pointers**
  (`TestNoCheckWritesThroughEntryOrReplaced`). For every K2 request with a
  field set: after `Unix().Check`, the `Check` of
  `All(Unix(), Func(allow))` and that of `Func(allow)`, what `Entry` and
  `Replaced` point to equals a copy taken before the call.
- **P2, P3 and P9** do not change. P2 already asks `Permits` of every mode
  with `0o7000` set, and P9 `AccessBits` of the same, so they are the pins
  that neither reads the bit (§5).

**`internal/blobfs`** (package `blobfs`), in one new file,
`internal/blobfs/sticky_test.go`, with helpers of its own prefixed `stk`.
It uses no name the code lacks at `273f010`, and no helper that this PR
adds to another test file, so that it builds on that commit's tree. The
harness is ADR 0011 §7's, as ADR 0014 §11 and ADR 0025 §6 apply it: a
`*store.Local` under `t.TempDir()` behind an unarmed `uc43Store`, through
`uc43Setup`; `uc43Config`; every `New` and every `FS` call in `uc43Go`,
waited for under `uc43Bound`; nothing sleeps, polls or runs in parallel;
`FS.dirty` and `FS.changes` read holding `FS.mu` for reading, through
`sa24Read`. `Unix()` is left in place on every mount, and no field of `FS`
is written. A test may reuse `uc43Env` and its methods,
`uc43WriteForeignRoot` and `uc43Writer`; the `pw` helpers, `pwMember` and
`pwAllAccess`; the `sa24` helpers and `sa24Other`; `sp24Root`; the `oa`
helpers of `owner_access_test.go`; `dg24Setup`, `dg24Env` and `dg24Ghost`;
and `testCaller`. Of unexported non-test names it uses one,
`mountDiverged`, through `uc43Env.mode`.

The callers are `testCaller` (`vfs.Caller{UID: 501, GID: 20}`), who owns
the root directory; `sa24Other` (`vfs.Caller{UID: 502, GID: 30}`);
`pwMember` (`vfs.Caller{UID: 503, GID: 20}`); and `sp24Root`
(`vfs.Caller{UID: 0}`). Facts a test may rely on, beside ADR 0014 §11's and
ADR 0024 §7's: an object is owned by the uid and gid of the caller that
created it, and a caller creates in any directory whose mode gives its
class write and search; `sp24Root`'s creation with a `UID` makes an object
of that owner, and its `SetAttr` with a `UID` changes an owner; a `Mode`
given to `Mkdir` is applied masked to `0o7777`, so `0o1777` makes a sticky
directory, and `GetAttr` reports `0o1777`; a refused call leaves
`FS.changes` and `FS.dirty` as they were; and a `Rename` of a name onto
itself returns nil and leaves them too.

**The tree** K5 and K6 each build afresh. `testCaller` makes, in the root:
`tmp`, a directory `0o1777`; `open`, a directory `0o777`; and `grp`, a
directory `0o1770`. Then: `sa24Other` makes, in `tmp`, the file `a`, the
empty directory `da`, the symlink `la` to `"a"`, and the directory `full`
holding a file `x`; `pwMember` makes the file `b` in `tmp`; `testCaller`
makes the file `t` in `tmp` and the file `g1` in `grp`; `sp24Root` makes
the file `g2` in `grp` with `UID` 502; `sa24Other` makes the files `o1` and
`o2` in `open`, and `pwMember` the file `m1` there. Then `Sync`, after
which `FS.dirty` is false.

- **K5. Removal** (`TestAStickyDirectorysEntriesAreRemovedOnlyByRootOrAnOwner`;
  §1; #94's first done-when).
  - `vfs.ErrPerm`: `pwMember`'s `Remove(tmp, "a")`; `sa24Other`'s
    `Remove(tmp, "b")`; `sa24Other`'s `Remove(tmp, "t")`, the directory
    owner's own file; `pwMember`'s `Rmdir(tmp, "da")`; `pwMember`'s
    `Remove(tmp, "la")`; and `pwMember`'s `Remove(grp, "g1")`, a group
    member with write and search.
  - `vfs.ErrPerm`, before stage E: `pwMember`'s `Rmdir(tmp, "a")`, not
    `vfs.ErrNotDir`; `Remove(tmp, "da")`, not `vfs.ErrIsDir`; and
    `Rmdir(tmp, "full")`, not `vfs.ErrNotEmpty`.
  - Another status: `sa24Other`'s `Remove(grp, "g1")` and its
    `Remove(grp, "g2")`, of the entry it owns, return `vfs.ErrAcces`; and
    `pwMember`'s `Remove(tmp, "nope")` returns `vfs.ErrNoEnt`.
  - After all of those: `Lookup` still finds every entry, by its file id;
    `FS.changes` is what it was after the setup's `Sync` and `FS.dirty` is
    false; and a `Sync` returns nil with no store call recorded since the
    setup's `Sync` returned (§4).
  - nil, each followed by `vfs.ErrNoEnt` from `Lookup` of the name:
    `sa24Other`'s `Remove(tmp, "la")` and `Rmdir(tmp, "da")`, the entry's
    owner; `testCaller`'s `Remove(tmp, "b")`, the directory's owner;
    `sp24Root`'s `Remove(tmp, "a")`; and `pwMember`'s `Remove(open, "o1")`,
    another's entry in a directory without the bit.
- **K6. Rename** (`TestARenameInAStickyDirectoryIsHeldToTheStickyRule`;
  §1, §2; the done-when's "for the moved entry and for an existing
  target").
  - `vfs.ErrPerm`: `pwMember`'s `Rename(tmp, "a", tmp, "a9")`;
    `Rename(tmp, "a", open, "x")`, out of a sticky source;
    `Rename(tmp, "b", tmp, "a")`, its own entry onto another's;
    `Rename(open, "m1", tmp, "a")`, its own entry from a plain source onto
    another's in a sticky target; and `Rename(tmp, "a", tmp, "da")`, not
    `vfs.ErrIsDir`. After them every entry named is still found where it
    was, and `FS.changes` is unchanged.
  - Other answers, each with `FS.changes` unchanged: `pwMember`'s
    `Rename(tmp, "t", tmp, "t")`, another's entry onto itself, returns nil;
    its `Rename(tmp, "nope", tmp, "t")`, no source and another's target,
    returns `vfs.ErrNoEnt`; and `sa24Other`'s `Rename(tmp, "a", grp, "y")`,
    its own entry into a directory it cannot write, returns `vfs.ErrAcces`.
  - nil: `pwMember`'s `Rename(open, "o1", tmp, "in")`, another's entry into
    a sticky target that replaces nothing, which is the second refused call
    with its directories exchanged, after which `Lookup(tmp, "in")` finds
    `o1`'s file id; `pwMember`'s `Rename(tmp, "b", open, "o2")`, its own
    entry onto another's in a plain target; `sa24Other`'s
    `Rename(tmp, "a", tmp, "a2")`, the entry's owner; `testCaller`'s
    `Rename(tmp, "la", tmp, "la2")`, the directory's owner; and
    `sp24Root`'s `Rename(tmp, "la2", open, "r")`.
- **K7. The mode and the owners as they stand**
  (`TestTheStickyRuleReadsTheModeAndTheOwnersAsTheyStand`; §1, §4).
  `testCaller` makes `tmp`, `0o1777`; `sa24Other` makes `a` and `c` in it,
  and `testCaller` makes `t`. `pwMember`'s `Remove(tmp, "a")` returns
  `vfs.ErrPerm`. `testCaller`'s `SetAttr(tmp, {Mode: 0o777})` returns nil,
  and `pwMember`'s `Remove(tmp, "a")` then returns nil. `testCaller`'s
  `SetAttr(tmp, {Mode: 0o1777})` returns nil, and `pwMember`'s
  `Remove(tmp, "c")` returns `vfs.ErrPerm`. `sp24Root`'s
  `SetAttr(c, {UID: 503})` returns nil, and `pwMember`'s `Remove(tmp, "c")`
  then returns nil. Then `Sync` returns nil, and on a remount `GetAttr` of
  `tmp` reports mode `0o1777`, `sa24Other`'s `Remove(tmp, "t")` returns
  `vfs.ErrPerm`, and `testCaller`'s returns nil.
- **K8. The mount gate comes before the rule**
  (`TestTheMountGateComesBeforeTheStickyRule`; §2), in two subtests. Each
  begins on a writable mount, where `testCaller` makes `tmp`, `0o1777`,
  `sa24Other` makes `a` and `p` in it, `pwMember`'s `Remove(tmp, "p")`
  returns `vfs.ErrPerm`, which shows that the row would refuse it, and a
  `Sync` returns nil.
  - *Read-only.* A second mount of the same bucket is made with
    `Config.ReadOnly` set, and the first is not used again. On it,
    `pwMember`'s `Remove(tmp, "a")`, `Rmdir(tmp, "a")` and
    `Rename(tmp, "a", tmp, "z")`, and `sa24Other`'s `Remove(tmp, "a")`,
    return `vfs.ErrROFS`, and `a` is still found.
  - *Diverged.* As ADR 0025 §6's A9 diverges a mount. Then `pwMember`'s
    `Remove(tmp, "a")` and `Rename(tmp, "a", tmp, "z")`, and `sa24Other`'s
    `Remove(tmp, "a")`, return `vfs.ErrStale`.
- **K9. An entry that names no inode is absent to the rule**
  (`TestAnEntryThatNamesNoInodeIsAbsentToTheStickyRule`; §2), a
  characterisation, which passes before the change and after. On
  `dg24Setup`'s mount, whose root holds `ghost`, naming no inode:
  `testCaller`'s `SetAttr(root, {Mode: 0o1777})` returns nil, and
  `sa24Other` makes the file `mine` in the root. Then `sa24Other`'s
  `Remove(root, "ghost")`, `Rmdir(root, "ghost")` and
  `Rename(root, "ghost", root, "x")` return `vfs.ErrNoEnt`, with
  `FS.changes` unchanged across the three; and its
  `Rename(root, "mine", root, "ghost")` returns nil, after which
  `Lookup(root, "ghost")` finds `mine`'s file id.

**`internal/nfs`** (package `nfs`), in one new file,
`internal/nfs/sticky_test.go`, with helpers of its own prefixed `stk`,
which also builds at `273f010`. It drives the server over a real TCP
socket, as `owner_access_test.go` does, and reuses `startServer`, `dial`,
`mountRoot`, `rpcClient` and its `uid` and `gid` fields, `nfsCreate`,
`nmfsCreate`, `nmfsLookup`, `nmfsWantEnd`, `skipWccData`, `skipPostOpAttr`,
`oaGetAttr`, and the constants `nmfsOK`, `nmfsNoEnt` and `nmfsGuarded`. Of
unexported non-test names it uses the procedure numbers `procMkdir`,
`procRemove`, `procRmdir` and `procRename`, and `dontChange`. The
encodings, as `internal/nfs` writes and reads them at `273f010`:

- `MKDIR`'s arguments are the directory's handle, the name and a `sattr3`
  (ADR 0007 §5); its reply is the status, then for `NFS3_OK` a
  `post_op_fh3`, a `post_op_attr` and one `wcc_data`, and for any other
  status one `wcc_data`.
- `REMOVE`'s and `RMDIR`'s arguments are the directory's handle and the
  name; the reply is the status and one `wcc_data`, whatever the status.
- `RENAME`'s arguments are the source directory's handle and name, then the
  target's; the reply is the status and two `wcc_data`, whatever the
  status.
- `NFS3ERR_PERM` is 1 and `NFS3ERR_ACCES` is 13 (RFC 1813 §2.6).

- **K10. On the wire the refusal is `NFS3ERR_PERM`**
  (`TestNFSAStickyDirectoryRefusesWithNFS3ERRPERM`; §3; #94's second
  done-when). The owner, uid 501, makes `tmp` in the root with a `MKDIR`
  whose `sattr3` sets mode `0o1777`, and `GETATTR` reports mode `0o1777`;
  it also creates `k` in the root. A client sending `502:30` creates `a`
  and `a2` in `tmp` and makes the directory `da` there. A client sending
  `503:20` creates `b` in `tmp`. Then, from `503:20`:
  - `REMOVE` of `tmp/a`, `RMDIR` of `tmp/da`, `RENAME` of `tmp/a` to
    `tmp/z`, and `RENAME` of `tmp/b` onto `tmp/a` each answer status 1,
    with one `wcc_data`, or two for `RENAME`, and nothing after;
  - `LOOKUP` of `a` and of `da` in `tmp` then answers `NFS3_OK`, and of `z`
    `NFS3ERR_NOENT`;
  - `REMOVE` of `k` in the root, whose mode `0o755` gives the group no
    write, answers status 13.

  Then `REMOVE` of `tmp/a` from `502:30`, `RMDIR` of `tmp/da` from
  `501:20` and `REMOVE` of `tmp/a2` from a client sending uid 0 each
  answer `NFS3_OK`.

**Existing tests** (Assumption 18). No assertion of an existing test
changes but W2's, by §7: its rows for `Remove`, `Rmdir` and `Rename`, and
the `Object`s its tree gives `d`, `d2` and `e`.

- `internal/policy/policy_test.go`: `unixCases()` and `p5Cases()` gain K2's
  rows. `TestUnreadFieldsChangeNothing` gains K3's two loops.
  `sampleRequest()`, "a request with every field set", sets `Entry` and
  `Replaced`; `cloneRequest` copies what they point to and `sameRequest`
  compares it, so that P8 still shows a `Func` given the request unchanged.
  The header, the doc comment of `unixCases()` and its comments on the
  `OpAddEntry` and `OpRemoveEntry` rows and on the `OpRename` rows say that
  U4 is unpinned and that no mode has `0o1000` set; they are to say that U4
  is pinned since this ADR, and where.
- `internal/blobfs/policy_wiring_test.go`: W2's tree, its rows and its
  comparison of requests, which gains `Entry` and `Replaced`, by §7; the
  `Object` of `d` in the two tests that share the tree; W4's two rows and
  their setup; and the comments that list the sticky bit among the rows
  left unpinned, which are to say that U4 is pinned since this ADR and
  tested in `sticky_test.go`, and that W4 still has no sticky row.
- `internal/blobfs/setattr_policy_test.go`: `sp24Recorder`'s `check`, which
  copies `Caller.GIDs`, copies what `Entry` and `Replaced` point to as
  well, so that a recorded request is a value and nothing the `FS` does
  afterwards can change it.
- These were read against §1 and §2 and stand as they are. W3 allows
  everything, and its `Rename`, `Remove` and `Rmdir` succeed as before. W5
  asks no removal. A3 and A8 of ADR 0025 ask `OpRemoveEntry` and
  `OpRename` with no entry, or of directories without the bit. ADR 0024
  §7's D1 and D2 and ADR 0017 §6's tests remove and rename as `testCaller`
  in directories without the bit. No existing test asks `OpRemoveEntry` or
  `OpRename` of a directory whose mode has `0o1000` set, and none makes
  such a directory through an `FS` or over the wire.

**Red run** (Assumption 18). `internal/blobfs/sticky_test.go` and
`internal/nfs/sticky_test.go` build on `273f010`'s tree, with no other file
of this PR, where, read from the code and not run:

- K5 fails on each call it expects `vfs.ErrPerm` of, which there returns
  nil and removes the entry, or, where an earlier such call has already
  removed it, `vfs.ErrNoEnt`; and on what depends on those. Its
  `vfs.ErrAcces` calls and its owners' and root's calls pass.
- K6 fails on each call it expects `vfs.ErrPerm` of, and passes its rename
  onto itself, its missing source, its `vfs.ErrAcces` and, where the
  entries are still there, its allowed calls.
- K7 fails on each call it expects `vfs.ErrPerm` of, the remount's
  included, and on what depends on those.
- K8 fails, in each subtest, on the writable mount's `vfs.ErrPerm` alone:
  the gate's answers are what they were.
- K9 passes.
- K10 fails on its four status-1 replies, each of which is `NFS3_OK` there,
  or `NFS3ERR_NOENT` once the entry is gone, and on the `LOOKUP`s that
  follow; its status 13 passes.

The other test files of this PR name `Entry` and `Replaced`, which
`policy.Request` lacks at `273f010`, so their packages do not build there
and have no red run: K1 to K4, P13's loops and W2's revised rows are
checked first against coder's change. After the change every test passes.

**What only review can show:**

- that `internal/policy/policy.go` and `internal/blobfs/fs.go` are the only
  non-test files changed, and in `fs.go` only `unlink`, `Rename` and their
  comments;
- that lines 3 and 4 compare `Caller.UID` with 0, with the directory's
  `UID` and with the entry's `UID`, read `Mode & 0o1000` of the directory,
  and read nothing else;
- that `Permits`, `AccessBits` and every other row are what they were;
- that the rows still take no lock, log nothing, allocate nothing when they
  allow, and write nothing through `Entry` or `Replaced`;
- that both lookups and the request are made in the hold of `FS.mu` for
  writing that makes the change, with nothing changed before the request,
  and that neither lookup dereferences an inode the table lacks;
- that `unlink` and `Rename` still perform `Mutate` holding the locks their
  `trace.tsv` rows give (§9);
- how a kernel client behaves, which needs a mount and a human
  (Assumption 15).

### 9. The model, the trace and the vocabulary

No TLA+ module is affected, and none is added (Assumption 19). ADR 0013 §1
gives permission rules to exhaustive tests of the policy and to security
review, as "per-request rules, not interleavings", and K1 is that test.
`docs/tla/CommitProtocol.tla` models no permission: its `Mutate` is guarded
by `Mutable`, the mount gate, which stays in front of the rows (§2), and a
refusal is no step of it.

`docs/tla/trace.tsv` names `FS.unlink` and `FS.Rename`, both as `Mutate`
holding `FS.mu` for writing, and this PR edits both. Neither row changes:
the lookup and the request come before the action and are not it, the hold
is the one the row names, and what decides the action is untouched, so
`decided` stays as it is for both. But a change to traced code is ADR 0013
§8's refactor, so, as ADR 0014's Assumption 49 applied it:

- once coder's commit is on the branch, the architect reads both rows
  against it: that each function is still declared on `*FS` in
  `internal/blobfs/fs.go`, keeps its `TLA+: … Mutate` line, calls
  `mutable()` before its lock, takes `FS.mu` for writing with a deferred
  unlock, and makes its change and its `markDirty` in that hold, after the
  request;
- the architect then moves the module's `Checked against` line,
  `docs/tla/README.md`'s statement of it and `docs/vocabulary.md` V3's from
  `555fc64` to that commit, on the same branch;
- that header edit is a change under `docs/tla/`, so CLAUDE.md's merge bar
  asks the session for `bash scripts/tla.sh full`, unpiped, with
  `git status --porcelain` the same before and after and a last line that
  reads `tla.sh: N of N passed`, N being the rows of
  `docs/tla/manifest.tsv`. The module changes in its header comment alone,
  so TLC's outcome cannot differ from the run that calibrated those rows.

That pass was not made with this ADR's first commit, `8c4bc42`, which had no
commit of coder's to read and changed nothing under `docs/tla/`.

**The `Checked against` pass** was made on 2026-11-20 against coder's commit
`afbda93`, read and not run (Assumption 25). Both rows hold:

- `FS.unlink` is declared on `*FS` in `internal/blobfs/fs.go` and keeps its
  `TLA+: docs/tla/CommitProtocol.tla Mutate` line. It calls `mutable()` and
  `validName` before its lock, takes `FS.mu` for writing by `Lock` with a
  deferred `Unlock`, and in that hold resolves the directory, looks the name
  up, asks its one request, and only then makes its change: the entry's
  removal, the directory's `touchM`, `relink`, and `markDirty`. Its branch
  for an entry that names no inode still returns before any change, which
  is what its row's `decided`, ADR 0024 section 3, pins.
- `FS.Rename` is declared on `*FS` in the same file and keeps its
  `TLA+: … Mutate` line. It calls `mutable()`, and `validName` twice, before
  its lock, takes `FS.mu` for writing the same way, and in that hold
  resolves both directories, looks both names up, asks its one request, and
  only then makes its change and its `markDirty`. A refusal, and a rename
  onto itself, return before any change.

Nothing in either changed but the lookups, which now precede the request,
and what the request carries, and both come before the action. So
`trace.tsv` is unchanged, `decided` included. The module's
`Checked against` line, `docs/tla/README.md`'s statement of it and
`docs/vocabulary.md` V3's moved from `555fc64` to `afbda93`. The module
changed in that one line of its header comment and nowhere else, and gained
no `Revised:` line, as it gained none for the passes of ADR 0024 and of
ADR 0014's wiring. So TLC's outcome cannot differ from the run that
calibrated the forty-five rows, and CLAUDE.md's merge bar still asks the
session for `tla.sh full` on the PR, as above.

`docs/vocabulary.md` does not otherwise change. V7 lists `Permits`,
`UnixAllows`, `AccessBits` and the `Op` and `Perm` names, none of which is
added to or renamed; `Request`'s fields are not vocabulary entries; and
`UnixAllows(r)` is still `policy.Unix().Check(r) == nil`, which now refuses
what §1 refuses.

### 10. State, the link and exemptions

Nothing here is state that ADR 0018's state rule reaches, and no exemption
is granted (Assumption 20).

- **No machine.** A directory's mode is an attribute: a value a caller sets
  whole, under `OpChmod`, and that the server never moves between named
  states of its own accord. ADR 0018 §2 keeps values outside the state
  rule. Nothing else is added: no field, no flag, no cache.
- **The policy rule is kept.** The decision is two rows of `Unix()`, pure,
  in `internal/policy`. `blobfs` adds no condition on who the caller is: it
  looks two names up and describes what it found. What it decides for
  itself, that a rename onto itself and a name that names nothing describe
  no entry, are facts about what the request names (ADR 0018 §2), not about
  who asks.
- **No link.** A sticky refusal is a policy's refusal of a request, not a
  transition, and no machine's transition depends on it. Link counts are
  still not security-relevant: `relink`'s changes follow the removals and
  renames these rows allow, after stage E, in the hold in which the row was
  asked (ADR 0018 §5).
- **The register**, `docs/adr/exemptions.md`, does not change. E11 and E12
  stand: the mount gate still comes first, and `ACCESS`'s read-only mask is
  untouched.

### 11. What stays out of this PR

Each of these is its own PR, and none has to move for #94 or #148
(Assumption 22). Titles are as ADR 0014's *Context* and ADR 0025 give them.

- **#97**, "ACCESS grants what the operations then refuse". §5 says what it
  inherits. `AccessBits` is untouched.
- **#98**, "CREATE, MKDIR and SYMLINK apply a requested group from any
  caller". `createEntry` is untouched, and a requested group makes nobody
  an owner.
- **#69**, "SETATTR with a size, and WRITE, accept a symlink's handle". No
  file row changes.
- **#96** and **#99**, on `SETATTR`. `SetAttr` and `internal/nfs` are
  untouched.
- **#100**, "Any caller with write permission can set a file's times to any
  value". `OpSetTimes` is untouched.
- **#101**, "Write and truncate check permission before a wait, not where
  they apply the change". The rule here is read in the hold that makes the
  change (§4), so it adds nothing to #101.
- **#155**, "A WRITE or a size change leaves a file's set-user-ID and
  set-group-ID bits set", and **#154**, whose title and text this ADR was
  not given. Nothing here touches `Write`, `truncate`, `createEntry` or a
  file row, so whatever #154 asks, this PR holds none of it.

### 12. Cross-references, and `CHANGES`

This ADR replaces three parts of ADR 0014, under `docs/adr/README.md`'s rule
for a part a later ADR replaces: §5's `Request`, which gains two fields,
and its bullets for the `OpRemoveEntry` and `OpRename` rows; §6's bullets
for the requests `Remove`, `Rmdir` and `Rename` make; and §11's U4, with
W2's tree and its rows for those three methods, and two rows added to W4.
ADR 0014 gains a `Revised` line, cross-references only, and one sentence in
each of §5, §6 and §11. Its text stays as the record, and every other test
it pins stands.

ADR 0018, ADR 0024 and ADR 0025 are not revised. ADR 0025 §3's list of what
#97 inherits is added to by §5 here, and its text stays true of what it
lists. `docs/DESIGN.md`'s table of ADRs gains this ADR's row, and
`docs/adr/README.md`'s index does.

One `CHANGES` line, proposed for the session to record, without
`BREAKING:` (Assumption 21). The session recorded it as proposed, in
`afbda93`, and by its report the owner decided on 2026-11-16 that it takes
no `BREAKING:` (*Context*, decision 3):

> A directory's sticky bit is honoured: in a directory whose mode has 01000
> set, REMOVE, RMDIR and RENAME of an entry, and RENAME onto an existing
> entry, are refused with NFS3ERR_PERM unless the caller is root or owns
> the directory or that entry, where before any caller with write and
> search permission on the directory could remove, rename or replace
> another user's entries in it; a directory's owner who wants that back
> clears the bit. ACCESS still grants DELETE on such a directory by its
> mode alone, since it names no entry, and the rule judges the uid a
> client sends, as every permission here does.

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation, and Issues #94 and #148**, under the
   owner's 2026-10-10 words, as ADRs 0015 to 0025 were. #94's done-when asks
   for the rule and for a decided error code, and #148's for W2's revision.
   The rest is mine: the interface (§2), the code (§3), what a rename onto
   itself and a name that names nothing ask (§2), that `ACCESS` stays as it
   is (§5), and the `CHANGES` judgement. The two an owner is most likely to
   weigh differently are Assumptions 11 and 21. By the session's report the
   owner decided both, as this ADR has them, on 2026-11-16 (*Context*,
   decision 3).
2. **Read, not run, at `273f010`.** Every statement about the code was
   checked by reading it there; nothing was run. #94's and #148's texts are
   as the session gave them. The titles of #69, #97, #98, #100 and #101 are
   as ADR 0014's *Context* gives them, and #155's as ADR 0025 gives it.
   Their texts were not given, nor were #6's, #96's, #99's or #154's.
3. **Three exemptions and no fourth** (§1). The issue's done-when names the
   three. XBD §4.5's optional fourth, a file the caller may write, is left
   out because the implementation this server is most compared with leaves
   it out, and because it would make a file's own mode decide whether its
   name can be taken, which is the directory's question. No deployment is
   known to want it, and one that did would have no way to ask for it.
4. **Root is uid 0, and an owner is an equality of uids** (§1), which are
   ADR 0014 §5's terms and ADR 0025's Assumption 4, with no exception for
   any uid, 4294967295 included. Linux's third exemption is a capability,
   `CAP_FOWNER`, which an AUTH_SYS credential cannot carry, so a process
   that holds it under another uid is refused here where its own kernel
   would let it through. Uid 0 is what this policy means by root in every
   row.
5. **Every type of entry, and the entry's uid alone** (§1). POSIX's "files"
   and Linux's `__check_sticky` are both blind to the type. Reading no type
   also means the rule holds for an inode whose type a bucket was given and
   no operation makes (ADR 0015, Assumption 15), so there is no type by
   which an entry escapes it.
6. **The directory's mode first, with its own status** (§1). It is XBD
   §4.5's "If a directory is writable", Linux's order, and ADR 0014 §5's
   order within a row. It also means that the sticky lines can only refuse
   a request the row allowed before, never allow one it refused.
7. **`vfs.ErrPerm`** (§3). The done-when asks only that the code be
   decided. Its likely cost is that a user of a macOS client reads
   "Operation not permitted" where a local macOS filesystem would say
   "Permission denied", by the `EACCES` its kernel's check returns. Both
   halves are expected, not read: whether anything between that check and
   the process changes the errno, and how the macOS client maps an NFS
   status, were not fetched.
8. **One request that carries the entries** (§2), rather than a second
   request after the lookup or a new `Op`. A request then describes the
   whole call, so one recorded request shows which directory is the source,
   which the target, what moves and what is replaced, which is #148's
   point, and a later policy that needs two of those facts together has
   them. The existing rows keep their place and their first two lines, so
   the order of every existing refusal is untouched, and W2's lists stay
   one request long.
9. **Pointers, named `Entry` and `Replaced`** (§2). The entry is optional,
   and `vfs.SetAttr`, in the package that holds the contract, already spells
   an optional field as a pointer. Nil is the zero value, so *Growth* holds
   by construction, and a caller cannot describe an entry and forget to say
   that it did, which a value with a separate flag would allow. The costs:
   `blobfs` allocates one or two 16-byte values per removal or rename, which
   is not measured and is small beside the call's own work; and a policy
   could write through a pointer, which the purity rule forbids and K4
   tests. The whole `Object` is carried, and not the uid alone, so that a
   later row can read more of the entry without another field.
10. **The lookup before the request, and the stages' order kept** (§2).
    `vfs.ErrNoEnt` stays stage E's, so no existing refusal moves, and the
    new one sits with the other policy refusals, before every precondition.
    That differs from Linux in one place. Linux answers a directory moved
    into its own subtree `-EINVAL` before it reaches `may_delete`; here the
    row is asked first, as it already is before every other precondition.
    No client is known to depend on which of two refusals such a call
    gets.
11. **A rename onto itself describes no entry** (§2). The alternative is to
    describe the entry and let a caller who does not own it be refused. I
    chose as RFC 1813 words the call, "should perform no action and return
    NFS3_OK", as Linux's server answers it, and so that this ADR changes no
    answer for a call that changes nothing. The cost is that `blobfs`, not
    the row, recognises the case; ADR 0018 §2 puts facts about what a
    request names outside the policy rule, and I read this as one. The
    directories are still asked, as at `273f010`, which is already stricter
    than the RFC's sentence; whether they should be is not decided here.
    Kernel clients are not expected to send the call at all, since Linux's
    VFS returns before it reaches the filesystem; that is read from
    `vfs_rename`, not observed. This was mine until 2026-11-16, when, by the
    session's report, the owner decided that a rename onto itself describes
    no entry and keeps its answer at `273f010` (*Context*, decision 3). The
    reasoning above is kept as the reasoning; the decision is the owner's.
12. **A name whose entry names no inode is absent** (§2), as ADR 0024 §3
    has it for `unlink` and ADR 0017 §2 for `Rename`. Such an entry has no
    owner to compare, and only a snapshot another build or a corruption
    wrote can hold one. So a caller with write and search on a sticky
    directory can rename its own object onto such a name. The alternative,
    to refuse because nobody owns it, would leave the name unusable by
    everyone but root and the directory's owner, for an entry no user made.
13. **A missing source describes no replaced entry** (§2). The call will
    answer `vfs.ErrNoEnt` and replace nothing, and Linux answers `-ENOENT`
    for it before either check. Describing the target would refuse, for an
    entry that is in no danger, a call that is about to fail anyway.
14. **`AccessBits` is unchanged, and what #97 inherits is my reading** of
    its title and of ADR 0025 §3 (§5). #97's text was not read. The one
    alternative that changes `ACCESS`, withholding `vfs.AccessDelete` in a
    sticky directory from a caller who does not own it, would misreport the
    caller's own entries, and a client that believed it would refuse a
    removal the server allows.
15. **What clients do is read, not observed** (*Context*, §6). That a Linux
    client applies the rule itself is read from `may_delete`'s place in
    `vfs_unlink`, `vfs_rmdir` and `vfs_rename`, which every filesystem's
    calls pass through; that the mode and the owners it reads are the ones
    this server reported is from recall of the NFS client, not fetched.
    That a macOS client leaves the rule to the server is read from four
    pieces of two files, and has two gaps: that `vfs_setauthopaque` is what
    sets `MNTK_AUTH_OPAQUE` is from recall, and which `ACCESS` bits the
    kext asks before a removal, on which object, was not read. Nothing was
    run against either client. If the macOS reading is wrong, the rule is
    newer to fewer users than §6 says, and nothing else here changes.
16. **`internal/vfs` is untouched** (§3). `vfs.ErrPerm` exists. `vfs.FS`'s
    doc comments say nothing of permission rules today, and gain nothing:
    the rules are `internal/policy`'s, which the backend that replaces
    `blobfs` reuses (ADR 0014, Assumption 3; ADR 0025, Assumption 17). The
    README's first limitation already says that permissions bind no caller
    that speaks the protocol, and needs no word.
17. **W2 is revised here, by replacement, and W4 gains two rows** (§7).
    #148's first done-when says that ADR 0014 §11's W2 "is revised". I read
    that as satisfied by a later ADR that replaces the part and a
    cross-reference in ADR 0014, which is how ADR 0024 and ADR 0025 changed
    ADR 0014's other parts; if the session reads it as an edit of ADR 0014's
    own text, the rows move there unchanged. Modes alone tell the nine
    objects apart, and every object keeps the owner `501:20`: the recorder
    compares whole `Object`s, so one differing field is enough, and owners
    set through a requested uid would rest on another row's behaviour.
    `e`'s mode moves to `0o664` so that no two objects in the tree are
    equal. That W4's two rows pass at `273f010` is read from `unlink`,
    which asks about the directory it resolved.
18. **The test surface** (§8). Three new files, named so that coder and
    review find them, with the prefix `stk`, since `sk` begins the `skip`
    helpers of `internal/nfs`'s tests. K2's rows go into P4's and P5's own
    tables so that P10 to P13 ask them. K1's grids are minimums, chosen to
    reach every line of both rows with every caller class and to stay near
    a million checks. The two files that carry the red run use no new name
    and no new helper, so that the session can take it on `273f010`'s tree
    before coder's change exists; the rest cannot build there, and I chose
    that over a second round of test-writing after the code. K8 proves the
    rule on a second entry, `p`, so that the gate's assertions do not
    depend on the rule at `273f010`. K9 reuses ADR 0024 §7's recipe, which
    is already proven, and is there because a lookup that dereferenced a
    missing inode would panic under `FS.mu`. K10 is on the wire because the
    code is one of #94's two done-whens, and because no test of
    `internal/nfs` that I read decodes a refused `REMOVE`, `RMDIR` or
    `RENAME` reply whole.
19. **No TLA+, a `Checked against` pass after coder's commit, and
    `tla.sh full` then** (§9). The rows are functions of one request, which
    K1 enumerates. The pass follows ADR 0014's Assumption 49: the line
    names the commit at which the trace was last checked against the code,
    so it moves when traced code is edited, even where no row changes. The
    pass was made on 2026-11-20, against `afbda93` (§9; Assumption 25).
20. **No state, no link, no exemption** (§10). That a mode is a value and
    not a machine is ADR 0018 §2's reading of "named states", applied to an
    attribute a caller sets whole. If a later ADR gave the server a reason
    to set or clear the bit itself, that would be new state under ADR 0018
    §3.
21. **Not breaking, and one `CHANGES` line** (§12). CLAUDE.md prefixes
    `BREAKING:` "when a running deployment needs action to keep working".
    No flag, bucket or mount has to change, and strata serves as before.
    What changes is that some calls are refused, and they are exactly the
    calls the directory's own mode asks to have refused: someone set the
    bit, or copied a tree that had it. The line says how a directory's
    owner gets the old behaviour, by clearing the bit, which is a notice
    and not a step a deployment needs in order to keep working. The other
    reading is defensible: on a client that left the check to the server,
    users of a sticky directory who removed each other's entries, or a
    cleanup job that ran as neither root nor the directory's owner, will
    be refused until the bit is cleared, and that is an action. If the
    owner reads it so, the same line takes the prefix. It is one line
    because it is one change a client can tell. By the session's report the
    owner decided on 2026-11-16, in answer to a question of the session's,
    that the line takes no `BREAKING:` (*Context*, decision 3), and the
    session recorded it without one in `afbda93`. So the judgement above is
    no longer mine alone.
22. **Scope** (§11). The brief named the issues that stay out, and I agree
    for each. None blocks this PR, and this PR makes none of them worse:
    it allows nothing that was refused.
23. **The bit on a non-directory, and who may set it** (§1). POSIX leaves
    the bit on a non-directory unspecified; here it is kept and ignored, as
    before. `OpChmod` still lets a directory's owner, or root, set and
    clear it, and nothing strips it. Some systems let only a privileged
    process set it on a file; no rule of that kind is added, and none is
    needed for this one, since the row reads the bit on directories alone.
24. **Nothing is asked of a moved directory itself** (*What this does not
    decide*). Linux asks write permission on a directory that moves to
    another parent, "we'll need to flip '..'", and POSIX says that such
    permission "may be required". This server keeps no `..` entry, asks
    nothing, and still asks nothing: the rule here is the sticky one, for
    every type alike.
25. **The `Checked against` pass was read, not run, and how far it went**
    (§2, §9). At `afbda93` I read `internal/policy/policy.go` whole, and in
    `internal/blobfs/fs.go` `unlink` and `Rename` whole, with `objectOf`,
    `checkPerm`, `mutable`, `markDirty` and `createEntry`, which are as they
    were at `273f010`. Nothing was run, and I cannot run git: that coder's
    commit changes no other code `trace.tsv` names is the session's account
    of its diff, not a comparison of mine. Every row but those two stands on
    the pass that last read it (ADR 0023 Assumption 23; ADR 0024 §5; ADR
    0014 Assumption 49). Two changes reached `main` between `555fc64` and
    this branch without moving the line. ADR 0025's is in
    `internal/policy/policy.go` alone, which the trace does not name (its
    §7). The fix for #105 is, by `loadChunk`'s own comment and by the header
    of `unverified_known_test.go`, in `loadChunk`, which the trace does not
    name either; I know of no pass made for it, and none was due if that is
    all it touched. Because `loadChunk` sits beside `putChunk`, I read the
    rows for `FS.putChunk` and `FS.flushOpen` against `afbda93` as well, and
    they hold: `flushOpen` takes the file's `openFile.mu` by `Lock` with a
    deferred `Unlock`, and `FS.mu` for writing for the repoint, and
    `putChunk` is called from it, and from `applyTrim` under it, in that
    hold. So "for every action", which `docs/tla/README.md`'s and
    vocabulary V3's statements of the line say, rests on this reading and
    on those earlier passes. A `git diff` of the files `trace.tsv` names,
    from the merge of ADR 0014's wiring to `afbda93`, would confirm it, and
    is the session's to run if it wants more than a reading. That coder's
    helper is named `stickyTakeOut`, and what §2 says of `Rename`'s stage
    E, are read from the same commit.

## Alternatives considered

- **The rule inline in `unlink` and `Rename`.** ADR 0018 §1 forbids it, and
  the rule could then be tested only through an `FS`.
- **A second request after the lookup, or a new `Op` for it.** It leaves
  `Request` with one field and no optional one, but the policy then sees a
  rename as two or three unrelated questions, W2's lists grow, and a new
  `Op` moves P6 and the pinned numbering for a question that is the same
  operation's (Assumption 8).
- **Values with a presence flag each, or a zero `Object` meaning none.**
  The first lets a caller set one and forget the other. The second cannot
  tell "no entry" from an entry all of whose fields are zero, which a
  bucket's unvalidated inode can be (Assumption 9).
- **Carrying the entry's uid alone.** It is all the row reads, but a later
  rule about the entry would need a field of its own.
- **`vfs.ErrAcces` for the refusal.** POSIX allows it and macOS's kernel
  gives it locally. RFC 1813's definitions point the other way, Linux's
  server answers the other, and the two refusals would become
  indistinguishable (§3).
- **The sticky rule before the directory's mode, or after stage E.** The
  first would answer `vfs.ErrPerm` to a caller who cannot write the
  directory at all; the second would move a permission refusal behind
  preconditions, against the stages' order, and tell a caller whether
  another's directory is empty before refusing (Assumptions 6 and 10).
- **Refusing a rename onto itself**, for a caller who does not own the
  entry. It refuses a call that changes nothing and that the RFC says
  should return `NFS3_OK` (Assumption 11).
- **Treating an entry that names no inode as owned by nobody**, and so
  refusing everyone but root and the directory's owner (Assumption 12).
- **POSIX's optional fourth exemption**, a file the caller may write
  (Assumption 3).
- **`ACCESS` withholding `DELETE` in a sticky directory** (Assumption 14).
- **Revising ADR 0014 §11's W2 in place.** The README's rule keeps an
  Accepted decision's text, and the revised rows need this ADR's fields
  (§7).
- **Sticky rows in W4.** W4 is the characterisation of the rows as they
  were wired, and its name says they are unchanged; the rule's own tests
  are K5 and K6.
- **A TLA+ model of the rows.** They are functions of one request
  (Assumption 19).
- **Stripping the bit from non-directories, or letting only root set it.**
  Not needed for the rule, and a change to `OpChmod` nobody asked for
  (Assumption 23).
- **Waiting for #6.** The rule would stay unenforced for every honest
  client, the macOS one above all, to no caller's benefit (§6).

## Consequences

- In a sticky directory, an entry is removed, renamed or replaced only by
  root, the directory's owner or its own owner, through every client. On a
  client that left the check to the server, a shared directory now protects
  its users' entries from one another.
- The refusal has its own status, `NFS3ERR_PERM`, which `REMOVE`, `RMDIR`
  and `RENAME` could not answer before.
- `Request` describes the entries a call takes out, so a later rule about
  them needs no new field, and W2 can see which directory and which entry
  each request names.
- `ACCESS` and the operations differ in one more stated way: `DELETE` on a
  sticky directory is granted to a caller who may delete only its own
  entries (§5).
- U4 is pinned, and with it every directory row of `Unix()`.
- `blobfs` looks names up before it asks its policy in `unlink` and
  `Rename`, and allocates a small value per entry described.
- The rule binds no caller that chooses its uid, as no permission of this
  server does until #6 (§6).
- The backend that replaces `blobfs` gets the same rows through
  `internal/policy`, and has to fill the same two fields.
- Hard to change later: the status, once clients' tools and scripts read
  it; and the two fields' meaning, once policies use them.

## What this does not decide

- #69, #96, #97, #98, #99, #100, #101, #154 and #155 (§11), and the rows U3,
  U5 and U6.
- Root squash and the AUTH_SYS trust boundary (#6, #90, #95).
- Whether `ACCESS`'s `DELETE` should say more than the directory's mode in
  a sticky directory, which is #97's with what §5 leaves it.
- Whether a rename onto itself should ask the two directories at all, where
  RFC 1813 says it "should perform no action and return NFS3_OK"
  (Assumption 11).
- Whether a `Rename` of a directory to another parent should ask write
  permission of that directory, as Linux does (Assumption 24).
- Which status a `Rename` onto an incompatible or non-empty target answers.
  RFC 1813's `RENAME` says the server "should return the error,
  NFS3ERR_EXIST"; this server answers `NFS3ERR_ISDIR`, `NFS3ERR_NOTDIR` or
  `NFS3ERR_NOTEMPTY`, as ADR 0014 §6's stage E lists them. That was found
  while reading the RFC for this ADR and is not touched.
- Whether the bit may be set on a non-directory, and by whom
  (Assumption 23).
- Hard links, and with them what the rule would say of an entry reached by
  another name.
- A test against a kernel client, macOS's above all (Assumption 15).
- What `vfs.FS`'s doc comments say of permissions (Assumption 16).

## Sources

Fetched on 2026-11-16, while this ADR was written, through a tool that
summarises what it fetches and quotes at most about 125 characters at once,
so a longer passage came as consecutive pieces, which are joined here with a
space; it drops a page's markup, and reads a long page 100,000 characters at
a time. Six fetches failed with an error of the tool's own before they
reached a page. Five were of three requests, each of which was repeated
until it came; the sixth asked for characters past the end of its page and
was not repeated. No fetched page held anything addressed to its reader as
an instruction.

- **POSIX.1-2024, XBD §4.5**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/basedefs/V1_chap04.html>,
  which gives itself as The Open Group Base Specifications Issue 8, IEEE Std
  1003.1-2024, fetched twice, since the first answer summarised the
  section. "4.5 Directory Protection", whole, in eight pieces: its
  lead-in, its four list items, of which the fourth has two sentences, and
  its closing sentence on a non-directory, all as *Context* quotes them. The
  tool gave the sections on either side as "4.4 Directory Operations" and
  "4.6 Extended Security Controls". ADR 0014's *Sources* quote the same
  section from a fetch of 2026-10-12, without its closing sentence. (§1;
  Assumptions 3 and 23)
- **POSIX.1-2024, `unlink()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/unlink.html>,
  the same edition. Under "These functions shall fail and shall not unlink
  the file if:", "[EACCES]": "Search permission is denied for a component of
  the path prefix, or write permission is denied on the directory containing
  the directory entry to be removed."; and "[EPERM] or [EACCES]", marked
  XSI, as *Context* quotes it. (§1, §3)
- **POSIX.1-2024, `rmdir()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/rmdir.html>,
  the same edition. Under "The rmdir() function shall fail if:",
  "[EACCES]": "Search permission is denied on a component of the path
  prefix, or write permission is denied on the parent directory of the
  directory to be removed."; and "[EPERM] or [EACCES]", marked XSI, in the
  words `unlink()` has. (§1, §3)
- **POSIX.1-2024, `rename()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/rename.html>,
  the same edition, fetched twice. Under "The rename() and renameat()
  functions shall fail if:", as the tool gave the line with its markup
  dropped, "[EACCES]": "A component of either path prefix denies search
  permission; or one of the directories containing old or new denies write
  permissions; or, write permission is required and is denied for a
  directory pointed to by the old or new arguments."; and "[EPERM] or
  [EACCES]", marked XSI, as *Context* quotes it. From DESCRIPTION: the
  sentence on the same directory entry, as *Context* quotes it; "Write
  access permission is required for the directory containing old and the
  directory containing new. If the old argument points to the pathname of a
  directory, write access permission may be required for the directory named
  by old, and, if it exists, the directory named by new."; and "Otherwise,
  if the directory entry named by new exists, it shall be removed and old
  renamed to new." (§1, §2; Assumptions 11 and 24)
- **RFC 1813**, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>, 229,793 characters. Read
  from 0 to 200,000 in two parts; 200,000 to the end was not read.
  - Under `fattr3`, whose section number the tool did not give: "The mode
    bits are defined as follows:", with "0x00800 Set user ID on execution.",
    "0x00400 Set group ID on execution.", "0x00200 Save swapped text (not
    defined in POSIX)." and "0x00100 Read permission for owner." `mode3`
    itself is `typedef uint32 mode3;`. (*Context*)
  - §2.6: `NFS3ERR_PERM` and `NFS3ERR_ACCES`, as *Context* quotes them,
    which is also how ADR 0014's *Sources* quote them; and, of `nfsstat3`,
    "No other values may be returned by a server." and "Servers are
    expected to make a best effort mapping of error conditions to the set
    of error codes defined." (§3)
  - §3, of each procedure's ERRORS section, as *Context* quotes it. (§3)
  - §3.3.12 `REMOVE`, ERRORS: `NFS3ERR_NOENT`, `NFS3ERR_IO`,
    `NFS3ERR_ACCES`, `NFS3ERR_NOTDIR`, `NFS3ERR_NAMETOOLONG`,
    `NFS3ERR_ROFS`, `NFS3ERR_STALE`, `NFS3ERR_BADHANDLE`,
    `NFS3ERR_SERVERFAULT`. §3.3.13 `RMDIR`: those, with `NFS3ERR_INVAL`,
    `NFS3ERR_EXIST`, `NFS3ERR_NOTEMPTY` and `NFS3ERR_NOTSUPP`. §3.3.14
    `RENAME`: eighteen, `NFS3ERR_ACCES` among them. The tool said that
    `NFS3ERR_PERM` is in none of the three. (§3)
  - §3.3.14 `RENAME`, IMPLEMENTATION, as *Context* quotes it; and
    DESCRIPTION: "If the directory, to.dir, already contains an entry with
    the name, to.name, the source object must be compatible with the
    target: either both are non-directories or both are directories and the
    target must be empty. If compatible, the existing target is removed
    before the rename occurs. If they are not compatible or if the target
    is a directory but not empty, the server should return the error,
    NFS3ERR_EXIST." (§2; *What this does not decide*)
- **Linux `fs/namei.c`**, at the tag `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/namei.c>,
  141,926 characters, read from 60,000 on in three parts. The tool returned
  these as code: `__check_sticky`, whole; `may_delete`, whole, with the
  eleven numbered lines of its comment, of which *Context* quotes the
  fifth; `may_create`, whole, with its comment; of `vfs_rename`, its
  declarations, `if (source == target) return 0;`, the first
  `error = may_delete(rd->old_mnt_idmap, old_dir, old_dentry, is_dir);`,
  the `if (!target)` block with `may_create` and the second `may_delete`,
  and, under "If we are going to change the parent - check write
  permissions, we'll need to flip '..'.",
  `error = inode_permission(rd->old_mnt_idmap, source, MAY_WRITE);`; of
  `do_renameat2`, `/* source must exist */` with `error = -ENOENT;`,
  `/* source should not be ancestor of target */` with `error = -EINVAL;`,
  and `/* target should not be an ancestor of source */` with
  `error = -ENOTEMPTY;`; and the openings of `vfs_rmdir`,
  `int error = may_delete(idmap, dir, dentry, 1);`, and of `vfs_unlink`,
  `int error = may_delete(idmap, dir, dentry, 0);`. (*Context*; §1, §2;
  Assumptions 4, 5, 10, 11, 13, 15 and 24)
- **Linux `include/linux/fs.h`**, at `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/include/linux/fs.h>,
  114,446 characters, read from 95,000 on. `check_sticky`:
  `if (!(dir->i_mode & S_ISVTX)) return 0;` and
  `return __check_sticky(idmap, dir, inode);`. (*Context*)
- **Linux `fs/nfsd/vfs.c`**, the server, at `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/nfsd/vfs.c>,
  fetched twice. `nfsd_rename`'s
  `err = fh_verify(rqstp, ffhp, S_IFDIR, NFSD_MAY_REMOVE);` and
  `err = fh_verify(rqstp, tfhp, S_IFDIR, NFSD_MAY_CREATE);`, its two
  `lookup_one_len` calls with `host_err = -ENOENT;`, `host_err = -EINVAL;`
  and `host_err = -ENOTEMPTY;` after them, `host_err = vfs_rename(&rd);`
  and `err = nfserrno(host_err);`. `nfsd_unlink`'s
  `err = fh_verify(rqstp, fhp, S_IFDIR, NFSD_MAY_REMOVE);`, its lookup, and
  its calls of `vfs_unlink` and `vfs_rmdir`. `nfs_errtbl`'s
  `{ nfserr_perm, -EPERM },` and `{ nfserr_acces, -EACCES },`. Of the three
  access tables, `{ NFS3_ACCESS_DELETE, NFSD_MAY_REMOVE },`, in
  `nfs3_diraccess` alone. The tool said that the file's unlink and rename
  paths check no sticky bit, and that `NFSD_MAY_REMOVE` is defined in a
  header it was not given. (*Context*; §3, §5)
- **Linux `fs/nfs/nfs3xdr.c`**, the client, at `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/nfs/nfs3xdr.c>,
  57,213 characters, read from 50,000 on. `nfs_errtbl`'s
  `{ NFSERR_PERM, -EPERM },` and `{ NFSERR_ACCES, -EACCES },`, and
  `nfs3_stat_to_errno`, which returns the table's errno for a status and
  `-EIO` for one it lacks. (§3)
- **Apple `NFS`, `kext/nfs_vfsops.c`**, at `main`:
  <https://raw.githubusercontent.com/apple-oss-distributions/NFS/main/kext/nfs_vfsops.c>,
  202,898 characters, read from 0 to 200,000 in two parts. Under
  `/* For NFSv3 and greater, there is a (relatively) reliable ACCESS call. */`:
  `if (nmp->nm_vers > NFS_VER2 && !NMFLAG(nmp, NOOPAQUE_AUTH)) {`,
  `vfs_setauthopaqueaccess(mp);` and `vfs_setauthopaque(mp);`. The tool
  could not name the function they are in, whose opening is in the first
  part. (*Context*; Assumption 15)
- **Apple `xnu`, `bsd/vfs/vfs_subr.c`**, at `main`:
  <https://raw.githubusercontent.com/apple-oss-distributions/xnu/main/bsd/vfs/vfs_subr.c>,
  345,956 characters, read from 200,000 on in two parts.
  `vnode_authorize_opaque`'s comment, which begins "Handle authorization
  actions for filesystems that advertise that the server will be
  enforcing." and gives its returns as "0 Authorization should be handled
  locally" and "1 Authorization was handled by the FS"; its
  `if ((error = VNOP_ACCESS(vp, action, ctx)) != ENOTSUP) {` with
  `*resultp = error;` and `return 1;`; `vnode_authorize_callback_int`'s
  line, as *Context* quotes it; and, at the end of
  `vnode_authorize_delete`, under `/* enforce sticky bit behaviour */`,
  `if ((dvap->va_mode & S_ISTXT) && !vauth_file_owner(vcp) && !vauth_dir_owner(vcp)) {`
  with `return EACCES;`. **Honesty note:** the first 200,000 characters were
  not read, and neither was `vfs_setauthopaque`, nor the kext's
  `nfs_vnop_access`. (*Context*; §3; Assumptions 7 and 15)

Nothing else was fetched. Everything else here is read from this
repository's own code and documents, cited by symbol and section.
