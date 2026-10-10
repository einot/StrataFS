# 0012. A commit stores a truncate whole or not at all

**Status:** Accepted
**Date:** 2026-10-08
**Issue:** #64

## Context

ADR 0006 made a pending trim part of the file for every read, write and flush
in memory, and left the commit window to #64 (its *What this does not
decide*); ADR 0007 and ADR 0011 left it open too. This ADR closes it. Every
statement in this section was checked against the code at `48aa23a`, which is
cited by symbol. `cs` is the chunk size.

**The window.** `Sync`, in `internal/blobfs/commit.go`, runs `flushAll`
holding no lock, then takes `FS.mu` for writing and, when the namespace is
dirty, calls `commitLocked`, which encodes `f.inodes` as they stand.
`flushAll` lists the `FS.open` entries holding `FS.mu` for reading, releases
it, and runs `flushOpen` on each in turn. Only a flush applies a file's
pending trim or stores its shortened dirty tail (ADR 0006 §5).

`truncate`, in `internal/blobfs/fs.go`, cuts the chunk list but leaves
`n.Chunks[lastIdx]` naming the whole stored chunk: for a new end inside a
chunk it keeps `lastIdx + 1` entries and changes none of them. Only memory
hides the bytes it removed: a pending trim when the index is not dirty
(ADR 0006 §3(b)), or the dirty tail that `truncate` shortened, by a reslice,
when it is. Nothing orders a truncate against a `Sync` but the file's
`openFile.mu`, which `flushOpen` holds while it flushes that file, and
`FS.mu`, which `Sync` holds while it encodes. So for each file the window runs
from its flush in `flushAll`, or from `flushAll`'s listing for a file not on
the list, to the encode, and a truncate in it is committed with its new size
and the whole old chunk.

**How the removed bytes come back**, once a commit made in the window is the
last one:

1. **After a remount, a `SETATTR` that grows the file.** The remounted file
   names the whole stored chunk at its end, and no trim survives the restart,
   so the grown range reads the removed bytes from that chunk.
2. **After a remount, a `WRITE` past the new end within the cut chunk.**
   `bufferWrite` loads the whole stored chunk, because no trim survives the
   restart, so the removed bytes between the new end and the write's offset
   become part of the file, for the next flush to upload.
3. **In the window, a `WRITE` past the new end within the cut chunk.** In
   memory the write is right, because `bufferWrite` honours the trim
   (ADR 0006 §4). But the commit stores the write's end as the size with the
   old chunk, so a fresh mount reads the removed bytes between the cut and
   the write's offset with no growth. A write that covers the cut chunk whole
   does the same. For example, a 6000-byte file at 4 KiB chunks, cut to 5000
   and then written whole over [cs, 2cs), is committed at size 8192 with the
   old 1904-byte chunk at index 1, and [5000, 6000) reads back.
4. **The dirty tail.** A cut into a dirty index whose stored chunk is longer
   than the new end shortens only the buffer. The commit stores the stored
   chunk, and route 1 or route 2 follows.
5. **In the window, growth past the cut chunk**, by a `SETATTR` or by a
   `WRITE` into a later chunk. The commit stores the grown size with the
   whole cut chunk, so a fresh mount reads from the cut to the end of that
   chunk with no growth. A worked example at `48aa23a`: seed a file A of 2cs,
   mount the bucket afresh so that A is cold, hold a `Sync` in `flushAll`,
   truncate A to cs + 1024, then `SetAttr` it to 2cs + 2048 or write 10 bytes
   at 2cs + 808. The truncate leaves two entries and a trim at index 1. For
   the `SetAttr`, `lastIdx` is 2 and `len(n.Chunks)` is 2, so it appends no
   hole and makes no trim; for the write, index 2 lies past the end of the
   list, so `bufferWrite` starts it from zeros. Either way the commit stores
   the grown size with the whole chunk 1, and a fresh mount reads
   [cs + 1024, 2cs) back.

**No crash is needed.** ADR 0006's bullet on the commit window records the
shutdown path, and the code at `48aa23a` still has it. `Serve`, in
`internal/sunrpc/rpc.go`, closes only its listener once its context is done.
`serveConn` keeps reading records from the connections already open, and
`readRecord` reads with no deadline. When the context is done and a slot is
free, the `select` in `serveConn` can proceed both ways, so it picks at random
between dispatching the record it has read and returning, as the Go
specification says (*Sources*). The NFS server's `setattr` handler, `SetAttr`
and `truncate` never check their context, so a size change dispatched then
runs. And `run`, in `cmd/strata/main.go`, returns once its own shutdown `Sync`
has, without waiting for the committer's final `Sync`. A crash is enough on
its own.

**The protocol.** Nothing in the protocol tells a client that a size change
was lost, or makes it resend one. The write verifier covers `WRITE` and
`COMMIT` only: RFC 1813 §3.3.7 calls it a cookie for telling "whether the
server has changed state between a call to WRITE and a subsequent call to
either WRITE or COMMIT", as ADR 0006's *Sources* quote it. RFC 1813 §1.6 says
that "Most data-modifying operations in the NFS protocol are synchronous",
meaning that when such a procedure returns, "any modified data associated
with the request is now on stable storage", and its list of the synchronous
procedures leaves out `SETATTR`. §3.3.2, `SETATTR`'s own section, says
nothing about stable storage (*Sources*).

**Store calls under `FS.mu`.** No ADR states a general rule against them:
ADR 0003 §3's quoted rule is about lock order and the budget's leaf. What
there is lives in doc comments in `internal/blobfs/fs.go`: `loadChunk` "must
not be called while holding mu", `flushAll` "must be called without mu", and
`applyTrim`'s caller "must not hold mu". `Sync`'s own comment gives the lock
order, not the store calls, as the reason for the second. `commitLocked` makes
its own store calls holding `FS.mu`: the snapshot's `Put` and the root
pointer's `PutIfMatch`, or, on a backend without conditional writes, a `Put`
and a `Head` (#43).

**What reproduces it.** Nothing does today. `pending_trim_test.go` lists the
commit window under "Not covered, on purpose", and no other test names it.

## Decision

### 1. What a commit holds of a truncate

A truncate **cuts into a stored chunk** when, in the hold of the file's
`openFile.mu` and of `FS.mu` for writing in which it changes the file, it
leaves the chunk list ending at the new end's index `lastIdx` with a stored
chunk there, not a hole, whose `Size` is greater than `tail`, the new end's
offset within it, and `tail` is not 0. That covers both shapes in *Context*:
an index that is not dirty, where the truncate makes, lowers or keeps a
pending trim (ADR 0006 §3(b)), and the dirty tail. A file is **unsettled**
from such a truncate until its next flush that succeeds.

**Rule C.** No commit stores an unsettled file.

A file that is not unsettled names no byte a truncate removed. Its last
successful flush applied its trims and stored its shortened tails, and since
then, or since the mount if it has had none, only writes and truncates that
cut into no stored chunk have changed it. Writes buffer bytes and extend the
size, and an extension reads as zeros in a snapshot. A truncate that cuts into
no stored chunk leaves no stored chunk ending past its new end: the list ends
at a chunk boundary, in a hole, before the new end's index, or with a stored
chunk no longer than `tail`. A pairing an earlier build committed is another
matter (§8).

**Rule D.** A truncate that has returned when a `Sync` begins its commit
phase (§2) is in that `Sync`'s commit, whole, if the commit succeeds: the
snapshot holds its size, and the chunk at its end holds the bytes below the
end and none past it. A truncate that has not returned by then is not in that
commit at all, and is in a later one, whole, by the same rule.

Calls made after the truncate and before the commit change the file in the
snapshot as they changed it in memory, a write's bytes only once a flush has
stored them. What Rule D rules out is a commit that holds a truncate's size,
or a size a later call set, together with bytes the truncate removed.

### 2. The commit gate

`FS.commitGate` is a `sync.RWMutex`.

- `truncate` takes it for reading after the checks it makes before `getOpen`:
  ADR 0007 §4's size check, and the resolve, type and permission checks it
  makes holding `FS.mu` for reading. It takes it before `getOpen`, and so
  before `openFile.mu` and `FS.mu`, and holds it until it returns. A size
  change refused by those checks never waits for the gate. The second
  resolve, under both locks, can still refuse after the gate is taken.
- `Sync` takes it for writing, holding no lock, once its first pass has
  succeeded, and holds it until it returns. That span is its **commit
  phase**.
- Nothing else takes it, except through `Sync`: not a `SetAttr` that sets no
  size, `Write`, `Read`, `Commit`'s flush of its own file, `flushAll`, any
  namespace operation, or `Run`.

`Sync` runs in three steps:

1. **Its first pass:** `flushAll`, holding no gate.
2. **Its settle pass,** holding the gate: under `FS.mu` for reading, list the
   `FS.open` entries whose mark (§3) is set, release `FS.mu`, and run
   `flushOpen` on each.
3. **Its commit,** holding `FS.mu`, if the namespace is dirty, as today.

Errors from steps 1 and 2 are wrapped with `%w`, and nothing is committed.
The order of the settle pass's flushes is not pinned. A flush flushes the
whole file (ADR 0006 §5), so the settle pass also uploads what was written to
those files during the first pass.

`truncate` releases the gate when it returns, before `SetAttr` applies the
rest of `sa`. So a `SETATTR` that sets a size and other attributes can land in
two commits, the size in one and the rest in the next. RFC 1813 §3.3.2 allows
it: "SETATTR is not guaranteed atomic" (*Sources*). At `48aa23a` it could
happen too, since `truncate` released `FS.mu` before `SetAttr` took it again.

### 3. The unsettled mark

`openFile.unsettled` is a `bool`, written only holding that `openFile`'s `mu`
and `FS.mu` for writing, and so readable holding either.

- `truncate` sets it when it cuts into a stored chunk (§1), in the hold of
  both locks in which it makes the cut. Nothing else sets it, and `truncate`
  never clears it.
- `flushOpen` clears it when it succeeds: inside the hold of `FS.mu` in which
  it repoints the chunk list, whether or not the inode is still there; and on
  its early return, when the file has neither dirty buffers nor pending
  trims, by taking `FS.mu` for writing if the mark is set. A flush that fails
  leaves it set.

**Why the early return may clear it.** Every cut that marks a file leaves a
pending trim (new, lowered or kept) or a dirty buffer at its index. Between
two successful flushes there is at most one stale chunk, at the index of the
latest marking cut, because cuts never move to a higher index (ADR 0006 §2,
the argument for I4). Only three things remove that trim or buffer without a
successful flush:

- a truncate that drops the index, which by the same rule also cuts the stale
  chunk out of the chunk list;
- `bufferWrite`'s site 2 (ADR 0003 §2);
- `dropIfGone`.

Site 2 and `dropIfGone` reset buffers only when the inode has gone, and by
then `dropOpen` has taken the entry out of `FS.open`, in the critical section
that removed the inode. The entries `getOpen` can recreate for a gone inode
(#42) are never marked, because marking needs a successful resolve under both
locks. A write into an index with a trim replaces the trim with a buffer, so
the index still holds one or the other. So a file in `FS.open` with neither
dirty buffers nor pending trims names no removed byte.

**Why the mark is sticky.** `bufferWrite` extends `n.Size` without the gate:
`Write` takes none (§2), and waits only where ADR 0003 §4 puts its wait. That
is safe only because the mark is sticky: a write that grows a marked file,
within the cut chunk or past it, leaves the mark set, and the settle pass
flushes the file before the commit (routes 3 and 5; Assumption 9).

So every unsettled file is marked, and a settle pass that succeeds leaves none
marked: it flushes every entry marked when it lists them, and no entry can be
marked while it holds the gate.

### 4. The commit checks itself

`commitLocked`, after its check for divergence and before it encodes
anything, checks whether any `openFile` in `FS.open` is marked. If one is, it
logs one record at `Error`, returns an error and stores nothing. Neither the
error's text nor the record's message or attributes are pinned.

Under §2 and §3 the check cannot fail. It exists so that a later path that
changes a chunk list without the gate causes a failed, logged commit instead
of a silent exposure. `initEmpty`'s commit finds `FS.open` empty.

### 5. Lock order

The commit gate, then `openFile.mu`, then `FS.mu`; the budget's mutex stays a
leaf.

- Nothing takes the gate while holding an `openFile.mu`, `FS.mu` or the
  budget's mutex.
- `truncate`, holding it for reading, never reaches `Sync`, `flushAll` or
  `flushOpen`.
- `Sync`, holding it for writing, never calls `truncate`.
- Nothing waits on the budget while holding it, and nothing read-locks it
  recursively.
- No deadlock: both takers take the gate before any other lock, so every lock
  a gate holder waits for is held by a goroutine that is not waiting for the
  gate.
- No store call is added under `FS.mu`: the settle pass's fetches and uploads
  hold the gate and the file's `openFile.mu`, as any flush holds
  `openFile.mu`, and the commit's own store calls hold `FS.mu` as before
  (#43).

### 6. Every path into the window

| Path | At `48aa23a` | After this ADR |
|---|---|---|
| A `SETATTR` of the size | `truncate` holds the file's `openFile.mu` and `FS.mu` and nothing else, so it can land after its file's flush in `flushAll`, or on a file the list does not hold, and before the encode, which stores its size with the whole cut chunk | It holds the gate for reading. A commit phase begins either after it has returned, and its cut is applied before the commit, by the settle pass if no flush has applied it already (Rule D), or before it has taken the gate, and it waits for the phase to end and is not in that commit |
| An UNCHECKED `CREATE` with a size | The same: `Server.create` applies the size through `SetAttr` once `Create` has returned | As a `SETATTR` of the size. The create and its size can land in two commits, as before |
| The dirty tail (route 4) | The commit stores the stored chunk with the shortened size; route 1 or route 2 follows | The cut marks the file, and the settle pass uploads the shortened buffer before the commit |
| Routes 1 and 2 | A remount names the whole stored chunk, so growth reads the removed bytes, and a `WRITE` past the new end within the chunk loads them | No commit this build makes holds bytes a truncate removed, so after a remount growth and writes find zeros there. Pairings an earlier build committed stay (§8) |
| Route 3 | A fresh mount reads the removed bytes between the cut and the write's offset, or the whole old chunk after a write that covers it, with no growth | The mark outlives the write, and the settle pass flushes the file, cut and write together, before the commit |
| Route 5 | A fresh mount reads from the cut to the end of the cut chunk, with no growth | The mark outlives the growth, and the settle pass applies the trim before the commit |
| The committer's periodic `Sync` | `flushAll`, then the commit, with the window between them | A first pass, then the settle pass and the commit, holding the gate (§2) |
| `COMMIT` | `Commit` flushes its own file and then runs `Sync`, so the window is open for every file | `Commit`'s own flush takes no gate; its `Sync` is as above |
| `FILE_SYNC` and `DATA_SYNC` writes (#44) | `syncFileAndNamespace` flushes the file and runs a full `Sync`: the same | As `COMMIT` |
| A budget drain | The drain is `Sync`: the same | As the periodic `Sync`. A truncate still never waits on the budget |
| The shutdown `Sync`s in `run` and in the committer, with calls still dispatched | `serveConn` can still dispatch a `SETATTR` while either final `Sync` runs, and `run` exits once its own returns, so the last commit can hold a truncate's size without its cut | Each final `Sync`'s commit holds a truncate whole or not at all. A call dispatched during shutdown is lost whole if it is answered after the last commit |
| A crash at any point | The last commit can be one made in a window | Every commit is consistent, and what came after the last one is lost whole |

After this ADR every commit on those paths is consistent: none pairs a
truncate's size, or a size a later call set, with bytes the truncate removed.
Calls answered after the last commit before the process ends are lost whole,
with one qualification that this ADR does not change: a `SETATTR` that sets a
size and other attributes, and an UNCHECKED `CREATE` that carries a size, each
make their change in two steps, and a commit can fall between them (§2; the
table's second row).

### 7. What a size change waits for, and what a `Sync` costs

- A truncate never waits on the budget (ADR 0006 §3(d), §7).
- It waits for the commit gate while a `Sync` is in its commit phase: for the
  settle pass's flushes, and for the commit's store calls, which it already
  waited for through `FS.mu` (#43).
- It also waits while a `Sync` waits to begin a commit phase. A
  `sync.RWMutex` excludes new readers once a writer waits, and the writer
  waits for the readers already in (*Sources*). A size change that holds the
  gate waits, as it always did, for its own file's `openFile.mu`. A flush of
  that file (any `Sync`'s first pass, or `Commit`'s or a stable write's
  flush, through `commitFile`), or a write fetching one of its chunks, can
  hold that lock across store calls. So the whole flush of that file can hold
  up every size change on the server: `flushOpen` holds the file's
  `openFile.mu` across all of its store calls, the fetch, probe and upload
  that apply a pending trim and a probe and an upload for each dirty chunk.
  Its dirty chunks fit within the budget's bound on the dirty pool,
  `MaxDirtyBytes` (256 MiB by default) plus ADR 0003 §6's overshoot, which is
  up to 128 MiB per connection at the defaults and has no bound in total
  (ADR 0003 Assumption 19). With the budget disabled nothing bounds them: a
  negative `MaxDirtyBytes`, which `-max-dirty` 0 or less gives, makes `await`
  admit every write. At the defaults, `defaultChunkSize` and
  `defaultMaxDirtyBytes`, a file whose dirty chunks fill the budget has 256
  of 1 MiB, and its flush makes up to 256 probes and 256 uploads. A write
  holds the lock across at most two fetches, of the chunks at its two ends.
  For example:
  1. `Sync` S2's first pass flushes A early.
  2. A gets dirty data, and a `COMMIT` of A uploads it, holding A's
     `openFile.mu`.
  3. A `SETATTR` of A's size takes the gate for reading and waits for that
     lock.
  4. S2 ends its first pass and waits for the gate.
  5. Every size change on every other file now waits until `COMMIT`(A)'s
     upload is done.
- So a size change made during a `Sync`'s first pass waits for nothing new
  only when no other `Sync` is in its commit phase or waiting for one. Stable
  writes each run a `Sync` (#44), so overlapping `Sync`s make the wider wait
  routine.
- It is a delay, never a deadlock: no holder of an `openFile.mu` or of
  `FS.mu` waits for the gate. A client can lengthen it, and repeat it. Each
  file cut during a first pass adds its whole flush to the settle pass, its
  dirty set included, because the settle pass, `settleAll`, flushes whole
  files with `flushOpen` (Assumption 8), and writes made to such a file after
  the cut, until the settle pass takes its `openFile.mu`, are uploaded under
  the gate too. When the pass reaches a file, the file holds at most the
  dirty pool's bound in dirty chunks and at most one pending trim
  (ADR 0006 §7). The files of one pass can hold more together, since each
  flush frees room that writes to the files after it can take. But every part
  of it is bounded by store calls, each capped by the S3 client's timeout and
  retries: the same kind of delay as #43, inside the loopback trust boundary
  of ADR 0003 Assumption 19. A stream of size changes cannot starve commits,
  because a waiting writer excludes new readers.
- The S3 client caps each request, not each store call. `NewS3` gives its
  `http.Client` a `Timeout` of 30 s unless `S3Config.Timeout` sets another,
  and `cmd/strata` sets none; the timeout includes connecting, any redirects
  and reading the response body (*Sources*). `S3.do` sends up to
  `maxRetries` + 1, four, requests for every call but `PutIfMatch`, 200, 400
  and 800 ms apart, so one store call can last about two minutes, and every
  call a flush makes is retried so. A local-directory store, `store.Local`,
  sets no timeout of its own.
- What is new since `48aa23a` is that size changes of other files wait for a
  file's whole flush. Commits already did, since every first pass,
  `flushAll`, takes each open file's `openFile.mu` to flush it; a size change
  waited only for its own file's lock and for `FS.mu` (#43). #85 tracks
  reducing that stall.
- The commit phases of overlapping `Sync`s take the gate one at a time, as
  their commits already took `FS.mu` one at a time; their first passes still
  overlap.
- `TestBackpressureTruncateNeverChargesOrWaits` keeps passing: its drain is
  held in a first pass, no other `Sync` runs, and nothing holds the truncated
  file's `openFile.mu`.
- The costs are unmeasured.

### 8. No format change

`snapshotVersion` stays 1 and `chunkRef` is unchanged, so builds on either
side of this ADR mount each other's buckets. A pairing an earlier build
committed is neither detected nor repaired.

### 9. The test surface

**No new name.** `FS.commitGate` and `openFile.unsettled` are named in §2 and
§3 for the implementation and its review; a test must not read or write them.
The names pinned by ADR 0003 §4, ADR 0004 §5 and §7, ADR 0006 §8, ADR 0007 §7
and ADR 0011 §7 still apply, and a test computes a chunk's object key as
ADR 0004 §5 gives it.

**Terms.**

- A `Sync` is **held in its first pass** when it is held in the store's `Put`
  of a chunk whose bytes a file's dirty buffer held when the `Sync` was
  called.
- A file **the first pass does not list** is one with no `FS.open` entry when
  the `Sync` is called.

**A test may rely on these:**

1. While a `Sync` is held in its first pass, a truncate returns without
   waiting for the `Sync`, when no other `Sync` is running, no other size
   change is in progress, and nothing holds the truncated file's
   `openFile.mu`. This holds whether the `Sync` was run directly, by a
   `Commit` of another file, by a `FILE_SYNC` or `DATA_SYNC` `Write` to
   another file, or as a budget drain.
2. Suppose that, while a `Sync` is held so, a truncate cuts into a stored
   chunk of a file the first pass does not list, and the call that runs the
   `Sync` then succeeds. Then a fresh mount of the bucket, made without
   another `Sync`, finds the file at the truncate's size with the bytes below
   it unchanged. Once the file is grown, by a `SetAttr` of the size or by a
   `Write` past the new end within the cut chunk, it reads zeros where the
   truncate removed bytes.
3. If, while so held, such a truncate is followed by an `UNSTABLE` `Write`
   past the new end within the cut chunk, a fresh mount reads zeros between
   the new end and the `Write`'s offset. The `Write`'s own bytes are not
   pinned.
4. If, while so held, an `UNSTABLE` `Write` into a stored chunk of such a file
   is followed by a truncate whose new end falls inside that chunk (the dirty
   tail), a fresh mount, once the file is grown by a `SetAttr`, reads zeros
   from the new end to the end of that chunk. The `Write`'s own bytes are not
   pinned.
5. Before it commits, such a `Sync` uploads the shortened chunk that applies a
   truncate made during its first pass: the chunk's first `to` bytes
   (ADR 0006 §5), under the key ADR 0004 §5 gives them, unless the bucket
   already holds that content. If that flush fails, the `Sync` returns an
   error for which `errors.Is` finds the failure, and commits nothing: a fresh
   mount finds the file as the previous commit left it.
6. While a `Sync` is held in that upload:
   - a `SetAttr` whose size is above `MaxFileSize` returns `vfs.ErrFBig`;
   - one that sets a size on a stale handle returns `vfs.ErrStale`;
   - one that sets a size, by a caller without write permission, returns
     `vfs.ErrAcces`;
   - a `SetAttr` that sets no size returns;
   - an `UNSTABLE` `Write` to another file, with the budget disabled or below
     its limit, returns;
   - a `Read` of a file with no `FS.open` entry returns.

   A truncate of another file the first pass does not list, started then, may
   or may not return before the `Sync` does. Once both have returned, a fresh
   mount finds that file either at its old size with its old bytes, or at its
   new size, reading zeros past the new end after it is grown.
7. If, while a `Sync` is held in its first pass, a truncate cuts into a stored
   chunk and a second truncate then sets the size to the start of that chunk,
   the `Sync` succeeds, and a fresh mount finds the file at the second
   truncate's size.
8. Suppose that, while a `Sync` is held in its first pass, a truncate cuts
   into a stored chunk of a file the first pass does not list, and then a
   `SetAttr` grows the file past the cut chunk or an `UNSTABLE` `Write` lands
   past the end of the cut chunk. If the `Sync` then succeeds, a fresh mount
   finds the file at the grown size and reads zeros from the cut to the end
   of the cut chunk, with no further growth. The `Write`'s own bytes are not
   pinned.

**A test must not rely on:**

- whether, or for how long, a size change waits for the commit gate, beyond
  item 1;
- the order or number of the flushes a `Sync` makes;
- whether the settle pass also uploads a file's other dirty data;
- what a `Sync` does when no file is unsettled.

**How a test reaches each case:**

- **A `Sync` held in its first pass:** on the mount under test, create a file
  and write bytes new to the bucket at its offset 0, so that its chunk 0 is
  exactly those bytes. Hold the store's `Put` of that chunk's key, as the
  existing tests' `bpStore` does, start the call that runs the `Sync`, and
  wait until the `Put` has been entered. For a budget drain: give the mount a
  budget of one chunk; create the file the drainer will write, with nothing
  in it; write the held file last, so that its write fills the budget; arm
  the hold; then start an `UNSTABLE` `Write` to the empty file.
- **A file the first pass does not list:** seed it through another mount and
  mount the bucket afresh. Until something on the new mount writes or
  truncates it, it has no `FS.open` entry (ADR 0005 §4), which a test can
  check holding `FS.mu` for reading (ADR 0003 §4).
- **A `Sync` held in the settle upload:** after the cut, hold the `Put` of the
  shortened chunk's key, release the first-pass hold, and wait until that
  `Put` has been entered.
- **A failing settle pass:** fail the store's `Get` of the cut chunk's key,
  with an error of the test's own: `loadChunk` reports `store.ErrNotFound` as
  `vfs.ErrIO` without wrapping it, so `errors.Is` would not find that one. A
  fresh mount has not read that chunk, so the flush fetches it from the store
  (ADR 0006 §7).
- **A fresh mount:** `New` on the same bucket, with no further `Sync` of the
  mount under test. When a test grows a file in more than one way, it uses a
  fresh mount for each.

**Safety rules**, as ADR 0011 §7 gives them for tests that may meet a build
without this ADR:

- every FS call runs in a goroutine and is waited for under a bound;
- every hold is opened by the test's cleanup;
- every mount under test has `MaxDirtyBytes` −1 unless its case needs a budget
  drain, so that no `Write` made while a `Sync` is held becomes a drainer;
- every `Write` made while a `Sync` is held is `UNSTABLE`, since a stable
  `Write` runs a `Sync` that waits for the held file;
- no sleep except polling with a deadline, plus at most one fixed grace
  period, used only as a limit;
- every bucket is under the test's temporary directory;
- every mount that commits has `SnapshotRetention` −1.

**What only review can show:**

- that the gate is held across the whole commit phase on every path;
- the fail-safe of §4 and its record;
- that `Run`'s committer, `cmd/strata`'s shutdown, `Commit`, stable writes and
  budget drains all go through `Sync`;
- the lock-order rules of §5.

## Assumptions

Each is a judgement call, not something the issue, the spec or an earlier ADR
required. Six of them record a decision that the owner delegated to the
architect's recommendation, and say so: Assumptions 1 to 6.

1. **Fix rather than accept, by a commit gate with a settle pass.** Accepting
   leaves a defect any client can reach, with or without a crash
   (*Alternatives considered*). The owner delegated this to the architect's
   recommendation on 2026-10-08.
2. **No snapshot format version; `snapshotVersion` stays 1** (§8). The owner
   delegated this to the architect's recommendation on 2026-10-08.
3. **Scope.** Left out: the shutdown dispatch race; namespace operations
   acknowledged before they are committed; repairing or reporting pairings
   earlier builds committed; #43; #44 (*What this does not decide*). The
   owner delegated this to the architect's recommendation on 2026-10-08.
4. **A size change may now wait for the commit gate:** while a `Sync` is in
   its commit phase, whose settle pass flushes every marked file whole, and
   while one waits to begin it behind a size change that is itself waiting
   for its own file's `openFile.mu`, which a flush holds across all of that
   file's store calls (§7). Either wait can last through whole-file flushes,
   each of up to the dirty pool's worth of uploads, or of any size with the
   budget disabled, so one client can make every size change on the server
   wait that long, and repeat it to keep them waiting (§7; #85). ADR 0006
   §3(d) said a truncate never waits; it still never waits on the budget. The
   owner delegated this to the architect's recommendation on 2026-10-08.
5. **Not breaking under `CLAUDE.md`'s rule for `CHANGES`.** No deployment
   needs to act: the format, the flags and the protocol are unchanged, and
   what a size change gains is a wait (§7). The owner delegated this to the
   architect's recommendation on 2026-10-08.
6. **Status Accepted on creation,** on the delegation recorded in
   Assumptions 1 to 5. The owner delegated this to the architect's
   recommendation on 2026-10-08.
7. **Two passes rather than one gate across the whole `Sync`,** so that a size
   change does not wait for the files flushed in a first pass.
8. **The settle pass flushes the whole file with `flushOpen`,** not only the
   cut index. Its cost is that each marked file's whole dirty set, writes
   made after the cut included, is uploaded holding the gate, so every size
   change on the server waits for those uploads (§7; #85).
9. **The mark is sticky:** set at the cut, and cleared only by a successful
   flush. It is not computed at the settle pass or the commit from the size
   and the chunk list. A computed check misses route 5 and route 3's
   whole-chunk form: once the file has grown in the window past the cut
   chunk, or a write has covered the cut chunk whole, no stored chunk ends
   past the size and none lies past the end of the list, yet the snapshot
   would name the removed bytes. `bufferWrite` extends `n.Size` without the
   gate (§3), so only a mark that outlives the growth can tell the settle pass
   to flush the file.
10. **An RWMutex with size changes as readers,** so that size changes do not
    wait for one another's files' locks, except through a waiting writer
    (§7).
11. **The gate is taken after the checks made before `getOpen`,** so a refused
    size change never waits for it, and ADR 0007 §4's "takes no lock" stays
    true.
12. **The fail-safe of §4 and its `Error` record.** Its cost is a walk of
    `FS.open` under `FS.mu` per commit. A stale mark cannot trip it, because
    the clear on the early return is tested (§9 item 7).
13. **Every commit path is `Sync`,** so nothing outside `internal/blobfs`
    changes.
14. **The sentence added to `docs/DESIGN.md` §5,** so that the redesign's
    consistency points keep the rule this ADR sets for the proof of concept.
15. **The costs in §7 are unmeasured.**
16. **Read, not run.** The behaviour at `48aa23a` is read from the code, and
    the window will be reproduced only by the tests §9 specifies.
17. **No test seam is needed:** ADR 0004 §5's chunk keys and the existing store
    fakes reach every case.
18. **Tests do not pin whether, or how long, a size change waits for the
    gate,** beyond §9 item 1.

## Alternatives considered

- **Accept and document.** It leaves a confidentiality and integrity defect
  that any client can reach, with or without a crash.
- **Record the trimmed length in `chunkRef.Size` and the snapshot, with a
  format version.** ADR 0006 listed this and did not adopt it, calling the
  format version the owner's call and the redesign's spans its natural home.
  An older build refuses a newer bucket (`loadOrInit`'s version check), and a
  newer build that still read the old version would upgrade an older bucket
  at its first commit. So the change is one-way and blocks a rollback, and it
  would rewrite ADR 0006's mechanism.
- **Stop dispatching before the final `Sync`** (the option ADR 0006's audit
  revision lists). It closes only the clean shutdown route, and only if calls
  already dispatched are drained too. Crashes, and every window during normal
  running, stay open. Graceful shutdown is a change of its own.
- **The gate held across the whole `Sync`.** Every size change would wait for
  every upload, against ADR 0006 §3(d), and
  `TestBackpressureTruncateNeverChargesOrWaits` would hang.
- **A second `flushAll` under the gate, with no mark.** About ten lines and
  plainly correct, since every open file is flushed at a moment when no
  truncate can intervene. But it uploads everything written during the first
  pass while holding the gate, so size changes wait for all of it, and it
  leaves the commit nothing to check itself against.
- **A size change takes the gate only when it cuts,** deciding under its locks
  with `TryRLock` and a retry. It would spare truncates to zero and growth, at
  the price of a try-lock and a retry loop under `openFile.mu`; the `sync`
  documentation calls correct uses of `TryRLock` rare (*Sources*).
- **Shadow,** where the commit stores an unsettled file's last settled size
  and chunk list. No new wait, but:
  - it leaves out truncates that returned before the commit began (every one
    made after its file's first-pass flush, and every one of a file the first
    pass did not list), where the gate leaves out only those answered after
    the last commit;
  - it needs a copy of the references a cut drops: 24 to 88 MiB for a file cut
    from 2^20 chunks (ADR 0007 §2), per file, with no limit on the number of
    files, the kind of exhaustion ADR 0006 and ADR 0007 closed;
  - it cannot meet #64's done-when, since a remounted file comes back at its
    old size with its old bytes.

  A hybrid that settles without a gate and shadows only files cut after the
  listing carries all of the shadow's bookkeeping.
- **Retry until settled,** where the commit refuses and `Sync` flushes again,
  with no gate. It livelocks under a steady stream of cuts, and its fallback
  is a gate.
- **Per-file locks held across the commit.** A cut of another file still lands
  in the window.
- **A trim at mount for stored bytes past a file's size.** It repairs only the
  pairings of routes 1, 2 and 4 that an earlier build left, not those of
  routes 3 and 5. It touches `New`, ADR 0005 §3's invariant 3 and ADR 0011.
  On its own, it would pass #64's done-when test while leaving the window
  open.
- **`truncate` applies its own trim before it returns.** A window remains
  between its namespace change and its flush, and ADR 0006 rejected store
  calls in `truncate`.
- **The commit applies trims under `FS.mu`.** It needs `openFile.mu` under
  `FS.mu`, inverting the lock order, and adds store calls under `FS.mu`,
  against `loadChunk`'s doc comment and the direction of #43.

## Consequences

- No commit, after a crash or a clean shutdown, pairs a truncate's size with
  bytes it removed, and a file grown after a remount reads zeros there.
- Rule D (§1): a truncate that has returned when a commit phase begins is in
  that commit, whole, if the commit succeeds; one that has not is in no part
  of it.
- A size change can wait for the gate as §7 says: a delay bounded by store
  calls, never a deadlock.
- Commit phases are serialised.
- A settle pass that fails commits nothing.
- A chunk that cannot be fetched blocks every commit until the store recovers,
  as a trim queued before a `Sync` already does.
- One `SETATTR`'s size and its other attributes can land in two commits.
- Buckets earlier builds left are not repaired.

## What this does not decide

- **The shutdown dispatch race:** `serveConn` dispatches calls during a clean
  shutdown, and one answered after the last commit is lost. An issue is
  recommended.
- **Namespace operations answered before they are committed,** against
  RFC 1813 §1.6, which lists `CREATE`, `MKDIR`, `REMOVE`, `RENAME` and others
  as synchronous. An issue is recommended.
- **Repairing, or warning at mount about, pairings earlier builds committed.**
  A warning is recommended as an issue.
- **#43.** Once it is fixed, a `Sync` can release the gate as soon as the
  snapshot is encoded.
- **#44.** A per-file commit for stable writes would have to keep Rule C.
- **The redesign's truncate** (`docs/DESIGN.md` §4, §5).
- **An unlocked pre-settle pass:** before taking the gate, flush the marked
  files once, and leave the gate only what was marked meanwhile. It would cut
  the store calls made under the gate to almost none, and §9 already allows
  it. A later refinement, not a fix. #85 tracks reducing the stall of size
  changes that §7 describes; this pass would shrink only the settle pass's
  share of it, not the wait behind a size change held up by its own file's
  lock.
- **A test of the committer's tick or of `cmd/strata`'s shutdown,** which
  review covers by checking that both go through `Sync`.

## Sources

Fetched on 2026-10-08 through a tool that summarises what it fetches, and
quoted as it returned them when asked for verbatim text. The tool returns at
most 125 characters per quote and reads a long page 100,000 characters at a
time, so several quotes below came back in consecutive segments, which are
joined here without other change. The tool keeps a page for 15 minutes, so a
second fetch of a URL inside that window may have come from its cache.

- RFC 1813, *NFS Version 3 Protocol Specification*, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>.
  - §1.6, under the heading the tool gave as "1.6 Philosophy": "Most
    data-modifying operations in the NFS protocol are synchronous." "That is,
    when a data modifying procedure returns to the client, the client can
    assume that the operation has completed and any modified data associated
    with the request is now on stable storage." "The following data modifying
    procedures are synchronous: WRITE (with stable flag set to FILE_SYNC),
    CREATE, MKDIR, SYMLINK, MKNOD, REMOVE, RMDIR, RENAME, LINK, and COMMIT."
    `SETATTR` is not among them. (*Context*; *What this does not decide*)
  - §3.3.2, `SETATTR`, from its IMPLEMENTATION text: "SETATTR is not
    guaranteed atomic." "A failed SETATTR may partially change a file's
    attributes." And "The new_attributes.size field is used to request
    changes to the size of a file. A value of 0 causes the file to be
    truncated, a value less than the current size of the file causes data
    from new size to the end of the file to be discarded, and a size greater
    than the current size of the file causes logically zeroed data bytes to
    be added to the end of the file." Asked whether "stable" or "stable
    storage" appears anywhere in §3.3.2, the tool answered that neither does.
    (*Context*, §2)

  **Honesty note:** the tool read only the first 100,000 of the text's
  229,793 characters, which cover §1.6 and §3.3.2; it stopped inside §3.3.7,
  as ADR 0007 found. The first fetch returned §1.6's first sentence and its
  list but not the sentence on stable storage, and a second fetch asking for
  every sentence between the two returned it. The §3.3.2 quotes come from a
  third fetch. All three may have read the same cached copy.
- RFC 1813 §3.3.2 as freesoft.org renders it:
  <https://www.freesoft.org/CIE/RFC/1813/22.htm>. Its heading, "3.3.2
  Procedure 2: SETATTR - Set file attributes"; the same size text, word for
  word; "SETATTR is not guaranteed atomic."; "A failed SETATTR may partially
  change a file's attributes."; and, by the tool's answer, no occurrence of
  "stable" or "stable storage" on the page. A mirror, and so secondhand, but
  it matches the RFC Editor's text above. Fetched once. (§2)
- RFC 1813 §3.3.7, on the write verifier, and POSIX.1-2024 `ftruncate()`: not
  fetched for this ADR, and cited as ADR 0006's *Sources* quote them, from
  fetches made on 2026-09-25. (*Context*)
- *The Go Programming Language Specification*, *Select statements*:
  <https://go.dev/ref/spec>. Step 2 of the execution: "If one or more of the
  communications can proceed, a single one that can proceed is chosen via a
  uniform pseudo-random selection. Otherwise, if there is a default case,
  that case is chosen. If there is no default case, the "select" statement
  blocks until at least one of the communications can proceed." (*Context*)

  **Honesty note:** the page is 263,947 characters long. The first two
  fetches, from its start and from character 100,000, stopped before the
  section; the third, from character 200,000, returned it.
- Go standard library, `sync.RWMutex`: <https://pkg.go.dev/sync#RWMutex>. The
  type's doc comment: "A RWMutex is a reader/writer mutual exclusion lock. The
  lock can be held by an arbitrary number of readers or a single writer." and
  "If any goroutine calls RWMutex.Lock while the lock is already held by one
  or more readers, concurrent calls to RWMutex.RLock will block until the
  writer has acquired (and released) the lock, to ensure that the lock
  eventually becomes available to the writer. Note that this prohibits
  recursive read-locking." `RLock`: "It should not be used for recursive read
  locking; a blocked Lock call excludes new readers from acquiring the lock."
  `TryRLock`: "Note that while correct uses of TryRLock do exist, they are
  rare, and use of TryRLock is often a sign of a deeper problem in a
  particular use of mutexes." (§5, §7; *Alternatives considered*)

  **Honesty note:** fetched once; the tool removed the page's hyperlink markup
  and returned these in segments. I did not read the page end to end.
- Go standard library, `net/http`, `Client`:
  <https://pkg.go.dev/net/http#Client>, fetched on 2026-10-10 through the
  same tool. The `Timeout` field's doc comment: "Timeout specifies a time
  limit for requests made by this Client." "The timeout includes connection
  time, any redirects, and reading the response body." "The timer remains
  running after Get, Head, Post, or Do return and will interrupt reading of
  the Response.Body." (§7)

  **Honesty note:** fetched once. The tool read the first 100,000 of the
  page's 234,315 characters, which hold the `Client` type. Asked for any
  sentence in the type's or `Client.Do`'s doc comment saying whether the
  limit applies to each call of `Do`, it found none. That each request
  `S3.do` sends gets a limit of its own is my reading of "requests made by
  this Client", since `S3.do` builds a new request for each attempt and sends
  it with `Client.Do`.
