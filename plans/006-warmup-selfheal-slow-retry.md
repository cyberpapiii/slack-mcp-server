# Plan 006: Keep retrying cache warmup on a slow backoff so tools register without a restart

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- cmd/slack-mcp-server/warmup.go pkg/handler/auth_status.go AGENTS.md`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED
- **Depends on**: none
- **Category**: direction
- **Planned at**: commit `adbae97`, 2026-07-02

## Why this matters

Cache warmup tries 3 times, 30 seconds apart, then gives up forever. Because the warmup goroutine is the ONLY caller of `RegisterCacheDependentTools`, a transient network or auth blip during the first ~90 seconds of a process's life permanently disables `channels_list`, `channels_me`, `channels_starred`, `conversations_unreads`, and the `activity_*` tools — the only recovery is manually restarting Plug's slack server. This is documented as a known limitation in AGENTS.md ("process stays alive but cache-dependent tools never register until restart"). The fix: after the 3 fast attempts, keep retrying on a slow interval indefinitely. The server already emits `tools/list_changed` when tools register late, so clients pick them up automatically whenever recovery happens.

## Current state

Relevant files:

- `cmd/slack-mcp-server/warmup.go` — the whole warmup goroutine (97 lines; full file inlined below in relevant part). No tests exist anywhere in `cmd/`.
- `pkg/server/server.go:671-673` — `RegisterCacheDependentTools` is `sync.Once`-guarded, so calling it repeatedly is safe; only the first successful call registers.
- `pkg/handler/auth_status.go:88-95` (`buildAuthSummary`) — user-facing text that currently says recovery requires a restart.
- `AGENTS.md:66-68` — documents the restart-only limitation this plan removes.

The give-up flow (`cmd/slack-mcp-server/warmup.go:13-70`, abbreviated to the load-bearing parts):

```go
const (
	warmupMaxAttempts = 3
	warmupRetryDelay  = 30 * time.Second
)
...
func startCacheWarmup(p *provider.ApiProvider, s *server.MCPServer, logger *zap.Logger) {
	go func() {
		for attempt := 1; attempt <= warmupMaxAttempts; attempt++ {
			if isDemoCredentials() {
				logger.Info("Demo credentials are set, skip cache warm-up", ...)
				return
			}

			refreshUsersCache(p, logger)
			refreshChannelsCache(p, logger)

			ready, err := p.IsReady()
			if ready {
				s.RegisterCacheDependentTools()
				...
				return
			}

			if attempt < warmupMaxAttempts {
				logger.Warn("Cache warm-up incomplete, retrying", ..., zap.Duration("next_retry_in", warmupRetryDelay))
				time.Sleep(warmupRetryDelay)
				continue
			}

			logger.Error("Cache warm-up failed after retries; cache-dependent tools will not be available", ...)
		}
	}()
}
```

The registration guard (`pkg/server/server.go:668-673`) — idempotent by construction:

```go
// RegisterCacheDependentTools registers tools and resources that require the cache to be ready.
// Called after cache warm-up completes. The mcp-go server automatically sends
// notifications/tools/list_changed to connected clients when AddTool is called.
func (s *MCPServer) RegisterCacheDependentTools() {
	s.cacheToolsOnce.Do(s.registerCacheDependentTools)
}
```

The stale summary text (`pkg/handler/auth_status.go`, inside `buildAuthSummary`):

```go
		parts = append(parts, "Cache-dependent tools (channels_list, unreads, activity, saved) may be unavailable until caches warm or Plug is restarted.")
```

Conventions: zap structured logging with `zap.String("context", "console")` on warmup-path logs (match the existing calls); constants grouped in a `const (...)` block; unit tests use testify `assert`/`require` with the `TestUnit` name prefix (see `pkg/handler/*_test.go` — `cmd/` has no test files yet, this plan creates the first).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Tests | `go test -count=1 -skip="Integration" ./...` | all pass |
| Format check | `gofmt -l pkg/ cmd/` | no output |

## Scope

**In scope** (the only files you should modify):
- `cmd/slack-mcp-server/warmup.go`
- `cmd/slack-mcp-server/warmup_test.go` (create)
- `pkg/handler/auth_status.go` (one string literal only)
- `AGENTS.md` (the limitation paragraph)
- `plans/README.md` — status row

**Out of scope** (do NOT touch, even though they look related):
- `pkg/server/server.go` and `pkg/server/tool_phases.go` — the `sync.Once` guard and phase registry are exactly what make repeated `RegisterCacheDependentTools` calls safe; do not restructure them.
- `pkg/provider/api.go` — `RefreshUsers`/`RefreshChannels`/`IsReady` behavior stays as-is.
- `slack_auth_status` behavior beyond the one summary string — do NOT make the status tool trigger warmup (rejected: it would add a second concurrent caller of the refresh path and complicate the goroutine story for marginal benefit).

## Git workflow

- Branch: `advisor/006-warmup-selfheal-slow-retry`
- Commit style: imperative summary line, body explaining why (see `git log --oneline -10`).
- Do NOT push or open a PR. This fork never pushes to origin.

## Steps

### Step 1: Extract the retry-delay policy as a pure function

In `cmd/slack-mcp-server/warmup.go`, add to the `const` block and add the policy function:

```go
const (
	warmupMaxAttempts    = 3
	warmupRetryDelay     = 30 * time.Second
	warmupSlowRetryDelay = 5 * time.Minute
)

// warmupNextDelay returns how long to wait before the given (1-based) next
// attempt. The first warmupMaxAttempts attempts are fast; afterwards the
// loop degrades to a slow indefinite retry so a transient startup failure
// doesn't permanently disable cache-dependent tools.
func warmupNextDelay(nextAttempt int) time.Duration {
	if nextAttempt <= warmupMaxAttempts {
		return warmupRetryDelay
	}
	return warmupSlowRetryDelay
}
```

**Verify**: `go build ./...` → exit 0.

### Step 2: Convert the bounded loop to fast-then-slow indefinite retries

Replace the `for attempt := 1; attempt <= warmupMaxAttempts; attempt++` loop body flow with an unbounded loop that:

1. Keeps the `isDemoCredentials()` early return, the two refresh calls, and the ready → `RegisterCacheDependentTools()` → return path exactly as they are (including the success log messages).
2. On not-ready: if the *completed* attempt count is `< warmupMaxAttempts`, log the existing `Warn("Cache warm-up incomplete, retrying", ...)`.
3. When attempt count reaches exactly `warmupMaxAttempts`, log ONCE (keep severity Error and keep the existing message string so log-watchers still match, but append the new behavior):

```go
logger.Error("Cache warm-up failed after retries; cache-dependent tools will not be available until a background retry succeeds",
	zap.String("context", "console"),
	zap.Int("attempts", warmupMaxAttempts),
	zap.Duration("slow_retry_every", warmupSlowRetryDelay),
	zap.Error(err),
)
```

4. For attempts beyond `warmupMaxAttempts`, log each retry at Info (not Warn/Error — this can run for hours; don't spam):

```go
logger.Info("Background cache warm-up retry",
	zap.String("context", "console"),
	zap.Int("attempt", attempt),
	zap.Error(err),
)
```

5. Sleep `warmupNextDelay(attempt + 1)` and continue. The loop only exits via the demo-credentials return or the ready return.

Shape (for orientation — adapt, keeping existing log calls):

```go
	go func() {
		for attempt := 1; ; attempt++ {
			if isDemoCredentials() { ... return }
			refreshUsersCache(p, logger)
			refreshChannelsCache(p, logger)
			ready, err := p.IsReady()
			if ready { s.RegisterCacheDependentTools(); ...; return }
			switch {
			case attempt < warmupMaxAttempts:
				logger.Warn("Cache warm-up incomplete, retrying", ...)
			case attempt == warmupMaxAttempts:
				logger.Error("Cache warm-up failed after retries; ...", ...)
			default:
				logger.Info("Background cache warm-up retry", ...)
			}
			time.Sleep(warmupNextDelay(attempt + 1))
		}
	}()
```

**Verify**: `go build ./... && go vet ./...` → exit 0.

### Step 3: Update the auth-status summary string

In `pkg/handler/auth_status.go` (`buildAuthSummary`), change:

```go
"Cache-dependent tools (channels_list, unreads, activity, saved) may be unavailable until caches warm or Plug is restarted."
```

to:

```go
"Cache-dependent tools (channels_list, unreads, activity, saved) are unavailable until caches warm; the server keeps retrying in the background and registers them automatically on success."
```

Check for tests asserting the old string: `grep -rn "or Plug is restarted" pkg/` — if a test asserts it, update the expectation to the new string (that is in scope; changing test *purpose* is not).

**Verify**: `go build ./... && go test -count=1 -skip="Integration" ./pkg/handler/` → all pass.

### Step 4: Unit tests for the policy

Create `cmd/slack-mcp-server/warmup_test.go` (package `main`):

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUnitWarmupNextDelay(t *testing.T) {
	// attempts 2..warmupMaxAttempts are fast retries
	assert.Equal(t, 30*time.Second, warmupNextDelay(2))
	assert.Equal(t, 30*time.Second, warmupNextDelay(warmupMaxAttempts))
	// beyond the fast window, slow indefinite retries
	assert.Equal(t, 5*time.Minute, warmupNextDelay(warmupMaxAttempts+1))
	assert.Equal(t, 5*time.Minute, warmupNextDelay(100))
}
```

**Verify**: `go test -count=1 -skip="Integration" ./cmd/...` → passes, 1 new test.

### Step 5: Update AGENTS.md

Replace the limitation sentence (around line 66):

> If users or channels cache warm-up fails after retries, the process stays alive but cache-dependent tools never register until restart. Restart Plug's slack server (or the plug daemon) after fixing auth or network issues.

with:

> If users or channels cache warm-up fails after the 3 fast attempts, the server keeps retrying in the background every 5 minutes and registers cache-dependent tools automatically once a retry succeeds (clients are notified via `tools/list_changed`). Restarting Plug's slack server is only needed to force an immediate retry.

**Verify**: `grep -n "never register until restart" AGENTS.md` → no matches.

## Test plan

- `TestUnitWarmupNextDelay` (new, `cmd/slack-mcp-server/warmup_test.go`) — fast window boundary and slow tail; the first test in `cmd/`.
- Any existing test asserting the old auth-summary string updated (Step 3).
- The goroutine loop itself is not unit-tested (it sleeps for real; wrapping time for a personal fork isn't worth the machinery) — the policy function carries the testable logic. Structural pattern: testify + `TestUnit` prefix as in `pkg/handler/`.
- Verification: `go test -count=1 -skip="Integration" ./...` → all pass.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `go test -count=1 -skip="Integration" ./...` exits 0, including `TestUnitWarmupNextDelay`
- [ ] `gofmt -l pkg/ cmd/` prints nothing
- [ ] `grep -n "warmupSlowRetryDelay" cmd/slack-mcp-server/warmup.go` ≥ 2 matches (const + use)
- [ ] `grep -rn "attempt <= warmupMaxAttempts; attempt++" cmd/` → no matches (bounded loop gone)
- [ ] `grep -rn "or Plug is restarted" pkg/handler/auth_status.go` → no matches
- [ ] No files outside the in-scope list modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The drift check shows in-scope files changed and the excerpts no longer match.
- `RegisterCacheDependentTools` is no longer `sync.Once`-guarded (`pkg/server/server.go:671-673`) — the indefinite loop's safety depends on that idempotency.
- You find a second caller of `startCacheWarmup` or of `RegisterCacheDependentTools` outside `warmup.go` — the single-goroutine assumption would be false.
- `refreshUsersCache`/`refreshChannelsCache` turn out to hold locks or block indefinitely on failure (read `pkg/provider/api.go`'s `RefreshUsers`/`RefreshChannels` if a test hangs) — a 5-minute loop over a blocking call needs a different design.
- A step's verification fails twice after a reasonable fix attempt.

## Maintenance notes

- The retry goroutine now lives for the process lifetime when Slack is unreachable. Each retry hits `users.list`/`conversations.list`; at 5-minute intervals this is well inside rate limits, but if the interval is ever shortened, check the provider's limiter usage first.
- If a future change adds a manual "re-warm now" trigger (e.g. via `slack_auth_status`), it must coordinate with this goroutine — that design was considered and deliberately deferred to keep a single warmup owner.
- Reviewer scrutiny: log levels in the slow tail (must be Info, not Error, to avoid multi-hour log spam) and that the demo-credentials early return still precedes any network call.
- Maintainer's post-merge live check: hard to simulate a warmup failure locally; at minimum `make deploy-local` and confirm normal startup still logs "Slack MCP Server is fully ready" and the tools appear (per AGENTS.md live-verification preference).
