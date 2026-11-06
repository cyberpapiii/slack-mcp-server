package edge

import (
	"context"
	"time"
)

// client.userBoot API

type clientUserBootForm struct {
	BaseRequest
	MinChannelUpdated          int64 `json:"min_channel_updated"`
	IncludeMinVersionBumpCheck int   `json:"include_min_version_bump_check"`
	VersionTS                  int64 `json:"version_ts"`
	BuildVersionTS             int64 `json:"build_version_ts"`
	WebClientFields
}

func (cl *Client) ClientUserBoot(ctx context.Context) (*ClientUserBootResponse, error) {
	future := time.Now().Add(24 * time.Hour)
	form := clientUserBootForm{
		BaseRequest:                BaseRequest{Token: cl.token},
		IncludeMinVersionBumpCheck: 1,
		VersionTS:                  future.Unix(),
		BuildVersionTS:             future.Unix(),
		WebClientFields:            webclientReason("initial-data"),
	}
	var ub ClientUserBootResponse
	if err := cl.postForm(ctx, "client.userBoot", form, &ub); err != nil {
		return nil, err
	}
	return &ub, nil
}

// ClientUserBootResponse keeps the fields this server actually reads.
// encoding/json ignores the rest of the client.userBoot payload.
type ClientUserBootResponse struct {
	IMs      []Channel `json:"ims"`
	Starred  []any     `json:"starred"`
	Channels []Channel `json:"channels"`
}
