package handler

import (
	"strconv"
	"strings"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitClassifyChannelType(t *testing.T) {
	tests := []struct {
		name     string
		channel  provider.Channel
		expected string
	}{
		{
			name:     "regular public channel",
			channel:  provider.Channel{ID: "C123", Name: "#general"},
			expected: "internal",
		},
		{
			name:     "private channel",
			channel:  provider.Channel{ID: "C456", Name: "#secret", IsPrivate: true},
			expected: "internal",
		},
		{
			name:     "DM channel",
			channel:  provider.Channel{ID: "D123", Name: "@alice", IsIM: true},
			expected: "dm",
		},
		{
			name:     "group DM",
			channel:  provider.Channel{ID: "G123", Name: "mpdm-alice-bob", IsMpIM: true},
			expected: "group_dm",
		},
		{
			name:     "external shared channel",
			channel:  provider.Channel{ID: "C789", Name: "#ext-partner", IsExtShared: true},
			expected: "partner",
		},
		{
			name:     "ext shared takes precedence over private",
			channel:  provider.Channel{ID: "C999", Name: "#ext-priv", IsPrivate: true, IsExtShared: true},
			expected: "partner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyChannelType(tt.channel)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestUnitPaginateChannelsInvalidCursor(t *testing.T) {
	channels := []provider.Channel{
		{ID: "C01", Name: "#one"},
		{ID: "C02", Name: "#two"},
	}

	paged, nextCursor, err := paginateChannels(channels, "!!!not-base64!!!", 2)

	require.Error(t, err, "an undecodable cursor must be rejected, not silently restarted")
	assert.Contains(t, err.Error(), "invalid cursor")
	assert.Empty(t, paged, "no rows should be returned for a bad cursor")
	assert.Empty(t, nextCursor, "no next cursor should be handed back for a bad cursor")
}

func TestUnitPaginateChannelsRoundTrip(t *testing.T) {
	channels := []provider.Channel{
		{ID: "C01", Name: "#one"},
		{ID: "C02", Name: "#two"},
		{ID: "C03", Name: "#three"},
		{ID: "C04", Name: "#four"},
		{ID: "C05", Name: "#five"},
	}

	page1, cursor1, err := paginateChannels(channels, "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, []string{"C01", "C02"}, channelIDs(page1))
	require.NotEmpty(t, cursor1, "more rows remain, so a cursor is expected")

	page2, cursor2, err := paginateChannels(channels, cursor1, 2)
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.Equal(t, []string{"C03", "C04"}, channelIDs(page2),
		"page 2 must resume exactly after page 1 with no rows skipped or repeated")
	require.NotEmpty(t, cursor2)

	page3, cursor3, err := paginateChannels(channels, cursor2, 2)
	require.NoError(t, err)
	require.Len(t, page3, 1)
	assert.Equal(t, []string{"C05"}, channelIDs(page3))
	assert.Empty(t, cursor3, "the final page must not advertise another page")
}

// Non-positive limit must not panic the page slice (GetInt only defaults absent keys).
func TestUnitPaginateChannelsNonPositiveLimit(t *testing.T) {
	channels := []provider.Channel{
		{ID: "C01", Name: "#one"},
		{ID: "C02", Name: "#two"},
		{ID: "C03", Name: "#three"},
	}

	for _, limit := range []int{-5, -1, 0} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			paged, nextCursor, err := paginateChannels(channels, "", limit)

			require.NoError(t, err)
			assert.Empty(t, paged, "a non-positive limit yields an empty page, not a panic")
			assert.Empty(t, nextCursor, "an empty page must not advertise a cursor to resume from")
		})
	}
}

func channelIDs(channels []provider.Channel) []string {
	ids := make([]string, len(channels))
	for i, c := range channels {
		ids[i] = c.ID
	}
	return ids
}

func TestUnitNextPageSize(t *testing.T) {
	tests := []struct {
		name     string
		limit    int
		have     int
		expected int
	}{
		{name: "default limit fits in one page", limit: 100, have: 0, expected: 100},
		{name: "oversized limit caps at slack max", limit: 250, have: 0, expected: 200},
		{name: "second request asks only for the remainder", limit: 250, have: 200, expected: 50},
		{name: "max limit caps at slack max", limit: 999, have: 0, expected: 200},
		{name: "exactly slack max", limit: 200, have: 0, expected: 200},
		{name: "one row still missing", limit: 5, have: 4, expected: 1},
		{name: "nothing missing floors at one", limit: 100, have: 100, expected: 1},
		{name: "over-fetched floors at one", limit: 10, have: 25, expected: 1},
		{name: "nonpositive limit floors at one", limit: 0, have: 0, expected: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, nextPageSize(tt.limit, tt.have))
		})
	}
}

func TestUnitSortChannelsPopularityPaginatesInSortedOrder(t *testing.T) {
	channels := []provider.Channel{
		{ID: "C01", MemberCount: 5},
		{ID: "C02", MemberCount: 50},
		{ID: "C03", MemberCount: 50},
		{ID: "C04", MemberCount: 9},
	}
	sortChannels(channels, "popularity")
	assert.Equal(t, []string{"C02", "C03", "C04", "C01"}, channelIDs(channels), "members desc, ties by ID")

	page1, cursor, err := paginateChannels(channels, "", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"C02", "C03"}, channelIDs(page1))
	page2, cursor, err := paginateChannels(channels, cursor, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"C04", "C01"}, channelIDs(page2), "page 2 continues the popularity order")
	assert.Empty(t, cursor)
}

func TestUnitChannelsCSVCursorLineAndHeader(t *testing.T) {
	rows := []Channel{
		{ID: "C01", Name: "#general", Topic: "t", Purpose: "p", MemberCount: 12},
		{ID: "C02", Name: "#random", MemberCount: 3},
	}

	result, err := marshalRowsToCSV("", &rows, "page-2")
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)
	lines := strings.Split(ResultText(result), "\n")
	assert.Equal(t, "#next_cursor: page-2", lines[0])
	assert.Equal(t, "ID,Name,Topic,Purpose,MemberCount", lines[1])
	assert.Equal(t, "C01,#general,t,p,12", lines[2])

	last, err := marshalRowsToCSV("", &rows, "")
	require.NoError(t, err)
	assert.Nil(t, last.StructuredContent)
	body := ResultText(last)
	assert.True(t, strings.HasPrefix(body, "ID,Name,Topic,Purpose,MemberCount\n"), body)
	assert.NotContains(t, body, "#next_cursor")
	assert.NotContains(t, body, "Cursor")
}

func TestUnitStarredChannelsCSVHeader(t *testing.T) {
	rows := []StarredChannel{{ID: "D01", Name: "@bob", ChannelType: "dm", IsMuted: true, MemberCount: 2}}

	result, err := marshalRowsToCSV("", &rows, "")
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)
	assert.Equal(t, "ID,Name,ChannelType,IsMuted,MemberCount\nD01,@bob,dm,true,2\n", ResultText(result))
}
