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
# Path guards (both of them): wired in .claude/settings.json, NOT here.
# `hooks:` is a documented frontmatter field, but a guard declared there
# did not fire in the environment this kit came out of -- probed three
# times, once with an absolute script path; no error, no warning,
# nothing to notice. The docs require workspace trust for project-level
# frontmatter hooks, which is the likely cause but is not confirmed.
# Either way a guard here can look enforced on one machine and silently
# do nothing on another -- and an unfenced test-author reads the
# implementation and writes tests that pass, so nothing in its output
# reveals the failure. settings.json hooks fired in every test.
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

### 2. Install and wire the path guard

**Copy the hook.** `path-guard.sh` is a real file in this bundle at
`skills/agent-kit/hooks/path-guard.sh`. It is the same script every
guarded agent in the kit uses, and it is not a template — copy it, do not
edit it. If another kit skill already installed it, leave it alone.

```bash
mkdir -p .claude/hooks
cp path/to/agent-kit-skills/skills/agent-kit/hooks/path-guard.sh .claude/hooks/
chmod +x .claude/hooks/path-guard.sh
```

Requires `bash` and `jq`. Read its header comment before adapting
anything around it; the section on unscoped content tools is the one that
keeps this agent's read guard from being trivially bypassable.

**Wire both entries in `.claude/settings.json` — not in the agent file.**
This agent needs two policies, a read denylist and a write allowlist, and
one settings entry can only carry one:

````json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Read|Grep|Glob",
        "hooks": [
          {
            "type": "command",
            "command": "SCOPE_AGENT_TYPES='test-author' EXEMPT_GLOBS='{{TEST_READ_EXEMPT_GLOBS}}' DENY_GLOBS='{{IMPL_DENY_GLOBS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/path-guard.sh"
          }
        ]
      },
      {
        "matcher": "Edit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "SCOPE_AGENT_TYPES='test-author' ALLOW_GLOBS='{{TEST_WRITE_ALLOW_GLOBS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/path-guard.sh"
          }
        ]
      }
    ]
  }
}
````

Merge these entries into the file's existing `PreToolUse` array rather
than replacing it; each guarded agent in the kit contributes its own.

Four things about this wiring are load-bearing:

- **Never use the agent file's `hooks:` frontmatter.** It is a documented
  field, but a guard declared there did not fire in the environment this
  kit came out of — probed three times, including with an absolute script
  path. No error, no warning; the agent ran completely unfenced. Whether
  it fires appears to depend on workspace-trust state that is invisible
  from the repository, so it can look enforced on one machine and do
  nothing on another. `settings.json` hooks fired in every test. For this
  agent that failure mode is worse than for the others: an unfenced
  `test-author` reads the implementation and writes tests that pass, and
  nothing in the output looks wrong.
- **`SCOPE_AGENT_TYPES` is required on both entries.** A `settings.json`
  hook is session-wide. Without the scope, the read denylist would blind
  the top-level session to the whole implementation tree.
- **The scoping is fail-open.** An absent or unlisted `agent_type` passes
  through. That is routing, not a check — an exit 0 for an out-of-scope
  caller is not approval.
- **Hook config is read from the main checkout**, not from a
  worktree-isolated agent's checkout. Put policy changes in the main
  checkout and dispatch a probe right after.

`EXEMPT_GLOBS` is checked before `DENY_GLOBS`, which is what lets the read
policy deny whole implementation trees while still admitting the test
directories nested inside them. Order matters: the exemptions must name
every test directory that lives *under* a denied tree, or the agent
cannot read its own existing tests.

### 3. Verify — do this one properly

The read guard is the whole value of this agent, and it is the one guard
in the kit with a real bypass history, so test it by dispatching the agent
and having it actually attempt each of these. Reading `settings.json` and
seeing the right globs proves only that the file says what you meant; it
does not prove the hook was reached.

- A `Grep` with no `path` is denied.
- A `Grep` with `path` set to the project root is denied.
- A `Read` of an implementation file is denied.
- A `Grep` with `output_mode: content` pointed at the *parent* of an
  implementation directory is denied (this is the bypass that matters).
- A `Read` of a spec file and a `Write` to a test file both succeed.
- A `Write` to an implementation file or to the spec is denied.
- Every denial above carries a reason whose text comes from the path
  guard. A refusal mentioning "allowed working directories" instead is
  the platform sandbox, and means this guard never ran.
- The top-level session can still read the implementation tree. If it
  cannot, `SCOPE_AGENT_TYPES` is missing from a settings entry.

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
