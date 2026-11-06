package provider

import (
	"os"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
)

// envBrowserKeychainAccount names the browser session's Keychain item. The
// OAuth item's variables are oauthcred's.
const envBrowserKeychainAccount = "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT"

// CredentialConfig is the credential environment, read once. Every value is
// trimmed; empty means unset.
type CredentialConfig struct {
	XOXP string // SLACK_MCP_XOXP_TOKEN
	XOXB string // SLACK_MCP_XOXB_TOKEN
	XOXC string // SLACK_MCP_XOXC_TOKEN
	XOXD string // SLACK_MCP_XOXD_TOKEN

	OAuthKeychainAccount string // oauthcred.EnvKeychainAccount
	OAuthClientID        string // oauthcred.EnvClientID
	OAuthClientSecret    string // oauthcred.EnvClientSecret

	BrowserKeychainAccount string // envBrowserKeychainAccount
}

func CredentialConfigFromEnv() CredentialConfig {
	env := func(key string) string { return strings.TrimSpace(os.Getenv(key)) }
	return CredentialConfig{
		XOXP:                   env("SLACK_MCP_XOXP_TOKEN"),
		XOXB:                   env("SLACK_MCP_XOXB_TOKEN"),
		XOXC:                   env("SLACK_MCP_XOXC_TOKEN"),
		XOXD:                   env("SLACK_MCP_XOXD_TOKEN"),
		OAuthKeychainAccount:   env(oauthcred.EnvKeychainAccount),
		OAuthClientID:          env(oauthcred.EnvClientID),
		OAuthClientSecret:      env(oauthcred.EnvClientSecret),
		BrowserKeychainAccount: env(envBrowserKeychainAccount),
	}
}

// Demo reports whether the config holds the documented placeholder tokens.
// Demo mode builds a real client around a canned auth response so every tool
// registers, but never reaches Slack.
func (c CredentialConfig) Demo() bool {
	return c.XOXP == "demo" || (c.XOXC == "demo" && c.XOXD == "demo")
}

func (c CredentialConfig) browserCredentialSource() string {
	if c.BrowserKeychainAccount != "" {
		return "macos_keychain"
	}
	if c.XOXC != "" && c.XOXD != "" {
		return "environment_compatibility"
	}
	return "not_configured"
}
