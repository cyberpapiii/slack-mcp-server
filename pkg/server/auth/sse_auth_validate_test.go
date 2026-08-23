package auth

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestUnitConstantTimeEqualAPIKey(t *testing.T) {
	assert.True(t, constantTimeEqualAPIKey("secret", "secret"))
	assert.False(t, constantTimeEqualAPIKey("secret", "secre"))
	assert.False(t, constantTimeEqualAPIKey("short", "longer-key"))
	assert.False(t, constantTimeEqualAPIKey("", "x"))
}

func TestUnitValidateToken(t *testing.T) {
	logger := zap.NewNop()

	t.Run("empty key without opt-out fails closed", func(t *testing.T) {
		t.Setenv("SLACK_MCP_API_KEY", "")
		t.Setenv("SLACK_MCP_SSE_API_KEY", "")
		t.Setenv("SLACK_MCP_ALLOW_UNAUTHENTICATED", "")

		ok, err := validateToken(context.Background(), logger)
		assert.False(t, ok)
		require.Error(t, err)
		assert.Equal(t, "unauthorized", err.Error())
	})

	t.Run("empty key with opt-out passes", func(t *testing.T) {
		t.Setenv("SLACK_MCP_API_KEY", "")
		t.Setenv("SLACK_MCP_SSE_API_KEY", "")
		t.Setenv("SLACK_MCP_ALLOW_UNAUTHENTICATED", "true")

		ok, err := validateToken(context.Background(), logger)
		assert.True(t, ok)
		require.NoError(t, err)
	})

	t.Run("matching bearer token passes", func(t *testing.T) {
		t.Setenv("SLACK_MCP_API_KEY", "my-secret")
		t.Setenv("SLACK_MCP_ALLOW_UNAUTHENTICATED", "")

		ctx := withAuthKey(context.Background(), "Bearer my-secret")
		ok, err := validateToken(ctx, logger)
		assert.True(t, ok)
		require.NoError(t, err)
	})

	t.Run("wrong token returns opaque unauthorized", func(t *testing.T) {
		t.Setenv("SLACK_MCP_API_KEY", "my-secret")
		t.Setenv("SLACK_MCP_ALLOW_UNAUTHENTICATED", "")

		ctx := withAuthKey(context.Background(), "Bearer wrong")
		ok, err := validateToken(ctx, logger)
		assert.False(t, ok)
		require.Error(t, err)
		assert.Equal(t, "unauthorized", err.Error())
	})

	t.Run("missing token returns opaque unauthorized", func(t *testing.T) {
		t.Setenv("SLACK_MCP_API_KEY", "my-secret")
		t.Setenv("SLACK_MCP_ALLOW_UNAUTHENTICATED", "")

		ok, err := validateToken(context.Background(), logger)
		assert.False(t, ok)
		require.Error(t, err)
		assert.Equal(t, "unauthorized", err.Error())
	})
}

func TestUnitFailedAuthLogsOnce(t *testing.T) {
	t.Setenv("SLACK_MCP_API_KEY", "")
	t.Setenv("SLACK_MCP_SSE_API_KEY", "my-secret")
	t.Setenv("SLACK_MCP_ALLOW_UNAUTHENTICATED", "")

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	next := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil }
	ctx := withAuthKey(context.Background(), "Bearer wrong")
	_, err := BuildMiddleware("sse", logger)(next)(ctx, mcp.CallToolRequest{})
	require.EqualError(t, err, "unauthorized")

	var warns, errs int
	for _, e := range logs.All() {
		switch e.Level {
		case zapcore.WarnLevel:
			warns++
		case zapcore.ErrorLevel:
			errs++
		}
	}
	assert.Equal(t, 1, warns, "one warn naming the reason: %v", logs.All())
	assert.Equal(t, 0, errs, "a client's bad token is not a server error")
	assert.Equal(t, "Invalid auth token provided", logs.FilterLevelExact(zapcore.WarnLevel).All()[0].Message, "the deprecated-key warning stays at startup, not per request")
}
