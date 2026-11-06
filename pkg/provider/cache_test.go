package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
