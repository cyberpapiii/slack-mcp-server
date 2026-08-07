package provider

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeConnectFailureClient implements just enough of SlackAPI to exercise
// fetchAndStoreUsers: GetUsersContext succeeds, but ClientUserBoot (which
// backs GetSlackConnect) fails, mirroring what happens for OAuth tokens or a
// degraded browser session.
type fakeConnectFailureClient struct {
	SlackAPI // embed interface to satisfy all methods; only override what we need

	users []slack.User
}

func (f *fakeConnectFailureClient) GetUsersContext(ctx context.Context, options ...slack.GetUsersOption) ([]slack.User, error) {
	return f.users, nil
}

func (f *fakeConnectFailureClient) ClientUserBoot(ctx context.Context) (*edge.ClientUserBootResponse, error) {
	return nil, errors.New("client.userBoot: browser features unavailable")
}

// TestUnitFetchAndStoreUsersSurvivesConnectFailure is a regression test for
// the bug where a Slack Connect enrichment failure (GetSlackConnect ->
// ClientUserBoot error) caused fetchAndStoreUsers to hard-fail via
// `return err`, leaving usersReady permanently false and the users cache
// never built. The enrichment is additive and must now be best-effort: the
// standard user list should still be cached and readiness still set.
func TestUnitFetchAndStoreUsersSurvivesConnectFailure(t *testing.T) {
	users := []slack.User{
		{ID: "U001", Name: "alice"},
		{ID: "U002", Name: "bob"},
	}

	ap := &ApiProvider{
		client:         &fakeConnectFailureClient{users: users},
		logger:         zap.NewNop(),
		usersCachePath: filepath.Join(t.TempDir(), "users.json"),
	}

	err := ap.fetchAndStoreUsers(context.Background())
	require.NoError(t, err)
	assert.True(t, ap.usersReady.Load())

	snapshot := ap.usersSnapshot.Load()
	require.NotNil(t, snapshot)
	assert.Len(t, snapshot.Users, 2)
	assert.Equal(t, "alice", snapshot.Users["U001"].Name)
	assert.Equal(t, "bob", snapshot.Users["U002"].Name)
	assert.Equal(t, "U001", snapshot.UsersInv["alice"])
	assert.Equal(t, "U002", snapshot.UsersInv["bob"])
}
