# Plan 009: Fix four agent-facing parameter-contract bugs (search limit, DM filter, date_due clear, cursor+duration conflict)

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
> `git diff --stat adbae97..HEAD -- pkg/handler/conversations.go pkg/handler/saved.go pkg/server/server.go`
> IMPORTANT: the unmerged branch `advisor/005-search-sort-and-has-modifiers`
> edits the same search-parsing region of `conversations.go`. If it has been
> merged since planning, the line numbers below will have shifted — locate the
> code by the excerpts, not the line numbers; if an excerpt cannot be found at
> all, STOP.

## Status

- **Priority**: P1
- **Effort**: S (four small independent fixes)
- **Risk**: LOW
- **Depends on**: none (but see drift note about plan 005's branch)
- **Category**: bug
- **Planned at**: commit `adbae97`, 2026-08-07

## Why this matters

This fork exists to make Slack ergonomic for AI agents, and each of these four
bugs makes an agent's *most natural call* fail or silently misbehave: the tool
schema documents one contract and the parser implements another. Agents echo
schema defaults and examples verbatim, so schema/parser mismatches are hit
constantly, and the resulting errors ("user not found" for a documented ID
form) mislead the agent into wrong retries.

## Current state

All in `pkg/handler/conversations.go`, `pkg/handler/saved.go`,
`pkg/server/server.go` at commit `adbae97`.

**Bug A — search `limit` defaults to 100 (schema says 20) and is unclamped:**

```go
// conversations.go:2534
	limit := req.GetInt("limit", 100)
```

The schema (`server.go` search-tool registration, ~line 380) declares
`mcp.DefaultNumber(20)` and "Must be an integer between 1 and 100". The value
flows to `slack.SearchParameters{Count: params.limit}` (`conversations.go:923`)
with no bounds check, so `0`, negatives, and `5000` are forwarded to Slack.
The correct pattern already exists in this file:

```go
// conversations.go:2413-2419 (parseParamsToolUsersSearch) — clamps to 1..100
```

**Bug B — `filter_in_im_or_mpim` rejects the documented `D…` ID form:**

```go
// conversations.go:2494-2500
	} else if im := req.GetString("filter_in_im_or_mpim", ""); im != "" {
		f, err := ch.paramFormatUser(ctx, im)
```

```go
// conversations.go:2574-2604 — paramFormatUser recognizes only U/W prefixes:
func isSlackUserIDPrefix(s string) bool {
	return strings.HasPrefix(s, "U") || strings.HasPrefix(s, "W")
}
```

A `D…` value falls through to a *username* map lookup and returns
`user "D1234567890" not found`. But the tool description
(`server.go`, search registration, ~line 353) says:
`"...by its ID or name. Example: 'D1234567890' or '@username_dm'."`

**Bug C — `saved_update` cannot clear a due date:**

```go
// saved.go:203-212
	dateDue := int64(request.GetInt("date_due", 0))
	...
	if mark == "" && dateDue == 0 {
		return nil, fmt.Errorf("at least one of mark or date_due must be provided")
	}
```

The schema (`server.go:616-618`) says: `date_due`: "Unix timestamp for due
date/reminder. **Set to 0 to clear.**" An explicit `date_due: 0` is
indistinguishable from "absent" and is rejected.

**Bug D — schema-default `limit: "1d"` silently conflicts with `cursor`:**

```go
// conversations.go:2056-2068 — only the numeric branch is guarded by cursor == "":
	if strings.HasSuffix(limit, "d") || strings.HasSuffix(limit, "w") || strings.HasSuffix(limit, "m") {
		paramLimit, paramOldest, paramLatest, err = limitByExpression(limit, defaultConversationsExpressionLimit)
		...
	} else if cursor == "" {
		paramLimit, err = limitByNumeric(limit, defaultConversationsNumericLimit)
```

The history tool schema (`server.go:188-191`) sets `mcp.DefaultString("1d")`
while its description says "Must be empty when 'cursor' is provided" — the
advertised default IS the forbidden value. An agent echoing `limit: "1d"` with
the page-2 cursor sends Slack both a cursor and a today-only `Oldest`/`Latest`
window; pagination beyond the window silently returns nothing.

Repo conventions: table-driven unit tests named `TestUnit*` in
`pkg/handler/conversations_test.go` (see `TestUnitLimitByExpression_Valid` at
line 516 as the structural pattern); `make test` skips names containing
"Integration".

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnit' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

## Scope

**In scope**:
- `pkg/handler/conversations.go` (the four parse sites above + `paramFormatUser` or a new sibling helper)
- `pkg/handler/saved.go` (`SavedUpdateHandler` param parsing)
- `pkg/handler/conversations_test.go`, `pkg/handler/saved_test.go`
- `pkg/server/server.go` ONLY if a description string needs a one-line
  clarification (no schema structure changes)

**Out of scope**:
- Search `sort`/`has:` handling — plan 005 (already executed on its branch).
- `limitByExpression`'s hardcoded count of 100 — noted, not this plan.
- Any change to CSV output or render pipeline.

## Git workflow

- Branch: `advisor/009-param-contract-fixes`
- One commit per bug (A–D) or one combined commit; imperative subjects. Do NOT push.

## Steps

### Step 1 (Bug A): Default and clamp search limit

In `ConversationsSearchHandler`'s param parsing (`conversations.go:2534`):
default to 20, then clamp: `if limit <= 0 { limit = 20 }; if limit > 100 { limit = 100 }` —
mirroring `parseParamsToolUsersSearch`.

**Verify**: `go build ./...` → exit 0

### Step 2 (Bug B): Resolve D/G-prefixed IDs in the IM/MPIM filter

In the `filter_in_im_or_mpim` branch (`conversations.go:2494-2500`), before
calling `paramFormatUser`: if the value starts with `D` or `G` (after
TrimSpace), look it up in `ch.apiProvider.ProvideChannelsMaps().Channels`.

- If found: pass the channel through as the `in:` filter value in the form
  Slack search accepts. Inspect how `filter_in_channel` formats its value
  (the branch immediately above, `conversations.go:2490` via its own
  format helper) and reuse that formatting for the resolved conversation.
- If NOT found in the cache: return an error that actually helps the agent:
  `fmt.Errorf("conversation %q not found in cache; pass the '@username' form instead", im)` —
  never the misleading "user not found".

If, after reading the channel-cache shape, it is genuinely unclear what
string Slack's `in:` modifier needs for an IM (e.g. the cache stores no
usable name for DMs), implement ONLY the improved error message and record
that in your report — that alone fixes the misleading failure.

**Verify**: `go build ./...` → exit 0

### Step 3 (Bug C): Distinguish absent from explicit-zero `date_due`

In `SavedUpdateHandler` (`saved.go:203-212`): read the raw arguments map
(`request.GetArguments()`) to detect whether the `date_due` key was provided
at all. Validation becomes: error only when `mark` is empty AND `date_due` is
*absent*. An explicit `date_due: 0` passes through to
`SavedUpdate(ctx, "message", itemID, ts, mark, 0)` (the documented clear).

**Verify**: `go build ./...` → exit 0

### Step 4 (Bug D): Ignore the duration window when a cursor is present

In the parse block at `conversations.go:2056-2068`: apply the same guard the
numeric branch has — when `cursor != ""`, skip `limitByExpression` (leave
`paramLimit`/`paramOldest`/`paramLatest` zero-valued, exactly like the numeric
branch does today when a cursor is present) and log at Debug that the
duration limit was ignored in favor of the cursor. This makes the
schema-default `"1d"` + cursor combination behave like numeric-limit + cursor
already does. Optionally append one sentence to the `limit` description in
`server.go` ("Ignored when 'cursor' is provided.").

**Verify**: `go build ./...` → exit 0

### Step 5: Tests

Add table-driven `TestUnit*` cases (pattern: `TestUnitLimitByExpression_Valid`,
`conversations_test.go:516`):

- A: parse with no limit → 20; `limit: 0` → 20; `limit: 500` → 100; `limit: 50` → 50.
- B: `D…` value with a stubbed channels map resolves (or errors with the new
  message — match what you implemented); `U…` and `@name` behavior unchanged.
- C: in `saved_test.go` (mirror its existing param-validation tests):
  `mark` absent + `date_due: 0` explicit → accepted; both absent → error;
  `mark: "completed"` alone → accepted.
- D: cursor + `limit: "1d"` → no `Oldest`/`Latest` set; cursor absent +
  `"1d"` → window set (existing behavior).

If a parse function is not directly callable with a constructed
`mcp.CallToolRequest`, follow however the existing handler tests construct
requests; if none do and construction is impractical, extract the minimal
pure logic (e.g. the clamp) into a helper and test that — note it in the report.

**Verify**: `go test -count=1 -run 'TestUnit' ./pkg/handler/` → pass, including new cases

### Step 6: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

See Step 5 — one regression test per bug, in the file that owns the handler.

## Done criteria

- [ ] `make test` exits 0; new tests for A–D exist and pass
- [ ] `grep -n 'GetInt("limit", 100)' pkg/handler/conversations.go` → no match in the search parser
- [ ] A `D…` `filter_in_im_or_mpim` value no longer produces "user ... not found"
- [ ] Explicit `date_due: 0` with empty `mark` is accepted by `SavedUpdateHandler` parsing
- [ ] `git status` shows only in-scope files modified
- [ ] `plans/README.md` status row updated

## STOP conditions

- An excerpt cannot be located (drift — especially if plan 005's branch was
  merged; report and wait for re-baselining).
- Bug B: Slack's `in:` modifier semantics for IMs can't be determined from
  the codebase → implement the error-message-only fallback and report (this
  is a documented partial-success path, not a failure).
- Bug C: `SavedUpdate`'s signature or the edge DTO can't express "clear" →
  stop, report; do not remove the schema's "Set to 0 to clear" promise
  yourself.
- Any fix seems to require touching the render pipeline or `buildQuery`
  beyond the named branches.

## Maintenance notes

- If plan 005 (search sort/has) merges after this, the search parser gains
  more keys near Bug A/B's code — merge conflicts are expected to be trivial
  but the parse-order of filters matters; re-run the search unit tests after
  merging.
- Reviewer: check Bug D preserves the exact zero-value behavior the numeric
  branch has (paramLimit 0 → Slack default page size), and Bug B's channel
  formatting matches the `filter_in_channel` branch.
- Deferred: `limitByExpression` hardcoding 100 as its count; the three-way
  timezone inconsistency between `limitByExpression` (local), `parseFlexibleDate`
  (UTC), and rendering (UTC) — needs a product decision.
