package provider

import (
	"context"
	"errors"
	"time"

	"github.com/slack-go/slack"
)

// ScheduledClient is the narrow Slack API surface needed for scheduled-message
// lifecycle operations. Neither method retries mutations.
type ScheduledClient interface {
	GetScheduledMessagesContext(context.Context, *slack.GetScheduledMessagesParameters) ([]slack.ScheduledMessage, string, error)
	DeleteScheduledMessageContext(context.Context, *slack.DeleteScheduledMessageParameters) (bool, error)
}

type ScheduledListRequest struct {
	ChannelID string
	Cursor    string
	Limit     int
	Oldest    string
	Latest    string
}

type ScheduledMessage struct {
	ScheduledMessageID string    `json:"scheduled_message_id"`
	ChannelID          string    `json:"channel_id"`
	Text               string    `json:"text"`
	PostAt             time.Time `json:"post_at"`
}

type ScheduledPage struct {
	Messages   []ScheduledMessage
	NextCursor string
}

type ScheduledProvider struct{ client ScheduledClient }

func NewScheduledProvider(client ScheduledClient) *ScheduledProvider {
	return &ScheduledProvider{client: client}
}

func (ap *ApiProvider) Scheduled() (*ScheduledProvider, error) {
	client, ok := ap.client.(ScheduledClient)
	if !ok {
		return nil, errors.New("configured Slack client does not support scheduled messages")
	}
	return NewScheduledProvider(client), nil
}

func (p *ScheduledProvider) ListScheduled(ctx context.Context, request ScheduledListRequest) (ScheduledPage, error) {
	messages, cursor, err := p.client.GetScheduledMessagesContext(ctx, &slack.GetScheduledMessagesParameters{
		Channel: request.ChannelID,
		Cursor:  request.Cursor,
		Limit:   request.Limit,
		Oldest:  request.Oldest,
		Latest:  request.Latest,
	})
	if err != nil {
		return ScheduledPage{}, err
	}
	result := ScheduledPage{Messages: make([]ScheduledMessage, len(messages)), NextCursor: cursor}
	for i, message := range messages {
		result.Messages[i] = ScheduledMessage{
			ScheduledMessageID: message.ID,
			ChannelID:          message.Channel,
			Text:               message.Text,
			PostAt:             time.Unix(int64(message.PostAt), 0).UTC(),
		}
	}
	return result, nil
}

func (p *ScheduledProvider) CancelScheduled(ctx context.Context, channelID, scheduledMessageID string) error {
	ok, err := p.client.DeleteScheduledMessageContext(ctx, &slack.DeleteScheduledMessageParameters{
		Channel:            channelID,
		ScheduledMessageID: scheduledMessageID,
	})
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("Slack did not confirm scheduled-message cancellation")
	}
	return nil
}

func (c *MCPSlackClient) GetScheduledMessagesContext(ctx context.Context, params *slack.GetScheduledMessagesParameters) ([]slack.ScheduledMessage, string, error) {
	return c.standardSlackClient().GetScheduledMessagesContext(ctx, params)
}

func (c *MCPSlackClient) DeleteScheduledMessageContext(ctx context.Context, params *slack.DeleteScheduledMessageParameters) (bool, error) {
	return c.standardSlackClient().DeleteScheduledMessageContext(ctx, params)
}
