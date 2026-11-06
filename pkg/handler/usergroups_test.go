package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeUsergroupsAPI struct {
	groups  []slack.UserGroup
	members []string
	updated string
}

// newTestUsergroupsHandler acts as user U1.
func newTestUsergroupsHandler(api UsergroupsAPI) *UsergroupsHandler {
	return NewUsergroupsHandler(api, func() provider.ProviderIdentity {
		return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "user"}
	}, zap.NewNop())
}
func (f *fakeUsergroupsAPI) GetUserGroupsContext(context.Context, ...slack.GetUserGroupsOption) ([]slack.UserGroup, error) {
	return f.groups, nil
}
func (f *fakeUsergroupsAPI) GetUserGroupMembersContext(context.Context, string, ...slack.GetUserGroupMembersOption) ([]string, error) {
	return append([]string(nil), f.members...), nil
}
func (f *fakeUsergroupsAPI) CreateUserGroupContext(context.Context, slack.UserGroup, ...slack.CreateUserGroupOption) (slack.UserGroup, error) {
	return slack.UserGroup{}, nil
}
func (f *fakeUsergroupsAPI) UpdateUserGroupContext(context.Context, string, ...slack.UpdateUserGroupsOption) (slack.UserGroup, error) {
	return slack.UserGroup{}, nil
}
func (f *fakeUsergroupsAPI) UpdateUserGroupMembersContext(_ context.Context, groupID, members string, _ ...slack.UpdateUserGroupMembersOption) (slack.UserGroup, error) {
	f.updated = members
	return slack.UserGroup{ID: groupID, Name: "eng", UserCount: len(strings.Split(members, ","))}, nil
}

func TestNewUserGroupFromSlack_UsersJoin(t *testing.T) {
	t.Run("joins member IDs with semicolon", func(t *testing.T) {
		ug := newUserGroupFromSlack(slack.UserGroup{
			ID:     "S1",
			Name:   "eng",
			Handle: "eng",
			Users:  []string{"U1", "U2"},
		})
		assert.Equal(t, "U1;U2", ug.Users)
	})

	t.Run("omits users when empty", func(t *testing.T) {
		ug := newUserGroupFromSlack(slack.UserGroup{ID: "S1", Name: "eng"})
		assert.Empty(t, ug.Users)

		csvBytes, err := gocsv.MarshalBytes(&[]UserGroup{ug})
		require.NoError(t, err)
		assert.Equal(t, "ID,Name,Handle,Description,UserCount,IsExternal,DateCreate,DateUpdate,Users\nS1,eng,,,0,false,,,\n", string(csvBytes))
	})

	t.Run("CSV round-trip keeps multi-user cell intact", func(t *testing.T) {
		ug := newUserGroupFromSlack(slack.UserGroup{
			ID:    "S1",
			Name:  "eng",
			Users: []string{"U1", "U2"},
		})
		csvBytes, err := gocsv.MarshalBytes(&[]UserGroup{ug})
		require.NoError(t, err)
		assert.Contains(t, string(csvBytes), "U1;U2")
		assert.NotContains(t, string(csvBytes), "U1,U2")
	})
}

func TestUsergroupsListAndMineReturnCSVOnly(t *testing.T) {
	api := &fakeUsergroupsAPI{groups: []slack.UserGroup{
		{ID: "S1", Name: "eng", Handle: "eng", UserCount: 2, Users: []string{"U1", "U2"}},
		{ID: "S2", Name: "ops", Handle: "ops", UserCount: 1, Users: []string{"U3"}},
	}}
	handler := newTestUsergroupsHandler(api)

	list, err := handler.UsergroupsListHandler(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	assert.Nil(t, list.StructuredContent)
	assert.Equal(t, "ID,Name,Handle,Description,UserCount,IsExternal,DateCreate,DateUpdate,Users\nS1,eng,eng,,2,false,,,U1;U2\nS2,ops,ops,,1,false,,,U3\n", ResultText(list))

	mine, err := handler.UsergroupsMineHandler(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	assert.Nil(t, mine.StructuredContent)
	assert.Equal(t, "ID,Name,Handle,Description,UserCount,IsExternal,DateCreate,DateUpdate,Users\nS1,eng,eng,,2,false,,,U1;U2\n", ResultText(mine))
}

func TestUsergroupsJoinAppendsCurrentUser(t *testing.T) {
	api := &fakeUsergroupsAPI{members: []string{"U2"}}
	handler := newTestUsergroupsHandler(api)
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"usergroup_id": "S1"}}}
	_, err := handler.UsergroupsJoinHandler(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "U2,U1", api.updated)
}

func TestUsergroupsLeaveRemovesOnlyCurrentUser(t *testing.T) {
	api := &fakeUsergroupsAPI{members: []string{"U2", "U1", "U3"}}
	handler := newTestUsergroupsHandler(api)
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"usergroup_id": "S1"}}}
	_, err := handler.UsergroupsLeaveHandler(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "U2,U3", api.updated)
}

func TestUnitUsergroupsMineUsesTheStartupIdentity(t *testing.T) {
	api := &fakeUsergroupsAPI{groups: []slack.UserGroup{
		{ID: "S1", Name: "eng", Handle: "eng", UserCount: 1, Users: []string{"U9"}},
		{ID: "S2", Name: "ops", Handle: "ops", UserCount: 1, Users: []string{"U1"}},
	}}
	handler := NewUsergroupsHandler(api, func() provider.ProviderIdentity { return provider.ProviderIdentity{UserID: "U9"} }, zap.NewNop())

	mine, err := handler.UsergroupsMineHandler(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	assert.Contains(t, ResultText(mine), "S1,eng")
	assert.NotContains(t, ResultText(mine), "S2,ops")
}
