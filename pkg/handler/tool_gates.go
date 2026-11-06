package handler

import (
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
)

// sendStatus reports whether conversations_add_message would accept a
// message to the given channel, for the conversations_draft_message preview.
func (ch *ConversationsHandler) sendStatus(channel string) string {
	if !ch.availability.servesName(capability.ConversationsAddMessage) {
		return "not available"
	}
	if ch.settings.requireChannel(capability.ConversationsAddMessage, channel) != nil {
		return "not available for this channel"
	}
	return "available"
}
