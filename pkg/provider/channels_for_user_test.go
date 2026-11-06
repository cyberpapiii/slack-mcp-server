package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usersConversationsServer answers users.conversations with as many channels
// as each request asks for, out of total, and records the requested limits.
func usersConversationsServer(t *testing.T, total int) (*ApiProvider, *[]int) {
	t.Helper()
	var limits []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		limit, _ := strconv.Atoi(r.Form.Get("limit"))
		start, _ := strconv.Atoi(r.Form.Get("cursor"))
		limits = append(limits, limit)
		var channels []map[string]any
		for i := start; i < min(start+limit, total); i++ {
			channels = append(channels, map[string]any{"id": fmt.Sprintf("C%03d", i), "name": fmt.Sprintf("c%d", i), "is_channel": true})
		}
		next := ""
		if start+limit < total {
			next = strconv.Itoa(start + limit)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channels": channels, "response_metadata": map[string]any{"next_cursor": next}})
	}))
	t.Cleanup(server.Close)
	return &ApiProvider{client: MCPSlackClient{slackClient: slack.New("x", slack.OptionAPIURL(server.URL+"/"))}}, &limits
}

func TestUnitChannelsForUserAsksOnlyForTheRowsStillNeeded(t *testing.T) {
	ap, limits := usersConversationsServer(t, 1000)
	channels, next, err := ap.ChannelsForUser(context.Background(), []string{"public_channel"}, "", 250)
	require.NoError(t, err)
	assert.Len(t, channels, 250)
	assert.Equal(t, []int{200, 50}, *limits, "pages cap at Slack's 200, then ask for the remainder")
	assert.Equal(t, "250", next, "the cursor resumes right after the last channel returned")
	assert.Equal(t, "C249", channels[249].ID)
}

func TestUnitChannelsForUserStopsAtTheLastPage(t *testing.T) {
	ap, limits := usersConversationsServer(t, 30)
	channels, next, err := ap.ChannelsForUser(context.Background(), []string{"public_channel"}, "", 100)
	require.NoError(t, err)
	assert.Len(t, channels, 30)
	assert.Empty(t, next)
	assert.Equal(t, []int{100}, *limits)
}
