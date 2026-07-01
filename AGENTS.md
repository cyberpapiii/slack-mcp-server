# Agent guide — local slack-mcp-server fork

This repository is a **local fork** of [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server) with ~41 commits of MCP and agent-oriented improvements on top of upstream v1.3.0. It is **not** the npm release path. Production use on this machine goes through **Plug** → `bin/slack-mcp-server`.

## Build and verify

```bash
make deps          # go mod download
make test          # all tests except *Integration* (CI gate)
make test-integration  # needs Slack/ngrok secrets
make build         # outputs ./build/slack-mcp-server
make deploy-local  # build bin/slack-mcp-server + plug reload
go build -o bin/slack-mcp-server ./cmd/slack-mcp-server  # manual equivalent
```

After changing Go code, run `make deploy-local` (or rebuild `bin/slack-mcp-server` and restart Plug's `slack` server) so Cursor picks up the new binary.

## Runtime layout

| Layer | Location |
|-------|----------|
| Source | This repo (`master`, tracks `origin/master` + local commits) |
| Binary | `bin/slack-mcp-server` (Plug config target) |
| MCP multiplexer | Plug — `~/Library/Application Support/plug/config.toml` → `[servers.slack]` |
| Cursor entry | `~/.cursor/mcp.json` → `plug connect` |

Auth tokens (`SLACK_MCP_XOXC_TOKEN`, `SLACK_MCP_XOXD_TOKEN`, etc.) live in the environment Plug resolves — never commit them.

## Tool surface

Canonical tool names: `ValidToolNames` in `pkg/server/server.go`. There are **30** tools; upstream README documents fewer.

Local-only or fork-extended tools include:

- `slack_auth_status` — cache and browser-session health (call before activity/saved tools)
- `conversations_open`, `conversations_draft_message`, `files_list`
- `channels_starred`, `channels_me`, `conversations_unreads`
- `activity_unreads`, `activity_mark_read` (browser session / xoxc+xoxd)
- `saved_list`, `saved_update`, `saved_clear_completed`
- `reactions_get`, compact CSV by default (keeps MsgID/ThreadTs for follow-up actions). Message tools take a per-call `detail` parameter (`standard`/`full`); `SLACK_MCP_COMPACT_OUTPUT` only sets the server-wide default when `detail` is omitted. Standard mode may prepend `#users:`/`#link_template:` legend lines and truncates long attachments with a re-fetch receipt — see `docs/agent-presets.md`.

Agent allowlist presets: `docs/agent-presets.md`.

Plug deployment uses `SLACK_MCP_ENABLED_TOOLS` as an allowlist. Adding a new tool to the server does **not** expose it in Cursor until the name is added to that env list and Plug is restarted.

Write tools require explicit env opt-in:

- `SLACK_MCP_ADD_MESSAGE_TOOL` — posting (channel allowlist or `true`)
- `SLACK_MCP_REACTION_TOOL` — reactions add/remove
- `SLACK_MCP_ATTACHMENT_TOOL` — attachment download

## Code map (runtime spine)

```
cmd/slack-mcp-server/main.go   → flags, cache warmup goroutine, transport
pkg/server/server.go         → MCP tool registration, middleware
pkg/handler/                 → per-tool handlers (conversations.go is largest)
pkg/provider/api.go          → Slack auth, SWR cache, API client
pkg/text/                    → message/block-kit formatting
```

Cache warmup runs in the background for stdio; the server serves immediately. Cache-dependent tools register after warmup via `RegisterCacheDependentTools()` (channels_list, channels_me, unreads, activity). Write tools register at startup only — phase guards in `pkg/server/tool_phases.go` prevent duplicate registration.

Warm-up retries up to 3 times (30s apart) before giving up. Startup logs a warning when browser session auth is degraded. `RegisterCacheDependentTools` is idempotent (`sync.Once`) and emits `tools/list_changed` when tools appear.

If users or channels cache warm-up fails after retries, the process stays alive but cache-dependent tools never register until restart. Restart Plug's slack server (or the plug daemon) after fixing auth or network issues.

## Upstream merge checklist

1. `git fetch origin && git rev-list --left-right --count HEAD...origin/master`
2. Merge `origin/master` on a backup branch first (`codex/pre-upstream-update-YYYYMMDD`)
3. Resolve conflicts preferring upstream for shared infra; preserve local tool/handler behavior
4. `make test` — must pass (includes cache SWR and block-kit tests)
5. Rebuild `bin/slack-mcp-server`, restart Plug, smoke one read tool + one local-only tool
6. Update this file if tool names or env vars changed

## Conventions

- Unit tests: any name except `*Integration*` runs under `make test`
- Integration tests: `*Integration*` in name, run via `make test-integration`
- Error handling: zap structured logging; avoid `logger.Fatal` on background cache paths
- Tool params are not logged at Info by default; set `SLACK_MCP_LOG_PARAMS=debug` to log full params at Info (may include message text)

## Local-only working tree notes

- `bin/` is the Plug target binary (may differ from `make build` output in `./build/`)
- Do not commit secrets, cache JSON files, or `.env`

## Learned User Preferences

- Do not propose upstream PRs or contributions — this is a personal/local fork only
- Prefer plain, simple English when summarizing updates (release-notes style)
- After server changes, verify live behavior through Plug-exposed Slack tools, not only `make test`
- When finishing work, merge to `master` locally and delete temporary branches rather than opening upstream PRs

## Learned Workspace Facts

- Source (`master`), built binary (`bin/slack-mcp-server`), and Plug runtime are three layers that can drift independently — verify all three after code changes
- Plug daemon (`plug serve --daemon`) spawns the server; Cursor connects via `~/.cursor/mcp.json` → `plug connect`, not a direct binary entry
- `SLACK_MCP_ENABLED_TOOLS` in Plug may expose fewer tools than the server registers (allowlist vs 29 registered)
- Upstream v1.3.0 is fully merged (0 behind `origin/master`); ongoing work lands as commits ahead on `master`
- Local fork intentionally keeps non-blocking stdio startup and browser-auth degradation — upstream blocks stdio until cache warm
- Restart Plug's `slack` server or run `plug reload` after rebuilding `bin/slack-mcp-server` so Cursor picks up the new binary
