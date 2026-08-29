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
	usersInfo, err := ap.client.GetUsersInfo(userID)
	if err != nil {
		ap.logger.Warn("Failed to fetch user for cache patch", zap.String("user_id", userID), zap.Error(err))
		return nil, err
	}
	if usersInfo == nil || len(*usersInfo) == 0 {
		ap.logger.Debug("User not found via API", zap.String("user_id", userID))
		return nil, errors.New("user not found")
	}

	user := (*usersInfo)[0]

	for {
		current := ap.usersSnapshot.Load()
		usersLen, invLen := 0, 0
		if current != nil {
			usersLen = len(current.Users)
			invLen = len(current.UsersInv)
		}

		newSnapshot := &UsersCache{
			Users:    make(map[string]slack.User, usersLen+1),
			UsersInv: make(map[string]string, invLen+1),
		}
		if current != nil {
			for k, v := range current.Users {
				newSnapshot.Users[k] = v
			}
			for k, v := range current.UsersInv {
				if v == user.ID {
					continue // drop stale name→ID before rename
				}
				newSnapshot.UsersInv[k] = v
			}
		}
		newSnapshot.Users[user.ID] = user
		newSnapshot.UsersInv[user.Name] = user.ID
		newSnapshot.search = make([]userSearchEntry, 0, len(newSnapshot.Users))
		for id, indexedUser := range newSnapshot.Users {
			entry := newUserSearchEntry(indexedUser)
			entry.id = id
			newSnapshot.search = append(newSnapshot.search, entry)
		}
		sort.Slice(newSnapshot.search, func(i, j int) bool { return newSnapshot.search[i].id < newSnapshot.search[j].id })

		if ap.usersSnapshot.CompareAndSwap(current, newSnapshot) {
			break
		}
	}

	ap.logger.Debug("Patched user into cache",
		zap.String("user_id", user.ID),
		zap.String("user_name", user.Name))

	return &user, nil
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
