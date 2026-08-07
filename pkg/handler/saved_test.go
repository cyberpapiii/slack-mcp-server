package handler

import (
	"context"
	"testing"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUnitSavedItemCSVFormat(t *testing.T) {
	items := []SavedItemRow{
		{
			ItemID:      "C0AJMCRNH0U",
			ChannelID:   "C0AJMCRNH0U",
			ChannelName: "#wg-feast-ui",
			Ts:          "1772680334.954409",
			DateCreated: "2026-03-03 12:00",
			DateDue:     "",
			State:       "in_progress",
		},
		{
			ItemID:      "D0AGSQXLJHG",
			ChannelID:   "D0AGSQXLJHG",
			ChannelName: "@john",
			Ts:          "1771941381.234049",
			DateCreated: "2026-02-25 08:30",
			DateDue:     "2026-03-01 15:00",
			State:       "in_progress",
		},
	}

	csvBytes, err := gocsv.MarshalBytes(&items)
	require.NoError(t, err)
	csvStr := string(csvBytes)

	assert.Contains(t, csvStr, "ItemID,ChannelID,ChannelName,Ts,DateCreated,DateDue,State")
	assert.Contains(t, csvStr, "C0AJMCRNH0U")
	assert.Contains(t, csvStr, "#wg-feast-ui")
	assert.Contains(t, csvStr, "1772680334.954409")
	assert.Contains(t, csvStr, "in_progress")
	assert.Contains(t, csvStr, "@john")
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

// Bug C: the schema documents `date_due: 0` as the way to clear a due date,
// so an explicit zero must be distinguishable from an absent key.
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
			args:        map[string]any{"item_id": "C1", "ts": "1.0", "date_due": 0},
			wantMark:    "",
			wantDateDue: 0,
		},
		{
			name:        "explicit date_due 0 as json float clears the due date",
			args:        map[string]any{"item_id": "C1", "ts": "1.0", "date_due": float64(0)},
			wantMark:    "",
			wantDateDue: 0,
		},
		{
			name:    "neither mark nor date_due is rejected",
			args:    map[string]any{"item_id": "C1", "ts": "1.0"},
			wantErr: "at least one of mark or date_due must be provided",
		},
		{
			name:        "mark alone is accepted",
			args:        map[string]any{"item_id": "C1", "ts": "1.0", "mark": "completed"},
			wantMark:    "completed",
			wantDateDue: 0,
		},
		{
			name:        "date_due alone is accepted",
			args:        map[string]any{"item_id": "C1", "ts": "1.0", "date_due": 1772521200},
			wantMark:    "",
			wantDateDue: 1772521200,
		},
		{
			name:    "missing item_id is rejected",
			args:    map[string]any{"ts": "1.0", "mark": "completed"},
			wantErr: "item_id and ts are required parameters",
		},
		{
			name:    "missing ts is rejected",
			args:    map[string]any{"item_id": "C1", "mark": "completed"},
			wantErr: "item_id and ts are required parameters",
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

func TestUnitSavedListResponseParsing(t *testing.T) {
	resp := edge.SavedListResponse{
		SavedItems: []edge.SavedItem{
			{
				ItemID:      "D0AGSQXLJHG",
				ItemType:    "message",
				DateCreated: 1771942696,
				DateDue:     1772521200,
				Ts:          "1771941381.234049",
				State:       "in_progress",
			},
			{
				ItemID:      "C0AJMCRNH0U",
				ItemType:    "message",
				DateCreated: 1772720838,
				DateDue:     0,
				Ts:          "1772680334.954409",
				State:       "in_progress",
			},
		},
		Counts: edge.SavedCounts{
			UncompletedCount: 51,
			TotalCount:       52,
		},
	}

	assert.Len(t, resp.SavedItems, 2)
	assert.Equal(t, 51, resp.Counts.UncompletedCount)

	first := resp.SavedItems[0]
	assert.Equal(t, "D0AGSQXLJHG", first.ItemID)
	assert.Equal(t, int64(1772521200), first.DateDue)

	second := resp.SavedItems[1]
	assert.Equal(t, int64(0), second.DateDue)
}

func TestSavedHandlersFailFastWhenBrowserUnavailable(t *testing.T) {
	h := NewSavedHandler(&provider.ApiProvider{}, zap.NewNop(), nil)
	ctx := context.Background()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"filter": "saved"}
	_, err := h.SavedListHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh browser tokens")

	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"item_id": "C1", "ts": "1.0", "mark": "completed"}
	_, err = h.SavedUpdateHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh browser tokens")

	req = mcp.CallToolRequest{}
	_, err = h.SavedClearCompletedHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh browser tokens")
}
