package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withKeychainAccounts(t *testing.T, oauth, browser string) {
	t.Helper()
	original := getenv
	t.Cleanup(func() { getenv = original })
	getenv = func(key string) string {
		switch key {
		case "SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT":
			return oauth
		case "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT":
			return browser
		}
		return ""
	}
}

func TestUnitCheckKeychainAccessReadsEveryConfiguredItem(t *testing.T) {
	withKeychainAccounts(t, "oauth-account", "browser-account")
	keychain := newFakeKeychain()
	keychain.items[fakeKeychainKey(defaultOAuthKeychainService, "oauth-account")] = []byte("{}")
	keychain.items[fakeKeychainKey(browserKeychainService, "browser-account")] = []byte("{}")

	read, err := checkKeychainAccess(context.Background(), keychain)

	require.NoError(t, err)
	assert.Equal(t, 2, read)
}

func TestUnitCheckKeychainAccessNamesTheMissingItem(t *testing.T) {
	withKeychainAccounts(t, "oauth-account", "browser-account")
	keychain := newFakeKeychain()
	keychain.items[fakeKeychainKey(defaultOAuthKeychainService, "oauth-account")] = []byte("{}")

	read, err := checkKeychainAccess(context.Background(), keychain)

	require.Error(t, err)
	assert.Equal(t, 1, read)
	assert.Contains(t, err.Error(), "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT")
}

func TestUnitCheckKeychainAccessReportsADeniedRead(t *testing.T) {
	withKeychainAccounts(t, "oauth-account", "")
	keychain := newFakeKeychain()
	keychain.readErr = errors.New("user denied")

	_, err := checkKeychainAccess(context.Background(), keychain)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
}

func TestUnitCheckKeychainAccessWithNothingConfiguredReadsNothing(t *testing.T) {
	withKeychainAccounts(t, "", "")

	read, err := checkKeychainAccess(context.Background(), newFakeKeychain())

	require.NoError(t, err)
	assert.Zero(t, read)
}
