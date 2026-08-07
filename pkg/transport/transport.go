package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/text"
	utls "github.com/refraction-networking/utls"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// UserAgentTransport wraps another RoundTripper to add User-Agent and cookies.
type UserAgentTransport struct {
	roundTripper http.RoundTripper
	userAgent    string
	cookies      []*http.Cookie
	logger       *zap.Logger
}

func NewUserAgentTransport(roundTripper http.RoundTripper, userAgent string, cookies []*http.Cookie, logger *zap.Logger) *UserAgentTransport {
	return &UserAgentTransport{
		roundTripper: roundTripper,
		userAgent:    userAgent,
		cookies:      cookies,
		logger:       logger,
	}
}

func (t *UserAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clonedReq := req.Clone(req.Context())
	clonedReq.Header.Set("User-Agent", t.userAgent)

	for _, cookie := range t.cookies {
		clonedReq.AddCookie(cookie)
	}

	t.logger.Debug("Making request", zap.String("url", clonedReq.URL.String()))

	resp, err := t.roundTripper.RoundTrip(clonedReq)
	if err != nil {
		t.logger.Error("Request failed", zap.Error(err))
	}
	return resp, err
}

type uTLSTransport struct {
	dialer         *net.Dialer
	tlsConfig      *utls.Config
	proxy          func(*http.Request) (*url.URL, error)
	clientHelloID  utls.ClientHelloID
	http2Transport *http2.Transport
	logger         *zap.Logger
}

func NewUTLSTransport(tlsConfig *utls.Config, proxy func(*http.Request) (*url.URL, error), clientHelloID utls.ClientHelloID, logger *zap.Logger) *uTLSTransport {
	return &uTLSTransport{
		dialer: &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		},
		tlsConfig:     tlsConfig,
		proxy:         proxy,
		clientHelloID: clientHelloID,
		http2Transport: &http2.Transport{
			AllowHTTP: false,
			DialTLS: func(network, addr string, cfg *tls.Config) (net.Conn, error) {
				return nil, fmt.Errorf("DialTLS should not be called")
			},
		},
		logger: logger,
	}
}

func (t *uTLSTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	targetAddr := req.URL.Host
	if req.URL.Port() == "" {
		if req.URL.Scheme == "https" {
			targetAddr += ":443"
		} else {
			targetAddr += ":80"
		}
	}

	var conn net.Conn
	var err error

	if t.proxy != nil {
		proxyURL, err := t.proxy(req)
		if err != nil {
			return nil, fmt.Errorf("proxy error: %w", err)
		}

		if proxyURL != nil {
			conn, err = t.dialProxy(req.Context(), proxyURL, targetAddr)
			if err != nil {
				return nil, fmt.Errorf("proxy dial error: %w", err)
			}
		}
	}

	if conn == nil {
		conn, err = t.dialer.DialContext(req.Context(), "tcp", targetAddr)
		if err != nil {
			return nil, fmt.Errorf("dial error: %w", err)
		}
	}

	if req.URL.Scheme == "https" {
		tlsConn, err := t.establishTLS(conn, req.URL.Hostname())
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS error: %w", err)
		}
		conn = tlsConn

		if uconn, ok := tlsConn.(*utls.UConn); ok {
			alpn := uconn.ConnectionState().NegotiatedProtocol

			t.logger.Debug("Negotiated protocol", zap.String("protocol", alpn))

			switch alpn {
			case "h2":
				clientConn, err := t.http2Transport.NewClientConn(conn)
				if err != nil {
					conn.Close()
					return nil, fmt.Errorf("HTTP/2 client connection error: %w", err)
				}
				t.logger.Debug("Using HTTP/2 transport for request", zap.String("request", req.URL.String()))
				resp, err := clientConn.RoundTrip(req)
				if err != nil {
					clientConn.Close()
					return nil, err
				}
				resp.Body = &closeAfterBody{ReadCloser: resp.Body, close: clientConn.Close}
				return resp, nil
			default:
				t.logger.Debug("Using HTTP/1.1 transport for request", zap.String("request", req.URL.String()))
			}
		}
	}

	err = req.Write(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("request write error: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		conn.Close()
		return nil, err
	}

	if resp.Close || resp.Header.Get("Connection") == "close" {
		conn.Close()
	}

	return resp, nil
}

type closeAfterBody struct {
	io.ReadCloser
	close func() error
}

func (c *closeAfterBody) Close() error {
	err := c.ReadCloser.Close()
	if cerr := c.close(); err == nil {
		err = cerr
	}
	return err
}

func (t *uTLSTransport) dialProxy(ctx context.Context, proxyURL *url.URL, targetAddr string) (net.Conn, error) {
	proxyAddr := proxyURL.Host
	if proxyURL.Port() == "" {
		if proxyURL.Scheme == "https" {
			proxyAddr += ":443"
		} else {
			proxyAddr += ":80"
		}
	}

	conn, err := t.dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}

	if proxyURL.Scheme == "https" {
		tlsConfig := &tls.Config{
			ServerName:         proxyURL.Hostname(),
			InsecureSkipVerify: t.tlsConfig.InsecureSkipVerify,
			RootCAs:            t.tlsConfig.RootCAs,
		}
		tlsConn := tls.Client(conn, tlsConfig)
		err = tlsConn.Handshake()
		if err != nil {
			conn.Close()
			return nil, err
		}
		conn = tlsConn
	}

	connectReq := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: targetAddr},
		Host:   targetAddr,
		Header: make(http.Header),
	}

	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		connectReq.Header.Set("Proxy-Authorization", "Basic "+basicAuth(username, password))
	}

	err = connectReq.Write(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		conn.Close()
		return nil, fmt.Errorf("proxy returned status %d", resp.StatusCode)
	}

	return conn, nil
}

func (t *uTLSTransport) establishTLS(conn net.Conn, serverName string) (net.Conn, error) {
	config := t.tlsConfig.Clone()
	config.ServerName = serverName

	t.logger.Debug("Starting uTLS handshake with server", zap.String("server", serverName))
	t.logger.Debug("Using ClientHello fingerprint", zap.String("fingerprint", t.getClientHelloName()))

	tlsConn := utls.UClient(conn, config, t.clientHelloID)

	err := tlsConn.Handshake()
	if err != nil {
		t.logger.Error("uTLS handshake failed", zap.Error(err))
		return nil, err
	}

	state := tlsConn.ConnectionState()
	t.logger.Debug("uTLS handshake successful",
		zap.String("cipher_suite", fmt.Sprintf("%x", state.CipherSuite)),
		zap.String("version", fmt.Sprintf("%x", state.Version)),
		zap.String("negotiated_protocol", fmt.Sprintf("%x", state.NegotiatedProtocol)),
		zap.String("server_certificates", fmt.Sprintf("%v", text.HumanizeCertificates(state.PeerCertificates))),
	)

	return tlsConn, nil
}

func (t *uTLSTransport) getClientHelloName() string {
	switch t.clientHelloID {
	case utls.HelloChrome_Auto:
		return "Chrome (Auto)"
	case utls.HelloFirefox_Auto:
		return "Firefox (Auto)"
	case utls.HelloSafari_Auto:
		return "Safari (Auto)"
	case utls.HelloEdge_Auto:
		return "Edge (Auto)"
	default:
		return fmt.Sprintf("Unknown (%v)", t.clientHelloID)
	}
}

func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}

func detectBrowserFromUserAgent(userAgent string) utls.ClientHelloID {
	ua := strings.ToLower(userAgent)

	if strings.Contains(ua, "edg/") || strings.Contains(ua, "edge/") {
		return utls.HelloEdge_Auto
	}

	if strings.Contains(ua, "firefox/") {
		return utls.HelloFirefox_Auto
	}

	if strings.Contains(ua, "safari/") &&
		(!strings.Contains(ua, "chrome/") || strings.Contains(ua, "version/")) {
		return utls.HelloSafari_Auto
	}

	if strings.Contains(ua, "chrome/") {
		return utls.HelloChrome_Auto
	}

	return utls.HelloChrome_Auto
}

// ProvideHTTPClient creates an HTTP client with optional uTLS support.
func ProvideHTTPClient(cookies []*http.Cookie, logger *zap.Logger) *http.Client {
	if os.Getenv("SLACK_MCP_PROXY") != "" && os.Getenv("SLACK_MCP_CUSTOM_TLS") != "" {
		logger.Fatal("SLACK_MCP_PROXY and SLACK_MCP_CUSTOM_TLS cannot be used together",
			zap.String("reason", "Custom TLS fingerprinting has no effect when using a proxy, as the target server sees the proxy's TLS handshake"))
	}

	var proxy func(*http.Request) (*url.URL, error)
	if proxyURL := os.Getenv("SLACK_MCP_PROXY"); proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			logger.Fatal("Failed to parse proxy URL",
				zap.String("proxy_url", proxyURL),
				zap.Error(err))
		}
		proxy = http.ProxyURL(parsed)
	}

	rootCAs, _ := x509.SystemCertPool()
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}

	if os.Getenv("SLACK_MCP_SERVER_CA_TOOLKIT") != "" {
		logger.Fatal("SLACK_MCP_SERVER_CA_TOOLKIT is no longer supported (embedded CA expired); set SLACK_MCP_SERVER_CA to your HTTP Toolkit CA PEM path")
	}

	if localCertFile := os.Getenv("SLACK_MCP_SERVER_CA"); localCertFile != "" {
		certs, err := os.ReadFile(localCertFile)
		if err != nil {
			logger.Fatal("Failed to read local certificate file",
				zap.String("cert_file", localCertFile),
				zap.Error(err))
		}
		if ok := rootCAs.AppendCertsFromPEM(certs); !ok {
			logger.Warn("No certs appended, using system certs only")
		}
	}

	insecure := false
	if os.Getenv("SLACK_MCP_SERVER_CA_INSECURE") != "" {
		if localCertFile := os.Getenv("SLACK_MCP_SERVER_CA"); localCertFile != "" {
			logger.Fatal("SLACK_MCP_SERVER_CA and SLACK_MCP_SERVER_CA_INSECURE cannot be used together")
		}
		insecure = true
	}

	userAgent := defaultUA
	if ua := os.Getenv("SLACK_MCP_USER_AGENT"); ua != "" {
		userAgent = ua
	}

	var transport http.RoundTripper

	if useCustomTLS := os.Getenv("SLACK_MCP_CUSTOM_TLS"); useCustomTLS != "" {
		logger.Debug("Custom TLS handshake enabled",
			zap.String("user_agent", userAgent))

		utlsConfig := &utls.Config{
			InsecureSkipVerify: insecure,
			RootCAs:            rootCAs,
		}

		clientHelloID := detectBrowserFromUserAgent(userAgent)

		var detectedBrowser string
		switch clientHelloID {
		case utls.HelloChrome_Auto:
			detectedBrowser = "Chrome"
		case utls.HelloFirefox_Auto:
			detectedBrowser = "Firefox"
		case utls.HelloSafari_Auto:
			detectedBrowser = "Safari"
		case utls.HelloEdge_Auto:
			detectedBrowser = "Edge"
		}

		logger.Debug("TLS Fingerprinting Details",
			zap.String("detected_browser", detectedBrowser),
			zap.String("client_hello_id", fmt.Sprintf("%v", clientHelloID.Version)),
			zap.String("user_agent", userAgent),
		)

		transport = NewUTLSTransport(utlsConfig, proxy, clientHelloID, logger)
	} else {
		logger.Debug("Using standard TLS handshake")

		transport = &http.Transport{
			Proxy: proxy,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: insecure,
				RootCAs:            rootCAs,
			},
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}

	transport = NewUserAgentTransport(transport, userAgent, cookies, logger)

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	return client
}
