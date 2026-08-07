package edge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	rslack "github.com/rusq/slack"
	"github.com/slack-go/slack"
)

// fakeAuthProvider satisfies auth.Provider without any real credentials.
// Note that Test returns the rusq/slack AuthTestResponse, while NewWithInfo
// takes the slack-go one — two different packages, hence the import alias.
type fakeAuthProvider struct{ cl *http.Client }

func (f fakeAuthProvider) SlackToken() string      { return "xoxc-test" }
func (f fakeAuthProvider) Cookies() []*http.Cookie { return nil }
func (f fakeAuthProvider) Validate() error         { return nil }
func (f fakeAuthProvider) Test(context.Context) (*rslack.AuthTestResponse, error) {
	return nil, nil
}
func (f fakeAuthProvider) HTTPClient() (*http.Client, error) { return f.cl, nil }

// doFunc adapts a plain function to the httpClient interface.
type doFunc func(*http.Request) (*http.Response, error)

func (f doFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// closeRecorder is a ReadCloser that records whether it was closed.
type closeRecorder struct {
	io.Reader
	closed int
}

func (c *closeRecorder) Close() error {
	c.closed++
	return nil
}

func newTestClient(t *testing.T, fn doFunc) *Client {
	t.Helper()
	cl, err := NewWithInfo(
		&slack.AuthTestResponse{TeamID: "T123", URL: "https://testws.slack.com/"},
		fakeAuthProvider{cl: &http.Client{}},
		OptionHTTPClient(fn),
	)
	if err != nil {
		t.Fatalf("NewWithInfo: %v", err)
	}
	return cl
}

// TestUnitEdgeCallEdgeAPINilResponse covers the crash path: the transport
// returns (nil, io.EOF-wrapped error) when the server closes a pooled
// keep-alive connection.  callEdgeAPI must not forward the nil response.
func TestUnitEdgeCallEdgeAPINilResponse(t *testing.T) {
	cl := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: io.EOF}
	})

	var out struct {
		Ok bool `json:"ok"`
	}
	err := cl.callEdgeAPI(context.Background(), &out, "test", &BaseRequest{})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected the transport error to be wrapped, got %v", err)
	}
}

func TestUnitEdgeParseResponseNil(t *testing.T) {
	cl := newTestClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("should not be called")
	})

	if err := cl.ParseResponse(&struct{}{}, nil); err == nil {
		t.Fatal("expected an error for a nil response, got nil")
	}
}

// TestUnitEdgeRetryRebuildsBody asserts that the 429 retry re-sends the same
// non-empty body rather than an empty one, and that the rate-limited response
// body is closed.
func TestUnitEdgeRetryRebuildsBody(t *testing.T) {
	rateLimited := &closeRecorder{Reader: strings.NewReader("rate limited")}

	var bodies []string
	calls := 0
	cl := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Body == nil {
			t.Error("request body is nil")
			return nil, errors.New("nil body")
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, string(b))

		calls++
		if calls == 1 {
			h := make(http.Header)
			h.Set("Retry-After", "0")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Header:     h,
				Body:       rateLimited,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})

	var out struct {
		Ok bool `json:"ok"`
	}
	if err := cl.callEdgeAPI(context.Background(), &out, "test", &BaseRequest{}); err != nil {
		t.Fatalf("callEdgeAPI: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 recorded bodies, got %d", len(bodies))
	}
	if bodies[0] == "" {
		t.Fatal("first request body was empty")
	}
	if bodies[1] != bodies[0] {
		t.Fatalf("retry body differs from the original:\nfirst:  %q\nsecond: %q", bodies[0], bodies[1])
	}
	if rateLimited.closed != 1 {
		t.Fatalf("expected the 429 body to be closed exactly once, got %d closes", rateLimited.closed)
	}
	if !out.Ok {
		t.Fatal("expected the successful response to be parsed as ok=true")
	}
}

// TestUnitEdgeRetryContextCancel asserts the retry wait honours context
// cancellation instead of sleeping through it.
func TestUnitEdgeRetryContextCancel(t *testing.T) {
	rateLimited := &closeRecorder{Reader: strings.NewReader("rate limited")}

	calls := 0
	cl := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		h := make(http.Header)
		h.Set("Retry-After", "300")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			Header:     h,
			Body:       rateLimited,
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(10*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() {
		var out struct{}
		done <- cl.callEdgeAPI(ctx, &out, "test", &BaseRequest{})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callEdgeAPI did not return within 1s; the retry wait ignored cancellation")
	}

	if calls != 1 {
		t.Fatalf("expected exactly 1 call before cancellation, got %d", calls)
	}
}
