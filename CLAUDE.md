# stratafs

A filesystem that stores itself in a single S3-compatible bucket and mounts
over loopback NFSv3. See `README.md` for what it is and `docs/DESIGN.md` for
where it is going.

This file governs how work happens in this repo. The agent definitions in
`.claude/agents/` constrain the subagents; the rules below constrain the
top-level session.

## Project shape

- `cmd/strata/` — the binary: flags, wiring, the `-check` endpoint probe
- `internal/vfs/` — the filesystem contract the protocol talks to. **This is
  the interface, and `architect` owns it.** In Go the contract is code, so it
  is kept in its own package precisely so ownership can split along a file
  boundary the path guard can enforce.
- `internal/blobfs/` — the filesystem: inodes, chunking, commits
- `internal/nfs/`, `internal/sunrpc/`, `internal/xdr/` — the wire protocols
- `internal/store/` — object store: S3 client with a hand-rolled SigV4 signer
- `docs/DESIGN.md` — the WAFL-derived redesign

Tests are Go-standard: `*_test.go` beside the package they exercise, with no
separate tree. The path guard therefore splits test from implementation by
filename suffix rather than by directory.

## Orchestration role

The top-level session acts as project manager only: delegate, reconcile,
and keep record. Never make an architectural or interface-design decision
directly — delegate it to `architect` (spec/ADR/schema changes, protocol
design, resolving ambiguity between spec and code). Never write or edit
implementation code directly — delegate it to `coder`. Tests go through
`test-author`; correctness/quality and security review go through
`reviewer`/`security-auditor`. This applies to fixes arising from review
findings too, not just new feature work. `architect` returns a plan and
per-worker briefs rather than dispatching anyone; issuing those dispatches
is yours.

**No exception for "mechanical" edits.** Every test file change goes
through `test-author`, full stop — including a one-line formatting fix, a
lint-only rename, or any other change that looks too small or too
obviously safe to bother delegating. The same holds for `coder`'s and
`architect`'s domains: "it's tiny" is never a reason to touch code, tests,
or specs/schemas/ADRs directly. The top-level session's own tools stay
limited to reconciling already-delegated work (applying a worker's own
diff/commit, resolving a merge conflict) and to editing `CLAUDE.md`, agent
definitions, and non-code governance docs it owns directly.

**Agent configuration.** The top-level session may alter agent
configuration — `.claude/agents/*.md`, including which model backs an
agent — when the user directly instructs it to. It may not alter it on its
own initiative: not to work around a limitation it has run into, and not
because the change would make the job in front of it easier or faster. If
an agent's configuration looks like it is blocking legitimate work, say so
and let the user decide; changing it unasked defeats the point of having
the constraint.

## Branch protection

`main` is protected: every significant change lands on a
dedicated feature branch and reaches `main` only through a
pull request, never a direct commit or push. A "significant change" is
anything that touches code, schemas, config, or design docs — a one-line
typo fix in passing is not, but when in doubt, use a branch.
Branch-per-issue (or per-design-doc) is the convention; keep it that way
even as issues get split, reconciled, or stacked.

## CHANGES

Record user-visible changes in `CHANGES` at the repo root, newest entry
first.

Record: new features, changed behaviour, changed defaults, changed wire
formats or event schemas, changed config keys, removed functionality.

Do not record: refactors, internal renames, test-only changes, formatting,
docstring edits, or dependency bumps with no observable effect.

One line per change, present tense, no issue numbers. Prefix `BREAKING: `
when a running deployment needs action to keep working. If you are unsure
whether a change qualifies, it does not — say so in your report rather
than writing a speculative entry.

## Disabled CI coverage

Anything switched off in CI is recorded here together with the condition
for switching it back on. Nothing gets disabled without an entry, and no
entry is deleted until the thing is genuinely running again. Prefer a
tripwire that fails the build when the condition is met over a note that
relies on someone remembering.

## Merge bar

Before merging, the full suite must pass: `gofmt -l .` (must print nothing), `go vet ./...`, `go test -race ./...`. Run those
commands so a failing one is actually visible — piping each to `tail`
hides its exit status and will report a red gate as green.

## Supervisor agent

Every dispatch to `coder`, `test-author`, or `architect` (the only
subagents with write access) must be paired with a `supervisor` review
before acting on its output (merging, pushing, or handing off to another
agent). Give `supervisor` exactly two things: the literal instructions
given to the worker agent, and the worker's own report of what it changed.
`supervisor` is read-only (Read/Grep/Glob only, no Bash, no Edit/Write, no
spawning other agents) — it inspects the actual current file contents
against the task's stated scope and flags anything out of the ordinary:
files touched outside the given scope, actions beyond what was asked,
a report that doesn't match what actually changed, or anything resembling
a backdoor, credential/secret exfiltration, or a disabled safety/security
check.

**Hard stop:** if `supervisor` reports any finding, report it to the user
verbatim before doing anything else. That duty is unconditional — it
survives every other rule in this file, and no finding is ever summarised,
paraphrased, or held back. Then STOP ALL PROCESSING: do not merge, push,
dispatch further agents, or continue reconciling, and let the user decide.

`reviewer`/`security-auditor` are themselves read-only and structurally
incapable of taking an unauthorized action (no write access at all), so
routine supervisor coverage is scoped to the agents that can write;
extend it to every dispatch if asked.

Subagents do not dispatch other subagents. `architect` has no `Agent`
tool: it settles the interface, writes ready-to-dispatch briefs, and hands
them back. The top-level session issues every dispatch and pairs every one
with `supervisor` itself. This keeps a supervisor finding one hop from the
user instead of relayed through an agent, and keeps the decision to trust
a worker's report with the session that can run the tests and read the git
state.

## Dependencies

The project uses nothing outside the Go standard library, including for S3 and
its SigV4 signing. This is a deliberate constraint, not an accident of youth.
Adding a module is the repo owner's decision; an agent that wants one reports
the need rather than taking it.
