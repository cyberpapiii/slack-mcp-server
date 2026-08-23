package handler

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func filesListFixture() []slack.File {
	return []slack.File{
		{
			ID: "F1", Name: "spec.pdf", Filetype: "pdf", Size: 1024, User: "U1", Created: 1700000000,
			Channels: []string{"C1"},
			Shares:   slack.Share{Public: map[string][]slack.ShareFileInfo{"C1": {{Ts: "1700000000.000100"}}}},
		},
		{
			ID: "F2", Name: "notes.txt", Filetype: "text", Size: 12, User: "U2", Created: 1700000100,
			Channels: []string{"C1", "C2"},
			Shares:   slack.Share{Private: map[string][]slack.ShareFileInfo{"C2": {{Ts: "1700000100.000200"}}}},
		},
		{ID: "F3", Name: "orphan.png", Filetype: "png", Size: 7, User: "U9", Created: 1700000200},
	}
}

func fixtureUserName(id string) string {
	return map[string]string{"U1": "Alice Smith", "U2": "bob"}[id]
}

func fixtureChannelName(id string) string {
	return map[string]string{"C1": "#general", "C2": "#eng"}[id]
}

func TestUnitFilesListCSVHeaderLegendAndCursor(t *testing.T) {
	var rows []fileRow
	for _, f := range filesListFixture() {
		rows = append(rows, fileRowFromSlack(f, "C2", fixtureUserName))
	}

	result, err := marshalFilesToCSV(rows, "files-page-2", fixtureChannelName)
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)

	lines := strings.Split(ResultText(result), "\n")
	assert.Equal(t, "#channels: C1=#general, C2=#eng", lines[0])
	assert.Equal(t, "#next_cursor: files-page-2", lines[1])
	assert.Equal(t, "FileID,Name,Filetype,Size,Created,User,Channel,MsgID", lines[2])
	assert.True(t, strings.HasPrefix(lines[3], "F1,spec.pdf,pdf,1024,"), lines[3])
	assert.True(t, strings.HasSuffix(lines[3], ",Alice Smith,C1,1700000000.000100"), lines[3])
	assert.True(t, strings.HasSuffix(lines[4], ",bob,C2,1700000100.000200"), "filtered channel wins over the first share: %s", lines[4])
	assert.True(t, strings.HasSuffix(lines[5], ",,,"), "no share, no user name: empty User, Channel, MsgID: %s", lines[5])
}

func TestUnitFilesListCSVLastPage(t *testing.T) {
	rows := []fileRow{fileRowFromSlack(filesListFixture()[0], "", fixtureUserName)}

	result, err := marshalFilesToCSV(rows, "", func(string) string { return "" })
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)
	body := ResultText(result)
	assert.True(t, strings.HasPrefix(body, "FileID,Name,Filetype,Size,Created,User,Channel,MsgID\n"), body)
	assert.NotContains(t, body, "#next_cursor")
	assert.NotContains(t, body, "#channels")
	for _, dropped := range []string{"Cursor", "Permalink", "Mimetype", "PrettyType", "Title"} {
		assert.NotContains(t, body, dropped)
	}
}

func TestUnitFileShareFallsBackToFirstConversation(t *testing.T) {
	f := filesListFixture()[1]

	channel, msgID := fileShare(f, "")
	assert.Equal(t, "C1", channel)
	assert.Equal(t, "", msgID, "C1 has no share entry, so no MsgID")

	channel, msgID = fileShare(f, "C2")
	assert.Equal(t, "C2", channel)
	assert.Equal(t, "1700000100.000200", msgID)
}

func TestUnitFileResultPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload fileResultPayload
	}{
		{
			name: "filename with quotes backslash and ANSI escape",
			payload: fileResultPayload{
				FileID:   `F0123"ABC\`,
				Filename: "we\x1bird \"name\"\\path.txt",
				Mimetype: "text/plain",
				Size:     12,
				Encoding: "none",
				Content:  "hello",
			},
		},
		{
			name: "content with NUL and ANSI escapes",
			payload: fileResultPayload{
				FileID:   "F0123ABC",
				Filename: "app.log",
				Mimetype: "text/plain",
				Size:     42,
				Encoding: "none",
				Content:  "\x00start\x1b[31mred\x1b[0m\v\f end ",
			},
		},
		{
			name: "base64 encoding preserved",
			payload: fileResultPayload{
				FileID:   "F0999ZZZ",
				Filename: "blob.bin",
				Mimetype: "application/octet-stream",
				Size:     3,
				Encoding: "base64",
				Content:  "AAEC",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := fallbackJSON(tt.payload)
			assert.True(t, json.Valid([]byte(out)), "output must be valid JSON: %q", out)

			var got fileResultPayload
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			assert.Equal(t, tt.payload, got, "payload must round-trip exactly")
		})
	}
}

func TestUnitFileResultShapes(t *testing.T) {
	keysOf := func(t *testing.T, s string) []string {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(s), &m))
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	t.Run("image metadata emits exactly four keys", func(t *testing.T) {
		out := fallbackJSON(fileMetadataPayload{
			FileID:   "F0123ABC",
			Filename: "pic.png",
			Mimetype: "image/png",
			Size:     7,
		})
		assert.Equal(t, []string{"file_id", "filename", "mimetype", "size"}, keysOf(t, out))
	})

	t.Run("text result emits exactly six keys with encoding none", func(t *testing.T) {
		out := fallbackJSON(fileResultPayload{
			FileID:   "F0123ABC",
			Filename: "notes.txt",
			Mimetype: "text/plain",
			Size:     0,
			Encoding: "none",
			Content:  "",
		})
		assert.Equal(t,
			[]string{"content", "encoding", "file_id", "filename", "mimetype", "size"},
			keysOf(t, out))

		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &m))
		assert.Equal(t, "none", m["encoding"], "encoding must survive even when \"none\"")
		assert.Equal(t, "", m["content"], "content key must be present even when empty")
	})
}

func TestUnitFilesPageCursorRoundTrip(t *testing.T) {
	page, err := filesPage("")
	require.NoError(t, err)
	assert.Equal(t, 1, page, "no cursor starts at page 1")

	page, err = filesPage("3")
	require.NoError(t, err)
	assert.Equal(t, 3, page)

	for _, bad := range []string{"0", "-1", "abc", "dXNlcjpV"} {
		_, err := filesPage(bad)
		var toolErr *ToolError
		require.ErrorAs(t, err, &toolErr, bad)
		assert.Equal(t, "invalid_arguments", toolErr.Code, bad)
	}

	assert.Equal(t, "", nextFilesCursor(nil))
	assert.Equal(t, "", nextFilesCursor(&slack.Paging{Page: 2, Pages: 2}), "last page emits no cursor")
	assert.Equal(t, "", nextFilesCursor(&slack.Paging{Page: 1, Pages: 0}), "empty result set emits no cursor")
	assert.Equal(t, "2", nextFilesCursor(&slack.Paging{Page: 1, Pages: 5}))
}
