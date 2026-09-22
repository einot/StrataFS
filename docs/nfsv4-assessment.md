# NFSv4.2 readiness: what a port would take, and whether to do it

**Status:** assessment. This document commits the project to nothing. It answers
one question — what would it take to serve NFSv4.2 instead of, or beside,
NFSv3 — and recommends **not doing it now**, with a trigger condition for
reopening the question (section 6).

**What landed with it.** Two things were changed alongside this document, both
cheap today and expensive later, neither of which starts the port:

- [`docs/DESIGN.md`](DESIGN.md) section 4's inode record now reserves 8 bytes
  for the NFSv4 change attribute, while the 256-byte layout is still unbuilt and
  free to change.
- `internal/vfs/vfs.go` gained four optional, unimplemented interfaces —
  `Sparse`, `Cloner`, `Deallocator`, `Pinner` — so that the file-tree work in
  section 14's phase 2 is done with hole queries and reference splicing in view
  rather than retrofitted, and so the READDIR cookie rule NFSv4 depends on is
  written down instead of being satisfied by accident.

Nothing else was implemented. `docs/DESIGN.md` remains the normative document;
where it and this assessment disagree, the spec wins.

## Provenance, and how much of this to take on faith

1. **RFC 8881 (NFSv4.1) and RFC 7862 (NFSv4.2) are too large to retrieve whole
   with the tooling available here.** Every attempt truncated between §2.10 and
   §8. What was obtained: the NFSv4.2 operation-status table and the table of
   contents from the HTML rendering of RFC 7862, the complete `nfs_opnum4` list
   and several verbatim XDR enums from RFC 7863, RFC 8276 in full, and the
   sections of RFC 7530 quoted below. Anything beyond that is flagged inline as
   **[recalled, unverified]** — it is a reading of the protocol, not a quote, and
   the section must be checked before anyone implements against it.
2. Line counts below are from the files as they stood at commit `491739f`;
   nothing was executed to produce them.
3. This assessment predates any NFSv4 code. It is an estimate of work not done.

---

## 1. What carries over

| Component | Lines at `491739f` | Verdict |
|---|---|---|
| `internal/xdr` | 187 | **Survives.** +~100 lines |
| `internal/sunrpc` | 373 | **Survives** the transport; one structural addition only if callbacks are wanted |
| `internal/nfs/nfs3.go` | 594 | Rewritten. Intent transfers, lines don't |
| `internal/nfs/wire.go` | 239 | ~half dead (`post_op_attr`, `wcc_data`); `statusOf` survives in shape |
| `internal/nfs/mount.go` | 149 | **Dead for v4.** Keep only while v3 is served |
| `internal/vfs` | 180 | Survives, grows — additively. Section 4 |
| `internal/blobfs` | ~1,940 | Survives essentially untouched; three additions |
| `internal/store`, `cmd/strata/check.go` | — | Untouched. v4 needs no new S3 behaviour |

**The XDR codec is general enough**, and the caveat is not about unions. XDR
discriminated unions are "uint32 discriminant, then the arm" — there is nothing
to generalise; a hand-written `switch` is the idiom and the repo already uses it.
`bitmap4` is `uint32 bitmap4<>`, a count followed by words, expressible today
with `Uint32` in a loop (a typed helper over attribute numbers 0..82 is worth
~60 lines). `attrlist4` is an opaque whose body is a packed sequence of
attribute values — build it in a second `xdr.NewWriter()` and
`w.Opaque(sub.Bytes())`, which is the pattern `integration_test.go` already uses
for AUTH_SYS credentials.

The one real gap: **`Writer` can only append.** `COMPOUND4res` is
`{ nfsstat4 status; utf8str_cs tag; nfs_resop4 resarray<> }` — the array's length
prefix precedes results whose *count is not known when encoding starts*, because
a COMPOUND stops at the first failing operation. That needs either a backpatch
primitive (`Mark() int` / `PatchUint32(pos, v)`, ~10 lines) or a per-op
sub-writer and a concatenation pass (one copy per op, zero new API). That is the
whole codec delta. `maxVarLen` at 64 MiB is fine.

**The RPC layer's threading model carries COMPOUND without changes.**
`serveConn` reads records, fans out to `maxInFlight` goroutines and serialises
writes under `writeMu` — a COMPOUND is just a long call, and NFSv4 has exactly
two procedures (NULL=0, COMPOUND=1), so the v4 server is a `sunrpc.Handler` with
`HasProc(p) = p < 2`. Registering `(100003, 4)` beside `(100003, 3)` needs no
code: `Server.versions` already produces a correct `PROG_MISMATCH` range.
`maxRecord`'s 8 MiB cap stops being a silent limit and becomes a *negotiated*
value declared in `CREATE_SESSION` (`ca_maxrequestsize` / `ca_maxresponsesize`),
which is an improvement.

The one thing `sunrpc` cannot do is **originate a call**. In NFSv4.1 the
backchannel runs in the reverse direction over the *same* TCP connection
[recalled, unverified — RFC 8881 §2.10.3.1, §18.36
`CREATE_SESSION4_FLAG_CONN_BACK_CHAN`]. `dispatch` returns `nil` for any message
that is not a CALL, so an inbound reply is silently dropped; there is no xid
allocator and no pending-call table. **If the server never grants delegations and
answers COPY synchronously, none of this is needed.** That is the single largest
scope lever in the whole port, and the design makes it cheap to pull (section 3).

**Becomes dead code:**

- All of `mount.go`. NFSv4 absorbs MOUNT: `PUTROOTFH` hands out the root
  filehandle and the export is a pseudo-filesystem. RFC 7530 §4.1: *"the NFSv4
  protocol will not use an ancillary protocol for translation from string-based
  pathnames to a filehandle"*. The `-export` flag's meaning changes with it.
- `putPostOpAttr` / `putWccData` / `wccFrom`, and the `attrOf` call after nearly
  every handler in `nfs3.go`. NFSv4 has neither post-op attributes nor weak cache
  consistency; the client appends its own `GETATTR` to the COMPOUND and validates
  caches off the `change` attribute. This deletes more complexity than it adds.
- **ADR 0002's duplicate request cache, for the v4 program.** NFSv4.1's session
  slot table gives exactly-once semantics by construction: no `Idempotent(proc)`
  classification, no 120 s retention guess, no "drop the in-flight duplicate"
  rule (a duplicate on a busy slot gets `NFS4ERR_DELAY`). ADR 0002 was Accepted
  but not yet implemented when this was written, so this is a scheduling
  question rather than sunk cost: if v4.1 ever happens, that cache only ever
  protects the v3 program.

**`internal/blobfs` survives almost whole.** Nothing in the storage engine is
v3-shaped: chunking, the commit ordering, content-addressed verification, dedup,
the cache, and the write verifier (`FS.WriteVerf()` feeds `writeverf4` in v4
exactly as it feeds `writeverf3` today). Three things it must learn — a
persistent change counter, hole and extent queries, and clone-by-reference-splice
— and all three belong in the redesign rather than being bolted onto the proof of
concept twice.

---

## 2. The structural shift, and what it demands from this codebase

### COMPOUND

One procedure carrying `{ tag, minorversion, argarray<> }`, executed in order
against a per-compound context. Concretely, `internal/nfs` grows a state object
threaded through every operation:

```
currentFH, savedFH           // PUTFH/PUTROOTFH set, SAVEFH/RESTOREFH swap
currentStateid, savedStateid // the "current stateid" special value
minorversion, session, slot
```

Three consequences worth naming:

- **Each result frames itself.** `nfs_resop4` is `{ opnum, status, arm }`, so the
  per-op encoder owns its own status — unlike `nfs3.go`, where the handler writes
  a bare status word as the first thing in the reply body.
- **A COMPOUND is not atomic** [recalled, unverified]. A `PUTFH+REMOVE+GETATTR`
  that fails at GETATTR has still removed the file. That is fine in itself, but
  it interacts with the slot cache: the reply for a *partially executed* compound
  must still be cached, or a replay re-executes the REMOVE — the exact bug
  ADR 0002 exists to prevent, reintroduced through a different door.
- **Current-stateid tracking across a COMPOUND is the classic implementation
  error.** An unrelated project's public issue tracker lists precisely this (and
  EXCHANGE_ID / CREATE_SESSION / RECLAIM_COMPLETE validation gaps) among its
  pynfs failures — weak secondary evidence about where implementations trip, not
  a specification.

### MOUNT and NLM absorbed

MOUNT's disappearance is pure simplification. **NLM's absorption is not.** The
binary prints `nolock` (Linux) / `nolocks,locallocks` (macOS) in its mount
instructions and the README lists the absence of NLM as a known limitation. Per
`nfs(5)`, `nolock` and `local_lock` are **"Options for NFS versions 2 and 3
only"** — the escape hatch does not exist in v4. `LOCK`/`LOCKT`/`LOCKU` are
REQUIRED operations in 4.1 [recalled, unverified — RFC 8881 §17], and clients
will use them.

The good news: byte-range locks are *pure protocol state* here. Nothing outside
this server can touch the bytes — a second mount is already declared unsafe by
the single-writer design — so an interval list per file keyed by lock-owner,
living entirely in `internal/nfs`, is correct and never needs to reach the vfs.
This is one of several places where being a single-writer loopback server makes
the job dramatically smaller than a general-purpose server's.

### Statefulness — the part that is 60% of the work

| Mechanism | What it demands here |
|---|---|
| `EXCHANGE_ID` → clientid | Client table keyed on owner id; detect client restart by verifier change and discard the old state |
| `CREATE_SESSION` + `SEQUENCE` | Slot table: per slot a sequence number and a cached reply. `seq == last+1` → execute and cache; `seq == last` → replay cached bytes; otherwise `NFS4ERR_SEQ_MISORDERED`; in flight → `NFS4ERR_DELAY`. **Bounded by construction** and negotiated, unlike ADR 0002's 4096/120 s judgement calls |
| Stateids `(seqid, other[12])` | Validate on READ/WRITE/SETATTR(size)/CLOSE/LOCK, plus the two special stateids. `TEST_STATEID`/`FREE_STATEID` REQUIRED |
| Share reservations | `OPEN` carries share_access/share_deny; conflicts → `NFS4ERR_SHARE_DENIED`. Protocol state only |
| Lease renewal | **In 4.1 any `SEQUENCE` renews the lease implicitly** [recalled, unverified — RFC 8881 §8.3]; `RENEW` is 4.0-only. This costs almost nothing, and is a strong argument for 4.1 over 4.0 |
| Grace period | After restart, refuse new opens and locks with `NFS4ERR_GRACE`; accept reclaims (`CLAIM_PREVIOUS`, `reclaim=true`); `RECLAIM_COMPLETE` ends a client's reclaim |
| Delegations | **Optional. Return `OPEN_DELEGATE_NONE` and the work disappears** [recalled, unverified]. This is what keeps the backchannel out of scope |

**What must persist across a server restart, and where — given the backing store
is an S3 bucket.**

Exactly one thing: **client records**. The server must know, after restart, which
clients held state *before* it, so it can allow their reclaims and refuse
everyone else's — otherwise a reclaim resurrects a lock over a file another
client has since modified. Linux's server persists exactly this and nothing more.
The content is tens of bytes: client owner id, boot verifier, a "has reclaimed"
flag. The write frequency is one PUT on a client's *first* OPEN, one on
`RECLAIM_COMPLETE`, one DELETE on expiry — per client per mount, not per
operation.

Everything else is recreated by reclaim: opens, locks, stateids, session slots.
The write verifier stays freshly random per boot, exactly as `DESIGN.md` section
9 requires.

Where it lives: **a `clients/` prefix in the bucket.** It is the only choice
consistent with "the filesystem stores itself in one bucket", it survives host
loss, and it needs *no S3 behaviour that `-check` does not already probe* (PUT,
GET, DELETE and LIST of small objects). Three wrinkles that a decision record
must settle rather than leave to the implementer:

- The record must be durable **before** the first OPEN reply goes out, so that is
  a synchronous PUT on the first open per client. One LAN round trip, once per
  mount. Acceptable — and worth saying so explicitly, so nobody later "optimises"
  it onto the background committer.
- **Grace period duration must be at least the lease time**, and `lease_time` is
  a REQUIRED attribute the server chooses. Ninety seconds (a common value) of
  `NFS4ERR_GRACE` after every restart would wreck this project's own demo story:
  start the binary, mount, copy a file. Two legal mitigations: a short lease, or
  lift the grace period immediately when the client table is empty, because there
  is nobody to reclaim.
- A crash loses up to `-commit-interval` of *data*, so a lock can be reclaimed
  against a filesystem that has rolled back. That is not new — v3 plus NLM has
  the same hole, and the write verifier still forces the client to resend — but
  it should be written down rather than discovered.

### Identity and security — two deviations that would have to be chosen

- **owner/owner_group are strings.** v4 sends `user@domain`; the vfs is numeric.
  RFC 7530 §5.9 permits the stringified-numeric form when translation is
  impossible, and that is what both Linux and macOS accept with AUTH_SYS and no
  idmapper. Cheap, but it needs a decision and possibly a `-domain` flag.
- **RPCSEC_GSS is mandatory to implement, and this project will not implement
  it.** RFC 7530 §3.2: *"the RPCSEC_GSS security flavor MUST be used to enable
  the mandatory-to-implement security mechanism"*; §3.2.1: *"NFSv4 clients and
  servers MUST support the Kerberos V5 security mechanism"*; AUTH_SYS is a MAY. A
  standard-library-only Kerberos V5 implementation would be larger than the rest
  of this server. A v4 port here is therefore **knowingly non-conformant on
  security**, and must say so in the README and the spec the way the project
  already says so about hard links. It still interoperates, because clients mount
  `sec=sys` by choice. This is a deviation to declare out loud, not an omission
  to discover in review.

---

## 3. What v4.2 adds on top of 4.1 — and where this design is an advantage

Thirteen operations, numbers 59–71, **all OPTIONAL** (RFC 7862 §13). Plus
RFC 8276's four extended-attribute operations (72–75), which are an extension
usable with 4.2 *without* a minor-version bump.

### Nearly free, because of chunking, content addressing and copy-on-write

**SEEK (69)** — verbatim from RFC 7863:

```
enum data_content4 {
       NFS4_CONTENT_DATA = 0,
       NFS4_CONTENT_HOLE = 1
};
```

`blobfs` already represents a hole as a chunk reference with an empty hash, and
`inode.used()` already sums only non-holes. SEEK is a walk of the chunk list with
no I/O whatsoever. Under the redesign it is a descent of the file tree skipping
zero-hash entries — and `DESIGN.md` section 4's rules 5 and 6 (holes only at
level 0, adjacent holes merged) are *exactly* the canonicalisation that lets SEEK
answer in one unambiguous pass. **This is the best fit between v4.2 and this
design, and it was arrived at for unrelated reasons.**

**READ_PLUS (68)** — return a hole segment where the chunk list says hole. Today
`blobfs.Read` *materialises* zeros for holes and then ships them over the wire;
READ_PLUS stops both. For a sparse file the win is the entire file. The caveat
worth writing into the spec so nobody "fixes" it later: holes here are
chunk-granular, and a chunk of explicitly-written zeros is not a hole. A server
may always return data in place of a hole, so that is legal.

**CLONE (71)** — copy chunk references from one file's range to another's. No
data movement, no upload, and because the store is content-addressed the two
files *genuinely* share storage rather than merely appearing to. `clone_blksize`
(`FATTR4_CLONE_BLKSIZE = 77`, verbatim from RFC 7863) exists precisely so the
server can demand alignment: advertise the chunk size and CLONE becomes a splice
of whole entries with zero read-modify-write. This is the same operation as a
snapshot, one level down — the design is already built for it.

**COPY (60), intra-server, synchronous** — CLONE without the alignment
guarantee: the aligned interior is a reference splice, and at most two boundary
chunks need read-modify-write. Copying a 10 GB file on the mount returns
immediately and consumes zero extra bucket space. That is the demonstration that
sells the whole design, and v3 has no way to express it.

**DEALLOCATE (62)** — punch a hole: set the covered references to holes, rewrite
at most two boundary chunks. `used` falls correctly for free, because
`inode.used()` already ignores holes.

**xattrs (RFC 8276, operations 72–75, `xattr_support` attribute 82)** — values
are small; store them in the inode record (proof of concept: a map in the inode
JSON; redesign: a named-attribute subtree). The concrete payoff is macOS:
`mount_nfs` documents `namedattr` as *"For NFSv4 mounts, if the server appears to
support named attributes, they will be used to store extended attributes"*,
against today's AppleDouble `._` files on a v3 mount. Halving the file count in
every directory matters a great deal to a filesystem whose proof of concept
re-serialises the whole namespace on every commit.

### Real work, or not worth it

- **ALLOCATE (59)** — a *guarantee* of space. Against an object store with no
  quota, and an `FSStat` that already invents 1 PiB of headroom, there is nothing
  to guarantee. `NFS4ERR_NOTSUPP` is the honest answer; implementing it as
  "extend the size" lies in the one direction that matters, telling the client a
  later write cannot fail. It is a separate operation from DEALLOCATE, so
  refusing it loses nothing.
- **Inter-server COPY + COPY_NOTIFY (61)** — requires this server to become an
  NFS *client* of another. Out of scope; refuse.
- **Asynchronous COPY + CB_OFFLOAD + OFFLOAD_STATUS/CANCEL (66/67)** — requires
  the backchannel. Avoidable entirely by answering COPY synchronously, which is
  legal and trivially fast here *because copies are metadata-only*. The design
  makes the hard case unnecessary; that is a genuine architectural dividend.
- **IO_ADVISE (63)** — could drive prefetch into the existing chunk cache.
  Answering "no hints accepted" is legal; revisit later.
- **WRITE_SAME (70) and application data blocks** — a block-shaped-data
  optimisation with no natural fit. Refuse.
- **LAYOUTERROR / LAYOUTSTATS (64/65)** — pNFS only. Refuse.
- **Labeled NFS (`FATTR4_SEC_LABEL = 80`)** — storing and returning an opaque
  label is ~50 lines; it only *means* anything with a mandatory access control
  policy on the client that trusts the server. Skip by default.

### The one place content addressing makes v4.2 *harder*

**`space_freed` (`FATTR4_SPACE_FREED = 78`)** is defined as *"space that would be
freed when a file is deleted, taking block-sharing into consideration"*. With
chunks deduplicated across files *and* snapshots, the true answer requires a
reference count — which `DESIGN.md` section 8 deliberately does not keep, having
replaced reference counting with mark-and-sweep on exactly the grounds that a
content-addressed block has no single "the" reference. The attribute is
RECOMMENDED, not REQUIRED: **do not advertise it.** This is the only v4.2 feature
where the design's shape is a liability rather than an asset, and it is worth
stating plainly so it is not discovered as a bug.

---

## 4. The vfs contract — what it could not express, and how it grew

### What was missing

1. **No change attribute.** `vfs.Attr` carries ATime/MTime/CTime and nothing
   else. NFSv4's `change` is a REQUIRED attribute that must differ whenever the
   object's data *or* metadata changes. Using CTime (the
   `NFS4_CHANGE_TYPE_IS_TIME_METADATA` approach) is unsafe here: `blobfs` sets it
   from the wall clock, and two modifications inside one clock tick would share a
   value. A counter is the right answer, and it permits advertising, verbatim
   from RFC 7863:

   ```
   enum change_attr_type4 {
          NFS4_CHANGE_TYPE_IS_MONOTONIC_INCR         = 0,
          NFS4_CHANGE_TYPE_IS_VERSION_COUNTER        = 1,
          NFS4_CHANGE_TYPE_IS_VERSION_COUNTER_NOPNFS = 2,
          NFS4_CHANGE_TYPE_IS_TIME_METADATA          = 3,
          NFS4_CHANGE_TYPE_IS_UNDEFINED              = 4
   };
   ```

   A single global monotonic counter, allocated per mutation and persisted with
   the namespace, gives `MONOTONIC_INCR` and covers directories too, which v4
   clients need for directory caching. Modifications made after the last commit
   reuse their numbers after a crash — exactly the window in which their *data*
   is also lost, so the two stay consistent. The on-disk field is now reserved:
   `DESIGN.md` section 4. The `vfs.Attr` field deliberately is **not** — see
   "What was deliberately left out" below.

2. **No open/close, and no way to keep an unlinked file alive.** This is the one
   piece of open state that *cannot* stay in the protocol layer. A v4 client
   expects a file that is still open to remain readable after REMOVE — in v3 the
   client fakes this with silly-rename; in v4 the server owes it. `blobfs.unlink`
   drops the inode the instant the link count hits zero. Share reservations,
   stateids and lock owners can and should stay in `internal/nfs`; this one needs
   a vfs primitive.

3. **No sparse queries.** Nothing could answer "where is the next hole after
   offset X".

4. **No server-side copy or clone.** COPY and CLONE would degrade to
   read-then-write through the vfs, losing the entire benefit — which is the
   whole reason to implement them.

5. **No named attributes.** 6. **No ACLs** — RECOMMENDED only; stay mode-only and
   skip. 7. **`ReadDir`'s `plus bool`** is a v3 shape; v4 asks for an attribute
   *bitmap* per entry. Harmless today, because attributes are in memory; under
   the redesign, where per-entry attributes cost block fetches, it will matter.

8. **A latent constraint that was satisfied by luck.** NFSv4's READDIR reserves
   cookie values 0, 1 and 2, and does not return `.` and `..` [recalled,
   unverified]. `blobfs.ReadDir` synthesises `.` and `..` at cookies 1 and 2 with
   real entries starting at 3, so filtering the first two and passing cookies
   through happens to work. That is now pinned as a contract in the vfs doc
   comments rather than left as an accident.

### How the contract grew — additively

Four optional interfaces, following the existing `WeakCache` idiom: discovered by
type assertion, absent means unsupported, and a v4 server must degrade to
`NFS4ERR_NOTSUPP` when a backend implements none of them. The authoritative
signatures and contracts are in `internal/vfs/vfs.go`; they are not duplicated
here, because a copy is a copy that drifts.

| Interface | Serves | The obligation that matters |
|---|---|---|
| `Sparse` | SEEK (69), READ_PLUS (68) | Answers from metadata only, never by reading data. May under-report holes, must never over-report them |
| `Cloner` | CLONE (71), intra-server COPY (60) | No data moves through the caller. `CloneBlockSize` is the alignment at which the operation is free, and becomes `clone_blksize` on the wire |
| `Deallocator` | DEALLOCATE (62) | The range reads back as zeros, the file's size does not change, and `Attr.Used` stops counting it |
| `Pinner` | OPEN/CLOSE | An unlinked file stays addressable through an existing handle until released |

Not landed, sketched only, because nothing wants it yet:

```go
// XAttrs is RFC 8276: GETXATTR, SETXATTR, LISTXATTRS, REMOVEXATTR.
// Worth having the day macOS extended attributes should stop being
// AppleDouble files.
type XAttrs interface { /* Get/Set/List/Remove */ }
```

Open state, share modes, stateids and byte-range locks are deliberately **not**
in the vfs. `Pinner` states the one obligation a backend genuinely has and
nothing more; the rest is protocol bookkeeping that no storage backend can help
with, and that a second backend would otherwise have to reimplement for no
benefit.

One further contract decision that keeps the change additive: **`vfs.Status`
stays the POSIX-shaped subset both versions share, and the v4-only codes
(`NFS4ERR_GRACE`, `NFS4ERR_BAD_STATEID`, `NFS4ERR_SEQ_MISORDERED`, …) stay inside
`internal/nfs`.** The backend never generates them, and the doc comment saying
`Status` is an NFSv3 code would otherwise invite someone to pile them in here.

### What was deliberately left out

**`vfs.Attr` did not gain a `Change uint64` field**, although the v4 server will
need one. Three reasons, in order:

- **The cheapness argument that justifies the inode record does not apply.** The
  on-disk record is a *format*: adding 8 bytes after phase 1 ships costs a format
  version and dual-read support. A Go struct field costs nothing to add later —
  there is no migration, and named-field construction means no caller breaks.
- **An unpopulated field is worse than an absent one.** No backend would fill it,
  so every object would report `Change: 0`, which is indistinguishable from a
  legitimate value. A failed type assertion on an optional interface is
  unambiguous; a zero integer is not.
- **It cannot be an optional interface either.** The change attribute must be
  read in the same snapshot as size, mode and the timestamps, or a client can
  observe a change value from before a mutation beside a size from after it —
  precisely the tearing the attribute exists to prevent. So it belongs in `Attr`,
  in one read, or nowhere; and it lands in the same change that teaches a backend
  to populate it.

The requirement is recorded in the `Attr` doc comment so the next person to touch
that struct finds it.

### Additive or breaking?

**Additive.** Nothing that exists changed shape; four interfaces and one struct
appeared. Two obligations are worth flagging for whoever implements them:

- `Pinner` is a behavioural obligation, not just a method: `blobfs.unlink` and
  `Rename`'s victim path must stop dropping inodes unconditionally.
- The eventual `Attr.Change` field is mandatory for every implementer once it
  exists, and in `blobfs` it means a snapshot format field.

---

## 5. Deployment and testing realities

### Mounting

**Linux gets simpler.** From `nfs(5)`: for NFSv4 *"the NFS client uses the
standard NFS port number of 2049 without first checking the server's rpcbind
service"*, `port=` is honoured, `nolock`/`local_lock` are listed under *"Options
for NFS versions 2 and 3 only"*, and with no version given *"the client tries
version 4.2 first, then negotiates down"*. So:

```
sudo mount -t nfs -o vers=4.2,port=20490 127.0.0.1:/ /tmp/strata-mnt
```

No `mountport`, no `nolock`, no rpcbind, one port. The printed instructions get
shorter, and the `-export` flag's meaning changes: under v4 the client mounts the
pseudo-root and looks up from there. The simplest conformant answer is that the
pseudo-root *is* the filesystem root and `-export` is restricted to `/` for v4 —
no pseudo-filesystem nodes, and no fsid boundary to get wrong.

**macOS is the problem, and it is decisive.** The `mount_nfs(8)` man page says,
verbatim: *"Currently NFSv4 is the highest supported version with a minor version
of zero or one."*, *"If no minor version is specified, zero is assumed."*, and
*"Specifying a non supported version or minor version will print a warning and
ignore the `vers` or `nfsvers` option."* Secondary evidence — a third-party issue
tracker, flagged as such — indicates macOS 15 rejects `vers=4.1` and that 4.1
client support arrives in macOS 26.

Therefore: **no macOS release speaks minor version 2.** A 4.1/4.2 server is
reachable from macOS only on the newest release, via `vers=4.1`, and none of the
4.2 operations — the interesting half of the exercise — are reachable from macOS
at all. Covering the macOS versions this project currently supports would require
**NFSv4.0 as well**, which is not a subset of 4.1: different state establishment
(`SETCLIENTID`/`SETCLIENTID_CONFIRM`, `OPEN_CONFIRM`), explicit `RENEW`, no
sessions and therefore no exactly-once semantics, and a *server-to-client
callback connection* for delegations. That is a second implementation, not a
reduction.

This matters more here than it would elsewhere, because macOS is *why* this
project chose NFS in the first place — the README's argument is that FUSE on
macOS needs a kernel extension and FSKit needs a signed app extension.

### `-check`

**Unchanged.** NFSv4 imposes no new requirement on the object store; client
records are ordinary small objects already covered by the existing put, get,
delete and list probes. What changes is `cmd/strata`: a version selector, the
printed mount instructions, and a lease/grace knob. The one *optional* addition
worth considering is a latency measurement on the small-object PUT, because a
synchronous client-record write would sit in the first-OPEN path — an
enhancement, not a requirement.

### Testing the state machine

The existing harness is already the right shape. `integration_test.go` drives a
real TCP socket with a hand-written RPC client and hand-built XDR; a v4 client is
the same thing plus a `compound(ops...)` helper. That level is *better* than a
kernel mount for state-machine conformance, because it can do what no kernel
client will: replay a slot, skip a sequence id, send two SEQUENCEs, present a
stale clientid.

Deterministic, in-process, no root and no mount:

- slot replay returns **byte-identical** cached reply bytes; `seq+2` →
  `NFS4ERR_SEQ_MISORDERED`; a second call on a busy slot → `NFS4ERR_DELAY`
- COMPOUND without SEQUENCE → `NFS4ERR_OP_NOT_IN_SESSION`; unknown minorversion →
  `NFS4ERR_MINOR_VERS_MISMATCH`
- restart with a persisted client record: reclaim accepted, new OPEN →
  `NFS4ERR_GRACE`; reclaim by an unrecorded client → `NFS4ERR_NO_GRACE`; grace
  lifted early when the client table is empty
- share-deny conflict → `NFS4ERR_SHARE_DENIED`; lock conflict → `NFS4ERR_DENIED`
  with the conflicting owner; stale stateid on CLOSE
- unlink-while-open still readable through the open stateid
- a backend implementing none of the optional interfaces degrades to
  `NFS4ERR_NOTSUPP` for SEEK, READ_PLUS, CLONE, COPY and DEALLOCATE

`go test -race ./...` already gates this, and the new shared state — session
tables, lock tables — is precisely where the race detector earns its place.

What the in-process harness cannot answer is whether a real client is *happy*:
the attribute set, the idmapping choice, the lease time, grace behaviour across a
remount. That needs `scripts/smoke-test.sh` against a kernel mount, plus
**pynfs**, the suite Linux server developers use, which covers NFSv4.0 and
4.1/4.2 and speaks the protocol itself rather than going through a kernel client.
It is Python: a *test-time tool*, not a Go module, so it does not touch the
standard-library-only rule — but adding a tool to CI is the repo owner's call.

---

## 6. Staged plan, and the recommendation

### Milestones

| # | Content | Size | Notes |
|---|---|---|---|
| **M0** | Decision records: target minor version and the AUTH_SYS/GSS deviation; the state model (what persists, where in the bucket, lease and grace numbers); the vfs extension. `DESIGN.md` gains an NFSv4 section | **S** (~1 day) | Blocks everything |
| **M1** | XDR additions; `(100003, 4)` registered beside v3; COMPOUND and session layer; PUTROOTFH/PUTFH/GETFH/SAVEFH/RESTOREFH, LOOKUP/LOOKUPP, GETATTR (REQUIRED attributes only), ACCESS, READLINK, READDIR, SECINFO. No OPEN, no state beyond the session | **L** (~2 wk, ~2,000 lines) | Honest deliverable: *mount, traverse, stat, readdir*. Reading a file needs OPEN, so this is not yet "read-only working" |
| **M2** | OPEN/CLOSE/OPEN_DOWNGRADE, stateids, share reservations, READ/WRITE/COMMIT/SETATTR/CREATE/REMOVE/RENAME, TEST_STATEID/FREE_STATEID, unlink-while-open (`Pinner`). Delegations never granted | **L** (~2 wk) | |
| **M3** | LOCK/LOCKT/LOCKU, lease expiry sweeper, client records in the bucket, grace, reclaim and early lift | **M–L** (~1.5 wk) | **End of M3 = a usable NFSv4.1 server** |
| **M4** | minorversion 2; SEEK, READ_PLUS, CLONE, COPY (intra-server, synchronous), DEALLOCATE; everything else refused. `Sparse`/`Cloner`/`Deallocator` implemented in the backend | **M** (~1 wk) | **Best payoff per line in the entire plan** |
| **M5** | xattrs (RFC 8276). Removes the `._` files on macOS | **S–M** (~4 days) | Optional |
| **M6** | pynfs triage, smoke test, version flag, docs | **M** | |

Roughly 8–10 weeks of work and **6,000–9,000 new lines**, against a protocol
layer that is **982 lines** today (`nfs3.go` 594 + `wire.go` 239 +
`mount.go` 149). Scale check from a mature implementation, by source size in
`torvalds/linux/fs/nfsd`: v3 is `nfs3proc.c` 30,579 B + `nfs3xdr.c` 31,963 B
≈ 63 KB; v4 is `nfs4proc.c` 120,046 + `nfs4state.c` 286,473 + `nfs4xdr.c` 187,416
+ `nfs4callback.c` 53,013 ≈ 647 KB — a **10×** ratio, for a server with
delegations, callbacks, pNFS hooks and 4.0 compatibility that this plan all
skips. Anyone who estimates "v4 is a bit more work than v3" is off by an order of
magnitude. Operation count: ~22 procedures today versus ~40 operations for a
minimal 4.1, each on average heavier because of the attribute bitmap.

Per-worker briefs for M1 were written when this assessment was, and are
deliberately **not** kept here: they name decision records that do not exist, and
a ready-to-dispatch brief sitting in the tree invites someone to dispatch work
that this document recommends against. If the trigger condition below fires they
are cheap to rewrite, and by then they will need rewriting anyway, because the
files they name will have been replaced by the redesign.

### Recommendation

**Do not port to NFSv4.2 now. Stay on v3, finish the WAFL redesign, and revisit
4.1-with-4.2-operations afterwards — never 4.0.**

Five arguments, in order of weight:

1. **The ordering dependency is asymmetric, and that settles it.** `DESIGN.md`'s
   phases 1–6 replace `blobfs` wholesale and are unstarted. v4.2's best features
   are cheap *because of* the block tree the redesign introduces: CLONE is a
   reference splice over canonical span entries, SEEK is a descent skipping zero
   hashes, COPY is the snapshot mechanism one level down. On the proof of
   concept's flat chunk list they are approximations — chunk-granular holes, no
   snapshot sharing to build CLONE on. **Doing the redesign first makes v4
   cheaper; doing v4 first makes the redesign no cheaper at all, and leaves a
   6,000-line protocol layer written against a backend that is about to be
   replaced.**

2. **The client story does not reward it yet.** macOS — the platform this project
   chose NFS *for* — cannot speak 4.2 at all, and speaks 4.1 only on the newest
   release. A 4.2 port buys nothing on older macOS and none of the 4.2 operations
   on any macOS. Linux gains, and Linux is the platform the README records as
   not yet tested. The cheapest available win there is testing the v3 server that
   already exists.

3. **The cost is not in the operations, it is in the state.** Roughly 60% of the
   new code is clientid, session, stateid, lock, lease and grace machinery that
   delivers no new functionality to a single-writer loopback mount whose clients
   already lock locally. It is distributed lock recovery for one client on the
   same host.

4. **There is accepted, unimplemented v3 work in front of it.** ADR 0002
   (duplicate request cache) and ADR 0003 (write backpressure) were both Accepted
   and neither was in the code when this was written. A retransmitted REMOVE is
   re-executed, and a fast writer can exhaust memory. Starting v4 ahead of those
   is the wrong order, and actively wasteful: sessions supersede ADR 0002 for the
   v4 program, so doing v4 first strands that design.

5. **What v4 uniquely fixes is real but small** next to "commits are
   O(filesystem)" and "deleted chunks leak forever", which are the two things
   `DESIGN.md` exists to fix.

**Stated fairly, the argument for doing it anyway:** v4.2 is the only way this
filesystem can ever *express* what it is actually good at. A content-addressed,
copy-on-write, deduplicating store that cannot tell a client "this copy was free"
or "this region is a hole" is hiding its best properties behind a 1995 wire
protocol. CLONE and READ_PLUS are not incremental features here; they are the
protocol finally having vocabulary for the design. That argument is good — it is
just not *yet*, because the tree those operations would be cheap over does not
exist.

**The trigger condition, so this can be reopened on a fact rather than a mood:**
revisit when `DESIGN.md` phases 1–4 have landed **and** either Linux becomes a
first-class tested target or macOS 26+ becomes the supported floor. If both hold,
the right shape is 4.1 minimal-session read and write first and the 4.2
operations after — M1 → M2 → M3 → M4 — with M4 where the value is.

### The cheap insurance, which was bought

Both of these landed with this document, and neither commits the project to the
port:

- **The change counter is reserved in `DESIGN.md` section 4's inode record.** The
  256-byte layout was full and unbuilt; adding 8 bytes later would have cost a
  format version and dual-read support, which ADR 0001 identifies as the
  expensive kind of change.
- **The optional vfs interfaces exist, unimplemented**, so phase 2's file-tree
  work is done with hole extents and reference splicing in view.

What was deliberately *not* bought: the `vfs.Attr.Change` field, for the reasons
in section 4.

---

## Sources

- [RFC 7862 — NFSv4 Minor Version 2](https://www.rfc-editor.org/rfc/rfc7862.html)
  — §13's table of new operations and their OPTIONAL status with numbers 59–71;
  the section structure (§4 server-side copy, §6 sparse files, §7 space
  reservation, §9 labeled NFS, §12 new attributes, §15.13 CLONE); the
  `space_freed` definition ("taking block-sharing into consideration").
  *The HTML rendering reached §13; the `.txt` fetch truncated at §8, so §15's
  operation detail (`content4`, `sr_eof`, ALLOCATE's exact effect on size) was
  not obtainable and must be read directly before implementing.*
- [RFC 7863 — NFSv4.2 XDR](https://www.rfc-editor.org/rfc/rfc7863.txt) — the
  complete `nfs_opnum4` list; `change_attr_type4` and `data_content4` verbatim;
  `FATTR4_CLONE_BLKSIZE = 77`, `FATTR4_SPACE_FREED = 78`,
  `FATTR4_CHANGE_ATTR_TYPE = 79`, `FATTR4_SEC_LABEL = 80`.
- [RFC 8881 — NFSv4.1](https://www.rfc-editor.org/rfc/rfc8881.html) — "when the
  SEQUENCE operation is present, it MUST be the first operation in the COMPOUND
  procedure"; "The primary purpose of SEQUENCE is to carry the session
  identifier". *Every fetch truncated between §2.10 and §8; §17's
  REQUIRED/RECOMMENDED/OPTIONAL table and §8.4's grace-period text were not
  reachable. Everything attributed to those sections is marked
  [recalled, unverified].*
- [RFC 7530 — NFSv4.0](https://www.rfc-editor.org/rfc/rfc7530.txt) — §3.2 "the
  RPCSEC_GSS security flavor MUST be used to enable the mandatory-to-implement
  security mechanism"; §3.2.1 "NFSv4 clients and servers MUST support the
  Kerberos V5 security mechanism"; §3.2 "Other flavors, such as AUTH_NONE,
  AUTH_SYS, and AUTH_DH, MAY be implemented as well"; §3.1 TCP MUST, port 2049
  SHOULD; §4.1 no ancillary protocol for pathname-to-filehandle; §5.9 the
  numeric-string fallback for owner and owner_group.
- [RFC 8276 — Extended Attributes in NFSv4](https://www.rfc-editor.org/rfc/rfc8276.html)
  — GETXATTR 72, SETXATTR 73, LISTXATTRS 74, REMOVEXATTR 75; `xattr_support`
  attribute 82; `NFS4ERR_NOXATTR` 10095, `NFS4ERR_XATTR2BIG` 10096; no
  minor-version bump.
- [`nfs(5)`, man7.org](https://man7.org/linux/man-pages/man5/nfs.5.html) — v4 uses
  port 2049 without consulting rpcbind; `port=` honoured; `nolock`/`local_lock`
  are "Options for NFS versions 2 and 3 only"; default negotiation tries 4.2
  first.
- [macOS `mount_nfs(8)`](https://keith.github.io/xcode-man-pages/mount_nfs.8.html)
  — "Currently NFSv4 is the highest supported version with a minor version of
  zero or one."; "If no minor version is specified, zero is assumed.";
  "Specifying a non supported version or minor version will print a warning and
  ignore the `vers` or `nfsvers` option."; the `nocallback`, `namedattr` and
  `noacl`/`aclonly` options. Cross-checked against
  [ss64's copy](https://ss64.com/mac/mount_nfs.html), which reflects an older
  release ("currently only zero is supported").
- [torvalds/linux `fs/nfsd` contents](https://api.github.com/repos/torvalds/linux/contents/fs/nfsd)
  — the source-size figures used for the 10× v3-to-v4 scale check.
- [pynfs](https://linux-nfs.org/wiki/index.php/Pynfs) and
  [kofemann/pynfs `nfs4.1/`](https://github.com/kofemann/pynfs/tree/master/nfs4.1)
  — an NFSv4.0/4.1 conformance suite that speaks the protocol directly rather
  than through a kernel client.
- [Linux NFSv4.1 server notes](https://docs.kernel.org/filesystems/nfs/nfs41-server.html)
  — a real implementation's choices: sessions implemented, pNFS and file and
  directory delegations not.
- Third-party issue tracker, weak secondary evidence cited only for "these are
  the parts that bite":
  [current stateid not tracked across a COMPOUND](https://github.com/marmos91/dittofs/issues/2328),
  [EXCHANGE_ID/CREATE_SESSION/RECLAIM_COMPLETE validation gaps](https://github.com/marmos91/dittofs/issues/2340),
  [no delegation granted to pynfs](https://github.com/marmos91/dittofs/issues/2329).
  Not a specification, and not used to support any normative claim.

None of the fetched pages contained instructions or content attempting to
redirect this assessment; all were read as descriptions of external behaviour.
