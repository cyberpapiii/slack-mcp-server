package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
)

const oauthRecordVersion = 1

var ErrOAuthRefreshUnavailable = errors.New("OAuth refresh is unavailable")

// OAuthTokenRecord is persisted only through CredentialStore. Callers must
// never log or return this value.
type OAuthTokenRecord struct {
	Version      int       `json:"version"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Generation   uint64    `json:"generation"`
	TeamID       string    `json:"team_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
}

func (OAuthTokenRecord) String() string { return "[REDACTED OAuth credential]" }

func (r OAuthTokenRecord) validate() error {
	if r.Version != oauthRecordVersion {
		return fmt.Errorf("unsupported OAuth credential version %d", r.Version)
	}
	if strings.TrimSpace(r.AccessToken) == "" {
		return errors.New("OAuth credential has no access token")
	}
	return nil
}

func (r OAuthTokenRecord) expiring(now time.Time, earlyRefresh time.Duration) bool {
	return !r.ExpiresAt.IsZero() && !r.ExpiresAt.After(now.Add(earlyRefresh))
}

type CredentialStore interface {
	Load(context.Context) (OAuthTokenRecord, error)
	SaveIfGeneration(context.Context, uint64, OAuthTokenRecord) error
	Delete(context.Context) error
}

var ErrCredentialGenerationChanged = errors.New("OAuth credential generation changed")

type OAuthRefreshResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
}

type OAuthRefresher interface {
	Refresh(context.Context, string) (OAuthRefreshResult, error)
}

type SlackOAuthRefresher struct {
	HTTPClient   *http.Client
	ClientID     string
	ClientSecret string
	now          func() time.Time
}

func (r *SlackOAuthRefresher) Refresh(ctx context.Context, refreshToken string) (OAuthRefreshResult, error) {
	if r.ClientID == "" || r.ClientSecret == "" {
		return OAuthRefreshResult{}, ErrOAuthRefreshUnavailable
	}
	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := slack.RefreshOAuthV2TokenContext(ctx, client, r.ClientID, r.ClientSecret, refreshToken)
	if err != nil {
		return OAuthRefreshResult{}, err
	}
	now := r.now
	if now == nil {
		now = time.Now
	}
	accessToken := response.AuthedUser.AccessToken
	rotatedRefresh := response.AuthedUser.RefreshToken
	expiresIn := response.AuthedUser.ExpiresIn
	scope := response.AuthedUser.Scope
	if accessToken == "" {
		accessToken = response.AccessToken
		rotatedRefresh = response.RefreshToken
		expiresIn = response.ExpiresIn
		scope = response.Scope
	}
	return OAuthRefreshResult{
		AccessToken: accessToken, RefreshToken: rotatedRefresh,
		ExpiresAt: now().Add(time.Duration(expiresIn) * time.Second).UTC(),
		Scopes:    splitScopes(scope),
	}, nil
}

func splitScopes(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
	return append([]string(nil), fields...)
}

type OAuthTokenManager struct {
	store        CredentialStore
	refresher    OAuthRefresher
	lock         func(context.Context, func() error) error
	now          func() time.Time
	earlyRefresh time.Duration
	validate     func(context.Context, OAuthTokenRecord) error
	mu           sync.Mutex
}

func (m *OAuthTokenManager) WithValidator(validate func(context.Context, OAuthTokenRecord) error) *OAuthTokenManager {
	m.validate = validate
	return m
}

func NewOAuthTokenManager(store CredentialStore, refresher OAuthRefresher, lock func(context.Context, func() error) error) *OAuthTokenManager {
	if lock == nil {
		lock = func(_ context.Context, fn func() error) error { return fn() }
	}
	return &OAuthTokenManager{
		store: store, refresher: refresher, lock: lock,
		now: time.Now, earlyRefresh: 5 * time.Minute,
	}
}

// Current serializes refresh in-process and across processes. It reloads after
// taking the lock so a waiter observes the winner's rotated refresh token.
func (m *OAuthTokenManager) Current(ctx context.Context) (OAuthTokenRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var current OAuthTokenRecord
	err := m.lock(ctx, func() error {
		record, err := m.store.Load(ctx)
		if err != nil {
			return err
		}
		if err := record.validate(); err != nil {
			return err
		}
		if !record.expiring(m.now(), m.earlyRefresh) {
			current = record
			return nil
		}
		if m.refresher == nil || record.RefreshToken == "" {
			return ErrOAuthRefreshUnavailable
		}
		rotated, err := m.refresher.Refresh(ctx, record.RefreshToken)
		if err != nil {
			return fmt.Errorf("refresh OAuth credential: %w", err)
		}
		if rotated.AccessToken == "" || rotated.RefreshToken == "" || rotated.ExpiresAt.IsZero() {
			return errors.New("refresh OAuth credential: incomplete rotated credential")
		}
		if missing := missingScopes(record.Scopes, rotated.Scopes); len(missing) != 0 {
			return fmt.Errorf("refresh OAuth credential: rotated credential lost required scopes: %s", strings.Join(missing, ","))
		}
		record.AccessToken = rotated.AccessToken
		record.RefreshToken = rotated.RefreshToken
		record.ExpiresAt = rotated.ExpiresAt.UTC()
		record.Scopes = append([]string(nil), rotated.Scopes...)
		record.Generation++
		if m.validate != nil {
			if err := m.validate(ctx, record); err != nil {
				return fmt.Errorf("validate rotated OAuth credential: %w", err)
			}
		}
		if err := m.store.SaveIfGeneration(ctx, record.Generation-1, record); err != nil {
			return fmt.Errorf("persist rotated OAuth credential: %w", err)
		}
		current = record
		return nil
	})
	return current, err
}

func missingScopes(required, actual []string) []string {
	have := make(map[string]struct{}, len(actual))
	for _, scope := range actual {
		have[scope] = struct{}{}
	}
	var missing []string
	for _, scope := range required {
		if _, ok := have[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	return missing
}

type PKCEAuthorization struct {
	State       string
	Verifier    string
	Challenge   string
	RedirectURI string
	ExpiresAt   time.Time
	used        bool
	now         func() time.Time
	mu          sync.Mutex
}

func NewPKCEAuthorization(redirectURI string) (*PKCEAuthorization, error) {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Fragment != "" {
		return nil, errors.New("OAuth redirect must be an exact HTTP loopback URL")
	}
	state, err := randomURLToken(32)
	if err != nil {
		return nil, err
	}
	verifier, err := randomURLToken(64)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(verifier))
	return &PKCEAuthorization{
		State: state, Verifier: verifier,
		Challenge:   base64.RawURLEncoding.EncodeToString(digest[:]),
		RedirectURI: u.String(),
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		now:         time.Now,
	}, nil
}

func (p *PKCEAuthorization) ValidateCallback(state, redirectURI string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.used {
		return errors.New("OAuth callback already consumed")
	}
	if p.now == nil || !p.now().Before(p.ExpiresAt) {
		return errors.New("OAuth callback state expired")
	}
	if state != p.State || redirectURI != p.RedirectURI {
		return errors.New("OAuth callback state or redirect mismatch")
	}
	p.used = true
	return nil
}

func randomURLToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate OAuth secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// CommitAuthorizedOAuthCredential is final authorization-code boundary. Caller
// exchanges code using Slack app configuration, then supplies exact auth.test
// identity validation before any credential becomes durable.
func CommitAuthorizedOAuthCredential(
	ctx context.Context,
	store CredentialStore,
	expectedGeneration uint64,
	record OAuthTokenRecord,
	requiredScopes []string,
	validateIdentity func(context.Context, OAuthTokenRecord) error,
) error {
	if err := record.validate(); err != nil {
		return err
	}
	if record.RefreshToken == "" || record.ExpiresAt.IsZero() {
		return errors.New("authorized OAuth credential is not rotation-capable")
	}
	if missing := missingScopes(requiredScopes, record.Scopes); len(missing) != 0 {
		return fmt.Errorf("authorized OAuth credential is missing required scopes: %s", strings.Join(missing, ","))
	}
	if validateIdentity == nil {
		return errors.New("OAuth identity validation is required before commit")
	}
	if err := validateIdentity(ctx, record); err != nil {
		return fmt.Errorf("validate authorized OAuth identity: %w", err)
	}
	return store.SaveIfGeneration(ctx, expectedGeneration, record)
}

func ExchangeOAuthAuthorizationCode(
	ctx context.Context,
	client *http.Client,
	clientID, clientSecret, code, callbackState, callbackRedirect string,
	authorization *PKCEAuthorization,
	now time.Time,
) (OAuthTokenRecord, error) {
	if authorization == nil {
		return OAuthTokenRecord{}, errors.New("PKCE authorization state is required")
	}
	if err := authorization.ValidateCallback(callbackState, callbackRedirect); err != nil {
		return OAuthTokenRecord{}, err
	}
	if clientID == "" || code == "" {
		return OAuthTokenRecord{}, errors.New("OAuth client ID and authorization code are required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	form := url.Values{
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {authorization.RedirectURI},
		"code_verifier": {authorization.Verifier},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/oauth.v2.access", strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthTokenRecord{}, errors.New("build Slack OAuth exchange request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return OAuthTokenRecord{}, fmt.Errorf("exchange Slack OAuth code: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return OAuthTokenRecord{}, fmt.Errorf("exchange Slack OAuth code: HTTP %d", response.StatusCode)
	}
	var exchanged slack.OAuthV2Response
	if err := json.NewDecoder(response.Body).Decode(&exchanged); err != nil {
		return OAuthTokenRecord{}, errors.New("decode Slack OAuth exchange response")
	}
	if err := exchanged.Err(); err != nil {
		return OAuthTokenRecord{}, fmt.Errorf("exchange Slack OAuth code: %w", err)
	}
	accessToken := exchanged.AuthedUser.AccessToken
	refreshToken := exchanged.AuthedUser.RefreshToken
	expiresIn := exchanged.AuthedUser.ExpiresIn
	scopes := splitScopes(exchanged.AuthedUser.Scope)
	userID := exchanged.AuthedUser.ID
	if accessToken == "" {
		accessToken = exchanged.AccessToken
		refreshToken = exchanged.RefreshToken
		expiresIn = exchanged.ExpiresIn
		scopes = splitScopes(exchanged.Scope)
	}
	record := OAuthTokenRecord{
		Version: oauthRecordVersion, AccessToken: accessToken, RefreshToken: refreshToken,
		ExpiresAt: now.Add(time.Duration(expiresIn) * time.Second).UTC(),
		TeamID:    exchanged.Team.ID, UserID: userID, Scopes: scopes,
	}
	if err := record.validate(); err != nil {
		return OAuthTokenRecord{}, err
	}
	if record.RefreshToken == "" || expiresIn <= 0 {
		return OAuthTokenRecord{}, errors.New("Slack OAuth exchange did not return rotating credentials")
	}
	return record, nil
}

type OAuthCredentialStatus struct {
	Source             string `json:"source"`
	RotationConfigured bool   `json:"rotation_configured"`
	Store              string `json:"store"`
	State              string `json:"state"`
	Reason             string `json:"reason,omitempty"`
}

func OAuthStatusFromEnvironment() OAuthCredentialStatus {
	status := OAuthCredentialStatus{Source: "not_configured", Store: "not_configured", State: "unavailable"}
	if strings.TrimSpace(getenv("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT")) != "" {
		status.Source = "keychain"
		status.Store = "macos_keychain"
		if getenv("SLACK_MCP_OAUTH_CLIENT_ID") != "" && getenv("SLACK_MCP_OAUTH_CLIENT_SECRET") != "" {
			status.RotationConfigured = true
			status.State = "configured"
		} else {
			status.State = "foundation"
			status.Reason = "OAuth client configuration is absent; authorization and rotation are unavailable"
		}
		return status
	}
	if strings.TrimSpace(getenv("SLACK_MCP_XOXP_TOKEN")) != "" {
		status.Source = "static_user_oauth"
		status.Store = "environment"
		status.State = "live"
		status.Reason = "static non-expiring OAuth compatibility mode; durable rotation is unavailable"
		return status
	}
	if strings.TrimSpace(getenv("SLACK_MCP_XOXB_TOKEN")) != "" {
		status.Source = "static_bot_oauth"
		status.Store = "environment"
		status.State = "live"
		status.Reason = "static non-expiring OAuth compatibility mode; durable rotation is unavailable"
		return status
	}
	return status
}

func loadManagedOAuth(ctx context.Context) (*OAuthTokenManager, OAuthTokenRecord, error) {
	account := strings.TrimSpace(getenv("SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT"))
	if account == "" {
		return nil, OAuthTokenRecord{}, nil
	}
	store, err := NewKeychainCredentialStore(account)
	if err != nil {
		return nil, OAuthTokenRecord{}, err
	}
	var refresher OAuthRefresher
	clientID := strings.TrimSpace(getenv("SLACK_MCP_OAUTH_CLIENT_ID"))
	clientSecret := strings.TrimSpace(getenv("SLACK_MCP_OAUTH_CLIENT_SECRET"))
	if clientID != "" && clientSecret != "" {
		refresher = &SlackOAuthRefresher{ClientID: clientID, ClientSecret: clientSecret}
	}
	manager := NewOAuthTokenManager(
		store,
		refresher,
		withOAuthFileLock(filepath.Join(getCacheDir(), "oauth-refresh.lock")),
	)
	expected, err := store.Load(ctx)
	if err != nil {
		return nil, OAuthTokenRecord{}, err
	}
	manager.WithValidator(func(ctx context.Context, rotated OAuthTokenRecord) error {
		identity, err := slack.New(rotated.AccessToken).AuthTestContext(ctx)
		if err != nil {
			return err
		}
		if expected.TeamID == "" || expected.UserID == "" || identity.TeamID != expected.TeamID || identity.UserID != expected.UserID {
			return errors.New("rotated OAuth identity does not match persisted workspace and user")
		}
		return nil
	})
	record, err := manager.Current(ctx)
	if err != nil {
		return nil, OAuthTokenRecord{}, err
	}
	return manager, record, nil
}

var getenv = os.Getenv
