package provider

import (
	"context"
	"errors"
	"fmt"
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

// Connect enrichment is best-effort: ClientUserBoot failure must not leave
// usersReady false or skip caching the standard user list.
func TestUnitFetchAndStoreUsersSurvivesConnectFailure(t *testing.T) {
	users := []slack.User{
		{ID: "U001", Name: "alice"},
		{ID: "U002", Name: "bob"},
	}

	ap := &ApiProvider{
		api:            &fakeConnectFailureClient{users: users},
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

// connectBootClient answers client.userBoot with shared and unshared DMs and
// records each users.info call.
type connectBootClient struct {
	SlackAPI

	boot  *edge.ClientUserBootResponse
	calls [][]string
}

func (c *connectBootClient) ClientUserBoot(context.Context) (*edge.ClientUserBootResponse, error) {
	return c.boot, nil
}

func (c *connectBootClient) GetUsersInfoContext(_ context.Context, ids ...string) (*[]slack.User, error) {
	c.calls = append(c.calls, ids)
	users := make([]slack.User, len(ids))
	for i, id := range ids {
		users[i] = slack.User{ID: id}
	}
	return &users, nil
}

func sharedIM(user string, shared bool) edge.Channel {
	var ch edge.Channel
	ch.IsIM, ch.User, ch.IsExtShared = true, user, shared
	return ch
}

// Slack Connect enrichment used to check the DMs against the previous users
// snapshot, so a cold start asked users.info for every listed user too.
func TestUnitSlackConnectUsersAsksOnlyForPeopleTheListLacked(t *testing.T) {
	client := &connectBootClient{boot: &edge.ClientUserBootResponse{IMs: []edge.Channel{
		sharedIM("U1", true), sharedIM("U2", true), sharedIM("U2", true), sharedIM("U3", false), sharedIM("", true),
	}}}
	ap := &ApiProvider{api: client, logger: zap.NewNop()}
	ap.usersSnapshot.Store(newUsersCache(nil))

	users, err := ap.slackConnectUsers(context.Background(), []slack.User{{ID: "U1"}})
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"U2"}}, client.calls)
	require.Len(t, users, 1)
	assert.Equal(t, "U2", users[0].ID)

	client.boot = nil
	users, err = ap.slackConnectUsers(context.Background(), nil)
	require.NoError(t, err, "an empty boot answer is not a crash")
	assert.Empty(t, users)
}

func TestUnitSlackConnectUsersBatchesUsersInfo(t *testing.T) {
	ims := make([]edge.Channel, usersInfoBatch+1)
	for i := range ims {
		ims[i] = sharedIM(fmt.Sprintf("U%03d", i), true)
	}
	client := &connectBootClient{boot: &edge.ClientUserBootResponse{IMs: ims}}
	ap := &ApiProvider{api: client, logger: zap.NewNop()}

	users, err := ap.slackConnectUsers(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, users, usersInfoBatch+1)
	require.Len(t, client.calls, 2)
	assert.Len(t, client.calls[0], usersInfoBatch)
}
