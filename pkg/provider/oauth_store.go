package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const defaultOAuthKeychainService = "slack-mcp-server.oauth"

var ErrCredentialNotFound = errors.New("OAuth credential not found")

type commandRunner func(context.Context, []byte, string, ...string) ([]byte, error)

type KeychainCredentialStore struct {
	Service string
	Account string
	run     commandRunner
}

func NewKeychainCredentialStore(account string) (*KeychainCredentialStore, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, errors.New("Keychain account is required")
	}
	return &KeychainCredentialStore{
		Service: defaultOAuthKeychainService,
		Account: account,
		run:     keychainCommandRunner(),
	}, nil
}

func keychainCommandRunner() commandRunner {
	return func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(stdin) != 0 {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		return cmd.Output()
	}
}

func (s *KeychainCredentialStore) Load(ctx context.Context) (OAuthTokenRecord, error) {
	raw, err := s.run(ctx, nil, "security", "find-generic-password", "-s", s.Service, "-a", s.Account, "-w")
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) || isKeychainItemNotFound(err) {
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

func isKeychainItemNotFound(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 44
}

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
	// security updates one generic-password item atomically. Do not include
	// command output in returned errors because it can contain credential data.
	if _, err := s.run(ctx, raw, "security", "add-generic-password", "-U", "-s", s.Service, "-a", s.Account, "-w"); err != nil {
		return errors.New("save OAuth credential to macOS Keychain")
	}
	return nil
}

func (s *KeychainCredentialStore) Delete(ctx context.Context) error {
	if _, err := s.run(ctx, nil, "security", "delete-generic-password", "-s", s.Service, "-a", s.Account); err != nil {
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
			if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
				defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
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
