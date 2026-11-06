package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

// The bucket test above proves each read looks up its own method name; this
// proves the production table carries a bucket under that same name, so a
// renamed key cannot silently leave a read unpaced.
func TestUnitSlackPacingCoversEveryPacedRead(t *testing.T) {
	pacing := limiter.SlackPacing()
	for _, method := range []string{"conversations.list", "conversations.replies", "search.messages"} {
		assert.NotNil(t, pacing[method], method)
	}
	assert.Nil(t, pacing["conversations.history"], "history is retried, not paced")
}

// The Enterprise merge walks every conversations.list page in one call; each
// page waits on the bucket, not just the first, and carries the caller's
// params.
func TestUnitEnterpriseMergePacesEveryPage(t *testing.T) {
	var calls atomic.Int32
	var types []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.NoError(t, r.ParseForm())
		types = append(types, r.Form.Get("types"))
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1"}],"response_metadata":{"next_cursor":"more"}}`))
	}))
	t.Cleanup(server.Close)
	client := &MCPSlackClient{
		slackClient: slack.New("xoxc-test", slack.OptionAPIURL(server.URL+"/")),
		pacing:      limiter.Pacing{"conversations.list": rate.NewLimiter(0, 1)},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	params := &slack.GetConversationsParameters{Types: []string{"im"}}
	_, _, err := mergeStandardConversationPages(nil, params, func(p *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
		return client.conversationsPage(ctx, p)
	})
	require.ErrorContains(t, err, "rate limiter", "the second page waits on the spent bucket")
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, []string{"im"}, types)
}
