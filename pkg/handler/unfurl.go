package handler

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	"go.uber.org/zap"
	"golang.org/x/net/publicsuffix"
)

var (
	unfurlURLRegex    = regexp.MustCompile(`https?://[^\s]+`)
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
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			policy.allowed[d] = struct{}{}
		}
	}
	return policy
}

// unfurls reports whether a posted message may unfurl its links: with a
// domain allowlist, only when every URL and bare domain in the text is on it.
func (s Settings) unfurls(text string, logger *zap.Logger) bool {
	if s.unfurling.all {
		return true
	}
	allowed := s.unfurling.allowed
	if allowed == nil {
		return false
	}
	refuse := func(host string) bool {
		if logger != nil {
			logger.Warn("Security: attempt to unfurl non-whitelisted host",
				zap.String("host", host),
				zap.String("allowed", s.unfurling.raw),
			)
		}
		return false
	}

	for _, rawURL := range unfurlURLRegex.FindAllString(text, -1) {
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
			return refuse(host)
		}
	}

	txtNoURLs := unfurlURLRegex.ReplaceAllString(text, " ")
	for _, d := range unfurlDomainRegex.FindAllString(txtNoURLs, -1) {
		d = strings.ToLower(d)
		if _, icann := publicsuffix.PublicSuffix(d); !icann {
			continue
		}
		if _, ok := allowed[d]; !ok {
			return refuse(d)
		}
	}

	return true
}
