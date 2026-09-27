package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// SavedItemRow is one saved ("Later") message. Channel and MsgID are the
// saved_update item_id and ts arguments; Slack's item_id is the channel ID,
// so it is not carried twice.
type SavedItemRow struct {
	ChannelID   string `csv:"Channel" json:"channel_id"`
	Ts          string `csv:"MsgID" json:"ts"`
	DateCreated string `csv:"DateCreated" json:"date_created"`
	DateDue     string `csv:"DateDue" json:"date_due,omitempty"`
	State       string `csv:"State" json:"state"`
}

// renderSavedItems is the include_messages=false output: rows keyed by the
// bare channel ID with a "#channels:" legend.
func renderSavedItems(items []SavedItemRow, channelName func(string) string, meta ResultMeta) (*mcp.CallToolResult, error) {
	csvBytes, err := gocsv.MarshalBytes(&items)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal saved items: %w", err)
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ChannelID
	}
	return NewCSVResult(channelsLegend(ids, channelName), meta, string(csvBytes)), nil
}

type SavedHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
	convHandler *ConversationsHandler

	// pace, listPage and itemMessages are the Slack side of listSaved,
	// swapped out in tests.
	pace         *rate.Limiter
	listPage     func(ctx context.Context, filter string, limit int, cursor string) (edge.SavedListResponse, error)
	itemMessages func(ctx context.Context, item edge.SavedItem, maxMsgsPerItem int, mode text.OutputMode) ([]Message, error)
}

func NewSavedHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, convHandler *ConversationsHandler) *SavedHandler {
	h := &SavedHandler{apiProvider: apiProvider, logger: logger, convHandler: convHandler, pace: limiter.Tier3.Limiter()}
	h.listPage = func(ctx context.Context, filter string, limit int, cursor string) (edge.SavedListResponse, error) {
		return apiProvider.Slack().SavedList(ctx, filter, limit, cursor)
	}
	h.itemMessages = h.savedItemMessages
	return h
}

func (h *SavedHandler) SavedListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "SavedListHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("saved_list", h.apiProvider.BrowserDegradedReason())
	}

	filter := request.GetString("filter", "saved")
	limit := pageLimit(request, 50, 0)
	includeMessages := request.GetBool("include_messages", true)
	maxMsgsPerItem := request.GetInt("max_messages_per_item", 5)
	if maxMsgsPerItem <= 0 {
		maxMsgsPerItem = 5
	}

	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}

	return h.listSaved(ctx, filter, limit, request.GetString("cursor", ""), includeMessages, maxMsgsPerItem, mode)
}

// listSaved walks saved.list pages and, with includeMessages, fetches each
// saved message. When the call's context ends partway, the rows already
// listed are returned marked partial instead of being discarded.
func (h *SavedHandler) listSaved(ctx context.Context, filter string, limit int, cursor string, includeMessages bool, maxMsgsPerItem int, mode text.OutputMode) (*mcp.CallToolResult, error) {
	var allItems []SavedItemRow
	var allMessages []Message
	nextCursor := ""
	fetched := 0
	pageSize := limit
	if pageSize > 50 {
		pageSize = 50
	}
	// stopErr is set once the walk has to stop early. The rest of the page in
	// hand is still listed, without messages, so nextCursor stays a valid
	// resume point.
	var stopErr error

	for fetched < limit && stopErr == nil {
		if cursor != "" {
			if err := h.pace.Wait(ctx); err != nil {
				stopErr = err
				break
			}
		}

		resp, err := h.listPage(ctx, filter, pageSize, cursor)
		if err != nil {
			if len(allItems) > 0 && ctx.Err() != nil {
				stopErr = err
				break
			}
			h.logger.Debug("SavedList failed", zap.Error(err))
			return nil, fmt.Errorf("failed to list saved items: %w", err)
		}
		nextCursor = resp.ResponseMetadata.NextCursor

		for _, item := range resp.SavedItems {
			if fetched >= limit {
				break
			}
			allItems = append(allItems, SavedItemRow{
				ChannelID:   item.ItemID,
				Ts:          item.Ts,
				DateCreated: formatUnixTs(item.DateCreated),
				DateDue:     formatUnixTs(item.DateDue),
				State:       item.State,
			})
			fetched++

			if !includeMessages || stopErr != nil {
				continue
			}
			if err := h.pace.Wait(ctx); err != nil {
				stopErr = err
				continue
			}
			msgs, err := h.itemMessages(ctx, item, maxMsgsPerItem, mode)
			if err != nil {
				stopErr = err
				continue
			}
			allMessages = append(allMessages, msgs...)
		}

		if resp.ResponseMetadata.NextCursor == "" {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
	}

	if stopErr != nil && len(allItems) == 0 {
		return cancelledToolResult(), nil
	}
	partialReason := ""
	if stopErr != nil {
		h.logger.Warn("saved_list stopped early; returning the items listed so far", zap.Int("items", len(allItems)), zap.Error(stopErr))
		partialReason = "saved_list stopped before the walk finished"
		if includeMessages {
			partialReason += "; some rows have no message"
		}
	}
	meta := SlackResultMeta(nextCursor, stopErr != nil, partialReason)

	if includeMessages && len(allMessages) > 0 {
		opts := h.convHandler.render(mode, meta)
		trailer, err := csvSection("saved_items", &allItems)
		if err != nil {
			return nil, err
		}
		opts.trailer = trailer
		return marshalMessagesToCSV(allMessages, opts)
	}

	return renderSavedItems(allItems, h.convHandler.channelDisplayName, meta)
}

// savedItemMessages fetches one saved message, or the thread reply it points
// at. A Slack failure becomes a placeholder row; an error is returned only
// when ctx is done, so the caller can stop instead of filling the output with
// placeholders.
func (h *SavedHandler) savedItemMessages(ctx context.Context, item edge.SavedItem, maxMsgsPerItem int, mode text.OutputMode) ([]Message, error) {
	histResp, err := h.apiProvider.WebAPI().GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID: item.ItemID,
		Latest:    item.Ts,
		Oldest:    item.Ts,
		Inclusive: true,
		Limit:     1,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		h.logger.Warn("Failed to fetch saved message via history",
			zap.String("channel", item.ItemID),
			zap.String("ts", item.Ts),
			zap.Error(err))
		return []Message{{
			MsgID:   item.Ts,
			Channel: item.ItemID,
			Text:    "[message content unavailable: channel access denied]",
			Time:    formatUnixTs(item.DateCreated),
		}}, nil
	}

	if len(histResp.Messages) > 0 {
		msg := histResp.Messages[0]
		if msg.ThreadTimestamp != "" && msg.ThreadTimestamp != msg.Timestamp {
			replies, _, _, err := h.apiProvider.WebAPI().GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
				ChannelID: item.ItemID,
				Timestamp: msg.ThreadTimestamp,
				Latest:    item.Ts,
				Oldest:    item.Ts,
				Inclusive: true,
				Limit:     maxMsgsPerItem,
			})
			if err == nil && len(replies) > 0 {
				return h.convHandler.convertMessagesFromHistory(ctx, replies, item.ItemID, false, mode), nil
			}
		}
		return h.convHandler.convertMessagesFromHistory(ctx, histResp.Messages, item.ItemID, false, mode), nil
	}

	replies, _, _, err := h.apiProvider.WebAPI().GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: item.ItemID,
		Timestamp: item.Ts,
		Latest:    item.Ts,
		Oldest:    item.Ts,
		Inclusive: true,
		Limit:     maxMsgsPerItem,
	})
	if err == nil && len(replies) > 0 {
		return h.convHandler.convertMessagesFromHistory(ctx, replies, item.ItemID, false, mode), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return []Message{{
		MsgID:   item.Ts,
		Channel: item.ItemID,
		Text:    "[saved item: message not found in channel history]",
		Time:    formatUnixTs(item.DateCreated),
	}}, nil
}

// parseSavedUpdateParams: schema uses date_due:0 to clear; key presence (not truthiness) gates the required-field check.
func parseSavedUpdateParams(request mcp.CallToolRequest) (itemID, ts, mark string, dateDue int64, err error) {
	itemID = request.GetString("channel_id", "")
	ts = request.GetString("timestamp", "")
	mark = request.GetString("mark", "")
	dateDue = int64(request.GetInt("date_due", 0))

	_, dateDueProvided := request.GetArguments()["date_due"]

	if itemID == "" || ts == "" {
		return "", "", "", 0, fmt.Errorf("channel_id and timestamp are required parameters")
	}
	if mark == "" && !dateDueProvided {
		return "", "", "", 0, fmt.Errorf("at least one of mark or date_due must be provided")
	}
	return itemID, ts, mark, dateDue, nil
}

func (h *SavedHandler) SavedUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "SavedUpdateHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("saved_update", h.apiProvider.BrowserDegradedReason())
	}

	itemID, ts, mark, dateDue, err := parseSavedUpdateParams(request)
	if err != nil {
		return nil, err
	}

	err = h.apiProvider.Slack().SavedUpdate(ctx, "message", itemID, ts, mark, dateDue)
	if err != nil {
		h.logger.Debug("SavedUpdate failed",
			zap.String("item_id", itemID),
			zap.String("ts", ts),
			zap.String("mark", mark),
			zap.Int64("date_due", dateDue),
			zap.Error(err))
		return nil, fmt.Errorf("failed to update saved item: %w", err)
	}

	action := "updated"
	if mark == "completed" {
		action = "marked as completed"
	}
	if dateDue > 0 {
		dueTime := time.Unix(dateDue, 0).UTC().Format("2006-01-02 15:04")
		action += fmt.Sprintf(", due date set to %s", dueTime)
	}

	fallback := fmt.Sprintf("Successfully %s saved item (item_id=%s, ts=%s)", action, itemID, ts)
	return NewStructuredResult(ActionData{Action: "update_saved_item", Status: "updated", ChannelID: itemID, MessageID: ts, ItemID: itemID}, SlackResultMeta("", false, ""), fallback), nil
}

func (h *SavedHandler) SavedClearCompletedHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "SavedClearCompletedHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("saved_clear_completed", h.apiProvider.BrowserDegradedReason())
	}

	err := h.apiProvider.Slack().SavedClearCompleted(ctx)
	if err != nil {
		h.logger.Debug("SavedClearCompleted failed", zap.Error(err))
		return nil, fmt.Errorf("failed to clear completed saved items: %w", err)
	}

	return NewStructuredResult(ActionData{Action: "clear_completed_saved_items", Status: "cleared"}, SlackResultMeta("", false, ""), "Successfully cleared all completed saved items"), nil
}

func formatUnixTs(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04")
}
