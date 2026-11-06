package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

const savedItemsHeader = "Channel,MsgID,DateCreated,DateDue,State,Type,TodoState,ItemID,Text"

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
		assert.Equal(t, "C0AJMCRNH0U,1772680334.954409,2026-03-03 12:00,,in_progress,,,,", lines[3])
		assert.Equal(t, "D0AGSQXLJHG,1771941381.234049,2026-02-25 08:30,2026-03-01 15:00,in_progress,,,,", lines[4])
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
	res, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard, meta: SlackResultMeta("next-page", false, ""), sections: []csvSection{{name: "saved_items", rows: &items}}})
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

func TestUnitUnixTimeText(t *testing.T) {
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
			expected: "2026-03-05T14:27:18Z",
		},
		{
			name:     "negative means unset",
			input:    -1,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := unixTimeText(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Schema documents date_due: 0 to clear a due date; explicit zero must differ from absent key.
func TestUnitParseSavedUpdateParams(t *testing.T) {
	message := map[string]any{"channel_id": "C1", "timestamp": "1.0"}
	with := func(base map[string]any, extra map[string]any) map[string]any {
		merged := map[string]any{}
		for k, v := range base {
			merged[k] = v
		}
		for k, v := range extra {
			merged[k] = v
		}
		return merged
	}
	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
		want    provider.SavedUpdate
	}{
		{
			name: "explicit date_due 0 clears the due date",
			args: with(message, map[string]any{"date_due": 0}),
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "message", ID: "C1", Ts: "1.0"}, DateDue: "0"},
		},
		{
			name: "explicit date_due 0 as json float clears the due date",
			args: with(message, map[string]any{"date_due": float64(0)}),
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "message", ID: "C1", Ts: "1.0"}, DateDue: "0"},
		},
		{
			name: "mark alone is accepted",
			args: with(message, map[string]any{"mark": "completed"}),
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "message", ID: "C1", Ts: "1.0"}, Mark: "completed"},
		},
		{
			name: "date_due alone is accepted",
			args: with(message, map[string]any{"date_due": 1772521200}),
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "message", ID: "C1", Ts: "1.0"}, DateDue: "1772521200"},
		},
		{
			name: "todo_state alone moves the item",
			args: with(message, map[string]any{"todo_state": "saved"}),
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "message", ID: "C1", Ts: "1.0"}, TodoState: "saved"},
		},
		{
			name: "reopening goes back to To-dos",
			args: with(message, map[string]any{"mark": "uncompleted"}),
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "message", ID: "C1", Ts: "1.0"}, Mark: "uncompleted", TodoState: "to_do"},
		},
		{
			name: "reopening keeps an explicit list",
			args: with(message, map[string]any{"mark": "uncompleted", "todo_state": "saved"}),
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "message", ID: "C1", Ts: "1.0"}, Mark: "uncompleted", TodoState: "saved"},
		},
		{
			name: "a custom to-do is addressed by item_id",
			args: map[string]any{"item_id": "Sa0CD69VBVKR", "mark": "completed"},
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "reminder", ID: "Sa0CD69VBVKR"}, Mark: "completed"},
		},
		{
			name: "a file is addressed by item_id",
			args: map[string]any{"item_id": "F123", "todo_state": "to_do"},
			want: provider.SavedUpdate{SavedRef: provider.SavedRef{Type: "file", ID: "F123"}, TodoState: "to_do"},
		},
		{name: "no change is rejected", args: message, wantErr: "at least one of mark, todo_state or date_due"},
		{name: "unknown mark is rejected", args: with(message, map[string]any{"mark": "done"}), wantErr: "mark must be"},
		{name: "unknown todo_state is rejected", args: with(message, map[string]any{"todo_state": "later"}), wantErr: "todo_state must be"},
		{name: "missing channel_id is rejected", args: map[string]any{"timestamp": "1.0", "mark": "completed"}, wantErr: "channel_id and timestamp are required"},
		{name: "missing ts is rejected", args: map[string]any{"channel_id": "C1", "mark": "completed"}, wantErr: "channel_id and timestamp are required"},
		{name: "both target forms are rejected", args: with(message, map[string]any{"item_id": "Sa1", "mark": "completed"}), wantErr: "not both"},
		{name: "unknown item_id is rejected", args: map[string]any{"item_id": "C1", "mark": "completed"}, wantErr: "not a custom to-do"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tt.args

			got, err := parseSavedUpdateParams(req)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUnitParseSavedAddParams(t *testing.T) {
	parse := func(args map[string]any) (provider.SavedAdd, error) {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = args
		return parseSavedAddParams(req)
	}

	custom, err := parse(map[string]any{"text": "Call Sam", "date_due": 1772521200})
	require.NoError(t, err)
	assert.Equal(t, "reminder", custom.Type)
	assert.Equal(t, "to_do", custom.TodoState, "a new item goes to To-dos by default")
	assert.Equal(t, "1772521200", custom.DateDue)
	assert.Equal(t, "Call Sam", custom.Text)

	message, err := parse(map[string]any{"channel_id": "C1", "timestamp": "1.0", "todo_state": "saved"})
	require.NoError(t, err)
	assert.Equal(t, provider.SavedAdd{SavedRef: provider.SavedRef{Type: "message", ID: "C1", Ts: "1.0"}, TodoState: "saved"}, message)

	file, err := parse(map[string]any{"file_id": "F1"})
	require.NoError(t, err)
	assert.Equal(t, provider.SavedAdd{SavedRef: provider.SavedRef{Type: "file", ID: "F1"}, TodoState: "to_do"}, file)

	for _, args := range []map[string]any{
		{},
		{"text": "   "},
		{"channel_id": "C1"},
		{"timestamp": "1.0"},
		{"text": "x", "file_id": "F1"},
		{"text": "x", "channel_id": "C1", "timestamp": "1.0"},
		{"channel_id": "C1", "timestamp": "1.0", "file_id": "F1"},
	} {
		_, err := parse(args)
		assert.ErrorContains(t, err, "exactly one of", "%v", args)
	}
	_, err = parse(map[string]any{"text": "x", "todo_state": "later"})
	assert.ErrorContains(t, err, "todo_state must be")
}

func TestUnitListSavedShowsCustomToDosWithoutFetchingMessages(t *testing.T) {
	var fetched []string
	var body slack.Blocks
	require.NoError(t, json.Unmarshal([]byte(`[{"type":"rich_text","elements":[{"type":"rich_text_section","elements":[{"type":"text","text":"Call Sam"}]}]}]`), &body))
	page := provider.SavedPage{Items: []provider.SavedItem{
		{SavedRef: provider.SavedRef{ID: "Sa1", Type: "reminder"}, State: "in_progress", TodoState: "to_do", Description: body},
		{SavedRef: provider.SavedRef{ID: "F1", Type: "file"}, State: "in_progress", TodoState: "to_do"},
	}}
	h := newStubSavedHandler([]provider.SavedPage{page}, nil, func(item provider.SavedItem) { fetched = append(fetched, item.ID) })

	res, err := served(h.listSaved(context.Background(), "todo", 50, "", true, text.ModeStandard))
	require.NoError(t, err)
	assert.Empty(t, fetched, "only a message has history to fetch")

	csvBody := csvResultBody(t, res)
	assert.Contains(t, csvBody, ",,,,in_progress,reminder,to_do,Sa1,Call Sam")
	assert.Contains(t, csvBody, ",,,,in_progress,file,to_do,F1,")
}

func TestSavedHandlersFailFastWhenBrowserUnavailable(t *testing.T) {
	h := NewSavedHandler(&provider.ApiProvider{}, zap.NewNop(), Settings{})
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
func newStubSavedHandler(pages []provider.SavedPage, onPage func(n int), onItem func(provider.SavedItem)) *SavedHandler {
	calls := 0
	return &SavedHandler{
		apiProvider: &provider.ApiProvider{},
		logger:      zap.NewNop(),
		messages:    messagePipeline{api: &provider.ApiProvider{}, logger: zap.NewNop()},
		pace:        rate.NewLimiter(rate.Inf, 0),
		listPage: func(context.Context, string, int, string) (provider.SavedPage, error) {
			page := pages[calls]
			calls++
			if onPage != nil {
				onPage(calls)
			}
			return page, nil
		},
		itemMessages: func(_ context.Context, item provider.SavedItem, _ text.OutputMode) ([]Message, error) {
			if onItem != nil {
				onItem(item)
			}
			return []Message{{MsgID: item.Ts, Channel: item.Channel(), Text: "saved " + item.Ts}}, nil
		},
	}
}

func savedPage(nextCursor string, ts ...string) provider.SavedPage {
	var page provider.SavedPage
	for _, t := range ts {
		page.Items = append(page.Items, provider.SavedItem{SavedRef: provider.SavedRef{ID: "C1", Ts: t}, State: "in_progress"})
	}
	page.NextCursor = nextCursor
	return page
}

func TestUnitSavedListKeepsRowsWhenStoppedBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newStubSavedHandler([]provider.SavedPage{savedPage("c2", "1.0", "2.0")}, func(int) { cancel() }, nil)

	res, err := served(h.listSaved(ctx, "saved", 50, "", false, text.ModeStandard))
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
	h := newStubSavedHandler([]provider.SavedPage{savedPage("c2", "1.0", "2.0", "3.0")}, nil, func(provider.SavedItem) { cancel() })

	res, err := served(h.listSaved(ctx, "saved", 50, "", true, text.ModeStandard))
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

	res, err := served(h.listSaved(ctx, "saved", 50, "c1", false, text.ModeStandard))
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, ResultText(res), "cancelled")
}

func TestUnitSavedListCompleteWalkIsNotPartial(t *testing.T) {
	h := newStubSavedHandler([]provider.SavedPage{savedPage("c2", "1.0"), savedPage("", "2.0")}, nil, nil)

	res, err := served(h.listSaved(context.Background(), "saved", 50, "", true, text.ModeStandard))
	require.NoError(t, err)
	out := ResultText(res)
	assert.NotContains(t, out, "#partial:")
	assert.NotContains(t, out, "#next_cursor:")
	assert.Contains(t, out, "saved 1.0")
	assert.Contains(t, out, "saved 2.0")
}
