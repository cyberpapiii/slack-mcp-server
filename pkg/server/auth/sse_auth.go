package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// The transports the server runs on. Only stdio skips authentication.
const (
	TransportStdio = "stdio"
	TransportSSE   = "sse"
	TransportHTTP  = "http"
)

type authKey struct{}

var silentPassWarnOnce sync.Once

// access is the network transports' access configuration, read from the
// environment once, when a middleware is built.
type access struct {
	apiKey string
	// allowUnauthenticated is SLACK_MCP_ALLOW_UNAUTHENTICATED=true, exact
	// match only (docs); do not widen to IsTruthy.
	allowUnauthenticated bool
}

func accessFromEnv() access {
	key := os.Getenv("SLACK_MCP_API_KEY")
	if key == "" {
		key = os.Getenv("SLACK_MCP_SSE_API_KEY")
	}
	return access{apiKey: key, allowUnauthenticated: os.Getenv("SLACK_MCP_ALLOW_UNAUTHENTICATED") == "true"}
}

// RequireAPIKeyOrOptOut returns an error if no API key is configured for a
// network transport and the operator has not explicitly opted out via
// SLACK_MCP_ALLOW_UNAUTHENTICATED=true.
func RequireAPIKeyOrOptOut(logger *zap.Logger) error {
	a := accessFromEnv()
	if a.apiKey != "" {
		if os.Getenv("SLACK_MCP_API_KEY") == "" {
			logger.Warn("SLACK_MCP_SSE_API_KEY is deprecated, please use SLACK_MCP_API_KEY")
		}
		return nil
	}

	if a.allowUnauthenticated {
		logger.Warn("serving WITHOUT authentication: every client that can reach this server gets full Slack access",
			zap.String("context", "console"),
		)
		return nil
	}

	return fmt.Errorf("no API key configured: set SLACK_MCP_API_KEY (or deprecated SLACK_MCP_SSE_API_KEY), or explicitly opt out with SLACK_MCP_ALLOW_UNAUTHENTICATED=true")
}

func constantTimeEqualAPIKey(configured, provided string) bool {
	// Hash first so ConstantTimeCompare always runs on equal-length digests
	// (raw compare leaks length via early exit when sizes differ).
	sumA := sha256.Sum256([]byte(configured))
	sumB := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(sumA[:], sumB[:]) == 1
}

func (a access) validateToken(ctx context.Context, logger *zap.Logger) (bool, error) {
	if a.apiKey == "" {
		if a.allowUnauthenticated {
			alreadyWarned := true
			silentPassWarnOnce.Do(func() {
				alreadyWarned = false
				logger.Warn("No API key configured; allowing unauthenticated access (SLACK_MCP_ALLOW_UNAUTHENTICATED=true)",
					zap.String("context", "http"),
				)
			})
			if alreadyWarned {
				logger.Debug("No API key configured; allowing unauthenticated access",
					zap.String("context", "http"),
				)
			}
			return true, nil
		}
		logger.Warn("No API key configured and unauthenticated access not opted in",
			zap.String("context", "http"),
		)
		return false, fmt.Errorf("unauthorized")
	}

	provided, ok := ctx.Value(authKey{}).(string)
	if !ok || provided == "" {
		logger.Warn("Missing auth token in context",
			zap.String("context", "http"),
		)
		return false, fmt.Errorf("unauthorized")
	}

	logger.Debug("Validating auth token",
		zap.String("context", "http"),
		zap.Bool("has_bearer_prefix", strings.HasPrefix(provided, "Bearer ")),
	)

	provided = strings.TrimPrefix(provided, "Bearer ")
	if !constantTimeEqualAPIKey(a.apiKey, provided) {
		logger.Warn("Invalid auth token provided",
			zap.String("context", "http"),
		)
		return false, fmt.Errorf("unauthorized")
	}

	logger.Debug("Auth token validated successfully",
		zap.String("context", "http"),
	)
	return true, nil
}

// AuthFromRequest carries the request's Authorization header in ctx for the
// tool and resource middlewares to check.
func AuthFromRequest(ctx context.Context, r *http.Request) context.Context {
	return context.WithValue(ctx, authKey{}, r.Header.Get("Authorization"))
}

func BuildMiddleware(transport string, logger *zap.Logger) server.ToolHandlerMiddleware {
	a := accessFromEnv()
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			logger.Debug("Auth middleware invoked",
				zap.String("context", "http"),
				zap.String("transport", transport),
				zap.String("tool", req.Params.Name),
			)

			if authenticated, err := a.authenticate(ctx, transport, logger); !authenticated {
				return nil, err
			}

			logger.Debug("Authentication successful",
				zap.String("context", "http"),
				zap.String("transport", transport),
				zap.String("tool", req.Params.Name),
			)

			return next(ctx, req)
		}
	}
}

// BuildResourceMiddleware applies the tool calls' authentication to resource
// reads.
func BuildResourceMiddleware(transport string, logger *zap.Logger) server.ResourceHandlerMiddleware {
	a := accessFromEnv()
	return func(next server.ResourceHandlerFunc) server.ResourceHandlerFunc {
		return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			if authenticated, err := a.authenticate(ctx, transport, logger); !authenticated {
				return nil, err
			}
			return next(ctx, req)
		}
	}
}

func (a access) authenticate(ctx context.Context, transport string, logger *zap.Logger) (bool, error) {
	if transport == TransportStdio {
		return true, nil
	}
	// validateToken logs the specific reason once; callers add nothing.
	if authenticated, err := a.validateToken(ctx, logger); !authenticated || err != nil {
		return false, fmt.Errorf("unauthorized")
	}
	return true, nil
}
