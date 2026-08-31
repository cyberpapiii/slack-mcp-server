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
