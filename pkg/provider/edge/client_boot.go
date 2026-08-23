package edge

import (
	"context"
	"runtime/trace"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge/fasttime"
	"github.com/rusq/slack"
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
	ctx, task := trace.NewTask(ctx, "ClientUserBoot")
	defer task.End()

	future := time.Now().Add(24 * time.Hour)
	form := clientUserBootForm{
		BaseRequest:                BaseRequest{Token: cl.token},
		IncludeMinVersionBumpCheck: 1,
		VersionTS:                  future.Unix(),
		BuildVersionTS:             future.Unix(),
		WebClientFields:            webclientReason("initial-data"),
	}
	var ub ClientUserBootResponse
	resp, err := cl.postForm(ctx, "client.userBoot", values(form, true))
	if err != nil {
		return nil, err
	}
	if err := cl.parseResponse(&ub, resp); err != nil {
		return nil, err
	}
	if err := ub.validate("client.userBoot"); err != nil {
		return nil, err
	}
	return &ub, nil
}

// ClientUserBootResponse keeps the fields this server actually reads.
// encoding/json ignores the rest of the client.userBoot payload.
type ClientUserBootResponse struct {
	baseResponse
	IMs      []IM              `json:"ims"`
	Starred  []any             `json:"starred"`
	Channels []UserBootChannel `json:"channels"`
}

type UserBootChannel struct {
	ID                 string        `json:"id"`
	Name               string        `json:"name"`
	IsChannel          bool          `json:"is_channel"`
	IsGroup            bool          `json:"is_group"`
	IsIM               bool          `json:"is_im"`
	IsMpim             bool          `json:"is_mpim"`
	IsPrivate          bool          `json:"is_private"`
	Created            int64         `json:"created"`
	IsArchived         bool          `json:"is_archived"`
	IsGeneral          bool          `json:"is_general"`
	Unlinked           int64         `json:"unlinked"`
	NameNormalized     string        `json:"name_normalized"`
	IsShared           bool          `json:"is_shared"`
	IsOrgShared        bool          `json:"is_org_shared"`
	IsPendingEXTShared bool          `json:"is_pending_ext_shared"`
	Creator            string        `json:"creator"`
	IsEXTShared        bool          `json:"is_ext_shared"`
	Topic              Purpose       `json:"topic"`
	Purpose            Purpose       `json:"purpose"`
	IsMember           bool          `json:"is_member,omitempty"`
	LastRead           fasttime.Time `json:"last_read,omitempty"`
	IsOpen             bool          `json:"is_open,omitempty"`
	Members            []string      `json:"members"`
}

func (c *UserBootChannel) slackChannel() slack.Channel {
	// IM: first non-empty Members entry (self ID unavailable here).
	var userID string
	if c.IsIM && len(c.Members) > 0 {
		if len(c.Members) == 1 {
			userID = c.Members[0]
		} else if len(c.Members) >= 2 {
			for _, member := range c.Members {
				if member != "" {
					userID = member
					break
				}
			}
		}
	}

	return slack.Channel{
		GroupConversation: slack.GroupConversation{
			Conversation: slack.Conversation{
				ID:                 c.ID,
				Created:            slack.JSONTime(c.Created),
				IsOpen:             c.IsOpen,
				LastRead:           c.LastRead.SlackString(),
				Latest:             &slack.Message{},
				UnreadCount:        0,
				UnreadCountDisplay: 0,
				IsGroup:            c.IsGroup,
				IsShared:           c.IsShared,
				IsIM:               c.IsIM,
				IsExtShared:        c.IsEXTShared,
				IsOrgShared:        c.IsOrgShared,
				IsGlobalShared:     false,
				IsPendingExtShared: c.IsPendingEXTShared,
				IsPrivate:          c.IsPrivate,
				IsMpIM:             c.IsMpim,
				Unlinked:           int(c.Unlinked),
				NameNormalized:     c.NameNormalized,
				NumMembers:         len(c.Members),
				Priority:           0,
				User:               userID,
				ConnectedTeamIDs:   []string{},
				SharedTeamIDs:      []string{},
				InternalTeamIDs:    []string{},
			},
			Name:       c.Name,
			Creator:    c.Creator,
			IsArchived: c.IsArchived,
			Members:    c.Members,
			Topic: slack.Topic{
				Value:   c.Topic.Value,
				Creator: c.Topic.Creator,
				LastSet: slack.JSONTime(c.Topic.LastSet),
			},
			Purpose: slack.Purpose{
				Value:   c.Purpose.Value,
				Creator: c.Purpose.Creator,
				LastSet: slack.JSONTime(c.Purpose.LastSet),
			},
		},
		IsChannel: c.IsChannel,
		IsGeneral: c.IsGeneral,
		IsMember:  c.IsMember,
		Locale:    "",
	}
}

type Purpose struct {
	Value   string `json:"value"`
	Creator string `json:"creator"`
	LastSet int64  `json:"last_set"`
}
