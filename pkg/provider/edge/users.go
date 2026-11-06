package edge

import (
	"context"

	"github.com/slack-go/slack"
)

// users/search API (edge cache; xoxc/xoxd only)

type usersSearchResponse struct {
	Results []slack.User `json:"results,omitempty"`
}

func (cl *Client) UsersSearch(ctx context.Context, query string, count int) ([]slack.User, error) {
	if count <= 0 {
		count = 10
	}

	req := &usersSearchForm{
		Query: query,
		Count: count,
	}

	var resp usersSearchResponse
	if err := cl.postJSON(ctx, "users/search", req, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

type usersSearchForm struct {
	BaseRequest
	Query string `json:"query"`
	Count int    `json:"count,omitempty"`
}
