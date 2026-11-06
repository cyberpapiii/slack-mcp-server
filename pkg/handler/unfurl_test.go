package handler

import (
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestUnitUnfurls(t *testing.T) {
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
			name: "yes allows every domain",
			opt:  "yes",
			text: "YOLO mode, any link works http://anydomain.com",
			want: true,
		},
		{
			name: "1 allows every domain",
			opt:  "1",
			text: "YOLO mode, any link works http://anydomain.com",
			want: true,
		},
		{
			name: "true allows every domain",
			opt:  "true",
			text: "YOLO mode, any link works http://anydomain.com",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Settings{unfurling: parseUnfurling(tt.opt)}.unfurls(tt.text, zap.NewNop())
			if got != tt.want {
				t.Fatalf("opt=%q text=%q → got %v; want %v",
					tt.opt, tt.text, got, tt.want)
			}
		})
	}
}

// A domain allowlist also covers links that only appear in blocks.
func TestUnitUnfurlOptionsCheckBlockLinks(t *testing.T) {
	settings := Settings{unfurling: parseUnfurling("example.com")}
	blocks := []slack.Block{slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, "<https://evil.test/x|look>", false, false), nil, nil)}

	assert.Len(t, settings.unfurlOptions("", blocks, zap.NewNop()), 2, "a non-allowlisted block link disables unfurling")
	assert.Len(t, settings.unfurlOptions("see https://example.com/a", nil, zap.NewNop()), 1, "an allowlisted text link enables it")
}

// Links an earlier version of the check let through: a path-less link in
// blocks, an allowed link hiding a later one in the same block JSON, and
// Slack's <url|label> markup.
func TestUnitUnfurlOptionsRefuseHiddenLinks(t *testing.T) {
	settings := Settings{unfurling: parseUnfurling("good.com")}
	section := func(text string) []slack.Block {
		return []slack.Block{slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, text, false, false), nil, nil)}
	}
	actions := []slack.Block{slack.NewActionBlock("a", slack.NewButtonBlockElement("b", "v", slack.NewTextBlockObject(slack.PlainTextType, "go", false, false)).WithURL("https://evil.com"))}

	for name, blocks := range map[string][]slack.Block{
		"path-less button url": actions,
		"allowed then denied":  append(section("https://good.com/a"), section("https://evil.com/b")...),
		"slack link markup":    section("<https://evil.com|click>"),
	} {
		assert.Len(t, settings.unfurlOptions("", blocks, zap.NewNop()), 2, name)
	}
	assert.Len(t, settings.unfurlOptions("<https://evil.com|click>", nil, zap.NewNop()), 2, "markup in text")
	assert.Len(t, settings.unfurlOptions("<https://good.com/x|ok>", nil, zap.NewNop()), 1, "allowed markup in text")
}

func TestUnitUnfurlsNormalizeHostsTheSameWay(t *testing.T) {
	settings := Settings{unfurling: parseUnfurling("www.example.com")}
	for _, text := range []string{"https://example.com/a", "see https://example.com.", "www.example.com", "https://WWW.Example.com/x, then"} {
		assert.True(t, settings.unfurls(text, zap.NewNop()), text)
	}
	for _, text := range []string{"see evil.github.io now", "evil.herokuapp.com", "x.vercel.app"} {
		assert.False(t, settings.unfurls(text, zap.NewNop()), "a private suffix is still a domain: %s", text)
	}
	assert.True(t, settings.unfurls("open file.txt", zap.NewNop()), "not a domain")
}
