package server

import (
	"github.com/korotovsky/slack-mcp-server/pkg/handler"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"go.uber.org/zap"
)

type handlerSet struct {
	tools         []handler.ToolDef
	conversations *handler.ConversationsHandler
	channels      *handler.ChannelsHandler
}

// slackSource is the provider as the handlers see it: cache lookups and
// patches from the ApiProvider, Web API calls from its WebClient, and
// browser-session calls from its SlackAPI.
type slackSource struct {
	*provider.ApiProvider
	*provider.WebClient
	provider.SlackAPI
}

var (
	_ handler.MessageSource    = slackSource{}
	_ handler.ConversationsAPI = slackSource{}
	_ handler.ChannelsAPI      = slackSource{}
	_ handler.SavedAPI         = slackSource{}
	_ handler.ActivityAPI      = slackSource{}
)

func buildHandlers(api *provider.ApiProvider, logger *zap.Logger, availability handler.Availability, settings handler.Settings) handlerSet {
	source := slackSource{ApiProvider: api, WebClient: api.WebAPI(), SlackAPI: api.Slack()}
	messages := handler.NewMessagePipeline(source, availability, logger)
	conversations := handler.NewConversationsHandler(source, messages, logger, settings, availability)
	channels := handler.NewChannelsHandler(source, logger)
	set := handlerSet{conversations: conversations, channels: channels}
	for _, tools := range [][]handler.ToolDef{
		conversations.Tools(),
		channels.Tools(),
		handler.NewAuthStatusHandler(api, availability).Tools(),
		handler.NewUsergroupsHandler(api.WebAPI(), api.Identity, logger).Tools(),
		handler.NewSavedHandler(source, messages, logger, settings).Tools(),
		handler.NewActivityHandler(source, messages, logger, settings).Tools(),
		handler.NewScheduledHandler(api.WebAPI(), api.Identity).Tools(),
		handler.NewChannelMutationHandler(api.ChannelMutations(), api.Identity, settings).Tools(),
		handler.NewListsHandler(api.Lists(), api.Identity).Tools(),
		handler.NewDNDHandler(api.WebAPI(), api.Identity).Tools(),
		handler.NewMessageFilesHandler(api.MessageFiles(), api.Identity, settings, logger).Tools(),
		handler.NewPeopleChannelsHandler(api.PeopleChannels(), api, api.Identity).Tools(),
		handler.NewCanvasHandler(api.Canvases()).Tools(),
		handler.NewDraftsHandler(api.Drafts(), api, api.Identity).Tools(),
		handler.NewSemanticSearchHandler(api.WebAPI(), api).Tools(),
	} {
		set.tools = append(set.tools, tools...)
	}
	return set
}
