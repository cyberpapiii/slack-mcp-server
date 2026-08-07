package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestUnitRequireAPIKeyOrOptOut(t *testing.T) {
	tests := []struct {
		name           string
		apiKey         string
		sseAPIKey      string
		allowUnauth    string
		wantErr        bool
		wantErrContain string
		wantWarn       bool
	}{
		{
			name:           "no key, no opt-out returns error naming both env vars",
			apiKey:         "",
			sseAPIKey:      "",
			allowUnauth:    "",
			wantErr:        true,
			wantErrContain: "SLACK_MCP_API_KEY",
		},
		{
			name:        "SLACK_MCP_API_KEY set returns nil",
			apiKey:      "my-secret",
			sseAPIKey:   "",
			allowUnauth: "",
			wantErr:     false,
		},
		{
			name:        "deprecated SLACK_MCP_SSE_API_KEY set returns nil",
			apiKey:      "",
			sseAPIKey:   "my-secret",
			allowUnauth: "",
			wantErr:     false,
		},
		{
			name:        "no key, opt-out true returns nil and warns",
			apiKey:      "",
			sseAPIKey:   "",
			allowUnauth: "true",
			wantErr:     false,
			wantWarn:    true,
		},
		{
			name:           "no key, opt-out '1' is not accepted",
			apiKey:         "",
			sseAPIKey:      "",
			allowUnauth:    "1",
			wantErr:        true,
			wantErrContain: "SLACK_MCP_ALLOW_UNAUTHENTICATED",
		},
		{
			name:           "no key, opt-out 'yes' is not accepted",
			apiKey:         "",
			sseAPIKey:      "",
			allowUnauth:    "yes",
			wantErr:        true,
			wantErrContain: "SLACK_MCP_ALLOW_UNAUTHENTICATED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SLACK_MCP_API_KEY", tt.apiKey)
			t.Setenv("SLACK_MCP_SSE_API_KEY", tt.sseAPIKey)
			t.Setenv("SLACK_MCP_ALLOW_UNAUTHENTICATED", tt.allowUnauth)

			core, logs := observer.New(zapcore.DebugLevel)
			logger := zap.New(core)

			err := RequireAPIKeyOrOptOut(logger)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContain != "" {
					assert.Contains(t, err.Error(), tt.wantErrContain)
				}
			} else {
				require.NoError(t, err)
			}

			warnFound := false
			for _, entry := range logs.All() {
				if entry.Level == zapcore.WarnLevel {
					warnFound = true
					break
				}
			}
			assert.Equal(t, tt.wantWarn, warnFound, "unexpected warn-log presence")
		})
	}
}
