package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitOAuthCredentialStatusNeverContainsTokens(t *testing.T) {
	original := getenv
	t.Cleanup(func() { getenv = original })
	getenv = func(key string) string {
		if key == "SLACK_MCP_XOXP_TOKEN" {
			return "sentinel-access-secret"
		}
		return ""
	}
	status := OAuthStatusFromEnvironment()
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sentinel-access-secret")
	assert.Equal(t, "static_user_oauth", status.Source)
	assert.False(t, status.RotationConfigured)
}

func TestUnitOAuthCredentialStatusSupportsPKCEPublicClient(t *testing.T) {
	original := getenv
	t.Cleanup(func() { getenv = original })
	getenv = func(key string) string {
		switch key {
		case "SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT":
			return "loop-user"
		case "SLACK_MCP_OAUTH_CLIENT_ID":
			return "client-id"
		default:
			return ""
		}
	}

	status := OAuthStatusFromEnvironment()
	assert.Equal(t, "configured", status.State)
	assert.True(t, status.RotationConfigured)
}
