// Package keychain is the one seam between the credential stores and the OS
// keychain. Platform returns the implementation for the build target.
package keychain

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("Keychain item not found")

type Store interface {
	// Read returns ErrNotFound when no item exists for the identity.
	Read(ctx context.Context, service, account string) ([]byte, error)
	// Write creates the item or replaces its data in place.
	Write(ctx context.Context, service, account string, data []byte) error
	// Delete returns nil when no item exists for the identity.
	Delete(ctx context.Context, service, account string) error
}
