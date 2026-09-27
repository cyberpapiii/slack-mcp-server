package limiter

import (
	"time"

	"golang.org/x/time/rate"
)

// Tier is a process-wide Slack rate-limit budget. Every caller of the same
// tier shares one limiter, so concurrent tool calls split the burst instead of
// each minting a fresh one.
type Tier struct {
	lim *rate.Limiter
}

func newTier(every time.Duration, burst int) Tier {
	return Tier{lim: rate.NewLimiter(rate.Every(every), burst)}
}

func (t Tier) Limiter() *rate.Limiter {
	return t.lim
}

var (
	// Tier2 paces the conversations.list pager behind the channel cache.
	Tier2      = newTier(3*time.Second, 3)
	Tier2boost = newTier(300*time.Millisecond, 5)
	Tier3      = newTier(1200*time.Millisecond, 4)

	// SearchMessages is search.messages' own Tier 2 bucket: Slack's 20/min
	// sustained, with a burst that covers an agent's usual run of queries.
	// Sharing Tier2's burst of 3 made 30% of field searches wait ~2.5s, and a
	// 429 past the burst is still retried on Slack's Retry-After.
	SearchMessages = newTier(3*time.Second, 10)

	// ConversationsReplies paces activity_unreads' per-thread fetches at
	// Slack's Tier 3 rate, apart from saved_list's pagination on Tier3.
	ConversationsReplies = newTier(1200*time.Millisecond, 8)
)
