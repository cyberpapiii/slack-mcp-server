package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestUnitCacheDurationsFromEnv(t *testing.T) {
	ttl := func(c cacheConfig) time.Duration { return c.ttl }
	minRefresh := func(c cacheConfig) time.Duration { return c.minRefreshInterval }
	tests := []struct {
		name     string
		key      string
		value    string
		field    func(cacheConfig) time.Duration
		expected time.Duration
	}{
		{"TTL default when unset", "SLACK_MCP_CACHE_TTL", "", ttl, defaultCacheTTL},
		{"TTL duration", "SLACK_MCP_CACHE_TTL", "2h", ttl, 2 * time.Hour},
		{"TTL integer seconds", "SLACK_MCP_CACHE_TTL", "3600", ttl, 3600 * time.Second},
		{"TTL zero disables expiry", "SLACK_MCP_CACHE_TTL", "0", ttl, 0},
		{"TTL invalid falls back", "SLACK_MCP_CACHE_TTL", "invalid", ttl, defaultCacheTTL},
		{"TTL negative duration falls back", "SLACK_MCP_CACHE_TTL", "-1h", ttl, defaultCacheTTL},
		{"TTL negative seconds falls back", "SLACK_MCP_CACHE_TTL", "-3600", ttl, defaultCacheTTL},
		{"min refresh default when unset", "SLACK_MCP_MIN_REFRESH_INTERVAL", "", minRefresh, defaultMinRefreshInterval},
		{"min refresh duration", "SLACK_MCP_MIN_REFRESH_INTERVAL", "1m", minRefresh, time.Minute},
		{"min refresh zero disables the limit", "SLACK_MCP_MIN_REFRESH_INTERVAL", "0", minRefresh, 0},
		{"min refresh negative falls back", "SLACK_MCP_MIN_REFRESH_INTERVAL", "-30s", minRefresh, defaultMinRefreshInterval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			assert.Equal(t, tt.expected, tt.field(cacheConfigFromEnv(zap.NewNop())))
		})
	}
}

// A value that falls back to the default used to do so silently.
func TestUnitBadCacheDurationIsLogged(t *testing.T) {
	t.Setenv("SLACK_MCP_CACHE_TTL", "1 day")
	t.Setenv("SLACK_MCP_MIN_REFRESH_INTERVAL", "-5s")
	core, logs := observer.New(zapcore.WarnLevel)

	config := cacheConfigFromEnv(zap.New(core))

	assert.Equal(t, defaultCacheTTL, config.ttl)
	assert.Equal(t, defaultMinRefreshInterval, config.minRefreshInterval)
	require.Equal(t, 2, logs.Len())
	assert.Contains(t, logs.All()[0].Message, "SLACK_MCP_CACHE_TTL")
	assert.Contains(t, logs.All()[1].Message, "SLACK_MCP_MIN_REFRESH_INTERVAL")
}
