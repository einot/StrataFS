# strata

A proof-of-concept filesystem for Linux and macOS whose bytes live in one S3
bucket and whose namespace lives in another. It mounts as a normal filesystem
with no kernel extension, by serving NFSv3 on loopback.

```
$ strata -data s3://acme-chunks -meta s3://alice-namespace
$ sudo mount -t nfs -o vers=3,tcp,port=20490,mountport=20490,noresvport 127.0.0.1:/ /tmp/strata-mnt
$ cp ~/big-video.mov /tmp/strata/
```

> **Where this is going:** [docs/DESIGN.md](docs/DESIGN.md) describes the
> WAFL-derived redesign that replaces the monolithic namespace below with a
> Merkle tree of metadata blocks, giving O(changed paths) commits, free
> snapshots under `.snapshot/`, and exact garbage collection. The code in this
> repository is still the proof of concept described here.

## The idea: split the buckets by what they reveal

Most S3-backed filesystems map paths to object keys: `/photos/2024/beach.jpg`
becomes the object `photos/2024/beach.jpg`. That is simple, but it means the
bucket *is* the namespace. Anyone who can list the bucket learns every filename,
every directory, and the size of every file. You cannot share the storage
without sharing the structure.

strata separates the two:

| | **Data bucket** | **Metadata bucket** |
|---|---|---|
| Holds | `chunks/<sha256>` and nothing else | `root`, `snapshots/<epoch>-<nonce>.json.gz` |
| Reveals | chunk sizes and count | the whole namespace |
| Mutability | append-only; objects never change | one small mutable pointer |
| Sharing | safe to share read-only, or publish | private to the mount |

Because a chunk's key is the SHA-256 of its contents, the data bucket contains
no names, no paths, no directory structure, and no notion of which chunks belong
to the same file. That is what makes it shareable.

Three things follow directly:

**Independent read-only sharing.** Give someone read access to the data bucket
and a copy of your namespace, and they mount a private tree over your bytes.
They cannot write to your storage — the mount enforces it, and so does the
bucket policy. Several people can mount the *same* data bucket with *different*
metadata buckets, each with their own view.

**Deduplication for free.** Identical content written by anyone, anywhere in any
tree, is stored once. Three copies of the same file cost one chunk.

**Atomic commits.** The only mutable object in either bucket is a single small
`root` pointer, swapped with a conditional write. Readers see the old tree or
the new one, never a half-written mixture.

## How a commit works

The ordering is the entire durability argument:

1. Every dirty chunk is uploaded to the data bucket under its content hash.
2. The complete namespace is serialized, gzipped and written as a new immutable
   snapshot object.
3. The `root` pointer is swapped to the new snapshot with `If-Match` on the
   ETag we last read.

A crash at any point leaves unreferenced objects behind, never a root pointer
naming data that does not exist. Because step 3 is conditional, a second writer
that has been committing to the same metadata bucket is *detected* rather than
silently clobbering the first: the losing mount marks itself diverged, refuses
further writes, and says so.

Superseded snapshots are pruned in the background, keeping a short rollback
window (`-snapshot-retention`, default 10). Without that a busy mount fills the
metadata bucket quickly: a single smoke-test run produced 440 of them.

Unreferenced chunks are not reclaimed inline — they may be shared with other
files or other people's trees, so collecting them is a separate mark-and-sweep
problem. See "Not implemented" below.

## Why NFS instead of FUSE

A filesystem needs a kernel interface. The options on macOS:

- **macFUSE** is the standard answer and the right production choice, but it is
  a kernel extension: admin password, reboot, and an approval in System
  Settings before a single line runs.
- **FSKit** is Apple's kext-free framework, but it is macOS 15+ only and needs a
  signed app extension, so the Linux half would need a second implementation.
- **Loopback NFS** needs nothing. Both kernels have shipped an NFSv3 client for
  decades. One `mount` command and the same code path works on both platforms.

For a proof of concept the tradeoff is obvious. The cost is that NFSv3 is not
perfectly POSIX: no hard links here, no `O_APPEND` atomicity, and the usual
NFS close-to-open consistency rather than strict coherence. The server is a
hand-written ONC RPC + XDR + NFSv3 implementation with no dependencies.

## Layout

```
cmd/strata/          the binary: flags, wiring, mount instructions
internal/xdr/        XDR (RFC 4506) codec
internal/sunrpc/     ONC RPC v2 (RFC 5531): record marking, AUTH_SYS, dispatch
internal/nfs/        NFSv3 and MOUNTv3 programs (RFC 1813)
internal/vfs/        the filesystem contract the protocol talks to
internal/store/      object store: S3 client with hand-rolled SigV4, plus a local backend
internal/blobfs/     the filesystem itself: inodes, chunking, commits
```

Nothing outside the standard library is used, including for S3: the signer is
verified against the published AWS `get-vanilla` test vector in
`internal/store/sigv4_test.go`.

## Running it

No credentials needed — two local directories stand in for buckets:

```bash
go build ./cmd/strata
./strata -data /tmp/strata-data -meta /tmp/strata-meta
```

It prints the exact mount command. In another terminal:

```bash
mkdir -p /tmp/strata-mnt
sudo mount -t nfs -o vers=3,tcp,port=20490,mountport=20490,noresvport 127.0.0.1:/ /tmp/strata-mnt
```

`mount` needs root on both macOS and Linux; the server itself does not, and
binds to loopback only.

Against real S3:

```bash
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...
./strata -data s3://my-chunks -meta s3://my-namespace -region eu-west-1
```

Against someone else's shared chunk bucket:

```bash
./strata -data s3://their-chunks -data-read-only -meta s3://my-namespace
```

Against MinIO or another S3-compatible server:

```bash
./strata -data s3://chunks -meta s3://meta \
  -endpoint http://127.0.0.1:9000 -path-style
```

## Exercising a live mount

`scripts/smoke-test.sh /tmp/strata` drives a mounted filesystem the way a real
workload does — 8 MB copies, nested directories, renames, random-access writes,
truncation, sparse files, symlinks, chmod, and a 200-file directory — and
reports pass/fail per case.

It must run in a shell allowed to touch network volumes. On macOS a sandboxed
or non-interactive process is refused with `EPERM` no matter what the
filesystem's own permissions say, which looks alarming but has nothing to do
with the mount.

## Tests

```bash
go test -race ./...
```

Covers the storage engine (durability across remount, dedup, sparse files,
truncate, rename edge cases, stale handles, the two-writer conflict) and the
protocol end-to-end over a real TCP socket with a hand-written RPC client. The
test asserting that the data bucket leaks no filenames is
`TestDataBucketLeaksNoNames`; the read-only sharing scenario is
`TestSharedReadOnlyDataBucket`.

S3 conformance runs against a live endpoint when one is configured:

```bash
STRATA_S3_ENDPOINT=http://127.0.0.1:9000 STRATA_S3_BUCKET=strata-test \
AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin \
go test ./internal/store -run Conformance -v
```

## Honest limitations

This is a proof of concept. What that means concretely:

- **The namespace is held in memory and written whole on every commit.** One
  GET gives a mounted client a consistent view and there is no log to replay,
  but a tree of millions of files would make commits expensive. Real systems
  shard the namespace or keep a log with periodic checkpoints.
- **Single writer per metadata bucket.** Concurrent writers are detected, not
  merged. A second mount that loses the compare-and-swap stops rather than
  corrupting anything.
- **No chunk garbage collection.** Superseded *snapshots* are pruned, but
  deleting a file only unlinks it from the namespace; its chunks stay. A mark-and-sweep over reachable hashes would be the next
  piece of work, and has to account for chunks shared with other trees.
- **No hard links**, and MKNOD is refused. FSINFO advertises both absences.
- **Fixed-size chunking**, so inserting a byte at the front of a large file
  rewrites every chunk. Content-defined chunking would fix this and is a
  natural extension of the content-addressed design.
- **Writes are buffered in memory** until commit, so a single file being
  written faster than it uploads can grow the resident set.
- **No encryption.** Chunks are stored as-is. Since the data bucket is the part
  meant to be shared, per-chunk encryption with keys held in the metadata
  bucket is the obvious next step and fits the split cleanly.
- **atime is not updated on read**, deliberately: doing so would dirty the
  namespace on every read.

## License

MIT. See [LICENSE](LICENSE).
