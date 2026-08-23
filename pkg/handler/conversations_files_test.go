package handler

import (
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
