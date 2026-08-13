package handler

import (
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/approval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnitPrepareOrExecute(t *testing.T) {
	store := approval.NewStore(time.Minute)
	binding := approval.Binding{TeamID: "T1", UserID: "U1", Provider: "local", Tool: "messages_delete", Arguments: []byte(`{"channel_id":"C1"}`)}

	_, _, err := prepareOrExecute(store, "nope", "", binding)
	var toolErr *ToolError
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "invalid_arguments", toolErr.Code)

	prepared, execute, err := prepareOrExecute(store, "prepare", "", binding)
	require.NoError(t, err)
	assert.False(t, execute)
	require.NotNil(t, prepared)
	assert.NotEmpty(t, prepared.Token)

	_, _, err = prepareOrExecute(store, "execute", "", binding)
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "approval_required", toolErr.Code)

	_, execute, err = prepareOrExecute(store, "execute", prepared.Token, binding)
	require.NoError(t, err)
	assert.True(t, execute)

	_, _, err = prepareOrExecute(store, "execute", prepared.Token, binding)
	require.ErrorAs(t, err, &toolErr)
	assert.Equal(t, "approval_invalid", toolErr.Code)
}
