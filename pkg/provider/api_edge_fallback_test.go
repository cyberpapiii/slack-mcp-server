package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
	"github.com/korotovsky/slack-mcp-server/pkg/slackcreds"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIsBrowserSessionAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"invalid_auth", slack.SlackErrorResponse{Err: "invalid_auth"}, true},
		{"not_authed", slack.SlackErrorResponse{Err: "not_authed"}, true},
		{"code as plain text", errors.New("invalid_auth"), false},
		{"edge api error code", slack.SlackErrorResponse{Err: "token_revoked"}, true},
		{"wrapped edge api error", fmt.Errorf("saved.list: %w: [ERROR] detail", slack.SlackErrorResponse{Err: "invalid_auth"}), true},
		{"edge api non-auth code", slack.SlackErrorResponse{Err: "ratelimited"}, false},
		{"http 401", slack.StatusCodeError{Code: 401}, true},
		{"http 500", slack.StatusCodeError{Code: 500}, false},
		{"slack error response", slack.SlackErrorResponse{Err: "account_inactive"}, true},
		{"prose mentioning session", errors.New("session expired"), false},
		{"own degraded error", ErrBrowserSessionUnavailable, false},
		{"generic error", errors.New("timeout"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isBrowserSessionAuthError(tt.err))
		})
	}
}

// The degradation message reaches osascript as an argv element, never
// interpolated into the AppleScript source, so a reason carrying quotes or
// backslashes cannot inject script.
func TestUnitOsascriptNotificationArgs(t *testing.T) {
	t.Run("the reason is passed verbatim outside the script", func(t *testing.T) {
		reason := `has "quotes" and \backslashes\ and ` + "`backticks`"
		args := osascriptNotificationArgs(reason)
		plain := osascriptNotificationArgs("plain")

		require.Len(t, args, 3)
		assert.Equal(t, "-e", args[0])
		assert.Equal(t, plain[1], args[1], "the script must not vary with the reason")
		assert.NotContains(t, args[1], "quotes")
		assert.Equal(t, reason, args[2], "reason must appear verbatim, with no escaping")
	})

	t.Run("long reason is truncated on a rune boundary", func(t *testing.T) {
		// Place a multi-byte rune (3 bytes in UTF-8) straddling byte offset
		// 200: 199 ASCII bytes then "世" spans bytes 199-201. A naive
		// byte-slice truncation at 200 bytes would split that rune and
		// produce invalid UTF-8; []rune truncation at 200 runes must not.
		reason := strings.Repeat("a", 199) + "世" + strings.Repeat("b", 300)
		message := osascriptNotificationArgs(reason)[2]

		assert.True(t, utf8.ValidString(message), "truncated message must be valid UTF-8")
		assert.Equal(t, strings.Repeat("a", 199)+"世"+"…", message)
	})
}

func TestBrowserDegradationState(t *testing.T) {
	origNotifier := browserDegradationNotifier
	defer func() { browserDegradationNotifier = origNotifier }()

	notifies := 0
	browserDegradationNotifier = func(reason string, logger *zap.Logger) {
		notifies++
	}

	c := &MCPSlackClient{mode: ModeBrowser}
	c.logger = zap.NewNop()
	c.initBrowserState()
	assert.True(t, c.browserFeaturesAvailable())

	c.degradeBrowserSession(errors.New("invalid_auth"))
	assert.False(t, c.browserFeaturesAvailable())
	assert.Equal(t, "invalid_auth", c.browserDegradedReason())

	c.degradeBrowserSession(errors.New("invalid_auth"))
	assert.Equal(t, 1, notifies, "degradation should only notify once")
}

func TestAttachBrowserRejectsProviderIdentityMismatch(t *testing.T) {
	c := &MCPSlackClient{authResponse: slack.AuthTestResponse{TeamID: "T1", UserID: "U1"}, logger: zap.NewNop()}
	browserAuth, err := slackcreds.New("xoxc-browser", "xoxd-cookie")
	require.NoError(t, err)

	err = c.attachBrowserSession(browserAuth, &slack.AuthTestResponse{TeamID: "T1", UserID: "U2"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity mismatch")
	assert.False(t, c.browserFeaturesAvailable())
}

func TestManagedOAuthRefreshReplacesRuntimeClient(t *testing.T) {
	now := time.Now().UTC()
	oldClient := slack.New("old")
	store := &memoryStore{record: oauthcred.Record{
		Version: oauthcred.RecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 3,
	}}
	manager := oauthcred.NewManager(store, &hookRefresher{now: now}, noLock)
	var authTestToken string
	slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/auth.test", r.URL.Path)
		authTestToken = r.FormValue("token")
		_, _ = w.Write([]byte(`{"ok":true,"team_id":"T1","user_id":"U1"}`))
	}))
	t.Cleanup(slackServer.Close)
	c := &MCPSlackClient{
		slackClient: oldClient, oauthManager: manager, logger: zap.NewNop(),
		httpClient: slackServer.Client(), authResponse: slack.AuthTestResponse{URL: slackServer.URL + "/"},
	}
	c.oauthGeneration.Store(3)

	require.NoError(t, c.refreshManagedOAuthOnce(context.Background()))
	assert.NotSame(t, oldClient, c.standardSlackClient())
	assert.Equal(t, "rotated-access", authTestToken, "the replacement client carries the rotated token")
	assert.Equal(t, "rotated-access", (&WebClient{client: c}).token())
	assert.Equal(t, uint64(4), c.oauthGeneration.Load())
	assert.Equal(t, "live", c.oauthRuntimeState.Load())
}

func TestManagedOAuthRefreshIntervalHonorsBoundedRetryAfter(t *testing.T) {
	assert.Equal(t, time.Minute, managedOAuthRefreshInterval(nil))
	assert.Equal(t, 3*time.Minute, managedOAuthRefreshInterval(
		fmt.Errorf("refresh failed: %w", &slack.RateLimitedError{RetryAfter: 3 * time.Minute}),
	))
	assert.Equal(t, 15*time.Minute, managedOAuthRefreshInterval(
		&slack.RateLimitedError{RetryAfter: time.Hour},
	))
}

func TestBrowserKeychainFailureDegradesWhenOAuthExists(t *testing.T) {
	originalLoader := loadBrowserCredential
	t.Cleanup(func() { loadBrowserCredential = originalLoader })
	loadBrowserCredential = func(context.Context, string) (BrowserTokenRecord, error) {
		return BrowserTokenRecord{}, errors.New("Keychain locked")
	}

	credentials, err := resolveBrowserStartupCredentials(
		context.Background(), "workspace-user", "xoxp-oauth", "xoxc-stale", "xoxd-stale",
	)
	require.NoError(t, err)
	assert.Empty(t, credentials.xoxc)
	assert.Empty(t, credentials.xoxd)
	assert.EqualError(t, credentials.degraded, "Keychain locked")

	_, err = resolveBrowserStartupCredentials(context.Background(), "workspace-user", "", "", "")
	assert.EqualError(t, err, "Keychain locked")
}
