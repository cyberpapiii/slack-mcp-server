package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/server/auth"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

func (ch *ConversationsHandler) UsersResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	ch.logger.Debug("UsersResource called")

	if authenticated, err := auth.IsAuthenticated(ctx, ch.apiProvider.ServerTransport(), ch.logger); !authenticated {
		ch.logger.Error("Authentication failed for users resource", zap.Error(err))
		return nil, err
	}

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Warn("API provider not ready", zap.Error(err))
		return nil, err
	}

	ws, err := workspaceName(ch.apiProvider, ch.logger)
	if err != nil {
		return nil, err
	}

	usersMaps := ch.apiProvider.ProvideUsersMap()
	users := usersMaps.Users
	usersList := make([]User, 0, len(users))
	for _, user := range users {
		usersList = append(usersList, User{
			UserID:   user.ID,
			UserName: user.Name,
			RealName: user.RealName,
		})
	}

	csvBytes, err := gocsv.MarshalBytes(&usersList)
	if err != nil {
		ch.logger.Error("Failed to marshal users to CSV", zap.Error(err))
		return nil, err
	}

	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "slack://" + ws + "/users",
			MIMEType: "text/csv",
			Text:     string(csvBytes),
		},
	}, nil
}

func (ch *ConversationsHandler) UsersSearchHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	if ready, err := ch.apiProvider.IsReady(); !ready {
		ch.logger.Debug("API provider not ready", zap.Error(err))
		return nil, err
	}

	params, err := ch.parseParamsToolUsersSearch(request)
	if err != nil {
		ch.logger.Debug("Failed to parse users-search params", zap.Error(err))
		return nil, err
	}

	ch.logger.Debug("Searching for users",
		zap.String("query", params.query),
		zap.Int("limit", params.limit),
	)

	users, err := ch.apiProvider.SearchUsers(ctx, params.query, params.limit)
	if err != nil {
		ch.logger.Debug("UsersSearch failed", zap.Error(err))
		return nil, fmt.Errorf("users search failed: %w", err)
	}

	dir := ch.apiProvider.Directory()
	results := make([]UserSearchResult, 0, len(users))
	for _, user := range users {
		if user.Deleted {
			continue
		}
		dm, _ := dir.DMChannelFor(user.ID)
		dmChannelID := dm.ID

		results = append(results, UserSearchResult{
			UserID:      user.ID,
			UserName:    user.Name,
			RealName:    user.RealName,
			DisplayName: user.Profile.DisplayName,
			Email:       user.Profile.Email,
			Title:       user.Profile.Title,
			DMChannelID: dmChannelID,
		})
	}

	if len(results) == 0 {
		return mcp.NewToolResultText("No users found matching the query."), nil
	}

	csvBytes, err := gocsv.MarshalBytes(&results)
	if err != nil {
		ch.logger.Error("Failed to marshal users to CSV", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultText(string(csvBytes)), nil
}

func (ch *ConversationsHandler) parseParamsToolUsersSearch(request mcp.CallToolRequest) (*usersSearchParams, error) {
	query := strings.TrimSpace(request.GetString("query", ""))
	if query == "" {
		return nil, invalidArguments("query is required")
	}

	limit := pageLimit(request, 10, 100)

	return &usersSearchParams{
		query: query,
		limit: limit,
	}, nil
}
