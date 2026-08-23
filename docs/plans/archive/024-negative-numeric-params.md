# Plan 024: Clamp non-positive numeric tool parameters (three reachable panics)

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `727b517`; otherwise STOP.

## Status

- **Priority**: P1 (three reachable panics from ordinary tool input)
- **Effort**: S
- **Risk**: LOW (adds guards; no path that works today changes behavior)
- **Depends on**: nothing; stacked at Track B tip `727b517`
- **Category**: correctness / crash
- **Planned at**: commit `727b517`, 2026-08-07

## Why this matters

`mcp.CallToolRequest.GetInt(name, default)` substitutes the default **only when
the parameter is absent**. A caller that passes `limit: -5` gets `-5`. Three
call sites then slice with that value and panic. A panic in a stdio MCP server
takes down the process the editor is talking to, so this is a crash, not a
handled error.

All three are reachable from ordinary tool input — no auth or special config
needed.

## Current state

All excerpts verified at `727b517`.

### Panic 1 — `paginateChannels` (`pkg/handler/channels.go:567-572`)

```go
	endIndex := startIndex + limit
	if endIndex > len(channels) {
		endIndex = len(channels)
	}

	paged := channels[startIndex:endIndex]
```

With a negative `limit`, `endIndex < startIndex`. The guard only catches
`endIndex` being too *large*, so `channels[startIndex:endIndex]` panics with
`slice bounds out of range [x:y]`. Reached from `ChannelsHandler`, whose limit
comes from `pkg/handler/channels.go:122`:

```go
	limit := request.GetInt("limit", 0)
```

and is normalized at `channels.go:154-162` by a `limit == 0` check plus a
`limit > 999` cap — neither catches negatives.

### Panic 2 — `ChannelsMeHandler` (`pkg/handler/channels.go:343-348`)

```go
	end := limit
	if end > len(allChannels) {
		end = len(allChannels)
	}
	var channelList []Channel
	for _, channel := range allChannels[:end] {
```

Its limit (`channels.go:268`) has the same `limit == 0` / `limit > 999`
normalization at `channels.go:270-275`, so a negative survives to
`allChannels[:end]` and panics.

### Panic 3 — `collectUnreadChannels` (`pkg/handler/conversations.go:1343-1345`)

```go
	// Limit channels
	if len(unreadChannels) > params.maxChannels {
		unreadChannels = unreadChannels[:params.maxChannels]
	}
```

`params.maxChannels` comes from `parseParamsToolUnreads`
(`pkg/handler/conversations.go:2562-2563`), which applies no clamp at all:

```go
		maxChannels:           request.GetInt("max_channels", 50),
		maxMessagesPerChannel: request.GetInt("max_messages_per_channel", 10),
```

A negative `max_channels` reaches the slice and panics.

### The full set of unclamped `GetInt` sites at `727b517`

```
pkg/handler/activity.go:62      max_messages_per_thread (default 10)
pkg/handler/activity.go:63      limit                   (default 30)
pkg/handler/channels.go:122     limit                   (default 0)   ← panic 1
pkg/handler/channels.go:268     limit                   (default 0)   ← panic 2
pkg/handler/channels.go:438     limit                   (default 100)
pkg/handler/saved.go:48         limit                   (default 50)
pkg/handler/saved.go:50         max_messages_per_item   (default 5)
pkg/handler/conversations.go:2544  limit                (default 10)  ← already clamped
pkg/handler/conversations.go:2562  max_channels         (default 50)  ← panic 3
pkg/handler/conversations.go:2563  max_messages_per_channel (default 10)
pkg/handler/conversations.go:2678  limit                (search)      ← already clamped
```

Two are already correct and are your **pattern to copy** — do not change them:

`conversations.go:2544-2550` (`parseParamsToolUsersSearch`):

```go
	limit := request.GetInt("limit", 10)
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
```

and `conversations.go:2678-2684` (search), which uses the same shape against
named constants.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnit' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**: `pkg/handler/channels.go`, `pkg/handler/conversations.go`
(`parseParamsToolUnreads` only), `pkg/handler/activity.go`,
`pkg/handler/saved.go`, and the corresponding `_test.go` files.

**Out of scope**:
- `saved.go:202` `date_due` — a timestamp, not a size; negative is meaningful
  there. Do not touch it.
- The two already-clamped sites listed above.
- `nextPageSize` (`channels.go:252`) — it already floors at 1 and is correct.
- Any change to what a *valid* limit does. This plan must not alter a single
  currently-working request.

## Git workflow

- Branch: `advisor/024-negative-numeric-params`, based on `727b517`.
- One commit, imperative subject. Do NOT push.

## Steps

### Step 1: Clamp at every parse site

For each of the eight unclamped sites in the table above (not the two already
clamped, not `date_due`), apply the existing pattern immediately after the
`GetInt` call: if the value is `<= 0`, replace it with that site's own default.

Concretely, for a site reading `x := request.GetInt("foo", N)` add:

```go
	if x <= 0 {
		x = N
	}
```

using the **same** `N` already in the `GetInt` call, so absent and non-positive
behave identically.

Two sites need care:

- `channels.go:122` and `channels.go:268` pass a `GetInt` default of `0`, and a
  few lines later normalize `limit == 0` to `100`. Do **not** add a separate
  clamp there — instead widen the existing check from `limit == 0` to
  `limit <= 0` at `channels.go:154` and `channels.go:270`. That is a one-token
  change per site and keeps the existing "default then cap" structure intact.
- `conversations.go:2562-2563` is inside a struct literal. Read the values into
  locals first, clamp, then build the literal — do not try to inline the guard.

**Verify**: `go build ./...` → exit 0

### Step 2: Add a defence-in-depth guard at the slice site

Even with Step 1 the slice expressions above are fragile. Add a lower guard so
they cannot panic regardless of caller:

- `paginateChannels` (`channels.go:567`): after the `endIndex > len(channels)`
  check, add `if endIndex < startIndex { endIndex = startIndex }`.
- `ChannelsMeHandler` (`channels.go:343`): after the `end > len(allChannels)`
  check, add `if end < 0 { end = 0 }`.
- `collectUnreadChannels` (`conversations.go:1343`): change the condition to
  `if params.maxChannels > 0 && len(unreadChannels) > params.maxChannels {`.

Keep these guards minimal — they are backstops, and Step 1 is the real fix.

**Verify**: `go build ./...` → exit 0

### Step 3: Tests

Add table tests in the matching `_test.go` files. Follow the style of the
existing `TestUnitParseParamsToolSearchLimit` in
`pkg/handler/conversations_test.go` (locate it by name and read it first —
it builds an `mcp.CallToolRequest` with an `args map[string]any`).

Required cases:

1. **`paginateChannels` with a negative limit does not panic.** Call it
   directly with a non-empty `[]provider.Channel`, `cursor: ""`, `limit: -5`.
   It must return without panicking. Assert the returned page is empty.
2. **`ChannelsMeHandler`'s limit normalization**: a unit test over whatever
   seam is reachable — if the handler needs a live provider, instead test the
   normalization arithmetic by asserting that after your `limit <= 0` change,
   `-5` maps to `100`. If no seam exists without constructing a provider, say
   so in your report and cover it via `paginateChannels` plus case 3 only. Do
   NOT invent a provider fake for this.
3. **`collectUnreadChannels` with `maxChannels: -1` does not panic** and
   returns the unlimited list. This function is pure and takes plain
   arguments — plan 020 added tests for it in
   `pkg/handler/conversations_test.go`; find them by name and follow their
   fixture-construction pattern rather than building your own.
4. **`parseParamsToolUnreads` clamps**: `max_channels: -1` → 50,
   `max_channels: 0` → 50, `max_messages_per_channel: -3` → 10, and a valid
   value passes through unchanged.

**Verify**: `go test -count=1 -run 'TestUnit' ./pkg/handler/` → pass

### Step 4: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 3. The load-bearing assertions are the three "does not panic" cases — a
Go test fails on panic automatically, so simply calling the function with the
hostile value is a sufficient assertion, but still assert the returned value so
the test documents the intended behavior.

## Done criteria

- [ ] `make test` exits 0; new tests pass
- [ ] All eight sites from the table clamp non-positive values (read the diff)
- [ ] The three slice sites have their backstop guards
- [ ] `saved.go:202` `date_due` is untouched
- [ ] `gofmt -l pkg cmd` → no output
- [ ] `git status` shows only in-scope files modified

## STOP conditions

- A slice site turns out to be unreachable with a negative value because
  something upstream already clamps — report which and skip that guard rather
  than adding a dead one.
- Clamping a site breaks an existing test. That would mean a test depends on
  negative-limit behavior; report it, do not edit the test to pass.
- `collectUnreadChannels`'s existing plan-020 tests fail after your change —
  they characterize current behavior deliberately; report the failure.

## Maintenance notes

- Root cause is `GetInt`'s absent-only default. Any *new* numeric parameter
  needs the same two-line clamp; there is no shared helper today and this plan
  deliberately does not add one (it would touch every call site twice).
- Reviewer: confirm no currently-valid request changes behavior — every guard
  should be unreachable for in-range input.
