# Plan 015: Gate full request-parameter logging behind an env flag

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
> `git diff --stat adbae97..HEAD -- pkg/handler/ pkg/server/server.go`
> Line numbers below may shift if plans 009/011/012/014 landed first —
> re-grep for the pattern (Step 1's grep is authoritative); the fix is
> mechanical either way.

## Status

- **Priority**: P2
- **Effort**: S (mechanical, ~30 sites)
- **Risk**: LOW
- **Depends on**: none, but EXECUTE AFTER plans 009, 011, 012, 014 if all are
  selected — they edit the same handler files and this plan touches every
  handler, so doing it last minimizes merge friction.
- **Category**: security (data exposure in logs)
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

Every tool handler's first line logs the **complete request parameters** with
`zap.Any("params", request.Params)` at Debug level. Params routinely contain
message text being posted (`conversations_add_message` payloads), search
queries, and channel/user identifiers. Anyone running with
`SLACK_MCP_LOG_LEVEL=debug` — the natural first move when debugging — writes
Slack message content into local log files indefinitely.

The codebase already solved this problem once: the HTTP transport middleware
gates request-body logging behind a dedicated
`SLACK_MCP_LOG_PARAMS=debug` opt-in (`pkg/server/server.go:920-935`). The
handlers just never adopted the same gate. This plan routes all ~30 handler
sites through one helper honoring that same env var.

## Current state

At commit `adbae97` there are 30 ungated sites, all of the form:

```go
	uh.logger.Debug("usergroups_list called", zap.Any("params", request.Params))
```

Authoritative list (file:line at planning time; re-grep at execution):

- `pkg/handler/activity.go`: 39, 198
- `pkg/handler/conversations.go`: 211, 271, 354, 412, 448, 486, 568, 633, 725, 820, 865, 906, 964, 1692, 1737, 1765, 2142
- `pkg/handler/saved.go`: 38, 193, 238
- `pkg/handler/channels.go`: 54, 393
- `pkg/handler/usergroups.go`: 71, 113, 155, 208, 246

The existing middleware gate to mirror (`pkg/server/server.go:920-935`,
abridged):

```go
	logParams := strings.EqualFold(os.Getenv("SLACK_MCP_LOG_PARAMS"), "debug")
	...
	if logParams {
		fields = append(fields, zap.ByteString("body", bodyBytes))
	}
```

Handler struct shape: each handler holds a `logger *zap.Logger` field (e.g.
`ConversationsHandler`, `pkg/handler/conversations.go` around `:150`;
`UsergroupsHandler`, `pkg/handler/usergroups.go` near the top). There is no
shared handler base type — a package-level helper function in `pkg/handler`
is the right home.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Find all sites | `grep -rn 'zap.Any("params", request.Params)' pkg/` | 30 matches pre-change, 0 post-change |
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnitLogToolCall' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- One new helper (suggested: `pkg/handler/logging.go`) + its test file.
- The 30 call sites listed above (mechanical replacement).

**Out of scope**:
- The middleware gate in `server.go` (already correct — do not touch).
- Any other `zap.Any`/`zap.String` log fields in handlers (some log
  individual derived values like a channel ID — those are fine).
- Log *levels* generally; this is only about the params payload.
- Token/auth logging (none found in handlers at planning time).

## Git workflow

- Branch: `advisor/015-param-logging-gate`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 1: Confirm the site list

```
grep -rn 'zap.Any("params", request.Params)' pkg/
```

Expect ~30 matches in the five files above (count may differ slightly if
other plans landed; the grep is authoritative, the list above is a guide).

### Step 2: Add the helper

Create `pkg/handler/logging.go`:

```go
package handler

// logToolCall logs a tool invocation. The full request params are included
// only when SLACK_MCP_LOG_PARAMS=debug, mirroring the HTTP middleware gate
// in pkg/server — params can contain message text and search queries.
func logToolCall(logger *zap.Logger, event string, request mcp.CallToolRequest) {
	if strings.EqualFold(os.Getenv("SLACK_MCP_LOG_PARAMS"), "debug") {
		logger.Debug(event, zap.Any("params", request.Params))
		return
	}
	logger.Debug(event)
}
```

Read the env var per call (do NOT cache in a package var) — that keeps it
testable with `t.Setenv` and consistent with the middleware, which also reads
per request. Match import grouping style of neighboring files.

**Verify**: `go build ./...` → exit 0

### Step 3: Replace all sites

Each site

```go
	xh.logger.Debug("<event>", zap.Any("params", request.Params))
```

becomes

```go
	logToolCall(xh.logger, "<event>", request)
```

keeping each site's existing event string exactly. Remove now-unused imports
if `gofmt`/compiler flags them (zap likely remains used elsewhere in every
file; check per file).

**Verify**: `grep -rn 'zap.Any("params", request.Params)' pkg/` → no matches; `go build ./...` → exit 0

### Step 4: Test

`pkg/handler/logging_test.go`, `TestUnitLogToolCall`: use
`zap.New(zapcore.NewCore(...))` with an `observer` core
(`go.uber.org/zap/zaptest/observer`, already in the module graph via zap) to
capture entries. Cases:

- `t.Setenv("SLACK_MCP_LOG_PARAMS", "debug")` → entry has a `params` field.
- Env unset → entry logged, NO `params` field.
- `"DEBUG"` (case variance) → gated open (EqualFold).

**Verify**: `go test -count=1 -run 'TestUnitLogToolCall' ./pkg/handler/` → pass

### Step 5: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 4 covers the gate's three states. No per-handler tests needed — the
replacement is mechanical and the compiler enforces it.

## Done criteria

- [ ] `make test` exits 0; `TestUnitLogToolCall` passes
- [ ] `grep -rn 'zap.Any("params", request.Params)' pkg/` → 0 matches
- [ ] Helper reads the env var per call (read the diff)
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- The grep in Step 1 returns sites in files other than the five listed —
  include them (same mechanical change) but note it in your report.
- A handler logs params at a level other than Debug or with a different
  field name — report that site rather than guessing; it may be intentional.
- `zaptest/observer` is somehow unavailable — fall back to a custom
  `zapcore.Core` capture; do not add a new dependency.

## Maintenance notes

- New handlers should call `logToolCall` — plan 021's docs update can add one
  line to AGENTS.md's conventions section noting this (flag it in your report).
- Reviewer: spot-check ~5 replaced sites for event-string fidelity; verify no
  site was missed (`grep` in done criteria) and no *other* params-shaped
  logging was introduced.
- `SLACK_MCP_LOG_PARAMS` is already documented for the middleware in
  `docs/03-configuration-and-usage.md`; its meaning now extends to handlers —
  plan 021 should reflect that.
