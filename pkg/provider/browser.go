package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

const browserKeychainService = "slack-mcp-server.browser"

type BrowserTokenRecord struct {
	Version int    `json:"version"`
	XOXC    string `json:"xoxc"`
	XOXD    string `json:"xoxd"`
	TeamID  string `json:"team_id"`
	UserID  string `json:"user_id"`
}

func (BrowserTokenRecord) String() string { return "[REDACTED browser credential]" }

type BrowserCredentialStore struct {
	account string
	run     commandRunner
}

func NewBrowserCredentialStore(account string) (*BrowserCredentialStore, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, errors.New("browser Keychain account is required")
	}
	return &BrowserCredentialStore{account: account, run: keychainCommandRunner()}, nil
}

func (s *BrowserCredentialStore) Load(ctx context.Context) (BrowserTokenRecord, error) {
	raw, err := s.run(ctx, nil, "security", "find-generic-password", "-s", browserKeychainService, "-a", s.account, "-w")
	if err != nil {
		return BrowserTokenRecord{}, errors.New("browser credential not found in macOS Keychain")
	}
	var record BrowserTokenRecord
	if json.Unmarshal(raw, &record) != nil || record.Version != 1 || record.XOXC == "" || record.XOXD == "" {
		return BrowserTokenRecord{}, errors.New("browser credential in macOS Keychain is corrupt")
	}
	return record, nil
}

func (s *BrowserCredentialStore) Save(ctx context.Context, record BrowserTokenRecord) error {
	if record.Version != 1 || record.XOXC == "" || record.XOXD == "" {
		return errors.New("invalid browser credential")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return errors.New("encode browser credential")
	}
	if _, err := s.run(ctx, raw, "security", "add-generic-password", "-U", "-s", browserKeychainService, "-a", s.account, "-w"); err != nil {
		return errors.New("save browser credential to macOS Keychain")
	}
	return nil
}
