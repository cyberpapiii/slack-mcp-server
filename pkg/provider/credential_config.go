package provider

import (
	"os"
	"strings"
)

// CredentialConfig is the credential environment, read once. Every value is
// trimmed; empty means unset.
type CredentialConfig struct {
	XOXP string // SLACK_MCP_XOXP_TOKEN
	XOXB string // SLACK_MCP_XOXB_TOKEN
	XOXC string // SLACK_MCP_XOXC_TOKEN
	XOXD string // SLACK_MCP_XOXD_TOKEN

	OAuthKeychainAccount string // SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT
	OAuthClientID        string // SLACK_MCP_OAUTH_CLIENT_ID
	OAuthClientSecret    string // SLACK_MCP_OAUTH_CLIENT_SECRET

	BrowserKeychainAccount string // SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT
}

func CredentialConfigFromEnv() CredentialConfig {
	env := func(key string) string { return strings.TrimSpace(os.Getenv(key)) }
	return CredentialConfig{
		XOXP:                   env("SLACK_MCP_XOXP_TOKEN"),
		XOXB:                   env("SLACK_MCP_XOXB_TOKEN"),
		XOXC:                   env("SLACK_MCP_XOXC_TOKEN"),
		XOXD:                   env("SLACK_MCP_XOXD_TOKEN"),
		OAuthKeychainAccount:   env("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT"),
		OAuthClientID:          env("SLACK_MCP_OAUTH_CLIENT_ID"),
		OAuthClientSecret:      env("SLACK_MCP_OAUTH_CLIENT_SECRET"),
		BrowserKeychainAccount: env("SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT"),
	}
}

// Demo reports whether the config holds the documented placeholder tokens.
// Demo mode builds a real client around a canned auth response so every tool
// registers, but never reaches Slack.
func (c CredentialConfig) Demo() bool {
	return c.XOXP == "demo" || (c.XOXC == "demo" && c.XOXD == "demo")
}

// IsDemoCredentials reports whether the environment holds the demo tokens.
// After New, Mode() == ModeDemo says the same without reading the environment.
func IsDemoCredentials() bool {
	return CredentialConfigFromEnv().Demo()
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
