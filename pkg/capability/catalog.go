// Package capability defines canonical Slack intents independently from MCP
// tool registration. The catalog is the source for local presets and host-side
// inventory verification.
package capability

import (
	"fmt"
	"sort"
	"strings"
)

const CatalogVersion = "2026-08-09.2"

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
	e := official(id, action, result, confirmation, scopes...)
	e.LocalTool = tool
	return e
}

var catalog = []Entry{
	legacy("message.history.read", "conversations_history", "slack_read_channel", "message_page", ConfirmationNone, "channels:history", "groups:history", "im:history", "mpim:history"),
	legacy("message.thread.read", "conversations_replies", "slack_read_thread", "message_page", ConfirmationNone, "channels:history", "groups:history", "im:history", "mpim:history"),
	local("message.exact.read", "conversations_get_message", "message", ConfirmationNone, AuthOAuth, MigrationActive, "channels:history", "groups:history", "im:history", "mpim:history"),
	legacy("message.send", "conversations_add_message", "slack_send_message", "message_mutation", ConfirmationChange, "chat:write"),
	local("draft.preview", "conversations_draft_message", "draft_preview", ConfirmationNone, AuthOAuth, MigrationLegacy),
	official("draft.persisted.create", "slack_send_message_draft", "draft", ConfirmationNone, "chat:write"),
	legacy("message.search", "conversations_search_messages", "slack_search_public_and_private", "search_results", ConfirmationNone, "search:read"),
	local("message.unreads.read", "conversations_unreads", "unread_page", ConfirmationNone, AuthOAuth, MigrationActive, "channels:history", "groups:history", "im:history", "mpim:history"),
	local("message.read_progress.mark", "conversations_mark", "read_progress", ConfirmationChange, AuthOAuth, MigrationActive, "channels:write", "groups:write", "im:write", "mpim:write"),
	legacy("conversation.open", "conversations_open", "slack_create_conversation", "conversation", ConfirmationChange, "im:write", "mpim:write"),
	legacy("conversation.join", "conversations_join", "slack_join_conversation", "conversation_membership", ConfirmationChange, "channels:join"),
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
	local("usergroup.mine.manage", "usergroups_me", "usergroup_membership", ConfirmationChange, AuthOAuth, MigrationLegacy, "usergroups:read", "usergroups:write"),
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

	official("file.upload", "slack_complete_file_upload", "file_mutation", ConfirmationChange, "files:write"),
	official("message.schedule", "slack_schedule_message", "scheduled_message", ConfirmationChange, "chat:write"),
	official("message.edit", "slack_edit_message", "message_mutation", ConfirmationChange, "chat:write"),
	official("message.delete", "slack_delete_message", "message_mutation", ConfirmationPreview, "chat:write"),
	official("conversation.create", "slack_create_conversation", "conversation", ConfirmationChange, "channels:manage", "groups:write"),
	official("conversation.members.list", "slack_list_channel_members", "member_page", ConfirmationNone, "channels:read", "groups:read"),
	official("conversation.members.invite", "slack_invite_to_conversation", "conversation_membership", ConfirmationChange, "channels:write", "groups:write"),
	official("channel.search", "slack_search_channels", "conversation_page", ConfirmationNone, "channels:read", "groups:read"),
	official("emoji.search", "slack_search_emojis", "emoji_page", ConfirmationNone, "emoji:read"),
	official("profile.read", "slack_read_user_profile", "user_profile", ConfirmationNone, "users.profile:read"),
	official("profile.update", "slack_update_user_profile", "user_profile", ConfirmationChange, "users.profile:write"),
	official("canvas.create", "slack_create_canvas", "canvas", ConfirmationChange, "canvases:write"),
	official("canvas.read", "slack_read_canvas", "canvas", ConfirmationNone, "canvases:read"),
	official("canvas.update", "slack_update_canvas", "canvas", ConfirmationChange, "canvases:write"),

	local("scheduled.list", "scheduled_messages_list", "scheduled_message_page", ConfirmationNone, AuthOAuth, MigrationActive, "chat:write"),
	local("scheduled.cancel", "scheduled_message_cancel", "scheduled_message_mutation", ConfirmationPreview, AuthOAuth, MigrationActive, "chat:write"),
	local("channel.rename", "channels_rename", "channel_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "channels:manage", "groups:write"),
	local("channel.topic.update", "channels_set_topic", "channel_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "channels:manage", "groups:write"),
	local("channel.purpose.update", "channels_set_purpose", "channel_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "channels:manage", "groups:write"),
	local("channel.archive", "channels_archive", "channel_mutation", ConfirmationPreview, AuthOAuth, MigrationActive, "channels:manage", "groups:write"),
	local("list.create", "lists_create", "list_create", ConfirmationChange, AuthOAuth, MigrationActive, "lists:write"),
	local("list.update", "lists_update", "list_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "lists:write"),
	local("list.items.list", "lists_items_list", "list_items_page", ConfirmationNone, AuthOAuth, MigrationActive, "lists:read"),
	local("list.items.create", "lists_items_create", "list_item", ConfirmationChange, AuthOAuth, MigrationActive, "lists:write"),
	local("list.items.update", "lists_items_update", "list_item_mutation", ConfirmationChange, AuthOAuth, MigrationActive, "lists:write"),
	local("list.item.delete", "lists_item_delete", "list_item_mutation", ConfirmationPreview, AuthOAuth, MigrationActive, "lists:read", "lists:write"),
	local("dnd.read", "dnd_get", "dnd_state", ConfirmationNone, AuthOAuth, MigrationActive, "dnd:read"),
	local("dnd.snooze.set", "dnd_set_snooze", "dnd_state", ConfirmationChange, AuthOAuth, MigrationActive, "dnd:write"),
	local("dnd.snooze.end", "dnd_end_snooze", "dnd_state", ConfirmationChange, AuthOAuth, MigrationActive, "dnd:write"),
	{ID: "draft.persisted.manage", Owner: OwnerLocalBrowser, LocalTool: "drafts_manage", Auth: AuthBrowser, Confirmation: ConfirmationNone, ResultType: "draft", Migration: MigrationPlanned, BrowserOptional: true},
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
		return e.Owner != OwnerOfficial && e.Migration == MigrationActive && e.Confirmation == ConfirmationNone
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

func VerifyInventory(official InventorySnapshot, host HostInventory) VerificationReport {
	var report VerificationReport
	add := func(code, id, detail string) {
		report.Issues = append(report.Issues, Issue{Code: code, CapabilityID: id, Detail: detail})
	}
	if official.CatalogVersion != "" && official.CatalogVersion != CatalogVersion {
		add("catalog_version_mismatch", "", fmt.Sprintf("official snapshot uses %s, expected %s", official.CatalogVersion, CatalogVersion))
	}
	if host.CatalogVersion != "" && host.CatalogVersion != CatalogVersion {
		add("catalog_version_mismatch", "", fmt.Sprintf("host inventory uses %s, expected %s", host.CatalogVersion, CatalogVersion))
	}
	if !identityComplete(official.Identity) || !identityComplete(host.OfficialIdentity) || !identityComplete(host.LocalIdentity) {
		add("identity_unverified", "", "official snapshot, host official provider, and local provider must each identify one workspace user")
	} else if identitiesConflict(official.Identity, host.OfficialIdentity) || identitiesConflict(host.OfficialIdentity, host.LocalIdentity) {
		add("identity_mismatch", "", "official and local providers do not represent the same workspace user")
	}

	observed := map[string]ObservedTool{}
	for _, tool := range official.Tools {
		observed[tool.Name] = tool
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
		if entry.Owner == OwnerOfficial {
			tool, ok := observed[entry.OfficialAction]
			if !ok {
				add("official_tool_missing", entry.ID, entry.OfficialAction+" absent from official snapshot")
			} else if !tool.InputSchemaObject || !tool.StructuredResult || !tool.SemanticsVerified {
				add("official_contract_incomplete", entry.ID, entry.OfficialAction+" lacks required schema, result, or behavior proof")
			}
		}
	}
	return report
}

func identitiesConflict(a, b Identity) bool {
	return a.TeamID != b.TeamID || a.UserID != b.UserID || (a.EnterpriseID != "" && b.EnterpriseID != "" && a.EnterpriseID != b.EnterpriseID)
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
