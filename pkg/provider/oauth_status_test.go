package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitOAuthCredentialStatusNeverContainsTokens(t *testing.T) {
	status := CredentialConfig{XOXP: "sentinel-access-secret"}.oauthStatus()
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sentinel-access-secret")
	assert.Equal(t, "static_user_oauth", status.Source)
	assert.False(t, status.RotationConfigured)
}

func TestUnitOAuthCredentialStatusSupportsPKCEPublicClient(t *testing.T) {
	status := CredentialConfig{OAuthKeychainAccount: "loop-user", OAuthClientID: "client-id"}.oauthStatus()
	assert.Equal(t, "configured", status.State)
	assert.True(t, status.RotationConfigured)
}

func TestUnitCredentialConfigTrimsEveryValue(t *testing.T) {
	t.Setenv("SLACK_MCP_XOXP_TOKEN", " demo\n")
	t.Setenv("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", "  ")
	creds := CredentialConfigFromEnv()
	assert.Equal(t, "demo", creds.XOXP)
	assert.True(t, creds.Demo())
	assert.Empty(t, creds.OAuthKeychainAccount)
}
