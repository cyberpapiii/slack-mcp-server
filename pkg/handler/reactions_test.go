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
	added, removed []string
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
