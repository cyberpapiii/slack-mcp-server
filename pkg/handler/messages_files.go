package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

const (
	maxUploadBytes  = 50 * 1024 * 1024
	maxMessageRunes = 40000
)

var slackTimestampPattern = regexp.MustCompile(`^[0-9]+\.[0-9]{6}$`)

type MessageFilesService interface {
	Upload(context.Context, provider.FileUploadRequest) (provider.UploadedFile, error)
	Schedule(context.Context, string, string, time.Time, string) (provider.MessageMutation, error)
	Update(context.Context, string, string, string) (provider.MessageMutation, error)
	Delete(context.Context, string, string) (provider.MessageMutation, error)
	GetMessage(context.Context, string, string) (provider.MessageSnapshot, error)
}

type FileUploadData struct {
	FileID    string `json:"file_id"`
	Filename  string `json:"filename"`
	Title     string `json:"title,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	Outcome   string `json:"outcome"`
}

type MessageMutationData struct {
	Action             string                    `json:"action"`
	Phase              string                    `json:"phase"`
	ChannelID          string                    `json:"channel_id"`
	Timestamp          string                    `json:"timestamp,omitempty"`
	ScheduledMessageID string                    `json:"scheduled_message_id,omitempty"`
	PostAt             string                    `json:"post_at,omitempty"`
	Target             *provider.MessageSnapshot `json:"target,omitempty"`
	ApprovalToken      string                    `json:"approval_token,omitempty"`
	ExpiresAt          string                    `json:"expires_at,omitempty"`
	Outcome            string                    `json:"outcome"`
}

type FileUploadResult = ToolResult[FileUploadData]
type MessageMutationResult = ToolResult[MessageMutationData]

type MessageFilesHandler struct {
	service   MessageFilesService
	approvals *approval.Store
	identity  func() provider.ProviderIdentity
	logger    *zap.Logger
	now       func() time.Time
}

func NewMessageFilesHandler(service MessageFilesService, approvals *approval.Store, identity func() provider.ProviderIdentity, logger *zap.Logger) *MessageFilesHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	if approvals == nil {
		approvals = approval.NewStore(5 * time.Minute)
	}
	return &MessageFilesHandler{service: service, approvals: approvals, identity: identity, logger: logger, now: time.Now}
}

func (h *MessageFilesHandler) FilesUpload(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "FilesUploadHandler called", request)
	filename := strings.TrimSpace(request.GetString("filename", ""))
	if filename == "" || filename != filepath.Base(filename) || strings.ContainsAny(filename, "\x00\r\n") {
		return messageFilesInvalid("filename must be a non-empty base filename"), nil
	}
	data, err := readUploadData(request)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	threadTS := strings.TrimSpace(request.GetString("thread_ts", ""))
	if threadTS != "" && (channelID == "" || !validSlackTimestamp(threadTS)) {
		return messageFilesInvalid("thread_ts requires channel_id and must be a Slack timestamp"), nil
	}
	comment := request.GetString("initial_comment", "")
	if !validMessageText(comment, true) {
		return messageFilesInvalid("initial_comment must be valid UTF-8 and at most 40000 characters"), nil
	}
	uploaded, err := h.service.Upload(ctx, provider.FileUploadRequest{
		Filename: filename, Title: strings.TrimSpace(request.GetString("title", "")), Data: data,
		ChannelID: channelID, InitialComment: comment, ThreadTS: threadTS,
		AltText: strings.TrimSpace(request.GetString("alt_text", "")), SnippetType: strings.TrimSpace(request.GetString("snippet_type", "")),
	})
	if err != nil {
		return NewTypedErrorResult(messageFilesError(err, true, "file upload")), nil
	}
	result := FileUploadData{FileID: uploaded.FileID, Filename: uploaded.Filename, Title: uploaded.Title, ChannelID: uploaded.ChannelID, Outcome: "uploaded"}
	return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
}

func (h *MessageFilesHandler) MessagesSchedule(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "MessagesScheduleHandler called", request)
	channelID, text, _, err := validateMessageMutation(request, false)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	if err := requireMessageLifecycleTool("messages_schedule", channelID); err != nil {
		return NewTypedErrorResult(err), nil
	}
	postAt, err := parsePostAt(request.GetString("post_at", ""))
	now := h.now()
	if err != nil || !postAt.After(now.Add(5*time.Second)) || postAt.After(now.Add(120*24*time.Hour)) {
		return messageFilesInvalid("post_at must be a Unix timestamp or RFC3339 time between 5 seconds and 120 days in the future"), nil
	}
	threadTS := strings.TrimSpace(request.GetString("thread_ts", ""))
	if threadTS != "" && !validSlackTimestamp(threadTS) {
		return messageFilesInvalid("thread_ts must be a Slack timestamp"), nil
	}
	mutation, err := h.service.Schedule(ctx, channelID, text, postAt, threadTS)
	if err != nil {
		return NewTypedErrorResult(messageFilesError(err, true, "message scheduling")), nil
	}
	result := MessageMutationData{Action: "schedule", Phase: "executed", ChannelID: mutation.ChannelID, ScheduledMessageID: mutation.ScheduledMessageID, PostAt: postAt.UTC().Format(time.RFC3339), Outcome: "scheduled"}
	return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
}

func (h *MessageFilesHandler) MessagesUpdate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "MessagesUpdateHandler called", request)
	channelID, text, timestamp, err := validateMessageMutation(request, true)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	if err := requireMessageLifecycleTool("messages_update", channelID); err != nil {
		return NewTypedErrorResult(err), nil
	}
	mutation, err := h.service.Update(ctx, channelID, timestamp, text)
	if err != nil {
		return NewTypedErrorResult(messageFilesError(err, true, "message update")), nil
	}
	result := MessageMutationData{Action: "update", Phase: "executed", ChannelID: mutation.ChannelID, Timestamp: mutation.Timestamp, Outcome: "updated"}
	return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
}

func (h *MessageFilesHandler) MessagesDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "MessagesDeleteHandler called", request)
	action := strings.TrimSpace(request.GetString("action", ""))
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	timestamp := strings.TrimSpace(request.GetString("timestamp", ""))
	if (action != "prepare" && action != "execute") || channelID == "" || !validSlackTimestamp(timestamp) {
		return messageFilesInvalid("action must be prepare or execute; channel_id and valid timestamp are required"), nil
	}
	if err := requireMessageLifecycleTool("messages_delete", channelID); err != nil {
		return NewTypedErrorResult(err), nil
	}
	target, err := h.service.GetMessage(ctx, channelID, timestamp)
	if err != nil {
		return NewTypedErrorResult(messageFilesError(err, false, "message lookup")), nil
	}
	binding, err := deleteMessageBinding(h.currentIdentity(), target)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	prepared, execute, err := prepareOrExecute(h.approvals, action, request.GetString("approval_token", ""), binding)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	if !execute {
		result := MessageMutationData{Action: "delete", Phase: "prepared", ChannelID: channelID, Timestamp: timestamp, Target: &target, ApprovalToken: prepared.Token, ExpiresAt: prepared.ExpiresAt.Format(time.RFC3339), Outcome: "awaiting_confirmation"}
		return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
	}
	mutation, err := h.service.Delete(ctx, channelID, timestamp)
	if err != nil {
		return NewTypedErrorResult(messageFilesError(err, true, "message deletion")), nil
	}
	result := MessageMutationData{Action: "delete", Phase: "executed", ChannelID: mutation.ChannelID, Timestamp: mutation.Timestamp, Target: &target, Outcome: "deleted"}
	return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
}

func requireMessageLifecycleTool(tool, channelID string) error {
	if !isChannelAllowedForConfig(channelID, os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")) {
		return &ToolError{Code: "permission_denied", Message: tool + " is not allowed for this channel by SLACK_MCP_ADD_MESSAGE_TOOL"}
	}
	return nil
}

func (h *MessageFilesHandler) currentIdentity() provider.ProviderIdentity {
	if h.identity == nil {
		return provider.ProviderIdentity{}
	}
	return h.identity()
}

func readUploadData(request mcp.CallToolRequest) ([]byte, error) {
	encoded := strings.TrimSpace(request.GetString("content_base64", ""))
	content := request.GetString("content", "")
	count := 0
	if encoded != "" {
		count++
	}
	if content != "" {
		count++
	}
	if count != 1 {
		return nil, &ToolError{Code: "invalid_arguments", Message: "exactly one of content_base64 or content is required"}
	}
	var data []byte
	var err error
	switch {
	case encoded != "":
		data, err = base64.StdEncoding.DecodeString(encoded)
	case content != "":
		data = []byte(content)
	}
	if err != nil {
		return nil, &ToolError{Code: "invalid_arguments", Message: "content_base64 could not be decoded", Cause: err}
	}
	if len(data) == 0 || len(data) > maxUploadBytes {
		return nil, &ToolError{Code: "invalid_arguments", Message: "file must contain 1 byte to 50 MiB"}
	}
	return data, nil
}

func validateMessageMutation(request mcp.CallToolRequest, requireTimestamp bool) (string, string, string, error) {
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	text := request.GetString("text", "")
	timestamp := strings.TrimSpace(request.GetString("timestamp", ""))
	if channelID == "" || !validMessageText(text, false) {
		return "", "", "", &ToolError{Code: "invalid_arguments", Message: "channel_id and valid non-empty text up to 40000 characters are required"}
	}
	if requireTimestamp && !validSlackTimestamp(timestamp) {
		return "", "", "", &ToolError{Code: "invalid_arguments", Message: "timestamp must be a Slack timestamp"}
	}
	return channelID, text, timestamp, nil
}

func validMessageText(text string, allowEmpty bool) bool {
	return utf8.ValidString(text) && (allowEmpty || strings.TrimSpace(text) != "") && utf8.RuneCountInString(text) <= maxMessageRunes
}

func validSlackTimestamp(timestamp string) bool { return slackTimestampPattern.MatchString(timestamp) }

func parsePostAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(unix, 0).UTC(), nil
	}
	return time.Parse(time.RFC3339, value)
}

func deleteMessageBinding(identity provider.ProviderIdentity, target provider.MessageSnapshot) (approval.Binding, error) {
	if identity.TeamID == "" || identity.UserID == "" || identity.ActorType != "user" {
		return approval.Binding{}, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	arguments, err := approval.CanonicalJSON(struct {
		ChannelID string `json:"channel_id"`
		Timestamp string `json:"timestamp"`
	}{target.ChannelID, target.Timestamp})
	if err != nil {
		return approval.Binding{}, err
	}
	observed, err := approval.CanonicalJSON(target)
	if err != nil {
		return approval.Binding{}, err
	}
	return approval.Binding{TeamID: identity.TeamID, UserID: identity.UserID, Provider: "local", Tool: "messages_delete", Arguments: arguments, ObservedState: observed}, nil
}

func messageFilesInvalid(message string) *mcp.CallToolResult {
	return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: message})
}

func messageFilesError(err error, mutationAttempted bool, operation string) error {
	var typed *ToolError
	if errors.As(err, &typed) {
		return typed
	}
	var rateLimited *slack.RateLimitedError
	if errors.As(err, &rateLimited) {
		return &ToolError{Code: "rate_limited", Message: err.Error(), Retryable: !mutationAttempted, RetryAfter: rateLimited.RetryAfter, Cause: err}
	}
	if mutationAttempted && isAmbiguousMutationError(err) {
		return &ToolError{Code: "outcome_unknown", Message: fmt.Sprintf("Slack may have accepted %s; observe Slack state before another attempt", operation), Cause: err}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return &ToolError{Code: "network_error", Message: err.Error(), Retryable: !mutationAttempted, Cause: err}
	}
	var slackError slack.SlackErrorResponse
	if errors.As(err, &slackError) {
		switch slackError.Err {
		case "missing_scope", "not_authed", "invalid_auth", "not_allowed_token_type", "restricted_action", "cant_update_message", "cant_delete_message":
			return &ToolError{Code: "permission_denied", Message: slackError.Err, Cause: err}
		case "message_not_found", "channel_not_found":
			return &ToolError{Code: "not_found", Message: slackError.Err, Cause: err}
		case "time_in_past", "time_too_far":
			return &ToolError{Code: "invalid_arguments", Message: slackError.Err, Cause: err}
		}
	}
	if strings.Contains(err.Error(), "message_not_found") {
		return &ToolError{Code: "not_found", Message: "message was not found", Cause: err}
	}
	return &ToolError{Code: "slack_error", Message: err.Error(), Cause: err}
}
