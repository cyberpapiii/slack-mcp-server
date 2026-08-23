# Plan 032: A 429 storm hangs the background users refresh forever, and it never recovers

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **You must work in your own isolated git worktree.** Run
> `git rev-parse --show-toplevel` first. If it prints exactly
> `/Users/robdezendorf/Documents/GitHub/slack-mcp-server` you have been given
> the user's main checkout — STOP IMMEDIATELY, change nothing, run no
> `git checkout`, and report it.
>
> **STEP ZERO — fix your base commit.** Your worktree probably came up at
> `origin/master`, which is the wrong base:
>
> ```
> git rev-parse --short HEAD
> git fetch origin
> git worktree prune
> git branch -D advisor/032-background-refresh-unbounded-hang 2>/dev/null || true
> git checkout -B advisor/032-background-refresh-unbounded-hang 0bd7766
> git rev-parse --short HEAD
> ```
>
> The last command MUST print `0bd7766`. Anything else: STOP and report.
>
> This plan is on **Track A** — the `pkg/provider/` + Makefile stack
> (007 → 013 → 017 → 008 → 019 → 018 → 028 → 029 → 031). `0bd7766` is its tip.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW — adds a deadline where there was none; no signature changes
- **Depends on**: plan 031 (`0bd7766` is simply the current Track A tip; this
  plan does not depend on 031's logic)
- **Category**: correctness / liveness
- **Planned at**: commit `0bd7766`, 2026-08-07

## Why this matters

Three facts combine into a permanent stall.

**1. slack-go retries rate limits without any bound.** `GetUsersContext` in
slack-go v0.19.0 (`users.go:415-430`, read from the module cache at
`$(go env GOMODCACHE)/github.com/slack-go/slack@v0.19.0/users.go`):

```go
func (api *Client) GetUsersContext(ctx context.Context, options ...GetUsersOption) (results []User, err error) {
	p := api.GetUsersPaginated(options...)
	for err == nil {
		p, err = p.Next(ctx)
		if err == nil {
			results = append(results, p.Users...)
		} else if rateLimitedError, ok := err.(*RateLimitedError); ok {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(rateLimitedError.RetryAfter):
				err = nil
			}
		}
	}

	return results, p.Failure(err)
}
```

A rate-limit error sleeps for `RetryAfter` and then sets `err = nil`, which
re-enters the `for err == nil` loop. There is no attempt counter. The **only**
exit from a sustained 429 is `ctx.Done()`.

**2. The background refresh passes a context that is never done.**
`spawnBackgroundUsersRefresh` (`pkg/provider/api.go:1303-1315`):

```go
func (ap *ApiProvider) spawnBackgroundUsersRefresh() {
	if !ap.refreshingUsers.CompareAndSwap(false, true) {
		ap.logger.Debug("Skipping background users refresh, already in progress")
		return
	}
	go func() {
		defer ap.refreshingUsers.Store(false)
		if err := ap.fetchAndStoreUsers(context.Background()); err != nil {
			ap.logger.Warn("Background users refresh failed, continuing with stale data",
				zap.Error(err))
		}
	}()
}
```

`context.Background()` has no deadline and is never cancelled. So fact 1's only
escape hatch is disabled.

**3. While hung, it blocks every future attempt.** `fetchAndStoreUsers` takes
`ap.fetchUsersMu` on entry and holds it for the duration, and
`ap.refreshingUsers` stays `true` because the `defer` never runs. So the
compare-and-swap at the top of `spawnBackgroundUsersRefresh` fails for every
subsequent call, logging only `"Skipping background users refresh, already in
progress"` at Debug level.

**Net effect**: one extended rate-limit episode leaks a goroutine that spins
`fetch → 429 → sleep → fetch` forever, and the users cache can never refresh
again for the life of the process. The only recovery is a restart. The
symptom a user would see is a users cache that silently stops updating, with
nothing above Debug level to explain it.

### Why the channels path is not affected the same way

`spawnBackgroundChannelsRefresh` (`api.go:1516-1528`) also passes
`context.Background()`. But plan 031 routed that path through
`limiter.CallWithRetry(ctx, lim, 2, ...)`, which gives up after two retries and
returns the error. So it terminates.

It is still the only thing standing between that goroutine and an unbounded
wait, and it is incidental rather than deliberate. This plan gives both
background refreshes an explicit deadline, so neither depends on a downstream
retry bound for liveness.

## Current state

Verified at `0bd7766`. Both background spawns use `context.Background()`;
`grep -n 'context.Background()' pkg/provider/api.go` returns exactly two hits,
at lines `1309` and `1522`.

### `spawnBackgroundUsersRefresh` — `api.go:1303-1315`

Quoted in full above.

### `spawnBackgroundChannelsRefresh` — `api.go:1516-1528`

```go
// spawnBackgroundChannelsRefresh starts a background goroutine to fetch fresh channel data.
func (ap *ApiProvider) spawnBackgroundChannelsRefresh() {
	if !ap.refreshingChannels.CompareAndSwap(false, true) {
		ap.logger.Debug("Skipping background channels refresh, already in progress")
		return
	}
	go func() {
		defer ap.refreshingChannels.Store(false)
		if err := ap.fetchAndStoreChannels(context.Background()); err != nil {
			ap.logger.Warn("Background channels refresh failed, continuing with stale data",
				zap.Error(err))
		}
	}()
}
```

### `ApiProvider` fields — `api.go:385-410`

The struct already carries tuning values as fields, e.g.:

```go
	cacheTTL           time.Duration
	minRefreshInterval time.Duration
```

`cacheTTL` defaults to `defaultCacheTTL = 24 * time.Hour` (`api.go:31`) and is
overridable via `SLACK_MCP_CACHE_TTL` (`getCacheTTL`, `api.go:113-139`). The
struct is constructed in two places — `grep -n 'minRefreshInterval:' pkg/provider/api.go`
returns `1070` and `1143`. **Both must be updated.**

## Repo conventions to follow

- Go 1.25, `gofmt`. `gofmt -l pkg/provider/` must be silent.
- `context`, `time` and `go.uber.org/zap` are already imported in `api.go`.
  **No new imports and no `go.mod`/`go.sum` change should be needed.**
- Tunables live as a `time.Duration` field on `ApiProvider`, seeded from a
  package-level `default...` constant. Follow `cacheTTL`'s shape exactly.
- Tests in `pkg/provider/` use **testify** (`require`, `assert`). The mock
  pattern is embed-the-interface-and-override; `api_patch_test.go` has
  `newTestApiProvider(client SlackAPI, snapshot *UsersCache) *ApiProvider`.
  Read it before writing tests.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Format | `gofmt -l pkg/provider/` | no output |
| Package tests | `go test -count=1 -race ./pkg/provider/` | `ok`, exit 0 |
| Full suite | `make test` | exit 0 |
| Module purity | `git status --porcelain go.mod go.sum` | no output |

## Scope

**In scope**:
- `pkg/provider/api.go` — one new constant, one new `ApiProvider` field, the
  two constructor sites, and the two `spawnBackground*Refresh` functions.
- One new test file, `pkg/provider/api_background_refresh_test.go`.

**Out of scope — do not touch**:
- `fetchAndStoreUsers` and `fetchAndStoreChannels` themselves. They already
  respect the context they are handed; the bug is in what is handed to them.
- The **synchronous** paths (`refreshUsersInternal`, `refreshChannelsInternal`,
  `RefreshUsers`, `ForceRefreshUsers` and their channel twins). They receive a
  caller-supplied context and must keep doing so. Only the two `go func()`
  bodies change.
- Replacing slack-go's internal pagination with a hand-rolled loop plus
  `limiter.CallWithRetry`, mirroring what plan 031 did for channels. That is
  the more thorough fix and probably the right eventual one, but it changes the
  users fetch's semantics and belongs in its own plan. **Do not attempt it
  here.** Note it in your report if you form an opinion.
- Adding a new `SLACK_MCP_*` environment variable for the timeout. See
  "Deliberately not configurable" below.
- `pkg/limiter/`, `pkg/handler/`, anything outside `pkg/provider/`.

## Deliberately not configurable

`cacheTTL` reads `SLACK_MCP_CACHE_TTL`, so the obvious move is a matching env
var. **Do not add one.** A new environment variable is a new user-facing
surface that has to be documented in `.env.dist`, `README.md` and `AGENTS.md`,
and this plan is a liveness fix, not a feature. The field exists so tests can
override it; production always gets the constant.

If you think an env var is warranted, say so in your report and leave the code
without one.

## Git workflow

- Branch: `advisor/032-background-refresh-unbounded-hang`, based on `0bd7766`.
- One commit, imperative subject. Do **not** push. Do **not** merge to master.

## Steps

### Step 1: Add the constant and the field

Next to `defaultCacheTTL` (`api.go:31`), add:

```go
// defaultBackgroundRefreshTimeout bounds a background cache refresh. Without a
// deadline these goroutines can run forever: slack-go's GetUsersContext
// retries rate limits in an unbounded loop whose only exit is ctx.Done(), and
// a hung refresh holds fetchUsersMu and leaves refreshingUsers set, so no
// later refresh can ever start. Sized generously — a large workspace
// legitimately takes many minutes when Slack is throttling — because the goal
// is to bound an infinite hang, not to make the refresh fast. It sits well
// inside the 24h default cache TTL, so a timeout never races the next refresh.
const defaultBackgroundRefreshTimeout = 15 * time.Minute
```

Add a matching field to `ApiProvider`, immediately after `minRefreshInterval`
so the tuning values stay together:

```go
	// backgroundRefreshTimeout bounds spawnBackgroundUsersRefresh and
	// spawnBackgroundChannelsRefresh. Always defaultBackgroundRefreshTimeout in
	// production; a field only so tests can shorten it.
	backgroundRefreshTimeout time.Duration
```

Set it at **both** construction sites. Find them with
`grep -n 'minRefreshInterval:' pkg/provider/api.go` — there are two, and
missing one leaves a zero-valued timeout, which would make
`context.WithTimeout` expire **immediately** and break every background
refresh. This is the highest-risk mistake in the plan; do not skip the grep.

**Verify**:
- `go build ./...` → exit 0
- `grep -c 'backgroundRefreshTimeout:' pkg/provider/api.go` → `2`

### Step 2: Give both background refreshes a deadline

In `spawnBackgroundUsersRefresh`, replace the goroutine body:

```go
	go func() {
		defer ap.refreshingUsers.Store(false)

		ctx, cancel := context.WithTimeout(context.Background(), ap.backgroundRefreshTimeout)
		defer cancel()

		if err := ap.fetchAndStoreUsers(ctx); err != nil {
			ap.logger.Warn("Background users refresh failed, continuing with stale data",
				zap.Error(err))
		}
	}()
```

Make the identical change in `spawnBackgroundChannelsRefresh`, using
`ap.refreshingChannels` and `ap.fetchAndStoreChannels`.

Order matters: `defer ap.refreshing*.Store(false)` must stay **first** so the
flag is cleared even if the fetch panics — that is existing behavior, keep it.

**Verify**:
- `go build ./...` → exit 0
- `go vet ./...` → exit 0
- `grep -c 'context.Background()' pkg/provider/api.go` → still `2` (each
  `context.Background()` is now wrapped by `context.WithTimeout` rather than
  passed directly — confirm by reading both call sites, not by the count alone)
- `grep -c 'context.WithTimeout' pkg/provider/api.go` → `2`

### Step 3: Test that a hung fetch terminates and releases the flag

New file `pkg/provider/api_background_refresh_test.go`, package `provider`,
testify style. Read `api_patch_test.go` for `newTestApiProvider` and the
embed-and-override mock and reuse them.

Build a mock whose `GetUsersContext` blocks until the context is done — that
is what an unbounded 429 retry loop looks like from the outside:

```go
type hangingUsersClient struct {
	SlackAPI // embed interface to satisfy all methods; only override what we need
	started  chan struct{}
	sawDone  chan struct{}
}

func (c *hangingUsersClient) GetUsersContext(ctx context.Context, _ ...slack.GetUsersOption) ([]slack.User, error) {
	close(c.started)
	<-ctx.Done()
	close(c.sawDone)
	return nil, ctx.Err()
}
```

Two subtests:

1. **`background users refresh gives up at the timeout`** — build a provider
   with `backgroundRefreshTimeout` set to something short (**50ms**, not 10ms:
   `-race` slows everything and a too-tight bound makes the test flaky), call
   `spawnBackgroundUsersRefresh()`, then assert:
   - the mock's `GetUsersContext` was entered (`<-started`);
   - the mock observed context cancellation (`<-sawDone`) within ~2 seconds;
   - `ap.refreshingUsers.Load()` returns to `false` within ~2 seconds.

   Use a polling helper or `select` with `time.After` for the bounds. **Do not
   use a bare `time.Sleep` long enough to "probably" be safe** — assert on the
   channel and poll the flag.

2. **`a second spawn is skipped while one is in flight, and allowed after`** —
   spawn once, wait for `started`, call `spawnBackgroundUsersRefresh()` again
   and confirm the mock was not re-entered (its call counter stays 1), then
   after the flag clears confirm a fresh spawn *does* run. This pins the
   recovery half: the whole point is that the process becomes able to refresh
   again.

   Guard the counter with a mutex or use `atomic.Int32` — the test runs under
   `-race`.

The test must not touch the real filesystem. `newTestApiProvider` should give
you a provider with no cache paths configured; if `fetchAndStoreUsers` tries to
write a cache file in these tests, that means the fetch succeeded, which it
must not in subtest 1. If you hit a filesystem write, STOP and report — it
means the mock is not being used.

**Verify**:
- `go test -count=1 -race -run 'TestUnitBackgroundRefresh' -v ./pkg/provider/` → PASS
- The whole run takes well under 2 seconds

### Step 4: Prove the test catches the old behavior

Temporarily revert Step 2's users change back to
`ap.fetchAndStoreUsers(context.Background())`. Run subtest 1 with a **short**
Go test timeout so a hang fails fast rather than stalling your session:

```
go test -count=1 -race -timeout 30s -run 'TestUnitBackgroundRefresh' ./pkg/provider/
```

It must fail — either on the `sawDone` assertion or by the test binary timing
out (the goroutine never returns). Record which. Then restore Step 2 and
confirm it passes again.

**Verify**: after restoring, `go test -count=1 -race ./pkg/provider/` → exit 0

### Step 5: Final

**Verify**:
- `make test` → exit 0
- `gofmt -l pkg/provider/` → no output
- `git status --porcelain go.mod go.sum` → no output
- `git diff 0bd7766..HEAD --stat` → exactly two files: `pkg/provider/api.go`
  and `pkg/provider/api_background_refresh_test.go`

## Test plan

Two subtests in one new file, described in Step 3. They pin:

- a background users refresh whose fetch never returns on its own is cancelled
  at the deadline, and the in-flight flag is released;
- the compare-and-swap still suppresses concurrent spawns, and stops
  suppressing them once the first one finishes.

No existing test changes. If any existing test in `pkg/provider/` fails, that
means the new field was left zero at one of the two construction sites — check
Step 1's grep before doing anything else.

Not covered, deliberately: the real 15-minute production value, and slack-go's
internal retry loop (a dependency's behavior, quoted above but not this repo's
to test).

## Done criteria

- [ ] `grep -c 'backgroundRefreshTimeout:' pkg/provider/api.go` → `2`
- [ ] `grep -c 'context.WithTimeout' pkg/provider/api.go` → `2`
- [ ] Neither `spawnBackgroundUsersRefresh` nor `spawnBackgroundChannelsRefresh`
      passes a context without a deadline to its fetch function
- [ ] `defer ap.refreshing*.Store(false)` is still the **first** statement in
      both goroutine bodies
- [ ] No new `SLACK_MCP_*` environment variable was added
- [ ] `go build ./...` → exit 0
- [ ] `go vet ./...` → exit 0
- [ ] `gofmt -l pkg/provider/` → no output
- [ ] `git status --porcelain go.mod go.sum` → no output
- [ ] `go test -count=1 -race ./pkg/provider/` → exit 0
- [ ] `make test` → exit 0
- [ ] `git diff 0bd7766..HEAD --stat` touches exactly the two files named above
- [ ] Your report states the Step 4 result explicitly: did subtest 1 fail with
      the old `context.Background()` restored, and did it fail by assertion or
      by test-binary timeout
- [ ] The new tests add well under 2 seconds to the suite

> Reviewer note on the grep criteria: both were run against `0bd7766` while
> writing this plan. Pre-fix, `backgroundRefreshTimeout:` = 0,
> `context.WithTimeout` = 0, and `context.Background()` = 2 (at lines 1309 and
> 1522). If your observed starting counts disagree, the criterion is wrong —
> report it rather than bending the code to match.

## STOP conditions

- `grep -n 'minRefreshInterval:' pkg/provider/api.go` returns a number of
  construction sites other than two. Report how many and where before adding
  the field.
- An existing test fails after Step 1. That is almost certainly a zero-valued
  `backgroundRefreshTimeout` at a construction site you missed — but confirm,
  and report rather than adding a `if timeout == 0 { timeout = default }`
  fallback. A fallback would paper over exactly the mistake the test caught.
- Subtest 1 passes with the old `context.Background()` restored in Step 4. The
  test is not testing what it claims — say so plainly.
- You conclude the users fetch should be re-plumbed through
  `limiter.CallWithRetry` like plan 031 did for channels. Report the reasoning;
  do not do it here.
- Any test needs to sleep for more than ~100ms of wall clock.

## Maintenance notes

- The invariant: **no goroutine in this package may call a fetch with a context
  that has neither a deadline nor a cancel path.** Both background spawns are
  now the only `context.Background()` uses in the file; a third one appearing
  is the thing to catch in review.
- The deeper issue is untouched: the users fetch delegates pagination and
  rate-limit handling entirely to slack-go, which retries without bound and
  does no proactive pacing. This plan bounds the damage; it does not fix the
  cause. The thorough fix is to paginate with `GetUsersPaginated` behind the
  repo's own `limiter` + `CallWithRetry`, exactly as plan 031 did for channels.
  That should be a separate plan, and it would make this plan's timeout a
  backstop rather than the primary defense.
- Plan 030's table of limiter call sites does not list the users fetch, because
  the users fetch uses no limiter at all. Worth adding when 030 is next revised.
- Reviewer: confirm the diff is two files; that the field is set at **both**
  construction sites; that `defer ...Store(false)` stayed first in both
  goroutines; and that no environment variable was introduced.
