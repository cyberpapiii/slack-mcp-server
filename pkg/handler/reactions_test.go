package handler

import (
	"context"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type reactionsAPI struct {
	ConversationsAPI
	added, removed   []string
	pinned, unpinned []string
	pinErr, unpinErr error
}

func (r *reactionsAPI) AddPinContext(_ context.Context, channel string, item slack.ItemRef) error {
	r.pinned = append(r.pinned, channel+"/"+item.Timestamp+item.Channel)
	return r.pinErr
}

func (r *reactionsAPI) RemovePinContext(_ context.Context, channel string, item slack.ItemRef) error {
	r.unpinned = append(r.unpinned, channel+"/"+item.Timestamp+item.Channel)
	return r.unpinErr
}

func (r *reactionsAPI) AddReactionContext(_ context.Context, name string, item slack.ItemRef) error {
	r.added = append(r.added, name+"@"+item.Channel+"/"+item.Timestamp)
	return nil
}

func (r *reactionsAPI) RemoveReactionContext(_ context.Context, name string, item slack.ItemRef) error {
	r.removed = append(r.removed, name+"@"+item.Channel+"/"+item.Timestamp)
	return nil
}

// Adding and removing a reaction answer the same shape.
func TestReactionsAddAndRemoveAnswerAlike(t *testing.T) {
	api := &reactionsAPI{}
	handler := NewConversationsHandler(api, (&fakeSlack{}).pipeline(), zap.NewNop(), envSettings(), servedTools())
	args := map[string]any{"channel_id": "C1", "timestamp": "1.000001", "emoji": ":wave:"}

	added, err := served(handler.ReactionsAddHandler(context.Background(), toolRequest(args)))
	require.NoError(t, err)
	removed, err := served(handler.ReactionsRemoveHandler(context.Background(), toolRequest(args)))
	require.NoError(t, err)

	assert.Equal(t, []string{"wave@C1/1.000001"}, api.added)
	assert.Equal(t, []string{"wave@C1/1.000001"}, api.removed)
	assert.Equal(t, "Successfully added :wave: reaction to message 1.000001 in channel C1", ResultText(added))
	addData := added.StructuredContent.(ToolResult[ActionData]).Data
	removeData := removed.StructuredContent.(ToolResult[ActionData]).Data
	assert.Equal(t, &ActionData{Action: "add_reaction", Status: "added", ChannelID: "C1", MessageID: "1.000001"}, addData)
	assert.Equal(t, "remove_reaction", removeData.Action)
}

// Pinning is idempotent both ways: Slack's already_pinned and no_pin mean the
// message is already in the state asked for.
func TestPinsAddAndRemoveAreIdempotent(t *testing.T) {
	api := &reactionsAPI{pinErr: slack.SlackErrorResponse{Err: "already_pinned"}, unpinErr: slack.SlackErrorResponse{Err: "no_pin"}}
	handler := NewConversationsHandler(api, (&fakeSlack{}).pipeline(), zap.NewNop(), envSettings(), servedTools())
	args := map[string]any{"channel_id": "C1", "timestamp": "1.000001"}

	pinned, err := served(handler.PinsAddHandler(context.Background(), toolRequest(args)))
	require.NoError(t, err)
	unpinned, err := served(handler.PinsRemoveHandler(context.Background(), toolRequest(args)))
	require.NoError(t, err)

	assert.Equal(t, []string{"C1/1.000001"}, api.pinned, "channel is the argument, not the item")
	assert.Equal(t, []string{"C1/1.000001"}, api.unpinned)
	assert.Equal(t, &ActionData{Action: "pin", Status: "pinned", ChannelID: "C1", MessageID: "1.000001"}, pinned.StructuredContent.(ToolResult[ActionData]).Data)
	assert.Equal(t, "unpinned", unpinned.StructuredContent.(ToolResult[ActionData]).Data.Status)

	api.pinErr = slack.SlackErrorResponse{Err: "message_not_found"}
	failed, err := served(handler.PinsAddHandler(context.Background(), toolRequest(args)))
	require.NoError(t, err)
	assert.True(t, failed.IsError)
	assert.Equal(t, "not_found", ErrorCode(failed))
}
