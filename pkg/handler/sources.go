package handler

import (
	"context"
	"io"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
)

// The interfaces below are the Slack and cache surface each handler family
// calls. The server satisfies them all from one *provider.ApiProvider; tests
// satisfy only what a case needs.

// MessageSource is what the message pipeline reads: one message by
// timestamp, the users and channels caches, and the fetches that fill a miss.
type MessageSource interface {
	GetMessage(ctx context.Context, channelID, timestamp string) (slack.Message, error)
	Directory() *provider.Directory
	PatchUser(ctx context.Context, userID string) (*slack.User, error)
	PatchUsers(ctx context.Context, userIDs []string) error
	ForceRefreshChannels(ctx context.Context) error
	WorkspaceURL() string
}

// ConversationsAPI is the Slack side of the conversations, reactions, files
// and users-search tools.
type ConversationsAPI interface {
	Directory() *provider.Directory
	PatchUser(ctx context.Context, userID string) (*slack.User, error)
	SearchUsers(ctx context.Context, query string, limit int) ([]slack.User, error)
	Identity() provider.ProviderIdentity
	// The membership calls keep the channels cache current.
	JoinConversation(ctx context.Context, channelID string) (*slack.Channel, error)
	LeaveConversation(ctx context.Context, channelID string) (notInChannel bool, err error)
	OpenConversation(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, error)

	historyFetcher
	GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error)
	SearchContext(ctx context.Context, query string, params slack.SearchParameters) (*slack.SearchMessages, *slack.SearchFiles, error)
	PostMessageContext(ctx context.Context, channelID string, options ...slack.MsgOption) (string, string, error)
	AddReactionContext(ctx context.Context, name string, item slack.ItemRef) error
	RemoveReactionContext(ctx context.Context, name string, item slack.ItemRef) error
	GetFilesContext(ctx context.Context, params slack.GetFilesParameters) ([]slack.File, *slack.Paging, error)
	GetFileInfoContext(ctx context.Context, fileID string, count, page int) (*slack.File, []slack.Comment, *slack.Paging, error)
	GetFileContext(ctx context.Context, downloadURL string, writer io.Writer) error

	MarkConversationContext(ctx context.Context, channel, ts string) error
	ClientCounts(ctx context.Context) (provider.UnreadCounts, error)
	GetMutedChannels(ctx context.Context) (map[string]bool, error)
}

// ChannelsAPI is the Slack side of channels_list, channels_me and
// channels_starred.
type ChannelsAPI interface {
	Directory() *provider.Directory
	ChannelsForUser(ctx context.Context, types []string, cursor string, limit int) ([]provider.Channel, string, error)
	GetStarredChannelIDs(ctx context.Context, limit int) ([]string, error)
	GetMutedChannels(ctx context.Context) (map[string]bool, error)
}

// SavedAPI is the Slack side of the saved_* tools.
type SavedAPI interface {
	SavedList(ctx context.Context, filter string, limit int, cursor string) (provider.SavedPage, error)
	SavedUpdate(ctx context.Context, update provider.SavedUpdate) error
	SavedAdd(ctx context.Context, add provider.SavedAdd) (provider.SavedItem, error)
	SavedDelete(ctx context.Context, ref provider.SavedRef) error
	SavedClearCompleted(ctx context.Context) error
}

// ActivityAPI is the Slack side of the activity_* tools.
type ActivityAPI interface {
	ActivityFeed(ctx context.Context, limit int) ([]provider.ActivityEntry, error)
	ActivityMarkRead(ctx context.Context, itemType, feedTs, key string) error
	GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error)
}

// AuthStatusSource is what slack_auth_status reports on.
type AuthStatusSource interface {
	UsersCacheReady() bool
	ChannelsCacheReady() bool
	BrowserFeaturesAvailable() bool
	BrowserDegradedReason() string
	BrowserCredentialSource() string
	Mode() provider.CredentialMode
	Identity() provider.ProviderIdentity
	OAuthCredentialStatus() provider.OAuthCredentialStatus
}
