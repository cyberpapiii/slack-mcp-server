package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	transportpkg "github.com/korotovsky/slack-mcp-server/pkg/transport"
	"github.com/rusq/slackdump/v3/auth"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

const usersNotReadyMsg = "users cache is not ready yet, sync process is still running... please wait"
const channelsNotReadyMsg = "channels cache is not ready yet, sync process is still running... please wait"
const defaultCacheTTL = 24 * time.Hour
const defaultMinRefreshInterval = 30 * time.Second

// Bounds background cache refresh so a hung GetUsersContext (unbounded rate-limit
// retries) cannot hold the shared users refresh flight forever.
const defaultBackgroundRefreshTimeout = 15 * time.Minute

var AllChanTypes = []string{MpIMChanType, IMChanType, PubChanType, PrivateChanType}

const (
	IMChanType      = "im"
	MpIMChanType    = "mpim"
	PubChanType     = "public_channel"
	PrivateChanType = "private_channel"
)

var ErrUsersNotReady = errors.New(usersNotReadyMsg)
var ErrChannelsNotReady = errors.New(channelsNotReadyMsg)
var ErrRefreshRateLimited = errors.New("refresh skipped due to rate limiting")
var ErrBrowserSessionUnavailable = errors.New("browser-session Slack auth is unavailable; refresh browser tokens to restore browser-only Slack tools")
var ErrUserOAuthRequired = errors.New("this personal Slack action requires user OAuth")

type browserRuntimeState int32

const (
	browserStateOAuthOnly browserRuntimeState = iota
	browserStateActive
	browserStateDegraded
)

type browserRuntimeStatus struct {
	Timestamp string `json:"timestamp"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

var browserStatusWriter = writeBrowserRuntimeStatus
var browserDegradationNotifier = notifyBrowserDegradation

// Atomic rename via CreateTemp; unpredictable name avoids symlink races.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cache_*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("setting file permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

func getCacheDir() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "."
	}

	dir := filepath.Join(cacheDir, "slack-mcp-server")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "."
	}
	return dir
}

// Go duration or integer seconds; empty/negative/unparseable → defaultVal.
func parseEnvDuration(envKey string, defaultVal time.Duration) time.Duration {
	raw := os.Getenv(envKey)
	if raw == "" {
		return defaultVal
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			return defaultVal
		}
		return d
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if secs < 0 {
			return defaultVal
		}
		return time.Duration(secs) * time.Second
	}
	return defaultVal
}

func getCacheTTL() time.Duration {
	return parseEnvDuration("SLACK_MCP_CACHE_TTL", defaultCacheTTL)
}

func getMinRefreshInterval() time.Duration {
	return parseEnvDuration("SLACK_MCP_MIN_REFRESH_INTERVAL", defaultMinRefreshInterval)
}

// Startup auth.test; TeamID namespaces cache files across workspaces.
func validateAuthAndGetTeamID(authProvider auth.Provider, logger *zap.Logger) (string, *slack.AuthTestResponse, error) {
	xoxpToken := os.Getenv("SLACK_MCP_XOXP_TOKEN")
	xoxcToken := os.Getenv("SLACK_MCP_XOXC_TOKEN")
	xoxdToken := os.Getenv("SLACK_MCP_XOXD_TOKEN")
	if xoxpToken == "demo" || (xoxcToken == "demo" && xoxdToken == "demo") {
		return "demo", nil, nil
	}

	startupJitter(logger)

	httpClient := transportpkg.ProvideHTTPClient(authProvider.Cookies(), logger)
	slackOpts := []slack.Option{slack.OptionHTTPClient(httpClient)}
	if os.Getenv("SLACK_MCP_GOVSLACK") == "true" {
		slackOpts = append(slackOpts, slack.OptionAPIURL("https://slack-gov.com/api/"))
	}
	slackClient := slack.New(authProvider.SlackToken(), slackOpts...)

	authResp, err := slackClient.AuthTest()
	if err != nil {
		return "", nil, err
	}

	logger.Info("Authenticated to Slack",
		zap.String("team", authResp.Team),
		zap.String("team_id", authResp.TeamID),
		zap.String("user", authResp.User))

	return authResp.TeamID, authResp, nil
}

func getCachePathWithTeamID(teamID, filename string) string {
	cacheDir := getCacheDir()
	if teamID != "" {
		return filepath.Join(cacheDir, teamID+"_"+filename)
	}
	return filepath.Join(cacheDir, filename)
}

// Random 0-3s sleep so concurrent instances do not stampede Slack.
func startupJitter(logger *zap.Logger) {
	jitter := time.Duration(rand.Intn(3000)) * time.Millisecond
	logger.Info("Startup jitter", zap.Duration("delay", jitter))
	time.Sleep(jitter)
}

func browserRuntimeStatePath() string {
	return filepath.Join(getCacheDir(), "browser_auth_runtime.json")
}

func writeBrowserRuntimeStatus(state, reason string, logger *zap.Logger) {
	status := browserRuntimeStatus{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		State:     state,
		Reason:    reason,
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		logger.Warn("Failed to marshal browser runtime status", zap.Error(err))
		return
	}
	if err := atomicWriteFile(browserRuntimeStatePath(), data, 0600); err != nil {
		logger.Warn("Failed to write browser runtime status", zap.Error(err))
	}
}

// Reason is argv to a fixed script (never interpolated into AppleScript source).
func osascriptNotificationArgs(reason string) []string {
	const maxRunes = 200
	if r := []rune(reason); len(r) > maxRunes {
		reason = string(r[:maxRunes]) + "…"
	}
	const script = `on run argv
	display notification (item 1 of argv) with title "Slack MCP fallback active"
end run`
	return []string{"-e", script, reason}
}

func notifyBrowserDegradation(reason string, logger *zap.Logger) {
	if err := exec.Command("osascript", osascriptNotificationArgs(reason)...).Run(); err != nil {
		logger.Debug("Failed to emit browser degradation notification", zap.Error(err))
	}
}

func isBrowserSessionAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	needles := []string{
		"invalid_auth",
		"not_authed",
		"token_revoked",
		"auth failed",
		"invalid auth token",
		"login",
		"cookie",
		"session",
	}
	for _, needle := range needles {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

type UsersCache struct {
	Users    map[string]slack.User `json:"users"`
	UsersInv map[string]string     `json:"users_inv"`
}

type ChannelsCache struct {
	Channels    map[string]Channel `json:"channels"`
	ChannelsInv map[string]string  `json:"channels_inv"`
}

type Channel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Topic       string   `json:"topic"`
	Purpose     string   `json:"purpose"`
	MemberCount int      `json:"memberCount"`
	IsMpIM      bool     `json:"mpim"`
	IsIM        bool     `json:"im"`
	IsPrivate   bool     `json:"private"`
	IsExtShared bool     `json:"is_ext_shared"`     // Shared with external organizations
	User        string   `json:"user,omitempty"`    // User ID for IM channels
	Members     []string `json:"members,omitempty"` // Member IDs for the channel
}

type SlackAPI interface {
	AuthTest() (*slack.AuthTestResponse, error)
	GetUsersContext(ctx context.Context, options ...slack.GetUsersOption) ([]slack.User, error)
	GetUsersInfo(users ...string) (*[]slack.User, error)
	MarkConversationContext(ctx context.Context, channel, ts string) error
	LeaveConversationContext(ctx context.Context, channelID string) (bool, error)
	GetConversationsContext(ctx context.Context, params *slack.GetConversationsParameters) ([]slack.Channel, string, error)

	ClientUserBoot(ctx context.Context) (*edge.ClientUserBootResponse, error)
	UsersSearch(ctx context.Context, query string, count int) ([]slack.User, error)
	ClientCounts(ctx context.Context) (edge.ClientCountsResponse, error)
	ActivityFeed(ctx context.Context, limit int) (edge.ActivityFeedResponse, error)
	ActivityMarkRead(ctx context.Context, itemType, feedTs, key string) error
	GetMutedChannels(ctx context.Context) (map[string]bool, error)
	SavedList(ctx context.Context, filter string, limit int, cursor string) (edge.SavedListResponse, error)
	SavedUpdate(ctx context.Context, itemType, itemID, ts, mark string, dateDue int64) error
	SavedClearCompleted(ctx context.Context) error

	GetStarredChannelIDs(ctx context.Context, limit int) ([]string, error)
}

type MCPSlackClient struct {
	slackClient *slack.Client
	edgeClient  *edge.Client

	authResponse *slack.AuthTestResponse
	authProvider auth.Provider
	logger       *zap.Logger

	isEnterprise bool
	isOAuth      bool
	isBotToken   bool

	browserState         atomic.Int32
	browserReason        atomic.Value
	browserNotifyOnce    sync.Once
	browserConfigured    bool
	oauthClientMu        sync.RWMutex
	oauthManager         *OAuthTokenManager
	oauthGeneration      atomic.Uint64
	oauthAccessToken     atomic.Value
	oauthRuntimeState    atomic.Value
	managedClientFactory func(context.Context, string) (*slack.Client, *slack.AuthTestResponse, error)
}

type ApiProvider struct {
	transport string
	client    SlackAPI
	logger    *zap.Logger

	cacheTTL           time.Duration
	minRefreshInterval time.Duration

	// Test-overridable; production always uses defaultBackgroundRefreshTimeout.
	backgroundRefreshTimeout time.Duration

	usersSnapshot  atomic.Pointer[UsersCache] // immutable snapshot; atomic load, no copy
	usersCachePath string
	usersReady     atomic.Bool
	usersMu        sync.RWMutex // refreshUsersInternal load path
	usersRefreshMu sync.Mutex
	usersRefresh   *usersRefreshCall

	channelsSnapshot          atomic.Pointer[ChannelsCache] // immutable snapshot; atomic load, no copy
	channelsCachePath         string
	channelsReady             atomic.Bool
	lastForcedChannelsRefresh time.Time
	channelsMu                sync.RWMutex // lastForcedChannelsRefresh
	channelsRefreshMu         sync.Mutex
	channelsRefresh           *channelsRefreshCall
}

type ProviderIdentity struct {
	TeamID       string `json:"team_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	EnterpriseID string `json:"enterprise_id,omitempty"`
	ActorType    string `json:"actor_type"`
	TokenMode    string `json:"token_mode"`
}

type usersRefreshCall struct {
	done chan struct{}
	err  error
}

type channelsRefreshCall struct {
	done chan struct{}
	err  error
}

func NewMCPSlackClient(authProvider auth.Provider, logger *zap.Logger, cachedAuth *slack.AuthTestResponse) (*MCPSlackClient, error) {
	httpClient := transportpkg.ProvideHTTPClient(authProvider.Cookies(), logger)

	slackOpts := []slack.Option{slack.OptionHTTPClient(httpClient)}
	if os.Getenv("SLACK_MCP_GOVSLACK") == "true" {
		slackOpts = append(slackOpts, slack.OptionAPIURL("https://slack-gov.com/api/"))
	}
	slackClient := slack.New(authProvider.SlackToken(), slackOpts...)

	var authResp *slack.AuthTestResponse
	if cachedAuth != nil {
		authResp = cachedAuth
	} else {
		var err error
		authResp, err = slackClient.AuthTest()
		if err != nil {
			return nil, err
		}
	}

	authResponse := &slack.AuthTestResponse{
		URL:          authResp.URL,
		Team:         authResp.Team,
		User:         authResp.User,
		TeamID:       authResp.TeamID,
		UserID:       authResp.UserID,
		EnterpriseID: authResp.EnterpriseID,
		BotID:        authResp.BotID,
	}

	slackClient = slack.New(authProvider.SlackToken(),
		slack.OptionHTTPClient(httpClient),
		slack.OptionAPIURL(authResp.URL+"api/"),
	)

	edgeClient, err := edge.NewWithInfo(authResponse, authProvider,
		edge.OptionHTTPClient(httpClient),
	)
	if err != nil {
		return nil, err
	}

	isEnterprise := authResp.EnterpriseID != ""
	token := authProvider.SlackToken()

	// xoxe.* are rotation variants of xoxp/xoxb (same scopes, ~12h expiry).
	isOAuth := strings.HasPrefix(token, "xoxp-") || strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxp-") || strings.HasPrefix(token, "xoxe.xoxb-")
	isBotToken := strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxb-")

	client := &MCPSlackClient{
		slackClient:  slackClient,
		edgeClient:   edgeClient,
		authResponse: authResponse,
		authProvider: authProvider,
		logger:       logger,
		isEnterprise: isEnterprise,
		isOAuth:      isOAuth,
		isBotToken:   isBotToken,
	}
	client.oauthAccessToken.Store(token)
	return client, nil
}

func (c *MCPSlackClient) standardSlackClient() *slack.Client {
	c.oauthClientMu.RLock()
	defer c.oauthClientMu.RUnlock()
	return c.slackClient
}

func (c *MCPSlackClient) browserFeaturesAvailable() bool {
	return browserRuntimeState(c.browserState.Load()) == browserStateActive
}

func (c *MCPSlackClient) browserDegradedReason() string {
	if reason, ok := c.browserReason.Load().(string); ok {
		return reason
	}
	return ""
}

func (c *MCPSlackClient) effectiveOAuth() bool {
	return c.isOAuth
}

func (c *MCPSlackClient) initBrowserState() {
	if c.isOAuth {
		c.browserState.Store(int32(browserStateOAuthOnly))
		browserStatusWriter("oauth_only", "", c.logger)
		return
	}
	c.browserState.Store(int32(browserStateActive))
	browserStatusWriter("browser_active", "", c.logger)
}

func (c *MCPSlackClient) degradeBrowserSession(reason error) {
	if c.isOAuth && !c.browserConfigured {
		return
	}
	if browserRuntimeState(c.browserState.Load()) == browserStateDegraded {
		return
	}
	reasonText := "browser-session Slack auth failed"
	if reason != nil {
		reasonText = reason.Error()
	}
	c.browserReason.Store(reasonText)
	c.browserState.Store(int32(browserStateDegraded))
	browserStatusWriter("browser_degraded", reasonText, c.logger)
	c.logger.Warn("Browser-session Slack auth degraded", zap.String("reason", reasonText), zap.Bool("standard_oauth", c.effectiveOAuth()))
	c.browserNotifyOnce.Do(func() {
		browserDegradationNotifier(reasonText, c.logger)
	})
}

func (c *MCPSlackClient) ensureBrowserFeature(feature string) error {
	if c.browserFeaturesAvailable() {
		return nil
	}
	reason := c.browserDegradedReason()
	if reason == "" {
		reason = "browser-session Slack auth is unavailable"
	}
	return fmt.Errorf("%w: %s (%s)", ErrBrowserSessionUnavailable, reason, feature)
}

func (c *MCPSlackClient) AuthTest() (*slack.AuthTestResponse, error) {
	if os.Getenv("SLACK_MCP_XOXP_TOKEN") == "demo" || (os.Getenv("SLACK_MCP_XOXC_TOKEN") == "demo" && os.Getenv("SLACK_MCP_XOXD_TOKEN") == "demo") {
		return &slack.AuthTestResponse{
			URL:          "https://_.slack.com",
			Team:         "Demo Team",
			User:         "Username",
			TeamID:       "TEAM123456",
			UserID:       "U1234567890",
			EnterpriseID: "",
			BotID:        "",
		}, nil
	}

	if c.authResponse != nil {
		return c.authResponse, nil
	}

	return c.slackClient.AuthTest()
}

func (c *MCPSlackClient) GetUsersContext(ctx context.Context, options ...slack.GetUsersOption) ([]slack.User, error) {
	return c.standardSlackClient().GetUsersContext(ctx, options...)
}

func (c *MCPSlackClient) GetUsersInfo(users ...string) (*[]slack.User, error) {
	return c.standardSlackClient().GetUsersInfo(users...)
}

func (c *MCPSlackClient) MarkConversationContext(ctx context.Context, channel, ts string) error {
	if c.IsBotToken() {
		return ErrUserOAuthRequired
	}
	return c.standardSlackClient().MarkConversationContext(ctx, channel, ts)
}

func (c *MCPSlackClient) LeaveConversationContext(ctx context.Context, channelID string) (bool, error) {
	if c.isEnterprise && !c.effectiveOAuth() {
		// Edge webclient path bypasses enterprise_is_restricted on session tokens.
		notInChannel, err := c.edgeClient.LeaveConversation(ctx, channelID)
		if err == nil {
			return notInChannel, nil
		}
	}
	return c.standardSlackClient().LeaveConversationContext(ctx, channelID)
}

func (c *MCPSlackClient) GetConversationsContext(ctx context.Context, params *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	std := c.standardSlackClient()
	// Enterprise + session: edge alone is partial (issue #73); merge with fully
	// paginated standard API and return empty cursor. OAuth / non-Enterprise: standard only.
	if c.isEnterprise {
		if c.effectiveOAuth() {
			return std.GetConversationsContext(ctx, params)
		}

		edgeChannels, _, edgeErr := c.edgeClient.GetConversationsContext(ctx, nil)
		if edgeErr != nil {
			return std.GetConversationsContext(ctx, params)
		}

		var channels []slack.Channel
		for _, ec := range edgeChannels {
			if params != nil && params.ExcludeArchived && ec.IsArchived {
				continue
			}
			channels = append(channels, slack.Channel{
				IsGeneral: ec.IsGeneral,
				GroupConversation: slack.GroupConversation{
					Conversation: slack.Conversation{
						ID:                 ec.ID,
						IsIM:               ec.IsIM,
						IsMpIM:             ec.IsMpIM,
						IsPrivate:          ec.IsPrivate,
						Created:            slack.JSONTime(ec.Created.Time().UnixMilli()),
						Unlinked:           ec.Unlinked,
						NameNormalized:     ec.NameNormalized,
						IsShared:           ec.IsShared,
						IsExtShared:        ec.IsExtShared,
						IsOrgShared:        ec.IsOrgShared,
						IsPendingExtShared: ec.IsPendingExtShared,
						NumMembers:         ec.NumMembers,
						User:               ec.User,
					},
					Name:       ec.Name,
					IsArchived: ec.IsArchived,
					Members:    ec.Members,
					Topic: slack.Topic{
						Value: ec.Topic.Value,
					},
					Purpose: slack.Purpose{
						Value: ec.Purpose.Value,
					},
				},
			})
		}

		return mergeStandardConversationPages(channels, params, func(p *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
			return std.GetConversationsContext(ctx, p)
		})
	}

	return std.GetConversationsContext(ctx, params)
}

func mergeStandardConversationPages(
	channels []slack.Channel,
	params *slack.GetConversationsParameters,
	fetchStd func(*slack.GetConversationsParameters) ([]slack.Channel, string, error),
) ([]slack.Channel, string, error) {
	seen := make(map[string]struct{}, len(channels))
	for _, ch := range channels {
		seen[ch.ID] = struct{}{}
	}

	stdParams := &slack.GetConversationsParameters{
		Limit:           999,
		ExcludeArchived: true,
	}
	if params != nil {
		stdParams.Types = params.Types
	}

	for {
		stdChannels, nextCur, stdErr := fetchStd(stdParams)
		if stdErr != nil {
			return channels, "", stdErr
		}
		for _, sc := range stdChannels {
			if _, ok := seen[sc.ID]; !ok {
				seen[sc.ID] = struct{}{}
				channels = append(channels, sc)
			}
		}
		if nextCur == "" {
			return channels, "", nil
		}
		stdParams.Cursor = nextCur
	}
}

func (c *MCPSlackClient) ClientUserBoot(ctx context.Context) (*edge.ClientUserBootResponse, error) {
	if err := c.ensureBrowserFeature("client.userBoot"); err != nil {
		return nil, err
	}
	return c.edgeClient.ClientUserBoot(ctx)
}

func (c *MCPSlackClient) UsersSearch(ctx context.Context, query string, count int) ([]slack.User, error) {
	if err := c.ensureBrowserFeature("users/search"); err != nil {
		return nil, err
	}
	users, err := c.edgeClient.UsersSearch(ctx, query, count)
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return nil, ErrBrowserSessionUnavailable
	}
	return users, err
}

func (c *MCPSlackClient) ClientCounts(ctx context.Context) (edge.ClientCountsResponse, error) {
	if err := c.ensureBrowserFeature("client.counts"); err != nil {
		return edge.ClientCountsResponse{}, err
	}
	resp, err := c.edgeClient.ClientCounts(ctx)
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return edge.ClientCountsResponse{}, ErrBrowserSessionUnavailable
	}
	return resp, err
}

func (c *MCPSlackClient) ActivityFeed(ctx context.Context, limit int) (edge.ActivityFeedResponse, error) {
	if err := c.ensureBrowserFeature("activity.feed"); err != nil {
		return edge.ActivityFeedResponse{}, err
	}
	resp, err := c.edgeClient.ActivityFeed(ctx, limit)
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return edge.ActivityFeedResponse{}, ErrBrowserSessionUnavailable
	}
	return resp, err
}

func (c *MCPSlackClient) ActivityMarkRead(ctx context.Context, itemType, feedTs, key string) error {
	if err := c.ensureBrowserFeature("activity.markRead"); err != nil {
		return err
	}
	err := c.edgeClient.ActivityMarkRead(ctx, itemType, feedTs, key)
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return ErrBrowserSessionUnavailable
	}
	return err
}

func (c *MCPSlackClient) GetMutedChannels(ctx context.Context) (map[string]bool, error) {
	if err := c.ensureBrowserFeature("users.prefs.get"); err != nil {
		return nil, err
	}
	resp, err := c.edgeClient.GetMutedChannels(ctx)
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return nil, ErrBrowserSessionUnavailable
	}
	return resp, err
}

func (c *MCPSlackClient) GetStarredChannelIDs(ctx context.Context, limit int) ([]string, error) {
	if c.effectiveOAuth() {
		params := slack.StarsParameters{
			Count: limit,
			Page:  1,
		}
		items, _, err := c.standardSlackClient().ListStarsContext(ctx, params)
		if err != nil {
			return nil, err
		}
		var channelIDs []string
		for _, item := range items {
			switch item.Type {
			case slack.TYPE_CHANNEL, slack.TYPE_IM, slack.TYPE_GROUP:
				if item.Channel != "" {
					channelIDs = append(channelIDs, item.Channel)
				}
			}
		}
		return channelIDs, nil
	}

	ub, err := c.edgeClient.ClientUserBoot(ctx)
	if err != nil {
		if isBrowserSessionAuthError(err) {
			c.degradeBrowserSession(err)
			return nil, ErrBrowserSessionUnavailable
		}
		return nil, err
	}
	var channelIDs []string
	for _, item := range ub.Starred {
		if id, ok := item.(string); ok {
			channelIDs = append(channelIDs, id)
		}
	}
	return channelIDs, nil
}

func (c *MCPSlackClient) SavedList(ctx context.Context, filter string, limit int, cursor string) (edge.SavedListResponse, error) {
	if err := c.ensureBrowserFeature("saved.list"); err != nil {
		return edge.SavedListResponse{}, err
	}
	resp, err := c.edgeClient.SavedList(ctx, filter, limit, cursor)
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return edge.SavedListResponse{}, ErrBrowserSessionUnavailable
	}
	return resp, err
}

func (c *MCPSlackClient) SavedUpdate(ctx context.Context, itemType, itemID, ts, mark string, dateDue int64) error {
	if err := c.ensureBrowserFeature("saved.update"); err != nil {
		return err
	}
	err := c.edgeClient.SavedUpdate(ctx, itemType, itemID, ts, mark, dateDue)
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return ErrBrowserSessionUnavailable
	}
	return err
}

func (c *MCPSlackClient) SavedClearCompleted(ctx context.Context) error {
	if err := c.ensureBrowserFeature("saved.clearCompleted"); err != nil {
		return err
	}
	err := c.edgeClient.SavedClearCompleted(ctx)
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return ErrBrowserSessionUnavailable
	}
	return err
}

func (c *MCPSlackClient) AuthResponse() *slack.AuthTestResponse {
	return c.authResponse
}

func (c *MCPSlackClient) IsBotToken() bool {
	return c.isBotToken
}

func (c *MCPSlackClient) IsOAuth() bool {
	return c.effectiveOAuth()
}

// ConfiguredWithBrowserSession reports construction-time xoxc/xoxd (or similar
// session) auth — not runtime OAuth after browser degrade.
// Use this for registering browser-only tools so mid-warmup degradation cannot
// hide activity/saved tools that still need session credentials when healthy.
func (c *MCPSlackClient) ConfiguredWithBrowserSession() bool {
	return c.browserConfigured || (!c.isOAuth && !c.isBotToken)
}

// attachBrowserSession adds browser-only Slack surfaces to an OAuth-primary
// client. Standard Web API calls continue to use the OAuth client.
func (c *MCPSlackClient) attachBrowserSession(browserAuth auth.ValueAuth, browserIdentity *slack.AuthTestResponse) error {
	if browserIdentity == nil || c.authResponse == nil {
		return errors.New("cannot verify OAuth and browser provider identities")
	}
	if browserIdentity.TeamID != c.authResponse.TeamID || browserIdentity.UserID != c.authResponse.UserID {
		return fmt.Errorf("browser provider identity mismatch: OAuth team/user %s/%s, browser team/user %s/%s",
			c.authResponse.TeamID, c.authResponse.UserID, browserIdentity.TeamID, browserIdentity.UserID)
	}
	httpClient := transportpkg.ProvideHTTPClient(browserAuth.Cookies(), c.logger)
	browserEdge, err := edge.NewWithInfo(browserIdentity, browserAuth, edge.OptionHTTPClient(httpClient))
	if err != nil {
		return fmt.Errorf("create browser provider: %w", err)
	}
	c.edgeClient = browserEdge
	c.browserConfigured = true
	c.browserState.Store(int32(browserStateActive))
	c.browserReason.Store("")
	browserStatusWriter("browser_active", "", c.logger)
	return nil
}

var loadBrowserCredential = func(ctx context.Context, account string) (BrowserTokenRecord, error) {
	store, err := NewBrowserCredentialStore(account)
	if err != nil {
		return BrowserTokenRecord{}, err
	}
	return store.Load(ctx)
}

type browserStartupCredentials struct {
	xoxc     string
	xoxd     string
	degraded error
}

func resolveBrowserStartupCredentials(ctx context.Context, account, oauthToken, xoxc, xoxd string) (browserStartupCredentials, error) {
	if account == "" {
		return browserStartupCredentials{xoxc: xoxc, xoxd: xoxd}, nil
	}
	record, err := loadBrowserCredential(ctx, account)
	if err == nil {
		return browserStartupCredentials{xoxc: record.XOXC, xoxd: record.XOXD}, nil
	}
	if oauthToken == "" {
		return browserStartupCredentials{}, err
	}
	return browserStartupCredentials{degraded: err}, nil
}

func New(transport string, logger *zap.Logger) *ApiProvider {
	var (
		authProvider auth.ValueAuth
		err          error
	)

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
			logger.Fatal("Failed to load managed OAuth credential", zap.Error(managedErr))
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
			logger.Fatal("Failed to load browser credential and no OAuth fallback is configured", zap.Error(loadErr))
		}
		xoxcToken, xoxdToken = browserCredentials.xoxc, browserCredentials.xoxd
		if browserCredentials.degraded != nil {
			logger.Warn("Browser credential unavailable; continuing with OAuth-only Slack tools", zap.Error(browserCredentials.degraded))
		}
	}

	// Supported Web API calls always prefer OAuth. Browser credentials are
	// attached only to browser-private Activity and Later surfaces.
	if xoxpToken != "" {
		authProvider, err = auth.NewValueAuth(xoxpToken, "")
		if err != nil {
			logger.Fatal("Failed to create auth provider with XOXP token", zap.Error(err))
		}
		ap := newWithXOXP(transport, authProvider, logger)
		if managedManager != nil {
			ap.startManagedOAuth(managedManager, managedRecord)
		}
		if !ap.IsBotToken() && xoxcToken != "" && xoxdToken != "" {
			attachBrowserToOAuth(ap, xoxcToken, xoxdToken, logger)
		}
		if xoxbToken != "" {
			logger.Warn("Both SLACK_MCP_XOXP_TOKEN and SLACK_MCP_XOXB_TOKEN are set; using user OAuth token")
		}
		return ap
	}

	if xoxbToken != "" {
		authProvider, err = auth.NewValueAuth(xoxbToken, "")
		if err != nil {
			logger.Fatal("Failed to create auth provider with XOXB token", zap.Error(err))
		}

		logger.Info("Using Bot token authentication",
			zap.String("context", "console"),
			zap.String("token_type", "xoxb"),
		)

		return newWithXOXP(transport, authProvider, logger)
	}

	if xoxcToken != "" && xoxdToken != "" {
		authProvider, err = auth.NewValueAuth(xoxcToken, xoxdToken)
		if err != nil {
			logger.Fatal("Failed to create auth provider with XOXC/XOXD tokens", zap.Error(err))
		}
		ap, startupErr := newWithXOXC(transport, authProvider, logger)
		if startupErr == nil {
			return ap
		}
		logger.Fatal("Authentication failed: browser-session tokens are invalid and no OAuth fallback is configured", zap.Error(startupErr))
	}

	logger.Fatal("Authentication required: Either SLACK_MCP_XOXP_TOKEN, SLACK_MCP_XOXB_TOKEN, or both SLACK_MCP_XOXC_TOKEN and SLACK_MCP_XOXD_TOKEN must be provided")
	return nil
}

func attachBrowserToOAuth(ap *ApiProvider, xoxcToken, xoxdToken string, logger *zap.Logger) {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil {
		return
	}
	browserAuth, err := auth.NewValueAuth(xoxcToken, xoxdToken)
	if err != nil {
		client.browserReason.Store("browser credentials are invalid")
		client.browserState.Store(int32(browserStateDegraded))
		return
	}
	_, browserIdentity, err := validateAuthAndGetTeamID(browserAuth, logger)
	if err == nil {
		err = client.attachBrowserSession(browserAuth, browserIdentity)
	}
	if err != nil {
		client.browserConfigured = true
		client.degradeBrowserSession(err)
	}
}

func newWithXOXP(transport string, authProvider auth.ValueAuth, logger *zap.Logger) *ApiProvider {
	var (
		client *MCPSlackClient
		err    error
	)

	teamID, cachedAuth, err := validateAuthAndGetTeamID(authProvider, logger)
	if err != nil {
		logger.Fatal("Authentication failed: check your Slack tokens", zap.Error(err))
	}

	usersCache := os.Getenv("SLACK_MCP_USERS_CACHE")
	if usersCache == "" {
		usersCache = getCachePathWithTeamID(teamID, "users_cache.json")
	}

	channelsCache := os.Getenv("SLACK_MCP_CHANNELS_CACHE")
	if channelsCache == "" {
		channelsCache = getCachePathWithTeamID(teamID, "channels_cache_v2.json")
	}

	if os.Getenv("SLACK_MCP_XOXP_TOKEN") == "demo" || (os.Getenv("SLACK_MCP_XOXC_TOKEN") == "demo" && os.Getenv("SLACK_MCP_XOXD_TOKEN") == "demo") {
		logger.Info("Demo credentials are set, skip.")
	} else {
		client, err = NewMCPSlackClient(authProvider, logger, cachedAuth)
		if err != nil {
			logger.Fatal("Failed to create MCP Slack client", zap.Error(err))
		}
		client.initBrowserState()
	}

	ap := &ApiProvider{
		transport: transport,
		client:    client,
		logger:    logger,

		cacheTTL:           getCacheTTL(),
		minRefreshInterval: getMinRefreshInterval(),

		backgroundRefreshTimeout: defaultBackgroundRefreshTimeout,

		usersCachePath:    usersCache,
		channelsCachePath: channelsCache,
	}
	ap.usersSnapshot.Store(&UsersCache{
		Users:    make(map[string]slack.User),
		UsersInv: make(map[string]string),
	})
	ap.channelsSnapshot.Store(&ChannelsCache{
		Channels:    make(map[string]Channel),
		ChannelsInv: make(map[string]string),
	})
	return ap
}

func newWithXOXC(transport string, authProvider auth.ValueAuth, logger *zap.Logger) (*ApiProvider, error) {
	var (
		client *MCPSlackClient
		err    error
	)

	teamID, cachedAuth, err := validateAuthAndGetTeamID(authProvider, logger)
	if err != nil {
		return nil, err
	}

	usersCache := os.Getenv("SLACK_MCP_USERS_CACHE")
	if usersCache == "" {
		usersCache = getCachePathWithTeamID(teamID, "users_cache.json")
	}

	channelsCache := os.Getenv("SLACK_MCP_CHANNELS_CACHE")
	if channelsCache == "" {
		channelsCache = getCachePathWithTeamID(teamID, "channels_cache_v2.json")
	}

	if os.Getenv("SLACK_MCP_XOXP_TOKEN") == "demo" || (os.Getenv("SLACK_MCP_XOXC_TOKEN") == "demo" && os.Getenv("SLACK_MCP_XOXD_TOKEN") == "demo") {
		logger.Info("Demo credentials are set, skip.")
	} else {
		client, err = NewMCPSlackClient(authProvider, logger, cachedAuth)
		if err != nil {
			return nil, err
		}
		client.initBrowserState()
	}

	ap := &ApiProvider{
		transport: transport,
		client:    client,
		logger:    logger,

		cacheTTL:           getCacheTTL(),
		minRefreshInterval: getMinRefreshInterval(),

		backgroundRefreshTimeout: defaultBackgroundRefreshTimeout,

		usersCachePath:    usersCache,
		channelsCachePath: channelsCache,
	}
	ap.usersSnapshot.Store(&UsersCache{
		Users:    make(map[string]slack.User),
		UsersInv: make(map[string]string),
	})
	ap.channelsSnapshot.Store(&ChannelsCache{
		Channels:    make(map[string]Channel),
		ChannelsInv: make(map[string]string),
	})
	return ap, nil
}

func (ap *ApiProvider) RefreshUsers(ctx context.Context) error {
	return ap.refreshUsersInternal(ctx, false)
}

// PatchUser merges one users.info result into the in-memory snapshot (no disk write).
func (ap *ApiProvider) PatchUser(ctx context.Context, userID string) (*slack.User, error) {
	usersInfo, err := ap.client.GetUsersInfo(userID)
	if err != nil {
		ap.logger.Warn("Failed to fetch user for cache patch", zap.String("user_id", userID), zap.Error(err))
		return nil, err
	}
	if usersInfo == nil || len(*usersInfo) == 0 {
		ap.logger.Debug("User not found via API", zap.String("user_id", userID))
		return nil, errors.New("user not found")
	}

	user := (*usersInfo)[0]

	for {
		current := ap.usersSnapshot.Load()
		usersLen, invLen := 0, 0
		if current != nil {
			usersLen = len(current.Users)
			invLen = len(current.UsersInv)
		}

		newSnapshot := &UsersCache{
			Users:    make(map[string]slack.User, usersLen+1),
			UsersInv: make(map[string]string, invLen+1),
		}
		if current != nil {
			for k, v := range current.Users {
				newSnapshot.Users[k] = v
			}
			for k, v := range current.UsersInv {
				if v == user.ID {
					continue // drop stale name→ID before rename
				}
				newSnapshot.UsersInv[k] = v
			}
		}
		newSnapshot.Users[user.ID] = user
		newSnapshot.UsersInv[user.Name] = user.ID

		if ap.usersSnapshot.CompareAndSwap(current, newSnapshot) {
			break
		}
	}

	ap.logger.Debug("Patched user into cache",
		zap.String("user_id", user.ID),
		zap.String("user_name", user.Name))

	return &user, nil
}

func (ap *ApiProvider) refreshUsersInternal(ctx context.Context, force bool) error {
	ap.usersMu.Lock()

	if !force {
		if data, err := os.ReadFile(ap.usersCachePath); err == nil {
			var cachedUsers []slack.User
			if err := json.Unmarshal(data, &cachedUsers); err != nil {
				ap.logger.Warn("Failed to unmarshal users cache, will refetch",
					zap.String("cache_file", ap.usersCachePath),
					zap.Error(err))
			} else if len(cachedUsers) == 0 {
				ap.logger.Warn("Cache file contains zero users, treating as cache miss",
					zap.String("cache_file", ap.usersCachePath))
			} else {
				newSnapshot := &UsersCache{
					Users:    make(map[string]slack.User, len(cachedUsers)),
					UsersInv: make(map[string]string, len(cachedUsers)),
				}
				for _, u := range cachedUsers {
					newSnapshot.Users[u.ID] = u
					newSnapshot.UsersInv[u.Name] = u.ID
				}
				ap.usersSnapshot.Store(newSnapshot)
				ap.usersReady.Store(true)

				cacheExpired := false
				if ap.cacheTTL > 0 {
					if fileInfo, err := os.Stat(ap.usersCachePath); err == nil {
						cacheAge := time.Since(fileInfo.ModTime())
						if cacheAge > ap.cacheTTL {
							cacheExpired = true
							ap.logger.Info("Serving stale users cache, background refresh starting",
								zap.Duration("cache_age", cacheAge),
								zap.Duration("ttl", ap.cacheTTL),
								zap.Int("count", len(cachedUsers)),
								zap.String("cache_file", ap.usersCachePath))
						}
					}
				}

				if !cacheExpired {
					ap.logger.Info("Loaded users from cache",
						zap.Int("count", len(cachedUsers)),
						zap.String("cache_file", ap.usersCachePath))
					ap.usersMu.Unlock()
					return nil
				}

				ap.usersMu.Unlock()
				ap.spawnBackgroundUsersRefresh()
				return nil
			}
		}
	}

	ap.usersMu.Unlock()
	return ap.refreshUsers(ctx)
}

func (ap *ApiProvider) spawnBackgroundUsersRefresh() {
	call, leader := ap.beginUsersRefresh()
	if !leader {
		ap.logger.Debug("Skipping background users refresh, already in progress")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), ap.backgroundRefreshTimeout)
		defer cancel()

		if err := ap.runUsersRefresh(ctx, call); err != nil {
			ap.logger.Warn("Background users refresh failed, continuing with stale data",
				zap.Error(err))
		}
	}()
}

func (ap *ApiProvider) refreshUsers(ctx context.Context) error {
	for {
		call, leader := ap.beginUsersRefresh()
		if leader {
			return ap.runUsersRefresh(ctx, call)
		}

		select {
		case <-call.done:
			if call.err == nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// Preserve the old serialized retry behavior after a failed
			// predecessor while coalescing successful overlapping refreshes.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ap *ApiProvider) beginUsersRefresh() (*usersRefreshCall, bool) {
	ap.usersRefreshMu.Lock()
	defer ap.usersRefreshMu.Unlock()

	if ap.usersRefresh != nil {
		return ap.usersRefresh, false
	}

	call := &usersRefreshCall{done: make(chan struct{})}
	ap.usersRefresh = call
	return call, true
}

func (ap *ApiProvider) runUsersRefresh(ctx context.Context, call *usersRefreshCall) error {
	err := ap.fetchAndStoreUsers(ctx)

	ap.usersRefreshMu.Lock()
	call.err = err
	ap.usersRefresh = nil
	close(call.done)
	ap.usersRefreshMu.Unlock()

	return err
}

func (ap *ApiProvider) fetchAndStoreUsers(ctx context.Context) error {
	users, err := ap.client.GetUsersContext(ctx, slack.GetUsersOptionLimit(1000))
	if err != nil {
		ap.logger.Error("Failed to fetch users", zap.Error(err))
		return err
	}

	if len(users) == 0 {
		if ap.usersReady.Load() {
			ap.logger.Warn("API returned zero users, keeping existing cache")
			return nil
		}
		return errors.New("API returned zero users and no existing cache is available")
	}

	list := users

	if ap.IsOAuth() {
		ap.logger.Debug("Skipping Slack Connect enrichment (OAuth token, browser features unavailable)")
	} else {
		connectUsers, err := ap.GetSlackConnect(ctx)
		if err != nil {
			ap.logger.Warn("Slack Connect enrichment failed; continuing with standard user list",
				zap.Error(err))
		} else {
			list = append(list, connectUsers...)
		}
	}

	// Single publish after enrichment so concurrent PatchUser CAS is not
	// clobbered by an intermediate Store of the pre-Connect snapshot.
	finalSnapshot := &UsersCache{
		Users:    make(map[string]slack.User, len(list)),
		UsersInv: make(map[string]string, len(list)),
	}
	for _, user := range list {
		finalSnapshot.Users[user.ID] = user
		finalSnapshot.UsersInv[user.Name] = user.ID
	}
	ap.usersSnapshot.Store(finalSnapshot)

	if data, err := json.MarshalIndent(list, "", "  "); err != nil {
		ap.logger.Error("Failed to marshal users for cache", zap.Error(err))
	} else if err := atomicWriteFile(ap.usersCachePath, data, 0600); err != nil {
		ap.logger.Error("Failed to write cache file",
			zap.String("cache_file", ap.usersCachePath),
			zap.Error(err))
	} else {
		ap.logger.Info("Wrote users to cache",
			zap.Int("count", len(list)),
			zap.String("cache_file", ap.usersCachePath))
	}

	ap.usersReady.Store(true)

	return nil
}

func (ap *ApiProvider) RefreshChannels(ctx context.Context) error {
	return ap.refreshChannelsInternal(ctx, false)
}

// ForceRefreshChannels bypasses cache; rate-limited by minRefreshInterval (ErrRefreshRateLimited).
func (ap *ApiProvider) ForceRefreshChannels(ctx context.Context) error {
	if ap.minRefreshInterval > 0 {
		// Check-and-stamp under one lock to avoid TOCTOU on concurrent forces.
		ap.channelsMu.Lock()
		sinceLast := time.Since(ap.lastForcedChannelsRefresh)
		if sinceLast < ap.minRefreshInterval {
			ap.channelsMu.Unlock()
			ap.logger.Debug("Skipping forced channels refresh, within rate limit",
				zap.Duration("since_last", sinceLast),
				zap.Duration("min_interval", ap.minRefreshInterval))
			return ErrRefreshRateLimited
		}
		ap.lastForcedChannelsRefresh = time.Now()
		ap.channelsMu.Unlock()
	}

	ap.logger.Info("Force refreshing channels cache")
	return ap.refreshChannelsInternal(ctx, true)
}

func (ap *ApiProvider) refreshChannelsInternal(ctx context.Context, force bool) error {
	ap.channelsMu.Lock()

	if !force {
		if data, err := os.ReadFile(ap.channelsCachePath); err == nil {
			var cachedChannels []Channel
			if err := json.Unmarshal(data, &cachedChannels); err != nil {
				ap.logger.Warn("Failed to unmarshal channels cache, will refetch",
					zap.String("cache_file", ap.channelsCachePath),
					zap.Error(err))
			} else if len(cachedChannels) == 0 {
				ap.logger.Warn("Cache file contains zero channels, treating as cache miss",
					zap.String("cache_file", ap.channelsCachePath))
			} else {
				// Re-map IMs against current users so @names stay fresh after user cache updates.
				usersMap := ap.ProvideUsersMap().Users
				newSnapshot := &ChannelsCache{
					Channels:    make(map[string]Channel, len(cachedChannels)),
					ChannelsInv: make(map[string]string, len(cachedChannels)),
				}
				for _, c := range cachedChannels {
					if c.IsIM {
						remappedChannel := mapChannel(
							c.ID, "", "", c.Topic, c.Purpose,
							c.User, c.Members, c.MemberCount,
							c.IsIM, c.IsMpIM, c.IsPrivate, c.IsExtShared,
							usersMap,
						)
						newSnapshot.Channels[c.ID] = remappedChannel
						newSnapshot.ChannelsInv[remappedChannel.Name] = c.ID
					} else {
						newSnapshot.Channels[c.ID] = c
						newSnapshot.ChannelsInv[c.Name] = c.ID
					}
				}
				ap.channelsSnapshot.Store(newSnapshot)
				ap.channelsReady.Store(true)

				cacheExpired := false
				if ap.cacheTTL > 0 {
					if fileInfo, err := os.Stat(ap.channelsCachePath); err == nil {
						cacheAge := time.Since(fileInfo.ModTime())
						if cacheAge > ap.cacheTTL {
							cacheExpired = true
							ap.logger.Info("Serving stale channels cache, background refresh starting",
								zap.Duration("cache_age", cacheAge),
								zap.Duration("ttl", ap.cacheTTL),
								zap.Int("count", len(cachedChannels)),
								zap.String("cache_file", ap.channelsCachePath))
						}
					}
				}

				if !cacheExpired {
					ap.logger.Info("Loaded channels from cache and re-mapped DM names",
						zap.Int("count", len(cachedChannels)),
						zap.String("cache_file", ap.channelsCachePath))
					ap.channelsMu.Unlock()
					return nil
				}

				ap.channelsMu.Unlock()
				ap.spawnBackgroundChannelsRefresh()
				return nil
			}
		}
	}

	ap.channelsMu.Unlock()
	return ap.refreshChannels(ctx)
}

func (ap *ApiProvider) spawnBackgroundChannelsRefresh() {
	call, leader := ap.beginChannelsRefresh()
	if !leader {
		ap.logger.Debug("Skipping background channels refresh, already in progress")
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), ap.backgroundRefreshTimeout)
		defer cancel()

		if err := ap.runChannelsRefresh(ctx, call); err != nil {
			ap.logger.Warn("Background channels refresh failed, continuing with stale data",
				zap.Error(err))
		}
	}()
}

func (ap *ApiProvider) refreshChannels(ctx context.Context) error {
	for {
		call, leader := ap.beginChannelsRefresh()
		if leader {
			return ap.runChannelsRefresh(ctx, call)
		}

		select {
		case <-call.done:
			if call.err == nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// Preserve the old serialized retry behavior after a failed
			// predecessor while coalescing successful overlapping refreshes.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ap *ApiProvider) beginChannelsRefresh() (*channelsRefreshCall, bool) {
	ap.channelsRefreshMu.Lock()
	defer ap.channelsRefreshMu.Unlock()

	if ap.channelsRefresh != nil {
		return ap.channelsRefresh, false
	}

	call := &channelsRefreshCall{done: make(chan struct{})}
	ap.channelsRefresh = call
	return call, true
}

func (ap *ApiProvider) runChannelsRefresh(ctx context.Context, call *channelsRefreshCall) error {
	err := ap.fetchAndStoreChannels(ctx)

	ap.channelsRefreshMu.Lock()
	call.err = err
	ap.channelsRefresh = nil
	close(call.done)
	ap.channelsRefreshMu.Unlock()

	return err
}

func (ap *ApiProvider) fetchAndStoreChannels(ctx context.Context) error {
	channels, err := ap.GetChannels(ctx, AllChanTypes)

	// Prefer the real fetch error over a misleading "zero channels" when page 1 fails.
	if err != nil {
		if ap.channelsReady.Load() {
			ap.logger.Warn("Channel fetch incomplete, keeping existing cache",
				zap.Int("partialCount", len(channels)),
				zap.Error(err))
			return nil
		}
		return fmt.Errorf("channel fetch incomplete and no existing cache is available: %w", err)
	}

	if len(channels) == 0 {
		if ap.channelsReady.Load() {
			ap.logger.Warn("API returned zero channels, keeping existing cache")
			return nil
		}
		return errors.New("API returned zero channels and no existing cache is available")
	}

	if data, err := json.MarshalIndent(channels, "", "  "); err != nil {
		ap.logger.Error("Failed to marshal channels for cache", zap.Error(err))
	} else if err := atomicWriteFile(ap.channelsCachePath, data, 0600); err != nil {
		ap.logger.Error("Failed to write cache file",
			zap.String("cache_file", ap.channelsCachePath),
			zap.Error(err))
	} else {
		ap.logger.Info("Wrote channels to cache",
			zap.Int("count", len(channels)),
			zap.String("cache_file", ap.channelsCachePath))
	}

	ap.channelsReady.Store(true)

	return nil
}

func (ap *ApiProvider) GetSlackConnect(ctx context.Context) ([]slack.User, error) {
	boot, err := ap.client.ClientUserBoot(ctx)
	if err != nil {
		ap.logger.Error("Failed to fetch client user boot", zap.Error(err))
		return nil, err
	}

	usersSnapshot := ap.usersSnapshot.Load()
	var collectedIDs []string
	for _, im := range boot.IMs {
		if !im.IsShared && !im.IsExtShared {
			continue
		}

		_, ok := usersSnapshot.Users[im.User]
		if !ok {
			collectedIDs = append(collectedIDs, im.User)
		}
	}

	res := make([]slack.User, 0, len(collectedIDs))
	if len(collectedIDs) > 0 {
		usersInfo, err := ap.client.GetUsersInfo(strings.Join(collectedIDs, ","))
		if err != nil {
			ap.logger.Error("Failed to fetch users info for shared IMs", zap.Error(err))
			return nil, err
		}

		for _, u := range *usersInfo {
			res = append(res, u)
		}
	}

	return res, nil
}

// Retry-After for Slack rate limits; 0 means not retryable (limiter.CallWithRetry callback).
func slackRetryAfter(err error) time.Duration {
	var rle *slack.RateLimitedError
	if errors.As(err, &rle) {
		return rle.RetryAfter
	}
	return 0
}

// Wraps channels+cursor so CallWithRetry (one T) can paginate GetConversationsContext.
type channelsPageResult struct {
	channels []slack.Channel
	cursor   string
}

// Partial pages returned on error — callers must not treat that as a complete list.
func (ap *ApiProvider) getChannelsMultiType(ctx context.Context, channelTypes []string) ([]Channel, error) {
	params := &slack.GetConversationsParameters{
		Types:           channelTypes,
		Limit:           999,
		ExcludeArchived: true,
	}

	var chans []Channel

	usersMap := ap.ProvideUsersMap().Users
	// Tier2 matches conversations.list; faster budgets caused 429 truncations.
	lim := limiter.Tier2.Limiter()

	for {
		// CallWithRetry already paces; do not wait again in this loop.
		page, err := limiter.CallWithRetry(ctx, lim, 2, slackRetryAfter,
			func() (channelsPageResult, error) {
				c, cur, err := ap.client.GetConversationsContext(ctx, params)
				return channelsPageResult{channels: c, cursor: cur}, err
			})
		ap.logger.Debug("Fetched channels",
			zap.Strings("channelTypes", channelTypes),
			zap.Int("count", len(page.channels)),
		)
		if err != nil {
			ap.logger.Error("Failed to fetch channels, returning partial result",
				zap.Strings("channelTypes", channelTypes),
				zap.Int("collectedSoFar", len(chans)),
				zap.Error(err))
			return chans, err
		}

		for _, channel := range page.channels {
			ch := mapChannel(
				channel.ID,
				channel.Name,
				channel.NameNormalized,
				channel.Topic.Value,
				channel.Purpose.Value,
				channel.User,
				channel.Members,
				channel.NumMembers,
				channel.IsIM,
				channel.IsMpIM,
				channel.IsPrivate,
				channel.IsExtShared,
				usersMap,
			)
			chans = append(chans, ch)
		}

		if page.cursor == "" {
			break
		}

		params.Cursor = page.cursor
	}
	return chans, nil
}

func (ap *ApiProvider) GetChannels(ctx context.Context, channelTypes []string) ([]Channel, error) {
	if len(channelTypes) == 0 {
		channelTypes = AllChanTypes
	}

	chans, err := ap.getChannelsMultiType(ctx, channelTypes)
	if err != nil {
		// Incomplete page must not replace a good snapshot.
		return chans, err
	}

	newSnapshot := &ChannelsCache{
		Channels:    make(map[string]Channel, len(chans)),
		ChannelsInv: make(map[string]string, len(chans)),
	}
	for _, ch := range chans {
		newSnapshot.Channels[ch.ID] = ch
		newSnapshot.ChannelsInv[ch.Name] = ch.ID
	}
	ap.channelsSnapshot.Store(newSnapshot)

	return chans, nil
}

func (ap *ApiProvider) ProvideUsersMap() *UsersCache {
	return ap.usersSnapshot.Load()
}

func (ap *ApiProvider) ProvideChannelsMaps() *ChannelsCache {
	return ap.channelsSnapshot.Load()
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
func (ap *ApiProvider) WebAPI() *slack.Client {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil {
		return nil
	}
	return client.standardSlackClient()
}

func (ap *ApiProvider) IsBotToken() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.IsBotToken()
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

func (ap *ApiProvider) OAuthCredentialStatus() OAuthCredentialStatus {
	status := OAuthStatusFromEnvironment()
	client, ok := ap.client.(*MCPSlackClient)
	if ok && client != nil {
		if state, loaded := client.oauthRuntimeState.Load().(string); loaded && state != "" {
			status.State = state
			if state == "live" {
				status.Reason = ""
			}
		}
	}
	return status
}

func (ap *ApiProvider) startManagedOAuth(manager *OAuthTokenManager, record OAuthTokenRecord) {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil {
		return
	}
	expected := client.AuthResponse()
	manager.WithValidator(func(ctx context.Context, rotated OAuthTokenRecord) error {
		_, identity, err := client.managedSlackClient(ctx, rotated.AccessToken)
		if err != nil {
			return err
		}
		if expected == nil || identity.TeamID != expected.TeamID || identity.UserID != expected.UserID {
			return fmt.Errorf("rotated OAuth identity mismatch")
		}
		return nil
	})
	client.oauthManager = manager
	client.oauthGeneration.Store(record.Generation)
	client.oauthRuntimeState.Store("live")
	go client.runManagedOAuthRefresh()
}

func (c *MCPSlackClient) managedSlackClient(ctx context.Context, token string) (*slack.Client, *slack.AuthTestResponse, error) {
	if c.managedClientFactory != nil {
		return c.managedClientFactory(ctx, token)
	}
	authProvider, err := auth.NewValueAuth(token, "")
	if err != nil {
		return nil, nil, err
	}
	httpClient := transportpkg.ProvideHTTPClient(authProvider.Cookies(), c.logger)
	options := []slack.Option{slack.OptionHTTPClient(httpClient)}
	if c.authResponse != nil && c.authResponse.URL != "" {
		options = append(options, slack.OptionAPIURL(c.authResponse.URL+"api/"))
	} else if os.Getenv("SLACK_MCP_GOVSLACK") == "true" {
		options = append(options, slack.OptionAPIURL("https://slack-gov.com/api/"))
	}
	client := slack.New(token, options...)
	identity, err := client.AuthTestContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	return client, identity, nil
}

func (c *MCPSlackClient) refreshManagedOAuthOnce(ctx context.Context) error {
	if c.oauthManager == nil {
		return nil
	}
	record, err := c.oauthManager.Current(ctx)
	if err != nil {
		c.oauthRuntimeState.Store("degraded")
		return err
	}
	if record.Generation == c.oauthGeneration.Load() {
		return nil
	}
	replacement, _, err := c.managedSlackClient(ctx, record.AccessToken)
	if err != nil {
		c.oauthRuntimeState.Store("degraded")
		return err
	}
	c.oauthClientMu.Lock()
	c.slackClient = replacement
	c.oauthClientMu.Unlock()
	c.oauthGeneration.Store(record.Generation)
	c.oauthAccessToken.Store(record.AccessToken)
	c.oauthRuntimeState.Store("live")
	return nil
}

func (c *MCPSlackClient) runManagedOAuthRefresh() {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for range timer.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := c.refreshManagedOAuthOnce(ctx)
		cancel()
		if err != nil {
			c.logger.Warn("Managed OAuth refresh failed", zap.Error(err))
		}
		timer.Reset(managedOAuthRefreshInterval(err))
	}
}

func managedOAuthRefreshInterval(err error) time.Duration {
	const (
		base = time.Minute
		max  = 15 * time.Minute
	)
	retryAfter := slackRetryAfter(err)
	if retryAfter <= base {
		return base
	}
	if retryAfter > max {
		return max
	}
	return retryAfter
}

func (ap *ApiProvider) IsOAuth() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.IsOAuth()
}

func (ap *ApiProvider) ConfiguredWithBrowserSession() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.ConfiguredWithBrowserSession()
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

func (ap *ApiProvider) BrowserFeaturesAvailable() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.browserFeaturesAvailable()
}

func (ap *ApiProvider) BrowserDegradedReason() string {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil {
		return ""
	}
	return client.browserDegradedReason()
}

func (ap *ApiProvider) BrowserCredentialSource() string {
	if strings.TrimSpace(os.Getenv("SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT")) != "" {
		return "macos_keychain"
	}
	if os.Getenv("SLACK_MCP_XOXC_TOKEN") != "" && os.Getenv("SLACK_MCP_XOXD_TOKEN") != "" {
		return "environment_compatibility"
	}
	return "not_configured"
}

var slackUserIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)

// ID query → users.info; OAuth → local cache regex; browser → edge UsersSearch.
func (ap *ApiProvider) SearchUsers(ctx context.Context, query string, limit int) ([]slack.User, error) {
	if slackUserIDPattern.MatchString(query) {
		users, err := ap.client.GetUsersInfo(query)
		if err != nil {
			return nil, err
		}
		if users != nil {
			return *users, nil
		}
		return nil, nil
	}

	if ap.IsOAuth() {
		return ap.searchUsersInCache(query, limit)
	}

	users, err := ap.client.UsersSearch(ctx, query, limit)
	if errors.Is(err, ErrBrowserSessionUnavailable) && ap.IsOAuth() {
		return ap.searchUsersInCache(query, limit)
	}
	return users, err
}

func (ap *ApiProvider) searchUsersInCache(query string, limit int) ([]slack.User, error) {
	if !ap.usersReady.Load() {
		return nil, ErrUsersNotReady
	}

	pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(query))
	if err != nil {
		return nil, err
	}

	usersCache := ap.usersSnapshot.Load()
	var results []slack.User
	for _, user := range usersCache.Users {
		if user.Deleted {
			continue
		}

		if pattern.MatchString(user.Name) ||
			pattern.MatchString(user.RealName) ||
			pattern.MatchString(user.Profile.DisplayName) ||
			pattern.MatchString(user.Profile.Email) {
			results = append(results, user)

			if len(results) >= limit {
				break
			}
		}
	}

	return results, nil
}

func mapChannel(
	id, name, nameNormalized, topic, purpose, user string,
	members []string,
	numMembers int,
	isIM, isMpIM, isPrivate, isExtShared bool,
	usersMap map[string]slack.User,
) Channel {
	channelName := name
	finalPurpose := purpose
	finalTopic := topic
	finalMemberCount := numMembers

	var userID string
	if isIM {
		finalMemberCount = 2
		userID = user

		// Some payloads omit User; recover peer ID from members when present.
		if userID == "" && len(members) > 0 {
			for _, memberID := range members {
				if _, ok := usersMap[memberID]; ok {
					userID = memberID
					break
				}
			}
		}

		if u, ok := usersMap[userID]; ok {
			channelName = "@" + u.Name
			finalPurpose = "DM with " + u.RealName
		} else if userID != "" {
			channelName = "@" + userID
			finalPurpose = "DM with " + userID
		} else {
			channelName = "@"
			finalPurpose = "DM with "
		}
		finalTopic = ""
	} else if isMpIM {
		if len(members) > 0 {
			finalMemberCount = len(members)
			var userNames []string
			for _, uid := range members {
				if u, ok := usersMap[uid]; ok {
					userNames = append(userNames, u.RealName)
				} else {
					userNames = append(userNames, uid)
				}
			}
			channelName = "@" + nameNormalized
			finalPurpose = "Group DM with " + strings.Join(userNames, ", ")
			finalTopic = ""
		}
	} else {
		channelName = "#" + nameNormalized
	}

	return Channel{
		ID:          id,
		Name:        channelName,
		Topic:       finalTopic,
		Purpose:     finalPurpose,
		MemberCount: finalMemberCount,
		IsIM:        isIM,
		IsMpIM:      isMpIM,
		IsPrivate:   isPrivate,
		IsExtShared: isExtShared,
		User:        userID,
		Members:     members,
	}
}

func MapChannelFromSlack(c slack.Channel, usersMap map[string]slack.User) Channel {
	return mapChannel(
		c.ID, c.Name, c.NameNormalized,
		c.Topic.Value, c.Purpose.Value,
		c.User, c.Members, c.NumMembers,
		c.IsIM, c.IsMpIM, c.IsPrivate, c.IsExtShared,
		usersMap,
	)
}
