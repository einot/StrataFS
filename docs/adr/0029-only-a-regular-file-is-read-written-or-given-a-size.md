# 0029. Only a regular file is read, written or given a size: a symbolic link's handle is refused, and ACCESS still reports its mode

**Status:** Accepted
**Date:** 2026-11-28
**Issue:** #69

## Context

**The owner's decisions.** Four, as ADR 0014, ADR 0018 and ADR 0027 record
them: the first two in the owner's words, the third in the wording the owner
approved, and the fourth as the session reported it. Nothing here claims any
approval beyond them.

1. On 2026-10-10: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0028 were, and its design is the architect's (Assumption 1). *For the
   owner* lists the judgement calls an owner is most likely to weigh
   differently, each with its alternatives.
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." By the brief, a change a client can see still
   gets a `CHANGES` line, prefixed `BREAKING:` only where a running
   deployment must act (§12; Assumption 15).
3. On 2026-10-22, the rules CLAUDE.md carries under "State machines and
   policies", which ADR 0018 records: a decision about whether a caller may
   do something is a pure policy in `internal/policy`, not an inline
   condition, and only an ADR exempts code from that.
4. On 2026-11-23, by the session's report, which ADR 0027 records (its
   *Context*, decision 4) and a comment on #69 repeats: an object that is
   neither a regular file nor a directory keeps the `ACCESS` bits it has
   "until #69". This ADR is where that interim ends. §3 keeps the bits, as
   the architect's recommendation, and *For the owner* puts it to the owner
   again, since "until #69" decided nothing past this point.

**The defect.** #69, "SETATTR with a size, and WRITE, accept a symlink's
handle", as the session gave its text (Assumption 2). "`truncate` (reached
from `SetAttr` when `sa.Size` is set) and `Write` refuse only directories
(`ErrIsDir`). Neither checks for a symlink." "RFC 1813 expects WRITE and a
size change to apply to regular files only. For other types, servers
typically answer `NFS3ERR_INVAL`." Its consequences: a symlink "can be grown
up to `MaxFileSize`", and "then holds up to 2^20 chunk references like a
large file"; and ADR 0007 §6's "regular-files-only filter is therefore
untested: this defect is the only way to put a non-regular inode above the
limit". It was "Found by reading the code; not reproduced by a test." Its
done-when:

- "`SetAttr` with a size, and `Write`, on a symlink (and any other
  non-regular, non-directory inode) fail with an error `architect` chooses
  (probably `ErrInval` → `NFS3ERR_INVAL`), before anything is changed."
- "Clean-room tests cover both calls on a symlink."
- "ADR 0007's mount-warning filter gets a test, if a reachable way to put a
  non-regular inode above the limit remains."

A comment on the issue, of 2026-11-23, adds that `ACCESS` asks the same rows
since ADR 0027, so that a change to them for a symlink changes what `ACCESS`
reports for it, and gives the one constraint ADR 0027 §6 found: a macOS
client's `readlink(2)` needs `ACCESS` to grant the symlink READ.

**What the code has.** Every statement about the code here was checked by
reading it at `939f2b2`, the commit this ADR's branch was cut from, which is
`origin/main` just after #170 was merged. Nothing was run (Assumption 2).
Code is cited by symbol.

- `policy.Unix()`'s `Check`, in `internal/policy/policy.go`, answers `OpRead`
  with `fileAccess(c, o, PermRead, r.Advisory)`, and `OpWrite` and
  `OpTruncate` with `fileAccess(c, o, PermWrite, r.Advisory)`. `fileAccess`
  returns `vfs.ErrIsDir` for a directory; nil if `Permits`; nil, for a
  request that is not advisory, for a regular file's owner, and for a read of
  a regular file by a caller with execute; and `vfs.ErrAcces` otherwise. So
  an object that is neither a regular file nor a directory is judged by
  `Permits` alone, and is allowed all three wherever its mode gives the
  permission.
- `createEntry`, in `internal/blobfs/fs.go`, gives a symlink the mode
  `0o777` unless a mode is asked for, stores its target in `inode.Target`,
  and sets `inode.Size` to the target's length. A symlink has no chunk list.
  So every caller has, by the mode, read, write and execute permission on
  every symlink made with no mode.
- `ReadLink` returns `inode.Target` for a symlink and `vfs.ErrInval` for
  anything else. It takes no `vfs.Caller` and asks no policy. Nothing writes
  `inode.Target` after `createEntry`.
- `Write` calls `mutable()`; returns `(0, vfs.FileSync, nil)` for an empty
  write; refuses an offset at or past `maxFileSize` with `vfs.ErrFBig` and
  clamps a write that crosses it; then, holding `FS.mu` for reading,
  resolves the handle and asks `OpWrite`; then waits on the budget, calls
  `getOpen` and `bufferWrite`, and syncs a stable write. `bufferWrite`
  copies the bytes into the file's dirty chunks and, in its tail block,
  raises `inode.Size` to the write's end if that is larger and sets the
  times. A later flush stores the chunks and puts them in `inode.Chunks`.
- `SetAttr` with a size calls `mutable()` and then `truncate`, which refuses
  a size above `maxFileSize`; asks every request `sa` needs, `OpTruncate`
  first, holding `FS.mu` for reading; takes the commit gate, `getOpen`, the
  file's `openFile.mu` and `FS.mu` for writing; asks again; and then sets
  `inode.Size`, appending one hole reference a chunk when the size grows
  (ADR 0024 §2; ADR 0007 §4).
- `Read` resolves the handle and asks `OpRead`, in `readable`, and answers
  from `inode.Size`, the chunk list and the file's dirty buffers. An index
  past the end of the chunk list reads as zeros.
- So, at `939f2b2`, of a symlink whose target is 14 bytes long: a `READ`
  returns 14 zero bytes; a `WRITE` of 3 bytes at offset 100 leaves it with a
  size of 103 and, after a flush, one stored chunk; a `SETATTR` of size 0
  leaves it with a size of 0; and after each `READLINK` still returns the
  14-byte target.
- `internal/nfs`'s `read`, `write` and `setattr` handlers pass the status
  through `statusOf`, which passes a `vfs.Status` unchanged. Its `create`
  handler applies an UNCHECKED create's size with a `SetAttr` once `Create`
  has returned, and drops that call's error (ADR 0007, Assumption 9); and
  `createEntry` answers a `Create` that is not exclusive, of a name that
  exists and is not a directory, with that object's handle, a symlink's
  included.
- `New` logs ADR 0007 §6's record when at least one inode for which
  `isFile()` holds has a size above the limit.
- `internal/vfs/vfs.go` has `ErrInval Status = 22`, and says nothing of the
  type of object `Read`, `Write` or `SetAttr` is for. `Cloner.CloneRange`'s
  comment already has `ErrInval` report "a handle that is not a regular
  file".
- `internal/sunrpc` bounds one RPC record at `maxRecord`, 8 MiB.

**Where the issue's account and the code differ.** The issue says that a
symlink's "content is the target", and that writing to it "changes the bytes
that `READLINK` and lookups through it return". It does not: `READLINK`
returns `inode.Target`, which no `WRITE` and no size change touches, and a
client resolves a path through a link from what `READLINK` returns. What
the two calls change is the symlink's size, which `GETATTR` then reports as
something other than the target's length, its times, and the file data they
give it, which a `READ` of its handle returns and which costs chunk
references in every snapshot. The defect is real and the done-when stands;
the damage is to the attributes and to storage, not to where the link
points. The session may want to correct the issue's text (Assumption 2).

**What RFC 1813 says** (*Sources*).

- §3.3.6, of `READ`'s argument `file`: "The file handle of the file from
  which data is to be read. This must identify a file system object of type,
  NF3REG." Its ERRORS: NFS3ERR_IO, NFS3ERR_NXIO, NFS3ERR_ACCES,
  NFS3ERR_INVAL, NFS3ERR_STALE, NFS3ERR_BADHANDLE, NFS3ERR_SERVERFAULT.
- §3.3.7, of `WRITE`'s argument `file`: "The file handle for the file to
  which data is to be written. This must identify a file system object of
  type, NF3REG." Its ERRORS: NFS3ERR_IO, NFS3ERR_ACCES, NFS3ERR_FBIG,
  NFS3ERR_DQUOT, NFS3ERR_NOSPC, NFS3ERR_ROFS, NFS3ERR_INVAL, NFS3ERR_STALE,
  NFS3ERR_BADHANDLE, NFS3ERR_SERVERFAULT.
- §3.3.2, of `SETATTR`: "The new_attributes.size field is used to request
  changes to the size of a file." It does not say which types of object a
  size may be set on. Its ERRORS include NFS3ERR_INVAL, and its
  IMPLEMENTATION gives that status to requests a server cannot honour: "The
  error, NFS3ERR_INVAL, may be returned if the server can not store a uid or
  gid in its own representation of uids or gids, respectively."
- §2.6, of `NFS3ERR_INVAL`: "Invalid argument or unsupported argument for an
  operation. Two examples are attempting a READLINK on an object other than
  a symbolic link or attempting to SETATTR a time field on a server that
  does not support this operation." Of `NFS3ERR_ISDIR`: "Is a directory. The
  caller specified a directory in a non-directory operation." None of the
  three procedures lists `NFS3ERR_ISDIR`, which this server answers for a
  directory, as Linux's does.
- §3.3.5, of `READLINK`'s argument: "The file handle for a symbolic link
  (file system object of type NF3LNK)."

**How Linux's server answers**, `fs/nfsd` at the tag `v6.6` (*Sources*).

- `nfsd_mode_check`, which `fh_verify` calls before `nfsd_permission`,
  compares the object's type with the type the procedure asks for, and for
  NFSv3 ends: `if (requested == S_IFDIR)` `return nfserr_notdir;`
  `if (mode == S_IFDIR)` `return nfserr_isdir;` `return nfserr_inval;`. So a
  wrong type is answered before any permission is looked at, a directory
  with `nfserr_isdir` and anything else with `nfserr_inval`.
- `nfsd_read` and `nfsd_write` call `nfsd_file_acquire_gc`, and
  `nfsd_file_do_acquire` verifies the handle with
  `fh_verify(rqstp, fhp, S_IFREG, may_flags|NFSD_MAY_OWNER_OVERRIDE)`.
  `nfsd_setattr`, for a request that sets a size, sets `ftype = S_IFREG;`
  and calls `fh_verify(rqstp, fhp, ftype, accmode)`. So all three are
  refused on a symlink with `NFS3ERR_INVAL`.
- `nfsd_readlink` verifies with `fh_verify(rqstp, fhp, S_IFLNK,
  NFSD_MAY_NOP)`: the type, and no permission.
- `nfsd_access` answers an object that is neither a regular file nor a
  directory from `nfs3_anyaccess`, which maps READ, EXECUTE, MODIFY and
  EXTEND by the mode (ADR 0027, *Context* and *Sources*). So Linux's server
  refuses the three calls on a symlink and still reports its permissions by
  its mode.

**What Apple's client asks of a symlink** (*Sources*; Assumption 17).

- XNU's `readlink` authorizes `KAUTH_VNODE_READ_DATA` on the symlink, which
  Apple's NFS client turns into an `ACCESS` for READ, answering `EACCES`
  when it is not granted (ADR 0027 §6 and *Sources*).
- `nfs_vnop_access`, in a block marked `/* common */` that follows its
  directory and file branches and so covers every object, asks MODIFY for
  three more actions:
  `KAUTH_VNODE_WRITE_ATTRIBUTES | KAUTH_VNODE_WRITE_EXTATTRIBUTES |
  KAUTH_VNODE_WRITE_SECURITY`, and again for `KAUTH_VNODE_CHANGE_OWNER`.
- XNU's `vnode_authattr` requires `KAUTH_VNODE_WRITE_ATTRIBUTES` of a caller
  who sets an object's times and is its owner or root, and
  `KAUTH_VNODE_WRITE_SECURITY` of one who sets its mode and is not the
  superuser. `vnode_authorize_opaque` hands the authorization of every type
  but a device, a socket and a FIFO to the filesystem's `VNOP_ACCESS`.

So, as read, a macOS client asks the server for READ before it reads a
symlink, and for MODIFY before it sets a symlink's times, mode or owner,
which is what restoring a link from an archive does.

**What the ADRs left to #69.** ADR 0007 bounds the two calls on a symlink as
on a file and leaves their acceptance open. ADR 0014 §11 leaves the row U3
unpinned: "`OpRead`, `OpWrite` or `OpTruncate` on an `Object` whose `Type` is
neither `vfs.TypeReg` nor `vfs.TypeDir`, symlinks included (#69)". ADR 0024
§2 notes that "a symlink's handle is still accepted with a size". ADR 0025
§1 gives such an object neither of its two departures, pins U3 as a refusal
only where `Permits` refuses, and assumes that #69's fix "allows a symlink
nothing that is refused today". ADR 0027 §6 pins such an object's `ACCESS`
bits as they are, does not claim §2's first law for it, and names two things
that bind this ADR; its *What this does not decide* adds "whether `READLINK`
should ask a policy". ADR 0026 and ADR 0028 list #69 among what they leave
out. §11 settles each.

**What this constrains.** Three rows of `policy.Unix()`, in
`internal/policy/policy.go`, and through them what `Read`, `Write` and a
`SetAttr` that sets a size answer for an object that is not a regular file;
ADR 0007, ADR 0014, ADR 0024, ADR 0025 and ADR 0027, by cross-reference; and
the tests of `internal/policy`, `internal/blobfs` and `internal/nfs`. No
non-test code of `internal/blobfs`, `internal/nfs` or `internal/vfs`
changes (§2; §12 says what is proposed for `internal/vfs`'s comments), and
nothing in the bucket or the flags. On the wire, `READ`, `WRITE` and a
`SETATTR` that sets a size answer `NFS3ERR_INVAL` in the cases §1 gives, and
nothing else changes.

## Decision

### 1. The rule

**A read, a write and a size change are for a regular file.** A call's
request for `OpRead`, `OpWrite` or `OpTruncate` on an object that is neither
a regular file nor a directory is refused with `vfs.ErrInval`, before any
permission is looked at. A directory is still refused with `vfs.ErrIsDir`.
An advisory request, which only `ACCESS` makes, is answered as before (§3).

**The rows.** `Unix().Check` answers `OpRead`, `OpWrite` and `OpTruncate` as
below, in place of ADR 0027 §2's rows for them, which replaced ADR 0025 §1's.
The terms are ADR 0014 §5's and ADR 0025 §1's: *root* is `c.UID == 0`, *the
owner* is `c.UID == o.UID`, *a directory* is `Type == vfs.TypeDir`, and *a
regular file* is `Type == vfs.TypeReg`. Every other `Type`, valid or not, is
*neither*: `vfs.TypeLnk`, `vfs.TypeBlk`, `vfs.TypeChr`, `vfs.TypeSock`,
`vfs.TypeFifo`, 0, and any value above 7. *Advisory* is ADR 0027 §2's mark.
Each row is read from the top, and the first line whose condition holds
gives the answer.

- `OpRead`:
  1. `vfs.ErrIsDir`, if `Object` is a directory;
  2. `vfs.ErrInval`, if the request is not advisory and `Object` is not a
     regular file;
  3. nil, if `Permits(Caller, Object, PermRead)`;
  4. nil, if the request is not advisory, `Object` is a regular file and the
     caller is its owner;
  5. nil, if the request is not advisory, `Object` is a regular file and
     `Permits(Caller, Object, PermExec)`;
  6. `vfs.ErrAcces` otherwise.
- `OpWrite` and `OpTruncate`:
  1. `vfs.ErrIsDir`, if `Object` is a directory;
  2. `vfs.ErrInval`, if the request is not advisory and `Object` is not a
     regular file;
  3. nil, if `Permits(Caller, Object, PermWrite)`;
  4. nil, if the request is not advisory, `Object` is a regular file and the
     caller is its owner;
  5. `vfs.ErrAcces` otherwise.

Line 2 of each row is new, and nothing else is. Read by the mark:

- *A request that is not advisory*, which is every request a call makes: a
  directory is `vfs.ErrIsDir`; anything else that is not a regular file is
  `vfs.ErrInval`, for every caller, root included; and a regular file is
  answered by ADR 0025 §1's lines, as before.
- *An advisory request*: a directory is `vfs.ErrIsDir`, and anything else is
  nil if `Permits` gives the row's permission and `vfs.ErrAcces` otherwise,
  whatever its type. That is ADR 0027 §2, unchanged.

The rows read `Op`, `Caller`, `Object` and `Advisory`, and nothing else; line
2 reads `Object.Type` and `Advisory` alone. Each refusal is the bare
`vfs.Status`. Everything else in ADR 0014 §5, ADR 0025 §1, ADR 0026 §1 and
ADR 0027 §2 stands as written: `InGroup`, `Permits`, `AccessBits`, the
`OpExecute` row, every other row, purity, `All` and `Func`. No name is added
to `internal/policy`, and none to `internal/vfs`.

The rows' form in the code is coder's: the helper behind them may take the
line as one more condition, or be split. Three doc comments are pinned in
substance. The helper's lists the lines above. `Check`'s says that the three
file rows are this ADR's since line 2. And `Request.Advisory`'s, which ADR
0027 §2 pins, also says that a file row does not refuse an advisory request
for an object that is neither a regular file nor a directory, whose mode
`ACCESS` still reports (§3). The package comment's `Spec:` line gains this
ADR's §1 and §3.

For a symlink owned by `501:20`, with every answer the same for `OpRead`,
`OpWrite` and `OpTruncate`:

| Mode | Caller | A call's request | An advisory request | A call's, at `939f2b2` |
|---|---|---|---|---|
| `0o777` | anyone | `ErrInval` | nil | nil |
| `0o700` | uid 501, the owner | `ErrInval` | nil | nil |
| `0o700` | `502:30`, or `503:20` | `ErrInval` | `ErrAcces` | `ErrAcces` |
| `0o000` | uid 501 | `ErrInval` | `ErrAcces` | `ErrAcces` |
| any | uid 0, root | `ErrInval` | nil | nil |

The advisory column is what it was at `939f2b2`.

**Why `vfs.ErrInval`** (Assumption 3). It is `NFS3ERR_INVAL`, which all
three procedures list, and which RFC 1813 §2.6 defines by the mirror image
of this case: "attempting a READLINK on an object other than a symbolic
link". `ReadLink` here already answers a handle that is not a symlink with
it, and Linux's server answers these three calls on a symlink with it.

**Why the type comes before the permission** (Assumption 5). Every type
check of `Unix()` comes first in its row (ADR 0014 §11's P5), and Linux's
server checks the type before the permission. So a caller the mode refuses
is told the true reason, which no change of mode or owner would cure.

### 2. Where the rows are asked, and the order of errors

**No line of `internal/blobfs` changes.** `Read`, `Write` and a `SetAttr`
that sets a size already ask these three rows, at stage D, where ADR 0014
§6 and ADR 0024 §2 place them, and return the policy's refusal unchanged.
So the rule arrives through the requests they make:

- `Write` asks `OpWrite` once, holding `FS.mu` for reading, after it has
  resolved the handle and before the budget's wait, `getOpen` and
  `bufferWrite`.
- A `SetAttr` that sets a size asks `OpTruncate` first of its requests, in
  `truncate`'s pre-check, holding `FS.mu` for reading, before the commit
  gate and `getOpen`; and again in the hold that would apply the change
  (ADR 0024 §2). An object's type never changes, so a request refused for
  its type is refused in the pre-check.
- `Read` asks `OpRead` at its step 1, holding `FS.mu` for reading, before it
  reads the size. An object that is not a regular file is refused there, so
  its step 2 is never reached for one.

**The order of errors** is ADR 0007 §4's and ADR 0014 §6's, with
`vfs.ErrInval` joining the errors of stage D:

| Call | Stage A | Stage B | Stage C | Stage D |
|---|---|---|---|---|
| `Write` | `ErrROFS`, or a diverged mount's `ErrStale` | `ErrFBig` | `ErrBadHandle`, `ErrStale` | `ErrIsDir`, `ErrInval`, `ErrAcces` |
| `SetAttr` with a size | the same | `ErrFBig` | the same | for the size, `ErrIsDir`, `ErrInval`, `ErrAcces`; then the other attributes' `ErrPerm` |
| `Read` | none | none | `ErrBadHandle`, `ErrStale` | `ErrIsDir`, `ErrInval`, `ErrAcces` |

So, of a symlink's handle:

- on a read-only mount a `Write` and a `SetAttr` with a size answer
  `vfs.ErrROFS`, and on a diverged one `vfs.ErrStale`, as every change does;
  a `Read` has no stage A and answers `vfs.ErrInval` on any mount;
- a `Write` of at least one byte at or past `MaxFileSize`, and a size above
  it, answer `vfs.ErrFBig`, since that check reads the arguments alone and
  comes before the handle is resolved (ADR 0007 §4, Assumption 7);
- a `Write` of no bytes returns `(0, vfs.FileSync, nil)` after stage A,
  whatever its handle names, as before (ADR 0007 §4; Assumption 9);
- a handle whose symlink has been removed answers `vfs.ErrStale`;
- a `SetAttr` that sets a size and other attributes answers `vfs.ErrInval`,
  whatever the other attributes would answer, and applies none of them
  (ADR 0024 §2);
- a `SetAttr` that sets no size is as it was: a symlink's mode, owner, group
  and times are set by the rows that decide them.

**What a refused call leaves.** Nothing changed. A refused `Write` has not
waited on the budget or charged it, has made no `FS.open` entry, buffered
nothing, changed no size and no time, marked nothing dirty and, for a stable
write, synced nothing. A refused `SetAttr` has taken no commit gate, made no
`FS.open` entry and applied nothing (ADR 0024 §2). `FS.dirty` and
`FS.changes` are as they were, and the next `Sync`, with nothing else dirty,
makes no store call. So, from this build on, an inode that is not a regular
file has no `FS.open` entry, no dirty buffer and no pending trim, and its
size and chunk list are what they were when it was made or loaded.

**The three ways in.** The rule closes each at the same request:

- a `SETATTR` that sets a size, and a `WRITE`, which the issue names;
- an UNCHECKED `CREATE` that carries a size and names a symlink that exists.
  `Create` returns the symlink's handle, and the `create` handler's `SetAttr`
  of the size is now refused. The handler drops that error, as it drops
  every error of that call (ADR 0007, Assumption 9), so the reply is still
  `NFS3_OK` with the symlink's handle, and the symlink is as it was.

**On the wire.** A refused `WRITE` answers status 22 and `WRITE3resfail`,
one `wcc_data`; a refused `SETATTR`, 22 and `SETATTR3resfail`, one
`wcc_data`; and a refused `READ`, 22 and `READ3resfail`, one `post_op_attr`.
Each reply ends there.

### 3. `ACCESS` reports the mode of an object that is neither

**`ACCESS` answers such an object as before.** `AccessBits` is unchanged. It
asks the second table of ADR 0027 §1, each request marked `Advisory`, and an
advisory request to the three rows is answered by the mode whatever the
object's type (§1). So an object that is neither a regular file nor a
directory is granted READ with read, MODIFY and EXTEND with write on a mount
that takes changes, EXECUTE with execute, and never LOOKUP or DELETE, by its
caller's class of the mode bits: the bits ADR 0027 §6 pinned, and its X1
still pins.

For a symlink owned by `501:20`, on a mount that takes changes:

| Mode | The owner | `503:20`, in the group | `502:30` | Root |
|---|---|---|---|---|
| `0o777`, which a symlink has unless one is asked for | R M E X | R M E X | R M E X | R M E X |
| `0o700` | R M E X | nothing | nothing | R M E X |
| `0o000` | nothing | nothing | nothing | R M E |

On a mount that takes none, M and E are withheld (ADR 0027 §4). A symlink's
mode is whatever was asked for when it was made (ADR 0028 §6) or set since,
so the answer is not always the first row's.

**What the bits speak for.** Not this server's `Read` and `Write`, which it
refuses such an object. They speak for what a client does with the object by
other means, by the client's own rules:

- READ on a symlink, for reading the link. XNU authorizes `readlink` with
  it, and a server that withheld it would stop `readlink(2)` on a macOS
  client, while `READLINK` itself asks no policy here (ADR 0027 §6).
- MODIFY on a symlink, for a change of its attributes. Apple's client asks
  it before a process sets any object's times, mode, owner or extended
  attributes, and a server that withheld it would, as read, have that client
  refuse to set a symlink's times or mode (*Context*). The `SETATTR` that
  follows is still decided by `OpSetTimes`, `OpChmod`, `OpChown` and
  `OpChgrp`.
- Any bit on a device, a socket or a FIFO, for the client's own open of an
  endpoint on its own machine. This server makes none.

**Why keep them** (Assumption 7; *For the owner*, 2).

1. Two bits are in use, READ and MODIFY, each by a client's ordinary
   operation on a symlink, and the server cannot tell which use a client
   puts a bit to.
2. The mode's verdict is what the client would compute from the attributes
   it holds, so the answer adds no permission and removes none.
3. Linux's server gives the same pair of answers: `NFS3ERR_INVAL` to the
   three calls, and the mode's bits to `ACCESS`, from `nfs3_anyaccess`.
4. Withholding EXTEND or EXECUTE alone would be a rule with no evidence
   behind it. Nothing was found that asks either of a symlink, and nothing
   that would be protected by refusing it.

**So for such an object an advisory request and a call's differ, by
design.** ADR 0027 §2's first law, that an advisory request only narrows,
is claimed for a regular file and a directory, and stays there for good. For
an object that is neither it is false since this ADR: an advisory `OpRead`,
`OpWrite` or `OpTruncate` is allowed wherever the mode allows it, and the
same request unmarked is always refused. ADR 0027 anticipated exactly this
(its §6 and Assumption 24). Its §8 stands: S2 is claimed for a regular file
and a directory, and S5 gives such an object S1 and nothing more.

**The mount gate is kept** for these objects, as ADR 0027 §6 has it.

### 4. `READLINK` asks no policy

`ReadLink` stays as it is: it resolves the handle, answers `vfs.ErrInval`
for an object that is not a symlink, and returns the target to whoever asks.
This settles what ADR 0027 left open (Assumption 8; *For the owner*, 3).

1. `vfs.FS.ReadLink` takes no `vfs.Caller`, so a policy has no caller to
   judge. Giving it one is a change to the contract every backend
   implements, for a check no client was found to need.
2. Linux's server checks the type and no permission (*Context*), and POSIX
   leaves a symbolic link's mode bits unspecified (ADR 0028, *Context*).
3. A client that does honour a symlink's mode, as macOS does, asks `ACCESS`
   first and is told the mode's answer (§3).

The cost: a symlink's mode binds only a client that asks. A caller that
sends `READLINK` itself reads a link of mode `0o000`, as it reads everything
by claiming uid 0 (ADR 0015 §8).

### 5. A symlink an earlier build changed

Builds before this ADR accepted both calls, so a bucket can hold a symlink
whose size is not its target's length, with a chunk list of up to 2^20
references. Such a symlink:

- is mounted, and `READLINK` returns its target, which no earlier build
  changed;
- reports the size and the space used that the earlier call left;
- answers `NFS3ERR_INVAL` to `READ`, `WRITE` and a `SETATTR` that sets a
  size, whatever size is asked for, the target's length included, so its
  size cannot be put right through the mount;
- can have its other attributes set, and be renamed and removed. Removing it
  and making it again gives a symlink whose size is its target's length and
  which has no chunk list.

Nothing repairs it and nothing reports it (Assumption 11; *For the owner*,
4). ADR 0017 §5 and ADR 0024 §3 declined to repair at load what an earlier
build left, a read-only mount could not commit a repair (ADR 0011 §4), and
ADR 0007 §6's record, which the owner approved as that section has it,
counts regular files only. By the owner's words there are no clients yet
(*Context*, decision 2), and the defect was found by reading, so no such
symlink is known to exist.

### 6. ADR 0007's record, and its filter

**No call of this build puts an inode that is not a regular file above the
limit.** Three statements write `inode.Size`, read at `939f2b2`:

- `createEntry` gives a symlink the length of its target. Over NFS a target
  is at most one RPC record, 8 MiB, and `MaxFileSize` is at least 2^32
  (ADR 0007 §1).
- `bufferWrite`'s tail block and `truncate` are reached only past `OpWrite`
  and `OpTruncate`, which now refuse every object that is not a regular
  file. `inode.Type` is written once, when the inode is made, and a handle
  names one inode by number and generation, so nothing between a check and
  the change can make the object another type.

And `inode.Chunks` is written by `truncate` and by a flush of an `FS.open`
entry, which only `Write` and `truncate` make, after their checks.

**A reachable way remains: a bucket an earlier build wrote** (§5). So the
filter is live, and the issue's third done-when is due: a test (§9, N7)
mounts a bucket whose snapshot holds a symlink and a directory above the
limit and shows that the record counts neither. It builds the snapshot by
ADR 0024 §7's recipe, setting `inode.Size` by hand, which is how an earlier
build's bucket is stood for without the defect itself as a test route
(Assumption 12). The record, its message and its attributes are ADR 0007
§6's, unchanged.

### 7. State, the policy rule and exemptions

- **The refusal is a line of the policy's rows, and no inline condition is
  added** (Assumption 4). By ADR 0018 §2 the policy rule does not strictly
  reach it: it turns on what the request names and not on who asks, and
  root is refused with everyone. It goes in the rows all the same, because
  that is where ADR 0014 §5 and §6 put the type checks of these operations:
  the directory's `vfs.ErrIsDir` is line 1 of the same rows, so that every
  refusal of a call at stage D, and its order, is one table that is tested
  without a filesystem and that the backend replacing `blobfs` reuses
  (ADR 0014, Assumption 34). A second place for one more type would split
  that table and leave the order of its two halves to `blobfs`.
- **What `blobfs` relies on grows by one refusal.** ADR 0014 §6 has every
  policy `blobfs` installs refuse at least what `Unix()` refuses with
  `vfs.ErrIsDir` and `vfs.ErrNotDir`. Since this ADR that includes the
  `vfs.ErrInval` of line 2: without it `Write` and `truncate` would give a
  symlink a size that is not its target's length, and a chunk list. `New`
  installs `Unix()` alone. ADR 0014 §11's safety rule reads accordingly: a
  test that installs a policy allowing more than `Unix()` asks a `Read`, a
  `Write` or a size only of a regular file, as every such test already does.
- **No machine, no transition, no link.** Nothing here adds state. An
  object's type is a value fixed when it is made (ADR 0018 §2).
- **No exemption.** `docs/adr/exemptions.md` does not change: none is
  granted, restated or revoked. E10, the `MaxFileSize` clamp, stays where it
  is, ahead of the request.

### 8. Security

**Nothing widens.** For every request, the answer is what it was or, for a
call's request on an object that is neither, `vfs.ErrInval` where it was nil
or `vfs.ErrAcces`. No request that was refused is allowed. `ACCESS` grants
what it granted.

**What closes.** A symlink made with no mode has the mode `0o777`, so its
mode gave every caller write permission, and the two calls asked nothing
else. Any caller that could look a symlink up could therefore, at
`939f2b2`:

- set its size to anything up to `MaxFileSize`, making the server hold up
  to 2^20 chunk references for it, which ADR 0007 §2 costs at 24 MiB of
  memory and 11 to 14 MiB of every snapshot, with one request and with no
  write permission on any directory;
- give it file data, and a size its target does not have, whoever owned it.

Neither is possible now, for any caller, root included.

**The mark is no way round.** An advisory request is still allowed where the
mode allows it, and nothing is done on its answer: `AccessBits` is the only
code that sets `Advisory`, and no request a call makes carries it (ADR 0027
§2; its X12 holds every recorded call to that). Review checks both again.

**A type the bucket holds is not trusted.** An inode's type is decoded
unchecked (ADR 0025, Assumption 3). Line 2 refuses every type but
`vfs.TypeReg`, the ones no operation of this server makes included, so no
value of the field reaches a write.

**The uid is the client's claim**, as in every row (ADR 0015 §8). This rule
does not read it.

### 9. The test surface

Every name is spelled as in Go. test-author cannot read the non-test files
of `internal/policy`, `internal/blobfs` or `internal/nfs`, so what a test
may use is here, in ADR 0014 §5 and §11, ADR 0007 §5 to §7, ADR 0024 §7,
ADR 0025 §6, ADR 0027 §10, ADR 0028 §8, `internal/vfs/vfs.go`, or a
`_test.go` file. The tests are N1 to N9. Every test file here uses only
names the code has at `939f2b2`, so every package builds on that commit's
tree and the session can take a red run.

**What becomes pinned.** ADR 0014 §11's U3, whole: a request for `OpRead`,
`OpWrite` or `OpTruncate` that is not advisory, of an object that is neither
a regular file nor a directory, is `vfs.ErrInval`; and the same request
marked is nil or `vfs.ErrAcces` by the mode. U6 stays unpinned, and no test
here sets a time.

**`internal/policy`** (package `policy_test`), by changes to three existing
files and no new one (Assumption 19). They use the fixtures those files
have. Every refusal is compared with `==` against the bare `vfs.Status`.

- **N1. The three rows, exhaustively.** `TestTheFileRowsExhaustively`, A1 of
  ADR 0025 §6, in `owner_access_test.go`, keeps its grid and its branches
  for a directory and for a regular file. Its branch for any other type,
  which asserted a refusal where `oraclePermits` refuses and nothing where
  it allows, asserts `vfs.ErrInval` for every caller, mode and `Op`.
- **N2. An advisory request, exhaustively.**
  `TestAnAdvisoryRequestIsAnsweredByTheModeAlone`, X4 of ADR 0027 §10, in
  `access_test.go`, keeps everything but its branch for any other type,
  whose refusal it now pins: nil exactly when `oraclePermits` gives the
  row's permission, and `vfs.ErrAcces` otherwise. It still asks ADR 0027
  §2's first law of a regular file and a directory alone.
- **N3. The rows by name**, in `policy_test.go`, so that P10 to P13 ask
  them too.
  - `unixCases()`: the four requests it pins as refusals only for U3 are
    pinned as `vfs.ErrInval`: `OpRead` by the owner of a symlink `0o000` and
    by other of a symlink `0o001`, and `OpWrite` and `OpTruncate` by the
    owner of a symlink `0o000`. The advisory `OpRead` by the owner of a
    symlink `0o000`, pinned as a refusal only, is pinned as `vfs.ErrAcces`.
  - `unixCases()` gains, each `vfs.ErrInval`: `OpRead`, `OpWrite` and
    `OpTruncate` each by the owner, by other and by root, of a symlink
    `0o777`; `OpRead` by other of a `vfs.FileType(0)` object `0o777`; and
    `OpWrite` by a primary-group member of a `vfs.TypeFifo` object `0o666`.
    And, advisory: `OpWrite` and `OpTruncate` by other of a symlink `0o777`,
    nil; and `OpWrite` by other of a symlink `0o755`, `vfs.ErrAcces`.
  - `p5Cases()` gains, each `vfs.ErrInval`, where the type and the
    permission both refuse: `OpRead` by other, and by the owner, of a
    symlink `0o000`; `OpWrite` by other of a symlink `0o000`; and
    `OpTruncate` by a primary-group member of a symlink `0o000`.
  - P13's loop over `Advisory` passed over the three `Op`s for a type that
    is neither. It now asserts, for such a request, that the unmarked answer
    is `vfs.ErrInval`, whatever the marked one is.
  - The helpers and constants that pinned a refusal only for U3 and for the
    advisory symlink row go where nothing uses them any more. U6's stays.

  The headers and doc comments of the three files that say U3 is unpinned,
  or pinned only as a refusal, say that it is pinned since this ADR, and
  where.

**`internal/blobfs`** (package `blobfs`), in one new file,
`internal/blobfs/symlink_handle_test.go`, with helpers of its own prefixed
`nr`. The harness is ADR 0011 §7's, as ADR 0014 §11, ADR 0025 §6 and ADR
0027 §10 apply it: a `*store.Local` under `t.TempDir()` behind an unarmed
`uc43Store`, through `uc43Setup`; `uc43Config`; every `New` and every `FS`
call in `uc43Go`, waited for under `uc43Bound`; nothing sleeps, polls or
runs in parallel; `FS.dirty` and `FS.changes` read through `sa24Read`.
`Unix()` is left in place on every mount: no test here writes `FS.perm`. A
test may reuse `uc43Env` and its methods, `uc43WriteForeignRoot` and
`uc43Writer`; the `pw` helpers, `pwMember` and `pwAllAccess`; the `sa24`
helpers and `sa24Other`; `sp24Root`; the `oa` helpers; `bpOpen`; `dg24Named`;
`newBTCapture`, `btAttr`, `mfsWantKind`, `mfsMsgLarger` and `mfsMax`; and
`testCaller`. Of unexported non-test names it uses `mountDiverged`, through
`uc43Env.mode`, and, for N7 and N8, the names ADR 0024 §7 lists: `rootKey`,
`rootPointer`, `snapshot`, `inode`, `decodeSnapshot` and `encodeSnapshot`.

The callers are `testCaller` (`vfs.Caller{UID: 501, GID: 20}`), who owns the
root directory and what it makes; `pwMember` (`vfs.Caller{UID: 503, GID:
20}`), in the group; `sa24Other` (`vfs.Caller{UID: 502, GID: 30}`), neither;
and `sp24Root` (`vfs.Caller{UID: 0}`). The bits are written R, L, M, E, D
and X, as in ADR 0027 §10.

Facts a test may rely on, beside those of the sections named above:

- `Symlink(dir, name, target, sa)` makes an object whose `Attr.Type` is
  `vfs.TypeLnk`, whose mode is `0o777` or the `Mode` given, masked to
  `0o7777`, and which its caller owns. Its `Attr.Size` is the length of
  `target` in bytes and its `Attr.Used` is 0. `ReadLink` of its handle
  returns `target` and nil. It has no `FS.open` entry: `bpOpen` of its
  `Attr.FileID` is nil.
- A refused `Read` returns `(nil, false, err)`; a refused `Write`,
  `(0, 0, err)`, a zero `vfs.Stability` being `vfs.Unstable`; and a refused
  `SetAttr`, the zero `vfs.Attr` and the error.
- A refused call changes nothing (§2): every attribute `GetAttr` reports,
  what `ReadLink` returns, `DirtyBytes()`, the absence of an `FS.open`
  entry, `FS.dirty` and `FS.changes` are as they were, and a `Sync` on a
  namespace that was clean makes no store call.
- A mount made with `uc43Config` has 4096-byte chunks, so its
  `MaxFileSize()` is 2^32, which is `mfsMax`.
- A read-only mount and a diverged one are made as ADR 0025 §6's A9 makes
  them.
- A snapshot whose inodes have sizes set by hand is made by ADR 0024 §7's
  recipe: after a `Sync`, `dg24Named` on the inner `*store.Local` returns
  the root pointer and the decoded snapshot it names; `snap.Inodes` is keyed
  by inode number, which is `Attr.FileID`; the test sets `Size` on the
  inodes it means to, encodes with `encodeSnapshot`, and `Put`s the bytes
  under `rp.Snapshot` on the inner store, leaving the root pointer as it is;
  it never uses the first `*FS` again, and mounts afresh. Such a snapshot
  decodes and mounts: nothing checks an inode's size at load. A regular file
  given a size with no chunk list is a sparse file. A mount that only reads
  commits nothing, so a second doctoring finds the same snapshot key.
- ADR 0007 §6's record is as that section gives it, and a mount takes its
  logger from `Config.Log`.

The tests:

- **N4. Neither read, nor written, nor given a size**
  (`TestASymlinkIsNeitherReadNorWrittenNorGivenASize`; §1, §2; the issue's
  first two done-whens). `testCaller` makes `f`, a regular file with 100
  bytes, and `ln`, a symlink to `"dir/target.txt"`, with no `Mode`, and
  `Sync`s. `GetAttr` of `ln` reports `vfs.TypeLnk`, mode `0o777` and size
  14. Then each of these calls, by each of the four callers, returns
  `vfs.ErrInval` in the form the facts give:
  - `Write(ln, 0, 3 bytes, vfs.Unstable)` and `Write(ln, 100, 3 bytes,
    vfs.Unstable)`;
  - `SetAttr(ln, {Size: 0})`, `{Size: 14}`, `{Size: 15}`, `{Size: 12288}`
    and `{Size: 0, Mode: 0o600}`;
  - `Read(ln, 0, 10)`, `Read(ln, 0, 0)` and `Read(ln, 1000, 10)`.

  After each, everything the facts list is as it was, `ReadLink` returns the
  target, and the harness recorded no store call. Then `testCaller` creates
  a file, so that the namespace is dirty, and its `Write(ln, 0, 3 bytes,
  vfs.FileSync)` returns `(0, 0, vfs.ErrInval)` with no store call recorded
  and `FS.dirty` still true: a refused stable write syncs nothing. Then
  `Sync` returns nil, and on a remount `ln` is a symlink of size 14 whose
  target is unchanged, and `f` reads its 100 bytes. The first wrong answer
  ends the test.
- **N5. The refusal keeps its place among the checks**
  (`TestTheTypeRefusalKeepsItsPlaceAmongTheChecks`; §1, §2). On a writable
  mount `testCaller` makes `ln`, with no `Mode`; `ln700` and `ln000`,
  symlinks made with `Mode` `0o700` and `Mode` 0; and `gone`, a symlink it
  then removes, keeping the handle.
  - *The type before the permission.* `sa24Other`'s and `pwMember`'s
    `Read`, `Write` and `SetAttr` of a size on `ln700`, and `testCaller`'s
    on `ln000`, return `vfs.ErrInval`, not `vfs.ErrAcces`.
  - *The size before the other attributes.* `testCaller`'s
    `SetAttr(ln, {Size: 0, UID: 0})` returns `vfs.ErrInval`, not
    `vfs.ErrPerm`.
  - *The arguments before the type*, a characterisation: `Write(ln,
    MaxFileSize(), 1 byte, vfs.Unstable)` and `SetAttr(ln, {Size:
    MaxFileSize() + 1})` return `vfs.ErrFBig`. No write is made that starts
    just below the limit: a build without line 2 would accept it, and its
    next flush would build 2^20 chunk references (ADR 0007 §7).
  - *The handle before the type*, a characterisation: `Read`, `Write` and
    `SetAttr` of a size on `gone`'s handle return `vfs.ErrStale`.
  - *An empty write*, a characterisation: `Write(ln, 0, no bytes,
    vfs.Unstable)` returns `(0, vfs.FileSync, nil)`.
  - *The mount gate before the type.* After a `Sync`, on a read-only mount
    of the bucket: `Write(ln, …)` and `SetAttr(ln, {Size: 0})` return
    `vfs.ErrROFS`, a characterisation, and `Read(ln, 0, 10)` returns
    `vfs.ErrInval`. On a mount diverged as A9 diverges one: the two changes
    return `vfs.ErrStale`, and the `Read`, `vfs.ErrInval`.
- **N6. `ACCESS` still reports a symlink's mode**
  (`TestAccessStillReportsASymlinksMode`; §3), a characterisation that
  passes before the change and after, pinned so that a change which carried
  line 2 into an advisory request fails a test that names the decision. With
  N5's `ln`, `ln700` and `ln000`, `Access` with every bit asked gives
  exactly §3's table, for the four callers. `sa24Other`'s `Access` of `ln`,
  asked one bit at a time, returns that bit for R, M, E and X, and 0 for L
  and D. On a read-only mount of the bucket every caller is given R and X
  on `ln`.
- **N7. The record counts regular files only**
  (`TestTheMountRecordCountsRegularFilesOnly`; §6; ADR 0007 §6; the issue's
  third done-when), a characterisation that passes before the change and
  after. `testCaller` makes `ln`, a symlink; `d`, a directory; `big.bin`, an
  empty regular file; and `small.bin`, with 10 bytes; and `Sync`s. The
  first doctoring sets `ln`'s size to `mfsMax + 7` and `d`'s to
  `mfsMax + 9`. A mount of the bucket, with a capturing logger, logs no
  record with the message `mfsMsgLarger`, and `GetAttr` reports those two
  sizes. The second doctoring sets `big.bin`'s size to `mfsMax + 1`. A
  mount then logs exactly one such record, at `slog.LevelWarn`, whose
  `count` is 1, whose `largest` is `mfsMax + 1` and whose `max_file_size` is
  `mfsMax`, each of the kind ADR 0007 §6 gives. A record that counted every
  inode would have a `count` of 3 and a `largest` of `mfsMax + 9`. No size
  here builds a chunk list.
- **N8. A symlink an earlier build resized**
  (`TestASymlinkAnEarlierBuildResizedIsRefusedAsAnyOther`; §5). `ln` is
  made to `"dir/target.txt"` and committed, and a doctoring sets its size to
  100. On a mount of the bucket: `GetAttr` reports `vfs.TypeLnk` and size
  100; `ReadLink` returns the target; `Read(ln, 0, 10)`, `Write(ln, 0, 3
  bytes, vfs.Unstable)` and `SetAttr(ln, {Size: 14})` return `vfs.ErrInval`,
  and the size is still 100 with `FS.dirty` false; `Remove` of `ln` returns
  nil; and a `Symlink` of the same name to the same target reports size 14,
  as it does on a remount after a `Sync`.

**`internal/nfs`** (package `nfs`), in one new file,
`internal/nfs/symlink_handle_test.go`, with helpers of its own prefixed
`nr`. It drives the server over a real TCP socket, as `sticky_test.go`
does, and may reuse `startServer`, `dial`, `mountRoot`, `rpcClient`,
`skipPostOpAttr` and `skipWccData`; `nmfsWrite`, `nmfsSetSize`, `nmfsCreate`,
`nmfsGetSize`, `nmfsWantEnd`, `nmfsUnchecked` and `nmfsOK`; `stkClient`;
`cgDial`, `cgMake`, `cgGetAttr` and `cgLnk`; and `accAccess`. Of unexported
non-test names it uses the procedure numbers `procGetAttr`, `procSetAttr`,
`procAccess`, `procReadlink`, `procRead`, `procWrite`, `procCreate` and
`procSymlink`. `startServer`'s root directory is owned by `501:20`, which is
what `dial`'s client sends.

The encodings, beside ADR 0007 §5's and ADR 0028 §8's, as
`internal/nfs/nfs3.go`'s handlers read and write them at `939f2b2`:
`NFS3ERR_INVAL` is 22, and `NF3LNK` is 5. `READLINK`, procedure 5, takes one
`nfs_fh3`; its reply is the status and a `post_op_attr`, then, for `NFS3_OK`
alone, the target as a string, and nothing after. `READ`, procedure 6, takes
an `nfs_fh3`, an offset as a `uint64` and a count as a `uint32`; a reply
that is not `NFS3_OK` is the status and one `post_op_attr`, and nothing
after. `SYMLINK`, procedure 10, is as ADR 0028 §8 gives it and `cgMake`
sends it. `ACCESS` is as ADR 0027 §10 gives it.

- **N9. On the wire** (`TestNFSASymlinksHandleIsRefusedWithNFS3ERRINVAL`;
  §1, §2, §3). The owner, uid 501, makes `ln` in the root with a `SYMLINK`
  to `"dir/target.txt"`. `GETATTR` of its handle reports type 5 and size 14,
  and `READLINK` returns the target. Then, from the owner and from a client
  sending `502:30`:
  - a `WRITE` of 3 bytes at 0, and one at 100, answer 22, with one
    `wcc_data` and nothing after;
  - a `SETATTR` of size 0 answers 22, with one `wcc_data` and nothing after;
  - a `READ` of 10 bytes at 0 answers 22, with one `post_op_attr` and
    nothing after;
  - an `ACCESS` with mask 0x3f answers `NFS3_OK` and 0x2d.

  After each, `GETATTR` still reports type 5 and size 14 and `READLINK`
  still returns the target. Then the owner sends an UNCHECKED `CREATE` of
  `ln` that carries size 0. Whatever it answers, `GETATTR` of the symlink's
  handle still reports type 5 and size 14, and `READLINK` the target. The
  status is not pinned, since the handler drops the refusal (ADR 0007,
  Assumption 9).

**Red run.** Read from the code at `939f2b2` and not run:

- N1 fails for every type that is neither, on every request: with nil where
  `Permits` allows, and with `vfs.ErrAcces` where it refuses.
- N2 passes: the advisory answers do not move, and the refusal it now pins
  is the one the rows already give.
- N3: P4 fails on each of the four converted rows and of the eleven new
  rows that are not advisory, and passes the converted advisory row and the
  three new ones; P5 fails on its four new rows, with `vfs.ErrAcces`; P13
  fails on every request for one of the three `Op`s on a type that is
  neither.
- N4 fails on its first call, `testCaller`'s `Write`, which returns 3 and
  nil.
- N5 fails on the type before the permission, with `vfs.ErrAcces`; on the
  size before the other attributes, with `vfs.ErrPerm`; and on the `Read`s
  of the read-only and the diverged mount, which return zeros. Its
  characterisations pass, and at `939f2b2` none of its calls changes a
  symlink.
- N6 and N7 pass.
- N8 fails on its `Read`, which returns 10 zero bytes.
- N9 fails on its first `WRITE`, which answers `NFS3_OK`.

After the change every test passes.

**Existing tests** (Assumption 19). Beside N1 to N3, no assertion of an
existing test changes. Two comments do: the header of
`internal/blobfs/max_file_size_test.go`, whose *Not covered, on purpose*
says that §6's filter is never exercised, says that N7 exercises it since
this ADR; and `internal/blobfs/policy_wiring_test.go`'s header and W4's doc
comment, which keep the tally of the rows U1 to U6, say that U3 is pinned
since this ADR and tested in `symlink_handle_test.go`, that W4 still has no
such row, and that U6 alone stays unpinned. These were read against §1 to
§3 and stand: every test that asks a `Read`, a `Write` or a size under
`Unix()` asks it of a regular file or a directory; ADR 0027 §10's X1, X2
and X11, whose symlink rows are advisory or are answered by a recorder,
and its X8 and X9, which ask nothing of a symlink; ADR 0014 §11's W2, W3
and W5; and `TestSymlink`.

**What only review can show:**

- that `internal/policy/policy.go` is the only non-test file changed, and in
  it only the three file rows, their helper and doc comments;
- that line 2 comes after line 1 and before `Permits` is called, and reads
  `Object.Type` and `Advisory` alone;
- that no line of `internal/blobfs` changed, so that no check of the type
  stands outside the policy, and no function `docs/tla/trace.tsv` names was
  edited (§10);
- that `AccessBits` is still the only code that sets `Advisory`;
- that the rows still take no lock, log nothing and allocate nothing when
  they allow (ADR 0014 §5);
- that §6's account of what writes `inode.Size` and `inode.Chunks` still
  holds at coder's commit;
- how a kernel client behaves, which needs a mount and a human
  (Assumption 17).

### 10. The model, the trace and the vocabulary

No TLA+ module is affected, and none is added (Assumption 14). ADR 0013 §1
gives permission rules to exhaustive tests of the policy and to security
review, and N1 and N2 are those tests. `docs/tla/CommitProtocol.tla` models
no permission and no type: its `Mutate` is guarded by `Mutable`, which stays
the first statement of `Write` and `SetAttr`, and a request the policy
refuses is no step of it.

**What a change in `blobfs` would have touched.** Three functions of
`internal/blobfs/fs.go` on the path of this defect carry a
`TLA+: docs/tla/CommitProtocol.tla Mutate` line and a row of
`docs/tla/trace.tsv`:

| Row | Symbol | `locks` | `decided` |
|---|---|---|---|
| 13 | `FS.SetAttr` | `FS.mu (write)` | `ADR 0024 section 2` |
| 17 | `FS.bufferWrite` | `openFile.mu, then FS.mu (write)` | `ADR 0003 section 3; ADR 0024 section 1` |
| 18 | `FS.truncate` | `commitGate (read), openFile.mu, FS.mu (write)` | `ADR 0012 section 2; ADR 0024 section 2` |

`Write`, `Read` and `readable` have no row. A fix that edited any of the
three would be ADR 0013 §8's refactor of traced code: the architect would
read the row against coder's commit and move the `Checked against` line of
`docs/tla/CommitProtocol.tla`'s header, `docs/tla/README.md`'s statement of
it and `docs/vocabulary.md` V3's from `de0a151` to that commit, and
CLAUDE.md's merge bar would ask the session for `bash scripts/tla.sh full`,
ending `tla.sh: 45 of 45 passed`.

**This design touches none of them.** The change is confined to
`internal/policy/policy.go`, which `trace.tsv` does not name. So no row
changes, `locks` and `decided` included: the three functions perform
`Mutate` as they did, and a refusal is not the action. The module's
`Checked against` line, the README's statement and vocabulary V3's stay at
`de0a151`. Nothing under `docs/tla/` changes in this PR, and CLAUDE.md's
merge bar asks no `tla.sh full` of it. Both rest on coder's diff showing
`internal/policy/policy.go` and no other non-test file.

**The pass after coder's commit**, which the architect makes on the same
branch, confirms that from the session's `git diff --stat` and by reading
the file, records here what coder chose where §1 leaves room, settles the
points test-author's report raises, and records the owner's answers to *For
the owner*. If the diff does touch `internal/blobfs/fs.go`, which is
outside coder's brief, the pass is the one the paragraph above describes,
for whichever of the three rows the diff reaches, and `tla.sh full` is due.

`docs/vocabulary.md` does not change. V7 lists the three `Op`s already;
`UnixAllows(r)` is still `policy.Unix().Check(r) == nil`, with the new
answers; and `EffectAuthorized`, which is stated through it for a call's
request, reads as it did, and can now hold only of a regular file.

### 11. What each ADR left to #69

| ADR | What it left | What becomes of it |
|---|---|---|
| 0007, *What this does not decide* | "`SETATTR` with a size, and `WRITE`, on a symlink's handle. Both check only that the handle is not a directory." | Decided: both are refused (§1). `vfs.ErrFBig` still comes first (§2). |
| 0007 §6, §7 | The record's filter, untested, with no route pinned to an inode above the limit that is not a regular file | Tested, by N7, on a route that is not the defect (§6). |
| 0014 §11, U3 | The three rows for a type that is neither, unpinned | Pinned whole (§1, §9). |
| 0014 §6, §11 | What `blobfs` relies on in `Unix()`'s type rows, and the tests' safety rule | Each grows by line 2 (§7). |
| 0014 §11, U6 | `OpSetTimes` by a caller who is neither root nor the owner | Not #69's. It reads `Permits`, which does not change, and stays unpinned for #100. One thing bears on #100: a symlink's default mode gives every caller the write permission U6's allowed half turns on. |
| 0024 §2 | "a symlink's handle is still accepted with a size" | It is refused, in `truncate`'s pre-check, by `setAttrRequests`' first request (§2). |
| 0025 §1, Assumption 12 | An object that is neither gets neither departure; U3 pinned as a refusal only, on the assumption that #69 "allows a symlink nothing that is refused today" | The assumption holds: every refusal stays one, and its status is now pinned (§8, N1). |
| 0025 §3, item 3 | The `ACCESS` bits of such an object "wait on #69" | They stay (§3). |
| 0026 §8, §11 | Nothing: it lists #69 among what it leaves, and its tests ask no `Read`, `Write` or size of a symlink | Its sticky rule reads an entry's owner and not its type, so nothing moves. |
| 0027 §2 | The first law, claimed for a regular file and a directory alone | Its scope stays, for good (§3). |
| 0027 §6 | Such an object's bits, pinned "as it is, not as it should be"; two things that bind this ADR | The bits are kept as they should be (§3). This ADR takes the path §6 left open, refusing the call alone, so X1 does not fail. |
| 0027, *What this does not decide* | What `ACCESS` reports for a symlink, "and whether `READLINK` should ask a policy" | Both decided (§3, §4). |
| 0028 §8, §11 | Nothing: it lists #69, and its tests ask no `Read`, `Write` or size of a symlink | Unchanged. Its §6 has a creation store the mode asked for, a symlink's included, which is why §3 speaks of a symlink's mode and not of `0o777`. Its Assumption 13 lists what tests pin of a creation; none of them asks one of the three calls of the object made, so none moves. Neither names #69. |

### 12. Cross-references, `vfs.FS` and `CHANGES`

This ADR replaces parts of five ADRs, under `docs/adr/README.md`'s rule for a
part a later ADR replaces. Each keeps its status and its text, and gains a
`Revised` line, cross-references only, and one sentence in each section
touched.

- **ADR 0007**: the bullet on a symlink's handle under *What this does not
  decide*; and a pointer in §6 and in §7 to §6 here.
- **ADR 0014**: §5's bullets for the `OpRead` row and for the `OpWrite` and
  `OpTruncate` row, as ADR 0025 and ADR 0027 left them; §6's statement of
  what `blobfs` relies on; and §11's U3.
- **ADR 0024**: §2's statement that a symlink's handle is still accepted
  with a size.
- **ADR 0025**: §1's rows and its paragraph on an object that is neither;
  §3's third item; and the form of §6's A1 and of A2's symlink rows.
- **ADR 0027**: §2's rows; what §6 leaves to this ADR; what §10 says is
  unpinned, with X4's and P13's branch for such an object; and one entry of
  *What this does not decide*.

ADR 0018, ADR 0026 and ADR 0028 are not revised (Assumption 16). They list
#69 among what they leave, which stays true of them. The register does not
change (§7). `docs/DESIGN.md`'s table of ADRs gains this ADR's row, and
`docs/adr/README.md`'s index does.

**`vfs.FS`'s doc comments are not changed in this pass.** The brief this ADR
was written from kept `internal/vfs` closed unless the design needed a
change to the interface, and it needs none: `vfs.ErrInval` exists. But the
rule is the contract's as much as `blobfs`'s. A backend that replaced
`blobfs` and reused `internal/policy` would keep it, and one that did not
would find it only here. So these words are proposed, for the pass after
coder's commit to add if the session authorises it (*For the owner*, 5;
Assumption 13), as ADR 0024 and ADR 0028 added theirs. To `SetAttr`'s
comment:

> A SetAttr that sets a size is for a regular file. Given a directory it
> fails with ErrIsDir, and given any other object, a symbolic link included,
> with ErrInval, unless it refuses the call for another reason first.

Above `Read`:

> Read reads a regular file. Given a directory it fails with ErrIsDir, and
> given any other object, a symbolic link included, with ErrInval, unless it
> refuses the call for another reason first. ReadLink returns a symbolic
> link's target.

To `Write`'s comment:

> Write writes a regular file. A Write of at least one byte given a
> directory fails with ErrIsDir, and given any other object, a symbolic link
> included, with ErrInval, unless it refuses the call for another reason
> first, and changes nothing.

They state what the contract is and no permission rule, the line ADR 0026
kept (its Assumption 16).

One `CHANGES` line, proposed for the session to record, without `BREAKING:`
(Assumption 15; *For the owner*, 6):

> WRITE, READ and a SETATTR that sets a size are refused with NFS3ERR_INVAL
> on a symbolic link, and on any other object that is neither a regular file
> nor a directory, as RFC 1813 has all three name a regular file. Before,
> any caller could send a WRITE or a size change to a symbolic link made
> with no mode, which gave the link a size other than its target's length
> and file data of its own, up to the maximum file size, while READLINK went
> on returning the target; and a READ returned that data, or zeros. A
> directory is still refused with NFS3ERR_ISDIR, ACCESS still reports a
> symbolic link's permissions by its mode, and READLINK is unchanged; a link
> an earlier version left with a wrong size keeps it until it is removed and
> made again.

## For the owner

Six judgement calls, each decided above as the architect's recommendation.
They are the ones with a trade-off a client or an operator can see, for the
session to put to the owner. A different answer to the first, the second or
the third would change a row, a table and its tests. The mechanism, a line
of the three rows read by the mark, stands under every alternative.

1. **A `READ` of a symlink is refused too** (§1), though the issue's
   done-when names only a `WRITE` and a size. *Recommended:* refuse it. RFC
   1813 has `READ` name a regular file in the words it uses for `WRITE`;
   Linux's server refuses it; U3 names all three rows, and the earlier ADRs
   handed all three to this one; and what a `READ` returns today is not the
   target but zeros, or whatever an earlier `WRITE` stored. *Alternatives:*
   leave `READ` as it is and pin its row as allowed by the mode, which keeps
   answering a call the protocol does not define with bytes that mean
   nothing; or leave it to an issue of its own, which leaves a third of U3
   unpinned.
2. **`ACCESS` keeps reporting the mode's verdict for an object that is
   neither** (§3), which ends the interim of 2026-11-23 by making it
   permanent. *Recommended:* keep the bits, as Linux's server does.
   *Alternatives:*
   - grant READ and EXECUTE by the mode and never MODIFY or EXTEND, so that
     `ACCESS` never grants a write the server always refuses. As read, a
     macOS client would then refuse to set a symlink's times, mode or
     owner, which restoring a link from an archive does;
   - grant such an object nothing. As read, that also stops `readlink(2)` on
     a macOS client.
3. **`READLINK` asks no policy** (§4). *Recommended:* none, as Linux's
   server and POSIX have it. *Alternative:* give `vfs.FS.ReadLink` a caller
   and the policy a row for it, so that a symlink's mode binds a caller that
   does not ask `ACCESS` first. That is a change to the `vfs.FS` contract,
   and it binds nobody who can claim uid 0.
4. **A symlink an earlier build changed is neither repaired nor reported**
   (§5). *Recommended:* leave it, and file an issue for detecting such
   links. *Alternatives:* set its size back to its target's length and drop
   its chunk list at load, which decides that a mount repairs what a
   snapshot holds, against ADR 0017 §5, and which a read-only mount cannot
   commit; or count such links in a record at mount, beside ADR 0007 §6's.
5. **`vfs.FS`'s doc comments say the rule** (§12). *Recommended:* add the
   three paragraphs §12 proposes, in the pass after coder's commit.
   *Alternative:* leave the contract's words as they are, with the rule in
   this ADR and in `docs/DESIGN.md`'s row for it.
6. **The `CHANGES` line takes no `BREAKING:`** (§12; Assumption 15).
   *Recommended:* none, since no running deployment has to act to keep
   working. *Alternative:* prefix it, on the reading that a deployment
   holding a link an earlier version resized must remove it and make it
   again to have its size right.

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation, and Issue #69**, under the owner's
   2026-10-10 words, as ADRs 0015 to 0028 were. The issue's done-when asks
   that the two calls fail before anything is changed, with an error the
   architect chooses. The rest is mine: that the refusal is a line of the
   policy's rows (Assumption 4), that a `READ` is refused too
   (Assumption 6), what `ACCESS` reports (Assumption 7), and the calls *For
   the owner* lists.
2. **Read, not run, at `939f2b2`.** Every statement about the code was
   checked by reading it there; nothing was run. #69's text and its comment
   are as the session gave them in the brief; neither was read from the
   tracker. What *Context* says the issue gets wrong is a finding against
   one sentence of its account, not against its ask.
3. **`vfs.ErrInval`.** The issue suggests it. The alternatives were
   `vfs.ErrAcces`, which would tell a caller that another credential might
   do, and which root would get too; `vfs.ErrIsDir`, which is false of a
   symlink; and `vfs.ErrBadType` and `vfs.ErrNotSupp`, which none of the
   three procedures lists. That the status's value is 22 is
   `internal/vfs/vfs.go`'s, and RFC 1813's by recall.
4. **A line of the rows, not a check in `blobfs`** (§7). ADR 0018 §2 would
   let it be either without an exemption. The rows are chosen for the reason
   ADR 0014's Assumption 34 gives for the directory's check, which this one
   sits beside. The cost is that `blobfs`'s invariant, that only a regular
   file has a chunk list and a size its calls set, rests on the installed
   policy keeping the line, as its directory invariant already does.
5. **The type before the permission** (§1), as every type check of
   `Unix()`. A caller with no permission on a symlink is answered
   `vfs.ErrInval`, where it was answered `vfs.ErrAcces`.
6. **`Read` is in scope** (§1; *For the owner*, 1). The brief asked that it
   be considered. The three rows share one helper and one unpinned row, U3,
   and a rule that refused a symlink a write and answered its read with
   zeros would be half of one.
7. **An advisory request is not refused for its type** (§1, §3; *For the
   owner*, 2). ADR 0027 §6 left this path open in terms. It is taken because
   two of the bits are in use by a client, as read, and because the
   alternative needs a rule for what each bit means on a symlink, for which
   the protocol gives no text. The cost is that `ACCESS` grants MODIFY and
   EXTEND on an object no `WRITE` can change, and READ on one no `READ`
   returns, which §3 states so that it is not later taken for a defect.
8. **`READLINK` asks no policy** (§4; *For the owner*, 3).
9. **An empty write still succeeds on any handle** (§2). ADR 0007 §4 and
   ADR 0014 §6 have it return after stage A, before the handle is resolved,
   and `vfs.FS`'s comment allows it. It changes nothing, so the done-when's
   "before anything is changed" is kept. N5 pins it as it is.
10. **`vfs.ErrFBig` still comes before the type** (§2). ADR 0007's
    Assumption 7 put that check first because it reads the arguments alone.
    A symlink given an offset or a size past the limit is told the limit,
    not the type. Moving the type first would need the handle resolved
    before stage B, for every call.
11. **No repair and no report of what an earlier build left** (§5; *For the
    owner*, 4).
12. **N7's route** (§6, §9). ADR 0007's Assumption 13 declined a snapshot
    written by hand, to keep the snapshot's JSON out of the test surface.
    ADR 0024 §7 has since pinned `encodeSnapshot`, `decodeSnapshot` and the
    fields a doctoring sets, so the route costs one more field, `Size`,
    which that section's `inode` already lists. The other route, a policy
    that allows the defect back in, would break ADR 0014 §11's safety rule
    as §7 reads it.
13. **`internal/vfs` is not edited in this pass** (§12; *For the owner*, 5).
    The brief forbade it unless the interface had to change. The words are
    proposed rather than made, as ADR 0024's Assumption 17 has it for its
    own. They name no status's order beyond "unless it refuses the call for
    another reason first", the phrase the comment above `Remove` already
    uses, so that §2's order stays `blobfs`'s.
14. **No TLA+, and no trace pass** (§10), provided coder edits
    `internal/policy/policy.go` alone.
15. **Not breaking, and one `CHANGES` line** (§12; *For the owner*, 6). No
    flag, bucket or mount has to change. Every answer that changes is a
    refusal of a call the protocol does not define, which no kernel client
    was found to send.
16. **Which ADRs are revised** (§12): the five of which a part is replaced
    or a statement about the code stops being true. An ADR that only lists
    #69 among what it leaves is not revised, as ADR 0028's Assumption 18 has
    it.
17. **What clients do is read, not observed** (*Context*; §3). Apple's
    client is read from the first 100,000 characters of `kext/nfs_vnops.c`
    at `main`, and XNU from one stretch of `bsd/vfs/vfs_subr.c`. That a
    program which restores a symlink sets its times, and that XNU reaches
    `vnode_authattr` for a symlink, are from recall and were not read. That
    Linux's client asks nothing of a symlink is ADR 0027's reading. Nothing
    was run against either client. If the macOS reading is wrong, §3's first
    reason is weaker, and its second and third stand.
18. **The cost is not measured.** Line 2 is one comparison of a field and
    one of a bool, with no allocation.
19. **The policy tests are changed in place** (§9). A1 and X4 already walk
    the whole grid, each for one value of the mark, and each has a branch
    for a type that is neither which this ADR's rule now fills. A new
    exhaustive test beside them would pin the same rows twice. The comments
    that change are those of the files whose assertions change, and the two
    running tallies the earlier ADRs kept. Other test files say that they do
    not rely on U3, which stays true of them, and are left.
20. **What "neither" covers** (§1): every `Type` but `vfs.TypeReg` and
    `vfs.TypeDir`, the values no operation makes included, as ADR 0014 §5
    reads a non-directory and ADR 0025's A1 reads its grid.
21. **The names** (§9): the test files, the prefix `nr`, N1 to N9, the
    target `"dir/target.txt"` and the sizes, which no requirement gives.

## Alternatives considered

- **A check of the type in `blobfs`**, in `Write`, `truncate` and
  `readable`. It needs no exemption (ADR 0018 §2), and leaves the rows, and
  so `ACCESS`, untouched. It puts one type's refusal in the policy and
  another's beside it, leaves their order to `blobfs`, cannot be tested
  without a filesystem, and edits `truncate`, a traced function. The backend
  replacing `blobfs` would have to repeat it (Assumption 4).
- **Both**, the row and a second check where the change lands. The second
  could only hide a hole in the first, as ADR 0007's Assumption 10 has it
  for a check inside the fills.
- **Refusing an advisory request too.** `ACCESS` would then grant a symlink
  nothing, which as read stops `readlink(2)` on a macOS client (§3).
- **Refusing an advisory `OpWrite` and `OpTruncate`, and not `OpRead`.** As
  read, a macOS client would refuse to set a symlink's times or mode
  (*For the owner*, 2).
- **A third `ACCESS` table**, for an object that is neither, with requests
  of its own. It needs an `Op` for reading a link, which no call asks
  (§4), and a reading of what MODIFY means on a symlink.
- **`vfs.ErrAcces`, `vfs.ErrIsDir`, `vfs.ErrBadType` or `vfs.ErrNotSupp`**
  (Assumption 3).
- **Leaving `Read` as it is** (*For the owner*, 1).
- **Refusing an empty write of a symlink.** It would move the type check
  ahead of the one return `Write` makes without resolving its handle
  (Assumption 9).
- **A policy for `READLINK`** (§4).
- **Repairing at load, or reporting at mount** (§5).
- **Testing the filter through a policy that allows the defect.** It breaks
  the safety rule (Assumption 12).
- **A TLA+ model.** The rule is a function of one request, which N1 and N2
  enumerate.

## Consequences

- A symlink's size is its target's length for as long as the symlink lives,
  and no caller can make the server hold file data, or chunk references, for
  anything but a regular file.
- `READ`, `WRITE` and a `SETATTR` that sets a size answer `NFS3ERR_INVAL` on
  an object that is neither a regular file nor a directory, for every
  caller.
- For such an object `ACCESS` and the three calls differ by design: the bits
  speak for what a client does by other means. It is stated, pinned, and no
  longer #69's.
- U3 is pinned, and with it every answer of the three rows for every type
  and both values of the mark. Of ADR 0014 §11's six, only U6 is left.
- `blobfs` relies on one more refusal of the installed policy.
- ADR 0007 §6's filter is tested.
- One line in one file changes, and nothing under `docs/tla/`.
- Hard to change later: the status, once a client's handling rests on it;
  and what `ACCESS` grants on a symlink, once clients' behaviour rests on
  that.

## What this does not decide

- #99, #100, #101, #154 and #155, and the row U6. `OpSetTimes`, the guard,
  where `Write` checks and the set-ID bits are untouched.
- What `COMMIT` answers for a handle that is not a regular file. `Commit`
  takes no caller and asks no policy, and commits whatever its handle
  names. RFC 1813 §3.3.21 has its handle name a regular file, and its
  ERRORS list no status for one that does not.
- What a `Create` that is not exclusive answers for a name that exists and
  is not a regular file. It returns a symlink's handle, as before; only the
  size that follows is refused (§2). The question beside it, whether such a
  `Create` should be judged as the open it stands for, is ADR 0025's and
  ADR 0027's, and stays open.
- Whether a symlink's mode should be settable. `OpChmod` allows its owner to
  set it, and a creation stores the mode asked for, where Linux's server
  ignores a mode set on a symlink. Since §3 it is the mode a macOS client is
  told for `readlink(2)`.
- How long a symlink's target may be. `Symlink` refuses an empty target and
  no other, so over NFS a target can be as long as an RPC record, though
  RFC 1813 §3.3.10 lists NFS3ERR_NAMETOOLONG among `SYMLINK`'s errors.
- A repair or a report of what an earlier build left (§5).
- What `vfs.FS`'s doc comments say, until the session authorises §12's
  words.
- Root squash and the AUTH_SYS trust boundary (#6, #90, #95).
- A test against a kernel client (Assumption 17).

## Sources

Fetched on 2026-11-28, while this ADR was written, through a tool that
summarises what it fetches and quotes at most about 125 characters at once,
so a longer passage came as consecutive pieces, which are joined here with a
space; it reads a long page 100,000 characters at a time. Two fetches gave
nothing usable: GitHub's rendering of Apple's `nfs_vnops.c` returned no
source text, and one of XNU's `vfs_subr.c` began past the end of the file.
No fetched page held anything addressed to its reader as an instruction.
The tool's own summary of the first of those two named the file's raw URL;
the file was then fetched from `raw.githubusercontent.com`, which is the
primary source for it.

- **RFC 1813**, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>, 229,793 characters.
  Characters 0 to 100,000 were fetched three times and 100,000 to 200,000
  twice; the rest was not read.
  - §2.6: the texts of `NFS3ERR_INVAL` and `NFS3ERR_ISDIR`, as *Context*
    quotes them; and of `NFS3ERR_BADTYPE`, "An attempt was made to create an
    object of a type not supported by the server.", and `NFS3ERR_NOTSUPP`,
    "Operation is not supported." (*Context*; Assumption 3)
  - §3.3.2, SETATTR: its ERRORS, "NFS3ERR_PERM, NFS3ERR_IO, NFS3ERR_ACCES,
    NFS3ERR_INVAL, NFS3ERR_NOSPC, NFS3ERR_ROFS, NFS3ERR_DQUOT,
    NFS3ERR_NOT_SYNC, NFS3ERR_STALE, NFS3ERR_BADHANDLE,
    NFS3ERR_SERVERFAULT"; "The new_attributes.size field is used to request
    changes to the size of a file."; and three adjacent sentences of its
    IMPLEMENTATION: "In a heterogeneous environment, it is quite possible
    that the server will not be able to support the full range of SETATTR
    requests." "The error, NFS3ERR_INVAL, may be returned if the server can
    not store a uid or gid in its own representation of uids or gids,
    respectively." "If the server can only support 32 bit offsets and sizes,
    a SETATTR request to set the size of a file to larger than can be
    represented in 32 bits will be rejected with this same error." The tool
    said the section does not say which types a size may be set on.
    (*Context*)
  - §3.3.5, READLINK: of its argument, as *Context* quotes it; its ERRORS,
    "NFS3ERR_IO, NFS3ERR_INVAL, NFS3ERR_ACCES, NFS3ERR_STALE,
    NFS3ERR_BADHANDLE, NFS3ERR_NOTSUPP, NFS3ERR_SERVERFAULT". Its XDR was
    not fetched; §9 gives the reply as `Server.readlink` writes it.
    (*Context*; §9)
  - §3.3.6, READ, and §3.3.7, WRITE: of each argument `file`, and each
    ERRORS list, as *Context* quotes them. (*Context*; §1)
  - §3.3.10, SYMLINK: its ERRORS, "NFS3ERR_IO, NFS3ERR_ACCES,
    NFS3ERR_EXIST, NFS3ERR_NOTDIR, NFS3ERR_NOSPC, NFS3ERR_ROFS,
    NFS3ERR_NAMETOOLONG, NFS3ERR_DQUOT, NFS3ERR_STALE, NFS3ERR_BADHANDLE,
    NFS3ERR_NOTSUPP, NFS3ERR_SERVERFAULT"; the tool found no sentence there
    on the length of the link's data. (*What this does not decide*)
  - §3.3.21, COMMIT: of its argument `file`, "This must identify a file
    system object of type, NF3REG."; its ERRORS, NFS3ERR_IO, NFS3ERR_STALE,
    NFS3ERR_BADHANDLE, NFS3ERR_SERVERFAULT. (*What this does not decide*)
- **Linux `fs/nfsd/nfsfh.c`**, at the tag `v6.6`:
  <https://github.com/torvalds/linux/blob/v6.6/fs/nfsd/nfsfh.c>, fetched
  twice, 789 lines by the page's own count. `nfsd_mode_check`'s
  `umode_t mode = d_inode(dentry)->i_mode & S_IFMT;`,
  `if (requested == 0) /* the caller doesn't care */`,
  `if (rqstp->rq_vers == 4 && mode == S_IFLNK)`, `return nfserr_symlink;`,
  and its last five lines, `if (requested == S_IFDIR)`,
  `return nfserr_notdir;`, `if (mode == S_IFDIR)`, `return nfserr_isdir;`,
  `return nfserr_inval;`. In `fh_verify`,
  `error = nfsd_mode_check(rqstp, dentry, type);` comes before
  `error = nfsd_permission(rqstp, exp, dentry, access);`. (*Context*; §1)
- **Linux `fs/nfsd/vfs.c`**, at `v6.6`. From GitHub's rendering,
  <https://github.com/torvalds/linux/blob/v6.6/fs/nfsd/vfs.c>, the first
  1000 of 2485 lines: in `nfsd_setattr`, `if (iap->ia_valid & ATTR_SIZE) {`,
  `accmode |= NFSD_MAY_WRITE|NFSD_MAY_OWNER_OVERRIDE;`, `ftype = S_IFREG;`
  and `err = fh_verify(rqstp, fhp, ftype, accmode);`; in
  `nfsd_sanitize_attrs`, `/* Ignore mode updates on symlinks */` over
  `if (S_ISLNK(inode->i_mode))` and `iap->ia_valid &= ~ATTR_MODE;`; the
  array `nfs3_anyaccess`, with `{ NFS3_ACCESS_READ, NFSD_MAY_READ },`,
  `{ NFS3_ACCESS_EXECUTE, NFSD_MAY_EXEC },`,
  `{ NFS3_ACCESS_MODIFY, NFSD_MAY_WRITE|NFSD_MAY_LOCAL_ACCESS },` and
  `{ NFS3_ACCESS_EXTEND, NFSD_MAY_WRITE|NFSD_MAY_LOCAL_ACCESS },`; and
  `nfsd_access`'s choice of `nfs3_regaccess` for `d_is_reg(dentry)`,
  `nfs3_diraccess` for `d_is_dir(dentry)` and `nfs3_anyaccess` otherwise.
  From <https://sourcegraph.com/github.com/torvalds/linux@v6.6/-/raw/fs/nfsd/vfs.c>,
  characters 30,000 on: `nfsd_read`'s
  `err = nfsd_file_acquire_gc(rqstp, fhp, NFSD_MAY_READ, &nf);`,
  `nfsd_write`'s
  `err = nfsd_file_acquire_gc(rqstp, fhp, NFSD_MAY_WRITE, &nf);`, and
  `nfsd_readlink`'s `err = fh_verify(rqstp, fhp, S_IFLNK, NFSD_MAY_NOP);`.
  (*Context*; §3, §4; *What this does not decide*)
- **Linux `fs/nfsd/filecache.c`**, at `v6.6`:
  <https://github.com/torvalds/linux/blob/v6.6/fs/nfsd/filecache.c>, the
  first 1000 of 1238 lines. In `nfsd_file_do_acquire`,
  `status = fh_verify(rqstp, fhp, S_IFREG,` and
  `may_flags|NFSD_MAY_OWNER_OVERRIDE);`. That `nfsd_file_acquire_gc` calls
  `nfsd_file_do_acquire` is from recall: its definition lies past the lines
  seen. (*Context*)
- **Apple `NFS`, `kext/nfs_vnops.c`**, at `main`:
  <https://raw.githubusercontent.com/apple-oss-distributions/NFS/main/kext/nfs_vnops.c>,
  the first 100,000 of 286,611 characters, without indentation. In
  `nfs_vnop_access`, after its directory and file branches: `/* common */`,
  `if (ap->a_action & KAUTH_VNODE_DELETE) {`, `access |= NFS_ACCESS_DELETE;`,
  then `if (ap->a_action &`, `(KAUTH_VNODE_WRITE_ATTRIBUTES |`,
  `KAUTH_VNODE_WRITE_EXTATTRIBUTES |`, `KAUTH_VNODE_WRITE_SECURITY)) {`,
  `access |= NFS_ACCESS_MODIFY;`, then `/* XXX this is pretty dubious */`,
  `if (ap->a_action & KAUTH_VNODE_CHANGE_OWNER) {`,
  `access |= NFS_ACCESS_MODIFY;`. In its file branch,
  `if (ap->a_action & KAUTH_VNODE_READ_DATA) {` gives
  `access |= NFS_ACCESS_READ;`, and
  `if (ap->a_action & KAUTH_VNODE_WRITE_DATA) {` gives
  `access |= NFS_ACCESS_MODIFY | NFS_ACCESS_EXTEND;`. ADR 0027's *Sources*
  quote the same function from GitHub's rendering, to the line that answers
  `EACCES`. (*Context*; §3; Assumption 17)
- **Apple `xnu`, `bsd/vfs/vfs_subr.c`**, at `main`:
  <https://sourcegraph.com/github.com/apple-oss-distributions/xnu@main/-/raw/bsd/vfs/vfs_subr.c>,
  characters 200,000 to 300,000 of 345,956. In `vnode_authorize_opaque`, a
  `switch (vp->v_type)` whose cases `VBLK`, `VCHR`, `VSOCK` and `VFIFO`
  `return 0;`, and `if ((error = VNOP_ACCESS(vp, action, ctx)) != ENOTSUP) {`.
  In `vnode_authattr`, as the tool paired each line with the condition
  before it: under
  `if (has_priv_suser || vauth_node_owner(&ova, cred)) {`, for the
  timestamps,
  `required_action |= KAUTH_VNODE_CHECKIMMUTABLE | KAUTH_VNODE_WRITE_ATTRIBUTES;`;
  under `if (vap->va_vaflags & VA_UTIMES_NULL) {`,
  `required_action |= KAUTH_VNODE_WRITE_ATTRIBUTES;`; and, for the mode of a
  caller that is not the superuser,
  `required_action |= KAUTH_VNODE_WRITE_SECURITY;`. **Honesty note:** the
  pairing of lines with conditions is the tool's, and the function was not
  read whole. Characters 300,000 on were fetched too and hold neither
  function. (*Context*; §3; Assumption 17)

Nothing else was fetched. Everything else here is read from this
repository's own code and documents, cited by symbol and section.
