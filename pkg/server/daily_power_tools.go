package server

import (
	"context"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

func registerDailyPowerLifecycleTools(s *mcpserver.MCPServer, api *provider.ApiProvider, logger *zap.Logger, enabledTools []string) {
	approvals := approval.NewStore(5 * time.Minute)

	if service, err := api.Scheduled(); err == nil {
		h := handler.NewScheduledHandler(service, approvals, api.Identity, logger)
		if shouldAddTool(ToolScheduledMessagesList, enabledTools, "") {
			s.AddTool(newDailyPowerTool(ToolScheduledMessagesList,
				mcp.WithDescription("List pending scheduled Slack messages with stable IDs and UTC post times."),
				mcp.WithString("channel_id"), mcp.WithString("cursor"), mcp.WithString("oldest"), mcp.WithString("latest"), mcp.WithString("text_query"),
				mcp.WithNumber("limit", mcp.DefaultNumber(50)),
			), h.List)
		}
		if shouldAddTool(ToolScheduledMessageCancel, enabledTools, "SLACK_MCP_SCHEDULED_MESSAGE_TOOL") {
			s.AddTool(newDailyPowerTool(ToolScheduledMessageCancel,
				mcp.WithDescription("Preview or execute cancellation of one pending scheduled message. Prepare first, then confirm the exact target before execute."),
				mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute")),
				mcp.WithString("channel_id", mcp.Required()), mcp.WithString("scheduled_message_id", mcp.Required()), mcp.WithString("approval_token"),
			), h.Cancel)
		}
	} else {
		logger.Warn("Scheduled-message tools unavailable", zap.Error(err))
	}

	if service, err := api.ChannelMutations(); err == nil {
		h := handler.NewChannelMutationHandler(service, approvals, api.Identity, logger)
		registerChannelMutationTools(s, h, enabledTools)
	} else {
		logger.Warn("Channel-mutation tools unavailable", zap.Error(err))
	}

	if lists, err := api.Lists(); err == nil {
		h := handler.NewListsHandler(lists, approvals, api.Identity, logger)
		registerListsTools(s, h, enabledTools)
	} else {
		logger.Warn("Slack Lists tools unavailable", zap.Error(err))
	}

	if dnd, err := api.DND(); err == nil {
		h := handler.NewDNDHandler(dnd, api.Identity, logger)
		if shouldAddTool(ToolDNDGet, enabledTools, "") {
			s.AddTool(newDailyPowerTool(ToolDNDGet, mcp.WithDescription("Read the authenticated user's Do Not Disturb and snooze state.")), h.Get)
		}
		if shouldAddTool(ToolDNDSetSnooze, enabledTools, "SLACK_MCP_DND_TOOL") {
			s.AddTool(newDailyPowerTool(ToolDNDSetSnooze,
				mcp.WithDescription("Set a bounded Do Not Disturb snooze for the authenticated user. Requires client confirmation."),
				mcp.WithNumber("minutes", mcp.Required(), mcp.Min(1), mcp.Max(10080)),
			), h.SetSnooze)
		}
		if shouldAddTool(ToolDNDEndSnooze, enabledTools, "SLACK_MCP_DND_TOOL") {
			s.AddTool(newDailyPowerTool(ToolDNDEndSnooze, mcp.WithDescription("End the authenticated user's current Do Not Disturb snooze. Requires client confirmation.")), h.EndSnooze)
		}
	} else {
		logger.Info("DND tools unavailable", zap.Error(err))
	}
}

func registerChannelMutationTools(s *mcpserver.MCPServer, h *handler.ChannelMutationHandler, enabledTools []string) {
	gate := "SLACK_MCP_CHANNEL_MANAGEMENT_TOOL"
	if shouldAddTool(ToolChannelsRename, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolChannelsRename, mcp.WithDescription("Rename one ordinary channel. Requires client confirmation."), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("name", mcp.Required())), h.ConversationsRenameHandler)
	}
	if shouldAddTool(ToolChannelsSetTopic, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolChannelsSetTopic, mcp.WithDescription("Set or clear one ordinary channel topic. Requires client confirmation."), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("topic", mcp.Required())), h.ConversationsSetTopicHandler)
	}
	if shouldAddTool(ToolChannelsSetPurpose, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolChannelsSetPurpose, mcp.WithDescription("Set or clear one ordinary channel purpose. Requires client confirmation."), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("purpose", mcp.Required())), h.ConversationsSetPurposeHandler)
	}
	if shouldAddTool(ToolChannelsArchive, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolChannelsArchive,
			mcp.WithDescription("Preview or archive one ordinary channel. Prepare first, then confirm the exact observed channel before execute."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute")), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("approval_token"),
		), h.ConversationsArchiveHandler)
	}
}

func registerListsTools(s *mcpserver.MCPServer, h *handler.ListsHandler, enabledTools []string) {
	gate := "SLACK_MCP_LISTS_WRITE_TOOL"
	if shouldAddTool(ToolListsItemsList, enabledTools, "") {
		s.AddTool(newDailyPowerTool(ToolListsItemsList, mcp.WithDescription("List items and schema metadata for a known Slack List ID."), mcp.WithString("list_id", mcp.Required()), mcp.WithNumber("limit"), mcp.WithString("cursor"), mcp.WithBoolean("archived")), h.ListItems)
	}
	if shouldAddTool(ToolListsCreate, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolListsCreate, mcp.WithDescription("Create a Slack List. Requires client confirmation."), mcp.WithString("name", mcp.Required()), mcp.WithArray("description_blocks"), mcp.WithArray("schema"), mcp.WithString("copy_from_list_id"), mcp.WithBoolean("include_copied_list_records"), mcp.WithBoolean("todo_mode")), h.CreateList)
	}
	if shouldAddTool(ToolListsUpdate, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolListsUpdate, mcp.WithDescription("Update Slack List metadata. Requires client confirmation."), mcp.WithString("id", mcp.Required()), mcp.WithString("name"), mcp.WithArray("description_blocks"), mcp.WithBoolean("todo_mode")), h.UpdateList)
	}
	if shouldAddTool(ToolListsItemsCreate, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolListsItemsCreate, mcp.WithDescription("Create one Slack List item. Requires client confirmation."), mcp.WithString("list_id", mcp.Required()), mcp.WithString("duplicated_item_id"), mcp.WithString("parent_item_id"), mcp.WithArray("initial_fields")), h.CreateItem)
	}
	if shouldAddTool(ToolListsItemsUpdate, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolListsItemsUpdate, mcp.WithDescription("Update typed cells on Slack List items. Requires client confirmation."), mcp.WithString("list_id", mcp.Required()), mcp.WithArray("cells", mcp.Required())), h.UpdateItems)
	}
	if shouldAddTool(ToolListsItemDelete, enabledTools, gate) {
		s.AddTool(newDailyPowerTool(ToolListsItemDelete,
			mcp.WithDescription("Preview or delete one Slack List item. Prepare first, then confirm the exact item before execute."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute")), mcp.WithString("list_id", mcp.Required()), mcp.WithString("item_id", mcp.Required()), mcp.WithString("approval_token"),
		), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			switch request.GetString("action", "") {
			case "prepare":
				return h.PrepareDeleteItem(ctx, request)
			case "execute":
				return h.DeleteItem(ctx, request)
			default:
				return handler.NewTypedErrorResult(&handler.ToolError{Code: "invalid_arguments", Message: "action must be prepare or execute"}), nil
			}
		})
	}
}
