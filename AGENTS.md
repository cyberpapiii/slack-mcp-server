# Agent guide: local slack-mcp-server fork

This repository is a **local fork** of [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server) with over 50 fork-authored commits of MCP and agent-oriented improvements on top of upstream v1.3.0. It is **not** the npm release path. Production use on this machine goes through **Plug** → `bin/slack-mcp-server`.

## Build and verify

```bash
make deps          # go mod download
make lint          # go vet + gofmt check + `go mod tidy -diff` (read-only)
make test          # all tests except *Integration*, with -race (CI gate)
make test-integration  # needs Slack/ngrok secrets
make build         # outputs ./build/slack-mcp-server (read-only: no tidy/format)
make prepare       # go mod tidy + go fmt, the only targets that rewrite files
make deploy-local  # build bin/slack-mcp-server + plug server disable/enable slack
go build -o bin/slack-mcp-server ./cmd/slack-mcp-server  # manual equivalent
```

`make lint && make test` is the whole verification gate. Neither mutates the
working tree; `make build` no longer runs `tidy`/`format` either, so it is safe
mid-review. Run `make prepare` explicitly when you want the tree reformatted.

After changing Go code, run `make deploy-local` (or rebuild `bin/slack-mcp-server` and restart Plug's `slack` server) so Cursor picks up the new binary.

## Runtime layout

| Layer | Location |
|-------|----------|
| Source | This repo (`master`, tracks `origin/master` + local commits) |
| Binary | `bin/slack-mcp-server` (Plug config target) |
| MCP multiplexer | Plug: `~/Library/Application Support/plug/config.toml` → `[servers.slack]` |
| Cursor entry | `~/.cursor/mcp.json` → `plug connect` |

Auth tokens (`SLACK_MCP_XOXC_TOKEN`, `SLACK_MCP_XOXD_TOKEN`, etc.) live in the environment Plug resolves. Never commit them.

The `sse` and `http` transports refuse to start unless `SLACK_MCP_API_KEY` is set (deprecated fallback: `SLACK_MCP_SSE_API_KEY`), or `SLACK_MCP_ALLOW_UNAUTHENTICATED` is set to exactly `true`. `1`/`yes` are rejected. `stdio` (the Plug path) is unaffected. See `pkg/server/auth/sse_auth.go`.

`SLACK_MCP_SERVER_CA_TOOLKIT` is unusable: the embedded HTTP Toolkit CA in `pkg/transport/transport.go` expired **2026-03-13**. Setting the env var fatals at startup. For MitM debugging, set `SLACK_MCP_SERVER_CA` to a current CA PEM instead.

## Tool surface

Canonical tool names: `ValidToolNames` in `pkg/server/server.go`. There are **31** tools; upstream README documents fewer.

The full set, grouped (★ = local-only or fork-extended):

| Group | Tools |
|-------|-------|
| Messages | `conversations_history`, `conversations_replies`, ★`conversations_get_message`, `conversations_search_messages`, `conversations_add_message`, ★`conversations_draft_message` |
| Conversations | ★`conversations_open`, ★`conversations_unreads`, `conversations_mark`, `conversations_join`, `conversations_leave` |
| Channels | `channels_list`, ★`channels_starred`, ★`channels_me` |
| Reactions | `reactions_add`, `reactions_remove`, ★`reactions_get` |
| Usergroups | `usergroups_list`, `usergroups_me`, `usergroups_create`, `usergroups_update`, `usergroups_users_update` |
| Users | `users_search` |
| Activity | ★`activity_unreads`, ★`activity_mark_read` (browser session / xoxc+xoxd) |
| Saved items | ★`saved_list`, ★`saved_update`, ★`saved_clear_completed` |
| Files | ★`files_list`, `attachment_get_data` |
| Diagnostics | ★`slack_auth_status`: cache and browser-session health (call before activity/saved tools) |

`conversations_get_message` fetches a single message by channel and timestamp, the recovery path for an attachment-truncation receipt.

Output is compact CSV by default (keeps MsgID/ThreadTs for follow-up actions). Message tools take a per-call `detail` parameter (`standard`/`full`); `SLACK_MCP_COMPACT_OUTPUT` only sets the server-wide default when `detail` is omitted. Standard mode may prepend `#users:`/`#link_template:` legend lines and truncates long attachments with a re-fetch receipt. See `docs/agent-presets.md`.

Agent allowlist presets: `docs/agent-presets.md`.

Documented solutions: `docs/solutions/`, past problems solved in this repo (bugs, deploy and workflow issues), organized by category with YAML frontmatter (`module`, `tags`, `problem_type`); relevant when debugging or changing documented areas. Shared domain vocabulary: `CONCEPTS.md`.

Plug deployment uses `SLACK_MCP_ENABLED_TOOLS` as an allowlist. Adding a new tool to the server does **not** expose it in Cursor until the name is added to that env list and Plug is restarted.

Side-effecting tools require explicit env opt-in. Every gate below is enforced **at registration** (`shouldAddTool`, `pkg/server/server.go`), so a disabled tool never appears in `tools/list`, and all but `SLACK_MCP_FILES_LIST_TOOL` are re-checked in the handler:

| Env var | Gates | Accepted values |
|---------|-------|-----------------|
| `SLACK_MCP_ADD_MESSAGE_TOOL` | `conversations_add_message` | `true`/`1`, or a comma-separated channel allowlist (`!C123…` negates) |
| `SLACK_MCP_REACTION_TOOL` | `reactions_add`, `reactions_remove` | `true`/`1`, or a comma-separated channel allowlist (`!C123…` negates) |
| `SLACK_MCP_ATTACHMENT_TOOL` | `attachment_get_data` | `true`, `1`, or `yes` |
| `SLACK_MCP_MARK_TOOL` | `conversations_mark` | `true`, `1`, or `yes` |
| `SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL` | `conversations_join`, `conversations_leave` | `true`, `1`, or `yes` |
| `SLACK_MCP_USERGROUPS_WRITE_TOOL` | `usergroups_create`, `usergroups_update`, `usergroups_users_update` | `true`, `1`, or `yes` |
| `SLACK_MCP_FILES_LIST_TOOL` | `files_list` | `true`, `1`, or `yes` (registration gate only) |

The five boolean gates (`ATTACHMENT`, `MARK`, `CHANNEL_MEMBERSHIP`, `USERGROUPS_WRITE`, `FILES_LIST`) accept only `true`, `1`, or `yes`, matched case-insensitively and ignoring surrounding whitespace (`isTruthyEnv`, duplicated in `pkg/handler/conversations.go` and `pkg/server/server.go`). Any other value, **including `false`**, leaves the tool disabled. The two channel-allowlist gates (`ADD_MESSAGE`, `REACTION`) are not booleans: their value *is* the configuration, so any non-empty value enables them (`channelListGates` in `pkg/server/server.go`).

Gate vars and the allowlist interact: when `SLACK_MCP_ENABLED_TOOLS` is set, the allowlist alone decides registration: a gated tool named in it registers without its dedicated env var, and one absent from it stays unregistered even when the env var is set. When `SLACK_MCP_ENABLED_TOOLS` is unset, a gated tool registers only if its own env var is truthy, or non-empty for the two channel-allowlist gates. Matching is an **exact** per-entry comparison (`isToolInEnabledList`, `slices.Contains`), not a substring test.

- MCP resources (`slack://…/channels`, `slack://…/users`) always register after cache warm-up; they ignore `SLACK_MCP_ENABLED_TOOLS`.

## Code map (runtime spine)

```
cmd/slack-mcp-server/main.go   → flags, cache warmup goroutine, transport
pkg/server/server.go         → MCP tool registration, middleware
pkg/handler/                 → per-tool handlers (conversations.go is largest)
pkg/provider/api.go          → Slack auth, SWR cache, API client
pkg/text/                    → message/block-kit formatting
```

Cache warmup runs in the background for stdio; the server serves immediately. Cache-dependent tools register after warmup via `RegisterCacheDependentTools()` (channels_list, channels_me, unreads, activity). Write tools register at startup only; phase guards in `pkg/server/tool_phases.go` prevent duplicate registration.

Warm-up tries up to 3 times (30s apart), then keeps retrying in the background on a slow interval (5m) indefinitely. Startup logs a warning when browser session auth is degraded. `RegisterCacheDependentTools` is idempotent (`sync.Once`) and emits `tools/list_changed` when tools appear.

If users or channels cache warm-up fails after the 3 fast attempts, the server registers cache-dependent tools automatically once a slow retry succeeds. Restarting Plug's slack server is only needed to force an immediate retry.

## Upstream merge checklist

1. `git fetch origin && git rev-list --left-right --count HEAD...origin/master`
2. Merge `origin/master` on a backup branch first (`codex/pre-upstream-update-YYYYMMDD`)
3. Resolve conflicts preferring upstream for shared infra; preserve local tool/handler behavior
4. `make test` must pass (includes cache SWR and block-kit tests)
5. Rebuild `bin/slack-mcp-server`, restart Plug, smoke one read tool + one local-only tool
6. Update this file if tool names or env vars changed

## Conventions

- Unit tests: any name except `*Integration*` runs under `make test`
- Integration tests: `*Integration*` in name, run via `make test-integration`
- Error handling: zap structured logging; avoid `logger.Fatal` on background cache paths
- Tool params are not logged at Info by default; set `SLACK_MCP_LOG_PARAMS=debug` to log full params at Info (may include message text). The same gate covers handler-level debug logs, not just the HTTP middleware.
- New handlers log entry/exit via `logToolCall` / `logResourceCall` (`pkg/handler/logging.go`) rather than passing `request.Params` to the logger directly. That helper is what honors `SLACK_MCP_LOG_PARAMS`

## Local-only working tree notes

- `bin/` is the Plug target binary (may differ from `make build` output in `./build/`)
- Do not commit secrets, cache JSON files, or `.env`

## Learned user preferences

- Do not propose upstream PRs or contributions. This is a personal/local fork only
- Prefer plain, simple English when summarizing updates (release-notes style)
- After server changes, verify live behavior through Plug-exposed Slack tools, not only `make test`
- When finishing work, merge to `master` locally and delete temporary branches rather than opening upstream PRs

## Learned workspace facts

- Source (`master`), built binary (`bin/slack-mcp-server`), and Plug runtime are three layers that can drift independently, so verify all three after code changes
- Plug daemon (`plug serve --daemon`) spawns the server; Cursor connects via `~/.cursor/mcp.json` → `plug connect`, not a direct binary entry
- `SLACK_MCP_ENABLED_TOOLS` in Plug may expose fewer tools than the server registers (allowlist vs 31 registered)
- Upstream v1.3.0 is fully merged (0 behind `origin/master`); ongoing work lands as commits ahead on `master`
- Local fork intentionally keeps non-blocking stdio startup and browser-auth degradation, while upstream blocks stdio until cache warm
- Restart Plug's `slack` server after rebuilding `bin/slack-mcp-server` so Cursor picks up the new binary (`make deploy-local` does disable/sleep/enable; `plug reload` alone can leave the old process)
