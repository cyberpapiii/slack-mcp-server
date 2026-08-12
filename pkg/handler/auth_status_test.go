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
	require.NotNil(t, result.StructuredContent)
	structured, ok := result.StructuredContent.(ToolResult[AuthStatusData])
	require.True(t, ok)
	require.NotNil(t, structured.Data)
	assert.Equal(t, TrustSystem, structured.Meta.Provenance.Trust)
	assert.Contains(t, body, "users_cache_ready")
	assert.Contains(t, body, "catalog_version")
	assert.Contains(t, body, "provider_identity")
	assert.Contains(t, body, "capability_availability")
	assert.Contains(t, body, "summary")
}

func TestUnitCapabilityAvailabilityIsolatesBrowserDegradation(t *testing.T) {
	available := capabilityAvailability(true, false, false, "expired browser session")
	assert.Equal(t, "available", available["standard_oauth"])
	assert.Equal(t, "degraded", available["browser_session"])
	assert.Equal(t, "available_if_workspace_supported_and_enabled", available["slack_lists"])
	assert.Equal(t, "browser_session_degraded", available["activity"])
	assert.Equal(t, "browser_session_degraded", available["conversations_unreads"])
}

func TestUnitCapabilityAvailabilityExplainsBotLimitations(t *testing.T) {
	available := capabilityAvailability(false, true, false, "")
	assert.Equal(t, "user_oauth_required", available["dnd"])
	assert.Equal(t, "user_oauth_required", available["slack_lists"])
	assert.Equal(t, "browser_session_required", available["conversations_unreads"])
}

func TestUnitCapabilityAvailabilityReportsBrowserUnreads(t *testing.T) {
	available := capabilityAvailability(true, false, true, "")
	assert.Equal(t, "available_after_cache_warmup", available["conversations_unreads"])
}
