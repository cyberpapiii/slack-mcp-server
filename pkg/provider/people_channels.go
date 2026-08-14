package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	transportpkg "github.com/korotovsky/slack-mcp-server/pkg/transport"
	"github.com/slack-go/slack"
)

var ErrInvalidPaginationCursor = errors.New("invalid pagination cursor")

// PeopleChannelsClient is the narrow Slack API surface used by profile, emoji,
// and channel-membership tools. Mutation methods are called once; callers must
// observe current Slack state before retrying an ambiguous failure.
var _ PeopleChannelsClient = (*MCPSlackClient)(nil)

type PeopleChannelsClient interface {
	GetUserProfileContext(context.Context, *slack.GetUserProfileParameters) (*slack.UserProfile, error)
	UpdateUserProfileContext(context.Context, UserProfileUpdate) (*slack.UserProfile, error)
	SetUserCustomStatusContext(context.Context, string, string, int64) error
	GetEmojiContext(context.Context) (map[string]string, error)
	CreateConversationContext(context.Context, slack.CreateConversationParams) (*slack.Channel, error)
	GetUsersInConversationContext(context.Context, *slack.GetUsersInConversationParameters) ([]string, string, error)
	InviteUsersToConversationContext(context.Context, string, ...string) (*slack.Channel, error)
}

type ProfileCustomField struct {
	Value string `json:"value"`
	Alt   string `json:"alt,omitempty"`
	Label string `json:"label,omitempty"`
}

type ProfileCustomFieldUpdate struct {
	Value string `json:"value"`
	Alt   string `json:"alt,omitempty"`
}

type UserProfile struct {
	UserID                string                                    `json:"user_id"`
	FirstName             string                                    `json:"first_name,omitempty"`
	LastName              string                                    `json:"last_name,omitempty"`
	RealName              string                                    `json:"real_name,omitempty"`
	RealNameNormalized    string                                    `json:"real_name_normalized,omitempty"`
	DisplayName           string                                    `json:"display_name,omitempty"`
	DisplayNameNormalized string                                    `json:"display_name_normalized,omitempty"`
	Pronouns              string                                    `json:"pronouns,omitempty"`
	Email                 string                                    `json:"email,omitempty"`
	Phone                 string                                    `json:"phone,omitempty"`
	Skype                 string                                    `json:"skype,omitempty"`
	Title                 string                                    `json:"title,omitempty"`
	StartDate             string                                    `json:"start_date,omitempty"`
	StatusText            string                                    `json:"status_text,omitempty"`
	StatusTextCanonical   string                                    `json:"status_text_canonical,omitempty"`
	StatusEmoji           string                                    `json:"status_emoji,omitempty"`
	StatusExpiresAt       int64                                     `json:"status_expires_at,omitempty"`
	StatusEmojiDisplay    []slack.UserProfileStatusEmojiDisplayInfo `json:"status_emoji_display_info,omitempty"`
	AvatarHash            string                                    `json:"avatar_hash,omitempty"`
	Image24               string                                    `json:"image_24,omitempty"`
	Image32               string                                    `json:"image_32,omitempty"`
	Image48               string                                    `json:"image_48,omitempty"`
	Image72               string                                    `json:"image_72,omitempty"`
	Image192              string                                    `json:"image_192,omitempty"`
	Image512              string                                    `json:"image_512,omitempty"`
	Image1024             string                                    `json:"image_1024,omitempty"`
	ImageOriginal         string                                    `json:"image_original,omitempty"`
	IsCustomImage         bool                                      `json:"is_custom_image"`
	AlwaysActive          bool                                      `json:"always_active"`
	TeamID                string                                    `json:"team_id,omitempty"`
	BotID                 string                                    `json:"bot_id,omitempty"`
	APIAppID              string                                    `json:"api_app_id,omitempty"`
	HuddleState           string                                    `json:"huddle_state,omitempty"`
	HuddleExpiresAt       int64                                     `json:"huddle_expires_at,omitempty"`
	CustomFields          map[string]ProfileCustomField             `json:"custom_fields,omitempty"`
}

// Pointer fields distinguish omission from an explicit empty-string clear.
type UserProfileUpdate struct {
	FirstName    *string                             `json:"first_name,omitempty"`
	LastName     *string                             `json:"last_name,omitempty"`
	RealName     *string                             `json:"real_name,omitempty"`
	DisplayName  *string                             `json:"display_name,omitempty"`
	Pronouns     *string                             `json:"pronouns,omitempty"`
	Email        *string                             `json:"email,omitempty"`
	Phone        *string                             `json:"phone,omitempty"`
	Skype        *string                             `json:"skype,omitempty"`
	Title        *string                             `json:"title,omitempty"`
	StartDate    *string                             `json:"start_date,omitempty"`
	CustomFields map[string]ProfileCustomFieldUpdate `json:"fields,omitempty"`
}

func (u UserProfileUpdate) Empty() bool {
	return u.FirstName == nil && u.LastName == nil && u.RealName == nil && u.DisplayName == nil &&
		u.Pronouns == nil && u.Email == nil && u.Phone == nil && u.Skype == nil &&
		u.Title == nil && u.StartDate == nil && u.CustomFields == nil
}

type Emoji struct {
	Name     string `json:"name"`
	URL      string `json:"url,omitempty"`
	AliasFor string `json:"alias_for,omitempty"`
}

type EmojiPage struct {
	Emoji      []Emoji `json:"emoji"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type ChannelState struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	Private   bool   `json:"private"`
	Archived  bool   `json:"archived"`
	Members   int    `json:"member_count,omitempty"`
}

type ChannelMembersPage struct {
	ChannelID  string   `json:"channel_id"`
	UserIDs    []string `json:"user_ids"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type PeopleChannelsProvider struct{ client PeopleChannelsClient }

func NewPeopleChannelsProvider(client PeopleChannelsClient) *PeopleChannelsProvider {
	return &PeopleChannelsProvider{client: client}
}

func (ap *ApiProvider) PeopleChannels() (*PeopleChannelsProvider, error) {
	client, ok := ap.client.(PeopleChannelsClient)
	if !ok || client == nil {
		return nil, errors.New("configured Slack client does not support people and channel operations")
	}
	return NewPeopleChannelsProvider(client), nil
}

func (p *PeopleChannelsProvider) GetProfile(ctx context.Context, userID string, includeLabels bool) (UserProfile, error) {
	profile, err := p.client.GetUserProfileContext(ctx, &slack.GetUserProfileParameters{UserID: userID, IncludeLabels: includeLabels})
	if err != nil {
		return UserProfile{}, err
	}
	if profile == nil {
		return UserProfile{}, errors.New("Slack returned no user profile")
	}
	return profileFromSlack(userID, profile), nil
}

func (p *PeopleChannelsProvider) UpdateProfile(ctx context.Context, userID string, update UserProfileUpdate) (UserProfile, error) {
	profile, err := p.client.UpdateUserProfileContext(ctx, update)
	if err != nil {
		return UserProfile{}, err
	}
	if profile == nil {
		return profileFromUpdate(userID, update), nil
	}
	return profileFromSlack(userID, profile), nil
}

func (p *PeopleChannelsProvider) SetStatus(ctx context.Context, userID, text, emoji string, expiration int64) (UserProfile, error) {
	if err := p.client.SetUserCustomStatusContext(ctx, text, emoji, expiration); err != nil {
		return UserProfile{}, err
	}
	return UserProfile{UserID: userID, StatusText: text, StatusEmoji: emoji, StatusExpiresAt: expiration}, nil
}

func (p *PeopleChannelsProvider) ListEmoji(ctx context.Context, query, cursor string, limit int) (EmojiPage, error) {
	offset, err := parseOffsetCursor(cursor)
	if err != nil {
		return EmojiPage{}, err
	}
	items, err := p.client.GetEmojiContext(ctx)
	if err != nil {
		return EmojiPage{}, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	names := make([]string, 0, len(items))
	for name := range items {
		if query == "" || strings.Contains(strings.ToLower(name), query) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if offset > len(names) {
		return EmojiPage{}, ErrInvalidPaginationCursor
	}
	end := offset + limit
	if end > len(names) {
		end = len(names)
	}
	page := EmojiPage{Emoji: make([]Emoji, 0, end-offset)}
	for _, name := range names[offset:end] {
		value := items[name]
		item := Emoji{Name: name, URL: value}
		if strings.HasPrefix(value, "alias:") {
			item.URL = ""
			item.AliasFor = strings.TrimPrefix(value, "alias:")
		}
		page.Emoji = append(page.Emoji, item)
	}
	if end < len(names) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func (p *PeopleChannelsProvider) CreateChannel(ctx context.Context, name string, private bool, teamID string) (ChannelState, error) {
	channel, err := p.client.CreateConversationContext(ctx, slack.CreateConversationParams{ChannelName: name, IsPrivate: private, TeamID: teamID})
	return channelState(channel, err)
}

func (p *PeopleChannelsProvider) ListChannelMembers(ctx context.Context, channelID, cursor string, limit int) (ChannelMembersPage, error) {
	users, nextCursor, err := p.client.GetUsersInConversationContext(ctx, &slack.GetUsersInConversationParameters{ChannelID: channelID, Cursor: cursor, Limit: limit})
	if err != nil {
		return ChannelMembersPage{}, err
	}
	return ChannelMembersPage{ChannelID: channelID, UserIDs: users, NextCursor: nextCursor}, nil
}

func (p *PeopleChannelsProvider) InviteChannelMembers(ctx context.Context, channelID string, userIDs []string) (ChannelState, error) {
	channel, err := p.client.InviteUsersToConversationContext(ctx, channelID, userIDs...)
	return channelState(channel, err)
}

func parseOffsetCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 {
		return 0, ErrInvalidPaginationCursor
	}
	return offset, nil
}

func channelState(channel *slack.Channel, err error) (ChannelState, error) {
	if err != nil {
		return ChannelState{}, err
	}
	if channel == nil {
		return ChannelState{}, errors.New("Slack returned no channel")
	}
	return ChannelState{ChannelID: channel.ID, Name: channel.Name, Private: channel.IsPrivate, Archived: channel.IsArchived, Members: channel.NumMembers}, nil
}

func profileFromSlack(userID string, profile *slack.UserProfile) UserProfile {
	fields := profile.Fields.ToMap()
	custom := make(map[string]ProfileCustomField, len(fields))
	for id, field := range fields {
		custom[id] = ProfileCustomField{Value: field.Value, Alt: field.Alt, Label: field.Label}
	}
	return UserProfile{
		UserID: userID, FirstName: profile.FirstName, LastName: profile.LastName,
		RealName: profile.RealName, RealNameNormalized: profile.RealNameNormalized,
		DisplayName: profile.DisplayName, DisplayNameNormalized: profile.DisplayNameNormalized, Pronouns: profile.Pronouns,
		Email: profile.Email, Phone: profile.Phone, Skype: profile.Skype, Title: profile.Title,
		StartDate: profile.StartDate, StatusText: profile.StatusText, StatusTextCanonical: profile.StatusTextCanonical,
		StatusEmoji: profile.StatusEmoji, StatusExpiresAt: int64(profile.StatusExpiration), StatusEmojiDisplay: profile.StatusEmojiDisplayInfo,
		AvatarHash: profile.AvatarHash, Image24: profile.Image24, Image32: profile.Image32,
		Image48: profile.Image48, Image72: profile.Image72, Image192: profile.Image192,
		Image512: profile.Image512, Image1024: profile.Image1024, ImageOriginal: profile.ImageOriginal,
		IsCustomImage: profile.IsCustomImage, AlwaysActive: profile.AlwaysActive, TeamID: profile.Team,
		BotID: profile.BotID, APIAppID: profile.ApiAppID, HuddleState: profile.HuddleState,
		HuddleExpiresAt: int64(profile.HuddleStateExpirationTS),
		CustomFields:    custom,
	}
}

func profileFromUpdate(userID string, update UserProfileUpdate) UserProfile {
	custom := make(map[string]ProfileCustomField, len(update.CustomFields))
	for id, field := range update.CustomFields {
		custom[id] = ProfileCustomField{Value: field.Value, Alt: field.Alt}
	}
	result := UserProfile{UserID: userID, CustomFields: custom}
	if update.FirstName != nil {
		result.FirstName = *update.FirstName
	}
	if update.LastName != nil {
		result.LastName = *update.LastName
	}
	if update.RealName != nil {
		result.RealName = *update.RealName
	}
	if update.DisplayName != nil {
		result.DisplayName = *update.DisplayName
	}
	if update.Pronouns != nil {
		result.Pronouns = *update.Pronouns
	}
	if update.Email != nil {
		result.Email = *update.Email
	}
	if update.Phone != nil {
		result.Phone = *update.Phone
	}
	if update.Skype != nil {
		result.Skype = *update.Skype
	}
	if update.Title != nil {
		result.Title = *update.Title
	}
	if update.StartDate != nil {
		result.StartDate = *update.StartDate
	}
	return result
}

func (c *MCPSlackClient) GetUserProfileContext(ctx context.Context, params *slack.GetUserProfileParameters) (*slack.UserProfile, error) {
	return c.standardSlackClient().GetUserProfileContext(ctx, params)
}

func (c *MCPSlackClient) SetUserCustomStatusContext(ctx context.Context, text, emoji string, expiration int64) error {
	if c.IsBotToken() {
		return ErrUserOAuthRequired
	}
	return c.standardSlackClient().SetUserCustomStatusContext(ctx, text, emoji, expiration)
}

func (c *MCPSlackClient) GetEmojiContext(ctx context.Context) (map[string]string, error) {
	return c.standardSlackClient().GetEmojiContext(ctx)
}

func (c *MCPSlackClient) CreateConversationContext(ctx context.Context, params slack.CreateConversationParams) (*slack.Channel, error) {
	return c.standardSlackClient().CreateConversationContext(ctx, params)
}

func (c *MCPSlackClient) GetUsersInConversationContext(ctx context.Context, params *slack.GetUsersInConversationParameters) ([]string, string, error) {
	return c.standardSlackClient().GetUsersInConversationContext(ctx, params)
}

func (c *MCPSlackClient) InviteUsersToConversationContext(ctx context.Context, channelID string, users ...string) (*slack.Channel, error) {
	return c.standardSlackClient().InviteUsersToConversationContext(ctx, channelID, users...)
}

func (c *MCPSlackClient) UpdateUserProfileContext(ctx context.Context, update UserProfileUpdate) (*slack.UserProfile, error) {
	if c.IsBotToken() {
		return nil, ErrUserOAuthRequired
	}
	profileJSON, err := json.Marshal(update)
	if err != nil {
		return nil, err
	}
	values := url.Values{"profile": {string(profileJSON)}}
	token, _ := c.oauthAccessToken.Load().(string)
	if token == "" {
		token = c.authProvider.SlackToken()
	}
	endpoint := "https://slack.com/api/users.profile.set"
	if os.Getenv("SLACK_MCP_GOVSLACK") == "true" {
		endpoint = "https://slack-gov.com/api/users.profile.set"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := transportpkg.ProvideHTTPClient(c.authProvider.Cookies(), c.logger).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		retryAfter, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		return nil, &slack.RateLimitedError{RetryAfter: time.Duration(retryAfter) * time.Second}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Slack users.profile.set returned HTTP %d", response.StatusCode)
	}
	var result struct {
		slack.SlackResponse
		Profile *slack.UserProfile `json:"profile"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	if err := result.Err(); err != nil {
		return nil, err
	}
	return result.Profile, nil
}
