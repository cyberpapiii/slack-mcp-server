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
		addEnabledTool(s, enabledTools, newDailyPowerTool(ToolScheduledMessagesList,
			mcp.WithDescription("List pending scheduled Slack messages with stable IDs and UTC post times."),
			mcp.WithString("channel_id"), mcp.WithString("cursor"), mcp.WithString("oldest"), mcp.WithString("latest"), mcp.WithString("text_query"),
			mcp.WithNumber("limit", mcp.DefaultNumber(50)),
		), h.List)

		addEnabledTool(s, enabledTools, newDailyPowerTool(ToolScheduledMessageCancel,
			mcp.WithDescription("Preview or execute cancellation of one pending scheduled message. Prepare first, then confirm the exact target before execute."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute")),
			mcp.WithString("channel_id", mcp.Required()), mcp.WithString("scheduled_message_id", mcp.Required()), mcp.WithString("approval_token"),
		), h.Cancel)

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
		addEnabledTool(s, enabledTools, newDailyPowerTool(ToolDNDGet, mcp.WithDescription("Read the authenticated user's Do Not Disturb and snooze state.")), h.Get)

		addEnabledTool(s, enabledTools, newDailyPowerTool(ToolDNDSetSnooze,
			mcp.WithDescription("Set a bounded Do Not Disturb snooze for the authenticated user. Requires client confirmation."),
			mcp.WithNumber("minutes", mcp.Required(), mcp.Min(1), mcp.Max(10080)),
		), h.SetSnooze)

		addEnabledTool(s, enabledTools, newDailyPowerTool(ToolDNDEndSnooze, mcp.WithDescription("End the authenticated user's current Do Not Disturb snooze. Requires client confirmation.")), h.EndSnooze)

	} else {
		logger.Info("DND tools unavailable", zap.Error(err))
	}

	registerCustomPowerTools(s, api, logger, enabledTools, approvals)
}

func registerChannelMutationTools(s *mcpserver.MCPServer, h *handler.ChannelMutationHandler, enabledTools []string) {
	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolChannelsRename, mcp.WithDescription("Rename one ordinary channel. Requires client confirmation."), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("name", mcp.Required())), h.ConversationsRenameHandler)

	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolChannelsSetTopic, mcp.WithDescription("Set or clear one ordinary channel topic. Requires client confirmation."), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("topic", mcp.Required())), h.ConversationsSetTopicHandler)

	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolChannelsSetPurpose, mcp.WithDescription("Set or clear one ordinary channel purpose. Requires client confirmation."), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("purpose", mcp.Required())), h.ConversationsSetPurposeHandler)

	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolChannelsArchive,
		mcp.WithDescription("Preview or archive one ordinary channel. Prepare first, then confirm the exact observed channel before execute."),
		mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute")), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("approval_token"),
	), h.ConversationsArchiveHandler)

}

func registerListsTools(s *mcpserver.MCPServer, h *handler.ListsHandler, enabledTools []string) {
	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolListsItemsList, mcp.WithDescription("List items for a known Slack List ID."), mcp.WithString("list_id", mcp.Required()), mcp.WithNumber("limit"), mcp.WithString("cursor"), mcp.WithBoolean("archived")), h.ListItems)

	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolListsCreate,
		mcp.WithDescription("Create a Slack List. Requires client confirmation."),
		mcp.WithString("name", mcp.Required()),
		mcp.WithArray("description_blocks", mcp.Items(openObjectSchema())),
		mcp.WithArray("schema", mcp.Items(listColumnItemSchema())),
		mcp.WithString("copy_from_list_id"), mcp.WithBoolean("include_copied_list_records"), mcp.WithBoolean("todo_mode"),
	), h.CreateList)

	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolListsUpdate, mcp.WithDescription("Update Slack List metadata. Requires client confirmation."), mcp.WithString("id", mcp.Required()), mcp.WithString("name"), mcp.WithArray("description_blocks", mcp.Items(openObjectSchema())), mcp.WithBoolean("todo_mode")), h.UpdateList)

	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolListsItemsCreate, mcp.WithDescription("Create one Slack List item. Requires client confirmation."), mcp.WithString("list_id", mcp.Required()), mcp.WithString("duplicated_item_id"), mcp.WithString("parent_item_id"), mcp.WithArray("initial_fields", mcp.Items(listFieldItemSchema(false)))), h.CreateItem)

	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolListsItemsUpdate, mcp.WithDescription("Update typed cells on Slack List items. Requires client confirmation."), mcp.WithString("list_id", mcp.Required()), mcp.WithArray("cells", mcp.Required(), mcp.MinItems(1), mcp.Items(listFieldItemSchema(true)))), h.UpdateItems)

	addEnabledTool(s, enabledTools, newDailyPowerTool(ToolListsItemDelete,
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

func openObjectSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true}
}

func listColumnItemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":               map[string]any{"type": "string"},
			"name":              map[string]any{"type": "string"},
			"type":              map[string]any{"type": "string", "enum": []string{"text", "rich_text", "number", "select", "multi_select", "date", "user", "checkbox", "email", "phone", "channel", "link"}},
			"is_primary_column": map[string]any{"type": "boolean"},
			"options": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"choices": map[string]any{"type": "array", "items": map[string]any{
						"type": "object", "required": []string{"value", "label"},
						"properties":           map[string]any{"value": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}, "color": map[string]any{"type": "string"}},
						"additionalProperties": false,
					}},
					"format": map[string]any{"type": "string"}, "precision": map[string]any{"type": "integer"},
					"date_format": map[string]any{"type": "string"}, "emoji": map[string]any{"type": "string"},
					"emoji_team_id": map[string]any{"type": "string"}, "max": map[string]any{"type": "integer"},
					"show_member_name": map[string]any{"type": "boolean"}, "notify_users": map[string]any{"type": "boolean"},
				},
				"additionalProperties": false,
			},
		},
		"required":             []string{"key", "name", "type"},
		"additionalProperties": false,
	}
}

func listFieldItemSchema(requireRowID bool) map[string]any {
	typedValueProperties := map[string]any{
		"rich_text": map[string]any{"type": "array", "items": openObjectSchema()},
		"date":      stringArraySchema(), "select": stringArraySchema(), "user": stringArraySchema(),
		"channel": stringArraySchema(), "number": numberArraySchema(), "checkbox": booleanArraySchema(),
		"email": stringArraySchema(), "phone": stringArraySchema(),
		"link": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "required": []string{"original_url"},
			"properties":           map[string]any{"original_url": map[string]any{"type": "string"}, "display_as_url": map[string]any{"type": "boolean"}, "display_name": map[string]any{"type": "string"}},
			"additionalProperties": false,
		}},
	}
	properties := map[string]any{"column_id": map[string]any{"type": "string"}, "row_id": map[string]any{"type": "string"}}
	typedValueNames := []string{"rich_text", "date", "select", "user", "channel", "number", "checkbox", "email", "phone", "link"}
	oneOf := make([]map[string]any, 0, len(typedValueNames))
	for _, name := range typedValueNames {
		schema := typedValueProperties[name]
		properties[name] = schema
		oneOf = append(oneOf, map[string]any{"required": []string{name}})
	}
	required := []string{"column_id"}
	if requireRowID {
		required = append(required, "row_id")
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "oneOf": oneOf, "additionalProperties": false}
}

func stringArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func numberArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "number"}}
}

func booleanArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "boolean"}}
}
