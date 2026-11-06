// Package capability is the one table of per-tool facts: MCP annotations,
// preset membership, registration phase and OAuth scopes. pkg/server builds
// every registered tool from it, and slack-mcp-auth derives scopes from it.
package capability

import (
	"fmt"
	"sort"
	"strings"
)

// Tool names.
const (
	ActivityMarkRead            = "activity_mark_read"
	ActivityUnreads             = "activity_unreads"
	AttachmentGetData           = "attachment_get_data"
	CanvasesCreate              = "canvases_create"
	CanvasesRead                = "canvases_read"
	CanvasesUpdate              = "canvases_update"
	ChannelsArchive             = "channels_archive"
	ChannelsCreate              = "channels_create"
	ChannelsInvite              = "channels_invite"
	ChannelsList                = "channels_list"
	ChannelsMe                  = "channels_me"
	ChannelsMembers             = "channels_members"
	ChannelsRename              = "channels_rename"
	ChannelsSetPurpose          = "channels_set_purpose"
	ChannelsSetTopic            = "channels_set_topic"
	ChannelsStarred             = "channels_starred"
	ConversationsAddMessage     = "conversations_add_message"
	ConversationsDraftMessage   = "conversations_draft_message"
	ConversationsGetMessage     = "conversations_get_message"
	ConversationsHistory        = "conversations_history"
	ConversationsJoin           = "conversations_join"
	ConversationsLeave          = "conversations_leave"
	ConversationsMark           = "conversations_mark"
	ConversationsOpen           = "conversations_open"
	ConversationsReplies        = "conversations_replies"
	ConversationsSearchMessages = "conversations_search_messages"
	ConversationsUnreads        = "conversations_unreads"
	DNDEndSnooze                = "dnd_end_snooze"
	DNDGet                      = "dnd_get"
	DNDSetSnooze                = "dnd_set_snooze"
	DraftsCreate                = "drafts_create"
	DraftsDelete                = "drafts_delete"
	DraftsGet                   = "drafts_get"
	DraftsList                  = "drafts_list"
	DraftsUpdate                = "drafts_update"
	EmojiList                   = "emoji_list"
	FilesDownload               = "files_download"
	FilesList                   = "files_list"
	FilesUpload                 = "files_upload"
	ListsCreate                 = "lists_create"
	ListsItemDelete             = "lists_item_delete"
	ListsItemsCreate            = "lists_items_create"
	ListsItemsList              = "lists_items_list"
	ListsItemsUpdate            = "lists_items_update"
	ListsUpdate                 = "lists_update"
	MessagesDelete              = "messages_delete"
	MessagesSchedule            = "messages_schedule"
	MessagesUpdate              = "messages_update"
	ReactionsAdd                = "reactions_add"
	ReactionsGet                = "reactions_get"
	ReactionsRemove             = "reactions_remove"
	SavedAdd                    = "saved_add"
	SavedClearCompleted         = "saved_clear_completed"
	SavedDelete                 = "saved_delete"
	SavedList                   = "saved_list"
	SavedUpdate                 = "saved_update"
	ScheduledMessageCancel      = "scheduled_message_cancel"
	ScheduledMessagesList       = "scheduled_messages_list"
	SearchSemantic              = "search_semantic"
	SlackAuthStatus             = "slack_auth_status"
	UsergroupsCreate            = "usergroups_create"
	UsergroupsJoin              = "usergroups_join"
	UsergroupsLeave             = "usergroups_leave"
	UsergroupsList              = "usergroups_list"
	UsergroupsMine              = "usergroups_mine"
	UsergroupsUpdate            = "usergroups_update"
	UsergroupsUsersUpdate       = "usergroups_users_update"
	UsersGetProfile             = "users_get_profile"
	UsersSearch                 = "users_search"
	UsersSetProfile             = "users_set_profile"
	UsersSetStatus              = "users_set_status"
)

// Requirement is the credential a tool needs. A tool whose requirement the
// running credentials cannot meet is not registered.
type Requirement int

const (
	AnyToken       Requirement = iota
	UserToken                  // any token that is not a bot token
	UserOAuth                  // a user OAuth token
	BrowserSession             // a configured Slack browser session (xoxc/xoxd)
)

type Tool struct {
	Name        string
	Title       string // MCP title annotation
	DailyPower  bool   // in the default "daily-power" preset
	Requires    Requirement
	CacheReady  bool     // registered after the users/channels cache warms (RegisterCacheDependentTools)
	ReadOnly    bool     // MCP readOnlyHint; read-only tools are also non-destructive and idempotent
	Destructive bool     // only meaningful when !ReadOnly
	Idempotent  bool     // only meaningful when !ReadOnly
	Scopes      []string // OAuth scopes slack-mcp-auth requests for this tool
}

var Tools = []Tool{
	// Messages.
	{Name: ConversationsHistory, Title: "Get Conversation History", ReadOnly: true, Scopes: []string{"channels:history", "groups:history", "im:history", "mpim:history"}},
	{Name: ConversationsReplies, Title: "Get Thread Replies", ReadOnly: true, Scopes: []string{"channels:history", "groups:history", "im:history", "mpim:history"}},
	{Name: ConversationsGetMessage, Title: "Get Single Message", DailyPower: true, ReadOnly: true, Scopes: []string{"channels:history", "groups:history", "im:history", "mpim:history"}},
	{Name: ConversationsAddMessage, Title: "Send Message", Scopes: []string{"chat:write"}},
	{Name: ConversationsDraftMessage, Title: "Draft Message", ReadOnly: true},

	// Persisted drafts (browser session).
	{Name: DraftsList, Title: "List Drafts", DailyPower: true, Requires: BrowserSession, ReadOnly: true},
	{Name: DraftsGet, Title: "Get Draft", DailyPower: true, Requires: BrowserSession, ReadOnly: true},
	{Name: DraftsCreate, Title: "Create Draft", Requires: BrowserSession},
	{Name: DraftsUpdate, Title: "Update Draft", Requires: BrowserSession, Destructive: true},
	{Name: DraftsDelete, Title: "Delete Draft", Requires: BrowserSession, Destructive: true},

	// Search and unreads.
	{Name: ConversationsSearchMessages, Requires: UserToken, Title: "Search Messages", ReadOnly: true, Scopes: []string{"search:read"}},
	{Name: ConversationsUnreads, Title: "Get Unread Messages", DailyPower: true, Requires: BrowserSession, CacheReady: true, ReadOnly: true},
	{Name: ConversationsMark, Title: "Mark Conversation Read", Idempotent: true, Scopes: []string{"channels:write", "groups:write", "im:write", "mpim:write"}},

	// Conversation membership.
	{Name: ConversationsOpen, Title: "Open Conversation", Idempotent: true, Scopes: []string{"im:write", "mpim:write"}},
	{Name: ConversationsJoin, Title: "Join Channel", Idempotent: true, Scopes: []string{"channels:write"}},
	{Name: ConversationsLeave, Title: "Leave Channel", Destructive: true, Scopes: []string{"channels:write", "groups:write"}},
	{Name: ChannelsList, Title: "List Channels", CacheReady: true, ReadOnly: true, Scopes: []string{"channels:read", "groups:read", "im:read", "mpim:read"}},
	{Name: ChannelsStarred, Requires: UserToken, Title: "List Starred Channels", CacheReady: true, ReadOnly: true, Scopes: []string{"stars:read"}},
	{Name: ChannelsMe, Title: "My Channels", CacheReady: true, ReadOnly: true, Scopes: []string{"channels:read", "groups:read", "im:read", "mpim:read"}},

	// Reactions.
	{Name: ReactionsAdd, Title: "Add Reaction", Scopes: []string{"reactions:write"}},
	{Name: ReactionsRemove, Title: "Remove Reaction", Destructive: true, Scopes: []string{"reactions:write"}},
	{Name: ReactionsGet, Title: "Get Message Reactions", ReadOnly: true, Scopes: []string{"reactions:read"}},

	// Files and users.
	{Name: AttachmentGetData, Title: "Get Attachment Data", ReadOnly: true, Scopes: []string{"files:read"}},
	{Name: FilesList, Title: "List Files", ReadOnly: true, Scopes: []string{"files:read"}},
	{Name: FilesDownload, Title: "Download File To Disk", Idempotent: true, Scopes: []string{"files:read"}},
	{Name: UsersSearch, Title: "Search Users", ReadOnly: true, Scopes: []string{"users:read"}},

	// User groups.
	{Name: UsergroupsList, Title: "List User Groups", DailyPower: true, ReadOnly: true, Scopes: []string{"usergroups:read"}},
	{Name: UsergroupsMine, Title: "List My User Groups", DailyPower: true, ReadOnly: true, Scopes: []string{"usergroups:read"}},
	{Name: UsergroupsJoin, Title: "Join User Group", Idempotent: true, Scopes: []string{"usergroups:read", "usergroups:write"}},
	{Name: UsergroupsLeave, Title: "Leave User Group", Destructive: true, Idempotent: true, Scopes: []string{"usergroups:read", "usergroups:write"}},
	{Name: UsergroupsCreate, Title: "Create User Group", Scopes: []string{"usergroups:write"}},
	{Name: UsergroupsUpdate, Title: "Update User Group", Scopes: []string{"usergroups:write"}},
	{Name: UsergroupsUsersUpdate, Title: "Replace User Group Members", Destructive: true, Idempotent: true, Scopes: []string{"usergroups:write"}},

	// Activity and saved items (browser session).
	{Name: ActivityUnreads, Title: "Get Activity Unreads", DailyPower: true, Requires: BrowserSession, CacheReady: true, ReadOnly: true},
	{Name: ActivityMarkRead, Title: "Mark Activity Read", Requires: BrowserSession, CacheReady: true, Idempotent: true},
	{Name: SavedList, Title: "List To-dos and Saved Items", DailyPower: true, Requires: BrowserSession, ReadOnly: true},
	{Name: SavedAdd, Title: "Add To-do or Saved Item", Requires: BrowserSession},
	{Name: SavedUpdate, Title: "Update To-do or Saved Item", Requires: BrowserSession, Idempotent: true},
	{Name: SavedDelete, Title: "Remove To-do or Saved Item", Requires: BrowserSession, Destructive: true, Idempotent: true},
	{Name: SavedClearCompleted, Title: "Clear Completed To-dos", Requires: BrowserSession, Destructive: true, Idempotent: true},

	// Diagnostics.
	{Name: SlackAuthStatus, Title: "Auth & Cache Status", DailyPower: true, ReadOnly: true},

	// File and message mutations.
	{Name: FilesUpload, Title: "Upload File", Scopes: []string{"files:write"}},
	{Name: MessagesSchedule, Title: "Schedule Message", Scopes: []string{"chat:write"}},
	{Name: MessagesUpdate, Title: "Update Message", Destructive: true, Scopes: []string{"chat:write"}},
	{Name: MessagesDelete, Title: "Delete Message", Destructive: true, Scopes: []string{"chat:write"}},

	// Channels, people and profiles.
	{Name: ChannelsCreate, Title: "Create Channel", Scopes: []string{"channels:write", "groups:write"}},
	{Name: ChannelsMembers, Title: "List Channel Members", DailyPower: true, ReadOnly: true, Scopes: []string{"channels:read", "groups:read"}},
	{Name: ChannelsInvite, Title: "Invite Channel Members", Scopes: []string{"channels:write", "channels:write.invites", "groups:write", "groups:write.invites"}},
	{Name: EmojiList, Title: "List Custom Emoji", DailyPower: true, ReadOnly: true, Scopes: []string{"emoji:read"}},
	{Name: UsersGetProfile, Title: "Get User Profile", DailyPower: true, ReadOnly: true, Scopes: []string{"users.profile:read"}},
	{Name: UsersSetProfile, Title: "Update User Profile", Destructive: true, Scopes: []string{"users.profile:write"}},
	{Name: UsersSetStatus, Title: "Set User Status", Scopes: []string{"users.profile:write"}},

	// Canvases and semantic search.
	{Name: CanvasesCreate, Title: "Create Canvas", Scopes: []string{"canvases:write"}},
	{Name: CanvasesRead, Title: "Read Canvas", DailyPower: true, ReadOnly: true, Scopes: []string{"canvases:read"}},
	{Name: CanvasesUpdate, Title: "Update Canvas", Destructive: true, Scopes: []string{"canvases:write"}},
	{Name: SearchSemantic, Title: "Search Slack Semantically", DailyPower: true, ReadOnly: true, Scopes: []string{"search:read.files", "search:read.public", "search:read.private", "search:read.im", "search:read.mpim", "search:read.users"}},

	// Scheduled messages.
	{Name: ScheduledMessagesList, Title: "List Scheduled Messages", DailyPower: true, ReadOnly: true},
	{Name: ScheduledMessageCancel, Title: "Cancel Scheduled Message", Destructive: true, Scopes: []string{"chat:write"}},

	// Channel mutations.
	{Name: ChannelsRename, Title: "Rename Channel", Scopes: []string{"channels:write", "groups:write"}},
	{Name: ChannelsSetTopic, Title: "Set Channel Topic", Scopes: []string{"channels:write", "groups:write"}},
	{Name: ChannelsSetPurpose, Title: "Set Channel Purpose", Scopes: []string{"channels:write", "groups:write"}},
	{Name: ChannelsArchive, Title: "Archive Channel", Destructive: true, Scopes: []string{"channels:write", "groups:write"}},

	// Slack Lists.
	{Name: ListsCreate, Requires: UserOAuth, Title: "Create Slack List", Scopes: []string{"lists:write"}},
	{Name: ListsUpdate, Requires: UserOAuth, Title: "Update Slack List", Scopes: []string{"lists:write"}},
	{Name: ListsItemsList, Requires: UserOAuth, Title: "List Slack List Items", DailyPower: true, ReadOnly: true, Scopes: []string{"lists:read"}},
	{Name: ListsItemsCreate, Requires: UserOAuth, Title: "Create Slack List Item", Scopes: []string{"lists:write"}},
	{Name: ListsItemsUpdate, Requires: UserOAuth, Title: "Update Slack List Items", Scopes: []string{"lists:write"}},
	{Name: ListsItemDelete, Requires: UserOAuth, Title: "Delete Slack List Item", Destructive: true, Scopes: []string{"lists:read", "lists:write"}},

	// Do Not Disturb.
	{Name: DNDGet, Requires: UserOAuth, Title: "Get Do Not Disturb", DailyPower: true, ReadOnly: true, Scopes: []string{"dnd:read"}},
	{Name: DNDSetSnooze, Requires: UserOAuth, Title: "Set Do Not Disturb", Scopes: []string{"dnd:write"}},
	{Name: DNDEndSnooze, Requires: UserOAuth, Title: "End Do Not Disturb", Scopes: []string{"dnd:write"}},
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
	return names(func(t Tool) bool { return t.Requires == BrowserSession })
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

// cacheScopes are what the users and channels caches need to warm
// (users.list, conversations.list); every name-resolving tool reads them.
var cacheScopes = []string{"channels:read", "groups:read", "im:read", "mpim:read", "users:read"}

// OAuthScopesForTools returns the deduplicated, sorted scopes a server serving
// these tools needs: each tool's own plus the cache warm-up scopes.
func OAuthScopesForTools(tools []string) []string {
	wanted := make(map[string]bool, len(tools))
	for _, tool := range tools {
		wanted[tool] = true
	}
	seen := map[string]bool{}
	var scopes []string
	add := func(scope string) {
		if !seen[scope] {
			seen[scope] = true
			scopes = append(scopes, scope)
		}
	}
	for _, scope := range cacheScopes {
		add(scope)
	}
	for _, tool := range Tools {
		if wanted[tool.Name] {
			for _, scope := range tool.Scopes {
				add(scope)
			}
		}
	}
	sort.Strings(scopes)
	return scopes
}

// Preset returns the tool names a named preset enables.
func Preset(name string) ([]string, error) {
	switch name {
	case "daily-power":
		return DailyPowerNames(), nil
	case "legacy-full":
		return Names(), nil
	default:
		return nil, fmt.Errorf("unknown tool preset %q (valid: daily-power, legacy-full)", name)
	}
}

// Validate reports every name that is not a tool in the table.
func Validate(names []string) error {
	var invalid []string
	for _, name := range names {
		if _, ok := Lookup(name); !ok {
			invalid = append(invalid, name)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("invalid tool name(s): %s. Valid tools are: %s",
			strings.Join(invalid, ", "), strings.Join(Names(), ", "))
	}
	return nil
}
