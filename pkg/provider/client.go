package provider

import (
	"context"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/slackcreds"
	transportpkg "github.com/korotovsky/slack-mcp-server/pkg/transport"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

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
	// httpClient carries the primary credentials' cookies and connection pool;
	// every direct Web API call reuses it instead of dialing a fresh pool.
	httpClient *http.Client

	authResponse *slack.AuthTestResponse
	authProvider slackcreds.Credentials
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

// IsDemoCredentials reports whether the process runs with the documented
// placeholder tokens. Demo mode builds a real client around a canned auth
// response so every tool registers, but never reaches Slack.
func IsDemoCredentials() bool {
	return os.Getenv("SLACK_MCP_XOXP_TOKEN") == "demo" ||
		(os.Getenv("SLACK_MCP_XOXC_TOKEN") == "demo" && os.Getenv("SLACK_MCP_XOXD_TOKEN") == "demo")
}

func demoAuthResponse() *slack.AuthTestResponse {
	return &slack.AuthTestResponse{
		URL:    "https://_.slack.com/",
		Team:   "Demo Team",
		User:   "Username",
		TeamID: "TEAM123456",
		UserID: "U1234567890",
	}
}

// Random 0-3s sleep so concurrent instances do not stampede Slack.
func startupJitter(logger *zap.Logger) {
	jitter := time.Duration(rand.Intn(3000)) * time.Millisecond
	logger.Info("Startup jitter", zap.Duration("delay", jitter))
	time.Sleep(jitter)
}

// authTest builds the HTTP client for authProvider's credentials and proves
// them against auth.test. Demo credentials skip the network entirely.
func authTest(authProvider slackcreds.Credentials, logger *zap.Logger) (*http.Client, *slack.AuthTestResponse, error) {
	httpClient, err := transportpkg.ProvideHTTPClient(authProvider.Cookies(), logger)
	if err != nil {
		return nil, nil, err
	}
	if IsDemoCredentials() {
		return httpClient, demoAuthResponse(), nil
	}

	slackClient := slack.New(authProvider.SlackToken(),
		slack.OptionHTTPClient(httpClient),
		slack.OptionAPIURL(transportpkg.SlackAPIBase()),
	)
	authResp, err := slackClient.AuthTest()
	if err != nil {
		return nil, nil, err
	}

	logger.Info("Authenticated to Slack",
		zap.String("team", authResp.Team),
		zap.String("team_id", authResp.TeamID),
		zap.String("user", authResp.User))
	return httpClient, authResp, nil
}

func newMCPSlackClient(authProvider slackcreds.Credentials, logger *zap.Logger) (*MCPSlackClient, error) {
	if !IsDemoCredentials() {
		startupJitter(logger)
	}
	httpClient, authResp, err := authTest(authProvider, logger)
	if err != nil {
		return nil, err
	}

	slackClient := slack.New(authProvider.SlackToken(),
		slack.OptionHTTPClient(httpClient),
		slack.OptionAPIURL(authResp.URL+"api/"),
	)

	edgeClient, err := edge.NewWithInfo(authResp, authProvider,
		edge.OptionHTTPClient(httpClient),
	)
	if err != nil {
		return nil, err
	}

	token := authProvider.SlackToken()

	// xoxe.* are rotation variants of xoxp/xoxb (same scopes, ~12h expiry).
	isOAuth := strings.HasPrefix(token, "xoxp-") || strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxp-") || strings.HasPrefix(token, "xoxe.xoxb-")
	isBotToken := strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxb-")
	demo := IsDemoCredentials()
	if demo {
		isOAuth = true
	}

	client := &MCPSlackClient{
		slackClient:  slackClient,
		edgeClient:   edgeClient,
		httpClient:   httpClient,
		authResponse: authResp,
		authProvider: authProvider,
		logger:       logger,
		isEnterprise: authResp.EnterpriseID != "",
		isOAuth:      isOAuth,
		isBotToken:   isBotToken,
		// Demo advertises the full tool surface, OAuth-gated and browser-gated alike.
		browserConfigured: demo,
	}
	client.oauthAccessToken.Store(token)
	client.initBrowserState()
	return client, nil
}

func (c *MCPSlackClient) standardSlackClient() *slack.Client {
	c.oauthClientMu.RLock()
	defer c.oauthClientMu.RUnlock()
	return c.slackClient
}

func (c *MCPSlackClient) AuthTest() (*slack.AuthTestResponse, error) {
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

func (c *MCPSlackClient) AuthResponse() *slack.AuthTestResponse {
	return c.authResponse
}

func (c *MCPSlackClient) IsBotToken() bool {
	return c.isBotToken
}

func (c *MCPSlackClient) IsOAuth() bool {
	return c.isOAuth
}
