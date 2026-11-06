package provider

import (
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
)

func testDirectory() *Directory {
	users := newUsersCache([]slack.User{
		{ID: "U001", Name: "alice", RealName: "Alice Liddell", Profile: slack.UserProfile{Email: "alice@example.com"}},
		{ID: "U002", Name: "bob"},
		{ID: "W003", Name: "carol", RealName: "Carol"},
	})
	channels := newChannelsCache(4)
	channels.add(Channel{ID: "C100", Name: "#general"})
	channels.add(Channel{ID: "D200", Name: "@alice", IsIM: true, User: "U001"})
	channels.add(Channel{ID: "D300", Name: "@W003", IsIM: true, User: "W003"})
	channels.add(Channel{ID: "G400", Name: "@mpdm-alice--bob-1", IsMpIM: true})
	return NewDirectory(users, channels)
}

func TestUnitClassifyID(t *testing.T) {
	for id, want := range map[string]IDKind{
		"U0123ABC":  IDUser,
		"W0123ABC":  IDUser,
		"C0123ABC":  IDChannel,
		"G0123ABC":  IDGroup,
		"D0123ABC":  IDDirectMessage,
		"F0123ABC":  IDFile,
		"Sa0123ABC": IDReminder,
		"Uwe":       IDUnknown,
		"c0123abc":  IDUnknown,
		"U1":        IDUser,
		"#general":  IDUnknown,
		"":          IDUnknown,
		"Sa":        IDUnknown,
	} {
		assert.Equal(t, want, ClassifyID(id), id)
	}
}

func TestUnitChannelByRef(t *testing.T) {
	d := testDirectory()
	for ref, want := range map[string]string{
		"C100":               "C100",
		"#general":           "C100",
		"<#C100|general>":    "C100",
		"<#C100>":            "C100",
		"@alice":             "D200",
		"@U001":              "D200",
		"@carol":             "D300",
		"@mpdm-alice--bob-1": "G400",
		" #general ":         "C100",
	} {
		ch, err := d.ChannelByRef(ref)
		assert.NoError(t, err, ref)
		assert.Equal(t, want, ch.ID, ref)
	}
	for _, ref := range []string{"#missing", "@nobody", "C999", "@bob"} {
		_, err := d.ChannelByRef(ref)
		assert.ErrorIs(t, err, ErrNotCached, ref)
	}
}

func TestUnitUserByRef(t *testing.T) {
	d := testDirectory()
	for ref, want := range map[string]string{
		"U001":              "U001",
		"<@U001>":           "U001",
		"<@U001|alice>":     "U001",
		"@alice":            "U001",
		"alice":             "U001",
		"ALICE@example.com": "U001",
		"W003":              "W003",
	} {
		u, err := d.UserByRef(ref)
		assert.NoError(t, err, ref)
		assert.Equal(t, want, u.ID, ref)
	}
	for _, ref := range []string{"U999", "@nobody", "", "<@>"} {
		_, err := d.UserByRef(ref)
		assert.ErrorIs(t, err, ErrNotCached, ref)
	}
}

func TestUnitAMissOnAWarmingCacheSaysSo(t *testing.T) {
	d := testDirectory()
	d.usersReady, d.channelsReady = false, false

	_, err := d.ChannelByRef("#general")
	assert.NoError(t, err, "a cached name resolves while warming")
	_, err = d.ChannelByRef("#missing")
	assert.ErrorIs(t, err, ErrChannelsNotReady)
	_, err = d.ChannelByRef("@nobody")
	assert.ErrorIs(t, err, ErrUsersNotReady, "the DM lookup needs the user first")
	_, err = d.UserByRef("nobody")
	assert.ErrorIs(t, err, ErrUsersNotReady)

	d.usersReady = true
	_, err = d.ChannelByRef("@bob")
	assert.ErrorIs(t, err, ErrChannelsNotReady, "a known user whose DM is not cached yet")
}

func TestUnitLabels(t *testing.T) {
	d := testDirectory()
	assert.Equal(t, "Alice Liddell", d.UserLabel("U001"))
	assert.Equal(t, "bob", d.UserLabel("U002"))
	assert.Equal(t, "", d.UserLabel("U999"))

	assert.Equal(t, "#general", d.ChannelLabel("C100"))
	assert.Equal(t, "@alice", d.ChannelLabel("D200"))
	assert.Equal(t, "@carol", d.ChannelLabel("D300"), "a DM cached before its peer was known takes the current name")
	assert.Equal(t, "@mpdm-alice--bob-1", d.ChannelLabel("G400"))
	assert.Equal(t, "", d.ChannelLabel("C999"))
}

func TestUnitDMChannelFor(t *testing.T) {
	d := testDirectory()
	ch, ok := d.DMChannelFor("U001")
	assert.True(t, ok)
	assert.Equal(t, "D200", ch.ID)
	_, ok = d.DMChannelFor("U002")
	assert.False(t, ok)
}

func TestUnitDirectoryReadsTheCurrentSnapshot(t *testing.T) {
	ap := newTestApiProvider(nil, newUsersCache(nil))
	ap.channelsSnapshot.Store(newChannelsCache(0))
	assert.Equal(t, "", ap.UserLabel("U001"))
	ap.usersSnapshot.Store(newUsersCache([]slack.User{{ID: "U001", Name: "alice"}}))
	assert.Equal(t, "alice", ap.UserLabel("U001"))
}
