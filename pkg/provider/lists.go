package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	transportpkg "github.com/korotovsky/slack-mcp-server/pkg/transport"
)

const slackListsAPIBase = "https://slack.com/api/"

var ErrListEnumerationUnsupported = errors.New("Slack public API does not provide workspace-wide List enumeration")
var ErrListInfoUnsupported = errors.New("Slack public API does not provide List metadata lookup")

type ListsErrorKind string

const (
	ListsErrorUnavailable ListsErrorKind = "paid_plan_unavailable"
	ListsErrorScope       ListsErrorKind = "missing_scope"
	ListsErrorPermission  ListsErrorKind = "permission_denied"
	ListsErrorRateLimit   ListsErrorKind = "rate_limited"
	ListsErrorConflict    ListsErrorKind = "conflict"
	ListsErrorAPI         ListsErrorKind = "slack_api_error"
)

type ListsAPIError struct {
	Kind           ListsErrorKind
	SlackCode      string
	StatusCode     int
	RetryAfter     time.Duration
	MayHaveMutated bool
}

func (e *ListsAPIError) Error() string {
	if e.SlackCode != "" {
		return fmt.Sprintf("Slack Lists %s: %s", e.Kind, e.SlackCode)
	}
	return fmt.Sprintf("Slack Lists %s", e.Kind)
}

func (e *ListsAPIError) Retryable() bool { return e.Kind == ListsErrorRateLimit }

type ListsClient struct {
	HTTPClient    *http.Client
	Token         string
	TokenProvider func() string
	APIBase       string
}

func (ap *ApiProvider) Lists() (*ListsClient, error) {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil || !client.IsOAuth() || client.IsBotToken() {
		return nil, ErrUserOAuthRequired
	}
	return &ListsClient{
		HTTPClient: transportpkg.ProvideHTTPClient(client.authProvider.Cookies(), client.logger),
		TokenProvider: func() string {
			token, _ := client.oauthAccessToken.Load().(string)
			return token
		},
	}, nil
}

type ListChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Color string `json:"color,omitempty"`
}

type ListColumnOptions struct {
	Choices        []ListChoice `json:"choices,omitempty"`
	Format         string       `json:"format,omitempty"`
	Precision      *int         `json:"precision,omitempty"`
	DateFormat     string       `json:"date_format,omitempty"`
	Emoji          string       `json:"emoji,omitempty"`
	EmojiTeamID    string       `json:"emoji_team_id,omitempty"`
	Max            *int         `json:"max,omitempty"`
	ShowMemberName *bool        `json:"show_member_name,omitempty"`
	NotifyUsers    *bool        `json:"notify_users,omitempty"`
}

type ListColumn struct {
	ID              string             `json:"id,omitempty"`
	Key             string             `json:"key"`
	Name            string             `json:"name"`
	Type            string             `json:"type"`
	IsPrimaryColumn bool               `json:"is_primary_column,omitempty"`
	Options         *ListColumnOptions `json:"options,omitempty"`
}

var supportedListColumnTypes = map[string]struct{}{
	"text": {}, "rich_text": {}, "number": {}, "select": {}, "multi_select": {}, "date": {},
	"user": {}, "checkbox": {}, "email": {}, "phone": {}, "channel": {}, "link": {},
}

type UnsupportedListFieldTypeError struct{ Type string }

func (e *UnsupportedListFieldTypeError) Error() string {
	return fmt.Sprintf("unsupported Slack List field type %q", e.Type)
}

func validateColumns(columns []ListColumn) error {
	for _, column := range columns {
		if _, ok := supportedListColumnTypes[column.Type]; !ok {
			return &UnsupportedListFieldTypeError{Type: column.Type}
		}
	}
	return nil
}

type CreateListRequest struct {
	Name                     string          `json:"name"`
	DescriptionBlocks        json.RawMessage `json:"description_blocks,omitempty"`
	Schema                   []ListColumn    `json:"schema,omitempty"`
	CopyFromListID           string          `json:"copy_from_list_id,omitempty"`
	IncludeCopiedListRecords bool            `json:"include_copied_list_records,omitempty"`
	TodoMode                 bool            `json:"todo_mode,omitempty"`
}

type UpdateListRequest struct {
	ID                string          `json:"id"`
	Name              string          `json:"name,omitempty"`
	DescriptionBlocks json.RawMessage `json:"description_blocks,omitempty"`
	TodoMode          *bool           `json:"todo_mode,omitempty"`
}

type ListMetadata struct {
	ID            string       `json:"id,omitempty"`
	Name          string       `json:"name,omitempty"`
	Schema        []ListColumn `json:"schema,omitempty"`
	SubtaskSchema []ListColumn `json:"subtask_schema,omitempty"`
	TodoMode      bool         `json:"todo_mode,omitempty"`
}

type ListFieldValue struct {
	ColumnID string             `json:"column_id"`
	RowID    string             `json:"row_id,omitempty"`
	Key      string             `json:"key,omitempty"`
	Value    string             `json:"value,omitempty"`
	Text     string             `json:"text,omitempty"`
	RichText *[]json.RawMessage `json:"rich_text,omitempty"`
	Date     *[]string          `json:"date,omitempty"`
	Select   *[]string          `json:"select,omitempty"`
	User     *[]string          `json:"user,omitempty"`
	Channel  *[]string          `json:"channel,omitempty"`
	Number   *[]float64         `json:"number,omitempty"`
	Checkbox *[]bool            `json:"checkbox,omitempty"`
	Email    *[]string          `json:"email,omitempty"`
	Phone    *[]string          `json:"phone,omitempty"`
	Link     *[]string          `json:"link,omitempty"`
}

type ListItem struct {
	ID               string           `json:"id"`
	ListID           string           `json:"list_id"`
	DateCreated      int64            `json:"date_created"`
	CreatedBy        string           `json:"created_by"`
	UpdatedBy        string           `json:"updated_by"`
	UpdatedTimestamp string           `json:"updated_timestamp"`
	ParentRecordID   string           `json:"parent_record_id,omitempty"`
	Fields           []ListFieldValue `json:"fields"`
}

type CreateListItemRequest struct {
	ListID           string           `json:"list_id"`
	DuplicatedItemID string           `json:"duplicated_item_id,omitempty"`
	ParentItemID     string           `json:"parent_item_id,omitempty"`
	InitialFields    []ListFieldValue `json:"initial_fields,omitempty"`
}

type UpdateListItemsRequest struct {
	ListID string           `json:"list_id"`
	Cells  []ListFieldValue `json:"cells"`
}

type ListItemsRequest struct {
	ListID   string `json:"list_id"`
	Limit    int    `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Archived *bool  `json:"archived,omitempty"`
}

type ListItemsPage struct {
	Items        []ListItem   `json:"items"`
	ListMetadata ListMetadata `json:"list_metadata,omitempty"`
	NextCursor   string       `json:"next_cursor"`
}

func (c *ListsClient) CreateList(ctx context.Context, request CreateListRequest) (string, ListMetadata, error) {
	if err := validateColumns(request.Schema); err != nil {
		return "", ListMetadata{}, err
	}
	var response struct {
		ListID       string       `json:"list_id"`
		ListMetadata ListMetadata `json:"list_metadata"`
	}
	if err := c.call(ctx, "slackLists.create", request, &response); err != nil {
		return "", ListMetadata{}, err
	}
	response.ListMetadata.ID = response.ListID
	return response.ListID, response.ListMetadata, nil
}

func (c *ListsClient) UpdateList(ctx context.Context, request UpdateListRequest) error {
	return c.call(ctx, "slackLists.update", request, nil)
}

func (c *ListsClient) GetList(ctx context.Context, listID string) (ListMetadata, error) {
	return ListMetadata{}, ErrListInfoUnsupported
}

func (c *ListsClient) ListLists(context.Context, string, int) ([]ListMetadata, string, error) {
	return nil, "", ErrListEnumerationUnsupported
}

func (c *ListsClient) CreateItem(ctx context.Context, request CreateListItemRequest) (ListItem, error) {
	if err := validateFieldInputs(request.InitialFields, false); err != nil {
		return ListItem{}, err
	}
	var response struct {
		Item ListItem `json:"item"`
	}
	if err := c.call(ctx, "slackLists.items.create", request, &response); err != nil {
		return ListItem{}, err
	}
	return response.Item, nil
}

func (c *ListsClient) UpdateItems(ctx context.Context, request UpdateListItemsRequest) error {
	if err := validateFieldInputs(request.Cells, true); err != nil {
		return err
	}
	return c.call(ctx, "slackLists.items.update", request, nil)
}

func (c *ListsClient) GetItem(ctx context.Context, listID, itemID string) (ListItem, error) {
	var response struct {
		Record ListItem `json:"record"`
	}
	if err := c.call(ctx, "slackLists.items.info", map[string]string{"list_id": listID, "id": itemID}, &response); err != nil {
		return ListItem{}, err
	}
	return response.Record, nil
}

func (c *ListsClient) ListItems(ctx context.Context, request ListItemsRequest) (ListItemsPage, error) {
	var response struct {
		Items        []ListItem   `json:"items"`
		ListMetadata ListMetadata `json:"list_metadata"`
		Metadata     struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := c.call(ctx, "slackLists.items.list", request, &response); err != nil {
		return ListItemsPage{}, err
	}
	return ListItemsPage{Items: response.Items, ListMetadata: response.ListMetadata, NextCursor: response.Metadata.NextCursor}, nil
}

func (c *ListsClient) DeleteItem(ctx context.Context, listID, itemID string) error {
	return c.call(ctx, "slackLists.items.delete", map[string]string{"list_id": listID, "id": itemID}, nil)
}

func (c *ListsClient) call(ctx context.Context, method string, requestBody any, output any) error {
	token := c.Token
	if c.TokenProvider != nil {
		token = c.TokenProvider()
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("Slack Lists OAuth token is required")
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", method, err)
	}
	base := c.APIBase
	if base == "" {
		base = slackListsAPIBase
	}
	if base != slackListsAPIBase && !strings.HasPrefix(base, "http://127.0.0.1:") {
		return errors.New("Slack Lists API base must be Slack or loopback test server")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, base+method, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		if listsMethodMutates(method) {
			return &ListsAPIError{Kind: ListsErrorAPI, SlackCode: "outcome_unknown", MayHaveMutated: true}
		}
		return fmt.Errorf("call %s: %w", method, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		retryAfter, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		return &ListsAPIError{Kind: ListsErrorRateLimit, StatusCode: response.StatusCode, RetryAfter: time.Duration(retryAfter) * time.Second}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", method, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &ListsAPIError{Kind: ListsErrorAPI, StatusCode: response.StatusCode, MayHaveMutated: listsMethodMutates(method) && response.StatusCode >= 500}
	}
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	if !envelope.OK {
		return classifyListsError(envelope.Error)
	}
	if output != nil {
		if err := json.Unmarshal(raw, output); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
	}
	return nil
}

func listsMethodMutates(method string) bool {
	return method == "slackLists.create" || method == "slackLists.update" || method == "slackLists.items.create" || method == "slackLists.items.update" || method == "slackLists.items.delete"
}

func classifyListsError(code string) error {
	kind := ListsErrorAPI
	switch code {
	case "missing_scope":
		kind = ListsErrorScope
	case "no_permission", "permission_denied", "access_denied", "not_allowed_token_type":
		kind = ListsErrorPermission
	case "paid_feature_not_available", "feature_not_enabled", "plan_upgrade_required", "enterprise_is_restricted":
		kind = ListsErrorUnavailable
	case "item_not_found", "invalid_row_id", "list_not_found":
		kind = ListsErrorConflict
	}
	return &ListsAPIError{Kind: kind, SlackCode: code, MayHaveMutated: code == "fatal_error" || code == "internal_error"}
}

func validateFieldInputs(fields []ListFieldValue, requireRowID bool) error {
	for _, field := range fields {
		if field.ColumnID == "" {
			return errors.New("Slack List field column_id is required")
		}
		if requireRowID && field.RowID == "" {
			return errors.New("Slack List update field row_id is required")
		}
		if field.Key != "" || field.Value != "" || field.Text != "" {
			return errors.New("Slack List response-only key, value, and text fields cannot be sent")
		}
		count := 0
		for _, present := range []bool{
			field.RichText != nil, field.Date != nil, field.Select != nil, field.User != nil,
			field.Channel != nil, field.Number != nil, field.Checkbox != nil, field.Email != nil,
			field.Phone != nil, field.Link != nil,
		} {
			if present {
				count++
			}
		}
		if count != 1 {
			return errors.New("Slack List field requires exactly one supported typed value")
		}
	}
	return nil
}
