package limiter

import (
	"context"

	"golang.org/x/time/rate"
)

// Retries is how many times a rate-limited Slack call is retried, after the
// Retry-After Slack sent. Every Slack client uses it.
const Retries = 2

// Pacing maps a Slack method to the bucket it waits on before each attempt.
// A method the table does not name is not paced, and a nil table paces
// nothing, which is what tests use.
type Pacing map[string]*rate.Limiter

// SlackPacing is the table the Web API and edge clients run with: the methods
// Slack throttles hard enough that a burst of calls would otherwise 429.
func SlackPacing() Pacing {
	return Pacing{
		"conversations.list":      Tier2,
		"conversations.replies":   ConversationsReplies,
		"search.messages":         SearchMessages,
		"im.list":                 Tier2boost,
		"search.modules.channels": Tier2boost,
		"saved.list":              Tier3,
	}
}

func Paced[T any](ctx context.Context, pacing Pacing, method string, fn func() (T, error)) (T, error) {
	return CallWithRetry(ctx, pacing[method], Retries, fn)
}
