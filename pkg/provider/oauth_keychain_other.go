//go:build !darwin || !cgo

package provider

import (
	"context"
	"errors"
)

func platformKeychainCommandRunner() commandRunner {
	return func(context.Context, []byte, string, ...string) ([]byte, error) {
		return nil, errors.New("macOS Keychain requires Darwin with cgo enabled")
	}
}
