# strata

A proof-of-concept filesystem for Linux and macOS that stores itself in an
S3-compatible bucket and mounts as a normal filesystem — no kernel extension,
no FUSE, no admin install.

```
$ strata -bucket s3://my-filesystem -endpoint https://objectscale.example.net -path-style
$ sudo mount -t nfs -o vers=3,tcp,port=20490,mountport=20490,noresvport,nolocks 127.0.0.1:/ /tmp/strata-mnt
$ cp ~/big-video.mov /tmp/strata-mnt/
```

> **Where this is going:** [docs/DESIGN.md](docs/DESIGN.md) describes the
> WAFL-derived redesign that replaces the monolithic namespace below with a
> Merkle tree of metadata blocks, giving O(changed paths) commits, free
> snapshots under `.snapshot/`, and exact garbage collection. The code in this
> repository is still the proof of concept described here.

## The idea

Everything lives in one bucket, in three kinds of object:

| Key | Contents | Mutable? |
|---|---|---|
| `chunks/<sha256>` | file contents, split into chunks | never |
| `snapshots/<epoch>-<nonce>.json.gz` | a complete namespace image | never |
| `root` | which snapshot is current | **the only mutable object** |

A chunk's key is the SHA-256 of its bytes. Two consequences follow, and they
carry most of the design:

**Writing the same bytes twice stores them once.** Deduplication is not a
feature bolted on; it is what content addressing means. Three copies of a file
cost one set of chunks.

**A modified chunk cannot overwrite the old one.** Change a byte and the hash
changes, so the new data lands at a new key by construction. This is
copy-on-write without needing to implement copy-on-write, and it is what makes
the commit protocol below safe.

## How a commit works

The ordering is the entire durability argument:

1. Every dirty chunk is uploaded under its content hash.
2. The complete namespace is serialized, gzipped, and written as a new
   immutable snapshot object.
3. `root` is swapped to point at it, with `If-Match` on the ETag we last read.

A crash at any point leaves unreferenced objects behind, never a `root` naming
data that does not exist. Readers see the old filesystem or the new one, never
a half-written mixture.

Because step 3 is conditional, a second process that has been committing to the
same bucket is *detected* rather than silently clobbering the first: the losing
mount marks itself diverged, refuses further writes, and says so. Superseded
snapshots are pruned in the background, keeping a short rollback window
(`-snapshot-retention`, default 10).

This is the consistency-point design from the WAFL paper, which reaches the same
place by a different route: WAFL writes only to unused disk blocks between
consistency points, so the last consistency point stays intact until the root
inode is atomically replaced. Content addressing gives us "only writes to unused
blocks" for free, since a new hash is by definition an unused key.

## Why NFS instead of FUSE

A filesystem needs a kernel interface. On macOS the options are:

- **macFUSE** — the standard answer and the right production choice, but it is a
  kernel extension: admin password, reboot, and an approval in System Settings
  before a single line runs.
- **FSKit** — Apple's kext-free framework, but macOS 15+ only and it needs a
  signed app extension, so the Linux half would need a second implementation.
- **Loopback NFS** — needs nothing. Both kernels have shipped an NFSv3 client
  for decades. One `mount` command, same code path on both platforms.

The cost is that NFSv3 is not perfectly POSIX: no hard links, no `O_APPEND`
atomicity, and close-to-open consistency rather than strict coherence. The
server is a hand-written ONC RPC + XDR + NFSv3 implementation with no
dependencies.

## Layout

```
cmd/strata/          the binary: flags, wiring, the -check probe
internal/xdr/        XDR (RFC 4506) codec
internal/sunrpc/     ONC RPC v2 (RFC 5531): record marking, AUTH_SYS, dispatch
internal/nfs/        NFSv3 and MOUNTv3 programs (RFC 1813)
internal/vfs/        the filesystem contract the protocol talks to
internal/store/      object store: S3 client with hand-rolled SigV4, plus a local backend
internal/blobfs/     the filesystem itself: inodes, chunking, commits
```

Nothing outside the standard library is used, including for S3: the SigV4
signer is verified against AWS's published `get-vanilla` test vector in
`internal/store/sigv4_test.go`, and against a live server by the conformance
suite.

## Running it

No credentials needed — a local directory stands in for a bucket:

```bash
go build ./cmd/strata
./strata -bucket /tmp/strata-demo
```

It prints the exact mount command. In another terminal:

```bash
mkdir -p /tmp/strata-mnt
sudo mount -t nfs -o vers=3,tcp,port=20490,mountport=20490,noresvport,nolocks,locallocks 127.0.0.1:/ /tmp/strata-mnt
```

`mount` needs root on both macOS and Linux; the server itself does not, and
binds to loopback only.

Against AWS S3:

```bash
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...
./strata -bucket s3://my-filesystem -region eu-west-1
```

Against an on-premise or S3-compatible cluster — Dell ObjectScale, MinIO, Ceph:

```bash
./strata -bucket s3://my-filesystem \
  -endpoint https://objectscale.example.net -path-style
```

## Checking an endpoint before you trust it

"S3-compatible" is a spectrum, and implementations vary most in exactly the
places this filesystem is most sensitive to. `-check` probes a live endpoint for
the behaviour strata depends on and then exits, writing nothing permanent:

```bash
./strata -bucket s3://my-filesystem -endpoint https://objectscale.example.net -path-style -check
```

```
  [ok  ] put and get                        objects round-trip intact
  [ok  ] keys needing escaping              signing handles spaces and non-ASCII
  [ok  ] ranged reads                       partial reads work; chunks fetch individually
  [ok  ] head returns an etag               etag e80b50170989…
  [ok  ] missing key reports 404            absent objects are distinguishable
  [ok  ] list with prefix                   listing is needed to prune old snapshots
  [ok  ] delete                             objects can be reclaimed
  [ok  ] conditional create (If-None-Match) create-if-absent honoured
  [ok  ] conditional update (If-Match)      update with the current etag succeeds
  [ok  ] stale If-Match is REJECTED         a second writer is detected, not silently lost
```

The last one is the one that matters. An endpoint that *accepts* a write
conditioned on a superseded ETag cannot protect the root pointer, so two mounts
of the same bucket will silently overwrite each other's commits. strata still
runs there — it falls back to an unconditional write — but you must guarantee a
single writer yourself.

## Tests

```bash
go test -race ./...
```

Covers the storage engine (durability across remount, dedup, sparse files,
truncate, rename edge cases, stale handles, the two-writer conflict, snapshot
pruning) and the protocol end-to-end over a real TCP socket with a hand-written
RPC client.

Two suites additionally run against a live S3 endpoint when one is configured,
and skip otherwise:

```bash
STRATA_S3_ENDPOINT=http://127.0.0.1:9000 \
AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin \
go test -race ./...
```

They use separate buckets (`strata-test` and `strata-test-fs`) because the
filesystem suite empties its bucket before running, and `go test ./...` runs
packages in parallel. Both refuse to run against a bucket whose name does not
contain "test".

## Exercising a live mount

`scripts/smoke-test.sh /tmp/strata-mnt` drives a mounted filesystem the way a
real workload does — 8 MB copies, nested directories, renames, random-access
writes, truncation, sparse files, symlinks, chmod, and a 200-file directory —
and reports pass/fail per case.

It must run in a shell allowed to touch network volumes. On macOS a sandboxed or
non-interactive process is refused with `EPERM` no matter what the filesystem's
own permissions say, which looks alarming but has nothing to do with the mount.

## Honest limitations

This is a proof of concept. What that means concretely:

- **The namespace is held in memory and written whole on every commit.** One GET
  gives a mounting client a consistent view and there is no log to replay, but a
  tree of millions of files would make commits expensive. This is the limitation
  [docs/DESIGN.md](docs/DESIGN.md) exists to remove.
- **Single writer per bucket.** Concurrent writers are detected, not merged.
- **No garbage collection of chunks.** Superseded *snapshots* are pruned, but
  deleting a file only unlinks it from the namespace; its chunks stay.
- **No hard links**, and MKNOD is refused. FSINFO advertises both absences.
- **Fixed-size chunking**, so inserting a byte at the front of a large file
  rewrites every chunk.
- **Writes are buffered in memory** until commit.
- **No encryption.** Chunks are stored as-is.
- **atime is not updated on read**, deliberately: doing so would dirty the
  namespace on every read.

## License

MIT. See [LICENSE](LICENSE).
