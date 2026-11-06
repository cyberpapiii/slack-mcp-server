package limiter

import (
	"context"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func rateLimited(after time.Duration) error {
	return &slack.RateLimitedError{RetryAfter: after}
}

func TestCallWithRetry_Success(t *testing.T) {
	rl := rate.NewLimiter(rate.Inf, 1)
	ctx := context.Background()

	callCount := 0
	result, err := CallWithRetry(ctx, rl, 2, func() (string, error) {
		callCount++
		return "ok", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 1, callCount)
}

func TestCallWithRetry_RetryOnRateLimit(t *testing.T) {
	rl := rate.NewLimiter(rate.Inf, 1)
	ctx := context.Background()

	callCount := 0
	result, err := CallWithRetry(ctx, rl, 2, func() (string, error) {
		callCount++
		if callCount <= 2 {
			return "", rateLimited(10 * time.Millisecond)
		}
		return "ok", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 3, callCount, "should count initial call + 2 retries")
}

func TestCallWithRetry_ExhaustedRetriesReturnTheRateLimit(t *testing.T) {
	for _, retries := range []int{0, 2} {
		rl := rate.NewLimiter(rate.Inf, 1)
		callCount := 0
		_, err := CallWithRetry(context.Background(), rl, retries, func() (string, error) {
			callCount++
			return "", rateLimited(10 * time.Millisecond)
		})

		var re *slack.RateLimitedError
		assert.ErrorAs(t, err, &re, "retries=%d", retries)
		assert.Equal(t, retries+1, callCount, "initial call + %d retries", retries)
	}
}

func TestCallWithRetry_NonRetryableError(t *testing.T) {
	rl := rate.NewLimiter(rate.Inf, 1)
	ctx := context.Background()

	callCount := 0
	_, err := CallWithRetry(ctx, rl, 2, func() (string, error) {
		callCount++
		return "", assert.AnError
	})

	require.Error(t, err)
	assert.Equal(t, assert.AnError, err, "should return non-retryable error immediately without retry")
	assert.Equal(t, 1, callCount)
}

func TestCallWithRetry_ContextCancelled(t *testing.T) {
	rl := rate.NewLimiter(rate.Inf, 1)
	ctx, cancel := context.WithCancel(context.Background())

	callCount := 0
	_, err := CallWithRetry(ctx, rl, 2, func() (string, error) {
		callCount++
		cancel()
		return "", rateLimited(5 * time.Second)
	})

	require.Error(t, err)
	assert.Equal(t, 1, callCount, "should stop after context cancellation")
}

func TestCallWithRetry_WaitsOnTheLimiterBeforeEachCall(t *testing.T) {
	rl := rate.NewLimiter(rate.Every(100*time.Millisecond), 1)
	ctx := context.Background()
	call := func() (string, error) { return "ok", nil }

	start := time.Now()
	_, err := CallWithRetry(ctx, rl, 0, call)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 50*time.Millisecond, "first call uses the burst token")

	_, err = CallWithRetry(ctx, rl, 0, call)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 80*time.Millisecond, "second call waits for the next token")
}

func TestCallWithRetry_RetryThenSuccess(t *testing.T) {
	rl := rate.NewLimiter(rate.Inf, 1)
	ctx := context.Background()

	type result struct {
		ID string
	}

	callCount := 0
	start := time.Now()
	res, err := CallWithRetry(ctx, rl, 1, func() (*result, error) {
		callCount++
		if callCount == 1 {
			return nil, rateLimited(50 * time.Millisecond)
		}
		return &result{ID: "C123"}, nil
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "C123", res.ID)
	assert.Equal(t, 2, callCount)
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "should have slept for retryAfter duration")
}

// A nil limiter retries a 429 without pacing the calls.
func TestCallWithRetry_NilLimiterRetriesWithoutPacing(t *testing.T) {
	callCount := 0
	res, err := CallWithRetry(context.Background(), nil, 1, func() (string, error) {
		callCount++
		if callCount == 1 {
			return "", rateLimited(0)
		}
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", res)
	assert.Equal(t, 2, callCount)
}
