package provider

import "strings"

// CredentialMode is the set of Slack credentials the server runs on. New
// decides it once; the browser session's health can change later, the mode
// cannot.
type CredentialMode int

const (
	ModeBot              CredentialMode = iota + 1 // a bot OAuth token
	ModeUserOAuth                                  // a user OAuth token, no browser session
	ModeBrowser                                    // a browser session (xoxc/xoxd) alone
	ModeUserOAuthBrowser                           // a user OAuth token with a browser session beside it
	ModeDemo                                       // the documented placeholder tokens; every tool registers
)

// credentialModeFor classifies the primary token. xoxe.* are the rotating
// variants of xoxp/xoxb, with the same scopes and a ~12h expiry.
func credentialModeFor(token string, browserConfigured bool) CredentialMode {
	switch {
	case strings.HasPrefix(token, "xoxb-") || strings.HasPrefix(token, "xoxe.xoxb-"):
		return ModeBot
	case strings.HasPrefix(token, "xoxp-") || strings.HasPrefix(token, "xoxe.xoxp-"):
		if browserConfigured {
			return ModeUserOAuthBrowser
		}
		return ModeUserOAuth
	default:
		return ModeBrowser
	}
}

// OAuth reports whether Web API calls carry an OAuth token, user or bot.
func (m CredentialMode) OAuth() bool {
	return m == ModeBot || m == ModeUserOAuth || m == ModeUserOAuthBrowser || m == ModeDemo
}

// BrowserSession reports whether a browser session is configured, healthy or
// not.
func (m CredentialMode) BrowserSession() bool {
	return m == ModeBrowser || m == ModeUserOAuthBrowser || m == ModeDemo
}

// UserToken reports whether Slack sees the calls as a user: any token but a
// bot token.
func (m CredentialMode) UserToken() bool {
	return m != ModeBot
}

// UserOAuth reports whether Web API calls carry a user OAuth token.
func (m CredentialMode) UserOAuth() bool {
	return m == ModeUserOAuth || m == ModeUserOAuthBrowser || m == ModeDemo
}

// BrowserRecovers reports whether a degraded browser session comes back by
// itself: beside OAuth the re-probe reloads it every 5 minutes. A browser-only
// session has nothing to re-probe with and needs a restart.
func (m CredentialMode) BrowserRecovers() bool {
	return m == ModeUserOAuthBrowser
}
