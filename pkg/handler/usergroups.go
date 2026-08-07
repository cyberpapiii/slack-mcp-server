package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type UserGroup struct {
	ID          string `csv:"id" json:"id" jsonschema_description:"User group ID"`
	Name        string `csv:"name" json:"name" jsonschema_description:"Group display name"`
	Handle      string `csv:"handle" json:"handle" jsonschema_description:"@mention handle"`
	Description string `csv:"description" json:"description" jsonschema_description:"Group description"`
	UserCount   int    `csv:"user_count" json:"user_count" jsonschema_description:"Number of members"`
	IsExternal  bool   `csv:"is_external" json:"is_external" jsonschema_description:"Whether group is external"`
	DateCreate  string `csv:"date_create" json:"date_create,omitempty" jsonschema_description:"Creation timestamp"`
	DateUpdate  string `csv:"date_update" json:"date_update,omitempty" jsonschema_description:"Last update timestamp"`
	Users       string `csv:"-" json:"users,omitempty" jsonschema_description:"Comma-separated user IDs"`
}

// UsergroupListResult is the structured output for list handlers.
type UsergroupListResult struct {
	Usergroups []UserGroup `json:"usergroups" jsonschema_description:"List of user groups"`
}

// UsergroupMeActionResult is the structured output for join/leave actions.
type UsergroupMeActionResult struct {
	Message   string `json:"message" jsonschema_description:"Result message"`
	GroupID   string `json:"group_id" jsonschema_description:"User group ID"`
	GroupName string `json:"group_name,omitempty" jsonschema_description:"User group name"`
	UserCount int    `json:"user_count,omitempty" jsonschema_description:"Number of members after action"`
}

type UsergroupsHandler struct {
	apiProvider *provider.ApiProvider
	logger      *zap.Logger
}

// newUserGroupFromSlack converts a slack.UserGroup to our output type.
func newUserGroupFromSlack(g slack.UserGroup) UserGroup {
	return UserGroup{
		ID:          g.ID,
		Name:        g.Name,
		Handle:      g.Handle,
		Description: g.Description,
		UserCount:   g.UserCount,
		IsExternal:  g.IsExternal,
		DateCreate:  formatJSONTime(g.DateCreate),
		DateUpdate:  formatJSONTime(g.DateUpdate),
	}
}

func NewUsergroupsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger) *UsergroupsHandler {
	return &UsergroupsHandler{
		apiProvider: apiProvider,
		logger:      logger,
	}
}

// UsergroupsListHandler lists all user groups in the workspace.
// No IsReady check: usergroup handlers call the Slack API directly and do not
// depend on the users/channels cache, so cache readiness is not required.
func (h *UsergroupsHandler) UsergroupsListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("UsergroupsListHandler called", zap.Any("params", request.Params))

	includeUsers := request.GetBool("include_users", false)
	includeCount := request.GetBool("include_count", true)
	includeDisabled := request.GetBool("include_disabled", false)

	h.logger.Debug("Request parameters",
		zap.Bool("include_users", includeUsers),
		zap.Bool("include_count", includeCount),
		zap.Bool("include_disabled", includeDisabled),
	)

	options := []slack.GetUserGroupsOption{
		slack.GetUserGroupsOptionIncludeUsers(includeUsers),
		slack.GetUserGroupsOptionIncludeCount(includeCount),
		slack.GetUserGroupsOptionIncludeDisabled(includeDisabled),
	}

	groups, err := h.apiProvider.Slack().GetUserGroupsContext(ctx, options...)
	if err != nil {
		h.logger.Error("GetUserGroupsContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Fetched user groups", zap.Int("count", len(groups)))

	userGroupList := make([]UserGroup, 0, len(groups))
	for _, g := range groups {
		userGroupList = append(userGroupList, newUserGroupFromSlack(g))
	}

	csvBytes, err := gocsv.MarshalBytes(&userGroupList)
	if err != nil {
		h.logger.Error("Failed to marshal user groups to CSV", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultStructured(UsergroupListResult{Usergroups: userGroupList}, string(csvBytes)), nil
}

// UsergroupsCreateHandler creates a new user group
func (h *UsergroupsHandler) UsergroupsCreateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("UsergroupsCreateHandler called", zap.Any("params", request.Params))

	if !requireToolEnabled("SLACK_MCP_USERGROUPS_WRITE_TOOL", "usergroups_create") {
		h.logger.Error("Usergroups write tool disabled by default")
		return nil, errors.New(
			"by default, the usergroups_create tool is disabled to guard Slack workspaces against accidental workspace-visible mutations. " +
				"To enable it, set the SLACK_MCP_USERGROUPS_WRITE_TOOL environment variable to true or 1, " +
				"or add 'usergroups_create' to SLACK_MCP_ENABLED_TOOLS",
		)
	}

	name := request.GetString("name", "")
	if name == "" {
		return nil, errors.New("name is required")
	}

	handle := request.GetString("handle", "")
	description := request.GetString("description", "")
	channelsStr := request.GetString("channels", "")

	h.logger.Debug("Request parameters",
		zap.String("name", name),
		zap.String("handle", handle),
		zap.String("description", description),
		zap.String("channels", channelsStr),
	)

	userGroup := slack.UserGroup{
		Name:        name,
		Handle:      handle,
		Description: description,
	}

	if channelsStr != "" {
		channels := parseCommaSeparatedList(channelsStr)
		userGroup.Prefs.Channels = channels
	}

	created, err := h.apiProvider.Slack().CreateUserGroupContext(ctx, userGroup)
	if err != nil {
		h.logger.Error("CreateUserGroupContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Created user group", zap.String("id", created.ID), zap.String("name", created.Name))

	return mcp.NewToolResultStructuredOnly(newUserGroupFromSlack(created)), nil
}

// UsergroupsUpdateHandler updates an existing user group's metadata
func (h *UsergroupsHandler) UsergroupsUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("UsergroupsUpdateHandler called", zap.Any("params", request.Params))

	if !requireToolEnabled("SLACK_MCP_USERGROUPS_WRITE_TOOL", "usergroups_update") {
		h.logger.Error("Usergroups write tool disabled by default")
		return nil, errors.New(
			"by default, the usergroups_update tool is disabled to guard Slack workspaces against accidental workspace-visible mutations. " +
				"To enable it, set the SLACK_MCP_USERGROUPS_WRITE_TOOL environment variable to true or 1, " +
				"or add 'usergroups_update' to SLACK_MCP_ENABLED_TOOLS",
		)
	}

	usergroupID := request.GetString("usergroup_id", "")
	if usergroupID == "" {
		return nil, errors.New("usergroup_id is required")
	}

	name := request.GetString("name", "")
	handle := request.GetString("handle", "")
	description := request.GetString("description", "")
	channelsStr := request.GetString("channels", "")

	h.logger.Debug("Request parameters",
		zap.String("usergroup_id", usergroupID),
		zap.String("name", name),
		zap.String("handle", handle),
		zap.String("description", description),
		zap.String("channels", channelsStr),
	)

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
		return nil, errors.New("at least one update field (name, handle, description, or channels) is required")
	}

	updated, err := h.apiProvider.Slack().UpdateUserGroupContext(ctx, usergroupID, options...)
	if err != nil {
		h.logger.Error("UpdateUserGroupContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Updated user group", zap.String("id", updated.ID), zap.String("name", updated.Name))

	return mcp.NewToolResultStructuredOnly(newUserGroupFromSlack(updated)), nil
}

// UsergroupsUsersUpdateHandler updates the members of a user group
func (h *UsergroupsHandler) UsergroupsUsersUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("UsergroupsUsersUpdateHandler called", zap.Any("params", request.Params))

	if !requireToolEnabled("SLACK_MCP_USERGROUPS_WRITE_TOOL", "usergroups_users_update") {
		h.logger.Error("Usergroups write tool disabled by default")
		return nil, errors.New(
			"by default, the usergroups_users_update tool is disabled to guard Slack workspaces against accidental workspace-visible mutations. " +
				"To enable it, set the SLACK_MCP_USERGROUPS_WRITE_TOOL environment variable to true or 1, " +
				"or add 'usergroups_users_update' to SLACK_MCP_ENABLED_TOOLS",
		)
	}

	usergroupID := request.GetString("usergroup_id", "")
	if usergroupID == "" {
		return nil, errors.New("usergroup_id is required")
	}

	usersStr := request.GetString("users", "")
	if usersStr == "" {
		return nil, errors.New("users is required")
	}

	h.logger.Debug("Request parameters",
		zap.String("usergroup_id", usergroupID),
		zap.String("users", usersStr),
	)

	// UpdateUserGroupMembersContext expects a comma-separated string of user IDs
	updated, err := h.apiProvider.Slack().UpdateUserGroupMembersContext(ctx, usergroupID, usersStr)
	if err != nil {
		h.logger.Error("UpdateUserGroupMembersContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Updated user group members",
		zap.String("id", updated.ID),
		zap.String("name", updated.Name),
		zap.Int("user_count", updated.UserCount),
	)

	result := newUserGroupFromSlack(updated)
	result.Users = strings.Join(updated.Users, ",")

	return mcp.NewToolResultStructuredOnly(result), nil
}

// UsergroupsMeHandler allows the current user to list their groups, join or leave a user group
func (h *UsergroupsHandler) UsergroupsMeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	h.logger.Debug("UsergroupsMeHandler called", zap.Any("params", request.Params))

	action := request.GetString("action", "")
	if action != "list" && action != "join" && action != "leave" {
		return nil, errors.New("action must be 'list', 'join', or 'leave'")
	}

	// Get current user ID
	authResp, err := h.apiProvider.Slack().AuthTest()
	if err != nil {
		h.logger.Error("AuthTest failed", zap.Error(err))
		return nil, err
	}
	currentUserID := authResp.UserID
	h.logger.Debug("Current user ID", zap.String("user_id", currentUserID))

	// Handle list action
	if action == "list" {
		return h.handleListMyGroups(ctx, currentUserID)
	}

	// For join/leave, usergroup_id is required
	usergroupID := request.GetString("usergroup_id", "")
	if usergroupID == "" {
		return nil, errors.New("usergroup_id is required for join/leave actions")
	}

	h.logger.Debug("Request parameters",
		zap.String("usergroup_id", usergroupID),
		zap.String("action", action),
	)

	// Get current members of the group
	members, err := h.apiProvider.Slack().GetUserGroupMembersContext(ctx, usergroupID)
	if err != nil {
		h.logger.Error("GetUserGroupMembersContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Current group members", zap.Int("count", len(members)), zap.Strings("members", members))

	// Check current membership
	isMember := false
	memberIndex := -1
	for i, uid := range members {
		if uid == currentUserID {
			isMember = true
			memberIndex = i
			break
		}
	}

	var newMembers []string
	var resultMessage string

	if action == "join" {
		if isMember {
			return mcp.NewToolResultStructuredOnly(
				UsergroupMeActionResult{Message: "You are already a member of this user group.", GroupID: usergroupID},
			), nil
		}
		newMembers = append(members, currentUserID)
		resultMessage = "Successfully joined the user group."
	} else { // leave
		if !isMember {
			return mcp.NewToolResultStructuredOnly(
				UsergroupMeActionResult{Message: "You are not a member of this user group.", GroupID: usergroupID},
			), nil
		}
		// Remove current user from members
		newMembers = append(members[:memberIndex], members[memberIndex+1:]...)
		resultMessage = "Successfully left the user group."
	}

	// Update the group members
	membersStr := strings.Join(newMembers, ",")
	updated, err := h.apiProvider.Slack().UpdateUserGroupMembersContext(ctx, usergroupID, membersStr)
	if err != nil {
		h.logger.Error("UpdateUserGroupMembersContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Updated user group members",
		zap.String("id", updated.ID),
		zap.String("name", updated.Name),
		zap.Int("new_user_count", updated.UserCount),
	)

	return mcp.NewToolResultStructuredOnly(UsergroupMeActionResult{
		Message:   resultMessage,
		GroupID:   updated.ID,
		GroupName: updated.Name,
		UserCount: updated.UserCount,
	}), nil
}

// handleListMyGroups returns groups where the current user is a member
func (h *UsergroupsHandler) handleListMyGroups(ctx context.Context, currentUserID string) (*mcp.CallToolResult, error) {
	options := []slack.GetUserGroupsOption{
		slack.GetUserGroupsOptionIncludeUsers(true),
		slack.GetUserGroupsOptionIncludeCount(true),
		slack.GetUserGroupsOptionIncludeDisabled(false),
	}

	groups, err := h.apiProvider.Slack().GetUserGroupsContext(ctx, options...)
	if err != nil {
		h.logger.Error("GetUserGroupsContext failed", zap.Error(err))
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

	csvBytes, err := gocsv.MarshalBytes(&userGroupList)
	if err != nil {
		h.logger.Error("Failed to marshal user groups to CSV", zap.Error(err))
		return nil, err
	}

	return mcp.NewToolResultStructured(UsergroupListResult{Usergroups: userGroupList}, string(csvBytes)), nil
}

// formatJSONTime converts slack.JSONTime (Unix timestamp) to a readable string
func formatJSONTime(jt slack.JSONTime) string {
	if int64(jt) == 0 {
		return ""
	}
	t := time.Unix(int64(jt), 0)
	return t.Format(time.RFC3339)
}

// parseCommaSeparatedList splits a comma-separated string into a slice of trimmed strings
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
