package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/cachedir"
	"github.com/korotovsky/slack-mcp-server/pkg/keychain"
	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
	"github.com/korotovsky/slack-mcp-server/pkg/slackcreds"
	"go.uber.org/zap"
)

const usersNotReadyMsg = "users cache is not ready yet, sync process is still running... please wait"
const channelsNotReadyMsg = "channels cache is not ready yet, sync process is still running... please wait"

// Upper bound for one cache warm-up or background refresh, so an orphaned
// goroutine cannot hold Slack rate-limit budget forever. Large workspaces need
// most of it cold.
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
	// oauthLoadErr is why managed OAuth failed at startup when the server
	// continued on the browser session instead. OAuth-only tools are absent.
	oauthLoadErr error
	// oauthScopes are the scopes the managed OAuth login was granted, as
	// loaded at startup; nil without managed OAuth.
	oauthScopes []string

	creds CredentialConfig
	// client is the Slack session. api is the Slack calls the caches make:
	// the session itself, or a fake in tests.
	client MCPSlackClient
	api    SlackAPI
	logger *zap.Logger

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
	usersMu        sync.Mutex // RefreshUsers load path
	usersFlight    refreshFlight

	channelsSnapshot          atomic.Pointer[ChannelsCache] // immutable snapshot; atomic load, no copy
	channelsCachePath         string
	channelsReady             atomic.Bool
	lastForcedChannelsRefresh time.Time
	channelsMu                sync.Mutex // lastForcedChannelsRefresh and the load path
	channelsFlight            refreshFlight

	revalidateOnce sync.Once
}

// ProviderIdentity.ActorType values.
const (
	ActorUser    = "user"
	ActorBot     = "bot"
	ActorUnknown = "unknown"
)

type ProviderIdentity struct {
	TeamID       string `json:"team_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	EnterpriseID string `json:"enterprise_id,omitempty"`
	ActorType    string `json:"actor_type"`
	TokenMode    string `json:"token_mode"`
}

// New resolves credentials from the environment and authenticates once.
// Every failure is returned; the caller decides whether it is fatal.
func New(logger *zap.Logger) (*ApiProvider, error) {
	creds := CredentialConfigFromEnv()
	cache := cacheConfigFromEnv(logger)
	xoxpToken, xoxbToken, xoxcToken, xoxdToken := creds.XOXP, creds.XOXB, creds.XOXC, creds.XOXD
	browserAccount := creds.BrowserKeychainAccount
	var managedManager *oauthcred.Manager
	var managedRecord oauthcred.Record
	var oauthLoadErr error
	if creds.OAuthKeychainAccount != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var managedErr error
		stop := keychain.WarnWhileWaiting(logger, keychain.PromptAfter, "oauth")
		managedManager, managedRecord, managedErr = loadManagedOAuth(ctx, creds, logger)
		stop()
		cancel()
		switch {
		case managedErr == nil:
			xoxpToken = managedRecord.AccessToken
		case !browserSessionCanServe(xoxbToken, xoxcToken, xoxdToken, browserAccount):
			return nil, fmt.Errorf("load managed OAuth credential: %w", managedErr)
		default:
			managedManager = nil
			oauthLoadErr = managedErr
			logger.Error("Managed OAuth credential unavailable; running on the browser session without OAuth-only tools. Run slack-mcp-auth login --replace, then restart.", zap.Error(managedErr))
		}
	}
	envXoxc, envXoxd := xoxcToken, xoxdToken
	var browserLoadErr error
	if account := browserAccount; account != "" {
		oauthFallbackToken := xoxpToken
		if oauthFallbackToken == "" {
			oauthFallbackToken = xoxbToken
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		stop := keychain.WarnWhileWaiting(logger, keychain.PromptAfter, "browser")
		browserCredentials, loadErr := resolveBrowserStartupCredentials(ctx, account, oauthFallbackToken, xoxcToken, xoxdToken)
		stop()
		cancel()
		if loadErr != nil {
			return nil, fmt.Errorf("load browser credential and no OAuth fallback is configured: %w", loadErr)
		}
		xoxcToken, xoxdToken = browserCredentials.xoxc, browserCredentials.xoxd
		if browserCredentials.degraded != nil {
			browserLoadErr = browserCredentials.degraded
			logger.Warn("Browser credential unavailable; continuing with OAuth-only Slack tools until it loads", zap.Error(browserLoadErr))
		}
	}

	// Web API calls use OAuth. The browser session serves the browser-only
	// tools and, while healthy, the reads it answers more completely.
	if xoxpToken != "" {
		authProvider, err := slackcreds.New(xoxpToken, "")
		if err != nil {
			return nil, fmt.Errorf("create auth provider with XOXP token: %w", err)
		}
		browser := oauthBrowser{
			xoxc: xoxcToken, xoxd: xoxdToken, loadErr: browserLoadErr,
			account: browserAccount, envXoxc: envXoxc, envXoxd: envXoxd,
		}
		ap, err := newProvider(authProvider, creds, cache, browser.configured(), logger)
		if err != nil {
			return nil, authFailure("check your Slack tokens", err)
		}
		ap.startUserOAuth(managedManager, managedRecord, browser)
		if xoxbToken != "" {
			logger.Warn("Both SLACK_MCP_XOXP_TOKEN and SLACK_MCP_XOXB_TOKEN are set; using user OAuth token")
		}
		return ap, nil
	}

	if xoxbToken != "" {
		authProvider, err := slackcreds.New(xoxbToken, "")
		if err != nil {
			return nil, fmt.Errorf("create auth provider with XOXB token: %w", err)
		}

		logger.Info("Using Bot token authentication",
			zap.String("context", "console"),
			zap.String("token_type", "xoxb"),
		)

		ap, err := newProvider(authProvider, creds, cache, false, logger)
		if err != nil {
			return nil, authFailure("check your Slack tokens", err)
		}
		return ap, nil
	}

	if xoxcToken != "" && xoxdToken != "" {
		authProvider, err := slackcreds.New(xoxcToken, xoxdToken)
		if err != nil {
			return nil, fmt.Errorf("create auth provider with XOXC/XOXD tokens: %w", err)
		}
		ap, err := newProvider(authProvider, creds, cache, true, logger)
		if err != nil {
			if oauthLoadErr != nil {
				err = errors.Join(err, oauthLoadErr)
			}
			return nil, authFailure("browser-session tokens are invalid and no OAuth fallback is available", err)
		}
		ap.oauthLoadErr = oauthLoadErr
		return ap, nil
	}

	return nil, errors.New("authentication required: Either SLACK_MCP_XOXP_TOKEN, SLACK_MCP_XOXB_TOKEN, or both SLACK_MCP_XOXC_TOKEN and SLACK_MCP_XOXD_TOKEN must be provided")
}

// browserSessionCanServe reports whether the server has a working path without
// managed OAuth. A bot token takes priority over a browser session in New, and
// serving as the bot instead of the user would be a silent identity change.
func browserSessionCanServe(xoxb, xoxc, xoxd, browserAccount string) bool {
	return xoxb == "" && ((xoxc != "" && xoxd != "") || browserAccount != "")
}

// authFailure keeps a network failure from reading as rejected credentials.
// Slack never saw the token when the request did not complete.
func authFailure(rejected string, err error) error {
	var requestErr *url.Error
	if errors.As(err, &requestErr) {
		return fmt.Errorf("could not reach Slack to authenticate; the tokens were not rejected: %w", err)
	}
	return fmt.Errorf("authentication failed: %s: %w", rejected, err)
}

// oauthBrowser is the browser session configured beside a user OAuth token:
// the loaded tokens, or why they failed to load, and where the re-probe
// reloads them from.
type oauthBrowser struct {
	xoxc, xoxd       string
	loadErr          error
	account          string
	envXoxc, envXoxd string
}

func (b oauthBrowser) configured() bool {
	return (b.xoxc != "" && b.xoxd != "") || b.loadErr != nil
}

// startUserOAuth starts what runs beside a user OAuth client. The browser
// re-probe starts last because it only acts on a session the attach step left
// degraded.
func (ap *ApiProvider) startUserOAuth(managed *oauthcred.Manager, record oauthcred.Record, browser oauthBrowser) {
	client := &ap.client
	if managed != nil {
		ap.startManagedOAuth(managed, record)
	}
	if client.mode != ModeUserOAuthBrowser {
		return
	}
	if browser.loadErr != nil {
		// The browser tools still register and say why they fail, and the
		// re-probe attaches the session once the credential loads.
		client.degradeBrowserSession(fmt.Errorf("load browser credential: %w", browser.loadErr))
	} else {
		client.attachBrowser(browser.xoxc, browser.xoxd)
	}
	ap.startBrowserReprobe(client, browser.account, browser.envXoxc, browser.envXoxd)
}

func newProvider(authProvider slackcreds.Value, creds CredentialConfig, cache cacheConfig, browserConfigured bool, logger *zap.Logger) (*ApiProvider, error) {
	mode := credentialModeFor(authProvider.SlackToken(), browserConfigured)
	if creds.Demo() {
		mode = ModeDemo
	}
	ap := &ApiProvider{creds: creds, logger: logger}
	if err := ap.client.connect(authProvider, mode, logger); err != nil {
		return nil, err
	}
	ap.api = &ap.client

	teamID := ap.client.authResponse.TeamID
	cacheDir, err := cachedir.Dir()
	if err != nil {
		logger.Warn("User cache directory unavailable; caching in the working directory", zap.Error(err))
	}
	usersCache := cache.usersPath
	if usersCache == "" {
		usersCache = cachePath(cacheDir, teamID, "users_cache.json")
	}
	channelsCache := cache.channelsPath
	if channelsCache == "" {
		channelsCache = cachePath(cacheDir, teamID, "channels_cache_v2.json")
	}

	ap.ctx, ap.cancel = context.WithCancel(context.Background())
	ap.cacheTTL = cache.ttl
	ap.minRefreshInterval = cache.minRefreshInterval
	ap.backgroundRefreshTimeout = defaultBackgroundRefreshTimeout
	ap.usersCachePath = usersCache
	ap.channelsCachePath = channelsCache
	ap.usersSnapshot.Store(newUsersCache(nil))
	ap.channelsSnapshot.Store(newChannelsCache(0))
	return ap, nil
}

// Close stops background refresh goroutines and waits, briefly, for a
// managed OAuth refresh in flight to save its rotated token. Safe to call
// more than once.
func (ap *ApiProvider) Close() {
	if ap.cancel != nil {
		ap.cancel()
	}
	ap.client.waitForOAuthRefresh(oauthShutdownGrace)
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

func (ap *ApiProvider) Slack() SlackAPI {
	return ap.api
}

// WebAPI is the Slack Web API client used for ordinary slack-go calls.
// Browser-only and enterprise-merge methods stay on Slack().
//
// The returned *WebClient resolves the underlying client on every call, so it
// is safe to hold for the life of the process. OAuth rotation replaces the
// *slack.Client, and a caller holding one keeps an access token that later
// expires.
func (ap *ApiProvider) WebAPI() *WebClient {
	return &WebClient{client: &ap.client}
}

// Mode is the credential mode New chose; zero for a provider without
// credentials.
func (ap *ApiProvider) Mode() CredentialMode {
	return ap.client.mode
}

func (ap *ApiProvider) Identity() ProviderIdentity {
	client := &ap.client
	identity := ProviderIdentity{
		TeamID:       client.authResponse.TeamID,
		UserID:       client.authResponse.UserID,
		EnterpriseID: client.authResponse.EnterpriseID,
	}
	switch {
	case client.mode == 0:
		identity.ActorType = ActorUnknown
		identity.TokenMode = "unknown"
	case client.mode == ModeBot:
		identity.ActorType = ActorBot
		identity.TokenMode = "bot-oauth"
	case client.mode.OAuth():
		identity.ActorType = ActorUser
		identity.TokenMode = "user-oauth"
	default:
		identity.ActorType = ActorUser
		identity.TokenMode = "browser-session"
	}
	return identity
}

func (i ProviderIdentity) IsUser() bool {
	return i.ActorType == ActorUser
}

// Auth workspace base URL, or "" if unavailable (omit workspace-dependent output).
func (ap *ApiProvider) WorkspaceURL() string {
	return ap.client.authResponse.URL
}

// UserToken, UserOAuth and BrowserSession are the running credentials as a
// tool's capability.Requirement reads them. A configured browser session
// counts even while degraded: its tools stay registered and say why they fail
// until the session recovers.
func (ap *ApiProvider) UserToken() bool      { return ap.Mode().UserToken() }
func (ap *ApiProvider) UserOAuth() bool      { return ap.Mode().UserOAuth() }
func (ap *ApiProvider) BrowserSession() bool { return ap.Mode().BrowserSession() }
