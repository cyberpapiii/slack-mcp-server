package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
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
		return nil, fmt.Errorf("failed to marshal saved items: %v", err)
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
}

func NewSavedHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, convHandler *ConversationsHandler) *SavedHandler {
	return &SavedHandler{apiProvider: apiProvider, logger: logger, convHandler: convHandler}
}

func (h *SavedHandler) SavedListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "SavedListHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("saved_list", h.apiProvider.BrowserDegradedReason())
	}

	filter := request.GetString("filter", "saved")
	limit := request.GetInt("limit", 50)
	if limit <= 0 {
		limit = 50
	}
	includeMessages := request.GetBool("include_messages", true)
	maxMsgsPerItem := request.GetInt("max_messages_per_item", 5)
	if maxMsgsPerItem <= 0 {
		maxMsgsPerItem = 5
	}

	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}

	rl := limiter.Tier3.Limiter()

	var allItems []SavedItemRow
	var allMessages []Message
	cursor := request.GetString("cursor", "")
	nextCursor := ""
	fetched := 0
	pageSize := limit
	if pageSize > 50 {
		pageSize = 50
	}
	var stopErr error

	for fetched < limit {
		if cursor != "" {
			if err := rl.Wait(ctx); err != nil {
				h.logger.Warn("Rate limiter wait failed, stopping pagination", zap.Error(err))
				stopErr = err
				break
			}
		}

		resp, err := h.apiProvider.Slack().SavedList(ctx, filter, pageSize, cursor)
		if err != nil {
			h.logger.Error("SavedList failed", zap.Error(err))
			return nil, fmt.Errorf("failed to list saved items: %v", err)
		}
		nextCursor = resp.ResponseMetadata.NextCursor

		for _, item := range resp.SavedItems {
			if fetched >= limit {
				break
			}

			row := SavedItemRow{
				ChannelID:   item.ItemID,
				Ts:          item.Ts,
				DateCreated: formatUnixTs(item.DateCreated),
				DateDue:     formatUnixTs(item.DateDue),
				State:       item.State,
			}
			allItems = append(allItems, row)
			fetched++

			if includeMessages {
				if err := rl.Wait(ctx); err != nil {
					stopErr = err
					break
				}
				histParams := &slack.GetConversationHistoryParameters{
					ChannelID: item.ItemID,
					Latest:    item.Ts,
					Oldest:    item.Ts,
					Inclusive: true,
					Limit:     1,
				}
				histResp, err := h.apiProvider.WebAPI().GetConversationHistoryContext(ctx, histParams)
				if err != nil {
					h.logger.Warn("Failed to fetch saved message via history, trying replies",
						zap.String("channel", item.ItemID),
						zap.String("ts", item.Ts),
						zap.Error(err))
					allMessages = append(allMessages, Message{
						MsgID:   item.Ts,
						Channel: item.ItemID,
						Text:    "[message content unavailable: channel access denied]",
						Time:    formatUnixTs(item.DateCreated),
					})
					continue
				}
				if len(histResp.Messages) > 0 {
					msg := histResp.Messages[0]
					if msg.ThreadTimestamp != "" && msg.ThreadTimestamp != msg.Timestamp {
						repliesParams := &slack.GetConversationRepliesParameters{
							ChannelID: item.ItemID,
							Timestamp: msg.ThreadTimestamp,
							Latest:    item.Ts,
							Oldest:    item.Ts,
							Inclusive: true,
							Limit:     maxMsgsPerItem,
						}
						replies, _, _, err := h.apiProvider.WebAPI().GetConversationRepliesContext(ctx, repliesParams)
						if err == nil && len(replies) > 0 {
							msgs := h.convHandler.convertMessagesFromHistory(ctx, replies, item.ItemID, false, mode)
							allMessages = append(allMessages, msgs...)
							continue
						}
					}
					msgs := h.convHandler.convertMessagesFromHistory(ctx, histResp.Messages, item.ItemID, false, mode)
					allMessages = append(allMessages, msgs...)
				} else {
					repliesParams := &slack.GetConversationRepliesParameters{
						ChannelID: item.ItemID,
						Timestamp: item.Ts,
						Latest:    item.Ts,
						Oldest:    item.Ts,
						Inclusive: true,
						Limit:     maxMsgsPerItem,
					}
					replies, _, _, err := h.apiProvider.WebAPI().GetConversationRepliesContext(ctx, repliesParams)
					if err == nil && len(replies) > 0 {
						msgs := h.convHandler.convertMessagesFromHistory(ctx, replies, item.ItemID, false, mode)
						allMessages = append(allMessages, msgs...)
					} else {
						allMessages = append(allMessages, Message{
							MsgID:   item.Ts,
							Channel: item.ItemID,
							Text:    "[saved item: message not found in channel history]",
							Time:    formatUnixTs(item.DateCreated),
						})
					}
				}
			}
		}

		if stopErr != nil || resp.ResponseMetadata.NextCursor == "" || fetched >= limit {
			break
		}
		cursor = resp.ResponseMetadata.NextCursor
	}

	if stopErr != nil {
		return cancelledToolResult(), nil
	}

	if includeMessages && len(allMessages) > 0 {
		opts := h.convHandler.render(mode, SlackResultMeta(nextCursor, false, ""))
		trailer, err := csvSection("saved_items", &allItems)
		if err != nil {
			return nil, err
		}
		opts.trailer = trailer
		return marshalMessagesToCSV(allMessages, opts)
	}

	return renderSavedItems(allItems, h.convHandler.channelDisplayName, SlackResultMeta(nextCursor, false, ""))
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
		h.logger.Error("SavedUpdate failed",
			zap.String("item_id", itemID),
			zap.String("ts", ts),
			zap.String("mark", mark),
			zap.Int64("date_due", dateDue),
			zap.Error(err))
		return nil, fmt.Errorf("failed to update saved item: %v", err)
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
		h.logger.Error("SavedClearCompleted failed", zap.Error(err))
		return nil, fmt.Errorf("failed to clear completed saved items: %v", err)
	}

	return NewStructuredResult(ActionData{Action: "clear_completed_saved_items", Status: "cleared"}, SlackResultMeta("", false, ""), "Successfully cleared all completed saved items"), nil
}

func formatUnixTs(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format("2006-01-02 15:04")
}
