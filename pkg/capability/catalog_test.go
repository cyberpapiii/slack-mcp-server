package capability

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolNamesAreUniqueAndTitled(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range Tools {
		assert.NotEmpty(t, tool.Name)
		assert.NotEmpty(t, tool.Title, "tool %q has no title", tool.Name)
		assert.False(t, seen[tool.Name], "duplicate tool %q", tool.Name)
		seen[tool.Name] = true
	}
	assert.Len(t, Names(), len(Tools))
	assert.True(t, slices.IsSorted(Names()))
}

func TestLookup(t *testing.T) {
	tool, ok := Lookup("conversations_history")
	require.True(t, ok)
	assert.Equal(t, "Get Conversation History", tool.Title)
	assert.True(t, tool.ReadOnly)

	_, ok = Lookup("no_such_tool")
	assert.False(t, ok)
}

func TestDailyPowerIsTheReadOnlySubset(t *testing.T) {
	daily := DailyPowerNames()
	require.NotEmpty(t, daily)
	all := Names()
	for _, name := range daily {
		assert.Contains(t, all, name)
		tool, ok := Lookup(name)
		require.True(t, ok)
		assert.True(t, tool.ReadOnly, "daily-power tool %q must be read-only", name)
	}
	assert.Less(t, len(daily), len(all))
	for _, forbidden := range []string{"conversations_add_message", "conversations_draft_message", "conversations_search_messages", "files_list", "users_search"} {
		assert.NotContains(t, daily, forbidden)
	}
}

func TestBrowserNames(t *testing.T) {
	assert.Equal(t, []string{
		"activity_mark_read",
		"activity_unreads",
		"conversations_unreads",
		"drafts_create",
		"drafts_delete",
		"drafts_get",
		"drafts_list",
		"drafts_update",
		"saved_clear_completed",
		"saved_list",
		"saved_update",
	}, BrowserNames())
	for _, name := range BrowserNames() {
		tool, _ := Lookup(name)
		assert.Empty(t, tool.Scopes, "browser-session tool %q needs no OAuth scope", name)
	}
}

func TestOAuthScopesForToolsDedupsAndSorts(t *testing.T) {
	scopes := OAuthScopesForTools([]string{"conversations_history", "conversations_replies", "channels_list", "no_such_tool"})
	assert.Equal(t, []string{
		"channels:history", "channels:read",
		"groups:history", "groups:read",
		"im:history", "im:read",
		"mpim:history", "mpim:read",
	}, scopes)
	assert.Empty(t, OAuthScopesForTools(nil))
}
