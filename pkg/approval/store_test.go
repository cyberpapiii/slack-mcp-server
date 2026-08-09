package approval

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testBinding() Binding {
	return Binding{
		TeamID: "T1", UserID: "U1", Provider: "local", Tool: "channels_archive",
		Arguments:     json.RawMessage(`{"channel_id":"C1"}`),
		ObservedState: json.RawMessage(`{"archived":false,"name":"daily-test"}`),
	}
}

func TestStoreConsumesExactBindingOnce(t *testing.T) {
	store := NewStore(time.Minute)
	prepared, err := store.Prepare(testBinding())
	require.NoError(t, err)

	got, err := store.Consume(prepared.Token, testBinding())
	require.NoError(t, err)
	assert.Equal(t, "C1", stringValue(t, got.Arguments, "channel_id"))
	_, err = store.Consume(prepared.Token, testBinding())
	assert.ErrorIs(t, err, ErrReplay)
}

func TestStoreRejectsTamperingAndCrossIdentityReplay(t *testing.T) {
	store := NewStore(time.Minute)
	prepared, err := store.Prepare(testBinding())
	require.NoError(t, err)

	tampered := testBinding()
	tampered.Arguments = json.RawMessage(`{"channel_id":"C2"}`)
	_, err = store.Consume(prepared.Token, tampered)
	assert.ErrorIs(t, err, ErrBinding)

	wrongUser := testBinding()
	wrongUser.UserID = "U2"
	_, err = store.Consume(prepared.Token, wrongUser)
	assert.ErrorIs(t, err, ErrBinding)

	_, err = store.Consume(prepared.Token, testBinding())
	require.NoError(t, err, "a failed binding check must not consume the token")
}

func TestStoreRejectsExpiryAndRestart(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	store := NewStore(time.Minute)
	store.now = func() time.Time { return now }
	prepared, err := store.Prepare(testBinding())
	require.NoError(t, err)
	store.now = func() time.Time { return now.Add(time.Minute) }
	_, err = store.Consume(prepared.Token, testBinding())
	assert.ErrorIs(t, err, ErrExpired)

	restarted := NewStore(time.Minute)
	_, err = restarted.Consume(prepared.Token, testBinding())
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestStoreRejectsInvalidCanonicalJSON(t *testing.T) {
	binding := testBinding()
	binding.Arguments = json.RawMessage(`{"channel_id":`)
	_, err := NewStore(time.Minute).Prepare(binding)
	assert.ErrorIs(t, err, ErrBinding)
}

func stringValue(t *testing.T, raw json.RawMessage, key string) string {
	t.Helper()
	var values map[string]string
	require.NoError(t, json.Unmarshal(raw, &values))
	return values[key]
}
