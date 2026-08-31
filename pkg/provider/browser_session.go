package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/slackcreds"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type browserRuntimeState int32

const (
	browserStateOAuthOnly browserRuntimeState = iota
	browserStateActive
	browserStateDegraded
)

var browserDegradationNotifier = notifyBrowserDegradation

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

// browserAuthErrorCodes are the Slack error codes that mean the xoxc/xoxd
// session itself is dead, as opposed to a per-call failure.
var browserAuthErrorCodes = map[string]bool{
	"invalid_auth":     true,
	"not_authed":       true,
	"token_revoked":    true,
	"token_expired":    true,
	"account_inactive": true,
}

func isBrowserSessionAuthError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *edge.APIError
	if errors.As(err, &apiErr) {
		return browserAuthErrorCodes[apiErr.Err]
	}
	var statusErr slack.StatusCodeError
	if errors.As(err, &statusErr) {
		return statusErr.Code == http.StatusUnauthorized || statusErr.Code == http.StatusForbidden
	}
	var slackErr slack.SlackErrorResponse
	if errors.As(err, &slackErr) {
		return browserAuthErrorCodes[slackErr.Err]
	}
	return browserAuthErrorCodes[strings.TrimSpace(err.Error())]
}

// browserCall runs one browser-session (edge) request. It refuses when the
// session is degraded and degrades the session when Slack reports dead auth.
func browserCall[T any](c *MCPSlackClient, feature string, fn func() (T, error)) (T, error) {
	var zero T
	if err := c.ensureBrowserFeature(feature); err != nil {
		return zero, err
	}
	v, err := fn()
	if isBrowserSessionAuthError(err) {
		c.degradeBrowserSession(err)
		return zero, ErrBrowserSessionUnavailable
	}
	return v, err
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

func (c *MCPSlackClient) initBrowserState() {
	if c.isOAuth && !c.browserConfigured {
		c.browserState.Store(int32(browserStateOAuthOnly))
		return
	}
	c.browserState.Store(int32(browserStateActive))
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
	c.logger.Warn("Browser-session Slack auth degraded", zap.String("reason", reasonText), zap.Bool("standard_oauth", c.isOAuth))
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

// ConfiguredWithBrowserSession reports construction-time xoxc/xoxd (or similar
// session) auth — not runtime OAuth after browser degrade.
// Use this for registering browser-only tools so mid-warmup degradation cannot
// hide activity/saved tools that still need session credentials when healthy.
func (c *MCPSlackClient) ConfiguredWithBrowserSession() bool {
	return c.browserConfigured || (!c.isOAuth && !c.isBotToken)
}

// attachBrowserSession adds browser-only Slack surfaces to an OAuth-primary
// client. Standard Web API calls continue to use the OAuth client.
func (c *MCPSlackClient) attachBrowserSession(browserAuth slackcreds.Value, browserIdentity *slack.AuthTestResponse, httpClient *http.Client) error {
	if browserIdentity == nil || c.authResponse == nil {
		return errors.New("cannot verify OAuth and browser provider identities")
	}
	if browserIdentity.TeamID != c.authResponse.TeamID || browserIdentity.UserID != c.authResponse.UserID {
		return fmt.Errorf("browser provider identity mismatch: OAuth team/user %s/%s, browser team/user %s/%s",
			c.authResponse.TeamID, c.authResponse.UserID, browserIdentity.TeamID, browserIdentity.UserID)
	}
	browserEdge, err := edge.NewWithInfo(browserIdentity, browserAuth, edge.OptionHTTPClient(httpClient))
	if err != nil {
		return fmt.Errorf("create browser provider: %w", err)
	}
	c.edgeClient = browserEdge
	c.browserConfigured = true
	c.browserState.Store(int32(browserStateActive))
	c.browserReason.Store("")
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

// attachBrowserToOAuth verifies the browser session belongs to the same
// workspace user as the OAuth token, then attaches it. Any failure leaves the
// client degraded rather than failing startup: OAuth tools still work.
func attachBrowserToOAuth(ap *ApiProvider, xoxcToken, xoxdToken string, logger *zap.Logger) {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil {
		return
	}
	browserAuth, err := slackcreds.New(xoxcToken, xoxdToken)
	if err == nil {
		var (
			httpClient      *http.Client
			browserIdentity *slack.AuthTestResponse
		)
		httpClient, browserIdentity, err = authTest(browserAuth, logger)
		if err == nil {
			err = client.attachBrowserSession(browserAuth, browserIdentity, httpClient)
		}
	}
	if err != nil {
		client.browserConfigured = true
		client.degradeBrowserSession(fmt.Errorf("attach browser session: %w", err))
	}
}

func (c *MCPSlackClient) ClientUserBoot(ctx context.Context) (*edge.ClientUserBootResponse, error) {
	return browserCall(c, "client.userBoot", func() (*edge.ClientUserBootResponse, error) { return c.edgeClient.ClientUserBoot(ctx) })
}

func (c *MCPSlackClient) UsersSearch(ctx context.Context, query string, count int) ([]slack.User, error) {
	return browserCall(c, "users/search", func() ([]slack.User, error) { return c.edgeClient.UsersSearch(ctx, query, count) })
}

func (c *MCPSlackClient) ClientCounts(ctx context.Context) (edge.ClientCountsResponse, error) {
	return browserCall(c, "client.counts", func() (edge.ClientCountsResponse, error) { return c.edgeClient.ClientCounts(ctx) })
}

func (c *MCPSlackClient) ActivityFeed(ctx context.Context, limit int) (edge.ActivityFeedResponse, error) {
	return browserCall(c, "activity.feed", func() (edge.ActivityFeedResponse, error) { return c.edgeClient.ActivityFeed(ctx, limit) })
}

func (c *MCPSlackClient) ActivityMarkRead(ctx context.Context, itemType, feedTs, key string) error {
	_, err := browserCall(c, "activity.markRead", func() (struct{}, error) { return struct{}{}, c.edgeClient.ActivityMarkRead(ctx, itemType, feedTs, key) })
	return err
}

func (c *MCPSlackClient) GetMutedChannels(ctx context.Context) (map[string]bool, error) {
	return browserCall(c, "users.prefs.get", func() (map[string]bool, error) { return c.edgeClient.GetMutedChannels(ctx) })
}

func (c *MCPSlackClient) GetStarredChannelIDs(ctx context.Context, limit int) ([]string, error) {
	if c.isOAuth {
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

	ub, err := c.ClientUserBoot(ctx)
	if err != nil {
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
	return browserCall(c, "saved.list", func() (edge.SavedListResponse, error) { return c.edgeClient.SavedList(ctx, filter, limit, cursor) })
}

func (c *MCPSlackClient) SavedUpdate(ctx context.Context, itemType, itemID, ts, mark string, dateDue int64) error {
	_, err := browserCall(c, "saved.update", func() (struct{}, error) {
		return struct{}{}, c.edgeClient.SavedUpdate(ctx, itemType, itemID, ts, mark, dateDue)
	})
	return err
}

func (c *MCPSlackClient) SavedClearCompleted(ctx context.Context) error {
	_, err := browserCall(c, "saved.clearCompleted", func() (struct{}, error) { return struct{}{}, c.edgeClient.SavedClearCompleted(ctx) })
	return err
}

func (ap *ApiProvider) ConfiguredWithBrowserSession() bool {
	client, ok := ap.client.(*MCPSlackClient)
	return ok && client != nil && client.ConfiguredWithBrowserSession()
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
