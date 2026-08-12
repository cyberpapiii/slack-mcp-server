package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/server/auth"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/korotovsky/slack-mcp-server/pkg/version"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

type MCPServer struct {
	server         *server.MCPServer
	logger         *zap.Logger
	workspace      string
	provider       *provider.ApiProvider
	enabledTools   []string
	cacheToolsOnce sync.Once
}

const (
	ToolConversationsHistory        = "conversations_history"
	ToolConversationsReplies        = "conversations_replies"
	ToolConversationsAddMessage     = "conversations_add_message"
	ToolConversationsDraftMessage   = "conversations_draft_message"
	ToolReactionsAdd                = "reactions_add"
	ToolReactionsRemove             = "reactions_remove"
	ToolReactionsGet                = "reactions_get"
	ToolConversationsGetMessage     = "conversations_get_message"
	ToolAttachmentGetData           = "attachment_get_data"
	ToolConversationsSearchMessages = "conversations_search_messages"
	ToolConversationsUnreads        = "conversations_unreads"
	ToolConversationsMark           = "conversations_mark"
	ToolConversationsOpen           = "conversations_open"
	ToolConversationsLeave          = "conversations_leave"
	ToolConversationsJoin           = "conversations_join"
	ToolChannelsList                = "channels_list"
	ToolChannelsStarred             = "channels_starred"
	ToolChannelsMe                  = "channels_me"
	ToolUsergroupsList              = "usergroups_list"
	ToolUsergroupsMe                = "usergroups_me"
	ToolUsergroupsMine              = "usergroups_mine"
	ToolUsergroupsJoin              = "usergroups_join"
	ToolUsergroupsLeave             = "usergroups_leave"
	ToolUsergroupsCreate            = "usergroups_create"
	ToolUsergroupsUpdate            = "usergroups_update"
	ToolUsergroupsUsersUpdate       = "usergroups_users_update"
	ToolUsersSearch                 = "users_search"
	ToolActivityUnreads             = "activity_unreads"
	ToolActivityMarkRead            = "activity_mark_read"
	ToolSavedList                   = "saved_list"
	ToolSavedUpdate                 = "saved_update"
	ToolSavedClearCompleted         = "saved_clear_completed"
	ToolFilesList                   = "files_list"
	ToolSlackAuthStatus             = "slack_auth_status"
	ToolScheduledMessagesList       = "scheduled_messages_list"
	ToolScheduledMessageCancel      = "scheduled_message_cancel"
	ToolChannelsRename              = "channels_rename"
	ToolChannelsSetTopic            = "channels_set_topic"
	ToolChannelsSetPurpose          = "channels_set_purpose"
	ToolChannelsArchive             = "channels_archive"
	ToolListsCreate                 = "lists_create"
	ToolListsUpdate                 = "lists_update"
	ToolListsItemsList              = "lists_items_list"
	ToolListsItemsCreate            = "lists_items_create"
	ToolListsItemsUpdate            = "lists_items_update"
	ToolListsItemDelete             = "lists_item_delete"
	ToolDNDGet                      = "dnd_get"
	ToolDNDSetSnooze                = "dnd_set_snooze"
	ToolDNDEndSnooze                = "dnd_end_snooze"
	ToolFilesUpload                 = "files_upload"
	ToolMessagesSchedule            = "messages_schedule"
	ToolMessagesUpdate              = "messages_update"
	ToolMessagesDelete              = "messages_delete"
	ToolChannelsCreate              = "channels_create"
	ToolChannelsMembers             = "channels_members"
	ToolChannelsInvite              = "channels_invite"
	ToolEmojiList                   = "emoji_list"
	ToolUsersGetProfile             = "users_get_profile"
	ToolUsersSetProfile             = "users_set_profile"
	ToolUsersSetStatus              = "users_set_status"
	ToolCanvasesCreate              = "canvases_create"
	ToolCanvasesRead                = "canvases_read"
	ToolCanvasesUpdate              = "canvases_update"
	ToolDraftsList                  = "drafts_list"
	ToolDraftsGet                   = "drafts_get"
	ToolDraftsCreate                = "drafts_create"
	ToolDraftsUpdate                = "drafts_update"
	ToolDraftsDelete                = "drafts_delete"
	ToolSearchSemantic              = "search_semantic"

	toolDetailDescription = "Output fidelity: 'standard' (compact CSV) or 'full' (all columns, including UserID and Permalink where available). When omitted, follows SLACK_MCP_COMPACT_OUTPUT (compact/standard unless that env is false/0/no, then full). Overrides the server-wide default for this call only. Output may begin with `#users:` (UserID=name legend) and `#link_template:` (build message permalinks from Channel + MsgID) comment lines before the CSV header."
)

var ValidToolNames = capability.LegacyFullLocalTools()

func ResolveToolPreset(name string) ([]string, error) {
	switch name {
	case "daily-power":
		return capability.DailyPowerLocalTools(), nil
	case "legacy-full":
		return capability.LegacyFullLocalTools(), nil
	default:
		return nil, fmt.Errorf("unknown tool preset %q (valid: daily-power, legacy-full)", name)
	}
}

func ValidateEnabledTools(tools []string) error {
	validToolSet := make(map[string]bool, len(ValidToolNames))
	for _, name := range ValidToolNames {
		validToolSet[name] = true
	}

	var invalidTools []string
	for _, tool := range tools {
		if !validToolSet[tool] {
			invalidTools = append(invalidTools, tool)
		}
	}
	if len(invalidTools) > 0 {
		return fmt.Errorf("invalid tool name(s): %s. Valid tools are: %s",
			strings.Join(invalidTools, ", "),
			strings.Join(ValidToolNames, ", "))
	}
	return nil
}

// channelListGates are gate variables whose value is a channel allowlist, not
// a boolean, e.g. "C1234567890,D0987654321" or "!C1234567890". For these, any
// non-empty value means "enabled", because the value IS the configuration.
// Every other gate variable is a boolean and goes through envutil.IsTruthy.
var channelListGates = map[string]bool{
	"SLACK_MCP_ADD_MESSAGE_TOOL":        true,
	"SLACK_MCP_REACTION_TOOL":           true,
	"SLACK_MCP_CHANNEL_MANAGEMENT_TOOL": true,
}

func shouldAddTool(name string, enabledTools []string, envVarName string) bool {
	if envVarName == "" {
		if len(enabledTools) == 0 {
			return true
		}
		return slices.Contains(enabledTools, name)
	}

	if len(enabledTools) > 0 && slices.Contains(enabledTools, name) {
		return true
	}

	if len(enabledTools) == 0 {
		value := os.Getenv(envVarName)
		if channelListGates[envVarName] {
			return value != ""
		}
		return envutil.IsTruthy(value)
	}

	return false
}

func NewMCPServer(provider *provider.ApiProvider, logger *zap.Logger, enabledTools []string) *MCPServer {
	s := server.NewMCPServer(
		"Slack MCP Server",
		version.Version,
		server.WithLogging(),
		server.WithRecovery(),
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, true),
		// mcp-go applies middlewares in reverse registration order; register
		// auth first so it stays outermost and API-key failures stay protocol errors.
		server.WithToolHandlerMiddleware(auth.BuildMiddleware(provider.ServerTransport(), logger)),
		server.WithToolHandlerMiddleware(buildLoggerMiddleware(logger)),
		server.WithToolHandlerMiddleware(buildErrorRecoveryMiddleware(logger)),
	)

	conversationsHandler := handler.NewConversationsHandler(provider, logger)
	authStatusHandler := handler.NewAuthStatusHandler(provider, logger)

	if shouldAddTool(ToolSlackAuthStatus, enabledTools, "") {
		s.AddTool(newDailyPowerTool(ToolSlackAuthStatus,
			mcp.WithDescription("Report Slack auth, cache readiness, and browser-session health. Use before activity or saved tools when auth may have expired."),
		), authStatusHandler.Handler)
	}

	if shouldAddTool(ToolConversationsOpen, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolConversationsOpen,
			mcp.WithDescription("Open a direct message (DM) or multi-person direct message (MPIM) with one or more users. Returns the channel ID of the opened conversation."),
			mcp.WithTitleAnnotation("Open Conversation"),
			mcp.WithString("users",
				mcp.Required(),
				mcp.Description("Comma-separated list of user IDs or @usernames to open a DM with. Example: U12345678, @username"),
			),
		), conversationsHandler.ConversationsOpenHandler)
	}

	if shouldAddTool(ToolConversationsHistory, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolConversationsHistory,
			mcp.WithDescription("Fetch messages from a channel or DM by channel_id. When more messages exist, the cursor value in the last CSV row is the 'cursor' parameter for the next call."),
			mcp.WithTitleAnnotation("Get Conversation History"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @, e.g. #general or @username_dm."),
			),
			mcp.WithBoolean("include_activity_messages",
				mcp.Description("If true, include activity messages such as 'channel_join' or 'channel_leave'. Default is false."),
				mcp.DefaultBool(false),
			),
			mcp.WithString("cursor",
				mcp.Description("Pagination cursor. Pass the cursor value from the last row of the previous response."),
			),
			mcp.WithString("limit",
				mcp.DefaultString("1d"),
				mcp.Description("How much history to fetch: a time range ('1d' for 1 day, '1w' for 1 week, '30d', '90d' which is the free-tier history limit) or a number of messages (e.g. 50). Default is 1d. Ignored when 'cursor' is set."),
			),
			mcp.WithString("detail",
				mcp.Description(toolDetailDescription),
				mcp.Enum("standard", "full"),
			),
		), conversationsHandler.ConversationsHistoryHandler)
	}

	if shouldAddTool(ToolConversationsReplies, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolConversationsReplies,
			mcp.WithDescription("Fetch a thread's messages by channel_id and thread_ts. When more messages exist, the cursor value in the last CSV row is the 'cursor' parameter for the next call."),
			mcp.WithTitleAnnotation("Get Thread Replies"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @, e.g. #general or @username_dm."),
			),
			mcp.WithString("thread_ts",
				mcp.Required(),
				mcp.Description("Timestamp of the thread's parent message, or of any message in the thread, in format 1234567890.123456."),
			),
			mcp.WithBoolean("include_activity_messages",
				mcp.Description("If true, include activity messages such as 'channel_join' or 'channel_leave'. Default is false."),
				mcp.DefaultBool(false),
			),
			mcp.WithString("cursor",
				mcp.Description("Pagination cursor. Pass the cursor value from the last row of the previous response."),
			),
			mcp.WithString("limit",
				mcp.DefaultString("1d"),
				mcp.Description("How many replies to fetch: a time range ('1d' for 1 day, '30d', '90d' which is the free-tier history limit) or a number of messages (e.g. 50). Default is 1d. Ignored when 'cursor' is set."),
			),
			mcp.WithString("detail",
				mcp.Description(toolDetailDescription),
				mcp.Enum("standard", "full"),
			),
		), conversationsHandler.ConversationsRepliesHandler)
	}

	if shouldAddTool(ToolReactionsGet, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolReactionsGet,
			mcp.WithDescription("Get detailed reaction data for a specific message, including which users reacted with each emoji. Returns CSV with Emoji, Count, and Users (semicolon-separated user IDs) columns."),
			mcp.WithTitleAnnotation("Get Message Reactions"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @, e.g. #general or @username_dm."),
			),
			mcp.WithString("timestamp",
				mcp.Required(),
				mcp.Description("Timestamp of the message to get reactions for, in format 1234567890.123456."),
			),
		), conversationsHandler.ReactionsGetHandler)
	}

	if shouldAddTool(ToolConversationsGetMessage, enabledTools, "") {
		s.AddTool(newDailyPowerTool(ToolConversationsGetMessage,
			mcp.WithDescription("Fetch a single message by channel and timestamp. Use the MsgID column from any compact CSV output as the timestamp, for example to re-fetch a message with detail: 'full' after an attachment-truncation receipt. Returns the same CSV format as conversations_history."),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @, e.g. #general or @username_dm."),
			),
			mcp.WithString("timestamp",
				mcp.Required(),
				mcp.Description("Timestamp of the message to fetch, in format 1234567890.123456."),
			),
			mcp.WithString("detail",
				mcp.Description(toolDetailDescription),
				mcp.Enum("standard", "full"),
			),
		), conversationsHandler.ConversationsGetMessageHandler)
	}

	if shouldAddTool(ToolConversationsAddMessage, enabledTools, "SLACK_MCP_ADD_MESSAGE_TOOL") {
		s.AddTool(mcp.NewTool(ToolConversationsAddMessage,
			mcp.WithDescription("Post a message to a channel or DM by channel_id. Pass thread_ts to reply in a thread."),
			mcp.WithTitleAnnotation("Send Message"),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @, e.g. #general or @username_dm."),
			),
			mcp.WithString("thread_ts",
				mcp.Description("Timestamp of an existing message, in format 1234567890.123456, identifying the thread to reply in. Omit to post to the channel itself."),
			),
			mcp.WithString("text",
				mcp.Description("Message text in specified content_type format. Example: 'Hello, world!' for text/plain or '# Hello, world!' for text/markdown."),
			),
			mcp.WithString("content_type",
				mcp.DefaultString("text/markdown"),
				mcp.Description("Content type of the message. Default is 'text/markdown'. Allowed values: 'text/markdown', 'text/plain'. Ignored when blocks is provided."),
			),
			mcp.WithString("blocks",
				mcp.Description("Raw Slack Block Kit JSON array for rich message formatting (rich_text lists, code blocks, etc.). When provided, this takes precedence over text/content_type for rendering. The text parameter becomes the notification fallback text."),
			),
		), conversationsHandler.ConversationsAddMessageHandler)
	}

	if shouldAddTool(ToolConversationsDraftMessage, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolConversationsDraftMessage,
			mcp.WithDescription("Preview a message without sending it. Returns the formatted message for review. Send it with conversations_add_message."),
			mcp.WithTitleAnnotation("Draft Message"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @, e.g. #general or @username_dm."),
			),
			mcp.WithString("thread_ts",
				mcp.Description("Timestamp of an existing message, in format 1234567890.123456, identifying the thread the draft replies to. Omit to draft for the channel itself."),
			),
			mcp.WithString("text",
				mcp.Required(),
				mcp.Description("Message text in specified content_type format. Example: 'Hello, world!' for text/plain or '# Hello, world!' for text/markdown."),
			),
			mcp.WithString("content_type",
				mcp.DefaultString("text/markdown"),
				mcp.Description("Content type of the message. Default is 'text/markdown'. Allowed values: 'text/markdown', 'text/plain'."),
			),
		), conversationsHandler.ConversationsDraftMessageHandler)
	}

	if shouldAddTool(ToolReactionsAdd, enabledTools, "SLACK_MCP_REACTION_TOOL") {
		s.AddTool(mcp.NewTool(ToolReactionsAdd,
			mcp.WithDescription("Add an emoji reaction to a message."),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @, e.g. #general or @username_dm."),
			),
			mcp.WithString("timestamp",
				mcp.Required(),
				mcp.Description("Timestamp of the message to add reaction to, in format 1234567890.123456."),
			),
			mcp.WithString("emoji",
				mcp.Required(),
				mcp.Description("Emoji name without colons, e.g. 'thumbsup', 'heart', 'rocket'."),
			),
		), conversationsHandler.ReactionsAddHandler)
	}

	if shouldAddTool(ToolReactionsRemove, enabledTools, "SLACK_MCP_REACTION_TOOL") {
		s.AddTool(newDailyPowerTool(ToolReactionsRemove,
			mcp.WithDescription("Remove an emoji reaction from a message."),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @, e.g. #general or @username_dm."),
			),
			mcp.WithString("timestamp",
				mcp.Required(),
				mcp.Description("Timestamp of the message to remove reaction from, in format 1234567890.123456."),
			),
			mcp.WithString("emoji",
				mcp.Required(),
				mcp.Description("Emoji name without colons, e.g. 'thumbsup', 'heart', 'rocket'."),
			),
		), conversationsHandler.ReactionsRemoveHandler)
	}

	if shouldAddTool(ToolAttachmentGetData, enabledTools, "SLACK_MCP_ATTACHMENT_TOOL") {
		s.AddTool(mcp.NewTool(ToolAttachmentGetData,
			mcp.WithDescription("Download an attachment's content by file ID. Returns file metadata and content (text files as-is, binary files as base64). Maximum file size is 5MB."),
			mcp.WithTitleAnnotation("Get Attachment Data"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("file_id",
				mcp.Required(),
				mcp.Description("Attachment ID in format Fxxxxxxxxxx. The AttachmentIDs field of message metadata lists IDs with filenames when FileCount > 0."),
			),
		), conversationsHandler.FilesGetHandler)
	}
	// Bot tokens cannot use search.messages.
	if !provider.IsBotToken() && shouldAddTool(ToolConversationsSearchMessages, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolConversationsSearchMessages,
			mcp.WithDescription("Search messages across channels and DMs. All filters are optional. If no filter is set, search_query is required."),
			mcp.WithTitleAnnotation("Search Messages"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("search_query",
				mcp.Description("Search query, e.g. 'marketing report'. A full Slack message URL such as 'https://slack.com/archives/C1234567890/p1234567890123456' returns that single message and ignores all other parameters."),
			),
			mcp.WithString("filter_in_channel",
				mcp.Description("Limit search to one public or private channel by ID or name, e.g. 'C1234567890', 'G1234567890', or '#general'. Omit to search all channels."),
			),
			mcp.WithString("filter_in_im_or_mpim",
				mcp.Description("Limit search to one DM or multi-person DM by ID or name, e.g. 'D1234567890' or '@username_dm'. Omit to search all DMs and MPIMs."),
			),
			mcp.WithString("filter_users_with",
				mcp.Description("Only messages in threads and DMs with a specific user, by ID or display name, e.g. 'U1234567890' or '@username'. Omit to search all threads and DMs."),
			),
			mcp.WithString("filter_users_from",
				mcp.Description("Only messages sent by a specific user, by ID or display name, e.g. 'U1234567890' or '@username'. Omit to search all users."),
			),
			mcp.WithString("filter_date_before",
				mcp.Description("Only messages sent before a date. Accepts 'YYYY-MM-DD' (e.g. '2023-10-01'), 'July', 'Yesterday', or 'Today'."),
			),
			mcp.WithString("filter_date_after",
				mcp.Description("Only messages sent after a date. Accepts 'YYYY-MM-DD' (e.g. '2023-10-01'), 'July', 'Yesterday', or 'Today'."),
			),
			mcp.WithString("filter_date_on",
				mcp.Description("Only messages sent on a specific date. Accepts 'YYYY-MM-DD' (e.g. '2023-10-01'), 'July', 'Yesterday', or 'Today'."),
			),
			mcp.WithString("filter_date_during",
				mcp.Description("Only messages sent during a named period, e.g. 'July', 'Yesterday', or 'Today'."),
			),
			mcp.WithBoolean("filter_threads_only",
				mcp.Description("If true, return only messages from threads. Default is false."),
			),
			mcp.WithString("filter_has",
				mcp.Description("Only messages containing a given element: 'link', 'reaction', 'pin', 'file', or an emoji name like ':eyes:'. Maps to Slack's has: search modifier."),
			),
			mcp.WithString("cursor",
				mcp.DefaultString(""),
				mcp.Description("Pagination cursor. Pass the cursor value from the last row of the previous response."),
			),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(100),
				mcp.Description("Maximum number of results, an integer between 1 and 100. Default is 100."),
			),
			mcp.WithString("sort",
				mcp.DefaultString("score"),
				mcp.Description("Sort order: 'score' (default, relevance) or 'timestamp' (most recent first)."),
			),
			mcp.WithString("detail",
				mcp.Description(toolDetailDescription),
				mcp.Enum("standard", "full"),
			),
		), conversationsHandler.ConversationsSearchHandler)
	}

	if shouldAddTool(ToolUsersSearch, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolUsersSearch,
			mcp.WithDescription("Search for users by name, email, display name, or Slack user ID. If a Slack user ID is provided (e.g. U07VCEPP4N5), the user is looked up directly. Returns user details and DM channel ID if available."),
			mcp.WithTitleAnnotation("Search Users"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("query",
				mcp.Required(),
				mcp.Description("Search query. Matches against real name, display name, username, email, or a Slack user ID (e.g. U07VCEPP4N5)."),
			),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(10),
				mcp.Description("Maximum number of results to return (1-100). Default is 10."),
			),
		), conversationsHandler.UsersSearchHandler)
	}

	if shouldAddTool(ToolFilesList, enabledTools, "SLACK_MCP_FILES_LIST_TOOL") {
		s.AddTool(mcp.NewTool(ToolFilesList,
			mcp.WithDescription("List files shared in a Slack channel or workspace. Returns file metadata including ID, name, type, size, uploader, and permalink. Pass a file ID from the results to attachment_get_data to download content. The cursor column of the last row paginates."),
			mcp.WithTitleAnnotation("List Files"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Description("Filter files by channel ID (format Cxxxxxxxxxx) or channel name (e.g. #general). If omitted, lists files across all channels."),
			),
			mcp.WithString("user_id",
				mcp.Description("Filter files uploaded by a specific user ID (format Uxxxxxxxxxx)."),
			),
			mcp.WithString("types",
				mcp.DefaultString("all"),
				mcp.Description("Filter by file type. Comma-separated values: all, spaces, snippets, images, gdocs, zips, pdfs. Default is all."),
			),
			mcp.WithString("limit",
				mcp.DefaultString("50"),
				mcp.Description("Maximum number of files to return (1-200). Default is 50."),
			),
			mcp.WithString("cursor",
				mcp.Description("Pagination cursor. Pass the cursor value from the last row of the previous response."),
			),
		), conversationsHandler.FilesListHandler)
	}
	if shouldAddTool(ToolConversationsMark, enabledTools, "SLACK_MCP_MARK_TOOL") {
		s.AddTool(newDailyPowerTool(ToolConversationsMark,
			mcp.WithDescription("Mark a channel or DM as read. If no timestamp is provided, marks all messages as read."),
			mcp.WithTitleAnnotation("Mark as Read"),
			mcp.WithDestructiveHintAnnotation(false),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @ (e.g. #general, @username)."),
			),
			mcp.WithString("ts",
				mcp.Description("Timestamp of the message to mark as read up to. If not provided, marks all messages as read."),
			),
		), conversationsHandler.ConversationsMarkHandler)
	}

	if shouldAddTool(ToolConversationsLeave, enabledTools, "SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL") {
		s.AddTool(mcp.NewTool(ToolConversationsLeave,
			mcp.WithDescription("Leave a channel, group conversation, or DM. Cannot leave the #general channel."),
			mcp.WithTitleAnnotation("Leave Channel"),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel or DM name starting with # or @ (e.g. #general, @username)."),
			),
		), conversationsHandler.ConversationsLeaveHandler)
	}

	if shouldAddTool(ToolConversationsJoin, enabledTools, "SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL") {
		s.AddTool(mcp.NewTool(ToolConversationsJoin,
			mcp.WithDescription("Join a public channel. Use channels_list or channels_me to find channel IDs."),
			mcp.WithTitleAnnotation("Join Channel"),
			mcp.WithIdempotentHintAnnotation(true),
			mcp.WithString("channel_id",
				mcp.Required(),
				mcp.Description("Channel ID in format Cxxxxxxxxxx, or a channel name starting with # (e.g. #general)."),
			),
		), conversationsHandler.ConversationsJoinHandler)
	}

	usergroupsHandler := handler.NewUsergroupsHandler(provider, logger)

	if shouldAddTool(ToolUsergroupsList, enabledTools, "") {
		s.AddTool(newDailyPowerTool(ToolUsergroupsList,
			mcp.WithDescription("List all user groups (subteams) in the workspace. User groups are mention handles like @engineering that notify all members. Use this to find a group's ID before joining or updating it. Returns CSV with columns: id, name, handle, description, user_count, is_external, date_create, date_update; when include_users=true also users (semicolon-separated user IDs)."),
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
		), usergroupsHandler.UsergroupsListHandler)
	}

	if shouldAddTool(ToolUsergroupsMe, enabledTools, "") {
		s.AddTool(mcp.NewTool(ToolUsergroupsMe,
			mcp.WithDescription("Manage your own user group membership. action='list' shows the groups you belong to, 'join' adds you to a group, 'leave' removes you. Unlike usergroups_users_update, this needs no full member list."),
			mcp.WithTitleAnnotation("My User Groups"),
			mcp.WithString("action",
				mcp.Required(),
				mcp.Description("Action to perform: 'list' returns CSV of groups you're a member of, 'join' adds you to a group, 'leave' removes you from a group."),
			),
			mcp.WithString("usergroup_id",
				mcp.Description("ID of the user group (starts with 'S', e.g., 'S0123456789'). Required for 'join' and 'leave' actions. Get IDs from usergroups_list."),
			),
		), usergroupsHandler.UsergroupsMeHandler)
	}

	if shouldAddTool(ToolUsergroupsMine, enabledTools, "") {
		s.AddTool(newDailyPowerTool(ToolUsergroupsMine,
			mcp.WithDescription("List the user groups the authenticated Slack user belongs to."),
		), usergroupsHandler.UsergroupsMineHandler)
	}

	if shouldAddTool(ToolUsergroupsJoin, enabledTools, "SLACK_MCP_USERGROUPS_WRITE_TOOL") {
		s.AddTool(newDailyPowerTool(ToolUsergroupsJoin,
			mcp.WithDescription("Add the authenticated Slack user to one user group. Requires client confirmation."),
			mcp.WithString("usergroup_id", mcp.Required(), mcp.Description("User group ID beginning with S.")),
		), usergroupsHandler.UsergroupsJoinHandler)
	}

	if shouldAddTool(ToolUsergroupsLeave, enabledTools, "SLACK_MCP_USERGROUPS_WRITE_TOOL") {
		s.AddTool(newDailyPowerTool(ToolUsergroupsLeave,
			mcp.WithDescription("Remove the authenticated Slack user from one user group. Requires client confirmation."),
			mcp.WithString("usergroup_id", mcp.Required(), mcp.Description("User group ID beginning with S.")),
		), usergroupsHandler.UsergroupsLeaveHandler)
	}

	if shouldAddTool(ToolUsergroupsCreate, enabledTools, "SLACK_MCP_USERGROUPS_WRITE_TOOL") {
		s.AddTool(newDailyPowerTool(ToolUsergroupsCreate,
			mcp.WithDescription("Create a new user group (mention group) in the Slack workspace. After creation, use usergroups_users_update to add members, or users can join themselves with usergroups_me. The handle becomes the @mention (e.g., handle='engineering' creates @engineering)."),
			mcp.WithTitleAnnotation("Create User Group"),
			mcp.WithDestructiveHintAnnotation(true),
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
		), usergroupsHandler.UsergroupsCreateHandler)
	}

	if shouldAddTool(ToolUsergroupsUpdate, enabledTools, "SLACK_MCP_USERGROUPS_WRITE_TOOL") {
		s.AddTool(newDailyPowerTool(ToolUsergroupsUpdate,
			mcp.WithDescription("Update a user group's metadata: name, handle (@mention), description, or default channels. Does NOT change members, use usergroups_users_update for that. At least one field must be provided."),
			mcp.WithTitleAnnotation("Update User Group"),
			mcp.WithDestructiveHintAnnotation(true),
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
		), usergroupsHandler.UsergroupsUpdateHandler)
	}

	if shouldAddTool(ToolUsergroupsUsersUpdate, enabledTools, "SLACK_MCP_USERGROUPS_WRITE_TOOL") {
		s.AddTool(newDailyPowerTool(ToolUsergroupsUsersUpdate,
			mcp.WithDescription("Replace all members of a user group with a new list. WARNING: any user not in the 'users' parameter is removed. To add or remove only yourself, use usergroups_me instead. To add one user without removing others, first get current members from usergroups_list with include_users=true, then call this with the combined list."),
			mcp.WithTitleAnnotation("Update User Group Members"),
			mcp.WithDestructiveHintAnnotation(true),
			mcp.WithString("usergroup_id",
				mcp.Required(),
				mcp.Description("ID of the user group (starts with 'S', e.g., 'S0123456789'). Get IDs from usergroups_list."),
			),
			mcp.WithString("users",
				mcp.Required(),
				mcp.Description("Comma-separated user IDs forming the COMPLETE new member list (e.g., 'U0123456789,U9876543210'). Current members not in this list are removed."),
			),
		), usergroupsHandler.UsergroupsUsersUpdateHandler)
	}

	browserSession := provider.ConfiguredWithBrowserSession()
	addSavedList := browserSession && shouldAddTool(ToolSavedList, enabledTools, "")
	addSavedUpdate := browserSession && shouldAddTool(ToolSavedUpdate, enabledTools, "SLACK_MCP_SAVED_WRITE_TOOL")
	addSavedClear := browserSession && shouldAddTool(ToolSavedClearCompleted, enabledTools, "SLACK_MCP_SAVED_WRITE_TOOL")
	if addSavedList || addSavedUpdate || addSavedClear {
		savedHandler := handler.NewSavedHandler(provider, logger, conversationsHandler)
		if addSavedList {
			s.AddTool(newDailyPowerTool(ToolSavedList,
				mcp.WithDescription("List saved items from Slack's 'Save for Later' panel. Returns items the user has saved, with optional message content. Replaces the deprecated stars.list API. Requires browser session tokens (xoxc/xoxd)."),
				mcp.WithString("filter",
					mcp.Description("Filter saved items: 'saved' (active/in-progress, default), 'completed' (marked done), 'archived'."),
					mcp.DefaultString("saved"),
				),
				mcp.WithNumber("limit",
					mcp.Description("Maximum number of items to return from this page. Default is 50."),
					mcp.DefaultNumber(50),
				),
				mcp.WithString("cursor",
					mcp.Description("Pagination cursor from meta.next_cursor in the previous response."),
				),
				mcp.WithBoolean("include_messages",
					mcp.Description("If true (default), fetches the actual saved message content. If false, returns metadata only."),
					mcp.DefaultBool(true),
				),
				mcp.WithNumber("max_messages_per_item",
					mcp.Description("Max messages to fetch per saved item (for thread replies). Default is 5."),
					mcp.DefaultNumber(5),
				),
				mcp.WithString("detail",
					mcp.Description(toolDetailDescription),
					mcp.Enum("standard", "full"),
				),
			), savedHandler.SavedListHandler)
		}

		if addSavedUpdate {
			s.AddTool(newDailyPowerTool(ToolSavedUpdate,
				mcp.WithDescription("Update a saved item: mark as completed, set a due date, or both. Use item_id and ts values from saved_list output. Replaces the deprecated stars.add/stars.remove APIs."),
				mcp.WithTitleAnnotation("Update Saved Item"),
				mcp.WithDestructiveHintAnnotation(true),
				mcp.WithString("item_id",
					mcp.Required(),
					mcp.Description("Channel/DM ID where the saved message lives (from saved_list output)."),
				),
				mcp.WithString("ts",
					mcp.Required(),
					mcp.Description("Message timestamp of the saved item (from saved_list output)."),
				),
				mcp.WithString("mark",
					mcp.Description("Set to 'completed' to mark the item as done."),
				),
				mcp.WithNumber("date_due",
					mcp.Description("Unix timestamp for due date/reminder. Set to 0 to clear."),
				),
			), savedHandler.SavedUpdateHandler)
		}

		if addSavedClear {
			s.AddTool(newDailyPowerTool(ToolSavedClearCompleted,
				mcp.WithDescription("Clear all completed saved items from the 'Save for Later' panel. This is a bulk operation that removes all items with state='completed'."),
				mcp.WithTitleAnnotation("Clear Completed Saved Items"),
				mcp.WithDestructiveHintAnnotation(true),
			), savedHandler.SavedClearCompletedHandler)
		}
	}

	logger.Info("Authenticating with Slack API...",
		zap.String("context", "console"),
	)
	ar, err := provider.Slack().AuthTest()
	if err != nil {
		logger.Fatal("Failed to authenticate with Slack",
			zap.String("context", "console"),
			zap.Error(err),
		)
	}

	logger.Info("Successfully authenticated with Slack",
		zap.String("context", "console"),
		zap.String("team", ar.Team),
		zap.String("user", ar.User),
		zap.String("enterprise", ar.EnterpriseID),
		zap.String("url", ar.URL),
	)

	ws, err := text.Workspace(ar.URL)
	if err != nil {
		logger.Fatal("Failed to parse workspace from URL",
			zap.String("context", "console"),
			zap.String("url", ar.URL),
			zap.Error(err),
		)
	}

	registerDailyPowerLifecycleTools(s, provider, logger, enabledTools)

	return &MCPServer{
		server:       s,
		logger:       logger,
		workspace:    ws,
		provider:     provider,
		enabledTools: enabledTools,
	}
}

// RegisterCacheDependentTools registers tools and resources that require the cache to be ready.
// Called after cache warm-up completes. The mcp-go server automatically sends
// notifications/tools/list_changed to connected clients when AddTool is called.
func (s *MCPServer) RegisterCacheDependentTools() {
	s.cacheToolsOnce.Do(s.registerCacheDependentTools)
}

func (s *MCPServer) registerCacheDependentTools() {
	provider := s.provider
	logger := s.logger
	enabledTools := s.enabledTools
	browserSession := provider.ConfiguredWithBrowserSession()

	conversationsHandler := handler.NewConversationsHandler(provider, logger)
	channelsHandler := handler.NewChannelsHandler(provider, logger)

	if shouldAddTool(ToolChannelsList, enabledTools, "") {
		guardCacheDependentRegistration(ToolChannelsList)
		s.server.AddTool(mcp.NewTool(ToolChannelsList,
			mcp.WithDescription("List channels in the workspace, filtered by channel_types. Returns CSV; the cursor value in the last row paginates."),
			mcp.WithTitleAnnotation("List Channels"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("channel_types",
				mcp.Required(),
				mcp.Description("Comma-separated channel types. Allowed values: 'mpim', 'im', 'public_channel', 'private_channel'. Example: 'public_channel,private_channel,im'"),
			),
			mcp.WithString("sort",
				mcp.Description("Sort order. Allowed value: 'popularity' sorts by number of members in each channel."),
			),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(100),
				mcp.Description("Maximum number of items to return, an integer between 1 and 999. Default is 100."),
			),
			mcp.WithString("cursor",
				mcp.Description("Pagination cursor. Pass the cursor value from the last row of the previous response."),
			),
			mcp.WithString("query",
				mcp.Description("Optional keyword to filter channels. Case-insensitive substring match against the fields specified by query_targets. Example: 'marketing' returns channels like #marketing, #marketing-ops."),
			),
			mcp.WithString("query_targets",
				mcp.DefaultString("name"),
				mcp.Description("Comma-separated list of fields to match the query against. Allowed values: 'name', 'topic', 'purpose'. Example: 'name,topic,purpose' to search all fields. Default is 'name'."),
			),
		), channelsHandler.ChannelsHandler)
	}

	if shouldAddTool(ToolChannelsMe, enabledTools, "") {
		guardCacheDependentRegistration(ToolChannelsMe)
		s.server.AddTool(mcp.NewTool(ToolChannelsMe,
			mcp.WithDescription("List only the channels you have joined, unlike channels_list which returns all workspace channels. Useful on large workspaces where channels_list returns thousands of results."),
			mcp.WithTitleAnnotation("My Channels"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("channel_types",
				mcp.Description("Comma-separated channel types. Allowed values: 'mpim', 'im', 'public_channel', 'private_channel'. Default: 'public_channel,private_channel'."),
			),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(100),
				mcp.Description("Maximum number of items to return (1-999)."),
			),
			mcp.WithString("cursor",
				mcp.Description("Cursor for pagination."),
			),
		), channelsHandler.ChannelsMeHandler)
	}

	if !provider.IsBotToken() && shouldAddTool(ToolChannelsStarred, enabledTools, "") {
		guardCacheDependentRegistration(ToolChannelsStarred)
		s.server.AddTool(mcp.NewTool(ToolChannelsStarred,
			mcp.WithDescription("List channels and DMs the user has starred (bookmarked). Returns only that subset, not the full channel list."),
			mcp.WithTitleAnnotation("List Starred Channels"),
			mcp.WithReadOnlyHintAnnotation(true),
			mcp.WithString("channel_types",
				mcp.Description("Filter by channel type: 'all' (default), 'dm' (direct messages), 'group_dm' (group DMs), 'partner' (ext-shared channels), 'internal' (regular workspace channels)."),
				mcp.DefaultString("all"),
			),
			mcp.WithNumber("limit",
				mcp.DefaultNumber(100),
				mcp.Description("Maximum number of starred channels to return (1-1000). Default is 100."),
			),
		), channelsHandler.ChannelsStarredHandler)
	}

	if browserSession && shouldAddTool(ToolConversationsUnreads, enabledTools, "") {
		guardCacheDependentRegistration(ToolConversationsUnreads)
		s.server.AddTool(newDailyPowerTool(ToolConversationsUnreads,
			mcp.WithDescription("Get unread messages across all channels using Slack browser session state (xoxc/xoxd). Requires a healthy browser session. Results are prioritized: DMs, then group DMs, then partner channels, then internal channels."),
			mcp.WithBoolean("include_messages",
				mcp.Description("If true (default), returns the actual unread messages. If false, returns only a summary of channels with unreads."),
				mcp.DefaultBool(true),
			),
			mcp.WithString("channel_types",
				mcp.Description("Filter by channel type: 'all' (default), 'dm' (direct messages), 'group_dm' (group DMs), 'partner' (ext-* channels), 'internal' (other channels)."),
				mcp.DefaultString("all"),
			),
			mcp.WithNumber("max_channels",
				mcp.Description("Maximum number of channels to fetch unreads from. Default is 50."),
				mcp.DefaultNumber(50),
			),
			mcp.WithNumber("max_messages_per_channel",
				mcp.Description("Maximum messages to fetch per channel. Default is 10."),
				mcp.DefaultNumber(10),
			),
			mcp.WithBoolean("mentions_only",
				mcp.Description("If true, only returns channels where you have @mentions. Default is false."),
				mcp.DefaultBool(false),
			),
			mcp.WithBoolean("include_muted",
				mcp.Description("If true, includes muted channels in results. Default is false (muted channels are excluded, matching Slack app behavior)."),
				mcp.DefaultBool(false),
			),
			mcp.WithString("detail",
				mcp.Description(toolDetailDescription),
				mcp.Enum("standard", "full"),
			),
		), conversationsHandler.ConversationsUnreadsHandler)
	}

	addActivityUnreads := browserSession && shouldAddTool(ToolActivityUnreads, enabledTools, "")
	addActivityMarkRead := browserSession && shouldAddTool(ToolActivityMarkRead, enabledTools, "SLACK_MCP_ACTIVITY_MARK_TOOL")
	if addActivityUnreads || addActivityMarkRead {
		activityHandler := handler.NewActivityHandler(provider, logger, conversationsHandler)
		if addActivityUnreads {
			guardCacheDependentRegistration(ToolActivityUnreads)
			s.server.AddTool(newDailyPowerTool(ToolActivityUnreads,
				mcp.WithDescription("Get unread Activity items (thread replies and @mentions). Returns the same data as Slack's Activity panel Unreads tab. Requires browser session tokens (xoxc/xoxd)."),
				mcp.WithBoolean("include_messages",
					mcp.Description("If true (default), fetches unread reply messages per thread. If false, returns summary only."),
					mcp.DefaultBool(true),
				),
				mcp.WithNumber("max_messages_per_thread",
					mcp.Description("Max messages to fetch per thread when include_messages is true. Default is 10."),
					mcp.DefaultNumber(10),
				),
				mcp.WithNumber("limit",
					mcp.Description("Max Activity items to return. Default is 30."),
					mcp.DefaultNumber(30),
				),
				mcp.WithString("detail",
					mcp.Description(toolDetailDescription),
					mcp.Enum("standard", "full"),
				),
			), activityHandler.ActivityUnreadsHandler)
		}

		if addActivityMarkRead {
			guardCacheDependentRegistration(ToolActivityMarkRead)
			s.server.AddTool(newDailyPowerTool(ToolActivityMarkRead,
				mcp.WithDescription("Mark an Activity item as read. Use the key, feed_ts, and type values from activity_unreads output."),
				mcp.WithTitleAnnotation("Mark Activity Read"),
				mcp.WithString("key",
					mcp.Description("Activity item key from activity_unreads output, e.g. 'thread_v2-C092WJP9Z38-1772545632.256259'."),
					mcp.Required(),
				),
				mcp.WithString("feed_ts",
					mcp.Description("Feed timestamp from activity_unreads output."),
					mcp.Required(),
				),
				mcp.WithString("type",
					mcp.Description("Item type from activity_unreads output: thread_v2, at_user, at_user_group, at_channel, at_everyone."),
					mcp.Required(),
				),
			), activityHandler.ActivityMarkReadHandler)
		}
	}

	s.server.AddResource(mcp.NewResource(
		"slack://"+s.workspace+"/channels",
		"Directory of Slack channels",
		mcp.WithResourceDescription("CSV directory of Slack channels."),
		mcp.WithMIMEType("text/csv"),
	), channelsHandler.ChannelsResource)

	s.server.AddResource(mcp.NewResource(
		"slack://"+s.workspace+"/users",
		"Directory of Slack users",
		mcp.WithResourceDescription("CSV directory of Slack users."),
		mcp.WithMIMEType("text/csv"),
	), conversationsHandler.UsersResource)

	logger.Info("Cache-dependent tools and resources registered",
		zap.String("context", "console"),
	)
}

func (s *MCPServer) ServeSSE(addr string) *server.SSEServer {
	s.logger.Info("Creating SSE server",
		zap.String("context", "console"),
		zap.String("version", version.Version),
		zap.String("build_time", version.BuildTime),
		zap.String("commit_hash", version.CommitHash),
		zap.String("address", addr),
	)
	return server.NewSSEServer(s.server,
		server.WithBaseURL(fmt.Sprintf("http://%s", addr)),
		server.WithSSEContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			ctx = auth.AuthFromRequest(s.logger)(ctx, r)

			return ctx
		}),
	)
}

func (s *MCPServer) ServeHTTP(addr string) *server.StreamableHTTPServer {
	s.logger.Info("Creating HTTP server",
		zap.String("context", "console"),
		zap.String("version", version.Version),
		zap.String("build_time", version.BuildTime),
		zap.String("commit_hash", version.CommitHash),
		zap.String("address", addr),
	)
	return server.NewStreamableHTTPServer(s.server,
		server.WithEndpointPath("/mcp"),
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			ctx = auth.AuthFromRequest(s.logger)(ctx, r)

			return ctx
		}),
	)
}

func (s *MCPServer) ServeStdio() error {
	s.logger.Info("Starting STDIO server",
		zap.String("version", version.Version),
		zap.String("build_time", version.BuildTime),
		zap.String("commit_hash", version.CommitHash),
	)
	err := server.ServeStdio(s.server)
	if err != nil {
		s.logger.Error("STDIO server error", zap.Error(err))
	}
	return err
}

// buildErrorRecoveryMiddleware converts tool handler errors into MCP tool results
// with isError=true, allowing LLMs to see the error and retry with different parameters.
// Without this, errors become JSON-RPC -32603 protocol errors that crash MCP clients.
func buildErrorRecoveryMiddleware(logger *zap.Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := next(ctx, req)
			if err != nil {
				logger.Warn("Tool call returned error, converting to isError tool result",
					zap.String("tool", req.Params.Name),
					zap.Error(err),
				)
				return handler.NewTypedErrorResult(err), nil
			}
			return res, nil
		}
	}
}

func buildLoggerMiddleware(logger *zap.Logger) server.ToolHandlerMiddleware {
	logParams := strings.EqualFold(os.Getenv("SLACK_MCP_LOG_PARAMS"), "debug")
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			logger.Info("Request received",
				zap.String("tool", req.Params.Name),
			)
			if logParams {
				logger.Info("Request params",
					zap.String("tool", req.Params.Name),
					zap.Any("params", req.Params),
				)
			}

			startTime := time.Now()

			res, err := next(ctx, req)

			duration := time.Since(startTime)

			logger.Info("Request finished",
				zap.String("tool", req.Params.Name),
				zap.Duration("duration", duration),
			)

			return res, err
		}
	}
}
