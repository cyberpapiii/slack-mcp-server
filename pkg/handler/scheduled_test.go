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

func newTestScheduledHandler(service ScheduledService) *ScheduledHandler {
	return NewScheduledHandler(service, approval.NewStore(time.Minute), userIdentity, zap.NewNop())
}

type fakeScheduledService struct {
	pages       []provider.ScheduledPage
	listCalls   int
	cancelCalls int
	cancelErr   error
}

func (f *fakeScheduledService) ListScheduled(context.Context, provider.ScheduledListRequest) (provider.ScheduledPage, error) {
	index := f.listCalls
	f.listCalls++
	if index >= len(f.pages) {
		return provider.ScheduledPage{}, nil
	}
	return f.pages[index], nil
}

func (f *fakeScheduledService) CancelScheduled(context.Context, string, string) error {
	f.cancelCalls++
	return f.cancelErr
}

func scheduledRequest(args map[string]any) mcp.CallToolRequest {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = args
	return request
}

func TestUnitScheduledListReturnsCSVRowsFilteredByQuery(t *testing.T) {
	service := &fakeScheduledService{pages: []provider.ScheduledPage{{
		Messages: []provider.ScheduledMessage{
			{ScheduledMessageID: "Q1", ChannelID: "C1", Text: "abcdefghijklmnopqrstuvwxyz", PostAt: time.Date(2026, 8, 9, 16, 0, 0, 0, time.FixedZone("EDT", -4*3600))},
			{ScheduledMessageID: "Q2", ChannelID: "C1", Text: "unrelated", PostAt: time.Date(2026, 8, 9, 17, 0, 0, 0, time.UTC)},
		},
		NextCursor: "next",
	}}}
	handler := newTestScheduledHandler(service)
	result, err := handler.List(context.Background(), scheduledRequest(map[string]any{"limit": 25, "text_query": "abc"}))
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)
	assert.Equal(t, "#next_cursor: next\nScheduledID,Channel,PostAt,Text\nQ1,C1,2026-08-09T20:00:00Z,abcdefghijklmnopqrstuvwxyz\n", ResultText(result))
}

func TestUnitScheduledCancelPrepareExecuteRevalidatesAndConsumesApproval(t *testing.T) {
	target := provider.ScheduledMessage{ScheduledMessageID: "Q1", ChannelID: "C1", Text: "hello", PostAt: time.Unix(1786305600, 0).UTC()}
	service := &fakeScheduledService{pages: []provider.ScheduledPage{{Messages: []provider.ScheduledMessage{target}}, {Messages: []provider.ScheduledMessage{target}}}}
	handler := newTestScheduledHandler(service)

	prepared, err := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "prepare", "channel_id": "C1", "scheduled_message_id": "Q1"}))
	require.NoError(t, err)
	preparedData := prepared.StructuredContent.(ToolResult[ScheduledCancelData]).Data
	require.NotNil(t, preparedData)
	require.NotEmpty(t, preparedData.ApprovalToken)
	assert.False(t, preparedData.Cancelled)

	executed, err := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "execute", "channel_id": "C1", "scheduled_message_id": "Q1", "approval_token": preparedData.ApprovalToken}))
	require.NoError(t, err)
	executedData := executed.StructuredContent.(ToolResult[ScheduledCancelData]).Data
	require.NotNil(t, executedData)
	assert.True(t, executedData.Cancelled)
	assert.Equal(t, 1, service.cancelCalls)

	replayed, err := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "execute", "channel_id": "C1", "scheduled_message_id": "Q1", "approval_token": preparedData.ApprovalToken}))
	require.NoError(t, err)
	assert.True(t, replayed.IsError)
	assert.Equal(t, 1, service.cancelCalls)
}

func TestUnitScheduledCancelRejectsStateDriftBeforeMutation(t *testing.T) {
	before := provider.ScheduledMessage{ScheduledMessageID: "Q1", ChannelID: "C1", Text: "hello", PostAt: time.Unix(1786305600, 0).UTC()}
	after := before
	after.Text = "changed"
	service := &fakeScheduledService{pages: []provider.ScheduledPage{{Messages: []provider.ScheduledMessage{before}}, {Messages: []provider.ScheduledMessage{after}}}}
	handler := newTestScheduledHandler(service)
	prepared, err := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "prepare", "channel_id": "C1", "scheduled_message_id": "Q1"}))
	require.NoError(t, err)
	token := prepared.StructuredContent.(ToolResult[ScheduledCancelData]).Data.ApprovalToken

	result, err := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "execute", "channel_id": "C1", "scheduled_message_id": "Q1", "approval_token": token}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, service.cancelCalls)
}

func TestUnitScheduledCancelReturnsRetryAfterAndOutcomeUnknown(t *testing.T) {
	target := provider.ScheduledMessage{ScheduledMessageID: "Q1", ChannelID: "C1", Text: "hello", PostAt: time.Unix(1786305600, 0).UTC()}
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "rate limit", err: &slack.RateLimitedError{RetryAfter: 7 * time.Second}, code: "rate_limited"},
		{name: "timeout", err: context.DeadlineExceeded, code: "outcome_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeScheduledService{pages: []provider.ScheduledPage{{Messages: []provider.ScheduledMessage{target}}, {Messages: []provider.ScheduledMessage{target}}}, cancelErr: test.err}
			handler := newTestScheduledHandler(service)
			prepared, _ := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "prepare", "channel_id": "C1", "scheduled_message_id": "Q1"}))
			token := prepared.StructuredContent.(ToolResult[ScheduledCancelData]).Data.ApprovalToken
			result, err := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "execute", "channel_id": "C1", "scheduled_message_id": "Q1", "approval_token": token}))
			require.NoError(t, err)
			structured := result.StructuredContent.(ToolResult[struct{}])
			require.NotNil(t, structured.Error)
			assert.Equal(t, test.code, structured.Error.Code)
			assert.Equal(t, 1, service.cancelCalls)
		})
	}
}

func TestUnitScheduledCancelMapsPermissionError(t *testing.T) {
	target := provider.ScheduledMessage{ScheduledMessageID: "Q1", ChannelID: "C1", Text: "hello", PostAt: time.Unix(1786305600, 0).UTC()}
	service := &fakeScheduledService{
		pages:     []provider.ScheduledPage{{Messages: []provider.ScheduledMessage{target}}, {Messages: []provider.ScheduledMessage{target}}},
		cancelErr: slack.SlackErrorResponse{Err: "missing_scope"},
	}
	handler := newTestScheduledHandler(service)
	prepared, _ := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "prepare", "channel_id": "C1", "scheduled_message_id": "Q1"}))
	token := prepared.StructuredContent.(ToolResult[ScheduledCancelData]).Data.ApprovalToken
	result, err := handler.Cancel(context.Background(), scheduledRequest(map[string]any{"action": "execute", "channel_id": "C1", "scheduled_message_id": "Q1", "approval_token": token}))
	require.NoError(t, err)
	structured := result.StructuredContent.(ToolResult[struct{}])
	require.NotNil(t, structured.Error)
	assert.Equal(t, "permission_denied", structured.Error.Code)
}

func TestUnitScheduledLookupIsBounded(t *testing.T) {
	pages := make([]provider.ScheduledPage, maxScheduledLookupPages)
	for index := range pages {
		pages[index].NextCursor = "next-" + time.Unix(int64(index), 0).UTC().Format("150405")
	}
	service := &fakeScheduledService{pages: pages}
	_, err := newTestScheduledHandler(service).findScheduled(context.Background(), "C1", "Q-missing")
	var typed *ToolError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "lookup_limit_exceeded", typed.Code)
	assert.Equal(t, maxScheduledLookupPages, service.listCalls)
}
