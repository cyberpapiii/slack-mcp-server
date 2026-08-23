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

// authKey is a custom context key for storing the auth token.
type authKey struct{}

// withAuthKey adds an auth key to the context.
func withAuthKey(ctx context.Context, auth string) context.Context {
	return context.WithValue(ctx, authKey{}, auth)
}

// silentPassWarnOnce ensures the "serving without authentication" log fires
// at Warn level only once; subsequent occurrences log at Debug to avoid
// flooding logs on every request.
var silentPassWarnOnce sync.Once

// RequireAPIKeyOrOptOut returns an error if no API key is configured for a
// network transport and the operator has not explicitly opted out via
// SLACK_MCP_ALLOW_UNAUTHENTICATED=true.
func apiKeyFromEnv() string {
	if key := os.Getenv("SLACK_MCP_API_KEY"); key != "" {
		return key
	}
	return os.Getenv("SLACK_MCP_SSE_API_KEY")
}

func allowUnauthenticated() bool {
	// Exact match only (docs); do not widen to IsTruthy.
	return os.Getenv("SLACK_MCP_ALLOW_UNAUTHENTICATED") == "true"
}

func RequireAPIKeyOrOptOut(logger *zap.Logger) error {
	if apiKeyFromEnv() != "" {
		if os.Getenv("SLACK_MCP_API_KEY") == "" {
			logger.Warn("SLACK_MCP_SSE_API_KEY is deprecated, please use SLACK_MCP_API_KEY")
		}
		return nil
	}

	if allowUnauthenticated() {
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

func validateToken(ctx context.Context, logger *zap.Logger) (bool, error) {
	keyA := apiKeyFromEnv()

	if keyA == "" {
		if allowUnauthenticated() {
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

	keyB, ok := ctx.Value(authKey{}).(string)
	if !ok || keyB == "" {
		logger.Warn("Missing auth token in context",
			zap.String("context", "http"),
		)
		return false, fmt.Errorf("unauthorized")
	}

	logger.Debug("Validating auth token",
		zap.String("context", "http"),
		zap.Bool("has_bearer_prefix", strings.HasPrefix(keyB, "Bearer ")),
	)

	if strings.HasPrefix(keyB, "Bearer ") {
		keyB = strings.TrimPrefix(keyB, "Bearer ")
	}

	if !constantTimeEqualAPIKey(keyA, keyB) {
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

// AuthFromRequest extracts the auth token from the request headers.
func AuthFromRequest(logger *zap.Logger) func(context.Context, *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		authHeader := r.Header.Get("Authorization")
		return withAuthKey(ctx, authHeader)
	}
}

// BuildMiddleware creates a middleware function that ensures authentication based on the provided transport type.
func BuildMiddleware(transport string, logger *zap.Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			logger.Debug("Auth middleware invoked",
				zap.String("context", "http"),
				zap.String("transport", transport),
				zap.String("tool", req.Params.Name),
			)

			if authenticated, err := IsAuthenticated(ctx, transport, logger); !authenticated {
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

func IsAuthenticated(ctx context.Context, transport string, logger *zap.Logger) (bool, error) {
	switch transport {
	case "stdio":
		return true, nil

	case "sse", "http":
		// validateToken logs the specific reason once; callers add nothing.
		if authenticated, err := validateToken(ctx, logger); !authenticated || err != nil {
			return false, fmt.Errorf("unauthorized")
		}
		return true, nil

	default:
		logger.Error("Unknown transport type",
			zap.String("context", "http"),
			zap.String("transport", transport),
		)
		return false, fmt.Errorf("unknown transport type: %s", transport)
	}
}
