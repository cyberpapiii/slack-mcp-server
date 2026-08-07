package edge

import (
	"context"
	"errors"
	"sync"

	"github.com/rusq/slack"
)

var ErrParameterMissing = errors.New("required parameter missing")

// High level functions that wrap low level calls to webclient API to return
// the data in the format close to the Slack API.

// channelResult is one pipeline source's contribution to
// GetConversationsContext.
type channelResult struct {
	Channels []slack.Channel
	Err      error
}

// collectChannels drains the per-source results, de-duplicating channels by
// ID.  Individual failures are non-fatal: channels from sources that succeeded
// are kept, and only if every source failed (nothing was collected) is the last
// error returned.  The returned seen set lets the caller skip IDs it has
// already collected.
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
	var resultC = make(chan channelResult, 2)
	var pipeline = []func(){
		func() {
			// getting client.userBoot information
			ub, err := cl.ClientUserBoot(ctx)
			if err != nil {
				resultC <- channelResult{Err: err}
				return
			}
			var ch = make([]slack.Channel, 0, len(ub.Channels))
			for _, c := range ub.Channels {
				ch = append(ch, c.SlackChannel())
			}
			resultC <- channelResult{Channels: ch, Err: err}
		},
		func() {
			// collecting the IMs.
			ims, err := cl.IMList(ctx)
			var ch = make([]slack.Channel, 0, len(ims))
			for _, c := range ims {
				ch = append(ch, c.SlackChannel())
			}
			resultC <- channelResult{Channels: ch, Err: err}
		},
		func() {
			// collecting the channels.
			ch, err := cl.SearchChannels(ctx, "")
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

	// Collect results from all goroutines. Individual failures are non-fatal:
	// we keep channels from sources that succeeded. Only if every source fails
	// (nothing collected) do we propagate the last error.
	var seenChannels map[string]struct{}
	channels, seenChannels, err = collectChannels(resultC)
	if err != nil {
		return nil, "", err
	}

	// ClientCounts returns MPIM IDs that we haven't seen in the user boot
	// response. This is supplementary — failures here don't discard the
	// channels we already collected.
	cr, err := cl.ClientCounts(ctx)
	if err != nil {
		return channels, "", nil
	}

	// determine which mpims are already in the list, and which need to be
	// fetched
	var fetchIDs = make([]string, 0, len(cr.MPIMs))
	for _, c := range cr.MPIMs {
		if _, seen := seenChannels[c.ID]; !seen {
			fetchIDs = append(fetchIDs, c.ID)
		}
	}

	// getting the info on any MPIMs that we haven't seen yet.
	mpims, err := cl.ConversationsGenericInfo(ctx, fetchIDs...)
	if err != nil {
		return channels, "", nil
	}
	channels = append(channels, mpims...)
	return channels, "", nil
}

func (cl *Client) GetUsersInConversationContext(ctx context.Context, p *slack.GetUsersInConversationParameters) (ids []string, _ string, err error) {
	if p.ChannelID == "" {
		return nil, "", ErrParameterMissing
	}
	uu, err := cl.UsersList(ctx, p.ChannelID)
	if err != nil {
		return nil, "", err
	}
	for _, u := range uu {
		ids = append(ids, u.ID)
	}
	return ids, "", nil
}

var ErrNotFound = errors.New("not found")

func (cl *Client) GetConversationInfoContext(ctx context.Context, input *slack.GetConversationInfoInput) (*slack.Channel, error) {
	cc, err := cl.ConversationsGenericInfo(ctx, input.ChannelID)
	if err != nil {
		return nil, err
	}
	if len(cc) == 0 {
		return nil, ErrNotFound
	}
	return &cc[0], nil
}
