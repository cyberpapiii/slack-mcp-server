package main

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveEnabledTools(t *testing.T) {
	t.Run("explicit list overrides preset", func(t *testing.T) {
		tools, err := resolveEnabledTools("channels_list, users_search", "daily-power")
		require.NoError(t, err)
		assert.Equal(t, []string{"channels_list", "users_search"}, tools)
	})

	t.Run("daily power is default", func(t *testing.T) {
		tools, err := resolveEnabledTools("", "")
		require.NoError(t, err)
		assert.Contains(t, tools, server.ToolSlackAuthStatus)
		assert.NotContains(t, tools, server.ToolConversationsAddMessage)
	})

	t.Run("legacy preset remains available", func(t *testing.T) {
		tools, err := resolveEnabledTools("", "legacy-full")
		require.NoError(t, err)
		assert.ElementsMatch(t, server.ValidToolNames, tools)
	})
}
