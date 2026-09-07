package egress

import (
	"net/http"
	"testing"
	"time"
)

// TestTransportIsTuned guards against quietly drifting back to
// http.DefaultTransport. A caller that leaves Transport nil re-inherits
// MaxIdleConnsPerHost = 2 and ResponseHeaderTimeout = 0, and neither fails
// visibly until the proxy is under concurrent load.
func TestTransportIsTuned(t *testing.T) {
	tr := Transport()
	if tr.MaxIdleConnsPerHost <= 2 {
		t.Fatalf("MaxIdleConnsPerHost = %d, must exceed the Go default of 2",
			tr.MaxIdleConnsPerHost)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("ResponseHeaderTimeout is unset: an upstream that accepts a connection " +
			"and never answers will hang the request indefinitely")
	}
	if tr.IdleConnTimeout <= 0 {
		t.Fatal("IdleConnTimeout is unset: pooled connections never get reaped")
	}
	if tr.TLSHandshakeTimeout <= 0 {
		t.Fatal("TLSHandshakeTimeout is unset")
	}
	if Transport() != tr {
		t.Fatal("Transport() returned a different instance: callers no longer share one pool")
	}
}

func TestClientsShareOneTransport(t *testing.T) {
	stream := StreamClient()
	probe := NewClient(2 * time.Second)
	if stream.Transport != probe.Transport {
		t.Fatal("clients built here do not share one transport")
	}
	if stream.Transport != http.RoundTripper(Transport()) {
		t.Fatal("clients are not bound to the shared transport")
	}
	if stream.Timeout != StreamTotalTimeout {
		t.Fatalf("stream client timeout = %v, want %v", stream.Timeout, StreamTotalTimeout)
	}
}
