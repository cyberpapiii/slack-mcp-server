package provider

import (
	"testing"

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

			ap := New("stdio", zap.NewNop())
			require.NotNil(t, ap.WebAPI(), "demo must expose the Web API surface")

			auth, err := ap.Slack().AuthTest()
			require.NoError(t, err)
			assert.Equal(t, "TEAM123456", auth.TeamID)

			_, isUnsupported := ap.Drafts().(UnsupportedDraftsProvider)
			assert.False(t, isUnsupported, "demo must expose browser-only tools")

			_, err = ap.DND()
			assert.NoError(t, err, "demo must expose user-OAuth tools")
			_, err = ap.Lists()
			assert.NoError(t, err)
		})
	}
}
