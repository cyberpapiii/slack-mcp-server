package approval

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type archiveArgs struct {
	ChannelID string `json:"channel_id"`
}

type channelState struct {
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

func testBinding(t *testing.T) Binding {
	t.Helper()
	binding, err := NewBinding("T1", "U1", "channels_archive", archiveArgs{"C1"}, channelState{Name: "daily-test"})
	require.NoError(t, err)
	return binding
}

func TestStoreConsumesExactBindingOnce(t *testing.T) {
	store := NewStore()
	token, _, err := store.Prepare(testBinding(t))
	require.NoError(t, err)

	require.NoError(t, store.Consume(token, testBinding(t)))
	assert.ErrorIs(t, store.Consume(token, testBinding(t)), ErrReplay)
}

func TestStoreRejectsTamperingAndCrossIdentityReplay(t *testing.T) {
	store := NewStore()
	token, _, err := store.Prepare(testBinding(t))
	require.NoError(t, err)

	otherChannel, err := NewBinding("T1", "U1", "channels_archive", archiveArgs{"C2"}, channelState{Name: "daily-test"})
	require.NoError(t, err)
	assert.ErrorIs(t, store.Consume(token, otherChannel), ErrBinding)

	otherUser, err := NewBinding("T1", "U2", "channels_archive", archiveArgs{"C1"}, channelState{Name: "daily-test"})
	require.NoError(t, err)
	assert.ErrorIs(t, store.Consume(token, otherUser), ErrBinding)

	changed, err := NewBinding("T1", "U1", "channels_archive", archiveArgs{"C1"}, channelState{Name: "renamed"})
	require.NoError(t, err)
	assert.ErrorIs(t, store.Consume(token, changed), ErrBinding)

	require.NoError(t, store.Consume(token, testBinding(t)), "a failed binding check must not consume the token")
}

func TestStoreRejectsExpiryAndRestart(t *testing.T) {
	start := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	now := start
	store := NewStoreWithClock(func() time.Time { return now })
	token, expiresAt, err := store.Prepare(testBinding(t))
	require.NoError(t, err)
	assert.Equal(t, start.Add(TTL), expiresAt)
	now = start.Add(TTL)
	assert.ErrorIs(t, store.Consume(token, testBinding(t)), ErrExpired)

	assert.ErrorIs(t, NewStore().Consume(token, testBinding(t)), ErrInvalid)
}

func TestNewBindingNeedsAnActorAndTool(t *testing.T) {
	_, err := NewBinding("", "U1", "channels_archive", archiveArgs{"C1"}, nil)
	assert.ErrorIs(t, err, ErrBinding)
	_, err = NewBinding("T1", "U1", "", archiveArgs{"C1"}, nil)
	assert.ErrorIs(t, err, ErrBinding)
}
