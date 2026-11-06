package provider

import (
	"encoding/json"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge/fasttime"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitUnreadCountsFromEdge(t *testing.T) {
	i, err := fasttime.TS2int("1700000100.000200")
	require.NoError(t, err)
	counts := unreadCountsFromEdge(edge.ClientCountsResponse{
		Channels: []edge.ChannelSnapshot{
			{ID: "C1", HasUnreads: true, MentionCount: 2, LastRead: fasttime.Time(fasttime.Int2Time(i))},
			{ID: "C2", HasUnreads: true},
		},
	})
	require.Len(t, counts.Channels, 2)
	assert.Equal(t, ConversationUnread{ID: "C1", HasUnreads: true, MentionCount: 2, LastRead: "1700000100.000200"}, counts.Channels[0])
	assert.Equal(t, "", counts.Channels[1].LastRead, "a never-read conversation has no last-read bound, not year 1")
}

func TestUnitActivityFromEdgeDropsItemsThatNameNothing(t *testing.T) {
	var feed edge.ActivityFeedResponse
	require.NoError(t, json.Unmarshal([]byte(`{"items":[
		{"is_unread":true,"feed_ts":"1.1","key":"k1","item":{"type":"thread_v2","bundle_info":{"payload":{"thread_entry":{"channel_id":"C1","thread_ts":"9.0","unread_msg_count":3,"min_unread_ts":"9.5"}}}}},
		{"is_unread":true,"feed_ts":"1.2","key":"k2","item":{"type":"at_user","message":{"ts":"8.0","channel":"C2"}}},
		{"is_unread":true,"feed_ts":"1.3","key":"k3","item":{"type":"thread_v2"}},
		{"is_unread":false,"feed_ts":"1.4","key":"k4","item":{"type":"at_user"}}
	]}`), &feed))
	assert.Equal(t, []ActivityEntry{
		{Unread: true, Type: "thread_v2", FeedTs: "1.1", Key: "k1", ChannelID: "C1", ThreadTs: "9.0", MinUnreadTs: "9.5", UnreadCount: 3},
		{Unread: true, Type: "at_user", FeedTs: "1.2", Key: "k2", ChannelID: "C2", MinUnreadTs: "8.0", UnreadCount: 1},
	}, activityFromEdge(feed))
}

func TestUnitSavedRefChannel(t *testing.T) {
	assert.Equal(t, "C1", SavedRef{Type: SavedMessage, ID: "C1", Ts: "1.0"}.Channel())
	assert.Equal(t, "C1", SavedRef{ID: "C1", Ts: "1.0"}.Channel(), "an untyped item is a message")
	assert.Equal(t, "", SavedRef{Type: SavedReminder, ID: "Sa1"}.Channel())
	assert.Equal(t, "", SavedRef{Type: SavedFile, ID: "F1"}.Channel())
}

func TestUnitSavedAddBodyRoundTripsAsRichText(t *testing.T) {
	params, err := SavedAdd{SavedRef: SavedRef{Type: SavedReminder}, Text: "Call Sam", TodoState: "to_do"}.edgeParams()
	require.NoError(t, err)
	item := savedItemFromEdge(edge.SavedItem{ItemID: "Sa1", ItemType: SavedReminder, Description: json.RawMessage(params.Description)})
	require.Len(t, item.Description.BlockSet, 1)
	block, ok := item.Description.BlockSet[0].(*slack.RichTextBlock)
	require.True(t, ok)
	section := block.Elements[0].(*slack.RichTextSection)
	assert.Equal(t, "Call Sam", section.Elements[0].(*slack.RichTextSectionTextElement).Text)
}

func TestUnitSavedItemFromEdgeIgnoresAnUnreadableDescription(t *testing.T) {
	item := savedItemFromEdge(edge.SavedItem{ItemID: "Sa1", ItemType: SavedReminder, Description: json.RawMessage(`{"not":"blocks"`)})
	assert.Empty(t, item.Description.BlockSet)
}
