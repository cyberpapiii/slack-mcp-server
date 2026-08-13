package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

const channelManagementGate = "SLACK_MCP_CHANNEL_MANAGEMENT_TOOL"

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
	approvals *approval.Store
	identity  func() provider.ProviderIdentity
	logger    *zap.Logger
}

type ChannelMutationData struct {
	Action        string                         `json:"action" jsonschema_description:"Mutation performed"`
	Phase         string                         `json:"phase"`
	Channel       *provider.ChannelMutationState `json:"channel,omitempty" jsonschema_description:"Observed or resulting channel state"`
	ApprovalToken string                         `json:"approval_token,omitempty"`
	ExpiresAt     string                         `json:"expires_at,omitempty"`
}

type ChannelMutationResult = ToolResult[ChannelMutationData]

func NewChannelMutationHandler(service ChannelMutationService, approvals *approval.Store, identity func() provider.ProviderIdentity, logger *zap.Logger) *ChannelMutationHandler {
	return &ChannelMutationHandler{service: service, approvals: approvals, identity: identity, logger: logger}
}

func (h *ChannelMutationHandler) ConversationsRenameHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ConversationsRenameHandler called", request)
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	if err := requireChannelMutationAllowed("channels_rename", channelID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(request.GetString("name", ""))
	if name == "" {
		return nil, errors.New("name is required and must not be empty")
	}
	if !channelNamePattern.MatchString(name) {
		return nil, errors.New("name must be 1-80 lowercase letters, numbers, hyphens, or underscores")
	}
	state, err := h.service.Rename(ctx, channelID, name)
	return mutationResult("rename", state, err)
}

func (h *ChannelMutationHandler) ConversationsSetTopicHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ConversationsSetTopicHandler called", request)
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	if err := requireChannelMutationAllowed("channels_set_topic", channelID); err != nil {
		return nil, err
	}
	topic, err := requiredPresentString(request, "topic")
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(topic) > 250 {
		return nil, errors.New("topic must be at most 250 characters")
	}
	state, err := h.service.SetTopic(ctx, channelID, topic)
	return mutationResult("set_topic", state, err)
}

func (h *ChannelMutationHandler) ConversationsSetPurposeHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ConversationsSetPurposeHandler called", request)
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	if err := requireChannelMutationAllowed("channels_set_purpose", channelID); err != nil {
		return nil, err
	}
	purpose, err := requiredPresentString(request, "purpose")
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(purpose) > 250 {
		return nil, errors.New("purpose must be at most 250 characters")
	}
	state, err := h.service.SetPurpose(ctx, channelID, purpose)
	return mutationResult("set_purpose", state, err)
}

func (h *ChannelMutationHandler) ConversationsArchiveHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "ConversationsArchiveHandler called", request)
	channelID, err := requiredChannelID(request)
	if err != nil {
		return nil, err
	}
	if err := requireChannelMutationAllowed("channels_archive", channelID); err != nil {
		return nil, err
	}
	action := strings.TrimSpace(request.GetString("action", "prepare"))
	if action != "prepare" && action != "execute" {
		return nil, errors.New("action must be prepare or execute")
	}
	identity := h.identity()
	if identity.TeamID == "" || identity.UserID == "" || identity.ActorType != "user" {
		return nil, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	preparation, err := h.service.PrepareArchive(ctx, channelID)
	if err != nil {
		return nil, err
	}
	binding, err := archiveBinding(identity, preparation)
	if err != nil {
		return nil, err
	}
	prepared, execute, err := prepareOrExecute(h.approvals, action, request.GetString("approval_token", ""), binding)
	if err != nil {
		return nil, err
	}
	if !execute {
		data := ChannelMutationData{Action: "archive", Phase: "prepared", Channel: &preparation.Expected, ApprovalToken: prepared.Token, ExpiresAt: prepared.ExpiresAt.Format(time.RFC3339)}
		return NewStructuredResult(data, SlackResultMeta("", false, ""), mutationFallback(data)), nil
	}
	state, err := h.service.ArchivePrepared(ctx, preparation)
	if err != nil {
		if isAmbiguousMutationError(err) {
			return nil, &ToolError{Code: "outcome_unknown", Message: "Slack may have archived the channel; read current channel state before another attempt", Cause: err}
		}
		return nil, err
	}
	data := ChannelMutationData{Action: "archive", Phase: "executed", Channel: &state}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), mutationFallback(data)), nil
}

func requiredChannelID(request mcp.CallToolRequest) (string, error) {
	channelID := strings.TrimSpace(request.GetString("channel_id", ""))
	if channelID == "" {
		return "", errors.New("channel_id is required")
	}
	if !strings.HasPrefix(channelID, "C") && !strings.HasPrefix(channelID, "G") {
		return "", errors.New("channel_id must be a public or private channel ID starting with C or G")
	}
	return channelID, nil
}

func requiredPresentString(request mcp.CallToolRequest, field string) (string, error) {
	value, present := request.GetArguments()[field]
	if !present {
		return "", fmt.Errorf("%s is required; pass an empty string to clear it", field)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", field)
	}
	return text, nil
}

func requireChannelMutationAllowed(toolName, channelID string) error {
	config := strings.TrimSpace(os.Getenv(channelManagementGate))
	enabledTools := strings.TrimSpace(os.Getenv("SLACK_MCP_ENABLED_TOOLS"))
	allowlisted := isToolInEnabledList(enabledTools, toolName)
	if enabledTools != "" && !allowlisted {
		return fmt.Errorf("%s is disabled by SLACK_MCP_ENABLED_TOOLS", toolName)
	}
	if config == "" {
		if allowlisted {
			return nil
		}
		return fmt.Errorf("%s is disabled; set %s or add it to SLACK_MCP_ENABLED_TOOLS", toolName, channelManagementGate)
	}
	if envutil.IsTruthy(config) {
		return nil
	}
	if !isChannelAllowedForConfig(channelID, config) {
		return fmt.Errorf("%s is not allowed for channel %q", toolName, channelID)
	}
	return nil
}

func mutationResult(action string, state provider.ChannelMutationState, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return nil, err
	}
	data := ChannelMutationData{Action: action, Phase: "executed", Channel: &state}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), mutationFallback(data)), nil
}

func archiveBinding(identity provider.ProviderIdentity, preparation provider.ArchivePreparation) (approval.Binding, error) {
	arguments, err := approval.CanonicalJSON(struct {
		ChannelID string `json:"channel_id"`
	}{ChannelID: preparation.Expected.ChannelID})
	if err != nil {
		return approval.Binding{}, err
	}
	observed, err := approval.CanonicalJSON(preparation.Expected)
	if err != nil {
		return approval.Binding{}, err
	}
	return approval.Binding{TeamID: identity.TeamID, UserID: identity.UserID, Provider: "local", Tool: "channels_archive", Arguments: arguments, ObservedState: observed}, nil
}

func mutationFallback(data ChannelMutationData) string {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("channel %s %s", data.Action, data.Phase)
	}
	return string(raw)
}
