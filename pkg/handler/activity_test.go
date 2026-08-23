package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const activityItemsHeader = "Type,Channel,MsgID,ThreadTs,UnreadCount,FeedTs,Key"

func activityFixtureItems() []ActivityItem {
	return []ActivityItem{
		{
			Type:        "thread_v2",
			ChannelID:   "C092WJP9Z38",
			MinUnreadTs: "1772226261.119069",
			ThreadTs:    "1772226249.415959",
			UnreadCount: 193,
			FeedTs:      "1772832921.456189",
			Key:         "thread_v2-C092WJP9Z38-1772226249.415959",
		},
		{
			Type:        "at_user",
			ChannelID:   "C099MEPGF43",
			MinUnreadTs: "1772826812.305899",
			ThreadTs:    "1772826588.319629",
			UnreadCount: 1,
			FeedTs:      "1772826812.305899",
			Key:         "at_user-C099MEPGF43-1772826812.305899-1772826588.319629",
		},
	}
}

func activityFixtureChannelName(id string) string {
	return map[string]string{
		"C092WJP9Z38": "#wg-maas-internal",
		"C0BLANKNAME": "",
	}[id]
}

func TestUnitRenderActivityItems(t *testing.T) {
	t.Run("rows carry the bare channel ID and the legend names what the cache knows", func(t *testing.T) {
		res, err := renderActivityItems(activityFixtureItems(), activityFixtureChannelName, SlackResultMeta("", false, ""))
		require.NoError(t, err)
		assert.Nil(t, res.StructuredContent)

		lines := strings.Split(csvResultBody(t, res), "\n")
		assert.Equal(t, "#channels: C092WJP9Z38=#wg-maas-internal", lines[0])
		assert.Equal(t, activityItemsHeader, lines[1])
		assert.Equal(t, "thread_v2,C092WJP9Z38,1772226261.119069,1772226249.415959,193,1772832921.456189,thread_v2-C092WJP9Z38-1772226249.415959", lines[2])
		assert.Equal(t, "at_user,C099MEPGF43,1772826812.305899,1772826588.319629,1,1772826812.305899,at_user-C099MEPGF43-1772826812.305899-1772826588.319629", lines[3])
	})

	t.Run("partial meta lands between legend and header", func(t *testing.T) {
		res, err := renderActivityItems(activityFixtureItems(), activityFixtureChannelName, SlackResultMeta("", true, "2 activity threads could not be fetched"))
		require.NoError(t, err)
		lines := strings.Split(csvResultBody(t, res), "\n")
		assert.Equal(t, "#channels: C092WJP9Z38=#wg-maas-internal", lines[0])
		assert.Equal(t, "#partial: 2 activity threads could not be fetched", lines[1])
		assert.Equal(t, activityItemsHeader, lines[2])
	})

	t.Run("no items is a header-only CSV with no legend", func(t *testing.T) {
		res, err := renderActivityItems(nil, activityFixtureChannelName, SlackResultMeta("", false, ""))
		require.NoError(t, err)
		assert.Equal(t, activityItemsHeader+"\n", csvResultBody(t, res))
	})
}

func TestUnitChannelsLegend(t *testing.T) {
	name := func(id string) string {
		return map[string]string{"C1": "#general", "D2": "@ada", "C3": ""}[id]
	}
	assert.Equal(t, "#channels: C1=#general, D2=@ada\n", channelsLegend([]string{"C1", "D2", "C1", "C3", "", "C9"}, name))
	assert.Equal(t, "", channelsLegend([]string{"C3", "C9"}, name), "nothing resolvable, no legend line")
	assert.Equal(t, "", channelsLegend(nil, name))
}

func TestUnitActivityMessagesTrailer(t *testing.T) {
	messages := compactCSVFixtureMessagesN(1)
	rendered, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard})
	require.NoError(t, err)
	items := activityFixtureItems()
	trailer, err := csvSection("activity_items", &items)
	require.NoError(t, err)
	res := NewCSVResult("", SlackResultMeta("", true, "1 activity threads could not be fetched"), ResultText(rendered)+trailer)
	assert.Nil(t, res.StructuredContent)

	lines := strings.Split(csvResultBody(t, res), "\n")
	assert.Equal(t, "#partial: 1 activity threads could not be fetched", lines[0])
	assert.True(t, strings.HasPrefix(lines[1], "User,Channel,"), lines[1])
	assert.Equal(t, "#activity_items:", lines[3])
	assert.Equal(t, activityItemsHeader, lines[4])
	assert.Len(t, lines, 8, "trailer rows follow the section header; body ends with a newline")
}

func TestActivityHandlersFailFastWhenBrowserUnavailable(t *testing.T) {
	h := NewActivityHandler(&provider.ApiProvider{}, zap.NewNop(), nil)
	ctx := context.Background()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"limit": 10}
	_, err := h.ActivityUnreadsHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "browser session (xoxc/xoxd)")

	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"key":     "thread_v2-C1-1.0",
		"feed_ts": "1.0",
		"type":    "thread_v2",
	}
	_, err = h.ActivityMarkReadHandler(ctx, req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "browser session (xoxc/xoxd)")
}
