package handler

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
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
	Action string `json:"action"`
	ApprovalPhase
	ChannelID          string                    `json:"channel_id"`
	Timestamp          string                    `json:"timestamp,omitempty"`
	ScheduledMessageID string                    `json:"scheduled_message_id,omitempty"`
	PostAt             string                    `json:"post_at,omitempty"`
	Target             *provider.MessageSnapshot `json:"target,omitempty"`
	Outcome            string                    `json:"outcome"`
}

type MessageMutationResult = ToolResult[MessageMutationData]

type MessageFilesHandler struct {
	service   MessageFilesService
	approvals approvalGate
	logger    *zap.Logger
	now       func() time.Time
}

func NewMessageFilesHandler(service MessageFilesService, approvals *approval.Store, identity func() provider.ProviderIdentity, logger *zap.Logger) *MessageFilesHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MessageFilesHandler{service: service, approvals: approvalGate{store: approvals, identity: identityFunc(identity)}, logger: logger, now: time.Now}
}

func (h *MessageFilesHandler) FilesUpload(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "FilesUploadHandler called", request)
	filename := strings.TrimSpace(request.GetString("filename", ""))
	if filename == "" || filename != filepath.Base(filename) || strings.ContainsAny(filename, "\x00\r\n") {
		return nil, invalidArguments("filename must be a non-empty base filename")
	}
	data, err := readUploadData(request)
	if err != nil {
		return nil, err
	}
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	threadTS := strings.TrimSpace(request.GetString("thread_ts", ""))
	if threadTS != "" && (channelID == "" || !validSlackTimestamp(threadTS)) {
		return nil, invalidArguments("thread_ts requires channel_id and must be a Slack timestamp")
	}
	// Sharing into a channel posts a message, so it answers to the same
	// allowlist as the message lifecycle tools.
	if channelID != "" {
		if err := requireMessageLifecycleTool("files_upload", channelID); err != nil {
			return nil, err
		}
	}
	comment := request.GetString("initial_comment", "")
	if !validMessageText(comment, true) {
		return nil, invalidArguments("initial_comment must be valid UTF-8 and at most 40000 characters")
	}
	uploaded, err := h.service.Upload(ctx, provider.FileUploadRequest{
		Filename: filename, Title: strings.TrimSpace(request.GetString("title", "")), Data: data,
		ChannelID: channelID, InitialComment: comment, ThreadTS: threadTS,
		AltText: strings.TrimSpace(request.GetString("alt_text", "")), SnippetType: strings.TrimSpace(request.GetString("snippet_type", "")),
	})
	if err != nil {
		return nil, TypeError(err, true)
	}
	result := FileUploadData{FileID: uploaded.FileID, Filename: uploaded.Filename, Title: uploaded.Title, ChannelID: uploaded.ChannelID, Outcome: "uploaded"}
	return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
}

func (h *MessageFilesHandler) MessagesSchedule(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "MessagesScheduleHandler called", request)
	channelID, text, _, err := validateMessageMutation(request, false)
	if err != nil {
		return nil, err
	}
	if err := requireMessageLifecycleTool("messages_schedule", channelID); err != nil {
		return nil, err
	}
	postAt, err := parsePostAt(request.GetString("post_at", ""))
	now := h.now()
	if err != nil || !postAt.After(now.Add(5*time.Second)) || postAt.After(now.Add(120*24*time.Hour)) {
		return nil, invalidArguments("post_at must be a Unix timestamp or RFC3339 time between 5 seconds and 120 days in the future")
	}
	threadTS := strings.TrimSpace(request.GetString("thread_ts", ""))
	if threadTS != "" && !validSlackTimestamp(threadTS) {
		return nil, invalidArguments("thread_ts must be a Slack timestamp")
	}
	mutation, err := h.service.Schedule(ctx, channelID, text, postAt, threadTS)
	if err != nil {
		return nil, TypeError(err, true)
	}
	result := MessageMutationData{Action: "schedule", ApprovalPhase: ApprovalPhase{Phase: "executed"}, ChannelID: mutation.ChannelID, ScheduledMessageID: mutation.ScheduledMessageID, PostAt: postAt.UTC().Format(time.RFC3339), Outcome: "scheduled"}
	return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
}

func (h *MessageFilesHandler) MessagesUpdate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "MessagesUpdateHandler called", request)
	channelID, text, timestamp, err := validateMessageMutation(request, true)
	if err != nil {
		return nil, err
	}
	if err := requireMessageLifecycleTool("messages_update", channelID); err != nil {
		return nil, err
	}
	mutation, err := h.service.Update(ctx, channelID, timestamp, text)
	if err != nil {
		return nil, TypeError(err, true)
	}
	result := MessageMutationData{Action: "update", ApprovalPhase: ApprovalPhase{Phase: "executed"}, ChannelID: mutation.ChannelID, Timestamp: mutation.Timestamp, Outcome: "updated"}
	return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
}

func (h *MessageFilesHandler) MessagesDelete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "MessagesDeleteHandler called", request)
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	timestamp := strings.TrimSpace(request.GetString("timestamp", ""))
	if channelID == "" || !validSlackTimestamp(timestamp) {
		return nil, invalidArguments("channel_id and valid timestamp are required")
	}
	prepare, err := approvalAction(request)
	if err != nil {
		return nil, err
	}
	if err := requireMessageLifecycleTool("messages_delete", channelID); err != nil {
		return nil, err
	}
	target, err := h.service.GetMessage(ctx, channelID, timestamp)
	if err != nil {
		return nil, TypeError(err, false)
	}
	arguments := struct {
		ChannelID string `json:"channel_id"`
		Timestamp string `json:"timestamp"`
	}{target.ChannelID, target.Timestamp}
	phase, err := h.approvals.check(request, capability.MessagesDelete, prepare, arguments, target)
	if err != nil {
		return nil, err
	}
	if phase.Prepared() {
		result := MessageMutationData{Action: "delete", ApprovalPhase: phase, ChannelID: channelID, Timestamp: timestamp, Target: &target, Outcome: "awaiting_confirmation"}
		return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
	}
	mutation, err := h.service.Delete(ctx, channelID, timestamp)
	if err != nil {
		return nil, TypeError(err, true)
	}
	result := MessageMutationData{Action: "delete", ApprovalPhase: phase, ChannelID: mutation.ChannelID, Timestamp: mutation.Timestamp, Target: &target, Outcome: "deleted"}
	return NewStructuredResult(result, SlackResultMeta("", false, ""), fallbackJSON(result)), nil
}

func requireMessageLifecycleTool(tool, channelID string) error {
	if !isChannelAllowedForConfig(channelID, os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")) {
		return &ToolError{Code: "permission_denied", Message: tool + " is not allowed for this channel by SLACK_MCP_ADD_MESSAGE_TOOL"}
	}
	return nil
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
		return nil, invalidArguments("exactly one of content_base64 or content is required")
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
		return nil, invalidArguments("file must contain 1 byte to 50 MiB")
	}
	return data, nil
}

func validateMessageMutation(request mcp.CallToolRequest, requireTimestamp bool) (string, string, string, error) {
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	text := request.GetString("text", "")
	timestamp := strings.TrimSpace(request.GetString("timestamp", ""))
	if channelID == "" || !validMessageText(text, false) {
		return "", "", "", invalidArguments("channel_id and valid non-empty text up to 40000 characters are required")
	}
	if requireTimestamp && !validSlackTimestamp(timestamp) {
		return "", "", "", invalidArguments("timestamp must be a Slack timestamp")
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

// Tools lists the MCP tools this handler serves.
func (h *MessageFilesHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.FilesUpload, Run: h.FilesUpload, Options: []mcp.ToolOption{
			mcp.WithDescription("Upload a file to Slack, and post it as a message when shared into a channel. This is also how you send a message with an attachment: pass channel_id plus initial_comment and the file and text post together as one message. Uses Slack's supported external-upload flow."),
			mcp.WithString("filename", mcp.Required(), mcp.Description("File name including extension, e.g. report.csv; no directory path.")),
			mcp.WithString("content_base64", mcp.Description("Base64-encoded file bytes; use this or content, not both. Max 50 MiB decoded.")),
			mcp.WithString("content", mcp.Description("Plain-text file contents; use this or content_base64, not both. Max 50 MiB.")),
			mcp.WithString("title", mcp.Description("Title shown in Slack; defaults to filename.")),
			mcp.WithString("channel_id", mcp.Description("Channel ID (Cxxxxxxxxxx) to share the file into; omit to upload without sharing.")),
			mcp.WithString("initial_comment", mcp.Description("Message text posted with the file when sharing; up to 40000 characters.")),
			mcp.WithString("thread_ts", mcp.Description("Parent message timestamp (e.g. 1712345678.123456) to share into a thread; needs channel_id.")),
			mcp.WithString("alt_text", mcp.Description("Screen-reader description for image files; up to 1000 characters.")),
			mcp.WithString("snippet_type", mcp.Description("Syntax type for text snippets, e.g. python or javascript; unsupported types fail.")),
		}},
		{Name: capability.MessagesSchedule, Run: h.MessagesSchedule, Options: []mcp.ToolOption{
			mcp.WithDescription("Schedule a Slack message for a future time."),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) to post into; names are not resolved.")),
			mcp.WithString("text", mcp.Required(), mcp.Description("Message text, non-empty and up to 40000 characters.")),
			mcp.WithString("post_at", mcp.Required(), mcp.Description("Unix seconds or RFC 3339 time between 5 seconds and 120 days in the future.")),
			mcp.WithString("thread_ts", mcp.Description("Parent message timestamp (e.g. 1712345678.123456) to schedule a thread reply.")),
		}},
		{Name: capability.MessagesUpdate, Run: h.MessagesUpdate, Options: []mcp.ToolOption{
			mcp.WithDescription("Edit a Slack message that the authenticated user owns."),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) containing the message; names are not resolved.")),
			mcp.WithString("timestamp", mcp.Required(), mcp.Description("Slack message timestamp of the message to edit, e.g. 1712345678.123456.")),
			mcp.WithString("text", mcp.Required(), mcp.Description("Replacement message text, non-empty and up to 40000 characters.")),
		}},
		{Name: capability.MessagesDelete, Run: h.MessagesDelete, Options: []mcp.ToolOption{
			mcp.WithOutputSchema[MessageMutationResult](),
			mcp.WithDescription("Preview or delete a Slack message. Prepare first, then execute with the approval token."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) containing the message; names are not resolved.")),
			mcp.WithString("timestamp", mcp.Required(), mcp.Description("Slack message timestamp of the message to delete, e.g. 1712345678.123456.")),
			mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
		}},
	}
}
