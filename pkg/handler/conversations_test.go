package handler

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUnitParseFlexibleDate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantDate string
		wantErr  bool
	}{
		// Standard formats
		{
			name:     "YYYY-MM-DD",
			input:    "2025-07-15",
			wantDate: "2025-07-15",
			wantErr:  false,
		},
		{
			name:     "YYYY/MM/DD",
			input:    "2025/07/15",
			wantDate: "2025-07-15",
			wantErr:  false,
		},

		// Flexible month-year formats
		{
			name:     "Month Year - July 2025",
			input:    "July 2025",
			wantDate: "2025-07-01",
			wantErr:  false,
		},
		{
			name:     "Year Month - 2025 July",
			input:    "2025 July",
			wantDate: "2025-07-01",
			wantErr:  false,
		},
		{
			name:     "Abbreviated Month Year - Jul 2025",
			input:    "Jul 2025",
			wantDate: "2025-07-01",
			wantErr:  false,
		},
		{
			name:     "Year Abbreviated Month - 2025 Jul",
			input:    "2025 Jul",
			wantDate: "2025-07-01",
			wantErr:  false,
		},
		{
			name:     "Case insensitive - july 2025",
			input:    "july 2025",
			wantDate: "2025-07-01",
			wantErr:  false,
		},
		{
			name:     "Case insensitive - JULY 2025",
			input:    "JULY 2025",
			wantDate: "2025-07-01",
			wantErr:  false,
		},

		// Day-Month-Year formats
		{
			name:     "1-July-2025",
			input:    "1-July-2025",
			wantDate: "2025-07-01",
			wantErr:  false,
		},
		{
			name:     "July-25-2025",
			input:    "July-25-2025",
			wantDate: "2025-07-25",
			wantErr:  false,
		},
		{
			name:     "July 10 2025",
			input:    "July 10 2025",
			wantDate: "2025-07-10",
			wantErr:  false,
		},
		{
			name:     "10 July 2025",
			input:    "10 July 2025",
			wantDate: "2025-07-10",
			wantErr:  false,
		},
		{
			name:     "31-December-2025",
			input:    "31-December-2025",
			wantDate: "2025-12-31",
			wantErr:  false,
		},
		{
			name:     "2025 July 10",
			input:    "2025 July 10",
			wantDate: "2025-07-10",
			wantErr:  false,
		},

		// Various month names
		{
			name:     "January full name",
			input:    "January 2025",
			wantDate: "2025-01-01",
			wantErr:  false,
		},
		{
			name:     "February abbreviated",
			input:    "Feb 2025",
			wantDate: "2025-02-01",
			wantErr:  false,
		},
		{
			name:     "September with Sept abbreviation",
			input:    "Sept 2025",
			wantDate: "2025-09-01",
			wantErr:  false,
		},

		// Relative dates
		{
			name:     "today",
			input:    "today",
			wantDate: time.Now().UTC().Format("2006-01-02"),
			wantErr:  false,
		},
		{
			name:     "yesterday",
			input:    "yesterday",
			wantDate: time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"),
			wantErr:  false,
		},
		{
			name:     "Today with capital T",
			input:    "Today",
			wantDate: time.Now().UTC().Format("2006-01-02"),
			wantErr:  false,
		},
		{
			name:     "Yesterday with capital Y",
			input:    "Yesterday",
			wantDate: time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"),
			wantErr:  false,
		},
		{
			name:     "TODAY all caps",
			input:    "TODAY",
			wantDate: time.Now().UTC().Format("2006-01-02"),
			wantErr:  false,
		},
		{
			name:     "YESTERDAY all caps",
			input:    "YESTERDAY",
			wantDate: time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"),
			wantErr:  false,
		},
		{
			name:     "tomorrow",
			input:    "tomorrow",
			wantDate: time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02"),
			wantErr:  false,
		},
		{
			name:     "5 days ago",
			input:    "5 days ago",
			wantDate: time.Now().UTC().AddDate(0, 0, -5).Format("2006-01-02"),
			wantErr:  false,
		},
		{
			name:     "1 day ago",
			input:    "1 day ago",
			wantDate: time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"),
			wantErr:  false,
		},

		// Edge cases
		{
			name:     "Whitespace trimming",
			input:    "  July 2025  ",
			wantDate: "2025-07-01",
			wantErr:  false,
		},
		{
			name:     "Invalid month name",
			input:    "Jully 2025",
			wantDate: "",
			wantErr:  true,
		},
		{
			name:     "Invalid date format",
			input:    "2025-13-01",
			wantDate: "",
			wantErr:  true,
		},
		{
			name:     "Invalid day for month",
			input:    "31-February-2025",
			wantDate: "",
			wantErr:  true,
		},
		{
			name:     "Empty string",
			input:    "",
			wantDate: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, gotDate, err := parseFlexibleDate(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseFlexibleDate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && gotDate != tt.wantDate {
				t.Errorf("parseFlexibleDate() gotDate = %v, want %v", gotDate, tt.wantDate)
			}
		})
	}
}

func TestUnitBuildDateFiltersUnit(t *testing.T) {
	tests := []struct {
		name    string
		before  string
		after   string
		on      string
		during  string
		want    map[string]string
		wantErr bool
	}{
		{
			name:    "On with flexible format July 2025",
			before:  "",
			after:   "",
			on:      "July 2025",
			during:  "",
			want:    map[string]string{"on": "2025-07-01"},
			wantErr: false,
		},
		{
			name:    "Before and After with flexible formats",
			before:  "December 2025",
			after:   "January 2025",
			on:      "",
			during:  "",
			want:    map[string]string{"before": "2025-12-01", "after": "2025-01-01"},
			wantErr: false,
		},
		{
			name:    "During with day format",
			before:  "",
			after:   "",
			on:      "",
			during:  "15-July-2025",
			want:    map[string]string{"during": "2025-07-15"},
			wantErr: false,
		},
		{
			name:    "Error: on with other filters",
			before:  "2025-12-01",
			after:   "",
			on:      "July 2025",
			during:  "",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Error: during with before",
			before:  "2025-12-01",
			after:   "",
			on:      "",
			during:  "July 2025",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Error: after date is after before date",
			before:  "January 2025",
			after:   "December 2025",
			on:      "",
			during:  "",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Valid: complex date formats",
			before:  "31-December-2025",
			after:   "1-January-2025",
			on:      "",
			during:  "",
			want:    map[string]string{"before": "2025-12-31", "after": "2025-01-01"},
			wantErr: false,
		},
		{
			name:    "Error: invalid date format",
			before:  "",
			after:   "",
			on:      "Jully 2025",
			during:  "",
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildDateFilters(tt.before, tt.after, tt.on, tt.during)
			if (err != nil) != tt.wantErr {
				t.Errorf("buildDateFilters() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("buildDateFilters() got map length = %v, want %v", len(got), len(tt.want))
					return
				}
				for k, v := range tt.want {
					if got[k] != v {
						t.Errorf("buildDateFilters() got[%s] = %v, want %v", k, got[k], v)
					}
				}
			}
		})
	}
}

func TestUnitLimitByExpression_Valid(t *testing.T) {
	now := time.Now()

	oneMonthAgo := now.AddDate(0, -1, 0)
	twoMonthsAgo := now.AddDate(0, -2, 0)

	oneMonthSpan := int64(now.Sub(oneMonthAgo).Seconds())
	twoMonthSpan := int64(now.Sub(twoMonthsAgo).Seconds())

	const tolerance = 86400

	tests := []struct {
		name    string
		input   string
		minSecs int64 // inclusive
		maxSecs int64 // exclusive
	}{
		{"1 day", "", 0, 86400}, // default case with no input test
		{"1 day", "1d", 0, 86400},
		{"2 days", "2d", 86400, 172800},
		{"1 week", "1w", 6 * 86400, 7 * 86400},
		{"2 weeks", "2w", 13 * 86400, 14 * 86400},
		{"1 month", "1m", oneMonthSpan - tolerance, oneMonthSpan + tolerance},
		{"2 months", "2m", twoMonthSpan - tolerance, twoMonthSpan + tolerance},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slackLimit, oldestStr, latestStr, err := limitByExpression(tt.input, defaultConversationsExpressionLimit)
			if err != nil {
				t.Fatalf("expected no error for %q, got %v", tt.input, err)
			}
			if slackLimit != 100 {
				t.Errorf("expected slackLimit=100 for %q, got %d", tt.input, slackLimit)
			}

			o, err := strconv.ParseInt(strings.TrimSuffix(oldestStr, ".000000"), 10, 64)
			if err != nil {
				t.Fatalf("invalid oldest timestamp %q: %v", oldestStr, err)
			}
			l, err := strconv.ParseInt(strings.TrimSuffix(latestStr, ".000000"), 10, 64)
			if err != nil {
				t.Fatalf("invalid latest timestamp %q: %v", latestStr, err)
			}

			if l <= o {
				t.Errorf("for %q expected latest(%d) > oldest(%d)", tt.input, l, o)
			}
			diff := l - o
			if diff < tt.minSecs || diff >= tt.maxSecs {
				t.Errorf(
					"for %q expected span in [%d, %d), got %d",
					tt.input, tt.minSecs, tt.maxSecs, diff,
				)
			}
		})
	}
}

func TestUnitLimitByExpression_Invalid(t *testing.T) {
	invalid := []string{
		"d",   // too short
		"0d",  // zero
		"-1d", // negative
		"1x",  // bad suffix
		"1",   // missing suffix
		"01",  // no suffix + zero value
	}

	for _, input := range invalid {
		t.Run(input, func(t *testing.T) {
			_, _, _, err := limitByExpression(input, defaultConversationsExpressionLimit)
			if err == nil {
				t.Errorf("expected error for %q, got nil", input)
			}
		})
	}
}

func TestUnitSendStatus(t *testing.T) {
	newHandler := func(sendEnabled bool) *ConversationsHandler {
		return NewConversationsHandler(nil, zap.NewNop(), sendEnabled)
	}

	t.Run("not available when add_message not enabled", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		assert.Equal(t, "not available", newHandler(false).sendStatus("C123"))
	})

	t.Run("available when add_message enabled", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		assert.Equal(t, "available", newHandler(true).sendStatus("C123"))
	})

	t.Run("not available when channel not in allowlist", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C456,C789")
		assert.Equal(t, "not available for this channel", newHandler(true).sendStatus("C123"))
	})

	t.Run("available when channel in allowlist", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C123,C456")
		assert.Equal(t, "available", newHandler(true).sendStatus("C123"))
	})

	t.Run("not available when channel in blocklist", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "!C123")
		assert.Equal(t, "not available for this channel", newHandler(true).sendStatus("C123"))
	})

	t.Run("allowlist alone does not enable sending", func(t *testing.T) {
		t.Setenv("SLACK_MCP_ADD_MESSAGE_TOOL", "C123")
		assert.Equal(t, "not available", newHandler(false).sendStatus("C123"))
	})
}

func TestUnitFormatThreadTs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty returns top-level", "", "(top-level message)"},
		{"timestamp passes through", "1234567890.123456", "1234567890.123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatThreadTs(tt.input)
			if got != tt.want {
				t.Errorf("formatThreadTs(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestUnitIsSlackUserIDPrefix(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want bool
	}{
		{"U prefix", "U0123ABCD", true},
		{"W prefix", "W0123ABCD", true},
		{"plain name not ID", "alice", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSlackUserIDPrefix(tt.s)
			if got != tt.want {
				t.Errorf("isSlackUserIDPrefix(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

func TestUnitSplitQueryHasModifier(t *testing.T) {
	free, filters := splitQuery("quarterly report has:link from:@bob")
	assert.Equal(t, []string{"quarterly", "report"}, free)
	assert.Equal(t, []string{"link"}, filters["has"])
	assert.Equal(t, []string{"@bob"}, filters["from"])
}

func TestUnitBuildQueryEmitsHas(t *testing.T) {
	filters := map[string][]string{"has": {"link"}, "in": {"#general"}}
	q := buildQuery([]string{"report"}, filters)
	assert.Contains(t, q, "has:link")
	assert.Contains(t, q, "in:#general")
	assert.Contains(t, q, "report")
}

func TestUnitBuildQueryUnknownKeyStillDropped(t *testing.T) {
	// documents the invariant: keys outside the ordered list don't survive buildQuery
	q := buildQuery(nil, map[string][]string{"bogus": {"x"}})
	assert.Equal(t, "", q)
}

func compactCSVFixtureMessages() []Message {
	return []Message{
		{
			MsgID:         "1782935556.396379",
			UserID:        "U03BMAR2R50",
			UserName:      "robdezendorf",
			RealName:      "rob dezendorf",
			Channel:       "C039NRB81UL",
			ThreadTs:      "1782935556.396379",
			Text:          "hello",
			Time:          "2026-05-20T13:40:20Z",
			Permalink:     "https://loop.slack.com/archives/C039NRB81UL/p1782935556396379",
			Reactions:     "fire:1",
			AttachmentIDs: "F123 (deck.pdf)",
			HasMedia:      true,
		},
	}
}

func csvResultBody(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	var body string
	for _, content := range result.Content {
		if textContent, ok := content.(mcp.TextContent); ok {
			body = textContent.Text
			break
		}
	}
	require.NotEmpty(t, body)
	return body
}

func TestUnitMarshalMessagesToCompactCSV(t *testing.T) {
	result, err := marshalMessagesToCSV(compactCSVFixtureMessages(), renderOptions{mode: text.ModeStandard})
	require.NoError(t, err)
	require.NotNil(t, result)

	body := csvResultBody(t, result)

	assert.Contains(t, body, "MsgID")
	assert.Contains(t, body, "1782935556.396379")
	assert.Contains(t, body, "ThreadTs")
	assert.Contains(t, body, "F123 (deck.pdf)")
	assert.Contains(t, body, "Files")
	assert.Contains(t, body, "rob dezendorf")
	assert.NotContains(t, body, "Permalink")
	assert.NotContains(t, body, "U03BMAR2R50")
	assert.NotContains(t, body, "HasMedia")
}

func TestUnitMarshalMessagesToFullCSV(t *testing.T) {
	result, err := marshalMessagesToCSV(compactCSVFixtureMessages(), renderOptions{mode: text.ModeFull})
	require.NoError(t, err)
	require.NotNil(t, result)

	body := csvResultBody(t, result)

	assert.Contains(t, body, "Permalink")
	assert.Contains(t, body, "U03BMAR2R50")
	assert.False(t, strings.Contains(body, "\n#"), "full mode output should not contain legend comment lines")
}

func compactCSVFixtureMessagesN(n int) []Message {
	base := compactCSVFixtureMessages()[0]
	users := []struct {
		id, userName, realName string
	}{
		{"U03BMAR2R50", "robdezendorf", "rob dezendorf"},
		{"U04CNAS3S61", "alicew", "Alice Wonderland"},
	}
	messages := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		m := base
		m.MsgID = fmt.Sprintf("1782935556.%06d", 396379+i)
		if i%3 == 2 {
			// Every third message is a bot message, excluded from the legend.
			m.UserID = ""
			m.UserName = ""
			m.RealName = ""
			m.BotName = "GitHub"
		} else {
			u := users[i%2]
			m.UserID = u.id
			m.UserName = u.userName
			m.RealName = u.realName
			m.BotName = ""
		}
		messages = append(messages, m)
	}
	return messages
}

func TestUnitCompactCSVLegendHeader(t *testing.T) {
	messages := compactCSVFixtureMessagesN(4)
	result, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard, workspaceURL: "https://loop.slack.com/"})
	require.NoError(t, err)
	require.NotNil(t, result)

	body := csvResultBody(t, result)

	require.True(t, strings.HasPrefix(body, "#users:"), "body should start with #users: legend, got: %s", body)
	assert.Contains(t, body, "U03BMAR2R50=robdezendorf|rob dezendorf")
	assert.Contains(t, body, "U04CNAS3S61=alicew|Alice Wonderland")
	assert.Contains(t, body, "#link_template: https://loop.slack.com/archives/")

	lines := strings.Split(body, "\n")
	require.True(t, len(lines) >= 3)
	assert.True(t, strings.HasPrefix(lines[0], "#users:"))
	assert.NotContains(t, lines[0], "GitHub", "bot rows must be excluded from the #users: legend")
	assert.True(t, strings.HasPrefix(lines[1], "#link_template:"))
	assert.True(t, strings.HasPrefix(lines[2], "#attachments: "), "the fixture messages carry files: %s", lines[2])
	assert.Equal(t, "User,Channel,Text,Time,MsgID,ThreadTs,Reactions,AttachmentIDs,Files", lines[3])
}

func TestUnitCompactCSVChannelsLegendAndCursor(t *testing.T) {
	messages := compactCSVFixtureMessagesN(4)
	names := map[string]string{messages[0].Channel: "#general"}
	opts := renderOptions{
		mode:         text.ModeStandard,
		workspaceURL: "https://loop.slack.com/",
		channelName:  func(id string) string { return names[id] },
		meta:         SlackResultMeta("cursor-2", false, ""),
	}
	result, err := marshalMessagesToCSV(messages, opts)
	require.NoError(t, err)
	assert.Nil(t, result.StructuredContent)

	lines := strings.Split(csvResultBody(t, result), "\n")
	assert.Equal(t, "#channels: "+messages[0].Channel+"=#general", lines[0])
	assert.True(t, strings.HasPrefix(lines[1], "#users:"))
	assert.True(t, strings.HasPrefix(lines[2], "#link_template:"))
	assert.True(t, strings.HasPrefix(lines[3], "#attachments: "))
	assert.Equal(t, "#next_cursor: cursor-2", lines[4])
	assert.True(t, strings.HasPrefix(lines[5], "User,Channel,"))
}

func TestUnitFullCSVCarriesCursorLine(t *testing.T) {
	messages := compactCSVFixtureMessagesN(1)
	result, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeFull, meta: SlackResultMeta("c9", false, "")})
	require.NoError(t, err)
	body := csvResultBody(t, result)
	assert.True(t, strings.HasPrefix(body, "#next_cursor: c9\n"), body)
	assert.NotContains(t, body, "Cursor")
}

func TestUnitCompactCSVLegendSkippedForSmallResults(t *testing.T) {
	messages := compactCSVFixtureMessagesN(2)
	result, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard, workspaceURL: "https://loop.slack.com/"})
	require.NoError(t, err)
	require.NotNil(t, result)

	body := csvResultBody(t, result)

	assert.NotContains(t, body, "#users:")
	assert.NotContains(t, body, "#link_template:")
	assert.True(t, strings.HasPrefix(body, "#attachments: "), "files still need their recovery route on a small result: %s", body)

	noFiles := compactCSVFixtureMessagesN(2)
	for i := range noFiles {
		noFiles[i].AttachmentIDs = ""
	}
	bare, err := marshalMessagesToCSV(noFiles, renderOptions{mode: text.ModeStandard, workspaceURL: "https://loop.slack.com/"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(csvResultBody(t, bare), "User,Channel,Text,Time,MsgID"), "no files, no legend at all")
}

func TestUnitCompactCSVLegendNoWorkspace(t *testing.T) {
	messages := compactCSVFixtureMessagesN(4)
	result, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard, workspaceURL: ""})
	require.NoError(t, err)
	require.NotNil(t, result)

	body := csvResultBody(t, result)

	assert.Contains(t, body, "#users:")
	assert.NotContains(t, body, "#link_template:")
}

func TestUnitCompactCSVLegendDeterministic(t *testing.T) {
	messages := compactCSVFixtureMessagesN(6)

	result1, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard, workspaceURL: "https://loop.slack.com/"})
	require.NoError(t, err)
	body1 := csvResultBody(t, result1)

	result2, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard, workspaceURL: "https://loop.slack.com/"})
	require.NoError(t, err)
	body2 := csvResultBody(t, result2)

	assert.Equal(t, body1, body2)
}

// Documented 'D1234567890' filter_in_im_or_mpim is a conversation ID, not a user ID.
func TestUnitIsSlackConversationIDPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"D1234567890", true},
		{"G1234567890", true},
		{"U1234567890", false},
		{"W1234567890", false},
		{"C1234567890", false},
		{"@username_dm", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, isSlackConversationIDPrefix(tt.input))
		})
	}
}

func TestUnitFormatConversationFilter(t *testing.T) {
	cms := &provider.ChannelsCache{
		Channels: map[string]provider.Channel{
			"D1234567890": {ID: "D1234567890", Name: "@alice", IsIM: true, User: "U0000000001"},
			"D2222222222": {ID: "D2222222222", Name: "@bob", IsIM: true},
			"G1234567890": {ID: "G1234567890", Name: "@mpdm-alice--bob--carol", IsMpIM: true},
		},
		ChannelsInv: map[string]string{},
	}

	tests := []struct {
		name    string
		cms     *provider.ChannelsCache
		input   string
		want    string
		wantErr bool
	}{
		{"DM resolves to the peer user link", cms, "D1234567890", "<@U0000000001>", false},
		{"DM without a peer falls back to the cached name", cms, "D2222222222", "@bob", false},
		{"MPIM resolves to the cached name", cms, "G1234567890", "@mpdm-alice--bob--carol", false},
		{"unknown conversation errors", cms, "D9999999999", "", true},
		{"nil cache errors", nil, "D1234567890", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := formatConversationFilter(tt.cms, tt.input)
			if tt.wantErr {
				require.Error(t, err)
				// Never the misleading users-map error.
				assert.NotContains(t, err.Error(), "user ")
				assert.Contains(t, err.Error(), "not found in cache")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
