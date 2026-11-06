package handler

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

const (
	defaultConversationsNumericLimit = 50
	maxFileSizeBytes                 = 5 << 20

	// Search `limit` bounds. Default and max are both 100, matching the tool
	// schema's DefaultNumber(100) and its "between 1 and 100" description.
	defaultSearchMessagesLimit = 100
	maxSearchMessagesLimit     = 100
)

// searchFilterKeys are Slack's search modifiers, in the order the rebuilt
// query lists them.
var searchFilterKeys = []string{"is", "in", "from", "with", "before", "after", "on", "during", "has"}

var (
	flexibleDateStandardFormats = [...]string{
		"2006-01-02", "2006/01/02", "01-02-2006", "01/02/2006", "02-01-2006", "02/01/2006",
		"Jan 2, 2006", "January 2, 2006", "2 Jan 2006", "2 January 2006",
	}
	flexibleDateMonthYear = regexp.MustCompile(`^(\d{4})\s+([A-Za-z]+)$|^([A-Za-z]+)\s+(\d{4})$`)
	flexibleDateDMY       = regexp.MustCompile(`^(\d{1,2})[-\s]+([A-Za-z]+)[-\s]+(\d{4})$`)
	flexibleDateMDY       = regexp.MustCompile(`^([A-Za-z]+)[-\s]+(\d{1,2})[-\s]+(\d{4})$`)
	flexibleDateYMD       = regexp.MustCompile(`^(\d{4})[-\s]+([A-Za-z]+)[-\s]+(\d{1,2})$`)
	flexibleDateDaysAgo   = regexp.MustCompile(`^(\d+)\s+days?\s+ago$`)

	flexibleDateMonths = map[string]time.Month{
		"january": time.January, "jan": time.January,
		"february": time.February, "feb": time.February,
		"march": time.March, "mar": time.March,
		"april": time.April, "apr": time.April,
		"may":  time.May,
		"june": time.June, "jun": time.June,
		"july": time.July, "jul": time.July,
		"august": time.August, "aug": time.August,
		"september": time.September, "sep": time.September, "sept": time.September,
		"october": time.October, "oct": time.October,
		"november": time.November, "nov": time.November,
		"december": time.December, "dec": time.December,
	}
)

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
	page    int
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
	settings    Settings
	messages    messagePipeline
	// sendEnabled says whether conversations_add_message is served, for the
	// draft preview's "would it send" line.
	sendEnabled bool
}

func NewConversationsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger, settings Settings, sendEnabled bool) *ConversationsHandler {
	return &ConversationsHandler{
		apiProvider: apiProvider,
		logger:      logger,
		settings:    settings,
		messages:    messagePipeline{api: apiProvider, logger: logger},
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

func formatThreadTs(threadTs string) string {
	if threadTs == "" {
		return "(top-level message)"
	}
	return threadTs
}

// conversationFilter maps a DM (D...) or group (G...) ID to Slack's `in:`
// search form: the peer user for a DM, otherwise the cached name.
func conversationFilter(dir *provider.Directory, raw string) (string, error) {
	c, err := dir.ChannelByRef(raw)
	if err != nil && !errors.Is(err, provider.ErrNotCached) {
		return "", cacheMiss(raw, err, nil)
	}
	if err == nil {
		if c.IsIM && c.User != "" {
			return fmt.Sprintf("<@%s>", c.User), nil
		}
		if c.Name != "" {
			return c.Name, nil
		}
	}
	return "", invalidArguments(fmt.Sprintf("conversation %q not found in cache; pass the '@username' form instead", raw))
}

// userFilter maps a user reference to Slack's `<@U123>` search form. A user
// ID the cache does not know is fetched once; a name must be cached.
func (ch *ConversationsHandler) userFilter(ctx context.Context, raw string) (string, error) {
	id, name := provider.ParseUserRef(raw)
	if id != "" {
		if _, ok := ch.apiProvider.Directory().User(id); ok {
			return fmt.Sprintf("<@%s>", id), nil
		}
		patched, err := ch.apiProvider.PatchUser(ctx, id)
		if err != nil {
			ch.logger.Debug("Targeted user fetch failed, user not found", zap.String("user_id", id), zap.Error(err))
			return "", userNotFound(id)
		}
		return fmt.Sprintf("<@%s>", patched.ID), nil
	}
	u, err := ch.apiProvider.UserByRef(name)
	if err != nil {
		return "", cacheMiss(name, err, userNotFound)
	}
	return fmt.Sprintf("<@%s>", u.ID), nil
}

// channelFilter maps a "#name", channel ID or channel mention to the cached
// "#name" Slack's `in:` filter takes.
func (ch *ConversationsHandler) channelFilter(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	id, name := provider.ParseChannelRef(raw)
	kind := provider.ClassifyID(id)
	if strings.HasPrefix(name, "#") || kind == provider.IDChannel || kind == provider.IDGroup {
		c, err := ch.apiProvider.ChannelByRef(raw)
		if err != nil {
			return "", cacheMiss(raw, err, channelNotFound)
		}
		return c.Name, nil
	}
	return "", invalidArguments(fmt.Sprintf("invalid channel %q; pass a channel ID (Cxxxxxxxxxx) or a #channel-name", raw))
}

func limitByNumeric(limit string, defaultLimit int) (int, error) {
	if limit == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(limit)
	if err != nil {
		return 0, invalidArguments(fmt.Sprintf("invalid limit %q: use a number of messages (e.g. 50) or a time range such as 6h, 1d, 1w, or 1m", limit))
	}
	return n, nil
}

func limitByExpression(limit string) (slackLimit int, oldest, latest string, err error) {
	if len(limit) < 2 {
		return 0, "", "", invalidArguments(fmt.Sprintf("invalid duration limit %q: too short", limit))
	}
	suffix := limit[len(limit)-1]
	numStr := limit[:len(limit)-1]
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return 0, "", "", invalidArguments(fmt.Sprintf("invalid duration limit %q: must be a positive integer followed by 'h', 'd', 'w', or 'm'", limit))
	}
	now := time.Now()
	loc := now.Location()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	var oldestTime time.Time
	switch suffix {
	case 'h':
		// Hours are a rolling window. The day-based ranges snap to local midnight.
		oldestTime = now.Add(-time.Duration(n) * time.Hour)
	case 'd':
		oldestTime = startOfToday.AddDate(0, 0, -n+1)
	case 'w':
		oldestTime = startOfToday.AddDate(0, 0, -n*7+1)
	case 'm':
		oldestTime = startOfToday.AddDate(0, -n, 0)
	default:
		return 0, "", "", invalidArguments(fmt.Sprintf("invalid duration limit %q: must end in 'h', 'd', 'w', or 'm'", limit))
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
	if m := flexibleDateDaysAgo.FindStringSubmatch(lower); m != nil {
		days, _ := strconv.Atoi(m[1])
		t := now.AddDate(0, 0, -days)
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return t, t.Format("2006-01-02"), nil
	}

	hasLetters := strings.ContainsAny(dateStr, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if !hasLetters {
		if t, normalized, ok := parseStandardDate(dateStr); ok {
			return t, normalized, nil
		}
	}

	if m := flexibleDateMonthYear.FindStringSubmatch(dateStr); m != nil {
		var year int
		var monStr string
		if m[1] != "" && m[2] != "" {
			year, _ = strconv.Atoi(m[1])
			monStr = strings.ToLower(m[2])
		} else {
			year, _ = strconv.Atoi(m[4])
			monStr = strings.ToLower(m[3])
		}
		if mon, ok := flexibleDateMonths[monStr]; ok {
			t := time.Date(year, mon, 1, 0, 0, 0, 0, time.UTC)
			return t, t.Format("2006-01-02"), nil
		}
	}

	if m := flexibleDateDMY.FindStringSubmatch(dateStr); m != nil {
		day, _ := strconv.Atoi(m[1])
		year, _ := strconv.Atoi(m[3])
		monStr := strings.ToLower(m[2])
		if mon, ok := flexibleDateMonths[monStr]; ok {
			t := time.Date(year, mon, day, 0, 0, 0, 0, time.UTC)
			if t.Day() == day {
				return t, t.Format("2006-01-02"), nil
			}
		}
	}
	if m := flexibleDateMDY.FindStringSubmatch(dateStr); m != nil {
		monStr := strings.ToLower(m[1])
		day, _ := strconv.Atoi(m[2])
		year, _ := strconv.Atoi(m[3])
		if mon, ok := flexibleDateMonths[monStr]; ok {
			t := time.Date(year, mon, day, 0, 0, 0, 0, time.UTC)
			if t.Day() == day {
				return t, t.Format("2006-01-02"), nil
			}
		}
	}
	if m := flexibleDateYMD.FindStringSubmatch(dateStr); m != nil {
		year, _ := strconv.Atoi(m[1])
		monStr := strings.ToLower(m[2])
		day, _ := strconv.Atoi(m[3])
		if mon, ok := flexibleDateMonths[monStr]; ok {
			t := time.Date(year, mon, day, 0, 0, 0, 0, time.UTC)
			if t.Day() == day {
				return t, t.Format("2006-01-02"), nil
			}
		}
	}

	if hasLetters {
		if t, normalized, ok := parseStandardDate(dateStr); ok {
			return t, normalized, nil
		}
	}

	return time.Time{}, "", invalidArguments(fmt.Sprintf("unable to parse date: %s", dateStr))
}

func parseStandardDate(dateStr string) (time.Time, string, bool) {
	for _, format := range flexibleDateStandardFormats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t, t.Format("2006-01-02"), true
		}
	}
	return time.Time{}, "", false
}

func buildDateFilters(before, after, on, during string) (map[string]string, error) {
	out := make(map[string]string)
	if on != "" {
		if during != "" || before != "" || after != "" {
			return nil, invalidArguments("'on' cannot be combined with other date filters")
		}
		_, normalized, err := parseFlexibleDate(on)
		if err != nil {
			return nil, fmt.Errorf("invalid 'on' date: %w", err)
		}
		out["on"] = normalized
		return out, nil
	}
	if during != "" {
		if before != "" || after != "" {
			return nil, invalidArguments("'during' cannot be combined with 'before' or 'after'")
		}
		_, normalized, err := parseFlexibleDate(during)
		if err != nil {
			return nil, fmt.Errorf("invalid 'during' date: %w", err)
		}
		out["during"] = normalized
		return out, nil
	}
	if after != "" {
		_, normalized, err := parseFlexibleDate(after)
		if err != nil {
			return nil, fmt.Errorf("invalid 'after' date: %w", err)
		}
		out["after"] = normalized
	}
	if before != "" {
		_, normalized, err := parseFlexibleDate(before)
		if err != nil {
			return nil, fmt.Errorf("invalid 'before' date: %w", err)
		}
		out["before"] = normalized
	}
	if after != "" && before != "" {
		a, _, _ := parseFlexibleDate(after)
		b, _, _ := parseFlexibleDate(before)
		if a.After(b) {
			return nil, invalidArguments("'after' date is after 'before' date")
		}
	}
	return out, nil
}

func isFilterKey(key string) bool {
	return slices.Contains(searchFilterKeys, strings.ToLower(key))
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
	for _, key := range searchFilterKeys {
		for _, val := range filters[key] {
			out = append(out, fmt.Sprintf("%s:%s", key, val))
		}
	}
	return strings.Join(out, " ")
}

// cacheMiss is a failed cache lookup of ref as the caller reports it:
// notFound once the cache is warm, the retryable warming error before.
func cacheMiss(ref string, err error, notFound func(string) error) error {
	if errors.Is(err, provider.ErrNotCached) {
		return notFound(ref)
	}
	return fmt.Errorf("resolve %q: %w", ref, err)
}
