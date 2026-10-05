# 0008. No size flag wraps

**Status:** Accepted
**Date:** 2026-10-02
**Revised:** 2026-10-05 — cross-references only. The bullet *A negative
`-commit-interval` (#73)* under *What this does not decide* now ends by saying
that ADR 0009 decides it. Nothing else changed.
**Revised:** 2026-10-05 — cross-references only, a second time. The bullet
*`-uid` and `-gid` (#72)* under *What this does not decide* now ends by saying
that ADR 0010 decides it. Nothing else changed.
**Issue:** #61

## Context

`run`, in `cmd/strata/main.go`, converts three size flags into the byte
counts `blobfs.Config` takes. `-max-dirty` goes through `maxDirtyBytes`
(ADR 0003 §1). The other two are converted inline, in the `Config` literal:

```go
ChunkSize:         uint32(*chunkKiB) * 1024,
CacheBytes:        int64(*cacheMiB) << 20,
```

All three flags are `flag.Int`, which parses its value with
`strconv.ParseInt(s, 0, strconv.IntSize)` and itself refuses a value that an
`int` cannot hold: `flag.Parse` reports, for example, `invalid value
"9223372036854775808" for flag -cache: value out of range`, prints the usage
text and exits with status 2 (*Sources*). So each conversion sees every `int`
from `math.MinInt` to `math.MaxInt`, and nothing else.

**`-chunk-size` wraps on every platform.** The conversion to `uint32` keeps
the count's low 32 bits, and the `uint32` product wraps modulo 2^32, so the
chunk size it produces is 1024 times the count's residue modulo 2^22, a
number from 0 to 2^22 − 1. §2's table shows what that made of the values at
the boundaries. `New` substitutes the default, 1 MiB, for 0, refuses anything
else below 4096 bytes, and accepts everything else.

The damage lasts when the bucket is new. For an existing filesystem,
`loadOrInit` adopts the chunk size the bucket holds, whatever the flag says
(ADR 0007 §1), and logs `adopting chunk size from existing filesystem` at
`Info`, with the wrapped value as `requested`. A new filesystem keeps its
chunk size for good, and since ADR 0007 its maximum file size is 2^20 times
that: `-chunk-size 4194308` makes 4 KiB chunks and a 4 GiB limit. And
`-chunk-size -1` makes chunks of 4 GiB less 1 KiB, so that every chunk index
written holds a buffer that large, because the write path allocates each
buffer at the full chunk size (ADR 0003 §2).

**`-cache` wraps where `int` is 64 bits.** The shift wraps modulo 2^64 once
the count is above 2^43 − 1 or below −2^43, which only a 64-bit `int`
reaches. `newChunkCache` takes a bound of zero or less as 256 MiB (ADR 0004
§2). So 2^43 MiB and `math.MaxInt` quietly become the 256 MiB default,
2^44 + 1 becomes 1 MiB, and −2^43 − 1 becomes a bound of 2^63 − 2^20 bytes
(§3's table).

`-max-dirty` had the same shape until ADR 0003 §1 gave it `maxDirtyBytes`,
which compares before it converts and gives a count too large to express in
bytes the meaning "no limit". #61 asks that the other two conversions refuse
a value that does not fit, or clamp it deliberately, deciding for each flag
because their meanings differ, and that tests cover the boundaries on 64-bit
in the style of the `-max-dirty` test. ADR 0007 lists #61 under *What this
does not decide*.

## Decision

### 1. No size flag wraps

Each size flag reaches its `blobfs.Config` field through a helper of its own,
in package `main` of `cmd/strata`, named for the bytes it returns:
`maxDirtyBytes` for `-max-dirty` (ADR 0003 §1), `chunkSizeBytes` for
`-chunk-size` (§2) and `cacheBytes` for `-cache` (§3). A helper returns the
flag's value in bytes, exactly, when the `Config` field can hold it.
Otherwise it returns the value that this ADR, or ADR 0003, gives the case, or
it refuses the value. None wraps. Each converts to `int64` before it compares
against a bound, so that the bound is a representable constant and the helper
behaves as specified where `int` is 32 bits. A size flag added later gets a
helper of its own under the same rule, pinned in an ADR.

### 2. `-chunk-size` is refused when no `uint32` can hold it

```go
// cmd/strata
//
// chunkSizeBytes converts -chunk-size, in KiB, to Config.ChunkSize.
func chunkSizeBytes(kib int) (uint32, error)
```

- For `0 <= kib <= math.MaxUint32>>10`, which is 4194303, or 2^22 − 1, it
  returns `uint32(kib) * 1024` and a nil error. The product is at most
  4294966272, so nothing wraps.
- For `kib < 0`, or `kib > 4194303`, it returns 0 and a non-nil error.
  4194304 KiB is 2^32 bytes, one more than a `uint32` holds.
- The error's text contains `-chunk-size` and the value as given, in decimal
  as `strconv.Itoa` writes it. It does not state the least chunk size `New`
  accepts, which is `New`'s to state (Assumption 4). Nothing else about the
  text is pinned. For example:

  ```text
  -chunk-size 4194308 is out of range: it cannot be negative or more than 4194303 KiB
  ```

`New` is unchanged, and does with the result what it does now: 0 selects the
default, 1 MiB; 1024, 2048 and 3072 bytes are below the least it accepts, and
it refuses them with its own error; anything else is the chunk size of a new
filesystem, while an existing filesystem keeps its own (ADR 0007 §1).

| `-chunk-size` (KiB) | `chunkSizeBytes` | Before: `uint32(kib) * 1024` | A new filesystem gets |
|---|---|---|---|
| `math.MinInt` | 0, an error | 0 | refused; was the default, 1 MiB |
| −1 | 0, an error | 4294966272 | refused; was chunks of 4 GiB less 1 KiB |
| 0 | 0, nil | 0 | the default, 1 MiB, as before |
| 1 to 3 | 1024 to 3072, nil | the same | refused by `New`, as before |
| 4 | 4096, nil | the same | 4 KiB chunks and a 4 GiB limit |
| 1024, the default | 1048576, nil | the same | 1 MiB chunks and a 1 TiB limit |
| 4194303 | 4294966272, nil | the same | chunks of 4 GiB less 1 KiB, the largest the flag gives |
| 4194304 | 0, an error | 0 | refused; was the default |
| 4194308 | 0, an error | 4096 | refused; was 4 KiB chunks and a 4 GiB limit |
| 2^32, where `int` is 64 bits | 0, an error | 0 | refused; was the default |
| 2^32 + 4, where `int` is 64 bits | 0, an error | 4096 | refused; was 4 KiB chunks |
| 2^32 + 1024, where `int` is 64 bits | 0, an error | 1048576 | refused; was 1 MiB chunks, the default size by accident |
| `math.MaxInt` | 0, an error | 4294966272 | refused; was chunks of 4 GiB less 1 KiB |

Where `int` is 32 bits, `math.MinInt` and `math.MaxInt` are −2^31 and
2^31 − 1, and every row that applies there reads the same.

A value that does not fit is refused rather than replaced (Assumption 1). A
chunk size is not a limit, so no value is large enough to mean "none", as a
`-max-dirty` too large to express does. Every substitute, a clamp to either
end or the default, is a chunk size the operator did not ask for, which a new
filesystem would keep for good, with the maximum file size that follows from
it (ADR 0007 §1), and which no later mount can change.

### 3. `-cache` takes the largest bound when its bound cannot be expressed

```go
// cmd/strata
//
// cacheBytes converts -cache, in MiB, to Config.CacheBytes.
func cacheBytes(mib int) int64
```

- For `mib <= 0` it returns 0, which selects the default, 256 MiB (ADR 0004
  §2; ADR 0003 Assumption 17 for `-cache 0`).
- For `int64(mib) > math.MaxInt64>>20`, which is 8796093022207, or 2^43 − 1,
  it returns `math.MaxInt64`.
- Otherwise it returns `int64(mib) << 20`.

A bound of `math.MaxInt64`, 2^63 − 1 bytes, is what this ADR calls
effectively unbounded: no host has that much memory, so the cache never
evicts.

| `-cache` (MiB) | `cacheBytes` | Before: `int64(mib) << 20` | The cache's bound |
|---|---|---|---|
| `math.MinInt` | 0 | 0, where `int` is 64 bits | 256 MiB, as before |
| −2^43 − 1, where `int` is 64 bits | 0 | 2^63 − 2^20 | 256 MiB; was effectively unbounded |
| −2^44 + 1, where `int` is 64 bits | 0 | 2^20 | 256 MiB; was 1 MiB |
| −1 | 0 | −2^20 | 256 MiB, as before |
| 0 | 0 | 0 | 256 MiB, as before |
| 1 | 2^20 | the same | 1 MiB |
| 256, the default | 2^28 | the same | 256 MiB |
| 2^43 − 1, where `int` is 64 bits | 2^63 − 2^20 | the same | the largest whole number of MiB |
| 2^43, where `int` is 64 bits | `math.MaxInt64` | −2^63 | effectively unbounded; was 256 MiB |
| 2^44 + 1, where `int` is 64 bits | `math.MaxInt64` | 2^20 | effectively unbounded; was 1 MiB |
| `math.MaxInt`, where `int` is 64 bits | `math.MaxInt64` | −2^20 | effectively unbounded; was 256 MiB |

Where `int` is 32 bits nothing wraps, before this ADR or after it:
`math.MinInt`, −2^31, gives 0 and the default, and `math.MaxInt`, 2^31 − 1,
converts exactly.

Why the top is clamped rather than refused, and why zero and below are the
default: Assumptions 6 and 7.

### 4. `-max-dirty` is unchanged, and how the three compare

`maxDirtyBytes` stays as ADR 0003 §1 pins it.

| | `maxDirtyBytes` (ADR 0003 §1) | `cacheBytes` (§3) | `chunkSizeBytes` (§2) |
|---|---|---|---|
| Returns | `int64` | `int64` | `uint32`, `error` |
| Zero | −1, no limit | 0, the default | 0, the default |
| Negative | −1, no limit | 0, the default | refused |
| Too large to express | −1, no limit | `math.MaxInt64` | refused |

ADR 0003 §1's pattern fits all three in what it does: one helper per flag,
converting to `int64` before comparing, and a decided meaning for a value too
large to express. It does not fit in two places. A `-cache` too large to
express cannot take the flag's zero meaning, as a `-max-dirty` too large
does, because `Config.CacheBytes`'s zero is the default, which is what #61
says a wrapped `-cache` quietly becomes. And a chunk size has no meaning for
"too large" at all, so `chunkSizeBytes` returns an error, which
`maxDirtyBytes` never needs.

### 5. Where `run` refuses, and what the operator sees

- `run` calls `chunkSizeBytes(*chunkKiB)` after the `-bucket` check and
  before `openStore`. If it returns an error, `run` returns that error
  unchanged, and `main` prints `strata: ` and its text on standard error and
  exits with status 1, as it does for any error `run` returns. The usage text
  is not printed.
- So a refused `-chunk-size` creates nothing, not even the directory
  `store.NewLocal` makes for a local bucket; it contacts no endpoint; and it
  stops `-check` as well.
- `run` builds `blobfs.Config` with `ChunkSize` from `chunkSizeBytes`,
  `CacheBytes: cacheBytes(*cacheMiB)`, and
  `MaxDirtyBytes: maxDirtyBytes(*maxDirty)` as before.
- The flags' names, defaults and help texts are unchanged. Nothing in
  `internal/` changes: not `blobfs.Config`, not `New`, not `vfs`.

### 6. The test surface

ADR 0003 §1 pinned `maxDirtyBytes` so that a clean-room test in package
`main` could call it. Following it, `chunkSizeBytes` and `cacheBytes`, with
the signatures in §2 and §3, are part of this ADR's interface, and renaming
either changes it. A test in package `main` of `cmd/strata` calls them
directly, and may rely on:

- every row of §2's and §3's tables;
- for `chunkSizeBytes`, that the error is non-nil exactly where §2 refuses,
  that the `uint32` is then 0, and that the error's text contains
  `-chunk-size` and `strconv.Itoa(kib)`;
- that neither helper does anything but compute its result: no I/O, no
  logging, no state.

The rows past 2^31 − 1 exist only where `int` is 64 bits. A test builds them
at run time from `int64` values, as the `-max-dirty` test does, so that it
still compiles where `int` is 32 bits. 4194303, 4194304 and 4194308 fit in
32 bits and apply everywhere.

What the helpers cannot show is left to review: that `run` refuses before it
opens the store, the exit status, and that no usage text is printed (§5).

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required, except where it says otherwise. Those the repository owner
approved say so (Assumption 13).

1. **`-chunk-size` is refused, not clamped.** #61 says an out-of-range chunk
   size "should probably be an error, not a clamp"; settling it was mine
   (§2). The repository owner approved it on 2026-10-02.
2. **A negative `-chunk-size` is refused, not taken as the default as 0 is.**
   No `uint32` holds it, nothing documents a meaning for it, and the old
   conversion made `-1` into chunks of 4 GiB less 1 KiB. The repository owner
   approved it on 2026-10-02, as part of refusing a `-chunk-size` out of
   range.
3. **`-chunk-size 0` still selects the default, 1 MiB.** It is not a wrap.
   `Config.ChunkSize`'s zero is the default, and `-cache 0` and
   `-snapshot-retention 0` mean their defaults too (ADR 0003 Assumption 17).
   Refusing it would break a command line that works today, for nothing #61
   asks. The repository owner approved it on 2026-10-02.
4. **1 to 3 KiB are left to `New`,** which owns the least chunk size it
   accepts (ADR 0007 §1's table). Refusing them in `cmd/strata` as well would
   put that number in two places, free to drift. The cost is today's: `New`'s
   error states bytes, `blobfs: chunk size 2048 is too small`, and it comes
   after `openStore`, which for a local bucket has already made the
   directory. The repository owner approved it on 2026-10-02. For the same
   reason, the error text of §2 does not state that number either.
5. **The upper bound is what a `uint32` holds,** 4194303 KiB, not a smaller
   cap. `New` accepts chunk sizes up to 2^32 − 1 bytes, and still does;
   whether it should is left open (*What this does not decide*). The
   repository owner approved it on 2026-10-02, as part of refusing a
   `-chunk-size` out of range.
6. **A `-cache` too large to express is `math.MaxInt64`, not refused.**
   `Config.CacheBytes` has no spelling for an unbounded cache, since zero or
   less selects the default (ADR 0004 §2), so `-max-dirty`'s answer, no
   limit, is not available. A bound of 2^63 − 1 bytes is, like one of
   2^43 − 1 MiB, which converts exactly, a bound no host reaches. Refusing
   would draw a line between two values that mean the same in practice, as
   ADR 0003 Assumption 16 argues for `-max-dirty`. Nor would refusing catch a
   typo: a mistyped `-cache` of 10^9 MiB is also beyond any host, and stays
   accepted. The repository owner approved it on 2026-10-02.
7. **A `-cache` of zero or less is 0, the default, and is not refused.** A
   negative one already gave the default, through `Config`'s zero or less,
   except below −2^43 MiB, where the shift wrapped. Returning 0 rather than
   the negative byte count closes that wrap and changes nothing else.
   Refusing negative values was weighed and not taken: an operator who writes
   `-cache -1` by analogy with `-max-dirty`, whose negative values mean no
   limit, and `-snapshot-retention`, whose `-1` keeps everything, still gets
   256 MiB without a word. I kept the behaviour because #61 is about wraps,
   and ADR 0003 Assumption 17 already records, without fixing, how
   differently the flags read zero and below. The repository owner approved
   it on 2026-10-02.
8. **A refusal is an error that `run` returns:** status 1, the `strata: `
   prefix, and no usage text. It is not a `flag.Value` or `flag.Func` that
   has `flag.Parse` refuse the value, which would print the usage text and
   exit with status 2, as for a value that does not parse (*Sources*). `run`
   already refuses a bucket spec without a bucket name, and missing
   credentials, this way, and keeping `flag.Int` leaves the usage text as it
   is. The repository owner approved it on 2026-10-02.
9. **The refusal comes before the store is opened,** so a bad `-chunk-size`
   is reported in every mode, `-check` included, which before ignored the
   flag. The repository owner approved it on 2026-10-02.
10. **The error's text is pinned only as far as a test and Assumption 4
    need.** It contains `-chunk-size` and the given value, so that a test can
    show the message identifies the mistake, and it does not state the least
    chunk size `New` accepts. Its wording is otherwise free.
11. **The help texts are unchanged.** Stating each flag's range there is left
    open.
12. **The change is breaking,** under `CLAUDE.md`'s rule for `CHANGES`: a
    deployment whose command line carries a negative or over-large
    `-chunk-size` no longer starts, even against an existing filesystem whose
    chunk size the flag never changed, and must correct the flag. The
    repository owner approved the breaking change on 2026-10-02.
13. **Status is Accepted on creation.** This is not a judgement of mine: on
    2026-10-02 the repository owner told the session to follow the
    recommendations of the planning pass behind this ADR, approving the
    design, namely that a `-chunk-size` out of range is refused, as a
    breaking change (Assumptions 1, 2, 5 and 12); that `-chunk-size 0`
    selects the default and 1 to 3 KiB are left to `New` (Assumptions 3 and
    4); that a `-cache` too large to express becomes `math.MaxInt64`
    (Assumption 6); that a negative `-cache` selects the default (Assumption
    7); that a refusal exits with status 1 and no usage text, before the
    store is opened (Assumptions 8 and 9); and this status.
14. **The figures are arithmetic,** worked from the code at `9ba50ae`, not
    measured. I cannot run the binary.
15. **Scope is #61.** Everything under *What this does not decide* is left
    alone on purpose.

## Alternatives considered

- **Clamping a `-chunk-size` that does not fit,** to 4194303 KiB or to
  4 KiB. A new filesystem would keep a chunk size, and a maximum file size,
  that nobody asked for (Assumption 1).
- **Taking a bad `-chunk-size` as the default.** The same, and it is what
  4194304 KiB did by accident.
- **Checking in `New`.** By then the `uint32` has wrapped, and `New` cannot
  tell 4 KiB asked for from 4194308 KiB wrapped.
- **Refusing a `-chunk-size` only when it would create a filesystem.** It
  needs the bucket's state before the flag is judged, and it leaves a command
  line that goes wrong the day it meets an empty bucket.
- **A `flag.Value` or `flag.Func` per flag,** so that `flag.Parse` refuses
  (Assumption 8).
- **`flag.Uint` for `-chunk-size`.** It refuses a negative value at parse
  time, since `strconv.ParseUint` permits no sign (*Sources*), but it still
  needs the upper bound, and the usage text would show the flag's type as
  `uint` instead of `int`.
- **Refusing a `-cache` too large to express** (Assumption 6).
- **Mapping a `-cache` too large to express to 0, `Config`'s default,** as
  `maxDirtyBytes` maps a `-max-dirty` too large to `Config`'s no limit. That
  is the quiet replacement #61 names.
- **Refusing a negative `-cache`** (Assumption 7).

## Consequences

- No size flag wraps. A new filesystem's chunk size is the one asked for, or
  the default for 0, and so is its maximum file size.
- A command line with a negative or over-large `-chunk-size` must be
  corrected, even against an existing filesystem (Assumption 12).
- A `-cache` above 2^43 − 1 MiB leaves the cache effectively unbounded, where
  it used to wrap to an arbitrary bound, and no negative `-cache` wraps to a
  positive bound.
- The `Info` record logged when an existing filesystem's chunk size is
  adopted reports, as `requested`, the size the operator gave, in bytes, or
  the default for 0, and never a wrapped one.
- A size flag added later is converted by a helper pinned in an ADR (§1).

## What this does not decide

- **A `-chunk-size` that differs from the bucket's.** `loadOrInit` adopts the
  bucket's and logs at `Info` (ADR 0007 §1). Warning, or failing, when
  `-chunk-size` was given and differs is a decision of its own. The flag's
  default, 1024, differs from every filesystem made with another chunk size,
  so either would have to tell a flag that was given from one left at its
  default (`flag.Visit`), and failing would stop mounts that work today.
- **An upper bound on the chunk size `New` accepts.** Near 4 GiB, each chunk
  index written holds a buffer that large (ADR 0003 §2), a read fetches whole
  chunks, and the chunk cache keeps none larger than `-cache` (ADR 0004 §2).
- **The meanings of zero and of negative values across the size flags,**
  which differ (ADR 0003 Assumption 17), and help texts that state each
  flag's range.
- **`-uid` and `-gid` (#72).** `run` converts a value of 0 or more with
  `uint32(...)`, which keeps the low 32 bits, so `-uid 4294967296` makes
  uid 0 the owner of a new filesystem's root directory. The shape is #61's,
  but neither is a size flag. ADR 0010 decides it: `cmd/strata` now refuses
  such a value before it opens the store.
- **A negative `-commit-interval` (#73).** `New` replaces only a zero
  interval, so a negative one reaches the committer that `FS.Run` starts on a
  writable mount, and `time.NewTicker` panics on a duration that is not
  positive (*Sources*). The panic, in the goroutine `run` starts for
  `FS.Run`, ends the process after it has opened its listener and printed
  the mount instructions. It is not a conversion. ADR 0009 decides it:
  `blobfs.New` refuses a negative `Config.CommitInterval` before it touches
  the store, and `cmd/strata` refuses a negative `-commit-interval` before it
  opens the store.

## Sources

Fetched on 2026-10-02 through a tool that summarises what it fetches, and
quoted as it returned them when asked for verbatim text. The `flag` source
and the `NewTicker` documentation were fetched twice that day, once while
this design was planned and once while this ADR was written, and the parts
fetched both times matched; the notes say what was fetched once.

- Go, package `flag`, source: <https://go.dev/src/flag/flag.go>.
  `intValue.Set` parses with `v, err := strconv.ParseInt(s, 0,
  strconv.IntSize)`, and `uintValue.Set` with `v, err :=
  strconv.ParseUint(s, 0, strconv.IntSize)`; each passes a parse error to
  `numError`, which returns `errRange`, declared as `var errRange =
  errors.New("value out of range")`, for `strconv.ErrRange`. `parseOne`
  reports a value that fails with `return false, f.failf("invalid value %q
  for flag -%s: %v", value, name, err)`, and `failf`'s body is
  `msg := f.sprintf(format, a...)`, `f.usage()`, `return errors.New(msg)`.
  Under `ExitOnError`, `Parse` calls `os.Exit(0)` for `ErrHelp` and
  `os.Exit(2)` otherwise. `UnquoteUsage` names a `*intValue` or
  `*int64Value` `int`, and a `*uintValue` or `*uint64Value` `uint`.
  `uintValue.Set`, `failf`'s body and `UnquoteUsage` were fetched once, while
  this ADR was written. (*Context*; Assumption 8; *Alternatives considered*)
- Go, `strconv.ParseUint`: <https://pkg.go.dev/strconv#ParseUint>.
  "ParseUint is like ParseInt but for unsigned numbers. A sign prefix is not
  permitted." Fetched once, while this ADR was written. (*Alternatives
  considered*)
- Go, `time.NewTicker`: <https://pkg.go.dev/time#NewTicker>. "The duration d
  must be greater than zero; if not, NewTicker will panic." (*What this does
  not decide*)

**Honesty note:** the Go specification's sections on conversions between
numeric types and on integer overflow could not be fetched. Three fetches,
from <https://go.dev/ref/spec>, from <https://tip.golang.org/ref/spec> and
from the specification's source in the Go repository,
<https://raw.githubusercontent.com/golang/go/master/doc/go_spec.html>, came
back cut off before them. So the wrap figures in *Context*, §2 and §3 rest
on arithmetic from the code, a conversion to `uint32` keeping the low 32 bits
and the `uint32` product and the `int64` shift wrapping modulo 2^32 and 2^64,
and not on a quotation of the specification.
