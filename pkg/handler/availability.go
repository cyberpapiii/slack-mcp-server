package handler

import (
	"fmt"
	"slices"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
)

// RuntimeState is what the running server knows that decides whether a
// tool can be called: the credentials, the browser session and the caches.
type RuntimeState interface {
	capability.Credentials
	BrowserFeaturesAvailable() bool
	UsersCacheReady() bool
	ChannelsCacheReady() bool
}

// Availability decides, from the enabled-tools list and the runtime state,
// whether a tool is served and whether it can be called now. Registration
// and slack_auth_status both ask it.
type Availability struct {
	enabled []string
	state   RuntimeState
}

func NewAvailability(enabled []string, state RuntimeState) Availability {
	return Availability{enabled: enabled, state: state}
}

// Served reports whether the server registers the tool at all: it is
// enabled and the running credentials meet its requirement.
func (a Availability) Served(tool capability.Tool) bool {
	return a.unserved(tool) == ""
}

func (a Availability) servesName(name string) bool {
	tool, ok := capability.Lookup(name)
	return ok && a.Served(tool)
}

// toolHint is hint with its %s set to tool when the server serves tool, and
// fallback when it does not, so a hint never points at a tool the caller
// cannot call.
func (a Availability) toolHint(tool, hint, fallback string) string {
	if a.servesName(tool) {
		return fmt.Sprintf(hint, tool)
	}
	return fallback
}

// State says whether a tool can be called now and, if not, why.
func (a Availability) State(tool capability.Tool) string {
	if reason := a.unserved(tool); reason != "" {
		return reason
	}
	switch {
	case tool.Requires.Degrades() && !a.state.BrowserFeaturesAvailable():
		return "browser_session_degraded"
	case tool.CacheReady && !(a.state.UsersCacheReady() && a.state.ChannelsCacheReady()):
		return "available_after_cache_warmup"
	}
	return "available"
}

func (a Availability) unserved(tool capability.Tool) string {
	if !slices.Contains(a.enabled, tool.Name) {
		return "not_enabled"
	}
	if !tool.Requires.Met(a.state) {
		return tool.Requires.Missing()
	}
	return ""
}
