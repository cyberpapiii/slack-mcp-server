package handler

import (
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// logParamsEnabled reports whether full request parameters may be written to
// the log. It reads SLACK_MCP_LOG_PARAMS on every call (rather than caching)
// so the gate stays testable with t.Setenv.
func logParamsEnabled() bool {
	return strings.EqualFold(os.Getenv("SLACK_MCP_LOG_PARAMS"), "debug")
}

// logGatedParams logs event at Debug level, attaching params only when the
// SLACK_MCP_LOG_PARAMS=debug opt-in is set.
func logGatedParams(logger *zap.Logger, event string, params any) {
	if logParamsEnabled() {
		logger.Debug(event, zap.Any("params", params))
		return
	}
	logger.Debug(event)
}

// logToolCall logs a tool invocation. The full request params are included
// only when SLACK_MCP_LOG_PARAMS=debug, mirroring the HTTP middleware gate
// in pkg/server; params can contain message text and search queries.
func logToolCall(logger *zap.Logger, event string, request mcp.CallToolRequest) {
	logGatedParams(logger, event, request.Params)
}

// logResourceCall is logToolCall for resource reads, which carry a different
// request type but the same disclosure risk.
func logResourceCall(logger *zap.Logger, event string, request mcp.ReadResourceRequest) {
	logGatedParams(logger, event, request.Params)
}
