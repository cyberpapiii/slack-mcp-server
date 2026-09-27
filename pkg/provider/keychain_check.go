package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// CheckKeychainAccess reads every Keychain item the environment names
// (SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT, SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT) and
// discards the contents. It returns how many items it read.
//
// A rebuilt binary has a new cdhash, so macOS asks before letting it read
// these items. `make deploy-local` runs the staged binary with
// --check-keychain first, so that prompt is answered before Plug starts the
// binary under its 60s initialize timeout. The read blocks until someone
// answers the prompt; ctx cannot interrupt the cgo call.
func CheckKeychainAccess(ctx context.Context) (int, error) {
	return checkKeychainAccess(ctx, platformKeychain())
}

func checkKeychainAccess(ctx context.Context, keychain keychainStore) (int, error) {
	items := []struct{ label, service, envVar string }{
		{"OAuth", defaultOAuthKeychainService, "SLACK_MCP_OAUTH_KEYCHAIN_ACCOUNT"},
		{"browser", browserKeychainService, "SLACK_MCP_BROWSER_KEYCHAIN_ACCOUNT"},
	}
	read := 0
	for _, item := range items {
		account := strings.TrimSpace(getenv(item.envVar))
		if account == "" {
			continue
		}
		if _, err := keychain.Read(ctx, item.service, account); err != nil {
			if errors.Is(err, ErrCredentialNotFound) {
				return read, fmt.Errorf("%s credential named by %s is not in the Keychain", item.label, item.envVar)
			}
			return read, fmt.Errorf("read %s credential named by %s: access denied or Keychain unavailable", item.label, item.envVar)
		}
		read++
	}
	return read, nil
}
