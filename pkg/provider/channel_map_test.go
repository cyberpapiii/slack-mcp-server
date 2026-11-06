package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
