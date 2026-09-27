package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestUnitWarnWhileWaitingLogsAStalledKeychainRead(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)

	stop := warnWhileWaiting(zap.New(core), 5*time.Millisecond, "oauth")
	time.Sleep(100 * time.Millisecond)
	stop()

	entries := logs.All()
	if assert.Len(t, entries, 1) {
		assert.Contains(t, entries[0].Message, "Keychain")
		assert.Contains(t, entries[0].Message, "Always Allow")
		assert.Equal(t, "oauth", entries[0].ContextMap()["credential"])
	}
}

func TestUnitWarnWhileWaitingStaysQuietForAFastRead(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)

	stop := warnWhileWaiting(zap.New(core), 50*time.Millisecond, "oauth")
	stop()
	time.Sleep(100 * time.Millisecond)

	assert.Zero(t, logs.Len())
}
