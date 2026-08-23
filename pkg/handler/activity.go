package handler

import (
	"context"
	"fmt"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
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

type ActivityHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
	convHandler *ConversationsHandler
}

func NewActivityHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, convHandler *ConversationsHandler) *ActivityHandler {
	return &ActivityHandler{apiProvider: apiProvider, logger: logger, convHandler: convHandler}
}

// renderActivityItems is the include_messages=false output: rows keyed by the
// bare channel ID with a "#channels:" legend.
func renderActivityItems(items []ActivityItem, channelName func(string) string, meta ResultMeta) (*mcp.CallToolResult, error) {
	csvBytes, err := gocsv.MarshalBytes(&items)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal activity items: %v", err)
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ChannelID
	}
	return NewCSVResult(channelsLegend(ids, channelName), meta, string(csvBytes)), nil
}

func (h *ActivityHandler) ActivityUnreadsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ActivityUnreadsHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("activity_unreads", h.apiProvider.BrowserDegradedReason())
	}

	includeMessages := request.GetBool("include_messages", true)
	maxMsgsPerThread := request.GetInt("max_messages_per_thread", 10)
	if maxMsgsPerThread <= 0 {
		maxMsgsPerThread = 10
	}
	limit := request.GetInt("limit", 30)
	if limit <= 0 {
		limit = 30
	}

	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}

	feedResp, err := h.apiProvider.Slack().ActivityFeed(ctx, limit)
	if err != nil {
		h.logger.Error("ActivityFeed failed", zap.Error(err))
		return nil, fmt.Errorf("failed to get activity feed: %v", err)
	}

	var items []ActivityItem
	for _, fi := range feedResp.Items {
		if !fi.IsUnread {
			continue
		}

		var ai ActivityItem
		ai.FeedTs = fi.FeedTs
		ai.Key = fi.Key
		ai.Type = fi.Item.Type

		switch fi.Item.Type {
		case "thread_v2":
			if fi.Item.BundleInfo == nil {
				continue
			}
			te := fi.Item.BundleInfo.Payload.ThreadEntry
			ai.ChannelID = te.ChannelID
			ai.ThreadTs = te.ThreadTs
			ai.UnreadCount = te.UnreadMsgCount
			ai.MinUnreadTs = te.MinUnreadTs
		default:
			if fi.Item.Message == nil {
				continue
			}
			ai.ChannelID = fi.Item.Message.Channel
			ai.ThreadTs = fi.Item.Message.ThreadTs
			ai.UnreadCount = 1
			ai.MinUnreadTs = fi.Item.Message.Ts
		}

		items = append(items, ai)
	}

	h.logger.Debug("Filtered unread activity items", zap.Int("count", len(items)))

	if !includeMessages {
		return renderActivityItems(items, h.convHandler.channelDisplayName, SlackResultMeta("", false, ""))
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

	rl := limiter.Tier3.Limiter()
	var allMessages []Message
	failedThreads := 0
	stoppedEarly := false

	for _, t := range threads {
		if err := rl.Wait(ctx); err != nil {
			h.logger.Warn("Rate limiter wait failed, stopping fetch", zap.Error(err))
			stoppedEarly = true
			break
		}

		oldest := t.MinUnreadTs
		repliesParams := slack.GetConversationRepliesParameters{
			ChannelID: t.ChannelID,
			Timestamp: t.ThreadTs,
			Oldest:    oldest,
			Limit:     maxMsgsPerThread,
			Inclusive: true,
		}
		replies, _, _, err := h.apiProvider.WebAPI().GetConversationRepliesContext(ctx, &repliesParams)
		if err != nil {
			h.logger.Warn("Failed to get thread replies",
				zap.String("channel", t.ChannelID),
				zap.String("thread_ts", t.ThreadTs),
				zap.Error(err))
			failedThreads++
			continue
		}

		msgs := h.convHandler.convertMessagesFromHistory(ctx, replies, t.ChannelID, false, mode)
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
		return renderActivityItems(items, h.convHandler.channelDisplayName, meta)
	}

	opts := h.convHandler.render(mode, meta)
	if opts.trailer, err = csvSection("activity_items", &items); err != nil {
		return nil, err
	}
	return marshalMessagesToCSV(allMessages, opts)
}

// csvSection renders a second CSV table under a "#<name>:" comment line, for
// results whose message rows need a companion table (saved state, activity
// feed keys) that the agent acts on with another tool.
func csvSection(name string, rows any) (string, error) {
	csvBytes, err := gocsv.MarshalBytes(rows)
	if err != nil {
		return "", fmt.Errorf("failed to marshal %s: %v", name, err)
	}
	return "#" + name + ":\n" + string(csvBytes), nil
}

func (h *ActivityHandler) ActivityMarkReadHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ActivityMarkReadHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("activity_mark_read", h.apiProvider.BrowserDegradedReason())
	}

	key := request.GetString("key", "")
	feedTs := request.GetString("feed_ts", "")
	itemType := request.GetString("type", "")

	if key == "" || feedTs == "" || itemType == "" {
		return nil, fmt.Errorf("key, feed_ts, and type are all required parameters")
	}

	err := h.apiProvider.Slack().ActivityMarkRead(ctx, itemType, feedTs, key)
	if err != nil {
		h.logger.Error("ActivityMarkRead failed",
			zap.String("key", key),
			zap.String("feed_ts", feedTs),
			zap.String("type", itemType),
			zap.Error(err))
		return nil, fmt.Errorf("failed to mark activity as read: %v", err)
	}

	fallback := fmt.Sprintf("Successfully marked activity item as read (key=%s)", key)
	return NewStructuredResult(ActionData{Action: "mark_activity_read", Status: "marked", ActivityKey: key, MessageID: feedTs}, SlackResultMeta("", false, ""), fallback), nil
}
