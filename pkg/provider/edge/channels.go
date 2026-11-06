package edge

import (
	"encoding/json"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge/fasttime"
	"github.com/slack-go/slack"
)

// Channel is a conversation as client.userBoot, im.list,
// search.modules.channels and conversations.genericInfo send it: slack-go's
// shape, except for the fields those endpoints send differently.
type Channel struct {
	slack.Channel
	// Latest is a timestamp in im.list and a message elsewhere; nothing here
	// reads it, so it is kept raw rather than failing the decode.
	Latest json.RawMessage `json:"latest,omitempty"`
	// MemberCount is search.modules.channels' name for num_members.
	MemberCount int `json:"member_count,omitempty"`
}

func (c Channel) slackChannel() slack.Channel {
	ch := c.Channel
	if ts, err := fasttime.TS2int(ch.LastRead); err == nil {
		ch.LastRead = fasttime.Int2TS(ts)
	}
	if c.MemberCount != 0 {
		ch.NumMembers = c.MemberCount
	}
	if len(ch.Members) == 0 && ch.User != "" {
		ch.Members = []string{ch.User}
	}
	if ch.IsIM && ch.User == "" {
		for _, member := range ch.Members {
			if member != "" {
				ch.User = member
				break
			}
		}
	}
	if ch.NumMembers == 0 {
		ch.NumMembers = len(ch.Members)
	}
	if !ch.IsChannel && !ch.IsPrivate && !ch.IsIM && !ch.IsMpIM {
		ch.IsChannel = true
	}
	return ch
}

func slackChannels(in []Channel) []slack.Channel {
	out := make([]slack.Channel, len(in))
	for i, c := range in {
		out[i] = c.slackChannel()
	}
	return out
}
