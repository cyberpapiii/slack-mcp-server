package handler

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitParseChoicesRejectsUnknownValues(t *testing.T) {
	allowed := []string{"im", "mpim", "public_channel"}
	got, err := parseChoices("channel_types", " im, public_channel,im,", allowed, []string{"mpim"})
	require.NoError(t, err)
	assert.Equal(t, []string{"im", "public_channel"}, got)

	got, err = parseChoices("channel_types", "", allowed, []string{"mpim"})
	require.NoError(t, err)
	assert.Equal(t, []string{"mpim"}, got)

	_, err = parseChoices("channel_types", "im,channels", allowed, nil)
	var typed *ToolError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "invalid_arguments", typed.Code)
	assert.Contains(t, typed.Message, `"channels"`)

	one, err := parseChoice("sort", "", []string{"popularity"}, "popularity")
	require.NoError(t, err)
	assert.Equal(t, "popularity", one)
	_, err = parseChoice("sort", "name", []string{"popularity"}, "popularity")
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, "invalid_arguments", typed.Code)
}

func TestUnitSignpostFileParam(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		signposts bool
	}{
		{"plain text message passes", map[string]any{"channel_id": "C1", "text": "hi"}, false},
		{"payload is not file-shaped", map[string]any{"channel_id": "C1", "payload": "hi"}, false},
		{"blocks pass", map[string]any{"channel_id": "C1", "blocks": "[]"}, false},
		{"unknown non-file param is left alone", map[string]any{"channel_id": "C1", "reply_broadcast": true}, false},
		{"file_path", map[string]any{"channel_id": "C1", "file_path": "/tmp/a.png"}, true},
		{"files", map[string]any{"channel_id": "C1", "files": []any{"F1"}}, true},
		{"attachments", map[string]any{"channel_id": "C1", "attachments": "[]"}, true},
		{"attachment_ids", map[string]any{"channel_id": "C1", "attachment_ids": "F1"}, true},
		{"upload", map[string]any{"channel_id": "C1", "upload": "x"}, true},
		{"image_url", map[string]any{"channel_id": "C1", "image_url": "http://x"}, true},
		{"screenshot", map[string]any{"channel_id": "C1", "screenshot": "x"}, true},
		{"mixed case is matched", map[string]any{"channel_id": "C1", "FilePath": "/tmp/a.png"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tt.args

			err := signpostFileParam(req)
			if !tt.signposts {
				assert.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, "invalid_arguments", err.Code)
			assert.Contains(t, err.Message, "files_upload", "the error must name the tool, it is the only pointer a deferred-tool client gets")
			assert.Contains(t, err.Message, "initial_comment")
		})
	}
}
