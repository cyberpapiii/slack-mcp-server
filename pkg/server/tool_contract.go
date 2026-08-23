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
	expectedResultType, ok := resultTypeByTool[name]
	if !ok || entry.ResultType != expectedResultType {
		panic(fmt.Sprintf("daily-power tool %q result contract mismatch: catalog=%q server=%q", name, entry.ResultType, expectedResultType))
	}

	if schema, ok := outputSchemaByTool[name]; ok {
		options = append(options, schema)
	}
	options = append(options,
		mcp.WithTitleAnnotation(behavior.Title),
		mcp.WithReadOnlyHintAnnotation(behavior.ReadOnly),
		mcp.WithDestructiveHintAnnotation(behavior.Destructive),
		mcp.WithIdempotentHintAnnotation(behavior.Idempotent),
		mcp.WithOpenWorldHintAnnotation(behavior.OpenWorld),
	)
	return mcp.NewTool(name, options...)
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

// resultTypeByTool is cross-checked against the capability catalog so the two
// cannot drift.
var resultTypeByTool = map[string]string{
	ToolSlackAuthStatus:         "diagnostics",
	ToolConversationsGetMessage: "message",
	ToolConversationsUnreads:    "unread_page",
	ToolUsergroupsList:          "usergroup_page",
	ToolUsergroupsMine:          "usergroup_page",
	ToolUsergroupsJoin:          "usergroup_membership",
	ToolUsergroupsLeave:         "usergroup_membership",
	ToolActivityUnreads:         "activity_page",
	ToolSavedList:               "saved_page",
	ToolScheduledMessagesList:   "scheduled_message_page",
	ToolScheduledMessageCancel:  "scheduled_message_mutation",
	ToolChannelsRename:          "channel_mutation",
	ToolChannelsSetTopic:        "channel_mutation",
	ToolChannelsSetPurpose:      "channel_mutation",
	ToolChannelsArchive:         "channel_mutation",
	ToolListsCreate:             "list_create",
	ToolListsUpdate:             "list_mutation",
	ToolListsItemsList:          "list_items_page",
	ToolListsItemsCreate:        "list_item",
	ToolListsItemsUpdate:        "list_item_mutation",
	ToolListsItemDelete:         "list_item_mutation",
	ToolDNDGet:                  "dnd_state",
	ToolDNDSetSnooze:            "dnd_state",
	ToolDNDEndSnooze:            "dnd_state",
	ToolConversationsMark:       "read_progress",
	ToolReactionsRemove:         "reaction_mutation",
	ToolActivityMarkRead:        "activity_mutation",
	ToolSavedUpdate:             "saved_mutation",
	ToolSavedClearCompleted:     "saved_mutation",
	ToolUsergroupsCreate:        "usergroup_mutation",
	ToolUsergroupsUpdate:        "usergroup_mutation",
	ToolUsergroupsUsersUpdate:   "usergroup_mutation",
	ToolFilesUpload:             "file_mutation",
	ToolMessagesSchedule:        "scheduled_message",
	ToolMessagesUpdate:          "message_mutation",
	ToolMessagesDelete:          "message_mutation",
	ToolChannelsCreate:          "conversation",
	ToolChannelsInvite:          "conversation_membership",
	ToolChannelsMembers:         "member_page",
	ToolEmojiList:               "emoji_page",
	ToolUsersGetProfile:         "user_profile",
	ToolUsersSetProfile:         "user_profile",
	ToolUsersSetStatus:          "user_profile",
	ToolCanvasesCreate:          "canvas",
	ToolCanvasesRead:            "canvas",
	ToolCanvasesUpdate:          "canvas",
	ToolDraftsList:              "draft_page",
	ToolDraftsGet:               "draft",
	ToolDraftsCreate:            "draft_mutation",
	ToolDraftsUpdate:            "draft_mutation",
	ToolDraftsDelete:            "draft_mutation",
	ToolSearchSemantic:          "semantic_search_page",
}
