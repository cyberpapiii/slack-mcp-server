package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// countingCacheClient counts full users and channels fetches.
type countingCacheClient struct {
	SlackAPI

	users    atomic.Int32
	channels atomic.Int32
}

func (c *countingCacheClient) GetUsersContext(context.Context, ...slack.GetUsersOption) ([]slack.User, error) {
	c.users.Add(1)
	return []slack.User{{ID: "U1", Name: "one"}}, nil
}

func (c *countingCacheClient) ClientUserBoot(context.Context) (*edge.ClientUserBootResponse, error) {
	return nil, errors.New("no browser session in this test")
}

func (c *countingCacheClient) GetConversationsContext(context.Context, *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	c.channels.Add(1)
	return nil, "", nil
}

// writeAgedCache writes a one-entry cache file last modified age ago.
func writeAgedCache(t *testing.T, path string, age time.Duration) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(`[{"id":"X1"}]`), 0o600))
	then := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, then, then))
}

func newRevalidateProvider(t *testing.T, client SlackAPI) *ApiProvider {
	t.Helper()
	dir := t.TempDir()
	ap := &ApiProvider{
		client:                   client,
		logger:                   zap.NewNop(),
		cacheTTL:                 24 * time.Hour,
		backgroundRefreshTimeout: time.Minute,
		usersCachePath:           filepath.Join(dir, "users.json"),
		channelsCachePath:        filepath.Join(dir, "channels.json"),
	}
	ap.usersSnapshot.Store(newUsersCache(nil))
	ap.channelsSnapshot.Store(newChannelsCache(0))
	return ap
}

func cacheIsExpired(path string, ttl time.Duration) bool {
	expired, _ := cacheExpired(path, ttl)
	return expired
}

func TestUnitCacheExpired(t *testing.T) {
	dir := t.TempDir()
	fresh, old := filepath.Join(dir, "fresh.json"), filepath.Join(dir, "old.json")
	writeAgedCache(t, fresh, time.Hour)
	writeAgedCache(t, old, 25*time.Hour)

	assert.False(t, cacheIsExpired(fresh, 24*time.Hour))
	assert.True(t, cacheIsExpired(old, 24*time.Hour))
	assert.False(t, cacheIsExpired(old, 0), "a TTL of 0 means the cache never expires")
	assert.True(t, cacheIsExpired(filepath.Join(dir, "missing.json"), 24*time.Hour), "a cache never written is due")
}

// A server that runs for days used to keep the users list it loaded at
// startup; now a cache past its TTL is refetched while the server runs.
func TestUnitRevalidateCachesRefetchesOnlyTheStaleCache(t *testing.T) {
	client := &countingCacheClient{}
	ap := newRevalidateProvider(t, client)
	writeAgedCache(t, ap.usersCachePath, 25*time.Hour)
	writeAgedCache(t, ap.channelsCachePath, time.Hour)

	ap.revalidateCaches(context.Background())

	assert.Equal(t, int32(1), client.users.Load(), "users cache is past its TTL")
	assert.Equal(t, int32(0), client.channels.Load(), "channels cache is still fresh")
	assert.False(t, cacheIsExpired(ap.usersCachePath, ap.cacheTTL), "the refetch rewrote the file")
	_, ok := ap.ProvideUsersMap().Users["U1"]
	assert.True(t, ok, "the refetched users are live")
}

func TestUnitWarmupNextDelay(t *testing.T) {
	assert.Equal(t, 30*time.Second, warmupNextDelay(2))
	assert.Equal(t, 30*time.Second, warmupNextDelay(warmupFastAttempts))
	assert.Equal(t, 5*time.Minute, warmupNextDelay(warmupFastAttempts+1))
	assert.Equal(t, 5*time.Minute, warmupNextDelay(100))
}

// usersFirstClient records which cache fetch reached Slack first and answers
// with one user and one DM with that user.
type usersFirstClient struct {
	SlackAPI

	order []string
}

func (c *usersFirstClient) GetUsersContext(context.Context, ...slack.GetUsersOption) ([]slack.User, error) {
	c.order = append(c.order, "users")
	return []slack.User{{ID: "U1", Name: "one"}}, nil
}

func (c *usersFirstClient) ClientUserBoot(context.Context) (*edge.ClientUserBootResponse, error) {
	return nil, errors.New("no browser session in this test")
}

func (c *usersFirstClient) GetConversationsContext(context.Context, *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	c.order = append(c.order, "channels")
	return []slack.Channel{{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "D1", IsIM: true, User: "U1"}}}}, "", nil
}

func TestUnitCacheLifecycleWarmsUsersBeforeChannelsThenReportsReady(t *testing.T) {
	client := &usersFirstClient{}
	ap := newRevalidateProvider(t, client)
	ap.conversationsLimiter = rate.NewLimiter(rate.Inf, 1)
	ready := make(chan struct{})

	ap.runCacheLifecycle(context.Background(), func() { close(ready) })

	select {
	case <-ready:
	default:
		t.Fatal("onReady was not called once both caches loaded")
	}
	assert.Equal(t, []string{"users", "channels"}, client.order)
	assert.Equal(t, "@one", ap.ProvideChannelsMaps().Channels["D1"].Name, "DM names map from the users fetched first")
}

// The warm-up loop used to sleep between attempts with no way to stop it.
func TestUnitCacheLifecycleStopsOnCancelBetweenAttempts(t *testing.T) {
	ap := newRevalidateProvider(t, &countingCacheClient{})
	ap.conversationsLimiter = rate.NewLimiter(rate.Inf, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ap.runCacheLifecycle(ctx, func() { t.Error("caches cannot be ready") })
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("warm-up loop did not stop on cancel")
	}
}

func TestUnitCacheRevalidationLoopRunsOnATimerAndStopsOnCancel(t *testing.T) {
	client := &countingCacheClient{}
	ap := newRevalidateProvider(t, client)
	writeAgedCache(t, ap.usersCachePath, 25*time.Hour)
	writeAgedCache(t, ap.channelsCachePath, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ap.runCacheRevalidation(ctx, 5*time.Millisecond)
		close(done)
	}()
	assert.Eventually(t, func() bool { return client.users.Load() == 1 }, time.Second, 5*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, int32(1), client.users.Load(), "a fresh cache is not refetched on later ticks")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revalidation loop did not stop on cancel")
	}
}
