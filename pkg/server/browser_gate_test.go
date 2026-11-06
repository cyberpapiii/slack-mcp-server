package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBrowserSession struct {
	healthy bool
	reason  string
}

func (f *fakeBrowserSession) BrowserFeaturesAvailable() bool { return f.healthy }
func (f *fakeBrowserSession) BrowserDegradedReason() string  { return f.reason }

func TestUnitBrowserGateRefusesWhileDegradedAndNamesTheTool(t *testing.T) {
	session := &fakeBrowserSession{reason: "invalid_auth"}
	ran := false
	run := requireBrowserSession(session, "saved_list", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ran = true
		return nil, nil
	})

	_, err := run(context.Background(), mcp.CallToolRequest{})
	assert.False(t, ran, "a degraded session never reaches Slack")
	var typed *handler.ToolError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "browser_session_required", typed.Code)
	assert.Equal(t, "saved_list needs a Slack browser session (xoxc/xoxd): invalid_auth. Check slack_auth_status; token-only tools still work.", typed.Message)
}

func TestUnitBrowserGateNamesTheToolWhenACallFindsTheSessionDead(t *testing.T) {
	session := &fakeBrowserSession{healthy: true}
	run := requireBrowserSession(session, "conversations_unreads", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		session.healthy, session.reason = false, "token_revoked"
		return nil, fmt.Errorf("get Slack unread state: %w", provider.ErrBrowserSessionUnavailable)
	})

	_, err := run(context.Background(), mcp.CallToolRequest{})
	var typed *handler.ToolError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "browser_session_required", typed.Code)
	assert.Contains(t, typed.Message, "conversations_unreads needs a Slack browser session (xoxc/xoxd): token_revoked.")
}

func TestUnitBrowserGatePassesOtherErrorsThrough(t *testing.T) {
	failure := errors.New("channel_not_found")
	run := requireBrowserSession(&fakeBrowserSession{healthy: true}, "drafts_list", func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, failure
	})
	_, err := run(context.Background(), mcp.CallToolRequest{})
	assert.Same(t, failure, err)
}
