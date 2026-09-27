package handler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitFetchInOrderKeepsIndexOrderWhenLaterFetchesFinishFirst(t *testing.T) {
	const n = 6
	vals, errs := fetchInOrder(context.Background(), n, 4, func(_ context.Context, i int) (int, error) {
		// Earlier indexes are slower, so completion order is reversed.
		time.Sleep(time.Duration(n-i) * 10 * time.Millisecond)
		if i == 2 {
			return 0, errors.New("boom")
		}
		return i * 10, nil
	}, nil)

	assert.Equal(t, []int{0, 10, 0, 30, 40, 50}, vals)
	require.Len(t, errs, n)
	for i, err := range errs {
		if i == 2 {
			assert.EqualError(t, err, "boom")
		} else {
			assert.NoError(t, err, "index %d", i)
		}
	}
}

func TestUnitFetchInOrderRunsConcurrentlyWithinTheWorkerBound(t *testing.T) {
	var inFlight, peak atomic.Int32
	start := time.Now()
	fetchInOrder(context.Background(), 8, 4, func(context.Context, int) (struct{}, error) {
		now := inFlight.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		inFlight.Add(-1)
		return struct{}{}, nil
	}, nil)

	assert.Equal(t, int32(4), peak.Load(), "never more than the worker bound in flight, and the bound is used")
	assert.Less(t, time.Since(start), 350*time.Millisecond, "8 fetches of 50ms on 4 workers take ~100ms, not the 400ms of a serial loop")
}

func TestUnitFetchInOrderReportsProgressOneAtATime(t *testing.T) {
	var calls []int
	fetchInOrder(context.Background(), 5, 3, func(context.Context, int) (int, error) {
		time.Sleep(5 * time.Millisecond)
		return 0, nil
	}, func(finished int) {
		// Unsynchronized append: the race detector fails the test if done
		// is ever called concurrently.
		calls = append(calls, finished)
	})
	assert.Equal(t, []int{1, 2, 3, 4, 5}, calls)
}

func TestUnitFetchInOrderSkipsFetchesAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var fetched atomic.Int32
	_, errs := fetchInOrder(ctx, 10, 1, func(context.Context, int) (int, error) {
		if fetched.Add(1) == 2 {
			cancel()
		}
		return 0, nil
	}, nil)

	assert.Equal(t, int32(2), fetched.Load())
	for _, err := range errs[2:] {
		assert.ErrorIs(t, err, context.Canceled)
	}
}
