package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

const maxSnoozeMinutes = 7 * 24 * 60

type DNDAPI interface {
	GetDNDInfoContext(context.Context, *string) (*slack.DNDStatus, error)
	SetSnoozeContext(context.Context, int) (*slack.DNDStatus, error)
	EndSnoozeContext(context.Context) (*slack.DNDStatus, error)
}

type DNDStateData struct {
	ActorUserID       string `json:"actor_user_id"`
	DNDEnabled        bool   `json:"dnd_enabled"`
	NextDNDStart      string `json:"next_dnd_start,omitempty"`
	NextDNDEnd        string `json:"next_dnd_end,omitempty"`
	SnoozeEnabled     bool   `json:"snooze_enabled"`
	SnoozeEndsAt      string `json:"snooze_ends_at,omitempty"`
	SnoozeMinutesLeft int    `json:"snooze_minutes_left,omitempty"`
}

type DNDHandler struct {
	api      DNDAPI
	identity func() provider.ProviderIdentity
	logger   *zap.Logger
}

func NewDNDHandler(api DNDAPI, identity func() provider.ProviderIdentity, logger *zap.Logger) *DNDHandler {
	return &DNDHandler{api: api, identity: identityFunc(identity), logger: logger}
}

func (h *DNDHandler) Get(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "DNDGetHandler called", request)
	identity, err := h.userIdentity()
	if err != nil {
		return nil, err
	}
	status, err := h.api.GetDNDInfoContext(ctx, nil)
	if err != nil {
		return nil, TypeError(err, false)
	}
	return dndResult(identity.UserID, status)
}

func (h *DNDHandler) SetSnooze(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "DNDSetSnoozeHandler called", request)
	identity, err := h.userIdentity()
	if err != nil {
		return nil, err
	}
	minutes := request.GetInt("minutes", 0)
	if minutes < 1 || minutes > maxSnoozeMinutes {
		return nil, &ToolError{Code: "invalid_snooze_duration", Message: fmt.Sprintf("minutes must be between 1 and %d", maxSnoozeMinutes)}
	}
	status, err := h.api.SetSnoozeContext(ctx, minutes)
	if err != nil {
		return nil, TypeError(err, true)
	}
	return dndResult(identity.UserID, status)
}

func (h *DNDHandler) EndSnooze(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logToolCall(h.logger, "DNDEndSnoozeHandler called", request)
	identity, err := h.userIdentity()
	if err != nil {
		return nil, err
	}
	status, err := h.api.EndSnoozeContext(ctx)
	if err != nil {
		return nil, TypeError(err, true)
	}
	return dndResult(identity.UserID, status)
}

func (h *DNDHandler) userIdentity() (provider.ProviderIdentity, error) {
	identity := h.identity()
	if identity.ActorType != "user" || identity.TeamID == "" || identity.UserID == "" {
		return provider.ProviderIdentity{}, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	return identity, nil
}

func dndResult(userID string, status *slack.DNDStatus) (*mcp.CallToolResult, error) {
	if status == nil {
		return nil, &ToolError{Code: "invalid_slack_response", Message: "Slack returned no DND state"}
	}
	data := DNDStateData{
		ActorUserID: userID, DNDEnabled: status.Enabled,
		NextDNDStart: unixTime(status.NextStartTimestamp), NextDNDEnd: unixTime(status.NextEndTimestamp),
		SnoozeEnabled: status.SnoozeEnabled, SnoozeEndsAt: unixTime(status.SnoozeEndTime),
		SnoozeMinutesLeft: status.SnoozeRemaining,
	}
	fallback, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), string(fallback)), nil
}

func unixTime(value int) string {
	if value <= 0 {
		return ""
	}
	return time.Unix(int64(value), 0).UTC().Format(time.RFC3339)
}

// Tools lists the MCP tools this handler serves.
func (h *DNDHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.DNDGet, Run: h.Get, Options: []mcp.ToolOption{
			mcp.WithDescription("Read the authenticated user's Do Not Disturb and snooze state."),
		}},
		{Name: capability.DNDSetSnooze, Run: h.SetSnooze, Options: []mcp.ToolOption{
			mcp.WithDescription("Set a bounded Do Not Disturb snooze for the authenticated user."),
			mcp.WithNumber("minutes", mcp.Required(), mcp.Min(1), mcp.Max(maxSnoozeMinutes), mcp.Description("Snooze length in minutes, 1 to 10080 (7 days).")),
		}},
		{Name: capability.DNDEndSnooze, Run: h.EndSnooze, Options: []mcp.ToolOption{
			mcp.WithDescription("End the authenticated user's current Do Not Disturb snooze."),
		}},
	}
}
