package provider

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/stretchr/testify/assert"
)

func TestUnitCredentialModeForPrimaryToken(t *testing.T) {
	assert.Equal(t, ModeBot, credentialModeFor("xoxb-1", true), "a bot token never carries a browser session")
	assert.Equal(t, ModeBot, credentialModeFor("xoxe.xoxb-1", false))
	assert.Equal(t, ModeUserOAuth, credentialModeFor("xoxe.xoxp-1", false))
	assert.Equal(t, ModeUserOAuthBrowser, credentialModeFor("xoxp-1", true))
	assert.Equal(t, ModeBrowser, credentialModeFor("xoxc-1", true))
}

func TestUnitCredentialModeSatisfies(t *testing.T) {
	for mode, want := range map[CredentialMode][]capability.Requirement{
		ModeBot:              {capability.AnyToken},
		ModeUserOAuth:        {capability.AnyToken, capability.UserToken, capability.UserOAuth},
		ModeBrowser:          {capability.AnyToken, capability.UserToken, capability.BrowserSession},
		ModeUserOAuthBrowser: {capability.AnyToken, capability.UserToken, capability.UserOAuth, capability.BrowserSession},
		ModeDemo:             {capability.AnyToken, capability.UserToken, capability.UserOAuth, capability.BrowserSession},
	} {
		var got []capability.Requirement
		for _, requirement := range []capability.Requirement{capability.AnyToken, capability.UserToken, capability.UserOAuth, capability.BrowserSession} {
			if mode.satisfies(requirement) {
				got = append(got, requirement)
			}
		}
		assert.Equal(t, want, got, "mode %d", mode)
	}
}
