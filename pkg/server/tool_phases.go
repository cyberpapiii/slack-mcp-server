package server

// Tool registration phases prevent duplicate registration across NewMCPServer
// (immediate) and RegisterCacheDependentTools (after cache warm-up).
//
// immediateOnlyToolNames must never appear in cacheDependentToolNames.

var cacheDependentToolNames = map[string]struct{}{
	ToolChannelsList:         {},
	ToolChannelsMe:           {},
	ToolChannelsStarred:      {},
	ToolConversationsUnreads: {},
	ToolActivityUnreads:      {},
	ToolActivityMarkRead:     {},
}

var immediateOnlyToolNames = map[string]struct{}{
	ToolConversationsAddMessage: {},
	ToolReactionsAdd:            {},
	ToolReactionsRemove:         {},
	ToolAttachmentGetData:       {},
	ToolConversationsMark:       {},
	ToolConversationsLeave:      {},
	ToolConversationsJoin:       {},
	ToolUsergroupsCreate:        {},
	ToolUsergroupsUpdate:        {},
	ToolUsergroupsUsersUpdate:   {},
	ToolUsergroupsMe:            {}, // join/leave mutate membership
	ToolUsergroupsJoin:          {},
	ToolUsergroupsLeave:         {},
	ToolSavedUpdate:             {},
	ToolSavedClearCompleted:     {},
	ToolScheduledMessageCancel:  {},
	ToolChannelsRename:          {},
	ToolChannelsSetTopic:        {},
	ToolChannelsSetPurpose:      {},
	ToolChannelsArchive:         {},
	ToolListsCreate:             {},
	ToolListsUpdate:             {},
	ToolListsItemsCreate:        {},
	ToolListsItemsUpdate:        {},
	ToolListsItemDelete:         {},
	ToolDNDSetSnooze:            {},
	ToolDNDEndSnooze:            {},
}

func isCacheDependentTool(name string) bool {
	_, ok := cacheDependentToolNames[name]
	return ok
}

func isImmediateOnlyTool(name string) bool {
	_, ok := immediateOnlyToolNames[name]
	return ok
}

func validateToolPhaseRegistry() {
	for name := range immediateOnlyToolNames {
		if _, overlap := cacheDependentToolNames[name]; overlap {
			panic("tool " + name + " is both immediate-only and cache-dependent")
		}
	}
}

// guardCacheDependentRegistration panics if a tool is registered in the delayed
// path but classified as immediate-only, or if a known cache-dependent tool is
// missing from the registry (drift between server.go and tool_phases.go).
func guardCacheDependentRegistration(toolName string) {
	if isImmediateOnlyTool(toolName) {
		panic("registerCacheDependentTools must not register immediate-only tool: " + toolName)
	}
	if !isCacheDependentTool(toolName) {
		panic("tool " + toolName + " registered in cache-dependent path but missing from cacheDependentToolNames")
	}
}

func init() {
	validateToolPhaseRegistry()
}
