package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	scheduledExcerptRunes    = 24
	defaultScheduledPageSize = 50
	maxScheduledPageSize     = 100
	maxScheduledLookupPages  = 10
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
}

func NewScheduledHandler(service ScheduledService, identity func() provider.ProviderIdentity) *ScheduledHandler {
	return &ScheduledHandler{service: service, approvals: newApprovalGate(identity)}
}

func (h *ScheduledHandler) List(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := pageLimit(request, defaultScheduledPageSize, maxScheduledPageSize)
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
			PostAt:      timeText(message.PostAt),
			Text:        message.Text,
		})
	}
	return csvResult("", SlackResultMeta(page.NextCursor, false, ""), &rows)
}

func (h *ScheduledHandler) Cancel(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
	}
	if err := h.service.CancelScheduled(ctx, channelID, scheduledMessageID); err != nil {
		return nil, scheduledError(err, true)
	}
	resultTarget := ScheduledMessage{ScheduledMessageID: scheduledMessageID, ChannelID: channelID}
	data := ScheduledCancelData{ApprovalPhase: phase, Target: &resultTarget, Cancelled: true, Outcome: "cancelled"}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
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
			return provider.ScheduledMessage{}, notFound("scheduled message was not found or is no longer pending")
		}
		cursor = page.NextCursor
	}
	return provider.ScheduledMessage{}, &ToolError{Code: codeLookupLimitExceeded, Message: fmt.Sprintf("scheduled-message lookup exceeded %d pending messages; narrow the channel or cancel from Slack", maxScheduledLookupPages*maxScheduledPageSize)}
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
	if isSlackError(err, "invalid_scheduled_message_id", "message_not_found") {
		return &ToolError{Code: codeConflict, Message: "scheduled message is no longer pending", Cause: err}
	}
	return TypeError(err, mutationAttempted)
}

func (h *ScheduledHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.ScheduledMessagesList, Run: h.List, Options: []mcp.ToolOption{
			mcp.WithDescription("List pending scheduled Slack messages with stable IDs and UTC post times."),
			mcp.WithString("channel_id", mcp.Description("Channel ID (Cxxxxxxxxxx) to filter by; omit for all channels. Names are not resolved.")),
			mcp.WithString("cursor", mcp.Description(descCursor)),
			mcp.WithString("oldest", mcp.Description("Unix seconds; only messages scheduled to post at or after this time.")),
			mcp.WithString("latest", mcp.Description("Unix seconds; only messages scheduled to post at or before this time.")),
			mcp.WithString("text_query", mcp.Description("Case-insensitive substring filter applied to message text after fetching the page.")),
			mcp.WithNumber("limit", mcp.DefaultNumber(defaultScheduledPageSize), mcp.Description(fmt.Sprintf("Maximum messages per page, 1 to %d; defaults to %d.", maxScheduledPageSize, defaultScheduledPageSize))),
		}},
		{Name: capability.ScheduledMessageCancel, Run: h.Cancel, Options: append([]mcp.ToolOption{
			mcp.WithOutputSchema[ScheduledCancelResult](),
			mcp.WithDescription("Preview or execute cancellation of one pending scheduled message. Prepare first, then confirm the exact target before execute."),
		}, approvalOptions(
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) of the scheduled message; names are not resolved.")),
			mcp.WithString("scheduled_message_id", mcp.Required(), mcp.Description("Scheduled message ID from scheduled_messages_list, e.g. Q1234ABCD.")),
		)...)},
	}
}
