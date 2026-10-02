package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func captureProxyLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := proxyLogSink
	proxyLogSink = &buf
	t.Cleanup(func() { proxyLogSink = old })
	return &buf
}

func sseChunk(content string) string {
	return "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":" + quoteForTest(content) + "}}]}\n\n"
}

func quoteForTest(s string) string {
	out := strings.ReplaceAll(s, "\\", "\\\\")
	out = strings.ReplaceAll(out, "\"", "\\\"")
	out = strings.ReplaceAll(out, "\n", "\\n")
	return "\"" + out + "\""
}

func writeSplit(t *testing.T, w http.ResponseWriter, payload string) {
	t.Helper()
	// Uneven splits prove reassembly across Write calls.
	for len(payload) > 0 {
		n := 7
		if len(payload) < n {
			n = len(payload)
		}
		if _, err := w.Write([]byte(payload[:n])); err != nil {
			t.Fatal(err)
		}
		payload = payload[n:]
	}
}

// The tap must be invisible: bytes, status, and flush pass through exactly.
func TestTapPassesBytesUntouched(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "text/event-stream")
	tap.WriteHeader(http.StatusOK)
	body := sseChunk("Hello,\nWorld\n") + "data: [DONE]\n\n"
	writeSplit(t, tap, body)
	tap.Flush()
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if rec.Body.String() != body {
		t.Fatalf("bytes altered:\n%q\nwant:\n%q", rec.Body.String(), body)
	}
	if !rec.Flushed {
		t.Fatal("Flush did not delegate to the underlying writer")
	}
	if strings.Contains(logs.String(), "degenerate") {
		t.Fatalf("healthy stream logged: %q", logs.String())
	}
}

func TestTapIgnoresNonSSE(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "application/json")
	tap.WriteHeader(http.StatusOK)
	loop := strings.Repeat("Let me write.\nLet me go.\nOK.\n", 20)
	if _, err := tap.Write([]byte(loop)); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != loop {
		t.Fatal("non-SSE bytes altered")
	}
	if strings.Contains(logs.String(), "degenerate") {
		t.Fatalf("non-SSE body inspected: %q", logs.String())
	}
}

// Replay of the observed collapse: a short-phrase cycle, thousands of times
// in production — dozens here are enough to fill the window.
func TestTapDetectsCyclicLoop(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "text/event-stream")
	tap.WriteHeader(http.StatusOK)
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		sb.WriteString(sseChunk("Let me write.\n"))
		sb.WriteString(sseChunk("Let me go.\n"))
		if i%2 == 0 {
			sb.WriteString(sseChunk("OK.\n"))
		} else {
			sb.WriteString(sseChunk("Writing.\n"))
		}
	}
	sb.WriteString("data: [DONE]\n\n")
	body := sb.String()
	writeSplit(t, tap, body)
	if rec.Body.String() != body {
		t.Fatal("log-only tap altered bytes")
	}
	got := logs.String()
	if strings.Count(got, "degenerate stream suspected") != 1 {
		t.Fatalf("want exactly one tripwire line, got %q", got)
	}
	if !strings.Contains(got, "distinct=4") {
		t.Fatalf("fingerprint should name the collapse shape: %q", got)
	}
}

func TestTapDetectsReasoningContent(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "text/event-stream")
	tap.WriteHeader(http.StatusOK)
	// Some vendors stream thinking in reasoning_content, not content.
	var sb strings.Builder
	for i := 0; i < guardWindowLines; i++ {
		sb.WriteString("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"still thinking\\n\"}}]}\n\n")
	}
	writeSplit(t, tap, sb.String())
	if strings.Count(logs.String(), "degenerate stream suspected") != 1 {
		t.Fatalf("reasoning_content collapse missed: %q", logs.String())
	}
}

func TestTapIgnoresHealthyStream(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "text/event-stream")
	tap.WriteHeader(http.StatusOK)
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString(sseChunk("Finding number " + strings.Repeat("x", i) + " looks different.\n"))
	}
	// Long code lines never enter the window.
	sb.WriteString(sseChunk(strings.Repeat("const v = 1234567890; // filler ", 20) + "\n"))
	writeSplit(t, tap, sb.String())
	if strings.Contains(logs.String(), "degenerate") {
		t.Fatalf("healthy varied stream tripped: %q", logs.String())
	}
}

func TestTapFiresOnlyOnFullWindow(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "text/event-stream")
	tap.WriteHeader(http.StatusOK)
	cycle := []string{"a.\n", "b.\n", "c.\n", "d.\n"}
	for i := 0; i < guardWindowLines-1; i++ {
		if _, err := tap.Write([]byte(sseChunk(cycle[i%4]))); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(logs.String(), "degenerate") {
		t.Fatalf("fired before the window filled: %q", logs.String())
	}
	if _, err := tap.Write([]byte(sseChunk(cycle[(guardWindowLines-1)%4]))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "degenerate stream suspected") {
		t.Fatalf("full window of 4-cycle did not trip: %q", logs.String())
	}
}

// End to end: Serve's verbatim OpenAI path keeps bytes identical while the
// tripwire records the collapse beside the access line.
func TestServeLogsDegeneratePassthrough(t *testing.T) {
	logs := captureProxyLogs(t)
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		sb.WriteString(sseChunk("Let me write.\n"))
		sb.WriteString(sseChunk("Let me go.\n"))
		sb.WriteString(sseChunk("OK.\n"))
	}
	loopSSE := sb.String()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(loopSSE))
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL + "/v2")
	up := &Upstream{BaseURL: base, Auth: "tok", OpenAIPath: "/chat/completions"}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	Serve(rec, req, up)
	if rec.Body.String() != loopSSE {
		t.Fatalf("passthrough bytes changed:\n%q", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "degenerate stream suspected") {
		t.Fatalf("Serve did not trip on the looped stream; logs:\n%s", logs.String())
	}
}

func TestServeSilentOnHealthyPassthrough(t *testing.T) {
	logs := captureProxyLogs(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseChunk("Here is a varied answer.\nWith two lines.\n") + "data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL + "/v2")
	up := &Upstream{BaseURL: base, Auth: "tok", OpenAIPath: "/chat/completions"}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	Serve(httptest.NewRecorder(), req, up)
	if strings.Contains(logs.String(), "degenerate") {
		t.Fatalf("healthy Serve tripped: %q", logs.String())
	}
}

// Replay of the second observed collapse: ~7 short filler phrases spread
// across 1300 lines of varied filler (538 distinct overall). The window rule
// never closes on this shape; the cumulative rule must.
func TestTapDetectsDiffuseLoop(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "text/event-stream")
	tap.WriteHeader(http.StatusOK)
	fill := []string{"Go.", "Let me write.", "OK.", "Final.", "Writing.", "Output.", "Now."}
	var sb strings.Builder
	// 200 rounds of two phrases + one unique filler: every phrase reaches
	// 50 repeats inside a ~260-line span, mirroring the measured collapse
	// (50th repeat within ~420 lines of the first).
	for i := 0; i < 200; i++ {
		sb.WriteString(sseChunk(fill[i%len(fill)] + "\n"))
		sb.WriteString(sseChunk(fill[(i+1)%len(fill)] + "\n"))
		sb.WriteString(sseChunk("filler detail line about coverage mapping variant " + strings.Repeat("z", i%23) + "\n"))
	}
	body := sb.String()
	writeSplit(t, tap, body)
	if rec.Body.String() != body {
		t.Fatal("log-only tap altered bytes")
	}
	got := logs.String()
	if strings.Count(got, "degenerate stream suspected") != 1 {
		t.Fatalf("want exactly one tripwire line, got %q", got)
	}
	if !strings.Contains(got, "repeat=50") {
		t.Fatalf("fingerprint should carry the repeat count: %q", got)
	}
}

func TestTapCumulativeBoundary(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "text/event-stream")
	tap.WriteHeader(http.StatusOK)
	for i := 0; i < guardMaxLineRepeats-1; i++ {
		if _, err := tap.Write([]byte(sseChunk("Same.\n"))); err != nil {
			t.Fatal(err)
		}
		if _, err := tap.Write([]byte(sseChunk("unique filler " + strings.Repeat("q", i) + "\n"))); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(logs.String(), "degenerate") {
		t.Fatalf("fired before the repeat budget: %q", logs.String())
	}
	if _, err := tap.Write([]byte(sseChunk("Same.\n"))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "degenerate stream suspected") {
		t.Fatalf("50th repeat did not trip: %q", logs.String())
	}
}

// Repeated fences and separators across a long answer must stay silent:
// 25 blocks spread over ~800 lines trip neither the window rule (varied
// filler) nor the density-bounded cumulative rule (span > 600).
func TestTapIgnoresRepeatedFences(t *testing.T) {
	logs := captureProxyLogs(t)
	rec := httptest.NewRecorder()
	tap := newDegenerateTap(rec)
	tap.Header().Set("Content-Type", "text/event-stream")
	tap.WriteHeader(http.StatusOK)
	var sb strings.Builder
	for i := 0; i < 25; i++ {
		sb.WriteString(sseChunk("```\n"))
		sb.WriteString(sseChunk("block content variant " + strings.Repeat("w", i) + "\n"))
		sb.WriteString(sseChunk("```\n"))
		for j := 0; j < 30; j++ {
			sb.WriteString(sseChunk("prose between blocks, all different " + strings.Repeat("v", j) + "\n"))
		}
	}
	writeSplit(t, tap, sb.String())
	if strings.Contains(logs.String(), "degenerate") {
		t.Fatalf("code fences tripped: %q", logs.String())
	}
}
