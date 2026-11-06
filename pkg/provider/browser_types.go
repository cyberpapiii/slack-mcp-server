package provider

import (
	"encoding/json"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/slack-go/slack"
)

// The browser-session answers (client.counts, activity.feed, saved.*) as the
// handlers read them. Edge's wire types stay inside the provider.

// UnreadCounts is client.counts: which conversations have unread messages.
type UnreadCounts struct {
	Channels []ConversationUnread
	MPIMs    []ConversationUnread
	IMs      []ConversationUnread
}

// ConversationUnread is one conversation's read state. LastRead is a Slack
// timestamp, "" when the conversation was never read.
type ConversationUnread struct {
	ID           string
	LastRead     string
	MentionCount int
	HasUnreads   bool
}

func unreadCountsFromEdge(r edge.ClientCountsResponse) UnreadCounts {
	convert := func(snaps []edge.ChannelSnapshot) []ConversationUnread {
		if snaps == nil {
			return nil
		}
		out := make([]ConversationUnread, len(snaps))
		for i, s := range snaps {
			out[i] = ConversationUnread{ID: s.ID, LastRead: s.LastRead.SlackString(), MentionCount: s.MentionCount, HasUnreads: s.HasUnreads}
		}
		return out
	}
	return UnreadCounts{Channels: convert(r.Channels), MPIMs: convert(r.MPIMs), IMs: convert(r.IMs)}
}

// ActivityEntry is one Activity feed item. MinUnreadTs is the oldest unread
// message (the mention itself, or the first unread reply of a thread);
// FeedTs, Key and Type are what activity.markRead takes.
type ActivityEntry struct {
	Unread      bool
	Type        string
	FeedTs      string
	Key         string
	ChannelID   string
	ThreadTs    string
	MinUnreadTs string
	UnreadCount int
}

// activityFromEdge flattens the feed. A thread item without its bundle, or a
// mention without its message, names nothing to read and is dropped.
func activityFromEdge(r edge.ActivityFeedResponse) []ActivityEntry {
	var out []ActivityEntry
	for _, fi := range r.Items {
		entry := ActivityEntry{Unread: fi.IsUnread, Type: fi.Item.Type, FeedTs: fi.FeedTs, Key: fi.Key}
		switch fi.Item.Type {
		case "thread_v2":
			if fi.Item.BundleInfo == nil {
				continue
			}
			te := fi.Item.BundleInfo.Payload.ThreadEntry
			entry.ChannelID, entry.ThreadTs, entry.UnreadCount, entry.MinUnreadTs = te.ChannelID, te.ThreadTs, te.UnreadMsgCount, te.MinUnreadTs
		default:
			if fi.Item.Message == nil {
				continue
			}
			m := fi.Item.Message
			entry.ChannelID, entry.ThreadTs, entry.UnreadCount, entry.MinUnreadTs = m.Channel, m.ThreadTs, 1, m.Ts
		}
		out = append(out, entry)
	}
	return out
}

// Saved item types. To-dos and Saved share one API; the list an item is on
// is its TodoState.
const (
	SavedMessage  = edge.SavedItemMessage
	SavedFile     = edge.SavedItemFile
	SavedReminder = edge.SavedItemReminder
)

// SavedRef names one saved item. Slack's item_id is the channel for a
// message (addressed by channel and timestamp), and the item's own ID for a
// custom to-do (Sa...) or a file (F...).
type SavedRef struct {
	Type string
	ID   string
	Ts   string
}

// IsMessage reports whether the item is a message; Slack leaves the type
// empty on some message items.
func (r SavedRef) IsMessage() bool { return r.Type == SavedMessage || r.Type == "" }

// Channel is the conversation a saved message is in, "" for anything else.
func (r SavedRef) Channel() string {
	if r.IsMessage() {
		return r.ID
	}
	return ""
}

// SavedItem is one entry on the To-dos or Saved list. Description is the
// rich-text body of a custom to-do.
type SavedItem struct {
	SavedRef
	DateCreated int64
	DateDue     int64
	State       string
	TodoState   string
	Description slack.Blocks
}

// SavedPage is one saved.list page; NextCursor is "" on the last page.
type SavedPage struct {
	Items      []SavedItem
	NextCursor string
}

func savedItemFromEdge(item edge.SavedItem) SavedItem {
	out := SavedItem{
		SavedRef:    SavedRef{Type: item.ItemType, ID: item.ItemID, Ts: item.Ts},
		DateCreated: item.DateCreated,
		DateDue:     item.DateDue,
		State:       item.State,
		TodoState:   item.TodoState,
	}
	var description slack.Blocks
	if len(item.Description) > 0 && json.Unmarshal(item.Description, &description) == nil {
		out.Description = description
	}
	return out
}

func savedPageFromEdge(r edge.SavedListResponse) SavedPage {
	page := SavedPage{NextCursor: r.ResponseMetadata.Cursor}
	if r.SavedItems != nil {
		page.Items = make([]SavedItem, len(r.SavedItems))
		for i, item := range r.SavedItems {
			page.Items[i] = savedItemFromEdge(item)
		}
	}
	return page
}

// SavedUpdate changes one item. DateDue is a string because "0" is a real
// value that clears the due date; "" leaves it alone.
type SavedUpdate struct {
	SavedRef
	Mark      string
	TodoState string
	DateDue   string
}

// SavedAdd creates an item: a message or file by reference, or a custom to-do
// whose body is Text.
type SavedAdd struct {
	SavedRef
	Text      string
	TodoState string
	DateDue   string
}

func (a SavedAdd) edgeParams() (edge.SavedAddParams, error) {
	params := edge.SavedAddParams{ItemType: a.Type, ItemID: a.ID, Ts: a.Ts, TodoState: a.TodoState, DateDue: a.DateDue}
	if a.Text != "" {
		description, err := edge.RichTextJSON(a.Text)
		if err != nil {
			return edge.SavedAddParams{}, err
		}
		params.Description = description
	}
	return params, nil
}
