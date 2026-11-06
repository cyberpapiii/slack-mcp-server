package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/keychain/keychaintest"
	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func keychainAccounts(oauth, browser string) CredentialConfig {
	return CredentialConfig{OAuthKeychainAccount: oauth, BrowserKeychainAccount: browser}
}

func TestUnitCheckKeychainAccessReadsEveryConfiguredItem(t *testing.T) {
	creds := keychainAccounts("oauth-account", "browser-account")
	keychain := keychaintest.New()
	keychain.Items[keychaintest.Key(oauthcred.KeychainService, "oauth-account")] = []byte("{}")
	keychain.Items[keychaintest.Key(browserKeychainService, "browser-account")] = []byte("{}")

	read, err := checkKeychainAccess(context.Background(), keychain, creds)

	require.NoError(t, err)
	assert.Equal(t, 2, read)
}

func TestUnitCheckKeychainAccessNamesTheMissingItem(t *testing.T) {
	creds := keychainAccounts("oauth-account", "browser-account")
	keychain := keychaintest.New()
	keychain.Items[keychaintest.Key(oauthcred.KeychainService, "oauth-account")] = []byte("{}")

	read, err := checkKeychainAccess(context.Background(), keychain, creds)

	require.Error(t, err)
	assert.Equal(t, 1, read)
	assert.Contains(t, err.Error(), "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT")
}

func TestUnitCheckKeychainAccessReportsADeniedRead(t *testing.T) {
	creds := keychainAccounts("oauth-account", "")
	keychain := keychaintest.New()
	keychain.ReadErr = errors.New("user denied")

	_, err := checkKeychainAccess(context.Background(), keychain, creds)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
}

func TestUnitCheckKeychainAccessWithNothingConfiguredReadsNothing(t *testing.T) {
	creds := keychainAccounts("", "")

	read, err := checkKeychainAccess(context.Background(), keychaintest.New(), creds)

	require.NoError(t, err)
	assert.Zero(t, read)
}
