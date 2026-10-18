# 0015. Every mount command carries nosuid

**Status:** Accepted
**Date:** 2026-10-18
**Issue:** #90

## Context

**The owner's decisions.** Two, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So reconciled recommendation 12 stands, as delegated:
   add `nosuid` to every printed and documented mount command and to both
   scripts, and add a README limitation saying any local user can reach the
   server as any uid.
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." This change is not breaking (Assumption 7), so
   nothing here relies on it.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `60da89f`, the commit this ADR's branch was cut from, and
every line number is from that commit (Assumption 10).

**The defect.** No mount command that strata prints, documents or scripts
carries `nosuid`, and any local user can reach the server as any uid. So a
mount made with any of them can give any local user root.

**The sites.**

- `printMountInstructions`, in `cmd/strata/main.go`, prints the command to
  standard error once the listener is bound. Its options are
  `vers=3,tcp,port=P,mountport=P,noresvport,hard`, then `nolocks,locallocks`
  when `runtime.GOOS` is `"darwin"` and `nolock` otherwise, then `ro` under
  `-read-only`, where `P` is the port the listener bound.
- `README.md` lines 9 and 113: the opening example, and the command under
  *Running it*.
- `scripts/demo.sh:22`, a command the script prints before it starts strata.
- `scripts/mount.sh:12`, which builds the `opts` that the command at line 20
  runs with; lines 13 to 17 add the lock options for each platform.
- `docs/nfsv4-assessment.md:467`, an NFSv4.2 command in the assessment's
  section on mounting.

**The chain.** Five links:

1. **`readCred` believes AUTH_SYS** (`internal/sunrpc/rpc.go`). It takes the
   uid, the gid and up to 16 supplementary gids from the credential's body
   and checks none of them, and `callerFrom` (`internal/nfs/nfs3.go`) hands
   them to `blobfs` as the `vfs.Caller`. `Cred`'s doc comment reasons that,
   because the server listens on loopback only, "that is the same trust
   boundary as the local user account". It is not: every local account can
   reach the port (link 4) and assert any uid.
2. **Root may set any mode and owner in `SetAttr`** (`internal/blobfs/fs.go`).
   A caller whose uid is 0 passes the owner check. The mode is stored as
   `*sa.Mode & 0o7777`, which keeps the set-user-ID and set-group-ID bits, and
   the uid as given.
3. **`createEntry` applies root's uid and any caller's gid** (#98). It applies
   a requested uid only when the caller's uid is 0, and a requested gid from
   any caller, unchecked. The new inode takes the requested mode `& 0o7777`
   (`allocInode`), set-id bits included, and the caller's uid and gid where
   none is requested.
4. **Any local process can connect**; #90 cites #6 for this. `-listen`
   defaults to `127.0.0.1:20490`, and `Serve` (`internal/sunrpc/rpc.go`)
   serves every connection it accepts, asking nothing of the peer.
5. **Linux mount(8)'s defaults include `suid`** (*Sources*).

Together these give local root. A local user who speaks NFSv3 to the port,
asserting uid 0, can create a file that root owns with mode 04755 and write a
program into it. Executed from a mount without `nosuid`, that program runs as
root: on Linux by link 5, and on macOS by Assumption 12. Asserting another uid
gives that user's rights the same way, and any caller can give a file any
group (#98).

**What ADR 0014 says of these issues.** It records #98 as "CREATE, MKDIR and
SYMLINK apply a requested group from any caller", and leaves "Root squash and
the `AUTH_SYS` trust boundary (#6, #90, #95)" undecided. This ADR cites #6,
#95 and #98 only as that does (Assumption 11).

**What this constrains.** The command `cmd/strata` prints; the commands
`scripts/demo.sh` and `scripts/mount.sh` print or run; and every mount
command in `README.md` and in the Markdown under `docs/` outside `docs/adr/`.
Nothing in `internal/` changes, and nothing on the wire.

## Decision

### 1. The rule

Every mount command that strata prints, and every one that `README.md`, the
Markdown files under `docs/` other than `docs/adr/`, or `scripts/` gives,
carries `nosuid`. `docs/adr/` is excluded because an ADR records history and
may quote a command as it was.

At `60da89f` the rule reaches the five sites in *Context*. §3 to §5 give the
new text of four of them, and `docs/nfsv4-assessment.md:467` becomes
`sudo mount -t nfs -o vers=4.2,port=20490,nosuid 127.0.0.1:/ /tmp/strata-mnt`.

### 2. `mountOptions`

```go
// cmd/strata
//
// mountOptions returns the options of the mount command strata prints.
func mountOptions(goos, port string, readOnly bool) []string
```

It returns, in order:

- `"vers=3"`, `"tcp"`, `"port="+port`, `"mountport="+port`, `"noresvport"`,
  `"hard"`, `"nosuid"`;
- then `"nolocks"`, `"locallocks"` if `goos == "darwin"`, and `"nolock"` for
  any other `goos`, the empty string included;
- then `"ro"` if `readOnly`.

It returns a new slice on each call, does no I/O and keeps no state.

| `goos` | `port` | `readOnly` | `mountOptions`, its elements joined with `,` |
|---|---|---|---|
| `"darwin"` | `"20490"` | `false` | `vers=3,tcp,port=20490,mountport=20490,noresvport,hard,nosuid,nolocks,locallocks` |
| `"darwin"` | `"20490"` | `true` | `vers=3,tcp,port=20490,mountport=20490,noresvport,hard,nosuid,nolocks,locallocks,ro` |
| `"linux"` | `"20490"` | `false` | `vers=3,tcp,port=20490,mountport=20490,noresvport,hard,nosuid,nolock` |
| `"linux"` | `"12345"` | `true` | `vers=3,tcp,port=12345,mountport=12345,noresvport,hard,nosuid,nolock,ro` |
| `"freebsd"` | `"2049"` | `false` | `vers=3,tcp,port=2049,mountport=2049,noresvport,hard,nosuid,nolock`, the Linux form |
| `""` | `"20490"` | `false` | `vers=3,tcp,port=20490,mountport=20490,noresvport,hard,nosuid,nolock`, the Linux form |

No element contains a comma, so each cell gives its slice exactly. The first
row is
`[]string{"vers=3", "tcp", "port=20490", "mountport=20490", "noresvport", "hard", "nosuid", "nolocks", "locallocks"}`.
Before this ADR, `printMountInstructions` built the same lists without
`"nosuid"`.

### 3. `printMountInstructions`

`printMountInstructions` prints
`strings.Join(mountOptions(runtime.GOOS, port, ro), ",")` in place of the list
it builds now. Everything else it prints is unchanged.

### 4. The scripts

- In `demo.sh`'s printed command, `nosuid` goes after `noresvport`, so line 22
  becomes:

  ```text
    sudo mount -t nfs -o vers=3,tcp,port=$port,mountport=$port,noresvport,nosuid,nolocks,locallocks 127.0.0.1:/ $mountpoint
  ```

- In `mount.sh`, `opts` becomes
  `vers=3,tcp,port=$port,mountport=$port,noresvport,hard,nosuid`, so both
  platform branches carry it.

### 5. The README

- Line 9 becomes:

  ```text
  $ sudo mount -t nfs -o vers=3,tcp,port=20490,mountport=20490,noresvport,nosuid,nolocks 127.0.0.1:/ /tmp/strata-mnt
  ```

- Line 113 becomes:

  ```text
  sudo mount -t nfs -o vers=3,tcp,port=20490,mountport=20490,noresvport,nosuid,nolocks,locallocks 127.0.0.1:/ /tmp/strata-mnt
  ```

- The sentence at line 116 becomes: "`mount` needs root on both macOS and
  Linux; the server itself does not, and binds to loopback only, which keeps
  other machines out but not the other users of this one (see *Honest
  limitations*)."
- *Honest limitations* gains this as its first bullet:

  ```markdown
  - **Any local user can act as any user, root included.** strata believes the
    uid and gid a client puts in its AUTH_SYS credential, as NFSv3 servers do,
    and its loopback port is open to every process on the machine, so any local
    user who speaks the protocol can read, change or delete anything in the
    filesystem, whatever its permissions say. The mount commands above carry
    `nosuid`, so that a set-user-ID or set-group-ID program made that way does
    not run with its owner's or group's rights through the mount; a mount
    without it can give any local user root. `nosuid` does not stop the access
    itself: run strata only where every local user may be trusted with the
    whole filesystem ([ADR 0015](docs/adr/0015-every-mount-command-carries-nosuid.md)).
  ```

The README's prose keeps the literal text `mount -t nfs` out, because T2
checks every line that contains it (§7).

### 6. What `nosuid` does and does not do

- **It does:** on Linux, set-user-ID and set-group-ID bits, and file
  capabilities, are not honoured when a program is executed from the mount;
  on macOS, set-user-identifier and set-group-identifier bits do not take
  effect (*Sources*).
- **It does not** stop any local user from reading, changing or deleting
  anything as any uid.
- **It is the client's option.** NFSv3 gives the server no view of the
  client's mount flags (MNT takes only a `dirpath`), so strata cannot require
  it, and a mount made any other way (the operator's own command, fstab,
  autofs) is exposed unless it carries `nosuid`.

### 7. The test surface

- **T1.** Every row of §2's table, compared as exact slices. In every row
  `"nosuid"` occurs exactly once and `"suid"` not at all, as elements.
  Changing a returned slice does not change the next call's result.
- **T2.** A tripwire in `cmd/strata`, reaching the repository root as
  `../..`, because `go test` runs a package's tests in its source directory
  (*Sources*).
  - (a) For every line of `README.md`, and of every `.md` file under `docs/`,
    at any depth, outside `docs/adr/`, that contains `mount -t nfs`, the line
    has ` -o ` after that text, and the whitespace-delimited token after
    ` -o `, split on `,`, has an element equal to `nosuid`.
  - (b) `README.md` has at least one such line.
  - (c) `scripts/demo.sh` and `scripts/mount.sh` each contain `nosuid`. This
    is a substring check only, because `mount.sh` assembles its options in a
    variable. It shows that the option is present, not that every command
    carries it.

  Docs keep their mount commands in code blocks, each on one line, and keep
  the literal text `mount -t nfs` out of their prose, or (a) will flag them.
- **What only review can show:** that `printMountInstructions` prints
  `mountOptions`' list; that every script branch carries the option; that the
  README text matches §5; and that the printed command mounts on macOS and
  Linux, which needs a human.

## Assumptions

Each is mine unless it says otherwise. Assumptions 1 and 2 record what the
owner's decision 1 delegated, and say so.

1. **Status Accepted on creation**, delegated by the owner's words of
   2026-10-10 (decision 1).
2. **The scope is recommendation 12**, delegated by the same words: the
   printed command, the documented ones, both scripts, and the README
   limitation. Everything under *What this does not decide* is left alone on
   purpose.
3. **Where `nosuid` sits.** After `hard` in the printed list and in
   `mount.sh`; after `noresvport` in the README's commands and `demo.sh`'s,
   which carry no `hard`; and before the lock options everywhere. This is for
   reading order; mount does not care.
4. **`nodev` and `noexec` are not added.** MKNOD is refused, so no client can
   create a device node (README, "No hard links, and MKNOD is refused";
   `internal/nfs/nfs3.go` answers MKNOD with `NFS3ERR_NOTSUPP`, and
   `createEntry` makes only regular files, directories and symlinks).
   `noexec` would stop legitimate programs. `nodev` would be harmless but is
   beyond recommendation 12.
5. **The tripwire's scope and its parsing rule** (§7). The scope is where a
   reader copies a command from, without `docs/adr/`, for §1's reason. The
   rule is plain string handling, not a shell parser: it finds `nosuid` in a
   literal option list on the line that names the mount. That is why the
   scripts get only (c), and why a doc keeps each command on one line.
6. **Every `goos` other than darwin gets the Linux spelling.** That is today's
   behaviour.
7. **Not breaking.** No running deployment needs to act to keep working, and
   remounting with `nosuid` is for safety. So the `CHANGES` entry carries no
   `BREAKING:` prefix, and the owner's decision 2 is not relied on.
8. **The printed text gets no explanation of `nosuid`; the README carries
   one.**
9. **macOS accepting `nosuid`** rests on Apple's man pages, on `mount_nfs.c`,
   and on the `mntopts.h` and `getmntopts.c` in Apple's `libutil`
   (*Sources*). The brief this ADR was written from had what `MOPT_STDOPTS`
   expands to only secondhand, the file having answered 404; this ADR reads
   Apple's copy at another address. That `mount_nfs` is built against that
   copy is my inference: `mount_nfs.c` includes `<mntopts.h>` and does not
   define `MOPT_STDOPTS`. A real mount is what confirms it.
10. **Read, not run.** Every statement about the code was checked by reading
    it at `60da89f`. Nothing was run: not the chain in *Context*, and not a
    mount on either platform.
11. **The issues are cited as ADR 0014 cites them**, and their texts were not
    read. ADR 0014 names #6, #90 and #95 together, as "Root squash and the
    `AUTH_SYS` trust boundary". This ADR's narrower uses of them are as the
    session gave them: root squash as #95's subject, authenticating the peer
    as #6's, and #90 citing #6 for the claim that any local process can
    connect.
12. **A macOS mount without `nosuid` lets set-id bits take effect.** Apple's
    mount(8) states no default for them. This is my reading of its `nosuid`
    text, and of `mount_nfs.c`, which zeroes its options before calling
    `getmntopts` and sets `MNT_NOSUID` nowhere else (*Sources*). For macOS,
    *Context*'s "local root" and the README's "a mount without it can give
    any local user root" rest on it; for Linux, they rest on mount(8)'s
    defaults. A real mount is what confirms it, as for Assumption 9.

## Alternatives considered

- **Root squash (#95).** It does not help: a client claiming any other uid X
  can still create a set-user-ID file owned by X, and #98 lets any caller pick
  any group.
- **Refusing set-id bits in the server.** Stripping 06000 from every stored
  mode is enforcement whatever the mount, but it changes the filesystem for
  every user (setgid directories, legitimate set-id programs) to cover a hole
  that #6 owns.
- **Printing a warning with the command.** The README carries it
  (Assumption 8).
- **The printed command only.** Recommendation 12 covers the docs and scripts
  too.
- **A server-side check of the client's options.** Impossible: MNT carries
  only the path (§6).
- **`nodev` and `noexec` as well** (Assumption 4).

## Consequences

- A mount made with any command strata prints, documents or scripts does not
  run a set-id program made through the protocol with its owner's or group's
  rights (§6; for macOS, Assumptions 9 and 12). That closes the route from
  any local user to root through such a mount.
- The access itself is unchanged: any local user can still read, change or
  delete anything in the filesystem as any uid (§6). The README now says so,
  first among its limitations.
- A set-id program kept in strata does not run with its rights through such
  a mount, nor, on Linux, with its file capabilities.
- A mount made before this change, or made any other way, stays exposed
  until it carries `nosuid`, and strata cannot tell which kind it is
  serving (§6). The `CHANGES` entry tells the operator to remount.
- Every mount command added later to `README.md`, or to the docs outside
  `docs/adr/`, must carry `nosuid` on the line that names `mount -t nfs`, or
  T2 fails.
- Hard to change later: T1 pins `mountOptions`' order, so changing or adding
  an option changes §2's table and T1 together.

## What this does not decide

- **#6, authenticating the peer.** One known route is Linux nfsd's default
  `secure` export, which requires "that requests not using gss originate on
  an Internet port less than IPPORT_RESERVED (1024)". The Linux client uses a
  privileged source port unless told `noresvport`, which every NFSv3 command
  strata prints, documents or scripts passes. Whether strata should require a
  reserved port is #6's question.
- **#95**, root squash.
- **#98**, a requested group from any caller.
- **Refusing set-id bits** in the server (*Alternatives considered*).
- **A `-listen` address off loopback**, which would put the same server in
  reach of other machines.

## Sources

Fetched on 2026-10-18, while this ADR was written, through a tool that
summarises what it fetches, and quoted as it returned them when asked for
verbatim text. The brief this ADR was written from cites the same pages from
fetches earlier that day, during planning. Each page quoted here was fetched
again, and matched the brief's quotations except where a note says otherwise.
The tool keeps a page for 15 minutes, so a fetch may have come from its cache;
I could not tell whether any did. It reads a long page 100,000 characters at a
time, and the notes say which parts of a long page were read.

- **Linux mount(8)**, util-linux:
  <https://man7.org/linux/man-pages/man8/mount.8.html>, 137,710 characters,
  read whole in two parts. The footer reads "util-linux 2.43.devel-1062-f...
  2026-08-03", cut short as the tool returned it. Of `defaults`: "Use the
  default options: rw, suid, dev, exec, auto, nouser, and async." and "Note
  that the real set of all default mount options depends on the kernel and
  filesystem type." Of `nosuid`: "Do not honor set-user-ID and set-group-ID
  bits or file capabilities when executing programs from this filesystem." Of
  `suid`: "Honor set-user-ID and set-group-ID bits or file capabilities when
  executing programs from this filesystem." **Note:** the brief quoted the
  first sentence of `defaults` without the second. (*Context*, link 5; §6)
- **macOS mount(8)**, Apple's `diskdev_cmds`:
  <https://raw.githubusercontent.com/apple-oss-distributions/diskdev_cmds/main/mount.tproj/mount.8>,
  `.Dd December 20, 2023`. Of `nosuid`: "Do not allow set-user-identifier or
  set-group-identifier bits to take effect." And "Options are specified with
  a -o flag followed by a comma separated string of options." The page has no
  `suid` option. Asked for any sentence stating which options are in effect
  by default, or that any is forced for a user who is not root, the tool found
  none that bears on set-id bits. (§6; Assumptions 9 and 12)
- **macOS mount_nfs(8)**, Apple's `NFS`:
  <https://raw.githubusercontent.com/apple-oss-distributions/NFS/main/mount_nfs/mount_nfs.8>,
  `.Dd February 28, 2010`. "Mount options are specified with a .Fl o flag
  followed by a comma separated string of options.", where `.Fl o` is the
  mdoc source of `-o`; and "See the .Xr mount 8 man page for possible options
  and their meanings." Of `locallocks`: "For NFSv2/v3 mounts, perform all file
  locking operations locally on the NFS client (in the VFS layer) instead of
  on the NFS server." Of `nolocks`, whose entry also lists `nolock`: "For
  NFSv2/v3 mounts, do not support NFS file locking operations." The page
  mentions neither `nosuid` nor `suid`. (§2; Assumption 9)
- **`mount_nfs.c`**, Apple's `NFS`:
  <https://raw.githubusercontent.com/apple-oss-distributions/NFS/main/mount_nfs/mount_nfs.c>.
  `#include <mntopts.h>`; `struct mntopt mopts[] = {`, whose first entry is
  `MOPT_STDOPTS,`; `mop = getmntopts(opts, mopts, &options.mntflags, &altflags);`;
  and, earlier in `mount_nfs_imp`, `bzero(&options, sizeof(options));`.
  `MOPT_STDOPTS` is not defined in the file, and `MNT_NOSUID` appears only in
  `{ MNT_NOSUID, "nosuid" },`, an entry in the table that `dump_mount_options`
  takes flag names from. (Assumptions 9 and 12)
- **`mntopts.h`**, Apple's `libutil`:
  <https://raw.githubusercontent.com/apple-oss-distributions/libutil/main/mntopts.h>,
  "Copyright (c) 1999-2021 Apple Computer, Inc." above a 1994 copyright of
  the Regents of the University of California.
  `#define MOPT_NOSUID { "suid", 1, MNT_NOSUID, 0 }`, with the tabs the tool
  returned written here as single spaces; `MOPT_STDOPTS` lists `MOPT_NOSUID`,
  with `MOPT_NODEV`, `MOPT_NOEXEC` and `MOPT_RDONLY` among others; and
  `struct mntopt`'s fields, `m_option` "option name",
  `m_inverse` "if a negative option, eg "dev"", `m_flag` "bit to set, eg.
  MNT_RDONLY" and `m_altloc` "1 => set bit in altflags". (Assumption 9)
- **`getmntopts.c`**, Apple's `libutil`:
  <https://raw.githubusercontent.com/apple-oss-distributions/libutil/main/getmntopts.c>,
  which the tool described as NetBSD's code under a BSD licence. An option
  beginning `no` sets `negative = 1;` and drops the prefix (`opt += 2;`); the
  table is searched with `strcasecmp(opt, m->m_option) == 0`; then
  `if (negative == m->m_inverse)` sets the flag with `*thisflagp |= m->m_flag;`
  and otherwise clears it with `*thisflagp &= ~m->m_flag;`. So `nosuid` finds
  `MOPT_NOSUID` with `negative` equal to its `m_inverse`, 1, and sets
  `MNT_NOSUID`. That is my reading of the code. (Assumptions 9 and 12)
- **Secondhand.** The brief's web-search summary of Apple's `mntopts.h`
  copies, in `cddafs` and in a `libutil` mirror at `git.saurik.com`, gave
  `MOPT_NOSUID` as `{ "suid", 1, MNT_NOSUID, 0 }` within `MOPT_STDOPTS`. A
  search run again while this ADR was written, for
  `Apple mntopts.h MOPT_STDOPTS MOPT_NOSUID "suid", 1, MNT_NOSUID`, gave the
  same. Two copies it named could not be fetched:
  <https://opensource.apple.com/source/cddafs/cddafs-201.0.1/MountUtility/mntopts.h.auto.html>
  answered 404, and
  <https://git.saurik.com/apple/libutil.git/blob/1bd2040a698ab5d6035aaf13c3544e5ab3c8af2c:/mntopts.h>
  answered 503. Nor could
  <https://raw.githubusercontent.com/apple-oss-distributions/diskdev_cmds/main/disklib/mntopts.h>,
  a guessed path, which answered 404. The `libutil` copy above is the
  firsthand reading. (Assumption 9)
- **Linux exports(5)**, nfs-utils:
  <https://man7.org/linux/man-pages/man5/exports.5.html>, dated "31 December
  2009". Of `secure`: "This option requires that requests not using gss
  originate on an Internet port less than IPPORT_RESERVED (1024).", "This
  option is on by default." and "To turn it off, specify insecure." (*What
  this does not decide*)
- **Linux nfs(5)**, nfs-utils:
  <https://man7.org/linux/man-pages/man5/nfs.5.html>, dated "9 October 2012",
  which man7 says it obtained from the project's repository on 2026-08-04. Of
  `resvport` and `noresvport`: "If this option is not specified, or the
  resvport option is specified, the NFS client uses a privileged source port."
  and "If the noresvport option is specified, the NFS client uses a
  non-privileged source port." (*What this does not decide*)
- **RFC 1813**, Appendix I: <https://www.rfc-editor.org/rfc/rfc1813.txt>,
  229,793 characters. Characters 0 to 100,000 and 185,000 to the end were
  read; 100,000 to 185,000 were not. §5.2.1's synopsis, "mountres3
  MOUNTPROC3_MNT(dirpath) = 1;", and `typedef string dirpath<MNTPATHLEN>;`.
  (§6)
- **`go test`**: <https://pkg.go.dev/cmd/go>, "Version: go1.27.2", 178,900
  characters, read whole in two parts. Under *Testing flags*: "When 'go test'
  runs a test binary, it does so from within the corresponding package's
  source code directory." (§7)
