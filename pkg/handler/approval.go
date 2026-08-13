package handler

import (
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
)

// prepareOrExecute is the shared prepare/execute gate for destructive tools.
// prepare returns the issued token; execute consumes it. Missing tokens are
// approval_required; a mismatched, expired, or replayed token is approval_invalid.
func prepareOrExecute(store *approval.Store, action, token string, binding approval.Binding) (*approval.Prepared, bool, error) {
	switch strings.TrimSpace(action) {
	case "prepare":
		prepared, err := store.Prepare(binding)
		if err != nil {
			return nil, false, err
		}
		return &prepared, false, nil
	case "execute":
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, false, &ToolError{Code: "approval_required", Message: "approval_token is required for execute"}
		}
		if _, err := store.Consume(token, binding); err != nil {
			return nil, false, &ToolError{Code: "approval_invalid", Message: err.Error(), Cause: err}
		}
		return nil, true, nil
	default:
		return nil, false, &ToolError{Code: "invalid_arguments", Message: "action must be prepare or execute"}
	}
}
