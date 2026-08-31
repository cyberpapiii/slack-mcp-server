package slackcreds

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// clientToken is a syntactically valid xoxc token: the regexp requires exactly
// 64 lowercase hex-ish characters in the last segment.
var clientToken = "xoxc-1234567890-1234567890-1234567890-" + strings.Repeat("a", 64)

// The cookie shape is what Slack's private endpoints check, so it is pinned
// field by field rather than just "two cookies came back".
func TestUnitNewBrowserSessionCookies(t *testing.T) {
	creds, err := New(clientToken, "xoxd-secret")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if creds.SlackToken() != clientToken {
		t.Errorf("SlackToken = %q", creds.SlackToken())
	}

	cookies := creds.Cookies()
	if len(cookies) != 2 {
		t.Fatalf("want the d and d-s cookies, got %d", len(cookies))
	}
	if cookies[0].Name != "d" || cookies[0].Value != "xoxd-secret" {
		t.Errorf("first cookie = %s=%s", cookies[0].Name, cookies[0].Value)
	}
	if cookies[1].Name != "d-s" {
		t.Errorf("second cookie = %s", cookies[1].Name)
	}
	for _, c := range cookies {
		if c.Path != "/" || c.Domain != ".slack.com" || !c.Secure {
			t.Errorf("cookie %s: path=%q domain=%q secure=%v", c.Name, c.Path, c.Domain, c.Secure)
		}
		if !c.Expires.After(time.Now().AddDate(9, 0, 0)) {
			t.Errorf("cookie %s expires too soon: %v", c.Name, c.Expires)
		}
	}
}

// A cookie value with reserved characters has to be escaped, or the header is
// truncated at the first one.
func TestUnitNewEscapesUnsafeCookieValues(t *testing.T) {
	creds, err := New(clientToken, "xoxd-a/b+c=")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := creds.Cookies()[0].Value; got != "xoxd-a%2Fb%2Bc%3D" {
		t.Errorf("cookie value = %q, want it percent-escaped", got)
	}
}

// OAuth tokens carry no browser session, so no cookies and no cookie
// requirement.
func TestUnitNewOAuthTokenNeedsNoCookie(t *testing.T) {
	for _, token := range []string{"xoxp-1-2-3", "xoxb-1-2-3", "xoxe.xoxp-1-2-3"} {
		creds, err := New(token, "")
		if err != nil {
			t.Fatalf("New(%q): %v", token, err)
		}
		if creds.Cookies() != nil {
			t.Errorf("New(%q) returned cookies", token)
		}
	}
}

func TestUnitNewRejectsIncompleteCredentials(t *testing.T) {
	if _, err := New("", ""); err != ErrNoToken {
		t.Errorf("empty token: got %v, want ErrNoToken", err)
	}
	if _, err := New(clientToken, ""); err != ErrNoCookies {
		t.Errorf("xoxc without cookie: got %v, want ErrNoCookies", err)
	}
}

// The user agent has to name the build platform; Slack rejects obviously
// non-browser agents on the private endpoints.
func TestUnitUserAgentNamesBuildPlatform(t *testing.T) {
	want := map[string]string{
		"darwin":  "Macintosh; Intel Mac OS X 10_15_7",
		"windows": "Windows NT 10.0; Win64; x64",
		"linux":   "X11; Linux x86_64",
		"plan9":   "X11; Linux x86_64",
	}
	for goos, platform := range want {
		if got := userAgentOS(goos); got != platform {
			t.Errorf("userAgentOS(%q) = %q, want %q", goos, got, platform)
		}
	}
	if !strings.Contains(UserAgent, want[runtime.GOOS]) && runtime.GOOS != "plan9" {
		t.Errorf("UserAgent %q does not name this platform", UserAgent)
	}
	if !strings.HasPrefix(UserAgent, "Mozilla/5.0 (") || !strings.HasSuffix(UserAgent, "Safari/537.36") {
		t.Errorf("UserAgent %q is not shaped like a browser agent", UserAgent)
	}
}
