package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/trace"
)

type userPrefsGetForm struct {
	BaseRequest
	WebClientFields
}

type userPrefsGetResponse struct {
	baseResponse
	Prefs map[string]json.RawMessage `json:"prefs"`
}

// allNotificationsPrefs is the JSON string stored in "all_notifications_prefs".
type allNotificationsPrefs struct {
	Channels map[string]channelNotifSettings `json:"channels"`
}

type channelNotifSettings struct {
	Muted *bool `json:"muted,omitempty"`
}

// GetMutedChannels returns muted channel IDs from users.prefs.get.
func (cl *Client) GetMutedChannels(ctx context.Context) (map[string]bool, error) {
	ctx, task := trace.NewTask(ctx, "GetMutedChannels")
	defer task.End()

	form := userPrefsGetForm{
		BaseRequest:     BaseRequest{Token: cl.token},
		WebClientFields: webclientReason("prefs"),
	}

	resp, err := cl.PostForm(ctx, "users.prefs.get", values(form, true))
	if err != nil {
		return nil, err
	}

	var prefsResp userPrefsGetResponse
	if err := cl.ParseResponse(&prefsResp, resp); err != nil {
		return nil, err
	}
	if err := prefsResp.validate("users.prefs.get"); err != nil {
		return nil, err
	}

	raw, ok := prefsResp.Prefs["all_notifications_prefs"]
	if !ok {
		return nil, nil
	}

	var notifJSON string
	if err := json.Unmarshal(raw, &notifJSON); err != nil {
		return nil, fmt.Errorf("users.prefs.get all_notifications_prefs: %w", err)
	}

	var notifPrefs allNotificationsPrefs
	if err := json.Unmarshal([]byte(notifJSON), &notifPrefs); err != nil {
		return nil, fmt.Errorf("users.prefs.get all_notifications_prefs decode: %w", err)
	}

	muted := make(map[string]bool, len(notifPrefs.Channels))
	for channelID, settings := range notifPrefs.Channels {
		if settings.Muted != nil && *settings.Muted {
			muted[channelID] = true
		}
	}

	return muted, nil
}
