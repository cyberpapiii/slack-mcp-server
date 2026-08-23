// Package capability is the one table of per-tool facts: MCP annotations,
// preset membership, registration phase and OAuth scopes. pkg/server builds
// every registered tool from it, and slack-mcp-auth derives scopes from it.
package capability

import "sort"

type Tool struct {
	Name        string
	Title       string   // MCP title annotation
	DailyPower  bool     // in the default "daily-power" preset
	Browser     bool     // needs a Slack browser session (xoxc/xoxd); registered only when one is configured
	CacheReady  bool     // registered after the users/channels cache warms (RegisterCacheDependentTools)
	ReadOnly    bool     // MCP readOnlyHint; read-only tools are also non-destructive and idempotent
	Destructive bool     // only meaningful when !ReadOnly
	Idempotent  bool     // only meaningful when !ReadOnly
	Scopes      []string // OAuth scopes slack-mcp-auth requests for this tool
}

var Tools = []Tool{
	// Messages.
	{Name: "conversations_history", Title: "Get Conversation History", ReadOnly: true, Scopes: []string{"channels:history", "groups:history", "im:history", "mpim:history"}},
	{Name: "conversations_replies", Title: "Get Thread Replies", ReadOnly: true, Scopes: []string{"channels:history", "groups:history", "im:history", "mpim:history"}},
	{Name: "conversations_get_message", Title: "Get Single Message", DailyPower: true, ReadOnly: true, Scopes: []string{"channels:history", "groups:history", "im:history", "mpim:history"}},
	{Name: "conversations_add_message", Title: "Send Message", Scopes: []string{"chat:write"}},
	{Name: "conversations_draft_message", Title: "Draft Message", ReadOnly: true},

	// Persisted drafts (browser session).
	{Name: "drafts_list", Title: "List Drafts", DailyPower: true, Browser: true, ReadOnly: true},
	{Name: "drafts_get", Title: "Get Draft", DailyPower: true, Browser: true, ReadOnly: true},
	{Name: "drafts_create", Title: "Create Draft", Browser: true},
	{Name: "drafts_update", Title: "Update Draft", Browser: true, Destructive: true},
	{Name: "drafts_delete", Title: "Delete Draft", Browser: true, Destructive: true},

	// Search and unreads.
	{Name: "conversations_search_messages", Title: "Search Messages", ReadOnly: true, Scopes: []string{"search:read"}},
	{Name: "conversations_unreads", Title: "Get Unread Messages", DailyPower: true, Browser: true, CacheReady: true, ReadOnly: true},
	{Name: "conversations_mark", Title: "Mark Conversation Read", Idempotent: true, Scopes: []string{"channels:write", "groups:write", "im:write", "mpim:write"}},

	// Conversation membership.
	{Name: "conversations_open", Title: "Open Conversation", Idempotent: true, Scopes: []string{"im:write", "mpim:write"}},
	{Name: "conversations_join", Title: "Join Channel", Idempotent: true, Scopes: []string{"channels:write"}},
	{Name: "conversations_leave", Title: "Leave Channel", Destructive: true, Scopes: []string{"channels:write", "groups:write"}},
	{Name: "channels_list", Title: "List Channels", CacheReady: true, ReadOnly: true, Scopes: []string{"channels:read", "groups:read", "im:read", "mpim:read"}},
	{Name: "channels_starred", Title: "List Starred Channels", CacheReady: true, ReadOnly: true, Scopes: []string{"stars:read"}},
	{Name: "channels_me", Title: "My Channels", CacheReady: true, ReadOnly: true, Scopes: []string{"channels:read", "groups:read", "im:read", "mpim:read"}},

	// Reactions.
	{Name: "reactions_add", Title: "Add Reaction", Scopes: []string{"reactions:write"}},
	{Name: "reactions_remove", Title: "Remove Reaction", Destructive: true, Scopes: []string{"reactions:write"}},
	{Name: "reactions_get", Title: "Get Message Reactions", ReadOnly: true, Scopes: []string{"reactions:read"}},

	// Files and users.
	{Name: "attachment_get_data", Title: "Get Attachment Data", ReadOnly: true, Scopes: []string{"files:read"}},
	{Name: "files_list", Title: "List Files", ReadOnly: true, Scopes: []string{"files:read"}},
	{Name: "users_search", Title: "Search Users", ReadOnly: true, Scopes: []string{"users:read"}},

	// User groups.
	{Name: "usergroups_list", Title: "List User Groups", DailyPower: true, ReadOnly: true, Scopes: []string{"usergroups:read"}},
	{Name: "usergroups_mine", Title: "List My User Groups", DailyPower: true, ReadOnly: true, Scopes: []string{"usergroups:read"}},
	{Name: "usergroups_join", Title: "Join User Group", Idempotent: true, Scopes: []string{"usergroups:read", "usergroups:write"}},
	{Name: "usergroups_leave", Title: "Leave User Group", Destructive: true, Idempotent: true, Scopes: []string{"usergroups:read", "usergroups:write"}},
	{Name: "usergroups_create", Title: "Create User Group", Scopes: []string{"usergroups:write"}},
	{Name: "usergroups_update", Title: "Update User Group", Scopes: []string{"usergroups:write"}},
	{Name: "usergroups_users_update", Title: "Replace User Group Members", Destructive: true, Idempotent: true, Scopes: []string{"usergroups:write"}},

	// Activity and saved items (browser session).
	{Name: "activity_unreads", Title: "Get Activity Unreads", DailyPower: true, Browser: true, CacheReady: true, ReadOnly: true},
	{Name: "activity_mark_read", Title: "Mark Activity Read", Browser: true, CacheReady: true, Idempotent: true},
	{Name: "saved_list", Title: "List Saved Items", DailyPower: true, Browser: true, ReadOnly: true},
	{Name: "saved_update", Title: "Update Saved Item", Browser: true, Idempotent: true},
	{Name: "saved_clear_completed", Title: "Clear Completed Saved Items", Browser: true, Destructive: true, Idempotent: true},

	// Diagnostics.
	{Name: "slack_auth_status", Title: "Auth & Cache Status", DailyPower: true, ReadOnly: true},

	// File and message mutations.
	{Name: "files_upload", Title: "Upload File", Scopes: []string{"files:write"}},
	{Name: "messages_schedule", Title: "Schedule Message", Scopes: []string{"chat:write"}},
	{Name: "messages_update", Title: "Update Message", Destructive: true, Scopes: []string{"chat:write"}},
	{Name: "messages_delete", Title: "Delete Message", Destructive: true, Scopes: []string{"chat:write"}},

	// Channels, people and profiles.
	{Name: "channels_create", Title: "Create Channel", Scopes: []string{"channels:write", "groups:write"}},
	{Name: "channels_members", Title: "List Channel Members", DailyPower: true, ReadOnly: true, Scopes: []string{"channels:read", "groups:read"}},
	{Name: "channels_invite", Title: "Invite Channel Members", Scopes: []string{"channels:write", "channels:write.invites", "groups:write", "groups:write.invites"}},
	{Name: "emoji_list", Title: "List Custom Emoji", DailyPower: true, ReadOnly: true, Scopes: []string{"emoji:read"}},
	{Name: "users_get_profile", Title: "Get User Profile", DailyPower: true, ReadOnly: true, Scopes: []string{"users.profile:read"}},
	{Name: "users_set_profile", Title: "Update User Profile", Destructive: true, Scopes: []string{"users.profile:write"}},
	{Name: "users_set_status", Title: "Set User Status", Scopes: []string{"users.profile:write"}},

	// Canvases and semantic search.
	{Name: "canvases_create", Title: "Create Canvas", Scopes: []string{"canvases:write"}},
	{Name: "canvases_read", Title: "Read Canvas Sections", DailyPower: true, ReadOnly: true, Scopes: []string{"canvases:read"}},
	{Name: "canvases_update", Title: "Update Canvas", Destructive: true, Scopes: []string{"canvases:write"}},
	{Name: "search_semantic", Title: "Search Slack Semantically", DailyPower: true, ReadOnly: true, Scopes: []string{"search:read.files", "search:read.public", "search:read.private", "search:read.im", "search:read.mpim", "search:read.users"}},

	// Scheduled messages.
	{Name: "scheduled_messages_list", Title: "List Scheduled Messages", DailyPower: true, ReadOnly: true},
	{Name: "scheduled_message_cancel", Title: "Cancel Scheduled Message", Destructive: true, Scopes: []string{"chat:write"}},

	// Channel mutations.
	{Name: "channels_rename", Title: "Rename Channel", Scopes: []string{"channels:write", "groups:write"}},
	{Name: "channels_set_topic", Title: "Set Channel Topic", Scopes: []string{"channels:write", "groups:write"}},
	{Name: "channels_set_purpose", Title: "Set Channel Purpose", Scopes: []string{"channels:write", "groups:write"}},
	{Name: "channels_archive", Title: "Archive Channel", Destructive: true, Scopes: []string{"channels:write", "groups:write"}},

	// Slack Lists.
	{Name: "lists_create", Title: "Create Slack List", Scopes: []string{"lists:write"}},
	{Name: "lists_update", Title: "Update Slack List", Scopes: []string{"lists:write"}},
	{Name: "lists_items_list", Title: "List Slack List Items", DailyPower: true, ReadOnly: true, Scopes: []string{"lists:read"}},
	{Name: "lists_items_create", Title: "Create Slack List Item", Scopes: []string{"lists:write"}},
	{Name: "lists_items_update", Title: "Update Slack List Items", Scopes: []string{"lists:write"}},
	{Name: "lists_item_delete", Title: "Delete Slack List Item", Destructive: true, Scopes: []string{"lists:read", "lists:write"}},

	// Do Not Disturb.
	{Name: "dnd_get", Title: "Get Do Not Disturb", DailyPower: true, ReadOnly: true, Scopes: []string{"dnd:read"}},
	{Name: "dnd_set_snooze", Title: "Set Do Not Disturb", Scopes: []string{"dnd:write"}},
	{Name: "dnd_end_snooze", Title: "End Do Not Disturb", Scopes: []string{"dnd:write"}},
}

func Lookup(name string) (Tool, bool) {
	for _, tool := range Tools {
		if tool.Name == name {
			tool.Scopes = append([]string(nil), tool.Scopes...)
			return tool, true
		}
	}
	return Tool{}, false
}

// Names lists every tool, sorted.
func Names() []string {
	return names(func(Tool) bool { return true })
}

// DailyPowerNames lists the default preset, sorted.
func DailyPowerNames() []string {
	return names(func(t Tool) bool { return t.DailyPower })
}

// BrowserNames lists the tools that need a Slack browser session, sorted.
func BrowserNames() []string {
	return names(func(t Tool) bool { return t.Browser })
}

func names(include func(Tool) bool) []string {
	var result []string
	for _, tool := range Tools {
		if include(tool) {
			result = append(result, tool.Name)
		}
	}
	sort.Strings(result)
	return result
}

// OAuthScopesForTools returns the deduplicated, sorted union of the scopes the
// named tools need.
func OAuthScopesForTools(tools []string) []string {
	wanted := make(map[string]bool, len(tools))
	for _, tool := range tools {
		wanted[tool] = true
	}
	seen := map[string]bool{}
	var scopes []string
	for _, tool := range Tools {
		if !wanted[tool.Name] {
			continue
		}
		for _, scope := range tool.Scopes {
			if !seen[scope] {
				seen[scope] = true
				scopes = append(scopes, scope)
			}
		}
	}
	sort.Strings(scopes)
	return scopes
}
