package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slack-go/slack"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// CallWithRetry calls fn, waiting on rl before each attempt, and retries up to
// maxRetries times when fn returns a Slack rate limit, after the Retry-After
// Slack sent. Any other error returns at once. A nil rl does not pace, for
// calls that only want the retry. A non-nil logger records each rate limit:
// Info while a retry follows, Debug once the retries are spent.
func CallWithRetry[T any](ctx context.Context, rl *rate.Limiter, maxRetries int, logger *zap.Logger, fn func() (T, error)) (T, error) {
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

		backoff, limited := RetryAfter(err)
		if !limited {
			return result, err
		}
		if attempt == maxRetries {
			if logger != nil {
				logger.Debug("Slack call still rate limited after retrying")
			}
			return result, err
		}
		if logger != nil {
			logger.Info("Slack call rate limited, waiting", zap.Duration("delay", backoff))
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(backoff):
		}
	}

	return result, err
}

// RetryAfter reports whether err is a Slack rate limit and, if so, the wait
// Slack asked for, which may be zero.
func RetryAfter(err error) (time.Duration, bool) {
	var limited *slack.RateLimitedError
	if errors.As(err, &limited) {
		return limited.RetryAfter, true
	}
	return 0, false
}
