package server

import (
	"context"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestEveryToolConstantIsInTable builds the whole server in demo mode with
// every tool enabled and checks the registered set equals the capability
// table, so a Tool* constant cannot exist without a table row or vice versa.
func TestEveryToolConstantIsInTable(t *testing.T) {
	for _, k := range []string{"SLACK_MCP_XOXP_TOKEN", "SLACK_MCP_XOXB_TOKEN", "SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT"} {
		t.Setenv(k, "")
	}
	t.Setenv("SLACK_MCP_XOXC_TOKEN", "demo")
	t.Setenv("SLACK_MCP_XOXD_TOKEN", "demo")
	require.True(t, provider.IsDemoCredentials())

	api, err := provider.New("stdio", zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(api.Close)
	srv := NewMCPServer(api, zap.NewNop(), ValidToolNames)
	api.SkipCache()
	srv.RegisterCacheDependentTools()

	var registered []string
	for name := range srv.server.ListTools() {
		registered = append(registered, name)
	}
	assert.ElementsMatch(t, capability.Names(), registered)
}

func TestNewToolAppliesTableHints(t *testing.T) {
	for _, spec := range capability.Tools {
		t.Run(spec.Name, func(t *testing.T) {
			tool := newTool(spec.Name, mcp.WithDescription("x"))
			require.NotNil(t, tool.Annotations.ReadOnlyHint)
			require.NotNil(t, tool.Annotations.DestructiveHint)
			require.NotNil(t, tool.Annotations.IdempotentHint)
			require.NotNil(t, tool.Annotations.OpenWorldHint)
			assert.Equal(t, spec.Title, tool.Annotations.Title)
			assert.Equal(t, spec.ReadOnly, *tool.Annotations.ReadOnlyHint)
			assert.True(t, *tool.Annotations.OpenWorldHint)
			if spec.ReadOnly {
				assert.False(t, *tool.Annotations.DestructiveHint)
				assert.True(t, *tool.Annotations.IdempotentHint)
			} else {
				assert.Equal(t, spec.Destructive, *tool.Annotations.DestructiveHint)
				assert.Equal(t, spec.Idempotent, *tool.Annotations.IdempotentHint)
			}
			if _, declared := outputSchemaByTool[spec.Name]; declared {
				assert.Equal(t, "object", tool.OutputSchema.Type)
				assert.Contains(t, tool.OutputSchema.Properties, "schema_version")
				assert.Contains(t, tool.OutputSchema.Properties, "meta")
				assert.Contains(t, tool.OutputSchema.Properties, "data")
				assert.Contains(t, tool.OutputSchema.Properties, "error")
			} else {
				assert.Empty(t, tool.OutputSchema.Type, "only approval-flow and diagnostics tools advertise an output schema")
			}
		})
	}
}

func TestNewToolPanicsOnUnknownName(t *testing.T) {
	assert.PanicsWithValue(t, `tool "no_such_tool" is not in the capability table`, func() {
		newTool("no_such_tool", mcp.WithDescription("x"))
	})
}

func TestPhaseGuards(t *testing.T) {
	noop := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	base := mcpserver.NewMCPServer("test", "1", mcpserver.WithToolCapabilities(true))
	ms := &MCPServer{server: base}

	assert.Panics(t, func() {
		addEnabledTool(base, ValidToolNames, newTool(ToolChannelsList, mcp.WithDescription("x")), noop)
	}, "cache-dependent tool must not register immediately")
	assert.Panics(t, func() {
		addCacheDependentTool(ms, ValidToolNames, newTool(ToolUsersSearch, mcp.WithDescription("x")), noop)
	}, "immediate tool must not register in the cache-dependent phase")

	assert.NotPanics(t, func() {
		addEnabledTool(base, ValidToolNames, newTool(ToolUsersSearch, mcp.WithDescription("x")), noop)
		addCacheDependentTool(ms, ValidToolNames, newTool(ToolChannelsList, mcp.WithDescription("x")), noop)
	})
	assert.Len(t, base.ListTools(), 2)
}

func TestListsWriteToolsDeclareObjectArrayContracts(t *testing.T) {
	server := mcpserver.NewMCPServer("test", "1", mcpserver.WithToolCapabilities(false))
	enabled := []string{ToolListsCreate, ToolListsUpdate, ToolListsItemsCreate, ToolListsItemsUpdate}
	registerListsTools(server, &handler.ListsHandler{}, enabled)
	tools := server.ListTools()

	assertObjectItems := func(toolName, property string) map[string]any {
		tool, ok := tools[toolName]
		require.True(t, ok)
		array, ok := tool.Tool.InputSchema.Properties[property].(map[string]any)
		require.True(t, ok, "%s.%s must be an array schema", toolName, property)
		items, ok := array["items"].(map[string]any)
		require.True(t, ok, "%s.%s must declare item schema", toolName, property)
		assert.Equal(t, "object", items["type"])
		return items
	}

	column := assertObjectItems(ToolListsCreate, "schema")
	assert.NotEmpty(t, column["properties"])
	assert.ElementsMatch(t, []string{"key", "name", "type"}, column["required"])
	assertObjectItems(ToolListsCreate, "description_blocks")
	assertObjectItems(ToolListsUpdate, "description_blocks")

	initial := assertObjectItems(ToolListsItemsCreate, "initial_fields")
	assert.NotEmpty(t, initial["properties"])
	assert.ElementsMatch(t, []string{"column_id"}, initial["required"])
	assert.Len(t, initial["oneOf"], 10)

	cells := assertObjectItems(ToolListsItemsUpdate, "cells")
	assert.NotEmpty(t, cells["properties"])
	assert.ElementsMatch(t, []string{"column_id", "row_id"}, cells["required"])
	assert.Len(t, cells["oneOf"], 10)
	properties := cells["properties"].(map[string]any)
	link := properties["link"].(map[string]any)["items"].(map[string]any)
	assert.ElementsMatch(t, []string{"original_url"}, link["required"])
}

func TestOutputSchemaToolsExposeApprovalToken(t *testing.T) {
	for name := range outputSchemaByTool {
		if name == ToolSlackAuthStatus {
			continue
		}
		tool := newTool(name, mcp.WithDescription("contract test"))
		data, ok := tool.OutputSchema.Properties["data"].(map[string]any)
		require.True(t, ok, "%s data schema", name)
		props, ok := data["properties"].(map[string]any)
		require.True(t, ok, "%s data properties", name)
		assert.Contains(t, props, "approval_token", "%s prepare must return data.approval_token", name)
	}
}
