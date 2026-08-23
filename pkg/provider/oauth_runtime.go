package provider

import (
	"context"
	"fmt"
	"time"

	transportpkg "github.com/korotovsky/slack-mcp-server/pkg/transport"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

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
	if c.authResponse != nil && c.authResponse.URL != "" {
		options = append(options, slack.OptionAPIURL(c.authResponse.URL+"api/"))
	} else {
		options = append(options, slack.OptionAPIURL(transportpkg.SlackAPIBase()))
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
		refreshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := c.refreshManagedOAuthOnce(refreshCtx)
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
	retryAfter := SlackRetryAfter(err)
	if retryAfter <= base {
		return base
	}
	if retryAfter > max {
		return max
	}
	return retryAfter
}
