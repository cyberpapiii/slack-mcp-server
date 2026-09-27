package provider

import (
	"context"
	"os"
	"time"

	"go.uber.org/zap"
)

// cacheRevalidateEvery is how often a running server checks its users and
// channels caches against SLACK_MCP_CACHE_TTL. The TTL (24h by default) is
// what decides a refetch; this only bounds how late it can be.
const cacheRevalidateEvery = time.Hour

// cacheStale reports whether the cache file at path is older than ttl. The
// file is rewritten on every successful fetch, so its mtime is the time of
// the last good fetch. A missing file is stale; a ttl of 0 never is.
func cacheStale(path string, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return time.Since(info.ModTime()) > ttl
}

// StartCacheRevalidation keeps the users and channels caches within their
// TTL for the life of the provider. Before it, a process refetched them only
// at startup, so a server that ran for days kept stale names and titles.
// Call it once the caches are warm; later calls do nothing.
func (ap *ApiProvider) StartCacheRevalidation() {
	if ap.cacheTTL <= 0 {
		return
	}
	ap.revalidateOnce.Do(func() {
		go ap.runCacheRevalidation(ap.lifetime(), cacheRevalidateEvery)
	})
}

func (ap *ApiProvider) runCacheRevalidation(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		ap.revalidateCaches(ctx)
	}
}

// revalidateCaches refetches whichever cache is past its TTL. Users go first
// and are awaited, because channel DM names are mapped from the users
// snapshot. A fetch already in flight is joined, not repeated; a failed one
// keeps the current snapshot and is retried on the next tick.
func (ap *ApiProvider) revalidateCaches(ctx context.Context) {
	for _, c := range []struct {
		what  string
		path  string
		f     *refreshFlight
		fetch func(context.Context) error
	}{
		{"users", ap.usersCachePath, &ap.usersFlight, ap.fetchAndStoreUsers},
		{"channels", ap.channelsCachePath, &ap.channelsFlight, ap.fetchAndStoreChannels},
	} {
		if ctx.Err() != nil || !cacheStale(c.path, ap.cacheTTL) {
			continue
		}
		ap.logger.Info("Refreshing cache past its TTL", zap.String("cache", c.what), zap.Duration("ttl", ap.cacheTTL))
		fetchCtx, cancel := context.WithTimeout(ctx, ap.backgroundRefreshTimeout)
		err := c.f.do(fetchCtx, c.fetch)
		cancel()
		if err != nil {
			ap.logger.Warn("Cache refresh failed, continuing with stale data", zap.String("cache", c.what), zap.Error(err))
		}
	}
}
