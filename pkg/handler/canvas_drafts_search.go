package handler

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
)

type CanvasHandler struct {
	api provider.CanvasAPI
}

func NewCanvasHandler(api provider.CanvasAPI) *CanvasHandler {
	return &CanvasHandler{api: api}
}

type CanvasMutationData struct {
	CanvasID string `json:"canvas_id"`
	Status   string `json:"status"`
}

func (h *CanvasHandler) Create(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.CanvasCreateRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" && strings.TrimSpace(input.Markdown) == "" {
		return nil, invalidArguments("title or markdown is required")
	}
	id, err := h.api.CreateCanvas(ctx, input)
	if err != nil {
		return nil, err
	}
	data := CanvasMutationData{CanvasID: id, Status: "created"}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func (h *CanvasHandler) Read(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.CanvasReadRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	input.CanvasID = strings.TrimSpace(input.CanvasID)
	input.ContainsText = strings.TrimSpace(input.ContainsText)
	if input.CanvasID == "" {
		return nil, invalidArguments("canvas_id is required")
	}
	switch input.ContentFormat {
	case "", "markdown", "html", "none":
	default:
		return nil, invalidArguments("content_format must be markdown, html, or none")
	}
	document, err := h.api.ReadCanvas(ctx, input)
	if err != nil {
		return nil, err
	}
	return NewStructuredResult(document, SlackResultMeta("", document.Limitation != "", document.Limitation), fallbackJSON(document)), nil
}

const maxSemanticResults = 20

var canvasOperations = []string{"insert_at_start", "insert_at_end", "insert_before", "insert_after", "replace"}

func (h *CanvasHandler) Update(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.CanvasUpdateRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	input.CanvasID = strings.TrimSpace(input.CanvasID)
	if input.CanvasID == "" || len(input.Changes) != 1 {
		return nil, invalidArguments("canvas_id and exactly one change are required; Slack accepts one canvas edit per call")
	}
	for i := range input.Changes {
		change := &input.Changes[i]
		change.Operation = strings.TrimSpace(change.Operation)
		change.SectionID = strings.TrimSpace(change.SectionID)
		if !slices.Contains(canvasOperations, change.Operation) {
			return nil, invalidArguments(fmt.Sprintf("changes[%d].operation is unsupported", i))
		}
		if (change.Operation == "insert_before" || change.Operation == "insert_after") && change.SectionID == "" {
			return nil, invalidArguments(fmt.Sprintf("changes[%d].section_id is required for %s", i, change.Operation))
		}
		if strings.TrimSpace(change.Markdown) == "" {
			return nil, invalidArguments(fmt.Sprintf("changes[%d].markdown is required for %s", i, change.Operation))
		}
	}
	if err := h.api.UpdateCanvas(ctx, input); err != nil {
		return nil, err
	}
	data := CanvasMutationData{CanvasID: input.CanvasID, Status: "updated"}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

// drafts_list limit bounds.
const (
	defaultDraftsLimit = 50
	maxDraftsLimit     = 100
)

type DraftsHandler struct {
	api       provider.DraftsAPI
	approvals approvalGate
	labels    Labels
}

func NewDraftsHandler(api provider.DraftsAPI, labels Labels, identity func() provider.ProviderIdentity) *DraftsHandler {
	return &DraftsHandler{api: renderedDrafts{api}, labels: labels, approvals: newApprovalGate(identity)}
}

// renderedDrafts fills each draft Slack returns with its blocks rendered as
// text, so every drafts tool shows the same text.
type renderedDrafts struct{ provider.DraftsAPI }

func renderDraft(draft provider.Draft) provider.Draft {
	if len(draft.Blocks.BlockSet) > 0 {
		draft.Text = text.BlocksToText(draft.Blocks)
	}
	return draft
}

func (r renderedDrafts) ListDrafts(ctx context.Context, cursor string, limit int) (provider.DraftPage, error) {
	page, err := r.DraftsAPI.ListDrafts(ctx, cursor, limit)
	for i := range page.Drafts {
		page.Drafts[i] = renderDraft(page.Drafts[i])
	}
	return page, err
}

func (r renderedDrafts) GetDraft(ctx context.Context, id string) (provider.Draft, error) {
	draft, err := r.DraftsAPI.GetDraft(ctx, id)
	return renderDraft(draft), err
}

func (r renderedDrafts) CreateDraft(ctx context.Context, draft provider.Draft) (provider.Draft, error) {
	created, err := r.DraftsAPI.CreateDraft(ctx, draft)
	return renderDraft(created), err
}

func (r renderedDrafts) UpdateDraft(ctx context.Context, draft provider.Draft) (provider.Draft, error) {
	updated, err := r.DraftsAPI.UpdateDraft(ctx, draft)
	return renderDraft(updated), err
}

type DraftMutationData struct {
	ApprovalPhase
	Status string          `json:"status"`
	Draft  *provider.Draft `json:"draft,omitempty"`
}

type DraftMutationResult = ToolResult[DraftMutationData]

// DraftRow is one drafts_list CSV row. DraftID feeds drafts_get, drafts_update,
// and drafts_delete; the handlers re-read the draft's update stamp themselves.
type DraftRow struct {
	DraftID  string `csv:"DraftID"`
	Channel  string `csv:"Channel"`
	ThreadTs string `csv:"ThreadTs"`
	Updated  string `csv:"Updated"`
	Text     string `csv:"Text"`
}

func (h *DraftsHandler) List(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	limit := pageLimit(request, defaultDraftsLimit, maxDraftsLimit)
	page, err := h.api.ListDrafts(ctx, strings.TrimSpace(request.GetString("cursor", "")), limit)
	if err != nil {
		return nil, err
	}
	rows := make([]DraftRow, len(page.Drafts))
	channelIDs := make([]string, len(page.Drafts))
	for i, draft := range page.Drafts {
		rows[i] = DraftRow{DraftID: draft.ID, Channel: draft.ChannelID, ThreadTs: draft.ThreadTS, Updated: slackTsText(draft.UpdatedTS), Text: draft.Text}
		channelIDs[i] = draft.ChannelID
	}
	return csvResult(channelsLegend(channelIDs, h.labels.ChannelLabel), SlackResultMeta(page.NextCursor, false, ""), &rows)
}

func (h *DraftsHandler) Get(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strings.TrimSpace(request.GetString("draft_id", ""))
	if id == "" {
		return nil, invalidArguments("draft_id is required")
	}
	draft, err := h.api.GetDraft(ctx, id)
	if err != nil {
		return nil, err
	}
	return NewStructuredResult(draft, slackMeta(), fallbackJSON(draft)), nil
}

func (h *DraftsHandler) Create(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	draft, err := decodeDraft(request, false)
	if err != nil {
		return nil, err
	}
	created, err := h.api.CreateDraft(ctx, draft)
	if err != nil {
		return nil, err
	}
	data := DraftMutationData{ApprovalPhase: ApprovalPhase{Phase: "executed"}, Status: "created", Draft: &created}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func (h *DraftsHandler) Update(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	draft, err := decodeDraft(request, true)
	if err != nil {
		return nil, err
	}
	updated, err := h.api.UpdateDraft(ctx, draft)
	if err != nil {
		return nil, err
	}
	data := DraftMutationData{ApprovalPhase: ApprovalPhase{Phase: "executed"}, Status: "updated", Draft: &updated}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func (h *DraftsHandler) Delete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strings.TrimSpace(request.GetString("draft_id", ""))
	if id == "" {
		return nil, invalidArguments("draft_id is required")
	}
	prepare, err := approvalAction(request)
	if err != nil {
		return nil, err
	}
	current, err := h.api.GetDraft(ctx, id)
	if err != nil {
		return nil, TypeError(err, false)
	}
	arguments := struct {
		DraftID string `json:"draft_id"`
	}{current.ID}
	phase, err := h.approvals.check(request, capability.DraftsDelete, prepare, arguments, current)
	if err != nil {
		return nil, err
	}
	if phase.Prepared() {
		data := DraftMutationData{ApprovalPhase: phase, Status: "awaiting_confirmation", Draft: &current}
		return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
	}
	if err := h.api.DeleteDraft(ctx, id, current.UpdatedTS); err != nil {
		return nil, err
	}
	data := DraftMutationData{ApprovalPhase: phase, Status: "deleted", Draft: &current}
	return NewStructuredResult(data, slackMeta(), fallbackJSON(data)), nil
}

func decodeDraft(request mcp.CallToolRequest, requireID bool) (provider.Draft, error) {
	var draft provider.Draft
	if err := decodeArguments(request, &draft); err != nil {
		return provider.Draft{}, err
	}
	draft.ID = strings.TrimSpace(draft.ID)
	draft.ChannelID = strings.TrimSpace(draft.ChannelID)
	draft.ThreadTS = strings.TrimSpace(draft.ThreadTS)
	if requireID && draft.ID == "" {
		return provider.Draft{}, invalidArguments("id is required")
	}
	if draft.ChannelID == "" || strings.TrimSpace(draft.Text) == "" {
		return provider.Draft{}, invalidArguments("channel_id and text are required")
	}
	return draft, nil
}

type SemanticSearchHandler struct {
	api    SemanticSearchAPI
	labels Labels
}

type SemanticSearchAPI interface {
	SearchSemantic(context.Context, provider.SemanticSearchRequest) (provider.SemanticSearchPage, error)
}

func NewSemanticSearchHandler(api SemanticSearchAPI, labels Labels) *SemanticSearchHandler {
	return &SemanticSearchHandler{api: api, labels: labels}
}

// semanticMessageRow mirrors the history row shape so message hits read like
// conversations_history output. Slack returns author IDs only, hence UserID
// rather than a resolved User column. semanticFileRow extends it when the
// page can contain file hits; FileID feeds attachment_get_data.
type semanticMessageRow struct {
	UserID  string `csv:"UserID"`
	Channel string `csv:"Channel"`
	Text    string `csv:"Text"`
	Time    string `csv:"Time"`
	MsgID   string `csv:"MsgID"`
}

type semanticFileRow struct {
	Type     string `csv:"Type"`
	UserID   string `csv:"UserID"`
	Channel  string `csv:"Channel"`
	Text     string `csv:"Text"`
	Time     string `csv:"Time"`
	MsgID    string `csv:"MsgID"`
	FileID   string `csv:"FileID"`
	Title    string `csv:"Title"`
	FileType string `csv:"FileType"`
}

func semanticSearchResult(page provider.SemanticSearchPage, includeFiles bool, channelName func(string) string) (*mcp.CallToolResult, error) {
	channelIDs := make([]string, len(page.Items))
	for i, item := range page.Items {
		channelIDs[i] = item.ChannelID
		includeFiles = includeFiles || item.Kind == "file"
	}
	legend := channelsLegend(channelIDs, channelName)
	meta := SlackResultMeta(page.NextCursor, false, "")
	if !includeFiles {
		rows := make([]semanticMessageRow, len(page.Items))
		for i, item := range page.Items {
			rows[i] = semanticMessageRow{UserID: item.AuthorUserID, Channel: item.ChannelID, Text: item.Content, Time: slackTsText(item.MessageTS), MsgID: item.MessageTS}
		}
		return csvResult(legend, meta, &rows)
	}
	rows := make([]semanticFileRow, len(page.Items))
	for i, item := range page.Items {
		rows[i] = semanticFileRow{Type: item.Kind, UserID: item.AuthorUserID, Channel: item.ChannelID, Text: item.Content, Time: slackTsText(item.MessageTS), MsgID: item.MessageTS, FileID: item.FileID, Title: item.Title, FileType: item.FileType}
	}
	return csvResult(legend, meta, &rows)
}

func (h *SemanticSearchHandler) Search(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input provider.SemanticSearchRequest
	if err := decodeArguments(request, &input); err != nil {
		return nil, err
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return nil, invalidArguments("query is required")
	}
	if input.Limit == 0 {
		input.Limit = maxSemanticResults
	}
	if input.Limit < 1 || input.Limit > maxSemanticResults {
		return nil, invalidArguments(fmt.Sprintf("limit must be between 1 and %d", maxSemanticResults))
	}
	for _, contentType := range input.ContentTypes {
		if contentType != "messages" && contentType != "files" {
			return nil, invalidArguments("content_types supports only messages and files")
		}
	}
	page, err := h.api.SearchSemantic(ctx, input)
	if err != nil {
		return nil, err
	}
	return semanticSearchResult(page, slices.Contains(input.ContentTypes, "files"), h.labels.ChannelLabel)
}

func (h *DraftsHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.DraftsList, Run: h.List, Options: []mcp.ToolOption{
			mcp.WithDescription("List persisted Slack drafts from the authenticated browser session."),
			mcp.WithString("cursor", mcp.Description(descCursor)),
			mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("Maximum drafts per page, 1 to %d; defaults to %d.", maxDraftsLimit, defaultDraftsLimit))),
		}},
		{Name: capability.DraftsGet, Run: h.Get, Options: []mcp.ToolOption{
			mcp.WithDescription("Read one persisted Slack draft."),
			mcp.WithString("draft_id", mcp.Required(), mcp.Description("Draft ID as returned by drafts_list or drafts_create.")),
		}},
		{Name: capability.DraftsCreate, Run: h.Create, Options: []mcp.ToolOption{
			mcp.WithDescription("Create a persisted Slack draft without sending it."),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Destination channel or DM ID (Cxxxxxxxxxx or Dxxxxxxxxxx); names are not resolved.")),
			mcp.WithString("text", mcp.Required(), mcp.Description("Draft message text; required and non-empty.")),
			mcp.WithString("thread_ts", mcp.Description("Parent message timestamp (e.g. 1712345678.123456) to draft a thread reply.")),
		}},
		{Name: capability.DraftsUpdate, Run: h.Update, Options: []mcp.ToolOption{
			mcp.WithDescription("Update a persisted Slack draft without sending it."),
			mcp.WithString("id", mcp.Required(), mcp.Description("ID of the draft to update, as returned by drafts_list or drafts_create.")),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description("Destination channel or DM ID (Cxxxxxxxxxx or Dxxxxxxxxxx); names are not resolved.")),
			mcp.WithString("text", mcp.Required(), mcp.Description("Replacement draft text; required and non-empty.")),
			mcp.WithString("thread_ts", mcp.Description("Parent message timestamp (e.g. 1712345678.123456) to target a thread; omit for a top-level draft.")),
		}},
		{Name: capability.DraftsDelete, Run: h.Delete, Options: append([]mcp.ToolOption{
			mcp.WithOutputSchema[DraftMutationResult](),
			mcp.WithDescription("Preview or delete a persisted Slack draft."),
		}, approvalOptions(
			mcp.WithString("draft_id", mcp.Required(), mcp.Description("ID of the draft to delete, as returned by drafts_list.")),
		)...)},
	}
}

func (h *SemanticSearchHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.SearchSemantic, Run: h.Search, Options: []mcp.ToolOption{
			mcp.WithDescription("Semantic (meaning-based) search over messages and files; use for natural-language questions when exact keywords are unknown. For keyword, from:, in:, or date-filtered search use conversations_search_messages. Only works when Slack Real-time Search is enabled for the app."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Natural-language search query or question.")),
			mcp.WithArray("content_types", mcp.Items(map[string]any{"type": "string", "enum": []string{"messages", "files"}}), mcp.Description("Result kinds to include; defaults to messages only.")),
			mcp.WithArray("channel_types", mcp.Items(map[string]any{"type": "string"}), mcp.Description("Channel kinds to search: public_channel, private_channel, mpim, im; defaults to public_channel.")),
			mcp.WithString("context_channel_id", mcp.Description("Channel ID (Cxxxxxxxxxx) Slack uses to scope or bias results when applicable.")),
			mcp.WithString("cursor", mcp.Description(descCursor)),
			mcp.WithBoolean("include_bots", mcp.Description("Include messages authored by bots; defaults to false.")),
			mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("Maximum results per page, 1 to %d; defaults to %d.", maxSemanticResults, maxSemanticResults))),
		}},
	}
}

func (h *CanvasHandler) Tools() []ToolDef {
	change := map[string]any{"type": "object", "required": []string{"operation", "markdown"}, "properties": map[string]any{"operation": map[string]any{"type": "string", "enum": canvasOperations}, "section_id": map[string]any{"type": "string"}, "markdown": map[string]any{"type": "string"}}, "additionalProperties": false}
	return []ToolDef{
		{Name: capability.CanvasesCreate, Run: h.Create, Options: []mcp.ToolOption{
			mcp.WithDescription("Create a Slack canvas from Markdown."),
			mcp.WithString("title", mcp.Description("Canvas title; at least one of title or markdown is required.")),
			mcp.WithString("markdown", mcp.Description("Initial canvas body in Slack-flavored Markdown; at least one of title or markdown is required.")),
		}},
		{Name: capability.CanvasesRead, Run: h.Read, Options: []mcp.ToolOption{
			mcp.WithDescription("Read a canvas: its full content plus metadata and matching section IDs. Full content needs user OAuth; without it the result carries a preview and says why."),
			mcp.WithString("canvas_id", mcp.Required(), mcp.Description("Canvas ID, a Slack file ID starting with F (e.g. F1234567890).")),
			mcp.WithArray("section_types", mcp.Items(map[string]any{"type": "string"}), mcp.Description("Section types to look up, e.g. h1, h2, h3, list, table, any_header; returns matching section IDs.")),
			mcp.WithString("contains_text", mcp.Description("Return IDs of sections whose text contains this string; combinable with section_types.")),
			mcp.WithString("content_format", mcp.Enum("markdown", "html", "none"), mcp.Description("Format of the full content: markdown (default), html, or none for metadata only.")),
		}},
		{Name: capability.CanvasesUpdate, Run: h.Update, Options: []mcp.ToolOption{
			mcp.WithDescription("Apply exactly one Markdown edit to a Slack canvas."),
			mcp.WithString("canvas_id", mcp.Required(), mcp.Description("Canvas ID, a Slack file ID starting with F (e.g. F1234567890).")),
			mcp.WithArray("changes", mcp.Required(), mcp.MinItems(1), mcp.MaxItems(1), mcp.Items(change), mcp.Description("Exactly one edit object with operation, markdown, and section_id (needed for insert_before and insert_after).")),
		}},
	}
}
