# 0025. A regular file's owner may read, write and truncate it whatever its mode, execute permission is enough to read it, and ACCESS reports the mode alone

**Status:** Accepted
**Date:** 2026-11-13
**Revised:** 2026-11-15 — before merge, for two findings `security-auditor`
made on the branch, and to record the owner's confirmation of three of this
ADR's judgement calls (*Context*; Assumptions 1 and 15). §4 now says that
one path asks no `ACCESS` first: an open that creates a name the server
already holds, on which a size the `CREATE` carries truncates its caller's
own read-only file and the descriptor then writes it. §4's first reason for
accepting the widening claimed the client's check for every open and is
corrected; §1 points to it; *Consequences*, *What this does not decide* and
§8's first `CHANGES` line follow; and A10 (§6) pins it over the wire. §4
also names a possible further exception, unconfirmed, a set-group-ID file
whose owner is outside its group, gives its remedy to #155, and decides
that this ADR does not wait for #155. Assumptions 11, 13, 16 and 18 are
revised, Assumptions 20 to 22 are added, and *Sources* gains what was read
for both. The rows, §2, §3, `Permits`, `AccessBits` and every assertion of
A2 to A9 are unchanged. This line first said the same of A1, whose grid of
types test-author widened in the same PR; the next line records it.
**Revised:** 2026-11-15, second — before merge, for two findings of
`reviewer`'s on the revision above. The path that asks no `ACCESS` serves
the second widening as well as the first: a user behind a kernel client who
may execute a regular file, and who has write and search on the directory
that holds it, can open the file by creating it and read it. §2 said that
such a user "cannot `cat` or copy it", and is corrected, with §4's list of
callers, its paragraph on the path and what is given up, Assumptions 15 and
20, *Consequences*, *What this does not decide* and §8's second `CHANGES`
line. §2's rule is not re-decided: the owner confirmed it on the cost as
first stated, and the corrected cost is the session's to put to the owner
(*Context*, decision 3; Assumption 23, which is new). §6's A1 now names the
types the test iterates since it was widened, and why (Assumption 13), and
§6's list of fixtures gains `mkRename`, which A3 uses. *Sources* gains what
was read to check the first finding. The rows, §3, `Permits`, `AccessBits`
and every assertion of A2 to A10 are unchanged.
**Revised:** 2026-11-15, third — before merge, to record the owner's
decision on what the line above left open. By the session's report, the
owner accepted §2 at the corrected cost, and confirmed that the reworded
second `CHANGES` line takes no `BREAKING:` (*Context*, decision 3). §2's
and §4's closing paragraphs and Assumptions 15 and 23 said that the cost
awaited the owner, and now say what the owner chose. Nothing else changes.
**Issue:** #93

## Context

**The owner's decisions.** Two, as ADR 0014 and ADR 0024 record them in the
owner's words, and a third the session reported. The brief this ADR was
written from restated neither of the first two, and nothing here claims any
approval beyond the three.

1. On 2026-10-10: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0024 were, and its design is the architect's (Assumption 1).
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." Nothing here is breaking (Assumption 15), so
   nothing here relies on it.
3. On 2026-11-13, by the session's report, which gave the substance and not
   the owner's words: the owner confirmed that reading with execute
   permission is allowed (§2), that the override covers regular files only
   (§1), and that the `CHANGES` line for reading with execute carries no
   `BREAKING:` (§8). Those three were the architect's judgement until then.
   The report says nothing of §4's two additions of 2026-11-15, which are
   the architect's (Assumptions 20 to 22). And the confirmation of §2 was
   of its cost as then stated: that an execute-only mode no longer hides a
   file's contents from a caller that sends `READ` itself, or, in the words
   of the `CHANGES` line then proposed, "reads it over NFS directly". The
   cost as corrected on 2026-11-15, which adds a user behind a kernel
   client who can write the file's directory, was not covered by it. The
   session put that cost to the owner on 2026-11-15, as Assumption 23
   states it, with three options: to accept the cost, to close the `CREATE`
   route first, or to drop reading with execute. By the session's report,
   which gave the owner's choice of an option and not the owner's words,
   the owner chose to accept the cost. Asked separately whether the
   reworded second `CHANGES` line takes `BREAKING:`, the owner answered no.
   So §2 stands at the corrected cost by the owner's decision.

**The defect.** #93, "Owners cannot read or write their own files when the
mode denies them", as the session gave its text (Assumption 2). A caller is
judged by one class of the mode bits only, and a read, a write and a size
change are refused without an owner override. So, the issue says, these fail
with EACCES on the mount: git's object files, `tar -x` or `cp -p` of
read-only files, and executing an execute-only file. It was found by reading
the code, "not reproduced by a test", and "not reproduced against a kernel
client". Its done-when:

- the owner may read, write and truncate their file whatever its mode;
- reading with execute permission is decided;
- ACCESS still reports the mode's permissions, as RFC 1813 §4.4 asks.

**What the code has.** Every statement about the code here was checked by
reading it at `900b5b7`, the commit this ADR's branch was cut from, which is
`origin/main` with ADR 0014 §6's wiring and the fix for #105 merged. Nothing
was run (Assumption 2). Code is cited by symbol. The issue's line numbers
are from `8377902` and name code that has since moved: `permitted` is now
`policy.Permits`, and each check the issue names is now a request to
`f.perm`.

- `policy.Permits`, in `internal/policy/policy.go`, judges anyone but root
  by exactly one class of the low nine mode bits: the owner's if
  `c.UID == o.UID`, else the group's if `InGroup(c, o.GID)`, else the
  others'. Root may read and write anything.
- `policy.Unix()`'s `Check` answers `OpRead` with
  `fileAccess(c, o, PermRead)`, and `OpWrite` and `OpTruncate` with
  `fileAccess(c, o, PermWrite)`. `fileAccess` returns `vfs.ErrIsDir` for a
  directory, and `vfs.ErrAcces` unless `Permits(c, o, want)`. So the owner
  of a file of mode `0o444` is refused `OpWrite` and `OpTruncate`, the owner
  of one of mode `0o000` all three, and a caller whose class has execute and
  not read is refused `OpRead`.
- `policy.AccessBits` builds the six `vfs.Access` bits from `Permits` alone.
- In `internal/blobfs/fs.go`, `readable` asks `OpRead`, for both of `Read`'s
  steps; `Write` asks `OpWrite`, holding `FS.mu` for reading, before the
  budget's wait; and `setAttrRequests` asks `OpTruncate` first for a call
  that sets a size, which `truncate` makes twice, the second time in the
  hold that applies the change (ADR 0024 §2). `Write` and `SetAttr` call
  `mutable()` before any of it; `Read` does not. `Access` returns
  `policy.AccessBits`, less `vfs.AccessModify`, `vfs.AccessExtend` and
  `vfs.AccessDelete` on a read-only mount, and asks `f.perm` nothing.
- `createEntry` applies a requested mode masked to `0o7777` and makes the
  new inode the caller's, so a file can be created with a mode that denies
  its own creator.
- In `internal/nfs/nfs3.go`, `callerFrom` copies the uid, the gid and the
  supplementary gids of the call's AUTH_SYS credential into the
  `vfs.Caller`, checking none. The `create` handler, for an UNCHECKED
  `CREATE` that carries a size, calls `SetAttr` with that size once `Create`
  has returned, and drops that call's error (ADR 0007, Assumption 9).

**What RFC 1813 says.** §4.4, "Permission issues" (*Sources*). Of the owner:
"Most operating systems check permission at open time, and then check that
the file is open on each read and write request. With stateless servers, the
server cannot detect that the file is open and must do permission checking on
each read and write call." And: "On a local file system, a user can open a
file and then change the permissions so that no one is allowed to touch it,
but will still be able to write to the file because it is open. On a remote
file system, by contrast, the write would fail. To get around this problem,
the server's permission checking algorithm should allow the owner of a file
to access it regardless of the permission setting. This is needed in a
practical NFS version 3 protocol server implementation, but it does depart
from correct local file system semantics. This should not affect the return
result of access permissions as returned by the ACCESS procedure, however."

Of execute: "The operating system usually checks for execute permission
before opening a file for demand paging, and then reads blocks from the open
file. In a local UNIX file system, an executable file does not need read
permission to execute (pagein). An NFS version 3 protocol server can not
tell the difference between a normal file read (where the read permission bit
is meaningful) and a demand pagein read (where the server should allow access
to the executable file if the execute bit is set for that user or group or
public). To make this work, the server allows reading of files if the uid
given in the call has either execute or read permission on the file, through
ownership, group membership or public access. Again, this departs from
correct local file system semantics."

Of `ACCESS`: "UNIX client semantics of access permission checking on open can
be provided with the ACCESS procedure call in this revision, which allows a
client to explicitly check access permissions without resorting to trying the
operation."

**How Linux's server reads it.** `fs/nfsd/vfs.c`, at the tag `v6.6`
(*Sources*):

- `nfsd_permission` returns early for the owner,
  `if ((acc & NFSD_MAY_OWNER_OVERRIDE) && uid_eq(inode->i_uid, current_fsuid())) return 0;`,
  under a comment that reads: "The file owner always gets access permission
  for accesses that would normally be checked at open time. This is to make
  file access work even when the client has done a fchmod(fd, 0). However,
  `cp foo bar' should fail nevertheless when bar is readonly. A sensible way
  to do this might be to reject all attempts to truncate a read-only file,
  because a creat() call always implies file truncation. ... but this isn't
  really fair. A process may reasonably call ftruncate on an open file
  descriptor on a file with perm 000. We must trust the client to do
  permission checking - using "ACCESS" with NFSv3."
- The override is given to regular files only. `nfsd_open` sets it with
  `if (type == S_IFREG) may_flags |= NFSD_MAY_OWNER_OVERRIDE;`, saying
  "Arguably we should also allow the owner override for directories, but we
  never have and it doesn't seem to have caused anyone a problem."
  `nfsd_setattr` gives a size change
  `accmode |= NFSD_MAY_WRITE|NFSD_MAY_OWNER_OVERRIDE;` and
  `ftype = S_IFREG;`.
- A read refused by the mode is asked again as execute, for a regular file:
  under `/* Allow read access to binaries even when mode 111 */`,
  `if (err == -EACCES && S_ISREG(inode->i_mode) && ...) err = inode_permission(&nop_mnt_idmap, inode, MAY_EXEC);`.
- `ACCESS` carries neither. `nfsd_access` calls
  `nfsd_permission(rqstp, export, dentry, map->how)` for each bit asked, and
  a regular file's table maps `NFS3_ACCESS_READ` to `NFSD_MAY_READ`,
  `NFS3_ACCESS_EXECUTE` to `NFSD_MAY_EXEC`, `NFS3_ACCESS_MODIFY` to
  `NFSD_MAY_WRITE|NFSD_MAY_TRUNC` and `NFS3_ACCESS_EXTEND` to
  `NFSD_MAY_WRITE`, with no `NFSD_MAY_OWNER_OVERRIDE` in any row.

**What the ADRs say.**

- ADR 0014 §5 pins `Permits`, the rows of `Unix()` and `AccessBits`. Its §11
  lists six rows as unpinned, "Tests must not rely on these", of which U1 is
  "`OpRead`, `OpWrite` or `OpTruncate` by an owner who is not root, whose
  owner bits lack the permission (#93; RFC 1813 §4.4's owner override)" and
  U2 "`OpRead` by a caller who is not root, whose class has execute but not
  read (#93; RFC 1813 §4.4's read with execute)". Its Assumption 21 keeps
  `Access` off `f.perm` until #97's fix, since "RFC 1813 §4.4 keeps
  `ACCESS`'s answer apart from the server's own overrides". Its §6 gives the
  stages A to E, the requests each method makes, and what `blobfs` relies on
  in `Unix()`'s type rows.
- ADR 0018 §1 to §3 make the policy rule standing: a change to what an
  existing decision allows keeps that decision a pure policy in
  `internal/policy`, with no inline condition.
- ADR 0024 §2 has `SetAttr` make every request before it applies anything,
  twice for a call with a size.
- ADR 0011 §4 pins what a read-only mount answers, and ADR 0014 §7 what
  `mutable()` answers for each mode.
- ADR 0015 records the trust model: `readCred` believes the uid of an
  AUTH_SYS credential, and by default any local process can connect "and
  claim any uid, root included". The README's first limitation says that any
  local user who speaks the protocol can read anything in the filesystem
  and, on a writable mount, change or delete it, "whatever its permissions
  say".
- ADR 0010 decides who owns a new filesystem's root directory, and nothing
  about what an owner may do.
- ADR 0013 §1 gives permission rules to exhaustive tests of the policy and
  to security review, not to TLA+: "per-request rules, not interleavings".

**What this constrains.** Three rows of `policy.Unix()`, in
`internal/policy/policy.go`, and through them what `Read`, `Write` and a
`SetAttr` with a size answer; ADR 0014 §5's bullets for those rows and §11's
U1 and U2, by cross-reference; and the tests of `internal/policy` and
`internal/blobfs`. No non-test code of `internal/blobfs`, `internal/nfs` or
`internal/vfs` changes, and nothing on the wire, in the bucket or in the
flags.

## Decision

### 1. The owner of a regular file may read, write and truncate it whatever its mode

**The rows.** `Unix().Check` answers `OpRead`, `OpWrite` and `OpTruncate` as
below, in place of ADR 0014 §5's two bullets for them. The terms are
ADR 0014 §5's: *root* is `c.UID == 0`, *the owner* is `c.UID == o.UID`, and
*a directory* is `Type == vfs.TypeDir`. One is added: *a regular file* is
`Type == vfs.TypeReg`. Each row is read from the top, and the first line
whose condition holds gives the answer.

- `OpRead`:
  1. `vfs.ErrIsDir`, if `Object` is a directory;
  2. nil, if `Permits(Caller, Object, PermRead)`;
  3. nil, if `Object` is a regular file and the caller is its owner;
  4. nil, if `Object` is a regular file and
     `Permits(Caller, Object, PermExec)` (§2);
  5. `vfs.ErrAcces` otherwise.
- `OpWrite` and `OpTruncate`:
  1. `vfs.ErrIsDir`, if `Object` is a directory;
  2. nil, if `Permits(Caller, Object, PermWrite)`;
  3. nil, if `Object` is a regular file and the caller is its owner;
  4. `vfs.ErrAcces` otherwise.

Line 3 of each row is the owner override. Everything else in ADR 0014 §5
stands as written: `InGroup`, `Permits`, `AccessBits`, every other row, the
bare `vfs.Status` of a refusal, the fields a row reads (these three read
`Op`, `Caller` and `Object`, and nothing else), purity, `All` and `Func`. No
name is added to `internal/policy` (Assumption 8).

For a regular file owned by `501:20`:

| Mode | Caller | `OpRead` | `OpWrite`, `OpTruncate` | At `900b5b7` |
|---|---|---|---|---|
| `0o444` | uid 501, the owner | nil | nil | write and truncate `ErrAcces` |
| `0o000` | uid 501 | nil | nil | all three `ErrAcces` |
| `0o200` | uid 501 | nil | nil | read `ErrAcces` |
| `0o711` | `502:30`, neither owner nor in the group | nil, by line 4 | `ErrAcces` | read `ErrAcces` |
| `0o710` | `503:20`, in the group | nil, by line 4 | `ErrAcces` | read `ErrAcces` |
| `0o701` | `503:20` | `ErrAcces` | `ErrAcces` | the same |
| `0o000` | `502:30` or `503:20` | `ErrAcces` | `ErrAcces` | the same |
| any | uid 0, root | nil | nil | the same |

**The owner is a uid** (Assumption 4). "Its owner" means
`Caller.UID == Object.UID`: the uid of the call's credential against the uid
the inode holds when the request is made. No group makes a caller the owner,
and the row reads neither `Request.UID` nor `Request.GID`, which are
`OpChown`'s and `OpChgrp`'s. Root needs no override, since line 2 already
allows root both permissions on anything that is not a directory.

**A regular file only** (Assumption 3). Lines 3 and 4 hold for
`vfs.TypeReg` and for nothing else.

- *A directory* is refused these three operations at line 1, before any
  permission is looked at, as now. And no directory row changes: `OpLookup`,
  `OpReadDir`, `OpAddEntry`, `OpRemoveEntry` and `OpRename` judge a
  directory's owner by the owner bits, as ADR 0014 §5 has them
  (Assumption 5). So the owner of a directory of mode `0o555` still cannot
  create, remove or rename in it until it changes the mode, as on a local
  filesystem.
- *An object that is neither*, a symlink or a type that no operation of this
  server makes, gets neither line. Its rows are what they were,
  `vfs.ErrAcces` unless `Permits`, which is ADR 0014 §11's U3 and #69's to
  change. One half of U3 is pinned from here, as a refusal only: where
  `Permits` refuses such an object the permission its row asks for, the row
  refuses it (§6; Assumption 12).

**`Permits` does not change.** The override is in the three rows, so nothing
else that reads `Permits` moves: not `AccessBits` (§3); not `OpSetTimes`,
which allows the owner by name already and anyone else by
`Permits(…, PermWrite)`; and not a directory row.

**The mount gate comes first.** The requests are made where ADR 0014 §6 and
ADR 0024 §2 place them, at stage D, and nothing in `internal/blobfs`
changes. `Write` and `SetAttr` call `mutable()` at stage A, before the
handle is resolved. So on a read-only mount the owner's `Write` and size
change answer `vfs.ErrROFS`, and on a diverged mount `vfs.ErrStale`, as
everyone's do (ADR 0011 §4; ADR 0014 §7): the override gives no change to a
mount that takes none. `Read` has no stage A, so a read-only or a diverged
mount serves the owner's reads, and §2's, as a writable mount does.

**Each check reads the owner afresh.** `Read` asks at its step 1 and, for a
file with an `FS.open` entry, again at its step 2. A `SetAttr` with a size
asks in its pre-check and again in the hold that applies the change
(ADR 0024 §2). Each builds `Object` from the inode as it then stands, so a
change of owner between the two is seen by the second. `Write` asks once,
before the budget's wait, and whether it asks again where the write lands is
still #101's question: the override adds no input to that verdict, which
already read the owner's uid to choose a class.

**The order of errors** is unchanged (ADR 0007 §4; ADR 0014 §6's stages):
the mount gate, the arguments, the handle, and then the row, whose type
check comes before its permission check (Assumption 7). A refusal is still
the bare `vfs.ErrAcces`, and is not logged (ADR 0018 §5).

**What a client can observe.** For a regular file, from a caller whose uid
is the file's owner:

- `READ`, `WRITE` and a `SETATTR` that sets a size are no longer answered
  `NFS3ERR_ACCES` for the mode. Each is applied as it would be under a mode
  that allows it. So a file created with a read-only mode can be written by
  the caller that created it, which is what the issue's first cases need:
  git's object files, and `tar -x` or `cp -p` of a read-only file
  (Assumption 16).
- A `SETATTR` that sets a size and other attributes is applied whole
  (ADR 0024 §2), where the size's refusal used to refuse all of it.
- An UNCHECKED `CREATE` that carries a size, of a name that exists and is
  the caller's regular file, now sets that size. The `create` handler's
  `SetAttr` is allowed, where before it was refused and its error dropped;
  the reply is `NFS3_OK` either way (Assumption 18). A client asks no
  `ACCESS` before such a call, so §4 counts this among what is given up.
- `ACCESS` and `GETATTR` answer as before, and none of these calls changes
  the mode (§3).

For any other caller a regular file's rows change only by §2. For
directories and symlinks nothing changes.

### 2. A caller with execute permission may read a regular file

**The rule** is line 4 of `OpRead`: a caller whom the mode refuses read is
allowed `OpRead` on a regular file if `Permits(Caller, Object, PermExec)`,
that is, if the one class of mode bits that judges the caller has execute.
It is RFC 1813 §4.4's rule, "either execute or read permission on the file,
through ownership, group membership or public access", and Linux's
(*Context*).

**For whom** (Assumption 6). Every caller, by its own class: a member of the
file's group by the group's bits, anyone else by the others'. One class
still judges, as ADR 0014 §5 has it: a group member is not helped by an
execute bit for others, so a member of the group is refused a read of a file
of mode `0o701`. The owner and root are not affected, since lines 2 and 3
already allow them.

**For reads only.** Execute gives no `OpWrite`, no `OpTruncate`, and no
`ACCESS` bit it did not give before (§3). A directory's execute bit is
search, and `OpRead` on a directory is `vfs.ErrIsDir` at line 1.

**Why it is allowed.**

1. Running a program is reading it. The client's operating system "checks
   for execute permission before opening a file for demand paging, and then
   reads blocks from the open file", and the server "can not tell the
   difference between a normal file read ... and a demand pagein read".
   Refusing the read makes an execute-only program unrunnable from the
   mount, which is the issue's second case.
2. It is what RFC 1813 says the server does, and what Linux's server does,
   so it is what a client written against either expects.
3. Under this server's trust model it shows nothing to anyone who could not
   already read it (§4).

**What a client can observe.**

- `READ` of a regular file, from a caller whose class has execute and not
  read, returns the bytes, where it answered `NFS3ERR_ACCES`.
- `ACCESS` for that caller still reports `ACCESS3_EXECUTE` and not
  `ACCESS3_READ`. A kernel client asks `ACCESS` when a process opens a file
  the client knows of, so it is expected still to refuse that open for
  reading and still to allow an exec (Assumption 16).
- A kernel client asks nothing when it opens a name by creating it (§4). So
  on a client that opens that way, a user who may execute the file, and who
  has write and search on the directory that holds it, can open it for
  reading and read it with ordinary tools, where at `900b5b7` its reads
  were refused (Assumption 20). A user who cannot add a name to that
  directory cannot: its `CREATE` is refused at the directory.
- A caller that sends `READ` without asking, as any process that speaks the
  protocol can, gets the bytes whatever the directory's mode.

**An execute-only mode no longer keeps a regular file's contents from a
caller who may execute it**: not from one that speaks the protocol, and not
from a user behind a kernel client who can write the file's directory. That
is the cost of this section, and §4 says why it is accepted. The first
version of this section named the first of the two alone, and said that
through a kernel mount such a user "can run the file and cannot `cat` or
copy it", which `reviewer` found untrue of the second (Assumption 20). The
owner's confirmation of 2026-11-13 was of the cost as first stated. On
2026-11-15, by the session's report, the owner accepted this section at
the cost as it is stated here (*Context*, decision 3; Assumption 23).

### 3. `ACCESS` reports the mode's permissions, and stays apart from the operations

**Neither `AccessBits` nor `Access` changes** (Assumption 9).
`policy.AccessBits` stays the function ADR 0014 §5 pins, built on `Permits`
alone, with neither line 3 nor line 4. `(*FS).Access` still returns it, less
a read-only mount's write bits (E12), and still asks `f.perm` nothing
(ADR 0014 §11's W5). So `ACCESS` reports what the mode bits give the
caller's class, as it did.

For a regular file:

| Mode | Caller | `ACCESS` grants | The rows allow |
|---|---|---|---|
| `0o644` | the owner | read, modify, extend | read, write, truncate, as before |
| `0o444` | the owner | read | read, write, truncate |
| `0o200` | the owner | modify, extend | read, write, truncate |
| `0o100` | the owner | execute | read, write, truncate |
| `0o000` | the owner | nothing | read, write, truncate |
| `0o711` | a group member, or anyone else | execute | read |
| `0o000` | a group member, or anyone else | nothing | nothing |

**Why.**

1. §4.4 asks for it in terms: the override "should not affect the return
   result of access permissions as returned by the ACCESS procedure".
2. `ACCESS` is how a client gives its processes the check at open ("UNIX
   client semantics of access permission checking on open can be provided
   with the ACCESS procedure call"). The override stands in for a descriptor
   that is already open, which is the one thing a stateless server cannot
   see. It is not a permission to open. If `ACCESS` reported it, a client
   would let the owner open a read-only file for writing, which no local
   filesystem allows. Linux's server says the same of `cp foo bar` onto a
   read-only `bar`, and concludes: "We must trust the client to do
   permission checking - using "ACCESS" with NFSv3."

**How they stay apart.** There are two predicates, and from this ADR they
differ on purpose. `Permits` is what the mode gives, and `AccessBits` is
built on it. The three file rows are `Permits` and the two departures of §1
and §2. So, for a regular file, at stage D:

- every bit `ACCESS` grants names something the rows allow the same caller:
  `vfs.AccessRead` is backed by `OpRead`; `vfs.AccessModify` and
  `vfs.AccessExtend` by `OpWrite` and `OpTruncate`; and
  `vfs.AccessExecute` by `OpRead`, which is new, by lines 3 and 4;
- the converse is false, in exactly two ways: lines 3 and 4.

**What #97 will have to reconcile.** #97, "ACCESS grants what the operations
then refuse", is a separate PR, and nothing of it is done here. This ADR
leaves it four things (Assumption 10):

1. *A direction.* The property #97 can state runs one way: what `ACCESS`
   grants, the operations allow. The other way is false since this ADR and
   stays false: the operations allow the owner, and an execute holder's
   read, what `ACCESS` does not report. A fix that made the two equal would
   undo §3.
2. *No derivation from these three rows.* If #97 has `Access` ask `f.perm`,
   as ADR 0014 §6 anticipates, it cannot get a regular file's bits by asking
   `OpRead`, `OpWrite` or `OpTruncate`, which now carry the departures. It
   keeps `Permits` for them, or adds a request that asks for the mode's
   verdict, under ADR 0014 §5's *Growth* rule. `Permits` is left as the one
   mode-bit predicate so that either is open.
3. *What is still #97's.* The directory bits of U5, where `ACCESS` exceeds
   `OpLookup`, `OpAddEntry` and `OpRemoveEntry`; whether a diverged mount
   masks its write bits (ADR 0014 §7); and the bits of an object that is
   neither a regular file nor a directory, which wait on #69. None moves
   here.
4. *What is no longer #97's.* On a regular file, `ACCESS3_EXECUTE` granted
   and the read that execution needs then refused. §2 closes it, and A4
   (§6) pins it.

### 4. Security

**What widens**, and nothing else does:

- (a) a caller whose uid is a regular file's owner is allowed `OpRead`,
  `OpWrite` and `OpTruncate` on it where its owner bits lack the permission;
- (b) a caller whose class of a regular file's mode bits has execute and not
  read is allowed `OpRead` on it.

No row for a directory, a symlink, the mode, the owner, the group or the
times allows anything it refused. `ACCESS` reports nothing more. Root is as
it was. And no call gets past the mount gate that did not (§1).

**The uid is the client's claim.** `callerFrom` takes the uid from the
call's AUTH_SYS credential, which `readCred` believes, and by default any
process on the machine can connect "and claim any uid, root included"
(ADR 0015 §8; README, *Honest limitations*). So "the owner" in
line 3 is whoever sends the owner's uid. The override is a rule about a
claimed uid, as every other row is; it does not say the caller is who it
claims to be.

**What it gives each kind of caller.**

- *A caller that speaks the protocol itself and chooses its uid.* Nothing.
  It could already claim uid 0, which every one of these rows allows on
  anything that is not a directory, so no file's mode kept anything from it.
  That is the limitation the README states, and #6, #90 and #95 own it.
- *A caller behind a kernel client, whose uid the kernel sets.*
  - For (a), on a writable mount: nothing it could not already do, with one
    possible exception, which is not confirmed (below). `OpChmod` allows a
    file's owner to set any mode, so the owner could make the same call
    after one `SETATTR` of the mode and restore the mode with another. The
    override removes the two calls.
  - For (a), on a read-only or a diverged mount: the owner cannot change the
    mode there, and line 3 lets its `READ` through where the mode denies its
    owner read. That is the one case in which (a) allows such a caller a
    call it had no other way to make under its own uid.
  - For (b): a `READ` it could not make before.

  In the last two the kernel client is expected to refuse an *open* of a
  file it knows of, by the `ACCESS` answer, which is unchanged. So what
  reaches the server by that route is a read on a descriptor the client
  already let the process open, or a page-in for an exec (Assumption 16).
  The creating open is a second route, which nothing checks (below). The
  mount gate closes it on a read-only or a diverged mount, where `CREATE`
  is refused before anything else (§1). So on a writable mount it serves
  (a) and (b), and on a mount that takes no change it serves neither:
  (a)'s reads there come by the first route alone.
- *A caller under a stricter trust model*, should #6 or #95 bring one. (a)
  still gives an owner on a writable mount nothing that a `SETATTR` of the
  mode does not, and root squash would not stop it, since a squashed client
  can still claim the owner's uid. (b), and (a)'s reads on a mount that
  takes no change, are then real: a regular file's contents are open to its
  owner's uid whatever its mode, and to whoever may execute it. Those are
  §4.4's two departures, which the RFC states as departures, and an ADR
  that tightens the trust model inherits them knowingly.

**One path asks no `ACCESS` first** (Assumption 20). The client's check at
open covers a file the client knows of. A client that takes a name to be
absent opens it by sending `CREATE`, and asks nothing about a file it
believes it has just made. If the name exists and is a regular file,
`Create` returns it to any caller who has write and search on the
directory: it asks `OpAddEntry` of the directory, and nothing of the file.
What the descriptor the client then holds can do is what the rows allow its
caller, so both widenings reach it:

- *The owner*, (a). A size the `CREATE` carries truncates the file, by
  line 3, where at `900b5b7` the size was refused, its error dropped and the
  contents kept (§1); and the descriptor then writes it, by the same line.
  So on this path a read-only mode no longer protects a file from its
  owner's own accident, where a local filesystem refuses the open.
- *A caller with execute and not read*, (b). The descriptor reads the file,
  by line 4, where at `900b5b7` its `READ`s were refused. So a user behind a
  kernel client who may execute a file, and who has write and search on the
  directory that holds it, can read it, where a local filesystem refuses
  the open.

Nobody else gains by it. A caller the mode gives read or write gets no more
than a checked open would have given it, and a caller it gives none of
read, write and execute gets a descriptor whose every `READ` and `WRITE` is
still refused. Linux's server answers such a `CREATE`, and the calls that
follow it, the same way, so this is what the two departures cost under
NFSv3, and not a defect of this server's alone (*Sources*). The first
revision of this paragraph gave the owner's half alone; `reviewer` found
the other.

**A possible further exception, not confirmed: a set-ID file**
(Assumption 21). `security-auditor` found that "nothing it could not already
do" assumes the owner can change the mode and put it back exactly, and that
this may fail for a set-group-ID file whose group its owner is not in. On a
local filesystem, the finding says, such an owner can change the file's
contents only by losing the bit: a write drops it, as POSIX allows, and so
does a `chmod` by a caller outside the group, as POSIX requires
(*Sources*). Here an owner who holds a descriptor for writing on a file of
mode `02555`, from before the mode lost owner write or from the path above,
now writes it with the mode left as it was: line 3 allows the `WRITE`, and
this server clears no set-ID bit when a file's contents change.

Whether that is more than the mode route gave depends on what drops the bit
from the owner's `chmod`. This server does not: it stores the mode it is
sent. So it is more only if the client drops the bit before it sends the
`SETATTR`. That is the finding's reading of Linux's client. It is not mine:
as I read `v6.6`, the client sends the mode as given and leaves the bit to
the server (*Sources*), in which case the owner could do the same at
`900b5b7` by the mode route, and the override adds nothing to it. Neither
reading was run. Confirming it needs a kernel NFS mount in a sandbox and a
user outside the file's group: `chmod u+w` a `02555` file and read back the
mode the server holds; then write through a descriptor opened before the
mode lost owner write, and read the mode back again.

Whichever is right, the cause is one, and it is not in the rows: this server
leaves set-ID bits set across a change of a file's contents, and across a
change of its mode by an owner outside its group. #155, "A WRITE or a size
change leaves a file's set-user-ID and set-group-ID bits set", decides the
first, and a fix there clears the bits on an override write as on any other,
with no change to these rows. The second is not in #155's title, and is its
ADR's to take or to hand on (*What this does not decide*).

**This ADR does not wait for #155** (Assumption 22).

1. On a mount made with any command strata prints, documents or scripts the
   bit has no effect, since each carries `nosuid` (ADR 0015 §1, §6). A mount
   made without it is one ADR 0015 already says can give any local user
   root.
2. The override does not make the gap. At `900b5b7` any caller the mode
   allows to write, a member of the group or anyone else, changes a set-ID
   file's contents with the bits kept, and by my reading so does its owner,
   by the mode route.
3. The remedy is in the change, not in who may make it. A rule here that
   withheld the override from a file with a set-ID bit would refuse a write
   that a local filesystem allows, and would be retracted when #155 lands.

**Why that is acceptable** (Assumption 11).

1. A file's mode has never bound its owner, who sets it. What the mode
   protects an owner from is accident, and that protection lives in the
   client's check at open, which still asks `ACCESS` and still gets the
   mode's answer, on every path but the creating open above. The first
   version of this reason claimed it for every open, which
   `security-auditor` found untrue.
2. The protocol asks for both departures, and Linux's server makes both,
   beside root squashing, which is its default (ADR 0015, *Sources*), so
   they are what an NFSv3 deployment already lives with.
3. The alternative breaks every honest client that writes a file it created
   read-only, or runs a program it may not read, in order to keep a boundary
   that holds against no caller able to probe it.
4. The widening is narrow and pinned: regular files, three rows, an equality
   of uids. A1 (§6) checks it exhaustively over modes, types and callers,
   and the pins on directories, on other types and on `AccessBits` fail a
   build that widens further.

**What is given up**, which *Consequences* restates:

- an execute-only mode's secrecy against a caller that speaks the protocol,
  and against a user behind a kernel client who has write and search on the
  file's directory, by the creating open;
- the mode's hold on an owner's reads on a mount that takes no change;
- a read-only mode's protection of a file against its owner's own creating
  open, which no `ACCESS` precedes: a size the `CREATE` carries truncates
  the file, and the writes that follow succeed;
- possibly, and not confirmed, the tie between a set-group-ID bit and the
  contents it was set on, for an owner outside the file's group, on a mount
  made without `nosuid`.

The second half of the first item was found after the owner confirmed §2,
and that confirmation did not cover it. The session put it to the owner on
2026-11-15, and by its report the owner accepted §2 at that cost (*Context*,
decision 3; Assumption 23).

### 5. What stays out of this PR

Each of these is its own PR under #114, and none has to move for #93. Their
titles are as ADR 0014's *Context* gives them (Assumption 2).

- **#101**, "Write and truncate check permission before a wait, not where
  they apply the change". The rows change, not where they are asked (§1).
- **#94**, "The sticky bit on a directory is not honoured". No directory row
  changes, and U4 stays unpinned.
- **#97**, "ACCESS grants what the operations then refuse". §3 says what it
  inherits. This PR cannot make its defect worse: the operations only allow
  more, so the cases where `ACCESS` grants and an operation refuses can only
  become fewer, and they become fewer by one kind, §3's fourth.
- **#98**, "CREATE, MKDIR and SYMLINK apply a requested group from any
  caller". `createEntry` is untouched. A requested group changes no owner,
  so it gives nobody the override.
- **#69**, "SETATTR with a size, and WRITE, accept a symlink's handle".
  Lines 3 and 4 do not reach a symlink (§1), so U3's rows allow exactly what
  they allowed.
- **#99**, "A guarded SETATTR checks its guard outside the filesystem's
  lock". `internal/nfs` is untouched.
- **#100**, "Any caller with write permission can set a file's times to any
  value". `OpSetTimes` is untouched, and reads `Permits`, which does not
  change, so the override does not reach it. U6 stays as it is.
- **#148**. The brief named it without its title or its text, so nothing
  here describes it (Assumption 19). The code this PR changes is three rows
  of one file, so it includes nothing else, whatever #148 asks.

### 6. The test surface

Every name is spelled as in Go. test-author cannot read the non-test files
of `internal/policy`, `internal/blobfs` or `internal/nfs`, so what a test
may use is here, in ADR 0014 §5 (the `policy` API, which does not change),
in ADR 0007 §5 (the encodings a wire test needs), in `internal/vfs/vfs.go`,
or in a `_test.go` file. No test here replaces `f.perm`, writes a field of
`FS`, or reads one but through the helpers named.

**What becomes pinned** (Assumption 12). ADR 0014 §11's U1 and U2, for a
regular file: U1 as allowed, by line 3, and U2 as allowed, by line 4. U3
stays unpinned where `Permits` allows, and is pinned as a refusal only where
`Permits` refuses. U4, U5 and U6 stay unpinned, and no test here relies on
them: no directory's mode has `0o1000` set; `AccessBits` and `Access` are
asked of no directory that has read without search or write without search;
and no time is set by a caller who is not the owner.

**`internal/policy`** (package `policy_test`; Assumption 13). The new tests
go in one new file, `internal/policy/owner_access_test.go`, and reuse
`policy_test.go`'s fixtures: the callers `callerRoot`, `callerOwner`,
`callerOwnerInGroup`, `callerGroup`, `callerSupp` and `callerOther`;
`p2Callers`, `p2Modes` and `p2Types`; `mkObj`, `mkReq` and `mkRename`;
`oraclePermits`; `limiter`; and `opName`.

- **A1. The three rows, exhaustively** (`TestTheFileRowsExhaustively`). Over
  every mode of `p2Modes()`, every type of `oaAllTypes`, every caller of
  `p2Callers()` and each of `OpRead`, `OpWrite` and `OpTruncate`,
  `Unix().Check` is compared with an oracle written from §1's rows, with
  `oraclePermits` standing for `Permits`. `oaAllTypes`, the test file's own,
  is every `vfs.FileType` that `internal/vfs/vfs.go` names, which are
  `vfs.TypeReg`, `vfs.TypeDir`, `vfs.TypeBlk`, `vfs.TypeChr`, `vfs.TypeLnk`,
  `vfs.TypeSock` and `vfs.TypeFifo`, with `vfs.FileType(0)` and
  `vfs.FileType(99)`. As first written A1 iterated `p2Types`, four of those
  nine. It was widened because an inode's type is decoded from the bucket
  unchecked (Assumption 3), so any value can reach the rows, and a build
  whose lines 3 and 4 held for a type the narrower grid left out, a device
  node or a socket say, would have passed it (Assumption 13). For each
  type:
  - for a directory, `vfs.ErrIsDir`;
  - for a regular file, nil or `vfs.ErrAcces` exactly as the row's lines
    give, compared with `==`;
  - for any other type, a non-nil error, whichever it is, where
    `oraclePermits` refuses the permission the row asks for, and nothing at
    all where it allows (U3).
- **A2. The rows by name.** `unixCases()`, P4's table, gains these requests,
  on regular files:
  - `OpRead`, nil: the owner, of `0o000` and of `0o077`; the owner who is
    also in the group, of `0o000`; a primary-group member and a
    supplementary-group member, of `0o010`; other, of `0o001`.
  - `OpRead`, `vfs.ErrAcces`: a primary-group member, of `0o701` and of
    `0o020`; other, of `0o110` and of `0o002`.
  - `OpWrite` and `OpTruncate` each, nil: the owner, of `0o000` and of
    `0o444`; the owner who is also in the group, of `0o555`.
  - `OpWrite` and `OpTruncate` each, `vfs.ErrAcces`: a primary-group member,
    of `0o010`; other, of `0o001`.
  - As refusals only, the error not pinned: the owner, of a symlink of mode
    `0o000`, for each of the three; and other, of a symlink of mode `0o001`,
    for `OpRead`.

  `p5Cases()`, P5's table, gains the owner of a directory of mode `0o000`,
  for each of the three, and other of a directory of mode `0o111`, for
  `OpRead`, each `vfs.ErrIsDir`. Being in those two tables, the new requests
  are asked by P10 to P13 as well. P13 then shows that `Request.UID`,
  `Request.GID` and `Dst` change none of their answers, so that line 3 reads
  the owner from `Object.UID`.
- **A3. A directory's owner is held to its mode**
  (`TestADirectorysOwnerIsHeldToItsMode`), a characterisation, which passes
  before the change and after. For every mode from 0 to `0o777`, with no
  higher bit set, a directory `mkObj(vfs.TypeDir, mode)`, and the callers
  `callerOwner` and `callerOwnerInGroup`: `OpLookup` is nil exactly when the
  owner bits have execute; `OpReadDir`, when they have read; `OpAddEntry`
  and `OpRemoveEntry`, when they have write and execute; `OpRename`, with
  that directory as `Object` and a directory of mode `0o700` as `Dst`, and
  again the other way round, when they have write and execute; every
  refusal is `vfs.ErrAcces`; and `OpRead`, `OpWrite` and `OpTruncate` are
  `vfs.ErrIsDir` for every mode.
- **A4. `AccessBits` stays apart from the rows**
  (`TestAccessBitsStayApartFromTheFileRows`). For each row of this table, of
  a regular file, `AccessBits` is exactly the bits given, and `Unix().Check`
  answers the three operations as given:

  | Caller | Mode | `AccessBits` | `OpRead` | `OpWrite`, `OpTruncate` |
  |---|---|---|---|---|
  | the owner | `0o000` | 0 | nil | nil |
  | the owner | `0o444` | `AccessRead` | nil | nil |
  | the owner | `0o200` | `AccessModify` and `AccessExtend` | nil | nil |
  | the owner | `0o100` | `AccessExecute` | nil | nil |
  | the owner, also in the group | `0o070` | 0 | nil | nil |
  | a primary-group member | `0o010` | `AccessExecute` | nil | `ErrAcces` |
  | a supplementary-group member | `0o010` | `AccessExecute` | nil | `ErrAcces` |
  | other | `0o001` | `AccessExecute` | nil | `ErrAcces` |
  | other | `0o000` | 0 | `ErrAcces` | `ErrAcces` |
  | root | `0o000` | `AccessRead`, `AccessModify` and `AccessExtend` | nil | nil |

  And, over every mode of `p2Modes()` and every caller of `p2Callers()`, for
  a regular file: whenever `AccessBits` has `vfs.AccessExecute`,
  `Unix().Check` of `OpRead` is nil (§3).
- **P2, P3 and P9** do not change, and are the pins that `Permits` and
  `AccessBits` did not move: P3 still has the owner of `0o077` refused by
  `Permits`, and P9 still has `AccessBits` follow `Permits` exactly for
  anything that is not a directory.

**`internal/blobfs`** (package `blobfs`), in one new file,
`internal/blobfs/owner_access_test.go`, with helpers of its own prefixed
`oa`. The harness is ADR 0011 §7's, as ADR 0023 §11 and ADR 0014 §11 apply
it: a `*store.Local` under `t.TempDir()` behind an unarmed `uc43Store`,
through `uc43Setup`; `SnapshotRetention` −1 and `MaxDirtyBytes` −1, through
`uc43Config`; every `New` and every `FS` call in `uc43Go`, waited for under
`uc43Bound`; nothing sleeps, polls or runs in parallel. `Unix()` is left in
place on every mount. A test may reuse `uc43Env` and its methods,
`uc43WriteForeignRoot` and `uc43Writer`; the `pw` helpers, `pwMember` and
`pwAllAccess` of `policy_wiring_test.go`; the `sa24` helpers and
`sa24Other` of `setattr_atomic_test.go`; `sp24Root`; and `testCaller`. Of
unexported non-test names it uses one, `mountDiverged`, through
`uc43Env.mode`.

The callers are `testCaller` (`vfs.Caller{UID: 501, GID: 20}`), who owns the
root directory and everything it creates; `pwMember`
(`vfs.Caller{UID: 503, GID: 20}`), in the group of those; `sa24Other`
(`vfs.Caller{UID: 502, GID: 30}`), neither; `sp24Root`
(`vfs.Caller{UID: 0}`); and, where a supplementary group is wanted,
`vfs.Caller{UID: 504, GID: 40, GIDs: []uint32{20}}`. Every refusal is
compared with `==` against the bare `vfs.Status`. The facts of ADR 0014
§11 stand, and one is used with a mode of zero: a `Mode` given to `Create`
is applied as given, masked to `0o7777`, so `Mode` 0 makes a file of mode 0.

- **A5. The owner reads, writes and truncates its file whatever its mode**
  (`TestTheOwnerReadsWritesAndTruncatesItsFileWhateverItsMode`; §1; the
  issue's first done-when). `testCaller` creates, in the root, `ro` with
  `Mode` `0o444`, `none` with `Mode` 0, `wo` with `Mode` `0o200` and `e0`
  with `Mode` 0, each exclusive. Then, by `testCaller`:
  - `Write(ro, 0, p, vfs.Unstable)`, 100 bytes, returns 100 and nil;
    `GetAttr` reports size 100 and mode `0o444`; `Read(ro, 0, 100)` returns
    `p`;
  - `SetAttr(ro, {Size: 10})` returns nil with size 10 and mode `0o444`, and
    a `Read` returns `p[:10]` with eof;
  - `Write(none, 0, q)`, 50 bytes, returns 50 and nil; `Read(none, 0, 50)`
    returns `q`; `SetAttr(none, {Size: 20})` returns nil with size 20;
  - `Write(wo, 0, r)` returns nil, and `Read(wo, 0, len(r))` returns `r`;
  - `Read(e0, 0, 10)` returns no bytes, eof true and nil: `e0` was never
    written, so it has no `FS.open` entry and the read is step 1's alone;
  - `SetAttr(ro, {Size: 5, Mode: 0o400})` returns nil with size 5 and mode
    `0o400`.

  The override is the owner's alone: `sa24Other`'s and `pwMember`'s
  `Write(ro, …)` and `sa24Other`'s `SetAttr(ro, {Size: 0})` return
  `vfs.ErrAcces`, as do `sa24Other`'s and `pwMember`'s `Read(none, …)`, and
  `ro`'s and `none`'s sizes are afterwards what they were; `sp24Root`'s
  `Read(none, 0, 20)` returns `q[:20]`. Then `Sync` returns nil, and on a
  remount `testCaller`'s `Read` of `none`, which has no `FS.open` entry
  there, returns `q[:20]`, with `none` of mode 0 and size 20 and `ro` of
  mode `0o400` and size 5.
- **A6. A caller with execute permission reads a regular file**
  (`TestACallerWithExecutePermissionReadsARegularFile`; §2; the second
  done-when). `testCaller` creates `x711`, `x750`, `x701` and `x710`, of
  those modes, and writes 100 bytes to each. A `Read` of 100 bytes at 0:
  - by `sa24Other`: of `x711` and of `x701`, the bytes; of `x750` and of
    `x710`, `vfs.ErrAcces`;
  - by `pwMember`: of `x711`, of `x750` and of `x710`, the bytes; of `x701`,
    `vfs.ErrAcces`, since one class judges;
  - by the caller with the supplementary group: of `x710`, the bytes.

  Execute gives no write: `sa24Other`'s `Write(x711, …)` and
  `SetAttr(x711, {Size: 0})` return `vfs.ErrAcces`, and `x711` is still 100
  bytes. `Access` with every bit asked for gives `sa24Other` and `pwMember`
  `vfs.AccessExecute` alone on `x711`, `pwMember` `vfs.AccessRead` and
  `vfs.AccessExecute` on `x750`, and `sa24Other` nothing on `x750`. After a
  `Sync`, on a remount, `sa24Other`'s `Read` of `x711` returns the bytes.
- **A7. `ACCESS` reports the mode whatever the operations allow**
  (`TestAccessReportsTheModeWhateverTheOperationsAllow`; §3; the third
  done-when). `testCaller` creates `a444`, `a200`, `a100`, `a000` and
  `a644` with no `Mode`, writes bytes to each, and then sets each one's mode
  to the one its name gives, which `OpChmod` allows its owner, so that the
  setup does not rest on the override. For `testCaller`, with every bit
  asked for, `Access` gives `vfs.AccessRead` on `a444`; `vfs.AccessModify`
  and `vfs.AccessExtend` on `a200`; `vfs.AccessExecute` on `a100`; nothing
  on `a000`; and `vfs.AccessRead`, `vfs.AccessModify` and
  `vfs.AccessExtend` on `a644`. Asked for `vfs.AccessModify` alone on
  `a444`, it gives nothing. Then `testCaller`'s `Read` and `Write` of each
  of the five return nil, and the same `Access` calls give the same answers
  again. `sp24Root`'s `Access` of `a000` gives `vfs.AccessRead`,
  `vfs.AccessModify` and `vfs.AccessExtend`.
- **A8. A directory's owner gets no override**
  (`TestADirectorysOwnerGetsNoOverride`; §1), a characterisation, which
  passes before the change and after. `testCaller` makes a directory `d`,
  and in it a file `k` and a directory `sub`, and a file `top` in the root.
  It then sets `d`'s mode, and makes these calls itself:
  - at `0o555`: `Create(d, "n", …)`, `Mkdir(d, "n", …)`,
    `Symlink(d, "n", "k", …)`, `Remove(d, "k")`, `Rmdir(d, "sub")`,
    `Rename(d, "k", d, "k2")`, `Rename(d, "k", root, "k2")` and
    `Rename(root, "top", d, "in")` return `vfs.ErrAcces`; `Lookup(d, "k")`
    and `ReadDir(d, …)` return nil;
  - at `0o655`: `Lookup(d, "k")` and `Create(d, "n", …)` return
    `vfs.ErrAcces`, and `ReadDir(d, …)` nil;
  - at `0o355`: `ReadDir(d, …)` returns `vfs.ErrAcces`, and
    `Lookup(d, "k")` nil;
  - at 0: `Lookup`, `ReadDir` and `Create` return `vfs.ErrAcces`; `Access`
    with every bit gives nothing; and `Read(d, 0, 10)`, `Write(d, 0, …)` and
    `SetAttr(d, {Size: 0})` return `vfs.ErrIsDir`, the type before the
    override;
  - at `0o755`: `Remove(d, "k")` returns nil.
- **A9. The mount gate comes before the override**
  (`TestTheMountGateComesBeforeTheOwnerOverride`; §1), in two subtests. Each
  begins on a writable mount, where `testCaller` creates `ro` and `none`
  with no `Mode`, writes 100 bytes to each, sets their modes to `0o444` and
  to 0, and `Sync`s, so that the setup does not rest on the override.
  - *Read-only.* A second mount of the same bucket is made with
    `Config.ReadOnly` set, and the first is not used again. On it,
    `testCaller`'s `Write(ro, …)`, `SetAttr(ro, {Size: 0})`,
    `Write(none, …)` and `SetAttr(none, {Mode: 0o600})` return
    `vfs.ErrROFS`; `testCaller`'s `Read(none, 0, 100)` returns the bytes;
    `sa24Other`'s returns `vfs.ErrAcces`; `Access` with every bit gives
    `testCaller` `vfs.AccessRead` on `ro` and nothing on `none`; and both
    files are still 100 bytes.
  - *Diverged.* As ADR 0023 §11's U9 diverges a mount: a foreign root
    pointer is written on the inner store with `uc43WriteForeignRoot`, at
    the mount's epoch plus one; `uc43Env.commit` of a fresh name returns an
    error whose text contains `commit conflict`; and `uc43Env.mode` is
    `mountDiverged`. Then `testCaller`'s `Write(ro, …)`,
    `SetAttr(ro, {Size: 0})` and `Write(none, …)` return `vfs.ErrStale`,
    and its `Read(none, 0, 100)` returns the bytes.

**`internal/nfs`** (package `nfs`), in one new file,
`internal/nfs/owner_access_test.go`, added on 2026-11-15 for §4's path that
asks no `ACCESS` (Assumption 20). It drives the server over a real TCP
socket, as `max_file_size_test.go` does, and reuses that file's and
`integration_test.go`'s helpers: `startServer`, `dial`, `mountRoot`,
`nfsCreate`, `nmfsCreate`, `nmfsWrite`, `nmfsGetSize`, `skipWccData`, and
`rpcClient`, whose `uid` and `gid` fields a test sets to call as another
caller. `startServer`'s root directory is owned by `501:20`, which is what
`dial`'s client sends. The encodings are ADR 0007 §5's.

- **A10. An UNCHECKED `CREATE` that carries a size truncates its caller's
  own read-only file, and no one else's**
  (`TestNFSUncheckedCreateWithASizeTruncatesItsOwnersReadOnlyFile`; §1,
  §4). The owner, uid 501, creates `ro.bin` with a GUARDED `CREATE`, writes
  100 bytes to it, and with `SETATTR` sets its mode to `0o444` and the root
  directory's to `0o777`, so that the setup does not rest on the override
  and another caller may add a name in the root. `GETATTR` reports mode
  `0o444` and size 100. Then:
  - a client sending uid 502 and gid 30 sends an UNCHECKED `CREATE` of
    `ro.bin` with size 0. Whatever it answers, `GETATTR` still reports size
    100: the size is refused to a caller the mode denies. The status is not
    pinned, since today the handler answers `NFS3_OK` and drops the refusal
    (ADR 0007, Assumption 9);
  - the owner sends the same `CREATE`. It answers `NFS3_OK`, with
    attributes whose size is 0, and `GETATTR` of the handle the first
    `CREATE` returned reports size 0 and mode `0o444`, the mode being as it
    was;
  - the owner's `WRITE` of 10 bytes at 0 to that handle answers `NFS3_OK`
    with count 10, and `GETATTR` reports size 10.

  No call the test makes is an `ACCESS`, which is the path.

**Red run.** The tests use no name the code lacks, so they build at
`900b5b7`, where, read from the code and not run:

- P4 fails on A2's twelve allowed rows, each with `vfs.ErrAcces` for nil,
  and passes its other rows, the refusals-only among them; P5 passes;
- A1 fails for the owner of a regular file whose owner bits lack the
  permission, and for `OpRead` by a caller whose class has execute and not
  read, and nowhere else;
- A4 fails on each operation its table gives as nil and the mode denies,
  and on the execute rule, while every `AccessBits` value it names is right;
- A5 fails first on `Write(ro, …)`, with `vfs.ErrAcces`, then on every call
  of the owner's that the mode denies, and on what depends on those, such as
  the bytes read back; its refusals pass;
- A6 fails on the five reads that execute alone allows, and on the
  remount's;
- A7 fails on the reads and writes the mode denies the owner, while every
  `Access` answer is right;
- A9 fails, in each subtest, on `testCaller`'s `Read` of `none` alone;
- A10 fails on the owner's `CREATE`, whose reply and whose `GETATTR` report
  size 100, and on the owner's `WRITE`; its other caller's part passes;
- A3, A8, P1 to P3, P6 to P13 and every other test pass.

After the change all pass. A10 was specified after coder's change was in
the working tree, so a red run of it needs that change set aside.

**Existing tests** (Assumption 13). No assertion of any existing test
changes.

- `internal/policy/policy_test.go`: `unixCases()` and `p5Cases()` gain A2's
  rows. Their doc comments, and the file's header, say that U1 and U2 are
  unpinned and that no row asks them; they are to say that U1 and U2 are
  pinned since this ADR, and where. `checkUnix`'s message for a case pinned
  as a refusal only names `OpSetTimes` and U6; it is to name the case's own
  rule.
- `internal/blobfs/policy_wiring_test.go`: comments only, at three places.
  The file's header and `TestTheUnixRowsAreUnchangedThroughTheFS`'s doc
  comment list "an owner lacking its own bits" and "read through execute
  alone" among the unpinned rows W4 leaves out; they are to say that both
  are pinned since this ADR and tested in `owner_access_test.go`, and that
  W4 still leaves them out. That doc comment's "so that no setup step relies
  on an owner lacking its own bits (U1)" is about the directory `ro`, whose
  owner §1 holds to its mode; it is to cite §1 in place of U1.
- These were read against §1 and §2 and stand as they are. W2, W3 and W5
  replace `f.perm` or ask `Access`, neither of which this ADR touches. The
  rest expect a refusal only of a caller who is neither root nor the owner
  and whose class lacks both read and execute, or lacks write: W4's rows;
  `TestPermissionsEnforced`; `backpressure_test.go`'s write by a caller that
  mode `0o600` excludes; `TestReadAnswersFromTheCheckUnderTheFileLock`'s
  permission case; `TestCommitWindowTruncateDuringSettleIsWholeOrAbsent`'s
  and `TestMaxFileSizeCheckedBeforeTheHandle`'s calls by `rcOther`; and
  ADR 0024 §7's S3, whose `vfs.ErrAcces` is `sa24Other`'s and whose
  `vfs.ErrIsDir` is a directory's. Through the `FS`, no existing test gives
  a regular file a mode that denies its owner read or write; the two modes
  that deny an owner, W4's `0o555` and `0o333`, are directories'.

**What only review can show:**

- that the three rows are the only code that changed, and that `Permits`,
  `AccessBits` and every other row are what they were;
- that line 3 compares `Caller.UID` with `Object.UID` and nothing else;
- that the rows still take no lock, log nothing and allocate nothing when
  they allow (ADR 0014 §5);
- that no non-test file of `internal/blobfs`, `internal/nfs` or
  `internal/vfs` changed;
- that a kernel client behaves as §1 and §2 expect, which needs a mount and
  a human (Assumption 16).

### 7. The model, the trace and the vocabulary

No TLA+ module is affected, and none is added (Assumption 14). ADR 0013 §1
gives permission rules to exhaustive tests of the policy and to security
review, as "per-request rules, not interleavings", and §6's A1 is that test.
`docs/tla/CommitProtocol.tla` models no permission: its `Mutate` is guarded
by `Mutable`, the mount gate, which this ADR leaves in front of the rows
(§1).

`docs/tla/trace.tsv` names no symbol of `internal/policy`. The code change
is confined to `internal/policy/policy.go`, so no function the trace names
is edited, no row changes, and the module's `Checked against` line stays at
`555fc64`. No pass of the architect's is due after coder's commit, and
nothing under `docs/tla/` changes in this PR, so CLAUDE.md's merge bar does
not ask the session for `tla.sh full`. Both rest on coder's diff showing
`internal/policy/policy.go` and no other file. A diff that touches
`internal/blobfs/fs.go` is outside coder's brief, and would bring ADR 0013
§8's rule for a refactor of traced code back.

`docs/vocabulary.md` does not change. V7's entries stand as written:
`Permits` is still `policy.Permits(c, o, want)`, `UnixAllows(r)` is still
`policy.Unix().Check(r) == nil`, and `AccessBits` is still
`policy.AccessBits`. What changes is that `UnixAllows` now allows more than
`Permits` on a regular file, which is §1's and §2's, and `EffectAuthorized`,
which is stated through `UnixAllows`, reads as it did.

### 8. Cross-references, and `CHANGES`

This ADR replaces two parts of ADR 0014, under `docs/adr/README.md`'s rule
for a part a later ADR replaces: §5's bullets for the `OpRead` row and for
the `OpWrite` and `OpTruncate` row, and §11's U1 and U2. ADR 0014 gains a
`Revised` line, cross-references only, and one sentence in each of §5 and
§11. Its text stays as the record, and every test it pins stands.

ADR 0018 is not revised, and the register `docs/adr/exemptions.md` does not
change (Assumption 17). The decision stays a pure policy in
`internal/policy`, with no inline condition, so the policy rule is kept and
no exemption is granted. No state is added, so the state rule and the link
reach nothing. E12, `ACCESS`'s read-only mask, stands. `docs/DESIGN.md`'s
table of ADRs gains this ADR's row, and `docs/adr/README.md`'s index does.

Two `CHANGES` lines, neither `BREAKING:` (Assumption 15), proposed for the
session to record, newest first:

> The owner of a regular file can read, write and truncate it whatever its
> mode, as RFC 1813 asks of a server that cannot see which files a client
> has open: a WRITE to a file its creator made read-only, which git's
> object files and tar -x or cp -p of a read-only file need, is no longer
> refused with NFS3ERR_ACCES. ACCESS still reports only what the mode bits
> give, so a client that asks before it opens still refuses to open such a
> file for writing; a client that opens a name by sending CREATE asks
> nothing, so a CREATE that carries a size now truncates its caller's own
> read-only file, and the writes that follow succeed, where before the
> server refused both. A directory's owner is still held to the directory's
> mode.

> A caller with execute but not read permission on a regular file can read
> it, so an execute-only program can be run from the mount, where before its
> READ was refused with NFS3ERR_ACCES. ACCESS still reports execute without
> read, so a client that asks before it opens still refuses to open such a
> file for reading; but an execute-only mode no longer keeps a file's
> contents from a caller who may execute it and either reads it over NFS
> directly, or can also write the file's directory and opens the file by
> creating it, which a client does not check.

The first line's clause on `CREATE` was added on 2026-11-15, for §4's path
that asks no `ACCESS`. As first proposed the line said that "a client still
refuses to open such a file for writing", which that path makes untrue. The
second line was reworded later that day, for the same path: as first
proposed it named only a caller who "reads it over NFS directly", and
`reviewer` found that a user behind a kernel client who can write the
file's directory reads it too (§2). The set-ID exception changes neither
line: it is not confirmed, and what it rests on is older than this change
and is #155's to record when it is fixed (Assumption 21).

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation, and Issue #93**, under the owner's
   2026-10-10 words, as ADRs 0015 to 0024 were. The issue's done-when asks
   for the owner override and for `ACCESS` to stay as it is. The rest was
   mine, and two parts of it were the ones an owner was most likely to weigh
   differently: that reading with execute permission is allowed (§2), and
   that the override stops at regular files and gives a directory's owner
   nothing (§1). By the session's report the owner confirmed both on
   2026-11-13 (*Context*, decision 3).
2. **Read, not run, at `900b5b7`.** Every statement about the code was
   checked by reading it there; nothing was run. #93's text is as the
   session gave it. The titles of #69, #94, #97, #98, #99, #100 and #101
   are as ADR 0014's *Context* gives them, and their texts were not read.
3. **A regular file only** (§1). RFC 1813 §4.4 speaks of "the owner of a
   file" that it has open, and of "an executable file". Nothing opens a
   symlink for reading or writing, `blobfs` makes no other type, and a type
   decoded from a bucket is not validated (ADR 0015, Assumption 15), so an
   override keyed on "not a directory" would reach objects nobody meant it
   for. Linux's server gives both departures to regular files only. Stopping
   there also leaves U3's rows allowing exactly what they allowed, so #69 is
   untouched.
4. **The owner is `Caller.UID == Object.UID`** (§1), ADR 0014 §5's term,
   with no exception for any uid, 4294967295 included (ADR 0010, *What this
   does not decide*). The row reads the inode's uid as it stands at the
   check.
5. **No directory row changes** (§1). On a local filesystem a lookup, a
   create, a remove and a rename check the directory's mode when they are
   made, not at an open, so there is no open-time permit for the server to
   stand in for. Reading an open directory is the one exception, and Linux's
   server declines it too, having "never" given directories the override.
   The issue names files. That none of the workloads it lists needs more,
   `tar` and `cp -p` setting a directory's mode after they have filled it,
   is from recall of those tools and was not checked.
6. **Read with execute is allowed, by one class, for reads only** (§2). The
   issue asks only that it be decided. The class is the one `Permits`
   judges by, so a group member gains nothing from the others' execute bit;
   that is what "through ownership, group membership or public access"
   means under ADR 0014 §5's rule.
7. **The refusal stays `vfs.ErrAcces`, after the type check** (§1), as the
   rows have it now, so P5 and ADR 0007 §4's order are untouched.
8. **`Permits` is not changed, and no name is added** (§1). An override
   inside `Permits` would reach `AccessBits`, `OpSetTimes` and every
   directory row at once. A new exported predicate for "the mode's verdict"
   is not needed while `Permits` is that predicate. `fileAccess`'s shape is
   coder's.
9. **`ACCESS` is not widened** (§3). The done-when and §4.4 both ask for it.
   The cost is that `ACCESS` and the operations differ by design, which §3
   states so that it is not later taken for a defect.
10. **What #97 has to reconcile is my reading** (§3) of its title and of
    what ADR 0014 §5, §7 and §11 leave to it. Its text was not read. If #97
    asks for something else, its ADR says so; nothing here binds it but
    §3's direction and the pin of A4.
11. **The security judgement** (§4) is mine, and is the part of this ADR
    that most deserves a second reading. It rests on three things read and
    not run: that `OpChmod` allows an owner any mode, which ADR 0014 §5
    pins; that any local process can claim any uid, which ADR 0015 records;
    and that a kernel client asks `ACCESS` at open (Assumption 16). The one
    case where the first does not hold, a mount that takes no change, is
    named in §4 and not argued away. The second reading was
    `security-auditor`'s, and it found a gap in two of the three. The third
    does not hold for an open that creates (Assumption 20). And the first
    may not give an owner a way back to the same mode for a set-ID file
    (Assumption 21). §4 names both since 2026-11-15, the second as not
    confirmed.
12. **U3's refusing half is pinned as a refusal only** (§1, §6). Without it
    no test could tell lines 3 and 4 from the same lines without "a regular
    file", and Assumption 3 would be review's alone. It pins that such a
    call is refused and not the error, as U6 pins its refusal, so that it
    stands whatever error #69's fix gives. It assumes that #69's fix allows
    a symlink nothing that is refused today; an ADR that did would revise
    this pin.
13. **The test surface** (§6). Two new files, named so that coder and review
    find them. A2's rows go into P4's and P5's own tables, and not a table
    of their own, so that P10 to P13 ask them too, P13 above all. A3 and A8
    are characterisations, pinned so that a change which put the override
    into `Permits` or into a directory row fails on a test that names the
    decision, where today P2, P3, P9 and a few rows of P4 would fail, none
    of which names it. A7 and A9 change their files' modes after writing,
    so that their setup passes at `900b5b7` and the red run shows the
    decision alone. A9's diverged subtest reuses U9's recipe, which is
    already proven. The comments of two existing test files change and no
    assertion does. A10 was added on
    2026-11-15, in a third new file, because §4 now counts the creating
    open among what is given up, and what it says a `CREATE` answers on the
    wire had only been read. Without a test that statement would rest on
    `Server.create` staying as ADR 0007 describes it. The status of its
    other caller's `CREATE` is left unpinned, so that a fix for the dropped
    error (ADR 0007, Assumption 9) changes no assertion. A1's grid of types
    was widened by test-author in this PR, after the first revision of
    2026-11-15, from `p2Types` to `oaAllTypes` (§6). `reviewer` says that it
    followed a third finding of `security-auditor`'s. The architect was
    given neither that finding's text nor the brief the widening was
    written from, and records the reason the test's own comment gives. The
    widening adds no rule: §1 already holds lines 3 and 4 to `vfs.TypeReg`,
    and Assumption 12 already pins the refusing half of U3 for every other
    type. It adds five types to those a test asks, so that the pin does
    what Assumption 12 says it is for.
14. **`policy.go` alone, no trace pass, no TLC** (§7). The rows are the only
    place the decision lives since ADR 0014 §6's wiring, so `blobfs` needs
    no edit, not even to a comment: `Access`'s doc comment, that it answers
    from the mode bits through `policy.AccessBits`, is still exact.
15. **Not breaking, and two `CHANGES` lines** (§8). No deployment has to act
    to keep working: calls that were refused are now allowed, and nothing
    that worked stops. They are two lines because they are two changes a
    client can tell apart. The second line's last clause is there because an
    operator who relied on an execute-only mode to hide a file's contents
    should learn of it; that is a notice, not an action a deployment needs
    in order to keep working. By the session's report the owner confirmed
    on 2026-11-13 that the second line carries no `BREAKING:` (*Context*,
    decision 3). The first line gained its clause on `CREATE` on 2026-11-15
    (§8; Assumption 20), and takes no `BREAKING:` for the same reason: it
    tells an operator of a protection lost, and asks nothing of a
    deployment for it to keep working. That judgement is mine. The second
    line was reworded on 2026-11-15 too, to name the user behind a kernel
    client (§8). The owner's confirmation of 2026-11-13 was of the line as
    first proposed. That the reworded line still takes no `BREAKING:` was
    mine, for the same reason, until the owner confirmed it on 2026-11-15,
    by the session's report (*Context*, decision 3).
16. **What a kernel client does is expected, not observed.** That git's
    object files, and `tar -x` or `cp -p` of a read-only file, fail on the
    mount is the issue's statement, which was itself "not reproduced against
    a kernel client". That they fail because each creates its file with a
    read-only mode and then writes it is my reading of the issue, from
    recall of those tools, and was not checked. That Linux's client asks
    `ACCESS` when a file is opened, and for an exec asks for execute alone,
    is from a summary of its `fs/nfs/dir.c` (*Sources*). macOS's client was
    not read. Nothing was run against either. For Assumptions 20 and 21
    more of Linux's client and of its VFS was read on 2026-11-15, at `v6.6`
    and `v6.12`, as single lines the tool quoted, and none of it was run.
17. **Nothing else changes.** `internal/vfs` says nothing of permission
    rules today, and gains nothing: the rules are `internal/policy`'s, which
    the backend that replaces `blobfs` reuses (ADR 0014, Assumption 3). The
    README's first limitation already says that permissions bind no caller
    that speaks the protocol, and needs no word.
18. **The UNCHECKED `CREATE`** (§1) follows from the row and is not a
    decision of its own: the handler's `SetAttr` is a size change by the
    file's owner, which the done-when asks to be allowed. That the handler
    drops that call's error is ADR 0007's Assumption 9 and is not touched.
    Since 2026-11-15 §4 counts it among what is given up, because no
    `ACCESS` precedes it (Assumption 20).
19. **#148** was named in the brief with no title and no text, and the
    architect cannot read the tracker. §5 therefore says only what this PR
    contains.
20. **The creating open** (§4). The truncation is `security-auditor`'s
    finding, which I confirmed by reading `Server.create`, `createEntry` and
    the row: the caller needs write and search on the directory, the name
    must be its own regular file, and the `CREATE` must be UNCHECKED and
    carry a size. That the descriptor the client then holds also writes the
    file is my addition, from the same reading: the handle `Create` returns
    is the file's, and line 3 allows its owner's `WRITE`. How often a client
    takes the path was not run, and is read from Linux's client alone
    (*Sources*). At `v6.6`, `nfs_create` sets no size; that the VFS there
    calls it only for a name the client's own lookup found absent is from
    recall, and was not fetched. At `v6.12` the NFSv3 directory operations
    have `nfs_atomic_open_v23`, which for an open with `O_CREAT` of a name
    the client holds no positive entry for sends `CREATE` with no lookup
    first, with size 0 for `O_TRUNC`, and marks the file created, after
    which `do_open` checks no permission. So there a shell's `> f`, of a
    read-only file its user owns and the client has not looked up, is
    expected to empty `f` and write it. macOS's client was not read. That
    Linux's server answers the same way is read from
    `nfsd3_create_file`, `nfsd_create_setattr` and `nfsd_get_write_access`
    with what *Context* quotes of `nfsd_setattr`. A remedy would be a
    decision about `CREATE` and not about these rows (*What this does not
    decide*), and none is made here: the done-when asks that the owner may
    truncate, and the server cannot tell the writes that follow a creating
    open from any other write of the owner's. On 2026-11-15 `reviewer`
    found that the same path serves the second widening, which the first
    revision had missed, and the same reading confirms it. `createEntry`
    asks `OpAddEntry` of the directory and nothing of the file before it
    returns an existing regular file to a non-exclusive create, which
    ADR 0014 §11's W2 pins. A file the client marks created is opened with
    no permission asked, whatever the open is for: `may_open` is then given
    no access to check, and `nfs_permission` asks nothing for a mask with
    none of read, write and execute. And line 4 allows the reads. So the
    condition is execute on the file, write and search on its directory,
    and a client that opens by creating: Linux's at `v6.12` for any open
    with `O_CREAT`, `O_RDONLY|O_CREAT` included, of a name it holds no
    positive entry for. A shell's `<>` is such an open, by my recall of how
    POSIX defines it, which was not fetched. The client asks no `ACCESS` of
    the directory either, leaving that check to the server, so the
    directory's mode is what bounds the path. No test is added for this
    half: W2 pins what `Create` asks and returns, and A6 pins the read.
21. **The set-ID exception is not confirmed** (§4), and I doubt its premise.
    The finding has a Linux client drop S_ISGID from a `chmod` by an owner
    outside the file's group before it sends `SETATTR`. As I read Linux
    `v6.6` (*Sources*), `nfs_setattr` neither calls `setattr_prepare`, where
    the VFS clears that bit, nor masks it; `notify_change` does not call
    `setattr_prepare` either; and `nfs_setattr` drops a mode change that is
    only "for clearing setuid/setgid", leaving that to the server. The
    reading covers `nfs_setattr` and the first 1000 lines of its file, and
    was not run; another client, or another kernel, may differ. If it
    holds, an owner outside the group could change such a file's contents
    with the bit kept at `900b5b7`, by the mode route, since `applySetAttr`
    stores the mode it is sent, and the override adds no exception. §4
    gives both readings and what a run needs, and does not choose between
    them.
22. **Not waiting for #155** (§4) is mine, and is the session's reading
    too. #155 was filed by the session from the architect's first report on
    this ADR; what it describes was found by reading `bufferWrite` and
    `truncate`, and not reproduced. The judgement rests on ADR 0015's
    `nosuid`, which is a property of the mount commands strata gives and
    not of every mount that can be made; on the gap being older than the
    override; and on the remedy belonging to the write and not to the rows.
    It would be the wrong call if running set-ID programs from a strata
    mount made without `nosuid` were a supported use, and ADR 0015 already
    tells a deployment that relies on it to move such programs off strata
    (its Assumption 7). That this server also leaves S_ISGID set on a
    change of mode by an owner outside the file's group, and S_ISUID and
    S_ISGID set on a change of group, where POSIX has `chmod` and `chown`
    clear them (*Sources*), is read from `applySetAttr`, and is put to #155
    in a comment, not decided here.
23. **The corrected cost of §2 was put to the owner, who accepted it**
    (§2, §4). The owner confirmed §2 on 2026-11-13 on a cost confined to a
    caller that reads over NFS directly. `reviewer` then found that a user
    behind a kernel client reads an execute-only file too, if it has write
    and search on the file's directory and its client opens by creating.
    This ADR did not re-decide §2. The session put the corrected cost to
    the owner on 2026-11-15, and reports that it gave the cost so: a user
    behind a Linux 6.12 or later kernel client who may execute a regular
    file, and who has write and search on its directory, can read it with
    ordinary tools through a creating open. It gave three options: to
    accept the cost, to close the `CREATE` route first, or to drop reading
    with execute. By its report, which gave the owner's choice of an option
    and not the owner's words, the owner chose to accept the cost
    (*Context*, decision 3). What follows is my own reasoning, kept, by the
    session's word, as the reasoning put to the owner; it is not the
    decision, which is the owner's. The rule is still RFC 1813's, and what
    Linux's server does, where the same open reads the same file.
    The user it adds already holds write on the file's directory, so it
    could already remove the file or put another in its place; what is new
    is that it can read it. Under this server's trust model that user could
    read it before, by speaking the protocol. And without line 4 an
    execute-only program does not run from the mount. So I would still
    allow it. What does change is where the cost falls: on ordinary users
    of a recent Linux client, with ordinary tools, and no longer only on a
    caller that speaks the protocol. If that is more than the owner
    accepts, there are two remedies, and neither is taken here. The
    narrower keeps line 4 and closes this route: the decision about
    `CREATE` that *What this does not decide* names. The wider strikes
    line 4, and leaves execute-only programs unrunnable. The owner accepted
    the cost, so neither remedy is taken, and the decision about `CREATE`
    stays open.

## Alternatives considered

- **The override inside `Permits`.** One line, but it would make `ACCESS`
  report the override, against §4.4 and the done-when, and give a
  directory's owner create and remove in a directory whose mode forbids them
  (Assumption 8).
- **The override for everything that is not a directory.** It reaches
  symlinks and unvalidated types for no caller's benefit, and moves U3
  (Assumption 3).
- **The override for directories too, or for `OpReadDir` alone.** No local
  semantics to stand in for but a listing on an open directory, which
  Linux's server has never given and no named workload needs (Assumption 5).
- **`ACCESS` reporting what the operations allow.** A client would then let
  an owner open a read-only file for writing (§3).
- **Refusing the owner's truncation of a read-only file**, which Linux's
  comment weighs so that `cp foo bar` fails at the server. It rejects it,
  since a process may hold a descriptor to a file of mode 0 and truncate it,
  and the done-when asks for truncation.
- **Remembering which caller opened or created which file**, and giving the
  override only then. NFSv3 has no open, and state kept for it would have to
  survive a restart to be right.
- **No read with execute.** An execute-only program stays unrunnable, which
  the issue reports, to keep a secrecy that this server's trust model does
  not provide (§2, §4).
- **Fixing #97 here.** It is its own decision (ADR 0014, Assumption 21),
  and §3 leaves it open in both of the ways it could go.
- **A request that asks for the mode's verdict, added now for #97.** Nothing
  would ask it yet.
- **The check inline in `blobfs`.** The policy rule forbids it (ADR 0018
  §1).
- **A TLA+ model of the rows.** They are functions of one request, which A1
  enumerates (Assumption 14).
- **Withholding the override from a file with a set-ID bit**, or holding
  this ADR until #155 is decided. The first refuses a write that a local
  filesystem allows and would be retracted when #155 lands; the second
  keeps every honest client broken for a bit that has no effect on a mount
  made as ADR 0015 has it (§4; Assumption 22).

## Consequences

- A file created read-only can be written by its creator, an owner's read,
  write and size change no longer depend on its mode, and an execute-only
  program's pages can be read. The workloads the issue names are expected to
  work on a mount; that is not yet observed (Assumption 16).
- `ACCESS` and the operations differ by design, in two stated ways, and
  every later change to either keeps that or decides against this ADR.
- An execute-only mode no longer keeps a regular file's contents from a
  caller that may execute it: one that sends `READ` itself, or a user
  behind a kernel client who has write and search on the file's directory
  and opens the file by creating it (§2, §4; Assumptions 20 and 23).
- On a mount that takes no change, a file's mode no longer bounds what its
  owner's uid may read.
- A read-only mode no longer protects a file from its owner's own creating
  open. A client that takes the name to be absent sends `CREATE` with no
  `ACCESS` first; a size it carries truncates the file, and the writes that
  follow succeed, where before the server refused both. On Linux's client
  at `v6.12`, as read, that is every open with `O_CREAT` of a name it has
  not looked up (§4; Assumption 20).
- A set-ID file's bits survive an override write, as they survive every
  write on this server. Whether that gives an owner behind a kernel client
  anything the mode route did not is not confirmed, and #155 decides the
  remedy (§4; Assumptions 21 and 22).
- A design that tightens the trust model, root squash or an authenticated
  peer, inherits both departures (§4).
- U1 and U2 are pinned, and U3's refusing half is, as a refusal.
- The backend that replaces `blobfs` gets the same rows through
  `internal/policy`.
- Hard to change later: the override, once clients' workloads rest on it;
  and `Permits` as the mode-bit predicate apart from the rows, which #97
  builds on.

## What this does not decide

- #69, #94, #97, #98, #99, #100 and #101 (§5), and the rows U3 to U6.
- Root squash and the AUTH_SYS trust boundary (#6, #90, #95), and whether
  either departure should narrow under them.
- An override for `READDIR` of a directory its owner holds open
  (Assumption 5).
- Whether a `WRITE` or a size change clears a file's set-user-ID and
  set-group-ID bits, which is #155. POSIX says of a write that they "may be
  cleared" (*Sources*); nothing here clears them, before this ADR or after.
- Whether a change of mode by an owner outside the file's group clears
  S_ISGID, and a change of group S_ISUID and S_ISGID, as POSIX has `chmod`
  and `chown` do (*Sources*). Neither is cleared here. #155's title names
  neither, and its ADR takes them or says which issue does (§4;
  Assumption 22).
- Whether a `CREATE` of a name that exists should be judged as the open it
  stands for: the file returned only to a caller the mode gives read or
  write, its size judged by the mode alone, with no override, and a refusal
  returned to the client. Today `Create` asks nothing of the file, and the
  handler asks of the size as of any size change and drops the error
  (ADR 0007, Assumption 9; Assumption 18). That would close §4's path for a
  reader with execute alone, and for a `CREATE` that truncates. It would
  not close it for the writes that follow a `CREATE` with no size, which
  the server cannot tell from any other write of the owner's
  (Assumptions 20 and 23).
- A test against a kernel client (Assumption 16).
- What `vfs.FS`'s doc comments say of permissions (Assumption 17).

## Sources

Fetched on 2026-11-13, while this ADR was written, through a tool that
summarises what it fetches and quotes at most about 125 characters at once,
so a longer passage came as consecutive pieces, which are joined here with a
space. It reads a long page 100,000 characters at a time.

- **RFC 1813**, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>, 229,793 characters. A first
  fetch returned characters 0 to 100,000, which do not reach §4.4; a second
  returned 170,000 to the end, which hold it; 100,000 to 170,000 were not
  read. §4.4's paragraph on the open operation, its nine sentences, of which
  *Context* quotes the second, the third and the fifth to the ninth, and the
  fourth under `ACCESS`; and its paragraph on paging in an executable, its
  six sentences, of which *Context* quotes the second to the sixth. Its next
  paragraph was read too: "A UNIX server by default maps uid 0 to a
  distinguished value (UID_NOBODY), as well as mapping the groups list,
  before doing its access checking." (*Context*; §1 to §4)
- **Linux `fs/nfsd/vfs.c`**, at the tag `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/nfsd/vfs.c>,
  which the tool said it was given whole. The tool returned these as code:
  `nfsd_permission`, from its comment on the file owner to
  `return err? nfserrno(err) : 0;`, with
  `if ((acc & NFSD_MAY_OWNER_OVERRIDE) && uid_eq(inode->i_uid, current_fsuid())) return 0;`
  and, under `/* Allow read access to binaries even when mode 111 */`,
  `if (err == -EACCES && S_ISREG(inode->i_mode) && (acc == (NFSD_MAY_READ | NFSD_MAY_OWNER_OVERRIDE) || acc == (NFSD_MAY_READ | NFSD_MAY_READ_IF_EXEC))) err = inode_permission(&nop_mnt_idmap, inode, MAY_EXEC);`;
  `nfsd_open`'s comment and
  `if (type == S_IFREG) may_flags |= NFSD_MAY_OWNER_OVERRIDE;`;
  `nfsd_setattr`'s
  `if (iap->ia_valid & ATTR_SIZE) { accmode |= NFSD_MAY_WRITE|NFSD_MAY_OWNER_OVERRIDE; ftype = S_IFREG; }`;
  `nfsd_read`'s `err = nfsd_file_acquire_gc(rqstp, fhp, NFSD_MAY_READ, &nf);`;
  the tables `nfs3_regaccess`, `nfs3_diraccess` and `nfs3_anyaccess`; and
  `nfsd_access`'s loop, with
  `err2 = nfsd_permission(rqstp, export, dentry, map->how);`. Line breaks
  inside a statement are written here as spaces. That a `READ` of a regular
  file reaches `nfsd_permission` with the override is my reading of
  `nfsd_read` with `nfsd_open`; the path between them was not read.
  (*Context*; §1 to §3; Assumptions 3 and 5)
- **Linux `fs/nfsd/vfs.c`**, at `master`:
  <https://raw.githubusercontent.com/torvalds/linux/master/fs/nfsd/vfs.c>.
  `nfsd_permission`'s comment and its owner test read as at `v6.6`; the
  execute rule's condition is there
  `(((acc & NFSD_MAY_MASK) == NFSD_MAY_READ) && (acc & (NFSD_MAY_OWNER_OVERRIDE | NFSD_MAY_READ_IF_EXEC)))`.
  **Honesty note:** the tool reported that at `master` the NFSv3 access
  tables are no longer in this file, `nfsd_access` taking them from its
  caller, and `fs/nfsd/nfs3proc.c` at `master` answered
  `429 Too Many Requests`, so the tables are cited from `v6.6` alone.
  (*Context*)
- **Linux `fs/nfs/dir.c`**, the client, at `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/nfs/dir.c>,
  characters 80,000 to 87,614, its end. **Honesty note:** the tool declined
  to quote whole functions and summarised them, so this is secondhand:
  `nfs_open_permission_mask`, "With `__FMODE_EXEC` it checks only MAY_EXEC.
  Otherwise it adds MAY_READ and/or MAY_WRITE"; `nfs_may_open` passes that
  mask to `nfs_do_access`; `nfs_permission` "calls `nfs_do_access` and, for
  exec, `nfs_execute_ok`". It quoted one line of `nfs_do_access`:
  `status = NFS_PROTO(inode)->access(inode, &cache, cred);`.
  (§2, §4; Assumption 16)
- **POSIX.1-2024, `write()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/write.html>,
  which gives itself as The Open Group Base Specifications Issue 8, IEEE Std
  1003.1-2024. Of a successful write of more than no bytes to a regular
  file, the tool quoted the clause "the S_ISUID and S_ISGID bits of the file
  mode may be cleared." and not the sentence whole. (*What this does not
  decide*)

Fetched on 2026-11-15, for the revision of that date, through the same
tool, which this time gave code as single lines of under about 120
characters; a statement that spans lines is written here on one. Where it
answered a question about the code in its own words, this says "the tool
said".

- **Linux `fs/nfs/dir.c`**, the client, at `v6.6`, the URL above, characters
  55,000 on: `nfs_create`'s
  `int open_flags = excl ? O_CREAT | O_EXCL : O_CREAT;`,
  `attr.ia_mode = mode;` and `attr.ia_valid = ATTR_MODE;`. The tool said
  that no line of `nfs_create` tests `O_TRUNC` or sets a size, and that the
  text holds no `nfs_atomic_open_v23` and no `nfs_do_create`. And at
  `v6.12`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.12/fs/nfs/dir.c>,
  characters 55,000 on: `nfs_do_create`'s `open_flags |= O_CREAT;`,
  `attr.ia_valid = ATTR_MODE;` and
  `if (open_flags & O_TRUNC) { attr.ia_size = 0; attr.ia_valid |= ATTR_SIZE; }`;
  `nfs_create`'s `return nfs_do_create(dir, dentry, mode, excl ? O_EXCL : 0);`;
  and `nfs_atomic_open_v23`, under
  `/* Same as look+open from lookup_open(), but with different O_TRUNC handling. */`,
  whose branch for `O_CREAT` is
  `if (open_flags & O_CREAT) { file->f_mode |= FMODE_CREATED; error = nfs_do_create(dir, dentry, mode, open_flags); if (error) return error; return finish_open(file, dentry, NULL); }`.
  The tool said that `nfs_lookup` is called only in the other branch,
  `else if (d_in_lookup(dentry))`. (§4; Assumption 20)
- **Linux `fs/nfs/nfs3proc.c`**, the client, at `v6.12`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.12/fs/nfs/nfs3proc.c>.
  `nfs3_dir_inode_operations`' `.create = nfs_create,` and
  `.atomic_open = nfs_atomic_open_v23,`; and `nfs3_proc_create`'s
  `data->arg.create.createmode = NFS3_CREATE_UNCHECKED;`,
  `if (flags & O_EXCL) {`,
  `data->arg.create.createmode = NFS3_CREATE_EXCLUSIVE;` and
  `data->arg.create.sattr = sattr;`. The tool said that it makes no `ACCESS`
  call before the `CREATE`. (Assumption 20)
- **Linux `fs/namei.c`**, at `v6.12`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.12/fs/namei.c>,
  characters 95,000 on. `do_open`'s
  `if (file->f_mode & FMODE_CREATED) { /* Don't check for write permission, don't truncate */ open_flag &= ~O_TRUNC; acc_mode = 0; }`
  and `error = may_open(idmap, &nd->path, acc_mode, open_flag);`; and
  `lookup_open`'s
  `if (dentry->d_inode) { /* Cached positive dentry: will open in f_op->open */ return dentry; }`
  and
  `if (dir_inode->i_op->atomic_open) { dentry = atomic_open(nd, dentry, file, open_flag, mode);`.
  (Assumption 20)
- **Linux `fs/nfsd/nfs3proc.c`**, the server, at `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/nfsd/nfs3proc.c>.
  `nfsd3_create_file`'s `if (d_really_is_positive(child)) {`, with
  `case NFS3_CREATE_UNCHECKED: if (!d_is_reg(child)) break; iap->ia_valid &= ATTR_SIZE; goto set_attr;`
  and `case NFS3_CREATE_GUARDED: status = nfserr_exist; break;`; and
  `set_attr: status = nfsd_create_setattr(rqstp, fhp, resfhp, &attrs);`. The
  tool said that this status is the one the function returns. (§4;
  Assumption 20)
- **Linux `fs/nfsd/vfs.c`**, at `v6.6`, the URL above, again:
  `nfsd_create_setattr`'s `if (iap->ia_valid)` and
  `status = nfsd_setattr(rqstp, resfhp, attrs, 0, (time64_t)0);`;
  `nfsd_get_write_access`'s
  `err = nfsd_permission(rqstp, fhp->fh_export, fhp->fh_dentry, NFSD_MAY_TRUNC | NFSD_MAY_OWNER_OVERRIDE);`;
  and `nfsd_sanitize_attrs`' `/* Revoke setuid/setgid on chown */`. The tool
  said that the file's write path has no code that clears set-ID bits.
  (§4; Assumptions 20 and 22)
- **Linux `fs/nfs/inode.c`**, the client, at `v6.6`, from GitHub's
  rendering, <https://github.com/torvalds/linux/blob/v6.6/fs/nfs/inode.c>,
  since `raw.githubusercontent.com` answered `429 Too Many Requests` for it
  and `git.kernel.org` `403 Forbidden`. The page gave the first 1000 of the
  file's 2540 lines, without their indentation. `nfs_setattr`, from its
  opening to its test under
  `/* Optimization: if the end result is no change, don't RPC */`, with
  `/* skip mode change if it's just for clearing setuid/setgid */` over
  `if (attr->ia_valid & (ATTR_KILL_SUID | ATTR_KILL_SGID)) attr->ia_valid &= ~ATTR_MODE;`.
  The tool said that `nfs_setattr` does not call `setattr_prepare()` and
  does not mask S_ISGID out of `attr->ia_mode`; that `setattr_prepare` does
  not occur in what it was given; and that the one masking of S_ISGID there
  is `nfs_setattr_update_inode`'s, of the client's own `inode->i_mode`:
  `if (setattr_should_drop_sgid(&nop_mnt_idmap, inode)) inode->i_mode &= ~S_ISGID;`.
  **Honesty note:** lines 1001 to 2540 were not seen. (§4; Assumption 21)
- **Linux `fs/attr.c`**, at `v6.6`:
  <https://raw.githubusercontent.com/torvalds/linux/v6.6/fs/attr.c>, which
  answered `429 Too Many Requests` once and then came. `setattr_prepare`'s
  block under `/* Make sure a caller can chmod. */`, which ends
  `/* Also check the setgid bit! */` and
  `if (!in_group_or_capable(idmap, inode, vfsgid)) attr->ia_mode &= ~S_ISGID;`;
  `notify_change`'s lines for `ATTR_KILL_SUID` and `ATTR_KILL_SGID`, which
  turn a kill request into a change of mode, and its
  `if (inode->i_op->setattr) error = inode->i_op->setattr(idmap, dentry, attr); else error = simple_setattr(idmap, dentry, attr);`.
  The tool said that `notify_change`'s block for `ATTR_MODE` does not clear
  S_ISGID, and that `notify_change()` never calls `setattr_prepare()`.
  (Assumption 21)
- **POSIX.1-2024, `chmod()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/chmod.html>,
  The Open Group Base Specifications Issue 8, IEEE Std 1003.1-2024, in three
  pieces: "If the calling process does not have appropriate privileges, and
  if the group ID of the file does not match the effective group ID or one
  of the supplementary group IDs and if the file is a regular file, bit
  S_ISGID (set-group-ID on execution) in the file's mode shall be cleared
  upon successful return from chmod()." (§4; Assumption 22)
- **POSIX.1-2024, `chown()`**:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/chown.html>,
  the same edition, in three pieces: "If the specified file is a regular
  file, one or more of the S_IXUSR, S_IXGRP, or S_IXOTH bits of the file
  mode are set, and the process does not have appropriate privileges, the
  set-user-ID (S_ISUID) and set-group-ID (S_ISGID) bits of the file mode
  shall be cleared upon successful return from chown()." (Assumption 22)

Fetched later on 2026-11-15, for the second revision of that date, to check
`reviewer`'s finding, through the same tool and in the same form.

- **Linux `fs/namei.c`**, at `v6.12`, the URL above, characters 85,000 on:
  `may_open`'s `error = inode_permission(idmap, inode, MAY_OPEN | acc_mode);`;
  `may_o_create`'s
  `error = inode_permission(idmap, dir->dentry->d_inode, MAY_WRITE | MAY_EXEC);`;
  and `lookup_open`'s
  `if (likely(got_write)) create_error = may_o_create(idmap, &nd->path, dentry, mode);`
  and `if (create_error) open_flag &= ~O_CREAT;`. (Assumption 20)
- **Linux `fs/nfs/dir.c`**, the client, at `v6.12`, the URL above,
  characters 80,000 to 89,383, its end, which the tool said hold
  `nfs_permission` whole: `if ((mask & (MAY_READ | MAY_WRITE | MAY_EXEC)) == 0) goto out;`;
  under `case S_IFDIR:`, the comment "Optimize away all write operations,
  since the server will check permissions when we perform the op." over
  `if ((mask & MAY_WRITE) && !(mask & MAY_READ)) goto out;`; and
  `res = nfs_do_access(inode, cred, mask);`. (Assumption 20)

Nothing else was fetched. Everything else here is read from this
repository's own code and documents, cited by symbol and section.
