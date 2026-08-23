package edge

import (
	"context"
	"encoding/json"
	"runtime/trace"

	"github.com/rusq/slack"
)

// conversations.* API

type conversationsGenericInfoForm struct {
	BaseRequest
	UpdatedChannels string `json:"updated_channels"`
	WebClientFields
}

type conversationsGenericInfoResponse struct {
	baseResponse
	Channels            []slack.Channel `json:"channels"`
	UnchangedChannelIDs []string        `json:"unchanged_channel_ids"`
}

func (cl *Client) conversationsGenericInfo(ctx context.Context, channelID ...string) ([]slack.Channel, error) {
	ctx, task := trace.NewTask(ctx, "ConversationsGenericInfo")
	defer task.End()
	trace.Logf(ctx, "params", "channelID=%v", channelID)

	if len(channelID) == 0 {
		return nil, nil
	}

	updChannel := make(map[string]int, len(channelID))
	for _, id := range channelID {
		updChannel[id] = 0
	}
	b, err := json.Marshal(updChannel)
	if err != nil {
		return nil, err
	}
	form := conversationsGenericInfoForm{
		BaseRequest: BaseRequest{
			Token: cl.token,
		},
		UpdatedChannels: string(b),
		WebClientFields: webclientReason("fallback:UnknownFetchManager"),
	}
	resp, err := cl.postForm(ctx, "conversations.genericInfo", values(form, true))
	if err != nil {
		return nil, err
	}
	var r conversationsGenericInfoResponse
	if err := cl.parseResponse(&r, resp); err != nil {
		return nil, err
	}
	if err := r.validate("conversations.genericInfo"); err != nil {
		return nil, err
	}
	return r.Channels, nil
}

type conversationsLeaveRequest struct {
	BaseRequest
	Channel string `json:"channel"`
}

type conversationsLeaveResponse struct {
	baseResponse
	NotInChannel bool `json:"not_in_channel,omitempty"`
}

func (cl *Client) LeaveConversation(ctx context.Context, channelID string) (bool, error) {
	ctx, task := trace.NewTask(ctx, "LeaveConversation")
	defer task.End()

	form := conversationsLeaveRequest{
		BaseRequest: BaseRequest{Token: cl.token},
		Channel:     channelID,
	}

	resp, err := cl.postForm(ctx, "conversations.leave", values(form, true))
	if err != nil {
		return false, err
	}

	r := &conversationsLeaveResponse{}
	if err := cl.parseResponse(r, resp); err != nil {
		return false, err
	}

	if err := r.validate("conversations.leave"); err != nil {
		return false, err
	}

	return r.NotInChannel, nil
}
