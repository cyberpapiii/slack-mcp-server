package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// channelsPage is one scripted response from GetConversationsContext. A page
// with a non-empty cursor makes the pagination loop ask for another one.
type channelsPage struct {
	channels []slack.Channel
	cursor   string
	err      error
}

// fakeChannelsClient replays a fixed script of conversations.list pages so a
// test can make page 1 succeed and page 2 fail, the mid-pagination failure
// that used to truncate the cache silently.
type fakeChannelsClient struct {
	SlackAPI // embed interface to satisfy all methods; only override what we need

	pages []channelsPage
	calls int
}

func (f *fakeChannelsClient) GetConversationsContext(_ context.Context, _ *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	// Past the end of the script, keep replaying the last page so a retry does
	// not panic; every test below asserts on f.calls to pin the real count.
	i := f.calls
	if i >= len(f.pages) {
		i = len(f.pages) - 1
	}
	f.calls++

	p := f.pages[i]
	if p.err != nil {
		return nil, "", p.err
	}
	return p.channels, p.cursor, nil
}

// testChannel builds a public slack.Channel that mapChannel renders as
// "#<name>".
func testChannel(id, name string) slack.Channel {
	var ch slack.Channel
	ch.ID = id
	ch.Name = name
	ch.NameNormalized = name
	return ch
}

// emptyUsersCache is the minimal users snapshot getChannelsMultiType needs,
// since it calls ProvideUsersMap to resolve DM names.
func emptyUsersCache() *UsersCache {
	return &UsersCache{
		Users:    map[string]slack.User{},
		UsersInv: map[string]string{},
	}
}

// Truncated channel pagination must error; must not replace a good snapshot.
func TestUnitChannelFetchPartialResult(t *testing.T) {
	t.Run("partial fetch returns an error", func(t *testing.T) {
		client := &fakeChannelsClient{pages: []channelsPage{
			{channels: []slack.Channel{testChannel("C001", "general")}, cursor: "cur1"},
			// A plain error is non-retryable, so CallWithRetry gives up
			// immediately and the subtest does not sleep.
			{err: errors.New("boom")},
		}}
		ap := newTestApiProvider(client, emptyUsersCache())

		chans, err := ap.getChannelsMultiType(context.Background(), AllChanTypes)

		require.Error(t, err, "an incomplete pagination must report an error")
		assert.Contains(t, err.Error(), "boom")
		// Partial data is deliberately returned alongside the error so the
		// caller can decide whether it is usable.
		require.Len(t, chans, 1)
		assert.Equal(t, "C001", chans[0].ID)
		assert.Equal(t, 2, client.calls)
	})

	t.Run("partial fetch does not replace the snapshot", func(t *testing.T) {
		// A known-good cache, as a healthy process would already hold.
		good := &ChannelsCache{
			Channels: map[string]Channel{
				"C001": {ID: "C001", Name: "#general"},
				"C002": {ID: "C002", Name: "#random"},
			},
			ChannelsInv: map[string]string{"#general": "C001", "#random": "C002"},
		}

		client := &fakeChannelsClient{pages: []channelsPage{
			{channels: []slack.Channel{testChannel("C001", "general")}, cursor: "cur1"},
			{err: errors.New("boom")},
		}}
		ap := newTestApiProvider(client, emptyUsersCache())
		ap.channelsSnapshot.Store(good)

		_, err := ap.GetChannels(context.Background(), AllChanTypes)
		require.Error(t, err)

		// The regression: the truncated one-channel result must not have
		// atomically replaced the good two-channel cache.
		snapshot := ap.ProvideChannelsMaps()
		require.NotNil(t, snapshot)
		assert.Len(t, snapshot.Channels, 2, "a failed fetch must leave the existing snapshot intact")
		assert.Equal(t, "#general", snapshot.Channels["C001"].Name)
		assert.Equal(t, "#random", snapshot.Channels["C002"].Name)
		assert.Equal(t, "C002", snapshot.ChannelsInv["#random"])
	})

	t.Run("complete fetch replaces the snapshot", func(t *testing.T) {
		client := &fakeChannelsClient{pages: []channelsPage{
			{channels: []slack.Channel{testChannel("C001", "general")}, cursor: "cur1"},
			{channels: []slack.Channel{testChannel("C002", "random")}, cursor: ""},
		}}
		ap := newTestApiProvider(client, emptyUsersCache())
		ap.channelsSnapshot.Store(&ChannelsCache{
			Channels:    map[string]Channel{"COLD": {ID: "COLD", Name: "#stale"}},
			ChannelsInv: map[string]string{"#stale": "COLD"},
		})

		chans, err := ap.GetChannels(context.Background(), AllChanTypes)

		require.NoError(t, err)
		require.Len(t, chans, 2)
		assert.Equal(t, 2, client.calls)

		snapshot := ap.ProvideChannelsMaps()
		require.NotNil(t, snapshot)
		assert.Len(t, snapshot.Channels, 2)
		assert.Equal(t, "#general", snapshot.Channels["C001"].Name)
		assert.Equal(t, "#random", snapshot.Channels["C002"].Name)
		assert.Equal(t, "C001", snapshot.ChannelsInv["#general"])
		_, stale := snapshot.Channels["COLD"]
		assert.False(t, stale, "a complete fetch should install the fresh snapshot")
	})

	t.Run("rate limited fetch retries then gives up", func(t *testing.T) {
		// RetryAfter is one millisecond so exercising the retry path stays
		// fast. The failure is scripted on the FIRST page deliberately: Tier2
		// has a burst of 3, so three attempts draw three instantly-available
		// tokens. A preceding successful page would push the last attempt past
		// the burst and make this subtest block for a real 3s refill.
		client := &fakeChannelsClient{pages: []channelsPage{
			{err: &slack.RateLimitedError{RetryAfter: time.Millisecond}},
		}}
		ap := newTestApiProvider(client, emptyUsersCache())

		chans, err := ap.getChannelsMultiType(context.Background(), AllChanTypes)

		require.Error(t, err)
		assert.Empty(t, chans)
		// Initial attempt plus two retries, per the maxRetries of 2.
		assert.Equal(t, 3, client.calls)
	})
}

func TestUnitSlackRetryAfter(t *testing.T) {
	t.Run("rate limit error yields its retry after", func(t *testing.T) {
		err := &slack.RateLimitedError{RetryAfter: 7 * time.Second}
		assert.Equal(t, 7*time.Second, slackRetryAfter(err))
	})

	t.Run("wrapped rate limit error yields its retry after", func(t *testing.T) {
		err := fmt.Errorf("fetching page: %w", &slack.RateLimitedError{RetryAfter: 2 * time.Second})
		assert.Equal(t, 2*time.Second, slackRetryAfter(err))
	})

	t.Run("unrelated error is not retryable", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), slackRetryAfter(errors.New("boom")))
	})
}
