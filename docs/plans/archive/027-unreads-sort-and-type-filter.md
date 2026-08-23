# Plan 027: `conversations_unreads` — nondeterministic sort, unknown channel types outranking real ones, silently-ignored filters

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `b481c1b`; otherwise STOP.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: MEDIUM — changes result ordering and turns one silently-accepted
  input into an error. Pinned by plan-020 characterization tests that must be
  updated deliberately.
- **Depends on**: plan 026 (same file, same functions; stack after it)
- **Category**: correctness / UX
- **Planned at**: commit `727b517`, 2026-08-07

## Why this matters

Three of the eight surprises plan 020 pinned in the unreads pipeline:

1. **The sort has no tiebreaker and is not stable.** Channels are ordered by
   type alone, with `sort.Slice`, so the order of same-type channels is
   unspecified and can differ between identical calls. Combined with the
   `max_channels` truncation that happens right after, *which* channels you see
   is nondeterministic.
2. **An unknown channel type sorts first.** The priority lookup is a bare map
   index, so a miss yields `0` — the same rank as `dm`, ahead of `partner` and
   `internal`.
3. **An unrecognized `channel_types` value is silently accepted** and matches
   nothing, so the tool returns an empty result that looks like "you have no
   unreads" rather than "you passed a bad filter".

Plus one small inconsistency: MPIM names bypass the `#`/`@` prefix
normalization every other branch applies.

## Current state

All excerpts verified at `727b517`.

### The sort (`pkg/handler/conversations.go:1897-1911`)

```go
// sortChannelsByPriority sorts channels: DMs > group_dm > partner > internal
func (ch *ConversationsHandler) sortChannelsByPriority(channels []UnreadChannel) {
	priority := map[string]int{
		"dm":       0,
		"group_dm": 1,
		"partner":  2,
		"internal": 3,
	}

	sort.Slice(channels, func(i, j int) bool {
		pi := priority[channels[i].ChannelType]
		pj := priority[channels[j].ChannelType]
		return pi < pj
	})
}
```

`priority[unknown]` is `0` — surprise 2. `sort.Slice` is not stable and the
comparator has no secondary key — surprise 1.

Called from two places: `collectUnreadChannels` (`conversations.go:1340`) and
`getUnreadsViaConversationsInfo` (`conversations.go:1413`). Both are in scope
for the fix by virtue of sharing the function; do not change either call site.

The truncation that makes the nondeterminism visible sits immediately after the
first call (`conversations.go:1342-1345`):

```go
	// Limit channels
	if len(unreadChannels) > params.maxChannels {
		unreadChannels = unreadChannels[:params.maxChannels]
	}
```

(Plan 024 may have already added a `params.maxChannels > 0 &&` guard to that
condition. If so, leave its guard in place.)

### The type filter (three sites in `collectUnreadChannels`)

```go
// conversations.go:1245-1248 — regular channels
		// Filter by requested channel types
		if params.channelTypes != "all" && channelType != params.channelTypes {
			continue
		}
```

```go
// conversations.go:1276-1279 — MPIMs
		// Filter by requested channel types
		if params.channelTypes != "all" && params.channelTypes != "group_dm" {
			continue
		}
```

```go
// conversations.go:1312-1315 — IMs
		// Filter by requested channel types
		if params.channelTypes != "all" && params.channelTypes != "dm" {
			continue
		}
```

`params.channelTypes` is a plain string from
`parseParamsToolUnreads` (`conversations.go:2561`):

```go
		channelTypes:          request.GetString("channel_types", "all"),
```

So `channel_types: "public"` (a plausible guess) matches no branch and returns
an empty list with no diagnostic. The complete set of values that mean anything
is `all`, `dm`, `group_dm`, `partner`, `internal`.

### The MPIM name (`conversations.go:1281-1284`)

```go
		channelName := snap.ID
		if cached, ok := channelsMaps.Channels[snap.ID]; ok {
			channelName = cached.Name
		}
```

Compare the regular-channel branch (`conversations.go:1231-1238`), which
normalizes:

```go
		if cached, ok := channelsMaps.Channels[snap.ID]; ok {
			// The cached name may already have # prefix, so handle both cases
			name := cached.Name
			if strings.HasPrefix(name, "#") {
				channelName = name
			} else {
				channelName = "#" + name
			}
```

and the IM branch (`conversations.go:1319-1327`), which prefixes `@`.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnit' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**: `pkg/handler/conversations.go` (`sortChannelsByPriority`,
`collectUnreadChannels`, `parseParamsToolUnreads`) and
`pkg/handler/conversations_test.go`.

**Out of scope**:
- The **primary** sort key. Type-before-count is deliberate product behavior
  ("a DM outranks a busy channel"). Do not change it — only add a tiebreaker
  below it.
- Cache-miss rendering of bare IDs (`C_UNKNOWN`, `D_NOUSER`). Leaving an
  unresolved ID unprefixed is arguably correct — a bare ID is not a name, and
  `#C_UNKNOWN` would be a lie. Not fixed here; recorded as accepted behavior.
- The summary CSV header field names (surprise 3) — an output-contract change,
  maintainer's call.
- `slackTS` and `unreadCountFromHistory`'s internals — plan 026 settled those.
  Step 4 below *calls* `unreadCountFromHistory` from one new place; it must not
  change the function itself.

## Git workflow

- Branch: `advisor/027-unreads-sort-and-type-filter`, based on `b481c1b`.
- One commit, imperative subject. Do NOT push.

## Steps

### Step 1: Deterministic sort with an explicit unknown rank

Rewrite `sortChannelsByPriority`:

- Hoist the `priority` map to a package-level `var` so it is not rebuilt per
  call, and add a named constant for the unknown rank that is larger than every
  real rank (e.g. `unknownChannelTypePriority = 99`, with a comment saying it
  must stay above the largest value in the map).
- Look up with the two-value form: `pi, ok := channelPriority[...]`; on a miss
  use the unknown rank. An unrecognized type now sorts **last**, not first.
- Use `sort.SliceStable`.
- Add tiebreakers so the order is total and reproducible: equal type →
  higher `UnreadCount` first; still equal → `ChannelID` ascending.

The comparator becomes three ordered comparisons. Keep the doc comment accurate
— it currently claims `DMs > group_dm > partner > internal` and should now also
state the tiebreakers and where unknown types land.

**Verify**: `go build ./...` → exit 0

### Step 2: Validate `channel_types`

`parseParamsToolUnreads` currently returns `*unreadsParams` with no error. Read
its signature and its single caller (grep `parseParamsToolUnreads`) before
changing anything.

Change it to return `(*unreadsParams, error)` and validate `channel_types`
against exactly `all`, `dm`, `group_dm`, `partner`, `internal`. On an
unrecognized value return an error naming the value and listing the accepted
set, e.g.:

```
unsupported channel_types "public": must be one of all, dm, group_dm, partner, internal
```

Update the caller to propagate the error the way its sibling parse functions do
— read a neighbouring handler that already calls a fallible
`parseParamsTool…` and copy its error handling exactly.

Also update the tool's parameter description in `pkg/server/server.go` if it
does not already enumerate the accepted values; grep for `channel_types` there
and read the `conversations_unreads` registration before editing.

**Verify**: `go build ./...` → exit 0

### Step 3: Normalize the MPIM name

In the MPIM branch of `collectUnreadChannels`, apply the same prefix
normalization the regular-channel branch uses, so a cached MPIM name gets a
single leading `#` whether or not the cache already stored one.

Only touch the cache-hit path. The cache-miss `channelName := snap.ID` stays a
bare ID (see Scope).

**Verify**: `go build ./...` → exit 0

### Step 4: Close the last summary-vs-message disagreement

**Added 2026-08-07**, handed over by plan 026's executor. Plan 026 unified the
two modes for a *successful* zero-row fetch but deliberately left the *failed*
fetch case alone to stay inside its test budget. The asymmetry that remains:

In `backfillUnreadCounts` (summary mode), the error branch reads:

```go
		if err != nil {
			ch.logger.Debug("Failed to backfill unread count",
				zap.String("channel", unreadChannels[i].ChannelID),
				zap.Error(err))
			continue
		}
```

It `continue`s without touching `UnreadCount`, so a failed fetch leaves `0` and
the CSV renders "0 unread" for a channel `client.counts` flagged as unread. The
message path handles the same case with
`unreadCountFromHistory(current, 0, err)`, which yields the conservative 1.

Fix: set the count before continuing, using the same helper:

```go
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, err)
			continue
```

A plan-020 characterization test pins the current behavior — at `b481c1b` it
sits around `conversations_test.go:1804` and describes summary mode reporting 0
on a failed fetch. Rewrite it to assert the conservative 1, and replace its
`NOTE:` comment with a pointer to this plan, exactly as plan 026 did for the
three it rewrote.

**Verify**: `go build ./...` → exit 0

### Step 5: Tests

Update the plan-020 characterization tests this plan invalidates. Find them
first:

```
grep -n 'characterizes current behavior' pkg/handler/conversations_test.go
```

Read every hit. Rewrite the ones covering sort order and channel-type
handling; leave the rest untouched.

New tests:

1. `TestUnitSortChannelsByPriority`: build a slice mixing all four known types
   plus one with `ChannelType: "weird"` and one with `ChannelType: ""`. Assert
   the exact resulting order — types in the documented order, unknown/empty
   last, higher `UnreadCount` first within a type, `ChannelID` ascending as the
   final tiebreak.
2. Determinism: sort the same input twice from two independently-built slices
   with equal contents and assert both produce identical `ChannelID` sequences.
3. `TestUnitParseParamsToolUnreadsChannelTypes`: each of the five accepted
   values parses without error; `"public"` and `"DM"` (wrong case) return an
   error whose message contains the offending value; the parameter absent
   defaults to `all`.
4. `collectUnreadChannels` with a cached MPIM whose name lacks `#` yields a
   name with exactly one `#`; one that already has `#` is unchanged. Reuse the
   fixture pattern from the existing plan-020 tests for this function.

**Verify**: `go test -count=1 -run 'TestUnit' ./pkg/handler/` → pass

### Step 6: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 5. Load-bearing: the exact-order assertion in test 1, and that an
unrecognized `channel_types` is an error rather than an empty result.

## Done criteria

- [ ] `make test` exits 0
- [ ] `grep -n 'sort.Slice(' pkg/handler/conversations.go` shows no remaining
      unstable sort in `sortChannelsByPriority` (other unrelated `sort.Slice`
      calls elsewhere in the file are fine — check the one you changed)
- [ ] An unknown `ChannelType` sorts last, not first (read the diff)
- [ ] `parseParamsToolUnreads` returns an error for an unrecognized
      `channel_types`, and the caller propagates it
- [ ] Cache-miss bare IDs are still unprefixed
- [ ] `git status` shows only the two in-scope files modified

## STOP conditions

- `parseParamsToolUnreads` has more than one caller, or a caller that cannot
  return an error — report the call graph before changing the signature.
- Changing the sort breaks a plan-020 test whose NOTE says the ordering is
  *intentional* rather than *possibly wrong* — report it.
- `getUnreadsViaConversationsInfo` (the second `sortChannelsByPriority` caller)
  populates `ChannelType` with values outside the five known ones — that would
  mean this plan's "unknown sorts last" makes real channels sink. Grep how it
  sets `ChannelType` before finalizing, and report if so.

## Maintenance notes

- The five channel-type values are now enumerated in three places: the priority
  map, the validation set, and the tool's parameter description. A sixth type
  means updating all three; consider a single package-level slice as the source
  of truth if that ever happens.
- Two of plan 020's eight pinned surprises remain deliberately unfixed after
  this plan: the summary CSV header field names, and bare cache-miss IDs. Both
  are recorded in `plans/README.md` as accepted.
- Reviewer: confirm the tiebreaker did not accidentally become the primary key
  — a silent DM must still outrank a busy internal channel.
