package handler

import (
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
)

func TestUnitSearchMatchMessage(t *testing.T) {
	match := slack.SearchMessage{
		Channel:   slack.CtxChannel{ID: "C1"},
		User:      "U1",
		Timestamp: "1712345678.000200",
		Text:      "hello",
		Permalink: "https://x.slack.com/archives/C1/p1712345678000200?thread_ts=1712345678.000100",
	}
	msg := searchMatchMessage(match)
	assert.Equal(t, "C1", msg.Channel)
	assert.Equal(t, "1712345678.000100", msg.ThreadTimestamp)
	assert.Equal(t, match.Permalink, msg.Permalink)
	assert.Equal(t, "", msg.SubType)
	assert.True(t, rendersInHistory(msg, false))

	bot := searchMatchMessage(slack.SearchMessage{Username: "deploybot", Timestamp: "1712345678.000300"})
	assert.Equal(t, "bot_message", bot.SubType, "a match with a username and no user is a bot post, as history marks it")
}
