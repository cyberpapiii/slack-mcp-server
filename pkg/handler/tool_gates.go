package handler

import (
	"os"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
)

func isChannelAllowedForConfig(channel, config string) bool {
	// Registration treats any non-empty allowlist-gate value as enabled; for
	// the "all channels" case accept the same truthy set as envutil.IsTruthy
	// (true/1/yes), not only exact "true"/"1".
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

// checkSendStatus reports whether conversations_add_message would accept a message
// to the given channel. Returns a human-readable status string.
func checkSendStatus(channel string) string {
	addMessageTool := os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")
	addMessageEnabled := os.Getenv("SLACK_MCP_ENABLED_TOOLS")
	if addMessageTool == "" && !isToolInEnabledList(addMessageEnabled, "conversations_add_message") {
		return "not available"
	}
	if !isChannelAllowedForConfig(channel, addMessageTool) {
		return "not available for this channel"
	}
	return "available"
}

func isToolInEnabledList(enabledTools, toolName string) bool {
	if enabledTools == "" {
		return false
	}
	for _, t := range strings.Split(enabledTools, ",") {
		if strings.TrimSpace(t) == toolName {
			return true
		}
	}
	return false
}

// Dedicated env truthy, or toolName listed in SLACK_MCP_ENABLED_TOOLS.
func requireToolEnabled(envVarName, toolName string) bool {
	if envutil.IsTruthy(os.Getenv(envVarName)) {
		return true
	}
	return isToolInEnabledList(os.Getenv("SLACK_MCP_ENABLED_TOOLS"), toolName)
}
