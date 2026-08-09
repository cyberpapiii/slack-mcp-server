# Custom Slack MCP capabilities

This fork is a self-contained Slack MCP. It does not connect to or depend on
Slack's official MCP server. The catalog in `pkg/capability/catalog.go` is the
source of truth for tool ownership, auth mode, OAuth scopes, confirmation tier,
and typed result contract.

`daily-power` is the safe default: typed, no-confirmation reads. `legacy-full`
exposes all 69 implemented local tools, including writes and browser-session
features. An explicit `SLACK_MCP_ENABLED_TOOLS` list overrides both presets.

OAuth is used for supported Slack Web API methods. The separate xoxc/xoxd
browser session is limited to Slack surfaces that have no public equivalent:
Activity, Later, and persisted draft CRUD. The two identities must match.

The custom-only expansion adds supported external file upload, message
schedule/edit/delete, full profiles and status, custom emoji, channel creation
and membership, canvas create/read/update, persisted draft list/get/create/
update/delete, and semantic search.

Two Slack limitations remain explicit:

- Slack's public Web API does not return full canvas content. `canvases_read`
  returns metadata, preview text, and matching section IDs.
- `search_semantic` searches messages and files through Slack's Real-time
  Search API and works only when Slack has enabled that API for the app.

Every new canonical tool has an object output schema, structured content, a
text fallback, explicit MCP behavior annotations, and an exact auth/scope entry.
Destructive operations use one-use, identity-bound prepare/execute approvals.
