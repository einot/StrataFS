---
name: agent-kit
description: Bootstrap a delegation-based multi-agent orchestration system in a project — architect, coder, test-author, reviewer, security-auditor and supervisor, the shared path-guard hook, and the CLAUDE.md governance rules that make the whole thing hold together. Use when starting a new development project that should be built by delegated agents rather than by the session directly, or when auditing an existing setup against the reference design.
---

# agent-kit

Six agents, one hook, and a page of governance rules. Together they make a
top-level session into a project manager that delegates all work and
verifies what comes back, instead of an editor that does everything
itself.

Each agent also ships as its own single-file skill, so you can install or
repair one without the rest:

| Skill | Agent | Writes? |
| --- | --- | --- |
| `agent-kit-architect` | `architect` | docs + schemas only |
| `agent-kit-coder` | `coder` | implementation only, in a worktree |
| `agent-kit-test-author` | `test-author` | tests only, cannot read the code |
| `agent-kit-reviewer` | `reviewer` | no |
| `agent-kit-security-auditor` | `security-auditor` | no |
| `agent-kit-supervisor` | `supervisor` | no |

## The design in one page

**Separation of write scope.** Exactly one agent may write each kind of
artifact: interfaces (`architect`), implementation (`coder`), tests
(`test-author`). No agent can write two kinds, so no agent can make its
own work pass by moving the target. A failing test is a finding, not an
edit.

**Clean-room tests.** `test-author` is blocked from *reading* the
implementation, so its tests encode the spec rather than the code's
current behaviour. This is the single highest-value constraint in the kit
and the easiest one to accidentally break — see `agent-kit-test-author`.

**Enforcement, not instruction.** The boundaries above are hooks
(`.claude/hooks/path-guard.sh`), not paragraphs. Where enforcement is
impossible — the guard cannot inspect Bash, and `coder` needs Bash to run
tests — the prompt says so plainly and `supervisor` covers the gap.

**Plans separated from dispatch.** `architect` has no `Agent` tool. It
settles the interface and hands back ready-to-dispatch briefs; the
top-level session issues every dispatch. A `supervisor` finding is then
one hop from the human instead of relayed through another agent, and the
layer deciding whether to trust a worker's report is the layer that can
run the tests and read the git state.

**Verification of the workers themselves.** Every dispatch to a
write-capable agent is paired with a `supervisor` review that receives the
literal brief and the worker's own report, and checks both against the
files on disk. Any finding stops everything and goes to the human
verbatim.

```text
                  ┌──────────────── top-level session (PM only) ───────────────┐
                  │  delegates · reconciles · keeps record · never edits code  │
                  └───┬──────────────┬──────────────┬─────────────┬────────────┘
                      │ briefs       │              │             │
                      ▼              ▼              ▼             ▼
                 architect     test-author        coder      reviewer
                 docs+schemas  tests only      impl only     security-auditor
                      │              │              │        (read-only, JSON)
                      └──────────────┴──────────────┘             │
                          every dispatch also ──▶ supervisor ──▶ findings
                                                        │
                                  any finding ──▶ verbatim to human, STOP
```

## Install

Work through this in order; steps 2–4 are the per-agent skills.

### 1. Shared path-guard hook

Write to `.claude/hooks/path-guard.sh`, then `chmod +x` it. Requires
`bash` and `jq`. Three agents wire it in with different env vars rather
than duplicating the logic.

````bash
#!/usr/bin/env bash
# Generic PreToolUse path guard shared by this project's subagents
# (.claude/agents/*.md). A subagent wires this in via its own `hooks:`
# frontmatter, setting env vars inline on the command line to parametrize
# the same script per-agent instead of duplicating the logic per agent.
#
# Env vars (space-separated glob lists; `[[ str == pattern ]]` semantics,
# so a bare `*` in a pattern matches across `/` too — "docs/*" matches
# "docs/spec/overview.md"):
#
#   EXEMPT_GLOBS  - path matching any of these is always ALLOWED,
#                   checked before DENY_GLOBS/ALLOW_GLOBS.
#   DENY_GLOBS    - path matching any of these (and not exempt) is DENIED.
#   ALLOW_GLOBS   - if set, a path that is not exempt/already-denied must
#                   match at least one of these or it is DENIED
#                   (allowlist mode). Leave unset for denylist-only mode.
#
# Reads the PreToolUse JSON payload on stdin (see
# https://code.claude.com/docs/en/hooks) and checks tool_input.file_path,
# falling back to tool_input.path (Grep/Glob).
#
# Unscoped content tools are DENIED for guarded agents. Grep and Glob take
# an optional `path`; without one they search the whole project, and
# Grep's `output_mode: content` then returns matching lines from files the
# guard is supposed to hide. Passing such a call through makes the guard
# advisory rather than enforced. A guarded agent must therefore name an
# in-scope path it wants to search. The same applies to a `path` that
# resolves to the project root itself.
#
# Caveat (documented, not a bug): this only intercepts the tool calls named
# in the subagent's own `matcher` (Edit|Write or Read|Grep|Glob). It does
# NOT inspect Bash commands, so an agent that also has the Bash tool could
# still read or write a guarded path via a shell command. Keep Bash off
# any agent whose guard must be a hard boundary, or treat the guard as a
# strong default rather than a sandbox for agents that keep Bash.
#
# Requires: bash, jq.

set -f -e -u -o pipefail

input="$(cat)"
tool_name="$(printf '%s' "$input" | jq -r '.tool_name // empty')"
file_path="$(printf '%s' "$input" | jq -r '.tool_input.file_path // .tool_input.path // empty')"
cwd="$(printf '%s' "$input" | jq -r '.cwd // empty')"

# A guard is in force for this agent if it constrains paths at all.
guarded=0
if [[ -n "${DENY_GLOBS:-}" || -n "${ALLOW_GLOBS:-}" ]]; then
  guarded=1
fi

# Tools whose RESULTS can disclose the contents or existence of files
# anywhere under the search root, not just at one named path.
content_tool=0
case "$tool_name" in
  Read | Grep | Glob) content_tool=1 ;;
esac

deny() {
  local reason="$1"
  jq -n --arg reason "$reason" '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: $reason
    }
  }'
  exit 2
}

# No path at all. For a guarded agent this is an unscoped Grep/Glob over
# the whole project, which can return guarded content; deny it and say how
# to proceed. Any other tool shape passes through as before.
if [[ -z "$file_path" ]]; then
  if (( guarded && content_tool )); then
    deny "path guard: an unscoped ${tool_name} would search the whole project and can return files this agent may not read. Re-run it with an explicit in-scope 'path'."
  fi
  exit 0
fi

# Normalize to a path relative to the project/worktree root when possible.
# Note the exact-match arm: without it, a path equal to the root itself
# falls through with `rel` still absolute and matches no glob at all.
project_dir="${CLAUDE_PROJECT_DIR:-$cwd}"
rel="$file_path"
for base in "$cwd" "$project_dir"; do
  [[ -z "$base" ]] && continue
  base="${base%/}"
  if [[ "$file_path" == "$base" ]]; then
    rel="."
    break
  elif [[ "$file_path" == "$base"/* ]]; then
    rel="${file_path#"$base"/}"
    break
  fi
done

# The path resolved to the project root: same exposure as no path at all.
if [[ "$rel" == "." || "$rel" == "./" || -z "$rel" ]]; then
  if (( guarded && content_tool )); then
    deny "path guard: a project-root ${tool_name} would search every file and can return files this agent may not read. Re-run it with an explicit in-scope 'path'."
  fi
  exit 0
fi

matches_any() {
  local path="$1"; shift
  local pattern
  for pattern in "$@"; do
    [[ -z "$pattern" ]] && continue
    if [[ "$path" == $pattern ]]; then
      return 0
    fi
  done
  return 1
}

if [[ -n "${EXEMPT_GLOBS:-}" ]] && matches_any "$rel" $EXEMPT_GLOBS; then
  exit 0
fi

if [[ -n "${DENY_GLOBS:-}" ]] && matches_any "$rel" $DENY_GLOBS; then
  deny "path guard: '$rel' is out of scope for this agent (matched DENY_GLOBS)."
fi

if [[ -n "${ALLOW_GLOBS:-}" ]] && ! matches_any "$rel" $ALLOW_GLOBS; then
  deny "path guard: '$rel' is out of scope for this agent (did not match ALLOW_GLOBS)."
fi

exit 0
````

### 2. The write-capable agents

Install in this order — each depends on the previous one's boundary being
in place:

1. `agent-kit-architect` — settles interfaces; nothing else can start
   until there is something to implement against.
2. `agent-kit-test-author` — encodes the spec as tests. Verify its read
   guard properly; it is the one guard with a real bypass history.
3. `agent-kit-coder` — implements against both.

### 3. The read-only agents

4. `agent-kit-reviewer`
5. `agent-kit-security-auditor`

Both emit JSON-only findings so the session can act on them mechanically.
Neither needs `supervisor` pairing: with no write access they are
structurally incapable of an unauthorized action.

### 4. The watchdog

6. `agent-kit-supervisor` — **plus its session-side protocol**, which that
   skill spells out. The agent without the protocol is a report nobody
   acts on.

### 5. Governance rules in `CLAUDE.md`

The agents constrain the subagents. These constrain the session. Add them
adjusted to the project (the supervisor section lives in
`agent-kit-supervisor`):

````markdown
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

`{{DEFAULT_BRANCH}}` is protected: every significant change lands on a
dedicated feature branch and reaches `{{DEFAULT_BRANCH}}` only through a
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

Before merging, the full suite must pass: {{TEST_COMMANDS}}. Run those
commands so a failing one is actually visible — piping each to `tail`
hides its exit status and will report a red gate as green.
````

### 6. Optional: a pre-1.0 standing order

Useful while a project is young and the delegation overhead outweighs the
back-and-forth. It removes the *waiting*, not the *telling*:

````markdown
## Standing order (pre-1.0)

Until the 1.0.0 release ships: **where there is one clearly best next
step, the top-level session takes it without consulting the user first.**
Only genuine forks — two or more defensible options with no clear winner —
are put to the user before acting.

This is a rule about *asking*, not about *telling*. Anything this file
requires the session to report, it still reports, in the same turn it
acts; reporting is not consulting, because it does not block. Nor does it
loosen any other rule here: delegation, branch protection, the full test
bar and supervisor pairing all apply exactly as written.
````

If you adopt it, keep the supervisor carve-out: a finding naming a
backdoor, secret exfiltration or a disabled safety check always stops.

## Verify the whole system

Before trusting it with real work, run this once:

1. `bash -n .claude/hooks/path-guard.sh`; the file is executable; `jq` is
   installed.
2. `coder` is denied an edit to a test file and to a spec file.
3. `test-author` is denied an unscoped `Grep`, a project-root `Grep`, a
   `Grep` at the *parent* of an implementation directory, and a `Read` of
   an implementation file.
4. `architect` is denied a write outside its doc/schema allowlist, and has
   no `Agent` tool.
5. `reviewer`, `security-auditor` and `supervisor` each return output that
   parses with `jq`.
6. `supervisor` catches a worker that touched one file beyond its brief.

A guard that has never been tested against its own bypass is a guard you
are only assuming you have.

## What this kit does not give you

- **A sandbox.** `coder` keeps Bash, and the guard cannot inspect shell
  commands. The boundary is a strong default plus `supervisor`, not
  containment.
- **Protection against a bad brief.** Every constraint here is about a
  worker exceeding its instructions. A worker that does exactly what a
  wrong brief said will pass every check in the kit.
- **Test quality.** Clean-room tests encode the spec; if the spec is
  wrong, they encode that faithfully.
