package server

import (
	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"go.uber.org/zap"
)

type handlerSet struct {
	tools         []handler.ToolDef
	conversations *handler.ConversationsHandler
	channels      *handler.ChannelsHandler
}

func buildHandlers(api *provider.ApiProvider, logger *zap.Logger, availability handler.Availability, settings handler.Settings, sendEnabled bool) handlerSet {
	approvals := approval.NewStore()
	conversations := handler.NewConversationsHandler(api, logger, settings, sendEnabled)
	channels := handler.NewChannelsHandler(api, logger)
	set := handlerSet{conversations: conversations, channels: channels}
	for _, tools := range [][]handler.ToolDef{
		conversations.Tools(),
		channels.Tools(),
		handler.NewAuthStatusHandler(api, availability).Tools(),
		handler.NewUsergroupsHandler(api.WebAPI(), api.Identity, logger).Tools(),
		handler.NewSavedHandler(api, logger, settings).Tools(),
		handler.NewActivityHandler(api, logger, settings).Tools(),
		handler.NewScheduledHandler(api.WebAPI(), approvals, api.Identity).Tools(),
		handler.NewChannelMutationHandler(api.ChannelMutations(), approvals, api.Identity, settings).Tools(),
		handler.NewListsHandler(api.Lists(), approvals, api.Identity).Tools(),
		handler.NewDNDHandler(api.WebAPI(), api.Identity).Tools(),
		handler.NewMessageFilesHandler(api.MessageFiles(), approvals, api.Identity, settings).Tools(),
		handler.NewPeopleChannelsHandler(api.PeopleChannels(), api, api.Identity).Tools(),
		handler.NewCanvasHandler(api.Canvases()).Tools(),
		handler.NewDraftsHandler(api.Drafts(), api, approvals, api.Identity).Tools(),
		handler.NewSemanticSearchHandler(api.WebAPI(), api).Tools(),
	} {
		set.tools = append(set.tools, tools...)
	}
	return set
}
