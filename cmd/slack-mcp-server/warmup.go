package main

import (
	"context"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/server"
	"go.uber.org/zap"
)

const (
	// warmupFastAttempts is how many attempts run at warmupRetryDelay before
	// the loop drops to warmupSlowRetryDelay; it never stops retrying.
	warmupFastAttempts   = 3
	warmupRetryDelay     = 30 * time.Second
	warmupSlowRetryDelay = 5 * time.Minute
	// Match provider background refresh budget so cold large workspaces can
	// finish warm-up (and RegisterCacheDependentTools) instead of timing out
	// every attempt at 2m while background fetches get 15m.
	warmupRefreshTimeout = 15 * time.Minute
)

// warmupNextDelay: nextAttempt is 1-based. Fast for attempts <= warmupFastAttempts, else slow.
func warmupNextDelay(nextAttempt int) time.Duration {
	if nextAttempt <= warmupFastAttempts {
		return warmupRetryDelay
	}
	return warmupSlowRetryDelay
}

func startCacheWarmup(p *provider.ApiProvider, s *server.MCPServer, logger *zap.Logger) {
	go func() {
		for attempt := 1; ; attempt++ {
			warm(logger, "users", p.RefreshUsers)
			warm(logger, "channels", p.RefreshChannels)

			ready, err := p.IsReady()
			if ready {
				s.RegisterCacheDependentTools()
				if attempt > 1 {
					logger.Info("Cache warm-up succeeded after retry",
						zap.String("context", "console"),
						zap.Int("attempt", attempt),
					)
				} else {
					logger.Info("Slack MCP Server is fully ready",
						zap.String("context", "console"),
					)
				}
				return
			}

			switch {
			case attempt < warmupFastAttempts:
				logger.Warn("Cache warm-up incomplete, retrying",
					zap.String("context", "console"),
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("next_retry_in", warmupRetryDelay),
				)
			case attempt == warmupFastAttempts:
				logger.Error("Cache warm-up failed after retries; cache-dependent tools will not be available until a background retry succeeds",
					zap.String("context", "console"),
					zap.Int("attempts", warmupFastAttempts),
					zap.Duration("slow_retry_every", warmupSlowRetryDelay),
					zap.Error(err),
				)
			default:
				logger.Info("Background cache warm-up retry",
					zap.String("context", "console"),
					zap.Int("attempt", attempt),
					zap.Error(err),
				)
			}

			time.Sleep(warmupNextDelay(attempt + 1))
		}
	}()
}

// warm runs one cache refresh under the warm-up budget and logs a failure;
// the caller decides whether to retry.
func warm(logger *zap.Logger, what string, refresh func(context.Context) error) {
	logger.Info("Caching "+what+" collection...", zap.String("context", "console"))
	ctx, cancel := context.WithTimeout(context.Background(), warmupRefreshTimeout)
	defer cancel()
	if err := refresh(ctx); err != nil {
		logger.Error("Cache warm-up failed; server continues with degraded cache",
			zap.String("context", "console"),
			zap.String("cache", what),
			zap.Error(err),
		)
	}
}

func logStartupAuthStatus(p *provider.ApiProvider, logger *zap.Logger) {
	if p.BrowserFeaturesAvailable() {
		return
	}
	reason := p.BrowserDegradedReason()
	if reason != "" {
		logger.Warn("Browser session auth degraded: activity and saved tools may fail until Slack is refreshed in the browser and Plug is restarted",
			zap.String("context", "console"),
			zap.String("reason", reason),
		)
		return
	}
	if !p.IsOAuth() && !p.IsBotToken() {
		logger.Info("Browser-only tools (activity_*, saved_*) require xoxc/xoxd browser session tokens; use slack_auth_status for details",
			zap.String("context", "console"),
		)
	}
}
