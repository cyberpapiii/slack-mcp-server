package handler

import (
	"context"
	"encoding/csv"
	"fmt"
	"slices"
	"strings"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

type CanvasHandler struct {
	api    provider.CanvasAPI
	logger *zap.Logger
}

func NewCanvasHandler(api provider.CanvasAPI, logger *zap.Logger) *CanvasHandler {
	return &CanvasHandler{api: api, logger: logger}
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
		return nil, TypeError(err, true)
	}
	data := CanvasMutationData{CanvasID: id, Status: "created"}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
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
		return nil, TypeError(err, false)
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
		return nil, TypeError(err, true)
	}
	data := CanvasMutationData{CanvasID: input.CanvasID, Status: "updated"}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

type DraftsHandler struct {
	api       provider.DraftsAPI
	approvals approvalGate
	logger    *zap.Logger
	labels    Labels
}

func NewDraftsHandler(api provider.DraftsAPI, labels Labels, approvals *approval.Store, identity func() provider.ProviderIdentity, logger *zap.Logger) *DraftsHandler {
	return &DraftsHandler{api: api, labels: labels, approvals: approvalGate{store: approvals, identity: identityFunc(identity)}, logger: logger}
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
	limit := pageLimit(request, 50, 100)
	page, err := h.api.ListDrafts(ctx, strings.TrimSpace(request.GetString("cursor", "")), limit)
	if err != nil {
		return nil, TypeError(err, false)
	}
	rows := make([]DraftRow, len(page.Drafts))
	channelIDs := make([]string, len(page.Drafts))
	for i, draft := range page.Drafts {
		rows[i] = DraftRow{DraftID: draft.ID, Channel: draft.ChannelID, ThreadTs: draft.ThreadTS, Updated: slackTsTime(draft.UpdatedTS), Text: draft.Text}
		channelIDs[i] = draft.ChannelID
	}
	csvBytes, err := gocsv.MarshalBytes(&rows)
	if err != nil {
		return nil, err
	}
	return NewCSVResult(channelsLegend(channelIDs, h.labels.ChannelLabel), SlackResultMeta(page.NextCursor, false, ""), string(csvBytes)), nil
}

// slackTsTime renders a Slack "seconds.micros" timestamp as RFC3339 and
// passes anything else through unchanged.
func slackTsTime(ts string) string {
	if rendered, err := text.TimestampToIsoRFC3339(ts); err == nil {
		return rendered
	}
	return ts
}

func (h *DraftsHandler) Get(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := strings.TrimSpace(request.GetString("draft_id", ""))
	if id == "" {
		return nil, invalidArguments("draft_id is required")
	}
	draft, err := h.api.GetDraft(ctx, id)
	if err != nil {
		return nil, TypeError(err, false)
	}
	return NewStructuredResult(draft, SlackResultMeta("", false, ""), fallbackJSON(draft)), nil
}

func (h *DraftsHandler) Create(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	draft, err := decodeDraft(request, false)
	if err != nil {
		return nil, err
	}
	created, err := h.api.CreateDraft(ctx, draft)
	if err != nil {
		return nil, TypeError(err, true)
	}
	data := DraftMutationData{ApprovalPhase: ApprovalPhase{Phase: "executed"}, Status: "created", Draft: &created}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *DraftsHandler) Update(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	draft, err := decodeDraft(request, true)
	if err != nil {
		return nil, err
	}
	updated, err := h.api.UpdateDraft(ctx, draft)
	if err != nil {
		return nil, TypeError(err, true)
	}
	data := DraftMutationData{ApprovalPhase: ApprovalPhase{Phase: "executed"}, Status: "updated", Draft: &updated}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
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
		return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
	}
	if err := h.api.DeleteDraft(ctx, id, current.UpdatedTS); err != nil {
		return nil, TypeError(err, true)
	}
	data := DraftMutationData{ApprovalPhase: phase, Status: "deleted", Draft: &current}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
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
	api    provider.SemanticSearchAPI
	logger *zap.Logger
	labels Labels
}

func NewSemanticSearchHandler(api provider.SemanticSearchAPI, labels Labels, logger *zap.Logger) *SemanticSearchHandler {
	return &SemanticSearchHandler{api: api, labels: labels, logger: logger}
}

// semanticMessageColumns mirror the history row shape so message hits read
// like conversations_history output. Slack returns author IDs only, hence
// UserID rather than a resolved User column. semanticFileColumns extend them
// when the page can contain file hits; FileID feeds attachment_get_data.
var (
	semanticMessageColumns = []string{"UserID", "Channel", "Text", "Time", "MsgID"}
	semanticFileColumns    = []string{"Type", "UserID", "Channel", "Text", "Time", "MsgID", "FileID", "Title", "FileType"}
)

func semanticSearchCSV(items []provider.SemanticSearchItem, includeFiles bool) (string, error) {
	for _, item := range items {
		includeFiles = includeFiles || item.Kind == "file"
	}
	header := semanticMessageColumns
	if includeFiles {
		header = semanticFileColumns
	}
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	_ = w.Write(header)
	for _, item := range items {
		row := []string{item.AuthorUserID, item.ChannelID, item.Content, "", item.MessageTS}
		if item.MessageTS != "" {
			row[3] = slackTsTime(item.MessageTS)
		}
		if includeFiles {
			row = append([]string{item.Kind}, row...)
			row = append(row, item.FileID, item.Title, item.FileType)
		}
		_ = w.Write(row)
	}
	w.Flush()
	return sb.String(), w.Error()
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
		return nil, TypeError(err, false)
	}
	csvText, err := semanticSearchCSV(page.Items, slices.Contains(input.ContentTypes, "files"))
	if err != nil {
		return nil, err
	}
	channelIDs := make([]string, len(page.Items))
	for i, item := range page.Items {
		channelIDs[i] = item.ChannelID
	}
	return NewCSVResult(channelsLegend(channelIDs, h.labels.ChannelLabel), SlackResultMeta(page.NextCursor, false, ""), csvText), nil
}

// Tools lists the MCP tools this handler serves.
func (h *DraftsHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.DraftsList, Run: h.List, Options: []mcp.ToolOption{
			mcp.WithDescription("List persisted Slack drafts from the authenticated browser session."),
			mcp.WithString("cursor", mcp.Description(descCursor)),
			mcp.WithNumber("limit", mcp.Description("Maximum drafts per page, 1 to 100; defaults to 50.")),
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
		{Name: capability.DraftsDelete, Run: h.Delete, Options: []mcp.ToolOption{
			mcp.WithOutputSchema[DraftMutationResult](),
			mcp.WithDescription("Preview or delete a persisted Slack draft."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)),
			mcp.WithString("draft_id", mcp.Required(), mcp.Description("ID of the draft to delete, as returned by drafts_list.")),
			mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
		}},
	}
}

// Tools lists the MCP tools this handler serves.
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
			mcp.WithNumber("limit", mcp.Description("Maximum results per page, 1 to 20; defaults to 20.")),
		}},
	}
}

// Tools lists the MCP tools this handler serves.
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
