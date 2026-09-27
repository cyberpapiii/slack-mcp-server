package server

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func runLoggerMiddleware(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	handler := buildLoggerMiddleware(zap.New(core))(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	req := mcp.CallToolRequest{}
	req.Params.Name = "conversations_replies"
	_, err := handler(context.Background(), req)
	assert.NoError(t, err)
	return logs
}

// One Info line per call: "Request finished" carries the tool, duration and
// error flag, so a separate "Request received" line only doubles the volume.
func TestUnitLoggerMiddlewareOneInfoLinePerCall(t *testing.T) {
	logs := runLoggerMiddleware(t)
	if assert.Equal(t, 1, logs.Len()) {
		entry := logs.All()[0]
		assert.Equal(t, "Request finished", entry.Message)
		assert.Equal(t, "conversations_replies", entry.ContextMap()["tool"])
	}
}

func TestUnitLoggerMiddlewareLogsParamsAtInfoWhenOptedIn(t *testing.T) {
	t.Setenv("SLACK_MCP_LOG_PARAMS", "debug")
	logs := runLoggerMiddleware(t)
	received := logs.FilterMessage("Request received").All()
	if assert.Len(t, received, 1) {
		assert.Contains(t, received[0].ContextMap(), "params")
	}
}
