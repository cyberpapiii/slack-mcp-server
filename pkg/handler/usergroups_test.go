package handler

import (
	"context"
	"strings"
	"testing"

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

func TestUsergroupsListAndMineReturnCSVOnly(t *testing.T) {
	api := &fakeUsergroupsAPI{groups: []slack.UserGroup{
		{ID: "S1", Name: "eng", Handle: "eng", UserCount: 2, Users: []string{"U1", "U2"}},
		{ID: "S2", Name: "ops", Handle: "ops", UserCount: 1, Users: []string{"U3"}},
		{ID: "S3", Name: "empty"},
	}}
	handler := newTestUsergroupsHandler(api)

	list, err := handler.UsergroupsListHandler(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	assert.Nil(t, list.StructuredContent)
	assert.Equal(t, "ID,Name,Handle,Description,UserCount,IsExternal,DateCreate,DateUpdate,Users\nS1,eng,eng,,2,false,,,U1;U2\nS2,ops,ops,,1,false,,,U3\nS3,empty,,,0,false,,,\n", ResultText(list))

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
