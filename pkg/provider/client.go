package provider

import (
	"context"
	"net/http"
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

// MCPSlackClient is the server's Slack session: the Web API client for the
// primary token, the browser session's edge client, and the managed OAuth
// rotation that replaces the Web API client. ApiProvider holds it by value, so
// the zero value is a session without credentials.
type MCPSlackClient struct {
	slackClient *slack.Client
	// edgeClient is swapped when a degraded browser session recovers.
	edgeClient atomic.Pointer[edge.Client]
	// httpClient carries the primary credentials' cookies and connection pool;
	// every direct Web API call reuses it instead of dialing a fresh pool.
	httpClient *http.Client

	authResponse slack.AuthTestResponse
	logger       *zap.Logger

	mode         CredentialMode
	isEnterprise bool

	browserState    atomic.Int32
	browserReason   atomic.Value
	browserNotified atomic.Bool
	browserProbe    browserProbeFunc
	oauthClientMu   sync.RWMutex
	oauthManager    *oauthcred.Manager
	oauthGeneration atomic.Uint64
	// token is the token Web API calls carry: the primary credential's, then
	// each rotated OAuth access token. On a browser session it is the xoxc.
	token                atomic.Value
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
// them against auth.test.
func authTest(authProvider slackcreds.Credentials, logger *zap.Logger) (*http.Client, *slack.AuthTestResponse, error) {
	httpClient, err := transportpkg.ProvideHTTPClient(authProvider.Cookies(), logger)
	if err != nil {
		return nil, nil, err
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

// connect authenticates authProvider and sets c up around it. Demo mode never
// reaches Slack: it answers auth.test itself.
func (c *MCPSlackClient) connect(authProvider slackcreds.Credentials, mode CredentialMode, logger *zap.Logger) error {
	var httpClient *http.Client
	var authResp *slack.AuthTestResponse
	var err error
	if mode == ModeDemo {
		httpClient, err = transportpkg.ProvideHTTPClient(authProvider.Cookies(), logger)
		authResp = demoAuthResponse()
	} else {
		httpClient, authResp, err = authTest(authProvider, logger)
	}
	if err != nil {
		return err
	}
	token := authProvider.SlackToken()
	c.httpClient = httpClient
	c.authResponse = *authResp
	c.logger = logger
	c.mode = mode
	c.isEnterprise = authResp.EnterpriseID != ""
	c.slackClient = slack.New(token,
		slack.OptionHTTPClient(httpClient),
		slack.OptionAPIURL(c.apiBase()),
	)
	c.edgeClient.Store(edge.NewWithInfo(authResp, token, httpClient, edge.OptionLogger(logger)))
	c.token.Store(token)
	c.initBrowserState()
	return nil
}

// apiBase is the Web API root on the workspace host.
func (c *MCPSlackClient) apiBase() string {
	return slackhttp.WorkspaceAPIBase(c.authResponse.URL)
}

func (c *MCPSlackClient) standardSlackClient() *slack.Client {
	c.oauthClientMu.RLock()
	defer c.oauthClientMu.RUnlock()
	return c.slackClient
}

// AuthTest answers from the startup auth.test; the identity cannot change.
func (c *MCPSlackClient) AuthTest() (*slack.AuthTestResponse, error) {
	return &c.authResponse, nil
}

func (c *MCPSlackClient) GetUsersContext(ctx context.Context, options ...slack.GetUsersOption) ([]slack.User, error) {
	return c.standardSlackClient().GetUsersContext(ctx, options...)
}

func (c *MCPSlackClient) GetUsersInfo(users ...string) (*[]slack.User, error) {
	return c.standardSlackClient().GetUsersInfo(users...)
}

func (c *MCPSlackClient) MarkConversationContext(ctx context.Context, channel, ts string) error {
	if c.mode == ModeBot {
		return ErrUserOAuthRequired
	}
	return c.standardSlackClient().MarkConversationContext(ctx, channel, ts)
}
