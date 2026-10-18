# 0015. Every mount command carries nosuid and nodev

**Status:** Accepted
**Date:** 2026-10-18
**Revised:** 2026-10-18 — added `nodev` beside `nosuid` everywhere `nosuid`
goes (§1–§7), in response to the security-auditor's medium finding on the
impostor-server route; added §8 correcting the `Cred` doc comment and the
README to say the loopback binding is the *default*, not a guarantee (the
auditor's findings 3 and 4). The decision to carry `nosuid` did not change, and
nothing on the wire changed.
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

**A second route: an impostor on the port.** `nosuid` closes the route above,
where strata itself serves the set-id file. It does not close a second one that
the security-auditor raised. The mount can outlive strata — Ctrl-C before
`umount`, a crash, an exit on error, a failed restart — and the default
`-listen` port, `127.0.0.1:20490`, is unprivileged (above 1024), so once strata
is gone any local user can bind it. strata binds `127.0.0.1` specifically and
sets no `SO_REUSEPORT`, so the port cannot be taken while it runs (the
security-auditor's reading of the listener code, Assumption 13); after it exits
the port is free. A `hard` mount — the default when neither `hard` nor `soft`
is given (*Sources*, Linux `nfs(5)`), so this covers the README's and
`demo.sh`'s commands too — retries indefinitely and reconnects on a fresh
socket, so the client then binds to whatever now answers. An impostor can return
an `NF3CHR` or `NF3BLK` entry, mode 0666, carrying the `rdev` of a real local
device; without `nodev` the kernel opens that device when a process on the mount
opens the node (*Sources*, Linux `fs/nfs/inode.c` and `fs/namei.c`; macOS
`spec_vnops.c` and the NFS kext), which is local root with no set-id bit
involved. `nosuid` does not stop this; `nodev` does — the client refuses to open
a character or block device on a `nodev` mount (*Sources*). This route does not
need strata to serve a device node, and strata never does: it answers MKNOD with
`NFS3ERR_NOTSUPP`, makes only regular files, directories and symlinks, and
always sends `rdev` as 0,0 (`internal/nfs/wire.go`'s `putFattr` writes `a.RDev`,
which no `blobfs` path sets). So `nodev` is not needed against strata itself,
only against an impostor (Assumption 15) — but a mount command cannot know in
advance which server it will end up talking to.

**What ADR 0014 says of these issues.** It records #98 as "CREATE, MKDIR and
SYMLINK apply a requested group from any caller", and leaves "Root squash and
the `AUTH_SYS` trust boundary (#6, #90, #95)" undecided. This ADR cites #6,
#95 and #98 only as that does (Assumption 11).

**What this constrains.** The command `cmd/strata` prints; the commands
`scripts/demo.sh` and `scripts/mount.sh` print or run; every mount command in
`README.md` and in the Markdown under `docs/` outside `docs/adr/`; and the
`Cred` doc comment in `internal/sunrpc/rpc.go`, whose wording §8 corrects.
Nothing else in `internal/` changes, and nothing on the wire: §8 is a comment,
not behaviour. (The accepted ADR said "Nothing in `internal/` changes"; the
`nosuid` implementation had already reworded this comment, and §8 now settles
its wording, so this revision owns it.)

## Decision

### 1. The rule

Every mount command that strata prints, and every one that `README.md`, the
Markdown files under `docs/` other than `docs/adr/`, or `scripts/` gives,
carries `nosuid` and `nodev`. `docs/adr/` is excluded because an ADR records
history and may quote a command as it was.

At `60da89f` the rule reaches the five sites in *Context*. §3 to §5 give the
new text of four of them, and `docs/nfsv4-assessment.md:467` becomes
`sudo mount -t nfs -o vers=4.2,port=20490,nosuid,nodev 127.0.0.1:/ /tmp/strata-mnt`.

### 2. `mountOptions`

```go
// cmd/strata
//
// mountOptions returns the options of the mount command strata prints.
func mountOptions(goos, port string, readOnly bool) []string
```

It returns, in order:

- `"vers=3"`, `"tcp"`, `"port="+port`, `"mountport="+port`, `"noresvport"`,
  `"hard"`, `"nosuid"`, `"nodev"`;
- then `"nolocks"`, `"locallocks"` if `goos == "darwin"`, and `"nolock"` for
  any other `goos`, the empty string included;
- then `"ro"` if `readOnly`.

It returns a new slice on each call, does no I/O and keeps no state.

| `goos` | `port` | `readOnly` | `mountOptions`, its elements joined with `,` |
|---|---|---|---|
| `"darwin"` | `"20490"` | `false` | `vers=3,tcp,port=20490,mountport=20490,noresvport,hard,nosuid,nodev,nolocks,locallocks` |
| `"darwin"` | `"20490"` | `true` | `vers=3,tcp,port=20490,mountport=20490,noresvport,hard,nosuid,nodev,nolocks,locallocks,ro` |
| `"linux"` | `"20490"` | `false` | `vers=3,tcp,port=20490,mountport=20490,noresvport,hard,nosuid,nodev,nolock` |
| `"linux"` | `"12345"` | `true` | `vers=3,tcp,port=12345,mountport=12345,noresvport,hard,nosuid,nodev,nolock,ro` |
| `"freebsd"` | `"2049"` | `false` | `vers=3,tcp,port=2049,mountport=2049,noresvport,hard,nosuid,nodev,nolock`, the Linux form |
| `""` | `"20490"` | `false` | `vers=3,tcp,port=20490,mountport=20490,noresvport,hard,nosuid,nodev,nolock`, the Linux form |

No element contains a comma, so each cell gives its slice exactly. The first
row is
`[]string{"vers=3", "tcp", "port=20490", "mountport=20490", "noresvport", "hard", "nosuid", "nodev", "nolocks", "locallocks"}`.
Before this ADR, `printMountInstructions` built the same lists without
`"nosuid"` or `"nodev"`; the accepted ADR added `"nosuid"`, and this revision
adds `"nodev"` after it.

### 3. `printMountInstructions`

`printMountInstructions` prints
`strings.Join(mountOptions(runtime.GOOS, port, ro), ",")` in place of the list
it builds now. Everything else it prints is unchanged.

### 4. The scripts

- In `demo.sh`'s printed command, `nosuid,nodev` goes after `noresvport`, so
  line 22 becomes:

  ```text
    sudo mount -t nfs -o vers=3,tcp,port=$port,mountport=$port,noresvport,nosuid,nodev,nolocks,locallocks 127.0.0.1:/ $mountpoint
  ```

- In `mount.sh`, the base `opts` assignment (the one whose value begins
  `vers=`, not the two that append the lock options) becomes
  `vers=3,tcp,port=$port,mountport=$port,noresvport,hard,nosuid,nodev`, so both
  platform branches carry both options, and the comment line above it names
  both and why. Coder's brief gives the exact comment text.

### 5. The README

- Line 9 becomes:

  ```text
  $ sudo mount -t nfs -o vers=3,tcp,port=20490,mountport=20490,noresvport,nosuid,nodev,nolocks 127.0.0.1:/ /tmp/strata-mnt
  ```

- Line 113 becomes:

  ```text
  sudo mount -t nfs -o vers=3,tcp,port=20490,mountport=20490,noresvport,nosuid,nodev,nolocks,locallocks 127.0.0.1:/ /tmp/strata-mnt
  ```

- The sentence at line 116 becomes (finding 3 — the loopback binding is the
  default, not a guarantee): "`mount` needs root on both macOS and Linux; the
  server itself does not, and by default binds to loopback (`-listen` defaults
  to `127.0.0.1:20490`), which keeps other machines out but not the other users
  of this one; a `-listen` address off loopback drops even that and exposes the
  server to every host that can reach the port (see *Honest limitations*)."
- *Honest limitations*' first bullet becomes (findings 3 and 4, and the
  reviewer's `-read-only` nuance):

  ```markdown
  - **Any local user can act as any user, root included.** strata believes the
    uid and gid a client puts in its AUTH_SYS credential, as NFSv3 servers do.
    Unlike a default Linux `nfsd` export — which squashes a client's root to an
    anonymous id and, through its `secure` export option, requires requests to
    come from a reserved port — strata neither squashes root nor requires a
    reserved source port; every mount command above even passes `noresvport`.
    Its loopback port is open to every process on the machine, so any local user
    who speaks the protocol can read anything in the filesystem and,
    unless strata runs with `-read-only`, change or delete it too, whatever its
    permissions say. The mount commands above carry `nosuid` and `nodev`:
    `nosuid` stops a set-user-ID or set-group-ID program made through the protocol
    from running with its owner's or group's rights through the mount, and `nodev`
    stops a device node from being opened through it — which matters most if the
    mount outlives the server, since any local user can then bind its unprivileged
    port and serve a planted set-id or device-node file. A mount without these can
    give any local user root. Neither option stops the access itself: run strata
    only where every local user may be trusted with the whole filesystem
    ([ADR 0015](docs/adr/0015-every-mount-command-carries-nosuid.md)).
  ```

The README's prose keeps the literal text `mount -t nfs` out, because T2
checks every line that contains it (§7).

### 6. What `nosuid` and `nodev` do and do not do

- **`nosuid` does:** on Linux, set-user-ID and set-group-ID bits, and file
  capabilities, are not honoured when a program is executed from the mount;
  on macOS, set-user-identifier and set-group-identifier bits do not take
  effect (*Sources*).
- **`nodev` does:** a character or block device node on the mount is not
  interpreted — the client refuses to open it (Linux returns `-EACCES`, macOS
  `ENXIO`), so a device node served through the mount cannot be opened as a
  device (*Sources*). This matters against an impostor on the port, not against
  strata, which never serves a device node (*Context*, Assumption 15).
- **Neither does** stop any local user from reading, changing or deleting
  anything as any uid: `nosuid` and `nodev` bound what a program or node from
  the mount can do once opened, not who may speak to the server.
- **`noexec` is not added.** It would stop legitimate programs stored in the
  filesystem from running, which is a primary use, and it closes no escalation
  that `nosuid` and `nodev` leave open — a program an impostor plants and a user
  runs executes with that user's own rights, the same risk as any file a user
  fetches and runs (Assumption 4, *Alternatives considered*).
- **They are the client's options.** NFSv3 gives the server no view of the
  client's mount flags (MNT takes only a `dirpath`), so strata cannot require
  them, and a mount made any other way (the operator's own command, fstab,
  autofs) is exposed unless it carries them.

### 7. The test surface

- **T1.** Every row of §2's table, compared as exact slices. In every row
  `"nosuid"` occurs exactly once and `"nodev"` exactly once, and neither
  `"suid"` nor `"dev"` at all, as elements. Changing a returned slice does not
  change the next call's result.
- **T2.** A tripwire in `cmd/strata`, reaching the repository root as
  `../..`, because `go test` runs a package's tests in its source directory
  (*Sources*).
  - (a) For every line of `README.md`, and of every `.md` file under `docs/`,
    at any depth, outside `docs/adr/`, that contains `mount -t nfs`, the line
    has ` -o ` after that text, and the whitespace-delimited token after
    ` -o `, split on `,`, has an element equal to `nosuid` and an element equal
    to `nodev`.
  - (b) `README.md` has at least one such line. This is the at-least-one guard
    for `README.md`: a run that found no `mount -t nfs` line there would
    otherwise pass (a) vacuously.
  - (c) The scripts, checked on the line that assembles or runs the command and
    not as a whole-file substring — because a comment line in `mount.sh` also
    names the options, so a substring check would pass even if they were dropped
    from the command (the auditor's test-weakness finding):
    - `scripts/mount.sh`: the base assignment — the line that, after leading
      whitespace, begins `opts=` and whose value begins `vers=`, which
      distinguishes it from the `opts="$opts,…"` lines that append the lock
      options — has, in the comma-separated value between its first pair of
      double quotes, an element equal to `nosuid` and an element equal to
      `nodev`. At least one such base assignment must be found, or (c) fails.
    - `scripts/demo.sh`: every line containing `mount -t nfs` is checked as in
      (a). At least one such line must be found, or (c) fails.

  Docs keep their mount commands in code blocks, each on one line, and keep
  the literal text `mount -t nfs` out of their prose, or (a) will flag them.
- **What only review can show:** that `printMountInstructions` prints
  `mountOptions`' list; that every script branch carries both options; that the
  README text matches §5, and the `Cred` comment matches §8; and that the
  printed command mounts, and honours `nosuid` and `nodev`, on macOS and Linux,
  which needs a human (#90).

### 8. The `Cred` doc comment and "loopback by default"

The `Cred` doc comment in `internal/sunrpc/rpc.go` and `README.md` stated that
the server "listens on loopback" / "binds to loopback only" as though it always
does. It does not: `-listen` takes any address and nothing checks it (its
default is `127.0.0.1:20490`; `cmd/strata/main.go` passes the flag value
straight to `net.Listen`). Off loopback, every host that can reach the port can
assert any uid. The comment and the README (§5) are corrected to say the
loopback binding is the *default*, to name `-listen`, and to say what an address
off loopback costs. Whether strata should refuse or warn on such an address is
left open (*What this does not decide*).

The `Cred` doc comment becomes, verbatim:

```go
// Cred carries the AUTH_SYS identity a client asserts. NFS auth is advisory by
// design: the client states who it is and the server believes it, and RFC 5531
// Appendix A warns that this flavor "does not guarantee any security for the
// users or providers of a service, in itself". By default the server binds
// loopback (-listen defaults to 127.0.0.1:20490), which keeps other machines
// out but not other local users: any local process can connect and claim any
// uid, root included, so every local user can act as any user through it. A
// -listen address off loopback drops even that, exposing the server to every
// host that can reach the port (README.md, "Honest limitations"; ADR 0015).
```

This is a comment: no behaviour changes, and nothing on the wire.

## Assumptions

Each is mine unless it says otherwise. Assumptions 1 and 2 record what the
owner's decision 1 delegated, and say so.

1. **Status Accepted on creation**, delegated by the owner's words of
   2026-10-10 (decision 1).
2. **The scope is recommendation 12**, delegated by the same words: the
   printed command, the documented ones, both scripts, and the README
   limitation. Everything under *What this does not decide* is left alone on
   purpose.
3. **Where `nosuid` and `nodev` sit.** `nodev` goes immediately after `nosuid`
   everywhere, so the two hardening options read together. That puts them after
   `hard` in the printed list and in `mount.sh`; after `noresvport` in the
   README's commands and `demo.sh`'s, which carry no `hard`; and before the lock
   options everywhere. This is for reading order; mount does not care.
4. **`nodev` is added; `noexec` is not.** This revision adds `nodev`, which the
   accepted ADR left out. The accepted ADR's reason for leaving it out — MKNOD
   is refused, so no client can create a device node (`internal/nfs/nfs3.go`
   answers MKNOD with `NFS3ERR_NOTSUPP`, and `createEntry` makes only regular
   files, directories and symlinks) — was about strata as the server and missed
   the impostor route (*Context*): after strata exits, a local user can bind its
   unprivileged port and serve a device node strata never would. `nodev` is the
   client-side defence against that, and nothing legitimate on this filesystem
   needs device nodes, so it costs nothing. Adding it is my judgement, prompted
   by the security-auditor's medium finding; it is beyond the owner's
   recommendation 12 (which named `nosuid`), and I read it as covered by the
   owner's standing "follow architect's … recommendations" (decision 1), but it
   is a judgement call open to pushback. `noexec` stays out: it would stop
   legitimate programs stored in the filesystem from running, and it closes no
   escalation that `nosuid` and `nodev` leave open (§6, *Alternatives
   considered*).
5. **The tripwire's scope and its parsing rule** (§7). The scope is where a
   reader copies a command from, without `docs/adr/`, for §1's reason. The
   rule is plain string handling, not a shell parser: it finds `nosuid` and
   `nodev` in a literal option list on the line that names the mount, and in
   `mount.sh` on the base `opts=` assignment. A doc keeps each command on one
   line, and the scripts keep the base command on one line, for this to hold.
6. **Every `goos` other than darwin gets the Linux spelling.** That is today's
   behaviour.
7. **Not breaking.** No running deployment needs to act to keep working. The
   change is to the commands strata prints, documents and scripts: strata itself
   behaves as before, and no existing mount changes, so an existing mount keeps
   working as it was, and remounting with the new options is for safety. That
   narrower ground is what the conclusion rests on, not a claim that the options
   cost nothing. `nodev` costs nothing legitimate, since no client can make a
   device node through strata. `nosuid` does cost something: strata stores and
   serves set-id bits (*Context*, links 2 and 3), so a set-id program kept in
   strata no longer runs with its owner's or group's rights through a mount made
   with the new commands, a remount through `scripts/mount.sh` included
   (*Consequences*). A deployment that relies on that has to move such programs
   off strata; mounting without `nosuid` keeps them working only by keeping the
   route to root open. The `CHANGES` entry says so. So it carries no `BREAKING:`
   prefix, and the owner's decision 2 is not relied on. This is a judgement
   call: if a set-id program that stops running with its owner's rights at the
   next mount through the shipped script counts as a deployment needing action
   to keep working, the entry should take `BREAKING:` instead.
8. **The printed text gets no explanation of `nosuid` or `nodev`; the README
   carries one.**
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
13. **The impostor route's premises** (*Context*). That the default port 20490
    is unprivileged, and so bindable by any local user once strata exits, rests
    on its being above 1024 and on macOS having lifted the privileged-port
    restriction for the common bind (*Sources*, Apple Developer Forums); the
    forum's caveat is about binding a *specific* address on a *privileged* port,
    which 20490 is not, so it does not apply. That strata holds the port
    exclusively while it runs — it binds `127.0.0.1` with no `SO_REUSEPORT` — is
    the security-auditor's reading of the listener code, which I did not re-run.
    That a `hard` mount is the default and reconnects to whatever then answers
    the port rests on Linux `nfs(5)` (*Sources*) for Linux; for macOS it is my
    inference from the same `hard` semantics, which a real mount (#90) confirms.
14. **Both kernels honour `nodev` as they do `nosuid`.** The citations show each
    kernel's NFS client gate the opening of a device node on the mount's
    `nodev`/`MNT_NODEV` flag (*Sources*, Linux `fs/namei.c`'s `may_open_dev` and
    `fs/nfs/inode.c`'s `nfs_fhget`; macOS `spec_vnops.c`'s `spec_open` reached
    through the NFS kext's `spec_nfsv2nodeop_entries`). That the NFS mount's own
    flag is the one consulted is my reading of those two tables; as for `nosuid`
    (Assumption 10) nothing was run, and #90 stays open to confirm it by a real
    mount.
15. **`rdev` is never served.** `internal/nfs/wire.go`'s `putFattr` writes
    `a.RDev`, and no `blobfs` path sets it, so every object strata serves has
    `rdev` 0,0. A tampered bucket could set an inode's type — it is decoded from
    JSON (`inode.Type`, tag `t`) without validation — but not its `rdev`, and
    bucket write access is a different, higher principal than a local mount
    user. So `nodev` is not a defence against strata or its bucket, only against
    an impostor on the port (the security-auditor's point, which I confirmed by
    reading `wire.go` and the `blobfs` inode paths).

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
- **`noexec` as well.** Left out: it would stop legitimate programs stored in
  the filesystem from running, and a program an impostor plants and a user runs
  executes with that user's own rights — the same risk as any file a user
  fetches and runs — so it closes no escalation that `nosuid` and `nodev` leave
  open (§6, Assumption 4). `nodev` is now added, not an alternative.
- **Refusing or warning on a non-loopback `-listen`.** A stronger answer to the
  auditor's findings 3 and 4 than wording alone, but a behaviour change beyond
  this decision; drafted as a separate issue rather than decided here (*What
  this does not decide*).
- **Making strata `umount` on exit, or warning if the mount outlives it.** The
  only thing that fully closes the impostor route, since `nodev` and `nosuid`
  bound but do not remove what an impostor can serve — planted non-device,
  non-set-id content remains (*Consequences*). A lifecycle and trust-boundary
  question tied to #6; drafted as a separate issue (*What this does not
  decide*).

## Consequences

- A mount made with any command strata prints, documents or scripts does not
  run a set-id program made through the protocol with its owner's or group's
  rights (§6; for macOS, Assumptions 9 and 12), and does not open a device node
  served to it (§6; Assumption 14). That closes two routes from any local user
  to root through such a mount: strata serving a set-id file, and — if the mount
  outlives strata — an impostor on the port serving a device node (*Context*).
- The access itself is unchanged: any local user can still read anything, and
  (unless strata runs `-read-only`) change or delete anything, in the filesystem
  as any uid (§6). The README now says so, first among its limitations, and says
  plainly that strata, unlike a default `nfsd` export, neither squashes root nor
  requires a reserved source port.
- A set-id program kept in strata does not run with its rights through such
  a mount, nor, on Linux, with its file capabilities.
- The impostor route is bounded, not removed. `nodev` and `nosuid` stop
  privilege escalation through an impostor, but an impostor can still serve
  planted ordinary files — programs that run as the invoking user, or
  misleading data or config — to any process that trusts the mount. Closing
  that needs the server and client to authenticate each other, or the mount not
  to outlive the server (#6; *What this does not decide*).
- A mount made before this change, or made any other way, stays exposed until
  it carries both `nosuid` and `nodev`, and strata cannot tell which kind it is
  serving (§6). The `CHANGES` entry tells the operator to remount.
- Every mount command added later to `README.md`, or to the docs outside
  `docs/adr/`, must carry `nosuid` and `nodev` on the line that names
  `mount -t nfs`, or T2 fails.
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
- **Whether to refuse or warn on a `-listen` address off loopback.** Off
  loopback the same server is in reach of other machines, each able to assert
  any uid. This revision corrects the wording (§5, §8) but does not change
  behaviour; whether strata should refuse such an address, or warn, is drafted
  as a separate issue, cross-referencing #6 and #95.
- **What to do about an impostor that outlives the server.** `nodev` and
  `nosuid` bound the harm but do not remove it (*Consequences*); a fuller answer
  — mutual authentication (#6), or strata unmounting or warning on exit — is
  drafted as a separate issue.
- **`noexec`** (§6, *Alternatives considered*).

## Sources

Fetched on 2026-10-18, while this ADR was written, through a tool that
summarises what it fetches, and quoted as it returned them when asked for
verbatim text. The brief this ADR was written from cites the same pages from
fetches earlier that day, during planning. Each page quoted here was fetched
again, and matched the brief's quotations except where a note says otherwise.
The tool keeps a page for 15 minutes, so a fetch may have come from its cache;
I could not tell whether any did. It reads a long page 100,000 characters at a
time, and the notes say which parts of a long page were read. The sources for
this revision — the `nodev`/`noexec`/`dev`/`exec` entries added to the two
mount(8) pages and to `mntopts.h`, the root-squash text from exports(5), the
reconnect text from nfs(5), and the five new entries below (Linux `fs/namei.c`
and `fs/nfs/inode.c`, macOS `spec_vnops.c` and the NFS kext's `nfs_vnops.c`, the
Apple Developer Forums thread, and RFC 5531) — were fetched the same day, while
this revision was written, through the same tool and treated the same way:
untrusted evidence to cite, not instructions to follow.

- **Linux mount(8)**, util-linux:
  <https://man7.org/linux/man-pages/man8/mount.8.html>, 137,710 characters,
  read whole in two parts. The footer reads "util-linux 2.43.devel-1062-f...
  2026-08-03", cut short as the tool returned it. Of `defaults`: "Use the
  default options: rw, suid, dev, exec, auto, nouser, and async." and "Note
  that the real set of all default mount options depends on the kernel and
  filesystem type." Of `nosuid`: "Do not honor set-user-ID and set-group-ID
  bits or file capabilities when executing programs from this filesystem." Of
  `suid`: "Honor set-user-ID and set-group-ID bits or file capabilities when
  executing programs from this filesystem." Of `nodev`: "Do not interpret
  character or block special devices on the filesystem." Of `dev`: "Interpret
  character or block special devices on the filesystem." Of `noexec`: "Do not
  permit direct execution of any binaries on the mounted filesystem." Of
  `exec`: "Permit execution of binaries and other executable files."
  **Note:** the brief quoted the first sentence of `defaults` without the
  second; the `nodev`/`dev`/`noexec`/`exec` quotes are this revision's, from the
  same page read again (characters 0 to 100,000). (*Context*, link 5; §6;
  Assumption 4)
- **macOS mount(8)**, Apple's `diskdev_cmds`:
  <https://raw.githubusercontent.com/apple-oss-distributions/diskdev_cmds/main/mount.tproj/mount.8>,
  `.Dd December 20, 2023`. Of `nosuid`: "Do not allow set-user-identifier or
  set-group-identifier bits to take effect." And "Options are specified with
  a -o flag followed by a comma separated string of options." The page has no
  `suid` option. Asked for any sentence stating which options are in effect
  by default, or that any is forced for a user who is not root, the tool found
  none that bears on set-id bits. Of `nodev` (this revision, same page read
  again): "Do not interpret character or block special devices on the file
  system." Of `noexec`: "Do not allow execution of any binaries on the mounted
  file system." The page has no `dev` or `exec` option. (§6; Assumption 4;
  Assumptions 9 and 12)
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
  MNT_RDONLY" and `m_altloc` "1 => set bit in altflags". Read again for this
  revision, the page gave `#define MOPT_NODEV { "dev", 1, MNT_NODEV, 0 }` and
  `#define MOPT_NOEXEC { "exec", 1, MNT_NOEXEC, 0 }` (tabs again written as
  spaces), and `MOPT_STDOPTS` lists `MOPT_NODEV` and `MOPT_NOEXEC` alongside
  `MOPT_NOSUID`. So on macOS, as with `nosuid`, the string `nodev` sets
  `MNT_NODEV` by the inverse-of-`dev` rule `getmntopts.c` applies (above).
  (Assumption 4; Assumption 9)
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
  option is on by default." and "To turn it off, specify insecure." Of root
  squashing, read again for this revision: "This mode of operation (called
  `root squashing') is the default, and can be turned off with no_root_squash.",
  and of `root_squash`: "Map requests from uid/gid 0 to the anonymous uid/gid."
  (*What this does not decide*; §5's README text and finding 4)
- **Linux nfs(5)**, nfs-utils:
  <https://man7.org/linux/man-pages/man5/nfs.5.html>, dated "9 October 2012",
  which man7 says it obtained from the project's repository on 2026-08-04. Of
  `resvport` and `noresvport`: "If this option is not specified, or the
  resvport option is specified, the NFS client uses a privileged source port."
  and "If the noresvport option is specified, the NFS client uses a
  non-privileged source port." Read again for this revision, of `hard`: "If no
  option is specified (or if the hard option is specified), NFS requests are
  retried indefinitely."; on reconnection: "After the client exhausts its
  retransmits ... it assumes a network partition has occurred, and attempts to
  reconnect to the server on a fresh socket." (so a `hard` mount, the default,
  reconnects to whatever answers the port); and "Refer to mount(8) for a
  description of generic mount options available for all file systems." (so the
  mount(8) options above, including `nodev`, apply to an NFS mount).
  (*Context*; *What this does not decide*)
- **RFC 1813**, Appendix I: <https://www.rfc-editor.org/rfc/rfc1813.txt>,
  229,793 characters. Characters 0 to 100,000 and 185,000 to the end were
  read; 100,000 to 185,000 were not. §5.2.1's synopsis, "mountres3
  MOUNTPROC3_MNT(dirpath) = 1;", and `typedef string dirpath<MNTPATHLEN>;`.
  (§6)
- **`go test`**: <https://pkg.go.dev/cmd/go>, "Version: go1.27.2", 178,900
  characters, read whole in two parts. Under *Testing flags*: "When 'go test'
  runs a test binary, it does so from within the corresponding package's
  source code directory." (§7)
- **Linux `fs/namei.c`**, Torvalds' tree:
  <https://raw.githubusercontent.com/torvalds/linux/master/fs/namei.c>, 182,863
  characters, the relevant part read from offset 100,000. `may_open_dev`:
  "return !(path->mnt->mnt_flags & MNT_NODEV) && !(path->mnt->mnt_sb->s_iflags &
  SB_I_NODEV);"; and in `may_open`, "case S_IFBLK:" / "case S_IFCHR:" / "if
  (!may_open_dev(path))" / "return -EACCES;". So opening a device node returns
  `-EACCES` when the mount carries `MNT_NODEV`. (*Context*; §6; Assumption 14)
- **Linux `fs/nfs/inode.c`**, Torvalds' tree:
  <https://raw.githubusercontent.com/torvalds/linux/master/fs/nfs/inode.c>.
  `nfs_fhget` ends its type dispatch with "} else" / "init_special_inode(inode,
  inode->i_mode, fattr->rdev);", reached when the inode is not a regular file,
  directory or symlink, so the NFS client makes a device node from the
  server-supplied mode and `rdev`. That the resulting node is then subject to
  `may_open_dev` is the tie to `fs/namei.c` above. (*Context*; Assumption 14)
- **macOS `spec_vnops.c`**, Apple's `xnu`:
  <https://raw.githubusercontent.com/apple-oss-distributions/xnu/main/bsd/miscfs/specfs/spec_vnops.c>.
  `spec_open(struct vnop_open_args *ap)` contains "if (vp->v_mount &&
  (vp->v_mount->mnt_flag & MNT_NODEV)) {" guarding "return ENXIO;", so opening a
  special file returns `ENXIO` when its mount carries `MNT_NODEV`. (*Context*;
  §6; Assumption 14)
- **macOS NFS kext `nfs_vnops.c`**, Apple's `NFS`:
  <https://raw.githubusercontent.com/apple-oss-distributions/NFS/main/kext/nfs_vnops.c>,
  286,611 characters, characters 0 to 100,000 read. "static const struct
  vnodeopv_entry_desc spec_nfsv2nodeop_entries[] = {" with the entry "{
  &vnop_open_desc, (vnop_t *)spec_open }," (the `spec_nfsv4nodeop_entries`
  variant has the same line). So a device node on an NFS mount routes `open`
  through `spec_open` above, which consults the NFS mount's own `MNT_NODEV`.
  That this is the flag the `nodev` option sets is my reading of these tables
  (Assumption 14). (*Context*; Assumption 14)
- **Apple Developer Forums**, "Binding on priviledged ports on macOS":
  <https://developer.apple.com/forums/thread/674179>, a DTS engineer's reply
  dated Feb '21, not formal Apple documentation. On non-root binding below
  1024: "That's correct. iOS never had this restriction and we've now lifted it
  on macOS." and "this was an intentional change". A later post in the thread
  reports that binding a *specific* address on a privileged port still needs
  root; 20490 is unprivileged, so that caveat does not bear on the impostor
  route. (*Context*; Assumption 13)
- **RFC 5531**, Appendix A (System Authentication):
  <https://www.rfc-editor.org/rfc/rfc5531.txt>, 161,720 characters, characters
  0 to 100,000 read. "The verifier accompanying the credential should have
  "AUTH_NONE" flavor value"; the reply verifier "may be "AUTH_NONE" or
  "AUTH_SHORT"."; and "It should be noted that use of this flavor of
  authentication does not guarantee any security for the users or providers of
  a service, in itself." The server-reply verifier matches what `rpc.go`
  already writes (AUTH_NONE, empty body), and the "does not guarantee any
  security" sentence is quoted in §8's `Cred` comment. (§8)
