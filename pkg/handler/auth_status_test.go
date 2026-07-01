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

func TestUnitAuthStatusHandler_ReturnsJSONSummary(t *testing.T) {
	h := NewAuthStatusHandler(&provider.ApiProvider{}, zap.NewNop())
	result, err := h.Handler(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	require.NotNil(t, result)

	var body string
	for _, content := range result.Content {
		if textContent, ok := content.(mcp.TextContent); ok {
			body = textContent.Text
			break
		}
	}
	require.NotEmpty(t, body)
	assert.Contains(t, body, "users_cache_ready")
	assert.Contains(t, body, "summary")
}
