#!/usr/bin/env bash
# Generic PreToolUse path guard shared by this project's subagents
# (.claude/agents/*.md). One hook entry carries one agent's policy, set as
# env vars inline on the hook's own command line, so the same script is
# parametrized per-agent instead of duplicating the logic five times. See
# WIRING below for where that entry goes -- it is not the agent file.
#
# Env vars. The three glob lists are space-separated and use
# `[[ str == pattern ]]` semantics, so a bare `*` in a pattern matches
# across `/` too — "docs/*" matches "docs/spec/hammertime_spec_1.md":
#
#   EXEMPT_GLOBS      - path matching any of these is always ALLOWED,
#                       checked before DENY_GLOBS/ALLOW_GLOBS.
#   DENY_GLOBS        - path matching any of these (and not exempt) is
#                       DENIED.
#   ALLOW_GLOBS       - if set, a path that is not exempt/already-denied
#                       must match at least one of these or it is DENIED
#                       (allowlist mode). Leave unset for denylist-only
#                       mode.
#   SCOPE_AGENT_TYPES - agent types this policy applies to, matched
#                       against the payload's `agent_type`. A list of
#                       exact names, not globs. Unset or empty polices
#                       every call that reaches this hook (the historical
#                       behaviour). Set polices only the listed agents and
#                       passes every other caller through untouched. See
#                       WIRING below -- this exists because the hook has
#                       to be installed session-wide.
#
# Reads the PreToolUse JSON payload on stdin (see
# https://code.claude.com/docs/en/hooks) and checks tool_input.file_path,
# falling back to tool_input.path (Grep/Glob).
#
# WIRING -- read this before believing the guard is doing anything.
#
# This hook must be wired in `.claude/settings.json` (or
# `.claude/settings.local.json`). It must NOT be wired in an agent file's
# `hooks:` frontmatter. `hooks:` is a documented frontmatter field, but
# a guard declared there did not fire in this environment: tested
# three times, including with an absolute script path -- no error, no
# warning, nothing to notice, so the agent ran completely unfenced
# (three probes). The best-supported explanation is the documented
# requirement that a project-level agent's frontmatter hooks run only
# once the workspace trust dialog has been accepted for the folder
# containing the agent file; this session has no trust record for the
# project. That has not been confirmed directly. Either way the
# consequence is the same: whether a frontmatter guard fires depends on
# environment state that is invisible from the repository, so it can
# look enforced on one machine and silently do nothing on another.
# `.claude/settings.json` hooks fired in every test -- see also WIRING
# in bash-guard.sh.
#
# WHERE THE CONFIGURATION IS READ FROM. Hook configuration is read from
# the main project checkout, not from a subagent's worktree. A
# worktree-isolated agent is fenced by whatever the main checkout's
# `.claude/settings.json` contains at dispatch time; the copy in its
# worktree is inert. So to test a policy change, the change must be in
# the main checkout, and a probe dispatched right after editing tests
# the edited policy -- not whatever the worktree has checked out.
# Established by experiment: with the policy removed from the main
# checkout only, while the worktree copy still carried it, the write
# was not denied.
#
# Settings-level hooks are session-wide: they fire for every agent and for
# the top-level session, not only the agent a policy was written for. That
# is what SCOPE_AGENT_TYPES is for. One entry still carries one policy,
# because the env vars come from the hook's own command line, so two
# agents needing different globs need two entries. Two entries whose
# SCOPE_AGENT_TYPES overlap both run, and the stricter one's denial wins,
# since any deny is final.
#
# The scoping is FAIL-OPEN by design: an absent or unlisted agent_type
# means "not my business", not "deny" -- a top-level call carries no
# agent_type at all. It is routing, not a check, so an exit 0 for an
# out-of-scope caller is not approval, only a statement that this policy
# did not apply. See "SCOPING IS FAIL-OPEN BY DESIGN" in bash-guard.sh for
# why a stricter rule would break the session it was installed in without
# being a boundary for anyone.
#
# Unscoped content tools are DENIED for guarded agents. Grep and Glob take
# an optional `path`; without one they search the whole project, and
# Grep's `output_mode: content` then returns matching lines from files the
# guard is supposed to hide. Passing such a call through (as this script
# did before) made the guard advisory rather than enforced. A guarded
# agent must therefore name an in-scope path it wants to search. The same
# applies to a `path` that resolves to the project root itself.
#
# Caveat (documented, not a bug): this only intercepts the tool calls named
# in the subagent's own `matcher` (Edit|Write or Read|Grep|Glob). It does
# NOT inspect Bash commands, so an agent that also has the Bash tool could
# still read or write a guarded path via a shell command. Keep Bash off
# any agent whose guard must be a hard boundary, or treat the guard as a
# strong default rather than a sandbox for agents that keep Bash.

set -f -e -u -o pipefail

input="$(cat)"
tool_name="$(printf '%s' "$input" | jq -r '.tool_name // empty')"
file_path="$(printf '%s' "$input" | jq -r '.tool_input.file_path // .tool_input.path // empty')"
cwd="$(printf '%s' "$input" | jq -r '.cwd // empty')"
agent_type="$(printf '%s' "$input" | jq -r '.agent_type // empty')"

# Scope routing -- deliberately NOT a check. This hook has to be wired
# session-wide (see WIRING in the header), so it sees tool calls from
# callers this policy was never written for. When SCOPE_AGENT_TYPES is
# set, only the listed agents are policed and everyone else passes
# through untouched: an absent agent_type (a top-level call carries none)
# or one that is not on the list means "not my business", not "deny".
# Unset behaves as it always has and polices every call that reaches this
# hook, which is what the test suite exercises.
#
# This test deliberately sits first, before `guarded` is even worked out,
# so that an out-of-scope caller costs nothing and cannot be affected by
# this policy's configuration.
if [[ -n "${SCOPE_AGENT_TYPES:-}" ]]; then
  in_scope=0
  if [[ -n "$agent_type" ]]; then
    for scoped_agent in $SCOPE_AGENT_TYPES; do
      if [[ "$agent_type" == "$scoped_agent" ]]; then
        in_scope=1
        break
      fi
    done
  fi
  if (( ! in_scope )); then
    exit 0
  fi
fi

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
    deny "path guard: an unscoped ${tool_name} would search the whole project and can return files this agent may not read. Re-run it with an explicit in-scope 'path' (for example 'docs', 'schemas', or a specific tests directory)."
  fi
  exit 0
fi

# Normalize to a path relative to the project/worktree root when possible.
# Note the exact-match arm: without it, a path equal to the root itself
# fell through with `rel` still absolute and matched no glob at all.
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
