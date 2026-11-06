package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// rateLimitedClient answers every Web API call with a 429 and counts them.
func rateLimitedClient(t *testing.T, pacing limiter.Pacing) (*MCPSlackClient, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	return &MCPSlackClient{slackClient: slack.New("xoxp-test", slack.OptionAPIURL(server.URL+"/")), pacing: pacing}, &calls
}

// Callers no longer wrap these reads in their own limiter and retry; the
// client does, the same way for every caller.
func TestUnitPacedReadsRetryARateLimit(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(*WebClient, *MCPSlackClient) error{
		"conversations.replies": func(w *WebClient, _ *MCPSlackClient) error {
			_, _, _, err := w.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{ChannelID: "C1", Timestamp: "1.2"})
			return err
		},
		"conversations.history": func(w *WebClient, _ *MCPSlackClient) error {
			_, err := w.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: "C1"})
			return err
		},
		"search.messages": func(w *WebClient, _ *MCPSlackClient) error {
			_, _, err := w.SearchContext(ctx, "q", slack.SearchParameters{})
			return err
		},
		"conversations.list": func(_ *WebClient, c *MCPSlackClient) error {
			_, _, err := c.GetConversationsContext(ctx, &slack.GetConversationsParameters{})
			return err
		},
	}
	for method, call := range calls {
		t.Run(method, func(t *testing.T) {
			client, count := rateLimitedClient(t, nil)
			err := call(&WebClient{client: client}, client)
			var limited *slack.RateLimitedError
			require.ErrorAs(t, err, &limited)
			assert.Equal(t, int32(1+limiter.Retries), count.Load(), "one attempt plus limiter.Retries")
		})
	}
}

// Each paced read waits on its own method's bucket. A bucket that can never
// grant a token fails the wait, which proves the read consulted it.
func TestUnitPacedReadsWaitOnTheirMethodsBucket(t *testing.T) {
	ctx := context.Background()
	for method, call := range map[string]func(*WebClient, *MCPSlackClient) error{
		"conversations.replies": func(w *WebClient, _ *MCPSlackClient) error {
			_, _, _, err := w.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{ChannelID: "C1", Timestamp: "1.2"})
			return err
		},
		"search.messages": func(w *WebClient, _ *MCPSlackClient) error {
			_, _, err := w.SearchContext(ctx, "q", slack.SearchParameters{})
			return err
		},
		"conversations.list": func(_ *WebClient, c *MCPSlackClient) error {
			_, _, err := c.GetConversationsContext(ctx, &slack.GetConversationsParameters{})
			return err
		},
	} {
		t.Run(method, func(t *testing.T) {
			client, count := rateLimitedClient(t, limiter.Pacing{method: rate.NewLimiter(0, 0)})
			err := call(&WebClient{client: client}, client)
			require.ErrorContains(t, err, "rate limiter")
			assert.Zero(t, count.Load(), "the call waits before reaching Slack")
		})
	}
}

func TestUnitSlackPacingNamesEveryPacedMethod(t *testing.T) {
	pacing := limiter.SlackPacing()
	assert.Same(t, limiter.Tier2, pacing["conversations.list"])
	assert.Same(t, limiter.ConversationsReplies, pacing["conversations.replies"])
	assert.Same(t, limiter.SearchMessages, pacing["search.messages"])
	assert.Same(t, limiter.Tier2boost, pacing["im.list"])
	assert.Same(t, limiter.Tier2boost, pacing["search.modules.channels"])
	assert.Same(t, limiter.Tier3, pacing["saved.list"])
	assert.Nil(t, pacing["conversations.history"], "history is retried, not paced")
}
