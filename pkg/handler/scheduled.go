package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

const (
	scheduledExcerptRunes   = 24
	maxScheduledPageSize    = 100
	maxScheduledLookupPages = 10
)

type ScheduledService interface {
	ListScheduled(context.Context, provider.ScheduledListRequest) (provider.ScheduledPage, error)
	CancelScheduled(context.Context, string, string) error
}

type ScheduledMessage struct {
	ScheduledMessageID string `json:"scheduled_message_id"`
	ChannelID          string `json:"channel_id"`
	TextExcerpt        string `json:"text_excerpt"`
	PostAt             string `json:"post_at"`
}

// ScheduledRow is one scheduled_messages_list CSV row. ScheduledID and
// Channel are the two arguments scheduled_message_cancel needs.
type ScheduledRow struct {
	ScheduledID string `csv:"ScheduledID"`
	Channel     string `csv:"Channel"`
	PostAt      string `csv:"PostAt"`
	Text        string `csv:"Text"`
}

type ScheduledCancelData struct {
	ApprovalPhase
	Target    *ScheduledMessage `json:"target,omitempty"`
	Cancelled bool              `json:"cancelled"`
	Outcome   string            `json:"outcome"`
}

type ScheduledCancelResult = ToolResult[ScheduledCancelData]

type ScheduledHandler struct {
	service   ScheduledService
	approvals approvalGate
	logger    *zap.Logger
}

func NewScheduledHandler(service ScheduledService, approvals *approval.Store, identity func() provider.ProviderIdentity, logger *zap.Logger) *ScheduledHandler {
	return &ScheduledHandler{service: service, approvals: approvalGate{store: approvals, identity: identityFunc(identity)}, logger: logger}
}

func (h *ScheduledHandler) List(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ScheduledListHandler called", request)
	limit := pageLimit(request, 50, maxScheduledPageSize)
	page, err := h.service.ListScheduled(ctx, provider.ScheduledListRequest{
		ChannelID: strings.TrimSpace(request.GetString("channel_id", "")),
		Cursor:    strings.TrimSpace(request.GetString("cursor", "")),
		Limit:     limit,
		Oldest:    strings.TrimSpace(request.GetString("oldest", "")),
		Latest:    strings.TrimSpace(request.GetString("latest", "")),
	})
	if err != nil {
		return nil, scheduledError(err, false)
	}
	query := strings.ToLower(strings.TrimSpace(request.GetString("text_query", "")))
	rows := make([]ScheduledRow, 0, len(page.Messages))
	for _, message := range page.Messages {
		if query != "" && !strings.Contains(strings.ToLower(message.Text), query) {
			continue
		}
		rows = append(rows, ScheduledRow{
			ScheduledID: message.ScheduledMessageID,
			Channel:     message.ChannelID,
			PostAt:      message.PostAt.UTC().Format(time.RFC3339),
			Text:        message.Text,
		})
	}
	csvBytes, err := gocsv.MarshalBytes(&rows)
	if err != nil {
		return nil, err
	}
	return NewCSVResult("", SlackResultMeta(page.NextCursor, false, ""), string(csvBytes)), nil
}

func (h *ScheduledHandler) Cancel(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ScheduledCancelHandler called", request)
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	scheduledMessageID := strings.TrimSpace(request.GetString("scheduled_message_id", ""))
	if channelID == "" || scheduledMessageID == "" {
		return nil, invalidArguments("channel_id and scheduled_message_id are required")
	}
	prepare, err := approvalAction(request)
	if err != nil {
		return nil, err
	}
	// The token binds team, user, channel and message ID, and Slack's own
	// not-found answer covers a message that was posted or cancelled in between,
	// so only prepare pays for the lookup.
	var target provider.ScheduledMessage
	if prepare {
		if target, err = h.findScheduled(ctx, channelID, scheduledMessageID); err != nil {
			return nil, scheduledError(err, false)
		}
	}
	arguments := struct {
		ChannelID          string `json:"channel_id"`
		ScheduledMessageID string `json:"scheduled_message_id"`
	}{channelID, scheduledMessageID}
	phase, err := h.approvals.check(request, capability.ScheduledMessageCancel, prepare, arguments, nil)
	if err != nil {
		return nil, err
	}
	if phase.Prepared() {
		preview := scheduledResultMessage(target)
		data := ScheduledCancelData{ApprovalPhase: phase, Target: &preview, Outcome: "awaiting_confirmation"}
		return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
	}
	if err := h.service.CancelScheduled(ctx, channelID, scheduledMessageID); err != nil {
		return nil, scheduledError(err, true)
	}
	resultTarget := ScheduledMessage{ScheduledMessageID: scheduledMessageID, ChannelID: channelID}
	data := ScheduledCancelData{ApprovalPhase: phase, Target: &resultTarget, Cancelled: true, Outcome: "cancelled"}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *ScheduledHandler) findScheduled(ctx context.Context, channelID, scheduledMessageID string) (provider.ScheduledMessage, error) {
	cursor := ""
	for pageNumber := 0; pageNumber < maxScheduledLookupPages; pageNumber++ {
		page, err := h.service.ListScheduled(ctx, provider.ScheduledListRequest{ChannelID: channelID, Cursor: cursor, Limit: maxScheduledPageSize})
		if err != nil {
			return provider.ScheduledMessage{}, err
		}
		for _, message := range page.Messages {
			if message.ChannelID == channelID && message.ScheduledMessageID == scheduledMessageID {
				return message, nil
			}
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return provider.ScheduledMessage{}, &ToolError{Code: "not_found", Message: "scheduled message was not found or is no longer pending"}
		}
		cursor = page.NextCursor
	}
	return provider.ScheduledMessage{}, &ToolError{Code: "lookup_limit_exceeded", Message: "scheduled-message lookup exceeded 1000 pending messages; narrow the channel or cancel from Slack"}
}

func scheduledResultMessage(message provider.ScheduledMessage) ScheduledMessage {
	return ScheduledMessage{
		ScheduledMessageID: message.ScheduledMessageID,
		ChannelID:          message.ChannelID,
		TextExcerpt:        excerpt(message.Text, scheduledExcerptRunes),
		PostAt:             message.PostAt.UTC().Format(time.RFC3339),
	}
}

func excerpt(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// scheduledError reads a missing scheduled message as a conflict: the message
// was pending when listed and has since posted or been cancelled.
func scheduledError(err error, mutationAttempted bool) error {
	var slackError slack.SlackErrorResponse
	if errors.As(err, &slackError) && (slackError.Err == "invalid_scheduled_message_id" || slackError.Err == "message_not_found") {
		return &ToolError{Code: "conflict", Message: "scheduled message is no longer pending", Cause: err}
	}
	return TypeError(err, mutationAttempted)
}

// Tools lists the MCP tools this handler serves.
func (h *ScheduledHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.ScheduledMessagesList, Run: h.List, Options: []mcp.ToolOption{
			mcp.WithDescription("List pending scheduled Slack messages with stable IDs and UTC post times."),
			mcp.WithString("channel_id", mcp.Description("Channel ID (Cxxxxxxxxxx) to filter by; omit for all channels. Names are not resolved.")),
			mcp.WithString("cursor", mcp.Description(descCursor)),
			mcp.WithString("oldest", mcp.Description("Unix seconds; only messages scheduled to post at or after this time.")),
			mcp.WithString("latest", mcp.Description("Unix seconds; only messages scheduled to post at or before this time.")),
			mcp.WithString("text_query", mcp.Description("Case-insensitive substring filter applied to message text after fetching the page.")),
			mcp.WithNumber("limit", mcp.DefaultNumber(50), mcp.Description("Maximum messages per page, 1 to 100; defaults to 50.")),
		}},
		{Name: capability.ScheduledMessageCancel, Run: h.Cancel, Options: []mcp.ToolOption{
			mcp.WithOutputSchema[ScheduledCancelResult](),
			mcp.WithDescription("Preview or execute cancellation of one pending scheduled message. Prepare first, then confirm the exact target before execute."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) of the scheduled message; names are not resolved.")),
			mcp.WithString("scheduled_message_id", mcp.Required(), mcp.Description("Scheduled message ID from scheduled_messages_list, e.g. Q1234ABCD.")),
			mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
		}},
	}
}
