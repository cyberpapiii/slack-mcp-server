package edge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// DraftDestination identifies where Slack will place a native unsent draft.
type DraftDestination struct {
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	Broadcast bool   `json:"broadcast,omitempty"`
}

// Draft is Slack's browser-session draft representation.
type Draft struct {
	ID            string             `json:"id"`
	Blocks        []map[string]any   `json:"blocks,omitempty"`
	Destinations  []DraftDestination `json:"destinations,omitempty"`
	LastUpdatedTS string             `json:"last_updated_ts,omitempty"`
	DateCreated   int64              `json:"date_created,omitempty"`
	DateScheduled int64              `json:"date_scheduled,omitempty"`
	FileIDs       []string           `json:"file_ids,omitempty"`
}

type draftsResponse struct {
	baseResponse
	Draft  Draft   `json:"draft"`
	Drafts []Draft `json:"drafts"`
	NextTS string  `json:"next_ts,omitempty"`
}

func (cl *Client) DraftsList(ctx context.Context, limit int, activeOnly bool, cursor string) ([]Draft, string, error) {
	form := url.Values{}
	form.Set("token", cl.token)
	form.Set("limit", intString(limit))
	if activeOnly {
		form.Set("is_active", "true")
	}
	if strings.TrimSpace(cursor) != "" {
		form.Set("next_ts", cursor)
	}
	addDraftWebClientFields(form, "drafts/list")
	response, err := cl.PostForm(ctx, "drafts.list", form)
	if err != nil {
		return nil, "", err
	}
	var parsed draftsResponse
	if err := cl.ParseResponse(&parsed, response); err != nil {
		return nil, "", err
	}
	if err := parsed.validate("drafts.list"); err != nil {
		return nil, "", err
	}
	return parsed.Drafts, parsed.NextTS, nil
}

func (cl *Client) DraftCreate(ctx context.Context, channelID, threadTS, text string) (Draft, error) {
	form, err := draftMutationForm(channelID, threadTS, text)
	if err != nil {
		return Draft{}, err
	}
	form.Set("token", cl.token)
	form.Set("is_from_composer", "true")
	addDraftWebClientFields(form, "drafts/create")
	return cl.postDraft(ctx, "drafts.create", form)
}

func (cl *Client) DraftUpdate(ctx context.Context, id, lastUpdatedTS, channelID, threadTS, text string) (Draft, error) {
	form, err := draftMutationForm(channelID, threadTS, text)
	if err != nil {
		return Draft{}, err
	}
	form.Set("token", cl.token)
	form.Set("draft_id", id)
	form.Set("client_last_updated_ts", padDraftTimestamp(lastUpdatedTS))
	addDraftWebClientFields(form, "drafts/update")
	return cl.postDraft(ctx, "drafts.update", form)
}

func (cl *Client) DraftDelete(ctx context.Context, id, lastUpdatedTS string) error {
	form := url.Values{
		"token":                  {cl.token},
		"draft_id":               {id},
		"client_last_updated_ts": {padDraftTimestamp(lastUpdatedTS)},
	}
	addDraftWebClientFields(form, "drafts/delete")
	response, err := cl.PostForm(ctx, "drafts.delete", form)
	if err != nil {
		return err
	}
	var parsed baseResponse
	if err := cl.ParseResponse(&parsed, response); err != nil {
		return err
	}
	return parsed.validate("drafts.delete")
}

func (cl *Client) postDraft(ctx context.Context, method string, form url.Values) (Draft, error) {
	response, err := cl.PostForm(ctx, method, form)
	if err != nil {
		return Draft{}, err
	}
	var parsed draftsResponse
	if err := cl.ParseResponse(&parsed, response); err != nil {
		return Draft{}, err
	}
	if err := parsed.validate(method); err != nil {
		return Draft{}, err
	}
	return parsed.Draft, nil
}

func draftMutationForm(channelID, threadTS, text string) (url.Values, error) {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(text) == "" {
		return nil, errors.New("channel ID and text are required")
	}
	destination := DraftDestination{ChannelID: channelID, ThreadTS: threadTS}
	blocks := []map[string]any{{
		"type": "rich_text",
		"elements": []map[string]any{{
			"type":     "rich_text_section",
			"elements": []map[string]any{{"type": "text", "text": text}},
		}},
	}}
	blocksJSON, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	destinationsJSON, err := json.Marshal([]DraftDestination{destination})
	if err != nil {
		return nil, err
	}
	fileIDsJSON, err := json.Marshal([]string{})
	if err != nil {
		return nil, err
	}
	return url.Values{
		"blocks":        {string(blocksJSON)},
		"destinations":  {string(destinationsJSON)},
		"client_msg_id": {newDraftClientMessageID()},
		"file_ids":      {string(fileIDsJSON)},
	}, nil
}

func addDraftWebClientFields(form url.Values, reason string) {
	fields := webclientReason(reason)
	form.Set("_x_reason", fields.XReason)
	form.Set("_x_mode", fields.XMode)
	form.Set("_x_sonic", "true")
	form.Set("_x_app_name", fields.XAppName)
}

func newDraftClientMessageID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "slack-mcp-draft"
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func padDraftTimestamp(timestamp string) string {
	parts := strings.SplitN(timestamp, ".", 2)
	if len(parts) != 2 {
		return timestamp
	}
	return parts[0] + "." + (parts[1] + "0000000")[:7]
}

func intString(value int) string {
	if value <= 0 {
		return "50"
	}
	const digits = "0123456789"
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = digits[value%10]
		value /= 10
	}
	return string(buf[i:])
}
