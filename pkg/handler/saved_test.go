package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

const savedItemsHeader = "Channel,MsgID,DateCreated,DateDue,State"

func savedFixtureItems() []SavedItemRow {
	return []SavedItemRow{
		{
			ChannelID:   "C0AJMCRNH0U",
			Ts:          "1772680334.954409",
			DateCreated: "2026-03-03 12:00",
			DateDue:     "",
			State:       "in_progress",
		},
		{
			ChannelID:   "D0AGSQXLJHG",
			Ts:          "1771941381.234049",
			DateCreated: "2026-02-25 08:30",
			DateDue:     "2026-03-01 15:00",
			State:       "in_progress",
		},
	}
}

func savedFixtureChannelName(id string) string {
	return map[string]string{"C0AJMCRNH0U": "#wg-feast-ui", "D0AGSQXLJHG": "@john"}[id]
}

func TestUnitRenderSavedItems(t *testing.T) {
	t.Run("rows carry the bare channel ID, names go in the legend, cursor in meta", func(t *testing.T) {
		res, err := renderSavedItems(savedFixtureItems(), savedFixtureChannelName, SlackResultMeta("next-page", false, ""))
		require.NoError(t, err)
		assert.Nil(t, res.StructuredContent)

		lines := strings.Split(csvResultBody(t, res), "\n")
		assert.Equal(t, "#channels: C0AJMCRNH0U=#wg-feast-ui, D0AGSQXLJHG=@john", lines[0])
		assert.Equal(t, "#next_cursor: next-page", lines[1])
		assert.Equal(t, savedItemsHeader, lines[2])
		assert.Equal(t, "C0AJMCRNH0U,1772680334.954409,2026-03-03 12:00,,in_progress", lines[3])
		assert.Equal(t, "D0AGSQXLJHG,1771941381.234049,2026-02-25 08:30,2026-03-01 15:00,in_progress", lines[4])
	})

	t.Run("last page has no cursor line", func(t *testing.T) {
		res, err := renderSavedItems(savedFixtureItems(), savedFixtureChannelName, SlackResultMeta("", false, ""))
		require.NoError(t, err)
		body := csvResultBody(t, res)
		assert.NotContains(t, body, "#next_cursor:")
		assert.True(t, strings.HasPrefix(body, "#channels: "), body)
	})

	t.Run("no items is a header-only CSV", func(t *testing.T) {
		res, err := renderSavedItems(nil, savedFixtureChannelName, SlackResultMeta("", false, ""))
		require.NoError(t, err)
		assert.Equal(t, savedItemsHeader+"\n", csvResultBody(t, res))
	})
}

func TestUnitSavedMessagesTrailer(t *testing.T) {
	messages := compactCSVFixtureMessagesN(1)
	items := savedFixtureItems()
	trailer, err := csvSection("saved_items", &items)
	require.NoError(t, err)
	res, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard, meta: SlackResultMeta("next-page", false, ""), trailer: trailer})
	require.NoError(t, err)
	assert.Nil(t, res.StructuredContent)

	lines := strings.Split(csvResultBody(t, res), "\n")
	assert.True(t, strings.HasPrefix(lines[0], "#attachments: "), "the fixture message carries a file: %s", lines[0])
	assert.Equal(t, "#next_cursor: next-page", lines[1])
	assert.True(t, strings.HasPrefix(lines[2], "User,Channel,"), lines[2])
	assert.Equal(t, "#saved_items:", lines[4])
	assert.Equal(t, savedItemsHeader, lines[5])
	assert.Len(t, lines, 9, "two item rows follow the section header; body ends with a newline")
}

func TestUnitFormatUnixTs(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected string
	}{
		{
			name:     "zero returns empty string",
			input:    0,
			expected: "",
		},
		{
			name:     "valid timestamp formats correctly",
			input:    1772720838,
			expected: "2026-03-05 14:27",
		},
		{
			name:     "another valid timestamp",
			input:    1772521200,
			expected: "2026-03-03 07:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatUnixTs(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Schema documents date_due: 0 to clear a due date; explicit zero must differ from absent key.
func TestUnitParseSavedUpdateParams(t *testing.T) {
	tests := []struct {
		name        string
		args        map[string]any
		wantErr     string
		wantMark    string
		wantDateDue int64
	}{
		{
			name:        "explicit date_due 0 with no mark clears the due date",
			args:        map[string]any{"channel_id": "C1", "timestamp": "1.0", "date_due": 0},
			wantMark:    "",
			wantDateDue: 0,
		},
		{
			name:        "explicit date_due 0 as json float clears the due date",
			args:        map[string]any{"channel_id": "C1", "timestamp": "1.0", "date_due": float64(0)},
			wantMark:    "",
			wantDateDue: 0,
		},
		{
			name:    "neither mark nor date_due is rejected",
			args:    map[string]any{"channel_id": "C1", "timestamp": "1.0"},
			wantErr: "at least one of mark or date_due must be provided",
		},
		{
			name:        "mark alone is accepted",
			args:        map[string]any{"channel_id": "C1", "timestamp": "1.0", "mark": "completed"},
			wantMark:    "completed",
			wantDateDue: 0,
		},
		{
			name:        "date_due alone is accepted",
			args:        map[string]any{"channel_id": "C1", "timestamp": "1.0", "date_due": 1772521200},
			wantMark:    "",
			wantDateDue: 1772521200,
		},
		{
			name:    "missing channel_id is rejected",
			args:    map[string]any{"timestamp": "1.0", "mark": "completed"},
			wantErr: "channel_id and timestamp are required parameters",
		},
		{
			name:    "missing ts is rejected",
			args:    map[string]any{"channel_id": "C1", "mark": "completed"},
			wantErr: "channel_id and timestamp are required parameters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tt.args

			itemID, ts, mark, dateDue, err := parseSavedUpdateParams(req)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "C1", itemID)
			assert.Equal(t, "1.0", ts)
			assert.Equal(t, tt.wantMark, mark)
			assert.Equal(t, tt.wantDateDue, dateDue)
		})
	}
}

func TestSavedHandlersFailFastWhenBrowserUnavailable(t *testing.T) {
	h := NewSavedHandler(&provider.ApiProvider{}, zap.NewNop(), nil)
	ctx := context.Background()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"filter": "saved"}
	_, err := h.SavedListHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "browser session (xoxc/xoxd)")

	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"channel_id": "C1", "timestamp": "1.0", "mark": "completed"}
	_, err = h.SavedUpdateHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "browser session (xoxc/xoxd)")

	req = mcp.CallToolRequest{}
	_, err = h.SavedClearCompletedHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "browser session (xoxc/xoxd)")
}

// newStubSavedHandler serves one saved.list page per call from pages and
// runs onPage after each; itemMessages returns one message per item and runs
// onItem first.
func newStubSavedHandler(pages []edge.SavedListResponse, onPage func(n int), onItem func(edge.SavedItem)) *SavedHandler {
	calls := 0
	return &SavedHandler{
		apiProvider: &provider.ApiProvider{},
		logger:      zap.NewNop(),
		convHandler: &ConversationsHandler{apiProvider: &provider.ApiProvider{}, logger: zap.NewNop()},
		pace:        rate.NewLimiter(rate.Inf, 0),
		listPage: func(context.Context, string, int, string) (edge.SavedListResponse, error) {
			page := pages[calls]
			calls++
			if onPage != nil {
				onPage(calls)
			}
			return page, nil
		},
		itemMessages: func(_ context.Context, item edge.SavedItem, _ int, _ text.OutputMode) ([]Message, error) {
			if onItem != nil {
				onItem(item)
			}
			return []Message{{MsgID: item.Ts, Channel: item.ItemID, Text: "saved " + item.Ts}}, nil
		},
	}
}

func savedPage(nextCursor string, ts ...string) edge.SavedListResponse {
	var page edge.SavedListResponse
	for _, t := range ts {
		page.SavedItems = append(page.SavedItems, edge.SavedItem{ItemID: "C1", Ts: t, State: "in_progress"})
	}
	page.ResponseMetadata.NextCursor = nextCursor
	return page
}

func TestUnitSavedListKeepsRowsWhenStoppedBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newStubSavedHandler([]edge.SavedListResponse{savedPage("c2", "1.0", "2.0")}, func(int) { cancel() }, nil)

	res, err := h.listSaved(ctx, "saved", 50, "", false, 5, text.ModeStandard)
	require.NoError(t, err)
	require.False(t, res.IsError, "the first page was listed; a stop before page two must not discard it")
	out := ResultText(res)
	assert.Contains(t, out, "#partial: ")
	assert.Contains(t, out, "#next_cursor: c2", "resume from the page that was not fetched")
	assert.Contains(t, out, "C1,1.0,")
	assert.Contains(t, out, "C1,2.0,")
}

func TestUnitSavedListListsTheRestOfThePageWhenStoppedMidPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newStubSavedHandler([]edge.SavedListResponse{savedPage("c2", "1.0", "2.0", "3.0")}, nil, func(edge.SavedItem) { cancel() })

	res, err := h.listSaved(ctx, "saved", 50, "", true, 5, text.ModeStandard)
	require.NoError(t, err)
	require.False(t, res.IsError)
	out := ResultText(res)
	assert.Contains(t, out, "#partial: ")
	assert.Contains(t, out, "#next_cursor: c2", "every row of the page is listed, so the cursor still resumes cleanly")
	assert.Contains(t, out, "saved 1.0", "the message fetched before the stop is kept")
	assert.NotContains(t, out, "saved 2.0", "no message fetch after the stop")
	for _, ts := range []string{"1.0", "2.0", "3.0"} {
		assert.Contains(t, out, "C1,"+ts+",", "row %s is listed in #saved_items", ts)
	}
}

func TestUnitSavedListStoppedBeforeAnyRowIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := newStubSavedHandler(nil, nil, nil)

	res, err := h.listSaved(ctx, "saved", 50, "c1", false, 5, text.ModeStandard)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, ResultText(res), "cancelled")
}

func TestUnitSavedListCompleteWalkIsNotPartial(t *testing.T) {
	h := newStubSavedHandler([]edge.SavedListResponse{savedPage("c2", "1.0"), savedPage("", "2.0")}, nil, nil)

	res, err := h.listSaved(context.Background(), "saved", 50, "", true, 5, text.ModeStandard)
	require.NoError(t, err)
	out := ResultText(res)
	assert.NotContains(t, out, "#partial:")
	assert.NotContains(t, out, "#next_cursor:")
	assert.Contains(t, out, "saved 1.0")
	assert.Contains(t, out, "saved 2.0")
}
