# Plan 029: `edge.UsersList` has a data race on its result slice and runs two independent rate limiters

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `420e4d1`. If it is anything else — in particular if it is `adbae97`, the
> repository's `master` — you are on the wrong base. Run:
>
> ```
> git fetch origin
> git checkout -B advisor/029-userslist-race-and-shared-limiter 420e4d1
> git rev-parse --short HEAD    # must now print 420e4d1
> ```
>
> This plan sits on the **Track A** tip — the `pkg/provider/` + Makefile stack
> (007 → 013 → 017 → 008 → 019 → 018 → 028). It is **not** on the Track B
> stack (`pkg/handler/`, `pkg/server/`) where plans 023–027 live. Track A is
> where the edge test harness this plan depends on lives.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: plan 019 (provides the `newFixtureClient` / `jsonResponse`
  test harness in `pkg/provider/edge/edge_test.go` that Step 3 uses) and plan
  018 (puts `-race` on `make test`, which is what gives the new test its
  teeth). Both are already in the base commit `420e4d1`.
- **Category**: correctness / concurrency
- **Planned at**: commit `420e4d1`, 2026-08-07

## Why this matters

`edge.UsersList` fans out into two `errgroup` goroutines and both of them
append into the **same** `uu` slice variable with no synchronization:

```go
	var uu []User
	eg, ctx := errgroup.WithContext(ctx)
	if len(channelIDs) > 0 {
		eg.Go(func() error {
			u, err := cl.publicUserList(ctx, channelIDs)
			if err != nil {
				return err
			}
			uu = append(uu, u...)   // <-- unsynchronized write
			return nil
		})
	}
	if len(dmIDs) > 0 {
		eg.Go(func() error {
			u, err := cl.directUserList(ctx, dmIDs)
			if err != nil {
				return err
			}
			uu = append(uu, u...)   // <-- unsynchronized write, same variable
			return nil
		})
	}
```

`append` reads `uu`'s length, pointer and capacity, then writes all three
back. Two goroutines doing that concurrently is a textbook data race: the
losing write can silently discard the other goroutine's entire result, or
produce a slice header whose length exceeds its allocation.

**Honest severity note — read this before assuming it is on fire.** The race
is currently *unreachable through the only production caller*.
`pkg/provider/edge/slacker.go:117` calls `cl.UsersList(ctx, p.ChannelID)` with
exactly one ID, and `splitDMs` (bottom of `userlist.go`) routes each ID into
exactly one of the two buckets. With one ID, only one bucket is non-empty, so
only one `eg.Go` ever runs and the two writes never overlap. So this is a
**latent** defect in an exported, variadic API — not a live production bug.

It is still worth fixing, for three reasons:

1. `UsersList` is exported and its signature (`channelIDs ...string`) actively
   invites multi-ID calls. The next caller that passes a channel and a DM
   together trips it.
2. The fix is small, local, and removes shared mutable state rather than
   papering over it with a mutex.
3. The same function has a second, *already-live* defect described next, and
   both live in the same twenty lines.

### The second defect: two independent rate limiters against one budget

`publicUserList` and `directUserList` each construct their own limiter:

- `pkg/provider/edge/userlist.go:239` — `lim := limiter.Tier3.Limiter()`
- `pkg/provider/edge/userlist.go:268` — `lim := limiter.Tier3.Limiter()`

`limiter.Tier3` is `tier{t: 1200 * time.Millisecond, b: 4}`, and
`tier.Limiter()` calls `rate.NewLimiter(...)` — it returns a **brand new**
limiter every call, not a shared one. So when both goroutines run, the process
issues requests at twice the intended Tier 3 rate against the same token.
Slack's rate limits are per token and per method, not per goroutine, so two
limiters do not mean two budgets.

Unlike the race, this one has no "only one goroutine runs" escape clause in
principle — it is simply also masked today by the single-ID caller. Fixing it
means threading one limiter through both helpers.

## Current state

Verified by reading `pkg/provider/edge/userlist.go` at `420e4d1`. (The file is
byte-identical at `060b6ef`, two commits earlier, if you need to cross-check.)

`UsersList`, lines 186–217:

```go
// UserList lists users in the conversation(s).
func (cl *Client) UsersList(ctx context.Context, channelIDs ...string) ([]User, error) {
	if len(channelIDs) == 0 {
		return nil, errors.New("no channel IDs provided")
	}
	channelIDs, dmIDs := splitDMs(channelIDs)
	var uu []User
	eg, ctx := errgroup.WithContext(ctx)
	if len(channelIDs) > 0 {
		eg.Go(func() error {
			u, err := cl.publicUserList(ctx, channelIDs)
			if err != nil {
				return err
			}
			uu = append(uu, u...)
			return nil
		})
	}
	if len(dmIDs) > 0 {
		eg.Go(func() error {
			u, err := cl.directUserList(ctx, dmIDs)
			if err != nil {
				return err
			}
			uu = append(uu, u...)
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return uu, nil
}
```

`publicUserList`, lines 219–255 (limiter at 239, wait at 253):

```go
func (cl *Client) publicUserList(ctx context.Context, channelIDs []string) ([]User, error) {
	const (
		// everyone = "everyone AND NOT bots AND NOT apps"
		everyone = "everyone"
		filter   = "people"
		index    = "users_by_display_name"

		count = 50
	)
	req := UsersListRequest{
		Channels:     channelIDs,
		Filter:       everyone,
		PresentFirst: false,
		Index:        index,
		Locale:       "en-US",
		Marker:       "",
		Count:        count,
	}
	uu := make([]User, 0, count)
	lim := limiter.Tier3.Limiter()
	for {
		var ur UsersListResponse
		if err := cl.callEdgeAPI(ctx, &ur, "users/list", &req); err != nil {
			return nil, err
		}
		if len(ur.Results) == 0 && ur.NextMarker == "" {
			break
		}
		uu = append(uu, ur.Results...)
		if ur.NextMarker == "" {
			break
		}
		req.Marker = ur.NextMarker
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
	}
	return uu, nil
}
```

`directUserList`, lines 257–280 (limiter at 268):

```go
// directUserList tries to get users from the direct message channels.  It is
// much slower than getting users from the public channels, as it uses
// conversations.view endpoint.
func (cl *Client) directUserList(ctx context.Context, dmIDs []string) ([]User, error) {
	if len(dmIDs) == 0 {
		return nil, errors.New("no direct message IDs provided")
	}
	var ret []User
	lim := limiter.Tier3.Limiter()
	for _, id := range dmIDs {
		resp, err := cl.ConversationsView(ctx, id)
		if err != nil {
			return nil, err
		}
		ret = append(ret, resp.Users...)
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
	}
	return ret, nil
}
```

`splitDMs`, lines 282–296 — an ID beginning with `D` is a DM, everything else
is a channel, empty strings are dropped:

```go
func splitDMs(IDs []string) (chans []string, dms []string) {
	for _, id := range IDs {
		if len(id) == 0 {
			continue
		}
		if len(id) > 0 && id[0] == 'D' {
			dms = append(dms, id)
		} else {
			chans = append(chans, id)
		}
	}
	return chans, dms
}
```

Current imports of `userlist.go` (lines 1–10):

```go
package edge

import (
	"context"
	"errors"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/rusq/slack"
	"golang.org/x/sync/errgroup"
)
```

Note the line numbers above are from `420e4d1`. Re-verify them before editing —
`grep -n 'func (cl \*Client) UsersList' pkg/provider/edge/userlist.go` — and
edit by matching the code text, not by line number.

## Repo conventions to follow

- Go 1.25, standard `gofmt`. Run `gofmt -l pkg/provider/edge/` and expect no
  output before committing.
- Comments in this package are full sentences explaining *why*, not *what*.
  Match that: the new comments should say why one limiter is shared, not
  restate that a limiter is shared.
- Tests in `pkg/provider/edge/` are table-free plain `t.Run` subtests using
  the standard library only — `t.Errorf` / `t.Fatalf`, **not** testify — in
  `edge_test.go`. (`slacker_test.go` does use testify; `edge_test.go` does
  not. Follow `edge_test.go`, since that is where the fixture harness lives.)
- Test names in `edge_test.go` are prefixed `TestUnit...`. The `make test`
  target skips anything matching `Integration`, so a unit test must not have
  that word in its name.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0, no output |
| Vet | `go vet ./...` | exit 0, no output |
| Format check | `gofmt -l pkg/provider/edge/` | no output |
| Race test, this package | `go test -count=1 -race -run 'TestUnit' ./pkg/provider/edge/` | `ok`, exit 0 |
| Full suite | `make test` | exit 0 |

`make test` at this commit is `go test -count=1 -race -v -skip="Integration" ./...`
(plan 018 added `-race`), so it already exercises the race detector. The
per-package `go test -race` commands above are still the faster inner loop —
use them while iterating, and `make test` as the final gate.

## Scope

**In scope**:
- `pkg/provider/edge/userlist.go` — `UsersList`, `publicUserList`,
  `directUserList`, and the import block.
- `pkg/provider/edge/edge_test.go` — one new test.

**Out of scope — do not touch**:
- `splitDMs`. Its behavior is correct and one existing caller depends on it.
- `pkg/provider/edge/slacker.go`. The single-ID call site stays as it is;
  this plan does not change who calls `UsersList` or how.
- Every other `limiter.TierN.Limiter()` call site in the repo. There are
  eleven more, and they have the same per-invocation-limiter shape. They are
  a separate design question — see plan 030. **Changing them is out of scope
  and will fail review.**
- The choice of `Tier3` for these two edge endpoints. Other edge calls use
  `Tier2boost`; whether `Tier3` is the right tier here is a question for plan
  030, not a change to make now. Keep `Tier3`.
- `pkg/limiter/` itself — no changes to `limits.go` or `retry.go`.

## Git workflow

- Branch: `advisor/029-userslist-race-and-shared-limiter`, based on `420e4d1`.
- One commit, imperative subject line. Do **not** push. Do **not** merge into
  `master`.

## Steps

### Step 1: Thread a single limiter through both helpers

Change the two helper signatures to accept a limiter instead of creating one.

`publicUserList`: change

```go
func (cl *Client) publicUserList(ctx context.Context, channelIDs []string) ([]User, error) {
```

to take a third parameter `lim *rate.Limiter`, and **delete** the line
`lim := limiter.Tier3.Limiter()` from its body. Leave the `lim.Wait(ctx)` call
at the bottom of the loop exactly where it is.

`directUserList`: make the identical change — add the `lim *rate.Limiter`
parameter, delete its `lim := limiter.Tier3.Limiter()` line, leave its
`lim.Wait(ctx)` alone.

Add `"golang.org/x/time/rate"` to the import block. It goes in the third-party
group with the existing `github.com/...` and `golang.org/x/sync/errgroup`
imports; `gofmt` will not reorder groups for you, so place it after
`golang.org/x/sync/errgroup` to keep the group alphabetized.

`golang.org/x/time` is already a module dependency (`pkg/limiter` imports it),
so **`go.mod` and `go.sum` must not change.** If they do, you have added the
wrong import path — stop and re-check.

**Verify**:
- `go build ./...` → exit 0
- `grep -c 'limiter.Tier3.Limiter()' pkg/provider/edge/userlist.go` → `1`
  (the one remaining occurrence is the one you add in Step 2; if you run this
  check before Step 2 it will print `0`, which is also correct at that point)
- `git status --porcelain go.mod go.sum` → no output

### Step 2: Remove the shared `uu` and construct the limiter once

Rewrite `UsersList` so that each goroutine writes to its own variable and the
results are joined after `eg.Wait()` returns. The whole point is that no
variable is written by two goroutines — do **not** solve this with a
`sync.Mutex`; removing the shared state is simpler and is what review expects.

The replacement body:

```go
// UserList lists users in the conversation(s).
func (cl *Client) UsersList(ctx context.Context, channelIDs ...string) ([]User, error) {
	if len(channelIDs) == 0 {
		return nil, errors.New("no channel IDs provided")
	}
	channelIDs, dmIDs := splitDMs(channelIDs)

	// Both branches hit the same workspace token, and Slack meters requests
	// per token rather than per goroutine, so they share one limiter. Two
	// limiters would let the pair issue requests at twice the Tier 3 rate.
	lim := limiter.Tier3.Limiter()

	// Each goroutine writes only its own slice; they are joined below, after
	// eg.Wait, so there is no shared mutable state to synchronize.
	var pub, dms []User

	eg, ctx := errgroup.WithContext(ctx)
	if len(channelIDs) > 0 {
		eg.Go(func() error {
			u, err := cl.publicUserList(ctx, channelIDs, lim)
			if err != nil {
				return err
			}
			pub = u
			return nil
		})
	}
	if len(dmIDs) > 0 {
		eg.Go(func() error {
			u, err := cl.directUserList(ctx, dmIDs, lim)
			if err != nil {
				return err
			}
			dms = u
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return append(pub, dms...), nil
}
```

Two behaviors are deliberately preserved and must not drift:

- **Error handling is unchanged**: if either goroutine fails, `eg.Wait()`
  returns that error and `UsersList` returns `nil, err`. It does not return
  partial results. (A different function in this package, the one plan 019's
  `TestPartialResultCollection` covers, *does* collect partial results — that
  is `GetConversationsContext`, not this one. Do not import that behavior
  here.)
- **Ordering is unchanged**: public-channel users come before DM users, which
  is the order the old sequential-in-practice code produced.

Note `dms` shadows nothing — the existing local is `dmIDs`, a different name.
Keep both.

**Verify**:
- `go build ./...` → exit 0
- `go vet ./...` → exit 0
- `grep -c 'uu = append' pkg/provider/edge/userlist.go` → `0`
- `grep -c 'sync.Mutex\|sync.RWMutex' pkg/provider/edge/userlist.go` → `0`
- `gofmt -l pkg/provider/edge/` → no output

### Step 3: Add a race test that exercises both goroutines at once

Plan 019 added a fixture harness to `pkg/provider/edge/edge_test.go`. Read
these three helpers before writing anything — they are already in that file
and you must reuse them rather than write your own:

- `roundTripperFunc` — a `func(*http.Request) (*http.Response, error)` that
  satisfies `http.RoundTripper`.
- `jsonResponse(status int, body string) *http.Response` — builds a canned
  JSON response.
- `newFixtureClient(t *testing.T, fake roundTripperFunc) *Client` — builds a
  `*Client` via `NewWithClient("testws", "T123", "xoxc-test", ...)` whose
  transport is your fake.

`TestUnitClientCounts` in the same file is the pattern to copy for structure.

The two code paths hit different URLs, which is how the fake tells them apart:

- `publicUserList` → `callEdgeAPI(ctx, &ur, "users/list", &req)` → a POST whose
  URL path contains `users/list`.
- `directUserList` → `ConversationsView` → `PostForm(ctx, "conversations.view", ...)`
  → a POST whose URL path contains `conversations.view`.

**Confirm this before relying on it.** Write the fake to `t.Logf("%s", r.URL.String())`
on the first run, run the test once, and read the two actual URLs out of the
output. If the paths differ from what is written above, match on whatever the
log actually shows. Do not guess.

Write a test named `TestUnitUsersListConcurrentBuckets` that:

1. Builds a fixture client whose `roundTripperFunc` inspects `r.URL.Path`:
   - contains `users/list` → return `jsonResponse(200, ...)` with a body
     containing one user and **no** `next_marker`, so `publicUserList` stops
     after one page.
   - contains `conversations.view` → return `jsonResponse(200, ...)` with a
     body containing one *different* user.
   - anything else → `t.Errorf` with the URL and return a 200 `{"ok":true}`.
2. Calls `cl.UsersList(context.Background(), "C111", "D222")` — one channel ID
   and one DM ID, so `splitDMs` fills **both** buckets and both goroutines run.
   This is the whole point of the test; a single-ID call proves nothing.
3. Asserts `err == nil` and that the returned slice has length 2, and that it
   contains both user IDs.

For the response bodies, derive the JSON shape from the Go structs rather than
inventing it: read `UsersListResponse` (top of `userlist.go`) and
`ConversationsViewResponse` (in `conversations.go`) and use their `json:` tags.
`ParseResponse` requires the envelope to carry `"ok": true` — every other
fixture body in `edge_test.go` includes it, so include it too.

Getting the fixture JSON right may take a couple of iterations. If after
reasonable effort you cannot get `conversations.view` to yield a user through
`ConversationsView`, **degrade the test rather than abandoning it**: have the
`conversations.view` fake return a valid `{"ok":true}` envelope with an empty
user list, assert length 1 instead of 2, and say so in your report. Both
goroutines still run, so the race detector still gets its chance — that is the
property under test. Do not delete the test.

**Verify**:
- `go test -count=1 -race -run 'TestUnitUsersListConcurrentBuckets' -v ./pkg/provider/edge/`
  → `PASS`, exit 0, and **no** `WARNING: DATA RACE` in the output

### Step 4: Prove the test would have caught the old bug

This step is a check on the test, not on the fix. Do it, record the result,
then undo it.

1. Temporarily revert `UsersList` to the old shared-`uu` form (keep the new
   helper signatures so it still compiles — just reintroduce
   `var uu []User` plus the two `uu = append(uu, u...)` writes).
2. Run `go test -count=1 -race -run 'TestUnitUsersListConcurrentBuckets' ./pkg/provider/edge/`
   **ten times**:
   ```
   go test -count=10 -race -run 'TestUnitUsersListConcurrentBuckets' ./pkg/provider/edge/
   ```
3. Record whether `WARNING: DATA RACE` appears.
4. **Restore the Step 2 version.** Confirm with
   `grep -c 'uu = append' pkg/provider/edge/userlist.go` → `0`.

The race detector only reports races it actually observes, and two goroutines
that each make one fast fake HTTP call may not interleave. So a **clean** run
here is a real possible outcome and is **not** a failure of this plan — report
it honestly either way. Do not add sleeps, `runtime.Gosched()`, or retry loops
to force an interleaving; that would be tuning the test to the bug rather than
testing the contract.

**Verify**: after the restore, `git diff` shows `UsersList` in its Step 2 form
and `go test -count=1 -race -run 'TestUnit' ./pkg/provider/edge/` passes.

### Step 5: Full suite

**Verify**:
- `make test` → exit 0
- `git diff 420e4d1..HEAD --stat` → exactly two files:
  `pkg/provider/edge/userlist.go` and `pkg/provider/edge/edge_test.go`

## Test plan

One new test, `TestUnitUsersListConcurrentBuckets`, in
`pkg/provider/edge/edge_test.go`, following the `TestUnitClientCounts`
structure and using the `newFixtureClient` / `jsonResponse` /
`roundTripperFunc` helpers already in that file.

What it pins:
- `UsersList` with both a channel ID and a DM ID runs both goroutines and
  returns both sets of users, in public-then-DM order.
- Run under `-race`, it exercises the code path where the old shared-`uu`
  append could be flagged.

What it does **not** pin, and deliberately so: that the two goroutines observe
a shared limiter. Rate-limiter sharing is not observable through this API
without timing assertions, and timing assertions are flaky. The shared limiter
is verified by reading the diff, not by a test.

No existing test changes. `slacker_test.go` does not touch `UsersList`.

## Done criteria

- [ ] `grep -c 'uu = append' pkg/provider/edge/userlist.go` → `0`
- [ ] `grep -c 'sync.Mutex\|sync.RWMutex' pkg/provider/edge/userlist.go` → `0`
- [ ] `grep -c 'limiter.Tier3.Limiter()' pkg/provider/edge/userlist.go` → `1`
- [ ] `publicUserList` and `directUserList` both take a `*rate.Limiter`
      parameter and neither constructs a limiter
- [ ] `go build ./...` → exit 0
- [ ] `go vet ./...` → exit 0
- [ ] `gofmt -l pkg/provider/edge/` → no output
- [ ] `git status --porcelain go.mod go.sum` → no output
- [ ] `go test -count=1 -race -run 'TestUnitUsersListConcurrentBuckets' -v ./pkg/provider/edge/`
      → PASS, no `WARNING: DATA RACE`
- [ ] `make test` → exit 0
- [ ] `git diff 420e4d1..HEAD --stat` touches exactly
      `pkg/provider/edge/userlist.go` and `pkg/provider/edge/edge_test.go`
- [ ] Your report states the Step 4 result explicitly: whether the pre-fix
      code tripped the race detector in ten runs, yes or no

## STOP conditions

- `newFixtureClient`, `jsonResponse`, or `roundTripperFunc` is not present in
  `pkg/provider/edge/edge_test.go`. That means plan 019 is not in your base.
  Re-check `git rev-parse --short HEAD` against `420e4d1` and stop.
- `go.mod` or `go.sum` changes at any point. Restore them
  (`git checkout -- go.mod go.sum`) and report — the `golang.org/x/time/rate`
  import should require no module change.
- `UsersList` turns out to have callers other than
  `pkg/provider/edge/slacker.go:117`. Verify with
  `grep -rn 'UsersList(' --include='*.go' .` and report what you found before
  changing the signature of anything exported.
- Making the two helpers share a limiter requires changing anything in
  `pkg/limiter/`. It should not — `tier.Limiter()` already returns a
  `*rate.Limiter` you can pass around. If you think it does, stop and explain.
- The new test cannot be made to pass without asserting on timing or sleeps.
  Report rather than committing a flaky test.

## Maintenance notes

- After this change, `UsersList` is the only place in `pkg/provider/edge/` that
  constructs a limiter for a multi-goroutine fan-out. If a third bucket is ever
  added to the fan-out, it takes the same `lim` — do not let a new `eg.Go`
  branch call `limiter.Tier3.Limiter()` for itself.
- The pattern to watch for in review, repo-wide: `eg.Go` or `go func` closures
  that append to a slice declared in the enclosing function. That is the same
  bug shape. `-race` only catches it when a test actually runs both branches
  concurrently, which is rare — reading for it is more reliable than testing
  for it.
- The broader question this plan deliberately does not answer — that *every*
  `limiter.TierN.Limiter()` call site builds a fresh, per-invocation limiter,
  so the process has no global rate budget — is written up in plan 030. This
  plan fixes one function; 030 decides whether the architecture changes.
- Reviewer: confirm the diff is two files, that no mutex was introduced, and
  that the error path still returns `nil, err` rather than partial results.
