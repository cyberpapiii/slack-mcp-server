package provider

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/slackcreds"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// degradedBrowserClient is an OAuth client whose attached browser session
// has degraded, with probe as its re-probe.
func degradedBrowserClient(t *testing.T, probe browserProbeFunc) *MCPSlackClient {
	t.Helper()
	origNotifier := browserDegradationNotifier
	t.Cleanup(func() { browserDegradationNotifier = origNotifier })
	browserDegradationNotifier = func(string, *zap.Logger) {}

	c := &MCPSlackClient{
		authResponse:      &slack.AuthTestResponse{TeamID: "T1", UserID: "U1"},
		logger:            zap.NewNop(),
		isOAuth:           true,
		browserConfigured: true,
		browserProbe:      probe,
	}
	c.initBrowserState()
	c.degradeBrowserSession(errors.New("invalid_auth"))
	require.False(t, c.browserFeaturesAvailable())
	return c
}

func probeAs(t *testing.T, teamID, userID string) browserProbeFunc {
	return func(context.Context) (slackcreds.Value, *slack.AuthTestResponse, *http.Client, error) {
		creds, err := slackcreds.New("xoxc-browser", "")
		require.NoError(t, err)
		return creds, &slack.AuthTestResponse{TeamID: teamID, UserID: userID}, http.DefaultClient, nil
	}
}

func TestUnitBrowserReprobeRecoversADegradedSession(t *testing.T) {
	c := degradedBrowserClient(t, probeAs(t, "T1", "U1"))

	require.NoError(t, c.reprobeBrowser(context.Background()))
	assert.True(t, c.browserFeaturesAvailable())
	assert.Empty(t, c.browserDegradedReason())
	assert.NotNil(t, c.edgeClient.Load())
}

func TestUnitBrowserReprobeKeepsTheSessionDegradedWhileTheCredentialFails(t *testing.T) {
	c := degradedBrowserClient(t, func(context.Context) (slackcreds.Value, *slack.AuthTestResponse, *http.Client, error) {
		return slackcreds.Value{}, nil, nil, errors.New("invalid_auth")
	})

	assert.Error(t, c.reprobeBrowser(context.Background()))
	assert.False(t, c.browserFeaturesAvailable())
	assert.Equal(t, "invalid_auth", c.browserDegradedReason())
}

func TestUnitBrowserReprobeRefusesAnotherUsersSession(t *testing.T) {
	c := degradedBrowserClient(t, probeAs(t, "T1", "U2"))

	assert.Error(t, c.reprobeBrowser(context.Background()))
	assert.False(t, c.browserFeaturesAvailable())
}

func TestUnitBrowserReprobeLeavesAHealthySessionAlone(t *testing.T) {
	var probes atomic.Int32
	c := degradedBrowserClient(t, func(context.Context) (slackcreds.Value, *slack.AuthTestResponse, *http.Client, error) {
		probes.Add(1)
		return probeAs(t, "T1", "U1")(context.Background())
	})
	require.NoError(t, c.reprobeBrowser(context.Background()))
	require.NoError(t, c.reprobeBrowser(context.Background()))
	assert.Equal(t, int32(1), probes.Load(), "only the degraded session is probed")
}

func TestUnitBrowserDegradationNotifiesAgainAfterRecovery(t *testing.T) {
	c := degradedBrowserClient(t, probeAs(t, "T1", "U1"))
	notifies := 0
	browserDegradationNotifier = func(string, *zap.Logger) { notifies++ }

	require.NoError(t, c.reprobeBrowser(context.Background()))
	c.degradeBrowserSession(errors.New("token_revoked"))
	assert.Equal(t, 1, notifies, "a fresh degradation after a recovery is news again")
	assert.Equal(t, "token_revoked", c.browserDegradedReason())
}

func TestUnitBrowserReprobeLoopRetriesUntilRecoveredAndStopsOnCancel(t *testing.T) {
	var probes atomic.Int32
	c := degradedBrowserClient(t, func(ctx context.Context) (slackcreds.Value, *slack.AuthTestResponse, *http.Client, error) {
		if probes.Add(1) < 3 {
			return slackcreds.Value{}, nil, nil, errors.New("invalid_auth")
		}
		return probeAs(t, "T1", "U1")(ctx)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runBrowserReprobe(ctx, 5*time.Millisecond)
		close(done)
	}()

	assert.Eventually(t, c.browserFeaturesAvailable, time.Second, 5*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("re-probe loop did not stop on cancel")
	}
	assert.Equal(t, int32(3), probes.Load(), "probing stops once the session is back")
}

func TestUnitMarkBrowserUnavailableRegistersADegradedSession(t *testing.T) {
	origNotifier := browserDegradationNotifier
	t.Cleanup(func() { browserDegradationNotifier = origNotifier })
	browserDegradationNotifier = func(string, *zap.Logger) {}

	c := &MCPSlackClient{logger: zap.NewNop(), isOAuth: true}
	c.initBrowserState()
	ap := &ApiProvider{client: c}
	markBrowserUnavailable(ap, errors.New("load browser credential: item not found"))

	assert.True(t, ap.ConfiguredWithBrowserSession(), "browser tools register and explain the failure")
	assert.False(t, ap.BrowserFeaturesAvailable())
	assert.Contains(t, ap.BrowserDegradedReason(), "item not found")
}
