package transport

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testStall = 150 * time.Millisecond

func stallClient() *http.Client {
	return &http.Client{Transport: stallTransport{next: http.DefaultTransport, stall: testStall}}
}

func TestStallTransportFailsAHungCallAsATimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	_, err := stallClient().Get(server.URL)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout())
}

// A transfer that keeps moving may take longer than the stall timeout.
func TestStallTransportLetsASteadyTransferOutlastTheTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		for range 6 {
			_, _ = w.Write([]byte("chunk"))
			flusher.Flush()
			time.Sleep(testStall / 2)
		}
	}))
	defer server.Close()

	resp, err := stallClient().Get(server.URL)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("chunk", 6), string(body))
}

func TestStallTransportFailsABodyThatStops(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("start"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	resp, err := stallClient().Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout())
}

// An upload counts as progress while its body is being sent.
func TestStallTransportLetsASlowUploadThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(strings.Repeat("x", int(n))))
	}))
	defer server.Close()

	reader, writer := io.Pipe()
	go func() {
		for range 6 {
			_, _ = writer.Write([]byte("part"))
			time.Sleep(testStall / 2)
		}
		_ = writer.Close()
	}()
	resp, err := stallClient().Post(server.URL, "application/octet-stream", reader)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	assert.Len(t, body, 24)
}
