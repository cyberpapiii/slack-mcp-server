package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
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

type CanvasCreateResult = ToolResult[CanvasMutationData]
type CanvasReadResult = ToolResult[provider.CanvasDocument]
type CanvasUpdateResult = ToolResult[CanvasMutationData]

func (h *CanvasHandler) Create(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "CanvasCreate called", request)
	if !requireToolEnabled("SLACK_MCP_CANVAS_WRITE_TOOL", "canvases_create") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "canvases_create is disabled"}), nil
	}
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
	if !requireToolEnabled("SLACK_MCP_CANVAS_WRITE_TOOL", "canvases_update") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "canvases_update is disabled"}), nil
	}
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

type DraftPageResult = ToolResult[provider.DraftPage]
type DraftResult = ToolResult[provider.Draft]
type DraftMutationResult = ToolResult[DraftMutationData]

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
	return NewStructuredResult(page, SlackResultMeta(page.NextCursor, false, ""), fallbackJSON(page)), nil
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
	if !requireToolEnabled("SLACK_MCP_DRAFT_WRITE_TOOL", "drafts_create") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "drafts_create is disabled"}), nil
	}
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
	if !requireToolEnabled("SLACK_MCP_DRAFT_WRITE_TOOL", "drafts_update") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "drafts_update is disabled"}), nil
	}
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
	if !requireToolEnabled("SLACK_MCP_DRAFT_WRITE_TOOL", "drafts_delete") {
		return NewTypedErrorResult(&ToolError{Code: "tool_disabled", Message: "drafts_delete is disabled"}), nil
	}
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
	if action == "prepare" {
		prepared, prepareErr := h.approvals.Prepare(binding)
		if prepareErr != nil {
			return NewTypedErrorResult(prepareErr), nil
		}
		data := DraftMutationData{Phase: "prepared", Status: "awaiting_confirmation", Draft: &current, ApprovalToken: prepared.Token, ExpiresAt: prepared.ExpiresAt.Format(time.RFC3339)}
		return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
	}
	if _, err := h.approvals.Consume(strings.TrimSpace(request.GetString("approval_token", "")), binding); err != nil {
		return NewTypedErrorResult(&ToolError{Code: "approval_invalid", Message: err.Error(), Cause: err}), nil
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
}

func NewSemanticSearchHandler(api provider.SemanticSearchAPI, logger *zap.Logger) *SemanticSearchHandler {
	return &SemanticSearchHandler{api: api, logger: logger}
}

type SemanticSearchResult = ToolResult[provider.SemanticSearchPage]

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
	return NewStructuredResult(page, SlackResultMeta(page.NextCursor, false, ""), fallbackJSON(page)), nil
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
