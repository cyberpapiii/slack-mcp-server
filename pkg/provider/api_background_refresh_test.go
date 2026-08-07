package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangingUsersClient models slack-go's unbounded rate-limit retry loop: its
// users fetch never returns on its own, so the only way out is the context.
type hangingUsersClient struct {
	SlackAPI // embed interface to satisfy all methods; only override what we need

	started chan struct{}
	sawDone chan struct{}
}

func (c *hangingUsersClient) GetUsersContext(ctx context.Context, _ ...slack.GetUsersOption) ([]slack.User, error) {
	close(c.started)
	<-ctx.Done()
	close(c.sawDone)
	return nil, ctx.Err()
}

// gatedUsersClient blocks each users fetch until the test releases it, so the
// in-flight window is controlled by the test rather than by a timeout.
type gatedUsersClient struct {
	SlackAPI

	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (c *gatedUsersClient) GetUsersContext(ctx context.Context, _ ...slack.GetUsersOption) ([]slack.User, error) {
	c.calls.Add(1)
	c.entered <- struct{}{}
	select {
	case <-c.release:
		return nil, errors.New("released by test")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func usersRefreshInFlight(ap *ApiProvider) bool {
	ap.usersRefreshMu.Lock()
	defer ap.usersRefreshMu.Unlock()
	return ap.usersRefresh != nil
}

// Background users refresh cancels at deadline and can spawn again after.
func TestUnitBackgroundRefresh(t *testing.T) {
	t.Run("background users refresh gives up at the timeout", func(t *testing.T) {
		client := &hangingUsersClient{
			started: make(chan struct{}),
			sawDone: make(chan struct{}),
		}
		ap := newTestApiProvider(client, emptyUsersCache())
		ap.backgroundRefreshTimeout = 50 * time.Millisecond

		ap.spawnBackgroundUsersRefresh()

		select {
		case <-client.started:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "background refresh never entered GetUsersContext")
		}

		select {
		case <-client.sawDone:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "background refresh context was never cancelled; the fetch has no deadline")
		}

		require.Eventually(t, func() bool {
			return !usersRefreshInFlight(ap)
		}, 2*time.Second, 5*time.Millisecond, "users refresh call was never released")
	})

	t.Run("a second spawn is skipped while one is in flight, and allowed after", func(t *testing.T) {
		client := &gatedUsersClient{
			entered: make(chan struct{}, 4),
			release: make(chan struct{}),
		}
		ap := newTestApiProvider(client, emptyUsersCache())
		// Long enough that the deadline never fires; `release` ends the fetch.
		ap.backgroundRefreshTimeout = 30 * time.Second

		ap.spawnBackgroundUsersRefresh()

		select {
		case <-client.entered:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "first background refresh never entered GetUsersContext")
		}
		require.True(t, usersRefreshInFlight(ap), "first refresh should still be in flight")

		// A second spawn while the first is in flight must be suppressed.
		ap.spawnBackgroundUsersRefresh()
		select {
		case <-client.entered:
			require.FailNow(t, "second spawn ran while a refresh was already in flight")
		case <-time.After(50 * time.Millisecond):
		}
		assert.Equal(t, int32(1), client.calls.Load())

		// Let the first refresh finish and confirm the flag clears.
		close(client.release)
		require.Eventually(t, func() bool {
			return !usersRefreshInFlight(ap)
		}, 2*time.Second, 5*time.Millisecond, "users refresh call was never released")

		// A fresh spawn must now be allowed: this is the recovery half.
		ap.spawnBackgroundUsersRefresh()
		select {
		case <-client.entered:
		case <-time.After(2 * time.Second):
			require.FailNow(t, "a later spawn was still suppressed after the first refresh finished")
		}
		assert.Equal(t, int32(2), client.calls.Load())
	})
}
