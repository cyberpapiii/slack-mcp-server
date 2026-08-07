package handler

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/server/auth"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// Channel is the output type for channels tools, used for both structured output and CSV.
type Channel struct {
	ID          string `csv:"id" json:"id" jsonschema_description:"Channel ID"`
	Name        string `csv:"name" json:"name" jsonschema_description:"Channel name"`
	Topic       string `csv:"topic" json:"topic,omitempty" jsonschema_description:"Channel topic"`
	Purpose     string `csv:"purpose" json:"purpose,omitempty" jsonschema_description:"Channel purpose"`
	MemberCount int    `csv:"member_count" json:"member_count" jsonschema_description:"Number of members"`
	Cursor      string `csv:"cursor" json:"cursor,omitempty" jsonschema_description:"Pagination cursor"`
}

// ChannelList wraps a slice of Channel for structured output.
type ChannelList struct {
	Channels []Channel `json:"channels" jsonschema_description:"List of channels"`
}

type ChannelsHandler struct {
	apiProvider *provider.ApiProvider
	validTypes  map[string]bool
	logger      *zap.Logger
}

func NewChannelsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger) *ChannelsHandler {
	validTypes := make(map[string]bool, len(provider.AllChanTypes))
	for _, v := range provider.AllChanTypes {
		validTypes[v] = true
	}

	return &ChannelsHandler{
		apiProvider: apiProvider,
		validTypes:  validTypes,
		logger:      logger,
	}
}

func (ch *ChannelsHandler) ChannelsResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	logResourceCall(ch.logger, "ChannelsResource called", request)

	// mark3labs/mcp-go does not support middlewares for resources.
	if authenticated, err := auth.IsAuthenticated(ctx, ch.apiProvider.ServerTransport(), ch.logger); !authenticated {
		ch.logger.Error("Authentication failed for channels resource", zap.Error(err))
		return nil, err
	}

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	ar, err := ch.apiProvider.Slack().AuthTest()
	if err != nil {
		ch.logger.Error("Auth test failed", zap.Error(err))
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

	channels := ch.apiProvider.ProvideChannelsMaps().Channels
	ch.logger.Debug("Retrieved channels from provider", zap.Int("count", len(channels)))

	channelList := make([]Channel, 0, len(channels))
	for _, channel := range channels {
		channelList = append(channelList, Channel{
			ID:          channel.ID,
			Name:        channel.Name,
			Topic:       channel.Topic,
			Purpose:     channel.Purpose,
			MemberCount: channel.MemberCount,
		})
	}

	csvBytes, err := gocsv.MarshalBytes(&channelList)
	if err != nil {
		ch.logger.Error("Failed to marshal channels to CSV", zap.Error(err))
		return nil, err
	}

	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "slack://" + ws + "/channels",
			MIMEType: "text/csv",
			Text:     string(csvBytes),
		},
	}, nil
}

func (ch *ChannelsHandler) ChannelsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ch.logger.Debug("ChannelsHandler called")

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	sortType := request.GetString("sort", "popularity")
	types := request.GetString("channel_types", provider.PubChanType)
	cursor := request.GetString("cursor", "")
	limit := request.GetInt("limit", 0)
	query := request.GetString("query", "")
	queryTargets := request.GetString("query_targets", "name")

	ch.logger.Debug("Request parameters",
		zap.String("sort", sortType),
		zap.String("channel_types", types),
		zap.String("cursor", cursor),
		zap.Int("limit", limit),
		zap.String("query", query),
		zap.String("query_targets", queryTargets),
	)

	// MCP Inspector v0.14.0 has issues with Slice type
	// introspection, so some type simplification makes sense here
	channelTypes := []string{}
	for _, t := range strings.Split(types, ",") {
		t = strings.TrimSpace(t)
		if ch.validTypes[t] {
			channelTypes = append(channelTypes, t)
		} else if t != "" {
			ch.logger.Warn("Invalid channel type ignored", zap.String("type", t))
		}
	}

	if len(channelTypes) == 0 {
		ch.logger.Debug("No valid channel types provided, using defaults")
		channelTypes = append(channelTypes, provider.PubChanType)
		channelTypes = append(channelTypes, provider.PrivateChanType)
	}

	ch.logger.Debug("Validated channel types", zap.Strings("types", channelTypes))

	// GetInt only substitutes its default when the key is absent, so a caller
	// passing limit: -5 gets -5 here. Treat non-positive exactly like absent.
	if limit <= 0 {
		limit = 100
		ch.logger.Debug("Limit not provided, using default", zap.Int("limit", limit))
	}
	if limit > 999 {
		ch.logger.Warn("Limit exceeds maximum, capping to 999", zap.Int("requested", limit))
		limit = 999
	}

	allChannels := ch.apiProvider.ProvideChannelsMaps().Channels
	ch.logger.Debug("Total channels available", zap.Int("count", len(allChannels)))

	channels := filterChannelsByTypes(allChannels, channelTypes)
	ch.logger.Debug("Channels after filtering by type", zap.Int("count", len(channels)))

	if query != "" {
		validTargets := map[string]bool{"name": true, "topic": true, "purpose": true}
		targetSet := make(map[string]bool)
		for _, t := range strings.Split(queryTargets, ",") {
			t = strings.TrimSpace(strings.ToLower(t))
			if validTargets[t] {
				targetSet[t] = true
			} else if t != "" {
				ch.logger.Warn("Invalid query target ignored", zap.String("target", t))
			}
		}
		if len(targetSet) == 0 {
			ch.logger.Debug("No valid query targets provided, using default (name)")
			targetSet["name"] = true
		}

		channels = filterChannelsByQuery(channels, query, targetSet)
		ch.logger.Debug("Channels after keyword filter", zap.Int("count", len(channels)))
	}

	chans, nextcur, err := paginateChannels(
		channels,
		cursor,
		limit,
	)
	if err != nil {
		ch.logger.Error("Failed to paginate channels", zap.Error(err))
		return nil, err
	}

	ch.logger.Debug("Pagination results",
		zap.Int("returned_count", len(chans)),
		zap.Bool("has_next_page", nextcur != ""),
	)

	channelList := make([]Channel, len(chans))
	for i, channel := range chans {
		channelList[i] = Channel{
			ID:          channel.ID,
			Name:        channel.Name,
			Topic:       channel.Topic,
			Purpose:     channel.Purpose,
			MemberCount: channel.MemberCount,
		}
	}

	switch sortType {
	case "popularity":
		ch.logger.Debug("Sorting channels by popularity (member count)")
		sort.Slice(channelList, func(i, j int) bool {
			return channelList[i].MemberCount > channelList[j].MemberCount
		})
	default:
		ch.logger.Debug("No sorting applied", zap.String("sort_type", sortType))
	}

	if len(channelList) > 0 && nextcur != "" {
		channelList[len(channelList)-1].Cursor = nextcur
		ch.logger.Debug("Added cursor to last channel", zap.String("cursor", nextcur))
	}

	csvBytes, err := gocsv.MarshalBytes(&channelList)
	if err != nil {
		ch.logger.Error("Failed to marshal channels to CSV", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultStructured(
		ChannelList{Channels: channelList},
		string(csvBytes),
	), nil
}

// slackMaxChannelsPageSize is the largest page users.conversations will serve
// in a single call.
const slackMaxChannelsPageSize = 200

// nextPageSize returns how many channels to ask Slack for on the next call:
// exactly the number still missing to reach limit, capped at Slack's maximum
// page size. Never over-requesting is what keeps the cursor Slack returns
// aligned with the last row we actually hand back. Asking for more would
// leave the surplus rows unreachable by any later page.
func nextPageSize(limit, have int) int {
	remaining := limit - have
	if remaining > slackMaxChannelsPageSize {
		return slackMaxChannelsPageSize
	}
	if remaining < 1 {
		return 1
	}
	return remaining
}

func (ch *ChannelsHandler) ChannelsMeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ch.logger.Debug("ChannelsMeHandler called")

	types := request.GetString("channel_types", "public_channel,private_channel")
	cursor := request.GetString("cursor", "")
	limit := request.GetInt("limit", 0)

	// Non-positive is treated exactly like absent: GetInt's default only applies
	// to a missing key, so a negative limit would otherwise reach the slice below.
	if limit <= 0 {
		limit = 100
	}
	if limit > 999 {
		limit = 999
	}

	channelTypes := []string{}
	for _, t := range strings.Split(types, ",") {
		t = strings.TrimSpace(t)
		if ch.validTypes[t] {
			channelTypes = append(channelTypes, t)
		}
	}
	if len(channelTypes) == 0 {
		channelTypes = []string{provider.PubChanType, provider.PrivateChanType}
	}

	// Fetch channels via the Slack API, stopping as soon as we have enough
	// results and using the API's native cursor for pagination. This avoids
	// fetching every channel the user belongs to on large workspaces.
	usersMap := ch.apiProvider.ProvideUsersMap().Users
	var allChannels []provider.Channel
	var apiCursor string
	var slackNextCursor string

	if cursor != "" {
		apiCursor = cursor
	}

	for {
		params := &slack.GetConversationsForUserParameters{
			Types:           channelTypes,
			Limit:           nextPageSize(limit, len(allChannels)),
			Cursor:          apiCursor,
			ExcludeArchived: true,
		}
		channels, nextCursor, err := ch.apiProvider.Slack().GetConversationsForUserContext(ctx, params)
		if err != nil {
			ch.logger.Error("Failed to fetch user conversations", zap.Error(err))
			return nil, fmt.Errorf("failed to fetch your channels: %v", err)
		}

		for _, c := range channels {
			allChannels = append(allChannels, provider.MapChannelFromSlack(c, usersMap))
		}

		// Early exit: stop paginating through the Slack API once we have
		// enough. Because every request asked for exactly the rows still
		// missing, the API cannot have returned more than we hand back, so
		// nextCursor is positioned right after the last row we return.
		if len(allChannels) >= limit {
			slackNextCursor = nextCursor
			break
		}

		// Zero-progress guard: an empty page paired with a non-empty cursor
		// would otherwise spin forever.
		if len(channels) == 0 {
			slackNextCursor = nextCursor
			break
		}

		if nextCursor == "" {
			break
		}
		apiCursor = nextCursor
	}

	ch.logger.Debug("Fetched member channels", zap.Int("count", len(allChannels)))

	// Truncate to limit and use the Slack API cursor. With per-request sizing
	// above this slice is a no-op safeguard against a server that over-serves.
	end := limit
	if end > len(allChannels) {
		end = len(allChannels)
	}
	// Backstop against a non-positive limit reaching the slice below.
	if end < 0 {
		end = 0
	}
	var channelList []Channel
	for _, channel := range allChannels[:end] {
		channelList = append(channelList, Channel{
			ID:          channel.ID,
			Name:        channel.Name,
			Topic:       channel.Topic,
			Purpose:     channel.Purpose,
			MemberCount: channel.MemberCount,
		})
	}

	if len(channelList) > 0 && slackNextCursor != "" {
		channelList[len(channelList)-1].Cursor = slackNextCursor
	}

	csvBytes, err := gocsv.MarshalBytes(&channelList)
	if err != nil {
		return nil, err
	}

	return mcp.NewToolResultText(string(csvBytes)), nil
}

func filterChannelsByTypes(channels map[string]provider.Channel, types []string) []provider.Channel {
	logger := zap.L()

	result := make([]provider.Channel, 0, len(channels))
	typeSet := make(map[string]bool)

	for _, t := range types {
		typeSet[t] = true
	}

	publicCount := 0
	privateCount := 0
	imCount := 0
	mpimCount := 0

	for _, ch := range channels {
		switch {
		case ch.IsIM && typeSet[provider.IMChanType]:
			result = append(result, ch)
			imCount++
		case ch.IsMpIM && typeSet[provider.MpIMChanType]:
			result = append(result, ch)
			mpimCount++
		case ch.IsPrivate && !ch.IsIM && !ch.IsMpIM && typeSet[provider.PrivateChanType]:
			result = append(result, ch)
			privateCount++
		case !ch.IsPrivate && !ch.IsIM && !ch.IsMpIM && typeSet[provider.PubChanType]:
			result = append(result, ch)
			publicCount++
		}
	}

	logger.Debug("Channel filtering complete",
		zap.Int("total_input", len(channels)),
		zap.Int("total_output", len(result)),
		zap.Int("public_channels", publicCount),
		zap.Int("private_channels", privateCount),
		zap.Int("ims", imCount),
		zap.Int("mpims", mpimCount),
	)

	return result
}

type StarredChannel struct {
	ChannelID   string `csv:"channel_id"`
	ChannelName string `csv:"channel_name"`
	ChannelType string `csv:"channel_type"` // "dm", "group_dm", "internal", "partner"
	IsMuted     bool   `csv:"is_muted"`
	MemberCount int    `csv:"member_count"`
}

func (ch *ChannelsHandler) ChannelsStarredHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(ch.logger, "ChannelsStarredHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	if ch.apiProvider.IsBotToken() {
		return nil, fmt.Errorf(
			"channels_starred requires a user token (xoxp) or browser session tokens (xoxc/xoxd); " +
				"bot tokens (xoxb) do not support starred items",
		)
	}

	channelTypesFilter := request.GetString("channel_types", "all")
	limit := request.GetInt("limit", 100)
	if limit <= 0 {
		limit = 100
	}

	ch.logger.Debug("Request parameters",
		zap.String("channel_types", channelTypesFilter),
		zap.Int("limit", limit),
	)

	starredIDs, err := ch.apiProvider.Slack().GetStarredChannelIDs(ctx, limit)
	if err != nil {
		ch.logger.Error("Failed to get starred channel IDs", zap.Error(err))
		return nil, fmt.Errorf("failed to get starred channels: %v", err)
	}

	ch.logger.Debug("Got starred channel IDs", zap.Int("count", len(starredIDs)))

	// Fetch muted channels for the is_muted column
	mutedChannels := make(map[string]bool)
	if !ch.apiProvider.IsOAuth() {
		mc, err := ch.apiProvider.Slack().GetMutedChannels(ctx)
		if err != nil {
			ch.logger.Warn("Failed to fetch muted channels, proceeding without mute info", zap.Error(err))
		} else {
			mutedChannels = mc
		}
	}

	channelsMaps := ch.apiProvider.ProvideChannelsMaps()

	var result []StarredChannel
	for _, id := range starredIDs {
		sc := StarredChannel{
			ChannelID: id,
			IsMuted:   mutedChannels[id],
		}

		if cached, ok := channelsMaps.Channels[id]; ok {
			sc.ChannelName = cached.Name
			sc.MemberCount = cached.MemberCount
			sc.ChannelType = classifyChannelType(cached)
		} else {
			// Channel not in cache; use the ID as the name, type unknown.
			sc.ChannelName = id
			sc.ChannelType = "internal"
		}

		if channelTypesFilter != "all" && sc.ChannelType != channelTypesFilter {
			continue
		}

		result = append(result, sc)
		if len(result) >= limit {
			break
		}
	}

	ch.logger.Debug("Returning starred channels", zap.Int("count", len(result)))

	csvBytes, err := gocsv.MarshalBytes(&result)
	if err != nil {
		ch.logger.Error("Failed to marshal starred channels to CSV", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(string(csvBytes)), nil
}

// classifyChannelType returns "dm", "group_dm", "partner", or "internal" for a cached channel.
func classifyChannelType(ch provider.Channel) string {
	if ch.IsIM {
		return "dm"
	}
	if ch.IsMpIM {
		return "group_dm"
	}
	if ch.IsExtShared {
		return "partner"
	}
	return "internal"
}

func filterChannelsByQuery(channels []provider.Channel, query string, targetSet map[string]bool) []provider.Channel {
	q := strings.ToLower(query)
	var result []provider.Channel
	for _, ch := range channels {
		if (targetSet["name"] && strings.Contains(strings.ToLower(ch.Name), q)) ||
			(targetSet["topic"] && strings.Contains(strings.ToLower(ch.Topic), q)) ||
			(targetSet["purpose"] && strings.Contains(strings.ToLower(ch.Purpose), q)) {
			result = append(result, ch)
		}
	}
	return result
}

// paginateChannels returns one page of channels plus the cursor for the next
// page. An undecodable cursor is an error, not a silent restart at page one:
// restarting would hand a paginating agent the first page forever.
func paginateChannels(channels []provider.Channel, cursor string, limit int) ([]provider.Channel, string, error) {
	logger := zap.L()

	// Always sort by ID for stable cursor-based pagination
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].ID < channels[j].ID
	})

	startIndex := 0
	if cursor != "" {
		decoded, err := base64.StdEncoding.DecodeString(cursor)
		if err != nil {
			logger.Error("Failed to decode cursor",
				zap.String("cursor", cursor),
				zap.Error(err),
			)
			return nil, "", fmt.Errorf("invalid cursor: %q", cursor)
		}
		lastID := string(decoded)
		// Binary search for the first channel with ID > lastID
		startIndex = sort.Search(len(channels), func(i int) bool {
			return channels[i].ID > lastID
		})
		logger.Debug("Decoded cursor",
			zap.String("cursor", cursor),
			zap.String("decoded_id", lastID),
			zap.Int("start_index", startIndex),
		)
	}

	endIndex := startIndex + limit
	if endIndex > len(channels) {
		endIndex = len(channels)
	}
	// Backstop: a non-positive limit would put endIndex behind startIndex and
	// panic the slice below. Every caller clamps, but a panic here would take
	// down the whole stdio server, so do not rely on that alone.
	if endIndex < startIndex {
		endIndex = startIndex
	}

	paged := channels[startIndex:endIndex]

	var nextCursor string
	if endIndex > 0 && endIndex < len(channels) {
		nextCursor = base64.StdEncoding.EncodeToString([]byte(channels[endIndex-1].ID))
		logger.Debug("Generated next cursor",
			zap.String("last_id", channels[endIndex-1].ID),
			zap.String("next_cursor", nextCursor),
		)
	}

	logger.Debug("Pagination complete",
		zap.Int("total_channels", len(channels)),
		zap.Int("start_index", startIndex),
		zap.Int("end_index", endIndex),
		zap.Int("page_size", len(paged)),
		zap.Bool("has_more", nextCursor != ""),
	)

	return paged, nextCursor, nil
}
