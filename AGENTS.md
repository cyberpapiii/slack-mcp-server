# Agent guide: local slack-mcp-server fork

This repository is a **local fork** of [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server) with about 165 fork-authored commits of MCP and agent-oriented improvements on top of upstream v1.3.0. It is **not** the npm release path. Production use on this machine goes through **Plug** → `bin/slack-mcp-server`.

## Build and verify

```bash
make deps          # go mod download
make lint          # go vet + gofmt check + `go mod tidy -diff` (read-only)
make test          # all tests except *Integration*, with -race (CI gate)
make test-integration  # needs Slack/ngrok secrets
make build         # outputs ./build/slack-mcp-server (read-only: no tidy/format)
make prepare       # go mod tidy + go fmt, the only targets that rewrite files
make deploy-local  # build bin/slack-mcp-server + bin/slack-mcp-auth, codesign, plug server disable/enable slack
go build -o bin/slack-mcp-server ./cmd/slack-mcp-server  # manual equivalent
go build -o build/slack-mcp-auth ./cmd/slack-mcp-auth      # OAuth helper: manifest / login / status / logout (docs/01)
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

`SLACK_MCP_SERVER_CA_TOOLKIT` is removed (embedded CA expired). Setting it fatals; use `SLACK_MCP_SERVER_CA` with a current HTTP Toolkit CA PEM instead.

## Tool surface

Canonical tool names: `capability.Names()` (`pkg/capability/catalog.go`), the one table every registered tool is built from (title, MCP hints, preset membership, registration phase, OAuth scopes). There are **68** tools. The README "Tools" section lists them by family; `scripts/toolslist-snapshot.sh [preset] [out.json]` dumps the live `tools/list` for a preset. This fork is self-contained and does not use Slack's official MCP.

Browser-session tools (`capability.BrowserNames()`, registered only when xoxc/xoxd is configured): `activity_mark_read`, `activity_unreads`, `conversations_unreads`, `drafts_*`, `saved_*`. Cache-dependent tools (`CacheReady`, registered after warm-up): `channels_list`, `channels_me`, `channels_starred`, `conversations_unreads`, `activity_unreads`, `activity_mark_read`.

`conversations_get_message` fetches a single message by channel and timestamp, the recovery path for an attachment-truncation receipt.

List tools return CSV text only (no structured content). Legend comment lines precede the header: `#channels:` (ID to `#name`/`@user`), `#users:`, `#link_template:`, `#next_cursor:` (only when another page exists), `#partial:`. The `Channel` column is the bare ID. `activity_unreads` and `saved_list` append a companion table after `#activity_items:` / `#saved_items:`. Message tools take a per-call `detail` parameter (`standard`/`full`); `SLACK_MCP_COMPACT_OUTPUT` only sets the server-wide default when `detail` is omitted. Standard mode truncates long attachments with a re-fetch receipt. Single-record and mutation tools return structured content with a JSON text fallback. See `docs/agent-presets.md`.

Agent allowlist presets: `docs/agent-presets.md`.

Documented solutions: `docs/solutions/`, past problems solved in this repo (bugs, deploy and workflow issues), organized by category with YAML frontmatter (`module`, `tags`, `problem_type`); relevant when debugging or changing documented areas. Shared domain vocabulary: `CONCEPTS.md`.

Plug deployment uses `SLACK_MCP_ENABLED_TOOLS` as an allowlist. Adding a new tool to the server does **not** expose it in Cursor until the name is added to that env list and Plug is restarted.

Tool registration has one switch: a tool registers iff its name is in the resolved enabled-tools list (`addEnabledTool` / `addCacheDependentTool`, `pkg/server/server.go`; each panics at startup if a tool is registered in the wrong phase). That list comes from `SLACK_MCP_ENABLED_TOOLS` / `--enabled-tools` when set, otherwise from `SLACK_MCP_TOOL_PRESET` (`daily-power`, the read-only default, or `legacy-full`). Matching is an exact per-entry comparison. A tool outside the list never appears in `tools/list`; there are no per-tool boolean gate variables. Any leftover `SLACK_MCP_*_TOOL` variable other than the three below is ignored and logged as a warning at startup.

Three write families additionally take a channel allow/block list, checked in the handler on every call:

| Env var | Scopes | Value |
|---------|--------|-------|
| `SLACK_MCP_ADD_MESSAGE_TOOL` | `conversations_add_message`, `messages_schedule`, `messages_update`, `messages_delete` | empty or `true`/`1`/`yes` = every channel; `C1,C2` = only those; `!C1,!C2` = all except those |
| `SLACK_MCP_REACTION_TOOL` | `reactions_add`, `reactions_remove` | same shape |
| `SLACK_MCP_CHANNEL_MANAGEMENT_TOOL` | `channels_rename`, `channels_set_topic`, `channels_set_purpose`, `channels_archive` | same shape |

A denied channel returns a `permission_denied` tool error that names the variable. Setting one of these to `false` does not disable the tool; it is read as a one-entry channel list and blocks every channel (startup warns). To turn a tool off, drop it from the enabled-tools list.

- MCP resources (`slack://…/channels`, `slack://…/users`) always register after cache warm-up; they ignore `SLACK_MCP_ENABLED_TOOLS`.

## Code map (runtime spine)

```
cmd/slack-mcp-server/main.go   → flags, cache warmup goroutine, transport
cmd/slack-mcp-auth/            → OAuth login helper; writes the rotating credential to macOS Keychain
pkg/capability/catalog.go    → the tool table (names, hints, presets, phases, scopes)
pkg/server/                  → tool registration (server.go, core_tools.go, daily_power_tools.go, custom_power_tools.go), middleware
pkg/handler/                 → per-tool handlers; csv_result.go holds the CSV contract
pkg/provider/                → Slack auth, SWR cache, API client, browser session
pkg/text/                    → message/block-kit formatting
```

Cache warmup runs in the background for every transport; the server serves immediately. Cache-dependent tools register after warmup via `RegisterCacheDependentTools()`. Every other tool registers at startup; `addEnabledTool` refuses a `CacheReady` tool and `addCacheDependentTool` refuses anything else, so a tool cannot be registered in both phases.

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
- `SLACK_MCP_ENABLED_TOOLS` in Plug is an allowlist; the server registers only what it names (or the preset's list)
- Upstream v1.3.0 is fully merged (0 behind `origin/master`); ongoing work lands as commits ahead on `master`
- Local fork intentionally keeps non-blocking stdio startup and browser-auth degradation, while upstream blocks stdio until cache warm
- Restart Plug's `slack` server after rebuilding `bin/slack-mcp-server` so Cursor picks up the new binary (`make deploy-local` does disable/sleep/enable; `plug reload` alone can leave the old process)
