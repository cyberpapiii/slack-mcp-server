# Plan 020: Characterization tests for the `conversations_unreads` pipeline

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
> This plan MUST run AFTER plans 009 and 011 (if selected) — it locks in
> post-011 behavior. Confirm 011's guard (`if !params.includeMessages` around
> the backfill) is present before writing expectations; if 011 was skipped,
> write expectations against the code as it stands and say so in your report.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW (tests only — plus, possibly, one small test-seam refactor)
- **Depends on**: order after 009 and 011; complements 019
- **Category**: test coverage
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

`conversations_unreads` is the fork's flagship tool and its most complex
pipeline — `client.counts` → channel resolution → mention/unread merging →
backfill → sorting → CSV — and it has **zero unit coverage**. Every recent
regression risk in this repo (plans 009, 011) ran through this file blind.
Characterization tests pin today's behavior so the next change diffs against
something.

## Current state

`pkg/handler/conversations.go` at commit `adbae97`:

- `processClientCountsResponse` (`:1013` onward) — the pipeline core. Takes
  ctx, params, and the edge `client.counts` response; resolves channels via
  `ch.apiProvider` (cache lookups), fetches history via
  `ch.apiProvider.Slack()`.
- Pure helpers, directly testable today with zero scaffolding:
  - `sortChannelsByPriority` (`:1788-1799`) — mentions first, then by
    latest-activity timestamp.
  - `marshalUnreadChannelsToCSV` (`:1801-1810` area) — summary CSV shape.
- The handler's provider field: `ch.apiProvider *provider.ApiProvider`
  (concrete type). `ApiProvider.Slack()` returns the concrete
  `*slack.Client`-backed wrapper — **check at execution time** whether
  `Slack()` returns an interface the tests can fake
  (`pkg/provider/api.go:375` defines the internal `SlackAPI` interface used
  for `ap.client`; whether the handler-facing surface is fakeable without a
  refactor is the open question below).
- Existing test conventions: `TestUnit*` in `pkg/handler/conversations_test.go`
  (e.g. `TestUnitFormatThreadTs` at `:736`), plain table tests, no mocking
  framework anywhere in the repo.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnitSortChannelsByPriority|TestUnitMarshalUnreadChannels|TestUnitProcessClientCounts' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/handler/conversations_test.go` (new tests).
- IF (and only if) the escape hatch in Step 2 demands it: a minimal seam in
  `pkg/handler/conversations.go` — see the strict limits there.

**Out of scope**:
- Changing any pipeline behavior. Characterization = pin what IS.
- The OAuth fallback path (`getUnreadsViaConversationsInfo`) — separate
  surface, live-verified; cover only if it costs nothing extra.
- Faking the edge client itself (plan 019 covers edge parsing).

## Git workflow

- Branch: `advisor/020-unreads-characterization-tests`
- One commit; imperative subject. Do NOT push.

## Steps

### Step 1: Pin the pure helpers (guaranteed-cheap coverage)

In `conversations_test.go`:

- `TestUnitSortChannelsByPriority`: table with channels mixing
  mentions>0/mentions==0 and varying timestamps → assert full expected
  order, including the tie-break you observe in the code (read it; document
  the tie-break in the test name or a comment). Include an empty slice and a
  single-element case.
- `TestUnitMarshalUnreadChannelsToCSV`: 2–3 channels → assert exact CSV
  output (header row, column order, escaping of a channel name containing a
  comma). This is the tool's output contract — exactness matters.

**Verify**: targeted `go test` above → these pass

### Step 2: Fake-driven test of `processClientCountsResponse`

First, determine the seam. Read how `processClientCountsResponse` reaches
Slack: every external call goes through `ch.apiProvider.<something>`. Then:

- **If** the methods it calls are satisfiable by an interface already
  (e.g. the handler only needs `Slack()` returning something you can
  construct around a fake `http.RoundTripper` via `slack.New(token,
  slack.OptionHTTPClient(...))` — the slack-go client supports this) —
  build the fake at the HTTP layer: a `roundTripperFunc` returning canned
  `conversations.history` JSON. No production changes needed.
- **Else**, add the minimal seam: define a small unexported interface in
  `conversations.go` naming ONLY the methods this function uses, change the
  function (not the handler struct) to accept it as a parameter with the
  current call site passing `ch.apiProvider`. Mechanical, behavior-free.
  If the seam would require touching more than ~10 lines of production
  code, SKIP this step (escape hatch) and note it — the pure-helper tests
  from Step 1 still land.

Then write `TestUnitProcessClientCountsResponse` covering, on the
browser-token path:

- A counts response with one mention-channel, one zero-mention unread
  channel, one fully-read channel → assert: read channel excluded; mention
  channel keeps its mention count; behavior of the zero-mention channel
  matches post-011 code (with `include_messages=false`: backfill history
  call happens and sets the count; with default `include_messages=true`:
  NO backfill call — count comes from the message fetch).
- History fetch failure for one channel → pipeline continues, channel still
  listed (assert the count fallback the code implements).
- Assert the NUMBER of history calls the fake observed — that is the
  regression test for 011's redundancy fix.

**Verify**: targeted `go test` → pass

### Step 3: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

This plan IS a test plan. Priorities if time-boxed: Step 1 helpers (certain
value), then the history-call-count assertion (guards 011), then breadth.

## Done criteria

- [ ] `make test` exits 0; all new tests pass
- [ ] CSV output contract pinned exactly (read the test)
- [ ] If Step 2 ran: history-call-count asserted for both include_messages
      modes; if skipped: report says why, with the blocking detail
- [ ] Production diff is zero OR ≤ ~10 lines of pure seam (read the diff)
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- Drift check fails and you cannot locate the functions by name.
- The seam requires exporting anything or touching `pkg/provider` — that's
  a design decision, not a test chore; report instead.
- Pinning reveals a behavior that looks like a bug (e.g. mis-sorted output,
  malformed CSV) — pin it anyway with a `// NOTE: characterizes current
  behavior, possibly wrong:` comment AND list it in your report. Do not fix.

## Maintenance notes

- These tests intentionally encode current behavior; when future plans
  change the pipeline, updating an assertion here is expected — the value is
  that the change becomes visible in a diff.
- Reviewer: watch for over-faking — if the test constructs deep slack-go
  types by hand for marginal branches, trim; the two priority assertions
  are the yield.
- Keep fixture field names consistent with plan 019's edge fixtures.
