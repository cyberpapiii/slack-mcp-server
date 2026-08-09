package approval

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalid = errors.New("approval token is invalid")
	ErrExpired = errors.New("approval token expired")
	ErrReplay  = errors.New("approval token was already used")
	ErrBinding = errors.New("approval token does not match this action")
)

// Binding is the complete server-observed identity and action authorized by a
// token. Arguments must be the canonical, user-visible representation of the
// pending Slack change.
type Binding struct {
	TeamID        string          `json:"team_id"`
	UserID        string          `json:"user_id"`
	Provider      string          `json:"provider"`
	Tool          string          `json:"tool"`
	Arguments     json.RawMessage `json:"arguments"`
	ObservedState json.RawMessage `json:"observed_state,omitempty"`
}

type Prepared struct {
	Token     string    `json:"approval_token"`
	ExpiresAt time.Time `json:"expires_at"`
	Binding   Binding   `json:"binding"`
}

type record struct {
	binding   Binding
	expiresAt time.Time
	used      bool
}

// Store issues opaque, restart-invalidated, one-use approval tokens. The MCP
// client never receives signing material or mutable server state.
type Store struct {
	mu      sync.Mutex
	records map[string]record
	ttl     time.Duration
	now     func() time.Time
}

// CanonicalJSON provides the byte-stable representation handlers must show to
// the user and bind into a token. Structs preserve explicit field semantics;
// maps are encoded with sorted keys by encoding/json.
func CanonicalJSON(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode canonical approval view")
	}
	return raw, nil
}

func NewStore(ttl time.Duration) *Store {
	return NewStoreWithClock(ttl, time.Now)
}

func NewStoreWithClock(ttl time.Duration, clock func() time.Time) *Store {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if clock == nil {
		clock = time.Now
	}
	return &Store{records: make(map[string]record), ttl: ttl, now: clock}
}

func (s *Store) Prepare(binding Binding) (Prepared, error) {
	if err := validateBinding(binding); err != nil {
		return Prepared{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Prepared{}, errors.New("generate approval token")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expiresAt := s.now().Add(s.ttl).UTC()
	binding = cloneBinding(binding)

	s.mu.Lock()
	s.records[token] = record{binding: binding, expiresAt: expiresAt}
	s.pruneLocked(s.now())
	s.mu.Unlock()
	return Prepared{Token: token, ExpiresAt: expiresAt, Binding: binding}, nil
}

// Consume validates and burns a token atomically. Callers must re-read Slack
// state after this succeeds and compare it with Binding.ObservedState before
// executing the mutation.
func (s *Store) Consume(token string, expected Binding) (Binding, error) {
	if token == "" {
		return Binding{}, ErrInvalid
	}
	if err := validateBinding(expected); err != nil {
		return Binding{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[token]
	if !ok {
		return Binding{}, ErrInvalid
	}
	if rec.used {
		return Binding{}, ErrReplay
	}
	if !s.now().Before(rec.expiresAt) {
		delete(s.records, token)
		return Binding{}, ErrExpired
	}
	if !equalBinding(rec.binding, expected) {
		return Binding{}, ErrBinding
	}
	rec.used = true
	s.records[token] = rec
	return cloneBinding(rec.binding), nil
}

func validateBinding(binding Binding) error {
	if binding.TeamID == "" || binding.UserID == "" || binding.Provider == "" || binding.Tool == "" || !json.Valid(binding.Arguments) {
		return ErrBinding
	}
	if len(binding.ObservedState) != 0 && !json.Valid(binding.ObservedState) {
		return ErrBinding
	}
	return nil
}

func equalBinding(a, b Binding) bool {
	return a.TeamID == b.TeamID && a.UserID == b.UserID && a.Provider == b.Provider && a.Tool == b.Tool &&
		string(a.Arguments) == string(b.Arguments) && string(a.ObservedState) == string(b.ObservedState)
}

func cloneBinding(binding Binding) Binding {
	binding.Arguments = append(json.RawMessage(nil), binding.Arguments...)
	binding.ObservedState = append(json.RawMessage(nil), binding.ObservedState...)
	return binding
}

func (s *Store) pruneLocked(now time.Time) {
	for token, rec := range s.records {
		if !now.Before(rec.expiresAt) {
			delete(s.records, token)
		}
	}
}
