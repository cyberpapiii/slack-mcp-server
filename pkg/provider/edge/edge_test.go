package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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

// ---------------------------------------------------------------------------
// Fixture-driven tests for the constructor, response parsing and the three
// endpoints the fork's tools depend on.
//
// These build their clients through NewWithClient, which is both the cheap
// seam (plain strings + *http.Client, no auth.Provider) and the constructor
// that used to create tape.txt in the working directory and drop its options.
// ---------------------------------------------------------------------------

// roundTripperFunc adapts a plain function to http.RoundTripper so a real
// *http.Client can be handed to NewWithClient.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// memTape is an in-memory io.WriteCloser, used to prove WithTape is applied.
type memTape struct {
	buf    strings.Builder
	closed int
}

func (m *memTape) Write(p []byte) (int, error) { return m.buf.Write(p) }
func (m *memTape) Close() error                { m.closed++; return nil }

// jsonResponse builds a canned response with the given status and body.
func jsonResponse(status int, body string) *http.Response {
	h := make(http.Header)
	h.Set(hdrContentType, "application/json")
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newFixtureClient builds a Client whose transport is fake, via NewWithClient.
func newFixtureClient(t *testing.T, fake roundTripperFunc) *Client {
	t.Helper()
	cl, err := NewWithClient("testws", "T123", "xoxc-test", &http.Client{Transport: fake})
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	return cl
}

// TestUnitNewWithClient pins the constructor's contract: no tape file on disk,
// a no-op tape by default, and options actually applied.
func TestUnitNewWithClient(t *testing.T) {
	t.Run("defaults to a no-op tape and writes no file", func(t *testing.T) {
		// Run in a scratch directory so a regression would be visible here
		// rather than polluting the package directory.
		t.Chdir(t.TempDir())

		cl, err := NewWithClient("testws", "T123", "xoxc-test", &http.Client{})
		if err != nil {
			t.Fatalf("NewWithClient: %v", err)
		}
		if _, ok := cl.tape.(nopTape); !ok {
			t.Fatalf("expected the default tape to be nopTape, got %T", cl.tape)
		}
		if _, err := os.Stat("tape.txt"); !os.IsNotExist(err) {
			t.Fatalf("NewWithClient created tape.txt (stat err = %v)", err)
		}
		if cl.webclientAPI != "https://testws.slack.com/api/" {
			t.Fatalf("unexpected webclientAPI: %q", cl.webclientAPI)
		}
		if cl.edgeAPI != "https://edgeapi.slack.com/cache/T123/" {
			t.Fatalf("unexpected edgeAPI: %q", cl.edgeAPI)
		}
	})

	t.Run("applies options", func(t *testing.T) {
		tape := &memTape{}
		fn := doFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("not called")
		})

		cl, err := NewWithClient("testws", "T123", "xoxc-test", &http.Client{},
			WithTape(tape), OptionHTTPClient(fn))
		if err != nil {
			t.Fatalf("NewWithClient: %v", err)
		}
		if cl.tape != io.WriteCloser(tape) {
			t.Fatalf("WithTape was not applied, tape is %T", cl.tape)
		}
		if _, ok := cl.cl.(doFunc); !ok {
			t.Fatalf("OptionHTTPClient was not applied, client is %T", cl.cl)
		}
	})

	t.Run("rejects empty teamID and token", func(t *testing.T) {
		if _, err := NewWithClient("testws", "", "xoxc-test", &http.Client{}); !errors.Is(err, ErrNoTeamID) {
			t.Fatalf("expected ErrNoTeamID, got %v", err)
		}
		if _, err := NewWithClient("testws", "T123", "", &http.Client{}); !errors.Is(err, ErrNoToken) {
			t.Fatalf("expected ErrNoToken, got %v", err)
		}
	})
}

// TestUnitParseResponse tables the decode contract.  ParseResponse itself only
// guards the status and unmarshals; turning `"ok":false` into an error is
// baseResponse.validate's job and every endpoint calls the pair, so the table
// runs the pair too.  The nil-response row lives in
// TestUnitEdgeParseResponseNil.
func TestUnitParseResponse(t *testing.T) {
	cl := newFixtureClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("ParseResponse must not make requests")
	})

	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string // substring; empty means no error expected
		wantOk  bool
	}{
		{
			name:   "ok true decodes cleanly",
			status: http.StatusOK,
			body:   `{"ok":true,"response_metadata":{"next_cursor":"c2"}}`,
			wantOk: true,
		},
		{
			name:    "ok false surfaces the api error",
			status:  http.StatusOK,
			body:    `{"ok":false,"error":"invalid_auth"}`,
			wantErr: "invalid_auth",
		},
		{
			name:    "non-2xx status is an error",
			status:  http.StatusInternalServerError,
			body:    `{"ok":true}`,
			wantErr: "status code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r baseResponse
			err := cl.ParseResponse(&r, jsonResponse(tt.status, tt.body))
			if err == nil {
				err = r.validate("test.endpoint")
			}

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if r.Ok != tt.wantOk {
					t.Fatalf("ok = %v, want %v", r.Ok, tt.wantOk)
				}
				if r.ResponseMetadata.NextCursor != "c2" {
					t.Fatalf("next_cursor = %q, want %q", r.ResponseMetadata.NextCursor, "c2")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

const clientCountsFixture = `{
  "ok": true,
  "channels": [
    {"id":"C111","last_read":"1710000000.000100","latest":"1710000100.000200","mention_count":2,"has_unreads":true},
    {"id":"C222","last_read":"1710000200.000300","latest":"1710000200.000300","mention_count":0,"has_unreads":false}
  ],
  "mpims": [
    {"id":"G111","last_read":"1710000300.000400","latest":"1710000400.000500","mention_count":1,"has_unreads":true}
  ],
  "ims": [
    {"id":"D111","last_read":"1710000500.000600","latest":"1710000600.000700","mention_count":3,"has_unreads":true}
  ]
}`

// TestUnitClientCounts decodes a client.counts fixture and, in the same test,
// pins the request-building contract (endpoint path, form token, reason).
func TestUnitClientCounts(t *testing.T) {
	var (
		gotURL         string
		gotContentType string
		gotForm        url.Values
	)

	cl := newFixtureClient(t, func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotContentType = r.Header.Get(hdrContentType)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		gotForm, err = url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusOK, clientCountsFixture), nil
	})

	resp, err := cl.ClientCounts(context.Background())
	if err != nil {
		t.Fatalf("ClientCounts: %v", err)
	}

	// Request contract.
	if want := "https://testws.slack.com/api/client.counts"; gotURL != want {
		t.Errorf("request URL = %q, want %q", gotURL, want)
	}
	if want := "application/x-www-form-urlencoded"; gotContentType != want {
		t.Errorf("Content-Type = %q, want %q", gotContentType, want)
	}
	if got := gotForm.Get("token"); got != "xoxc-test" {
		t.Errorf("form token = %q, want %q", got, "xoxc-test")
	}
	if got := gotForm.Get("thread_counts_by_channel"); got != "true" {
		t.Errorf("form thread_counts_by_channel = %q, want %q", got, "true")
	}
	if got := gotForm.Get("_x_reason"); got != "client-counts-api/fetchClientCounts" {
		t.Errorf("form _x_reason = %q, want %q", got, "client-counts-api/fetchClientCounts")
	}

	// Decoded shape.
	if len(resp.Channels) != 2 || len(resp.MPIMs) != 1 || len(resp.IMs) != 1 {
		t.Fatalf("counts: channels=%d mpims=%d ims=%d", len(resp.Channels), len(resp.MPIMs), len(resp.IMs))
	}
	c0 := resp.Channels[0]
	if c0.ID != "C111" {
		t.Errorf("channels[0].id = %q, want %q", c0.ID, "C111")
	}
	if got := c0.LastRead.SlackString(); got != "1710000000.000100" {
		t.Errorf("channels[0].last_read = %q, want %q", got, "1710000000.000100")
	}
	if got := c0.Latest.SlackString(); got != "1710000100.000200" {
		t.Errorf("channels[0].latest = %q, want %q", got, "1710000100.000200")
	}
	if c0.MentionCount != 2 || !c0.HasUnreads {
		t.Errorf("channels[0]: mention_count=%d has_unreads=%v", c0.MentionCount, c0.HasUnreads)
	}
	if resp.Channels[1].MentionCount != 0 || resp.Channels[1].HasUnreads {
		t.Errorf("channels[1] should be read with no mentions, got %+v", resp.Channels[1])
	}
	if resp.MPIMs[0].ID != "G111" || resp.IMs[0].ID != "D111" {
		t.Errorf("mpims[0]=%q ims[0]=%q", resp.MPIMs[0].ID, resp.IMs[0].ID)
	}
	if resp.IMs[0].MentionCount != 3 {
		t.Errorf("ims[0].mention_count = %d, want 3", resp.IMs[0].MentionCount)
	}
}

// TestUnitClientCountsAPIError checks that a well-formed 200 carrying
// `"ok":false` is reported as an error rather than an empty result.
func TestUnitClientCountsAPIError(t *testing.T) {
	cl := newFixtureClient(t, func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"ok":false,"error":"not_allowed_token_type"}`), nil
	})

	if _, err := cl.ClientCounts(context.Background()); err == nil {
		t.Fatal("expected an error for ok:false, got nil")
	} else if !strings.Contains(err.Error(), "not_allowed_token_type") {
		t.Fatalf("error %q does not mention the API error", err)
	}
}

const savedListFixture = `{
  "ok": true,
  "saved_items": [
    {"item_id":"Sv111","item_type":"message","date_created":1710000000,"date_due":1710003600,
     "date_completed":0,"date_updated":1710000001,"is_archived":false,
     "date_snoozed_until":0,"ts":"1710000000.000100","state":"uncompleted"},
    {"item_id":"Sv222","item_type":"message","date_created":1709000000,"date_due":0,
     "date_completed":1709000500,"date_updated":1709000500,"is_archived":true,
     "date_snoozed_until":0,"ts":"1709000000.000200","state":"completed"}
  ],
  "counts": {"uncompleted_count":1,"uncompleted_overdue_count":0,
             "archived_count":1,"completed_count":1,"total_count":2}
}`

func TestUnitSavedList(t *testing.T) {
	var gotURL string
	var gotForm url.Values

	cl := newFixtureClient(t, func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		gotForm, err = url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusOK, savedListFixture), nil
	})

	resp, err := cl.SavedList(context.Background(), "uncompleted", 50, "cur1")
	if err != nil {
		t.Fatalf("SavedList: %v", err)
	}

	if want := "https://testws.slack.com/api/saved.list"; gotURL != want {
		t.Errorf("request URL = %q, want %q", gotURL, want)
	}
	if got := gotForm.Get("filter"); got != "uncompleted" {
		t.Errorf("form filter = %q, want %q", got, "uncompleted")
	}
	if got := gotForm.Get("limit"); got != "50" {
		t.Errorf("form limit = %q, want %q", got, "50")
	}
	if got := gotForm.Get("cursor"); got != "cur1" {
		t.Errorf("form cursor = %q, want %q", got, "cur1")
	}

	if len(resp.SavedItems) != 2 {
		t.Fatalf("saved_items: got %d, want 2", len(resp.SavedItems))
	}
	first := resp.SavedItems[0]
	if first.ItemID != "Sv111" || first.ItemType != "message" || first.State != "uncompleted" {
		t.Errorf("saved_items[0] = %+v", first)
	}
	if first.DateCreated != 1710000000 || first.DateDue != 1710003600 || first.DateCompleted != 0 {
		t.Errorf("saved_items[0] dates = %+v", first)
	}
	if first.IsArchived {
		t.Error("saved_items[0] should not be archived")
	}
	if first.Ts != "1710000000.000100" {
		t.Errorf("saved_items[0].ts = %q", first.Ts)
	}
	second := resp.SavedItems[1]
	if !second.IsArchived || second.State != "completed" || second.DateCompleted != 1709000500 {
		t.Errorf("saved_items[1] = %+v", second)
	}
	if resp.Counts != (SavedCounts{
		UncompletedCount:        1,
		UncompletedOverdueCount: 0,
		ArchivedCount:           1,
		CompletedCount:          1,
		TotalCount:              2,
	}) {
		t.Errorf("counts = %+v", resp.Counts)
	}
}

const activityFeedFixture = `{
  "ok": true,
  "items": [
    {"is_unread":true,"feed_ts":"1710000000.000100","key":"at_user:C111:1710000000.000100",
     "item":{"type":"at_user","message":{"ts":"1710000000.000100","channel":"C111",
             "thread_ts":"","author_user_id":"U111","is_broadcast":false}}},
    {"is_unread":false,"feed_ts":"1710000100.000200","key":"thread_v2:C222:1709999000.000000",
     "item":{"type":"thread_v2","bundle_info":{"payload":{"thread_entry":{
             "channel_id":"C222","thread_ts":"1709999000.000000",
             "latest_ts":"1710000100.000200","unread_msg_count":3,
             "min_unread_ts":"1710000050.000000"}}}}}
  ]
}`

func TestUnitActivityFeed(t *testing.T) {
	var gotURL string
	var gotForm url.Values

	cl := newFixtureClient(t, func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		gotForm, err = url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusOK, activityFeedFixture), nil
	})

	resp, err := cl.ActivityFeed(context.Background(), 25)
	if err != nil {
		t.Fatalf("ActivityFeed: %v", err)
	}

	if want := "https://testws.slack.com/api/activity.feed"; gotURL != want {
		t.Errorf("request URL = %q, want %q", gotURL, want)
	}
	if got := gotForm.Get("limit"); got != "25" {
		t.Errorf("form limit = %q, want %q", got, "25")
	}
	if got := gotForm.Get("mode"); got != "priority_unreads_v1" {
		t.Errorf("form mode = %q, want %q", got, "priority_unreads_v1")
	}

	if len(resp.Items) != 2 {
		t.Fatalf("items: got %d, want 2", len(resp.Items))
	}

	mention := resp.Items[0]
	if !mention.IsUnread || mention.FeedTs != "1710000000.000100" {
		t.Errorf("items[0] = %+v", mention)
	}
	if mention.Item.Type != "at_user" {
		t.Errorf("items[0].item.type = %q, want at_user", mention.Item.Type)
	}
	if mention.Item.Message == nil {
		t.Fatal("items[0].item.message is nil")
	}
	if mention.Item.Message.Channel != "C111" || mention.Item.Message.AuthorUserID != "U111" {
		t.Errorf("items[0].item.message = %+v", *mention.Item.Message)
	}
	if mention.Item.BundleInfo != nil {
		t.Error("items[0] should have no bundle_info")
	}

	thread := resp.Items[1]
	if thread.IsUnread {
		t.Error("items[1] should be read")
	}
	if thread.Item.BundleInfo == nil {
		t.Fatal("items[1].item.bundle_info is nil")
	}
	entry := thread.Item.BundleInfo.Payload.ThreadEntry
	if entry.ChannelID != "C222" || entry.ThreadTs != "1709999000.000000" || entry.UnreadMsgCount != 3 {
		t.Errorf("items[1] thread_entry = %+v", entry)
	}
	if thread.Item.Message != nil {
		t.Error("items[1] should have no message")
	}
}

// TestUnitUsersListConcurrentBuckets calls UsersList with one channel ID and
// one DM ID so splitDMs fills both buckets and both errgroup goroutines run
// at once.  Under -race that exercises the path where the old shared-slice
// append in UsersList could be flagged; it also pins that the two result
// sets are joined in public-then-DM order.
func TestUnitUsersListConcurrentBuckets(t *testing.T) {
	cl := newFixtureClient(t, func(r *http.Request) (*http.Response, error) {
		t.Logf("fake got: %s", r.URL.String())
		switch {
		case strings.Contains(r.URL.Path, "users/list"):
			return jsonResponse(http.StatusOK,
				`{"ok":true,"results":[{"id":"U-PUB","name":"pub-user"}],"next_marker":""}`), nil
		case strings.Contains(r.URL.Path, "conversations.view"):
			return jsonResponse(http.StatusOK,
				`{"ok":true,"users":[{"id":"U-DM","name":"dm-user"}]}`), nil
		default:
			t.Errorf("unexpected request URL: %s", r.URL)
			return jsonResponse(http.StatusOK, `{"ok":true}`), nil
		}
	})

	uu, err := cl.UsersList(context.Background(), "C111", "D222")
	if err != nil {
		t.Fatalf("UsersList: %v", err)
	}
	if len(uu) != 2 {
		t.Fatalf("got %d users, want 2: %+v", len(uu), uu)
	}
	// Public-channel users come first, DM users after — the order the old
	// sequential-in-practice code produced.
	if uu[0].ID != "U-PUB" || uu[1].ID != "U-DM" {
		t.Errorf("user IDs = %q, %q; want U-PUB then U-DM", uu[0].ID, uu[1].ID)
	}
}
