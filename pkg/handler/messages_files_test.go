package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMessageFilesService struct {
	uploadCalls   int
	scheduleCalls int
	updateCalls   int
	deleteCalls   int
	lookupCalls   int
	updateErr     error
	message       slack.Message
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
func (f *fakeMessageFilesService) GetMessage(context.Context, string, string) (slack.Message, error) {
	f.lookupCalls++
	return f.message, nil
}

func newTestMessageFilesHandler(service MessageFilesService) *MessageFilesHandler {
	return NewMessageFilesHandler(service, approval.NewStore(), func() provider.ProviderIdentity {
		return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "user", TokenMode: "oauth"}
	}, envSettings())
}

func TestUnitFilesUploadRejectsMultipleSourcesWithoutMutation(t *testing.T) {
	service := &fakeMessageFilesService{}
	result, err := served(newTestMessageFilesHandler(service).FilesUpload(context.Background(), toolRequest(map[string]any{
		"filename": "a.txt", "content": "hello", "content_base64": "aGVsbG8=",
	})))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, service.uploadCalls)
}

func TestUnitMessagesScheduleValidatesThenReturnsTypedResult(t *testing.T) {
	service := &fakeMessageFilesService{}
	handler := newTestMessageFilesHandler(service)
	handler.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	result, err := served(handler.MessagesSchedule(context.Background(), toolRequest(map[string]any{
		"channel_id": "C1", "text": "later", "post_at": "1800000060",
	})))
	require.NoError(t, err)
	require.False(t, result.IsError)
	structured := result.StructuredContent.(ToolResult[MessageMutationData])
	require.NotNil(t, structured.Data)
	assert.Equal(t, "Q1", structured.Data.ScheduledMessageID)
	assert.Equal(t, "scheduled", structured.Data.Outcome)
	assert.Equal(t, 1, service.scheduleCalls)
}

func TestUnitMessagesDeletePrepareExecuteRevalidatesAndConsumesToken(t *testing.T) {
	service := &fakeMessageFilesService{message: slack.Message{Msg: slack.Msg{Timestamp: "1.000001", Text: "remove", User: "U1"}}}
	handler := newTestMessageFilesHandler(service)
	prepared, err := served(handler.MessagesDelete(context.Background(), toolRequest(map[string]any{
		"action": "prepare", "channel_id": "C1", "timestamp": "1.000001",
	})))
	require.NoError(t, err)
	token := prepared.StructuredContent.(ToolResult[MessageMutationData]).Data.ApprovalToken

	executed, err := served(handler.MessagesDelete(context.Background(), toolRequest(map[string]any{
		"action": "execute", "channel_id": "C1", "timestamp": "1.000001", "approval_token": token,
	})))
	require.NoError(t, err)
	assert.False(t, executed.IsError)
	assert.Equal(t, 2, service.lookupCalls)
	assert.Equal(t, 1, service.deleteCalls)

	replayed, err := served(handler.MessagesDelete(context.Background(), toolRequest(map[string]any{
		"action": "execute", "channel_id": "C1", "timestamp": "1.000001", "approval_token": token,
	})))
	require.NoError(t, err)
	assert.True(t, replayed.IsError)
	assert.Equal(t, 1, service.deleteCalls)
}

func TestUnitMessagesUpdateTimeoutIsOutcomeUnknownAndNeverRetried(t *testing.T) {
	service := &fakeMessageFilesService{updateErr: context.DeadlineExceeded}
	result, err := served(newTestMessageFilesHandler(service).MessagesUpdate(context.Background(), toolRequest(map[string]any{
		"channel_id": "C1", "timestamp": "1.000001", "text": "changed",
	})))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	structured := result.StructuredContent.(ToolResult[struct{}])
	require.NotNil(t, structured.Error)
	assert.Equal(t, "outcome_unknown", structured.Error.Code)
	assert.Equal(t, 1, service.updateCalls)
}

func TestUnitMessagesUpdateRejectsInvalidTimestamp(t *testing.T) {
	service := &fakeMessageFilesService{updateErr: errors.New("must not run")}
	result, err := served(newTestMessageFilesHandler(service).MessagesUpdate(context.Background(), toolRequest(map[string]any{
		"channel_id": "C1", "timestamp": "bad", "text": "changed",
	})))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, service.updateCalls)
}

// The denial side is TestUnitEveryAllowlistedToolRefusesADeniedChannel; this
// checks the handler tests the channel it was given, not some other value.
func TestUnitMessageFilesPassAnAllowlistedChannel(t *testing.T) {
	t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C1")
	service := &fakeMessageFilesService{}
	handler := newTestMessageFilesHandler(service)
	handler.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }

	scheduled, err := served(handler.MessagesSchedule(context.Background(), toolRequest(map[string]any{
		"channel_id": "C1", "text": "later", "post_at": "1800000060",
	})))
	require.NoError(t, err)
	require.False(t, scheduled.IsError)
	assert.Equal(t, 1, service.scheduleCalls)

	uploaded, err := served(handler.FilesUpload(context.Background(), toolRequest(map[string]any{
		"filename": "a.txt", "content": "hello", "channel_id": "C1",
	})))
	require.NoError(t, err)
	require.False(t, uploaded.IsError)
	assert.Equal(t, 1, service.uploadCalls)
}

func TestUnitFilesUploadWithoutChannelSkipsAllowlist(t *testing.T) {
	t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C1")

	service := &fakeMessageFilesService{}
	result, err := served(newTestMessageFilesHandler(service).FilesUpload(context.Background(), toolRequest(map[string]any{
		"filename": "a.txt", "content": "hello",
	})))
	require.NoError(t, err)
	require.False(t, result.IsError, "an unshared upload posts nothing, so the message allowlist does not apply")
	assert.Equal(t, 1, service.uploadCalls)
}
