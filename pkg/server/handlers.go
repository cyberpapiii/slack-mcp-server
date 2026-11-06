package server

import (
	"slices"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"go.uber.org/zap"
)

type handlerSet struct {
	tools         []handler.ToolDef
	conversations *handler.ConversationsHandler
	channels      *handler.ChannelsHandler
}

// buildHandlers wires every handler family to the provider and collects the
// tools they serve.
func buildHandlers(api *provider.ApiProvider, logger *zap.Logger, enabledTools []string, settings handler.Settings) handlerSet {
	approvals := approval.NewStore()
	conversations := handler.NewConversationsHandler(api, logger, settings, slices.Contains(enabledTools, capability.ConversationsAddMessage))
	channels := handler.NewChannelsHandler(api, logger)
	set := handlerSet{conversations: conversations, channels: channels}
	for _, tools := range [][]handler.ToolDef{
		conversations.Tools(),
		channels.Tools(),
		handler.NewAuthStatusHandler(api, enabledTools, logger).Tools(),
		handler.NewUsergroupsHandler(api, logger).Tools(),
		handler.NewSavedHandler(api, logger, settings).Tools(),
		handler.NewActivityHandler(api, logger, settings).Tools(),
		handler.NewScheduledHandler(api.Scheduled(), approvals, api.Identity, logger).Tools(),
		handler.NewChannelMutationHandler(api.ChannelMutations(), approvals, api.Identity, settings, logger).Tools(),
		handler.NewListsHandler(api.Lists(), approvals, api.Identity, logger).Tools(),
		handler.NewDNDHandler(api.DND(), api.Identity, logger).Tools(),
		handler.NewMessageFilesHandler(api.MessageFiles(), approvals, api.Identity, settings, logger).Tools(),
		handler.NewPeopleChannelsHandler(api.PeopleChannels(), api, api.Identity, logger).Tools(),
		handler.NewCanvasHandler(api.Canvases(), logger).Tools(),
		handler.NewDraftsHandler(api.Drafts(), api, approvals, api.Identity, logger).Tools(),
		handler.NewSemanticSearchHandler(api.SemanticSearch(), api, logger).Tools(),
	} {
		set.tools = append(set.tools, tools...)
	}
	return set
}
