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
