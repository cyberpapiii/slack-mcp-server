package provider

import (
	"context"
	"errors"
	"time"

	"github.com/slack-go/slack"
)

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

func (c *WebClient) ListScheduled(ctx context.Context, request ScheduledListRequest) (ScheduledPage, error) {
	messages, cursor, err := c.resolve().GetScheduledMessagesContext(ctx, &slack.GetScheduledMessagesParameters{
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

// CancelScheduled deletes a scheduled message once; it never retries.
func (c *WebClient) CancelScheduled(ctx context.Context, channelID, scheduledMessageID string) error {
	ok, err := c.resolve().DeleteScheduledMessageContext(ctx, &slack.DeleteScheduledMessageParameters{
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
