package handler

import (
	"context"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// fakeSlack is the Slack side of the handlers for tests: the message source,
// the conversations surface and saved.list. A call a case did not set up
// reaches a nil embedded interface and panics.
type fakeSlack struct {
	ConversationsAPI

	// dir defaults to a warm, empty directory; coldDirectory() is one still
	// warming.
	dir *provider.Directory
	// messages answers GetMessage by channel and timestamp; a missing key is
	// no message.
	messages      map[[2]string]slack.Message
	getMessageErr error
	onGetMessage  func(channel, ts string)
	counts        provider.UnreadCounts
	muted         map[string]bool
	history       func(*slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
	// savedPages answers one SavedList call each, running onSavedPage after.
	savedPages  []provider.SavedPage
	savedCalls  int
	onSavedPage func(n int)
}

func coldDirectory() *provider.Directory { return (&provider.ApiProvider{}).Directory() }

func (f *fakeSlack) pipeline() MessagePipeline {
	return NewMessagePipeline(f, Availability{}, zap.NewNop())
}

func (f *fakeSlack) Directory() *provider.Directory {
	if f.dir == nil {
		return provider.NewDirectory(nil, nil)
	}
	return f.dir
}

func (f *fakeSlack) GetMessage(_ context.Context, channel, ts string) (*slack.Message, error) {
	if f.onGetMessage != nil {
		f.onGetMessage(channel, ts)
	}
	if f.getMessageErr != nil {
		return nil, f.getMessageErr
	}
	if msg, ok := f.messages[[2]string{channel, ts}]; ok {
		return &msg, nil
	}
	return nil, nil
}

func (f *fakeSlack) PatchUser(context.Context, string) (*slack.User, error) {
	return nil, slack.SlackErrorResponse{Err: "user_not_found"}
}

func (f *fakeSlack) PatchUsers(context.Context, []string) error { return nil }

func (f *fakeSlack) ForceRefreshChannels(context.Context) error { return nil }

func (f *fakeSlack) WorkspaceURL() string { return "https://example.slack.com/" }

func (f *fakeSlack) ClientCounts(context.Context) (provider.UnreadCounts, error) {
	return f.counts, nil
}

func (f *fakeSlack) GetMutedChannels(context.Context) (map[string]bool, error) { return f.muted, nil }

func (f *fakeSlack) GetConversationHistoryContext(_ context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	return f.history(params)
}

func (f *fakeSlack) SavedList(context.Context, string, int, string) (provider.SavedPage, error) {
	page := f.savedPages[f.savedCalls]
	f.savedCalls++
	if f.onSavedPage != nil {
		f.onSavedPage(f.savedCalls)
	}
	return page, nil
}

func (f *fakeSlack) SavedUpdate(context.Context, provider.SavedUpdate) error { panic("unexpected") }
func (f *fakeSlack) SavedAdd(context.Context, provider.SavedAdd) (provider.SavedItem, error) {
	panic("unexpected")
}
func (f *fakeSlack) SavedDelete(context.Context, provider.SavedRef) error { panic("unexpected") }
func (f *fakeSlack) SavedClearCompleted(context.Context) error            { panic("unexpected") }
