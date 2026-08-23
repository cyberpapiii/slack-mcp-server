package edge

import (
	"context"
	"sync"

	"github.com/rusq/slack"
)

type channelResult struct {
	Channels []slack.Channel
	Err      error
}

// collectChannels de-dupes by ID. Partial source failures are non-fatal; only
// when every source fails (nothing collected) is the last error returned.
func collectChannels(results <-chan channelResult) (channels []slack.Channel, seen map[string]struct{}, err error) {
	seen = make(map[string]struct{})
	var lastErr error
	for r := range results {
		if r.Err != nil {
			lastErr = r.Err
			continue
		}
		for _, c := range r.Channels {
			if _, ok := seen[c.ID]; !ok {
				seen[c.ID] = struct{}{}
				channels = append(channels, c)
			}
		}
	}
	if len(channels) == 0 && lastErr != nil {
		return nil, seen, lastErr
	}
	return channels, seen, nil
}

func (cl *Client) GetConversationsContext(ctx context.Context, _ *slack.GetConversationsParameters) (channels []slack.Channel, _ string, err error) {
	// Buffer matches pipeline length so producers never block if collect lags.
	resultC := make(chan channelResult, 3)
	var pipeline = []func(){
		func() {
			ub, err := cl.ClientUserBoot(ctx)
			if err != nil {
				resultC <- channelResult{Err: err}
				return
			}
			var ch = make([]slack.Channel, 0, len(ub.Channels))
			for _, c := range ub.Channels {
				ch = append(ch, c.slackChannel())
			}
			resultC <- channelResult{Channels: ch, Err: err}
		},
		func() {
			ims, err := cl.imList(ctx)
			var ch = make([]slack.Channel, 0, len(ims))
			for _, c := range ims {
				ch = append(ch, c.slackChannel())
			}
			resultC <- channelResult{Channels: ch, Err: err}
		},
		func() {
			ch, err := cl.searchChannels(ctx, "")
			resultC <- channelResult{Channels: ch, Err: err}
		},
	}

	var wg sync.WaitGroup
	wg.Add(len(pipeline))
	for _, f := range pipeline {
		go func(f func()) {
			defer wg.Done()
			f()
		}(f)
	}
	go func() {
		wg.Wait()
		close(resultC)
	}()

	var seenChannels map[string]struct{}
	channels, seenChannels, err = collectChannels(resultC)
	if err != nil {
		return nil, "", err
	}

	// Supplementary MPIM IDs from ClientCounts; failure keeps boot/IM/search results.
	cr, err := cl.ClientCounts(ctx)
	if err != nil {
		return channels, "", nil
	}

	var fetchIDs = make([]string, 0, len(cr.MPIMs))
	for _, c := range cr.MPIMs {
		if _, seen := seenChannels[c.ID]; !seen {
			fetchIDs = append(fetchIDs, c.ID)
		}
	}

	mpims, err := cl.conversationsGenericInfo(ctx, fetchIDs...)
	if err != nil {
		return channels, "", nil
	}
	channels = append(channels, mpims...)
	return channels, "", nil
}
