# 0010. A new filesystem is never owned by root by accident

**Status:** Accepted
**Date:** 2026-10-05
**Issue:** #72

## Context

`run`, in `cmd/strata/main.go`, defines `-uid` and `-gid` with `flag.Int`,
each with a default of −1 and the help text "owner uid for a new filesystem
(default: current user)", or "owner gid" for the second (`main.go:57-58`;
every line number here is from `ac470e8`). `flag.Int` parses its value with
`strconv.ParseInt(s, 0, strconv.IntSize)`, and `flag.Parse` refuses a value
that an `int` cannot hold, printing the usage text and exiting with status 2
(ADR 0008, *Sources*). So `run` sees every `int` from `math.MinInt` to
`math.MaxInt`, and nothing else.

Once it has opened the store (`main.go:94`) and passed the branch that
returns for `-check` (`main.go:99-101`), `run` does this (`main.go:103-109`):

```go
uid, gid := currentIDs()
if *uidFlag >= 0 {
	uid = uint32(*uidFlag)
}
if *gidFlag >= 0 {
	gid = uint32(*gidFlag)
}
```

and passes the two as `Config.OwnerUID` and `Config.OwnerGID`
(`main.go:119-120`). So a value of 0 or more keeps its low 32 bits, and any
negative value takes the id that `currentIDs` returns.

**Where the owner goes.** `New` copies the two fields into the `FS`
(`internal/blobfs/fs.go:223-224`), and only `initEmpty` reads them: it gives
them to the root directory, with mode 0755, of the filesystem it creates when
the bucket has no root pointer (`internal/blobfs/commit.go:22-26`, `:80-103`,
the mode at `:86`). For a bucket that already holds a filesystem the flags
have no effect, and nothing logs them. `-check` returns before `New` is
called. A `-read-only` mount of an empty bucket still creates a filesystem,
since `Config.ReadOnly` refuses changes at the VFS layer only (#68), and that
filesystem gets this owner too.

**The wrap.** Where `int` is 64 bits:

| `-uid` | The root directory's owner today |
|---|---|
| 4294967296, 2^32 | 0, root |
| 4294967297, 2^32 + 1 | 1 |
| 4294967797, 2^32 + 501 | 501 |
| `math.MaxInt` | 4294967295 |

`-gid` wraps the same way, so `-gid 4294967296` makes group 0 the root
directory's group. Where `int` is 32 bits, `flag.Parse` refuses every value
above 2^31 − 1, so nothing wraps there.

**4294967295.** That is `(uid_t)-1`, which no file or process can usefully
have. POSIX `chown` reads an owner of `(uid_t)-1`, or a group of `(gid_t)-1`,
as "do not change", so no file can be given either that way. Linux's
`setresuid` reads an argument of −1 as "unchanged", and the kernel's
`uid_valid` holds every id valid except `(uid_t)-1`. And a Linux NFS client
refuses the attributes of any object whose `uid` or `gid` is `(uid_t)-1`:
`decode_fattr3` returns `-EINVAL` (*Sources*). So today `-uid 4294967295`, or
`math.MaxInt` wrapped, makes a root directory whose attributes no Linux
client accepts.

**`currentIDs`.** A negative flag takes its id from this (`main.go:253-261`):

```go
func currentIDs() (uint32, uint32) {
	u, err := user.Current()
	if err != nil {
		return 0, 0
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uint32(uid), uint32(gid)
}
```

The architect who planned #73 found, by reading the code, that a failure of
`user.Current` makes root the owner, and named one way it fails. Reading
`os/user` (*Sources*) confirms it and finds more:

- On Linux without cgo, and in any build with the `osusergo` tag, `os/user`
  uses `lookup_stubs.go`. Its `current()` looks the uid up in `/etc/passwd`.
  If that fails, because there is no entry or the file cannot be read, it
  builds the user from `os.Getuid`, `os.Getgid`, `$USER` and
  `os.UserHomeDir`, and fails with `user: Current requires cgo or $USER,
  $HOME set in environment`, or the part of it that applies, when the
  username or the home directory is empty. This is the case the finding
  named.
- On macOS, and in any build with cgo, unless the build has the `osusergo`
  tag, `current()` is `lookupUnixUid(syscall.Getuid())`, which returns
  `UnknownUserIdError` when `getpwuid_r` finds no entry, with no fallback to
  the environment. So there a uid that the user database does not list makes
  root the owner, whatever `$USER` and `$HOME` hold.
- `Current` caches its first result, and returns a nil `*User` and the error
  when that result failed.
- `currentIDs` also discards the errors of `strconv.Atoi`. On Windows `Uid`
  and `Gid` are SIDs, and on Plan 9 the contents of `/dev/user`, none of
  which parse, so both ids are 0. Where `int` is 32 bits an id of 2^31 or
  more overflows: with cgo, `buildUser` writes it in decimal and `Atoi`
  returns the largest `int32`, 2147483647, with an error that is discarded;
  without cgo, `os.Getuid` returns it as a negative `int`, so `currentUID`
  returns "" and `Current` fails.
- Each flag replaces only its own id, so `-uid 501` with a failed lookup
  still makes group 0 the root directory's group.

Nothing logs the owner: `initEmpty` logs only the bucket (`commit.go:101`).
All of this is read from the code at `ac470e8` and from the sources cited;
none of it was reproduced.

**What root ownership costs.** The root directory's mode is 0755. A new
filesystem whose root directory root owns is one that the operator's own
user, working through the kernel's NFS client, cannot create anything at the
top of until a client acting as root changes the owner
(`internal/blobfs/fs.go:438-461`). A value that wraps to another id gives the
root directory to another local user instead. Permissions are checked
against the identity a client asserts in AUTH_SYS (`fs.go:346-365`), which
this server trusts by design because it listens on loopback only
(`internal/sunrpc/rpc.go:50-53`). So the owner decides what an ordinary user
can do through the mount, not what a process that speaks the protocol itself
can claim.

ADR 0008 lists #72 under *What this does not decide*, noting that its shape
is #61's but that neither flag is a size flag, and ADR 0009 lists it too.
#72 asks that a `-uid` or `-gid` above 2^32 − 1 be refused before the store
is opened, with exit status 1 and a message, as ADR 0008 §5 does for
`-chunk-size`, and that tests in `cmd/strata/main_test.go` cover the
boundaries on 64-bit through a helper pinned in an ADR.

## Decision

### 1. No flag conversion wraps

ADR 0008 §1 holds that no size flag wraps. This ADR holds the same of every
flag whose value `run` converts to another type on its way into
`blobfs.Config`. Each reaches its field through a helper of its own, in
package `main` of `cmd/strata` and pinned in an ADR, which converts to
`int64` before it compares against a bound, and which returns the value
exactly where the field can hold it and the flag means it, returns the value
an ADR gives the case, or refuses it. None wraps. Those helpers are
`maxDirtyBytes` (ADR 0003 §1), `chunkSizeBytes` and `cacheBytes` (ADR 0008
§2, §3), and `ownerID` (§3), which `run` calls once for `-uid` and once for
`-gid`. `-commit-interval` (ADR 0009 §3) and `-snapshot-retention` reach
`Config` with the type they are parsed as. A flag added later that `run`
converts gets a helper under the same rule.

### 2. An owner flag is −1 or an id from 0 to 4294967294

- On the wire an owner is a `uint32` (RFC 1813 §2.5, `uid3` and `gid3`), and
  `blobfs` stores it as one (`internal/blobfs/inode.go:47-48`).
- 4294967295 is refused. It is `(uid_t)-1` and `(gid_t)-1`, which nothing
  can give a file or a process, and which a Linux client refuses in any
  attributes (*Context*).
- −1, the default, means the id strata runs as (§4). It is the only negative
  value with a meaning, and every other negative value is refused.
- 0, root, is accepted. An operator who asks for it gets it.

So `-uid` and `-gid` accept −1 and the ids from 0 to 4294967294, of which an
`int` holds only those up to 2^31 − 1 where it is 32 bits.

### 3. `ownerID` resolves `-uid` and `-gid`

```go
// cmd/strata
//
// ownerID resolves -uid or -gid to Config.OwnerUID or Config.OwnerGID.
func ownerID(flagName string, given int, current uint32) (uint32, error)
```

- `flagName` is the flag's name as an operator types it, `"-uid"` or
  `"-gid"`. `ownerID` puts it in its error text and depends on it in no other
  way.
- `given` is the flag's value.
- `current` is the id strata runs as, as `currentIDs` returns it (§4).
  4294967295 there means that the platform reports none.

The rules:

- For `0 <= given <= 4294967294`, which is `math.MaxUint32 - 1`, it returns
  `uint32(given)` and a nil error. It does not use `current`.
- For `given == -1` it returns `current` and a nil error, unless `current`
  is 4294967295, when it returns 0 and a non-nil error.
- For `given < -1`, or `given > 4294967294`, it returns 0 and a non-nil
  error.
- It converts `given` to `int64` before it compares it with 4294967294, so
  that the bound is a representable constant and `ownerID` behaves as
  specified where `int` is 32 bits (§1).
- The error's text contains `flagName`, and `given` in decimal as
  `strconv.Itoa` writes it. Nothing else about the text is pinned. For
  example:

  ```text
  -uid 4294967296 is out of range: give an id from 0 to 4294967294, or -1 for the id strata runs as
  -gid -1 asks for the gid strata runs as, which this platform does not report: give -gid an id from 0 to 4294967294
  ```

| `given` | `current` | `ownerID` | Before this ADR |
|---|---|---|---|
| `math.MinInt` | 501 | 0, an error | 501, the current user |
| −2 | 501 | 0, an error | 501, the current user |
| −2 | 4294967295 | 0, an error | 4294967295 |
| −1, the default | 501 | 501, nil | the same |
| −1 | 0 | 0, nil: root, because strata runs as root | the same |
| −1 | 4294967294 | 4294967294, nil | the same |
| −1 | 4294967295 | 0, an error: the platform reports no id | 4294967295 |
| 0 | 501 | 0, nil: root, as asked | the same |
| 0 | 4294967295 | 0, nil | the same |
| 1 | 501 | 1, nil | the same |
| 501 | 4294967295 | 501, nil: a given id needs no current one | the same |
| 65534 | 501 | 65534, nil | the same |
| 2147483647, 2^31 − 1 | 501 | 2147483647, nil | the same |
| 4294967294, 2^32 − 2, where `int` is 64 bits | 501 | 4294967294, nil | the same |
| 4294967295, 2^32 − 1, where `int` is 64 bits | 501 | 0, an error | 4294967295, which no Linux client accepts |
| 4294967296, 2^32, where `int` is 64 bits | 501 | 0, an error | 0, root |
| 4294967297, 2^32 + 1, where `int` is 64 bits | 501 | 0, an error | 1 |
| 4294967797, 2^32 + 501, where `int` is 64 bits | 501 | 0, an error | 501, by accident |
| `math.MaxInt`, where `int` is 64 bits | 501 | 0, an error | 4294967295 |

*Before this ADR* is what `run` made of the two values: `uint32(given)` for
a `given` of 0 or more, and `current` otherwise. Where `int` is 32 bits,
`math.MinInt` and `math.MaxInt` are −2^31 and 2^31 − 1, and every row that
applies there reads the same.

### 4. `currentIDs` is the uid and gid strata runs as

```go
// cmd/strata
//
// currentIDs returns the real uid and gid that strata runs as.
func currentIDs() (uid, gid uint32)
```

- It returns `uint32(os.Getuid())` and `uint32(os.Getgid())`, the real uid
  and gid of the process. It consults nothing else: not `os/user`, not the
  user or group database, not the environment.
- On Linux and macOS, `getuid` and `getgid` always succeed (*Sources*), so
  it has no failure to handle.
- `os.Getuid` and `os.Getgid` return an `int`. Where `int` is 64 bits that
  holds every id from 0 to 4294967295. Where it is 32 bits, `syscall`
  converts the kernel's 32-bit id to `int` (`uid = int(r0)` on linux/386),
  so an id of 2^31 or more comes back negative, and the conversion to
  `uint32` restores it. On Windows both return −1 (*Sources*), which the
  conversion makes 4294967295, the value §3 reads as "no id".

| Where strata runs | `currentIDs` before this ADR | After |
|---|---|---|
| Under a uid that the user database lists | the uid, and its entry's primary group | the uid, and the real gid, which is the same unless strata was started under another group |
| Under a uid that it does not list, on macOS or with cgo, without `osusergo` | 0 and 0, root | the real uid and gid |
| Under a uid that it does not list, on Linux without cgo or with `osusergo`, `$USER` and `$HOME` set | the real uid and gid | the same |
| The same, with `$USER` or `$HOME` unset or empty | 0 and 0, root | the real uid and gid |
| Where `int` is 32 bits, under a listed id of 2^31 or more | 2147483647 with cgo, since `Atoi` clamps it; 0, root, without | the id |
| On Windows | 0 and 0, root, since a SID does not parse | 4294967295 for each, which §3 refuses unless both flags are given |

### 5. Where `run` refuses, and what the operator sees

- After the `-bucket` check, `chunkSizeBytes` and `commitInterval`, and
  before `openStore`, `run` calls `currentIDs` once, then
  `ownerID("-uid", *uidFlag, uid)` and `ownerID("-gid", *gidFlag, gid)`. If
  either returns an error, `run` returns that error unchanged, and `main`
  prints `strata: ` and its text on standard error and exits with status 1.
  The usage text is not printed. This is ADR 0008 §5's pattern.
- So a refused `-uid` or `-gid` creates nothing, not even the directory
  `store.NewLocal` makes for a local bucket, and it contacts no endpoint. It
  stops `-check` and `-read-only` alike, and the mount of a bucket that
  already holds a filesystem, whose owner the flags never change.
- `run` sets `Config.OwnerUID` and `Config.OwnerGID` from `ownerID`'s
  results, and converts the flags nowhere else.
- When more than one of `-chunk-size`, `-commit-interval`, `-uid` and
  `-gid` is bad, `run` reports the first in that order. The order is not
  part of the test surface.

### 6. What does not change

- The flags' names, types, defaults and help texts.
- `blobfs.Config.OwnerUID` and `OwnerGID` stay `uint32`, and `New` does with
  them what it does now: `initEmpty` gives them to the root directory of a
  new filesystem, and nothing else reads them. `New` checks neither: by the
  time a value reaches it, it is a `uint32`, and `New` cannot tell 0 asked
  for from 2^32 wrapped.
- Nothing in `internal/vfs` changes.

### 7. The test surface

`ownerID`, with the signature in §3, and `currentIDs`, with the signature in
§4, are part of this ADR's interface, and renaming either changes it. A test
in package `main` of `cmd/strata` calls them directly, and may rely on:

- every row of §3's table, for `flagName` `"-uid"` and `"-gid"` alike;
- that the error is non-nil exactly where §3 refuses, that the `uint32` is
  then 0, and that the error's text contains `flagName` and
  `strconv.Itoa(given)`;
- that where §3's rule for `given` does not use `current`, the result is the
  same whatever `current` is;
- that `ownerID` does nothing but compute its result: no I/O, no logging, no
  state;
- that `currentIDs` returns `uint32(os.Getuid())` and `uint32(os.Getgid())`
  on whatever host runs the test, and does nothing else.

The rows whose `given` is past 2^31 − 1 exist only where `int` is 64 bits. A
test builds them at run time from `int64` values, as the `-max-dirty` test
does, so that it still compiles where `int` is 32 bits. `math.MaxInt` is
refused only where `int` is 64 bits; 2147483647 is accepted everywhere.
`current` is a `uint32`, so every value of it applies everywhere.

What the tests cannot show is left to review: that `run` calls `currentIDs`
and `ownerID` before `openStore` and puts their results into `Config`, the
exit status, and that no usage text is printed (§5); and that `currentIDs`
consults nothing but `os.Getuid` and `os.Getgid` (§4).

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required, except where it says otherwise. Those the repository owner
approved say so (Assumption 17).

1. **This is an ADR of its own, and §1 states the rule for every flag `run`
   converts.** ADR 0008's rule, and the owner's approvals behind it, are
   about size flags. Its §1 promises a helper to a size flag added later, and
   it lists #72 as undecided. The ADR convention keeps an accepted decision's
   text and records a new decision in a new ADR. Extending the rule to every
   flag `run` converts is my judgement: once this ADR is in force every such
   flag follows it, and it costs a flag added later one pinned helper.
2. **4294967295 is refused.** #72 asks only that ids above it be refused.
   Nothing can give a file or a process `(uid_t)-1`, and a Linux client
   refuses attributes that carry it (*Context*), so a root directory that
   has it is one strata should never make. The repository owner approved it
   on 2026-10-05.
3. **−1 is the only negative value accepted.** Only the default is
   documented, as "(default: current user)". Any other negative value has no
   documented meaning, and today it silently becomes the current user, which
   is root when strata runs as root. The cost is that the flags now read
   negative values differently from `-snapshot-retention`, which takes every
   negative value as "keep everything" (`internal/blobfs/fs.go:213-214`), and
   that a command line passing another negative `-uid` or `-gid` must be
   corrected. The repository owner approved it on 2026-10-05.
4. **0 is accepted, without a warning.** Root is a legitimate owner when it
   is asked for, and running strata as root with the default left in place
   asks for it.
5. **A refused value stops every mode, before the store is opened.** #72
   asks for the refusal before the store is opened, when the bucket's state
   is not yet known, so the refusal cannot depend on whether a filesystem
   would be created. ADR 0008 rejected that dependence for `-chunk-size` for
   the same reason: a wrong command line would work until the day it met an
   empty bucket. The repository owner approved it on 2026-10-05, with the
   breaking change it makes (Assumption 16).
6. **A refusal is an error that `run` returns:** status 1, the `strata: `
   prefix, and no usage text, as ADR 0008 Assumption 8 decides for
   `-chunk-size`. It keeps `flag.Int` and the usage text as they are.
7. **One helper serves both flags, with the flag's name as a parameter.**
   The two flags' rules are the same, and two helpers would put one rule in
   two places.
8. **The error's text is pinned only as far as a test needs:** the flag's
   name and the value in decimal. `flag.Int` keeps only the parsed value, so
   `-uid 0x100000000` is reported as `4294967296`.
9. **The default comes from the real uid and gid strata runs as, through
   `os.Getuid` and `os.Getgid`.** The finding about `currentIDs` was not part
   of #72. Including it here, and the mechanism, are the owner's choice: the
   repository owner approved both on 2026-10-05, choosing to bundle the
   finding with this change and to use the real uid and gid. On Linux and
   macOS, `getuid` and `getgid` cannot fail, `user.Current` took its uid
   from the real uid anyway, and `mke2fs` gives a new filesystem's root
   directory the same default (*Sources*). Two consequences follow from the
   choice. The default group is the real gid, where before it was the user
   database's primary group for the uid, which differs only when strata is
   started under another group. And on a platform with no POSIX ids the
   default is refused (Assumption 11).
10. **Real ids, not effective ones.** This follows from Assumption 9's
    choice, which names the real ids. `user.Current` looked up the real uid,
    and strata is not installed set-user-ID, where the two would differ.
11. **4294967295, as `current`, means that the platform reports no id.** It
    is what `os.Getuid`'s −1 becomes on Windows. On Linux and macOS `getuid`
    and `getgid` always succeed, and no process can run as `(uid_t)-1` on
    Linux (*Context*). With a flag left at its default, such a platform is
    refused in every mode, `-check` included, unless `-uid` and `-gid` are
    both given. `README.md` names Linux and macOS as strata's platforms, so
    this applies only where strata is not meant to run.
12. **`uint32(os.Getuid())` is exact.** It is the inverse of `syscall`'s
    conversion of the kernel's 32-bit id to `int` (§4), on either width of
    `int`.
13. **`blobfs` and `vfs` are unchanged.** The fields are `uint32` already, so
    nothing wraps there, and `New` cannot tell an intended id from a wrapped
    one. 4294967295 can also reach the namespace through the protocol
    (*What this does not decide*), so refusing it in `New` alone would not
    keep it out.
14. **The help texts are unchanged,** as ADR 0008 Assumption 11 leaves those
    of the size flags.
15. **`run` judges `-uid` before `-gid`, and both after
    `-commit-interval`.** When more than one flag is bad it reports the first
    only. The order is not part of the test surface.
16. **The change is breaking,** under `CLAUDE.md`'s rule for `CHANGES`. A
    deployment whose command line carries a `-uid` or `-gid` that §3 refuses
    no longer starts, even when the bucket already holds a filesystem, whose
    owner the flags never changed, and even with `-check` or `-read-only`. It
    must correct the flag. The repository owner approved the breaking change
    on 2026-10-05.
17. **Status is Accepted on creation.** This is not a judgement of mine: on
    2026-10-05 the repository owner answered the four questions the session
    put to them, choosing for each the option it recommended, namely to
    bundle the finding about `currentIDs` and use the real uid and gid
    (Assumption 9), to refuse 4294967295 (Assumption 2), to refuse negative
    values other than −1 (Assumption 3), and to approve the breaking change
    with this ADR Accepted (Assumptions 5 and 16).
18. **Read, not run.** Everything said here about today's behaviour is read
    from the code at `ac470e8` and from the sources cited. Nothing was run,
    and the finding about `currentIDs` was checked by reading, not
    reproduced.
19. **Scope is #72 and the finding about `currentIDs`.** Everything under
    *What this does not decide* is left alone on purpose.

## Alternatives considered

- **Widening ADR 0008 in place** (Assumption 1).
- **Keeping the low 32 bits,** as today. 2^32 is root.
- **Clamping a value too large to 4294967294, or replacing it with the
  current user's id.** A new filesystem would keep an owner nobody asked
  for. The second is what 2^32 + 501 already does, by accident, when strata
  runs as uid 501.
- **Refusing only when the bucket is empty** (Assumption 5).
- **Checking in `New`.** By then the id is a `uint32`, and `New` cannot tell
  0 asked for from 2^32 wrapped.
- **`flag.Uint`.** It would refuse a negative value at parse time, since
  `strconv.ParseUint` permits no sign, but −1 is the default and has to stay
  expressible, and the usage text would name the flags' type `uint` (ADR
  0008, *Sources*).
- **A `flag.Value` or `flag.Func`,** so that `flag.Parse` refuses the value.
  It would print the usage text and exit with status 2 (Assumption 6).
- **Accepting 4294967295** (Assumption 2).
- **Keeping every negative value as the current user** (Assumption 3).
- **Two helpers, `ownerUID` and `ownerGID`** (Assumption 7).
- **Refusing to start when `user.Current` fails.** It would refuse mounts
  that work today, though the kernel knows the uid.
- **Requiring `-uid` and `-gid` whenever the lookup fails.** The same.
- **Logging a warning and keeping root.** Root stays the owner.
- **Keeping the user database's primary group where the lookup succeeds,**
  and taking only the uid from `os.Getuid`. It keeps today's group where the
  database lists the uid, but keeps the dependence on the database, cgo and
  the environment for one field (Assumption 9).
- **Effective ids** (Assumption 10).
- **Leaving the choice to `blobfs`,** by passing an unresolved owner so that
  `New` refuses only when it creates a filesystem. It changes `Config` for a
  refusal that would come after `openStore`.

## Consequences

- No `-uid` or `-gid` wraps. A new filesystem's root directory is owned by
  root only when `-uid 0` is given, or when strata runs as root and `-uid` is
  left at its default; the same holds for the group.
- The default owner no longer depends on the user database, on cgo or on
  `$USER` and `$HOME`, and `cmd/strata` no longer uses `os/user`.
- A command line carrying a value §3 refuses must be corrected, in every
  mode, even for a bucket that already holds a filesystem (Assumption 16).
- Where the platform reports no ids, `-uid` and `-gid` must both be given
  (Assumption 11).
- A new filesystem created by a strata started under a group other than the
  user database's primary group for its uid gets that group, not the primary
  one (Assumption 9).
- A flag added later that `run` converts gets a helper pinned in an ADR
  (§1).

## What this does not decide

- **Ids from 2^31 to 2^32 − 2 where `int` is 32 bits.** `flag.Int` refuses
  them at parse time, so they cannot be given there. `flag.Int64` would
  accept them, and the usage text would still name the type `int`, since
  `UnquoteUsage` names an `*int64Value` `int` (*Sources*).
- **Base prefixes.** `flag.Int` parses with base 0, as it does for every
  `int` flag, so `-uid 0501` is uid 321, read as octal (*Sources*).
- **Names.** `-uid alice` is not accepted; the flags are numeric.
- **A word when the flags are ignored.** For a bucket that already holds a
  filesystem, `-uid` and `-gid` change nothing, silently. Telling a flag that
  was given from one left at its default would need `flag.Visit`, as ADR
  0008 notes for `-chunk-size`.
- **Changing an existing filesystem's owner.** No flag does; a client acting
  as root can (`internal/blobfs/fs.go:438-461`).
- **4294967295 through the protocol.** A client can still give a file the
  uid or gid 4294967295: SETATTR does it for a client asserting uid 0, and
  CREATE, MKDIR and SYMLINK apply a requested gid from any caller and a
  requested uid from one asserting uid 0 (`internal/blobfs/fs.go:447-461`,
  `:671-676`). Whether the server should refuse it is a decision of its own.
- **A warning when the owner is root.**
- **macOS's `KAUTH_UID_NONE`,** `~(uid_t)0 - 100`, which is 4294967195. It is
  accepted: it is an ordinary id on Linux (*Sources*).
- **`-read-only` on an empty bucket (#68).** `New` still creates and commits
  a filesystem there, now with the owner this ADR resolves.

## Sources

Fetched on 2026-10-05 through a tool that summarises what it fetches, and
quoted as it returned them when asked for verbatim text: once while this
design was planned and again while this ADR was written. The parts fetched
both times matched; the notes say what was fetched once and where a fetch
differed. The tool keeps a page for 15 minutes, so a second fetch inside
that window may have come from its cache rather than from the site; I could
not tell whether any did.

- Go, `os/user`, `lookup_stubs.go`:
  <https://go.dev/src/os/user/lookup_stubs.go>. The build constraint is
  `//go:build (!cgo && !darwin && !windows && !plan9) || android || (osusergo
  && !windows && !plan9)`. `current()` begins `uid := currentUID()` and `u,
  err := lookupUserId(uid)`, returning `u` if `err == nil`. Otherwise it
  takes `homeDir, _ := os.UserHomeDir()`, builds `&User{Uid: uid, Gid:
  currentGID(), Username: os.Getenv("USER"), Name: "", HomeDir: homeDir}`,
  returns it if `u.Uid != "" && u.Username != "" && u.HomeDir != ""`, and
  otherwise returns `fmt.Errorf("user: Current requires cgo or %s set in
  environment", missing)`, `missing` naming `$USER`, `$HOME` or both.
  `currentUID` is `if id := os.Getuid(); id >= 0 { return strconv.Itoa(id)
  }`, then `return ""`. (*Context*)
- Go, `os/user`, `cgo_lookup_unix.go`:
  <https://go.dev/src/os/user/cgo_lookup_unix.go>. The build constraint is
  `//go:build (cgo || darwin) && !osusergo && unix && !android`. `func
  current() (*User, error) { return lookupUnixUid(syscall.Getuid()) }`.
  `lookupUnixUid` has `if err == syscall.ENOENT || (err == nil && !found) {
  return nil, UnknownUserIdError(uid) }`. `buildUser` sets `Uid:
  strconv.FormatUint(uint64(_C_pw_uid(pwd)), 10)` and `Gid:
  strconv.FormatUint(uint64(_C_pw_gid(pwd)), 10)`. (*Context*; Assumption 9)
- Go, `os/user`, `lookup.go`: <https://go.dev/src/os/user/lookup.go>. "The
  first call will cache the current user information. Subsequent calls will
  return the cached value and will not reflect changes to the current user."
  The body is `cache.Do(func() { cache.u, cache.err = current() })`, then `if
  cache.err != nil { return nil, cache.err }`. The first fetch while this
  design was planned described the body rather than quoting it; a second
  that day, and the fetch while this ADR was written, returned the code.
  (*Context*)
- Go, `os/user`, `lookup_unix.go`: <https://go.dev/src/os/user/lookup_unix.go>.
  The build constraint is `//go:build ((unix && !android) || (js && wasm) ||
  wasip1) && ((!cgo && !darwin) || osusergo)`. `lookupUserId` opens
  `userFile` and returns `os.Open`'s error if that fails; `findUserId` ends
  `return nil, UnknownUserIdError(i)`. **Honesty note:** neither this page
  nor the raw file,
  <https://raw.githubusercontent.com/golang/go/master/src/os/user/lookup_unix.go>,
  fetched once, while this ADR was written, declares `userFile`, so that the
  pure Go code reads `/etc/passwd` rests on the package overview below.
  (*Context*)
- Go, package `os/user`: <https://pkg.go.dev/os/user>. "One is written in
  pure Go and parses /etc/passwd and /etc/group. The other is cgo-based and
  relies on the standard C library (libc) routines such as getpwuid_r,
  getgrnam_r, and getgrouplist." "This can be overridden by using osusergo
  build tag, which enforces the pure Go implementation." Of `Uid`: "On POSIX
  systems, this is a decimal number representing the uid. On Windows, this
  is a security identifier (SID) in a string format. On Plan 9, this is the
  contents of /dev/user." `Gid` reads alike. (*Context*)
- Go, `os.Getuid` and `os.Getgid`: <https://pkg.go.dev/os#Getuid>, and the
  source, <https://go.dev/src/os/proc.go>. "Getuid returns the numeric user
  id of the caller. On Windows, it returns -1." The body is `func Getuid()
  int { return syscall.Getuid() }`, and `Getgid` reads alike. (§4;
  Assumption 11)
- Go, `syscall` on linux/386: <https://go.dev/src/syscall/zsyscall_linux_386.go>.
  `Getuid` is `r0, _ := rawSyscallNoError(SYS_GETUID32, 0, 0, 0)` then `uid =
  int(r0)`, and `Getgid` reads alike with `SYS_GETGID32`. (§4; Assumption 12)
- Go, `strconv`: <https://pkg.go.dev/strconv#ParseInt>. "Atoi is equivalent
  to ParseInt(s, 10, 0), converted to type int." "If the base argument is 0,
  the true base is implied by the string's prefix following the sign (if
  present): 2 for "0b", 8 for "0" or "0o", 16 for "0x", and 10 otherwise."
  "If s is empty or contains invalid digits, err.Err = ErrSyntax and the
  returned value is 0; if the value corresponding to s cannot be represented
  by a signed integer of the given size, err.Err = ErrRange and the returned
  value is the maximum magnitude integer of the appropriate bitSize and
  sign." And "ParseUint is like ParseInt but for unsigned numbers. A sign
  prefix is not permitted." **Honesty note:** the fetch while this ADR was
  written first returned that sentence about `ErrRange` without its last
  clause; a narrower fetch returned it whole, as the first fetch had. The
  source, `go.dev/src/strconv/atoi.go`, answered 404, so this rests on the
  documentation. (*Context*; §4; *What this does not decide*)
- Go, package `flag`, source: <https://go.dev/src/flag/flag.go>.
  `intValue.Set` is `v, err := strconv.ParseInt(s, 0, strconv.IntSize)`,
  then `err = numError(err)` if that fails. `UnquoteUsage` has `case
  *intValue, *int64Value:` and `name = "int"`, fetched once, while this ADR
  was written, and as ADR 0008's *Sources* quotes it. How `flag.Parse`
  reports a value that fails, and its exit status 2, read as ADR 0008's
  *Sources* quotes them. (*Context*; Assumption 6; *What this does not
  decide*)
- Linux, `fs/nfs/nfs3xdr.c`:
  <https://raw.githubusercontent.com/torvalds/linux/master/fs/nfs/nfs3xdr.c>.
  `decode_fattr3` has `fattr->uid = make_kuid(userns, be32_to_cpup(p++));`
  and `if (!uid_valid(fattr->uid)) goto out_uid;`, the same for the gid with
  `make_kgid`, `gid_valid` and `out_gid`, and `out_uid: dprintk("NFS:
  returned invalid uid\n"); return -EINVAL;`, with `out_gid` alike.
  **Honesty note:** while this ADR was written that URL answered `429 Too
  Many Requests` twice, and
  <https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/plain/fs/nfs/nfs3xdr.c>
  answered `403 Forbidden`, so the second reading is from GitHub's rendering
  of the same file, <https://github.com/torvalds/linux/blob/master/fs/nfs/nfs3xdr.c>,
  which matched the first but for indentation. (*Context*; §2; Assumption 2)
- Linux, `include/linux/uidgid.h`:
  <https://raw.githubusercontent.com/torvalds/linux/master/include/linux/uidgid.h>.
  `#define INVALID_UID KUIDT_INIT(-1)`, and `uid_valid` is `return
  __kuid_val(uid) != (uid_t) -1;`. **Honesty note:** for the reasons in the
  note above, the second reading is from
  <https://github.com/torvalds/linux/blob/master/include/linux/uidgid.h>.
  (*Context*)
- POSIX, `chown`, The Open Group Base Specifications Issue 8, IEEE Std
  1003.1-2024: <https://pubs.opengroup.org/onlinepubs/9799919799/functions/chown.html>.
  "If owner or group is specified as (uid_t)-1 or (gid_t)-1, respectively,
  the corresponding ID of the file shall not be changed." (*Context*;
  Assumption 2)
- Linux, `setresuid(2)`: <https://man7.org/linux/man-pages/man2/setresuid.2.html>.
  "If one of the arguments equals -1, the corresponding value is not
  changed." and "EINVAL One or more of the target user or group IDs is not
  valid in this user namespace." (*Context*; Assumption 11)
- Linux, `getuid(2)`: <https://man7.org/linux/man-pages/man2/getuid.2.html>.
  "getuid() returns the real user ID of the calling process." and "These
  functions are always successful and never modify errno." And
  `getgid(2)`, <https://man7.org/linux/man-pages/man2/getgid.2.html>,
  fetched once, while this ADR was written: "getgid() returns the real group
  ID of the calling process." and "These functions are always successful and
  never modify errno." (§4; Assumptions 9 and 11)
- macOS, `getuid(2)` and `getgid(2)`, the BSD manual pages that Xcode ships,
  through a third-party mirror: <https://keith.github.io/xcode-man-pages/getuid.2.html>,
  "The getuid() and geteuid() functions are always successful, and no return
  value is reserved to indicate an error.", and
  <https://keith.github.io/xcode-man-pages/getgid.2.html>, fetched once,
  while this ADR was written, "The getgid() and getegid() functions are
  always successful; no return value is reserved to indicate an error."
  **Honesty note:** both are secondhand, a mirror rather than Apple's own
  publication. (§4; Assumptions 9 and 11)
- e2fsprogs, `mke2fs(8)`: <https://man7.org/linux/man-pages/man8/mke2fs.8.html>.
  Of `root_owner`: "If no UID:GID is specified, use the user and group ID of
  the user running mke2fs." And its source,
  <https://raw.githubusercontent.com/tytso/e2fsprogs/master/misc/mke2fs.c>:
  `root_uid = getuid();` and `root_gid = getgid();` when `root_owner` has no
  argument, and `root_uid = strtoul(arg, &p, 0);` when it has one, a parse
  with no range check, so it is no precedent for §3's bound. (Assumption 9)
- RFC 1813, *NFS Version 3 Protocol Specification*, §2.5:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>. `typedef uint32 uid3;` and
  `typedef uint32 gid3;`. (§2)
- XNU, `bsd/sys/kauth.h`:
  <https://raw.githubusercontent.com/apple-oss-distributions/xnu/main/bsd/sys/kauth.h>.
  `#define KAUTH_UID_NONE  (~(uid_t)0 - 100)       /* not a valid UID */`.
  (*What this does not decide*)

**Honesty note:** the Go specification's section on conversions between
numeric types could not be fetched. <https://go.dev/ref/spec>, while this
design was planned, and <https://tip.golang.org/ref/spec>, while this ADR
was written, came back cut off in the section on composite literals, as ADR
0008 found. Two web searches returned summaries of the rule, sign or zero
extension and then truncation to the result type, in two different wordings,
one of them attributed to an old commit of the specification in a mirror of
the Go repository,
<https://lab.nexedi.cn/kirr/go/-/commit/5e391cff2cecdc16a223679b41e2eeb0e6c8347d>.
Both are secondhand, so the wrap figures in *Context* and §3 rest on
arithmetic from the code, as ADR 0008's do.
