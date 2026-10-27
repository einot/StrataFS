# docs/tla

The TLA+ specifications of the protocols that decide this filesystem's data
integrity, and the configurations TLC checks them with.
[ADR 0013](../adr/0013-data-integrity-protocols-are-specified-in-tla.md)
decides what is specified, how, and how the specs stay tied to the Go code.
This file says what the directory holds, how to read it and how to run it.

## What is here now

- **`CommitProtocol.tla`**: how a mount reads the root pointer, commits,
  prunes snapshots and diverges, against other mounts, failing store calls
  and crashes (ADR 0013 §1, item 1). It is checked against `8415f61`.
- **Its 32 configurations**, `CommitProtocol.cfg` and `CommitProtocol_*.cfg`:
  nineteen MustHold and thirteen MustFail. Ten of the MustFail ones each
  remove a fix the code has, one of them,
  `CommitProtocol_UnconditionalTwoWriters`, stating besides what ADR 0021's
  opt-in gives up, and three each show an open defect, #5 by three. Ten of
  the MustHold ones showed a defect until its fix flipped them, eight #88
  (ADR 0019), one #89 (ADR 0020) and one #91 (ADR 0021), and keep the two
  invariants they had (*MustHold and MustFail*). Each
  opens with a comment giving its expectation, its issue or the fix it
  removes, and the source of each `Fixes` member. One,
  `CommitProtocol_PruneTwoWriters`, also sets a state constraint,
  `PassApartFromOwnCommits`, for the reason its comment and the module's
  Assumption J15 give, and `CommitProtocol_PruneOverlapTwoWriters` is its
  twin without one (*Configuration files*).
- **The three tables** (*Tables*): `trace.tsv`, `manifest.tsv` and
  `tools.tsv`. A row of `manifest.tsv` whose `exit` and `log` are `-` is not
  calibrated yet (*Running*).
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

`CommitProtocol.tla` has five `Revised:` lines: for ADR 0019's fix of #88,
for ADR 0020's fix of #89, two for ADR 0021's fix of #91, the second from
its review, and one for ADR 0014 §7's conversion of the mount mode. Each
action's comment names the Go symbols that `trace.tsv` gives it.

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
change an existing one takes (ADR 0013 §7). A defect that several
configurations show flips them all, and *Configuration files* sets the one
case the runner enforces.

## Configuration files

TLC reads a configuration with the TLA+ token manager, as a stream of
tokens, and the names after a keyword such as `CONSTRAINT` run to the next
keyword (*Sources*). So line breaks mean nothing to TLC, and a keyword could
stand inside another line. The runner therefore reads every `.cfg` that a
row names in this narrower layout, and refuses one that breaks it before TLC
runs (*Running*):

- ASCII printable characters only, with no tab. Every line ends in LF, the
  last one included.
- A line that begins `\*` is a comment, which TLC ignores too (*Sources*). No
  other line holds a backslash or `(*`.
- Every other line is a heading, at the start of the line, or an entry, which
  is two spaces and then the entry's text and belongs to the heading above
  it. The headings are exactly `SPECIFICATION <name>`, `CONSTANTS`,
  `CONSTRAINT`, `INVARIANTS`, `CHECK_DEADLOCK TRUE` and
  `CHECK_DEADLOCK FALSE`. A file has `SPECIFICATION` and `INVARIANTS` once
  each, and each of the other three keywords at most once. Only
  `CONSTANTS`, `CONSTRAINT` and `INVARIANTS` take entries.
- An entry under `CONSTANTS` is `<name> = <value>`; under `CONSTRAINT` or
  `INVARIANTS`, a name alone. No name appears twice under one heading.
- A name matches `^[A-Za-z][A-Za-z0-9_]*$`, and is not made of capital
  letters and underscores alone, that is, it does not match `^[A-Z_]+$`.
  That holds for `SPECIFICATION`'s name, every name under `CONSTANTS`, and
  every entry under `CONSTRAINT` and `INVARIANTS`.
- A value is a natural number in digits, a string of letters and digits in
  double quotes, or a set: `{}`, or `{`, then such numbers or strings
  separated by `, `, then `}`.

The rule on capitals keeps a keyword out of every place a name stands. TLC
1.7.4 has eighteen configuration keywords: `CONSTANT`, `CONSTANTS`,
`CONSTRAINT`, `CONSTRAINTS`, `ACTION_CONSTRAINT`, `ACTION_CONSTRAINTS`,
`INVARIANT`, `INVARIANTS`, `INIT`, `NEXT`, `VIEW`, `SYMMETRY`,
`SPECIFICATION`, `PROPERTY`, `PROPERTIES`, `TYPE`, `TYPE_CONSTRAINT` and
`CHECK_DEADLOCK` (*Sources*). Each is capital letters and underscores alone,
so an entry `CONSTRAINT` under `INVARIANTS`, which TLC would read as a
keyword and so as the start of a constraint, is refused. The runner refuses the shape rather than this list,
so that a keyword a later TLC adds the same way is refused too. The cost is
that a name such as `N` must be spelt otherwise. A name such as `I1` or `G1`,
which `docs/vocabulary.md` reserves, holds a digit and stands.

Three rules tie the configurations to `manifest.tsv`, and the runner enforces
all three:

- **A MustFail configuration** has exactly two entries under `INVARIANTS`:
  `TypeOK` and its row's `property`, in either order (ADR 0013 §7).
- **A MustHold configuration** has `TypeOK` and at least one other entry
  under `INVARIANTS`, so that none can pass by checking nothing but types.
  The runner asks no more, because a defect's configuration keeps its two
  invariants when it flips, and the flip may change nothing else (ADR 0013
  §7). A configuration written as MustHold lists every property its module
  defines, as the nine here do; review holds it to that, since the runner
  cannot tell the two kinds apart.
- **A MustHold configuration with a state constraint has a twin.** A
  constraint stops TLC taking a step from a state that breaks it
  (*Sources*), so it narrows what a configuration checks inside its bounds,
  which ADR 0013 §11 does not count among them. So a MustHold configuration
  that has a `CONSTRAINT` heading needs another MustHold configuration of the
  same module whose lines, comments aside, are its own lines with the
  `CONSTRAINT` heading and its entries taken out, in the same order. That
  compares the specification, the constants, the invariants and the deadlock
  check at once. `CommitProtocol_PruneTwoWriters` and
  `CommitProtocol_PruneOverlapTwoWriters` flipped together with ADR 0019,
  `PruneBelowCurrent` added to both, as this rule requires; the runner checks
  the pair on every run.

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

- `bash` 3.2 or later, `find`, and `shasum`, or else `sha256sum` and
  `sha1sum`, for the digests.
- The jar and the Java runtime that `tools.tsv` pins. `$TLA2TOOLS_JAR` names
  the jar and `$TLA_JAVA` the `java` binary; each one that is unset defaults
  to its row's `default`. The runtime installed under
  `$HOME/.local/share/tla/` is not on `PATH`, which is why `$TLA_JAVA`
  exists.
- Nothing is downloaded. A tool that is missing fails the run.

**What the runner checks and runs, in order.** `<root>` is the repository the
script sits in, found from the script's own path, and `<tmp>` the one
temporary directory a run writes in. Every refusal below exits with status 1
before TLC runs.

1. **Its environment, first, in every mode.** It sets `LC_ALL=C` and unsets
   `CDPATH`. It unsets every variable through which the caller's environment
   could change what `java`, the JVM or a helper does without changing which
   program runs: `JAVA_TOOL_OPTIONS`, `_JAVA_OPTIONS`, `JDK_JAVA_OPTIONS`,
   `CLASSPATH`, every variable whose name begins `DYLD_` or `LD_`,
   `BASH_ENV`, `PERL5OPT`, `PERL5LIB` and `PERLLIB`. It unsets `GLOBIGNORE`,
   turns globbing on, and turns `nullglob`, `failglob`, `dotglob`,
   `nocaseglob`, `nocasematch` and `extglob` off, so that `docs/tla/*.cfg`
   matches exactly the files there and every comparison is exact unless the
   runner itself asks otherwise.
2. **Its paths.** `docs/tla` must resolve, by `pwd -P`, to
   `<root>/docs/tla`; nothing under `docs/tla` may be a symbolic link; and
   every file the runner reads there must be a regular file. A name the
   runner prints from the directory itself, such as a link `find` reports or
   a `.cfg` with no row, has every character that is not printable ASCII
   shown as `?`.
3. **The temporary directory.** `mktemp -d` makes `<tmp>` under `$TMPDIR`,
   or else `/tmp`. The runner refuses, both before `mktemp` and after it, a
   directory that lies inside a guard: `<root>`, or any directory above
   `<root>` that holds an entry named `.git`, a dangling symbolic link of
   that name included, such as the main checkout that holds a linked
   worktree. It finds the guards by walking up from `<root>`, and runs no git
   command.
   - A directory lies inside a guard when it, or any directory on its path
     up to `/`, is the guard itself by device and inode, as bash's
     `test -ef` compares them. So a path that reaches a guard by another
     spelling is caught: a firmlink such as macOS's `/System/Volumes/Data`,
     a bind mount of a guard or of a directory above one, or a change of
     case. The runner also refuses, as it did before, a path that begins
     with a guard's path, ignoring case.
   - If it cannot examine a directory on either path, it refuses the run.
   - It also refuses a `<tmp>` whose path holds `:`.
   - Its limits:
     - a linked worktree that git placed outside its main checkout does not
       guard that checkout, since only git knows of it;
     - a bind mount of a directory inside a guard is not caught, since no
       directory on the mount's own path is the guard;
     - a network or FUSE mount of the tree, such as NFS, SMB or bindfs,
       reports another device, so it is not caught either.

     Each needs the caller both to have made such a worktree or mount and
     to choose a `$TMPDIR` that relies on it.
4. **The tools**, before TLC runs:
   - `tools.tsv` keeps the layout in *Tables*, which allows exactly two
     rows;
   - the jar, from `$TLA2TOOLS_JAR` or else its `default`, must be an
     absolute path, with no `:` in it, to a regular file. The runner copies
     it to `<tmp>/tla2tools.jar`, and the copy's size, SHA-256 and SHA-1
     must equal its row's `bytes`, `sha256` and `sha1`. TLC loads that copy
     and no other;
   - `java`, from `$TLA_JAVA` or else its `default`, must be an absolute
     path to an executable file, and, run with `<tmp>` as its working
     directory as `"$java" -XX:-UsePerfData -XshowSettings:properties
     -version`, must exit with status 0 and report a
     `java.specification.version` whose major number is at least 11 (ADR
     0013 §9);
   - a tool that is missing or does not match fails the run, and is never
     skipped.

   `tools` stops here.
5. **The manifest**: `manifest.tsv` keeps the layout in *Tables*, which
   ties each row's `exit` and `log` to its `expect` and `property`. For
   every row the runner makes `<run>`, which is `<tmp>/<config>`, and copies
   `docs/tla/<module>.tla` and `docs/tla/<config>.cfg` into it; each must be
   a regular file, and from then on the runner, and TLC, read only the
   copies, so TLC reads exactly what was checked. Every copied `.cfg` keeps
   the layout in *Configuration files* and meets its rules, and every
   `docs/tla/*.cfg` has exactly one row.
6. **Each row the mode runs**:
   - a row whose `exit` or `log` is `-` is uncalibrated, and fails without
     running TLC;
   - otherwise the runner makes the empty directories `<run>/home` and
     `<run>/tmp`, and, with `<run>` as its working directory, runs this
     command on the copies in it, with the output going to a log:

     ```sh
     "$java" -XX:+UseParallelGC -XX:-UsePerfData \
       -Duser.home="<run>/home" -Djava.io.tmpdir="<run>/tmp" \
       -cp "<tmp>/tla2tools.jar" tlc2.TLC -workers 1 \
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

`show <config>` makes steps 1 to 4, then step 5's checks of `manifest.tsv`
and, on its copies, of that one row's files and configuration, but not the
checks across rows: that every `.cfg` has a row, and that a constrained
MustHold configuration has its twin. It then runs that configuration as
step 6 runs a row, whether or not the row is calibrated. It prints TLC's
whole output and then `tla.sh: TLC exited with status N`, and exits with
TLC's status. It
judges nothing: it is how the session calibrates a row and gets a
counterexample.

**Outcomes.** The runner prints one `PASS` or `FAIL` line per row, and exits
with:

- `0` when everything it checked passed;
- `1` when a tool, the manifest, a configuration or any row failed, or the
  runner refused its environment, its paths or its temporary directory;
- `2` on a usage error.

**Where TLC writes.** Only under `<tmp>`. TLC writes its state files under
`<run>/states`; the standard modules it parses, which it copies out of the
jar into `java.io.tmpdir`, under `<run>/tmp`; whatever it would keep under
`user.home` under `<run>/home`; and anything else, such as a trace, in its
working directory, beside the copied module. The JVM writes a fatal-error
log, if it ever writes one, in its working directory, which is `<run>` for
TLC and `<tmp>` for the version probe, and `-XX:-UsePerfData` stops it making
its `hsperfdata_<user>` directory at all, wherever it would have put it
(*Sources*). The runner removes `<tmp>` when it exits, unless a row failed,
when it keeps it so that the logs can be read; it writes nothing in the tree
(ADR 0013 §9).

**What TLC reads, and the network.** The pinned jar holds TLC's opt-in
usage-statistics reporter, `util.ExecutionStatisticsCollector`, which takes
an identifier from the system property `util.ExecutionStatisticsCollector.id`
or from the file `.tlaplus/esc.txt` under `user.home`, and with one sends
statistics over HTTPS (*Sources*). The runner sets `user.home` itself, to
`<run>/home`, which is empty, rather than leave it to the account; passes no
such property; and in step 1 unsets the variables through which the
environment could pass one. So nothing TLC finds through `user.home` is the
caller's, and it sends nothing.

## Tables

Three tab-separated tables, whose layouts this section sets. These rules hold
for all three:

- ASCII only: printable characters, and the tab between fields. Every line
  ends in LF, the last one included, and no line holds a CR.
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
| `action` | an action of that module, by the name `docs/vocabulary.md` gives it: a disjunct of `Next`, an operator such an action conjoins (as `FallbackPut`, `StartPrune` and `Diverge` are), or `Init` |
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
| `property` | `-` for MustHold; for MustFail, the one invariant it must violate, which is never `TypeOK` |
| `exit` | TLC's exit status for the expected outcome, in digits, or `-` |
| `log` | the exact, whole line TLC prints for the expected outcome, or `-` |
| `shows` | free text: what the configuration shows, naming its issue or the fix it removes |

`exit` and `log` stay `-` until the session has confirmed them against the
installed TLC (ADR 0013 §7), and a row with `-` in either fails in `full` and
in `smoke`.

Each of the two that is not `-` must agree with the row's `expect` and
`property`, or the runner refuses the manifest:

- a MustHold row's `exit` is `0`, and its `log` is
  `Model checking completed. No error has been found.`;
- a MustFail row's `exit` is not `0`, and its `log` is
  `Error: Invariant <property> is violated.`, with the row's `property` in
  place of `<property>`.

Those are the lines TLC 1.7.4, which `tools.tsv` pins, prints for the two
outcomes, as the calibration of this directory's first eighteen rows
recorded them. A new pin that prints others changes this rule and the runner
with it.

### `tools.tsv`

The pinned tools (ADR 0013 §9). Its header:

```text
#tool	version	env	default	bytes	sha256	sha1	verify	source
```

| Field | Holds |
|---|---|
| `tool` | the tool's name: `tla2tools.jar` or `java` |
| `version` | its pinned version |
| `env` | the environment variable that names the installed tool, overriding `default` |
| `default` | where the tool is when `env` is unset: an absolute path, or one that begins with the literal `$HOME/`, the only expansion allowed |
| `bytes` | the size, in bytes, of the file the digests are of |
| `sha256`, `sha1` | that file's SHA-256 and SHA-1 in hex, or `-` where none is pinned |
| `verify` | `file` or `java` |
| `source` | the URL the file was downloaded from |

It has exactly two rows: `tla2tools.jar`, whose `verify` is `file`, and
`java`, whose `verify` is `java`. The runner refuses a row for any other
tool, a second row for either, and a table without both, since the path a
`java` row names is a program the runner runs.

- `verify` `file`: the copy of the installed file must match `bytes`, and
  `sha256` and `sha1` where they are not `-`.
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

The state constraint rests on two more facts from TLC 1.7.4's source, each
fetched on 2026-10-22, while the configurations were calibrated, through the
same tool:

- `ModelConfig.java`, at the URL above: `CONSTRAINT` and `CONSTRAINTS` are
  keywords of a configuration, and each name that follows one, up to the next
  keyword, is stored as a state constraint.
- `tlc2/tool/ModelChecker.java`, at
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/tlc2/tool/ModelChecker.java>:
  in `doNext`, a successor state that breaks a state constraint is still
  checked against the invariants, but is neither added to the set of states
  seen nor queued, so TLC takes no step from it.

**Honesty note:** the tool was asked to quote the lines that do these
things, and gave them as quotations inside its own summary; neither file was
read whole.

The list of keywords in *Configuration files* rests on two more files of TLC
1.7.4's source, each fetched on 2026-10-23, while the re-review's findings
were ruled on, through the same tool:

- `ModelConfig.java`, at the URL above: its 18 keyword constants, and
  `public final static String[] ALL_KEYWORDS = { Constant, Constants,
  Constraint, Constraints, ActionConstraint, ActionConstraints, Invariant,
  Invariants, Init, Next, View, Symmetry, Spec, Prop, Props, Type,
  TypeConstraint, CheckDeadlock };`. Seven are literals there:
  `"CONSTRAINT"`, `"CONSTRAINTS"`, `"VIEW"`, `"PROPERTIES"`, `"TYPE"`,
  `"TYPE_CONSTRAINT"` and `"CHECK_DEADLOCK"`. `ActionConstraints` and
  `Invariants` add `'S'` to `ActionConstraint` and `Invariant`, and the other
  nine come from `TLAConstants.KeyWords`.
- `util/TLAConstants.java`, at
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/util/TLAConstants.java>:
  in `KeyWords`, `ACTION_CONSTRAINT = "ACTION_CONSTRAINT"`,
  `CONSTANT = "CONSTANT"`, `CONSTANTS = CONSTANT + 'S'`, `INIT = "INIT"`,
  `INVARIANT = "INVARIANT"`, `NEXT = "NEXT"`, `PROPERTY = "PROPERTY"`,
  `SPECIFICATION = "SPECIFICATION"` and `SYMMETRY = "SYMMETRY"`.

**Honesty note:** the tool quoted the declarations and the array, but not the
constructor's table of keywords whole, of which it gave only the first
`this.configTbl.put(...)` line, and its own count of the literals, eight, does
not match the seven it quoted; the seven are what the lines show. The
session also checked the list against the pinned jar itself, by reading
`tlc2/tool/impl/ModelConfig.class`, which holds exactly these 18 keyword
strings and no others, `ALIAS` and `POSTCONDITION` among the absent. That a
keyword written where a name stands is still read as a keyword rests on the
token stream described above, not on a run.

What the runner does to keep TLC's writes and reads in `<tmp>` rests on four
more sources, each fetched on 2026-10-23, while the review's findings on the
runner were ruled on, through the same tool:

- `util/ExecutionStatisticsCollector.java`, at
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/util/ExecutionStatisticsCollector.java>:
  its `PATH` is `System.getProperty("user.home", "") + File.separator +
  ".tlaplus" + File.separator + "esc.txt"`, over two source lines;
  `getIdentifier()` begins `if (System.getProperty(PROP) != null) {` and
  `return System.getProperty(PROP);`, where
  `PROP = ExecutionStatisticsCollector.class.getName() + ".id"`; the
  statistics go to `"https://" + (isOptOut() ? "esc02" : "esc01") +
  ".tlapl.us/?" + encode(parameters)` through an `HttpURLConnection` whose
  method is `HEAD`; and the method that creates the file is
  `public void set(final Selection c)`.
- `util/SimpleFilenameToStream.java`, at
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/util/SimpleFilenameToStream.java>:
  a standard module is read from the jar by
  `cl.getResourceAsStream(STANDARD_MODULES + name)`, and `read` writes it to
  `new TLAFile(TMPDIR + File.separator + name, true, this)`.
- `util/FilenameToStream.java`, at
  <https://raw.githubusercontent.com/tlaplus/tlaplus/v1.7.4/tlatools/org.lamport.tlatools/src/util/FilenameToStream.java>:
  `static final String TMPDIR = System.getProperty("java.io.tmpdir");`.
- *The java Command*, JDK 21, at
  <https://docs.oracle.com/en/java/javase/21/docs/specs/man/java.html>: of
  `-XX:+UsePerfData`, "Disabling it suppresses the creation of the
  `hsperfdata_userid` directories. To disable the `perfdata` feature,
  specify `-XX:-UsePerfData`."; of `-XX:ErrorFile`, "By default, this file
  is created in the current working directory and named `hs_err_pid`*pid*`.log`",
  and where it cannot be created there, "the file is created in the
  temporary directory for the operating system", which is `/tmp` on every
  system but Windows.

**Honesty note:** the tool quoted the lines it was asked for, but declined to
quote `getIdentifier()` whole, which it summarised: the property's value
wins, and otherwise the file's first line, unless the file is missing, empty
or reads `NO_STATISTICS`. `isOptOut()` was not read whole either. That TLC
runs the reporter is the session's static reading of the pinned jar, not
these files: the session found that the class reads that file and, when it
is there, sends statistics over HTTPS with `HttpURLConnection`. The java page
was read in its first 100,000 of 170,631 characters, which hold both
entries.
