package edge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type doFunc func(*http.Request) (*http.Response, error)

func (f doFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

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
	return NewWithInfo(&slack.AuthTestResponse{TeamID: "T123", URL: "https://testws.slack.com/"}, "xoxc-test", fn, zap.NewNop(), nil)
}

// TestUnitEdgeTransportErrorNilResponse covers the crash path: the transport
// returns (nil, io.EOF-wrapped error) when the server closes a pooled
// keep-alive connection, and the call must surface that error.
func TestUnitEdgeTransportErrorNilResponse(t *testing.T) {
	cl := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: io.EOF}
	})

	_, err := cl.UsersSearch(context.Background(), "q", 1)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected the transport error to be wrapped, got %v", err)
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
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"results":[{"id":"U1"}]}`)),
		}, nil
	})

	users, err := cl.UsersSearch(context.Background(), "q", 1)
	if err != nil {
		t.Fatalf("UsersSearch: %v", err)
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
	if len(users) != 1 || users[0].ID != "U1" {
		t.Fatalf("expected the successful response to be decoded, got %+v", users)
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
		_, err := cl.UsersSearch(ctx, "q", 1)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UsersSearch did not return within 1s; the retry wait ignored cancellation")
	}

	if calls != 1 {
		t.Fatalf("expected exactly 1 call before cancellation, got %d", calls)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newFixtureClient(t *testing.T, fake roundTripperFunc) *Client {
	t.Helper()
	return NewWithInfo(&slack.AuthTestResponse{TeamID: "T123", URL: "https://testws.slack.com/"}, "xoxc-test", &http.Client{Transport: fake}, zap.NewNop(), nil)
}

// The API bases derive from the auth response: webclient methods go to the
// workspace host, edge methods to the team's edge cache.
func TestUnitNewWithInfoDerivesAPIBases(t *testing.T) {
	var urls []string
	cl := newFixtureClient(t, func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.String())
		return jsonResponse(http.StatusOK, `{"ok":true}`), nil
	})
	_, _ = cl.ClientCounts(context.Background())
	_, _ = cl.UsersSearch(context.Background(), "q", 1)

	want := []string{"https://testws.slack.com/api/client.counts", "https://edgeapi.slack.com/cache/T123/users/search"}
	if strings.Join(urls, " ") != strings.Join(want, " ") {
		t.Fatalf("request URLs = %v, want %v", urls, want)
	}
}

// TestUnitWebclientDecode tables the decode contract every webclient call
// shares: "ok":false is a slack.SlackErrorResponse carrying Slack's code and
// first message, and a non-2xx status is a slack.StatusCodeError.
func TestUnitWebclientDecode(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantErr  string
		wantCode string
	}{
		{
			name:     "ok false surfaces the api error",
			status:   http.StatusOK,
			body:     `{"ok":false,"error":"invalid_auth"}`,
			wantErr:  "invalid_auth",
			wantCode: "invalid_auth",
		},
		{
			name:     "ok false keeps Slack's message",
			status:   http.StatusOK,
			body:     `{"ok":false,"error":"invalid_arguments","response_metadata":{"messages":["[ERROR] missing channel"]}}`,
			wantErr:  "invalid_arguments: [ERROR] missing channel",
			wantCode: "invalid_arguments",
		},
		{
			name:    "non-2xx status is an error",
			status:  http.StatusInternalServerError,
			body:    `{"ok":true}`,
			wantErr: "500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := newFixtureClient(t, func(*http.Request) (*http.Response, error) {
				return jsonResponse(tt.status, tt.body), nil
			})
			_, err := cl.ClientCounts(context.Background())
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
			var slackErr slack.SlackErrorResponse
			if tt.wantCode != "" && (!errors.As(err, &slackErr) || slackErr.Err != tt.wantCode) {
				t.Fatalf("error %v does not carry Slack code %q", err, tt.wantCode)
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
		gotContentType = r.Header.Get("Content-Type")
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

func TestUnitSavedWriteForms(t *testing.T) {
	var gotURL string
	var gotForm url.Values
	body := `{"ok":true,"item":{"item_id":"Sa1","item_type":"reminder","todo_state":"to_do"}}`
	cl := newFixtureClient(t, func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		gotForm, err = url.ParseQuery(string(raw))
		if err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusOK, body), nil
	})

	if err := cl.SavedUpdate(context.Background(), SavedUpdateParams{ItemType: SavedItemMessage, ItemID: "C1", Ts: "1.0", DateDue: "0"}); err != nil {
		t.Fatalf("SavedUpdate: %v", err)
	}
	if !strings.HasSuffix(gotURL, "/api/saved.update") {
		t.Errorf("request URL = %q", gotURL)
	}
	if got := gotForm.Get("date_due"); got != "0" {
		t.Errorf("date_due = %q, want 0: clearing a reminder must reach Slack", got)
	}
	if gotForm.Has("todo_state") || gotForm.Has("mark") {
		t.Errorf("unset fields were sent: %v", gotForm)
	}

	item, err := cl.SavedAdd(context.Background(), SavedAddParams{ItemType: SavedItemReminder, Description: "[]", TodoState: "to_do"})
	if err != nil {
		t.Fatalf("SavedAdd: %v", err)
	}
	if !strings.HasSuffix(gotURL, "/api/saved.add") {
		t.Errorf("request URL = %q", gotURL)
	}
	if item.ItemID != "Sa1" || item.TodoState != "to_do" {
		t.Errorf("item = %+v", item)
	}
	if gotForm.Get("description") != "[]" || gotForm.Get("todo_state") != "to_do" || gotForm.Has("item_id") {
		t.Errorf("saved.add form = %v", gotForm)
	}

	if err := cl.SavedDelete(context.Background(), SavedItemReminder, "Sa1", ""); err != nil {
		t.Fatalf("SavedDelete: %v", err)
	}
	if !strings.HasSuffix(gotURL, "/api/saved.delete") {
		t.Errorf("request URL = %q", gotURL)
	}
	if gotForm.Get("item_type") != "reminder" || gotForm.Get("item_id") != "Sa1" || gotForm.Has("ts") {
		t.Errorf("saved.delete form = %v", gotForm)
	}
}
