package handler

import (
	"context"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakePeopleChannelsService struct {
	profile       provider.UserProfile
	emoji         provider.EmojiPage
	members       provider.ChannelMembersPage
	channel       provider.ChannelState
	err           error
	update        provider.UserProfileUpdate
	statusEmoji   string
	statusExpires int64
	channelName   string
	invited       []string
	updateCalls   int
	statusCalls   int
	createCalls   int
	inviteCalls   int
}

func (f *fakePeopleChannelsService) GetProfile(context.Context, string, bool) (provider.UserProfile, error) {
	return f.profile, f.err
}
func (f *fakePeopleChannelsService) UpdateProfile(_ context.Context, _ string, update provider.UserProfileUpdate) (provider.UserProfile, error) {
	f.updateCalls++
	f.update = update
	return f.profile, f.err
}
func (f *fakePeopleChannelsService) SetStatus(_ context.Context, _ string, _ string, emoji string, expiration int64) (provider.UserProfile, error) {
	f.statusCalls++
	f.statusEmoji = emoji
	f.statusExpires = expiration
	return f.profile, f.err
}
func (f *fakePeopleChannelsService) ListEmoji(context.Context, string, string, int) (provider.EmojiPage, error) {
	return f.emoji, f.err
}
func (f *fakePeopleChannelsService) CreateChannel(_ context.Context, name string, _ bool, _ string) (provider.ChannelState, error) {
	f.createCalls++
	f.channelName = name
	return f.channel, f.err
}
func (f *fakePeopleChannelsService) ListChannelMembers(context.Context, string, string, int) (provider.ChannelMembersPage, error) {
	return f.members, f.err
}
func (f *fakePeopleChannelsService) InviteChannelMembers(_ context.Context, _ string, users []string) (provider.ChannelState, error) {
	f.inviteCalls++
	f.invited = append([]string(nil), users...)
	return f.channel, f.err
}

func peopleRequest(arguments map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: arguments}}
}

func newPeopleHandler(service PeopleChannelsService) *PeopleChannelsHandler {
	return NewPeopleChannelsHandler(service, func() provider.ProviderIdentity {
		return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "user", TokenMode: "user-oauth"}
	}, zap.NewNop())
}

func TestPeopleHandlerProfileReadDefaultsToActor(t *testing.T) {
	service := &fakePeopleChannelsService{profile: provider.UserProfile{UserID: "U1", DisplayName: "Rob"}}
	result, err := newPeopleHandler(service).GetUserProfile(context.Background(), peopleRequest(map[string]any{}))
	require.NoError(t, err)
	structured := result.StructuredContent.(ToolResult[ProfileData])
	assert.Equal(t, "Rob", structured.Data.Profile.DisplayName)
	assert.Equal(t, TrustUntrusted, structured.Meta.Provenance.Trust)
}

func TestPeopleHandlerProfileUpdateSupportsExplicitClears(t *testing.T) {
	t.Setenv(profileWriteGate, "true")
	service := &fakePeopleChannelsService{profile: provider.UserProfile{UserID: "U1"}}
	result, err := newPeopleHandler(service).SetUserProfile(context.Background(), peopleRequest(map[string]any{
		"display_name": "", "custom_fields": map[string]any{"X1": map[string]any{"value": "blue"}},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Equal(t, 1, service.updateCalls)
	require.NotNil(t, service.update.DisplayName)
	assert.Empty(t, *service.update.DisplayName)
	assert.Equal(t, "blue", service.update.CustomFields["X1"].Value)
}

func TestPeopleHandlerRejectsEmptyProfileMutation(t *testing.T) {
	t.Setenv(profileWriteGate, "true")
	service := &fakePeopleChannelsService{}
	result, err := newPeopleHandler(service).SetUserProfile(context.Background(), peopleRequest(map[string]any{}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Equal(t, 0, service.updateCalls)
	structured := result.StructuredContent.(ToolResult[struct{}])
	assert.Equal(t, "invalid_arguments", structured.Error.Code)
}

func TestPeopleHandlerStatusNormalizesEmojiAndDoesNotRetry(t *testing.T) {
	t.Setenv(profileWriteGate, "true")
	service := &fakePeopleChannelsService{err: context.DeadlineExceeded}
	result, err := newPeopleHandler(service).SetUserStatus(context.Background(), peopleRequest(map[string]any{
		"status_text": "Heads down", "status_emoji": "focus", "status_expiration": 123,
	}))
	require.NoError(t, err)
	assert.Equal(t, 1, service.statusCalls)
	assert.Equal(t, ":focus:", service.statusEmoji)
	structured := result.StructuredContent.(ToolResult[struct{}])
	assert.Equal(t, "outcome_unknown", structured.Error.Code)
	assert.False(t, structured.Error.Retryable)
}

func TestPeopleHandlerStatusRejectsHalfWrappedEmoji(t *testing.T) {
	t.Setenv(profileWriteGate, "true")
	service := &fakePeopleChannelsService{}
	result, err := newPeopleHandler(service).SetUserStatus(context.Background(), peopleRequest(map[string]any{
		"status_text": "Heads down", "status_emoji": "focus:",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Zero(t, service.statusCalls)
}

func TestPeopleHandlerEmojiAndMembersExposeNextCursor(t *testing.T) {
	service := &fakePeopleChannelsService{
		emoji:   provider.EmojiPage{Emoji: []provider.Emoji{{Name: "wave"}}, NextCursor: "10"},
		members: provider.ChannelMembersPage{ChannelID: "C1", UserIDs: []string{"U1"}, NextCursor: "next"},
	}
	handler := newPeopleHandler(service)

	emojiResult, err := handler.ListEmoji(context.Background(), peopleRequest(map[string]any{"limit": 10}))
	require.NoError(t, err)
	emoji := emojiResult.StructuredContent.(ToolResult[EmojiPageData])
	assert.Equal(t, "10", emoji.Meta.NextCursor)

	membersResult, err := handler.ListChannelMembers(context.Background(), peopleRequest(map[string]any{"channel_id": "C1", "limit": 10}))
	require.NoError(t, err)
	members := membersResult.StructuredContent.(ToolResult[ChannelMembersData])
	assert.Equal(t, "next", members.Meta.NextCursor)
}

func TestPeopleHandlerCreateAndInviteValidateBeforeOneMutation(t *testing.T) {
	t.Setenv(channelCreateGate, "true")
	t.Setenv(channelInviteGate, "true")
	service := &fakePeopleChannelsService{channel: provider.ChannelState{ChannelID: "C1", Name: "new-room"}}
	handler := newPeopleHandler(service)

	created, err := handler.CreateChannel(context.Background(), peopleRequest(map[string]any{"name": "new-room"}))
	require.NoError(t, err)
	assert.False(t, created.IsError)
	assert.Equal(t, 1, service.createCalls)

	invited, err := handler.InviteChannelMembers(context.Background(), peopleRequest(map[string]any{
		"channel_id": "C1", "user_ids": []any{"U2", "U1", "U2"},
	}))
	require.NoError(t, err)
	assert.False(t, invited.IsError)
	assert.Equal(t, 1, service.inviteCalls)
	assert.Equal(t, []string{"U1", "U2"}, service.invited)

	bad, err := handler.CreateChannel(context.Background(), peopleRequest(map[string]any{"name": "Bad Name"}))
	require.NoError(t, err)
	assert.True(t, bad.IsError)
	assert.Equal(t, 1, service.createCalls)
}

func TestPeopleHandlerWriteGatesFailClosed(t *testing.T) {
	service := &fakePeopleChannelsService{}
	handler := newPeopleHandler(service)

	profile, _ := handler.SetUserProfile(context.Background(), peopleRequest(map[string]any{"title": "Editor"}))
	created, _ := handler.CreateChannel(context.Background(), peopleRequest(map[string]any{"name": "room"}))
	invited, _ := handler.InviteChannelMembers(context.Background(), peopleRequest(map[string]any{"channel_id": "C1", "user_ids": []string{"U2"}}))

	assert.True(t, profile.IsError)
	assert.True(t, created.IsError)
	assert.True(t, invited.IsError)
	assert.Zero(t, service.updateCalls+service.createCalls+service.inviteCalls)
}
