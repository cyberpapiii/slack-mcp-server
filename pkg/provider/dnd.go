package provider

import (
	"context"

	"github.com/slack-go/slack"
)

// DND is the Web API client; the handler takes it as handler.DNDAPI.
func (ap *ApiProvider) DND() *WebClient {
	return ap.WebAPI()
}

func (c *WebClient) GetDNDInfoContext(ctx context.Context, user *string) (*slack.DNDStatus, error) {
	return c.resolve().GetDNDInfoContext(ctx, user)
}

func (c *WebClient) SetSnoozeContext(ctx context.Context, minutes int) (*slack.DNDStatus, error) {
	return c.resolve().SetSnoozeContext(ctx, minutes)
}

func (c *WebClient) EndSnoozeContext(ctx context.Context) (*slack.DNDStatus, error) {
	return c.resolve().EndSnoozeContext(ctx)
}
