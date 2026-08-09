package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

const (
	profileWriteGate  = "SLACK_MCP_PROFILE_WRITE_TOOL"
	channelCreateGate = "SLACK_MCP_CHANNEL_CREATE_TOOL"
	channelInviteGate = "SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL"
	maxPeoplePageSize = 200
	maxEmojiPageSize  = 200
)

var (
	userIDPattern    = regexp.MustCompile(`^[UW][A-Z0-9]+$`)
	emojiNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_+\-]+$`)
)

type PeopleChannelsService interface {
	GetProfile(context.Context, string, bool) (provider.UserProfile, error)
	UpdateProfile(context.Context, string, provider.UserProfileUpdate) (provider.UserProfile, error)
	SetStatus(context.Context, string, string, string, int64) (provider.UserProfile, error)
	ListEmoji(context.Context, string, string, int) (provider.EmojiPage, error)
	CreateChannel(context.Context, string, bool, string) (provider.ChannelState, error)
	ListChannelMembers(context.Context, string, string, int) (provider.ChannelMembersPage, error)
	InviteChannelMembers(context.Context, string, []string) (provider.ChannelState, error)
}

type ProfileData struct {
	Profile provider.UserProfile `json:"profile"`
}

type EmojiPageData struct {
	Emoji []provider.Emoji `json:"emoji"`
}

type ChannelData struct {
	Channel provider.ChannelState `json:"channel"`
}

type ChannelMembersData struct {
	ChannelID string   `json:"channel_id"`
	UserIDs   []string `json:"user_ids"`
}

type ProfileResult = ToolResult[ProfileData]
type EmojiPageResult = ToolResult[EmojiPageData]
type ChannelResult = ToolResult[ChannelData]
type ChannelMembersResult = ToolResult[ChannelMembersData]

type PeopleChannelsHandler struct {
	service  PeopleChannelsService
	identity func() provider.ProviderIdentity
	logger   *zap.Logger
}

func NewPeopleChannelsHandler(service PeopleChannelsService, identity func() provider.ProviderIdentity, logger *zap.Logger) *PeopleChannelsHandler {
	return &PeopleChannelsHandler{service: service, identity: identity, logger: logger}
}

func (h *PeopleChannelsHandler) GetUserProfile(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsersGetProfileHandler called", request)
	userID := strings.TrimSpace(request.GetString("user_id", ""))
	if userID == "" {
		userID = h.identity().UserID
	}
	if err := validateUserID(userID); err != nil {
		return NewTypedErrorResult(err), nil
	}
	profile, err := h.service.GetProfile(ctx, userID, request.GetBool("include_labels", true))
	if err != nil {
		return NewTypedErrorResult(peopleChannelsError(err, false)), nil
	}
	data := ProfileData{Profile: profile}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) SetUserProfile(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsersSetProfileHandler called", request)
	if !requireToolEnabled(profileWriteGate, "users_set_profile") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "users_set_profile is disabled"}), nil
	}
	identity, err := h.requireUserIdentity()
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	update, err := parseProfileUpdate(request)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	profile, err := h.service.UpdateProfile(ctx, identity.UserID, update)
	if err != nil {
		return NewTypedErrorResult(peopleChannelsError(err, true)), nil
	}
	data := ProfileData{Profile: profile}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) SetUserStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsersSetStatusHandler called", request)
	if !requireToolEnabled(profileWriteGate, "users_set_status") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "users_set_status is disabled"}), nil
	}
	identity, err := h.requireUserIdentity()
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	text, err := presentString(request, "status_text")
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	if utf8.RuneCountInString(text) > 100 {
		return NewTypedErrorResult(invalidArguments("status_text must be at most 100 characters")), nil
	}
	emoji, err := presentString(request, "status_emoji")
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	emoji, err = normalizeEmoji(emoji)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	expiration := int64(request.GetInt("status_expiration", 0))
	if expiration < 0 {
		return NewTypedErrorResult(invalidArguments("status_expiration must be 0 or a Unix timestamp")), nil
	}
	profile, err := h.service.SetStatus(ctx, identity.UserID, text, emoji, expiration)
	if err != nil {
		return NewTypedErrorResult(peopleChannelsError(err, true)), nil
	}
	data := ProfileData{Profile: profile}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) ListEmoji(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "EmojiListHandler called", request)
	limit := request.GetInt("limit", 100)
	if limit < 1 || limit > maxEmojiPageSize {
		return NewTypedErrorResult(invalidArguments("limit must be between 1 and 200")), nil
	}
	page, err := h.service.ListEmoji(ctx, strings.TrimSpace(request.GetString("query", "")), strings.TrimSpace(request.GetString("cursor", "")), limit)
	if err != nil {
		return NewTypedErrorResult(peopleChannelsError(err, false)), nil
	}
	data := EmojiPageData{Emoji: page.Emoji}
	return NewStructuredResult(data, SlackResultMeta(page.NextCursor, false, ""), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) CreateChannel(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ChannelsCreateHandler called", request)
	if !requireToolEnabled(channelCreateGate, "channels_create") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "channels_create is disabled"}), nil
	}
	name := strings.TrimSpace(request.GetString("name", ""))
	if !channelNamePattern.MatchString(name) {
		return NewTypedErrorResult(invalidArguments("name must be 1-80 lowercase letters, numbers, hyphens, or underscores")), nil
	}
	teamID := strings.TrimSpace(request.GetString("team_id", ""))
	if teamID == "" {
		teamID = h.identity().TeamID
	}
	state, err := h.service.CreateChannel(ctx, name, request.GetBool("is_private", false), teamID)
	if err != nil {
		return NewTypedErrorResult(peopleChannelsError(err, true)), nil
	}
	data := ChannelData{Channel: state}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) ListChannelMembers(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ChannelsMembersHandler called", request)
	channelID, err := requiredChannelID(request)
	if err != nil {
		return NewTypedErrorResult(invalidArguments(err.Error())), nil
	}
	limit := request.GetInt("limit", 100)
	if limit < 1 || limit > maxPeoplePageSize {
		return NewTypedErrorResult(invalidArguments("limit must be between 1 and 200")), nil
	}
	page, err := h.service.ListChannelMembers(ctx, channelID, strings.TrimSpace(request.GetString("cursor", "")), limit)
	if err != nil {
		return NewTypedErrorResult(peopleChannelsError(err, false)), nil
	}
	data := ChannelMembersData{ChannelID: page.ChannelID, UserIDs: page.UserIDs}
	return NewStructuredResult(data, SlackResultMeta(page.NextCursor, false, ""), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) InviteChannelMembers(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ChannelsInviteHandler called", request)
	if !requireToolEnabled(channelInviteGate, "channels_invite") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "channels_invite is disabled"}), nil
	}
	channelID, err := requiredChannelID(request)
	if err != nil {
		return NewTypedErrorResult(invalidArguments(err.Error())), nil
	}
	userIDs, err := request.RequireStringSlice("user_ids")
	if err != nil || len(userIDs) == 0 {
		return NewTypedErrorResult(invalidArguments("user_ids must contain at least one user ID")), nil
	}
	if len(userIDs) > 1000 {
		return NewTypedErrorResult(invalidArguments("user_ids must contain at most 1000 user IDs")), nil
	}
	unique := make(map[string]struct{}, len(userIDs))
	for i, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if err := validateUserID(userID); err != nil {
			return NewTypedErrorResult(invalidArguments(fmt.Sprintf("user_ids[%d] is invalid", i))), nil
		}
		unique[userID] = struct{}{}
	}
	userIDs = userIDs[:0]
	for userID := range unique {
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	state, err := h.service.InviteChannelMembers(ctx, channelID, userIDs)
	if err != nil {
		return NewTypedErrorResult(peopleChannelsError(err, true)), nil
	}
	data := ChannelData{Channel: state}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) requireUserIdentity() (provider.ProviderIdentity, error) {
	identity := h.identity()
	if identity.ActorType != "user" || identity.UserID == "" || identity.TeamID == "" {
		return provider.ProviderIdentity{}, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	return identity, nil
}

func parseProfileUpdate(request mcp.CallToolRequest) (provider.UserProfileUpdate, error) {
	arguments := request.GetArguments()
	update := provider.UserProfileUpdate{}
	fields := []struct {
		name string
		max  int
		dest **string
	}{
		{"first_name", 80, &update.FirstName}, {"last_name", 80, &update.LastName},
		{"real_name", 80, &update.RealName}, {"display_name", 80, &update.DisplayName},
		{"pronouns", 100, &update.Pronouns}, {"email", 320, &update.Email},
		{"phone", 100, &update.Phone}, {"skype", 100, &update.Skype},
		{"title", 100, &update.Title}, {"start_date", 10, &update.StartDate},
	}
	for _, field := range fields {
		value, present := arguments[field.name]
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return provider.UserProfileUpdate{}, invalidArguments(field.name + " must be a string")
		}
		if utf8.RuneCountInString(text) > field.max {
			return provider.UserProfileUpdate{}, invalidArguments(fmt.Sprintf("%s must be at most %d characters", field.name, field.max))
		}
		copy := text
		*field.dest = &copy
	}
	if update.Email != nil && *update.Email != "" {
		address, err := mail.ParseAddress(*update.Email)
		if err != nil || address.Address != *update.Email {
			return provider.UserProfileUpdate{}, invalidArguments("email must be a valid email address or empty to clear")
		}
	}
	if update.StartDate != nil && *update.StartDate != "" {
		if _, err := time.Parse("2006-01-02", *update.StartDate); err != nil {
			return provider.UserProfileUpdate{}, invalidArguments("start_date must use YYYY-MM-DD or be empty to clear")
		}
	}
	if raw, present := arguments["custom_fields"]; present {
		encoded, err := json.Marshal(raw)
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err != nil || decoder.Decode(&update.CustomFields) != nil {
			return provider.UserProfileUpdate{}, invalidArguments("custom_fields must map field IDs to {value, alt} objects")
		}
		if len(update.CustomFields) > 50 {
			return provider.UserProfileUpdate{}, invalidArguments("custom_fields must contain at most 50 fields")
		}
		for id, field := range update.CustomFields {
			if utf8.RuneCountInString(field.Alt) > 256 {
				return provider.UserProfileUpdate{}, invalidArguments("custom_fields[" + id + "].alt must be at most 256 characters")
			}
		}
	}
	if update.Empty() {
		return provider.UserProfileUpdate{}, invalidArguments("at least one profile field is required")
	}
	return update, nil
}

func presentString(request mcp.CallToolRequest, name string) (string, error) {
	value, present := request.GetArguments()[name]
	if !present {
		return "", invalidArguments(name + " is required; pass an empty string to clear it")
	}
	text, ok := value.(string)
	if !ok {
		return "", invalidArguments(name + " must be a string")
	}
	return text, nil
}

func normalizeEmoji(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	wrapped := strings.HasPrefix(value, ":") && strings.HasSuffix(value, ":")
	if strings.Contains(value, ":") && !wrapped {
		return "", invalidArguments("status_emoji must be an emoji name with optional surrounding colons, or empty to clear")
	}
	name := strings.Trim(value, ":")
	if name == "" || !emojiNamePattern.MatchString(name) || strings.Count(value, ":") > 2 {
		return "", invalidArguments("status_emoji must be an emoji name with optional surrounding colons, or empty to clear")
	}
	return ":" + name + ":", nil
}

func validateUserID(userID string) error {
	if !userIDPattern.MatchString(userID) {
		return invalidArguments("user_id must be a Slack user ID starting with U or W")
	}
	return nil
}

func invalidArguments(message string) *ToolError {
	return &ToolError{Code: "invalid_arguments", Message: message}
}

func peopleChannelsError(err error, mutationAttempted bool) error {
	var typed *ToolError
	if errors.As(err, &typed) {
		return typed
	}
	if errors.Is(err, provider.ErrUserOAuthRequired) {
		return &ToolError{Code: "user_oauth_required", Message: err.Error(), Cause: err}
	}
	if errors.Is(err, provider.ErrInvalidPaginationCursor) {
		return &ToolError{Code: "invalid_cursor", Message: err.Error(), Cause: err}
	}
	var rateLimited *slack.RateLimitedError
	if errors.As(err, &rateLimited) {
		return &ToolError{Code: "rate_limited", Message: err.Error(), Retryable: !mutationAttempted, RetryAfter: rateLimited.RetryAfter, Cause: err}
	}
	var networkError net.Error
	if mutationAttempted && (errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout())) {
		return &ToolError{Code: "outcome_unknown", Message: "Slack may have accepted the change; observe current Slack state before another attempt", Cause: err}
	}
	var slackError slack.SlackErrorResponse
	if errors.As(err, &slackError) {
		switch slackError.Err {
		case "missing_scope", "not_authed", "invalid_auth", "not_allowed_token_type", "restricted_action", "not_allowed":
			return &ToolError{Code: "permission_denied", Message: slackError.Err, Cause: err}
		case "user_not_found", "channel_not_found":
			return &ToolError{Code: "not_found", Message: slackError.Err, Cause: err}
		case "name_taken", "already_in_channel", "cant_invite_self":
			return &ToolError{Code: "conflict", Message: slackError.Err, Cause: err}
		case "invalid_cursor":
			return &ToolError{Code: "invalid_cursor", Message: slackError.Err, Cause: err}
		}
	}
	return &ToolError{Code: "slack_error", Message: err.Error(), Retryable: !mutationAttempted, Cause: err}
}
