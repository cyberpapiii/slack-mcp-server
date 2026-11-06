package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

// RefreshChannels loads the channels cache file, or fetches the channels when
// the file is missing or unreadable. An expired file is served while a
// background fetch replaces it.
func (ap *ApiProvider) RefreshChannels(ctx context.Context) error {
	return ap.refreshChannelsInternal(ctx, false)
}

// ForceRefreshChannels bypasses cache; rate-limited by minRefreshInterval (ErrRefreshRateLimited).
func (ap *ApiProvider) ForceRefreshChannels(ctx context.Context) error {
	if ap.minRefreshInterval > 0 {
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
					c = withIMPeer(c, usersMap)
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
	channels, err := ap.fetchChannels(ctx)

	// Prefer the real fetch error over a misleading "zero channels" when page 1
	// fails. The existing snapshot stays in service either way; the error still
	// reaches the caller, which would otherwise report a channel the refresh
	// never saw as not found.
	if err != nil {
		if ap.channelsReady.Load() {
			return fmt.Errorf("channel fetch incomplete after %d channels; keeping the existing cache: %w", len(channels), err)
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

// Partial pages returned on error — callers must not treat that as a complete list.
func (ap *ApiProvider) getChannelsMultiType(ctx context.Context, channelTypes []string) ([]Channel, error) {
	params := &slack.GetConversationsParameters{
		Types:           channelTypes,
		Limit:           999,
		ExcludeArchived: true,
	}

	var chans []Channel

	usersMap := ap.ProvideUsersMap().Users
	for {
		// The client paces conversations.list on Tier2 and retries a 429;
		// faster budgets caused 429 truncations.
		pageChannels, cursor, err := ap.api.GetConversationsContext(ctx, params)
		ap.logger.Debug("Fetched channels",
			zap.Strings("channelTypes", channelTypes),
			zap.Int("count", len(pageChannels)),
		)
		if err != nil {
			ap.logger.Error("Failed to fetch channels, returning partial result",
				zap.Strings("channelTypes", channelTypes),
				zap.Int("collectedSoFar", len(chans)),
				zap.Error(err))
			return chans, err
		}

		for _, channel := range pageChannels {
			chans = append(chans, MapChannelFromSlack(channel, usersMap))
		}

		if cursor == "" {
			break
		}

		params.Cursor = cursor
	}
	return chans, nil
}

func (ap *ApiProvider) fetchChannels(ctx context.Context) ([]Channel, error) {
	chans, err := ap.getChannelsMultiType(ctx, AllChanTypes)
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
