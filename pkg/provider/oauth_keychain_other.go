//go:build !darwin || !cgo

package provider

import (
	"context"
	"errors"
)

var errKeychainUnavailable = errors.New("macOS Keychain requires Darwin with cgo enabled")

// unavailableKeychain stands in where Security.framework cannot be linked so
// the credential stores still construct and fail at the first access.
type unavailableKeychain struct{}

func platformKeychain() keychainStore { return unavailableKeychain{} }

func (unavailableKeychain) Read(context.Context, string, string) ([]byte, error) {
	return nil, errKeychainUnavailable
}

func (unavailableKeychain) Write(context.Context, string, string, []byte) error {
	return errKeychainUnavailable
}

func (unavailableKeychain) Delete(context.Context, string, string) error {
	return errKeychainUnavailable
}
