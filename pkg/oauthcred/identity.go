package oauthcred

import (
	"context"
	"fmt"
	"net/http"

	"github.com/korotovsky/slack-mcp-server/pkg/transport"
	"github.com/slack-go/slack"
)

// Identity is the Slack workspace user a credential must belong to.
type Identity struct {
	TeamID string
	UserID string
}

func IdentityOf(response *slack.AuthTestResponse) Identity {
	if response == nil {
		return Identity{}
	}
	return Identity{TeamID: response.TeamID, UserID: response.UserID}
}

func (r Record) Identity() Identity {
	return Identity{TeamID: r.TeamID, UserID: r.UserID}
}

// Match fails unless got is the same team and user. An incomplete want
// matches nothing.
func (want Identity) Match(got Identity) error {
	if want.TeamID == "" || want.UserID == "" || got != want {
		return fmt.Errorf("identity mismatch: expected team %s user %s, received team %s user %s",
			want.TeamID, want.UserID, got.TeamID, got.UserID)
	}
	return nil
}

// AuthTestFunc runs auth.test for token.
type AuthTestFunc func(ctx context.Context, token string) (*slack.AuthTestResponse, error)

// SlackAuthTest calls auth.test through httpClient, which should come from
// transport.ProvideHTTPClient so the call honors the proxy, CA and GovSlack
// settings.
func SlackAuthTest(httpClient *http.Client) AuthTestFunc {
	return func(ctx context.Context, token string) (*slack.AuthTestResponse, error) {
		client := slack.New(token, slack.OptionHTTPClient(httpClient), slack.OptionAPIURL(transport.SlackAPIBase()))
		return client.AuthTestContext(ctx)
	}
}

// Validator returns a check for Manager.WithValidator and CommitAuthorized:
// auth.test with the record's access token must answer as want.
func (want Identity) Validator(authTest AuthTestFunc) func(context.Context, Record) error {
	return func(ctx context.Context, record Record) error {
		response, err := authTest(ctx, record.AccessToken)
		if err != nil {
			return err
		}
		return want.Match(IdentityOf(response))
	}
}
