package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitNewLoggerWritesToLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "server.log")
	t.Setenv("SLACK_MCP_LOG_FILE", path)
	t.Setenv("SLACK_MCP_LOG_FORMAT", "json")

	logger, err := newLogger("stdio")
	require.NoError(t, err)
	logger.Info("hello from the test")
	logger.Warn("a warning")
	require.NoError(t, logger.Sync())

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(got), "hello from the test")
	assert.Contains(t, string(got), "a warning")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "log file may hold message text, keep it owner-only")
}

func TestUnitNewLoggerRejectsRelativeLogFile(t *testing.T) {
	t.Setenv("SLACK_MCP_LOG_FILE", "relative/server.log")
	_, err := newLogger("stdio")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SLACK_MCP_LOG_FILE")
}

func TestUnitPrepareLogFileRotatesOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o600))

	require.NoError(t, prepareLogFile(path, 50))

	old, err := os.ReadFile(path + ".1")
	require.NoError(t, err)
	assert.Len(t, old, 100)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Zero(t, info.Size())
}

func TestUnitPrepareLogFileKeepsSmallFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	require.NoError(t, os.WriteFile(path, []byte("keep"), 0o600))

	require.NoError(t, prepareLogFile(path, 50))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(got))
	_, err = os.Stat(path + ".1")
	assert.True(t, os.IsNotExist(err))
}
