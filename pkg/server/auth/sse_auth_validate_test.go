package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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
