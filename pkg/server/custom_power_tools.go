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
		add := func(name, description string, options []mcp.ToolOption, fn mcpserver.ToolHandlerFunc) {
			addEnabledTool(s, enabled, newTool(name, append([]mcp.ToolOption{mcp.WithDescription(description)}, options...)...), fn)
		}
		add(ToolFilesUpload, "Upload a file to Slack using Slack's supported external-upload flow.", []mcp.ToolOption{
			mcp.WithString("filename", mcp.Required(), mcp.Description("File name including extension, e.g. report.csv; no directory path.")), mcp.WithString("content_base64", mcp.Description("Base64-encoded file bytes; use this or content, not both. Max 50 MiB decoded.")), mcp.WithString("content", mcp.Description("Plain-text file contents; use this or content_base64, not both. Max 50 MiB.")),
			mcp.WithString("title", mcp.Description("Title shown in Slack; defaults to filename.")), mcp.WithString("channel_id", mcp.Description("Channel ID (Cxxxxxxxxxx) to share the file into; omit to upload without sharing.")), mcp.WithString("initial_comment", mcp.Description("Message text posted with the file when sharing; up to 40000 characters.")),
			mcp.WithString("thread_ts", mcp.Description("Parent message timestamp (e.g. 1712345678.123456) to share into a thread; needs channel_id.")), mcp.WithString("alt_text", mcp.Description("Screen-reader description for image files; up to 1000 characters.")), mcp.WithString("snippet_type", mcp.Description("Syntax type for text snippets, e.g. python or javascript; unsupported types fail.")),
		}, h.FilesUpload)
		add(ToolMessagesSchedule, "Schedule a Slack message for a future time.", []mcp.ToolOption{
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) to post into; names are not resolved.")), mcp.WithString("text", mcp.Required(), mcp.Description("Message text, non-empty and up to 40000 characters.")), mcp.WithString("post_at", mcp.Required(), mcp.Description("Unix seconds or RFC 3339 time between 5 seconds and 120 days in the future.")), mcp.WithString("thread_ts", mcp.Description("Parent message timestamp (e.g. 1712345678.123456) to schedule a thread reply.")),
		}, h.MessagesSchedule)
		add(ToolMessagesUpdate, "Edit a Slack message that the authenticated user owns.", []mcp.ToolOption{
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) containing the message; names are not resolved.")), mcp.WithString("timestamp", mcp.Required(), mcp.Description("Slack message timestamp of the message to edit, e.g. 1712345678.123456.")), mcp.WithString("text", mcp.Required(), mcp.Description("Replacement message text, non-empty and up to 40000 characters.")),
		}, h.MessagesUpdate)
		add(ToolMessagesDelete, "Preview or delete a Slack message. Prepare first, then execute with the approval token.", []mcp.ToolOption{
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)), mcp.WithString("channel_id", mcp.Required(), mcp.Description("Channel ID (Cxxxxxxxxxx) containing the message; names are not resolved.")), mcp.WithString("timestamp", mcp.Required(), mcp.Description("Slack message timestamp of the message to delete, e.g. 1712345678.123456.")), mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
		}, h.MessagesDelete)
	} else {
		logger.Warn("File and message mutation tools unavailable", zap.Error(err))
	}

	if service, err := api.PeopleChannels(); err == nil {
		h := handler.NewPeopleChannelsHandler(service, api.Identity, logger)
		h.UserName = func(id string) string {
			u, ok := api.ProvideUsersMap().Users[id]
			if !ok {
				return ""
			}
			if u.RealName != "" {
				return u.RealName
			}
			return u.Name
		}
		addEnabledTool(s, enabled, newTool(ToolUsersGetProfile, mcp.WithDescription("Read a full Slack user profile."), mcp.WithString("user_id", mcp.Description("User ID starting with U or W; defaults to the authenticated user.")), mcp.WithBoolean("include_labels", mcp.Description("Include the display labels of custom profile fields; defaults to true."))), h.GetUserProfile)

		addEnabledTool(s, enabled, newTool(ToolUsersSetProfile, mcp.WithDescription("Update the authenticated user's Slack profile."),
			mcp.WithString("first_name", mcp.Description("First name, up to 80 characters; empty string clears it.")), mcp.WithString("last_name", mcp.Description("Last name, up to 80 characters; empty string clears it.")), mcp.WithString("real_name", mcp.Description("Full name, up to 80 characters; empty string clears it.")), mcp.WithString("display_name", mcp.Description("Display name shown in Slack, up to 80 characters; empty string clears it.")), mcp.WithString("pronouns", mcp.Description("Pronouns, up to 100 characters; empty string clears them.")),
			mcp.WithString("email", mcp.Description("Email address, up to 320 characters; empty string clears it.")), mcp.WithString("phone", mcp.Description("Phone number, up to 100 characters; empty string clears it.")), mcp.WithString("skype", mcp.Description("Skype handle, up to 100 characters; empty string clears it.")), mcp.WithString("title", mcp.Description("Job title, up to 100 characters; empty string clears it.")), mcp.WithString("start_date", mcp.Description("Start date in YYYY-MM-DD format; empty string clears it.")), mcp.WithObject("custom_fields", mcp.Description("Map of custom field ID (Xf...) to {value, alt} objects; at most 50 fields."))), h.SetUserProfile)

		addEnabledTool(s, enabled, newTool(ToolUsersSetStatus, mcp.WithDescription("Set or clear the authenticated user's Slack status."), mcp.WithString("status_text", mcp.Required(), mcp.Description("Status text, up to 100 characters; empty string clears the status.")), mcp.WithString("status_emoji", mcp.Required(), mcp.Description("Emoji name such as :palm_tree: (colons optional); empty string clears it.")), mcp.WithNumber("status_expiration", mcp.Description("Unix seconds when the status clears; 0 keeps it until changed."))), h.SetUserStatus)

		addEnabledTool(s, enabled, newTool(ToolEmojiList, mcp.WithDescription("List and search workspace custom emoji."), mcp.WithString("query", mcp.Description("Case-insensitive substring to match against emoji names.")), mcp.WithString("cursor", mcp.Description(descCursor)), mcp.WithNumber("limit", mcp.Description("Maximum emoji per page, 1 to 200; defaults to 100."))), h.ListEmoji)

		addEnabledTool(s, enabled, newTool(ToolChannelsCreate, mcp.WithDescription("Create a public or private Slack channel."), mcp.WithString("name", mcp.Required(), mcp.Description("Channel name, 1 to 80 lowercase letters, numbers, hyphens, or underscores.")), mcp.WithBoolean("is_private", mcp.Description("Create a private channel; defaults to false (public).")), mcp.WithString("team_id", mcp.Description("Workspace (team) ID for Enterprise Grid orgs; defaults to the authenticated user's team."))), h.CreateChannel)

		addEnabledTool(s, enabled, newTool(ToolChannelsMembers, mcp.WithDescription("List channel members. Returns CSV with UserID and Name (empty when the user is not in the cache)."), mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)), mcp.WithString("cursor", mcp.Description(descCursor)), mcp.WithNumber("limit", mcp.Description("Maximum members per page, 1 to 200; defaults to 100."))), h.ListChannelMembers)

		addEnabledTool(s, enabled, newTool(ToolChannelsInvite, mcp.WithDescription("Invite users to a Slack channel."), mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)), mcp.WithArray("user_ids", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("User IDs starting with U or W to invite; 1 to 1000 entries, duplicates ignored."))), h.InviteChannelMembers)

	} else {
		logger.Warn("People and channel tools unavailable", zap.Error(err))
	}

	if service, err := api.Canvases(); err == nil {
		h := handler.NewCanvasHandler(service, logger)
		addEnabledTool(s, enabled, newTool(ToolCanvasesCreate, mcp.WithDescription("Create a Slack canvas from Markdown."), mcp.WithString("title", mcp.Description("Canvas title; at least one of title or markdown is required.")), mcp.WithString("markdown", mcp.Description("Initial canvas body in Slack-flavored Markdown; at least one of title or markdown is required."))), h.Create)

		addEnabledTool(s, enabled, newTool(ToolCanvasesRead, mcp.WithDescription("Read canvas metadata, preview, and matching section IDs. Slack does not expose a full public canvas export."), mcp.WithString("canvas_id", mcp.Required(), mcp.Description("Canvas ID, a Slack file ID starting with F (e.g. F1234567890).")), mcp.WithArray("section_types", mcp.Items(map[string]any{"type": "string"}), mcp.Description("Section types to look up, e.g. h1, h2, h3, list, table, any_header; returns matching section IDs.")), mcp.WithString("contains_text", mcp.Description("Return IDs of sections whose text contains this string; combinable with section_types."))), h.Read)

		change := map[string]any{"type": "object", "required": []string{"operation", "markdown"}, "properties": map[string]any{"operation": map[string]any{"type": "string", "enum": []string{"insert_at_start", "insert_at_end", "insert_before", "insert_after", "replace"}}, "section_id": map[string]any{"type": "string"}, "markdown": map[string]any{"type": "string"}}, "additionalProperties": false}
		addEnabledTool(s, enabled, newTool(ToolCanvasesUpdate, mcp.WithDescription("Apply exactly one Markdown edit to a Slack canvas."), mcp.WithString("canvas_id", mcp.Required(), mcp.Description("Canvas ID, a Slack file ID starting with F (e.g. F1234567890).")), mcp.WithArray("changes", mcp.Required(), mcp.MinItems(1), mcp.MaxItems(1), mcp.Items(change), mcp.Description("Exactly one edit object with operation, markdown, and section_id (needed for insert_before and insert_after)."))), h.Update)
	}

	channelName := func(id string) string {
		if cached, ok := api.ProvideChannelsMaps().Channels[id]; ok {
			return cached.Name
		}
		return ""
	}
	drafts := handler.NewDraftsHandler(api.Drafts(), approvals, api.Identity, logger)
	drafts.ChannelName = channelName
	addEnabledTool(s, enabled, newTool(ToolDraftsList, mcp.WithDescription("List persisted Slack drafts from the authenticated browser session."), mcp.WithString("cursor", mcp.Description(descCursor)), mcp.WithNumber("limit", mcp.Description("Maximum drafts per page, 1 to 100; defaults to 50."))), drafts.List)

	addEnabledTool(s, enabled, newTool(ToolDraftsGet, mcp.WithDescription("Read one persisted Slack draft."), mcp.WithString("draft_id", mcp.Required(), mcp.Description("Draft ID as returned by drafts_list or drafts_create."))), drafts.Get)

	addEnabledTool(s, enabled, newTool(ToolDraftsCreate, mcp.WithDescription("Create a persisted Slack draft without sending it."), mcp.WithString("channel_id", mcp.Required(), mcp.Description("Destination channel or DM ID (Cxxxxxxxxxx or Dxxxxxxxxxx); names are not resolved.")), mcp.WithString("text", mcp.Required(), mcp.Description("Draft message text; required and non-empty.")), mcp.WithString("thread_ts", mcp.Description("Parent message timestamp (e.g. 1712345678.123456) to draft a thread reply."))), drafts.Create)

	addEnabledTool(s, enabled, newTool(ToolDraftsUpdate, mcp.WithDescription("Update a persisted Slack draft without sending it."), mcp.WithString("id", mcp.Required(), mcp.Description("ID of the draft to update, as returned by drafts_list or drafts_create.")), mcp.WithString("channel_id", mcp.Required(), mcp.Description("Destination channel or DM ID (Cxxxxxxxxxx or Dxxxxxxxxxx); names are not resolved.")), mcp.WithString("text", mcp.Required(), mcp.Description("Replacement draft text; required and non-empty.")), mcp.WithString("thread_ts", mcp.Description("Parent message timestamp (e.g. 1712345678.123456) to target a thread; omit for a top-level draft."))), drafts.Update)

	addEnabledTool(s, enabled, newTool(ToolDraftsDelete, mcp.WithDescription("Preview or delete a persisted Slack draft."), mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)), mcp.WithString("draft_id", mcp.Required(), mcp.Description("ID of the draft to delete, as returned by drafts_list.")), mcp.WithString("approval_token", mcp.Description(descApprovalToken))), drafts.Delete)

	if service, err := api.SemanticSearch(); err == nil {
		h := handler.NewSemanticSearchHandler(service, logger)
		h.ChannelName = channelName
		addEnabledTool(s, enabled, newTool(ToolSearchSemantic, mcp.WithDescription("Semantic (meaning-based) search over messages and files; use for natural-language questions when exact keywords are unknown. For keyword, from:, in:, or date-filtered search use conversations_search_messages. Only works when Slack Real-time Search is enabled for the app."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language search query or question.")), mcp.WithArray("content_types", mcp.Items(map[string]any{"type": "string", "enum": []string{"messages", "files"}}), mcp.Description("Result kinds to include; defaults to messages only.")),
			mcp.WithArray("channel_types", mcp.Items(map[string]any{"type": "string"}), mcp.Description("Channel kinds to search: public_channel, private_channel, mpim, im; defaults to public_channel.")), mcp.WithString("context_channel_id", mcp.Description("Channel ID (Cxxxxxxxxxx) Slack uses to scope or bias results when applicable.")), mcp.WithString("cursor", mcp.Description(descCursor)), mcp.WithBoolean("include_bots", mcp.Description("Include messages authored by bots; defaults to false.")), mcp.WithNumber("limit", mcp.Description("Maximum results per page, 1 to 20; defaults to 20."))), h.Search)
	}
}
