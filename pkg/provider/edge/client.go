package edge

import (
	"context"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge/fasttime"
)

// client.* API

type clientCountsForm struct {
	ThreadCountsByChannel bool `json:"thread_counts_by_channel"`
	OrgWideAware          bool `json:"org_wide_aware"`
	IncludeFileChannels   bool `json:"include_file_channels"`
	WebClientFields
}

type ClientCountsResponse struct {
	Channels []ChannelSnapshot `json:"channels,omitempty"`
	MPIMs    []ChannelSnapshot `json:"mpims,omitempty"`
	IMs      []ChannelSnapshot `json:"ims,omitempty"`
}

type ChannelSnapshot struct {
	ID             string        `json:"id"`
	LastRead       fasttime.Time `json:"last_read"`
	Latest         fasttime.Time `json:"latest"`
	HistoryInvalid fasttime.Time `json:"history_invalid"`
	MentionCount   int           `json:"mention_count"`
	HasUnreads     bool          `json:"has_unreads"`
}

func (cl *Client) ClientCounts(ctx context.Context) (ClientCountsResponse, error) {
	form := clientCountsForm{
		ThreadCountsByChannel: true,
		OrgWideAware:          true,
		IncludeFileChannels:   true,
		WebClientFields:       webclientReason("client-counts-api/fetchClientCounts"),
	}

	r := ClientCountsResponse{}
	if err := cl.postForm(ctx, "client.counts", form, &r); err != nil {
		return ClientCountsResponse{}, err
	}
	return r, nil
}

// activity.feed API

type activityFeedForm struct {
	Limit               int    `json:"limit"`
	Types               string `json:"types"`
	Mode                string `json:"mode"`
	ArchiveOnly         bool   `json:"archive_only"`
	UnreadOnly          bool   `json:"unread_only"`
	PriorityOnly        bool   `json:"priority_only"`
	OnlySalesforceChans bool   `json:"only_salesforce_channels"`
	IsActivityInbox     bool   `json:"is_activity_inbox"`
	WebClientFields
}

type ActivityFeedResponse struct {
	Items []ActivityFeedItem `json:"items,omitempty"`
}

type ActivityFeedItem struct {
	IsUnread bool              `json:"is_unread"`
	FeedTs   string            `json:"feed_ts"`
	Key      string            `json:"key"`
	Item     ActivityItemInner `json:"item"`
}

type ActivityItemInner struct {
	Type       string              `json:"type"`
	BundleInfo *ActivityBundleInfo `json:"bundle_info,omitempty"`
	Message    *ActivityMessage    `json:"message,omitempty"`
}

type ActivityBundleInfo struct {
	Payload struct {
		ThreadEntry struct {
			ChannelID      string `json:"channel_id"`
			ThreadTs       string `json:"thread_ts"`
			LatestTs       string `json:"latest_ts"`
			UnreadMsgCount int    `json:"unread_msg_count"`
			MinUnreadTs    string `json:"min_unread_ts"`
		} `json:"thread_entry"`
	} `json:"payload"`
}

type ActivityMessage struct {
	Ts           string `json:"ts"`
	Channel      string `json:"channel"`
	ThreadTs     string `json:"thread_ts"`
	AuthorUserID string `json:"author_user_id"`
	IsBroadcast  bool   `json:"is_broadcast"`
}

func (cl *Client) ActivityFeed(ctx context.Context, limit int) (ActivityFeedResponse, error) {
	form := activityFeedForm{
		Limit:           limit,
		Types:           "thread_v2,at_user,at_user_group,at_channel,at_everyone",
		Mode:            "priority_unreads_v1",
		WebClientFields: webclientReason("fetchActivityFeed"),
	}

	r := ActivityFeedResponse{}
	if err := cl.postForm(ctx, "activity.feed", form, &r); err != nil {
		return ActivityFeedResponse{}, err
	}
	return r, nil
}

// activity.markRead API

type activityMarkReadForm struct {
	Type   string `json:"type"`
	FeedTs string `json:"feed_ts"`
	Key    string `json:"key"`
	WebClientFields
}

func (cl *Client) ActivityMarkRead(ctx context.Context, itemType, feedTs, key string) error {
	form := activityMarkReadForm{
		Type:            itemType,
		FeedTs:          feedTs,
		Key:             key,
		WebClientFields: webclientReason("mark-as-read-v2"),
	}

	return cl.postForm(ctx, "activity.markRead", form, nil)
}
