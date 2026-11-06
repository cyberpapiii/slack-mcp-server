package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockSlackClient implements just enough of SlackAPI for PatchUser tests.
type mockSlackClient struct {
	SlackAPI // embed interface to satisfy all methods; only override what we need

	usersInfoResult *[]slack.User
	usersInfoErr    error
}

func (m *mockSlackClient) GetUsersInfoContext(_ context.Context, users ...string) (*[]slack.User, error) {
	return m.usersInfoResult, m.usersInfoErr
}

// perUserSlackClient returns a distinct user for each requested user ID,
// so concurrent PatchUser calls can be distinguished in assertions.
type perUserSlackClient struct {
	SlackAPI
}

func (m *perUserSlackClient) GetUsersInfoContext(_ context.Context, users ...string) (*[]slack.User, error) {
	id := users[0]
	result := []slack.User{{ID: id, Name: "user_" + id}}
	return &result, nil
}

func newTestApiProvider(client SlackAPI, snapshot *UsersCache) *ApiProvider {
	ap := &ApiProvider{api: client, logger: zap.NewNop(), ctx: context.Background()}
	ap.usersSnapshot.Store(snapshot)
	return ap
}

func TestUnitPatchUser(t *testing.T) {
	t.Run("fetches and adds new user to snapshot", func(t *testing.T) {
		initial := newUsersCache([]slack.User{{ID: "U001", Name: "alice"}})

		newUser := slack.User{ID: "U002", Name: "bob"}
		ap := newTestApiProvider(
			&mockSlackClient{usersInfoResult: &[]slack.User{newUser}},
			initial,
		)

		result, err := ap.PatchUser(context.Background(), "U002")
		require.NoError(t, err)
		assert.Equal(t, "U002", result.ID)
		assert.Equal(t, "bob", result.Name)

		snapshot := ap.currentUsers()
		assert.Len(t, snapshot.Users, 2)
		assert.Equal(t, "bob", snapshot.Users["U002"].Name)
		assert.Equal(t, "U002", snapshot.UsersInv["bob"])
		assert.Equal(t, "alice", snapshot.Users["U001"].Name)
	})

	t.Run("API error leaves snapshot unchanged", func(t *testing.T) {
		initial := newUsersCache([]slack.User{{ID: "U001", Name: "alice"}})

		ap := newTestApiProvider(
			&mockSlackClient{usersInfoErr: errors.New("slack API error")},
			initial,
		)

		result, err := ap.PatchUser(context.Background(), "U999")
		assert.Error(t, err)
		assert.Nil(t, result)

		snapshot := ap.currentUsers()
		assert.Len(t, snapshot.Users, 1)
	})

	for name, result := range map[string]*[]slack.User{"empty API result": {}, "nil API result": nil} {
		t.Run(name+" returns not found", func(t *testing.T) {
			ap := newTestApiProvider(&mockSlackClient{usersInfoResult: result}, newUsersCache(nil))

			user, err := ap.PatchUser(context.Background(), "U999")
			require.Error(t, err)
			assert.Nil(t, user)
			assert.Contains(t, err.Error(), "not found")
		})
	}

	t.Run("does not mutate original snapshot", func(t *testing.T) {
		initial := newUsersCache([]slack.User{{ID: "U001", Name: "alice"}})

		newUser := slack.User{ID: "U002", Name: "bob"}
		ap := newTestApiProvider(
			&mockSlackClient{usersInfoResult: &[]slack.User{newUser}},
			initial,
		)

		_, err := ap.PatchUser(context.Background(), "U002")
		require.NoError(t, err)

		_, hasNew := initial.Users["U002"]
		assert.False(t, hasNew, "original snapshot should not be mutated")
		assert.Len(t, initial.Users, 1)
	})

	t.Run("overwrites existing user with fresh data", func(t *testing.T) {
		initial := newUsersCache([]slack.User{{ID: "U001", Name: "alice_old"}})

		updatedUser := slack.User{ID: "U001", Name: "alice_new"}
		ap := newTestApiProvider(
			&mockSlackClient{usersInfoResult: &[]slack.User{updatedUser}},
			initial,
		)

		result, err := ap.PatchUser(context.Background(), "U001")
		require.NoError(t, err)
		assert.Equal(t, "alice_new", result.Name)

		snapshot := ap.currentUsers()
		assert.Equal(t, "alice_new", snapshot.Users["U001"].Name)
		assert.Equal(t, "U001", snapshot.UsersInv["alice_new"])
	})
}

// Concurrent PatchUser must not drop updates (CAS retry on snapshot store).
func TestUnitPatchUserConcurrent(t *testing.T) {
	const seedCount = 5
	const concurrentPatches = 8

	seed := make([]slack.User, 0, seedCount)
	for i := 0; i < seedCount; i++ {
		seed = append(seed, slack.User{ID: fmt.Sprintf("SEED%d", i), Name: fmt.Sprintf("seed_%d", i)})
	}
	initial := newUsersCache(seed)

	ap := newTestApiProvider(&perUserSlackClient{}, initial)

	var wg sync.WaitGroup
	for i := 0; i < concurrentPatches; i++ {
		id := fmt.Sprintf("U%03d", i)
		wg.Add(1)
		go func(userID string) {
			defer wg.Done()
			_, err := ap.PatchUser(context.Background(), userID)
			assert.NoError(t, err)
		}(id)
	}
	wg.Wait()

	snapshot := ap.currentUsers()
	assert.Len(t, snapshot.Users, seedCount+concurrentPatches)
	assert.Len(t, snapshot.UsersInv, seedCount+concurrentPatches)

	for i := 0; i < seedCount; i++ {
		id := fmt.Sprintf("SEED%d", i)
		name := fmt.Sprintf("seed_%d", i)
		assert.Equal(t, name, snapshot.Users[id].Name, "seed user %s should survive concurrent patches", id)
		assert.Equal(t, id, snapshot.UsersInv[name])
	}

	for i := 0; i < concurrentPatches; i++ {
		id := fmt.Sprintf("U%03d", i)
		expectedName := "user_" + id
		assert.Equal(t, expectedName, snapshot.Users[id].Name, "patched user %s should be present", id)
		assert.Equal(t, id, snapshot.UsersInv[expectedName])
	}
}
