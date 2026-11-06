package provider

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// memoryStore is an in-memory oauthcred.Store.
type memoryStore struct {
	mu     sync.Mutex
	record oauthcred.Record
}

func (s *memoryStore) Load(context.Context) (oauthcred.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, nil
}

func (s *memoryStore) SaveIfGeneration(_ context.Context, expected uint64, record oauthcred.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Generation != expected {
		return oauthcred.ErrGenerationChanged
	}
	s.record = record
	return nil
}

func (s *memoryStore) Delete(context.Context) error { return nil }

// hookRefresher rotates like Slack, running onRefresh after Slack has
// already consumed the old refresh token.
type hookRefresher struct {
	now       time.Time
	onRefresh func()
}

func (r *hookRefresher) Refresh(context.Context, string) (oauthcred.RefreshResult, error) {
	if r.onRefresh != nil {
		r.onRefresh()
	}
	return oauthcred.RefreshResult{
		AccessToken: "rotated-access", RefreshToken: "rotated-refresh",
		ExpiresAt: r.now.Add(time.Hour), Scopes: []string{"channels:read"},
	}, nil
}

// expiringStore holds a credential inside the refresh window, so the next
// Current rotates it.
func expiringStore(now time.Time) *memoryStore {
	return &memoryStore{record: oauthcred.Record{
		Version: oauthcred.RecordVersion, AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 7, Scopes: []string{"channels:read"},
	}}
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
	manager := oauthcred.NewManager(store, refresher, nil)

	ctx, cancel := context.WithCancel(context.Background())
	ap := &ApiProvider{client: MCPSlackClient{logger: zap.NewNop(), oauthManager: manager}, ctx: ctx, cancel: cancel, logger: zap.NewNop()}
	client := &ap.client

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
	manager := oauthcred.NewManager(expiringStore(now), &hookRefresher{now: now, onRefresh: func() {
		close(entered)
		<-release
	}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ap := &ApiProvider{client: MCPSlackClient{logger: zap.NewNop(), oauthManager: manager, oauthShutdownGrace: 20 * time.Millisecond}, ctx: ctx, cancel: cancel, logger: zap.NewNop()}
	client := &ap.client

	go client.refreshManagedOAuthTick(ctx)
	<-entered
	start := time.Now()
	ap.Close()
	assert.Less(t, time.Since(start), time.Second)
}

// oauthRoundTripFunc stubs an http.Client transport.
type oauthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f oauthRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
