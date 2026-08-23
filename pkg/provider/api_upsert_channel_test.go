package provider

import (
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertChannelAddsOpenedDMToSnapshot(t *testing.T) {
	users := &UsersCache{
		Users:    map[string]slack.User{"U1": {ID: "U1", Name: "alice"}},
		UsersInv: map[string]string{"alice": "U1"},
	}
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

	snapshot := ap.ProvideChannelsMaps()
	require.Len(t, snapshot.Channels, 2, "existing channels survive the upsert")
	assert.Equal(t, "#general", snapshot.Channels["C001"].Name)
	assert.Equal(t, "@alice", snapshot.Channels["D123"].Name)
	assert.Equal(t, "D123", snapshot.ChannelsInv["@alice"])

	ap.UpsertChannel(dm)
	assert.Len(t, ap.ProvideChannelsMaps().Channels, 2, "upsert is idempotent")

	ap.UpsertChannel(nil)
	ap.UpsertChannel(&slack.Channel{})
	assert.Len(t, ap.ProvideChannelsMaps().Channels, 2, "nil and blank channels are ignored")
}
