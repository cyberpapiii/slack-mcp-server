package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUnitRefreshFlightWaitersShareLeaderError(t *testing.T) {
	var flight refreshFlight
	boom := errors.New("boom")
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32

	fetch := func(ctx context.Context) error {
		calls.Add(1)
		close(entered)
		<-release
		return boom
	}

	leaderErr := make(chan error, 1)
	go func() { leaderErr <- flight.do(context.Background(), fetch) }()
	<-entered

	const waiters = 3
	var wg sync.WaitGroup
	var waiting atomic.Int32
	waiterErrs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			waiting.Add(1)
			waiterErrs <- flight.do(context.Background(), fetch)
		}()
	}
	// Every waiter must be parked on the leader before it is released, or a
	// late waiter would start a second fetch of its own.
	require.Eventually(t, func() bool { return waiting.Load() == waiters }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	require.True(t, flight.inFlight())
	close(release)
	wg.Wait()
	close(waiterErrs)

	require.ErrorIs(t, <-leaderErr, boom)
	for err := range waiterErrs {
		assert.ErrorIs(t, err, boom, "waiters must see the leader's error, not nil")
	}
	assert.Equal(t, int32(1), calls.Load(), "only the leader fetches")
	assert.False(t, flight.inFlight())
}

func TestUnitRefreshFlightWaiterHonorsOwnContext(t *testing.T) {
	var flight refreshFlight
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	go func() {
		_ = flight.do(context.Background(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := flight.do(ctx, func(context.Context) error { return nil })
	assert.ErrorIs(t, err, context.Canceled)
}

func TestUnitSearchUsersInCacheOrderIsDeterministic(t *testing.T) {
	var users []slack.User
	for _, id := range []string{"U9", "U3", "U7", "U1", "U5", "U8", "U2", "U6", "U4"} {
		users = append(users, slack.User{ID: id, Name: "same-" + id})
	}
	ap := newTestApiProvider(nil, newUsersCache(users))
	ap.usersReady.Store(true)

	first, err := ap.searchUsersInCache("same", 3)
	require.NoError(t, err)
	require.Len(t, first, 3)
	assert.Equal(t, []string{"U1", "U2", "U3"}, []string{first[0].ID, first[1].ID, first[2].ID})

	for i := 0; i < 20; i++ {
		again, err := ap.searchUsersInCache("same", 3)
		require.NoError(t, err)
		assert.Equal(t, first, again, "same query must truncate the same way every call")
	}
}

func TestUnitSearchUsersInCacheMatchesEveryFieldCaseInsensitively(t *testing.T) {
	users := []slack.User{
		{ID: "U1", Name: "literal[.*", RealName: "Jörg Example", Profile: slack.UserProfile{DisplayName: "Display Name", Email: "PERSON@EXAMPLE.COM"}},
		{ID: "U2", Name: "deleted-match", Deleted: true},
	}
	ap := newTestApiProvider(nil, newUsersCache(users))
	ap.usersReady.Store(true)

	for _, query := range []string{"literal[.*", "JÖRG", "display name", "person@example.com"} {
		results, err := ap.searchUsersInCache(query, 10)
		require.NoError(t, err)
		require.Len(t, results, 1, query)
		assert.Equal(t, "U1", results[0].ID)
	}
	results, err := ap.searchUsersInCache("deleted-match", 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	folded := newTestApiProvider(nil, newUsersCache([]slack.User{{ID: "U3", Name: "ſymbol Kelvin"}}))
	folded.usersReady.Store(true)
	for _, query := range []string{"symbol", "kelvin"} {
		results, err := folded.searchUsersInCache(query, 10)
		require.NoError(t, err)
		require.Len(t, results, 1, query)
	}
}

func BenchmarkSearchUsersInCache(b *testing.B) {
	users := make([]slack.User, 2000)
	for i := range users {
		users[i] = slack.User{
			ID:       fmt.Sprintf("U%08d", i),
			Name:     fmt.Sprintf("person-%04d", i),
			RealName: fmt.Sprintf("Person Number %04d", i),
			Profile: slack.UserProfile{
				DisplayName: fmt.Sprintf("Teammate %04d", i),
				Email:       fmt.Sprintf("person-%04d@example.com", i),
			},
		}
	}
	ap := newTestApiProvider(nil, newUsersCache(users))
	ap.usersReady.Store(true)

	for _, query := range []string{"person-1999", "PERSON NUMBER 1999", "example.com", "missing"} {
		b.Run(query, func(b *testing.B) {
			for b.Loop() {
				_, _ = ap.searchUsersInCache(query, 100)
			}
		})
	}
}

func TestUnitCloseStopsBackgroundRefresh(t *testing.T) {
	client := &hangingUsersClient{
		started: make(chan struct{}),
		sawDone: make(chan struct{}),
	}
	ap := newTestApiProvider(client, emptyUsersCache())
	ap.ctx, ap.cancel = context.WithCancel(context.Background())
	ap.backgroundRefreshTimeout = time.Hour

	spawnBackgroundUsersRefresh(ap)
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "background refresh never started")
	}

	ap.Close()
	select {
	case <-client.sawDone:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Close did not cancel the background refresh")
	}
	require.Eventually(t, func() bool { return !ap.usersFlight.inFlight() }, 2*time.Second, 5*time.Millisecond)
	ap.Close() // idempotent
}

func TestUnitManagedOAuthRefreshLoopStopsOnCancel(t *testing.T) {
	c := &MCPSlackClient{logger: zap.NewNop()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runManagedOAuthRefresh(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "runManagedOAuthRefresh did not return after cancel")
	}
}

func TestUnitBrowserCallWithoutBrowserSession(t *testing.T) {
	c := &MCPSlackClient{logger: zap.NewNop(), isOAuth: true}
	c.initBrowserState()
	require.False(t, c.browserFeaturesAvailable())

	called := false
	_, err := browserCall(c, "activity.feed", func() (int, error) {
		called = true
		return 1, nil
	})
	require.ErrorIs(t, err, ErrBrowserSessionUnavailable)
	assert.Contains(t, err.Error(), "activity.feed")
	assert.False(t, called, "a degraded session must not reach the edge client")

	_, err = c.ActivityFeed(context.Background(), 10)
	assert.ErrorIs(t, err, ErrBrowserSessionUnavailable)
}

func TestUnitRefreshFlightReleasesAfterPanic(t *testing.T) {
	f := &refreshFlight{}
	func() {
		defer func() { _ = recover() }()
		_ = f.do(context.Background(), func(context.Context) error { panic("boom") })
	}()
	require.False(t, f.inFlight(), "a recovered panic must not leave the flight open")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, f.do(ctx, func(context.Context) error { return nil }), "next caller becomes leader")
}
