package provider

import (
	"context"
	"errors"
	"testing"

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
// since it calls currentUsers to resolve DM names.
func emptyUsersCache() *UsersCache {
	return newUsersCache(nil)
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

		_, err := ap.fetchChannels(context.Background())
		require.Error(t, err)

		ap.channelsReady.Store(true)
		client.calls = 0
		err = ap.fetchAndStoreChannels(context.Background())
		require.ErrorContains(t, err, "boom", "a stale cache stays in service, but the refresh failure is not hidden")

		// The regression: the truncated one-channel result must not have
		// atomically replaced the good two-channel cache.
		snapshot := ap.currentChannels()
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

		chans, err := ap.fetchChannels(context.Background())

		require.NoError(t, err)
		require.Len(t, chans, 2)
		assert.Equal(t, 2, client.calls)

		snapshot := ap.currentChannels()
		require.NotNil(t, snapshot)
		assert.Len(t, snapshot.Channels, 2)
		assert.Equal(t, "#general", snapshot.Channels["C001"].Name)
		assert.Equal(t, "#random", snapshot.Channels["C002"].Name)
		assert.Equal(t, "C001", snapshot.ChannelsInv["#general"])
		_, stale := snapshot.Channels["COLD"]
		assert.False(t, stale, "a complete fetch should install the fresh snapshot")
	})
}

func TestUnitEnterpriseMergePropagatesStandardPaginationError(t *testing.T) {
	edge := []slack.Channel{testChannel("D001", "im")}
	calls := 0
	fetchStd := func(params *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
		calls++
		if params.Cursor == "" {
			return []slack.Channel{testChannel("C001", "general")}, "cur1", nil
		}
		return nil, "", errors.New("boom")
	}

	chans, cursor, err := mergeStandardConversationPages(edge, &slack.GetConversationsParameters{Limit: 999}, fetchStd)

	require.Error(t, err, "a failed conversations.list page must not look like a complete merge")
	assert.Contains(t, err.Error(), "boom")
	assert.Empty(t, cursor)
	require.Len(t, chans, 2, "partial merge is returned alongside the error")
	assert.Equal(t, "D001", chans[0].ID)
	assert.Equal(t, "C001", chans[1].ID)
	assert.Equal(t, 2, calls)
}

func TestUnitEnterpriseMergeCompletesWhenStandardPagesFinish(t *testing.T) {
	edge := []slack.Channel{testChannel("D001", "im")}
	fetchStd := func(params *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
		if params.Cursor == "" {
			return []slack.Channel{testChannel("C001", "general")}, "cur1", nil
		}
		return []slack.Channel{testChannel("C002", "random")}, "", nil
	}

	chans, cursor, err := mergeStandardConversationPages(edge, &slack.GetConversationsParameters{Limit: 999}, fetchStd)

	require.NoError(t, err)
	assert.Empty(t, cursor)
	require.Len(t, chans, 3)
	assert.Equal(t, []string{"D001", "C001", "C002"}, []string{chans[0].ID, chans[1].ID, chans[2].ID})
}
