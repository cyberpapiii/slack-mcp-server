package handler

import (
	"fmt"
	"os"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
)

// isChannelAllowedForConfig applies a channel allowlist such as
// SLACK_MCP_ADD_MESSAGE_TOOL. Empty or truthy means every channel;
// "C1,C2" allows only those; "!C1,!C2" allows everything except those.
func isChannelAllowedForConfig(channel, config string) bool {
	if config == "" || envutil.IsTruthy(config) {
		return true
	}
	items := strings.Split(config, ",")
	isNegated := false
	sawEntry := false
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || item == "!" {
			continue
		}
		if !sawEntry {
			isNegated = strings.HasPrefix(item, "!")
			sawEntry = true
		}
		if isNegated {
			if strings.TrimPrefix(item, "!") == channel {
				return false
			}
		} else if item == channel {
			return true
		}
	}
	// Empty/`!`-only lists are invalid config (startup should reject); fail closed.
	if !sawEntry {
		return false
	}
	return isNegated
}

// sendStatus reports whether conversations_add_message would accept a
// message to the given channel, for the conversations_draft_message preview.
func (ch *ConversationsHandler) sendStatus(channel string) string {
	if !ch.sendEnabled {
		return "not available"
	}
	if !isChannelAllowedForConfig(channel, os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")) {
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
