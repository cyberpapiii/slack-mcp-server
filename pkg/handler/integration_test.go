//go:build integration

// Live tests against a real Slack workspace through the MCP server. They need
// SLACK_MCP_XOXP_TOKEN, SLACK_MCP_OPENAI_API and an ngrok token; run them with
// `make test-integration`.

package handler

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/korotovsky/slack-mcp-server/pkg/test/util"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
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
	reader.Comment = '#'
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
	reader.Comment = '#'
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

func TestIntegrationConversations(t *testing.T) {
	sseKey := uuid.New().String()
	require.NotEmpty(t, sseKey, "sseKey must be generated for integration tests")
	apiKey := os.Getenv("SLACK_MCP_OPENAI_API")
	require.NotEmpty(t, apiKey, "SLACK_MCP_OPENAI_API must be set for integration tests")

	cfg := util.MCPConfig{
		SSEKey:             sseKey,
		MessageToolEnabled: true,
		MessageToolMark:    true,
	}

	mcp, err := util.SetupMCP(cfg)
	if err != nil {
		t.Fatalf("Failed to set up MCP server: %v", err)
	}
	fwd, err := util.SetupForwarding(context.Background(), "http://"+mcp.Host+":"+strconv.Itoa(mcp.Port))
	if err != nil {
		t.Fatalf("Failed to set up ngrok forwarding: %v", err)
	}
	defer fwd.Shutdown()
	defer mcp.Shutdown()

	client := openai.NewClient(option.WithAPIKey(apiKey))
	ctx := context.Background()

	type matchingRule struct {
		csvFieldName    string
		csvFieldValueRE string
		RowPosition     *int
		TotalRows       *int
	}

	type tc struct {
		name                            string
		input                           string
		expectedToolName                string
		expectedToolOutputMatchingRules []matchingRule
		expectedLLMOutputMatchingRules  []string
	}

	cases := []tc{
		{
			name:             "Test conversations_history tool",
			input:            "Provide a list of slack messages from #testcase-1",
			expectedToolName: "conversations_history",
			expectedToolOutputMatchingRules: []matchingRule{
				{
					csvFieldName:    "Text",
					csvFieldValueRE: "^message 3$",
				},
				{
					csvFieldName:    "Text",
					csvFieldValueRE: "^message 2$",
				},
				{
					csvFieldName:    "Text",
					csvFieldValueRE: "^message 1$",
				},
			},
			expectedLLMOutputMatchingRules: []string{
				"message 1", "message 2", "message 3",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := responses.ResponseNewParams{
				Model: "gpt-4.1-mini",
				Tools: []responses.ToolUnionParam{
					{
						OfMcp: &responses.ToolMcpParam{
							ServerLabel: "slack-mcp-server",
							ServerURL:   fmt.Sprintf("%s://%s/sse", fwd.URL.Scheme, fwd.URL.Host),
							RequireApproval: responses.ToolMcpRequireApprovalUnionParam{
								OfMcpToolApprovalSetting: param.NewOpt("never"),
							},
							Headers: map[string]string{
								"Authorization": "Bearer " + sseKey,
							},
						},
					},
				},
				Input: responses.ResponseNewParamsInputUnion{
					OfString: openai.String(tc.input),
				},
			}

			resp, err := client.Responses.New(ctx, params)
			require.NoError(t, err, "API call failed")

			assert.NotNil(t, resp.Status, "completed")

			var llmOutput strings.Builder
			var toolOutput strings.Builder
			for _, out := range resp.Output {
				if out.Type == "message" && out.Role == "assistant" {
					for _, c := range out.Content {
						if c.Type == "output_text" {
							llmOutput.WriteString(c.Text)
						}
					}
				}
				if out.Type == "mcp_call" && out.Name == tc.expectedToolName {
					toolOutput.WriteString(out.Output)
				}
			}

			require.NotEmpty(t, toolOutput, "no tool output captured")

			reader := csv.NewReader(strings.NewReader(toolOutput.String()))
			rows, err := reader.ReadAll()
			require.NoError(t, err, "failed to parse CSV")

			header := rows[0]
			dataRows := rows[1:]
			colIndex := map[string]int{}
			for i, col := range header {
				colIndex[col] = i
			}

			for _, rule := range tc.expectedToolOutputMatchingRules {
				if rule.TotalRows != nil && *rule.TotalRows > 0 {
					assert.Equalf(t, *rule.TotalRows, len(dataRows),
						"expected %d data rows, got %d", rule.TotalRows, len(dataRows))
				}

				idx, ok := colIndex[rule.csvFieldName]
				require.Truef(t, ok, "CSV did not contain column %q, toolOutput: %q", rule.csvFieldName, toolOutput.String())

				re, err := regexp.Compile(rule.csvFieldValueRE)
				require.NoErrorf(t, err, "invalid regex %q", rule.csvFieldValueRE)

				if rule.RowPosition != nil && *rule.RowPosition >= 0 {
					require.Lessf(t, rule.RowPosition, len(dataRows), "RowPosition %d out of range (only %d data rows)", rule.RowPosition, len(dataRows))
					value := dataRows[*rule.RowPosition][idx]
					assert.Regexpf(t, re, value, "row %d, column %q: expected to match %q, got %q",
						rule.RowPosition, rule.csvFieldName, rule.csvFieldValueRE, value)
					continue
				}

				found := false
				for _, row := range dataRows {
					if idx < len(row) && re.MatchString(row[idx]) {
						found = true
						break
					}
				}
				assert.Truef(t, found, "no row in column %q matched %q; full CSV:\n%s",
					rule.csvFieldName, rule.csvFieldValueRE, toolOutput.String())
			}

			for _, pattern := range tc.expectedLLMOutputMatchingRules {
				re, err := regexp.Compile(pattern)
				require.NoErrorf(t, err, "invalid LLM regex %q", pattern)
				assert.Regexpf(t, re, llmOutput.String(), "LLM output did not match regex %q; output:\n%s",
					pattern, llmOutput.String())
			}
		})
	}
}
