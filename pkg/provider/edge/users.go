package edge

import (
	"context"
	"runtime/trace"

	"github.com/slack-go/slack"
)

// users/search API (edge cache; xoxc/xoxd only)

type UsersSearchResponse struct {
	Ok      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Results []slack.User `json:"results,omitempty"`
}

func (cl *Client) UsersSearch(ctx context.Context, query string, count int) ([]slack.User, error) {
	ctx, task := trace.NewTask(ctx, "UsersSearch")
	defer task.End()

	if count <= 0 {
		count = 10
	}

	req := &usersSearchForm{
		Query: query,
		Count: count,
	}

	var resp UsersSearchResponse
	if err := cl.callEdgeAPI(ctx, &resp, "users/search", req); err != nil {
		return nil, err
	}

	if !resp.Ok {
		return nil, &APIError{Err: resp.Error, Endpoint: "users/search"}
	}

	return resp.Results, nil
}

type usersSearchForm struct {
	BaseRequest
	Query string `json:"query"`
	Count int    `json:"count,omitempty"`
}
