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
	IsExtShared bool     `json:"is_ext_shared"`
	User        string   `json:"user,omitempty"` // User ID for IM channels
	Members     []string `json:"members,omitempty"`
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

// add stores ch and indexes it by name. A conversation with no name stays
// reachable by ID only.
func (c *ChannelsCache) add(ch Channel) {
	c.Channels[ch.ID] = ch
	if ch.Name != "" {
		c.ChannelsInv[ch.Name] = ch.ID
	}
}

// remove drops the conversation with this ID and its name index entry.
func (c *ChannelsCache) remove(id string) {
	ch, ok := c.Channels[id]
	if !ok {
		return
	}
	delete(c.Channels, id)
	if c.ChannelsInv[ch.Name] == id {
		delete(c.ChannelsInv, ch.Name)
	}
}

// withSigil is name behind its "#" or "@" sigil, or "" when there is no name,
// so no conversation is ever named by a bare sigil.
func withSigil(sigil, name string) string {
	if name == "" {
		return ""
	}
	return sigil + name
}

// dmName is a DM's label: "@" and the peer's username as the users snapshot
// knows it, else the peer's ID.
func dmName(peer string, users map[string]slack.User) string {
	if u, ok := users[peer]; ok && u.Name != "" {
		return "@" + u.Name
	}
	return withSigil("@", peer)
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
		ch.Name = withSigil("@", c.NameNormalized)
		if len(c.Members) > 0 {
			userNames := make([]string, 0, len(c.Members))
			for _, uid := range c.Members {
				if u, ok := usersMap[uid]; ok {
					userNames = append(userNames, u.RealName)
				} else {
					userNames = append(userNames, uid)
				}
			}
			ch.Purpose = "Group DM with " + strings.Join(userNames, ", ")
			ch.Topic = ""
			ch.MemberCount = len(c.Members)
		}
	default:
		ch.Name = withSigil("#", c.NameNormalized)
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
	ch.Name = dmName(ch.User, usersMap)
	if u, ok := usersMap[ch.User]; ok {
		ch.Purpose = "DM with " + u.RealName
	} else {
		ch.Purpose = "DM with " + ch.User
	}
	return ch
}
