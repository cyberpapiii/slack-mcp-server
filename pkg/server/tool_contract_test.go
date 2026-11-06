package server

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newDemoServer builds the whole server in demo mode, where every credential
// requirement is met, with the given tools enabled.
func newDemoServer(t *testing.T, enabled []string) *MCPServer {
	t.Helper()
	for _, k := range []string{"SLACK_MCP_XOXP_TOKEN", "SLACK_MCP_XOXB_TOKEN", "SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT", "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT"} {
		t.Setenv(k, "")
	}
	t.Setenv("SLACK_MCP_XOXC_TOKEN", "demo")
	t.Setenv("SLACK_MCP_XOXD_TOKEN", "demo")
	require.True(t, provider.IsDemoCredentials())

	api, err := provider.New(zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(api.Close)
	srv := NewMCPServer(api, "stdio", zap.NewNop(), enabled, handler.Settings{})
	api.SkipCache()
	return srv
}

func registeredTools(srv *MCPServer) map[string]mcp.Tool {
	tools := map[string]mcp.Tool{}
	for name, tool := range srv.server.ListTools() {
		tools[name] = tool.Tool
	}
	return tools
}

func TestEveryCatalogToolIsServed(t *testing.T) {
	srv := newDemoServer(t, capability.Names())
	srv.RegisterCacheDependentTools()

	var registered []string
	for name := range registeredTools(srv) {
		registered = append(registered, name)
	}
	assert.ElementsMatch(t, capability.Names(), registered)
}

func TestOnlyEnabledToolsRegister(t *testing.T) {
	enabled := []string{capability.ConversationsHistory, capability.ReactionsAdd, capability.ChannelsList}
	srv := newDemoServer(t, enabled)
	srv.RegisterCacheDependentTools()

	var registered []string
	for name := range registeredTools(srv) {
		registered = append(registered, name)
	}
	assert.ElementsMatch(t, enabled, registered)

	none := newDemoServer(t, nil)
	none.RegisterCacheDependentTools()
	assert.Empty(t, none.server.ListTools())
}

func TestCacheReadyToolsWaitForWarmup(t *testing.T) {
	srv := newDemoServer(t, capability.Names())
	before := registeredTools(srv)
	for _, spec := range capability.Tools {
		_, present := before[spec.Name]
		assert.Equal(t, !spec.CacheReady, present, "%s registered before warm-up", spec.Name)
	}
	assert.Empty(t, srv.server.ListResources())

	srv.RegisterCacheDependentTools()
	srv.RegisterCacheDependentTools()
	assert.Len(t, registeredTools(srv), len(capability.Tools))
	assert.Len(t, srv.server.ListResources(), 2)
}

func TestToolsCarryTableHints(t *testing.T) {
	srv := newDemoServer(t, capability.Names())
	srv.RegisterCacheDependentTools()
	tools := registeredTools(srv)
	for _, spec := range capability.Tools {
		t.Run(spec.Name, func(t *testing.T) {
			tool := tools[spec.Name]
			require.NotNil(t, tool.Annotations.ReadOnlyHint)
			require.NotNil(t, tool.Annotations.DestructiveHint)
			require.NotNil(t, tool.Annotations.IdempotentHint)
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
		})
	}
}

// An agent has to parse the prepare result of every approval tool to get the
// token it passes to execute.
func TestApprovalToolsAdvertiseTheirToken(t *testing.T) {
	srv := newDemoServer(t, capability.Names())
	advertised := 0
	for name, tool := range registeredTools(srv) {
		if tool.OutputSchema.Type == "" || name == capability.SlackAuthStatus {
			continue
		}
		advertised++
		data, ok := tool.OutputSchema.Properties["data"].(map[string]any)
		require.True(t, ok, "%s data schema", name)
		props, ok := data["properties"].(map[string]any)
		require.True(t, ok, "%s data properties", name)
		assert.Contains(t, props, "approval_token", "%s prepare must return data.approval_token", name)
	}
	assert.Equal(t, 5, advertised)
}

func TestListsWriteToolsDeclareObjectArrayContracts(t *testing.T) {
	tools := registeredTools(newDemoServer(t, capability.Names()))

	assertObjectItems := func(toolName, property string) map[string]any {
		tool, ok := tools[toolName]
		require.True(t, ok)
		array, ok := tool.InputSchema.Properties[property].(map[string]any)
		require.True(t, ok, "%s.%s must be an array schema", toolName, property)
		items, ok := array["items"].(map[string]any)
		require.True(t, ok, "%s.%s must declare item schema", toolName, property)
		assert.Equal(t, "object", items["type"])
		return items
	}

	column := assertObjectItems(capability.ListsCreate, "schema")
	assert.NotEmpty(t, column["properties"])
	assert.ElementsMatch(t, []string{"key", "name", "type"}, column["required"])
	assertObjectItems(capability.ListsCreate, "description_blocks")
	assertObjectItems(capability.ListsUpdate, "description_blocks")

	initial := assertObjectItems(capability.ListsItemsCreate, "initial_fields")
	assert.NotEmpty(t, initial["properties"])
	assert.ElementsMatch(t, []string{"column_id"}, initial["required"])
	assert.Len(t, initial["oneOf"], 10)

	cells := assertObjectItems(capability.ListsItemsUpdate, "cells")
	assert.NotEmpty(t, cells["properties"])
	assert.ElementsMatch(t, []string{"column_id", "row_id"}, cells["required"])
	assert.Len(t, cells["oneOf"], 10)
	properties := cells["properties"].(map[string]any)
	link := properties["link"].(map[string]any)["items"].(map[string]any)
	assert.ElementsMatch(t, []string{"original_url"}, link["required"])
}
