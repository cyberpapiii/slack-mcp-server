package edge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each edge source sends conversations a little differently; one mapper turns
// all of them into the slack-go shape the channel cache reads.
func TestUnitChannelMapping(t *testing.T) {
	t.Run("im.list: latest is a timestamp, the user is the only member", func(t *testing.T) {
		var c Channel
		require.NoError(t, json.Unmarshal([]byte(`{"id":"D1","is_im":true,"user":"U2","created":1700000000,
			"last_read":"0000000000.000000","latest":"1710000000.000100"}`), &c))
		ch := c.slackChannel()
		assert.Equal(t, "U2", ch.User)
		assert.Equal(t, []string{"U2"}, ch.Members)
		assert.Equal(t, "", ch.LastRead, "a never-read DM has no last-read bound")
		assert.EqualValues(t, 1700000000, ch.Created, "created stays in seconds")
		assert.False(t, ch.IsChannel)
	})

	t.Run("search: member_count is the member count", func(t *testing.T) {
		var c Channel
		require.NoError(t, json.Unmarshal([]byte(`{"id":"C1","name":"general","member_count":42,"last_read":"1710000000.000100"}`), &c))
		ch := c.slackChannel()
		assert.Equal(t, 42, ch.NumMembers)
		assert.True(t, ch.IsChannel, "a conversation with no type flag is a public channel")
		assert.Equal(t, "1710000000.000100", ch.LastRead)
	})

	t.Run("userBoot: an IM's user comes from its members", func(t *testing.T) {
		var c Channel
		require.NoError(t, json.Unmarshal([]byte(`{"id":"D2","is_im":true,"members":["","U3"]}`), &c))
		ch := c.slackChannel()
		assert.Equal(t, "U3", ch.User)
		assert.Equal(t, 2, ch.NumMembers)
	})
}
