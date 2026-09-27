package limiter

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"
)

// Search had shared Tier2 (burst 3) with the conversations.list pager, so the
// fourth back-to-back search waited ~2.5s and a channel refresh could starve
// it. It keeps Slack's 20/min sustained rate but gets its own bucket and a
// burst that covers an agent's usual run of queries. Reads only Burst and
// Limit: tests must not spend the process-wide tokens.
func TestUnitSearchMessagesHasItsOwnBurstyBucket(t *testing.T) {
	search := SearchMessages.Limiter()
	assert.NotSame(t, Tier2.Limiter(), search, "search must not share the conversations.list bucket")
	assert.GreaterOrEqual(t, search.Burst(), 10)
	assert.Equal(t, rate.Every(3*time.Second), search.Limit(), "sustained rate stays at Slack's Tier 2 (20/min)")
}

// activity_unreads fetches its threads four at a time. On the shared Tier3
// bucket, saved_list pagination spent the same burst of 4 that the fan-out
// needs, so its own bucket keeps the parallel reads from queueing behind it.
func TestUnitConversationsRepliesHasItsOwnBucket(t *testing.T) {
	replies := ConversationsReplies.Limiter()
	assert.NotSame(t, Tier3.Limiter(), replies, "thread fetches must not share saved_list's bucket")
	assert.GreaterOrEqual(t, replies.Burst(), 8, "burst covers two rounds of the 4-worker fan-out")
	assert.Equal(t, rate.Every(1200*time.Millisecond), replies.Limit(), "sustained rate stays at Slack's Tier 3 (50/min)")
}
