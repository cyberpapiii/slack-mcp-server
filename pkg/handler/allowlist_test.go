package handler

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// allowlistedToolArgs are otherwise valid arguments for every tool with a
// channel allowlist, aimed at a channel the allowlist does not name.
var allowlistedToolArgs = map[string]map[string]any{
	capability.ConversationsAddMessage: {"channel_id": "C123", "text": "hi"},
	capability.ReactionsAdd:            {"channel_id": "C123", "timestamp": "1712345678.000100", "emoji": "+1"},
	capability.ReactionsRemove:         {"channel_id": "C123", "timestamp": "1712345678.000100", "emoji": "+1"},
	capability.FilesUpload:             {"channel_id": "C123", "filename": "a.txt", "content": "hello"},
	capability.MessagesSchedule:        {"channel_id": "C123", "text": "hi", "post_at": strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)},
	capability.MessagesUpdate:          {"channel_id": "C123", "text": "hi", "timestamp": "1712345678.000100"},
	capability.MessagesDelete:          {"channel_id": "C123", "timestamp": "1712345678.000100", "action": "prepare"},
	capability.ChannelsRename:          {"channel_id": "C123", "name": "renamed"},
	capability.ChannelsSetTopic:        {"channel_id": "C123", "topic": "t"},
	capability.ChannelsSetPurpose:      {"channel_id": "C123", "purpose": "p"},
	capability.ChannelsArchive:         {"channel_id": "C123", "action": "prepare"},
}

// Allowlist enforcement lives in each handler, so this drives every catalog
// tool that declares a channel allowlist and checks it refuses a channel
// outside the list before touching Slack. A new allowlisted tool fails here
// until it is added to allowlistedToolArgs, and then until its handler checks.
func TestUnitEveryAllowlistedToolRefusesADeniedChannel(t *testing.T) {
	for _, spec := range capability.Tools {
		if spec.ChannelAllowlist == "" {
			continue
		}
		t.Run(spec.Name, func(t *testing.T) {
			args, ok := allowlistedToolArgs[spec.Name]
			require.True(t, ok, "add %s to allowlistedToolArgs", spec.Name)

			settings, err := settingsWith(map[string]string{spec.ChannelAllowlist: "C456"})
			require.NoError(t, err)
			messageFiles := &fakeMessageFilesService{}
			channelMutations := &fakeChannelMutationService{}
			var tools []ToolDef
			tools = append(tools, NewConversationsHandler(&fakeSlack{}, (&fakeSlack{}).pipeline(), zap.NewNop(), settings, servedTools(capability.ConversationsAddMessage)).Tools()...)
			tools = append(tools, NewMessageFilesHandler(messageFiles, userIdentity, settings, zap.NewNop()).Tools()...)
			tools = append(tools, NewChannelMutationHandler(channelMutations, userIdentity, settings).Tools()...)

			var run *ToolDef
			for i := range tools {
				if tools[i].Name == spec.Name {
					run = &tools[i]
				}
			}
			require.NotNil(t, run, "build the handler serving %s in this test", spec.Name)

			_, err = run.Run(context.Background(), toolRequest(args))
			var typed *ToolError
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, "permission_denied", typed.Code)
			assert.Contains(t, typed.Message, spec.Name+" is not allowed")
			assert.Contains(t, typed.Message, spec.ChannelAllowlist)
			assert.Zero(t, messageFiles.uploadCalls+messageFiles.scheduleCalls+messageFiles.updateCalls+messageFiles.deleteCalls+messageFiles.lookupCalls)
			assert.Zero(t, channelMutations.prepareCalls+channelMutations.archiveCalls)
			assert.Empty(t, channelMutations.action)
		})
	}
}
