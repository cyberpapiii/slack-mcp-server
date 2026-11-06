package handler

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

var channelNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,80}$`)

type ChannelMutationService interface {
	Rename(context.Context, string, string) (provider.ChannelMutationState, error)
	SetTopic(context.Context, string, string) (provider.ChannelMutationState, error)
	SetPurpose(context.Context, string, string) (provider.ChannelMutationState, error)
	PrepareArchive(context.Context, string) (provider.ArchivePreparation, error)
	ArchivePrepared(context.Context, provider.ArchivePreparation) (provider.ChannelMutationState, error)
}

type ChannelMutationHandler struct {
	service   ChannelMutationService
	approvals approvalGate
	settings  Settings
	logger    *zap.Logger
}

type ChannelMutationData struct {
	Action string `json:"action" jsonschema:"Mutation performed"`
	ApprovalPhase
	Channel *provider.ChannelMutationState `json:"channel,omitempty" jsonschema:"Observed or resulting channel state"`
}

type ChannelMutationResult = ToolResult[ChannelMutationData]

func NewChannelMutationHandler(service ChannelMutationService, approvals *approval.Store, identity func() provider.ProviderIdentity, settings Settings, logger *zap.Logger) *ChannelMutationHandler {
	return &ChannelMutationHandler{service: service, approvals: approvalGate{store: approvals, identity: identityFunc(identity)}, settings: settings, logger: logger}
}

func (h *ChannelMutationHandler) ConversationsRenameHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	if err := h.settings.requireChannel(capability.ChannelsRename, channelID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(request.GetString("name", ""))
	if name == "" {
		return nil, invalidArguments("name is required and must not be empty")
	}
	if !channelNamePattern.MatchString(name) {
		return nil, invalidArguments("name must be 1-80 lowercase letters, numbers, hyphens, or underscores")
	}
	state, err := h.service.Rename(ctx, channelID, name)
	return mutationResult("rename", state, err)
}

func (h *ChannelMutationHandler) ConversationsSetTopicHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	if err := h.settings.requireChannel(capability.ChannelsSetTopic, channelID); err != nil {
		return nil, err
	}
	topic, err := presentString(request, "topic")
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(topic) > 250 {
		return nil, invalidArguments("topic must be at most 250 characters")
	}
	state, err := h.service.SetTopic(ctx, channelID, topic)
	return mutationResult("set_topic", state, err)
}

func (h *ChannelMutationHandler) ConversationsSetPurposeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	if err := h.settings.requireChannel(capability.ChannelsSetPurpose, channelID); err != nil {
		return nil, err
	}
	purpose, err := presentString(request, "purpose")
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(purpose) > 250 {
		return nil, invalidArguments("purpose must be at most 250 characters")
	}
	state, err := h.service.SetPurpose(ctx, channelID, purpose)
	return mutationResult("set_purpose", state, err)
}

func (h *ChannelMutationHandler) ConversationsArchiveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	if err := h.settings.requireChannel(capability.ChannelsArchive, channelID); err != nil {
		return nil, err
	}
	prepare, err := approvalAction(request)
	if err != nil {
		return nil, err
	}
	if _, err := requireUserActor(h.approvals.identity()); err != nil {
		return nil, err
	}
	preparation, err := h.service.PrepareArchive(ctx, channelID)
	if err != nil {
		return nil, err
	}
	arguments := struct {
		ChannelID string `json:"channel_id"`
	}{preparation.Expected.ChannelID}
	phase, err := h.approvals.check(request, capability.ChannelsArchive, prepare, arguments, preparation.Expected)
	if err != nil {
		return nil, err
	}
	if phase.Prepared() {
		data := ChannelMutationData{Action: "archive", ApprovalPhase: phase, Channel: &preparation.Expected}
		return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
	}
	state, err := h.service.ArchivePrepared(ctx, preparation)
	if err != nil {
		return nil, TypeError(err, true)
	}
	data := ChannelMutationData{Action: "archive", ApprovalPhase: phase, Channel: &state}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

func mutationResult(action string, state provider.ChannelMutationState, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return nil, err
	}
	data := ChannelMutationData{Action: action, ApprovalPhase: ApprovalPhase{Phase: "executed"}, Channel: &state}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), fallbackJSON(data)), nil
}

// Tools lists the MCP tools this handler serves.
func (h *ChannelMutationHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.ChannelsRename, Run: h.ConversationsRenameHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Rename one ordinary channel."),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)),
			mcp.WithString("name", mcp.Required(), mcp.Description("New channel name, 1 to 80 lowercase letters, numbers, hyphens, or underscores.")),
		}},
		{Name: capability.ChannelsSetTopic, Run: h.ConversationsSetTopicHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Set or clear one ordinary channel topic."),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)),
			mcp.WithString("topic", mcp.Required(), mcp.Description("New topic, up to 250 characters; pass an empty string to clear it.")),
		}},
		{Name: capability.ChannelsSetPurpose, Run: h.ConversationsSetPurposeHandler, Options: []mcp.ToolOption{
			mcp.WithDescription("Set or clear one ordinary channel purpose."),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)),
			mcp.WithString("purpose", mcp.Required(), mcp.Description("New purpose, up to 250 characters; pass an empty string to clear it.")),
		}},
		{Name: capability.ChannelsArchive, Run: h.ConversationsArchiveHandler, Options: []mcp.ToolOption{
			mcp.WithOutputSchema[ChannelMutationResult](),
			mcp.WithDescription("Preview or archive one ordinary channel. Prepare first, then confirm the exact observed channel before execute."),
			mcp.WithString("action", mcp.Required(), mcp.Enum("prepare", "execute"), mcp.Description(descPrepareAction)),
			mcp.WithString("channel_id", mcp.Required(), mcp.Description(descChannelIDRaw)),
			mcp.WithString("approval_token", mcp.Description(descApprovalToken)),
		}},
	}
}
