# Architecture decision records

An ADR records one decision that was expensive to make and would be expensive
to re-derive: why the design is the way it is, what else was considered, and
what the decision costs. `docs/DESIGN.md` says what the system *is*; an ADR
says why it is that and not something else.

A decision earns an ADR when it constrains work that has not happened yet — a
wire format, an on-disk layout, an interface other code will be written
against. A decision that only affects one function does not.

## Convention

- One file per decision, named `NNNN-kebab-case-title.md`, four digits,
  zero-padded, allocated in order. Numbers are never reused and never
  renumbered, because they are cited from the spec and from commit messages.
- ADRs are append-only in spirit. A decision that turns out wrong is not
  edited away: a new ADR supersedes it, and the old one's status becomes
  `Superseded by NNNN`. The record of having been wrong is part of the value.
- `Status` is one of `Proposed`, `Accepted`, `Rejected`, or
  `Superseded by NNNN`.
- The normative text lives in `docs/DESIGN.md`. An ADR may restate a format
  for readability, but where the two disagree the spec wins and the
  disagreement is a bug in one of them.
- Every assumption that was *not* handed to the author by an issue, the spec,
  or a prior ADR goes under **Assumptions**, including values picked without a
  requirement behind them. A reader must be able to tell which parts follow
  from a real constraint and which are the author's judgement, and push back on
  the judgement calls specifically.
- External sources are cited with a URL and with what was taken from them, so
  a reader can check the reading rather than trust it. Say plainly when the
  best available source was secondhand.

## Template

```markdown
# NNNN. Title in the imperative

**Status:** Proposed | Accepted | Rejected | Superseded by NNNN
**Date:** YYYY-MM-DD
**Issue:** #N

## Context
What forced a decision now, and what is constrained by it.

## Decision
What was decided, stated so it can be implemented against.

## Assumptions
Everything chosen without an explicit requirement, one per bullet.

## Alternatives considered
Each one, with why it lost.

## Consequences
What gets better, what gets worse, what is now hard to change.

## What this does not decide
The adjacent questions deliberately left open, and what would settle them.

## Sources
URL, and what was taken from it.
```

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](0001-file-trees-carry-chunk-spans.md) | File trees carry chunk spans | Accepted |
