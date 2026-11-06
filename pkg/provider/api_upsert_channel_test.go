package provider

import (
	"context"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertChannelAddsOpenedDMToSnapshot(t *testing.T) {
	users := newUsersCache([]slack.User{{ID: "U1", Name: "alice"}})
	ap := newTestApiProvider(&fakeChannelsClient{}, users)
	ap.channelsSnapshot.Store(&ChannelsCache{
		Channels:    map[string]Channel{"C001": {ID: "C001", Name: "#general"}},
		ChannelsInv: map[string]string{"#general": "C001"},
	})

	dm := &slack.Channel{}
	dm.ID = "D123"
	dm.IsIM = true
	dm.User = "U1"
	ap.UpsertChannel(dm)

	snapshot := ap.currentChannels()
	require.Len(t, snapshot.Channels, 2, "existing channels survive the upsert")
	assert.Equal(t, "#general", snapshot.Channels["C001"].Name)
	assert.Equal(t, "@alice", snapshot.Channels["D123"].Name)
	assert.Equal(t, "D123", snapshot.ChannelsInv["@alice"])

	ap.UpsertChannel(dm)
	assert.Len(t, ap.currentChannels().Channels, 2, "upsert is idempotent")

	ap.UpsertChannel(nil)
	ap.UpsertChannel(&slack.Channel{})
	assert.Len(t, ap.currentChannels().Channels, 2, "nil and blank channels are ignored")
}

type leaveClient struct{ SlackAPI }

func (leaveClient) LeaveConversationContext(context.Context, string) (bool, error) { return false, nil }

func TestUnitUpsertKeepsCountAndLeaveForgetsPrivateChannels(t *testing.T) {
	ap := newTestApiProvider(leaveClient{}, newUsersCache(nil))
	ap.channelsSnapshot.Store(&ChannelsCache{
		Channels: map[string]Channel{
			"C1": {ID: "C1", Name: "#old", MemberCount: 12},
			"G1": {ID: "G1", Name: "#secret", IsPrivate: true},
		},
		ChannelsInv: map[string]string{"#old": "C1", "#secret": "G1"},
	})

	renamed := &slack.Channel{}
	renamed.ID, renamed.NameNormalized = "C1", "new"
	ap.UpsertChannel(renamed)
	snapshot := ap.currentChannels()
	assert.Equal(t, 12, snapshot.Channels["C1"].MemberCount, "a write response omits num_members")
	assert.Equal(t, "C1", snapshot.ChannelsInv["#new"])
	assert.NotContains(t, snapshot.ChannelsInv, "#old")

	_, err := ap.LeaveConversation(context.Background(), "C1")
	require.NoError(t, err)
	_, err = ap.LeaveConversation(context.Background(), "G1")
	require.NoError(t, err)
	snapshot = ap.currentChannels()
	assert.Contains(t, snapshot.Channels, "C1", "a public channel is still listed")
	assert.NotContains(t, snapshot.Channels, "G1")
	assert.NotContains(t, snapshot.ChannelsInv, "#secret")
}
