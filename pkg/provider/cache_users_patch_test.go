package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingUsersInfoClient records every users.info call and the ids it carried,
// so a test can pin how many round trips a patch costs.
type countingUsersInfoClient struct {
	SlackAPI

	calls   int
	batches [][]string
	users   map[string]slack.User
	failAt  int // 1-based call number that returns an error; 0 never fails
}

func (c *countingUsersInfoClient) GetUsersInfo(ids ...string) (*[]slack.User, error) {
	c.calls++
	c.batches = append(c.batches, append([]string(nil), ids...))
	if c.failAt == c.calls {
		return nil, errors.New("slack unavailable")
	}

	found := make([]slack.User, 0, len(ids))
	for _, id := range ids {
		if u, ok := c.users[id]; ok {
			found = append(found, u)
		}
	}
	return &found, nil
}

func namedUser(id, name string) slack.User {
	u := slack.User{ID: id, Name: name, RealName: "Real " + name}
	u.Profile.DisplayName = "Display " + name
	u.Profile.Email = name + "@example.com"
	return u
}

// searchIDs returns the ids in the snapshot's search index, in index order.
func searchIDs(cache *UsersCache) []string {
	ids := make([]string, 0, len(cache.search))
	for _, entry := range cache.search {
		ids = append(ids, entry.id)
	}
	return ids
}

func TestUnitPatchUsersUsesOneRoundTripPerBatch(t *testing.T) {
	t.Run("many ids ride on a single users.info call", func(t *testing.T) {
		client := &countingUsersInfoClient{users: map[string]slack.User{}}
		var want []string
		for i := 0; i < 5; i++ {
			id := fmt.Sprintf("UNEW%d", i)
			client.users[id] = namedUser(id, fmt.Sprintf("guest%d", i))
			want = append(want, id)
		}
		ap := newTestApiProvider(client, newUsersCache([]slack.User{namedUser("U001", "alice")}))

		require.NoError(t, ap.PatchUsers(context.Background(), want))

		assert.Equal(t, 1, client.calls, "one batch must cost one round trip, not one per id")
		assert.Equal(t, want, client.batches[0])

		snapshot := ap.ProvideUsersMap()
		require.Len(t, snapshot.Users, 6)
		for _, id := range want {
			_, ok := snapshot.Users[id]
			assert.True(t, ok, "patched user %s must be in the snapshot", id)
		}
	})

	t.Run("ids past the batch cap split into further calls", func(t *testing.T) {
		client := &countingUsersInfoClient{users: map[string]slack.User{}}
		var ids []string
		for i := 0; i < usersInfoBatch*2+1; i++ {
			id := fmt.Sprintf("UB%04d", i)
			client.users[id] = namedUser(id, fmt.Sprintf("bulk%d", i))
			ids = append(ids, id)
		}
		ap := newTestApiProvider(client, newUsersCache(nil))

		require.NoError(t, ap.PatchUsers(context.Background(), ids))

		assert.Equal(t, 3, client.calls)
		assert.Len(t, client.batches[0], usersInfoBatch)
		assert.Len(t, client.batches[2], 1)
		assert.Len(t, ap.ProvideUsersMap().Users, len(ids))
	})

	t.Run("ids the API does not return are simply absent", func(t *testing.T) {
		client := &countingUsersInfoClient{users: map[string]slack.User{
			"UOK": namedUser("UOK", "present"),
		}}
		ap := newTestApiProvider(client, newUsersCache(nil))

		require.NoError(t, ap.PatchUsers(context.Background(), []string{"UOK", "UGONE"}))

		snapshot := ap.ProvideUsersMap()
		_, ok := snapshot.Users["UOK"]
		assert.True(t, ok)
		_, ok = snapshot.Users["UGONE"]
		assert.False(t, ok, "an id users.info omits must not be invented")
	})
}

// The search index is patched in place rather than rebuilt, so every patch has
// to leave it sorted by id with exactly one entry per cached user.
func TestUnitPatchKeepsSearchIndexConsistent(t *testing.T) {
	seed := []slack.User{
		namedUser("U003", "carol"), namedUser("U001", "alice"), namedUser("U005", "erin"),
	}

	t.Run("a new user is inserted in id order", func(t *testing.T) {
		client := &countingUsersInfoClient{users: map[string]slack.User{
			"U002": namedUser("U002", "bob"), "U009": namedUser("U009", "zoe"),
		}}
		ap := newTestApiProvider(client, newUsersCache(seed))

		require.NoError(t, ap.PatchUsers(context.Background(), []string{"U009", "U002"}))

		snapshot := ap.ProvideUsersMap()
		ids := searchIDs(snapshot)
		assert.Equal(t, []string{"U001", "U002", "U003", "U005", "U009"}, ids)
		assert.Len(t, ids, len(snapshot.Users), "one index entry per cached user")
		assert.True(t, sort.StringsAreSorted(ids))
	})

	t.Run("a renamed user replaces its entry and drops the stale handle", func(t *testing.T) {
		client := &countingUsersInfoClient{users: map[string]slack.User{
			"U003": namedUser("U003", "carol-renamed"),
		}}
		ap := newTestApiProvider(client, newUsersCache(seed))
		ap.usersReady.Store(true)

		_, err := ap.PatchUser(context.Background(), "U003")
		require.NoError(t, err)

		snapshot := ap.ProvideUsersMap()
		assert.Equal(t, []string{"U001", "U003", "U005"}, searchIDs(snapshot))
		assert.Equal(t, "U003", snapshot.UsersInv["carol-renamed"])
		_, stale := snapshot.UsersInv["carol"]
		assert.False(t, stale, "the pre-rename handle must not still resolve")

		hits, err := ap.searchUsersInCache("carol-renamed", 10)
		require.NoError(t, err)
		require.Len(t, hits, 1)
		assert.Equal(t, "U003", hits[0].ID)

		stalehits, err := ap.searchUsersInCache("Real carol@", 10)
		require.NoError(t, err)
		assert.Empty(t, stalehits, "the index must not still carry the pre-rename entry")
	})

	t.Run("a patched user is searchable by every indexed field", func(t *testing.T) {
		client := &countingUsersInfoClient{users: map[string]slack.User{
			"U007": namedUser("U007", "grace"), "U008": namedUser("U008", "heidi"),
		}}
		ap := newTestApiProvider(client, newUsersCache(seed))
		ap.usersReady.Store(true)

		require.NoError(t, ap.PatchUsers(context.Background(), []string{"U007", "U008"}))

		for _, query := range []string{"grace", "Real grace", "Display grace", "GRACE@EXAMPLE.COM"} {
			hits, err := ap.searchUsersInCache(query, 10)
			require.NoError(t, err, query)
			require.Len(t, hits, 1, query)
			assert.Equal(t, "U007", hits[0].ID, query)
		}
	})
}

// The incremental index must be indistinguishable from the one a full rebuild
// produces, since searchUsersInCache reads it directly.
func TestUnitPatchedSearchIndexMatchesFullRebuild(t *testing.T) {
	var seed []slack.User
	for _, id := range []string{"U005", "U001", "U009", "U003", "U007"} {
		seed = append(seed, namedUser(id, "seed"+id))
	}

	patched := []slack.User{
		namedUser("U004", "inserted-middle"),
		namedUser("U000", "inserted-first"),
		namedUser("U999", "inserted-last"),
		namedUser("U003", "replaced-existing"),
	}
	client := &countingUsersInfoClient{users: map[string]slack.User{}}
	var ids []string
	for _, u := range patched {
		client.users[u.ID] = u
		ids = append(ids, u.ID)
	}

	ap := newTestApiProvider(client, newUsersCache(seed))
	require.NoError(t, ap.PatchUsers(context.Background(), ids))
	incremental := ap.ProvideUsersMap()

	all := make([]slack.User, 0, len(incremental.Users))
	for _, u := range incremental.Users {
		all = append(all, u)
	}
	rebuilt := newUsersCache(all)

	assert.Equal(t, rebuilt.search, incremental.search,
		"an incrementally patched index must equal the one a full rebuild produces")
}

// A patch copies the two snapshot maps and splices the search index; it must
// not fold and re-sort every cached user. Guards the cost of the resolver path
// that renders message authors.
func BenchmarkPatchUser(b *testing.B) {
	const cached = 10000
	seed := make([]slack.User, cached)
	client := &countingUsersInfoClient{users: make(map[string]slack.User, cached)}
	for i := range seed {
		seed[i] = namedUser(fmt.Sprintf("U%07d", i), fmt.Sprintf("member%d", i))
	}
	ap := newTestApiProvider(client, newUsersCache(seed))

	ctx := context.Background()
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		id := fmt.Sprintf("UNEW%06d", i)
		client.users[id] = namedUser(id, fmt.Sprintf("guest%d", i))
		if _, err := ap.PatchUser(ctx, id); err != nil {
			b.Fatal(err)
		}
	}
}

// A late failure must not throw away the round trips that already succeeded:
// the users those batches returned belong in the snapshot regardless.
func TestUnitPatchUsersKeepsSucceededBatchesOnLaterFailure(t *testing.T) {
	client := &countingUsersInfoClient{users: map[string]slack.User{}, failAt: 2}
	var ids []string
	for i := 0; i < usersInfoBatch+3; i++ {
		id := fmt.Sprintf("UP%04d", i)
		client.users[id] = namedUser(id, fmt.Sprintf("person%d", i))
		ids = append(ids, id)
	}
	ap := newTestApiProvider(client, newUsersCache(nil))
	ap.usersReady.Store(true)

	err := ap.PatchUsers(context.Background(), ids)

	require.Error(t, err, "the failed batch must still be reported")
	snapshot := ap.ProvideUsersMap()
	assert.Len(t, snapshot.Users, usersInfoBatch, "the first batch must survive the second batch failing")
	assert.Len(t, snapshot.search, len(snapshot.Users))

	hits, searchErr := ap.searchUsersInCache("person0", 10)
	require.NoError(t, searchErr)
	require.NotEmpty(t, hits, "a user from the surviving batch must be searchable")
}
