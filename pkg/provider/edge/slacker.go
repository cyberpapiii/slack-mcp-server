package edge

import (
	"context"
	"sync"

	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type channelResult struct {
	Source   string
	Channels []slack.Channel
	Err      error
}

// collectChannels de-dupes by ID. Partial source failures are non-fatal and
// logged at Warn; only when every source fails (nothing collected) is the last
// error returned.
func collectChannels(results <-chan channelResult, logger *zap.Logger) (channels []slack.Channel, seen map[string]struct{}, err error) {
	seen = make(map[string]struct{})
	var failed []channelResult
	for r := range results {
		if r.Err != nil {
			failed = append(failed, r)
			continue
		}
		for _, c := range r.Channels {
			if _, ok := seen[c.ID]; !ok {
				seen[c.ID] = struct{}{}
				channels = append(channels, c)
			}
		}
	}
	if len(channels) == 0 && len(failed) > 0 {
		return nil, seen, failed[len(failed)-1].Err
	}
	for _, r := range failed {
		logger.Warn("Edge channel source failed; the channel list may be incomplete", zap.String("source", r.Source), zap.Error(r.Err))
	}
	return channels, seen, nil
}

// GetConversationsContext collects every conversation the browser session can
// see from client.userBoot, im.list and the channel browser, plus the MPIMs
// client.counts knows of. It is not paginated.
func (cl *Client) GetConversationsContext(ctx context.Context) ([]slack.Channel, error) {
	resultC := make(chan channelResult, 3)
	var pipeline = []func(){
		func() {
			ub, err := cl.ClientUserBoot(ctx)
			if err != nil {
				resultC <- channelResult{Source: "client.userBoot", Err: err}
				return
			}
			resultC <- channelResult{Source: "client.userBoot", Channels: slackChannels(ub.Channels)}
		},
		func() {
			ims, err := cl.imList(ctx)
			resultC <- channelResult{Source: "im.list", Channels: slackChannels(ims), Err: err}
		},
		func() {
			ch, err := cl.searchChannels(ctx, "")
			resultC <- channelResult{Source: "search.modules.channels", Channels: ch, Err: err}
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

	channels, seenChannels, err := collectChannels(resultC, cl.logger)
	if err != nil {
		return nil, err
	}

	// Supplementary MPIM IDs from ClientCounts; failure keeps boot/IM/search results.
	cr, err := cl.ClientCounts(ctx)
	if err != nil {
		cl.logger.Warn("Edge client.counts failed; group DMs only it lists are missing", zap.Error(err))
		return channels, nil
	}

	var fetchIDs = make([]string, 0, len(cr.MPIMs))
	for _, c := range cr.MPIMs {
		if _, seen := seenChannels[c.ID]; !seen {
			fetchIDs = append(fetchIDs, c.ID)
		}
	}

	mpims, err := cl.conversationsGenericInfo(ctx, fetchIDs...)
	if err != nil {
		cl.logger.Warn("Edge conversations.genericInfo failed; group DMs only client.counts lists are missing", zap.Error(err))
		return channels, nil
	}
	return append(channels, mpims...), nil
}
