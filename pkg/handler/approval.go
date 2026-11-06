package handler

import (
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/mark3labs/mcp-go/mcp"
)

// identityFunc lets constructors accept a nil identity callback so call sites
// never guard; a nil callback reports the zero identity, which
// requireUserActor rejects as user_oauth_required.
func identityFunc(identity func() provider.ProviderIdentity) func() provider.ProviderIdentity {
	if identity == nil {
		return func() provider.ProviderIdentity { return provider.ProviderIdentity{} }
	}
	return identity
}

// requireUserActor accepts only a Slack user identity: personal actions and
// approval tokens are bound to a team and user.
func requireUserActor(identity provider.ProviderIdentity) (provider.ProviderIdentity, error) {
	if identity.ActorType != "user" || identity.TeamID == "" || identity.UserID == "" {
		return provider.ProviderIdentity{}, &ToolError{Code: "user_oauth_required", Message: provider.ErrUserOAuthRequired.Error()}
	}
	return identity, nil
}

// ApprovalPhase is the prepare/execute envelope every destructive tool returns.
type ApprovalPhase struct {
	Phase         string `json:"phase"`
	ApprovalToken string `json:"approval_token,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

// Prepared reports whether this call only previewed the change.
func (p ApprovalPhase) Prepared() bool { return p.Phase == "prepared" }

// approvalAction reads the action argument; prepare previews, execute acts.
func approvalAction(request mcp.CallToolRequest) (prepare bool, err error) {
	switch strings.TrimSpace(request.GetString("action", "")) {
	case "prepare":
		return true, nil
	case "execute":
		return false, nil
	default:
		return false, invalidArguments("action must be prepare or execute")
	}
}

// approvalGate is the prepare/execute contract of a destructive tool. prepare
// issues a token bound to the actor, the tool, its arguments and the Slack
// state the caller was shown; execute spends that token, and fails if any of
// them changed.
type approvalGate struct {
	store    *approval.Store
	identity func() provider.ProviderIdentity
}

func (g approvalGate) check(request mcp.CallToolRequest, tool string, prepare bool, arguments, observed any) (ApprovalPhase, error) {
	actor, err := requireUserActor(g.identity())
	if err != nil {
		return ApprovalPhase{}, err
	}
	binding, err := approval.NewBinding(actor.TeamID, actor.UserID, tool, arguments, observed)
	if err != nil {
		return ApprovalPhase{}, err
	}
	if prepare {
		token, expiresAt, err := g.store.Prepare(binding)
		if err != nil {
			return ApprovalPhase{}, err
		}
		return ApprovalPhase{Phase: "prepared", ApprovalToken: token, ExpiresAt: expiresAt.Format(time.RFC3339)}, nil
	}
	token := strings.TrimSpace(request.GetString("approval_token", ""))
	if token == "" {
		return ApprovalPhase{}, &ToolError{Code: "approval_required", Message: "approval_token is required for execute"}
	}
	if err := g.store.Consume(token, binding); err != nil {
		return ApprovalPhase{}, &ToolError{Code: "approval_invalid", Message: err.Error(), Cause: err}
	}
	return ApprovalPhase{Phase: "executed"}, nil
}
