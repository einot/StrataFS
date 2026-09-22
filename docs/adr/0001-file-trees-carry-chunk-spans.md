# 0001. File trees carry chunk spans

**Status:** Accepted
**Date:** 2026-09-22
**Issue:** #18, blocking phase 2 of #15

## Context

`docs/DESIGN.md` section 4 specifies the file tree as an array of bare 32-byte
SHA-256 references, 2048 per 64 KiB indirect block, with chunk *i* covering
`[i × chunkSize, (i+1) × chunkSize)`. Position implies offset, so locating the
chunk that covers a file offset is a division and a digit decomposition in base
2048. The proof of concept does the same thing with a flat list: `internal/blobfs/fs.go`
computes `idx = off / chunkSize` in `Read`, `Write` and `truncate`, and
`internal/blobfs/inode.go` documents the invariant on the `Chunks` field.

Issue #18 argues for content-defined chunking (CDC): choose boundaries from the
content with a rolling hash, so that inserting a byte perturbs one chunk instead
of shifting every boundary in the file. It also states the real cost — variable
chunk sizes break offset arithmetic everywhere it is used — and observes that
the indirect-block layout must be settled before the file-tree layer is written,
or that layer gets written twice.

Nothing has been built against the section 4 layout yet. The redesign has not
shipped, so no bucket anywhere holds a tree in this format and there is no
migration to pay for. This is the cheapest moment the decision will ever have.

Two questions have been travelling together and are separable:

1. **Layout.** Does the tree assume every leaf covers the same number of bytes,
   or does it carry each child's byte span?
2. **Algorithm.** Are boundaries chosen by byte count or by content, and if by
   content, with which rolling hash and which size parameters?

Question 2 cannot honestly be answered now: #18's own "done when" list requires
measured dedup ratios over a documented corpus, and no such measurement exists.
Question 1 must be answered now, because phase 2 is what is blocked.

## Decision

**The file tree carries a byte span per reference. The chunker becomes a
policy, and stays fixed-size for the moment.**

An indirect block is a 16-byte header followed by up to 1638 packed 40-byte
entries; an entry is a 32-byte SHA-256 plus an 8-byte big-endian span, the
number of file bytes the child's subtree covers. Levels are packed left to
right, `depth` in the inode record is authoritative, an all-zero hash at the
leaf level is a hole of its span's length, and the spans at the root sum to the
inode's `size`. The normative format is `docs/DESIGN.md` section 4.

Consequently:

- Locating the chunk covering an offset is a descent that accumulates spans at
  each level — the first child whose running total exceeds the offset — instead
  of a division. Both are free next to the HTTPS round trip underneath them.
- The chunker is an interface: bytes in, chunk boundaries out. Today it emits
  fixed 1 MiB chunks, which is exactly what the tree sees as "every span is the
  same". The tree layer cannot tell the difference and must never assume it.
- Adopting FastCDC later is a swap of that one policy plus the write path's
  re-chunking rule. It is not a change to the block format, the tree walk, the
  inode record, the commit protocol or the sweeper.

The decision is deliberately *not* "adopt CDC". It is "stop the tree from
foreclosing the question", which turns out to be worth doing on its own merits.

### Why, in one paragraph

The strongest argument for spans is not CDC at all — it is that section 4
already specifies directories as files whose leaves are `dirent` blocks holding
variable-length `{name_len, inode, type, name}` entries. Under a fixed-size
layout every leaf but the last must be exactly one block long, so dirent blocks
have to be padded to 64 KiB and a directory's `size` stops meaning anything.
Phase 3 is already committed to that design. The span layout is therefore
required by work that is already planned, regardless of how #18's measurement
turns out; getting insert-resilient dedup within reach is a second payment on
the same purchase.

### What it costs, stated plainly

| | Bare reference | With span |
|---|---|---|
| Entry size | 32 B | 40 B |
| Fanout (64 KiB block) | 2048 | 1638 |
| Metadata per 1 MiB chunk | 0.0031% of data | 0.0038% of data |
| Levels for 10⁹ chunks | 3 | 3 |
| Largest file at depth 3 (1 MiB chunks) | 8.0 PiB | 4.09 PiB |
| Offset → leaf | division | accumulate spans, per level |

The fanout loss costs no level at any size this filesystem will see: 1638³ is
4,394,826,072 chunks, and a file needing four levels under the span layout needs
four under the bare one too. The extra 8 bytes per chunk is 40 bytes of metadata
per megabyte of data — a 1 TiB file carries about 41 MiB of level-0 indirect
blocks instead of 33 MiB.

The genuine new cost is that the number of leaves is data-dependent, so:

- `depth` can no longer be recomputed from `size`; the stored field is
  authoritative. It already exists in the 256-byte record, so nothing grows.
- A block needs an entry count, so blocks are no longer bare arrays.
- An edit that changes how many leaves cover a region must repack that level
  from the first changed entry onward. That is bounded by 40 bytes of indirect
  per megabyte of file *after* the edit — about 41 MiB of rewritten metadata for
  an insertion at the front of a 1 TiB file, against 1 TiB of rewritten data
  under fixed chunking. It is a rounding error against what it replaces, which
  is why this design needs no content-defined boundaries at the interior levels
  (no prolly tree, no Merkle search tree). Storing spans rather than absolute
  offsets is what keeps it that cheap: with absolute offsets a one-byte insert
  would change every following entry at every level.

## Assumptions

Everything here that no issue, spec section or prior decision dictated.

- **16-byte header, 1638 entries.** Nothing required 16. It is the smallest
  header that carries magic, version, level and count with room to spare *and*
  divides evenly: 16 + 1638 × 40 = 65536 exactly. A different header size would
  waste bytes at the end of every full block.
- **8-byte span.** A 4-byte span would cap a subtree at 4 GiB, which a depth-2
  subtree exceeds at 1638 × 1638 × 1 MiB. No requirement named a width.
- **Big-endian.** Chosen to match the repo's existing on-the-wire and on-disk
  conventions (`encodeHandle` in `internal/blobfs/fs.go`, and XDR throughout).
  Nothing made it necessary.
- **Blocks are not padded.** A short block is `16 + 40 × count` bytes. Padding
  would make every block uniform at the cost of uploading zeros; the entry count
  makes the body self-describing without it.
- **The all-zero hash is reserved for holes.** This assumes no all-zero SHA-256
  preimage will ever be stored, which is true in the sense that finding one is a
  preimage attack. SHA-256 of the empty string is not zero, so there is no
  accidental collision with an empty block either.
- **Holes appear only at the leaf level.** Because a hole's span is arbitrary, a
  hole of any length is one entry, so upper-level hole entries buy nothing and
  would break the rule that a level-*k* entry corresponds to one level-(*k*−1)
  block.
- **`depth` 0 means the root names a single data chunk**, and `root` is the zero
  hash exactly when the data is inline. This saves one object and one GET for
  every file between 161 bytes and one chunk, which is the common case; the cost
  is one branch in the walk. WAFL does the analogous thing with 16 direct
  pointers, but its thresholds are not ours.
- **Canonical shape.** Adjacent holes are merged, and `depth` is the least that
  fits the leaf count. Nothing demanded this; without it two identical leaf
  sequences could produce different root hashes and stop deduplicating. Note it
  makes the tree a function of the *leaf sequence*, not of the file's bytes: a
  file built by random writes and one written sequentially may chunk
  differently, and under a future CDC chunker they certainly can.
- **Every byte of a block is determined by its contents.** Reserved header bytes
  are zero. This is not a style rule: a timestamp, a writer id or uninitialised
  padding in a metadata block would stop two identical subtrees deduplicating,
  which is the property section 8's sweeper economics rest on.
- **The inode record's `blocks` field becomes `used`, in bytes.** Same offset,
  same width. "Blocks" has no fixed meaning once chunks vary, and the value it
  feeds — `vfs.Attr.Used`, and through it RFC 1813's `fattr3.used` — is defined
  in bytes. This is a correction I made while respecifying the record, not
  something #18 asked for.
- **A maximum chunk size is mandatory, and stays at today's 1 MiB.** Not because
  the format needs one — a span is 64 bits — but because verifying a block's
  hash on read requires fetching all of it, so the maximum chunk size *is* the
  read amplification of a small random read. Any future chunker must declare
  min, average and max. I did not change the current value.
- **The chunker is injectable and the fixed one stays the default.** No issue
  said to keep shipping fixed chunks; I am choosing not to ship an unmeasured
  optimization, per #18's own insistence on numbers first.
- **Scope boundary: phase 1 does not parse this format.** I assumed the block
  layer stores, hashes, routes and caches opaque bodies, and that encoding and
  decoding indirect blocks belongs to phase 2. Section 14 does not say which
  phase owns the format; splitting it this way is what makes phase 1
  independent of this ADR.
- **The dirent design in section 4 stands.** The argument above leans on
  variable-length directory entries being already decided. If phase 3 were
  re-specified with fixed-size padded directory blocks, that leg of the argument
  goes away — though the CDC leg does not.
- **No migration burden.** I assumed there is no deployed filesystem in the
  redesign's format, because the design document says it is not implemented.

## Alternatives considered

### 1. Keep fixed chunking, close #18 with the reasoning recorded

The simplest thing, and it is WAFL's own design: the paper's inodes hold 16
pointers that "all refer to blocks at the same level", over 4 KB blocks with no
fragments. Position implies offset; the tree is fully implicit; `depth` is
derivable; no entry is ever inserted except at the end.

Rejected for two reasons, in order of weight. First, it does not actually fit
the rest of section 4: `dirent` blocks hold variable-length entries, so a
fixed-size layout forces padding and makes a directory's `size` a lie. Second,
it forecloses #18 permanently in the place that is most expensive to reopen, in
exchange for a simplification that is smaller than it looks — the walk is a
division instead of a running sum, and everything else is the same code.

It remains the right answer if the leaves really are all one size, and that is
worth saying: had directories not been specified this way, this ADR would
probably have gone the other way and accepted the rewrite risk.

### 2. Adopt FastCDC now, respecify section 4 around it

This is what #18 proposes. FastCDC is the right algorithm if CDC is adopted —
its authors report roughly 10× the throughput of Rabin-based CDC and 3× that of
Gear, at "nearly the same deduplication ratio as the classic Rabin-based
approach" — and it needs nothing outside the standard library: a 256-entry table
of random 64-bit values, shifts, adds and a mask.

Rejected as premature, not as wrong. The measurement #18 demands has not been
made, and there is a specific reason to distrust an argument-from-first-
principles here: the target deployment in section 2 is an on-premise ECS cluster
on a LAN, where per-request latency dominates and bandwidth is comparatively
cheap, which is the environment least favourable to CDC. Adopting the algorithm
also drags in re-chunking on partial writes, a termination argument for the
resynchronisation loop, and a re-chunking truncate — real work with real edge
cases, none of it needed to unblock phase 2.

The published parameters also do not transfer: the authors' own reference
implementation is parameterised around 2 KB, 8 KB and 32 KB expected chunk
sizes, two to three orders of magnitude below the 1 MiB that amortises an HTTPS
round trip. Whatever parameters this system uses will have to be derived here,
which is another reason not to adopt the algorithm in the same breath as the
layout.

### 3. Pay for a boundary-agnostic layout now — **chosen**

Carry the span, keep the chunker fixed, decide the algorithm on evidence.

The objection to this option is that it is speculative generality: machinery
built for a benefit that has not been measured and might be zero. Two things
answer that. The dirent argument means the machinery is not speculative — phase
3 needs it. And the variable-arity paths are *exercised* rather than dormant,
because the chunker is injectable: phase 2's differential test against
`os.File` can run with a deterministic pseudo-random chunker emitting adversarial
sizes, which is a harder test than the fixed chunker provides and costs nothing
to write.

### 4. A layout that gets both — does not exist

Worth recording as a dead end, since it is the first thing one looks for.
"Position implies offset" and "boundaries chosen from content" are mutually
exclusive by construction: content-defined boundaries are, by definition, not at
predictable byte positions. Alignment tricks (content-defined boundaries
constrained to multiples of some unit) fail on the motivating case — a one-byte
insertion shifts everything by one byte, so no aligned boundary survives. An
extent indirection (leaf names a range inside a larger object) does not help
either, because the fixed leaf's *bytes* still differ after a shift, whatever
you call them. There is no middle road; the choice is binary and this ADR makes
it.

## Consequences

**Better.** Phase 2 is written once. Directories get variable-length dirent
blocks without padding, and a directory's `size` is the packed length of its
entries. A hole of any length costs one 40-byte entry. The inode record does not
change size or shape — issue #18 predicted `chunkRef` would gain an offset, but
in a tree the span lives in the indirect entry, so the 256-byte record is
untouched. The chunker becomes a testable seam, and phase 2 gets a harder
differential test for free.

**Worse.** Fanout 1638 rather than 2048, and 25% more bytes per reference. The
walk is a running sum rather than a division. `depth` must be stored and trusted.
Blocks carry a header. An edit that changes a region's leaf count repacks the
rest of that level. Section 14's phase 2 grows a little: a general splice of a
run of leaf entries, which the fixed layout would only ever have needed at the
tail.

**Now hard to change.** The 40-byte entry and the 64 KiB block are a format: a
reader that cannot parse them cannot mount. Changing either later means a format
version in `fsinfo` and dual-read support. That is the price of deciding now, and
it is the price #18 asked to pay.

**Unaffected.** The commit protocol (section 5), snapshots (section 6),
mark-and-sweep (section 8), NVRAM-free durability (section 9), crash recovery
(section 10) and the NFS, RPC and store layers. None of them look inside a file
tree.

**No new dependency.** Spans, SHA-256 and binary search are standard library. A
future FastCDC would be too.

## What this does not decide

- **Which chunker.** Fixed 1 MiB stays until a measurement says otherwise. #18's
  remaining "done when" items — dedup ratio over a documented corpus, the
  prepend-a-byte test, counting chunks written for random middle writes — are
  the algorithm's entry criteria and no longer block phase 2.
- **What would change the answer.** If measurement shows a materially better
  stored-to-written ratio on data this filesystem will actually hold, adopt
  FastCDC with parameters derived at object-storage scale. If it shows little,
  keep the fixed chunker permanently and lose nothing but the option — the
  layout is still the right one, for the dirent reason. Nothing plausible sends
  the *layout* back to bare references.
- **Re-chunking on partial writes.** The termination argument for the
  resynchronisation loop, and how the write path represents dirty byte ranges
  rather than dirty chunk indices, belong with the chunker decision and with
  phase 4's consistency point. The tree layer is indifferent: it is handed a run
  of leaf entries to splice.
- **How a directory locates the block holding a name.** Binary searching across
  dirent blocks needs an ordered index of first keys or an equivalent; the span
  layout does not provide one and does not prevent one. That is phase 3's
  problem.
- **Encryption (#17).** Chunking runs on plaintext before encryption, so a
  per-chunk AEAD composes with either chunker. Unchanged by this ADR, and still
  worth confirming rather than assuming when #17 is designed.

## Sources

- Dave Hitz, James Lau, Michael Malcolm, *File System Design for an NFS File
  Server Appliance*, USENIX Winter 1994 —
  https://www.usenix.org/legacy/publications/library/proceedings/sf94/full_papers/hitz.a
  Taken: "It uses 4 KB blocks with no fragments"; "all the block pointers in a
  WAFL inode refer to blocks at the same level. Thus, inodes for files smaller
  than 64 KB use the 16 block pointers to point to data blocks"; "For very small
  files, data is stored in the inode itself in place of the block pointers."
  Used as evidence that the uniform-depth, position-implies-offset tree is
  WAFL's actual design (so alternative 1 is the faithful translation), and that
  the paper sets no byte threshold for the inline case — the 160 bytes in
  section 4 is this project's number, not the paper's.
- Wen Xia et al., *FastCDC: a Fast and Efficient Content-Defined Chunking
  Approach for Data Deduplication*, USENIX ATC 2016 —
  https://www.usenix.org/conference/atc16/technical-sessions/presentation/xia
  Taken, from the abstract on that page: the three techniques (simplified hash
  judgment, skipping sub-minimum cut-points, normalising the chunk-size
  distribution); "10x faster" than Rabin-based CDC and "3x faster" than
  Gear-based, "while achieving nearly the same deduplication ratio as the
  classic Rabin-based approach". **Caveat on this citation:** the paper body is
  a PDF, and the tooling available here returned it undecoded with no renderer
  installed, so only the abstract could be read at the primary source. No claim
  above depends on a number from the paper's evaluation tables.
- Wen Xia, `FastCDC-c` reference implementation —
  https://github.com/wxiacode/FastCDC-c/blob/master/fastcdc.h
  Taken: the mask constants are named `FING_GEAR_02KB`, `FING_GEAR_08KB`,
  `FING_GEAR_32KB`, i.e. the reference implementation is parameterised for
  kilobyte-scale expected chunk sizes. Used only to support the claim that the
  published parameters are far below what object storage wants. This is the
  author's code, not the paper's text, and is cited as such.
- RFC 1813, *NFS Version 3 Protocol Specification* —
  https://www.rfc-editor.org/rfc/rfc1813.txt
  Taken: a WRITE's data "must be less than or equal to the value of the wtmax
  field in the FSINFO reply", and a READ's count likewise against `rtmax`.
  strata advertises both as 1 MiB (`internal/nfs/nfs3.go`), so once chunks vary
  no code may assume a client's WRITE aligns with a chunk boundary — under the
  fixed 1 MiB chunker today it usually does, which is exactly the kind of
  accident a variable chunker must not be allowed to rely on.
- The proof of concept itself: `internal/blobfs/fs.go`, `inode.go`, `commit.go`,
  `cache.go`. Taken: the concrete shape of what changes — `chunkIndex`,
  the `idx = pos / cs` loops in `Read` and `Write`, the index-keyed
  `openFile.dirty` map, `truncate`'s `lastIdx`/`tail` arithmetic and its
  hole-filling `chunkRef{Size: chunkSize}`, and `chunkCache`'s reliance on
  content addressing for coherence. None of it changes under this ADR, because
  the redesign replaces `blobfs` wholesale; it is cited so the cost of the
  alternative is measured against real code.
