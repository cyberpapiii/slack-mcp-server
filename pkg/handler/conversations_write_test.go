package handler

import (
	"context"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The allowed path is not covered here: a ready provider with a fake Web API
// client needs a seam pkg/provider does not expose yet.
func TestUnitWriteToolsDenyChannelOutsideAllowlist(t *testing.T) {
	api := &provider.ApiProvider{}
	api.SkipCache()
	h := NewConversationsHandler(api, zap.NewNop(), true)

	tests := []struct {
		name   string
		env    string
		call   func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args   map[string]any
		detail string
	}{
		{"add_message", "SLACK_MCP_ADD_MESSAGE_TOOL", h.ConversationsAddMessageHandler, map[string]any{"channel_id": "C123", "text": "hi"}, "conversations_add_message is not allowed"},
		{"reactions_add", "SLACK_MCP_REACTION_TOOL", h.ReactionsAddHandler, map[string]any{"channel_id": "C123", "timestamp": "1.2", "emoji": "+1"}, "reactions tools are not allowed"},
		{"reactions_remove", "SLACK_MCP_REACTION_TOOL", h.ReactionsRemoveHandler, map[string]any{"channel_id": "C123", "timestamp": "1.2", "emoji": "+1"}, "reactions tools are not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.env, "C456")
			_, err := tt.call(context.Background(), mutationRequest(tt.args))
			var toolErr *ToolError
			require.ErrorAs(t, err, &toolErr)
			assert.Equal(t, "permission_denied", toolErr.Code)
			assert.Contains(t, err.Error(), tt.detail)
			assert.Contains(t, err.Error(), tt.env)
		})
	}
}
