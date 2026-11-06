package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/zap"
)

const defaultCacheTTL = 24 * time.Hour
const defaultMinRefreshInterval = 30 * time.Second

// Atomic rename via CreateTemp; unpredictable name avoids symlink races.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".cache_*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("setting file permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

func cachePath(cacheDir, teamID, filename string) string {
	if teamID != "" {
		return filepath.Join(cacheDir, teamID+"_"+filename)
	}
	return filepath.Join(cacheDir, filename)
}

// Go duration or integer seconds; empty/negative/unparseable → defaultVal.
func parseEnvDuration(envKey string, defaultVal time.Duration) time.Duration {
	raw := os.Getenv(envKey)
	if raw == "" {
		return defaultVal
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			return defaultVal
		}
		return d
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if secs < 0 {
			return defaultVal
		}
		return time.Duration(secs) * time.Second
	}
	return defaultVal
}

func getCacheTTL() time.Duration {
	return parseEnvDuration("SLACK_MCP_CACHE_TTL", defaultCacheTTL)
}

func getMinRefreshInterval() time.Duration {
	return parseEnvDuration("SLACK_MCP_MIN_REFRESH_INTERVAL", defaultMinRefreshInterval)
}

// loadCacheFile reads a JSON list from path. ok is false when the file is
// missing, corrupt or empty, so the caller refetches; expired reports that the
// file is older than ttl and should be served stale while a refresh runs.
func loadCacheFile[T any](path string, ttl time.Duration, logger *zap.Logger) (items []T, expired bool, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, false
	}
	if err := json.Unmarshal(data, &items); err != nil {
		logger.Warn("Failed to unmarshal cache file, will refetch",
			zap.String("cache_file", path),
			zap.Error(err))
		return nil, false, false
	}
	if len(items) == 0 {
		logger.Warn("Cache file contains zero entries, treating as cache miss",
			zap.String("cache_file", path))
		return nil, false, false
	}
	if expired, age := cacheExpired(path, ttl); expired {
		logger.Info("Serving stale cache, background refresh starting",
			zap.Duration("cache_age", age),
			zap.Duration("ttl", ttl),
			zap.Int("count", len(items)),
			zap.String("cache_file", path))
		return items, true, true
	}
	logger.Info("Loaded cache file",
		zap.Int("count", len(items)),
		zap.String("cache_file", path))
	return items, false, true
}

// cacheExpired reports whether the cache file at path is older than ttl, and
// its age. The file is rewritten on every successful fetch, so its mtime is
// the time of the last good fetch. A missing file is expired; a ttl of 0 never
// expires.
func cacheExpired(path string, ttl time.Duration) (expired bool, age time.Duration) {
	if ttl <= 0 {
		return false, 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return true, 0
	}
	age = time.Since(info.ModTime())
	return age > ttl, age
}

// writeCacheFile persists items as compact JSON. Failures are logged rather
// than returned because the in-memory snapshot is already live.
func writeCacheFile[T any](path string, items []T, logger *zap.Logger) {
	data, err := json.Marshal(items)
	if err != nil {
		logger.Error("Failed to marshal cache", zap.String("cache_file", path), zap.Error(err))
		return
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		logger.Error("Failed to write cache file",
			zap.String("cache_file", path),
			zap.Error(err))
		return
	}
	logger.Debug("Wrote cache file",
		zap.Int("count", len(items)),
		zap.String("cache_file", path))
}
