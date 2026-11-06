package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/korotovsky/slack-mcp-server/pkg/keychain"
	"github.com/korotovsky/slack-mcp-server/pkg/oauthcred"
)

// CheckKeychainAccess reads every Keychain item creds names
// (oauthcred.EnvKeychainAccount, envBrowserKeychainAccount) and discards the
// contents. It returns how many items it read.
//
// A rebuilt binary has a new cdhash, so macOS asks before letting it read
// these items. `make deploy-local` runs the staged binary with
// --check-keychain first, so that prompt is answered before Plug starts the
// binary under its 60s initialize timeout. The read blocks until someone
// answers the prompt; ctx cannot interrupt the cgo call.
func CheckKeychainAccess(ctx context.Context, creds CredentialConfig) (int, error) {
	return checkKeychainAccess(ctx, keychain.Platform(), creds)
}

func checkKeychainAccess(ctx context.Context, store keychain.Store, creds CredentialConfig) (int, error) {
	items := []struct{ label, service, account, envVar string }{
		{"OAuth", oauthcred.KeychainService, creds.OAuthKeychainAccount, oauthcred.EnvKeychainAccount},
		{"browser", browserKeychainService, creds.BrowserKeychainAccount, envBrowserKeychainAccount},
	}
	read := 0
	for _, item := range items {
		if item.account == "" {
			continue
		}
		if _, err := store.Read(ctx, item.service, item.account); err != nil {
			if errors.Is(err, keychain.ErrNotFound) {
				return read, fmt.Errorf("%s credential named by %s is not in the Keychain", item.label, item.envVar)
			}
			return read, fmt.Errorf("read %s credential named by %s: access denied or Keychain unavailable: %w", item.label, item.envVar, err)
		}
		read++
	}
	return read, nil
}
