package handler

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func settingsWith(env map[string]string) (Settings, error) {
	return SettingsFromEnv(func(key string) string { return env[key] })
}

func TestChannelAllowlistGrammar(t *testing.T) {
	tests := []struct {
		value   string
		allowed []string
		denied  []string
	}{
		{"", []string{"C1", "C9"}, nil},
		{"true", []string{"C1", "C9"}, nil},
		{" C1 , C2 ", []string{"C1", "C2"}, []string{"C9"}},
		{"!C1,!C2", []string{"C9"}, []string{"C1", "C2"}},
		// false is read as a one-entry list and blocks every channel; main warns.
		{"false", nil, []string{"C1"}},
	}
	for _, tt := range tests {
		settings, err := settingsWith(map[string]string{capability.AddMessageAllowlist: tt.value})
		require.NoError(t, err, tt.value)
		for _, channel := range tt.allowed {
			assert.NoError(t, settings.requireChannel(capability.MessagesSchedule, channel), "%q allows %s", tt.value, channel)
		}
		for _, channel := range tt.denied {
			err := settings.requireChannel(capability.ConversationsAddMessage, channel)
			var toolErr *ToolError
			require.ErrorAs(t, err, &toolErr, "%q denies %s", tt.value, channel)
			assert.Equal(t, "permission_denied", toolErr.Code)
			assert.Contains(t, toolErr.Message, capability.AddMessageAllowlist)
		}
	}
}

func TestUnitOutputModeReadsDetail(t *testing.T) {
	request := func(detail string) mcp.CallToolRequest {
		r := mcp.CallToolRequest{}
		r.Params.Arguments = map[string]any{"detail": detail}
		return r
	}
	full := Settings{Detail: text.ModeFull}
	for detail, want := range map[string]text.OutputMode{"": text.ModeFull, "standard": text.ModeStandard, " FULL ": text.ModeFull} {
		got, err := full.outputMode(request(detail))
		require.NoError(t, err, detail)
		assert.Equal(t, want, got, detail)
	}
	_, err := full.outputMode(request("bogus"))
	var typed *ToolError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "invalid_arguments", typed.Code)
}

func TestAFalsyAllowlistIsWarnedAbout(t *testing.T) {
	settings, err := settingsWith(map[string]string{capability.ReactionAllowlist: "false"})
	require.NoError(t, err)
	require.Len(t, settings.Warnings, 1)
	assert.Contains(t, settings.Warnings[0], capability.ReactionAllowlist+"=false is treated as a channel allowlist and blocks every channel")

	settings, err = settingsWith(map[string]string{capability.ReactionAllowlist: "C1"})
	require.NoError(t, err)
	assert.Empty(t, settings.Warnings)
}

func TestMalformedChannelAllowlistFailsAtStartup(t *testing.T) {
	for _, value := range []string{"C1,!C2", "C1,,C2", "!"} {
		_, err := settingsWith(map[string]string{capability.ReactionAllowlist: value})
		assert.ErrorContains(t, err, capability.ReactionAllowlist, value)
	}
}

func TestEachAllowlistGovernsOnlyItsTools(t *testing.T) {
	settings, err := settingsWith(map[string]string{capability.ChannelManagementAllowlist: "C1"})
	require.NoError(t, err)
	assert.Error(t, settings.requireChannel(capability.ChannelsRename, "C9"))
	assert.NoError(t, settings.requireChannel(capability.ReactionsAdd, "C9"))
	assert.NoError(t, settings.requireChannel(capability.ConversationsHistory, "C9"))
}
