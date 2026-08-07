# Agent presets for Slack MCP (Plug)

These presets tune `SLACK_MCP_ENABLED_TOOLS` and related env vars in Plug's `[servers.slack.env]` block. After editing `~/Library/Application Support/plug/config.toml` or changing Go code, run `make deploy-local` from this repo. It builds the binary and restarts Plug's `slack` server. (A bare `plug reload` is not enough. It reloads config but leaves the old server process running.)

## Shared settings

| Variable | Recommended | Purpose |
|----------|-------------|---------|
| `SLACK_MCP_COMPACT_OUTPUT` | default **on** (unset = on) | Server-wide default output mode. Set `false` for legacy verbose output. Agents can override per call with the `detail` parameter (below), which is usually the better lever. |
| `slack_auth_status` | in allowlist | Check cache + browser auth before activity/saved tools |

## Output format (standard mode)

Message-returning tools (`conversations_history`, `conversations_replies`,
`conversations_search_messages`, `conversations_unreads`, `activity_unreads`,
`saved_list`) accept a per-call `detail` parameter: `standard` (default,
compact agent CSV) or `full` (verbose CSV with all columns, including
`UserID` and `Permalink` where available).

Standard-mode output may begin with legend comment lines before the CSV
header:

```
#users: U03BMAR2R50=robdezendorf|rob dezendorf, U045MM2AJCQ=konopackimarie|Marie Konopacki
#link_template: https://<workspace>.slack.com/archives/{CHANNEL_ID}/p{MsgID with "." removed}
User,Channel,Text,Time,MsgID,ThreadTs,Reactions,AttachmentIDs,Files,Cursor
```

- `#users:` maps each distinct human speaker to `UserID=username|Real Name`
  (bots excluded; emitted only for responses with 3+ messages).
- `#link_template:` lets you construct a message permalink from the Channel
  and MsgID columns. The search tool's Channel column is `ID (#name)`, so use
  the leading ID. Example: MsgID `1782935556.396379` in `C041QQ9FNAJ` →
  `.../archives/C041QQ9FNAJ/p1782935556396379`.
- `Files` is a count of attached files; `AttachmentIDs` carries their
  downloadable IDs.
- Long bot/link-unfurl attachments render up to a 300-char budget. When cut,
  the row ends with `…[attachment truncated; re-fetch this message with
  detail: full]`. Attachments have no ID-addressable fetch path, so the
  `detail: full` re-fetch is the lossless recovery route.

Side-effecting tools still need registration opt-in. Canonical gate table (boolean vs channel-allowlist, `true`/`1`/`yes`, allowlist interaction): `AGENTS.md` "Tool surface". Common local trio:

- `SLACK_MCP_ADD_MESSAGE_TOOL`: posting
- `SLACK_MCP_REACTION_TOOL`: reactions
- `SLACK_MCP_ATTACHMENT_TOOL`: file download

Also gated: `SLACK_MCP_MARK_TOOL`, `SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL`, `SLACK_MCP_USERGROUPS_WRITE_TOOL`, `SLACK_MCP_FILES_LIST_TOOL`. When `SLACK_MCP_ENABLED_TOOLS` is set, naming a gated tool in that list registers it without its dedicated env var.

## Preset: read-only triage

Best for inbox review, search, and channel discovery without posting.

```toml
SLACK_MCP_ENABLED_TOOLS = "slack_auth_status,conversations_history,conversations_replies,conversations_get_message,conversations_search_messages,conversations_mark,channels_list,channels_me,channels_starred,conversations_unreads,reactions_get,users_search,files_list,usergroups_list,usergroups_me,activity_unreads,saved_list"
```

## Preset: full agent (default local)

Read + write + activity/saved; matches typical Cursor agent workflows on this machine. Full catalog: `AGENTS.md` / `ValidToolNames` (31 tools). This preset omits `conversations_join` / `conversations_leave` on purpose.

```toml
SLACK_MCP_ADD_MESSAGE_TOOL = "true"
SLACK_MCP_REACTION_TOOL = "true"
SLACK_MCP_ATTACHMENT_TOOL = "true"
SLACK_MCP_ENABLED_TOOLS = "slack_auth_status,conversations_history,conversations_replies,conversations_get_message,conversations_add_message,conversations_draft_message,conversations_search_messages,conversations_mark,conversations_open,channels_list,channels_me,channels_starred,conversations_unreads,reactions_add,reactions_remove,reactions_get,attachment_get_data,files_list,usergroups_list,usergroups_me,usergroups_create,usergroups_update,usergroups_users_update,users_search,activity_unreads,activity_mark_read,saved_list,saved_update,saved_clear_completed"
```

## Preset: minimal (IDs only)

Use with `--no-cache` or when channel/user name resolution is not needed.

```toml
SLACK_MCP_ENABLED_TOOLS = "slack_auth_status,conversations_history,conversations_replies,conversations_search_messages,users_search"
```

## Troubleshooting

1. Call **`slack_auth_status`**. It confirms user/channel cache readiness and xoxc/xoxd browser session health.
2. If caches are not ready, wait for warm-up (up to 3 attempts, 30s apart) or restart Plug.
3. Activity and Saved tools require browser session tokens; refresh Slack in the browser and restart Plug if degraded.
