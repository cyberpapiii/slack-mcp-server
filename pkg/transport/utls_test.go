package transport

import (
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The custom-TLS HTTP/1.1 path reads a raw connection; a stalled call must
// still end.
func TestUTLSCallEndsWhenTheStallTimerFires(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())

	next := newUTLSTransport(&utls.Config{RootCAs: roots}, utls.HelloChrome_Auto, zap.NewNop())
	client := &http.Client{Transport: stallTransport{next: next, stall: testStall}}

	started := time.Now()
	_, err := client.Get(server.URL)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout())
	assert.Less(t, time.Since(started), 2*time.Second)
}
