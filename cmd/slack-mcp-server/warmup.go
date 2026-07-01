package main

import (
	"context"
	"os"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/server"
	"go.uber.org/zap"
)

const (
	warmupMaxAttempts = 3
	warmupRetryDelay  = 30 * time.Second
)

func isDemoCredentials() bool {
	return os.Getenv("SLACK_MCP_XOXP_TOKEN") == "demo" ||
		(os.Getenv("SLACK_MCP_XOXC_TOKEN") == "demo" && os.Getenv("SLACK_MCP_XOXD_TOKEN") == "demo")
}

func startCacheWarmup(p *provider.ApiProvider, s *server.MCPServer, logger *zap.Logger) {
	go func() {
		for attempt := 1; attempt <= warmupMaxAttempts; attempt++ {
			if isDemoCredentials() {
				logger.Info("Demo credentials are set, skip cache warm-up",
					zap.String("context", "console"),
				)
				return
			}

			refreshUsersCache(p, logger)
			refreshChannelsCache(p, logger)

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

			if attempt < warmupMaxAttempts {
				logger.Warn("Cache warm-up incomplete, retrying",
					zap.String("context", "console"),
					zap.Int("attempt", attempt),
					zap.Error(err),
					zap.Duration("next_retry_in", warmupRetryDelay),
				)
				time.Sleep(warmupRetryDelay)
				continue
			}

			logger.Error("Cache warm-up failed after retries; cache-dependent tools will not be available",
				zap.String("context", "console"),
				zap.Int("attempts", warmupMaxAttempts),
				zap.Error(err),
			)
		}
	}()
}

func refreshUsersCache(p *provider.ApiProvider, logger *zap.Logger) {
	logger.Info("Caching users collection...",
		zap.String("context", "console"),
	)
	err := p.RefreshUsers(context.Background())
	if err != nil {
		logger.Error("Users cache warm-up failed; server continues with degraded cache",
			zap.String("context", "console"),
			zap.Error(err),
		)
	}
}

func refreshChannelsCache(p *provider.ApiProvider, logger *zap.Logger) {
	logger.Info("Caching channels collection...",
		zap.String("context", "console"),
	)
	err := p.RefreshChannels(context.Background())
	if err != nil {
		logger.Error("Channels cache warm-up failed; server continues with degraded cache",
			zap.String("context", "console"),
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
		logger.Warn("Browser session auth degraded — activity and saved tools may fail until Slack is refreshed in the browser and Plug is restarted",
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
