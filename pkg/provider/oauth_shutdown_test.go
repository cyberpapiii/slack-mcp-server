package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ctxCheckingStore refuses a save on a done context, as the Keychain store
// does before its cgo write.
type ctxCheckingStore struct{ memoryCredentialStore }

func (s *ctxCheckingStore) SaveIfGeneration(ctx context.Context, expected uint64, record OAuthTokenRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.memoryCredentialStore.SaveIfGeneration(ctx, expected, record)
}

// hookRefresher rotates like Slack, running onRefresh after Slack has
// already consumed the old refresh token.
type hookRefresher struct {
	now       time.Time
	onRefresh func()
}

func (r *hookRefresher) Refresh(context.Context, string) (OAuthRefreshResult, error) {
	if r.onRefresh != nil {
		r.onRefresh()
	}
	return OAuthRefreshResult{
		AccessToken: "rotated-access", RefreshToken: "rotated-refresh",
		ExpiresAt: r.now.Add(time.Hour), Scopes: []string{"channels:read"},
	}, nil
}

func expiringStore(now time.Time) *ctxCheckingStore {
	return &ctxCheckingStore{memoryCredentialStore{record: OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 7, Scopes: []string{"channels:read"},
	}}}
}

// Slack consumes the old refresh token when it answers, so a context that
// ends after the answer (shutdown, the 30s refresh deadline) must not stop
// the rotated token reaching the store. Losing it forces a new login.
func TestUnitOAuthRotationIsPersistedEvenIfTheCallerStopsWaiting(t *testing.T) {
	now := time.Now().UTC()
	store := expiringStore(now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewOAuthTokenManager(store, &hookRefresher{now: now, onRefresh: cancel}, nil)
	manager.now = func() time.Time { return now }

	_, _ = manager.Current(ctx)

	saved, _ := store.Load(context.Background())
	assert.Equal(t, "rotated-refresh", saved.RefreshToken, "the rotated refresh token is durable")
	assert.Equal(t, uint64(8), saved.Generation)
}

// Close waits for a refresh already in flight, so a normal shutdown (Plug
// disabling the server, SIGTERM on stdio) never exits between Slack's answer
// and the Keychain write.
func TestUnitCloseWaitsForAnInFlightOAuthRefresh(t *testing.T) {
	now := time.Now().UTC()
	store := expiringStore(now)
	entered, release := make(chan struct{}), make(chan struct{})
	refresher := &hookRefresher{now: now, onRefresh: func() {
		close(entered)
		<-release
	}}
	manager := NewOAuthTokenManager(store, refresher, nil)
	manager.now = func() time.Time { return now }

	ctx, cancel := context.WithCancel(context.Background())
	client := &MCPSlackClient{logger: zap.NewNop(), oauthManager: manager, managedClientFactory: nil}
	ap := &ApiProvider{client: client, ctx: ctx, cancel: cancel, logger: zap.NewNop()}

	go client.refreshManagedOAuthTick(ctx)
	<-entered

	closed := make(chan struct{})
	go func() {
		ap.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a rotation was between Slack and the store")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the refresh finished")
	}
	saved, _ := store.Load(context.Background())
	require.Equal(t, "rotated-refresh", saved.RefreshToken)
}

func TestUnitCloseDoesNotWaitForeverOnAStuckRefresh(t *testing.T) {
	now := time.Now().UTC()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	manager := NewOAuthTokenManager(expiringStore(now), &hookRefresher{now: now, onRefresh: func() {
		close(entered)
		<-release
	}}, nil)
	manager.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	client := &MCPSlackClient{logger: zap.NewNop(), oauthManager: manager, oauthShutdownGrace: 20 * time.Millisecond}
	ap := &ApiProvider{client: client, ctx: ctx, cancel: cancel, logger: zap.NewNop()}

	go client.refreshManagedOAuthTick(ctx)
	<-entered
	start := time.Now()
	ap.Close()
	assert.Less(t, time.Since(start), time.Second)
}
