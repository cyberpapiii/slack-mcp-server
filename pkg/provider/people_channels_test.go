package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePeopleChannelsClient struct {
	profile         *slack.UserProfile
	emoji           map[string]string
	members         []string
	nextCursor      string
	channel         *slack.Channel
	mutationErr     error
	profileUpdates  int
	statusUpdates   int
	channelCreates  int
	channelInvites  int
	lastUpdate      UserProfileUpdate
	lastStatusEmoji string
	lastUsers       []string
}

func (f *fakePeopleChannelsClient) GetUserProfileContext(context.Context, *slack.GetUserProfileParameters) (*slack.UserProfile, error) {
	return f.profile, nil
}

func (f *fakePeopleChannelsClient) UpdateUserProfileContext(_ context.Context, update UserProfileUpdate) (*slack.UserProfile, error) {
	f.profileUpdates++
	f.lastUpdate = update
	return f.profile, f.mutationErr
}

func (f *fakePeopleChannelsClient) SetUserCustomStatusContext(_ context.Context, _ string, emoji string, _ int64) error {
	f.statusUpdates++
	f.lastStatusEmoji = emoji
	return f.mutationErr
}

func (f *fakePeopleChannelsClient) GetEmojiContext(context.Context) (map[string]string, error) {
	return f.emoji, nil
}

func (f *fakePeopleChannelsClient) CreateConversationContext(context.Context, slack.CreateConversationParams) (*slack.Channel, error) {
	f.channelCreates++
	return f.channel, f.mutationErr
}

func (f *fakePeopleChannelsClient) GetUsersInConversationContext(context.Context, *slack.GetUsersInConversationParameters) ([]string, string, error) {
	return f.members, f.nextCursor, nil
}

func (f *fakePeopleChannelsClient) InviteUsersToConversationContext(_ context.Context, _ string, users ...string) (*slack.Channel, error) {
	f.channelInvites++
	f.lastUsers = append([]string(nil), users...)
	return f.channel, f.mutationErr
}

func TestPeopleChannelsProfileReadAndExplicitClear(t *testing.T) {
	fields := slack.UserProfileCustomFields{}
	fields.SetMap(map[string]slack.UserProfileCustomField{"X1": {Value: "blue", Label: "Color"}})
	fake := &fakePeopleChannelsClient{profile: &slack.UserProfile{DisplayName: "Rob", StatusEmoji: ":wave:", Fields: fields}}
	service := NewPeopleChannelsProvider(fake)

	profile, err := service.GetProfile(context.Background(), "U123", true)
	require.NoError(t, err)
	assert.Equal(t, "U123", profile.UserID)
	assert.Equal(t, "Rob", profile.DisplayName)
	assert.Equal(t, "blue", profile.CustomFields["X1"].Value)

	empty := ""
	updated, err := service.UpdateProfile(context.Background(), "U123", UserProfileUpdate{DisplayName: &empty})
	require.NoError(t, err)
	assert.Equal(t, 1, fake.profileUpdates)
	require.NotNil(t, fake.lastUpdate.DisplayName)
	assert.Empty(t, *fake.lastUpdate.DisplayName)
	assert.Equal(t, "Rob", updated.DisplayName)
}

func TestPeopleChannelsEmojiSearchSortAndPagination(t *testing.T) {
	fake := &fakePeopleChannelsClient{emoji: map[string]string{
		"party_parrot": "https://emoji/party", "party_blob": "alias:blob", "wave": "https://emoji/wave",
	}}
	service := NewPeopleChannelsProvider(fake)

	first, err := service.ListEmoji(context.Background(), "party", "", 1)
	require.NoError(t, err)
	require.Len(t, first.Emoji, 1)
	assert.Equal(t, "party_blob", first.Emoji[0].Name)
	assert.Equal(t, "blob", first.Emoji[0].AliasFor)
	assert.Equal(t, "1", first.NextCursor)

	second, err := service.ListEmoji(context.Background(), "party", first.NextCursor, 1)
	require.NoError(t, err)
	assert.Equal(t, "party_parrot", second.Emoji[0].Name)
	assert.Empty(t, second.NextCursor)

	_, err = service.ListEmoji(context.Background(), "", "bad", 1)
	require.ErrorIs(t, err, ErrInvalidPaginationCursor)
}

func TestPeopleChannelsPassesSlackChannelPaginationAndMutatesOnce(t *testing.T) {
	channel := &slack.Channel{GroupConversation: slack.GroupConversation{
		Conversation: slack.Conversation{ID: "C123", IsPrivate: true}, Name: "private-room",
	}}
	channel.NumMembers = 3
	fake := &fakePeopleChannelsClient{channel: channel, members: []string{"U1", "U2"}, nextCursor: "next"}
	service := NewPeopleChannelsProvider(fake)

	page, err := service.ListChannelMembers(context.Background(), "C123", "cursor", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"U1", "U2"}, page.UserIDs)
	assert.Equal(t, "next", page.NextCursor)

	created, err := service.CreateChannel(context.Background(), "private-room", true, "T1")
	require.NoError(t, err)
	assert.Equal(t, "C123", created.ChannelID)
	assert.True(t, created.Private)
	assert.Equal(t, 1, fake.channelCreates)

	_, err = service.InviteChannelMembers(context.Background(), "C123", []string{"U1", "U2"})
	require.NoError(t, err)
	assert.Equal(t, 1, fake.channelInvites)
	assert.Equal(t, []string{"U1", "U2"}, fake.lastUsers)
}

func TestPeopleChannelsDoesNotRetryMutations(t *testing.T) {
	fake := &fakePeopleChannelsClient{mutationErr: errors.New("timeout")}
	service := NewPeopleChannelsProvider(fake)
	empty := ""

	_, _ = service.UpdateProfile(context.Background(), "U1", UserProfileUpdate{Title: &empty})
	_, _ = service.SetStatus(context.Background(), "U1", "away", ":away:", 0)
	_, _ = service.CreateChannel(context.Background(), "room", false, "T1")
	_, _ = service.InviteChannelMembers(context.Background(), "C1", []string{"U2"})

	assert.Equal(t, 1, fake.profileUpdates)
	assert.Equal(t, 1, fake.statusUpdates)
	assert.Equal(t, 1, fake.channelCreates)
	assert.Equal(t, 1, fake.channelInvites)
}

func TestUnitPeopleChannelsUsesWrapper(t *testing.T) {
	_, err := (&ApiProvider{}).PeopleChannels()
	require.Error(t, err)

	got, err := (&ApiProvider{client: &MCPSlackClient{slackClient: slack.New("xoxp-test")}}).PeopleChannels()
	require.NoError(t, err)
	require.NotNil(t, got)
}
