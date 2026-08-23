package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	slackGoUtil "github.com/takara2314/slack-go-util"
	"go.uber.org/zap"
)

func (ch *ConversationsHandler) ConversationsAddMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsAddMessageHandler called", request)

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
		// Text alongside blocks is notification/fallback only.
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
	respChannel, respTimestamp, err := ch.apiProvider.WebAPI().PostMessageContext(ctx, params.channel, options...)
	if err != nil {
		ch.logger.Error("Slack PostMessageContext failed", zap.Error(err))
		return nil, err
	}

	toolConfig := os.Getenv("SLACK_MCP_ADD_MESSAGE_MARK")
	if envutil.IsTruthy(toolConfig) {
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

// ConversationsDraftMessageHandler validates and formats a message without sending.
// Preview for review before conversations_add_message.
func (ch *ConversationsHandler) ConversationsDraftMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsDraftMessageHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolDraftMessage(ctx, request)
	if err != nil {
		ch.logger.Error("Failed to parse draft-message params", zap.Error(err))
		return nil, err
	}

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

	// Labeled sections avoid delimiter ambiguity when message text looks similar.
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

	err = ch.apiProvider.WebAPI().AddReactionContext(ctx, params.emoji, itemRef)
	if err != nil {
		ch.logger.Error("Slack AddReactionContext failed", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully added :%s: reaction to message %s in channel %s", params.emoji, params.timestamp, params.channel)), nil
}

// ReactionsRemoveHandler removes an emoji reaction from a message
func (ch *ConversationsHandler) ReactionsRemoveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ReactionsRemoveHandler called", request)

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

	err = ch.apiProvider.WebAPI().RemoveReactionContext(ctx, params.emoji, itemRef)
	if err != nil {
		ch.logger.Error("Slack RemoveReactionContext failed", zap.Error(err))
		return nil, err
	}

	fallback := fmt.Sprintf("Successfully removed :%s: reaction from message %s in channel %s", params.emoji, params.timestamp, params.channel)
	return NewStructuredResult(ActionData{Action: "remove_reaction", Status: "removed", ChannelID: params.channel, MessageID: params.timestamp}, SlackResultMeta("", false, ""), fallback), nil
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

	msg, err := ch.fetchMessageByTimestamp(ctx, channel, timestamp)
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

// fetchMessageByTimestamp retrieves a single message by timestamp using conversations.replies.
// Works for top-level messages, thread parents, and thread replies; needs only channels:history.
func (ch *ConversationsHandler) fetchMessageByTimestamp(ctx context.Context, channel, timestamp string) (*slack.Message, error) {
	msgs, _, _, err := ch.apiProvider.WebAPI().GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
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

// ConversationsGetMessageHandler fetches one message by channel + timestamp
// (MsgID from compact CSV / attachment-truncation receipt; optional detail:full).

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
	if !isChannelAllowedForConfig(channel, toolConfig) {
		ch.logger.Warn("Add-message tool not allowed for channel", zap.String("channel", channel), zap.String("policy", toolConfig))
		return nil, fmt.Errorf("conversations_add_message is not allowed for channel %q by SLACK_MCP_ADD_MESSAGE_TOOL", channel)
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
			// Raw JSON array/object passed directly; re-marshal to bytes.
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
		return nil, fmt.Errorf("reactions tools are not allowed for channel %q by SLACK_MCP_REACTION_TOOL", channel)
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
