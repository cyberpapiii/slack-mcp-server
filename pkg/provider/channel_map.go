package provider

import (
	"strings"

	"github.com/slack-go/slack"
)

type Channel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Topic       string   `json:"topic"`
	Purpose     string   `json:"purpose"`
	MemberCount int      `json:"memberCount"`
	IsMpIM      bool     `json:"mpim"`
	IsIM        bool     `json:"im"`
	IsPrivate   bool     `json:"private"`
	IsExtShared bool     `json:"is_ext_shared"`     // Shared with external organizations
	User        string   `json:"user,omitempty"`    // User ID for IM channels
	Members     []string `json:"members,omitempty"` // Member IDs for the channel
}

type ChannelsCache struct {
	Channels    map[string]Channel `json:"channels"`
	ChannelsInv map[string]string  `json:"channels_inv"`
}

func newChannelsCache(size int) *ChannelsCache {
	return &ChannelsCache{
		Channels:    make(map[string]Channel, size),
		ChannelsInv: make(map[string]string, size),
	}
}

// add stores ch and indexes it by name. An IM whose peer could not be resolved
// yet carries the bare "@" sigil; indexing that would make every such IM
// collide on one inverse key, so it stays reachable by ID only until the next
// refresh resolves it.
func (c *ChannelsCache) add(ch Channel) {
	c.Channels[ch.ID] = ch
	if ch.Name != "" && ch.Name != "@" && ch.Name != "#" {
		c.ChannelsInv[ch.Name] = ch.ID
	}
}

func mapChannel(
	id, name, nameNormalized, topic, purpose, user string,
	members []string,
	numMembers int,
	isIM, isMpIM, isPrivate, isExtShared bool,
	usersMap map[string]slack.User,
) Channel {
	channelName := name
	finalPurpose := purpose
	finalTopic := topic
	finalMemberCount := numMembers

	var userID string
	if isIM {
		finalMemberCount = 2
		userID = user

		// Some payloads omit User; recover peer ID from members when present.
		if userID == "" && len(members) > 0 {
			for _, memberID := range members {
				if _, ok := usersMap[memberID]; ok {
					userID = memberID
					break
				}
			}
		}

		if u, ok := usersMap[userID]; ok {
			channelName = "@" + u.Name
			finalPurpose = "DM with " + u.RealName
		} else if userID != "" {
			channelName = "@" + userID
			finalPurpose = "DM with " + userID
		} else {
			channelName = "@"
			finalPurpose = "DM with "
		}
		finalTopic = ""
	} else if isMpIM {
		if len(members) > 0 {
			finalMemberCount = len(members)
			var userNames []string
			for _, uid := range members {
				if u, ok := usersMap[uid]; ok {
					userNames = append(userNames, u.RealName)
				} else {
					userNames = append(userNames, uid)
				}
			}
			channelName = "@" + nameNormalized
			finalPurpose = "Group DM with " + strings.Join(userNames, ", ")
			finalTopic = ""
		}
	} else {
		channelName = "#" + nameNormalized
	}

	return Channel{
		ID:          id,
		Name:        channelName,
		Topic:       finalTopic,
		Purpose:     finalPurpose,
		MemberCount: finalMemberCount,
		IsIM:        isIM,
		IsMpIM:      isMpIM,
		IsPrivate:   isPrivate,
		IsExtShared: isExtShared,
		User:        userID,
		Members:     members,
	}
}

func MapChannelFromSlack(c slack.Channel, usersMap map[string]slack.User) Channel {
	return mapChannel(
		c.ID, c.Name, c.NameNormalized,
		c.Topic.Value, c.Purpose.Value,
		c.User, c.Members, c.NumMembers,
		c.IsIM, c.IsMpIM, c.IsPrivate, c.IsExtShared,
		usersMap,
	)
}
