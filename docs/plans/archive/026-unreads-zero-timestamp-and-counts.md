# Plan 026: `conversations_unreads` — zero `last_read` renders as garbage, and the unread-count fallbacks disagree

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `47c8c10`; otherwise STOP.

## Status

- **Priority**: P1 (unbounded API queries + a wrong number shown to the user)
- **Effort**: M
- **Risk**: MEDIUM — changes numbers the tool reports. Every behavior it
  changes is currently pinned by a plan-020 characterization test marked
  `// NOTE: characterizes current behavior, possibly wrong:`; each of those
  tests must be updated **deliberately** as part of this plan.
- **Depends on**: plan 025 (same file; stack after it)
- **Category**: correctness
- **Planned at**: commit `727b517`, 2026-08-07

## Why this matters

Three related defects, all pinned by plan 020's characterization tests:

1. **A missing `last_read` does not produce an empty string.** The code has a
   `LastRead == ""` branch that is **unreachable** for edge-sourced channels,
   so instead of the intended conservative fallback, `conversations.history` is
   called with `Oldest: "-62135596800.000000"` — an effectively unbounded
   query. This is the most consequential of the eight surprises plan 020 pinned.
2. **Summary mode and message mode disagree** about a channel with no
   countable unreads: summary renders `0 unread` for a channel that
   `client.counts` flagged as unread; the message path reports `1`.
3. **A successful zero-row fetch destroys a positive mention count.** A channel
   with `MentionCount: 5` whose history window returns no rows is reported as
   `1 unread`.

## Current state

All excerpts verified at `727b517`.

### Defect 1 — the zero timestamp

`fasttime.Time` is `type Time time.Time`
(`pkg/provider/edge/fasttime/fasttime.go:10`). Its renderer:

```go
// fasttime.go:31-33
// SlackString returns the time as a slack timestamp (i.e. "1234567890.123456").
func (t Time) SlackString() string {
	return Int2TS(time.Time(t).UnixMicro())
}
```

```go
// fasttime.go:38-49
func Int2TS(ts int64) string {
	const cut = 6
	s := strconv.FormatInt(ts, 10)
	l := len(s)
	if l < cut+1 {
		return ""
	}
	lo := s[l-cut:]
	hi := s[:l-cut]
	return hi + "." + lo
}
```

A zero `time.Time` is 1 January year 1, whose `UnixMicro()` is
`-62135596800000000`. That formats to an 18-character string, so `Int2TS`
happily returns **`-62135596800.000000`** — not `""`.

`collectUnreadChannels` stores exactly that
(`pkg/handler/conversations.go:1250-1257`, and the same two lines again in the
MPIM block at `:1286-1293` and the IM block at `:1329-1336`):

```go
		unreadChannels = append(unreadChannels, UnreadChannel{
			ChannelID:   snap.ID,
			ChannelName: channelName,
			ChannelType: channelType,
			UnreadCount: snap.MentionCount,
			LastRead:    snap.LastRead.SlackString(),
			Latest:      snap.Latest.SlackString(),
		})
```

Which makes this branch in `backfillUnreadCounts` dead
(`pkg/handler/conversations.go:1173-1179`):

```go
		if unreadChannels[i].LastRead == "" {
			// No last-read timestamp means we can't bound the query.
			// Conservatively report 1 unread since HasUnreads was true.
			unreadChannels[i].UnreadCount = 1
			backfilled++
			continue
		}
```

and sends the garbage value as `Oldest` (`conversations.go:1180-1186`) and
again on the message path (`conversations.go:1083-1088`):

```go
		historyParams := slack.GetConversationHistoryParameters{
			ChannelID: unreadChannels[i].ChannelID,
			Oldest:    unreadChannels[i].LastRead,
			Limit:     params.maxMessagesPerChannel,
			Inclusive: false,
		}
```

### Defects 2 and 3 — the count fallbacks

`pkg/handler/conversations.go:1121-1132`:

```go
func unreadCountFromHistory(current, msgCount int, fetchErr error) int {
	if fetchErr != nil {
		if current == 0 {
			return 1
		}
		return current
	}
	if msgCount == 0 {
		return 1
	}
	return msgCount
}
```

The `fetchErr != nil` arm correctly preserves a positive `current`. The
success arm does not — `msgCount == 0` returns `1` even when `current` is 5.
That is **defect 3**.

The message path calls it (`conversations.go:1097` and `:1103`). The summary
path does not — `backfillUnreadCounts` instead has
(`conversations.go:1193-1195`):

```go
		if len(history.Messages) > 0 {
			unreadChannels[i].UnreadCount = len(history.Messages)
		}
```

so a zero-row fetch leaves `UnreadCount` at 0 and the CSV renders `0`. That is
**defect 2**.

### The tests that pin all of this

Plan 020 added characterization tests in `pkg/handler/conversations_test.go`,
each marked with a comment beginning
`// NOTE: characterizes current behavior, possibly wrong:`. Find them:

```
grep -n 'characterizes current behavior' pkg/handler/conversations_test.go
```

Read every hit before editing anything. The ones this plan invalidates must be
rewritten to assert the new, correct behavior, and their `NOTE:` comment
removed or replaced with a note pointing at this plan. Any hit this plan does
**not** address must be left exactly as it is.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnit' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**: `pkg/handler/conversations.go` (the unreads pipeline only) and
`pkg/handler/conversations_test.go`.

**Out of scope**:
- `pkg/provider/edge/fasttime/` — do NOT add an `IsZero` method or change
  `Int2TS`. That package is shared with the edge client and sits on the other
  branch stack (Track A); editing it here risks a cross-track conflict for no
  benefit. Keep the fix inside `pkg/handler`.
- `sortChannelsByPriority`, the `channel_types` filter, and MPIM name
  rendering — plan 027 owns those.
- `getUnreadsViaConversationsInfo` (`conversations.go:1350`) — the non-edge
  fallback path. Read it to confirm it does not share the defective helper; if
  it does, report rather than expanding scope.

## Git workflow

- Branch: `advisor/026-unreads-zero-timestamp-and-counts`, based on `47c8c10`.
- One commit, imperative subject. Do NOT push.

## Steps

### Step 1: Render a zero timestamp as the empty string

Add an unexported helper in `pkg/handler/conversations.go`, near
`collectUnreadChannels`:

```go
// slackTS renders an edge-API timestamp, mapping the zero value to "" rather
// than to fasttime's literal rendering of year 1 ("-62135596800.000000").
// A zero LastRead means "never read"; callers treat "" as "no bound available"
// and must not pass it to conversations.history as Oldest.
func slackTS(t fasttime.Time) string {
	if time.Time(t).IsZero() {
		return ""
	}
	return t.SlackString()
}
```

Add the import `"github.com/korotovsky/slack-mcp-server/pkg/provider/edge/fasttime"`
to the file's import block (`time` is already imported; `edge` is already
imported as a sibling package).

Then replace **all six** `.SlackString()` calls in `collectUnreadChannels` —
the `LastRead` and `Latest` lines in each of the three blocks (channels at
`:1255-1256`, MPIMs at `:1291-1292`, IMs at `:1334-1335`) — with
`slackTS(snap.LastRead)` / `slackTS(snap.Latest)`.

Confirm you got all of them:

**Verify**: `grep -n 'SlackString()' pkg/handler/conversations.go` → no output; `go build ./...` → exit 0

### Step 2: Confirm the now-live branch is correct

With Step 1 in place, the `LastRead == ""` branch in `backfillUnreadCounts`
(`conversations.go:1173`) becomes reachable for the first time. Read it and
confirm it does the right thing: it sets `UnreadCount = 1` and skips the API
call. That is correct — leave the logic alone, but update its comment to note
it is reached when `client.counts` reported no last-read timestamp.

Then guard the **message** path the same way. At `conversations.go:1083-1088`
the same empty `LastRead` would now be sent as `Oldest: ""`, which Slack
interprets as "from the beginning of the channel". Before building
`historyParams`, add:

```go
		if unreadChannels[i].LastRead == "" {
			// No last-read bound: fetching from the beginning of the channel
			// would be unbounded and misleading. Report the conservative 1
			// and move on, matching the summary path.
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, nil)
			continue
		}
```

Note this `continue` means such a channel contributes no messages to the
output. That is intended and is strictly better than today's unbounded query.

**Verify**: `go build ./...` → exit 0

### Step 3: Fix `unreadCountFromHistory` (defect 3)

Change the success arm so a zero-row window never destroys a positive count:

```go
	if msgCount == 0 {
		if current > 0 {
			return current
		}
		return 1
	}
	return msgCount
```

Update the function's doc comment (`conversations.go:1115-1120`) — it currently
says "Both zero-guards are conservative", which will no longer describe what
the success arm does.

**Verify**: `go build ./...` → exit 0

### Step 4: Make the summary path agree (defect 2)

In `backfillUnreadCounts`, replace `conversations.go:1193-1195`:

```go
		if len(history.Messages) > 0 {
			unreadChannels[i].UnreadCount = len(history.Messages)
		}
```

with a call to the shared helper so both paths use identical logic:

```go
		unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, len(history.Messages), nil)
```

After Step 3 this preserves a positive `current`, sets `1` when there is
nothing else to say, and otherwise uses the row count — the same in both modes.

**Verify**: `go build ./...` → exit 0

### Step 5: Tests

Add unit tests, and update the plan-020 characterization tests this plan
invalidates.

New tests:

1. `TestUnitSlackTS`: zero `fasttime.Time` → `""`; a real timestamp round-trips
   to the same string `SlackString()` produces. Construct a non-zero value with
   `fasttime.Time(time.UnixMicro(1710632873037269))` and assert
   `"1710632873.037269"`.
2. `TestUnitUnreadCountFromHistory`: table over `(current, msgCount, err)` →
   expected. Required rows: `(0, 0, nil)` → 1; `(5, 0, nil)` → **5** (the fix);
   `(5, 3, nil)` → 3; `(0, 3, nil)` → 3; `(0, 0, err)` → 1; `(5, 0, err)` → 5.
   If plan 020 already added a test of this name, extend it rather than
   duplicating — and the `(5, 0, nil)` row is the one whose expectation flips.
3. `collectUnreadChannels` with a snapshot whose `LastRead` is the zero value
   produces `LastRead: ""`. Follow the fixture-construction pattern of the
   existing plan-020 tests for this function.
4. `backfillUnreadCounts` with a fake `historyFetcher` (the interface at
   `conversations.go:1137` exists for exactly this; plan 020 already built a
   call-counting fake — find and reuse it): a channel with `LastRead: ""`
   causes **zero** `GetConversationHistoryContext` calls and yields
   `UnreadCount: 1`.

Then re-run the whole handler suite and reconcile each failure against the
`characterizes current behavior` list from the Current state section.

**Verify**: `go test -count=1 -run 'TestUnit' ./pkg/handler/` → pass

### Step 6: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 5. Load-bearing assertions: a zero `last_read` never reaches Slack as
`Oldest`, and a positive mention count survives a zero-row window.

## Done criteria

- [ ] `make test` exits 0
- [ ] `grep -n 'SlackString()' pkg/handler/conversations.go` → no output
- [ ] No path passes a `LastRead` of `""` or `-62135596800.000000` as `Oldest`
      (read the diff at both call sites)
- [ ] Every plan-020 `characterizes current behavior` test this plan
      invalidates has been rewritten and its NOTE updated; the rest are
      untouched
- [ ] `pkg/provider/edge/fasttime/` is unmodified
- [ ] `git status` shows only the two in-scope files modified

## STOP conditions

- More than four `characterizes current behavior` tests fail. This plan expects
  to invalidate roughly three (surprises 4, 5 and 6 in the index). A larger
  blast radius means the change reaches further than planned — report the list
  before rewriting anything.
- `getUnreadsViaConversationsInfo` shares `unreadCountFromHistory` and your
  change alters its behavior — report; that path is out of scope.
- A test asserts `-62135596800.000000` literally somewhere outside the tests
  you are updating — report where.

## Maintenance notes

- The root cause is that `fasttime.Time`'s zero value renders as a valid-looking
  timestamp. `slackTS` is a local plaster. If Track A ever adds an `IsZero` to
  `fasttime`, `slackTS` should delegate to it.
- Plan 027 covers the remaining unreads surprises (sort order, channel type
  validation, MPIM naming).
- Surprise 3 in the index (the summary CSV header uses Go field names, not the
  json tags) is deliberately **not** fixed here — changing it alters the output
  contract for anything parsing that CSV, which is a maintainer decision.
- Reviewer: confirm the message path's new `continue` is acceptable — a channel
  with no last-read bound now contributes a count but no messages.
