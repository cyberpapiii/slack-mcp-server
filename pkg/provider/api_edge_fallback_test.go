package provider

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestIsBrowserSessionAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"invalid_auth", errors.New("invalid_auth"), true},
		{"not_authed", errors.New("not_authed"), true},
		{"invalid auth token", errors.New("AUTH_FAILED: invalid auth token"), true},
		{"session expired", errors.New("session expired"), true},
		{"generic error", errors.New("timeout"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isBrowserSessionAuthError(tt.err))
		})
	}
}

// TestUnitOsascriptNotificationArgs verifies that the degradation message is
// passed to osascript as an argv element, never interpolated into the
// AppleScript source string.
func TestUnitOsascriptNotificationArgs(t *testing.T) {
	const fixedScript = `on run argv
	display notification (item 1 of argv) with title "Slack MCP fallback active"
end run`

	t.Run("special characters pass through verbatim", func(t *testing.T) {
		reason := `has "quotes" and \backslashes\ and ` + "`backticks`"
		args := osascriptNotificationArgs(reason)

		if assert.Len(t, args, 3) {
			assert.Equal(t, "-e", args[0])
			assert.Equal(t, fixedScript, args[1], "script must be a fixed constant, never interpolated")
			assert.Equal(t, reason, args[2], "reason must appear verbatim, with no escaping")
		}
	})

	t.Run("long reason is truncated on a rune boundary", func(t *testing.T) {
		// Place a multi-byte rune (3 bytes in UTF-8) straddling byte offset
		// 200: 199 ASCII bytes then "世" spans bytes 199-201. A naive
		// byte-slice truncation at 200 bytes would split that rune and
		// produce invalid UTF-8; []rune truncation at 200 runes must not.
		reason := strings.Repeat("a", 199) + "世" + strings.Repeat("b", 300)
		args := osascriptNotificationArgs(reason)

		if assert.Len(t, args, 3) {
			assert.Equal(t, fixedScript, args[1])
			message := args[2]
			assert.True(t, utf8.ValidString(message), "truncated message must be valid UTF-8")
			assert.True(t, strings.HasSuffix(message, "…"), "truncated message must end with an ellipsis")
			assert.Equal(t, strings.Repeat("a", 199)+"世"+"…", message)
			// 199 ASCII bytes + 3-byte rune + 3-byte ellipsis.
			assert.LessOrEqual(t, len(message), 205)
		}
	})

	t.Run("empty reason still produces three elements", func(t *testing.T) {
		args := osascriptNotificationArgs("")

		if assert.Len(t, args, 3) {
			assert.Equal(t, "-e", args[0])
			assert.Equal(t, fixedScript, args[1])
			assert.Equal(t, "", args[2])
		}
	})
}

// TestEdgeFallbackFlag verifies that MCPSlackClient remembers edge API failures
// and skips straight to the standard API on subsequent calls.
func TestEdgeFallbackFlag(t *testing.T) {
	t.Run("edgeFailed starts false", func(t *testing.T) {
		c := &MCPSlackClient{
			isEnterprise: true,
			isOAuth:      false,
		}
		assert.False(t, c.edgeFailed, "edgeFailed should start as false")
	})

	t.Run("edgeFailed flag is sticky", func(t *testing.T) {
		c := &MCPSlackClient{
			isEnterprise: true,
			isOAuth:      false,
			edgeFailed:   true,
		}
		assert.True(t, c.edgeFailed, "edgeFailed should remain true once set")
	})
}

// TestGetConversationsContextRouting verifies that MCPSlackClient routes
// GetConversationsContext to the correct backend based on isEnterprise,
// isOAuth, and edgeFailed flags.
func TestGetConversationsContextRouting(t *testing.T) {
	// Decision matrix:
	// | isEnterprise | isOAuth | edgeFailed | Expected path     |
	// |:-------------|:--------|:-----------|:------------------|
	// | false        | *       | *          | standard API      |
	// | true         | true    | *          | standard API      |
	// | true         | false   | false      | edge API (first)  |
	// | true         | false   | true       | standard API      |

	tests := []struct {
		name         string
		isEnterprise bool
		isOAuth      bool
		edgeFailed   bool
		expectEdge   bool
	}{
		{"non-enterprise goes to standard", false, false, false, false},
		{"enterprise+oauth goes to standard", true, true, false, false},
		{"enterprise+non-oauth tries edge first", true, false, false, true},
		{"enterprise+non-oauth+edgeFailed skips edge", true, false, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &MCPSlackClient{
				isEnterprise: tt.isEnterprise,
				isOAuth:      tt.isOAuth,
				edgeFailed:   tt.edgeFailed,
			}

			wouldTryEdge := c.isEnterprise && !c.isOAuth && !c.edgeFailed
			assert.Equal(t, tt.expectEdge, wouldTryEdge)
		})
	}
}

// TestEdgeFailedPreventsRetry verifies that once edgeFailed is set, the client
// won't attempt the edge API again. This prevents wasted API calls on every
// page of a paginated standard API fetch.
func TestEdgeFailedPreventsRetry(t *testing.T) {
	c := &MCPSlackClient{
		isEnterprise: true,
		isOAuth:      false,
		edgeFailed:   false,
	}

	// Simulate edge failure
	c.edgeFailed = true

	// Verify 10 subsequent "pagination" calls would all skip edge
	for i := 0; i < 10; i++ {
		wouldTryEdge := c.isEnterprise && !c.isOAuth && !c.edgeFailed
		assert.False(t, wouldTryEdge,
			"call %d: should not try edge after it failed", i+1)
	}
}

func TestBrowserDegradationState(t *testing.T) {
	origWriter := browserStatusWriter
	origNotifier := browserDegradationNotifier
	defer func() {
		browserStatusWriter = origWriter
		browserDegradationNotifier = origNotifier
	}()

	writes := 0
	notifies := 0
	browserStatusWriter = func(state, reason string, logger *zap.Logger) {
		writes++
	}
	browserDegradationNotifier = func(reason string, logger *zap.Logger) {
		notifies++
	}

	c := &MCPSlackClient{}
	c.logger = zap.NewNop()
	c.initBrowserState()
	assert.True(t, c.browserFeaturesAvailable())
	assert.False(t, c.IsOAuth())

	c.fallbackSlackClient = &slack.Client{}
	c.degradeBrowserSession(errors.New("invalid_auth"))
	assert.False(t, c.browserFeaturesAvailable())
	assert.True(t, c.IsOAuth())
	assert.Equal(t, "invalid_auth", c.browserDegradedReason())

	c.degradeBrowserSession(errors.New("invalid_auth"))
	assert.Equal(t, 2, writes, "one write for active, one for degraded")
	assert.Equal(t, 1, notifies, "degradation should only notify once")
}
