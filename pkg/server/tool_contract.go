package server

import (
	"fmt"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/mark3labs/mcp-go/mcp"
)

// newTool builds every registered tool from the capability table so title,
// hints and output schema have one source.
func newTool(name string, options ...mcp.ToolOption) mcp.Tool {
	spec, ok := capability.Lookup(name)
	if !ok {
		panic(fmt.Sprintf("tool %q is not in the capability table", name))
	}
	if schema, ok := outputSchemaByTool[name]; ok {
		options = append(options, schema)
	}
	options = append(options,
		mcp.WithTitleAnnotation(spec.Title),
		mcp.WithReadOnlyHintAnnotation(spec.ReadOnly),
		mcp.WithDestructiveHintAnnotation(spec.Destructive),
		mcp.WithIdempotentHintAnnotation(spec.Idempotent),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	return normalizeAnnotations(mcp.NewTool(name, options...))
}

// outputSchemaByTool lists the tools whose structured result an agent must
// parse to continue: the diagnostics report, and the prepare/execute tools
// whose prepare phase returns data.approval_token. Every other tool still
// returns the same ToolResult envelope as structuredContent; it is
// self-describing JSON, so advertising its schema in tools/list would only
// cost tokens on every session.
var outputSchemaByTool = map[string]mcp.ToolOption{
	ToolSlackAuthStatus:        mcp.WithOutputSchema[handler.AuthStatusResult](),
	ToolScheduledMessageCancel: mcp.WithOutputSchema[handler.ScheduledCancelResult](),
	ToolChannelsArchive:        mcp.WithOutputSchema[handler.ChannelMutationResult](),
	ToolListsItemDelete:        mcp.WithOutputSchema[handler.ListItemDeleteResult](),
	ToolMessagesDelete:         mcp.WithOutputSchema[handler.MessageMutationResult](),
	ToolDraftsDelete:           mcp.WithOutputSchema[handler.DraftMutationResult](),
}
