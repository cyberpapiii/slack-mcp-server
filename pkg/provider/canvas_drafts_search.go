package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/slack-go/slack"
)

var ErrPersistedDraftsUnsupported = errors.New("persisted Slack drafts require a healthy browser session")

type CapabilityAPIError struct {
	Capability     string
	Code           string
	RetryAfter     time.Duration
	MayHaveMutated bool
	Cause          error
}

func (e *CapabilityAPIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("Slack %s failed: %s", e.Capability, e.Code)
	}
	if e.Cause != nil {
		return fmt.Sprintf("Slack %s failed: %v", e.Capability, e.Cause)
	}
	return "Slack " + e.Capability + " failed"
}

func (e *CapabilityAPIError) Unwrap() error { return e.Cause }
func (e *CapabilityAPIError) Retryable() bool {
	return e.RetryAfter > 0 && !e.MayHaveMutated
}

type CanvasCreateRequest struct {
	Title    string `json:"title,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}

type CanvasUpdateRequest struct {
	CanvasID string         `json:"canvas_id"`
	Changes  []CanvasChange `json:"changes"`
}

type CanvasChange struct {
	Operation string `json:"operation"`
	SectionID string `json:"section_id,omitempty"`
	Markdown  string `json:"markdown"`
}

type CanvasReadRequest struct {
	CanvasID     string   `json:"canvas_id"`
	SectionTypes []string `json:"section_types,omitempty"`
	ContainsText string   `json:"contains_text,omitempty"`
}

// CanvasDocument is deliberately metadata-only. Slack's public API can create,
// edit, inspect file metadata, and look up section IDs, but cannot read complete
// canvas content. Preview is only files.info preview data and is never labelled
// as a full export.
type CanvasDocument struct {
	CanvasID             string   `json:"canvas_id"`
	Title                string   `json:"title,omitempty"`
	Filetype             string   `json:"filetype,omitempty"`
	Mimetype             string   `json:"mimetype,omitempty"`
	Permalink            string   `json:"permalink,omitempty"`
	Preview              string   `json:"preview,omitempty"`
	SectionIDs           []string `json:"section_ids,omitempty"`
	FullContentAvailable bool     `json:"full_content_available"`
	Limitation           string   `json:"limitation"`
}

type CanvasAPI interface {
	CreateCanvas(context.Context, CanvasCreateRequest) (string, error)
	ReadCanvas(context.Context, CanvasReadRequest) (CanvasDocument, error)
	UpdateCanvas(context.Context, CanvasUpdateRequest) error
}

var _ canvasSlackAPI = (*MCPSlackClient)(nil)

type canvasSlackAPI interface {
	CreateCanvasContext(context.Context, string, slack.DocumentContent) (string, error)
	EditCanvasContext(context.Context, slack.EditCanvasParams) error
	LookupCanvasSectionsContext(context.Context, slack.LookupCanvasSectionsParams) ([]slack.CanvasSection, error)
	GetFileInfoContext(context.Context, string, int, int) (*slack.File, []slack.Comment, *slack.Paging, error)
}

type CanvasProvider struct{ client canvasSlackAPI }

func NewCanvasProvider(client canvasSlackAPI) *CanvasProvider { return &CanvasProvider{client: client} }

func (ap *ApiProvider) Canvases() (*CanvasProvider, error) {
	client, ok := ap.client.(canvasSlackAPI)
	if !ok {
		return nil, errors.New("configured Slack client does not support canvases")
	}
	return NewCanvasProvider(client), nil
}

func (p *CanvasProvider) CreateCanvas(ctx context.Context, request CanvasCreateRequest) (string, error) {
	id, err := p.client.CreateCanvasContext(ctx, request.Title, slack.DocumentContent{Type: "markdown", Markdown: request.Markdown})
	return id, capabilityError("canvas create", err, true)
}

func (p *CanvasProvider) ReadCanvas(ctx context.Context, request CanvasReadRequest) (CanvasDocument, error) {
	file, _, _, err := p.client.GetFileInfoContext(ctx, request.CanvasID, 0, 0)
	if err != nil {
		return CanvasDocument{}, capabilityError("canvas metadata read", err, false)
	}
	if file == nil {
		return CanvasDocument{}, &CapabilityAPIError{Capability: "canvas metadata read", Code: "empty_response"}
	}
	preview := strings.TrimSpace(file.PreviewPlainText)
	if preview == "" {
		preview = strings.TrimSpace(file.PlainText)
	}
	if preview == "" {
		preview = strings.TrimSpace(file.Preview)
	}
	document := CanvasDocument{
		CanvasID: request.CanvasID, Title: file.Title, Filetype: file.Filetype,
		Mimetype: file.Mimetype, Permalink: file.Permalink, Preview: preview,
		FullContentAvailable: false,
		Limitation:           "Slack Web API does not expose full canvas-content reads; preview and matching section IDs only",
	}
	if len(request.SectionTypes) != 0 || request.ContainsText != "" {
		sections, lookupErr := p.client.LookupCanvasSectionsContext(ctx, slack.LookupCanvasSectionsParams{
			CanvasID: request.CanvasID,
			Criteria: slack.LookupCanvasSectionsCriteria{SectionTypes: request.SectionTypes, ContainsText: request.ContainsText},
		})
		if lookupErr != nil {
			return CanvasDocument{}, capabilityError("canvas section lookup", lookupErr, false)
		}
		document.SectionIDs = make([]string, len(sections))
		for i, section := range sections {
			document.SectionIDs[i] = section.ID
		}
	}
	return document, nil
}

func (p *CanvasProvider) UpdateCanvas(ctx context.Context, request CanvasUpdateRequest) error {
	changes := make([]slack.CanvasChange, len(request.Changes))
	for i, change := range request.Changes {
		changes[i] = slack.CanvasChange{
			Operation: change.Operation, SectionID: change.SectionID,
			DocumentContent: slack.DocumentContent{Type: "markdown", Markdown: change.Markdown},
		}
	}
	err := p.client.EditCanvasContext(ctx, slack.EditCanvasParams{CanvasID: request.CanvasID, Changes: changes})
	return capabilityError("canvas update", err, true)
}

func (c *MCPSlackClient) CreateCanvasContext(ctx context.Context, title string, content slack.DocumentContent) (string, error) {
	return c.standardSlackClient().CreateCanvasContext(ctx, title, content)
}

func (c *MCPSlackClient) EditCanvasContext(ctx context.Context, params slack.EditCanvasParams) error {
	return c.standardSlackClient().EditCanvasContext(ctx, params)
}

func (c *MCPSlackClient) GetFileInfoContext(ctx context.Context, fileID string, count, page int) (*slack.File, []slack.Comment, *slack.Paging, error) {
	return c.standardSlackClient().GetFileInfoContext(ctx, fileID, count, page)
}

func (c *MCPSlackClient) LookupCanvasSectionsContext(ctx context.Context, params slack.LookupCanvasSectionsParams) ([]slack.CanvasSection, error) {
	return c.standardSlackClient().LookupCanvasSectionsContext(ctx, params)
}

type Draft struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	Text      string `json:"text"`
	UpdatedTS string `json:"updated_ts,omitempty"`
}

type DraftPage struct {
	Drafts     []Draft `json:"drafts"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type DraftsAPI interface {
	ListDrafts(context.Context, string, int) (DraftPage, error)
	GetDraft(context.Context, string) (Draft, error)
	CreateDraft(context.Context, Draft) (Draft, error)
	UpdateDraft(context.Context, Draft) (Draft, error)
	DeleteDraft(context.Context, string, string) error
}

type UnsupportedDraftsProvider struct{}

type BrowserDraftsProvider struct{ client *edge.Client }

func (ap *ApiProvider) Drafts() DraftsAPI {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client.edgeClient == nil || !client.browserFeaturesAvailable() {
		return UnsupportedDraftsProvider{}
	}
	return &BrowserDraftsProvider{client: client.edgeClient}
}

func (p *BrowserDraftsProvider) ListDrafts(ctx context.Context, cursor string, limit int) (DraftPage, error) {
	drafts, next, err := p.client.DraftsList(ctx, limit, true, cursor)
	if err != nil {
		return DraftPage{}, capabilityError("draft list", err, false)
	}
	page := DraftPage{Drafts: make([]Draft, len(drafts)), NextCursor: next}
	for i := range drafts {
		page.Drafts[i] = providerDraft(drafts[i])
	}
	return page, nil
}

func (p *BrowserDraftsProvider) GetDraft(ctx context.Context, id string) (Draft, error) {
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		drafts, next, err := p.client.DraftsList(ctx, 100, false, cursor)
		if err != nil {
			return Draft{}, capabilityError("draft read", err, false)
		}
		for _, draft := range drafts {
			if draft.ID == id {
				return providerDraft(draft), nil
			}
		}
		if next == "" || seen[next] {
			break
		}
		seen[next] = true
		cursor = next
	}
	return Draft{}, &CapabilityAPIError{Capability: "draft read", Code: "draft_not_found"}
}

func (p *BrowserDraftsProvider) CreateDraft(ctx context.Context, draft Draft) (Draft, error) {
	created, err := p.client.DraftCreate(ctx, draft.ChannelID, draft.ThreadTS, draft.Text)
	if err != nil {
		return Draft{}, capabilityError("draft create", err, true)
	}
	return providerDraft(created), nil
}

func (p *BrowserDraftsProvider) UpdateDraft(ctx context.Context, draft Draft) (Draft, error) {
	current, err := p.GetDraft(ctx, draft.ID)
	if err != nil {
		return Draft{}, err
	}
	updated, err := p.client.DraftUpdate(ctx, draft.ID, current.UpdatedTS, draft.ChannelID, draft.ThreadTS, draft.Text)
	if err != nil {
		return Draft{}, capabilityError("draft update", err, true)
	}
	return providerDraft(updated), nil
}

func (p *BrowserDraftsProvider) DeleteDraft(ctx context.Context, id, expectedUpdatedTS string) error {
	return capabilityError("draft delete", p.client.DraftDelete(ctx, id, expectedUpdatedTS), true)
}

func providerDraft(draft edge.Draft) Draft {
	result := Draft{ID: draft.ID, UpdatedTS: draft.LastUpdatedTS, Text: draftBlocksText(draft.Blocks)}
	if len(draft.Destinations) != 0 {
		result.ChannelID = draft.Destinations[0].ChannelID
		result.ThreadTS = draft.Destinations[0].ThreadTS
	}
	return result
}

func draftBlocksText(blocks []map[string]any) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text := draftNodeText(block); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func draftNodeText(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		if text, ok := typed["text"].(string); ok && text != "" {
			return text
		}
		elements, ok := typed["elements"]
		if !ok {
			return ""
		}
		separator := ""
		kind, _ := typed["type"].(string)
		if kind == "rich_text" || kind == "rich_text_list" || kind == "rich_text_quote" || kind == "rich_text_preformatted" {
			separator = "\n"
		}
		return joinDraftNodes(elements, separator)
	case []any:
		return joinDraftNodes(typed, "")
	case []map[string]any:
		values := make([]any, len(typed))
		for i := range typed {
			values[i] = typed[i]
		}
		return joinDraftNodes(values, "")
	default:
		return ""
	}
}

func joinDraftNodes(value any, separator string) string {
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case []map[string]any:
		values = make([]any, len(typed))
		for i := range typed {
			values[i] = typed[i]
		}
	default:
		return draftNodeText(value)
	}
	parts := make([]string, 0, len(values))
	for _, item := range values {
		if text := draftNodeText(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, separator)
}

func (UnsupportedDraftsProvider) ListDrafts(context.Context, string, int) (DraftPage, error) {
	return DraftPage{}, ErrPersistedDraftsUnsupported
}
func (UnsupportedDraftsProvider) GetDraft(context.Context, string) (Draft, error) {
	return Draft{}, ErrPersistedDraftsUnsupported
}
func (UnsupportedDraftsProvider) CreateDraft(context.Context, Draft) (Draft, error) {
	return Draft{}, ErrPersistedDraftsUnsupported
}
func (UnsupportedDraftsProvider) UpdateDraft(context.Context, Draft) (Draft, error) {
	return Draft{}, ErrPersistedDraftsUnsupported
}
func (UnsupportedDraftsProvider) DeleteDraft(context.Context, string, string) error {
	return ErrPersistedDraftsUnsupported
}

type SemanticSearchRequest struct {
	Query            string   `json:"query"`
	ContentTypes     []string `json:"content_types,omitempty"`
	ChannelTypes     []string `json:"channel_types,omitempty"`
	ContextChannelID string   `json:"context_channel_id,omitempty"`
	Cursor           string   `json:"cursor,omitempty"`
	IncludeBots      bool     `json:"include_bots,omitempty"`
	Limit            int      `json:"limit,omitempty"`
}

type SemanticSearchItem struct {
	Kind         string `json:"kind"`
	AuthorUserID string `json:"author_user_id,omitempty"`
	TeamID       string `json:"team_id,omitempty"`
	ChannelID    string `json:"channel_id,omitempty"`
	MessageTS    string `json:"message_ts,omitempty"`
	Content      string `json:"content"`
	IsAuthorBot  bool   `json:"is_author_bot,omitempty"`
	Permalink    string `json:"permalink,omitempty"`
	FileID       string `json:"file_id,omitempty"`
	Title        string `json:"title,omitempty"`
	FileType     string `json:"file_type,omitempty"`
}

type SemanticSearchPage struct {
	Items      []SemanticSearchItem `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

type SemanticSearchAPI interface {
	SearchSemantic(context.Context, SemanticSearchRequest) (SemanticSearchPage, error)
}

type semanticSlackAPI interface {
	SearchSemanticContext(context.Context, SemanticSearchRequest) (SemanticSearchPage, error)
}

type SemanticSearchProvider struct{ client semanticSlackAPI }

func NewSemanticSearchProvider(client semanticSlackAPI) *SemanticSearchProvider {
	return &SemanticSearchProvider{client: client}
}

func (ap *ApiProvider) SemanticSearch() (*SemanticSearchProvider, error) {
	client, ok := ap.client.(semanticSlackAPI)
	if !ok {
		return nil, errors.New("configured Slack client does not support semantic search")
	}
	return NewSemanticSearchProvider(client), nil
}

func (p *SemanticSearchProvider) SearchSemantic(ctx context.Context, request SemanticSearchRequest) (SemanticSearchPage, error) {
	page, err := p.client.SearchSemanticContext(ctx, request)
	if err != nil {
		return SemanticSearchPage{}, capabilityError("semantic search", err, false)
	}
	return page, nil
}

func (c *MCPSlackClient) SearchSemanticContext(ctx context.Context, params SemanticSearchRequest) (SemanticSearchPage, error) {
	token, _ := c.oauthAccessToken.Load().(string)
	if token == "" {
		return SemanticSearchPage{}, errors.New("OAuth token unavailable")
	}
	form := url.Values{"token": {token}, "query": {params.Query}}
	for _, value := range params.ChannelTypes {
		form.Add("channel_types", value)
	}
	for _, value := range params.ContentTypes {
		form.Add("content_types", value)
	}
	if params.ContextChannelID != "" {
		form.Set("context_channel_id", params.ContextChannelID)
	}
	if params.Cursor != "" {
		form.Set("cursor", params.Cursor)
	}
	if params.IncludeBots {
		form.Set("include_bots", "true")
	}
	if params.Limit > 0 {
		form.Set("limit", strconv.Itoa(params.Limit))
	}
	endpoint := strings.TrimRight(c.authResponse.URL, "/") + "/api/assistant.search.context"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return SemanticSearchPage{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpClient, err := c.authProvider.HTTPClient()
	if err != nil {
		return SemanticSearchPage{}, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return SemanticSearchPage{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		seconds, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		return SemanticSearchPage{}, &slack.RateLimitedError{RetryAfter: time.Duration(seconds) * time.Second}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return SemanticSearchPage{}, fmt.Errorf("Slack semantic search HTTP %d", response.StatusCode)
	}
	var payload struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Results struct {
			Messages []struct {
				AuthorUserID string `json:"author_user_id"`
				TeamID       string `json:"team_id"`
				ChannelID    string `json:"channel_id"`
				MessageTS    string `json:"message_ts"`
				Content      string `json:"content"`
				IsAuthorBot  bool   `json:"is_author_bot"`
				Permalink    string `json:"permalink"`
			} `json:"messages"`
			Files []struct {
				UploaderUserID string `json:"uploader_user_id"`
				AuthorUserID   string `json:"author_user_id"`
				TeamID         string `json:"team_id"`
				FileID         string `json:"file_id"`
				Title          string `json:"title"`
				FileType       string `json:"file_type"`
				Content        string `json:"content"`
				Permalink      string `json:"permalink"`
			} `json:"files"`
		} `json:"results"`
		ResponseMetadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	if err := decoder.Decode(&payload); err != nil {
		return SemanticSearchPage{}, err
	}
	if !payload.OK {
		return SemanticSearchPage{}, errors.New(payload.Error)
	}
	page := SemanticSearchPage{NextCursor: payload.ResponseMetadata.NextCursor, Items: make([]SemanticSearchItem, 0, len(payload.Results.Messages)+len(payload.Results.Files))}
	for _, item := range payload.Results.Messages {
		page.Items = append(page.Items, SemanticSearchItem{Kind: "message", AuthorUserID: item.AuthorUserID, TeamID: item.TeamID, ChannelID: item.ChannelID, MessageTS: item.MessageTS, Content: item.Content, IsAuthorBot: item.IsAuthorBot, Permalink: item.Permalink})
	}
	for _, item := range payload.Results.Files {
		author := item.AuthorUserID
		if author == "" {
			author = item.UploaderUserID
		}
		page.Items = append(page.Items, SemanticSearchItem{Kind: "file", AuthorUserID: author, TeamID: item.TeamID, FileID: item.FileID, Title: item.Title, FileType: item.FileType, Content: item.Content, Permalink: item.Permalink})
	}
	return page, nil
}

func capabilityError(capability string, err error, mutation bool) error {
	if err == nil {
		return nil
	}
	result := &CapabilityAPIError{Capability: capability, Cause: err}
	var slackError slack.SlackErrorResponse
	if errors.As(err, &slackError) {
		result.Code = slackError.Err
	}
	var limited *slack.RateLimitedError
	if errors.As(err, &limited) {
		result.Code = "rate_limited"
		result.RetryAfter = limited.RetryAfter
	}
	if mutation && (errors.Is(err, context.DeadlineExceeded) || isTimeout(err)) {
		result.MayHaveMutated = true
	}
	return result
}

type timeoutError interface{ Timeout() bool }

func isTimeout(err error) bool {
	var timeout timeoutError
	return errors.As(err, &timeout) && timeout.Timeout()
}
