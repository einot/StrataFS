---
name: agent-kit
description: Bootstrap a delegation-based multi-agent orchestration system in a project — architect, coder, test-author, reviewer, security-auditor and supervisor, the shared path-guard and bash-guard hooks, the settings.json wiring that actually makes those guards fire, and the CLAUDE.md governance rules that hold the whole thing together. Use when starting a new development project that should be built by delegated agents rather than by the session directly, or when auditing an existing setup against the reference design.
---

# agent-kit

Six agents, two hooks, and a page of governance rules. Together they make
a top-level session into a project manager that delegates all work and
verifies what comes back, instead of an editor that does everything
itself.

Each agent also ships as its own single-file skill, so you can install or
repair one without the rest:

| Skill | Agent | Writes? | Bash? |
| --- | --- | --- | --- |
| `agent-kit-architect` | `architect` | docs + schemas only | no |
| `agent-kit-coder` | `coder` | implementation only, in a worktree | yes, unfenced |
| `agent-kit-test-author` | `test-author` | tests only, cannot read the code | no |
| `agent-kit-reviewer` | `reviewer` | no | no |
| `agent-kit-security-auditor` | `security-auditor` | no | optional, fenced |
| `agent-kit-supervisor` | `supervisor` | no | no |

The two hooks are real files in this bundle, at
`skills/agent-kit/hooks/path-guard.sh` and
`skills/agent-kit/hooks/bash-guard.sh`. Every skill that needs a guard
points at those same two files rather than carrying its own copy, so a
fix lands once.

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

**Enforcement, not instruction.** The boundaries above are `PreToolUse`
hooks, not paragraphs. `path-guard.sh` polices which paths an agent may
read or write; `bash-guard.sh` is a default-deny fence over the Bash tool
for an agent that needs to run read-only inspection tooling and nothing
else. Where enforcement is impossible — `coder` needs an unfenced Bash to
run the test suite — the prompt says so plainly and `supervisor` covers
the gap.

**Guards are only real once they have fired.** Hook configuration lives in
`.claude/settings.json`, never in an agent file's `hooks:` frontmatter,
and the only evidence a guard works is a real dispatch that a real policy
refused. See *Wire the guards* below; this is the part of the kit most
likely to look installed while doing nothing at all.

**Plans separated from dispatch.** `architect` has no `Agent` tool. It
settles the interface and hands back ready-to-dispatch briefs; the
top-level session issues every dispatch. A `supervisor` finding is then
one hop from the human instead of relayed through another agent, and the
layer deciding whether to trust a worker's report is the layer that can
run the tests and read the git state.

**Verification of the workers themselves.** Every dispatch to an agent
that can write or execute is paired with a `supervisor` review. It
receives the literal brief, the worker's own report, and the `git status`
and `git diff` for that dispatch — collected by the session, because
`supervisor` has no Bash and keeps it that way. Any finding goes to the
human verbatim, and stops everything unless every entry is `unverified`.

```text
                  ┌──────────────── top-level session (PM only) ───────────────┐
                  │  delegates · reconciles · keeps record · never edits code  │
                  └───┬──────────────┬──────────────┬─────────────┬────────────┘
                      │ briefs       │              │             │
                      ▼              ▼              ▼             ▼
                 architect     test-author        coder      reviewer
                 docs+schemas  tests only      impl only     security-auditor
                      │              │              │        (read-only, JSON)
                      └──────────────┴──────────────┴──────────────┘
                     every dispatch that can write or execute
                                  ──▶ supervisor ──▶ findings
                                            │
                        any finding ──▶ verbatim to human, STOP
```

## Install

Work through this in order. Steps 3–5 are the per-agent skills.

### 1. Copy the shared hooks

Both hooks live in this bundle. Copy them into the project and make them
executable — they are not templates and need no editing:

```bash
mkdir -p .claude/hooks
cp path/to/agent-kit-skills/skills/agent-kit/hooks/path-guard.sh .claude/hooks/
cp path/to/agent-kit-skills/skills/agent-kit/hooks/bash-guard.sh .claude/hooks/
chmod +x .claude/hooks/path-guard.sh .claude/hooks/bash-guard.sh
bash -n .claude/hooks/path-guard.sh && bash -n .claude/hooks/bash-guard.sh
```

Requires `bash` and `jq` on the machine running the session; both hooks
parse their JSON payload with `jq`.

`bash-guard.sh` is only needed if you give `security-auditor` a Bash tool
(see `agent-kit-security-auditor`). Copy it anyway — an inert hook costs
nothing, and the alternative is discovering it is missing at the moment
you wire it.

Read the header comment of each before adapting anything. Both carry the
reasoning behind rules that look arbitrary until you know what broke.

### 2. Wire the guards in `.claude/settings.json`

**This is the step that makes the difference between a guard and a
decoration.** Read all four points before writing the file.

**Hooks go in `.claude/settings.json`, never in an agent file's
frontmatter.** `hooks:` is a documented frontmatter field, but a guard
declared there did not fire in the environment this kit came out of —
probed three times, including with an absolute script path and with
`${CLAUDE_PROJECT_DIR}`. No error, no warning, nothing to notice; the
agent simply ran unfenced. The best-supported explanation is the
documented requirement that a project-level agent's frontmatter hooks
take effect only once the workspace trust dialog has been accepted for
the folder containing the agent file, which a headless session never
does — but that has not been confirmed directly, and it does not matter
much: whether a frontmatter guard fires depends on environment state that
is invisible from the repository, so it can look enforced on one machine
and silently do nothing on another. `settings.json` hooks fired in every
test.

**Settings hooks are session-wide, so every policy needs
`SCOPE_AGENT_TYPES`.** A hook wired in `settings.json` sees tool calls
from every agent *and* from the top-level session, not just the agent the
policy was written for. `SCOPE_AGENT_TYPES` names the agent types a
policy applies to, matched against the payload's `agent_type`. One entry
carries one policy, because the env vars come from that entry's own
command line — two agents needing different globs need two entries. Two
entries whose scopes overlap both run, and the stricter denial wins.

The scoping is **fail-open by design**: an absent or unlisted
`agent_type` means "not my business", not "deny" — a top-level call
carries no `agent_type` at all. It is routing, not a check, so an exit 0
for an out-of-scope caller is not approval.

**Hook configuration is read from the main checkout, not from a
worktree.** A worktree-isolated agent such as `coder` is fenced by
whatever the main checkout's `.claude/settings.json` says at dispatch
time; the copy inside its worktree is inert. This was established by
experiment: with a policy removed from the main checkout only, while the
worktree copy still carried it, the write was *not* denied. So to test a
policy change, put the change in the main checkout and dispatch a probe
right after.

**A config test is not a fired hook.** Pinning the contents of
`settings.json` in a test is worth doing, but it proves only that the
file says what you meant. The only evidence a guard works is a real
dispatch attempting an operation the policy must refuse, and a refusal
whose text comes from the guard. If a command that should be denied
simply succeeds, treat that as evidence the fence is missing.

The wiring, with the kit's default scopes:

````json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "SCOPE_AGENT_TYPES='architect' ALLOW_GLOBS='{{DOC_GLOBS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/path-guard.sh"
          }
        ]
      },
      {
        "matcher": "Edit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "SCOPE_AGENT_TYPES='coder' DENY_GLOBS='{{DENY_GLOBS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/path-guard.sh"
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
      },
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
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "SCOPE_AGENT_TYPES='security-auditor' ALLOW_CMDS='{{ALLOW_CMDS}}' ALLOW_GIT_SUBCMDS='{{ALLOW_GIT_SUBCMDS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/bash-guard.sh"
          }
        ]
      }
    ]
  }
}
````

Each per-agent skill repeats its own entry and explains its globs. The
last entry is only needed if `security-auditor` has Bash.

### 3. The write-capable agents

Install in this order — each depends on the previous one's boundary being
in place:

1. `agent-kit-architect` — settles interfaces; nothing else can start
   until there is something to implement against.
2. `agent-kit-test-author` — encodes the spec as tests. Verify its read
   guard properly; it is the one guard with a real bypass history.
3. `agent-kit-coder` — implements against both.

### 4. The read-only agents

4. `agent-kit-reviewer`
5. `agent-kit-security-auditor`

Both emit JSON-only findings so the session can act on them mechanically.
`reviewer` never needs `supervisor` pairing: with no write access and no
Bash it is structurally incapable of an unauthorized action.
`security-auditor` is in the same position **only while it has no Bash**
— give it the fenced Bash and it joins the paired set. See that skill, and
the supervisor protocol, for why.

### 5. The watchdog

6. `agent-kit-supervisor` — **plus its session-side protocol**, which that
   skill spells out. The agent without the protocol is a report nobody
   acts on.

### 6. Governance rules in `CLAUDE.md`

The agent files constrain the subagents. These constrain the session. Add
them adjusted to the project (the supervisor section lives in
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
obviously safe to bother delegating. `test-author` has no Bash and so
cannot run a formatter itself; that means making the edit by hand with
Edit until the content matches, not an excuse to make the edit directly
instead. The same holds for `coder`'s and `architect`'s domains: "it's
tiny" is never a reason to touch code, tests, or specs/schemas/ADRs
directly. The top-level session's own tools stay limited to reconciling
already-delegated work (applying a worker's own diff/commit, resolving a
merge conflict) and to editing `CLAUDE.md`, agent definitions, and
non-code governance docs it owns directly.

**Agent configuration.** The top-level session may alter agent
configuration — `.claude/agents/*.md`, including which model backs an
agent — when the user directly instructs it to. It may not alter it on its
own initiative: not to work around a limitation it has run into, and not
because the change would make the job in front of it easier or faster. If
an agent's configuration looks like it is blocking legitimate work, say so
and let the user decide; changing it unasked defeats the point of having
the constraint.

**Agent guards.** The subagent path and Bash guards live in
`.claude/settings.json`, scoped per agent with `SCOPE_AGENT_TYPES`, and
nowhere else. Never declare them in an agent file's `hooks:` frontmatter:
a guard declared there did not fire in this environment (three probes),
most likely because project-level frontmatter hooks require the workspace
trust dialog to have been accepted, which a headless session never does.
Hook configuration is read from the *main checkout*, not from a
worktree-isolated agent's checkout — so a `coder` dispatch is fenced by
whatever the main checkout's `settings.json` says at that moment, and the
copy in its worktree is inert. Verify any guard change the only way that
counts: put it in the main checkout, dispatch a real agent, and have it
attempt an operation the policy must refuse. A test that pins the wiring
is worth having, but a passing test is not a fired hook.

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

### 7. Optional: a pre-1.0 standing order

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

Before trusting it with real work, run this once. Every step that says
"dispatch" means a real subagent dispatch — nothing here can be checked by
reading a config file.

1. `bash -n` passes on both hooks, both are executable, and `jq` is
   installed.
2. `coder` is denied an edit to a test file and to a spec file, and the
   refusal text comes from the path guard. A platform message about
   "allowed working directories" instead means the guard never ran.
3. `test-author` is denied an unscoped `Grep`, a project-root `Grep`, a
   `Grep` at the *parent* of an implementation directory, and a `Read` of
   an implementation file.
4. `architect` is denied a write outside its doc/schema allowlist, and has
   no `Agent` tool.
5. If `security-auditor` has Bash: it is denied a command whose name is
   absent from `ALLOW_CMDS`, and the denial says `bash guard:`.
6. `reviewer`, `security-auditor` and `supervisor` each return a final
   message whose **last JSON object** parses with `jq`. Extract it rather
   than parsing the whole message: in testing, agents prefixed the JSON
   with a paragraph often enough that a whole-message parse is not a
   contract you can build on.
7. `supervisor` catches a worker that touched one file beyond its brief —
   make it a change grepping cannot find, such as a deletion — *and* stays
   quiet when given an honest report of an in-scope change. Give it the
   git evidence; without it, the scope question is guesswork.
8. The top-level session is *not* affected by any of the above — the
   scoping is meant to leave it alone.

A guard that has never been tested against its own bypass is a guard you
are only assuming you have. The guard in this kit was reviewed nine times
before anyone noticed it had never once been invoked.

## What this kit does not give you

- **A sandbox.** `coder` keeps an unfenced Bash, and `path-guard.sh`
  cannot inspect shell commands. For `coder` the boundary is a strong
  default plus `supervisor`, not containment. `bash-guard.sh` is a real
  fence, but it is a shell script reasoning about shell syntax, not an
  OS-enforced boundary — it is why `security-auditor` gets paired with
  `supervisor` once it has Bash.
- **Protection against a bad brief.** Every constraint here is about a
  worker exceeding its instructions. A worker that does exactly what a
  wrong brief said will pass every check in the kit.
- **Test quality.** Clean-room tests encode the spec; if the spec is
  wrong, they encode that faithfully.
