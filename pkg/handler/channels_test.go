package handler

import (
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/test/util"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testEnv struct {
	mcpClient *client.Client
	ctx       context.Context
}

type matchingRule struct {
	csvFieldName    string
	csvFieldValueRE string
}

func setupTestEnv(t *testing.T) (*testEnv, func()) {
	t.Helper()

	sseKey := uuid.New().String()
	require.NotEmpty(t, sseKey, "sseKey must be generated for integration tests")

	cfg := util.MCPConfig{
		SSEKey:             sseKey,
		MessageToolEnabled: true,
		MessageToolMark:    true,
	}

	mcpServer, err := util.SetupMCP(cfg)
	require.NoError(t, err, "Failed to set up MCP server")

	fwd, err := util.SetupForwarding(context.Background(), "http://"+mcpServer.Host+":"+strconv.Itoa(mcpServer.Port))
	require.NoError(t, err, "Failed to set up ngrok forwarding")

	sseURL := fmt.Sprintf("%s://%s/sse", fwd.URL.Scheme, fwd.URL.Host)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	mcpClient, err := client.NewSSEMCPClient(sseURL,
		client.WithHeaders(map[string]string{
			"Authorization": "Bearer " + sseKey,
		}),
	)
	require.NoError(t, err, "Failed to create MCP client")

	err = mcpClient.Start(ctx)
	require.NoError(t, err, "Failed to start MCP client")

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "channels-test-client",
		Version: "1.0.0",
	}
	initReq.Params.Capabilities = mcp.ClientCapabilities{}

	_, err = mcpClient.Initialize(ctx, initReq)
	require.NoError(t, err, "Failed to initialize MCP client")

	cleanup := func() {
		cancel()
		mcpClient.Close()
		fwd.Shutdown()
		mcpServer.Shutdown()
	}

	return &testEnv{
		mcpClient: mcpClient,
		ctx:       ctx,
	}, cleanup
}

func runChannelTest(t *testing.T, env *testEnv, channelType string, expectedChannels []matchingRule) {
	t.Helper()

	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "channels_list"
	callReq.Params.Arguments = map[string]any{
		"channel_types": channelType,
	}

	result, err := env.mcpClient.CallTool(env.ctx, callReq)
	require.NoError(t, err, "Tool call failed")
	require.NotNil(t, result, "Tool result is nil")
	require.False(t, result.IsError, "Tool returned error")

	var toolOutput strings.Builder
	for _, content := range result.Content {
		if textContent, ok := content.(mcp.TextContent); ok {
			toolOutput.WriteString(textContent.Text)
		}
	}

	require.NotEmpty(t, toolOutput.String(), "No tool output captured")

	reader := csv.NewReader(strings.NewReader(toolOutput.String()))
	rows, err := reader.ReadAll()
	require.NoError(t, err, "Failed to parse CSV")
	require.GreaterOrEqual(t, len(rows), 1, "CSV must have at least a header row")

	header := rows[0]
	dataRows := rows[1:]
	colIndex := map[string]int{}
	for i, col := range header {
		colIndex[col] = i
	}

	for _, rule := range expectedChannels {
		idx, ok := colIndex[rule.csvFieldName]
		require.Truef(t, ok, "CSV did not contain column %q, toolOutput: %q", rule.csvFieldName, toolOutput.String())

		re, err := regexp.Compile(rule.csvFieldValueRE)
		require.NoErrorf(t, err, "Invalid regex %q", rule.csvFieldValueRE)

		found := false
		for _, row := range dataRows {
			if idx < len(row) && re.MatchString(row[idx]) {
				found = true
				break
			}
		}
		assert.Truef(t, found, "No row in column %q matched %q; full CSV:\n%s",
			rule.csvFieldName, rule.csvFieldValueRE, toolOutput.String())
	}
}

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

func TestIntegrationChannelsListQueryFilter(t *testing.T) {
	env, cleanup := setupTestEnv(t)
	defer cleanup()

	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "channels_list"
	callReq.Params.Arguments = map[string]any{
		"channel_types": "public_channel",
		"query":         "testcase",
	}

	result, err := env.mcpClient.CallTool(env.ctx, callReq)
	require.NoError(t, err, "Tool call failed")
	require.NotNil(t, result, "Tool result is nil")
	require.False(t, result.IsError, "Tool returned error")

	var toolOutput strings.Builder
	for _, content := range result.Content {
		if textContent, ok := content.(mcp.TextContent); ok {
			toolOutput.WriteString(textContent.Text)
		}
	}

	reader := csv.NewReader(strings.NewReader(toolOutput.String()))
	rows, err := reader.ReadAll()
	require.NoError(t, err, "Failed to parse CSV")
	require.GreaterOrEqual(t, len(rows), 1, "CSV must have at least a header row")

	nameIdx := -1
	for i, col := range rows[0] {
		if col == "Name" {
			nameIdx = i
			break
		}
	}
	require.NotEqualf(t, -1, nameIdx, "CSV did not contain Name column; header: %v", rows[0])

	dataRows := rows[1:]
	for _, row := range dataRows {
		require.Less(t, nameIdx, len(row), "row shorter than Name column index")
		name := strings.ToLower(row[nameIdx])
		assert.Containsf(t, name, "testcase",
			"Expected all results to match query 'testcase', got: %s", row[nameIdx])
		assert.NotContainsf(t, name, "general",
			"Expected #general to be filtered out, but found: %s", row[nameIdx])
	}
	assert.GreaterOrEqual(t, len(dataRows), 3, "Expected at least testcase-1, testcase-2, testcase-3")
}

func TestIntegrationPublicChannelsList(t *testing.T) {
	env, cleanup := setupTestEnv(t)
	defer cleanup()

	expectedChannels := []matchingRule{
		{csvFieldName: "Name", csvFieldValueRE: `^#general$`},
		{csvFieldName: "Name", csvFieldValueRE: `^#testcase-1$`},
		{csvFieldName: "Name", csvFieldValueRE: `^#testcase-2$`},
		{csvFieldName: "Name", csvFieldValueRE: `^#testcase-3$`},
	}

	runChannelTest(t, env, "public_channel", expectedChannels)
}

func TestIntegrationPrivateChannelsList(t *testing.T) {
	env, cleanup := setupTestEnv(t)
	defer cleanup()

	expectedChannels := []matchingRule{
		{csvFieldName: "Name", csvFieldValueRE: `^#testcase-4$`},
	}

	runChannelTest(t, env, "private_channel", expectedChannels)
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
