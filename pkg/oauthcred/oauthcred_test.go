package oauthcred

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/slackhttp"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noLock runs fn without the cross-process lock, for tests whose store no
// other process shares.
func noLock(_ context.Context, fn func() error) error { return fn() }

type memoryStore struct {
	mu     sync.Mutex
	record Record
}

type flakyStore struct {
	memoryStore
	failSaves atomic.Int32
}

func (s *flakyStore) SaveIfGeneration(ctx context.Context, expected uint64, record Record) error {
	if s.failSaves.Add(-1) >= 0 {
		return errors.New("temporary Keychain failure")
	}
	return s.memoryStore.SaveIfGeneration(ctx, expected, record)
}

func (s *memoryStore) Load(context.Context) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, nil
}

func (s *memoryStore) SaveIfGeneration(_ context.Context, expected uint64, record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Generation != expected {
		return ErrGenerationChanged
	}
	s.record = record
	return nil
}

func (s *memoryStore) Delete(context.Context) error { return nil }

type countingRefresher struct {
	calls atomic.Int32
	now   time.Time
}

func (r *countingRefresher) Refresh(context.Context, string) (RefreshResult, error) {
	r.calls.Add(1)
	return RefreshResult{
		AccessToken: "rotated-access", RefreshToken: "rotated-refresh",
		ExpiresAt: r.now.Add(time.Hour), Scopes: []string{"channels:read"},
	}, nil
}

func TestUnitManagerRefreshesOnceForConcurrentCallers(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	store := &memoryStore{record: Record{
		Version: RecordVersion, AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 4,
	}}
	refresher := &countingRefresher{now: now}
	sharedLock := fileLock(t.TempDir() + "/oauth-refresh.lock")
	managers := []*Manager{
		NewManager(store, refresher, sharedLock),
		NewManager(store, refresher, sharedLock),
	}
	for _, manager := range managers {
		manager.now = func() time.Time { return now }
	}

	var wg sync.WaitGroup
	results := make(chan Record, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record, err := managers[i%len(managers)].Current(context.Background())
			require.NoError(t, err)
			results <- record
		}()
	}
	wg.Wait()
	close(results)

	assert.Equal(t, int32(1), refresher.calls.Load())
	for record := range results {
		assert.Equal(t, "rotated-access", record.AccessToken)
		assert.Equal(t, uint64(5), record.Generation)
	}
}

func TestUnitManagerRejectsExpiredCredentialWithoutRotation(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryStore{record: Record{
		Version: RecordVersion, AccessToken: "expired", ExpiresAt: now.Add(-time.Minute),
	}}
	manager := NewManager(store, nil, noLock)
	manager.now = func() time.Time { return now }

	_, err := manager.Current(context.Background())
	assert.ErrorIs(t, err, ErrRefreshUnavailable)
}

func TestUnitPKCECallbackIsExactAndOneUse(t *testing.T) {
	authz, err := NewPKCE("http://127.0.0.1:19453/oauth/callback")
	require.NoError(t, err)
	assert.NotEmpty(t, authz.Verifier)
	assert.NotEmpty(t, authz.Challenge)

	assert.Error(t, authz.ValidateCallback("wrong", authz.RedirectURI))
	require.NoError(t, authz.ValidateCallback(authz.State, authz.RedirectURI))
	assert.Error(t, authz.ValidateCallback(authz.State, authz.RedirectURI))
	_, err = NewPKCE("https://example.com/oauth/callback")
	assert.Error(t, err)
}

func TestUnitPKCECallbackExpires(t *testing.T) {
	authz, err := NewPKCE("http://127.0.0.1:19453/oauth/callback")
	require.NoError(t, err)
	authz.now = func() time.Time { return authz.ExpiresAt }

	err = authz.ValidateCallback(authz.State, authz.RedirectURI)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestUnitRecordStringIsRedacted(t *testing.T) {
	record := Record{Version: RecordVersion, AccessToken: "sentinel-access", RefreshToken: "sentinel-refresh"}
	formatted := fmt.Sprintf("%v", record)
	assert.NotContains(t, formatted, "sentinel")
	assert.Contains(t, formatted, "REDACTED")
}

func TestUnitManagerPreservesOldRecordOnRefreshFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryStore{record: Record{
		Version: RecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 7,
	}}
	manager := NewManager(store, failingRefresher{}, noLock)
	manager.now = func() time.Time { return now }

	_, err := manager.Current(context.Background())
	assert.Error(t, err)
	record, _ := store.Load(context.Background())
	assert.Equal(t, "old", record.AccessToken)
	assert.Equal(t, uint64(7), record.Generation)
}

func TestUnitManagerPreservesRotationBeforeValidation(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryStore{record: Record{
		Version: RecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 2,
	}}
	manager := NewManager(store, &countingRefresher{now: now}, noLock).WithValidator(
		func(context.Context, Record) error { return errors.New("identity mismatch") },
	)
	manager.now = func() time.Time { return now }

	_, err := manager.Current(context.Background())
	require.Error(t, err)
	record, _ := store.Load(context.Background())
	assert.Equal(t, "rotated-access", record.AccessToken)
	assert.Equal(t, "rotated-refresh", record.RefreshToken)
	assert.Equal(t, uint64(3), record.Generation)

	_, err = manager.Current(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity mismatch")
}

func TestUnitManagerRejectsScopeLoss(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryStore{record: Record{
		Version: RecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 2, Scopes: []string{"channels:read", "chat:write"},
	}}
	manager := NewManager(store, &countingRefresher{now: now}, noLock)
	manager.now = func() time.Time { return now }

	_, err := manager.Current(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lost required scopes")
	record, loadErr := store.Load(context.Background())
	require.NoError(t, loadErr)
	assert.Equal(t, "rotated-refresh", record.RefreshToken)
	assert.Equal(t, uint64(3), record.Generation)
}

func TestUnitManagerRetriesPendingRotationAfterStoreFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &flakyStore{memoryStore: memoryStore{record: Record{
		Version: RecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 2, Scopes: []string{"channels:read"},
	}}}
	store.failSaves.Store(1)
	refresher := &countingRefresher{now: now}
	manager := NewManager(store, refresher, noLock)
	manager.now = func() time.Time { return now }

	_, err := manager.Current(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persist rotated")
	assert.Equal(t, int32(1), refresher.calls.Load())

	record, err := manager.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "rotated-access", record.AccessToken)
	assert.Equal(t, "rotated-refresh", record.RefreshToken)
	assert.Equal(t, uint64(3), record.Generation)
	assert.Equal(t, int32(1), refresher.calls.Load())
}

func TestUnitAuthorizedOAuthCommitRequiresScopesAndIdentity(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryStore{record: Record{Version: RecordVersion, AccessToken: "old", Generation: 4}}
	record := Record{
		Version: RecordVersion, AccessToken: "new", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Hour), Generation: 5, Scopes: []string{"channels:read"},
	}
	err := CommitAuthorized(context.Background(), store, 4, record, []string{"channels:read", "chat:write"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required scopes")

	record.Scopes = append(record.Scopes, "chat:write")
	err = CommitAuthorized(context.Background(), store, 4, record, record.Scopes,
		func(context.Context, Record) error { return errors.New("wrong user") })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong user")

	require.NoError(t, CommitAuthorized(context.Background(), store, 4, record, record.Scopes,
		func(context.Context, Record) error { return nil }))
	saved, _ := store.Load(context.Background())
	assert.Equal(t, "new", saved.AccessToken)
}

func TestUnitOAuthCodeExchangeUsesPKCEAndFixedSlackEndpoint(t *testing.T) {
	authorization, err := NewPKCE("http://127.0.0.1:19453/oauth/callback")
	require.NoError(t, err)
	client := &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://slack.com/api/oauth.v2.access", request.URL.String())
		raw, readErr := io.ReadAll(request.Body)
		require.NoError(t, readErr)
		form, parseErr := url.ParseQuery(string(raw))
		require.NoError(t, parseErr)
		assert.Equal(t, authorization.Verifier, form.Get("code_verifier"))
		assert.Equal(t, authorization.RedirectURI, form.Get("redirect_uri"))
		body := `{"ok":true,"team":{"id":"T1"},"authed_user":{"id":"U1","access_token":"xoxe.xoxp-new","refresh_token":"xoxe-refresh","expires_in":43200,"scope":"channels:read,chat:write"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	now := time.Now().UTC()
	record, err := ExchangeCode(
		context.Background(), client, "client-id", "client-secret", "code",
		authorization.State, authorization.RedirectURI, authorization, now,
	)
	require.NoError(t, err)
	assert.Equal(t, "T1", record.TeamID)
	assert.Equal(t, "U1", record.UserID)
	assert.Equal(t, now.Add(12*time.Hour), record.ExpiresAt)
	assert.ElementsMatch(t, []string{"channels:read", "chat:write"}, record.Scopes)
}

func TestUnitSlackRefresherSupportsPKCEPublicClient(t *testing.T) {
	client := &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://slack.com/api/oauth.v2.access", request.URL.String())
		raw, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		form, err := url.ParseQuery(string(raw))
		require.NoError(t, err)
		assert.Equal(t, "client-id", form.Get("client_id"))
		assert.Equal(t, "refresh_token", form.Get("grant_type"))
		assert.Equal(t, "refresh-secret", form.Get("refresh_token"))
		assert.Empty(t, form.Get("client_secret"))
		body := `{"ok":true,"authed_user":{"access_token":"xoxe.xoxp-new","refresh_token":"xoxe-refresh-new","expires_in":43200,"scope":"channels:read,chat:write"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	refresher := &SlackRefresher{HTTPClient: client, ClientID: "client-id", now: func() time.Time { return now }}

	result, err := refresher.Refresh(context.Background(), "refresh-secret")
	require.NoError(t, err)
	assert.Equal(t, "xoxe.xoxp-new", result.AccessToken)
	assert.Equal(t, "xoxe-refresh-new", result.RefreshToken)
	assert.Equal(t, now.Add(12*time.Hour), result.ExpiresAt)
	assert.ElementsMatch(t, []string{"channels:read", "chat:write"}, result.Scopes)
}

func TestUnitSlackRefresherPreservesRetryAfter(t *testing.T) {
	client := &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{"Retry-After": []string{"180"}},
		}, nil
	})}
	refresher := &SlackRefresher{HTTPClient: client, ClientID: "client-id"}

	_, err := refresher.Refresh(context.Background(), "refresh-secret")
	var limited *slack.RateLimitedError
	require.ErrorAs(t, err, &limited)
	assert.Equal(t, 3*time.Minute, limited.RetryAfter)
}

func TestUnitSlackRefresherRejectsMissingExpiry(t *testing.T) {
	client := &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"ok":true,"authed_user":{"access_token":"xoxe.xoxp-new","refresh_token":"xoxe-refresh-new","scope":"channels:read"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	refresher := &SlackRefresher{HTTPClient: client, ClientID: "client-id"}

	_, err := refresher.Refresh(context.Background(), "refresh-secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incomplete rotated credential")
}

func TestUnitIdentityMatchRequiresTeamAndUser(t *testing.T) {
	want := Identity{TeamID: "T1", UserID: "U1"}
	require.NoError(t, want.Match(Identity{TeamID: "T1", UserID: "U1"}))
	assert.ErrorContains(t, want.Match(Identity{TeamID: "T1", UserID: "U2"}), "identity mismatch")
	assert.Error(t, Identity{}.Match(Identity{}), "an unknown expected identity matches nothing")
	assert.Error(t, want.Match(IdentityOf(nil)))
}

func TestUnitIdentityValidatorAuthTestsTheRecordToken(t *testing.T) {
	var sawToken string
	validate := Identity{TeamID: "T1", UserID: "U1"}.Validator(func(_ context.Context, token string) (*slack.AuthTestResponse, error) {
		sawToken = token
		return &slack.AuthTestResponse{TeamID: "T1", UserID: "U2"}, nil
	})

	err := validate(context.Background(), Record{AccessToken: "candidate"})
	assert.ErrorContains(t, err, "identity mismatch")
	assert.Equal(t, "candidate", sawToken)
}

func TestUnitSlackAuthTestUsesTheGivenClientAndGovSlack(t *testing.T) {
	t.Cleanup(slackhttp.SetGovSlack(true))
	client := &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://slack-gov.com/api/auth.test", request.URL.String())
		body := `{"ok":true,"team_id":"T1","user_id":"U1"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}

	response, err := SlackAuthTest(client)(context.Background(), "token")
	require.NoError(t, err)
	assert.Equal(t, Identity{TeamID: "T1", UserID: "U1"}, IdentityOf(response))
}

type oauthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f oauthRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingRefresher struct{}

func (failingRefresher) Refresh(context.Context, string) (RefreshResult, error) {
	return RefreshResult{}, errors.New("revoked")
}
