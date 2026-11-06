package capability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectPrefersFlagsOverTheEnvironment(t *testing.T) {
	env := map[string]string{EnvEnabledTools: DNDGet, EnvToolPreset: PresetLegacyFull}
	getenv := func(key string) string { return env[key] }

	tools, err := Select("", "", getenv)
	require.NoError(t, err)
	assert.Equal(t, []string{DNDGet}, tools, "the environment list beats the environment preset")

	tools, err = Select(ConversationsHistory, "", getenv)
	require.NoError(t, err)
	assert.Equal(t, []string{ConversationsHistory}, tools)

	tools, err = Select("", PresetDailyPower, getenv)
	require.NoError(t, err)
	assert.Equal(t, DailyPowerNames(), tools, "a preset flag beats an inherited tool list")

	tools, err = Select("", "", func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, DailyPowerNames(), tools)
}
