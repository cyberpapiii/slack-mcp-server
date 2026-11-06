package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/keychain/keychaintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBrowserStore(t *testing.T) (*BrowserCredentialStore, *keychaintest.Fake) {
	t.Helper()
	store, err := NewBrowserCredentialStore("workspace-user")
	require.NoError(t, err)
	keychain := keychaintest.New()
	store.keychain = keychain
	return store, keychain
}

func TestUnitBrowserKeychainStoreLoadsExistingItemFormat(t *testing.T) {
	store, keychain := newTestBrowserStore(t)
	keychain.Items[keychaintest.Key(browserKeychainService, "workspace-user")] =
		[]byte(`{"version":1,"xoxc":"sentinel-xoxc","xoxd":"sentinel-xoxd","team_id":"T1","user_id":"U1"}`)

	loaded, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, BrowserTokenRecord{Version: 1, XOXC: "sentinel-xoxc", XOXD: "sentinel-xoxd"}, loaded)
}

func TestUnitBrowserKeychainStoreLoadErrors(t *testing.T) {
	store, keychain := newTestBrowserStore(t)

	_, err := store.Load(context.Background())
	assert.EqualError(t, err, "browser credential not found in macOS Keychain")

	keychain.Items[keychaintest.Key(browserKeychainService, "workspace-user")] = []byte(`{"version":1,"xoxc":""}`)
	_, err = store.Load(context.Background())
	assert.EqualError(t, err, "browser credential in macOS Keychain is corrupt")
}

func TestUnitBrowserKeychainStoreReportsADeniedRead(t *testing.T) {
	store, keychain := newTestBrowserStore(t)
	keychain.ReadErr = errors.New("sentinel-secret")

	_, err := store.Load(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access denied")
	assert.NotContains(t, err.Error(), "not found")
	assert.NotContains(t, err.Error(), "sentinel-secret")
}
