package handler

import (
	"context"
	"slices"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type AuthStatusHandler struct {
	apiProvider  *provider.ApiProvider
	enabledTools []string
	logger       *zap.Logger
}

func NewAuthStatusHandler(apiProvider *provider.ApiProvider, enabledTools []string, logger *zap.Logger) *AuthStatusHandler {
	return &AuthStatusHandler{apiProvider: apiProvider, enabledTools: enabledTools, logger: logger}
}

type AuthStatusData struct {
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
	usersReady := h.apiProvider.UsersCacheReady()
	channelsReady := h.apiProvider.ChannelsCacheReady()
	browserOK := h.apiProvider.BrowserFeaturesAvailable()
	degradedReason := h.apiProvider.BrowserDegradedReason()
	mode := h.apiProvider.Mode()

	availability := make(map[string]string, len(capability.Tools))
	for _, tool := range capability.Tools {
		availability[tool.Name] = toolAvailability(tool, toolState{
			enabled:    slices.Contains(h.enabledTools, tool.Name),
			satisfied:  h.apiProvider.Satisfies(tool.Requires),
			browserOK:  browserOK,
			cacheReady: usersReady && channelsReady,
		})
	}

	payload := AuthStatusData{
		ProviderIdentity:         h.apiProvider.Identity(),
		OAuthCredential:          h.apiProvider.OAuthCredentialStatus(),
		CapabilityAvailability:   availability,
		UsersCacheReady:          usersReady,
		ChannelsCacheReady:       channelsReady,
		CacheFullyReady:          usersReady && channelsReady,
		BrowserFeaturesAvailable: browserOK,
		BrowserCredentialSource:  h.apiProvider.BrowserCredentialSource(),
		BrowserDegradedReason:    degradedReason,
		IsOAuth:                  mode.OAuth(),
		IsBotToken:               mode == provider.ModeBot,
		BrowserOnlyTools:         capability.BrowserNames(),
		BrowserOnlyToolsBlocked:  !browserOK && degradedReason != "",
		Summary:                  buildAuthSummary(usersReady, channelsReady, browserOK, degradedReason, mode.BrowserRecovers()),
	}

	if payload.OAuthCredential.Reason != "" && payload.OAuthCredential.State == "unavailable" {
		payload.Summary += " OAuth is unavailable: " + payload.OAuthCredential.Reason + "."
	}

	return NewStructuredResult(payload, SystemResultMeta(), fallbackJSON(payload)), nil
}

type toolState struct {
	enabled    bool // in the enabled-tools list
	satisfied  bool // the running credentials meet the tool's requirement
	browserOK  bool // the browser session is healthy
	cacheReady bool // the users and channels caches are warm
}

// toolAvailability says whether a tool can be called now and, if not, why,
// from the same facts registration and the browser-session gate decide on.
func toolAvailability(tool capability.Tool, state toolState) string {
	switch {
	case !state.enabled:
		return "not_enabled"
	case !state.satisfied:
		return requirementMissing[tool.Requires]
	case tool.Requires == capability.BrowserSession && !state.browserOK:
		return "browser_session_degraded"
	case tool.CacheReady && !state.cacheReady:
		return "available_after_cache_warmup"
	default:
		return "available"
	}
}

var requirementMissing = map[capability.Requirement]string{
	capability.UserToken:      "user_token_required",
	capability.UserOAuth:      "user_oauth_required",
	capability.BrowserSession: "browser_session_required",
}

func buildAuthSummary(usersReady, channelsReady, browserOK bool, degradedReason string, browserRecovers bool) string {
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
		parts = append(parts, "Cache-dependent tools ("+strings.Join(cacheReadyNames(), ", ")+") are unavailable until caches warm; the server keeps retrying in the background and registers them automatically on success.")
	}

	switch {
	case browserOK:
		parts = append(parts, "Browser session auth is healthy for the browser-session tools.")
	case degradedReason != "" && browserRecovers:
		parts = append(parts, "Browser session auth is degraded: "+degradedReason+". Refresh Slack in your browser or the browser Keychain item; the server retries every 5 minutes and recovers without a restart.")
	case degradedReason != "":
		parts = append(parts, "Browser session auth is degraded: "+degradedReason+". Refresh Slack in your browser and restart Plug.")
	default:
		parts = append(parts, "Browser-only tools require xoxc/xoxd browser session tokens.")
	}

	return strings.Join(parts, " ")
}

func cacheReadyNames() []string {
	var names []string
	for _, tool := range capability.Tools {
		if tool.CacheReady {
			names = append(names, tool.Name)
		}
	}
	return names
}

// Tools lists the MCP tools this handler serves.
func (h *AuthStatusHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.SlackAuthStatus, Run: h.Handler, Options: []mcp.ToolOption{
			mcp.WithOutputSchema[AuthStatusResult](),
			mcp.WithDescription("Report Slack auth, cache readiness, and browser-session health. Use before activity or saved tools when auth may have expired."),
		}},
	}
}
