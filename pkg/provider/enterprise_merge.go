package provider

import (
	"context"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

func (c *MCPSlackClient) LeaveConversationContext(ctx context.Context, channelID string) (bool, error) {
	if c.isEnterprise && c.mode == ModeBrowser {
		// Edge webclient path bypasses enterprise_is_restricted on session tokens.
		notInChannel, err := browserCall(c, "conversations.leave", func() (bool, error) {
			return c.edgeClient.Load().LeaveConversation(ctx, channelID)
		})
		if err == nil {
			return notInChannel, nil
		}
		c.logger.Debug("Edge conversations.leave failed; using the Web API", zap.Error(err))
	}
	return c.standardSlackClient().LeaveConversationContext(ctx, channelID)
}

type conversationsPage struct {
	channels []slack.Channel
	cursor   string
}

// GetConversationsContext lists conversations, each conversations.list page
// paced and retried. On an Enterprise browser session one call merges the
// edge list with every standard page and returns no cursor.
func (c *MCPSlackClient) GetConversationsContext(ctx context.Context, params *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	if !c.isEnterprise || c.mode != ModeBrowser {
		return c.conversationsPage(ctx, params)
	}
	// Edge alone is partial (issue #73), so it is merged with the fully
	// paginated standard API.
	edgeChannels, edgeErr := browserCall(c, "conversation list", func() ([]slack.Channel, error) {
		return c.edgeClient.Load().GetConversationsContext(ctx)
	})
	if edgeErr != nil {
		c.logger.Debug("Edge channel list failed; using the Web API alone", zap.Error(edgeErr))
		return c.conversationsPage(ctx, params)
	}
	var channels []slack.Channel
	for _, ec := range edgeChannels {
		if params.ExcludeArchived && ec.IsArchived {
			continue
		}
		channels = append(channels, ec)
	}
	return mergeStandardConversationPages(channels, params, func(p *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
		return c.conversationsPage(ctx, p)
	})
}

func (c *MCPSlackClient) conversationsPage(ctx context.Context, params *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	page, err := limiter.Paced(ctx, c.pacing, "conversations.list", nil, func() (conversationsPage, error) {
		channels, cursor, err := c.standardSlackClient().GetConversationsContext(ctx, params)
		return conversationsPage{channels, cursor}, err
	})
	return page.channels, page.cursor, err
}

// mergeStandardConversationPages appends every standard page for the
// caller's params to channels, skipping IDs already present.
func mergeStandardConversationPages(
	channels []slack.Channel,
	params *slack.GetConversationsParameters,
	fetchStd func(*slack.GetConversationsParameters) ([]slack.Channel, string, error),
) ([]slack.Channel, string, error) {
	seen := make(map[string]struct{}, len(channels))
	for _, ch := range channels {
		seen[ch.ID] = struct{}{}
	}

	stdParams := *params
	for {
		stdChannels, nextCur, stdErr := fetchStd(&stdParams)
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
