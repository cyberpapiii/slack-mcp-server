package handler

import (
	"fmt"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
)

// sendStatus reports whether conversations_add_message would accept a
// message to the given channel, for the conversations_draft_message preview.
func (ch *ConversationsHandler) sendStatus(channel string) string {
	if !ch.settings.AddMessageEnabled {
		return "not available"
	}
	if !ch.settings.channelAllowed(capability.ConversationsAddMessage, channel) {
		return "not available for this channel"
	}
	return "available"
}

// browserSessionRequired is the error every xoxc/xoxd-only tool returns when
// the provider reports browser credentials missing or expired.
func browserSessionRequired(tool, reason string) error {
	if reason == "" {
		reason = "browser-session credentials are missing or expired"
	}
	return &ToolError{
		Code:    "browser_session_required",
		Message: fmt.Sprintf("%s needs a Slack browser session (xoxc/xoxd): %s. Check slack_auth_status; token-only tools still work.", tool, reason),
	}
}
