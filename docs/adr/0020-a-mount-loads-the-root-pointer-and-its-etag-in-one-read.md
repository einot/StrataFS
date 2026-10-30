# 0020. A mount loads the root pointer and its ETag in one read

**Status:** Accepted
**Date:** 2026-10-24
**Revised:** 2026-10-24 — on review, before merge: §4 records the route TLC
reported for `CommitProtocol_NoAtomicLoadPrune` and labels the hand trace;
§6's count of `Head` calls at `55fc666` is one, not two; Assumption 3 no
longer claims that `-check` catches every store the refusal reaches, and
Assumption 4 makes the `CHANGES` entry `BREAKING:`; *What this does not
decide* names a proxy that rewrites a `GET`'s ETag, with its sources; §6's
harness, once `GetWithETag` existed, passes the read through and records
the store's ETag as the loaded ETag beside the body's MD5, which the route
test checks agree; the Consequences bullet on the refusal says what a
read-only mount did before, and Assumption 4 that only the refusal's
`CHANGES` entry is `BREAKING:`. The rule, the Go change and the
configurations are unchanged.
**Revised:** 2026-10-25 — cross-reference only: ADR 0021 decided #91, which
§2 and *What this does not decide* leave to it. The unconditional `Put` and
the `Head` after it now follow a swap answered `store.ErrUnsupported` only on
a mount whose operator set `Config.AllowUnconditionalRoot`; without the
opt-in such a swap fails the commit and writes nothing more. The rule, the
Go change and the configurations here are unchanged.
**Revised:** 2026-10-27 — cross-reference only: ADR 0022 decided #103 and
#5, which §2, Assumption 3 and *What this does not decide* leave to them. A
swap reply with no ETag now leaves the root pointer in doubt, and one read of
it, `GetWithETag`, supplies the ETag before the mount swaps again, so
Assumption 3's asymmetry, a refusal at mount and nothing at a swap, is
closed; §2's refusal at mount stands. The rule, the Go change and the
configurations here are unchanged.
**Revised:** 2026-10-30 — cross-reference only: ADR 0023 decided #43.
`commitLocked`, whose success path §3 and §6 name, is since that ADR the
functions `seal`, `commitSealed` and `applyLocked`, and the success path is
`applyLocked`'s, setting what it set; the swap conditions on the ETag copied
at the seal from `FS.rootETag`. The rule, the Go change and the
configurations here are unchanged.
**Issue:** #89

## Context

**The owner's decisions.** Two, recorded in the owner's words as the session
gave them. Nothing in this ADR claims any approval beyond them.

1. On 2026-10-10 the owner said: "Otherwise, follow architect's and your own
   recommendations." So this ADR is Accepted on creation, as ADRs 0015 to
   0019 were, and its design is the architect's (Assumption 1).
2. Also on 2026-10-10: "We don't have any clients yet so any breaking changes
   are allowed until release." This change adds a method to `store.Store`, an
   interface inside the module that nothing outside it implements, and a
   refusal at mount for a store that gives the root pointer no ETag.
   Assumption 4 says why the refusal gets a `BREAKING:` entry and the
   interface change does not.

**What the code has.** Every statement about the code in this ADR was checked
by reading it at `55fc666`, the commit this ADR's branch was cut from
(Assumption 13). Code is cited by symbol.

- `loadOrInit`, in `internal/blobfs/commit.go`, makes three reads.
  `Get(rootKey)` returns the root pointer's body, which it parses as
  `rootPointer` and checks for `snapshotVersion`; a not-found answer sends a
  read-only mount to its refusal and a writable one to `initEmpty` (ADR 0011
  §1, §2). Then `Head(rootKey)` returns an `ObjectInfo`, whose `ETag` the
  function keeps under the comment "the next commit compare-and-swaps against
  it, so a second mount writing concurrently is detected rather than silently
  clobbering us". Then `Get(rp.Snapshot)` and `decodeSnapshot` give the
  namespace, whose `Epoch` becomes `FS.epoch`, and last
  `f.rootETag = info.ETag`. An empty ETag from the `Head` is kept as it is.
- `commitLocked` swaps the root pointer with
  `PutIfMatch(ctx, rootKey, rpBody, f.rootETag)`; takes
  `store.ErrPrecondition` as another writer's commit, setting `FS.diverged`;
  falls back after `store.ErrUnsupported` to `Put` and then a `Head` whose
  ETag it keeps when the `Head` succeeds; and on success stores the reply's
  ETag, empty or not (#103), as `f.rootETag`. `initEmpty` sets
  `f.rootETag = ""`, which `S3.PutIfMatch` sends as `If-None-Match: *`.
  `mutable()`, in `internal/blobfs/fs.go`, answers `vfs.ErrStale` once
  `FS.diverged` is set (ADR 0003 §4).
- `store.Store`, in `internal/store/store.go`, has eight methods: `Get`,
  `GetRange`, `Head`, `Put`, `Delete`, `List`, `PutIfMatch` and `Name`.
  `ReadOnly` is `struct{ Store }` and overrides only `Put`, `Delete`,
  `PutIfMatch` and `Name`, so every other method is the embedded store's.
- `S3.Get`, in `internal/store/s3.go`, sends `GET` through `S3.do`, which
  sends it again after a transport error, a 5xx or a 429, and returns the
  body of a 200 and nothing of its headers. `S3.Head` sends `HEAD` and
  returns `Content-Length` and `cleanETag(resp.Header.Get("ETag"))`, which
  strips a `W/` prefix and the quotes. `S3.PutIfMatch` sends
  `If-Match: "<etag>"`, or `If-None-Match: *` for an empty one, is never
  retried, and returns `cleanETag` of the reply's `ETag`. `errorFor` maps 404
  to `ErrNotFound`, 412 and 409 to `ErrPrecondition` and 501 to
  `ErrUnsupported`.
- `Local.Get`, in `internal/store/local.go`, is `os.ReadFile`. `Local.Head`
  stats the file and computes `Local.etag`, the lowercase hexadecimal MD5 of
  the file's bytes, read again. `Local.Put` writes a temporary file and
  renames it over the key, so a reader sees one whole body. `Local.PutIfMatch`
  holds `Local.mu`, compares `Head`'s ETag with the one given, and on a match
  writes and returns the MD5 of the new body.
- `checkHeadETag`, in `cmd/strata/check.go`, is a required probe: an endpoint
  whose `Head` returns no ETag fails `-check` with "no ETag returned;
  conditional writes cannot work", and the run ends "This endpoint is missing
  behaviour strata requires. It will not work correctly."
- Three test fakes implement `store.Store` without embedding it, each with a
  `var _ store.Store` assertion: `pr88Store` in
  `internal/blobfs/prune_root_test.go`; `ciStore` in
  `internal/blobfs/commit_interval_test.go`, whose `ciMethods` lists the
  eight methods it counts; and `dr48Store` in
  `internal/blobfs/diverged_race_test.go`.

**The defect.** #89, "A mount's compare-and-swap ETag can belong to a newer
root than the namespace it loaded". Another writer's swap between the `Get`
and the `Head` gives this mount the older namespace and the newer ETag. Its
next `PutIfMatch` then matches, and replaces the other writer's root pointer
with a snapshot that lacks that writer's acknowledged commit. Neither mount
reports an error, and the second-writer detection that `docs/DESIGN.md` §2
and §5 rely on is defeated. `docs/tla/CommitProtocol_LoadRace.cfg` showed it:
TLC's counterexample at `55fc666` has thirteen states. A initialises the
bucket at `[1,1]`; B runs `GetRoot`; A commits `[2,1]`; B runs `HeadRoot`,
taking A's new ETag, then `GetSnapshot`, loading A's epoch-1 commit only; B
commits `[2,2]`, whose swap wins, and A's epoch-2 commit is no longer in the
snapshot the root pointer names (`NoLostAck`). It needs a second writable
mount committing within one round trip of this mount's start, and the same
window opens again after a crash, since a restart is `New` (ADR 0013 §4).

**What the ADRs and documents say.** ADR 0013 Assumption 1 names `AtomicLoad`
for "the window between `loadOrInit`'s `Get` and its `Head` of the root
pointer", §7 and §8 say how a fix lands, spec first, with the defect's
configuration flipped and a MustFail configuration that removes the fix, and
its *What this does not decide* leaves "`docs/DESIGN.md` §5 step 4's
wording, 'the ETag read at mount'" to this ADR. `docs/vocabulary.md` reserves
`AtomicLoad`, V3, as "one read of the root pointer's body and its ETag".
ADR 0019 §1 and Assumption 4 leave #89's residual to this ADR: through the
window a swap can name a key below the one a prune pass was begun for, and
the comments of `CommitProtocol_PruneTwoWriters` and
`CommitProtocol_PruneOverlapTwoWriters` say their bounds hold only by that
assumption. ADR 0018 §3 binds a change that adds state or a decision about
who may act (§5).

**What this constrains.** `store.Store` and its implementations in
`internal/store`; `loadOrInit` in `internal/blobfs/commit.go`;
`docs/tla/CommitProtocol.tla`, its configurations and the tables beside it;
`docs/vocabulary.md`; and `docs/DESIGN.md` §5 step 4. Nothing on the wire, no
flag and no format changes.

## Decision

### 1. The rule

The ETag a mount compare-and-swaps the root pointer against is always the
ETag of the root pointer body its namespace descends from: before its first
commit, the ETag that came in the same response as the body `loadOrInit`
loaded; after a commit, the ETag that commit's own swap returned, as now. A
mount never takes the ETag from a read other than the one that gave it the
body.

**Why that closes #89.** A write of one key is atomic and gives the object a
fresh ETag (ADR 0013 §4, Assumption 6), and a read returns one body together
with that body's ETag (§2; *Sources*). So the pair the mount loads describes
one root pointer, and the epoch of the snapshot it names is that root
pointer's. If another writer swaps the root pointer after the read, the ETag
changes, the mount's `If-Match` fails, and the mount diverges, as
`docs/DESIGN.md` §5 intends; the other writer's commit stands. No write can
fall between the body and the ETag, because there is no second read for it to
fall between. ADR 0019 §1's step 2, that the key the root pointer names never
falls below a prune pass's, then rests on nothing about the load: the one
route by which a writer's epoch could lag the ETag it holds is gone (§4).

### 2. The Go change

**The store interface.** `store.Store` gains a ninth method, which every
implementation provides:

```go
// GetWithETag returns the whole object together with the ETag of the body it
// returns, both taken from one response, so that the pair is consistent
// however the object is being replaced meanwhile: the ETag is the one a later
// PutIfMatch of the key compares against for exactly this body. A missing key
// is ErrNotFound. A backend that gives no ETag returns "" and no error.
GetWithETag(ctx context.Context, key string) (data []byte, etag string, err error)
```

- **`S3.GetWithETag`** sends one `GET`, through `do` with the retries `Get`
  has, and on a 200 returns the body and `cleanETag(resp.Header.Get("ETag"))`;
  any other status goes to `errorFor("GET", key, resp)`, as `Get`'s does. A
  retried `GET` returns a later body with that body's ETag, so the pair stays
  consistent. Its doc comment carries
  `TLA+: docs/tla/CommitProtocol.tla GetRoot`.
- **`Local.GetWithETag`** reads the file once, as `Get` does, maps a missing
  file to `ErrNotFound`, and returns the bytes it read with the lowercase
  hexadecimal MD5 *of those bytes*: `Local`'s ETag (`Local.etag`), computed
  from the body returned and not from a second read of the file. It takes no
  lock: `Put` is a rename, so one read sees one whole body (Assumption 6).
  The same `TLA+:` line.
- **`ReadOnly`** gets nothing. It embeds `Store`, so the method is promoted,
  and a read is not a mutation (ADR 0011 §3).
- **`Get`, `Head` and every other method** keep their signatures and
  behaviour. `Get` may be implemented through `GetWithETag`, dropping the ETag
  (Assumption 7).

**`loadOrInit`** replaces its first two reads with one:

1. `raw, etag, err := f.store.GetWithETag(ctx, rootKey)`. `store.ErrNotFound`
   takes the branches it takes now, the read-only refusal or `initEmpty`; any
   other error is returned as `read root pointer: %w`, as now.
2. The parse and the version check, as now.
3. **A root pointer read with no ETag fails the mount.** If `etag == ""`,
   `loadOrInit` returns an error, before any further store call, whose text
   contains `no ETag` and the store's name, taken as the not-found branch
   takes it: the embedder's store, not `ReadOnly`'s wrapper. `New` then
   returns that error and a nil `*FS`, having written nothing. Assumption 3
   says why this fails closed where `commitLocked` does not.
4. `Get(rp.Snapshot)`, `decodeSnapshot`, the adoption of the chunk size and
   the assignments, as now, with `f.rootETag = etag`.

The `Head(rootKey)` call and its `stat root pointer` error go, and
`loadOrInit` makes no `Head` of the root pointer on any path. Its `TLA+:` line
becomes `GetRoot, RefuseEmpty, InitEmpty, GetSnapshot`. The comment above the
read says what §1 says. Logs are not pinned.

**The fallback for a backend without conditional writes.** Nothing on it
changes: the loaded ETag goes to `PutIfMatch`, which answers
`store.ErrUnsupported`, and the unconditional `Put` and the `Head` after it
run as now. The ETag's provenance is irrelevant on that path, whose guarantee
is a single writer's (`docs/DESIGN.md` §2), and #91 decides the fallback's
policy. Step 3's refusal applies on that backend too (Assumption 3). ADR 0021
has since decided #91: the unconditional `Put` and the `Head` follow a swap
answered `store.ErrUnsupported` only on a mount whose operator set
`Config.AllowUnconditionalRoot`, and otherwise the commit fails with nothing
more written.

### 3. What does not change

`initEmpty`; `commitLocked`, its success path, its treatment of a swap reply
with no ETag (#103) and its fallback; `schedulePrune` and `pruneSnapshots`;
`Get`, `GetRange`, `Head`, `Put`, `Delete`, `List`, `PutIfMatch` and `Name`;
`cmd/strata`, the probes of `-check` included (Assumption 11); the formats of
the root pointer and the snapshot; every flag. `FS.rootETag` keeps its
meaning, the root pointer's ETag as last read or written, with `""` for no
root (`docs/vocabulary.md`, V2).

### 4. The model

In `docs/tla/CommitProtocol.tla`, under a `Revised: 2026-10-24` line:

- `Fixes` gains the member **`AtomicLoad`** (`docs/vocabulary.md`, V3), and
  `AllFixes` lists it.
- With the member, `GetRoot(m)` records `rootETag[m]` from the root pointer's
  ETag beside `rpSnapshot[m]`, clears `overtaken[m]`, and moves `pc[m]` to
  `"GetSnapshot"`, so `"HeadRoot"` is never due and `HeadRoot` is never
  enabled. Without it, `GetRoot` moves to `"HeadRoot"` and `HeadRoot`
  assigns the ETag, as at `55fc666`. R2 is reworded for both cases; J11 and
  J13 are rewritten; the Leaves-out list names a read that returns no ETag,
  which the model does not have, since every root pointer has one (J3), and
  which with the member makes `New` fail (§2).
- **`CommitProtocol_LoadRace` flips to MustHold**, `AtomicLoad` added to its
  `Fixes`, its two invariants and every other line unchanged (ADR 0013 §7).
  Its comment records the flip and the route TLC reported.
- **`CommitProtocol_NoAtomicLoad`**, MustFail on `NoLostAck`, is
  `CommitProtocol_LoadRace` without the member: the configuration that
  removes the fix (ADR 0013 §7). Its route is the one TLC reported at
  `55fc666`.
- **`CommitProtocol_PruneTwoWritersFromEmpty`**, MustHold on every property:
  two writers from an empty bucket, each pruning at `Retention` 1, with
  `Nonces` `{1, 2, 3}`, `MaxEpoch` 2, the faults `SwapFailed`, `PutFailed`
  and `SnapshotReplyLost`, one crash, and both `PruneBelowCurrent` and
  `AtomicLoad`. It is the first MustHold configuration in which two writers
  load from a bucket the other writes, and it widens the envelope J11 records
  (Assumption 8).
- **`CommitProtocol_NoAtomicLoadPrune`**, MustFail on `RootSnapshotExists`:
  the bounds of the one above, narrowed to the fault `SwapFailed` and no
  crash, without `AtomicLoad`. It shows the residual ADR 0019 §1 and
  Assumption 4 left to this fix, and that three nonces alone do not reach it.
  The route TLC reported, at `0282bbf`, in 20 states: A initialises `[1,1]`,
  which begins a pass; A's attempt `[2,2]` fails its swap without taking
  effect, and A puts its retry `[2,3]`; A's first pass lists and ends with
  nothing to delete; B runs `GetRoot`; A's swap to `[2,3]` lands and begins a
  pass for it; B runs `HeadRoot`, taking A's ETag, and `GetSnapshot`, loading
  epoch 1, then puts `[2,1]`; A's pass lists, keeps `[2,2]`, A's own orphan,
  as the newest key below `[2,3]`, and deletes `[1,1]` and then `[2,1]`; B's
  swap then names `[2,1]`, which is gone. A hand-traced route of the same
  length, which TLC did not report first, has the orphan as B's own failed
  attempt `[2,2]` and B's retry `[2,1]` swapping in before the pass lists, so
  that the pass deletes the snapshot the root pointer already names; on that
  route `NoLostAck` fails first, at B's swap, while on TLC's it never fails,
  since the root pointer names a key the bucket no longer holds. The
  configuration checks `RootSnapshotExists` because that is the residual
  ADR 0019 named (Assumption 9).
- **No other existing configuration changes its `Fixes`.** ADR 0013 §7 keeps
  configurations append-only, with the flip as the only change an existing
  one takes, and this ADR does not revise it. So `CommitProtocol_PruneTwoWriters`,
  `CommitProtocol_PruneOverlapTwoWriters`, `CommitProtocol_InitRace`,
  `CommitProtocol_Follower` and `CommitProtocol_NoReadOnlyFence`, the
  configurations in which a mount loads from a bucket another mount writes,
  keep describing the load as it was at `55fc666`. Each remains sound: a
  configuration without the member admits every behaviour the fixed code has
  and more, so a MustHold that holds over it holds over the code. The
  comments of the two prune configurations now say so, and name the two new
  configurations as where the window is shown closed and the residual shown
  (Assumption 10).
- In `docs/tla/manifest.tsv` the flipped row and the three new rows have
  `exit` and `log` `-` until the session has run them (ADR 0013 §7). In
  `docs/tla/trace.tsv`, `GetRoot` gains rows for `S3.GetWithETag` and
  `Local.GetWithETag`, `FS.loadOrInit`'s row for it cites this section, and
  `HeadRoot`'s row goes, since no Go symbol performs it once the member is in
  the code (ADR 0013 §6; Assumption 12). The module's `Checked against` is
  `55fc666` (Assumption 13).

### 5. ADR 0018

- **No state the standing rule reaches is added or changed.** `FS.rootETag`
  is a value, an ETag string, not a named state (ADR 0018 §2), and this ADR
  changes which read supplies it, not what it means (§3). The root-pointer
  knowledge machine, V2's `FS.rootKnowledge`, stays reserved for the ADR for
  #5: this ADR neither adds it nor decides anything it will decide, since the
  value that machine would guard keeps its meaning. No transition is added or
  changed, so nothing is security-relevant under ADR 0018 §5, and no
  transition asks a policy.
- **The refusal in §2 step 3 is not a policy.** ADR 0018 §2's policy rule
  reaches a decision about whether a caller may do something. The refusal is a
  step the server takes on a store's answer, a reply with no ETag, which §2
  keeps outside the rule: it judges no caller and nothing the mount allows.
- So nothing is converted and no exemption is granted;
  `docs/adr/exemptions.md` is unchanged.

### 6. The test surface

Every name is spelled as in Go. test-author cannot read `internal/store`'s
implementation files or `internal/blobfs`'s non-test files, so everything a
test may use is here.

**Names a test may use**, restated from the code at `55fc666` unless marked
new:

```go
// internal/blobfs
func New(ctx context.Context, cfg Config) (*FS, error)
type Config struct {
    Store              store.Store
    ChunkSize          uint32
    OwnerUID, OwnerGID uint32
    ReadOnly           bool  // ADR 0011
    SnapshotRetention  int   // 0 selects the default, 10; negative turns pruning off
    MaxDirtyBytes      int64 // negative disables the budget's wait
    Log                *slog.Logger
    // ... fields this ADR does not use
}
func (f *FS) Sync(ctx context.Context) error
func (f *FS) Root() vfs.Handle
// Create, Lookup and the other vfs.FS methods, as internal/vfs/vfs.go declares them
func quietLog() *slog.Logger // internal/blobfs/fs_test.go: a logger that discards everything

// unexported
const (
    rootKey        = "root"
    snapshotPrefix = "snapshots/"
    rootInodeID    = 1
)
type rootPointer struct {
    Version   int    `json:"version"`
    Epoch     uint64 `json:"epoch"`
    Snapshot  string `json:"snapshot"`
    FSID      uint64 `json:"fsid"`
    WrittenNS int64  `json:"written"`
    Writer    string `json:"writer,omitempty"`
}
func decodeSnapshot(b []byte) (*snapshot, error) // the bytes of an object under snapshotPrefix
type snapshot struct {
    Version   int
    Epoch     uint64
    NextIno   uint64
    ChunkSize uint32
    Inodes    map[uint64]*inode
    CreatedNS int64
}
type inode struct {
    ID      uint64
    Entries map[string]uint64 // a directory's names to inode IDs; the root directory is Inodes[rootInodeID]
    // ...
}
```

```go
// internal/store
type Store interface {
    Get(ctx context.Context, key string) ([]byte, error)
    GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error)
    Head(ctx context.Context, key string) (ObjectInfo, error)
    Put(ctx context.Context, key string, data []byte) error
    Delete(ctx context.Context, key string) error // a missing key is not an error
    List(ctx context.Context, prefix, after string, max int) ([]ObjectInfo, error)
    PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error) // etag "" means create-only
    Name() string
    GetWithETag(ctx context.Context, key string) (data []byte, etag string, err error) // new in this ADR (section 2)
}
type ObjectInfo struct { Key string; Size int64; ETag string }
var ErrNotFound, ErrPrecondition, ErrUnsupported error
func NewLocal(root string) (*Local, error) // *Local implements Store
type ReadOnly struct{ Store }               // implements Store; refuses Put, Delete and PutIfMatch with ErrUnsupported
func NewS3(cfg S3Config) (*S3, error)       // *S3 implements Store
type S3Config struct {
    Endpoint  string // e.g. an httptest.Server's URL
    Bucket    string
    Region    string
    Creds     Credentials
    PathStyle bool // true: requests go to /<bucket>/<key> on Endpoint's host
    Timeout   time.Duration
}
type Credentials struct{ AccessKeyID, SecretAccessKey, SessionToken string }
```

**Facts a test may rely on.**

- On a `*store.Local`, an object's ETag is the lowercase hexadecimal MD5 of
  its body, the same from `Head`, from `GetWithETag` and as `PutIfMatch`
  returns it; `PutIfMatch` compares the ETag it is given with that of the
  current body (ADR 0013 Assumption 6; *Context*). So a fake over a
  `*store.Local` may answer `GetWithETag` with the body `inner.Get` returned
  and `hex.EncodeToString(md5.Sum(body))` as its ETag: that is the store's
  own answer, computed from the bytes returned, and it never races. It stood
  in for `inner.GetWithETag` until the method existed; the harness now passes
  the call through and records both, the store's ETag and that MD5, and the
  route test checks that they agree (Assumption 14).
- A `*store.S3` built with `PathStyle` true sends `GET /<bucket>/<key>` and
  `HEAD /<bucket>/<key>` to `Endpoint`, with a SigV4 `Authorization` header a
  fake server may ignore. On a 200 it reads the body and the `ETag` header,
  stripping the quotes and a `W/` prefix; a 404 is `ErrNotFound`, whatever
  the body. It sends a `GET` or a `HEAD` again after a 5xx or a 429, with a
  wait that starts at 200 ms, so a test's fake server answers neither.
- Once a mount has diverged, which `Sync` reports by returning a non-nil
  error from a swap whose condition failed, every change on it, `Create`
  included, returns `vfs.ErrStale` (ADR 0003 §4; `docs/vocabulary.md`, V1).
- `vfs.FS` methods take a `vfs.Caller`; `Create` with `vfs.SetAttr{}` and
  exclusive true makes a regular file. `vfs.Caller{UID: 501, GID: 20}` is
  the root directory's owner when `Config` has `OwnerUID` 501 and `OwnerGID`
  20.

**The harness.** A `store.Store` over a `*store.Local` under `t.TempDir()`,
written without embedding `store.Store`, that passes every call through to
the inner store and, in addition:

- implements `GetWithETag` by passing the call through to the inner store's
  `GetWithETag`, with the hold below (at `55fc666`, before the method
  existed, it computed the ETag as the fact above says: Assumption 14);
- **holds the next read of `rootKey`, by `Get` or by `GetWithETag`, after
  the inner call has returned and before the harness returns**, when armed,
  one-shot: it records as the *loaded ETag* the ETag the inner `GetWithETag`
  returned, or, for a `Get`, the lowercase hexadecimal MD5 of the body it is
  about to return, and records that MD5 of the body in either case; then
  signals that it is held; waits until released or until the test's context
  ends; then returns the inner call's result. So the hold falls where #89's
  window is, whichever read a build makes first: with this ADR that read is
  `GetWithETag(rootKey)`, and at `55fc666`, or in a build that regresses to
  it, `Get(rootKey)`;
- records the `etag` argument of every `PutIfMatch` of `rootKey` it passes
  through, in call order, and counts every `Head` of `rootKey`;
- in its **no-ETag mode**, answers `Head(rootKey)` with the inner store's
  `ObjectInfo` and an empty `ETag`, and `GetWithETag(rootKey)` with the inner
  body and an empty ETag, and passes every other key through.

Two mounts in one test share one harness. A *commit* is a `Create` of a
fresh name in the root directory, by `vfs.Caller{UID: 501, GID: 20}`, then
`Sync`.

**The route test**, from `CommitProtocol_LoadRace`'s counterexample
(`TestCommitBetweenMountReadsIsRefusedNotOverwritten`, in
`internal/blobfs/load_race_test.go`; red at `55fc666`). Every mount has
`SnapshotRetention` −1 and `MaxDirtyBytes` −1.

1. `New` of mount A on the harness: A initialises the bucket (`InitEmpty`,
   `PutSnapshot` `[1,1]`, `SwapOK`).
2. Arm the hold. Start `New` of mount B on the harness in a goroutine; wait
   for the hold's signal: B's `GetRoot` has read the epoch-1 root pointer and
   is held.
3. Commit on A, name `a2`: `Sync` returns nil (A's `Mutate`, `PutSnapshot`
   `[2,1]`, `SwapOK`).
4. Release the hold; B's `New` returns a non-nil `*FS` and a nil error, under
   the bound. At `55fc666` B now runs `HeadRoot`, taking A's ETag; with this
   ADR B holds the ETag of the body it loaded.
5. Commit on B, name `b2`. **`Sync` returns a non-nil error**: B's swap fails
   its condition, since A moved the root pointer after B's read. Then a
   `Create` of `b3` on B returns `vfs.ErrStale`.
6. The *loaded ETag* equals the `etag` argument of the last `PutIfMatch` of
   `rootKey` the harness saw, B's: the ETag swapped against belongs to the
   body B loaded. And the loaded ETag equals the MD5 the hold recorded of
   the body it returned, by the first fact: the ETag the store gave B is
   that body's.
7. The harness counted no `Head` of `rootKey`.
8. Through the inner `*store.Local`: `Get(rootKey)` decodes as `rootPointer`,
   `Get` of its `Snapshot` decodes with `decodeSnapshot`, and the root
   directory's `Entries` hold `a2` and not `b2`: A's acknowledged commit is
   in the snapshot the root pointer names. A remount, `New` on the inner store
   with `SnapshotRetention` −1, succeeds, and `Lookup` of `a2` in its root
   directory succeeds.

At `55fc666` steps 5, 6, 7 and 8 fail: B's `Sync` returns nil, the last swap
carries A's ETag, `Head` of `rootKey` was called once, by B's load (A's
`New` found no root pointer and made no `Head`), and the root pointer's
snapshot holds `b2` and not `a2`. Where the test deviates from the trace,
and why its hold is where it is, is Assumption 14.

**The refusal** (`TestNewRefusesRootPointerWithoutETag`, same file; red at
`55fc666`). Seed a filesystem: `New` on the inner `*store.Local` with
`SnapshotRetention` −1. Then, on the harness in no-ETag mode, for
`Config.ReadOnly` false and for true: `New` returns a nil `*FS` and a
non-nil error whose text contains `no ETag` and `cfg.Store.Name()`; the
harness saw no `Put`, `Delete` or `PutIfMatch`; and the bytes of `rootKey`
on the inner store are as they were. At `55fc666` `New` returns an `*FS` and
no error in both cases.

**The fakes.** In the same round, `pr88Store`, `ciStore` and `dr48Store`
gain `GetWithETag`, implemented as the fact above says over their inner
store, so that each still satisfies `store.Store` once the method exists;
`ciStore` counts it and `ciMethods` lists it as the ninth method. At
`55fc666` an extra method on a fake changes nothing, so the package still
builds and the two tests above fail rather than fail to build. These
stand-ins were replaced by `inner.GetWithETag(ctx, key)` once the code
existed (Assumption 14).

**Tests of the new method**, which cannot build at `55fc666` and are written
once the code exists, in `internal/store/get_with_etag_test.go`:

- **S1** (`TestLocalGetWithETagReturnsBodyAndItsETag`). On a `*store.Local`
  under `t.TempDir()`: after `Put` of a body, `GetWithETag` returns that body
  and an ETag equal to `Head`'s and to the lowercase hexadecimal MD5 of the
  body; `PutIfMatch` of a new body with that ETag succeeds, and `GetWithETag`
  then returns the new body with the MD5 of it; `GetWithETag` of a key never
  put returns `ErrNotFound`.
- **S2** (`TestS3GetWithETagTakesETagFromGetResponse`). An `httptest.Server`
  standing for S3 answers `GET /b/root` with status 200, header
  `ETag: "etag-from-get"` and a body, and `HEAD /b/root` with status 200,
  `Content-Length` and `ETag: "etag-from-head"`; it answers `GET /b/missing`
  with status 404 and the body `<Error><Code>NoSuchKey</Code></Error>`. A
  `*store.S3` from `NewS3(S3Config{Endpoint: server.URL, Bucket: "b",
  Region: "us-east-1", Creds: Credentials{AccessKeyID: "k",
  SecretAccessKey: "s"}, PathStyle: true})`: `GetWithETag(ctx, "root")`
  returns the body and `etag-from-get`, not `etag-from-head`, with the quotes
  stripped; with the header `ETag: W/"weak"` it returns `weak`;
  `GetWithETag(ctx, "missing")` returns `ErrNotFound`; and `Get(ctx, "root")`
  still returns the body.
- **S3** (`TestReadOnlyPassesGetWithETagThrough`).
  `store.ReadOnly{Store: local}.GetWithETag` returns what `local.GetWithETag`
  returns, for a key that exists and for one that does not; its `Put` still
  returns `ErrUnsupported`.

**Safety rules**, as ADR 0011 §7 and ADR 0012 §9 give them:

- every `New` and every FS call runs in a goroutine that recovers a panic and
  is waited for under a bound of 10 s; goroutines never touch `t`; nothing is
  parallel;
- every hold the harness takes selects on the test's context, which cleanup
  cancels, and a cleanup releases every hold, so no build can hang the suite;
- every mount has `SnapshotRetention` −1 and `MaxDirtyBytes` −1, and no
  `Write` is made, so no pass and no drain runs;
- no sleep, and no polling: every wait is on a channel under the bound;
- every bucket is under `t.TempDir()`, and every fake server is an
  `httptest.Server` closed by cleanup;
- the bucket is inspected through the inner `*store.Local` after the
  harness's records have been read, never through the harness.

**What only review can show:**

- that `loadOrInit` takes the body and the ETag from one `GetWithETag` call,
  makes no `Head` of the root pointer on any path, and refuses an empty ETag
  before its snapshot read (§2);
- that `S3.GetWithETag` takes the ETag from the response that carried the
  body, and `Local.GetWithETag` hashes the bytes it returns;
- that `ReadOnly` overrides nothing new (§2);
- that `commitLocked`, `initEmpty` and the fallback are unchanged (§3);
- the `TLA+:` lines, the `trace.tsv` rows, and that `GetRoot` with the
  member describes `loadOrInit` (ADR 0013 §8);
- that the fakes' stand-ins were replaced once the method existed.

## Assumptions

Each is mine unless it says otherwise.

1. **Status Accepted on creation**, under the owner's 2026-10-10 words, as
   ADRs 0015 to 0019 were. The design is mine: the brief asked for the ETag
   swapped against to come from the same response as the body, and left the
   interface to this ADR.
2. **The method's name and shape** (§2). `GetWithETag` says what it adds to
   `Get`; it returns the ETag as a string, as `PutIfMatch` does, rather than
   an `ObjectInfo`, so that a caller cannot read a size or a key it did not
   ask for and a fake has one value to get right. It is a new method rather
   than a change to `Get`, so that `Get`'s callers, the chunk reads and
   `-check` among them, and every test that calls `Get`, stay as they are.
3. **A read with no ETag fails the mount** (§2). An empty `FS.rootETag` means
   "no root" (V2), and a mount of an existing root pointer must not hold a
   value with that meaning: at `55fc666` it does, and its first commit then
   sends `If-None-Match: *`, which a backend with conditional writes answers
   412, so the mount diverges, blaming "another writer" that does not exist,
   and a backend without them takes through the fallback. Refusing at `New`
   costs nothing, since nothing is yet at stake, and names the cause.
   `-check` fails an endpoint whose `HEAD` gives no ETag, on a required
   probe, but it does not probe `GET`'s, so an endpoint, or a proxy in front
   of one, that gives an ETag on `HEAD` and none on `GET` passes `-check`,
   mounted at `55fc666`, and is refused now (Assumption 4; #133 asks for the
   `GET` probe). `commitLocked` keeps storing a swap
   reply's ETag as it is, empty included: after a swap that took effect the
   commit is durable, and refusing it would be wrong; what the mount should
   do then is #103's question, with #5's in-doubt knowledge the likely tool,
   and this ADR decides neither. The asymmetry is deliberate.
4. **`BREAKING:`, and the `CHANGES` entry.** No client, flag, wire format or
   object format changes, and the `store.Store` change is inside the module.
   The refusal, though, reaches a running deployment whose store, or a proxy
   in front of it, gives the root pointer an ETag on `HEAD` and none on
   `GET`: it mounted at `55fc666`, read-only or not, and fails at start-up
   now, and nothing in `-check` tells such a deployment apart until #133
   lands (Assumption 3). CLAUDE.md prefixes `BREAKING:` when a running
   deployment needs action to keep working; one on such a store does, so the
   refusal's entry is prefixed and says what to check, while the race fix,
   which asks nothing of a deployment, has an unprefixed entry of its own
   (CLAUDE.md: one line per change). That no such deployment is
   known is not a reason to leave the prefix off: the rule asks whether
   action is needed, not whether we know who needs it. A first draft of this
   ADR left it off on the claim Assumption 3 now narrows; review caught it.
5. **The ETag a `GET` returns is the one `If-Match` compares against**
   (§1; *Sources*). HTTP defines `ETag` as "the current entity tag for the
   selected representation" and evaluates `If-Match` against "the entity tag
   of the selected representation"; S3 documents the `GetObject` `ETag` as
   an identifier "assigned by a web server to a specific version of a
   resource", `HeadObject`'s response as "identical to the `GET` response
   except that there is no response body", and `PutObject`'s `If-Match` as
   matching "the ETag of the object in S3". That S3 and the target cluster
   answer a `PUT` conditioned on a `GET`'s ETag as they do on a `HEAD`'s is
   the judgement; `-check` probes `HEAD`'s ETag today, and #133 asks for a
   probe of `GET`'s.
6. **`Local.GetWithETag` takes no lock** (§2). `Local.PutIfMatch` holds
   `Local.mu` so that its `Head` and its `Put` are one step against other
   conditional writes; a read needs no such pairing, since `Put` renames a
   whole file into place and the ETag is computed from the bytes read.
7. **`Get` may delegate to `GetWithETag`** (§2). Behaviour is pinned, not
   structure; coder chooses.
8. **The new MustHold configuration's bounds and tier** (§4). `MaxEpoch` 2
   and `Nonces` `{1, 2, 3}` are the least that admit the residual's route;
   the three faults are those `CommitProtocol` and
   `CommitProtocol_PruneFaultsSingle` use, since each other fault makes a
   mount diverge with no other writer (#5) and so cannot join a configuration
   that checks `NoSelfDivergence`; one crash reopens the load window after a
   restart, which no other MustHold configuration with two writers reaches.
   Tier `full`, since two writers from an empty bucket with a crash explore
   more than any configuration so far. If it runs too long, the session
   reports it and the bounds shrink in a later dispatch, as ADR 0019
   Assumption 11 provided for its own.
9. **`CommitProtocol_NoAtomicLoadPrune`'s property and bounds** (§4). It
   checks `RootSnapshotExists`, not `NoLostAck`, because the point is the
   residual ADR 0019 §1 left, which `CommitProtocol_NoAtomicLoad` does not
   show; a MustFail configuration checks one property, and on the route TLC
   reported `NoLostAck` does not fail at all. Its bounds are the smallest I
   traced a route in: `SwapFailed` alone, no crash, tier `smoke`. The route I
   traced by hand was a prediction (Assumption 13); TLC confirmed the
   violation at the same depth by another interleaving, and §4 records both,
   TLC's as the configuration's.
10. **No landed fix is added to an existing MustHold configuration** (§4).
    ADR 0013 §7 says the flip is the only change an existing configuration
    takes, this ADR's write scope does not include ADR 0013, and ADR 0019
    read the rule the same way for `PruneBelowCurrent`. The cost is that five
    configurations model a load the code no longer makes; they stay sound,
    as §4 says, and their comments say which configurations carry the
    member. If the owner wants landed fixes added to such configurations, a
    `Revised` line on ADR 0013 §7 decides it, not this ADR.
11. **`-check` is unchanged** (§3). The brief pins this fix to
    `internal/store` and `loadOrInit`. A probe that conditions a `PUT` on a
    `GET`'s ETag would check Assumption 5 against the endpoint in front of
    the operator; #133, filed from this ADR's report, asks for it.
12. **`HeadRoot`'s trace row goes now** (§4). The trace describes the code
    this branch lands, spec first, as ADR 0019's `prunable` row did: the
    tripwire checks that each named symbol exists, and will fail on
    `S3.GetWithETag` and `Local.GetWithETag` until coder's change is on the
    branch, which is the order ADR 0013 §8 sets.
13. **Read, not run.** Every statement about the code was checked by reading
    it at `55fc666`; nothing was run: not TLC, not `go test`. The flip, the
    new rows' outcomes and the routes are predictions the session's runs
    confirm or refute. `Checked against` is `55fc666` because that is the
    code the trace was checked against; once coder's change is on the branch,
    the architect bumps it to that commit and marks `AtomicLoad` *exists* in
    `docs/vocabulary.md`, as was done for ADR 0019.
14. **Where the Go tests deviate from the trace, and the stand-in** (§6).
    The trace's window is between `GetRoot` and `HeadRoot`; a test cannot
    hold `HeadRoot` without failing to find it once the fix lands, so the
    harness holds the first read of the root pointer after it has read,
    where every build has a read. At `55fc666` the harness's `GetWithETag`
    computed `Local`'s ETag from the bytes it returned rather than calling
    `inner.GetWithETag`, because that method did not exist there and one
    reference to it would have stopped the package's tests from building,
    hiding the red run. The stand-in was exact, not approximate: it is
    `Local`'s own ETag rule. Once the code existed, the harness and the
    fakes were switched to the passthrough, so that they describe the store
    and not a copy of its rule; the harness still records the body's MD5
    beside the ETag the store returned, and the route test checks that the
    two agree, so the rule is exercised against the store's own answer, and
    a build that regresses to `Get` is still held, with the MD5 as its
    loaded ETag.
15. **The error's two parts** (§2, §6). `no ETag` names the cause and the
    store's name says where, as ADR 0011 §7's refusal does; the rest of the
    text is not pinned.
16. **The refusal is tested read-only too** (§6), since `ReadOnly` promotes
    the method and a read-only mount of such a store would otherwise serve a
    namespace with a false "no root".
17. **`ciMethods` gains the method in the first round** (§6), so that
    ADR 0009's test, which asserts that a refused `New` makes no call of any
    `store.Store` method, keeps covering every method.
18. **The trust boundary.** The new path reads the same header, from the same
    server, that `Head` reads today, and the value flows where `Head`'s did:
    into `If-Match`, quoted. Nothing new is parsed, and no body size bound
    changes (`Get` has none either). The report recommends a short security
    review all the same, because the change is on the object store's edge.

## Alternatives considered

- **Change `Get` to return the ETag.** Every caller of `Get`, the chunk
  reads, `-check`, and every test that calls it, would change for a value
  only `loadOrInit` needs (Assumption 2).
- **An optional interface** that `loadOrInit` type-asserts, falling back to
  `Get` then `Head`. The fallback is the defect; a backend that lacked the
  method would keep it.
- **Read, `Head`, read again, and compare the bodies.** Sound, given that no
  two writes of the root pointer have the same body (ADR 0013 Assumption 6):
  an unchanged body across the three calls proves the `Head` fell on it. But
  it costs a third round trip at every mount and rests on a format property
  where the response already carries the answer.
- **A conditional `GET`**, `If-Match` on the `Head`'s ETag, retried on 412.
  Two calls and a retry loop for what one response gives.
- **Keep an empty ETag as today**, storing `""` and letting the swap decide.
  Assumption 3: it misreports a second writer, or goes through the fallback
  on an endpoint `-check` calls unsupported.
- **Warn and continue on an empty ETag.** The same outcomes, with a log line.
- **Add `AtomicLoad` to every configuration whose mounts load.** Assumption
  10: ADR 0013 §7 forbids it, and this ADR cannot revise that.
- **Make the window a state of a machine**, so that the standing rule covers
  it. There is no state: the fix removes a step, and leaves one value with
  its meaning (§5).

## Consequences

- A commit by another mount that lands while a mount is starting is refused,
  not overwritten: the mount diverges, as `docs/DESIGN.md` §5 intends, and
  the other mount's acknowledged commit stands.
- Mounting makes one read of the root pointer where it made two; the rest of
  `New` is as it was.
- ADR 0019 §1's argument that the root pointer's key only rises no longer
  carries a residual; the model shows the residual without the member and its
  absence with it.
- `store.Store` has nine methods, and every fake of it implements one more.
- A bucket whose store gives the root pointer no ETag on `GET` cannot be
  mounted, read-only or not. Before, a read-only mount of it served normally
  in every case, since it never commits; a writable mount worked when `HEAD`
  gave an ETag, and when `HEAD` gave none either it misreported a second
  writer at its first commit, or wrote the root pointer unconditionally on a
  store without conditional writes (Assumption 4).
- Hard to change later: that `GetWithETag` is the only read a mount takes its
  first ETag from, which the redesign's `fsinfo` load (`docs/DESIGN.md` §5)
  inherits.

## What this does not decide

- A swap reply with no ETag (#103), and the in-doubt knowledge of #5: what a
  mount does after a swap whose ETag it does not have. Assumption 3 names
  the asymmetry this leaves.
- The fallback's policy on a backend without conditional writes (#91):
  decided by ADR 0021.
- A `-check` probe that conditions a `PUT` on the ETag a `GET` returned, so
  that Assumption 5 is checked on the endpoint in front of the operator
  (Assumption 11): #133.
- A proxy between strata and its store that rewrites or removes the ETag of
  a compressed `GET` response. Go's transport asks for `gzip` on a `GET` it
  sends and not on a `HEAD` (*Sources*), so a proxy that compresses the
  reply and alters its ETag, as Apache's `mod_deflate` does by default,
  appending the compression method, hands the mount an ETag the store does
  not recognise, and one that removes the header hands it none. The first
  makes the mount's first `If-Match` fail, so it diverges with no other
  writer: fail-closed, with the wrong cause named. The second is §2's
  refusal (Assumption 4). #133's probe covers both if it conditions a `PUT`
  on the ETag of a `GET` made through the same client the mount uses;
  whether the client should ask for identity encoding instead is that
  issue's, or a new one's. security-auditor raised the case; the two
  sources confirm its mechanism.
- Whether ADR 0013 §7 should let a landed fix join an existing MustHold
  configuration (Assumption 10).
- Multi-writer operation beyond detection (`docs/DESIGN.md` §15).

## Sources

Each was fetched on 2026-10-24, while this ADR was written or revised,
through a tool that summarises what it fetches and reads a long page 100,000
characters at a time, and quoted as it returned them when asked for verbatim
text. Where the tool shortened a quotation to about 125 characters, the
entry says so.

- AWS, *Amazon S3 API Reference*, `GetObject`:
  <https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html>. The
  response syntax lists `ETag: {{ETag}}`; of the `ETag` response header: "An
  entity tag (ETag) is an opaque identifier assigned by a web server to a
  specific version of a resource found at a URL."; of the `If-Match` request
  header: "Return the object only if its entity tag (ETag) is the same as the
  one specified in this header; otherwise, return a `412 Precondition Failed`
  error." (§1, Assumption 5)
- AWS, *Amazon S3 API Reference*, `HeadObject`:
  <https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html>. "A
  `HEAD` request has the same options as a `GET` operation on an object. The
  response is identical to the `GET` response except that there is no
  response body."; the same `ETag` description as `GetObject`'s. (§1,
  Assumption 5)
- AWS, *Amazon S3 API Reference*, `PutObject`:
  <https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html>. Of
  `If-Match`: "Uploads the object only if the ETag (entity tag) value provided
  during the WRITE operation matches the ETag of the object in S3. If the ETag
  values do not match, the operation returns a `412 Precondition Failed`
  error." and "If a conflicting operation occurs during the upload S3 returns
  a `409 ConditionalRequestConflict` response."; of `If-None-Match`: "Uploads
  the object only if the object key name does not already exist in the bucket
  specified. Otherwise, Amazon S3 returns a `412 Precondition Failed` error."
  (§1, Assumption 5) **Honesty note:** the page is 69 KB; these lines were
  found by searching the saved text for `If-Match`, and the rest was not
  read.
- RFC 9110, *HTTP Semantics*: <https://www.rfc-editor.org/rfc/rfc9110.html>,
  read in parts from characters 150,000, 250,000 and 350,000 of 564,890.
  §8.8.3: "The "ETag" field in a response provides the current entity tag
  for the selected representation" and "An entity tag is an opaque validator
  for differentiating between multiple representations of the same
  resource"; §8.8.3.1: "An origin server SHOULD send an ETag for any selected
  representation for which detection of changes can be reasonably" (cut by
  the tool at that word); §9.3.2: "The server SHOULD send the same header
  fields in response to a HEAD request as it would have sent" (cut by the
  tool; the sentence continues "if the request method had been GET", which
  the tool paraphrased but did not quote); §13.1.1: "the origin server MUST
  evaluate the If-Match condition per Section 13.2 prior to performing the
  method." and, of the evaluation, "the condition is true if any of the
  listed tags match the entity tag of the selected representation." (§1,
  Assumption 5) **Honesty note:** the tool quoted at most about 125
  characters at a time, so the two cut sentences are cited as far as it
  quoted them.
- Go, `net/http`'s `transport.go` at `master`:
  <https://raw.githubusercontent.com/golang/go/master/src/net/http/transport.go>.
  In `persistConn.roundTrip`: `if !pc.t.DisableCompression &&
  req.Header.Get("Accept-Encoding") == "" && req.Header.Get("Range") == "" &&
  req.Method != "HEAD" {`, then `req.extraHeaders().Set("Accept-Encoding",
  "gzip")`, under the comment "Ask for a compressed version if the caller
  didn't set their own value for Accept-Encoding." and, inside the block,
  "Note that we don't request this for HEAD requests, due to a bug in nginx".
  (*What this does not decide*) **Honesty note:** `master`, not the Go
  version the module builds with; the tool summarised the file and quoted
  these lines on request.
- Apache HTTP Server 2.4, `mod_deflate`:
  <https://httpd.apache.org/docs/2.4/mod/mod_deflate.html>. The directive
  `DeflateAlterETag AddSuffix|NoChange|Remove`, default `AddSuffix`: "Append
  the compression method onto the end of the ETag, causing compressed and
  uncompressed representations to have unique ETags. This has been the
  default since 2.4.0"; and `Remove`: "Remove the ETag header from compressed
  responses." The page does not spell the suffix. (*What this does not
  decide*)

Cited as earlier ADRs quote them, and not fetched again: AWS's statements
that updates to a single key are atomic and that an `If-Match` write whose
ETag does not match fails with 412 (ADR 0013 *Sources*).
