package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

func (ch *ConversationsHandler) ConversationsMarkHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolMark(ctx, request)
	if err != nil {
		ch.logger.Debug("Failed to parse mark params", zap.Error(err))
		return nil, err
	}

	channel := params.channel
	ts := params.ts

	if ts == "" {
		// No explicit ts: mark the channel read up to its newest message.
		historyParams := slack.GetConversationHistoryParameters{
			ChannelID: channel,
			Limit:     1,
		}
		history, err := ch.api.GetConversationHistoryContext(ctx, &historyParams)
		if err != nil {
			ch.logger.Debug("Failed to get latest message", zap.Error(err))
			return nil, fmt.Errorf("failed to get latest message: %w", err)
		}
		if len(history.Messages) > 0 {
			ts = history.Messages[0].Timestamp
		} else {
			return NewStructuredResult(ActionData{Action: "mark_read", Status: "no_messages", ChannelID: channel}, slackMeta(), "No messages to mark as read"), nil
		}
	}

	err = ch.api.MarkConversationContext(ctx, channel, ts)
	if err != nil {
		ch.logger.Debug("Failed to mark conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to mark conversation as read: %w", err)
	}

	ch.logger.Info("Marked conversation as read",
		zap.String("channel", channel),
		zap.String("ts", ts))

	fallback := fmt.Sprintf("Marked %s as read up to %s", channel, ts)
	return NewStructuredResult(ActionData{Action: "mark_read", Status: "marked", ChannelID: channel, MessageID: ts}, slackMeta(), fallback), nil
}

func (ch *ConversationsHandler) ConversationsLeaveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, invalidArguments("channel_id is required")
	}

	channel, err := ch.messages.channelID(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve channel: %w", err)
	}

	notInChannel, err := ch.api.LeaveConversationContext(ctx, channel)
	if err != nil {
		ch.logger.Debug("Failed to leave conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to leave conversation: %w", err)
	}

	if notInChannel {
		ch.logger.Info("Was not in channel", zap.String("channel", channel))
		return mcp.NewToolResultText(fmt.Sprintf("Not a member of %s", channel)), nil
	}

	ch.logger.Info("Left conversation", zap.String("channel", channel))
	return mcp.NewToolResultText(fmt.Sprintf("Successfully left %s", channel)), nil
}

func (ch *ConversationsHandler) ConversationsJoinHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, invalidArguments("channel_id is required")
	}

	channel, err := ch.messages.channelID(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve channel: %w", err)
	}

	_, _, _, err = ch.api.JoinConversationContext(ctx, channel)
	if err != nil {
		ch.logger.Debug("Failed to join conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to join conversation: %w", err)
	}

	ch.logger.Info("Joined conversation", zap.String("channel", channel))
	return mcp.NewToolResultText(fmt.Sprintf("Successfully joined %s", channel)), nil
}

func (ch *ConversationsHandler) parseParamsToolOpenConversation(request mcp.CallToolRequest) ([]string, error) {
	usersStr := request.GetString("users", "")
	if usersStr == "" {
		return nil, invalidArguments("users must be a comma-separated string of user IDs or @usernames")
	}

	var userIDs []string
	for _, user := range strings.Split(usersStr, ",") {
		user = strings.TrimSpace(user)
		if user == "" {
			continue
		}
		if id, _ := provider.ParseUserRef(user); id != "" {
			userIDs = append(userIDs, id)
			continue
		}
		u, err := ch.api.Directory().UserByRef(user)
		if err != nil {
			return nil, cacheMiss(user, err, ch.availability.userNotFound)
		}
		userIDs = append(userIDs, u.ID)
	}

	if len(userIDs) == 0 {
		return nil, invalidArguments("no valid users provided")
	}

	return userIDs, nil
}

func (ch *ConversationsHandler) ConversationsOpenHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	userIDs, err := ch.parseParamsToolOpenConversation(request)
	if err != nil {
		ch.logger.Debug("Failed to parse open-conversation params", zap.Error(err))
		return nil, err
	}

	params := &slack.OpenConversationParameters{
		Users:    userIDs,
		ReturnIM: true,
	}

	channel, _, _, err := ch.api.OpenConversationContext(ctx, params)
	if err != nil {
		ch.logger.Debug("Slack OpenConversationContext failed", zap.Error(err))
		return nil, err
	}

	ch.api.UpsertChannel(channel)

	return mcp.NewToolResultText(fmt.Sprintf("Successfully opened conversation. Channel ID: %s", channel.Conversation.ID)), nil
}

func (ch *ConversationsHandler) parseParamsToolMark(ctx context.Context, request mcp.CallToolRequest) (*markParams, error) {

	channel := request.GetString("channel_id", "")
	if channel == "" {
		ch.logger.Debug("channel_id missing in mark params")
		return nil, invalidArguments("channel_id is required")
	}

	channel, err := ch.messages.channelID(ctx, channel)
	if err != nil {
		return nil, err
	}

	ts := request.GetString("timestamp", "")

	return &markParams{
		channel: channel,
		ts:      ts,
	}, nil
}
