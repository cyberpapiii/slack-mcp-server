package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rusq/slackdump/v3/auth"
	"go.uber.org/zap"
)

const usersNotReadyMsg = "users cache is not ready yet, sync process is still running... please wait"
const channelsNotReadyMsg = "channels cache is not ready yet, sync process is still running... please wait"

// Upper bound for background cache refreshes so an orphaned goroutine cannot
// hold Slack rate-limit budget forever.
const defaultBackgroundRefreshTimeout = 15 * time.Minute

var AllChanTypes = []string{MpIMChanType, IMChanType, PubChanType, PrivateChanType}

const (
	PubChanType     = "public_channel"
	PrivateChanType = "private_channel"
	MpIMChanType    = "mpim"
	IMChanType      = "im"
)

var ErrUsersNotReady = errors.New(usersNotReadyMsg)
var ErrChannelsNotReady = errors.New(channelsNotReadyMsg)
var ErrRefreshRateLimited = errors.New("refresh skipped due to rate limiting")
var ErrBrowserSessionUnavailable = errors.New("browser-session Slack auth is unavailable; refresh browser tokens to restore browser-only Slack tools")
var ErrUserOAuthRequired = errors.New("this personal Slack action requires user OAuth")

type ApiProvider struct {
	transport string
	client    SlackAPI
	logger    *zap.Logger

	// ctx bounds every background goroutine the provider starts; Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc

	cacheTTL           time.Duration
	minRefreshInterval time.Duration

	// Test-overridable; production always uses defaultBackgroundRefreshTimeout.
	backgroundRefreshTimeout time.Duration

	usersSnapshot  atomic.Pointer[UsersCache] // immutable snapshot; atomic load, no copy
	usersCachePath string
	usersReady     atomic.Bool
	usersMu        sync.RWMutex // refreshUsersInternal load path
	usersFlight    refreshFlight

	channelsSnapshot          atomic.Pointer[ChannelsCache] // immutable snapshot; atomic load, no copy
	channelsCachePath         string
	channelsReady             atomic.Bool
	lastForcedChannelsRefresh time.Time
	channelsMu                sync.RWMutex // lastForcedChannelsRefresh
	channelsFlight            refreshFlight
}

type ProviderIdentity struct {
	TeamID       string `json:"team_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	EnterpriseID string `json:"enterprise_id,omitempty"`
	ActorType    string `json:"actor_type"`
	TokenMode    string `json:"token_mode"`
}

// New resolves credentials from the environment and authenticates once.
// Every failure is returned; the caller decides whether it is fatal.
func New(transport string, logger *zap.Logger) (*ApiProvider, error) {
	xoxpToken := os.Getenv("SLACK_MCP_XOXP_TOKEN")
	xoxbToken := os.Getenv("SLACK_MCP_XOXB_TOKEN")
	xoxcToken := os.Getenv("SLACK_MCP_XOXC_TOKEN")
	xoxdToken := os.Getenv("SLACK_MCP_XOXD_TOKEN")
	var managedManager *OAuthTokenManager
	var managedRecord OAuthTokenRecord
	if os.Getenv("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT") != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var managedErr error
		managedManager, managedRecord, managedErr = loadManagedOAuth(ctx)
		cancel()
		if managedErr != nil {
			return nil, fmt.Errorf("load managed OAuth credential: %w", managedErr)
		}
		xoxpToken = managedRecord.AccessToken
	}
	if account := strings.TrimSpace(os.Getenv("SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT")); account != "" {
		oauthFallbackToken := xoxpToken
		if oauthFallbackToken == "" {
			oauthFallbackToken = xoxbToken
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		browserCredentials, loadErr := resolveBrowserStartupCredentials(ctx, account, oauthFallbackToken, xoxcToken, xoxdToken)
		cancel()
		if loadErr != nil {
			return nil, fmt.Errorf("load browser credential and no OAuth fallback is configured: %w", loadErr)
		}
		xoxcToken, xoxdToken = browserCredentials.xoxc, browserCredentials.xoxd
		if browserCredentials.degraded != nil {
			logger.Warn("Browser credential unavailable; continuing with OAuth-only Slack tools", zap.Error(browserCredentials.degraded))
		}
	}

	// Supported Web API calls always prefer OAuth. Browser credentials are
	// attached only to browser-private Activity and Later surfaces.
	if xoxpToken != "" {
		authProvider, err := auth.NewValueAuth(xoxpToken, "")
		if err != nil {
			return nil, fmt.Errorf("create auth provider with XOXP token: %w", err)
		}
		ap, err := newProvider(transport, authProvider, logger)
		if err != nil {
			return nil, fmt.Errorf("authentication failed: check your Slack tokens: %w", err)
		}
		if managedManager != nil {
			ap.startManagedOAuth(managedManager, managedRecord)
		}
		if !ap.IsBotToken() && xoxcToken != "" && xoxdToken != "" {
			attachBrowserToOAuth(ap, xoxcToken, xoxdToken, logger)
		}
		if xoxbToken != "" {
			logger.Warn("Both SLACK_MCP_XOXP_TOKEN and SLACK_MCP_XOXB_TOKEN are set; using user OAuth token")
		}
		return ap, nil
	}

	if xoxbToken != "" {
		authProvider, err := auth.NewValueAuth(xoxbToken, "")
		if err != nil {
			return nil, fmt.Errorf("create auth provider with XOXB token: %w", err)
		}

		logger.Info("Using Bot token authentication",
			zap.String("context", "console"),
			zap.String("token_type", "xoxb"),
		)

		ap, err := newProvider(transport, authProvider, logger)
		if err != nil {
			return nil, fmt.Errorf("authentication failed: check your Slack tokens: %w", err)
		}
		return ap, nil
	}

	if xoxcToken != "" && xoxdToken != "" {
		authProvider, err := auth.NewValueAuth(xoxcToken, xoxdToken)
		if err != nil {
			return nil, fmt.Errorf("create auth provider with XOXC/XOXD tokens: %w", err)
		}
		ap, err := newProvider(transport, authProvider, logger)
		if err != nil {
			return nil, fmt.Errorf("authentication failed: browser-session tokens are invalid and no OAuth fallback is configured: %w", err)
		}
		return ap, nil
	}

	return nil, errors.New("authentication required: Either SLACK_MCP_XOXP_TOKEN, SLACK_MCP_XOXB_TOKEN, or both SLACK_MCP_XOXC_TOKEN and SLACK_MCP_XOXD_TOKEN must be provided")
}

func newProvider(transport string, authProvider auth.Provider, logger *zap.Logger) (*ApiProvider, error) {
	client, err := newMCPSlackClient(authProvider, logger)
	if err != nil {
		return nil, err
	}

	teamID := client.AuthResponse().TeamID
	if IsDemoCredentials() {
		teamID = "demo"
	}
	cacheDir, err := userCacheDir()
	if err != nil {
		cacheDir = "."
		logger.Warn("User cache directory unavailable; caching in the working directory", zap.Error(err))
	}
	usersCache := os.Getenv("SLACK_MCP_USERS_CACHE")
	if usersCache == "" {
		usersCache = cachePath(cacheDir, teamID, "users_cache.json")
	}
	channelsCache := os.Getenv("SLACK_MCP_CHANNELS_CACHE")
	if channelsCache == "" {
		channelsCache = cachePath(cacheDir, teamID, "channels_cache_v2.json")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ap := &ApiProvider{
		transport: transport,
		client:    client,
		logger:    logger,
		ctx:       ctx,
		cancel:    cancel,

		cacheTTL:           getCacheTTL(),
		minRefreshInterval: getMinRefreshInterval(),

		backgroundRefreshTimeout: defaultBackgroundRefreshTimeout,

		usersCachePath:    usersCache,
		channelsCachePath: channelsCache,
	}
	ap.usersSnapshot.Store(newUsersCache(nil))
	ap.channelsSnapshot.Store(newChannelsCache(0))
	return ap, nil
}

// Close stops background refresh goroutines. Safe to call more than once.
func (ap *ApiProvider) Close() {
	if ap.cancel != nil {
		ap.cancel()
	}
}

// lifetime is the parent context for background work; tests that build an
// ApiProvider literal without New get context.Background.
func (ap *ApiProvider) lifetime() context.Context {
	if ap.ctx == nil {
		return context.Background()
	}
	return ap.ctx
}

func (ap *ApiProvider) IsReady() (bool, error) {
	if !ap.usersReady.Load() {
		return false, ErrUsersNotReady
	}
	if !ap.channelsReady.Load() {
		return false, ErrChannelsNotReady
	}
	return true, nil
}

func (ap *ApiProvider) UsersCacheReady() bool {
	return ap.usersReady.Load()
}

func (ap *ApiProvider) ChannelsCacheReady() bool {
	return ap.channelsReady.Load()
}

// Marks caches ready without loading; name lookups need IDs instead.
func (ap *ApiProvider) SkipCache() {
	ap.usersReady.Store(true)
	ap.channelsReady.Store(true)
}

func (ap *ApiProvider) ServerTransport() string {
	return ap.transport
}

func (ap *ApiProvider) Slack() SlackAPI {
	return ap.client
}

// WebAPI is the Slack Web API client used for ordinary slack-go calls.
// Browser-only and enterprise-merge methods stay on Slack().
//
// The returned *WebClient resolves the underlying client on every call, so it
// is safe to hold for the life of the process. It used to return the
// *slack.Client itself, which OAuth rotation replaces, and long-lived callers
// silently kept an access token that later expired.
func (ap *ApiProvider) WebAPI() *WebClient {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil {
		return nil
	}
	return &WebClient{resolve: client.standardSlackClient}
}

func (ap *ApiProvider) IsBotToken() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.IsBotToken()
}

func (ap *ApiProvider) IsOAuth() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.IsOAuth()
}

func (ap *ApiProvider) Identity() ProviderIdentity {
	identity := ProviderIdentity{ActorType: "unknown", TokenMode: "unknown"}
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil {
		return identity
	}
	if authResponse := client.AuthResponse(); authResponse != nil {
		identity.TeamID = authResponse.TeamID
		identity.UserID = authResponse.UserID
		identity.EnterpriseID = authResponse.EnterpriseID
	}
	switch {
	case client.IsBotToken():
		identity.ActorType = "bot"
		identity.TokenMode = "bot-oauth"
	case client.IsOAuth():
		identity.ActorType = "user"
		identity.TokenMode = "user-oauth"
	default:
		identity.ActorType = "user"
		identity.TokenMode = "browser-session"
	}
	return identity
}

// Auth workspace base URL, or "" if unavailable (omit workspace-dependent output).
func (ap *ApiProvider) WorkspaceURL() string {
	if c, ok := ap.client.(*MCPSlackClient); ok {
		if ar := c.AuthResponse(); ar != nil {
			return ar.URL
		}
	}
	return ""
}
