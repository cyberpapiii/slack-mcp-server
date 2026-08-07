package edge

import (
	"context"
	"errors"
	"sync"

	"github.com/rusq/slack"
)

var ErrParameterMissing = errors.New("required parameter missing")

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
	var resultC = make(chan channelResult, 2)
	var pipeline = []func(){
		func() {
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
			ims, err := cl.IMList(ctx)
			var ch = make([]slack.Channel, 0, len(ims))
			for _, c := range ims {
				ch = append(ch, c.SlackChannel())
			}
			resultC <- channelResult{Channels: ch, Err: err}
		},
		func() {
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
