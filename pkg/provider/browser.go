package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/keychain"
)

const browserKeychainService = "slack-mcp-server.browser"

// BrowserTokenRecord is the browser session item in the Keychain. Items
// written by earlier versions also carry team_id and user_id; decoding
// ignores them.
type BrowserTokenRecord struct {
	Version int    `json:"version"`
	XOXC    string `json:"xoxc"`
	XOXD    string `json:"xoxd"`
}

func (BrowserTokenRecord) String() string { return "[REDACTED browser credential]" }

type BrowserCredentialStore struct {
	account  string
	keychain keychain.Store
}

func NewBrowserCredentialStore(account string) (*BrowserCredentialStore, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, errors.New("browser Keychain account is required")
	}
	return &BrowserCredentialStore{account: account, keychain: keychain.Platform()}, nil
}

func (s *BrowserCredentialStore) Load(ctx context.Context) (BrowserTokenRecord, error) {
	raw, err := s.keychain.Read(ctx, browserKeychainService, s.account)
	if err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			return BrowserTokenRecord{}, errors.New("browser credential not found in macOS Keychain")
		}
		// Keychain errors are replaced wholesale because they may carry item data.
		return BrowserTokenRecord{}, errors.New("browser credential lookup in macOS Keychain failed: access denied or Keychain unavailable")
	}
	var record BrowserTokenRecord
	if json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.XOXC == "" || record.XOXD == "" {
		return BrowserTokenRecord{}, errors.New("browser credential in macOS Keychain is corrupt")
	}
	return record, nil
}
