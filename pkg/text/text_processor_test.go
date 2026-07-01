package text

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/slack-go/slack"
)

func TestBlocksToText(t *testing.T) {
	tests := []struct {
		name   string
		blocks slack.Blocks
		want   string
	}{
		{
			name:   "empty blocks",
			blocks: slack.Blocks{},
			want:   "",
		},
		{
			name: "header block",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.HeaderBlock{
						Type: slack.MBTHeader,
						Text: &slack.TextBlockObject{
							Type: "plain_text",
							Text: "Important Header",
						},
					},
				},
			},
			want: "Important Header",
		},
		{
			name: "section block with text",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.SectionBlock{
						Type: slack.MBTSection,
						Text: &slack.TextBlockObject{
							Type: "mrkdwn",
							Text: "Hello from section",
						},
					},
				},
			},
			want: "Hello from section",
		},
		{
			name: "section block with fields",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.SectionBlock{
						Type: slack.MBTSection,
						Text: &slack.TextBlockObject{
							Type: "mrkdwn",
							Text: "Main text",
						},
						Fields: []*slack.TextBlockObject{
							{Type: "mrkdwn", Text: "Field 1"},
							{Type: "mrkdwn", Text: "Field 2"},
						},
					},
				},
			},
			want: "Main text\nField 1\nField 2",
		},
		{
			name: "context block",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.ContextBlock{
						Type: slack.MBTContext,
						ContextElements: slack.ContextElements{
							Elements: []slack.MixedElement{
								&slack.TextBlockObject{
									Type: "mrkdwn",
									Text: "context info",
								},
							},
						},
					},
				},
			},
			want: "context info",
		},
		{
			name: "rich text block with text elements",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.RichTextBlock{
						Type: slack.MBTRichText,
						Elements: []slack.RichTextElement{
							&slack.RichTextSection{
								Type: slack.RTESection,
								Elements: []slack.RichTextSectionElement{
									&slack.RichTextSectionTextElement{
										Type: slack.RTSEText,
										Text: "Hello ",
									},
									&slack.RichTextSectionTextElement{
										Type: slack.RTSEText,
										Text: "World",
									},
								},
							},
						},
					},
				},
			},
			want: "Hello World",
		},
		{
			name: "rich text block with link element",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.RichTextBlock{
						Type: slack.MBTRichText,
						Elements: []slack.RichTextElement{
							&slack.RichTextSection{
								Type: slack.RTESection,
								Elements: []slack.RichTextSectionElement{
									&slack.RichTextSectionTextElement{
										Type: slack.RTSEText,
										Text: "Click ",
									},
									&slack.RichTextSectionLinkElement{
										Type: slack.RTSELink,
										URL:  "https://example.com",
										Text: "here",
									},
								},
							},
						},
					},
				},
			},
			want: "Click here (https://example.com)",
		},
		{
			name: "rich text link without display text falls back to URL",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.RichTextBlock{
						Type: slack.MBTRichText,
						Elements: []slack.RichTextElement{
							&slack.RichTextSection{
								Type: slack.RTESection,
								Elements: []slack.RichTextSectionElement{
									&slack.RichTextSectionLinkElement{
										Type: slack.RTSELink,
										URL:  "https://example.com",
									},
								},
							},
						},
					},
				},
			},
			want: "https://example.com",
		},
		{
			name: "multiple blocks combined",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.HeaderBlock{
						Type: slack.MBTHeader,
						Text: &slack.TextBlockObject{
							Type: "plain_text",
							Text: "Alert",
						},
					},
					&slack.SectionBlock{
						Type: slack.MBTSection,
						Text: &slack.TextBlockObject{
							Type: "mrkdwn",
							Text: "Server is down",
						},
					},
					&slack.ContextBlock{
						Type: slack.MBTContext,
						ContextElements: slack.ContextElements{
							Elements: []slack.MixedElement{
								&slack.TextBlockObject{
									Type: "plain_text",
									Text: "sent by monitoring",
								},
							},
						},
					},
				},
			},
			want: "Alert\nServer is down\nsent by monitoring",
		},
		{
			name: "divider and image blocks are skipped",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.SectionBlock{
						Type: slack.MBTSection,
						Text: &slack.TextBlockObject{
							Type: "mrkdwn",
							Text: "visible text",
						},
					},
					&slack.DividerBlock{
						Type: slack.MBTDivider,
					},
				},
			},
			want: "visible text",
		},
		{
			name: "rich text with broadcast element",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.RichTextBlock{
						Type: slack.MBTRichText,
						Elements: []slack.RichTextElement{
							&slack.RichTextSection{
								Type: slack.RTESection,
								Elements: []slack.RichTextSectionElement{
									&slack.RichTextSectionBroadcastElement{
										Type:  slack.RTSEBroadcast,
										Range: "channel",
									},
									&slack.RichTextSectionTextElement{
										Type: slack.RTSEText,
										Text: " please review",
									},
								},
							},
						},
					},
				},
			},
			want: "@channel please review",
		},
		{
			name: "section block with nil text",
			blocks: slack.Blocks{
				BlockSet: []slack.Block{
					&slack.SectionBlock{
						Type: slack.MBTSection,
						Fields: []*slack.TextBlockObject{
							{Type: "mrkdwn", Text: "Only fields"},
						},
					},
				},
			},
			want: "Only fields",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BlocksToText(tt.blocks)
			if got != tt.want {
				t.Errorf("BlocksToText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnitResolveOutputMode(t *testing.T) {
	tests := []struct {
		name    string
		detail  string
		env     string // value for SLACK_MCP_COMPACT_OUTPUT; "" leaves it unset/empty
		want    OutputMode
		wantErr bool
	}{
		{name: "empty_detail_env_unset", detail: "", env: "", want: ModeStandard},
		{name: "empty_detail_env_false", detail: "", env: "false", want: ModeFull},
		{name: "standard", detail: "standard", env: "", want: ModeStandard},
		{name: "full", detail: "full", env: "", want: ModeFull},
		{name: "full_case_insensitive", detail: "FULL", env: "", want: ModeFull},
		{name: "bogus", detail: "bogus", env: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SLACK_MCP_COMPACT_OUTPUT", tt.env)
			got, err := ResolveOutputMode(tt.detail)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveOutputMode(%q) expected error, got nil", tt.detail)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveOutputMode(%q) unexpected error: %v", tt.detail, err)
			}
			if got != tt.want {
				t.Errorf("ResolveOutputMode(%q) = %v, want %v", tt.detail, got, tt.want)
			}
		})
	}
}

func TestUnitAttachmentToTextModeSelection(t *testing.T) {
	// The body is longer than attachmentCompactBudget so the two modes must
	// diverge: standard truncates with a receipt, full renders everything.
	longBody := strings.Repeat("the long body text ", 20) + "UNIQUE-TAIL-MARKER"
	att := slack.Attachment{
		Title:     "Release notes",
		TitleLink: "https://example.com/notes",
		Text:      longBody,
	}

	standard := AttachmentToText(att, ModeStandard)
	if !strings.Contains(standard, "Release notes") {
		t.Errorf("ModeStandard output %q should contain the title", standard)
	}
	if !strings.Contains(standard, "https://example.com/notes") {
		t.Errorf("ModeStandard output %q should contain the link", standard)
	}
	if !strings.Contains(standard, attachmentTruncationReceipt) {
		t.Errorf("ModeStandard output %q should contain the truncation receipt", standard)
	}
	if strings.Contains(standard, "UNIQUE-TAIL-MARKER") {
		t.Errorf("ModeStandard output %q should not contain the tail of the long body", standard)
	}

	full := AttachmentToText(att, ModeFull)
	if !strings.Contains(full, "UNIQUE-TAIL-MARKER") {
		t.Errorf("ModeFull output %q should contain the full body text", full)
	}
	if strings.Contains(full, attachmentTruncationReceipt) {
		t.Errorf("ModeFull output %q should not contain the truncation receipt", full)
	}
}

func TestAttachmentToTextWithBlocks(t *testing.T) {
	att := slack.Attachment{
		Blocks: slack.Blocks{
			BlockSet: []slack.Block{
				&slack.SectionBlock{
					Type: slack.MBTSection,
					Text: &slack.TextBlockObject{
						Type: "mrkdwn",
						Text: "block content",
					},
				},
			},
		},
	}

	result := AttachmentToText(att, ModeFull)
	if result != "Blocks: block content" {
		t.Errorf("AttachmentToText() = %q, want %q", result, "Blocks: block content")
	}
}

func TestAttachmentToTextWithBlocksAndText(t *testing.T) {
	att := slack.Attachment{
		Title: "Email Subject",
		Text:  "email body",
		Blocks: slack.Blocks{
			BlockSet: []slack.Block{
				&slack.SectionBlock{
					Type: slack.MBTSection,
					Text: &slack.TextBlockObject{
						Type: "mrkdwn",
						Text: "extra block info",
					},
				},
			},
		},
	}

	result := AttachmentToText(att, ModeFull)
	expected := "Title: Email Subject; Text: email body; Blocks: extra block info"
	if result != expected {
		t.Errorf("AttachmentToText() = %q, want %q", result, expected)
	}
}

func TestAttachmentToText(t *testing.T) {
	tests := []struct {
		name string
		att  slack.Attachment
		want string
	}{
		{
			name: "fields_only",
			att: slack.Attachment{
				Fields: []slack.AttachmentField{
					{Title: "Env", Value: "production"},
				},
			},
			want: "Env: production",
		},
		{
			name: "multiple_fields",
			att: slack.Attachment{
				Fields: []slack.AttachmentField{
					{Title: "Workflow", Value: "remy-abtest"},
					{Title: "Env", Value: "production"},
				},
			},
			want: "Workflow: remy-abtest; Env: production",
		},
		{
			name: "fields_with_text",
			att: slack.Attachment{
				Text: "deploy started",
				Fields: []slack.AttachmentField{
					{Title: "Env", Value: "production"},
				},
			},
			want: "Text: deploy started; Env: production",
		},
		{
			name: "fields_empty_slice",
			att: slack.Attachment{
				Fields: []slack.AttachmentField{},
			},
			want: "",
		},
		{
			name: "field_title_only",
			att: slack.Attachment{
				Fields: []slack.AttachmentField{
					{Title: "Status", Value: ""},
				},
			},
			want: "Status",
		},
		{
			name: "field_value_only",
			att: slack.Attachment{
				Fields: []slack.AttachmentField{
					{Title: "", Value: "running"},
				},
			},
			want: "running",
		},
		{
			name: "fields_with_all_parts",
			att: slack.Attachment{
				Title: "Alert",
				Text:  "Failed workflow",
				Fields: []slack.AttachmentField{
					{Title: "Env", Value: "prod"},
					{Title: "Version", Value: "v1.9.0"},
				},
				Footer: "remy-worker",
				Ts:     "1775138400",
			},
			want: "Title: Alert; Text: Failed workflow; Env: prod; Version: v1.9.0; Footer: remy-worker @ 2026-04-02T14:00:00Z",
		},
		{
			name: "field_value_with_newline",
			att: slack.Attachment{
				Fields: []slack.AttachmentField{
					{Title: "Templates", Value: "step1\nstep2"},
				},
			},
			want: "Templates: step1 step2",
		},
		{
			name: "field_both_empty",
			att: slack.Attachment{
				Fields: []slack.AttachmentField{
					{Title: "", Value: ""},
				},
			},
			want: "",
		},
		{
			name: "title_with_link",
			att: slack.Attachment{
				Title:     "Error details",
				TitleLink: "https://sentry.io/issues/123",
			},
			want: "Title: [Error details](https://sentry.io/issues/123)",
		},
		{
			name: "title_without_link",
			att: slack.Attachment{
				Title: "Plain title",
			},
			want: "Title: Plain title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AttachmentToText(tt.att, ModeFull)
			if got != tt.want {
				t.Errorf("AttachmentToText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAttachmentToTextCompact(t *testing.T) {
	// Env-driven fallback: empty detail param + compact env var resolves to ModeStandard.
	t.Setenv("SLACK_MCP_COMPACT_OUTPUT", "true")
	mode, err := ResolveOutputMode("")
	if err != nil {
		t.Fatalf("ResolveOutputMode() error = %v", err)
	}
	if mode != ModeStandard {
		t.Fatalf("ResolveOutputMode(\"\") = %v, want ModeStandard", mode)
	}

	got := AttachmentToText(slack.Attachment{
		Title:     "Free Ideas",
		TitleLink: "https://loop.substack.com/",
		Text:      "Short preview body that fits the standard-mode budget.",
		Footer:    "Substack",
	}, mode)
	// Standard mode renders title+link then body text (it fits the budget);
	// footer is not part of the standard-mode priority list and stays absent.
	want := "Free Ideas (https://loop.substack.com/); Short preview body that fits the standard-mode budget."
	if got != want {
		t.Errorf("AttachmentToText() = %q, want %q", got, want)
	}
}

func TestUnitAttachmentCompactIncludesFields(t *testing.T) {
	att := slack.Attachment{
		Title:     "Alert fired",
		TitleLink: "https://alerts.example.com/123",
		Fields: []slack.AttachmentField{
			{Title: "sev", Value: "P1"},
			{Title: "svc", Value: "api-gw"},
		},
	}

	got := AttachmentToText(att, ModeStandard)
	for _, part := range []string{"Alert fired", "https://alerts.example.com/123", "sev: P1", "svc: api-gw"} {
		if !strings.Contains(got, part) {
			t.Errorf("ModeStandard output %q should contain %q", got, part)
		}
	}
	if strings.Contains(got, attachmentTruncationReceipt) {
		t.Errorf("ModeStandard output %q should not contain the truncation receipt", got)
	}
}

func TestUnitAttachmentCompactTruncatesWithReceipt(t *testing.T) {
	att := slack.Attachment{
		Title:     "Deploy failed",
		TitleLink: "https://ci.example.com/build/42",
		Fields: []slack.AttachmentField{
			{Title: "env", Value: "production"},
			{Title: "log", Value: strings.Repeat("error line ", 20)},
		},
		Text: strings.Repeat("stack trace frame ", 30) + "FINAL-FRAME",
	}

	got := AttachmentToText(att, ModeStandard)
	if !strings.HasSuffix(got, attachmentTruncationReceipt) {
		t.Fatalf("ModeStandard output %q should end with the truncation receipt", got)
	}
	if !strings.Contains(got, "env: production") {
		t.Errorf("ModeStandard output %q should contain the first field (priority order)", got)
	}
	body := strings.TrimSuffix(got, attachmentTruncationReceipt)
	if len(body) > attachmentCompactBudget {
		t.Errorf("ModeStandard output body length = %d, want <= %d", len(body), attachmentCompactBudget)
	}

	// Full mode remains the untouched recovery path: everything renders, no receipt.
	full := AttachmentToText(att, ModeFull)
	if !strings.Contains(full, "FINAL-FRAME") {
		t.Errorf("ModeFull output %q should contain the full body text", full)
	}
	if strings.Contains(full, attachmentTruncationReceipt) {
		t.Errorf("ModeFull output %q should not contain the truncation receipt", full)
	}
}

func TestUnitAttachmentCompactNoReceiptWhenFits(t *testing.T) {
	att := slack.Attachment{
		Title:     "Build passed",
		TitleLink: "https://ci.example.com/build/43",
		Fields: []slack.AttachmentField{
			{Title: "env", Value: "staging"},
		},
		Text: "All 128 tests green.",
	}

	got := AttachmentToText(att, ModeStandard)
	if strings.Contains(got, attachmentTruncationReceipt) {
		t.Errorf("ModeStandard output %q should not contain the truncation receipt", got)
	}
	if !strings.Contains(got, "All 128 tests green.") {
		t.Errorf("ModeStandard output %q should contain the body text", got)
	}
}

func TestUnitAttachmentCompactTitleOnlyUnchanged(t *testing.T) {
	att := slack.Attachment{
		Title:     "Title",
		TitleLink: "https://link",
	}

	got := AttachmentToText(att, ModeStandard)
	want := "Title (https://link)"
	if got != want {
		t.Errorf("AttachmentToText() = %q, want %q", got, want)
	}
}

func TestUnitAttachmentCompactOversizedTitle(t *testing.T) {
	att := slack.Attachment{
		Title: strings.Repeat("A", 400),
	}

	got := AttachmentToText(att, ModeStandard)
	if !strings.HasSuffix(got, attachmentTruncationReceipt) {
		t.Fatalf("ModeStandard output %q should end with the truncation receipt", got)
	}
	body := strings.TrimSuffix(got, attachmentTruncationReceipt)
	want := strings.Repeat("A", attachmentCompactBudget)
	if body != want {
		t.Errorf("ModeStandard output body = %q (len %d), want %d-char hard-cut title", body, len(body), attachmentCompactBudget)
	}
}

func TestUnitAttachmentCompactOversizedTitleMultibyte(t *testing.T) {
	// "é" is 2 bytes in UTF-8; 200 of them put a rune straddling byte 300,
	// so a naive byte-slice cut would produce invalid UTF-8.
	att := slack.Attachment{
		Title: strings.Repeat("é", 200),
	}

	got := AttachmentToText(att, ModeStandard)
	if !strings.HasSuffix(got, attachmentTruncationReceipt) {
		t.Fatalf("ModeStandard output %q should end with the truncation receipt", got)
	}
	body := strings.TrimSuffix(got, attachmentTruncationReceipt)
	if !utf8.ValidString(body) {
		t.Errorf("ModeStandard output body is not valid UTF-8 after hard cut: %q", body)
	}
	if len(body) > attachmentCompactBudget {
		t.Errorf("ModeStandard output body length = %d, want <= %d", len(body), attachmentCompactBudget)
	}
}

func TestAttachmentsTo2CSV(t *testing.T) {
	tests := []struct {
		name        string
		msgText     string
		attachments []slack.Attachment
		want        string
	}{
		{
			name:        "empty_attachments",
			msgText:     "",
			attachments: []slack.Attachment{},
			want:        "",
		},
		{
			name:    "single_no_msgtext",
			msgText: "",
			attachments: []slack.Attachment{
				{Title: "hello"},
			},
			want: "Title: hello",
		},
		{
			name:    "single_with_msgtext",
			msgText: "hi",
			attachments: []slack.Attachment{
				{Title: "hello"},
			},
			want: ". Title: hello",
		},
		{
			name:    "multiple_attachments",
			msgText: "",
			attachments: []slack.Attachment{
				{Title: "first"},
				{Title: "second"},
			},
			want: "Title: first, Title: second",
		},
		{
			name:    "attachment_with_fields",
			msgText: "",
			attachments: []slack.Attachment{
				{
					Fields: []slack.AttachmentField{
						{Title: "Env", Value: "prod"},
					},
				},
			},
			want: "Env: prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AttachmentsTo2CSV(tt.msgText, tt.attachments, ModeFull)
			if got != tt.want {
				t.Errorf("AttachmentsTo2CSV() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsUnfurlingEnabled(t *testing.T) {
	tests := []struct {
		name string
		opt  string
		text string
		want bool
	}{
		{
			name: "no domains",
			opt:  "example.com,foo.io",
			text: "Hello world, no domains here.",
			want: true,
		},
		{
			name: "allowed URL",
			opt:  "example.com,foo.io",
			text: "Check this link: http://example.com/page",
			want: true,
		},
		{
			name: "disallowed URL",
			opt:  "example.com,foo.io",
			text: "Visit http://bad.com now",
			want: false,
		},
		{
			name: "allowed bare domain",
			opt:  "example.com,foo.io",
			text: "Visit example.com for info.",
			want: true,
		},
		{
			name: "disallowed bare domain",
			opt:  "example.com,foo.io",
			text: "Visit bad.com for info.",
			want: false,
		},
		{
			name: "multiple allowed mixed",
			opt:  "example.com,foo.io",
			text: "example.com and foo.io and https://example.com/test",
			want: true,
		},
		{
			name: "one disallowed among many",
			opt:  "example.com,foo.io",
			text: "example.com and bar.org",
			want: false,
		},
		{
			name: "subdomain not allowed",
			opt:  "example.com,foo.io",
			text: "Visit sub.example.com",
			want: false,
		},
		{
			name: "bare domain with port",
			opt:  "example.com",
			text: "Service at example.com:8080 is running",
			want: true,
		},
		{
			name: "invalid TLD skipped",
			opt:  "example.com",
			text: "Check foo.invalidtld and example.com",
			want: true, // foo.invalidtld is ignored, example.com is allowed
		},
		{
			name: "allowed subdomain check",
			opt:  "sub.example.com,bar.com",
			text: "Check sub.example.com forsubdomain",
			want: true,
		},
		{
			name: "enable for all - YOLO mode",
			opt:  "yes",
			text: "YOLO mode, any link works http://anydomain.com",
			want: true,
		},
		{
			name: "enable for all - YOLO mode",
			opt:  "1",
			text: "YOLO mode, any link works http://anydomain.com",
			want: true,
		},
		{
			name: "enable for all - YOLO mode",
			opt:  "true",
			text: "YOLO mode, any link works http://anydomain.com",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsUnfurlingEnabled(tt.text, tt.opt, nil)
			if got != tt.want {
				t.Fatalf("opt=%q text=%q → got %v; want %v",
					tt.opt, tt.text, got, tt.want)
			}
		})
	}
}

func TestProcessText_LinkNormalization(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Slack-style link in middle",
			input:    "aaabbcc <https://google.com|This is a link> aabbcc",
			expected: "aaabbcc https://google.com - This is a link, aabbcc",
		},
		{
			name:     "Slack-style link at end",
			input:    "aaabbcc <https://google.com|This is a link>",
			expected: "aaabbcc https://google.com - This is a link",
		},
		{
			name:     "Slack-style link at end with spaces",
			input:    "aaabbcc <https://google.com|This is a link>   ",
			expected: "aaabbcc https://google.com - This is a link",
		},
		{
			name:     "Two links, second at end",
			input:    "First <https://site1.com|Site One> then <https://site2.com|Site Two>",
			expected: "First https://site1.com - Site One, then https://site2.com - Site Two",
		},
		{
			name:     "Two links, text after second",
			input:    "First <https://site1.com|Site One> then <https://site2.com|Site Two> done",
			expected: "First https://site1.com - Site One, then https://site2.com - Site Two, done",
		},
		{
			name:     "Markdown link at end",
			input:    "Check this [Google](https://google.com)",
			expected: "Check this https://google.com - Google",
		},
		{
			name:     "Markdown link in middle",
			input:    "Check this [Google](https://google.com) out",
			expected: "Check this https://google.com - Google, out",
		},
		{
			name:     "HTML anchor at end",
			input:    `Visit <a href="https://example.com">Example</a>`,
			expected: "Visit https://example.com - Example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProcessText(tt.input)
			if result != tt.expected {
				t.Errorf("ProcessText() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestProcessText_PreservesContent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "apostrophes in contractions",
			input:    "I'll let you know what didn't work, it's fine",
			expected: "I'll let you know what didn't work, it's fine",
		},
		{
			name:     "straight double quotes",
			input:    `she said "hello" and left`,
			expected: `she said "hello" and left`,
		},
		{
			name:     "curly quotes from iOS",
			input:    "\u2018don\u2019t\u2019 \u201csay\u201d that",
			expected: "\u2018don\u2019t\u2019 \u201csay\u201d that",
		},
		{
			name:     "exclamation and question marks",
			input:    "wow! really?! amazing!!",
			expected: "wow! really?! amazing!!",
		},
		{
			name:     "parentheses and brackets",
			input:    "see note (important) and [aside]",
			expected: "see note (important) and [aside]",
		},
		{
			name:     "blockquote marker",
			input:    "> this was quoted",
			expected: "> this was quoted",
		},
		{
			name:     "currency and math",
			input:    "costs $5.00 (2+2 = 4)",
			expected: "costs $5.00 (2+2 = 4)",
		},
		{
			name:     "markdown emphasis",
			input:    "*bold* _italic_ ~strike~ `code`",
			expected: "*bold* _italic_ ~strike~ `code`",
		},
		{
			name:     "unicode emoji",
			input:    "great work \U0001F389 \U0001F44D",
			expected: "great work \U0001F389 \U0001F44D",
		},
		{
			name:     "raw slack mention markup",
			input:    "cc <@U0123ABC> in <#C0456DEF|general>",
			expected: "cc <@U0123ABC> in <#C0456DEF|general>",
		},
		{
			name:     "raw broadcast mention",
			input:    "<!channel> please review",
			expected: "<!channel> please review",
		},
		{
			name:     "preserves newlines, collapses inline spaces",
			input:    "first line\n\nsecond  line   with   gaps",
			expected: "first line\n\nsecond line with gaps",
		},
		{
			name:     "strips bidi override (prompt injection vector)",
			input:    "safe\u202etext",
			expected: "safetext",
		},
		{
			name:     "strips ZWSP and BOM",
			input:    "a\u200bb\ufeffc",
			expected: "abc",
		},
		{
			name:     "preserves ZWJ in family emoji sequence",
			input:    "hi \U0001F468\u200D\U0001F469\u200D\U0001F467 bye",
			expected: "hi \U0001F468\u200D\U0001F469\u200D\U0001F467 bye",
		},
		{
			name:     "preserves ZWJ and VS16 in rainbow flag",
			input:    "\U0001F3F3\uFE0F\u200D\U0001F308",
			expected: "\U0001F3F3\uFE0F\u200D\U0001F308",
		},
		{
			name:     "preserves ZWNJ in Persian text",
			input:    "\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645",
			expected: "\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645",
		},
		{
			name:     "strips DEL and C0 controls; tabs collapse to space, newlines kept",
			input:    "ok\x01\x7fmessage\twith\ntabs",
			expected: "okmessage with\ntabs",
		},
		{
			name: "twelve Slack-style links in one message (regression for placeholder bug)",
			input: "see <https://a.example/1|one> and <https://b.example/2|two> and <https://c.example/3|three> " +
				"and <https://d.example/4|four> and <https://e.example/5|five> and <https://f.example/6|six> " +
				"and <https://g.example/7|seven> and <https://h.example/8|eight> and <https://i.example/9|nine> " +
				"and <https://j.example/10|ten> and <https://k.example/11|eleven> and <https://l.example/12|twelve>",
			expected: "see https://a.example/1 - one, and https://b.example/2 - two, and https://c.example/3 - three, " +
				"and https://d.example/4 - four, and https://e.example/5 - five, and https://f.example/6 - six, " +
				"and https://g.example/7 - seven, and https://h.example/8 - eight, and https://i.example/9 - nine, " +
				"and https://j.example/10 - ten, and https://k.example/11 - eleven, and https://l.example/12 - twelve",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProcessText(tt.input)
			if result != tt.expected {
				t.Errorf("ProcessText(%q)\n  got:  %q\n  want: %q", tt.input, result, tt.expected)
			}
		})
	}
}
func TestFilesToText(t *testing.T) {
	tests := []struct {
		name  string
		files []slack.File
		want  string
	}{
		{
			name:  "empty files",
			files: nil,
			want:  "",
		},
		{
			// Covers: non-email filtering, From name+address, CC name+address,
			// CC address-only, "/" separator, "@" → "at" conversion, Subject
			name: "extracts email metadata and skips non-email files",
			files: []slack.File{
				{Filetype: "pdf", Title: "report.pdf"},
				{
					Filetype: "email",
					Subject:  "Team Update",
					From: []slack.EmailFileUserInfo{
						{Name: "Alice", Address: "alice@example.com"},
					},
					Cc: []slack.EmailFileUserInfo{
						{Name: "Bob Smith", Address: "bob@example.com"},
						{Address: "carol@example.com"},
					},
				},
				{Filetype: "png", Title: "chart.png"},
			},
			want: "Email, From: Alice - alice at example.com, CC: Bob Smith - bob at example.com/carol at example.com, Subject: Team Update",
		},
		{
			name: "from with name only",
			files: []slack.File{
				{
					Filetype: "email",
					Subject:  "Test",
					From: []slack.EmailFileUserInfo{
						{Name: "Support Team"},
					},
				},
			},
			want: "Email, From: Support Team, Subject: Test",
		},
		{
			name: "from with address only",
			files: []slack.File{
				{
					Filetype: "email",
					Subject:  "Test",
					From: []slack.EmailFileUserInfo{
						{Address: "noreply@example.com"},
					},
				},
			},
			want: "Email, From: noreply at example.com, Subject: Test",
		},
		{
			// Covers: Mode-based detection and Title → Subject fallback
			name: "mode detection with title fallback",
			files: []slack.File{
				{Mode: "email", Title: "Fwd: Hello"},
			},
			want: "Email, Subject: Fwd: Hello",
		},
		{
			name: "multiple email files",
			files: []slack.File{
				{Filetype: "email", Subject: "First"},
				{Filetype: "email", Subject: "Second"},
			},
			want: "Email, Subject: First Email, Subject: Second",
		},
		{
			name: "empty from and cc entries skipped",
			files: []slack.File{
				{
					Filetype: "email",
					Subject:  "Newsletter",
					From:     []slack.EmailFileUserInfo{{Name: "", Address: ""}},
					Cc:       []slack.EmailFileUserInfo{{Name: "", Address: ""}},
				},
			},
			want: "Email, Subject: Newsletter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilesToText(tt.files)
			if got != tt.want {
				t.Errorf("FilesToText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFilesToTextProcessTextPipeline verifies that FilesToText output
// survives the ProcessText pipeline (filterSpecialChars) without losing structure.
func TestFilesToTextProcessTextPipeline(t *testing.T) {
	tests := []struct {
		name  string
		files []slack.File
		want  string
	}{
		{
			// Covers: format survival, unicode \p{L}\p{M}, CC "/" separator
			name: "format with unicode and cc survives",
			files: []slack.File{
				{
					Filetype: "email",
					Subject:  "R\u00e9union g\u00e9n\u00e9rale",
					From: []slack.EmailFileUserInfo{
						{Name: "Ren\u00e9 M\u00fcller", Address: "rene@example.com"},
					},
					Cc: []slack.EmailFileUserInfo{
						{Address: "bob@example.com"},
					},
				},
			},
			want: "Email, From: Ren\u00e9 M\u00fcller - rene at example.com, CC: bob at example.com, Subject: R\u00e9union g\u00e9n\u00e9rale",
		},
		{
			// Covers: punctuation preserved post-#281; URL preserved by placeholder mechanism
			name: "punctuation preserved and URL preserved",
			files: []slack.File{
				{
					Filetype: "email",
					Subject:  "[Alert] $100 payment - https://example.com/invoice?id=42",
					From: []slack.EmailFileUserInfo{
						{Name: "Billing", Address: "billing@example.com"},
					},
				},
			},
			want: "Email, From: Billing - billing at example.com, Subject: [Alert] $100 payment - https://example.com/invoice?id=42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := FilesToText(tt.files)
			got := ProcessText(raw)
			if got != tt.want {
				t.Errorf("ProcessText(FilesToText()) = %q, want %q\n  raw = %q", got, tt.want, raw)
			}
		})
	}
}
