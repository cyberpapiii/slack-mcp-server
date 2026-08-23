package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestChannelMutationHandler(service ChannelMutationService) *ChannelMutationHandler {
	return NewChannelMutationHandler(service, approval.NewStore(time.Minute), userIdentity, zap.NewNop())
}

type fakeChannelMutationService struct {
	action             string
	channelID          string
	value              string
	prepareCalls       int
	archiveCalls       int
	archivePreparation provider.ArchivePreparation
	err                error
	archiveErr         error
}

func (f *fakeChannelMutationService) result(action, channelID, value string) (provider.ChannelMutationState, error) {
	f.action, f.channelID, f.value = action, channelID, value
	return provider.ChannelMutationState{ChannelID: channelID, Name: value}, f.err
}
func (f *fakeChannelMutationService) Rename(_ context.Context, channelID, value string) (provider.ChannelMutationState, error) {
	return f.result("rename", channelID, value)
}
func (f *fakeChannelMutationService) SetTopic(_ context.Context, channelID, value string) (provider.ChannelMutationState, error) {
	state, err := f.result("topic", channelID, value)
	state.Topic = value
	return state, err
}
func (f *fakeChannelMutationService) SetPurpose(_ context.Context, channelID, value string) (provider.ChannelMutationState, error) {
	state, err := f.result("purpose", channelID, value)
	state.Purpose = value
	return state, err
}
func (f *fakeChannelMutationService) PrepareArchive(_ context.Context, channelID string) (provider.ArchivePreparation, error) {
	f.prepareCalls++
	f.archivePreparation = provider.ArchivePreparation{Expected: provider.ChannelMutationState{ChannelID: channelID}}
	return f.archivePreparation, f.err
}
func (f *fakeChannelMutationService) ArchivePrepared(_ context.Context, preparation provider.ArchivePreparation) (provider.ChannelMutationState, error) {
	f.archiveCalls++
	f.archivePreparation = preparation
	state := preparation.Expected
	state.Archived = true
	return state, f.archiveErr
}

func mutationRequest(arguments map[string]any) mcp.CallToolRequest {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = arguments
	return request
}

func TestChannelMetadataHandlersSupportDistinctActionsAndExplicitClearing(t *testing.T) {
	t.Setenv(channelManagementGate, "true")
	tests := []struct {
		name   string
		args   map[string]any
		call   func(*ChannelMutationHandler, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		action string
		value  string
	}{
		{"rename", map[string]any{"channel_id": "C123", "name": "new-name"}, func(h *ChannelMutationHandler, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return h.ConversationsRenameHandler(context.Background(), r)
		}, "rename", "new-name"},
		{"clear topic", map[string]any{"channel_id": "C123", "topic": ""}, func(h *ChannelMutationHandler, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return h.ConversationsSetTopicHandler(context.Background(), r)
		}, "topic", ""},
		{"clear purpose", map[string]any{"channel_id": "C123", "purpose": ""}, func(h *ChannelMutationHandler, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return h.ConversationsSetPurposeHandler(context.Background(), r)
		}, "purpose", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeChannelMutationService{}
			result, err := tt.call(newTestChannelMutationHandler(service), mutationRequest(tt.args))
			require.NoError(t, err)
			assert.Equal(t, tt.action, service.action)
			assert.Equal(t, tt.value, service.value)
			assert.Contains(t, ResultText(result), `"phase":"executed"`)
			structured, ok := result.StructuredContent.(ToolResult[ChannelMutationData])
			require.True(t, ok)
			assert.Equal(t, "C123", structured.Data.Channel.ChannelID)
		})
	}
}

func TestChannelMetadataHandlerRequiresFieldPresenceButAllowsEmptyClear(t *testing.T) {
	t.Setenv(channelManagementGate, "true")
	service := &fakeChannelMutationService{}
	handler := newTestChannelMutationHandler(service)
	_, err := handler.ConversationsSetTopicHandler(context.Background(), mutationRequest(map[string]any{"channel_id": "C123"}))
	require.ErrorContains(t, err, "pass an empty string to clear")
	assert.Empty(t, service.action)
}

func TestChannelMetadataHandlerAcceptsPrivateChannelID(t *testing.T) {
	t.Setenv(channelManagementGate, "true")
	service := &fakeChannelMutationService{}
	_, err := newTestChannelMutationHandler(service).ConversationsRenameHandler(
		context.Background(), mutationRequest(map[string]any{"channel_id": "G123", "name": "private-name"}),
	)
	require.NoError(t, err)
	assert.Equal(t, "G123", service.channelID)
}

func TestChannelMutationHandlerRechecksChannelAllowlist(t *testing.T) {
	t.Setenv(channelManagementGate, "C456")
	service := &fakeChannelMutationService{}
	_, err := newTestChannelMutationHandler(service).ConversationsRenameHandler(
		context.Background(), mutationRequest(map[string]any{"channel_id": "C123", "name": "new-name"}),
	)
	require.ErrorContains(t, err, "not allowed for channel")
	assert.Empty(t, service.action)
}

func TestChannelMutationHandlerEmptyAllowlistAllowsAllChannels(t *testing.T) {
	t.Setenv(channelManagementGate, "")
	service := &fakeChannelMutationService{}
	_, err := newTestChannelMutationHandler(service).ConversationsRenameHandler(
		context.Background(), mutationRequest(map[string]any{"channel_id": "C123", "name": "new-name"}),
	)
	require.NoError(t, err)
	assert.Equal(t, "rename", service.action)
}

func TestArchiveHandlerUsesPrepareThenExactArchiveSeam(t *testing.T) {
	t.Setenv(channelManagementGate, "true")
	service := &fakeChannelMutationService{}
	handler := newTestChannelMutationHandler(service)
	prepared, err := handler.ConversationsArchiveHandler(
		context.Background(), mutationRequest(map[string]any{"channel_id": "C123", "action": "prepare"}),
	)
	require.NoError(t, err)
	preview := prepared.StructuredContent.(ToolResult[ChannelMutationData])
	require.NotEmpty(t, preview.Data.ApprovalToken)
	assert.Zero(t, service.archiveCalls)

	result, err := handler.ConversationsArchiveHandler(
		context.Background(), mutationRequest(map[string]any{"channel_id": "C123", "action": "execute", "approval_token": preview.Data.ApprovalToken}),
	)
	require.NoError(t, err)
	assert.Equal(t, 2, service.prepareCalls)
	assert.Equal(t, 1, service.archiveCalls)
	assert.Equal(t, "C123", service.archivePreparation.Expected.ChannelID)
	assert.Contains(t, ResultText(result), "archive")
}

func TestArchiveHandlerDoesNotCallArchiveWhenPreparationFails(t *testing.T) {
	t.Setenv(channelManagementGate, "true")
	service := &fakeChannelMutationService{err: errors.New("state unavailable")}
	_, err := newTestChannelMutationHandler(service).ConversationsArchiveHandler(
		context.Background(), mutationRequest(map[string]any{"channel_id": "C123"}),
	)
	require.ErrorContains(t, err, "state unavailable")
	assert.Equal(t, 1, service.prepareCalls)
	assert.Zero(t, service.archiveCalls)
}

func TestArchiveHandlerTimeoutIsOutcomeUnknown(t *testing.T) {
	t.Setenv(channelManagementGate, "true")
	service := &fakeChannelMutationService{}
	handler := newTestChannelMutationHandler(service)
	prepared, err := handler.ConversationsArchiveHandler(context.Background(), mutationRequest(map[string]any{"channel_id": "C123", "action": "prepare"}))
	require.NoError(t, err)
	token := prepared.StructuredContent.(ToolResult[ChannelMutationData]).Data.ApprovalToken
	service.archiveErr = context.DeadlineExceeded
	_, err = handler.ConversationsArchiveHandler(context.Background(), mutationRequest(map[string]any{"channel_id": "C123", "action": "execute", "approval_token": token}))
	var typed *ToolError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "outcome_unknown", typed.Code)
}

func TestRenameValidationRejectsUnsafeNames(t *testing.T) {
	t.Setenv(channelManagementGate, "true")
	for _, name := range []string{"", "#general", "two words", "one,two", "Uppercase"} {
		service := &fakeChannelMutationService{}
		_, err := newTestChannelMutationHandler(service).ConversationsRenameHandler(
			context.Background(), mutationRequest(map[string]any{"channel_id": "C123", "name": name}),
		)
		require.Error(t, err)
		assert.Empty(t, service.action)
	}
}
