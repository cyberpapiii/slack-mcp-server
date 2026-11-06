// Package oauthcred holds the rotating Slack user OAuth credential: its record,
// its Keychain store and lock, the refresh that rotates it, and the PKCE login
// that creates it.
package oauthcred

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/slackhttp"
	"github.com/slack-go/slack"
)

const RecordVersion = 1

var ErrRefreshUnavailable = errors.New("OAuth refresh is unavailable")

// Record is persisted only through Store. Callers must
// never log or return this value.
type Record struct {
	Version           int       `json:"version"`
	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token,omitempty"`
	ExpiresAt         time.Time `json:"expires_at,omitempty"`
	Generation        uint64    `json:"generation"`
	TeamID            string    `json:"team_id,omitempty"`
	UserID            string    `json:"user_id,omitempty"`
	Scopes            []string  `json:"scopes,omitempty"`
	PendingValidation bool      `json:"pending_validation,omitempty"`
	RequiredScopes    []string  `json:"required_scopes,omitempty"`
}

func (Record) String() string { return "[REDACTED OAuth credential]" }

func (r Record) validate() error {
	if r.Version != RecordVersion {
		return fmt.Errorf("unsupported OAuth credential version %d", r.Version)
	}
	if strings.TrimSpace(r.AccessToken) == "" {
		return errors.New("OAuth credential has no access token")
	}
	return nil
}

func (r Record) expiring(now time.Time, earlyRefresh time.Duration) bool {
	return !r.ExpiresAt.IsZero() && !r.ExpiresAt.After(now.Add(earlyRefresh))
}

type Store interface {
	Load(context.Context) (Record, error)
	SaveIfGeneration(context.Context, uint64, Record) error
	Delete(context.Context) error
}

var ErrGenerationChanged = errors.New("OAuth credential generation changed")

type RefreshResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
}

type Refresher interface {
	Refresh(context.Context, string) (RefreshResult, error)
}

// SlackRefresher rotates the credential through oauth.v2.access. HTTPClient
// is required; build it with transport.ProvideHTTPClient so the call honors
// the proxy, CA and GovSlack settings.
type SlackRefresher struct {
	HTTPClient   *http.Client
	ClientID     string
	ClientSecret string
	now          func() time.Time
}

func (r *SlackRefresher) Refresh(ctx context.Context, refreshToken string) (RefreshResult, error) {
	if r.ClientID == "" {
		return RefreshResult{}, ErrRefreshUnavailable
	}
	form := url.Values{
		"client_id":     {r.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if r.ClientSecret != "" {
		form.Set("client_secret", r.ClientSecret)
	}
	response, err := oauthAccess(ctx, r.HTTPClient, form)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("refresh Slack OAuth token: %w", err)
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
	if accessToken == "" || rotatedRefresh == "" || expiresIn <= 0 {
		return RefreshResult{}, errors.New("refresh Slack OAuth token: incomplete rotated credential")
	}
	return RefreshResult{
		AccessToken: accessToken, RefreshToken: rotatedRefresh,
		ExpiresAt: now().Add(time.Duration(expiresIn) * time.Second).UTC(),
		Scopes:    splitScopes(scope),
	}, nil
}

func splitScopes(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
	return append([]string(nil), fields...)
}

type Manager struct {
	store        Store
	refresher    Refresher
	lock         func(context.Context, func() error) error
	now          func() time.Time
	earlyRefresh time.Duration
	validate     func(context.Context, Record) error
	pending      *Record
	mu           sync.Mutex
}

func (m *Manager) WithValidator(validate func(context.Context, Record) error) *Manager {
	m.validate = validate
	return m
}

// oauthRotateTimeout bounds one refresh call plus the save of the token it
// returns. It is independent of the caller's context; see Current.
const oauthRotateTimeout = 30 * time.Second

func NewManager(store Store, refresher Refresher, lock func(context.Context, func() error) error) *Manager {
	if lock == nil {
		lock = func(_ context.Context, fn func() error) error { return fn() }
	}
	return &Manager{
		store: store, refresher: refresher, lock: lock,
		now: time.Now, earlyRefresh: 5 * time.Minute,
	}
}

// Current serializes refresh in-process and across processes. It reloads after
// taking the lock so a waiter observes the winner's rotated refresh token.
func (m *Manager) Current(ctx context.Context) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var current Record
	err := m.lock(ctx, func() error {
		record, err := m.store.Load(ctx)
		if err != nil {
			return err
		}
		if err := record.validate(); err != nil {
			return err
		}
		if m.pending != nil {
			pending := *m.pending
			pending.Scopes = append([]string(nil), m.pending.Scopes...)
			pending.RequiredScopes = append([]string(nil), m.pending.RequiredScopes...)
			if err := m.store.SaveIfGeneration(ctx, pending.Generation-1, pending); err != nil {
				if errors.Is(err, ErrGenerationChanged) {
					m.pending = nil
				}
				return fmt.Errorf("persist pending rotated OAuth credential: %w", err)
			}
			m.pending = nil
			record = pending
		}
		if record.PendingValidation {
			if record, err = m.commitValidated(ctx, record); err != nil {
				return err
			}
		}
		if !record.expiring(m.now(), m.earlyRefresh) {
			current = record
			return nil
		}
		if m.refresher == nil || record.RefreshToken == "" {
			return ErrRefreshUnavailable
		}
		// Slack may consume the old refresh token as soon as it is sent, so
		// the call and the save of its replacement run to completion even if
		// ctx ends: a shutdown or the caller's deadline must not leave the
		// only valid refresh token in memory.
		rotateCtx, cancelRotate := context.WithTimeout(context.WithoutCancel(ctx), oauthRotateTimeout)
		defer cancelRotate()
		rotated, err := m.refresher.Refresh(rotateCtx, record.RefreshToken)
		if err != nil {
			return fmt.Errorf("refresh OAuth credential: %w", err)
		}
		if rotated.AccessToken == "" || rotated.RefreshToken == "" || rotated.ExpiresAt.IsZero() {
			return errors.New("refresh OAuth credential: incomplete rotated credential")
		}
		requiredScopes := append([]string(nil), record.Scopes...)
		record.AccessToken = rotated.AccessToken
		record.RefreshToken = rotated.RefreshToken
		record.ExpiresAt = rotated.ExpiresAt.UTC()
		record.Scopes = append([]string(nil), rotated.Scopes...)
		record.Generation++
		record.PendingValidation = true
		record.RequiredScopes = requiredScopes
		pending := record
		pending.Scopes = append([]string(nil), record.Scopes...)
		pending.RequiredScopes = append([]string(nil), record.RequiredScopes...)
		m.pending = &pending
		// Slack refresh tokens rotate on use. Persist the replacement before
		// checks that may fail so the consumed token is never left durable.
		if err := m.store.SaveIfGeneration(rotateCtx, record.Generation-1, record); err != nil {
			return fmt.Errorf("persist rotated OAuth credential: %w", err)
		}
		m.pending = nil
		current, err = m.commitValidated(ctx, record)
		return err
	})
	return current, err
}

// commitValidated clears a rotated record's pending flag once it still has
// every scope it had before rotation and passes the identity validator.
func (m *Manager) commitValidated(ctx context.Context, record Record) (Record, error) {
	if missing := missingScopes(record.RequiredScopes, record.Scopes); len(missing) != 0 {
		return Record{}, fmt.Errorf("validate rotated OAuth credential: lost required scopes: %s", strings.Join(missing, ","))
	}
	if m.validate != nil {
		if err := m.validate(ctx, record); err != nil {
			return Record{}, fmt.Errorf("validate rotated OAuth credential: %w", err)
		}
	}
	record.PendingValidation = false
	record.RequiredScopes = nil
	if err := m.store.SaveIfGeneration(ctx, record.Generation, record); err != nil {
		return Record{}, fmt.Errorf("commit validated OAuth credential: %w", err)
	}
	return record, nil
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

type PKCE struct {
	State       string
	Verifier    string
	Challenge   string
	RedirectURI string
	ExpiresAt   time.Time
	used        bool
	now         func() time.Time
	mu          sync.Mutex
}

func NewPKCE(redirectURI string) (*PKCE, error) {
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
	return &PKCE{
		State: state, Verifier: verifier,
		Challenge:   base64.RawURLEncoding.EncodeToString(digest[:]),
		RedirectURI: u.String(),
		ExpiresAt:   time.Now().Add(10 * time.Minute),
		now:         time.Now,
	}, nil
}

func (p *PKCE) ValidateCallback(state, redirectURI string) error {
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

// CommitAuthorized is final authorization-code boundary. Caller
// exchanges code using Slack app configuration, then supplies exact auth.test
// identity validation before any credential becomes durable.
func CommitAuthorized(
	ctx context.Context,
	store Store,
	expectedGeneration uint64,
	record Record,
	requiredScopes []string,
	validateIdentity func(context.Context, Record) error,
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

// ExchangeCode trades the callback's authorization code for a credential.
// client is required, as for SlackRefresher.
func ExchangeCode(
	ctx context.Context,
	client *http.Client,
	clientID, clientSecret, code, callbackState, callbackRedirect string,
	authorization *PKCE,
	now time.Time,
) (Record, error) {
	if authorization == nil {
		return Record{}, errors.New("PKCE authorization state is required")
	}
	if err := authorization.ValidateCallback(callbackState, callbackRedirect); err != nil {
		return Record{}, err
	}
	if clientID == "" || code == "" {
		return Record{}, errors.New("OAuth client ID and authorization code are required")
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
	exchanged, err := oauthAccess(ctx, client, form)
	if err != nil {
		return Record{}, fmt.Errorf("exchange Slack OAuth code: %w", err)
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
	record := Record{
		Version: RecordVersion, AccessToken: accessToken, RefreshToken: refreshToken,
		ExpiresAt: now.Add(time.Duration(expiresIn) * time.Second).UTC(),
		TeamID:    exchanged.Team.ID, UserID: userID, Scopes: scopes,
	}
	if err := record.validate(); err != nil {
		return Record{}, err
	}
	if record.RefreshToken == "" || expiresIn <= 0 {
		return Record{}, errors.New("Slack OAuth exchange did not return rotating credentials")
	}
	return record, nil
}

// oauthAccess posts to oauth.v2.access, which both the code exchange and the
// refresh use.
func oauthAccess(ctx context.Context, client slackhttp.Doer, form url.Values) (slack.OAuthV2Response, error) {
	var response slack.OAuthV2Response
	err := slackhttp.Call(ctx, client, slackhttp.Request{URL: slackhttp.SlackAPIBase() + "oauth.v2.access", Form: form}, &response)
	return response, err
}
