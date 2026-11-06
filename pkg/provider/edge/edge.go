// Package edge provides a limited implementation of undocumented Slack Edge
// API necessary to get the data from a slack workspace.
//
// A rate-limited call is retried once, after Slack's Retry-After.
package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/slackhttp"
	"github.com/rusq/tagops"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type Client struct {
	cl           slackhttp.Doer
	edgeAPI      string
	webclientAPI string
	token        string
	logger       *zap.Logger
}

type Option func(*Client)

func OptionHTTPClient(client slackhttp.Doer) Option {
	return func(cl *Client) {
		cl.cl = client
	}
}

func OptionLogger(logger *zap.Logger) Option {
	return func(cl *Client) {
		cl.logger = logger
	}
}

// NewWithInfo builds a client for the workspace info describes without
// calling auth.test; the caller must ensure that the token is valid.
func NewWithInfo(info *slack.AuthTestResponse, token string, opt ...Option) *Client {
	c := &Client{
		// Every caller passes OptionHTTPClient below, which is what carries the
		// proxy and TLS settings; this is only a non-nil floor.
		cl:           http.DefaultClient,
		token:        token,
		webclientAPI: info.URL + "api/",
		edgeAPI:      fmt.Sprintf("https://edgeapi.%s/cache/%s/", slackhttp.SlackDomain(), info.TeamID),
		logger:       zap.NewNop(),
	}

	for _, o := range opt {
		o(c)
	}
	return c
}

type BaseRequest struct {
	Token string `json:"token"`
}

func (r *BaseRequest) setToken(token string) {
	r.Token = token
}

func (r *BaseRequest) isTokenSet() bool {
	return len(r.Token) > 0
}

type postRequest interface {
	setToken(string)
	isTokenSet() bool
}

const acceptLanguage = "en-NZ,en-AU;q=0.9,en;q=0.8"

// postJSON posts req as JSON to the edge API and decodes the response into v.
func (cl *Client) postJSON(ctx context.Context, path string, req postRequest, v any) error {
	if !req.isTokenSet() {
		req.setToken(cl.token)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return cl.call(ctx, slackhttp.Request{URL: cl.edgeAPI + path, JSON: data}, v)
}

// postForm posts form, a struct with json tags, to a webclient API method and
// decodes the response into v.
func (cl *Client) postForm(ctx context.Context, method string, form any, v any) error {
	return cl.call(ctx, slackhttp.Request{URL: cl.webclientAPI + method, Token: cl.token, Form: values(form)}, v)
}

func (cl *Client) call(ctx context.Context, req slackhttp.Request, v any) error {
	req.Header = http.Header{"Accept-Language": {acceptLanguage}}
	attempt := 0
	_, err := limiter.CallWithRetry(ctx, nil, 1, func() (struct{}, error) {
		attempt++
		err := slackhttp.Call(ctx, cl.cl, req, v)
		if wait, limited := slackhttp.RetryAfter(err); limited {
			if attempt == 1 {
				cl.logger.Info("Edge call rate limited, waiting", zap.String("url", req.URL), zap.Duration("delay", wait))
			} else {
				cl.logger.Debug("Edge call still rate limited after one retry", zap.String("url", req.URL))
			}
		}
		return struct{}{}, err
	})
	return withSlackMessage(err)
}

// withSlackMessage appends Slack's first response_metadata message to an
// "ok":false error, which names what was wrong with the request.
func withSlackMessage(err error) error {
	var slackErr slack.SlackErrorResponse
	if errors.As(err, &slackErr) && len(slackErr.ResponseMetadata.Messages) > 0 {
		return fmt.Errorf("%w: %s", err, slackErr.ResponseMetadata.Messages[0])
	}
	return err
}

// values returns url.Values from a struct, omitting the empty values of
// fields tagged omitempty.
func values(s any) url.Values {
	var v = make(url.Values)
	m := tagops.ToMap(s, "json", true, true)
	for k, val := range m {
		v.Set(k, fmt.Sprint(val))
	}
	return v
}

type WebClientFields struct {
	XReason  string `json:"_x_reason,omitempty"`
	XMode    string `json:"_x_mode,omitempty"`
	XSonic   bool   `json:"_x_sonic"`
	XAppName string `json:"_x_app_name"`
}

func webclientReason(reason string) WebClientFields {
	return WebClientFields{
		XReason:  reason,
		XMode:    "online",
		XSonic:   true,
		XAppName: "client",
	}
}

// pagination contains the pagination information.  It is truly fucked, Slack
// does not allow to seek past Page 100, when page > 100 requested, Slack
// returns the first page (Page=1).  Seems to be an internal limitation.  The
// workaround would be to use the Query parameter, to be more specific about
// the channel names, but to get all channels, this would require iterating
// through all 65536 runes of unicode give or take the special characters.
//
// For now, this doesn't work as a replacement for conversation.list (202403).
type pagination struct {
	NextCursor string `json:"next_cursor,omitempty"`
}
