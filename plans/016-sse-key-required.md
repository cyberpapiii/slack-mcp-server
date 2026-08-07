# Plan 016: Refuse to start network transports without an API key (explicit opt-out only)

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
> `git diff --stat adbae97..HEAD -- pkg/server/auth/sse_auth.go cmd/slack-mcp-server/main.go docs/03-configuration-and-usage.md`
> On any change, locate code by the excerpts below; unlocatable = STOP.

## Status

- **Priority**: P2 (conditional: this machine deploys stdio-only under Plug,
  so the exposure is latent, not live — but the failure mode is severe)
- **Effort**: S
- **Risk**: LOW code risk; **breaking change** for anyone running sse/http
  without a key (that break is the point)
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

When the server runs with `--transport sse` or `http` and no API key env var
is set, **authentication silently passes for every request**, and the only
trace is a Debug-level log line most configs never show. The server fronts a
Slack browser token (xoxc/xoxd) that can read DMs and post as the user; the
docs actively suggest exposing SSE via ngrok
(`docs/03-configuration-and-usage.md:216-222`). "Forgot to set the key while
following the ngrok section" currently equals "handed my Slack session to
the internet, silently."

Fail closed: no key → refuse to start, unless the operator sets an explicit
`SLACK_MCP_ALLOW_UNAUTHENTICATED=true` opt-out (dev loopback use), which
logs a prominent Warn. The default host is `127.0.0.1`, which bounds today's
default-config exposure — this plan is about removing the silent failure
mode, not fixing an active hole in the user's deployment.

## Current state

**Corrected 2026-08-07** — the first version of this plan described a
`ValidateSSEAPIKey` HTTP middleware that does not exist. The real
architecture, verified at `adbae97`:

`pkg/server/auth/sse_auth.go` — the silent pass lives in `validateToken`:

```go
// sse_auth.go:25-40
func validateToken(ctx context.Context, logger *zap.Logger) (bool, error) {
	// no configured token means no authentication
	keyA := os.Getenv("SLACK_MCP_API_KEY")
	if keyA == "" {
		keyA = os.Getenv("SLACK_MCP_SSE_API_KEY")
		if keyA != "" {
			logger.Warn("SLACK_MCP_SSE_API_KEY is deprecated, please use SLACK_MCP_API_KEY")
		}
	}

	if keyA == "" {
		logger.Debug("No SSE API key configured, skipping authentication",
			zap.String("context", "http"),
		)
		return true, nil
	}
	...
```

The rest of the file: `AuthFromRequest` (`:73`) pulls the `Authorization`
header into context; `BuildMiddleware` (`:81`) is an **MCP tool-handler**
middleware (not an `http.Handler` wrapper) wired at
`pkg/server/server.go:147`; `IsAuthenticated` (`:112`) switches on transport —
`stdio` returns true unconditionally, `sse`/`http` call `validateToken`.
`subtle.ConstantTimeCompare` at `:59` is already correct — do not touch it.

Transport startup, `cmd/slack-mcp-server/main.go` — `case "sse":` (~`:94`)
and `case "http":` (~`:124`): each reads `SLACK_MCP_HOST`/`SLACK_MCP_PORT`
(defaults from `defaultSseHost` = `127.0.0.1` / `defaultSsePort` = `13080`,
constants at `:17-18`), builds via `s.ServeSSE(":"+port)` /
`s.ServeHTTP(":"+port)`, logs a listening line, and calls
`.Start(host+":"+port)`. Fatal-error convention in these branches:
`logger.Fatal("Server error", zap.String("context", "console"), zap.Error(err))`.
The `stdio` case never reaches `validateToken` — stdio is unaffected.

Docs: `docs/03-configuration-and-usage.md:216-222` shows the ngrok recipe;
the env-var table in the same file documents `SLACK_MCP_API_KEY`.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnitRequireAPIKey|TestValidateSSEAPIKey' ./pkg/server/...` | pass |
| Format | `gofmt -l pkg cmd` | no output |
| Manual (optional) | `SLACK_MCP_XOXP_TOKEN=stub go run ./cmd/slack-mcp-server --transport sse 2>&1 \| head -5` | fatal "no API key" error |

## Scope

**In scope**:
- `pkg/server/auth/sse_auth.go` (+ its test file, `sse_auth_test.go` if
  present — check; create if absent)
- `cmd/slack-mcp-server/main.go` (sse/http cases only)
- `docs/03-configuration-and-usage.md` (ngrok section + env table)
- `.env.dist` gains `SLACK_MCP_ALLOW_UNAUTHENTICATED` ONLY IF plan 021 is not
  selected; otherwise leave `.env.dist` to plan 021 and note the handoff.

**Out of scope**:
- The constant-time comparison (already correct).
- TLS/proxy-header handling, rate limiting (not planned).
- The stdio transport.
- Removing the deprecated `SLACK_MCP_SSE_API_KEY` fallback (keep it working;
  plan 021 documents the preferred name).

## Git workflow

- Branch: `advisor/016-sse-key-required`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 1: Startup check

Add an exported helper in `pkg/server/auth` (same file is fine):

```go
// RequireAPIKeyOrOptOut returns an error if no API key is configured for a
// network transport and the operator has not explicitly opted out via
// SLACK_MCP_ALLOW_UNAUTHENTICATED=true.
func RequireAPIKeyOrOptOut(logger *zap.Logger) error
```

Behavior: key present (either env var) → nil. Key absent and
`strings.EqualFold(os.Getenv("SLACK_MCP_ALLOW_UNAUTHENTICATED"), "true")` →
nil, but `logger.Warn` a clear "serving WITHOUT authentication — every client
that can reach <host:port> gets full Slack access" message. Key absent, no
opt-out → return an error whose text names both env vars and the opt-out.

In `main.go`, call it as the first statement of the `case "sse":` and
`case "http":` branches, before host/port resolution. On error use the
branches' existing fatal convention:
`logger.Fatal("<message>", zap.String("context", "console"), zap.Error(err))`.

**Verify**: `go build ./...` → exit 0

### Step 2: Upgrade the silent-pass log

In `validateToken` (`sse_auth.go:35-40`), change the `keyA == ""` branch's
`logger.Debug` to `logger.Warn`. That branch is now reachable only via the
explicit opt-out, so the noise is deliberate friction — but it fires per
request, so wrap it in a package-level `sync.Once` that Warns the first time
and Debugs thereafter. Keep the `return true, nil` behavior and the
`zap.String("context", "http")` field.

**Verify**: `go build ./...` → exit 0

### Step 3: Docs

In `docs/03-configuration-and-usage.md`:
- Env table: add `SLACK_MCP_ALLOW_UNAUTHENTICATED` (default unset; only
  honored by sse/http; strongly discouraged).
- ngrok section (~:216-222): add one bold sentence that the server refuses
  to start without `SLACK_MCP_API_KEY`, and never to combine ngrok with the
  opt-out.

**Verify**: re-read the section; `grep -n 'ALLOW_UNAUTHENTICATED' docs/` → 2+ matches

### Step 4: Tests

In `pkg/server/auth`'s test file, table-test `RequireAPIKeyOrOptOut` with
`t.Setenv`:

- No key, no opt-out → error mentioning `SLACK_MCP_API_KEY`.
- `SLACK_MCP_API_KEY` set → nil.
- Deprecated `SLACK_MCP_SSE_API_KEY` set → nil.
- No key, `SLACK_MCP_ALLOW_UNAUTHENTICATED=true` → nil (and, using a zap
  observer core, a Warn entry was emitted).
- Opt-out set to `"1"`/`"yes"` → error (strict `true` only — document this
  in the error text).

**Verify**: `go test -count=1 -run 'TestUnitRequireAPIKey' ./pkg/server/...` → pass

### Step 5: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 4. The load-bearing case: absent key + absent opt-out must be a startup
error, not a warning.

## Done criteria

- [ ] `make test` exits 0; new tests pass
- [ ] sse/http cases in `main.go` call the check before serving (read diff)
- [ ] `validateToken`'s no-key branch logs at Warn (via `sync.Once`), not Debug
- [ ] Docs updated (grep match above)
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated; report notes the breaking change

## STOP conditions

- Excerpts don't match (this plan's excerpts were corrected on 2026-08-07
  against the real code; a further mismatch means genuine drift).
- `ServeSSE`/`ServeHTTP` turn out to be called from anywhere besides
  main.go's transport switch (grep first) — the check must cover every
  entry point; report if the extra caller is out of scope.
- Note `IsAuthenticated` is also called directly from two handlers
  (`pkg/handler/channels.go:57`, `pkg/handler/conversations.go:214`). Those
  paths are unaffected by this plan — do not change them.
- An existing test asserts the silent-pass behavior — read its intent before
  deleting; report if it looks deliberate.

## Maintenance notes

- This user's Plug deployment is stdio; nothing changes for it. If they ever
  enable sse/http, startup will now demand a key — that's the feature.
- Plan 021 replaces `SLACK_MCP_SSE_API_KEY` with `SLACK_MCP_API_KEY` in
  `.env.dist`; keep the code fallback until upstream drops it.
- Reviewer: confirm the error path can't be reached on `stdio` (would break
  the primary deployment).
