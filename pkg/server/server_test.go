package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestValidToolNames(t *testing.T) {
	t.Run("ValidToolNames contains all expected tools", func(t *testing.T) {
		expectedTools := map[string]bool{
			ToolConversationsHistory:        true,
			ToolConversationsReplies:        true,
			ToolConversationsAddMessage:     true,
			ToolConversationsDraftMessage:   true,
			ToolReactionsAdd:                true,
			ToolReactionsRemove:             true,
			ToolReactionsGet:                true,
			ToolConversationsGetMessage:     true,
			ToolAttachmentGetData:           true,
			ToolConversationsSearchMessages: true,
			ToolConversationsUnreads:        true,
			ToolConversationsMark:           true,
			ToolConversationsOpen:           true,
			ToolConversationsLeave:          true,
			ToolConversationsJoin:           true,
			ToolChannelsList:                true,
			ToolChannelsStarred:             true,
			ToolChannelsMe:                  true,
			ToolUsergroupsList:              true,
			ToolUsergroupsMine:              true,
			ToolUsergroupsJoin:              true,
			ToolUsergroupsLeave:             true,
			ToolUsergroupsCreate:            true,
			ToolUsergroupsUpdate:            true,
			ToolUsergroupsUsersUpdate:       true,
			ToolUsersSearch:                 true,
			ToolActivityUnreads:             true,
			ToolActivityMarkRead:            true,
			ToolSavedList:                   true,
			ToolSavedUpdate:                 true,
			ToolSavedClearCompleted:         true,
			ToolFilesList:                   true,
			ToolSlackAuthStatus:             true,
			ToolScheduledMessagesList:       true,
			ToolScheduledMessageCancel:      true,
			ToolChannelsRename:              true,
			ToolChannelsSetTopic:            true,
			ToolChannelsSetPurpose:          true,
			ToolChannelsArchive:             true,
			ToolListsCreate:                 true,
			ToolListsUpdate:                 true,
			ToolListsItemsList:              true,
			ToolListsItemsCreate:            true,
			ToolListsItemsUpdate:            true,
			ToolListsItemDelete:             true,
			ToolDNDGet:                      true,
			ToolDNDSetSnooze:                true,
			ToolDNDEndSnooze:                true,
			ToolFilesUpload:                 true,
			ToolMessagesSchedule:            true,
			ToolMessagesUpdate:              true,
			ToolMessagesDelete:              true,
			ToolChannelsCreate:              true,
			ToolChannelsMembers:             true,
			ToolChannelsInvite:              true,
			ToolEmojiList:                   true,
			ToolUsersGetProfile:             true,
			ToolUsersSetProfile:             true,
			ToolUsersSetStatus:              true,
			ToolCanvasesCreate:              true,
			ToolCanvasesRead:                true,
			ToolCanvasesUpdate:              true,
			ToolDraftsList:                  true,
			ToolDraftsGet:                   true,
			ToolDraftsCreate:                true,
			ToolDraftsUpdate:                true,
			ToolDraftsDelete:                true,
			ToolSearchSemantic:              true,
		}

		assert.Equal(t, len(expectedTools), len(ValidToolNames), "ValidToolNames should have %d tools", len(expectedTools))

		for _, tool := range ValidToolNames {
			assert.True(t, expectedTools[tool], "unexpected tool in ValidToolNames: %s", tool)
		}
	})
}

func TestCapabilityPresets(t *testing.T) {
	daily, err := ResolveToolPreset("daily-power")
	require.NoError(t, err)
	assert.Contains(t, daily, ToolSlackAuthStatus)
	assert.Contains(t, daily, ToolUsergroupsMine)
	assert.NotContains(t, daily, ToolConversationsAddMessage)
	assert.NotContains(t, daily, ToolUsergroupsJoin)

	legacy, err := ResolveToolPreset("legacy-full")
	require.NoError(t, err)
	assert.ElementsMatch(t, ValidToolNames, legacy)

	_, err = ResolveToolPreset("unknown")
	require.Error(t, err)
}

func TestValidateEnabledTools(t *testing.T) {
	t.Run("empty list is valid", func(t *testing.T) {
		err := ValidateEnabledTools([]string{})
		assert.NoError(t, err)
	})

	t.Run("nil list is valid", func(t *testing.T) {
		err := ValidateEnabledTools(nil)
		assert.NoError(t, err)
	})

	t.Run("all valid tool names pass", func(t *testing.T) {
		err := ValidateEnabledTools(ValidToolNames)
		assert.NoError(t, err)
	})

	t.Run("single valid tool passes", func(t *testing.T) {
		err := ValidateEnabledTools([]string{ToolChannelsList})
		assert.NoError(t, err)
	})

	t.Run("single invalid tool fails", func(t *testing.T) {
		err := ValidateEnabledTools([]string{"invalid_tool"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_tool")
		assert.Contains(t, err.Error(), "Valid tools are:")
	})

	t.Run("multiple invalid tools listed in error", func(t *testing.T) {
		err := ValidateEnabledTools([]string{"foo", "bar"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "foo")
		assert.Contains(t, err.Error(), "bar")
	})

	t.Run("mix of valid and invalid tools fails", func(t *testing.T) {
		err := ValidateEnabledTools([]string{ToolChannelsList, "invalid_tool", ToolReactionsAdd})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid tool name(s): invalid_tool.")
	})

	t.Run("typo in tool name fails", func(t *testing.T) {
		err := ValidateEnabledTools([]string{"channel_list"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "channel_list")
	})
}

func TestRegisterCacheDependentTools(t *testing.T) {
	newTestServer := func(enabledTools []string) *MCPServer {
		base := server.NewMCPServer(
			"test-server",
			"1.0.0",
			server.WithToolCapabilities(true),
			server.WithResourceCapabilities(true, true),
		)

		return &MCPServer{
			server:       base,
			logger:       zap.NewNop(),
			workspace:    "test-workspace",
			provider:     &provider.ApiProvider{},
			enabledTools: enabledTools,
		}
	}

	t.Run("registers cache-dependent read-only tools after warmup", func(t *testing.T) {
		srv := newTestServer([]string{ToolChannelsList, ToolChannelsMe, ToolConversationsUnreads, ToolUsersSearch})

		assert.Nil(t, srv.server.ListTools(), "expected no cache-dependent tools before registration")

		srv.RegisterCacheDependentTools()

		tools := srv.server.ListTools()
		require.NotNil(t, tools)
		assert.Contains(t, tools, ToolChannelsList)
		assert.Contains(t, tools, ToolChannelsMe)
		assert.NotContains(t, tools, ToolConversationsUnreads, "unreads requires a configured browser session")
		assert.NotContains(t, tools, ToolUsersSearch, "users_search is registered during initial server setup, not delayed warmup")
	})

	t.Run("empty enabled-tools registers nothing", func(t *testing.T) {
		srv := newTestServer(nil)

		srv.RegisterCacheDependentTools()

		assert.Nil(t, srv.server.ListTools())
	})

	t.Run("honors enabled-tools filter during delayed registration", func(t *testing.T) {
		srv := newTestServer([]string{ToolChannelsList})

		srv.RegisterCacheDependentTools()

		tools := srv.server.ListTools()
		require.NotNil(t, tools)
		assert.Contains(t, tools, ToolChannelsList)
		assert.NotContains(t, tools, ToolConversationsUnreads)
		assert.NotContains(t, tools, ToolConversationsAddMessage)
		assert.Len(t, tools, 1)
	})

	t.Run("second call is idempotent via sync.Once", func(t *testing.T) {
		srv := newTestServer([]string{ToolChannelsList, ToolChannelsMe})

		srv.RegisterCacheDependentTools()
		firstCount := len(srv.server.ListTools())

		srv.RegisterCacheDependentTools()
		assert.Len(t, srv.server.ListTools(), firstCount)
	})
}

func TestAddEnabledTool_RegistersOnlyListedTools(t *testing.T) {
	base := server.NewMCPServer("test-server", "1.0.0", server.WithToolCapabilities(true))
	noop := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
	enabledTools := []string{ToolConversationsHistory, ToolReactionsAdd}

	for _, name := range ValidToolNames {
		addEnabledTool(base, enabledTools, mcp.NewTool(name), noop)
	}

	tools := base.ListTools()
	require.Len(t, tools, len(enabledTools))
	for _, name := range enabledTools {
		assert.Contains(t, tools, name)
	}

	empty := server.NewMCPServer("test-server", "1.0.0", server.WithToolCapabilities(true))
	for _, name := range ValidToolNames {
		addEnabledTool(empty, nil, mcp.NewTool(name), noop)
	}
	assert.Nil(t, empty.ListTools(), "empty enabled-tools list registers no tools")
}

// setupMCPClientServer creates an MCP server with the given options and tool handler,
// wires up a client via stdio pipes, and returns the connected client.
func setupMCPClientServer(t *testing.T, opts []server.ServerOption, toolHandler server.ToolHandlerFunc) *client.Client {
	t.Helper()

	mcpSrv := server.NewMCPServer("test", "1.0.0", opts...)
	mcpSrv.AddTool(mcp.NewTool("test_tool",
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

	t.Run("production stack turns a panic into an isError tool result", func(t *testing.T) {
		c := setupMCPClientServer(t,
			toolHandlerOptions("stdio", logger),
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
			toolHandlerOptions("stdio", logger),
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

	t.Run("without middleware handler error becomes JSON-RPC error", func(t *testing.T) {
		c := setupMCPClientServer(t,
			nil, // no error recovery middleware
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, fmt.Errorf("simulated tool error: invalid channel ID")
			},
		)

		var callReq mcp.CallToolRequest
		callReq.Params.Name = "test_tool"
		result, err := c.CallTool(context.Background(), callReq)

		assert.Error(t, err, "should return a JSON-RPC error without middleware")
		assert.Nil(t, result)
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

func TestUnitGetMessageToolNameRegistered(t *testing.T) {
	assert.Contains(t, ValidToolNames, "conversations_get_message")
}
