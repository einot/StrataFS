# strata: a WAFL-derived filesystem for object storage

**Status:** design, not yet implemented. The current code is the proof of
concept described in [../README.md](../README.md); this document describes what
replaces it and why.

**Source:** Dave Hitz, James Lau, Michael Malcolm, *File System Design for an
NFS File Server Appliance*, USENIX Winter 1994 (NetApp TR-3002). Section
numbers below refer to that paper.

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
is exactly the kind of thing implementations differ on. Dell documents ECS as
supporting `If-Match` and `If-None-Match`, but `strata -check` probes a live
endpoint for every behaviour the filesystem needs, and should be run against any
new cluster before trusting it.

If conditional writes turn out to be unavailable, the fallback is not to give up
atomicity — the root swap is still a single object PUT, so readers still see one
state or the other — but to give up *detection* of a second writer. In that case
the filesystem must be operated single-writer by policy, and the design should
say so rather than pretend the guarantee holds.

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
| Metadata block | 64 KiB | One round trip fetches 2048 references or 256 inodes |
| Reference | 32 B | Bare SHA-256; no address needed |
| Fanout | 2048 | `log₂₀₄₈(10⁹)` ≈ 3 levels for a billion blocks |
| Inode record | 256 B | Fits pointers, times, and small files inline |
| Data chunk | 1 MiB default | Amortizes request cost; content-defined chunking is a later option |

WAFL stores very small files in the inode itself in place of block pointers
(§3.1). We keep that: up to 160 bytes of file data, or a symlink target, live
inline, so a small file costs zero extra objects.

### Inode record (256 bytes)

```
offset size field
0      8    generation      bumped on reuse; makes a stale NFS handle detectable
8      2    type            reg / dir / lnk
10     2    mode            permission bits
12     4    uid
16     4    gid
20     8    size            logical length in bytes
28     8    blocks          blocks actually stored (sparse files count less)
36     24   atime/mtime/ctime, nanoseconds
60     4    depth           levels of indirection below this inode
64     32   root            hash of this file's top block
96     160  inline          file data or symlink target when it fits
```

`depth` makes the tree self-describing: a reader knows how many indirect levels
to walk without consulting anything else, and a file grows a level simply by
incrementing it and pushing the old root down.

### Directories

A directory is a file whose blocks are `dirent` blocks: entries sorted by name,
each `{name_len, inode, type, name}`. A lookup binary-searches the block index,
so a directory of a million entries costs two or three GETs rather than a scan.

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
then fetch the data blocks. At depth 2 with 2048 fanout that is at most three
fetches, and in practice the indirect blocks are hot.

Every fetched block is cacheable forever without invalidation, because its key
is its hash. That is worth stating plainly: **a content-addressed cache has no
coherence protocol**, because a given key's contents can never change. The PoC
already relies on this; the redesign extends it to all metadata.

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
- **Sweep.** List both buckets; delete objects that were not marked.

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
  block splitting, `IN_CP` deferral and sweeping are all real code with real
  edge cases. The PoC fits in a day; this does not.
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

1. **Block layer.** Typed blocks, hashing, the two-bucket router, cache.
   Test: round-trip every block type; verify data blocks never land in metadata.
2. **File trees.** Indirect walking, read/write/truncate at arbitrary offsets,
   sparse holes, growing and shrinking `depth`.
   Test: differential against `os.File` under random operation sequences.
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
   refusal on shared buckets.
   Test: churn, sweep, assert nothing reachable was deleted — the one test that
   must never be flaky.

Phases 1–4 give a filesystem strictly better than the PoC. 5 and 6 are the
features the PoC cannot have at all.

---

## 15. Open questions

- **Content-defined chunking?** Fixed chunks mean inserting a byte at the front
  of a file rewrites everything. Rolling-hash boundaries fix it and improve
  dedup, at some CPU cost and with variable block sizes complicating the offset
  arithmetic. Probably worth it; not on the critical path.
- **Should the inode file be sparse?** Freed inodes leave holes. WAFL keeps an
  inode-map file (§3.2). A free list in `fsinfo` is simpler and probably enough.
- **Multi-writer.** Single-writer-with-detection is correct and limiting. Doing
  better means per-subtree leases or a real consensus layer, and is a much
  larger design.
- **Where does the snapshot schedule live?** Policy in the daemon, but the names
  and rotation have to survive restarts, so something must persist in `fsinfo`.
