package handler

import (
	"fmt"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"go.uber.org/zap"
)

// workspaceName is the workspace subdomain the slack://<workspace>/...
// resource URIs carry, read from auth.test.
func workspaceName(api *provider.ApiProvider, logger *zap.Logger) (string, error) {
	ar, err := api.Slack().AuthTest()
	if err != nil {
		logger.Error("Slack AuthTest failed", zap.Error(err))
		return "", err
	}
	ws, err := text.Workspace(ar.URL)
	if err != nil {
		logger.Error("Failed to parse workspace from URL", zap.String("url", ar.URL), zap.Error(err))
		return "", fmt.Errorf("failed to parse workspace from URL: %w", err)
	}
	return ws, nil
}
