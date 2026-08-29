package protocol

import (
	"io"
	"net/http"
	"strings"
)

// sseRecorder captures what the translator writes to a ResponseWriter.
type sseRecorder struct {
	buf strings.Builder
}

func newSSERecorder() *sseRecorder { return &sseRecorder{} }

func (r *sseRecorder) Header() http.Header         { return http.Header{} }
func (r *sseRecorder) Write(p []byte) (int, error) { return r.buf.Write(p) }
func (r *sseRecorder) WriteHeader(int)             {}
func (r *sseRecorder) Flush()                      {}

func (r *sseRecorder) rawLines() []string {
	return strings.Split(r.buf.String(), "\n\n")
}

type namedEvent struct {
	name string
	data string
}

func (r *sseRecorder) events() []string {
	var out []string
	for _, block := range r.rawLines() {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "event: ") {
				out = append(out, strings.TrimPrefix(line, "event: "))
			}
		}
	}
	return out
}

var _ = namedEvent{}

// fakeSSEResponse wraps a raw SSE body as an *http.Response.
func newFakeSSEResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
