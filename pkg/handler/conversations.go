package handler

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/slack-go/slack"
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
	"has":    {},
}

type Message struct {
	MsgID         string `json:"message_id"`
	UserID        string `json:"user_id"`
	UserName      string `json:"user_name"`
	RealName      string `json:"real_name"`
	Channel       string `json:"channel"`
	ThreadTs      string `json:"thread_ts,omitempty"`
	Text          string `json:"text"`
	Time          string `json:"time"`
	Permalink     string `json:"permalink,omitempty"`
	Reactions     string `json:"reactions,omitempty"`
	BotName       string `json:"bot_name,omitempty"`
	FileCount     int    `json:"file_count,omitempty"`
	AttachmentIDs string `json:"attachment_ids,omitempty"`
	HasMedia      bool   `json:"has_media,omitempty"`
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
	sort  string
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
	mutedUnavailable      bool            // true when browser-session preferences could not be fetched
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
	// sendEnabled mirrors whether conversations_add_message is registered,
	// so conversations_draft_message can report send availability.
	sendEnabled bool
}

func NewConversationsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, sendEnabled bool) *ConversationsHandler {
	return &ConversationsHandler{
		apiProvider: apiProvider,
		logger:      logger,
		sendEnabled: sendEnabled,
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

func (ch *ConversationsHandler) resolveChannelID(ctx context.Context, channel string) (string, error) {
	if !strings.HasPrefix(channel, "#") && !strings.HasPrefix(channel, "@") {
		return channel, nil
	}

	channelsMaps := ch.apiProvider.ProvideChannelsMaps()
	chn, ok := channelsMaps.ChannelsInv[channel]
	if ok {
		return channelsMaps.Channels[chn].ID, nil
	}

	// Not in the cache; refresh it and retry the lookup once.
	ch.logger.Debug("Channel not found in cache, attempting refresh",
		zap.String("channel", channel))

	refreshErr := ch.apiProvider.ForceRefreshChannels(ctx)
	wasRateLimited := errors.Is(refreshErr, provider.ErrRefreshRateLimited)

	if refreshErr != nil && !wasRateLimited {
		ch.logger.Error("Failed to refresh channels cache",
			zap.String("channel", channel),
			zap.Error(refreshErr))
		return "", &ToolError{Code: "channel_not_found", Message: fmt.Sprintf("channel %q not found and cache refresh failed; pass the channel ID (Cxxxxxxxxxx) or look it up with channels_list", channel), Cause: refreshErr}
	}

	// If rate-limited, the cache wasn't refreshed, so a second lookup is pointless.
	if wasRateLimited {
		ch.logger.Warn("Channel not found; cache refresh was rate-limited",
			zap.String("channel", channel))
		return "", &ToolError{Code: "channel_not_found", Message: fmt.Sprintf("channel %q not found and the cache refresh was rate-limited; retry shortly or pass the channel ID (Cxxxxxxxxxx)", channel), Retryable: true}
	}

	channelsMaps = ch.apiProvider.ProvideChannelsMaps()
	chn, ok = channelsMaps.ChannelsInv[channel]
	if !ok {
		ch.logger.Error("Channel not found even after cache refresh",
			zap.String("channel", channel))
		return "", channelNotFound(channel)
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
			userName, realName, ok = msg.Username, msg.Username, true
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
				"Slack users sync is not ready yet: names may render as raw UIDs and @handle lookups will fail. Users sync runs as part of channels sync, and IM/MPIM channel operations need the users collection. Wait for the sync to finish and try again.",
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
			userName, realName, ok = msg.Username, msg.Username, true
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
			Channel:   msg.Channel.ID,
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

func formatThreadTs(threadTs string) string {
	if threadTs == "" {
		return "(top-level message)"
	}
	return threadTs
}

func isSlackUserIDPrefix(s string) bool {
	return strings.HasPrefix(s, "U") || strings.HasPrefix(s, "W")
}

// isSlackConversationIDPrefix reports whether s looks like a DM (D…) or
// group/MPIM (G…) conversation ID rather than a user ID or an @handle.
func isSlackConversationIDPrefix(s string) bool {
	return strings.HasPrefix(s, "D") || strings.HasPrefix(s, "G")
}

// formatConversationFilter maps a D…/G… ID to Slack `in:` form via channels cache.
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
				return "", userNotFound(raw)
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
		return "", userNotFound(raw)
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
		return "", channelNotFound(raw)
	}
	// Handle both C (standard channels) and G (private groups/channels) prefixes
	if strings.HasPrefix(raw, "C") || strings.HasPrefix(raw, "G") {
		if chn, ok := cms.Channels[raw]; ok {
			return chn.Name, nil
		}
		return "", channelNotFound(raw)
	}
	return "", &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("invalid channel %q; pass a channel ID (Cxxxxxxxxxx) or a #channel-name", raw)}
}

// renderOptions carries the per-call rendering context through
// marshalMessagesToCSV: output mode, the legend inputs, and the cursor for
// the next page.
type renderOptions struct {
	mode         text.OutputMode
	workspaceURL string
	channelName  func(channelID string) string
	meta         ResultMeta
	// trailer is appended after the message rows: a companion CSV table
	// (saved state, activity feed keys) under its own "#<name>:" line.
	trailer string
}

// render builds the per-call rendering context for message CSV output.
func (ch *ConversationsHandler) render(mode text.OutputMode, meta ResultMeta) renderOptions {
	return renderOptions{
		mode:         mode,
		workspaceURL: ch.apiProvider.WorkspaceURL(),
		channelName:  ch.channelDisplayName,
		meta:         meta,
	}
}

// channelDisplayName returns the cached "#name" or "@user" label for a
// conversation ID, or "" when the cache does not know it.
func (ch *ConversationsHandler) channelDisplayName(channelID string) string {
	if cached, ok := ch.apiProvider.ProvideChannelsMaps().Channels[channelID]; ok {
		return cached.Name
	}
	return ""
}

func marshalMessagesToCSV(messages []Message, opts renderOptions) (*mcp.CallToolResult, error) {
	if opts.mode != text.ModeFull {
		return marshalMessagesToCompactCSV(messages, opts)
	}
	csvBytes, err := gocsv.MarshalBytes(&messages)
	if err != nil {
		return nil, err
	}
	return NewCSVResult(buildChannelsLegend(messages, opts.channelName), opts.meta, string(csvBytes)+opts.trailer), nil
}

// marshalMessagesToCompactCSV converts messages to the default agent CSV format.
func marshalMessagesToCompactCSV(messages []Message, opts renderOptions) (*mcp.CallToolResult, error) {
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
		}
	}

	csvBytes, err := gocsv.MarshalBytes(&compact)
	if err != nil {
		return nil, err
	}

	legend := buildLegendHeader(messages, opts)
	return NewCSVResult(legend, opts.meta, string(csvBytes)+opts.trailer), nil
}

// buildChannelsLegend emits "#channels: C1=#general, D2=@bob" for every
// distinct conversation ID in the page that the cache can name. The Channel
// column itself always holds the bare ID.
func buildChannelsLegend(messages []Message, channelName func(string) string) string {
	ids := make([]string, len(messages))
	for i, m := range messages {
		ids[i] = m.Channel
	}
	return channelsLegend(ids, channelName)
}

// buildLegendHeader emits comment lines (agent-oriented, not CSV data) that
// normalize per-row redundancy: channel IDs with their names, distinct users
// with their IDs, and a permalink template so links are derivable without a
// Permalink column. The users and link lines are skipped for tiny result sets
// where they would outweigh the rows.
func buildLegendHeader(messages []Message, opts renderOptions) string {
	var sb strings.Builder
	sb.WriteString(buildChannelsLegend(messages, opts.channelName))
	if len(messages) < 3 {
		return sb.String()
	}

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

	if opts.workspaceURL != "" {
		base := opts.workspaceURL
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		sb.WriteString(fmt.Sprintf("#link_template: %sarchives/{Channel}/p{MsgID with \".\" removed}\n", base))
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
	for _, key := range []string{"is", "in", "from", "with", "before", "after", "on", "during", "has"} {
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

func channelNotFound(channel string) error {
	return &ToolError{Code: "channel_not_found", Message: fmt.Sprintf("channel %q not found; pass the channel ID (Cxxxxxxxxxx) or look the name up with channels_list", channel)}
}

func userNotFound(user string) error {
	return &ToolError{Code: "user_not_found", Message: fmt.Sprintf("user %q not found; pass the user ID (Uxxxxxxxxxx) or look the name up with users_search", user)}
}
