package approval

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// TTL is how long a prepared token stays valid.
const TTL = 5 * time.Minute

var (
	ErrInvalid = errors.New("approval token is invalid")
	ErrExpired = errors.New("approval token expired")
	ErrReplay  = errors.New("approval token was already used")
	ErrBinding = errors.New("approval token does not match this action")
)

// Binding is what a token authorizes: who acts, with which tool, on which
// arguments, against which observed Slack state.
type Binding struct {
	teamID, userID, tool string
	arguments, observed  string
}

// NewBinding encodes the arguments and observed state the token binds. A nil
// observed state binds none: the tool does not re-check Slack before executing.
func NewBinding(teamID, userID, tool string, arguments, observed any) (Binding, error) {
	if teamID == "" || userID == "" || tool == "" {
		return Binding{}, ErrBinding
	}
	args, err := json.Marshal(arguments)
	if err != nil {
		return Binding{}, errors.New("encode approval arguments")
	}
	binding := Binding{teamID: teamID, userID: userID, tool: tool, arguments: string(args)}
	if observed != nil {
		state, err := json.Marshal(observed)
		if err != nil {
			return Binding{}, errors.New("encode approval observed state")
		}
		binding.observed = string(state)
	}
	return binding, nil
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
	now     func() time.Time
}

func NewStore() *Store {
	return NewStoreWithClock(time.Now)
}

func NewStoreWithClock(clock func() time.Time) *Store {
	return &Store{records: make(map[string]record), now: clock}
}

// Prepare issues a token for the binding and reports when it expires.
func (s *Store) Prepare(binding Binding) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, errors.New("generate approval token")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expiresAt := s.now().Add(TTL).UTC()

	s.mu.Lock()
	s.records[token] = record{binding: binding, expiresAt: expiresAt}
	s.pruneLocked(s.now())
	s.mu.Unlock()
	return token, expiresAt, nil
}

// Consume spends a token if it was issued for exactly this binding. A token
// checked against a different binding stays unspent.
func (s *Store) Consume(token string, expected Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[token]
	if !ok {
		return ErrInvalid
	}
	if rec.used {
		return ErrReplay
	}
	if !s.now().Before(rec.expiresAt) {
		delete(s.records, token)
		return ErrExpired
	}
	if rec.binding != expected {
		return ErrBinding
	}
	rec.used = true
	s.records[token] = rec
	return nil
}

func (s *Store) pruneLocked(now time.Time) {
	for token, rec := range s.records {
		if !now.Before(rec.expiresAt) {
			delete(s.records, token)
		}
	}
}
