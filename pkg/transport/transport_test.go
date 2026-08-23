package transport

import (
	"bufio"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	rt := NewUserAgentTransport(inner, "ua/1", []*http.Cookie{{Name: "d", Value: "xoxd-1"}}, zap.NewNop())
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

func TestCloseAfterBodyClosesBothAndKeepsFirstError(t *testing.T) {
	closed := false
	body := &closeAfterBody{
		ReadCloser: io.NopCloser(strings.NewReader("")),
		close:      func() error { closed = true; return nil },
	}
	require.NoError(t, body.Close())
	assert.True(t, closed)
}

func TestUTLSTransportHTTP1AgainstTLSServer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		_, _ = io.WriteString(w, "ok "+r.URL.Path)
	}))
	defer srv.Close()

	rt := NewUTLSTransport(&utls.Config{InsecureSkipVerify: true}, nil, utls.HelloChrome_Auto, zap.NewNop())
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

	rt := NewUTLSTransport(&utls.Config{InsecureSkipVerify: true}, nil, utls.HelloChrome_Auto, zap.NewNop())
	client := &http.Client{Transport: rt}

	resp, err := client.Get(srv.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "HTTP/2.0", resp.Header.Get("X-Proto"))
}

// connectProxy is the smallest CONNECT tunnel: it records the auth header and
// the target, then splices bytes both ways.
func connectProxy(t *testing.T) (addr string, seen *http.Request) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	seen = &http.Request{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				*seen = *req
				if req.Method != http.MethodConnect {
					_, _ = io.WriteString(conn, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
					return
				}
				if req.Header.Get("Proxy-Authorization") == "" {
					_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
					return
				}
				upstream, err := net.Dial("tcp", req.Host)
				if err != nil {
					_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
					return
				}
				defer upstream.Close()
				_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(upstream, conn); done <- struct{}{} }()
				go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
				<-done
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), seen
}

func TestUTLSTransportTunnelsThroughConnectProxyWithBasicAuth(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "via proxy")
	}))
	defer srv.Close()
	proxyAddr, seen := connectProxy(t)

	proxyURL, _ := url.Parse("http://user:pa%3Ass@" + proxyAddr)
	rt := NewUTLSTransport(&utls.Config{InsecureSkipVerify: true}, http.ProxyURL(proxyURL), utls.HelloChrome_Auto, zap.NewNop())
	resp, err := (&http.Client{Transport: rt}).Get(srv.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "via proxy", string(b))

	assert.Equal(t, http.MethodConnect, seen.Method)
	assert.Equal(t, strings.TrimPrefix(srv.URL, "https://"), seen.Host)
	assert.Equal(t, "Basic "+basicAuth("user", "pa:ss"), seen.Header.Get("Proxy-Authorization"))
}

func TestUTLSTransportProxyRejectionIsAnError(t *testing.T) {
	proxyAddr, _ := connectProxy(t)
	proxyURL, _ := url.Parse("http://" + proxyAddr) // no credentials: proxy answers 407
	rt := NewUTLSTransport(&utls.Config{InsecureSkipVerify: true}, http.ProxyURL(proxyURL), utls.HelloChrome_Auto, zap.NewNop())
	_, err := (&http.Client{Transport: rt}).Get("https://127.0.0.1:1/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy returned status 407")
}

func TestUTLSTransportProxyDialError(t *testing.T) {
	proxyURL, _ := url.Parse("http://127.0.0.1:1")
	rt := NewUTLSTransport(&utls.Config{InsecureSkipVerify: true}, http.ProxyURL(proxyURL), utls.HelloChrome_Auto, zap.NewNop())
	_, err := (&http.Client{Transport: rt}).Get("https://example.invalid/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxy dial error")
}

func clearTransportEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SLACK_MCP_PROXY", "SLACK_MCP_CUSTOM_TLS", "SLACK_MCP_SERVER_CA", "SLACK_MCP_SERVER_CA_INSECURE", "SLACK_MCP_SERVER_CA_TOOLKIT", "SLACK_MCP_USER_AGENT"} {
		t.Setenv(k, "")
	}
}

func unwrapUA(t *testing.T, c *http.Client) *UserAgentTransport {
	t.Helper()
	ua, ok := c.Transport.(*UserAgentTransport)
	require.True(t, ok, "outermost transport must be UserAgentTransport, got %T", c.Transport)
	return ua
}

func TestProvideHTTPClientStandardTransportDefaults(t *testing.T) {
	clearTransportEnv(t)
	c, err := ProvideHTTPClient([]*http.Cookie{{Name: "d", Value: "v"}}, zap.NewNop())
	require.NoError(t, err)
	ua := unwrapUA(t, c)
	assert.Equal(t, defaultUA, ua.userAgent)
	assert.Len(t, ua.cookies, 1)
	inner, ok := ua.roundTripper.(*http.Transport)
	require.True(t, ok, "standard path wraps *http.Transport, got %T", ua.roundTripper)
	assert.False(t, inner.TLSClientConfig.InsecureSkipVerify)
	assert.True(t, inner.ForceAttemptHTTP2)
}

func TestProvideHTTPClientHonorsUserAgentInsecureAndCustomTLS(t *testing.T) {
	clearTransportEnv(t)
	t.Setenv("SLACK_MCP_USER_AGENT", "Mozilla/5.0 Gecko/20100101 Firefox/121.0")
	t.Setenv("SLACK_MCP_SERVER_CA_INSECURE", "true")
	t.Setenv("SLACK_MCP_CUSTOM_TLS", "1")
	c, err := ProvideHTTPClient(nil, zap.NewNop())
	require.NoError(t, err)
	ua := unwrapUA(t, c)
	assert.Equal(t, "Mozilla/5.0 Gecko/20100101 Firefox/121.0", ua.userAgent)
	inner, ok := ua.roundTripper.(*uTLSTransport)
	require.True(t, ok, "custom TLS path wraps *uTLSTransport, got %T", ua.roundTripper)
	assert.Equal(t, utls.HelloFirefox_Auto, inner.clientHelloID)
	assert.True(t, inner.tlsConfig.InsecureSkipVerify)
	assert.Nil(t, inner.proxy)
}

func TestProvideHTTPClientProxyAndServerCA(t *testing.T) {
	clearTransportEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	pem := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(pem, pemEncode(srv.Certificate().Raw), 0o600))

	t.Setenv("SLACK_MCP_PROXY", "http://proxy.local:3128")
	t.Setenv("SLACK_MCP_SERVER_CA", pem)
	c, err := ProvideHTTPClient(nil, zap.NewNop())
	require.NoError(t, err)
	inner, ok := unwrapUA(t, c).roundTripper.(*http.Transport)
	require.True(t, ok)
	u, err := inner.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "slack.com"}})
	require.NoError(t, err)
	assert.Equal(t, "proxy.local:3128", u.Host)

	pool := inner.TLSClientConfig.RootCAs
	require.NotNil(t, pool)
	_, err = srv.Certificate().Verify(verifyOpts(pool))
	assert.NoError(t, err, "server CA from SLACK_MCP_SERVER_CA must be trusted")
}

func TestProvideHTTPClientUnparsableCAWarnsAndContinues(t *testing.T) {
	clearTransportEnv(t)
	pem := filepath.Join(t.TempDir(), "junk.pem")
	require.NoError(t, os.WriteFile(pem, []byte("not a pem"), 0o600))
	t.Setenv("SLACK_MCP_SERVER_CA", pem)
	c, err := ProvideHTTPClient(nil, zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.NotNil(t, unwrapUA(t, c).roundTripper.(*http.Transport).TLSClientConfig)
}

func TestBasicAuth(t *testing.T) {
	assert.Equal(t, "dXNlcjpwYXNz", basicAuth("user", "pass"))
}

func pemEncode(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func verifyOpts(pool *x509.CertPool) x509.VerifyOptions {
	return x509.VerifyOptions{Roots: pool, DNSName: "example.com"}
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

func TestRateLimited(t *testing.T) {
	assert.Nil(t, RateLimited(&http.Response{StatusCode: http.StatusOK}))

	withHeader := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{" 7 "}}}
	limited := RateLimited(withHeader)
	require.NotNil(t, limited)
	assert.Equal(t, 7*time.Second, limited.RetryAfter)

	for _, raw := range []string{"", "soon", "-1"} {
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{raw}}}
		limited := RateLimited(resp)
		require.NotNil(t, limited, raw)
		assert.Equal(t, defaultRetryAfter, limited.RetryAfter, raw)
	}
}

func TestSlackDomain(t *testing.T) {
	t.Setenv("SLACK_MCP_GOVSLACK", "")
	assert.Equal(t, "slack.com", SlackDomain())
	assert.Equal(t, "https://slack.com/api/", SlackAPIBase())

	t.Setenv("SLACK_MCP_GOVSLACK", "1")
	assert.Equal(t, "slack-gov.com", SlackDomain())
	assert.Equal(t, "https://slack-gov.com/api/", SlackAPIBase())
}
