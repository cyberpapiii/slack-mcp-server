package oauthcred

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/keychain/keychaintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestKeychainStore(t *testing.T) (*KeychainStore, *keychaintest.Fake) {
	t.Helper()
	store, err := NewKeychainStore("workspace-user")
	require.NoError(t, err)
	keychain := keychaintest.New()
	store.keychain = keychain
	return store, keychain
}

func TestUnitKeychainStoreRedactsKeychainErrors(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.ReadErr = errors.New("sentinel-secret")
	keychain.WriteErr = errors.New("sentinel-secret")
	keychain.DeleteErr = errors.New("sentinel-secret")
	record := Record{Version: RecordVersion, AccessToken: "sentinel-secret"}

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
	lock := fileLock(lockPath)
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

// Without a user cache directory the lock used to fall back to the working
// directory, where the server and slack-mcp-auth each held their own.
func TestUnitWithLockFailsWithoutACacheDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("LocalAppData", "")
	ran := false
	err := WithLock(context.Background(), func() error { ran = true; return nil })
	require.ErrorContains(t, err, "locate OAuth lock")
	assert.False(t, ran)
}

func TestUnitKeychainStoreLoadReportsMissingItem(t *testing.T) {
	store, _ := newTestKeychainStore(t)
	_, err := store.Load(context.Background())
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestUnitKeychainStoreRejectsCorruptRecord(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.Items[keychaintest.Key(store.Service, store.Account)] = []byte("not-json")
	_, err := store.Load(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "corrupt"))
}

func TestUnitKeychainStoreWritesRecordUnderServiceAndAccount(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.Put(t, store.Service, store.Account, Record{
		Version: RecordVersion, AccessToken: "initial", Generation: 0,
	})
	record := Record{
		Version: RecordVersion, AccessToken: "sentinel-access", RefreshToken: "sentinel-refresh",
	}

	require.NoError(t, store.SaveIfGeneration(context.Background(), 0, record))
	assert.Equal(t, 1, keychain.Writes)
	stored := string(keychain.Items[keychaintest.Key(KeychainService, "workspace-user")])
	assert.Contains(t, stored, "sentinel-access")
	assert.Contains(t, stored, "sentinel-refresh")
	loaded, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, record, loaded)
}

func TestUnitKeychainStoreChecksGenerationZeroBeforeOverwrite(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.Put(t, store.Service, store.Account, Record{
		Version: RecordVersion, AccessToken: "existing", Generation: 1,
	})

	err := store.SaveIfGeneration(context.Background(), 0, Record{
		Version: RecordVersion, AccessToken: "replacement", Generation: 1,
	})
	assert.ErrorIs(t, err, ErrGenerationChanged)
	assert.Equal(t, 0, keychain.Writes)
}

func TestUnitKeychainStoreAllowsGenerationZeroOnlyWhenMissing(t *testing.T) {
	store, keychain := newTestKeychainStore(t)

	err := store.SaveIfGeneration(context.Background(), 0, Record{
		Version: RecordVersion, AccessToken: "first", Generation: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, keychain.Writes)
}

func TestUnitKeychainStoreDeleteRemovesItem(t *testing.T) {
	store, keychain := newTestKeychainStore(t)
	keychain.Put(t, store.Service, store.Account, Record{
		Version: RecordVersion, AccessToken: "existing", Generation: 1,
	})

	require.NoError(t, store.Delete(context.Background()))
	assert.Equal(t, 1, keychain.Deletes)
	_, err := store.Load(context.Background())
	assert.ErrorIs(t, err, ErrNotFound)
}
