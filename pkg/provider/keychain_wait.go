package provider

import (
	"time"

	"go.uber.org/zap"
)

// keychainPromptAfter is how long a Keychain read may take before startup
// says why it is stuck. A trusted read answers in milliseconds.
const keychainPromptAfter = 3 * time.Second

// warnWhileWaiting logs one warning if stop is not called within after.
//
// A Keychain item's partition list pins the exact build (cdhash) that was last
// allowed, so every rebuild of this unsigned-by-Apple binary makes macOS show
// an access prompt. SecItemCopyMatching blocks on that prompt with no timeout
// the context can cancel, and Plug gives up on initialize after 60s without a
// log line. This warning is that log line.
func warnWhileWaiting(logger *zap.Logger, after time.Duration, credential string) (stop func()) {
	timer := time.AfterFunc(after, func() {
		logger.Warn("Still waiting on macOS Keychain; macOS is likely showing an access prompt for this rebuilt binary. Click Always Allow on the Mac to continue",
			zap.String("credential", credential),
			zap.Duration("waited", after))
	})
	return func() { timer.Stop() }
}
