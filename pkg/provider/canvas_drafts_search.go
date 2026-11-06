package provider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/slackhttp"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/slack-go/slack"
)

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
	// ContentFormat is markdown (the default), html, or none for metadata only.
	ContentFormat string `json:"content_format,omitempty"`
}

// CanvasDocument carries the whole canvas in Content when canvases.getContent
// answered. That method rejects browser-session tokens, so a read without user
// OAuth falls back to Preview, which is only files.info preview data and is
// never labelled as a full export.
type CanvasDocument struct {
	CanvasID             string   `json:"canvas_id"`
	Title                string   `json:"title,omitempty"`
	Filetype             string   `json:"filetype,omitempty"`
	Mimetype             string   `json:"mimetype,omitempty"`
	Permalink            string   `json:"permalink,omitempty"`
	Preview              string   `json:"preview,omitempty"`
	Content              string   `json:"content,omitempty"`
	ContentFormat        string   `json:"content_format,omitempty"`
	SectionIDs           []string `json:"section_ids,omitempty"`
	FullContentAvailable bool     `json:"full_content_available"`
	Limitation           string   `json:"limitation,omitempty"`
}

type CanvasAPI interface {
	CreateCanvas(context.Context, CanvasCreateRequest) (string, error)
	ReadCanvas(context.Context, CanvasReadRequest) (CanvasDocument, error)
	UpdateCanvas(context.Context, CanvasUpdateRequest) error
}

var _ canvasSlackAPI = (*WebClient)(nil)

type canvasSlackAPI interface {
	CreateCanvasContext(context.Context, string, slack.DocumentContent) (string, error)
	EditCanvasContext(context.Context, slack.EditCanvasParams) error
	LookupCanvasSectionsContext(context.Context, slack.LookupCanvasSectionsParams) ([]slack.CanvasSection, error)
	GetFileInfoContext(context.Context, string, int, int) (*slack.File, []slack.Comment, *slack.Paging, error)
	GetCanvasContentContext(ctx context.Context, canvasID, format string) (string, error)
}

type CanvasProvider struct{ client canvasSlackAPI }

func NewCanvasProvider(client canvasSlackAPI) *CanvasProvider { return &CanvasProvider{client: client} }

func (ap *ApiProvider) Canvases() *CanvasProvider {
	return NewCanvasProvider(ap.WebAPI())
}

func (p *CanvasProvider) CreateCanvas(ctx context.Context, request CanvasCreateRequest) (string, error) {
	id, err := p.client.CreateCanvasContext(ctx, request.Title, slack.DocumentContent{Type: "markdown", Markdown: request.Markdown})
	return id, wrapSlack("canvas create", err)
}

func (p *CanvasProvider) ReadCanvas(ctx context.Context, request CanvasReadRequest) (CanvasDocument, error) {
	file, _, _, err := p.client.GetFileInfoContext(ctx, request.CanvasID, 0, 0)
	if err != nil {
		return CanvasDocument{}, wrapSlack("canvas metadata read", err)
	}
	if file == nil {
		return CanvasDocument{}, errors.New("canvas metadata read: Slack returned no file")
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
	}
	switch format := request.ContentFormat; format {
	case "none":
	case "", "markdown", "html":
		if format == "" {
			format = "markdown"
		}
		content, contentErr := p.client.GetCanvasContentContext(ctx, request.CanvasID, format)
		switch {
		case contentErr == nil:
			document.Content, document.ContentFormat, document.FullContentAvailable = content, format, true
		case canvasContentRefused(contentErr):
			document.Limitation = "full canvas content unavailable (" + contentErr.Error() + "); preview and matching section IDs only"
		default:
			return CanvasDocument{}, wrapSlack("canvas content read", contentErr)
		}
	default:
		return CanvasDocument{}, fmt.Errorf("unknown canvas content format %q", format)
	}
	if len(request.SectionTypes) != 0 || request.ContainsText != "" {
		sections, lookupErr := p.client.LookupCanvasSectionsContext(ctx, slack.LookupCanvasSectionsParams{
			CanvasID: request.CanvasID,
			Criteria: slack.LookupCanvasSectionsCriteria{SectionTypes: request.SectionTypes, ContainsText: request.ContainsText},
		})
		if lookupErr != nil {
			return CanvasDocument{}, wrapSlack("canvas section lookup", lookupErr)
		}
		document.SectionIDs = make([]string, len(sections))
		for i, section := range sections {
			document.SectionIDs[i] = section.ID
		}
	}
	return document, nil
}

// canvasContentRefused reports whether canvases.getContent refused the token
// itself: a browser-session token, or a user token without the scope. The
// preview still answers then; any other failure is the read failing.
func canvasContentRefused(err error) bool {
	var slackErr slack.SlackErrorResponse
	if !errors.As(err, &slackErr) {
		return false
	}
	switch slackErr.Err {
	case "not_allowed_token_type", "missing_scope":
		return true
	default:
		return false
	}
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
	return wrapSlack("canvas update", err)
}

func (c *WebClient) CreateCanvasContext(ctx context.Context, title string, content slack.DocumentContent) (string, error) {
	return c.resolve().CreateCanvasContext(ctx, title, content)
}

func (c *WebClient) EditCanvasContext(ctx context.Context, params slack.EditCanvasParams) error {
	return c.resolve().EditCanvasContext(ctx, params)
}

// GetCanvasContentContext calls canvases.getContent, which slack-go does not wrap
// yet. Slack answers not_allowed_token_type to a browser-session token.
func (c *WebClient) GetCanvasContentContext(ctx context.Context, canvasID, format string) (string, error) {
	body, err := slackhttp.Post(ctx, c.client.httpClient, slackhttp.Request{
		URL:   c.client.apiBase() + "canvases.getContent",
		Token: c.token(),
		Form:  url.Values{"canvas_id": {canvasID}, "content_type": {format}},
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := slackhttp.Decode(body, &payload); err != nil {
		var slackErr slack.SlackErrorResponse
		if errors.As(err, &slackErr) {
			return "", err
		}
		return "", fmt.Errorf("decode canvases.getContent response: %w", err)
	}
	return payload.Content, nil
}

func (c *WebClient) LookupCanvasSectionsContext(ctx context.Context, params slack.LookupCanvasSectionsParams) ([]slack.CanvasSection, error) {
	return c.resolve().LookupCanvasSectionsContext(ctx, params)
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

// BrowserDraftsProvider reaches Slack drafts through the browser session. Each
// call takes the session's current edge client, so drafts follow a session the
// re-probe brought back.
type BrowserDraftsProvider struct{ client *MCPSlackClient }

func (ap *ApiProvider) Drafts() DraftsAPI {
	return &BrowserDraftsProvider{client: &ap.client}
}

type draftsListPage struct {
	drafts []edge.Draft
	next   string
}

func (p *BrowserDraftsProvider) list(ctx context.Context, limit int, activeOnly bool, cursor string) ([]edge.Draft, string, error) {
	page, err := browserCall(p.client, "drafts.list", func() (draftsListPage, error) {
		drafts, next, err := p.client.edgeClient.Load().DraftsList(ctx, limit, activeOnly, cursor)
		return draftsListPage{drafts: drafts, next: next}, err
	})
	return page.drafts, page.next, err
}

func (p *BrowserDraftsProvider) ListDrafts(ctx context.Context, cursor string, limit int) (DraftPage, error) {
	drafts, next, err := p.list(ctx, limit, true, cursor)
	if err != nil {
		return DraftPage{}, wrapSlack("draft list", err)
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
		drafts, next, err := p.list(ctx, 100, false, cursor)
		if err != nil {
			return Draft{}, wrapSlack("draft read", err)
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
	return Draft{}, slack.SlackErrorResponse{Err: "draft_not_found"}
}

func (p *BrowserDraftsProvider) CreateDraft(ctx context.Context, draft Draft) (Draft, error) {
	created, err := browserCall(p.client, "drafts.create", func() (edge.Draft, error) {
		return p.client.edgeClient.Load().DraftCreate(ctx, draft.ChannelID, draft.ThreadTS, draft.Text)
	})
	if err != nil {
		return Draft{}, wrapSlack("draft create", err)
	}
	return providerDraft(created), nil
}

func (p *BrowserDraftsProvider) UpdateDraft(ctx context.Context, draft Draft) (Draft, error) {
	current, err := p.GetDraft(ctx, draft.ID)
	if err != nil {
		return Draft{}, err
	}
	updated, err := browserCall(p.client, "drafts.update", func() (edge.Draft, error) {
		return p.client.edgeClient.Load().DraftUpdate(ctx, draft.ID, current.UpdatedTS, draft.ChannelID, draft.ThreadTS, draft.Text)
	})
	if err != nil {
		return Draft{}, wrapSlack("draft update", err)
	}
	return providerDraft(updated), nil
}

func (p *BrowserDraftsProvider) DeleteDraft(ctx context.Context, id, expectedUpdatedTS string) error {
	_, err := browserCall(p.client, "drafts.delete", func() (struct{}, error) {
		return struct{}{}, p.client.edgeClient.Load().DraftDelete(ctx, id, expectedUpdatedTS)
	})
	return wrapSlack("draft delete", err)
}

func providerDraft(draft edge.Draft) Draft {
	result := Draft{ID: draft.ID, UpdatedTS: draft.LastUpdatedTS, Text: text.BlocksToText(draft.Blocks)}
	if len(draft.Destinations) != 0 {
		result.ChannelID = draft.Destinations[0].ChannelID
		result.ThreadTS = draft.Destinations[0].ThreadTS
	}
	return result
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

func (ap *ApiProvider) SemanticSearch() *SemanticSearchProvider {
	return NewSemanticSearchProvider(ap.WebAPI())
}

func (p *SemanticSearchProvider) SearchSemantic(ctx context.Context, request SemanticSearchRequest) (SemanticSearchPage, error) {
	page, err := p.client.SearchSemanticContext(ctx, request)
	if err != nil {
		return SemanticSearchPage{}, wrapSlack("semantic search", err)
	}
	return page, nil
}

func (c *WebClient) SearchSemanticContext(ctx context.Context, params SemanticSearchRequest) (SemanticSearchPage, error) {
	form := url.Values{"query": {params.Query}}
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
	body, err := slackhttp.Post(ctx, c.client.httpClient, slackhttp.Request{
		URL:   c.client.apiBase() + "assistant.search.context",
		Token: c.token(),
		Form:  form,
	})
	if err != nil {
		return SemanticSearchPage{}, err
	}
	var payload struct {
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
	if err := slackhttp.Decode(body, &payload); err != nil {
		return SemanticSearchPage{}, err
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

// wrapSlack names the operation that failed, keeping the Slack error
// underneath for the handler's error mapping.
func wrapSlack(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("Slack %s failed: %w", operation, err)
}
