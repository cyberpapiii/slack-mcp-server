package provider

import (
	"context"

	"github.com/slack-go/slack"
)

// JoinConversation joins a channel and caches it as Slack returns it.
func (ap *ApiProvider) JoinConversation(ctx context.Context, channelID string) (*slack.Channel, error) {
	channel, _, _, err := ap.WebAPI().JoinConversationContext(ctx, channelID)
	if err != nil {
		return nil, err
	}
	ap.UpsertChannel(channel)
	return channel, nil
}

// LeaveConversation leaves a conversation. A private channel or group DM
// leaves the cache with it: conversations.list stops returning it.
func (ap *ApiProvider) LeaveConversation(ctx context.Context, channelID string) (notInChannel bool, err error) {
	notInChannel, err = ap.api.LeaveConversationContext(ctx, channelID)
	if err != nil {
		return false, err
	}
	if ch, ok := ap.currentChannels().Channels[channelID]; ok && (ch.IsPrivate || ch.IsMpIM) {
		ap.forgetChannel(channelID)
	}
	return notInChannel, nil
}

// OpenConversation opens a DM or group DM and caches it, so it resolves by
// ID and name at once.
func (ap *ApiProvider) OpenConversation(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, error) {
	channel, _, _, err := ap.WebAPI().OpenConversationContext(ctx, params)
	if err != nil {
		return nil, err
	}
	ap.UpsertChannel(channel)
	return channel, nil
}
