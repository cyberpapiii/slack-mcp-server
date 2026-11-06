// Package keychaintest provides a map-backed keychain.Store for tests.
package keychaintest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/korotovsky/slack-mcp-server/pkg/keychain"
)

// Fake is a map-backed keychain.Store. Error fields, when set, are returned by
// the matching method instead of touching the map.
type Fake struct {
	Items     map[string][]byte
	ReadErr   error
	WriteErr  error
	DeleteErr error
	Writes    int
	Deletes   int
}

func New() *Fake {
	return &Fake{Items: map[string][]byte{}}
}

func Key(service, account string) string { return service + "\x00" + account }

func (k *Fake) Read(_ context.Context, service, account string) ([]byte, error) {
	if k.ReadErr != nil {
		return nil, k.ReadErr
	}
	data, ok := k.Items[Key(service, account)]
	if !ok {
		return nil, keychain.ErrNotFound
	}
	return append([]byte(nil), data...), nil
}

func (k *Fake) Write(_ context.Context, service, account string, data []byte) error {
	if k.WriteErr != nil {
		return k.WriteErr
	}
	k.Writes++
	k.Items[Key(service, account)] = append([]byte(nil), data...)
	return nil
}

func (k *Fake) Delete(_ context.Context, service, account string) error {
	if k.DeleteErr != nil {
		return k.DeleteErr
	}
	k.Deletes++
	delete(k.Items, Key(service, account))
	return nil
}

// Put stores record as JSON under service and account.
func (k *Fake) Put(t *testing.T, service, account string, record any) {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	k.Items[Key(service, account)] = raw
}
