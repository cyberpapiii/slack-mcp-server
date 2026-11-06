package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
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
	settings    Settings
	messages    messagePipeline

	// pace, listPage and itemMessages are the Slack side of listSaved,
	// swapped out in tests.
	pace         *rate.Limiter
	listPage     func(ctx context.Context, filter string, limit int, cursor string) (edge.SavedListResponse, error)
	itemMessages func(ctx context.Context, item edge.SavedItem, maxMsgsPerItem int, mode text.OutputMode) ([]Message, error)
}

func NewSavedHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, settings Settings) *SavedHandler {
	h := &SavedHandler{apiProvider: apiProvider, logger: logger, settings: settings, messages: messagePipeline{api: apiProvider, logger: logger}, pace: limiter.Tier3}
	h.listPage = func(ctx context.Context, filter string, limit int, cursor string) (edge.SavedListResponse, error) {
		return apiProvider.Slack().SavedList(ctx, filter, limit, cursor)
	}
	h.itemMessages = h.savedItemMessages
	return h
}

func (h *SavedHandler) SavedListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

	mode, err := h.settings.outputMode(request)
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
		nextCursor = resp.ResponseMetadata.Cursor

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

		if resp.ResponseMetadata.Cursor == "" {
			break
		}
		cursor = resp.ResponseMetadata.Cursor
	}

	if stopErr != nil && len(allItems) == 0 {
		return nil, stopErr
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
		opts := h.messages.render(mode, meta)
		trailer, err := csvSection("saved_items", &allItems)
		if err != nil {
			return nil, err
		}
		opts.trailer = trailer
		return marshalMessagesToCSV(allMessages, opts)
	}

	return renderSavedItems(allItems, h.apiProvider.ChannelLabel, meta)
}

// savedItemMessages fetches one saved message. A Slack failure is logged and
// becomes a placeholder row; an error is returned only when ctx is done, so
// the caller can stop instead of filling the output with placeholders.
func (h *SavedHandler) savedItemMessages(ctx context.Context, item edge.SavedItem, _ int, mode text.OutputMode) ([]Message, error) {
	msg, err := h.messages.message(ctx, item.ItemID, item.Ts)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	placeholder := func(text string) []Message {
		return []Message{{MsgID: item.Ts, Channel: item.ItemID, Text: text, Time: formatUnixTs(item.DateCreated)}}
	}
	if err != nil {
		h.logger.Warn("Failed to fetch saved message",
			zap.String("channel", item.ItemID),
			zap.String("ts", item.Ts),
			zap.Error(err))
		return placeholder("[message content unavailable: channel access denied]"), nil
	}
	if msg == nil {
		return placeholder("[saved item: message not found in channel history]"), nil
	}
	return h.messages.rows(ctx, []slack.Message{*msg}, item.ItemID, false, mode), nil
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
		return savedTarget{}, invalidArguments("pass either channel_id and timestamp, or item_id, not both")
	case itemID != "" && strings.HasPrefix(itemID, "Sa"):
		return savedTarget{edge.SavedItemReminder, itemID, ""}, nil
	case itemID != "" && strings.HasPrefix(itemID, "F"):
		return savedTarget{edge.SavedItemFile, itemID, ""}, nil
	case itemID != "":
		return savedTarget{}, invalidArguments(fmt.Sprintf("item_id %q is not a custom to-do (Sa...) or a file (F...)", itemID))
	case channelID == "" || ts == "":
		return savedTarget{}, invalidArguments("channel_id and timestamp are required for a message; use item_id for a custom to-do or a file")
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
		return edge.SavedUpdateParams{}, invalidArguments(fmt.Sprintf("mark must be completed or uncompleted, got %q", params.Mark))
	}
	if !validSavedTodoState(params.TodoState) {
		return edge.SavedUpdateParams{}, invalidArguments(fmt.Sprintf("todo_state must be to_do or saved, got %q", params.TodoState))
	}
	if params.Mark == "" && params.TodoState == "" && params.DateDue == "" {
		return edge.SavedUpdateParams{}, invalidArguments("at least one of mark, todo_state or date_due must be provided")
	}
	// Without a state, Slack files a reopened item under Saved. It was completed
	// from To-dos, so that is where it goes back to.
	if params.Mark == "uncompleted" && params.TodoState == "" {
		params.TodoState = "to_do"
	}
	return params, nil
}

func (h *SavedHandler) SavedUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		return edge.SavedAddParams{}, invalidArguments(fmt.Sprintf("todo_state must be to_do or saved, got %q", params.TodoState))
	}
	switch {
	case body != "" && channelID == "" && ts == "" && fileID == "":
		description, err := edge.RichTextJSON(body)
		if err != nil {
			return edge.SavedAddParams{}, err
		}
		params.ItemType, params.Description = edge.SavedItemReminder, description
	case body == "" && channelID != "" && ts != "" && fileID == "":
		params.ItemType, params.ItemID, params.Ts = edge.SavedItemMessage, channelID, ts
	case body == "" && channelID == "" && ts == "" && fileID != "":
		params.ItemType, params.ItemID = edge.SavedItemFile, fileID
	default:
		return edge.SavedAddParams{}, invalidArguments("pass exactly one of: text, channel_id with timestamp, or file_id")
	}
	return params, nil
}

func (h *SavedHandler) SavedAddHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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

// Tools lists the MCP tools this handler serves.
func (h *SavedHandler) Tools() []ToolDef {
	savedTargetChannel := mcp.WithString("channel_id", mcp.Description("Channel or DM ID of a message (Channel column in saved_list). Use with timestamp."))
	savedTargetTimestamp := mcp.WithString("timestamp", mcp.Description("Message timestamp (MsgID column in saved_list). Use with channel_id."))
	savedTargetItemID := mcp.WithString("item_id", mcp.Description("ID of a custom to-do (Sa...) or a file (F...), from the ItemID column in saved_list. Use instead of channel_id and timestamp."))
	savedTodoState := func(description string) mcp.ToolOption {
		return mcp.WithString("todo_state", mcp.Enum("to_do", "saved"), mcp.Description(description))
	}

	return []ToolDef{
		{Name: capability.SavedList, Run: h.SavedListHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("List items from Slack's To-dos tab and Saved section. Returns CSV: with include_messages=true the message rows come first, then a `#saved_items:` line and an item table (Channel,MsgID,DateCreated,DateDue,State,Type,TodoState,ItemID,Text). Channel and MsgID identify a message; ItemID identifies a custom to-do or a file. Needs a Slack browser session (xoxc/xoxd)."),
			mcp.WithString("filter",
				mcp.Description("Which list: 'saved' (Saved section, default), 'todo' (To-dos in progress), 'completed' (completed to-dos), 'archived' (empty since Slack moved archived items to Saved)."),
				mcp.DefaultString("saved"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Maximum number of items to return from this page. Default is 50."),
				mcp.DefaultNumber(50),
			),
			mcp.WithString("cursor",
				mcp.Description(descCursor),
			),
			mcp.WithBoolean("include_messages",
				mcp.Description("If true (default), fetches the actual saved message content. If false, returns metadata only."),
				mcp.DefaultBool(true),
			),
			mcp.WithNumber("max_messages_per_item",
				mcp.Description("Max messages to fetch per saved item (for thread replies). Default is 5."),
				mcp.DefaultNumber(5),
			),
			mcp.WithString("detail",
				mcp.Description(toolDetailDescription),
				mcp.Enum("standard", "full"),
			),
		}},
		{Name: capability.SavedAdd, Run: h.SavedAddHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Add a to-do or saved item: a custom to-do from text, a message, or a file. Pass exactly one of text, channel_id with timestamp, or file_id. Private to the user; nothing is posted."),
			mcp.WithString("text", mcp.Description("Text of a custom to-do.")),
			mcp.WithString("channel_id", mcp.Description("Channel or DM ID of the message to add. Use with timestamp.")),
			mcp.WithString("timestamp", mcp.Description("Timestamp of the message to add. Use with channel_id.")),
			mcp.WithString("file_id", mcp.Description("ID of the file to add (F...).")),
			savedTodoState("List to add to: to_do (To-dos tab, default) or saved (Saved section)."),
			mcp.WithNumber("date_due", mcp.Description("Unix timestamp for a reminder.")),
		}},
		{Name: capability.SavedUpdate, Run: h.SavedUpdateHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Update a to-do or saved item: complete or reopen it, move it between To-dos and Saved, or set or clear its reminder. Identify it with channel_id and timestamp, or item_id. Moving an item to Saved clears its reminder."),
			savedTargetChannel,
			savedTargetTimestamp,
			savedTargetItemID,
			mcp.WithString("mark",
				mcp.Enum("completed", "uncompleted"),
				mcp.Description("completed marks the item done; uncompleted reopens it."),
			),
			savedTodoState("Move the item: to_do (To-dos tab) or saved (Saved section)."),
			mcp.WithNumber("date_due",
				mcp.Description("Unix timestamp for due date/reminder. Set to 0 to clear."),
			),
		}},
		{Name: capability.SavedDelete, Run: h.SavedDeleteHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Remove one item from To-dos or Saved. A message or file stays where it is; a custom to-do is deleted. Identify it with channel_id and timestamp, or item_id."),
			savedTargetChannel,
			savedTargetTimestamp,
			savedTargetItemID,
		}},
		{Name: capability.SavedClearCompleted, Run: h.SavedClearCompletedHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Clear all completed items from To-dos. This is a bulk operation that removes all items with state='completed'."),
		}},
	}
}
