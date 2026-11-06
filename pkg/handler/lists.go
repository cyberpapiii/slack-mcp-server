package handler

import (
	"context"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
)

type ListsAPI interface {
	CreateList(context.Context, provider.CreateListRequest) (string, provider.ListMetadata, error)
	UpdateList(context.Context, provider.UpdateListRequest) error
	CreateItem(context.Context, provider.CreateListItemRequest) (provider.ListItem, error)
	UpdateItems(context.Context, provider.UpdateListItemsRequest) error
	GetItem(context.Context, string, string) (provider.ListItem, error)
	ListItems(context.Context, provider.ListItemsRequest) (provider.ListItemsPage, error)
	DeleteItem(context.Context, string, string) error
}

type ListsHandler struct {
	api       ListsAPI
	approvals approvalGate
}

func NewListsHandler(api ListsAPI, approvals *approval.Store, identity func() provider.ProviderIdentity) *ListsHandler {
	return &ListsHandler{api: api, approvals: approvalGate{store: approvals, identity: identity}}
}

type ListCreateData struct {
	ListID   string                `json:"list_id"`
	Metadata provider.ListMetadata `json:"metadata"`
}

type ListMutationData struct {
	ListID string `json:"list_id"`
	Status string `json:"status"`
}

type ListItemMutationData struct {
	ListID string `json:"list_id"`
	Status string `json:"status"`
}

type ListItemDeleteData struct {
	ApprovalPhase
	ListID string             `json:"list_id"`
	ItemID string             `json:"item_id"`
	Item   *provider.ListItem `json:"item,omitempty"`
	Status string             `json:"status"`
}

type ListItemDeleteResult = ToolResult[ListItemDeleteData]

func (h *ListsHandler) CreateList(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.CreateListRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	if input.Name == "" {
		return nil, invalidArguments("name is required")
	}
	listID, metadata, err := h.api.CreateList(ctx, input)
	if err != nil {
		return nil, TypeError(err, true)
	}
	return NewStructuredResult(ListCreateData{ListID: listID, Metadata: metadata}, SlackResultMeta("", false, ""), "Created Slack List "+listID), nil
}

func (h *ListsHandler) UpdateList(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.UpdateListRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	if input.ID == "" {
		return nil, invalidArguments("id is required")
	}
	if err := h.api.UpdateList(ctx, input); err != nil {
		return nil, TypeError(err, true)
	}
	return NewStructuredResult(ListMutationData{ListID: input.ID, Status: "updated"}, SlackResultMeta("", false, ""), "Updated Slack List "+input.ID), nil
}

func (h *ListsHandler) ListItems(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.ListItemsRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	if input.ListID == "" {
		return nil, invalidArguments("list_id is required")
	}
	if input.Limit < 0 || input.Limit > 200 {
		return nil, invalidArguments("limit must be between 0 and 200")
	}
	page, err := h.api.ListItems(ctx, input)
	if err != nil {
		return nil, err
	}
	return NewStructuredResult(page, SlackResultMeta(page.NextCursor, false, ""), fallbackJSON(page)), nil
}

func (h *ListsHandler) CreateItem(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.CreateListItemRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	if input.ListID == "" {
		return nil, invalidArguments("list_id is required")
	}
	item, err := h.api.CreateItem(ctx, input)
	if err != nil {
		return nil, TypeError(err, true)
	}
	return NewStructuredResult(item, SlackResultMeta("", false, ""), "Created Slack List item "+item.ID), nil
}

func (h *ListsHandler) UpdateItems(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.UpdateListItemsRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	if input.ListID == "" || len(input.Cells) == 0 {
		return nil, invalidArguments("list_id and at least one cell are required")
	}
	if err := h.api.UpdateItems(ctx, input); err != nil {
		return nil, TypeError(err, true)
	}
	return NewStructuredResult(ListItemMutationData{ListID: input.ListID, Status: "updated"}, SlackResultMeta("", false, ""), "Updated Slack List items"), nil
}

func (h *ListsHandler) DeleteItem(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	listID := strings.TrimSpace(request.GetString("list_id", ""))
	itemID := strings.TrimSpace(request.GetString("item_id", ""))
	if listID == "" || itemID == "" {
		return nil, invalidArguments("list_id and item_id are required")
	}
	prepare, err := approvalAction(request)
	if err != nil {
		return nil, err
	}
	item, err := h.api.GetItem(ctx, listID, itemID)
	if err != nil {
		return nil, TypeError(err, false)
	}
	arguments := struct {
		ListID string `json:"list_id"`
		ItemID string `json:"item_id"`
	}{listID, itemID}
	phase, err := h.approvals.check(request, capability.ListsItemDelete, prepare, arguments, item)
	if err != nil {
		return nil, err
	}
	if phase.Prepared() {
		data := ListItemDeleteData{ApprovalPhase: phase, ListID: listID, ItemID: itemID, Item: &item, Status: "awaiting_confirmation"}
		return NewStructuredResult(data, SlackResultMeta("", false, ""), "Prepared deletion of Slack List item "+itemID), nil
	}
	if err := h.api.DeleteItem(ctx, listID, itemID); err != nil {
		return nil, TypeError(err, true)
	}
	data := ListItemDeleteData{ApprovalPhase: phase, ListID: listID, ItemID: itemID, Status: "deleted"}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), "Deleted Slack List item "+itemID), nil
}

func (h *ListsHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.ListsItemsList, Run: h.ListItems, Options: []mcp.ToolOption{
			mcp.WithDescription("List items for a known Slack List ID."),
			mcp.WithString("list_id", mcp.Required(), mcp.Description("Slack List ID (starts with F, e.g. F1234567890).")),
			mcp.WithNumber("limit", mcp.Description("Maximum items per page, 0 to 200; 0 lets Slack choose the default.")),
			mcp.WithString("cursor", mcp.Description(descCursor)),
			mcp.WithBoolean("archived", mcp.Description("true returns archived items only; false or omitted returns normal items.")),
		}},
		{Name: capability.ListsCreate, Run: h.CreateList, Options: []mcp.ToolOption{
			mcp.WithDescription("Create a Slack List."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Display name of the new Slack List, shown in its header.")),
			mcp.WithArray("description_blocks", mcp.Items(openObjectSchema()), mcp.Description("Block Kit rich_text blocks for the List description; array of block objects.")),
			mcp.WithArray("schema", mcp.Items(listColumnItemSchema()), mcp.Description("Column definitions; each needs key, name, and type (text, number, select, date, user, etc.).")),
			mcp.WithString("copy_from_list_id", mcp.Description("ID of an existing Slack List (Fxxxxxxxxxx) to copy columns from.")),
			mcp.WithBoolean("include_copied_list_records", mcp.Description("When copying a List, also copy its items, not only the column schema.")),
			mcp.WithBoolean("todo_mode", mcp.Description("Create the List in Slack's to-do mode for tracking tasks.")),
		}},
		{Name: capability.ListsUpdate, Run: h.UpdateList, Options: []mcp.ToolOption{
			mcp.WithDescription("Update Slack List metadata."),
			mcp.WithString("id", mcp.Required(), mcp.Description("Slack List ID to update (starts with F, e.g. F1234567890).")),
			mcp.WithString("name", mcp.Description("New display name for the List; omit to leave unchanged.")),
			mcp.WithArray("description_blocks", mcp.Items(openObjectSchema()), mcp.Description("Block Kit rich_text blocks replacing the List description; omit to leave unchanged.")),
			mcp.WithBoolean("todo_mode", mcp.Description("Turn Slack's to-do mode on or off; omit to leave unchanged.")),
		}},
		{Name: capability.ListsItemsCreate, Run: h.CreateItem, Options: []mcp.ToolOption{
			mcp.WithDescription("Create one Slack List item."),
			mcp.WithString("list_id", mcp.Required(), mcp.Description("Slack List ID to add the item to (starts with F, e.g. F1234567890).")),
			mcp.WithString("duplicated_item_id", mcp.Description("ID of an existing item in the List to copy as the new item.")),
			mcp.WithString("parent_item_id", mcp.Description("ID of the parent item; makes the new item a subtask of it.")),
			mcp.WithArray("initial_fields", mcp.Items(listFieldItemSchema(false)), mcp.Description("Initial cell values; each has column_id plus one typed value key such as rich_text, date, select, or user.")),
		}},
		{Name: capability.ListsItemsUpdate, Run: h.UpdateItems, Options: []mcp.ToolOption{
			mcp.WithDescription("Update typed cells on Slack List items."),
			mcp.WithString("list_id", mcp.Required(), mcp.Description("Slack List ID containing the items (starts with F, e.g. F1234567890).")),
			mcp.WithArray("cells", mcp.Required(), mcp.MinItems(1), mcp.Items(listFieldItemSchema(true)), mcp.Description("Cells to set; each has column_id, row_id (the item ID), and one typed value key such as rich_text or select.")),
		}},
		{Name: capability.ListsItemDelete, Run: h.DeleteItem, Options: []mcp.ToolOption{
			mcp.WithOutputSchema[ListItemDeleteResult](),
			mcp.WithDescription("Preview or delete one Slack List item. Prepare first, then confirm the exact item before execute."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)),
			mcp.WithString("list_id", mcp.Required(), mcp.Description("Slack List ID containing the item (starts with F, e.g. F1234567890).")),
			mcp.WithString("item_id", mcp.Required(), mcp.Description("ID of the List item to delete, from lists_items_list or lists_items_create.")),
			mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
		}},
	}
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
			"type":              map[string]any{"type": "string", "enum": provider.ListColumnTypes},
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
