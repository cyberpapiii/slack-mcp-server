package handler

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
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
		api:    apiProvider.WebAPI(),
		logger: logger,
	}
}

func newUsergroupsHandlerWithAPI(api UsergroupsAPI, logger *zap.Logger) *UsergroupsHandler {
	return &UsergroupsHandler{api: api, logger: logger}
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

	groups, err := h.api.GetUserGroupsContext(ctx, options...)
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

	return NewCSVResult("", SlackResultMeta("", false, ""), string(csvBytes)), nil
}

func (h *UsergroupsHandler) UsergroupsCreateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsCreateHandler called", request)

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

	created, err := h.api.CreateUserGroupContext(ctx, userGroup)
	if err != nil {
		h.logger.Error("CreateUserGroupContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Created user group", zap.String("id", created.ID), zap.String("name", created.Name))

	result := newUserGroupFromSlack(created)
	return NewStructuredResult(result, SlackResultMeta("", false, ""), "Created user group "+result.ID), nil
}

func (h *UsergroupsHandler) UsergroupsUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsUpdateHandler called", request)

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

	updated, err := h.api.UpdateUserGroupContext(ctx, usergroupID, options...)
	if err != nil {
		h.logger.Error("UpdateUserGroupContext failed", zap.Error(err))
		return nil, err
	}

	h.logger.Debug("Updated user group", zap.String("id", updated.ID), zap.String("name", updated.Name))

	result := newUserGroupFromSlack(updated)
	return NewStructuredResult(result, SlackResultMeta("", false, ""), "Updated user group "+result.ID), nil
}

func (h *UsergroupsHandler) UsergroupsUsersUpdateHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsUsersUpdateHandler called", request)

	usergroupID := request.GetString("usergroup_id", "")
	if usergroupID == "" {
		return nil, errors.New("usergroup_id is required")
	}

	usersStr := request.GetString("users", "")
	if usersStr == "" {
		return nil, errors.New("users is required")
	}

	updated, err := h.api.UpdateUserGroupMembersContext(ctx, usergroupID, usersStr)
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

	return NewStructuredResult(result, SlackResultMeta("", false, ""), "Updated user group members for "+result.ID), nil
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
	currentUserID, err := h.currentUserID()
	if err != nil {
		return nil, err
	}
	return h.handleMyGroupMembership(ctx, currentUserID, request.GetString("usergroup_id", ""), membershipJoin)
}

func (h *UsergroupsHandler) UsergroupsLeaveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "UsergroupsLeaveHandler called", request)
	currentUserID, err := h.currentUserID()
	if err != nil {
		return nil, err
	}
	return h.handleMyGroupMembership(ctx, currentUserID, request.GetString("usergroup_id", ""), membershipLeave)
}

func (h *UsergroupsHandler) currentUserID() (string, error) {
	authResp, err := h.api.AuthTest()
	if err != nil {
		h.logger.Error("AuthTest failed", zap.Error(err))
		return "", err
	}
	if authResp.UserID == "" {
		return "", errors.New("Slack auth response has no user ID")
	}
	return authResp.UserID, nil
}

func (h *UsergroupsHandler) handleMyGroupMembership(ctx context.Context, currentUserID, usergroupID string, action membershipAction) (*mcp.CallToolResult, error) {

	if usergroupID == "" {
		return nil, errors.New("usergroup_id is required")
	}

	members, err := h.api.GetUserGroupMembersContext(ctx, usergroupID)
	if err != nil {
		h.logger.Error("GetUserGroupMembersContext failed", zap.Error(err))
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
	case membershipLeave:
		if !isMember {
			data := UsergroupMeActionResult{Message: "You are not a member of this user group.", GroupID: usergroupID}
			return NewStructuredResult(data, SlackResultMeta("", false, ""), data.Message), nil
		}
		newMembers = append([]string(nil), members[:memberIndex]...)
		newMembers = append(newMembers, members[memberIndex+1:]...)
		resultMessage = "Successfully left the user group."
	default:
		return nil, errors.New("invalid user group membership action")
	}

	membersStr := strings.Join(newMembers, ",")
	currentMembers, err := h.api.GetUserGroupMembersContext(ctx, usergroupID)
	if err != nil {
		return nil, err
	}
	if !sameMemberSet(members, currentMembers) {
		return nil, &ToolError{Code: "membership_conflict", Message: "user group membership changed; read current membership before trying again"}
	}
	updated, err := h.api.UpdateUserGroupMembersContext(ctx, usergroupID, membersStr)
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

	groups, err := h.api.GetUserGroupsContext(ctx, options...)
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

	return NewCSVResult("", SlackResultMeta("", false, ""), string(csvBytes)), nil
}

func sameMemberSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b)
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
