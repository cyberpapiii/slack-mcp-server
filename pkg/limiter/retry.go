package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/slackhttp"
	"golang.org/x/time/rate"
)

// CallWithRetry calls fn, waiting on rl before each attempt, and retries up to
// maxRetries times when fn returns a Slack rate limit, after the Retry-After
// Slack sent. Any other error returns at once. A nil rl does not pace, for
// calls that only want the retry.
func CallWithRetry[T any](ctx context.Context, rl *rate.Limiter, maxRetries int, fn func() (T, error)) (T, error) {
	var result T
	var err error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if rl != nil {
			if waitErr := rl.Wait(ctx); waitErr != nil {
				return result, fmt.Errorf("rate limiter context cancelled: %w", waitErr)
			}
		}

		result, err = fn()
		if err == nil {
			return result, nil
		}

		backoff, limited := slackhttp.RetryAfter(err)
		if !limited || attempt == maxRetries {
			return result, err
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(backoff):
		}
	}

	return result, err
}
