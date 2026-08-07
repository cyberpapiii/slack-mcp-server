# Plan 011: Skip the redundant unread-count backfill when `conversations_unreads` fetches messages anyway

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
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- pkg/handler/conversations.go`
> On any change, locate the code by the excerpts below; unlocatable excerpt = STOP.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none (execute after plan 009 if both run, same file)
- **Category**: perf
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

On the browser-token (primary) path of `conversations_unreads` — the fork's
flagship "what needs my attention" tool — a backfill loop issues one
`conversations.history` call per zero-mention unread channel purely to compute
`UnreadCount`. Then, because `include_messages` defaults to `true`, a second
loop immediately re-issues `conversations.history` for the same channels with
the same `Oldest` and **overwrites `UnreadCount` anyway**. Every default-path
call pays up to `max_channels` (default 50) wasted Slack API round-trips and
their serial latency. Skipping the backfill when messages will be fetched
halves the tool's API volume with no observable output change on the success
path.

## Current state

`pkg/handler/conversations.go`, inside `processClientCountsResponse`, at
commit `adbae97`:

The backfill loop (runs unconditionally):

```go
// conversations.go:1163-1208 (abridged)
	// Backfill real unread counts for channels where client.counts only gave us
	// HasUnreads=true but MentionCount=0 ...
	const backfillLimit = 20
	backfilled := 0
	for i := range unreadChannels {
		...
		if unreadChannels[i].UnreadCount > 0 {
			continue // MentionCount was positive, good enough
		}
		if unreadChannels[i].LastRead == "" {
			// Conservatively report 1 unread since HasUnreads was true.
			unreadChannels[i].UnreadCount = 1
			backfilled++
			continue
		}
		history, err := ch.apiProvider.Slack().GetConversationHistoryContext(ctx,
			&slack.GetConversationHistoryParameters{
				ChannelID: unreadChannels[i].ChannelID,
				Oldest:    unreadChannels[i].LastRead,
				Limit:     backfillLimit,
				Inclusive: false,
			})
		if err != nil { ...; continue }
		if len(history.Messages) > 0 {
			unreadChannels[i].UnreadCount = len(history.Messages)
		}
		backfilled++
	}
```

The branch and the second loop that makes it redundant:

```go
// conversations.go:1214-1251 (abridged)
	// If not including messages, just return channel summary
	if !params.includeMessages {
		return ch.marshalUnreadChannelsToCSV(unreadChannels)
	}

	// Fetch messages for each unread channel
	for i := range unreadChannels {
		...
		history, err := ch.apiProvider.Slack().GetConversationHistoryContext(ctx, &historyParams)
		if err != nil { ...; continue }

		// Update unread count from actual message count
		unreadChannels[i].UnreadCount = len(history.Messages)
		...
	}
```

Note the second loop uses `Limit: params.maxMessagesPerChannel` and
`Oldest: unreadChannels[i].LastRead` — same window; the overwrite at line 1246
is unconditional for every channel whose fetch succeeds.

The `include_messages` default is `true` (`conversations.go:2429`, in the
unreads param parser).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/handler/conversations.go` — the backfill block inside
  `processClientCountsResponse` only.

**Out of scope**:
- The message loop itself, `marshalUnreadChannelsToCSV`, sorting, the OAuth
  fallback (`getUnreadsViaConversationsInfo`), and the parser. Do not
  restructure anything; this is a guard, not a refactor.
- Concurrency/worker-pool ideas for the remaining loop (surfaced in the
  audit, deliberately not planned).

## Git workflow

- Branch: `advisor/011-unreads-skip-redundant-backfill`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 1: Guard the backfill

Wrap the entire backfill block (comment through the `if backfilled > 0`
debug log) in `if !params.includeMessages { ... }`. Preserve behavior the
message loop does NOT replicate, inside the message path:

- In the message loop, after a *successful* fetch, keep the existing
  `UnreadCount = len(history.Messages)` overwrite (already there).
- Channels with `LastRead == ""`: the backfill's "conservatively report 1"
  rule only matters when the message fetch *fails* or returns zero rows for
  such a channel. Add, in the message loop after a failed fetch (`continue`
  branch) or a zero-message result: if `UnreadCount == 0` and
  `HasUnreads`-derived presence put it in this list, set `UnreadCount = 1`
  so the channel doesn't render as "0 unread" in the summary column. Keep
  this to 2–3 lines; match the comment style of the backfill's explanation.

Update the backfill comment block to state it runs only for the
summary-only mode.

**Verify**: `go build ./...` → exit 0

### Step 2: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

There is no unit seam for `processClientCountsResponse` today (zero coverage
— a characterization-test plan, 020, follows this one and will lock in the
new behavior). For this plan:

- `make test` green (no existing test asserts the backfill).
- Manual live verification is the real gate and is the maintainer's step, per
  repo convention ("verify live behavior through Plug-exposed Slack tools"):
  after merge + `make deploy-local`, one `conversations_unreads` call with
  defaults should show unchanged output, and server debug logs should show no
  "Backfilling unread counts" line on the default path but still show it with
  `include_messages: false`. Record this expectation in your report.

## Done criteria

- [ ] `make test` exits 0
- [ ] The backfill loop is inside an `if !params.includeMessages` guard (read the diff)
- [ ] The `LastRead == ""` → count 1 fallback is preserved for the messages path per Step 1
- [ ] `git status` shows only `pkg/handler/conversations.go` modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- Excerpts don't match (drift).
- You find a consumer of `UnreadCount` *between* the backfill and the
  `includeMessages` branch (there is none at planning time — the branch
  directly follows the backfill; if code appeared in between, re-evaluate).
- The Step 1 fallback turns out to need more than ~5 lines in the message
  loop — that suggests a misread of the flow; report instead of expanding.

## Maintenance notes

- Plan 020 (unreads characterization tests) should be written against the
  post-011 behavior; if 020 lands first, its expected-API-call-count
  fixtures will need updating here.
- Reviewer: the only intended observable difference is for channels where
  the message fetch *fails* — before, the backfill may have set a count the
  failed fetch couldn't; now such channels show the client.counts value or
  the conservative 1. Confirm that's acceptable (it was judged so at
  planning: the exact count "matters less than surfacing that unreads
  exist", per the code's own comment).
