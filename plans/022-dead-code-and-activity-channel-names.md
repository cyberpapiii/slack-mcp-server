# Plan 022: Wire the computed-but-discarded channel names into activity output; remove dead code

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
> `git diff --stat adbae97..HEAD -- pkg/handler/activity.go pkg/handler/conversations.go`
> Run AFTER plan 012 if both selected (012 edits `conversations.go`;
> serializing avoids merge friction). Every deletion below must be
> re-verified dead by grep at execution time — greps are authoritative,
> this plan's claims are as-of `adbae97`.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: order after 012 (same file); unaffected by others
- **Category**: tech debt + small output-quality bug
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

Two things wearing one plan because they share files:

1. **`activity_feed` computes channel names and throws them away.** In the
   thread-bundle path, `channelName` and `usersMap` are computed and then
   explicitly discarded (`_ = channelName; _ = usersMap`), because they're
   computed *after* the call that needed them. Agents reading activity
   output get raw `C0123ABC` IDs where a human-readable `#general` was
   already paid for. This is a real (small) output-quality bug, and the
   `_ =` discards are the smell that found it.
2. **Verified-dead code confuses future readers**: an unused function and an
   unused type in `conversations.go`.

Also recorded here (deliberately NOT deleted): six exported edge-client
methods with no callers — they're API surface on a fork that tracks
upstream, and expose-or-delete is a direction decision for the maintainer,
not a cleanup.

## Current state

**Finding 1** — `pkg/handler/activity.go:160-180` (abridged, at `adbae97`):

```go
	// inside the thread-bundle branch of the activity handler
	msgs := h.convHandler.convertMessagesFromHistory(ctx, replies, t.ChannelID, false, mode)   // :169

	channelName := t.ChannelID                                    // :172
	if ch, ok := channelsMap[t.ChannelID]; ok {                   // :173
		channelName = ch.Name                                     // :174
	}                                                             // :175
	_ = channelName // computed but unused — see plan 022         // (discards near :176-177)
	_ = usersMap
```

Read the surrounding function at execution time: the fix is to move the
`channelName` computation ABOVE the `convertMessagesFromHistory` call and
pass the name through to wherever the rendered output labels the thread
(read how the non-bundle path labels channels — mirror it; grep for where
`msgs` is subsequently rendered/joined to find the right insertion point).
`usersMap`: check whether it becomes genuinely useful in the same wiring
(user names in the bundle header); if not, delete its computation rather
than keep the discard.

**Finding 2** — dead code in `pkg/handler/conversations.go`:

- `getUserInfo` (`:2735` area) — grep `rg -n 'getUserInfo\b' pkg cmd` at
  planning finds only the definition.
- `UnreadMessage` type (`:957` area) — grep `rg -n 'UnreadMessage\b' pkg cmd`
  finds only the definition.

**Recorded, NOT for deletion** — exported edge methods with no callers at
`adbae97`: `ClientDMs` (`pkg/provider/edge/client.go:132`),
`ChannelsMembership` (`userlist.go:178`), `GetUsersInConversationContext`
(`slacker.go:113`), `GetUsers` (`userlist.go:114`), `ConversationsView`,
`ConversationsGenericInfo`. Leave them; the index records the pending
direction decision ("expose as tools or drop").

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Deadness proof | `rg -n 'getUserInfo\b\|UnreadMessage\b' pkg cmd` | definitions only, pre-delete |
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/handler/activity.go` (channel-name wiring; the two `_ =` discards go away)
- `pkg/handler/conversations.go` (two deletions)
- `pkg/handler/activity_test.go` if a cheap seam exists for the label (see
  test plan)

**Out of scope**:
- The six edge exports (listed above — do not delete).
- `convertMessagesFromHistory` itself — pass data to it / around it; don't
  change its signature unless that's the ONLY way to label output, and if
  so keep the change additive (new parameter with all existing callers
  updated) and note it in the report.
- Any other `_ =` discards in the repo (grep will find some in tests —
  they're fine).

## Git workflow

- Branch: `advisor/022-activity-names-dead-code`
- Two commits (wiring; deletions) preferred; imperative subjects. Do NOT push.

## Steps

### Step 1: Wire the channel name

Move the `channelName` resolution above the `convertMessagesFromHistory`
call and thread it into the rendered thread-bundle output where the channel
is currently labeled by raw ID. Read the non-bundle activity path first and
match its naming/labeling convention exactly (same prefix format, e.g.
`#name` vs `name`). Resolve `usersMap` the same way: use it if the header
naturally takes user names, else delete its computation. After this step no
`_ =` discards remain in the function.

**Verify**: `go build ./...` → exit 0; `rg -n '_ = channelName|_ = usersMap' pkg/` → no matches

### Step 2: Delete dead code

Re-run the deadness greps; if still definition-only, delete `getUserInfo`
and `UnreadMessage`. Remove imports orphaned by the deletion (compiler will
tell you).

**Verify**: `go build ./...` → exit 0; the greps now return nothing

### Step 3: Tests and suite

If `activity.go` has an existing unit-testable seam for output rendering
(check `activity_test.go` — at planning it holds mostly integration tests),
add a small case asserting a thread bundle labels with the resolved name
when `channelsMap` contains the channel, and falls back to the ID when not.
If no cheap seam exists, skip (note it) — the live check below covers it.

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 3 if the seam is cheap. Live verification (maintainer step, repo
convention): after merge + `make deploy-local`, one `activity_feed` call
with a thread in a known channel should show the channel's name, not its ID
— record this expectation in your report.

## Done criteria

- [ ] `make test` exits 0
- [ ] No `_ =` discards remain in the activity thread-bundle path
- [ ] Thread bundles label channels by name when resolvable (diff + report)
- [ ] `getUserInfo` and `UnreadMessage` gone; greps return nothing
- [ ] Edge exports untouched (`git diff --stat` shows no `pkg/provider/edge/` changes)
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- A deadness grep finds a NEW caller (code moved since `adbae97`) — that
  item is no longer dead; skip its deletion and report.
- Wiring the name requires restructuring `convertMessagesFromHistory` beyond
  an additive parameter — report with the shape of the problem.
- The discards turn out to be load-bearing for a compiler/vet suppression
  reason you can identify — report it (none was found at planning).

## Maintenance notes

- The six retained edge exports are recorded in `plans/README.md` under
  direction decisions — next audit should not re-report them as findings.
- Reviewer: check the label format matches the non-bundle path exactly —
  agents parse this output; two formats for one concept is a new bug.
