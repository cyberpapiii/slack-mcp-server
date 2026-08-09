package provider

import (
	"context"
	"encoding/json"
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

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryCredentialStore struct {
	mu     sync.Mutex
	record OAuthTokenRecord
}

type flakyCredentialStore struct {
	memoryCredentialStore
	failSaves atomic.Int32
}

func (s *flakyCredentialStore) SaveIfGeneration(ctx context.Context, expected uint64, record OAuthTokenRecord) error {
	if s.failSaves.Add(-1) >= 0 {
		return errors.New("temporary Keychain failure")
	}
	return s.memoryCredentialStore.SaveIfGeneration(ctx, expected, record)
}

func (s *memoryCredentialStore) Load(context.Context) (OAuthTokenRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record, nil
}

func (s *memoryCredentialStore) SaveIfGeneration(_ context.Context, expected uint64, record OAuthTokenRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record.Generation != expected {
		return ErrCredentialGenerationChanged
	}
	s.record = record
	return nil
}

func (s *memoryCredentialStore) Delete(context.Context) error { return nil }

type countingRefresher struct {
	calls atomic.Int32
	now   time.Time
}

func (r *countingRefresher) Refresh(context.Context, string) (OAuthRefreshResult, error) {
	r.calls.Add(1)
	return OAuthRefreshResult{
		AccessToken: "rotated-access", RefreshToken: "rotated-refresh",
		ExpiresAt: r.now.Add(time.Hour), Scopes: []string{"channels:read"},
	}, nil
}

func TestUnitOAuthTokenManagerRefreshesOnceForConcurrentCallers(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	store := &memoryCredentialStore{record: OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "old-access", RefreshToken: "old-refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 4,
	}}
	refresher := &countingRefresher{now: now}
	sharedLock := withOAuthFileLock(t.TempDir() + "/oauth-refresh.lock")
	managers := []*OAuthTokenManager{
		NewOAuthTokenManager(store, refresher, sharedLock),
		NewOAuthTokenManager(store, refresher, sharedLock),
	}
	for _, manager := range managers {
		manager.now = func() time.Time { return now }
	}

	var wg sync.WaitGroup
	results := make(chan OAuthTokenRecord, 8)
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

func TestUnitOAuthTokenManagerRejectsExpiredCredentialWithoutRotation(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryCredentialStore{record: OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "expired", ExpiresAt: now.Add(-time.Minute),
	}}
	manager := NewOAuthTokenManager(store, nil, nil)
	manager.now = func() time.Time { return now }

	_, err := manager.Current(context.Background())
	assert.ErrorIs(t, err, ErrOAuthRefreshUnavailable)
}

func TestUnitPKCECallbackIsExactAndOneUse(t *testing.T) {
	authz, err := NewPKCEAuthorization("http://127.0.0.1:19453/oauth/callback")
	require.NoError(t, err)
	assert.NotEmpty(t, authz.Verifier)
	assert.NotEmpty(t, authz.Challenge)

	assert.Error(t, authz.ValidateCallback("wrong", authz.RedirectURI))
	require.NoError(t, authz.ValidateCallback(authz.State, authz.RedirectURI))
	assert.Error(t, authz.ValidateCallback(authz.State, authz.RedirectURI))
	_, err = NewPKCEAuthorization("https://example.com/oauth/callback")
	assert.Error(t, err)
}

func TestUnitPKCECallbackExpires(t *testing.T) {
	authz, err := NewPKCEAuthorization("http://127.0.0.1:19453/oauth/callback")
	require.NoError(t, err)
	authz.now = func() time.Time { return authz.ExpiresAt }

	err = authz.ValidateCallback(authz.State, authz.RedirectURI)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestUnitOAuthCredentialStatusNeverContainsTokens(t *testing.T) {
	original := getenv
	t.Cleanup(func() { getenv = original })
	getenv = func(key string) string {
		if key == "SLACK_MCP_XOXP_TOKEN" {
			return "sentinel-access-secret"
		}
		return ""
	}
	status := OAuthStatusFromEnvironment()
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sentinel-access-secret")
	assert.Equal(t, "static_user_oauth", status.Source)
	assert.False(t, status.RotationConfigured)
}

func TestUnitOAuthTokenRecordStringIsRedacted(t *testing.T) {
	record := OAuthTokenRecord{Version: oauthRecordVersion, AccessToken: "sentinel-access", RefreshToken: "sentinel-refresh"}
	formatted := fmt.Sprintf("%v", record)
	assert.NotContains(t, formatted, "sentinel")
	assert.Contains(t, formatted, "REDACTED")
}

func TestUnitOAuthTokenManagerPreservesOldRecordOnRefreshFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryCredentialStore{record: OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 7,
	}}
	manager := NewOAuthTokenManager(store, failingRefresher{}, nil)
	manager.now = func() time.Time { return now }

	_, err := manager.Current(context.Background())
	assert.Error(t, err)
	record, _ := store.Load(context.Background())
	assert.Equal(t, "old", record.AccessToken)
	assert.Equal(t, uint64(7), record.Generation)
}

func TestUnitOAuthTokenManagerPreservesRotationBeforeValidation(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryCredentialStore{record: OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 2,
	}}
	manager := NewOAuthTokenManager(store, &countingRefresher{now: now}, nil).WithValidator(
		func(context.Context, OAuthTokenRecord) error { return errors.New("identity mismatch") },
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

func TestUnitOAuthTokenManagerRejectsScopeLoss(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryCredentialStore{record: OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 2, Scopes: []string{"channels:read", "chat:write"},
	}}
	manager := NewOAuthTokenManager(store, &countingRefresher{now: now}, nil)
	manager.now = func() time.Time { return now }

	_, err := manager.Current(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lost required scopes")
	record, loadErr := store.Load(context.Background())
	require.NoError(t, loadErr)
	assert.Equal(t, "rotated-refresh", record.RefreshToken)
	assert.Equal(t, uint64(3), record.Generation)
}

func TestUnitOAuthTokenManagerRejectsGenerationChange(t *testing.T) {
	store := &memoryCredentialStore{record: OAuthTokenRecord{Version: oauthRecordVersion, AccessToken: "current", Generation: 9}}
	err := store.SaveIfGeneration(context.Background(), 8, OAuthTokenRecord{Version: oauthRecordVersion, AccessToken: "new", Generation: 9})
	assert.ErrorIs(t, err, ErrCredentialGenerationChanged)
}

func TestUnitOAuthTokenManagerRetriesPendingRotationAfterStoreFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &flakyCredentialStore{memoryCredentialStore: memoryCredentialStore{record: OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "old", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Minute), Generation: 2, Scopes: []string{"channels:read"},
	}}}
	store.failSaves.Store(1)
	refresher := &countingRefresher{now: now}
	manager := NewOAuthTokenManager(store, refresher, nil)
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
	store := &memoryCredentialStore{record: OAuthTokenRecord{Version: oauthRecordVersion, AccessToken: "old", Generation: 4}}
	record := OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: "new", RefreshToken: "refresh",
		ExpiresAt: now.Add(time.Hour), Generation: 5, Scopes: []string{"channels:read"},
	}
	err := CommitAuthorizedOAuthCredential(context.Background(), store, 4, record, []string{"channels:read", "chat:write"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required scopes")

	record.Scopes = append(record.Scopes, "chat:write")
	err = CommitAuthorizedOAuthCredential(context.Background(), store, 4, record, record.Scopes,
		func(context.Context, OAuthTokenRecord) error { return errors.New("wrong user") })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong user")

	require.NoError(t, CommitAuthorizedOAuthCredential(context.Background(), store, 4, record, record.Scopes,
		func(context.Context, OAuthTokenRecord) error { return nil }))
	saved, _ := store.Load(context.Background())
	assert.Equal(t, "new", saved.AccessToken)
}

func TestUnitOAuthCodeExchangeUsesPKCEAndFixedSlackEndpoint(t *testing.T) {
	authorization, err := NewPKCEAuthorization("http://127.0.0.1:19453/oauth/callback")
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
	record, err := ExchangeOAuthAuthorizationCode(
		context.Background(), client, "client-id", "client-secret", "code",
		authorization.State, authorization.RedirectURI, authorization, now,
	)
	require.NoError(t, err)
	assert.Equal(t, "T1", record.TeamID)
	assert.Equal(t, "U1", record.UserID)
	assert.Equal(t, now.Add(12*time.Hour), record.ExpiresAt)
	assert.ElementsMatch(t, []string{"channels:read", "chat:write"}, record.Scopes)
}

func TestUnitSlackOAuthRefresherSupportsPKCEPublicClient(t *testing.T) {
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
	refresher := &SlackOAuthRefresher{HTTPClient: client, ClientID: "client-id", now: func() time.Time { return now }}

	result, err := refresher.Refresh(context.Background(), "refresh-secret")
	require.NoError(t, err)
	assert.Equal(t, "xoxe.xoxp-new", result.AccessToken)
	assert.Equal(t, "xoxe-refresh-new", result.RefreshToken)
	assert.Equal(t, now.Add(12*time.Hour), result.ExpiresAt)
	assert.ElementsMatch(t, []string{"channels:read", "chat:write"}, result.Scopes)
}

func TestUnitSlackOAuthRefresherPreservesRetryAfter(t *testing.T) {
	client := &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     http.Header{"Retry-After": []string{"180"}},
		}, nil
	})}
	refresher := &SlackOAuthRefresher{HTTPClient: client, ClientID: "client-id"}

	_, err := refresher.Refresh(context.Background(), "refresh-secret")
	var limited *slack.RateLimitedError
	require.ErrorAs(t, err, &limited)
	assert.Equal(t, 3*time.Minute, limited.RetryAfter)
}

func TestUnitSlackOAuthRefresherRejectsMissingExpiry(t *testing.T) {
	client := &http.Client{Transport: oauthRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"ok":true,"authed_user":{"access_token":"xoxe.xoxp-new","refresh_token":"xoxe-refresh-new","scope":"channels:read"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	refresher := &SlackOAuthRefresher{HTTPClient: client, ClientID: "client-id"}

	_, err := refresher.Refresh(context.Background(), "refresh-secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incomplete rotated credential")
}

func TestUnitOAuthCredentialStatusSupportsPKCEPublicClient(t *testing.T) {
	original := getenv
	t.Cleanup(func() { getenv = original })
	getenv = func(key string) string {
		switch key {
		case "SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT":
			return "loop-user"
		case "SLACK_MCP_OAUTH_CLIENT_ID":
			return "client-id"
		default:
			return ""
		}
	}

	status := OAuthStatusFromEnvironment()
	assert.Equal(t, "configured", status.State)
	assert.True(t, status.RotationConfigured)
}

type oauthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f oauthRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingRefresher struct{}

func (failingRefresher) Refresh(context.Context, string) (OAuthRefreshResult, error) {
	return OAuthRefreshResult{}, errors.New("revoked")
}
