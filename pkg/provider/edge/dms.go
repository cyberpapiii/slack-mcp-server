package edge

import (
	"context"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/slack-go/slack"
)

// im.* API

type imListForm struct {
	BaseRequest
	GetLatest    bool   `json:"get_latest"`
	GetReadState bool   `json:"get_read_state"`
	Cursor       string `json:"cursor,omitempty"`
	WebClientFields
}

type imListResponse struct {
	IMs              []Channel              `json:"ims,omitempty"`
	ResponseMetadata slack.ResponseMetadata `json:"response_metadata"`
}

func (cl *Client) imList(ctx context.Context) ([]Channel, error) {
	form := imListForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		GetLatest:       true,
		GetReadState:    true,
		WebClientFields: webclientReason("guided-search-people-empty-state"),
	}
	lim := limiter.Tier2boost
	var IMs []Channel
	for {
		var r imListResponse
		if err := cl.postForm(ctx, "im.list", form, &r); err != nil {
			return nil, err
		}
		IMs = append(IMs, r.IMs...)
		if r.ResponseMetadata.Cursor == "" {
			break
		}
		form.Cursor = r.ResponseMetadata.Cursor
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
	}
	return IMs, nil
}
