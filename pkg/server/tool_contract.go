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
	case ToolUsergroupsList, ToolUsergroupsMine:
		return mcp.WithOutputSchema[handler.UsergroupPageResult](), "usergroup_page"
	case ToolUsergroupsJoin, ToolUsergroupsLeave:
		return mcp.WithOutputSchema[handler.UsergroupMembershipResult](), "usergroup_membership"
	case ToolActivityUnreads:
		return mcp.WithOutputSchema[handler.ActivityPageResult](), "activity_page"
	case ToolSavedList:
		return mcp.WithOutputSchema[handler.SavedPageResult](), "saved_page"
	case ToolScheduledMessagesList:
		return mcp.WithOutputSchema[handler.ScheduledPageResult](), "scheduled_message_page"
	case ToolScheduledMessageCancel:
		return mcp.WithOutputSchema[handler.ScheduledCancelResult](), "scheduled_message_mutation"
	case ToolChannelsRename, ToolChannelsSetTopic, ToolChannelsSetPurpose, ToolChannelsArchive:
		return mcp.WithOutputSchema[handler.ChannelMutationResult](), "channel_mutation"
	case ToolListsCreate:
		return mcp.WithOutputSchema[handler.ListCreateResult](), "list_create"
	case ToolListsUpdate:
		return mcp.WithOutputSchema[handler.ListMutationResult](), "list_mutation"
	case ToolListsItemsList:
		return mcp.WithOutputSchema[handler.ListItemsPageResult](), "list_items_page"
	case ToolListsItemsCreate:
		return mcp.WithOutputSchema[handler.ListItemResult](), "list_item"
	case ToolListsItemsUpdate:
		return mcp.WithOutputSchema[handler.ListItemMutationResult](), "list_item_mutation"
	case ToolListsItemDelete:
		return mcp.WithOutputSchema[handler.ListItemDeleteResult](), "list_item_mutation"
	case ToolDNDGet, ToolDNDSetSnooze, ToolDNDEndSnooze:
		return mcp.WithOutputSchema[handler.DNDStateResult](), "dnd_state"
	case ToolConversationsMark:
		return mcp.WithOutputSchema[handler.ActionResult](), "read_progress"
	case ToolReactionsRemove:
		return mcp.WithOutputSchema[handler.ActionResult](), "reaction_mutation"
	case ToolActivityMarkRead:
		return mcp.WithOutputSchema[handler.ActionResult](), "activity_mutation"
	case ToolSavedUpdate, ToolSavedClearCompleted:
		return mcp.WithOutputSchema[handler.ActionResult](), "saved_mutation"
	case ToolUsergroupsCreate, ToolUsergroupsUpdate, ToolUsergroupsUsersUpdate:
		return mcp.WithOutputSchema[handler.UsergroupMutationResult](), "usergroup_mutation"
	default:
		return nil, ""
	}
}
