package handler

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// ToolDef is one MCP tool a handler serves: its capability name, the MCP
// description and parameters, and the function that runs it. Title, hints,
// registration phase and credential requirement come from the capability row.
type ToolDef struct {
	Name    string
	Options []mcp.ToolOption
	Run     func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

const (
	descChannelID = "Channel ID (Cxxxxxxxxxx) or a channel/DM name starting with # or @, e.g. #general or @username."
	descCursor    = "Pagination cursor from the #next_cursor line of the previous response."
	// descChannelIDRaw is for tools whose handler passes the ID straight to Slack
	// without resolving #name or @name.
	descChannelIDRaw      = "Channel ID starting with C or G (Cxxxxxxxxxx); names are not resolved."
	descPrepareAction     = "prepare returns data.approval_token and a preview; execute performs the change."
	descApprovalToken     = "Token from the prepare call; required for execute."
	toolDetailDescription = "'standard' (compact CSV) or 'full' (adds UserID and Permalink columns); omit for the server default, normally standard. Comment lines may precede the CSV header: `#channels:` (ID=name), `#users:` (UserID=name), `#link_template:`, `#attachments:` (rows carry files; fetch them with attachment_get_data), `#partial:`, `#next_cursor:`."
)
