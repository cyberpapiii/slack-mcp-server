package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// oauthWithBrowser is a user OAuth client whose Web API answers stars.list
// with C_STAR and whose browser session answers client.userBoot with boot.
func oauthWithBrowser(t *testing.T, boot string) *MCPSlackClient {
	t.Helper()
	origNotifier := browserDegradationNotifier
	t.Cleanup(func() { browserDegradationNotifier = origNotifier })
	browserDegradationNotifier = func(string, *zap.Logger) {}

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"items":[{"type":"channel","channel":"C_STAR"}]}`))
	}))
	t.Cleanup(web.Close)
	c := &MCPSlackClient{
		slackClient: slack.New("xoxp-test", slack.OptionAPIURL(web.URL+"/")),
		mode:        ModeUserOAuthBrowser,
		logger:      zap.NewNop(),
	}
	c.initBrowserState()
	c.edgeClient.Store(edgeAnswering(boot))
	return c
}

func TestUnitStarredChannelsComeFromAHealthyBrowserSessionBesideOAuth(t *testing.T) {
	c := oauthWithBrowser(t, `{"ok":true,"starred":["C_BOOT"]}`)
	ids, err := c.GetStarredChannelIDs(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"C_BOOT"}, ids)

	c.degradeBrowserSession(errors.New("invalid_auth"))
	ids, err = c.GetStarredChannelIDs(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"C_STAR"}, ids, "a degraded session falls back to OAuth")
}

func TestUnitStarredChannelsFallBackToOAuthWhenTheCallFindsTheSessionDead(t *testing.T) {
	c := oauthWithBrowser(t, `{"ok":false,"error":"invalid_auth"}`)
	ids, err := c.GetStarredChannelIDs(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"C_STAR"}, ids)
	assert.False(t, c.browserFeaturesAvailable())
}

func TestUnitPrefersBrowserByMode(t *testing.T) {
	for mode, want := range map[CredentialMode]bool{
		ModeBot: false, ModeUserOAuth: false, ModeDemo: false,
		ModeBrowser: true, ModeUserOAuthBrowser: true,
	} {
		c := &MCPSlackClient{mode: mode, logger: zap.NewNop()}
		c.initBrowserState()
		assert.Equal(t, want, c.prefersBrowser(), "mode %d", mode)
	}
	degraded := &MCPSlackClient{mode: ModeUserOAuthBrowser, logger: zap.NewNop()}
	degraded.browserState.Store(int32(browserStateDegraded))
	assert.False(t, degraded.prefersBrowser())
}
