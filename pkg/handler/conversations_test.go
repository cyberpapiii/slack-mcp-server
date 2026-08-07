package handler

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/test/util"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIntegrationConversations(t *testing.T) {
	sseKey := uuid.New().String()
	require.NotEmpty(t, sseKey, "sseKey must be generated for integration tests")
	apiKey := os.Getenv("SLACK_MCP_OPENAI_API")
	require.NotEmpty(t, apiKey, "SLACK_MCP_OPENAI_API must be set for integration tests")

	cfg := util.MCPConfig{
		SSEKey:             sseKey,
		MessageToolEnabled: true,
		MessageToolMark:    true,
	}

	mcp, err := util.SetupMCP(cfg)
	if err != nil {
		t.Fatalf("Failed to set up MCP server: %v", err)
	}
	fwd, err := util.SetupForwarding(context.Background(), "http://"+mcp.Host+":"+strconv.Itoa(mcp.Port))
	if err != nil {
		t.Fatalf("Failed to set up ngrok forwarding: %v", err)
	}
	defer fwd.Shutdown()
	defer mcp.Shutdown()

	client := openai.NewClient(option.WithAPIKey(apiKey))
	ctx := context.Background()

	type matchingRule struct {
		csvFieldName    string
		csvFieldValueRE string
		RowPosition     *int
		TotalRows       *int
	}

	type tc struct {
		name                            string
		input                           string
		expectedToolName                string
		expectedToolOutputMatchingRules []matchingRule
		expectedLLMOutputMatchingRules  []string
	}

	cases := []tc{
		{
			name:             "Test conversations_history tool",
			input:            "Provide a list of slack messages from #testcase-1",
			expectedToolName: "conversations_history",
			expectedToolOutputMatchingRules: []matchingRule{
				{
					csvFieldName:    "Text",
					csvFieldValueRE: "^message 3$",
				},
				{
					csvFieldName:    "Text",
					csvFieldValueRE: "^message 2$",
				},
				{
					csvFieldName:    "Text",
					csvFieldValueRE: "^message 1$",
				},
			},
			expectedLLMOutputMatchingRules: []string{
				"message 1", "message 2", "message 3",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := responses.ResponseNewParams{
				Model: "gpt-4.1-mini",
				Tools: []responses.ToolUnionParam{
					{
						OfMcp: &responses.ToolMcpParam{
							ServerLabel: "slack-mcp-server",
							ServerURL:   fmt.Sprintf("%s://%s/sse", fwd.URL.Scheme, fwd.URL.Host),
							RequireApproval: responses.ToolMcpRequireApprovalUnionParam{
								OfMcpToolApprovalSetting: param.NewOpt("never"),
							},
							Headers: map[string]string{
								"Authorization": "Bearer " + sseKey,
							},
						},
					},
				},
				Input: responses.ResponseNewParamsInputUnion{
					OfString: openai.String(tc.input),
				},
			}

			resp, err := client.Responses.New(ctx, params)
			require.NoError(t, err, "API call failed")

			assert.NotNil(t, resp.Status, "completed")

			var llmOutput strings.Builder
			var toolOutput strings.Builder
			for _, out := range resp.Output {
				if out.Type == "message" && out.Role == "assistant" {
					for _, c := range out.Content {
						if c.Type == "output_text" {
							llmOutput.WriteString(c.Text)
						}
					}
				}
				if out.Type == "mcp_call" && out.Name == tc.expectedToolName {
					toolOutput.WriteString(out.Output)
				}
			}

			require.NotEmpty(t, toolOutput, "no tool output captured")

			// Parse CSV
			reader := csv.NewReader(strings.NewReader(toolOutput.String()))
			rows, err := reader.ReadAll()
			require.NoError(t, err, "failed to parse CSV")

			header := rows[0]
			dataRows := rows[1:]
			colIndex := map[string]int{}
			for i, col := range header {
				colIndex[col] = i
			}

			for _, rule := range tc.expectedToolOutputMatchingRules {
				if rule.TotalRows != nil && *rule.TotalRows > 0 {
					assert.Equalf(t, *rule.TotalRows, len(dataRows),
						"expected %d data rows, got %d", rule.TotalRows, len(dataRows))
				}

				idx, ok := colIndex[rule.csvFieldName]
				require.Truef(t, ok, "CSV did not contain column %q, toolOutput: %q", rule.csvFieldName, toolOutput.String())

				re, err := regexp.Compile(rule.csvFieldValueRE)
				require.NoErrorf(t, err, "invalid regex %q", rule.csvFieldValueRE)

				if rule.RowPosition != nil && *rule.RowPosition >= 0 {
					require.Lessf(t, rule.RowPosition, len(dataRows), "RowPosition %d out of range (only %d data rows)", rule.RowPosition, len(dataRows))
					value := dataRows[*rule.RowPosition][idx]
					assert.Regexpf(t, re, value, "row %d, column %q: expected to match %q, got %q",
						rule.RowPosition, rule.csvFieldName, rule.csvFieldValueRE, value)
					continue
				}

				found := false
				for _, row := range dataRows {
					if idx < len(row) && re.MatchString(row[idx]) {
						found = true
						break
					}
				}
				assert.Truef(t, found, "no row in column %q matched %q; full CSV:\n%s",
					rule.csvFieldName, rule.csvFieldValueRE, toolOutput.String())
			}

			for _, pattern := range tc.expectedLLMOutputMatchingRules {
				re, err := regexp.Compile(pattern)
				require.NoErrorf(t, err, "invalid LLM regex %q", pattern)
				assert.Regexpf(t, re, llmOutput.String(), "LLM output did not match regex %q; output:\n%s",
					pattern, llmOutput.String())
			}
		})
	}
}

func TestUnitParseFlexibleDate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantDate string
		wantErr  bool
	}{
		// Standard formats (existing)
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

		// New flexible month-year formats
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

			// Parse the "1234567890.000000" format back to an integer
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

func TestUnitIsChannelAllowedForConfig(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		config  string
		want    bool
	}{
		// Allow all cases
		{"empty config allows all", "C123", "", true},
		{"true allows all", "C123", "true", true},
		{"1 allows all", "C123", "1", true},

		// Allowlist (whitelist) cases
		{"allowlist - channel in list", "C123", "C123,C456", true},
		{"allowlist - second channel in list", "C456", "C123,C456", true},
		{"allowlist - channel NOT in list", "C789", "C123,C456", false},
		{"allowlist - with spaces", "C123", " C123 , C456 ", true},

		// Blocklist cases
		{"blocklist - channel in list", "C123", "!C123,!C456", false},
		{"blocklist - second channel in list", "C456", "!C123,!C456", false},
		{"blocklist - channel NOT in list", "C789", "!C123,!C456", true},
		{"blocklist - with spaces", "C123", " !C123 , !C456 ", false},

		// Single item cases
		{"single allowlist - match", "C123", "C123", true},
		{"single allowlist - no match", "C456", "C123", false},
		{"single blocklist - match", "C123", "!C123", false},
		{"single blocklist - no match", "C456", "!C123", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isChannelAllowedForConfig(tt.channel, tt.config)
			if got != tt.want {
				t.Errorf("isChannelAllowedForConfig(%q, %q) = %v, want %v",
					tt.channel, tt.config, got, tt.want)
			}
		})
	}
}

func TestUnitCheckSendStatus(t *testing.T) {
	setEnv := func(key, value string) func() {
		old, existed := os.LookupEnv(key)
		os.Setenv(key, value)
		return func() {
			if existed {
				os.Setenv(key, old)
			} else {
				os.Unsetenv(key)
			}
		}
	}

	t.Run("not available when add_message not enabled", func(t *testing.T) {
		cleanup1 := setEnv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		cleanup2 := setEnv("SLACK_MCP_ENABLED_TOOLS", "")
		defer cleanup1()
		defer cleanup2()

		got := checkSendStatus("C123")
		if got != "not available" {
			t.Errorf("expected 'not available', got %q", got)
		}
	})

	t.Run("available when add_message enabled via env var", func(t *testing.T) {
		cleanup1 := setEnv("SLACK_MCP_ADD_MESSAGE_TOOL", "true")
		cleanup2 := setEnv("SLACK_MCP_ENABLED_TOOLS", "")
		defer cleanup1()
		defer cleanup2()

		got := checkSendStatus("C123")
		if got != "available" {
			t.Errorf("expected 'available', got %q", got)
		}
	})

	t.Run("available when add_message in enabled tools list", func(t *testing.T) {
		cleanup1 := setEnv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		cleanup2 := setEnv("SLACK_MCP_ENABLED_TOOLS", "conversations_add_message,channels_list")
		defer cleanup1()
		defer cleanup2()

		got := checkSendStatus("C123")
		if got != "available" {
			t.Errorf("expected 'available', got %q", got)
		}
	})

	t.Run("not available when channel not in allowlist", func(t *testing.T) {
		cleanup1 := setEnv("SLACK_MCP_ADD_MESSAGE_TOOL", "C456,C789")
		cleanup2 := setEnv("SLACK_MCP_ENABLED_TOOLS", "")
		defer cleanup1()
		defer cleanup2()

		got := checkSendStatus("C123")
		if got != "not available for this channel" {
			t.Errorf("expected 'not available for this channel', got %q", got)
		}
	})

	t.Run("available when channel in allowlist", func(t *testing.T) {
		cleanup1 := setEnv("SLACK_MCP_ADD_MESSAGE_TOOL", "C123,C456")
		cleanup2 := setEnv("SLACK_MCP_ENABLED_TOOLS", "")
		defer cleanup1()
		defer cleanup2()

		got := checkSendStatus("C123")
		if got != "available" {
			t.Errorf("expected 'available', got %q", got)
		}
	})

	t.Run("not available when channel in blocklist", func(t *testing.T) {
		cleanup1 := setEnv("SLACK_MCP_ADD_MESSAGE_TOOL", "!C123")
		cleanup2 := setEnv("SLACK_MCP_ENABLED_TOOLS", "")
		defer cleanup1()
		defer cleanup2()

		got := checkSendStatus("C123")
		if got != "not available for this channel" {
			t.Errorf("expected 'not available for this channel', got %q", got)
		}
	})

	t.Run("not available when only draft tool in enabled list (substring false positive)", func(t *testing.T) {
		cleanup1 := setEnv("SLACK_MCP_ADD_MESSAGE_TOOL", "")
		cleanup2 := setEnv("SLACK_MCP_ENABLED_TOOLS", "conversations_draft_message,channels_list")
		defer cleanup1()
		defer cleanup2()

		got := checkSendStatus("C123")
		if got != "not available" {
			t.Errorf("expected 'not available' (draft tool name is superstring of add_message), got %q", got)
		}
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
			// Every third message is a bot message — excluded from the legend.
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
	assert.Equal(t, "User,Channel,Text,Time,MsgID,ThreadTs,Reactions,AttachmentIDs,Files,Cursor", lines[2])
}

func TestUnitCompactCSVLegendSkippedForSmallResults(t *testing.T) {
	messages := compactCSVFixtureMessagesN(2)
	result, err := marshalMessagesToCSV(messages, renderOptions{mode: text.ModeStandard, workspaceURL: "https://loop.slack.com/"})
	require.NoError(t, err)
	require.NotNil(t, result)

	body := csvResultBody(t, result)

	assert.True(t, strings.HasPrefix(body, "User,Channel,Text,Time,MsgID"), "body should start directly with the CSV header, got: %s", body)
	assert.NotContains(t, body, "#users:")
	assert.NotContains(t, body, "#link_template:")
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

// TestUnitIsToolInEnabledList covers plan 014: isToolInEnabledList must do an
// exact, comma-split match rather than a raw substring match, so that a
// longer tool name (or one sharing a prefix) present in the allowlist does
// not accidentally enable an unrelated tool.
func TestUnitIsToolInEnabledList(t *testing.T) {
	tests := []struct {
		name         string
		enabledTools string
		toolName     string
		want         bool
	}{
		{"empty list", "", "conversations_add_message", false},
		{"exact match single item", "conversations_add_message", "conversations_add_message", true},
		{"exact match among many", "channels_list,conversations_add_message,reactions_add", "conversations_add_message", true},
		{"no match", "channels_list,reactions_add", "conversations_add_message", false},
		{"whitespace padding around match", " conversations_add_message , channels_list ", "conversations_add_message", true},
		{
			name:         "substring collision - longer name in list must NOT enable shorter name",
			enabledTools: "conversations_add_message_v2",
			toolName:     "conversations_add_message",
			want:         false,
		},
		{
			name:         "substring collision - shorter name in list must NOT enable longer name",
			enabledTools: "conversations_add_message",
			toolName:     "conversations_add_message_v2",
			want:         false,
		},
		{"substring collision reactions_add vs reactions_add_extra", "reactions_add_extra", "reactions_add", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isToolInEnabledList(tt.enabledTools, tt.toolName)
			assert.Equal(t, tt.want, got, "isToolInEnabledList(%q, %q)", tt.enabledTools, tt.toolName)
		})
	}
}

// TestUnitRequireToolEnabled covers the shared call-time gate helper used by
// conversations_leave/join and the usergroups write handlers (plan 014):
// enabled either via the tool's dedicated env var or via an exact-match
// entry in SLACK_MCP_ENABLED_TOOLS.
func TestUnitRequireToolEnabled(t *testing.T) {
	const envVar = "SLACK_MCP_TEST_REQUIRE_TOOL_ENABLED"
	const toolName = "conversations_leave"

	t.Run("neither env var nor allowlist set - disabled", func(t *testing.T) {
		t.Setenv(envVar, "")
		t.Setenv("SLACK_MCP_ENABLED_TOOLS", "")

		assert.False(t, requireToolEnabled(envVar, toolName))
	})

	t.Run("env var set - enabled", func(t *testing.T) {
		t.Setenv(envVar, "true")
		t.Setenv("SLACK_MCP_ENABLED_TOOLS", "")

		assert.True(t, requireToolEnabled(envVar, toolName))
	})

	t.Run("tool named in allowlist - enabled without env var", func(t *testing.T) {
		t.Setenv(envVar, "")
		t.Setenv("SLACK_MCP_ENABLED_TOOLS", "channels_list,"+toolName)

		assert.True(t, requireToolEnabled(envVar, toolName))
	})

	t.Run("allowlist has substring-colliding name only - still disabled", func(t *testing.T) {
		t.Setenv(envVar, "")
		t.Setenv("SLACK_MCP_ENABLED_TOOLS", toolName+"_extra")

		assert.False(t, requireToolEnabled(envVar, toolName))
	})
}

// Bug A: the search tool schema declares DefaultNumber(20) and a documented
// 1..100 range; the parser must default and clamp to match.
func TestUnitParseParamsToolSearchLimit(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{"absent limit uses schema default", map[string]any{"search_query": "hello"}, 20},
		{"explicit zero uses schema default", map[string]any{"search_query": "hello", "limit": 0}, 20},
		{"negative uses schema default", map[string]any{"search_query": "hello", "limit": -5}, 20},
		{"above max clamps to 100", map[string]any{"search_query": "hello", "limit": 500}, 100},
		{"in-range value passes through", map[string]any{"search_query": "hello", "limit": 50}, 50},
		{"max value passes through", map[string]any{"search_query": "hello", "limit": 100}, 100},
		{"json float encoding clamps too", map[string]any{"search_query": "hello", "limit": float64(500)}, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tt.args

			params, err := ch.parseParamsToolSearch(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, tt.want, params.limit)
		})
	}
}

// Bug B: a documented 'D1234567890' filter_in_im_or_mpim value is a
// conversation ID, not a user ID, and must never yield "user ... not found".
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

// Bug D: the history schema advertises limit "1d" by default while forbidding
// a limit alongside 'cursor'; the cursor must win over the duration window.
func TestUnitParseParamsToolConversationsCursorBeatsDurationLimit(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	tests := []struct {
		name       string
		args       map[string]any
		wantLimit  int
		wantWindow bool
	}{
		{
			name:       "duration limit with cursor is ignored",
			args:       map[string]any{"channel_id": "C1234567890", "limit": "1d", "cursor": "abc"},
			wantLimit:  0,
			wantWindow: false,
		},
		{
			name:       "duration limit without cursor sets the window",
			args:       map[string]any{"channel_id": "C1234567890", "limit": "1d"},
			wantLimit:  100,
			wantWindow: true,
		},
		{
			name:       "numeric limit with cursor is ignored (existing behavior)",
			args:       map[string]any{"channel_id": "C1234567890", "limit": "50", "cursor": "abc"},
			wantLimit:  0,
			wantWindow: false,
		},
		{
			name:       "numeric limit without cursor is honored",
			args:       map[string]any{"channel_id": "C1234567890", "limit": "50"},
			wantLimit:  50,
			wantWindow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := mcp.CallToolRequest{}
			req.Params.Arguments = tt.args

			params, err := ch.parseParamsToolConversations(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLimit, params.limit)
			if tt.wantWindow {
				assert.NotEmpty(t, params.oldest)
				assert.NotEmpty(t, params.latest)
			} else {
				assert.Empty(t, params.oldest)
				assert.Empty(t, params.latest)
			}
		})
	}
}
