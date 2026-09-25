# 0005. A read takes one view of a file, under the file's lock

**Status:** Accepted
**Date:** 2026-09-25
**Revised:** 2026-09-25 — the same day, after review of the tests and before
the change merged. §6 gains a bullet, *An attribute change*, §7's list of
behaviours a test may rely on gains one entry, and Assumption 15 is added: a
`SetAttr` that sets no size takes `FS.mu` for writing and never an
`openFile.mu`, so a flush of the file held in a chunk upload does not hold it
up. The code already works that way, and ADR 0002 Assumption 7 implies it by
naming only a size-setting `SETATTR` among the calls that can wait on
`openFile.mu`, but nothing stated it, and the test of §2 step 2's permission
check depends on it. The decision, the three steps, the invariants and
everything else are unchanged.
**Issue:** #41, #49

## Context

`blobfs`'s `Read` builds its reply from two holds of two locks:

1. Holding `FS.mu` for reading, it resolves the handle, checks the file's type
   and the caller's read permission, and copies the file's size, its whole
   chunk list and its `openFile`, if `FS.open` has one. It releases `FS.mu`.
2. Holding that `openFile`'s `mu`, it copies the `dirty` map: the map's slice
   headers, not the bytes they point to. It releases `openFile.mu`.

It then fills the reply holding no lock: from those slices for the indices the
copied map holds, and from chunks it fetches with `loadChunk` for the rest.

**#41: a flush between the two holds.** `flushOpen` uploads a file's dirty
chunks, repoints `n.Chunks[idx]` at each upload and replaces `of.dirty` with an
empty map, all in one hold of `openFile.mu`. A flush that runs between `Read`'s
two holds leaves `Read` with a chunk list from before the flush and a dirty map
from after it. For an index that the flush uploaded, `Read` finds nothing in
the map and fetches what its out-of-date list names: the chunk's previous
contents. Nothing is lost in the bucket and a retry reads the right bytes, but
a client can see an acknowledged write disappear and come back. It is the
read-side twin of #37, which `5479fe2` fixed in `bufferWrite` by reading the
chunk list under `openFile.mu` (ADR 0003 §4, *The chunk list is read after the
wait, under `openFile.mu`*). There the out-of-date view was written back, and
data was lost.

**#49: bytes copied from shared buffers with no lock held.** The slice headers
`Read` copies share their backing arrays with `of.dirty`. `bufferWrite` copies
new bytes into those arrays in place, holding `openFile.mu`. `truncate`
reslices a dirty tail in place (`of.dirty[lastIdx] = chunk[:tail]`), so a later
write appends into the same array (ADR 0004 *Context*). `Read` copies out of
them holding no lock. Under the Go memory model that is a data race, and a
reply can hold part of a write (*Sources*).

ADR 0003 left #41 to its issue, and ADR 0004 left both (*What this does not
decide* in each).

The fix is local to `Read`, but it makes `Read` hold both locks, and it relies
on things the write path keeps without anything stating them (§3). Those
constrain later work: a fix for `truncate`'s deferred trims (#40, #56), any
change to when an `openFile` leaves `FS.open` (`vfs.Pinner`, or evicting idle
entries), and the redesign's read path (`docs/DESIGN.md` §7). The clean-room
tests for #41 and #49 also need something to be written from. Hence an ADR
rather than a comment in the code.

## Decision

### 1. What a Read returns

A `Read` of a regular file returns the bytes the file holds at one instant
between the call and its return, the `Read`'s *view*, clamped to the size the
file has at that instant, with `eof` decided by that size. A client can
observe two consequences:

- **Freshness.** Every byte that a `Write` which returned before `Read` was
  called put in the file reads as that `Write` left it, unless another `Write`
  or a size change had changed it by the view.
- **Atomicity.** One reply never holds bytes from both before and after one
  `Write`, or one size change. A `Write` that reports fewer bytes than it was
  given, the partial write of ADR 0003 §2, site 1, counts as the bytes it
  stored.

These are the guarantees POSIX gives `read()` against `write()` and
`ftruncate()` on a regular file (*Sources*). A view is of one file; nothing is
promised across files.

One way a `Read` can return bytes the file should not hold is outside this ADR.
A truncate into a stored chunk that this `FS` has not cached keeps the chunk
list naming the untrimmed chunk, and queues the trim in `of.pendingTrim` for
the next flush to apply (ADR 0003 §2; Assumption 6). A `Read` is clamped to the
size, so the bytes past the new end stay hidden until the file grows back over
them. After that a `Read` can return them where it should return zeros: before
the next flush, and in some cases after it too, because a flush applies only
the first trim queued for an index. That is a defect in what a pending trim
means, not in how a `Read` takes its view. #56 tracks it, and #40 tracks a
write into such an index, which starts from the untrimmed chunk.

### 2. The three steps, and the locks each holds

1. **Holding `FS.mu` for reading.** Resolve the handle; refuse a directory with
   `vfs.ErrIsDir` and then a caller without read permission with
   `vfs.ErrAcces`, as today; look the inode up in `FS.open`. If it has no
   entry, take the view in this same hold, the size and the chunk references
   of the indices the range touches (§4), release `FS.mu`, and go to step 3.
   Otherwise release `FS.mu`.
2. **Holding that entry's `openFile.mu`, and `FS.mu` for reading inside it.**
   Resolve the handle and apply both checks again. The answer comes from this
   hold: a handle that has gone since step 1 returns its error, `vfs.ErrStale`,
   and a permission change made since step 1 applies. Take the size, and the
   chunk references of the indices the range touches that are absent from
   `of.dirty`. Release `FS.mu`. Then, still holding `openFile.mu`, allocate the
   reply and copy into it the bytes the range needs from each index it touches
   that is present in `of.dirty`. Release `openFile.mu`.
3. **Holding no lock.** Fetch each recorded reference with `loadChunk`, and
   copy the bytes the range needs into the reply.

In steps 2 and 3, a hole, an index past the end of the chunk list, and bytes
past the end of a chunk or of a dirty buffer read as zeros.

The rules that go with the steps:

- `openFile.mu` is taken only while `FS.mu` is not held, and `FS.mu` only inside
  it: the order of ADR 0003 §3 and of the `FS.mu` doc comment in
  `internal/blobfs/fs.go`.
- `FS.mu` is read-locked at most once at a time, never recursively, which
  `sync.RWMutex` forbids (*Sources*).
- `Read` never calls `getOpen`, so it never adds an entry to `FS.open`. It never
  charges, releases or waits on the budget, and never calls anything that
  reaches `flushOpen`. It calls `loadChunk` only in step 3.
- The end of the range is `off + min(count, size − off)`, computed once
  `off < size` is known, so that it cannot wrap.
- Unchanged from before this ADR: the reply is exactly as long as the clamped
  range; `off >= size` returns no bytes, with `eof`; a zero `count` returns no
  bytes; and `Read` returns the errors it returned before, from the same
  checks.

### 3. The invariants the steps rely on

The code keeps all three today. They are pinned so that later changes keep them
too.

1. **One lock guards a file's buffered state.** Every change to an `openFile`'s
   `dirty` or `pendingTrim` is made holding that `openFile`'s `mu`. Every change
   to an inode's `Size` or `Chunks`, once the call that created the inode has
   returned, is made holding `FS.mu` for writing and the `mu` of the `openFile`
   that `FS.open` holds for the inode. The sites today are `bufferWrite`, its
   tail block included, `truncate`, `flushOpen`, and `dropIfGone`, which
   `flushOpen` calls. So a holder of `openFile.mu` sees the dirty map, the
   pending trims, the size and the chunk list as one state, and an index missing
   from `of.dirty` has its current contents in `n.Chunks`, except that a trim
   waiting in `of.pendingTrim` has not been applied to it (§1). ADR 0003 §4
   states this for `bufferWrite`; `Read` now relies on it too.
2. **An entry lives as long as its inode.** An `openFile` enters `FS.open` only
   through `getOpen`, and leaves it only through `dropOpen`, in the critical
   section that removes its inode from the inode table (ADR 0003 §2, site 5).
   Inode numbers are never reused (ADR 0003 Assumption 18). So while an inode
   is in the table, its `FS.open` entry, once made, stays the same `openFile`,
   and a successful resolve in step 2 means the entry step 2 locked is still
   the file's. `bufferWrite` relies on the same thing between `getOpen` and
   taking the lock. A change that removes the entry of a live inode, evicting
   idle entries for instance, must revisit both.
3. **No entry, no buffered state.** An inode with no `FS.open` entry has no
   dirty buffers and no pending trims, because both are only ever created in an
   `openFile` that `getOpen` has already put in `FS.open`. So step 1's view,
   taken under `FS.mu` alone, is complete. The entries `getOpen` can recreate
   for an inode that has already gone (#42) do not matter here: step 1 resolves
   the handle before it looks at `FS.open`.

### 4. What each lock is held for, and for how long

- **`FS.mu`, for reading, in step 1 or step 2:** resolving the handle, the two
  checks, the size, and the references of the indices the range touches, at
  most ⌈`count` ÷ chunk size⌉ + 1 of them, never a copy of the whole chunk list.
  The hold is proportional to the range, not to the file; `Read` before this
  ADR copied the whole list on every call. It matters because once a writer is
  waiting for `FS.mu`, every later reader waits behind it (*Sources*).
- **`openFile.mu`, in step 2:** the `FS.mu` section above, the allocation of
  the reply, and the copy of the dirty bytes in range: at most `count` bytes,
  and at most 1 MiB for an NFS `READ`, which the server caps at `maxReadSize`
  (`internal/nfs/nfs3.go`). That is a memory copy, unmeasured, and shorter than
  what already holds the same lock: `flushOpen` across its uploads, and
  `bufferWrite` across a chunk fetch.
- **No lock:** every store call and every cache call `Read` makes, all of them
  in step 3.
- **A file with no `FS.open` entry:** `Read` takes no `openFile.mu` at all. A
  file gets its entry from its first write or truncate on this mount and keeps
  it until it is removed (§3, invariant 2), so this is the path for a file that
  has not been written or truncated since the mount.

### 5. Why the fetches hold no lock

A reference names bytes that cannot change. Its key is the SHA-256 of its
contents, a chunk fetched from the bucket is checked against that hash unless
`SkipChunkVerification` is set, and the cache holds its own copy, which nothing
modifies (ADR 0004 §1). A flush after the view repoints an index at a chunk
holding either the bytes the view already copied from the dirty buffer or a
later write's; neither changes what the view's references name. So a fetch
made after the locks are released returns what the file held at the view.

This keeps what `Read` does today: a slow bucket holds up no `Write`, flush,
truncate or namespace operation. `loadChunk`'s rule that it is never called
holding `FS.mu` still holds, and so does ADR 0004 §3's statement that `Read`
calls it holding nothing.

It relies on every chunk a view names staying in the bucket until it is
fetched. Nothing deletes chunk objects today; the sweeper of `docs/DESIGN.md`
§8 is not implemented.

### 6. How a Read meets the file's other lock holders

- **A flush.** Every flush of a file is `flushOpen`: the ticker's `Sync`, the
  shutdown `Sync`, a `COMMIT`, a stable write's sync and a budget drain
  (ADR 0003 §4) all reach it. It holds `openFile.mu` from before its first
  pending-trim fetch or upload until it has repointed `n.Chunks` and replaced
  `of.dirty`. So step 2 comes entirely before a flush and copies the dirty
  bytes, which are the newest, or entirely after it and takes the new
  references. A flush that fails has repointed nothing and leaves the dirty
  buffers in place, with any pending trims it had materialised added, unless
  the inode has gone (ADR 0003 §2, site 4); step 2 then copies the dirty bytes.
  A `Read` of a file waits for a flush of that file that is in progress. It did
  before this ADR too, because the old `Read` also took `openFile.mu`.
- **`truncate`** holds `openFile.mu`, and `FS.mu` for writing, across the whole
  of its change, so step 2 comes before or after it, and takes the size and
  the chunk list as it left them, including a trim it queued in
  `of.pendingTrim` (§1).
- **The budget.** `Read` neither charges nor waits, so ADR 0003 §3's leaf rule
  and its two further rules are untouched. A drain is a flush.
- **A removal.** `dropOpen` (ADR 0003 §2, site 5) runs under `FS.mu` alone and
  leaves `of.dirty` in place. A `Read` whose step 2 resolved the handle before
  the removal copies bytes that were the file's at its view; one whose step 2
  comes after the removal returns `vfs.ErrStale`.
- **An attribute change.** A `SetAttr` that sets no size, whether of the mode,
  the owner, the group or the times, changes only state that `FS.mu` guards:
  those fields of the inode, its change time, and the flag that marks the
  namespace dirty. It takes `FS.mu` for writing and never an `openFile.mu`. So a
  flush of the file that is uploading, which holds the file's `openFile.mu`
  and not `FS.mu`, does not hold it up, though a commit holding `FS.mu` can
  (#43; ADR 0002 Assumption 7). It is ordered against step 1 and against step
  2's hold of `FS.mu`, and can fall between the two while a `Read` waits for
  the file's lock; step 2's checks then see it (§2). A `SetAttr` that sets a
  size is a truncate (above).
- **ADR 0004 §3.** `Read` copies out of `loadChunk`'s result into the reply and
  never writes to it or appends to it.
- **A commit holding `FS.mu` (#43).** Step 2 can wait for `FS.mu` behind a
  commit that holds it across its store calls, while holding the file's
  `openFile.mu`, so a flush or a write of that file queues behind the `Read`.
  Both would wait for `FS.mu` anyway, holding the same lock, but a flush's
  uploads, which need no `FS.mu`, start later.

### 7. The test surface, and how a test reaches each case

Following ADR 0002 §8, ADR 0003 §4 and ADR 0004 §7, this ADR pins no new
unexported name. The names those ADRs pin apply: `FS.open` in particular, read
holding `FS.mu` for reading. A test may rely on these behaviours:

- A `Read` of bytes that are neither buffered nor in the cache fetches their
  chunk with the store's `Get`, under the chunk's object key, in step 3,
  holding no filesystem lock. A fresh mount caches nothing (ADR 0004 §5).
- A `Write` that covers a chunk index from its first byte to its last fetches
  nothing for it (ADR 0003 §4: such a write "never starts from the old
  contents").
- A flush uploads each chunk of content new to the bucket and to this `FS` with
  the store's `Put`, holding the file's `openFile.mu` (§6; ADR 0004 §5).
- `Read` never adds an entry to `FS.open`.
- A `SetAttr` that sets no size takes `FS.mu` for writing and never an
  `openFile.mu`, so a flush of the file held in a chunk upload does not hold
  it up (§6, *An attribute change*).

How a test reaches each case:

- **#49, deterministically.** Seed a file of two chunks through another mount.
  On a fresh mount, write chunk index 1 whole, hold the store's `Get` of chunk
  0's key, start a `Read` whose range spans both indices, and wait until its
  `Get` has been entered. Then write a range that covers index 0 whole and the
  part of index 1 the `Read` needs, which fetches nothing, and release the
  `Get`. §1 requires the reply to be all from before that write or all from
  after it; the old `Read` returns index 0 from before and index 1 from after.
  That the write completes while the `Read` is held is §5's property.
- **#41.** The old `Read`'s window lies between two lock acquisitions with no
  store call inside it, so no store fake can mark it. A test holds a flush of
  the file in its chunk `Put`, which holds the file's `openFile.mu` and has
  repointed nothing yet, starts the `Read`, waits a fixed grace period for it
  to reach its first lock, and releases the flush. A correct `Read` returns the
  acknowledged write whatever the scheduling. The old one is caught if it took
  its first hold within the grace period.
- **Stress**, as #41's and #49's done-when ask: readers against a writer, with
  and without flushes, under `-race`, checking freshness and atomicity for each
  `Read` call. The race detector finds only races that happen in the run
  (*Sources*), so detection there is probabilistic.
- **What to avoid:** truncating into a stored chunk and then growing the file
  over it, and writing into an index with a pending trim (§1).

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required.

1. **§1's atomicity goes beyond both issues' text.** #41 asks for freshness, and
   #49 for a `Read` that never reads a buffer another goroutine can write.
   Atomicity is what POSIX asks of `read()` against `write()` and `ftruncate()`
   (*Sources*), and one critical section gives it at no extra cost, so it is
   written into the contract and tested.
2. **The dirty bytes are copied under `openFile.mu`,** rather than through
   copy-on-write buffers, clones of whole dirty buffers, or a reader–writer
   lock (*Alternatives considered*).
3. **`FS.mu` is released before the copy.** Nothing in the copy needs it
   (§3, invariant 1), and releasing it keeps the namespace lock's hold
   independent of how many bytes a `Read` copies.
4. **Only the references the range touches are copied,** where `Read` before
   this ADR copied the whole chunk list on every call.
5. **The checks are repeated in step 2, and the answer comes from there.** A
   handle removed between the steps now gets `vfs.ErrStale` where the old
   `Read` returned data from its first hold.
6. **Step 2 does not check that `FS.open` still holds the entry it locked.**
   §3's invariant 2 makes that follow from a successful resolve, and
   `bufferWrite` relies on the same invariant without a check. A check with a
   retry was considered and left out: it could not fire today, and a change
   that made it fire would have to revisit `bufferWrite` anyway.
7. **Fetching after the locks are released relies on nothing deleting chunks**
   (§5).
8. **A `Read` waits for a flush of the same file in progress.** It did before
   this ADR, and step 2 keeps it.
9. **The overflow-safe end of the range is folded in** (§2). `off + count` can
   wrap for an offset near 2^64, which a file can reach because nothing
   enforces the advertised maximum file size (#55). The lines are rewritten
   anyway; #55 itself is not decided here.
10. **The #41 test relies on a grace period** (§7). No hook is added to
    production code to mark the old `Read`'s window, which exists only in the
    code this ADR replaces.
11. **The rule is written into `docs/DESIGN.md` §7,** as ADR 0004 wrote its
    rule into §3, so that the redesign's read path inherits it.
12. **The costs in §4 are unmeasured.** I cannot run this server.
13. **Status is Accepted on creation,** following the precedent of ADR 0003 and
    ADR 0004. The repository owner may prefer Proposed until the change merges.
14. **Scope is #41 and #49 only.** Everything under *What this does not decide*
    is left alone on purpose.
15. **A `SetAttr` that sets no size stays off `openFile.mu`** (§6, §7). Added
    by the 2026-09-25 revision. The code and ADR 0002 Assumption 7 already had
    it; pinning it is the judgement. It keeps attribute changes, which touch no
    buffered state, off the lock that guards buffered state (§3, invariant 1),
    and it is the only way a test can change a file's permissions while a
    `Read` of the file waits for that lock. The cost is that ordering attribute
    changes against a flush through `openFile.mu` now needs a revision of this
    ADR. Such a change should come with one anyway: it would add a holder of
    the lock §6 describes, and a wait to ADR 0002 Assumption 7's list.

## Alternatives considered

- **Hold `openFile.mu` across the fetches too.** Simple, but a slow `Get` would
  hold up every write, flush and truncate of the file, a budget drain included.
- **Clone each touched dirty buffer under the lock and copy out afterwards.**
  Copies up to a whole chunk per index under the lock instead of the bytes
  needed, and allocates more.
- **Copy-on-write dirty buffers,** with `bufferWrite` cloning a buffer a `Read`
  still uses. Needs reference tracking, and each clone changes a buffer's
  `cap`, which ADR 0003 §2's accounting counts.
- **`openFile.mu` as a `sync.RWMutex`,** so that `Read`s of one file share it.
  Changes ADR 0003 §4's pinned test surface, where `openFile.mu` is a
  `sync.Mutex`, for a gain nobody has measured.
- **A per-file version counter, and a retry when it moved.** The copy out of a
  dirty buffer would still race with `bufferWrite` under the Go memory model.
- **`Read` calls `getOpen`.** Every file ever read would gain a permanent
  `FS.open` entry, every `Read` would take `FS.mu` for writing, and a file that
  is only read would take an `openFile.mu`.
- **Read the chunk list first, as now, and check it afterwards.** Fixes #41 but
  not #49, and needs a second `FS.mu` hold anyway.

## Consequences

- A `Read` returns what an acknowledged write left, and never a mixture of
  before and after one write: the symptoms of #41 and #49 are gone.
- The time a `Read` holds `FS.mu` no longer grows with the length of the file.
- A `Read` of a file that has an `FS.open` entry holds that file's
  `openFile.mu` for one reply's copy, so a write, flush or truncate of the file
  waits that long, and concurrent `Read`s of one file take turns for it.
- ADR 0002 Assumption 7 lists what a size-setting `SETATTR` can wait for on
  `openFile.mu`. A `Read`'s step 2 joins the list, bounded by one reply's copy,
  and the assumption's conclusion is unchanged.
- A flush of a file can start its uploads later while a `Read` of the file waits
  for `FS.mu` behind a commit (§6, #43).
- A `Read` that races the removal of its file can return `vfs.ErrStale` where
  it used to return data.
- The rule lives in `docs/DESIGN.md` §7, for the redesign.

## What this does not decide

- **What a pending trim means** to a reader, a writer and a flush (#40, #56;
  §1). Step 2 leaves room for the fix: it already holds the lock under which
  pending trims change.
- **`getOpen` recreating an entry for an inode that has already gone** (#42).
  `Read` is unaffected (§3, invariant 3).
- **Evicting idle `openFile` entries.** §3's invariant 2 says what that would
  have to revisit.
- **A doc comment on `vfs.FS.Read`** stating §1 for every backend.
  `internal/vfs` is unchanged by this ADR.
- **Concurrency among `Read`s of one file:** a reader–writer lock, or short
  reads that bound the lock hold for a caller other than the NFS server, which
  may pass any `count`.
- **The sweeper and in-flight views** (`docs/DESIGN.md` §8). A sweep must not
  delete a chunk that a view has named and not yet fetched, which the sweeper's
  age rule does not cover.
- **`bufferWrite`'s whole-list copy.** `bufferWrite` still copies a file's whole
  chunk list on every call, the cost §4 removes from `Read`.
- **#55,** beyond computing the range's end without wrapping.

## Sources

Fetched on 2026-09-25 through a tool that summarises what it fetches; quoted as
it returned them when asked for verbatim text.

- POSIX.1-2024, `write()`:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/write.html>.
  DESCRIPTION: "After a write() to a regular file has successfully returned:
  Any successful read() from each byte position in the file that was modified
  by that write shall return the data specified by the write() for that
  position until such byte positions are again modified." RATIONALE: "Writes
  can be serialized with respect to other reads and writes. If a read() of file
  data can be proven (by any means) to occur after a write() of the data, it
  must reflect that write(), even if the calls are made by different threads."
  (§1, freshness)
- POSIX, IEEE Std 1003.1-2001 (2004 edition), XSH 2.9.7 *Thread Interactions
  with Regular File Operations*:
  <https://pubs.opengroup.org/onlinepubs/009695399/functions/xsh_chap02_09.html>.
  "All of the functions chmod(), close(), fchmod(), fcntl(), fstat(),
  ftruncate(), lseek(), open(), read(), readlink(), stat(), symlink(), and
  write() shall be atomic with respect to each other in the effects specified
  in IEEE Std 1003.1-2001 when they operate on regular files. If two threads
  each call one of these functions, each call shall either see all of the
  specified effects of the other call, or none of them." (§1, atomicity)
- Linux `write(2)`, BUGS:
  <https://man7.org/linux/man-pages/man2/write.2.html>. Quotes POSIX.1-2008
  XSI 2.9.7, "All of the following functions shall be atomic with respect to
  each other in the effects specified in POSIX.1-2008 when they operate on
  regular files or symbolic links: ...", and names `write()` and `writev()`
  among them. (§1; secondhand for the 2008 wording)
- The Go memory model: <https://go.dev/ref/mem>. "A data race is defined as a
  write to a memory location happening concurrently with another read or write
  to that same location, unless all the accesses involved are atomic data
  accesses as provided by the sync/atomic package"; "races on multiword data
  structures can lead to inconsistent values not corresponding to a single
  write"; and "For any sync.Mutex or sync.RWMutex variable l and n < m, call n
  of l.Unlock() is synchronized before call m of l.Lock() returns."
  (*Context*, #49; §3)
- Go standard library, `sync.RWMutex`: <https://pkg.go.dev/sync#RWMutex>. "If
  any goroutine calls RWMutex.Lock while the lock is already held by one or
  more readers, concurrent calls to RWMutex.RLock will block until the writer
  has acquired (and released) the lock, to ensure that the lock eventually
  becomes available to the writer", and "this prohibits recursive
  read-locking". (§2, §4)
- The Go race detector: <https://go.dev/doc/articles/race_detector>. "The race
  detector only finds races that happen at runtime, so it can't find races in
  code paths that are not executed." (§7)

**Honesty note:** the POSIX.1-2017 and POSIX.1-2024 editions put XSH chapter 2
on one page, too long for the tool, which returned it cut off before 2.9.7. The
atomicity rule is therefore quoted from the 2004 edition, whose chapter 2 is
split into sections, and the 2008 wording only secondhand, from the Linux
manual page. I have not confirmed that later editions keep `ftruncate()` and
`read()` in the list.
