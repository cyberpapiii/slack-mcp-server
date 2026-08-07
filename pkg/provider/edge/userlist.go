package edge

import (
	"context"
	"errors"
	"fmt"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/rusq/slack"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

type UsersListRequest struct {
	BaseRequest
	Channels                []string `json:"channels"`
	PresentFirst            bool     `json:"present_first,omitempty"`
	Filter                  string   `json:"filter"`
	Index                   string   `json:"index,omitempty"`
	Locale                  string   `json:"locale,omitempty"`
	IncludeProfileOnlyUsers bool     `json:"include_profile_only_users,omitempty"`
	Marker                  string   `json:"marker,omitempty"` // pagination, it must contain the next_marker from the previous response
	Count                   int      `json:"count"`
}

type UsersListResponse struct {
	Results    []User `json:"results"`
	NextMarker string `json:"next_marker"` // pagination, marker value which must be used in the next request, if not empty.
	baseResponse
}

type User struct {
	ID                     string         `json:"id"`
	TeamID                 string         `json:"team_id"`
	Name                   string         `json:"name"`
	Deleted                bool           `json:"deleted"`
	Color                  string         `json:"color"`
	RealName               string         `json:"real_name"`
	Tz                     string         `json:"tz"`
	TzLabel                string         `json:"tz_label"`
	TzOffset               int64          `json:"tz_offset"`
	Profile                Profile        `json:"profile"`
	IsAdmin                bool           `json:"is_admin"`
	IsOwner                bool           `json:"is_owner"`
	IsPrimaryOwner         bool           `json:"is_primary_owner"`
	IsRestricted           bool           `json:"is_restricted"`
	IsUltraRestricted      bool           `json:"is_ultra_restricted"`
	IsBot                  bool           `json:"is_bot"`
	IsAppUser              bool           `json:"is_app_user"`
	Updated                slack.JSONTime `json:"updated"`
	IsEmailConfirmed       bool           `json:"is_email_confirmed"`
	WhoCanShareContactCard string         `json:"who_can_share_contact_card"`
	Has2Fa                 *bool          `json:"has_2fa,omitempty"`
}

type Profile struct {
	Title                  string  `json:"title"`
	Phone                  string  `json:"phone"`
	Skype                  string  `json:"skype"`
	RealName               string  `json:"real_name"`
	RealNameNormalized     string  `json:"real_name_normalized"`
	DisplayName            string  `json:"display_name"`
	DisplayNameNormalized  string  `json:"display_name_normalized"`
	Fields                 any     `json:"fields"`
	StatusText             string  `json:"status_text"`
	StatusEmoji            string  `json:"status_emoji"`
	StatusEmojiDisplayInfo []any   `json:"status_emoji_display_info"`
	StatusExpiration       int64   `json:"status_expiration"`
	AvatarHash             string  `json:"avatar_hash"`
	GuestInvitedBy         string  `json:"guest_invited_by"`
	ImageOriginal          *string `json:"image_original,omitempty"`
	IsCustomImage          *bool   `json:"is_custom_image,omitempty"`
	Email                  string  `json:"email"`
	FirstName              *string `json:"first_name,omitempty"`
	LastName               *string `json:"last_name,omitempty"`
	StatusTextCanonical    string  `json:"status_text_canonical"`
	Team                   string  `json:"team"`
}

type UsersInfoRequest struct {
	BaseRequest
	CheckInteraction        bool             `json:"check_interaction"`
	IncludeProfileOnlyUsers bool             `json:"include_profile_only_users"`
	UpdatedIDS              map[string]int64 `json:"updated_ids"`
}

type UserInfoResponse struct {
	Results     []UserInfo      `json:"results"`
	FailedIDS   []string        `json:"failed_ids"`
	PendingIDS  []string        `json:"pending_ids"`
	CanInteract map[string]bool `json:"can_interact"`
	baseResponse
}

type UserInfo struct {
	ID                     string  `json:"id"`
	TeamID                 string  `json:"team_id"`
	Name                   string  `json:"name"`
	Color                  string  `json:"color"`
	IsBot                  bool    `json:"is_bot"`
	IsAppUser              bool    `json:"is_app_user"`
	Deleted                bool    `json:"deleted"`
	Profile                Profile `json:"profile"`
	IsStranger             bool    `json:"is_stranger"`
	Updated                int64   `json:"updated"`
	WhoCanShareContactCard string  `json:"who_can_share_contact_card"`
}

var ErrNotOK = errors.New("server returned NOT OK")

// GetUsers fetches user info by ID via users/info, retrying pending IDs.
func (cl *Client) GetUsers(ctx context.Context, userID ...string) ([]UserInfo, error) {
	if len(userID) == 0 {
		return []UserInfo{}, nil
	}
	updatedIds := make(map[string]int64, len(userID))
	for _, id := range userID {
		updatedIds[id] = 0
	}

	lim := limiter.Tier3.Limiter()
	var users []UserInfo
	const maxUsersInfoRounds = 20
	for round := 0; round < maxUsersInfoRounds; round++ {
		uiresp, err := cl.UsersInfo(ctx, &UsersInfoRequest{
			CheckInteraction:        true,
			IncludeProfileOnlyUsers: true,
			UpdatedIDS:              updatedIds,
		})
		if err != nil {
			return nil, err
		}
		if !uiresp.Ok {
			return nil, ErrNotOK
		}
		if len(uiresp.Results) > 0 {
			users = append(users, uiresp.Results...)
		}

		failed := make(map[string]struct{}, len(uiresp.FailedIDS))
		for _, id := range uiresp.FailedIDS {
			failed[id] = struct{}{}
		}
		updatedFromResults := make(map[string]int64, len(uiresp.Results))
		for _, ui := range uiresp.Results {
			updatedFromResults[ui.ID] = ui.Updated
		}

		pending := make(map[string]int64, len(uiresp.PendingIDS))
		for _, id := range uiresp.PendingIDS {
			if _, bad := failed[id]; bad {
				continue
			}
			if u, ok := updatedFromResults[id]; ok {
				pending[id] = u
			} else if u, ok := updatedIds[id]; ok {
				pending[id] = u
			} else {
				pending[id] = 0
			}
		}
		if len(pending) == 0 {
			return users, nil
		}
		updatedIds = pending
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("users/info: %d ids still pending after %d rounds", len(updatedIds), maxUsersInfoRounds)
}

// UsersInfo may return pending_ids; callers should retry (see [Client.GetUsers]).
func (cl *Client) UsersInfo(ctx context.Context, req *UsersInfoRequest) (*UserInfoResponse, error) {
	var ui UserInfoResponse
	if err := cl.callEdgeAPI(ctx, &ui, "users/info", req); err != nil {
		return nil, err
	}
	return &ui, nil
}

func (cl *Client) UsersList(ctx context.Context, channelIDs ...string) ([]User, error) {
	if len(channelIDs) == 0 {
		return nil, errors.New("no channel IDs provided")
	}
	channelIDs, dmIDs := splitDMs(channelIDs)

	// Shared limiter: Slack meters per token, not per goroutine.
	lim := limiter.Tier3.Limiter()

	var pub, dms []User

	eg, ctx := errgroup.WithContext(ctx)
	if len(channelIDs) > 0 {
		eg.Go(func() error {
			u, err := cl.publicUserList(ctx, channelIDs, lim)
			if err != nil {
				return err
			}
			pub = u
			return nil
		})
	}
	if len(dmIDs) > 0 {
		eg.Go(func() error {
			u, err := cl.directUserList(ctx, dmIDs, lim)
			if err != nil {
				return err
			}
			dms = u
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	return append(pub, dms...), nil
}

func (cl *Client) publicUserList(ctx context.Context, channelIDs []string, lim *rate.Limiter) ([]User, error) {
	const (
		everyone = "everyone"
		index    = "users_by_display_name"
		count    = 50
	)
	req := UsersListRequest{
		Channels: channelIDs,
		Filter:   everyone,
		Index:    index,
		Locale:   "en-US",
		Count:    count,
	}
	uu := make([]User, 0, count)
	for {
		var ur UsersListResponse
		if err := cl.callEdgeAPI(ctx, &ur, "users/list", &req); err != nil {
			return nil, err
		}
		if err := ur.validate("users/list"); err != nil {
			return nil, err
		}
		if len(ur.Results) == 0 && ur.NextMarker == "" {
			break
		}
		uu = append(uu, ur.Results...)
		if ur.NextMarker == "" {
			break
		}
		req.Marker = ur.NextMarker
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
	}
	return uu, nil
}

// directUserList fetches users via conversations.view (slower than users/list).
func (cl *Client) directUserList(ctx context.Context, dmIDs []string, lim *rate.Limiter) ([]User, error) {
	if len(dmIDs) == 0 {
		return nil, errors.New("no direct message IDs provided")
	}
	var ret []User
	for _, id := range dmIDs {
		resp, err := cl.ConversationsView(ctx, id)
		if err != nil {
			return nil, err
		}
		ret = append(ret, resp.Users...)
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func splitDMs(IDs []string) (chans []string, dms []string) {
	for _, id := range IDs {
		if len(id) == 0 {
			continue
		}
		if id[0] == 'D' {
			dms = append(dms, id)
		} else {
			chans = append(chans, id)
		}
	}
	return chans, dms
}
