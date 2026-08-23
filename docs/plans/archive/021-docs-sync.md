# Plan 021: Sync AGENTS.md and .env.dist with reality (tool count, gates, key names)

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `adbae97` or a descendant (`git merge-base --is-ancestor adbae97 HEAD && echo ok`); otherwise STOP.
>
> **Sequencing**: run this LAST among selected plans — it documents what the
> others changed. In particular, AFTER plan 014 (new gate env vars), 016
> (opt-out var), 018 (Makefile targets — it updates AGENTS.md's commands
> section itself; don't duplicate). Every count and list below must be
> **re-derived from the code at execution time**, not copied from this plan.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: NONE (docs only)
- **Depends on**: 014, 016, 018 (order-only; still valid if some were skipped)
- **Category**: docs
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

AGENTS.md is the file agents (including plan executors) read first, and it
is wrong in ways that cause real mistakes: it disagrees with itself on the
tool count, omits several write-gate env vars (an agent auditing "what can
mutate Slack here" gets an incomplete answer), and `.env.dist` seeds new
setups with a deprecated key name. Docs that lie are worse than no docs.

## Current state (at `adbae97` — RE-VERIFY EVERYTHING at execution)

- **Tool count contradiction**: `AGENTS.md:31` says 30 tools (correct at
  planning); `AGENTS.md:102` says "29 MCP tools". Source of truth:
  `ValidToolNames` in `pkg/server/server.go:65-96`. Count it at execution
  (`grep -c 'Tool' ...` is unreliable — count the slice entries). If the
  unmerged plan-004 branch (single-message fetch tool) has merged, the
  number is 31.
- **Gate list incomplete**: `AGENTS.md:48-52` lists write-gates
  `SLACK_MCP_ADD_MESSAGE_TOOL`, `SLACK_MCP_REACTION_TOOL`,
  `SLACK_MCP_ATTACHMENT_TOOL` but omits `SLACK_MCP_FILES_LIST_TOOL`
  (`server.go:409`) and `SLACK_MCP_MARK_TOOL`
  (`conversations.go:2438`), plus — if plan 014 landed —
  `SLACK_MCP_USERGROUPS_WRITE_TOOL` and `SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL`.
  Source of truth: `grep -rn 'SLACK_MCP_[A-Z_]*_TOOL' pkg/ | grep -v _test`.
- **Tool list omission**: `AGENTS.md:64`'s tool enumeration omits
  `channels_starred` (present in `ValidToolNames`).
- **.env.dist stale**: uses deprecated `SLACK_MCP_SSE_API_KEY`; the code
  prefers `SLACK_MCP_API_KEY` (`pkg/server/auth/sse_auth.go:23-28`). It also
  lists no write-gate vars at all, so a new setup has no prompt to think
  about them.
- Related, if plans landed: 015 introduced `logToolCall` convention +
  extended `SLACK_MCP_LOG_PARAMS` meaning; 016 introduced
  `SLACK_MCP_ALLOW_UNAUTHENTICATED` (016 already updated
  `docs/03-configuration-and-usage.md`; AGENTS.md may deserve one line).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Derive tool count | read `ValidToolNames` in `pkg/server/server.go` | one number, used everywhere |
| Derive gate list | `grep -rn 'SLACK_MCP_[A-Z_]*_TOOL' pkg/ \| grep -v _test \| grep -v ENABLED_TOOLS` | the authoritative env-var set |
| Consistency | `grep -n '29\|30\|31' AGENTS.md` | every count matches the derived number |
| Build sanity | `make test` | exit 0 (docs can't break it; run anyway) |

## Scope

**In scope**:
- `AGENTS.md` (counts, gate list, tool list; one-line conventions additions)
- `.env.dist`

**Out of scope**:
- `README.md` and `docs/` (upstream-shaped; 016 already touched the one
  section that needed it; don't restyle upstream docs on the fork).
- `CONCEPTS.md`, `plans/` content.
- Any code.

## Git workflow

- Branch: `advisor/021-docs-sync`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 1: Derive ground truth

Run the derivation commands above. Write down: the tool count, the full
tool-name list, the full gate-var list. These derived values override every
number in this plan.

### Step 2: AGENTS.md

- Fix the count at `:102` (and re-check `:31` and anywhere else the grep
  finds a count) to the derived number.
- Gate section (`:48-52` area): replace with the complete derived list, one
  line each, stating what each gates. Keep the section's existing formatting
  style.
- Tool enumeration (`:64` area): add `channels_starred` and any other name
  present in `ValidToolNames` but missing from the doc (diff the two lists
  both directions — also remove doc entries for tools that don't exist).
- If plan 015 landed: one line in the conventions section — "new handlers
  log via `logToolCall`; full params only under `SLACK_MCP_LOG_PARAMS=debug`".
- If plan 016 landed: one line noting network transports require
  `SLACK_MCP_API_KEY` at startup.

**Verify**: `grep -n '29' AGENTS.md` → no stale-count matches; visually diff the section

### Step 3: .env.dist

- Replace `SLACK_MCP_SSE_API_KEY` with `SLACK_MCP_API_KEY` (keep any
  explanatory comment, updated).
- Add a commented block of the write-gate vars from Step 1, all commented
  out (defaults stay off — that's the security model), each with a
  half-line description.

**Verify**: `grep -n 'SSE_API_KEY' .env.dist` → no matches; `grep -c 'SLACK_MCP' .env.dist` grew by the gate count

### Step 4: Cross-check and suite

Re-run the consistency grep; run `make test` (should be untouched-green).

**Verify**: `make test` → exit 0

## Test plan

Docs plan — the greps in Steps 2–3 are the machine checks.

## Done criteria

- [ ] AGENTS.md contains exactly one tool count, equal to `len(ValidToolNames)`
- [ ] Every `SLACK_MCP_*_TOOL` gate var found in code appears in AGENTS.md
- [ ] AGENTS.md tool list == `ValidToolNames` (both directions)
- [ ] `.env.dist` uses `SLACK_MCP_API_KEY` and lists the gates (commented)
- [ ] `git status` shows only `AGENTS.md` and `.env.dist` modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- The derived tool count differs from BOTH 30 and 31 — something structural
  changed; report what you found before writing numbers.
- `.env.dist` doesn't exist or has been replaced by another mechanism —
  report.
- You find gate vars in code that gate NON-write tools in surprising ways —
  document what the code does, don't editorialize the security model.

## Maintenance notes

- This drifts again by design — any plan that adds a tool or gate var
  should touch AGENTS.md in the same diff. Reviewer: consider asking for
  that line in future plan templates.
- Deliberately NOT added: a doc for every env var (docs/03 already does
  that); AGENTS.md stays a lean agent-orientation file.
