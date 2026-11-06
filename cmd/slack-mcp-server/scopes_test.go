package main

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/stretchr/testify/assert"
)

func TestMissingOAuthScopesForServedTools(t *testing.T) {
	enabled := []string{capability.DNDGet, capability.ConversationsMark}
	granted := []string{"channels:read", "groups:read", "im:read", "mpim:read", "users:read"}

	assert.Equal(t, []string{"channels:write", "dnd:read", "groups:write", "im:write", "mpim:write"},
		missingOAuthScopes(granted, servedTools(provider.ModeUserOAuth, enabled)))
	assert.Empty(t, servedTools(provider.ModeBot, enabled), "a bot serves neither, so needs no scope for them")
	assert.Empty(t, missingOAuthScopes(capability.OAuthScopesForTools(enabled), enabled))
}
