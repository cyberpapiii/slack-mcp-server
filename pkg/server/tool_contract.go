package server

import (
	"fmt"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/mark3labs/mcp-go/mcp"
)

func newDailyPowerTool(name string, options ...mcp.ToolOption) mcp.Tool {
	behavior, ok := capability.BehaviorForLocalTool(name)
	if !ok {
		panic(fmt.Sprintf("daily-power tool %q has no behavior contract", name))
	}
	entry, ok := capability.EntryForLocalTool(name)
	if !ok {
		panic(fmt.Sprintf("daily-power tool %q has no active catalog entry", name))
	}
	outputSchema, expectedResultType := dailyPowerOutputSchema(name)
	if expectedResultType == "" || entry.ResultType != expectedResultType {
		panic(fmt.Sprintf("daily-power tool %q result contract mismatch: catalog=%q server=%q", name, entry.ResultType, expectedResultType))
	}

	options = append(options,
		outputSchema,
		mcp.WithTitleAnnotation(behavior.Title),
		mcp.WithReadOnlyHintAnnotation(behavior.ReadOnly),
		mcp.WithDestructiveHintAnnotation(behavior.Destructive),
		mcp.WithIdempotentHintAnnotation(behavior.Idempotent),
		mcp.WithOpenWorldHintAnnotation(behavior.OpenWorld),
	)
	return mcp.NewTool(name, options...)
}

func dailyPowerOutputSchema(name string) (mcp.ToolOption, string) {
	switch name {
	case ToolSlackAuthStatus:
		return mcp.WithOutputSchema[handler.AuthStatusResult](), "diagnostics"
	case ToolConversationsGetMessage:
		return mcp.WithOutputSchema[handler.MessageResult](), "message"
	case ToolConversationsUnreads:
		return mcp.WithOutputSchema[handler.UnreadPageResult](), "unread_page"
	case ToolUsergroupsList:
		return mcp.WithOutputSchema[handler.UsergroupPageResult](), "usergroup_page"
	case ToolActivityUnreads:
		return mcp.WithOutputSchema[handler.ActivityPageResult](), "activity_page"
	case ToolSavedList:
		return mcp.WithOutputSchema[handler.SavedPageResult](), "saved_page"
	default:
		return nil, ""
	}
}
