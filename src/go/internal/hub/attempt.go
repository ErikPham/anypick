package hub

import (
	"bytes"
	"net/http"
)

// attemptWriter holds a failed upstream response back so the hub can fail over
// to another account, and streams a successful one straight to the caller.
//
// The rule is deliberately simpler than the pool ring's writer: a status below
// 400 commits immediately (a stream must not wait on a decision that cannot
// change it), anything else buffers until the chain either replays it verbatim
// or replaces it with the next account's attempt. The hub has no provider-shaped
// egress leaves of its own — it calls proxy.Serve, which owns that table — so
// this is all the capture it needs.
type attemptWriter struct {
	inner     http.ResponseWriter
	committed bool
	status    int
	header    http.Header
	body      bytes.Buffer
}

func newAttemptWriter(inner http.ResponseWriter) *attemptWriter {
	return &attemptWriter{inner: inner, header: http.Header{}}
}

// committedResponse reports whether bytes already reached the caller, which
// makes failover impossible for this request.
func (w *attemptWriter) committedResponse() bool { return w.committed }

func (w *attemptWriter) Header() http.Header {
	if w.committed {
		return w.inner.Header()
	}
	return w.header
}

func (w *attemptWriter) WriteHeader(code int) {
	if w.committed {
		w.inner.WriteHeader(code)
		return
	}
	if code < 400 {
		w.commit(code)
		return
	}
	if w.status == 0 {
		w.status = code
	}
}

func (w *attemptWriter) Write(p []byte) (int, error) {
	if w.committed {
		return w.inner.Write(p)
	}
	if w.status == 0 {
		// A body without a status is a 200 by HTTP's rules; treat it as
		// committed so a streaming response is not buffered forever.
		w.commit(http.StatusOK)
		return w.inner.Write(p)
	}
	return w.body.Write(p)
}

func (w *attemptWriter) Flush() {
	if f, ok := w.inner.(http.Flusher); ok && w.committed {
		f.Flush()
	}
}

// commit passes control to the real writer, carrying the headers collected so
// far. Content-Length is dropped: the framing belongs to whoever writes the
// stream (chunked for a stream, replayed body for a captured failure).
func (w *attemptWriter) commit(code int) {
	if w.committed {
		return
	}
	dst := w.inner.Header()
	for name, values := range w.header {
		if name == "Content-Length" {
			continue
		}
		dst[name] = values
	}
	w.committed = true
	w.inner.WriteHeader(code)
}

// bodyPreview is what quota classification reads. It is the captured body only,
// so a committed stream never reaches it.
func (w *attemptWriter) bodyPreview() string { return w.body.String() }

// headerValue reads a captured header; safe when no response ever arrived.
func (w *attemptWriter) headerValue(name string) string { return w.header.Get(name) }

// statusCode is the captured status, 0 when nothing was captured.
func (w *attemptWriter) statusCode() int { return w.status }

func (w *attemptWriter) replay() {
	if w.committed {
		return
	}
	dst := w.inner.Header()
	for name, values := range w.header {
		if name == "Content-Length" {
			continue
		}
		dst[name] = values
	}
	status := w.status
	if status == 0 {
		status = http.StatusBadGateway
	}
	w.committed = true
	w.inner.WriteHeader(status)
	_, _ = w.inner.Write(w.body.Bytes())
	if f, ok := w.inner.(http.Flusher); ok {
		f.Flush()
	}
}
