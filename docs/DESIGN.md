# strata: a WAFL-derived filesystem for object storage

**Status:** design, not yet implemented. The current code is the proof of
concept described in [../README.md](../README.md); this document describes what
replaces it and why.

**Source:** Dave Hitz, James Lau, Michael Malcolm, *File System Design for an
NFS File Server Appliance*, USENIX Winter 1994 (NetApp TR-3002). Section
numbers below refer to that paper.

**Companion:** [nfsv4-assessment.md](nfsv4-assessment.md) assesses what serving
NFSv4.2 instead of NFSv3 would take, and recommends against doing it before this
design lands. It is an assessment, not a commitment, and this document stays
normative — but two of its conclusions are already binding here: the `change`
field in section 4's inode record, and the optional interfaces in
`internal/vfs`.

---

## 1. What WAFL actually does

Five mechanisms matter here, all from the paper:

**A tree of blocks rooted at one block (§3.3).** The root inode describes the
inode file; the inode file contains every other inode, including those of the
metadata files; data blocks are the leaves. The sole exception to writing
anywhere is that the block holding the root inode must sit at a known fixed
location, or the system cannot find the filesystem at boot.

**Metadata lives in files (§3.2).** The inode file, the block-map file and the
inode-map file are ordinary files in the tree. This is what makes "write
anywhere" possible — if metadata were pinned to fixed offsets, copy-on-write
could not work — and it is where the name comes from.

**Snapshots are a duplicated root inode (§3.4).** Copying that one inode yields
a complete second tree that shares every block with the active filesystem, so a
fresh snapshot costs nothing but the inode. Modifying a block writes it to a new
location and updates its parent, which updates *its* parent, up to the root. The
paper contrasts this with Episode, which copied the whole inode file and all
indirect blocks — 320 MB of I/O for a 10 GB filesystem.

**Consistency points (§3.5).** Every few seconds, and at most every 10 seconds,
WAFL writes an unnamed internal snapshot. Between consistency points it only
ever writes to blocks that are *not in use*, so the tree of the last consistency
point stays intact on disk. The on-disk image therefore advances atomically from
one self-consistent state to the next. Blocks may be written in any order, with
one constraint: every block of the new consistency point reaches disk before the
root inode does. The paper names this as the shadow-paging technique from
System R.

**The block-map replaces the free bitmap (§4.1).** One bit per block cannot work
when many snapshots may reference the same block, so each 4 KB block gets a
32-bit entry: bit 0 set when the active filesystem references it, bit 1 for the
first snapshot, bit 2 for the second, and so on. A block is in use if any bit is
set, and becomes reusable only when all of them are clear. Creating a snapshot
copies the active-filesystem bit into the new snapshot's bit for every entry;
deleting one zeroes the snapshot's root inode and clears its bit everywhere.

The payoff is §3.5's: after an unclean shutdown there is nothing to check. WAFL
reverts to the last consistency point, which is by construction consistent, and
`fsck` never runs.

---

## 2. Why this maps unusually well onto object storage

WAFL was designed to escape the constraints of spinning disks: fixed metadata
locations, seek costs, RAID's read-modify-write penalty on partial stripes. Most
of that machinery turns out to be *free* in an object store, which makes WAFL a
better fit here than it was on the hardware it was written for.

| WAFL mechanism | Why it existed on disk | In an object store |
|---|---|---|
| Write anywhere | Escape fixed metadata offsets and seek penalties | Every PUT is already "anywhere" — you choose the key |
| Copy-on-write to a new location | Must not overwrite a block a snapshot references | Content addressing *forces* it: changed bytes hash differently, so the key changes on its own |
| Block-map with a bit per snapshot | Decide when a disk address may be reused | Addresses are never reused; the question becomes when an object may be deleted |
| Root inode at a fixed location | Something must be findable at boot | One mutable object at a known key |
| Consistency point | Advance the on-disk image atomically | Compare-and-swap that one object |
| NVRAM request log | Acknowledge NFS writes before they reach disk | NFSv3's own UNSTABLE/COMMIT contract (§9) |
| RAID | Survive a disk failure | The provider's durability |

### A note on where this runs

The target is an on-premise Dell ECS cluster (ObjectScale is its renamed next
release). "S3-compatible" is a spectrum, and the behaviour this design leans on
hardest — conditional writes on PUT, which make the consistency point atomic —
is exactly the kind of thing implementations differ on.

**This has now been confirmed on the target cluster.** `strata -check` was run
against a live ECS endpoint and passed all ten probes, including the one that
matters: a PUT conditioned on a superseded ETag is *rejected*. The
compare-and-swap this design commits through is therefore sound there, and a
second writer is detected rather than silently overwriting the first. The
SigV4 signer was accepted too, which makes ECS the third independent
implementation it has been validated against, after AWS's published test vector
and MinIO.

That result is specific to one cluster, so `-check` remains the thing to run
against any new endpoint rather than assuming this generalises.

Where conditional writes are unavailable, the fallback is not to give up
atomicity — the root swap is still a single object PUT, so readers still see one
state or the other — but to give up *detection* of a second writer. Such a
deployment must be operated single-writer by policy, and should say so rather
than pretend the guarantee holds.

The one thing object storage takes away is cheap small random writes. A 4 KB
block is the wrong unit when each access is an HTTPS round trip, so block sizes
grow by two orders of magnitude and the tree gets shallower and wider. That is a
parameter change, not a structural one.

The one thing it adds is that content addressing makes block identity *global*.
Two snapshots that share a subtree do not merely point at the same address —
they name it with the same hash. That property does real work in §8.

---

## 3. Blocks, and the single bucket

A **block** is an immutable object named by the SHA-256 of its contents. There
are five kinds, distinguished only by how their referrer interprets them:

| Kind | Contents | Key prefix |
|---|---|---|
| `data` | raw file bytes | `b/` |
| `indirect` | array of block references | `m/` |
| `inode-file` | a run of packed inode records | `m/` |
| `dirent` | a run of directory entries | `m/` |
| `fsinfo` | the root; the one mutable object | `fsinfo` |

Everything lives in **one bucket**. An earlier iteration split data and metadata
across two, so the chunk half could be shared read-only without revealing
filenames. That is not worth the cost: the isolation it bought is achievable
with key prefixes and an access policy, while two buckets introduce a
cross-bucket invariant that no backup, restore or replication operation can
preserve atomically. One bucket is also what WAFL describes — a single volume,
not two storage pools.

The `b/` and `m/` prefixes are retained because they cost nothing and keep the
option open: a policy granting read on `b/*` alone still exposes only
content-addressed bytes, with no filenames or directory structure. Nothing in
the format depends on that, and no code distinguishes the two beyond choosing a
prefix.

Worth noting for its own sake: because block names are unguessable hashes,
granting `s3:GetObject` *without* `s3:ListBucket` turns a hash into a
capability. A holder can fetch exactly the subtree whose root they were given
and enumerate nothing else. That is a finer-grained sharing primitive than any
bucket split, and it needs no structural support.

### Keys, integrity and the cache

A block's key is its prefix followed by the SHA-256 of its body as 64 lowercase
hex characters: `b/9f86d081…` or `m/9f86d081…`. The kind selects the prefix and
nothing else — bodies are opaque at this level, and what a body *means* is
decided by the referrer that led you to it.

Four rules govern every block, and they are the whole of the block layer's
contract:

- **Storing is idempotent.** A block whose key already exists is not uploaded
  again. This is deduplication and crash-recovery reuse in one step (section 5,
  step 3), and it is safe precisely because an existing object under that key
  necessarily has those contents.
- **Fetching verifies.** The body is hashed and compared against the key it was
  asked for. A mismatch is corruption, not staleness: the object can never
  become correct, so it is never cached and never retried as though the failure
  were transient. This applies to data and metadata alike.
- **A block is a pure function of what it represents.** Reserved fields are
  zero; no timestamps, no writer identity, no uninitialised padding. A single
  incidental byte would give two identical subtrees two different hashes and
  stop them sharing storage, which is the property section 8's economics rest
  on.
- **The block layer never deletes.** Objects are removed only by the sweeper of
  section 8, which works from a listing rather than through this path.

The cache is keyed by the full object key, so it preserves the prefix split
rather than dissolving it: a block stored under `b/` cannot be served to a
reader that asked for it under `m/`. Cached entries are never invalidated —
section 7 explains why they cannot go stale — and callers must treat what they
get back as read-only, because it is shared. The cache stores its own copy of
each block, sized to the block's length, and never a buffer a caller still
holds, so no later change by a caller can reach a cached block, and its size
limit counts the memory it actually holds
([ADR 0004](adr/0004-chunk-cache-owns-its-bytes.md)).

`fsinfo` is not a block. It is mutable and lives at a fixed key, so none of
this applies to it.

## 4. The tree

```
fsinfo  (mutable, key "fsinfo", compare-and-swapped)
  ├── active root inode ────────┐
  ├── snapshot[0..n] root inodes│   each 256 bytes, inline
  └── superblock fields         │
                                ▼
                         inode file  (a regular file)
                                │
                      ┌─────────┴─────────┐
                  indirect             indirect              (m/ prefix)
                      │                   │
                inode-file blocks   inode-file blocks  (256 inodes each)
                      │
              ┌───────┴───────┬──────────────┐
           inode 1         inode 2        inode 3
          (a dir)         (a file)      (a symlink)
              │               │              │
          dirent blocks   indirect        target inline
           (m/ prefix)        │
                         data blocks            (b/ prefix)
```

The root inode describes the inode file, exactly as in §3.3. Every inode —
including the inode file's own, and the directories' — is reached by indexing
into the inode file. Inode number *n* lives at byte offset `n × 256`.

`fsinfo` is the fixed location. The paper needed a fixed disk address; we need a
fixed key. It is the only object in the bucket that is ever modified.

### Block sizing

| Parameter | Value | Reasoning |
|---|---|---|
| Metadata block | 64 KiB | One round trip fetches 1638 references or 256 inodes |
| Reference | 40 B | 32-byte SHA-256 plus the 8-byte span it covers |
| Fanout | 1638 | `16 + 1638 × 40 = 65536`; ≈ 3 levels for a billion chunks |
| Inode record | 256 B | Fits pointers, times, and small files inline |
| Data chunk | fixed 1 MiB today | Amortizes request cost; where the boundaries fall is policy, not format |

A data block and a chunk are the same object seen from two directions — the
storage layer's kind and the file's leaf unit — and the words are used
interchangeably below.

How a file's bytes are divided into chunks is a **policy**. The chunker maps a
byte stream to a sequence of chunks; the tree stores whatever it produces and
cannot tell one chunker from another. Today it cuts every 1 MiB. Whether it
should cut on content instead is issue #18, which [ADR 0001](adr/0001-file-trees-carry-chunk-spans.md)
narrows to a question the tree no longer constrains. Any chunker must declare a
minimum, an average and a maximum size, and the maximum is the one that matters:
verifying a block's hash means fetching all of it, so the largest chunk is the
read amplification of the smallest random read.

WAFL stores very small files in the inode itself in place of block pointers
(§3.1). We keep that: up to 152 bytes of file data, or a symlink target, live
inline, so a small file costs zero extra objects.

### Indirect blocks

A reference is not bare: it carries the number of file bytes its child's subtree
covers. Position therefore does not imply offset, which is what makes the tree
indifferent to the chunker. ADR 0001 records why, what it costs, and what it
deliberately leaves open.

An indirect block is a 16-byte header followed by up to 1638 packed 40-byte
entries. All integers are big-endian.

```
header  offset size field
        0      4    magic     ASCII "SIND"
        4      1    version   1
        5      1    level     0 = children are chunks; n = children are indirect
                              blocks of level n−1
        6      2    count     entries present, 1..1638
        8      8    reserved  zero

entry   0      32   hash      SHA-256 of the child, or 32 zero bytes: a hole
        32     8    span      file bytes covered by this child's subtree
```

The body is `16 + 40 × count` bytes. Blocks are not padded, so the last block of
each level is short, and `count` must agree with the body's length.

Six rules make the shape canonical, so that one leaf sequence always yields one
root hash and therefore always deduplicates:

1. **Levels are packed left to right.** Level 0 holds the leaf entries, 1638 per
   block; each level above holds one entry per block of the level below, in
   order. Only the last block of a level may be short.
2. **`depth` is the least that fits.** For *n* leaf entries, `depth` is the
   smallest *d* ≥ 1 with *n* ≤ 1638^*d*, and `root` names the single block of
   level *d*−1 — unless rule 3 applies.
3. **`depth` 0 is the small-file case.** Either the data is inline (`size` ≤
   152) and `root` is the zero hash, or the file is exactly one stored chunk and
   `root` is that chunk's hash. A file over 152 bytes that is entirely a hole is
   depth 1 with a single hole entry.
4. **Spans sum to `size`.** At every level the entries' spans sum to their
   parent's span, and at the root to the inode's `size`. Extending a file by
   truncation appends a hole entry rather than leaving the tree short.
5. **Holes live only at level 0.** A hole's span is arbitrary, so a hole of any
   length is one entry and upper-level holes would buy nothing.
6. **Adjacent holes merge.** Two hole entries never sit side by side.

Locating the byte at offset *x*: start at `root` with *x*; at each level, sum
entry spans until the running total exceeds *x*, then descend into that entry
with *x* reduced by the total before it. A zero hash on the way down means the
region was never written and reads as zeros. The walk costs `depth` metadata
fetches and one data fetch, and the metadata blocks are hot.

An edit that leaves the number of leaf entries unchanged rewrites only the
blocks holding the changed entries and their ancestors — the root-to-leaf path
section 11 sells. An edit that changes how many entries cover a region repacks
that level from the first changed entry onward, at 40 bytes of indirect block
per megabyte of file beyond the edit.

One structure carries every file, the inode file and the directories included,
because in this design those *are* files (§3.2).

### Inode record (256 bytes)

```
offset size field
0      8    generation      bumped on reuse; makes a stale NFS handle detectable
8      2    type            reg / dir / lnk
10     2    mode            permission bits
12     4    uid
16     4    gid
20     8    size            logical length in bytes
28     8    used            bytes actually stored; holes count nothing
36     24   atime/mtime/ctime, nanoseconds
60     4    depth           levels of indirection below this inode
64     32   root            hash of this file's top block
96     8    change          version stamp; changes whenever this inode does
104    152  inline          file data or symlink target when it fits
```

`used` is in bytes rather than blocks because there is no fixed block to count
once chunk sizes vary, and because it feeds NFSv3's `fattr3.used`, which RFC
1813 defines in bytes. It is the sum of the spans of the non-hole leaves.

`depth` makes the tree self-describing: a reader knows how many indirect levels
to walk without consulting anything else, and a file grows a level by writing
one new top block whose single entry is the old root. It is also *authoritative*
rather than a cross-check — with variable-length leaves the number of leaves
depends on the data, so `depth` cannot be recomputed from `size`.

`change` is here for a protocol this server does not yet speak, and is the one
field in the record that is far cheaper to allocate now than to add later.
**Nothing reads it today** — NFSv3 has no use for it — so it needs a reason on
the record rather than in someone's memory, or it will be reclaimed as dead
space by the next person to want eight bytes.

NFSv4 requires a per-object *change attribute*: a value that differs whenever
the object's data or metadata changes, which clients use for cache validation in
place of comparing timestamps. A timestamp cannot stand in for it here, because
`ctime` comes from the wall clock and two modifications inside one clock tick
would be indistinguishable. The value is allocated from a single monotonically
increasing counter in `fsinfo`, taken on every mutation of any inode — one
counter rather than one per inode, so a directory gets a change stamp for free,
and a counter rather than a hash of the record, so that a server can advertise
`NFS4_CHANGE_TYPE_IS_MONOTONIC_INCR` and a client may assume the value only ever
rises. Mutations made after the last consistency point reuse their numbers after
a crash, which is exactly the window in which the data they describe is also
lost, so the two stay consistent.

**It is written from the first implementation, not reserved as zeros.** A
reserved field is deleted as unused and is wrong the first time it is read;
writing it costs one counter increment on a path that is already rewriting the
record.

The eight bytes come out of the inline area, which drops from 160 to 152. The
record cannot grow instead: 256 bytes is what puts inode *n* at offset
*n* × 256 and fits 256 inodes in one metadata block. The inline threshold was
always this project's number rather than a derived one — the WAFL paper sets
none — so moving it costs nothing but the handful of files between 153 and 160
bytes, which gain one object each.

[nfsv4-assessment.md](nfsv4-assessment.md) records why this field is here now
rather than when an NFSv4 server is actually written, and what else such a
server would need.

### Directories

A directory is a file whose blocks are `dirent` blocks: entries sorted by name,
each `{name_len, inode, type, name}`. A lookup binary-searches the block index,
so a directory of a million entries costs two or three GETs rather than a scan.

Directory entries are variable-length, so a `dirent` block is as long as the
entries packed into it and no longer. That works only because a reference
carries its span: a layout where position implied offset would force every
`dirent` block but the last to be padded to 64 KiB, and a directory's `size`
would stop meaning anything. This is the reason the span layout would be right
even if issue #18 were closed tomorrow with fixed chunking kept forever.

This replaces the PoC's inline `map[string]uint64`, which forced the entire
directory to be rewritten — and re-serialized into the global snapshot — on
every create.

---

## 5. The write path and consistency points

Writes accumulate in memory as dirty blocks. Nothing is uploaded until a
consistency point, which runs every few seconds, on NFS COMMIT, or when dirty
state exceeds a threshold. WAFL's §3.4 point applies unchanged: batching means
hot blocks like the inode file and upper indirect levels are written once per
consistency point rather than once per request.

A consistency point is:

1. **Seal.** Mark every dirty block `IN_CP`. New writes that would modify an
   `IN_CP` block are deferred; writes to anything else proceed. This is WAFL's
   `IN_SNAPSHOT` rule from §4.2, and it exists so the server never stops
   answering while a consistency point is in flight.
2. **Hash bottom-up.** Compute hashes leaves first, so each parent is hashed
   only once its children's names are final. This pass is pure computation and
   touches no network.
3. **Upload.** PUT every new block. Order is irrelevant — they are immutable and
   nothing references them yet — so this is one wide parallel fan-out. Blocks
   whose hash already exists are skipped, which is dedup and crash-recovery
   reuse in the same step.
4. **Swap.** Once *every* block is durable, compare-and-swap `fsinfo` with
   `If-Match` on the ETag read at mount.

Step 4 after step 3 is WAFL's one ordering constraint from §3.6: all blocks of
the consistency point reach storage before the root does. Violating it is the
only way to produce a root that names something absent.

A failed CAS means another mount advanced the filesystem. The PoC's answer —
refuse further writes and demand a remount — stays, because merging two
divergent trees is a different problem and pretending otherwise loses data
silently.

---

## 6. Snapshots

Creating one copies the active root inode into the `fsinfo` snapshot table under
a name. That is 256 bytes and no I/O beyond the consistency point that carries
it, precisely as §3.4 describes. Deleting one removes its entry.

Because the paper's snapshots are user-visible through NFS (§2.1), and because
ours cost the same, the filesystem exposes them the same way:

```
/mnt/.snapshot/hourly.0/project/notes.txt
/mnt/.snapshot/nightly.2/project/notes.txt
```

`.snapshot` is synthesized at lookup: it does not exist in any directory block,
it appears in no READDIR listing, and each child resolves to a root inode from
the `fsinfo` table, mounted read-only. Recovering a file becomes `cp`, with no
administrator involved — which was the paper's motivation for exposing them at
all.

Scheduled snapshots (hourly, nightly, with rotation) are policy over this
primitive and belong in the daemon, not the format.

---

## 7. Reading

`GETATTR(ino)`: read `inode_file[ino × 256]` — one block fetch, usually cached.

`READ(ino, offset, n)`: walk `depth` indirect levels from the inode's root hash,
accumulating spans to choose a child at each level, then fetch the chunks. At
depth 2 that is at most three fetches, and in practice the indirect blocks are
hot. Depth 3 at a fanout of 1638 covers about 4 PiB of 1 MiB chunks, so nothing
stored here needs a fourth level.

Every fetched block is cacheable forever without invalidation, because its key
is its hash. That is worth stating plainly: **a content-addressed cache has no
coherence protocol**, because a given key's contents can never change. The PoC
already relies on this; the redesign extends it to all metadata.

A read sees the file at one instant. Holding what orders it against writes to
the file and against a consistency point's swap, it decides which blocks of its
range are dirty in memory and which references name the rest, and copies out
the dirty bytes it needs; it fetches the referenced blocks only after letting
go. Fetching late loses nothing, because a block named by its hash cannot
change. So a read never returns less than a write acknowledged before it
began, nor a mixture of bytes from before and after one write
([ADR 0005](adr/0005-read-takes-one-view-under-the-file-lock.md)).

---

## 8. Free space: why the block-map becomes mark-and-sweep

This is the one place where a faithful translation of WAFL is the *wrong*
answer, and it is worth being explicit about why.

The block-map (§4.1) answers "may this disk address be reused?", and the
bit-per-snapshot layout works because a disk block has *positional* identity —
it is one slot, referenced from one place per tree. Content-addressed blocks do
not work that way. One block may be reached through thousands of paths, in any
number of snapshots, in trees belonging to other people who share the bucket.
There is no single "the" reference to record a bit for, and the hash space is
far too sparse to index by number.

The equivalent question here is "may this object be deleted?", answered by
**mark-and-sweep over reachable hashes**:

- **Mark.** From every root in `fsinfo` — active plus every snapshot — walk the
  tree and record reachable hashes.
- **Sweep.** List the bucket; delete objects that were not marked.

Two properties make this cheap, and they come directly from content addressing:

- **Shared subtrees are visited once.** If the mark phase has already seen hash
  `H`, it never descends into it again — and two snapshots that share a subtree
  necessarily name it with the *same* hash. Marking *n* snapshots therefore
  costs the number of unique blocks, not *n* × blocks. WAFL got the analogous
  saving from its bitmap; we get it from the Merkle structure.
- **The mark set is exact**, not conservative. There is no ambiguity about
  whether a reference is live.

Safety rules, both non-negotiable:

1. **Never sweep an object created after the mark phase began.** A concurrent
   writer uploads blocks before the `fsinfo` that references them, so a
   newly-uploaded block is legitimately unreferenced for a moment. Sweeping only
   objects older than the mark start closes that window.
2. **Never sweep a bucket whose roots you cannot all enumerate.** Sweeping is
   only sound when `fsinfo` names every live root. If subtree hashes have been
   handed out as capabilities, or another party mounts the same bucket, those
   references are invisible to the mark phase and the sweeper will delete data
   that is still in use. Either keep such a bucket append-only, or require every
   consumer to register a root that the mark phase can see.

---

## 9. NVRAM, and why we do not need it

WAFL logs NFS requests to battery-backed RAM so it can acknowledge a write
before it reaches disk, replaying the log after an unclean shutdown (§3.5). The
paper is careful about the distinction: it logs *requests*, not dirty blocks,
which is why a rename costs ~150 bytes instead of 32 KB, and why an NVRAM
failure can lose requests but cannot corrupt the on-disk image.

We have no NVRAM, and the cloud equivalent — a durable low-latency log — is
exactly the thing object storage is worst at. But NFSv3 already solves this at
the protocol level, and the PoC already implements the pieces:

- A client's `UNSTABLE` write may be acknowledged from memory. The client is
  required to retain the data until it issues `COMMIT` and gets a success.
- `COMMIT` forces a consistency point and returns only when `fsinfo` has moved.
- Every reply carries a **write verifier**. If the server restarts, the verifier
  changes, and the client detects this and *re-sends* everything it had not
  committed.

The client's memory is the NVRAM. This is not a workaround — it is what the
UNSTABLE/COMMIT mechanism was designed for, and it is the reason a
consistency-point filesystem and NFSv3 fit together as neatly as they do. The
one obligation is absolute: **the write verifier must be freshly random on every
process start**, or a client will assume unacknowledged writes survived when
they did not.

For workloads that will not tolerate losing uncommitted writes on a crash, an
optional local journal on SSD is a well-understood addition, but it buys
durability against process death only — not host loss — and should not be on by
default.

---

## 10. Crash recovery

There is none, which is the point of §3.5.

`fsinfo` names a tree whose every block was durable before `fsinfo` moved.
Whatever was in flight at the moment of the crash is a set of unreferenced
objects, which the sweeper collects later. On restart the filesystem reads
`fsinfo` and is immediately consistent. No log replay, no scan, no `fsck`, and
mount time is independent of filesystem size.

Partially-uploaded blocks cannot be mistaken for good ones: a block's key is the
hash of its contents, so a truncated body is simply an object nothing will ever
ask for. Object stores also make a PUT atomic, so no reader sees a half-written
block.

---

## 11. What this fixes relative to the proof of concept

| | Proof of concept | This design |
|---|---|---|
| Namespace storage | One gzipped JSON document | Merkle tree of 64 KiB blocks |
| Cost of one create | Rewrite **entire** namespace | Rewrite root→leaf path, ~3 blocks |
| Commit cost | O(filesystem) | O(changed paths) |
| Directory of 1M files | Whole map re-serialized per change | Two or three blocks touched |
| Metadata cache | Whole-namespace, invalidated per commit | Per-block, never invalidated |
| Snapshots | None | Free; user-visible under `.snapshot/` |
| Garbage collection | None — chunks leak on delete | Mark-and-sweep, exact |
| Bucket growth | 440 snapshots from one smoke test | Bounded by live set plus sweep lag |
| Mount time | Download and parse whole namespace | One GET of `fsinfo` |
| Memory | Entire namespace resident | Working set only |

The first row causes all the others. Writing the namespace as one document is
what makes commits O(filesystem), forces whole-namespace caching, and makes
snapshots unaffordable. WAFL's §3.2 — metadata lives in files — is the single
idea that dissolves the whole cluster.

---

## 12. What gets worse

Worth stating plainly, because the PoC is better at some things:

- **More round trips for a cold read.** The PoC has the whole namespace in
  memory after mount; a path lookup is free. Here a cold lookup walks indirect
  blocks. Mitigated by caching, which is unusually effective given there is no
  invalidation, but it is a real regression for small filesystems.
- **Small filesystems pay tree overhead.** A hundred files do not need three
  levels of indirection. The `depth` field means shallow trees stay shallow, but
  there is a floor.
- **Implementation complexity is several times larger.** Indirect-block walking,
  repacking a level when the number of leaves changes, `IN_CP` deferral and
  sweeping are all real code with real edge cases. The PoC fits in a day; this
  does not.
- **Sweeping is a background job that can go wrong.** A bug deletes live data.
  It must be conservative, restartable, and dry-runnable before it ever deletes.

---

## 13. Security note on content addressing

Because a block's key is the hash of its contents, anyone who can list the data
bucket can test whether you store a *particular* file: hash the candidate, look
for the key. Against a shared or public bucket this is a confirmation-of-file
attack, and it is inherent to convergent addressing, not a bug in this design.

If that matters, encrypt data blocks with a key held in the metadata tree.
Dedup then stops working across trust boundaries — which is the honest trade,
and exactly the trade convergent encryption tries to blur.

---

## 14. Implementation order

Each phase is independently testable, and the existing NFS, RPC and store layers
carry over unchanged — only `blobfs` is replaced.

1. **Block layer.** Typed blocks, hashing, prefix routing, cache — section 3's
   four rules, over opaque bodies. The indirect-block format belongs to phase 2,
   which is what keeps this phase independent of issue #18.
   Test: round-trip every block type; verify data blocks never land in metadata;
   verify a body that does not hash to its key is rejected rather than served.
2. **File trees.** Indirect walking over the span-carrying format of section 4,
   read/write/truncate at arbitrary offsets, sparse holes, growing and shrinking
   `depth`.
   Test: differential against `os.File` under random operation sequences, run
   twice — once with the fixed chunker and once with a deterministic chunker
   that emits adversarial sizes, since the tree must not be able to tell them
   apart.
3. **Inode file and directories.** Inode file as a regular file, `dirent`
   blocks, binary-searched lookup.
   Test: a million-entry directory; verify block touch counts, not just results.
4. **Consistency points.** `IN_CP` marking, bottom-up hashing, ordered upload,
   `fsinfo` CAS.
   Test: kill the process at each step; assert the mount is always consistent
   and never references a missing block.
5. **Snapshots.** Root-inode table, `.snapshot` synthesis in LOOKUP.
   Test: snapshot, modify, confirm the snapshot is unchanged and shares blocks.
6. **Sweeper.** Mark from all roots, sweep with the age rule, dry-run mode,
   refusal when live roots cannot all be enumerated.
   Test: churn, sweep, assert nothing reachable was deleted — the one test that
   must never be flaky.

Phases 1–4 give a filesystem strictly better than the PoC. 5 and 6 are the
features the PoC cannot have at all.

---

## 15. Open questions

- **Which chunker?** *Narrowed.* The layout question is settled: references
  carry spans, so the tree is indifferent to where boundaries fall
  ([ADR 0001](adr/0001-file-trees-carry-chunk-spans.md)), and phase 2 of section
  14 is no longer blocked. What remains open is whether to cut on content rather
  than by byte count — issue #18 — which is an optimization and is held to #18's
  own bar: measured dedup ratios over a documented corpus, at parameters derived
  for object storage rather than borrowed from backup tools. Adopting it later
  replaces the chunker and the write path's re-chunking rule, and nothing else.
- **Should the inode file be sparse?** Freed inodes leave holes. WAFL keeps an
  inode-map file (§3.2). A free list in `fsinfo` is simpler and probably enough.
- **Multi-writer.** Single-writer-with-detection is correct and limiting. Doing
  better means per-subtree leases or a real consensus layer, and is a much
  larger design.
- **Where does the snapshot schedule live?** Policy in the daemon, but the names
  and rotation have to survive restarts, so something must persist in `fsinfo`.

---

## 16. Decision records

Decisions that constrain work not yet done live in `docs/adr/`, one file per
decision, each with its alternatives and the assumptions behind it. The
convention and the index are in [docs/adr/README.md](adr/README.md). This
document is normative: where it and an ADR disagree, one of them has a bug and
it is not this one.

| ADR | Decision | Sections it constrains |
|---|---|---|
| [0001](adr/0001-file-trees-carry-chunk-spans.md) | File trees carry chunk spans; the chunker is a policy | 3, 4, 7, 14 |
| [0002](adr/0002-duplicate-request-cache.md) | A duplicate request cache, keyed per connection instance, replays the reply to a retransmitted non-idempotent call | none — it constrains `internal/sunrpc` and `internal/nfs`, which section 14 carries over unchanged |
| [0003](adr/0003-write-backpressure.md) | Buffered writes are bounded by a byte budget; a writer that has to wait performs the drain itself | 5, 9 |
| [0004](adr/0004-chunk-cache-owns-its-bytes.md) | The chunk cache stores its own exact-length copy of each chunk, charges what it holds, and does not re-verify hits | 3 |
| [0005](adr/0005-read-takes-one-view-under-the-file-lock.md) | A read takes its view of a file — size, chunk references and the dirty bytes it needs — at one instant under the file's lock, and fetches chunks holding no lock | 7 |
