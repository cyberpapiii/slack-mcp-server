package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUnitSearchSortValidation(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"search_query": "hello", "sort": "bogus"}
	_, err := ch.parseParamsToolSearch(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sort")
}

// Search schema DefaultNumber(100) / 1..100 range: parser must default and clamp to match.
func TestUnitParseParamsToolSearchLimit(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{"absent limit uses schema default", map[string]any{"search_query": "hello"}, 100},
		{"explicit zero uses schema default", map[string]any{"search_query": "hello", "limit": 0}, 100},
		{"negative uses schema default", map[string]any{"search_query": "hello", "limit": -5}, 100},
		{"above max clamps to 100", map[string]any{"search_query": "hello", "limit": 500}, 100},
		{"in-range value passes through", map[string]any{"search_query": "hello", "limit": 50}, 50},
		{"max value passes through", map[string]any{"search_query": "hello", "limit": 100}, 100},
		{"json float encoding clamps too", map[string]any{"search_query": "hello", "limit": float64(500)}, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tt.args

			params, err := ch.parseParamsToolSearch(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, tt.want, params.limit)
		})
	}
}

// History default limit "1d" is forbidden with cursor; cursor must win over the duration window.
func TestUnitParseParamsToolConversationsCursorBeatsDurationLimit(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	tests := []struct {
		name       string
		args       map[string]any
		wantLimit  int
		wantWindow bool
	}{
		{
			name:       "duration limit with cursor is ignored",
			args:       map[string]any{"channel_id": "C1234567890", "limit": "1d", "cursor": "abc"},
			wantLimit:  0,
			wantWindow: false,
		},
		{
			name:       "duration limit without cursor sets the window",
			args:       map[string]any{"channel_id": "C1234567890", "limit": "1d"},
			wantLimit:  100,
			wantWindow: true,
		},
		{
			name:       "numeric limit with cursor is ignored (existing behavior)",
			args:       map[string]any{"channel_id": "C1234567890", "limit": "50", "cursor": "abc"},
			wantLimit:  0,
			wantWindow: false,
		},
		{
			name:       "numeric limit without cursor is honored",
			args:       map[string]any{"channel_id": "C1234567890", "limit": "50"},
			wantLimit:  50,
			wantWindow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tt.args

			params, err := ch.parseParamsToolConversations(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLimit, params.limit)
			if tt.wantWindow {
				assert.NotEmpty(t, params.oldest)
				assert.NotEmpty(t, params.latest)
			} else {
				assert.Empty(t, params.oldest)
				assert.Empty(t, params.latest)
			}
		})
	}
}

func TestUnitConversationsGetMessageParamValidation(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	// missing channel_id
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"timestamp": "1234567890.123456"}
	_, err := ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel_id")

	// missing timestamp
	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"channel_id": "C0123456789"}
	_, err = ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp")

	// invalid detail value
	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"channel_id": "C0123456789", "timestamp": "1234567890.123456", "detail": "bogus"}
	_, err = ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
}

func TestUnitAttachmentsLegendOnlyWhenFilesPresent(t *testing.T) {
	assert.Empty(t, buildAttachmentsLegend([]Message{{MsgID: "1"}, {MsgID: "2"}}))

	legend := buildAttachmentsLegend([]Message{{MsgID: "1"}, {MsgID: "2", AttachmentIDs: "F1 (shot.png, image, 340KB)"}})
	assert.True(t, strings.HasPrefix(legend, "#attachments: "), legend)
	assert.Contains(t, legend, "attachment_get_data")
	assert.True(t, strings.HasSuffix(legend, "\n"), "legend lines must terminate")
}

func TestUnitAttachmentsLegendSurvivesShortResponses(t *testing.T) {
	messages := []Message{{MsgID: "1", Channel: "C1", AttachmentIDs: "F1 (shot.png, image, 340KB)"}}
	header := buildLegendHeader(messages, renderOptions{channelName: func(string) string { return "" }})
	assert.Contains(t, header, "#attachments: ", "a single message with a file still needs the recovery route")
	assert.NotContains(t, header, "#users: ", "the 3-message user-legend gate still applies")
}
