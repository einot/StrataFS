# 0004. The chunk cache owns its bytes and charges what it holds

**Status:** Accepted
**Date:** 2026-09-24
**Issue:** #46, #47

## Context

`internal/blobfs/cache.go` keeps a byte-bounded LRU of chunk contents, keyed by
chunk hash. Its doc comment states the premise it rests on: chunks are
content-addressed, so "a cached entry can never go stale: the key is the hash
of the value". `docs/DESIGN.md` §3 and §7 say the same of the redesign's cache.
The premise holds only while an entry's bytes cannot change after they are
cached, because nothing checks them again: `loadChunk` returns a hit without
hashing it, and chunk verification (`-verify-chunks`) applies only to chunks
fetched from the bucket.

**#46: a cached chunk can change.** `putChunk` caches the slice it is given, and
`flushOpen` gives it `of.dirty[idx]`, the file's dirty buffer itself. When every
upload succeeds, `flushOpen` replaces `of.dirty` and nothing writes to that
buffer again. When a later upload fails, `flushOpen` returns the error and
leaves `of.dirty` as it was, unless the inode has gone (ADR 0003 §2, site 4);
a panic in a later upload leaves it the same way. The buffers already uploaded
are then both dirty and cached, and two things can change one of them in place:

- a `Write` to that index: `bufferWrite` finds the index in `of.dirty` and
  copies the new bytes into the buffer it finds there;
- a truncate into that index, which reslices the dirty buffer in place
  (`of.dirty[lastIdx] = chunk[:tail]`), followed by a write past the new end,
  which `append`s into the spare capacity of the same array.

The cache's entry for the original hash then holds bytes that do not hash to
it. Any file on the mount whose chunk list names that hash reads them through a
hit, and because chunks are content-addressed, that can be an unrelated file
whose content happened to match. Three paths copy cached bytes into a buffer
that the next flush stores: `bufferWrite` loading a chunk it partly overwrites,
`truncate` staging a shortened chunk from the cache, and `flushOpen` resolving
a pending trim. The wrong bytes are then stored under the hash of what they
are, so the object in the bucket matches its key, no verification can object,
and the damage is permanent. The file whose flush failed reads its own dirty
buffers rather than the cache, which is why nothing looks wrong where the fault
began. The bucket's object under the original hash is correct throughout.

**#47: the charge is not what the cache holds.** `put` charges `len(data)`. The
write path allocates dirty buffers at the chunk size, `cs`: `bufferWrite` makes
one with `make([]byte, 0, cs)`, or copies a loaded chunk into one. So a flushed
chunk of `L` bytes is charged `L` and holds `cs`. At the defaults, a 1 MiB
chunk size and a 256 MiB cache, each small file this mount uploads leaves a
1 MiB buffer in the cache, and a cache charged its full 256 MiB for 4 KiB
chunks holds 64 GiB. Chunks fetched from the bucket are cached as the store
returned them, and neither `os.ReadFile`, behind `store.Local`, nor
`io.ReadAll`, behind `store.S3`, documents anything about the capacity of what
it returns (*Sources*), so charging `len` for those is not a bound either.

ADR 0003 counts dirty buffers by capacity for the same reason, and deferred
both issues: its Assumption 2 notes that the cache's half of the two memory
pools is not a real bound, and its *What this does not decide* leaves the fix
to #47 and #46.

## Decision

### 1. The cache stores its own copy

`put(hash, data)` stores a copy of `data` that it allocates with
`make([]byte, len(data))` and fills with `copy`. It never keeps `data` itself.
The copy is complete when `put` returns, and the caller may then change,
reslice, append to or reuse `data`. `make` sets the capacity equal to the
length (*Sources*). A copy built with `bytes.Clone` or `append` does not
promise that: `bytes.Clone`'s result "may have additional unused capacity",
and `append` chooses the capacity of any new array it allocates.

`put` makes the copy after its size guard and before it takes the cache's
mutex. `truncate` reads the cache while holding `FS.mu`, so time spent under the
cache's mutex is time the whole namespace can wait. A `put` that then finds its
hash already present discards its copy.

It follows that every entry is immutable while it is cached. An entry cached
by `putChunk` matches its hash by construction: `putChunk` hashes, uploads and
caches the same bytes, and `flushOpen` holds the file's `openFile.mu`
throughout, so nothing writes to them in between. An entry cached by
`loadChunk` matches its hash when verification is on. With
`SkipChunkVerification` it holds whatever the bucket returned, as it does
today.

### 2. The charge is what an entry holds

Each entry is charged `len(data)` when it is inserted and released by the same
amount when it is evicted. Because `cap == len` (§1), that is what the entry
holds. Whenever the cache's mutex is not held, the charge equals Σ `len` and
Σ `cap` over the entries, and is at most `maxBytes`.

Three existing behaviours are kept, and are now part of the contract:

- An entry longer than `maxBytes` is not stored, and `put` changes nothing.
- A `put` of a hash already present keeps the existing entry, charges nothing,
  and refreshes the entry's recency.
- Eviction removes the least recently used entry first, where an insertion, a
  `put` of a present hash and a hit each count as a use.

Not charged: whatever the allocator rounds up beneath `cap`, and per-entry
overhead — the map entry, the list element, the entry header and the hash
string. ADR 0003 leaves map and slice-header overhead uncounted in the same
way.

`Config.CacheBytes` is `maxBytes`; zero or less selects 256 MiB, as today
(ADR 0003 Assumption 17 records how `-cache` reaches it). The `cacheBytes` that
`FS.Stats` reports is this charge.

### 3. What `get` hands out

`get` returns the cache's own slice, shared with every caller and with every
later hit. A caller must not write through it, and must not append to it or to
any reslice of it: appending to a reslice shorter than its capacity writes into
the cache's array. `loadChunk`'s result falls under the same rule whether it was
a hit or a miss, because its callers cannot tell which they got. `loadChunk`'s
callers, and `truncate`, keep the rule today:

- `Read` copies what it needs out of the result into the reply;
- `bufferWrite` copies the result into a new buffer before writing into it;
- `flushOpen`, resolving a pending trim, reslices the result and then copies
  it;
- `truncate`, staging a shortened chunk from the cache, copies `data[:tail]`.

A new consumer copies before it modifies.

The cache's mutex is a leaf lock. It is taken while holding an `openFile.mu`
(`putChunk` and `loadChunk` under `flushOpen`, `loadChunk` under
`bufferWrite`), while holding both an `openFile.mu` and `FS.mu` (`truncate`),
or while holding nothing (`loadChunk` called from `Read`, and `FS.Stats`).
Nothing is acquired and nothing is logged while it is held.

### 4. Hits are not verified

A hit is trusted, as it is today. Verifying it was considered and rejected:

- **Cost.** A hit is a map lookup. A verified hit hashes the whole chunk on
  every `READ` that touches it, because `Read` calls `loadChunk` once for each
  chunk its range touches. At the advertised read size of 1 MiB (`maxReadSize`
  in `internal/nfs/nfs3.go`, sent as both `rtmax` and `rtpref`) with 1 MiB
  chunks, that hashes one to two times the bytes served; a client reading
  64 KiB at a time would hash sixteen times what it reads. Unmeasured.
- **Redundant after §1.** The mechanism behind #46 is gone. What remains is a
  caller breaking §3, which tests catch, or memory corruption below this layer.
- **No policy.** A hit that failed verification would need one — evict and
  refetch, log, count — and nothing has specified it.

`-verify-chunks` keeps its documented meaning: chunks fetched from the bucket
are verified.

### 5. When the filesystem caches a chunk

This is today's behaviour, pinned so that a test can predict which entries
exist:

- `putChunk` caches a chunk as soon as its upload succeeds, whatever then
  happens to the rest of the flush.
- `loadChunk` caches a chunk fetched from the bucket once it passes
  verification, or at once when verification is skipped.
- Nothing else is cached: not an upload skipped because this `FS` has already
  stored or fetched that hash, not one skipped because the `HEAD` before the
  upload found the object, not a hole, not a fetch that fails, and not a fetch
  that fails verification.
- `New` leaves the cache empty: mounting reads the root pointer and the
  snapshot from the store directly.

The cache is keyed by the chunk's hash as it appears in the chunk's object key
after `chunkPrefix`: the lowercase hex SHA-256 of its bytes. This agrees with
ADR 0003 §2, *Staging from the cache*.

### 6. What the copy costs

Each uploaded chunk costs one allocation and one memcpy of its length, under the
`openFile.mu` the flush already holds. The flush already hashes the same bytes
with SHA-256 and uploads them, and both cost more. Each cache miss costs the
same, after a network fetch and a verification hash. None of this is measured.

After a successful flush the dirty buffers are released (ADR 0003 §2, site 4)
and the cache holds exact-length copies. While a flush runs, a file's dirty
buffers and the copies of its uploaded chunks exist side by side, so live
memory can reach both configured pools together, the dirty budget and
`CacheBytes`, which is what ADR 0003 Assumption 2 already tells an operator to
budget for. Writes of whole chunks now allocate every uploaded byte twice,
where the cache used to keep the flushed buffer. Small files and partly written
chunks save far more: at the defaults a cache charged 256 MiB for 4 KiB chunks
could hold 64 GiB (*Context*), and now holds what it is charged.
Garbage-collector headroom is out of scope, as it is in ADR 0003.

### 7. The test surface

Following ADR 0002 §8 and ADR 0003 §4, these unexported names are part of this
ADR's interface, so that a clean-room test in package `blobfs` can reach them:

```go
type FS struct {
	// cache is set by New and never reassigned. A test may read the field
	// without holding any lock.
	cache *chunkCache

	// The other fields are not pinned here.
}

// newChunkCache returns an empty cache bounded to maxBytes; zero or less
// selects 256 MiB.
func newChunkCache(maxBytes int64) *chunkCache

// put stores a copy of data under hash (§1, §2).
func (c *chunkCache) put(hash string, data []byte)

// get returns the cache's own bytes for hash, which must not be modified
// (§3). It counts a hit or a miss, and a hit refreshes the entry's recency.
func (c *chunkCache) get(hash string) ([]byte, bool)

// stats reports the hits and misses counted so far and the charge in bytes,
// which is the number FS.Stats reports as cacheBytes.
func (c *chunkCache) stats() (hits, misses, bytes int64)
```

A test may call all of them. It must never modify what `get` returns. Renaming
any of them changes this ADR's interface. The cache's fields, its entry type and
its eviction structure are not pinned. #47 asks for a test that caches a short
buffer with a large capacity and checks the charge; `newChunkCache`, `put`,
`get` and `stats` are enough for it.

**How a test reaches #46.** The fault needs a particular sequence:

- The content must be new to the bucket, so that the flush uploads it: an
  upload that is skipped caches nothing (§5).
- The store's chunk `Put` must fail after the flush's first chunk upload has
  succeeded.
- The order in which `flushOpen` uploads a file's chunks is not pinned, so a
  test either touches every dirty index of the file or uses `get` to find which
  chunk was cached.
- The file whose flush failed reads its own dirty buffers, so the wrong bytes
  show only through another file that names the same hash. That file must come
  to name it after the failed flush uploaded it: written with the same content
  and flushed afterwards, which `putChunk` recognises as already stored and
  neither uploads nor caches again. Had the other file stored that content
  first, the failing flush would have skipped the upload and cached nothing.
- A write into that second file that partly overwrites the chunk, or a truncate
  into it that stages from the cache, then stores whatever the cache holds,
  which a fresh mount shows.

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required.

1. **The copy lives in `put`, not only in `putChunk`.** #46 needs a copy only
   where `putChunk` caches a dirty buffer. Making it in `put` makes ownership a
   property of the cache rather than of each caller, covers `loadChunk`'s
   fetched slices, whose capacity is not promised, and covers any put site
   added later.
2. **`cap == len`, rather than charging `cap`.** #47 allows either. Charging
   `cap` without copying would leave #46 open and spend the cache on the unused
   part of partly written chunks.
3. **`make` and `copy`, with allocator rounding and per-entry overhead not
   charged.** `cap` is what a test can see. What the allocator rounds up beneath
   it is invisible to Go code, and the overhead is of the kind ADR 0003 leaves
   uncounted for the dirty pool.
4. **Hits are not verified** (§4).
5. **The copy is made outside the mutex.** Two misses of the same chunk racing
   to cache it both copy, and one copy is thrown away. That is rare, and
   cheaper than holding the lock for a memcpy.
6. **The three existing behaviours in §2 are kept** — the size guard, a put of a
   present hash, least-recently-used eviction — and pinned, so that tests can
   rely on them.
7. **Caching at upload time is kept, and pinned** (§5), including for the
   uploads of a flush that later fails. The alternative, caching only once the
   whole flush has succeeded, is weighed below.
8. **The cost figures in §4 and §6 are unmeasured.** I cannot run this server.
9. **Status is Accepted on creation,** following ADR 0003's precedent. The
   repository owner may prefer Proposed until the change merges.
10. **Scope is #46 and #47 only.** Everything under *What this does not decide*
    is left alone on purpose.

## Alternatives considered

- **Verify every hit.** Rejected in §4.
- **Stop caching on upload.** Removes the shared buffer without a copy, but
  loses read-after-write hits, removes the staging path that ADR 0003 §2
  documents and its tests use, and leaves `loadChunk`'s capacity problem.
- **Hand the flushed buffer to the cache once the file's flush has succeeded**,
  copying only when `cap > len`. This saves the copy for whole chunks. But it
  is safe only while nothing else holds a buffer after its flush, which is the
  kind of reasoning #46 broke, and any later change that keeps a flushed buffer
  reachable would bring #46 back without a sound. It also caches nothing from
  the uploads of a flush that fails.
- **Charge `cap` and keep sharing.** Leaves #46 open, and spends the cache on
  unused capacity.
- **Make `get` return a copy.** An allocation and a copy on every hit, when
  every consumer already copies or only reads.
- **Copy only in `putChunk`.** Fixes #46 and most of #47, but leaves ownership
  to each caller (Assumption 1).

ADR 0002 §8 makes the opposite choice for the RPC reply cache, whose `finish`
keeps the slice it is given, which "must not be modified afterwards". That works
because the reply's producer is finished with it. The chunk cache's caller is
not: a flush that fails keeps its buffers dirty, and the next write changes
them.

## Consequences

- A cached chunk's bytes cannot change. A file that shares a chunk with another
  reads the right bytes, and a write or truncate that starts from a cached chunk
  stores the right bytes.
- `-cache` bounds what the cache holds, up to per-entry overhead, so the two
  memory pools add up the way ADR 0003 Assumption 2 says an operator should
  budget for them.
- Each uploaded chunk and each cache miss costs one more allocation and copy
  (§6).
- The rule is written into `docs/DESIGN.md` §3, so the block cache of the
  redesign (§14, step 1) inherits it.

## What this does not decide

- **Verifying hits.** What would reopen it: evidence that cached bytes change by
  some route §1 does not close, or a policy for what a failed check should do.
- **`Read` and the dirty buffers** (#41, #49). `Read` copies the chunk list
  before it takes `openFile.mu` (#41), and it keeps slices of the dirty buffers
  after releasing that lock while `bufferWrite` writes into them in place
  (#49), which is a data race. That is the kind of sharing §1 removes from the
  cache, but it is in `Read`, and fixing it changes where `Read` takes its
  locks. It is left to those issues.
- **`truncate`'s deferred trims** (#40, #56).
- **One memory pool for dirty buffers and the cache.** Left open by ADR 0003.
- **The form of the key.** `docs/DESIGN.md` §3 keys the redesign's cache by the
  full object key. This code keys by the bare hash, having one chunk prefix.
- **Charging per-entry overhead.**

## Sources

Fetched on 2026-09-24 through a tool that summarises what it fetches; quoted as
it returned them when asked for verbatim text.

- <https://pkg.go.dev/builtin#make> — for a slice, "The size specifies the
  length. The capacity of the slice is equal to its length." (§1)
- <https://pkg.go.dev/builtin#append> — "If it has sufficient capacity, the
  destination is resliced to accommodate the new elements. If it does not, a
  new underlying array will be allocated." Nothing is said about the new
  array's capacity. (§1)
- <https://pkg.go.dev/builtin#copy> — "Copy returns the number of elements
  copied, which will be the minimum of len(src) and len(dst)." (§1)
- <https://pkg.go.dev/bytes#Clone> — "Clone returns a copy of b[:len(b)]. The
  result may have additional unused capacity." (§1)
- <https://pkg.go.dev/io#ReadAll> and <https://pkg.go.dev/os#ReadFile> — neither
  doc comment says anything about the capacity of the slice returned.
  (*Context*)

**Honesty note:** these are the doc comments of the `builtin`, `bytes`, `io` and
`os` packages, not the language specification. My fetches of the
specification's sections on `make` and `append` came back truncated before
reaching them.
