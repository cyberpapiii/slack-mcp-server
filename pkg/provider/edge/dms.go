package edge

import (
	"context"

	"github.com/slack-go/slack"
)

// im.* API

type imListForm struct {
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
		GetLatest:       true,
		GetReadState:    true,
		WebClientFields: webclientReason("guided-search-people-empty-state"),
	}
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
	}
	return IMs, nil
}
