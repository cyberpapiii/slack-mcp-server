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
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "canvases_create,canvases_update")
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
	deletedID   string
	unsupported bool
}

func (f *fakeDraftsAPI) ListDrafts(context.Context, string, int) (provider.DraftPage, error) {
	if f.unsupported {
		return provider.DraftPage{}, provider.ErrPersistedDraftsUnsupported
	}
	return provider.DraftPage{Drafts: []provider.Draft{f.draft}}, nil
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

func TestDraftDeleteRequiresBoundOneUseApproval(t *testing.T) {
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "drafts_delete")
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
}

func (f *fakeSemanticAPI) SearchSemantic(_ context.Context, request provider.SemanticSearchRequest) (provider.SemanticSearchPage, error) {
	f.request = request
	return provider.SemanticSearchPage{Items: []provider.SemanticSearchItem{{Content: "match"}}, NextCursor: "next"}, nil
}

func TestSemanticSearchHandlerSupportsMessageAndFileFilters(t *testing.T) {
	api := &fakeSemanticAPI{}
	handler := NewSemanticSearchHandler(api, zap.NewNop())
	result, err := handler.Search(context.Background(), capabilityRequest(map[string]any{"query": "launch plan", "content_types": []any{"messages", "files"}}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Equal(t, 20, api.request.Limit)
	assert.Equal(t, "next", result.StructuredContent.(ToolResult[provider.SemanticSearchPage]).Meta.NextCursor)

	invalid, err := handler.Search(context.Background(), capabilityRequest(map[string]any{"query": "x", "content_types": []any{"canvases"}}))
	require.NoError(t, err)
	assert.True(t, invalid.IsError)
}
