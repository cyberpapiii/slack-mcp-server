package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
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

type ScheduledPageData struct {
	Messages []ScheduledMessage `json:"messages"`
}

type ScheduledCancelData struct {
	Phase         string            `json:"phase"`
	ApprovalToken string            `json:"approval_token,omitempty"`
	ExpiresAt     string            `json:"expires_at,omitempty"`
	Target        *ScheduledMessage `json:"target,omitempty"`
	Cancelled     bool              `json:"cancelled"`
	Outcome       string            `json:"outcome"`
}

type ScheduledPageResult = ToolResult[ScheduledPageData]
type ScheduledCancelResult = ToolResult[ScheduledCancelData]

type ScheduledHandler struct {
	service   ScheduledService
	approvals *approval.Store
	identity  func() provider.ProviderIdentity
	logger    *zap.Logger
}

func NewScheduledHandler(service ScheduledService, approvals *approval.Store, identity func() provider.ProviderIdentity, logger *zap.Logger) *ScheduledHandler {
	return &ScheduledHandler{service: service, approvals: approvals, identity: identity, logger: logger}
}

func (h *ScheduledHandler) List(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ScheduledListHandler called", request)
	limit := request.GetInt("limit", 50)
	if limit < 1 || limit > maxScheduledPageSize {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "limit must be between 1 and 100"}), nil
	}
	page, err := h.service.ListScheduled(ctx, provider.ScheduledListRequest{
		ChannelID: strings.TrimSpace(request.GetString("channel_id", "")),
		Cursor:    strings.TrimSpace(request.GetString("cursor", "")),
		Limit:     limit,
		Oldest:    strings.TrimSpace(request.GetString("oldest", "")),
		Latest:    strings.TrimSpace(request.GetString("latest", "")),
	})
	if err != nil {
		return NewTypedErrorResult(scheduledError(err, false)), nil
	}
	query := strings.ToLower(strings.TrimSpace(request.GetString("text_query", "")))
	data := ScheduledPageData{Messages: make([]ScheduledMessage, 0, len(page.Messages))}
	for _, message := range page.Messages {
		if query != "" && !strings.Contains(strings.ToLower(message.Text), query) {
			continue
		}
		data.Messages = append(data.Messages, scheduledResultMessage(message))
	}
	return NewStructuredResult(data, SlackResultMeta(page.NextCursor, false, ""), fallbackJSON(data)), nil
}

func (h *ScheduledHandler) Cancel(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ScheduledCancelHandler called", request)
	action := strings.TrimSpace(request.GetString("action", ""))
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	scheduledMessageID := strings.TrimSpace(request.GetString("scheduled_message_id", ""))
	if channelID == "" || scheduledMessageID == "" || (action != "prepare" && action != "execute") {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "action, channel_id, and scheduled_message_id are required"}), nil
	}

	target, err := h.findScheduled(ctx, channelID, scheduledMessageID)
	if err != nil {
		return NewTypedErrorResult(scheduledError(err, false)), nil
	}
	binding, err := scheduledBinding(h.identity(), target)
	if err != nil {
		return NewTypedErrorResult(scheduledError(err, false)), nil
	}
	prepared, execute, err := prepareOrExecute(h.approvals, action, request.GetString("approval_token", ""), binding)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	if !execute {
		preview := scheduledResultMessage(target)
		data := ScheduledCancelData{Phase: "prepared", ApprovalToken: prepared.Token, ExpiresAt: prepared.ExpiresAt.Format(time.RFC3339), Target: &preview, Outcome: "awaiting_confirmation"}
		return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
	}
	if err := h.service.CancelScheduled(ctx, channelID, scheduledMessageID); err != nil {
		return NewTypedErrorResult(scheduledError(err, true)), nil
	}
	resultTarget := scheduledResultMessage(target)
	data := ScheduledCancelData{Phase: "executed", Target: &resultTarget, Cancelled: true, Outcome: "cancelled"}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func scheduledBinding(identity provider.ProviderIdentity, target provider.ScheduledMessage) (approval.Binding, error) {
	if identity.TeamID == "" || identity.UserID == "" || identity.ActorType != "user" {
		return approval.Binding{}, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	arguments, err := approval.CanonicalJSON(struct {
		ChannelID          string `json:"channel_id"`
		ScheduledMessageID string `json:"scheduled_message_id"`
	}{ChannelID: target.ChannelID, ScheduledMessageID: target.ScheduledMessageID})
	if err != nil {
		return approval.Binding{}, err
	}
	observed, err := approval.CanonicalJSON(target)
	if err != nil {
		return approval.Binding{}, err
	}
	return approval.Binding{TeamID: identity.TeamID, UserID: identity.UserID, Provider: "local", Tool: "scheduled_message_cancel", Arguments: arguments, ObservedState: observed}, nil
}

func (h *ScheduledHandler) findScheduled(ctx context.Context, channelID, scheduledMessageID string) (provider.ScheduledMessage, error) {
	cursor := ""
	lookupLimitReached := false
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
		lookupLimitReached = pageNumber == maxScheduledLookupPages-1
	}
	if lookupLimitReached {
		return provider.ScheduledMessage{}, &ToolError{Code: "lookup_limit_exceeded", Message: "scheduled-message lookup exceeded 1000 pending messages; narrow the channel or cancel from Slack"}
	}
	return provider.ScheduledMessage{}, &ToolError{Code: "not_found", Message: "scheduled message was not found or is no longer pending"}
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

func fallbackJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func scheduledError(err error, mutationAttempted bool) error {
	var typed *ToolError
	if errors.As(err, &typed) {
		return typed
	}
	var rateLimited *slack.RateLimitedError
	if errors.As(err, &rateLimited) {
		return &ToolError{Code: "rate_limited", Message: err.Error(), Retryable: !mutationAttempted, RetryAfter: rateLimited.RetryAfter, Cause: err}
	}
	var networkError net.Error
	if mutationAttempted && (errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout())) {
		return &ToolError{Code: "outcome_unknown", Message: "Slack may have accepted the cancellation; observe scheduled messages before another attempt", Cause: err}
	}
	var slackError slack.SlackErrorResponse
	if errors.As(err, &slackError) {
		switch slackError.Err {
		case "missing_scope", "not_authed", "invalid_auth", "not_allowed_token_type", "restricted_action":
			return &ToolError{Code: "permission_denied", Message: slackError.Err, Cause: err}
		case "invalid_scheduled_message_id", "message_not_found":
			return &ToolError{Code: "conflict", Message: "scheduled message is no longer pending", Cause: err}
		}
	}
	return &ToolError{Code: "slack_error", Message: err.Error(), Cause: err}
}
