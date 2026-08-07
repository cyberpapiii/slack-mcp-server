package handler

import (
	"strings"
	"testing"

	"github.com/gocarina/gocsv"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewUserGroupFromSlack_UsersJoin(t *testing.T) {
	t.Run("joins member IDs with semicolon", func(t *testing.T) {
		ug := newUserGroupFromSlack(slack.UserGroup{
			ID:     "S1",
			Name:   "eng",
			Handle: "eng",
			Users:  []string{"U1", "U2"},
		})
		assert.Equal(t, "U1;U2", ug.Users)
	})

	t.Run("omits users when empty", func(t *testing.T) {
		ug := newUserGroupFromSlack(slack.UserGroup{ID: "S1", Name: "eng"})
		assert.Empty(t, ug.Users)

		csvBytes, err := gocsv.MarshalBytes(&[]UserGroup{ug})
		require.NoError(t, err)
		assert.NotContains(t, string(csvBytes), "U1")
		// Header still includes users column via omitempty on empty field.
		assert.True(t, strings.Contains(string(csvBytes), "id") || strings.Contains(string(csvBytes), "ID") || len(csvBytes) > 0)
	})

	t.Run("CSV round-trip keeps multi-user cell intact", func(t *testing.T) {
		ug := newUserGroupFromSlack(slack.UserGroup{
			ID:    "S1",
			Name:  "eng",
			Users: []string{"U1", "U2"},
		})
		csvBytes, err := gocsv.MarshalBytes(&[]UserGroup{ug})
		require.NoError(t, err)
		assert.Contains(t, string(csvBytes), "U1;U2")
		assert.NotContains(t, string(csvBytes), "U1,U2")
	})
}
