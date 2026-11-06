package edge

import (
	"context"
	"encoding/json"

	"github.com/slack-go/slack"
)

// saved.* API (xoxc/xoxd; replaced stars.* March 2023)

type savedListForm struct {
	BaseRequest
	Limit             int    `json:"limit"`
	Filter            string `json:"filter"`
	Cursor            string `json:"cursor,omitempty"`
	IncludeTombstones bool   `json:"include_tombstones"`
	WebClientFields
}

type SavedListResponse struct {
	SavedItems       []SavedItem            `json:"saved_items"`
	Counts           SavedCounts            `json:"counts"`
	ResponseMetadata slack.ResponseMetadata `json:"response_metadata"`
}

type SavedItem struct {
	ItemID           string `json:"item_id"`
	ItemType         string `json:"item_type"`
	DateCreated      int64  `json:"date_created"`
	DateDue          int64  `json:"date_due"`
	DateCompleted    int64  `json:"date_completed"`
	DateUpdated      int64  `json:"date_updated"`
	IsArchived       bool   `json:"is_archived"`
	DateSnoozedUntil int64  `json:"date_snoozed_until"`
	Ts               string `json:"ts"`
	State            string `json:"state"`
	// TodoState says which list the item is on: to_do, saved or completed.
	TodoState string `json:"todo_state"`
	// Description is the rich-text body of a custom to-do (item type reminder).
	Description json.RawMessage `json:"description,omitempty"`
}

type SavedCounts struct {
	UncompletedCount        int `json:"uncompleted_count"`
	UncompletedOverdueCount int `json:"uncompleted_overdue_count"`
	ArchivedCount           int `json:"archived_count"`
	CompletedCount          int `json:"completed_count"`
	TotalCount              int `json:"total_count"`
}

func (cl *Client) SavedList(ctx context.Context, filter string, limit int, cursor string) (SavedListResponse, error) {
	form := savedListForm{
		BaseRequest:       BaseRequest{Token: cl.token},
		Limit:             limit,
		Filter:            filter,
		Cursor:            cursor,
		IncludeTombstones: true,
		WebClientFields:   webclientReason("saved-api/savedList"),
	}

	r := SavedListResponse{}
	if err := cl.postForm(ctx, "saved.list", form, &r); err != nil {
		return SavedListResponse{}, err
	}
	return r, nil
}

// Saved item types. A to-do or saved entry is one of these; the list it is on is
// its todo_state, so To-dos and Saved share one API.
const (
	SavedItemMessage  = "message"
	SavedItemFile     = "file"
	SavedItemReminder = "reminder"
)

// SavedUpdateParams changes a saved item. DateDue is a string because 0 is a real
// value that clears the due date, and an omitempty integer would drop it.
type SavedUpdateParams struct {
	ItemType  string
	ItemID    string
	Ts        string
	Mark      string
	TodoState string
	DateDue   string
}

type savedUpdateForm struct {
	BaseRequest
	ItemType  string `json:"item_type"`
	ItemID    string `json:"item_id"`
	Ts        string `json:"ts,omitempty"`
	Mark      string `json:"mark,omitempty"`
	TodoState string `json:"todo_state,omitempty"`
	DateDue   string `json:"date_due,omitempty"`
	WebClientFields
}

func (cl *Client) SavedUpdate(ctx context.Context, params SavedUpdateParams) error {
	form := savedUpdateForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		ItemType:        params.ItemType,
		ItemID:          params.ItemID,
		Ts:              params.Ts,
		Mark:            params.Mark,
		TodoState:       params.TodoState,
		DateDue:         params.DateDue,
		WebClientFields: webclientReason("saved-api/updateSavedItem"),
	}

	return cl.postForm(ctx, "saved.update", form, nil)
}

// SavedAddParams creates a saved item. Description is the JSON rich-text body of
// a custom to-do; message and file items point at something that already exists.
type SavedAddParams struct {
	ItemType    string
	ItemID      string
	Ts          string
	Description string
	TodoState   string
	DateDue     string
}

type savedAddForm struct {
	BaseRequest
	ItemType    string `json:"item_type"`
	ItemID      string `json:"item_id,omitempty"`
	Ts          string `json:"ts,omitempty"`
	Description string `json:"description,omitempty"`
	TodoState   string `json:"todo_state,omitempty"`
	DateDue     string `json:"date_due,omitempty"`
	WebClientFields
}

func (cl *Client) SavedAdd(ctx context.Context, params SavedAddParams) (SavedItem, error) {
	form := savedAddForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		ItemType:        params.ItemType,
		ItemID:          params.ItemID,
		Ts:              params.Ts,
		Description:     params.Description,
		TodoState:       params.TodoState,
		DateDue:         params.DateDue,
		WebClientFields: webclientReason("saved-api/addSavedItem"),
	}

	r := struct {
		Item SavedItem `json:"item"`
	}{}
	if err := cl.postForm(ctx, "saved.add", form, &r); err != nil {
		return SavedItem{}, err
	}
	return r.Item, nil
}

type savedDeleteForm struct {
	BaseRequest
	ItemType string `json:"item_type"`
	ItemID   string `json:"item_id"`
	Ts       string `json:"ts,omitempty"`
	WebClientFields
}

func (cl *Client) SavedDelete(ctx context.Context, itemType, itemID, ts string) error {
	form := savedDeleteForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		ItemType:        itemType,
		ItemID:          itemID,
		Ts:              ts,
		WebClientFields: webclientReason("saved-api/deleteSavedItem"),
	}

	return cl.postForm(ctx, "saved.delete", form, nil)
}

type savedClearCompletedForm struct {
	BaseRequest
	WebClientFields
}

func (cl *Client) SavedClearCompleted(ctx context.Context) error {
	form := savedClearCompletedForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		WebClientFields: webclientReason("manually_marked_all_completed"),
	}

	return cl.postForm(ctx, "saved.clearCompleted", form, nil)
}
