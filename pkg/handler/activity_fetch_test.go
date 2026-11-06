package handler

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeActivity struct {
	feed    []provider.ActivityEntry
	replies map[string][]slack.Message // by thread ts
	mu      sync.Mutex
	calls   map[string]int // limit by thread ts
}

func (f *fakeActivity) ActivityFeed(context.Context, int) ([]provider.ActivityEntry, error) {
	return f.feed, nil
}
func (f *fakeActivity) ActivityMarkRead(context.Context, string, string, string) error {
	panic("unexpected")
}
func (f *fakeActivity) GetConversationRepliesContext(_ context.Context, params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[params.Timestamp] = params.Limit
	return f.replies[params.Timestamp], false, "", nil
}

func message(ts, text string) slack.Message {
	return slack.Message{Msg: slack.Msg{Timestamp: ts, Text: text, User: "U1"}}
}

// A top-level @mention is one message, not a thread: its row is the message
// it points at, even when that message has replies.
func TestActivityUnreadsIncludesTopLevelMentions(t *testing.T) {
	api := &fakeActivity{
		feed: []provider.ActivityEntry{
			{Unread: true, Type: "at_user", ChannelID: "C1", MinUnreadTs: "100.000001", FeedTs: "100.000001", Key: "k1"},
			{Unread: true, Type: "thread_v2", ChannelID: "C2", ThreadTs: "200.000001", MinUnreadTs: "200.000002", UnreadCount: 2, FeedTs: "200.000003", Key: "k2"},
		},
		replies: map[string][]slack.Message{
			"100.000001": {message("100.000001", "hey you"), message("100.000009", "a reply to the mention")},
			"200.000001": {message("200.000002", "first unread"), message("200.000003", "second unread")},
		},
	}
	handler := NewActivityHandler(api, (&fakeSlack{}).pipeline(), zap.NewNop(), envSettings())

	result, err := served(handler.ActivityUnreadsHandler(context.Background(), toolRequest(map[string]any{})))
	require.NoError(t, err)
	text := ResultText(result)
	assert.Contains(t, text, "hey you")
	assert.NotContains(t, text, "a reply to the mention")
	assert.Contains(t, text, "first unread")
	assert.Contains(t, text, "second unread")
	assert.False(t, strings.Contains(text, "#partial:"), text)
	assert.Equal(t, map[string]int{"100.000001": 1, "200.000001": defaultActivityMessagesPerThread}, api.calls)
}
