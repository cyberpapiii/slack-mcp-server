package handler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge/fasttime"
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
	logToolCall(ch.logger, "ConversationsUnreadsHandler called", request)

	params, err := ch.parseParamsToolUnreads(request)
	if err != nil {
		ch.logger.Error("Failed to parse unreads params", zap.Error(err))
		return nil, err
	}

	mode, err := text.ResolveOutputMode(request.GetString("detail", ""))
	if err != nil {
		return nil, err
	}
	if !ch.apiProvider.BrowserFeaturesAvailable() {
		return nil, browserSessionRequired("conversations_unreads", ch.apiProvider.BrowserDegradedReason())
	}

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

	counts, err := ch.apiProvider.Slack().ClientCounts(ctx)
	if err != nil {
		if errors.Is(err, provider.ErrBrowserSessionUnavailable) {
			return nil, browserSessionRequired("conversations_unreads", ch.apiProvider.BrowserDegradedReason())
		}
		ch.logger.Error("ClientCounts failed", zap.Error(err))
		return nil, fmt.Errorf("get Slack unread state: %w", err)
	}

	return ch.processClientCountsResponse(ctx, request, params, counts, mode)
}

func (ch *ConversationsHandler) processClientCountsResponse(ctx context.Context, request mcp.CallToolRequest, params *unreadsParams, counts edge.ClientCountsResponse, mode text.OutputMode) (*mcp.CallToolResult, error) {
	ch.logger.Debug("Got counts data",
		zap.Int("channels", len(counts.Channels)),
		zap.Int("mpims", len(counts.MPIMs)),
		zap.Int("ims", len(counts.IMs)))

	usersMap := ch.apiProvider.ProvideUsersMap()
	channelsMaps := ch.apiProvider.ProvideChannelsMaps()

	unreadChannels, dropped := ch.collectUnreadChannels(params, counts, usersMap, channelsMaps)

	ch.logger.Debug("Found unread channels", zap.Int("count", len(unreadChannels)))

	if !ch.backfillUnreadCounts(ctx, request, ch.apiProvider.WebAPI(), params, unreadChannels) {
		return cancelledToolResult(), nil
	}

	coverage := unreadsCoverage{mutedUnavailable: params.mutedUnavailable, maxChannels: params.maxChannels, dropped: dropped}

	if !params.includeMessages {
		return marshalUnreadChannelsToCSV(unreadChannels, coverage.meta())
	}

	var allMessages []Message

	for i := range unreadChannels {
		select {
		case <-ctx.Done():
			return cancelledToolResult(), nil
		default:
		}
		sendProgress(ctx, request, i+1, len(unreadChannels), fmt.Sprintf("Fetching messages: channel %d of %d", i+1, len(unreadChannels)))

		if unreadChannels[i].LastRead == "" {
			// No bound: unbounded history would mislead. Same conservative 1
			// as the summary/backfill path.
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, nil)
			coverage.unbounded = append(coverage.unbounded, unreadChannels[i].ChannelID)
			continue
		}

		historyParams := slack.GetConversationHistoryParameters{
			ChannelID: unreadChannels[i].ChannelID,
			Oldest:    unreadChannels[i].LastRead,
			Limit:     params.maxMessagesPerChannel,
			Inclusive: false,
		}

		history, err := ch.apiProvider.WebAPI().GetConversationHistoryContext(ctx, &historyParams)
		if err != nil {
			ch.logger.Warn("Failed to get history for channel",
				zap.String("channel", unreadChannels[i].ChannelID),
				zap.Error(err))
			// Failed fetch must not render 0 unread when HasUnreads was true.
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, err)
			coverage.failed = append(coverage.failed, unreadChannels[i].ChannelID)
			continue
		}

		// Zero-row window still reports ≥1 when HasUnreads was true.
		unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, len(history.Messages), nil)

		channelMessages := ch.convertMessagesFromHistory(ctx, history.Messages, unreadChannels[i].ChannelID, false, mode)
		allMessages = append(allMessages, channelMessages...)
	}

	ch.logger.Debug("Fetched unread messages", zap.Int("total", len(allMessages)))

	return marshalMessagesToCSV(allMessages, ch.render(mode, coverage.meta()))
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
	backfilled := 0
	for i := range unreadChannels {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		sendProgress(ctx, request, i+1, len(unreadChannels), fmt.Sprintf("Backfilling unread counts: channel %d of %d", i+1, len(unreadChannels)))

		if unreadChannels[i].UnreadCount > 0 {
			continue // already counted (mentions)
		}
		if unreadChannels[i].LastRead == "" {
			// No last-read bound (slackTS maps zero fasttime.Time to ""). Skip
			// unbounded history query; report 1 since HasUnreads was true.
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
			// Same conservative 1 as the include_messages path.
			unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, 0, err)
			continue
		}
		unreadChannels[i].UnreadCount = unreadCountFromHistory(unreadChannels[i].UnreadCount, len(history.Messages), nil)
		backfilled++
	}
	if backfilled > 0 {
		ch.logger.Debug("Backfilled unread counts via conversations.history",
			zap.Int("backfilled", backfilled))
	}
	return true
}

// slackTS renders an edge-API timestamp, mapping the zero value to "" rather
// than to fasttime's literal rendering of year 1 ("-62135596800.000000").
// A zero LastRead means "never read"; callers treat "" as "no bound available"
// and must not pass it to conversations.history as Oldest.
func slackTS(t fasttime.Time) string {
	if time.Time(t).IsZero() {
		return ""
	}
	return t.SlackString()
}

// collectUnreadChannels turns a client.counts snapshot into the sorted, limited
// list of unread channels plus the count cut by max_channels. Pure: no API
// calls. The caller supplies the cache snapshots.
func (ch *ConversationsHandler) collectUnreadChannels(params *unreadsParams, counts edge.ClientCountsResponse, usersMap *provider.UsersCache, channelsMaps *provider.ChannelsCache) ([]UnreadChannel, int) {
	var unreadChannels []UnreadChannel

	for _, snap := range counts.Channels {
		if !snap.HasUnreads {
			continue
		}

		if params.mutedChannels[snap.ID] {
			continue
		}

		if params.mentionsOnly && snap.MentionCount == 0 {
			continue
		}

		channelName := snap.ID
		channelType := "internal"
		if cached, ok := channelsMaps.Channels[snap.ID]; ok {
			name := cached.Name
			if strings.HasPrefix(name, "#") {
				channelName = name
			} else {
				channelName = "#" + name
			}
			if cached.IsExtShared {
				channelType = "partner"
			}
		}

		if params.channelTypes != "all" && channelType != params.channelTypes {
			continue
		}

		unreadChannels = append(unreadChannels, UnreadChannel{
			ChannelID:   snap.ID,
			ChannelName: channelName,
			ChannelType: channelType,
			UnreadCount: snap.MentionCount,
			LastRead:    slackTS(snap.LastRead),
		})
	}

	for _, snap := range counts.MPIMs {
		if !snap.HasUnreads {
			continue
		}

		if params.mutedChannels[snap.ID] {
			continue
		}

		if params.mentionsOnly && snap.MentionCount == 0 {
			continue
		}

		if params.channelTypes != "all" && params.channelTypes != "group_dm" {
			continue
		}

		channelName := snap.ID
		if cached, ok := channelsMaps.Channels[snap.ID]; ok {
			name := cached.Name
			if strings.HasPrefix(name, "#") {
				channelName = name
			} else {
				channelName = "#" + name
			}
		}

		unreadChannels = append(unreadChannels, UnreadChannel{
			ChannelID:   snap.ID,
			ChannelName: channelName,
			ChannelType: "group_dm",
			UnreadCount: snap.MentionCount,
			LastRead:    slackTS(snap.LastRead),
		})
	}

	for _, snap := range counts.IMs {
		if !snap.HasUnreads {
			continue
		}

		if params.mutedChannels[snap.ID] {
			continue
		}

		if params.mentionsOnly && snap.MentionCount == 0 {
			continue
		}

		if params.channelTypes != "all" && params.channelTypes != "dm" {
			continue
		}

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
			LastRead:    slackTS(snap.LastRead),
		})
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
		// No explicit ts: mark the channel read up to its newest message.
		historyParams := slack.GetConversationHistoryParameters{
			ChannelID: channel,
			Limit:     1,
		}
		history, err := ch.apiProvider.WebAPI().GetConversationHistoryContext(ctx, &historyParams)
		if err != nil {
			ch.logger.Error("Failed to get latest message", zap.Error(err))
			return nil, fmt.Errorf("failed to get latest message: %v", err)
		}
		if len(history.Messages) > 0 {
			ts = history.Messages[0].Timestamp
		} else {
			return NewStructuredResult(ActionData{Action: "mark_read", Status: "no_messages", ChannelID: channel}, SlackResultMeta("", false, ""), "No messages to mark as read"), nil
		}
	}

	err = ch.apiProvider.Slack().MarkConversationContext(ctx, channel, ts)
	if err != nil {
		ch.logger.Error("Failed to mark conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to mark conversation as read: %v", err)
	}

	ch.logger.Info("Marked conversation as read",
		zap.String("channel", channel),
		zap.String("ts", ts))

	fallback := fmt.Sprintf("Marked %s as read up to %s", channel, ts)
	return NewStructuredResult(ActionData{Action: "mark_read", Status: "marked", ChannelID: channel, MessageID: ts}, SlackResultMeta("", false, ""), fallback), nil
}

func (ch *ConversationsHandler) ConversationsLeaveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ConversationsLeaveHandler called", request)

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

	channel := request.GetString("channel_id", "")
	if channel == "" {
		return nil, fmt.Errorf("channel_id is required")
	}

	channel, err := ch.resolveChannelID(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve channel: %w", err)
	}

	_, _, _, err = ch.apiProvider.WebAPI().JoinConversationContext(ctx, channel)
	if err != nil {
		ch.logger.Error("Failed to join conversation", zap.Error(err))
		return nil, fmt.Errorf("failed to join conversation: %v", err)
	}

	ch.logger.Info("Joined conversation", zap.String("channel", channel))
	return mcp.NewToolResultText(fmt.Sprintf("Successfully joined %s", channel)), nil
}

// Keep in sync with validUnreadsChannelTypes and tool param docs in pkg/server/server.go.
var channelTypePriority = map[string]int{
	"dm":       0,
	"group_dm": 1,
	"partner":  2,
	"internal": 3,
}

// Must exceed all channelTypePriority values.
const unknownChannelTypePriority = 99

// sortChannelsByPriority: type rank, then UnreadCount desc, ChannelID asc.
func (ch *ConversationsHandler) sortChannelsByPriority(channels []UnreadChannel) {
	rank := func(channelType string) int {
		if p, ok := channelTypePriority[channelType]; ok {
			return p
		}
		return unknownChannelTypePriority
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

// marshalUnreadChannelsToCSV renders the include_messages=false summary. Rows
// carry their own Name column, so no "#channels:" legend is needed.
func marshalUnreadChannelsToCSV(channels []UnreadChannel, meta ResultMeta) (*mcp.CallToolResult, error) {
	csvBytes, err := gocsv.MarshalBytes(&channels)
	if err != nil {
		return nil, err
	}
	return NewCSVResult("", meta, string(csvBytes)), nil
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

	channel, _, _, err := ch.apiProvider.WebAPI().OpenConversationContext(ctx, params)
	if err != nil {
		ch.logger.Error("Slack OpenConversationContext failed", zap.Error(err))
		return nil, err
	}

	ch.apiProvider.UpsertChannel(channel)

	return mcp.NewToolResultText(fmt.Sprintf("Successfully opened conversation. Channel ID: %s", channel.Conversation.ID)), nil
}

func (ch *ConversationsHandler) parseParamsToolUnreads(request mcp.CallToolRequest) (*unreadsParams, error) {
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

	channelTypes := strings.TrimSpace(request.GetString("channel_types", "all"))
	if channelTypes == "" {
		channelTypes = "all"
	}
	if !slices.Contains(validUnreadsChannelTypes, channelTypes) {
		return nil, fmt.Errorf("unsupported channel_types %q: must be one of %s",
			channelTypes, strings.Join(validUnreadsChannelTypes, ", "))
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

func (ch *ConversationsHandler) parseParamsToolMark(request mcp.CallToolRequest) (*markParams, error) {

	channel := request.GetString("channel_id", "")
	if channel == "" {
		ch.logger.Error("channel_id missing in mark params")
		return nil, errors.New("channel_id is required")
	}

	if strings.HasPrefix(channel, "#") || strings.HasPrefix(channel, "@") {
		channelsMaps := ch.apiProvider.ProvideChannelsMaps()
		chn, ok := channelsMaps.ChannelsInv[channel]
		if !ok {
			ch.logger.Error("Channel not found", zap.String("channel", channel))
			return nil, fmt.Errorf("channel %q not found", channel)
		}
		channel = channelsMaps.Channels[chn].ID
	}

	ts := request.GetString("timestamp", "")

	return &markParams{
		channel: channel,
		ts:      ts,
	}, nil
}
