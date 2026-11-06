package handler

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"github.com/slack-go/slack"
	"go.uber.org/zap"
	"golang.org/x/net/publicsuffix"
)

var (
	unfurlURLRegex    = regexp.MustCompile(`https?://[^\s"<>|)\]\\]+`)
	unfurlDomainRegex = regexp.MustCompile(`\b(?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?\.)+[A-Za-z]{2,}\b`)
)

// unfurlPolicy is SLACK_MCP_ADD_MESSAGE_UNFURLING: off when empty or false,
// on for every link when true, otherwise a comma-separated domain allowlist.
type unfurlPolicy struct {
	all     bool
	allowed map[string]struct{}
	raw     string
}

func parseUnfurling(value string) unfurlPolicy {
	policy := unfurlPolicy{raw: value}
	if value == "" || envutil.IsFalsy(value) {
		return policy
	}
	if envutil.IsTruthy(value) {
		policy.all = true
		return policy
	}
	policy.allowed = make(map[string]struct{})
	for _, d := range strings.Split(value, ",") {
		if d = normalizeHost(d); d != "" {
			policy.allowed[d] = struct{}{}
		}
	}
	return policy
}

// unfurlOptions turns links on or off for a message under the unfurling
// policy. Every string in the blocks is checked along with the text, since
// links in blocks unfurl too.
func (s Settings) unfurlOptions(text string, blocks []slack.Block, logger *zap.Logger) []slack.MsgOption {
	if len(blocks) > 0 {
		encoded, err := json.Marshal(blocks)
		var decoded any
		if err != nil || json.Unmarshal(encoded, &decoded) != nil {
			return disabledUnfurls()
		}
		text = strings.Join(append([]string{text}, jsonStrings(decoded)...), " ")
	}
	if s.unfurls(text, logger) {
		return []slack.MsgOption{slack.MsgOptionEnableLinkUnfurl()}
	}
	return disabledUnfurls()
}

func disabledUnfurls() []slack.MsgOption {
	return []slack.MsgOption{slack.MsgOptionDisableLinkUnfurl(), slack.MsgOptionDisableMediaUnfurl()}
}

func jsonStrings(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, jsonStrings(item)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, item := range v {
			out = append(out, jsonStrings(item)...)
		}
		return out
	}
	return nil
}

// unfurls reports whether a posted message may unfurl its links: with a
// domain allowlist, only when every URL and bare domain in the text is on it.
// A URL that does not parse to a host is refused.
func (s Settings) unfurls(text string, logger *zap.Logger) bool {
	if s.unfurling.all {
		return true
	}
	allowed := s.unfurling.allowed
	if allowed == nil {
		return false
	}
	refuse := func(host string) bool {
		logger.Warn("Security: attempt to unfurl non-whitelisted host",
			zap.String("host", host),
			zap.String("allowed", s.unfurling.raw),
		)
		return false
	}

	for _, rawURL := range unfurlURLRegex.FindAllString(text, -1) {
		u, err := url.Parse(strings.TrimRight(rawURL, ".,;:!?'"))
		if err != nil || u.Hostname() == "" {
			return refuse(rawURL)
		}
		host := normalizeHost(u.Hostname())
		if _, ok := allowed[host]; !ok {
			return refuse(host)
		}
	}

	txtNoURLs := unfurlURLRegex.ReplaceAllString(text, " ")
	for _, d := range unfurlDomainRegex.FindAllString(txtNoURLs, -1) {
		d = normalizeHost(d)
		// A suffix outside the public list ("file.txt") is not a domain; a
		// privately run one ("evil.github.io") is.
		if suffix, icann := publicsuffix.PublicSuffix(d); !icann && !strings.Contains(suffix, ".") {
			continue
		}
		if _, ok := allowed[d]; !ok {
			return refuse(d)
		}
	}

	return true
}

// normalizeHost is how link hosts, bare domains and allowlist entries compare.
func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www."), ".")
}
