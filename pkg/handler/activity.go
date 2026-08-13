package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type ActivityItem struct {
	Type        string `csv:"Type" json:"type"`
	ChannelID   string `csv:"ChannelID" json:"channel_id"`
	ChannelName string `csv:"ChannelName" json:"channel_name"`
	ThreadTs    string `csv:"ThreadTs" json:"thread_ts,omitempty"`
	UnreadCount int    `csv:"UnreadCount" json:"unread_count"`
	FeedTs      string `csv:"FeedTs" json:"feed_ts"`
	Key         string `csv:"Key" json:"key"`
	MinUnreadTs string `csv:"MinUnreadTs" json:"min_unread_ts,omitempty"`
}

type ActivityHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
	convHandler *ConversationsHandler
}

func NewActivityHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, convHandler *ConversationsHandler) *ActivityHandler {
	return &ActivityHandler{apiProvider: apiProvider, logger: logger, convHandler: convHandler}
}

// activityChannelLabel formats "ID (#name)" like search output; bare ID if uncached.
func activityChannelLabel(channelID string, channels map[string]provider.Channel) string {
	if cached, ok := channels[channelID]; ok && cached.Name != "" {
		return fmt.Sprintf("%s (%s)", channelID, cached.Name)
	}
	return channelID
}

func (h *ActivityHandler) ActivityUnreadsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ActivityUnreadsHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		reason := h.apiProvider.BrowserDegradedReason()
		if reason == "" {
			reason = "browser-session Slack auth is unavailable"
		}
		return nil, fmt.Errorf("browser-session Slack auth is no longer valid; stable Slack tools remain available; refresh browser tokens to restore Activity tools (%s)", reason)
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

	channelsMaps := h.apiProvider.ProvideChannelsMaps()

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

		if cached, ok := channelsMaps.Channels[ai.ChannelID]; ok {
			ai.ChannelName = cached.Name
		} else {
			ai.ChannelName = ai.ChannelID
		}

		items = append(items, ai)
	}

	h.logger.Debug("Filtered unread activity items", zap.Int("count", len(items)))

	if !includeMessages {
		csvBytes, err := gocsv.MarshalBytes(&items)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal activity items: %v", err)
		}
		return NewStructuredResult(
			ActivityPageData{Items: items},
			SlackResultMeta("", false, ""),
			string(csvBytes),
		), nil
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

		channelLabel := activityChannelLabel(t.ChannelID, channelsMaps.Channels)
		msgs := h.convHandler.convertMessagesFromHistory(ctx, replies, channelLabel, false, mode)
		allMessages = append(allMessages, msgs...)
	}

	if len(allMessages) == 0 {
		var sb strings.Builder
		sb.WriteString("No messages could be fetched. Activity summary:\n")
		csvBytes, err := gocsv.MarshalBytes(&items)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal activity items: %v", err)
		}
		sb.Write(csvBytes)
		return NewStructuredResult(
			ActivityPageData{Items: items},
			SlackResultMeta("", true, "activity messages could not be fetched"),
			sb.String(),
		), nil
	}

	rendered, err := marshalMessagesToCSV(allMessages, renderOptions{mode: mode, workspaceURL: h.apiProvider.WorkspaceURL()})
	if err != nil {
		return nil, err
	}
	partial := failedThreads > 0 || stoppedEarly
	partialReason := ""
	if partial {
		partialReason = fmt.Sprintf("%d activity threads could not be fetched", failedThreads)
		if stoppedEarly {
			partialReason = "activity message fetch stopped before all threads were attempted"
		}
	}
	return NewStructuredResult(
		ActivityPageData{Items: items, Messages: allMessages},
		SlackResultMeta("", partial, partialReason),
		ResultText(rendered),
	), nil
}

func (h *ActivityHandler) ActivityMarkReadHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ActivityMarkReadHandler called", request)
	if !requireToolEnabled("SLACK_MCP_ACTIVITY_MARK_TOOL", "activity_mark_read") {
		return nil, &ToolError{Code: "tool_disabled", Message: "activity_mark_read is disabled"}
	}
	if !h.apiProvider.BrowserFeaturesAvailable() {
		reason := h.apiProvider.BrowserDegradedReason()
		if reason == "" {
			reason = "browser-session Slack auth is unavailable"
		}
		return nil, fmt.Errorf("browser-session Slack auth is no longer valid; stable Slack tools remain available; refresh browser tokens to restore Activity tools (%s)", reason)
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
