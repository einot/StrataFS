# 0007. A file is at most 2^20 chunks long, and no call takes it further

**Status:** Accepted
**Date:** 2026-09-26
**Issue:** #55, #66

## Context

A regular file's contents are its chunk list, `inode.Chunks` in
`internal/blobfs/inode.go`. Entry `i` covers bytes `[i × cs, i × cs + Size)`,
`cs` being the chunk size, and an index past the end of the list reads as
zeros: `readRefs` records nothing for it, and `bufferWrite` starts it from
zeros. Position implies offset, so a chunk at index `k` needs `k` entries
before it, and a hole is one `chunkRef` per chunk. A file of size `S` can
therefore cost up to `⌈S / cs⌉` entries, whether it holds any data or not.

Each entry costs:

- **in memory**, 24 bytes for a hole, which is a string header and a `uint32`,
  and 88 bytes for a stored chunk, whose hash string holds 64 bytes more;
- **in every snapshot**, which `encodeSnapshot` writes as JSON on every
  commit: a hole is `{"s":1048576}` and a stored chunk
  `{"h":"<64 hex digits>","s":1048576}`, 14 and 85 bytes with the comma at
  1 MiB chunks, and 11 and 82 bytes at 4 KiB. `encoding/json`'s `Encoder`
  builds the whole document before the gzip writer sees any of it, and its
  `Decoder` reads a whole value before decoding it (*Sources*), so a commit
  holds the uncompressed snapshot in memory, holding `FS.mu` (#43), and a
  mount holds it too;
- **on every attribute read**, a step of `inode.used`, which walks the list.
  `FSStat` walks every file's list, and `bufferWrite` copies a file's whole
  list on every `Write` (ADR 0005, *What this does not decide*).

**#55: nothing bounds a file's size.** Two routes build a long list:

1. **A `SETATTR` of the size.** `truncate` appends a hole for every index up
   to the new size (`for len(n.Chunks) < lastIdx`), holding the file's
   `openFile.mu` and `FS.mu` for writing.
2. **A `WRITE` at a far offset.** It is admitted and charged one chunk like
   any other write (ADR 0003 §2, site 1), and the next flush of the file,
   whether the ticker's, a `COMMIT`'s, a stable write's or a writer's budget
   drain, fills the gap before it in `flushOpen`, holding `FS.mu` for writing.

With 1 MiB chunks, a size of 2^62 asks for 2^42 references, 96 TiB, while
every other call waits for `FS.mu`, until the process is killed for want of
memory and the namespace changes acknowledged since the last commit are lost. A
size of 2^45 makes 2^25 references, 768 MiB, which the next commit stores and
every later commit, drain and mount pays for. `FSINFO` advertises a
`maxfilesize` of 2^62 (`Server.fsinfo` in `internal/nfs/nfs3.go`), and
nothing enforces it. Any process that can reach the server's port can do
this, as any uid, because `AUTH_SYS` is believed.

**#66: a write that runs past 2^64 wraps.** The security audit of #65 found
that `bufferWrite` computes a position, `off + uint64(written)`, and the new
end of the file, `off + uint64(written)`, without checking either for
overflow, and that neither `Write` nor the NFS `WRITE` handler bounds `off`.
With 1 MiB chunks, a `WRITE` of 20 bytes at 2^64 − 10 puts its first 10 bytes
at index 2^44 − 1, wraps, and puts the other 10 over bytes 0 to 9 of the
file. The end wraps to 10, so the size does not grow, and the reply
acknowledges all 20 bytes. The next flush then meets index 2^44 − 1 and takes
route 2. The finding was filed as #66.

**`CREATE` carries a size too.** For `UNCHECKED` and `GUARDED`, `createhow3`
carries a `sattr3` (RFC 1813 §3.3.8). `blobfs`'s `Create` ignores `sa.Size`.
The NFS server applies an `UNCHECKED` create's size with a `SetAttr` once
`Create` has returned, and ignores that `SetAttr`'s error (`Server.create`),
so an `UNCHECKED` create reaches route 1 and answers success whatever
happened there. A `GUARDED` create's size is dropped.

ADR 0003, ADR 0005 and ADR 0006 each left #55 open (*What this does not
decide* in each). This ADR bounds a file, says what each call that could pass
the bound does instead, and makes `FSINFO` advertise what is enforced.

## Decision

### 1. The limit

`MaxFileSize = 2^20 × cs`, where `cs` is the chunk size the mount uses: for an
existing filesystem, the one `New` adopts from the bucket in `loadOrInit`,
whatever the configuration asked for; for a new one, the configured chunk
size.

| Chunk size | `MaxFileSize` |
|---|---|
| 4 KiB, the least `New` accepts | 2^32 bytes, 4 GiB |
| 64 KiB | 2^36 bytes, 64 GiB |
| 1 MiB, the default | 2^40 bytes, 1 TiB |
| 4 MiB | 2^42 bytes, 4 TiB |

The limit is inclusive: a file may be exactly `MaxFileSize` bytes long. It
cannot overflow, since the chunk size is a `uint32`. It is fixed for the life
of the mount, and nothing configures it: there is no flag and no `Config`
field for it.

The bound is on the length of a file's chunk list, stated as the logical size
it allows. In this format the two are one bound, because a size `S` needs up
to `⌈S / cs⌉` entries, holes included. A file that this build grows never has
more than 2^20 chunk references.

### 2. What a file at the limit costs, and why 2^20

Per file of 2^20 references, by arithmetic from the code rather than
measurement (Assumption 15):

| | All holes | All stored chunks |
|---|---|---|
| The chunk list in memory | 24 MiB | 88 MiB |
| Its snapshot JSON, at 1 MiB chunks | 14 MiB | 85 MiB |
| Its snapshot JSON, at 4 KiB chunks | 11 MiB | 82 MiB |
| `truncate`'s fill, or `flushOpen`'s gap fill | at most 2^20 appends, holding `FS.mu` for writing | — |
| `inode.used`, on every attribute read | 2^20 steps | 2^20 steps |
| `bufferWrite`, on every `Write` to the file | a 24 MiB copy | a 24 MiB copy |

A commit holds the snapshot JSON uncompressed while it encodes it, holding
`FS.mu`, and a mount holds it while it decodes it. A fill reallocates the list
as it grows, so it briefly holds more than the final list. Every memory figure
in this ADR counts a list's length: a list that grew by appends, as
`truncate`'s fill and `flushOpen`'s gap fill grow it, can keep a capacity
larger than its length, so what a file at the limit keeps resident can exceed
these figures, by an amount that has not been measured. For comparison,
`-max-dirty` and `-cache` each default to 256 MiB. One request's worst case is
24 MiB of references and 2^20 appends holding `FS.mu`: a `SETATTR` to the
limit, or the flush after a `WRITE` just below it.

Why 2^20:

- It is the least power of two for which every chunk size `New` accepts gives
  a limit of at least 2^32. RFC 1813 §3.3.19 says that `maxfilesize`
  "determines whether a server's particular file system uses 32 bit sizes and
  offsets or 64 bit file sizes and offsets", so no client takes this
  filesystem for a 32-bit one; and macOS's client, which reports large-file
  support for an NFSv3 mount only when `maxfilesize` is at least 2^32, still
  reports it (*Sources*).
- A 1 TiB file at the default chunk size is already more than the
  whole-namespace snapshot carries comfortably as data: 85 MiB of JSON a
  commit, for that one file.

### 3. The contract

`vfs.FS`, in `internal/vfs/vfs.go`, gains a method, and three of its methods
say what each does at the limit:

```go
// MaxFileSize is the largest size, in bytes, that a regular file may
// reach, which the NFS server advertises as FSINFO's maxfilesize (RFC
// 1813 §3.3.19). It is fixed for the life of the FS value.
//
// No method writes a byte at an offset at or past MaxFileSize, or sets a
// size above it; SetAttr, Create and Write say what each does instead,
// and a method added later that can extend a file must say the same. A
// file already larger, left by a build that allowed more, can still be
// read whole, written below the limit, shrunk to it, renamed and removed.
MaxFileSize() uint64
```

As rules, for every backend:

- **`SetAttr`.** A `sa.Size` above `MaxFileSize` fails with `ErrFBig`,
  whatever the file's current size, and none of `sa` is applied.
- **`Create`.** If `sa.Size` is set and above `MaxFileSize`, `Create` fails
  with `ErrFBig` before it creates or changes anything, whether or not `name`
  exists.
- **`Write`.** A write of at least one byte that starts at or past
  `MaxFileSize` fails with `ErrFBig` and changes nothing. One that starts below
  `MaxFileSize` and would run past it writes only the bytes below
  `MaxFileSize` and returns how many that was: a short write, which RFC 1813
  §3.3.7 permits, after which the client sends the rest in another `WRITE` and
  is refused. An empty write is not refused for its offset.
- **Any other method that can extend a file**, `Cloner.CloneRange` included
  once something implements it, follows the same rule.

### 4. Where `blobfs` checks, and in what order

- **`Write`.** `mutable()` first, as now. Then the empty write, which returns
  `(0, FILE_SYNC, nil)` at any offset, as now. Then, if `off >= MaxFileSize`,
  it returns `(0, 0, ErrFBig)`. Then, if `len(data) > MaxFileSize − off`, it
  keeps only `data[:MaxFileSize − off]`. Then everything `Write` does now, in
  its order: resolving the handle and its checks, the budget's wait,
  `getOpen`, `bufferWrite` and a stable write's sync. The check is a
  comparison and a subtraction, neither of which can wrap, and nothing
  computes `off + len(data)`. After it, `off + written <= MaxFileSize` inside
  `bufferWrite`, so its arithmetic cannot wrap either, which closes #66. Any
  other caller of `bufferWrite` must clamp the same way first.
- **`truncate`.** Its first statement fails a `size` above `MaxFileSize` with
  `ErrFBig`. `SetAttr` calls `truncate` after `mutable()` and before it
  changes any attribute, so it applies none of `sa`.
- **`Create`**, in `createEntry`, for a regular file only. After `mutable()`
  and the name check, and before it takes `FS.mu`, a `sa.Size` that is set and
  above `MaxFileSize` fails with `ErrFBig`. `Mkdir` and `Symlink` ignore
  `sa.Size`, as before.

A refused `vfs.FS` call, `SetAttr`, `Create` or `Write` on `blobfs`, takes no
lock, neither `FS.mu` nor any `openFile.mu`; adds no `FS.open` entry; neither
waits on the budget nor charges it; makes no store call; changes no size,
time, mode, chunk list, dirty buffer or pending trim; creates nothing; and,
for a stable write, syncs nothing. So `ErrFBig` comes after `ErrROFS` and a
diverged mount's `ErrStale`, and after a bad name's `ErrInval` or
`ErrNameTooLong`. It comes before a stale handle's `ErrStale`,
`ErrBadHandle`, `ErrIsDir`, `ErrAcces`, `ErrNotDir`, `ErrExist` and any wait.

That is a property of the `vfs.FS` call, not of an NFS request. The NFS
server's `SETATTR`, `WRITE` and `CREATE` handlers read attributes with
`GetAttr` before and after the call, the file's or, for `CREATE`, the
directory's, for their reply's `wcc_data`, and a `SETATTR` with a guard reads
them once more to check it. Each `GetAttr` takes `FS.mu` for reading and, for a
file, walks its chunk list (`inode.used`), so a refused request still waits
behind a commit that holds `FS.mu` (#43). That predates this ADR, and lets a
client do nothing that a plain `GETATTR` does not.

The fills in `truncate` and `flushOpen` are unchanged, and admission bounds
them: `truncate` appends at most 2^20 entries, and `flushOpen` fills at most
to index 2^20 − 1. Neither gets a check of its own (Assumption 10).

`New` computes the limit once the filesystem is loaded, from the chunk size
`loadOrInit` adopted, into `FS.maxFileSize` (§7). `MaxFileSize` returns it,
holding no lock.

### 5. The wire

`FSINFO`'s `maxfilesize` is `MaxFileSize()`. `internal/nfs` checks nothing
itself: `blobfs` is the one authority, and `statusOf` passes `ErrFBig`
through as `NFS3ERR_FBIG`, which each procedure answers with its usual error
body. RFC 1813 lists `NFS3ERR_FBIG` among the errors of `WRITE` only
(§3.3.7). `SETATTR` (§3.3.2) and `CREATE` (§3.3.8) return it too, as Linux's
server does, and nothing in their lists of errors means "too large"
(Assumption 5). A `CREATE` with mode `UNCHECKED` whose size is at or below the
limit is unchanged: the server applies the size with a `SetAttr` once
`Create` has returned.

**The encodings a wire test needs.** These are condensed from RFC 1813's XDR
(§2.4, §2.5, §2.6 and §3.3; *Sources*), keeping its names, types and field
order. They match `Server.getattr`, `setattr`, `lookup`, `read`, `write`,
`create` and `fsinfo` in `internal/nfs/nfs3.go`, and the encoders in
`internal/nfs/wire.go`.

- **Types.** `mode3`, `uid3`, `gid3` and `count3` are `uint32`; `size3`,
  `offset3` and `fileid3` are `uint64`; `ftype3`, `nfsstat3`, `time_how`,
  `stable_how` and `createmode3` are enums; `writeverf3` and `createverf3` are
  fixed-length opaques of 8 bytes (`NFS3_WRITEVERFSIZE` and
  `NFS3_CREATEVERFSIZE`, §2.4). A union is its discriminant followed by the arm
  it selects. The existing wire tests in `internal/nfs` write these with
  `internal/xdr`'s `Writer` and read them with its `Reader`: `Uint32` for a
  `uint32` or an enum, `Bool` for a `bool`, `Uint64` for a `uint64`, `Opaque`
  for `nfs_fh3` and `opaque data<>`, `String` for `filename3`, and `Fixed(8)`
  for a verifier.
- **Status.** Every reply body starts with an `nfsstat3`: `NFS3_OK` is 0,
  `NFS3ERR_NOENT` 2, and `NFS3ERR_FBIG` 27, "File too large. The operation
  would have caused a file to grow beyond the server's limit." (§2.6).
- **`FSINFO`'s limit.** In `FSINFO3resok`, `maxfilesize` is a `size3`, a
  `uint64`, after `obj_attributes` and the seven `uint32`s `rtmax` to
  `dtpref`.
- **A refused `SETATTR`, `WRITE` or `CREATE`** carries one `wcc_data` after its
  status, and nothing else: `SETATTR3resfail`'s `obj_wcc` and
  `WRITE3resfail`'s `file_wcc` describe the file, and `CREATE3resfail`'s
  `dir_wcc` describes the directory.
- **A short `WRITE`** is `NFS3_OK` followed by `WRITE3resok`: `file_wcc`, then
  `count`, a `uint32`, the number of bytes written, then `committed`, a
  `stable_how`, then `verf`, 8 bytes.
- **A size read back.** In `fattr3`, `size` is the `uint64` after the five
  `uint32`s `type` to `gid`. `GETATTR3resok` is a bare `fattr3`, and a
  `GETATTR` that fails carries nothing after its status. A `post_op_attr` is a
  `bool`, followed by an `fattr3` when it is true.
- **The end of a reply.** A test shows that a reply carries nothing after its
  last field with the `*xdr.Reader` that the existing wire tests'
  `rpcClient.call` returns: once that field is read, `Err()` is nil and
  `Remaining()`, which reports the bytes not yet decoded, is 0, since the
  server's reply record ends where the procedure's body does.

```
enum nfsstat3 { NFS3_OK = 0, NFS3ERR_NOENT = 2, NFS3ERR_FBIG = 27 /* , ... */ };

struct nfstime3  { uint32 seconds; uint32 nseconds; };
struct specdata3 { uint32 specdata1; uint32 specdata2; };
struct fattr3 {
    ftype3    type;
    mode3     mode;
    uint32    nlink;
    uid3      uid;
    gid3      gid;
    size3     size;
    size3     used;
    specdata3 rdev;
    uint64    fsid;
    fileid3   fileid;
    nfstime3  atime;
    nfstime3  mtime;
    nfstime3  ctime;
};
union post_op_attr switch (bool attributes_follow) {
case TRUE:  fattr3 attributes;
case FALSE: void;
};
struct wcc_attr { size3 size; nfstime3 mtime; nfstime3 ctime; };
union pre_op_attr switch (bool attributes_follow) {
case TRUE:  wcc_attr attributes;
case FALSE: void;
};
struct wcc_data { pre_op_attr before; post_op_attr after; };
union post_op_fh3 switch (bool handle_follows) {
case TRUE:  nfs_fh3 handle;
case FALSE: void;
};
struct nfs_fh3    { opaque data<NFS3_FHSIZE>; };    /* NFS3_FHSIZE is 64 */
struct diropargs3 { nfs_fh3 dir; filename3 name; };  /* filename3 is string<> */

enum time_how { DONT_CHANGE = 0, SET_TO_SERVER_TIME = 1, SET_TO_CLIENT_TIME = 2 };
union set_mode3 switch (bool set_it) { case TRUE: mode3 mode; default: void; };
union set_uid3  switch (bool set_it) { case TRUE: uid3 uid;   default: void; };
union set_gid3  switch (bool set_it) { case TRUE: gid3 gid;   default: void; };
union set_size3 switch (bool set_it) { case TRUE: size3 size; default: void; };
union set_atime switch (time_how set_it) {
case SET_TO_CLIENT_TIME: nfstime3 atime;
default:                 void;
};
union set_mtime switch (time_how set_it) {
case SET_TO_CLIENT_TIME: nfstime3 mtime;
default:                 void;
};
struct sattr3 {
    set_mode3 mode;
    set_uid3  uid;
    set_gid3  gid;
    set_size3 size;
    set_atime atime;
    set_mtime mtime;
};

/* GETATTR, procedure 1, §3.3.1 */
struct GETATTR3resok { fattr3 obj_attributes; };

/* SETATTR, procedure 2, §3.3.2 */
union sattrguard3 switch (bool check) {
case TRUE:  nfstime3 obj_ctime;
case FALSE: void;
};
struct SETATTR3args    { nfs_fh3 object; sattr3 new_attributes; sattrguard3 guard; };
struct SETATTR3resok   { wcc_data obj_wcc; };
struct SETATTR3resfail { wcc_data obj_wcc; };

/* LOOKUP, procedure 3, §3.3.3 */
struct LOOKUP3resfail { post_op_attr dir_attributes; };

/* READ, procedure 6, §3.3.6 */
struct READ3args  { nfs_fh3 file; offset3 offset; count3 count; };
struct READ3resok { post_op_attr file_attributes; count3 count; bool eof; opaque data<>; };

/* WRITE, procedure 7, §3.3.7 */
enum stable_how { UNSTABLE = 0, DATA_SYNC = 1, FILE_SYNC = 2 };
struct WRITE3args {
    nfs_fh3    file;
    offset3    offset;
    count3     count;
    stable_how stable;
    opaque     data<>;
};
struct WRITE3resok {
    wcc_data   file_wcc;
    count3     count;
    stable_how committed;
    writeverf3 verf;
};
struct WRITE3resfail { wcc_data file_wcc; };

/* CREATE, procedure 8, §3.3.8 */
enum createmode3 { UNCHECKED = 0, GUARDED = 1, EXCLUSIVE = 2 };
union createhow3 switch (createmode3 mode) {
case UNCHECKED:
case GUARDED:   sattr3 obj_attributes;
case EXCLUSIVE: createverf3 verf;
};
struct CREATE3args    { diropargs3 where; createhow3 how; };
struct CREATE3resok   { post_op_fh3 obj; post_op_attr obj_attributes; wcc_data dir_wcc; };
struct CREATE3resfail { wcc_data dir_wcc; };

/* FSINFO, procedure 19, §3.3.19 */
struct FSINFO3resok {
    post_op_attr obj_attributes;
    uint32       rtmax;
    uint32       rtpref;
    uint32       rtmult;
    uint32       wtmax;
    uint32       wtpref;
    uint32       wtmult;
    uint32       dtpref;
    size3        maxfilesize;
    nfstime3     time_delta;
    uint32       properties;
};
```

### 6. A file an earlier build left larger

Builds before this ADR accepted any size, so a bucket can hold a regular file
larger than `MaxFileSize`. Such a file:

- is mounted: `New` does not refuse the bucket, and logs the record below;
- is read whole by the server, at any offset below its size;
- is written as §4 says: a write at or past the limit is refused, even one
  inside the file; one that crosses the limit is short; one below it is
  written as before. None makes the file larger;
- can be shrunk to the limit, or below it, with `SetAttr`, which cuts its
  chunk list, while a size above the limit is refused, even one that would
  shrink it (Assumption 6);
- can be renamed and removed.

A Linux client takes the advertised limit as its `s_maxbytes` and does two
things with it (*Sources*). Its buffered read of such a file stops at the
limit, as though the file ended there, although `stat` reports the whole
size, so a copy made through the mount comes out short without an error. And
it refuses a write past the limit itself, before sending it. The bucket's
format is unchanged, so the earlier version still mounts the bucket and reads
such a file in full, which is how to copy one out.

**The record.** `New` logs one record when the filesystem it has loaded holds
at least one regular file whose size is above the `MaxFileSize` it computed,
and nothing otherwise:

| Level | Message | Attributes |
|---|---|---|
| `Warn` | `files larger than the maximum file size` | `count`, `largest`, `max_file_size` |

`count` is the number of such regular files (`slog.KindInt64`), `largest` the
largest such size, and `max_file_size` the `MaxFileSize` (both
`slog.KindUint64`). The attributes are top-level, not grouped. Directories and
symlinks are not counted. It is logged once per `New`, after the filesystem is
loaded.

### 7. The test surface

Following ADR 0002 §8 and ADR 0003 §4, one unexported name joins this
interface: `FS.maxFileSize`, a `uint64`. `New` sets it to 2^20 times the chunk
size it adopted, once the filesystem is loaded; `MaxFileSize` returns it;
every check of §4 compares against it; and nothing else in the package writes
it. A test in package `blobfs` may set it to any value after `New` returns and
before it first uses that `FS`, to stand for a build with another limit:
`math.MaxUint64` for the builds before this ADR, which had none, or a value
below the size of a file already in the bucket, for a file that an earlier
build left larger. §6's record compares against the value `New` set, before
any test changes it. Renaming the field changes this ADR's interface.

The names ADR 0003 §4 pins apply as well. A test may hold `FS.mu` for
writing, and a file's `openFile.mu` before it, in ADR 0003 §3's order, to show
that a refused call takes neither; and it may read `budget.waiters()`.

A test may rely on these behaviours:

- `MaxFileSize()` is 2^20 times the chunk size the mount uses, the adopted one
  for an existing filesystem (§1).
- A refused call returns `vfs.ErrFBig`, which `errors.Is` finds, and a refused
  `Write` returns `(0, 0, vfs.ErrFBig)`, a zero `vfs.Stability` being
  `vfs.Unstable` (§4).
- A refused `vfs.FS` call returns while another goroutine holds `FS.mu` for
  writing and the file's `openFile.mu`; it never parks in the budget; and
  afterwards the file's attributes, `DirtyBytes()`, the file's `FS.open` entry
  or its absence, the file's dirty buffers and pending trims, its contents, and
  what a fresh mount of the bucket finds are all as they were (§4).
- A refused stable write syncs nothing (§4).
- A write that crosses the limit returns the count of its bytes below it, and
  makes the file exactly `MaxFileSize` long if it was shorter; it is charged as
  any write of those bytes is (ADR 0003 §2) (§3, §4).
- An empty write succeeds at any offset (§3).
- `Mkdir` and `Symlink` accept a `sa.Size` of any value (§4).
- A read-only or diverged mount answers `ErrROFS` or `ErrStale` before
  `ErrFBig`, and a bad name answers `ErrInval` or `ErrNameTooLong` before it
  (§4).
- A file that an earlier build left larger behaves as §6 says, and §6's record
  is logged as it says.
- `FSINFO`'s `maxfilesize` is `MaxFileSize()`; a refused `SETATTR`, `WRITE` or
  `CREATE` is answered `NFS3ERR_FBIG` with the error body of §5; and a short
  `WRITE` is answered `NFS3_OK` with the count it wrote (§5).

How a test reaches each case:

- **A file above the limit, cheaply:** seed it through one mount, mount the
  bucket afresh, and lower that mount's `maxFileSize` below the file's size
  before its first use.
- **A file above the default limit**, for §6's record: a mount whose
  `maxFileSize` is `math.MaxUint64` grows a file to 2^20 × cs + 1 with
  `SetAttr` and `Sync`s, and the bucket is mounted afresh. That builds 2^20
  references, about 24 MiB, and a snapshot of about 11 MiB at 4 KiB chunks.
- **A refused call under held locks:** take the file's `openFile.mu`, if it has
  an `FS.open` entry, then `FS.mu` for writing, make the call in another
  goroutine, and wait for it under a bound.
- **A refused `Write` while a drain is held:** fill the budget and hold a drain
  in its chunk `Put`, as the existing tests' `bpBlockDrain` does, then make the
  call.
- **A read-only mount:** pass `blobfs.Config{ReadOnly: true}` to `New`, on a
  bucket that already holds a filesystem; such a mount answers every call that
  would change the filesystem, `SetAttr`, `Create` and `Write` among them, with
  `vfs.ErrROFS`, before `ErrFBig` (§4's order), whereas a store wrapped in
  `store.ReadOnly` refuses only the store's own writes, so on it those calls
  are accepted in memory and fail only when a commit reaches the store.

A build without these checks turns an over-limit request into a chunk list as
long as the request asks for. So a test reaches a clean failure with the
cheapest over-limit value, a size of `MaxFileSize + 1` or a write at
`MaxFileSize`, before it sends a larger one; it does not flush after a far
write that it has not confirmed was refused; and if a call that it made while
holding locks has not returned, it fails with the locks still held, so that
the call cannot go on to build the list. Such a build then fails the test
instead of exhausting memory.

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required, except where it says otherwise.

1. **2^20 chunk references a file, and so 1 TiB at the default chunk size.**
   No issue, spec or earlier ADR gave a number; #55 asks only for "a value the
   server can really handle". §2 gives the anchors: the least power of two
   that keeps every chunk size `New` accepts at or above 2^32, and a default
   that the whole-namespace snapshot can still carry. 2^21 would double every
   cost in §2 for no anchor, and 2^19 would put a 4 KiB filesystem's limit
   below 2^32.
2. **Derived from the chunk size, and not configurable.** A constant safe at
   4 KiB chunks, 4 GiB, would be needlessly small at the default, and one sized
   for the default, 1 TiB, is 2^28 references at 4 KiB chunks. A flag would let
   a deployment reopen #55 (*Alternatives considered*).
3. **Inclusive.** RFC 1813 calls `maxfilesize` "The maximum size of a file on
   the file system", and Linux's client, which takes it as its `s_maxbytes`,
   lets a file reach exactly that size (*Sources*).
4. **A `WRITE` that crosses the limit is short, not refused.** #55's done-when
   says such a write "is rejected". RFC 1813 §3.3.7 asks a server that writes
   fewer bytes than requested not to "return an error unless no data was
   written at all"; POSIX `write()` writes "only as many bytes as there is room
   for"; and Linux makes it "a short access" (*Sources*). No byte at or past the
   limit is ever accepted, and a write that starts there is refused with
   `NFS3ERR_FBIG`. The repository owner approved this reading on 2026-09-26.
5. **`NFS3ERR_FBIG` for `SETATTR` and `CREATE`**, although RFC 1813 lists it
   among the errors of `WRITE` only. Linux's server answers it for both, and
   the commit that made it so says the reference implementations are believed
   to as well (*Sources*). `NFS3ERR_INVAL`, which `SETATTR`'s list has, would
   tell the client that its request was malformed rather than too large.
6. **The check is stateless.** A size above the limit is refused even when it
   would shrink a file that an earlier build left larger. POSIX `ftruncate()`
   refuses a length "greater than the maximum file size" outright, where
   Linux's `inode_newsize_ok` checks the limit only when a size grows a file
   (*Sources*). A stateless check needs no lock and no view of the file, and
   such a file can still be shrunk to the limit.
7. **The checks come first** (§4). They read only the call's arguments and a
   value fixed at mount, so they follow only the checks that apply to the
   whole mount (`mutable()`) or to an argument alone (the name), and they
   refuse before anything is looked up or waited for. Linux's server likewise
   refuses a `WRITE` whose range it cannot represent before it copies the file
   handle (*Sources*). The cost: a request that is both too large and for a
   stale handle is answered `NFS3ERR_FBIG`.
8. **An empty write is never refused for its offset.** RFC 1813 §3.3.7 says
   that a `WRITE` whose count is 0 "will succeed and return a count of 0,
   barring errors due to permissions checking", and POSIX's [EFBIG] for a
   starting position past the offset maximum needs "nbyte is greater than 0"
   (*Sources*). `blobfs` already returns an empty write before any other
   check but `mutable()`.
9. **`Create` checks `sa.Size`, although it does not apply it**, and only for a
   regular file. The NFS server applies an `UNCHECKED` create's size with a
   `SetAttr` afterwards and ignores that call's error, so without a check in
   `Create` a create asking for a size above the limit would make the file and
   answer success. Checking in `Create` refuses it before anything exists,
   whichever layer applies the size. Directories and symlinks have no chunk
   list.
10. **No check inside the fills.** Admission bounds them (§4). A check there
    could only catch a hole in admission, and failing a flush there would fail
    every later flush of the file.
11. **A file that an earlier build left larger stays usable within the limit,
    and the advertised limit stays the one enforced** (§6), although a Linux
    client then reads such a file only up to the limit. Advertising the larger
    value would let the client accept writes that the server refuses
    (*Alternatives considered*). The repository owner approved this handling
    on 2026-09-26.
12. **The record at mount** (§6). Without it, a deployment learns of a file
    left larger only when a write past the limit fails, or a copy comes out
    short. It carries only numbers, and removing it would change nothing else
    here. The repository owner approved it on 2026-09-26.
13. **`FS.maxFileSize` is a seam for tests** (§7), as ADR 0003's `warnAfter`
    is. The alternative, a test that writes a snapshot into a bucket by hand,
    would pin the snapshot's JSON as a test surface.
14. **No format change.** A hole stays one entry per chunk, and `truncate`
    still appends a file's trailing holes (*What this does not decide*).
15. **The figures in §2 are arithmetic, not measurements.** I cannot run this
    server.
16. **Status is Accepted on creation.** This is not a judgement of mine: the
    repository owner approved the design on 2026-09-26, namely the limit of
    2^20 chunks, derived from the chunk size and not configurable; a crossing
    `WRITE` answered short; a breaking change, with files already above the
    limit handled as §6 says; the record at mount; and the bullet this change
    adds to "Honest limitations" in `README.md`.
17. **Scope is #55 and #66.** Everything under *What this does not decide* is
    left alone on purpose.
18. **The macOS evidence is the archived xnu source**, not the source of the
    current client (`apple-oss-distributions/NFS`), and whether macOS's
    client enforces the limit on its writes or reads was not established.
    Nothing in this ADR relies on a client enforcing it.

## Alternatives considered

- **A fixed logical size.** Keeping 2^62 is #55. A constant sized for the
  default, 1 TiB, is 2^28 references, about 6 GiB, at 4 KiB chunks, from one
  request; one sized for 4 KiB chunks, 4 GiB, is needlessly small at the
  default.
- **A flag or a `Config` field.** It would let a deployment reopen #55; the
  value that is safe follows from what a reference costs, not from
  preference; and two mounts of one bucket could disagree about which files
  are legal. A filesystem that needs larger files is created with a larger
  `-chunk-size`.
- **A sparse representation of holes**, such as a run of holes as one entry,
  or the list as a map from index to reference. It makes a large logical size
  cheap. But it changes the snapshot's format, which a build that predates it
  would misread, so it needs a format version, which is the repository owner's
  call; and the spans of `docs/DESIGN.md` §4, with its rules 4 to 6, are its
  natural home, as ADR 0006 found for a trimmed length.
- **Implicit trailing holes.** `truncate` could stop appending the holes after
  a file's last entry, since an index past the list already reads as zeros,
  and no format change is needed. That makes a `SETATTR` that grows a file
  free, but a `WRITE` past the list still fills the gap, so the limit this ADR
  needs is unchanged.
- **Refusing a crossing `WRITE` whole.** RFC 1813 §3.3.7 asks a server that
  writes fewer bytes not to "return an error unless no data was written at
  all", and POSIX and Linux write what fits (Assumption 4).
- **Checks in `internal/nfs`**, instead of or as well as in the backend. Two
  places would have to agree, and a caller of `vfs.FS` other than the NFS
  server would pass the first.
- **Checking against the file's current size, under its lock**, refusing only
  growth, as Linux's `inode_newsize_ok` does. It would let a file that an
  earlier build left larger be shrunk to a size still above the limit, and it
  needs `FS.mu` and the file's size for a decision the arguments already
  settle; POSIX `ftruncate()` refuses such a length outright (Assumption 6).
- **`NFS3ERR_INVAL` for `SETATTR` and `CREATE`**, which RFC 1813 lists for
  `SETATTR`. It would tell the client that its request was malformed rather
  than too large, and Linux's server answers `NFS3ERR_FBIG` (Assumption 5).
- **Advertising the larger of the limit and the largest file at mount**, so
  that a Linux client could read a file an earlier build left larger in full.
  The client would then accept writes that the server refuses, and the error
  would surface at writeback, `close()` or `fsync()` instead of at `write()`;
  and `FSINFO` would again promise more than is enforced (Assumption 11).
- **Refusing to mount a bucket that holds a file above the limit.** It would
  take every other file away with it.
- **A check inside the fills.** Admission already bounds them, a check there
  could only hide a hole in admission, and failing a flush there would fail
  every later flush of the file (Assumption 10).

## Consequences

- One request can no longer make the server hold more than 2^20 chunk
  references for one file. #55's exhaustion by one request is gone, and so is
  its lasting form for one request: no single request can leave more than one
  file at the limit in the bucket for every later commit, drain and mount to
  pay for. Many requests still can, one file each, and what they leave lasts
  across a restart as #55's did (*What this does not decide*).
- `FSINFO` advertises what is enforced.
- The `WRITE` wrap (#66) is closed: no write accepts a byte at or past
  `MaxFileSize`, and `bufferWrite`'s arithmetic cannot wrap.
- **The change is breaking.** A file larger than the limit can no longer be
  written past it or grown, and a Linux client reads it only up to the limit
  (§6). A filesystem created with a small chunk size gets a small limit:
  4 GiB at 4 KiB chunks.
- A Linux client refuses a write past the limit, or a size change that would
  grow a file past it, before sending it. Another client is answered
  `NFS3ERR_FBIG`, or a short count.
- Every backend of `vfs.FS` declares its maximum file size, the one that
  replaces `blobfs` included (`docs/DESIGN.md` §14).
- Many requests together can still exhaust memory (*What this does not
  decide*).

## What this does not decide

- **The total across files.** A bound per file is not a bound per mount, and
  one request makes a file at the limit: a `CREATE` with mode `UNCHECKED` and a
  size of `MaxFileSize`, which `Create` admits and the NFS server then applies
  with a `SetAttr`. `F` such files hold `F × 24 MiB` of holes. A commit, which
  encodes every inode holding `FS.mu`, holds about `F × 14 MiB` more of
  snapshot JSON at 1 MiB chunks while it does, about `F × 38 MiB` in all; so
  1,000 files, made by 1,000 requests, hold about 24 GiB, and about 38 GiB
  while a commit encodes them. Once a commit has stored them, the cost
  survives a restart: every mount decodes the snapshot and rebuilds the lists
  before it can serve any request, the one that would remove the files
  included. These figures are arithmetic, not measurements, and count each
  list's length, which may fall short of what the list keeps resident (§2).
  The options are a bound
  per mount on chunk references, refused with `NFS3ERR_NOSPC`; sparse holes;
  and the redesign. The server is loopback-only, and its clients are inside the
  trust boundary that ADR 0003 Assumption 19 records for the budget's
  overshoot.
- **Sparse holes, and implicit trailing holes** (*Alternatives considered*).
- **The redesign's maximum file size.** In `docs/DESIGN.md` §4 a hole of any
  span is one entry, so a file's logical size no longer sets its cost, and its
  limit is a decision of its own, which its backend declares through
  `MaxFileSize`.
- **`bufferWrite`'s copy of a file's whole chunk list** on every `Write`: 24 MiB
  a call for a file at the limit.
- **`CREATE`.** The NFS server ignores the error of the `SetAttr` that applies
  an `UNCHECKED` create's size, and drops a `GUARDED` create's size.
- **`SETATTR` with a size, and `WRITE`, on a symlink's handle.** Both check
  only that the handle is not a directory. This ADR bounds them as it bounds a
  regular file.
- **`inode.used` and `FSStat`**, which walk chunk lists on every attribute read
  and every `FSSTAT`.
- **#61.** Converting `-chunk-size` can wrap. The chunk size now sets the
  maximum file size as well, so a wrapped value sets a surprising one too:
  `-chunk-size 4194308` becomes 4 KiB chunks and a 4 GiB limit.
- **#64, #42 and #43.** The commit window (#64) is unchanged. The checks of §4
  run before `getOpen`, so they neither widen nor narrow #42, and before any
  lock, so a refused `vfs.FS` call never waits behind a commit that holds
  `FS.mu` (#43), though the NFS handler around it still does, when it reads
  attributes for its reply (§4); a commit still encodes every chunk list
  holding it, one at the limit included.

## Sources

Fetched on 2026-09-26 through a tool that summarises what it fetches, and
quoted as it returned them when asked for verbatim text. Most were fetched
twice that day, once while this design was planned and once while this ADR
was written, and the two fetches matched; the notes say which were fetched
once.

- RFC 1813, *NFS Version 3 Protocol Specification*, from the RFC Editor:
  <https://www.rfc-editor.org/rfc/rfc1813.txt>. §2.6, `NFS3ERR_FBIG = 27`:
  "File too large. The operation would have caused a file to grow beyond the
  server's limit." (§5)
- The same RFC as freesoft.org renders it, one section to a page (*Honesty
  note* below):
  - §2.4, <https://www.freesoft.org/CIE/RFC/1813/13.htm>: `NFS3_FHSIZE 64`,
    "The maximum size in bytes of the opaque file handle."; `NFS3_CREATEVERFSIZE
    8`; and `NFS3_WRITEVERFSIZE 8`, "The size in bytes of the opaque verifier
    used for asynchronous WRITE." (§5)
  - §2.5, <https://www.freesoft.org/CIE/RFC/1813/14.htm>: the XDR of every
    type in §5's block outside the procedures, and the line `NFS3ERR_FBIG = 27`
    in `nfsstat3`. (§5)
  - §2.6, <https://www.freesoft.org/CIE/RFC/1813/15.htm>: `NFS3ERR_FBIG`'s
    text, as above, and `NFS3ERR_NOENT`'s, "No such file or directory. The
    file or directory name specified does not exist." (§5)
  - §3.3.1 GETATTR, §3.3.3 LOOKUP and §3.3.6 READ,
    <https://www.freesoft.org/CIE/RFC/1813/21.htm>,
    <https://www.freesoft.org/CIE/RFC/1813/23.htm> and
    <https://www.freesoft.org/CIE/RFC/1813/26.htm>: the XDR of their arguments
    and results. (§5)
  - §3.3.2 SETATTR, <https://www.freesoft.org/CIE/RFC/1813/22.htm>: its XDR;
    "Servers must support extending the file size via SETATTR."; and its
    ERRORS, "NFS3ERR_PERM, NFS3ERR_IO, NFS3ERR_ACCES, NFS3ERR_INVAL,
    NFS3ERR_NOSPC, NFS3ERR_ROFS, NFS3ERR_DQUOT, NFS3ERR_NOT_SYNC,
    NFS3ERR_STALE, NFS3ERR_BADHANDLE, NFS3ERR_SERVERFAULT", which do not
    include `NFS3ERR_FBIG`. (§5; Assumption 5)
  - §3.3.7 WRITE, <https://www.freesoft.org/CIE/RFC/1813/27.htm>: its XDR; of
    the argument `count`, "If count is 0, the WRITE will succeed and return a
    count of 0, barring errors due to permissions checking."; of the result
    `count`, "The number of bytes of data written to the file. The server may
    write fewer bytes than requested. If so, the actual number of bytes written
    starting at location, offset, is returned."; from IMPLEMENTATION, "It is
    possible for the server to write fewer than count bytes of data. In this
    case, the server should not return an error unless no data was written at
    all. If the server writes less than count bytes, the client should issue
    another WRITE to write the remaining data."; and its ERRORS, "NFS3ERR_IO
    NFS3ERR_ACCES NFS3ERR_FBIG NFS3ERR_DQUOT NFS3ERR_NOSPC NFS3ERR_ROFS
    NFS3ERR_INVAL NFS3ERR_STALE NFS3ERR_BADHANDLE NFS3ERR_SERVERFAULT". (§3, §5;
    Assumptions 4 and 8)
  - §3.3.8 CREATE, <https://www.freesoft.org/CIE/RFC/1813/28.htm>: its XDR, and
    its ERRORS, "NFS3ERR_IO NFS3ERR_ACCES NFS3ERR_EXIST NFS3ERR_NOTDIR
    NFS3ERR_NOSPC NFS3ERR_ROFS NFS3ERR_NAMETOOLONG NFS3ERR_DQUOT NFS3ERR_STALE
    NFS3ERR_BADHANDLE NFS3ERR_NOTSUPP NFS3ERR_SERVERFAULT", which do not include
    `NFS3ERR_FBIG`. (*Context*, §5; Assumption 5)
  - §3.3.19 FSINFO, <https://www.freesoft.org/CIE/RFC/1813/39.htm>: its XDR; of
    `maxfilesize`, "The maximum size of a file on the file system."; and from
    IMPLEMENTATION, "The maxfilesize field determines whether a server's
    particular file system uses 32 bit sizes and offsets or 64 bit file sizes
    and offsets." (§2, §5; Assumption 3)

  **Honesty note:** every fetch of the RFC Editor's text, and of the
  datatracker's rendering at <https://datatracker.ietf.org/doc/html/rfc1813>,
  stopped inside §3.3.7, so only §2.6 is quoted from the RFC Editor. The rest
  of the RFC is quoted from freesoft.org's rendering, a mirror, and so is
  secondhand. Its §2.6 text for `NFS3ERR_FBIG` matched the RFC Editor's word
  for word. It renders the declaration of `FSINFO3args` as `struct
  FSINFOargs`, which nothing here depends on. Its pages for §2.4, §2.5, §2.6,
  §3.3.1, §3.3.3 and §3.3.6 were fetched once, while this ADR was written.
- Linux's NFS server, "[PATCH 5.16 015/203] NFSD: Fix NFSv3 SETATTR/CREATEs
  handling of large file sizes", the subject as the LKML archive gives it:
  <https://lkml.iu.edu/hypermail/linux/kernel/2202.1/07655.html>. "Note that
  RFC 1813 permits only the WRITE procedure to return NFS3ERR_FBIG. We believe
  that NFSv3 reference implementations also return NFS3ERR_FBIG when ia_size is
  too large." (§5; Assumption 5)
- Linux, <https://github.com/torvalds/linux>, branch `master`:
  - `fs/nfsd/nfs3proc.c`, `nfsd3_proc_write`: `resp->status = nfserr_fbig;`
    and `if (argp->offset > (u64)OFFSET_MAX || argp->offset + argp->len >
    (u64)OFFSET_MAX) return rpc_success;`, before `fh_copy(&resp->fh,
    &argp->fh);`. Fetched once, while this ADR was written. (Assumption 7)
  - `fs/nfs/internal.h`, `nfs_super_set_maxbytes`: `sb->s_maxbytes =
    (loff_t)maxfilesize;`, replaced by `MAX_LFS_FILESIZE` when that is larger
    or not positive. (§6; Assumption 3)
  - `fs/nfs/inode.c`, `nfs_setattr`: calls `inode_newsize_ok(inode,
    attr->ia_size)` for `ATTR_SIZE`. (§6; Consequences)
  - `fs/attr.c`, `inode_newsize_ok`: tests `offset >
    inode->i_sb->s_maxbytes` only inside `if (inode->i_size < offset)`.
    (Assumption 6)
  - `fs/read_write.c`, `generic_write_check_limits`: "If pos is under the
    limit it becomes a short access. If it exceeds the limit we return
    -EFBIG.", with `if (unlikely(pos >= max_size)) return -EFBIG;` and
    `*count = min(*count, max_size - pos);`. (§6; Assumptions 3 and 4)
  - `mm/filemap.c`, `filemap_read`: `if (unlikely(iocb->ki_pos >=
    inode->i_sb->s_maxbytes)) return 0;` and `iov_iter_truncate(iter,
    inode->i_sb->s_maxbytes - iocb->ki_pos);`. (§6; Assumption 11)

  **Honesty note:** `fs/read_write.c` and `mm/filemap.c` were fetched once,
  while this design was planned. Every fetch of either while this ADR was
  written was refused by the host (HTTP 429) or came back cut off before the
  function, so those two quotes were not checked a second time.
- POSIX.1-2024, `write()`:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/write.html>.
  DESCRIPTION: "only as many bytes as there is room for shall be written", and
  "For example, suppose there is space for 20 bytes more in a file before
  reaching a limit. A write of 512 bytes will return 20." ERRORS: [EFBIG]
  entries that end "and there was no room for any bytes to be written", and
  [EFBIG] "The file is a regular file, nbyte is greater than 0, and the
  starting position is greater than or equal to the offset maximum established
  in the open file description associated with fildes." (Assumptions 4 and 8)
- POSIX.1-2024, `ftruncate()`:
  <https://pubs.opengroup.org/onlinepubs/9799919799/functions/ftruncate.html>.
  ERRORS: "[EFBIG] or [EINVAL] The length argument is greater than the maximum
  file size." (Assumption 6)
- macOS's NFS client, as the archived xnu sources have it:
  <https://github.com/apple/darwin-xnu/blob/main/bsd/nfs/nfs_vfsops.c>.
  `nfs3_fsinfo` reads `maxfilesize` into `nfsa_maxfilesize` and sets
  `NFS_FATTR_MAXFILESIZE` in `nfsa_bitmap`. `nfs_vfs_getattr`, when that bit is
  set, sets `VOL_CAP_FMT_2TB_FILESIZE` only if `nfsa_maxfilesize >=
  0x100000000ULL`, under the comment "Is server's max file size at least
  4GB?", and sets it for any NFSv3 mount when the bit is not set. The bitmap
  detail was fetched once, while this ADR was written. (§2; Assumption 18)
- Go, `encoding/json`: <https://go.dev/src/encoding/json/stream.go>.
  `Encoder.Encode` calls `e.marshal(v, ...)` and only then `enc.w.Write(b)`;
  `Decoder.readValue` "reads a JSON value into dec.buf". (*Context*, §2)
