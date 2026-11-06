package provider

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const (
	// warmupFastAttempts is how many warm-up attempts run warmupRetryDelay
	// apart before the retries drop to warmupSlowRetryDelay. They never stop.
	warmupFastAttempts   = 3
	warmupRetryDelay     = 30 * time.Second
	warmupSlowRetryDelay = 5 * time.Minute

	// cacheRevalidateEvery is how often a warm server checks its caches
	// against SLACK_MCP_CACHE_TTL. The TTL (24h by default) decides a
	// refetch; this only bounds how late it can be.
	cacheRevalidateEvery = time.Hour
)

// warmupNextDelay is the wait before attempt nextAttempt (1-based).
func warmupNextDelay(nextAttempt int) time.Duration {
	if nextAttempt <= warmupFastAttempts {
		return warmupRetryDelay
	}
	return warmupSlowRetryDelay
}

type cacheSpec struct {
	what   string
	path   string
	flight *refreshFlight
	load   func(context.Context) error // the cache file if usable, else a fetch
	fetch  func(context.Context) error
}

// caches lists the users cache before the channels cache. DM channel names
// are mapped from the users snapshot, so channels come out right only once
// users are in.
func (ap *ApiProvider) caches() []cacheSpec {
	return []cacheSpec{
		{"users", ap.usersCachePath, &ap.usersFlight, ap.RefreshUsers, ap.fetchAndStoreUsers},
		{"channels", ap.channelsCachePath, &ap.channelsFlight, ap.RefreshChannels, ap.fetchAndStoreChannels},
	}
}

// StartCacheLifecycle warms the users and channels caches in the background,
// retrying until they load, then calls onReady once and keeps them within
// their TTL. Close stops it at any point.
func (ap *ApiProvider) StartCacheLifecycle(onReady func()) {
	go ap.runCacheLifecycle(ap.lifetime(), onReady)
}

func (ap *ApiProvider) runCacheLifecycle(ctx context.Context, onReady func()) {
	for attempt := 1; ctx.Err() == nil; attempt++ {
		ap.warmCaches(ctx)
		ready, err := ap.IsReady()
		if ready {
			onReady()
			ap.startCacheRevalidation(ctx)
			if attempt > 1 {
				ap.logger.Info("Cache warm-up succeeded after retry", zap.String("context", "console"), zap.Int("attempt", attempt))
			} else {
				ap.logger.Info("Slack MCP Server is fully ready", zap.String("context", "console"))
			}
			return
		}

		switch {
		case attempt < warmupFastAttempts:
			ap.logger.Warn("Cache warm-up incomplete, retrying",
				zap.String("context", "console"),
				zap.Int("attempt", attempt),
				zap.Error(err),
				zap.Duration("next_retry_in", warmupRetryDelay),
			)
		case attempt == warmupFastAttempts:
			ap.logger.Error("Cache warm-up failed after retries; cache-dependent tools will not be available until a background retry succeeds",
				zap.String("context", "console"),
				zap.Int("attempts", warmupFastAttempts),
				zap.Duration("slow_retry_every", warmupSlowRetryDelay),
				zap.Error(err),
			)
		default:
			ap.logger.Info("Background cache warm-up retry",
				zap.String("context", "console"),
				zap.Int("attempt", attempt),
				zap.Error(err),
			)
		}

		timer := time.NewTimer(warmupNextDelay(attempt + 1))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// warmCaches loads each cache once, from its file when usable, under the
// background refresh budget. A failure is logged; the caller retries.
func (ap *ApiProvider) warmCaches(ctx context.Context) {
	for _, c := range ap.caches() {
		ap.logger.Info("Caching "+c.what+" collection...", zap.String("context", "console"))
		loadCtx, cancel := context.WithTimeout(ctx, ap.backgroundRefreshTimeout)
		err := c.load(loadCtx)
		cancel()
		if err != nil {
			ap.logger.Error("Cache warm-up failed; server continues with degraded cache",
				zap.String("context", "console"),
				zap.String("cache", c.what),
				zap.Error(err),
			)
		}
	}
}

// startCacheRevalidation keeps the caches within their TTL for the life of
// the provider. Before it, a process refetched them only at startup, so a
// server that ran for days kept stale names and titles.
func (ap *ApiProvider) startCacheRevalidation(ctx context.Context) {
	if ap.cacheTTL <= 0 {
		return
	}
	ap.revalidateOnce.Do(func() {
		go ap.runCacheRevalidation(ctx, cacheRevalidateEvery)
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

// revalidateCaches refetches whichever cache is past its TTL, each awaited in
// order. A fetch already in flight is joined, not repeated; a failed one
// keeps the current snapshot and is retried on the next tick.
func (ap *ApiProvider) revalidateCaches(ctx context.Context) {
	for _, c := range ap.caches() {
		if ctx.Err() != nil {
			return
		}
		if expired, _ := cacheExpired(c.path, ap.cacheTTL); !expired {
			continue
		}
		ap.logger.Info("Refreshing cache past its TTL", zap.String("cache", c.what), zap.Duration("ttl", ap.cacheTTL))
		fetchCtx, cancel := context.WithTimeout(ctx, ap.backgroundRefreshTimeout)
		err := c.flight.do(fetchCtx, c.fetch)
		cancel()
		if err != nil {
			ap.logger.Warn("Cache refresh failed, continuing with stale data", zap.String("cache", c.what), zap.Error(err))
		}
	}
}
