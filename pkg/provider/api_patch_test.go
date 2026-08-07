package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

func (m *mockSlackClient) GetUsersInfo(users ...string) (*[]slack.User, error) {
	return m.usersInfoResult, m.usersInfoErr
}

// perUserSlackClient returns a distinct user for each requested user ID,
// so concurrent PatchUser calls can be distinguished in assertions.
type perUserSlackClient struct {
	SlackAPI
}

func (m *perUserSlackClient) GetUsersInfo(users ...string) (*[]slack.User, error) {
	id := users[0]
	result := []slack.User{{ID: id, Name: "user_" + id}}
	return &result, nil
}

func newTestApiProvider(client SlackAPI, snapshot *UsersCache) *ApiProvider {
	ap := &ApiProvider{
		client: client,
		logger: zap.NewNop(),
	}
	ap.usersSnapshot.Store(snapshot)
	return ap
}

// TestUnitPatchUser verifies the targeted single-user cache patch behavior.
func TestUnitPatchUser(t *testing.T) {
	t.Run("fetches and adds new user to snapshot", func(t *testing.T) {
		initial := &UsersCache{
			Users:    map[string]slack.User{"U001": {ID: "U001", Name: "alice"}},
			UsersInv: map[string]string{"alice": "U001"},
		}

		newUser := slack.User{ID: "U002", Name: "bob"}
		ap := newTestApiProvider(
			&mockSlackClient{usersInfoResult: &[]slack.User{newUser}},
			initial,
		)

		result, err := ap.PatchUser(context.Background(), "U002")
		require.NoError(t, err)
		assert.Equal(t, "U002", result.ID)
		assert.Equal(t, "bob", result.Name)

		snapshot := ap.usersSnapshot.Load()
		assert.Len(t, snapshot.Users, 2)
		assert.Equal(t, "bob", snapshot.Users["U002"].Name)
		assert.Equal(t, "U002", snapshot.UsersInv["bob"])
		assert.Equal(t, "alice", snapshot.Users["U001"].Name)
	})

	t.Run("API error leaves snapshot unchanged", func(t *testing.T) {
		initial := &UsersCache{
			Users:    map[string]slack.User{"U001": {ID: "U001", Name: "alice"}},
			UsersInv: map[string]string{"alice": "U001"},
		}

		ap := newTestApiProvider(
			&mockSlackClient{usersInfoErr: errors.New("slack API error")},
			initial,
		)

		result, err := ap.PatchUser(context.Background(), "U999")
		assert.Error(t, err)
		assert.Nil(t, result)

		snapshot := ap.usersSnapshot.Load()
		assert.Len(t, snapshot.Users, 1)
	})

	t.Run("empty API result returns not found", func(t *testing.T) {
		initial := &UsersCache{
			Users:    map[string]slack.User{},
			UsersInv: map[string]string{},
		}

		ap := newTestApiProvider(
			&mockSlackClient{usersInfoResult: &[]slack.User{}},
			initial,
		)

		result, err := ap.PatchUser(context.Background(), "U999")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("nil API result returns not found", func(t *testing.T) {
		initial := &UsersCache{
			Users:    map[string]slack.User{},
			UsersInv: map[string]string{},
		}

		ap := newTestApiProvider(
			&mockSlackClient{usersInfoResult: nil},
			initial,
		)

		result, err := ap.PatchUser(context.Background(), "U999")
		assert.Error(t, err)
		assert.Nil(t, result)
	})

	t.Run("does not mutate original snapshot", func(t *testing.T) {
		initial := &UsersCache{
			Users:    map[string]slack.User{"U001": {ID: "U001", Name: "alice"}},
			UsersInv: map[string]string{"alice": "U001"},
		}

		var snapshotRef atomic.Pointer[UsersCache]
		snapshotRef.Store(initial)

		newUser := slack.User{ID: "U002", Name: "bob"}
		ap := newTestApiProvider(
			&mockSlackClient{usersInfoResult: &[]slack.User{newUser}},
			initial,
		)

		_, err := ap.PatchUser(context.Background(), "U002")
		require.NoError(t, err)

		orig := snapshotRef.Load()
		_, hasNew := orig.Users["U002"]
		assert.False(t, hasNew, "original snapshot should not be mutated")
		assert.Len(t, orig.Users, 1)
	})

	t.Run("overwrites existing user with fresh data", func(t *testing.T) {
		initial := &UsersCache{
			Users:    map[string]slack.User{"U001": {ID: "U001", Name: "alice_old"}},
			UsersInv: map[string]string{"alice_old": "U001"},
		}

		updatedUser := slack.User{ID: "U001", Name: "alice_new"}
		ap := newTestApiProvider(
			&mockSlackClient{usersInfoResult: &[]slack.User{updatedUser}},
			initial,
		)

		result, err := ap.PatchUser(context.Background(), "U001")
		require.NoError(t, err)
		assert.Equal(t, "alice_new", result.Name)

		snapshot := ap.usersSnapshot.Load()
		assert.Equal(t, "alice_new", snapshot.Users["U001"].Name)
		assert.Equal(t, "U001", snapshot.UsersInv["alice_new"])
	})
}

// TestUnitPatchUserConcurrent verifies that concurrent PatchUser calls do not
// lose updates via the load->copy->store race that CompareAndSwap retry
// guards against.
func TestUnitPatchUserConcurrent(t *testing.T) {
	const seedCount = 5
	const concurrentPatches = 8

	initial := &UsersCache{
		Users:    make(map[string]slack.User, seedCount),
		UsersInv: make(map[string]string, seedCount),
	}
	for i := 0; i < seedCount; i++ {
		id := fmt.Sprintf("SEED%d", i)
		name := fmt.Sprintf("seed_%d", i)
		initial.Users[id] = slack.User{ID: id, Name: name}
		initial.UsersInv[name] = id
	}

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

	snapshot := ap.usersSnapshot.Load()
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
