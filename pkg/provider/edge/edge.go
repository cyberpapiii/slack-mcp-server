// Package edge provides a limited implementation of undocumented Slack Edge
// API necessary to get the data from a slack workspace.
//
// Each call waits on its method's bucket in the client's pacing table, and a
// rate-limited call is retried under limiter.Retries, after Slack's
// Retry-After.
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
	pacing       limiter.Pacing
}

// NewWithInfo builds a client for the workspace info describes without
// calling auth.test; the caller must ensure that the token is valid.
// httpClient carries the proxy and TLS settings; pacing is
// limiter.SlackPacing() outside tests.
func NewWithInfo(info *slack.AuthTestResponse, token string, httpClient slackhttp.Doer, logger *zap.Logger, pacing limiter.Pacing) *Client {
	return &Client{
		cl:           httpClient,
		token:        token,
		webclientAPI: slackhttp.WorkspaceAPIBase(info.URL),
		edgeAPI:      fmt.Sprintf("https://edgeapi.%s/cache/%s/", slackhttp.SlackDomain(), info.TeamID),
		logger:       logger,
		pacing:       pacing,
	}
}

const acceptLanguage = "en-NZ,en-AU;q=0.9,en;q=0.8"

// postJSON posts req as JSON to the edge API and decodes the response into v.
// The edge API takes the token in the body, so req carries it.
func (cl *Client) postJSON(ctx context.Context, path string, req any, v any) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return cl.call(ctx, path, slackhttp.Request{URL: cl.edgeAPI + path, JSON: data}, v)
}

// postForm posts form, a struct with json tags, to a webclient API method and
// decodes the response into v. The token joins the form there.
func (cl *Client) postForm(ctx context.Context, method string, form any, v any) error {
	return cl.call(ctx, method, slackhttp.Request{URL: cl.webclientAPI + method, Token: cl.token, Form: values(form)}, v)
}

func (cl *Client) call(ctx context.Context, method string, req slackhttp.Request, v any) error {
	req.Header = http.Header{"Accept-Language": {acceptLanguage}}
	_, err := limiter.Paced(ctx, cl.pacing, method, cl.logger.With(zap.String("url", req.URL)), func() (struct{}, error) {
		return struct{}{}, slackhttp.Call(ctx, cl.cl, req, v)
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

// Slack does not seek search.modules.channels past page 100: a request for a
// later page returns page 1.
type pagination struct {
	NextCursor string `json:"next_cursor,omitempty"`
}
