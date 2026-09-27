package handler

import (
	"context"
	"sync"
)

// fanOutWorkers bounds the Slack reads one tool call runs at once when it
// fetches a thread or channel per unread item. Plug serializes tool calls, so
// this is the whole server's concurrency for those reads.
const fanOutWorkers = 4

// fetchInOrder runs fetch for indexes 0..n-1 on at most workers goroutines
// and returns the values and errors in index order, so output stays in the
// order a serial loop would give. Once ctx is done, unstarted indexes get
// ctx.Err() without calling fetch. done, when non-nil, is called after each
// fetch with the number finished so far, never concurrently.
//
// Callers convert the results after it returns, on one goroutine: only the
// Slack round trips run in parallel.
func fetchInOrder[T any](ctx context.Context, n, workers int, fetch func(context.Context, int) (T, error), done func(finished int)) ([]T, []error) {
	vals := make([]T, n)
	errs := make([]error, n)
	indexes := make(chan int)
	var mu sync.Mutex
	finished := 0
	var wg sync.WaitGroup
	for range min(workers, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indexes {
				if err := ctx.Err(); err != nil {
					errs[i] = err
				} else {
					vals[i], errs[i] = fetch(ctx, i)
				}
				mu.Lock()
				finished++
				if done != nil {
					done(finished)
				}
				mu.Unlock()
			}
		}()
	}
	for i := range n {
		indexes <- i
	}
	close(indexes)
	wg.Wait()
	return vals, errs
}
