package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type UsersCache struct {
	Users    map[string]slack.User `json:"users"`
	UsersInv map[string]string     `json:"users_inv"`
	search   []userSearchEntry
}

type userSearchEntry struct {
	id          string
	name        string
	realName    string
	displayName string
	email       string
}

// newUsersCache is the only way a snapshot is built, so its search index
// always holds exactly one entry per user, sorted by id. A user listed twice
// keeps its last entry, in the maps and the index alike.
func newUsersCache(users []slack.User) *UsersCache {
	snapshot := &UsersCache{
		Users:    make(map[string]slack.User, len(users)),
		UsersInv: make(map[string]string, len(users)),
	}
	for _, u := range users {
		snapshot.Users[u.ID] = u
		snapshot.UsersInv[u.Name] = u.ID
	}
	snapshot.search = make([]userSearchEntry, 0, len(snapshot.Users))
	for _, u := range snapshot.Users {
		snapshot.search = append(snapshot.search, newUserSearchEntry(u))
	}
	sort.Slice(snapshot.search, func(i, j int) bool { return snapshot.search[i].id < snapshot.search[j].id })
	return snapshot
}

// RefreshUsers loads the users cache file, or fetches the users when the file
// is missing or unreadable. An expired file is served while a background
// fetch replaces it.
func (ap *ApiProvider) RefreshUsers(ctx context.Context) error {
	ap.usersMu.Lock()
	if cached, expired, ok := loadCacheFile[slack.User](ap.usersCachePath, ap.cacheTTL, ap.logger); ok {
		ap.usersSnapshot.Store(newUsersCache(cached))
		ap.usersReady.Store(true)
		ap.usersMu.Unlock()
		if expired {
			ap.spawnBackgroundRefresh(&ap.usersFlight, "users", ap.fetchAndStoreUsers)
		}
		return nil
	}
	ap.usersMu.Unlock()
	return ap.usersFlight.do(ctx, ap.fetchAndStoreUsers)
}

// PatchUser merges one users.info result into the in-memory snapshot (no disk write).
func (ap *ApiProvider) PatchUser(ctx context.Context, userID string) (*slack.User, error) {
	if err := ap.PatchUsers(ctx, []string{userID}); err != nil {
		return nil, err
	}
	user, ok := ap.ProvideUsersMap().Users[userID]
	if !ok {
		return nil, errors.New("user not found")
	}
	return &user, nil
}

// usersInfoBatch caps how many ids ride on one users.info call. Slack accepts a
// comma-separated list; the cap keeps a single form value from growing without
// bound when a page carries hundreds of uncached authors.
const usersInfoBatch = 50

// PatchUsers merges a batched users.info result into the in-memory snapshot.
// Slack's users.info takes a comma-separated list, so a page of uncached
// authors costs one round trip and one snapshot rebuild per batch instead of
// one of each per user. Ids the API does not return are simply absent from the
// snapshot afterwards; the caller's per-user path still handles those.
func (ap *ApiProvider) PatchUsers(ctx context.Context, userIDs []string) error {
	var patched []slack.User
	var fetchErr error
	for start := 0; start < len(userIDs); start += usersInfoBatch {
		end := min(start+usersInfoBatch, len(userIDs))
		fetched, err := ap.fetchUsersInfo(ctx, userIDs[start:end]...)
		if err != nil {
			ap.logger.Debug("Failed to fetch users for cache patch",
				zap.Strings("user_ids", userIDs[start:end]), zap.Error(err))
			// Batches that already succeeded are still installed below, so a
			// late failure does not throw away round trips already spent.
			fetchErr = err
			break
		}
		patched = append(patched, fetched...)
	}
	if len(patched) == 0 {
		if fetchErr != nil {
			return fetchErr
		}
		return errors.New("users not found")
	}

	ap.applyUserPatch(patched)
	ap.logger.Debug("Patched users into cache",
		zap.Int("requested", len(userIDs)),
		zap.Int("patched", len(patched)))
	return fetchErr
}

func (ap *ApiProvider) fetchUsersInfo(ctx context.Context, userIDs ...string) ([]slack.User, error) {
	usersInfo, err := ap.api.GetUsersInfoContext(ctx, userIDs...)
	if err != nil {
		return nil, err
	}
	if usersInfo == nil {
		return nil, nil
	}
	return *usersInfo, nil
}

func (ap *ApiProvider) applyUserPatch(patched []slack.User) {
	patchedIDs := make(map[string]bool, len(patched))
	for _, user := range patched {
		patchedIDs[user.ID] = true
	}
	for {
		current := ap.usersSnapshot.Load()
		newSnapshot := &UsersCache{
			Users:    make(map[string]slack.User, len(current.Users)+len(patched)),
			UsersInv: make(map[string]string, len(current.UsersInv)+len(patched)),
		}
		for k, v := range current.Users {
			newSnapshot.Users[k] = v
		}
		for k, v := range current.UsersInv {
			if patchedIDs[v] {
				continue // drop stale name→ID before rename
			}
			newSnapshot.UsersInv[k] = v
		}
		for _, user := range patched {
			newSnapshot.Users[user.ID] = user
			newSnapshot.UsersInv[user.Name] = user.ID
		}
		newSnapshot.search = patchSearchIndex(current.search, patched)

		if ap.usersSnapshot.CompareAndSwap(current, newSnapshot) {
			return
		}
	}
}

// patchSearchIndex returns the search index current becomes once the patched
// users are in it. The index is sorted by id, so entries are binary-searched
// into a copy instead of folding and re-sorting every cached user: at 10k
// cached users the full rebuild cost ~7.7ms and ~11.5MB per patched user, and
// one history page can patch a dozen.
func patchSearchIndex(current []userSearchEntry, patched []slack.User) []userSearchEntry {
	kept := make([]userSearchEntry, len(current))
	copy(kept, current)

	var inserts []userSearchEntry
	seen := make(map[string]bool, len(patched))
	for _, user := range patched {
		if seen[user.ID] {
			continue // the maps dedupe on id; the index has to as well
		}
		seen[user.ID] = true

		entry := newUserSearchEntry(user)
		at := sort.Search(len(kept), func(i int) bool { return kept[i].id >= user.ID })
		if at < len(kept) && kept[at].id == user.ID {
			kept[at] = entry
			continue
		}
		inserts = append(inserts, entry)
	}
	if len(inserts) == 0 {
		return kept
	}
	sort.Slice(inserts, func(i, j int) bool { return inserts[i].id < inserts[j].id })

	index := make([]userSearchEntry, 0, len(kept)+len(inserts))
	i, j := 0, 0
	for i < len(kept) && j < len(inserts) {
		if kept[i].id < inserts[j].id {
			index = append(index, kept[i])
			i++
			continue
		}
		index = append(index, inserts[j])
		j++
	}
	index = append(index, kept[i:]...)
	return append(index, inserts[j:]...)
}

func (ap *ApiProvider) fetchAndStoreUsers(ctx context.Context) error {
	users, err := ap.api.GetUsersContext(ctx, slack.GetUsersOptionLimit(1000))
	if err != nil {
		ap.logger.Error("Failed to fetch users", zap.Error(err))
		return err
	}

	if len(users) == 0 {
		if ap.usersReady.Load() {
			ap.logger.Warn("API returned zero users, keeping existing cache")
			return nil
		}
		return errors.New("API returned zero users and no existing cache is available")
	}

	list := users

	if !ap.PrefersBrowser() {
		ap.logger.Debug("Skipping Slack Connect enrichment: no healthy browser session")
	} else {
		connectUsers, err := ap.slackConnectUsers(ctx, users)
		if err != nil {
			ap.logger.Warn("Slack Connect enrichment failed; continuing with standard user list",
				zap.Error(err))
		} else {
			list = append(list, connectUsers...)
		}
	}

	// Single publish after enrichment so concurrent PatchUser CAS is not
	// clobbered by an intermediate Store of the pre-Connect snapshot.
	ap.usersSnapshot.Store(newUsersCache(list))

	writeCacheFile(ap.usersCachePath, list, ap.logger)
	ap.usersReady.Store(true)

	return nil
}

// slackConnectUsers returns the people in the browser session's shared DMs
// that users.list did not return: Slack Connect users from other workspaces.
func (ap *ApiProvider) slackConnectUsers(ctx context.Context, listed []slack.User) ([]slack.User, error) {
	boot, err := ap.api.ClientUserBoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("client.userBoot: %w", err)
	}
	if boot == nil {
		return nil, nil
	}
	known := make(map[string]bool, len(listed))
	for _, u := range listed {
		known[u.ID] = true
	}
	var missing []string
	for _, im := range boot.IMs {
		if (!im.IsShared && !im.IsExtShared) || im.User == "" || known[im.User] {
			continue
		}
		known[im.User] = true
		missing = append(missing, im.User)
	}
	var users []slack.User
	for start := 0; start < len(missing); start += usersInfoBatch {
		batch, err := ap.fetchUsersInfo(ctx, missing[start:min(start+usersInfoBatch, len(missing))]...)
		if err != nil {
			return nil, fmt.Errorf("users.info for shared DMs: %w", err)
		}
		users = append(users, batch...)
	}
	return users, nil
}

func (ap *ApiProvider) ProvideUsersMap() *UsersCache {
	return ap.usersSnapshot.Load()
}

// SearchUsers looks an ID up with users.info. Otherwise it searches through the
// browser session when that is preferred, and the local cache on OAuth.
func (ap *ApiProvider) SearchUsers(ctx context.Context, query string, limit int) ([]slack.User, error) {
	if ClassifyID(query) == IDUser {
		return ap.fetchUsersInfo(ctx, query)
	}

	return preferBrowser(&ap.client,
		func() ([]slack.User, error) { return ap.api.UsersSearch(ctx, query, limit) },
		func() ([]slack.User, error) { return ap.searchUsersInCache(query, limit) })
}

// searchUsersInCache matches name, real name, display name and email; results
// are ordered by user ID so the same query always truncates the same way.
func (ap *ApiProvider) searchUsersInCache(query string, limit int) ([]slack.User, error) {
	if !ap.usersReady.Load() {
		return nil, ErrUsersNotReady
	}

	query = foldSearchText(query)

	usersCache := ap.usersSnapshot.Load()
	var results []slack.User
	for _, entry := range usersCache.search {
		if !entry.matches(query) {
			continue
		}
		user := usersCache.Users[entry.id]
		if user.Deleted {
			continue
		}
		results = append(results, user)
		if len(results) == limit {
			break
		}
	}
	return results, nil
}

func newUserSearchEntry(user slack.User) userSearchEntry {
	return userSearchEntry{
		id:          user.ID,
		name:        foldSearchText(user.Name),
		realName:    foldSearchText(user.RealName),
		displayName: foldSearchText(user.Profile.DisplayName),
		email:       foldSearchText(user.Profile.Email),
	}
}

func (entry userSearchEntry) matches(query string) bool {
	return strings.Contains(entry.name, query) ||
		strings.Contains(entry.realName, query) ||
		strings.Contains(entry.displayName, query) ||
		strings.Contains(entry.email, query)
}

// foldSearchText maps each rune to the smallest member of its Unicode simple
// fold cycle, so a substring match on folded text is case-insensitive.
func foldSearchText(value string) string {
	var folded strings.Builder
	folded.Grow(len(value))
	for _, r := range value {
		canonical := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		folded.WriteRune(canonical)
	}
	return folded.String()
}
