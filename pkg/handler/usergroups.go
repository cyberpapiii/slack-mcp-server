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
	Users       string `csv:"users,omitempty" json:"users,omitempty" jsonschema_description:"Semicolon-separated user IDs when include_users=true"`
}

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

func newUserGroupFromSlack(g slack.UserGroup) UserGroup {
	ug := UserGroup{
		ID:          g.ID,
		Name:        g.Name,
		Handle:      g.Handle,
		Description: g.Description,
		UserCount:   g.UserCount,
		IsExternal:  g.IsExternal,
		DateCreate:  formatJSONTime(g.DateCreate),
		DateUpdate:  formatJSONTime(g.DateUpdate),
	}
	if len(g.Users) > 0 {
		// Semicolon keeps CSV cells unambiguous (IDs never contain ';').
		ug.Users = strings.Join(g.Users, ";")
	}
	return ug
}

func NewUsergroupsHandler(apiProvider *provider.ApiProvider, logger *zap.Logger) *UsergroupsHandler {
	return &UsergroupsHandler{
		apiProvider: apiProvider,
		logger:      logger,
	}
}

// No IsReady check: usergroup handlers call the Slack API directly and do not
// depend on the users/channels cache, so cache readiness is not required.
func (h *UsergroupsHandler) UsergroupsListHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsListHandler called", request)

	includeUsers := request.GetBool("include_users", false)
	includeCount := request.GetBool("include_count", true)
	includeDisabled := request.GetBool("include_disabled", false)

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

	return NewStructuredResult(UsergroupPageData{Usergroups: userGroupList}, SlackResultMeta("", false, ""), string(csvBytes)), nil
}

func (h *UsergroupsHandler) UsergroupsCreateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsCreateHandler called", request)

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

func (h *UsergroupsHandler) UsergroupsUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsUpdateHandler called", request)

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

func (h *UsergroupsHandler) UsergroupsUsersUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsUsersUpdateHandler called", request)

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

func (h *UsergroupsHandler) UsergroupsMeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsMeHandler called", request)

	action := request.GetString("action", "")
	if action != "list" && action != "join" && action != "leave" {
		return nil, errors.New("action must be 'list', 'join', or 'leave'")
	}

	authResp, err := h.apiProvider.Slack().AuthTest()
	if err != nil {
		h.logger.Error("AuthTest failed", zap.Error(err))
		return nil, err
	}
	currentUserID := authResp.UserID
	h.logger.Debug("Current user ID", zap.String("user_id", currentUserID))

	if action == "list" {
		return h.handleListMyGroups(ctx, currentUserID)
	}
	return h.handleMyGroupMembership(ctx, currentUserID, request.GetString("usergroup_id", ""), action)
}

func (h *UsergroupsHandler) UsergroupsMineHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsMineHandler called", request)
	currentUserID, err := h.currentUserID()
	if err != nil {
		return nil, err
	}
	return h.handleListMyGroups(ctx, currentUserID)
}

func (h *UsergroupsHandler) UsergroupsJoinHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsJoinHandler called", request)
	if !requireToolEnabled("SLACK_MCP_USERGROUPS_WRITE_TOOL", "usergroups_join") {
		return nil, errors.New("usergroups_join is disabled")
	}
	currentUserID, err := h.currentUserID()
	if err != nil {
		return nil, err
	}
	return h.handleMyGroupMembership(ctx, currentUserID, request.GetString("usergroup_id", ""), "join")
}

func (h *UsergroupsHandler) UsergroupsLeaveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsLeaveHandler called", request)
	if !requireToolEnabled("SLACK_MCP_USERGROUPS_WRITE_TOOL", "usergroups_leave") {
		return nil, errors.New("usergroups_leave is disabled")
	}
	currentUserID, err := h.currentUserID()
	if err != nil {
		return nil, err
	}
	return h.handleMyGroupMembership(ctx, currentUserID, request.GetString("usergroup_id", ""), "leave")
}

func (h *UsergroupsHandler) currentUserID() (string, error) {
	authResp, err := h.apiProvider.Slack().AuthTest()
	if err != nil {
		h.logger.Error("AuthTest failed", zap.Error(err))
		return "", err
	}
	if authResp.UserID == "" {
		return "", errors.New("Slack auth response has no user ID")
	}
	return authResp.UserID, nil
}

func (h *UsergroupsHandler) handleMyGroupMembership(ctx context.Context, currentUserID, usergroupID, action string) (*mcp.CallToolResult, error) {

	if usergroupID == "" {
		return nil, errors.New("usergroup_id is required")
	}

	members, err := h.apiProvider.Slack().GetUserGroupMembersContext(ctx, usergroupID)
	if err != nil {
		h.logger.Error("GetUserGroupMembersContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Current group members", zap.Int("count", len(members)), zap.Strings("members", members))

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
			data := UsergroupMeActionResult{Message: "You are already a member of this user group.", GroupID: usergroupID}
			return NewStructuredResult(data, SlackResultMeta("", false, ""), data.Message), nil
		}
		newMembers = append(members, currentUserID)
		resultMessage = "Successfully joined the user group."
	} else { // leave
		if !isMember {
			data := UsergroupMeActionResult{Message: "You are not a member of this user group.", GroupID: usergroupID}
			return NewStructuredResult(data, SlackResultMeta("", false, ""), data.Message), nil
		}
		newMembers = append(members[:memberIndex], members[memberIndex+1:]...)
		resultMessage = "Successfully left the user group."
	}

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

	return NewStructuredResult(UsergroupPageData{Usergroups: userGroupList}, SlackResultMeta("", false, ""), string(csvBytes)), nil
}

func formatJSONTime(jt slack.JSONTime) string {
	if int64(jt) == 0 {
		return ""
	}
	t := time.Unix(int64(jt), 0)
	return t.Format(time.RFC3339)
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
