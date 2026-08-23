package handler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge/fasttime"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestUnitParseParamsToolUnreadsClamps pins the non-positive clamp on the two
// unreads size parameters. mcp GetInt defaults only absent keys; clamp keeps
// explicit -1/0 from becoming a panic-prone slice bound.
func TestUnitParseParamsToolUnreadsClamps(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	tests := []struct {
		name                      string
		args                      map[string]any
		wantMaxChannels           int
		wantMaxMessagesPerChannel int
	}{
		{"absent uses defaults", map[string]any{}, 50, 10},
		{"negative max_channels uses default", map[string]any{"max_channels": -1}, 50, 10},
		{"zero max_channels uses default", map[string]any{"max_channels": 0}, 50, 10},
		{"negative max_messages_per_channel uses default", map[string]any{"max_messages_per_channel": -3}, 50, 10},
		{"zero max_messages_per_channel uses default", map[string]any{"max_messages_per_channel": 0}, 50, 10},
		{"valid values pass through unchanged", map[string]any{"max_channels": 7, "max_messages_per_channel": 3}, 7, 3},
		{"both negative use defaults", map[string]any{"max_channels": -100, "max_messages_per_channel": -100}, 50, 10},
		{"json float encoding clamps too", map[string]any{"max_channels": float64(-1)}, 50, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tt.args

			params, err := ch.parseParamsToolUnreads(req)
			require.NoError(t, err)
			assert.Equal(t, tt.wantMaxChannels, params.maxChannels)
			assert.Equal(t, tt.wantMaxMessagesPerChannel, params.maxMessagesPerChannel)
		})
	}
}

// newUnreadsTestHandler builds a handler with no apiProvider.
func newUnreadsTestHandler() *ConversationsHandler {
	return &ConversationsHandler{logger: zap.NewNop()}
}

func TestUnitConversationsUnreadsFailsFastWithoutBrowserSession(t *testing.T) {
	h := NewConversationsHandler(&provider.ApiProvider{}, zap.NewNop(), false)
	result, err := h.ConversationsUnreadsHandler(context.Background(), mcp.CallToolRequest{})
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "conversations_unreads needs a Slack browser session (xoxc/xoxd)")
}

// ftime converts a Slack timestamp string into the edge fasttime.Time that
// ChannelSnapshot carries.
func ftime(t *testing.T, ts string) fasttime.Time {
	t.Helper()
	i, err := fasttime.TS2int(ts)
	require.NoError(t, err)
	return fasttime.Time(fasttime.Int2Time(i))
}

func unreadTypes(channels []UnreadChannel) []string {
	out := make([]string, 0, len(channels))
	for _, c := range channels {
		out = append(out, c.ChannelType)
	}
	return out
}

func unreadIDs(channels []UnreadChannel) []string {
	out := make([]string, 0, len(channels))
	for _, c := range channels {
		out = append(out, c.ChannelID)
	}
	return out
}

// Type rank, then UnreadCount desc, ChannelID asc; unknown last.
func TestUnitSortChannelsByPriority(t *testing.T) {
	ch := newUnreadsTestHandler()

	t.Run("empty slice does not panic", func(t *testing.T) {
		var channels []UnreadChannel
		ch.sortChannelsByPriority(channels)
		assert.Empty(t, channels)
	})

	t.Run("single element", func(t *testing.T) {
		channels := []UnreadChannel{{ChannelID: "C1", ChannelType: "internal"}}
		ch.sortChannelsByPriority(channels)
		assert.Equal(t, []string{"C1"}, unreadIDs(channels))
	})

	t.Run("reversed input sorts dm, group_dm, partner, internal", func(t *testing.T) {
		channels := []UnreadChannel{
			{ChannelID: "C_internal", ChannelType: "internal"},
			{ChannelID: "C_partner", ChannelType: "partner"},
			{ChannelID: "G_mpim", ChannelType: "group_dm"},
			{ChannelID: "D_dm", ChannelType: "dm"},
		}
		ch.sortChannelsByPriority(channels)
		assert.Equal(t, []string{"D_dm", "G_mpim", "C_partner", "C_internal"}, unreadIDs(channels))
	})

	t.Run("type outranks mention count: a silent DM beats a busy channel", func(t *testing.T) {
		// The tiebreakers must not have become the primary key.
		channels := []UnreadChannel{
			{ChannelID: "C_busy", ChannelType: "internal", UnreadCount: 99, LastRead: "1800000000.000000"},
			{ChannelID: "D_quiet", ChannelType: "dm", UnreadCount: 0, LastRead: "1000000000.000000"},
		}
		ch.sortChannelsByPriority(channels)
		assert.Equal(t, []string{"D_quiet", "C_busy"}, unreadIDs(channels))
	})

	t.Run("unknown and empty channel types sort last", func(t *testing.T) {
		channels := []UnreadChannel{
			{ChannelID: "Y_empty", ChannelType: ""},
			{ChannelID: "C_internal", ChannelType: "internal"},
			{ChannelID: "X_unknown", ChannelType: "totally-made-up"},
			{ChannelID: "C_partner", ChannelType: "partner"},
		}
		ch.sortChannelsByPriority(channels)

		assert.Equal(t, []string{"C_partner", "C_internal", "X_unknown", "Y_empty"}, unreadIDs(channels),
			"unknown ranks fall behind every known type; ChannelID breaks the tie between them")
		assert.Equal(t, []string{"partner", "internal", "totally-made-up", ""}, unreadTypes(channels))
	})

	t.Run("mixed input with all known types plus unknown and empty", func(t *testing.T) {
		channels := []UnreadChannel{
			{ChannelID: "Y_empty", ChannelType: "", UnreadCount: 42},
			{ChannelID: "C_internal_b", ChannelType: "internal", UnreadCount: 1},
			{ChannelID: "D_dm_low", ChannelType: "dm", UnreadCount: 1},
			{ChannelID: "X_weird", ChannelType: "weird", UnreadCount: 7},
			{ChannelID: "C_internal_a", ChannelType: "internal", UnreadCount: 1},
			{ChannelID: "C_partner", ChannelType: "partner", UnreadCount: 3},
			{ChannelID: "G_mpim", ChannelType: "group_dm", UnreadCount: 0},
			{ChannelID: "D_dm_high", ChannelType: "dm", UnreadCount: 9},
			{ChannelID: "C_internal_busy", ChannelType: "internal", UnreadCount: 50},
		}
		ch.sortChannelsByPriority(channels)

		assert.Equal(t, []string{
			// dm: higher UnreadCount first
			"D_dm_high", "D_dm_low",
			// group_dm
			"G_mpim",
			// partner
			"C_partner",
			// internal: UnreadCount desc, then ChannelID asc for the 1/1 tie
			"C_internal_busy", "C_internal_a", "C_internal_b",
			// unknown ranks last, UnreadCount desc among them
			"Y_empty", "X_weird",
		}, unreadIDs(channels))
	})

	t.Run("sorting is deterministic across independently built slices", func(t *testing.T) {
		build := func() []UnreadChannel {
			return []UnreadChannel{
				{ChannelID: "C_a", ChannelType: "internal", UnreadCount: 5},
				{ChannelID: "C_b", ChannelType: "internal", UnreadCount: 5},
				{ChannelID: "C_c", ChannelType: "internal", UnreadCount: 5},
				{ChannelID: "D_a", ChannelType: "dm", UnreadCount: 0},
				{ChannelID: "D_b", ChannelType: "dm", UnreadCount: 0},
				{ChannelID: "X_a", ChannelType: "weird"},
				{ChannelID: "X_b", ChannelType: ""},
			}
		}

		first, second := build(), build()
		// Feed the second one in a different order: a total order must still
		// collapse both to the same sequence.
		slices.Reverse(second)

		ch.sortChannelsByPriority(first)
		ch.sortChannelsByPriority(second)

		assert.Equal(t, unreadIDs(first), unreadIDs(second))
		assert.Equal(t, []string{"D_a", "D_b", "C_a", "C_b", "C_c", "X_a", "X_b"}, unreadIDs(first))
	})
}

// Unknown channel_types rejected at parse time.
func TestUnitParseParamsToolUnreadsChannelTypes(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	t.Run("accepted values parse", func(t *testing.T) {
		for _, want := range []string{"all", "dm", "group_dm", "partner", "internal"} {
			t.Run(want, func(t *testing.T) {
				req := mcp.CallToolRequest{}
				req.Params.Arguments = map[string]any{"channel_types": want}

				params, err := ch.parseParamsToolUnreads(req)
				require.NoError(t, err)
				assert.Equal(t, want, params.channelTypes)
			})
		}
	})

	t.Run("absent defaults to all", func(t *testing.T) {
		req := mcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{}

		params, err := ch.parseParamsToolUnreads(req)
		require.NoError(t, err)
		assert.Equal(t, "all", params.channelTypes)
	})

	t.Run("empty value is treated as absent, not rejected", func(t *testing.T) {
		// MCP clients differ on how they serialize an omitted optional string;
		// several send "" rather than dropping the key. GetString cannot tell
		// the two apart, so "" must mean "no filter".
		for _, empty := range []string{"", " ", "\t"} {
			t.Run(fmt.Sprintf("%q", empty), func(t *testing.T) {
				req := mcp.CallToolRequest{}
				req.Params.Arguments = map[string]any{"channel_types": empty}

				params, err := ch.parseParamsToolUnreads(req)
				require.NoError(t, err)
				assert.Equal(t, "all", params.channelTypes)
			})
		}
	})

	t.Run("unrecognized values are errors", func(t *testing.T) {
		for _, bad := range []string{"public", "DM", "im", "public_channel"} {
			t.Run(bad, func(t *testing.T) {
				req := mcp.CallToolRequest{}
				req.Params.Arguments = map[string]any{"channel_types": bad}

				params, err := ch.parseParamsToolUnreads(req)
				require.Error(t, err)
				assert.Nil(t, params)
				assert.Contains(t, err.Error(), fmt.Sprintf("%q", bad),
					"the message names the offending value")
				assert.Contains(t, err.Error(), "all, dm, group_dm, partner, internal",
					"the message lists the accepted set")
			})
		}
	})
}

// TestUnitMarshalUnreadChannelsToCSV pins the exact wire contract of the
// summary (include_messages=false) output.
func TestUnitMarshalUnreadChannelsToCSV(t *testing.T) {
	const header = "Channel,Name,Type,UnreadCount,LastRead\n"

	t.Run("three channels, comma in a name is quoted", func(t *testing.T) {
		channels := []UnreadChannel{
			{
				ChannelID:   "D111",
				ChannelName: "@alice",
				ChannelType: "dm",
				UnreadCount: 2,
				LastRead:    "1700000000.000100",
			},
			{
				ChannelID:   "C222",
				ChannelName: "#eng, product",
				ChannelType: "partner",
				UnreadCount: 7,
				LastRead:    "1700000100.000000",
			},
			{
				ChannelID:   "C333",
				ChannelName: "#general",
				ChannelType: "internal",
				UnreadCount: 0,
				LastRead:    "",
			},
		}

		res, err := marshalUnreadChannelsToCSV(channels, unreadsCoverage{}.meta())
		require.NoError(t, err)
		assert.Nil(t, res.StructuredContent)
		assert.Equal(t, header+
			"D111,@alice,dm,2,1700000000.000100\n"+
			"C222,\"#eng, product\",partner,7,1700000100.000000\n"+
			"C333,#general,internal,0,\n",
			csvResultBody(t, res))
	})

	t.Run("empty and nil slices still emit the header row", func(t *testing.T) {
		for _, channels := range [][]UnreadChannel{nil, {}} {
			res, err := marshalUnreadChannelsToCSV(channels, unreadsCoverage{}.meta())
			require.NoError(t, err)
			assert.Equal(t, header, csvResultBody(t, res))
		}
	})

	t.Run("coverage gaps become one #partial line ahead of the header", func(t *testing.T) {
		coverage := unreadsCoverage{
			mutedUnavailable: true,
			maxChannels:      2,
			dropped:          3,
			failed:           []string{"C_ERR"},
			unbounded:        []string{"D_NEW"},
		}
		res, err := marshalUnreadChannelsToCSV(nil, coverage.meta())
		require.NoError(t, err)
		assert.Equal(t,
			"#partial: muted-channel preferences unavailable; "+
				"max_channels=2 reached, 3 more unread channels not listed; "+
				"history fetch failed for C_ERR; "+
				"no last-read bound for D_NEW (messages not fetched)\n"+
				header,
			csvResultBody(t, res))
	})
}

// defaultUnreadsParams mirrors the defaults of parseParamsToolUnreads.
func defaultUnreadsParams() *unreadsParams {
	return &unreadsParams{
		includeMessages:       true,
		channelTypes:          "all",
		maxChannels:           50,
		maxMessagesPerChannel: 10,
	}
}

// TestUnitCollectUnreadChannels pins the client.counts -> []UnreadChannel
// resolution step: which snapshots survive filtering, what they get named, and
// what UnreadCount they carry before any history backfill.
func TestUnitCollectUnreadChannels(t *testing.T) {
	ch := newUnreadsTestHandler()

	users := &provider.UsersCache{Users: map[string]slack.User{
		"U_ALICE": {ID: "U_ALICE", Name: "alice", RealName: "Alice A"},
	}}
	channelsCache := &provider.ChannelsCache{Channels: map[string]provider.Channel{
		"C_MENTION": {ID: "C_MENTION", Name: "mentions"},
		"C_SILENT":  {ID: "C_SILENT", Name: "silent"},
		"C_READ":    {ID: "C_READ", Name: "read"},
		"C_EXT":     {ID: "C_EXT", Name: "vendor", IsExtShared: true},
		"C_HASHED":  {ID: "C_HASHED", Name: "#already-hashed"},
		"G_MPIM":    {ID: "G_MPIM", Name: "mpdm-alice--bob-1", IsMpIM: true},
		"G_HASHED":  {ID: "G_HASHED", Name: "#mpdm-hashed", IsMpIM: true},
		"D_ALICE":   {ID: "D_ALICE", Name: "alice", IsIM: true, User: "U_ALICE"},
		"D_GHOST":   {ID: "D_GHOST", Name: "ghost", IsIM: true, User: "U_NOT_CACHED"},
		"D_NOUSER":  {ID: "D_NOUSER", Name: "nouser", IsIM: true},
	}}

	counts := func() edge.ClientCountsResponse {
		return edge.ClientCountsResponse{
			Channels: []edge.ChannelSnapshot{
				{ID: "C_MENTION", HasUnreads: true, MentionCount: 3,
					LastRead: ftime(t, "1700000000.000100"), Latest: ftime(t, "1700000900.000200")},
				{ID: "C_SILENT", HasUnreads: true, MentionCount: 0,
					LastRead: ftime(t, "1700000100.000000"), Latest: ftime(t, "1700000800.000000")},
				{ID: "C_READ", HasUnreads: false, MentionCount: 0,
					LastRead: ftime(t, "1700000200.000000"), Latest: ftime(t, "1700000200.000000")},
			},
		}
	}

	t.Run("read channel excluded, mention count preserved, silent channel kept at zero", func(t *testing.T) {
		got, _ := ch.collectUnreadChannels(defaultUnreadsParams(), counts(), users, channelsCache)

		require.Len(t, got, 2)
		assert.ElementsMatch(t, []string{"C_MENTION", "C_SILENT"}, unreadIDs(got),
			"HasUnreads=false is dropped; both unread channels survive")

		byID := map[string]UnreadChannel{}
		for _, u := range got {
			byID[u.ChannelID] = u
		}
		assert.Equal(t, 3, byID["C_MENTION"].UnreadCount, "mention channel keeps its MentionCount")
		assert.Equal(t, 0, byID["C_SILENT"].UnreadCount,
			"a zero-mention unread channel leaves collect with UnreadCount 0; it is the "+
				"backfill (summary mode) or the message fetch (include_messages) that fills it in")

		// Timestamps round-trip through fasttime.SlackString().
		assert.Equal(t, "1700000000.000100", byID["C_MENTION"].LastRead)
	})

	t.Run("names, types and cache misses", func(t *testing.T) {
		c := edge.ClientCountsResponse{
			Channels: []edge.ChannelSnapshot{
				{ID: "C_SILENT", HasUnreads: true},
				{ID: "C_EXT", HasUnreads: true},
				{ID: "C_HASHED", HasUnreads: true},
				{ID: "C_UNKNOWN", HasUnreads: true},
			},
		}
		got, _ := ch.collectUnreadChannels(defaultUnreadsParams(), c, users, channelsCache)
		require.Len(t, got, 4)

		byID := map[string]UnreadChannel{}
		for _, u := range got {
			byID[u.ChannelID] = u
		}
		assert.Equal(t, "#silent", byID["C_SILENT"].ChannelName, "cached name gets a # prefix")
		assert.Equal(t, "internal", byID["C_SILENT"].ChannelType)
		assert.Equal(t, "#already-hashed", byID["C_HASHED"].ChannelName, "an existing # is not doubled")
		assert.Equal(t, "#vendor", byID["C_EXT"].ChannelName)
		assert.Equal(t, "partner", byID["C_EXT"].ChannelType, "IsExtShared maps to partner")
		// Cache miss: bare ID, no #.
		assert.Equal(t, "C_UNKNOWN", byID["C_UNKNOWN"].ChannelName)
		assert.Equal(t, "internal", byID["C_UNKNOWN"].ChannelType)
	})

	t.Run("mpim and im naming", func(t *testing.T) {
		c := edge.ClientCountsResponse{
			MPIMs: []edge.ChannelSnapshot{
				{ID: "G_MPIM", HasUnreads: true, MentionCount: 2},
				{ID: "G_HASHED", HasUnreads: true},
				{ID: "G_UNKNOWN", HasUnreads: true},
			},
			IMs: []edge.ChannelSnapshot{
				{ID: "D_ALICE", HasUnreads: true, MentionCount: 1},
				{ID: "D_GHOST", HasUnreads: true},
				{ID: "D_NOUSER", HasUnreads: true},
				{ID: "D_UNKNOWN", HasUnreads: true},
			},
		}
		got, _ := ch.collectUnreadChannels(defaultUnreadsParams(), c, users, channelsCache)
		require.Len(t, got, 7)

		byID := map[string]UnreadChannel{}
		for _, u := range got {
			byID[u.ChannelID] = u
		}
		// MPIM names get the same #-prefix normalization as channels.
		assert.Equal(t, "#mpdm-alice--bob-1", byID["G_MPIM"].ChannelName)
		assert.Equal(t, "group_dm", byID["G_MPIM"].ChannelType)
		assert.Equal(t, "#mpdm-hashed", byID["G_HASHED"].ChannelName,
			"an existing # is not doubled")
		// A cache miss still falls back to the bare ID with no prefix: an
		// unresolved ID is not a name, so "#G_UNKNOWN" would be a lie.
		assert.Equal(t, "G_UNKNOWN", byID["G_UNKNOWN"].ChannelName)

		assert.Equal(t, "@alice", byID["D_ALICE"].ChannelName, "IM resolves via the users cache")
		assert.Equal(t, "dm", byID["D_ALICE"].ChannelType)
		assert.Equal(t, "@U_NOT_CACHED", byID["D_GHOST"].ChannelName, "unknown user falls back to @<userID>")
		// Empty User or cache miss: bare DM ID, no @.
		assert.Equal(t, "D_NOUSER", byID["D_NOUSER"].ChannelName)
		assert.Equal(t, "D_UNKNOWN", byID["D_UNKNOWN"].ChannelName)
	})

	t.Run("muted channels are dropped", func(t *testing.T) {
		p := defaultUnreadsParams()
		p.mutedChannels = map[string]bool{"C_MENTION": true}
		got, _ := ch.collectUnreadChannels(p, counts(), users, channelsCache)
		assert.Equal(t, []string{"C_SILENT"}, unreadIDs(got))

		// collect only reads mutedChannels; include_muted is upstream.
		p2 := defaultUnreadsParams()
		p2.includeMuted = true
		p2.mutedChannels = map[string]bool{"C_MENTION": true}
		got2, _ := ch.collectUnreadChannels(p2, counts(), users, channelsCache)
		assert.Equal(t, []string{"C_SILENT"}, unreadIDs(got2),
			"include_muted alone does not undo an already-populated mutedChannels map")
	})

	t.Run("mentions_only drops zero-mention channels", func(t *testing.T) {
		p := defaultUnreadsParams()
		p.mentionsOnly = true
		c := counts()
		c.MPIMs = []edge.ChannelSnapshot{{ID: "G_MPIM", HasUnreads: true, MentionCount: 0}}
		c.IMs = []edge.ChannelSnapshot{{ID: "D_ALICE", HasUnreads: true, MentionCount: 1}}

		got, _ := ch.collectUnreadChannels(p, c, users, channelsCache)
		assert.Equal(t, []string{"D_ALICE", "C_MENTION"}, unreadIDs(got),
			"the zero-mention channel and the zero-mention MPIM are both dropped")
	})

	t.Run("channel_types filter", func(t *testing.T) {
		c := counts()
		c.Channels = append(c.Channels, edge.ChannelSnapshot{ID: "C_EXT", HasUnreads: true})
		c.MPIMs = []edge.ChannelSnapshot{{ID: "G_MPIM", HasUnreads: true}}
		c.IMs = []edge.ChannelSnapshot{{ID: "D_ALICE", HasUnreads: true}}

		for _, tc := range []struct {
			channelTypes string
			wantIDs      []string
		}{
			{"dm", []string{"D_ALICE"}},
			{"group_dm", []string{"G_MPIM"}},
			{"partner", []string{"C_EXT"}},
			{"internal", []string{"C_MENTION", "C_SILENT"}},
			// Pure collect: unrecognized → empty. Tool parse rejects first
			// (TestUnitParseParamsToolUnreadsChannelTypes).
			{"nonsense", nil},
		} {
			t.Run(tc.channelTypes, func(t *testing.T) {
				p := defaultUnreadsParams()
				p.channelTypes = tc.channelTypes
				got, _ := ch.collectUnreadChannels(p, c, users, channelsCache)
				assert.ElementsMatch(t, tc.wantIDs, unreadIDs(got))
			})
		}

		t.Run("all", func(t *testing.T) {
			p := defaultUnreadsParams()
			got, _ := ch.collectUnreadChannels(p, c, users, channelsCache)
			require.Len(t, got, 5)
			assert.Equal(t, []string{"D_ALICE", "G_MPIM", "C_EXT", "C_MENTION", "C_SILENT"}, unreadIDs(got))
		})
	})

	t.Run("max_channels truncates after sorting", func(t *testing.T) {
		c := counts()
		c.IMs = []edge.ChannelSnapshot{{ID: "D_ALICE", HasUnreads: true}}
		p := defaultUnreadsParams()
		p.maxChannels = 2

		got, dropped := ch.collectUnreadChannels(p, c, users, channelsCache)
		require.Len(t, got, 2)
		assert.Equal(t, 1, dropped)
		assert.Equal(t, "D_ALICE", got[0].ChannelID, "the DM survives truncation because sorting runs first")
	})

	t.Run("a non-positive max_channels does not panic and truncates nothing", func(t *testing.T) {
		c := counts()
		c.IMs = []edge.ChannelSnapshot{{ID: "D_ALICE", HasUnreads: true}}

		for _, maxChannels := range []int{-1, 0} {
			p := defaultUnreadsParams()
			p.maxChannels = maxChannels

			got, _ := ch.collectUnreadChannels(p, c, users, channelsCache)
			assert.ElementsMatch(t, []string{"D_ALICE", "C_MENTION", "C_SILENT"}, unreadIDs(got),
				"a non-positive limit slices with a negative bound unless guarded; "+
					"the guard skips truncation entirely rather than returning nothing")
		}
	})

	t.Run("nothing unread yields a nil slice", func(t *testing.T) {
		got, _ := ch.collectUnreadChannels(defaultUnreadsParams(), edge.ClientCountsResponse{}, users, channelsCache)
		assert.Nil(t, got)
	})

	t.Run("a snapshot with no last_read renders an empty string", func(t *testing.T) {
		// Zero fasttime -> "" via slackTS (not year-1 SlackString).
		c := edge.ClientCountsResponse{
			Channels: []edge.ChannelSnapshot{{ID: "C_SILENT", HasUnreads: true}},
		}
		got, _ := ch.collectUnreadChannels(defaultUnreadsParams(), c, users, channelsCache)
		require.Len(t, got, 1)
		assert.Equal(t, "", got[0].LastRead)
	})
}

// TestUnitSlackTS pins the zero-value plaster over fasttime.Time: a never-read
// channel must render as "" so callers can tell "no bound available" apart from
// a real timestamp, instead of fasttime's literal year-1 rendering.
func TestUnitSlackTS(t *testing.T) {
	t.Run("the zero value renders as an empty string", func(t *testing.T) {
		var zero fasttime.Time
		require.True(t, time.Time(zero).IsZero())
		assert.Equal(t, "-62135596800.000000", zero.SlackString(),
			"the raw fasttime rendering this helper exists to suppress")
		assert.Equal(t, "", slackTS(zero))
	})

	t.Run("a real timestamp round-trips unchanged", func(t *testing.T) {
		ts := fasttime.Time(time.UnixMicro(1710632873037269))
		assert.Equal(t, "1710632873.037269", slackTS(ts))
		assert.Equal(t, ts.SlackString(), slackTS(ts))
	})
}

// fakeHistoryFetcher is a historyFetcher that records every call and replays
// canned results keyed by channel ID.
type fakeHistoryFetcher struct {
	byChannel map[string]*slack.GetConversationHistoryResponse
	errs      map[string]error
	calls     []slack.GetConversationHistoryParameters
}

func (f *fakeHistoryFetcher) GetConversationHistoryContext(_ context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error) {
	f.calls = append(f.calls, *params)
	if err, ok := f.errs[params.ChannelID]; ok {
		return nil, err
	}
	if resp, ok := f.byChannel[params.ChannelID]; ok {
		return resp, nil
	}
	return &slack.GetConversationHistoryResponse{}, nil
}

func historyWith(n int) *slack.GetConversationHistoryResponse {
	resp := &slack.GetConversationHistoryResponse{}
	for i := 0; i < n; i++ {
		resp.Messages = append(resp.Messages, slack.Message{})
	}
	return resp
}

// TestUnitBackfillUnreadCounts pins the summary-mode unread-count backfill,
// including the call-count contract that keeps include_messages=true from
// issuing a redundant conversations.history round per channel.
func TestUnitBackfillUnreadCounts(t *testing.T) {
	ch := newUnreadsTestHandler()
	req := mcp.CallToolRequest{}

	t.Run("include_messages=true issues zero history calls", func(t *testing.T) {
		fake := &fakeHistoryFetcher{}
		channels := []UnreadChannel{
			{ChannelID: "C_SILENT", UnreadCount: 0, LastRead: "1700000000.000000"},
			{ChannelID: "C_MENTION", UnreadCount: 3, LastRead: "1700000000.000000"},
		}
		p := defaultUnreadsParams() // includeMessages defaults to true

		require.True(t, ch.backfillUnreadCounts(context.Background(), req, fake, p, channels))
		assert.Empty(t, fake.calls,
			"the message-fetch loop re-issues the same conversations.history call, so the "+
				"backfill must not run in include_messages mode")
		assert.Equal(t, 0, channels[0].UnreadCount, "counts are left untouched for the message loop to set")
		assert.Equal(t, 3, channels[1].UnreadCount)
	})

	t.Run("include_messages=false backfills only zero-count channels", func(t *testing.T) {
		fake := &fakeHistoryFetcher{
			byChannel: map[string]*slack.GetConversationHistoryResponse{
				"C_SILENT": historyWith(4),
			},
		}
		channels := []UnreadChannel{
			{ChannelID: "D_ALICE", UnreadCount: 2, LastRead: "1700000000.000000"},
			{ChannelID: "C_SILENT", UnreadCount: 0, LastRead: "1700000100.000000"},
		}
		p := defaultUnreadsParams()
		p.includeMessages = false

		require.True(t, ch.backfillUnreadCounts(context.Background(), req, fake, p, channels))

		require.Len(t, fake.calls, 1, "exactly one history call: the already-counted DM is skipped")
		assert.Equal(t, slack.GetConversationHistoryParameters{
			ChannelID: "C_SILENT",
			Oldest:    "1700000100.000000",
			Limit:     20,
			Inclusive: false,
		}, fake.calls[0], "the backfill window is bounded by LastRead and capped at 20 rows")

		assert.Equal(t, 2, channels[0].UnreadCount, "a positive MentionCount is left alone")
		assert.Equal(t, 4, channels[1].UnreadCount, "the row count replaces the zero")
	})

	t.Run("empty LastRead short-circuits to a conservative 1 with no call", func(t *testing.T) {
		fake := &fakeHistoryFetcher{}
		channels := []UnreadChannel{{ChannelID: "C_SILENT", UnreadCount: 0, LastRead: ""}}
		p := defaultUnreadsParams()
		p.includeMessages = false

		require.True(t, ch.backfillUnreadCounts(context.Background(), req, fake, p, channels))
		assert.Empty(t, fake.calls)
		assert.Equal(t, 1, channels[0].UnreadCount)
	})

	t.Run("history failure reports a conservative 1 and the loop continues", func(t *testing.T) {
		// Failed fetch: conservative 1, loop continues.
		fake := &fakeHistoryFetcher{
			errs:      map[string]error{"C_BROKEN": errors.New("boom")},
			byChannel: map[string]*slack.GetConversationHistoryResponse{"C_OK": historyWith(2)},
		}
		channels := []UnreadChannel{
			{ChannelID: "C_BROKEN", UnreadCount: 0, LastRead: "1700000000.000000"},
			{ChannelID: "C_OK", UnreadCount: 0, LastRead: "1700000000.000000"},
		}
		p := defaultUnreadsParams()
		p.includeMessages = false

		require.True(t, ch.backfillUnreadCounts(context.Background(), req, fake, p, channels))
		require.Len(t, fake.calls, 2, "a failure on one channel does not abort the loop")
		assert.Equal(t, 1, channels[0].UnreadCount, "the failed fetch reports a conservative 1")
		assert.Equal(t, 2, channels[1].UnreadCount)
	})

	t.Run("zero rows in the unread window reports a conservative 1", func(t *testing.T) {
		// Zero-row window: conservative 1, not "0 unread".
		fake := &fakeHistoryFetcher{
			byChannel: map[string]*slack.GetConversationHistoryResponse{"C_SILENT": historyWith(0)},
		}
		channels := []UnreadChannel{{ChannelID: "C_SILENT", UnreadCount: 0, LastRead: "1700000000.000000"}}
		p := defaultUnreadsParams()
		p.includeMessages = false

		require.True(t, ch.backfillUnreadCounts(context.Background(), req, fake, p, channels))
		require.Len(t, fake.calls, 1)
		assert.Equal(t, 1, channels[0].UnreadCount)
	})

	t.Run("cancelled context reports false before any call", func(t *testing.T) {
		fake := &fakeHistoryFetcher{}
		channels := []UnreadChannel{{ChannelID: "C_SILENT", UnreadCount: 0, LastRead: "1700000000.000000"}}
		p := defaultUnreadsParams()
		p.includeMessages = false

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		assert.False(t, ch.backfillUnreadCounts(ctx, req, fake, p, channels))
		assert.Empty(t, fake.calls)
		assert.Equal(t, 0, channels[0].UnreadCount)
	})
}

// Empty last_read never hits conversations.history / year-1 Oldest.
func TestUnitUnreadsZeroLastReadNeverReachesSlack(t *testing.T) {
	ch := newUnreadsTestHandler()

	counts := edge.ClientCountsResponse{
		Channels: []edge.ChannelSnapshot{
			{ID: "C_NEVER_READ", HasUnreads: true}, // zero LastRead and Latest
			{ID: "C_BOUNDED", HasUnreads: true,
				LastRead: ftime(t, "1700000100.000000"), Latest: ftime(t, "1700000800.000000")},
		},
	}
	users := &provider.UsersCache{Users: map[string]slack.User{}}
	channelsCache := &provider.ChannelsCache{Channels: map[string]provider.Channel{}}

	p := defaultUnreadsParams()
	p.includeMessages = false

	channels, _ := ch.collectUnreadChannels(p, counts, users, channelsCache)
	require.Len(t, channels, 2)

	fake := &fakeHistoryFetcher{
		byChannel: map[string]*slack.GetConversationHistoryResponse{"C_BOUNDED": historyWith(2)},
	}
	require.True(t, ch.backfillUnreadCounts(context.Background(), mcp.CallToolRequest{}, fake, p, channels))

	require.Len(t, fake.calls, 1, "only the channel with a real last_read is queried")
	assert.Equal(t, "C_BOUNDED", fake.calls[0].ChannelID)
	assert.Equal(t, "1700000100.000000", fake.calls[0].Oldest)
	for _, call := range fake.calls {
		assert.NotEqual(t, "-62135596800.000000", call.Oldest)
		assert.NotEqual(t, "", call.Oldest, "an empty Oldest would mean 'whole channel history'")
	}

	byID := map[string]UnreadChannel{}
	for _, c := range channels {
		byID[c.ChannelID] = c
	}
	assert.Equal(t, 1, byID["C_NEVER_READ"].UnreadCount, "conservative 1, no API call")
	assert.Equal(t, 2, byID["C_BOUNDED"].UnreadCount)
}

// Conservative UnreadCount after history fetch (summary CSV path).
func TestUnitUnreadCountFromHistory(t *testing.T) {
	boom := errors.New("boom")

	tests := []struct {
		name     string
		current  int
		msgCount int
		err      error
		want     int
	}{
		{"fetch failed with no prior count reports 1", 0, 0, boom, 1},
		{"fetch failed keeps a positive prior count", 5, 0, boom, 5},
		{"row count replaces a zero", 0, 3, nil, 3},
		{"row count replaces a positive prior count", 5, 3, nil, 3},
		{"zero rows reports 1", 0, 0, nil, 1},
		// Zero rows keep positive prior count.
		{"zero rows keeps a positive prior count", 5, 0, nil, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, unreadCountFromHistory(tt.current, tt.msgCount, tt.err))
		})
	}
}
