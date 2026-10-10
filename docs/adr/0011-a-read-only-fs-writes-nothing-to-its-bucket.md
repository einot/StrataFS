# 0011. A read-only FS writes nothing to its bucket

**Status:** Accepted
**Date:** 2026-10-07
**Revised:** 2026-10-08 — cross-references only. The bullet on #64, the commit
window, under *What this does not decide* now ends by saying that ADR 0012
decides it. Nothing else changed.
**Issue:** #68

## Context

`run`, in `cmd/strata/main.go`, defines `-read-only` with the help text
"refuse all modifications" (`main.go:42`; every line number here is from
`3dc7c11`) and passes it as `blobfs.Config.ReadOnly` (`main.go:121`). That
field's doc comment says it "refuses every mutating operation at the VFS
layer" (`internal/blobfs/fs.go:43-44`).

**What a read-only `New` does with a bucket that has no root pointer.** `New`
copies `cfg.ReadOnly` into `FS.readOnly` (`fs.go:225`) and calls `loadOrInit`
(`fs.go:236`), which never reads it. `loadOrInit` begins with a `Get` of the
root pointer's key, `root` (`internal/blobfs/commit.go:23`), and when that
answers `store.ErrNotFound` it calls `initEmpty` (`commit.go:24-26`), whatever
else the bucket holds. `initEmpty` builds a root directory owned by
`Config.OwnerUID` and `Config.OwnerGID` (`commit.go:87-88`), takes the new
filesystem's fsid from the store's name (`:98`), and commits (`:102`). The
commit, `commitLocked`:

- `Put`s a snapshot (`:163`);
- writes the root pointer with `PutIfMatch` and an empty ETag, that is
  If-None-Match (`:180`), or, on a backend without conditional writes, `Put`s
  it and `Head`s it (`:190-199`);
- calls `schedulePrune` (`:217`), which, unless `Config.SnapshotRetention` is
  negative, starts a goroutine (`:223-238`) that `List`s `snapshots/` and,
  when there are more snapshots than the retention, `Delete`s the lowest
  keys, which are the oldest epochs, sparing the current epoch's, which for
  `initEmpty`'s commit is epoch 1 (`:246-288`).

A bucket holds more snapshots than the retention when it held a filesystem
whose root pointer is gone and which kept more, under a larger
`-snapshot-retention` or `-1`, which keeps them all, or which kept the orphan
snapshots of commits that failed after writing theirs. So a read-only mount
of such a bucket makes a new filesystem among the old one's remains, and can
delete some of the old filesystem's snapshots.

**Where `ReadOnly` is enforced.** Only once `New` has returned: `mutable()`
answers `vfs.ErrROFS` to every call that would change the filesystem
(`fs.go:379-388`), `Access` grants no modify, extend or delete (`fs.go:498`),
and `Run` starts no committer (`fs.go:271-277`).

**#68's account, checked against the code.** #68 says that `New` does not
consult `ReadOnly` before `loadOrInit`, that on an empty bucket `initEmpty`'s
commit stores a snapshot and a root pointer, and that `ReadOnly` is enforced
only afterwards, through `mutable()`. All of that holds, and it understates
the commit, which also starts the pruning above and, on a backend without
conditional writes, writes the root pointer unconditionally. #68 also says
that a read-only mount pointed at the wrong bucket or prefix creates a
filesystem there. For a bucket it does. strata has no prefix: `openStore`
keeps only the bucket name of `s3://bucket/prefix` (`main.go:259`). And a
mistyped local path is created by `store.NewLocal`
(`internal/store/local.go:26`) and then given a filesystem.

**Every store write, and what reaches it.** The calls in `internal/blobfs`
that write to the store are `Put` at `commit.go:163`, `commit.go:194` and
`fs.go:1102`; `PutIfMatch` at `commit.go:180`; and `Delete` at
`commit.go:276`. Each is reached only by a commit, `initEmpty`'s or that of a
`Sync` that finds `FS.dirty` set; by a flush of an `FS.open` entry; or by the
pruning a commit starts. `FS.open` entries are made only by calls that
`mutable()` refuses first, and `FS.dirty` is set only by those calls and by
flushes of the entries they make. `Sync` (`commit.go:121-135`), which `Commit`
(`fs.go:1813-1830`) and `cmd/strata`'s shutdown (`main.go:160-164`) also call
on a read-only mount, commits only when `FS.dirty` is set. So `initEmpty`'s
commit, and the pruning it starts, are the only store writes a read-only
mount makes today. `knownChunks` and the chunk cache live in memory, and so
does the MOUNT server's table of clients (`internal/nfs/mount.go:49-52`). The
bucket has no lock object.

**Nothing below the FS enforces that.** For a bucket that holds a
filesystem, a read-only mount writes nothing only because every path that
changes something calls `mutable()` first, so that `FS.dirty` stays false.
`store.ReadOnly` (`internal/store/store.go:56-69`) exists to put the refusal
underneath instead: a filesystem opened through it "can be inspected or
recovered with no risk of modifying the bucket, because the refusal sits
below the filesystem rather than relying on every write path remembering to
check a flag". `cmd/strata` does not use it; only tests do.

**Outside `New`.** Two more writes come with `-read-only` that `New` never
sees. `openStore` calls `store.NewLocal`, which creates a local bucket's
directory, parents included, before `New` runs (`local.go:26`). And `-check`
returns before `New` (`main.go:107-109`): `runCheck` writes and deletes probe
objects under `strata-check/` whatever `-read-only` says
(`cmd/strata/check.go:21-68`, `:255-263`).

**Who works around it.** ADR 0007 §7, ADR 0009 §5 and the tests in
`internal/blobfs/commit_interval_test.go` reach a read-only mount through a
bucket that already holds a filesystem, because of #68. ADR 0009 and
ADR 0010 list #68 under *What this does not decide*.

## Decision

### 1. A bucket that holds no filesystem

A bucket holds no filesystem when `Get` of the root pointer's key, `root`,
returns an error for which `errors.Is(err, store.ErrNotFound)` holds. That is
the test `loadOrInit` already makes. Nothing else in the bucket is consulted.

| What the bucket holds | `Get` of `root` | Writable `New` (unchanged) | Read-only `New` before this ADR | Read-only `New` now |
|---|---|---|---|---|
| nothing | `store.ErrNotFound` | creates a filesystem and commits it | the same, and serves it | refused (§2) |
| objects, but no root pointer: a first commit cut short, a filesystem whose root pointer was deleted, unrelated objects | `store.ErrNotFound` | creates a new filesystem among them and commits it | the same; its pruning can delete some of the snapshots there (*Context*) | refused (§2) |
| a root pointer and the snapshot it names | the pointer | mounts it | mounts it | mounts it |
| a root pointer that does not parse or gives another format version, or that `Head` fails on, or that names a snapshot that cannot be read or decoded or that has no root inode | the pointer | fails, writing nothing | the same | the same |
| anything, when `Get` of `root` fails another way (a 403, a network error) | that error | fails, writing nothing | the same | the same |

A local directory that does not exist is created by `store.NewLocal` before
`New` runs, and then holds nothing. An S3 bucket that does not exist answers
`NoSuchBucket`, which the S3 store reports as `store.ErrNotFound`
(`internal/store/s3.go:173-175`), so it also holds nothing: a writable `New`
fails at its first `Put`, and a read-only one is refused.

### 2. A read-only `New` refuses a bucket that holds no filesystem

- With `cfg.ReadOnly` set and a bucket that holds no filesystem (§1), `New`
  returns a nil `*FS` and a non-nil error.
- It refuses after the read that tells it so, and before any write: it makes
  no call of `Put`, `Delete` or `PutIfMatch` on `cfg.Store`, creates nothing,
  and starts no goroutine. Which reads it makes, and how many, is not pinned.
- The refusal is on the branch of `loadOrInit` where `Get` of `root` reports
  `store.ErrNotFound`, in place of `initEmpty`. `initEmpty` is unchanged, and
  only a writable mount reaches it.
- The error's text contains `no filesystem` and `cfg.Store.Name()`, the
  store's name as given. Nothing else about it is pinned, including whether
  it wraps `store.ErrNotFound`. For example:

  ```text
  blobfs: local:/tmp/bucket holds no filesystem, and a read-only mount does not create one
  ```

- Every check `New` makes on `cfg` alone comes first, ADR 0009 §2's
  included, so a read-only `cfg` with a negative interval is still refused
  with no store call at all. ADR 0009 §1's "accepts it" is about the
  interval: a read-only `New` that accepts the interval can still refuse the
  bucket.
- `Config.OwnerUID`, `OwnerGID` and `SnapshotRetention`, and any `ChunkSize`
  `New` accepts, make no difference to the refusal.
- A writable `New` is unchanged.

### 3. A read-only FS reaches its store only through `store.ReadOnly`

- With `cfg.ReadOnly` set, `New` makes `store.ReadOnly{Store: cfg.Store}` the
  FS's store, after its checks on `cfg` and before its first store call,
  `loadOrInit`'s `Get`, and nothing replaces it. Building it makes no call,
  so ADR 0009 §2 is unaffected.
- So no call of `Put`, `Delete` or `PutIfMatch` reaches `cfg.Store` from a
  read-only FS, whatever is called on it, through a path that exists today
  or one added later. Reads pass through unchanged.
- `store.ReadOnly` refuses the writes it overrides. A writing method added to
  `store.Store` later must be overridden there too, or it passes through.
  That is `store.ReadOnly`'s own contract, and this ADR does not change it.
- The FS's store's `Name` then ends in ` (read-only)` (`store.go:69`). On a
  read-only FS the name is used only in §2's error, which contains
  `cfg.Store.Name()` either way. `initEmpty`, which derives a new
  filesystem's fsid from the name (`commit.go:98`), is reached only by a
  writable mount, so no fsid changes.
- Whether `New` avoids wrapping a `cfg.Store` that is already a
  `store.ReadOnly` is not pinned; wrapping twice is harmless.

### 4. What `Config.ReadOnly` means

On a read-only FS:

- `New` refuses a bucket that holds no filesystem (§2);
- no call of `Put`, `Delete` or `PutIfMatch` reaches `Config.Store` (§3);
- `SetAttr`, `Write`, `Create`, `Mkdir`, `Symlink`, `Remove`, `Rmdir` and
  `Rename`, given arguments a writable mount would accept, answer
  `vfs.ErrROFS`, and `Write`'s count is then 0;
- `Access` grants none of `vfs.AccessModify`, `vfs.AccessExtend` and
  `vfs.AccessDelete`;
- `Run` starts no committer, and returns once its context is cancelled;
- `Sync` and `Commit` return nil: nothing is ever buffered or changed, so
  there is nothing to commit.

Only the first two are new. `Config.ReadOnly`'s doc comment states all of
them, in words to this effect:

```go
// ReadOnly makes the mount read-only all the way down to the bucket. New
// refuses a bucket that holds no filesystem rather than create one there,
// and puts Store behind store.ReadOnly, so that nothing the FS does writes
// to it. Every method that would change the filesystem answers
// vfs.ErrROFS, Access grants no modify, extend or delete, Run starts no
// committer, and Sync and Commit, with nothing to commit, return nil
// (ADR 0011).
ReadOnly bool
```

### 5. What does not change

- Writable mounts: `initEmpty`, commits and pruning, including over a bucket
  with no root pointer.
- A `store.ReadOnly` passed as `Config.Store` with `Config.ReadOnly` unset:
  `New` over a bucket with no root pointer fails at `initEmpty`'s first
  `Put`, as before. `fs_test.go`'s `TestReadOnlyMount`, ADR 0003 §5 and
  ADR 0007 §7 rely on `store.ReadOnly` being usable as `Config.Store` without
  `Config.ReadOnly`, not on that failure; ADR 0007 §7 (lines 489-494) says
  so.
- `cmd/strata`, `internal/store`, `internal/vfs` and `internal/nfs`; the
  `-read-only` flag's name, default and help text; `-check`.

### 6. What the operator sees

- `strata -read-only` against a bucket that holds no filesystem exits with
  status 1, printing `strata: ` and §2's error (`main.go:28-33`), before it
  listens, prints the mount instructions or starts `Run`, because `run` calls
  `New` first (`main.go:114-145`).
- A local directory that does not exist is still created first (§5; *What
  this does not decide*).
- On AWS S3, a caller without `s3:ListBucket` gets 403, not 404, for a
  missing key (*Sources*). `Get` of `root` then fails with that error, and
  `New` fails with it in either mode, writing nothing, as before. Whether ECS
  answers the same way was not established.

### 7. The test surface

`New`, `Config.ReadOnly`, `Config.Store` and the behaviour in §1 to §4 are
this ADR's interface. A test in package `blobfs` may use these names,
restated from `3dc7c11`:

```go
// internal/blobfs
func New(ctx context.Context, cfg Config) (*FS, error)

type Config struct {
	Store              store.Store
	ChunkSize          uint32
	CommitInterval     time.Duration
	OwnerUID, OwnerGID uint32
	ReadOnly           bool
	SnapshotRetention  int
	Log                *slog.Logger
	// ... fields this ADR does not use
}

func (f *FS) Sync(ctx context.Context) error
func (f *FS) Run(ctx context.Context)
// and every vfs.FS method (internal/vfs/vfs.go)

// unexported
type FS struct {
	mu    sync.RWMutex // ADR 0003 §4
	dirty bool         // the namespace has changes no commit has stored
	// ...
}

const (
	rootKey        = "root"       // the root pointer (README.md, "The idea")
	snapshotPrefix = "snapshots/" // where snapshots are stored
)
```

`store.Store` and `store.ObjectInfo` are as ADR 0009 §5 restates them,
unchanged at `3dc7c11`. A test may use `rootKey` and `snapshotPrefix` to
build a bucket state directly through the `*store.Local` beneath its fake. A
test may set `FS.dirty` to true on a read-only FS, holding `FS.mu` for
writing, after `New` has returned, to stand for a build in which some path
dirties a read-only mount. Nothing else about `FS.dirty` is pinned.

A test may rely on:

1. With `Config.ReadOnly` set, for a bucket that holds nothing, one that
   holds only an object under a key outside `root`, `snapshots/` and
   `chunks/`, and one that held a filesystem whose root pointer has been
   deleted: `New` returns a nil `*FS` and a non-nil error whose text contains
   `no filesystem` and `cfg.Store.Name()`; it makes no call of `Put`,
   `Delete` or `PutIfMatch` on `cfg.Store`; and afterwards the bucket holds
   exactly the objects it held before, at the same sizes, and no root
   pointer.
2. With `Config.ReadOnly` unset, for a bucket that holds nothing: `New`
   returns a non-nil `*FS` and a nil error, and the bucket then holds an
   object under `root`.
3. With `Config.ReadOnly` set, for a bucket that holds a filesystem: `New`
   returns a non-nil `*FS` and a nil error. Then, whatever `vfs.FS` methods,
   `Sync`, `Commit` and `Run` are called, no call of `Put`, `Delete` or
   `PutIfMatch` reaches `cfg.Store`; the bucket's objects, their sizes and
   the root pointer's bytes stay as they were; and the calls behave as §4
   lists.
4. With `Config.ReadOnly` set, a bucket that holds a filesystem, and
   `FS.dirty` set as above: `Sync` makes no call of `Put`, `Delete` or
   `PutIfMatch` on `cfg.Store`, and the bucket stays as it was. What `Sync`
   returns is not pinned.
5. For a bucket whose root pointer is present but whose snapshots have all
   been deleted, with `Config.ReadOnly` set or not: `New` returns a nil `*FS`
   and a non-nil error, makes no call of `Put`, `Delete` or `PutIfMatch` on
   `cfg.Store`, and leaves the bucket as it was.

A test does not rely on which reads `New` makes, on the error's text beyond
its two parts, on what is logged, or on §3's mechanism beyond item 4.

These rules keep a test safe against a build without this ADR, which creates
and commits a filesystem for a read-only mount of a bucket with no root
pointer, and commits a dirty read-only FS:

- every `Config` sets `SnapshotRetention` to −1, so that such a build's
  commit starts no pruning goroutine that outlives the test;
- a test never calls `Run`, `Sync` or any other method on an `FS` that `New`
  returned for a bucket with no root pointer; it reports the case as failed
  instead;
- every `New` and every `FS` call runs in a goroutine that recovers a panic,
  and is waited for under a bound;
- the fake's counts are read before the bucket is inspected, and the bucket
  is inspected through the `*store.Local` beneath the fake, never through the
  fake;
- every bucket is a local directory under the test's temporary directory.

What the tests cannot show is left to review: that §3's wrapper carries
every call the FS makes on its store; that the refusal is on `loadOrInit`'s
not-found branch and nowhere else; that `New` makes no store call before
ADR 0009 §2's check, which ADR 0009's own tests show; that `cmd/strata` is
unchanged, so the refusal comes before the listener and the mount
instructions; and `Config.ReadOnly`'s doc comment.

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required, except where it says otherwise. Five of them record a decision
that the repository owner delegated to the architect's recommendation, and
say so: Assumptions 1, 5, 10, 11 and 12.

1. **Refuse, rather than serve an empty filesystem from memory.** Such a
   mount hides the mistyped bucket #68 is about behind an empty directory
   that reads as "everything is gone". It would serve a root directory, an
   owner, times and a handle that exist nowhere. And a read-only mount never
   reloads, so it would stay empty even after a writer creates a filesystem
   in the bucket. The owner delegated this to the architect's recommendation
   on 2026-10-07.
2. **"No filesystem" means no root pointer,** the test `loadOrInit` already
   makes. There is no `List`, and a damaged filesystem is never taken for an
   empty bucket, in either mode.
3. **Decided in `blobfs`, in `loadOrInit`.** `blobfs` owns the layout and
   already reads the root pointer, so the refusal costs no extra call, and
   every embedder gets it.
4. **After one read and before any write.** ADR 0008 to ADR 0010 refuse
   before `openStore` because they judge the command line; this refusal
   judges the bucket, which only a read can tell.
5. **The fence is `store.ReadOnly` in `New`, rather than a check on each
   write path.** The type exists for exactly this, and one wrapper also
   covers paths added later. The owner delegated this to the architect's
   recommendation on 2026-10-07.
6. **The contract names the three `store.Store` methods that write.** Reads
   are not restricted.
7. **`Sync` and `Commit` return nil on a read-only FS,** as they do today:
   nothing is buffered or changed there; RFC 1813 lists no `NFS3ERR_ROFS`
   for COMMIT (*Sources*); and `cmd/strata`'s shutdown `Sync` relies on it to
   exit cleanly.
8. **The error text is pinned only as far as a test needs.** There is no
   sentinel error: no caller needs to tell this error apart, and a new
   exported name would make the tests' first run a build error.
9. **`FS.dirty` is a seam for tests** (§7), as `FS.maxFileSize` (ADR 0007
   §7) and `FS.commitInterval` (ADR 0009 §5) are. Without it no test can
   reach a write attempt on a read-only FS, since no path makes one today.
10. **Scope is `internal/blobfs` only.** `-read-only` still lets
    `store.NewLocal` create a missing local directory, and `-check` still
    writes and deletes its probes when `-read-only` is given; both are left
    open (*What this does not decide*). The owner delegated this to the
    architect's recommendation on 2026-10-07.
11. **Not breaking,** under `CLAUDE.md`'s rule for `CHANGES`. A read-only
    deployment that has started once against an empty bucket created a
    filesystem there on that start, so it still starts. What fails now is a
    read-only start against a bucket that nothing has yet given a
    filesystem: a new deployment, or a reader started before its writer. It
    also fails a deployment that starts today but finds no root pointer at a
    later start: storage that does not persist between starts (a container
    path with no volume, a tmpfs directory after a reboot, a scratch bucket
    made per CI run), or a bucket emptied, or a root pointer lost, between
    starts. That is the one case that could fairly be called breaking. It is
    still not marked so: such a deployment serves a filesystem made up at
    that start, which is the defect #68 describes, with no data behind it,
    and in the lost-root-pointer case the old behaviour hid the loss and
    could delete some of the snapshots left. The `CHANGES` line's final
    clause, "only a mount without -read-only creates a filesystem", tells
    anyone in this case what to do. The owner delegated this to the
    architect's recommendation on 2026-10-07.
12. **Status is Accepted on creation,** on the delegation recorded in
    Assumptions 1, 5, 10 and 11. The owner delegated this to the architect's
    recommendation on 2026-10-07.
13. **Read, not run.** Today's behaviour is read from the code at `3dc7c11`,
    #68's account was checked against it, and nothing was reproduced.
14. **Writable mounts are unchanged,** including over a bucket with no root
    pointer (*What this does not decide*).
15. **The 403 is AWS's documented behaviour.** ECS's was not established.

## Alternatives considered

- **Serving an empty filesystem from memory** (Assumption 1).
- **`store.ReadOnly` alone, without the refusal.** `New` would fail with
  `write snapshot: store: operation not supported by backend`, which says
  nothing about the bucket, and only after encoding a snapshot.
- **The refusal alone, without the fence.** An existing filesystem's safety
  would keep resting on every path calling `mutable()` and on `FS.dirty`
  staying false.
- **Checks in `Sync`, `commitLocked`, `putChunk` and `pruneSnapshots`**
  (Assumption 5).
- **Refusing in `cmd/strata`.** It needs `blobfs`'s layout and a second read,
  and covers only the binary.
- **Wrapping in `cmd/strata` only.** It covers only the binary, with the same
  unhelpful error.
- **Waiting for a filesystem to appear.** A mount that hangs at start, and
  following a writer is a feature of its own.
- **Logging a warning and creating anyway.** It still writes.
- **A sentinel error** (Assumption 8).
- **Refusing a bucket with no root pointer in every mode, unless asked to
  create one.** It changes every writable first mount, `README.md`'s demo
  included (*What this does not decide*).

## Consequences

- A read-only FS writes nothing to its bucket, by construction (§3).
- A mistyped bucket, or a lost root pointer, mounted read-only is reported at
  once, with status 1, before strata listens or prints the mount
  instructions, instead of getting a new empty filesystem.
- A read-only mount can no longer create a filesystem. So it can no longer
  win the race to create one, which made a writer's first `New` fail with a
  commit conflict (`commit.go:182-189`) and let the reader's flags set the
  new filesystem's chunk size and owner.
- A reader started before its writer on a new bucket now fails until the
  writer has run once.
- `-uid`, `-gid` and `-chunk-size` shape a filesystem only through a writable
  mount.
- Tests that need a read-only mount still use a bucket that holds a
  filesystem, which `New` now requires.

## What this does not decide

- **`-check` with `-read-only`.** It still writes and deletes probe objects
  (*Context*).
- **The directory `store.NewLocal` creates under `-read-only`** (*Context*).
- **Writable mounts of a bucket with no root pointer.** A mistyped bucket
  gets a filesystem. A filesystem whose root pointer was lost gets a new one
  among its remains, and that filesystem's pruning, which goes by key, can
  delete the old snapshots, or its own rollback window while the old epochs
  sort above it.
- **Telling a lost root pointer from an empty bucket** in §2's error. A
  `List` would tell.
- **`s3://bucket/prefix` dropping the prefix** (`main.go:259`).
- **A read-only mount that follows a writer's commits.**
- **`store.ReadOnly` without `Config.ReadOnly`,** whose behaviour §5 leaves
  as it is.
- **#64, the commit window** (ADR 0006), which needs a truncate, and a
  read-only mount refuses truncates. ADR 0012 decides it.

## Sources

Fetched on 2026-10-07 through a tool that summarises what it fetches, and
quoted as it returned them when asked for verbatim text: once while this
design was planned and again while this ADR was written. The parts fetched
both times matched. The tool keeps a page for 15 minutes, so a second fetch
inside that window may have come from its cache rather than from the site; I
could not tell whether either did.

- RFC 1813, *NFS Version 3 Protocol Specification*, §2.6, from the RFC
  Editor: <https://www.rfc-editor.org/rfc/rfc1813.txt>. "NFS3ERR_ROFS
  Read-only file system. A modifying operation was attempted on a read-only
  file system." (§4)
- RFC 1813 §3.3.21, as freesoft.org renders it:
  <https://www.freesoft.org/CIE/RFC/1813/41.htm>. Its ERRORS, "NFS3ERR_IO
  NFS3ERR_STALE NFS3ERR_BADHANDLE NFS3ERR_SERVERFAULT", and the first
  sentence of its DESCRIPTION, "Procedure COMMIT forces or flushes data to
  stable storage that was previously written with a WRITE procedure call
  with the stable field set to UNSTABLE." (Assumption 7)

  **Honesty note:** the RFC Editor's text, fetched again while this ADR was
  written, came back cut off inside §3.3.7, as ADR 0007 and ADR 0009 found,
  so §3.3.21 is quoted from freesoft.org's rendering alone, a mirror, and so
  is secondhand.
- AWS, *Amazon Simple Storage Service API Reference*, `GetObject`:
  <https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html>. "If
  the object that you request doesn’t exist, the error that Amazon S3 returns
  depends on whether you also have the `s3:ListBucket` permission." "If you
  have the `s3:ListBucket` permission on the bucket, Amazon S3 returns an
  HTTP status code `404 Not Found` error." "If you don’t have the
  `s3:ListBucket` permission, Amazon S3 returns an HTTP status code `403
  Access Denied` error." Under *Errors*, `NoSuchKey`: "The specified key does
  not exist." HTTP status code 404. The apostrophes are the page's own,
  typographic ones. (§6; Assumption 15)
