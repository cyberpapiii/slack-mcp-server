package edge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/slack-go/slack"
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
	Blocks        slack.Blocks       `json:"blocks,omitempty"`
	Destinations  []DraftDestination `json:"destinations,omitempty"`
	LastUpdatedTS string             `json:"last_updated_ts,omitempty"`
	DateCreated   int64              `json:"date_created,omitempty"`
	DateScheduled int64              `json:"date_scheduled,omitempty"`
	FileIDs       []string           `json:"file_ids,omitempty"`
}

type draftsResponse struct {
	Draft  Draft   `json:"draft"`
	Drafts []Draft `json:"drafts"`
	NextTS string  `json:"next_ts,omitempty"`
}

// draftsListDefaultLimit is what DraftsList asks for when limit is not positive.
const draftsListDefaultLimit = 50

type draftsListForm struct {
	Limit    int    `json:"limit"`
	IsActive bool   `json:"is_active,omitempty"`
	NextTS   string `json:"next_ts,omitempty"`
	WebClientFields
}

func (cl *Client) DraftsList(ctx context.Context, limit int, activeOnly bool, cursor string) ([]Draft, string, error) {
	if limit <= 0 {
		limit = draftsListDefaultLimit
	}
	form := draftsListForm{
		Limit:           limit,
		IsActive:        activeOnly,
		WebClientFields: webclientReason("drafts/list"),
	}
	if strings.TrimSpace(cursor) != "" {
		form.NextTS = cursor
	}
	var parsed draftsResponse
	if err := cl.postForm(ctx, "drafts.list", form, &parsed); err != nil {
		return nil, "", err
	}
	return parsed.Drafts, parsed.NextTS, nil
}

// DraftContent is the body every draft mutation sends: blocks, destinations
// and file_ids are JSON strings inside the form.
type DraftContent struct {
	Blocks       string `json:"blocks"`
	Destinations string `json:"destinations"`
	ClientMsgID  string `json:"client_msg_id"`
	FileIDs      string `json:"file_ids"`
}

type draftCreateForm struct {
	DraftContent
	IsFromComposer bool `json:"is_from_composer"`
	WebClientFields
}

type draftUpdateForm struct {
	DraftContent
	DraftID             string `json:"draft_id"`
	ClientLastUpdatedTS string `json:"client_last_updated_ts"`
	WebClientFields
}

type draftDeleteForm struct {
	DraftID             string `json:"draft_id"`
	ClientLastUpdatedTS string `json:"client_last_updated_ts"`
	WebClientFields
}

func (cl *Client) DraftCreate(ctx context.Context, channelID, threadTS, text string) (Draft, error) {
	content, err := newDraftContent(channelID, threadTS, text)
	if err != nil {
		return Draft{}, err
	}
	form := draftCreateForm{
		DraftContent:    content,
		IsFromComposer:  true,
		WebClientFields: webclientReason("drafts/create"),
	}
	return cl.postDraft(ctx, "drafts.create", form)
}

func (cl *Client) DraftUpdate(ctx context.Context, id, lastUpdatedTS, channelID, threadTS, text string) (Draft, error) {
	content, err := newDraftContent(channelID, threadTS, text)
	if err != nil {
		return Draft{}, err
	}
	form := draftUpdateForm{
		DraftContent:        content,
		DraftID:             id,
		ClientLastUpdatedTS: padDraftTimestamp(lastUpdatedTS),
		WebClientFields:     webclientReason("drafts/update"),
	}
	return cl.postDraft(ctx, "drafts.update", form)
}

func (cl *Client) DraftDelete(ctx context.Context, id, lastUpdatedTS string) error {
	form := draftDeleteForm{
		DraftID:             id,
		ClientLastUpdatedTS: padDraftTimestamp(lastUpdatedTS),
		WebClientFields:     webclientReason("drafts/delete"),
	}
	return cl.postForm(ctx, "drafts.delete", form, nil)
}

func (cl *Client) postDraft(ctx context.Context, method string, form any) (Draft, error) {
	var parsed draftsResponse
	if err := cl.postForm(ctx, method, form, &parsed); err != nil {
		return Draft{}, err
	}
	return parsed.Draft, nil
}

func newDraftContent(channelID, threadTS, text string) (DraftContent, error) {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(text) == "" {
		return DraftContent{}, errors.New("channel ID and text are required")
	}
	blocks, err := RichTextJSON(text)
	if err != nil {
		return DraftContent{}, err
	}
	destinations, err := json.Marshal([]DraftDestination{{ChannelID: channelID, ThreadTS: threadTS}})
	if err != nil {
		return DraftContent{}, err
	}
	return DraftContent{
		Blocks:       blocks,
		Destinations: string(destinations),
		ClientMsgID:  newDraftClientMessageID(),
		FileIDs:      "[]",
	}, nil
}

// RichTextJSON is plain text as the JSON of a one-section rich_text block
// list, the body Slack's drafts and custom to-dos store.
func RichTextJSON(text string) (string, error) {
	blocks, err := json.Marshal([]slack.Block{
		slack.NewRichTextBlock("", slack.NewRichTextSection(slack.NewRichTextSectionTextElement(text, nil))),
	})
	return string(blocks), err
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
