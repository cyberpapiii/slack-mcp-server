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

// ConversationsGetMessageHandler fetches one message by channel + timestamp
// (MsgID from compact CSV / attachment-truncation receipt; optional detail:full).
func (ch *ConversationsHandler) ConversationsGetMessageHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	rawChannel := request.GetString("channel_id", "")
	if rawChannel == "" {
		return nil, invalidArguments("channel_id is required")
	}
	timestamp := request.GetString("timestamp", "")
	if timestamp == "" {
		return nil, invalidArguments("timestamp is required")
	}
	mode, err := ch.settings.outputMode(request)
	if err != nil {
		return nil, err
	}
	channel, err := ch.messages.channelID(ctx, rawChannel)
	if err != nil {
		ch.logger.Debug("Channel not found", zap.String("channel", rawChannel), zap.Error(err))
		return nil, err
	}

	msg, err := ch.messages.message(ctx, channel, timestamp)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, notFound(fmt.Sprintf("no message at timestamp %s in %s; timestamps must match exactly (for example 1712345678.123456)", timestamp, channel))
	}

	messages := ch.messages.rows(ctx, []slack.Message{*msg}, channel, true, mode)
	return marshalMessagesToCSV(messages, ch.messages.render(mode, SlackResultMeta("", false, "")))
}

func (ch *ConversationsHandler) ConversationsHistoryHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolConversations(ctx, request)
	if err != nil {
		ch.logger.Debug("Failed to parse history params", zap.Error(err))
		return nil, err
	}
	mode, err := ch.settings.outputMode(request)
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
	history, err := ch.apiProvider.WebAPI().GetConversationHistoryContext(ctx, &historyParams)
	if err != nil {
		ch.logger.Debug("GetConversationHistoryContext failed", zap.Error(err))
		return nil, err
	}

	ch.logger.Debug("Fetched conversation history", zap.Int("message_count", len(history.Messages)))

	messages := ch.messages.rows(ctx, history.Messages, params.channel, params.activity, mode)

	nextCursor := ""
	if history.HasMore {
		nextCursor = history.ResponseMetaData.NextCursor
	}
	return marshalMessagesToCSV(messages, ch.messages.render(mode, SlackResultMeta(nextCursor, false, "")))
}

// ConversationsRepliesHandler streams thread replies as CSV
func (ch *ConversationsHandler) ConversationsRepliesHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolConversations(ctx, request)
	if err != nil {
		ch.logger.Debug("Failed to parse replies params", zap.Error(err))
		return nil, err
	}
	mode, err := ch.settings.outputMode(request)
	if err != nil {
		return nil, err
	}
	threadTs := request.GetString("thread_ts", "")
	if threadTs == "" {
		ch.logger.Debug("thread_ts not provided for replies", zap.String("thread_ts", threadTs))
		return nil, invalidArguments("thread_ts must be a string")
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
	replies, hasMore, nextCursor, err := ch.apiProvider.WebAPI().GetConversationRepliesContext(ctx, &repliesParams)
	if err != nil {
		ch.logger.Debug("GetConversationRepliesContext failed", zap.Error(err))
		return nil, err
	}
	ch.logger.Debug("Fetched conversation replies", zap.Int("count", len(replies)))

	messages := ch.messages.rows(ctx, replies, params.channel, params.activity, mode)
	if !hasMore {
		nextCursor = ""
	}
	return marshalMessagesToCSV(messages, ch.messages.render(mode, SlackResultMeta(nextCursor, false, "")))
}

func (ch *ConversationsHandler) ConversationsSearchHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolSearch(ctx, request)
	if err != nil {
		ch.logger.Debug("Failed to parse search params", zap.Error(err))
		return nil, err
	}
	mode, err := ch.settings.outputMode(request)
	if err != nil {
		return nil, err
	}
	ch.logger.Debug("Search params parsed", zap.String("query", params.query), zap.Int("limit", params.limit), zap.Int("page", params.page))

	searchParams := slack.SearchParameters{
		Sort:          params.sort,
		SortDirection: slack.DEFAULT_SEARCH_SORT_DIR,
		Highlight:     false,
		Count:         params.limit,
		Page:          params.page,
	}

	messagesRes, _, err := ch.apiProvider.WebAPI().SearchContext(ctx, params.query, searchParams)
	if err != nil {
		ch.logger.Debug("Slack SearchContext failed", zap.Error(err))
		return nil, err
	}
	ch.logger.Debug("Search completed", zap.Int("matches", len(messagesRes.Matches)))

	messages := ch.messages.searchRows(ctx, messagesRes.Matches, mode)
	nextCursor := ""
	if messagesRes.Pagination.Page < messagesRes.Pagination.PageCount {
		nextCursor = encodePageCursor(messagesRes.Pagination.Page + 1)
	}
	return marshalMessagesToCSV(messages, ch.messages.render(mode, SlackResultMeta(nextCursor, false, "")))
}

func (ch *ConversationsHandler) parseParamsToolConversations(ctx context.Context, request mcp.CallToolRequest) (*conversationParams, error) {
	channel := request.GetString("channel_id", "")
	if channel == "" {
		ch.logger.Debug("channel_id missing in conversations params")
		return nil, invalidArguments("channel_id must be a string")
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
	isDurationLimit := strings.HasSuffix(limit, "h") || strings.HasSuffix(limit, "d") || strings.HasSuffix(limit, "w") || strings.HasSuffix(limit, "m")
	if isDurationLimit && cursor != "" {
		// A cursor carries its own position; a time window would fight it.
		ch.logger.Debug("Ignoring duration limit because a cursor was provided",
			zap.String("limit", limit),
			zap.String("cursor", cursor),
		)
	} else if isDurationLimit {
		paramLimit, paramOldest, paramLatest, err = limitByExpression(limit)
		if err != nil {
			ch.logger.Debug("Invalid duration limit", zap.String("limit", limit), zap.Error(err))
			return nil, err
		}
	} else if cursor == "" {
		paramLimit, err = limitByNumeric(limit, defaultConversationsNumericLimit)
		if err != nil {
			ch.logger.Debug("Invalid numeric limit", zap.String("limit", limit), zap.Error(err))
			return nil, err
		}
	}

	channel, err = ch.messages.channelID(ctx, channel)
	if err != nil {
		return nil, err
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

func (ch *ConversationsHandler) parseParamsToolSearch(ctx context.Context, req mcp.CallToolRequest) (*searchParams, error) {
	rawQuery := strings.TrimSpace(req.GetString("search_query", ""))
	freeText, filters := splitQuery(rawQuery)

	if req.GetBool("filter_threads_only", false) {
		addFilter(filters, "is", "thread")
	}
	if has := req.GetString("filter_has", ""); has != "" {
		addFilter(filters, "has", has)
	}
	if chName := req.GetString("filter_in_channel", ""); chName != "" {
		f, err := ch.channelFilter(chName)
		if err != nil {
			ch.logger.Debug("Invalid channel filter", zap.String("filter", chName), zap.Error(err))
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
		if kind := provider.ClassifyID(im); kind == provider.IDDirectMessage || kind == provider.IDGroup {
			f, err = conversationFilter(ch.apiProvider.Directory(), im)
		} else {
			f, err = ch.userFilter(ctx, im)
		}
		if err != nil {
			ch.logger.Debug("Invalid IM/MPIM filter", zap.String("filter", im), zap.Error(err))
			return nil, err
		}
		addFilter(filters, "in", f)
	}
	if with := req.GetString("filter_users_with", ""); with != "" {
		f, err := ch.userFilter(ctx, with)
		if err != nil {
			ch.logger.Debug("Invalid with-user filter", zap.String("filter", with), zap.Error(err))
			return nil, err
		}
		addFilter(filters, "with", f)
	}
	if from := req.GetString("filter_users_from", ""); from != "" {
		f, err := ch.userFilter(ctx, from)
		if err != nil {
			ch.logger.Debug("Invalid from-user filter", zap.String("filter", from), zap.Error(err))
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
		ch.logger.Debug("Invalid date filters", zap.Error(err))
		return nil, err
	}
	for key, val := range dateMap {
		addFilter(filters, key, val)
	}

	finalQuery := buildQuery(freeText, filters)
	limit := pageLimit(req, defaultSearchMessagesLimit, maxSearchMessagesLimit)

	sort := req.GetString("sort", "score")
	if sort != "score" && sort != "timestamp" {
		return nil, invalidArguments(fmt.Sprintf("invalid sort: %q (must be 'score' or 'timestamp')", sort))
	}

	page, err := decodePageCursor(req.GetString("cursor", ""))
	if err != nil {
		return nil, err
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
		sort:  sort,
	}, nil
}

// Slack user IDs may begin with U or W: https://docs.slack.dev/changelog/2016/08/11/user-id-format-changes
