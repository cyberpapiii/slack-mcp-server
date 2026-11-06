package slackhttp

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type doFunc func(*http.Request) (*http.Response, error)

func (f doFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func respond(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestPostForm(t *testing.T) {
	var got *http.Request
	var gotBody string
	client := doFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		return respond(http.StatusOK, `{"ok":true}`, nil), nil
	})

	t.Run("token joins the form", func(t *testing.T) {
		_, err := Post(context.Background(), client, Request{URL: "https://x.slack.com/api/a.b", Token: "xoxc-1", Form: url.Values{"b": {"2"}, "a": {"1"}}})
		require.NoError(t, err)
		assert.Equal(t, "a=1&b=2&token=xoxc-1", gotBody)
		assert.Equal(t, "application/x-www-form-urlencoded", got.Header.Get("Content-Type"))
		assert.Empty(t, got.Header.Get("Authorization"))
	})

	t.Run("a form token wins", func(t *testing.T) {
		_, err := Post(context.Background(), client, Request{URL: "https://x.slack.com/api/a.b", Token: "xoxc-1", Form: url.Values{"token": {"mine"}}})
		require.NoError(t, err)
		assert.Equal(t, "token=mine", gotBody)
	})

	t.Run("bearer token goes in the header", func(t *testing.T) {
		_, err := Post(context.Background(), client, Request{URL: "https://x.slack.com/api/a.b", Token: "xoxp-1", Bearer: true, Form: url.Values{"a": {"1"}}})
		require.NoError(t, err)
		assert.Equal(t, "a=1", gotBody)
		assert.Equal(t, "Bearer xoxp-1", got.Header.Get("Authorization"))
	})

	t.Run("json body with a header override", func(t *testing.T) {
		_, err := Post(context.Background(), client, Request{URL: "https://x.slack.com/api/a.b", JSON: []byte(`{"a":1}`), Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}})
		require.NoError(t, err)
		assert.Equal(t, `{"a":1}`, gotBody)
		assert.Equal(t, "application/json; charset=utf-8", got.Header.Get("Content-Type"))
	})
}

func TestPostStatusErrors(t *testing.T) {
	t.Run("429 is a RateLimitedError", func(t *testing.T) {
		client := doFunc(func(*http.Request) (*http.Response, error) {
			return respond(http.StatusTooManyRequests, "slow down", http.Header{"Retry-After": {"7"}}), nil
		})
		_, err := Post(context.Background(), client, Request{URL: "https://x"})
		var limited *slack.RateLimitedError
		require.ErrorAs(t, err, &limited)
		assert.Equal(t, 7*time.Second, limited.RetryAfter)
	})

	t.Run("other non-2xx is a StatusCodeError", func(t *testing.T) {
		client := doFunc(func(*http.Request) (*http.Response, error) {
			return respond(http.StatusForbidden, "no", nil), nil
		})
		_, err := Post(context.Background(), client, Request{URL: "https://x"})
		var status slack.StatusCodeError
		require.ErrorAs(t, err, &status)
		assert.Equal(t, http.StatusForbidden, status.Code)
	})

	t.Run("an oversized body is refused", func(t *testing.T) {
		client := doFunc(func(*http.Request) (*http.Response, error) {
			return respond(http.StatusOK, strings.Repeat("a", MaxResponseBytes+1), nil), nil
		})
		_, err := Post(context.Background(), client, Request{URL: "https://x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds")
	})
}

func TestDecode(t *testing.T) {
	var out struct {
		Value string `json:"value"`
	}
	require.NoError(t, Decode([]byte(`{"ok":true,"value":"v"}`), &out))
	assert.Equal(t, "v", out.Value)
	require.NoError(t, Decode([]byte(`{"ok":true}`), nil))

	err := Decode([]byte(`{"ok":false,"error":"invalid_auth","response_metadata":{"messages":["m1"]}}`), &out)
	var slackErr slack.SlackErrorResponse
	require.ErrorAs(t, err, &slackErr)
	assert.Equal(t, "invalid_auth", slackErr.Err)
	assert.Equal(t, []string{"m1"}, slackErr.ResponseMetadata.Messages)

	err = Decode([]byte(`{"ok":false}`), nil)
	require.ErrorAs(t, err, &slackErr, "ok:false without an error code is still an error")

	assert.Error(t, Decode([]byte(`not json`), nil))
}

func TestRateLimited(t *testing.T) {
	assert.Nil(t, RateLimited(&http.Response{StatusCode: http.StatusOK}))

	withHeader := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{" 7 "}}}
	limited := RateLimited(withHeader)
	require.NotNil(t, limited)
	assert.Equal(t, 7*time.Second, limited.RetryAfter)

	for _, raw := range []string{"", "soon", "-1"} {
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{raw}}}
		limited := RateLimited(resp)
		require.NotNil(t, limited, raw)
		assert.Equal(t, DefaultRetryAfter, limited.RetryAfter, raw)
	}
}

func TestSlackDomain(t *testing.T) {
	t.Setenv("SLACK_MCP_GOVSLACK", "")
	assert.Equal(t, "slack.com", SlackDomain())
	assert.Equal(t, "https://slack.com/api/", SlackAPIBase())

	t.Setenv("SLACK_MCP_GOVSLACK", "1")
	assert.Equal(t, "slack-gov.com", SlackDomain())
	assert.Equal(t, "https://slack-gov.com/api/", SlackAPIBase())
}
