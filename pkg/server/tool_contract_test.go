package server

import (
	"slices"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDailyPowerToolContractsAreComplete(t *testing.T) {
	tools := capability.DailyPowerLocalTools()
	require.NotEmpty(t, tools)

	for _, name := range tools {
		t.Run(name, func(t *testing.T) {
			behavior, ok := capability.BehaviorForLocalTool(name)
			require.True(t, ok)
			entry, ok := capability.EntryForLocalTool(name)
			require.True(t, ok)
			assert.Equal(t, capability.ConfirmationNone, entry.Confirmation)

			tool := newDailyPowerTool(name, mcp.WithDescription("contract test"))
			if _, declared := outputSchemaByTool[name]; declared {
				assert.Equal(t, "object", tool.OutputSchema.Type)
				assert.Contains(t, tool.OutputSchema.Properties, "schema_version")
				assert.Contains(t, tool.OutputSchema.Properties, "meta")
				assert.Contains(t, tool.OutputSchema.Properties, "data")
				assert.Contains(t, tool.OutputSchema.Properties, "error")
			} else {
				assert.Empty(t, tool.OutputSchema.Type, "only approval-flow and diagnostics tools advertise an output schema")
			}

			require.NotNil(t, tool.Annotations.ReadOnlyHint)
			require.NotNil(t, tool.Annotations.DestructiveHint)
			require.NotNil(t, tool.Annotations.IdempotentHint)
			require.NotNil(t, tool.Annotations.OpenWorldHint)
			assert.Equal(t, behavior.Title, tool.Annotations.Title)
			assert.Equal(t, behavior.ReadOnly, *tool.Annotations.ReadOnlyHint)
			assert.Equal(t, behavior.Destructive, *tool.Annotations.DestructiveHint)
			assert.Equal(t, behavior.Idempotent, *tool.Annotations.IdempotentHint)
			assert.Equal(t, behavior.OpenWorld, *tool.Annotations.OpenWorldHint)
		})
	}
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
		tool := newDailyPowerTool(name, mcp.WithDescription("contract test"))
		data, ok := tool.OutputSchema.Properties["data"].(map[string]any)
		require.True(t, ok, "%s data schema", name)
		props, ok := data["properties"].(map[string]any)
		require.True(t, ok, "%s data properties", name)
		assert.Contains(t, props, "approval_token", "%s prepare must return data.approval_token", name)
	}
}

func TestDailyPowerContractsDoNotClaimLegacyTools(t *testing.T) {
	for _, name := range capability.LegacyFullLocalTools() {
		if slices.Contains(capability.DailyPowerLocalTools(), name) {
			continue
		}
		if _, active := capability.EntryForLocalTool(name); active {
			continue
		}
		_, ok := capability.BehaviorForLocalTool(name)
		assert.False(t, ok, "legacy tool %q must not claim an unmigrated result contract", name)
	}
}

func TestAllActiveLocalToolsHaveTypedContracts(t *testing.T) {
	for _, entry := range capability.Entries() {
		if entry.Owner == capability.OwnerOfficial || entry.Migration != capability.MigrationActive {
			continue
		}
		behavior, ok := capability.BehaviorForLocalTool(entry.LocalTool)
		require.True(t, ok, "active tool %q has no behavior contract", entry.LocalTool)
		tool := newDailyPowerTool(entry.LocalTool, mcp.WithDescription("contract test"))
		require.NotNil(t, tool.Annotations.ReadOnlyHint)
		require.NotNil(t, tool.Annotations.DestructiveHint)
		require.NotNil(t, tool.Annotations.IdempotentHint)
		require.NotNil(t, tool.Annotations.OpenWorldHint)
		assert.Equal(t, behavior.Title, tool.Annotations.Title)
	}
}
