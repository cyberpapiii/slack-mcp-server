# Plan 023: Restore the `conversations_search_messages` default limit to 100

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. Do NOT edit anything under `plans/`; the reviewer
> maintains the index.
>
> **Worktree check (run zeroth)**: `git rev-parse --short HEAD` must be
> `2ab41b7` or a descendant; otherwise STOP.

## Status

- **Priority**: P1 (corrects a behavior regression introduced by plan 009)
- **Effort**: XS
- **Risk**: LOW
- **Depends on**: plan 009 (this amends it), stacked at Track B tip
- **Category**: regression fix
- **Planned at**: commit `2ab41b7`, 2026-08-07

## Why this matters

Plan 009 found a genuine contract mismatch: the `conversations_search_messages`
schema declared `mcp.DefaultNumber(20)` while the handler read
`req.GetInt("limit", 100)`. Two halves of one tool disagreed about the default.

Plan 009 resolved it by moving the **code** to 20. That was the wrong side to
move. The maintainer wants the effective default to stay **100** — the schema
number was the stale one. This plan flips the resolution: schema and code both
say 100.

The other half of plan 009's fix — clamping out-of-range values instead of
forwarding them to Slack — was correct and **stays exactly as it is**. Only the
default changes.

Net effect: the default result count returns to its pre-009 behavior (100),
while the clamp bugfix is retained.

## Current state

All excerpts verified at commit `2ab41b7`.

`pkg/server/server.go:380-383` — the schema:

```go
		mcp.WithNumber("limit",
			mcp.DefaultNumber(20),
			mcp.Description("The maximum number of items to return. Must be an integer between 1 and 100."),
		),
```

`pkg/handler/conversations.go:36-39` — the constants:

```go
	// Search `limit` bounds, mirroring the tool schema's DefaultNumber(20) and
	// its "Must be an integer between 1 and 100" description.
	defaultSearchMessagesLimit = 20
	maxSearchMessagesLimit     = 100
```

`pkg/handler/conversations.go:2678-2684` — the parse site (clamp logic is
correct; do not restructure it):

```go
	limit := req.GetInt("limit", defaultSearchMessagesLimit)
	if limit <= 0 {
		limit = defaultSearchMessagesLimit
	}
	if limit > maxSearchMessagesLimit {
		limit = maxSearchMessagesLimit
	}
```

There is an existing test `TestUnitParseParamsToolSearchLimit` in
`pkg/handler/conversations_test.go` written by plan 009. Locate it by name and
read it fully before editing — it asserts the 20 default and will fail until
updated.

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| All unit tests | `make test` | exit 0 |
| Targeted | `go test -count=1 -run 'TestUnitParseParamsToolSearchLimit' ./pkg/handler/` | pass |
| Format | `gofmt -l pkg cmd` | no output |

Note: `make test` on this base runs with `-race` only if plan 018 is in the
base. It is not (018 is on the other track). Plain `make test` is correct here.

## Scope

**In scope**:
- `pkg/server/server.go` — the `conversations_search_messages` `limit` schema
  only.
- `pkg/handler/conversations.go` — the `defaultSearchMessagesLimit` constant
  and its comment only.
- `pkg/handler/conversations_test.go` — `TestUnitParseParamsToolSearchLimit`
  expectations only.

**Out of scope**:
- `maxSearchMessagesLimit` (stays 100) and the clamp logic (stays as written).
- The `limit` schema of any *other* tool. `users_search` uses
  `DefaultNumber(10)` and `files_list` uses `DefaultString("50")` — both are
  correct for their own tools. Do not touch them.
- Anything else plan 009 changed (the `D…`/`G…` IM filter, `date_due: 0`, the
  cursor-vs-duration-limit fix).

## Git workflow

- Branch: `advisor/023-search-limit-default-100`, based on `2ab41b7`.
- One commit, imperative subject. Do NOT push.

## Steps

### Step 1: Schema

In `pkg/server/server.go`, change `mcp.DefaultNumber(20)` to
`mcp.DefaultNumber(100)` for the `conversations_search_messages` `limit`
parameter **only**.

Leave the description string byte-identical — "Must be an integer between 1
and 100" is still accurate, since the max is unchanged.

Confirm you edited the right one: the correct site sits inside the
`conversations_search_messages` registration and is preceded by a `cursor`
parameter whose description mentions `next_cursor`.

**Verify**: `grep -n 'DefaultNumber(20)' pkg/server/server.go` → no output

### Step 2: Constant

In `pkg/handler/conversations.go`, set `defaultSearchMessagesLimit = 100` and
update the stale comment above it, which currently claims it mirrors
`DefaultNumber(20)`. Replacement comment:

```go
	// Search `limit` bounds. Default and max are both 100, matching the tool
	// schema's DefaultNumber(100) and its "between 1 and 100" description.
	defaultSearchMessagesLimit = 100
	maxSearchMessagesLimit     = 100
```

Do NOT collapse the two constants into one even though they now share a value
— they mean different things and the max may change independently.

**Verify**: `go build ./...` → exit 0

### Step 3: Update the existing test

Update `TestUnitParseParamsToolSearchLimit` so its expectations read 100 where
they read 20. Keep every case the test already covers — in particular the
out-of-range cases, which are the part of plan 009 worth keeping:

- absent `limit` → 100
- `limit: 0` → 100
- negative `limit` → 100
- `limit: 250` → 100 (clamped)
- a valid in-range value (e.g. 5) → 5, unchanged
- the `float64` JSON-encoding case plan 009 added → keep it, adjust expectation

If the test's name or table shape makes "20" appear in a subtest *name* as well
as an expectation, update both so the names don't lie.

**Verify**: `go test -count=1 -run 'TestUnitParseParamsToolSearchLimit' ./pkg/handler/` → pass

### Step 4: Full suite

**Verify**: `make test` → exit 0; `gofmt -l pkg cmd` → no output

## Test plan

Step 3 — the existing test, re-pointed. No new test file. The load-bearing
assertion is that an absent `limit` now yields 100, and that clamping still
works.

## Done criteria

- [ ] `grep -n 'DefaultNumber(20)' pkg/server/server.go` → no output
- [ ] `grep -n 'defaultSearchMessagesLimit' pkg/handler/conversations.go` shows
      the constant set to 100 and no stale "DefaultNumber(20)" comment
- [ ] `make test` exits 0
- [ ] `gofmt -l pkg cmd` → no output
- [ ] `git status` shows only the three in-scope files modified

## STOP conditions

- `DefaultNumber(20)` appears more than once in `pkg/server/server.go` — that
  would mean another tool shares the value; report which, change only the
  search one.
- `TestUnitParseParamsToolSearchLimit` does not exist under that name (plan 009
  may have named it differently) — locate the test asserting the search limit
  default, report the real name, and update that one.
- Any other test fails after the change — report it rather than adjusting it;
  it would mean something else depended on the 20 default.

## Maintenance notes

- The underlying lesson: when a schema and its handler disagree about a
  default, that is a question for the maintainer, not a detail to resolve by
  picking the more "authoritative"-looking side. Plan 009 picked the schema and
  changed observable tool behavior as a side effect of a bug-fix plan.
- Reviewer: confirm the clamp logic is untouched and that
  `maxSearchMessagesLimit` is still 100.
