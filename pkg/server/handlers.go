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
	set.tools = append(set.tools, handler.NewSavedHandler(api, logger, settings).Tools()...)
	set.tools = append(set.tools, handler.NewActivityHandler(api, logger, settings).Tools()...)

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
	add("lists", func() ([]handler.ToolDef, error) {
		service, err := api.Lists()
		if err != nil {
			return nil, err
		}
		return handler.NewListsHandler(service, approvals, api.Identity, logger).Tools(), nil
	})
	add("dnd", func() ([]handler.ToolDef, error) {
		service, err := api.DND()
		if err != nil {
			return nil, err
		}
		return handler.NewDNDHandler(service, api.Identity, logger).Tools(), nil
	})
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
		return handler.NewPeopleChannelsHandler(service, api, api.Identity, logger).Tools(), nil
	})
	add("canvases", func() ([]handler.ToolDef, error) {
		service, err := api.Canvases()
		if err != nil {
			return nil, err
		}
		return handler.NewCanvasHandler(service, logger).Tools(), nil
	})
	add("drafts", func() ([]handler.ToolDef, error) {
		return handler.NewDraftsHandler(api.Drafts(), api, approvals, api.Identity, logger).Tools(), nil
	})
	add("semantic search", func() ([]handler.ToolDef, error) {
		service, err := api.SemanticSearch()
		if err != nil {
			return nil, err
		}
		return handler.NewSemanticSearchHandler(service, api, logger).Tools(), nil
	})
	return set
}
