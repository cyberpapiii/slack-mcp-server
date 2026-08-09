package handler

import (
	"context"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestListsHandler(api ListsAPI) *ListsHandler {
	return NewListsHandler(api, approval.NewStore(5*time.Minute), userIdentity, zap.NewNop())
}

type fakeListsAPI struct {
	item        provider.ListItem
	page        provider.ListItemsPage
	getItemErr  error
	deleteErr   error
	deleteCalls int
}

func (f *fakeListsAPI) CreateList(_ context.Context, request provider.CreateListRequest) (string, provider.ListMetadata, error) {
	return "F1", provider.ListMetadata{ID: "F1", Name: request.Name}, nil
}
func (f *fakeListsAPI) UpdateList(context.Context, provider.UpdateListRequest) error { return nil }
func (f *fakeListsAPI) GetList(context.Context, string) (provider.ListMetadata, error) {
	return provider.ListMetadata{ID: "F1", Name: "Sprint"}, nil
}
func (f *fakeListsAPI) CreateItem(_ context.Context, request provider.CreateListItemRequest) (provider.ListItem, error) {
	return provider.ListItem{ID: "Rec1", ListID: request.ListID, Fields: request.InitialFields}, nil
}
func (f *fakeListsAPI) UpdateItems(context.Context, provider.UpdateListItemsRequest) error {
	return nil
}
func (f *fakeListsAPI) GetItem(context.Context, string, string) (provider.ListItem, error) {
	return f.item, f.getItemErr
}
func (f *fakeListsAPI) ListItems(context.Context, provider.ListItemsRequest) (provider.ListItemsPage, error) {
	return f.page, nil
}
func (f *fakeListsAPI) DeleteItem(context.Context, string, string) error {
	f.deleteCalls++
	return f.deleteErr
}

func listRequest(arguments map[string]any) mcp.CallToolRequest {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = arguments
	return request
}

func TestUnitListsHandlerReturnsTypedPagination(t *testing.T) {
	api := &fakeListsAPI{page: provider.ListItemsPage{
		Items:      []provider.ListItem{{ID: "Rec1", ListID: "F1", Fields: []provider.ListFieldValue{}}},
		NextCursor: "next",
	}}
	handler := newTestListsHandler(api)
	result, err := handler.ListItems(context.Background(), listRequest(map[string]any{"list_id": "F1", "limit": 25}))
	require.NoError(t, err)
	structured, ok := result.StructuredContent.(ToolResult[provider.ListItemsPage])
	require.True(t, ok)
	require.NotNil(t, structured.Data)
	assert.Equal(t, "next", structured.Meta.NextCursor)
	assert.Equal(t, "Rec1", structured.Data.Items[0].ID)
}

func TestUnitListsDeleteRequiresFreshExactPreview(t *testing.T) {
	t.Setenv("SLACK_MCP_LISTS_WRITE_TOOL", "true")
	item := provider.ListItem{
		ID: "Rec1", ListID: "F1", UpdatedTimestamp: "100.1",
		Fields: []provider.ListFieldValue{{ColumnID: "Col1", Text: "Before"}},
	}
	api := &fakeListsAPI{item: item}
	handler := newTestListsHandler(api)

	prepared, err := handler.PrepareDeleteItem(context.Background(), listRequest(map[string]any{"list_id": "F1", "item_id": "Rec1"}))
	require.NoError(t, err)
	preparedResult := prepared.StructuredContent.(ToolResult[ListItemDeleteData])
	require.NotNil(t, preparedResult.Data)
	assert.Equal(t, "Before", preparedResult.Data.Item.Fields[0].Text)

	executed, err := handler.DeleteItem(context.Background(), listRequest(map[string]any{"approval_token": preparedResult.Data.ApprovalToken, "list_id": "F1", "item_id": "Rec1"}))
	require.NoError(t, err)
	assert.False(t, executed.IsError)
	assert.Equal(t, 1, api.deleteCalls)

	replayed, err := handler.DeleteItem(context.Background(), listRequest(map[string]any{"approval_token": preparedResult.Data.ApprovalToken, "list_id": "F1", "item_id": "Rec1"}))
	require.NoError(t, err)
	assert.True(t, replayed.IsError)
	assert.Equal(t, 1, api.deleteCalls)
}

func TestUnitListsDeleteRejectsChangedOrExpiredStateWithoutMutation(t *testing.T) {
	t.Setenv("SLACK_MCP_LISTS_WRITE_TOOL", "true")
	item := provider.ListItem{ID: "Rec1", ListID: "F1", UpdatedTimestamp: "100.1", Fields: []provider.ListFieldValue{{ColumnID: "Col1", Text: "Before"}}}
	api := &fakeListsAPI{item: item}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	clock := now
	handler := NewListsHandler(api, approval.NewStoreWithClock(5*time.Minute, func() time.Time { return clock }), userIdentity, zap.NewNop())

	prepared, _ := handler.PrepareDeleteItem(context.Background(), listRequest(map[string]any{"list_id": "F1", "item_id": "Rec1"}))
	token := prepared.StructuredContent.(ToolResult[ListItemDeleteData]).Data.ApprovalToken
	api.item.Fields[0].Text = "Changed"
	conflict, err := handler.DeleteItem(context.Background(), listRequest(map[string]any{"approval_token": token, "list_id": "F1", "item_id": "Rec1"}))
	require.NoError(t, err)
	assert.True(t, conflict.IsError)
	assert.Contains(t, ResultText(conflict), "does not match")
	assert.Zero(t, api.deleteCalls)

	api.item = item
	prepared, _ = handler.PrepareDeleteItem(context.Background(), listRequest(map[string]any{"list_id": "F1", "item_id": "Rec1"}))
	token = prepared.StructuredContent.(ToolResult[ListItemDeleteData]).Data.ApprovalToken
	clock = now.Add(6 * time.Minute)
	expired, err := handler.DeleteItem(context.Background(), listRequest(map[string]any{"approval_token": token, "list_id": "F1", "item_id": "Rec1"}))
	require.NoError(t, err)
	assert.True(t, expired.IsError)
	assert.Zero(t, api.deleteCalls)
}

func TestUnitListsDeleteDoesNotRetryMutation(t *testing.T) {
	t.Setenv("SLACK_MCP_LISTS_WRITE_TOOL", "true")
	api := &fakeListsAPI{
		item:      provider.ListItem{ID: "Rec1", ListID: "F1", UpdatedTimestamp: "100.1", Fields: []provider.ListFieldValue{}},
		deleteErr: &provider.ListsAPIError{Kind: provider.ListsErrorRateLimit, RetryAfter: 5 * time.Second},
	}
	handler := newTestListsHandler(api)
	prepared, _ := handler.PrepareDeleteItem(context.Background(), listRequest(map[string]any{"list_id": "F1", "item_id": "Rec1"}))
	token := prepared.StructuredContent.(ToolResult[ListItemDeleteData]).Data.ApprovalToken

	result, err := handler.DeleteItem(context.Background(), listRequest(map[string]any{"approval_token": token, "list_id": "F1", "item_id": "Rec1"}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, 1, api.deleteCalls)
	assert.Contains(t, ResultText(result), "rate_limited")
}

func TestUnitListsHandlerMapsPlanScopeAndPermissionErrors(t *testing.T) {
	t.Setenv("SLACK_MCP_LISTS_WRITE_TOOL", "true")
	for _, kind := range []provider.ListsErrorKind{provider.ListsErrorUnavailable, provider.ListsErrorScope, provider.ListsErrorPermission} {
		api := &fakeListsAPI{getItemErr: &provider.ListsAPIError{Kind: kind, SlackCode: string(kind)}}
		handler := newTestListsHandler(api)
		result, err := handler.PrepareDeleteItem(context.Background(), listRequest(map[string]any{"list_id": "F1", "item_id": "Rec1"}))
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Contains(t, ResultText(result), string(kind))
	}
}

func TestUnitListsHandlerRejectsInvalidArgumentsBeforeAPI(t *testing.T) {
	t.Setenv("SLACK_MCP_LISTS_WRITE_TOOL", "true")
	handler := newTestListsHandler(&fakeListsAPI{})
	result, err := handler.CreateList(context.Background(), listRequest(map[string]any{"name": "", "schema": "bad"}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
