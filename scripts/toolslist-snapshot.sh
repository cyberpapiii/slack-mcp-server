#!/usr/bin/env bash
# Dump the MCP tools/list payload the server advertises for a preset.
#
# Runs the server in demo mode over stdio with every tool gate armed, sends
# initialize + tools/list, and writes the raw JSON-RPC response to OUT.
# Prints total bytes, tool count, and the per-tool size table so before/after
# runs can be diffed.
#
# usage: scripts/toolslist-snapshot.sh [preset] [out.json]
#   preset  legacy-full (default) | daily-power
#   out     defaults to .audit/toolslist-<preset>.json
set -euo pipefail

preset="${1:-legacy-full}"
out="${2:-.audit/toolslist-${preset}.json}"
root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$root/build/slack-mcp-server"

mkdir -p "$(dirname "$out")"
(cd "$root" && go build -o "$bin" ./cmd/slack-mcp-server)

gates=(
  SLACK_MCP_ACTIVITY_MARK_TOOL SLACK_MCP_ADD_MESSAGE_TOOL SLACK_MCP_ATTACHMENT_TOOL
  SLACK_MCP_CANVAS_WRITE_TOOL SLACK_MCP_CHANNEL_CREATE_TOOL SLACK_MCP_CHANNEL_MANAGEMENT_TOOL
  SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL SLACK_MCP_DND_TOOL SLACK_MCP_DRAFT_WRITE_TOOL
  SLACK_MCP_FILES_LIST_TOOL SLACK_MCP_FILE_UPLOAD_TOOL SLACK_MCP_LISTS_WRITE_TOOL
  SLACK_MCP_MARK_TOOL SLACK_MCP_PROFILE_WRITE_TOOL SLACK_MCP_REACTION_TOOL
  SLACK_MCP_SAVED_WRITE_TOOL SLACK_MCP_SCHEDULED_MESSAGE_TOOL SLACK_MCP_USERGROUPS_WRITE_TOOL
  SLACK_MCP_MESSAGE_EDIT_TOOL
)
envargs=()
for g in "${gates[@]}"; do envargs+=("$g=true"); done

req='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"snapshot","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'

raw="$(printf '%s\n' "$req" | env -i PATH="$PATH" HOME="$HOME" \
  SLACK_MCP_XOXC_TOKEN=demo SLACK_MCP_XOXD_TOKEN=demo \
  SLACK_MCP_TOOL_PRESET="$preset" SLACK_MCP_LOG_LEVEL=error \
  "${envargs[@]}" "$bin" --transport stdio 2>/dev/null || true)"

printf '%s\n' "$raw" | grep -m1 '"id":2' > "$out"

bytes=$(wc -c < "$out" | tr -d ' ')
count=$(jq '.result.tools | length' "$out")
printf 'preset=%s tools=%s bytes=%s approx_tokens=%s\n' "$preset" "$count" "$bytes" $((bytes / 4))
jq -r '.result.tools[] | "\(.name)\t\(. | tojson | length)\t\(.description | length)\t\(.inputSchema | tojson | length)\t\(.outputSchema // {} | tojson | length)"' "$out" \
  | sort -t$'\t' -k2 -nr \
  | awk -F'\t' 'BEGIN{printf "%-34s %7s %6s %7s %7s\n","tool","total","desc","input","output"} {printf "%-34s %7d %6d %7d %7d\n",$1,$2,$3,$4,$5}'
