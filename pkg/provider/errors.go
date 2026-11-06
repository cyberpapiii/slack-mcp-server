package provider

import (
	"errors"

	"github.com/slack-go/slack"
)

// The sentinels a handler maps to a tool error code (handler.TypeError).
// ErrNotCached lives with the lookups in lookup.go.
var (
	ErrUsersNotReady             = errors.New("users cache is not ready yet, sync process is still running... please wait")
	ErrChannelsNotReady          = errors.New("channels cache is not ready yet, sync process is still running... please wait")
	ErrRefreshRateLimited        = errors.New("refresh skipped due to rate limiting")
	ErrBrowserSessionUnavailable = errors.New("browser-session Slack auth is unavailable; refresh browser tokens to restore browser-only Slack tools")
	ErrUserOAuthRequired         = errors.New("this personal Slack action requires user OAuth")
	ErrInvalidPaginationCursor   = errors.New("invalid pagination cursor")

	// ErrOutcomeUnknown marks a write whose result Slack did not report: it
	// may have been applied.
	ErrOutcomeUnknown = errors.New("Slack may have applied the change")

	// ErrInvalidListInput marks a Slack List request refused before it was
	// sent; the wrapping error names the field.
	ErrInvalidListInput = errors.New("invalid Slack List input")

	// The channel-mutation guards: the general channel and shared channels
	// are never mutated, and an archive runs only on the state it prepared.
	ErrChannelMutationGeneral = errors.New("general channel cannot be mutated")
	ErrChannelMutationShared  = errors.New("shared channel cannot be mutated")
	ErrChannelArchiveConflict = errors.New("channel changed after archive preparation")
	ErrChannelAlreadyArchived = errors.New("channel is already archived")
)

// errMessageNotFound is GetMessage's answer when Slack has no message at the
// timestamp: Slack's own code, which handler.TypeError maps to not_found.
var errMessageNotFound = slack.SlackErrorResponse{Err: "message_not_found"}

// IsMessageNotFound reports whether err says there is no message at the
// timestamp asked for.
func IsMessageNotFound(err error) bool {
	var slackErr slack.SlackErrorResponse
	return errors.As(err, &slackErr) && slackErr.Err == errMessageNotFound.Err
}
