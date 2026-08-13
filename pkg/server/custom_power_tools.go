package server

import (
	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

func registerCustomPowerTools(s *mcpserver.MCPServer, api *provider.ApiProvider, logger *zap.Logger, enabled []string, approvals *approval.Store) {
	if service, err := api.MessageFiles(); err == nil {
		h := handler.NewMessageFilesHandler(service, approvals, api.Identity, logger)
		add := func(name, gate, description string, options []mcp.ToolOption, fn mcpserver.ToolHandlerFunc) {
			addEnabledTool(s, enabled, name, gate, newDailyPowerTool(name, append([]mcp.ToolOption{mcp.WithDescription(description)}, options...)...), fn)
		}
		add(ToolFilesUpload, "SLACK_MCP_FILE_UPLOAD_TOOL", "Upload a file to Slack using Slack's supported external-upload flow.", []mcp.ToolOption{
			mcp.WithString("filename", mcp.Required()), mcp.WithString("content_base64"), mcp.WithString("content"),
			mcp.WithString("title"), mcp.WithString("channel_id"), mcp.WithString("initial_comment"),
			mcp.WithString("thread_ts"), mcp.WithString("alt_text"), mcp.WithString("snippet_type"),
		}, h.FilesUpload)
		add(ToolMessagesSchedule, "SLACK_MCP_ADD_MESSAGE_TOOL", "Schedule a Slack message for a future time.", []mcp.ToolOption{
			mcp.WithString("channel_id", mcp.Required()), mcp.WithString("text", mcp.Required()), mcp.WithString("post_at", mcp.Required()), mcp.WithString("thread_ts"),
		}, h.MessagesSchedule)
		add(ToolMessagesUpdate, "SLACK_MCP_ADD_MESSAGE_TOOL", "Edit a Slack message that the authenticated user owns.", []mcp.ToolOption{
			mcp.WithString("channel_id", mcp.Required()), mcp.WithString("timestamp", mcp.Required()), mcp.WithString("text", mcp.Required()),
		}, h.MessagesUpdate)
		add(ToolMessagesDelete, "SLACK_MCP_ADD_MESSAGE_TOOL", "Preview or delete a Slack message. Prepare first, then execute with the approval token.", []mcp.ToolOption{
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute")), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("timestamp", mcp.Required()), mcp.WithString("approval_token"),
		}, h.MessagesDelete)
	} else {
		logger.Warn("File and message mutation tools unavailable", zap.Error(err))
	}

	if service, err := api.PeopleChannels(); err == nil {
		h := handler.NewPeopleChannelsHandler(service, api.Identity, logger)
		addEnabledTool(s, enabled, ToolUsersGetProfile, "", newDailyPowerTool(ToolUsersGetProfile, mcp.WithDescription("Read a full Slack user profile."), mcp.WithString("user_id"), mcp.WithBoolean("include_labels")), h.GetUserProfile)

		addEnabledTool(s, enabled, ToolUsersSetProfile, "SLACK_MCP_PROFILE_WRITE_TOOL", newDailyPowerTool(ToolUsersSetProfile, mcp.WithDescription("Update the authenticated user's Slack profile."),
			mcp.WithString("first_name"), mcp.WithString("last_name"), mcp.WithString("real_name"), mcp.WithString("display_name"), mcp.WithString("pronouns"),
			mcp.WithString("email"), mcp.WithString("phone"), mcp.WithString("skype"), mcp.WithString("title"), mcp.WithString("start_date"), mcp.WithObject("custom_fields")), h.SetUserProfile)

		addEnabledTool(s, enabled, ToolUsersSetStatus, "SLACK_MCP_PROFILE_WRITE_TOOL", newDailyPowerTool(ToolUsersSetStatus, mcp.WithDescription("Set or clear the authenticated user's Slack status."), mcp.WithString("status_text", mcp.Required()), mcp.WithString("status_emoji", mcp.Required()), mcp.WithNumber("status_expiration")), h.SetUserStatus)

		addEnabledTool(s, enabled, ToolEmojiList, "", newDailyPowerTool(ToolEmojiList, mcp.WithDescription("List and search workspace custom emoji."), mcp.WithString("query"), mcp.WithString("cursor"), mcp.WithNumber("limit")), h.ListEmoji)

		addEnabledTool(s, enabled, ToolChannelsCreate, "SLACK_MCP_CHANNEL_CREATE_TOOL", newDailyPowerTool(ToolChannelsCreate, mcp.WithDescription("Create a public or private Slack channel."), mcp.WithString("name", mcp.Required()), mcp.WithBoolean("is_private"), mcp.WithString("team_id")), h.CreateChannel)

		addEnabledTool(s, enabled, ToolChannelsMembers, "", newDailyPowerTool(ToolChannelsMembers, mcp.WithDescription("List channel member user IDs."), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("cursor"), mcp.WithNumber("limit")), h.ListChannelMembers)

		addEnabledTool(s, enabled, ToolChannelsInvite, "SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL", newDailyPowerTool(ToolChannelsInvite, mcp.WithDescription("Invite users to a Slack channel."), mcp.WithString("channel_id", mcp.Required()), mcp.WithArray("user_ids", mcp.Required(), mcp.Items(map[string]any{"type": "string"}))), h.InviteChannelMembers)

	} else {
		logger.Warn("People and channel tools unavailable", zap.Error(err))
	}

	if service, err := api.Canvases(); err == nil {
		h := handler.NewCanvasHandler(service, logger)
		addEnabledTool(s, enabled, ToolCanvasesCreate, "SLACK_MCP_CANVAS_WRITE_TOOL", newDailyPowerTool(ToolCanvasesCreate, mcp.WithDescription("Create a Slack canvas from Markdown."), mcp.WithString("title"), mcp.WithString("markdown")), h.Create)

		addEnabledTool(s, enabled, ToolCanvasesRead, "", newDailyPowerTool(ToolCanvasesRead, mcp.WithDescription("Read canvas metadata, preview, and matching section IDs. Slack does not expose a full public canvas export."), mcp.WithString("canvas_id", mcp.Required()), mcp.WithArray("section_types", mcp.Items(map[string]any{"type": "string"})), mcp.WithString("contains_text")), h.Read)

		change := map[string]any{"type": "object", "required": []string{"operation", "markdown"}, "properties": map[string]any{"operation": map[string]any{"type": "string", "enum": []string{"insert_at_start", "insert_at_end", "insert_before", "insert_after", "replace"}}, "section_id": map[string]any{"type": "string"}, "markdown": map[string]any{"type": "string"}}, "additionalProperties": false}
		addEnabledTool(s, enabled, ToolCanvasesUpdate, "SLACK_MCP_CANVAS_WRITE_TOOL", newDailyPowerTool(ToolCanvasesUpdate, mcp.WithDescription("Apply exactly one Markdown edit to a Slack canvas."), mcp.WithString("canvas_id", mcp.Required()), mcp.WithArray("changes", mcp.Required(), mcp.MinItems(1), mcp.MaxItems(1), mcp.Items(change))), h.Update)
	}

	drafts := handler.NewDraftsHandler(api.Drafts(), approvals, api.Identity, logger)
	addEnabledTool(s, enabled, ToolDraftsList, "", newDailyPowerTool(ToolDraftsList, mcp.WithDescription("List persisted Slack drafts from the authenticated browser session."), mcp.WithString("cursor"), mcp.WithNumber("limit")), drafts.List)

	addEnabledTool(s, enabled, ToolDraftsGet, "", newDailyPowerTool(ToolDraftsGet, mcp.WithDescription("Read one persisted Slack draft."), mcp.WithString("draft_id", mcp.Required())), drafts.Get)

	addEnabledTool(s, enabled, ToolDraftsCreate, "SLACK_MCP_DRAFT_WRITE_TOOL", newDailyPowerTool(ToolDraftsCreate, mcp.WithDescription("Create a persisted Slack draft without sending it."), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("text", mcp.Required()), mcp.WithString("thread_ts")), drafts.Create)

	addEnabledTool(s, enabled, ToolDraftsUpdate, "SLACK_MCP_DRAFT_WRITE_TOOL", newDailyPowerTool(ToolDraftsUpdate, mcp.WithDescription("Update a persisted Slack draft without sending it."), mcp.WithString("id", mcp.Required()), mcp.WithString("channel_id", mcp.Required()), mcp.WithString("text", mcp.Required()), mcp.WithString("thread_ts")), drafts.Update)

	addEnabledTool(s, enabled, ToolDraftsDelete, "SLACK_MCP_DRAFT_WRITE_TOOL", newDailyPowerTool(ToolDraftsDelete, mcp.WithDescription("Preview or delete a persisted Slack draft."), mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute")), mcp.WithString("draft_id", mcp.Required()), mcp.WithString("approval_token")), drafts.Delete)

	if service, err := api.SemanticSearch(); err == nil {
		h := handler.NewSemanticSearchHandler(service, logger)
		addEnabledTool(s, enabled, ToolSearchSemantic, "", newDailyPowerTool(ToolSearchSemantic, mcp.WithDescription("Search Slack messages and files semantically when Slack Real-time Search is enabled for the app."),
			mcp.WithString("query", mcp.Required()), mcp.WithArray("content_types", mcp.Items(map[string]any{"type": "string", "enum": []string{"messages", "files"}})),
			mcp.WithArray("channel_types", mcp.Items(map[string]any{"type": "string"})), mcp.WithString("context_channel_id"), mcp.WithString("cursor"), mcp.WithBoolean("include_bots"), mcp.WithNumber("limit")), h.Search)
	}
}
