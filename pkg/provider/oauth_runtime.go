package provider

import (
	"context"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
	"github.com/korotovsky/slack-mcp-server/pkg/slackhttp"
	transportpkg "github.com/korotovsky/slack-mcp-server/pkg/transport"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type OAuthCredentialStatus struct {
	Source             string `json:"source"`
	RotationConfigured bool   `json:"rotation_configured"`
	Store              string `json:"store"`
	State              string `json:"state"`
	Reason             string `json:"reason,omitempty"`
}

func (c CredentialConfig) oauthStatus() OAuthCredentialStatus {
	status := OAuthCredentialStatus{Source: "not_configured", Store: "not_configured", State: "unavailable"}
	if c.OAuthKeychainAccount != "" {
		status.Source = "keychain"
		status.Store = "macos_keychain"
		if c.OAuthClientID != "" {
			status.RotationConfigured = true
			status.State = "configured"
		} else {
			status.State = "foundation"
			status.Reason = "OAuth client configuration is absent; authorization and rotation are unavailable"
		}
		return status
	}
	if c.XOXP != "" {
		status.Source = "static_user_oauth"
		status.Store = "environment"
		status.State = "live"
		status.Reason = "static non-expiring OAuth compatibility mode; durable rotation is unavailable"
		return status
	}
	if c.XOXB != "" {
		status.Source = "static_bot_oauth"
		status.Store = "environment"
		status.State = "live"
		status.Reason = "static non-expiring OAuth compatibility mode; durable rotation is unavailable"
		return status
	}
	return status
}

func loadManagedOAuth(ctx context.Context, creds CredentialConfig, logger *zap.Logger) (*oauthcred.Manager, oauthcred.Record, error) {
	store, err := oauthcred.NewKeychainStore(creds.OAuthKeychainAccount)
	if err != nil {
		return nil, oauthcred.Record{}, err
	}
	httpClient, err := transportpkg.ProvideHTTPClient(nil, logger)
	if err != nil {
		return nil, oauthcred.Record{}, err
	}
	var refresher oauthcred.Refresher
	if creds.OAuthClientID != "" {
		refresher = &oauthcred.SlackRefresher{HTTPClient: httpClient, ClientID: creds.OAuthClientID, ClientSecret: creds.OAuthClientSecret}
	}
	manager := oauthcred.NewManager(store, refresher, oauthcred.WithLock)
	expected, err := store.Load(ctx)
	if err != nil {
		return nil, oauthcred.Record{}, err
	}
	manager.WithValidator(expected.Identity().Validator(oauthcred.SlackAuthTest(httpClient)))
	record, err := manager.Current(ctx)
	if err != nil {
		return nil, oauthcred.Record{}, err
	}
	return manager, record, nil
}

func (ap *ApiProvider) OAuthCredentialStatus() OAuthCredentialStatus {
	status := ap.creds.oauthStatus()
	if ap.oauthLoadErr != nil {
		status.State = "unavailable"
		status.Reason = "stored OAuth login failed to load (" + ap.oauthLoadErr.Error() + "); run slack-mcp-auth login --replace, then restart"
		return status
	}
	if state, loaded := ap.client.oauthRuntimeState.Load().(string); loaded && state != "" {
		status.State = state
		if state == "live" {
			status.Reason = ""
		}
	}
	return status
}

func (ap *ApiProvider) startManagedOAuth(manager *oauthcred.Manager, record oauthcred.Record) {
	client := &ap.client
	expected := oauthcred.IdentityOf(&client.authResponse)
	manager.WithValidator(expected.Validator(func(ctx context.Context, token string) (*slack.AuthTestResponse, error) {
		_, identity, err := client.managedSlackClient(ctx, token)
		return identity, err
	}))
	client.oauthManager = manager
	client.oauthGeneration.Store(record.Generation)
	client.oauthRuntimeState.Store("live")
	go client.runManagedOAuthRefresh(ap.lifetime())
}

// managedSlackClient builds a Web API client for a rotated token. Rotated
// tokens carry no cookies, so the primary HTTP client is shared as-is.
func (c *MCPSlackClient) managedSlackClient(ctx context.Context, token string) (*slack.Client, *slack.AuthTestResponse, error) {
	if c.managedClientFactory != nil {
		return c.managedClientFactory(ctx, token)
	}
	httpClient := c.httpClient
	if httpClient == nil {
		var err error
		httpClient, err = transportpkg.ProvideHTTPClient(nil, c.logger)
		if err != nil {
			return nil, nil, err
		}
	}
	options := []slack.Option{slack.OptionHTTPClient(httpClient)}
	if c.authResponse.URL != "" {
		options = append(options, slack.OptionAPIURL(c.authResponse.URL+"api/"))
	} else {
		options = append(options, slack.OptionAPIURL(slackhttp.SlackAPIBase()))
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

// runManagedOAuthRefresh polls the token store until ctx ends.
func (c *MCPSlackClient) runManagedOAuthRefresh(ctx context.Context) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		err := c.refreshManagedOAuthTick(ctx)
		if err != nil {
			c.logger.Warn("Managed OAuth refresh failed", zap.Error(err))
		}
		timer.Reset(managedOAuthRefreshInterval(err))
	}
}

// oauthShutdownGrace bounds how long Close waits for a refresh in flight.
// A refresh normally takes well under a second.
const oauthShutdownGrace = 10 * time.Second

// refreshManagedOAuthTick runs one refresh while holding oauthRefreshMu, so
// Close can wait for it to finish.
func (c *MCPSlackClient) refreshManagedOAuthTick(ctx context.Context) error {
	c.oauthRefreshMu.Lock()
	defer c.oauthRefreshMu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.refreshManagedOAuthOnce(refreshCtx)
}

// waitForOAuthRefresh blocks until a refresh in flight finishes, or the grace
// period ends. Exiting between Slack's answer and the Keychain write would
// lose the rotated refresh token and force a new login.
func (c *MCPSlackClient) waitForOAuthRefresh() {
	grace := c.oauthShutdownGrace
	if grace <= 0 {
		grace = oauthShutdownGrace
	}
	done := make(chan struct{})
	go func() {
		c.oauthRefreshMu.Lock()
		c.oauthRefreshMu.Unlock()
		close(done)
	}()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		c.logger.Warn("Shutting down with an OAuth refresh still running; if the next start fails to authenticate, run slack-mcp-auth status",
			zap.Duration("waited", grace))
	}
}

func managedOAuthRefreshInterval(err error) time.Duration {
	const (
		base = time.Minute
		max  = 15 * time.Minute
	)
	retryAfter, _ := slackhttp.RetryAfter(err)
	if retryAfter <= base {
		return base
	}
	if retryAfter > max {
		return max
	}
	return retryAfter
}
