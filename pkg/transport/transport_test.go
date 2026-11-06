package transport

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type captureRT struct {
	seen *http.Request
}

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.seen = req
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestUserAgentTransportSetsHeaderAndCookiesWithoutMutatingOriginal(t *testing.T) {
	inner := &captureRT{}
	rt := newUserAgentTransport(inner, "ua/1", []*http.Cookie{{Name: "d", Value: "xoxd-1"}}, zap.NewNop())
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	assert.Equal(t, "ua/1", inner.seen.Header.Get("User-Agent"))
	c, err := inner.seen.Cookie("d")
	require.NoError(t, err)
	assert.Equal(t, "xoxd-1", c.Value)

	assert.Empty(t, req.Header.Get("User-Agent"), "original request must not be mutated")
	_, err = req.Cookie("d")
	assert.ErrorIs(t, err, http.ErrNoCookie)
}

func TestDetectBrowserFromUserAgent(t *testing.T) {
	cases := map[string]utls.ClientHelloID{
		"Mozilla/5.0 Chrome/120.0 Safari/537.36":                utls.HelloChrome_Auto,
		"Mozilla/5.0 Chrome/120.0 Safari/537.36 Edg/120.0":      utls.HelloEdge_Auto,
		"Mozilla/5.0 Gecko/20100101 Firefox/121.0":              utls.HelloFirefox_Auto,
		"Mozilla/5.0 Version/17.1 Safari/605.1.15":              utls.HelloSafari_Auto,
		"Mozilla/5.0 Chrome/120.0 Version/17.1 Safari/605.1.15": utls.HelloSafari_Auto,
		"curl/8.4.0": utls.HelloChrome_Auto,
		"":           utls.HelloChrome_Auto,
	}
	for ua, want := range cases {
		assert.Equal(t, want, detectBrowserFromUserAgent(ua), ua)
	}
}

type errCloser struct {
	io.Reader
	closed bool
	err    error
}

func (e *errCloser) Close() error { e.closed = true; return e.err }

func TestCloseAfterBodyClosesBothAndKeepsFirstError(t *testing.T) {
	bodyErr, connErr := errors.New("body"), errors.New("conn")
	body := &errCloser{Reader: strings.NewReader(""), err: bodyErr}
	connClosed := false
	wrapped := &closeAfterBody{ReadCloser: body, close: func() error { connClosed = true; return connErr }}

	assert.ErrorIs(t, wrapped.Close(), bodyErr)
	assert.True(t, body.closed)
	assert.True(t, connClosed, "the connection closes even when the body close fails")

	body.err = nil
	assert.ErrorIs(t, wrapped.Close(), connErr, "a connection close error surfaces when the body closed cleanly")
}

func TestUTLSTransportHTTP1AgainstTLSServer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		_, _ = io.WriteString(w, "ok "+r.URL.Path)
	}))
	defer srv.Close()

	rt := newUTLSTransport(&utls.Config{InsecureSkipVerify: true}, utls.HelloChrome_Auto, zap.NewNop())
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL + "/hello")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "ok /hello", string(b))
	assert.Equal(t, "HTTP/1.1", resp.Header.Get("X-Proto"))
}

func TestUTLSTransportHTTP2AgainstTLSServer(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		_, _ = io.WriteString(w, "ok")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	rt := newUTLSTransport(&utls.Config{InsecureSkipVerify: true}, utls.HelloChrome_Auto, zap.NewNop())
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "HTTP/2.0", resp.Header.Get("X-Proto"))
}

func clearTransportEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SLACK_MCP_PROXY", "SLACK_MCP_CUSTOM_TLS", "SLACK_MCP_SERVER_CA", "SLACK_MCP_SERVER_CA_INSECURE", "SLACK_MCP_SERVER_CA_TOOLKIT", "SLACK_MCP_USER_AGENT"} {
		t.Setenv(k, "")
	}
}

type seenRequest struct {
	proto, userAgent, cookie, host string
}

// recordingHandler answers every request and keeps what it saw.
func recordingHandler(seen *seenRequest) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*seen = seenRequest{proto: r.Proto, userAgent: r.UserAgent(), host: r.URL.Host}
		if c, err := r.Cookie("d"); err == nil {
			seen.cookie = c.Value
		}
	}
}

func get(t *testing.T, c *http.Client, url string) error {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

func TestProvideHTTPClientStandardTransportDefaults(t *testing.T) {
	clearTransportEnv(t)
	var seen seenRequest
	plain := httptest.NewServer(recordingHandler(&seen))
	defer plain.Close()
	selfSigned := httptest.NewTLSServer(recordingHandler(&seen))
	defer selfSigned.Close()

	c, err := ProvideHTTPClient([]*http.Cookie{{Name: "d", Value: "v"}}, zap.NewNop())
	require.NoError(t, err)

	require.NoError(t, get(t, c, plain.URL))
	assert.Equal(t, defaultUA, seen.userAgent)
	assert.Equal(t, "v", seen.cookie)

	var unknownAuthority x509.UnknownAuthorityError
	assert.ErrorAs(t, get(t, c, selfSigned.URL), &unknownAuthority, "certificates are verified by default")
}

func TestProvideHTTPClientHonorsUserAgentInsecureAndCustomTLS(t *testing.T) {
	clearTransportEnv(t)
	firefox := "Mozilla/5.0 Gecko/20100101 Firefox/121.0"
	t.Setenv("SLACK_MCP_USER_AGENT", firefox)
	t.Setenv("SLACK_MCP_SERVER_CA_INSECURE", "true")
	t.Setenv("SLACK_MCP_CUSTOM_TLS", "1")
	var seen seenRequest
	selfSigned := httptest.NewTLSServer(recordingHandler(&seen))
	defer selfSigned.Close()

	c, err := ProvideHTTPClient(nil, zap.NewNop())
	require.NoError(t, err)

	require.NoError(t, get(t, c, selfSigned.URL), "insecure mode skips verification on the custom TLS path")
	assert.Equal(t, firefox, seen.userAgent)
}

func TestProvideHTTPClientUsesTheProxy(t *testing.T) {
	clearTransportEnv(t)
	var seen seenRequest
	proxy := httptest.NewServer(recordingHandler(&seen))
	defer proxy.Close()
	t.Setenv("SLACK_MCP_PROXY", proxy.URL)

	c, err := ProvideHTTPClient(nil, zap.NewNop())
	require.NoError(t, err)

	require.NoError(t, get(t, c, "http://slack.invalid/api/auth.test"))
	assert.Equal(t, "slack.invalid", seen.host, "the proxy receives the request for the target host")
}

func TestProvideHTTPClientTrustsTheServerCA(t *testing.T) {
	clearTransportEnv(t)
	var seen seenRequest
	srv := httptest.NewUnstartedServer(recordingHandler(&seen))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))
	t.Setenv("SLACK_MCP_SERVER_CA", ca)

	c, err := ProvideHTTPClient(nil, zap.NewNop())
	require.NoError(t, err)

	require.NoError(t, get(t, c, srv.URL), "server CA from SLACK_MCP_SERVER_CA must be trusted")
	assert.Equal(t, "HTTP/2.0", seen.proto)
}

func TestProvideHTTPClientUnparsableCAFallsBackToSystemCerts(t *testing.T) {
	clearTransportEnv(t)
	junk := filepath.Join(t.TempDir(), "junk.pem")
	require.NoError(t, os.WriteFile(junk, []byte("not a pem"), 0o600))
	t.Setenv("SLACK_MCP_SERVER_CA", junk)
	selfSigned := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer selfSigned.Close()

	c, err := ProvideHTTPClient(nil, zap.NewNop())
	require.NoError(t, err)

	var unknownAuthority x509.UnknownAuthorityError
	assert.ErrorAs(t, get(t, c, selfSigned.URL), &unknownAuthority)
}

func TestProvideHTTPClientRejectsConflictingAndBrokenEnv(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"proxy with custom TLS", map[string]string{"SLACK_MCP_PROXY": "http://p:1", "SLACK_MCP_CUSTOM_TLS": "1"}, "cannot be used together"},
		{"unparsable proxy", map[string]string{"SLACK_MCP_PROXY": "http://[::1"}, "parse SLACK_MCP_PROXY"},
		{"removed toolkit var", map[string]string{"SLACK_MCP_SERVER_CA_TOOLKIT": "1"}, "no longer supported"},
		{"missing CA file", map[string]string{"SLACK_MCP_SERVER_CA": filepath.Join(t.TempDir(), "nope.pem")}, "read SLACK_MCP_SERVER_CA"},
		{"CA with insecure", map[string]string{"SLACK_MCP_SERVER_CA": "x.pem", "SLACK_MCP_SERVER_CA_INSECURE": "true"}, "cannot be used together"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearTransportEnv(t)
			if ca, ok := tc.env["SLACK_MCP_SERVER_CA"]; ok && ca == "x.pem" {
				ca = filepath.Join(t.TempDir(), "x.pem")
				require.NoError(t, os.WriteFile(ca, []byte("x"), 0o600))
				tc.env["SLACK_MCP_SERVER_CA"] = ca
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			c, err := ProvideHTTPClient(nil, zap.NewNop())
			require.Error(t, err)
			assert.Nil(t, c)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
