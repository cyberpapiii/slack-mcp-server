package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type AuthStatusHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
}

func NewAuthStatusHandler(apiProvider *provider.ApiProvider, logger *zap.Logger) *AuthStatusHandler {
	return &AuthStatusHandler{apiProvider: apiProvider, logger: logger}
}

type AuthStatusData struct {
	CatalogVersion           string                         `json:"catalog_version"`
	ProviderIdentity         provider.ProviderIdentity      `json:"provider_identity"`
	OAuthCredential          provider.OAuthCredentialStatus `json:"oauth_credential"`
	CapabilityAvailability   map[string]string              `json:"capability_availability"`
	UsersCacheReady          bool                           `json:"users_cache_ready"`
	ChannelsCacheReady       bool                           `json:"channels_cache_ready"`
	CacheFullyReady          bool                           `json:"cache_fully_ready"`
	BrowserFeaturesAvailable bool                           `json:"browser_features_available"`
	BrowserCredentialSource  string                         `json:"browser_credential_source"`
	BrowserDegradedReason    string                         `json:"browser_degraded_reason,omitempty"`
	IsOAuth                  bool                           `json:"is_oauth"`
	IsBotToken               bool                           `json:"is_bot_token"`
	BrowserOnlyTools         []string                       `json:"browser_only_tools"`
	BrowserOnlyToolsBlocked  bool                           `json:"browser_only_tools_blocked"`
	Summary                  string                         `json:"summary"`
}

func (h *AuthStatusHandler) Handler(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "AuthStatusHandler called", request)

	usersReady := h.apiProvider.UsersCacheReady()
	channelsReady := h.apiProvider.ChannelsCacheReady()
	browserOK := h.apiProvider.BrowserFeaturesAvailable()
	degradedReason := h.apiProvider.BrowserDegradedReason()
	browserBlocked := !browserOK && degradedReason != ""

	payload := AuthStatusData{
		CatalogVersion:           capability.CatalogVersion,
		ProviderIdentity:         h.apiProvider.Identity(),
		OAuthCredential:          h.apiProvider.OAuthCredentialStatus(),
		CapabilityAvailability:   capabilityAvailability(h.apiProvider.IsOAuth(), h.apiProvider.IsBotToken(), browserOK, degradedReason),
		UsersCacheReady:          usersReady,
		ChannelsCacheReady:       channelsReady,
		CacheFullyReady:          usersReady && channelsReady,
		BrowserFeaturesAvailable: browserOK,
		BrowserCredentialSource:  h.apiProvider.BrowserCredentialSource(),
		BrowserDegradedReason:    degradedReason,
		IsOAuth:                  h.apiProvider.IsOAuth(),
		IsBotToken:               h.apiProvider.IsBotToken(),
		BrowserOnlyTools:         capability.ActiveBrowserLocalTools(),
		BrowserOnlyToolsBlocked:  browserBlocked,
		Summary:                  buildAuthSummary(usersReady, channelsReady, browserOK, degradedReason),
	}

	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal auth status: %w", err)
	}

	return NewStructuredResult(payload, SystemResultMeta(), string(raw)), nil
}

func capabilityAvailability(standardOAuth, botToken, browserOK bool, degradedReason string) map[string]string {
	availability := map[string]string{
		"standard_oauth":            "not_configured",
		"slack_lists":               "unverified",
		"host_curation":             "unverified",
		"persisted_draft_lifecycle": "unsupported",
		"semantic_search":           "official_provider_unverified",
	}
	if standardOAuth {
		availability["standard_oauth"] = "available"
	}
	if !standardOAuth || botToken {
		availability["dnd"] = "user_oauth_required"
		availability["slack_lists"] = "user_oauth_required"
	} else {
		availability["dnd"] = "available_if_enabled"
		availability["slack_lists"] = "available_if_workspace_supported_and_enabled"
	}
	if botToken {
		availability["conversations_unreads"] = "unavailable_for_bot"
	} else {
		availability["conversations_unreads"] = "available_after_cache_warmup"
	}
	if browserOK {
		availability["browser_session"] = "available"
		availability["activity"] = "available_after_cache_warmup"
		availability["saved_later"] = "available_after_cache_warmup"
	} else if degradedReason != "" {
		availability["browser_session"] = "degraded"
		availability["activity"] = "browser_session_degraded"
		availability["saved_later"] = "browser_session_degraded"
	} else {
		availability["browser_session"] = "not_configured"
		availability["activity"] = "browser_session_required"
		availability["saved_later"] = "browser_session_required"
	}
	return availability
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
		parts = append(parts, "Cache-dependent tools (channels_list, unreads, activity, saved) are unavailable until caches warm; the server keeps retrying in the background and registers them automatically on success.")
	}

	if browserOK {
		parts = append(parts, "Browser session auth is healthy for Activity and Saved tools.")
	} else if degradedReason != "" {
		parts = append(parts, "Browser session auth is degraded: "+degradedReason+". Refresh Slack in your browser and restart Plug.")
	} else {
		parts = append(parts, "Browser-only tools require xoxc/xoxd browser session tokens.")
	}

	return strings.Join(parts, " ")
}
