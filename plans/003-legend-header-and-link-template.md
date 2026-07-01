# Plan 003: Prepend a legend header (users, workspace, link template) to standard-mode message output and replace HasMedia with a Files count

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 7781aaf..HEAD -- pkg/handler/ pkg/provider/api.go pkg/server/server.go`
> Plans 001/002 intentionally change `pkg/handler` and `pkg/text` (OutputMode
> threading) — that drift is expected and this plan assumes it. On any other
> mismatch with the excerpts below, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED (changes the default output format all agents consume)
- **Depends on**: plans/001-per-call-detail-parameter.md (signature threading; 002 is independent but lands cleaner first)
- **Category**: dx
- **Planned at**: commit `7781aaf`, 2026-07-01

## Why this matters

The compact (default) message CSV drops per-row `UserID` and `Permalink` to save tokens — but that loses real capability: agents can't cite messages with clickable links or chain into user-ID-based follow-ups (DMs, usergroups) without extra lookups. Notably, **permalinks are missing from history/replies output even in full mode** — `convertMessagesFromHistory` never populates the `Permalink` field; only the search path gets one from the API. Both losses are recoverable at near-zero token cost with normalization: emit the workspace link *template* once per response (permalinks are a pure function of workspace + channel + timestamp), and emit a `#users:` legend mapping the handful of distinct speakers to their IDs once, instead of repeating IDs on every row. Finally, the `HasMedia` boolean column becomes a `Files` count — an integer costs the same tokens as `"true"` and is strictly more informative.

## Current state

- `pkg/handler/conversations.go`:
  - `Message` struct (lines 48–64) already carries everything the legend needs per row: `UserID`, `UserName`, `RealName`, `BotName`, `Channel`, `MsgID`, `FileCount`, `HasMedia`.
  - `CompactMessage` struct (lines 69–80), the CSV shape this plan modifies:
    ```go
    type CompactMessage struct {
    	User          string `csv:"User"`
    	Channel       string `csv:"Channel"`
    	Text          string `csv:"Text"`
    	Time          string `csv:"Time"`
    	MsgID         string `csv:"MsgID"`
    	ThreadTs      string `csv:"ThreadTs,omitempty"`
    	Reactions     string `csv:"Reactions,omitempty"`
    	AttachmentIDs string `csv:"AttachmentIDs,omitempty"`
    	HasMedia      string `csv:"HasMedia,omitempty"`
    	Cursor        string `csv:"Cursor,omitempty"`
    }
    ```
  - `marshalMessagesToCompactCSV` (line 2622 at `7781aaf`; after plan 001 it takes a mode/context argument) builds `CompactMessage` rows — `User` is `RealName`, falling back to `UserName`, or `BotName + " (bot)"` — then `gocsv.MarshalBytes` and `mcp.NewToolResultText(string(csvBytes))`.
  - `MsgID` is the raw Slack timestamp (e.g. `1782935556.396379`). Slack permalink format: `https://<workspace>.slack.com/archives/<CHANNEL_ID>/p<ts-without-dot>` (e.g. `p1782935556396379` — see the fixture in `conversations_test.go:788` which pairs that MsgID with exactly that permalink).
  - **Channel column format varies**: history/replies pass a `channel` string through as-is; the search path formats it as `"%s (#%s)"` (ID + name, `conversations.go:2002`). The link template documentation must say "use the leading channel ID token".
- Workspace URL source: `pkg/provider/api.go` — `MCPSlackClient.AuthResponse()` (line 908) returns the cached `*slack.AuthTestResponse` whose `.URL` is the workspace URL (e.g. `https://myorg.slack.com/`). The `ApiProvider` struct (line 373) holds `client SlackAPI`; the `SlackAPI` interface does **not** expose `AuthResponse`, so a new provider method must type-assert. Precedent for parsing: `pkg/server/server.go:622-646` calls `provider.Slack().AuthTest()` and `text.Workspace(ar.URL)` (helper at `pkg/text/text_processor.go:253`).
- Handlers hold `ch.apiProvider *provider.ApiProvider` — `NewConversationsHandler` in `conversations.go`.
- Existing compact-CSV test to extend: `TestUnitMarshalMessagesToCompactCSV` at `pkg/handler/conversations_test.go:775`.

## Commands you will need

| Purpose   | Command              | Expected on success |
|-----------|----------------------|---------------------|
| Build     | `go build ./...`     | exit 0              |
| Tests     | `make test` (= `go test -count=1 -v -skip="Integration" ./...`) | all pass |
| Vet       | `go vet ./...`       | exit 0              |

## Scope

**In scope** (the only files you should modify):
- `pkg/handler/conversations.go`
- `pkg/handler/activity.go`, `pkg/handler/saved.go` (only if the marshal signature change from Step 3 requires touching their call sites)
- `pkg/provider/api.go` (one new method)
- `pkg/server/server.go` (tool description strings only)
- `pkg/handler/conversations_test.go`, `pkg/provider/api_test.go` if present (tests)

**Out of scope** (do NOT touch, even though they look related):
- Full-mode output (`Message` struct CSV) — unchanged; it is the escape hatch and must stay stable.
- `pkg/text/text_processor.go` rendering functions — plan 002's territory.
- `users_search` / `channels_*` tool output — different shapes; legends there are a possible follow-up, not this plan.
- Anything under `pkg/provider/edge/`.

## Git workflow

- Branch: `advisor/003-legend-header-and-link-template`
- Commit style: imperative sentence, capitalized — e.g. `Add legend header and link template to compact message output`.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Expose the workspace URL from the provider

In `pkg/provider/api.go`, add:

```go
// WorkspaceURL returns the cached workspace base URL from auth (e.g.
// "https://myorg.slack.com/"), or "" when the client type doesn't cache it
// (callers must treat "" as "omit workspace-dependent output").
func (ap *ApiProvider) WorkspaceURL() string {
	if c, ok := ap.client.(*MCPSlackClient); ok {
		if ar := c.AuthResponse(); ar != nil {
			return ar.URL
		}
	}
	return ""
}
```

**Verify**: `go build ./pkg/provider/` → exit 0.

### Step 2: Replace `HasMedia` with `Files` in `CompactMessage`

Change the field to `Files string \`csv:"Files,omitempty"\``. In the row-building loop in `marshalMessagesToCompactCSV`, set it from the source message's `FileCount`: empty string when 0, otherwise `strconv.Itoa(m.FileCount)`. Note: the search path (`convertMessagesFromSearch`) sets `HasMedia` but not `FileCount`; there, emit `"1"` when `HasMedia` is true and `FileCount` is 0 — a floor, documented in the code with a one-line comment.

**Verify**: `go build ./...` → exit 0. The existing test asserting `HasMedia` will fail — fix it in Step 5.

### Step 3: Build the legend header

In `pkg/handler/conversations.go`, add a helper near `marshalMessagesToCompactCSV`:

```go
// buildLegendHeader emits comment lines (agent-oriented, not CSV data) that
// normalize per-row redundancy: distinct users with their IDs, and a permalink
// template so links are derivable without a Permalink column. Skipped for
// tiny result sets where the header would outweigh the rows.
func buildLegendHeader(messages []Message, workspaceURL string) string
```

Behavior:
- Return `""` when `len(messages) < 3`.
- `#users:` line — iterate messages in order, collect **distinct** non-empty `UserID`s (skip bot rows: `BotName != ""`); render each as `UID=username|Real Name` (omit `|Real Name` when equal to username or empty), comma-space separated, in first-appearance order (deterministic — no map iteration).
- `#link_template:` line — only when `workspaceURL != ""`:
  `#link_template: <workspaceURL>archives/{CHANNEL_ID}/p{MsgID with "." removed}  (Channel column may contain "ID (#name)" — use the leading ID)`
  (workspaceURL from AuthTest ends with `/` — see the `ar.URL` usage in `server.go:640`; do not double the slash.)
- Lines end with `\n`; the header block ends with exactly one `\n` before the CSV header row.

Thread `workspaceURL` into the marshal path: the cleanest route is a small struct (e.g. `renderOptions{mode text.OutputMode, workspaceURL string}`) replacing the bare mode argument added in plan 001, populated by handlers via `ch.apiProvider.WorkspaceURL()`. Apply the header **only** in the compact/standard branch: `mcp.NewToolResultText(legend + string(csvBytes))`.

**Verify**: `go build ./...` → exit 0.

### Step 4: Document the format in the tool descriptions

In `pkg/server/server.go`, append one sentence to the `mcp.WithDescription` of the six message-returning tools (history:173, replies:196, search:336, saved_list:566, unreads:739, activity_unreads:775 at `7781aaf`):

> "Output may begin with `#users:` (UserID=name legend) and `#link_template:` (construct message permalinks from Channel + MsgID) comment lines before the CSV header."

**Verify**: `go build ./...` → exit 0.

### Step 5: Tests

Write the tests below, fix the `HasMedia` assertion in the existing test, run the gate.

**Verify**: `make test` → all pass; `go vet ./...` → exit 0.

## Test plan

In `pkg/handler/conversations_test.go`, modeled on `TestUnitMarshalMessagesToCompactCSV` (line 775):

- Update the existing test: `HasMedia` assertions become `Files` / `"1"`; add `assert.NotContains(t, body, "HasMedia")`.
- `TestUnitCompactCSVLegendHeader`: 4 messages, 2 distinct users + 1 bot, workspace URL `https://loop.slack.com/` → body starts with `#users:` containing both `UID=name` pairs (bot excluded), contains `#link_template: https://loop.slack.com/archives/`, and the CSV header row follows the legend block.
- `TestUnitCompactCSVLegendSkippedForSmallResults`: 2 messages → body starts directly with the CSV header, no `#` lines.
- `TestUnitCompactCSVLegendNoWorkspace`: workspace URL `""` → `#users:` present, `#link_template:` absent.
- `TestUnitCompactCSVLegendDeterministic`: call the marshal twice with the same input → byte-identical output (guards against map-ordered iteration).
- Full-mode regression: `text.ModeFull` output contains no `#` legend lines.

Verification: `make test` → all pass, including 5 new tests.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go build ./...` exits 0; `go vet ./...` exits 0
- [ ] `make test` exits 0; the 5 new legend tests exist and pass
- [ ] `grep -n "HasMedia" pkg/handler/conversations.go` matches only the `Message` struct field (full mode), not `CompactMessage`
- [ ] `grep -c "link_template" pkg/server/server.go` ≥ 6 (all six tool descriptions updated)
- [ ] `grep -n "func (ap \*ApiProvider) WorkspaceURL" pkg/provider/api.go` matches
- [ ] No files outside the in-scope list are modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- Plan 001 has not landed (marshal functions don't take a mode argument) — this plan's Step 3 extends that signature.
- `AuthResponse()` returns nil or a URL without a trailing `/` in your testing — verify the actual shape before hard-coding the template join, and report if it contradicts the `server.go:640` precedent.
- The `gocsv` output turns out to quote or mangle prepended non-CSV lines (it shouldn't — the legend is concatenated *outside* the marshal) — if you find yourself modifying gocsv behavior, stop.
- Any consumer test outside `pkg/handler` asserts the exact compact CSV shape and fails — list them and report; there may be downstream contracts the audit didn't see.

## Maintenance notes

- The legend grammar (`#users:`, `#link_template:`) is now part of the agent-facing contract; document it in `docs/agent-presets.md` as a follow-up (deferred here to keep scope tight — one file, one paragraph).
- If a `#channels:` legend is ever added (channel ID → name for the unreads output), reuse `buildLegendHeader`'s structure and the same skip-below-3-rows rule.
- Reviewer should scrutinize: first-appearance ordering (determinism), the bot-exclusion rule in the `#users:` line, and that full mode is byte-identical to pre-plan output.
- The search-path `Files` floor (`"1"` for has-media-but-unknown-count) is a documented approximation; if search results ever carry real file lists, remove the floor.
