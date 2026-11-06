package provider

import (
	"context"

	"github.com/slack-go/slack"
)

// usersConversationsMaxPage is the largest page users.conversations returns.
const usersConversationsMaxPage = 200

// ChannelsForUser lists up to limit unarchived conversations of the given
// types that the authenticated user is a member of, starting at cursor. It
// asks Slack only for the rows still needed, so the returned cursor resumes
// right after the last channel returned; "" means there are no more.
func (ap *ApiProvider) ChannelsForUser(ctx context.Context, types []string, cursor string, limit int) ([]Channel, string, error) {
	users := ap.Directory().users.Users
	var channels []Channel
	for {
		page, next, err := ap.WebAPI().GetConversationsForUserContext(ctx, &slack.GetConversationsForUserParameters{
			Types:           types,
			Limit:           min(limit-len(channels), usersConversationsMaxPage),
			Cursor:          cursor,
			ExcludeArchived: true,
		})
		if err != nil {
			return nil, "", err
		}
		for _, c := range page {
			channels = append(channels, MapChannelFromSlack(c, users))
		}
		// An empty page with a cursor would otherwise loop forever.
		if len(channels) >= limit || len(page) == 0 || next == "" {
			return channels[:min(len(channels), limit)], next, nil
		}
		cursor = next
	}
}
