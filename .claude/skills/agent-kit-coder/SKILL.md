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
# Path guard: wired in .claude/settings.json, NOT here. `hooks:` is a
# documented frontmatter field, but a guard declared there did not fire
# in the environment this kit came out of -- probed three times, once
# with an absolute script path; no error, no warning, nothing to notice.
# The docs require workspace trust for project-level frontmatter hooks,
# which is the likely cause but is not confirmed. Either way a guard
# here can look enforced on one machine and silently do nothing on
# another. settings.json hooks fired in every test, and they are read
# from the main checkout, not from this agent's worktree -- so the copy
# of settings.json inside the worktree is inert.
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
given, even though nothing will stop you mechanically. The same goes for
editing the guard's own configuration: the policy that fences you is read
from the main checkout, so the `.claude/` directory inside your worktree
is not the one in force, and changing it would be an attempt to escape
rather than a fix.

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
anything around it.

**Wire it in `.claude/settings.json` — not in the agent file:**

````json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "SCOPE_AGENT_TYPES='coder' DENY_GLOBS='{{DENY_GLOBS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/path-guard.sh"
          }
        ]
      }
    ]
  }
}
````

Merge this entry into the file's existing `PreToolUse` array rather than
replacing it; each guarded agent in the kit contributes its own entry.

Four things about this wiring are load-bearing, and the last one bites
this agent specifically:

- **Never use the agent file's `hooks:` frontmatter.** It is a documented
  field, but a guard declared there did not fire in the environment this
  kit came out of — probed three times, including with an absolute script
  path. No error, no warning; the agent ran completely unfenced. Whether
  it fires appears to depend on workspace-trust state that is invisible
  from the repository, so it can look enforced on one machine and do
  nothing on another. `settings.json` hooks fired in every test.
- **`SCOPE_AGENT_TYPES` is required.** A `settings.json` hook is
  session-wide: it sees calls from every agent and from the top-level
  session. Without the scope, this denylist would also stop the session
  from reconciling `coder`'s work.
- **The scoping is fail-open.** An absent or unlisted `agent_type` passes
  through. That is routing, not a check — an exit 0 for an out-of-scope
  caller is not approval.
- **Hook configuration is read from the main checkout, not from the
  worktree.** `coder` runs under `isolation: worktree`, and it is fenced
  by whatever the *main* checkout's `.claude/settings.json` says at
  dispatch time; the `settings.json` sitting inside its own worktree is
  inert. This was established by experiment: with the policy removed from
  the main checkout only, while the worktree copy still carried it, the
  write was not denied. So a policy change must land in the main checkout
  before the probe that tests it — editing the file the agent can see
  changes nothing.

This one is a **denylist** (`DENY_GLOBS`, no `ALLOW_GLOBS`), unlike
`architect`'s and `test-author`'s allowlists. That is deliberate:
implementation sprawls across build files, CI config and container
definitions that are legitimately in scope, so enumerating what `coder`
may write would be a list that is wrong within a week. The cost is that a
new guarded directory has to be added to the denylist explicitly — see
*Adapt* below.

### 3. Verify

Nothing here can be checked by reading a config file. The only evidence a
guard works is a real dispatch that a real policy refused — and for this
agent the policy that counts is the one in the **main** checkout.

- `bash -n .claude/hooks/path-guard.sh` parses, and the file is executable.
- Dispatch the agent with a throwaway task that tries to edit a test file
  and confirm the write is denied **with a reason whose text comes from
  the path guard**. A refusal that instead mentions "allowed working
  directories" is the platform sandbox, and means this guard never ran.
- Repeat for a spec/schema path — the denylist has to cover both trees,
  and covering one is the common half-installed state.
- Confirm a write to a legitimate implementation path still succeeds.
- Confirm the top-level session can still edit test files. If it cannot,
  `SCOPE_AGENT_TYPES` is missing from the settings entry.
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
