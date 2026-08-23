# Plan 031: One 429 mid-pagination silently truncates the channel cache — in memory and on disk

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **STEP ZERO — fix your base commit before anything else.** Your worktree
> probably came up at `origin/master`, which is the wrong base. Run:
>
> ```
> git rev-parse --short HEAD
> git fetch origin
> git worktree prune
> git branch -D advisor/031-partial-channel-cache-poisoning 2>/dev/null || true
> git checkout -B advisor/031-partial-channel-cache-poisoning 5c936ec
> git rev-parse --short HEAD
> ```
>
> The last command MUST print `5c936ec`. Anything else: STOP and report.
>
> This plan is on **Track A** — the `pkg/provider/` + Makefile stack
> (007 → 013 → 017 → 008 → 019 → 018 → 028 → 029). `5c936ec` is its tip.
> It is **not** on the Track B stack (`pkg/handler/`, `pkg/server/`).

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MEDIUM — changes three function signatures and one caching policy
- **Depends on**: nothing in-flight; `5c936ec` is simply the current Track A tip
- **Category**: correctness / caching
- **Planned at**: commit `5c936ec`, 2026-08-07
- **Supersedes**: the open question in plan 030 about `conversations.list`'s
  rate tier. See "Relationship to plan 030" below — this plan answers it.

## Why this matters

`getChannelsMultiType` paginates `conversations.list`. When a page fails, it
logs and **breaks out of the loop, returning the pages it already collected
with no indication that the list is incomplete**:

```go
		channels, nextcur, err = ap.client.GetConversationsContext(ctx, params)
		ap.logger.Debug("Fetched channels", ...)
		if err != nil {
			ap.logger.Error("Failed to fetch channels", zap.Error(err))
			break
		}
```

Follow where that truncated slice goes.

1. `GetChannels` (`api.go:1667`) receives it, builds a fresh `ChannelsCache`
   snapshot from it, and **unconditionally** installs it:
   `ap.channelsSnapshot.Store(newSnapshot)`. A complete, correct in-memory
   cache is atomically replaced by a truncated one.
2. `fetchAndStoreChannels` (`api.go:1531`) then guards only the *empty* case:

   ```go
   	if len(channels) == 0 {
   		if ap.channelsReady.Load() {
   			ap.logger.Warn("API returned zero channels, keeping existing cache")
   			return nil
   		}
   		return errors.New("API returned zero channels and no existing cache is available")
   	}
   ```

   A partial result of 1..N−1 channels sails straight past it.
3. The truncated list is then marshalled and written to the on-disk cache with
   `atomicWriteFile`, logged as a success (`"Wrote channels to cache"`), and
   `ap.channelsReady.Store(true)`.

So a single 429 on page 3 of 5 during a routine background refresh
**permanently truncates the channel cache**, in memory *and* on disk, with an
`Info`-level success log. It survives restarts: `refreshChannelsInternal`
reads the truncated file back, finds `len(cachedChannels) != 0`, and serves it
as valid.

Every channel past the failure point becomes a cache miss from then on. That
is the direct cause of the bare-ID rendering (`C_UNKNOWN`, `D_NOUSER`)
recorded as unreads surprise 7 in `plans/README.md`.

This is the same bug class the repo has already fixed once. `api_cache_test.go`
documents it:

> This is a regression test for the null cache poisoning bug: when
> `GetChannels` returned nil, `json.MarshalIndent(nil)` wrote `"null"` to the
> cache file. On next startup, `Unmarshal` succeeded with an empty slice,
> which was incorrectly treated as valid cached data.

That fix guarded `len == 0`. The partial case — the much more likely one,
since a rate limit hits *mid*-pagination rather than on page one — was left
unguarded. This plan finishes that work.

### Why a 429 here is likely, not hypothetical

`getChannelsMultiType` paces this loop with `limiter.Tier2boost`:

```go
	lim := limiter.Tier2boost.Limiter()
```

`Tier2boost` is `tier{t: 300 * time.Millisecond, b: 5}` — 200 requests/minute.
`Tier2boost` exists for the *undocumented internal edge API*, and the other
three call sites that use it (`edge/dms.go:41`, `edge/search.go:145`,
`edge/client.go:142`) are all genuine edge calls. This one is not:
`ap.client.GetConversationsContext` is the **standard** slack-go Web API path,
i.e. `conversations.list`, which Slack meters as a documented tier.

If `conversations.list` is Tier 2 (20 requests/minute), this loop runs roughly
10× over its allowance. It is almost certainly a copy-paste from the three edge
sites. Nothing else in the loop retries, so the first 429 truncates the cache.

## Relationship to plan 030

`plans/030-rate-limiter-architecture-decision.md` raised the `Tier2boost`
mis-assignment as an open question for the maintainer, on the grounds that
correcting it would cost ~10× latency on channel listing.

**That framing overstated the cost, and this plan corrects it.** The ratio is
right; the absolute numbers are small, because this loop issues only a handful
of requests. `params.Limit` is 999, and Slack caps `conversations.list` at
1000 per page, so:

| Workspace | Pages | `Tier2boost` (burst 5, 300ms) | `Tier2` (burst 3, 3s) | Delta |
|---|---|---|---|---|
| 5,000 channels | 5 | ~0s (all within burst) | ~6s | ~6s |
| 20,000 channels | 21 | ~5s | ~57s | ~52s |

Seconds to under a minute, on a background refresh, once per cache TTL — not
the minutes the 030 write-up implied. Weighed against a silently corrupted
channel cache that survives restarts, correcting the tier is clearly worth it.

**So this plan takes the decision**: move to `Tier2`, add retry, and make a
partial fetch loud instead of silent. Update plan 030's open question to
"answered by 031" — the reviewer does that, not you.

## Current state

All excerpts verified at `5c936ec` by reading `pkg/provider/api.go`. Line
numbers are from that commit; re-verify with `grep -n` before editing and match
on code text, not on line numbers.

### `getChannelsMultiType` — `api.go:1605-1665`

```go
func (ap *ApiProvider) getChannelsMultiType(ctx context.Context, channelTypes []string) []Channel {
	params := &slack.GetConversationsParameters{
		Types:           channelTypes,
		Limit:           999,
		ExcludeArchived: true,
	}

	var (
		channels []slack.Channel
		chans    []Channel

		nextcur string
		err     error
	)

	usersMap := ap.ProvideUsersMap().Users
	lim := limiter.Tier2boost.Limiter()

	for {
		if err := lim.Wait(ctx); err != nil {
			ap.logger.Error("Rate limiter wait failed", zap.Error(err))
			return nil
		}

		channels, nextcur, err = ap.client.GetConversationsContext(ctx, params)
		ap.logger.Debug("Fetched channels",
			zap.Strings("channelTypes", channelTypes),
			zap.Int("count", len(channels)),
		)
		if err != nil {
			ap.logger.Error("Failed to fetch channels", zap.Error(err))
			break
		}

		for _, channel := range channels {
			ch := mapChannel(
				channel.ID,
				channel.Name,
				channel.NameNormalized,
				channel.Topic.Value,
				channel.Purpose.Value,
				channel.User,
				channel.Members,
				channel.NumMembers,
				channel.IsIM,
				channel.IsMpIM,
				channel.IsPrivate,
				channel.IsExtShared,
				usersMap,
			)
			chans = append(chans, ch)
		}

		if nextcur == "" {
			break
		}

		params.Cursor = nextcur
	}
	return chans
}
```

### `GetChannelsType` — `api.go:1601-1603`

```go
func (ap *ApiProvider) GetChannelsType(ctx context.Context, channelType string) []Channel {
	return ap.getChannelsMultiType(ctx, []string{channelType})
}
```

### `GetChannels` — `api.go:1667-1695`

```go
func (ap *ApiProvider) GetChannels(ctx context.Context, channelTypes []string) []Channel {
	if len(channelTypes) == 0 {
		channelTypes = AllChanTypes
	}

	// Fetch all channel types in a single paginated call. The standard
	// conversations.list API supports multiple types per request, and the edge
	// API (Enterprise Grid + non-OAuth) returns all types regardless. This
	// avoids making 4 separate API round-trips (one per type).
	chans := ap.getChannelsMultiType(ctx, channelTypes)

	// Build new snapshot with all fetched channels
	newSnapshot := &ChannelsCache{
		Channels:    make(map[string]Channel, len(chans)),
		ChannelsInv: make(map[string]string, len(chans)),
	}
	for _, ch := range chans {
		newSnapshot.Channels[ch.ID] = ch
		newSnapshot.ChannelsInv[ch.Name] = ch.ID
	}
	ap.channelsSnapshot.Store(newSnapshot)

	return chans
}
```

### `fetchAndStoreChannels` — `api.go:1531-1563`

```go
func (ap *ApiProvider) fetchAndStoreChannels(ctx context.Context) error {
	ap.fetchChannelsMu.Lock()
	defer ap.fetchChannelsMu.Unlock()

	channels := ap.GetChannels(ctx, AllChanTypes)

	if len(channels) == 0 {
		if ap.channelsReady.Load() {
			ap.logger.Warn("API returned zero channels, keeping existing cache")
			return nil
		}
		return errors.New("API returned zero channels and no existing cache is available")
	}
	... marshal, atomicWriteFile, channelsReady.Store(true), return nil
}
```

### Callers — enumerated from the callee, not guessed

`grep -rn "GetChannels(\|GetChannelsType(\|getChannelsMultiType(" pkg cmd --include="*.go"`
at `5c936ec` returns exactly these, and nothing else:

| Function | Callers |
|---|---|
| `getChannelsMultiType` | `GetChannelsType:1602`, `GetChannels:1676` |
| `GetChannelsType` | **none** — exported, zero call sites in this repo |
| `GetChannels` | `fetchAndStoreChannels:1535` only |

No `_test.go` file calls any of the three; `api_channels_test.go` and
`api_cache_test.go` only *mirror* their logic in local helpers. So all three
signatures can change safely.

**Re-run that grep yourself before changing any signature** and confirm you get
the same list. If you find a caller not in the table, STOP and report.

### What happens to an error once `fetchAndStoreChannels` returns one

This matters, because the new policy relies on it. Both existing paths already
handle a returned error correctly:

- **Background refresh** — `spawnBackgroundChannelsRefresh:1515` logs
  `"Background channels refresh failed, continuing with stale data"` at Warn
  and leaves the existing snapshot and cache file untouched. Exactly right.
- **Synchronous path** — `refreshChannelsInternal` ends with
  `return ap.fetchAndStoreChannels(ctx)`, propagating to its caller. A cold
  start with no cache fails loudly, which is already the behavior for the
  zero-channel case.

So this plan invents **no new policy**. It extends the existing zero-channel
guard to cover "incomplete fetch", and the surrounding machinery already does
the right thing with the result.

## Repo conventions to follow

- Go 1.25, standard `gofmt`. `gofmt -l pkg/provider/` must be silent.
- `pkg/provider/api.go` already imports everything you need: `context`,
  `errors`, `time`, `github.com/slack-go/slack`,
  `github.com/korotovsky/slack-mcp-server/pkg/limiter`, and `go.uber.org/zap`.
  **No new imports and no `go.mod`/`go.sum` change should be required.**
- Comments explain *why*, in full sentences. Match the surrounding density.
- Tests in `pkg/provider/` use **testify** (`require`, `assert`) — unlike
  `pkg/provider/edge/edge_test.go`, which does not. Follow the `pkg/provider/`
  convention here.
- The mock pattern is embed-the-interface-and-override. From
  `api_patch_test.go`:
  ```go
  type mockSlackClient struct {
  	SlackAPI // embed interface to satisfy all methods; only override what we need
  	...
  }
  ```
  and there is a `newTestApiProvider(client SlackAPI, snapshot *UsersCache) *ApiProvider`
  helper in that same file. **Read both before writing your test** and reuse
  them rather than building a parallel harness.

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
- `pkg/provider/api.go` — `getChannelsMultiType`, `GetChannelsType`,
  `GetChannels`, `fetchAndStoreChannels`, plus one new small helper.
- One new test file, `pkg/provider/api_channel_fetch_test.go`.

**Out of scope — do not touch**:
- The **users** cache path: `fetchAndStoreUsers`, `refreshUsersInternal`,
  `spawnBackgroundUsersRefresh`, `GetUsers`. It has the same shape and very
  likely the same bug. **Leave it alone.** It is a separate plan, and mixing
  the two makes this diff unreviewable. If you notice specifics, put them in
  your report.
- `refreshChannelsInternal` and `spawnBackgroundChannelsRefresh`. They already
  handle a returned error correctly. Read them; do not edit them.
- The cache-file format, `atomicWriteFile`, and the TTL logic.
- Every other `limiter.TierN.Limiter()` call site in the repo. Only the one
  inside `getChannelsMultiType` changes tier.
- `pkg/limiter/` itself.
- `mapChannel` and the `ChannelsCache` struct.

## Git workflow

- Branch: `advisor/031-partial-channel-cache-poisoning`, based on `5c936ec`.
- One commit, imperative subject. Do **not** push. Do **not** merge to master.

## Steps

### Step 1: Add a retry-after classifier to package `provider`

`pkg/handler/conversations.go` already has one of these; package `provider`
does not, and the two packages do not import each other. Add a near-identical
unexported helper next to `getChannelsMultiType`:

```go
// slackRetryAfter reports the retry-after duration for a Slack rate-limit
// error, or 0 when the error is not retryable. It is the retryAfter callback
// for limiter.CallWithRetry.
func slackRetryAfter(err error) time.Duration {
	var rle *slack.RateLimitedError
	if errors.As(err, &rle) {
		return rle.RetryAfter
	}
	return 0
}
```

First check the name is free in this package:
`grep -rn 'func slackRetryAfter' pkg/provider/`. If something already defines
it, reuse that instead of adding a second one.

**Verify**: `go build ./...` → exit 0

### Step 2: Make `getChannelsMultiType` report incompleteness, retry, and use `Tier2`

Change its signature to `([]Channel, error)` and make four changes to the body.

1. `lim := limiter.Tier2boost.Limiter()` becomes `lim := limiter.Tier2.Limiter()`,
   with a comment recording *why*, e.g.:

   ```go
   	// conversations.list is a standard Web API method, so it is metered at
   	// Slack's documented tier — not at the edge API's rate. Tier2boost here
   	// was a copy-paste from the edge call sites and ran ~10x over budget,
   	// which is what made a mid-pagination 429 likely enough to truncate the
   	// cache.
   	lim := limiter.Tier2.Limiter()
   ```

2. The `lim.Wait` failure path returns `nil` today. Return `nil, err` instead —
   a cancelled context is not an empty channel list.

3. Wrap the API call in `limiter.CallWithRetry` so a transient 429 retries
   instead of truncating. `CallWithRetry` is generic over a single return
   value and `GetConversationsContext` returns three, so bundle them:

   ```go
   		type page struct {
   			channels []slack.Channel
   			cursor   string
   		}
   		p, err := limiter.CallWithRetry(ctx, lim, 2, slackRetryAfter,
   			func() (page, error) {
   				c, cur, err := ap.client.GetConversationsContext(ctx, params)
   				return page{channels: c, cursor: cur}, err
   			})
   ```

   Declare the `page` type at package level or just above the function rather
   than inside the loop body. Note `CallWithRetry` calls `lim.Wait` itself, so
   the loop's own leading `lim.Wait(ctx)` becomes redundant — **remove it**,
   and keep the context-cancellation behavior by letting `CallWithRetry`'s
   `"rate limiter context cancelled"` error propagate. Read
   `pkg/limiter/retry.go` in full before doing this; it is 75 lines.

4. The `if err != nil { log; break }` arm returns the partial result **with**
   the error:

   ```go
   		if err != nil {
   			ap.logger.Error("Failed to fetch channels, returning partial result",
   				zap.Strings("channelTypes", channelTypes),
   				zap.Int("collectedSoFar", len(chans)),
   				zap.Error(err))
   			return chans, err
   		}
   ```

   Returning the partial data *and* the error is deliberate: the caller decides
   whether partial is usable, and a future caller might legitimately want it.
   What must never happen again is partial data arriving with a nil error.

The success path still ends `return chans, nil`.

**Verify**:
- `go build ./...` → exit 0
- `grep -n 'Tier2boost' pkg/provider/api.go` → no output
- `grep -c 'limiter.Tier2.Limiter()' pkg/provider/api.go` → `1`

### Step 3: Propagate through `GetChannelsType` and `GetChannels`

`GetChannelsType` becomes `([]Channel, error)` and forwards both values. It has
zero callers, so nothing else changes.

`GetChannels` becomes `([]Channel, error)`. The important part:

```go
	chans, err := ap.getChannelsMultiType(ctx, channelTypes)
	if err != nil {
		// The fetch is incomplete. Installing a truncated snapshot would
		// atomically replace a good cache with a bad one, so leave the
		// existing snapshot alone and let the caller decide.
		return chans, err
	}

	// Build new snapshot with all fetched channels
	...
	ap.channelsSnapshot.Store(newSnapshot)

	return chans, nil
```

The snapshot `Store` **must** be unreachable when `err != nil`. That single
guard is the core of this plan.

**Verify**:
- `go build ./...` → exit 0
- `go vet ./...` → exit 0

### Step 4: Extend the `fetchAndStoreChannels` guard

Capture the error and treat "incomplete" exactly the way "empty" is already
treated — keep the existing cache if there is one, otherwise fail:

```go
	channels, err := ap.GetChannels(ctx, AllChanTypes)

	if err != nil {
		if ap.channelsReady.Load() {
			ap.logger.Warn("Channel fetch incomplete, keeping existing cache",
				zap.Int("partialCount", len(channels)),
				zap.Error(err))
			return nil
		}
		return fmt.Errorf("channel fetch incomplete and no existing cache is available: %w", err)
	}

	if len(channels) == 0 {
		... existing block, unchanged ...
	}
```

Order matters: check `err` **before** `len(channels) == 0`, so a failure on the
very first page reports the real cause rather than "returned zero channels".

Leave the `len(channels) == 0` block byte-identical.

Nothing below that point changes — a successful complete fetch marshals,
writes, and marks ready exactly as before.

`fmt` is already imported.

**Verify**:
- `go build ./...` → exit 0
- `make test` → exit 0 (the pre-existing suite must still pass untouched; if
  any existing test fails, STOP and report which one)

### Step 5: Tests

New file `pkg/provider/api_channel_fetch_test.go`, package `provider`, testify
style. Read `api_patch_test.go` first for `newTestApiProvider` and the
embed-and-override mock, and reuse them.

Build a mock overriding only `GetConversationsContext`, scripted with a slice
of canned pages so it can return page 1 successfully and then fail:

```go
type fakeChannelsClient struct {
	SlackAPI // embed interface to satisfy all methods; only override what we need
	pages    []channelsPage
	calls    int
}

type channelsPage struct {
	channels []slack.Channel
	cursor   string
	err      error
}
```

Four subtests:

1. **`partial fetch returns an error`** — page 1 succeeds with a cursor, page 2
   returns an error. Assert `getChannelsMultiType` returns a non-nil error
   **and** the page-1 channels (partial data is returned, not discarded).

2. **`partial fetch does not replace the snapshot`** — pre-load
   `ap.channelsSnapshot` with a known-good two-channel cache, run `GetChannels`
   against the same failing mock, then assert `ap.ProvideChannelsMaps()` still
   returns the original two channels. **This is the regression test that
   matters most** — it is the exact corruption described at the top of this
   plan.

3. **`complete fetch replaces the snapshot`** — two successful pages, second
   with an empty cursor. Assert nil error, all channels returned, and the
   snapshot now holds them. This pins that the happy path did not regress.

4. **`slackRetryAfter`** — a `*slack.RateLimitedError` yields its `RetryAfter`;
   an unrelated error yields `0`.

A note on subtest 1: the retry added in Step 2 means a **rate-limit** error is
attempted three times before giving up, and `CallWithRetry` really sleeps for
`RetryAfter` between attempts. Use a **plain non-retryable error**
(`errors.New("boom")`) for subtests 1 and 2 so they fail fast with no sleeping.
If you also want to cover the retry-then-give-up path, use a
`&slack.RateLimitedError{RetryAfter: time.Millisecond}` so the test stays fast.
**Do not** write a test that sleeps for seconds.

**Verify**:
- `go test -count=1 -race -run 'TestUnit' -v ./pkg/provider/` → PASS
- `make test` → exit 0

### Step 6: Prove the regression test catches the old behavior

Temporarily restore the unconditional `ap.channelsSnapshot.Store(newSnapshot)`
in `GetChannels` (drop the Step 3 error guard, keeping signatures so it still
compiles). Run subtest 2. It **must fail**. Then restore the guard and confirm
it passes again.

Report both observed outcomes. If subtest 2 passes with the guard removed, the
test is not testing what it claims — say so plainly rather than moving on.

**Verify**: after restoring, `go test -count=1 -race ./pkg/provider/` → exit 0

### Step 7: Final

**Verify**:
- `make test` → exit 0
- `gofmt -l pkg/provider/` → no output
- `git status --porcelain go.mod go.sum` → no output
- `git diff 5c936ec..HEAD --stat` → exactly two files: `pkg/provider/api.go`
  and `pkg/provider/api_channel_fetch_test.go`

## Test plan

Four subtests in one new file, listed in Step 5. They pin:

- an incomplete pagination reports an error rather than passing silently;
- partial data is still returned alongside that error;
- a failed fetch leaves the existing snapshot intact (**the regression**);
- a complete fetch still installs the new snapshot.

No existing test changes. `api_cache_test.go` and `api_channels_test.go` test
logic mirrors, not these functions, so they should pass untouched — if either
fails, that is a signal you changed more than intended.

Not covered, deliberately: that `Tier2` produces any particular wall-clock
pacing. Timing assertions are flaky; the tier change is verified by reading the
diff.

## Done criteria

- [ ] `grep -n 'Tier2boost' pkg/provider/api.go` → no output
- [ ] `grep -c 'limiter.Tier2.Limiter()' pkg/provider/api.go` → `1`
- [ ] `getChannelsMultiType`, `GetChannelsType` and `GetChannels` all return
      `([]Channel, error)`
- [ ] In `GetChannels`, `ap.channelsSnapshot.Store` is unreachable when the
      fetch returned an error
- [ ] `fetchAndStoreChannels` checks the error **before** the
      `len(channels) == 0` block, and that block is otherwise unchanged
- [ ] `go build ./...` → exit 0
- [ ] `go vet ./...` → exit 0
- [ ] `gofmt -l pkg/provider/` → no output
- [ ] `git status --porcelain go.mod go.sum` → no output
- [ ] `go test -count=1 -race ./pkg/provider/` → exit 0
- [ ] `make test` → exit 0
- [ ] `git diff 5c936ec..HEAD --stat` touches exactly `pkg/provider/api.go` and
      `pkg/provider/api_channel_fetch_test.go`
- [ ] Your report states the Step 6 result explicitly: did subtest 2 fail with
      the guard removed, yes or no
- [ ] No test in the suite takes more than ~2 seconds

> Reviewer note on the two `grep -c` criteria above: both were run against
> `5c936ec` while writing this plan and the pre-fix counts are
> `Tier2boost` = 1 and `limiter.Tier2.Limiter()` = 0. If your observed counts
> disagree with that starting point, the criterion is wrong — report it rather
> than bending the code to match.

## STOP conditions

- The caller grep in "Current state" returns a caller not in the table. Report
  what you found before changing any exported signature.
- Removing the loop's leading `lim.Wait(ctx)` in favor of `CallWithRetry`'s
  internal wait changes context-cancellation behavior in a way you cannot make
  equivalent. Report; do not leave both waits in place (that would double the
  pacing).
- Any existing test in `pkg/provider/` fails. Name it and stop — this plan
  should not require editing an existing test.
- `go.mod` or `go.sum` changes. Restore them and report.
- You conclude the users-cache path needs the same fix. It very likely does —
  **note it in your report and do not touch it.**
- Making subtest 2 pass requires more than the Step 3 guard. Report what else
  was needed; that means the corruption has a second route this plan missed.

## Maintenance notes

- The invariant this plan establishes: **a truncated channel list must never
  reach `channelsSnapshot.Store` or `atomicWriteFile` with a nil error.** Any
  future change to the fetch path should be reviewed against exactly that.
- The users-cache path (`fetchAndStoreUsers` and friends) is structurally
  identical and almost certainly has the same defect. It is deliberately out of
  scope here and should become its own plan.
- Plan 030's open question about `conversations.list`'s tier is answered by
  this plan: `Tier2`, on the evidence that the real cost is seconds, not
  minutes. 030's table of the other twelve limiter call sites still stands, and
  the per-invocation-limiter finding is still unaddressed everywhere else.
- Reviewer: confirm the diff is two files; that the `len(channels) == 0` block
  in `fetchAndStoreChannels` is byte-identical to `5c936ec`; and that no test
  sleeps for a real `RetryAfter` of more than a few milliseconds.
