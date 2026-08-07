package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/trace"
)

// userPrefsGetForm is the request body for users.prefs.get.
type userPrefsGetForm struct {
	BaseRequest
	WebClientFields
}

// userPrefsGetResponse is the response from users.prefs.get.
type userPrefsGetResponse struct {
	baseResponse
	Prefs map[string]json.RawMessage `json:"prefs"`
}

// allNotificationsPrefs is the parsed structure of the
// "all_notifications_prefs" user preference (stored as a JSON string).
type allNotificationsPrefs struct {
	Channels map[string]channelNotifSettings `json:"channels"`
}

type channelNotifSettings struct {
	Muted *bool `json:"muted,omitempty"`
}

// GetMutedChannels calls users.prefs.get and returns a set of channel IDs
// that the user has muted. Channels not present in the returned map are
// not muted.
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
