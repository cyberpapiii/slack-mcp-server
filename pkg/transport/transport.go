package transport

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/korotovsky/slack-mcp-server/pkg/envutil"
	utls "github.com/refraction-networking/utls"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
)

const defaultUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

type userAgentTransport struct {
	roundTripper http.RoundTripper
	userAgent    string
	cookies      []*http.Cookie
	logger       *zap.Logger
}

func newUserAgentTransport(roundTripper http.RoundTripper, userAgent string, cookies []*http.Cookie, logger *zap.Logger) *userAgentTransport {
	return &userAgentTransport{
		roundTripper: roundTripper,
		userAgent:    userAgent,
		cookies:      cookies,
		logger:       logger,
	}
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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
	clientHelloID  utls.ClientHelloID
	http2Transport *http2.Transport
	logger         *zap.Logger
}

// newUTLSTransport dials Slack directly: ProvideHTTPClient refuses
// SLACK_MCP_PROXY together with SLACK_MCP_CUSTOM_TLS, so there is no proxy to
// tunnel through.
func newUTLSTransport(tlsConfig *utls.Config, clientHelloID utls.ClientHelloID, logger *zap.Logger) *uTLSTransport {
	return &uTLSTransport{
		dialer: &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		},
		tlsConfig:     tlsConfig,
		clientHelloID: clientHelloID,
		// RoundTrip dials with uTLS and hands the conn to NewClientConn, so
		// this transport never dials.
		http2Transport: &http2.Transport{},
		logger:         logger,
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

	conn, err := t.dialer.DialContext(req.Context(), "tcp", targetAddr)
	if err != nil {
		return nil, fmt.Errorf("dial error: %w", err)
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

	// Always close the one-shot dial when the body is drained; keep-alive
	// reuse is not implemented for CUSTOM_TLS HTTP/1.1.
	resp.Body = &closeAfterBody{ReadCloser: resp.Body, close: conn.Close}
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

func (t *uTLSTransport) establishTLS(conn net.Conn, serverName string) (net.Conn, error) {
	config := t.tlsConfig.Clone()
	config.ServerName = serverName

	t.logger.Debug("Starting uTLS handshake with server", zap.String("server", serverName))
	t.logger.Debug("Using ClientHello fingerprint", zap.String("fingerprint", clientHelloName(t.clientHelloID)))

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
		zap.String("server_certificates", fmt.Sprintf("%v", humanizeCertificates(state.PeerCertificates))),
	)

	return tlsConn, nil
}

func clientHelloName(id utls.ClientHelloID) string {
	switch id {
	case utls.HelloChrome_Auto:
		return "Chrome"
	case utls.HelloFirefox_Auto:
		return "Firefox"
	case utls.HelloSafari_Auto:
		return "Safari"
	case utls.HelloEdge_Auto:
		return "Edge"
	default:
		return fmt.Sprintf("Unknown (%v)", id)
	}
}

func humanizeCertificates(certs []*x509.Certificate) string {
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

// ProvideHTTPClient builds the HTTP client every Slack call shares. Build it
// once per credential set: each call owns a fresh connection pool.
func ProvideHTTPClient(cookies []*http.Cookie, logger *zap.Logger) (*http.Client, error) {
	if os.Getenv("SLACK_MCP_PROXY") != "" && os.Getenv("SLACK_MCP_CUSTOM_TLS") != "" {
		return nil, errors.New("SLACK_MCP_PROXY and SLACK_MCP_CUSTOM_TLS cannot be used together: custom TLS fingerprinting has no effect when using a proxy, as the target server sees the proxy's TLS handshake")
	}

	var proxy func(*http.Request) (*url.URL, error)
	if proxyURL := os.Getenv("SLACK_MCP_PROXY"); proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse SLACK_MCP_PROXY %q: %w", proxyURL, err)
		}
		proxy = http.ProxyURL(parsed)
	}

	rootCAs, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system certificate pool: %w", err)
	}

	if os.Getenv("SLACK_MCP_SERVER_CA_TOOLKIT") != "" {
		return nil, errors.New("SLACK_MCP_SERVER_CA_TOOLKIT is no longer supported (embedded CA expired); set SLACK_MCP_SERVER_CA to your HTTP Toolkit CA PEM path")
	}

	if localCertFile := os.Getenv("SLACK_MCP_SERVER_CA"); localCertFile != "" {
		certs, err := os.ReadFile(localCertFile)
		if err != nil {
			return nil, fmt.Errorf("read SLACK_MCP_SERVER_CA %q: %w", localCertFile, err)
		}
		if ok := rootCAs.AppendCertsFromPEM(certs); !ok {
			logger.Warn("No certs appended, using system certs only")
		}
	}

	insecure := false
	if envutil.IsTruthy(os.Getenv("SLACK_MCP_SERVER_CA_INSECURE")) {
		if localCertFile := os.Getenv("SLACK_MCP_SERVER_CA"); localCertFile != "" {
			return nil, errors.New("SLACK_MCP_SERVER_CA and SLACK_MCP_SERVER_CA_INSECURE cannot be used together")
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

		logger.Debug("TLS Fingerprinting Details",
			zap.String("detected_browser", clientHelloName(clientHelloID)),
			zap.String("client_hello_id", fmt.Sprintf("%v", clientHelloID.Version)),
			zap.String("user_agent", userAgent),
		)

		transport = newUTLSTransport(utlsConfig, clientHelloID, logger)
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

	transport = newUserAgentTransport(transport, userAgent, cookies, logger)

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	return client, nil
}
