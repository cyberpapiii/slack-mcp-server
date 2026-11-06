package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/server/auth"
	"github.com/korotovsky/slack-mcp-server/pkg/version"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

type MCPServer struct {
	server *server.MCPServer
	logger *zap.Logger

	// cacheTools register once the users and channels caches are warm, with
	// the directory resources that read the same caches.
	cacheTools     []server.ServerTool
	resources      []server.ServerResource
	cacheToolsOnce sync.Once
}

// NewMCPServer registers every enabled tool whose credential requirement the
// provider meets. Tools the capability table marks CacheReady wait for
// RegisterCacheDependentTools.
func NewMCPServer(provider *provider.ApiProvider, transport string, logger *zap.Logger, enabledTools []string, settings handler.Settings) *MCPServer {
	opts := append([]server.ServerOption{
		server.WithLogging(),
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, true),
		server.WithResourceHandlerMiddleware(auth.BuildResourceMiddleware(transport, logger)),
	}, toolHandlerOptions(transport, logger, settings.LogParams)...)
	s := server.NewMCPServer("Slack MCP Server", version.Version, opts...)

	ws, err := workspaceName(provider.WorkspaceURL())
	if err != nil {
		logger.Fatal("Failed to parse workspace from URL",
			zap.String("context", "console"),
			zap.String("url", provider.WorkspaceURL()),
			zap.Error(err),
		)
	}

	ms := &MCPServer{server: s, logger: logger}
	families := buildHandlers(provider, logger, enabledTools, settings)
	for _, def := range families.tools {
		spec, ok := capability.Lookup(def.Name)
		if !ok {
			panic(fmt.Sprintf("tool %q is not in the capability table", def.Name))
		}
		if !slices.Contains(enabledTools, def.Name) || !provider.Satisfies(spec.Requires) {
			continue
		}
		tool := mcpTool(spec, def.Options)
		run := def.Run
		if spec.Requires == capability.BrowserSession {
			run = requireBrowserSession(provider, spec.Name, run)
		}
		if spec.CacheReady {
			ms.cacheTools = append(ms.cacheTools, server.ServerTool{Tool: tool, Handler: run})
			continue
		}
		s.AddTool(tool, run)
	}
	ms.resources = []server.ServerResource{
		{Resource: mcp.NewResource(
			"slack://"+ws+"/channels",
			"Directory of Slack channels",
			mcp.WithResourceDescription("CSV directory of Slack channels."),
			mcp.WithMIMEType("text/csv"),
		), Handler: families.channels.ChannelsResource},
		{Resource: mcp.NewResource(
			"slack://"+ws+"/users",
			"Directory of Slack users",
			mcp.WithResourceDescription("CSV directory of Slack users."),
			mcp.WithMIMEType("text/csv"),
		), Handler: families.conversations.UsersResource},
	}
	return ms
}

// workspaceName is the workspace's subdomain in its Slack URL
// (https://acme.slack.com/ is "acme"), the host of the resource URIs.
func workspaceName(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(u.Hostname(), ".")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid Slack URL: %q", rawURL)
	}
	return parts[0], nil
}

// mcpTool builds the advertised tool from its capability row. A read-only
// tool is never destructive and always idempotent.
func mcpTool(spec capability.Tool, options []mcp.ToolOption) mcp.Tool {
	options = append(options,
		mcp.WithTitleAnnotation(spec.Title),
		mcp.WithReadOnlyHintAnnotation(spec.ReadOnly),
		mcp.WithDestructiveHintAnnotation(spec.Destructive && !spec.ReadOnly),
		mcp.WithIdempotentHintAnnotation(spec.Idempotent || spec.ReadOnly),
		mcp.WithOpenWorldHintAnnotation(true),
	)
	return mcp.NewTool(spec.Name, options...)
}

// RegisterCacheDependentTools registers the tools and resources that read the
// users and channels caches. Called after cache warm-up completes; mcp-go
// sends notifications/tools/list_changed to connected clients.
func (s *MCPServer) RegisterCacheDependentTools() {
	s.cacheToolsOnce.Do(func() {
		s.server.AddTools(s.cacheTools...)
		s.server.AddResources(s.resources...)
		s.logger.Info("Cache-dependent tools and resources registered",
			zap.String("context", "console"),
		)
	})
}

func (s *MCPServer) ServeSSE(addr string) *server.SSEServer {
	s.logger.Info("Creating SSE server",
		zap.String("context", "console"),
		zap.String("version", version.Version),
		zap.String("build_time", version.BuildTime),
		zap.String("commit_hash", version.CommitHash),
		zap.String("address", addr),
	)
	return server.NewSSEServer(s.server,
		server.WithBaseURL(fmt.Sprintf("http://%s", addr)),
		server.WithSSEContextFunc(auth.AuthFromRequest),
	)
}

func (s *MCPServer) ServeHTTP(addr string) *server.StreamableHTTPServer {
	s.logger.Info("Creating HTTP server",
		zap.String("context", "console"),
		zap.String("version", version.Version),
		zap.String("build_time", version.BuildTime),
		zap.String("commit_hash", version.CommitHash),
		zap.String("address", addr),
	)
	return server.NewStreamableHTTPServer(s.server,
		server.WithEndpointPath("/mcp"),
		server.WithHTTPContextFunc(auth.AuthFromRequest),
	)
}

func (s *MCPServer) ServeStdio() error {
	s.logger.Info("Starting STDIO server",
		zap.String("version", version.Version),
		zap.String("build_time", version.BuildTime),
		zap.String("commit_hash", version.CommitHash),
	)
	return stdioExitErr(s.logger, server.ServeStdio(s.server))
}

// stdioExitErr maps a signalled shutdown to a clean exit. mcp-go cancels its
// context on SIGTERM/SIGINT, so Listen returns context.Canceled every time
// Plug stops the server; reporting that as an error made main exit 1 through
// logger.Fatal and skip the provider's deferred Close.
func stdioExitErr(logger *zap.Logger, err error) error {
	if errors.Is(err, context.Canceled) {
		logger.Info("STDIO server stopped by signal")
		return nil
	}
	if err != nil {
		logger.Error("STDIO server error", zap.Error(err))
	}
	return err
}

// toolHandlerOptions is the middleware stack every tool call runs through.
// mcp-go wraps middlewares in reverse registration order. Auth stays outermost
// so API-key failures remain protocol errors; WithRecovery sits innermost so a
// panic becomes an error that errorRecovery turns into an isError tool result
// instead of a JSON-RPC -32603.
func toolHandlerOptions(transport string, logger *zap.Logger, logParams bool) []server.ServerOption {
	return []server.ServerOption{
		server.WithToolHandlerMiddleware(auth.BuildMiddleware(transport, logger)),
		server.WithToolHandlerMiddleware(buildLoggerMiddleware(logger, logParams)),
		server.WithToolHandlerMiddleware(buildErrorRecoveryMiddleware(logger)),
		server.WithRecovery(),
	}
}

// buildErrorRecoveryMiddleware converts tool handler errors into MCP tool results
// with isError=true, allowing LLMs to see the error and retry with different parameters.
// Without this, errors become JSON-RPC -32603 protocol errors that crash MCP clients.
func buildErrorRecoveryMiddleware(logger *zap.Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := next(ctx, req)
			if err != nil {
				// A read-only tool cannot have changed Slack state, so its
				// failures are safe to retry; a write's may not be.
				meta, _ := capability.Lookup(req.Params.Name)
				err = handler.TypeError(err, !meta.ReadOnly)
				// A *handler.ToolError is an expected, typed outcome (bad
				// arguments, permission, rate limit, cancellation); anything
				// else is a defect worth a warning. "Request finished" carries
				// the code either way.
				var typed *handler.ToolError
				level := logger.Warn
				if errors.As(err, &typed) {
					level = logger.Debug
				}
				level("Tool call returned error, converting to isError tool result",
					zap.String("tool", req.Params.Name),
					zap.Error(err),
				)
				return handler.NewTypedErrorResult(err), nil
			}
			return res, nil
		}
	}
}

func buildLoggerMiddleware(logger *zap.Logger, logParams bool) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// "Request finished" is the per-call Info line; the entry line only
			// earns Info when it carries the opted-in params.
			if logParams {
				logger.Info("Request received", zap.String("tool", req.Params.Name), zap.Any("params", req.Params))
			} else {
				logger.Debug("Request received", zap.String("tool", req.Params.Name))
			}

			startTime := time.Now()
			res, err := next(ctx, req)

			fields := []zap.Field{
				zap.String("tool", req.Params.Name),
				zap.Duration("duration", time.Since(startTime)),
				zap.Bool("is_error", err != nil || (res != nil && res.IsError)),
			}
			if code := handler.ErrorCode(res); code != "" {
				fields = append(fields, zap.String("error_code", code))
			}
			logger.Info("Request finished", fields...)
			return res, err
		}
	}
}
