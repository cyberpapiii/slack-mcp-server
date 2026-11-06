package oauthcred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/cachedir"
	"github.com/korotovsky/slack-mcp-server/pkg/keychain"
)

// KeychainService is the Keychain service name the OAuth credential lives
// under. Existing logins depend on it.
const KeychainService = "slack-mcp-server.oauth"

var ErrNotFound = errors.New("OAuth credential not found")

type KeychainStore struct {
	Service  string
	Account  string
	keychain keychain.Store
}

func NewKeychainStore(account string) (*KeychainStore, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, errors.New("Keychain account is required")
	}
	return &KeychainStore{
		Service:  KeychainService,
		Account:  account,
		keychain: keychain.Platform(),
	}, nil
}

func (s *KeychainStore) Load(ctx context.Context) (Record, error) {
	raw, err := s.keychain.Read(ctx, s.Service, s.Account)
	if err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			return Record{}, fmt.Errorf("%w: macOS Keychain item does not exist", ErrNotFound)
		}
		return Record{}, errors.New("macOS Keychain lookup failed")
	}
	var record Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return Record{}, errors.New("OAuth credential in macOS Keychain is corrupt")
	}
	if err := record.validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

// SaveIfGeneration writes record only when the stored generation still equals
// expected (or nothing is stored and expected is 0). The load-then-write is not
// atomic against other processes: the caller must hold the credential lock
// (WithLock) across the call, as Manager and slack-mcp-auth do.
func (s *KeychainStore) SaveIfGeneration(ctx context.Context, expected uint64, record Record) error {
	if err := record.validate(); err != nil {
		return err
	}
	current, err := s.Load(ctx)
	if err != nil {
		if !errors.Is(err, ErrNotFound) || expected != 0 {
			return err
		}
	} else if current.Generation != expected {
		return ErrGenerationChanged
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return errors.New("encode OAuth credential")
	}
	// Keychain errors are replaced wholesale because they may carry item data.
	if err := s.keychain.Write(ctx, s.Service, s.Account, raw); err != nil {
		return errors.New("save OAuth credential to macOS Keychain")
	}
	return nil
}

func (s *KeychainStore) Delete(ctx context.Context) error {
	if err := s.keychain.Delete(ctx, s.Service, s.Account); err != nil {
		return errors.New("delete OAuth credential from macOS Keychain")
	}
	return nil
}

func fileLock(path string) func(context.Context, func() error) error {
	return func(ctx context.Context, fn func() error) error {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return fmt.Errorf("create OAuth lock directory: %w", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return fmt.Errorf("open OAuth lock: %w", err)
		}
		defer file.Close()

		for {
			if err := tryLockFile(file); err == nil {
				defer unlockFile(file)
				return fn()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			// Blocking flock is interruptible only by signals. Retry nonblocking
			// through context-aware scheduler instead.
			if err := waitLockRetry(ctx); err != nil {
				return err
			}
		}
	}
}

// WithLock serializes login, logout, and runtime refresh across processes so
// a newly rotated single-use refresh token is never overwritten.
func WithLock(ctx context.Context, fn func() error) error {
	dir, _ := cachedir.Dir()
	return fileLock(filepath.Join(dir, "oauth-refresh.lock"))(ctx, fn)
}

var waitLockRetry = func(ctx context.Context) error {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
