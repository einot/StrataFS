# docs/tla

The TLA+ specifications of the protocols that decide this filesystem's data
integrity, and the configurations TLC checks them with.
[ADR 0013](../adr/0013-data-integrity-protocols-are-specified-in-tla.md)
decides what is specified, how, and how the specs stay tied to the Go code.
This file says what the directory holds and how to read it.

## What is here now

Only this file. No module, configuration, table or runner exists yet.

## What will be here

- **Modules**, one per protocol, in the order ADR 0013 §1 sets:
  1. `CommitProtocol.tla`: reading the root pointer, committing, pruning and
     divergence, against other mounts, failing store calls and crashes.
  2. `FileData.tla`: one file's bytes through writes, truncates, reads,
     flushes and commits (ADR 0005, ADR 0006, ADR 0012).
  3. `NfsDurability.tla`: the write verifier, unstable and `FILE_SYNC`
     writes, `COMMIT`, the synchronous procedures and shutdown.
  4. `Budget.tla`: ADR 0003 §4's write budget, specified for availability.

  The redesign's consistency point and sweeper (`docs/DESIGN.md` §5, §8)
  follow, each before its code. Every module is plain TLA+ in ASCII, with no
  PlusCal, and extends only `Naturals`, `FiniteSets`, `Sequences` and `TLC`
  (ADR 0013 §3).
- **Configurations**: TLC `.cfg` files, each for one module, and each either
  MustHold or MustFail (below).
- **Three tables**, which arrive with the first spec:
  - `manifest.tsv`: every configuration and whether it must hold or must
    fail, with the exit status and log text by which the runner tells the
    outcomes apart, added once the session has confirmed them against the
    installed TLC (ADR 0013 §7).
  - `trace.tsv`: which Go symbol performs each action, with the spec, the
    action, the Go file, the Go symbol, the locks held and the ADR section
    that decides it (ADR 0013 §6).
  - `tools.tsv`: the pinned tools, with their versions, sources, sizes and
    checksums (ADR 0013 §9).
- **The runner**, `scripts/tla.sh`, outside this directory, which also arrives
  with the first spec.

## Reading a module

Each module opens with a header comment that states:

- its **scope**: what it models, and what it leaves out;
- **`Checked against`**, followed by a commit: the commit at which the module's
  trace was last checked against the Go code;
- one **`Revised:`** line per revision, dated, saying what moved and why, as an
  ADR's do.

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
the trace where TLC left it. The session pastes the trace into test-author's
brief (ADR 0013 §8, §9).

## Names

Every state, event, action, predicate, variable, property and `Fixes` member
in these modules is named in [`docs/vocabulary.md`](../vocabulary.md), which
also gives the Go constants and fields that share each name. A name that is
not there yet goes there first, by the rules ADR 0013 §5 sets.

## Running

The running instructions arrive with the first spec, together with
`scripts/tla.sh` and `tools.tsv`. TLC runs where the session runs it, since no
agent that writes these files has a shell (ADR 0013 §9). Once `scripts/tla.sh`
has landed, `bash scripts/tla.sh full` gates every PR that changes this
directory (ADR 0013 §10).
