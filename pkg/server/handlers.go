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
// tools they serve. A family whose provider service is unavailable serves none.
func buildHandlers(api *provider.ApiProvider, logger *zap.Logger, enabledTools []string, settings handler.Settings) handlerSet {
	approvals := approval.NewStore()
	settings.AddMessageEnabled = slices.Contains(enabledTools, capability.ConversationsAddMessage)
	conversations := handler.NewConversationsHandler(api, logger, settings)
	channels := handler.NewChannelsHandler(api, logger)
	set := handlerSet{conversations: conversations, channels: channels}
	add := func(family string, tools func() ([]handler.ToolDef, error)) {
		defs, err := tools()
		if err != nil {
			logger.Info("Tool family unavailable", zap.String("family", family), zap.Error(err))
			return
		}
		set.tools = append(set.tools, defs...)
	}
	set.tools = append(set.tools, conversations.Tools()...)
	set.tools = append(set.tools, channels.Tools()...)
	set.tools = append(set.tools, handler.NewAuthStatusHandler(api, logger).Tools()...)
	set.tools = append(set.tools, handler.NewUsergroupsHandler(api, logger).Tools()...)
	set.tools = append(set.tools, handler.NewSavedHandler(api, logger, conversations).Tools()...)
	set.tools = append(set.tools, handler.NewActivityHandler(api, logger, conversations).Tools()...)

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

	add("scheduled", func() ([]handler.ToolDef, error) {
		service, err := api.Scheduled()
		if err != nil {
			return nil, err
		}
		return handler.NewScheduledHandler(service, approvals, api.Identity, logger).Tools(), nil
	})
	add("channel mutations", func() ([]handler.ToolDef, error) {
		service, err := api.ChannelMutations()
		if err != nil {
			return nil, err
		}
		return handler.NewChannelMutationHandler(service, approvals, api.Identity, settings, logger).Tools(), nil
	})
	set.tools = append(set.tools, handler.NewListsHandler(api.Lists(), approvals, api.Identity, logger).Tools()...)
	set.tools = append(set.tools, handler.NewDNDHandler(api.DND(), api.Identity, logger).Tools()...)
	add("message and file mutations", func() ([]handler.ToolDef, error) {
		service, err := api.MessageFiles()
		if err != nil {
			return nil, err
		}
		return handler.NewMessageFilesHandler(service, approvals, api.Identity, settings, logger).Tools(), nil
	})
	add("people and channels", func() ([]handler.ToolDef, error) {
		service, err := api.PeopleChannels()
		if err != nil {
			return nil, err
		}
		h := handler.NewPeopleChannelsHandler(service, api.Identity, logger)
		h.UserName = userName
		return h.Tools(), nil
	})
	add("canvases", func() ([]handler.ToolDef, error) {
		service, err := api.Canvases()
		if err != nil {
			return nil, err
		}
		return handler.NewCanvasHandler(service, logger).Tools(), nil
	})
	add("drafts", func() ([]handler.ToolDef, error) {
		h := handler.NewDraftsHandler(api.Drafts(), approvals, api.Identity, logger)
		h.ChannelName = channelName
		return h.Tools(), nil
	})
	add("semantic search", func() ([]handler.ToolDef, error) {
		service, err := api.SemanticSearch()
		if err != nil {
			return nil, err
		}
		h := handler.NewSemanticSearchHandler(service, logger)
		h.ChannelName = channelName
		return h.Tools(), nil
	})
	return set
}
