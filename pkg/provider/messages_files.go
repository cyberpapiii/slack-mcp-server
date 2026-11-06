package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slack-go/slack"
)

// MessageFilesClient methods are called at most once per operation.
var _ MessageFilesClient = (*WebClient)(nil)

type MessageFilesClient interface {
	GetUploadURLExternalContext(context.Context, slack.GetUploadURLExternalParameters) (*slack.GetUploadURLExternalResponse, error)
	UploadExternalBytes(context.Context, string, string, []byte) error
	CompleteUploadExternalContext(context.Context, slack.CompleteUploadExternalParameters) (*slack.CompleteUploadExternalResponse, error)
	ScheduleMessageContext(context.Context, string, string, ...slack.MsgOption) (string, string, error)
	UpdateMessageContext(context.Context, string, string, ...slack.MsgOption) (string, string, string, error)
	DeleteMessageContext(context.Context, string, string) (string, string, error)
	GetConversationRepliesContext(context.Context, *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error)
}

type FileUploadRequest struct {
	Filename       string
	Title          string
	Data           []byte
	ChannelID      string
	InitialComment string
	ThreadTS       string
	AltText        string
	SnippetType    string
}

type UploadedFile struct {
	FileID    string `json:"file_id"`
	Filename  string `json:"filename"`
	Title     string `json:"title,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
}

type MessageMutation struct {
	ChannelID          string `json:"channel_id"`
	Timestamp          string `json:"timestamp,omitempty"`
	ScheduledMessageID string `json:"scheduled_message_id,omitempty"`
}

type MessageFilesProvider struct{ client MessageFilesClient }

func NewMessageFilesProvider(client MessageFilesClient) *MessageFilesProvider {
	return &MessageFilesProvider{client: client}
}

func (ap *ApiProvider) MessageFiles() *MessageFilesProvider {
	return NewMessageFilesProvider(ap.WebAPI())
}

func (p *MessageFilesProvider) Upload(ctx context.Context, request FileUploadRequest) (UploadedFile, error) {
	urlResponse, err := p.client.GetUploadURLExternalContext(ctx, slack.GetUploadURLExternalParameters{
		FileName:    request.Filename,
		FileSize:    len(request.Data),
		AltTxt:      request.AltText,
		SnippetType: request.SnippetType,
	})
	if err != nil {
		return UploadedFile{}, fmt.Errorf("get external upload URL: %w", err)
	}
	if urlResponse == nil || urlResponse.UploadURL == "" || urlResponse.FileID == "" {
		return UploadedFile{}, errors.New("get external upload URL: Slack returned an incomplete response")
	}
	if err := p.client.UploadExternalBytes(ctx, urlResponse.UploadURL, request.Filename, request.Data); err != nil {
		return UploadedFile{}, fmt.Errorf("upload file bytes: %w", err)
	}
	title := request.Title
	if title == "" {
		title = request.Filename
	}
	completed, err := p.client.CompleteUploadExternalContext(ctx, slack.CompleteUploadExternalParameters{
		Files:           []slack.FileSummary{{ID: urlResponse.FileID, Title: title}},
		Channel:         request.ChannelID,
		InitialComment:  request.InitialComment,
		ThreadTimestamp: request.ThreadTS,
	})
	if err != nil {
		return UploadedFile{}, fmt.Errorf("complete external upload: %w", err)
	}
	if completed == nil || len(completed.Files) == 0 {
		return UploadedFile{}, errors.New("complete external upload: Slack returned no files")
	}
	return UploadedFile{FileID: completed.Files[0].ID, Filename: request.Filename, Title: completed.Files[0].Title, ChannelID: request.ChannelID}, nil
}

func (p *MessageFilesProvider) Schedule(ctx context.Context, channelID, text string, postAt time.Time, threadTS string, extra ...slack.MsgOption) (MessageMutation, error) {
	options := append([]slack.MsgOption{slack.MsgOptionText(text, false)}, extra...)
	if threadTS != "" {
		options = append(options, slack.MsgOptionTS(threadTS))
	}
	channel, id, err := p.client.ScheduleMessageContext(ctx, channelID, fmt.Sprintf("%d", postAt.Unix()), options...)
	if err != nil {
		return MessageMutation{}, err
	}
	return MessageMutation{ChannelID: channel, ScheduledMessageID: id}, nil
}

func (p *MessageFilesProvider) Update(ctx context.Context, channelID, timestamp, text string) (MessageMutation, error) {
	channel, ts, _, err := p.client.UpdateMessageContext(ctx, channelID, timestamp, slack.MsgOptionText(text, false))
	if err != nil {
		return MessageMutation{}, err
	}
	return MessageMutation{ChannelID: channel, Timestamp: ts}, nil
}

func (p *MessageFilesProvider) Delete(ctx context.Context, channelID, timestamp string) (MessageMutation, error) {
	channel, ts, err := p.client.DeleteMessageContext(ctx, channelID, timestamp)
	if err != nil {
		return MessageMutation{}, err
	}
	return MessageMutation{ChannelID: channel, Timestamp: ts}, nil
}

// GetMessage returns the message at channelID and timestamp, or
// message_not_found (IsMessageNotFound) when Slack has none there.
func (p *MessageFilesProvider) GetMessage(ctx context.Context, channelID, timestamp string) (slack.Message, error) {
	return fetchMessage(ctx, p.client, channelID, timestamp)
}

// GetMessage returns the message at channelID and timestamp, or
// message_not_found (IsMessageNotFound) when Slack has none there.
func (c *WebClient) GetMessage(ctx context.Context, channelID, timestamp string) (slack.Message, error) {
	return fetchMessage(ctx, c, channelID, timestamp)
}

type repliesReader interface {
	GetConversationRepliesContext(context.Context, *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error)
}

// fetchMessage reads one message through conversations.replies, which answers
// for top-level messages, thread parents and thread replies alike and needs
// only channels:history. Slack can answer with a different message than the
// one asked for (the thread parent, or the next message after a timestamp
// that matches nothing), so the first message counts only when its timestamp
// matches; otherwise the message is not found.
func fetchMessage(ctx context.Context, client repliesReader, channelID, timestamp string) (slack.Message, error) {
	msgs, _, _, err := client.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channelID,
		Timestamp: timestamp,
		Limit:     1,
		Inclusive: true,
	})
	if err != nil {
		return slack.Message{}, err
	}
	if len(msgs) == 0 || msgs[0].Timestamp != timestamp {
		return slack.Message{}, errMessageNotFound
	}
	return msgs[0], nil
}
