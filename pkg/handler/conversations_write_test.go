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
	type call func(*ConversationsHandler, context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	tests := []struct {
		tool string
		env  string
		call call
		args map[string]any
	}{
		{"conversations_add_message", "SLACK_MCP_ADD_MESSAGE_TOOL", (*ConversationsHandler).ConversationsAddMessageHandler, map[string]any{"channel_id": "C123", "text": "hi"}},
		{"reactions_add", "SLACK_MCP_REACTION_TOOL", (*ConversationsHandler).ReactionsAddHandler, map[string]any{"channel_id": "C123", "timestamp": "1.2", "emoji": "+1"}},
		{"reactions_remove", "SLACK_MCP_REACTION_TOOL", (*ConversationsHandler).ReactionsRemoveHandler, map[string]any{"channel_id": "C123", "timestamp": "1.2", "emoji": "+1"}},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			t.Setenv(tt.env, "C456")
			api := &provider.ApiProvider{}
			api.SkipCache()
			h := NewConversationsHandler(api, zap.NewNop(), envSettings(), true)
			_, err := tt.call(h, context.Background(), mutationRequest(tt.args))
			var toolErr *ToolError
			require.ErrorAs(t, err, &toolErr)
			assert.Equal(t, "permission_denied", toolErr.Code)
			assert.Contains(t, err.Error(), tt.tool+" is not allowed")
			assert.Contains(t, err.Error(), tt.env)
		})
	}
}
