package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitBrowserKeychainStoreKeepsSecretsOutOfArguments(t *testing.T) {
	store, err := NewBrowserCredentialStore("workspace-user")
	require.NoError(t, err)
	var stdin []byte
	var args []string
	store.run = func(_ context.Context, input []byte, name string, commandArgs ...string) ([]byte, error) {
		assert.Equal(t, "security", name)
		stdin = append([]byte(nil), input...)
		args = append([]string(nil), commandArgs...)
		return nil, nil
	}

	require.NoError(t, store.Save(context.Background(), BrowserTokenRecord{
		Version: 1, XOXC: "sentinel-xoxc", XOXD: "sentinel-xoxd", TeamID: "T1", UserID: "U1",
	}))
	assert.Contains(t, string(stdin), "sentinel-xoxc")
	assert.NotContains(t, strings.Join(args, " "), "sentinel")
	assert.Equal(t, "-w", args[len(args)-1])
}
