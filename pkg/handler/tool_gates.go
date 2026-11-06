package handler

import (
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
