// Package egress owns outbound HTTP for the local serving layer.
//
// Why this exists: every call site used to build its own
// `&http.Client{Timeout: ...}` with a nil Transport, so all of them silently
// inherited http.DefaultTransport. That default carries two properties that
// hurt a streaming proxy specifically:
//
//   - MaxIdleConnsPerHost = 2. Past the second concurrent stream to the same
//     vendor, each completed connection is closed and the next one pays TCP
//     and TLS handshake again.
//   - ResponseHeaderTimeout = 0. A vendor that accepts the connection and
//     never sends headers hangs the request until something else gives up.
//
// Nothing here knows about providers, accounts, or credentials: credential
// injection, upstream selection and failover stay in internal/proxy and
// internal/upstream (ADR 0016). This package only answers "how do we leave
// this process".
package egress

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// DialTimeout bounds connecting to an upstream.
	DialTimeout = 10 * time.Second
	// KeepAlive is the TCP keep-alive period on pooled connections.
	KeepAlive = 30 * time.Second
	// TLSHandshakeTimeout bounds the TLS handshake.
	TLSHandshakeTimeout = 10 * time.Second
	// ResponseHeaderTimeout bounds the wait for upstream response headers.
	// This is the control that was missing entirely: accepting a socket and
	// never answering used to hang a request indefinitely.
	ResponseHeaderTimeout = 60 * time.Second
	// IdleConnTimeout bounds how long a pooled connection survives unused.
	IdleConnTimeout = 90 * time.Second
	// ExpectContinueTimeout bounds the wait for a 100-Continue response.
	ExpectContinueTimeout = 1 * time.Second
	// MaxIdleConns is the total pooled connection budget.
	MaxIdleConns = 512
	// MaxIdleConnsPerHost is the number that actually matters here. The Go
	// default is 2, which turns every burst past two concurrent streams into a
	// round of connection churn against the same vendor.
	MaxIdleConnsPerHost = 64
	// StreamTotalTimeout is a backstop for a stream that never ends, not the
	// primary control — that is ResponseHeaderTimeout. It is deliberately far
	// beyond any plausible model response so it cannot cut a legitimate one.
	StreamTotalTimeout = 30 * time.Minute
)

var (
	sharedOnce sync.Once
	shared     *http.Transport
)

// Transport returns the process-wide outbound transport. Every client and
// every ReverseProxy on the request path must share it, otherwise the pool
// they are trying to share does not exist.
func Transport() *http.Transport {
	sharedOnce.Do(func() {
		shared = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   DialTimeout,
				KeepAlive: KeepAlive,
			}).DialContext,
			MaxIdleConns:          MaxIdleConns,
			MaxIdleConnsPerHost:   MaxIdleConnsPerHost,
			IdleConnTimeout:       IdleConnTimeout,
			TLSHandshakeTimeout:   TLSHandshakeTimeout,
			ExpectContinueTimeout: ExpectContinueTimeout,
			ResponseHeaderTimeout: ResponseHeaderTimeout,
			ForceAttemptHTTP2:     true,
		}
	})
	return shared
}

// NewClient returns a client bound to the shared transport. Use it for any
// outbound call on the request path instead of a bare &http.Client{}.
//
// A short total timeout is fine for probes. Model requests that stream should
// use StreamClient: setting a tight total timeout there cuts legitimate
// responses.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: Transport(), Timeout: timeout}
}

// StreamClient returns a client for requests that may stream for a long time.
func StreamClient() *http.Client { return NewClient(StreamTotalTimeout) }
