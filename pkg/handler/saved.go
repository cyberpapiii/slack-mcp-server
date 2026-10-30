package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// SavedItemRow is one item on the To-dos or Saved list. For a message, Channel
// and MsgID identify it, since Slack's item_id is the channel ID. Custom to-dos
// and files have neither and carry ItemID instead.
type SavedItemRow struct {
	ChannelID   string `csv:"Channel" json:"channel_id"`
	Ts          string `csv:"MsgID" json:"ts"`
	DateCreated string `csv:"DateCreated" json:"date_created"`
	DateDue     string `csv:"DateDue" json:"date_due,omitempty"`
	State       string `csv:"State" json:"state"`
	Type        string `csv:"Type" json:"type"`
	TodoState   string `csv:"TodoState" json:"todo_state"`
	ItemID      string `csv:"ItemID" json:"item_id,omitempty"`
	Text        string `csv:"Text" json:"text,omitempty"`
}

func newSavedItemRow(item edge.SavedItem) SavedItemRow {
	row := SavedItemRow{
		Ts:          item.Ts,
		DateCreated: formatUnixTs(item.DateCreated),
		DateDue:     formatUnixTs(item.DateDue),
		State:       item.State,
		Type:        item.ItemType,
		TodoState:   item.TodoState,
		Text:        savedItemText(item.Description),
	}
	if savedItemIsMessage(item) {
		row.ChannelID = item.ItemID
	} else {
		row.ItemID = item.ItemID
	}
	return row
}

func savedItemIsMessage(item edge.SavedItem) bool {
	return item.ItemType == edge.SavedItemMessage || item.ItemType == ""
}

func savedItemText(description json.RawMessage) string {
	if len(description) == 0 {
		return ""
	}
	var blocks slack.Blocks
	if err := json.Unmarshal(description, &blocks); err != nil {
		return ""
	}
	return text.BlocksToText(blocks)
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
			allItems = append(allItems, newSavedItemRow(item))
			fetched++

			if !includeMessages || stopErr != nil || !savedItemIsMessage(item) {
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
// savedTarget names one existing item. A message is addressed by channel and
// timestamp; a custom to-do (Sa...) or a file (F...) by its item ID.
type savedTarget struct {
	itemType string
	itemID   string
	ts       string
}

func parseSavedTarget(request mcp.CallToolRequest) (savedTarget, error) {
	channelID := request.GetString("channel_id", "")
	ts := request.GetString("timestamp", "")
	itemID := request.GetString("item_id", "")
	switch {
	case itemID != "" && (channelID != "" || ts != ""):
		return savedTarget{}, errors.New("pass either channel_id and timestamp, or item_id, not both")
	case itemID != "" && strings.HasPrefix(itemID, "Sa"):
		return savedTarget{edge.SavedItemReminder, itemID, ""}, nil
	case itemID != "" && strings.HasPrefix(itemID, "F"):
		return savedTarget{edge.SavedItemFile, itemID, ""}, nil
	case itemID != "":
		return savedTarget{}, fmt.Errorf("item_id %q is not a custom to-do (Sa...) or a file (F...)", itemID)
	case channelID == "" || ts == "":
		return savedTarget{}, errors.New("channel_id and timestamp are required for a message; use item_id for a custom to-do or a file")
	}
	return savedTarget{edge.SavedItemMessage, channelID, ts}, nil
}

// savedDateDue returns the date_due argument as Slack's form value, or "" when it
// was not passed. 0 clears a due date, so absent and zero must stay distinct.
func savedDateDue(request mcp.CallToolRequest) string {
	if _, provided := request.GetArguments()["date_due"]; !provided {
		return ""
	}
	return strconv.Itoa(request.GetInt("date_due", 0))
}

func validSavedTodoState(state string) bool {
	return state == "" || state == "to_do" || state == "saved"
}

func parseSavedUpdateParams(request mcp.CallToolRequest) (edge.SavedUpdateParams, error) {
	target, err := parseSavedTarget(request)
	if err != nil {
		return edge.SavedUpdateParams{}, err
	}
	params := edge.SavedUpdateParams{
		ItemType:  target.itemType,
		ItemID:    target.itemID,
		Ts:        target.ts,
		Mark:      request.GetString("mark", ""),
		TodoState: request.GetString("todo_state", ""),
		DateDue:   savedDateDue(request),
	}
	if params.Mark != "" && params.Mark != "completed" && params.Mark != "uncompleted" {
		return edge.SavedUpdateParams{}, fmt.Errorf("mark must be completed or uncompleted, got %q", params.Mark)
	}
	if !validSavedTodoState(params.TodoState) {
		return edge.SavedUpdateParams{}, fmt.Errorf("todo_state must be to_do or saved, got %q", params.TodoState)
	}
	if params.Mark == "" && params.TodoState == "" && params.DateDue == "" {
		return edge.SavedUpdateParams{}, errors.New("at least one of mark, todo_state or date_due must be provided")
	}
	// Without a state, Slack files a reopened item under Saved. It was completed
	// from To-dos, so that is where it goes back to.
	if params.Mark == "uncompleted" && params.TodoState == "" {
		params.TodoState = "to_do"
	}
	return params, nil
}

func (h *SavedHandler) SavedUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "SavedUpdateHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("saved_update", h.apiProvider.BrowserDegradedReason())
	}

	params, err := parseSavedUpdateParams(request)
	if err != nil {
		return nil, err
	}

	if err := h.apiProvider.Slack().SavedUpdate(ctx, params); err != nil {
		h.logger.Debug("SavedUpdate failed", zap.String("item_id", params.ItemID), zap.String("ts", params.Ts), zap.Error(err))
		return nil, fmt.Errorf("failed to update saved item: %w", err)
	}

	fallback := fmt.Sprintf("Successfully updated saved item (item_id=%s, ts=%s)", params.ItemID, params.Ts)
	return NewStructuredResult(savedActionData("update_saved_item", "updated", params.ItemType, params.ItemID, params.Ts), SlackResultMeta("", false, ""), fallback), nil
}

// parseSavedAddParams builds saved.add arguments from exactly one of text, a
// message (channel_id and timestamp) or file_id. A new item goes to To-dos unless
// todo_state says otherwise.
func parseSavedAddParams(request mcp.CallToolRequest) (edge.SavedAddParams, error) {
	body := strings.TrimSpace(request.GetString("text", ""))
	channelID := request.GetString("channel_id", "")
	ts := request.GetString("timestamp", "")
	fileID := request.GetString("file_id", "")

	params := edge.SavedAddParams{TodoState: request.GetString("todo_state", "to_do"), DateDue: savedDateDue(request)}
	if !validSavedTodoState(params.TodoState) {
		return edge.SavedAddParams{}, fmt.Errorf("todo_state must be to_do or saved, got %q", params.TodoState)
	}
	switch {
	case body != "" && channelID == "" && ts == "" && fileID == "":
		description, err := json.Marshal([]map[string]any{{
			"type": "rich_text",
			"elements": []map[string]any{{
				"type":     "rich_text_section",
				"elements": []map[string]any{{"type": "text", "text": body}},
			}},
		}})
		if err != nil {
			return edge.SavedAddParams{}, err
		}
		params.ItemType, params.Description = edge.SavedItemReminder, string(description)
	case body == "" && channelID != "" && ts != "" && fileID == "":
		params.ItemType, params.ItemID, params.Ts = edge.SavedItemMessage, channelID, ts
	case body == "" && channelID == "" && ts == "" && fileID != "":
		params.ItemType, params.ItemID = edge.SavedItemFile, fileID
	default:
		return edge.SavedAddParams{}, errors.New("pass exactly one of: text, channel_id with timestamp, or file_id")
	}
	return params, nil
}

func (h *SavedHandler) SavedAddHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "SavedAddHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("saved_add", h.apiProvider.BrowserDegradedReason())
	}

	params, err := parseSavedAddParams(request)
	if err != nil {
		return nil, err
	}

	item, err := h.apiProvider.Slack().SavedAdd(ctx, params)
	if err != nil {
		h.logger.Debug("SavedAdd failed", zap.String("item_type", params.ItemType), zap.Error(err))
		return nil, fmt.Errorf("failed to add saved item: %w", err)
	}

	fallback := fmt.Sprintf("Successfully added saved item (item_id=%s, ts=%s)", item.ItemID, item.Ts)
	return NewStructuredResult(savedActionData("add_saved_item", "added", params.ItemType, item.ItemID, params.Ts), SlackResultMeta("", false, ""), fallback), nil
}

func (h *SavedHandler) SavedDeleteHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "SavedDeleteHandler called", request)
	if !h.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("saved_delete", h.apiProvider.BrowserDegradedReason())
	}

	target, err := parseSavedTarget(request)
	if err != nil {
		return nil, err
	}

	if err := h.apiProvider.Slack().SavedDelete(ctx, target.itemType, target.itemID, target.ts); err != nil {
		h.logger.Debug("SavedDelete failed", zap.String("item_id", target.itemID), zap.String("ts", target.ts), zap.Error(err))
		return nil, fmt.Errorf("failed to delete saved item: %w", err)
	}

	fallback := fmt.Sprintf("Successfully removed saved item (item_id=%s, ts=%s)", target.itemID, target.ts)
	return NewStructuredResult(savedActionData("delete_saved_item", "deleted", target.itemType, target.itemID, target.ts), SlackResultMeta("", false, ""), fallback), nil
}

func savedActionData(action, status, itemType, itemID, ts string) ActionData {
	data := ActionData{Action: action, Status: status, MessageID: ts, ItemID: itemID}
	if itemType == edge.SavedItemMessage {
		data.ChannelID = itemID
	}
	return data
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
