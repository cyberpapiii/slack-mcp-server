// Package slackhttp is the one way this server POSTs to Slack outside the
// slack-go client: it builds the request, maps a 429 to
// *slack.RateLimitedError and any other non-2xx status to
// slack.StatusCodeError, caps the response body, and decodes Slack's
// {ok,error,response_metadata} envelope into slack.SlackErrorResponse.
// It never retries; callers that want a retry use limiter.CallWithRetry.
package slackhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"github.com/slack-go/slack"
)

// MaxResponseBytes caps every response body read here. client.userBoot on a
// large workspace runs to several megabytes, so the cap sits well above it.
const MaxResponseBytes = 64 << 20

// DefaultRetryAfter covers 429 responses whose Retry-After header is missing
// or unparsable, so callers still see a retryable RateLimitedError.
const DefaultRetryAfter = 5 * time.Second

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Request is one POST to Slack. Exactly one of Form or JSON is the body.
type Request struct {
	URL string
	// Token goes into the "token" form field (unless Form already has one),
	// or into an Authorization: Bearer header when Bearer is set. Empty sends
	// no token; a JSON body then carries its own.
	Token  string
	Bearer bool
	Form   url.Values
	JSON   []byte
	// Header is applied after the defaults, so it may override Content-Type.
	Header http.Header
}

// Post sends req and returns the body of a 2xx response.
func Post(ctx context.Context, client Doer, req Request) ([]byte, error) {
	httpReq, err := newRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if limited := RateLimited(resp); limited != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBytes))
		return nil, limited
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBytes))
		return nil, slack.StatusCodeError{Code: resp.StatusCode, Status: resp.Status}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResponseBytes {
		return nil, fmt.Errorf("slack response exceeds %d bytes", MaxResponseBytes)
	}
	return body, nil
}

func newRequest(ctx context.Context, req Request) (*http.Request, error) {
	var body io.Reader
	contentType := ""
	switch {
	case req.JSON != nil:
		body, contentType = bytes.NewReader(req.JSON), "application/json"
	default:
		form := req.Form
		if form == nil {
			form = url.Values{}
		}
		if req.Token != "" && !req.Bearer && form["token"] == nil {
			form.Set("token", req.Token)
		}
		body, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	if req.Bearer {
		httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	}
	for key, values := range req.Header {
		httpReq.Header[key] = values
	}
	return httpReq, nil
}

type envelope struct {
	Ok               bool                   `json:"ok"`
	Error            string                 `json:"error"`
	ResponseMetadata slack.ResponseMetadata `json:"response_metadata"`
	Needed           string                 `json:"needed,omitempty"`
	Provided         string                 `json:"provided,omitempty"`
}

// Decode checks body's envelope and, when it is ok, unmarshals body into v
// (skipped when v is nil). "ok":false becomes slack.SlackErrorResponse, even
// when Slack names no error.
func Decode(body []byte, v any) error {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return err
	}
	if !env.Ok {
		return slack.SlackErrorResponse{Err: env.Error, ResponseMetadata: env.ResponseMetadata, Needed: env.Needed, Provided: env.Provided}
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(body, v)
}

func Call(ctx context.Context, client Doer, req Request, v any) error {
	body, err := Post(ctx, client, req)
	if err != nil {
		return err
	}
	return Decode(body, v)
}

// RateLimited converts a 429 response into slack.RateLimitedError carrying
// the server's Retry-After; nil for every other status.
func RateLimited(resp *http.Response) *slack.RateLimitedError {
	if resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil || secs < 0 {
		return &slack.RateLimitedError{RetryAfter: DefaultRetryAfter}
	}
	return &slack.RateLimitedError{RetryAfter: time.Duration(secs) * time.Second}
}

// SlackDomain is the Slack host the whole process talks to;
// SLACK_MCP_GOVSLACK switches every Web API, edge and OAuth call to GovSlack.
// The variable is read once, on first use.
func SlackDomain() string {
	on := govSlackFromEnv()
	if override := govSlackOverride.Load(); override != nil {
		on = *override
	}
	if on {
		return "slack-gov.com"
	}
	return "slack.com"
}

var govSlackFromEnv = sync.OnceValue(func() bool { return envutil.IsTruthy(os.Getenv("SLACK_MCP_GOVSLACK")) })

var govSlackOverride atomic.Pointer[bool]

// SetGovSlack overrides SLACK_MCP_GOVSLACK until restore is called, for
// tests.
func SetGovSlack(on bool) (restore func()) {
	old := govSlackOverride.Swap(&on)
	return func() { govSlackOverride.Store(old) }
}

// WorkspaceAPIBase is the Web API root on a workspace's own host, the URL
// auth.test returned.
func WorkspaceAPIBase(workspaceURL string) string {
	return strings.TrimRight(workspaceURL, "/") + "/api/"
}

// SlackAPIBase is the Web API root for direct HTTP calls and for slack.Client
// construction before the workspace URL is known.
func SlackAPIBase() string {
	return "https://" + SlackDomain() + "/api/"
}
