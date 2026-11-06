package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Regression for null cache poisoning: when GetChannels returned nil,
// json.Marshal(nil) wrote "null" to the cache file, and the next startup
// unmarshalled it into an empty slice and served that as valid cached data.
func TestUnitLoadCacheFileTreatsEmptyOrUnreadableFilesAsAMiss(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"null":    "null",
		"empty":   "[]",
		"corrupt": `[{"id":`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".json")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			items, _, ok := loadCacheFile[Channel](path, time.Hour, zap.NewNop())
			assert.False(t, ok, "the caller must refetch")
			assert.Empty(t, items)
		})
	}

	t.Run("missing", func(t *testing.T) {
		_, _, ok := loadCacheFile[Channel](filepath.Join(dir, "missing.json"), time.Hour, zap.NewNop())
		assert.False(t, ok)
	})

	t.Run("populated", func(t *testing.T) {
		path := filepath.Join(dir, "good.json")
		require.NoError(t, os.WriteFile(path, []byte(`[{"id":"C1"}]`), 0o600))
		items, expired, ok := loadCacheFile[Channel](path, time.Hour, zap.NewNop())
		require.True(t, ok)
		assert.False(t, expired)
		require.Len(t, items, 1)
		assert.Equal(t, "C1", items[0].ID)
	})
}

// The other half of the poisoning fix: a fetch that returns no channels must
// not overwrite a good cache file.
func TestUnitZeroChannelFetchDoesNotOverwriteTheCacheFile(t *testing.T) {
	client := &fakeChannelsClient{pages: []channelsPage{{channels: []slack.Channel{}}}}
	ap := newTestApiProvider(client, emptyUsersCache())
	ap.channelsCachePath = filepath.Join(t.TempDir(), "channels.json")
	good := []byte(`[{"id":"C1","name":"#general"}]`)
	require.NoError(t, os.WriteFile(ap.channelsCachePath, good, 0o600))

	err := ap.fetchAndStoreChannels(context.Background())
	require.Error(t, err, "with no cache in service, zero channels is a failure")

	ap.channelsReady.Store(true)
	require.NoError(t, ap.fetchAndStoreChannels(context.Background()), "the existing cache stays in service")

	data, err := os.ReadFile(ap.channelsCachePath)
	require.NoError(t, err)
	assert.Equal(t, good, data)
}
