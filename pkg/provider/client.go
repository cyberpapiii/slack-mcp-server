package provider

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/slackcreds"
	"github.com/korotovsky/slack-mcp-server/pkg/slackhttp"
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
	ClientCounts(ctx context.Context) (UnreadCounts, error)
	ActivityFeed(ctx context.Context, limit int) ([]ActivityEntry, error)
	ActivityMarkRead(ctx context.Context, itemType, feedTs, key string) error
	GetMutedChannels(ctx context.Context) (map[string]bool, error)
	SavedList(ctx context.Context, filter string, limit int, cursor string) (SavedPage, error)
	SavedUpdate(ctx context.Context, update SavedUpdate) error
	SavedAdd(ctx context.Context, add SavedAdd) (SavedItem, error)
	SavedDelete(ctx context.Context, ref SavedRef) error
	SavedClearCompleted(ctx context.Context) error

	GetStarredChannelIDs(ctx context.Context, limit int) ([]string, error)
}

type MCPSlackClient struct {
	slackClient *slack.Client
	// edgeClient is swapped when a degraded browser session recovers.
	edgeClient atomic.Pointer[edge.Client]
	// httpClient carries the primary credentials' cookies and connection pool;
	// every direct Web API call reuses it instead of dialing a fresh pool.
	httpClient *http.Client

	authResponse *slack.AuthTestResponse
	authProvider slackcreds.Credentials
	logger       *zap.Logger

	demo         bool
	isEnterprise bool
	isOAuth      bool
	isBotToken   bool

	browserState         atomic.Int32
	browserReason        atomic.Value
	browserNotified      atomic.Bool
	browserProbe         browserProbeFunc
	browserConfigured    bool
	oauthClientMu        sync.RWMutex
	oauthManager         *oauthcred.Manager
	oauthGeneration      atomic.Uint64
	oauthAccessToken     atomic.Value
	oauthRuntimeState    atomic.Value
	managedClientFactory func(context.Context, string) (*slack.Client, *slack.AuthTestResponse, error)
	// oauthRefreshMu is held for one managed OAuth refresh; Close waits on it.
	oauthRefreshMu     sync.Mutex
	oauthShutdownGrace time.Duration // test override for oauthShutdownGrace
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

// authTest builds the HTTP client for authProvider's credentials and proves
// them against auth.test. Demo credentials skip the network entirely.
func authTest(authProvider slackcreds.Credentials, demo bool, logger *zap.Logger) (*http.Client, *slack.AuthTestResponse, error) {
	httpClient, err := transportpkg.ProvideHTTPClient(authProvider.Cookies(), logger)
	if err != nil {
		return nil, nil, err
	}
	if demo {
		return httpClient, demoAuthResponse(), nil
	}

	slackClient := slack.New(authProvider.SlackToken(),
		slack.OptionHTTPClient(httpClient),
		slack.OptionAPIURL(slackhttp.SlackAPIBase()),
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

func newMCPSlackClient(authProvider slackcreds.Credentials, demo bool, logger *zap.Logger) (*MCPSlackClient, error) {
	httpClient, authResp, err := authTest(authProvider, demo, logger)
	if err != nil {
		return nil, err
	}

	slackClient := slack.New(authProvider.SlackToken(),
		slack.OptionHTTPClient(httpClient),
		slack.OptionAPIURL(authResp.URL+"api/"),
	)

	edgeClient := edge.NewWithInfo(authResp, authProvider.SlackToken(),
		httpClient,
		edge.OptionLogger(logger),
	)

	token := authProvider.SlackToken()

	// xoxe.* are rotation variants of xoxp/xoxb (same scopes, ~12h expiry).
	isOAuth := strings.HasPrefix(token, "xoxp-") || strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxp-") || strings.HasPrefix(token, "xoxe.xoxb-")
	isBotToken := strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxb-")
	if demo {
		isOAuth = true
	}

	client := &MCPSlackClient{
		slackClient:  slackClient,
		httpClient:   httpClient,
		authResponse: authResp,
		authProvider: authProvider,
		logger:       logger,
		demo:         demo,
		isEnterprise: authResp.EnterpriseID != "",
		isOAuth:      isOAuth,
		isBotToken:   isBotToken,
		// Demo advertises the full tool surface, OAuth-gated and browser-gated alike.
		browserConfigured: demo,
	}
	client.edgeClient.Store(edgeClient)
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
