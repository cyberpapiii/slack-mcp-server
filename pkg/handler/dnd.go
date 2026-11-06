package handler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
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
}

func NewDNDHandler(api DNDAPI, identity func() provider.ProviderIdentity) *DNDHandler {
	return &DNDHandler{api: api, identity: identity}
}

func (h *DNDHandler) Get(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	identity, err := requireUserActor(h.identity())
	if err != nil {
		return nil, err
	}
	status, err := h.api.GetDNDInfoContext(ctx, nil)
	if err != nil {
		return nil, err
	}
	return dndResult(identity.UserID, status)
}

func (h *DNDHandler) SetSnooze(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	identity, err := requireUserActor(h.identity())
	if err != nil {
		return nil, err
	}
	minutes := request.GetInt("minutes", 0)
	if minutes < 1 || minutes > maxSnoozeMinutes {
		return nil, &ToolError{Code: codeInvalidSnoozeDuration, Message: fmt.Sprintf("minutes must be between 1 and %d", maxSnoozeMinutes)}
	}
	status, err := h.api.SetSnoozeContext(ctx, minutes)
	if err != nil {
		return nil, TypeError(err, true)
	}
	return dndResult(identity.UserID, status)
}

func (h *DNDHandler) EndSnooze(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	identity, err := requireUserActor(h.identity())
	if err != nil {
		return nil, err
	}
	status, err := h.api.EndSnoozeContext(ctx)
	if err != nil {
		return nil, TypeError(err, true)
	}
	return dndResult(identity.UserID, status)
}

func dndResult(userID string, status *slack.DNDStatus) (*mcp.CallToolResult, error) {
	if status == nil {
		return nil, &ToolError{Code: codeInvalidSlackResponse, Message: "Slack returned no DND state"}
	}
	data := DNDStateData{
		ActorUserID: userID, DNDEnabled: status.Enabled,
		NextDNDStart: unixTimeText(int64(status.NextStartTimestamp)), NextDNDEnd: unixTimeText(int64(status.NextEndTimestamp)),
		SnoozeEnabled: status.SnoozeEnabled, SnoozeEndsAt: unixTimeText(int64(status.SnoozeEndTime)),
		SnoozeMinutesLeft: status.SnoozeRemaining,
	}
	fallback, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return NewStructuredResult(data, SlackResultMeta("", false, ""), string(fallback)), nil
}

func (h *DNDHandler) Tools() []ToolDef {
	return []ToolDef{
		{Name: capability.DNDGet, Run: h.Get, Options: []mcp.ToolOption{
			mcp.WithDescription("Read the authenticated user's Do Not Disturb and snooze state."),
		}},
		{Name: capability.DNDSetSnooze, Run: h.SetSnooze, Options: []mcp.ToolOption{
			mcp.WithDescription("Set a bounded Do Not Disturb snooze for the authenticated user."),
			mcp.WithNumber("minutes", mcp.Required(), mcp.Min(1), mcp.Max(maxSnoozeMinutes), mcp.Description(fmt.Sprintf("Snooze length in minutes, 1 to %d (%d days).", maxSnoozeMinutes, maxSnoozeMinutes/(24*60)))),
		}},
		{Name: capability.DNDEndSnooze, Run: h.EndSnooze, Options: []mcp.ToolOption{
			mcp.WithDescription("End the authenticated user's current Do Not Disturb snooze."),
		}},
	}
}
