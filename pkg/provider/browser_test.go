package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitBrowserKeychainStoreRoundTripsUnderBrowserService(t *testing.T) {
	store, err := NewBrowserCredentialStore("workspace-user")
	require.NoError(t, err)
	keychain := newFakeKeychain()
	store.keychain = keychain
	record := BrowserTokenRecord{Version: 1, XOXC: "sentinel-xoxc", XOXD: "sentinel-xoxd", TeamID: "T1", UserID: "U1"}

	require.NoError(t, store.Save(context.Background(), record))
	assert.Contains(t, string(keychain.items[fakeKeychainKey(browserKeychainService, "workspace-user")]), "sentinel-xoxc")
	loaded, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, record, loaded)
}

func TestUnitBrowserKeychainStoreLoadErrors(t *testing.T) {
	store, err := NewBrowserCredentialStore("workspace-user")
	require.NoError(t, err)
	keychain := newFakeKeychain()
	store.keychain = keychain

	_, err = store.Load(context.Background())
	assert.EqualError(t, err, "browser credential not found in macOS Keychain")

	keychain.items[fakeKeychainKey(browserKeychainService, "workspace-user")] = []byte(`{"version":1,"xoxc":""}`)
	_, err = store.Load(context.Background())
	assert.EqualError(t, err, "browser credential in macOS Keychain is corrupt")
}
