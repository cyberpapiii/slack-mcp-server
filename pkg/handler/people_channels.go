package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	defaultPeoplePageSize = 100
	maxPeoplePageSize     = 200
	maxEmojiPageSize      = 200

	maxStatusTextRunes = 100
	maxInvitees        = 1000
	maxCustomFields    = 50
	maxCustomFieldAlt  = 256
)

// profileFields are the users_set_profile string fields and the most
// characters Slack accepts in each.
var profileFields = []struct {
	name string
	max  int
}{
	{"first_name", 80}, {"last_name", 80}, {"real_name", 80}, {"display_name", 80},
	{"pronouns", 100}, {"email", 320}, {"phone", 100}, {"skype", 100},
	{"title", 100}, {"start_date", 10},
}

func profileFieldMax(name string) int {
	for _, field := range profileFields {
		if field.name == name {
			return field.max
		}
	}
	panic("no profile field " + name)
}

var (
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

// EmojiRow is one emoji_list CSV row. Alias is the canonical emoji name when
// this entry is an alias; URL is set for custom images.
type EmojiRow struct {
	Name  string `csv:"Name"`
	URL   string `csv:"URL"`
	Alias string `csv:"Alias"`
}

// ChannelMemberRow is one channels_members CSV row. Slack's members page only
// carries IDs; Name comes from the users cache and is empty when unknown.
type ChannelMemberRow struct {
	UserID string `csv:"UserID"`
	Name   string `csv:"Name"`
}

type ChannelData struct {
	Channel provider.ChannelState `json:"channel"`
}

type PeopleChannelsHandler struct {
	service  PeopleChannelsService
	identity func() provider.ProviderIdentity
	labels   Labels
}

func NewPeopleChannelsHandler(service PeopleChannelsService, labels Labels, identity func() provider.ProviderIdentity) *PeopleChannelsHandler {
	return &PeopleChannelsHandler{service: service, labels: labels, identity: identity}
}

func (h *PeopleChannelsHandler) GetUserProfile(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID := strings.TrimSpace(request.GetString("user_id", ""))
	if userID == "" {
		userID = h.identity().UserID
	}
	if err := validateUserID(userID); err != nil {
		return nil, err
	}
	profile, err := h.service.GetProfile(ctx, userID, request.GetBool("include_labels", true))
	if err != nil {
		return nil, err
	}
	data := ProfileData{Profile: profile}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) SetUserProfile(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	identity, err := requireUserActor(h.identity())
	if err != nil {
		return nil, err
	}
	update, err := parseProfileUpdate(request)
	if err != nil {
		return nil, err
	}
	profile, err := h.service.UpdateProfile(ctx, identity.UserID, update)
	if err != nil {
		return nil, err
	}
	data := ProfileData{Profile: profile}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) SetUserStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	identity, err := requireUserActor(h.identity())
	if err != nil {
		return nil, err
	}
	text, err := presentString(request, "status_text")
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(text) > maxStatusTextRunes {
		return nil, invalidArguments(fmt.Sprintf("status_text must be at most %d characters", maxStatusTextRunes))
	}
	emoji, err := presentString(request, "status_emoji")
	if err != nil {
		return nil, err
	}
	emoji, err = normalizeEmoji(emoji)
	if err != nil {
		return nil, err
	}
	expiration := int64(request.GetInt("status_expiration", 0))
	if expiration < 0 {
		return nil, invalidArguments("status_expiration must be 0 or a Unix timestamp")
	}
	profile, err := h.service.SetStatus(ctx, identity.UserID, text, emoji, expiration)
	if err != nil {
		return nil, err
	}
	data := ProfileData{Profile: profile}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) ListEmoji(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := pageLimit(request, defaultPeoplePageSize, maxEmojiPageSize)
	page, err := h.service.ListEmoji(ctx, strings.TrimSpace(request.GetString("query", "")), strings.TrimSpace(request.GetString("cursor", "")), limit)
	if err != nil {
		return nil, err
	}
	rows := make([]EmojiRow, len(page.Emoji))
	for i, e := range page.Emoji {
		rows[i] = EmojiRow{Name: e.Name, URL: e.URL, Alias: e.AliasFor}
	}
	return csvResult("", SlackResultMeta(page.NextCursor, false, ""), &rows)
}

func (h *PeopleChannelsHandler) CreateChannel(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := strings.TrimSpace(request.GetString("name", ""))
	if !channelNamePattern.MatchString(name) {
		return nil, invalidArguments("name must be 1-80 lowercase letters, numbers, hyphens, or underscores")
	}
	teamID := strings.TrimSpace(request.GetString("team_id", ""))
	if teamID == "" {
		teamID = h.identity().TeamID
	}
	state, err := h.service.CreateChannel(ctx, name, request.GetBool("is_private", false), teamID)
	if err != nil {
		return nil, err
	}
	data := ChannelData{Channel: state}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func (h *PeopleChannelsHandler) ListChannelMembers(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	limit := pageLimit(request, defaultPeoplePageSize, maxPeoplePageSize)
	page, err := h.service.ListChannelMembers(ctx, channelID, strings.TrimSpace(request.GetString("cursor", "")), limit)
	if err != nil {
		return nil, err
	}
	rows := make([]ChannelMemberRow, len(page.UserIDs))
	for i, id := range page.UserIDs {
		rows[i] = ChannelMemberRow{UserID: id, Name: h.labels.UserLabel(id)}
	}
	return csvResult("", SlackResultMeta(page.NextCursor, false, ""), &rows)
}

func (h *PeopleChannelsHandler) InviteChannelMembers(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	userIDs, err := request.RequireStringSlice("user_ids")
	if err != nil || len(userIDs) == 0 {
		return nil, invalidArguments("user_ids must contain at least one user ID")
	}
	if len(userIDs) > maxInvitees {
		return nil, invalidArguments(fmt.Sprintf("user_ids must contain at most %d user IDs", maxInvitees))
	}
	unique := make(map[string]struct{}, len(userIDs))
	for i, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if err := validateUserID(userID); err != nil {
			return nil, invalidArguments(fmt.Sprintf("user_ids[%d] is invalid", i))
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
		return nil, err
	}
	data := ChannelData{Channel: state}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func parseProfileUpdate(request mcp.CallToolRequest) (provider.UserProfileUpdate, error) {
	arguments := request.GetArguments()
	update := provider.UserProfileUpdate{}
	dests := map[string]**string{
		"first_name": &update.FirstName, "last_name": &update.LastName,
		"real_name": &update.RealName, "display_name": &update.DisplayName,
		"pronouns": &update.Pronouns, "email": &update.Email,
		"phone": &update.Phone, "skype": &update.Skype,
		"title": &update.Title, "start_date": &update.StartDate,
	}
	for _, field := range profileFields {
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
		*dests[field.name] = &copy
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
		if len(update.CustomFields) > maxCustomFields {
			return provider.UserProfileUpdate{}, invalidArguments(fmt.Sprintf("custom_fields must contain at most %d fields", maxCustomFields))
		}
		for id, field := range update.CustomFields {
			if utf8.RuneCountInString(field.Alt) > maxCustomFieldAlt {
				return provider.UserProfileUpdate{}, invalidArguments(fmt.Sprintf("custom_fields[%s].alt must be at most %d characters", id, maxCustomFieldAlt))
			}
		}
	}
	if update.Empty() {
		return provider.UserProfileUpdate{}, invalidArguments("at least one profile field is required")
	}
	return update, nil
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
	if provider.ClassifyID(userID) != provider.IDUser {
		return invalidArguments("user_id must be a Slack user ID starting with U or W")
	}
	return nil
}

func (h *PeopleChannelsHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.UsersGetProfile, Run: h.GetUserProfile, Options: []mcp.ToolOption{
			mcp.WithDescription("Read a full Slack user profile."),
			mcp.WithString("user_id", mcp.Description("User ID starting with U or W; defaults to the authenticated user.")),
			mcp.WithBoolean("include_labels", mcp.Description("Include the display labels of custom profile fields; defaults to true.")),
		}},
		{Name: capability.UsersSetProfile, Run: h.SetUserProfile, Options: []mcp.ToolOption{
			mcp.WithDescription("Update the authenticated user's Slack profile."),
			mcp.WithString("first_name", mcp.Description(fmt.Sprintf("First name, up to %d characters; empty string clears it.", profileFieldMax("first_name")))),
			mcp.WithString("last_name", mcp.Description(fmt.Sprintf("Last name, up to %d characters; empty string clears it.", profileFieldMax("last_name")))),
			mcp.WithString("real_name", mcp.Description(fmt.Sprintf("Full name, up to %d characters; empty string clears it.", profileFieldMax("real_name")))),
			mcp.WithString("display_name", mcp.Description(fmt.Sprintf("Display name shown in Slack, up to %d characters; empty string clears it.", profileFieldMax("display_name")))),
			mcp.WithString("pronouns", mcp.Description(fmt.Sprintf("Pronouns, up to %d characters; empty string clears them.", profileFieldMax("pronouns")))),
			mcp.WithString("email", mcp.Description(fmt.Sprintf("Email address, up to %d characters; empty string clears it.", profileFieldMax("email")))),
			mcp.WithString("phone", mcp.Description(fmt.Sprintf("Phone number, up to %d characters; empty string clears it.", profileFieldMax("phone")))),
			mcp.WithString("skype", mcp.Description(fmt.Sprintf("Skype handle, up to %d characters; empty string clears it.", profileFieldMax("skype")))),
			mcp.WithString("title", mcp.Description(fmt.Sprintf("Job title, up to %d characters; empty string clears it.", profileFieldMax("title")))),
			mcp.WithString("start_date", mcp.Description("Start date in YYYY-MM-DD format; empty string clears it.")),
			mcp.WithObject("custom_fields", mcp.Description(fmt.Sprintf("Map of custom field ID (Xf...) to {value, alt} objects; at most %d fields.", maxCustomFields))),
		}},
		{Name: capability.UsersSetStatus, Run: h.SetUserStatus, Options: []mcp.ToolOption{
			mcp.WithDescription("Set or clear the authenticated user's Slack status."),
			mcp.WithString("status_text", mcp.Required(), mcp.Description(fmt.Sprintf("Status text, up to %d characters; empty string clears the status.", maxStatusTextRunes))),
			mcp.WithString("status_emoji", mcp.Required(), mcp.Description("Emoji name such as :palm_tree: (colons optional); empty string clears it.")),
			mcp.WithNumber("status_expiration", mcp.Description("Unix seconds when the status clears; 0 keeps it until changed.")),
		}},
		{Name: capability.EmojiList, Run: h.ListEmoji, Options: []mcp.ToolOption{
			mcp.WithDescription("List and search workspace custom emoji."),
			mcp.WithString("query", mcp.Description("Case-insensitive substring to match against emoji names.")),
			mcp.WithString("cursor", mcp.Description(descCursor)),
			mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("Maximum emoji per page, 1 to %d; defaults to %d.", maxEmojiPageSize, defaultPeoplePageSize))),
		}},
		{Name: capability.ChannelsCreate, Run: h.CreateChannel, Options: []mcp.ToolOption{
			mcp.WithDescription("Create a public or private Slack channel."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Channel name, 1 to 80 lowercase letters, numbers, hyphens, or underscores.")),
			mcp.WithBoolean("is_private", mcp.Description("Create a private channel; defaults to false (public).")),
			mcp.WithString("team_id", mcp.Description("Workspace (team) ID for Enterprise Grid orgs; defaults to the authenticated user's team.")),
		}},
		{Name: capability.ChannelsMembers, Run: h.ListChannelMembers, Options: []mcp.ToolOption{
			mcp.WithDescription("List channel members. Returns CSV with UserID and Name (empty when the user is not in the cache)."),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)),
			mcp.WithString("cursor", mcp.Description(descCursor)),
			mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("Maximum members per page, 1 to %d; defaults to %d.", maxPeoplePageSize, defaultPeoplePageSize))),
		}},
		{Name: capability.ChannelsInvite, Run: h.InviteChannelMembers, Options: []mcp.ToolOption{
			mcp.WithDescription("Invite users to a Slack channel."),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)),
			mcp.WithArray("user_ids", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description(fmt.Sprintf("User IDs starting with U or W to invite; 1 to %d entries, duplicates ignored.", maxInvitees))),
		}},
	}
}
