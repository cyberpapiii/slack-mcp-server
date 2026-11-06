package handler

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
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

type ChannelsHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
}

func NewChannelsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger) *ChannelsHandler {
	return &ChannelsHandler{apiProvider: apiProvider, logger: logger}
}

// The values channels_list and channels_me accept, and their defaults.
var (
	defaultChannelTypes = []string{provider.PubChanType, provider.PrivateChanType}
	queryTargets        = []string{"name", "topic", "purpose"}
	channelSorts        = []string{"popularity"}
)

// allCategories is the channel_types value of conversations_unreads and
// channels_starred that filters nothing.
const allCategories = "all"

// categoryFilters is what conversations_unreads and channels_starred accept
// as channel_types: "all" or one category.
var categoryFilters = append([]string{allCategories}, provider.Categories...)

func (ch *ChannelsHandler) ChannelsResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	ch.logger.Debug("ChannelsResource called")

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Warn("API provider not ready", zap.Error(err))
		return nil, err
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
			URI:      request.Params.URI,
			MIMEType: "text/csv",
			Text:     string(csvBytes),
		},
	}, nil
}

func (ch *ChannelsHandler) ChannelsHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	sortType, err := parseChoice("sort", request.GetString("sort", ""), channelSorts, channelSorts[0])
	if err != nil {
		return nil, err
	}
	// channel_types and query_targets are comma-separated strings, not arrays:
	// MCP Inspector v0.14.0 mishandles array introspection.
	channelTypes, err := parseChoices("channel_types", request.GetString("channel_types", ""), provider.AllChanTypes, defaultChannelTypes)
	if err != nil {
		return nil, err
	}
	targets, err := parseChoices("query_targets", strings.ToLower(request.GetString("query_targets", "")), queryTargets, queryTargets[:1])
	if err != nil {
		return nil, err
	}
	cursor := request.GetString("cursor", "")
	limit := pageLimit(request, 100, 999)
	query := request.GetString("query", "")

	allChannels := ch.apiProvider.ProvideChannelsMaps().Channels
	ch.logger.Debug("Total channels available", zap.Int("count", len(allChannels)))

	channels := filterChannelsByTypes(allChannels, channelTypes)
	ch.logger.Debug("Channels after filtering by type", zap.Int("count", len(channels)))

	if query != "" {
		channels = filterChannelsByQuery(channels, query, targets)
		ch.logger.Debug("Channels after keyword filter", zap.Int("count", len(channels)))
	}

	sortChannels(channels, sortType)
	chans, nextcur, err := paginateChannels(channels, cursor, limit)
	if err != nil {
		ch.logger.Debug("Failed to paginate channels", zap.Error(err))
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

	result, err := csvResult("", SlackResultMeta(nextcur, false, ""), &channelList)
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

	channelTypes, err := parseChoices("channel_types", request.GetString("channel_types", ""), provider.AllChanTypes, defaultChannelTypes)
	if err != nil {
		return nil, err
	}
	cursor := request.GetString("cursor", "")
	limit := pageLimit(request, 100, 999)

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
			ch.logger.Debug("Failed to fetch user conversations", zap.Error(err))
			return nil, fmt.Errorf("failed to fetch your channels: %w", err)
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

	return csvResult("", SlackResultMeta(slackNextCursor, false, ""), &channelList)
}

func filterChannelsByTypes(channels map[string]provider.Channel, types []string) []provider.Channel {
	result := make([]provider.Channel, 0, len(channels))
	for _, ch := range channels {
		if slices.Contains(types, ch.SlackType()) {
			result = append(result, ch)
		}
	}
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

	channelTypesFilter, err := parseChoice("channel_types", request.GetString("channel_types", ""), categoryFilters, allCategories)
	if err != nil {
		return nil, err
	}
	limit := pageLimit(request, 100, 1000)

	starredIDs, err := ch.apiProvider.Slack().GetStarredChannelIDs(ctx, limit)
	if err != nil {
		ch.logger.Debug("Failed to get starred channel IDs", zap.Error(err))
		return nil, fmt.Errorf("failed to get starred channels: %w", err)
	}

	ch.logger.Debug("Got starred channel IDs", zap.Int("count", len(starredIDs)))

	mutedChannels, err := ch.apiProvider.Slack().GetMutedChannels(ctx)
	if err != nil {
		ch.logger.Warn("Failed to fetch muted channels, proceeding without mute info", zap.Error(err))
		mutedChannels = map[string]bool{}
	}

	dir := ch.apiProvider.Directory()

	var result []StarredChannel
	for _, id := range starredIDs {
		sc := StarredChannel{
			ID:      id,
			IsMuted: mutedChannels[id],
		}

		if cached, err := dir.ChannelByRef(id); err == nil {
			sc.Name = cached.Name
			sc.MemberCount = cached.MemberCount
			sc.ChannelType = cached.Category()
		} else {
			sc.Name = id
			sc.ChannelType = provider.CategoryInternal
		}

		if channelTypesFilter != allCategories && sc.ChannelType != channelTypesFilter {
			continue
		}

		result = append(result, sc)
		if len(result) >= limit {
			break
		}
	}

	ch.logger.Debug("Returning starred channels", zap.Int("count", len(result)))

	rendered, err := csvResult("", SlackResultMeta("", false, ""), &result)
	if err != nil {
		ch.logger.Error("Failed to marshal starred channels to CSV", zap.Error(err))
		return nil, err
	}
	return rendered, nil
}

func filterChannelsByQuery(channels []provider.Channel, query string, targets []string) []provider.Channel {
	q := strings.ToLower(query)
	var result []provider.Channel
	for _, ch := range channels {
		if (slices.Contains(targets, "name") && strings.Contains(strings.ToLower(ch.Name), q)) ||
			(slices.Contains(targets, "topic") && strings.Contains(strings.ToLower(ch.Topic), q)) ||
			(slices.Contains(targets, "purpose") && strings.Contains(strings.ToLower(ch.Purpose), q)) {
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
		lastID, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
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
		nextCursor = encodeCursor(channels[endIndex-1].ID)
	}
	return paged, nextCursor, nil
}

var channelTypesDescription = fmt.Sprintf("Comma-separated channel types. Allowed values: %s. Default: '%s'.", quotedList(provider.AllChanTypes), strings.Join(defaultChannelTypes, ","))

// Tools lists the MCP tools this handler serves.
func (ch *ChannelsHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.ChannelsList, Run: ch.ChannelsHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("List channels in the workspace, filtered by channel_types. Returns CSV."),
			mcp.WithString("channel_types",
				mcp.Description(channelTypesDescription),
			),
			mcp.WithString("sort",
				mcp.Description(fmt.Sprintf("Sort order. Allowed value: '%s' sorts by number of members in each channel.", channelSorts[0])),
			),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(100),
				mcp.Description("Maximum number of items to return, an integer between 1 and 999. Default is 100."),
			),
			mcp.WithString("cursor",
				mcp.Description(descCursor),
			),
			mcp.WithString("query",
				mcp.Description("Optional keyword to filter channels. Case-insensitive substring match against the fields specified by query_targets. Example: 'marketing' returns channels like #marketing, #marketing-ops."),
			),
			mcp.WithString("query_targets",
				mcp.DefaultString(queryTargets[0]),
				mcp.Description(fmt.Sprintf("Comma-separated list of fields to match the query against. Allowed values: %s. Example: '%s' to search all fields. Default is '%s'.", quotedList(queryTargets), strings.Join(queryTargets, ","), queryTargets[0])),
			),
		}},
		{Name: capability.ChannelsMe, Run: ch.ChannelsMeHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("List only the channels you have joined, unlike channels_list which returns all workspace channels. Useful on large workspaces where channels_list returns thousands of results."),
			mcp.WithString("channel_types",
				mcp.Description(channelTypesDescription),
			),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(100),
				mcp.Description("Maximum number of items to return (1-999)."),
			),
			mcp.WithString("cursor",
				mcp.Description(descCursor),
			),
		}},
		{Name: capability.ChannelsStarred, Run: ch.ChannelsStarredHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("List channels and DMs the user has starred (bookmarked). Returns only that subset, not the full channel list."),
			mcp.WithString("channel_types",
				mcp.Description(fmt.Sprintf("Filter by channel type: '%s' (default), '%s' (direct messages), '%s' (group DMs), '%s' (ext-shared channels), '%s' (regular workspace channels).",
					allCategories, provider.CategoryDM, provider.CategoryGroupDM, provider.CategoryPartner, provider.CategoryInternal)),
				mcp.DefaultString(allCategories),
			),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(100),
				mcp.Description("Maximum number of starred channels to return (1-1000). Default is 100."),
			),
		}},
	}
}
