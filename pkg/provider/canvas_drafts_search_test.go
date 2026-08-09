package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCanvasSlack struct {
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

	document, err := service.ReadCanvas(context.Background(), CanvasReadRequest{CanvasID: "F123", ContainsText: "decision"})
	require.NoError(t, err)
	assert.False(t, document.FullContentAvailable)
	assert.Equal(t, "preview", document.Preview)
	assert.Equal(t, []string{"S1"}, document.SectionIDs)
	assert.Contains(t, document.Limitation, "does not expose full canvas-content reads")

	err = service.UpdateCanvas(context.Background(), CanvasUpdateRequest{CanvasID: "F123", Changes: []CanvasChange{{Operation: "replace", SectionID: "S1", Markdown: "new"}}})
	require.NoError(t, err)
	assert.Equal(t, "F123", fake.edit.CanvasID)
	assert.Equal(t, "replace", fake.edit.Changes[0].Operation)
}

func TestUnsupportedDraftsProviderAlwaysFailsClosed(t *testing.T) {
	service := UnsupportedDraftsProvider{}
	_, err := service.ListDrafts(context.Background(), "", 20)
	assert.ErrorIs(t, err, ErrPersistedDraftsUnsupported)
	_, err = service.CreateDraft(context.Background(), Draft{ChannelID: "C1", Text: "x"})
	assert.ErrorIs(t, err, ErrPersistedDraftsUnsupported)
	assert.ErrorIs(t, service.DeleteDraft(context.Background(), "D1", "1.0000000"), ErrPersistedDraftsUnsupported)
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

type timeoutFailure struct{}

func (timeoutFailure) Error() string   { return "timeout" }
func (timeoutFailure) Timeout() bool   { return true }
func (timeoutFailure) Temporary() bool { return true }

func TestCapabilityErrorMarksOnlyAmbiguousMutation(t *testing.T) {
	mutation := capabilityError("canvas update", timeoutFailure{}, true)
	var mutationError *CapabilityAPIError
	require.ErrorAs(t, mutation, &mutationError)
	assert.True(t, mutationError.MayHaveMutated)

	read := capabilityError("semantic search", timeoutFailure{}, false)
	var readError *CapabilityAPIError
	require.ErrorAs(t, read, &readError)
	assert.False(t, readError.MayHaveMutated)

	limited := capabilityError("semantic search", &slack.RateLimitedError{RetryAfter: 2 * time.Second}, false)
	var limitedError *CapabilityAPIError
	require.True(t, errors.As(limited, &limitedError))
	assert.True(t, limitedError.Retryable())
}
