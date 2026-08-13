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

type fakeMessageFilesService struct {
	uploadCalls   int
	scheduleCalls int
	updateCalls   int
	deleteCalls   int
	lookupCalls   int
	updateErr     error
	snapshot      provider.MessageSnapshot
}

func (f *fakeMessageFilesService) Upload(_ context.Context, request provider.FileUploadRequest) (provider.UploadedFile, error) {
	f.uploadCalls++
	return provider.UploadedFile{FileID: "F1", Filename: request.Filename, Title: request.Title, ChannelID: request.ChannelID}, nil
}
func (f *fakeMessageFilesService) Schedule(_ context.Context, channelID, _ string, _ time.Time, _ string) (provider.MessageMutation, error) {
	f.scheduleCalls++
	return provider.MessageMutation{ChannelID: channelID, ScheduledMessageID: "Q1"}, nil
}
func (f *fakeMessageFilesService) Update(_ context.Context, channelID, timestamp, _ string) (provider.MessageMutation, error) {
	f.updateCalls++
	if f.updateErr != nil {
		return provider.MessageMutation{}, f.updateErr
	}
	return provider.MessageMutation{ChannelID: channelID, Timestamp: timestamp}, nil
}
func (f *fakeMessageFilesService) Delete(_ context.Context, channelID, timestamp string) (provider.MessageMutation, error) {
	f.deleteCalls++
	return provider.MessageMutation{ChannelID: channelID, Timestamp: timestamp}, nil
}
func (f *fakeMessageFilesService) GetMessage(context.Context, string, string) (provider.MessageSnapshot, error) {
	f.lookupCalls++
	return f.snapshot, nil
}

func messageFilesRequest(args map[string]any) mcp.CallToolRequest {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = args
	return request
}

func newTestMessageFilesHandler(service MessageFilesService) *MessageFilesHandler {
	return NewMessageFilesHandler(service, approval.NewStore(time.Minute), func() provider.ProviderIdentity {
		return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "user", TokenMode: "oauth"}
	}, zap.NewNop())
}

func TestUnitFilesUploadRejectsMultipleSourcesWithoutMutation(t *testing.T) {
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "files_upload")
	service := &fakeMessageFilesService{}
	result, err := newTestMessageFilesHandler(service).FilesUpload(context.Background(), messageFilesRequest(map[string]any{
		"filename": "a.txt", "content": "hello", "content_base64": "aGVsbG8=",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, service.uploadCalls)
}

func TestUnitMessagesScheduleValidatesThenReturnsTypedResult(t *testing.T) {
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "messages_schedule")
	service := &fakeMessageFilesService{}
	handler := newTestMessageFilesHandler(service)
	handler.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	result, err := handler.MessagesSchedule(context.Background(), messageFilesRequest(map[string]any{
		"channel_id": "C1", "text": "later", "post_at": "1800000060",
	}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	structured := result.StructuredContent.(ToolResult[MessageMutationData])
	require.NotNil(t, structured.Data)
	assert.Equal(t, "Q1", structured.Data.ScheduledMessageID)
	assert.Equal(t, "scheduled", structured.Data.Outcome)
	assert.Equal(t, 1, service.scheduleCalls)
}

func TestUnitMessagesDeletePrepareExecuteRevalidatesAndConsumesToken(t *testing.T) {
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "messages_delete")
	service := &fakeMessageFilesService{snapshot: provider.MessageSnapshot{ChannelID: "C1", Timestamp: "1.000001", Text: "remove", UserID: "U1"}}
	handler := newTestMessageFilesHandler(service)
	prepared, err := handler.MessagesDelete(context.Background(), messageFilesRequest(map[string]any{
		"action": "prepare", "channel_id": "C1", "timestamp": "1.000001",
	}))
	require.NoError(t, err)
	token := prepared.StructuredContent.(ToolResult[MessageMutationData]).Data.ApprovalToken

	executed, err := handler.MessagesDelete(context.Background(), messageFilesRequest(map[string]any{
		"action": "execute", "channel_id": "C1", "timestamp": "1.000001", "approval_token": token,
	}))
	require.NoError(t, err)
	assert.False(t, executed.IsError)
	assert.Equal(t, 2, service.lookupCalls)
	assert.Equal(t, 1, service.deleteCalls)

	replayed, err := handler.MessagesDelete(context.Background(), messageFilesRequest(map[string]any{
		"action": "execute", "channel_id": "C1", "timestamp": "1.000001", "approval_token": token,
	}))
	require.NoError(t, err)
	assert.True(t, replayed.IsError)
	assert.Equal(t, 1, service.deleteCalls)
}

func TestUnitMessagesUpdateTimeoutIsOutcomeUnknownAndNeverRetried(t *testing.T) {
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "messages_update")
	service := &fakeMessageFilesService{updateErr: context.DeadlineExceeded}
	result, err := newTestMessageFilesHandler(service).MessagesUpdate(context.Background(), messageFilesRequest(map[string]any{
		"channel_id": "C1", "timestamp": "1.000001", "text": "changed",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	structured := result.StructuredContent.(ToolResult[struct{}])
	require.NotNil(t, structured.Error)
	assert.Equal(t, "outcome_unknown", structured.Error.Code)
	assert.Equal(t, 1, service.updateCalls)
}

func TestUnitMessagesUpdateRejectsInvalidTimestamp(t *testing.T) {
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "messages_update")
	service := &fakeMessageFilesService{updateErr: errors.New("must not run")}
	result, err := newTestMessageFilesHandler(service).MessagesUpdate(context.Background(), messageFilesRequest(map[string]any{
		"channel_id": "C1", "timestamp": "bad", "text": "changed",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, service.updateCalls)
}

func TestUnitRequireMessageLifecycleToolHonorsChannelAllowlist(t *testing.T) {
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "")
	t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C1,C2")

	require.NoError(t, requireMessageLifecycleTool("messages_schedule", "C1"))
	err := requireMessageLifecycleTool("messages_delete", "C9")
	require.Error(t, err)
	var toolErr *ToolError
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "permission_denied", toolErr.Code)

	t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
	err = requireMessageLifecycleTool("messages_update", "C1")
	require.Error(t, err)
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "tool_disabled", toolErr.Code)
}

func TestUnitMessagesScheduleUsesAddMessageAllowlistWithoutEnabledTools(t *testing.T) {
	t.Setenv("SLACK_MCP_ENABLED_TOOLS", "")
	t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C1")
	service := &fakeMessageFilesService{}
	handler := newTestMessageFilesHandler(service)
	handler.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }

	ok, err := handler.MessagesSchedule(context.Background(), messageFilesRequest(map[string]any{
		"channel_id": "C1", "text": "later", "post_at": "1800000060",
	}))
	require.NoError(t, err)
	require.False(t, ok.IsError)
	assert.Equal(t, 1, service.scheduleCalls)

	denied, err := handler.MessagesSchedule(context.Background(), messageFilesRequest(map[string]any{
		"channel_id": "C9", "text": "later", "post_at": "1800000060",
	}))
	require.NoError(t, err)
	require.True(t, denied.IsError)
	assert.Equal(t, 1, service.scheduleCalls)
}
