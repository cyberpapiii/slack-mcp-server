package provider

import (
	"bytes"
	"context"
	"io"

	"github.com/slack-go/slack"
)

// WebClient is the Slack Web API surface this server calls, bound to whichever
// *slack.Client is current rather than to one instant's copy of it.
//
// Managed OAuth rotation replaces the underlying client instead of mutating it,
// so a caller holding a *slack.Client holds the access token that client was
// built with. A handler that resolved the client inside each request never
// noticed. A provider constructed once at startup kept the original token until
// it expired, then failed with token_expired while the credential store was
// healthy and every read tool worked. That split cost a day of investigation.
//
// Every method here resolves the client at call time, so storing a *WebClient
// for the life of the process is safe by construction. That is the point of the
// type: the old accessor returned a snapshot that looked like a live handle,
// and nothing in the type system told the two apart.
type WebClient struct {
	resolve func() *slack.Client
}

func (c *WebClient) AuthTest() (*slack.AuthTestResponse, error) {
	return c.resolve().AuthTest()
}

func (c *WebClient) GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	return c.resolve().GetConversationHistoryContext(ctx, params)
}

func (c *WebClient) GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	return c.resolve().GetConversationRepliesContext(ctx, params)
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

func (c *WebClient) GetScheduledMessagesContext(ctx context.Context, params *slack.GetScheduledMessagesParameters) ([]slack.ScheduledMessage, string, error) {
	return c.resolve().GetScheduledMessagesContext(ctx, params)
}

func (c *WebClient) DeleteScheduledMessageContext(ctx context.Context, params *slack.DeleteScheduledMessageParameters) (bool, error) {
	return c.resolve().DeleteScheduledMessageContext(ctx, params)
}

func (c *WebClient) AddReactionContext(ctx context.Context, name string, item slack.ItemRef) error {
	return c.resolve().AddReactionContext(ctx, name, item)
}

func (c *WebClient) RemoveReactionContext(ctx context.Context, name string, item slack.ItemRef) error {
	return c.resolve().RemoveReactionContext(ctx, name, item)
}

func (c *WebClient) SearchContext(ctx context.Context, query string, params slack.SearchParameters) (*slack.SearchMessages, *slack.SearchFiles, error) {
	return c.resolve().SearchContext(ctx, query, params)
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

// UploadExternalBytes is the one method with a shape of its own rather than a
// straight forward: MessageFilesClient wants the upload expressed as bytes.
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
