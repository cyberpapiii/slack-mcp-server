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
	settings.AddMessageEnabled = slices.Contains(enabledTools, capability.ConversationsAddMessage)
	conversations := handler.NewConversationsHandler(api, logger, settings)
	channels := handler.NewChannelsHandler(api, logger)
	set := handlerSet{conversations: conversations, channels: channels}

	userName := func(id string) string {
		u, ok := api.ProvideUsersMap().Users[id]
		if !ok {
			return ""
		}
		if u.RealName != "" {
			return u.RealName
		}
		return u.Name
	}
	channelName := func(id string) string {
		if cached, ok := api.ProvideChannelsMaps().Channels[id]; ok {
			return cached.Name
		}
		return ""
	}

	people := handler.NewPeopleChannelsHandler(api.PeopleChannels(), api.Identity, logger)
	people.UserName = userName
	drafts := handler.NewDraftsHandler(api.Drafts(), approvals, api.Identity, logger)
	drafts.ChannelName = channelName
	semantic := handler.NewSemanticSearchHandler(api.SemanticSearch(), logger)
	semantic.ChannelName = channelName

	for _, tools := range [][]handler.ToolDef{
		conversations.Tools(),
		channels.Tools(),
		handler.NewAuthStatusHandler(api, enabledTools, logger).Tools(),
		handler.NewUsergroupsHandler(api, logger).Tools(),
		handler.NewSavedHandler(api, logger, conversations).Tools(),
		handler.NewActivityHandler(api, logger, conversations).Tools(),
		handler.NewScheduledHandler(api.Scheduled(), approvals, api.Identity, logger).Tools(),
		handler.NewChannelMutationHandler(api.ChannelMutations(), approvals, api.Identity, settings, logger).Tools(),
		handler.NewListsHandler(api.Lists(), approvals, api.Identity, logger).Tools(),
		handler.NewDNDHandler(api.DND(), api.Identity, logger).Tools(),
		handler.NewMessageFilesHandler(api.MessageFiles(), approvals, api.Identity, settings, logger).Tools(),
		people.Tools(),
		handler.NewCanvasHandler(api.Canvases(), logger).Tools(),
		drafts.Tools(),
		semantic.Tools(),
	} {
		set.tools = append(set.tools, tools...)
	}
	return set
}
