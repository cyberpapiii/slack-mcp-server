package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/korotovsky/slack-mcp-server/pkg/provider"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge"
	"github.com/korotovsky/slack-mcp-server/pkg/provider/edge/fasttime"
	"github.com/korotovsky/slack-mcp-server/pkg/test/util"
	"github.com/korotovsky/slack-mcp-server/pkg/text"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/slack-go/slack"
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
		{"TRUE allows all", "C123", "TRUE", true},
		{"yes allows all", "C123", "yes", true},
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

		// Invalid configs fail closed (startup validateToolConfig should reject)
		{"bare bang fails closed", "C123", "!", false},
		{"empty tokens fail closed", "C123", ",,", false},
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

func TestUnitFileResultPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload fileResultPayload
	}{
		{
			name: "filename with quotes backslash and ANSI escape",
			payload: fileResultPayload{
				FileID:   `F0123"ABC\`,
				Filename: "we\x1bird \"name\"\\path.txt",
				Mimetype: "text/plain",
				Size:     12,
				Encoding: "none",
				Content:  "hello",
			},
		},
		{
			name: "content with NUL and ANSI escapes",
			payload: fileResultPayload{
				FileID:   "F0123ABC",
				Filename: "app.log",
				Mimetype: "text/plain",
				Size:     42,
				Encoding: "none",
				Content:  "\x00start\x1b[31mred\x1b[0m\v\f end ",
			},
		},
		{
			name: "base64 encoding preserved",
			payload: fileResultPayload{
				FileID:   "F0999ZZZ",
				Filename: "blob.bin",
				Mimetype: "application/octet-stream",
				Size:     3,
				Encoding: "base64",
				Content:  "AAEC",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := marshalFileResult(tt.payload)
			require.NoError(t, err)
			assert.True(t, json.Valid([]byte(out)), "output must be valid JSON: %q", out)

			var got fileResultPayload
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			assert.Equal(t, tt.payload, got, "payload must round-trip exactly")
		})
	}
}

func TestUnitFileResultShapes(t *testing.T) {
	keysOf := func(t *testing.T, s string) []string {
		t.Helper()
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(s), &m))
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	t.Run("image metadata emits exactly four keys", func(t *testing.T) {
		out, err := marshalFileMetadata(fileMetadataPayload{
			FileID:   "F0123ABC",
			Filename: "pic.png",
			Mimetype: "image/png",
			Size:     7,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"file_id", "filename", "mimetype", "size"}, keysOf(t, out))
	})

	t.Run("text result emits exactly six keys with encoding none", func(t *testing.T) {
		out, err := marshalFileResult(fileResultPayload{
			FileID:   "F0123ABC",
			Filename: "notes.txt",
			Mimetype: "text/plain",
			Size:     0,
			Encoding: "none",
			Content:  "",
		})
		require.NoError(t, err)
		assert.Equal(t,
			[]string{"content", "encoding", "file_id", "filename", "mimetype", "size"},
			keysOf(t, out))

		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &m))
		assert.Equal(t, "none", m["encoding"], "encoding must survive even when \"none\"")
		assert.Equal(t, "", m["content"], "content key must be present even when empty")
	})
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

func TestUnitSearchSortValidation(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"search_query": "hello", "sort": "bogus"}
	_, err := ch.parseParamsToolSearch(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sort")
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

// Exact comma-split match; no substring enable.
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

// Env truthy (true/1/yes) or exact SLACK_MCP_ENABLED_TOOLS entry.
func TestUnitRequireToolEnabled(t *testing.T) {
	const envVar = "SLACK_MCP_TEST_REQUIRE_TOOL_ENABLED"
	const toolName = "conversations_leave"

	tests := []struct {
		name         string
		envValue     string
		enabledTools string
		want         bool
	}{
		{"neither env var nor allowlist set - disabled", "", "", false},
		{"env var true - enabled", "true", "", true},
		{"env var 1 - enabled", "1", "", true},
		{"env var yes - enabled", "yes", "", true},
		{"env var TRUE - case insensitive, enabled", "TRUE", "", true},
		{"env var padded true - whitespace tolerant, enabled", "  true  ", "", true},
		{"env var false - disabled", "false", "", false},
		{"env var 0 - disabled", "0", "", false},
		{"env var no - disabled", "no", "", false},
		{"env var off - disabled", "off", "", false},
		{"env var empty - disabled", "", "", false},
		{"env var maybe - disabled", "maybe", "", false},
		{"tool named in allowlist - enabled without env var", "", "channels_list," + toolName, true},
		{"allowlist wins over env var false", "false", "channels_list," + toolName, true},
		{"allowlist has substring-colliding name only - still disabled", "", toolName + "_extra", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envVar, tt.envValue)
			t.Setenv("SLACK_MCP_ENABLED_TOOLS", tt.enabledTools)

			assert.Equal(t, tt.want, requireToolEnabled(envVar, toolName),
				"requireToolEnabled with %s=%q, SLACK_MCP_ENABLED_TOOLS=%q",
				envVar, tt.envValue, tt.enabledTools)
		})
	}
}

// Search schema DefaultNumber(100) / 1..100 range: parser must default and clamp to match.
func TestUnitParseParamsToolSearchLimit(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{"absent limit uses schema default", map[string]any{"search_query": "hello"}, 100},
		{"explicit zero uses schema default", map[string]any{"search_query": "hello", "limit": 0}, 100},
		{"negative uses schema default", map[string]any{"search_query": "hello", "limit": -5}, 100},
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

// History default limit "1d" is forbidden with cursor; cursor must win over the duration window.
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

// newUnreadsTestHandler builds a handler with no apiProvider.
func newUnreadsTestHandler() *ConversationsHandler {
	return &ConversationsHandler{logger: zap.NewNop()}
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
			{ChannelID: "C_busy", ChannelType: "internal", UnreadCount: 99, Latest: "1800000000.000000"},
			{ChannelID: "D_quiet", ChannelType: "dm", UnreadCount: 0, Latest: "1000000000.000000"},
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
	ch := newUnreadsTestHandler()

	// gocsv uses Go field names, not json tags.
	const header = "ChannelID,ChannelName,ChannelType,UnreadCount,LastRead,Latest\n"

	t.Run("three channels, comma in a name is quoted", func(t *testing.T) {
		channels := []UnreadChannel{
			{
				ChannelID:   "D111",
				ChannelName: "@alice",
				ChannelType: "dm",
				UnreadCount: 2,
				LastRead:    "1700000000.000100",
				Latest:      "1700000900.000200",
			},
			{
				ChannelID:   "C222",
				ChannelName: "#eng, product",
				ChannelType: "partner",
				UnreadCount: 7,
				LastRead:    "1700000100.000000",
				Latest:      "1700000800.000000",
			},
			{
				ChannelID:   "C333",
				ChannelName: "#general",
				ChannelType: "internal",
				UnreadCount: 0,
				LastRead:    "",
				Latest:      "",
			},
		}

		res, err := ch.marshalUnreadChannelsToCSV(channels)
		require.NoError(t, err)
		require.Len(t, res.Content, 1)
		got := res.Content[0].(mcp.TextContent).Text
		structured, ok := res.StructuredContent.(ToolResult[UnreadPageData])
		require.True(t, ok)
		require.NotNil(t, structured.Data)
		assert.Equal(t, channels, structured.Data.Channels)
		assert.Equal(t, TrustUntrusted, structured.Meta.Provenance.Trust)

		assert.Equal(t, header+
			"D111,@alice,dm,2,1700000000.000100,1700000900.000200\n"+
			"C222,\"#eng, product\",partner,7,1700000100.000000,1700000800.000000\n"+
			"C333,#general,internal,0,,\n",
			got)
	})

	t.Run("empty and nil slices still emit the header row", func(t *testing.T) {
		for _, channels := range [][]UnreadChannel{nil, {}} {
			res, err := ch.marshalUnreadChannelsToCSV(channels)
			require.NoError(t, err)
			require.Len(t, res.Content, 1)
			assert.Equal(t, header, res.Content[0].(mcp.TextContent).Text)
		}
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
		got := ch.collectUnreadChannels(defaultUnreadsParams(), counts(), users, channelsCache)

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
		assert.Equal(t, "1700000900.000200", byID["C_MENTION"].Latest)
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
		got := ch.collectUnreadChannels(defaultUnreadsParams(), c, users, channelsCache)
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
		got := ch.collectUnreadChannels(defaultUnreadsParams(), c, users, channelsCache)
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
		got := ch.collectUnreadChannels(p, counts(), users, channelsCache)
		assert.Equal(t, []string{"C_SILENT"}, unreadIDs(got))

		// collect only reads mutedChannels; include_muted is upstream.
		p2 := defaultUnreadsParams()
		p2.includeMuted = true
		p2.mutedChannels = map[string]bool{"C_MENTION": true}
		got2 := ch.collectUnreadChannels(p2, counts(), users, channelsCache)
		assert.Equal(t, []string{"C_SILENT"}, unreadIDs(got2),
			"include_muted alone does not undo an already-populated mutedChannels map")
	})

	t.Run("mentions_only drops zero-mention channels", func(t *testing.T) {
		p := defaultUnreadsParams()
		p.mentionsOnly = true
		c := counts()
		c.MPIMs = []edge.ChannelSnapshot{{ID: "G_MPIM", HasUnreads: true, MentionCount: 0}}
		c.IMs = []edge.ChannelSnapshot{{ID: "D_ALICE", HasUnreads: true, MentionCount: 1}}

		got := ch.collectUnreadChannels(p, c, users, channelsCache)
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
				got := ch.collectUnreadChannels(p, c, users, channelsCache)
				assert.ElementsMatch(t, tc.wantIDs, unreadIDs(got))
			})
		}

		t.Run("all", func(t *testing.T) {
			p := defaultUnreadsParams()
			got := ch.collectUnreadChannels(p, c, users, channelsCache)
			require.Len(t, got, 5)
			assert.Equal(t, []string{"D_ALICE", "G_MPIM", "C_EXT", "C_MENTION", "C_SILENT"}, unreadIDs(got))
		})
	})

	t.Run("max_channels truncates after sorting", func(t *testing.T) {
		c := counts()
		c.IMs = []edge.ChannelSnapshot{{ID: "D_ALICE", HasUnreads: true}}
		p := defaultUnreadsParams()
		p.maxChannels = 2

		got := ch.collectUnreadChannels(p, c, users, channelsCache)
		require.Len(t, got, 2)
		assert.Equal(t, "D_ALICE", got[0].ChannelID, "the DM survives truncation because sorting runs first")
	})

	t.Run("a non-positive max_channels does not panic and truncates nothing", func(t *testing.T) {
		c := counts()
		c.IMs = []edge.ChannelSnapshot{{ID: "D_ALICE", HasUnreads: true}}

		for _, maxChannels := range []int{-1, 0} {
			p := defaultUnreadsParams()
			p.maxChannels = maxChannels

			got := ch.collectUnreadChannels(p, c, users, channelsCache)
			assert.ElementsMatch(t, []string{"D_ALICE", "C_MENTION", "C_SILENT"}, unreadIDs(got),
				"a non-positive limit slices with a negative bound unless guarded; "+
					"the guard skips truncation entirely rather than returning nothing")
		}
	})

	t.Run("nothing unread yields a nil slice", func(t *testing.T) {
		got := ch.collectUnreadChannels(defaultUnreadsParams(), edge.ClientCountsResponse{}, users, channelsCache)
		assert.Nil(t, got)
	})

	t.Run("a snapshot with no last_read renders an empty string", func(t *testing.T) {
		// Zero fasttime -> "" via slackTS (not year-1 SlackString).
		c := edge.ClientCountsResponse{
			Channels: []edge.ChannelSnapshot{{ID: "C_SILENT", HasUnreads: true}},
		}
		got := ch.collectUnreadChannels(defaultUnreadsParams(), c, users, channelsCache)
		require.Len(t, got, 1)
		assert.Equal(t, "", got[0].LastRead)
		assert.Equal(t, "", got[0].Latest)
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

	channels := ch.collectUnreadChannels(p, counts, users, channelsCache)
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

func TestUnitConversationsGetMessageParamValidation(t *testing.T) {
	ch := &ConversationsHandler{logger: zap.NewNop()}

	// missing channel_id
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"timestamp": "1234567890.123456"}
	_, err := ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "channel_id")

	// missing timestamp
	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"channel_id": "C0123456789"}
	_, err = ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp")

	// invalid detail value
	req = mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"channel_id": "C0123456789", "timestamp": "1234567890.123456", "detail": "bogus"}
	_, err = ch.ConversationsGetMessageHandler(context.Background(), req)
	require.Error(t, err)
}
