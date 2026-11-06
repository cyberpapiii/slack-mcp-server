package handler

import (
	"context"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitAuthStatusHandler_ReturnsJSONSummary(t *testing.T) {
	api := &provider.ApiProvider{}
	h := NewAuthStatusHandler(api, NewAvailability([]string{capability.SavedList}, api))
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

// runtimeState is a RuntimeState with every fact set by hand.
type runtimeState struct {
	satisfied, browserOK, usersReady, channelsReady bool
}

func (s runtimeState) Satisfies(capability.Requirement) bool { return s.satisfied }
func (s runtimeState) BrowserFeaturesAvailable() bool        { return s.browserOK }
func (s runtimeState) UsersCacheReady() bool                 { return s.usersReady }
func (s runtimeState) ChannelsCacheReady() bool              { return s.channelsReady }

func TestUnitToolAvailabilityFollowsTheCatalogRow(t *testing.T) {
	saved, _ := capability.Lookup(capability.SavedList)
	unreads, _ := capability.Lookup(capability.ConversationsUnreads)
	dnd, _ := capability.Lookup(capability.DNDGet)
	enabled := []string{saved.Name, unreads.Name, dnd.Name}
	ready := runtimeState{satisfied: true, browserOK: true, usersReady: true, channelsReady: true}
	state := func(s runtimeState, tool capability.Tool) string { return NewAvailability(enabled, s).State(tool) }

	assert.Equal(t, "available", state(ready, saved))
	cold := ready
	cold.channelsReady = false
	assert.Equal(t, "available", state(cold, saved), "saved tools do not wait for the cache")
	assert.Equal(t, "available_after_cache_warmup", state(cold, unreads))
	assert.True(t, NewAvailability(enabled, cold).Served(unreads), "a cache-dependent tool is served, after warm-up")

	degraded := ready
	degraded.browserOK = false
	assert.Equal(t, "browser_session_degraded", state(degraded, unreads))
	assert.True(t, NewAvailability(enabled, degraded).Served(unreads), "a degraded session's tools stay registered")
	assert.Equal(t, "available", state(degraded, dnd), "an OAuth tool is untouched by the browser session")

	bot := ready
	bot.satisfied = false
	assert.Equal(t, "user_oauth_required", state(bot, dnd))
	assert.False(t, NewAvailability(enabled, bot).Served(dnd))

	assert.Equal(t, "not_enabled", NewAvailability(nil, ready).State(dnd))
	assert.False(t, NewAvailability(nil, ready).Served(dnd))
}

func TestUnitAuthSummarySaysWhetherADegradedSessionRecoversByItself(t *testing.T) {
	assert.Contains(t, buildAuthSummary(true, true, false, "invalid_auth", true), "recovers without a restart")
	assert.NotContains(t, buildAuthSummary(true, true, false, "invalid_auth", true), "restart Plug")
	assert.Contains(t, buildAuthSummary(true, true, false, "invalid_auth", false), "restart Plug")

	cold := buildAuthSummary(false, true, true, "", false)
	assert.Contains(t, cold, "channels_list")
	assert.NotContains(t, cold, "saved", "saved tools are not cache-dependent")
}
