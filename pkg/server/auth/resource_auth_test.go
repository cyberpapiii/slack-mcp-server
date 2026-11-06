package auth

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Resource reads used to check auth inside each handler; the server now runs
// this middleware in front of every resource.
func TestUnitResourceMiddlewareRequiresTheAPIKey(t *testing.T) {
	t.Setenv("SLACK_MCP_API_KEY", "my-secret")
	read := false
	next := func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		read = true
		return nil, nil
	}
	guarded := BuildResourceMiddleware("http", zap.NewNop())(next)

	_, err := guarded(withToken("Bearer wrong"), mcp.ReadResourceRequest{})
	require.EqualError(t, err, "unauthorized")
	assert.False(t, read)

	_, err = guarded(withToken("Bearer my-secret"), mcp.ReadResourceRequest{})
	require.NoError(t, err)
	assert.True(t, read)

	read = false
	_, err = BuildResourceMiddleware("stdio", zap.NewNop())(next)(context.Background(), mcp.ReadResourceRequest{})
	require.NoError(t, err)
	assert.True(t, read, "stdio needs no key")
}
