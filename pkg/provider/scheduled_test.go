package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledProviderListUsesSlackScheduledList(t *testing.T) {
	var path string
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		require.NoError(t, r.ParseForm())
		form = r.Form
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"scheduled_messages":[{"id":"Q1","channel_id":"C1","post_at":1786305600,"text":"hello"}],"response_metadata":{"next_cursor":"next"}}`))
	}))
	defer server.Close()

	service := NewScheduledProvider(slack.New("token", slack.OptionAPIURL(server.URL+"/")))
	page, err := service.ListScheduled(context.Background(), ScheduledListRequest{ChannelID: "C1", Cursor: "cur", Limit: 25})
	require.NoError(t, err)
	assert.Equal(t, "/chat.scheduledMessages.list", path)
	assert.Equal(t, "C1", form.Get("channel"))
	assert.Equal(t, "cur", form.Get("cursor"))
	assert.Equal(t, "25", form.Get("limit"))
	require.Len(t, page.Messages, 1)
	assert.Equal(t, "Q1", page.Messages[0].ScheduledMessageID)
	assert.Equal(t, time.Unix(1786305600, 0).UTC(), page.Messages[0].PostAt)
	assert.Equal(t, "next", page.NextCursor)
}

func TestScheduledProviderCancelUsesExactSlackMethodAndDoesNotRetryRateLimit(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "/chat.deleteScheduledMessage", r.URL.Path)
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "C1", r.Form.Get("channel"))
		assert.Equal(t, "Q1", r.Form.Get("scheduled_message_id"))
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	service := NewScheduledProvider(slack.New("token", slack.OptionAPIURL(server.URL+"/")))
	err := service.CancelScheduled(context.Background(), "C1", "Q1")
	var limited *slack.RateLimitedError
	require.ErrorAs(t, err, &limited)
	assert.Equal(t, 7*time.Second, limited.RetryAfter)
	assert.Equal(t, 1, calls)
}

type timeoutScheduledClient struct{ calls int }

func (c *timeoutScheduledClient) GetScheduledMessagesContext(context.Context, *slack.GetScheduledMessagesParameters) ([]slack.ScheduledMessage, string, error) {
	return nil, "", nil
}

func (c *timeoutScheduledClient) DeleteScheduledMessageContext(context.Context, *slack.DeleteScheduledMessageParameters) (bool, error) {
	c.calls++
	return false, context.DeadlineExceeded
}

func TestScheduledProviderCancelDoesNotRetryAmbiguousTimeout(t *testing.T) {
	client := &timeoutScheduledClient{}
	service := NewScheduledProvider(client)
	err := service.CancelScheduled(context.Background(), "C1", "Q1")
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	assert.Equal(t, 1, client.calls)
}

func TestScheduledAccessorRejectsUnsupportedClient(t *testing.T) {
	_, err := (&ApiProvider{}).Scheduled()
	require.Error(t, err)
}
