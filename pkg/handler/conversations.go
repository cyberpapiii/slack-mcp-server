package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/server/auth"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/slack-go/slack"
	slackGoUtil "github.com/takara2314/slack-go-util"
	"go.uber.org/zap"
)

const (
	defaultConversationsNumericLimit    = 50
	defaultConversationsExpressionLimit = "1d"
	maxFileSizeBytes                    = 5 * 1024 * 1024 // 5MB limit

	// Search `limit` bounds. Default and max are both 100, matching the tool
	// schema's DefaultNumber(100) and its "between 1 and 100" description.
	defaultSearchMessagesLimit = 100
	maxSearchMessagesLimit     = 100
)

var validFilterKeys = map[string]struct{}{
	"is":     {},
	"in":     {},
	"from":   {},
	"with":   {},
	"before": {},
	"after":  {},
	"on":     {},
	"during": {},
}

type Message struct {
	MsgID         string `json:"msgID"`
	UserID        string `json:"userID"`
	UserName      string `json:"userUser"`
	RealName      string `json:"realName"`
	Channel       string `json:"channelID"`
	ThreadTs      string `json:"ThreadTs"`
	Text          string `json:"text"`
	Time          string `json:"time"`
	Permalink     string `json:"permalink,omitempty"`
	Reactions     string `json:"reactions,omitempty"`
	BotName       string `json:"botName,omitempty"`
	FileCount     int    `json:"fileCount,omitempty"`
	AttachmentIDs string `json:"attachmentIDs,omitempty"`
	HasMedia      bool   `json:"hasMedia,omitempty"`
	Cursor        string `json:"cursor"`
}

// CompactMessage is the default agent-oriented CSV shape: readable columns plus the
// IDs needed for reactions, thread replies, and attachment downloads. Drops only
// redundant or bulky fields (UserID when User is set, Permalink URLs, FileCount).
type CompactMessage struct {
	User          string `csv:"User"`
	Channel       string `csv:"Channel"`
	Text          string `csv:"Text"`
	Time          string `csv:"Time"`
	MsgID         string `csv:"MsgID"`
	ThreadTs      string `csv:"ThreadTs,omitempty"`
	Reactions     string `csv:"Reactions,omitempty"`
	AttachmentIDs string `csv:"AttachmentIDs,omitempty"`
	Files         string `csv:"Files,omitempty"`
	Cursor        string `csv:"Cursor,omitempty"`
}

type User struct {
	UserID   string `json:"userID"`
	UserName string `json:"userName"`
	RealName string `json:"realName"`
}

type UserSearchResult struct {
	UserID      string `csv:"UserID"`
	UserName    string `csv:"UserName"`
	RealName    string `csv:"RealName"`
	DisplayName string `csv:"DisplayName"`
	Email       string `csv:"Email"`
	Title       string `csv:"Title"`
	DMChannelID string `csv:"DMChannelID"`
}

type conversationParams struct {
	channel  string
	limit    int
	oldest   string
	latest   string
	cursor   string
	activity bool
}

type searchParams struct {
	query string
	limit int
	page  int
}

type addMessageParams struct {
	channel     string
	threadTs    string
	text        string
	contentType string
	blocks      []slack.Block
}

type addReactionParams struct {
	channel   string
	timestamp string
	emoji     string
}

type filesGetParams struct {
	fileID string
}

type filesListParams struct {
	channel string
	user    string
	types   string
	limit   int
	cursor  string
}

type FileListResult struct {
	FileID     string `csv:"FileID"`
	Name       string `csv:"Name"`
	Title      string `csv:"Title"`
	Mimetype   string `csv:"Mimetype"`
	Filetype   string `csv:"Filetype"`
	PrettyType string `csv:"PrettyType"`
	Size       int    `csv:"Size"`
	UserID     string `csv:"UserID"`
	Created    string `csv:"Created"`
	Permalink  string `csv:"Permalink"`
	Cursor     string `csv:"Cursor"`
}

type usersSearchParams struct {
	query string
	limit int
}

type unreadsParams struct {
	includeMessages       bool
	channelTypes          string
	maxChannels           int
	maxMessagesPerChannel int
	mentionsOnly          bool
	includeMuted          bool
	mutedChannels         map[string]bool // populated at runtime from Slack prefs
	mutedUnavailable      bool            // true when muted channels could not be fetched (e.g. xoxp token)
}

type markParams struct {
	channel string
	ts      string
}

type reactionRow struct {
	Emoji string `csv:"Emoji"`
	Count int    `csv:"Count"`
	Users string `csv:"Users"`
}
type ConversationsHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
}

func NewConversationsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger) *ConversationsHandler {
	return &ConversationsHandler{
		apiProvider: apiProvider,
		logger:      logger,
	}
}

// sendProgress sends an MCP progress notification if a progress token is present in the request.
func sendProgress(ctx context.Context, request mcp.CallToolRequest, current, total int, message string) {
	if request.Params.Meta == nil || request.Params.Meta.ProgressToken == nil {
		return
	}
	srv := server.ServerFromContext(ctx)
	if srv == nil {
		return
	}
	totalF := float64(total)
	srv.SendNotificationToClient(ctx, "notifications/progress", map[string]any{
		"progressToken": request.Params.Meta.ProgressToken,
		"progress":      float64(current),
		"total":         totalF,
		"message":       message,
	})
}

// UsersResource streams a CSV of all users
func (ch *ConversationsHandler) UsersResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	logResourceCall(ch.logger, "UsersResource called", request)

	// authentication
	if authenticated, err := auth.IsAuthenticated(ctx, ch.apiProvider.ServerTransport(), ch.logger); !authenticated {
		ch.logger.Error("Authentication failed for users resource", zap.Error(err))
		return nil, err
	}

	// provider readiness
	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	// Slack auth test
	ar, err := ch.apiProvider.Slack().AuthTest()
	if err != nil {
		ch.logger.Error("Slack AuthTest failed", zap.Error(err))
		return nil, err
	}

	ws, err := text.Workspace(ar.URL)
	if err != nil {
		ch.logger.Error("Failed to parse workspace from URL",
			zap.String("url", ar.URL),
			zap.Error(err),
		)
		return nil, fmt.Errorf("failed to parse workspace from URL: %v", err)
	}

	// collect users
	usersMaps := ch.apiProvider.ProvideUsersMap()
	users := usersMaps.Users
	usersList := make([]User, 0, len(users))
	for _, user := range users {
		usersList = append(usersList, User{
			UserID:   user.ID,
			UserName: user.Name,
			RealName: user.RealName,
		})
	}

	// marshal CSV
	csvBytes, err := gocsv.MarshalBytes(&usersList)
	if err != nil {
		ch.logger.Error("Failed to marshal users to CSV", zap.Error(err))
		return nil, err
	}

	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "slack://" + ws + "/users",
			MIMEType: "text/csv",
			Text:     string(csvBytes),
		},
	}, nil
}

// ConversationsAddMessageHandler posts a message and returns a confirmation
func (ch *ConversationsHandler) ConversationsAddMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsAddMessageHandler called", request)

	// provider readiness
	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolAddMessage(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse add-message params", zap.Error(err))
		return nil, err
	}

	var options []slack.MsgOption
	if params.threadTs != "" {
		options = append(options, slack.MsgOptionTS(params.threadTs))
	}

	if params.blocks != nil {
		// Raw blocks provided: use them directly. If text is also provided, it
		// serves as the notification/fallback text.
		options = append(options, slack.MsgOptionBlocks(params.blocks...))
		if params.text != "" {
			options = append(options, slack.MsgOptionText(params.text, false))
		}
	} else {
		switch params.contentType {
		case "text/plain":
			options = append(options, slack.MsgOptionDisableMarkdown())
			options = append(options, slack.MsgOptionText(params.text, false))
		case "text/markdown":
			blocks, err := slackGoUtil.ConvertMarkdownTextToBlocks(params.text)
			if err != nil {
				ch.logger.Warn("Markdown parsing error", zap.Error(err))
				options = append(options, slack.MsgOptionDisableMarkdown())
				options = append(options, slack.MsgOptionText(params.text, false))
			} else {
				options = append(options, slack.MsgOptionBlocks(blocks...))
			}
		default:
			return nil, errors.New("content_type must be either 'text/plain' or 'text/markdown'")
		}
	}

	unfurlOpt := os.Getenv("SLACK_MCP_ADD_MESSAGE_UNFURLING")
	if text.IsUnfurlingEnabled(params.text, unfurlOpt, ch.logger) {
		options = append(options, slack.MsgOptionEnableLinkUnfurl())
	} else {
		options = append(options, slack.MsgOptionDisableLinkUnfurl())
		options = append(options, slack.MsgOptionDisableMediaUnfurl())
	}

	ch.logger.Debug("Posting Slack message",
		zap.String("channel", params.channel),
		zap.String("thread_ts", params.threadTs),
		zap.String("content_type", params.contentType),
	)
	respChannel, respTimestamp, err := ch.apiProvider.Slack().PostMessageContext(ctx, params.channel, options...)
	if err != nil {
		ch.logger.Error("Slack PostMessageContext failed", zap.Error(err))
		return nil, err
	}

	toolConfig := os.Getenv("SLACK_MCP_ADD_MESSAGE_MARK")
	if toolConfig == "1" || toolConfig == "true" || toolConfig == "yes" {
		err := ch.apiProvider.Slack().MarkConversationContext(ctx, params.channel, respTimestamp)
		if err != nil {
			ch.logger.Error("Slack MarkConversationContext failed", zap.Error(err))
			return nil, err
		}
	}

	if params.threadTs != "" {
		return mcp.NewToolResultText(fmt.Sprintf("Successfully posted message to channel %s in thread %s (ts=%s)", respChannel, params.threadTs, respTimestamp)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Successfully posted message to channel %s (ts=%s)", respChannel, respTimestamp)), nil
}

// ConversationsDraftMessageHandler validates and formats a message without sending it.
// Returns a preview of what the message would look like, allowing the user to review
// before sending via conversations_add_message.
func (ch *ConversationsHandler) ConversationsDraftMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsDraftMessageHandler called", request)

	// provider readiness
	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolDraftMessage(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse draft-message params", zap.Error(err))
		return nil, err
	}

	// Validate content type and check markdown parsing, matching conversations_add_message behavior.
	var formatStatus string

	switch params.contentType {
	case "text/plain":
		formatStatus = "plain_text"
	case "text/markdown":
		blocks, err := slackGoUtil.ConvertMarkdownTextToBlocks(params.text)
		if err != nil {
			ch.logger.Warn("Markdown parsing error", zap.Error(err))
			formatStatus = "plain_text (markdown parse failed, will send as plain text)"
		} else {
			formatStatus = fmt.Sprintf("markdown (%d block(s))", len(blocks))
		}
	default:
		return nil, errors.New("content_type must be either 'text/plain' or 'text/markdown'")
	}

	sendability := checkSendStatus(params.channel)

	// Build the draft preview response.
	// Use labeled sections instead of delimiter lines to avoid ambiguity
	// when the message text itself contains delimiter-like patterns.
	preview := fmt.Sprintf("[Draft message preview]\n"+
		"Channel: %s\n"+
		"Thread: %s\n"+
		"Format: %s\n"+
		"Send status: %s\n\n"+
		"[Message text]\n"+
		"%s\n\n"+
		"[End of draft]\n"+
		"To send this message, use conversations_add_message with the same parameters.",
		params.channel,
		formatThreadTs(params.threadTs),
		formatStatus,
		sendability,
		params.text,
	)

	return mcp.NewToolResultText(preview), nil
}

// ReactionsAddHandler adds an emoji reaction to a message
func (ch *ConversationsHandler) ReactionsAddHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ReactionsAddHandler called", request)

	// provider readiness
	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolReaction(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse add-reaction params", zap.Error(err))
		return nil, err
	}

	itemRef := slack.ItemRef{
		Channel:   params.channel,
		Timestamp: params.timestamp,
	}

	ch.logger.Debug("Adding reaction to Slack message",
		zap.String("channel", params.channel),
		zap.String("timestamp", params.timestamp),
		zap.String("emoji", params.emoji),
	)

	err = ch.apiProvider.Slack().AddReactionContext(ctx, params.emoji, itemRef)
	if err != nil {
		ch.logger.Error("Slack AddReactionContext failed", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully added :%s: reaction to message %s in channel %s", params.emoji, params.timestamp, params.channel)), nil
}

// ReactionsRemoveHandler removes an emoji reaction from a message
func (ch *ConversationsHandler) ReactionsRemoveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ReactionsRemoveHandler called", request)

	// provider readiness
	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolReaction(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse remove-reaction params", zap.Error(err))
		return nil, err
	}

	itemRef := slack.ItemRef{
		Channel:   params.channel,
		Timestamp: params.timestamp,
	}

	ch.logger.Debug("Removing reaction from Slack message",
		zap.String("channel", params.channel),
		zap.String("timestamp", params.timestamp),
		zap.String("emoji", params.emoji),
	)

	err = ch.apiProvider.Slack().RemoveReactionContext(ctx, params.emoji, itemRef)
	if err != nil {
		ch.logger.Error("Slack RemoveReactionContext failed", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully removed :%s: reaction from message %s in channel %s", params.emoji, params.timestamp, params.channel)), nil
}

// ReactionsGetHandler returns detailed reaction data (including user IDs) for a specific message.
// Uses conversations.replies which works for top-level messages, thread parents, and thread replies
// alike. Requires only channels:history scope, no additional reactions:read permission needed.
func (ch *ConversationsHandler) ReactionsGetHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ReactionsGetHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	rawChannel := request.GetString("channel_id", "")
	if rawChannel == "" {
		return nil, errors.New("channel_id is required")
	}
	channel, err := ch.resolveChannelID(ctx, rawChannel)
	if err != nil {
		ch.logger.Error("Channel not found", zap.String("channel", rawChannel), zap.Error(err))
		return nil, err
	}

	timestamp := request.GetString("timestamp", "")
	if timestamp == "" {
		return nil, errors.New("timestamp is required")
	}

	ch.logger.Debug("Fetching reactions for message",
		zap.String("channel", channel),
		zap.String("timestamp", timestamp),
	)

	msg, err := ch.fetchMessageForReactions(ctx, channel, timestamp)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return mcp.NewToolResultText("No message found at the specified timestamp"), nil
	}

	if len(msg.Reactions) == 0 {
		return mcp.NewToolResultText("No reactions on this message"), nil
	}

	var rows []reactionRow
	for _, r := range msg.Reactions {
		rows = append(rows, reactionRow{
			Emoji: r.Name,
			Count: r.Count,
			Users: strings.Join(r.Users, ";"),
		})
	}

	var buf bytes.Buffer
	if err := gocsv.Marshal(rows, &buf); err != nil {
		return nil, fmt.Errorf("failed to marshal reactions: %w", err)
	}

	return mcp.NewToolResultText(buf.String()), nil
}

// fetchMessageForReactions retrieves a single message by timestamp using conversations.replies.
// This works for top-level messages, thread parents, and thread replies alike — no additional
// scopes beyond channels:history required.
//
// Note: Slack's reactions.get API is purpose-built for this but requires the reactions:read scope.
// We use conversations.replies instead to avoid introducing a new scope requirement.
func (ch *ConversationsHandler) fetchMessageForReactions(ctx context.Context, channel, timestamp string) (*slack.Message, error) {
	msgs, _, _, err := ch.apiProvider.Slack().GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: timestamp,
		Limit:     1,
		Inclusive: true,
	})
	if err != nil {
		ch.logger.Error("Failed to fetch message for reactions", zap.Error(err))
		return nil, fmt.Errorf("failed to fetch message: %w", err)
	}

	if len(msgs) == 0 {
		return nil, nil
	}

	return &msgs[0], nil
}

func (ch *ConversationsHandler) UsersSearchHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "UsersSearchHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolUsersSearch(request)
	if err != nil {
		ch.logger.Error("Failed to parse users-search params", zap.Error(err))
		return nil, err
	}

	ch.logger.Debug("Searching for users",
		zap.String("query", params.query),
		zap.Int("limit", params.limit),
	)

	users, err := ch.apiProvider.SearchUsers(ctx, params.query, params.limit)
	if err != nil {
		ch.logger.Error("UsersSearch failed", zap.Error(err))
		return nil, fmt.Errorf("users search failed: %w", err)
	}

	channelsMap := ch.apiProvider.ProvideChannelsMaps()

	results := make([]UserSearchResult, 0, len(users))
	for _, user := range users {
		if user.Deleted {
			continue
		}

		dmChannelID := ""
		for _, ch := range channelsMap.Channels {
			if ch.IsIM && ch.User == user.ID {
				dmChannelID = ch.ID
				break
			}
		}

		results = append(results, UserSearchResult{
			UserID:      user.ID,
			UserName:    user.Name,
			RealName:    user.RealName,
			DisplayName: user.Profile.DisplayName,
			Email:       user.Profile.Email,
			Title:       user.Profile.Title,
			DMChannelID: dmChannelID,
		})
	}

	if len(results) == 0 {
		return mcp.NewToolResultText("No users found matching the query."), nil
	}

	csvBytes, err := gocsv.MarshalBytes(&results)
	if err != nil {
		ch.logger.Error("Failed to marshal users to CSV", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(string(csvBytes)), nil
}

func (ch *ConversationsHandler) FilesGetHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "FilesGetHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolFilesGet(request)
	if err != nil {
		ch.logger.Error("Failed to parse attachment_get_data params", zap.Error(err))
		return nil, err
	}

	fileInfo, _, _, err := ch.apiProvider.Slack().GetFileInfoContext(ctx, params.fileID, 0, 0)
	if err != nil {
		ch.logger.Error("Slack GetFileInfoContext failed", zap.Error(err))
		return nil, err
	}

	if fileInfo.Size > maxFileSizeBytes {
		return nil, fmt.Errorf("file size %d bytes exceeds maximum allowed size of %d bytes", fileInfo.Size, maxFileSizeBytes)
	}

	var buf bytes.Buffer
	downloadURL := fileInfo.URLPrivateDownload
	if downloadURL == "" {
		downloadURL = fileInfo.URLPrivate
	}
	if downloadURL == "" {
		return nil, errors.New("file has no downloadable URL")
	}

	err = ch.apiProvider.Slack().GetFileContext(ctx, downloadURL, &buf)
	if err != nil {
		ch.logger.Error("Slack GetFileContext failed", zap.Error(err))
		return nil, err
	}

	content := buf.Bytes()

	// For image files, return as native MCP image content so the client
	// can render them directly without base64-in-JSON overflow.
	if isImageMimetype(fileInfo.Mimetype) {
		imageData := base64.StdEncoding.EncodeToString(content)
		metadata, err := marshalFileMetadata(fileMetadataPayload{
			FileID:   fileInfo.ID,
			Filename: fileInfo.Name,
			Mimetype: fileInfo.Mimetype,
			Size:     len(content),
		})
		if err != nil {
			ch.logger.Error("Failed to marshal attachment metadata", zap.Error(err))
			return nil, err
		}
		return mcp.NewToolResultImage(metadata, imageData, fileInfo.Mimetype), nil
	}

	encoding := "none"
	var contentStr string

	if isTextMimetype(fileInfo.Mimetype) {
		contentStr = string(content)
	} else {
		contentStr = base64.StdEncoding.EncodeToString(content)
		encoding = "base64"
	}

	result, err := marshalFileResult(fileResultPayload{
		FileID:   fileInfo.ID,
		Filename: fileInfo.Name,
		Mimetype: fileInfo.Mimetype,
		Size:     len(content),
		Encoding: encoding,
		Content:  contentStr,
	})
	if err != nil {
		ch.logger.Error("Failed to marshal attachment result", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(result), nil
}

// fileMetadataPayload is the 4-key shape emitted alongside native MCP image
// content by attachment_get_data.
type fileMetadataPayload struct {
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	Mimetype string `json:"mimetype"`
	Size     int    `json:"size"`
}

// fileResultPayload is the 6-key shape emitted for text and binary files by
// attachment_get_data. Encoding is always present, including when it is "none".
type fileResultPayload struct {
	FileID   string `json:"file_id"`
	Filename string `json:"filename"`
	Mimetype string `json:"mimetype"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

func marshalFileMetadata(p fileMetadataPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func marshalFileResult(p fileResultPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (ch *ConversationsHandler) parseParamsToolFilesList(request mcp.CallToolRequest) (*filesListParams, error) {
	params := &filesListParams{
		channel: request.GetString("channel_id", ""),
		user:    request.GetString("user_id", ""),
		types:   request.GetString("types", ""),
		cursor:  request.GetString("cursor", ""),
		limit:   50,
	}

	if limitStr := request.GetString("limit", ""); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			params.limit = v
		}
	}

	return params, nil
}

func (ch *ConversationsHandler) FilesListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "FilesListHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolFilesList(request)
	if err != nil {
		ch.logger.Error("Failed to parse files_list params", zap.Error(err))
		return nil, err
	}

	listParams := slack.ListFilesParameters{
		Channel: params.channel,
		User:    params.user,
		Types:   params.types,
		Limit:   params.limit,
		Cursor:  params.cursor,
	}

	files, nextPage, err := ch.apiProvider.Slack().ListFilesContext(ctx, listParams)
	if err != nil {
		ch.logger.Error("Slack ListFilesContext failed", zap.Error(err))
		return nil, err
	}

	if len(files) == 0 {
		return mcp.NewToolResultText("No files found."), nil
	}

	nextCursor := ""
	if nextPage != nil {
		nextCursor = nextPage.Cursor
	}

	results := make([]FileListResult, 0, len(files))
	for i, f := range files {
		cursor := ""
		if i == len(files)-1 {
			cursor = nextCursor
		}
		results = append(results, FileListResult{
			FileID:     f.ID,
			Name:       f.Name,
			Title:      f.Title,
			Mimetype:   f.Mimetype,
			Filetype:   f.Filetype,
			PrettyType: f.PrettyType,
			Size:       f.Size,
			UserID:     f.User,
			Created:    f.Created.Time().Format(time.RFC3339),
			Permalink:  f.Permalink,
			Cursor:     cursor,
		})
	}

	csvBytes, err := gocsv.MarshalBytes(&results)
	if err != nil {
		ch.logger.Error("Failed to marshal files to CSV", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(string(csvBytes)), nil
}

func isImageMimetype(mimetype string) bool {
	return strings.HasPrefix(mimetype, "image/")
}

func isTextMimetype(mimetype string) bool {
	if strings.HasPrefix(mimetype, "text/") {
		return true
	}
	textMimetypes := map[string]bool{
		"application/json":       true,
		"application/xml":        true,
		"application/javascript": true,
		"application/x-yaml":     true,
		"application/x-sh":       true,
	}
	return textMimetypes[mimetype]
}

// ConversationsHistoryHandler streams conversation history as CSV
func (ch *ConversationsHandler) ConversationsHistoryHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsHistoryHandler called", request)

	params, err := ch.parseParamsToolConversations(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse history params", zap.Error(err))
		return nil, err
	}
	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}
	ch.logger.Debug("History params parsed",
		zap.String("channel", params.channel),
		zap.Int("limit", params.limit),
		zap.String("oldest", params.oldest),
		zap.String("latest", params.latest),
		zap.Bool("include_activity", params.activity),
	)

	historyParams := slack.GetConversationHistoryParameters{
		ChannelID: params.channel,
		Limit:     params.limit,
		Oldest:    params.oldest,
		Latest:    params.latest,
		Cursor:    params.cursor,
		Inclusive: false,
	}
	history, err := ch.apiProvider.Slack().GetConversationHistoryContext(ctx, &historyParams)
	if err != nil {
		ch.logger.Error("GetConversationHistoryContext failed", zap.Error(err))
		return nil, err
	}

	ch.logger.Debug("Fetched conversation history", zap.Int("message_count", len(history.Messages)))

	messages := ch.convertMessagesFromHistory(ctx, history.Messages, params.channel, params.activity, mode)

	if len(messages) > 0 && history.HasMore {
		messages[len(messages)-1].Cursor = history.ResponseMetaData.NextCursor
	}
	return marshalMessagesToCSV(messages, renderOptions{mode: mode, workspaceURL: ch.apiProvider.WorkspaceURL()})
}

// ConversationsRepliesHandler streams thread replies as CSV
func (ch *ConversationsHandler) ConversationsRepliesHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsRepliesHandler called", request)

	params, err := ch.parseParamsToolConversations(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse replies params", zap.Error(err))
		return nil, err
	}
	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}
	threadTs := request.GetString("thread_ts", "")
	if threadTs == "" {
		ch.logger.Error("thread_ts not provided for replies", zap.String("thread_ts", threadTs))
		return nil, errors.New("thread_ts must be a string")
	}

	repliesParams := slack.GetConversationRepliesParameters{
		ChannelID: params.channel,
		Timestamp: threadTs,
		Limit:     params.limit,
		Oldest:    params.oldest,
		Latest:    params.latest,
		Cursor:    params.cursor,
		Inclusive: false,
	}
	replies, hasMore, nextCursor, err := ch.apiProvider.Slack().GetConversationRepliesContext(ctx, &repliesParams)
	if err != nil {
		ch.logger.Error("GetConversationRepliesContext failed", zap.Error(err))
		return nil, err
	}
	ch.logger.Debug("Fetched conversation replies", zap.Int("count", len(replies)))

	messages := ch.convertMessagesFromHistory(ctx, replies, params.channel, params.activity, mode)
	if len(messages) > 0 && hasMore {
		messages[len(messages)-1].Cursor = nextCursor
	}
	return marshalMessagesToCSV(messages, renderOptions{mode: mode, workspaceURL: ch.apiProvider.WorkspaceURL()})
}

func (ch *ConversationsHandler) ConversationsSearchHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsSearchHandler called", request)

	params, err := ch.parseParamsToolSearch(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse search params", zap.Error(err))
		return nil, err
	}
	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}
	ch.logger.Debug("Search params parsed", zap.String("query", params.query), zap.Int("limit", params.limit), zap.Int("page", params.page))

	searchParams := slack.SearchParameters{
		Sort:          slack.DEFAULT_SEARCH_SORT,
		SortDirection: slack.DEFAULT_SEARCH_SORT_DIR,
		Highlight:     false,
		Count:         params.limit,
		Page:          params.page,
	}

	rl := limiter.Tier2.Limiter()
	messagesRes, err := limiter.CallWithRetry(ctx, rl, 2, slackRetryAfter, func() (*slack.SearchMessages, error) {
		msgs, _, err := ch.apiProvider.Slack().SearchContext(ctx, params.query, searchParams)
		return msgs, err
	})
	if err != nil {
		ch.logger.Error("Slack SearchContext failed", zap.Error(err))
		return nil, err
	}
	ch.logger.Debug("Search completed", zap.Int("matches", len(messagesRes.Matches)))

	messages := ch.convertMessagesFromSearch(ctx, messagesRes.Matches, mode)
	if len(messages) > 0 && messagesRes.Pagination.Page < messagesRes.Pagination.PageCount {
		nextCursor := fmt.Sprintf("page:%d", messagesRes.Pagination.Page+1)
		messages[len(messages)-1].Cursor = base64.StdEncoding.EncodeToString([]byte(nextCursor))
	}
	return marshalMessagesToCSV(messages, renderOptions{mode: mode, workspaceURL: ch.apiProvider.WorkspaceURL()})
}

// UnreadChannel represents a channel with unread messages
type UnreadChannel struct {
	ChannelID   string `json:"channelID"`
	ChannelName string `json:"channelName"`
	ChannelType string `json:"channelType"` // "dm", "group_dm", "partner", "internal"
	UnreadCount int    `json:"unreadCount"`
	LastRead    string `json:"lastRead"`
	Latest      string `json:"latest"`
}

// ConversationsUnreadsHandler returns unread messages across all channels
func (ch *ConversationsHandler) ConversationsUnreadsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsUnreadsHandler called", request)

	params := ch.parseParamsToolUnreads(request)

	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}

	// Fetch muted channels unless the caller wants them included
	if !params.includeMuted {
		mutedChannels, err := ch.apiProvider.Slack().GetMutedChannels(ctx)
		if err != nil {
			ch.logger.Warn("Failed to fetch muted channels, proceeding without mute filter", zap.Error(err))
			params.mutedUnavailable = true
		} else if len(mutedChannels) > 0 {
			params.mutedChannels = mutedChannels
			ch.logger.Debug("Loaded muted channels", zap.Int("count", len(mutedChannels)))
		}
	}

	// Route based on token type:
	// - xoxc/xoxd (browser session): use fast client.counts API
	// - xoxp (OAuth user): fall back to conversations.info/history approach
	// - xoxb (bot): not supported — unreads is a user-level concept
	if ch.apiProvider.IsOAuth() {
		if ch.apiProvider.IsBotToken() {
			return nil, fmt.Errorf(
				"conversations_unreads requires a user token (xoxp) or browser session tokens (xoxc/xoxd); " +
					"bot tokens (xoxb) do not support unread tracking",
			)
		}
		ch.logger.Info("OAuth token detected, using conversations.info fallback for unreads")
		return ch.getUnreadsViaConversationsInfo(ctx, request, params, mode)
	}

	counts, err := ch.apiProvider.Slack().ClientCounts(ctx)
	if err != nil {
		if errors.Is(err, provider.ErrBrowserSessionUnavailable) {
			ch.logger.Warn("Browser session unavailable for client.counts, falling back to OAuth-compatible unread scan", zap.Error(err))
			return ch.getUnreadsViaConversationsInfo(ctx, request, params, mode)
		}
		ch.logger.Error("ClientCounts failed", zap.Error(err))
		return nil, fmt.Errorf("failed to get client counts: %v", err)
	}

	return ch.processClientCountsResponse(ctx, request, params, counts, mode)
}

func (ch *ConversationsHandler) processClientCountsResponse(ctx context.Context, request mcp.CallToolRequest, params *unreadsParams, counts edge.ClientCountsResponse, mode text.OutputMode) (*mcp.CallToolResult, error) {
	ch.logger.Debug("Got counts data",
		zap.Int("channels", len(counts.Channels)),
		zap.Int("mpims", len(counts.MPIMs)),
		zap.Int("ims", len(counts.IMs)))

	// Get users map and channels map for resolving names
	usersMap := ch.apiProvider.ProvideUsersMap()
	channelsMaps := ch.apiProvider.ProvideChannelsMaps()

	unreadChannels := ch.collectUnreadChannels(params, counts, usersMap, channelsMaps)

	ch.logger.Debug("Found unread channels", zap.Int("count", len(unreadChannels)))

	if !ch.backfillUnreadCounts(ctx, request, ch.apiProvider.Slack(), params, unreadChannels) {
		return mcp.NewToolResultError("cancelled"), nil
	}

	// If not including messages, just return channel summary
	if !params.includeMessages {
		return ch.marshalUnreadChannelsToCSV(unreadChannels)
	}

	// Fetch messages for each unread channel
	var allMessages []Message

	for i := range unreadChannels {
		select {
		case <-ctx.Done():
			return mcp.NewToolResultError("cancelled"), nil
		default:
		}
		sendProgress(ctx, request, i+1, len(unreadChannels), fmt.Sprintf("Fetching messages: channel %d of %d", i+1, len(unreadChannels)))

		historyParams := slack.GetConversationHistoryParameters{
			ChannelID: unreadChannels[i].ChannelID,
			Oldest:    unreadChannels[i].LastRead,
			Limit:     params.maxMessagesPerChannel,
			Inclusive: false,
		}

		history, err := ch.apiProvider.Slack().GetConversationHistoryContext(ctx, &historyParams)
		if err != nil {
			ch.logger.Warn("Failed to get history for channel",
				zap.String("channel", unreadChannels[i].ChannelID),
				zap.Error(err))
			// The fetch failed, so we can't count: conservatively report 1 unread
			// since HasUnreads was true, rather than rendering "0 unread".
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, err)
			continue
		}

		// Update unread count from actual message count. A zero-row window (e.g.
		// no usable last-read timestamp) still reports 1, since HasUnreads was true.
		unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, len(history.Messages), nil)

		// Convert messages
		channelMessages := ch.convertMessagesFromHistory(ctx, history.Messages, unreadChannels[i].ChannelName, false, mode)
		allMessages = append(allMessages, channelMessages...)
	}

	ch.logger.Debug("Fetched unread messages", zap.Int("total", len(allMessages)))

	return marshalMessagesToCSV(allMessages, renderOptions{mode: mode, workspaceURL: ch.apiProvider.WorkspaceURL()})
}

// unreadCountFromHistory reports the UnreadCount to record for a channel after
// its conversations.history fetch. `current` is the count carried over from
// client.counts (the MentionCount), `msgCount` the number of rows returned, and
// a non-nil `fetchErr` means the fetch failed and msgCount is meaningless.
// Both zero-guards are conservative: client.counts said HasUnreads, so the tool
// never renders "0 unread".
func unreadCountFromHistory(current, msgCount int, fetchErr error) int {
	if fetchErr != nil {
		if current == 0 {
			return 1
		}
		return current
	}
	if msgCount == 0 {
		return 1
	}
	return msgCount
}

// historyFetcher is the single method of provider.SlackAPI that the unread-count
// backfill needs. It exists so tests can substitute a call-counting fake;
// production passes ch.apiProvider.Slack().
type historyFetcher interface {
	GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
}

// backfillUnreadCounts fills in UnreadCount for channels that client.counts
// reported as unread with MentionCount==0. It mutates unreadChannels in place
// and reports false if the context was cancelled mid-flight.
func (ch *ConversationsHandler) backfillUnreadCounts(ctx context.Context, request mcp.CallToolRequest, api historyFetcher, params *unreadsParams, unreadChannels []UnreadChannel) bool {
	if params.includeMessages {
		// The message-fetch loop issues the same conversations.history call over
		// the same window and overwrites UnreadCount anyway, so backfilling first
		// would double the API calls for no observable difference.
		return true
	}

	// Backfill real unread counts for channels where client.counts only gave us
	// HasUnreads=true but MentionCount=0 (unreads without @mentions).
	// DMs and group DMs don't need this — every DM message counts as a mention.
	//
	// NOTE: conversations.info does not return unread_count with browser tokens
	// (xoxc/xoxd), so we use conversations.history to count messages since the
	// last-read timestamp. Limit kept small (20) for speed; the exact count
	// matters less than surfacing that unreads exist.
	const backfillLimit = 20
	backfilled := 0
	for i := range unreadChannels {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		sendProgress(ctx, request, i+1, len(unreadChannels), fmt.Sprintf("Backfilling unread counts: channel %d of %d", i+1, len(unreadChannels)))

		if unreadChannels[i].UnreadCount > 0 {
			continue // MentionCount was positive, good enough
		}
		if unreadChannels[i].LastRead == "" {
			// No last-read timestamp means we can't bound the query.
			// Conservatively report 1 unread since HasUnreads was true.
			unreadChannels[i].UnreadCount = 1
			backfilled++
			continue
		}
		history, err := api.GetConversationHistoryContext(ctx,
			&slack.GetConversationHistoryParameters{
				ChannelID: unreadChannels[i].ChannelID,
				Oldest:    unreadChannels[i].LastRead,
				Limit:     backfillLimit,
				Inclusive: false,
			})
		if err != nil {
			ch.logger.Debug("Failed to backfill unread count",
				zap.String("channel", unreadChannels[i].ChannelID),
				zap.Error(err))
			continue
		}
		if len(history.Messages) > 0 {
			unreadChannels[i].UnreadCount = len(history.Messages)
		}
		backfilled++
	}
	if backfilled > 0 {
		ch.logger.Debug("Backfilled unread counts via conversations.history",
			zap.Int("backfilled", backfilled))
	}
	return true
}

// collectUnreadChannels turns a client.counts snapshot into the sorted, limited
// list of unread channels. Pure: no API calls — the caller supplies the cache
// snapshots.
func (ch *ConversationsHandler) collectUnreadChannels(params *unreadsParams, counts edge.ClientCountsResponse, usersMap *provider.UsersCache, channelsMaps *provider.ChannelsCache) []UnreadChannel {
	// Collect channels with unreads
	var unreadChannels []UnreadChannel

	// Process regular channels (public, private)
	for _, snap := range counts.Channels {
		if !snap.HasUnreads {
			continue
		}

		// Skip muted channels (unless include_muted is set)
		if params.mutedChannels[snap.ID] {
			continue
		}

		// Priority Inbox: skip channels without @mentions
		if params.mentionsOnly && snap.MentionCount == 0 {
			continue
		}

		// Get channel info from cache to determine type and name
		channelName := snap.ID
		channelType := "internal"
		if cached, ok := channelsMaps.Channels[snap.ID]; ok {
			// The cached name may already have # prefix, so handle both cases
			name := cached.Name
			if strings.HasPrefix(name, "#") {
				channelName = name
			} else {
				channelName = "#" + name
			}
			// Check if it's a partner/external channel using Slack's metadata
			if cached.IsExtShared {
				channelType = "partner"
			}
		}

		// Filter by requested channel types
		if params.channelTypes != "all" && channelType != params.channelTypes {
			continue
		}

		unreadChannels = append(unreadChannels, UnreadChannel{
			ChannelID:   snap.ID,
			ChannelName: channelName,
			ChannelType: channelType,
			UnreadCount: snap.MentionCount,
			LastRead:    snap.LastRead.SlackString(),
			Latest:      snap.Latest.SlackString(),
		})
	}

	// Process MPIMs (group DMs)
	for _, snap := range counts.MPIMs {
		if !snap.HasUnreads {
			continue
		}

		// Skip muted channels (unless include_muted is set)
		if params.mutedChannels[snap.ID] {
			continue
		}

		// Priority Inbox: skip channels without @mentions
		if params.mentionsOnly && snap.MentionCount == 0 {
			continue
		}

		// Filter by requested channel types
		if params.channelTypes != "all" && params.channelTypes != "group_dm" {
			continue
		}

		channelName := snap.ID
		if cached, ok := channelsMaps.Channels[snap.ID]; ok {
			channelName = cached.Name
		}

		unreadChannels = append(unreadChannels, UnreadChannel{
			ChannelID:   snap.ID,
			ChannelName: channelName,
			ChannelType: "group_dm",
			UnreadCount: snap.MentionCount,
			LastRead:    snap.LastRead.SlackString(),
			Latest:      snap.Latest.SlackString(),
		})
	}

	// Process IMs (direct messages)
	for _, snap := range counts.IMs {
		if !snap.HasUnreads {
			continue
		}

		// Skip muted channels (unless include_muted is set)
		if params.mutedChannels[snap.ID] {
			continue
		}

		// Priority Inbox: skip channels without @mentions
		if params.mentionsOnly && snap.MentionCount == 0 {
			continue
		}

		// Filter by requested channel types
		if params.channelTypes != "all" && params.channelTypes != "dm" {
			continue
		}

		// Get display name for DM from channel cache or users
		channelName := snap.ID
		if cached, ok := channelsMaps.Channels[snap.ID]; ok {
			if cached.User != "" {
				if u, ok := usersMap.Users[cached.User]; ok {
					channelName = "@" + u.Name
				} else {
					channelName = "@" + cached.User
				}
			}
		}

		unreadChannels = append(unreadChannels, UnreadChannel{
			ChannelID:   snap.ID,
			ChannelName: channelName,
			ChannelType: "dm",
			UnreadCount: snap.MentionCount,
			LastRead:    snap.LastRead.SlackString(),
			Latest:      snap.Latest.SlackString(),
		})
	}

	// Sort by priority: DMs > partner channels > internal
	ch.sortChannelsByPriority(unreadChannels)

	// Limit channels. The maxChannels > 0 guard is a backstop: a non-positive
	// value would otherwise slice with a negative bound and panic.
	if params.maxChannels > 0 && len(unreadChannels) > params.maxChannels {
		unreadChannels = unreadChannels[:params.maxChannels]
	}

	return unreadChannels
}

func (ch *ConversationsHandler) getUnreadsViaConversationsInfo(ctx context.Context, request mcp.CallToolRequest, params *unreadsParams, mode text.OutputMode) (*mcp.CallToolResult, error) {
	usersMap := ch.apiProvider.ProvideUsersMap()

	// Define channel type groups in priority order.
	// users.conversations returns channels in creation order (NOT by activity),
	// so we scan each type independently with its own budget/scan cap.
	type typeGroup struct {
		slackTypes  []string // users.conversations "types" parameter values
		channelType string   // our internal type label
		budget      int      // max unread channels to return for this group
		isDM        bool     // DMs get unread_count directly from conversations.info
	}

	// Allocate budgets: DMs get the full max_channels allocation,
	// MPIMs and channels each get half. Each type group scans up to
	// budget*2 channels to find unreads (see scanTypeGroupForUnreads).
	dmBudget := params.maxChannels
	mpimBudget := params.maxChannels / 2
	channelBudget := params.maxChannels / 2

	groups := []typeGroup{
		{slackTypes: []string{"im"}, channelType: "dm", budget: dmBudget, isDM: true},
		{slackTypes: []string{"mpim"}, channelType: "group_dm", budget: mpimBudget, isDM: false},
		{slackTypes: []string{"public_channel", "private_channel"}, channelType: "", budget: channelBudget, isDM: false},
	}

	var unreadChannels []UnreadChannel
	totalAPIcalls := 0
	totalScanned := 0
	totalRateLimited := 0

	for gi, group := range groups {
		select {
		case <-ctx.Done():
			return mcp.NewToolResultError("cancelled"), nil
		default:
		}
		sendProgress(ctx, request, gi+1, len(groups), fmt.Sprintf("Scanning channel type group %d of %d", gi+1, len(groups)))

		// Apply channel_types filter: skip groups that don't match the requested filter.
		if params.channelTypes != "all" {
			match := false
			switch params.channelTypes {
			case "dm":
				match = group.channelType == "dm"
			case "group_dm":
				match = group.channelType == "group_dm"
			case "internal", "partner":
				// Both internal and partner channels come from public/private channel types
				match = group.channelType == ""
			}
			if !match {
				continue
			}
		}

		found, apiCalls, scanned, rateLimited := ch.scanTypeGroupForUnreads(ctx, params, usersMap, group.slackTypes, group.channelType, group.budget, group.isDM)
		unreadChannels = append(unreadChannels, found...)
		totalAPIcalls += apiCalls
		totalScanned += scanned
		totalRateLimited += rateLimited
	}

	ch.sortChannelsByPriority(unreadChannels)

	ch.logger.Info("Found unread channels via xoxp fallback",
		zap.Int("count", len(unreadChannels)),
		zap.Int("scanned", totalScanned),
		zap.Int("apiCalls", totalAPIcalls),
		zap.Int("rateLimited", totalRateLimited))

	// Prepend a note about xoxp limitations so the LLM understands
	// these results may be partial.
	mutedNote := ""
	if params.mutedUnavailable && !params.includeMuted {
		mutedNote = "Muted channel filtering is unavailable with xoxp tokens; results may include muted channels. "
	}
	rateLimitNote := ""
	if totalRateLimited > 0 {
		rateLimitNote = fmt.Sprintf("WARNING: %d channels were skipped due to Slack rate limiting (even after retries) — results are degraded. Try again after a brief cooldown. ", totalRateLimited)
	}
	xoxpNote := fmt.Sprintf(
		"[xoxp token: scanned %d channels (%d API calls), found %d with unreads. %s%s"+
			"Results may be incomplete — increase max_channels for broader coverage, "+
			"or use xoxc/xoxd browser tokens for complete results.]\n\n",
		totalScanned, totalAPIcalls, len(unreadChannels), rateLimitNote, mutedNote,
	)

	if !params.includeMessages {
		result, err := ch.marshalUnreadChannelsToCSV(unreadChannels)
		if err != nil {
			return nil, err
		}
		// Prepend the xoxp note to the CSV output
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(mcp.TextContent); ok {
				tc.Text = xoxpNote + tc.Text
				result.Content[0] = tc
			}
		}
		return result, nil
	}

	// Fetch actual unread messages for each discovered channel
	rl := limiter.Tier3.Limiter()
	var allMessages []Message
	for i, uc := range unreadChannels {
		select {
		case <-ctx.Done():
			return mcp.NewToolResultError("cancelled"), nil
		default:
		}
		sendProgress(ctx, request, i+1, len(unreadChannels), fmt.Sprintf("Fetching messages: channel %d of %d", i+1, len(unreadChannels)))

		historyParams := slack.GetConversationHistoryParameters{
			ChannelID: uc.ChannelID,
			Oldest:    uc.LastRead,
			Limit:     params.maxMessagesPerChannel,
			Inclusive: false,
		}

		history, err := limiter.CallWithRetry(ctx, rl, 2, slackRetryAfter, func() (*slack.GetConversationHistoryResponse, error) {
			return ch.apiProvider.Slack().GetConversationHistoryContext(ctx, &historyParams)
		})
		if err != nil {
			ch.logger.Warn("Failed to get history for channel",
				zap.String("channel", uc.ChannelID),
				zap.Error(err))
			continue
		}

		channelMessages := ch.convertMessagesFromHistory(ctx, history.Messages, uc.ChannelName, false, mode)
		allMessages = append(allMessages, channelMessages...)
	}

	ch.logger.Debug("Fetched unread messages via fallback", zap.Int("total", len(allMessages)))

	result, err := marshalMessagesToCSV(allMessages, renderOptions{mode: mode, workspaceURL: ch.apiProvider.WorkspaceURL()})
	if err != nil {
		return nil, err
	}
	// Prepend the xoxp note to the messages CSV output
	if len(result.Content) > 0 {
		if tc, ok := result.Content[0].(mcp.TextContent); ok {
			tc.Text = xoxpNote + tc.Text
			result.Content[0] = tc
		}
	}
	return result, nil
}

// slackRetryAfter checks if an error is a Slack rate limit error and returns
// the retry-after duration. Returns 0 for non-rate-limit errors.
// Used as the retryAfter callback for limiter.CallWithRetry.
func slackRetryAfter(err error) time.Duration {
	var rle *slack.RateLimitedError
	if errors.As(err, &rle) {
		return rle.RetryAfter
	}
	return 0
}

// scanTypeGroupForUnreads fetches channels of the given Slack types via users.conversations
// and checks each for unreads via conversations.info.
//
// users.conversations returns only channels the calling user is a member of, which is
// significantly more efficient than conversations.list (excludes non-member public channels
// and closed DMs that cannot have unreads).
//
// Neither endpoint sorts by activity — channels are returned in creation order.
// Unread channels can appear anywhere in the list, so we scan up to a capped number
// per type group to keep API call count bounded (each channel costs 1-2 API calls).
//
// Returns the discovered unread channels, total API calls made, channels scanned,
// and the number of channels skipped due to rate limiting.
func (ch *ConversationsHandler) scanTypeGroupForUnreads(
	ctx context.Context,
	params *unreadsParams,
	usersMap *provider.UsersCache,
	slackTypes []string,
	groupType string,
	budget int,
	isDM bool,
) ([]UnreadChannel, int, int, int) {
	var unreadChannels []UnreadChannel
	apiCalls := 0
	scanned := 0
	rateLimited := 0

	// Proactive rate limiter for conversations.info (Tier 3: ~50 req/min).
	// Without this, the scan fires requests as fast as the network allows,
	// triggering cascading 429s that silently skip channels.
	rl := limiter.Tier3.Limiter()

	// users.conversations returns channels in creation order (not by activity),
	// so unread channels can appear anywhere. We scan up to budget*2 channels
	// per type group — a trade-off between coverage and API call count.
	// Each channel costs 1 API call (DMs: conversations.info returns
	// unread_count directly) or up to 2 calls (non-DMs: conversations.info
	// for last_read + conversations.history to detect new messages).
	//
	// With the default max_channels=50, this gives:
	//   DMs:      scan up to 100 channels (~100 API calls)
	//   MPIMs:    scan up to  50 channels (~100 API calls)
	//   Channels: scan up to  50 channels (~100 API calls)
	//   Total:    ~300 API calls max (vs ~2000+ unbounded)
	maxScan := budget * 2
	if maxScan < 50 {
		maxScan = 50
	}

	cursor := ""
	for {
		if len(unreadChannels) >= budget {
			break
		}
		if maxScan > 0 && scanned >= maxScan {
			break
		}

		// Fetch a page of channels via users.conversations (only channels the
		// calling user is a member of). This is significantly more efficient than
		// conversations.list for xoxp tokens because it excludes non-member public
		// channels and closed DMs — channels that cannot have unreads.
		// Empirically tested: 37% fewer channels on a 2700-channel workspace
		// (1724 vs 2737), with 85% reduction for public channels (156 vs 1046).
		// Both methods miss the same set of archived channels, so zero real
		// unreads are lost by this switch.
		userConvParams := &slack.GetConversationsForUserParameters{
			Types:           slackTypes,
			Limit:           200,
			ExcludeArchived: true,
			Cursor:          cursor,
		}

		channels, nextCursor, err := ch.apiProvider.Slack().GetConversationsForUserContext(ctx, userConvParams)
		apiCalls++
		if err != nil {
			ch.logger.Warn("Failed to list conversations for type group",
				zap.Strings("types", slackTypes),
				zap.Error(err))
			break
		}

		if len(channels) == 0 {
			break
		}

		for _, channel := range channels {
			select {
			case <-ctx.Done():
				return unreadChannels, apiCalls, scanned, rateLimited
			default:
			}

			if len(unreadChannels) >= budget {
				break
			}
			if maxScan > 0 && scanned >= maxScan {
				break
			}

			// Skip muted channels
			if params.mutedChannels[channel.ID] {
				continue
			}

			scanned++

			// Get full channel info including last_read and latest.
			// Uses rate limiting + retry to avoid cascading 429 errors
			// that silently skip channels (see: slack-go does NOT auto-retry
			// on *RateLimitedError for standard client methods).
			info, err := limiter.CallWithRetry(ctx, rl, 2, slackRetryAfter, func() (*slack.Channel, error) {
				return ch.apiProvider.Slack().GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{
					ChannelID: channel.ID,
				})
			})
			apiCalls++
			if err != nil {
				var rle *slack.RateLimitedError
				if errors.As(err, &rle) {
					rateLimited++
					ch.logger.Warn("Rate limited on conversation info (retries exhausted)",
						zap.String("channel", channel.ID),
						zap.Duration("retryAfter", rle.RetryAfter))
				} else {
					ch.logger.Debug("Failed to get conversation info",
						zap.String("channel", channel.ID),
						zap.Error(err))
				}
				continue
			}

			// Determine if channel has unreads
			hasUnreads := false
			unreadCount := 0

			if isDM {
				// DMs: conversations.info returns unread_count directly
				if info.UnreadCount > 0 {
					hasUnreads = true
					unreadCount = info.UnreadCount
				}
			} else {
				// Channels/groups/MPIMs: conversations.info does NOT return
				// unread_count or latest message for xoxp tokens. Use last_read
				// with conversations.history to detect unreads.
				//
				// last_read values:
				//   ""                    → never visited (skip — noise on large workspaces)
				//   "0000000000.000000"   → never read (Slack sentinel, treat as "never visited")
				//   "<timestamp>"         → last read position
				lastRead := info.LastRead
				neverVisited := lastRead == "" || lastRead == "0000000000.000000"

				if neverVisited && groupType != "group_dm" {
					// For regular channels: skip if never visited/read. On large
					// workspaces this includes hundreds of auto-joined or dormant
					// channels — skip to avoid flooding results with stale unreads.
					// MPIMs are always intentional (someone added you), so check them.
					continue
				}

				if neverVisited {
					lastRead = "0" // normalize sentinel to valid oldest param
				}

				historyParams := slack.GetConversationHistoryParameters{
					ChannelID: channel.ID,
					Oldest:    lastRead,
					Limit:     params.maxMessagesPerChannel,
					Inclusive: false,
				}
				history, err := limiter.CallWithRetry(ctx, rl, 2, slackRetryAfter, func() (*slack.GetConversationHistoryResponse, error) {
					return ch.apiProvider.Slack().GetConversationHistoryContext(ctx, &historyParams)
				})
				apiCalls++
				if err != nil {
					var rle *slack.RateLimitedError
					if errors.As(err, &rle) {
						rateLimited++
						ch.logger.Warn("Rate limited on conversation history (retries exhausted)",
							zap.String("channel", channel.ID),
							zap.Duration("retryAfter", rle.RetryAfter))
					} else {
						ch.logger.Debug("Failed to get history for unread check",
							zap.String("channel", channel.ID),
							zap.Error(err))
					}
				} else if len(history.Messages) > 0 {
					hasUnreads = true
					unreadCount = len(history.Messages)
				}
			}

			if !hasUnreads {
				continue
			}

			// Determine channel type and display name
			channelType := groupType
			if channelType == "" {
				// For public/private channels, categorize as internal or partner
				if info.IsExtShared {
					channelType = "partner"
				} else {
					channelType = "internal"
				}
			}

			// Apply channel_types filter for internal vs partner
			if params.channelTypes != "all" && params.channelTypes != channelType {
				continue
			}

			channelName := ch.getChannelDisplayName(info, channelType, usersMap)

			latestTs := ""
			if info.Latest != nil {
				latestTs = info.Latest.Timestamp
			}

			unreadChannels = append(unreadChannels, UnreadChannel{
				ChannelID:   channel.ID,
				ChannelName: channelName,
				ChannelType: channelType,
				UnreadCount: unreadCount,
				LastRead:    info.LastRead,
				Latest:      latestTs,
			})
		}

		// Move to next page if available
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	ch.logger.Debug("Scanned type group",
		zap.Strings("types", slackTypes),
		zap.Int("scanned", scanned),
		zap.Int("found", len(unreadChannels)),
		zap.Int("apiCalls", apiCalls),
		zap.Int("rateLimited", rateLimited))

	return unreadChannels, apiCalls, scanned, rateLimited
}

// getChannelDisplayName returns a human-readable name for a channel from conversations.info data.
func (ch *ConversationsHandler) getChannelDisplayName(info *slack.Channel, channelType string, usersMap *provider.UsersCache) string {
	switch channelType {
	case "dm":
		if info.User != "" {
			if u, ok := usersMap.Users[info.User]; ok {
				return "@" + u.Name
			}
			return "@" + info.User
		}
		return info.ID
	case "group_dm":
		return info.Name
	default:
		name := info.Name
		if !strings.HasPrefix(name, "#") {
			return "#" + name
		}
		return name
	}
}

// ConversationsMarkHandler marks a channel as read up to a specific timestamp
func (ch *ConversationsHandler) ConversationsMarkHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsMarkHandler called", request)

	params, err := ch.parseParamsToolMark(request)
	if err != nil {
		ch.logger.Error("Failed to parse mark params", zap.Error(err))
		return nil, err
	}

	channel := params.channel
	ts := params.ts

	if ts == "" {
		// Fetch the latest message to get its timestamp
		historyParams := slack.GetConversationHistoryParameters{
			ChannelID: channel,
			Limit:     1,
		}
		history, err := ch.apiProvider.Slack().GetConversationHistoryContext(ctx, &historyParams)
		if err != nil {
			ch.logger.Error("Failed to get latest message", zap.Error(err))
			return nil, fmt.Errorf("failed to get latest message: %v", err)
		}
		if len(history.Messages) > 0 {
			ts = history.Messages[0].Timestamp
		} else {
			// No messages in channel, nothing to mark
			return mcp.NewToolResultText("No messages to mark as read"), nil
		}
	}

	// Mark the conversation as read
	err = ch.apiProvider.Slack().MarkConversationContext(ctx, channel, ts)
	if err != nil {
		ch.logger.Error("Failed to mark conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to mark conversation as read: %v", err)
	}

	ch.logger.Info("Marked conversation as read",
		zap.String("channel", channel),
		zap.String("ts", ts))

	return mcp.NewToolResultText(fmt.Sprintf("Marked %s as read up to %s", channel, ts)), nil
}

func (ch *ConversationsHandler) ConversationsLeaveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsLeaveHandler called", request)

	if !requireToolEnabled("SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL", "conversations_leave") {
		ch.logger.Error("Channel membership tool disabled by default")
		return nil, errors.New(
			"by default, the conversations_leave tool is disabled to guard Slack workspaces against accidental channel membership changes. " +
				"To enable it, set the SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL environment variable to true or 1, " +
				"or add 'conversations_leave' to SLACK_MCP_ENABLED_TOOLS",
		)
	}

	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, fmt.Errorf("channel_id is required")
	}

	channel, err := ch.resolveChannelID(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve channel: %w", err)
	}

	notInChannel, err := ch.apiProvider.Slack().LeaveConversationContext(ctx, channel)
	if err != nil {
		ch.logger.Error("Failed to leave conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to leave conversation: %v", err)
	}

	if notInChannel {
		ch.logger.Info("Was not in channel", zap.String("channel", channel))
		return mcp.NewToolResultText(fmt.Sprintf("Not a member of %s", channel)), nil
	}

	ch.logger.Info("Left conversation", zap.String("channel", channel))
	return mcp.NewToolResultText(fmt.Sprintf("Successfully left %s", channel)), nil
}

func (ch *ConversationsHandler) ConversationsJoinHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsJoinHandler called", request)

	if !requireToolEnabled("SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL", "conversations_join") {
		ch.logger.Error("Channel membership tool disabled by default")
		return nil, errors.New(
			"by default, the conversations_join tool is disabled to guard Slack workspaces against accidental channel membership changes. " +
				"To enable it, set the SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL environment variable to true or 1, " +
				"or add 'conversations_join' to SLACK_MCP_ENABLED_TOOLS",
		)
	}

	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, fmt.Errorf("channel_id is required")
	}

	channel, err := ch.resolveChannelID(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve channel: %w", err)
	}

	_, _, _, err = ch.apiProvider.Slack().JoinConversationContext(ctx, channel)
	if err != nil {
		ch.logger.Error("Failed to join conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to join conversation: %v", err)
	}

	ch.logger.Info("Joined conversation", zap.String("channel", channel))
	return mcp.NewToolResultText(fmt.Sprintf("Successfully joined %s", channel)), nil
}

// sortChannelsByPriority sorts channels: DMs > group_dm > partner > internal
func (ch *ConversationsHandler) sortChannelsByPriority(channels []UnreadChannel) {
	priority := map[string]int{
		"dm":       0,
		"group_dm": 1,
		"partner":  2,
		"internal": 3,
	}

	sort.Slice(channels, func(i, j int) bool {
		pi := priority[channels[i].ChannelType]
		pj := priority[channels[j].ChannelType]
		return pi < pj
	})
}

// marshalUnreadChannelsToCSV converts unread channels to CSV format
func (ch *ConversationsHandler) marshalUnreadChannelsToCSV(channels []UnreadChannel) (*mcp.CallToolResult, error) {
	csvBytes, err := gocsv.MarshalBytes(&channels)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(csvBytes)), nil
}

func isChannelAllowedForConfig(channel, config string) bool {
	if config == "" || config == "true" || config == "1" {
		return true
	}
	items := strings.Split(config, ",")
	isNegated := strings.HasPrefix(strings.TrimSpace(items[0]), "!")
	for _, item := range items {
		item = strings.TrimSpace(item)
		if isNegated {
			if strings.TrimPrefix(item, "!") == channel {
				return false
			}
		} else {
			if item == channel {
				return true
			}
		}
	}
	return isNegated
}

func isChannelAllowed(channel string) bool {
	return isChannelAllowedForConfig(channel, os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL"))
}

// checkSendStatus reports whether conversations_add_message would accept a message
// to the given channel. Returns a human-readable status string.
func checkSendStatus(channel string) string {
	addMessageTool := os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")
	addMessageEnabled := os.Getenv("SLACK_MCP_ENABLED_TOOLS")
	if addMessageTool == "" && !isToolInEnabledList(addMessageEnabled, "conversations_add_message") {
		return "not available"
	}
	if !isChannelAllowedForConfig(channel, addMessageTool) {
		return "not available for this channel"
	}
	return "available"
}

func (ch *ConversationsHandler) resolveChannelID(ctx context.Context, channel string) (string, error) {
	if !strings.HasPrefix(channel, "#") && !strings.HasPrefix(channel, "@") {
		return channel, nil
	}

	// First attempt: try to resolve from current cache
	channelsMaps := ch.apiProvider.ProvideChannelsMaps()
	chn, ok := channelsMaps.ChannelsInv[channel]
	if ok {
		return channelsMaps.Channels[chn].ID, nil
	}

	// Channel not found - try refreshing cache and retry once
	ch.logger.Debug("Channel not found in cache, attempting refresh",
		zap.String("channel", channel))

	refreshErr := ch.apiProvider.ForceRefreshChannels(ctx)
	wasRateLimited := errors.Is(refreshErr, provider.ErrRefreshRateLimited)

	if refreshErr != nil && !wasRateLimited {
		ch.logger.Error("Failed to refresh channels cache",
			zap.String("channel", channel),
			zap.Error(refreshErr))
		return "", fmt.Errorf("channel %q not found and cache refresh failed: %w", channel, refreshErr)
	}

	// If rate-limited, cache wasn't refreshed - no point in a second lookup
	if wasRateLimited {
		ch.logger.Warn("Channel not found; cache refresh was rate-limited",
			zap.String("channel", channel))
		return "", fmt.Errorf("channel %q not found (cache refresh was rate-limited, try again later)", channel)
	}

	// Second attempt after successful refresh
	channelsMaps = ch.apiProvider.ProvideChannelsMaps()
	chn, ok = channelsMaps.ChannelsInv[channel]
	if !ok {
		ch.logger.Error("Channel not found even after cache refresh",
			zap.String("channel", channel))
		return "", fmt.Errorf("channel %q not found", channel)
	}

	ch.logger.Debug("Channel found after cache refresh",
		zap.String("channel", channel),
		zap.String("channel_id", channelsMaps.Channels[chn].ID))

	return channelsMaps.Channels[chn].ID, nil
}

func (ch *ConversationsHandler) convertMessagesFromHistory(ctx context.Context, slackMessages []slack.Message, channel string, includeActivity bool, mode text.OutputMode) []Message {
	resolver := ch.newUserResolver(ctx)
	var messages []Message
	warn := false

	for _, msg := range slackMessages {
		if (msg.SubType != "" && msg.SubType != "bot_message" && msg.SubType != "thread_broadcast") && !includeActivity {
			continue
		}

		userName, realName, ok := resolver.resolve(msg.User)

		if !ok && msg.SubType == "bot_message" {
			userName, realName, ok = getBotInfo(msg.Username)
		}

		if !ok {
			warn = true
		}

		timestamp, err := text.TimestampToIsoRFC3339(msg.Timestamp)
		if err != nil {
			ch.logger.Error("Failed to convert timestamp to RFC3339", zap.Error(err))
			continue
		}

		msgText := text.MergeBlocksWithText(msg.Text, msg.Blocks)
		if msgText == "" {
			msgText = text.FilesToText(msg.Files)
		}
		msgText += text.AttachmentsTo2CSV(msgText, msg.Attachments, mode)

		var reactionParts []string
		for _, r := range msg.Reactions {
			reactionParts = append(reactionParts, fmt.Sprintf("%s:%d", r.Name, r.Count))
		}
		reactionsString := strings.Join(reactionParts, "|")

		botName := ""
		if msg.BotProfile != nil && msg.BotProfile.Name != "" {
			botName = msg.BotProfile.Name
		}

		fileCount := len(msg.Files)
		hasMedia := fileCount > 0 || hasImageBlocks(msg.Blocks)

		var attachmentIDs []string
		for _, f := range msg.Files {
			if f.Name != "" {
				attachmentIDs = append(attachmentIDs, fmt.Sprintf("%s (%s)", f.ID, f.Name))
			} else {
				attachmentIDs = append(attachmentIDs, f.ID)
			}
		}
		attachmentIDsStr := strings.Join(attachmentIDs, ", ")

		messages = append(messages, Message{
			MsgID:         msg.Timestamp,
			UserID:        msg.User,
			UserName:      userName,
			RealName:      realName,
			Text:          text.ProcessText(msgText),
			Channel:       channel,
			ThreadTs:      msg.ThreadTimestamp,
			Time:          timestamp,
			Reactions:     reactionsString,
			BotName:       botName,
			FileCount:     fileCount,
			AttachmentIDs: attachmentIDsStr,
			HasMedia:      hasMedia,
		})
	}

	if ready, err := ch.apiProvider.IsReady(); !ready {
		if warn && errors.Is(err, provider.ErrUsersNotReady) {
			ch.logger.Warn(
				"WARNING: Slack users sync is not ready yet, you may experience some limited functionality and see UIDs instead of resolved names as well as unable to query users by their @handles. Users sync is part of channels sync and operations on channels depend on users collection (IM, MPIM). Please wait until users are synced and try again",
				zap.Error(err),
			)
		}
	}
	return messages
}

func (ch *ConversationsHandler) convertMessagesFromSearch(ctx context.Context, slackMessages []slack.SearchMessage, mode text.OutputMode) []Message {
	resolver := ch.newUserResolver(ctx)
	var messages []Message
	warn := false

	for _, msg := range slackMessages {
		userName, realName, ok := resolver.resolve(msg.User)

		if !ok && msg.User == "" && msg.Username != "" {
			userName, realName, ok = getBotInfo(msg.Username)
		}

		if !ok {
			warn = true
		}

		threadTs, _ := extractThreadTS(msg.Permalink)

		timestamp, err := text.TimestampToIsoRFC3339(msg.Timestamp)
		if err != nil {
			ch.logger.Error("Failed to convert timestamp to RFC3339", zap.Error(err))
			continue
		}

		msgText := text.MergeBlocksWithText(msg.Text, msg.Blocks)
		msgText += text.AttachmentsTo2CSV(msgText, msg.Attachments, mode)

		hasMedia := hasImageBlocks(msg.Blocks)

		messages = append(messages, Message{
			MsgID:     msg.Timestamp,
			UserID:    msg.User,
			UserName:  userName,
			RealName:  realName,
			Text:      text.ProcessText(msgText),
			Channel:   fmt.Sprintf("%s (#%s)", msg.Channel.ID, msg.Channel.Name),
			ThreadTs:  threadTs,
			Time:      timestamp,
			Permalink: msg.Permalink,
			Reactions: "",
			HasMedia:  hasMedia,
		})
	}

	if ready, err := ch.apiProvider.IsReady(); !ready {
		if warn && errors.Is(err, provider.ErrUsersNotReady) {
			ch.logger.Warn(
				"Slack users sync not ready; you may see raw UIDs instead of names and lose some functionality.",
				zap.Error(err),
			)
		}
	}
	return messages
}

func (ch *ConversationsHandler) parseParamsToolConversations(ctx context.Context, request mcp.CallToolRequest) (*conversationParams, error) {
	channel := request.GetString("channel_id", "")
	if channel == "" {
		ch.logger.Error("channel_id missing in conversations params")
		return nil, errors.New("channel_id must be a string")
	}

	limit := request.GetString("limit", "")
	cursor := request.GetString("cursor", "")
	activity := request.GetBool("include_activity_messages", false)

	var (
		paramLimit  int
		paramOldest string
		paramLatest string
		err         error
	)
	isDurationLimit := strings.HasSuffix(limit, "d") || strings.HasSuffix(limit, "w") || strings.HasSuffix(limit, "m")
	if isDurationLimit && cursor != "" {
		// The schema's advertised default limit is "1d", but the same schema
		// forbids a limit alongside 'cursor'. Sending both a cursor and a
		// duration window makes pagination silently return nothing, so honour
		// the cursor and drop the window — matching what the numeric branch
		// already does when a cursor is present.
		ch.logger.Debug("Ignoring duration limit because a cursor was provided",
			zap.String("limit", limit),
			zap.String("cursor", cursor),
		)
	} else if isDurationLimit {
		paramLimit, paramOldest, paramLatest, err = limitByExpression(limit, defaultConversationsExpressionLimit)
		if err != nil {
			ch.logger.Error("Invalid duration limit", zap.String("limit", limit), zap.Error(err))
			return nil, err
		}
	} else if cursor == "" {
		paramLimit, err = limitByNumeric(limit, defaultConversationsNumericLimit)
		if err != nil {
			ch.logger.Error("Invalid numeric limit", zap.String("limit", limit), zap.Error(err))
			return nil, err
		}
	}

	if strings.HasPrefix(channel, "#") || strings.HasPrefix(channel, "@") {
		if ready, err := ch.apiProvider.IsReady(); !ready {
			if errors.Is(err, provider.ErrUsersNotReady) {
				ch.logger.Warn(
					"WARNING: Slack users sync is not ready yet, you may experience some limited functionality and see UIDs instead of resolved names as well as unable to query users by their @handles. Users sync is part of channels sync and operations on channels depend on users collection (IM, MPIM). Please wait until users are synced and try again",
					zap.Error(err),
				)
			}
			if errors.Is(err, provider.ErrChannelsNotReady) {
				ch.logger.Warn(
					"WARNING: Slack channels sync is not ready yet, you may experience some limited functionality and be able to request conversation only by Channel ID, not by its name. Please wait until channels are synced and try again.",
					zap.Error(err),
				)
			}
			return nil, fmt.Errorf("channel %q not found in empty cache", channel)
		}
		// Use resolveChannelID which includes refresh-on-error logic
		resolvedChannel, err := ch.resolveChannelID(ctx, channel)
		if err != nil {
			return nil, err
		}
		channel = resolvedChannel
	}

	return &conversationParams{
		channel:  channel,
		limit:    paramLimit,
		oldest:   paramOldest,
		latest:   paramLatest,
		cursor:   cursor,
		activity: activity,
	}, nil
}

func (ch *ConversationsHandler) parseParamsToolOpenConversation(ctx context.Context, request mcp.CallToolRequest) ([]string, error) {
	usersStr := request.GetString("users", "")
	if usersStr == "" {
		return nil, errors.New("users must be a comma-separated string of user IDs or @usernames")
	}

	var userIDs []string
	usersMap := ch.apiProvider.ProvideUsersMap()

	for _, user := range strings.Split(usersStr, ",") {
		user = strings.TrimSpace(user)
		if user == "" {
			continue
		}

		if strings.HasPrefix(user, "@") {
			username := strings.TrimPrefix(user, "@")
			if id, ok := usersMap.UsersInv[username]; ok {
				userIDs = append(userIDs, id)
			} else {
				ch.logger.Error("User not found", zap.String("username", username))
				return nil, fmt.Errorf("user %q not found. Please provide a valid @username or User ID (e.g. U12345678)", user)
			}
		} else {
			// Assume it's a User ID
			userIDs = append(userIDs, user)
		}
	}

	if len(userIDs) == 0 {
		return nil, errors.New("no valid users provided")
	}

	return userIDs, nil
}

// ConversationsOpenHandler opens a new DM or MPIM
func (ch *ConversationsHandler) ConversationsOpenHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsOpenHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	userIDs, err := ch.parseParamsToolOpenConversation(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse open-conversation params", zap.Error(err))
		return nil, err
	}

	params := &slack.OpenConversationParameters{
		Users:    userIDs,
		ReturnIM: true,
	}

	channel, _, _, err := ch.apiProvider.Slack().OpenConversationContext(ctx, params)
	if err != nil {
		ch.logger.Error("Slack OpenConversationContext failed", zap.Error(err))
		return nil, err
	}

	// Try to force cache refresh in background since we have a new channel
	go func() {
		_ = ch.apiProvider.ForceRefreshChannels(context.Background())
	}()

	return mcp.NewToolResultText(fmt.Sprintf("Successfully opened conversation. Channel ID: %s", channel.Conversation.ID)), nil
}

// isToolInEnabledList checks for an exact tool name match in a comma-separated list.
// Unlike strings.Contains, this avoids false positives from partial matches
// in comma-separated lists (e.g., matching "foo" inside "foobar,baz").
func isToolInEnabledList(enabledTools, toolName string) bool {
	if enabledTools == "" {
		return false
	}
	for _, t := range strings.Split(enabledTools, ",") {
		if strings.TrimSpace(t) == toolName {
			return true
		}
	}
	return false
}

// isTruthyEnv reports whether a gate environment variable's value means
// "enabled". The accepted set matches the SLACK_MCP_ATTACHMENT_TOOL and
// SLACK_MCP_MARK_TOOL checks and the error text every gate prints: true, 1,
// yes. Comparison is case-insensitive and ignores surrounding whitespace so
// that `=True` and `= true` behave as an operator expects.
//
// A deliberate copy of this predicate lives in pkg/server/server.go for the
// registration-time gate; the two must stay in sync.
func isTruthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// requireToolEnabled reports whether a call-time-gated tool is enabled: either
// its dedicated envVarName is set to a truthy value (true, 1, yes —
// case-insensitive), or toolName is explicitly listed in the
// SLACK_MCP_ENABLED_TOOLS allowlist. Any other value, including "false",
// leaves the tool disabled.
func requireToolEnabled(envVarName, toolName string) bool {
	if isTruthyEnv(os.Getenv(envVarName)) {
		return true
	}
	return isToolInEnabledList(os.Getenv("SLACK_MCP_ENABLED_TOOLS"), toolName)
}

func formatThreadTs(threadTs string) string {
	if threadTs == "" {
		return "(top-level message)"
	}
	return threadTs
}

func (ch *ConversationsHandler) parseParamsToolDraftMessage(ctx context.Context, request mcp.CallToolRequest) (*addMessageParams, error) {
	channel := request.GetString("channel_id", "")
	if channel == "" {
		ch.logger.Error("channel_id missing in draft-message params")
		return nil, errors.New("channel_id must be a string")
	}
	channel, err := ch.resolveChannelID(ctx, channel)
	if err != nil {
		ch.logger.Error("Channel not found", zap.String("channel", channel), zap.Error(err))
		return nil, err
	}

	threadTs := request.GetString("thread_ts", "")
	if threadTs != "" && !strings.Contains(threadTs, ".") {
		ch.logger.Error("Invalid thread_ts format", zap.String("thread_ts", threadTs))
		return nil, errors.New("thread_ts must be a valid timestamp in format 1234567890.123456")
	}

	msgText := request.GetString("text", "")
	if msgText == "" {
		ch.logger.Error("Message text missing")
		return nil, errors.New("text is required and must not be empty")
	}

	contentType := request.GetString("content_type", "text/markdown")
	if contentType != "text/plain" && contentType != "text/markdown" {
		ch.logger.Error("Invalid content_type", zap.String("content_type", contentType))
		return nil, errors.New("content_type must be either 'text/plain' or 'text/markdown'")
	}

	return &addMessageParams{
		channel:     channel,
		threadTs:    threadTs,
		text:        msgText,
		contentType: contentType,
	}, nil
}

func (ch *ConversationsHandler) parseParamsToolAddMessage(ctx context.Context, request mcp.CallToolRequest) (*addMessageParams, error) {
	toolConfig := os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")
	enabledTools := os.Getenv("SLACK_MCP_ENABLED_TOOLS")

	if toolConfig == "" {
		if !isToolInEnabledList(enabledTools, "conversations_add_message") {
			ch.logger.Error("Add-message tool disabled by default")
			return nil, errors.New(
				"by default, the conversations_add_message tool is disabled to guard Slack workspaces against accidental spamming. " +
					"To enable it, set the SLACK_MCP_ADD_MESSAGE_TOOL environment variable to true, 1, or comma separated list of channels " +
					"to limit where the MCP can post messages, e.g. 'SLACK_MCP_ADD_MESSAGE_TOOL=C1234567890,D0987654321', 'SLACK_MCP_ADD_MESSAGE_TOOL=!C1234567890' " +
					"to enable all except one or 'SLACK_MCP_ADD_MESSAGE_TOOL=true' for all channels and DMs",
			)
		}
		toolConfig = "true"
	}

	channel := request.GetString("channel_id", "")
	if channel == "" {
		ch.logger.Error("channel_id missing in add-message params")
		return nil, errors.New("channel_id must be a string")
	}
	channel, err := ch.resolveChannelID(ctx, channel)
	if err != nil {
		ch.logger.Error("Channel not found", zap.String("channel", channel), zap.Error(err))
		return nil, err
	}
	if !isChannelAllowed(channel) {
		ch.logger.Warn("Add-message tool not allowed for channel", zap.String("channel", channel), zap.String("policy", toolConfig))
		return nil, fmt.Errorf("conversations_add_message tool is not allowed for channel %q, applied policy: %s", channel, toolConfig)
	}

	threadTs := request.GetString("thread_ts", "")
	if threadTs != "" && !strings.Contains(threadTs, ".") {
		ch.logger.Error("Invalid thread_ts format", zap.String("thread_ts", threadTs))
		return nil, errors.New("thread_ts must be a valid timestamp in format 1234567890.123456")
	}

	msgText := request.GetString("text", "")
	if msgText == "" {
		// Backward compatibility with "payload" parameter
		msgText = request.GetString("payload", "")
	}

	contentType := request.GetString("content_type", "text/markdown")
	if contentType != "text/plain" && contentType != "text/markdown" {
		ch.logger.Error("Invalid content_type", zap.String("content_type", contentType))
		return nil, errors.New("content_type must be either 'text/plain' or 'text/markdown'")
	}

	// Parse optional raw blocks JSON. Accepts blocks as either:
	// - A JSON string containing a blocks array: "blocks": "[{...}]"
	// - A raw JSON array (parsed by MCP SDK): "blocks": [{...}]
	var blocks []slack.Block
	args := request.GetArguments()
	if rawBlocks, ok := args["blocks"]; ok && rawBlocks != nil {
		var blocksJSON []byte
		switch v := rawBlocks.(type) {
		case string:
			if v != "" {
				blocksJSON = []byte(v)
			}
		default:
			// Raw JSON array/object passed directly - re-marshal to bytes
			var err error
			blocksJSON, err = json.Marshal(v)
			if err != nil {
				ch.logger.Error("Failed to marshal blocks argument", zap.Error(err))
				return nil, fmt.Errorf("blocks must be valid Slack Block Kit JSON: %w", err)
			}
		}
		if blocksJSON != nil {
			var slackBlocks slack.Blocks
			if err := json.Unmarshal(blocksJSON, &slackBlocks); err != nil {
				ch.logger.Error("Failed to parse blocks JSON", zap.Error(err))
				return nil, fmt.Errorf("blocks must be valid Slack Block Kit JSON: %w", err)
			}
			blocks = slackBlocks.BlockSet
		}
	}

	// Require either text or blocks
	if msgText == "" && blocks == nil {
		ch.logger.Error("Message text and blocks both missing")
		return nil, errors.New("either text or blocks must be provided")
	}

	return &addMessageParams{
		channel:     channel,
		threadTs:    threadTs,
		text:        msgText,
		contentType: contentType,
		blocks:      blocks,
	}, nil
}

func (ch *ConversationsHandler) parseParamsToolReaction(ctx context.Context, request mcp.CallToolRequest) (*addReactionParams, error) {
	toolConfig := os.Getenv("SLACK_MCP_REACTION_TOOL")
	enabledTools := os.Getenv("SLACK_MCP_ENABLED_TOOLS")

	if toolConfig == "" {
		if !isToolInEnabledList(enabledTools, "reactions_add") && !isToolInEnabledList(enabledTools, "reactions_remove") {
			ch.logger.Error("Reactions tool disabled by default")
			return nil, errors.New(
				"by default, the reactions tools are disabled to guard Slack workspaces against accidental spamming. " +
					"To enable them, set the SLACK_MCP_REACTION_TOOL environment variable to true, 1, or comma separated list of channels " +
					"to limit where the MCP can manage reactions, e.g. 'SLACK_MCP_REACTION_TOOL=C1234567890,D0987654321', 'SLACK_MCP_REACTION_TOOL=!C1234567890' " +
					"to enable all except one or 'SLACK_MCP_REACTION_TOOL=true' for all channels and DMs",
			)
		}
		toolConfig = "true"
	}

	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, errors.New("channel_id is required")
	}
	channel, err := ch.resolveChannelID(ctx, channel)
	if err != nil {
		ch.logger.Error("Channel not found", zap.String("channel", channel), zap.Error(err))
		return nil, err
	}
	if !isChannelAllowedForConfig(channel, toolConfig) {
		ch.logger.Warn("Reactions tool not allowed for channel", zap.String("channel", channel), zap.String("policy", toolConfig))
		return nil, fmt.Errorf("reactions tools are not allowed for channel %q, applied policy: %s", channel, toolConfig)
	}

	timestamp := request.GetString("timestamp", "")
	if timestamp == "" {
		return nil, errors.New("timestamp is required")
	}

	emoji := strings.Trim(request.GetString("emoji", ""), ":")
	if emoji == "" {
		return nil, errors.New("emoji is required")
	}

	return &addReactionParams{
		channel:   channel,
		timestamp: timestamp,
		emoji:     emoji,
	}, nil
}

func (ch *ConversationsHandler) parseParamsToolFilesGet(request mcp.CallToolRequest) (*filesGetParams, error) {
	toolConfig := os.Getenv("SLACK_MCP_ATTACHMENT_TOOL")
	enabledTools := os.Getenv("SLACK_MCP_ENABLED_TOOLS")

	if toolConfig == "" {
		if !isToolInEnabledList(enabledTools, "attachment_get_data") {
			ch.logger.Error("Attachment tool disabled by default")
			return nil, errors.New(
				"by default, the attachment_get_data tool is disabled. " +
					"To enable it, set the SLACK_MCP_ATTACHMENT_TOOL environment variable to true or 1",
			)
		}
		toolConfig = "true"
	}
	if !isTruthyEnv(toolConfig) {
		ch.logger.Error("Attachment tool disabled", zap.String("config", toolConfig))
		return nil, errors.New("SLACK_MCP_ATTACHMENT_TOOL must be set to 'true', '1', or 'yes' to enable")
	}

	fileID := request.GetString("file_id", "")
	if fileID == "" {
		return nil, errors.New("file_id is required")
	}

	return &filesGetParams{
		fileID: fileID,
	}, nil
}

func (ch *ConversationsHandler) parseParamsToolUsersSearch(request mcp.CallToolRequest) (*usersSearchParams, error) {
	query := strings.TrimSpace(request.GetString("query", ""))
	if query == "" {
		return nil, errors.New("query is required")
	}

	limit := request.GetInt("limit", 10)
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	return &usersSearchParams{
		query: query,
		limit: limit,
	}, nil
}

func (ch *ConversationsHandler) parseParamsToolUnreads(request mcp.CallToolRequest) *unreadsParams {
	// GetInt only substitutes its default for an absent key, so a caller-supplied
	// non-positive value survives. maxChannels reaches a slice expression in
	// collectUnreadChannels, so treat non-positive exactly like absent.
	maxChannels := request.GetInt("max_channels", 50)
	if maxChannels <= 0 {
		maxChannels = 50
	}
	maxMessagesPerChannel := request.GetInt("max_messages_per_channel", 10)
	if maxMessagesPerChannel <= 0 {
		maxMessagesPerChannel = 10
	}

	return &unreadsParams{
		includeMessages:       request.GetBool("include_messages", true),
		channelTypes:          request.GetString("channel_types", "all"),
		maxChannels:           maxChannels,
		maxMessagesPerChannel: maxMessagesPerChannel,
		mentionsOnly:          request.GetBool("mentions_only", false),
		includeMuted:          request.GetBool("include_muted", false),
	}
}

func (ch *ConversationsHandler) parseParamsToolMark(request mcp.CallToolRequest) (*markParams, error) {
	toolConfig := os.Getenv("SLACK_MCP_MARK_TOOL")
	if toolConfig == "" {
		ch.logger.Error("Mark tool disabled by default")
		return nil, errors.New(
			"by default, the conversations_mark tool is disabled to prevent accidental marking of messages as read. " +
				"To enable it, set the SLACK_MCP_MARK_TOOL environment variable to true or 1, " +
				"e.g. 'SLACK_MCP_MARK_TOOL=true'",
		)
	}
	if !isTruthyEnv(toolConfig) {
		ch.logger.Error("Mark tool disabled by config", zap.String("config", toolConfig))
		return nil, errors.New(
			"the conversations_mark tool is disabled. " +
				"To enable it, set the SLACK_MCP_MARK_TOOL environment variable to true or 1",
		)
	}

	channel := request.GetString("channel_id", "")
	if channel == "" {
		ch.logger.Error("channel_id missing in mark params")
		return nil, errors.New("channel_id is required")
	}

	// Resolve channel name to ID if needed
	if strings.HasPrefix(channel, "#") || strings.HasPrefix(channel, "@") {
		channelsMaps := ch.apiProvider.ProvideChannelsMaps()
		chn, ok := channelsMaps.ChannelsInv[channel]
		if !ok {
			ch.logger.Error("Channel not found", zap.String("channel", channel))
			return nil, fmt.Errorf("channel %q not found", channel)
		}
		channel = channelsMaps.Channels[chn].ID
	}

	ts := request.GetString("ts", "")

	return &markParams{
		channel: channel,
		ts:      ts,
	}, nil
}
func (ch *ConversationsHandler) parseParamsToolSearch(ctx context.Context, req mcp.CallToolRequest) (*searchParams, error) {
	rawQuery := strings.TrimSpace(req.GetString("search_query", ""))
	freeText, filters := splitQuery(rawQuery)

	if req.GetBool("filter_threads_only", false) {
		addFilter(filters, "is", "thread")
	}
	if chName := req.GetString("filter_in_channel", ""); chName != "" {
		f, err := ch.paramFormatChannel(chName)
		if err != nil {
			ch.logger.Error("Invalid channel filter", zap.String("filter", chName), zap.Error(err))
			return nil, err
		}
		addFilter(filters, "in", f)
	} else if im := strings.TrimSpace(req.GetString("filter_in_im_or_mpim", "")); im != "" {
		var (
			f   string
			err error
		)
		// The tool description documents the 'D1234567890' conversation-ID
		// form, which is not a user ID and must be resolved via the channels
		// cache rather than the users map.
		if isSlackConversationIDPrefix(im) {
			f, err = formatConversationFilter(ch.apiProvider.ProvideChannelsMaps(), im)
		} else {
			f, err = ch.paramFormatUser(ctx, im)
		}
		if err != nil {
			ch.logger.Error("Invalid IM/MPIM filter", zap.String("filter", im), zap.Error(err))
			return nil, err
		}
		addFilter(filters, "in", f)
	}
	if with := req.GetString("filter_users_with", ""); with != "" {
		f, err := ch.paramFormatUser(ctx, with)
		if err != nil {
			ch.logger.Error("Invalid with-user filter", zap.String("filter", with), zap.Error(err))
			return nil, err
		}
		addFilter(filters, "with", f)
	}
	if from := req.GetString("filter_users_from", ""); from != "" {
		f, err := ch.paramFormatUser(ctx, from)
		if err != nil {
			ch.logger.Error("Invalid from-user filter", zap.String("filter", from), zap.Error(err))
			return nil, err
		}
		addFilter(filters, "from", f)
	}

	dateMap, err := buildDateFilters(
		req.GetString("filter_date_before", ""),
		req.GetString("filter_date_after", ""),
		req.GetString("filter_date_on", ""),
		req.GetString("filter_date_during", ""),
	)
	if err != nil {
		ch.logger.Error("Invalid date filters", zap.Error(err))
		return nil, err
	}
	for key, val := range dateMap {
		addFilter(filters, key, val)
	}

	finalQuery := buildQuery(freeText, filters)
	// The tool schema declares a default of 100 and a documented range of
	// 1..100; clamp so out-of-range values are never forwarded to Slack.
	limit := req.GetInt("limit", defaultSearchMessagesLimit)
	if limit <= 0 {
		limit = defaultSearchMessagesLimit
	}
	if limit > maxSearchMessagesLimit {
		limit = maxSearchMessagesLimit
	}
	cursor := req.GetString("cursor", "")

	var (
		page          int
		decodedCursor []byte
	)
	if cursor != "" {
		decodedCursor, err = base64.StdEncoding.DecodeString(cursor)
		if err != nil {
			ch.logger.Error("Invalid cursor decoding", zap.String("cursor", cursor), zap.Error(err))
			return nil, fmt.Errorf("invalid cursor: %v", err)
		}
		parts := strings.Split(string(decodedCursor), ":")
		if len(parts) != 2 {
			ch.logger.Error("Invalid cursor format", zap.String("cursor", cursor))
			return nil, fmt.Errorf("invalid cursor: %v", cursor)
		}
		page, err = strconv.Atoi(parts[1])
		if err != nil || page < 1 {
			ch.logger.Error("Invalid cursor page", zap.String("cursor", cursor), zap.Error(err))
			return nil, fmt.Errorf("invalid cursor page: %v", err)
		}
	} else {
		page = 1
	}

	ch.logger.Debug("Search parameters built",
		zap.String("query", finalQuery),
		zap.Int("limit", limit),
		zap.Int("page", page),
	)
	return &searchParams{
		query: finalQuery,
		limit: limit,
		page:  page,
	}, nil
}

// Slack user IDs may begin with U or W: https://docs.slack.dev/changelog/2016/08/11/user-id-format-changes
func isSlackUserIDPrefix(s string) bool {
	return strings.HasPrefix(s, "U") || strings.HasPrefix(s, "W")
}

// isSlackConversationIDPrefix reports whether s looks like a DM (D…) or
// group/MPIM (G…) conversation ID rather than a user ID or an @handle.
func isSlackConversationIDPrefix(s string) bool {
	return strings.HasPrefix(s, "D") || strings.HasPrefix(s, "G")
}

// formatConversationFilter resolves a D…/G… conversation ID against the
// channels cache into the value Slack's `in:` search modifier expects.
//
// For a DM the cache stores the peer's user ID, so we emit the same
// `<@Uxxxx>` form that the documented '@username_dm' input produces — the two
// documented spellings of the same conversation therefore build an identical
// query. For an MPIM (or any other cached conversation) we reuse the
// filter_in_channel formatting, which is the cached channel Name.
func formatConversationFilter(cms *provider.ChannelsCache, raw string) (string, error) {
	if cms != nil {
		if c, ok := cms.Channels[raw]; ok {
			if c.IsIM && c.User != "" {
				return fmt.Sprintf("<@%s>", c.User), nil
			}
			if c.Name != "" {
				return c.Name, nil
			}
		}
	}
	return "", fmt.Errorf("conversation %q not found in cache; pass the '@username' form instead", raw)
}

func (ch *ConversationsHandler) paramFormatUser(ctx context.Context, raw string) (string, error) {
	users := ch.apiProvider.ProvideUsersMap()
	raw = strings.TrimSpace(raw)
	if isSlackUserIDPrefix(raw) {
		u, ok := users.Users[raw]
		if !ok {
			// Targeted fetch: single users.info call instead of full cache rebuild
			patched, err := ch.apiProvider.PatchUser(ctx, raw)
			if err != nil {
				ch.logger.Debug("Targeted user fetch failed, user not found",
					zap.String("user_id", raw), zap.Error(err))
				return "", fmt.Errorf("user %q not found", raw)
			}
			return fmt.Sprintf("<@%s>", patched.ID), nil
		}
		return fmt.Sprintf("<@%s>", u.ID), nil
	}
	if strings.HasPrefix(raw, "<@") {
		raw = raw[2:]
	}
	if strings.HasPrefix(raw, "@") {
		raw = raw[1:]
	}
	uid, ok := users.UsersInv[raw]
	if !ok {
		return "", fmt.Errorf("user %q not found", raw)
	}
	return fmt.Sprintf("<@%s>", uid), nil
}

func (ch *ConversationsHandler) paramFormatChannel(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	cms := ch.apiProvider.ProvideChannelsMaps()
	if strings.HasPrefix(raw, "#") {
		if id, ok := cms.ChannelsInv[raw]; ok {
			return cms.Channels[id].Name, nil
		}
		return "", fmt.Errorf("channel %q not found", raw)
	}
	// Handle both C (standard channels) and G (private groups/channels) prefixes
	if strings.HasPrefix(raw, "C") || strings.HasPrefix(raw, "G") {
		if chn, ok := cms.Channels[raw]; ok {
			return chn.Name, nil
		}
		return "", fmt.Errorf("channel %q not found", raw)
	}
	return "", fmt.Errorf("invalid channel format: %q", raw)
}

// renderOptions carries the per-call rendering context (output mode plus
// anything the compact-mode legend header needs) through marshalMessagesToCSV.
type renderOptions struct {
	mode         text.OutputMode
	workspaceURL string
}

func marshalMessagesToCSV(messages []Message, opts renderOptions) (*mcp.CallToolResult, error) {
	if opts.mode != text.ModeFull {
		return marshalMessagesToCompactCSV(messages, opts.workspaceURL)
	}
	csvBytes, err := gocsv.MarshalBytes(&messages)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultText(string(csvBytes)), nil
}

// marshalMessagesToCompactCSV converts messages to the default agent CSV format.
func marshalMessagesToCompactCSV(messages []Message, workspaceURL string) (*mcp.CallToolResult, error) {
	compact := make([]CompactMessage, len(messages))
	for i, m := range messages {
		user := m.RealName
		if user == "" {
			user = m.UserName
		}
		if m.BotName != "" {
			user = m.BotName + " (bot)"
		}

		files := ""
		if m.FileCount > 0 {
			files = strconv.Itoa(m.FileCount)
		} else if m.HasMedia {
			// Search-path messages don't populate FileCount; HasMedia-but-unknown-count
			// is floored to 1 so the column still signals "there are files here".
			files = "1"
		}

		compact[i] = CompactMessage{
			User:          user,
			Channel:       m.Channel,
			Text:          m.Text,
			Time:          m.Time,
			MsgID:         m.MsgID,
			ThreadTs:      m.ThreadTs,
			Reactions:     m.Reactions,
			AttachmentIDs: m.AttachmentIDs,
			Files:         files,
			Cursor:        m.Cursor,
		}
	}

	csvBytes, err := gocsv.MarshalBytes(&compact)
	if err != nil {
		return nil, err
	}

	legend := buildLegendHeader(messages, workspaceURL)
	return mcp.NewToolResultText(legend + string(csvBytes)), nil
}

// buildLegendHeader emits comment lines (agent-oriented, not CSV data) that
// normalize per-row redundancy: distinct users with their IDs, and a permalink
// template so links are derivable without a Permalink column. Skipped for
// tiny result sets where the header would outweigh the rows.
func buildLegendHeader(messages []Message, workspaceURL string) string {
	if len(messages) < 3 {
		return ""
	}

	var sb strings.Builder

	seen := make(map[string]bool)
	var userParts []string
	for _, m := range messages {
		if m.BotName != "" || m.UserID == "" || seen[m.UserID] {
			continue
		}
		seen[m.UserID] = true

		name := m.UserName
		label := m.UserID + "=" + name
		if m.RealName != "" && m.RealName != name {
			label += "|" + m.RealName
		}
		userParts = append(userParts, label)
	}
	if len(userParts) > 0 {
		sb.WriteString("#users: ")
		sb.WriteString(strings.Join(userParts, ", "))
		sb.WriteString("\n")
	}

	if workspaceURL != "" {
		base := workspaceURL
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		sb.WriteString(fmt.Sprintf(
			"#link_template: %sarchives/{CHANNEL_ID}/p{MsgID with \".\" removed}  (Channel column may contain \"ID (#name)\" — use the leading ID)\n",
			base,
		))
	}

	return sb.String()
}

// userResolver resolves user IDs to names, fetching unknown users from the
// Slack API on demand. It caches the snapshot locally and remembers which IDs
// it already tried to fetch, so a user that doesn't exist in Slack is only
// looked up once per batch rather than once per message.
type userResolver struct {
	apiProvider  *provider.ApiProvider
	ctx          context.Context
	usersMap     *provider.UsersCache
	attemptedIDs map[string]bool
}

func (ch *ConversationsHandler) newUserResolver(ctx context.Context) *userResolver {
	return &userResolver{
		apiProvider:  ch.apiProvider,
		ctx:          ctx,
		usersMap:     ch.apiProvider.ProvideUsersMap(),
		attemptedIDs: make(map[string]bool),
	}
}

func (r *userResolver) resolve(userID string) (userName, realName string, ok bool) {
	if u, ok := r.usersMap.Users[userID]; ok {
		return u.Name, u.RealName, true
	}
	if userID == "" || r.attemptedIDs[userID] {
		return userID, userID, false
	}
	r.attemptedIDs[userID] = true
	patched, err := r.apiProvider.PatchUser(r.ctx, userID)
	if err != nil {
		return userID, userID, false
	}
	r.usersMap = r.apiProvider.ProvideUsersMap()
	return patched.Name, patched.RealName, true
}

func getBotInfo(botID string) (userName, realName string, ok bool) {
	return botID, botID, true
}

func limitByNumeric(limit string, defaultLimit int) (int, error) {
	if limit == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(limit)
	if err != nil {
		return 0, fmt.Errorf("invalid numeric limit: %q", limit)
	}
	return n, nil
}

func limitByExpression(limit, defaultLimit string) (slackLimit int, oldest, latest string, err error) {
	if limit == "" {
		limit = defaultLimit
	}
	if len(limit) < 2 {
		return 0, "", "", fmt.Errorf("invalid duration limit %q: too short", limit)
	}
	suffix := limit[len(limit)-1]
	numStr := limit[:len(limit)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return 0, "", "", fmt.Errorf("invalid duration limit %q: must be a positive integer followed by 'd', 'w', or 'm'", limit)
	}
	now := time.Now()
	loc := now.Location()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	var oldestTime time.Time
	switch suffix {
	case 'd':
		oldestTime = startOfToday.AddDate(0, 0, -n+1)
	case 'w':
		oldestTime = startOfToday.AddDate(0, 0, -n*7+1)
	case 'm':
		oldestTime = startOfToday.AddDate(0, -n, 0)
	default:
		return 0, "", "", fmt.Errorf("invalid duration limit %q: must end in 'd', 'w', or 'm'", limit)
	}
	latest = fmt.Sprintf("%d.000000", now.Unix())
	oldest = fmt.Sprintf("%d.000000", oldestTime.Unix())
	return 100, oldest, latest, nil
}

func extractThreadTS(rawurl string) (string, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", err
	}
	return u.Query().Get("thread_ts"), nil
}

func parseFlexibleDate(dateStr string) (time.Time, string, error) {
	dateStr = strings.TrimSpace(dateStr)
	standardFormats := []string{
		"2006-01-02",      // YYYY-MM-DD
		"2006/01/02",      // YYYY/MM/DD
		"01-02-2006",      // MM-DD-YYYY
		"01/02/2006",      // MM/DD/YYYY
		"02-01-2006",      // DD-MM-YYYY
		"02/01/2006",      // DD/MM/YYYY
		"Jan 2, 2006",     // Jan 2, 2006
		"January 2, 2006", // January 2, 2006
		"2 Jan 2006",      // 2 Jan 2006
		"2 January 2006",  // 2 January 2006
	}
	for _, fmtStr := range standardFormats {
		if t, err := time.Parse(fmtStr, dateStr); err == nil {
			return t, t.Format("2006-01-02"), nil
		}
	}

	monthMap := map[string]int{
		"january": 1, "jan": 1,
		"february": 2, "feb": 2,
		"march": 3, "mar": 3,
		"april": 4, "apr": 4,
		"may":  5,
		"june": 6, "jun": 6,
		"july": 7, "jul": 7,
		"august": 8, "aug": 8,
		"september": 9, "sep": 9, "sept": 9,
		"october": 10, "oct": 10,
		"november": 11, "nov": 11,
		"december": 12, "dec": 12,
	}

	// Month-Year patterns
	monthYear := regexp.MustCompile(`^(\d{4})\s+([A-Za-z]+)$|^([A-Za-z]+)\s+(\d{4})$`)
	if m := monthYear.FindStringSubmatch(dateStr); m != nil {
		var year int
		var monStr string
		if m[1] != "" && m[2] != "" {
			year, _ = strconv.Atoi(m[1])
			monStr = strings.ToLower(m[2])
		} else {
			year, _ = strconv.Atoi(m[4])
			monStr = strings.ToLower(m[3])
		}
		if mon, ok := monthMap[monStr]; ok {
			t := time.Date(year, time.Month(mon), 1, 0, 0, 0, 0, time.UTC)
			return t, t.Format("2006-01-02"), nil
		}
	}

	// Day-Month-Year and Month-Day-Year patterns
	dmy1 := regexp.MustCompile(`^(\d{1,2})[-\s]+([A-Za-z]+)[-\s]+(\d{4})$`)
	if m := dmy1.FindStringSubmatch(dateStr); m != nil {
		day, _ := strconv.Atoi(m[1])
		year, _ := strconv.Atoi(m[3])
		monStr := strings.ToLower(m[2])
		if mon, ok := monthMap[monStr]; ok {
			t := time.Date(year, time.Month(mon), day, 0, 0, 0, 0, time.UTC)
			if t.Day() == day {
				return t, t.Format("2006-01-02"), nil
			}
		}
	}
	mdy := regexp.MustCompile(`^([A-Za-z]+)[-\s]+(\d{1,2})[-\s]+(\d{4})$`)
	if m := mdy.FindStringSubmatch(dateStr); m != nil {
		monStr := strings.ToLower(m[1])
		day, _ := strconv.Atoi(m[2])
		year, _ := strconv.Atoi(m[3])
		if mon, ok := monthMap[monStr]; ok {
			t := time.Date(year, time.Month(mon), day, 0, 0, 0, 0, time.UTC)
			if t.Day() == day {
				return t, t.Format("2006-01-02"), nil
			}
		}
	}
	ymd := regexp.MustCompile(`^(\d{4})[-\s]+([A-Za-z]+)[-\s]+(\d{1,2})$`)
	if m := ymd.FindStringSubmatch(dateStr); m != nil {
		year, _ := strconv.Atoi(m[1])
		monStr := strings.ToLower(m[2])
		day, _ := strconv.Atoi(m[3])
		if mon, ok := monthMap[monStr]; ok {
			t := time.Date(year, time.Month(mon), day, 0, 0, 0, 0, time.UTC)
			if t.Day() == day {
				return t, t.Format("2006-01-02"), nil
			}
		}
	}

	lower := strings.ToLower(dateStr)
	now := time.Now().UTC()
	switch lower {
	case "today":
		t := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return t, t.Format("2006-01-02"), nil
	case "yesterday":
		t := now.AddDate(0, 0, -1)
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return t, t.Format("2006-01-02"), nil
	case "tomorrow":
		t := now.AddDate(0, 0, 1)
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return t, t.Format("2006-01-02"), nil
	}

	daysAgo := regexp.MustCompile(`^(\d+)\s+days?\s+ago$`)
	if m := daysAgo.FindStringSubmatch(lower); m != nil {
		days, _ := strconv.Atoi(m[1])
		t := now.AddDate(0, 0, -days)
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return t, t.Format("2006-01-02"), nil
	}

	return time.Time{}, "", fmt.Errorf("unable to parse date: %s", dateStr)
}

func buildDateFilters(before, after, on, during string) (map[string]string, error) {
	out := make(map[string]string)
	if on != "" {
		if during != "" || before != "" || after != "" {
			return nil, fmt.Errorf("'on' cannot be combined with other date filters")
		}
		_, normalized, err := parseFlexibleDate(on)
		if err != nil {
			return nil, fmt.Errorf("invalid 'on' date: %v", err)
		}
		out["on"] = normalized
		return out, nil
	}
	if during != "" {
		if before != "" || after != "" {
			return nil, fmt.Errorf("'during' cannot be combined with 'before' or 'after'")
		}
		_, normalized, err := parseFlexibleDate(during)
		if err != nil {
			return nil, fmt.Errorf("invalid 'during' date: %v", err)
		}
		out["during"] = normalized
		return out, nil
	}
	if after != "" {
		_, normalized, err := parseFlexibleDate(after)
		if err != nil {
			return nil, fmt.Errorf("invalid 'after' date: %v", err)
		}
		out["after"] = normalized
	}
	if before != "" {
		_, normalized, err := parseFlexibleDate(before)
		if err != nil {
			return nil, fmt.Errorf("invalid 'before' date: %v", err)
		}
		out["before"] = normalized
	}
	if after != "" && before != "" {
		a, _, _ := parseFlexibleDate(after)
		b, _, _ := parseFlexibleDate(before)
		if a.After(b) {
			return nil, fmt.Errorf("'after' date is after 'before' date")
		}
	}
	return out, nil
}

func isFilterKey(key string) bool {
	_, ok := validFilterKeys[strings.ToLower(key)]
	return ok
}

func splitQuery(q string) (freeText []string, filters map[string][]string) {
	filters = make(map[string][]string)
	for _, tok := range strings.Fields(q) {
		parts := strings.SplitN(tok, ":", 2)
		if len(parts) == 2 && isFilterKey(parts[0]) {
			key := strings.ToLower(parts[0])
			filters[key] = append(filters[key], parts[1])
		} else {
			freeText = append(freeText, tok)
		}
	}
	return
}

func addFilter(filters map[string][]string, key, val string) {
	for _, existing := range filters[key] {
		if existing == val {
			return
		}
	}
	filters[key] = append(filters[key], val)
}

func buildQuery(freeText []string, filters map[string][]string) string {
	var out []string
	out = append(out, freeText...)
	for _, key := range []string{"is", "in", "from", "with", "before", "after", "on", "during"} {
		for _, val := range filters[key] {
			out = append(out, fmt.Sprintf("%s:%s", key, val))
		}
	}
	return strings.Join(out, " ")
}

func hasImageBlocks(blocks slack.Blocks) bool {
	for _, block := range blocks.BlockSet {
		if block.BlockType() == slack.MBTImage {
			return true
		}
	}
	return false
}
