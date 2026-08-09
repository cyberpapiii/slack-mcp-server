package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeChannelMutationClient struct {
	infos        []*slack.Channel
	infoCalls    int
	renameCalls  int
	topicCalls   int
	purposeCalls int
	archiveCalls int
	lastValue    string
	mutationErr  error
}

func (f *fakeChannelMutationClient) GetConversationInfoContext(context.Context, *slack.GetConversationInfoInput) (*slack.Channel, error) {
	index := f.infoCalls
	f.infoCalls++
	if index >= len(f.infos) {
		return nil, errors.New("unexpected info call")
	}
	return f.infos[index], nil
}

func (f *fakeChannelMutationClient) RenameConversationContext(_ context.Context, _ string, value string) (*slack.Channel, error) {
	f.renameCalls++
	f.lastValue = value
	if f.mutationErr != nil {
		return nil, f.mutationErr
	}
	result := *f.infos[0]
	result.Name = value
	return &result, nil
}

func (f *fakeChannelMutationClient) SetTopicOfConversationContext(_ context.Context, _ string, value string) (*slack.Channel, error) {
	f.topicCalls++
	f.lastValue = value
	result := *f.infos[0]
	result.Topic.Value = value
	return &result, f.mutationErr
}

func (f *fakeChannelMutationClient) SetPurposeOfConversationContext(_ context.Context, _ string, value string) (*slack.Channel, error) {
	f.purposeCalls++
	f.lastValue = value
	result := *f.infos[0]
	result.Purpose.Value = value
	return &result, f.mutationErr
}

func (f *fakeChannelMutationClient) ArchiveConversationContext(context.Context, string) error {
	f.archiveCalls++
	return f.mutationErr
}

func testMutationChannel() *slack.Channel {
	return &slack.Channel{GroupConversation: slack.GroupConversation{
		Conversation: slack.Conversation{ID: "C123"},
		Name:         "old-name",
		Topic:        slack.Topic{Value: "old topic"},
		Purpose:      slack.Purpose{Value: "old purpose"},
	}}
}

func TestChannelMutationMetadataMethods(t *testing.T) {
	tests := []struct {
		name   string
		call   func(*ChannelMutationProvider) (ChannelMutationState, error)
		assert func(*testing.T, *fakeChannelMutationClient, ChannelMutationState)
	}{
		{"rename", func(p *ChannelMutationProvider) (ChannelMutationState, error) {
			return p.Rename(context.Background(), "C123", "new-name")
		}, func(t *testing.T, f *fakeChannelMutationClient, state ChannelMutationState) {
			assert.Equal(t, 1, f.renameCalls)
			assert.Equal(t, "new-name", state.Name)
		}},
		{"clear topic", func(p *ChannelMutationProvider) (ChannelMutationState, error) {
			return p.SetTopic(context.Background(), "C123", "")
		}, func(t *testing.T, f *fakeChannelMutationClient, state ChannelMutationState) {
			assert.Equal(t, 1, f.topicCalls)
			assert.Empty(t, f.lastValue)
			assert.Empty(t, state.Topic)
		}},
		{"clear purpose", func(p *ChannelMutationProvider) (ChannelMutationState, error) {
			return p.SetPurpose(context.Background(), "C123", "")
		}, func(t *testing.T, f *fakeChannelMutationClient, state ChannelMutationState) {
			assert.Equal(t, 1, f.purposeCalls)
			assert.Empty(t, f.lastValue)
			assert.Empty(t, state.Purpose)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeChannelMutationClient{infos: []*slack.Channel{testMutationChannel()}}
			state, err := tt.call(NewChannelMutationProvider(fake))
			require.NoError(t, err)
			assert.Equal(t, 1, fake.infoCalls)
			tt.assert(t, fake, state)
		})
	}
}

func TestChannelMutationRejectsGeneralAndSharedBeforeMutation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*slack.Channel)
		want error
	}{
		{"general", func(channel *slack.Channel) { channel.IsGeneral = true }, ErrChannelMutationGeneral},
		{"shared", func(channel *slack.Channel) { channel.IsExtShared = true }, ErrChannelMutationShared},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := testMutationChannel()
			tt.edit(channel)
			fake := &fakeChannelMutationClient{infos: []*slack.Channel{channel}}
			_, err := NewChannelMutationProvider(fake).Rename(context.Background(), "C123", "new-name")
			require.ErrorIs(t, err, tt.want)
			assert.Zero(t, fake.renameCalls)
		})
	}
}

func TestArchivePreparedChecksExactCurrentStateBeforeMutation(t *testing.T) {
	initial := testMutationChannel()
	unchanged := *initial
	fake := &fakeChannelMutationClient{infos: []*slack.Channel{initial, &unchanged}}
	provider := NewChannelMutationProvider(fake)

	preparation, err := provider.PrepareArchive(context.Background(), "C123")
	require.NoError(t, err)
	state, err := provider.ArchivePrepared(context.Background(), preparation)
	require.NoError(t, err)
	assert.True(t, state.Archived)
	assert.Equal(t, 2, fake.infoCalls)
	assert.Equal(t, 1, fake.archiveCalls)
}

func TestArchivePreparedRejectsStateDriftWithoutMutation(t *testing.T) {
	initial := testMutationChannel()
	changed := *initial
	changed.Topic.Value = "changed after review"
	fake := &fakeChannelMutationClient{infos: []*slack.Channel{initial, &changed}}
	provider := NewChannelMutationProvider(fake)

	preparation, err := provider.PrepareArchive(context.Background(), "C123")
	require.NoError(t, err)
	_, err = provider.ArchivePrepared(context.Background(), preparation)
	require.ErrorIs(t, err, ErrChannelArchiveConflict)
	assert.Zero(t, fake.archiveCalls)
}

func TestPrepareArchiveRejectsAlreadyArchived(t *testing.T) {
	channel := testMutationChannel()
	channel.IsArchived = true
	fake := &fakeChannelMutationClient{infos: []*slack.Channel{channel}}
	_, err := NewChannelMutationProvider(fake).PrepareArchive(context.Background(), "C123")
	require.ErrorIs(t, err, ErrChannelAlreadyArchived)
	assert.Zero(t, fake.archiveCalls)
}
