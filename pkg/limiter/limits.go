package limiter

import (
	"time"

	"golang.org/x/time/rate"
)

// Each bucket is a process-wide Slack rate-limit budget. Every caller of the
// same bucket shares one limiter, so concurrent tool calls split the burst
// instead of each minting a fresh one.
var (
	// Tier2 paces the conversations.list pager behind the channel cache.
	Tier2      = rate.NewLimiter(rate.Every(3*time.Second), 3)
	Tier2boost = rate.NewLimiter(rate.Every(300*time.Millisecond), 5)
	Tier3      = rate.NewLimiter(rate.Every(1200*time.Millisecond), 4)

	// SearchMessages is search.messages' own Tier 2 bucket: Slack's 20/min
	// sustained, with a burst that covers an agent's usual run of queries.
	// Sharing Tier2's burst of 3 made 30% of field searches wait ~2.5s, and a
	// 429 past the burst is still retried on Slack's Retry-After.
	SearchMessages = rate.NewLimiter(rate.Every(3*time.Second), 10)

	// ConversationsReplies paces every conversations.replies call (thread
	// reads, activity_unreads' per-thread fan-out, single-message fetches) at
	// Slack's Tier 3 rate, apart from saved_list's pagination on Tier3.
	ConversationsReplies = rate.NewLimiter(rate.Every(1200*time.Millisecond), 8)
)
