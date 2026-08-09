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

func TestUnitKeychainStoreRedactsCommandErrors(t *testing.T) {
	store, err := NewKeychainCredentialStore("workspace-user")
	require.NoError(t, err)
	store.run = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return []byte("sentinel-secret"), errors.New("sentinel-secret")
	}
	record := OAuthTokenRecord{Version: oauthRecordVersion, AccessToken: "sentinel-secret"}

	err = store.SaveIfGeneration(context.Background(), 0, record)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sentinel-secret")
	_, err = store.Load(context.Background())
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

func TestUnitKeychainStoreRejectsCorruptRecord(t *testing.T) {
	store, err := NewKeychainCredentialStore("workspace-user")
	require.NoError(t, err)
	store.run = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return []byte("not-json"), nil
	}
	_, err = store.Load(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "corrupt"))
}

func TestUnitKeychainStorePassesCredentialOnlyThroughStdin(t *testing.T) {
	store, err := NewKeychainCredentialStore("workspace-user")
	require.NoError(t, err)
	var capturedStdin []byte
	var capturedArgs []string
	store.run = func(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		assert.Equal(t, "security", name)
		if len(args) != 0 && args[0] == "find-generic-password" {
			current, marshalErr := json.Marshal(OAuthTokenRecord{
				Version: oauthRecordVersion, AccessToken: "initial", Generation: 0,
			})
			require.NoError(t, marshalErr)
			return current, nil
		}
		capturedStdin = append([]byte(nil), stdin...)
		capturedArgs = append([]string(nil), args...)
		return nil, nil
	}
	record := OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "sentinel-access", RefreshToken: "sentinel-refresh",
	}

	require.NoError(t, store.SaveIfGeneration(context.Background(), 0, record))
	assert.Contains(t, string(capturedStdin), "sentinel-access")
	assert.Contains(t, string(capturedStdin), "sentinel-refresh")
	assert.NotContains(t, strings.Join(capturedArgs, " "), "sentinel")
	assert.Equal(t, "-w", capturedArgs[len(capturedArgs)-1])
}

func TestUnitKeychainStoreChecksGenerationZeroBeforeOverwrite(t *testing.T) {
	store, err := NewKeychainCredentialStore("workspace-user")
	require.NoError(t, err)
	current, err := json.Marshal(OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "existing", Generation: 1,
	})
	require.NoError(t, err)
	writeCalled := false
	store.run = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		if len(args) != 0 && args[0] == "find-generic-password" {
			return current, nil
		}
		writeCalled = true
		return nil, nil
	}

	err = store.SaveIfGeneration(context.Background(), 0, OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "replacement", Generation: 1,
	})
	assert.ErrorIs(t, err, ErrCredentialGenerationChanged)
	assert.False(t, writeCalled)
}

func TestUnitKeychainStoreAllowsGenerationZeroOnlyWhenMissing(t *testing.T) {
	store, err := NewKeychainCredentialStore("workspace-user")
	require.NoError(t, err)
	writes := 0
	store.run = func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
		if len(args) != 0 && args[0] == "find-generic-password" {
			return nil, ErrCredentialNotFound
		}
		writes++
		return nil, nil
	}

	err = store.SaveIfGeneration(context.Background(), 0, OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "first", Generation: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, writes)
}
