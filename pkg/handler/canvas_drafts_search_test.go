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

func capabilityRequest(arguments map[string]any) mcp.CallToolRequest {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = arguments
	return request
}

type fakeCanvasAPI struct {
	created provider.CanvasCreateRequest
	updated provider.CanvasUpdateRequest
	doc     provider.CanvasDocument
	err     error
}

func (f *fakeCanvasAPI) CreateCanvas(_ context.Context, request provider.CanvasCreateRequest) (string, error) {
	f.created = request
	return "F1", f.err
}
func (f *fakeCanvasAPI) ReadCanvas(context.Context, provider.CanvasReadRequest) (provider.CanvasDocument, error) {
	return f.doc, f.err
}
func (f *fakeCanvasAPI) UpdateCanvas(_ context.Context, request provider.CanvasUpdateRequest) error {
	f.updated = request
	return f.err
}

func TestCanvasHandlersValidateAndReturnPartialRead(t *testing.T) {
	api := &fakeCanvasAPI{doc: provider.CanvasDocument{CanvasID: "F1", Preview: "preview", Limitation: "metadata only"}}
	handler := NewCanvasHandler(api, zap.NewNop())

	created, err := handler.Create(context.Background(), capabilityRequest(map[string]any{"title": "Plan", "markdown": "# Plan"}))
	require.NoError(t, err)
	assert.False(t, created.IsError)
	assert.Equal(t, "F1", created.StructuredContent.(ToolResult[CanvasMutationData]).Data.CanvasID)

	read, err := handler.Read(context.Background(), capabilityRequest(map[string]any{"canvas_id": "F1"}))
	require.NoError(t, err)
	readResult := read.StructuredContent.(ToolResult[provider.CanvasDocument])
	assert.True(t, readResult.Meta.Partial)
	assert.Equal(t, "metadata only", readResult.Meta.PartialReason)

	replaced, err := handler.Update(context.Background(), capabilityRequest(map[string]any{"canvas_id": "F1", "changes": []any{map[string]any{"operation": "replace", "markdown": "x"}}}))
	require.NoError(t, err)
	assert.False(t, replaced.IsError)

	invalid, err := handler.Update(context.Background(), capabilityRequest(map[string]any{"canvas_id": "F1", "changes": []any{map[string]any{"operation": "insert_before", "markdown": "x"}}}))
	require.NoError(t, err)
	assert.True(t, invalid.IsError)
	assert.Equal(t, "invalid_arguments", invalid.StructuredContent.(ToolResult[struct{}]).Error.Code)
}

type fakeDraftsAPI struct {
	draft       provider.Draft
	nextCursor  string
	deletedID   string
	unsupported bool
}

func (f *fakeDraftsAPI) ListDrafts(context.Context, string, int) (provider.DraftPage, error) {
	if f.unsupported {
		return provider.DraftPage{}, provider.ErrPersistedDraftsUnsupported
	}
	return provider.DraftPage{Drafts: []provider.Draft{f.draft}, NextCursor: f.nextCursor}, nil
}
func (f *fakeDraftsAPI) GetDraft(context.Context, string) (provider.Draft, error) {
	if f.unsupported {
		return provider.Draft{}, provider.ErrPersistedDraftsUnsupported
	}
	return f.draft, nil
}
func (f *fakeDraftsAPI) CreateDraft(context.Context, provider.Draft) (provider.Draft, error) {
	return f.draft, nil
}
func (f *fakeDraftsAPI) UpdateDraft(context.Context, provider.Draft) (provider.Draft, error) {
	return f.draft, nil
}
func (f *fakeDraftsAPI) DeleteDraft(_ context.Context, id, _ string) error {
	f.deletedID = id
	return nil
}

func TestDraftsUnsupportedIsTyped(t *testing.T) {
	handler := NewDraftsHandler(&fakeDraftsAPI{unsupported: true}, approval.NewStore(time.Minute), func() provider.ProviderIdentity {
		return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "user"}
	}, zap.NewNop())
	result, err := handler.List(context.Background(), capabilityRequest(map[string]any{"limit": 20}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, "unsupported", result.StructuredContent.(ToolResult[struct{}]).Error.Code)
}

func TestDraftsListReturnsCSVWithChannelsLegendAndNextCursor(t *testing.T) {
	api := &fakeDraftsAPI{
		draft:      provider.Draft{ID: "D1", ChannelID: "C1", ThreadTS: "1723456789.000100", Text: "hello, world", UpdatedTS: "1723456789.000200"},
		nextCursor: "page2",
	}
	handler := NewDraftsHandler(api, approval.NewStore(time.Minute), nil, zap.NewNop())
	handler.ChannelName = func(id string) string {
		if id == "C1" {
			return "#general"
		}
		return ""
	}
	result, err := handler.List(context.Background(), capabilityRequest(map[string]any{"limit": 20}))
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)
	assert.Equal(t, "#channels: C1=#general\n#next_cursor: page2\nDraftID,Channel,ThreadTs,Updated,Text\nD1,C1,1723456789.000100,2024-08-12T09:59:49Z,\"hello, world\"\n", ResultText(result))
}

func TestDraftsListWithoutChannelResolverOmitsLegend(t *testing.T) {
	api := &fakeDraftsAPI{draft: provider.Draft{ID: "D1", ChannelID: "C1", Text: "draft"}}
	handler := NewDraftsHandler(api, approval.NewStore(time.Minute), nil, zap.NewNop())
	result, err := handler.List(context.Background(), capabilityRequest(map[string]any{}))
	require.NoError(t, err)
	assert.Equal(t, "DraftID,Channel,ThreadTs,Updated,Text\nD1,C1,,,draft\n", ResultText(result))
}

func TestDraftDeleteRequiresBoundOneUseApproval(t *testing.T) {
	api := &fakeDraftsAPI{draft: provider.Draft{ID: "D1", ChannelID: "C1", Text: "draft"}}
	handler := NewDraftsHandler(api, approval.NewStore(time.Minute), func() provider.ProviderIdentity {
		return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "user"}
	}, zap.NewNop())

	prepared, err := handler.Delete(context.Background(), capabilityRequest(map[string]any{"action": "prepare", "draft_id": "D1"}))
	require.NoError(t, err)
	preparedData := prepared.StructuredContent.(ToolResult[DraftMutationData]).Data
	require.NotEmpty(t, preparedData.ApprovalToken)
	assert.Empty(t, api.deletedID)

	executed, err := handler.Delete(context.Background(), capabilityRequest(map[string]any{"action": "execute", "draft_id": "D1", "approval_token": preparedData.ApprovalToken}))
	require.NoError(t, err)
	assert.False(t, executed.IsError)
	assert.Equal(t, "D1", api.deletedID)

	replayed, err := handler.Delete(context.Background(), capabilityRequest(map[string]any{"action": "execute", "draft_id": "D1", "approval_token": preparedData.ApprovalToken}))
	require.NoError(t, err)
	assert.True(t, replayed.IsError)
	assert.Equal(t, "approval_invalid", replayed.StructuredContent.(ToolResult[struct{}]).Error.Code)
}

type fakeSemanticAPI struct {
	request provider.SemanticSearchRequest
	items   []provider.SemanticSearchItem
}

func (f *fakeSemanticAPI) SearchSemantic(_ context.Context, request provider.SemanticSearchRequest) (provider.SemanticSearchPage, error) {
	f.request = request
	return provider.SemanticSearchPage{Items: f.items, NextCursor: "next"}, nil
}

func TestSemanticSearchHandlerSupportsMessageAndFileFilters(t *testing.T) {
	api := &fakeSemanticAPI{items: []provider.SemanticSearchItem{
		{Kind: "message", AuthorUserID: "U1", ChannelID: "C1", MessageTS: "1723456789.000200", Content: "launch plan draft"},
		{Kind: "file", AuthorUserID: "U2", FileID: "F1", Title: "Launch plan", FileType: "canvas", Content: "plan, v2"},
	}}
	handler := NewSemanticSearchHandler(api, zap.NewNop())
	handler.ChannelName = func(id string) string { return map[string]string{"C1": "#launch"}[id] }
	result, err := handler.Search(context.Background(), capabilityRequest(map[string]any{"query": "launch plan", "content_types": []any{"messages", "files"}}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Equal(t, 20, api.request.Limit)
	assert.Nil(t, result.StructuredContent)
	assert.Equal(t, "#channels: C1=#launch\n#next_cursor: next\n"+
		"Type,UserID,Channel,Text,Time,MsgID,FileID,Title,FileType\n"+
		"message,U1,C1,launch plan draft,2024-08-12T09:59:49Z,1723456789.000200,,,\n"+
		"file,U2,,\"plan, v2\",,,F1,Launch plan,canvas\n", ResultText(result))

	invalid, err := handler.Search(context.Background(), capabilityRequest(map[string]any{"query": "x", "content_types": []any{"canvases"}}))
	require.NoError(t, err)
	assert.True(t, invalid.IsError)
}

func TestSemanticSearchMessagesOnlyUsesHistoryShapedColumns(t *testing.T) {
	api := &fakeSemanticAPI{items: []provider.SemanticSearchItem{
		{Kind: "message", AuthorUserID: "U1", ChannelID: "C1", MessageTS: "1723456789.000200", Content: "match"},
	}}
	handler := NewSemanticSearchHandler(api, zap.NewNop())
	result, err := handler.Search(context.Background(), capabilityRequest(map[string]any{"query": "match", "content_types": []any{"messages"}}))
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)
	assert.Equal(t, "#next_cursor: next\nUserID,Channel,Text,Time,MsgID\nU1,C1,match,2024-08-12T09:59:49Z,1723456789.000200\n", ResultText(result))
}
