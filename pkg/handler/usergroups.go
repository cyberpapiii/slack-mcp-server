package handler

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type UserGroup struct {
	ID          string `csv:"ID" json:"id" jsonschema:"User group ID"`
	Name        string `csv:"Name" json:"name" jsonschema:"Group display name"`
	Handle      string `csv:"Handle" json:"handle" jsonschema:"@mention handle"`
	Description string `csv:"Description" json:"description" jsonschema:"Group description"`
	UserCount   int    `csv:"UserCount" json:"user_count" jsonschema:"Number of members"`
	IsExternal  bool   `csv:"IsExternal" json:"is_external" jsonschema:"Whether group is external"`
	DateCreate  string `csv:"DateCreate" json:"date_create,omitempty" jsonschema:"Creation timestamp"`
	DateUpdate  string `csv:"DateUpdate" json:"date_update,omitempty" jsonschema:"Last update timestamp"`
	Users       string `csv:"Users" json:"users,omitempty" jsonschema:"Semicolon-separated user IDs when include_users=true"`
}

type UsergroupMeActionResult struct {
	Message   string `json:"message" jsonschema:"Result message"`
	GroupID   string `json:"group_id" jsonschema:"User group ID"`
	GroupName string `json:"group_name,omitempty" jsonschema:"User group name"`
	UserCount int    `json:"user_count,omitempty" jsonschema:"Number of members after action"`
}

type membershipAction string

const (
	membershipJoin  membershipAction = "join"
	membershipLeave membershipAction = "leave"
)

type UsergroupsHandler struct {
	api    UsergroupsAPI
	logger *zap.Logger
	// cachedUserID returns the user the startup auth.test named, or "".
	// Managed OAuth rotation keeps it valid: a rotated token must belong to
	// the same user before it is used.
	cachedUserID func() string
}

type UsergroupsAPI interface {
	AuthTest() (*slack.AuthTestResponse, error)
	GetUserGroupsContext(context.Context, ...slack.GetUserGroupsOption) ([]slack.UserGroup, error)
	GetUserGroupMembersContext(context.Context, string, ...slack.GetUserGroupMembersOption) ([]string, error)
	CreateUserGroupContext(context.Context, slack.UserGroup, ...slack.CreateUserGroupOption) (slack.UserGroup, error)
	UpdateUserGroupContext(context.Context, string, ...slack.UpdateUserGroupsOption) (slack.UserGroup, error)
	UpdateUserGroupMembersContext(context.Context, string, string, ...slack.UpdateUserGroupMembersOption) (slack.UserGroup, error)
}

func newUserGroupFromSlack(g slack.UserGroup) UserGroup {
	ug := UserGroup{
		ID:          g.ID,
		Name:        g.Name,
		Handle:      g.Handle,
		Description: g.Description,
		UserCount:   g.UserCount,
		IsExternal:  g.IsExternal,
		DateCreate:  unixTimeText(int64(g.DateCreate)),
		DateUpdate:  unixTimeText(int64(g.DateUpdate)),
	}
	if len(g.Users) > 0 {
		// Semicolon keeps CSV cells unambiguous (IDs never contain ';').
		ug.Users = strings.Join(g.Users, ";")
	}
	return ug
}

func NewUsergroupsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger) *UsergroupsHandler {
	return &UsergroupsHandler{
		api:          apiProvider.WebAPI(),
		logger:       logger,
		cachedUserID: func() string { return apiProvider.Identity().UserID },
	}
}

func newUsergroupsHandlerWithAPI(api UsergroupsAPI, logger *zap.Logger) *UsergroupsHandler {
	return &UsergroupsHandler{api: api, logger: logger}
}

// No IsReady check: usergroup handlers call the Slack API directly and do not
// depend on the users/channels cache, so cache readiness is not required.
func (h *UsergroupsHandler) UsergroupsListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	includeUsers := request.GetBool("include_users", false)
	includeCount := request.GetBool("include_count", true)
	includeDisabled := request.GetBool("include_disabled", false)

	options := []slack.GetUserGroupsOption{
		slack.GetUserGroupsOptionIncludeUsers(includeUsers),
		slack.GetUserGroupsOptionIncludeCount(includeCount),
		slack.GetUserGroupsOptionIncludeDisabled(includeDisabled),
	}

	groups, err := h.api.GetUserGroupsContext(ctx, options...)
	if err != nil {
		h.logger.Debug("GetUserGroupsContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Fetched user groups", zap.Int("count", len(groups)))

	userGroupList := make([]UserGroup, 0, len(groups))
	for _, g := range groups {
		userGroupList = append(userGroupList, newUserGroupFromSlack(g))
	}

	return csvResult("", SlackResultMeta("", false, ""), &userGroupList)
}

func (h *UsergroupsHandler) UsergroupsCreateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	name := request.GetString("name", "")
	if name == "" {
		return nil, invalidArguments("name is required")
	}

	handle := request.GetString("handle", "")
	description := request.GetString("description", "")
	channelsStr := request.GetString("channels", "")

	userGroup := slack.UserGroup{
		Name:        name,
		Handle:      handle,
		Description: description,
	}

	if channelsStr != "" {
		channels := parseCommaSeparatedList(channelsStr)
		userGroup.Prefs.Channels = channels
	}

	created, err := h.api.CreateUserGroupContext(ctx, userGroup)
	if err != nil {
		h.logger.Debug("CreateUserGroupContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Created user group", zap.String("id", created.ID), zap.String("name", created.Name))

	result := newUserGroupFromSlack(created)
	return NewStructuredResult(result, SlackResultMeta("", false, ""), "Created user group "+result.ID), nil
}

func (h *UsergroupsHandler) UsergroupsUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	usergroupID := request.GetString("usergroup_id", "")
	if usergroupID == "" {
		return nil, invalidArguments("usergroup_id is required")
	}

	name := request.GetString("name", "")
	handle := request.GetString("handle", "")
	description := request.GetString("description", "")
	channelsStr := request.GetString("channels", "")

	var options []slack.UpdateUserGroupsOption

	if name != "" {
		options = append(options, slack.UpdateUserGroupsOptionName(name))
	}
	if handle != "" {
		options = append(options, slack.UpdateUserGroupsOptionHandle(handle))
	}
	if description != "" {
		options = append(options, slack.UpdateUserGroupsOptionDescription(&description))
	}
	if channelsStr != "" {
		channels := parseCommaSeparatedList(channelsStr)
		options = append(options, slack.UpdateUserGroupsOptionChannels(channels))
	}

	if len(options) == 0 {
		return nil, invalidArguments("at least one update field (name, handle, description, or channels) is required")
	}

	updated, err := h.api.UpdateUserGroupContext(ctx, usergroupID, options...)
	if err != nil {
		h.logger.Debug("UpdateUserGroupContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Updated user group", zap.String("id", updated.ID), zap.String("name", updated.Name))

	result := newUserGroupFromSlack(updated)
	return NewStructuredResult(result, SlackResultMeta("", false, ""), "Updated user group "+result.ID), nil
}

func (h *UsergroupsHandler) UsergroupsUsersUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	usergroupID := request.GetString("usergroup_id", "")
	if usergroupID == "" {
		return nil, invalidArguments("usergroup_id is required")
	}

	usersStr := request.GetString("users", "")
	if usersStr == "" {
		return nil, invalidArguments("users is required")
	}

	updated, err := h.api.UpdateUserGroupMembersContext(ctx, usergroupID, usersStr)
	if err != nil {
		h.logger.Debug("UpdateUserGroupMembersContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Updated user group members",
		zap.String("id", updated.ID),
		zap.String("name", updated.Name),
		zap.Int("user_count", updated.UserCount),
	)

	result := newUserGroupFromSlack(updated)

	return NewStructuredResult(result, SlackResultMeta("", false, ""), "Updated user group members for "+result.ID), nil
}

func (h *UsergroupsHandler) UsergroupsMineHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	currentUserID, err := h.currentUserID()
	if err != nil {
		return nil, err
	}
	return h.handleListMyGroups(ctx, currentUserID)
}

func (h *UsergroupsHandler) UsergroupsJoinHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	currentUserID, err := h.currentUserID()
	if err != nil {
		return nil, err
	}
	return h.handleMyGroupMembership(ctx, currentUserID, request.GetString("usergroup_id", ""), membershipJoin)
}

func (h *UsergroupsHandler) UsergroupsLeaveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	currentUserID, err := h.currentUserID()
	if err != nil {
		return nil, err
	}
	return h.handleMyGroupMembership(ctx, currentUserID, request.GetString("usergroup_id", ""), membershipLeave)
}

func (h *UsergroupsHandler) currentUserID() (string, error) {
	if h.cachedUserID != nil {
		if id := h.cachedUserID(); id != "" {
			return id, nil
		}
	}
	authResp, err := h.api.AuthTest()
	if err != nil {
		h.logger.Debug("AuthTest failed", zap.Error(err))
		return "", err
	}
	if authResp.UserID == "" {
		return "", errors.New("Slack auth response has no user ID")
	}
	return authResp.UserID, nil
}

func (h *UsergroupsHandler) handleMyGroupMembership(ctx context.Context, currentUserID, usergroupID string, action membershipAction) (*mcp.CallToolResult, error) {

	if usergroupID == "" {
		return nil, invalidArguments("usergroup_id is required")
	}

	members, err := h.api.GetUserGroupMembersContext(ctx, usergroupID)
	if err != nil {
		h.logger.Debug("GetUserGroupMembersContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Current group members", zap.Int("count", len(members)), zap.Strings("members", members))

	memberIndex := slices.Index(members, currentUserID)
	isMember := memberIndex >= 0

	var newMembers []string
	var resultMessage string

	switch action {
	case membershipJoin:
		if isMember {
			data := UsergroupMeActionResult{Message: "You are already a member of this user group.", GroupID: usergroupID}
			return NewStructuredResult(data, SlackResultMeta("", false, ""), data.Message), nil
		}
		newMembers = append(members, currentUserID)
		resultMessage = "Successfully joined the user group."
	default: // membershipLeave
		if !isMember {
			data := UsergroupMeActionResult{Message: "You are not a member of this user group.", GroupID: usergroupID}
			return NewStructuredResult(data, SlackResultMeta("", false, ""), data.Message), nil
		}
		newMembers = append([]string(nil), members[:memberIndex]...)
		newMembers = append(newMembers, members[memberIndex+1:]...)
		resultMessage = "Successfully left the user group."
	}

	updated, err := h.api.UpdateUserGroupMembersContext(ctx, usergroupID, strings.Join(newMembers, ","))
	if err != nil {
		h.logger.Debug("UpdateUserGroupMembersContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Updated user group members",
		zap.String("id", updated.ID),
		zap.String("name", updated.Name),
		zap.Int("new_user_count", updated.UserCount),
	)

	data := UsergroupMeActionResult{
		Message:   resultMessage,
		GroupID:   updated.ID,
		GroupName: updated.Name,
		UserCount: updated.UserCount,
	}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), data.Message), nil
}

func (h *UsergroupsHandler) handleListMyGroups(ctx context.Context, currentUserID string) (*mcp.CallToolResult, error) {
	options := []slack.GetUserGroupsOption{
		slack.GetUserGroupsOptionIncludeUsers(true),
		slack.GetUserGroupsOptionIncludeCount(true),
		slack.GetUserGroupsOptionIncludeDisabled(false),
	}

	groups, err := h.api.GetUserGroupsContext(ctx, options...)
	if err != nil {
		h.logger.Debug("GetUserGroupsContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Fetched user groups for filtering", zap.Int("count", len(groups)))

	userGroupList := make([]UserGroup, 0)
	for _, g := range groups {
		isMember := false
		for _, uid := range g.Users {
			if uid == currentUserID {
				isMember = true
				break
			}
		}
		if !isMember {
			continue
		}

		userGroupList = append(userGroupList, newUserGroupFromSlack(g))
	}

	h.logger.Debug("Filtered to my groups", zap.Int("count", len(userGroupList)))

	return csvResult("", SlackResultMeta("", false, ""), &userGroupList)
}

func parseCommaSeparatedList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// Tools lists the MCP tools this handler serves.
func (h *UsergroupsHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.UsergroupsList, Run: h.UsergroupsListHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("List all user groups (subteams) in the workspace. User groups are mention handles like @engineering that notify all members. Use this to find a group's ID before joining or updating it. Returns CSV with columns ID, Name, Handle, Description, UserCount, IsExternal, DateCreate, DateUpdate, Users (semicolon-separated user IDs, filled when include_users=true)."),
			mcp.WithBoolean("include_users",
				mcp.Description("Include semicolon-separated user IDs in the users CSV column. Default is false."),
				mcp.DefaultBool(false),
			),
			mcp.WithBoolean("include_count",
				mcp.Description("Include user count for each group. Default is true."),
				mcp.DefaultBool(true),
			),
			mcp.WithBoolean("include_disabled",
				mcp.Description("Include disabled/archived groups. Default is false."),
				mcp.DefaultBool(false),
			),
		}},
		{Name: capability.UsergroupsMine, Run: h.UsergroupsMineHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("List the user groups the authenticated Slack user belongs to."),
		}},
		{Name: capability.UsergroupsJoin, Run: h.UsergroupsJoinHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Add the authenticated Slack user to one user group."),
			mcp.WithString("usergroup_id", mcp.Required(), mcp.Description("User group ID beginning with S.")),
		}},
		{Name: capability.UsergroupsLeave, Run: h.UsergroupsLeaveHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Remove the authenticated Slack user from one user group."),
			mcp.WithString("usergroup_id", mcp.Required(), mcp.Description("User group ID beginning with S.")),
		}},
		{Name: capability.UsergroupsCreate, Run: h.UsergroupsCreateHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Create a new user group (mention group) in the Slack workspace. After creation, use usergroups_users_update to add members, or users can join themselves with usergroups_join. The handle becomes the @mention (e.g., handle='engineering' creates @engineering)."),
			mcp.WithString("name",
				mcp.Required(),
				mcp.Description("Display name of the user group (e.g., 'Engineering Team', 'Design Squad')."),
			),
			mcp.WithString("handle",
				mcp.Description("The @mention handle without the @ symbol (e.g., 'engineering' for @engineering). Keep it short and lowercase. If omitted, Slack auto-generates one from the name."),
			),
			mcp.WithString("description",
				mcp.Description("Purpose or description shown in group details (e.g., 'Backend and frontend engineers')."),
			),
			mcp.WithString("channels",
				mcp.Description("Comma-separated channel IDs where this group is commonly mentioned. Members get suggestions to join these channels."),
			),
		}},
		{Name: capability.UsergroupsUpdate, Run: h.UsergroupsUpdateHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Update a user group's metadata: name, handle (@mention), description, or default channels. Does NOT change members, use usergroups_users_update for that. At least one field must be provided."),
			mcp.WithString("usergroup_id",
				mcp.Required(),
				mcp.Description("ID of the user group to update (starts with 'S', e.g., 'S0123456789'). Get IDs from usergroups_list."),
			),
			mcp.WithString("name",
				mcp.Description("New display name for the group."),
			),
			mcp.WithString("handle",
				mcp.Description("New @mention handle (without @). Changing this changes how users mention the group."),
			),
			mcp.WithString("description",
				mcp.Description("New description for the group."),
			),
			mcp.WithString("channels",
				mcp.Description("New default channel IDs (comma-separated). Replaces existing default channels."),
			),
		}},
		{Name: capability.UsergroupsUsersUpdate, Run: h.UsergroupsUsersUpdateHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Replace all members of a user group with a new list. WARNING: any user not in the 'users' parameter is removed. To add or remove only yourself, use usergroups_join or usergroups_leave. To add one user without removing others, first get current members from usergroups_list with include_users=true, then call this with the combined list."),
			mcp.WithString("usergroup_id",
				mcp.Required(),
				mcp.Description("ID of the user group (starts with 'S', e.g., 'S0123456789'). Get IDs from usergroups_list."),
			),
			mcp.WithString("users",
				mcp.Required(),
				mcp.Description("Comma-separated user IDs forming the COMPLETE new member list (e.g., 'U0123456789,U9876543210'). Current members not in this list are removed."),
			),
		}},
	}
}
