package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultOAuthKeychainService = "slack-mcp-server.oauth"

var ErrCredentialNotFound = errors.New("OAuth credential not found")

// keychainStore is the one seam between the credential stores and the OS
// keychain. platformKeychain returns the implementation for the build target.
type keychainStore interface {
	// Read returns ErrCredentialNotFound when no item exists for the identity.
	Read(ctx context.Context, service, account string) ([]byte, error)
	// Write creates the item or replaces its data in place.
	Write(ctx context.Context, service, account string, data []byte) error
	// Delete returns nil when no item exists for the identity.
	Delete(ctx context.Context, service, account string) error
}

type KeychainCredentialStore struct {
	Service  string
	Account  string
	keychain keychainStore
}

func NewKeychainCredentialStore(account string) (*KeychainCredentialStore, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, errors.New("Keychain account is required")
	}
	return &KeychainCredentialStore{
		Service:  defaultOAuthKeychainService,
		Account:  account,
		keychain: platformKeychain(),
	}, nil
}

func (s *KeychainCredentialStore) Load(ctx context.Context) (OAuthTokenRecord, error) {
	raw, err := s.keychain.Read(ctx, s.Service, s.Account)
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) {
			return OAuthTokenRecord{}, fmt.Errorf("%w: macOS Keychain item does not exist", ErrCredentialNotFound)
		}
		return OAuthTokenRecord{}, errors.New("macOS Keychain lookup failed")
	}
	var record OAuthTokenRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return OAuthTokenRecord{}, errors.New("OAuth credential in macOS Keychain is corrupt")
	}
	if err := record.validate(); err != nil {
		return OAuthTokenRecord{}, err
	}
	return record, nil
}

// SaveIfGeneration writes record only when the stored generation still equals
// expected (or nothing is stored and expected is 0). The load-then-write is not
// atomic against other processes: the caller must hold the credential lock
// (WithOAuthCredentialLock) across the call, as OAuthTokenManager and
// slack-mcp-auth do.
func (s *KeychainCredentialStore) SaveIfGeneration(ctx context.Context, expected uint64, record OAuthTokenRecord) error {
	if err := record.validate(); err != nil {
		return err
	}
	current, err := s.Load(ctx)
	if err != nil {
		if !errors.Is(err, ErrCredentialNotFound) || expected != 0 {
			return err
		}
	} else if current.Generation != expected {
		return ErrCredentialGenerationChanged
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

func (s *KeychainCredentialStore) Delete(ctx context.Context) error {
	if err := s.keychain.Delete(ctx, s.Service, s.Account); err != nil {
		return errors.New("delete OAuth credential from macOS Keychain")
	}
	return nil
}

func withOAuthFileLock(path string) func(context.Context, func() error) error {
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
			if err := waitOAuthLockRetry(ctx); err != nil {
				return err
			}
		}
	}
}

// WithOAuthCredentialLock serializes login, logout, and runtime refresh across
// processes so a newly rotated single-use refresh token is never overwritten.
func WithOAuthCredentialLock(ctx context.Context, fn func() error) error {
	return withOAuthFileLock(filepath.Join(getCacheDir(), "oauth-refresh.lock"))(ctx, fn)
}

var waitOAuthLockRetry = func(ctx context.Context) error {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
