package handler

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestUnitLogToolCall(t *testing.T) {
	newRequest := func() mcp.CallToolRequest {
		req := mcp.CallToolRequest{}
		req.Params.Name = "conversations_add_message"
		req.Params.Arguments = map[string]any{
			"channel_id":   "C123",
			"payload":      "secret message text",
			"content_type": "text/markdown",
		}
		return req
	}

	hasParams := func(entry observer.LoggedEntry) bool {
		for _, f := range entry.Context {
			if f.Key == "params" {
				return true
			}
		}
		return false
	}

	tests := []struct {
		name       string
		envValue   string
		setEnv     bool
		wantParams bool
	}{
		{name: "gate open with debug", envValue: "debug", setEnv: true, wantParams: true},
		{name: "gate open with DEBUG case variance", envValue: "DEBUG", setEnv: true, wantParams: true},
		{name: "gate closed when unset", setEnv: false, wantParams: false},
		{name: "gate closed for other value", envValue: "info", setEnv: true, wantParams: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("SLACK_MCP_LOG_PARAMS", tt.envValue)
			} else {
				t.Setenv("SLACK_MCP_LOG_PARAMS", "")
			}

			core, logs := observer.New(zapcore.DebugLevel)
			logger := zap.New(core)

			logToolCall(logger, "ConversationsAddMessageHandler called", newRequest())

			entries := logs.All()
			require.Len(t, entries, 1, "expected exactly one log entry")
			assert.Equal(t, "ConversationsAddMessageHandler called", entries[0].Message)
			assert.Equal(t, zapcore.DebugLevel, entries[0].Level)
			assert.Equal(t, tt.wantParams, hasParams(entries[0]))
		})
	}
}

func TestUnitLogResourceCall(t *testing.T) {
	newRequest := func() mcp.ReadResourceRequest {
		req := mcp.ReadResourceRequest{}
		req.Params.URI = "slack://my-workspace/channels"
		return req
	}

	hasParams := func(entry observer.LoggedEntry) bool {
		for _, f := range entry.Context {
			if f.Key == "params" {
				return true
			}
		}
		return false
	}

	t.Run("gate open", func(t *testing.T) {
		t.Setenv("SLACK_MCP_LOG_PARAMS", "debug")
		core, logs := observer.New(zapcore.DebugLevel)

		logResourceCall(zap.New(core), "ChannelsResource called", newRequest())

		entries := logs.All()
		require.Len(t, entries, 1)
		assert.True(t, hasParams(entries[0]))
	})

	t.Run("gate closed", func(t *testing.T) {
		t.Setenv("SLACK_MCP_LOG_PARAMS", "")
		core, logs := observer.New(zapcore.DebugLevel)

		logResourceCall(zap.New(core), "ChannelsResource called", newRequest())

		entries := logs.All()
		require.Len(t, entries, 1)
		assert.Equal(t, "ChannelsResource called", entries[0].Message)
		assert.False(t, hasParams(entries[0]))
	})
}

// Gate must re-read env each call (no package-level cache).
func TestUnitLogToolCallReadsEnvPerCall(t *testing.T) {
	t.Setenv("SLACK_MCP_LOG_PARAMS", "")
	assert.False(t, logParamsEnabled())

	t.Setenv("SLACK_MCP_LOG_PARAMS", "debug")
	assert.True(t, logParamsEnabled())

	t.Setenv("SLACK_MCP_LOG_PARAMS", "")
	assert.False(t, logParamsEnabled())
}
