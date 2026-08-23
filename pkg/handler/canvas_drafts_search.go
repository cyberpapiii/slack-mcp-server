package handler

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/approval"
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
	logToolCall(h.logger, "CanvasCreate called", request)
	var input provider.CanvasCreateRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" && strings.TrimSpace(input.Markdown) == "" {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "title or markdown is required"}), nil
	}
	id, err := h.api.CreateCanvas(ctx, input)
	if err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, true)), nil
	}
	data := CanvasMutationData{CanvasID: id, Status: "created"}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *CanvasHandler) Read(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "CanvasRead called", request)
	var input provider.CanvasReadRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	input.CanvasID = strings.TrimSpace(input.CanvasID)
	input.ContainsText = strings.TrimSpace(input.ContainsText)
	if input.CanvasID == "" {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "canvas_id is required"}), nil
	}
	document, err := h.api.ReadCanvas(ctx, input)
	if err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, false)), nil
	}
	return NewStructuredResult(document, SlackResultMeta("", true, document.Limitation), fallbackJSON(document)), nil
}

var canvasOperations = map[string]struct{}{
	"insert_at_start": {}, "insert_at_end": {}, "insert_before": {}, "insert_after": {}, "replace": {},
}

func (h *CanvasHandler) Update(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "CanvasUpdate called", request)
	var input provider.CanvasUpdateRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	input.CanvasID = strings.TrimSpace(input.CanvasID)
	if input.CanvasID == "" || len(input.Changes) != 1 {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "canvas_id and exactly one change are required; Slack accepts one canvas edit per call"}), nil
	}
	for i := range input.Changes {
		change := &input.Changes[i]
		change.Operation = strings.TrimSpace(change.Operation)
		change.SectionID = strings.TrimSpace(change.SectionID)
		if _, ok := canvasOperations[change.Operation]; !ok {
			return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: fmt.Sprintf("changes[%d].operation is unsupported", i)}), nil
		}
		if (change.Operation == "insert_before" || change.Operation == "insert_after") && change.SectionID == "" {
			return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: fmt.Sprintf("changes[%d].section_id is required for %s", i, change.Operation)}), nil
		}
		if strings.TrimSpace(change.Markdown) == "" {
			return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: fmt.Sprintf("changes[%d].markdown is required for %s", i, change.Operation)}), nil
		}
	}
	if err := h.api.UpdateCanvas(ctx, input); err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, true)), nil
	}
	data := CanvasMutationData{CanvasID: input.CanvasID, Status: "updated"}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

type DraftsHandler struct {
	api       provider.DraftsAPI
	approvals *approval.Store
	identity  func() provider.ProviderIdentity
	logger    *zap.Logger
	// ChannelName resolves a conversation ID to its cached "#name" or "@user"
	// label for the #channels legend. nil leaves the legend out.
	ChannelName func(channelID string) string
}

func NewDraftsHandler(api provider.DraftsAPI, approvals *approval.Store, identity func() provider.ProviderIdentity, logger *zap.Logger) *DraftsHandler {
	return &DraftsHandler{api: api, approvals: approvals, identity: identity, logger: logger}
}

type DraftMutationData struct {
	Phase         string          `json:"phase"`
	Status        string          `json:"status"`
	Draft         *provider.Draft `json:"draft,omitempty"`
	ApprovalToken string          `json:"approval_token,omitempty"`
	ExpiresAt     string          `json:"expires_at,omitempty"`
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
	logToolCall(h.logger, "DraftsList called", request)
	limit := request.GetInt("limit", 50)
	if limit < 1 || limit > 100 {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "limit must be between 1 and 100"}), nil
	}
	page, err := h.api.ListDrafts(ctx, strings.TrimSpace(request.GetString("cursor", "")), limit)
	if err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, false)), nil
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
	return NewCSVResult(channelsLegend(channelIDs, h.ChannelName), SlackResultMeta(page.NextCursor, false, ""), string(csvBytes)), nil
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
	logToolCall(h.logger, "DraftsGet called", request)
	id := strings.TrimSpace(request.GetString("draft_id", ""))
	if id == "" {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "draft_id is required"}), nil
	}
	draft, err := h.api.GetDraft(ctx, id)
	if err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, false)), nil
	}
	return NewStructuredResult(draft, SlackResultMeta("", false, ""), fallbackJSON(draft)), nil
}

func (h *DraftsHandler) Create(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "DraftsCreate called", request)
	draft, err := decodeDraft(request, false)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	created, err := h.api.CreateDraft(ctx, draft)
	if err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, true)), nil
	}
	data := DraftMutationData{Phase: "executed", Status: "created", Draft: &created}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *DraftsHandler) Update(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "DraftsUpdate called", request)
	draft, err := decodeDraft(request, true)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	updated, err := h.api.UpdateDraft(ctx, draft)
	if err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, true)), nil
	}
	data := DraftMutationData{Phase: "executed", Status: "updated", Draft: &updated}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func (h *DraftsHandler) Delete(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "DraftsDelete called", request)
	action := strings.TrimSpace(request.GetString("action", "prepare"))
	id := strings.TrimSpace(request.GetString("draft_id", ""))
	if id == "" || (action != "prepare" && action != "execute") {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "draft_id is required and action must be prepare or execute"}), nil
	}
	current, err := h.api.GetDraft(ctx, id)
	if err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, false)), nil
	}
	binding, err := draftDeleteBinding(h.identity, current)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	prepared, execute, err := prepareOrExecute(h.approvals, action, request.GetString("approval_token", ""), binding)
	if err != nil {
		return NewTypedErrorResult(err), nil
	}
	if !execute {
		data := DraftMutationData{Phase: "prepared", Status: "awaiting_confirmation", Draft: &current, ApprovalToken: prepared.Token, ExpiresAt: prepared.ExpiresAt.Format(time.RFC3339)}
		return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
	}
	if err := h.api.DeleteDraft(ctx, id, current.UpdatedTS); err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, true)), nil
	}
	data := DraftMutationData{Phase: "executed", Status: "deleted", Draft: &current}
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
		return provider.Draft{}, &ToolError{Code: "invalid_arguments", Message: "id is required"}
	}
	if draft.ChannelID == "" || strings.TrimSpace(draft.Text) == "" {
		return provider.Draft{}, &ToolError{Code: "invalid_arguments", Message: "channel_id and text are required"}
	}
	return draft, nil
}

func draftDeleteBinding(identity func() provider.ProviderIdentity, draft provider.Draft) (approval.Binding, error) {
	if identity == nil {
		return approval.Binding{}, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	actor := identity()
	if actor.TeamID == "" || actor.UserID == "" || actor.ActorType != "user" {
		return approval.Binding{}, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	arguments, err := approval.CanonicalJSON(struct {
		DraftID string `json:"draft_id"`
	}{DraftID: draft.ID})
	if err != nil {
		return approval.Binding{}, err
	}
	observed, err := approval.CanonicalJSON(draft)
	if err != nil {
		return approval.Binding{}, err
	}
	return approval.Binding{TeamID: actor.TeamID, UserID: actor.UserID, Provider: "local-browser", Tool: "drafts_delete", Arguments: arguments, ObservedState: observed}, nil
}

type SemanticSearchHandler struct {
	api    provider.SemanticSearchAPI
	logger *zap.Logger
	// ChannelName resolves a conversation ID to its cached "#name" or "@user"
	// label for the #channels legend. nil leaves the legend out.
	ChannelName func(channelID string) string
}

func NewSemanticSearchHandler(api provider.SemanticSearchAPI, logger *zap.Logger) *SemanticSearchHandler {
	return &SemanticSearchHandler{api: api, logger: logger}
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
	logToolCall(h.logger, "SemanticSearch called", request)
	var input provider.SemanticSearchRequest
	if err := decodeArguments(request, &input); err != nil {
		return NewTypedErrorResult(err), nil
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "query is required"}), nil
	}
	if input.Limit == 0 {
		input.Limit = 20
	}
	if input.Limit < 1 || input.Limit > 20 {
		return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "limit must be between 1 and 20"}), nil
	}
	for _, contentType := range input.ContentTypes {
		if contentType != "messages" && contentType != "files" {
			return NewTypedErrorResult(&ToolError{Code: "invalid_arguments", Message: "content_types supports only messages and files"}), nil
		}
	}
	page, err := h.api.SearchSemantic(ctx, input)
	if err != nil {
		return NewTypedErrorResult(canvasDraftSearchError(err, false)), nil
	}
	csvText, err := semanticSearchCSV(page.Items, slices.Contains(input.ContentTypes, "files"))
	if err != nil {
		return nil, err
	}
	channelIDs := make([]string, len(page.Items))
	for i, item := range page.Items {
		channelIDs[i] = item.ChannelID
	}
	return NewCSVResult(channelsLegend(channelIDs, h.ChannelName), SlackResultMeta(page.NextCursor, false, ""), csvText), nil
}

func canvasDraftSearchError(err error, mutation bool) error {
	if errors.Is(err, provider.ErrPersistedDraftsUnsupported) {
		return &ToolError{Code: "unsupported", Message: provider.ErrPersistedDraftsUnsupported.Error(), Cause: err}
	}
	var apiError *provider.CapabilityAPIError
	if !errors.As(err, &apiError) {
		return err
	}
	if mutation && apiError.MayHaveMutated {
		return &ToolError{Code: "outcome_unknown", Message: "Slack may have applied the mutation; read current state before another attempt", Cause: err}
	}
	code := apiError.Code
	if code == "" {
		code = "slack_api_error"
	}
	return &ToolError{Code: code, Message: "Slack request failed: " + code, Retryable: apiError.Retryable(), RetryAfter: apiError.RetryAfter, Cause: err}
}
