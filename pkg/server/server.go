package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
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

// Tools exist iff SLACK_MCP_ENABLED_TOOLS (or the preset that fills it) names
// them. Naming a write tool is the consent; SLACK_MCP_ADD_MESSAGE_TOOL,
// SLACK_MCP_REACTION_TOOL and SLACK_MCP_CHANNEL_MANAGEMENT_TOOL only narrow
// those tools to channel allowlists inside the handlers.
func addEnabledTool(s *server.MCPServer, enabledTools []string, tool mcp.Tool, fn server.ToolHandlerFunc) {
	if slices.Contains(enabledTools, tool.Name) {
		s.AddTool(tool, fn)
	}
}

func addCacheDependentTool(ms *MCPServer, enabledTools []string, tool mcp.Tool, fn server.ToolHandlerFunc) {
	if slices.Contains(enabledTools, tool.Name) {
		guardCacheDependentRegistration(tool.Name)
		ms.server.AddTool(tool, fn)
	}
}

func NewMCPServer(provider *provider.ApiProvider, logger *zap.Logger, enabledTools []string) *MCPServer {
	opts := append([]server.ServerOption{
		server.WithLogging(),
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, true),
	}, toolHandlerOptions(provider.ServerTransport(), logger)...)
	s := server.NewMCPServer("Slack MCP Server", version.Version, opts...)

	registerCoreTools(s, provider, logger, enabledTools)

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

	addCacheDependentTool(s, enabledTools, mcp.NewTool(ToolChannelsList,
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

	addCacheDependentTool(s, enabledTools, mcp.NewTool(ToolChannelsMe,
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

	if !provider.IsBotToken() {
		addCacheDependentTool(s, enabledTools, mcp.NewTool(ToolChannelsStarred,
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

	if browserSession {
		addCacheDependentTool(s, enabledTools, newDailyPowerTool(ToolConversationsUnreads,
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

	addActivityUnreads := browserSession && slices.Contains(enabledTools, ToolActivityUnreads)
	addActivityMarkRead := browserSession && slices.Contains(enabledTools, ToolActivityMarkRead)
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
// toolHandlerOptions is the middleware stack every tool call runs through.
// mcp-go wraps middlewares in reverse registration order. Auth stays outermost
// so API-key failures remain protocol errors; WithRecovery sits innermost so a
// panic becomes an error that errorRecovery turns into an isError tool result
// instead of a JSON-RPC -32603.
func toolHandlerOptions(transport string, logger *zap.Logger) []server.ServerOption {
	return []server.ServerOption{
		server.WithToolHandlerMiddleware(auth.BuildMiddleware(transport, logger)),
		server.WithToolHandlerMiddleware(buildLoggerMiddleware(logger)),
		server.WithToolHandlerMiddleware(buildErrorRecoveryMiddleware(logger)),
		server.WithRecovery(),
	}
}

func buildErrorRecoveryMiddleware(logger *zap.Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := next(ctx, req)
			if err != nil {
				// A *handler.ToolError is an expected, typed outcome (bad
				// arguments, permission, rate limit); everything else is
				// a defect worth a warning.
				var typed *handler.ToolError
				level := logger.Warn
				if errors.As(err, &typed) {
					level = logger.Debug
				}
				level("Tool call returned error, converting to isError tool result",
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
