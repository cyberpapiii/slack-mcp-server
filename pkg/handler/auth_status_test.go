package handler

import (
	"context"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUnitAuthStatusHandler_ReturnsJSONSummary(t *testing.T) {
	h := NewAuthStatusHandler(&provider.ApiProvider{}, []string{capability.SavedList}, zap.NewNop())
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
	assert.Contains(t, body, "provider_identity")
	assert.Contains(t, body, "capability_availability")
	assert.Contains(t, body, "summary")

	availability := structured.Data.CapabilityAvailability
	assert.Len(t, availability, len(capability.Tools), "every catalog tool is reported")
	assert.Equal(t, "browser_session_required", availability[capability.SavedList])
	assert.Equal(t, "not_enabled", availability[capability.DNDGet])
}

func TestUnitToolAvailabilityFollowsTheCatalogRow(t *testing.T) {
	saved, _ := capability.Lookup(capability.SavedList)
	unreads, _ := capability.Lookup(capability.ConversationsUnreads)
	dnd, _ := capability.Lookup(capability.DNDGet)
	ready := toolState{enabled: true, satisfied: true, browserOK: true, cacheReady: true}

	assert.Equal(t, "available", toolAvailability(saved, ready))
	cold := ready
	cold.cacheReady = false
	assert.Equal(t, "available", toolAvailability(saved, cold), "saved tools do not wait for the cache")
	assert.Equal(t, "available_after_cache_warmup", toolAvailability(unreads, cold))

	degraded := ready
	degraded.browserOK = false
	assert.Equal(t, "browser_session_degraded", toolAvailability(unreads, degraded))
	assert.Equal(t, "available", toolAvailability(dnd, degraded), "an OAuth tool is untouched by the browser session")

	bot := ready
	bot.satisfied = false
	assert.Equal(t, "user_oauth_required", toolAvailability(dnd, bot))
}

func TestUnitAuthSummarySaysWhetherADegradedSessionRecoversByItself(t *testing.T) {
	assert.Contains(t, buildAuthSummary(true, true, false, "invalid_auth", true), "recovers without a restart")
	assert.NotContains(t, buildAuthSummary(true, true, false, "invalid_auth", true), "restart Plug")
	assert.Contains(t, buildAuthSummary(true, true, false, "invalid_auth", false), "restart Plug")

	cold := buildAuthSummary(false, true, true, "", false)
	assert.Contains(t, cold, "channels_list")
	assert.NotContains(t, cold, "saved", "saved tools are not cache-dependent")
}
