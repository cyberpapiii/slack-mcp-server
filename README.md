# Slack MCP Server

Model Context Protocol (MCP) server for Slack workspaces. A fork of [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server) that is self-contained (it does not use Slack's official MCP), serves 69 tools, and is tuned for agents: compact CSV output, preset allowlists, and per-channel write gates. Stdio, SSE and HTTP transports; OAuth (`xoxp`), bot (`xoxb`), or browser-session (`xoxc`/`xoxd`) auth; Enterprise Grid workspaces.

What the server does:
- **Stealth and OAuth modes**: browser session tokens need no app install; OAuth tokens can be stored in macOS Keychain and rotate automatically ([auth setup](docs/01-authentication-setup.md)).
- **Channels, threads, DMs, group DMs**: address conversations by ID or by `#name` / `@user`.
- **Smart history**: fetch by date window (`1d`, `7d`, `1m`) or message count, with cursors.
- **Unreads and Activity**: unread messages across channels with DM-first priority and `@mention` filtering; Slack's Activity feed; Saved items. Browser session only.
- **Search**: message search with date, user, channel and thread filters; semantic search where Slack has enabled it.
- **Writes behind gates**: posting, reactions, channel maintenance, scheduled messages, drafts, canvases, Slack Lists, user groups, DND, profile and status. Nothing registers unless it is in the enabled-tools list, and three write families also honour a per-channel allow/block list.
- **Embedded user information**: messages carry who said what; a `#users:` legend maps IDs to names.
- **Caching**: users and channels are cached on disk, served stale-while-revalidate, refreshed in the background.
- **Proxy and custom TLS** for enterprise networks.

## Tools

Which tools register is decided by one list: `SLACK_MCP_ENABLED_TOOLS` (or `--enabled-tools`), else the preset in `SLACK_MCP_TOOL_PRESET`. Presets:

| Preset | Tools | What it is |
|--------|-------|------------|
| `daily-power` (default) | 17 | Read-only: unreads, Activity, Saved, drafts, canvases, lists, DND, emoji, user groups, profiles, semantic search, `slack_auth_status`. |
| `legacy-full` | 69 | Everything, including every write tool. |

The 69 tools by family:

| Family | Tools |
|--------|-------|
| Messages | `conversations_history`, `conversations_replies`, `conversations_get_message`, `conversations_search_messages`, `conversations_add_message`, `conversations_draft_message`, `messages_schedule`, `messages_update`, `messages_delete`, `scheduled_messages_list`, `scheduled_message_cancel` |
| Unreads and Activity (browser session) | `conversations_unreads`, `activity_unreads`, `activity_mark_read`, `saved_list`, `saved_update`, `saved_clear_completed` |
| Drafts (browser session) | `drafts_list`, `drafts_get`, `drafts_create`, `drafts_update`, `drafts_delete` |
| Conversations | `conversations_open`, `conversations_mark`, `conversations_join`, `conversations_leave` |
| Channels | `channels_list`, `channels_me`, `channels_starred`, `channels_members`, `channels_create`, `channels_invite`, `channels_rename`, `channels_set_topic`, `channels_set_purpose`, `channels_archive` |
| Reactions | `reactions_add`, `reactions_remove`, `reactions_get` |
| Files | `files_list`, `files_download`, `files_upload`, `attachment_get_data` |
| Users and groups | `users_search`, `users_get_profile`, `users_set_profile`, `users_set_status`, `usergroups_list`, `usergroups_mine`, `usergroups_join`, `usergroups_leave`, `usergroups_create`, `usergroups_update`, `usergroups_users_update` |
| Canvases, Lists, emoji, DND | `canvases_create`, `canvases_read`, `canvases_update`, `lists_create`, `lists_update`, `lists_items_list`, `lists_items_create`, `lists_items_update`, `lists_item_delete`, `emoji_list`, `dnd_get`, `dnd_set_snooze`, `dnd_end_snooze` |
| Search and diagnostics | `search_semantic`, `slack_auth_status` |

Parameter schemas, titles and hints come from the server itself; `scripts/toolslist-snapshot.sh [preset] [out.json]` builds the binary and dumps `tools/list` for a preset. The table that every tool is built from is `pkg/capability/catalog.go`.

Two Slack limitations are permanent: `canvases_read` returns metadata and preview text because the public API does not return full canvas content, and `search_semantic` works only when Slack has enabled the Real-time Search API for the app.

### Output format

List tools return CSV text. Comment lines before the header carry what does not fit a row:

```
#channels: C041QQ9FNAJ=#general, D0AGSQXLJHG=@john
#users: U03BMAR2R50=robdezendorf|Rob Dezendorf
#link_template: https://<workspace>.slack.com/archives/{Channel}/p{MsgID with "." removed}
#next_cursor: dXNlcjpVMDYxTkZUVDI=
User,Channel,Text,Time,MsgID,ThreadTs,Reactions,AttachmentIDs,Files
```

`Channel` holds the bare conversation ID; names live in the `#channels:` legend. `#next_cursor:` appears only when another page exists; pass it back as `cursor`. `#partial:` names a reason when a result was cut short. Tools that pair messages with their own items (`activity_unreads`, `saved_list`) append a second table after a `#activity_items:` / `#saved_items:` line. Message tools take `detail: standard` (default) or `detail: full` (every column, attachments untruncated); `SLACK_MCP_COMPACT_OUTPUT=false` makes `full` the server default. Details and preset recipes: [docs/agent-presets.md](docs/agent-presets.md).

## Resources

The Slack MCP Server exposes two directory resources with workspace metadata:

### 1. Channels directory: `slack://<workspace>/channels`

Fetches a CSV directory of all channels in the workspace, including public channels, private channels, DMs, and group DMs.

- **URI:** `slack://<workspace>/channels`
- **Format:** `text/csv`
- **Fields:**
  - `id`: Channel ID (e.g., `C1234567890`)
  - `name`: Channel name (e.g., `#general`, `@username_dm`)
  - `topic`: Channel topic (if any)
  - `purpose`: Channel purpose/description
  - `memberCount`: Number of members in the channel

### 2. Users directory: `slack://<workspace>/users`

Fetches a CSV directory of all users in the workspace.

- **URI:** `slack://<workspace>/users`
- **Format:** `text/csv`
- **Fields:**
  - `userID`: User ID (e.g., `U1234567890`)
  - `userName`: Slack username (e.g., `john`)
  - `realName`: User's real name (e.g., `John Doe`)

## Setup guide

- [Authentication Setup](docs/01-authentication-setup.md)
- [Installation](docs/02-installation.md)
- [Configuration and Usage](docs/03-configuration-and-usage.md)

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SLACK_MCP_XOXP_TOKEN` | | User OAuth token (`xoxp-...`). One of xoxp, xoxb, or xoxc+xoxd is required unless Keychain OAuth is configured. |
| `SLACK_MCP_XOXB_TOKEN` | | Bot token (`xoxb-...`). Invited channels only, no search. |
| `SLACK_MCP_XOXC_TOKEN` / `SLACK_MCP_XOXD_TOKEN` | | Browser session token and `d` cookie. Required for unreads, Activity, Saved and drafts; may be set alongside xoxp. |
| `SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT` | | macOS Keychain item holding a rotating OAuth credential written by `slack-mcp-auth login`. Replaces `SLACK_MCP_XOXP_TOKEN`. |
| `SLACK_MCP_OAUTH_CLIENT_ID` / `SLACK_MCP_OAUTH_CLIENT_SECRET` | | Slack app credentials used to refresh the Keychain credential. Secret is optional for PKCE public clients. |
| `SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT` | | macOS Keychain item holding xoxc/xoxd; an alternative to the two env vars. |
| `SLACK_MCP_ENABLED_TOOLS` | unset | Comma-separated allowlist. When set it alone decides which tools register. |
| `SLACK_MCP_TOOL_PRESET` | `daily-power` | `daily-power` or `legacy-full`; used when `SLACK_MCP_ENABLED_TOOLS` is unset. |
| `SLACK_MCP_ADD_MESSAGE_TOOL` | unset = every channel | Channel allow/block list for `conversations_add_message`, `messages_schedule`, `messages_update`, `messages_delete`: `C1,C2` = only those; `!C1,!C2` = all except those. It does not enable or disable tools. |
| `SLACK_MCP_REACTION_TOOL` | unset = every channel | Same shape, for `reactions_add` / `reactions_remove`. |
| `SLACK_MCP_CHANNEL_MANAGEMENT_TOOL` | unset = every channel | Same shape, for `channels_rename`, `channels_set_topic`, `channels_set_purpose`, `channels_archive`. |
| `SLACK_MCP_ADD_MESSAGE_MARK` | unset | `true` marks a conversation read after posting to it. |
| `SLACK_MCP_ADD_MESSAGE_UNFURLING` | unset | `true` lets Slack unfurl posted links, or a comma-separated domain allowlist (`github.com,slack.com`). A text that mixes allowed and unknown domains is not unfurled. |
| `SLACK_MCP_COMPACT_OUTPUT` | `true` | Server-wide default for the `detail` parameter: `true` = `standard`, `false` = `full`. |
| `SLACK_MCP_DOWNLOAD_DIR` | unset | Absolute directory `files_download` may write into. Unset disables that tool; the server writes nowhere else. |
| `SLACK_MCP_USERS_CACHE` / `SLACK_MCP_CHANNELS_CACHE` | OS cache dir, team-prefixed `users_cache.json` / `channels_cache_v2.json` | Cache file paths. |
| `SLACK_MCP_CACHE_TTL` | `24h` | Cache time-to-live (`24h`, `30m`, or seconds). `0` never expires. Stale data is served while a background refresh runs. |
| `SLACK_MCP_MIN_REFRESH_INTERVAL` | `30s` | Minimum gap between forced cache refreshes. `0` disables the limit. |
| `SLACK_MCP_HOST` / `SLACK_MCP_PORT` | `127.0.0.1` / `13080` | Listen address for `sse` and `http`. |
| `SLACK_MCP_API_KEY` | | Bearer token for `sse` and `http`. These transports refuse to start without it unless `SLACK_MCP_ALLOW_UNAUTHENTICATED=true` (exactly `true`). `SLACK_MCP_SSE_API_KEY` is the deprecated name. |
| `SLACK_MCP_PROXY` | | Proxy URL for outgoing requests. |
| `SLACK_MCP_USER_AGENT` / `SLACK_MCP_CUSTOM_TLS` | | Custom User-Agent, and a matching TLS handshake, for enterprise networks. |
| `SLACK_MCP_SERVER_CA` / `SLACK_MCP_SERVER_CA_INSECURE` | | Extra CA PEM path; `true` trusts every certificate (debugging only). `SLACK_MCP_SERVER_CA_TOOLKIT` is removed and fatals if set. |
| `SLACK_MCP_GOVSLACK` | | `true`/`1`/`yes` routes every API, edge, and OAuth call to `slack-gov.com`. |
| `SLACK_MCP_LOG_LEVEL` / `SLACK_MCP_LOG_FORMAT` / `SLACK_MCP_LOG_COLOR` | `info` / auto / auto | Zap level; `json` or console (auto: JSON when not a TTY or in a container); colour on or off. |
| `SLACK_MCP_LOG_PARAMS` | unset | `debug` logs full tool parameters at Info (may include message text). |

### Limitations matrix and cache

| Users Cache        | Channels Cache     | Limitations                                                                                                                                                                                                                                                                                                                  |
|--------------------|--------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| :x:                | :x:                | No cache, No LLM context enhancement with user data, tool `channels_list` will be fully not functional. Tools `conversations_*` will have limited capabilities and you won't be able to search messages by `@userHandle` or `#channel-name`, getting messages by `@userHandle` or `#channel-name` won't be available either. |
| :white_check_mark: | :x:                | No channels cache, tool `channels_list` will be fully not functional. Tools `conversations_*` will have limited capabilities and you won't be able to search messages by `@userHandle` or `#channel-name`, getting messages by `@userHandle` or `#channel-name` won't be available either.                                   |
| :white_check_mark: | :white_check_mark: | No limitations, fully functional Slack MCP Server.                                                                                                                                                                                                                                                                           |

### Debugging tools

```bash
# Run the inspector with stdio transport
npx @modelcontextprotocol/inspector go run ./cmd/slack-mcp-server --transport stdio

# View logs
tail -n 20 -f ~/Library/Logs/Claude/mcp*.log
```

## Security

- Never share API tokens
- Keep .env files secure and private

## License

Licensed under MIT. See the [LICENSE](LICENSE) file. This is not an official Slack product.
