package provider

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeCanvasSlack struct {
	content        string
	contentErr     error
	contentFormat  string
	contentCalls   int
	createdTitle   string
	createdContent slack.DocumentContent
	edit           slack.EditCanvasParams
	lookup         slack.LookupCanvasSectionsParams
	file           *slack.File
	err            error
}

func (f *fakeCanvasSlack) CreateCanvasContext(_ context.Context, title string, content slack.DocumentContent) (string, error) {
	f.createdTitle, f.createdContent = title, content
	return "F123", f.err
}
func (f *fakeCanvasSlack) EditCanvasContext(_ context.Context, params slack.EditCanvasParams) error {
	f.edit = params
	return f.err
}
func (f *fakeCanvasSlack) LookupCanvasSectionsContext(_ context.Context, params slack.LookupCanvasSectionsParams) ([]slack.CanvasSection, error) {
	f.lookup = params
	return []slack.CanvasSection{{ID: "S1"}}, f.err
}
func (f *fakeCanvasSlack) GetCanvasContentContext(_ context.Context, _, format string) (string, error) {
	f.contentFormat = format
	f.contentCalls++
	return f.content, f.contentErr
}
func (f *fakeCanvasSlack) GetFileInfoContext(context.Context, string, int, int) (*slack.File, []slack.Comment, *slack.Paging, error) {
	return f.file, nil, nil, f.err
}

func TestCanvasProviderUsesPublicCanvasAPIsTruthfully(t *testing.T) {
	fake := &fakeCanvasSlack{file: &slack.File{ID: "F123", Title: "Plan", Filetype: "quip", PreviewPlainText: "preview", Permalink: "https://example.slack.com/docs/F123"}}
	service := NewCanvasProvider(fake)

	id, err := service.CreateCanvas(context.Background(), CanvasCreateRequest{Title: "Plan", Markdown: "# Plan"})
	require.NoError(t, err)
	assert.Equal(t, "F123", id)
	assert.Equal(t, "markdown", fake.createdContent.Type)

	fake.content = "# Plan\nAll of it"
	document, err := service.ReadCanvas(context.Background(), CanvasReadRequest{CanvasID: "F123", ContainsText: "decision"})
	require.NoError(t, err)
	assert.True(t, document.FullContentAvailable)
	assert.Equal(t, "# Plan\nAll of it", document.Content)
	assert.Equal(t, "markdown", document.ContentFormat)
	assert.Equal(t, "preview", document.Preview)
	assert.Equal(t, []string{"S1"}, document.SectionIDs)
	assert.Empty(t, document.Limitation)

	fake.contentErr = ErrUserOAuthRequired
	document, err = service.ReadCanvas(context.Background(), CanvasReadRequest{CanvasID: "F123"})
	require.NoError(t, err)
	assert.False(t, document.FullContentAvailable)
	assert.Empty(t, document.Content)
	assert.Equal(t, "preview", document.Preview)
	assert.Contains(t, document.Limitation, "requires user OAuth")
	fake.contentErr = nil

	fake.contentCalls = 0
	document, err = service.ReadCanvas(context.Background(), CanvasReadRequest{CanvasID: "F123", ContentFormat: "none"})
	require.NoError(t, err)
	assert.Zero(t, fake.contentCalls)
	assert.Empty(t, document.Limitation)

	_, err = service.ReadCanvas(context.Background(), CanvasReadRequest{CanvasID: "F123", ContentFormat: "html"})
	require.NoError(t, err)
	assert.Equal(t, "html", fake.contentFormat)

	err = service.UpdateCanvas(context.Background(), CanvasUpdateRequest{CanvasID: "F123", Changes: []CanvasChange{{Operation: "replace", SectionID: "S1", Markdown: "new"}}})
	require.NoError(t, err)
	assert.Equal(t, "F123", fake.edit.CanvasID)
	assert.Equal(t, "replace", fake.edit.Changes[0].Operation)
}

type fakeSemanticSlack struct {
	params SemanticSearchRequest
	result SemanticSearchPage
	err    error
}

func (f *fakeSemanticSlack) SearchSemanticContext(_ context.Context, params SemanticSearchRequest) (SemanticSearchPage, error) {
	f.params = params
	return f.result, f.err
}

func TestSemanticSearchProviderMapsAssistantContext(t *testing.T) {
	response := SemanticSearchPage{Items: []SemanticSearchItem{{Kind: "message", ChannelID: "C1", MessageTS: "1.2", Content: "launch plan", Permalink: "https://example.slack.com/archives/C1/p12"}, {Kind: "file", FileID: "F1", Content: "launch brief"}}, NextCursor: "next"}
	fake := &fakeSemanticSlack{result: response}
	service := NewSemanticSearchProvider(fake)

	page, err := service.SearchSemantic(context.Background(), SemanticSearchRequest{Query: "launch", ContentTypes: []string{"messages", "files"}, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, []string{"messages", "files"}, fake.params.ContentTypes)
	assert.Equal(t, "next", page.NextCursor)
	assert.Equal(t, "launch plan", page.Items[0].Content)
	assert.Equal(t, "F1", page.Items[1].FileID)
}

func TestUnitGetCanvasContentCallsSlackAndReportsItsError(t *testing.T) {
	var form url.Values
	body := `{"ok":true,"content":"# Plan"}`
	client := &MCPSlackClient{
		authResponse: slack.AuthTestResponse{URL: "https://example.slack.com/"},
		httpClient: &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			assert.Equal(t, "https://example.slack.com/api/canvases.getContent", request.URL.String())
			require.NoError(t, request.ParseForm())
			form = request.PostForm
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})},
	}

	web := &WebClient{client: client}
	_, err := web.GetCanvasContentContext(context.Background(), "F1", "markdown")
	require.ErrorIs(t, err, ErrUserOAuthRequired, "a browser-session client has no OAuth token to send")

	client.oauthAccessToken.Store("xoxp-test")
	content, err := web.GetCanvasContentContext(context.Background(), "F1", "html")
	require.NoError(t, err)
	assert.Equal(t, "# Plan", content)
	assert.Equal(t, "F1", form.Get("canvas_id"))
	assert.Equal(t, "html", form.Get("content_type"))

	body = `{"ok":false,"error":"canvas_not_found"}`
	_, err = web.GetCanvasContentContext(context.Background(), "F1", "markdown")
	assert.ErrorContains(t, err, "canvas_not_found")
}

type draftsRoundTrip func(*http.Request) (*http.Response, error)

func (f draftsRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A drafts failure reaches the tool with Slack's error code.
func TestUnitDraftsErrorCarriesSlackCode(t *testing.T) {
	client := draftsClient(`{"ok":false,"error":"channel_not_found"}`)
	_, err := (&BrowserDraftsProvider{client: client}).ListDrafts(context.Background(), "", 10)
	var slackErr slack.SlackErrorResponse
	require.ErrorAs(t, err, &slackErr)
	assert.Equal(t, "channel_not_found", slackErr.Err)
}

func draftsClient(body string) *MCPSlackClient {
	c := &MCPSlackClient{}
	setUpDraftsClient(c, body)
	return c
}

func setUpDraftsClient(c *MCPSlackClient, body string) {
	c.mode, c.logger = ModeUserOAuthBrowser, zap.NewNop()
	c.initBrowserState()
	c.edgeClient.Store(edgeAnswering(body))
}

func edgeAnswering(body string) *edge.Client {
	return edge.NewWithInfo(&slack.AuthTestResponse{TeamID: "T1", URL: "https://example.slack.com/"}, "xoxc-test",
		&http.Client{Transport: draftsRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
}

// Drafts used to capture the edge client once at startup, so a session the
// re-probe restored never reached them.
func TestUnitDraftsFollowARecoveredBrowserSession(t *testing.T) {
	origNotifier := browserDegradationNotifier
	t.Cleanup(func() { browserDegradationNotifier = origNotifier })
	browserDegradationNotifier = func(string, *zap.Logger) {}

	ap := &ApiProvider{}
	client := &ap.client
	setUpDraftsClient(client, `{"ok":false,"error":"invalid_auth"}`)
	drafts := ap.Drafts()

	_, err := drafts.ListDrafts(context.Background(), "", 10)
	require.ErrorIs(t, err, ErrBrowserSessionUnavailable, "dead auth degrades the session")
	require.False(t, client.browserFeaturesAvailable())

	client.useBrowserEdge(edgeAnswering(`{"ok":true,"drafts":[{"id":"Dr1","destinations":[{"channel_id":"C1"}]}]}`))
	page, err := drafts.ListDrafts(context.Background(), "", 10)
	require.NoError(t, err)
	require.Len(t, page.Drafts, 1)
	assert.Equal(t, "C1", page.Drafts[0].ChannelID)
}
