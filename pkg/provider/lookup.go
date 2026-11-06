package provider

import (
	"errors"
	"strings"
	"sync"

	"github.com/slack-go/slack"
)

// IDKind is what a Slack ID names, read from its prefix.
type IDKind int

const (
	IDUnknown IDKind = iota
	// IDUser is a user: U, or W on Enterprise Grid.
	IDUser
	// IDChannel is a public or private channel (C).
	IDChannel
	// IDGroup is a legacy private channel or a group DM (G). Slack issues
	// both from the same prefix, so only the cache can say which one it is.
	IDGroup
	// IDDirectMessage is a one-to-one DM (D).
	IDDirectMessage
	// IDFile is a file or canvas (F).
	IDFile
	// IDReminder is a custom to-do (Sa).
	IDReminder
)

// ClassifyID reports what a Slack ID names. Slack IDs are an uppercase prefix
// followed by uppercase letters and digits; anything else, including a
// username that happens to start with U, is IDUnknown. A custom to-do ID is
// "Sa" and then letters and digits.
func ClassifyID(id string) IDKind {
	if strings.HasPrefix(id, "Sa") {
		if len(id) > 2 && isAlnum(id[2:]) {
			return IDReminder
		}
		return IDUnknown
	}
	if len(id) < 2 || !isIDBody(id[1:]) {
		return IDUnknown
	}
	switch id[0] {
	case 'U', 'W':
		return IDUser
	case 'C':
		return IDChannel
	case 'G':
		return IDGroup
	case 'D':
		return IDDirectMessage
	case 'F':
		return IDFile
	}
	return IDUnknown
}

func isIDBody(s string) bool {
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return s != ""
}

func isAlnum(s string) bool {
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// ParseChannelRef splits a channel reference into the ID it names directly
// (a conversation ID or a "<#C123|name>" mention) or the cache name it has to
// be looked up by ("#name", "@user"). Exactly one of the results is set for
// a non-empty reference; anything that is neither is returned as id, so an
// unrecognised value still reaches Slack and gets Slack's answer.
func ParseChannelRef(ref string) (id, name string) {
	ref = strings.TrimSpace(ref)
	if mention, ok := mentionID(ref, "<#"); ok {
		return mention, ""
	}
	if strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "@") {
		return "", ref
	}
	return ref, ""
}

// ParseUserRef splits a user reference into a user ID (an ID or a
// "<@U123|name>" mention, with or without a leading "@") or a username /
// email to look up, with any leading "@" dropped.
func ParseUserRef(ref string) (id, name string) {
	ref = strings.TrimSpace(ref)
	if mention, ok := mentionID(ref, "<@"); ok {
		return mention, ""
	}
	ref = strings.TrimPrefix(ref, "@")
	if ClassifyID(ref) == IDUser {
		return ref, ""
	}
	return "", ref
}

// mentionID reads the ID out of a Slack mention such as "<@U123>" or
// "<#C123|general>".
func mentionID(ref, open string) (string, bool) {
	if !strings.HasPrefix(ref, open) || !strings.HasSuffix(ref, ">") {
		return "", false
	}
	body := ref[len(open) : len(ref)-1]
	if i := strings.IndexByte(body, '|'); i >= 0 {
		body = body[:i]
	}
	if body == "" {
		return "", false
	}
	return body, true
}

// ErrNotCached means a warm cache does not know the reference.
var ErrNotCached = errors.New("not in the cache")

// Directory answers name and ID lookups against one pair of cache snapshots.
// It never fetches. A miss is ErrUsersNotReady or ErrChannelsNotReady while
// the cache that would know the reference is still warming, ErrNotCached
// once it is warm.
type Directory struct {
	users         *UsersCache
	channels      *ChannelsCache
	usersReady    bool
	channelsReady bool

	dmOnce sync.Once
	dms    map[string]string
}

// NewDirectory builds a Directory over the given snapshots, treated as warm;
// nil means empty.
func NewDirectory(users *UsersCache, channels *ChannelsCache) *Directory {
	if users == nil {
		users = newUsersCache(nil)
	}
	if channels == nil {
		channels = newChannelsCache(0)
	}
	return &Directory{users: users, channels: channels, usersReady: true, channelsReady: true}
}

// Directory returns lookups over the current users and channels snapshots.
// A user installed by PatchUser is visible to the next call.
func (ap *ApiProvider) Directory() *Directory {
	d := NewDirectory(ap.usersSnapshot.Load(), ap.channelsSnapshot.Load())
	d.usersReady, d.channelsReady = ap.usersReady.Load(), ap.channelsReady.Load()
	return d
}

func (ap *ApiProvider) ChannelByRef(ref string) (Channel, error) {
	return ap.Directory().ChannelByRef(ref)
}

func (ap *ApiProvider) UserByRef(ref string) (slack.User, error) {
	return ap.Directory().UserByRef(ref)
}

func (ap *ApiProvider) ChannelLabel(id string) string { return ap.Directory().ChannelLabel(id) }

func (ap *ApiProvider) UserLabel(id string) string { return ap.Directory().UserLabel(id) }

// ChannelByRef finds a cached conversation by ID, "#name", "@user" (the DM
// with that user, by username or user ID) or "<#C123|name>" mention.
func (d *Directory) ChannelByRef(ref string) (Channel, error) {
	id, name := ParseChannelRef(ref)
	if id == "" {
		if cached, ok := d.channels.ChannelsInv[name]; ok {
			id = cached
		} else if strings.HasPrefix(name, "@") {
			user, err := d.UserByRef(name)
			if errors.Is(err, ErrUsersNotReady) {
				return Channel{}, err
			}
			if err == nil {
				if ch, ok := d.DMChannelFor(user.ID); ok {
					return ch, nil
				}
			}
		}
	}
	if ch, ok := d.channels.Channels[id]; ok {
		return ch, nil
	}
	if !d.channelsReady {
		return Channel{}, ErrChannelsNotReady
	}
	return Channel{}, ErrNotCached
}

func (d *Directory) User(id string) (slack.User, bool) {
	u, ok := d.users.Users[id]
	return u, ok
}

// UserByRef finds a cached user by ID, "@name" or bare username, "<@U123>"
// mention, or email address.
func (d *Directory) UserByRef(ref string) (slack.User, error) {
	if u, ok := d.userByRef(ref); ok {
		return u, nil
	}
	if !d.usersReady {
		return slack.User{}, ErrUsersNotReady
	}
	return slack.User{}, ErrNotCached
}

func (d *Directory) userByRef(ref string) (slack.User, bool) {
	id, name := ParseUserRef(ref)
	if id != "" {
		u, ok := d.users.Users[id]
		return u, ok
	}
	if name == "" {
		return slack.User{}, false
	}
	if cached, ok := d.users.UsersInv[name]; ok {
		u, ok := d.users.Users[cached]
		return u, ok
	}
	if strings.Contains(name, "@") {
		for _, u := range d.users.Users {
			if u.Profile.Email != "" && strings.EqualFold(u.Profile.Email, name) {
				return u, true
			}
		}
	}
	return slack.User{}, false
}

// UserLabel is the name a person reads for a user: the real name, else the
// username, else "" when the cache does not know the ID.
func (d *Directory) UserLabel(id string) string {
	u, ok := d.users.Users[id]
	if !ok {
		return ""
	}
	if u.RealName != "" {
		return u.RealName
	}
	return u.Name
}

// ChannelLabel is the cached "#name" or "@user" label for a conversation, or
// "" when the cache cannot name it. A DM is labelled by its peer's username
// as the users cache knows it now, or the peer's ID.
func (d *Directory) ChannelLabel(id string) string {
	ch, ok := d.channels.Channels[id]
	if !ok {
		return ""
	}
	if ch.IsIM && ch.User != "" {
		return dmName(ch.User, d.users.Users)
	}
	return ch.Name
}

func (d *Directory) DMChannelFor(userID string) (Channel, bool) {
	d.dmOnce.Do(func() {
		d.dms = make(map[string]string)
		for id, ch := range d.channels.Channels {
			if ch.IsIM && ch.User != "" {
				d.dms[ch.User] = id
			}
		}
	})
	id, ok := d.dms[userID]
	if !ok {
		return Channel{}, false
	}
	return d.channels.Channels[id], true
}
