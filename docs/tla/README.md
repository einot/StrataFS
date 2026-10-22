# docs/tla

The TLA+ specifications of the protocols that decide this filesystem's data
integrity, and the configurations TLC checks them with.
[ADR 0013](../adr/0013-data-integrity-protocols-are-specified-in-tla.md)
decides what is specified, how, and how the specs stay tied to the Go code.
This file says what the directory holds, how to read it and how to run it.

## What is here now

- **`CommitProtocol.tla`**: how a mount reads the root pointer, commits,
  prunes snapshots and diverges, against other mounts, failing store calls
  and crashes (ADR 0013 §1, item 1). It is checked against `043927f`.
- **Its 18 configurations**, `CommitProtocol.cfg` and `CommitProtocol_*.cfg`:
  six MustHold and twelve MustFail. Five of the MustFail ones each remove a
  fix the code has, and seven each show an open defect: #88 by three routes,
  #89, #91, and #5 by two. Each opens with a comment giving its expectation,
  its issue or the fix it removes, and the source of each `Fixes` member.
- **The three tables** (*Tables*): `trace.tsv`, `manifest.tsv` and
  `tools.tsv`. No row of `manifest.tsv` is calibrated yet (*Running*).
- **The runner**, `scripts/tla.sh`, outside this directory (*Running*).
- **The tripwire**, `internal/blobfs/tla_trace_test.go`, outside it too
  (*Tripwire*).

## What will be here

- **More modules**, in the order ADR 0013 §1 sets:
  2. `FileData.tla`: one file's bytes through writes, truncates, reads,
     flushes and commits (ADR 0005, ADR 0006, ADR 0012), for one mount, with
     no prune goroutine and no removal of the file.
  3. `NfsDurability.tla`: the write verifier, unstable and `FILE_SYNC`
     writes, `COMMIT`, the synchronous procedures and shutdown.
  4. `Budget.tla`: ADR 0003's write budget, specified for availability: its
     wait (§4), and its accounting (§2), with a file removed under it.

  The redesign's consistency point and sweeper (`docs/DESIGN.md` §5, §8)
  follow, each before its code. Every module is plain TLA+ in ASCII, with no
  PlusCal, and extends only `Naturals`, `FiniteSets`, `Sequences` and `TLC`
  (ADR 0013 §3).
- **Their configurations, and their rows** in the three tables.

## Reading a module

Each module opens with a header comment that states:

- its **scope**: what it models, and what it leaves out;
- **`Checked against`**, followed by a commit: the commit at which the module's
  trace was last checked against the Go code;
- one **`Revised:`** line per revision, dated, saying what moved and why, as an
  ADR's do;
- its **reductions**, which argue, as ADR 0013 §4 asks, why one action may
  stand for a run of Go steps, and its **assumptions**: the judgement calls
  the model rests on, which no requirement made.

`CommitProtocol.tla` has no `Revised:` line yet. Each action's comment names
the Go symbols that `trace.tsv` gives it.

Every module declares the constant `Fixes`, the set of fixes a configuration
includes, and its actions consult it, so one module describes the code with
and without each fix (ADR 0013 §7).

A spec is a model. `docs/DESIGN.md` outranks the ADRs, and the ADRs outrank
the specs; where a spec disagrees with either, the disagreement is a bug in one
of them (ADR 0013 §6). A spec shows nothing about the Go code on its own, and
nothing past its bounds: the link to the code is `trace.tsv`, and review
checks it (ADR 0013 §11).

A Go function that performs an action says so on a line of its own in its doc
comment, apart from its `Spec:` line, for example:

```go
// TLA+: docs/tla/CommitProtocol.tla PutSnapshot, SwapOK
```

## MustHold and MustFail

- **MustHold**: TLC checks the configuration and finds no violation.
- **MustFail**: the configuration checks `TypeOK` and one other property, and
  TLC must report that property violated. Every other outcome fails the gate:
  no violation, a violation of `TypeOK` or of anything else, or an error.
  A MustFail configuration either removes, from `Fixes`, a fix the code has,
  which shows that the fix is needed, or names an open defect's issue, which
  shows the defect in the model.

When a fix lands, its defect's configuration flips to MustHold, with the fix
added to its `Fixes`, and a MustFail configuration that removes the fix is
added. Configurations are never deleted or renamed, and the flip is the only
change an existing one takes (ADR 0013 §7).

## Counterexamples

A defect's counterexample becomes its Go regression test, which test-author
writes from the trace and the spec, never from the code. TLC writes nothing in
this directory or anywhere else in the tree, and test-author's read guard
refuses every path outside the tree it works in, so test-author cannot read
the trace where TLC left it. The session gets a counterexample with
`bash scripts/tla.sh show <config>`, which prints TLC's whole output, the
trace included, and pastes the trace into test-author's brief (ADR 0013 §8,
§9).

## Names

Every state, event, action, predicate, variable, property and `Fixes` member
in these modules is named in [`docs/vocabulary.md`](../vocabulary.md), which
also gives the Go constants and fields that share each name. A name that is
not there yet goes there first, by the rules ADR 0013 §5 sets.

## Running

TLC runs where the session runs it, since no agent that writes these files
has a shell (ADR 0013 §9). `bash scripts/tla.sh full` gates every PR that
changes this directory (ADR 0013 §10).

```sh
bash scripts/tla.sh full            # every row of manifest.tsv: the gate
bash scripts/tla.sh smoke           # only the rows whose tier is smoke
bash scripts/tla.sh tools           # only the checks of the tools
bash scripts/tla.sh show <config>   # one configuration, with TLC's whole output
```

**Prerequisites.**

- `bash`, and `shasum`, or else `sha256sum` and `sha1sum`, for the digests.
- The jar and the Java runtime that `tools.tsv` pins. `$TLA2TOOLS_JAR` names
  the jar and `$TLA_JAVA` the `java` binary; each one that is unset defaults
  to its row's `default`. The runtime installed under
  `$HOME/.local/share/tla/` is not on `PATH`, which is why `$TLA_JAVA`
  exists.
- Nothing is downloaded. A tool that is missing fails the run.

**What the runner checks and runs, in order.**

1. **The tools, first, in every mode**, before TLC runs:
   - the jar, from `$TLA2TOOLS_JAR` or else its `default`, must be an
     absolute path to a file that exists, and its size, SHA-256 and SHA-1
     must equal its row's `bytes`, `sha256` and `sha1`;
   - `java`, from `$TLA_JAVA` or else its `default`, must run, and report a
     `java.specification.version` whose major number is at least 11 (ADR 0013
     §9);
   - a tool that is missing or does not match fails the run, and is never
     skipped.
2. **The manifest**: `manifest.tsv` keeps the layout in *Tables*; every row's
   `docs/tla/<module>.tla` and `docs/tla/<config>.cfg` exist; and every
   `docs/tla/*.cfg` has exactly one row.
3. **Each row the mode runs**:
   - a row whose `exit` or `log` is `-` is uncalibrated, and fails without
     running TLC;
   - otherwise the runner copies `docs/tla/<module>.tla` and
     `docs/tla/<config>.cfg` into a fresh directory that `mktemp -d` makes
     outside the repository, and refuses one inside it. With that directory
     as its working directory, it runs the command below, with the output
     going to a log:

     ```sh
     "$java" -XX:+UseParallelGC -cp "$jar" tlc2.TLC -workers 1 \
       -metadir "<run>/states" -config <config>.cfg <module>.tla
     ```

   - the row **passes** when TLC's exit status equals the row's `exit` and
     the log has a line exactly equal to the row's `log`. That covers a
     MustHold row's clean run and a MustFail row's violation of its property,
     once calibration has confirmed what TLC prints for each. Every other
     outcome **fails** the row: a violation of `TypeOK` or of another
     property, a violation in the initial state, which TLC reports in a
     different line, a deadlock, an error, or no violation where one is
     expected.

`show <config>` checks the tools, then runs that one configuration as `full`
runs a row, whether or not the row is calibrated. It prints TLC's whole
output and then `tla.sh: TLC exited with status N`, and exits with TLC's
status. It judges nothing: it is how the session calibrates a row and gets a
counterexample.

**Outcomes.** The runner prints one `PASS` or `FAIL` line per row, and exits
with:

- `0` when everything it checked passed;
- `1` when a tool, the manifest or any row failed;
- `2` on a usage error.

**Where TLC writes.** TLC writes only under the runner's temporary directory:
its state files under `<run>/states`, and anything else it writes, such as a
trace, in its working directory, beside the copied module. The runner keeps
that directory only when a row failed, so that it can be read, and writes
nothing in the tree (ADR 0013 §9).

## Tables

Three tab-separated tables, whose layouts this section sets. These rules hold
for all three:

- ASCII only, with LF line endings and one tab between fields.
- The first line is exactly the header shown below. Any other line that
  begins with `#` is a comment.
- No blank line, and no empty field: a field with nothing to say holds `-`.
- Every line that is not a comment has as many fields as the header.

The tripwire rejects a `trace.tsv` that breaks them, and the runner a
`manifest.tsv` or a `tools.tsv` that does (*Tripwire*, *Running*).

### `trace.tsv`

Which Go symbol performs each action (ADR 0013 §6). Its header:

```text
#spec	action	file	symbol	locks	decided
```

| Field | Holds |
|---|---|
| `spec` | the module's name, such as `CommitProtocol` |
| `action` | an action of that module, by the name `docs/vocabulary.md` gives it: a disjunct of `Next`, an operator such an action conjoins (as `FallbackPut` and `StartPrune` are), or `Init` |
| `file` | the Go file: a slash path relative to the repository's root, ending in `.go`, with no `.`, `..` or empty element |
| `symbol` | `Name` for a function, or `Recv.Name` for a method `Name` on the type `Recv`, whether its receiver is a pointer or not and whatever type parameters the type has |
| `locks` | the locks held while the symbol performs the action |
| `decided` | what decides the behaviour: one or more of `ADR NNNN section N`, `README.md <heading>` and `docs/DESIGN.md section N`, separated by `; `, or `-` |

There is one row per action and symbol, since an action can span functions
and a function can perform several actions. Each row stands alone: no field
refers to another row. An action that no Go symbol performs, such as
`StrayDirty`, has no row. For actions `trace.tsv` is authoritative, and the
Go column of `docs/vocabulary.md` indicative (rule 7).

### `manifest.tsv`

Every configuration, and what the runner expects of it (ADR 0013 §7). Its
header:

```text
#module	config	tier	expect	property	exit	log	shows
```

| Field | Holds |
|---|---|
| `module` | the module's name |
| `config` | the configuration's name: the module's own, or the module's followed by `_` and more |
| `tier` | `smoke` or `full` |
| `expect` | `MustHold` or `MustFail` |
| `property` | `-` for MustHold; for MustFail, the one invariant it must violate |
| `exit` | TLC's exit status for the expected outcome, in digits, or `-` |
| `log` | the exact, whole line TLC prints for the expected outcome, or `-` |
| `shows` | free text: what the configuration shows, naming its issue or the fix it removes |

`exit` and `log` stay `-` until the session has confirmed them against the
installed TLC (ADR 0013 §7), and a row with `-` in either fails in `full` and
in `smoke`.

### `tools.tsv`

The pinned tools (ADR 0013 §9). Its header:

```text
#tool	version	env	default	bytes	sha256	sha1	verify	source
```

| Field | Holds |
|---|---|
| `tool` | the tool's name |
| `version` | its pinned version |
| `env` | the environment variable that names the installed tool, overriding `default` |
| `default` | where the tool is when `env` is unset: an absolute path, or one that begins with the literal `$HOME/`, the only expansion allowed |
| `bytes` | the size, in bytes, of the file the digests are of |
| `sha256`, `sha1` | that file's SHA-256 and SHA-1 in hex, or `-` where none is pinned |
| `verify` | `file` or `java` |
| `source` | the URL the file was downloaded from |

- `verify` `file`: the installed file must match `bytes`, and `sha256` and
  `sha1` where they are not `-`.
- `verify` `java`: the installed path must run, and its
  `java.specification.version` must have a major number of at least 11 (ADR
  0013 §9). Its `bytes` and digests are those of the archive that was
  downloaded, recorded as provenance, and the runner does not check them.

## Tripwire

`internal/blobfs/tla_trace_test.go` is a Go test that uses only the standard
library, and `go test ./...` runs it as it runs every other test (ADR 0013
§6, §10). It reads `docs/tla/trace.tsv`, and fails when the table breaks its
layout (*Tables*), or when a row's file is missing or no longer declares the
row's symbol. It checks presence only: that each symbol the trace names is
still in the file it names. Whether the function still performs the row's
action, holding the row's locks, is review's to check (ADR 0013 §8, §11).

## Sources

The configurations' syntax rests on TLC 1.7.4's own source, the version
`tools.tsv` pins. Each file was fetched on 2026-10-22, while the CommitProtocol
spec was written, through a tool that summarises what it fetches and reads a
long page 100,000 characters at a time.

- `tlc2/tool/impl/ModelConfig.java`, at
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/tlc2/tool/impl/ModelConfig.java>:
  `CHECK_DEADLOCK` takes `TRUE` or `FALSE` and nothing else; a constant's
  value may be a string in double quotes or a set in braces, the empty set
  `{}` included; and the file is read by
  `new TLAplusParserTokenManager(scs, 2)`, in lexical state 2.
- `tla2sany/parser/TLAplusParserTokenManager.java`, at
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/tla2sany/parser/TLAplusParserTokenManager.java>:
  its `lexStateNames` begin `"DEFAULT", "PRAGMA", "SPEC"`, so state 2 is
  `SPEC`.
- The grammar, `javacc/tla+.jj`, at
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/javacc/tla%2B.jj>:
  in state `SPEC`, `\*` begins a comment that ends with the line, and `(*`
  one that ends with its `*)`. Both are special tokens, which no production
  consumes, so a configuration's `\*` lines are comments to TLC.

**Honesty note:** the token manager's first 100,000 characters did not show
how comments are handled, so the second item comes from a second read, from
character 100,000. The grammar's lexical rules were in its first 100,000
characters, and the rest of it was not read.
