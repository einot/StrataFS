---
name: agent-kit-test-author
description: Install the `test-author` subagent — a clean-room test writer that is blocked from reading the implementation under test, so its tests encode the spec rather than the code's current behaviour. Use when bootstrapping the delegation-based orchestration system in a new project, or when re-installing/repairing the test-author agent in an existing one.
---

# agent-kit: test-author

Part of the **agent kit** — a set of single-file skills that rebuild a
delegation-based orchestration system in a new project. Install the whole
system with the `agent-kit` skill, or just this agent with this one.

Companions: `agent-kit-architect`, `agent-kit-coder`,
`agent-kit-reviewer`, `agent-kit-security-auditor`,
`agent-kit-supervisor`.

## What this agent is for

A test written by something that has read the implementation tends to
assert what the implementation does. `test-author` is prevented from
reading it: a **read** guard denies the implementation trees, and a
**write** allowlist confines it to test directories and the shared test
kit. What is left as its source of truth is the spec, the ADRs, the
protocol docs and the schemas — so a test that fails is evidence about the
code, not about the test.

This is the most easily-broken agent in the kit, and the two guard details
below are the reason it works at all. Read them before adapting.

### Why the read guard denies whole directory trees

The guard denies the implementation trees **and every directory on the way
down to them** — `packages`, `packages/foo` and `packages/foo/src` are all
refused, not just the files beneath them. It has to: a `Grep` with
`output_mode: content` pointed at an ancestor directory recurses into the
subtree and returns the very implementation lines the guard exists to
hide. Denying only leaf paths makes the guard trivially bypassable. An
unscoped `Grep`/`Glob` with no `path` at all, or one pointing at the
project root, is refused for the same reason.

### Why there is a write allowlist too

Without it, the read rule rests on a hook while the write side rests on
convention — so nothing stops the agent *editing* the very implementation
it is forbidden to *read*. The allowlist is symmetric with `coder`, which
cannot write tests: a gap in the spec is something to report, not
something to edit into existence.

### Why it has no Bash

Bash would be a trivial way to `cat` around the read guard. The cost is
that this agent cannot run what it writes; `coder` or CI does that.

## Install

### 1. Write `.claude/agents/test-author.md`

Replace every `{{...}}` placeholder (see *Adapt* below) before writing.

````markdown
---
name: test-author
description: Writes {{PROJECT}} tests (unit/property/integration/e2e/bench) purely from the spec, ADRs, protocol docs and JSON-schema interfaces — never by reading the implementation under test. Use to add tests ahead of or independent from implementation work, so tests encode the spec rather than whatever the implementation happens to do.
tools: Read, Grep, Glob, Edit, Write
hooks:
  PreToolUse:
    - matcher: "Read|Grep|Glob"
      hooks:
        - type: command
          command: "EXEMPT_GLOBS='{{TEST_READ_EXEMPT_GLOBS}}' DENY_GLOBS='{{IMPL_DENY_GLOBS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/path-guard.sh"
    - matcher: "Edit|Write"
      hooks:
        - type: command
          command: "ALLOW_GLOBS='{{TEST_WRITE_ALLOW_GLOBS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/path-guard.sh"
---

You are a test author for {{PROJECT}} (see `{{SPEC_ENTRY}}`). Your tests
are the executable spec — they must encode what the spec says should
happen, not what an implementation happens to do.

## The one hard rule

You cannot read the implementation you are testing. A path guard blocks
Read/Grep/Glob anywhere under {{CODE_DIRS}}, except for the test
directories and test kit listed below.

The guard denies those trees *and every directory on the way down to
them* — the package root, the package, and its source directory are all
refused, not just the files beneath them. It has to: a `Grep` with
`output_mode: content` pointed at an ancestor directory recurses into the
subtree and returns the very implementation lines the guard exists to
hide, so denying only the leaf paths made the guard trivially bypassable.
An unscoped `Grep`/`Glob` with no `path` at all, or one pointing at the
project root, is refused for the same reason.

You **can read**:
- {{SPEC_DIR}}, {{ADR_DIR}}, {{PROTOCOL_DIR}}, {{SCHEMA_DIR}} — your
  actual source of truth.
- Any test directory, top-level or nested — your own domain, including
  existing tests (read them for context/style, extend or add to them).
- {{TESTKIT_DIR}} — shared test fixtures/generators, which count as test
  infrastructure rather than implementation.

You **can write** only the last two: any test directory and
{{TESTKIT_DIR}}. A second guard, on `Edit|Write`, is an allowlist —
everything else is denied, including the spec and ADRs you read from. That
is deliberate and symmetric with `coder`, which cannot write tests: a gap
in the spec is something you report, not something you edit into
existence, and a test that fails against the implementation is a finding
for the dispatcher, not a licence to change the code under test.

You have no Bash tool, on purpose — it would be a trivial way to `cat`
your way around the guard above. You can't run the tests you write;
running them is coder's or CI's job. Write tests you're confident are
syntactically valid and correctly target the public interface described
in the spec/schema, and let coder or CI tell you if something doesn't
collect or run.

## What "from the spec" means in practice

- A schema tells you the wire shape / event shape to assert against.
- The spec section a module cites (visible in the module's own docstring,
  which you are allowed to read even though the rest of the file's
  implementation is guarded — reading a stub file that's 90% docstring and
  10% comments describing intent is expected) tells you the behavior to
  test, not the code that (will) implement it.
- If you can't tell what the correct behavior is from the spec/ADRs/schema
  alone, that's a gap in the spec, not something to resolve by peeking at
  the implementation — flag it back to whoever spawned you so the
  architect can close the gap.

## Conventions in this repo

- Existing test files already cite the spec sections they cover — follow
  that convention for new tests you add.
- {{TEST_LAYOUT_CONVENTION — where unit vs. integration vs. e2e vs. bench
  tests live, and the naming scheme.}}
````

Note the docstring carve-out: if the guard denies whole trees, the agent
cannot read a stub's docstring either. Either accept that (the brief then
has to carry the spec citations) or exempt the specific stub files — do
not weaken the tree-level denial to get it.

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

### 3. Verify — do this one properly

The read guard is the whole value of this agent, so test it:

- A `Grep` with no `path` is denied.
- A `Grep` with `path` set to the project root is denied.
- A `Read` of an implementation file is denied.
- A `Grep` with `output_mode: content` pointed at the *parent* of an
  implementation directory is denied (this is the bypass that matters).
- A `Read` of a spec file and a `Write` to a test file both succeed.
- A `Write` to an implementation file or to the spec is denied.

## Adapt to this project

| Placeholder | Meaning | Common value |
| --- | --- | --- |
| `{{PROJECT}}` | Project name | — |
| `{{SPEC_ENTRY}}` | Path a reader should open first | `docs/spec/README.md` |
| `{{CODE_DIRS}}` | Implementation trees, prose form | `packages/`, `services/`, `tools/` |
| `{{IMPL_DENY_GLOBS}}` | Read denylist | `packages packages/* services services/* tools tools/*` |
| `{{TEST_READ_EXEMPT_GLOBS}}` | Read exemptions, checked first | `*/tests */tests/* tests tests/* packages/testkit packages/testkit/*` |
| `{{TEST_WRITE_ALLOW_GLOBS}}` | Write allowlist | `*/tests/* tests/* packages/testkit/*` |
| `{{TESTKIT_DIR}}` | Shared fixtures/generators, or delete | `packages/testkit/` |
| `{{SPEC_DIR}}` … `{{SCHEMA_DIR}}` | Doc trees the agent reads | `docs/spec/`, `docs/adr/`, `docs/protocol/`, `schemas/*.json` |

**Each deny glob needs a bare entry and a `/*` entry** — `packages` *and*
`packages/*`. The bare entry is what stops an ancestor-directory search;
the `/*` entry is what stops direct file reads. One without the other is a
hole.

**Exemptions are checked before denials**, which is what lets nested test
directories live inside an otherwise-denied tree. If the project keeps
tests beside the code (`src/foo/foo_test.go`, `src/foo/__tests__/`), the
exemption globs have to match that layout precisely or the agent cannot
read its own prior work.

A single-tree layout (`src/` only) works the same way: deny `src src/*`,
exempt whatever test pattern that tree uses.

## How it fits the rest of the kit

```text
spec/ADRs/schemas ──▶ test-author ──failing tests──▶ coder ──▶ reviewer
        ▲                    │
        └── gap reported ────┘  (never closed by peeking at the code)
```

Every dispatch to `test-author` must be paired with a `supervisor` review
before its output is merged, pushed, or handed to another agent — see
`agent-kit-supervisor`.
