package main

import (
	"context"
	"fmt"
	"os"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
)

// runKeychainCheck backs --check-keychain. It prints no credential data.
func runKeychainCheck() int {
	read, err := provider.CheckKeychainAccess(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "keychain check failed: %v\n", err)
		return 1
	}
	if read == 0 {
		fmt.Println("keychain check: no Keychain account configured, nothing to read")
		return 0
	}
	fmt.Printf("keychain check: read %d item(s)\n", read)
	return 0
}
