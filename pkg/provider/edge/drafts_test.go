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

func TestDraftBrowserEndpointsUseVerifiedForms(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		call   func(*Client) error
		assert func(*testing.T, url.Values)
	}{
		{
			name: "create", path: "/api/drafts.create", body: `{"ok":true,"draft":{"id":"Dr1","last_updated_ts":"1.0000000"}}`,
			call: func(c *Client) error { _, err := c.DraftCreate(context.Background(), "C1", "", "hello"); return err },
			assert: func(t *testing.T, form url.Values) {
				if form.Get("is_from_composer") != "true" || form.Get("file_ids") != "[]" || form.Get("client_msg_id") == "" {
					t.Fatalf("incomplete create form: %v", form)
				}
				assertDraftJSON(t, form, "destinations", "channel_id", "C1")
				assertDraftJSON(t, form, "blocks", "type", "rich_text")
			},
		},
		{
			name: "update", path: "/api/drafts.update", body: `{"ok":true,"draft":{"id":"Dr1"}}`,
			call: func(c *Client) error {
				_, err := c.DraftUpdate(context.Background(), "Dr1", "123.4", "C1", "9.000001", "edited")
				return err
			},
			assert: func(t *testing.T, form url.Values) {
				if form.Get("draft_id") != "Dr1" || form.Get("client_last_updated_ts") != "123.4000000" {
					t.Fatalf("wrong update binding: %v", form)
				}
				assertDraftJSON(t, form, "destinations", "thread_ts", "9.000001")
			},
		},
		{
			name: "delete", path: "/api/drafts.delete", body: `{"ok":true}`,
			call: func(c *Client) error { return c.DraftDelete(context.Background(), "Dr1", "123.4") },
			assert: func(t *testing.T, form url.Values) {
				if form.Get("draft_id") != "Dr1" || form.Get("client_last_updated_ts") != "123.4000000" {
					t.Fatalf("wrong delete binding: %v", form)
				}
			},
		},
		{
			name: "list", path: "/api/drafts.list", body: `{"ok":true,"drafts":[{"id":"Dr1"}],"next_ts":"next"}`,
			call: func(c *Client) error {
				drafts, next, err := c.DraftsList(context.Background(), 25, true, "cursor")
				if err == nil && len(drafts) != 1 {
					t.Fatalf("wrong drafts: %#v", drafts)
				}
				if err == nil && next != "next" {
					t.Fatalf("next = %q", next)
				}
				return err
			},
			assert: func(t *testing.T, form url.Values) {
				if form.Get("limit") != "25" || form.Get("is_active") != "true" || form.Get("next_ts") != "cursor" {
					t.Fatalf("wrong list form: %v", form)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := newFixtureClient(t, func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodPost || request.URL.Path != tc.path {
					t.Fatalf("request = %s %s", request.Method, request.URL.Path)
				}
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				form, err := url.ParseQuery(string(body))
				if err != nil {
					return nil, err
				}
				if form.Get("token") != "xoxc-test" || form.Get("_x_mode") == "" || form.Get("_x_reason") == "" {
					t.Fatalf("missing browser fields: %v", form)
				}
				tc.assert(t, form)
				return jsonResponse(http.StatusOK, tc.body), nil
			})
			if err := tc.call(client); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertDraftJSON(t *testing.T, form url.Values, field, key, want string) {
	t.Helper()
	var values []map[string]any
	if err := json.Unmarshal([]byte(form.Get(field)), &values); err != nil {
		t.Fatalf("%s JSON: %v", field, err)
	}
	if len(values) == 0 {
		t.Fatalf("%s empty", field)
	}
	if got, _ := values[0][key].(string); got != want {
		t.Fatalf("%s.%s = %q, want %q; raw=%s", field, key, got, want, strings.TrimSpace(form.Get(field)))
	}
}
