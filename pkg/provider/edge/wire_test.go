package edge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// wireCall is one edge request as Slack receives it.
type wireCall struct {
	path, contentType, body string
}

// captureWire calls every edge method once and records each request body.
func captureWire(t *testing.T) []wireCall {
	t.Helper()
	var calls []wireCall
	cl := newFixtureClient(t, func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, wireCall{r.URL.Path, r.Header.Get("Content-Type"), string(raw)})
		return jsonResponse(http.StatusOK, `{"ok":true}`), nil
	})
	ctx := context.Background()
	_, _ = cl.ClientUserBoot(ctx)
	_, _ = cl.imList(ctx)
	_, _ = cl.ClientCounts(ctx)
	_, _ = cl.ActivityFeed(ctx, 20)
	_ = cl.ActivityMarkRead(ctx, "thread_v2", "1.2", "k")
	_, _ = cl.conversationsGenericInfo(ctx, "G1")
	_, _ = cl.LeaveConversation(ctx, "C1")
	_, _ = cl.GetMutedChannels(ctx)
	_, _, _ = cl.DraftsList(ctx, 10, true, "")
	_, _ = cl.DraftCreate(ctx, "C1", "", "hello")
	_, _ = cl.DraftUpdate(ctx, "Dr1", "1.2", "C1", "", "hello")
	_ = cl.DraftDelete(ctx, "Dr1", "1.2")
	_, _ = cl.searchChannels(ctx, "gen")
	_, _ = cl.SavedList(ctx, "saved", 10, "")
	_ = cl.SavedUpdate(ctx, SavedUpdateParams{ItemType: "message", ItemID: "C1", Ts: "1.2", Mark: "completed"})
	_, _ = cl.SavedAdd(ctx, SavedAddParams{ItemType: "message", ItemID: "C1", Ts: "1.2"})
	_ = cl.SavedDelete(ctx, "message", "C1", "1.2")
	_ = cl.SavedClearCompleted(ctx)
	_, _ = cl.UsersSearch(ctx, "ann", 5)
	return calls
}

// Every edge request carries the session token exactly once: in the form for
// webclient methods, in the JSON body for the edge API.
func TestUnitEdgeRequestsCarryTheTokenOnce(t *testing.T) {
	calls := captureWire(t)
	if len(calls) != 19 {
		t.Fatalf("captured %d requests, want 19", len(calls))
	}
	for _, c := range calls {
		switch {
		case strings.HasPrefix(c.contentType, "application/json"):
			var body map[string]any
			if err := json.Unmarshal([]byte(c.body), &body); err != nil {
				t.Fatalf("%s: %v", c.path, err)
			}
			if body["token"] != "xoxc-test" {
				t.Errorf("%s: JSON token = %v", c.path, body["token"])
			}
		default:
			form, err := url.ParseQuery(c.body)
			if err != nil {
				t.Fatalf("%s: %v", c.path, err)
			}
			if got := form["token"]; len(got) != 1 || got[0] != "xoxc-test" {
				t.Errorf("%s: form token = %v, want exactly [xoxc-test]", c.path, got)
			}
		}
	}
}
