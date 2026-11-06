// Package cachedir names the per-user directory the server's caches and the
// OAuth credential lock share. slack-mcp-auth and slack-mcp-server must agree
// on it, or a login could overwrite a refresh token the server just rotated.
package cachedir

import (
	"os"
	"path/filepath"
)

// Dir returns the per-user cache directory, creating it. When that fails it
// returns "." with the error, so the caller can say why it fell back to the
// working directory.
func Dir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ".", err
	}
	dir := filepath.Join(cacheDir, "slack-mcp-server")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ".", err
	}
	return dir, nil
}
