package handler

import (
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func approvalRequest(args map[string]any) mcp.CallToolRequest {
	request := mcp.CallToolRequest{}
	request.Params.Arguments = args
	return request
}

func TestApprovalGateIssuesAndSpendsOneToken(t *testing.T) {
	gate := approvalGate{store: approval.NewStore(), identity: userIdentity}
	args := map[string]string{"channel_id": "C1"}

	phase, err := gate.check(approvalRequest(nil), capability.MessagesDelete, true, args, "observed")
	require.NoError(t, err)
	assert.True(t, phase.Prepared())
	require.NotEmpty(t, phase.ApprovalToken)
	assert.NotEmpty(t, phase.ExpiresAt)

	var toolErr *ToolError
	_, err = gate.check(approvalRequest(nil), capability.MessagesDelete, false, args, "observed")
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "approval_required", toolErr.Code)

	execute := approvalRequest(map[string]any{"approval_token": phase.ApprovalToken})
	_, err = gate.check(execute, capability.MessagesDelete, false, args, "changed")
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "approval_invalid", toolErr.Code, "a changed target must not spend the token")

	executed, err := gate.check(execute, capability.MessagesDelete, false, args, "observed")
	require.NoError(t, err)
	assert.False(t, executed.Prepared())

	_, err = gate.check(execute, capability.MessagesDelete, false, args, "observed")
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "approval_invalid", toolErr.Code)
}

func TestApprovalGateNeedsAUser(t *testing.T) {
	bot := func() provider.ProviderIdentity {
		return provider.ProviderIdentity{TeamID: "T1", UserID: "U1", ActorType: "bot"}
	}
	gate := approvalGate{store: approval.NewStore(), identity: bot}
	_, err := gate.check(approvalRequest(nil), capability.MessagesDelete, true, nil, nil)
	var toolErr *ToolError
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "user_oauth_required", toolErr.Code)
}

func TestApprovalActionIsRequired(t *testing.T) {
	_, err := approvalAction(approvalRequest(map[string]any{"action": "maybe"}))
	var toolErr *ToolError
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "invalid_arguments", toolErr.Code)
	_, err = approvalAction(approvalRequest(nil))
	require.ErrorAs(t, err, &toolErr)
}
