package handler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/slack-go/slack"
)

// Error codes a failed tool call carries.
const (
	codeToolError              = "tool_error"
	codeInvalidArguments       = "invalid_arguments"
	codeNotFound               = "not_found"
	codePermissionDenied       = "permission_denied"
	codeConflict               = "conflict"
	codeInvalidCursor          = "invalid_cursor"
	codePaidPlanUnavailable    = "paid_plan_unavailable"
	codeRateLimited            = "rate_limited"
	codeNetworkError           = "network_error"
	codeSlackError             = "slack_error"
	codeOutcomeUnknown         = "outcome_unknown"
	codeCancelled              = "cancelled"
	codeCacheWarming           = "cache_warming"
	codeUserTokenRequired      = "user_token_required"
	codeUserOAuthRequired      = "user_oauth_required"
	codeBrowserSessionRequired = "browser_session_required"
	codeApprovalRequired       = "approval_required"
	codeApprovalInvalid        = "approval_invalid"
	codeInvalidSnoozeDuration  = "invalid_snooze_duration"
	codeInvalidSlackResponse   = "invalid_slack_response"
	codeLookupLimitExceeded    = "lookup_limit_exceeded"
)

type ToolError struct {
	Code       string
	Message    string
	Retryable  bool
	RetryAfter time.Duration
	Cause      error
}

func (e *ToolError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Code
}

func (e *ToolError) Unwrap() error { return e.Cause }

func invalidArguments(message string) *ToolError {
	return &ToolError{Code: codeInvalidArguments, Message: message}
}

func notFound(message string) *ToolError {
	return &ToolError{Code: codeNotFound, Message: message}
}

func permissionDenied(message string) *ToolError {
	return &ToolError{Code: codePermissionDenied, Message: message}
}

func (a Availability) channelNotFound(channel string) error {
	return notFound(fmt.Sprintf("channel %q not found; pass the channel ID (Cxxxxxxxxxx)%s", channel, a.toolHint(capability.ChannelsList, " or look the name up with %s", "")))
}

func (a Availability) userNotFound(user string) error {
	return notFound(fmt.Sprintf("user %q not found; pass the user ID (Uxxxxxxxxxx)%s", user, a.toolHint(capability.UsersSearch, " or look the name up with %s", "")))
}

// BrowserSessionRequired is the error for a call that needs the browser
// session (xoxc/xoxd) while it is missing or degraded. tool names the
// refused tool when the caller knows it.
func BrowserSessionRequired(tool, reason string) *ToolError {
	if tool == "" {
		tool = "this tool"
	}
	if reason == "" {
		reason = "browser-session credentials are missing or expired"
	}
	return &ToolError{
		Code:    codeBrowserSessionRequired,
		Message: fmt.Sprintf("%s needs a Slack browser session (xoxc/xoxd): %s. Check %s; token-only tools still work.", tool, reason, capability.SlackAuthStatus),
	}
}

// slackErrorCodes maps Slack API error strings to this server's error codes.
var slackErrorCodes = map[string]string{
	"missing_scope":          codePermissionDenied,
	"not_authed":             codePermissionDenied,
	"invalid_auth":           codePermissionDenied,
	"not_allowed_token_type": codePermissionDenied,
	"restricted_action":      codePermissionDenied,
	"not_allowed":            codePermissionDenied,
	"cant_update_message":    codePermissionDenied,
	"cant_delete_message":    codePermissionDenied,
	"message_not_found":      codeNotFound,
	"channel_not_found":      codeNotFound,
	"user_not_found":         codeNotFound,
	"name_taken":             codeConflict,
	"already_in_channel":     codeConflict,
	"cant_invite_self":       codeConflict,
	"time_in_past":           codeInvalidArguments,
	"time_too_far":           codeInvalidArguments,
	"invalid_cursor":         codeInvalidCursor,
	"no_permission":          codePermissionDenied,
	codePermissionDenied:     codePermissionDenied,
	"access_denied":          codePermissionDenied,
	"draft_not_found":        codeNotFound,
	"item_not_found":         codeNotFound,
	"invalid_row_id":         codeNotFound,
	"list_not_found":         codeNotFound,
	// Lists and other paid features answer these on a plan without them.
	"paid_feature_not_available": codePaidPlanUnavailable,
	"feature_not_enabled":        codePaidPlanUnavailable,
	"plan_upgrade_required":      codePaidPlanUnavailable,
	"enterprise_is_restricted":   codePaidPlanUnavailable,
}

// slackServerFaults are Slack's answers when it failed partway; after a write
// they leave the outcome unknown.
var slackServerFaults = map[string]bool{"fatal_error": true, "internal_error": true}

// TypeError gives a failed call its stable error code. mutationAttempted says
// whether the call may already have changed Slack state: then a timeout means
// the outcome is unknown, and nothing is promised to be safe to retry.
func TypeError(err error, mutationAttempted bool) error {
	var typed *ToolError
	if err == nil || errors.As(err, &typed) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return &ToolError{Code: codeCancelled, Message: "the call was cancelled", Cause: err}
	}
	if errors.Is(err, provider.ErrBrowserSessionUnavailable) {
		typed := BrowserSessionRequired("", err.Error())
		typed.Cause = err
		return typed
	}
	for sentinel, code := range map[error]string{
		provider.ErrUserOAuthRequired:       codeUserOAuthRequired,
		provider.ErrInvalidPaginationCursor: codeInvalidCursor,
		provider.ErrChannelMutationGeneral:  codePermissionDenied,
		provider.ErrChannelMutationShared:   codePermissionDenied,
		provider.ErrChannelArchiveConflict:  codeConflict,
		provider.ErrChannelAlreadyArchived:  codeConflict,
	} {
		if errors.Is(err, sentinel) {
			return &ToolError{Code: code, Message: err.Error(), Cause: err}
		}
	}
	if errors.Is(err, provider.ErrUsersNotReady) || errors.Is(err, provider.ErrChannelsNotReady) {
		return &ToolError{Code: codeCacheWarming, Message: err.Error() + "; pass an ID instead of a name, or retry shortly", Retryable: true, Cause: err}
	}
	if errors.Is(err, provider.ErrOutcomeUnknown) {
		return outcomeUnknown(err)
	}
	var rateLimited *slack.RateLimitedError
	if errors.As(err, &rateLimited) {
		return &ToolError{Code: codeRateLimited, Message: err.Error(), Retryable: !mutationAttempted, RetryAfter: rateLimited.RetryAfter, Cause: err}
	}
	var networkError net.Error
	isNetwork := errors.As(err, &networkError)
	timedOut := errors.Is(err, context.DeadlineExceeded) || (isNetwork && networkError.Timeout())
	if mutationAttempted && timedOut {
		return outcomeUnknown(err)
	}
	if isNetwork || timedOut {
		return &ToolError{Code: codeNetworkError, Message: err.Error(), Retryable: !mutationAttempted, Cause: err}
	}
	var slackError slack.SlackErrorResponse
	if errors.As(err, &slackError) {
		if mutationAttempted && slackServerFaults[slackError.Err] {
			return outcomeUnknown(err)
		}
		if code, ok := slackErrorCodes[slackError.Err]; ok {
			return &ToolError{Code: code, Message: err.Error(), Cause: err}
		}
		return &ToolError{Code: codeSlackError, Message: err.Error(), Cause: err}
	}
	var status slack.StatusCodeError
	if errors.As(err, &status) {
		if mutationAttempted && status.Code >= 500 {
			return outcomeUnknown(err)
		}
		return &ToolError{Code: codeSlackError, Message: err.Error(), Retryable: status.Code >= 500, Cause: err}
	}
	return err
}

func outcomeUnknown(err error) *ToolError {
	return &ToolError{Code: codeOutcomeUnknown, Message: "Slack may have applied the change; read current Slack state before another attempt", Cause: err}
}
