package handler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
)

type Message struct {
	MsgID         string `json:"message_id"`
	UserID        string `json:"user_id"`
	UserName      string `json:"user_name"`
	RealName      string `json:"real_name"`
	Channel       string `json:"channel"`
	ThreadTs      string `json:"thread_ts,omitempty"`
	Text          string `json:"text"`
	Time          string `json:"time"`
	Permalink     string `json:"permalink,omitempty"`
	Reactions     string `json:"reactions,omitempty"`
	BotName       string `json:"bot_name,omitempty"`
	FileCount     int    `json:"file_count,omitempty"`
	AttachmentIDs string `json:"attachment_ids,omitempty"`
	HasMedia      bool   `json:"has_media,omitempty"`
}

// CompactMessage is the default agent-oriented CSV shape: readable columns plus the
// IDs needed for reactions, thread replies, and attachment downloads. Drops only
// redundant or bulky fields (UserID when User is set, Permalink URLs, FileCount).
type CompactMessage struct {
	User          string `csv:"User"`
	Channel       string `csv:"Channel"`
	Text          string `csv:"Text"`
	Time          string `csv:"Time"`
	MsgID         string `csv:"MsgID"`
	ThreadTs      string `csv:"ThreadTs,omitempty"`
	Reactions     string `csv:"Reactions,omitempty"`
	AttachmentIDs string `csv:"AttachmentIDs,omitempty"`
	Files         string `csv:"Files,omitempty"`
}

// messagePipeline is the one path from Slack messages to CSV rows. Every tool
// that returns messages (history, replies, get_message, search, unreads,
// activity, saved) converts and renders through it, so a row reads the same
// whichever tool produced it.
type messagePipeline struct {
	api    *provider.ApiProvider
	logger *zap.Logger
}

// rows converts Slack messages to CSV rows. channel is the conversation every
// message belongs to; "" takes each message's own Channel.
func (p messagePipeline) rows(ctx context.Context, slackMessages []slack.Message, channel string, includeActivity bool, mode text.OutputMode) []Message {
	resolver := p.newUserResolver(ctx)
	authors := make([]string, 0, len(slackMessages))
	for _, msg := range slackMessages {
		if rendersInHistory(msg, includeActivity) {
			authors = append(authors, msg.User)
		}
	}
	resolver.prefetch(authors)

	messages := make([]Message, 0, len(slackMessages))
	warn := false

	for _, msg := range slackMessages {
		if !rendersInHistory(msg, includeActivity) {
			continue
		}

		userName, realName, ok := resolver.resolve(msg.User)

		if !ok && msg.SubType == "bot_message" {
			userName, realName, ok = msg.Username, msg.Username, true
		}

		if !ok {
			warn = true
		}

		timestamp, err := text.TimestampToIsoRFC3339(msg.Timestamp)
		if err != nil {
			p.logger.Warn("Failed to convert timestamp to RFC3339", zap.Error(err))
			continue
		}

		msgText := text.MergeBlocksWithText(msg.Text, msg.Blocks)
		if msgText == "" {
			msgText = text.FilesToText(msg.Files)
		}
		msgText += text.AttachmentsTo2CSV(msgText, msg.Attachments, mode)

		var reactionParts []string
		for _, r := range msg.Reactions {
			reactionParts = append(reactionParts, fmt.Sprintf("%s:%d", r.Name, r.Count))
		}

		botName := ""
		if msg.BotProfile != nil && msg.BotProfile.Name != "" {
			botName = msg.BotProfile.Name
		}

		var attachmentIDs []string
		for _, f := range msg.Files {
			attachmentIDs = append(attachmentIDs, attachmentRef(f))
		}

		rowChannel := channel
		if rowChannel == "" {
			rowChannel = msg.Channel
		}

		messages = append(messages, Message{
			MsgID:         msg.Timestamp,
			UserID:        msg.User,
			UserName:      userName,
			RealName:      realName,
			Text:          text.ProcessText(msgText),
			Channel:       rowChannel,
			ThreadTs:      msg.ThreadTimestamp,
			Time:          timestamp,
			Permalink:     msg.Permalink,
			Reactions:     strings.Join(reactionParts, "|"),
			BotName:       botName,
			FileCount:     len(msg.Files),
			AttachmentIDs: strings.Join(attachmentIDs, ", "),
			HasMedia:      len(msg.Files) > 0 || hasImageBlocks(msg.Blocks),
		})
	}

	if warn && !p.api.UsersCacheReady() {
		p.logger.Warn("Slack users sync is not ready yet: names may render as raw UIDs and @handle lookups will fail. Users sync runs as part of channels sync, and IM/MPIM channel operations need the users collection. Wait for the sync to finish and try again.")
	}
	return messages
}

// searchRows converts search.messages matches through the same rows path.
func (p messagePipeline) searchRows(ctx context.Context, matches []slack.SearchMessage, mode text.OutputMode) []Message {
	msgs := make([]slack.Message, len(matches))
	for i, m := range matches {
		msgs[i] = searchMatchMessage(m)
	}
	return p.rows(ctx, msgs, "", true, mode)
}

// searchMatchMessage carries a search match into the history message shape.
// A match has no subtype; one with a username and no user is a bot post.
func searchMatchMessage(m slack.SearchMessage) slack.Message {
	var msg slack.Message
	msg.Type = m.Type
	msg.Channel = m.Channel.ID
	msg.User = m.User
	msg.Username = m.Username
	msg.Timestamp = m.Timestamp
	msg.Blocks = m.Blocks
	msg.Text = m.Text
	msg.Permalink = m.Permalink
	msg.Attachments = m.Attachments
	msg.ThreadTimestamp, _ = extractThreadTS(m.Permalink)
	if m.User == "" && m.Username != "" {
		msg.SubType = "bot_message"
	}
	return msg
}

// rendersInHistory reports whether a conversations.history message reaches the
// output. Activity subtypes (joins, topic changes) are dropped unless the
// caller asked for them.
func rendersInHistory(msg slack.Message, includeActivity bool) bool {
	return includeActivity ||
		msg.SubType == "" ||
		msg.SubType == "bot_message" ||
		msg.SubType == "thread_broadcast"
}

func hasImageBlocks(blocks slack.Blocks) bool {
	for _, block := range blocks.BlockSet {
		if block.BlockType() == slack.MBTImage {
			return true
		}
	}
	return false
}

// message fetches one message by channel and timestamp through
// conversations.replies, which answers for top-level messages, thread parents
// and thread replies alike and needs only channels:history. nil means Slack
// has no message at that timestamp.
//
// Slack can answer with a different message than the one asked for (the
// thread parent, or the next message after a timestamp that matches
// nothing), so the first message counts only when its timestamp matches,
// as MessageFilesProvider.GetMessage checks.
func (p messagePipeline) message(ctx context.Context, channel, timestamp string) (*slack.Message, error) {
	msgs, _, _, err := p.api.WebAPI().GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: timestamp,
		Limit:     1,
		Inclusive: true,
	})
	if err != nil {
		p.logger.Debug("Failed to fetch message", zap.Error(err))
		return nil, fmt.Errorf("failed to fetch message: %w", err)
	}
	return messageAt(msgs, timestamp), nil
}

func messageAt(msgs []slack.Message, timestamp string) *slack.Message {
	if len(msgs) == 0 || msgs[0].Timestamp != timestamp {
		return nil
	}
	return &msgs[0]
}

// channelID resolves a channel reference to an ID. A "#name" or "@user" the
// warm cache does not know triggers one cache refresh before it is reported
// missing; IDs and anything else pass through for Slack to judge.
func (p messagePipeline) channelID(ctx context.Context, ref string) (string, error) {
	id, name := provider.ParseChannelRef(ref)
	if id != "" {
		return id, nil
	}
	ch, err := p.api.ChannelByRef(name)
	if err == nil {
		return ch.ID, nil
	}
	if !errors.Is(err, provider.ErrNotCached) {
		return "", cacheMiss(name, err, channelNotFound)
	}

	p.logger.Debug("Channel not found in cache, attempting refresh", zap.String("channel", name))
	refreshErr := p.api.ForceRefreshChannels(ctx)
	if errors.Is(refreshErr, provider.ErrRefreshRateLimited) {
		p.logger.Warn("Channel not found; cache refresh was rate-limited", zap.String("channel", name))
		return "", &ToolError{Code: codeNotFound, Message: fmt.Sprintf("channel %q not found and the cache refresh was rate-limited; retry shortly or pass the channel ID (Cxxxxxxxxxx)", name), Retryable: true}
	}
	if refreshErr != nil {
		p.logger.Warn("Failed to refresh channels cache", zap.String("channel", name), zap.Error(refreshErr))
		return "", &ToolError{Code: codeNotFound, Message: fmt.Sprintf("channel %q not found and cache refresh failed; pass the channel ID (Cxxxxxxxxxx) or look it up with channels_list", name), Cause: refreshErr}
	}
	if ch, err = p.api.ChannelByRef(name); err != nil {
		return "", cacheMiss(name, err, channelNotFound)
	}
	return ch.ID, nil
}

// renderOptions carries the per-call rendering context through
// marshalMessagesToCSV: output mode, the legend inputs, the result meta and
// any companion tables.
type renderOptions struct {
	mode         text.OutputMode
	workspaceURL string
	channelName  func(channelID string) string
	meta         ResultMeta
	sections     []csvSection
}

func (p messagePipeline) render(mode text.OutputMode, meta ResultMeta) renderOptions {
	return renderOptions{
		mode:         mode,
		workspaceURL: p.api.WorkspaceURL(),
		channelName:  p.api.ChannelLabel,
		meta:         meta,
	}
}

func marshalMessagesToCSV(messages []Message, opts renderOptions) (*mcp.CallToolResult, error) {
	if opts.mode != text.ModeFull {
		return marshalMessagesToCompactCSV(messages, opts)
	}
	return csvResult(buildChannelsLegend(messages, opts.channelName), opts.meta, &messages, opts.sections...)
}

// marshalMessagesToCompactCSV converts messages to the default agent CSV format.
func marshalMessagesToCompactCSV(messages []Message, opts renderOptions) (*mcp.CallToolResult, error) {
	compact := make([]CompactMessage, len(messages))
	for i, m := range messages {
		user := m.RealName
		if user == "" {
			user = m.UserName
		}
		if m.BotName != "" {
			user = m.BotName + " (bot)"
		}

		files := ""
		if m.FileCount > 0 {
			files = strconv.Itoa(m.FileCount)
		} else if m.HasMedia {
			// An image block with no file still signals "there are files here".
			files = "1"
		}

		compact[i] = CompactMessage{
			User:          user,
			Channel:       m.Channel,
			Text:          m.Text,
			Time:          m.Time,
			MsgID:         m.MsgID,
			ThreadTs:      m.ThreadTs,
			Reactions:     m.Reactions,
			AttachmentIDs: m.AttachmentIDs,
			Files:         files,
		}
	}

	return csvResult(buildLegendHeader(messages, opts), opts.meta, &compact, opts.sections...)
}

// buildChannelsLegend emits "#channels: C1=#general, D2=@bob" for every
// distinct conversation ID in the page that the cache can name. The Channel
// column itself always holds the bare ID.
func buildChannelsLegend(messages []Message, channelName func(string) string) string {
	ids := make([]string, len(messages))
	for i, m := range messages {
		ids[i] = m.Channel
	}
	return channelsLegend(ids, channelName)
}

// buildAttachmentsLegend names the recovery route for rows carrying files, and
// states up front which types come back readable, so a reader can skip the ones
// that would only return bytes it cannot use.
func buildAttachmentsLegend(messages []Message) string {
	for _, m := range messages {
		if m.AttachmentIDs != "" {
			return "#attachments: fetch a FileID with attachment_get_data; images and text return readable content, other types return base64, 5MB cap\n"
		}
	}
	return ""
}

// buildLegendHeader emits comment lines (agent-oriented, not CSV data) that
// normalize per-row redundancy: channel IDs with their names, distinct users
// with their IDs, and a permalink template so links are derivable without a
// Permalink column. The users and link lines are skipped for tiny result sets
// where they would outweigh the rows.
func buildLegendHeader(messages []Message, opts renderOptions) string {
	var sb strings.Builder
	sb.WriteString(buildChannelsLegend(messages, opts.channelName))
	if len(messages) < 3 {
		sb.WriteString(buildAttachmentsLegend(messages))
		return sb.String()
	}

	seen := make(map[string]bool)
	var userParts []string
	for _, m := range messages {
		if m.BotName != "" || m.UserID == "" || seen[m.UserID] {
			continue
		}
		seen[m.UserID] = true

		label := m.UserID + "=" + m.UserName
		if m.RealName != "" && m.RealName != m.UserName {
			label += "|" + m.RealName
		}
		userParts = append(userParts, label)
	}
	if len(userParts) > 0 {
		sb.WriteString("#users: ")
		sb.WriteString(strings.Join(userParts, ", "))
		sb.WriteString("\n")
	}

	if opts.workspaceURL != "" {
		base := opts.workspaceURL
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		sb.WriteString(fmt.Sprintf("#link_template: %sarchives/{Channel}/p{MsgID with \".\" removed}\n", base))
	}
	sb.WriteString(buildAttachmentsLegend(messages))

	return sb.String()
}

// userResolver resolves user IDs to names, fetching unknown users from the
// Slack API on demand. It remembers which IDs it already tried to fetch, so a
// user that doesn't exist in Slack is only looked up once per batch rather
// than once per message.
type userResolver struct {
	api          *provider.ApiProvider
	ctx          context.Context
	attemptedIDs map[string]bool
}

func (p messagePipeline) newUserResolver(ctx context.Context) *userResolver {
	return &userResolver{api: p.api, ctx: ctx, attemptedIDs: make(map[string]bool)}
}

// prefetch resolves every uncached author on the page in one users.info call.
// Without it a page whose authors are outside the users cache — Slack Connect
// guests, members who joined since the last sync — costs one sequential round
// trip and one full snapshot rebuild each. A batch that fails or comes back
// short changes nothing: resolve still falls back to the per-user patch.
func (r *userResolver) prefetch(userIDs []string) {
	dir := r.api.Directory()
	var missing []string
	seen := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, cached := dir.User(id); !cached {
			missing = append(missing, id)
		}
	}
	if len(missing) < 2 {
		return // one miss is exactly what resolve already does in one call
	}
	_ = r.api.PatchUsers(r.ctx, missing)
}

func (r *userResolver) resolve(userID string) (userName, realName string, ok bool) {
	if u, ok := r.api.Directory().User(userID); ok {
		return u.Name, u.RealName, true
	}
	if userID == "" || r.attemptedIDs[userID] {
		return userID, userID, false
	}
	r.attemptedIDs[userID] = true
	patched, err := r.api.PatchUser(r.ctx, userID)
	if err != nil {
		return userID, userID, false
	}
	return patched.Name, patched.RealName, true
}

// label is the user's real name, else username, fetching an uncached user.
func (r *userResolver) label(userID string) string {
	name, realName, _ := r.resolve(userID)
	if realName != "" {
		return realName
	}
	return name
}
