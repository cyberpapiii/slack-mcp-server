package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestShouldAddTool_ReadOnly_EmptyEnabledTools(t *testing.T) {
	t.Run("all read-only tools registered with empty enabledTools", func(t *testing.T) {
		readOnlyTools := []string{
			ToolConversationsHistory,
			ToolConversationsReplies,
			ToolReactionsGet,
			ToolConversationsSearchMessages,
			ToolChannelsList,
			ToolUsersSearch,
		}
		for _, tool := range readOnlyTools {
			result := shouldAddTool(tool, []string{}, "")
			assert.True(t, result, "tool %s should be registered when enabledTools is empty", tool)
		}
	})

	t.Run("all read-only tools registered with nil enabledTools", func(t *testing.T) {
		result := shouldAddTool(ToolConversationsHistory, nil, "")
		assert.True(t, result, "tool should be registered when enabledTools is nil")
	})

	t.Run("unknown tools also registered with empty enabledTools", func(t *testing.T) {
		result := shouldAddTool("future_new_tool", []string{}, "")
		assert.True(t, result, "unknown tools should be registered when enabledTools is empty")
	})
}

func TestShouldAddTool_ReadOnly_ExplicitEnabledTools(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		enabledTools []string
		expected     bool
	}{
		{
			name:         "tool in enabledTools list is registered",
			toolName:     ToolConversationsHistory,
			enabledTools: []string{ToolConversationsHistory, ToolChannelsList},
			expected:     true,
		},
		{
			name:         "tool not in enabledTools list is not registered",
			toolName:     ToolConversationsAddMessage,
			enabledTools: []string{ToolConversationsHistory, ToolChannelsList},
			expected:     false,
		},
		{
			name:         "read-only tool blocked when not in explicit list",
			toolName:     ToolConversationsHistory,
			enabledTools: []string{ToolChannelsList},
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldAddTool(tt.toolName, tt.enabledTools, "")
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldAddTool_SingleToolEnabled(t *testing.T) {
	enabledTools := []string{ToolChannelsList}

	for _, tool := range ValidToolNames {
		result := shouldAddTool(tool, enabledTools, "")
		if tool == ToolChannelsList {
			assert.True(t, result, "channels_list should be registered")
		} else {
			assert.False(t, result, "%s should NOT be registered when only channels_list is enabled", tool)
		}
	}
}

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
			ToolUsergroupsMe:                true,
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

func TestShouldAddTool_WriteTool_AddMessage(t *testing.T) {
	t.Run("empty enabledTools and empty env var - not registered", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")

		result := shouldAddTool(ToolConversationsAddMessage, []string{}, "SLACK_MCP_ADD_MESSAGE_TOOL")
		assert.False(t, result, "write tool should NOT be registered when both enabledTools is empty and env var is not set")
	})

	t.Run("empty enabledTools and env var set to true - registered", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")

		result := shouldAddTool(ToolConversationsAddMessage, []string{}, "SLACK_MCP_ADD_MESSAGE_TOOL")
		assert.True(t, result, "write tool should be registered when enabledTools is empty but env var is set")
	})

	t.Run("empty enabledTools and env var set to channel list - registered", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C123,C456")

		result := shouldAddTool(ToolConversationsAddMessage, []string{}, "SLACK_MCP_ADD_MESSAGE_TOOL")
		assert.True(t, result, "write tool should be registered when enabledTools is empty but env var has channel list")
	})

	t.Run("explicit enabledTools includes tool and empty env var - registered", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")

		result := shouldAddTool(ToolConversationsAddMessage, []string{ToolConversationsAddMessage}, "SLACK_MCP_ADD_MESSAGE_TOOL")
		assert.True(t, result, "write tool should be registered when explicitly in enabledTools even without env var")
	})

	t.Run("explicit enabledTools excludes tool - not registered even with env var", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")

		result := shouldAddTool(ToolConversationsAddMessage, []string{ToolConversationsHistory}, "SLACK_MCP_ADD_MESSAGE_TOOL")
		assert.False(t, result, "write tool should NOT be registered when not in explicit enabledTools list")
	})
}

func TestShouldAddTool_WriteTool_Reactions(t *testing.T) {
	t.Run("empty enabledTools and no env var - not registered", func(t *testing.T) {
		t.Setenv("SLACK_MCP_REACTION_TOOL", "")

		result := shouldAddTool(ToolReactionsAdd, []string{}, "SLACK_MCP_REACTION_TOOL")
		assert.False(t, result, "reactions_add should NOT be registered when env var is not set")

		result = shouldAddTool(ToolReactionsRemove, []string{}, "SLACK_MCP_REACTION_TOOL")
		assert.False(t, result, "reactions_remove should NOT be registered when env var is not set")
	})

	t.Run("empty enabledTools and env var set - registered", func(t *testing.T) {
		t.Setenv("SLACK_MCP_REACTION_TOOL", "true")

		result := shouldAddTool(ToolReactionsAdd, []string{}, "SLACK_MCP_REACTION_TOOL")
		assert.True(t, result, "reactions_add should be registered when env var is set")

		result = shouldAddTool(ToolReactionsRemove, []string{}, "SLACK_MCP_REACTION_TOOL")
		assert.True(t, result, "reactions_remove should be registered when env var is set")
	})

	t.Run("explicit enabledTools includes tool - registered without env var", func(t *testing.T) {
		t.Setenv("SLACK_MCP_REACTION_TOOL", "")

		result := shouldAddTool(ToolReactionsAdd, []string{ToolReactionsAdd}, "SLACK_MCP_REACTION_TOOL")
		assert.True(t, result, "reactions_add should be registered when explicitly in enabledTools")
	})
}

func TestShouldAddTool_WriteTool_Attachment(t *testing.T) {
	t.Run("empty enabledTools and no env var - not registered", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ATTACHMENT_TOOL", "")

		result := shouldAddTool(ToolAttachmentGetData, []string{}, "SLACK_MCP_ATTACHMENT_TOOL")
		assert.False(t, result, "attachment_get_data should NOT be registered when env var is not set")
	})

	t.Run("empty enabledTools and env var set - registered", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ATTACHMENT_TOOL", "true")

		result := shouldAddTool(ToolAttachmentGetData, []string{}, "SLACK_MCP_ATTACHMENT_TOOL")
		assert.True(t, result, "attachment_get_data should be registered when env var is set")
	})

	t.Run("explicit enabledTools includes tool - registered without env var", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ATTACHMENT_TOOL", "")

		result := shouldAddTool(ToolAttachmentGetData, []string{ToolAttachmentGetData}, "SLACK_MCP_ATTACHMENT_TOOL")
		assert.True(t, result, "attachment_get_data should be registered when explicitly in enabledTools")
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
		srv := newTestServer(nil)

		assert.Nil(t, srv.server.ListTools(), "expected no cache-dependent tools before registration")

		srv.RegisterCacheDependentTools()

		tools := srv.server.ListTools()
		require.NotNil(t, tools)
		assert.Contains(t, tools, ToolChannelsList)
		assert.NotContains(t, tools, ToolConversationsUnreads, "unreads requires a configured browser session")
		assert.NotContains(t, tools, ToolUsersSearch, "users_search is registered during initial server setup, not delayed warmup")
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
		srv := newTestServer(nil)

		srv.RegisterCacheDependentTools()
		firstCount := len(srv.server.ListTools())

		srv.RegisterCacheDependentTools()
		assert.Len(t, srv.server.ListTools(), firstCount)
	})
}

func TestShouldAddTool_DraftMessage(t *testing.T) {
	t.Run("empty enabledTools - registered by default", func(t *testing.T) {
		result := shouldAddTool(ToolConversationsDraftMessage, []string{}, "")
		assert.True(t, result, "read-only draft tool should be registered by default")
	})

	t.Run("explicit enabledTools includes tool - registered", func(t *testing.T) {
		result := shouldAddTool(ToolConversationsDraftMessage, []string{ToolConversationsDraftMessage}, "")
		assert.True(t, result, "draft tool should be registered when in enabledTools")
	})

	t.Run("explicit enabledTools excludes tool - not registered", func(t *testing.T) {
		result := shouldAddTool(ToolConversationsDraftMessage, []string{ToolConversationsHistory}, "")
		assert.False(t, result, "draft tool should NOT be registered when not in enabledTools list")
	})
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

func TestShouldAddTool_Matrix(t *testing.T) {
	tests := []struct {
		name         string
		enabledTools []string
		envVarValue  string
		expected     bool
	}{
		{
			name:         "empty ENABLED_TOOLS + empty env var = NOT registered",
			enabledTools: []string{},
			envVarValue:  "",
			expected:     false,
		},
		{
			name:         "empty ENABLED_TOOLS + env var=true = registered",
			enabledTools: []string{},
			envVarValue:  "true",
			expected:     true,
		},
		{
			name:         "empty ENABLED_TOOLS + env var=channel list = registered",
			enabledTools: []string{},
			envVarValue:  "C123,C456",
			expected:     true,
		},
		{
			name:         "includes tool + empty env var = registered",
			enabledTools: []string{ToolConversationsAddMessage},
			envVarValue:  "",
			expected:     true,
		},
		{
			name:         "includes tool + env var=list = registered",
			enabledTools: []string{ToolConversationsAddMessage},
			envVarValue:  "C123",
			expected:     true,
		},
		{
			name:         "excludes tool + empty env var = NOT registered",
			enabledTools: []string{ToolConversationsHistory},
			envVarValue:  "",
			expected:     false,
		},
		{
			name:         "excludes tool + env var=true = NOT registered",
			enabledTools: []string{ToolConversationsHistory},
			envVarValue:  "true",
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", tt.envVarValue)

			result := shouldAddTool(ToolConversationsAddMessage, tt.enabledTools, "SLACK_MCP_ADD_MESSAGE_TOOL")
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShouldAddTool_RegistrationTimeGates(t *testing.T) {
	cases := []struct {
		tool   string
		envVar string
	}{
		{ToolConversationsMark, "SLACK_MCP_MARK_TOOL"},
		{ToolConversationsLeave, "SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL"},
		{ToolConversationsJoin, "SLACK_MCP_CHANNEL_MEMBERSHIP_TOOL"},
		{ToolUsergroupsCreate, "SLACK_MCP_USERGROUPS_WRITE_TOOL"},
		{ToolUsergroupsUpdate, "SLACK_MCP_USERGROUPS_WRITE_TOOL"},
		{ToolUsergroupsUsersUpdate, "SLACK_MCP_USERGROUPS_WRITE_TOOL"},
		{ToolAttachmentGetData, "SLACK_MCP_ATTACHMENT_TOOL"},
		{ToolFilesList, "SLACK_MCP_FILES_LIST_TOOL"},
	}

	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			t.Run("absent with no env and no allowlist", func(t *testing.T) {
				t.Setenv(c.envVar, "")

				result := shouldAddTool(c.tool, []string{}, c.envVar)
				assert.False(t, result, "%s should NOT be registered when %s is unset and enabledTools is empty", c.tool, c.envVar)
			})

			t.Run("present with env var set", func(t *testing.T) {
				t.Setenv(c.envVar, "true")

				result := shouldAddTool(c.tool, []string{}, c.envVar)
				assert.True(t, result, "%s should be registered when %s is set", c.tool, c.envVar)
			})

			t.Run("present when explicitly named in enabledTools", func(t *testing.T) {
				t.Setenv(c.envVar, "")

				result := shouldAddTool(c.tool, []string{c.tool}, c.envVar)
				assert.True(t, result, "%s should be registered when explicitly named in enabledTools even without %s", c.tool, c.envVar)
			})

			t.Run("absent when allowlist excludes it even with env var set", func(t *testing.T) {
				t.Setenv(c.envVar, "true")

				result := shouldAddTool(c.tool, []string{ToolConversationsHistory}, c.envVar)
				assert.False(t, result, "%s should NOT be registered when enabledTools excludes it", c.tool)
			})

			for _, value := range []string{"false", "0", "no", "off", "maybe"} {
				t.Run("absent with env var set to "+value, func(t *testing.T) {
					t.Setenv(c.envVar, value)

					result := shouldAddTool(c.tool, []string{}, c.envVar)
					assert.False(t, result, "%s should NOT be registered when %s=%q", c.tool, c.envVar, value)
				})
			}

			for _, value := range []string{"1", "yes", "TRUE", "  true  "} {
				t.Run("present with env var set to "+value, func(t *testing.T) {
					t.Setenv(c.envVar, value)

					result := shouldAddTool(c.tool, []string{}, c.envVar)
					assert.True(t, result, "%s should be registered when %s=%q", c.tool, c.envVar, value)
				})
			}
		})
	}
}

func TestShouldAddTool_ChannelListGates(t *testing.T) {
	cases := []struct {
		tool   string
		envVar string
	}{
		{ToolConversationsAddMessage, "SLACK_MCP_ADD_MESSAGE_TOOL"},
		{ToolReactionsAdd, "SLACK_MCP_REACTION_TOOL"},
		{ToolReactionsRemove, "SLACK_MCP_REACTION_TOOL"},
	}

	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			for _, value := range []string{"true", "1", "C123,C456", "!C123", "C1234567890,D0987654321"} {
				t.Run("registered with env var "+value, func(t *testing.T) {
					t.Setenv(c.envVar, value)

					result := shouldAddTool(c.tool, []string{}, c.envVar)
					assert.True(t, result, "%s should be registered when %s=%q (channel list is the configuration)", c.tool, c.envVar, value)
				})
			}

			t.Run("not registered when env var is empty", func(t *testing.T) {
				t.Setenv(c.envVar, "")

				result := shouldAddTool(c.tool, []string{}, c.envVar)
				assert.False(t, result, "%s should NOT be registered when %s is unset", c.tool, c.envVar)
			})
		})
	}
}

func TestUnitGetMessageToolNameRegistered(t *testing.T) {
	assert.Contains(t, ValidToolNames, "conversations_get_message")
}
