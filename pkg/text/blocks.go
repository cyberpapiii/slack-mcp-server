package text

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/slack-go/slack"
)

// BlocksToText converts Slack block kit structures into plain text.
// Returns empty string if no text content is found in blocks.
func BlocksToText(blocks slack.Blocks) string {
	if len(blocks.BlockSet) == 0 {
		return ""
	}

	var parts []string
	for _, block := range blocks.BlockSet {
		t := blockToText(block)
		if t != "" {
			parts = append(parts, t)
		}
	}

	return strings.Join(parts, "\n")
}

func blockToText(block slack.Block) string {
	switch b := block.(type) {
	case *slack.SectionBlock:
		return sectionBlockToText(b)
	case *slack.HeaderBlock:
		return headerBlockToText(b)
	case *slack.RichTextBlock:
		return richTextBlockToText(b)
	case *slack.ContextBlock:
		return contextBlockToText(b)
	case *slack.DividerBlock:
		return ""
	default:
		return ""
	}
}

func sectionBlockToText(b *slack.SectionBlock) string {
	var parts []string

	if b.Text != nil && b.Text.Text != "" {
		parts = append(parts, b.Text.Text)
	}

	for _, field := range b.Fields {
		if field != nil && field.Text != "" {
			parts = append(parts, field.Text)
		}
	}

	return strings.Join(parts, "\n")
}

func headerBlockToText(b *slack.HeaderBlock) string {
	if b.Text != nil && b.Text.Text != "" {
		return b.Text.Text
	}
	return ""
}

func contextBlockToText(b *slack.ContextBlock) string {
	var parts []string
	for _, elem := range b.ContextElements.Elements {
		switch e := elem.(type) {
		case *slack.TextBlockObject:
			if e.Text != "" {
				parts = append(parts, e.Text)
			}
		case *slack.ImageBlockElement:
			if e.AltText != "" {
				parts = append(parts, e.AltText)
			}
		}
	}
	return strings.Join(parts, " ")
}

func richTextBlockToText(b *slack.RichTextBlock) string {
	var parts []string
	for _, elem := range b.Elements {
		t := richTextElementToText(elem)
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

func richTextElementToText(elem slack.RichTextElement) string {
	switch e := elem.(type) {
	case *slack.RichTextSection:
		return richTextSectionToText(e.Elements)
	case *slack.RichTextList:
		return richTextListToText(e)
	case *slack.RichTextQuote:
		t := richTextSectionToText(e.Elements)
		if t != "" {
			return "> " + strings.ReplaceAll(t, "\n", "\n> ")
		}
		return ""
	case *slack.RichTextPreformatted:
		t := richTextSectionToText(e.Elements)
		if t != "" {
			return "```\n" + t + "\n```"
		}
		return ""
	default:
		return ""
	}
}

func richTextSectionToText(elements []slack.RichTextSectionElement) string {
	var parts []string
	for _, elem := range elements {
		t := richTextSectionElementToText(elem)
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "")
}

func richTextSectionElementToText(elem slack.RichTextSectionElement) string {
	switch e := elem.(type) {
	case *slack.RichTextSectionTextElement:
		return e.Text
	case *slack.RichTextSectionLinkElement:
		if e.Text != "" {
			return fmt.Sprintf("%s (%s)", e.Text, e.URL)
		}
		return e.URL
	case *slack.RichTextSectionEmojiElement:
		return fmt.Sprintf(":%s:", e.Name)
	case *slack.RichTextSectionChannelElement:
		return fmt.Sprintf("#%s", e.ChannelID)
	case *slack.RichTextSectionUserElement:
		return fmt.Sprintf("@%s", e.UserID)
	case *slack.RichTextSectionBroadcastElement:
		return fmt.Sprintf("@%s", e.Range)
	case *slack.RichTextSectionDateElement:
		if e.Fallback != nil {
			return *e.Fallback
		}
		return ""
	case *slack.RichTextSectionColorElement:
		return e.Value
	case *slack.RichTextSectionUserGroupElement:
		return fmt.Sprintf("@%s", e.UsergroupID)
	default:
		return ""
	}
}

func richTextListToText(list *slack.RichTextList) string {
	var lines []string
	itemNum := 0
	for _, elem := range list.Elements {
		t := richTextElementToText(elem)
		if t == "" {
			continue
		}
		prefix := "- "
		if list.Style == slack.RTEListOrdered {
			prefix = fmt.Sprintf("%d. ", itemNum+list.Offset+1)
			itemNum++
		}
		indent := strings.Repeat("  ", list.Indent)
		lines = append(lines, indent+prefix+t)
	}
	return strings.Join(lines, "\n")
}

// MergeBlocksWithText appends block-derived text to the message text
// when blocks contain content not already present in the text field.
//
// A human-typed message sends its content twice, as mrkdwn text and as a
// rich_text block. The block renders differently enough (entities decoded,
// links as "label (url)") that a verbatim containment check misses, so
// rich_text blocks whose typed text the text field already holds are dropped
// first. The text is kept over the block because it also carries channel and
// usergroup names that rich_text elements do not.
func MergeBlocksWithText(msgText string, blocks slack.Blocks) string {
	if msgText != "" {
		blocks = dropCoveredRichText(msgText, blocks)
	}
	blockText := BlocksToText(blocks)
	if blockText == "" {
		return msgText
	}
	if msgText == "" {
		return blockText
	}
	if strings.Contains(msgText, blockText) {
		return msgText
	}
	return msgText + "\n" + blockText
}

// mrkdwnEscaper applies the escaping Slack gives the text field, so a
// rich_text element's literal text can be looked up in it.
var mrkdwnEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// dropCoveredRichText removes the blocks whose content the text field
// already holds, leaving other blocks untouched. A rich_text block is covered
// when every typed line appears in msgText. Any block is covered when every
// line it renders does: agent-posted messages often repeat the text field in
// mrkdwn section blocks.
func dropCoveredRichText(msgText string, blocks slack.Blocks) slack.Blocks {
	norm := normalizeSpace(msgText)
	kept := make([]slack.Block, 0, len(blocks.BlockSet))
	for _, block := range blocks.BlockSet {
		if rt, ok := block.(*slack.RichTextBlock); ok && richTextCoveredBy(norm, rt) {
			continue
		}
		if renderedCoveredBy(norm, block) {
			continue
		}
		kept = append(kept, block)
	}
	return slack.Blocks{BlockSet: kept}
}

func richTextCoveredBy(normText string, b *slack.RichTextBlock) bool {
	for _, typed := range richTextLiterals(b.Elements) {
		for _, line := range strings.Split(typed, "\n") {
			if !lineCoveredBy(normText, line) {
				return false
			}
		}
	}
	return true
}

// renderedCoveredBy reports whether every line block renders, less its list
// or quote marker, appears in normText. A block that renders nothing is not
// covered; BlocksToText skips it anyway.
func renderedCoveredBy(normText string, block slack.Block) bool {
	rendered := blockToText(block)
	if rendered == "" {
		return false
	}
	for _, line := range strings.Split(rendered, "\n") {
		if !lineCoveredBy(normText, renderedLineMarker.ReplaceAllString(line, "")) {
			return false
		}
	}
	return true
}

// renderedLineMarker matches the prefixes BlocksToText adds to list items
// and quotes, which the text field writes differently ("•", "&gt;").
var renderedLineMarker = regexp.MustCompile(`^\s*(?:[-•] |\d+\. |> ?)`)

func lineCoveredBy(normText, line string) bool {
	line = normalizeSpace(line)
	return line == "" || strings.Contains(normText, line) || strings.Contains(normText, mrkdwnEscaper.Replace(line))
}

// normalizeSpace trims s and folds each run of spaces, tabs and non-breaking
// spaces into one space, keeping newlines. The text field and the blocks of
// one message disagree on spacing, and search collapses it differently from
// history.
func normalizeSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inRun := false
	for _, r := range strings.TrimSpace(s) {
		if r != '\n' && (unicode.IsSpace(r) || r == '\u00a0' || r == '\u202f') {
			if !inRun {
				b.WriteByte(' ')
			}
			inRun = true
			continue
		}
		inRun = false
		b.WriteRune(r)
	}
	return b.String()
}

// richTextLiterals collects the text elements' typed strings. Mentions,
// links and emoji are left out: the text field carries each of them in its
// own mrkdwn form, which already names what the element would render.
func richTextLiterals(elements []slack.RichTextElement) []string {
	var out []string
	sectionLiterals := func(elems []slack.RichTextSectionElement) {
		for _, e := range elems {
			if t, ok := e.(*slack.RichTextSectionTextElement); ok {
				out = append(out, t.Text)
			}
		}
	}
	for _, elem := range elements {
		switch e := elem.(type) {
		case *slack.RichTextSection:
			sectionLiterals(e.Elements)
		case *slack.RichTextQuote:
			sectionLiterals(e.Elements)
		case *slack.RichTextPreformatted:
			sectionLiterals(e.Elements)
		case *slack.RichTextList:
			out = append(out, richTextLiterals(e.Elements)...)
		}
	}
	return out
}
