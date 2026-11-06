package handler

import (
	"context"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUnitAColdCacheFailsOnlyNameLookups(t *testing.T) {
	p := messagePipeline{api: &provider.ApiProvider{}, logger: zap.NewNop()}

	for _, ref := range []string{"C0123ABC", "<#C0123ABC|general>"} {
		id, err := p.channelID(context.Background(), ref)
		require.NoError(t, err, "an ID needs no cache")
		assert.Equal(t, "C0123ABC", id)
	}
	for _, ref := range []string{"#general", "@alice"} {
		_, err := p.channelID(context.Background(), ref)
		var typed *ToolError
		require.ErrorAs(t, TypeError(err, false), &typed, ref)
		assert.Equal(t, "cache_warming", typed.Code, ref)
		assert.True(t, typed.Retryable, ref)
	}
}

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
	assert.Equal(t, "", bot.SubType, "no subtype is invented for a bot match")
	assert.Equal(t, "deploybot", bot.Username)
}
