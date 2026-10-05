# 0009. A negative commit interval is refused before anything starts

**Status:** Accepted
**Date:** 2026-10-05
**Revised:** 2026-10-05 — cross-references only. The bullet *`-uid` and
`-gid` (#72)* under *What this does not decide* now says that ADR 0010
decides it. Nothing else changed.
**Issue:** #73

## Context

`run`, in `cmd/strata/main.go`, defines `-commit-interval` with
`flag.Duration`, a default of 5 s and the help text "how often to commit the
namespace" (`main.go:51`; every line number here is from `5834965`). The
`flag` package parses the value with `time.ParseDuration` and turns any error
into its `errParse`, which `flag.Parse` reports by printing the usage text and
exiting with status 2, as it does for a size flag that does not parse (ADR
0008, *Sources*). `time.ParseDuration` takes an optional sign, reads `0` as
zero, and refuses as an `invalid duration` a value that an `int64` count of
nanoseconds cannot hold (*Sources*). So `run` sees every `time.Duration` from
`math.MinInt64` to `math.MaxInt64` nanoseconds, and nothing else.

`run` passes the value unchanged as `blobfs.Config.CommitInterval`
(`main.go:113`). `New`, in `internal/blobfs/fs.go`, replaces an interval of
zero with 5 s and keeps any other in `FS.commitInterval` (`fs.go:187-189`,
`:211`). `FS.Run` starts the committer only on a writable mount; on a
read-only one it waits for its context to end and returns (`fs.go:260-266`).
The committer's first statement is `time.NewTicker(f.commitInterval)`
(`commit.go:335`), and `time.NewTicker` panics on a duration that is not
positive, before it makes the ticker (*Sources*). Nothing else reads the
interval.

So a negative `-commit-interval` does this today:

- **On a writable mount**, `run` has opened the store (`main.go:89`). `New`
  has loaded the filesystem, or, for an empty bucket, created one with the
  chunk size and owner the command line gave, and committed it
  (`commit.go:22-25`, `:80-103`). `run` has bound its listener (`main.go:131`)
  and printed the mount instructions (`main.go:138`). Then the goroutine that
  `run` starts for `FS.Run` (`main.go:140`) panics. Nothing recovers the
  panic, so the process exits with status 2 and a goroutine trace
  (*Sources*), not with `strata: `, an error and status 1, as it does for
  every error `run` returns.
- **On a read-only mount** it runs. The committer never starts, and the value
  is never used.
- **With `-check`** it is ignored: `run` returns before it calls `New`
  (`main.go:94-96`).

#73 says that a client may have mounted the server before the panic, and that
writes it acknowledged but had not yet committed are lost. The code bears out
less than that. The panic comes from the committer's first statement, not
from its first tick, and `run` starts `rpc.Serve` only after it has started
`FS.Run` (`main.go:143`), so the window is the delay before that one
goroutine first runs. And a write the server answers `UNSTABLE` stays dirty
at the client until a `COMMIT` flushes it or a stable `WRITE` writes it
again, and a reply that carries a write verifier the client did not expect
makes it send every such write again (RFC 1813 §3.3.7, §3.3.21;
`docs/DESIGN.md` §9). `New` draws a fresh verifier each time it runs
(`fs.go:223`), and a stable write is committed before it is answered
(`fs.go:1385-1389`). This ADR does not rest on the window being empty: it
removes it.

The interval is not a size, and it is not converted. `flag.Duration` yields
the type `Config` takes, so nothing wraps, and ADR 0008 §1's rule, which is
about converting a size flag to a byte count, does not reach it. ADR 0008
lists #73 under *What this does not decide*, and its §5 leaves
`blobfs.Config` and `New` unchanged. The defect is a value of the right type
that has no meaning.

## Decision

### 1. What `Config.CommitInterval` means

| `Config.CommitInterval` | `New` | `FS.commitInterval` |
|---|---|---|
| `time.Duration(math.MinInt64)` to −1 ns | refuses it (§2) | — |
| 0 | accepts it | 5 s, the default |
| 1 ns to `time.Duration(math.MaxInt64)` | accepts it | the value as given |

The table holds whether `Config.ReadOnly` is set or not. So every `FS` that
`New` returns has a positive `commitInterval`, and the ticker the committer
makes from it never panics. `Config.CommitInterval`'s doc comment states this
table.

### 2. `New` refuses a negative interval before it touches the store

```go
// internal/blobfs
func New(ctx context.Context, cfg Config) (*FS, error)
```

- If `cfg.CommitInterval < 0`, `New` returns a nil `*FS` and a non-nil
  error.
- It refuses before it makes any call on `cfg.Store`, of any `store.Store`
  method, `Name` included. So a refused `New` reads and writes nothing in the
  bucket, creates no filesystem in an empty one, builds no `FS`, and starts no
  goroutine.
- `cfg.ReadOnly` makes no difference.
- The error's text contains `commit interval` and the value as
  `time.Duration`'s `String` method writes it, `cfg.CommitInterval.String()`.
  Nothing else about the text is pinned. For example:

  ```text
  blobfs: commit interval -1s is negative
  ```

- Where `cfg` has more than one fault, among a missing store, a chunk size
  `New` refuses and a negative interval, which one `New` reports is not
  pinned.
- Nothing else in `New` changes.

### 3. `cmd/strata` refuses a negative `-commit-interval` before it opens the store

```go
// cmd/strata
//
// commitInterval checks -commit-interval for Config.CommitInterval.
func commitInterval(d time.Duration) (time.Duration, error)
```

- For `d >= 0` it returns `d` and a nil error. Zero passes through for `New`
  to take as the default.
- For `d < 0` it returns 0 and a non-nil error. The error's text contains
  `-commit-interval` and `d.String()`. Nothing else about the text is pinned.
  For example:

  ```text
  -commit-interval -1s is negative: give a positive duration, or 0 for the default
  ```

| `-commit-interval` | `commitInterval` | Before this ADR |
|---|---|---|
| `time.Duration(math.MinInt64)` | 0, an error | a writable mount crashed after printing its mount instructions; a read-only one ran; `-check` ignored it |
| −5 s | 0, an error | as for `math.MinInt64` |
| −1 s | 0, an error | as for `math.MinInt64` |
| −1 ns | 0, an error | as for `math.MinInt64` |
| 0 | 0, nil | the default, 5 s, as before |
| 1 ns | 1 ns, nil | the same, used as given |
| 5 s, the default | 5 s, nil | the same, used as given |
| 1 h | 1 h, nil | the same, used as given |
| `time.Duration(math.MaxInt64)` | `time.Duration(math.MaxInt64)`, nil | the same, used as given |

- `run` calls `commitInterval(*interval)` after `chunkSizeBytes` and before
  `openStore`. If it returns an error, `run` returns that error unchanged,
  and `main` prints `strata: ` and its text on standard error and exits with
  status 1, with no usage text, as ADR 0008 §5 does for `-chunk-size`. So a
  refused `-commit-interval` creates nothing, not even the directory
  `store.NewLocal` makes for a local bucket; it contacts no endpoint; and it
  stops `-check` and `-read-only` alike.
- `run` sets `Config.CommitInterval` from `commitInterval`'s result.
- The flag's name, type, default and help text are unchanged.

### 4. `FS.Run` and the committer are unchanged, and no floor is set

- On a writable mount `Run` runs the committer until its context is
  cancelled: every `FS.commitInterval` it commits whatever is dirty, and once
  the context is cancelled it makes one final commit attempt and returns. On a
  read-only mount it starts no committer, and returns once its context is
  cancelled. Neither changes.
- A positive interval is used as given, however small (Assumption 5).

### 5. The test surface

`commitInterval` (§3) and, in package `blobfs`, `New` (§2),
`Config.CommitInterval`, `FS.Run(ctx context.Context)` (§4) and
`FS.commitInterval` are this ADR's interface, and renaming any of them
changes it.

Following ADR 0007 §7, one unexported name joins the interface:
`FS.commitInterval`, a `time.Duration`. `New` sets it before it returns, to
the value in §1's last column, and nothing else writes it. A test in package
`blobfs` may read it once `New` has returned.

```go
// internal/blobfs, unexported
type FS struct {
	// ...
	commitInterval time.Duration
	// ...
}
```

So that a test can write a fake of the store, these are the `store.Store`
interface and `store.ObjectInfo`, restated from `internal/store/store.go` at
`5834965`, where they are normative. The signatures are copied exactly; the
doc comments are left out.

```go
// internal/store
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	GetRange(ctx context.Context, key string, off int64, n int64) ([]byte, error)
	Head(ctx context.Context, key string) (ObjectInfo, error)
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix, after string, max int) ([]ObjectInfo, error)
	PutIfMatch(ctx context.Context, key string, data []byte, etag string) (newETag string, err error)
	Name() string
}

type ObjectInfo struct {
	Key  string
	Size int64
	ETag string
}
```

A test in package `blobfs` may rely on:

- every row of §1's table: the refused one as the next bullet says, and the
  others read through `FS.commitInterval`, with `Config.ReadOnly` set and not;
- for every negative interval, with `Config.ReadOnly` set and not, over an
  empty bucket and over one that holds a filesystem: that `New` returns a nil
  `*FS` and a non-nil error whose text contains §2's two parts, and that it
  makes no call of any `store.Store` method on `cfg.Store`;
- that `Run`, on a writable mount and for every interval `New` accepts,
  returns once its context is cancelled, without panicking.

Two rules keep such a test safe against a build without §2:

- It never calls `Run` on an `FS` that `New` returned for a negative interval.
  On such a build `Run` panics at once, and a panic that nothing recovers, in
  any goroutine, ends the test binary. A test checks what `New` returned, and
  reports the case as failed instead.
- A test that calls `Run` at all calls it in a goroutine that recovers a panic
  and reports it.

A test does not rely on what `New` does with a read-only mount of an empty
bucket (#68), beyond §2's refusal, which comes before `New` looks at the
bucket. A test that needs `New` to accept a read-only mount uses a bucket that
already holds a filesystem.

A test in package `main` of `cmd/strata` calls `commitInterval` directly, and
may rely on:

- every row of §3's table;
- that the error is non-nil exactly where §3 refuses, that the
  `time.Duration` is then 0, and that the error's text contains
  `-commit-interval` and `d.String()`;
- that the helper does nothing but compute its result: no I/O, no logging, no
  state.

A `time.Duration` is an `int64` on every platform, so no row depends on the
width of `int`.

What the tests cannot show is left to review: that `run` calls
`commitInterval` before `openStore` and passes its result into `Config`, the
exit status, and that no usage text is printed (§3); and that the committer
is unchanged (§4).

## Assumptions

Each is a judgement call, not something an issue, the spec or an earlier ADR
required, except where it says otherwise. Those the repository owner
approved say so (Assumption 11).

1. **Both `New` and `cmd/strata` refuse.** #73 asks that a negative
   `-commit-interval` be refused before the store is opened or the server
   serves, and leaves `Config.CommitInterval` to me. The panic is the
   package's: `New` accepts a `Config` that its own `Run` cannot honour,
   whoever calls it, so `New` refuses (§2). `cmd/strata` refuses as well
   (§3), for what `New` cannot give an operator: a refusal before
   `openStore`, in every mode, with nothing created or contacted, as ADR 0008
   §5 does for `-chunk-size`, and a message that names the flag. ADR 0008
   Assumption 4 kept `New`'s least chunk size out of `cmd/strata`, because
   the number would live in two places, free to drift. That does not bite
   here. The bound is not a tunable number but the sign, which
   `time.NewTicker`'s precondition and `Config`'s zero meaning the default
   fix, so the two checks cannot drift apart unless a floor is set, which
   would be a decision of its own (*What this does not decide*). The
   repository owner approved it on 2026-10-05.
2. **Zero still selects the default, 5 s,** in `Config` and on the command
   line. It does not panic, `Config`'s zero is the default elsewhere (ADR
   0003 Assumption 17; ADR 0008 Assumption 3), and refusing it would break a
   command line that works today, for nothing #73 asks. The repository owner
   approved it on 2026-10-05.
3. **A negative interval is refused, not replaced.** Nothing documents a
   meaning for one. A clamp, to the default or to 1 ns, would quietly run a
   schedule nobody asked for, and reading a negative interval as "no periodic
   commit" would be a new feature, and the opposite of what a mistyped `-5s`
   meant.
4. **Read-only mounts are not exempt.** A read-only mount never starts the
   committer, so a negative interval does no harm there today. It is refused
   anyway, so that `Config.CommitInterval` has one meaning whatever
   `Config.ReadOnly` says, and so that a later read-only mode that does
   something periodic inherits no meaningless value. The cost is Assumption
   10. The repository owner approved it on 2026-10-05.
5. **No floor.** Any positive interval is used as given. A very small one
   does not panic: the ticker drops the ticks a slow receiver misses
   (*Sources*), so the committer runs `Sync` back to back, one at a time.
   That costs a CPU; `FS.mu`, taken for writing on every pass, even when
   nothing is dirty (`commit.go:129-133`); a snapshot of the whole namespace
   on every pass that has something to commit; and a chunk object for each
   state that a chunk being written passes through, which nothing ever
   deletes (`README.md`, *Honest limitations*). None of that is a crash or a
   loss, and a floor would be a number with no requirement behind it, which
   would refuse command lines that work today. The repository owner approved
   it on 2026-10-05.
6. **`commitInterval` returns the interval as well as the error,** as
   `chunkSizeBytes` returns the chunk size, so that `run` puts only a checked
   value into `Config`.
7. **The error texts are pinned only as far as a test needs:** the flag's or
   the field's name, and the value as `time.Duration`'s `String` method
   writes it. `flag.Duration` keeps only the parsed value, so what is
   reported can differ from what was typed: `-commit-interval -60s` is
   reported as `-1m0s`. Neither text states the default, which `New` owns.
8. **`run` judges `-chunk-size` before `-commit-interval`.** When both are
   bad, it reports the first only. The order is not part of the test surface.
9. **`FS.commitInterval` is a seam for tests** (§5), one they only read, as
   `FS.maxFileSize` is one they may set (ADR 0007 §7, Assumption 13). Without
   it a test could see what `New` made of zero only by waiting 5 s for a
   commit.
10. **The change is breaking,** under `CLAUDE.md`'s rule for `CHANGES`. A
    read-only deployment whose command line carries a negative
    `-commit-interval` runs today and will not start. A `-check` run that
    carries one, such as a script that probes an endpoint routinely, today
    ignores the flag and probes, and exits with status 0 unless the endpoint
    fails a check strata requires (`runCheck`, `cmd/strata/check.go`); it now
    exits with status 1 before it probes anything (§3), as a bad
    `-chunk-size` has done since ADR 0008 (Assumption 9). Both must correct
    the flag. A writable mount never got past its first instant, so it only
    fails sooner and more plainly. The repository owner approved the
    breaking change on 2026-10-05, for the read-only case; the `-check` case
    follows from the refusal in every mode that the owner approved with
    Assumption 1.
11. **Status is Accepted on creation.** This is not a judgement of mine: on
    2026-10-05 the repository owner answered the four questions that the
    planning pass behind this ADR put to them, choosing for each the option
    it recommended, namely that both `New` and `cmd/strata` refuse a negative
    interval (Assumption 1), that read-only mounts are not exempt, which
    makes the change breaking (Assumptions 4 and 10), that no floor is set
    (Assumption 5), and that zero stays the default (Assumption 2); and the
    session's dispatch for this ADR gave its status as Accepted on that
    basis.
12. **Read, not run.** Everything said here about today's behaviour is read
    from the code at `5834965`. Nothing was run, and #73's account of the
    harm was checked against the code (*Context*). The 292 years under *What
    this does not decide* is arithmetic.
13. **Scope is #73.** Everything under *What this does not decide* is left
    alone on purpose.

## Alternatives considered

- **Only `cmd/strata` refuses.** `Run`'s panic stays for every other caller
  of the package (Assumption 1).
- **Only `New` refuses.** One check rather than two, but it comes after
  `openStore`, which for a local bucket has made the directory already;
  `-check` never reaches it; and its message names the field, not the flag
  (Assumption 1).
- **Refusing only on a writable mount.** The change would not be breaking,
  but `Config.CommitInterval`'s meaning would depend on `Config.ReadOnly`,
  and a value with no meaning would stay accepted (Assumption 4).
- **Clamping a negative interval,** to the default or to 1 ns (Assumption
  3).
- **Reading a negative interval, or zero, as "no periodic commit"**
  (Assumptions 2 and 3).
- **A floor** (Assumption 5).
- **A `flag.Value` or `flag.Func` that has `flag.Parse` refuse the value.**
  It would print the usage text and exit with status 2 (ADR 0008 Assumption
  8).
- **Guarding the committer instead,** by skipping the ticker, or logging and
  returning, for an interval that is not positive. The mistake would surface
  only once the server is serving, and the mount would run without the
  commits it was configured for.
- **An exported validator in `blobfs` that `run` calls before `openStore`.**
  One rule with two callers, but exported API for one comparison, and a
  message that cannot name the flag.

## Consequences

- No `Config` that `New` accepts makes `Run` panic.
- A negative `-commit-interval` is reported at once, as `strata: ` and an
  error, with status 1, before anything is created or contacted, in every
  mode.
- A read-only command line that carries one must be corrected, and so must a
  `-check` command line that carries one, which used to ignore it and probe
  the endpoint, and now fails before it probes (Assumption 10).
- A writable one no longer prints the mount instructions and then crashes,
  and no longer creates a filesystem in an empty bucket first.
- A duration flag or `Config` field added later states, in its ADR, what zero
  and negative values mean and where a bad one is refused.

## What this does not decide

- **A floor, or a ceiling, on the interval** (Assumption 5).
- **A way to turn periodic commits off.** `math.MaxInt64` nanoseconds is
  about 292 years, and `time.NewTicker` accepts it, because `time` caps the
  deadline it computes for a timer at `math.MaxInt64` (*Sources*), so a very
  large interval already does that in effect.
- **Help text that states the flag's range, or that 0 selects the default,**
  as ADR 0008 Assumption 11 leaves the size flags' help texts.
- **`store.S3Config.Timeout`,** the one other `time.Duration` in a
  configuration struct. `NewS3` replaces zero with 30 s
  (`internal/store/s3.go:58-60`), and `http.Client` sets no deadline for a
  timeout that is not positive (*Sources*), so a negative one means no
  timeout. No flag reaches it: `openStore` never sets it (`main.go:223-229`).
- **`-uid` and `-gid` (#72).** ADR 0010 decides it.
- **`-read-only` on an empty bucket (#68).** `New` still creates and commits
  a filesystem there, since `Config.ReadOnly` refuses changes at the VFS
  layer only. §2's refusal comes first, so a read-only mount with a negative
  interval no longer writes, but nothing else about #68 changes.

## Sources

Fetched through a tool that summarises what it fetches, and quoted as it
returned them when asked for verbatim text: on 2026-10-04, while this design
was planned, and again on 2026-10-05, while this ADR was written. The parts
fetched both times matched. The notes say what was fetched once, and where
one fetch returned less than the other.

- Go, `time.NewTicker`: <https://pkg.go.dev/time#NewTicker>, and its source,
  <https://go.dev/src/time/tick.go>. "The ticker will adjust the time
  interval or drop ticks to make up for slow receivers. The duration d must
  be greater than zero; if not, NewTicker will panic." The body begins `if d
  <= 0 { panic("non-positive interval for NewTicker") }`, before it makes the
  ticker's channel, which holds one tick, and its timer. The doc comment is
  quoted from the source; pkg.go.dev, fetched once, on 2026-10-05, returned
  only its first sentence. (*Context*; §1; Assumption 5)
- Go, `when`, which computes a timer's deadline:
  <https://go.dev/src/time/sleep.go>. "If the returned value would be less
  than zero because of an overflow, MaxInt64 is returned." (*What this does
  not decide*)
- Go, package `flag`, source: <https://go.dev/src/flag/flag.go>.
  `durationValue.Set` begins `v, err := time.ParseDuration(s)` and `if err !=
  nil { err = errParse }`, with `var errParse = errors.New("parse error")`.
  `Duration`'s doc comment: "The flag accepts a value acceptable to
  time.ParseDuration." `UnquoteUsage` names a `*durationValue` `duration`.
  `parseOne`'s report of a value that fails, `failf` and `Parse`'s handling
  of `ExitOnError`, fetched once, on 2026-10-05, read as ADR 0008's *Sources*
  quotes them. (*Context*)
- Go, `time.ParseDuration`: <https://pkg.go.dev/time#ParseDuration>, and its
  source, <https://go.dev/src/time/format.go>. "A duration string is a
  possibly signed sequence of decimal numbers, each with optional fraction
  and a unit suffix". The source consumes one leading `-` or `+`, has `if s
  == "0" { return 0, nil }`, and returns `&parseDurationError{"invalid
  duration", orig}` from each overflow check. (*Context*)
- Go, `time.Duration.String`: <https://pkg.go.dev/time#Duration.String>, and
  its source, <https://go.dev/src/time/time.go>. "String returns a string
  representing the duration in the form "72h3m0.5s". Leading zero units are
  omitted. As a special case, durations less than one second format use a
  smaller unit (milli-, micro-, or nanoseconds) to ensure that the leading
  digit is non-zero. The zero duration formats as 0s." pkg.go.dev returned
  the whole comment on 2026-10-04 and only its first two sentences on
  2026-10-05, so it was checked against `time.go`, fetched once, on
  2026-10-05, which matched the first fetch. (§2, §3; Assumption 7)
- Go, the runtime: <https://go.dev/src/runtime/panic.go>. `fatalpanic`
  "implements an unrecoverable panic", and ends in `exit(2)`. (*Context*)
- Go, `net/http`: <https://go.dev/src/net/http/client.go>. `Client.Timeout`'s
  doc comment says "A Timeout of zero means no timeout.", and `deadline` is
  `if c.Timeout > 0 { return time.Now().Add(c.Timeout) }` then `return
  time.Time{}`. The 2026-10-04 fetch returned the comment without that
  sentence, and the 2026-10-05 fetch with it; `deadline` matched. (*What this
  does not decide*)
- RFC 1813, *NFS Version 3 Protocol Specification*, from the RFC Editor,
  <https://www.rfc-editor.org/rfc/rfc1813.txt>, and as freesoft.org renders
  it, one section to a page. §3.3.7,
  <https://www.freesoft.org/CIE/RFC/1813/27.htm>, on `verf`: "This cookie
  must be consistent during a single instance of the NFS version 3 protocol
  service and must be unique between instances of the NFS version 3 protocol
  server, where uncommitted data may be lost." §3.3.21,
  <https://www.freesoft.org/CIE/RFC/1813/41.htm>: "After a buffer is written
  with stable UNSTABLE, it must be considered as dirty by the client system
  until it is either flushed via a COMMIT operation or written via a WRITE
  operation with stable set to FILE_SYNC or DATA_SYNC." and "When a response
  comes back from either a WRITE or a COMMIT operation that contains an
  unexpected verf, the client will need to retransmit all of the buffers
  containing uncommitted cached data to the server." (*Context*)

  **Honesty note:** the RFC Editor's text, fetched once, on 2026-10-05, came
  back cut off inside §3.3.7, as ADR 0007 found, though after the
  description of `verf`, which matched freesoft.org's rendering word for
  word. §3.3.21 is quoted from freesoft.org's rendering alone, a mirror, and
  so is secondhand.
