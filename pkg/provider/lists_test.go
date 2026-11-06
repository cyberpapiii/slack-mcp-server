package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testListsClient sends every Lists call through httpClient with a test token.
func testListsClient(httpClient *http.Client) *ListsClient {
	client := &MCPSlackClient{httpClient: httpClient}
	client.token.Store("xoxp-test")
	return &ListsClient{web: &WebClient{client: client}}
}

// listsClientFor sends every Lists call to server instead of slack.com.
func listsClientFor(server *httptest.Server) *ListsClient {
	target, _ := url.Parse(server.URL)
	return testListsClient(&http.Client{Transport: listsRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme, request.URL.Host = target.Scheme, target.Host
		return server.Client().Transport.RoundTrip(request)
	})})
}

func TestUnitListsClientExactMethodsAndJSON(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		wantJSON string
		response string
		invoke   func(context.Context, *ListsClient) error
	}{
		{
			name: "create list", method: "slackLists.create",
			wantJSON: `{"name":"Sprint","schema":[{"key":"title","name":"Title","type":"text","is_primary_column":true}]}`,
			response: `{"ok":true,"list_id":"F1","list_metadata":{"schema":[{"id":"Col1","key":"title","name":"Title","type":"text"}]}}`,
			invoke: func(ctx context.Context, client *ListsClient) error {
				id, metadata, err := client.CreateList(ctx, CreateListRequest{Name: "Sprint", Schema: []ListColumn{{Key: "title", Name: "Title", Type: "text", IsPrimaryColumn: true}}})
				assert.Equal(t, "F1", id)
				assert.Equal(t, "F1", metadata.ID)
				return err
			},
		},
		{
			name: "update list", method: "slackLists.update",
			wantJSON: `{"id":"F1","name":"Renamed"}`, response: `{"ok":true}`,
			invoke: func(ctx context.Context, client *ListsClient) error {
				return client.UpdateList(ctx, UpdateListRequest{ID: "F1", Name: "Renamed"})
			},
		},
		{
			name: "create item", method: "slackLists.items.create",
			wantJSON: `{"list_id":"F1","initial_fields":[{"column_id":"Col1","select":["doing"]}]}`,
			response: `{"ok":true,"item":{"id":"Rec1","list_id":"F1","fields":[{"column_id":"Col1","select":["doing"]}]}}`,
			invoke: func(ctx context.Context, client *ListsClient) error {
				item, err := client.CreateItem(ctx, CreateListItemRequest{ListID: "F1", InitialFields: []ListFieldValue{{ColumnID: "Col1", Select: stringValues("doing")}}})
				assert.Equal(t, "Rec1", item.ID)
				require.NotNil(t, item.Fields[0].Select)
				assert.Equal(t, []string{"doing"}, *item.Fields[0].Select)
				return err
			},
		},
		{
			name: "update cells", method: "slackLists.items.update",
			wantJSON: `{"list_id":"F1","cells":[{"column_id":"Col1","row_id":"Rec1","date":["2026-08-09"]}]}`,
			response: `{"ok":true}`,
			invoke: func(ctx context.Context, client *ListsClient) error {
				return client.UpdateItems(ctx, UpdateListItemsRequest{ListID: "F1", Cells: []ListFieldValue{{ColumnID: "Col1", RowID: "Rec1", Date: stringValues("2026-08-09")}}})
			},
		},
		{
			name: "item info", method: "slackLists.items.info",
			wantJSON: `{"id":"Rec1","list_id":"F1"}`,
			response: `{"ok":true,"record":{"id":"Rec1","list_id":"F1","fields":[]}}`,
			invoke: func(ctx context.Context, client *ListsClient) error {
				_, err := client.GetItem(ctx, "F1", "Rec1")
				return err
			},
		},
		{
			name: "delete item", method: "slackLists.items.delete",
			wantJSON: `{"id":"Rec1","list_id":"F1"}`, response: `{"ok":true}`,
			invoke: func(ctx context.Context, client *ListsClient) error { return client.DeleteItem(ctx, "F1", "Rec1") },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				assert.Equal(t, http.MethodPost, request.Method)
				assert.Equal(t, "/api/"+test.method, request.URL.Path)
				assert.Equal(t, "Bearer xoxp-test", request.Header.Get("Authorization"))
				raw, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				assert.JSONEq(t, test.wantJSON, string(raw))
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.response))
			}))
			defer server.Close()
			require.NoError(t, test.invoke(context.Background(), listsClientFor(server)))
		})
	}
}

func TestUnitListsItemsPaginationAndFieldRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		assert.JSONEq(t, `{"list_id":"F1","limit":25,"cursor":"next","archived":false}`, string(raw))
		_, _ = writer.Write([]byte(`{
			"ok":true,
			"items":[{"id":"Rec1","list_id":"F1","updated_timestamp":"123.456","fields":[{"column_id":"ColText","text":"Task","select":["doing"],"user":["U1"],"date":["2026-08-09"]}]}],
			"response_metadata":{"next_cursor":"after"}
		}`))
	}))
	defer server.Close()
	archived := false
	page, err := listsClientFor(server).ListItems(context.Background(), ListItemsRequest{ListID: "F1", Limit: 25, Cursor: "next", Archived: &archived})
	require.NoError(t, err)
	assert.Equal(t, "after", page.NextCursor)
	assert.Equal(t, "Task", page.Items[0].Fields[0].Text)
}

func TestUnitListsClientKeepsSlackErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		response   string
		retryAfter string
		slackCode  string
		wait       time.Duration
	}{
		{"Slack error", 200, `{"ok":false,"error":"plan_upgrade_required"}`, "", "plan_upgrade_required", 0},
		{"rate limit", 429, `{"ok":false,"error":"ratelimited"}`, "7", "", 7 * time.Second},
		{"rate limit without header", 429, `{"ok":false,"error":"ratelimited"}`, "", "", 5 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Retry-After", test.retryAfter)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.response))
			}))
			defer server.Close()
			_, err := listsClientFor(server).ListItems(context.Background(), ListItemsRequest{ListID: "F1"})
			if test.slackCode != "" {
				var slackErr slack.SlackErrorResponse
				require.ErrorAs(t, err, &slackErr)
				assert.Equal(t, test.slackCode, slackErr.Err)
				return
			}
			var limited *slack.RateLimitedError
			require.ErrorAs(t, err, &limited)
			assert.Equal(t, test.wait, limited.RetryAfter)
		})
	}
}

func TestUnitListsRejectUnsupportedFieldTypeBeforeHTTP(t *testing.T) {
	client := testListsClient(nil)
	_, _, err := client.CreateList(context.Background(), CreateListRequest{
		Name: "Bad", Schema: []ListColumn{{Key: "mystery", Name: "Mystery", Type: "quantum"}},
	})
	var unsupported *UnsupportedListFieldTypeError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "quantum", unsupported.Type)
	assert.ErrorIs(t, err, ErrInvalidListInput)

	_, err = client.CreateItem(context.Background(), CreateListItemRequest{ListID: "F1", InitialFields: []ListFieldValue{{}}})
	assert.ErrorIs(t, err, ErrInvalidListInput)
	assert.EqualError(t, err, "Slack List field column_id is required")
}

func TestUnitListsOfficialFixtureCompatibility(t *testing.T) {
	fixture := `{"id":"Rec018ALE9718","list_id":"F1234567","date_created":1758744345,"created_by":"W0AB1CDE2","updated_by":"W0AB1CDE2","updated_timestamp":"1758744346","fields":[{"key":"ready","value":true,"checkbox":[true],"column_id":"ColBool"},{"key":"estimate","value":3,"number":[3],"column_id":"ColNumber"},{"key":"empty","value":null,"column_id":"ColEmpty"},{"key":"website","value":"Slack","link":[{"originalUrl":"https://docs.slack.dev/","attachment":null,"displayAsUrl":false,"displayName":"Slack Developer Docs"}],"column_id":"ColLink"}]}`
	var item ListItem
	require.NoError(t, json.Unmarshal([]byte(fixture), &item))
	raw, err := json.Marshal(item)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"updated_timestamp":"1758744346"`)
	assert.JSONEq(t, `true`, string(item.Fields[0].Value))
	assert.JSONEq(t, `3`, string(item.Fields[1].Value))
	assert.Equal(t, "null", string(item.Fields[2].Value))
	require.NotNil(t, item.Fields[3].Link)
	link := (*item.Fields[3].Link)[0]
	assert.Equal(t, "https://docs.slack.dev/", link.OriginalURL)
	require.NotNil(t, link.DisplayAsURL)
	assert.False(t, *link.DisplayAsURL)
	assert.Equal(t, "Slack Developer Docs", link.DisplayName)
}

func TestUnitListsMutationResponseFailuresReportUnknownOutcome(t *testing.T) {
	tests := []struct {
		name   string
		client *http.Client
		invoke func(*ListsClient) error
	}{
		{
			name: "body read",
			client: &http.Client{Transport: listsRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: readErrorBody{}}, nil
			})},
			invoke: func(client *ListsClient) error {
				return client.UpdateList(context.Background(), UpdateListRequest{ID: "F1", Name: "Renamed"})
			},
		},
		{
			name:   "envelope decode",
			client: fixtureHTTPClient(`{"ok":true`),
			invoke: func(client *ListsClient) error {
				return client.UpdateList(context.Background(), UpdateListRequest{ID: "F1", Name: "Renamed"})
			},
		},
		{
			name:   "result decode",
			client: fixtureHTTPClient(`{"ok":true,"item":{"fields":"invalid"}}`),
			invoke: func(client *ListsClient) error {
				_, err := client.CreateItem(context.Background(), CreateListItemRequest{ListID: "F1"})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testListsClient(test.client)
			err := test.invoke(client)
			assert.ErrorIs(t, err, ErrOutcomeUnknown)
		})
	}
}

type readErrorBody struct{}

func (readErrorBody) Read([]byte) (int, error) { return 0, fmt.Errorf("fixture read failure") }
func (readErrorBody) Close() error             { return nil }

type listsRoundTripFunc func(*http.Request) (*http.Response, error)

func (function listsRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func fixtureHTTPClient(body string) *http.Client {
	return &http.Client{Transport: listsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func stringValues(values ...string) *[]string { return &values }
