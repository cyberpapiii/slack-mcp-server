package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKeychain is a map-backed keychainStore. Error fields, when set, are
// returned by the matching method instead of touching the map.
type fakeKeychain struct {
	items     map[string][]byte
	readErr   error
	writeErr  error
	deleteErr error
	writes    int
	deletes   int
}

func newFakeKeychain() *fakeKeychain {
	return &fakeKeychain{items: map[string][]byte{}}
}

func fakeKeychainKey(service, account string) string { return service + "\x00" + account }

func (k *fakeKeychain) Read(_ context.Context, service, account string) ([]byte, error) {
	if k.readErr != nil {
		return nil, k.readErr
	}
	data, ok := k.items[fakeKeychainKey(service, account)]
	if !ok {
		return nil, ErrCredentialNotFound
	}
	return append([]byte(nil), data...), nil
}

func (k *fakeKeychain) Write(_ context.Context, service, account string, data []byte) error {
	if k.writeErr != nil {
		return k.writeErr
	}
	k.writes++
	k.items[fakeKeychainKey(service, account)] = append([]byte(nil), data...)
	return nil
}

func (k *fakeKeychain) Delete(_ context.Context, service, account string) error {
	if k.deleteErr != nil {
		return k.deleteErr
	}
	k.deletes++
	delete(k.items, fakeKeychainKey(service, account))
	return nil
}

func (k *fakeKeychain) put(t *testing.T, service, account string, record any) {
	t.Helper()
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	k.items[fakeKeychainKey(service, account)] = raw
}

func newTestKeychainStore(t *testing.T) (*KeychainCredentialStore, *fakeKeychain) {
	t.Helper()
	store, err := NewKeychainCredentialStore("workspace-user")
	require.NoError(t, err)
	keychain := newFakeKeychain()
	store.keychain = keychain
	return store, keychain
}

func TestUnitKeychainStoreRedactsKeychainErrors(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.readErr = errors.New("sentinel-secret")
	keychain.writeErr = errors.New("sentinel-secret")
	keychain.deleteErr = errors.New("sentinel-secret")
	record := OAuthTokenRecord{Version: oauthRecordVersion, AccessToken: "sentinel-secret"}

	err := store.SaveIfGeneration(context.Background(), 0, record)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sentinel-secret")
	_, err = store.Load(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sentinel-secret")
	err = store.Delete(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sentinel-secret")
}

func TestUnitOAuthFileLockHonorsCancellation(t *testing.T) {
	lockPath := t.TempDir() + "/oauth.lock"
	lock := withOAuthFileLock(lockPath)
	entered := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = lock(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := lock(ctx, func() error { return errors.New("must not run") })
	close(release)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestUnitKeychainStoreLoadReportsMissingItem(t *testing.T) {
	store, _ := newTestKeychainStore(t)
	_, err := store.Load(context.Background())
	assert.ErrorIs(t, err, ErrCredentialNotFound)
}

func TestUnitKeychainStoreRejectsCorruptRecord(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.items[fakeKeychainKey(store.Service, store.Account)] = []byte("not-json")
	_, err := store.Load(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "corrupt"))
}

func TestUnitKeychainStoreWritesRecordUnderServiceAndAccount(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.put(t, store.Service, store.Account, OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "initial", Generation: 0,
	})
	record := OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "sentinel-access", RefreshToken: "sentinel-refresh",
	}

	require.NoError(t, store.SaveIfGeneration(context.Background(), 0, record))
	assert.Equal(t, 1, keychain.writes)
	stored := string(keychain.items[fakeKeychainKey(defaultOAuthKeychainService, "workspace-user")])
	assert.Contains(t, stored, "sentinel-access")
	assert.Contains(t, stored, "sentinel-refresh")
	loaded, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, record, loaded)
}

func TestUnitKeychainStoreChecksGenerationZeroBeforeOverwrite(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.put(t, store.Service, store.Account, OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "existing", Generation: 1,
	})

	err := store.SaveIfGeneration(context.Background(), 0, OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "replacement", Generation: 1,
	})
	assert.ErrorIs(t, err, ErrCredentialGenerationChanged)
	assert.Equal(t, 0, keychain.writes)
}

func TestUnitKeychainStoreAllowsGenerationZeroOnlyWhenMissing(t *testing.T) {
	store, keychain := newTestKeychainStore(t)

	err := store.SaveIfGeneration(context.Background(), 0, OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "first", Generation: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, keychain.writes)
}

func TestUnitKeychainStoreDeleteRemovesItem(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.put(t, store.Service, store.Account, OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "existing", Generation: 1,
	})

	require.NoError(t, store.Delete(context.Background()))
	assert.Equal(t, 1, keychain.deletes)
	_, err := store.Load(context.Background())
	assert.ErrorIs(t, err, ErrCredentialNotFound)
}
