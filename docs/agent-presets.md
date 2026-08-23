# Agent presets for Slack MCP (Plug)

These presets tune `SLACK_MCP_ENABLED_TOOLS` and related env vars in Plug's `[servers.slack.env]` block. After editing `~/Library/Application Support/plug/config.toml` or changing Go code, run `make deploy-local` from this repo. It builds the binary and restarts Plug's `slack` server. (A bare `plug reload` is not enough. It reloads config but leaves the old server process running.)

`daily-power` is the default when neither `SLACK_MCP_ENABLED_TOOLS` nor
`SLACK_MCP_TOOL_PRESET` is set. An explicit enabled-tools list always wins.
Set `SLACK_MCP_TOOL_PRESET=legacy-full` to expose every implemented tool in this
custom server. The tool table is `pkg/capability/catalog.go`.

## Shared settings

| Variable | Recommended | Purpose |
|----------|-------------|---------|
| `SLACK_MCP_COMPACT_OUTPUT` | default **on** (unset = on) | Server-wide default output mode. Set `false` for legacy verbose output. Agents can override per call with the `detail` parameter (below), which is usually the better lever. |
| `slack_auth_status` | in allowlist | Check cache + browser auth before activity/saved tools |

## Output format

Every list tool returns CSV text. Comment lines before the header carry what
does not fit a row; the header is PascalCase; the `Channel` column is the bare
conversation ID and `MsgID` is the message timestamp.

```
#channels: C041QQ9FNAJ=#general, D0AGSQXLJHG=@john
#users: U03BMAR2R50=robdezendorf|Rob Dezendorf, U045MM2AJCQ=konopackimarie|Marie Konopacki
#link_template: https://<workspace>.slack.com/archives/{Channel}/p{MsgID with "." removed}
#attachments: fetch a FileID with attachment_get_data; images and text return readable content, other types return base64, 5MB cap; files_download saves it to disk instead
#next_cursor: dXNlcjpVMDYxTkZUVDI=
User,Channel,Text,Time,MsgID,ThreadTs,Reactions,AttachmentIDs,Files
```

- `#channels:` maps each distinct conversation ID in the rows to `#name` or
  `@user` (emitted whenever the cache knows a name).
- `#users:` maps each distinct human speaker to `UserID=username|Real Name`
  (bots excluded; emitted only for responses with 3+ messages).
- `#link_template:` builds a permalink from `Channel` and `MsgID`. Example:
  MsgID `1782935556.396379` in `C041QQ9FNAJ` becomes
  `.../archives/C041QQ9FNAJ/p1782935556396379`.
- `#next_cursor:` appears only when another page exists; pass it back as the
  `cursor` parameter. There is no cursor column.
- `#partial:` names the reason when a result was cut short (rate limit,
  channel cap); the rows you got are still valid.
- `activity_unreads` and `saved_list` append a second table after a
  `#activity_items:` / `#saved_items:` line; its rows carry the IDs that
  `activity_mark_read` and `saved_update` take.
- `Files` is a count of attached files; `AttachmentIDs` describes each one as
  `FileID (name, kind, size)`, e.g. `F0BS2 (shot.png, image, 340KB)`. The kind
  is `image` or `text` when `attachment_get_data` will return readable content,
  and the raw Slack filetype (`pdf`, `mp4`, `zip`) when it will only return
  base64.
- `#attachments:` appears whenever any row carries a file and names the fetch
  route plus its limits. Unlike `#users:`, it is not suppressed on short
  responses, so a single-message read still carries it.
- Message tools accept `detail: standard` (default) or `detail: full` (every
  column, attachments untruncated). Long bot/link-unfurl attachments render up
  to a 300-char budget in standard mode. When cut, the row ends with
  `…[attachment truncated; re-fetch this message with detail: full]`; that
  re-fetch (or `conversations_get_message`) is the lossless recovery route.

Which tools register is decided only by `SLACK_MCP_ENABLED_TOOLS` (or the `SLACK_MCP_TOOL_PRESET` fallback). Three write families also honor a per-call channel allow/block list (`AGENTS.md` "Tool surface"):

- `SLACK_MCP_ADD_MESSAGE_TOOL`: posting and message lifecycle
- `SLACK_MCP_REACTION_TOOL`: reactions
- `SLACK_MCP_CHANNEL_MANAGEMENT_TOOL`: rename/topic/purpose/archive

## Preset: read-only triage

Best for inbox review, search, and channel discovery without posting.

```toml
SLACK_MCP_ENABLED_TOOLS = "slack_auth_status,conversations_history,conversations_replies,conversations_get_message,conversations_search_messages,conversations_mark,channels_list,channels_me,channels_starred,conversations_unreads,reactions_get,users_search,files_list,usergroups_list,activity_unreads,saved_list"
```

## Preset: daily power (default)

The safe custom-only default. It exposes typed read tools that require no
confirmation. Mutation tools remain hidden.

```toml
SLACK_MCP_TOOL_PRESET = "daily-power"
```

The `daily-power` allowlist:

```text
activity_unreads,canvases_read,channels_members,conversations_get_message,conversations_unreads,dnd_get,drafts_get,drafts_list,emoji_list,lists_items_list,saved_list,scheduled_messages_list,search_semantic,slack_auth_status,usergroups_list,usergroups_mine,users_get_profile
```

Before enabling mutations, verify the OAuth and browser sessions represent the
same Slack team and user, and cancellation leaves Slack unchanged.

## Preset: legacy full

Power-user preset with all 69 tools implemented by this custom server. It does
not use Slack's official MCP.

```toml
SLACK_MCP_TOOL_PRESET = "legacy-full"
```

## Preset: minimal (IDs only)

Use with `--no-cache` or when channel/user name resolution is not needed.

```toml
SLACK_MCP_ENABLED_TOOLS = "slack_auth_status,conversations_history,conversations_replies,conversations_search_messages,users_search"
```

## Troubleshooting

1. Call **`slack_auth_status`**. It confirms user/channel cache readiness and xoxc/xoxd browser session health.
2. If caches are not ready, wait for warm-up (up to 3 attempts, 30s apart) or restart Plug.
3. Unreads, Activity, Saved, and Draft tools require browser session tokens; refresh Slack in the browser and restart Plug if degraded.
