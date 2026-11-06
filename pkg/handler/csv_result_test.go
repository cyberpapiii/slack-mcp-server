package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// mapLabels is a Labels backed by fixed maps.
type mapLabels struct {
	channels map[string]string
	users    map[string]string
}

func (l mapLabels) ChannelLabel(id string) string { return l.channels[id] }

func (l mapLabels) UserLabel(id string) string { return l.users[id] }

func TestNewCSVResultCommentLines(t *testing.T) {
	result := NewCSVResult("#users: U1=alice\n", SlackResultMeta("abc", true, "stopped at limit"), "User,Text\nalice,hi\n")

	assert.Nil(t, result.StructuredContent)
	assert.Equal(t, "#users: U1=alice\n#partial: stopped at limit\n#next_cursor: abc\nUser,Text\nalice,hi\n", ResultText(result))
}

func TestNewCSVResultOmitsEmptyMeta(t *testing.T) {
	result := NewCSVResult("", SlackResultMeta("", false, "ignored"), "A\n1\n")

	assert.Equal(t, "A\n1\n", ResultText(result))
}

func TestUnitCSVResultEmptyListIsHeaderOnly(t *testing.T) {
	var rows []reactionRow
	result, err := csvResult("", SlackResultMeta("", false, ""), &rows)
	assert.NoError(t, err)
	assert.Equal(t, "Emoji,Count,Users\n", ResultText(result))
}

func TestUnitCSVResultSections(t *testing.T) {
	rows := []reactionRow{{Emoji: "wave", Count: 1, Users: "U1"}}
	more := []reactionRow{{Emoji: "tada", Count: 2, Users: "U1;U2"}}
	result, err := csvResult("#legend\n", SlackResultMeta("next", false, ""), &rows, csvSection{name: "extra", rows: &more})
	assert.NoError(t, err)
	assert.Equal(t, "#legend\n#next_cursor: next\nEmoji,Count,Users\nwave,1,U1\n#extra:\nEmoji,Count,Users\ntada,2,U1;U2\n", ResultText(result))
}

func TestUnitTimeText(t *testing.T) {
	assert.Equal(t, "", unixTimeText(0))
	assert.Equal(t, "", unixTimeText(-1), "negative means unset")
	assert.Equal(t, "2023-11-14T22:13:20Z", unixTimeText(1700000000))
	assert.Equal(t, "2023-11-14T22:13:20Z", slackTsText("1700000000.000100"))
	assert.Equal(t, "not-a-ts", slackTsText("not-a-ts"))
}
