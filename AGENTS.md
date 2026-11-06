# Agent guide: local slack-mcp-server fork

This repository is a **local fork** of [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server) with about 165 fork-authored commits of MCP and agent-oriented improvements on top of upstream v1.3.0. It is **not** the npm release path. Production use on this machine goes through **Plug** → `bin/slack-mcp-server`.

## Build and verify

```bash
make deps          # go mod download
make lint          # go vet + gofmt check + `go mod tidy -diff` (read-only)
make test          # unit tests with -race (CI gate); live tests are behind the integration build tag
make test-integration  # needs Slack/ngrok secrets
make build         # outputs ./build/slack-mcp-server (read-only: no tidy/format)
make prepare       # go mod tidy + go fmt, the only targets that rewrite files
make deploy-local  # build bin/slack-mcp-server + bin/slack-mcp-auth, codesign, stop the server so Plug restarts it
go build -o bin/slack-mcp-server ./cmd/slack-mcp-server  # manual equivalent
go build -o build/slack-mcp-auth ./cmd/slack-mcp-auth      # OAuth helper: manifest / login / status / logout (docs/01)
```

`scripts/perf.sh [since] [until]` is the read-only field harness. It reads Plug's daily logs and prints per-tool latency percentiles (n, p50, p75, p95, max), cold-start times, and failed calls for the slack server. Pass a deploy date as `since` or `until` to compare before and after.

`make lint && make test` is the whole verification gate. Neither mutates the
working tree; `make build` no longer runs `tidy`/`format` either, so it is safe
mid-review. Run `make prepare` explicitly when you want the tree reformatted.

After changing Go code, run `make deploy-local` (or rebuild `bin/slack-mcp-server` and restart Plug's `slack` server) so Cursor picks up the new binary.

The target restarts the server by stopping its process and waiting up to 120s for a new PID, because `plug server off` is refused while `[http.slack_events]` depends on the `slack` upstream. Plug reconnects a stopped stdio server by itself (47s measured), so expect tools to be unavailable for up to a minute.

Run the deploy while someone is at the Mac (or screen-sharing into it). The Keychain items' partition list pins the exact build (`cdhash:`) last allowed, and the local signing certificate has no Team ID, so every rebuild makes macOS show an access prompt for the OAuth (and browser) credential. `make deploy-local` collects that approval before it touches anything live: it builds and signs into `build/deploy-stage/`, runs that binary with `--check-keychain` (reads the items named by `SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT` / `SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT`, taken from Plug's `[servers.slack.env]`, and exits), and waits there with no deadline for **Always Allow**. Only after the check passes does it `rm` + `cp` the same file into `bin/` and restart Plug, so the live server starts already trusted. If the check fails, `bin/` and Plug are left unchanged. Removing the prompt entirely needs a signing identity with a Team ID; widening the Keychain ACL instead would let other programs read the token. With `SLACK_MCP_LOG_FILE` set, a stalled read in the live server logs `Still waiting on macOS Keychain` after 3s (`keychain.WarnWhileWaiting`, `pkg/keychain/wait.go`).

## Runtime layout

| Layer | Location |
|-------|----------|
| Source | This repo (`master`, tracks `origin/master` + local commits) |
| Binary | `bin/slack-mcp-server` (Plug config target) |
| MCP multiplexer | Plug: `~/Library/Application Support/plug/config.toml` → `[servers.slack]` |
| Cursor entry | `~/.cursor/mcp.json` → `plug connect` |

Managed OAuth lives in the Keychain item `SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT` names, with the rotating refresh token. Lists and DND tools and full canvas reads need it. If the login cannot load (`invalid_refresh_token`, a denied Keychain prompt), the server starts on the browser session instead of exiting, logs at Error, and `slack_auth_status` reports `oauth_credential.state: unavailable` with the fix: `slack-mcp-auth login --client-id <id> --team <T> --user <U> --replace --preset legacy-full`, then restart. Do not remove the variable to get the server back up; that hides 9 tools and reports nothing wrong. It stays fatal when there is no browser session to fall back on.

Auth tokens (`SLACK_MCP_XOXC_TOKEN`, `SLACK_MCP_XOXD_TOKEN`, etc.) live in the environment Plug resolves. Never commit them.

The `sse` and `http` transports refuse to start unless `SLACK_MCP_API_KEY` is set (deprecated fallback: `SLACK_MCP_SSE_API_KEY`), or `SLACK_MCP_ALLOW_UNAUTHENTICATED` is set to exactly `true`. `1`/`yes` are rejected. `stdio` (the Plug path) is unaffected. See `pkg/server/auth/sse_auth.go`.

`SLACK_MCP_SERVER_CA_TOOLKIT` is removed (embedded CA expired). Setting it fatals; use `SLACK_MCP_SERVER_CA` with a current HTTP Toolkit CA PEM instead.

## Tool surface

Canonical tool names: `capability.Names()` (`pkg/capability/catalog.go`), the one table every registered tool is built from (title, MCP hints, preset membership, credential requirement `Requires`, registration phase `CacheReady`, channel allowlist variable, OAuth scopes). Each handler family declares its tools' schemas and handlers next to the handlers (`Tools() []handler.ToolDef`); `pkg/server/handlers.go` builds the families and `pkg/server/server.go` registers what is served. There are **71** tools. The README "Tools" section lists them by family; `scripts/toolslist-snapshot.sh [preset] [out.json]` dumps the live `tools/list` for a preset. This fork is self-contained and does not use Slack's official MCP.

Browser-session tools (`capability.BrowserNames()`, rows with `Requires: BrowserSession`; registered only when xoxc/xoxd is configured, and wrapped in one session check, `pkg/server/browser_gate.go`, that answers `browser_session_required` while the session is degraded): `activity_mark_read`, `activity_unreads`, `conversations_unreads`, `drafts_*`, `saved_*`. Cache-dependent tools (`CacheReady`, registered after warm-up): `channels_list`, `channels_me`, `channels_starred`, `conversations_unreads`, `activity_unreads`, `activity_mark_read`.

`conversations_get_message` fetches a single message by channel and timestamp, the recovery path for an attachment-truncation receipt.

`AttachmentIDs` renders each file as `FileID (name, kind, size)` via `attachmentRef` (`pkg/handler/conversations_files.go`). The kind mirrors what `attachment_get_data` returns: `image` and `text` are readable, anything else comes back as base64. `files_upload` with a `channel_id` posts a message, so it honors `SLACK_MCP_ADD_MESSAGE_TOOL` like the other message lifecycle tools.

`ApiProvider.WebAPI()` returns a `*provider.WebClient` (`pkg/provider/web_client.go`), not a `*slack.Client`. Managed OAuth rotation replaces the underlying client rather than mutating it, so anything holding the raw client holds the access token that client was built with. `WebClient` resolves the current client on every method call, which makes it safe to store for the life of the process. It used to return the raw client, and four long-lived consumers captured it at startup: the message/file, scheduled and channel-mutation providers, plus the usergroups handler. Those tools returned `token_expired` after the captured token expired, while every per-request read tool kept working. `TestUnit*FollowsClientRotation` (`pkg/provider/web_client_test.go`) swaps the client underneath a constructed consumer and asserts the next call reaches the replacement; the four cases fail against the pre-fix code. Adding a method to `WebClient` is how a new Slack call joins this surface. Do not reintroduce an accessor that hands out the raw `*slack.Client`.

`canvases_read` returns the whole canvas through `canvases.getContent` (`GetCanvasContentContext`, `pkg/provider/canvas_drafts_search.go`), Markdown by default, `content_format` `html` or `none` otherwise. slack-go does not wrap that method. Slack answers `not_allowed_token_type` to a browser-session token, so the read needs user OAuth; without it the tool still answers, with the files.info preview and a `limitation` saying why.

Slack replaced Later with To-dos in October 2026 and built it on the same private `saved.*` API: `saved.list` takes `filter=todo`, and an item's `todo_state` (`to_do`, `saved`, `completed`) says which list it is on. `saved_list` shows it; `saved_add`, `saved_update` and `saved_delete` write it. A custom to-do is item type `reminder`, with an `Sa...` ID and a rich-text `description`, and has no message to fetch. Three behaviors were found by probing live, not from documentation: moving an item to Saved clears its due date, `mark=uncompleted` without a `todo_state` files the item under Saved (the handler sends `to_do`), and **`saved.markCompleted` with no arguments completes every open to-do**, so do not call it bare. `date_due=0` clears a reminder, so the form carries it as a string; an `omitempty` integer would silently drop it.

`files_download` saves a file to disk instead of returning its bytes. It is off unless `SLACK_MCP_DOWNLOAD_DIR` names an absolute directory; without it the handler returns a `permission_denied` error naming the variable. The caller passes only `file_id`. The server derives the destination as `<root>/<FileID>-<sanitized name>` (`downloadPath`, `pkg/handler/conversations_files.go`), so there is no caller-supplied path to traverse out of and a repeat call is idempotent: a file already on disk at its full size returns `outcome: "already_present"` without re-fetching. Downloads are capped at 100MB, far above `attachment_get_data`'s 5MB, because these bytes never enter the caller's context.

`conversations_add_message` and `conversations_draft_message` cannot attach files. Passing a file-shaped parameter to either one returns an `invalid_arguments` error naming `files_upload` rather than posting the message without the file, via `signpostFileParam` (`pkg/handler/params.go`). The match is any argument key containing `file`, `attach`, `upload`, `image`, `photo`, `screenshot`, `media`, or `document`. The tool descriptions carry the same pointer. Both surfaces exist because a client that loads tool schemas on demand holds no tool list to search: the name has to appear in something already in its context, or the caller concludes the capability is missing. `messages_schedule` is deliberately excluded, since Slack cannot schedule a file upload and pointing there would be a false lead.

List tools return CSV text only (no structured content). Legend comment lines precede the header: `#channels:` (ID to `#name`/`@user`), `#users:`, `#link_template:`, `#attachments:` (only when a row carries a file; not suppressed on short responses), `#next_cursor:` (only when another page exists), `#partial:`. The `Channel` column is the bare ID. `activity_unreads` and `saved_list` append a companion table after `#activity_items:` / `#saved_items:`. Message tools take a per-call `detail` parameter (`standard`/`full`); `SLACK_MCP_COMPACT_OUTPUT` only sets the server-wide default when `detail` is omitted. Standard mode truncates long attachments with a re-fetch receipt. Single-record and mutation tools return structured content with a JSON text fallback. See `docs/agent-presets.md`.

Agent allowlist presets: `docs/agent-presets.md`.

Documented solutions: `docs/solutions/`, past problems solved in this repo (bugs, deploy and workflow issues), organized by category with YAML frontmatter (`module`, `tags`, `problem_type`); relevant when debugging or changing documented areas. Shared domain vocabulary: `CONCEPTS.md`.

Plug deployment uses `SLACK_MCP_ENABLED_TOOLS` as an allowlist. Adding a new tool to the server does **not** expose it in Cursor until the name is added to that env list and Plug is restarted.

Tool registration has one rule (`handler.Availability`, `pkg/handler/availability.go`, which `slack_auth_status` also reports from): a tool is served iff its name is in the resolved enabled-tools list and the running credentials satisfy its `Requires` (`ApiProvider.Satisfies`); `CacheReady` tools wait for warm-up. The list comes from `SLACK_MCP_ENABLED_TOOLS` / `--enabled-tools` when set, otherwise from `SLACK_MCP_TOOL_PRESET` (`capability.DefaultPreset` `daily-power`, read-only, or `legacy-full`). Matching is an exact per-entry comparison. A tool outside the list never appears in `tools/list`; there are no per-tool boolean gate variables. Any leftover `SLACK_MCP_*_TOOL` variable other than the three below is ignored and logged as a warning at startup.

Three write families additionally take a channel allow/block list (the catalog's `ChannelAllowlist` column), parsed once at startup into `handler.Settings` and checked in the handler on the resolved channel ID (`Settings.requireChannel`); `TestUnitEveryAllowlistedToolRefusesADeniedChannel` (`pkg/handler/allowlist_test.go`) fails if a listed tool skips the check:

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
pkg/server/                  → registration (server.go), handler wiring (handlers.go), browser gate, middleware; server/auth: API-key auth for sse/http
pkg/handler/                 → per-family handlers + their tool specs; messages.go is the one message pipeline, csv_result.go the CSV contract, errors.go the one error mapping (TypeError), settings.go startup config, approval.go the prepare/execute gate
pkg/provider/                → credential mode, Slack clients (WebClient, edge), SWR cache + lifecycle, lookup API (lookup.go), browser session
pkg/slackcreds/              → the credential type (token + session cookies)
pkg/oauthcred/               → rotating user OAuth credential: record, Keychain store, refresh lock, refresher, PKCE login, identity check
pkg/keychain/                → macOS Keychain access (cgo) and the slow-prompt warning; shared by the OAuth and browser stores
pkg/cachedir/                → per-user cache directory shared by the caches and the OAuth lock
pkg/slackhttp/               → the one hand-built Slack POST: 429/status/body cap/ok:false decode
pkg/approval/                → prepare/execute token store
pkg/limiter/                 → rate buckets, the per-method pacing table and the one retry loop
pkg/text/                    → message/block-kit formatting
```

Uncached message authors (Slack Connect guests, members added since the last sync) are resolved in one batched `users.info` call per page: `userResolver.prefetch` (`pkg/handler/messages.go`) collects the ids the page will render and hands them to `ApiProvider.PatchUsers`, which chunks at 50 ids per call. `resolve` still falls back to a single-user `PatchUser` for anything the batch did not return, so a failed or short batch costs no more than the per-user path did. Both patch paths splice the sorted-by-id user search index rather than folding and re-sorting every cached user; `BenchmarkPatchUser` and `TestUnitPatchedSearchIndexMatchesFullRebuild` (`pkg/provider/cache_users_patch_test.go`) guard the cost and the equivalence.

Slack credentials live in `pkg/slackcreds`, not in `github.com/rusq/slackdump/v3/auth`. Only three things were ever used from that package (`ValueAuth`, `NewValueAuth`, the `Provider` interface) plus one user-agent string from `github.com/rusq/slackauth`, and both packages link a headless-browser stack (go-rod, playwright, bubbletea, charmbracelet) that this server never runs. `slackcreds.New` reproduces the upstream cookie shape exactly (`d` and `d-s`, `.slack.com`, path `/`, Secure, 10-year expiry, percent-escaped when the value is not RFC 3986 unreserved); `pkg/slackcreds/creds_test.go` pins it. The user agent Slack sees is `transport`'s (`SLACK_MCP_USER_AGENT` or its default), set on every request. `edge.NewWithInfo` takes the HTTP client as a required argument: the one from `transport.ProvideHTTPClient`, which carries the proxy and TLS settings.

Hand-built Slack calls (edge, `canvases.getContent`, `assistant.search.context`, `users.profile.set`, Lists) post through `pkg/slackhttp`: one 429 mapping to `*slack.RateLimitedError`, one non-2xx `slack.StatusCodeError`, one body cap, and one `ok:false` decode into `slack.SlackErrorResponse`. It never retries. Pacing and retries belong to the clients: `WebClient` and the edge client run each call through `limiter.Paced` with the per-method table `limiter.SlackPacing()` and `limiter.Retries`, so handlers just call methods.

Cache warmup runs in the background for every transport; the server serves immediately. `ApiProvider.StartCacheLifecycle` (`pkg/provider/cache_lifecycle.go`) owns warm-up and revalidation and stops on `Close`. Cache-dependent tools register after warmup via `RegisterCacheDependentTools()`. Other tools do not wait for the cache: only a lookup by name (`#name`, `@name`) needs it, and on a cold cache it returns `cache_warming` (retryable); IDs work at once.

Warm-up tries up to 3 times (30s apart), then keeps retrying in the background on a slow interval (5m) indefinitely. Startup logs a warning when browser session auth is degraded. A degraded browser session attached to OAuth is retried every 5 minutes (`runBrowserReprobe`, `pkg/provider/browser_session.go`): the credential is reloaded from the Keychain (or the env xoxc/xoxd) and checked with auth.test, and the session comes back without a restart once it works for the same workspace user. A browser Keychain item that fails to load at startup now registers the browser tools in the degraded state, so they name the failure and recover with the re-probe. `RegisterCacheDependentTools` is idempotent (`sync.Once`) and emits `tools/list_changed` when tools appear.

Once warm, the same lifecycle checks the users and channels cache files hourly and refetches one whose mtime is past `SLACK_MCP_CACHE_TTL` (24h default), users first because DM names map from them. Before this, a process refetched them only at startup.

If users or channels cache warm-up fails after the 3 fast attempts, the server registers cache-dependent tools automatically once a slow retry succeeds. Restarting Plug's slack server is only needed to force an immediate retry.

## Upstream merge checklist

1. `git fetch origin && git rev-list --left-right --count HEAD...origin/master`
2. Merge `origin/master` on a backup branch first (`codex/pre-upstream-update-YYYYMMDD`)
3. Resolve conflicts preferring upstream for shared infra; preserve local tool/handler behavior. Upstream still imports `github.com/rusq/slackdump/v3/auth`; this fork replaced it with `pkg/slackcreds`, so re-map `auth.Provider`/`auth.ValueAuth`/`auth.NewValueAuth` in anything a merge brings in rather than re-adding the dependency
4. `make test` must pass (includes cache SWR and block-kit tests)
5. Rebuild `bin/slack-mcp-server`, restart Plug, smoke one read tool + one local-only tool
6. Update this file if tool names or env vars changed

## Conventions

- Unit tests: every `_test.go` without a build tag runs under `make test`. It does not pass `-v`: a passing run is ~15 lines, and a failing package still prints its full output. Run `go test -v ./...` when you want test names
- Tests must not draw on the process-wide `limiter` buckets (`limiter.Tier2` and friends): build clients with a nil `limiter.Pacing`, which paces nothing. A test that paces through the real Tier2 budget blocks for a 3s token refill per call
- Block-kit rendering: `pkg/text/blocks_test.go` pins the rich-text quote and preformatted output. slack-go has changed those types' shape once already (they used to alias and embed `RichTextSection`), and the rendering is what every message tool returns
- Live Slack tests: `//go:build integration` files (`pkg/handler/integration_test.go`, `pkg/test/util`), run via `make test-integration`; `make lint` vets them so they cannot rot
- Error handling: a handler returns `(nil, err)`; the server's error middleware maps it once through `handler.TypeError` (`pkg/handler/errors.go`), using the catalog's `ReadOnly` flag to decide whether a timeout or 5xx means `outcome_unknown`. Providers return slack-go's errors (`slack.SlackErrorResponse`, `*slack.RateLimitedError`, `slack.StatusCodeError`) wrapped with the operation; Slack codes map through one table there. Validation failures use `invalidArguments`. Destructive tools go through `approvalGate` (`pkg/handler/approval.go`). zap structured logging; avoid `logger.Fatal` on background cache paths
- Plug sends the server's stderr to `/dev/null`. Set `SLACK_MCP_LOG_FILE` (absolute path) in Plug's `[servers.slack.env]` to see the server's own logs; the live setup writes `~/Library/Logs/slack-mcp-server/server.log`. Each tool call logs one Info line, "Request finished", with tool, duration, error flag and, for a failed call, `error_code`. Handlers log a failure they return to the caller at Debug: the error-recovery middleware already logs it once (Warn for an untyped error, Debug for a `ToolError` or a cancelled call). Keep `logger.Error` for real server faults such as a CSV marshal or a local file write
- Tool params are not logged at Info by default; set `SLACK_MCP_LOG_PARAMS=debug` to log full params at Info (may include message text). The same gate covers handler-level debug logs, not just the HTTP middleware.
- Handlers do not log each call: the logger middleware (`pkg/server/server.go`) writes the one line per call and adds params only when `SLACK_MCP_LOG_PARAMS=debug`. Do not pass `request.Params` to a logger elsewhere

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
- Restart the `slack` server after rebuilding `bin/slack-mcp-server` so Cursor picks up the new binary (`make deploy-local` stops the process and waits for Plug to start a new one; `plug reload` alone can leave the old process)
