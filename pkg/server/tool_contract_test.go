package server

import (
	"slices"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/mark3labs/mcp-go/mcp"
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
			assert.Equal(t, "object", tool.OutputSchema.Type)
			assert.NotEmpty(t, tool.OutputSchema.Properties)
			assert.Contains(t, tool.OutputSchema.Properties, "schema_version")
			assert.Contains(t, tool.OutputSchema.Properties, "meta")
			assert.Contains(t, tool.OutputSchema.Properties, "data")
			assert.Contains(t, tool.OutputSchema.Properties, "error")

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

func TestDailyPowerContractsDoNotClaimLegacyTools(t *testing.T) {
	for _, name := range capability.LegacyFullLocalTools() {
		if slices.Contains(capability.DailyPowerLocalTools(), name) {
			continue
		}
		_, ok := capability.BehaviorForLocalTool(name)
		assert.False(t, ok, "legacy tool %q must not claim an unmigrated result contract", name)
	}
}
