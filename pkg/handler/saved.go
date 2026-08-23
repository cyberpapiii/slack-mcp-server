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

type SavedItemRow struct {
	ItemID      string `csv:"ItemID" json:"item_id"`
	ChannelID   string `csv:"ChannelID" json:"channel_id"`
	ChannelName string `csv:"ChannelName" json:"channel_name"`
	Ts          string `csv:"Ts" json:"ts"`
	DateCreated string `csv:"DateCreated" json:"date_created"`
	DateDue     string `csv:"DateDue" json:"date_due,omitempty"`
	State       string `csv:"State" json:"state"`
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
		reason := h.apiProvider.BrowserDegradedReason()
		if reason == "" {
			reason = "browser-session Slack auth is unavailable"
		}
		return nil, fmt.Errorf("browser-session Slack auth is no longer valid; stable Slack tools remain available; refresh browser tokens to restore Saved tools (%s)", reason)
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

	channelsMaps := h.apiProvider.ProvideChannelsMaps()
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

			channelName := item.ItemID
			if cached, ok := channelsMaps.Channels[item.ItemID]; ok {
				channelName = cached.Name
			}

			row := SavedItemRow{
				ItemID:      item.ItemID,
				ChannelID:   item.ItemID,
				ChannelName: channelName,
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
						Channel: channelName,
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
							Channel: channelName,
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
		rendered, err := marshalMessagesToCSV(allMessages, renderOptions{mode: mode, workspaceURL: h.apiProvider.WorkspaceURL()})
		if err != nil {
			return nil, err
		}
		partialReason := ""
		if nextCursor != "" {
			partialReason = "result stopped at the requested item limit"
		}
		return NewStructuredResult(
			SavedPageData{Items: allItems, Messages: allMessages},
			SlackResultMeta(nextCursor, nextCursor != "", partialReason),
			ResultText(rendered),
		), nil
	}

	csvBytes, err := gocsv.MarshalBytes(&allItems)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal saved items: %v", err)
	}
	partialReason := ""
	if nextCursor != "" {
		partialReason = "result stopped at the requested item limit"
	}
	return NewStructuredResult(
		SavedPageData{Items: allItems},
		SlackResultMeta(nextCursor, nextCursor != "", partialReason),
		string(csvBytes),
	), nil
}

// parseSavedUpdateParams: schema uses date_due:0 to clear; key presence (not truthiness) gates the required-field check.
func parseSavedUpdateParams(request mcp.CallToolRequest) (itemID, ts, mark string, dateDue int64, err error) {
	itemID = request.GetString("item_id", "")
	ts = request.GetString("ts", "")
	mark = request.GetString("mark", "")
	dateDue = int64(request.GetInt("date_due", 0))

	_, dateDueProvided := request.GetArguments()["date_due"]

	if itemID == "" || ts == "" {
		return "", "", "", 0, fmt.Errorf("item_id and ts are required parameters")
	}
	if mark == "" && !dateDueProvided {
		return "", "", "", 0, fmt.Errorf("at least one of mark or date_due must be provided")
	}
	return itemID, ts, mark, dateDue, nil
}

func (h *SavedHandler) SavedUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "SavedUpdateHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		reason := h.apiProvider.BrowserDegradedReason()
		if reason == "" {
			reason = "browser-session Slack auth is unavailable"
		}
		return nil, fmt.Errorf("browser-session Slack auth is no longer valid; stable Slack tools remain available; refresh browser tokens to restore Saved tools (%s)", reason)
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
		reason := h.apiProvider.BrowserDegradedReason()
		if reason == "" {
			reason = "browser-session Slack auth is unavailable"
		}
		return nil, fmt.Errorf("browser-session Slack auth is no longer valid; stable Slack tools remain available; refresh browser tokens to restore Saved tools (%s)", reason)
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
