package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/limiter"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

func (ap *ApiProvider) RefreshChannels(ctx context.Context) error {
	return ap.refreshChannelsInternal(ctx, false)
}

// ForceRefreshChannels bypasses cache; rate-limited by minRefreshInterval (ErrRefreshRateLimited).
func (ap *ApiProvider) ForceRefreshChannels(ctx context.Context) error {
	if ap.minRefreshInterval > 0 {
		// Check-and-stamp under one lock to avoid TOCTOU on concurrent forces.
		ap.channelsMu.Lock()
		sinceLast := time.Since(ap.lastForcedChannelsRefresh)
		if sinceLast < ap.minRefreshInterval {
			ap.channelsMu.Unlock()
			ap.logger.Debug("Skipping forced channels refresh, within rate limit",
				zap.Duration("since_last", sinceLast),
				zap.Duration("min_interval", ap.minRefreshInterval))
			return ErrRefreshRateLimited
		}
		ap.lastForcedChannelsRefresh = time.Now()
		ap.channelsMu.Unlock()
	}

	ap.logger.Info("Force refreshing channels cache")
	return ap.refreshChannelsInternal(ctx, true)
}

func (ap *ApiProvider) refreshChannelsInternal(ctx context.Context, force bool) error {
	ap.channelsMu.Lock()

	if !force {
		if cached, expired, ok := loadCacheFile[Channel](ap.channelsCachePath, ap.cacheTTL, ap.logger); ok {
			// Re-map IMs against current users so @names stay fresh after user cache updates.
			usersMap := ap.ProvideUsersMap().Users
			newSnapshot := newChannelsCache(len(cached))
			for _, c := range cached {
				if c.IsIM {
					c = mapChannel(
						c.ID, "", "", c.Topic, c.Purpose,
						c.User, c.Members, c.MemberCount,
						c.IsIM, c.IsMpIM, c.IsPrivate, c.IsExtShared,
						usersMap,
					)
				}
				newSnapshot.add(c)
			}
			ap.channelsSnapshot.Store(newSnapshot)
			ap.channelsReady.Store(true)
			ap.channelsMu.Unlock()

			if expired {
				ap.spawnBackgroundRefresh(&ap.channelsFlight, "channels", ap.fetchAndStoreChannels)
			}
			return nil
		}
	}

	ap.channelsMu.Unlock()
	return ap.channelsFlight.do(ctx, ap.fetchAndStoreChannels)
}

func (ap *ApiProvider) fetchAndStoreChannels(ctx context.Context) error {
	channels, err := ap.GetChannels(ctx, AllChanTypes)

	// Prefer the real fetch error over a misleading "zero channels" when page 1 fails.
	if err != nil {
		if ap.channelsReady.Load() {
			ap.logger.Warn("Channel fetch incomplete, keeping existing cache",
				zap.Int("partialCount", len(channels)),
				zap.Error(err))
			return nil
		}
		return fmt.Errorf("channel fetch incomplete and no existing cache is available: %w", err)
	}

	if len(channels) == 0 {
		if ap.channelsReady.Load() {
			ap.logger.Warn("API returned zero channels, keeping existing cache")
			return nil
		}
		return errors.New("API returned zero channels and no existing cache is available")
	}

	writeCacheFile(ap.channelsCachePath, channels, ap.logger)
	ap.channelsReady.Store(true)

	return nil
}

// SlackRetryAfter is the limiter.CallWithRetry callback for Slack errors:
// the server's Retry-After for rate limits, 0 for anything else.
func SlackRetryAfter(err error) time.Duration {
	var rle *slack.RateLimitedError
	if errors.As(err, &rle) {
		return rle.RetryAfter
	}
	return 0
}

// Wraps channels+cursor so CallWithRetry (one T) can paginate GetConversationsContext.
type channelsPageResult struct {
	channels []slack.Channel
	cursor   string
}

// Partial pages returned on error — callers must not treat that as a complete list.
func (ap *ApiProvider) getChannelsMultiType(ctx context.Context, channelTypes []string) ([]Channel, error) {
	params := &slack.GetConversationsParameters{
		Types:           channelTypes,
		Limit:           999,
		ExcludeArchived: true,
	}

	var chans []Channel

	usersMap := ap.ProvideUsersMap().Users
	// Tier2 matches conversations.list; faster budgets caused 429 truncations.
	lim := limiter.Tier2.Limiter()

	for {
		// CallWithRetry already paces; do not wait again in this loop.
		page, err := limiter.CallWithRetry(ctx, lim, 2, SlackRetryAfter,
			func() (channelsPageResult, error) {
				c, cur, err := ap.client.GetConversationsContext(ctx, params)
				return channelsPageResult{channels: c, cursor: cur}, err
			})
		ap.logger.Debug("Fetched channels",
			zap.Strings("channelTypes", channelTypes),
			zap.Int("count", len(page.channels)),
		)
		if err != nil {
			ap.logger.Error("Failed to fetch channels, returning partial result",
				zap.Strings("channelTypes", channelTypes),
				zap.Int("collectedSoFar", len(chans)),
				zap.Error(err))
			return chans, err
		}

		for _, channel := range page.channels {
			chans = append(chans, MapChannelFromSlack(channel, usersMap))
		}

		if page.cursor == "" {
			break
		}

		params.Cursor = page.cursor
	}
	return chans, nil
}

func (ap *ApiProvider) GetChannels(ctx context.Context, channelTypes []string) ([]Channel, error) {
	if len(channelTypes) == 0 {
		channelTypes = AllChanTypes
	}

	chans, err := ap.getChannelsMultiType(ctx, channelTypes)
	if err != nil {
		// Incomplete page must not replace a good snapshot.
		return chans, err
	}

	newSnapshot := newChannelsCache(len(chans))
	for _, ch := range chans {
		newSnapshot.add(ch)
	}
	ap.channelsSnapshot.Store(newSnapshot)

	return chans, nil
}

// UpsertChannel adds one freshly opened conversation to the channels snapshot
// without a full refetch, so the new DM resolves by ID and name immediately.
func (ap *ApiProvider) UpsertChannel(channel *slack.Channel) {
	if channel == nil || channel.ID == "" {
		return
	}
	mapped := MapChannelFromSlack(*channel, ap.ProvideUsersMap().Users)
	for {
		old := ap.channelsSnapshot.Load()
		next := newChannelsCache(len(old.Channels) + 1)
		for id, ch := range old.Channels {
			next.Channels[id] = ch
		}
		for name, id := range old.ChannelsInv {
			next.ChannelsInv[name] = id
		}
		next.add(mapped)
		if ap.channelsSnapshot.CompareAndSwap(old, next) {
			return
		}
	}
}

func (ap *ApiProvider) ProvideChannelsMaps() *ChannelsCache {
	return ap.channelsSnapshot.Load()
}
