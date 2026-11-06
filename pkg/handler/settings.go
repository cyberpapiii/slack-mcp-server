package handler

import (
	"fmt"
	"path/filepath"
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
	unfurling  unfurlPolicy

	// Warnings are settings that parse but likely do not do what the operator
	// meant, for the startup log.
	Warnings []string
	// MarkAfterPost marks the channel read after conversations_add_message posts.
	MarkAfterPost bool
	// DownloadDir is SLACK_MCP_DOWNLOAD_DIR, the absolute directory
	// files_download writes into; empty disables the tool.
	DownloadDir string
	// Detail is the output mode for a call that omits `detail`:
	// SLACK_MCP_COMPACT_OUTPUT set to 0, false, no or off selects full.
	Detail text.OutputMode
}

// SettingsFromEnv reads the handler settings, rejecting a malformed channel
// allowlist or a relative download directory so it fails at startup instead
// of on a call.
func SettingsFromEnv(getenv func(string) string) (Settings, error) {
	settings := Settings{
		allowlists:    map[string]channelAllowlist{},
		MarkAfterPost: envutil.IsTruthy(getenv("SLACK_MCP_ADD_MESSAGE_MARK")),
		unfurling:     parseUnfurling(getenv("SLACK_MCP_ADD_MESSAGE_UNFURLING")),
		DownloadDir:   strings.TrimSpace(getenv("SLACK_MCP_DOWNLOAD_DIR")),
	}
	if settings.DownloadDir != "" && !filepath.IsAbs(settings.DownloadDir) {
		return Settings{}, fmt.Errorf("SLACK_MCP_DOWNLOAD_DIR must be an absolute path, got %q", settings.DownloadDir)
	}
	if envutil.IsFalsy(getenv("SLACK_MCP_COMPACT_OUTPUT")) {
		settings.Detail = text.ModeFull
	}
	for _, variable := range capability.ChannelAllowlists() {
		value := getenv(variable)
		allowlist, err := parseChannelAllowlist(value)
		if err != nil {
			return Settings{}, fmt.Errorf("%s: %w", variable, err)
		}
		settings.allowlists[variable] = allowlist
		if envutil.IsFalsy(value) {
			settings.Warnings = append(settings.Warnings, variable+"="+value+" is treated as a channel allowlist and blocks every channel; unset it, or drop the tool from SLACK_MCP_ENABLED_TOOLS to disable it")
		}
	}
	return settings, nil
}

// The values of a message tool's `detail` parameter.
const (
	detailStandard = "standard"
	detailFull     = "full"
)

func detailOption() mcp.ToolOption {
	return mcp.WithString("detail", mcp.Description(toolDetailDescription), mcp.Enum(detailStandard, detailFull))
}

// outputMode reads the call's `detail` parameter; omitted, it is the server
// default.
func (s Settings) outputMode(request mcp.CallToolRequest) (text.OutputMode, error) {
	detail := request.GetString("detail", "")
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "":
		return s.Detail, nil
	case detailStandard:
		return text.ModeStandard, nil
	case detailFull:
		return text.ModeFull, nil
	}
	return text.ModeStandard, invalidArguments(fmt.Sprintf("invalid detail value %q: must be \"standard\" or \"full\"", detail))
}

func (s Settings) requireChannel(tool, channel string) error {
	if s.channelAllowed(tool, channel) {
		return nil
	}
	spec, _ := capability.Lookup(tool)
	return permissionDenied(fmt.Sprintf("%s is not allowed for channel %q by %s", tool, channel, spec.ChannelAllowlist))
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
