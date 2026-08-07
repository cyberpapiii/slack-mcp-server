package provider

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetCacheTTL tests the app-specific logic in getCacheTTL:
// - Default when env not set
// - Numeric seconds fallback (app-specific parsing path)
// - Invalid input handling
// - Negative value rejection (P1 bug fix)
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
			oldVal := os.Getenv("SLACK_MCP_CACHE_TTL")
			defer os.Setenv("SLACK_MCP_CACHE_TTL", oldVal)

			if tt.envValue == "" {
				os.Unsetenv("SLACK_MCP_CACHE_TTL")
			} else {
				os.Setenv("SLACK_MCP_CACHE_TTL", tt.envValue)
			}

			result := getCacheTTL()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestChannelCacheRoundTrip verifies that Channel structs survive JSON serialization.
// This catches bugs in struct tags or field types that would corrupt cache data.
func TestChannelCacheRoundTrip(t *testing.T) {
	original := []Channel{
		{
			ID:          "C123",
			Name:        "#general",
			Topic:       "General discussion",
			Purpose:     "Company-wide announcements",
			MemberCount: 100,
			IsPrivate:   false,
		},
		{
			ID:        "D456",
			Name:      "@john.doe",
			IsIM:      true,
			IsPrivate: true,
			User:      "U789",
		},
		{
			ID:        "G789",
			Name:      "#private-team",
			IsPrivate: true,
			IsMpIM:    false,
			Members:   []string{"U001", "U002"},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var loaded []Channel
	err = json.Unmarshal(data, &loaded)
	require.NoError(t, err)

	require.Len(t, loaded, 3)

	// Verify public channel
	assert.Equal(t, "C123", loaded[0].ID)
	assert.Equal(t, "#general", loaded[0].Name)
	assert.Equal(t, 100, loaded[0].MemberCount)
	assert.False(t, loaded[0].IsPrivate)

	// Verify IM channel
	assert.Equal(t, "D456", loaded[1].ID)
	assert.True(t, loaded[1].IsIM)
	assert.Equal(t, "U789", loaded[1].User)

	// Verify private channel with members
	assert.True(t, loaded[2].IsPrivate)
	assert.Equal(t, []string{"U001", "U002"}, loaded[2].Members)
}

// TestGetCacheDir verifies the cache directory is created correctly.
func TestGetCacheDir(t *testing.T) {
	dir := getCacheDir()

	assert.NotEmpty(t, dir, "cache dir should not be empty")
	assert.Contains(t, dir, "slack-mcp-server", "cache dir should contain app name")

	// Directory should exist after getCacheDir() creates it
	info, err := os.Stat(dir)
	require.NoError(t, err, "cache directory should exist")
	assert.True(t, info.IsDir(), "cache path should be a directory")
}

// TestGetMinRefreshInterval tests the rate limiting configuration parsing.
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
			oldVal := os.Getenv("SLACK_MCP_MIN_REFRESH_INTERVAL")
			defer os.Setenv("SLACK_MCP_MIN_REFRESH_INTERVAL", oldVal)

			if tt.envValue == "" {
				os.Unsetenv("SLACK_MCP_MIN_REFRESH_INTERVAL")
			} else {
				os.Setenv("SLACK_MCP_MIN_REFRESH_INTERVAL", tt.envValue)
			}

			result := getMinRefreshInterval()
			assert.Equal(t, tt.expected, result)
		})
	}
}
