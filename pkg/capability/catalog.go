// Package capability defines canonical Slack intents independently from MCP
// tool registration. The catalog is the source for local presets and host-side
// inventory verification.
package capability

import (
	"fmt"
	"sort"
	"strings"
)

const CatalogVersion = "2026-08-12.1"

type Owner string

const (
	OwnerOfficial     Owner = "official"
	OwnerLocal        Owner = "local"
	OwnerLocalBrowser Owner = "local-browser"
)

type AuthMode string

const (
	AuthOAuth   AuthMode = "oauth"
	AuthBrowser AuthMode = "browser-session"
)

type ConfirmationTier string

const (
	ConfirmationNone    ConfirmationTier = "none"
	ConfirmationChange  ConfirmationTier = "confirm-change"
	ConfirmationPreview ConfirmationTier = "preview-confirm"
)

type MigrationState string

const (
	MigrationActive  MigrationState = "active"
	MigrationPlanned MigrationState = "planned"
	MigrationLegacy  MigrationState = "legacy-only"
)

// ToolBehavior contains MCP-neutral behavior hints. Confirmation remains on
// Entry because user approval policy and MCP destructive semantics differ.
type ToolBehavior struct {
	Title       string `json:"title"`
	ReadOnly    bool   `json:"read_only"`
	Destructive bool   `json:"destructive"`
	Idempotent  bool   `json:"idempotent"`
	OpenWorld   bool   `json:"open_world"`
}

type Entry struct {
	ID              string           `json:"id"`
	Owner           Owner            `json:"owner"`
	LocalTool       string           `json:"local_tool,omitempty"`
	OfficialAction  string           `json:"official_action,omitempty"`
	Auth            AuthMode         `json:"auth"`
	RequiredScopes  []string         `json:"required_oauth_scopes,omitempty"`
	Confirmation    ConfirmationTier `json:"confirmation"`
	ResultType      string           `json:"result_type"`
	Migration       MigrationState   `json:"migration"`
	PlanDependent   bool             `json:"plan_dependent,omitempty"`
	BrowserOptional bool             `json:"browser_optional,omitempty"`
}

func official(id, action, result string, confirmation ConfirmationTier, scopes ...string) Entry {
	return Entry{ID: id, Owner: OwnerOfficial, OfficialAction: action, Auth: AuthOAuth, RequiredScopes: scopes, Confirmation: confirmation, ResultType: result, Migration: MigrationActive}
}

func local(id, tool, result string, confirmation ConfirmationTier, auth AuthMode, migration MigrationState, scopes ...string) Entry {
	return Entry{ID: id, Owner: OwnerLocal, LocalTool: tool, Auth: auth, RequiredScopes: scopes, Confirmation: confirmation, ResultType: result, Migration: migration}
}

func legacy(id, tool, action, result string, confirmation ConfirmationTier, scopes ...string) Entry {
	return local(id, tool, result, confirmation, AuthOAuth, MigrationLegacy, scopes...)
}

var catalog = []Entry{
	legacy("message.history.read", "conversations_history", "slack_read_channel", "message_page", ConfirmationNone, "channels:history", "groups:history", "im:history", "mpim:history"),
	legacy("message.thread.read", "conversations_replies", "slack_read_thread", "message_page", ConfirmationNone, "channels:history", "groups:history", "im:history", "mpim:history"),
	local("message.exact.read", "conversations_get_message", "message", ConfirmationNone, AuthOAuth, MigrationActive, "channels:history", "groups:history", "im:history", "mpim:history"),
	legacy("message.send", "conversations_add_message", "slack_send_message", "message_mutation", ConfirmationChange, "chat:write"),
	local("draft.preview", "conversations_draft_message", "draft_preview", ConfirmationNone, AuthOAuth, MigrationLegacy),
	{ID: "draft.persisted.list", Owner: OwnerLocalBrowser, LocalTool: "drafts_list", Auth: AuthBrowser, Confirmation: ConfirmationNone, ResultType: "draft_page", Migration: MigrationActive, BrowserOptional: true},
	{ID: "draft.persisted.get", Owner: OwnerLocalBrowser, LocalTool: "drafts_get", Auth: AuthBrowser, Confirmation: ConfirmationNone, ResultType: "draft", Migration: MigrationActive, BrowserOptional: true},
	{ID: "draft.persisted.create", Owner: OwnerLocalBrowser, LocalTool: "drafts_create", Auth: AuthBrowser, Confirmation: ConfirmationChange, ResultType: "draft_mutation", Migration: MigrationActive, BrowserOptional: true},
	{ID: "draft.persisted.update", Owner: OwnerLocalBrowser, LocalTool: "drafts_update", Auth: AuthBrowser, Confirmation: ConfirmationChange, ResultType: "draft_mutation", Migration: MigrationActive, BrowserOptional: true},
	{ID: "draft.persisted.delete", Owner: OwnerLocalBrowser, LocalTool: "drafts_delete", Auth: AuthBrowser, Confirmation: ConfirmationPreview, ResultType: "draft_mutation", Migration: MigrationActive, BrowserOptional: true},
	legacy("message.search", "conversations_search_messages", "slack_search_public_and_private", "search_results", ConfirmationNone, "search:read"),
	{ID: "message.unreads.read", Owner: OwnerLocalBrowser, LocalTool: "conversations_unreads", Auth: AuthBrowser, Confirmation: ConfirmationNone, ResultType: "unread_page", Migration: MigrationActive, BrowserOptional: true},
	local("message.read_progress.mark", "conversations_mark", "read_progress", ConfirmationChange, AuthOAuth, MigrationActive, "channels:write", "groups:write", "im:write", "mpim:write"),
	legacy("conversation.open", "conversations_open", "slack_create_conversation", "conversation", ConfirmationChange, "im:write", "mpim:write"),
	legacy("conversation.join", "conversations_join", "slack_join_conversation", "conversation_membership", ConfirmationChange, "channels:write"),
	legacy("conversation.leave", "conversations_leave", "slack_leave_conversation", "conversation_membership", ConfirmationChange, "channels:write", "groups:write"),
	legacy("conversation.list", "channels_list", "slack_list_user_conversations", "conversation_page", ConfirmationNone, "channels:read", "groups:read", "im:read", "mpim:read"),
	legacy("conversation.starred.list", "channels_starred", "slack_list_starred_items", "starred_page", ConfirmationNone, "stars:read"),
	legacy("conversation.mine.list", "channels_me", "slack_list_user_conversations", "conversation_page", ConfirmationNone, "channels:read", "groups:read", "im:read", "mpim:read"),
	legacy("reaction.add", "reactions_add", "slack_add_reaction", "reaction_mutation", ConfirmationChange, "reactions:write"),
	local("reaction.remove", "reactions_remove", "reaction_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "reactions:write"),
	legacy("reaction.inspect", "reactions_get", "slack_get_reactions", "reaction_page", ConfirmationNone, "reactions:read"),
	legacy("file.download", "attachment_get_data", "slack_read_file", "file_content", ConfirmationNone, "files:read"),
	legacy("file.list", "files_list", "slack_search_public_and_private", "file_page", ConfirmationNone, "files:read"),
	legacy("user.search", "users_search", "slack_search_users", "user_page", ConfirmationNone, "users:read"),
	local("usergroup.list", "usergroups_list", "usergroup_page", ConfirmationNone, AuthOAuth, MigrationActive, "usergroups:read"),
	local("usergroup.mine.list", "usergroups_mine", "usergroup_page", ConfirmationNone, AuthOAuth, MigrationActive, "usergroups:read"),
	local("usergroup.mine.join", "usergroups_join", "usergroup_membership", ConfirmationChange, AuthOAuth, MigrationActive, "usergroups:read", "usergroups:write"),
	local("usergroup.mine.leave", "usergroups_leave", "usergroup_membership", ConfirmationChange, AuthOAuth, MigrationActive, "usergroups:read", "usergroups:write"),
	local("usergroup.create", "usergroups_create", "usergroup_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "usergroups:write"),
	local("usergroup.update", "usergroups_update", "usergroup_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "usergroups:write"),
	local("usergroup.members.replace", "usergroups_users_update", "usergroup_mutation", ConfirmationPreview, AuthOAuth, MigrationActive, "usergroups:write"),
	local("activity.unreads.read", "activity_unreads", "activity_page", ConfirmationNone, AuthBrowser, MigrationActive),
	local("activity.read_progress.mark", "activity_mark_read", "activity_mutation", ConfirmationChange, AuthBrowser, MigrationActive),
	local("later.list", "saved_list", "saved_page", ConfirmationNone, AuthBrowser, MigrationActive),
	local("later.update", "saved_update", "saved_mutation", ConfirmationChange, AuthBrowser, MigrationActive),
	local("later.completed.clear", "saved_clear_completed", "saved_mutation", ConfirmationPreview, AuthBrowser, MigrationActive),
	local("operations.diagnostics", "slack_auth_status", "diagnostics", ConfirmationNone, AuthOAuth, MigrationActive),

	local("file.upload", "files_upload", "file_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "files:write"),
	local("message.schedule", "messages_schedule", "scheduled_message", ConfirmationChange, AuthOAuth, MigrationActive, "chat:write"),
	local("message.edit", "messages_update", "message_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "chat:write"),
	local("message.delete", "messages_delete", "message_mutation", ConfirmationPreview, AuthOAuth, MigrationActive, "chat:write"),
	local("conversation.create", "channels_create", "conversation", ConfirmationChange, AuthOAuth, MigrationActive, "channels:write", "groups:write"),
	local("conversation.members.list", "channels_members", "member_page", ConfirmationNone, AuthOAuth, MigrationActive, "channels:read", "groups:read"),
	local("conversation.members.invite", "channels_invite", "conversation_membership", ConfirmationChange, AuthOAuth, MigrationActive, "channels:write", "channels:write.invites", "groups:write", "groups:write.invites"),
	local("emoji.search", "emoji_list", "emoji_page", ConfirmationNone, AuthOAuth, MigrationActive, "emoji:read"),
	local("profile.read", "users_get_profile", "user_profile", ConfirmationNone, AuthOAuth, MigrationActive, "users.profile:read"),
	local("profile.update", "users_set_profile", "user_profile", ConfirmationChange, AuthOAuth, MigrationActive, "users.profile:write"),
	local("status.update", "users_set_status", "user_profile", ConfirmationChange, AuthOAuth, MigrationActive, "users.profile:write"),
	local("canvas.create", "canvases_create", "canvas", ConfirmationChange, AuthOAuth, MigrationActive, "canvases:write"),
	local("canvas.read", "canvases_read", "canvas", ConfirmationNone, AuthOAuth, MigrationActive, "canvases:read"),
	local("canvas.update", "canvases_update", "canvas", ConfirmationChange, AuthOAuth, MigrationActive, "canvases:write"),
	local("search.semantic", "search_semantic", "semantic_search_page", ConfirmationNone, AuthOAuth, MigrationActive, "search:read.files", "search:read.public", "search:read.private", "search:read.im", "search:read.mpim", "search:read.users"),

	local("scheduled.list", "scheduled_messages_list", "scheduled_message_page", ConfirmationNone, AuthOAuth, MigrationActive),
	local("scheduled.cancel", "scheduled_message_cancel", "scheduled_message_mutation", ConfirmationPreview, AuthOAuth, MigrationActive, "chat:write"),
	local("channel.rename", "channels_rename", "channel_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "channels:write", "groups:write"),
	local("channel.topic.update", "channels_set_topic", "channel_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "channels:write", "groups:write"),
	local("channel.purpose.update", "channels_set_purpose", "channel_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "channels:write", "groups:write"),
	local("channel.archive", "channels_archive", "channel_mutation", ConfirmationPreview, AuthOAuth, MigrationActive, "channels:write", "groups:write"),
	local("list.create", "lists_create", "list_create", ConfirmationChange, AuthOAuth, MigrationActive, "lists:write"),
	local("list.update", "lists_update", "list_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "lists:write"),
	local("list.items.list", "lists_items_list", "list_items_page", ConfirmationNone, AuthOAuth, MigrationActive, "lists:read"),
	local("list.items.create", "lists_items_create", "list_item", ConfirmationChange, AuthOAuth, MigrationActive, "lists:write"),
	local("list.items.update", "lists_items_update", "list_item_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "lists:write"),
	local("list.item.delete", "lists_item_delete", "list_item_mutation", ConfirmationPreview, AuthOAuth, MigrationActive, "lists:read", "lists:write"),
	local("dnd.read", "dnd_get", "dnd_state", ConfirmationNone, AuthOAuth, MigrationActive, "dnd:read"),
	local("dnd.snooze.set", "dnd_set_snooze", "dnd_state", ConfirmationChange, AuthOAuth, MigrationActive, "dnd:write"),
	local("dnd.snooze.end", "dnd_end_snooze", "dnd_state", ConfirmationChange, AuthOAuth, MigrationActive, "dnd:write"),
}

var dailyPowerToolBehavior = map[string]ToolBehavior{
	"activity_unreads":          {Title: "Get Activity Unreads", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"conversations_get_message": {Title: "Get Single Message", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"conversations_unreads":     {Title: "Get Unread Messages", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"saved_list":                {Title: "List Saved Items", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"slack_auth_status":         {Title: "Auth & Cache Status", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"usergroups_list":           {Title: "List User Groups", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"usergroups_mine":           {Title: "List My User Groups", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"usergroups_join":           {Title: "Join User Group", Idempotent: true, OpenWorld: true},
	"usergroups_leave":          {Title: "Leave User Group", Destructive: true, Idempotent: true, OpenWorld: true},
	"scheduled_messages_list":   {Title: "List Scheduled Messages", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"scheduled_message_cancel":  {Title: "Cancel Scheduled Message", Destructive: true, OpenWorld: true},
	"channels_rename":           {Title: "Rename Channel", OpenWorld: true},
	"channels_set_topic":        {Title: "Set Channel Topic", OpenWorld: true},
	"channels_set_purpose":      {Title: "Set Channel Purpose", OpenWorld: true},
	"channels_archive":          {Title: "Archive Channel", Destructive: true, OpenWorld: true},
	"lists_create":              {Title: "Create Slack List", OpenWorld: true},
	"lists_update":              {Title: "Update Slack List", OpenWorld: true},
	"lists_items_list":          {Title: "List Slack List Items", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"lists_items_create":        {Title: "Create Slack List Item", OpenWorld: true},
	"lists_items_update":        {Title: "Update Slack List Items", OpenWorld: true},
	"lists_item_delete":         {Title: "Delete Slack List Item", Destructive: true, OpenWorld: true},
	"dnd_get":                   {Title: "Get Do Not Disturb", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"dnd_set_snooze":            {Title: "Set Do Not Disturb", OpenWorld: true},
	"dnd_end_snooze":            {Title: "End Do Not Disturb", OpenWorld: true},
	"conversations_mark":        {Title: "Mark Conversation Read", Idempotent: true, OpenWorld: true},
	"reactions_remove":          {Title: "Remove Reaction", Destructive: true, OpenWorld: true},
	"activity_mark_read":        {Title: "Mark Activity Read", Idempotent: true, OpenWorld: true},
	"saved_update":              {Title: "Update Saved Item", Idempotent: true, OpenWorld: true},
	"saved_clear_completed":     {Title: "Clear Completed Saved Items", Destructive: true, Idempotent: true, OpenWorld: true},
	"usergroups_create":         {Title: "Create User Group", OpenWorld: true},
	"usergroups_update":         {Title: "Update User Group", OpenWorld: true},
	"usergroups_users_update":   {Title: "Replace User Group Members", Destructive: true, Idempotent: true, OpenWorld: true},
	"files_upload":              {Title: "Upload File", Destructive: true, OpenWorld: true},
	"messages_schedule":         {Title: "Schedule Message", Destructive: true, OpenWorld: true},
	"messages_update":           {Title: "Update Message", Destructive: true, OpenWorld: true},
	"messages_delete":           {Title: "Delete Message", Destructive: true, OpenWorld: true},
	"channels_create":           {Title: "Create Channel", Destructive: true, OpenWorld: true},
	"channels_members":          {Title: "List Channel Members", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"channels_invite":           {Title: "Invite Channel Members", Destructive: true, OpenWorld: true},
	"emoji_list":                {Title: "List Custom Emoji", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"users_get_profile":         {Title: "Get User Profile", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"users_set_profile":         {Title: "Update User Profile", Destructive: true, OpenWorld: true},
	"users_set_status":          {Title: "Set User Status", Destructive: true, OpenWorld: true},
	"canvases_create":           {Title: "Create Canvas", Destructive: true, OpenWorld: true},
	"canvases_read":             {Title: "Read Canvas Sections", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"canvases_update":           {Title: "Update Canvas", Destructive: true, OpenWorld: true},
	"drafts_list":               {Title: "List Drafts", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"drafts_get":                {Title: "Get Draft", ReadOnly: true, Idempotent: true, OpenWorld: true},
	"drafts_create":             {Title: "Create Draft", Destructive: true, OpenWorld: true},
	"drafts_update":             {Title: "Update Draft", Destructive: true, OpenWorld: true},
	"drafts_delete":             {Title: "Delete Draft", Destructive: true, OpenWorld: true},
	"search_semantic":           {Title: "Search Slack Semantically", ReadOnly: true, Idempotent: true, OpenWorld: true},
}

func BehaviorForLocalTool(tool string) (ToolBehavior, bool) {
	behavior, ok := dailyPowerToolBehavior[tool]
	return behavior, ok
}

func EntryForLocalTool(tool string) (Entry, bool) {
	for _, entry := range catalog {
		if entry.LocalTool == tool && entry.Owner != OwnerOfficial && entry.Migration == MigrationActive {
			entry.RequiredScopes = append([]string(nil), entry.RequiredScopes...)
			return entry, true
		}
	}
	return Entry{}, false
}

func Entries() []Entry {
	result := make([]Entry, len(catalog))
	copy(result, catalog)
	for i := range result {
		result[i].RequiredScopes = append([]string(nil), result[i].RequiredScopes...)
	}
	return result
}

func DailyPowerLocalTools() []string {
	return localTools(func(e Entry) bool {
		_, hasBehavior := dailyPowerToolBehavior[e.LocalTool]
		return e.Owner != OwnerOfficial && e.Migration == MigrationActive && e.Confirmation == ConfirmationNone && hasBehavior
	})
}

func ActiveBrowserLocalTools() []string {
	return localTools(func(e Entry) bool {
		return e.Owner != OwnerOfficial && e.Migration == MigrationActive && e.Auth == AuthBrowser
	})
}

func LegacyFullLocalTools() []string {
	return localTools(func(e Entry) bool { return e.LocalTool != "" && e.Migration != MigrationPlanned })
}

func localTools(include func(Entry) bool) []string {
	seen := map[string]bool{}
	var tools []string
	for _, entry := range catalog {
		if include(entry) && entry.LocalTool != "" && !seen[entry.LocalTool] {
			seen[entry.LocalTool] = true
			tools = append(tools, entry.LocalTool)
		}
	}
	sort.Strings(tools)
	return tools
}

func OAuthScopesForTools(tools []string) []string {
	wanted := make(map[string]bool, len(tools))
	for _, tool := range tools {
		wanted[tool] = true
	}
	seen := map[string]bool{}
	var scopes []string
	for _, entry := range catalog {
		if !wanted[entry.LocalTool] {
			continue
		}
		for _, scope := range entry.RequiredScopes {
			if !seen[scope] {
				seen[scope] = true
				scopes = append(scopes, scope)
			}
		}
	}
	sort.Strings(scopes)
	return scopes
}

type Identity struct {
	TeamID       string `json:"team_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	EnterpriseID string `json:"enterprise_id,omitempty"`
	ActorType    string `json:"actor_type,omitempty"`
	TokenMode    string `json:"token_mode,omitempty"`
}

type ObservedTool struct {
	Name              string `json:"name"`
	InputSchemaObject bool   `json:"input_schema_object"`
	StructuredResult  bool   `json:"structured_result"`
	SemanticsVerified bool   `json:"semantics_verified"`
}

type InventorySnapshot struct {
	CapturedAt     string         `json:"captured_at,omitempty"`
	CatalogVersion string         `json:"catalog_version"`
	Identity       Identity       `json:"identity"`
	Tools          []ObservedTool `json:"tools"`
}

type VisibleTool struct {
	CapabilityID string `json:"capability_id"`
	Provider     Owner  `json:"provider"`
	Name         string `json:"name"`
}

type HostInventory struct {
	CapturedAt       string        `json:"captured_at,omitempty"`
	CatalogVersion   string        `json:"catalog_version"`
	OfficialIdentity Identity      `json:"official_identity"`
	LocalIdentity    Identity      `json:"local_identity"`
	Tools            []VisibleTool `json:"tools"`
}

type Issue struct {
	Code         string `json:"code"`
	CapabilityID string `json:"capability_id,omitempty"`
	Detail       string `json:"detail"`
}

type VerificationReport struct {
	Issues []Issue `json:"issues"`
}

func (r VerificationReport) OK() bool { return len(r.Issues) == 0 }

func (r VerificationReport) Has(code string) bool {
	for _, issue := range r.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func VerifyInventory(_ InventorySnapshot, host HostInventory) VerificationReport {
	var report VerificationReport
	add := func(code, id, detail string) {
		report.Issues = append(report.Issues, Issue{Code: code, CapabilityID: id, Detail: detail})
	}
	if host.CatalogVersion != "" && host.CatalogVersion != CatalogVersion {
		add("catalog_version_mismatch", "", fmt.Sprintf("host inventory uses %s, expected %s", host.CatalogVersion, CatalogVersion))
	}
	if !identityComplete(host.LocalIdentity) {
		add("identity_unverified", "", "local provider must identify one Slack workspace user")
	}
	visible := map[string][]VisibleTool{}
	for _, tool := range host.Tools {
		visible[tool.CapabilityID] = append(visible[tool.CapabilityID], tool)
		if excludedCapability(tool.CapabilityID) || excludedTool(tool.Name) {
			add("excluded_family", tool.CapabilityID, "administrative or live-work capability must remain outside daily-power")
		}
	}
	for _, entry := range catalog {
		if entry.Migration != MigrationActive {
			continue
		}
		owners := visible[entry.ID]
		if len(owners) == 0 {
			add("missing_capability", entry.ID, "canonical capability is absent from host inventory")
			continue
		}
		if len(owners) > 1 {
			add("duplicate_owner", entry.ID, "multiple tools are visible for one canonical capability")
			continue
		}
		if owners[0].Provider != entry.Owner {
			add("wrong_owner", entry.ID, fmt.Sprintf("visible owner %s, expected %s", owners[0].Provider, entry.Owner))
		}
	}
	return report
}

func identityComplete(identity Identity) bool {
	return identity.TeamID != "" && identity.UserID != ""
}

func excludedCapability(id string) bool {
	for _, prefix := range []string{"huddle.", "clip.", "slack_connect.admin", "workflow.", "workspace.admin"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

func excludedTool(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "huddle") || strings.Contains(lower, "clip") || strings.Contains(lower, "workflow") || strings.Contains(lower, "admin")
}
