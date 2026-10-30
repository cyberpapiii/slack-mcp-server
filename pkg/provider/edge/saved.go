package edge

import (
	"context"
	"encoding/json"
	"runtime/trace"
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
	baseResponse
	SavedItems []SavedItem `json:"saved_items"`
	Counts     SavedCounts `json:"counts"`
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
	ctx, task := trace.NewTask(ctx, "SavedList")
	defer task.End()

	form := savedListForm{
		BaseRequest:       BaseRequest{Token: cl.token},
		Limit:             limit,
		Filter:            filter,
		Cursor:            cursor,
		IncludeTombstones: true,
		WebClientFields:   webclientReason("saved-api/savedList"),
	}

	resp, err := cl.postForm(ctx, "saved.list", values(form, true))
	if err != nil {
		return SavedListResponse{}, err
	}
	r := SavedListResponse{}
	if err := cl.parseResponse(&r, resp); err != nil {
		return SavedListResponse{}, err
	}
	if err := r.validate("saved.list"); err != nil {
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
	ctx, task := trace.NewTask(ctx, "SavedUpdate")
	defer task.End()

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

	resp, err := cl.postForm(ctx, "saved.update", values(form, true))
	if err != nil {
		return err
	}
	r := baseResponse{}
	if err := cl.parseResponse(&r, resp); err != nil {
		return err
	}
	if err := r.validate("saved.update"); err != nil {
		return err
	}
	return nil
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
	ctx, task := trace.NewTask(ctx, "SavedAdd")
	defer task.End()

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

	resp, err := cl.postForm(ctx, "saved.add", values(form, true))
	if err != nil {
		return SavedItem{}, err
	}
	r := struct {
		baseResponse
		Item SavedItem `json:"item"`
	}{}
	if err := cl.parseResponse(&r, resp); err != nil {
		return SavedItem{}, err
	}
	if err := r.validate("saved.add"); err != nil {
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
	ctx, task := trace.NewTask(ctx, "SavedDelete")
	defer task.End()

	form := savedDeleteForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		ItemType:        itemType,
		ItemID:          itemID,
		Ts:              ts,
		WebClientFields: webclientReason("saved-api/deleteSavedItem"),
	}

	resp, err := cl.postForm(ctx, "saved.delete", values(form, true))
	if err != nil {
		return err
	}
	r := baseResponse{}
	if err := cl.parseResponse(&r, resp); err != nil {
		return err
	}
	return r.validate("saved.delete")
}

type savedClearCompletedForm struct {
	BaseRequest
	WebClientFields
}

func (cl *Client) SavedClearCompleted(ctx context.Context) error {
	ctx, task := trace.NewTask(ctx, "SavedClearCompleted")
	defer task.End()

	form := savedClearCompletedForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		WebClientFields: webclientReason("manually_marked_all_completed"),
	}

	resp, err := cl.postForm(ctx, "saved.clearCompleted", values(form, true))
	if err != nil {
		return err
	}
	r := baseResponse{}
	if err := cl.parseResponse(&r, resp); err != nil {
		return err
	}
	if err := r.validate("saved.clearCompleted"); err != nil {
		return err
	}
	return nil
}
