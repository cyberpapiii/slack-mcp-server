package text

import (
	"encoding/json"
	"testing"

	"github.com/slack-go/slack"
)

// unmarshalBlocks parses a real rich_text payload the way the Slack API
// delivers it, so the test exercises the same types the handlers see rather
// than hand-built structs that could drift from the wire shape.
func unmarshalBlocks(t *testing.T, raw string) slack.Blocks {
	t.Helper()
	var blocks slack.Blocks
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatalf("unmarshal blocks: %v", err)
	}
	return blocks
}

// Quote and preformatted elements carry their children directly in Elements.
// They used to be aliases of RichTextSection, so this pins the rendering
// against that shape changing under us again.
func TestUnitBlocksToTextRichTextQuoteAndPreformatted(t *testing.T) {
	blocks := unmarshalBlocks(t, `[{
	  "type": "rich_text",
	  "elements": [
	    {"type": "rich_text_quote", "elements": [
	      {"type": "text", "text": "first line\nsecond line"}
	    ]},
	    {"type": "rich_text_preformatted", "elements": [
	      {"type": "text", "text": "go build ./..."}
	    ]},
	    {"type": "rich_text_section", "elements": [
	      {"type": "text", "text": "plain tail"}
	    ]}
	  ]
	}]`)

	want := "> first line\n> second line\n```\ngo build ./...\n```\nplain tail"
	if got := BlocksToText(blocks); got != want {
		t.Errorf("BlocksToText:\n got %q\nwant %q", got, want)
	}
}

// An empty quote or preformatted element contributes nothing, so no stray
// "> " or fence ends up in the rendered message.
func TestUnitBlocksToTextSkipsEmptyRichTextElements(t *testing.T) {
	blocks := unmarshalBlocks(t, `[{
	  "type": "rich_text",
	  "elements": [
	    {"type": "rich_text_quote", "elements": []},
	    {"type": "rich_text_preformatted", "elements": []},
	    {"type": "rich_text_section", "elements": [
	      {"type": "text", "text": "only this"}
	    ]}
	  ]
	}]`)

	if got := BlocksToText(blocks); got != "only this" {
		t.Errorf("BlocksToText = %q, want %q", got, "only this")
	}
}

func TestUnitBlocksToTextEmptyBlockSet(t *testing.T) {
	if got := BlocksToText(slack.Blocks{}); got != "" {
		t.Errorf("BlocksToText = %q, want empty", got)
	}
}

// A human-typed message carries the same content twice: mrkdwn in text and
// the structured rich_text block. The block renders a little differently
// (&amp; decoded, <!channel> as @channel, links as "label (url)"), so the old
// verbatim-containment check missed and every formatted message came out
// twice. The text wins: it is what the author typed, and it keeps channel
// names that a rich_text channel element does not carry.
func TestUnitMergeBlocksWithTextDoesNotRepeatRichTextOfTheText(t *testing.T) {
	msgText := "hello <!channel>! ideas below\n\n• :bank: Chime\n    ◦ canceled the lunch &amp; storm!\n• Brief <https://x.test/doc|HERE>, see <#C123|ideation>"
	blocks := unmarshalBlocks(t, `[{
	  "type": "rich_text",
	  "elements": [
	    {"type": "rich_text_section", "elements": [
	      {"type": "text", "text": "hello "},
	      {"type": "broadcast", "range": "channel"},
	      {"type": "text", "text": "! ideas below\n\n"}
	    ]},
	    {"type": "rich_text_list", "style": "bullet", "indent": 0, "elements": [
	      {"type": "rich_text_section", "elements": [
	        {"type": "emoji", "name": "bank"},
	        {"type": "text", "text": " Chime"}
	      ]}
	    ]},
	    {"type": "rich_text_list", "style": "bullet", "indent": 1, "elements": [
	      {"type": "rich_text_section", "elements": [
	        {"type": "text", "text": "canceled the lunch & storm!"}
	      ]}
	    ]},
	    {"type": "rich_text_list", "style": "bullet", "indent": 0, "elements": [
	      {"type": "rich_text_section", "elements": [
	        {"type": "text", "text": "Brief "},
	        {"type": "link", "url": "https://x.test/doc", "text": "HERE"},
	        {"type": "text", "text": ", see "},
	        {"type": "channel", "channel_id": "C123"}
	      ]}
	    ]}
	  ]
	}]`)

	if got := MergeBlocksWithText(msgText, blocks); got != msgText {
		t.Fatalf("formatted message rendered twice:\n%s", got)
	}
}

// A bot can post rich_text under a short fallback text. The block holds the
// content, so it is still appended.
func TestUnitMergeBlocksWithTextKeepsRichTextBeyondFallback(t *testing.T) {
	blocks := unmarshalBlocks(t, `[{
	  "type": "rich_text",
	  "elements": [
	    {"type": "rich_text_section", "elements": [
	      {"type": "text", "text": "Deploy 42 finished: 3 services updated"}
	    ]}
	  ]
	}]`)

	got := MergeBlocksWithText("New notification", blocks)
	want := "New notification\nDeploy 42 finished: 3 services updated"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Section blocks are not a copy of the text field, so they keep merging
// alongside a rich_text block the text already covers.
func TestUnitMergeBlocksWithTextKeepsSectionBesideCoveredRichText(t *testing.T) {
	blocks := unmarshalBlocks(t, `[
	  {"type": "rich_text", "elements": [
	    {"type": "rich_text_section", "elements": [{"type": "text", "text": "Build & test"}]}
	  ]},
	  {"type": "section", "text": {"type": "mrkdwn", "text": "*Status:* green"}}
	]`)

	got := MergeBlocksWithText("Build &amp; test", blocks)
	want := "Build &amp; test\n*Status:* green"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// An agent-posted message carried its bullet summary in the text field and
// the same opening and closing lines again in mrkdwn section blocks. Only
// rich_text blocks were checked against the text, so those lines rendered
// twice (search and history both showed it live).
func TestUnitMergeBlocksWithTextDoesNotRepeatSectionsOfTheText(t *testing.T) {
	blocks := unmarshalBlocks(t, `[
	  {"type": "section", "text": {"type": "mrkdwn", "text": "Codex here — feedback landed:"}},
	  {"type": "section", "text": {"type": "mrkdwn", "text": "• Site: stronger color\n• Domains: /mx check"}},
	  {"type": "section", "text": {"type": "mrkdwn", "text": "I closed issue #55. No deploy."}}
	]`)
	text := "Codex here — feedback landed: • Site: stronger color\n• Domains: /mx check\nI closed issue #55. No deploy."

	if got := MergeBlocksWithText(text, blocks); got != text {
		t.Fatalf("got %q, want the text alone", got)
	}
}

// Runs of spaces and non-breaking spaces differ between the text field and
// the blocks (search even collapses them differently from history), so the
// comparison ignores them.
func TestUnitMergeBlocksWithTextIgnoresSpacingDifferences(t *testing.T) {
	blocks := unmarshalBlocks(t, `[
	  {"type": "rich_text", "elements": [
	    {"type": "rich_text_section", "elements": [{"type": "text", "text": "Codex here — feedback  landed:"}]},
	    {"type": "rich_text_list", "style": "bullet", "elements": [
	      {"type": "rich_text_section", "elements": [{"type": "text", "text": "Site: stronger color"}]}
	    ]}
	  ]}
	]`)
	text := "Codex here — feedback landed:  • Site: stronger color"

	if got := MergeBlocksWithText(text, blocks); got != text {
		t.Fatalf("got %q, want the text alone", got)
	}
}

func TestUnitMergeBlocksWithTextKeepsSectionWithNewLines(t *testing.T) {
	blocks := unmarshalBlocks(t, `[
	  {"type": "section", "text": {"type": "mrkdwn", "text": "Codex here — feedback landed:\n*Next:* ship Friday"}}
	]`)
	text := "Codex here — feedback landed:"

	want := text + "\nCodex here — feedback landed:\n*Next:* ship Friday"
	if got := MergeBlocksWithText(text, blocks); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
