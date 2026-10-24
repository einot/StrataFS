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
  This governs the *decision*, not every sentence in the file. A restatement of
  something normative — a number, a field width, a format — that the spec has
  since changed is the "bug in one of them" the rule below names, and is
  corrected in place, with a `**Revised:** YYYY-MM-DD` line in the header
  recording what moved, why, and what did not. Superseding is for a decision
  that turned out wrong, not for a fact that moved underneath one.
- A later ADR may replace one part of an earlier decision and leave the rest
  standing. It says which part it replaces. The earlier ADR keeps its status,
  and its text stays as the record. It gains a `Revised` line, cross-references
  only, that names the part and the ADR that replaced it, and may gain one
  sentence in each section the part touches, pointing to the later ADR.
- [`exemptions.md`](exemptions.md) is a register, not an ADR. It lists every
  exemption from ADR 0018's rules that an ADR has granted, in force or
  revoked. The PR that carries an ADR granting or revoking one adds or marks
  its row there, as it adds the ADR's own row to the index below, and revises
  no ADR for it.
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
**Revised:** YYYY-MM-DD — what moved, why, what did not (omit if never revised)
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
| [0002](0002-duplicate-request-cache.md) | Duplicate request cache for non-idempotent NFS procedures | Accepted |
| [0003](0003-write-backpressure.md) | Bounding buffered writes, and the blocking discipline that makes it safe | Accepted |
| [0004](0004-chunk-cache-owns-its-bytes.md) | The chunk cache owns its bytes and charges what it holds | Accepted |
| [0005](0005-read-takes-one-view-under-the-file-lock.md) | A read takes one view of a file, under the file's lock | Accepted |
| [0006](0006-a-pending-trim-is-part-of-the-file.md) | A pending trim is part of the file, and is never buffered | Accepted |
| [0007](0007-maximum-file-size.md) | A file is at most 2^20 chunks long, and no call takes it further | Accepted |
| [0008](0008-no-size-flag-wraps.md) | No size flag wraps | Accepted |
| [0009](0009-a-negative-commit-interval-is-refused.md) | A negative commit interval is refused before anything starts | Accepted |
| [0010](0010-a-new-filesystem-is-never-owned-by-root-by-accident.md) | A new filesystem is never owned by root by accident | Accepted |
| [0011](0011-a-read-only-fs-writes-nothing-to-its-bucket.md) | A read-only FS writes nothing to its bucket | Accepted |
| [0012](0012-a-commit-stores-a-truncate-whole-or-not-at-all.md) | A commit stores a truncate whole or not at all | Accepted |
| [0013](0013-data-integrity-protocols-are-specified-in-tla.md) | Data-integrity protocols are specified in TLA+ and checked by TLC | Accepted |
| [0014](0014-state-machines-are-tables-and-permission-checks-are-policies.md) | State machines are explicit tables, and permission checks are policies | Accepted |
| [0015](0015-every-mount-command-carries-nosuid.md) | Every mount command carries nosuid and nodev | Accepted |
| [0016](0016-a-failed-accept-is-retried-or-ends-in-the-final-commit.md) | A failed Accept is retried or ends in the final commit | Accepted |
| [0017](0017-removing-a-directory-removes-its-inode.md) | Removing a directory removes its inode | Accepted |
| [0018](0018-state-machines-and-policies-are-a-standing-rule.md) | State machines and policies are a standing rule | Accepted |
| [0019](0019-a-prune-pass-deletes-only-below-its-own-commit.md) | A prune pass deletes only below its own commit's snapshot | Accepted |
| [0020](0020-a-mount-loads-the-root-pointer-and-its-etag-in-one-read.md) | A mount loads the root pointer and its ETag in one read | Accepted |
