package provider

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCacheTTL(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected time.Duration
	}{
		{
			name:     "default when env not set",
			envValue: "",
			expected: defaultCacheTTL,
		},
		{
			name:     "valid duration passes through",
			envValue: "2h",
			expected: 2 * time.Hour,
		},
		{
			name:     "numeric seconds fallback path",
			envValue: "3600",
			expected: 3600 * time.Second,
		},
		{
			name:     "zero disables TTL",
			envValue: "0",
			expected: 0,
		},
		{
			name:     "invalid input falls back to default",
			envValue: "invalid",
			expected: defaultCacheTTL,
		},
		{
			name:     "negative duration rejected - falls back to default",
			envValue: "-1h",
			expected: defaultCacheTTL,
		},
		{
			name:     "negative seconds rejected - falls back to default",
			envValue: "-3600",
			expected: defaultCacheTTL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SLACK_MCP_CACHE_TTL", tt.envValue)
			assert.Equal(t, tt.expected, getCacheTTL())
		})
	}
}

func TestGetCacheDir(t *testing.T) {
	dir := getCacheDir()

	assert.NotEmpty(t, dir, "cache dir should not be empty")
	assert.Contains(t, dir, "slack-mcp-server", "cache dir should contain app name")

	info, err := os.Stat(dir)
	require.NoError(t, err, "cache directory should exist")
	assert.True(t, info.IsDir(), "cache path should be a directory")
}

func TestGetMinRefreshInterval(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected time.Duration
	}{
		{
			name:     "default when env not set",
			envValue: "",
			expected: defaultMinRefreshInterval,
		},
		{
			name:     "valid duration",
			envValue: "1m",
			expected: 1 * time.Minute,
		},
		{
			name:     "numeric seconds",
			envValue: "60",
			expected: 60 * time.Second,
		},
		{
			name:     "zero disables rate limiting",
			envValue: "0",
			expected: 0,
		},
		{
			name:     "invalid input falls back to default",
			envValue: "invalid",
			expected: defaultMinRefreshInterval,
		},
		{
			name:     "negative duration rejected",
			envValue: "-30s",
			expected: defaultMinRefreshInterval,
		},
		{
			name:     "negative seconds rejected",
			envValue: "-60",
			expected: defaultMinRefreshInterval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SLACK_MCP_MIN_REFRESH_INTERVAL", tt.envValue)
			assert.Equal(t, tt.expected, getMinRefreshInterval())
		})
	}
}
