package provider

import (
	"context"

	"github.com/slack-go/slack"
)

func (c *MCPSlackClient) LeaveConversationContext(ctx context.Context, channelID string) (bool, error) {
	if c.isEnterprise && !c.isOAuth {
		// Edge webclient path bypasses enterprise_is_restricted on session tokens.
		notInChannel, err := c.edgeClient.LeaveConversation(ctx, channelID)
		if err == nil {
			return notInChannel, nil
		}
	}
	return c.standardSlackClient().LeaveConversationContext(ctx, channelID)
}

func (c *MCPSlackClient) GetConversationsContext(ctx context.Context, params *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	std := c.standardSlackClient()
	// Enterprise + session: edge alone is partial (issue #73); merge with fully
	// paginated standard API and return empty cursor. OAuth / non-Enterprise: standard only.
	if c.isEnterprise {
		if c.isOAuth {
			return std.GetConversationsContext(ctx, params)
		}

		edgeChannels, _, edgeErr := c.edgeClient.GetConversationsContext(ctx, nil)
		if edgeErr != nil {
			return std.GetConversationsContext(ctx, params)
		}

		var channels []slack.Channel
		for _, ec := range edgeChannels {
			if params != nil && params.ExcludeArchived && ec.IsArchived {
				continue
			}
			channels = append(channels, slack.Channel{
				IsGeneral: ec.IsGeneral,
				GroupConversation: slack.GroupConversation{
					Conversation: slack.Conversation{
						ID:                 ec.ID,
						IsIM:               ec.IsIM,
						IsMpIM:             ec.IsMpIM,
						IsPrivate:          ec.IsPrivate,
						Created:            slack.JSONTime(ec.Created.Time().UnixMilli()),
						Unlinked:           ec.Unlinked,
						NameNormalized:     ec.NameNormalized,
						IsShared:           ec.IsShared,
						IsExtShared:        ec.IsExtShared,
						IsOrgShared:        ec.IsOrgShared,
						IsPendingExtShared: ec.IsPendingExtShared,
						NumMembers:         ec.NumMembers,
						User:               ec.User,
					},
					Name:       ec.Name,
					IsArchived: ec.IsArchived,
					Members:    ec.Members,
					Topic: slack.Topic{
						Value: ec.Topic.Value,
					},
					Purpose: slack.Purpose{
						Value: ec.Purpose.Value,
					},
				},
			})
		}

		return mergeStandardConversationPages(channels, params, func(p *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
			return std.GetConversationsContext(ctx, p)
		})
	}

	return std.GetConversationsContext(ctx, params)
}

func mergeStandardConversationPages(
	channels []slack.Channel,
	params *slack.GetConversationsParameters,
	fetchStd func(*slack.GetConversationsParameters) ([]slack.Channel, string, error),
) ([]slack.Channel, string, error) {
	seen := make(map[string]struct{}, len(channels))
	for _, ch := range channels {
		seen[ch.ID] = struct{}{}
	}

	stdParams := &slack.GetConversationsParameters{
		Limit:           999,
		ExcludeArchived: true,
	}
	if params != nil {
		stdParams.Types = params.Types
	}

	for {
		stdChannels, nextCur, stdErr := fetchStd(stdParams)
		if stdErr != nil {
			return channels, "", stdErr
		}
		for _, sc := range stdChannels {
			if _, ok := seen[sc.ID]; !ok {
				seen[sc.ID] = struct{}{}
				channels = append(channels, sc)
			}
		}
		if nextCur == "" || nextCur == stdParams.Cursor {
			return channels, "", nil
		}
		stdParams.Cursor = nextCur
	}
}
