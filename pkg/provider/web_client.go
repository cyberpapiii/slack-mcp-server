package provider

import (
	"bytes"
	"context"
	"io"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/korotovsky/slack-mcp-server/pkg/slackhttp"
	"github.com/slack-go/slack"
)

// WebClient is the Slack Web API surface this server calls, bound to whichever
// *slack.Client is current rather than to one instant's copy of it.
//
// Managed OAuth rotation replaces the underlying client instead of mutating it,
// so a caller holding a *slack.Client holds the access token that client was
// built with, and fails with token_expired once it expires. Every method here
// resolves the client at call time, so storing a *WebClient for the life of
// the process is safe. conversations.history, conversations.replies and
// search.messages retry a 429 under limiter.Retries; the last two also wait
// on their bucket in limiter.SlackPacing.
//
// The hand-built calls slack-go does not wrap (users.profile.set,
// canvases.getContent, assistant.search.context) live here too, reading the
// current token the same way.
type WebClient struct {
	client *MCPSlackClient
}

func (c *WebClient) resolve() *slack.Client {
	return c.client.standardSlackClient()
}

func (c *WebClient) token() string {
	token, _ := c.client.token.Load().(string)
	return token
}

// post sends a hand-built call with the current token through the primary
// HTTP client, for the methods slack-go does not wrap.
func (c *WebClient) post(ctx context.Context, req slackhttp.Request) ([]byte, error) {
	req.Token = c.token()
	return slackhttp.Post(ctx, c.client.httpClient, req)
}

func (c *WebClient) GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	return limiter.Paced(ctx, c.client.pacing, "conversations.history", nil, func() (*slack.GetConversationHistoryResponse, error) {
		return c.resolve().GetConversationHistoryContext(ctx, params)
	})
}

type repliesPage struct {
	messages []slack.Message
	hasMore  bool
	cursor   string
}

func (c *WebClient) GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	page, err := limiter.Paced(ctx, c.client.pacing, "conversations.replies", nil, func() (repliesPage, error) {
		messages, hasMore, cursor, err := c.resolve().GetConversationRepliesContext(ctx, params)
		return repliesPage{messages, hasMore, cursor}, err
	})
	return page.messages, page.hasMore, page.cursor, err
}

func (c *WebClient) GetConversationInfoContext(ctx context.Context, input *slack.GetConversationInfoInput) (*slack.Channel, error) {
	return c.resolve().GetConversationInfoContext(ctx, input)
}

func (c *WebClient) GetConversationsForUserContext(ctx context.Context, params *slack.GetConversationsForUserParameters) ([]slack.Channel, string, error) {
	return c.resolve().GetConversationsForUserContext(ctx, params)
}

func (c *WebClient) JoinConversationContext(ctx context.Context, channelID string) (*slack.Channel, string, []string, error) {
	return c.resolve().JoinConversationContext(ctx, channelID)
}

func (c *WebClient) OpenConversationContext(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
	return c.resolve().OpenConversationContext(ctx, params)
}

func (c *WebClient) RenameConversationContext(ctx context.Context, channelID, name string) (*slack.Channel, error) {
	return c.resolve().RenameConversationContext(ctx, channelID, name)
}

func (c *WebClient) SetTopicOfConversationContext(ctx context.Context, channelID, topic string) (*slack.Channel, error) {
	return c.resolve().SetTopicOfConversationContext(ctx, channelID, topic)
}

func (c *WebClient) SetPurposeOfConversationContext(ctx context.Context, channelID, purpose string) (*slack.Channel, error) {
	return c.resolve().SetPurposeOfConversationContext(ctx, channelID, purpose)
}

func (c *WebClient) ArchiveConversationContext(ctx context.Context, channelID string) error {
	return c.resolve().ArchiveConversationContext(ctx, channelID)
}

func (c *WebClient) PostMessageContext(ctx context.Context, channelID string, options ...slack.MsgOption) (string, string, error) {
	return c.resolve().PostMessageContext(ctx, channelID, options...)
}

func (c *WebClient) UpdateMessageContext(ctx context.Context, channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error) {
	return c.resolve().UpdateMessageContext(ctx, channelID, timestamp, options...)
}

func (c *WebClient) DeleteMessageContext(ctx context.Context, channelID, timestamp string) (string, string, error) {
	return c.resolve().DeleteMessageContext(ctx, channelID, timestamp)
}

func (c *WebClient) ScheduleMessageContext(ctx context.Context, channelID, postAt string, options ...slack.MsgOption) (string, string, error) {
	return c.resolve().ScheduleMessageContext(ctx, channelID, postAt, options...)
}

func (c *WebClient) AddReactionContext(ctx context.Context, name string, item slack.ItemRef) error {
	return c.resolve().AddReactionContext(ctx, name, item)
}

func (c *WebClient) RemoveReactionContext(ctx context.Context, name string, item slack.ItemRef) error {
	return c.resolve().RemoveReactionContext(ctx, name, item)
}

type searchPage struct {
	messages *slack.SearchMessages
	files    *slack.SearchFiles
}

func (c *WebClient) SearchContext(ctx context.Context, query string, params slack.SearchParameters) (*slack.SearchMessages, *slack.SearchFiles, error) {
	page, err := limiter.Paced(ctx, c.client.pacing, "search.messages", nil, func() (searchPage, error) {
		messages, files, err := c.resolve().SearchContext(ctx, query, params)
		return searchPage{messages, files}, err
	})
	return page.messages, page.files, err
}

func (c *WebClient) GetFilesContext(ctx context.Context, params slack.GetFilesParameters) ([]slack.File, *slack.Paging, error) {
	return c.resolve().GetFilesContext(ctx, params)
}

func (c *WebClient) GetFileInfoContext(ctx context.Context, fileID string, count, page int) (*slack.File, []slack.Comment, *slack.Paging, error) {
	return c.resolve().GetFileInfoContext(ctx, fileID, count, page)
}

func (c *WebClient) GetFileContext(ctx context.Context, downloadURL string, writer io.Writer) error {
	return c.resolve().GetFileContext(ctx, downloadURL, writer)
}

func (c *WebClient) GetUploadURLExternalContext(ctx context.Context, params slack.GetUploadURLExternalParameters) (*slack.GetUploadURLExternalResponse, error) {
	return c.resolve().GetUploadURLExternalContext(ctx, params)
}

func (c *WebClient) CompleteUploadExternalContext(ctx context.Context, params slack.CompleteUploadExternalParameters) (*slack.CompleteUploadExternalResponse, error) {
	return c.resolve().CompleteUploadExternalContext(ctx, params)
}

func (c *WebClient) UploadExternalBytes(ctx context.Context, uploadURL, filename string, data []byte) error {
	return c.resolve().UploadToURL(ctx, slack.UploadToURLParameters{
		UploadURL: uploadURL,
		Filename:  filename,
		Reader:    bytes.NewReader(data),
	})
}

func (c *WebClient) GetUserGroupsContext(ctx context.Context, options ...slack.GetUserGroupsOption) ([]slack.UserGroup, error) {
	return c.resolve().GetUserGroupsContext(ctx, options...)
}

func (c *WebClient) GetUserGroupMembersContext(ctx context.Context, usergroupID string, options ...slack.GetUserGroupMembersOption) ([]string, error) {
	return c.resolve().GetUserGroupMembersContext(ctx, usergroupID, options...)
}

func (c *WebClient) CreateUserGroupContext(ctx context.Context, group slack.UserGroup, options ...slack.CreateUserGroupOption) (slack.UserGroup, error) {
	return c.resolve().CreateUserGroupContext(ctx, group, options...)
}

func (c *WebClient) UpdateUserGroupContext(ctx context.Context, usergroupID string, options ...slack.UpdateUserGroupsOption) (slack.UserGroup, error) {
	return c.resolve().UpdateUserGroupContext(ctx, usergroupID, options...)
}

func (c *WebClient) UpdateUserGroupMembersContext(ctx context.Context, usergroupID, members string, options ...slack.UpdateUserGroupMembersOption) (slack.UserGroup, error) {
	return c.resolve().UpdateUserGroupMembersContext(ctx, usergroupID, members, options...)
}
