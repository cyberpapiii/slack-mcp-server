package provider

import (
	"context"
	"sync"

	"go.uber.org/zap"
)

// refreshFlight coalesces concurrent refreshes of one cache: the first caller
// runs the fetch, every overlapping caller waits for it and shares its result.
type refreshFlight struct {
	mu   sync.Mutex
	call *refreshCall
}

type refreshCall struct {
	done chan struct{}
	err  error
}

func (f *refreshFlight) begin() (call *refreshCall, leader bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.call != nil {
		return f.call, false
	}
	f.call = &refreshCall{done: make(chan struct{})}
	return f.call, true
}

func (f *refreshFlight) finish(call *refreshCall, err error) {
	f.mu.Lock()
	call.err = err
	f.call = nil
	close(call.done)
	f.mu.Unlock()
}

func (f *refreshFlight) inFlight() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.call != nil
}

// do runs fetch as the leader, or waits for the in-flight leader and returns
// its error. A failed leader fails its waiters too; the callers' own retry
// policy decides whether to try again, so one Slack outage costs one fetch.
func (f *refreshFlight) do(ctx context.Context, fetch func(context.Context) error) (err error) {
	call, leader := f.begin()
	if leader {
		// Deferred so a recovered panic in fetch cannot leave the flight
		// open forever (server.WithRecovery swallows handler panics).
		defer func() { f.finish(call, err) }()
		return fetch(ctx)
	}
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// spawnBackgroundRefresh runs fetch in the background unless a refresh is
// already in flight. The fetch is bounded by backgroundRefreshTimeout and by
// the provider's lifetime, so Close stops it.
func (ap *ApiProvider) spawnBackgroundRefresh(f *refreshFlight, what string, fetch func(context.Context) error) {
	call, leader := f.begin()
	if !leader {
		ap.logger.Debug("Skipping background refresh, already in progress", zap.String("cache", what))
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(ap.lifetime(), ap.backgroundRefreshTimeout)
		defer cancel()
		var err error
		defer func() { f.finish(call, err) }()
		err = fetch(ctx)
		if err != nil {
			ap.logger.Warn("Background refresh failed, continuing with stale data",
				zap.String("cache", what), zap.Error(err))
		}
	}()
}
