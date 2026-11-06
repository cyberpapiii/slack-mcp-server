package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	slackGoUtil "github.com/takara2314/slack-go-util"
	"go.uber.org/zap"
)

func (ch *ConversationsHandler) ConversationsAddMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolAddMessage(ctx, request)
	if err != nil {
		ch.logger.Debug("Failed to parse add-message params", zap.Error(err))
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
		options = append(options, ch.messageBody(params).options...)
	}

	if ch.settings.unfurls(params.text, ch.logger) {
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
	respChannel, respTimestamp, err := ch.api.PostMessageContext(ctx, params.channel, options...)
	if err != nil {
		ch.logger.Debug("Slack PostMessageContext failed", zap.Error(err))
		return nil, err
	}

	posted := fmt.Sprintf("Successfully posted message to channel %s (ts=%s)", respChannel, respTimestamp)
	if params.threadTs != "" {
		posted = fmt.Sprintf("Successfully posted message to channel %s in thread %s (ts=%s)", respChannel, params.threadTs, respTimestamp)
	}
	// The message is already posted, so a failed mark is reported, not
	// returned as an error a caller would retry into a duplicate post. A bot
	// has no read state to mark.
	if ch.settings.MarkAfterPost && ch.api.Identity().IsUser() {
		if err := ch.api.MarkConversationContext(ctx, params.channel, respTimestamp); err != nil {
			ch.logger.Debug("Slack MarkConversationContext failed", zap.Error(err))
			posted += fmt.Sprintf(". Warning: marking the channel read failed: %v", err)
		}
	}
	return mcp.NewToolResultText(posted), nil
}

// ConversationsDraftMessageHandler validates and formats a message without sending.
// Preview for review before conversations_add_message.
func (ch *ConversationsHandler) ConversationsDraftMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	if request.GetString("text", "") == "" {
		return nil, invalidArguments("text is required and must not be empty")
	}
	params, err := ch.parseMessageParams(ctx, request)
	if err != nil {
		ch.logger.Debug("Failed to parse draft-message params", zap.Error(err))
		return nil, err
	}

	formatStatus := ch.messageBody(params).format

	sendability := ch.sendStatus(params.channel)

	// Labeled sections avoid delimiter ambiguity when message text looks similar.
	preview := fmt.Sprintf("[Draft message preview]\n"+
		"Channel: %s\n"+
		"Thread: %s\n"+
		"Format: %s\n"+
		"Send status: %s\n\n"+
		"[Message text]\n"+
		"%s\n\n"+
		"[End of draft]%s",
		params.channel,
		formatThreadTs(params.threadTs),
		formatStatus,
		sendability,
		params.text,
		ch.availability.toolHint(capability.ConversationsAddMessage, "\nTo send this message, use %s with the same parameters.", ""),
	)

	return mcp.NewToolResultText(preview), nil
}

func (ch *ConversationsHandler) ReactionsAddHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolReaction(ctx, capability.ReactionsAdd, request)
	if err != nil {
		ch.logger.Debug("Failed to parse add-reaction params", zap.Error(err))
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

	err = ch.api.AddReactionContext(ctx, params.emoji, itemRef)
	if err != nil {
		ch.logger.Debug("Slack AddReactionContext failed", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(fmt.Sprintf("Successfully added :%s: reaction to message %s in channel %s", params.emoji, params.timestamp, params.channel)), nil
}

func (ch *ConversationsHandler) ReactionsRemoveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolReaction(ctx, capability.ReactionsRemove, request)
	if err != nil {
		ch.logger.Debug("Failed to parse remove-reaction params", zap.Error(err))
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

	err = ch.api.RemoveReactionContext(ctx, params.emoji, itemRef)
	if err != nil {
		ch.logger.Debug("Slack RemoveReactionContext failed", zap.Error(err))
		return nil, err
	}

	fallback := fmt.Sprintf("Successfully removed :%s: reaction from message %s in channel %s", params.emoji, params.timestamp, params.channel)
	return NewStructuredResult(ActionData{Action: "remove_reaction", Status: "removed", ChannelID: params.channel, MessageID: params.timestamp}, slackMeta(), fallback), nil
}

// ReactionsGetHandler returns detailed reaction data (including user IDs) for a specific message.
// Uses conversations.replies which works for top-level messages, thread parents, and thread replies
// alike. Requires only channels:history scope, no additional reactions:read permission needed.
func (ch *ConversationsHandler) ReactionsGetHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	rawChannel := request.GetString("channel_id", "")
	if rawChannel == "" {
		return nil, invalidArguments("channel_id is required")
	}
	channel, err := ch.messages.channelID(ctx, rawChannel)
	if err != nil {
		ch.logger.Debug("Channel not found", zap.String("channel", rawChannel), zap.Error(err))
		return nil, err
	}

	timestamp := request.GetString("timestamp", "")
	if timestamp == "" {
		return nil, invalidArguments("timestamp is required")
	}

	ch.logger.Debug("Fetching reactions for message",
		zap.String("channel", channel),
		zap.String("timestamp", timestamp),
	)

	msg, err := ch.messages.message(ctx, channel, timestamp)
	if err != nil {
		return nil, err
	}

	rows := make([]reactionRow, 0, len(msg.Reactions))
	for _, r := range msg.Reactions {
		rows = append(rows, reactionRow{
			Emoji: r.Name,
			Count: r.Count,
			Users: strings.Join(r.Users, ";"),
		})
	}

	return csvResult("", slackMeta(), &rows)
}

// messageBody is how a message's text goes to Slack, and how the draft
// preview names it: Block Kit blocks for markdown, plain text when the
// content type is plain or the markdown does not parse.
type messageBody struct {
	options []slack.MsgOption
	format  string
}

func (ch *ConversationsHandler) messageBody(params *addMessageParams) messageBody {
	plain := []slack.MsgOption{slack.MsgOptionDisableMarkdown(), slack.MsgOptionText(params.text, false)}
	if params.contentType == "text/plain" {
		return messageBody{options: plain, format: "plain_text"}
	}
	blocks, err := slackGoUtil.ConvertMarkdownTextToBlocks(params.text)
	if err != nil {
		ch.logger.Warn("Markdown parsing error", zap.Error(err))
		return messageBody{options: plain, format: "plain_text (markdown parse failed, will send as plain text)"}
	}
	return messageBody{options: []slack.MsgOption{slack.MsgOptionBlocks(blocks...)}, format: fmt.Sprintf("markdown (%d block(s))", len(blocks))}
}

// parseMessageParams reads what conversations_add_message and
// conversations_draft_message share: the destination, thread, text and
// content type.
func (ch *ConversationsHandler) parseMessageParams(ctx context.Context, request mcp.CallToolRequest) (*addMessageParams, error) {
	if err := signpostFileParam(request, ch.availability); err != nil {
		return nil, err
	}

	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, invalidArguments("channel_id must be a string")
	}
	channel, err := ch.messages.channelID(ctx, channel)
	if err != nil {
		ch.logger.Debug("Channel not found", zap.String("channel", channel), zap.Error(err))
		return nil, err
	}

	threadTs := request.GetString("thread_ts", "")
	if threadTs != "" && !validSlackTimestamp(threadTs) {
		return nil, invalidArguments("thread_ts must be a valid timestamp in format 1234567890.123456")
	}

	contentType := request.GetString("content_type", "text/markdown")
	if contentType != "text/plain" && contentType != "text/markdown" {
		return nil, invalidArguments("content_type must be either 'text/plain' or 'text/markdown'")
	}

	return &addMessageParams{
		channel:     channel,
		threadTs:    threadTs,
		text:        request.GetString("text", ""),
		contentType: contentType,
	}, nil
}

func (ch *ConversationsHandler) parseParamsToolAddMessage(ctx context.Context, request mcp.CallToolRequest) (*addMessageParams, error) {
	params, err := ch.parseMessageParams(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := ch.settings.requireChannel(capability.ConversationsAddMessage, params.channel); err != nil {
		return nil, err
	}

	// blocks arrives as a JSON string or, parsed by the MCP SDK, as a raw array.
	if rawBlocks, ok := request.GetArguments()["blocks"]; ok && rawBlocks != nil {
		var blocksJSON []byte
		switch v := rawBlocks.(type) {
		case string:
			if v != "" {
				blocksJSON = []byte(v)
			}
		default:
			blocksJSON, err = json.Marshal(v)
			if err != nil {
				return nil, invalidBlocks(err)
			}
		}
		if blocksJSON != nil {
			var slackBlocks slack.Blocks
			if err := json.Unmarshal(blocksJSON, &slackBlocks); err != nil {
				return nil, invalidBlocks(err)
			}
			params.blocks = slackBlocks.BlockSet
		}
	}

	if params.text == "" && params.blocks == nil {
		return nil, invalidArguments("either text or blocks must be provided")
	}
	return params, nil
}

func invalidBlocks(err error) error {
	return &ToolError{Code: codeInvalidArguments, Message: "blocks must be valid Slack Block Kit JSON: " + err.Error(), Cause: err}
}

func (ch *ConversationsHandler) parseParamsToolReaction(ctx context.Context, tool string, request mcp.CallToolRequest) (*addReactionParams, error) {
	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, invalidArguments("channel_id is required")
	}
	channel, err := ch.messages.channelID(ctx, channel)
	if err != nil {
		ch.logger.Debug("Channel not found", zap.String("channel", channel), zap.Error(err))
		return nil, err
	}
	if err := ch.settings.requireChannel(tool, channel); err != nil {
		return nil, err
	}

	timestamp := request.GetString("timestamp", "")
	if timestamp == "" {
		return nil, invalidArguments("timestamp is required")
	}

	emoji := strings.Trim(request.GetString("emoji", ""), ":")
	if emoji == "" {
		return nil, invalidArguments("emoji is required")
	}

	return &addReactionParams{
		channel:   channel,
		timestamp: timestamp,
		emoji:     emoji,
	}, nil
}
