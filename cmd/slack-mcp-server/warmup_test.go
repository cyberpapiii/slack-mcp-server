package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUnitWarmupNextDelay(t *testing.T) {
	assert.Equal(t, 30*time.Second, warmupNextDelay(2))
	assert.Equal(t, 30*time.Second, warmupNextDelay(warmupMaxAttempts))
	assert.Equal(t, 5*time.Minute, warmupNextDelay(warmupMaxAttempts+1))
	assert.Equal(t, 5*time.Minute, warmupNextDelay(100))
}
