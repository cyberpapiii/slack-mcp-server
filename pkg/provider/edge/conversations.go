package edge

import (
	"context"
	"encoding/json"

	"github.com/slack-go/slack"
)

type conversationsGenericInfoForm struct {
	UpdatedChannels string `json:"updated_channels"`
	WebClientFields
}

type conversationsGenericInfoResponse struct {
	Channels []Channel `json:"channels"`
}

func (cl *Client) conversationsGenericInfo(ctx context.Context, channelID ...string) ([]slack.Channel, error) {
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
		UpdatedChannels: string(b),
		WebClientFields: webclientReason("fallback:UnknownFetchManager"),
	}
	var r conversationsGenericInfoResponse
	if err := cl.postForm(ctx, "conversations.genericInfo", form, &r); err != nil {
		return nil, err
	}
	return slackChannels(r.Channels), nil
}

type conversationsLeaveRequest struct {
	Channel string `json:"channel"`
}

type conversationsLeaveResponse struct {
	NotInChannel bool `json:"not_in_channel,omitempty"`
}

func (cl *Client) LeaveConversation(ctx context.Context, channelID string) (bool, error) {
	form := conversationsLeaveRequest{
		Channel: channelID,
	}

	var r conversationsLeaveResponse
	if err := cl.postForm(ctx, "conversations.leave", form, &r); err != nil {
		return false, err
	}
	return r.NotInChannel, nil
}
