package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeDNDAPI struct {
	status *slack.DNDStatus
	set    int
	ended  bool
	err    error
}

func (f *fakeDNDAPI) GetDNDInfoContext(context.Context, *string) (*slack.DNDStatus, error) {
	return f.status, f.err
}
func (f *fakeDNDAPI) SetSnoozeContext(_ context.Context, minutes int) (*slack.DNDStatus, error) {
	f.set = minutes
	return f.status, f.err
}
func (f *fakeDNDAPI) EndSnoozeContext(context.Context) (*slack.DNDStatus, error) {
	f.ended = true
	return f.status, f.err
}

func userIdentity() provider.ProviderIdentity {
	return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "user", TokenMode: "user-oauth"}
}

func TestUnitDNDGetReturnsActorAndUTCState(t *testing.T) {
	api := &fakeDNDAPI{status: &slack.DNDStatus{Enabled: true, NextStartTimestamp: 1786291200, SnoozeInfo: slack.SnoozeInfo{SnoozeEnabled: true, SnoozeEndTime: 1786294800, SnoozeRemaining: 60}}}
	handler := NewDNDHandler(api, userIdentity, zap.NewNop())
	result, err := handler.Get(context.Background(), mcp.CallToolRequest{})
	require.NoError(t, err)
	structured := result.StructuredContent.(ToolResult[DNDStateData])
	assert.Equal(t, "U1", structured.Data.ActorUserID)
	assert.Equal(t, "2026-08-09T16:00:00Z", structured.Data.NextDNDStart)
	assert.True(t, structured.Data.SnoozeEnabled)
}

func TestUnitDNDSetValidatesDuration(t *testing.T) {
	t.Setenv("SLACK_MCP_DND_TOOL", "true")
	api := &fakeDNDAPI{status: &slack.DNDStatus{}}
	handler := NewDNDHandler(api, userIdentity, zap.NewNop())
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"minutes": 0}}}
	_, err := handler.SetSnooze(context.Background(), request)
	require.Error(t, err)
	assert.Zero(t, api.set)

	request.Params.Arguments = map[string]any{"minutes": 90}
	_, err = handler.SetSnooze(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, 90, api.set)
}

func TestUnitDNDMutationsRejectBotIdentity(t *testing.T) {
	t.Setenv("SLACK_MCP_DND_TOOL", "true")
	api := &fakeDNDAPI{status: &slack.DNDStatus{}}
	handler := NewDNDHandler(api, func() provider.ProviderIdentity {
		return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "bot"}
	}, zap.NewNop())
	_, err := handler.EndSnooze(context.Background(), mcp.CallToolRequest{})
	require.Error(t, err)
	assert.False(t, api.ended)
}

func TestUnitDNDErrorsAreTyped(t *testing.T) {
	api := &fakeDNDAPI{status: &slack.DNDStatus{}, err: errors.New("missing_scope")}
	handler := NewDNDHandler(api, userIdentity, zap.NewNop())
	_, err := handler.Get(context.Background(), mcp.CallToolRequest{})
	var typed *ToolError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "read_dnd_failed", typed.Code)
}

func TestUnitDNDMutationTimeoutIsOutcomeUnknown(t *testing.T) {
	t.Setenv("SLACK_MCP_DND_TOOL", "true")
	api := &fakeDNDAPI{err: context.DeadlineExceeded}
	handler := NewDNDHandler(api, userIdentity, zap.NewNop())
	request := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"minutes": 30}}}
	_, err := handler.SetSnooze(context.Background(), request)
	var typed *ToolError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "outcome_unknown", typed.Code)
}
