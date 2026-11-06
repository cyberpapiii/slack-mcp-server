package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// setupMCPClientServer serves toolHandler as "test_tool" (or toolName, when
// the test needs a real catalog name) over stdio pipes and returns a
// connected client.
func setupMCPClientServer(t *testing.T, opts []server.ServerOption, toolHandler server.ToolHandlerFunc, toolName ...string) *client.Client {
	t.Helper()

	name := "test_tool"
	if len(toolName) > 0 {
		name = toolName[0]
	}
	mcpSrv := server.NewMCPServer("test", "1.0.0", opts...)
	mcpSrv.AddTool(mcp.NewTool(name,
		mcp.WithDescription("A test tool"),
	), toolHandler)

	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stdioSrv := server.NewStdioServer(mcpSrv)
	go func() {
		_ = stdioSrv.Listen(ctx, serverReader, serverWriter)
	}()

	var logBuf bytes.Buffer
	tr := transport.NewIO(clientReader, clientWriter, io.NopCloser(&logBuf))
	err := tr.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { tr.Close() })

	c := client.NewClient(tr)

	var initReq mcp.InitializeRequest
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	_, err = c.Initialize(ctx, initReq)
	require.NoError(t, err)

	return c
}

func TestUnitWorkspaceName(t *testing.T) {
	ws, err := workspaceName("https://acme.slack.com/")
	require.NoError(t, err)
	assert.Equal(t, "acme", ws)
	_, err = workspaceName("https://localhost/")
	assert.Error(t, err)
	_, err = workspaceName("")
	assert.Error(t, err)
}

func TestErrorRecoveryMiddleware(t *testing.T) {
	logger := zap.NewNop()

	t.Run("handler error is converted to isError tool result", func(t *testing.T) {
		c := setupMCPClientServer(t,
			[]server.ServerOption{server.WithToolHandlerMiddleware(buildErrorRecoveryMiddleware(logger))},
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, fmt.Errorf("simulated tool error: invalid channel ID")
			},
		)

		var callReq mcp.CallToolRequest
		callReq.Params.Name = "test_tool"
		result, err := c.CallTool(context.Background(), callReq)

		require.NoError(t, err, "should not return a JSON-RPC error")
		require.NotNil(t, result)
		assert.True(t, result.IsError, "result should have isError=true")
		require.Len(t, result.Content, 1)
		textContent, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok, "content should be TextContent")
		assert.Contains(t, textContent.Text, "simulated tool error: invalid channel ID")
		require.NotNil(t, result.StructuredContent)
		structured, ok := result.StructuredContent.(map[string]any)
		require.True(t, ok)
		errorPayload, ok := structured["error"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "tool_error", errorPayload["code"])
	})

	rateLimitedPayload := func(t *testing.T, tool string, handlerErr error) map[string]any {
		t.Helper()
		c := setupMCPClientServer(t,
			[]server.ServerOption{server.WithToolHandlerMiddleware(buildErrorRecoveryMiddleware(logger))},
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, handlerErr
			},
			tool,
		)
		var callReq mcp.CallToolRequest
		callReq.Params.Name = tool
		result, err := c.CallTool(context.Background(), callReq)
		require.NoError(t, err)
		require.True(t, result.IsError)
		structured, ok := result.StructuredContent.(map[string]any)
		require.True(t, ok)
		payload, ok := structured["error"].(map[string]any)
		require.True(t, ok)
		return payload
	}

	// A 429 on a read tool used to reach the agent as tool_error with
	// retryable=false, so agents gave up instead of waiting it out.
	t.Run("rate limit on a read tool is retryable with its delay", func(t *testing.T) {
		payload := rateLimitedPayload(t, "conversations_search_messages",
			fmt.Errorf("search: %w", &slack.RateLimitedError{RetryAfter: 30 * time.Second}))
		assert.Equal(t, "rate_limited", payload["code"])
		assert.Equal(t, true, payload["retryable"])
		assert.EqualValues(t, 30, payload["retry_after_seconds"])
	})

	// A mutating handler that returns a raw 429 may have applied an earlier
	// step, so the code and delay are reported but retry is not promised.
	t.Run("rate limit on a write tool is not promised retryable", func(t *testing.T) {
		payload := rateLimitedPayload(t, "conversations_add_message",
			&slack.RateLimitedError{RetryAfter: 5 * time.Second})
		assert.Equal(t, "rate_limited", payload["code"])
		assert.Equal(t, false, payload["retryable"])
		assert.EqualValues(t, 5, payload["retry_after_seconds"])
	})

	t.Run("production stack turns a panic into an isError tool result", func(t *testing.T) {
		c := setupMCPClientServer(t,
			toolHandlerOptions("stdio", logger, false),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				panic("nil map write in handler")
			},
		)

		var callReq mcp.CallToolRequest
		callReq.Params.Name = "test_tool"
		result, err := c.CallTool(context.Background(), callReq)

		require.NoError(t, err, "a handler panic must not surface as a JSON-RPC error")
		require.NotNil(t, result)
		assert.True(t, result.IsError)
		require.Len(t, result.Content, 1)
		textContent, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok)
		assert.Contains(t, textContent.Text, "nil map write in handler")
	})

	t.Run("production stack preserves ToolError code and retryable", func(t *testing.T) {
		c := setupMCPClientServer(t,
			toolHandlerOptions("stdio", logger, false),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, &handler.ToolError{Code: "rate_limited", Message: "slow down", Retryable: true, RetryAfter: 3 * time.Second}
			},
		)

		var callReq mcp.CallToolRequest
		callReq.Params.Name = "test_tool"
		result, err := c.CallTool(context.Background(), callReq)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
		structured, ok := result.StructuredContent.(map[string]any)
		require.True(t, ok)
		errorPayload, ok := structured["error"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "rate_limited", errorPayload["code"])
		assert.Equal(t, true, errorPayload["retryable"])
		assert.EqualValues(t, 3, errorPayload["retry_after_seconds"])
	})

	t.Run("successful tool call passes through unchanged", func(t *testing.T) {
		c := setupMCPClientServer(t,
			[]server.ServerOption{server.WithToolHandlerMiddleware(buildErrorRecoveryMiddleware(logger))},
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultText("all good"), nil
			},
		)

		var callReq mcp.CallToolRequest
		callReq.Params.Name = "test_tool"
		result, err := c.CallTool(context.Background(), callReq)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError, "successful result should not have isError=true")
		require.Len(t, result.Content, 1)
		textContent, ok := result.Content[0].(mcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, "all good", textContent.Text)
	})
}
