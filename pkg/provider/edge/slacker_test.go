package edge

import (
	"errors"
	"testing"

	"github.com/rusq/slack"
	"github.com/stretchr/testify/assert"
)

// resultChan feeds a fixed set of results through a closed channel, the same
// way GetConversationsContext's pipeline goroutines feed collectChannels.
func resultChan(results []channelResult) <-chan channelResult {
	c := make(chan channelResult, len(results))
	for _, r := range results {
		c <- r
	}
	close(c)
	return c
}

// TestPartialResultCollection verifies the production result-collection helper
// used by GetConversationsContext: individual goroutine failures should not
// discard channels from goroutines that succeeded. Only if all sources fail
// should an error be returned.
func TestPartialResultCollection(t *testing.T) {
	ch := func(id string) slack.Channel {
		return slack.Channel{GroupConversation: slack.GroupConversation{
			Conversation: slack.Conversation{ID: id},
		}}
	}

	t.Run("all sources succeed", func(t *testing.T) {
		channels, seen, err := collectChannels(resultChan([]channelResult{
			{Channels: []slack.Channel{ch("C1"), ch("C2")}},
			{Channels: []slack.Channel{ch("C3")}},
			{Channels: []slack.Channel{ch("C4")}},
		}))
		assert.NoError(t, err)
		assert.Len(t, channels, 4)
		assert.Len(t, seen, 4, "the seen set should mirror the collected channels")
	})

	t.Run("one source fails, others succeed", func(t *testing.T) {
		channels, _, err := collectChannels(resultChan([]channelResult{
			{Channels: []slack.Channel{ch("C1"), ch("C2")}},
			{Err: errors.New("IMList failed")},
			{Channels: []slack.Channel{ch("C3")}},
		}))
		assert.NoError(t, err, "should not propagate error when some sources succeeded")
		assert.Len(t, channels, 3, "should keep channels from successful sources")
	})

	t.Run("two sources fail, one succeeds", func(t *testing.T) {
		channels, _, err := collectChannels(resultChan([]channelResult{
			{Err: errors.New("ClientUserBoot failed")},
			{Err: errors.New("IMList failed")},
			{Channels: []slack.Channel{ch("C1")}},
		}))
		assert.NoError(t, err, "should not propagate error when at least one source succeeded")
		assert.Len(t, channels, 1)
	})

	t.Run("all sources fail", func(t *testing.T) {
		channels, _, err := collectChannels(resultChan([]channelResult{
			{Err: errors.New("ClientUserBoot failed")},
			{Err: errors.New("IMList failed")},
			{Err: errors.New("SearchChannels failed")},
		}))
		assert.Error(t, err, "should propagate error when all sources failed")
		assert.Nil(t, channels)
	})

	t.Run("deduplicates channels across sources", func(t *testing.T) {
		channels, seen, err := collectChannels(resultChan([]channelResult{
			{Channels: []slack.Channel{ch("C1"), ch("C2")}},
			{Channels: []slack.Channel{ch("C2"), ch("C3")}}, // C2 is duplicate
			{Channels: []slack.Channel{ch("C1")}},           // C1 is duplicate
		}))
		assert.NoError(t, err)
		assert.Len(t, channels, 3, "duplicates should be removed")
		assert.Len(t, seen, 3)
	})

	t.Run("no results and no errors returns empty", func(t *testing.T) {
		channels, seen, err := collectChannels(resultChan([]channelResult{
			{Channels: []slack.Channel{}},
			{Channels: []slack.Channel{}},
		}))
		assert.NoError(t, err)
		assert.Empty(t, channels)
		assert.Empty(t, seen)
	})
}
