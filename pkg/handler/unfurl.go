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

// unfurls reports whether a posted message may unfurl its links under
// SLACK_MCP_ADD_MESSAGE_UNFURLING: off when empty or false, on for every link
// when true, otherwise only when every URL and bare domain in the text is on
// the comma-separated domain allowlist.
func (s Settings) unfurls(text string, logger *zap.Logger) bool {
	opt := s.Unfurling
	if opt == "" || envutil.IsFalsy(opt) {
		return false
	}
	if envutil.IsTruthy(opt) {
		return true
	}

	allowed := make(map[string]struct{})
	for _, d := range strings.Split(opt, ",") {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			allowed[d] = struct{}{}
		}
	}
	refuse := func(host string) bool {
		if logger != nil {
			logger.Warn("Security: attempt to unfurl non-whitelisted host",
				zap.String("host", host),
				zap.String("allowed", opt),
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
