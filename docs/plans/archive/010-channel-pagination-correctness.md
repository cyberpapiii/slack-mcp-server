# Plan 010: Stop channel pagination losing rows (`channels_me`) and looping on bad cursors (`channels_list`)

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
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- pkg/handler/channels.go`
> On any change, compare the excerpts below against the live code; mismatch = STOP.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

Two pagination defects that silently corrupt an agent's view of the workspace:

1. **`channels_me` permanently skips channels.** The handler fetches Slack
   pages of 200, stops once it has accumulated `limit` rows, returns rows
   `1..limit` — but hands back the cursor positioned after the *entire
   fetched page*. With the default `limit=100`: call 1 returns channels
   1–100 with a cursor pointing after channel 200; call 2 resumes at 201.
   Channels 101–200 are invisible to any paginating client. Silent data loss.
2. **`channels_list` treats an undecodable cursor as "restart at page 1"**,
   logging a warning and returning the first page *with a next-cursor
   attached*. An agent paginating in a loop gets the first page forever — an
   infinite context-burning loop instead of an error. The search tool handles
   the same case correctly by returning an "invalid cursor" error
   (`pkg/handler/conversations.go:2541-2556`), so this is an inconsistency,
   not a design choice.

## Current state

`pkg/handler/channels.go` at commit `adbae97`.

**Defect 1** — `ChannelsMyHandler`'s fetch loop:

```go
// channels.go:281-308
	for {
		params := &slack.GetConversationsForUserParameters{
			Types:           channelTypes,
			Limit:           200,
			Cursor:          apiCursor,
			ExcludeArchived: true,
		}
		channels, nextCursor, err := ch.apiProvider.Slack().GetConversationsForUserContext(ctx, params)
		...
		// Early exit: stop paginating through the Slack API once we have enough.
		if len(allChannels) >= limit {
			slackNextCursor = nextCursor
			break
		}
		if nextCursor == "" {
			break
		}
		apiCursor = nextCursor
	}
```

Then truncation + cursor stamping:

```go
// channels.go:313-330
	end := limit
	if end > len(allChannels) {
		end = len(allChannels)
	}
	...
	for _, channel := range allChannels[:end] { ... }
	if len(channelList) > 0 && slackNextCursor != "" {
		channelList[len(channelList)-1].Cursor = slackNextCursor
	}
```

**Defect 2** — `paginateChannels` (used by `channels_list`, which paginates
an in-memory cache with a base64-of-channel-ID cursor):

```go
// channels.go:504-531
func paginateChannels(channels []provider.Channel, cursor string, limit int) ([]provider.Channel, string) {
	...
	startIndex := 0
	if cursor != "" {
		if decoded, err := base64.StdEncoding.DecodeString(cursor); err == nil {
			...
		} else {
			logger.Warn("Failed to decode cursor", ...)
		}
	}
```

Find `paginateChannels`'s callers with `grep -n 'paginateChannels(' pkg/handler/`
before changing its signature.

Conventions: `TestUnit*` tests run under `make test`; `channels_test.go`
currently holds mostly `TestIntegration*` tests plus
`TestUnitClassifyChannelType` (line 141) — model new unit tests on the latter.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnitPaginateChannels' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/handler/channels.go` (`ChannelsMyHandler` fetch loop, `paginateChannels` + its callers in this file)
- `pkg/handler/channels_test.go`

**Out of scope**:
- The base64 cursor *encoding scheme* itself (a broader cursor-unification
  idea was surfaced and deliberately not planned — do not add cursor
  prefixes/tags here).
- `conversations.go` search pagination (already correct).
- `channels_me`'s pass-through of the `cursor` param to Slack (correct).

## Git workflow

- Branch: `advisor/010-channel-pagination-correctness`
- One commit per defect or one combined; imperative subjects. Do NOT push.

## Steps

### Step 1: Make `channels_me` request exactly what it needs

Change the fetch loop so it can never overshoot: request
`Limit: remaining` per iteration, where
`remaining = limit - len(allChannels)` capped at 200 (Slack's max page).
Because the API then never returns more rows than requested, when the loop
exits via `len(allChannels) >= limit` the `nextCursor` from the final request
is positioned exactly after the last *returned* row — the truncation slice
becomes a no-op safeguard, and the stamped cursor is correct. Keep the
`nextCursor == ""` exit unchanged.

**Verify**: `go build ./...` → exit 0

### Step 2: Make `paginateChannels` reject bad cursors

Change the signature to
`paginateChannels(channels []provider.Channel, cursor string, limit int) ([]provider.Channel, string, error)`.
On a base64 decode failure return
`nil, "", fmt.Errorf("invalid cursor: %q", cursor)` (match the wording used by
the search handler's invalid-cursor error so agents see one consistent
message). Update every caller (found via the grep above) to propagate the
error as a tool error result, the same way surrounding code in the caller
handles other errors.

**Verify**: `go build ./...` → exit 0

### Step 3: Unit tests

`paginateChannels` is pure — test it directly in `channels_test.go`:

- `TestUnitPaginateChannelsInvalidCursor`: garbage cursor → error, no rows.
- `TestUnitPaginateChannelsRoundTrip`: 5 channels, limit 2 → page 1 rows
  0–1 with cursor; feeding that cursor back → rows 2–3; final page → row 4,
  empty next cursor.
- For Step 1, the loop needs a Slack API fake; if
  `ch.apiProvider.Slack()` cannot be faked without new scaffolding, extract
  the per-iteration page-size computation into a tiny pure helper
  (`nextPageSize(limit, have int) int`) used by the loop, and table-test that
  (limit 100 → first request 100; limit 250 → 200 then 50; etc.). Note in
  your report which route you took.

**Verify**: `go test -count=1 -run 'TestUnitPaginateChannels|TestUnitNextPageSize' ./pkg/handler/` → pass

### Step 4: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 3: invalid-cursor regression, multi-page round-trip over the pure
paginator, page-size arithmetic for the `channels_me` loop.

## Done criteria

- [ ] `make test` exits 0; new tests pass
- [ ] `paginateChannels` returns an error type (signature check by reading the diff)
- [ ] In `ChannelsMyHandler`, no request uses a hardcoded `Limit: 200` independent of `limit`
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- Excerpts don't match the live code (drift).
- `paginateChannels` turns out to have callers outside `channels.go` (grep
  first; if any exist in other files, report before changing the signature —
  they may be out of scope).
- Slack's API demonstrably ignores `Limit` values below some floor (if you
  find evidence of that in the vendored slack-go code, report rather than
  guessing a workaround).

## Maintenance notes

- A future "unify the three cursor schemes" change (surfaced in the
  2026-08-07 audit, not planned) would subsume Defect 2's error handling —
  if that lands, keep the error-on-invalid behavior.
- Reviewer: check the Step 1 loop still terminates when Slack returns an
  empty page with a non-empty cursor (the `nextCursor == ""` exit plus
  requesting `remaining > 0` covers it; an empty page with a cursor would
  loop — if you see that risk in the final code, add a zero-progress guard).
- Note (do not "fix"): `channels_list` re-sorts its page by popularity
  *after* the ID-ordered cursor is computed (`channels.go:217-230`) — the
  cursor value is still correct; this is benign.
