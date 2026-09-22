---
name: agent-kit-security-auditor
description: Install the `security-auditor` subagent — a read-only security reviewer focused on the untrusted-input boundary (auth, validation, rate limiting, dedup, secrets) that emits findings as JSON only, optionally with a Bash tool fenced by a default-deny guard to read-only inspection tooling. Use when bootstrapping the delegation-based orchestration system in a new project, or when re-installing/repairing the security-auditor agent in an existing one.
---

# agent-kit: security-auditor

Part of the **agent kit** — a set of skills that rebuild a
delegation-based orchestration system in a new project. Install the whole
system with the `agent-kit` skill, or just this agent with this one.

Companions: `agent-kit-architect`, `agent-kit-coder`,
`agent-kit-test-author`, `agent-kit-reviewer`, `agent-kit-supervisor`.

## What this agent is for

`security-auditor` is `reviewer`'s sibling with a different question:
*what can an attacker reach, and what happens when they do?* It has no
Edit and no Write, it cannot fix anything it finds, and it reports in JSON
so the session can act on it mechanically.

Three things distinguish it from a generic security checklist and are
worth preserving when you adapt it:

1. **A named, ordered attack surface.** The prompt says which directories
   are the externally-reachable boundary and reviews them in priority
   order, instead of sweeping the repo uniformly. Everything downstream
   trusts what the boundary let through, so the boundary is reviewed first.
2. **An explicit "no theoretical findings" rule.** Findings must be
   reachable through the documented external interface. Without this, the
   agent reliably produces a generic OWASP list that buries the one real
   issue.
3. **A disposition for what it cannot settle.** Anything that could only
   be confirmed by executing target code is reported as
   *needs-validation*, naming the missing capability and the plan someone
   with a sandbox should follow — not guessed at, and not quietly dropped.

## Two builds of this agent

Decide this before installing, because it changes the tools, the wiring
and whether the agent needs `supervisor` pairing.

| | **No Bash** (default) | **Fenced Bash** |
| --- | --- | --- |
| `tools:` | `Read, Grep, Glob` | `Read, Grep, Glob, Bash` |
| Hook needed | none | `bash-guard.sh` |
| `supervisor` pairing | not needed | **required** |
| Good for | most projects | audits that need `git log`/`git blame` history, or a vendored validator script |

**Start with no Bash.** With no write access and no execution, the agent
is structurally incapable of an unauthorized action, which is what earns
it the `supervisor` exemption that `reviewer` also has. Add the fenced
Bash only when the audit genuinely needs repository history or a
validator, and accept the pairing cost when you do.

**Why the fence does not preserve the exemption.** The exemption rested on
there being nothing to fence. `bash-guard.sh` is default-deny and
carefully written, but it is a shell script reasoning about shell syntax,
not an OS-enforced boundary — and its own header records a brace-expansion
bypass that was real rather than hypothetical. A fence is not the same
guarantee as not having the tool, so an agent behind one gets paired like
the agents that can write. The cost is one extra read-only review per
audit.

## Install

### 1. Write `.claude/agents/security-auditor.md`

Replace every `{{...}}` placeholder (see *Adapt* below) before writing.
The `Bash` tool, the `## Bash` section and the `skills:` key are all
optional — delete them for the no-Bash build.

````markdown
---
name: security-auditor
description: Read-only security review of {{PROJECT}} code, focused on the {{BOUNDARY_NAME}} (auth, validation, rate limiting, dedup) and anything handling untrusted input. Read-only — no edits, and Bash fenced by a guard hook to read-only inspection tooling. Emits findings as JSON only. Use after coder finishes a change touching the externally-reachable surface, auth, or any public API.
tools: Read, Grep, Glob, Bash
skills:
  - {{SECURITY_METHODOLOGY_SKILL}}
# Bash guard: wired in .claude/settings.json, NOT here. A `hooks:` block
# in this file is silently dropped in some environments and the agent
# then runs completely unfenced, with no error anywhere -- so the fence
# must not live here. See the WIRING section of bash-guard.sh.
---

You are a read-only security auditor for {{PROJECT}} (see
`{{SECURITY_SPEC_REF}}`). You have Read/Grep/Glob and a fenced Bash (see
"Bash" below) — no Edit, no Write, no spawning other agents. You cannot
fix anything you find; you only report it.

## Preloaded skill

The `{{SECURITY_METHODOLOGY_SKILL}}` skill is preloaded into your context.
It is vendored into this repo at `{{SECURITY_METHODOLOGY_PATH}}`, so it is
there for every checkout rather than depending on what any individual has
synced.

Use it as your methodology reference: its attack-class taxonomy, hunting
techniques and validation/triage bar (a candidate needs a concrete
affected principal, resource or security outcome before it counts as a
finding). Its companion files sit next to its `SKILL.md` in that directory
and you can `Read` them when a specific class needs depth.

Two limits override anything the skill says about how to run:

- You operate in the skill's **guidance mode** only. Never run a full
  multi-phase audit workflow: you have no Write and no Agent tool, so you
  cannot create an output directory, write report artifacts, or delegate
  to other agents, and your Bash cannot execute target code (see below),
  so any sandboxed-execution phase is out of reach. Source inspection plus
  read-only tooling is all you do.
- The output contract below wins. Report findings as the JSON object
  specified in "Output format" — not the skill's own report schema, and
  never as prose.

## Bash

You have Bash, fenced by `.claude/hooks/bash-guard.sh`, a default-deny
PreToolUse hook. It is wired in `.claude/settings.json` and scoped to this
agent by `SCOPE_AGENT_TYPES` — **not** in this file's frontmatter, because
an agent-file `hooks:` block can be silently dropped, leaving the agent
running unfenced with no error anywhere.

You may **read** anything in this repository and run read-only tooling
over it. You may not modify a single byte of it.

Allowed: {{ALLOW_CMDS_PROSE}}; read-only `git` ({{ALLOW_GIT_SUBCMDS}})
with flags after the subcommand (`git log -p`, `git show -c HEAD`); and
`node` pointed at approved validator scripts, named exactly. Pipelines of
those are fine.

**`git -C` is refused, and it is the first thing you will reach for.**
Global options that take a value — `-C`, `-c`, `--git-dir`, `--work-tree`,
`--namespace`, `--exec-path` — swallow the following token, which moves
where the guard thinks the subcommand is and would let `core.pager` or
`diff.external` name a program to execute. So run git from the project
root and put every flag *after* the subcommand. If your working directory
is not the project root, say so in your report rather than reaching for
`-C`; only `{{GIT_SAFE_GLOBAL_OPTS}}` are accepted before a subcommand.

Denied: anything that writes, anything that mutates git state, arbitrary
interpreters (`python3`, `awk`, `sed -e`, `node -e`, `bash -c`, `xargs`),
package installation, and network access.

The shell metacharacters `$`, `` ` ``, `{`, `}`, `>`, `<`, `&` and newline
are refused anywhere in a command, because bash rewrites a command after
the guard has inspected it — brace expansion was a real bypass here, not a
hypothetical one. That costs some syntax, so use these instead:

- literal braces: `rg -n '\x7b\x7d' src` (with `grep` add `-P`; plain
  `grep '\x7b'` silently matches the letters `x7b`)
- repetition: `rg -n 'aaa?'` rather than `a{2,3}` — not an alternation,
  since `|` is a segment separator here
- jq: `jq .name`, `jq .a.b`, `jq 'with_entries(select(...))'`, `jq 'del(.b)'`

Running the project's own code — its test suite, a package manager, a
service entrypoint — is denied too, and deliberately. Executing
target-controlled code requires an OS-enforced sandbox, and this
environment has none. So when a candidate finding can only be settled by
executing something, do not try: report it with the **needs-validation**
disposition, naming the missing sandbox capability and the safe validation
plan someone with a sandbox should follow.

A guard denial is an answer about your role, not an obstacle; its message
names the workaround where one exists. If you believe a denial was wrong,
say so in your report rather than retrying variants.

Two limits worth knowing, both deliberate: the guard does no path scoping,
so it does not stop you reading outside the repository; and it vouches for
*which* validator script runs, never for what that script does.

## What to review

Priority order:

1. {{ENTRY_SERVICE}} — the externally-reachable surface: request
   validation, auth, rate limiting, dedup. This is the main attack
   surface — everything downstream trusts what this let through.
2. {{OTHER_NETWORK_APIS}} — anything else reachable over the network.
3. Config/secret handling anywhere ({{CONFIG_PATHS}}) — hardcoded secrets,
   overly permissive defaults, secrets written to logs.
4. Idempotency/dedup logic ({{DEDUP_PATHS}}) — replay and forgery
   resistance, not just functional correctness.

Look for: missing/weak authentication, missing authorization checks,
injection (log injection, deserialization of untrusted payloads),
resource-exhaustion (unbounded batch sizes, missing rate limits, unbounded
memory from attacker-controlled cardinality), secrets in code/config/logs,
and trust boundary violations (data crossing from "externally submitted"
to "trusted internal event" without validation).

Do not flag purely theoretical issues with no plausible trigger via the
documented external interface ({{PROTOCOL_DOC}}) — this is a review of
this system's actual attack surface, not a generic checklist.

## Output format

Your final message must be **only** a JSON object, no prose before or
after it:

```json
{
  "findings": [
    {
      "file": "{{EXAMPLE_FILE}}",
      "line": 17,
      "category": "auth-bypass",
      "severity": "high",
      "spec_ref": "section 36",
      "disposition": "confirmed",
      "summary": "One-sentence statement of the vulnerability.",
      "failure_scenario": "Concrete request/payload an attacker sends and what it achieves."
    }
  ]
}
```

- `severity` is one of `low`, `medium`, `high`, `critical`.
- `category` is a short kebab-case slug (`auth-bypass`, `injection`,
  `resource-exhaustion`, `secret-exposure`, `replay`, etc.).
- `disposition` is `confirmed` when you established it by reading the
  code, or `needs-validation` when settling it would require executing
  something. For `needs-validation`, `failure_scenario` must name the
  missing capability and the exact command or experiment a human with a
  sandbox should run.
- Omit `line`/`spec_ref` when not applicable rather than guessing.
- If you find nothing, output `{"findings": []}` — don't manufacture
  low-value findings to have something to say.
- Order findings most-severe first.
````

### 2. Wire the Bash fence (fenced build only)

Skip this entire step for the no-Bash build.

**Copy the hook.** `bash-guard.sh` is a real file in this bundle at
`skills/agent-kit/hooks/bash-guard.sh`. It is not a template — copy it, do
not edit it.

```bash
mkdir -p .claude/hooks
cp path/to/agent-kit-skills/skills/agent-kit/hooks/bash-guard.sh .claude/hooks/
chmod +x .claude/hooks/bash-guard.sh
bash -n .claude/hooks/bash-guard.sh
```

Requires `bash` and `jq`. **Read its header comment before changing any
policy around it.** It is long because it records what each rule is
defending against — brace expansion rewriting an approved command, options
that swallow the following token and move where the guard thinks the
subcommand is, `sed` and `git` and `node` each carrying a way to execute
arbitrary code or write a file. Nearly every rule that looks
over-cautious is there because the obvious version of it was bypassed.

**Wire it in `.claude/settings.json` — not in the agent file:**

````json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "SCOPE_AGENT_TYPES='security-auditor' ALLOW_CMDS='{{ALLOW_CMDS}}' ALLOW_GIT_SUBCMDS='{{ALLOW_GIT_SUBCMDS}}' ALLOW_NODE_SCRIPTS='{{ALLOW_NODE_SCRIPTS}}' ${CLAUDE_PROJECT_DIR}/.claude/hooks/bash-guard.sh"
          }
        ]
      }
    ]
  }
}
````

Merge this entry into the file's existing `PreToolUse` array rather than
replacing it.

The guard is **inert unless configured**, and that is its dangerous
property. An empty `ALLOW_CMDS` exits 0. An unmatched `agent_type` exits
0. A hook wired in agent frontmatter does not run at all in some
environments. From the outside all three are indistinguishable from a
working guard, because the command simply succeeds. Reading the settings
file is not enough either — it does not tell you the hook was reached.

### 3. Verify

Dispatch the agent and have it actually attempt these. For the fenced
build the first check is not optional: this guard was reviewed nine times
in its home project before anyone noticed it had never once been invoked,
and the smoke test that caught it was a `sed` command succeeding with
`sed` absent from `ALLOW_CMDS`.

- **Fenced build:** run a command whose name is absent from `ALLOW_CMDS`
  and read the refusal. **It must say `bash guard:`.** If it says
  something about "allowed working directories", that is the platform
  sandbox and this guard is not running. If it simply succeeds, the fence
  is missing — treat that as the finding, not as a pass.
- **Fenced build:** confirm an allowed command still works
  (`git log --oneline -5` — without `-C`, from the project root), and that
  a write attempt (`git commit`, a redirect) is denied.
- **Fenced build:** confirm the top-level session's own Bash is
  unaffected. If it is fenced too, `SCOPE_AGENT_TYPES` is missing.
- The **last JSON object** in the agent's final message parses with `jq`.
- Its findings name a concrete request or payload in `failure_scenario` —
  a finding that cannot describe how it is triggered is the
  generic-checklist failure mode this prompt exists to prevent.
- **No-Bash build:** confirm it has no Bash — it must not claim to have
  run any scanner.

## Adapt to this project

| Placeholder | Meaning | Common value |
| --- | --- | --- |
| `{{PROJECT}}` | Project name | — |
| `{{BOUNDARY_NAME}}` | What you call the untrusted edge | "ingestion boundary", "public API" |
| `{{SECURITY_SPEC_REF}}` | The spec's security section | `docs/spec/…#security` |
| `{{ENTRY_SERVICE}}` | The externally-reachable service/dir | `services/ingest/` |
| `{{OTHER_NETWORK_APIS}}` | Secondary reachable surfaces | read APIs, admin endpoints |
| `{{CONFIG_PATHS}}` | Config/secret handling | `core/config`, `.env.example`, `deploy/` |
| `{{DEDUP_PATHS}}` | Idempotency/replay logic, or delete | — |
| `{{PROTOCOL_DOC}}` | The documented external interface | `docs/protocol/…` |
| `{{EXAMPLE_FILE}}` | A real path, so the shape is unambiguous | — |
| `{{SECURITY_METHODOLOGY_SKILL}}` | A vendored methodology skill, or delete | `security-audit` |
| `{{SECURITY_METHODOLOGY_PATH}}` | Where it is vendored | `.claude/skills/security-audit/` |
| `{{ALLOW_CMDS}}` | Fenced build: permitted command names | `ls cat head tail wc stat find grep rg jq diff cmp git` |
| `{{ALLOW_CMDS_PROSE}}` | The same list, written out in the prompt | — |
| `{{ALLOW_GIT_SUBCMDS}}` | Fenced build: read-only git subcommands | `log show diff status ls-files ls-tree cat-file blame rev-parse rev-list shortlog grep describe` |
| `{{GIT_SAFE_GLOBAL_OPTS}}` | The valueless git global options the guard accepts; copy from `bash-guard.sh`'s default | `--no-pager --bare --literal-pathspecs --icase-pathspecs --no-replace-objects --no-optional-locks -h` |
| `{{ALLOW_NODE_SCRIPTS}}` | Exact validator scripts `node` may run, or drop `node` from `ALLOW_CMDS` | — |

If the project has no network surface at all, the priority list becomes
the untrusted inputs it *does* have: files it parses, arguments it takes,
archives it extracts, dependencies it resolves. Keep the ordering
principle — nearest to untrusted input first — and keep the
no-theoretical-findings rule.

`severity` here has four levels, one more than `reviewer`. That is
deliberate: `critical` means "reachable now, by anyone, with real impact",
and a session can escalate on it without reading the prose.

Keep `{{ALLOW_CMDS}}` as small as the audit actually needs. `sed`, `sort`
and `file` are deliberately absent from the recommended list even though
the guard has rules for them — each carries an option or a script language
that can write files, and the rules exist for projects that need them
anyway, not as an endorsement. Every name you add is a program whose full
option surface the guard now has to be right about.

## Pick a model

Security review rewards a strong model: the failure mode of a weak one is
a plausible-looking list of non-issues that costs more to triage than it
saves. Pin one with a `model:` line and say in the agent file *why*, so a
later reader does not "fix" the inconsistency with the other agents.

## Parsing the output: extract, don't assume

The JSON-only instruction is a strong default, not a guarantee. In
testing, both read-only agents prefixed their JSON with a paragraph — one
summarising its reasoning, one flagging suspicious content it had read and
ignored. Both were behaving sensibly; neither produced a message that
`jq` could parse whole.

So the session must **extract the last JSON object in the final message**
and parse that, rather than feeding the whole message to `jq`. Treat a
message with no parseable JSON object as a dispatch failure and re-run it;
do not fall back to reading the prose, because the point of the contract
is that the session decides mechanically.

## Using the output

Same as `reviewer`: parse the JSON, turn each finding into a `coder` brief
(or an `architect` brief when the interface itself is unsafe), re-audit
after the fix. A `critical` finding should block the merge, not join a
backlog.

`needs-validation` findings are not a backlog either. They are the ones a
human has to settle, and they are worth surfacing separately from the
confirmed set rather than being sorted in by severity — a session that
treats them as low-confidence noise throws away exactly the findings the
agent was honest about.

## How it fits the rest of the kit

```text
coder ──diff──▶ security-auditor ──▶ findings JSON ──▶ session ──▶ coder / architect
                       │
        (fenced-Bash build only) ──▶ supervisor ──▶ any finding stops everything
```

In the **no-Bash** build, `security-auditor` needs no `supervisor`
pairing, for the same reason `reviewer` does not: no write access, no
execution, nothing to exceed. In the **fenced-Bash** build it is paired
like the agents that can write — see `agent-kit-supervisor`, whose
`CLAUDE.md` protocol must name it explicitly, or it quietly escapes
coverage.
