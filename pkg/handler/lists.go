package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
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
	approvals *approval.Store
	identity  func() provider.ProviderIdentity
	logger    *zap.Logger
}

func NewListsHandler(api ListsAPI, approvals *approval.Store, identity func() provider.ProviderIdentity, logger *zap.Logger) *ListsHandler {
	return &ListsHandler{api: api, approvals: approvals, identity: identityFunc(identity), logger: logger}
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
	ItemID string `json:"item_id,omitempty"`
	Status string `json:"status"`
}

type ListItemDeleteData struct {
	Phase         string             `json:"phase"`
	ApprovalToken string             `json:"approval_token,omitempty"`
	ListID        string             `json:"list_id"`
	ItemID        string             `json:"item_id"`
	Item          *provider.ListItem `json:"item,omitempty"`
	ExpiresAt     string             `json:"expires_at,omitempty"`
	Status        string             `json:"status"`
}

type ListItemDeleteResult = ToolResult[ListItemDeleteData]

func (h *ListsHandler) CreateList(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ListsCreateList called", request)
	var input provider.CreateListRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	if input.Name == "" {
		return NewTypedErrorResult(errors.New("name is required")), nil
	}
	listID, metadata, err := h.api.CreateList(ctx, input)
	if err != nil {
		return NewTypedErrorResult(listToolError(err)), nil
	}
	return NewStructuredResult(ListCreateData{ListID: listID, Metadata: metadata}, SlackResultMeta("", false, ""), "Created Slack List "+listID), nil
}

func (h *ListsHandler) UpdateList(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ListsUpdateList called", request)
	var input provider.UpdateListRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	if input.ID == "" {
		return NewTypedErrorResult(errors.New("id is required")), nil
	}
	if err := h.api.UpdateList(ctx, input); err != nil {
		return NewTypedErrorResult(listToolError(err)), nil
	}
	return NewStructuredResult(ListMutationData{ListID: input.ID, Status: "updated"}, SlackResultMeta("", false, ""), "Updated Slack List "+input.ID), nil
}

func (h *ListsHandler) ListItems(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ListsListItems called", request)
	var input provider.ListItemsRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	if input.ListID == "" {
		return NewTypedErrorResult(errors.New("list_id is required")), nil
	}
	if input.Limit < 0 || input.Limit > 200 {
		return NewTypedErrorResult(errors.New("limit must be between 0 and 200")), nil
	}
	page, err := h.api.ListItems(ctx, input)
	if err != nil {
		return NewTypedErrorResult(listToolError(err)), nil
	}
	return NewStructuredResult(page, SlackResultMeta(page.NextCursor, false, ""), fallbackJSON(page)), nil
}

func (h *ListsHandler) CreateItem(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ListsCreateItem called", request)
	var input provider.CreateListItemRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	if input.ListID == "" {
		return NewTypedErrorResult(errors.New("list_id is required")), nil
	}
	item, err := h.api.CreateItem(ctx, input)
	if err != nil {
		return NewTypedErrorResult(listToolError(err)), nil
	}
	return NewStructuredResult(item, SlackResultMeta("", false, ""), "Created Slack List item "+item.ID), nil
}

func (h *ListsHandler) UpdateItems(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ListsUpdateItems called", request)
	var input provider.UpdateListItemsRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	if input.ListID == "" || len(input.Cells) == 0 {
		return NewTypedErrorResult(errors.New("list_id and at least one cell are required")), nil
	}
	if err := h.api.UpdateItems(ctx, input); err != nil {
		return NewTypedErrorResult(listToolError(err)), nil
	}
	return NewStructuredResult(ListItemMutationData{ListID: input.ListID, Status: "updated"}, SlackResultMeta("", false, ""), "Updated Slack List items"), nil
}

func (h *ListsHandler) PrepareDeleteItem(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ListsPrepareDeleteItem called", request)
	listID := request.GetString("list_id", "")
	itemID := request.GetString("item_id", "")
	if listID == "" || itemID == "" {
		return NewTypedErrorResult(errors.New("list_id and item_id are required")), nil
	}
	item, err := h.api.GetItem(ctx, listID, itemID)
	if err != nil {
		return NewTypedErrorResult(listToolError(err)), nil
	}
	binding, err := listDeleteBinding(h.identity(), listID, itemID, item)
	if err != nil {
		return nil, err
	}
	prepared, _, err := prepareOrExecute(h.approvals, "prepare", "", binding)
	if err != nil {
		return nil, err
	}
	data := ListItemDeleteData{
		Phase: "prepared", ApprovalToken: prepared.Token, ListID: listID, ItemID: itemID, Item: &item,
		ExpiresAt: prepared.ExpiresAt.Format(time.RFC3339), Status: "awaiting_confirmation",
	}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), "Prepared deletion of Slack List item "+itemID), nil
}

func (h *ListsHandler) DeleteItem(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ListsDeleteItem called", request)
	token := strings.TrimSpace(request.GetString("approval_token", ""))
	listID := strings.TrimSpace(request.GetString("list_id", ""))
	itemID := strings.TrimSpace(request.GetString("item_id", ""))
	if token == "" || listID == "" || itemID == "" {
		return NewTypedErrorResult(&ToolError{Code: "approval_required", Message: "approval_token, list_id, and item_id are required"}), nil
	}
	current, err := h.api.GetItem(ctx, listID, itemID)
	if err != nil {
		return NewTypedErrorResult(listToolError(err)), nil
	}
	binding, err := listDeleteBinding(h.identity(), listID, itemID, current)
	if err != nil {
		return nil, err
	}
	if _, _, err := prepareOrExecute(h.approvals, "execute", token, binding); err != nil {
		return NewTypedErrorResult(err), nil
	}
	if err := h.api.DeleteItem(ctx, listID, itemID); err != nil {
		return NewTypedErrorResult(listToolError(err)), nil
	}
	data := ListItemDeleteData{Phase: "executed", ListID: listID, ItemID: itemID, Status: "deleted"}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), "Deleted Slack List item "+itemID), nil
}

func listDeleteBinding(identity provider.ProviderIdentity, listID, itemID string, item provider.ListItem) (approval.Binding, error) {
	if identity.TeamID == "" || identity.UserID == "" || identity.ActorType != "user" {
		return approval.Binding{}, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	arguments, err := approval.CanonicalJSON(struct {
		ListID string `json:"list_id"`
		ItemID string `json:"item_id"`
	}{ListID: listID, ItemID: itemID})
	if err != nil {
		return approval.Binding{}, err
	}
	observed, err := approval.CanonicalJSON(item)
	if err != nil {
		return approval.Binding{}, err
	}
	return approval.Binding{TeamID: identity.TeamID, UserID: identity.UserID, Provider: "local", Tool: "lists_item_delete", Arguments: arguments, ObservedState: observed}, nil
}

func listToolError(err error) error {
	var apiError *provider.ListsAPIError
	if !errors.As(err, &apiError) {
		return err
	}
	if apiError.MayHaveMutated {
		return &ToolError{Code: "outcome_unknown", Message: "Slack may have applied the List mutation; read current List state before another attempt", Cause: err}
	}
	return &ToolError{
		Code: string(apiError.Kind), Message: apiError.Error(), Retryable: apiError.Retryable(),
		RetryAfter: apiError.RetryAfter, Cause: err,
	}
}
