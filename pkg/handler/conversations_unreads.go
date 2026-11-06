package handler

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// UnreadChannel is one summary row of conversations_unreads. LastRead is the
// oldest bound for a conversations_history follow-up; conversations_mark
// needs no timestamp, so Latest is not carried.
type UnreadChannel struct {
	ChannelID   string `csv:"Channel" json:"channel_id"`
	ChannelName string `csv:"Name" json:"channel_name"`
	ChannelType string `csv:"Type" json:"channel_type"` // "dm", "group_dm", "partner", "internal"
	UnreadCount int    `csv:"UnreadCount" json:"unread_count"`
	LastRead    string `csv:"LastRead" json:"last_read"`
}

func (ch *ConversationsHandler) ConversationsUnreadsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolUnreads(request)
	if err != nil {
		ch.logger.Debug("Failed to parse unreads params", zap.Error(err))
		return nil, err
	}

	mode, err := ch.settings.outputMode(request)
	if err != nil {
		return nil, err
	}
	// The mute prefs and client.counts are independent reads; overlap them.
	var mutedChannels map[string]bool
	var mutedErr error
	var wg sync.WaitGroup
	if !params.includeMuted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mutedChannels, mutedErr = ch.apiProvider.Slack().GetMutedChannels(ctx)
		}()
	}
	counts, err := ch.apiProvider.Slack().ClientCounts(ctx)
	wg.Wait()

	if !params.includeMuted {
		if mutedErr != nil {
			ch.logger.Warn("Failed to fetch muted channels, proceeding without mute filter", zap.Error(mutedErr))
			params.mutedUnavailable = true
		} else if len(mutedChannels) > 0 {
			params.mutedChannels = mutedChannels
			ch.logger.Debug("Loaded muted channels", zap.Int("count", len(mutedChannels)))
		}
	}

	if err != nil {
		ch.logger.Debug("ClientCounts failed", zap.Error(err))
		return nil, fmt.Errorf("get Slack unread state: %w", err)
	}

	return ch.processClientCountsResponse(ctx, request, params, counts, mode)
}

func (ch *ConversationsHandler) processClientCountsResponse(ctx context.Context, request mcp.CallToolRequest, params *unreadsParams, counts provider.UnreadCounts, mode text.OutputMode) (*mcp.CallToolResult, error) {
	ch.logger.Debug("Got counts data",
		zap.Int("channels", len(counts.Channels)),
		zap.Int("mpims", len(counts.MPIMs)),
		zap.Int("ims", len(counts.IMs)))

	unreadChannels, dropped := ch.collectUnreadChannels(params, counts, ch.apiProvider.Directory())

	ch.logger.Debug("Found unread channels", zap.Int("count", len(unreadChannels)))

	if !ch.backfillUnreadCounts(ctx, request, ch.apiProvider.WebAPI(), params, unreadChannels) {
		return nil, ctx.Err()
	}

	coverage := unreadsCoverage{mutedUnavailable: params.mutedUnavailable, maxChannels: params.maxChannels, dropped: dropped}

	if !params.includeMessages {
		// Rows carry their own Name column, so no "#channels:" legend.
		return csvResult("", coverage.meta(), &unreadChannels)
	}

	histories, errs := fetchUnreadHistories(ctx, ch.apiProvider.WebAPI(), unreadChannels, params.maxMessagesPerChannel,
		func(c UnreadChannel) bool { return c.LastRead == "" },
		func(finished int) {
			sendProgress(ctx, request, finished, len(unreadChannels), fmt.Sprintf("Fetching messages: %d of %d channels", finished, len(unreadChannels)))
		})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var allMessages []Message
	for i := range unreadChannels {
		if unreadChannels[i].LastRead == "" {
			// No bound: unbounded history would mislead. Same conservative 1
			// as the summary/backfill path.
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, nil)
			coverage.unbounded = append(coverage.unbounded, unreadChannels[i].ChannelID)
			continue
		}
		if err := errs[i]; err != nil {
			ch.logger.Warn("Failed to get history for channel",
				zap.String("channel", unreadChannels[i].ChannelID),
				zap.Error(err))
			// Failed fetch must not render 0 unread when HasUnreads was true.
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, err)
			coverage.failed = append(coverage.failed, unreadChannels[i].ChannelID)
			continue
		}
		history := histories[i]

		// Zero-row window still reports ≥1 when HasUnreads was true.
		unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, len(history.Messages), nil)

		channelMessages := ch.messages.rows(ctx, history.Messages, unreadChannels[i].ChannelID, false, mode)
		allMessages = append(allMessages, channelMessages...)
	}

	ch.logger.Debug("Fetched unread messages", zap.Int("total", len(allMessages)))

	return marshalMessagesToCSV(allMessages, ch.messages.render(mode, coverage.meta()))
}

// unreadsCoverage records why a conversations_unreads page may under-report
// and renders that as the result's partial meta.
type unreadsCoverage struct {
	mutedUnavailable bool
	maxChannels      int
	dropped          int      // unread channels cut by max_channels
	failed           []string // channels whose history fetch failed
	unbounded        []string // channels with no last_read, so no history window
}

func (c unreadsCoverage) meta() ResultMeta {
	var reasons []string
	if c.mutedUnavailable {
		reasons = append(reasons, "muted-channel preferences unavailable")
	}
	if c.dropped > 0 {
		reasons = append(reasons, fmt.Sprintf("max_channels=%d reached, %d more unread channels not listed", c.maxChannels, c.dropped))
	}
	if len(c.failed) > 0 {
		reasons = append(reasons, "history fetch failed for "+strings.Join(c.failed, " "))
	}
	if len(c.unbounded) > 0 {
		reasons = append(reasons, "no last-read bound for "+strings.Join(c.unbounded, " ")+" (messages not fetched)")
	}
	reason := strings.Join(reasons, "; ")
	return SlackResultMeta("", reason != "", reason)
}

func unreadCountFromHistory(current, msgCount int, fetchErr error) int {
	if fetchErr != nil {
		if current == 0 {
			return 1
		}
		return current
	}
	if msgCount == 0 {
		if current > 0 {
			return current
		}
		return 1
	}
	return msgCount
}

// historyFetcher is the single method of provider.SlackAPI that the unread-count
// backfill needs. It exists so tests can substitute a call-counting fake;
// production passes ch.apiProvider.WebAPI().
type historyFetcher interface {
	GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
}

// fetchUnreadHistories reads each channel's messages newer than its LastRead,
// fanOutWorkers at a time, retrying once on a 429. Channels skip selects get a
// nil history and a nil error. Results are in channel order.
func fetchUnreadHistories(ctx context.Context, api historyFetcher, channels []UnreadChannel, limit int, skip func(UnreadChannel) bool, done func(finished int)) ([]*slack.GetConversationHistoryResponse, []error) {
	return fetchInOrder(ctx, len(channels), fanOutWorkers, func(ctx context.Context, i int) (*slack.GetConversationHistoryResponse, error) {
		c := channels[i]
		if skip(c) {
			return nil, nil
		}
		return limiter.CallWithRetry(ctx, nil, 1, func() (*slack.GetConversationHistoryResponse, error) {
			return api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
				ChannelID: c.ChannelID,
				Oldest:    c.LastRead,
				Limit:     limit,
				Inclusive: false,
			})
		})
	}, done)
}

// backfillUnreadCounts fills in UnreadCount for channels that client.counts
// reported as unread with MentionCount==0. It mutates unreadChannels in place
// and reports false if the context was cancelled mid-flight.
func (ch *ConversationsHandler) backfillUnreadCounts(ctx context.Context, request mcp.CallToolRequest, api historyFetcher, params *unreadsParams, unreadChannels []UnreadChannel) bool {
	if params.includeMessages {
		// Message-fetch loop overwrites UnreadCount; backfill would double API calls.
		return true
	}

	// client.counts gives HasUnreads without a count when MentionCount==0;
	// history since LastRead is used because conversations.info omits unread_count for xoxc/xoxd.
	const backfillLimit = 20
	histories, errs := fetchUnreadHistories(ctx, api, unreadChannels, backfillLimit,
		func(c UnreadChannel) bool { return c.UnreadCount > 0 || c.LastRead == "" },
		func(finished int) {
			sendProgress(ctx, request, finished, len(unreadChannels), fmt.Sprintf("Backfilling unread counts: %d of %d channels", finished, len(unreadChannels)))
		})
	if ctx.Err() != nil {
		return false
	}
	backfilled := 0
	for i := range unreadChannels {
		switch {
		case unreadChannels[i].UnreadCount > 0:
			continue // already counted (mentions)
		case unreadChannels[i].LastRead == "":
			// No last-read bound: skip the unbounded history query and
			// report 1, since HasUnreads was true.
			unreadChannels[i].UnreadCount = 1
			backfilled++
		case errs[i] != nil:
			ch.logger.Debug("Failed to backfill unread count",
				zap.String("channel", unreadChannels[i].ChannelID),
				zap.Error(errs[i]))
			// Same conservative 1 as the include_messages path.
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, errs[i])
		default:
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, len(histories[i].Messages), nil)
			backfilled++
		}
	}
	if backfilled > 0 {
		ch.logger.Debug("Backfilled unread counts via conversations.history",
			zap.Int("backfilled", backfilled))
	}
	return true
}

// collectUnreadChannels turns a client.counts snapshot into the sorted, limited
// list of unread channels plus the count cut by max_channels. Pure: no API
// calls. The caller supplies the cache lookups.
func (ch *ConversationsHandler) collectUnreadChannels(params *unreadsParams, counts provider.UnreadCounts, dir *provider.Directory) ([]UnreadChannel, int) {
	var unreadChannels []UnreadChannel
	sections := []struct {
		snapshots []provider.ConversationUnread
		category  string
	}{
		{counts.Channels, provider.CategoryInternal},
		{counts.MPIMs, provider.CategoryGroupDM},
		{counts.IMs, provider.CategoryDM},
	}
	for _, section := range sections {
		for _, snap := range section.snapshots {
			if !snap.HasUnreads || params.mutedChannels[snap.ID] || (params.mentionsOnly && snap.MentionCount == 0) {
				continue
			}
			category := section.category
			if cached, err := dir.ChannelByRef(snap.ID); err == nil {
				category = cached.Category()
			}
			if params.channelTypes != allCategories && category != params.channelTypes {
				continue
			}
			unreadChannels = append(unreadChannels, UnreadChannel{
				ChannelID:   snap.ID,
				ChannelName: unreadChannelName(dir, snap.ID),
				ChannelType: category,
				UnreadCount: snap.MentionCount,
				LastRead:    snap.LastRead,
			})
		}
	}

	ch.sortChannelsByPriority(unreadChannels)

	// maxChannels > 0 backstop: non-positive would slice negative and panic.
	dropped := 0
	if params.maxChannels > 0 && len(unreadChannels) > params.maxChannels {
		dropped = len(unreadChannels) - params.maxChannels
		unreadChannels = unreadChannels[:params.maxChannels]
	}

	return unreadChannels, dropped
}

// unreadChannelName is the cached label for a conversation, or its ID.
func unreadChannelName(dir *provider.Directory, id string) string {
	if label := dir.ChannelLabel(id); label != "" {
		return label
	}
	return id
}

// ConversationsMarkHandler marks a channel as read up to a specific timestamp
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
		history, err := ch.apiProvider.WebAPI().GetConversationHistoryContext(ctx, &historyParams)
		if err != nil {
			ch.logger.Debug("Failed to get latest message", zap.Error(err))
			return nil, fmt.Errorf("failed to get latest message: %w", err)
		}
		if len(history.Messages) > 0 {
			ts = history.Messages[0].Timestamp
		} else {
			return NewStructuredResult(ActionData{Action: "mark_read", Status: "no_messages", ChannelID: channel}, SlackResultMeta("", false, ""), "No messages to mark as read"), nil
		}
	}

	err = ch.apiProvider.Slack().MarkConversationContext(ctx, channel, ts)
	if err != nil {
		ch.logger.Debug("Failed to mark conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to mark conversation as read: %w", err)
	}

	ch.logger.Info("Marked conversation as read",
		zap.String("channel", channel),
		zap.String("ts", ts))

	fallback := fmt.Sprintf("Marked %s as read up to %s", channel, ts)
	return NewStructuredResult(ActionData{Action: "mark_read", Status: "marked", ChannelID: channel, MessageID: ts}, SlackResultMeta("", false, ""), fallback), nil
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

	notInChannel, err := ch.apiProvider.Slack().LeaveConversationContext(ctx, channel)
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

	_, _, _, err = ch.apiProvider.WebAPI().JoinConversationContext(ctx, channel)
	if err != nil {
		ch.logger.Debug("Failed to join conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to join conversation: %w", err)
	}

	ch.logger.Info("Joined conversation", zap.String("channel", channel))
	return mcp.NewToolResultText(fmt.Sprintf("Successfully joined %s", channel)), nil
}

const (
	defaultUnreadsMaxChannels        = 50
	defaultUnreadsMessagesPerChannel = 10
)

// sortChannelsByPriority: type rank, then UnreadCount desc, ChannelID asc.
func (ch *ConversationsHandler) sortChannelsByPriority(channels []UnreadChannel) {
	rank := func(channelType string) int {
		if p := slices.Index(provider.Categories, channelType); p >= 0 {
			return p
		}
		return len(provider.Categories)
	}

	sort.SliceStable(channels, func(i, j int) bool {
		pi, pj := rank(channels[i].ChannelType), rank(channels[j].ChannelType)
		if pi != pj {
			return pi < pj
		}
		if channels[i].UnreadCount != channels[j].UnreadCount {
			return channels[i].UnreadCount > channels[j].UnreadCount
		}
		return channels[i].ChannelID < channels[j].ChannelID
	})
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
		u, err := ch.apiProvider.UserByRef(user)
		if err != nil {
			return nil, cacheMiss(user, err, userNotFound)
		}
		userIDs = append(userIDs, u.ID)
	}

	if len(userIDs) == 0 {
		return nil, invalidArguments("no valid users provided")
	}

	return userIDs, nil
}

// ConversationsOpenHandler opens a new DM or MPIM
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

	channel, _, _, err := ch.apiProvider.WebAPI().OpenConversationContext(ctx, params)
	if err != nil {
		ch.logger.Debug("Slack OpenConversationContext failed", zap.Error(err))
		return nil, err
	}

	ch.apiProvider.UpsertChannel(channel)

	return mcp.NewToolResultText(fmt.Sprintf("Successfully opened conversation. Channel ID: %s", channel.Conversation.ID)), nil
}

func (ch *ConversationsHandler) parseParamsToolUnreads(request mcp.CallToolRequest) (*unreadsParams, error) {
	maxChannels := positiveInt(request, "max_channels", defaultUnreadsMaxChannels)
	maxMessagesPerChannel := positiveInt(request, "max_messages_per_channel", defaultUnreadsMessagesPerChannel)

	channelTypes, err := parseChoice("channel_types", request.GetString("channel_types", ""), categoryFilters, allCategories)
	if err != nil {
		return nil, err
	}

	return &unreadsParams{
		includeMessages:       request.GetBool("include_messages", true),
		channelTypes:          channelTypes,
		maxChannels:           maxChannels,
		maxMessagesPerChannel: maxMessagesPerChannel,
		mentionsOnly:          request.GetBool("mentions_only", false),
		includeMuted:          request.GetBool("include_muted", false),
	}, nil
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
