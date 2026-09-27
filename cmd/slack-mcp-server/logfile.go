package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// maxLogFileBytes is the size past which startup moves the log to <path>.1.
// There is no rotation while the process runs; Plug restarts the server often
// enough that one generation checked at startup bounds the file.
const maxLogFileBytes = 10 << 20

// logFilePath returns SLACK_MCP_LOG_FILE, the opt-in log destination. Plug
// sends a stdio server's stderr to /dev/null, so without it the server's logs
// reach nobody.
func logFilePath() (string, error) {
	path := os.Getenv("SLACK_MCP_LOG_FILE")
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("SLACK_MCP_LOG_FILE must be an absolute path, got %q", path)
	}
	if err := prepareLogFile(path, maxLogFileBytes); err != nil {
		return "", fmt.Errorf("SLACK_MCP_LOG_FILE: %w", err)
	}
	return path, nil
}

// prepareLogFile creates path owner-only (logs can carry message text when
// SLACK_MCP_LOG_PARAMS=debug), and first moves it to path.1 once it exceeds
// maxBytes. zap then opens it for append.
func prepareLogFile(path string, maxBytes int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}
