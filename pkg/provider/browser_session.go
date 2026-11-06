package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
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

// notifyBrowserDegradation posts a macOS notification; osascript exists
// nowhere else.
func notifyBrowserDegradation(reason string, logger *zap.Logger) {
	if runtime.GOOS != "darwin" {
		return
	}
	if err := exec.Command("osascript", osascriptNotificationArgs(reason)...).Run(); err != nil {
		logger.Debug("Failed to emit browser degradation notification", zap.Error(err))
	}
}

// browserAuthErrorCodes are the Slack error codes that mean the xoxc/xoxd
// session itself is dead, as opposed to a per-call failure. The edge client
// reports them as slack.SlackErrorResponse.
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
	var statusErr slack.StatusCodeError
	if errors.As(err, &statusErr) {
		return statusErr.Code == http.StatusUnauthorized || statusErr.Code == http.StatusForbidden
	}
	var slackErr slack.SlackErrorResponse
	if errors.As(err, &slackErr) {
		return browserAuthErrorCodes[slackErr.Err]
	}
	return false
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

// prefersBrowser reports whether a read that Slack serves both ways should go
// through the browser session: always when it is the only session, and beside
// OAuth while it is healthy. It answers more completely there (users/search
// sees Slack Connect users, client.userBoot carries starred channels, mute
// state needs users.prefs).
func (c *MCPSlackClient) prefersBrowser() bool {
	switch c.mode {
	case ModeBrowser:
		return true
	case ModeUserOAuthBrowser:
		return c.browserFeaturesAvailable()
	default:
		return false
	}
}

// fallsBackToOAuth reports whether a browser read that failed with err can be
// retried on OAuth: the call found the session dead and OAuth is there.
func (c *MCPSlackClient) fallsBackToOAuth(err error) bool {
	return c.mode == ModeUserOAuthBrowser && errors.Is(err, ErrBrowserSessionUnavailable)
}

// preferBrowser answers a read Slack serves both ways: through the browser
// session while it is preferred, else (or once that read finds the session
// dead) through OAuth. A session that is not preferred always has OAuth.
func preferBrowser[T any](c *MCPSlackClient, browserRead, oauthRead func() (T, error)) (T, error) {
	if c.prefersBrowser() {
		v, err := browserRead()
		if !c.fallsBackToOAuth(err) {
			return v, err
		}
	}
	return oauthRead()
}

func (c *MCPSlackClient) browserDegradedReason() string {
	if reason, ok := c.browserReason.Load().(string); ok {
		return reason
	}
	return ""
}

func (c *MCPSlackClient) initBrowserState() {
	if !c.mode.BrowserSession() {
		c.browserState.Store(int32(browserStateOAuthOnly))
		return
	}
	c.browserState.Store(int32(browserStateActive))
}

func (c *MCPSlackClient) degradeBrowserSession(reason error) {
	if !c.mode.BrowserSession() {
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
	c.logger.Warn("Browser-session Slack auth degraded", zap.String("reason", reasonText), zap.Bool("standard_oauth", c.mode.OAuth()))
	if c.browserNotified.CompareAndSwap(false, true) {
		browserDegradationNotifier(reasonText, c.logger)
	}
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

// attachBrowserSession adds browser-only Slack surfaces to an OAuth-primary
// client. Standard Web API calls continue to use the OAuth client.
func (c *MCPSlackClient) attachBrowserSession(browserAuth slackcreds.Value, browserIdentity *slack.AuthTestResponse, httpClient *http.Client) error {
	if err := oauthcred.IdentityOf(&c.authResponse).Match(oauthcred.IdentityOf(browserIdentity)); err != nil {
		return fmt.Errorf("browser provider %w", err)
	}
	browserEdge := edge.NewWithInfo(browserIdentity, browserAuth.SlackToken(), httpClient, edge.OptionLogger(c.logger))
	c.useBrowserEdge(browserEdge)
	return nil
}

// useBrowserEdge makes browserEdge the live browser client and marks the
// session active. The client is stored before the state, so a caller that
// sees the session active also sees the new client.
func (c *MCPSlackClient) useBrowserEdge(browserEdge *edge.Client) {
	c.edgeClient.Store(browserEdge)
	c.browserReason.Store("")
	c.browserNotified.Store(false)
	c.browserState.Store(int32(browserStateActive))
}

// browserReprobeInterval is how often a degraded browser session is retried.
// Recovery needs the credential to work again (a refreshed Keychain item, or
// Slack clearing a transient failure), so a slow cadence is enough.
const browserReprobeInterval = 5 * time.Minute

// browserProbeFunc loads the browser credential again and checks it with
// auth.test.
type browserProbeFunc func(ctx context.Context) (slackcreds.Value, *slack.AuthTestResponse, *http.Client, error)

// reprobeBrowser retries a degraded browser session once and reattaches it
// when the credential works again for the same workspace user.
func (c *MCPSlackClient) reprobeBrowser(ctx context.Context) error {
	if c.browserProbe == nil || browserRuntimeState(c.browserState.Load()) != browserStateDegraded {
		return nil
	}
	browserAuth, identity, httpClient, err := c.browserProbe(ctx)
	if err != nil {
		return err
	}
	if err := oauthcred.IdentityOf(&c.authResponse).Match(oauthcred.IdentityOf(identity)); err != nil {
		return fmt.Errorf("browser provider %w", err)
	}
	browserEdge := edge.NewWithInfo(identity, browserAuth.SlackToken(), httpClient, edge.OptionLogger(c.logger))
	c.useBrowserEdge(browserEdge)
	c.logger.Info("Browser-session Slack auth recovered")
	return nil
}

// runBrowserReprobe retries a degraded browser session every interval until
// ctx ends. A healthy session costs one state check per tick.
func (c *MCPSlackClient) runBrowserReprobe(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := c.reprobeBrowser(probeCtx)
		cancel()
		if err != nil {
			c.logger.Debug("Browser-session Slack auth still unavailable", zap.Error(err))
		}
	}
}

// startBrowserReprobe lets a browser session attached to OAuth recover from
// degradation without a restart. It reloads the credential from the Keychain
// when account is set, else reuses the xoxc/xoxd values from the environment.
func (ap *ApiProvider) startBrowserReprobe(client *MCPSlackClient, account, xoxc, xoxd string) {
	client.browserProbe = func(ctx context.Context) (slackcreds.Value, *slack.AuthTestResponse, *http.Client, error) {
		token, cookie := xoxc, xoxd
		if account != "" {
			record, err := loadBrowserCredential(ctx, account)
			if err != nil {
				return slackcreds.Value{}, nil, nil, err
			}
			token, cookie = record.XOXC, record.XOXD
		}
		browserAuth, err := slackcreds.New(token, cookie)
		if err != nil {
			return slackcreds.Value{}, nil, nil, err
		}
		httpClient, identity, err := authTest(browserAuth, client.logger)
		return browserAuth, identity, httpClient, err
	}
	go client.runBrowserReprobe(ap.lifetime(), browserReprobeInterval)
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

// attachBrowser verifies the browser session belongs to the same workspace
// user as the OAuth token, then attaches it. Any failure leaves the session
// degraded rather than failing startup: OAuth tools still work.
func (c *MCPSlackClient) attachBrowser(xoxcToken, xoxdToken string) {
	browserAuth, err := slackcreds.New(xoxcToken, xoxdToken)
	if err == nil {
		var (
			httpClient      *http.Client
			browserIdentity *slack.AuthTestResponse
		)
		httpClient, browserIdentity, err = authTest(browserAuth, c.logger)
		if err == nil {
			err = c.attachBrowserSession(browserAuth, browserIdentity, httpClient)
		}
	}
	if err != nil {
		c.degradeBrowserSession(fmt.Errorf("attach browser session: %w", err))
	}
}

func (c *MCPSlackClient) ClientUserBoot(ctx context.Context) (*edge.ClientUserBootResponse, error) {
	return browserCall(c, "client.userBoot", func() (*edge.ClientUserBootResponse, error) { return c.edgeClient.Load().ClientUserBoot(ctx) })
}

func (c *MCPSlackClient) UsersSearch(ctx context.Context, query string, count int) ([]slack.User, error) {
	return browserCall(c, "users/search", func() ([]slack.User, error) { return c.edgeClient.Load().UsersSearch(ctx, query, count) })
}

func (c *MCPSlackClient) ClientCounts(ctx context.Context) (UnreadCounts, error) {
	counts, err := browserCall(c, "client.counts", func() (edge.ClientCountsResponse, error) { return c.edgeClient.Load().ClientCounts(ctx) })
	return unreadCountsFromEdge(counts), err
}

func (c *MCPSlackClient) ActivityFeed(ctx context.Context, limit int) ([]ActivityEntry, error) {
	feed, err := browserCall(c, "activity.feed", func() (edge.ActivityFeedResponse, error) { return c.edgeClient.Load().ActivityFeed(ctx, limit) })
	return activityFromEdge(feed), err
}

func (c *MCPSlackClient) ActivityMarkRead(ctx context.Context, itemType, feedTs, key string) error {
	_, err := browserCall(c, "activity.markRead", func() (struct{}, error) {
		return struct{}{}, c.edgeClient.Load().ActivityMarkRead(ctx, itemType, feedTs, key)
	})
	return err
}

func (c *MCPSlackClient) GetMutedChannels(ctx context.Context) (map[string]bool, error) {
	return browserCall(c, "users.prefs.get", func() (map[string]bool, error) { return c.edgeClient.Load().GetMutedChannels(ctx) })
}

func (c *MCPSlackClient) GetStarredChannelIDs(ctx context.Context, limit int) ([]string, error) {
	return preferBrowser(c,
		func() ([]string, error) { return c.bootStarredChannelIDs(ctx) },
		func() ([]string, error) { return c.starredChannelIDs(ctx, limit) })
}

func (c *MCPSlackClient) starredChannelIDs(ctx context.Context, limit int) ([]string, error) {
	// v0.29 of slack-go replaced the Count/Page pair with cursor pagination;
	// an empty cursor is still the first page.
	items, _, err := c.standardSlackClient().ListStarsContext(ctx, slack.StarsParameters{Limit: limit})
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

func (c *MCPSlackClient) bootStarredChannelIDs(ctx context.Context) ([]string, error) {
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

func (c *MCPSlackClient) SavedList(ctx context.Context, filter string, limit int, cursor string) (SavedPage, error) {
	page, err := browserCall(c, "saved.list", func() (edge.SavedListResponse, error) {
		return c.edgeClient.Load().SavedList(ctx, filter, limit, cursor)
	})
	return savedPageFromEdge(page), err
}

func (c *MCPSlackClient) SavedUpdate(ctx context.Context, update SavedUpdate) error {
	params := edge.SavedUpdateParams{ItemType: update.Type, ItemID: update.ID, Ts: update.Ts, Mark: update.Mark, TodoState: update.TodoState, DateDue: update.DateDue}
	_, err := browserCall(c, "saved.update", func() (struct{}, error) {
		return struct{}{}, c.edgeClient.Load().SavedUpdate(ctx, params)
	})
	return err
}

func (c *MCPSlackClient) SavedAdd(ctx context.Context, add SavedAdd) (SavedItem, error) {
	params, err := add.edgeParams()
	if err != nil {
		return SavedItem{}, err
	}
	item, err := browserCall(c, "saved.add", func() (edge.SavedItem, error) {
		return c.edgeClient.Load().SavedAdd(ctx, params)
	})
	return savedItemFromEdge(item), err
}

func (c *MCPSlackClient) SavedDelete(ctx context.Context, ref SavedRef) error {
	_, err := browserCall(c, "saved.delete", func() (struct{}, error) {
		return struct{}{}, c.edgeClient.Load().SavedDelete(ctx, ref.Type, ref.ID, ref.Ts)
	})
	return err
}

func (c *MCPSlackClient) SavedClearCompleted(ctx context.Context) error {
	_, err := browserCall(c, "saved.clearCompleted", func() (struct{}, error) { return struct{}{}, c.edgeClient.Load().SavedClearCompleted(ctx) })
	return err
}

// PrefersBrowser reports whether reads Slack serves both ways should use the
// browser session right now.
func (ap *ApiProvider) PrefersBrowser() bool {
	return ap.client.prefersBrowser()
}

func (ap *ApiProvider) BrowserFeaturesAvailable() bool {
	return ap.client.browserFeaturesAvailable()
}

func (ap *ApiProvider) BrowserDegradedReason() string {
	return ap.client.browserDegradedReason()
}

func (ap *ApiProvider) BrowserCredentialSource() string {
	return ap.creds.browserCredentialSource()
}
