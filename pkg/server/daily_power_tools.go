package server

import (
	"context"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

func registerDailyPowerLifecycleTools(s *mcpserver.MCPServer, api *provider.ApiProvider, logger *zap.Logger, enabledTools []string, approvals *approval.Store) {
	if service, err := api.Scheduled(); err == nil {
		h := handler.NewScheduledHandler(service, approvals, api.Identity, logger)
		addEnabledTool(s, enabledTools, newTool(ToolScheduledMessagesList,
			mcp.WithDescription("List pending scheduled Slack messages with stable IDs and UTC post times."),
			mcp.WithString("channel_id", mcp.Description("Channel ID (Cxxxxxxxxxx) to filter by; omit for all channels. Names are not resolved.")), mcp.WithString("cursor", mcp.Description(descCursor)), mcp.WithString("oldest", mcp.Description("Unix seconds; only messages scheduled to post at or after this time.")), mcp.WithString("latest", mcp.Description("Unix seconds; only messages scheduled to post at or before this time.")), mcp.WithString("text_query", mcp.Description("Case-insensitive substring filter applied to message text after fetching the page.")),
			mcp.WithNumber("limit", mcp.DefaultNumber(50), mcp.Description("Maximum messages per page, 1 to 100; defaults to 50.")),
		), h.List)

		addEnabledTool(s, enabledTools, newTool(ToolScheduledMessageCancel,
			mcp.WithDescription("Preview or execute cancellation of one pending scheduled message. Prepare first, then confirm the exact target before execute."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) of the scheduled message; names are not resolved.")), mcp.WithString("scheduled_message_id", mcp.Required(), mcp.Description("Scheduled message ID from scheduled_messages_list, e.g. Q1234ABCD.")), mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
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
		addEnabledTool(s, enabledTools, newTool(ToolDNDGet, mcp.WithDescription("Read the authenticated user's Do Not Disturb and snooze state.")), h.Get)

		addEnabledTool(s, enabledTools, newTool(ToolDNDSetSnooze,
			mcp.WithDescription("Set a bounded Do Not Disturb snooze for the authenticated user."),
			mcp.WithNumber("minutes", mcp.Required(), mcp.Min(1), mcp.Max(10080), mcp.Description("Snooze length in minutes, 1 to 10080 (7 days).")),
		), h.SetSnooze)

		addEnabledTool(s, enabledTools, newTool(ToolDNDEndSnooze, mcp.WithDescription("End the authenticated user's current Do Not Disturb snooze.")), h.EndSnooze)

	} else {
		logger.Info("DND tools unavailable", zap.Error(err))
	}
}

func registerChannelMutationTools(s *mcpserver.MCPServer, h *handler.ChannelMutationHandler, enabledTools []string) {
	addEnabledTool(s, enabledTools, newTool(ToolChannelsRename, mcp.WithDescription("Rename one ordinary channel."), mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)), mcp.WithString("name", mcp.Required(), mcp.Description("New channel name, 1 to 80 lowercase letters, numbers, hyphens, or underscores."))), h.ConversationsRenameHandler)

	addEnabledTool(s, enabledTools, newTool(ToolChannelsSetTopic, mcp.WithDescription("Set or clear one ordinary channel topic."), mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)), mcp.WithString("topic", mcp.Required(), mcp.Description("New topic, up to 250 characters; pass an empty string to clear it."))), h.ConversationsSetTopicHandler)

	addEnabledTool(s, enabledTools, newTool(ToolChannelsSetPurpose, mcp.WithDescription("Set or clear one ordinary channel purpose."), mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)), mcp.WithString("purpose", mcp.Required(), mcp.Description("New purpose, up to 250 characters; pass an empty string to clear it."))), h.ConversationsSetPurposeHandler)

	addEnabledTool(s, enabledTools, newTool(ToolChannelsArchive,
		mcp.WithDescription("Preview or archive one ordinary channel. Prepare first, then confirm the exact observed channel before execute."),
		mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)), mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)), mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
	), h.ConversationsArchiveHandler)

}

func registerListsTools(s *mcpserver.MCPServer, h *handler.ListsHandler, enabledTools []string) {
	addEnabledTool(s, enabledTools, newTool(ToolListsItemsList, mcp.WithDescription("List items for a known Slack List ID."), mcp.WithString("list_id", mcp.Required(), mcp.Description("Slack List ID (starts with F, e.g. F1234567890).")), mcp.WithNumber("limit", mcp.Description("Maximum items per page, 0 to 200; 0 lets Slack choose the default.")), mcp.WithString("cursor", mcp.Description(descCursor)), mcp.WithBoolean("archived", mcp.Description("true returns archived items only; false or omitted returns normal items."))), h.ListItems)

	addEnabledTool(s, enabledTools, newTool(ToolListsCreate,
		mcp.WithDescription("Create a Slack List."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name of the new Slack List, shown in its header.")),
		mcp.WithArray("description_blocks", mcp.Items(openObjectSchema()), mcp.Description("Block Kit rich_text blocks for the List description; array of block objects.")),
		mcp.WithArray("schema", mcp.Items(listColumnItemSchema()), mcp.Description("Column definitions; each needs key, name, and type (text, number, select, date, user, etc.).")),
		mcp.WithString("copy_from_list_id", mcp.Description("ID of an existing Slack List (Fxxxxxxxxxx) to copy columns from.")), mcp.WithBoolean("include_copied_list_records", mcp.Description("When copying a List, also copy its items, not only the column schema.")), mcp.WithBoolean("todo_mode", mcp.Description("Create the List in Slack's to-do mode for tracking tasks.")),
	), h.CreateList)

	addEnabledTool(s, enabledTools, newTool(ToolListsUpdate, mcp.WithDescription("Update Slack List metadata."), mcp.WithString("id", mcp.Required(), mcp.Description("Slack List ID to update (starts with F, e.g. F1234567890).")), mcp.WithString("name", mcp.Description("New display name for the List; omit to leave unchanged.")), mcp.WithArray("description_blocks", mcp.Items(openObjectSchema()), mcp.Description("Block Kit rich_text blocks replacing the List description; omit to leave unchanged.")), mcp.WithBoolean("todo_mode", mcp.Description("Turn Slack's to-do mode on or off; omit to leave unchanged."))), h.UpdateList)

	addEnabledTool(s, enabledTools, newTool(ToolListsItemsCreate, mcp.WithDescription("Create one Slack List item."), mcp.WithString("list_id", mcp.Required(), mcp.Description("Slack List ID to add the item to (starts with F, e.g. F1234567890).")), mcp.WithString("duplicated_item_id", mcp.Description("ID of an existing item in the List to copy as the new item.")), mcp.WithString("parent_item_id", mcp.Description("ID of the parent item; makes the new item a subtask of it.")), mcp.WithArray("initial_fields", mcp.Items(listFieldItemSchema(false)), mcp.Description("Initial cell values; each has column_id plus one typed value key such as rich_text, date, select, or user."))), h.CreateItem)

	addEnabledTool(s, enabledTools, newTool(ToolListsItemsUpdate, mcp.WithDescription("Update typed cells on Slack List items."), mcp.WithString("list_id", mcp.Required(), mcp.Description("Slack List ID containing the items (starts with F, e.g. F1234567890).")), mcp.WithArray("cells", mcp.Required(), mcp.MinItems(1), mcp.Items(listFieldItemSchema(true)), mcp.Description("Cells to set; each has column_id, row_id (the item ID), and one typed value key such as rich_text or select."))), h.UpdateItems)

	addEnabledTool(s, enabledTools, newTool(ToolListsItemDelete,
		mcp.WithDescription("Preview or delete one Slack List item. Prepare first, then confirm the exact item before execute."),
		mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)), mcp.WithString("list_id", mcp.Required(), mcp.Description("Slack List ID containing the item (starts with F, e.g. F1234567890).")), mcp.WithString("item_id", mcp.Required(), mcp.Description("ID of the List item to delete, from lists_items_list or lists_items_create.")), mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
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
