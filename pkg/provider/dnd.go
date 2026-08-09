package provider

import (
	"context"

	"github.com/slack-go/slack"
)

type DNDClient interface {
	GetDNDInfoContext(context.Context, *string) (*slack.DNDStatus, error)
	SetSnoozeContext(context.Context, int) (*slack.DNDStatus, error)
	EndSnoozeContext(context.Context) (*slack.DNDStatus, error)
}

func (ap *ApiProvider) DND() (DNDClient, error) {
	client, ok := ap.client.(*MCPSlackClient)
	if !ok || client == nil || !client.IsOAuth() || client.IsBotToken() {
		return nil, ErrUserOAuthRequired
	}
	return client, nil
}

func (c *MCPSlackClient) GetDNDInfoContext(ctx context.Context, user *string) (*slack.DNDStatus, error) {
	return c.standardSlackClient().GetDNDInfoContext(ctx, user)
}

func (c *MCPSlackClient) SetSnoozeContext(ctx context.Context, minutes int) (*slack.DNDStatus, error) {
	if c.IsBotToken() {
		return nil, ErrUserOAuthRequired
	}
	return c.standardSlackClient().SetSnoozeContext(ctx, minutes)
}

func (c *MCPSlackClient) EndSnoozeContext(ctx context.Context) (*slack.DNDStatus, error) {
	if c.IsBotToken() {
		return nil, ErrUserOAuthRequired
	}
	return c.standardSlackClient().EndSnoozeContext(ctx)
}
