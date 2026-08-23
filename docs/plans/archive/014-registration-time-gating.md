# Plan 014: Gate all mutating tools at registration time and use exact-match enabled-tools checks

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
> **Drift check (run first)**:
> `git diff --stat adbae97..HEAD -- pkg/server/server.go pkg/handler/conversations.go pkg/handler/usergroups.go`
> NOTE: unmerged branch `advisor/004-single-message-fetch-tool` also edits
> `server.go` (adds a 31st tool). If merged since planning, locate code by
> excerpt, expect `TestValidToolNames` to already list 31 names, and adjust
> counts accordingly.

## Status

- **Priority**: P1
- **Effort**: S–M
- **Risk**: LOW code risk; **deliberate behavior change** — see "Deployment impact"
- **Depends on**: none. Land BEFORE plan 021 (docs) which documents the env-gate list.
- **Category**: security
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

The repo's stated safety model is "mutating tools are opt-in via env vars"
(`SLACK_MCP_ADD_MESSAGE_TOOL`, `SLACK_MCP_REACTION_TOOL`, …). Three classes
of tools violate it:

1. `usergroups_create`, `usergroups_update`, `usergroups_users_update` —
   workspace-visible mutations (who gets paged by `@group`) — register with
   **no gate** and no call-time check. On by default.
2. `conversations_leave`, `conversations_join` — membership mutations —
   likewise ungated.
3. `conversations_mark` gates **only at call time**: it always appears in
   `tools/list`, so agents pick it, call it, and get an error — a wasted
   round trip and reasoning step in a fork built for agent efficiency.

Separately, three call-time checks test tool enablement with
`strings.Contains` on the raw `SLACK_MCP_ENABLED_TOOLS` string instead of the
exact-match helper that exists for precisely this purpose — a fail-open parse
on the gates the project most wants closed.

## Deployment impact (behavior change — intentional)

After this plan, `usergroups_create/update/users_update`,
`conversations_join/leave`, and `conversations_mark` do NOT register unless
either (a) their env var is set, or (b) they are explicitly named in the
`SLACK_MCP_ENABLED_TOOLS` allowlist. This machine's Plug deployment uses
`SLACK_MCP_ENABLED_TOOLS` as an allowlist, so behavior there only changes if
those names are in the list without the intent to use them. Call this out
prominently in your completion report so the maintainer updates Plug's env if
needed.

## Current state

`pkg/server/server.go` at commit `adbae97`:

The gate mechanism — `shouldAddTool(name, enabledTools, envVarName)`:

```go
// server.go:118-135
func shouldAddTool(name string, enabledTools []string, envVarName string) bool {
	if envVarName == "" {
		if len(enabledTools) == 0 { return true }
		return slices.Contains(enabledTools, name)
	}
	if len(enabledTools) > 0 && slices.Contains(enabledTools, name) { return true }
	if len(enabledTools) == 0 { return os.Getenv(envVarName) != "" }
	return false
}
```

Correctly gated examples: `server.go:244` (`SLACK_MCP_ADD_MESSAGE_TOOL`),
`:293`, `:312` (`SLACK_MCP_REACTION_TOOL`), `:331`
(`SLACK_MCP_ATTACHMENT_TOOL`), `:409` (`SLACK_MCP_FILES_LIST_TOOL`).

Ungated registrations (all pass `""` as the env var):

- `server.go:434` — `shouldAddTool(ToolConversationsMark, enabledTools, "")`
- `server.go:449` — `ToolConversationsLeave`
- `server.go:461` — `ToolConversationsJoin`
- `server.go:510` — `ToolUsergroupsCreate`
- `server.go:531` — `ToolUsergroupsUpdate`
- `server.go:555` — `ToolUsergroupsUsersUpdate`

(`ToolUsergroupsList` `:476` and `ToolUsergroupsMe` `:496` are read-only /
self-service — leave them ungated. `usergroups_me` mutates only the caller's
own membership; gating it is a judgment call explicitly left OUT of scope.)

The call-time gate pattern to replicate (from `conversations_mark`):

```go
// pkg/handler/conversations.go:2438-2447
func (ch *ConversationsHandler) parseParamsToolMark(request mcp.CallToolRequest) (*markParams, error) {
	toolConfig := os.Getenv("SLACK_MCP_MARK_TOOL")
	if toolConfig == "" {
		ch.logger.Error("Mark tool disabled by default")
		return nil, errors.New("by default, the conversations_mark tool is disabled ...")
	}
```

The substring-match bug sites in `pkg/handler/conversations.go`:

```go
// :2239   if !strings.Contains(enabledTools, "conversations_add_message") {
// :2335   if !strings.Contains(enabledTools, "reactions_add") && !strings.Contains(enabledTools, "reactions_remove") {
// :2383   if !strings.Contains(enabledTools, "attachment_get_data") {
```

The correct helper, in the same file:

```go
// conversations.go:2177-2187
func isToolInEnabledList(enabledTools, toolName string) bool { ... exact split-and-trim match ... }
```

Existing test patterns: `pkg/server/server_test.go` —
`TestShouldAddTool_WriteTool_AddMessage` (`:233`),
`TestShouldAddTool_WriteTool_Reactions` (`:275`), `TestValidToolNames` (`:96`,
pins the 30 tool names), `TestRegisterCacheDependentTools` (`:333`).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestShouldAddTool|TestValidToolNames' ./pkg/server/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/server/server.go` (the six `shouldAddTool` call sites listed above)
- `pkg/handler/conversations.go` (the three `strings.Contains` sites; join/leave call-time checks)
- `pkg/handler/usergroups.go` (call-time checks in the three write handlers)
- `pkg/server/server_test.go`, `pkg/handler/conversations_test.go`

**Out of scope**:
- `usergroups_me`, `usergroups_list`, `conversations_open` — leave as-is.
- The channel-allowlist syntax of `SLACK_MCP_ADD_MESSAGE_TOOL` — unchanged.
- AGENTS.md / README documentation — plan 021 owns docs (note the new env
  vars in your report so 021's executor includes them).

## Git workflow

- Branch: `advisor/014-registration-time-gating`
- Commits: one per step is fine; imperative subjects. Do NOT push.

## Steps

### Step 1: Registration-time gates

In `server.go`, replace the `""` argument at the six sites:

- `ToolConversationsMark` → `"SLACK_MCP_MARK_TOOL"`
- `ToolConversationsLeave`, `ToolConversationsJoin` → `"SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL"`
- `ToolUsergroupsCreate`, `ToolUsergroupsUpdate`, `ToolUsergroupsUsersUpdate` → `"SLACK_MCP_USERGROUPS_WRITE_TOOL"`

**Verify**: `go build ./...` → exit 0

### Step 2: Call-time checks (defense in depth)

`conversations_mark` already has one. Add the same pattern (env var empty AND
tool not in `SLACK_MCP_ENABLED_TOOLS` via `isToolInEnabledList` → clear
error naming the env var to set) to:

- `UsergroupsCreateHandler`, `UsergroupsUpdateHandler`,
  `UsergroupsUsersUpdateHandler` (`pkg/handler/usergroups.go:112/154/207`) —
  env var `SLACK_MCP_USERGROUPS_WRITE_TOOL`.
- `ConversationsLeaveHandler`, `ConversationsJoinHandler`
  (`pkg/handler/conversations.go`, near `:1737`/`:1765`) — env var
  `SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL`.

Follow the add-message convention of also honoring an explicit
`SLACK_MCP_ENABLED_TOOLS` listing (see `conversations.go:2237-2245`, but use
`isToolInEnabledList`, not `strings.Contains`). Extract a small shared helper
if it keeps the five checks to one line each; put it next to
`isToolInEnabledList`.

**Verify**: `go build ./...` → exit 0

### Step 3: Fix the substring matches

Replace the three `strings.Contains(enabledTools, "...")` conditions at
`conversations.go:2239`, `:2335`, `:2383` with `isToolInEnabledList(...)`
calls (the reactions site checks two names — OR the two helper calls).

**Verify**: `go build ./...` → exit 0

### Step 4: Tests

- `server_test.go`: following the `TestShouldAddTool_WriteTool_*` pattern,
  add matrix tests asserting each of the six tools is ABSENT with no env and
  no allowlist, PRESENT with its env var set, and PRESENT when explicitly
  named in `enabledTools`.
- `conversations_test.go`: table test for `isToolInEnabledList` covering the
  substring-collision case (`"conversations_add_message_v2"` in the list must
  NOT enable `"conversations_add_message"`), empty string, whitespace padding,
  exact match. Also cases asserting the three fixed sites' behavior via their
  parse functions if they are testable with a constructed request (the mark
  parser pattern); otherwise the helper test suffices.
- Run `TestValidToolNames` — it pins names, not registration, so it should
  pass unchanged; if it fails, read why before touching it (see STOP).

**Verify**: `go test -count=1 -run 'TestShouldAddTool|TestValidToolNames|TestUnitIsToolInEnabledList' ./pkg/server/ ./pkg/handler/` → pass

### Step 5: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 4. The load-bearing regressions: (a) six tools absent by default,
(b) substring collision no longer opens a write gate.

## Done criteria

- [ ] `make test` exits 0; new matrix rows pass
- [ ] `grep -n 'strings.Contains(enabledTools' pkg/handler/` → no matches
- [ ] `grep -n 'shouldAddTool(ToolUsergroupsCreate\|shouldAddTool(ToolConversationsMark\|shouldAddTool(ToolConversationsLeave\|shouldAddTool(ToolConversationsJoin' pkg/server/server.go` shows non-empty env-var arguments
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated; completion report names the two new env vars and the deployment impact

## STOP conditions

- Excerpts don't match (drift; especially if plan 004's branch merged —
  expect 31 tools, re-locate by name).
- A default-registration test other than the ones named starts failing in a
  way that implies some *other* code depends on these tools always existing
  (e.g. a preset in `docs/agent-presets.md` referenced from code) — report.
- The join/leave handlers turn out to have no param-parse function to host
  the check — put it at the top of the handler itself; if even that is
  structurally awkward, report.

## Maintenance notes

- Plan 021 (docs) must add `SLACK_MCP_USERGROUPS_WRITE_TOOL` and
  `SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL` to AGENTS.md's write-tool list and
  `.env.dist` — land 014 first.
- Maintainer post-merge: if any of the six tools should stay available under
  Plug, add the env var (or the tool name to `SLACK_MCP_ENABLED_TOOLS`) in
  Plug's config, then `make deploy-local`.
- Reviewer: check `shouldAddTool` semantics were not altered — only call
  sites changed.
