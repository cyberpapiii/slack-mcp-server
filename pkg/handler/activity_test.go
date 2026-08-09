package handler

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/gocarina/gocsv"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUnitActivityItemCSVFormat(t *testing.T) {
	t.Run("ActivityItem marshals to CSV with correct headers", func(t *testing.T) {
		items := []ActivityItem{
			{
				Type:        "thread_v2",
				ChannelID:   "C092WJP9Z38",
				ChannelName: "#wg-maas-internal",
				ThreadTs:    "1772226249.415959",
				UnreadCount: 193,
				FeedTs:      "1772832921.456189",
				Key:         "thread_v2-C092WJP9Z38-1772226249.415959",
				MinUnreadTs: "1772226261.119069",
			},
			{
				Type:        "at_user",
				ChannelID:   "C099MEPGF43",
				ChannelName: "#wg-dashboard-crimson",
				ThreadTs:    "1772826588.319629",
				UnreadCount: 1,
				FeedTs:      "1772826812.305899",
				Key:         "at_user-C099MEPGF43-1772826812.305899-1772826588.319629",
				MinUnreadTs: "",
			},
		}

		csvBytes, err := gocsv.MarshalBytes(&items)
		require.NoError(t, err)

		reader := csv.NewReader(strings.NewReader(string(csvBytes)))
		records, err := reader.ReadAll()
		require.NoError(t, err)
		require.Equal(t, 3, len(records), "should have header + 2 data rows")

		header := records[0]
		assert.Equal(t, []string{"Type", "ChannelID", "ChannelName", "ThreadTs", "UnreadCount", "FeedTs", "Key", "MinUnreadTs"}, header)

		row1 := records[1]
		assert.Equal(t, "thread_v2", row1[0])
		assert.Equal(t, "C092WJP9Z38", row1[1])
		assert.Equal(t, "#wg-maas-internal", row1[2])
		assert.Equal(t, "193", row1[4])

		row2 := records[2]
		assert.Equal(t, "at_user", row2[0])
		assert.Equal(t, "C099MEPGF43", row2[1])
		assert.Equal(t, "1", row2[4])
	})

	t.Run("empty items list produces empty CSV", func(t *testing.T) {
		items := []ActivityItem{}
		csvBytes, err := gocsv.MarshalBytes(&items)
		require.NoError(t, err)
		assert.Contains(t, string(csvBytes), "Type,ChannelID")
	})
}

func TestUnitActivityChannelLabel(t *testing.T) {
	channels := map[string]provider.Channel{
		"C092WJP9Z38": {ID: "C092WJP9Z38", Name: "#wg-maas-internal"},
		"D0118S60VFD": {ID: "D0118S60VFD", Name: "@ada"},
		"C0BLANKNAME": {ID: "C0BLANKNAME", Name: ""},
	}

	t.Run("resolvable channel renders as ID (#name)", func(t *testing.T) {
		assert.Equal(t, "C092WJP9Z38 (#wg-maas-internal)", activityChannelLabel("C092WJP9Z38", channels))
	})

	t.Run("DM keeps the cached @ prefix", func(t *testing.T) {
		assert.Equal(t, "D0118S60VFD (@ada)", activityChannelLabel("D0118S60VFD", channels))
	})

	t.Run("unknown channel falls back to the bare ID", func(t *testing.T) {
		assert.Equal(t, "C099MEPGF43", activityChannelLabel("C099MEPGF43", channels))
	})

	t.Run("cached entry with an empty name falls back to the bare ID", func(t *testing.T) {
		assert.Equal(t, "C0BLANKNAME", activityChannelLabel("C0BLANKNAME", channels))
	})

	t.Run("nil cache falls back to the bare ID", func(t *testing.T) {
		assert.Equal(t, "C092WJP9Z38", activityChannelLabel("C092WJP9Z38", nil))
	})
}

func TestActivityHandlersFailFastWhenBrowserUnavailable(t *testing.T) {
	t.Setenv("SLACK_MCP_ACTIVITY_MARK_TOOL", "true")
	h := NewActivityHandler(&provider.ApiProvider{}, zap.NewNop(), nil)
	ctx := context.Background()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"limit": 10}
	_, err := h.ActivityUnreadsHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh browser tokens")

	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"key":     "thread_v2-C1-1.0",
		"feed_ts": "1.0",
		"type":    "thread_v2",
	}
	_, err = h.ActivityMarkReadHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh browser tokens")
}
