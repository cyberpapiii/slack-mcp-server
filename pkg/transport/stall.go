package transport

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// stallTimeout is how long a Slack call may go without progress: no response
// headers yet, or no bytes moving in either body. It bounds a hung call without
// capping a large file transfer that keeps moving.
const stallTimeout = 30 * time.Second

// stallError is the net.Error a stalled call fails with, so callers classify
// it as a timeout rather than a cancellation.
type stallError struct{}

func (stallError) Error() string {
	return "Slack call stalled: no progress for " + stallTimeout.String()
}
func (stallError) Timeout() bool   { return true }
func (stallError) Temporary() bool { return true }

type stallTransport struct {
	next  http.RoundTripper
	stall time.Duration
}

func (t stallTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	watch := &stallWatch{cancel: cancel}
	watch.timer = time.AfterFunc(t.stall, watch.fire)
	req = req.WithContext(ctx)
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &progressBody{ReadCloser: req.Body, watch: watch, stall: t.stall}
	}
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		watch.stop()
		if watch.stalled.Load() {
			return nil, stallError{}
		}
		return nil, err
	}
	resp.Body = &progressBody{ReadCloser: resp.Body, watch: watch, stall: t.stall, closes: true}
	return resp, nil
}

type stallWatch struct {
	timer   *time.Timer
	cancel  context.CancelFunc
	stalled atomic.Bool
}

func (w *stallWatch) fire() {
	w.stalled.Store(true)
	w.cancel()
}

func (w *stallWatch) stop() {
	w.timer.Stop()
	w.cancel()
}

// progressBody restarts the stall timer when a read starts and when it
// returns bytes. The response body's
// Close ends the call.
type progressBody struct {
	io.ReadCloser
	watch  *stallWatch
	stall  time.Duration
	closes bool
}

func (b *progressBody) Read(p []byte) (int, error) {
	b.watch.timer.Reset(b.stall)
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.watch.timer.Reset(b.stall)
	}
	if err != nil && err != io.EOF && b.watch.stalled.Load() {
		return n, stallError{}
	}
	return n, err
}

func (b *progressBody) Close() error {
	if b.closes {
		b.watch.stop()
	}
	return b.ReadCloser.Close()
}
