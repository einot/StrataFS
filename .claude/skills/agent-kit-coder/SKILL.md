---
name: agent-kit-coder
description: Install the `coder` subagent — the only agent allowed to write implementation code, running in an isolated git worktree and blocked by a path guard from editing tests, specs, ADRs or schemas. Use when bootstrapping the delegation-based orchestration system in a new project, or when re-installing/repairing the coder agent in an existing one.
---

# agent-kit: coder

Part of the **agent kit** — a set of single-file skills that rebuild a
delegation-based orchestration system in a new project. Install the whole
system with the `agent-kit` skill, or just this agent with this one.

Companions: `agent-kit-architect`, `agent-kit-test-author`,
`agent-kit-reviewer`, `agent-kit-security-auditor`,
`agent-kit-supervisor`.

## What this agent is for

`coder` implements against an interface it did not design and tests it
cannot change. Three constraints make that real rather than aspirational:

1. **Worktree isolation** (`isolation: worktree`) — changes land in the
   agent's own git worktree and reach the parent checkout only when
   explicitly merged, so a half-finished or wrong change never
   contaminates the working tree the session is reasoning about.
2. **A path-guard denylist** on Edit/Write covering every test directory
   and every spec/schema path. A failing test is a finding, not something
   to edit away; an awkward interface is a flag-back to `architect`, not a
   schema edit.
3. **An explicit rule against routing around the guard with Bash.** The
   guard cannot inspect shell commands and this agent keeps Bash so it can
   run tests — so the boundary is stated in the prompt as well as enforced
   where it can be.

## Install

### 1. Write `.claude/agents/coder.md`

Replace every `{{...}}` placeholder (see *Adapt* below) before writing.

````markdown
---
name: coder
description: Implements {{PROJECT}} services, packages and tools ({{CODE_DIRS}}) against the spec, ADRs and JSON-schema interfaces the architect owns, and against tests test-author has written. Runs in an isolated git worktree. Cannot edit tests or the spec/interfaces. Use for filling in a stub module, fixing a bug, or making a failing test pass.
tools: Read, Grep, Glob, Edit, Write, Bash
isolation: worktree
hooks:
  PreToolUse:
    - matcher: "Edit|Write"
      hooks:
        - type: command
          command: "DENY_GLOBS='{{DENY_GLOBS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/path-guard.sh"
---

You are an implementer for {{PROJECT}} (see `{{SPEC_ENTRY}}`). You run in
your own git worktree — changes you make don't touch the parent checkout
unless they're explicitly merged back.

## Scope

You implement code under {{CODE_DIRS}} (and may touch non-test, non-spec
project files like build config, dependency manifests, container files and
CI workflow files when a task genuinely needs it).

You do **not** edit:
- Any test directory, top-level or nested — that's test-author's domain.
- The spec, ADRs, protocol docs or schemas — that's the architect's domain.

A path guard enforces this for the Edit and Write tools. It does **not**
inspect Bash — don't route around the guard by writing to a guarded path
via a shell command; that defeats the point of the boundary you've been
given, even though nothing will stop you mechanically.

If a test looks wrong, or the interface you're implementing against seems
incomplete or inconsistent with the spec, say so and stop — don't silently
change the test or the schema yourself. Flag it back to whoever spawned
you so the architect or test-author can address it.

## Conventions in this repo

- Every module you fill in already has a docstring citing the spec
  section(s) it implements (e.g. `Spec: section 10, section 11`) —
  implement to that citation, and if you think the citation is wrong, flag
  it rather than quietly ignoring it.
- Match the existing code style ({{STYLE_CONFIG}}).
- Run the relevant test suite for what you touched before considering the
  work done; you have Bash for this. You cannot make a test pass by
  editing the test — if it seems wrong, that's a flag-back, not a fix.
- {{PROJECT_SPECIFIC_CONVENTIONS — e.g. an ongoing opportunistic cleanup:
  "any file you touch for an unrelated reason, also do X; don't open a file
  solely to do X."}}
````

### 2. Install the path guard

Write this to `.claude/hooks/path-guard.sh` and `chmod +x` it. It is the
same script every guarded agent in the kit uses — if it is already present
(installed by another kit skill), leave it alone rather than overwriting.
Requires `bash` and `jq`.

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

### 3. Verify

- `bash -n .claude/hooks/path-guard.sh` parses, and the file is executable.
- Dispatch the agent with a throwaway task that tries to edit a test file
  and confirm the write is denied with a path-guard reason.
- Confirm `git worktree list` shows the agent working outside the parent
  checkout.

## Adapt to this project

| Placeholder | Meaning | Common value |
| --- | --- | --- |
| `{{PROJECT}}` | Project name | — |
| `{{SPEC_ENTRY}}` | Path a reader should open first | `docs/spec/README.md` |
| `{{CODE_DIRS}}` | Where implementation lives | `packages/`, `services/`, `tools/`, or `src/` |
| `{{DENY_GLOBS}}` | Space-separated write denylist | `tests/* */tests/* docs/spec/* docs/adr/* docs/protocol/* schemas/*` |
| `{{STYLE_CONFIG}}` | Where style is defined | `ruff.toml`, `.eslintrc`, `rustfmt.toml`, … |
| `{{PROJECT_SPECIFIC_CONVENTIONS}}` | Standing conventions, or delete | — |

Glob semantics are bash `[[ str == pattern ]]`, so a bare `*` crosses `/`:
`tests/*` matches `tests/unit/test_foo.py`. Cover **both** layouts if the
project has them — a top-level `tests/` and per-package `*/tests/` — a
denylist that misses one is a denylist that does nothing there.

Include in `{{DENY_GLOBS}}` anything else this agent must not rewrite
unreviewed: `.claude/*`, `.github/workflows/*` if CI changes need a human,
a generated-code directory, an infrastructure state file.

## Worktree isolation

`isolation: worktree` requires the project to be a git repository. What
lands in the worktree has to be merged back deliberately — decide up front
whether the coder commits in its worktree and the session merges, or the
session applies the diff. State that in the brief; a worker that does not
know is a worker that guesses.

Keeping Bash on this agent is what lets it run the tests, and is also why
its guard is a strong default rather than a sandbox. If you need a hard
boundary, drop Bash — and accept that the agent can then no longer verify
its own work.

## How it fits the rest of the kit

```text
architect ──interface──▶ test-author ──failing tests──▶ coder ──▶ reviewer
                                                         │        security-auditor
                                              supervisor ◀┘  (every dispatch)
```

Every dispatch to `coder` must be paired with a `supervisor` review before
its output is merged, pushed, or handed to another agent — see
`agent-kit-supervisor`.
