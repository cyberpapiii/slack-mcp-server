package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
const defaultCacheTTL = 24 * time.Hour
const defaultMinRefreshInterval = 30 * time.Second

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

// atomicWriteFile writes data to a file atomically using a temp file and rename.
// Uses os.CreateTemp for unpredictable temp file names (prevents symlink attacks)
// and cleans up the temp file on rename failure.
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

// getCacheDir returns the appropriate cache directory for slack-mcp-server
func getCacheDir() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		// Fallback to current directory if we can't get user cache dir
		return "."
	}

	dir := filepath.Join(cacheDir, "slack-mcp-server")
	if err := os.MkdirAll(dir, 0700); err != nil {
		// Fallback to current directory if we can't create cache dir
		return "."
	}
	return dir
}

// getCacheTTL returns the cache TTL from SLACK_MCP_CACHE_TTL env var or default (24 hours).
// Supports formats: "1h", "30m", "3600" (seconds), "0" (disable TTL, cache forever)
// Negative values are rejected and fall back to default.
func getCacheTTL() time.Duration {
	ttlStr := os.Getenv("SLACK_MCP_CACHE_TTL")
	if ttlStr == "" {
		return defaultCacheTTL
	}

	// Try parsing as duration first (e.g., "1h", "30m")
	if d, err := time.ParseDuration(ttlStr); err == nil {
		if d < 0 {
			return defaultCacheTTL // Reject negative TTL
		}
		return d
	}

	// Try parsing as seconds (e.g., "3600")
	if secs, err := strconv.ParseInt(ttlStr, 10, 64); err == nil {
		if secs < 0 {
			return defaultCacheTTL // Reject negative TTL
		}
		return time.Duration(secs) * time.Second
	}

	return defaultCacheTTL
}

// getMinRefreshInterval returns the minimum interval between forced refreshes from
// SLACK_MCP_MIN_REFRESH_INTERVAL env var or default (30s).
// Supports formats: "30s", "1m", "60" (seconds), "0" (disable rate limiting)
// Negative values are rejected and fall back to default.
func getMinRefreshInterval() time.Duration {
	intervalStr := os.Getenv("SLACK_MCP_MIN_REFRESH_INTERVAL")
	if intervalStr == "" {
		return defaultMinRefreshInterval
	}

	// Try parsing as duration first (e.g., "30s", "1m")
	if d, err := time.ParseDuration(intervalStr); err == nil {
		if d < 0 {
			return defaultMinRefreshInterval // Reject negative interval
		}
		return d
	}

	// Try parsing as seconds (e.g., "60")
	if secs, err := strconv.ParseInt(intervalStr, 10, 64); err == nil {
		if secs < 0 {
			return defaultMinRefreshInterval // Reject negative interval
		}
		return time.Duration(secs) * time.Second
	}

	return defaultMinRefreshInterval
}

// validateAuthAndGetTeamID performs auth validation on startup and returns the TeamID.
// This ensures tokens are valid before proceeding and enables cache namespacing
// to prevent cache contamination when using multiple Slack workspaces.
// Returns an error if authentication fails - the server should not start with invalid credentials.
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

// getCachePathWithTeamID returns a cache file path prefixed with TeamID for workspace isolation.
// If TeamID is empty, returns the default filename without prefix.
func getCachePathWithTeamID(teamID, filename string) string {
	cacheDir := getCacheDir()
	if teamID != "" {
		return filepath.Join(cacheDir, teamID+"_"+filename)
	}
	return filepath.Join(cacheDir, filename)
}

// startupJitter sleeps for a random 0-3s to stagger concurrent instance API calls.
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

func notifyBrowserDegradation(reason string, logger *zap.Logger) {
	if err := exec.Command("osascript", "-e", fmt.Sprintf(`display notification "%s" with title "Slack MCP fallback active"`,
		strings.ReplaceAll(reason, `"`, `\"`),
	)).Run(); err != nil {
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
	// Standard slack-go API methods
	AuthTest() (*slack.AuthTestResponse, error)
	AuthTestContext(ctx context.Context) (*slack.AuthTestResponse, error)
	GetUsersContext(ctx context.Context, options ...slack.GetUsersOption) ([]slack.User, error)
	GetUsersInfo(users ...string) (*[]slack.User, error)
	PostMessageContext(ctx context.Context, channel string, options ...slack.MsgOption) (string, string, error)
	MarkConversationContext(ctx context.Context, channel, ts string) error
	OpenConversationContext(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error)
	AddReactionContext(ctx context.Context, name string, item slack.ItemRef) error
	RemoveReactionContext(ctx context.Context, name string, item slack.ItemRef) error
	LeaveConversationContext(ctx context.Context, channelID string) (bool, error)
	JoinConversationContext(ctx context.Context, channelID string) (*slack.Channel, string, []string, error)

	// Used to get messages
	GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
	GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) (msgs []slack.Message, hasMore bool, nextCursor string, err error)
	SearchContext(ctx context.Context, query string, params slack.SearchParameters) (*slack.SearchMessages, *slack.SearchFiles, error)

	// Used to get files
	GetFileInfoContext(ctx context.Context, fileID string, count, page int) (*slack.File, []slack.Comment, *slack.Paging, error)
	GetFileContext(ctx context.Context, downloadURL string, writer io.Writer) error
	ListFilesContext(ctx context.Context, params slack.ListFilesParameters) ([]slack.File, *slack.ListFilesParameters, error)

	// Used to get channel info (for unread counts with xoxp tokens)
	GetConversationInfoContext(ctx context.Context, input *slack.GetConversationInfoInput) (*slack.Channel, error)

	// Used to get channels list from both Slack and Enterprise Grid versions
	GetConversationsContext(ctx context.Context, params *slack.GetConversationsParameters) ([]slack.Channel, string, error)

	// Used to list only channels the calling user is a member of (users.conversations).
	// For xoxp tokens this is more efficient than conversations.list because it excludes
	// non-member public channels and closed DMs that cannot have unreads.
	GetConversationsForUserContext(ctx context.Context, params *slack.GetConversationsForUserParameters) ([]slack.Channel, string, error)

	// Edge API methods
	ClientUserBoot(ctx context.Context) (*edge.ClientUserBootResponse, error)
	UsersSearch(ctx context.Context, query string, count int) ([]slack.User, error)
	ClientCounts(ctx context.Context) (edge.ClientCountsResponse, error)
	ActivityFeed(ctx context.Context, limit int) (edge.ActivityFeedResponse, error)
	ActivityMarkRead(ctx context.Context, itemType, feedTs, key string) error
	GetMutedChannels(ctx context.Context) (map[string]bool, error)
	SavedList(ctx context.Context, filter string, limit int, cursor string) (edge.SavedListResponse, error)
	SavedUpdate(ctx context.Context, itemType, itemID, ts, mark string, dateDue int64) error
	SavedClearCompleted(ctx context.Context) error

	// Stars API methods
	GetStarredChannelIDs(ctx context.Context, limit int) ([]string, error)

	// User groups API methods
	GetUserGroupsContext(ctx context.Context, options ...slack.GetUserGroupsOption) ([]slack.UserGroup, error)
	GetUserGroupMembersContext(ctx context.Context, userGroup string, options ...slack.GetUserGroupMembersOption) ([]string, error)
	CreateUserGroupContext(ctx context.Context, userGroup slack.UserGroup, options ...slack.CreateUserGroupOption) (slack.UserGroup, error)
	UpdateUserGroupContext(ctx context.Context, userGroupID string, options ...slack.UpdateUserGroupsOption) (slack.UserGroup, error)
	UpdateUserGroupMembersContext(ctx context.Context, userGroup string, members string, options ...slack.UpdateUserGroupMembersOption) (slack.UserGroup, error)
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
	edgeFailed   bool // set when edge API fails; subsequent calls skip straight to standard API
	teamEndpoint string

	fallbackSlackClient *slack.Client
	browserState        atomic.Int32
	browserReason       atomic.Value
	browserNotifyOnce   sync.Once
}

type ApiProvider struct {
	transport string
	client    SlackAPI
	logger    *zap.Logger

	cacheTTL           time.Duration
	minRefreshInterval time.Duration

	// Users cache: atomic pointer to immutable snapshot (no copy on read)
	usersSnapshot          atomic.Pointer[UsersCache]
	usersCachePath         string
	usersReady             atomic.Bool
	refreshingUsers        atomic.Bool // true while a background refresh goroutine is running
	lastForcedUsersRefresh time.Time
	usersMu                sync.RWMutex // protects lastForcedUsersRefresh
	fetchUsersMu           sync.Mutex   // serializes fetchAndStoreUsers calls

	// Channels cache: atomic pointer to immutable snapshot (no copy on read)
	channelsSnapshot          atomic.Pointer[ChannelsCache]
	channelsCachePath         string
	channelsReady             atomic.Bool
	refreshingChannels        atomic.Bool // true while a background refresh goroutine is running
	lastForcedChannelsRefresh time.Time
	channelsMu                sync.RWMutex // protects lastForcedChannelsRefresh
	fetchChannelsMu           sync.Mutex   // serializes fetchAndStoreChannels calls
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

	// Token type detection
	// isOAuth: Official OAuth tokens (xoxp or xoxb) - uses Standard API
	// isBotToken: Bot token - determines feature availability (e.g., search)
	// xoxe.xoxp- and xoxe.xoxb- are token-rotation variants of xoxp/xoxb (same scopes, 12h expiry)
	isOAuth := strings.HasPrefix(token, "xoxp-") || strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxp-") || strings.HasPrefix(token, "xoxe.xoxb-")
	isBotToken := strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxb-")

	return &MCPSlackClient{
		slackClient:  slackClient,
		edgeClient:   edgeClient,
		authResponse: authResponse,
		authProvider: authProvider,
		logger:       logger,
		isEnterprise: isEnterprise,
		isOAuth:      isOAuth,
		isBotToken:   isBotToken,
		teamEndpoint: authResp.URL,
	}, nil
}

func (c *MCPSlackClient) standardSlackClient() *slack.Client {
	if c.hasOAuthFallback() && !c.browserFeaturesAvailable() {
		return c.fallbackSlackClient
	}
	return c.slackClient
}

func (c *MCPSlackClient) hasOAuthFallback() bool {
	return c.fallbackSlackClient != nil
}

func (c *MCPSlackClient) browserFeaturesAvailable() bool {
	if c.isOAuth {
		return false
	}
	return browserRuntimeState(c.browserState.Load()) == browserStateActive
}

func (c *MCPSlackClient) browserDegradedReason() string {
	if reason, ok := c.browserReason.Load().(string); ok {
		return reason
	}
	return ""
}

func (c *MCPSlackClient) effectiveOAuth() bool {
	return c.isOAuth || (c.hasOAuthFallback() && !c.browserFeaturesAvailable())
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
	if c.isOAuth {
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
	c.logger.Warn("Browser-session Slack auth degraded", zap.String("reason", reasonText), zap.Bool("oauth_fallback", c.hasOAuthFallback()))
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

func (c *MCPSlackClient) AuthTestContext(ctx context.Context) (*slack.AuthTestResponse, error) {
	return c.standardSlackClient().AuthTestContext(ctx)
}

func (c *MCPSlackClient) GetUsersContext(ctx context.Context, options ...slack.GetUsersOption) ([]slack.User, error) {
	return c.standardSlackClient().GetUsersContext(ctx, options...)
}

func (c *MCPSlackClient) GetUsersInfo(users ...string) (*[]slack.User, error) {
	return c.standardSlackClient().GetUsersInfo(users...)
}

func (c *MCPSlackClient) MarkConversationContext(ctx context.Context, channel, ts string) error {
	return c.standardSlackClient().MarkConversationContext(ctx, channel, ts)
}

func (c *MCPSlackClient) LeaveConversationContext(ctx context.Context, channelID string) (bool, error) {
	if c.isEnterprise && !c.isOAuth {
		// Enterprise Grid + session tokens: use edge API which goes through
		// the webclient endpoint and bypasses enterprise_is_restricted.
		notInChannel, err := c.edgeClient.LeaveConversation(ctx, channelID)
		if err == nil {
			return notInChannel, nil
		}
		// Fall back to standard API if edge fails.
	}
	return c.slackClient.LeaveConversationContext(ctx, channelID)
}

func (c *MCPSlackClient) JoinConversationContext(ctx context.Context, channelID string) (*slack.Channel, string, []string, error) {
	return c.slackClient.JoinConversationContext(ctx, channelID)
}

func (c *MCPSlackClient) GetConversationsContext(ctx context.Context, params *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	// Please see https://github.com/korotovsky/slack-mcp-server/issues/73
	// It seems that `conversations.list` works with `xoxp` tokens within Enterprise Grid setups
	// and if `xoxc`/`xoxd` defined we fallback to edge client.
	// In non Enterprise Grid setups we always use `conversations.list` api as it accepts both token types wtf.
	if c.isEnterprise {
		if c.isOAuth {
			return c.slackClient.GetConversationsContext(ctx, params)
		}

		// Enterprise + non-OAuth: try edge API first (for DMs, MPIMs, etc.),
		// then supplement with standard API. The edge API may only return
		// partial results (e.g., DMs succeed but SearchChannels fails on
		// restricted teams), so we always merge both sources.
		//
		// The edge API returns all results in one shot (no pagination),
		// while the standard API paginates. We fully paginate the standard
		// API here and return a merged, deduplicated result set with an
		// empty cursor so the caller doesn't need to re-paginate.
		if !c.edgeFailed {
			edgeChannels, _, edgeErr := c.edgeClient.GetConversationsContext(ctx, nil)
			if edgeErr != nil {
				c.edgeFailed = true
				return c.slackClient.GetConversationsContext(ctx, params)
			}

			// Collect edge results into a map for deduplication.
			seen := make(map[string]struct{}, len(edgeChannels))
			var channels []slack.Channel
			for _, ec := range edgeChannels {
				if params != nil && params.ExcludeArchived && ec.IsArchived {
					continue
				}
				seen[ec.ID] = struct{}{}
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

			// Supplement with ALL pages from the standard API to fill gaps
			// the edge API missed (e.g., public/private channels on
			// restricted teams where SearchChannels returns an error).
			stdParams := &slack.GetConversationsParameters{
				Limit:           999,
				ExcludeArchived: true,
			}
			if params != nil {
				stdParams.Types = params.Types
			}
			for {
				stdChannels, nextCur, stdErr := c.slackClient.GetConversationsContext(ctx, stdParams)
				if stdErr != nil {
					break // standard API failed; keep what edge gave us
				}
				for _, sc := range stdChannels {
					if _, ok := seen[sc.ID]; !ok {
						seen[sc.ID] = struct{}{}
						channels = append(channels, sc)
					}
				}
				if nextCur == "" {
					break
				}
				stdParams.Cursor = nextCur
			}

			return channels, "", nil
		}

		// Edge API previously failed — use standard API directly.
		return c.slackClient.GetConversationsContext(ctx, params)
	}

	return c.slackClient.GetConversationsContext(ctx, params)
}

func (c *MCPSlackClient) GetConversationsForUserContext(ctx context.Context, params *slack.GetConversationsForUserParameters) ([]slack.Channel, string, error) {
	return c.standardSlackClient().GetConversationsForUserContext(ctx, params)
}

func (c *MCPSlackClient) GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	return c.standardSlackClient().GetConversationHistoryContext(ctx, params)
}

func (c *MCPSlackClient) GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) (msgs []slack.Message, hasMore bool, nextCursor string, err error) {
	return c.standardSlackClient().GetConversationRepliesContext(ctx, params)
}

func (c *MCPSlackClient) SearchContext(ctx context.Context, query string, params slack.SearchParameters) (*slack.SearchMessages, *slack.SearchFiles, error) {
	return c.standardSlackClient().SearchContext(ctx, query, params)
}

func (c *MCPSlackClient) PostMessageContext(ctx context.Context, channelID string, options ...slack.MsgOption) (string, string, error) {
	return c.standardSlackClient().PostMessageContext(ctx, channelID, options...)
}

func (c *MCPSlackClient) OpenConversationContext(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
	return c.standardSlackClient().OpenConversationContext(ctx, params)
}

func (c *MCPSlackClient) AddReactionContext(ctx context.Context, name string, item slack.ItemRef) error {
	return c.standardSlackClient().AddReactionContext(ctx, name, item)
}

func (c *MCPSlackClient) RemoveReactionContext(ctx context.Context, name string, item slack.ItemRef) error {
	return c.standardSlackClient().RemoveReactionContext(ctx, name, item)
}

func (c *MCPSlackClient) GetFileInfoContext(ctx context.Context, fileID string, count, page int) (*slack.File, []slack.Comment, *slack.Paging, error) {
	return c.standardSlackClient().GetFileInfoContext(ctx, fileID, count, page)
}

func (c *MCPSlackClient) GetFileContext(ctx context.Context, downloadURL string, writer io.Writer) error {
	return c.standardSlackClient().GetFileContext(ctx, downloadURL, writer)
}

func (c *MCPSlackClient) ListFilesContext(ctx context.Context, params slack.ListFilesParameters) ([]slack.File, *slack.ListFilesParameters, error) {
	return c.standardSlackClient().ListFilesContext(ctx, params)
}

func (c *MCPSlackClient) GetConversationInfoContext(ctx context.Context, input *slack.GetConversationInfoInput) (*slack.Channel, error) {
	return c.standardSlackClient().GetConversationInfoContext(ctx, input)
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
	if !c.browserFeaturesAvailable() {
		return nil, ErrBrowserSessionUnavailable
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
		// xoxp tokens: use stars.list standard API and filter for channel-like items
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

	// xoxc/xoxd tokens: use client.userBoot which returns starred channel IDs
	ub, err := c.edgeClient.ClientUserBoot(ctx)
	if err != nil {
		if isBrowserSessionAuthError(err) {
			c.degradeBrowserSession(err)
			if c.hasOAuthFallback() {
				return c.GetStarredChannelIDs(ctx, limit)
			}
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

func (c *MCPSlackClient) GetUserGroupsContext(ctx context.Context, options ...slack.GetUserGroupsOption) ([]slack.UserGroup, error) {
	return c.standardSlackClient().GetUserGroupsContext(ctx, options...)
}

func (c *MCPSlackClient) GetUserGroupMembersContext(ctx context.Context, userGroup string, options ...slack.GetUserGroupMembersOption) ([]string, error) {
	return c.standardSlackClient().GetUserGroupMembersContext(ctx, userGroup, options...)
}

func (c *MCPSlackClient) CreateUserGroupContext(ctx context.Context, userGroup slack.UserGroup, options ...slack.CreateUserGroupOption) (slack.UserGroup, error) {
	return c.standardSlackClient().CreateUserGroupContext(ctx, userGroup, options...)
}

func (c *MCPSlackClient) UpdateUserGroupContext(ctx context.Context, userGroupID string, options ...slack.UpdateUserGroupsOption) (slack.UserGroup, error) {
	return c.standardSlackClient().UpdateUserGroupContext(ctx, userGroupID, options...)
}

func (c *MCPSlackClient) UpdateUserGroupMembersContext(ctx context.Context, userGroup string, members string, options ...slack.UpdateUserGroupMembersOption) (slack.UserGroup, error) {
	return c.standardSlackClient().UpdateUserGroupMembersContext(ctx, userGroup, members, options...)
}

func (c *MCPSlackClient) IsEnterprise() bool {
	return c.isEnterprise
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

func (c *MCPSlackClient) Raw() struct {
	Slack *slack.Client
	Edge  *edge.Client
} {
	return struct {
		Slack *slack.Client
		Edge  *edge.Client
	}{
		Slack: c.slackClient,
		Edge:  c.edgeClient,
	}
}

func New(transport string, logger *zap.Logger) *ApiProvider {
	var (
		authProvider auth.ValueAuth
		err          error
	)

	// Read all environment variables
	xoxpToken := os.Getenv("SLACK_MCP_XOXP_TOKEN")
	xoxbToken := os.Getenv("SLACK_MCP_XOXB_TOKEN")
	xoxcToken := os.Getenv("SLACK_MCP_XOXC_TOKEN")
	xoxdToken := os.Getenv("SLACK_MCP_XOXD_TOKEN")

	// Prefer browser-session auth when present so browser-only tools can be
	// registered, but keep user OAuth as a runtime fallback when available.
	if xoxcToken != "" && xoxdToken != "" {
		authProvider, err = auth.NewValueAuth(xoxcToken, xoxdToken)
		if err != nil {
			logger.Fatal("Failed to create auth provider with XOXC/XOXD tokens", zap.Error(err))
		}
		ap, startupErr := newWithXOXC(transport, authProvider, xoxpToken, logger)
		if startupErr == nil {
			return ap
		}
		if xoxpToken != "" {
			logger.Warn("Browser-session auth failed at startup, using OAuth fallback", zap.Error(startupErr))
			writeBrowserRuntimeStatus("browser_degraded", startupErr.Error(), logger)
			notifyBrowserDegradation(startupErr.Error(), logger)
			authProvider, err = auth.NewValueAuth(xoxpToken, "")
			if err != nil {
				logger.Fatal("Failed to create auth provider with XOXP token", zap.Error(err))
			}
			return newWithXOXP(transport, authProvider, logger)
		}
		logger.Fatal("Authentication failed - browser-session tokens are invalid and no OAuth fallback is configured", zap.Error(startupErr))
	}

	// Warn if both user and bot tokens are set
	if xoxpToken != "" && xoxbToken != "" {
		logger.Warn(
			"Both SLACK_MCP_XOXP_TOKEN and SLACK_MCP_XOXB_TOKEN are set. "+
				"Using User token (xoxp) for full features. "+
				"Bot token will be ignored.",
			zap.String("context", "console"),
		)
	}

	// Priority 1: XOXP token (User OAuth)
	if xoxpToken != "" {
		authProvider, err = auth.NewValueAuth(xoxpToken, "")
		if err != nil {
			logger.Fatal("Failed to create auth provider with XOXP token", zap.Error(err))
		}

		return newWithXOXP(transport, authProvider, logger)
	}

	// Priority 2: XOXB token (Bot)
	if xoxbToken != "" {
		authProvider, err = auth.NewValueAuth(xoxbToken, "")
		if err != nil {
			logger.Fatal("Failed to create auth provider with XOXB token", zap.Error(err))
		}

		logger.Info("Using Bot token authentication",
			zap.String("context", "console"),
			zap.String("token_type", "xoxb"),
		)

		return newWithXOXB(transport, authProvider, logger)
	}

	// Priority 3: XOXC/XOXD tokens (session-based)
	if xoxcToken == "" || xoxdToken == "" {
		logger.Fatal("Authentication required: Either SLACK_MCP_XOXP_TOKEN, SLACK_MCP_XOXB_TOKEN, or both SLACK_MCP_XOXC_TOKEN and SLACK_MCP_XOXD_TOKEN must be provided")
	}

	authProvider, err = auth.NewValueAuth(xoxcToken, xoxdToken)
	if err != nil {
		logger.Fatal("Failed to create auth provider with XOXC/XOXD tokens", zap.Error(err))
	}

	ap, startupErr := newWithXOXC(transport, authProvider, "", logger)
	if startupErr != nil {
		logger.Fatal("Authentication failed - check your browser-session Slack tokens", zap.Error(startupErr))
	}
	return ap
}

func newWithXOXP(transport string, authProvider auth.ValueAuth, logger *zap.Logger) *ApiProvider {
	var (
		client *MCPSlackClient
		err    error
	)

	teamID, cachedAuth, err := validateAuthAndGetTeamID(authProvider, logger)
	if err != nil {
		logger.Fatal("Authentication failed - check your Slack tokens", zap.Error(err))
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

		usersCachePath:    usersCache,
		channelsCachePath: channelsCache,
	}
	// Initialize with empty snapshots
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

func newWithXOXB(transport string, authProvider auth.ValueAuth, logger *zap.Logger) *ApiProvider {
	// Bot tokens do not support demo mode, but otherwise share the same
	// initialization logic as user OAuth tokens.
	return newWithXOXP(transport, authProvider, logger)
}

func newWithXOXC(transport string, authProvider auth.ValueAuth, oauthFallbackToken string, logger *zap.Logger) (*ApiProvider, error) {
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
		if oauthFallbackToken != "" {
			fallbackAuth, fallbackErr := auth.NewValueAuth(oauthFallbackToken, "")
			if fallbackErr != nil {
				logger.Warn("Failed to create OAuth fallback auth provider", zap.Error(fallbackErr))
			} else {
				httpClient := transportpkg.ProvideHTTPClient(fallbackAuth.Cookies(), logger)
				client.fallbackSlackClient = slack.New(
					fallbackAuth.SlackToken(),
					slack.OptionHTTPClient(httpClient),
					slack.OptionAPIURL(cachedAuth.URL+"api/"),
				)
			}
		}
	}

	ap := &ApiProvider{
		transport: transport,
		client:    client,
		logger:    logger,

		cacheTTL:           getCacheTTL(),
		minRefreshInterval: getMinRefreshInterval(),

		usersCachePath:    usersCache,
		channelsCachePath: channelsCache,
	}
	// Initialize with empty snapshots
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

// ForceRefreshUsers bypasses the cache and fetches fresh user data from Slack API.
// Rate limited by SLACK_MCP_MIN_REFRESH_INTERVAL (default 30s) to prevent API abuse.
// Returns ErrRefreshRateLimited if refresh is skipped due to rate limiting.
func (ap *ApiProvider) ForceRefreshUsers(ctx context.Context) error {
	if ap.minRefreshInterval > 0 {
		// Use single lock scope for check-and-update to prevent TOCTOU race
		ap.usersMu.Lock()
		sinceLast := time.Since(ap.lastForcedUsersRefresh)
		if sinceLast < ap.minRefreshInterval {
			ap.usersMu.Unlock()
			ap.logger.Debug("Skipping forced users refresh, within rate limit",
				zap.Duration("since_last", sinceLast),
				zap.Duration("min_interval", ap.minRefreshInterval))
			return ErrRefreshRateLimited
		}
		// Update timestamp before refresh to prevent concurrent forced refreshes
		ap.lastForcedUsersRefresh = time.Now()
		ap.usersMu.Unlock()
	}

	ap.logger.Info("Force refreshing users cache")
	return ap.refreshUsersInternal(ctx, true)
}

// PatchUser fetches a single user by ID from the Slack API and adds them to
// the in-memory users snapshot. This is much cheaper than a full cache rebuild
// for a single cache miss (O(1) API call vs O(all users)).
// Disk persistence is skipped — the next full refresh will persist the entry.
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
	current := ap.usersSnapshot.Load()

	newSnapshot := &UsersCache{
		Users:    make(map[string]slack.User, len(current.Users)+1),
		UsersInv: make(map[string]string, len(current.UsersInv)+1),
	}
	for k, v := range current.Users {
		newSnapshot.Users[k] = v
	}
	for k, v := range current.UsersInv {
		newSnapshot.UsersInv[k] = v
	}
	newSnapshot.Users[user.ID] = user
	newSnapshot.UsersInv[user.Name] = user.ID

	ap.usersSnapshot.Store(newSnapshot)
	ap.logger.Debug("Patched user into cache",
		zap.String("user_id", user.ID),
		zap.String("user_name", user.Name))

	return &user, nil
}

func (ap *ApiProvider) refreshUsersInternal(ctx context.Context, force bool) error {
	ap.usersMu.Lock()

	// Check if we should use cache (not forced, cache exists)
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
				// Fall through to fetchAndStoreUsers
			} else {
				// Build snapshot from cache
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

				// Check cache TTL using file modification time
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

				// Cache is expired: release lock, spawn background refresh, return immediately
				ap.usersMu.Unlock()
				ap.spawnBackgroundUsersRefresh()
				return nil
			}
		}
	}

	// No usable cache: fetch fresh data synchronously (first run or force)
	ap.usersMu.Unlock()
	return ap.fetchAndStoreUsers(ctx)
}

// spawnBackgroundUsersRefresh starts a background goroutine to fetch fresh user data.
// Uses refreshingUsers flag to prevent concurrent background refreshes.
func (ap *ApiProvider) spawnBackgroundUsersRefresh() {
	if !ap.refreshingUsers.CompareAndSwap(false, true) {
		ap.logger.Debug("Skipping background users refresh, already in progress")
		return
	}
	go func() {
		defer ap.refreshingUsers.Store(false)
		if err := ap.fetchAndStoreUsers(context.Background()); err != nil {
			ap.logger.Warn("Background users refresh failed, continuing with stale data",
				zap.Error(err))
		}
	}()
}

// fetchAndStoreUsers fetches all users from the Slack API and updates the snapshot and cache file.
// Serialized by fetchUsersMu to prevent concurrent fetches from racing on snapshot/file writes.
func (ap *ApiProvider) fetchAndStoreUsers(ctx context.Context) error {
	ap.fetchUsersMu.Lock()
	defer ap.fetchUsersMu.Unlock()

	var (
		list        []slack.User
		optionLimit = slack.GetUsersOptionLimit(1000)
	)

	users, err := ap.client.GetUsersContext(ctx,
		optionLimit,
	)
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

	list = append(list, users...)

	// Build new snapshot
	newSnapshot := &UsersCache{
		Users:    make(map[string]slack.User),
		UsersInv: make(map[string]string),
	}
	for _, user := range users {
		newSnapshot.Users[user.ID] = user
		newSnapshot.UsersInv[user.Name] = user.ID
	}
	// Store intermediate snapshot so GetSlackConnect can read current users
	ap.usersSnapshot.Store(newSnapshot)

	if ap.IsOAuth() {
		ap.logger.Debug("Skipping Slack Connect enrichment (OAuth token, browser features unavailable)")
	} else {
		connectUsers, err := ap.GetSlackConnect(ctx)
		if err != nil {
			ap.logger.Warn("Slack Connect enrichment failed; continuing with standard user list",
				zap.Error(err))
		} else {
			list = append(list, connectUsers...)

			// Add Slack Connect users to a new snapshot (since maps are shared)
			if len(connectUsers) > 0 {
				finalSnapshot := &UsersCache{
					Users:    make(map[string]slack.User, len(newSnapshot.Users)+len(connectUsers)),
					UsersInv: make(map[string]string, len(newSnapshot.UsersInv)+len(connectUsers)),
				}
				for k, v := range newSnapshot.Users {
					finalSnapshot.Users[k] = v
				}
				for k, v := range newSnapshot.UsersInv {
					finalSnapshot.UsersInv[k] = v
				}
				for _, user := range connectUsers {
					finalSnapshot.Users[user.ID] = user
					finalSnapshot.UsersInv[user.Name] = user.ID
				}
				ap.usersSnapshot.Store(finalSnapshot)
			}
		}
	}

	if data, err := json.MarshalIndent(list, "", "  "); err != nil {
		ap.logger.Error("Failed to marshal users for cache", zap.Error(err))
	} else {
		// Atomic write: temp file + rename to prevent partial/corrupt files
		if err := atomicWriteFile(ap.usersCachePath, data, 0600); err != nil {
			ap.logger.Error("Failed to write cache file",
				zap.String("cache_file", ap.usersCachePath),
				zap.Error(err))
		} else {
			ap.logger.Info("Wrote users to cache",
				zap.Int("count", len(list)),
				zap.String("cache_file", ap.usersCachePath))
		}
	}

	ap.usersReady.Store(true)

	return nil
}

func (ap *ApiProvider) RefreshChannels(ctx context.Context) error {
	return ap.refreshChannelsInternal(ctx, false)
}

// ForceRefreshChannels bypasses the cache and fetches fresh channel data from Slack API.
// Use this when a channel lookup fails to attempt recovery with fresh data.
// Rate limited by SLACK_MCP_MIN_REFRESH_INTERVAL (default 30s) to prevent API abuse.
// Returns ErrRefreshRateLimited if refresh is skipped due to rate limiting.
func (ap *ApiProvider) ForceRefreshChannels(ctx context.Context) error {
	if ap.minRefreshInterval > 0 {
		// Use single lock scope for check-and-update to prevent TOCTOU race
		ap.channelsMu.Lock()
		sinceLast := time.Since(ap.lastForcedChannelsRefresh)
		if sinceLast < ap.minRefreshInterval {
			ap.channelsMu.Unlock()
			ap.logger.Debug("Skipping forced channels refresh, within rate limit",
				zap.Duration("since_last", sinceLast),
				zap.Duration("min_interval", ap.minRefreshInterval))
			return ErrRefreshRateLimited
		}
		// Update timestamp before refresh to prevent concurrent forced refreshes
		ap.lastForcedChannelsRefresh = time.Now()
		ap.channelsMu.Unlock()
	}

	ap.logger.Info("Force refreshing channels cache")
	return ap.refreshChannelsInternal(ctx, true)
}

func (ap *ApiProvider) refreshChannelsInternal(ctx context.Context, force bool) error {
	ap.channelsMu.Lock()

	// Check if we should use cache (not forced, cache exists)
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
				// Fall through to fetchAndStoreChannels
			} else {
				// Re-map channels with current users cache to ensure DM names are populated
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

				// Check cache TTL using file modification time
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

				// Cache is expired: release lock, spawn background refresh, return immediately
				ap.channelsMu.Unlock()
				ap.spawnBackgroundChannelsRefresh()
				return nil
			}
		}
	}

	// No usable cache: fetch fresh data synchronously (first run or force)
	ap.channelsMu.Unlock()
	return ap.fetchAndStoreChannels(ctx)
}

// spawnBackgroundChannelsRefresh starts a background goroutine to fetch fresh channel data.
func (ap *ApiProvider) spawnBackgroundChannelsRefresh() {
	if !ap.refreshingChannels.CompareAndSwap(false, true) {
		ap.logger.Debug("Skipping background channels refresh, already in progress")
		return
	}
	go func() {
		defer ap.refreshingChannels.Store(false)
		if err := ap.fetchAndStoreChannels(context.Background()); err != nil {
			ap.logger.Warn("Background channels refresh failed, continuing with stale data",
				zap.Error(err))
		}
	}()
}

// fetchAndStoreChannels fetches all channels from the Slack API and updates the snapshot and cache file.
// Serialized by fetchChannelsMu to prevent concurrent fetches from racing on snapshot/file writes.
func (ap *ApiProvider) fetchAndStoreChannels(ctx context.Context) error {
	ap.fetchChannelsMu.Lock()
	defer ap.fetchChannelsMu.Unlock()

	channels := ap.GetChannels(ctx, AllChanTypes)

	if len(channels) == 0 {
		if ap.channelsReady.Load() {
			ap.logger.Warn("API returned zero channels, keeping existing cache")
			return nil
		}
		return errors.New("API returned zero channels and no existing cache is available")
	}

	if data, err := json.MarshalIndent(channels, "", "  "); err != nil {
		ap.logger.Error("Failed to marshal channels for cache", zap.Error(err))
	} else {
		// Atomic write: temp file + rename to prevent partial/corrupt files
		if err := atomicWriteFile(ap.channelsCachePath, data, 0600); err != nil {
			ap.logger.Error("Failed to write cache file",
				zap.String("cache_file", ap.channelsCachePath),
				zap.Error(err))
		} else {
			ap.logger.Info("Wrote channels to cache",
				zap.Int("count", len(channels)),
				zap.String("cache_file", ap.channelsCachePath))
		}
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

func (ap *ApiProvider) GetChannelsType(ctx context.Context, channelType string) []Channel {
	return ap.getChannelsMultiType(ctx, []string{channelType})
}

func (ap *ApiProvider) getChannelsMultiType(ctx context.Context, channelTypes []string) []Channel {
	params := &slack.GetConversationsParameters{
		Types:           channelTypes,
		Limit:           999,
		ExcludeArchived: true,
	}

	var (
		channels []slack.Channel
		chans    []Channel

		nextcur string
		err     error
	)

	usersMap := ap.ProvideUsersMap().Users
	lim := limiter.Tier2boost.Limiter()

	for {
		if err := lim.Wait(ctx); err != nil {
			ap.logger.Error("Rate limiter wait failed", zap.Error(err))
			return nil
		}

		channels, nextcur, err = ap.client.GetConversationsContext(ctx, params)
		ap.logger.Debug("Fetched channels",
			zap.Strings("channelTypes", channelTypes),
			zap.Int("count", len(channels)),
		)
		if err != nil {
			ap.logger.Error("Failed to fetch channels", zap.Error(err))
			break
		}

		for _, channel := range channels {
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

		if nextcur == "" {
			break
		}

		params.Cursor = nextcur
	}
	return chans
}

func (ap *ApiProvider) GetChannels(ctx context.Context, channelTypes []string) []Channel {
	if len(channelTypes) == 0 {
		channelTypes = AllChanTypes
	}

	// Fetch all channel types in a single paginated call. The standard
	// conversations.list API supports multiple types per request, and the edge
	// API (Enterprise Grid + non-OAuth) returns all types regardless. This
	// avoids making 4 separate API round-trips (one per type).
	chans := ap.getChannelsMultiType(ctx, channelTypes)

	// Build new snapshot with all fetched channels
	newSnapshot := &ChannelsCache{
		Channels:    make(map[string]Channel, len(chans)),
		ChannelsInv: make(map[string]string, len(chans)),
	}
	for _, ch := range chans {
		newSnapshot.Channels[ch.ID] = ch
		newSnapshot.ChannelsInv[ch.Name] = ch.ID
	}
	ap.channelsSnapshot.Store(newSnapshot)

	return chans
}

func (ap *ApiProvider) ProvideUsersMap() *UsersCache {
	// Atomic load - no lock needed, snapshot is immutable
	return ap.usersSnapshot.Load()
}

func (ap *ApiProvider) ProvideChannelsMaps() *ChannelsCache {
	// Atomic load - no lock needed, snapshot is immutable
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

// SkipCache marks both users and channels caches as ready without loading
// any data. Lookups by #channel-name or @username will not work; callers
// must use channel/user IDs instead.
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

func (ap *ApiProvider) IsBotToken() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.IsBotToken()
}

func (ap *ApiProvider) IsOAuth() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.IsOAuth()
}

// WorkspaceURL returns the cached workspace base URL from auth (e.g.
// "https://myorg.slack.com/"), or "" when the client type doesn't cache it
// (callers must treat "" as "omit workspace-dependent output").
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

// slackUserIDPattern matches Slack user IDs (e.g., U07VCEPP4N5, W0123456789).
var slackUserIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)

// SearchUsers searches for users by name, email, or display name.
// If the query matches a Slack user ID pattern (e.g., U07VCEPP4N5), it looks up the user
// directly via the users.info API instead of searching.
// For OAuth tokens (xoxp/xoxb), it searches the local users cache using regex matching.
// For browser tokens (xoxc/xoxd), it uses the edge API's UsersSearch method.
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

// searchUsersInCache performs a case-insensitive regex search on cached users.
// Matches against username, real name, display name, and email.
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
		userID = user // Store the user ID for later re-mapping

		// If user field is empty but we have members, try to extract from members
		if userID == "" && len(members) > 0 {
			// For IM channels, members should contain the other user's ID
			// Try each member to find a valid user in the users map
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

// MapChannelFromSlack converts a slack.Channel to our internal Channel type.
func MapChannelFromSlack(c slack.Channel, usersMap map[string]slack.User) Channel {
	return mapChannel(
		c.ID, c.Name, c.NameNormalized,
		c.Topic.Value, c.Purpose.Value,
		c.User, c.Members, c.NumMembers,
		c.IsIM, c.IsMpIM, c.IsPrivate, c.IsExtShared,
		usersMap,
	)
}
