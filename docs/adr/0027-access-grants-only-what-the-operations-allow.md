# 0027. ACCESS grants a bit only where the operations it names are allowed, and asks the policy that decides them

**Status:** Accepted
**Date:** 2026-11-23
**Revised:** 2026-11-23 — the same day, before merge, after test-author's
commit `e2d1423` and coder's commit `729360f`, read and not run. One rule
moved. §2's first law, that an advisory request only narrows, is claimed for
a regular file and a directory, where §8's S2 rests on it, and no longer for
every request: claimed for every request it pinned, through X6's advisory
row for a symlink, the same request unmarked, which is ADR 0014 §11's U3 and
which §6 and §10 leave unpinned for #69. §2, §6, §10 in what it says is
pinned and in X4 and X7, Assumptions 12 and 24, the second new, and a new
entry under *Alternatives considered* say so, and the committed tests
already ask the law that way. The rest is record. By the session's report
the owner decided on 2026-11-23 the first four of the five calls this ADR
put to the owner, each as recommended, and the session decided the fifth,
that the `CHANGES` line takes no `BREAKING:`; *Context*, the section that
was *For the owner* and is now *Put to the owner, and decided*, and
Assumptions 1, 3, 11, 12, 19 and 22 say so. §2 records what coder chose
where the pins left room (Assumption 27, which is new), and §10's list of
what only review can show gains one item from it. §10 settles the four
other points test-author's report raised, in X1, X2, X8 and X9, each as the
committed test has it (Assumption 26, which is new). §11 records that no
function `docs/tla/trace.tsv` names changed, so that no row and no
`Checked against` line moves, nothing under `docs/tla/` changes and no
`tla.sh full` is due (Assumption 16, and Assumption 25, which is new and
says how far the pass went), and `docs/vocabulary.md` V7 marks `OpExecute`
as existing. §14's `CHANGES` line is reworded in its last sentence, and was
not yet recorded. §1's tables, the `policy` API, the rows, `Access`'s form,
the stages and what a client sees are unchanged, and no test changes.
**Revised:** 2026-11-24 — record only. By the session's report the owner
confirmed on 2026-11-24 the fifth of the five calls this ADR put to the
owner, that the `CHANGES` line takes no `BREAKING:`. The session had
decided it on 2026-11-23, and this ADR recorded it as the session's, which
the owner might still read otherwise. So each of the five calls is now the
owner's. *Context*, which gains decision 5, *Put to the owner, and
decided*, 5, and Assumption 19 say so. What §14, the opening of *Put to the
owner, and decided* and Assumptions 1 and 22 say of the session's decision
stands as the record of 2026-11-23. No rule, no table, no row, no test and
no word of the `CHANGES` line changes.
**Revised:** 2026-11-28 — cross-reference only, for ADR 0029, which decides
#69 and replaces four parts of this ADR: §2's rows for `OpRead`, `OpWrite`
and `OpTruncate`, which gain a line that refuses, with `vfs.ErrInval`, a
request that is not advisory whose `Object` is neither a regular file nor a
directory, an advisory request being answered as §2 has it; what §6 left to
#69's ADR, which takes the path §6 left open, refusing the call alone, keeps
such an object's `ACCESS` bits for good, and has `READLINK` go on asking no
policy; what §10 says is unpinned, U3 for a call, which is pinned since,
with the branch of X4 and of P13 for such an object; and the entry on a
symlink under *What this does not decide*. §2's first law keeps the scope
this ADR gave it, and for an object that is neither it is false by design
since ADR 0029 §3. §2, §6, §10 and *What this does not decide* each gain a
sentence saying so. §1's tables, `AccessBits`, the `OpExecute` row, every
answer to an advisory request, §8's S1 to S5 and every mask a test pins are
unchanged, X1 included, and the text of the replaced parts stays as the
record.
**Issue:** #97

## Context

**The owner's decisions.** Three, as ADR 0014 and ADR 0018 record them: the
first two in the owner's words, and the third in the wording the owner
approved; and a fourth and a fifth the session reported. The brief this ADR
was written from gave the second and the third in substance, and nothing
here claims any approval beyond the five.

1. On 2026-10-10: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0026 were, and its design is the architect's (Assumption 1). *Put to the
   owner, and decided* lists the judgement calls an owner is most likely to
   weigh differently, each with its alternatives and with who decided it.
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." By the brief, a change a client can see still
   gets a `CHANGES` line, prefixed `BREAKING:` only where a running
   deployment must act. Whether this one is breaking was Assumption 19's
   judgement, until the session decided it (decision 4).
3. On 2026-10-22, the rules CLAUDE.md carries under "State machines and
   policies", which ADR 0018 records: a decision about whether a caller may
   do something is a pure policy in `internal/policy`, "an access mask" and
   "a gate on an operation" included, and only an ADR exempts code from that.
4. On 2026-11-23, by the session's report, which gave each answer as the
   option this ADR recommended and not in the owner's words: the owner
   answered the first four of the five calls this ADR put to the owner, each
   as recommended. A diverged mount grants no MODIFY, EXTEND or DELETE (§4).
   In a sticky directory DELETE and MODIFY are granted by the directory's
   mode (§5). A regular file's `ACCESS` answer stays the mode's verdict, and
   reports neither the owner override nor READ for execute (§3). And an
   object that is neither a regular file nor a directory keeps the bits it
   has until #69 (§6). The fifth call, that the `CHANGES` line takes no
   `BREAKING:`, the session decided, since no running deployment must act
   (§14; Assumption 19); the report gives that one as the session's own and
   not as the owner's. All five were the architect's judgement until then.
   The report says nothing of any other assumption here, and each of those
   stays the architect's.
5. On 2026-11-24, by the session's report, which again gave the answer and
   not the owner's words: the owner confirmed the fifth call as the session
   had decided it. The `CHANGES` line takes no `BREAKING:` (§14;
   Assumption 19). So each of the five calls is now the owner's.

**The defect.** #97, "ACCESS grants what the operations then refuse", as the
session gave its text (Assumption 2). `Access` "disagrees with the operations
in three ways":

- "It grants LOOKUP on read alone, while Lookup needs search permission";
- "It grants MODIFY, EXTEND and DELETE on a directory for write alone, while
  create, remove and rename need write and search";
- "It masks those bits only when the mount is read-only, not when it has
  diverged."

"Clients cache ACCESS for access(2). RFC 1813 §3.3.4 calls it advisory." It
was found "while reconciling the TLA+ and FSM/policy plans, by reading the
code at `8377902`. Not reproduced by a test. Severity: low." Its done-when:
"ACCESS derives from the same rules as the operations, including on a
diverged mount." A comment on the issue repeats ADR 0026 §5: `ACCESS` names
no entry, so in a sticky directory the property "holds for DELETE only as
'some entry may be deleted', or it must make an exception for sticky
directories".

**What the code has.** Every statement about the code here was checked by
reading it at `4ded55e`, the commit this ADR's branch was cut from, which is
`origin/main` with ADR 0026 merged. Nothing was run (Assumption 2). Code is
cited by symbol; the issue's line numbers are from `8377902` and name code
that has since moved.

- `(*FS).Access`, in `internal/blobfs/fs.go`, holds `FS.mu` for reading,
  resolves the handle, takes `policy.AccessBits(c, objectOf(n))`, removes
  `vfs.AccessModify`, `vfs.AccessExtend` and `vfs.AccessDelete` if
  `f.mode.State() == mountReadOnly`, and returns what is left `& want`. It
  asks `f.perm` nothing, and it reads the mode itself, not through
  `mutable()`.
- `policy.AccessBits`, in `internal/policy/policy.go`, builds the mask from
  `Permits` alone: `vfs.AccessRead` with read; `vfs.AccessLookup` for a
  directory with read or execute; `vfs.AccessModify` and `vfs.AccessExtend`
  with write, and `vfs.AccessDelete` with them for a directory;
  `vfs.AccessExecute` for a non-directory with execute.
- The operations ask `f.perm`, at stage D, in the hold each has (ADR 0014
  §6): `Lookup` asks `OpLookup`; `ReadDir`, `OpReadDir`; `createEntry`, for
  `Create`, `Mkdir` and `Symlink`, `OpAddEntry`; `unlink`, for `Remove` and
  `Rmdir`, `OpRemoveEntry`; `Rename`, `OpRename`; `Read`, `OpRead`; `Write`,
  `OpWrite`; and a `SetAttr` that sets a size, `OpTruncate`. `Unix()`'s rows
  for the five directory operations ask search, read, or write and search of
  the directory, and its three file rows are ADR 0025 §1's.
- `mutable()` answers nil for `mountWritable`, `vfs.ErrROFS` for
  `mountReadOnly`, `vfs.ErrStale` for `mountDiverged` and
  `vfs.ErrServerFault` for any other mode. `SetAttr`, `Write`, `createEntry`,
  `unlink` and `Rename` call it before anything else; `Read`, `Lookup`,
  `ReadDir` and `Access` do not call it.
- Every `Fire` on the mode is made in `resolveRoot`, holding `FS.mu` for
  writing, or from `initEmpty` before the FS is shared (`FS.mode`'s doc
  comment; ADR 0023 §5).
- `internal/nfs/nfs3.go`'s `access` handler decodes the handle and the mask
  asked for, calls `Access`, and answers the status, a `post_op_attr` and,
  for `NFS3_OK`, the mask returned. It takes the `post_op_attr` from a
  `GetAttr` made after `Access` has returned.

**Where `ACCESS` and the operations differ, at `4ded55e`.** Under `Unix()`,
for a directory and for a regular file:

| Object | Bit | `ACCESS` grants it with | The operation it names needs | |
|---|---|---|---|---|
| directory | READ | read | `OpReadDir`: read | the same |
| directory | LOOKUP | read or search | `OpLookup`: search | granted and refused, with read alone |
| directory | MODIFY, EXTEND, DELETE | write | `OpRename`, `OpAddEntry`, `OpRemoveEntry`: write and search | granted and refused, with write alone |
| regular file | READ | read | `OpRead`: read, or the owner, or execute | never granted and refused |
| regular file | MODIFY, EXTEND | write | `OpWrite`, `OpTruncate`: write, or the owner | never granted and refused |
| regular file | EXECUTE | execute | `OpRead`, for the page-in | never granted and refused |
| any | MODIFY, EXTEND, DELETE | a mount that is not read-only | a mount that is writable | granted and refused, on a diverged mount |

So the three the issue lists are all there is, for a regular file and a
directory. On a regular file the two differ the other way, by design, in the
two ways ADR 0025 §3 states. An object that is neither is §6's.

**What RFC 1813 says** (*Sources*).

- §3.3.4, of the bits: `ACCESS3_READ` is "Read data from file or read a
  directory."; `ACCESS3_LOOKUP`, "Look up a name in a directory (no meaning
  for non-directory objects)."; `ACCESS3_MODIFY`, "Rewrite existing file data
  or modify existing directory entries."; `ACCESS3_EXTEND`, "Write new data
  or add directory entries."; `ACCESS3_DELETE`, "Delete an existing directory
  entry."; `ACCESS3_EXECUTE`, "Execute file (no meaning for a directory)."
- §3.3.4, of the answer: "A bit mask of access permissions indicating access
  rights for the authentication credentials provided with the request."
  And: "The results of this procedure are necessarily advisory in nature.
  That is, a return status of NFS3_OK and the appropriate bit set in the bit
  mask does not imply that such access will be allowed to the file system
  object in the future, as access rights can be revoked by the server at any
  time."
- §3.3.4, IMPLEMENTATION: "The ACCESS operation is provided to allow clients
  to check before doing a series of operations." "This is useful in operating
  systems (such as UNIX) where permission checking is done only when a file
  or directory is opened." "The intent is to make the behavior of opening a
  remote file more consistent with the behavior of opening a local file."
  And: "The information returned by the server in response to an ACCESS call
  is not permanent. It was correct at the exact time that the server
  performed the checks, but not necessarily afterwards. The server can revoke
  access permission at any time."
- §3.3.4, of `DELETE`: "Many implementations do not directly support the
  ACCESS3_DELETE permission. Operating systems like UNIX will ignore the
  ACCESS3_DELETE bit if set on an access request on a non-directory object.
  In these systems, delete permission on a file is determined by the access
  permissions on the directory in which the file resides, instead of being
  determined by the permissions of the file itself. Thus, the bit mask
  returned for such a request will have the ACCESS3_DELETE bit set to 0,
  indicating that the client does not have this permission."
- §4.4, of the owner: "To get around this problem, the server's permission
  checking algorithm should allow the owner of a file to access it regardless
  of the permission setting. This is needed in a practical NFS version 3
  protocol server implementation, but it does depart from correct local file
  system semantics. This should not affect the return result of access
  permissions as returned by the ACCESS procedure, however."
- §4.4, of execute: "To make this work, the server allows reading of files if
  the uid given in the call has either execute or read permission on the
  file, through ownership, group membership or public access. Again, this
  departs from correct local file system semantics." That paragraph does not
  name `ACCESS`.
- §4.4, of `ACCESS`: "UNIX client semantics of access permission checking on
  open can be provided with the ACCESS procedure call in this revision, which
  allows a client to explicitly check access permissions without resorting to
  trying the operation."

**How Linux's server answers**, `fs/nfsd` at the tag `v6.6` (*Sources*).

- `nfsd_access` picks one of three tables by the object's type and, for each
  bit asked, calls `nfsd_permission`, the function its operations call, with
  the table's flags. A regular file's table, `nfs3_regaccess`, maps READ to
  `NFSD_MAY_READ`, EXECUTE to `NFSD_MAY_EXEC`, MODIFY to
  `NFSD_MAY_WRITE|NFSD_MAY_TRUNC` and EXTEND to `NFSD_MAY_WRITE`. A
  directory's, `nfs3_diraccess`, maps READ to `NFSD_MAY_READ`, LOOKUP to
  `NFSD_MAY_EXEC`, MODIFY to `NFSD_MAY_EXEC|NFSD_MAY_WRITE|NFSD_MAY_TRUNC`,
  EXTEND to `NFSD_MAY_EXEC|NFSD_MAY_WRITE` and DELETE to `NFSD_MAY_REMOVE`,
  which `vfs.h` defines as `(NFSD_MAY_EXEC|NFSD_MAY_WRITE|NFSD_MAY_TRUNC)`.
  No row carries `NFSD_MAY_OWNER_OVERRIDE` or `NFSD_MAY_READ_IF_EXEC`.
- Its loop grants a bit on `nfs_ok` and withholds it on three errors, under
  the comment "the following error codes just mean the access was not
  allowed, rather than an error occurred": `nfserr_rofs`, `nfserr_acces` and
  `nfserr_perm`. `nfsd_permission` returns `nfserr_rofs` for a request with
  `NFSD_MAY_WRITE`, `NFSD_MAY_SATTR` or `NFSD_MAY_TRUNC` on a read-only
  export or mount. So a read-only export's `ACCESS` grants no MODIFY, EXTEND
  or DELETE, by the check the operations make.
- The third table, `nfs3_anyaccess`, for an object that is neither, maps
  READ, EXECUTE, MODIFY and EXTEND by the mode, under a comment that says
  such calls check "things like /dev/null (which really, the server doesn't
  care about)", and that it ignores "read-only filesystem checks" for them.

**How clients read the answer** (*Sources*; Assumption 18).

- *Linux's client*, `fs/nfs/dir.c` at `v6.6`. It asks for READ, MODIFY and
  EXTEND, with DELETE and LOOKUP for a directory and EXECUTE otherwise. For a
  directory it takes write permission to need all of MODIFY, EXTEND and
  DELETE (`NFS_DIR_MAY_WRITE`), and search to need LOOKUP; for a regular file
  write needs MODIFY and EXTEND. It asks nothing of a symlink
  (`case S_IFLNK: goto out;`), and for a directory it skips the call when
  only write is asked, under "Optimize away all write operations, since the
  server will check permissions when we perform the op."
- *macOS's client*, Apple's NFS kext at `main`. `nfs_vnop_access` asks READ
  of a directory to list it, LOOKUP to search it, MODIFY and EXTEND to add a
  file or a subdirectory, and MODIFY to delete a child
  (`KAUTH_VNODE_DELETE_CHILD`). Of a file it asks READ to read, MODIFY and
  EXTEND to write, EXTEND to append and EXECUTE to execute. It asks DELETE of
  the object a deletion names, and `nfs3_access_rpc` then sets the bit itself
  when the server did not, under "If we asked for DELETE but didn't get it,
  the server may simply not support returning that bit (possible on UNIX
  systems). So, we'll assume that it is OK, and just let any subsequent
  delete action fail if it really isn't deletable." It answers `EACCES` when
  a right it asked for is not granted. XNU's `readlink` asks
  `KAUTH_VNODE_READ_DATA` of the symlink before it reads it, which on such a
  mount is an `ACCESS` for READ (§6).

**What the ADRs say.**

- ADR 0014 §1 gave `ACCESS` the form "`policy.AccessBits`, with the mount's
  mask", so that "`ACCESS` becomes derivable from the same rules (#97
  later)". Its §5 pins `AccessBits`, `Request`, the `Op`s and the *Growth*
  rule. Its §6 has `Access` not "go through `f.perm` until #97's fix", and
  its Assumption 21 says why: "deriving `ACCESS` from the operations' rows is
  a decision of its own, #97's." Its §7 has `Access` mask "on
  `mountReadOnly` only", and: "Whether it should be is #97's to decide." Its
  §11 leaves U5 unpinned, the directory bits the issue names, and pins W5,
  that `Access` never consults `f.perm`.
- ADR 0018 §2 puts under the policy rule a decision that "for `ACCESS` says
  what it would allow", by who the caller is or "by what the mount allows,
  whoever asks". Its §4 grants E11, the mount gate, and E12, "`ACCESS`'s
  read-only mask", each exempt from the rule's "in `internal/policy`".
- ADR 0025 §3 decides that `ACCESS` reports neither the owner override nor
  the read that execute gives, and leaves #97 four things: a direction, "what
  `ACCESS` grants, the operations allow", whose converse "is false since this
  ADR and stays false"; that a regular file's bits cannot be had by asking
  `OpRead`, `OpWrite` or `OpTruncate`, "which now carry the departures", so
  that #97 "keeps `Permits` for them, or adds a request that asks for the
  mode's verdict, under ADR 0014 §5's *Growth* rule"; what is still #97's,
  the directory bits, the diverged mount and the bits of an object that is
  neither; and what no longer is, EXECUTE granted and the read refused.
- ADR 0026 §5 grants DELETE in a sticky directory by the directory's mode,
  and leaves #97 the qualification the issue's comment repeats. Its §2 has a
  request that sets neither `Entry` nor `Replaced` judged by the directories
  alone.
- ADR 0011 §4 pins that on a read-only FS "`Access` grants none of
  `vfs.AccessModify`, `vfs.AccessExtend` and `vfs.AccessDelete`".
- ADR 0013 §1 gives permission rules to exhaustive tests of the policy and to
  security review, not to TLA+.

**What this constrains.** `policy.AccessBits`, `policy.Request`, the `Op`
list and three rows of `policy.Unix()`, in `internal/policy/policy.go`; how
`(*FS).Access`, in `internal/blobfs/fs.go`, answers; ADR 0014 §5, §6, §7 and
§11, ADR 0025 §1, §3 and §6 and ADR 0026 §5, by cross-reference; the
register, where E12 is revoked; and the tests of `internal/policy`,
`internal/blobfs` and `internal/nfs`. No non-test code of `internal/nfs` or
`internal/vfs` changes, and nothing in the bucket or the flags. On the wire,
`ACCESS` answers fewer bits in three cases (§1), and nothing else changes.

## Decision

### 1. The rule

`ACCESS` grants a bit of the mask it is asked for only if both of these
hold, at one instant (§4):

1. if the bit names a change, the mount takes changes: the mount gate,
   `mutable()`, answers nil;
2. the installed policy allows every request the bit names, for that kind of
   object.

The requests are the ones the operations themselves make (ADR 0014 §6), each
marked `Advisory` (§2, §3), and one more for execution, which no operation
makes. So `ACCESS` asks the rows that decide the operations, and no
predicate of its own.

**For a directory**, `Type == vfs.TypeDir`:

| Bit | What RFC 1813 has it name | A change | Its requests | The operation that asks the same |
|---|---|---|---|---|
| `vfs.AccessRead` | "read a directory" | no | `OpReadDir` | `ReadDir` |
| `vfs.AccessLookup` | "Look up a name in a directory" | no | `OpLookup` | `Lookup` |
| `vfs.AccessModify` | "modify existing directory entries" | yes | `OpRename`, with the directory as `Dst` too | a `Rename` within the directory |
| `vfs.AccessExtend` | "add directory entries" | yes | `OpAddEntry` | `Create`, `Mkdir`, `Symlink` |
| `vfs.AccessDelete` | "Delete an existing directory entry." | yes | `OpRemoveEntry` | `Remove`, `Rmdir` |
| `vfs.AccessExecute` | "no meaning for a directory" | | none: never granted | |

**For anything else**:

| Bit | What RFC 1813 has it name | A change | Its requests | The operation that asks the same |
|---|---|---|---|---|
| `vfs.AccessRead` | "Read data from file" | no | `OpRead` | `Read` |
| `vfs.AccessLookup` | "no meaning for non-directory objects" | | none: never granted | |
| `vfs.AccessModify` | "Rewrite existing file data" | yes | `OpWrite`, then `OpTruncate` | `Write`; a `SetAttr` that sets a size |
| `vfs.AccessExtend` | "Write new data" | yes | `OpWrite` | `Write` |
| `vfs.AccessDelete` | a bit a UNIX server returns as 0 here | | none: never granted | |
| `vfs.AccessExecute` | "Execute file" | no | `OpExecute` (§2) | none: no call executes |

No request describes an entry: `Entry` and `Replaced` are nil (§5).

**Why these requests** (Assumption 5).

- A bit is given the requests of every operation it names, so that a grant
  is backed by each. MODIFY on a file names a rewrite and, with it, a
  truncation, so it asks `OpWrite` and `OpTruncate`; EXTEND names new data
  alone, so it asks `OpWrite`. Those are Linux's flags for the two bits,
  `NFSD_MAY_WRITE|NFSD_MAY_TRUNC` and `NFSD_MAY_WRITE`.
- MODIFY on a directory is a change to an entry that is there, which is a
  rename within the directory: the one request `Rename` makes for it, with
  that directory as `Object` and as `Dst` (ADR 0026 §1).
- EXECUTE has no operation. A client executes a file by reading it, and the
  bit says whether it may start to. So it has a request of its own,
  `OpExecute`, which only `ACCESS` asks.

**Under `Unix()`** the rule gives, with `Permits` as ADR 0014 §5 has it:

| Object | Bit | Granted exactly when |
|---|---|---|
| directory | READ | `Permits(c, o, PermRead)` |
| directory | LOOKUP | `Permits(c, o, PermExec)` |
| directory | MODIFY, EXTEND, DELETE | the mount takes changes, and `Permits(c, o, PermWrite\|PermExec)` |
| anything else | READ | `Permits(c, o, PermRead)` |
| anything else | MODIFY, EXTEND | the mount takes changes, and `Permits(c, o, PermWrite)` |
| anything else | EXECUTE | `Permits(c, o, PermExec)` |

and no other bit. For root, `Permits` gives a directory everything, so root
is granted all five of a directory's bits on a mount that takes changes, as
before.

**What changes for a client.** Three answers, and no other:

| Object | The caller's class has | `ACCESS` grants | At `4ded55e` |
|---|---|---|---|
| directory | `r--` | READ | READ, LOOKUP |
| directory | `-w-` | nothing | MODIFY, EXTEND, DELETE |
| directory | `rw-` | READ | READ, LOOKUP, MODIFY, EXTEND, DELETE |
| directory | `--x` | LOOKUP | the same |
| directory | `r-x` | READ, LOOKUP | the same |
| directory | `-wx` | LOOKUP, MODIFY, EXTEND, DELETE | the same |
| directory | `rwx` | all five | the same |
| any, on a diverged mount | | no MODIFY, EXTEND or DELETE | as on a writable mount |
| any, on a read-only mount | | no MODIFY, EXTEND or DELETE | the same |
| regular file | any | what the mode gives (§3) | the same |

Every change takes a bit away, and each bit taken away is one whose
operation the server already refused: a `LOOKUP` in a directory the caller
cannot search, a `CREATE`, `REMOVE` or `RENAME` in one it cannot both write
and search, and any change on a diverged mount. No bit is granted that was
not granted before (§9).

### 2. The `policy` API

Pinned, in place of ADR 0014 §5's `AccessBits` and added to its `Op` list and
to `Request` as ADR 0026 §2 has it:

```go
const (
    // OpRead to OpRename, 1 to 12, as ADR 0014 §5 has them.
    OpExecute // 13
)

type Request struct {
    // Op, Caller, Object, Dst, UID, GID, Entry and Replaced, as ADR 0026 §2
    // has them.

    // Advisory marks a request made for ACCESS: it asks what a client
    // should let the caller open or attempt, ahead of any call, and nothing
    // is done on its answer. A file row answers it by the mode alone, with
    // neither the owner override nor the read that execute permission
    // gives, which stand in for a descriptor a client already holds open.
    // Every other row answers it as it answers the same request without the
    // mark. It is false on every request a call makes.
    Advisory bool
}

func AccessBits(p Policy, c vfs.Caller, o Object, want uint32, mutable bool) uint32
```

The names, the types, `OpExecute`'s value and the field's place, after
`Replaced`, are pinned; the doc comment is pinned in substance. The function
`AccessBits(c vfs.Caller, o Object) uint32` is gone: nothing keeps the old
signature. No other exported name of `internal/policy` changes, and none of
`internal/vfs` does (Assumption 20).

Every rule below is normative.

**`OpExecute`.** `Unix().Check` answers it, reading from the top:

1. `vfs.ErrIsDir`, if `Object` is a directory;
2. nil, if `Permits(Caller, Object, PermExec)`;
3. `vfs.ErrAcces` otherwise.

It reads `Op`, `Caller` and `Object`, and nothing else, `Advisory` included.
So an object that is not a directory may be executed by a caller whose class
of the mode bits has execute, and by root only if some execute bit is set,
which is `Permits`' rule. Any `Op` from 14 to 255, and 0, still answers
`vfs.ErrServerFault`.

**`Advisory`.** The three file rows read it, and no other row does:

- `OpRead`:
  1. `vfs.ErrIsDir`, if `Object` is a directory;
  2. nil, if `Permits(Caller, Object, PermRead)`;
  3. nil, if the request is not advisory, `Object` is a regular file and the
     caller is its owner;
  4. nil, if the request is not advisory, `Object` is a regular file and
     `Permits(Caller, Object, PermExec)`;
  5. `vfs.ErrAcces` otherwise.
- `OpWrite` and `OpTruncate`:
  1. `vfs.ErrIsDir`, if `Object` is a directory;
  2. nil, if `Permits(Caller, Object, PermWrite)`;
  3. nil, if the request is not advisory, `Object` is a regular file and the
     caller is its owner;
  4. `vfs.ErrAcces` otherwise.

These are ADR 0025 §1's rows with "the request is not advisory" added to
lines 3 and 4. So an advisory request to these rows is answered by lines 1,
2 and the last: the type, then the mode's verdict.

**Growth.** This is ADR 0014 §5's *Growth* rule applied: a request that
leaves `Advisory` false means what it meant before the field existed. Every
request `blobfs` made before this ADR, and every request a test built, is
answered as it was.

**Two laws**, which hold for `Unix()` and which X3, X4 and X7 (§10) test:

- *An advisory request only narrows.* For every request `r` whose `Object`
  is a regular file or a directory, if `Unix()` allows `r` with `Advisory`
  set, it allows `r` with it unset, and the two answers differ only when
  the `Op` is `OpRead`, `OpWrite` or `OpTruncate` and `Object` is a regular
  file. For every request whose `Op` is none of those three, whatever its
  `Object`, the two answers are the same.

  The law stops there (Assumption 24). It is not claimed for `OpRead`,
  `OpWrite` or `OpTruncate` on an `Object` that is neither a regular file
  nor a directory. The rows above give such a request one answer, marked or
  not, since lines 3 and 4 never reach it; but that is what the rows do
  today, and no test asks it. Unmarked, such a request is ADR 0014 §11's
  U3, which stays unpinned where `Permits` allows and is #69's to decide,
  while marked it is pinned as allowed there (§6; X1, X4 and X6). Claimed
  for every request, as this section had it until the pass of 2026-11-23,
  the law pinned the unmarked answer through the marked one, against §6
  and §10. Of the two, the law gives way: it is claimed where §8's S2 rests
  on it, and no further. ADR 0029 decides #69, and the law stays where it
  stops: since its §1 such a request unmarked is refused with
  `vfs.ErrInval`, while the same request marked is still answered by the
  mode, so for such an object the law is false by design (ADR 0029 §3).
- *Execution is backed by a read.* For a regular file, if `Unix()` allows
  `OpExecute`, it allows `OpRead` unmarked, by line 2 or by line 4. That is
  ADR 0025 §3's fourth item, which stays closed.

**`AccessBits(p, c, o, want, mutable)`** returns the OR of the bits it
grants, by this procedure and no other:

1. It takes the table of §1 for `o`: the directory's if
   `o.Type == vfs.TypeDir`, and the other for every other type, valid or
   not.
2. It goes through the six bits in ascending order of their values: READ,
   LOOKUP, MODIFY, EXTEND, DELETE, EXECUTE. For each bit that is in `want`:
   - if the table gives the bit no requests, it grants nothing and asks
     nothing;
   - if the bit names a change and `mutable` is false, it grants nothing and
     asks nothing;
   - otherwise it asks `p.Check` the bit's requests in the table's order,
     stops at the first that returns a non-nil error, whichever error that
     is, and grants the bit only if every one returned nil.
3. Each request is `Request{Op: op, Caller: c, Object: o, Advisory: true}`,
   and the `OpRename` has `Dst: o` as well. Nothing else is set: `UID` and
   `GID` are zero, `Entry` and `Replaced` are nil, and `Dst` is the zero
   `Object` on every request but the `OpRename`.
4. A bit that is not in `want`, and a bit of `want` outside the six
   `vfs.Access` constants, is never granted, and nothing is asked for it.

So it asks `p` at most five requests, each bit's own: with MODIFY and EXTEND
both wanted of a file, `OpWrite` is asked once for each. It returns no
error: a policy's refusal withholds a bit and fails nothing. `mutable` is
what its caller knows of the mount (§4), and its zero value is the safe one.
`p` is the installed policy and is never nil; what a nil `p` does is not
pinned.

**Purity.** ADR 0014 §5's rule covers the new `AccessBits` as it covered the
old, given a `p` that keeps it: it takes no lock, makes no store call, logs
nothing, modifies nothing it is given, `Caller.GIDs` included, keeps nothing
after it returns, and allocates nothing when `p` does not.

**What coder's commit chose**, recorded against `729360f` where the pins
above left room (Assumption 25):

- In `internal/policy/policy.go`, §1's two tables are data. An unexported
  `type accessRow struct { bit uint32; change bool; ops [2]Op }` is one bit
  of a table: the requests the bit names, in order, and whether it names a
  change. A zero `Op` ends the list, so a row whose first `Op` is zero has
  no request. `var accessDir` and `var accessOther` are arrays of six rows
  each, in ascending order of their bits, for a directory and for every
  other type. The three rows with no request, the directory's EXECUTE and
  the other table's LOOKUP and DELETE, have `change` false, which decides
  nothing, since a row with no request is passed over first.
- The two tables are package variables, since Go has no constant array.
  They are unexported, and nothing names them but their declarations and
  `AccessBits`, which takes a pointer to one, reads it, and writes neither.
  They hold nothing that moves and name no state, so ADR 0018 §2's state
  rule does not reach them (Assumption 27); that nothing writes them is
  review's to keep (§10).
- `AccessBits` takes the directory's table for `vfs.TypeDir` and the other
  for every other type, and walks it. One condition passes a row over,
  asking nothing: the bit is not in `want`, or the row has no request, or
  it names a change and `mutable` is false, tried in that order. For a row
  it asks, it builds each request as
  `Request{Op: op, Caller: c, Object: o, Advisory: true}`, sets `Dst` to
  `o` where the `Op` is `OpRename`, and leaves the row at the first `Check`
  that does not return nil. It ORs in the rows' own bits and no other,
  which is why a bit outside the six is never granted.
- `fileAccess`, the helper behind the three file rows, gains a fourth
  parameter, `advisory bool`, which `Check` fills from `r.Advisory`. Lines
  3 and 4 sit under one condition, `!advisory && o.Type == vfs.TypeReg`,
  and lines 1, 2 and the last are as they were. `Check`'s cases for
  `OpRead` and for `OpWrite` and `OpTruncate` are the only code that reads
  the field, and `AccessBits` is the only code that sets it.
- The `OpExecute` row is a case of `Check`'s own, of the three lines above,
  and does not go through `fileAccess`. The constant follows `OpRename` in
  the same `iota` block, so its value is 13. The package comment's `Spec:`
  line gains this ADR's §1, §2 and §3.
- In `internal/blobfs/fs.go`, `Access` is §7's sketch, statement for
  statement, and it and `FS.perm` carry the doc comments §7 asks for. The
  file names no `vfs.Access` bit, and within it `f.mode` is read by `Run`
  and by `mutable()` alone. No other line of the file changed (§11).

### 3. `ACCESS` still reports the mode's permissions on a file

ADR 0025 §3 hands #97 two things the operations allow and `ACCESS` did not
report. Neither is reported now (Assumption 3).

**The owner of a regular file, whatever its mode.** `OpRead`, `OpWrite` and
`OpTruncate` allow the owner by line 3. `ACCESS` does not report it.

1. RFC 1813 §4.4 asks for exactly that, in the sentence that follows the
   override: "This should not affect the return result of access permissions
   as returned by the ACCESS procedure, however."
2. `ACCESS` is the client's check at open: it exists where "permission
   checking is done only when a file or directory is opened", and "The intent
   is to make the behavior of opening a remote file more consistent with the
   behavior of opening a local file." A local open for writing, of a file
   whose mode denies its owner write, fails. The override stands in for a
   descriptor that is already open, which the server cannot see; it is not a
   permission to open.
3. Linux's server gives its `ACCESS` rows no `NFSD_MAY_OWNER_OVERRIDE`.
4. It is #93's third done-when, which ADR 0025 records.

**A caller with execute and not read.** `OpRead` allows a regular file's
read by line 4. `ACCESS` reports EXECUTE and not READ.

1. §4.4's paragraph on execute does not name `ACCESS`, so the RFC does not
   settle it in terms. Its reason does: the server "can not tell the
   difference between a normal file read ... and a demand pagein read". A
   client can, and has a bit for each: it asks EXECUTE to execute and READ to
   read. The departure exists only because a `READ` does not say which of
   the two it is; an `ACCESS` does say, so it is answered for the one it
   asks about.
2. `ACCESS3_READ` is "Read data from file". A local open for reading of an
   execute-only file fails, and a client that granted READ here would let
   every user who may run a program copy it, through an ordinary open.
3. Linux's server maps READ to `NFSD_MAY_READ`, with no
   `NFSD_MAY_READ_IF_EXEC`.
4. It is what ADR 0025 §2 gives as what a client observes. By the session's
   report the owner confirmed that section's rule and its cost (ADR 0025,
   *Context*, decision 3).

**How.** A request marked `Advisory` gets neither line (§2). So `ACCESS`
asks the rows that decide `Read`, `Write` and a size change, and gets from
them the mode's verdict, which is the second of the two ways ADR 0025 §3
left open: "a request that asks for the mode's verdict, under ADR 0014 §5's
*Growth* rule". For a regular file owned by `501:20`, ADR 0025 §3's table
stands, cell for cell:

| Mode | Caller | `ACCESS` grants | The rows allow a call |
|---|---|---|---|
| `0o644` | the owner | read, modify, extend | read, write, truncate |
| `0o444` | the owner | read | read, write, truncate |
| `0o200` | the owner | modify, extend | read, write, truncate |
| `0o100` | the owner | execute | read, write, truncate |
| `0o000` | the owner | nothing | read, write, truncate |
| `0o711` | a group member, or anyone else | execute | read |
| `0o000` | a group member, or anyone else | nothing | nothing |

**So the done-when holds one way.** "ACCESS derives from the same rules as
the operations" is met as §8 states it: every bit `ACCESS` grants on a
regular file or a directory, the operations allow. On a regular file the
operations allow more, in these two ways, and a design that made the two
equal would undo ADR 0025 §3.

### 4. A mount that takes no change is granted none

**The gate.** Whether the mount takes changes is `mutable()`'s answer, the
predicate every operation that changes the filesystem asks first (ADR 0014
§6, stage A; §7). `Access` hands `AccessBits` that answer as `mutable`: true
exactly when `mutable()` returns nil. `AccessBits` then grants no bit that
names a change, and asks the policy nothing for one, as an operation refused
at stage A never reaches its row.

| Mode | `mutable()` | `ACCESS` | At `4ded55e` |
|---|---|---|---|
| `Writable` | nil | may grant MODIFY, EXTEND, DELETE | the same |
| `ReadOnly` | `vfs.ErrROFS` | grants none of the three | the same |
| `Diverged` | `vfs.ErrStale` | grants none of the three | granted them |
| any other value | `vfs.ErrServerFault` | grants none of the three | granted them |

READ, LOOKUP and EXECUTE are granted in every mode, by the policy alone,
since `Read`, `Lookup` and `ReadDir` have no stage A and a diverged mount
still serves them.

**No condition of `Access`'s own.** `Access` compares no mode and names no
`vfs.Access` bit. The state reaches it through the machine's one predicate,
and which bits name a change is the policy's table (§1). So a state added to
the mode later is masked until its ADR has `mutable()` answer nil for it,
with no line of `Access` to remember (ADR 0014, Assumption 24).

**E12 is revoked, and E11 stands** (Assumption 14). E12 exempted "the mask in
`(*FS).Access`" from the policy rule's "in `internal/policy`". The mask is
now in `internal/policy`: `AccessBits` holds which bits name a change, and
withholds them. What is left in `blobfs` is one call of `mutable()`, which
is E11's code, used as the gate it is. E11's reason is as it was: the gate
judges the mount, not the caller, and `policy` cannot name the mode's type.
No exemption is added.

**One instant.** `Access` makes its one call of `mutable()` in its hold of
`FS.mu` for reading, after it has resolved the handle. Every `Fire` on the
mode is made holding `FS.mu` for writing, or before the FS is shared, so the
mode cannot move during that hold. The answer is therefore of one instant:
the mode, and the object's type, mode, owner and group, as they stood
together.

**A grant is not recalled.** A client caches what it is told. A grant made
before a mount diverged stays with that client for as long as it keeps its
cached answer, which was not read for any client (Assumption 13), and the
operations answer `vfs.ErrStale` whatever the client believes. That is what
"advisory" means: the answer "was correct at the exact time that the server
performed the checks, but not necessarily afterwards."

**What a client can observe on a diverged mount.** Once it asks again, a
client that checks at open refuses an open for writing, and an `access(2)`
for write, with its own errno: `EACCES` in Apple's client, as read, and in
Linux's by recall (Assumption 13). Before, the open succeeded and the first
`WRITE`, `CREATE`, `REMOVE` or `RENAME` answered `NFS3ERR_STALE`. A program
that opens a file for reading and writing and only reads now fails at the
open, as it does on a read-only mount. Reads, lookups and listings are as
they were.

### 5. Directories, and the sticky bit

**The directory bits ask search.** LOOKUP is granted with search and not
with read alone, and MODIFY, EXTEND and DELETE with write and search and not
with write alone, because those are what `OpLookup`, `OpRename`,
`OpAddEntry` and `OpRemoveEntry` ask. They are Linux's flags for the same
bits. This pins ADR 0014 §11's U5.

**In a sticky directory, DELETE and MODIFY speak for the directory**
(Assumption 11). ADR 0026 §1's rows refuse, with `vfs.ErrPerm`, a caller who
takes out of a sticky directory an entry it does not own, unless it is root
or owns the directory. `ACCESS` is asked about the directory and names no
entry. Its requests describe none, so by ADR 0026 §2 the rows judge the
directory alone, by their lines 1 and 2. So:

- DELETE is granted to a caller the directory's mode gives write and search,
  in a sticky directory as in any other. It says that the caller may remove
  an entry it owns; for root and for the directory's owner, any entry.
- MODIFY is granted the same way, and says the same of a rename within the
  directory. ADR 0026 §5 named DELETE alone; a rename takes a name out too,
  so the qualification is MODIFY's as well.
- EXTEND is not qualified: adding an entry takes no name out.

That is the first of the two readings the issue's comment offers, "some
entry may be deleted", and not an exception for sticky directories.

1. It is what the bit can say. "Delete an existing directory entry." names
   an entry, not every entry, and the answer is about one object.
2. It is true of the caller's own entries, which it can also make: a caller
   with write and search can add an entry and then remove it.
3. Withholding the bits would break what works. Linux's client takes write
   permission on a directory to need MODIFY, EXTEND and DELETE together, so
   without DELETE a user could not create in a shared directory wherever
   that client asks. macOS's client asks MODIFY before it deletes a child,
   so without MODIFY a user could not remove its own file there.
4. Linux's server grants DELETE from the directory alone, by
   `NFSD_MAY_REMOVE`, and applies the sticky rule at the operation.
5. A client that asks the server to enforce still gets the right refusal:
   the `REMOVE`, `RMDIR` or `RENAME` answers `NFS3ERR_PERM` (ADR 0026 §3).

### 6. An object that is neither a regular file nor a directory

A symlink, and an inode whose type no operation of this server makes, is
answered by the second table of §1, as a regular file is. The file rows give
it no line 3 or 4 in any case (ADR 0025 §1), so `Advisory` changes nothing
for it, and `OpExecute` judges it by the mode. Its bits are therefore what
they were at `4ded55e`: READ with read, MODIFY and EXTEND with write on a
mount that takes changes, EXECUTE with execute, and never LOOKUP or DELETE.
The one change is §4's: a diverged mount now withholds MODIFY and EXTEND from
it too (Assumption 12).

**Pinned as it is, not as it should be.** X1 pins these bits, as ADR 0014
§11's P9 did. §8's property is not claimed for such an object: what a
`Read`, a `Write` or a size change of a symlink should answer is #69's, and
ADR 0014 §11's U3 stays unpinned for the calls where `Permits` allows. Nor
is §2's first law claimed for such an object: the rows answer an advisory
request of one as they answer the same request unmarked, and no test holds
them to that (§2; Assumption 24). Two things bind #69's ADR when it comes:

- Its change to the three file rows reaches `ACCESS`, which asks them. If a
  symlink is refused `OpRead`, `ACCESS` stops granting it READ unless that
  ADR says otherwise, and X1 fails until it decides. That ADR may instead
  refuse the call alone and leave the advisory answer as it is: §2's first
  law does not reach such an object, so nothing here stands against it,
  and no test here fails for it.
- A symlink's READ is not idle. XNU's `readlink` asks
  `KAUTH_VNODE_READ_DATA` of the symlink, which Apple's NFS client turns
  into an `ACCESS` for READ and answers `EACCES` when it is not granted
  (*Sources*; Assumption 18). So a server that granted a symlink no READ
  would stop `readlink(2)` on a macOS client, while `READLINK` itself asks
  no policy here.

The mount gate is kept for these objects, where Linux's server drops it for
device files, since a client opens a device on its own machine. This server
makes no device, and ADR 0011 §4 pins that a read-only FS grants none of the
three bits. ADR 0029 is #69's ADR: it refuses the call alone, with
`vfs.ErrInval`, and leaves the advisory answer as it is, so these bits stand
for good, for the reason the second point above gives and for one more that
it reads from Apple's client (its §3), and it has `READLINK` go on asking no
policy (its §4).

### 7. How `blobfs` asks

`(*FS).Access` becomes, in substance:

```go
func (f *FS) Access(ctx context.Context, c vfs.Caller, h vfs.Handle, want uint32) (uint32, error) {
    f.mu.RLock()
    defer f.mu.RUnlock()
    n, err := f.resolve(h)
    if err != nil {
        return 0, err
    }
    return policy.AccessBits(f.perm, c, objectOf(n), want, f.mutable() == nil), nil
}
```

Pinned: the hold, `FS.mu` for reading, across the resolve, the one call of
`mutable()` and the call of `AccessBits`; their order; that `f.perm` is the
policy asked; that `Object` is `objectOf` of the inode as it stands in that
hold; and that `Access` compares no mode and names no `vfs.Access` bit.

- **Stages.** `Access` has stage C, whose errors are `vfs.ErrBadHandle` and
  `vfs.ErrStale`, and stage D, which returns no error. It still has no stage
  A: the gate's answer is an input to its answer, and refuses nothing.
- **It asks `f.perm`.** So ADR 0014 §6's sentence that `Access` "does not go
  through `f.perm` until #97's fix" is spent, and its list of the methods
  that consult no policy is `GetAttr`, `ReadLink`, `FSStat` and `Commit`.
  The requests `Access` makes are §1's, through `AccessBits`, and it makes
  none of its own.
- **It changes nothing.** It takes no write hold, calls no `markDirty` and
  opens no `FS.open` entry. `FS.changes` and `FS.dirty` are as they were.
- **The safety rule does not bind it.** ADR 0014 §11's rule keeps a test
  that installs a policy wider than `Unix()` to objects of the type each
  operation needs, because `blobfs` relies on the type rows to protect its
  own structures. `Access` writes nothing and reads only the inode's type,
  mode, owner and group, so it is safe on any object under any policy.
- **Doc comments**, pinned in substance. `Access`'s says that it answers
  through `policy.AccessBits`, which asks `f.perm` the operations' own
  requests, and grants no change unless `mutable()` allows one, and cites
  this ADR. `FS.perm`'s says that every check of what a caller may do goes
  through it, `Access`'s included; its sentence that `Access` alone reads
  `policy.AccessBits` and the mount's mask instead goes.
- **No other method changes**, `mutable()` included: it gains a caller and
  no line. `internal/nfs`'s `access` handler is untouched.

### 8. What holds between `ACCESS` and the operations

For one caller, one object and one instant, with `A` the mask `Access`
returns for every bit asked.

- **S1, for any installed policy.** A bit is in `A` only if the policy
  allowed every request §1 lists for it and, for a change, `mutable()`
  answered nil. This holds by construction, for `Unix()` and for whatever an
  ADR installs behind it.
- **S2, for `Unix()`, on a regular file or a directory.** If a bit is in
  `A`, the operations it names, made then by that caller on that object,
  pass the mount gate and their row:
  - a file's READ: `Read`. Its MODIFY: `Write`, and a `SetAttr` that sets a
    size. Its EXTEND: `Write`. Its EXECUTE: `Read`, for the page-in;
  - a directory's READ: `ReadDir`. Its LOOKUP: `Lookup`. Its EXTEND:
    `Create`, `Mkdir` and `Symlink`. Its DELETE: `Remove` and `Rmdir` of an
    entry the caller owns. Its MODIFY: `Rename`, within the directory, of an
    entry the caller owns. Where the directory is not sticky, or the caller
    is root or owns it, "an entry the caller owns" reads "any entry".

  This follows from S1 and §2's two laws.
- **S3, for `Unix()`, on a directory: the converse.** READ, LOOKUP and
  EXTEND are granted exactly when `ReadDir`, `Lookup` and a creation pass
  the gate, where they have one, and their row. DELETE and MODIFY are
  granted exactly when the row allows a request that describes no entry.
- **S4, on a regular file: no converse.** The operations allow what `A`
  lacks in exactly two ways, §3's.
- **S5, on an object that is neither:** S1, and nothing more (§6).

**What `ACCESS` does not speak for.** Stage B and stage E: a name, an
offset, a size, whether an entry exists or a directory is empty. The other
directory of a rename that crosses two. An entry's owner (§5). And any later
instant (§4).

### 9. Security

**Nothing widens.** Under `Unix()`, for every caller, object and mode of the
mount, the mask is a subset of the mask at `4ded55e`. For a non-directory
each bit's condition is the one `AccessBits` had. For a directory, search is
within read-or-search, and write-and-search is within write. And for the
mount, `Writable` is within not-`ReadOnly`. X1 pins it against an oracle
written from ADR 0014 §5's old rule.

**What narrows**: §1's three answers. Each takes away a bit whose operation
the server already refused, so no call that worked stops working at the
server. On a client that checks first, the refusal moves from the server's
reply to the client's own check.

**`ACCESS` is the boundary for a user behind a kernel client, in two
places.** ADR 0025 §4 rests two protections on it: a read-only mode keeps a
file from its owner's accident, and an execute-only mode keeps a file's
contents from a user who may run it, in each case because the client asks
`ACCESS` at open and is told the mode's answer. Since this ADR `ACCESS`
asks the rows that carry the two departures, so those protections rest on
one thing: that every request `ACCESS` makes is marked `Advisory`, and that
an advisory request gets neither line. A request left unmarked would tell
every kernel client that an owner may open its read-only file for writing
and that an execute-only file may be opened for reading. X1, X2 and X4 pin
the mark and the rows, ADR 0025 §6's A4 and A7 pin the answers, and review
checks that `AccessBits` builds no request without it.

**The uid is the client's claim**, as in every row (ADR 0015 §8; ADR 0025
§4). A caller that speaks the protocol and chooses its uid learns nothing
from `ACCESS` that it could not have by claiming uid 0, and is bound by
nothing `ACCESS` says. The operations enforce, and `ACCESS` advises.

**The mount.** The gate fails closed: any mode `mutable()` does not answer
nil for is masked, and `mutable`'s zero value masks. A wider answer would
let a client open for writing on a mount that takes none, which the
operations would still refuse.

**A client cannot make it expensive.** One `ACCESS` asks the policy at most
five requests, whatever mask it sends, takes one read hold, allocates
nothing and logs nothing. A refusal is not logged, as none is (ADR 0018
§5).

**The seam.** `Access` now reads `f.perm`, which `New` sets and nothing else
writes (ADR 0014 §6). A policy an ADR installs behind `Unix()` is asked the
same requests for `ACCESS` as for the operations, so what it refuses an
operation outright it refuses `ACCESS` too.

### 10. The test surface

Every name is spelled as in Go. test-author cannot read the non-test files
of `internal/policy`, `internal/blobfs` or `internal/nfs`, so what a test
may use is here, in ADR 0014 §5 and §11, ADR 0025 §6, ADR 0026 §8, ADR 0007
§5, `internal/vfs/vfs.go`, or a `_test.go` file. The tests are X1 to X13.
The bits are written R, L, M, E, D and X below for `vfs.AccessRead`,
`vfs.AccessLookup`, `vfs.AccessModify`, `vfs.AccessExtend`,
`vfs.AccessDelete` and `vfs.AccessExecute`, whose values are 0x01, 0x02,
0x04, 0x08, 0x10 and 0x20.

**What becomes pinned.** ADR 0014 §11's U5, whole. P9 is replaced by X1,
which pins every bit for every type. U3 stays unpinned for a call, a request
that is not advisory, where `Permits` allows, and U6 stays as it is; no test
here relies on either. U3 was written of the requests a call makes, before
the mark existed, and by the *Growth* rule it speaks of those alone: an
advisory request of such an object is no part of it, and X1, X4 and X6 pin
it. So no test asks §2's first law of an object that is neither a regular
file nor a directory: asked there, beside those advisory answers, it would
pin U3 where `Permits` allows (§2). Since ADR 0029, U3 is pinned for a call
too, as `vfs.ErrInval`, and the branch of X4 and of P13 for such an object
is that ADR's N2 and N3 (its §9).

**`internal/policy`** (package `policy_test`). A new file,
`internal/policy/access_test.go`, with helpers of its own prefixed `ac`, and
changes to `policy_test.go` and `owner_access_test.go`. They use
`policy_test.go`'s fixtures: the callers and `p2Callers`; `p2Modes`;
`p2Types`; `mkObj`, `foreignDir`, `mkReq`, `mkRename`, `stkOwned`,
`stkRemove` and `stkRename`; `oraclePermits`; `limiter`; `opName`;
`unixCase`, `unixCases`, `p5Cases` and `checkUnix`; `allAccess`; and
`owner_access_test.go`'s `oaAllTypes`, `oaFileOps` and `oaRowPerm`. Every
refusal a test pins is compared with `==` against the bare `vfs.Status`.

- **X1. The mask, exhaustively** (`TestAccessBitsExhaustively`). For every
  caller of `p2Callers()`, every type of `oaAllTypes`, every mode of
  `p2Modes()` and `mutable` true and false, with `mkObj(type, mode)`, and
  for a directory `foreignDir(mode)` too:
  `AccessBits(Unix(), c, o, allAccess, mutable)` equals, compared with
  `==`, an oracle written from §1's table for `Unix()`, with `oraclePermits`
  standing for `Permits`. It also shows:
  - *nothing widens*: with `mutable` true, the mask has no bit outside what
    ADR 0014 §5's rule gave, written as a second oracle: R with read; L for
    a directory with read or execute; M and E with write; D for a directory
    with write; X for a non-directory with execute and
    `o.Mode & 0o111 != 0`, as ADR 0014 §5 writes it, a condition `Permits`
    already implies for a non-directory, so that the oracle is the old rule
    word for word;
  - *the mask asked for*: over a smaller grid, at least the modes 0,
    `0o111`, `0o222`, `0o444`, `0o333`, `0o555`, `0o666`, `0o700`, `0o070`,
    `0o007`, `0o644`, `0o755`, `0o777`, `0o1777`, `0o4711` and `0o7000`, the
    types `vfs.TypeReg`, `vfs.TypeDir` and `vfs.TypeLnk`, every caller and
    both values of `mutable`: for every `want` from 0 to 63 the answer is
    the answer for `allAccess`, `& want`; and for `^uint32(0)`,
    `allAccess|0x40` and `allAccess|1<<31` it is the answer for
    `allAccess`.
- **X2. The requests** (`TestAccessBitsAsksTheOperationsOwnRequests`). With
  a recording policy of the test's own, a `Func` that keeps a copy of each
  request under its own lock and refuses the `Op`s it is told to, for a
  directory, a regular file, a symlink and an object of type
  `vfs.FileType(0)`, and a caller with supplementary groups:
  - allowing everything, `mutable` true, every bit asked: a directory is
    answered R, L, M, E and D, having been asked exactly `OpReadDir`,
    `OpLookup`, `OpRename`, `OpAddEntry`, `OpRemoveEntry`, in that order;
    each of the other three is answered R, M, E and X, having been asked
    exactly `OpRead`, `OpWrite`, `OpTruncate`, `OpWrite`, `OpExecute`;
  - every request has `Caller` equal to the caller, its `GIDs` by content,
    `Object` the object, `Advisory` true, `UID` and `GID` zero, and `Entry`
    and `Replaced` nil; `Dst` is the object on the `OpRename` and the zero
    `Object` on every other;
  - one bit asked at a time: exactly that bit's requests, and none for a
    directory's X or for another object's L or D;
  - `mutable` false, every bit asked: a directory is answered R and L,
    asked `OpReadDir` and `OpLookup` alone; a file R and X, asked `OpRead`
    and `OpExecute` alone;
  - one `Op` refused at a time, each `Op` from `OpRead` to `OpExecute`, 1
    to 13, of each of the four objects, with each of `vfs.ErrNXIO`,
    `vfs.ErrAcces`, an `errors.New` value, a wrapped error and a pointer
    error: the answer lacks exactly the bits whose requests hold that `Op`,
    which for an `Op` the object's table never asks is none, and the
    requests are the ones §2's procedure gives, which the test writes out
    as an oracle of its own. Two cases are given in so many words, of each
    object that is not a directory. Refusing `OpTruncate` takes M and
    leaves E. Refusing `OpWrite` takes M and E, and the requests are then
    `OpRead`, `OpWrite`, `OpWrite`, `OpExecute`, since M stops at its first
    refusal;
  - `want` 0, and a `want` with no bit of the six: the answer is 0 and
    nothing is asked.
- **X3. The direction** (`TestWhatAccessBitsGrantsTheRowsAllow`; §8's S2 and
  S3; the issue's done-when). For every caller of `p2Callers()` and every
  mode of `p2Modes()`, with `u` a `Unix()` and requests that are not
  advisory:
  - a regular file `mkObj(vfs.TypeReg, mode)`, `mutable` true: R in the
    mask means `u` allows `OpRead`; M, `OpWrite` and `OpTruncate`; E,
    `OpWrite`; X, `OpRead`;
  - a directory, `mkObj(vfs.TypeDir, mode)` and `foreignDir(mode)`,
    `mutable` true: R is in the mask exactly when `u` allows `OpReadDir`; L,
    `OpLookup`; E, `OpAddEntry`; D, `OpRemoveEntry` with no `Entry`; M,
    `mkRename` with that directory twice. And D in the mask means `u`
    allows `stkRemove` with `stkOwned` of the caller's uid, and M means it
    allows `stkRename` within the directory with that entry and nothing
    replaced;
  - with `mutable` false, M, E and D are in no mask.
- **X4. An advisory request is answered by the mode alone**
  (`TestAnAdvisoryRequestIsAnsweredByTheModeAlone`; §2, §3). Over the grid
  of ADR 0025 §6's A1, every caller, every type of `oaAllTypes`, every mode
  of `p2Modes()` and each of `oaFileOps`, with `Advisory` set: a directory
  is `vfs.ErrIsDir`; a regular file is nil exactly when `oraclePermits`
  gives the row's permission, and `vfs.ErrAcces` otherwise; any other type
  is nil exactly when `oraclePermits` gives it, and otherwise a refusal
  whose error is not pinned. It also asks §2's first law, of a regular file
  and of a directory alone: where the marked request is allowed, the same
  request unmarked is allowed. It does not ask it of another type, where
  the unmarked request is U3.
- **X5. The `OpExecute` row, exhaustively**
  (`TestTheExecuteRowExhaustively`). Over the same grid, with `Advisory`
  unset and set: a directory is `vfs.ErrIsDir`; anything else is nil exactly
  when `oraclePermits(c, o, PermExec)`, and `vfs.ErrAcces` otherwise.
- **X6. The rows by name.** `unixCases()`, P4's table, gains these, so that
  P10 to P13 ask them too. "Advisory" means the request has the mark.
  - `OpExecute`, nil: the owner, of `0o100`; a primary-group member and a
    supplementary-group member, of `0o010`; other, of `0o001`; root, of
    `0o001`; the owner, of a symlink `0o777`; other, of a
    `vfs.FileType(0)` object `0o111`; and other, of `0o001`, advisory.
  - `OpExecute`, `vfs.ErrAcces`: the owner, of `0o677`; a primary-group
    member, of `0o701`; other, of `0o776`; root, of `0o000` and of `0o666`.
  - `OpExecute`, `vfs.ErrIsDir`: the owner, and root, of a directory
    `0o777`.
  - Advisory `OpRead`, nil: the owner, of `0o400`; a primary-group member,
    of `0o040`; other, of `0o004`; root, of `0o000`; other, of a symlink
    `0o004`.
  - Advisory `OpRead`, `vfs.ErrAcces`: the owner, of `0o000` and of
    `0o077`; the owner who is also in the group, of `0o070`; a
    primary-group member and a supplementary-group member, of `0o010`;
    other, of `0o001`.
  - Advisory `OpWrite` and `OpTruncate` each, nil: the owner, of `0o200`; a
    primary-group member, of `0o020`; root, of `0o000`. And
    `vfs.ErrAcces`: the owner, of `0o000` and of `0o444`; the owner who is
    also in the group, of `0o555`.
  - Advisory, `vfs.ErrIsDir`: `OpRead`, `OpWrite` and `OpTruncate` by the
    owner of a directory `0o777`.
  - As a refusal only: advisory `OpRead` by the owner of a symlink `0o000`.

  `p5Cases()`, P5's table, gains: `OpExecute` by other, of a directory
  `0o000`; and advisory `OpRead` by the owner of a directory `0o000`; each
  `vfs.ErrIsDir`.
- **X7. The rest of `policy_test.go`.**
  - `TestConstantsArePinned` has `OpExecute` as 13, and `opName` names it.
  - P6: `unknownOps()` is 0 and every `Op` from 14 to 255.
  - P9, `TestAccessBitsPinnedRows`, is removed. X1 pins everything it
    pinned, and what U5 kept it from pinning.
  - P10 and P12 call `AccessBits(Unix(), c, o, allAccess, true)` where they
    called `AccessBits(c, o)`; P12 asks every `Op` from 0 to 14.
  - P13, `TestUnreadFieldsChangeNothing`, gains a loop over every P4 and P5
    request: with `Advisory` flipped, the answer is the same unless the `Op`
    is `OpRead`, `OpWrite` or `OpTruncate`; and for those three, of a
    regular file or a directory, a request that is refused unmarked is
    refused marked. A request for one of the three of any other type is
    passed over, X6's two symlink rows among them, since unmarked it is U3
    (§2).
  - `sampleRequest()` sets `Advisory`, and `sameRequest` compares it, so
    that P8 still shows a `Func` given the request unchanged.
  - The header, which says that U5 is unpinned, says that it is pinned
    since this ADR, and where.
- **`owner_access_test.go`.** A4's two calls of `AccessBits` become
  `AccessBits(Unix(), c, o, allAccess, true)`. Every assertion stands, and
  is now the pin that the mark keeps the departures out of `ACCESS`.

**`internal/blobfs`** (package `blobfs`). The harness is ADR 0011 §7's, as
ADR 0014 §11, ADR 0025 §6 and ADR 0026 §8 apply it: a `*store.Local` under
`t.TempDir()` behind an unarmed `uc43Store`, through `uc43Setup`;
`uc43Config`; every `New` and every `FS` call in `uc43Go`, waited for under
`uc43Bound`; nothing sleeps, polls or runs in parallel; `FS.dirty` and
`FS.changes` read through `sa24Read`. A test may reuse `uc43Env` and its
methods, `uc43WriteForeignRoot` and `uc43Writer`; the `pw` helpers,
`pwMember`, `pwAllAccess`, `pwW2Tree` and its `Object`s; the `sa24` helpers
and `sa24Other`; `sp24Root`, `sp24Recorder` and `sp24Mount`; the `oa`
helpers; and `testCaller`. Of unexported non-test names it uses
`mountDiverged`, through `uc43Env.mode`, and `FS.perm`, written once as ADR
0014 §11's W1 allows.

The callers are `testCaller` (`vfs.Caller{UID: 501, GID: 20}`), who owns the
root directory and what it makes; `pwMember` (`vfs.Caller{UID: 503, GID:
20}`), in the group; `sa24Other` (`vfs.Caller{UID: 502, GID: 30}`),
neither; and `sp24Root` (`vfs.Caller{UID: 0}`). Facts a test may rely on,
beside those of ADR 0014 §11, ADR 0025 §6 and ADR 0026 §8: `Access` returns
a mask and nil for a live handle, whatever the policy answers; it changes
nothing, so `FS.changes` and `FS.dirty` are as they were; and it may be
asked of an object of any type under any policy (§7).

Two files, so that the first builds on `4ded55e`'s tree and carries the red
run.

`internal/blobfs/access_test.go`, with helpers prefixed `ac`. It uses no
name the code lacks at `4ded55e` and no helper this PR adds to another file.

- **X8. Through the FS, under `Unix()`**
  (`TestAccessReportsWhatTheOperationsAllow`; §1, §5, §8). `testCaller`
  makes, in the root, each directory below with the mode given and a file
  `k` in it, and each file below with no `Mode`, 20 bytes written, and then
  its mode set, so that the setup rests on no override. `d555` and `d000`
  are made `0o755`, filled, and then set. `Access` with every bit asked
  gives:

  | Directory | Mode | `testCaller` | `pwMember` | `sa24Other` | `sp24Root` |
  |---|---|---|---|---|---|
  | `d755` | `0o755` | R L M E D | R L | R L | R L M E D |
  | `d744` | `0o744` | R L M E D | R | R | R L M E D |
  | `d722` | `0o722` | R L M E D | nothing | nothing | R L M E D |
  | `d766` | `0o766` | R L M E D | R | R | R L M E D |
  | `d733` | `0o733` | R L M E D | L M E D | L M E D | R L M E D |
  | `d711` | `0o711` | R L M E D | L | L | R L M E D |
  | `d777` | `0o777` | R L M E D | R L M E D | R L M E D | R L M E D |
  | `d700` | `0o700` | R L M E D | nothing | nothing | R L M E D |
  | `d750` | `0o750` | R L M E D | R L | nothing | R L M E D |
  | `d705` | `0o705` | R L M E D | nothing | R L | R L M E D |
  | `t1777` | `0o1777` | R L M E D | R L M E D | R L M E D | R L M E D |
  | `d555` | `0o555` | R L | R L | R L | R L M E D |
  | `d000` | 0 | nothing | nothing | nothing | R L M E D |

  | File | Mode | `testCaller` | `pwMember` | `sa24Other` | `sp24Root` |
  |---|---|---|---|---|---|
  | `f644` | `0o644` | R M E | R | R | R M E |
  | `f600` | `0o600` | R M E | nothing | nothing | R M E |
  | `f755` | `0o755` | R M E X | R X | R X | R M E X |
  | `f711` | `0o711` | R M E X | X | X | R M E X |
  | `f444` | `0o444` | R | R | R | R M E |
  | `f000` | 0 | nothing | nothing | nothing | R M E |
  | `f070` | `0o070` | nothing | R M E X | nothing | R M E X |

  And for each cell the operations agree. What a cell grants is read from
  the tables above and not from the mask `Access` returned, so that a wrong
  mask fails once, as a mask, and the operations are judged apart from it:
  - a directory's R granted: the caller's `ReadDir` returns nil; not
    granted, `vfs.ErrAcces`. Its L: `Lookup` of `k` returns nil, or
    `vfs.ErrAcces`;
  - a directory's E, M and D granted: the caller's `Create` of a fresh name
    returns nil, its `Rename` of that name to another within the directory
    returns nil, and its `Remove` of that returns nil. Not granted: its
    `Create` of a fresh name, its `Rename` of `k` within the directory and
    its `Remove` of `k` each return `vfs.ErrAcces`;
  - in `t1777`, for `pwMember` and `sa24Other`, who are granted D and M:
    after the three calls above on a name of their own, `Remove` of `k` and
    `Rename` of `k` within the directory return `vfs.ErrPerm` (§5);
  - a file's R or X granted: the caller's `Read` returns nil. Its M: a
    `Write` of one byte at 0 and a `SetAttr` of the size it has return nil.
    Its E: a `Write` of one byte at its end returns nil;
  - the two departures, seen once each: `testCaller`, granted no M on
    `f444`, writes it; and `sa24Other`, granted no R on `f711`, reads it
    (§3).

  And `sa24Other`'s `Access` of `d777`, asked one bit at a time, returns
  that bit for each of R, L, M, E and D, and 0 for X.
- **X9. A mount that takes no change**
  (`TestAccessGrantsNoChangeOnAMountThatTakesNone`; §4), in two subtests.
  Each begins on a writable mount, where `testCaller` makes `d`, a directory
  `0o777` holding `k`; `f`, a file `0o666` with 20 bytes; and `x`, a file
  `0o777`; and `Sync`s. On it `Access` with every bit gives each of
  `testCaller`, `sa24Other` and `sp24Root` R L M E D on `d`, R M E on `f`
  and R M E X on `x`.
  - *Read-only.* A second mount of the same bucket is made with
    `Config.ReadOnly` set, and the first is not used again. On it, for each
    of the three callers, `Access` with every bit gives R L on `d`, R on `f`
    and R X on `x`, and asked for M, E and D alone gives 0. Each of the
    three callers also makes the calls: its `Create` in `d`, `Remove` of
    `k` and `Write` of `f` return `vfs.ErrROFS`, since the gate refuses
    whoever asks, and its `Lookup` of `k`, `ReadDir` of `d` and `Read` of
    `f` return nil.
  - *Diverged.* As ADR 0025 §6's A9 diverges a mount. Then the same masks,
    and for each of the three callers `Create` in `d`, `Remove` of `k` and
    `Write` of `f` return `vfs.ErrStale`, with its three reads returning
    nil.
- **X10. No check of `Access`'s is outside the policy**
  (`TestNoAccessCheckRemainsOutsideThePolicy`; §7). `sp24Mount` with a
  policy of the test's own that allows everything until it is switched,
  under its own lock, to refuse everything with `vfs.ErrNXIO`. `testCaller`
  makes `y`, a file `0o600`, and `p`, a directory `0o700`. Allowing:
  `sa24Other`'s `Access` with every bit gives R M E X on `y` and R L M E D
  on `p`. Refusing: `sp24Root`'s and `testCaller`'s give 0 on both.

`internal/blobfs/access_wiring_test.go`, with helpers prefixed `aw`, which
names `policy.OpExecute` and `Request.Advisory` and so builds only with
coder's change.

- **X11. The requests, through the FS**
  (`TestAccessAsksThePolicyTheOperationsRequests`; §1, §7). `sp24Mount`
  with an `sp24Recorder` and `pwW2Tree`, whose `Object`s are ADR 0026 §7's
  `D`, `D2`, `F` and `LN`. For each row the recorder is reset to refuse the
  index given, `FS.changes` is read, `testCaller` calls `Access`, the mask
  is the one given with a nil error, the recorder holds exactly the
  requests listed, each as X2 describes them, and `FS.changes` and
  `FS.dirty` are as they were.
  - `Access(d, every bit)`, index −1: R L M E D, with `OpReadDir`,
    `OpLookup`, `OpRename`, `OpAddEntry`, `OpRemoveEntry`, each on `D`.
    Index 0: all but R. Index 2: all but M. Index 4: all but D. The same
    five requests each time.
  - `Access(f, every bit)`, index −1: R M E X, with `OpRead`, `OpWrite`,
    `OpTruncate`, `OpWrite`, `OpExecute`, each on `F`. Index 1: R E X, with
    `OpRead`, `OpWrite`, `OpWrite`, `OpExecute`. Index 2: R E X, with the
    five. Index 3: R M X. Index 4: R M E.
  - `Access(f, R)`, index −1: R, with `OpRead` alone. `Access(f, L|D)`,
    `Access(d, X)` and `Access(f, 0)`: 0, with no request.
  - `Access(d2, D)`, index −1: D, with one `OpRemoveEntry` on `D2` and no
    `Entry`.
  - `Access(ln, every bit)`, index −1: R M E X, with the five requests on
    `LN`.

  Then a read-only mount of the same bucket, with a recorder installed the
  same way: `Access(d, every bit)` gives R L with `OpReadDir` and `OpLookup`
  alone, and `Access(f, every bit)` gives R X with `OpRead` and `OpExecute`
  alone.
- **X12. W5, revised.** In `policy_wiring_test.go`, ADR 0014 §11's W5 loses
  `Access`: the test is renamed
  `TestGetAttrReadLinkFSStatAndCommitConsultNoPolicy`, and its two `Access`
  assertions and its second mount go. X10 and X11 hold what they held,
  turned round. `pwCompareRequests`, which W2's rows use, also requires
  `Advisory` to be false on every request it compares, so that W2 shows
  that no call marks its request (§2). The file's header and W4's doc
  comment, which say that U5 is unpinned, say that it is pinned since this
  ADR and tested in `access_test.go`, and that W4 still has no such row.

**`internal/nfs`** (package `nfs`), in one new file,
`internal/nfs/access_test.go`, with helpers prefixed `acc`, which builds on
`4ded55e`'s tree. It drives the server over a real TCP socket, as
`sticky_test.go` does, and reuses `startServer`, `dial`, `mountRoot`,
`rpcClient`, `nfsCreate`, `skipPostOpAttr`, `nmfsCreate`, `nmfsLookup`,
`nmfsWantEnd`, `nmfsOK`, `nmfsNoEnt`, `nmfsGuarded`, `oaSetMode`,
`stkMkdir`, `stkClient` and `stkAcces`. Of unexported non-test names it
uses the procedure number `procAccess`. `ACCESS`'s arguments are the handle
and the mask asked for, a `uint32`; its reply is the status, a
`post_op_attr` and, for `NFS3_OK` alone, the mask granted, a `uint32`, and
nothing after.

- **X13. On the wire** (`TestNFSAccessReportsWhatTheOperationsAllow`; §1).
  The owner, uid 501, makes in the root the directories `rd`, `0o744`,
  `wo`, `0o722`, `ok`, `0o777`, and `none`, `0o700`, and the file `x.bin`,
  whose mode it sets to `0o755`. Then, from a client sending `502:30`, with
  mask 0x3f unless given:
  - `ACCESS` of `rd` answers `NFS3_OK` and 0x01; of `wo`, 0x00; of `ok`,
    0x1f; of `none`, 0x00; of `x.bin`, 0x21; of `ok` with mask 0x02, 0x02;
    and of `ok` with mask 0x20, 0x00;
  - `LOOKUP` of a name `rd` lacks answers 13, and of one `ok` lacks, 2;
  - a GUARDED `CREATE` in `wo` answers 13, and in `ok`, `NFS3_OK`.

  From the owner, `ACCESS` of `rd` answers 0x1f and of `x.bin` 0x2d. From a
  client sending uid 0, `ACCESS` of `none` answers 0x1f.

**Existing tests** (Assumption 15). Beside the changes X6, X7 and X12 list,
no assertion of an existing test changes. These were read against §1 to §7
and stand: ADR 0014 §11's W4, whose ten `Access` rows name no directory
with read and not search, or write and not search; ADR 0025 §6's A6, A7,
A8 and A9, whose `Access` assertions are of files, of a directory of mode
0, and of a read-only mount; `TestReadOnlyMountWritesNothing`; and the two
tests that call `Access` for its error alone,
`TestRemovedDirectoryHandleIsStale` and ADR 0023 §11's reads while a
commit's store call is held. W2, W3 and every other test that installs a
policy calls no `Access`.

**Red run.** `internal/blobfs/access_test.go` and
`internal/nfs/access_test.go` build on `4ded55e`'s tree, with no other file
of this PR, where, read from the code and not run:

- X8 fails on `pwMember`'s and `sa24Other`'s masks of `d744`, `d722` and
  `d766`, and passes every other row and every operation;
- X9 passes its read-only subtest and fails its diverged one, on the masks;
- X10 fails on each of its masks;
- X13 fails on `rd`'s mask, which is 0x03 there, and on `wo`'s, 0x1c.

The other test files of this PR name `policy.OpExecute`, `Request.Advisory`
or the new `AccessBits`, so their packages do not build there and have no
red run: X1 to X7, X11 and A4's revised calls are checked first against
coder's change. After the change every test passes.

**What test-author's commit settled**, recorded against `e2d1423`, read on
2026-11-23 (Assumptions 24 to 26). Its report raised five points where the
text above left room or was at odds with itself. Each is decided in the
text it bears on, and the committed tests match every one, so no test
changes:

1. *§2's first law, against U3 staying unpinned.* The law gives way: it is
   claimed for a regular file and a directory alone (§2), and U3 stays
   unpinned where `Permits` allows. X4's check of the law, and P13's, pass
   over every other type, as committed.
2. *Who makes X9's calls.* Each of the three callers makes the three
   changes and the three reads, on both mounts, as committed.
3. *What drives X8's operations.* The tables' cells, not the mask `Access`
   returned, as committed. That is what lets the red run fail on six masks
   and pass every operation.
4. *Which `Op`s X2 refuses.* Every `Op` from 1 to 13 of each of the four
   objects, with what is expected taken from an oracle of §2's procedure
   beside the two cases given in so many words, as committed.
5. *X1's old rule for X.* ADR 0014 §5's bullet word for word, its
   `o.Mode & 0o111 != 0` included, as committed.

One thing the commit does beyond the text, which stands: X5 also asks §2's
second law of a regular file, directly, where X3 and ADR 0025 §6's A4 ask
it through `AccessBits`.

**What only review can show:**

- that `internal/policy/policy.go` and `internal/blobfs/fs.go` are the only
  non-test files changed, and in `fs.go` only `Access`, its doc comment and
  `FS.perm`'s;
- that `Access` holds `FS.mu` for reading across the resolve, its one call
  of `mutable()` and `AccessBits`, compares no mode and names no
  `vfs.Access` bit;
- that `AccessBits` builds no request without `Advisory`, and that no code
  but `AccessBits` sets it;
- that `OpRead`, `OpWrite` and `OpTruncate` read `Advisory` and no other
  row does;
- that `AccessBits` and the rows still take no lock, log nothing and
  allocate nothing when they allow;
- that nothing writes `accessDir` or `accessOther`, the two tables coder's
  commit holds §1 in, which are package variables (§2);
- that nothing is exported beyond §2, and that `internal/vfs` and
  `internal/nfs` are unchanged;
- that no function `docs/tla/trace.tsv` names was edited, `mutable()` above
  all (§11), which the architect's pass of 2026-11-23 read and found so,
  and which review still judges;
- how a kernel client behaves, which needs a mount and a human
  (Assumption 18).

### 11. The model, the trace and the vocabulary

No TLA+ module is affected, and none is added (Assumption 16). ADR 0013 §1
gives permission rules to exhaustive tests of the policy and to security
review, and X1 to X5 are those tests. `docs/tla/CommitProtocol.tla` models
no permission and no `ACCESS`: its `Mutate` is guarded by `Mutable`, which
is `mutable()`, and `Access` is no `Mutate`. It reads the guard and changes
nothing.

`docs/tla/trace.tsv` names `FS.mutable`, and names neither `FS.Access` nor
any symbol of `internal/policy`. `mutable()` gains a caller and is not
edited, so no traced function changes, no row changes, and the module's
`Checked against` line stays at `afbda93`. Nothing under `docs/tla/` changes
in this PR, so CLAUDE.md's merge bar does not ask the session for
`tla.sh full`. Both rest on coder's diff touching, in `internal/blobfs`,
`Access` and one doc comment alone. A diff that edits `mutable()` or
another traced function is outside coder's brief, and would bring ADR 0013
§8's rule for a refactor of traced code back.

`docs/vocabulary.md` V7 gains one name, `OpExecute`, as reserved by this
ADR, since it is not in the code until coder's commit; the architect's pass
after that commit marks it as existing (Assumption 22). `AccessBits` keeps
its entry, which gives no signature. `Request`'s fields are not entries
(ADR 0026 §9). `UnixAllows(r)` is still `policy.Unix().Check(r) == nil`,
and `EffectAuthorized`, which is stated through it for a call's request,
reads as it did.

**The pass after coder's commit** was made on 2026-11-23 against `729360f`,
read and not run (Assumption 25). No function `docs/tla/trace.tsv` names
changed:

- The session's `git diff --stat 4ded55e..729360f`, over `internal` less
  its test files, lists two files: `internal/policy/policy.go`, which the
  trace does not name, and `internal/blobfs/fs.go`, with 28 lines changed.
  So `internal/blobfs/commit.go` and the three files of `internal/store`
  that the trace names are untouched, by the session's account.
- In `fs.go` the change is in two places, which are the 28 lines:
  `FS.perm`'s doc comment, five lines for five, and `Access` with its doc
  comment, nine lines for nine. Neither is a traced function.
- The thirteen functions of `fs.go` that the trace names, `New`,
  `FS.mutable`, `FS.markDirty`, `FS.SetAttr`, `FS.createEntry`,
  `FS.unlink`, `FS.Rename`, `FS.bufferWrite`, `FS.truncate`, `FS.flushAll`,
  `FS.settleAll`, `FS.flushOpen` and `FS.putChunk`, were each read whole at
  `729360f` beside the file as it stood before the change, and each is the
  same line for line, its `TLA+:` line and its doc comment included.
  `mutable()` gained a caller, `Access`, and no line.

So no row of `trace.tsv` changes, `locks` and `decided` included. The
module's `Checked against` line, `docs/tla/README.md`'s statement of it and
`docs/vocabulary.md` V3's stay at `afbda93`. Nothing under `docs/tla/`
changes in this PR, and CLAUDE.md's merge bar asks no `tla.sh full` of it.
`Access` now reads the mode through `mutable()`, in its hold of `FS.mu` for
reading; it fires nothing and changes nothing, so it is still no `Mutate`.

`OpExecute` is in the code as of `729360f`: `internal/policy/policy.go`
declares it after `OpRename`, in the same `iota` block, so its value is 13.
`docs/vocabulary.md` V7 now marks it as existing, as of that commit, where
it had it reserved. Nothing else in the vocabulary changes.

### 12. State, the link and exemptions

- **No machine, and no transition.** Nothing here adds state. The mount
  mode keeps its table, its one edge and its writers. `Access` reads it
  through `mutable()`, where it read it directly.
- **No link.** The mode is security-relevant, as ADR 0018 §5 has it, and
  its one transition still only narrows and is still caused by a store's
  answer. `ACCESS` causes none, so ADR 0018 §5 asks nothing of it.
- **The policy rule is kept, with one exemption fewer.** `ACCESS`'s whole
  answer is `AccessBits`', in `internal/policy`. `blobfs` adds no condition
  on who the caller is or on what the mount is.
- **The register.** `docs/adr/exemptions.md` marks E12 "revoked by ADR
  0027". E11's row stands as it is. No row is added.

### 13. What stays out of this PR

Each of these is its own PR. Titles are as ADR 0014's *Context* and ADR 0025
give them (Assumption 21).

- **#69**, "SETATTR with a size, and WRITE, accept a symlink's handle". No
  row's answer to a call changes. §6 says what its ADR inherits.
- **#98**, "CREATE, MKDIR and SYMLINK apply a requested group from any
  caller", and **#100**, "Any caller with write permission can set a file's
  times to any value". `ACCESS` has no bit for either, and asks neither
  row.
- **#101**, "Write and truncate check permission before a wait, not where
  they apply the change". `ACCESS` waits for nothing, and reads everything
  in one hold.
- **#99**, "A guarded SETATTR checks its guard outside the filesystem's
  lock". `internal/nfs` is untouched. Its `access` handler has a gap of the
  same kind, which this ADR finds and does not close (*What this does not
  decide*).
- **#155**, and **#154**, whose title and text this ADR was not given.
  Nothing here touches `Write`, `truncate` or `createEntry`.

### 14. Cross-references, and `CHANGES`

This ADR replaces parts of three ADRs, under `docs/adr/README.md`'s rule
for a part a later ADR replaces. Each keeps its status and its text, and
gains a `Revised` line, cross-references only, and one sentence in each
section touched.

- **ADR 0014**: §5's `AccessBits`, with the `Op` list, which gains
  `OpExecute`, and `Request`, which gains `Advisory`; §6's bullet for
  `Access`, and what *The wiring at `a6cff50`* says of it and of
  `FS.perm`'s doc comment; §7's bullet for `Access` among the mode's
  readers; and §11's P6, P9, U5 and W5.
- **ADR 0025**: §3's mechanism, that `AccessBits` is built on `Permits`
  alone and that `Access` asks `f.perm` nothing, and §6's A4, whose calls
  change form. §3's decision stands, and its list of what #97 reconciles is
  answered by §3 to §6 here. §1's rows gain the qualification of §2.
- **ADR 0026**: §5's condition for `vfs.AccessDelete` on a directory, which
  is now write and search. §5's decision stands, and §5 here extends it to
  `vfs.AccessModify`.

ADR 0018 is not revised: the README has the register carry a revocation,
"and revises no ADR for it". ADR 0011 §4's bullet on `Access` is still
true, word for word. `docs/DESIGN.md`'s table of ADRs gains this ADR's row,
and `docs/adr/README.md`'s index does.

One `CHANGES` line, proposed for the session to record, without `BREAKING:`
(Assumption 19). The session decided on 2026-11-23 that it takes none
(*Context*, decision 4), and at `729360f` had not yet recorded the line. The
pass of that day reworded its last sentence in two places, against what was
committed, and changed nothing before it. "A file's answer is unchanged"
said too much: on a diverged mount a file loses MODIFY and EXTEND, as the
line itself says just before. And the sticky clause named DELETE alone,
where §5 and the owner's decision have MODIFY beside it. The line as it now
stands:

> ACCESS grants only what the operations then allow: on a directory it
> grants LOOKUP only with search permission, and MODIFY, EXTEND and DELETE
> only with both write and search permission, where read alone and write
> alone used to be enough and the LOOKUP, CREATE, REMOVE or RENAME that
> followed was refused with NFS3ERR_ACCES; and on a mount that has diverged
> it grants none of MODIFY, EXTEND and DELETE, as on a read-only mount, so a
> client that asks before it opens now refuses to open a file there for
> writing, where before the open succeeded and the first WRITE failed with
> NFS3ERR_STALE. Nothing else changes: on a file ACCESS still reports only
> what the mode bits give, and in a sticky directory it still grants MODIFY
> and DELETE by the directory's mode, whoever owns the entries.

## Put to the owner, and decided

This section was headed *For the owner* until the pass of 2026-11-23. It
listed five judgement calls, each decided above as the architect's
recommendation: the ones with a trade-off a client can see, for the session
to put to the owner. None is open now. By the session's report the owner
decided the first four on 2026-11-23, each as recommended, and the session
decided the fifth (*Context*, decision 4). Each item is kept as it was put,
with the alternatives that were weighed, and ends with who decided it. A
different answer to any of them would have changed a table here and its
pins, not the mechanism.

1. **A diverged mount grants no MODIFY, EXTEND or DELETE** (§4). The issue's
   done-when asks for it. What a user sees changes: an open for writing is
   refused by the client, with `EACCES` as far as the clients were read
   (Assumption 13), where before it succeeded and the first write failed
   with `ESTALE`; and a program that opens read-write and only reads now
   fails at the open.
   *Recommended:* mask, since failing at the open loses no buffered write.
   *Alternatives:* leave the bits granted, which leaves the issue's third
   mismatch; or fail `ACCESS` itself with `NFS3ERR_STALE` on a diverged
   mount, which is louder and also stops the reads a diverged mount still
   serves.
   *Decided* by the owner: mask.
2. **In a sticky directory, DELETE and MODIFY are granted by the
   directory's mode** (§5), to a caller who may then remove or rename only
   its own entries. *Recommended:* grant, as Linux's server does.
   *Alternative:* withhold both from a caller who is neither root nor the
   directory's owner. As read, that stops a Linux client from creating in
   such a directory where it checks, since it needs all three bits for
   write, and a macOS client from removing its own file there, since it
   asks MODIFY first.
   *Decided* by the owner: grant both by the directory's mode.
3. **`ACCESS` still reports neither the owner override nor the read that
   execute gives** (§3), so #97's done-when is met one way: what `ACCESS`
   grants, the operations allow. ADR 0025 decided both, and this ADR keeps
   them. *Alternative:* report them, which makes the two equal on a file.
   A client would then let an owner open its read-only file for writing,
   against RFC 1813 §4.4's own sentence, and let anyone who may run a
   program open it for reading.
   *Decided* by the owner: a regular file's answer stays the mode's
   verdict, and reports neither.
4. **An object that is neither a file nor a directory keeps its bits**
   (§6), a symlink's READ above all, until #69 decides. *Alternative:*
   grant such an object nothing now, which as read stops `readlink(2)` on a
   macOS client.
   *Decided* by the owner: such an object keeps today's bits until #69.
5. **The `CHANGES` line takes no `BREAKING:`** (§14; Assumption 19).
   *Decided* by the session on 2026-11-23, and confirmed by the owner on
   2026-11-24 (*Context*, decision 5): no prefix, since no running
   deployment must act.

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation, and Issue #97**, under the owner's
   2026-10-10 words, as ADRs 0015 to 0026 were. The issue's done-when asks
   that `ACCESS` derive from the operations' rules, on a diverged mount too.
   The rest is mine: the mechanism (§1, §2), what the bits ask
   (Assumption 5), and the five calls *Put to the owner, and decided*
   lists. Those five were mine until 2026-11-23, when by the session's
   report the owner decided the first four, each as this ADR has it, and
   the session the fifth (*Context*, decision 4).
2. **Read, not run, at `4ded55e`.** Every statement about the code was
   checked by reading it there; nothing was run. #97's text and its comment
   are as the session gave them. The titles of #69, #98, #99, #100 and #101
   are as ADR 0014's *Context* gives them, and #155's as ADR 0025 gives it;
   their texts were not given, nor was #154's.
3. **The done-when is read one way** (§3, §8). Read as an equality, it
   contradicts ADR 0025 §3, which the issue predates, and RFC 1813 §4.4. I
   read "derives from the same rules" as: `ACCESS` asks the rows that
   decide the operations, and grants only what they allow. ADR 0025 §3 says
   the same of the direction. By the session's report the owner decided on
   2026-11-23 that a regular file's answer stays the mode's verdict
   (*Context*, decision 4), which is this reading.
4. **`ACCESS` goes through the installed policy, by a function over a
   `Policy`** (§2), rather than a row of its own or a direct use of
   `Permits`. A row of its own would let a policy answer `ACCESS` one way
   and the operation another. `Permits` would leave `ACCESS` outside
   `f.perm`, so that a policy installed later, or a row changed later,
   would bring #97 back unseen. A function over a `Policy` keeps the
   mapping in one place, in `internal/policy`, where it is tested without
   an `FS`.
5. **What each bit asks** (§1). The RFC's definitions are a line each, so
   the mapping is a reading: MODIFY on a file as `OpWrite` and `OpTruncate`
   and EXTEND as `OpWrite`, after Linux; MODIFY on a directory as a rename
   within it, by one `OpRename`; DELETE as `OpRemoveEntry` with no entry;
   and no request for a bit the RFC gives no meaning for that kind of
   object. Under `Unix()` the choice among these changes no answer: the two
   file rows are alike, and the three directory rows ask the same of the
   directory. It matters only to a policy that tells them apart.
6. **`Advisory`, a bool, on every request `ACCESS` makes** (§2). ADR 0025
   §3 left two ways to a file's bits; this is the second, as a mark on the
   operation's own request, so that the request keeps the operation's `Op`
   and any policy that refuses the operation refuses the bit. It is set on
   the directory requests too, which no row reads, so that a policy or a
   test can tell `ACCESS`'s questions from a call's. The name is the RFC's
   word for the answer. It is not named for an open, because an NFSv4
   `OPEN` may ask a different question, which its ADR decides.
7. **`OpExecute`, numbered 13, for every non-directory** (§2). No existing
   `Op` means execution, and `OpRead` cannot: a file of mode `0o644` may be
   read and not executed. Its row gives every non-directory the mode's
   verdict, as `AccessBits` did, so no bit moves; and a directory
   `vfs.ErrIsDir`, the status the file rows give it. P6's range moves by
   one, and no existing `Op` is renumbered.
8. **`AccessBits` keeps its name and changes its signature** (§2), so that
   an old call does not compile. The order of the bits, that a bit stops
   at its first refusal, that nothing is asked for a change the mount would
   refuse, that any error withholds a bit, and that `OpWrite` may be asked
   twice, are pinned so that X2 and X11 can list the requests. None of them
   changes an answer.
9. **`mutable` is a bool** (§2, §4), not the mode, which `policy` cannot
   name, and not the gate's error, which would work and reads worse in a
   table of tests. Its zero value masks.
10. **The gate is read once, inside the hold** (§4, §7). Outside the hold
    it would be as correct for the mode, which the operations also read
    with no lock. Inside, the answer is of one instant, which costs
    nothing: the read is one atomic load.
11. **A sticky directory's DELETE and MODIFY** (§5) follow ADR 0026 §5,
    whose Assumption 14 gives the reason for DELETE. That MODIFY carries
    the same qualification is new here, and follows from what MODIFY asks.
    By the session's report the owner decided on 2026-11-23 that both are
    granted by the directory's mode (*Context*, decision 4).
12. **An object that is neither keeps its bits, pinned as present
    behaviour** (§6). ADR 0025 §3 has them wait on #69. They are pinned
    exactly, as P9 pinned them, so that #69's change to the rows fails a
    test that names the decision instead of passing unseen. That a grant
    of MODIFY on a symlink is right is not claimed. By the session's report
    the owner decided on 2026-11-23 that such an object keeps today's bits
    until #69 (*Context*, decision 4). What is pinned is the answer to an
    advisory request, which is what `ACCESS` asks; the same request
    unmarked is not, so a change of #69's that moves a call's answer alone
    fails no test here (Assumption 24).
13. **A grant is not recalled, and what a client shows a user** (§4). The
    errno is the client's: `EACCES` is read from Apple's kext, and for
    Linux's client is from recall of `nfs_do_access`, not fetched. How long
    a client keeps a cached answer was not read for either.
14. **E12 revoked, E11 as it is, ADR 0018 unrevised** (§4, §12, §14). ADR
    0018 §4 has an ADR revoke an exemption "by saying so" and move the code
    in the same PR, which this one does. E11's row is not restated, since
    its code and its reason are unchanged.
15. **The test surface** (§10). Four new files, named so that coder and
    review find them, with prefixes no test uses. The two that carry the
    red run use no new name. X3 states the issue's property against
    `Unix()`'s own rows, not an oracle, so that it still holds the day a
    row changes. X8 repeats the property through the FS, with the calls
    themselves, since that is what a client meets. X11's rows refuse by
    index, as W2's do. X13 is on the wire because no test of
    `internal/nfs` decodes an `ACCESS` reply. P9 is removed, not kept
    beside X1, so that one test pins the mask. That the existing tests
    listed stand is from reading each against §1.
16. **No TLA+, and no trace pass** (§11), provided coder edits no traced
    function. `mutable()`'s doc comment needs no word: it still reports
    whether the mount accepts changes, from the mode alone. Coder's commit
    `729360f` edits none, as read on 2026-11-23, so none is due (§11;
    Assumption 25).
17. **Nothing in `internal/nfs` changes**, though its `access` handler
    pairs the mask with attributes read later (*What this does not
    decide*). Closing that needs `Access` to return the attributes, which
    is a change to `vfs.FS` that #97 does not need.
18. **What clients do is read, not observed** (*Context*; §5, §6). Linux's
    client is read from `fs/nfs/dir.c` at `v6.6`. Apple's is read from the
    first 1000 lines of `kext/nfs_vnops.c` at `main`, as GitHub renders
    them, and from one function of XNU; that XNU hands `readlink`'s
    authorization to the kext on an NFSv3 mount rests on ADR 0026's reading
    of `vnode_authorize_opaque`. Nothing was run against either client. If
    the macOS reading is wrong, §5's third reason and §6's second point are
    weaker, and no decision here turns on them alone.
19. **Not breaking, and one `CHANGES` line** (§14). No flag, bucket or
    mount has to change. Every answer that changes takes away a bit whose
    operation the server already refused. The diverged mount is the one
    place a program that ran now stops, at an open for reading and writing
    that only read; a diverged mount already needs a remount, and a
    read-only mount has always answered so. If the owner reads that as an
    action, the same line takes the prefix. The session decided on
    2026-11-23 that the line takes no `BREAKING:`, since no running
    deployment must act (*Context*, decision 4). The reasoning above is
    kept as the reasoning. The decision was the session's, and by its
    report of that day not the owner's, who might still have read it the
    other way. By the session's report the owner confirmed it on 2026-11-24
    (*Context*, decision 5): the line takes no `BREAKING:`. The line's last
    sentence was reworded in the pass of 2026-11-23 (§14), which changes
    nothing in this judgement.
20. **`internal/vfs` is untouched** (§2). `vfs.FS.Access` has no doc
    comment, and gains none: the rules are `internal/policy`'s, which the
    backend that replaces `blobfs` reuses (ADR 0025, Assumption 17).
21. **Scope** (§13). None of the issues left out blocks this PR, and this
    PR makes none of them worse.
22. **A second pass of the architect's, after coder's commit** (§11), to
    mark `OpExecute` as existing in the vocabulary, to record what coder
    chose where the pins leave room, and to confirm from the session's
    diff that no traced function changed. It was made on 2026-11-23,
    against `729360f` (§2, §11; Assumption 25). By the session's brief it
    also recorded the owner's and the session's decisions (*Context*,
    decision 4), settled the five points test-author's report raised (§10;
    Assumptions 24 and 26), and looked again at the `CHANGES` line (§14).
23. **The costs are not measured.** One `ACCESS` asks at most five requests
    where it asked none, each a call through an interface with no
    allocation.
24. **The first law is claimed for a regular file and a directory alone**
    (§2, §6, §10). test-author's report found the law, claimed for every
    request, at odds with U3 staying unpinned: beside X6's row that allows
    an advisory `OpRead` of a symlink, it pinned the same request unmarked.
    One of the two had to give way, and I chose the law. S2 is claimed for
    a regular file and a directory, and the law is there to carry S2;
    nothing else here rests on it. The other choice would have this ADR
    decide what a `Read`, a `Write` or a size change of a symlink answers
    where its mode allows, which §13 leaves to #69 and ADR 0014 §11 has
    kept unpinned since it wrote U3. The cost is that #69's ADR may refuse
    such a call and leave `ACCESS` granting the bit, and no test here fails
    for it: X1 fails only if the advisory answer moves. I accept that,
    since a symlink's READ already speaks for a `READLINK`, which asks no
    policy, and not for a `Read` (§6). The rows are unchanged, and so is
    every test: the committed X4 and P13 already pass over such an object,
    which test-author's report says its brief asked for.
25. **The pass after the two commits was read, not run, and how far it
    went** (§2, §10, §11). Nothing was run by me, and I cannot run git. At
    `729360f` I read `internal/policy/policy.go` whole. In
    `internal/blobfs/fs.go` I read lines 1 to 1081 whole, which hold
    `FS.perm`, `New`, `mutable`, `SetAttr`, `Access`, `markDirty`,
    `createEntry`, `unlink` and `Rename`, and then `putChunk`,
    `bufferWrite`, `truncate`, `flushOpen`, `flushAll` and `settleAll`
    whole, each with its doc comment. I compared all of that, line by line,
    with a copy of each file from before coder's change. The copy is not a
    checkout of `4ded55e`: it is the working tree that ADR 0026's coder
    dispatch left on the session's machine, which holds that ADR's change
    and not this one. That it is `4ded55e`'s text is not proven, and rests
    on the counts agreeing with the session's `git diff --stat`. `fs.go` is
    2138 lines in both, and the two places where the copies differ are 14
    lines for 14, the 28 the stat gives the file. `policy.go` is 354 lines
    in the copy and 433 at `729360f`, the net of the 117 insertions and 38
    deletions that the stat's totals, 131 and 52, leave it. Over the rest
    of `fs.go` I compared every function declaration, and every line that
    takes or releases a lock or the commit gate, calls `mutable()`,
    `markDirty()`, `checkPerm` or `f.perm.Check`, reads `f.mode`, or
    carries a `TLA+:` marker: each is at the same line with the same text
    in both, outside `Access`. That `internal/blobfs/commit.go` and
    `internal/store` did not change is the session's stat, not a reading
    of mine. A `git diff 4ded55e..729360f` of the files `trace.tsv` names
    is the session's to run if it wants more than this. Of the tests I read
    `internal/policy/access_test.go`, `internal/blobfs/access_test.go` and
    `internal/blobfs/access_wiring_test.go` whole, and of
    `internal/policy/policy_test.go` the header, the fixtures,
    `TestConstantsArePinned`, the file rows and X6's rows of `unixCases()`,
    `p5Cases()`, P6, P10, P12 and P13. `owner_access_test.go`,
    `policy_wiring_test.go` and `internal/nfs/access_test.go` I searched
    and did not read whole. That the suite passes is the session's report
    of its merge bar on `729360f`.
26. **The four smaller points of the tests** (§10). Each was open in the
    text, and each is settled as the committed test has it. X9's calls are
    made by each of the three callers, because the gate judges the mount
    and not the caller, which one caller would not show, and because `d`'s
    and `f`'s modes let all three make the reads. X8's operations are
    driven by the tables, because driven by the mask `Access` returned a
    wrong mask would fail twice, and the red run's note could not hold.
    X2 refuses every `Op`, the ones a table never asks included, which
    costs nothing and shows that `AccessBits` asks nothing else. What X2
    expects comes from an oracle the test writes from §2's procedure, which
    could share a misreading with the code; the cases given in so many
    words anchor it. X2 and X11 between them list every request of both
    tables, and a refusal at four of a file's five places and at three of
    a directory's five. And X1's second oracle keeps `o.Mode & 0o111 != 0`,
    because that is ADR 0014 §5's text, and the grants are the same either
    way.
27. **The two tables are data, not state** (§2). `accessDir` and
    `accessOther` are package variables only because Go has no constant
    array. ADR 0018 §2 has the state rule reach state that has named
    states; these have none, and nothing writes them, so I read them as
    outside it, with no exemption to grant. A machine's table is a
    package-level variable too (ADR 0014 §3). They are unexported, so no
    importer can reassign them, which is the worry ADR 0014 §5 has about a
    package-level authority. A switch inside `AccessBits` would have kept
    them out of package scope; coder's form is within the pins and reads as
    §1's tables do, and I leave it. A later change that wrote either would
    make it state, and would need its own decision.

## Alternatives considered

- **Mending `AccessBits` in place, and masking on `mutable()` in
  `Access`.** Three lines, and it removes the issue's three mismatches. It
  leaves `ACCESS` outside `f.perm` and beside the rows, so the two agree
  only while someone keeps them in step, and it keeps E12
  (Assumption 4).
- **A row of `ACCESS`'s own**, one `OpAccess` with the bit in the request,
  or six `Op`s. Each policy would then hold the mapping, and could answer
  `ACCESS` and the operation differently.
- **`Permits` for a file's bits**, the first way ADR 0025 §3 left. A file's
  bits would then bypass the installed policy while a directory's did not.
- **A request for the mode's verdict, as an `Op` with a permission.** It is
  the second way taken literally, but its `Op` is not the operation's, so a
  policy that refuses writes would have to know to refuse it too
  (Assumption 6).
- **New `Op`s for an open, in place of the mark.** Three `Op`s for one
  bool, and an NFSv4 `OPEN` may not ask what `ACCESS` asks.
- **Reporting the owner override, or READ for execute.** §3.
- **Withholding DELETE and MODIFY in a sticky directory.** §5.
- **Leaving a diverged mount unmasked, or failing its `ACCESS`.** *Put to
  the owner, and decided*, 1.
- **Keeping §2's first law for every request**, and letting it pin U3 where
  `Permits` allows. This ADR would then decide what a call on a symlink
  answers, which is #69's (Assumption 24).
- **Dropping the mount gate for an object that is neither**, as Linux does
  for device files. This server makes none, and ADR 0011 §4 pins the
  read-only answer (§6).
- **The mount's state as a field of `Request`, or the gate as a policy in
  `All`.** A field would sit unset on every call's request, where the gate
  has already answered at stage A. A policy would move the gate to stage D
  for the operations and reorder their errors (ADR 0007 §4).
- **The mapping in `blobfs`.** It would be an access mask outside
  `internal/policy`, tested only through an `FS`, and E12 would stay.
- **MODIFY on a file as `OpWrite` alone.** A policy that refused a size
  change would then see MODIFY granted and a truncation refused.
- **Asking every request and masking afterwards.** The same answer, with
  requests made for bits nobody asked about and for changes the mount has
  already refused.
- **A TLA+ model.** The rule is a function of one request and one bool,
  which X1 enumerates.
- **Closing the `access` handler's gap here.** It needs a change to
  `vfs.FS` (Assumption 17).

## Consequences

- `ACCESS` and the operations are decided by the same rows. A row changed
  later, or a policy installed later, moves `ACCESS` with the operation it
  backs, and X3 fails if a grant loses its backing.
- A client that checks first is told what the server would answer: no
  lookup in a directory it cannot search, no change in one it cannot both
  write and search, and no change on a diverged mount.
- On a diverged mount an open for writing fails at the client, where it
  used to succeed and fail at the first write.
- `ACCESS` and the operations still differ by design on a regular file, in
  ADR 0025 §3's two ways, and DELETE and MODIFY on a sticky directory speak
  for the directory. Both are stated, pinned, and no longer #97's.
- The two protections ADR 0025 §4 rests on a client's check now rest on
  the `Advisory` mark.
- One exemption fewer: the mount's mask is in `internal/policy`.
- `Access` asks the policy up to five requests a call.
- `Request` has a ninth field, and the `Op` list a thirteenth name.
- The backend that replaces `blobfs` gets `ACCESS` from `AccessBits`, its
  policy and its own gate, with nothing to rewrite.
- U5 is pinned, and with it every bit of `ACCESS` for every type.
- Hard to change later: what each bit asks, once clients' behaviour rests
  on it; and `Advisory`'s meaning, once policies read it.

## What this does not decide

- #69, #98, #99, #100, #101, #154 and #155 (§13), and the rows U3 and U6.
- What `ACCESS` reports for a symlink once #69 has decided what a `Read` or
  a `Write` of one answers, and whether `READLINK` should ask a policy
  (§6). ADR 0029 decides both: the bits stay, and `READLINK` asks none.
- Whether `ACCESS`'s reply should carry attributes taken in the hold that
  made the mask. The `access` handler takes them from a later `GetAttr`,
  so a change of mode or owner between the two gives a client a mask
  computed from the old attributes beside the new ones, which it may cache
  until they change again. The operations still enforce.
- Whether a `CREATE` of a name that exists should be judged as the open it
  stands for (ADR 0025, *What this does not decide*). An advisory `OpRead`
  or `OpWrite` is now the question such a check would ask.
- What an NFSv4 `OPEN` or `ACCESS` asks.
- Root squash and the AUTH_SYS trust boundary (#6, #90, #95).
- What `vfs.FS`'s doc comments say of `Access` (Assumption 20).
- A test against a kernel client (Assumption 18).

## Sources

Fetched on 2026-11-23, while this ADR was written, through a tool that
summarises what it fetches and quotes at most about 125 characters at once,
so a longer passage came as consecutive pieces, which are joined here with
a space; it reads a long page 100,000 characters at a time. Seven fetches
gave nothing usable: `raw.githubusercontent.com` answered
`429 Too Many Requests` three times; `elixir.bootlin.com` gave its
navigation and no source, twice; `kernel.googlesource.com` gave
`fs/nfs/dir.c` cut short of the part wanted; and `sourcegraph.com` answered
`404` for Apple's NFS repository. One fetch answered with a redirect to
`raw.githubusercontent.com`, which was followed and met the same 429. No
fetched page held anything addressed to its reader as an instruction.

- **RFC 1813**, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>, 229,793 characters. Fetched
  three times: characters 0 to 100,000 twice, for §3.3.4, and 170,000 to
  the end once, for §4.4; 100,000 to 170,000 were not read.
  - §3.3.4: the six constants, from `const ACCESS3_READ = 0x0001;` to
    `const ACCESS3_EXECUTE = 0x0020;`; the six definitions, the sentence on
    the mask returned and the two sentences on "advisory", all as *Context*
    quotes them; of IMPLEMENTATION, the three sentences on what `ACCESS` is
    for, which the tool gave as three items and did not say were adjacent,
    the three on the answer not being permanent, the four on
    `ACCESS3_DELETE`, and its first sentence: "In general, it is not
    sufficient for the client to attempt to deduce access permissions by
    inspecting the uid, gid, and mode fields in the file attributes, since
    the server may perform uid or gid mapping or enforce additional access
    control restrictions." Its ERRORS, as the tool listed them:
    `NFS3ERR_IO`, `NFS3ERR_STALE`, `NFS3ERR_BADHANDLE`,
    `NFS3ERR_SERVERFAULT`. (*Context*; §1, §3, §4, §5)
  - §4.4: the paragraph on the owner, from "On a local file system" to
    "the ACCESS procedure, however."; the paragraph on paging in an
    executable, its six sentences, which the tool said does not mention the
    ACCESS procedure; the sentence on "UNIX client semantics"; and "Most
    operating systems check permission at open time, and then check that
    the file is open on each read and write request." ADR 0014's and ADR
    0025's *Sources* quote the same section from their own fetches.
    (*Context*; §3)
- **Linux `fs/nfsd/vfs.c`**, the server, at the tag `v6.6`. From GitHub's
  rendering, <https://github.com/torvalds/linux/blob/v6.6/fs/nfsd/vfs.c>,
  which gave the first 1000 of the file's 2485 lines: the arrays
  `nfs3_regaccess`, `nfs3_diraccess` and `nfs3_anyaccess`, entry by entry,
  with the comment in the third; and `nfsd_access`'s choice of `map` by
  `d_is_reg` and `d_is_dir` and its loop, with
  `err2 = nfsd_permission(rqstp, export, dentry, map->how);` and the
  `switch` whose cases are `nfs_ok`, then `nfserr_rofs`, `nfserr_acces` and
  `nfserr_perm` under the comment *Context* quotes, then `default`. From
  <https://kernel.googlesource.com/pub/scm/linux/kernel/git/torvalds/linux/+/refs/tags/v6.6/fs/nfsd/vfs.c>,
  characters 60,000 on, which hold `nfsd_permission`:
  `if (!(acc & NFSD_MAY_LOCAL_ACCESS))`,
  `if (acc & (NFSD_MAY_WRITE | NFSD_MAY_SATTR | NFSD_MAY_TRUNC)) {`,
  `if (exp_rdonly(rqstp, exp) || __mnt_is_readonly(exp->ex_path.mnt))` and
  `return nfserr_rofs;`. That NFSv3's `ACCESS` reaches `nfsd_access` is
  from recall; its caller was not read. (*Context*; §1, §3, §5, §6)
- **Linux `fs/nfsd/vfs.h`**, at `v6.6`:
  <https://github.com/torvalds/linux/blob/v6.6/fs/nfsd/vfs.h>, whole, 183
  lines. `#define NFSD_MAY_CREATE (NFSD_MAY_EXEC|NFSD_MAY_WRITE)`,
  `#define NFSD_MAY_REMOVE (NFSD_MAY_EXEC|NFSD_MAY_WRITE|NFSD_MAY_TRUNC)`,
  and `#define NFSD_MAY_LOCAL_ACCESS 0x080 /* for device special files */`.
  (*Context*; §5, §6)
- **Linux `fs/nfs/dir.c`**, the client, at `v6.6`:
  <https://sourcegraph.com/github.com/torvalds/linux@v6.6/-/raw/fs/nfs/dir.c>,
  characters 76,000 to the end.
  `#define NFS_MAY_WRITE (NFS_ACCESS_MODIFY | NFS_ACCESS_EXTEND | NFS_ACCESS_DELETE)`,
  `#define NFS_FILE_MAY_WRITE (NFS_ACCESS_MODIFY | NFS_ACCESS_EXTEND)` and
  `#define NFS_DIR_MAY_WRITE NFS_MAY_WRITE`; `nfs_access_calc_mask`, whole,
  with
  `if ((access_result & NFS_DIR_MAY_WRITE) == NFS_DIR_MAY_WRITE) mask |= MAY_WRITE;`
  under `S_ISDIR`; `nfs_do_access`'s
  `cache.mask = NFS_ACCESS_READ | NFS_ACCESS_MODIFY | NFS_ACCESS_EXTEND | nfs_access_xattr_mask(NFS_SERVER(inode));`
  with `NFS_ACCESS_DELETE | NFS_ACCESS_LOOKUP` added for a directory and
  `NFS_ACCESS_EXECUTE` otherwise; and `nfs_permission`'s `switch`, with
  `case S_IFLNK: goto out;` and, under `case S_IFDIR:`, the comment
  *Context* quotes over
  `if ((mask & MAY_WRITE) && !(mask & MAY_READ)) goto out;`. Line breaks
  inside a statement are written here as spaces. (*Context*; §5, §6;
  Assumption 18)
- **Apple `NFS`, `kext/nfs_vnops.c`**, at `main`:
  <https://github.com/apple-oss-distributions/NFS/blob/main/kext/nfs_vnops.c>,
  which gave the first 1000 of the file's 9298 lines, without their
  indentation. In `nfs3_access_rpc`, the comment *Context* quotes, in five
  lines, over
  `if ((*access & NFS_ACCESS_DELETE) && !(np->n_access(slot) & NFS_ACCESS_DELETE)) {`
  and `np->n_access(slot) |= NFS_ACCESS_DELETE;`. In `nfs_vnop_access`,
  under `if (vnode_isdir(vp)) {`: `KAUTH_VNODE_LIST_DIRECTORY` gives
  `NFS_ACCESS_READ`; `KAUTH_VNODE_SEARCH`, `NFS_ACCESS_LOOKUP`;
  `KAUTH_VNODE_ADD_FILE | KAUTH_VNODE_ADD_SUBDIRECTORY`,
  `NFS_ACCESS_MODIFY | NFS_ACCESS_EXTEND`; and `KAUTH_VNODE_DELETE_CHILD`,
  `NFS_ACCESS_MODIFY`. Under its `else`: `KAUTH_VNODE_READ_DATA` gives
  `NFS_ACCESS_READ`; `KAUTH_VNODE_WRITE_DATA`,
  `NFS_ACCESS_MODIFY | NFS_ACCESS_EXTEND`; `KAUTH_VNODE_APPEND_DATA`,
  `NFS_ACCESS_EXTEND`; and `KAUTH_VNODE_EXECUTE`, `NFS_ACCESS_EXECUTE`.
  Under `/* common */`: `KAUTH_VNODE_DELETE` gives `NFS_ACCESS_DELETE`.
  Then `/* if caching, always ask for every right */` over
  `waccess = NFS_ACCESS_ALL;`, and
  `if (!error && ((waccess & access) != access)) {` with
  `error = EACCES;`. **Honesty note:** the tool gave these as single lines
  on request, and its `np->n_access(slot)` may be the rendering's form of
  an array index; lines 1001 to 9298 were not seen. (*Context*; §5, §6;
  Assumptions 13 and 18)
- **Apple `xnu`, `bsd/vfs/vfs_syscalls.c`**, at `main`:
  <https://sourcegraph.com/github.com/apple-oss-distributions/xnu@main/-/raw/bsd/vfs/vfs_syscalls.c>,
  characters 150,000 to 250,000 of 384,171. `readlinkat_internal`'s
  `error = mac_vnode_check_readlink(ctx, vp);`,
  `error = vnode_authorize(vp, NULL, KAUTH_VNODE_READ_DATA, ctx);` and
  `error = VNOP_READLINK(vp, auio, ctx);`. (§6; Assumption 18)

Nothing else was fetched. Everything else here is read from this
repository's own code and documents, cited by symbol and section.
