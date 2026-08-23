package text

import (
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slack-go/slack"
	"go.uber.org/zap"
	"golang.org/x/net/publicsuffix"
)

// OutputMode selects the render fidelity for tool output. Resolved once per
// request from the tool's `detail` parameter, falling back to the
// SLACK_MCP_COMPACT_OUTPUT env var.
type OutputMode int

const (
	ModeStandard OutputMode = iota // compact, agent-oriented (default)
	ModeFull                       // verbose legacy format, all columns
)

// ResolveOutputMode maps a tool's `detail` parameter to an OutputMode.
// Empty string defers to the SLACK_MCP_COMPACT_OUTPUT env var.
func ResolveOutputMode(detailParam string) (OutputMode, error) {
	switch strings.ToLower(strings.TrimSpace(detailParam)) {
	case "":
		if compactOutput() {
			return ModeStandard, nil
		}
		return ModeFull, nil
	case "standard":
		return ModeStandard, nil
	case "full":
		return ModeFull, nil
	default:
		return ModeStandard, fmt.Errorf("invalid detail value %q: must be \"standard\" or \"full\"", detailParam)
	}
}

func AttachmentToText(att slack.Attachment, mode OutputMode) string {
	if mode == ModeFull {
		return attachmentToFullText(att)
	}
	return attachmentToCompactText(att)
}

// attachmentCompactBudget caps per-attachment rendered length in standard mode.
// Fields render before body text because bot attachments (alerts, CI, Jira)
// carry their densest signal in fields. When content is cut, an explicit
// receipt tells the agent how to recover it losslessly.
const attachmentCompactBudget = 300

// No quotes around "full": these strings land inside quoted CSV fields, where
// literal double quotes get doubled by the CSV encoder and read as noise.
const attachmentTruncationReceipt = " …[attachment truncated; re-fetch this message with detail: full]"

// attachmentToCompactText renders bot-attachment content in standard mode up
// to attachmentCompactBudget characters, in priority order: title+link, then
// fields (Title: Value pairs, matching attachmentToFullText's formatting),
// then text, then pretext. Parts are cut whole, not mid-word, except that an
// oversized first part (typically the title) is hard-cut at the budget so an
// attachment always renders something. When any content is dropped or cut,
// attachmentTruncationReceipt is appended so the agent knows it can recover
// the rest by re-fetching the message with detail: "full".
func attachmentToCompactText(att slack.Attachment) string {
	var parts []string

	switch {
	case att.Title != "" && att.TitleLink != "":
		parts = append(parts, fmt.Sprintf("%s (%s)", att.Title, att.TitleLink))
	case att.Title != "":
		parts = append(parts, att.Title)
	case att.TitleLink != "":
		parts = append(parts, att.TitleLink)
	}

	for _, f := range att.Fields {
		if f.Title != "" && f.Value != "" {
			parts = append(parts, fmt.Sprintf("%s: %s", f.Title, f.Value))
		} else if f.Title != "" {
			parts = append(parts, f.Title)
		} else if f.Value != "" {
			parts = append(parts, f.Value)
		}
	}

	if att.Text != "" {
		parts = append(parts, att.Text)
	}

	if att.Pretext != "" {
		parts = append(parts, att.Pretext)
	}

	if blocksText := BlocksToText(att.Blocks); blocksText != "" {
		parts = append(parts, blocksText)
	}

	if len(parts) == 0 {
		return ""
	}

	var result string
	truncated := false
	for i, p := range parts {
		candidate := p
		if i > 0 {
			candidate = result + "; " + p
		}

		if len(candidate) <= attachmentCompactBudget {
			result = candidate
			continue
		}

		if i == 0 {
			// Nothing fits yet; hard-cut the first part so we still render
			// something. Back up to a rune boundary so the cut can't split a
			// multibyte character into invalid UTF-8.
			cut := attachmentCompactBudget
			for cut > 0 && !utf8.RuneStart(candidate[cut]) {
				cut--
			}
			result = candidate[:cut]
		}
		truncated = true
		break
	}

	result = strings.ReplaceAll(result, "\n", " ")
	result = strings.ReplaceAll(result, "\r", " ")
	result = strings.TrimSpace(result)

	if truncated {
		result += attachmentTruncationReceipt
	}

	return result
}

func attachmentToFullText(att slack.Attachment) string {
	var parts []string

	if att.Title != "" {
		if att.TitleLink != "" {
			parts = append(parts, fmt.Sprintf("Title: [%s](%s)", att.Title, att.TitleLink))
		} else {
			parts = append(parts, fmt.Sprintf("Title: %s", att.Title))
		}
	}

	if att.AuthorName != "" {
		parts = append(parts, fmt.Sprintf("Author: %s", att.AuthorName))
	}

	if att.Pretext != "" {
		parts = append(parts, fmt.Sprintf("Pretext: %s", att.Pretext))
	}

	if att.Text != "" {
		parts = append(parts, fmt.Sprintf("Text: %s", att.Text))
	}

	for _, f := range att.Fields {
		if f.Title != "" && f.Value != "" {
			parts = append(parts, fmt.Sprintf("%s: %s", f.Title, f.Value))
		} else if f.Title != "" {
			parts = append(parts, f.Title)
		} else if f.Value != "" {
			parts = append(parts, f.Value)
		}
	}

	if att.Footer != "" {
		ts, _ := TimestampToIsoRFC3339(string(att.Ts) + ".000000")

		parts = append(parts, fmt.Sprintf("Footer: %s @ %s", att.Footer, ts))
	}

	if blocksText := BlocksToText(att.Blocks); blocksText != "" {
		parts = append(parts, fmt.Sprintf("Blocks: %s", blocksText))
	}

	result := strings.Join(parts, "; ")

	result = strings.ReplaceAll(result, "\n", " ")
	result = strings.ReplaceAll(result, "\r", " ")
	result = strings.ReplaceAll(result, "\t", " ")
	result = strings.TrimSpace(result)

	return result
}

func compactOutput() bool {
	v := strings.TrimSpace(os.Getenv("SLACK_MCP_COMPACT_OUTPUT"))
	switch strings.ToLower(v) {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

// FilesToText extracts text metadata from email file attachments.
// Separators are chosen so the metadata survives the text-processing pipeline.
func FilesToText(files []slack.File) string {
	var parts []string

	for _, f := range files {
		if f.Filetype != "email" && f.Mode != "email" {
			continue
		}

		var emailParts []string

		if len(f.From) > 0 {
			if s := formatEmailUser(f.From[0]); s != "" {
				emailParts = append(emailParts, "From: "+s)
			}
		}

		if len(f.Cc) > 0 {
			var ccParts []string
			for _, c := range f.Cc {
				if s := formatEmailUser(c); s != "" {
					ccParts = append(ccParts, s)
				}
			}
			if len(ccParts) > 0 {
				emailParts = append(emailParts, "CC: "+strings.Join(ccParts, "/"))
			}
		}

		if f.Subject != "" {
			emailParts = append(emailParts, fmt.Sprintf("Subject: %s", f.Subject))
		} else if f.Title != "" {
			emailParts = append(emailParts, fmt.Sprintf("Subject: %s", f.Title))
		}

		if len(emailParts) > 0 {
			parts = append(parts, "Email, "+strings.Join(emailParts, ", "))
		}
	}

	return strings.Join(parts, " ")
}

func formatEmailUser(u slack.EmailFileUserInfo) string {
	addr := strings.ReplaceAll(u.Address, "@", " at ")
	if u.Name != "" && addr != "" {
		return u.Name + " - " + addr
	} else if u.Name != "" {
		return u.Name
	} else if addr != "" {
		return addr
	}
	return ""
}

func AttachmentsTo2CSV(msgText string, attachments []slack.Attachment, mode OutputMode) string {
	if len(attachments) == 0 {
		return ""
	}

	var descriptions []string
	for _, att := range attachments {
		plainText := AttachmentToText(att, mode)
		if plainText != "" {
			descriptions = append(descriptions, plainText)
		}
	}

	prefix := ""
	if msgText != "" {
		prefix = ". "
	}

	return prefix + strings.Join(descriptions, ", ")
}

func IsUnfurlingEnabled(text string, opt string, logger *zap.Logger) bool {
	if opt == "" || opt == "no" || opt == "false" || opt == "0" {
		return false
	}

	if opt == "yes" || opt == "true" || opt == "1" {
		return true
	}

	allowed := make(map[string]struct{}, 0)
	for _, d := range strings.Split(opt, ",") {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		allowed[d] = struct{}{}
	}

	urls := unfurlURLRegex.FindAllString(text, -1)
	for _, rawURL := range urls {
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			continue
		}
		host := strings.ToLower(u.Host)
		if idx := strings.Index(host, ":"); idx != -1 {
			host = host[:idx]
		}
		host = strings.TrimPrefix(host, "www.")
		if _, ok := allowed[host]; !ok {
			if logger != nil {
				logger.Warn("Security: attempt to unfurl non-whitelisted host",
					zap.String("host", host),
					zap.String("allowed", opt),
				)
			}
			return false
		}
	}

	txtNoURLs := unfurlURLRegex.ReplaceAllString(text, " ")
	doms := unfurlDomainRegex.FindAllString(txtNoURLs, -1)

	for _, d := range doms {
		d = strings.ToLower(d)

		if _, icann := publicsuffix.PublicSuffix(d); !icann {
			continue
		}

		if _, ok := allowed[d]; !ok {
			if logger != nil {
				logger.Warn("Security: attempt to unfurl non-whitelisted host",
					zap.String("host", d),
					zap.String("allowed", opt),
				)
			}
			return false
		}
	}

	return true
}

func Workspace(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	parts := strings.Split(host, ".")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid Slack URL: %q", rawURL)
	}
	return parts[0], nil
}

func TimestampToIsoRFC3339(slackTS string) (string, error) {
	parts := strings.Split(slackTS, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid slack timestamp format: %s", slackTS)
	}

	seconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("failed to parse seconds: %v", err)
	}

	microseconds, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("failed to parse microseconds: %v", err)
	}

	t := time.Unix(seconds, microseconds*1000)

	return t.UTC().Format(time.RFC3339), nil
}

func ProcessText(s string) string {
	s = normalizeLinks(s)
	s = stripUnsafeRunes(s)
	s = collapseInlineSpaces(s)

	return strings.TrimSpace(s)
}

func HumanizeCertificates(certs []*x509.Certificate) string {
	var descriptions []string
	for _, cert := range certs {
		subjectCN := cert.Subject.CommonName
		issuerCN := cert.Issuer.CommonName
		expiry := cert.NotAfter.Format("2006-01-02")

		description := fmt.Sprintf("CN=%s (Issuer CN=%s, expires %s)", subjectCN, issuerCN, expiry)
		descriptions = append(descriptions, description)
	}
	return strings.Join(descriptions, ", ")
}

var (
	slackLinkRegex    = regexp.MustCompile(`<(https?://[^>|]+)\|([^>]+)>`)
	markdownLinkRegex = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)]+)\)`)
	htmlLinkRegex     = regexp.MustCompile(`<a\s+href=["']([^"']+)["'][^>]*>([^<]+)</a>`)
	inlineSpaceRegex  = regexp.MustCompile(`[ \t]+`)
	unfurlURLRegex    = regexp.MustCompile(`https?://[^\s]+`)
	unfurlDomainRegex = regexp.MustCompile(`\b(?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?\.)+[A-Za-z]{2,}\b`)
)

// normalizeLinks rewrites Slack, Markdown and HTML links as "url - label",
// followed by a comma unless the link is the last non-blank content.
func normalizeLinks(text string) string {
	text = replaceLinks(text, slackLinkRegex, 1, 2)
	text = replaceLinks(text, markdownLinkRegex, 2, 1)
	return replaceLinks(text, htmlLinkRegex, 1, 2)
}

func replaceLinks(text string, re *regexp.Regexp, urlGroup, labelGroup int) string {
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	last := 0
	for _, m := range matches {
		b.WriteString(text[last:m[0]])
		b.WriteString(text[m[2*urlGroup]:m[2*urlGroup+1]])
		b.WriteString(" - ")
		b.WriteString(text[m[2*labelGroup]:m[2*labelGroup+1]])
		if strings.TrimSpace(text[m[1]:]) != "" {
			b.WriteByte(',')
		}
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// stripUnsafeRunes removes runes that are display-corrupting or carry no
// semantic content: C0/C1 controls (except \t \n \r), DEL, BOM, ZWSP,
// LRM/RLM, bidi overrides, and bidi isolates. Bidi overrides are a known
// prompt-injection vector in chat corpora. U+200C (ZWNJ) and U+200D (ZWJ)
// are preserved: they are required for Persian and Arabic letter joining
// and for emoji ZWJ sequences such as family and flag emoji.
func stripUnsafeRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7F:
			continue
		case r >= 0x80 && r <= 0x9F:
			continue
		case r == 0xFEFF:
			continue
		case r == 0x200B, r == 0x200E, r == 0x200F: // ZWSP, LRM, RLM; U+200C ZWNJ and U+200D ZWJ preserved
			continue
		case r >= 0x202A && r <= 0x202E:
			continue
		case r >= 0x2066 && r <= 0x2069:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func collapseInlineSpaces(s string) string {
	return inlineSpaceRegex.ReplaceAllString(s, " ")
}
