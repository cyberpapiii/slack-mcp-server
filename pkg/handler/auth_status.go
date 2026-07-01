package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

var browserOnlyTools = []string{
	"activity_unreads",
	"activity_mark_read",
	"saved_list",
	"saved_update",
	"saved_clear_completed",
}

type AuthStatusHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
}

func NewAuthStatusHandler(apiProvider *provider.ApiProvider, logger *zap.Logger) *AuthStatusHandler {
	return &AuthStatusHandler{apiProvider: apiProvider, logger: logger}
}

type authStatusPayload struct {
	UsersCacheReady          bool     `json:"users_cache_ready"`
	ChannelsCacheReady       bool     `json:"channels_cache_ready"`
	CacheFullyReady          bool     `json:"cache_fully_ready"`
	BrowserFeaturesAvailable bool     `json:"browser_features_available"`
	BrowserDegradedReason    string   `json:"browser_degraded_reason,omitempty"`
	IsOAuth                  bool     `json:"is_oauth"`
	IsBotToken               bool     `json:"is_bot_token"`
	BrowserOnlyTools         []string `json:"browser_only_tools"`
	BrowserOnlyToolsBlocked  bool     `json:"browser_only_tools_blocked"`
	Summary                  string   `json:"summary"`
}

func (h *AuthStatusHandler) Handler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	_ = ctx
	_ = request

	usersReady := h.apiProvider.UsersCacheReady()
	channelsReady := h.apiProvider.ChannelsCacheReady()
	browserOK := h.apiProvider.BrowserFeaturesAvailable()
	degradedReason := h.apiProvider.BrowserDegradedReason()
	browserBlocked := !browserOK && degradedReason != ""

	payload := authStatusPayload{
		UsersCacheReady:          usersReady,
		ChannelsCacheReady:       channelsReady,
		CacheFullyReady:          usersReady && channelsReady,
		BrowserFeaturesAvailable: browserOK,
		BrowserDegradedReason:    degradedReason,
		IsOAuth:                  h.apiProvider.IsOAuth(),
		IsBotToken:               h.apiProvider.IsBotToken(),
		BrowserOnlyTools:         append([]string(nil), browserOnlyTools...),
		BrowserOnlyToolsBlocked:  browserBlocked,
		Summary:                  buildAuthSummary(usersReady, channelsReady, browserOK, degradedReason),
	}

	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal auth status: %w", err)
	}

	return mcp.NewToolResultText(string(raw)), nil
}

func buildAuthSummary(usersReady, channelsReady, browserOK bool, degradedReason string) string {
	var parts []string

	if usersReady && channelsReady {
		parts = append(parts, "User and channel caches are ready.")
	} else {
		if !usersReady {
			parts = append(parts, "Users cache is still loading or failed.")
		}
		if !channelsReady {
			parts = append(parts, "Channels cache is still loading or failed.")
		}
		parts = append(parts, "Cache-dependent tools (channels_list, unreads, activity, saved) may be unavailable until caches warm or Plug is restarted.")
	}

	if browserOK {
		parts = append(parts, "Browser session auth is healthy for Activity and Saved tools.")
	} else if degradedReason != "" {
		parts = append(parts, "Browser session auth is degraded: "+degradedReason+". Refresh Slack in your browser and restart Plug.")
	} else if !browserOK {
		parts = append(parts, "Browser-only tools require xoxc/xoxd browser session tokens.")
	}

	return strings.Join(parts, " ")
}
