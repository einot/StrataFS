# 0006. A pending trim is part of the file, and is never buffered

**Status:** Accepted
**Date:** 2026-09-25
**Revised:** 2026-09-25 — the same day, after the security audit of the change
and before it merged. The bullet *The commit window* (#64) under *What this
does not decide* now states the window's preconditions as the audit found them
in the code: no crash is needed, because on a clean shutdown the server keeps
dispatching calls from open connections while the final `Sync`s run; a crash is
cheap anyway (#55); and a `WRITE` past the new end within the same chunk
exposes the removed bytes as well as growth does. It also lists stopping
dispatch before the final `Sync` among the options. The window is still left to
#64, and the decision and everything else are unchanged.
**Issue:** #40, #56

## Context

A truncate that ends inside a stored chunk has to shorten that chunk, and it
cannot fetch the chunk to do so. `truncate` holds the file's `openFile.mu`, and
`FS.mu` for writing, across the whole of its change (ADR 0005 §6), and
`loadChunk` must not be called holding `FS.mu` (its doc comment in
`internal/blobfs/fs.go`). So for a new size that ends inside a stored chunk
whose index is not dirty, `truncate` does one of two things. If this `FS`'s
chunk cache holds the chunk, it copies the chunk's first bytes out of the cache
into `of.dirty`, charged at once (ADR 0003 §2, site 3). Otherwise it appends a
trim, `trimReq{idx, to, ref}`, to the slice `of.pendingTrim`, and leaves
`n.Chunks[idx]` naming the whole chunk. The next `flushOpen` materialises each
queued trim whose index is not dirty by then: it fetches the chunk, copies its
first `to` bytes into `of.dirty`, charges them (site 4), and uploads them with
the file's other dirty chunks. It skips a trim whose index is already dirty,
and then empties the queue. Until then nothing else reads the queue:
`bufferWrite` and `Read` both take an index that is not dirty to hold what
`n.Chunks` names, whole.

POSIX says that once `ftruncate()` shortens a file "the extra data shall no
longer be available to reads on the file", and that once it lengthens one "the
extended area shall appear as if it were zero-filled". RFC 1813 §3.3.2 says the
same of a `SETATTR` of the size: a smaller size "causes data from new size to
the end of the file to be discarded", and a larger one "causes logically zeroed
data bytes to be added to the end of the file" (*Sources*). Five routes bring
the removed bytes back:

1. **A write into the chunk** (#40). A `Write` into the index loads the whole
   chunk, removed bytes included, and makes the index dirty. The flush then
   skips the trim, and the stored chunk keeps the removed bytes, which read as
   the old data once the file grows back over them.
2. **A second truncate at the same index** (#56 §2). Each truncate appends an
   entry, and the flush applies the first and skips the rest, because the
   index is dirty by then. With 1 MiB chunks, truncating to 5.5 MiB and then
   to 5.25 MiB keeps the old bytes from 5.25 to 5.5 MiB.
3. **A truncate below the entry** (#56 §2). A later truncate that cuts the file
   below the entry's index leaves the entry in place, and the flush's
   namespace update puts the shortened chunk back at that index, filling the
   gap with holes. Truncating to 5.25 MiB and then to 0 brings `n.Chunks[5]`
   back with its old first 0.25 MiB.
4. **A read after growth.** Before the next flush, once the file grows back
   over a trimmed index, by a `SETATTR` or by a write past the end, `Read`
   fetches the whole chunk there and returns the removed bytes where zeros
   belong. This is the exception ADR 0005 §1 names.
5. **Staging over a trim.** A truncate that misses the cache queues a trim. A
   `Read` then fetches the chunk, which caches it (ADR 0004 §5). A later
   truncate that grows the file within the chunk finds it cached, and copies
   its first bytes into `of.dirty` at the new, larger length, bringing back
   what the first truncate removed. The flush then skips the queued trim.

Nor does anything bound what a truncate costs in memory:

- **The queue** (#56 §1). A queued trim is charged nothing until the next
  flush, which then materialises and charges a file's whole queue in one pass,
  without waiting and without regard to the limit. At the defaults, 1,000
  `SETATTR`s in one commit interval hold about 1 GiB at that flush, four times
  `-max-dirty`, without a byte written, and 100,000 get the process killed for
  want of memory.
- **Holes.** A hole misses the cache, so a truncate into one queues a trim,
  which the flush materialises as a chunk of zeros. That spares the bytes but
  not the memory. Pruning entries past the end of the file and keeping one per
  index, which #56's done-when proposes, would not stop it: growing a file by
  a chunk and truncating into the new hole, a `SETATTR` to (k+1) × cs and then
  one to k × cs + cs/2 for k = 0, 1, 2 and so on, leaves one entry per two
  `SETATTR`s, each below the end of the file and at an index of its own.
- **Staging** (ADR 0003 Assumption 6). A truncate into a cached chunk is
  charged at once, without waiting, and holds its copy until the next flush,
  so every file that names a cached chunk can add up to a chunk between
  flushes.

Pruning the queue and keeping one entry per index closes routes 2 and 3, and
leaves routes 1, 4 and 5, holes and staging. The rest need a pending trim to
mean the same thing to every part of the code that reads a file's state, and
that is what this ADR decides. ADR 0003, ADR 0004 and ADR 0005 each left it
open (*What this does not decide* in each).

## Decision

### 1. What a pending trim means

For a chunk index that is not in `of.dirty` and has a pending trim `(to, ref)`,
the index holds the first `to` bytes of the chunk `ref` names, followed by
zeros. `Read` (§6), `Write` (§4) and a flush (§5) honour the trim, and
`truncate` (§3) only ever lowers it. So bytes that a truncate removed never
reach a reply, a dirty buffer or the bucket.

### 2. Representation and invariants

`openFile.pendingTrim` is a `map[uint64]trimReq`, keyed by chunk index. A value
records `to` and `ref`; its fields are not pinned. The map is guarded by
`openFile.mu`, as ADR 0005 §3's invariant 1 already requires of every change to
it, and `truncate` also holds `FS.mu` for writing when it changes the map.

Four invariants hold whenever the map is observed holding `openFile.mu`:

- **I1.** An index is never in both `of.dirty` and `of.pendingTrim`.
- **I2.** An entry at `idx` exists only while `n.Chunks[idx]` names a stored
  chunk, not a hole, whose `Size` is greater than the entry's `to`, and the
  entry's `ref` is that chunk.
- **I3.** `idx × cs + to <= size`, where `size` is the file's size. It follows
  from §3, because only a truncate lowers a file's size.
- **I4.** A file has at most one entry. It follows from §3 and §5. Entries are
  made only at a truncate's tail index, and that truncate cuts the chunk list
  to end there. Only a successful flush puts a stored chunk back at a higher
  index, and a successful flush empties the map; growing the file appends
  holes, and a hole takes no entry. So no truncate finds a stored chunk above
  an existing entry: a later one keeps the entry, lowers it, or drops it and
  makes at most one at a lower index.

§7's bound on memory does not rest on I4, because a flush applies trims one at
a time. Its bound on the store calls a flush makes does.

### 3. `truncate`

`truncate` keeps its locks, and what it does to the size, the chunk list and
the dirty map. What changes is what it does about a chunk it cannot fetch.
Holding `openFile.mu` and `FS.mu` for writing, with `lastIdx` and `tail` the
new size's chunk index and its offset within that chunk:

- **(a)** It drops every entry with `idx > lastIdx`, and the entry at `lastIdx`
  when `tail == 0`. That is the rule by which it already deletes dirty indices
  and cuts `n.Chunks`.
- **(b)** If `tail != 0`, `lastIdx` is not in `of.dirty`, and the chunk list,
  after the cut, reaches `lastIdx` with a stored chunk `ref` there: let `v` be
  the `to` of the entry at `lastIdx` if there is one, and `ref.Size` otherwise.
  If `tail < v`, it sets the entry at `lastIdx` to `(tail, ref)`; otherwise it
  leaves the map alone. The smallest length wins, so a truncate that grows the
  file within the chunk keeps the trim an earlier one made, and the bytes
  between the two lengths read as zeros.
- **(c)** A hole takes no entry. Its bytes read as zeros past any length.
- **(d)** It never reads the chunk cache, never stages a buffer in `of.dirty`,
  never charges the budget and never waits. It still releases the dirty
  buffers it deletes (ADR 0003 §2, site 3), and still shortens a dirty tail in
  place, by a reslice.

### 4. `Write`

When `bufferWrite` fills an index that is absent from `of.dirty` and has an
entry, it fetches the chunk the list names, as it does for any such index, and
still holds `openFile.mu` while it does (ADR 0003 §4). It keeps only the first
`to` bytes of what it fetched, or all of it if that is shorter, and copies them
into a new buffer of capacity `cs`, so the bytes past `to` read as zeros unless
the write puts something there. It cuts by reslicing and copies out; it never
writes into or appends to what `loadChunk` returned (ADR 0004 §3). It removes
the entry whenever it stores `of.dirty[idx]`, in the same hold of
`openFile.mu`, including for a write that covers the index whole and so fetches
nothing. A fetch that fails stores nothing for the index and leaves the entry.
The charge is `+cs`, as for any index new to `of.dirty` (ADR 0003 §2, site 1).

### 5. A flush

`flushOpen` returns early only when both `of.dirty` and `of.pendingTrim` are
empty. Otherwise, holding `openFile.mu` throughout, as today, it applies the
entries one at a time. For each, it fetches `ref` with `loadChunk`, copies the
first `min(to, len)` bytes of the result into a buffer of its own, uploads that
buffer with `putChunk`, and keeps only the reference `putChunk` returns. The
buffer never enters `of.dirty` and is never charged, and nothing of it is kept
once it is uploaded. The flush uploads the dirty chunks as today, and repoints
`n.Chunks` at the trimmed chunks and at the dirty ones in the same hold of
`FS.mu`, or at nothing if the inode has gone, as today. When it succeeds it
resets both `of.dirty` and `of.pendingTrim`.

A fetch or an upload that fails repoints nothing, and leaves `of.dirty` and
`of.pendingTrim` as they were, so the file's next flush applies the trims
again; a shortened chunk that was already uploaded is then found known and not
uploaded twice. Site 4's check on a failed return is as ADR 0003 §2 describes.

The order in which a flush fetches and uploads is not pinned.

A flush meets no entry for a dirty index: I1 rules it out. The code before this
ADR had that case, and skipped the trim in it, and that skip is what produced
#40.

**Why a copy of its own, and not a reslice of what `loadChunk` returned.** The
result may be the cache's own slice (ADR 0004 §3). `putChunk` hands its
argument to the store's `Put`, and `store.Store` in `internal/store/store.go`
does not promise that `Put` leaves `data` alone. Uploading a reslice would
stake the cache's immutability (ADR 0004 §1) on a property no store has
undertaken to keep, although both stores in this repository only read `data`
today. The copy costs at most one chunk's memcpy per trim.

### 6. `Read`

In ADR 0005 §2's step 2, holding `openFile.mu`, `Read` takes with each
reference it records, for an index the range touches that is absent from
`of.dirty`, the `to` of the entry at that index, if there is one. It looks
entries up index by index across the range, never by walking the map. In step 3
it copies from each fetched chunk only the bytes below that length; the rest of
the range at that index reads as zeros. Step 1 is unchanged: an inode with no
`FS.open` entry has no pending trims (ADR 0005 §3, invariant 3). `loadChunk` is
still called only in step 3, holding no lock.

Step 2 may leave out a reference whose part of the range lies wholly at or past
its trim's length, which saves a fetch, and it need not. A test must not rely
on either.

### 7. The budget, and what a truncate costs

ADR 0003 §2's sites 3 and 4 only release, and site 1 alone adds. `truncate`
does not wait on the budget, because it adds nothing for a wait to bound.

Before this ADR (*Context*):

- the queue was charged nothing until a flush, which then charged all of it at
  once: at the defaults, 1,000 `SETATTR`s in one commit interval held about
  1 GiB at that flush, and 100,000 exhausted memory (#56);
- pruning the queue and keeping one entry per index would not have bounded it:
  the sequence through holes keeps one entry per two `SETATTR`s, each
  materialised at half a chunk, about 0.5 GiB per 2,000 `SETATTR`s at the
  defaults, and about twice that with lengths nearer the end of each chunk;
- staging was charged at once, without waiting, and held until the next flush:
  `F` files that named cached chunks could hold about `F` chunks.

After it:

- a file has at most one entry (I4), and none for a hole;
- a truncate charges nothing, and allocates nothing the size of a chunk;
- for each file with an entry, a flush makes at most one `Get` of the trimmed
  chunk, none if this `FS`'s cache holds it, and at most one `Head` and one
  `Put` of the shortened copy, none if its content is known;
- ADR 0003 §6's bound, `MaxDirtyBytes + k × W`, covers everything the budget
  counts, with no term for `SETATTR`;
- while it applies a trim, a flush holds the chunk it fetched and the shortened
  copy, at most two chunks by length (the store promises nothing about the
  capacity of what `Get` returns; ADR 0004 *Context*), and, on a miss, the
  cache's own copy of the fetched chunk, which `-cache` bounds. A flush applies
  one trim at a time, and flushes run inside calls, the ticker or shutdown, so
  the flushes in progress are bounded as `k` is (ADR 0003 §6): by the calls
  running at once, 64 per connection, with connections uncapped (ADR 0003
  Assumption 19). At the defaults that is up to 128 MiB per connection outside
  every configured pool, with no bound in total. Per call it is no more than a
  `READ` can hold at its peak: its reply, up to 1 MiB (`maxReadSize` in
  `internal/nfs/nfs3.go`), a chunk it fetched, and the cache's copy of that
  chunk, up to 3 MiB at the defaults.

These figures are arithmetic from the code, not measurements (Assumption 13).

### 8. The test surface

Following ADR 0002 §8, ADR 0003 §4, ADR 0004 §7 and ADR 0005 §7, the names
those ADRs pin apply. This ADR changes one of them: `openFile.pendingTrim` is a
map from chunk index to `trimReq` (§2), where ADR 0003 §4 pinned a slice. A
test may read its length and its keys holding `openFile.mu`; the value's fields
are not pinned. A test may rely on these behaviours:

- A truncate makes no store call and never raises `DirtyBytes()`.
- After any truncate a file has at most one entry, none for an index whose
  chunk is a hole, and none for an index in `of.dirty`.
- I1, observed holding `openFile.mu`.
- A `Write` into an index that has an entry removes the entry. A `Write` that
  covers an index whole fetches nothing for it (ADR 0005 §7).
- A flush applies each entry with at most one store `Get` of its chunk's key,
  none if this `FS`'s cache holds the chunk, and at most one `Put`, none if the
  shortened content is known, holding the file's `openFile.mu`, and without
  changing `DirtyBytes()`. When it succeeds no entries remain. When a fetch or
  an upload fails the entries stay, unless the inode has gone (ADR 0003 §2,
  site 4).

A test must not rely on the order of a flush's uploads (§5), or on whether a
`Read` fetches a chunk whose part of its range lies wholly past a trim (§6).

How a test reaches each case:

- **A cold chunk:** seed the file through another mount, as the existing tests'
  `bpSeedFile` does, then mount the bucket afresh. `New` leaves the cache empty
  (ADR 0004 §5).
- **A cached chunk:** this `FS`'s own first upload of the content, or a `Read`
  on this `FS` that fetched it (ADR 0004 §5).
- **A hole:** grow the file with a `SETATTR` of the size.
- **Route 5 of *Context*:** truncate into a cold chunk, `Read` it, then
  truncate to a larger size within the same chunk.
- **A flush held in a fetch or an upload:** a store fake that gates the `Get`
  or `Put` of chunk keys, as the existing tests' `bpStore` does.

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required, except where it says otherwise.

1. **Only the smallest length is kept** (§3(b)). A later, larger truncate
   within the chunk grows the file, and the bytes between the two lengths must
   read as zeros, which only the smaller length keeps. The history of lengths
   is not needed: a length at or above the kept one changes nothing a reader
   can see.
2. **A hole takes no trim** (§3(c)). A hole reads as zeros past any length, so
   a trim on it changes nothing a reader can see. The code before this ADR
   queued one only because a hole misses the cache, and its flush then stored
   a chunk of zeros where the hole was. RFC 1813 §3.3.2 leaves it to the server
   whether zeros are holes or stored bytes (*Sources*).
3. **`truncate` never stages and never reads the cache** (§3(d)). The cost:
   the flush copies the chunk's first bytes instead of `truncate` doing it,
   and fetches the chunk again if the cache has evicted it in between. The
   gain: a truncate costs no memory and no memcpy, holds `FS.mu` for less
   time, and has no second path through which route 5 of *Context* could come
   back.
4. **A trim is applied through a transient, uncharged copy, not through
   `of.dirty`** (§5). The budget bounds buffers that wait for a flush; this
   copy exists for one upload inside a flush. `Read`'s reply and the chunk
   `bufferWrite` fetches are transient and uncharged in the same way.
5. **A copy rather than an upload of a reslice** (§5, *Why a copy of its
   own*).
6. **A map, not a slice** (§2). One entry per index is then a property of the
   type rather than of every caller, and `len` still reads it. It changes
   ADR 0003 §4's pinned test surface; a test that reads only the length
   compiles against either.
7. **`bufferWrite` keeps its fetch under `openFile.mu`** (§4). Moving the fetch
   out would reopen #37's race (ADR 0003 §4, *The chunk list is read after the
   wait, under `openFile.mu`*), and ADR 0005 §4 already counts that hold.
8. **`Read` may skip a fetch that a trim makes pointless, and need not** (§6).
   Requiring the skip would pin an optimisation; forbidding it would pin a
   wasted fetch.
9. **Site 4's check on a failed flush stays, though it now releases nothing in
   practice.** A flush charges nothing, and whatever site 1 charges after
   site 5 has run, site 2 releases in the same hold of `openFile.mu`, so a
   flush that fails finds nothing charged to a removed file. The check still
   resets the file's buffers and trims, which stops another flush from
   uploading them again, and it keeps the accounting exact if a later change
   charges in a flush again.
10. **ADR 0003 Assumption 21 is kept.** Site 2 still leaves `pendingTrim`
    alone when it finds the inode gone. A flush of the removed file that was
    already listed fetches and uploads one shortened copy, charging nothing.
11. **The order of a flush's uploads is not pinned** (§5), as ADR 0004 §7 left
    it.
12. **No doc comment on `vfs.FS.SetAttr`.** POSIX and RFC 1813 already say
    what a size change does. A comment stating it for every backend is left
    open (*What this does not decide*).
13. **The figures in §7 are unmeasured.** I cannot run this server.
14. **Status is Accepted on creation**, following ADR 0003, ADR 0004 and
    ADR 0005. This one is not a judgement of mine: the repository owner
    approved the design on 2026-09-25 with this status.
15. **Scope is #40 and #56 only.** Everything under *What this does not
    decide* is left alone on purpose.

## Alternatives considered

- **Keep the latest length for an index.** Wrong when a truncate grows the file
  within the chunk: it would keep the larger length and bring back what the
  earlier, smaller one removed.
- **Prune and deduplicate the queue, and nothing else**, as #56's done-when
  proposes. It closes routes 2 and 3 of *Context*. Holes, `Read`, staging, and
  both paths that charge, the flush's materialisation and staging from the
  cache, remain.
- **`truncate` waits on the budget** before it takes its locks, as `Write`
  does. After this ADR there is nothing for it to bound, and it would hold
  every size-setting `SETATTR` behind a drain, including one that shrinks a
  file and frees memory. ADR 0003 Assumption 6 records why it was not chosen
  before.
- **Charge a trim when it is queued.** The charge would count memory not yet
  held, which breaks ADR 0003 §2's invariant that `of.bytes` is the capacity in
  `of.dirty`, and `truncate` could not wait for it anyway.
- **Materialise trims into `of.dirty` in bounded batches**, charging each
  batch. §5 is the limiting case: a batch of one that is never buffered, which
  needs no charge and no batch size.
- **`truncate` resolves the trim itself**, fetching the chunk holding
  `openFile.mu` before it takes `FS.mu`. Every such `SETATTR` would make a
  store round trip holding the file's lock, ADR 0002 Assumption 7 would no
  longer hold, and the buffer it made would need a charge that `truncate`
  could not wait for.
- **Record the trimmed length in the chunk list and the snapshot**, as
  `chunkRef.Size`. No queue and no memory, and it would close the commit window
  under *What this does not decide*. But it changes the snapshot format: a
  build that predates it reads a shortened reference whole, and would show the
  removed bytes once the file grows, so it needs a format version. That is the
  repository owner's call, and the redesign's leaf spans (`docs/DESIGN.md` §4)
  are its natural home.
- **Keep staging from the cache, and honour the entry when staging.** Closes
  route 5, but leaves the memory that staging charges unbounded.

## Consequences

- Bytes a truncate removed stay removed on every route in memory: a reply, a
  write, a second truncate and a flush all honour the trim.
- A `SETATTR` adds nothing to buffered memory, and a flush holds at most two
  chunks at a time for a trim. #56's exhaustion through the queue, and
  ADR 0003 Assumption 6's through staging, are gone.
- `truncate` holds `FS.mu` for less time: no cache lookup and no memcpy.
- A flush that applies a trim to a chunk the cache has evicted fetches it
  again, where staging would already have copied it.
- A truncate into a hole leaves the hole: the flush no longer stores a chunk of
  zeros there, and the file's `used` attribute no longer counts one.
- The tests pinned to staging, and to materialisation into `of.dirty`, change
  with it: ADR 0003 §2's table, and ADR 0004 §7's recipe for reaching #46
  through a truncate.

## What this does not decide

- **The commit window** (#64). `Sync` flushes the open files and only then
  takes `FS.mu` and commits. A truncate that lands between the two leaves its
  trim in memory, as a pending trim or as a dirty tail, while the commit stores
  the new size together with the longer chunk. If the process ends before a
  later commit applies the trim, the bucket keeps that pairing, and the removed
  bytes come back once the file grows over them: through a `SETATTR` that grows
  it, or through a `WRITE` past the new end within the same chunk, which loads
  the whole stored chunk, because no trim survives the restart, and so makes
  the removed bytes between the new end and the write's offset part of the
  file, for the next flush to upload.

  No crash is needed. On shutdown `Serve` in `internal/sunrpc` closes only its
  listener. `serveConn` goes on reading records from the connections already
  open, and `readRecord` has no deadline; once the context is done, the
  `select` in `serveConn` chooses at random between dispatching the record it
  has read, when a slot is free, and returning. So `SETATTR`s are still
  dispatched while the shutdown `Sync` in `cmd/strata` and the committer's
  final `Sync` run, and the process exits as soon as the shutdown `Sync`
  returns, without waiting for the committer's. A client that keeps sending
  during a clean shutdown can land a truncate in the window of one of those
  final `Sync`s, whose commit may then be the last. A crash is cheap as well:
  #55 lets any local user who can reach the server's port crash it on demand.

  It predates this ADR, affects a dirty tail as well as a pending trim, and is
  neither widened nor narrowed here. Nothing in the protocol makes a client
  resend a `SETATTR` after a restart: the write verifier covers `WRITE` and
  `COMMIT` only (RFC 1813 §3.3.7), and §1.6 does not list `SETATTR` among the
  procedures that are synchronous. The options are a gate that orders a commit
  against the files' `openFile.mu`, a committed trim length (a format version;
  *Alternatives considered*), stopping dispatch before the final `Sync`, or
  accepting it.
- **#42, #43 and #55.** `getOpen` can recreate an entry for an inode that has
  already gone, but `truncate` resolves the handle again under both locks
  before it touches the map, so such an entry never gets a trim (#42). A commit
  holding `FS.mu` across its store calls still holds up a truncate (#43). A
  `SETATTR` to a huge size still appends a hole to the chunk list for every
  index up to it, holding `FS.mu`. Holes take no trim, so this ADR changes
  nothing there, and after it that is the cheapest way left to exhaust memory
  with `SETATTR` (#55).
- **The redesign's truncate** (`docs/DESIGN.md` §4, §5), which shortens a
  leaf's span in the file tree.
- **A doc comment on `vfs.FS.SetAttr`** stating the zero-fill rule for every
  backend.
- **`store.Store.Put`'s contract**: whether an implementation may modify
  `data`. §5 copies rather than depend on it.

## Sources

Fetched on 2026-09-25 through a tool that summarises what it fetches; quoted as
it returned them when asked for verbatim text. Each was fetched a second time
the same day to check the quotes, and they matched.

- POSIX.1-2024 (IEEE Std 1003.1-2024), `ftruncate()`:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/ftruncate.html>.
  DESCRIPTION: "If the size of the file previously exceeded *length*, the extra
  data shall no longer be available to reads on the file." and "If the file
  previously was smaller than this size, ftruncate() shall increase the size of
  the file. If the file size is increased, the extended area shall appear as if
  it were zero-filled." (*Context*, §1)
- RFC 1813, *NFS Version 3 Protocol Specification*:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>.
  - §3.3.2, `SETATTR`: "The new_attributes.size field is used to request
    changes to the size of a file. A value of 0 causes the file to be
    truncated, a value less than the current size of the file causes data from
    new size to the end of the file to be discarded, and a size greater than
    the current size of the file causes logically zeroed data bytes to be added
    to the end of the file." Checked against the rendering at
    <https://datatracker.ietf.org/doc/html/rfc1813>, which returned the same
    words and continues: "Servers are free to implement this using holes or
    actual zero data bytes. Clients should not make any assumptions regarding a
    server's implementation of this feature, beyond that the bytes returned
    will be zeroed." (*Context*, §1, Assumption 2)
  - §3.3.7, `WRITE`, on `verf`: "This is a cookie that the client can use to
    determine whether the server has changed state between a call to WRITE and
    a subsequent call to either WRITE or COMMIT." (*What this does not
    decide*)
  - §1.6: "The following data modifying procedures are synchronous: WRITE
    (with stable flag set to FILE_SYNC), CREATE, MKDIR, SYMLINK, MKNOD, REMOVE,
    RMDIR, RENAME, LINK, and COMMIT." `SETATTR` is not among them. (*What this
    does not decide*)

**Honesty note:** every fetch of RFC 1813 stopped at §3.3.7, so §3.3.21,
`COMMIT`, was not read, and nothing here quotes what it says of the verifier.
