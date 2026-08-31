package provider

import (
	"context"
	"errors"
	"regexp"
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

func newUsersCache(users []slack.User) *UsersCache {
	snapshot := &UsersCache{
		Users:    make(map[string]slack.User, len(users)),
		UsersInv: make(map[string]string, len(users)),
		search:   make([]userSearchEntry, 0, len(users)),
	}
	for _, u := range users {
		snapshot.Users[u.ID] = u
		snapshot.UsersInv[u.Name] = u.ID
		snapshot.search = append(snapshot.search, newUserSearchEntry(u))
	}
	sort.Slice(snapshot.search, func(i, j int) bool { return snapshot.search[i].id < snapshot.search[j].id })
	return snapshot
}

func (ap *ApiProvider) RefreshUsers(ctx context.Context) error {
	return ap.refreshUsersInternal(ctx, false)
}

// PatchUser merges one users.info result into the in-memory snapshot (no disk write).
func (ap *ApiProvider) PatchUser(ctx context.Context, userID string) (*slack.User, error) {
	fetched, err := ap.fetchUsersInfo(userID)
	if err != nil {
		ap.logger.Warn("Failed to fetch user for cache patch", zap.String("user_id", userID), zap.Error(err))
		return nil, err
	}
	if len(fetched) == 0 {
		ap.logger.Debug("User not found via API", zap.String("user_id", userID))
		return nil, errors.New("user not found")
	}

	user := fetched[0]
	ap.applyUserPatch([]slack.User{user})

	ap.logger.Debug("Patched user into cache",
		zap.String("user_id", user.ID),
		zap.String("user_name", user.Name))

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
		fetched, err := ap.fetchUsersInfo(userIDs[start:end]...)
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

func (ap *ApiProvider) fetchUsersInfo(userIDs ...string) ([]slack.User, error) {
	usersInfo, err := ap.client.GetUsersInfo(userIDs...)
	if err != nil {
		return nil, err
	}
	if usersInfo == nil {
		return nil, nil
	}
	return *usersInfo, nil
}

// applyUserPatch installs a snapshot holding every cached user plus patched,
// retrying the CAS if a concurrent patch or refresh won the race.
func (ap *ApiProvider) applyUserPatch(patched []slack.User) {
	for {
		current := ap.usersSnapshot.Load()
		usersLen, invLen := 0, 0
		if current != nil {
			usersLen = len(current.Users)
			invLen = len(current.UsersInv)
		}

		newSnapshot := &UsersCache{
			Users:    make(map[string]slack.User, usersLen+len(patched)),
			UsersInv: make(map[string]string, invLen+len(patched)),
		}
		if current != nil {
			patchedIDs := make(map[string]bool, len(patched))
			for _, user := range patched {
				patchedIDs[user.ID] = true
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
		}
		for _, user := range patched {
			newSnapshot.Users[user.ID] = user
			newSnapshot.UsersInv[user.Name] = user.ID
		}
		newSnapshot.search = patchSearchIndex(current, newSnapshot.Users, patched)

		if ap.usersSnapshot.CompareAndSwap(current, newSnapshot) {
			return
		}
	}
}

// patchSearchIndex returns the search index for a snapshot that differs from
// current only by the patched users. The index is sorted by id, so entries are
// binary-searched into a copy instead of folding and re-sorting every cached
// user: at 10k cached users the full rebuild cost ~7.7ms and ~11.5MB per
// patched user, and one history page can patch a dozen.
//
// A caller that hand-builds a UsersCache without a matching index (tests do)
// falls back to the full rebuild so the result is identical either way.
func patchSearchIndex(current *UsersCache, users map[string]slack.User, patched []slack.User) []userSearchEntry {
	if current == nil || len(current.search) != len(current.Users) {
		index := make([]userSearchEntry, 0, len(users))
		for id, indexedUser := range users {
			entry := newUserSearchEntry(indexedUser)
			entry.id = id
			index = append(index, entry)
		}
		sort.Slice(index, func(i, j int) bool { return index[i].id < index[j].id })
		return index
	}

	// Users already in the index are replaced where they sit. Only genuinely
	// new ids change the length, and they are merged in rather than appended
	// and re-sorted, so a patch never pays O(n log n) over the whole cache.
	kept := make([]userSearchEntry, len(current.search))
	copy(kept, current.search)

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

func (ap *ApiProvider) refreshUsersInternal(ctx context.Context, force bool) error {
	ap.usersMu.Lock()

	if !force {
		if cached, expired, ok := loadCacheFile[slack.User](ap.usersCachePath, ap.cacheTTL, ap.logger); ok {
			ap.usersSnapshot.Store(newUsersCache(cached))
			ap.usersReady.Store(true)
			ap.usersMu.Unlock()

			if expired {
				ap.spawnBackgroundRefresh(&ap.usersFlight, "users", ap.fetchAndStoreUsers)
			}
			return nil
		}
	}

	ap.usersMu.Unlock()
	return ap.usersFlight.do(ctx, ap.fetchAndStoreUsers)
}

func (ap *ApiProvider) fetchAndStoreUsers(ctx context.Context) error {
	users, err := ap.client.GetUsersContext(ctx, slack.GetUsersOptionLimit(1000))
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

	if ap.IsOAuth() {
		ap.logger.Debug("Skipping Slack Connect enrichment (OAuth token, browser features unavailable)")
	} else {
		connectUsers, err := ap.GetSlackConnect(ctx)
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

func (ap *ApiProvider) GetSlackConnect(ctx context.Context) ([]slack.User, error) {
	boot, err := ap.client.ClientUserBoot(ctx)
	if err != nil {
		ap.logger.Error("Failed to fetch client user boot", zap.Error(err))
		return nil, err
	}

	usersSnapshot := ap.usersSnapshot.Load()
	var collectedIDs []string
	for _, im := range boot.IMs {
		if !im.IsShared && !im.IsExtShared {
			continue
		}

		_, ok := usersSnapshot.Users[im.User]
		if !ok {
			collectedIDs = append(collectedIDs, im.User)
		}
	}

	res := make([]slack.User, 0, len(collectedIDs))
	if len(collectedIDs) > 0 {
		usersInfo, err := ap.client.GetUsersInfo(strings.Join(collectedIDs, ","))
		if err != nil {
			ap.logger.Error("Failed to fetch users info for shared IMs", zap.Error(err))
			return nil, err
		}

		for _, u := range *usersInfo {
			res = append(res, u)
		}
	}

	return res, nil
}

func (ap *ApiProvider) ProvideUsersMap() *UsersCache {
	return ap.usersSnapshot.Load()
}

var slackUserIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)

// ID query → users.info; OAuth → local cache regex; browser → edge UsersSearch.
func (ap *ApiProvider) SearchUsers(ctx context.Context, query string, limit int) ([]slack.User, error) {
	if slackUserIDPattern.MatchString(query) {
		users, err := ap.client.GetUsersInfo(query)
		if err != nil {
			return nil, err
		}
		if users != nil {
			return *users, nil
		}
		return nil, nil
	}

	if ap.IsOAuth() {
		return ap.searchUsersInCache(query, limit)
	}

	return ap.client.UsersSearch(ctx, query, limit)
}

// searchUsersInCache matches name, real name, display name and email; results
// are ordered by user ID so the same query always truncates the same way.
func (ap *ApiProvider) searchUsersInCache(query string, limit int) ([]slack.User, error) {
	if !ap.usersReady.Load() {
		return nil, ErrUsersNotReady
	}

	query = foldSearchText(query)
	if limit == 0 {
		return nil, nil
	}

	usersCache := ap.usersSnapshot.Load()
	if len(usersCache.search) == len(usersCache.Users) {
		var results []slack.User
		for _, entry := range usersCache.search {
			if entry.matches(query) {
				user := usersCache.Users[entry.id]
				if user.Deleted {
					continue
				}
				results = append(results, user)
				if len(results) == limit {
					break
				}
			}
		}
		return results, nil
	}

	// Hand-built snapshots in tests and older callers may not carry indexes.
	var results []slack.User
	for _, user := range usersCache.Users {
		if user.Deleted {
			continue
		}

		if newUserSearchEntry(user).matches(query) {
			results = append(results, user)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	if len(results) > limit {
		results = results[:limit]
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

// regexp's (?i) uses Unicode simple folding. Canonicalize each fold cycle once
// when building the cache so literal searches keep identical Unicode behavior.
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
