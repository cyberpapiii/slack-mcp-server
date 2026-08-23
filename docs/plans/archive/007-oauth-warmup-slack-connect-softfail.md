# Plan 007: Make Slack Connect enrichment best-effort so OAuth tokens can build the users cache

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` — if it is not
> `adbae97` and not a descendant of it (`git merge-base --is-ancestor adbae97 HEAD && echo ok`),
> STOP: you are on the wrong base (this happened to three executors in July).
>
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- pkg/provider/api.go`
> If the file changed since this plan was written, compare the "Current state"
> excerpts against the live code before proceeding; on a mismatch, treat it as
> a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

When the server runs with an OAuth token (`xoxp-`/`xoxb-`) and there is no
usable on-disk users cache (first run, new team ID, cleared or expired cache),
the users cache build *always* fails: `fetchAndStoreUsers` fetches the user
list successfully, then hard-fails on a Slack Connect enrichment step that is
only possible with browser (xoxc/xoxd) tokens. Because the cache never becomes
ready, warmup burns all its retries and `RegisterCacheDependentTools()` never
runs — `channels_list`, `channels_me`, `channels_starred`,
`conversations_unreads`, and both MCP resources are permanently missing until
restart, and every `#channel`/`@user` name lookup fails. The same failure hits
browser-token users who degrade to an OAuth fallback mid-session. The
enrichment is additive (extra external users in the cache); losing it must not
lose the whole cache.

## Current state

- `pkg/provider/api.go` — the provider; `fetchAndStoreUsers` builds the users
  snapshot. The bug is the unconditional hard `return err`:

```go
// pkg/provider/api.go:1334-1342
	// Store intermediate snapshot so GetSlackConnect can read current users
	ap.usersSnapshot.Store(newSnapshot)

	connectUsers, err := ap.GetSlackConnect(ctx)
	if err != nil {
		ap.logger.Error("Failed to fetch users from Slack Connect", zap.Error(err))
		return err
	}
	list = append(list, connectUsers...)
```

- Why it always fails for OAuth — the call chain bottoms out in a
  browser-only feature check:

```go
// pkg/provider/api.go:1540-1545
func (ap *ApiProvider) GetSlackConnect(ctx context.Context) ([]slack.User, error) {
	boot, err := ap.client.ClientUserBoot(ctx)
```

```go
// pkg/provider/api.go:737-742
func (c *MCPSlackClient) ClientUserBoot(ctx context.Context) (*edge.ClientUserBootResponse, error) {
	if err := c.ensureBrowserFeature("client.userBoot"); err != nil {
		return nil, err
	}
```

```go
// pkg/provider/api.go:476-481
func (c *MCPSlackClient) browserFeaturesAvailable() bool {
	if c.isOAuth {
		return false
	}
	return browserRuntimeState(c.browserState.Load()) == browserStateActive
}
```

- Success path context: after the enrichment block, the function writes the
  cache file and sets readiness (`ap.usersReady.Store(true)` at
  `api.go:1378`). The warmup loop (`cmd/slack-mcp-server/warmup.go:24-60`)
  checks `p.IsReady()` and only registers cache-dependent tools when both
  caches are ready.
- Accessors that exist and are relevant: `ApiProvider.IsOAuth()` at
  `api.go:1716`, `MCPSlackClient.IsOAuth()` at `api.go:916`.
- `ApiProvider.client` is the `SlackAPI` **interface** (`api.go:375`), so unit
  tests can install a fake.
- Convention: zap structured logging; background cache paths must not be
  fatal (see AGENTS.md "Error handling").

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0, all `ok`, no `FAIL` |
| Targeted tests | `go test -count=1 -run TestUnitFetchAndStoreUsers ./pkg/provider/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope** (the only files you should modify):
- `pkg/provider/api.go` (the enrichment block in `fetchAndStoreUsers` only)
- `pkg/provider/api_cache_test.go` or a new `pkg/provider/api_users_test.go`

**Out of scope** (do NOT touch, even though they look related):
- `GetSlackConnect` itself and `ClientUserBoot` — their error behavior is
  correct for their callers; the fix belongs at the enrichment call site.
- `cmd/slack-mcp-server/warmup.go` retry policy — separately planned (006).
- The channels-cache path (`refreshChannelsInternal`) — no equivalent bug.

## Git workflow

- Branch: `advisor/007-oauth-warmup-slack-connect-softfail` from current master
- One commit; message style: imperative sentence, e.g. "Make Slack Connect enrichment best-effort in users cache build"
- Do NOT push or open a PR (local-only fork; maintainer merges).

## Steps

### Step 1: Downgrade the enrichment failure to a warning

In `fetchAndStoreUsers`, replace the hard failure with skip-and-continue, and
skip the call entirely for OAuth tokens (it can never succeed there):

```go
	if ap.IsOAuth() {
		ap.logger.Debug("Skipping Slack Connect enrichment (OAuth token, browser features unavailable)")
	} else {
		connectUsers, err := ap.GetSlackConnect(ctx)
		if err != nil {
			ap.logger.Warn("Slack Connect enrichment failed; continuing with standard user list",
				zap.Error(err))
		} else {
			list = append(list, connectUsers...)
			// ... existing len(connectUsers) > 0 merge block unchanged ...
		}
	}
```

Keep the existing `if len(connectUsers) > 0 { ... }` snapshot-merge block
(currently `api.go:1345-1361`) inside the success branch. Check whether
`ApiProvider.IsOAuth()` (`api.go:1716`) is usable here (it should be — same
receiver type); if it delegates to a nil client in some construction path,
guard accordingly.

**Verify**: `go build ./...` → exit 0

### Step 2: Add a regression test

In `pkg/provider` (internal test, same package — the existing tests like
`TestUnitPatchUser` in `api_patch_test.go` are the structural pattern):

- Build an `ApiProvider` whose `client` field is a fake `SlackAPI`
  implementation where `GetUsersContext` returns 2 users and
  `ClientUserBoot` returns an error (mirror how existing tests fake the
  interface — check `api_patch_test.go` and `api_edge_fallback_test.go` for
  an existing fake to extend rather than writing a new one).
- Point `usersCachePath` at `t.TempDir()`.
- Call `fetchAndStoreUsers` (or `RefreshUsers`) and assert: it returns nil,
  `usersReady.Load()` is true, and the snapshot contains the 2 users.

Name it `TestUnitFetchAndStoreUsersSurvivesConnectFailure` (no
"Integration" in the name so `make test` runs it).

**Verify**: `go test -count=1 -run TestUnitFetchAndStoreUsersSurvivesConnectFailure ./pkg/provider/` → PASS

### Step 3: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

- New: `TestUnitFetchAndStoreUsersSurvivesConnectFailure` (Step 2) — the
  regression this plan fixes.
- Existing: full `make test` must stay green (cache tests in
  `cache_test.go` exercise adjacent behavior).

## Done criteria

- [ ] `go build ./...` exits 0
- [ ] `make test` exits 0 including the new test
- [ ] In `fetchAndStoreUsers`, a `GetSlackConnect` error no longer causes `return err` (verify by reading the diff)
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The excerpts above don't match the live code (drift).
- You find a caller that *depends* on `fetchAndStoreUsers` failing when
  Slack Connect fails (search callers of `RefreshUsers`/`fetchAndStoreUsers`
  first — none should).
- Faking `SlackAPI` requires adding methods to the interface or changing
  production signatures — report instead of changing the interface.
- The existing fake clients in tests are structured so differently that
  Step 2 needs >~80 lines of new scaffolding.

## Maintenance notes

- If a future change adds more enrichment sources to the users cache, they
  must follow this same best-effort pattern — cache readiness gates tool
  registration, so any hard failure here silently amputates the tool surface.
- Reviewer: check that the OAuth skip uses `IsOAuth()` and not a token-prefix
  re-check, and that the warn path still writes the cache file and sets
  `usersReady`.
- Deferred: warmup retry policy (plan 006, executed on `advisor/006-warmup-selfheal-slow-retry`, unmerged as of planning).
