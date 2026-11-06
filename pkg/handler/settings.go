package handler

import (
	"fmt"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
)

// Settings are the operator's choices the handlers enforce, read once at
// startup.
type Settings struct {
	allowlists map[string]channelAllowlist

	// AddMessageEnabled says whether conversations_add_message is served, for
	// the draft preview's "would it send" line.
	AddMessageEnabled bool
	// MarkAfterPost marks the channel read after conversations_add_message posts.
	MarkAfterPost bool
	// Unfurling is SLACK_MCP_ADD_MESSAGE_UNFURLING: empty, a truthy value, or a
	// domain allowlist.
	Unfurling string
	// DownloadDir is SLACK_MCP_DOWNLOAD_DIR, the absolute directory
	// files_download writes into; empty disables the tool.
	DownloadDir string
	// Detail is the output mode for a call that omits `detail`:
	// SLACK_MCP_COMPACT_OUTPUT set to 0, false, no or off selects full.
	Detail text.OutputMode
	// LogParams logs every tool call's arguments at Info.
	LogParams bool
}

// SettingsFromEnv reads the handler settings, rejecting a malformed channel
// allowlist so it fails at startup instead of on a call.
func SettingsFromEnv(getenv func(string) string) (Settings, error) {
	settings := Settings{
		allowlists:    map[string]channelAllowlist{},
		MarkAfterPost: envutil.IsTruthy(getenv("SLACK_MCP_ADD_MESSAGE_MARK")),
		Unfurling:     getenv("SLACK_MCP_ADD_MESSAGE_UNFURLING"),
		DownloadDir:   strings.TrimSpace(getenv("SLACK_MCP_DOWNLOAD_DIR")),
		LogParams:     strings.EqualFold(getenv("SLACK_MCP_LOG_PARAMS"), "debug"),
	}
	if envutil.IsFalsy(getenv("SLACK_MCP_COMPACT_OUTPUT")) {
		settings.Detail = text.ModeFull
	}
	for _, variable := range capability.ChannelAllowlists() {
		allowlist, err := parseChannelAllowlist(getenv(variable))
		if err != nil {
			return Settings{}, fmt.Errorf("%s: %w", variable, err)
		}
		settings.allowlists[variable] = allowlist
	}
	return settings, nil
}

// outputMode reads the call's `detail` parameter.
func (s Settings) outputMode(request mcp.CallToolRequest) (text.OutputMode, error) {
	mode, err := text.ResolveOutputMode(request.GetString("detail", ""), s.Detail)
	if err != nil {
		return mode, invalidArguments(err.Error())
	}
	return mode, nil
}

// requireChannel enforces the tool's channel allowlist, if it has one.
func (s Settings) requireChannel(tool, channel string) error {
	if s.channelAllowed(tool, channel) {
		return nil
	}
	spec, _ := capability.Lookup(tool)
	return &ToolError{Code: "permission_denied", Message: fmt.Sprintf("%s is not allowed for channel %q by %s", tool, channel, spec.ChannelAllowlist)}
}

func (s Settings) channelAllowed(tool, channel string) bool {
	spec, _ := capability.Lookup(tool)
	if spec.ChannelAllowlist == "" {
		return true
	}
	allowlist, ok := s.allowlists[spec.ChannelAllowlist]
	return !ok || allowlist.allows(channel)
}

// channelAllowlist is one allow/block list. Empty or truthy allows every
// channel; "C1,C2" allows only those; "!C1,!C2" allows all except those.
type channelAllowlist struct {
	channels map[string]bool
	block    bool
}

func parseChannelAllowlist(value string) (channelAllowlist, error) {
	if value == "" || envutil.IsTruthy(value) {
		return channelAllowlist{block: true}, nil
	}
	allowlist := channelAllowlist{channels: map[string]bool{}}
	negated, positive := false, false
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" || item == "!" {
			return channelAllowlist{}, fmt.Errorf("empty channel entry in allow/block list")
		}
		if strings.HasPrefix(item, "!") {
			negated = true
		} else {
			positive = true
		}
		allowlist.channels[strings.TrimPrefix(item, "!")] = true
	}
	if negated && positive {
		return channelAllowlist{}, fmt.Errorf("cannot mix allowed and disallowed (! prefixed) channels")
	}
	allowlist.block = negated
	return allowlist, nil
}

func (a channelAllowlist) allows(channel string) bool {
	return a.channels[channel] != a.block
}
