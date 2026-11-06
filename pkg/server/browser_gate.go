package server

import (
	"context"
	"errors"

	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type browserSession interface {
	BrowserFeaturesAvailable() bool
	BrowserDegradedReason() string
}

// requireBrowserSession wraps a tool that needs the browser session. While the
// session is degraded the tool answers browser_session_required without
// running, and a call that finds the session dead answers the same way.
func requireBrowserSession(session browserSession, tool string, next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !session.BrowserFeaturesAvailable() {
			return nil, handler.BrowserSessionRequired(tool, session.BrowserDegradedReason())
		}
		result, err := next(ctx, request)
		if errors.Is(err, provider.ErrBrowserSessionUnavailable) {
			return nil, handler.BrowserSessionRequired(tool, session.BrowserDegradedReason())
		}
		return result, err
	}
}
