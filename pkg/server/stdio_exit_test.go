package server

import (
	"context"
	"io"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// mcp-go's ServeStdio cancels its context on SIGTERM/SIGINT, and Listen then
// returns context.Canceled. That is Plug stopping the server, not a failure:
// treating it as one exits 1 and skips the provider's deferred Close.
func TestUnitStdioExitTreatsSignalShutdownAsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	err := server.NewStdioServer(server.NewMCPServer("t", "0")).Listen(ctx, pr, io.Discard)
	assert.ErrorIs(t, err, context.Canceled, "precondition: a signalled Listen returns context.Canceled")

	assert.NoError(t, stdioExitErr(zap.NewNop(), err))
}

func TestUnitStdioExitKeepsRealErrors(t *testing.T) {
	assert.Error(t, stdioExitErr(zap.NewNop(), io.ErrUnexpectedEOF))
	assert.NoError(t, stdioExitErr(zap.NewNop(), nil))
}
