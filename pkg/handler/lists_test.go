package handler

import (
	"context"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestListsHandler(api ListsAPI) *ListsHandler {
	return NewListsHandler(api, approval.NewStore(), userIdentity, zap.NewNop())
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
	result, err := served(handler.ListItems(context.Background(), listRequest(map[string]any{"list_id": "F1", "limit": 25})))
	require.NoError(t, err)
	structured, ok := result.StructuredContent.(ToolResult[provider.ListItemsPage])
	require.True(t, ok)
	require.NotNil(t, structured.Data)
	assert.Equal(t, "next", structured.Meta.NextCursor)
	assert.Equal(t, "Rec1", structured.Data.Items[0].ID)
	fallback := ResultText(result)
	assert.Contains(t, fallback, `"id":"Rec1"`)
	assert.Contains(t, fallback, `"list_id":"F1"`)
	assert.Contains(t, fallback, `"next_cursor":"next"`)
}

func TestUnitListsDeleteRequiresFreshExactPreview(t *testing.T) {
	item := provider.ListItem{
		ID: "Rec1", ListID: "F1", UpdatedTimestamp: "100.1",
		Fields: []provider.ListFieldValue{{ColumnID: "Col1", Text: "Before"}},
	}
	api := &fakeListsAPI{item: item}
	handler := newTestListsHandler(api)

	prepared, err := served(handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "prepare", "list_id": "F1", "item_id": "Rec1"})))
	require.NoError(t, err)
	preparedResult := prepared.StructuredContent.(ToolResult[ListItemDeleteData])
	require.NotNil(t, preparedResult.Data)
	assert.Equal(t, "Before", preparedResult.Data.Item.Fields[0].Text)

	executed, err := served(handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "execute", "approval_token": preparedResult.Data.ApprovalToken, "list_id": "F1", "item_id": "Rec1"})))
	require.NoError(t, err)
	assert.False(t, executed.IsError)
	assert.Equal(t, 1, api.deleteCalls)

	replayed, err := served(handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "execute", "approval_token": preparedResult.Data.ApprovalToken, "list_id": "F1", "item_id": "Rec1"})))
	require.NoError(t, err)
	assert.True(t, replayed.IsError)
	assert.Equal(t, 1, api.deleteCalls)
}

func TestUnitListsDeleteRejectsChangedOrExpiredStateWithoutMutation(t *testing.T) {
	item := provider.ListItem{ID: "Rec1", ListID: "F1", UpdatedTimestamp: "100.1", Fields: []provider.ListFieldValue{{ColumnID: "Col1", Text: "Before"}}}
	api := &fakeListsAPI{item: item}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	clock := now
	handler := NewListsHandler(api, approval.NewStoreWithClock(func() time.Time { return clock }), userIdentity, zap.NewNop())

	prepared, _ := handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "prepare", "list_id": "F1", "item_id": "Rec1"}))
	token := prepared.StructuredContent.(ToolResult[ListItemDeleteData]).Data.ApprovalToken
	api.item.Fields[0].Text = "Changed"
	conflict, err := served(handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "execute", "approval_token": token, "list_id": "F1", "item_id": "Rec1"})))
	require.NoError(t, err)
	assert.True(t, conflict.IsError)
	assert.Contains(t, ResultText(conflict), "does not match")
	assert.Zero(t, api.deleteCalls)

	api.item = item
	prepared, _ = handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "prepare", "list_id": "F1", "item_id": "Rec1"}))
	token = prepared.StructuredContent.(ToolResult[ListItemDeleteData]).Data.ApprovalToken
	clock = now.Add(6 * time.Minute)
	expired, err := served(handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "execute", "approval_token": token, "list_id": "F1", "item_id": "Rec1"})))
	require.NoError(t, err)
	assert.True(t, expired.IsError)
	assert.Zero(t, api.deleteCalls)
}

func TestUnitListsDeleteDoesNotRetryMutation(t *testing.T) {
	api := &fakeListsAPI{
		item:      provider.ListItem{ID: "Rec1", ListID: "F1", UpdatedTimestamp: "100.1", Fields: []provider.ListFieldValue{}},
		deleteErr: &slack.RateLimitedError{RetryAfter: 5 * time.Second},
	}
	handler := newTestListsHandler(api)
	prepared, _ := handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "prepare", "list_id": "F1", "item_id": "Rec1"}))
	token := prepared.StructuredContent.(ToolResult[ListItemDeleteData]).Data.ApprovalToken

	result, err := served(handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "execute", "approval_token": token, "list_id": "F1", "item_id": "Rec1"})))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, 1, api.deleteCalls)
	assert.Equal(t, "rate_limited", ErrorCode(result))
}

func TestUnitListsHandlerMapsPlanScopeAndPermissionErrors(t *testing.T) {
	for slackCode, code := range map[string]string{"plan_upgrade_required": "paid_plan_unavailable", "missing_scope": "permission_denied", "no_permission": "permission_denied"} {
		api := &fakeListsAPI{getItemErr: slack.SlackErrorResponse{Err: slackCode}}
		handler := newTestListsHandler(api)
		result, err := served(handler.DeleteItem(context.Background(), listRequest(map[string]any{"action": "prepare", "list_id": "F1", "item_id": "Rec1"})))
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Equal(t, code, ErrorCode(result), slackCode)
	}
}

func TestUnitListsHandlerRejectsInvalidArgumentsBeforeAPI(t *testing.T) {
	handler := newTestListsHandler(&fakeListsAPI{})
	result, err := served(handler.CreateList(context.Background(), listRequest(map[string]any{"name": "", "schema": "bad"})))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
