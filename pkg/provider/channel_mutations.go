package provider

import (
	"context"
	"errors"

	"github.com/slack-go/slack"
)

var _ ChannelMutationClient = (*WebClient)(nil)

type ChannelMutationClient interface {
	GetConversationInfoContext(context.Context, *slack.GetConversationInfoInput) (*slack.Channel, error)
	RenameConversationContext(context.Context, string, string) (*slack.Channel, error)
	SetTopicOfConversationContext(context.Context, string, string) (*slack.Channel, error)
	SetPurposeOfConversationContext(context.Context, string, string) (*slack.Channel, error)
	ArchiveConversationContext(context.Context, string) error
}

type ChannelMutationState struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	Topic     string `json:"topic"`
	Purpose   string `json:"purpose"`
	Archived  bool   `json:"archived"`
	General   bool   `json:"general"`
	Shared    bool   `json:"shared"`
}

type ArchivePreparation struct {
	Expected ChannelMutationState `json:"expected"`
}

// channelSnapshot is the channels cache a write keeps current.
type channelSnapshot interface {
	UpsertChannel(*slack.Channel)
	forgetChannel(id string)
}

type ChannelMutationProvider struct {
	client ChannelMutationClient
	cache  channelSnapshot
}

// ChannelMutations keeps the channels cache current: a rename, topic or
// purpose change is written into it, an archived channel leaves it.
func (ap *ApiProvider) ChannelMutations() *ChannelMutationProvider {
	return &ChannelMutationProvider{client: ap.WebAPI(), cache: ap}
}

func (p *ChannelMutationProvider) Rename(ctx context.Context, channelID, name string) (ChannelMutationState, error) {
	if _, err := p.currentMutableChannel(ctx, channelID); err != nil {
		return ChannelMutationState{}, err
	}
	channel, err := p.client.RenameConversationContext(ctx, channelID, name)
	return p.mutated(channel, err)
}

func (p *ChannelMutationProvider) SetTopic(ctx context.Context, channelID, topic string) (ChannelMutationState, error) {
	if _, err := p.currentMutableChannel(ctx, channelID); err != nil {
		return ChannelMutationState{}, err
	}
	channel, err := p.client.SetTopicOfConversationContext(ctx, channelID, topic)
	return p.mutated(channel, err)
}

func (p *ChannelMutationProvider) SetPurpose(ctx context.Context, channelID, purpose string) (ChannelMutationState, error) {
	if _, err := p.currentMutableChannel(ctx, channelID); err != nil {
		return ChannelMutationState{}, err
	}
	channel, err := p.client.SetPurposeOfConversationContext(ctx, channelID, purpose)
	return p.mutated(channel, err)
}

func (p *ChannelMutationProvider) PrepareArchive(ctx context.Context, channelID string) (ArchivePreparation, error) {
	channel, err := p.currentMutableChannel(ctx, channelID)
	if err != nil {
		return ArchivePreparation{}, err
	}
	if channel.IsArchived {
		return ArchivePreparation{}, ErrChannelAlreadyArchived
	}
	return ArchivePreparation{Expected: stateFromChannel(channel)}, nil
}

func (p *ChannelMutationProvider) ArchivePrepared(ctx context.Context, preparation ArchivePreparation) (ChannelMutationState, error) {
	current, err := p.currentMutableChannel(ctx, preparation.Expected.ChannelID)
	if err != nil {
		return ChannelMutationState{}, err
	}
	currentState := stateFromChannel(current)
	if currentState != preparation.Expected {
		return ChannelMutationState{}, ErrChannelArchiveConflict
	}
	if err := p.client.ArchiveConversationContext(ctx, preparation.Expected.ChannelID); err != nil {
		return ChannelMutationState{}, err
	}
	p.cache.forgetChannel(preparation.Expected.ChannelID)
	currentState.Archived = true
	return currentState, nil
}

func (p *ChannelMutationProvider) currentMutableChannel(ctx context.Context, channelID string) (*slack.Channel, error) {
	channel, err := p.client.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: channelID})
	if err != nil {
		return nil, err
	}
	if channel == nil {
		return nil, errors.New("Slack returned no channel")
	}
	if channel.IsGeneral {
		return nil, ErrChannelMutationGeneral
	}
	if channel.IsShared || channel.IsExtShared || channel.IsOrgShared || channel.IsGlobalShared || channel.IsPendingExtShared {
		return nil, ErrChannelMutationShared
	}
	return channel, nil
}

func stateFromChannel(channel *slack.Channel) ChannelMutationState {
	if channel == nil {
		return ChannelMutationState{}
	}
	return ChannelMutationState{
		ChannelID: channel.ID,
		Name:      channel.Name,
		Topic:     channel.Topic.Value,
		Purpose:   channel.Purpose.Value,
		Archived:  channel.IsArchived,
		General:   channel.IsGeneral,
		Shared:    channel.IsShared || channel.IsExtShared || channel.IsOrgShared || channel.IsGlobalShared || channel.IsPendingExtShared,
	}
}

func (p *ChannelMutationProvider) mutated(channel *slack.Channel, err error) (ChannelMutationState, error) {
	if err != nil {
		return ChannelMutationState{}, err
	}
	if channel == nil {
		return ChannelMutationState{}, errors.New("Slack returned no channel after mutation")
	}
	p.cache.UpsertChannel(channel)
	return stateFromChannel(channel), nil
}
