package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rotatingFixture stands up two Slack endpoints and a client pointed at the
// first, so a test can swap the client the way managed OAuth rotation does and
// then assert which endpoint the next call reached.
type rotatingFixture struct {
	client   *MCPSlackClient
	provider *ApiProvider
	oldCalls *int
	newCalls *int
	rotate   func()
}

func newRotatingFixture(t *testing.T, body string) *rotatingFixture {
	t.Helper()
	oldCalls, newCalls := 0, 0
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oldCalls++
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(oldServer.Close)
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		newCalls++
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(newServer.Close)

	client := &MCPSlackClient{slackClient: slack.New("old", slack.OptionAPIURL(oldServer.URL+"/"))}
	return &rotatingFixture{
		client:   client,
		provider: &ApiProvider{client: client},
		oldCalls: &oldCalls,
		newCalls: &newCalls,
		rotate: func() {
			client.oauthClientMu.Lock()
			client.slackClient = slack.New("new", slack.OptionAPIURL(newServer.URL+"/"))
			client.oauthClientMu.Unlock()
		},
	}
}

// assertUsedRotatedClient is the whole point of every case below: a provider
// built before rotation must send the call to the client built after it.
func (f *rotatingFixture) assertUsedRotatedClient(t *testing.T) {
	t.Helper()
	assert.Zero(t, *f.oldCalls, "the pre-rotation client must not be used after rotation")
	assert.Equal(t, 1, *f.newCalls, "the call must reach the rotated client")
}

func TestUnitScheduledProviderFollowsClientRotation(t *testing.T) {
	f := newRotatingFixture(t, `{"ok":true,"scheduled_messages":[]}`)
	service, err := f.provider.Scheduled()
	require.NoError(t, err)

	f.rotate()

	_, err = service.ListScheduled(context.Background(), ScheduledListRequest{ChannelID: "C1"})
	require.NoError(t, err)
	f.assertUsedRotatedClient(t)
}

func TestUnitChannelMutationsProviderFollowsClientRotation(t *testing.T) {
	f := newRotatingFixture(t, `{"ok":true,"channel":{"id":"C1","name":"renamed"}}`)
	service, err := f.provider.ChannelMutations()
	require.NoError(t, err)

	f.rotate()

	_, err = service.client.RenameConversationContext(context.Background(), "C1", "renamed")
	require.NoError(t, err)
	f.assertUsedRotatedClient(t)
}

func TestUnitMessageFilesProviderFollowsClientRotation(t *testing.T) {
	f := newRotatingFixture(t, `{"ok":true,"channel":"C1","ts":"1.000001"}`)
	service, err := f.provider.MessageFiles()
	require.NoError(t, err)

	f.rotate()

	_, err = service.Update(context.Background(), "C1", "1.000001", "updated")
	require.NoError(t, err)
	f.assertUsedRotatedClient(t)
}

func TestUnitWebAPIFollowsClientRotation(t *testing.T) {
	f := newRotatingFixture(t, `{"ok":true,"usergroups":[]}`)
	// A handler that stores the result of WebAPI() at construction, which is what
	// NewUsergroupsHandler does.
	stored := f.provider.WebAPI()
	require.NotNil(t, stored)

	f.rotate()

	_, err := stored.GetUserGroupsContext(context.Background())
	require.NoError(t, err)
	f.assertUsedRotatedClient(t)
}
