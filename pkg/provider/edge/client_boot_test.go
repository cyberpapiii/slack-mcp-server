package edge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitClientUserBootResponseIgnoresUnreadJSON(t *testing.T) {
	payload := []byte(`{
		"ok": true,
		"self": {"id": "U1", "name": "ignored"},
		"team": {"id": "T1", "prefs": {"dnd_enabled": true}},
		"ims": [{"id": "D1", "user": "U2", "is_shared": true, "is_ext_shared": false}],
		"starred": ["C1", "C2"],
		"channels": [{
			"id": "C1",
			"name": "general",
			"is_channel": true,
			"is_general": true,
			"members": ["U1"],
			"topic": {"value": "t", "creator": "U1", "last_set": 1},
			"purpose": {"value": "p", "creator": "U1", "last_set": 1}
		}]
	}`)

	var got ClientUserBootResponse
	require.NoError(t, json.Unmarshal(payload, &got))
	require.Len(t, got.IMs, 1)
	assert.Equal(t, "D1", got.IMs[0].ID)
	assert.Equal(t, "U2", got.IMs[0].User)
	assert.True(t, got.IMs[0].IsShared)
	assert.Equal(t, []any{"C1", "C2"}, got.Starred)
	require.Len(t, got.Channels, 1)
	ch := got.Channels[0].SlackChannel()
	assert.Equal(t, "C1", ch.ID)
	assert.Equal(t, "general", ch.Name)
	assert.True(t, ch.IsChannel)
	assert.True(t, ch.IsGeneral)
	assert.Equal(t, "t", ch.Topic.Value)
	assert.Equal(t, "p", ch.Purpose.Value)
}
