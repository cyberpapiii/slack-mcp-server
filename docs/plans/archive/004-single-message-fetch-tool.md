# Plan 004: Add a `conversations_get_message` tool (fetch one message by channel + timestamp)

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat adbae97..HEAD -- pkg/handler/conversations.go pkg/server/server.go pkg/handler/conversations_test.go AGENTS.md`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: direction
- **Planned at**: commit `adbae97`, 2026-07-02

## Why this matters

This server's compact "standard" output mode truncates long attachments and appends a receipt telling the agent to *"re-fetch this message with detail: full"* — but there is no tool that fetches one message. `conversations_history` accepts no timestamp parameters (its `oldest`/`latest` are derived internally from duration strings like `"1d"`), so today the only recovery is re-paging a whole history window in full mode and filtering client-side. Meanwhile the exact single-message fetch already exists as an internal helper used by `reactions_get`, which throws the message body away. This plan wraps that helper in a read-only tool, closing the recovery loop the truncation receipt promises.

## Current state

Relevant files:

- `pkg/handler/conversations.go` — all conversation handlers; contains the internal single-message fetch helper (`fetchMessageForReactions`, line 548), the history→Message converter (`convertMessagesFromHistory`, used at line 856), and the CSV marshaller (`marshalMessagesToCSV`, used at line 861).
- `pkg/server/server.go` — tool name constants (~line 33), `ValidToolNames` (lines 65–96), and tool registration. `reactions_get` registration at lines 228–241 is the structural model.
- `pkg/handler/conversations_test.go` — handler tests. Unit tests use the `TestUnit` name prefix (anything without `Integration` in the name runs in CI); testify `assert`/`require` are the assertion style.
- `AGENTS.md` — states the tool count ("There are **30** tools", line 31) and lists fork-added tools (lines 33–40).

The internal helper (`pkg/handler/conversations.go:544-555`) — note the doc comment already says it works for any message, not just reactions:

```go
// fetchMessageForReactions retrieves a single message by timestamp using conversations.replies.
// This works for top-level messages, thread parents, and thread replies alike — no additional
// scopes beyond channels:history required.
//
// Note: Slack's reactions.get API is purpose-built for this but requires the reactions:read scope.
// We use conversations.replies instead to avoid introducing a new scope requirement.
func (ch *ConversationsHandler) fetchMessageForReactions(ctx context.Context, channel, timestamp string) (*slack.Message, error) {
	msgs, _, _, err := ch.apiProvider.Slack().GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: timestamp,
		Limit:     1,
		Inclusive: true,
	})
```

How `ReactionsGetHandler` uses it (`conversations.go:493-519`, abbreviated) — this is the param-handling pattern to copy:

```go
	rawChannel := request.GetString("channel_id", "")
	if rawChannel == "" {
		return nil, errors.New("channel_id is required")
	}
	channel, err := ch.resolveChannelID(ctx, rawChannel)
	...
	timestamp := request.GetString("timestamp", "")
	if timestamp == "" {
		return nil, errors.New("timestamp is required")
	}
	...
	msg, err := ch.fetchMessageForReactions(ctx, channel, timestamp)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return mcp.NewToolResultText("No message found at the specified timestamp"), nil
	}
```

How the history handler renders messages (`conversations.go:827-861`, abbreviated) — the render pattern to copy:

```go
	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}
	...
	messages := ch.convertMessagesFromHistory(ctx, history.Messages, params.channel, params.activity, mode)
	...
	return marshalMessagesToCSV(messages, renderOptions{mode: mode, workspaceURL: ch.apiProvider.WorkspaceURL()})
```

The `reactions_get` registration (`pkg/server/server.go:228-241`) — the registration pattern to copy, including its `channel_id` and `timestamp` parameter descriptions:

```go
	if shouldAddTool(ToolReactionsGet, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolReactionsGet,
			mcp.WithDescription("Get detailed reaction data for a specific message, ..."),
			mcp.WithTitleAnnotation("Get Message Reactions"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("ID of the channel in format Cxxxxxxxxxx or its name starting with #... or @... aka #general or @username_dm."),
			),
			mcp.WithString("timestamp",
				mcp.Required(),
				mcp.Description("Timestamp of the message to get reactions for, in format 1234567890.123456."),
			),
		), conversationsHandler.ReactionsGetHandler)
	}
```

The `detail` parameter description used by every message tool (copy it verbatim from `server.go:192-194`, the history registration):

```go
			mcp.WithString("detail",
				mcp.Description("Output fidelity: 'standard' (default; compact agent-oriented CSV) or 'full' (verbose CSV with all columns including UserID and Permalink where available). Overrides the server-wide default for this call only. Output may begin with `#users:` (UserID=name legend) and `#link_template:` (construct message permalinks from Channel + MsgID) comment lines before the CSV header."),
			),
```

Conventions: zap structured logging (`ch.logger.Error("...", zap.Error(err))`); errors via `errors.New`/`fmt.Errorf`; tools registered inside `shouldAddTool(...)` guards. This tool is **not** cache-dependent (works with a bare channel ID before caches warm), so it registers in the immediate phase in `NewMCPServer` — do NOT add it to `cacheDependentToolNames` or `immediateOnlyToolNames` in `pkg/server/tool_phases.go` (that registry is only for tools that need duplicate-registration guards across the two phases; read-only immediate tools like `reactions_get` appear in neither map).

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Tests | `go test -count=1 -skip="Integration" ./...` | all pass |
| Format check | `gofmt -l pkg/ cmd/` | no output |

## Scope

**In scope** (the only files you should modify):
- `pkg/handler/conversations.go` — new handler method + rename of the fetch helper
- `pkg/server/server.go` — tool constant, `ValidToolNames` entry, registration
- `pkg/handler/conversations_test.go` — new unit tests
- `AGENTS.md` — tool count and fork-added tool list
- `plans/README.md` — status row

**Out of scope** (do NOT touch, even though they look related):
- `pkg/server/tool_phases.go` — this tool needs no phase guard (see Conventions above).
- `docs/agent-presets.md` — preset allowlists are the maintainer's live config; they'll add the tool name when deploying.
- Any change to existing tools' output shapes or params.
- The Plug config (`~/Library/Application Support/plug/...`) — outside the repo entirely.

## Git workflow

- Branch: `advisor/004-single-message-fetch-tool`
- Commit style: imperative summary line, body explaining why (see `git log --oneline -10` for examples like "Cut oversized attachment titles on a rune boundary").
- Do NOT push or open a PR. This fork never pushes to origin.

## Steps

### Step 1: Rename the fetch helper to reflect its general purpose

In `pkg/handler/conversations.go`, rename `fetchMessageForReactions` → `fetchMessageByTimestamp` and update its doc comment's first line to `// fetchMessageByTimestamp retrieves a single message by timestamp using conversations.replies.` (keep the rest of the comment). Update its one existing call site in `ReactionsGetHandler` (line ~513).

**Verify**: `grep -rn "fetchMessageForReactions" pkg/` → no matches; `go build ./...` → exit 0.

### Step 2: Add the tool constant and ValidToolNames entry

In `pkg/server/server.go`, next to `ToolReactionsGet = "reactions_get"` (~line 39), add:

```go
	ToolConversationsGetMessage = "conversations_get_message"
```

Add `ToolConversationsGetMessage,` to `ValidToolNames` (lines 65–96), directly after `ToolConversationsReplies,`.

**Verify**: `go build ./...` → exit 0.

### Step 3: Add the handler

In `pkg/handler/conversations.go`, add `ConversationsGetMessageHandler` near `ReactionsGetHandler`. **Validate both string params before any provider call** (this ordering is load-bearing for the unit tests in Step 5):

```go
// ConversationsGetMessageHandler fetches a single message by channel + timestamp.
// It exists so agents holding a MsgID (e.g. from compact CSV rows, or after an
// attachment-truncation receipt) can re-read exactly one message, optionally
// with detail: full, instead of re-paging conversations_history.
func (ch *ConversationsHandler) ConversationsGetMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ch.logger.Debug("ConversationsGetMessageHandler called", zap.Any("params", request.Params))

	rawChannel := request.GetString("channel_id", "")
	if rawChannel == "" {
		return nil, errors.New("channel_id is required")
	}
	timestamp := request.GetString("timestamp", "")
	if timestamp == "" {
		return nil, errors.New("timestamp is required")
	}
	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}

	channel, err := ch.resolveChannelID(ctx, rawChannel)
	if err != nil {
		ch.logger.Error("Channel not found", zap.String("channel", rawChannel), zap.Error(err))
		return nil, err
	}

	msg, err := ch.fetchMessageByTimestamp(ctx, channel, timestamp)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return mcp.NewToolResultText("No message found at the specified timestamp"), nil
	}

	messages := ch.convertMessagesFromHistory(ctx, []slack.Message{*msg}, channel, true, mode)
	return marshalMessagesToCSV(messages, renderOptions{mode: mode, workspaceURL: ch.apiProvider.WorkspaceURL()})
}
```

Note: `convertMessagesFromHistory`'s third argument is the channel string, fourth is `includeActivityMessages` — pass `true` so a requested `channel_join`-type message is still returned (the caller asked for this exact ts). If the function's signature at your HEAD differs from `(ctx, []slack.Message, string, bool, text.OutputMode)`, treat it as drift → STOP.

**Verify**: `go build ./... && go vet ./...` → exit 0.

### Step 4: Register the tool

In `pkg/server/server.go`, directly after the `ToolReactionsGet` registration block (ends ~line 242), add a registration following the same pattern:

- `shouldAddTool(ToolConversationsGetMessage, enabledTools, "")` guard (read-only, no env opt-in).
- Description: `"Fetch a single message by channel and timestamp. Use the MsgID column from any compact CSV output as the timestamp — e.g. to re-fetch a message with detail: 'full' after seeing an attachment-truncation receipt. Returns the same CSV format as conversations_history."`
- `mcp.WithTitleAnnotation("Get Single Message")`, `mcp.WithReadOnlyHintAnnotation(true)`.
- `channel_id` (Required) and `timestamp` (Required) params: copy the descriptions from the `reactions_get` registration verbatim, changing "to get reactions for" to "to fetch".
- `detail` param: copy the description verbatim from the history registration (quoted in Current state).
- Handler: `conversationsHandler.ConversationsGetMessageHandler`.

**Verify**: `go build ./...` → exit 0.

### Step 5: Unit tests

In `pkg/handler/conversations_test.go`, add `TestUnitConversationsGetMessageParamValidation`. Construct the handler with a nil provider — safe because Step 3 validates params before any provider call:

```go
func TestUnitConversationsGetMessageParamValidation(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	// missing channel_id
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"timestamp": "1234567890.123456"}
	_, err := ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel_id")

	// missing timestamp
	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"channel_id": "C0123456789"}
	_, err = ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp")

	// invalid detail value
	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"channel_id": "C0123456789", "timestamp": "1234567890.123456", "detail": "bogus"}
	_, err = ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
}
```

Adapt the request-construction lines to however existing `TestUnit` tests in `pkg/handler/` build a `mcp.CallToolRequest` (check `activity_test.go` and `channels_test.go` for the local idiom first; if they use a helper, use it). If `ConversationsHandler`'s fields are unexported such that `&ConversationsHandler{logger: ...}` won't compile from the same package — it will, tests are in package `handler` — but if a constructor is genuinely required and needs a live provider, STOP.

Also add a wiring test in the same file or note where one exists:

```go
func TestUnitGetMessageToolNameRegistered(t *testing.T) {
	assert.Contains(t, server.ValidToolNames, "conversations_get_message")
}
```

If importing `pkg/server` from `pkg/handler` tests creates an import cycle, put this assertion in a `pkg/server` test file instead (create `pkg/server/server_test.go` with package `server` if none exists).

**Verify**: `go test -count=1 -skip="Integration" ./...` → all pass, including the 2 new tests.

### Step 6: Update AGENTS.md

- Line 31: change "There are **30** tools" to "There are **31** tools".
- In the fork-added tool list (lines 33–40), add a bullet: `- conversations_get_message — fetch one message by channel + timestamp (recovery path for attachment-truncation receipts)`.

**Verify**: `grep -n "31" AGENTS.md` shows the updated count; `grep -n "conversations_get_message" AGENTS.md` → 1 match.

## Test plan

- `TestUnitConversationsGetMessageParamValidation` — missing channel_id, missing timestamp, invalid detail (Step 5).
- `TestUnitGetMessageToolNameRegistered` — the name is in `ValidToolNames`.
- Structural pattern: existing `TestUnit*` tests in `pkg/handler/` (testify assert/require, `TestUnit` prefix so `make test` picks them up and CI's `-skip="Integration"` doesn't).
- Verification: `go test -count=1 -skip="Integration" ./...` → all pass.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `go build ./...` exits 0
- [ ] `go vet ./...` exits 0
- [ ] `go test -count=1 -skip="Integration" ./...` exits 0, including 2 new tests
- [ ] `gofmt -l pkg/ cmd/` prints nothing
- [ ] `grep -rn "fetchMessageForReactions" pkg/` → no matches
- [ ] `grep -c "conversations_get_message" pkg/server/server.go` ≥ 2 (constant + registration)
- [ ] No files outside the in-scope list modified (`git status`)
- [ ] `plans/README.md` status row updated

## STOP conditions

Stop and report back (do not improvise) if:

- The drift check shows in-scope files changed and the "Current state" excerpts no longer match.
- `convertMessagesFromHistory` or `marshalMessagesToCSV`/`renderOptions` signatures differ from the excerpts (the output-mode plumbing has moved).
- Handler unit tests cannot be written without a live Slack provider (constructor/visibility blocks the nil-provider approach).
- A step's verification fails twice after a reasonable fix attempt.

## Maintenance notes

- **The tool will not appear in Cursor/Plug until the maintainer adds `conversations_get_message` to `SLACK_MCP_ENABLED_TOOLS` in Plug's config and runs `make deploy-local`** (the allowlist gates exposure; see AGENTS.md "Plug deployment uses SLACK_MCP_ENABLED_TOOLS as an allowlist"). That is a deploy step, not an executor step.
- Maintainer's post-merge live check (per AGENTS.md preferences): fetch a known message by its MsgID from a compact history row, then again with `detail: full`, and confirm the truncation-receipt recovery loop works end to end.
- If a future change adds thread-aware params here (e.g. fetching a reply without knowing its thread), revisit `fetchMessageByTimestamp` — `conversations.replies` with `Timestamp: <reply ts>` already handles replies, which is why this works.
- Reviewer scrutiny: the `includeActivityMessages=true` choice in Step 3 (deliberate — an explicitly requested ts should always return), and that the rename in Step 1 touched only the two expected sites.
