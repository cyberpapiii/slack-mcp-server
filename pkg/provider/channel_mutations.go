package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/slack-go/slack"
)

var (
	ErrChannelMutationGeneral = errors.New("general channel cannot be mutated")
	ErrChannelMutationShared  = errors.New("shared channel cannot be mutated")
	ErrChannelArchiveConflict = errors.New("channel changed after archive preparation")
	ErrChannelAlreadyArchived = errors.New("channel is already archived")
)

// ChannelMutationClient is intentionally smaller than SlackAPI. Archive uses
// GetConversationInfoContext both before preparation and immediately before
// mutation so callers never retry a destructive request blindly.
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

type ChannelMutationProvider struct {
	client ChannelMutationClient
}

func NewChannelMutationProvider(client ChannelMutationClient) *ChannelMutationProvider {
	return &ChannelMutationProvider{client: client}
}

func (ap *ApiProvider) ChannelMutations() (*ChannelMutationProvider, error) {
	client, ok := ap.client.(ChannelMutationClient)
	if !ok || client == nil {
		return nil, errors.New("Slack client does not support channel mutations")
	}
	return NewChannelMutationProvider(client), nil
}

func (c *MCPSlackClient) RenameConversationContext(ctx context.Context, channelID, name string) (*slack.Channel, error) {
	return c.standardSlackClient().RenameConversationContext(ctx, channelID, name)
}

func (c *MCPSlackClient) SetTopicOfConversationContext(ctx context.Context, channelID, topic string) (*slack.Channel, error) {
	return c.standardSlackClient().SetTopicOfConversationContext(ctx, channelID, topic)
}

func (c *MCPSlackClient) SetPurposeOfConversationContext(ctx context.Context, channelID, purpose string) (*slack.Channel, error) {
	return c.standardSlackClient().SetPurposeOfConversationContext(ctx, channelID, purpose)
}

func (c *MCPSlackClient) ArchiveConversationContext(ctx context.Context, channelID string) error {
	return c.standardSlackClient().ArchiveConversationContext(ctx, channelID)
}

func (p *ChannelMutationProvider) Rename(ctx context.Context, channelID, name string) (ChannelMutationState, error) {
	if _, err := p.currentMutableChannel(ctx, channelID); err != nil {
		return ChannelMutationState{}, err
	}
	channel, err := p.client.RenameConversationContext(ctx, channelID, name)
	return mutationResponseState(channel, err)
}

func (p *ChannelMutationProvider) SetTopic(ctx context.Context, channelID, topic string) (ChannelMutationState, error) {
	if _, err := p.currentMutableChannel(ctx, channelID); err != nil {
		return ChannelMutationState{}, err
	}
	channel, err := p.client.SetTopicOfConversationContext(ctx, channelID, topic)
	return mutationResponseState(channel, err)
}

func (p *ChannelMutationProvider) SetPurpose(ctx context.Context, channelID, purpose string) (ChannelMutationState, error) {
	if _, err := p.currentMutableChannel(ctx, channelID); err != nil {
		return ChannelMutationState{}, err
	}
	channel, err := p.client.SetPurposeOfConversationContext(ctx, channelID, purpose)
	return mutationResponseState(channel, err)
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
		return ChannelMutationState{}, fmt.Errorf("%w: expected %+v, current %+v", ErrChannelArchiveConflict, preparation.Expected, currentState)
	}
	if err := p.client.ArchiveConversationContext(ctx, preparation.Expected.ChannelID); err != nil {
		return ChannelMutationState{}, err
	}
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

func mutationResponseState(channel *slack.Channel, err error) (ChannelMutationState, error) {
	if err != nil {
		return ChannelMutationState{}, err
	}
	if channel == nil {
		return ChannelMutationState{}, errors.New("Slack returned no channel after mutation")
	}
	return stateFromChannel(channel), nil
}
