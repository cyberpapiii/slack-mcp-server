package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// users_search limit bounds.
const (
	defaultUsersSearchLimit = 10
	maxUsersSearchLimit     = 100
)

func (ch *ConversationsHandler) UsersResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	ch.logger.Debug("UsersResource called")

	users, err := ch.api.Directory().Users()
	if err != nil {
		ch.logger.Warn("Users cache not ready", zap.Error(err))
		return nil, err
	}
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
			URI:      request.Params.URI,
			MIMEType: "text/csv",
			Text:     string(csvBytes),
		},
	}, nil
}

func (ch *ConversationsHandler) UsersSearchHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	params, err := ch.parseParamsToolUsersSearch(request)
	if err != nil {
		ch.logger.Debug("Failed to parse users-search params", zap.Error(err))
		return nil, err
	}

	ch.logger.Debug("Searching for users",
		zap.String("query", params.query),
		zap.Int("limit", params.limit),
	)

	users, err := ch.api.SearchUsers(ctx, params.query, params.limit)
	if err != nil {
		ch.logger.Debug("UsersSearch failed", zap.Error(err))
		return nil, fmt.Errorf("users search failed: %w", err)
	}

	dir := ch.api.Directory()
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

	return csvResult("", slackMeta(), &results)
}

func (ch *ConversationsHandler) parseParamsToolUsersSearch(request mcp.CallToolRequest) (*usersSearchParams, error) {
	query := strings.TrimSpace(request.GetString("query", ""))
	if query == "" {
		return nil, invalidArguments("query is required")
	}

	limit := pageLimit(request, defaultUsersSearchLimit, maxUsersSearchLimit)

	return &usersSearchParams{
		query: query,
		limit: limit,
	}, nil
}
