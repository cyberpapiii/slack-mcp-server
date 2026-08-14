package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slack-go/slack"
)

// MessageFilesClient is the narrow, testable Slack surface for outbound files
// and message mutations. Each method is called at most once per operation.
var _ MessageFilesClient = messageFilesWebClient{}

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
	ContentType    string
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

type MessageSnapshot struct {
	ChannelID string `json:"channel_id"`
	Timestamp string `json:"timestamp"`
	Text      string `json:"text"`
	UserID    string `json:"user_id,omitempty"`
}

type MessageFilesProvider struct{ client MessageFilesClient }

func NewMessageFilesProvider(client MessageFilesClient) *MessageFilesProvider {
	return &MessageFilesProvider{client: client}
}

type messageFilesWebClient struct {
	*slack.Client
}

func (c messageFilesWebClient) UploadExternalBytes(ctx context.Context, uploadURL, filename string, data []byte) error {
	return c.UploadToURL(ctx, slack.UploadToURLParameters{
		UploadURL: uploadURL,
		Filename:  filename,
		Reader:    bytes.NewReader(data),
	})
}

// MessageFiles returns the custom local provider. No official MCP dependency.
func (ap *ApiProvider) MessageFiles() (*MessageFilesProvider, error) {
	web := ap.WebAPI()
	if web == nil {
		return nil, errors.New("configured Slack client does not support file and message mutations")
	}
	return NewMessageFilesProvider(messageFilesWebClient{Client: web}), nil
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

func (p *MessageFilesProvider) Schedule(ctx context.Context, channelID, text string, postAt time.Time, threadTS string) (MessageMutation, error) {
	options := []slack.MsgOption{slack.MsgOptionText(text, false)}
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

func (p *MessageFilesProvider) GetMessage(ctx context.Context, channelID, timestamp string) (MessageSnapshot, error) {
	msgs, _, _, err := p.client.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channelID,
		Timestamp: timestamp,
		Limit:     1,
		Inclusive: true,
	})
	if err != nil {
		return MessageSnapshot{}, err
	}
	if len(msgs) == 0 || msgs[0].Timestamp != timestamp {
		return MessageSnapshot{}, errors.New("message_not_found")
	}
	message := msgs[0]
	return MessageSnapshot{ChannelID: channelID, Timestamp: message.Timestamp, Text: message.Text, UserID: message.User}, nil
}
