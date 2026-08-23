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
	writeTools := []string{
		ToolConversationsAddMessage,
		ToolReactionsAdd,
		ToolReactionsRemove,
		ToolAttachmentGetData,
		ToolConversationsMark,
		ToolConversationsLeave,
		ToolConversationsJoin,
		ToolUsergroupsCreate,
		ToolUsergroupsUpdate,
		ToolUsergroupsUsersUpdate,
		ToolSavedUpdate,
		ToolSavedClearCompleted,
		ToolFilesUpload,
		ToolMessagesSchedule,
		ToolMessagesUpdate,
		ToolMessagesDelete,
		ToolChannelsCreate,
		ToolChannelsInvite,
		ToolUsersSetProfile,
		ToolUsersSetStatus,
		ToolCanvasesCreate,
		ToolCanvasesUpdate,
		ToolDraftsCreate,
		ToolDraftsUpdate,
		ToolDraftsDelete,
	}
	for _, name := range writeTools {
		assert.True(t, isImmediateOnlyTool(name), "%s should be immediate-only", name)
		assert.False(t, isCacheDependentTool(name), "%s must not be cache-dependent", name)
	}
}

func TestToolPhaseRegistry_ChannelsListIsCacheDependent(t *testing.T) {
	assert.True(t, isCacheDependentTool(ToolChannelsList))
	assert.False(t, isImmediateOnlyTool(ToolChannelsList))
}
