package provider

import (
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDemoCredentialsBuildFullClient(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"xoxp":      {"SLACK_MCP_XOXP_TOKEN": "demo"},
		"xoxc_xoxd": {"SLACK_MCP_XOXC_TOKEN": "demo", "SLACK_MCP_XOXD_TOKEN": "demo"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"SLACK_MCP_XOXP_TOKEN", "SLACK_MCP_XOXB_TOKEN", "SLACK_MCP_XOXC_TOKEN", "SLACK_MCP_XOXD_TOKEN", "SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT"} {
				t.Setenv(k, "")
			}
			for k, v := range env {
				t.Setenv(k, v)
			}
			require.True(t, IsDemoCredentials())

			ap, err := New("stdio", zap.NewNop())
			require.NoError(t, err)
			defer ap.Close()
			require.NotNil(t, ap.WebAPI(), "demo must expose the Web API surface")

			auth, err := ap.Slack().AuthTest()
			require.NoError(t, err)
			assert.Equal(t, "TEAM123456", auth.TeamID)

			assert.Equal(t, ModeDemo, ap.Mode())
			for _, requirement := range []capability.Requirement{capability.UserToken, capability.UserOAuth, capability.BrowserSession} {
				assert.True(t, ap.Satisfies(requirement), "demo registers every tool")
			}
		})
	}
}

func TestUnitFailedManagedOAuthFallsBackToBrowserSession(t *testing.T) {
	for _, k := range []string{"SLACK_MCP_XOXP_TOKEN", "SLACK_MCP_XOXB_TOKEN", "SLACK_MCP_OAUTH_CLIENT_ID", "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT"} {
		t.Setenv(k, "")
	}
	t.Setenv("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", "slack-mcp-test-account-that-does-not-exist")
	t.Setenv("SLACK_MCP_XOXC_TOKEN", "demo")
	t.Setenv("SLACK_MCP_XOXD_TOKEN", "demo")

	ap, err := New("stdio", zap.NewNop())
	require.NoError(t, err, "a dead OAuth login must not take the browser session down with it")
	defer ap.Close()

	status := ap.OAuthCredentialStatus()
	assert.Equal(t, "unavailable", status.State)
	assert.Contains(t, status.Reason, "slack-mcp-auth login --replace")
}

func TestUnitFailedManagedOAuthStaysFatalWithoutBrowserSession(t *testing.T) {
	for _, k := range []string{"SLACK_MCP_XOXP_TOKEN", "SLACK_MCP_XOXB_TOKEN", "SLACK_MCP_XOXC_TOKEN", "SLACK_MCP_XOXD_TOKEN", "SLACK_MCP_OAUTH_CLIENT_ID", "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT"} {
		t.Setenv(k, "")
	}
	t.Setenv("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", "slack-mcp-test-account-that-does-not-exist")

	_, err := New("stdio", zap.NewNop())
	require.ErrorContains(t, err, "load managed OAuth credential")
}

func TestUnitAuthFailureNamesNetworkProblems(t *testing.T) {
	network := fmt.Errorf("auth.test: %w", &url.Error{Op: "Post", URL: "https://slack.com/api/auth.test", Err: errors.New("no route to host")})
	assert.ErrorContains(t, authFailure("tokens are invalid", network), "could not reach Slack")
	assert.NotContains(t, authFailure("tokens are invalid", network).Error(), "tokens are invalid")

	assert.ErrorContains(t, authFailure("tokens are invalid", errors.New("invalid_auth")), "authentication failed: tokens are invalid")
}
