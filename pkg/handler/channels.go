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

type Channel struct {
	ID          string `csv:"ID" json:"id" jsonschema:"Channel ID"`
	Name        string `csv:"Name" json:"name" jsonschema:"Channel name"`
	Topic       string `csv:"Topic" json:"topic,omitempty" jsonschema:"Channel topic"`
	Purpose     string `csv:"Purpose" json:"purpose,omitempty" jsonschema:"Channel purpose"`
	MemberCount int    `csv:"MemberCount" json:"member_count" jsonschema:"Number of members"`
}

// marshalRowsToCSV renders one page of tabular rows as CSV text. rows is a
// pointer to a slice of csv-tagged structs; nextCursor becomes the
// "#next_cursor:" comment line when more pages exist.
func marshalRowsToCSV(legend string, rows any, nextCursor string) (*mcp.CallToolResult, error) {
	csvBytes, err := gocsv.MarshalBytes(rows)
	if err != nil {
		return nil, err
	}
	return NewCSVResult(legend, SlackResultMeta(nextCursor, false, ""), string(csvBytes)), nil
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
	logToolCall(ch.logger, "ChannelsHandler called", request)

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Error("API provider not ready", zap.Error(err))
		return nil, err
	}

	sortType := request.GetString("sort", "popularity")
	types := request.GetString("channel_types", "public_channel,private_channel")
	cursor := request.GetString("cursor", "")
	limit := pageLimit(request, 100, 999)
	query := request.GetString("query", "")
	queryTargets := request.GetString("query_targets", "name")

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

	sortChannels(channels, sortType)
	chans, nextcur, err := paginateChannels(channels, cursor, limit)
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

	result, err := marshalRowsToCSV("", &channelList, nextcur)
	if err != nil {
		ch.logger.Error("Failed to marshal channels to CSV", zap.Error(err))
		return nil, err
	}
	return result, nil
}

// slackMaxChannelsPageSize is users.conversations max page size.
const slackMaxChannelsPageSize = 200

// nextPageSize asks only for rows still needed, capped at Slack's page max so
// the returned cursor stays aligned with the last row we hand back.
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
	logToolCall(ch.logger, "ChannelsMeHandler called", request)

	types := request.GetString("channel_types", "public_channel,private_channel")
	cursor := request.GetString("cursor", "")
	limit := pageLimit(request, 100, 999)

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

	usersMap := ch.apiProvider.ProvideUsersMap().Users
	var allChannels []provider.Channel
	var slackNextCursor string
	apiCursor := cursor

	for {
		params := &slack.GetConversationsForUserParameters{
			Types:           channelTypes,
			Limit:           nextPageSize(limit, len(allChannels)),
			Cursor:          apiCursor,
			ExcludeArchived: true,
		}
		channels, nextCursor, err := ch.apiProvider.WebAPI().GetConversationsForUserContext(ctx, params)
		if err != nil {
			ch.logger.Error("Failed to fetch user conversations", zap.Error(err))
			return nil, fmt.Errorf("failed to fetch your channels: %v", err)
		}

		for _, c := range channels {
			allChannels = append(allChannels, provider.MapChannelFromSlack(c, usersMap))
		}

		if len(allChannels) >= limit {
			slackNextCursor = nextCursor
			break
		}

		// Empty page + non-empty cursor would spin forever.
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

	end := limit
	if end > len(allChannels) {
		end = len(allChannels)
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

	return marshalRowsToCSV("", &channelList, slackNextCursor)
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
	ID          string `csv:"ID"`
	Name        string `csv:"Name"`
	ChannelType string `csv:"ChannelType"` // "dm", "group_dm", "internal", "partner"
	IsMuted     bool   `csv:"IsMuted"`
	MemberCount int    `csv:"MemberCount"`
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
	limit := pageLimit(request, 100, 1000)

	starredIDs, err := ch.apiProvider.Slack().GetStarredChannelIDs(ctx, limit)
	if err != nil {
		ch.logger.Error("Failed to get starred channel IDs", zap.Error(err))
		return nil, fmt.Errorf("failed to get starred channels: %v", err)
	}

	ch.logger.Debug("Got starred channel IDs", zap.Int("count", len(starredIDs)))

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
			ID:      id,
			IsMuted: mutedChannels[id],
		}

		if cached, ok := channelsMaps.Channels[id]; ok {
			sc.Name = cached.Name
			sc.MemberCount = cached.MemberCount
			sc.ChannelType = classifyChannelType(cached)
		} else {
			sc.Name = id
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

	rendered, err := marshalRowsToCSV("", &result, "")
	if err != nil {
		ch.logger.Error("Failed to marshal starred channels to CSV", zap.Error(err))
		return nil, err
	}
	return rendered, nil
}

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
// sortChannels orders the full list before pagination so a cursor walks the
// same ordering the first page showed. Ties break on ID for stability.
func sortChannels(channels []provider.Channel, sortType string) {
	sort.Slice(channels, func(i, j int) bool {
		if sortType == "popularity" && channels[i].MemberCount != channels[j].MemberCount {
			return channels[i].MemberCount > channels[j].MemberCount
		}
		return channels[i].ID < channels[j].ID
	})
}

// paginateChannels slices an already-ordered list. The cursor is the base64
// ID of the last row served; the next page starts right after it.
func paginateChannels(channels []provider.Channel, cursor string, limit int) ([]provider.Channel, string, error) {
	startIndex := 0
	if cursor != "" {
		decoded, err := base64.StdEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor: %q", cursor)
		}
		lastID := string(decoded)
		startIndex = len(channels)
		for i, ch := range channels {
			if ch.ID == lastID {
				startIndex = i + 1
				break
			}
		}
	}

	endIndex := startIndex + limit
	if endIndex > len(channels) {
		endIndex = len(channels)
	}
	// Non-positive limit would panic the slice; callers clamp, but do not rely on that alone.
	if endIndex < startIndex {
		endIndex = startIndex
	}

	paged := channels[startIndex:endIndex]
	var nextCursor string
	if endIndex > 0 && endIndex < len(channels) {
		nextCursor = base64.StdEncoding.EncodeToString([]byte(channels[endIndex-1].ID))
	}
	return paged, nextCursor, nil
}
