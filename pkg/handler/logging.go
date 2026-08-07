package handler

import (
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// logParamsEnabled reads SLACK_MCP_LOG_PARAMS each call (no cache) so t.Setenv stays testable.
func logParamsEnabled() bool {
	return strings.EqualFold(os.Getenv("SLACK_MCP_LOG_PARAMS"), "debug")
}

func logGatedParams(logger *zap.Logger, event string, params any) {
	if logParamsEnabled() {
		logger.Debug(event, zap.Any("params", params))
		return
	}
	logger.Debug(event)
}

// logToolCall attaches request.Params only when SLACK_MCP_LOG_PARAMS=debug (may include message text).
func logToolCall(logger *zap.Logger, event string, request mcp.CallToolRequest) {
	logGatedParams(logger, event, request.Params)
}

func logResourceCall(logger *zap.Logger, event string, request mcp.ReadResourceRequest) {
	logGatedParams(logger, event, request.Params)
}
