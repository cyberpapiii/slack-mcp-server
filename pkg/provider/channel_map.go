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

// Conversation categories, as conversations_unreads and channels_starred
// report them.
const (
	CategoryDM       = "dm"
	CategoryGroupDM  = "group_dm"
	CategoryPartner  = "partner"
	CategoryInternal = "internal"
)

// Categories lists every category in the order conversations_unreads ranks
// them.
var Categories = []string{CategoryDM, CategoryGroupDM, CategoryPartner, CategoryInternal}

// Category says who a conversation is with: one person, a group DM, an
// external organization (Slack Connect), or the workspace.
func (c Channel) Category() string {
	switch {
	case c.IsIM:
		return CategoryDM
	case c.IsMpIM:
		return CategoryGroupDM
	case c.IsExtShared:
		return CategoryPartner
	}
	return CategoryInternal
}

// SlackType is the conversation type as Slack's types parameter names it.
func (c Channel) SlackType() string {
	switch {
	case c.IsIM:
		return IMChanType
	case c.IsMpIM:
		return MpIMChanType
	case c.IsPrivate:
		return PrivateChanType
	}
	return PubChanType
}

func MapChannelFromSlack(c slack.Channel, usersMap map[string]slack.User) Channel {
	ch := Channel{
		ID:          c.ID,
		Name:        c.Name,
		Topic:       c.Topic.Value,
		Purpose:     c.Purpose.Value,
		MemberCount: c.NumMembers,
		IsIM:        c.IsIM,
		IsMpIM:      c.IsMpIM,
		IsPrivate:   c.IsPrivate,
		IsExtShared: c.IsExtShared,
		Members:     c.Members,
	}
	switch {
	case c.IsIM:
		ch.User = c.User
		return withIMPeer(ch, usersMap)
	case c.IsMpIM:
		if len(c.Members) > 0 {
			userNames := make([]string, 0, len(c.Members))
			for _, uid := range c.Members {
				if u, ok := usersMap[uid]; ok {
					userNames = append(userNames, u.RealName)
				} else {
					userNames = append(userNames, uid)
				}
			}
			ch.Name = "@" + c.NameNormalized
			ch.Purpose = "Group DM with " + strings.Join(userNames, ", ")
			ch.Topic = ""
			ch.MemberCount = len(c.Members)
		}
	default:
		ch.Name = "#" + c.NameNormalized
	}
	return ch
}

// withIMPeer names a DM after its peer as the users snapshot knows them.
func withIMPeer(ch Channel, usersMap map[string]slack.User) Channel {
	ch.MemberCount = 2
	ch.Topic = ""
	// Some payloads omit User; recover peer ID from members when present.
	if ch.User == "" {
		for _, memberID := range ch.Members {
			if _, ok := usersMap[memberID]; ok {
				ch.User = memberID
				break
			}
		}
	}
	if u, ok := usersMap[ch.User]; ok {
		ch.Name = "@" + u.Name
		ch.Purpose = "DM with " + u.RealName
	} else {
		ch.Name = "@" + ch.User
		ch.Purpose = "DM with " + ch.User
	}
	return ch
}
