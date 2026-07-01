package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToolPhaseRegistry_NoOverlap(t *testing.T) {
	for name := range immediateOnlyToolNames {
		_, inCache := cacheDependentToolNames[name]
		assert.False(t, inCache, "tool %q must not be in both phase sets", name)
	}
}

func TestToolPhaseRegistry_WriteToolsAreImmediateOnly(t *testing.T) {
	assert.True(t, isImmediateOnlyTool(ToolConversationsAddMessage))
	assert.False(t, isCacheDependentTool(ToolConversationsAddMessage))
}

func TestToolPhaseRegistry_ChannelsListIsCacheDependent(t *testing.T) {
	assert.True(t, isCacheDependentTool(ToolChannelsList))
	assert.False(t, isImmediateOnlyTool(ToolChannelsList))
}
