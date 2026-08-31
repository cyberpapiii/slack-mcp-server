// Package slackcreds holds the Slack credentials the server authenticates
// with: an API token plus, for browser sessions, the session cookies.
//
// This replaces github.com/rusq/slackdump/v3/auth, which supplied the same
// three values through a package that also links a headless browser stack
// (go-rod, playwright, charmbracelet). Only the token, the cookies and the
// user agent were ever used here.
package slackcreds

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"time"
)

var (
	ErrNoToken   = errors.New("no token")
	ErrNoCookies = errors.New("no cookies")
)

// Credentials is what a Slack client needs from a credential source.
type Credentials interface {
	SlackToken() string
	Cookies() []*http.Cookie
}

// Value is a token and its cookies. The zero value is unusable; build one
// with New.
type Value struct {
	token   string
	cookies []*http.Cookie
}

// SlackToken returns the token these credentials authenticate with.
func (v Value) SlackToken() string { return v.token }

// Cookies returns the browser session cookies, nil for token-only credentials.
func (v Value) Cookies() []*http.Cookie { return v.cookies }

// clientTokenRE matches a web-client (xoxc) token, which is only usable
// alongside the browser session cookie.
var clientTokenRE = regexp.MustCompile(`xoxc-[0-9]+-[0-9]+-[0-9]+-[0-9a-z]{64}`)

// IsClientToken reports whether tok is a web-client token.
func IsClientToken(tok string) bool { return clientTokenRE.MatchString(tok) }

// New builds credentials from a token and, for xoxc tokens, the xoxd cookie
// value. Non-client tokens (xoxp, xoxb) carry no cookies.
func New(token string, cookie string) (Value, error) {
	if token == "" {
		return Value{}, ErrNoToken
	}
	if !IsClientToken(token) {
		return Value{token: token}, nil
	}
	if cookie == "" {
		return Value{}, ErrNoCookies
	}
	return Value{
		token: token,
		cookies: []*http.Cookie{
			makeCookie("d", cookie),
			// Slack's web client sends d-s next to d: a unix timestamp a few
			// seconds in the past.
			makeCookie("d-s", fmt.Sprintf("%d", time.Now().Unix()-10)),
		},
	}, nil
}

func makeCookie(key, val string) *http.Cookie {
	if !urlsafe(val) {
		val = url.QueryEscape(val)
	}
	return &http.Cookie{
		Name:    key,
		Value:   val,
		Path:    "/",
		Domain:  ".slack.com",
		Expires: time.Now().AddDate(10, 0, 0).UTC(),
		Secure:  true,
	}
}

var reURLsafe = regexp.MustCompile(`[-._~%a-zA-Z0-9]+`)

// urlsafe reports whether s is made only of RFC 3986 unreserved characters,
// so it can go into a cookie without escaping.
func urlsafe(s string) bool {
	return reURLsafe.ReplaceAllString(s, "") == ""
}

// UserAgent is the browser user agent Slack's private endpoints expect. The
// platform tracks the build target, as a real desktop client's would.
var UserAgent = fmt.Sprintf(
	"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
	userAgentOS(runtime.GOOS),
)

func userAgentOS(goos string) string {
	switch goos {
	case "darwin":
		return "Macintosh; Intel Mac OS X 10_15_7"
	case "windows":
		return "Windows NT 10.0; Win64; x64"
	default:
		return "X11; Linux x86_64"
	}
}
