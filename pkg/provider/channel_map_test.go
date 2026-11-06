package provider

import (
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
)

func TestUnitMappedNamesNeverAreABareSigil(t *testing.T) {
	users := map[string]slack.User{"U1": {ID: "U1", Name: "alice", RealName: "Alice"}}
	mpim := func(members ...string) slack.Channel {
		c := slack.Channel{IsMpIM: true, Members: members}
		c.ID, c.NameNormalized = "G1", "mpdm-alice--bob-1"
		return c
	}
	assert.Equal(t, "@mpdm-alice--bob-1", MapChannelFromSlack(mpim("U1"), users).Name)
	assert.Equal(t, "@mpdm-alice--bob-1", MapChannelFromSlack(mpim(), users).Name, "a group DM without members gets the same prefix")

	nameless := slack.Channel{}
	nameless.ID = "C1"
	assert.Equal(t, "", MapChannelFromSlack(nameless, users).Name)

	im := slack.Channel{}
	im.ID, im.IsIM = "D1", true
	assert.Equal(t, "", MapChannelFromSlack(im, users).Name, "a DM with no known peer has no name")
	im.User = "U1"
	assert.Equal(t, "@alice", MapChannelFromSlack(im, users).Name)
	im.User = "U9"
	assert.Equal(t, "@U9", MapChannelFromSlack(im, users).Name)
}

func TestUnitChannelCategoryAndSlackType(t *testing.T) {
	tests := []struct {
		name      string
		channel   Channel
		category  string
		slackType string
	}{
		{"public channel", Channel{ID: "C123"}, CategoryInternal, PubChanType},
		{"private channel", Channel{ID: "C456", IsPrivate: true}, CategoryInternal, PrivateChanType},
		{"DM", Channel{ID: "D123", IsIM: true}, CategoryDM, IMChanType},
		{"group DM", Channel{ID: "G123", IsMpIM: true, IsPrivate: true}, CategoryGroupDM, MpIMChanType},
		{"Slack Connect channel", Channel{ID: "C789", IsExtShared: true}, CategoryPartner, PubChanType},
		{"ext-shared beats private", Channel{ID: "C999", IsPrivate: true, IsExtShared: true}, CategoryPartner, PrivateChanType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.category, tt.channel.Category())
			assert.Equal(t, tt.slackType, tt.channel.SlackType())
		})
	}
}
