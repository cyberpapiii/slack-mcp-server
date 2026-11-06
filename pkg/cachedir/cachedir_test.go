package cachedir

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirIsCreatedUnderTheAppName(t *testing.T) {
	dir, err := Dir()
	require.NoError(t, err)
	assert.Contains(t, dir, "slack-mcp-server")

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}
