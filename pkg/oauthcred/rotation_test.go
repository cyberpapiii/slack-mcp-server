package oauthcred

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ctxCheckingStore refuses a save on a done context, as the Keychain store
// does before its cgo write.
type ctxCheckingStore struct{ memoryStore }

func (s *ctxCheckingStore) SaveIfGeneration(ctx context.Context, expected uint64, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.memoryStore.SaveIfGeneration(ctx, expected, record)
}

// hookRefresher rotates like Slack, running onRefresh after Slack has
// already consumed the old refresh token.
type hookRefresher struct {
	now       time.Time
	onRefresh func()
}

func (r *hookRefresher) Refresh(context.Context, string) (RefreshResult, error) {
	if r.onRefresh != nil {
		r.onRefresh()
	}
	return RefreshResult{
		AccessToken: "rotated-access", RefreshToken: "rotated-refresh",
		ExpiresAt: r.now.Add(time.Hour), Scopes: []string{"channels:read"},
	}, nil
}

func expiringStore(now time.Time) *ctxCheckingStore {
	return &ctxCheckingStore{memoryStore{record: Record{
		Version: RecordVersion, AccessToken: "old-access", RefreshToken: "old-refresh",
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
	manager := NewManager(store, &hookRefresher{now: now, onRefresh: cancel}, noLock)
	manager.now = func() time.Time { return now }

	_, _ = manager.Current(ctx)

	saved, _ := store.Load(context.Background())
	assert.Equal(t, "rotated-refresh", saved.RefreshToken, "the rotated refresh token is durable")
	assert.Equal(t, uint64(8), saved.Generation)
}
