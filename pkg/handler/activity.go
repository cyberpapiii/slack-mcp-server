package handler

import (
	"context"
	"fmt"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// ActivityItem is one unread activity-feed entry. MsgID is the oldest unread
// message (the mention itself, or the first unread reply of a thread);
// FeedTs, Key and Type are the activity_mark_read arguments.
type ActivityItem struct {
	Type        string `csv:"Type" json:"type"`
	ChannelID   string `csv:"Channel" json:"channel_id"`
	MinUnreadTs string `csv:"MsgID" json:"min_unread_ts,omitempty"`
	ThreadTs    string `csv:"ThreadTs" json:"thread_ts,omitempty"`
	UnreadCount int    `csv:"UnreadCount" json:"unread_count"`
	FeedTs      string `csv:"FeedTs" json:"feed_ts"`
	Key         string `csv:"Key" json:"key"`
}

const (
	// activityFeedMaxLimit is the largest page activity.feed accepts. Above it
	// Slack answers invalid_arguments instead of a shorter page.
	activityFeedMaxLimit = 50

	defaultActivityLimit             = 30
	defaultActivityMessagesPerThread = 10
)

type ActivityHandler struct {
	api      ActivityAPI
	messages MessagePipeline
	logger   *zap.Logger
	settings Settings
}

func NewActivityHandler(api ActivityAPI, messages MessagePipeline, logger *zap.Logger, settings Settings) *ActivityHandler {
	return &ActivityHandler{api: api, messages: messages, logger: logger, settings: settings}
}

func (item ActivityItem) channel() string { return item.ChannelID }

func (h *ActivityHandler) ActivityUnreadsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	includeMessages := request.GetBool("include_messages", true)
	maxMsgsPerThread := positiveInt(request, "max_messages_per_thread", defaultActivityMessagesPerThread)
	limit := pageLimit(request, defaultActivityLimit, activityFeedMaxLimit)

	mode, err := h.settings.outputMode(request)
	if err != nil {
		return nil, err
	}

	feed, err := h.api.ActivityFeed(ctx, limit)
	if err != nil {
		h.logger.Debug("ActivityFeed failed", zap.Error(err))
		return nil, fmt.Errorf("failed to get activity feed: %w", err)
	}

	var items []ActivityItem
	for _, entry := range feed {
		if !entry.Unread {
			continue
		}
		items = append(items, ActivityItem{
			Type:        entry.Type,
			ChannelID:   entry.ChannelID,
			MinUnreadTs: entry.MinUnreadTs,
			ThreadTs:    entry.ThreadTs,
			UnreadCount: entry.UnreadCount,
			FeedTs:      entry.FeedTs,
			Key:         entry.Key,
		})
	}

	h.logger.Debug("Filtered unread activity items", zap.Int("count", len(items)))

	if !includeMessages {
		return itemRows(items, h.messages.channelLabel, slackMeta())
	}

	type threadKey struct {
		ChannelID string
		ThreadTs  string
	}
	seen := make(map[threadKey]bool)
	var threads []struct {
		ChannelID   string
		ThreadTs    string
		MinUnreadTs string
	}
	for _, ai := range items {
		if ai.ThreadTs == "" {
			continue
		}
		tk := threadKey{ai.ChannelID, ai.ThreadTs}
		if seen[tk] {
			continue
		}
		seen[tk] = true
		threads = append(threads, struct {
			ChannelID   string
			ThreadTs    string
			MinUnreadTs string
		}{ai.ChannelID, ai.ThreadTs, ai.MinUnreadTs})
	}

	replies, errs := fetchInOrder(ctx, len(threads), fanOutWorkers, func(ctx context.Context, i int) ([]slack.Message, error) {
		t := threads[i]
		msgs, _, _, err := h.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
			ChannelID: t.ChannelID,
			Timestamp: t.ThreadTs,
			Oldest:    t.MinUnreadTs,
			Limit:     maxMsgsPerThread,
			Inclusive: true,
		})
		return msgs, err
	}, nil)

	var allMessages []Message
	failedThreads := 0
	stoppedEarly := false
	for i, t := range threads {
		if err := errs[i]; err != nil {
			if ctx.Err() != nil {
				stoppedEarly = true
				continue
			}
			h.logger.Warn("Failed to get thread replies",
				zap.String("channel", t.ChannelID),
				zap.String("thread_ts", t.ThreadTs),
				zap.Error(err))
			failedThreads++
			continue
		}
		msgs := h.messages.rows(ctx, replies[i], t.ChannelID, false, mode)
		allMessages = append(allMessages, msgs...)
	}

	partialReason := ""
	switch {
	case stoppedEarly:
		partialReason = "activity message fetch stopped before all threads were attempted"
	case failedThreads > 0:
		partialReason = fmt.Sprintf("%d activity threads could not be fetched", failedThreads)
	case len(allMessages) == 0 && len(items) > 0:
		partialReason = "activity messages could not be fetched; summary rows only"
	}
	meta := SlackResultMeta("", partialReason != "", partialReason)

	if len(allMessages) == 0 {
		return itemRows(items, h.messages.channelLabel, meta)
	}

	opts := h.messages.render(mode, meta)
	opts.sections = []csvSection{{name: "activity_items", rows: &items}}
	return marshalMessagesToCSV(allMessages, opts)
}

func (h *ActivityHandler) ActivityMarkReadHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := request.GetString("key", "")
	feedTs := request.GetString("feed_ts", "")
	itemType := request.GetString("type", "")

	if key == "" || feedTs == "" || itemType == "" {
		return nil, invalidArguments("key, feed_ts, and type are all required parameters")
	}

	err := h.api.ActivityMarkRead(ctx, itemType, feedTs, key)
	if err != nil {
		h.logger.Debug("ActivityMarkRead failed",
			zap.String("key", key),
			zap.String("feed_ts", feedTs),
			zap.String("type", itemType),
			zap.Error(err))
		return nil, fmt.Errorf("failed to mark activity as read: %w", err)
	}

	fallback := fmt.Sprintf("Successfully marked activity item as read (key=%s)", key)
	return NewStructuredResult(ActionData{Action: "mark_activity_read", Status: "marked", ActivityKey: key, MessageID: feedTs}, slackMeta(), fallback), nil
}

func (h *ActivityHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.ActivityUnreads, Run: h.ActivityUnreadsHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Get unread Activity items (thread replies and @mentions), as in Slack's Activity panel Unreads tab. Returns CSV: with include_messages=true the message rows come first, then an `#activity_items:` line and an item table (Type,Channel,MsgID,ThreadTs,UnreadCount,FeedTs,Key) whose Key, FeedTs and Type feed activity_mark_read. For unread channel messages use conversations_unreads. Needs a Slack browser session (xoxc/xoxd)."),
			mcp.WithBoolean("include_messages",
				mcp.Description("If true (default), fetches unread reply messages per thread. If false, returns summary only."),
				mcp.DefaultBool(true),
			),
			mcp.WithNumber("max_messages_per_thread",
				mcp.Description(fmt.Sprintf("Max messages to fetch per thread when include_messages is true. Default is %d.", defaultActivityMessagesPerThread)),
				mcp.DefaultNumber(defaultActivityMessagesPerThread),
			),
			mcp.WithNumber("limit",
				mcp.Description(fmt.Sprintf("Max Activity items to return, 1 to %d; larger values return %d. Default is %d.", activityFeedMaxLimit, activityFeedMaxLimit, defaultActivityLimit)),
				mcp.DefaultNumber(defaultActivityLimit),
			),
			detailOption(),
		}},
		{Name: capability.ActivityMarkRead, Run: h.ActivityMarkReadHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Mark an Activity item as read. Use the Key, FeedTs and Type values from the activity_unreads item table."),
			mcp.WithString("key",
				mcp.Description("Key column from activity_unreads, e.g. 'thread_v2-C092WJP9Z38-1772545632.256259'."),
				mcp.Required(),
			),
			mcp.WithString("feed_ts",
				mcp.Description("FeedTs column from activity_unreads."),
				mcp.Required(),
			),
			mcp.WithString("type",
				mcp.Description("Type column from activity_unreads: thread_v2, at_user, at_user_group, at_channel, at_everyone."),
				mcp.Required(),
			),
		}},
	}
}
