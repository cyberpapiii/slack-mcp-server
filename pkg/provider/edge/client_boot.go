package edge

import (
	"context"
	"time"
)

type clientUserBootForm struct {
	MinChannelUpdated          int64 `json:"min_channel_updated"`
	IncludeMinVersionBumpCheck int   `json:"include_min_version_bump_check"`
	VersionTS                  int64 `json:"version_ts"`
	BuildVersionTS             int64 `json:"build_version_ts"`
	WebClientFields
}

func (cl *Client) ClientUserBoot(ctx context.Context) (*ClientUserBootResponse, error) {
	future := time.Now().Add(24 * time.Hour)
	form := clientUserBootForm{
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

type ClientUserBootResponse struct {
	IMs      []Channel `json:"ims"`
	Starred  []any     `json:"starred"`
	Channels []Channel `json:"channels"`
}
