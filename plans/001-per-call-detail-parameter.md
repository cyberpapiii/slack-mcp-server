# Plan 001: Thread an explicit output mode through the render pipeline, controlled by a per-call `detail` parameter

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat 7781aaf..HEAD -- pkg/handler/ pkg/text/ pkg/server/server.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED (touches every message-returning tool's output path)
- **Depends on**: none
- **Category**: dx
- **Planned at**: commit `7781aaf`, 2026-07-01

## Why this matters

This Go MCP server for Slack renders tool output in one of two formats — compact CSV (default) or verbose CSV — selected by the `SLACK_MCP_COMPACT_OUTPUT` environment variable, read at call time deep inside the render code. Because it is process-wide, an agent cannot do cheap compact triage sweeps and then re-fetch one thread in full fidelity; changing modes requires editing the host config and restarting the server. This plan makes the output mode an explicit value resolved once per request — from a new optional `detail` tool parameter, falling back to the env var — and threads it through the render pipeline as a function argument. It is the foundation for plans 002 and 003, which add richer rendering that must be mode-aware.

## Current state

- `pkg/handler/conversations.go` — main conversations handler.
  - `compactOutput()` at line 2659–2669 reads the env var:
    ```go
    // compactOutput is the default message format. Set SLACK_MCP_COMPACT_OUTPUT=false
    // for legacy verbose CSV (all columns including UserID and Permalink).
    func compactOutput() bool {
    	v := strings.TrimSpace(os.Getenv("SLACK_MCP_COMPACT_OUTPUT"))
    	switch strings.ToLower(v) {
    	case "0", "false", "no":
    		return false
    	default:
    		return true
    	}
    }
    ```
  - `marshalMessagesToCSV` at line 2610 branches on it:
    ```go
    func marshalMessagesToCSV(messages []Message) (*mcp.CallToolResult, error) {
    	if compactOutput() {
    		return marshalMessagesToCompactCSV(messages)
    	}
    	csvBytes, err := gocsv.MarshalBytes(&messages)
    	...
    }
    ```
  - Call sites of `marshalMessagesToCSV` (all must be updated):
    - `pkg/handler/conversations.go:856` (ConversationsHistoryHandler path)
    - `pkg/handler/conversations.go:894` (replies path)
    - `pkg/handler/conversations.go:931`
    - `pkg/handler/conversations.go:1238` (unreads path)
    - `pkg/handler/conversations.go:1378` (search path)
    - `pkg/handler/activity.go:188`
    - `pkg/handler/saved.go:176`
- `pkg/text/text_processor.go` — text rendering helpers.
  - Has its own duplicate `compactOutput()` at line 98–106 (same body).
  - `AttachmentToText` at line 18 branches on it:
    ```go
    func AttachmentToText(att slack.Attachment) string {
    	if compactOutput() {
    		return attachmentToCompactText(att)
    	}
    	return attachmentToFullText(att)
    }
    ```
  - `AttachmentToText` is called from `AttachmentsTo2CSV` (same file, line 165), which is called during message *conversion* (not marshaling) at `pkg/handler/conversations.go:1913` (`convertMessagesFromHistory`) and `pkg/handler/conversations.go:1992` (`convertMessagesFromSearch`). This means the mode must be threaded into the conversion functions too, not just the marshal functions.
- `pkg/server/server.go` — tool definitions. Message-returning tools that need the new `detail` parameter, by registration line:
  - `ToolConversationsHistory` — line 173
  - `ToolConversationsReplies` — line 196
  - `ToolConversationsSearchMessages` — line 336 (note: assigned to `conversationsSearchTool` variable, not inline)
  - `ToolSavedList` — line 566
  - `ToolConversationsUnreads` — line 739 (in `registerCacheDependentTools`, the delayed-registration path)
  - `ToolActivityUnreads` — line 775 (same delayed path)
- Parameter conventions: handlers read string params via `request.GetString("name", "")` (e.g. `pkg/handler/conversations.go:493`). Tool params are declared with `mcp.WithString("name", mcp.Description(...))` — see `pkg/server/server.go:162-172` (`ToolConversationsOpen`) for the pattern.
- Existing test for the current behavior: `TestUnitMarshalMessagesToCompactCSV` in `pkg/handler/conversations_test.go:775` uses `t.Setenv("SLACK_MCP_COMPACT_OUTPUT", "true")` then calls `marshalMessagesToCSV(...)` directly.
- Repo conventions: zap structured logging, table-driven tests with `testify` (`require`/`assert`), unit tests named `TestUnit*`, integration tests named `TestIntegration*` (skipped by `make test`).

## Commands you will need

| Purpose   | Command              | Expected on success |
|-----------|----------------------|---------------------|
| Build     | `go build ./...`     | exit 0              |
| Tests     | `make test` (= `go test -count=1 -v -skip="Integration" ./...`) | all pass |
| Vet       | `go vet ./...`       | exit 0              |
| Format    | `make format`        | exit 0, no diff churn beyond your files |

## Scope

**In scope** (the only files you should modify):
- `pkg/handler/conversations.go`
- `pkg/handler/activity.go`
- `pkg/handler/saved.go`
- `pkg/text/text_processor.go`
- `pkg/server/server.go` (only the six tool definitions listed above — add one parameter each)
- `pkg/handler/conversations_test.go`, `pkg/text/text_processor_test.go` (tests)

**Out of scope** (do NOT touch, even though they look related):
- `pkg/server/tool_phases.go` — tool phase registry; adding a parameter does not change a tool's phase.
- Column shapes of `Message` / `CompactMessage` structs — plans 002/003 handle format changes; this plan changes only *how the mode is selected*, output bytes must be identical for a given mode.
- `docs/`, `README.md` — documentation updates are deferred to plan 003.
- The env var name/semantics of `SLACK_MCP_COMPACT_OUTPUT` — it remains the default-provider, unchanged.

## Git workflow

- Branch: `advisor/001-per-call-detail-parameter` (repo already uses `advisor/*` branches — see `git log`).
- Commit style: imperative sentence, capitalized, no prefix — e.g. `Harden two-phase tool registration and add slack_auth_status tool`.
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Define an `OutputMode` type in `pkg/text`

In `pkg/text/text_processor.go`, add:

```go
// OutputMode selects the render fidelity for tool output. Resolved once per
// request from the tool's `detail` parameter, falling back to the
// SLACK_MCP_COMPACT_OUTPUT env var.
type OutputMode int

const (
	ModeStandard OutputMode = iota // compact, agent-oriented (default)
	ModeFull                       // verbose legacy format, all columns
)

// ResolveOutputMode maps a tool's `detail` parameter to an OutputMode.
// Empty string defers to the SLACK_MCP_COMPACT_OUTPUT env var.
func ResolveOutputMode(detailParam string) (OutputMode, error) {
	switch strings.ToLower(strings.TrimSpace(detailParam)) {
	case "":
		if compactOutput() {
			return ModeStandard, nil
		}
		return ModeFull, nil
	case "standard":
		return ModeStandard, nil
	case "full":
		return ModeFull, nil
	default:
		return ModeStandard, fmt.Errorf("invalid detail value %q: must be \"standard\" or \"full\"", detailParam)
	}
}
```

Keep the existing `compactOutput()` in this file as the private env fallback. Change `AttachmentToText` to take the mode explicitly:

```go
func AttachmentToText(att slack.Attachment, mode OutputMode) string {
	if mode == ModeFull {
		return attachmentToFullText(att)
	}
	return attachmentToCompactText(att)
}
```

Update `AttachmentsTo2CSV` (line 165) to accept and pass through `mode OutputMode`.

**Verify**: `go build ./pkg/text/` → exit 0 (the handler package will not build yet; that is expected until Step 2).

### Step 2: Thread the mode through the handler package

In `pkg/handler/conversations.go`:

1. Change `marshalMessagesToCSV(messages []Message)` → `marshalMessagesToCSV(messages []Message, mode text.OutputMode)`; branch on `mode == text.ModeFull` instead of `!compactOutput()`. Delete the now-unused local `compactOutput()` at line 2659 (the one in `pkg/text` remains).
2. Change `convertMessagesFromHistory(ctx, slackMessages, channel, includeActivity)` and `convertMessagesFromSearch(ctx, slackMessages)` to accept a trailing `mode text.OutputMode` and pass it to `text.AttachmentsTo2CSV` at lines 1913 and 1992.
3. In each tool handler that ultimately calls `marshalMessagesToCSV` (history, replies, search, unreads), resolve the mode once at the top:
   ```go
   mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
   if err != nil {
   	return nil, err
   }
   ```
   and pass it down through the conversion and marshal calls. Trace each of the call sites listed in "Current state" back to its handler entry point to find where `request` is available.
4. Do the same in `pkg/handler/activity.go` (call site line 188) and `pkg/handler/saved.go` (call site line 176).

Where a conversion function is called from a path that has no `request` (if any), pass the env-resolved default: `mode, _ := text.ResolveOutputMode("")`.

**Verify**: `go build ./...` → exit 0.

### Step 3: Declare the `detail` parameter on the six tools

In `pkg/server/server.go`, add to each of the six tool definitions listed in "Current state" (history:173, replies:196, search:336, saved_list:566, unreads:739, activity_unreads:775):

```go
mcp.WithString("detail",
	mcp.Description("Output fidelity: 'standard' (default; compact agent-oriented CSV) or 'full' (verbose CSV with all columns including UserID and Permalink where available). Overrides the server-wide default for this call only."),
),
```

Match the surrounding parameter style exactly (see the existing `mcp.WithString("cursor", ...)` declarations in the same tool definitions).

**Verify**: `go build ./...` → exit 0, then `make test` → all pass (existing tests still pass because empty `detail` defers to the env var, preserving current behavior).

### Step 4: Tests

See "Test plan" below. Write them, then run the full gate.

**Verify**: `make test` → all pass including the new tests; `go vet ./...` → exit 0; `make format` → no unexpected diffs (`git status`).

## Test plan

- In `pkg/text/text_processor_test.go` (model after the existing table-driven tests in that file):
  - `TestUnitResolveOutputMode`: table over `""` with env unset → `ModeStandard`; `""` with `t.Setenv("SLACK_MCP_COMPACT_OUTPUT", "false")` → `ModeFull`; `"standard"` → `ModeStandard`; `"full"` → `ModeFull`; `"FULL"` (case-insensitive) → `ModeFull`; `"bogus"` → error.
  - `TestUnitAttachmentToTextModeSelection`: one attachment with title+link+text; `ModeStandard` output contains title and link but not the body text; `ModeFull` output contains the body text. (Adapt the existing tests at lines 267–449 that currently use `t.Setenv` — convert them to pass the mode explicitly; keep at least one env-driven test via `ResolveOutputMode("")` to cover the fallback.)
- In `pkg/handler/conversations_test.go`:
  - Update `TestUnitMarshalMessagesToCompactCSV` (line 775) to call `marshalMessagesToCSV(msgs, text.ModeStandard)`; assertions unchanged.
  - Add `TestUnitMarshalMessagesToFullCSV`: same input, `text.ModeFull` → body contains `Permalink` and `U03BMAR2R50` (the inversions of the compact assertions).
- Verification: `make test` → all pass.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go build ./...` exits 0
- [ ] `make test` exits 0; `TestUnitResolveOutputMode` and `TestUnitMarshalMessagesToFullCSV` exist and pass
- [ ] `grep -n "compactOutput()" pkg/handler/` returns no matches (handler no longer reads the env directly)
- [ ] `grep -c 'WithString("detail"' pkg/server/server.go` returns `6`
- [ ] With `detail` omitted, output is byte-identical to before for both env settings (covered by the unchanged assertions in existing tests)
- [ ] No files outside the in-scope list are modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The code at the locations in "Current state" doesn't match the excerpts (drift since `7781aaf`).
- You find a call path into `text.AttachmentToText` that cannot reach a `request` object and is not covered by the env-default fallback described in Step 2.
- Any existing `TestUnit*` test fails for a reason other than a signature change you made deliberately.
- Adding the parameter to `conversationsSearchTool` (line 336) conflicts with how that tool variable is later mutated/registered — inspect its uses before editing; if it is registered twice with different options, report instead of guessing.

## Maintenance notes

- Plans 002 and 003 build on the `OutputMode` threading; if you rename the type or the parameter, update those plans before executing them.
- Reviewer should scrutinize: that every `marshalMessagesToCSV` caller resolves the mode from the *request* (not the env) when a request is in scope, and that the `detail` description string is identical across all six tools.
- Deferred: a `minimal` mode tier was considered and deliberately not included — two modes cover the current need; add a third only with a concrete use case.
