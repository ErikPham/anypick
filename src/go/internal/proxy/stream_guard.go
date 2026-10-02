package proxy

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"strings"
)

// Degenerate-stream tripwire (log-only) for verbatim OpenAI SSE passthrough.
//
// Context: WorkBuddy/CodeBuddy streamed a 200 SSE holding a reasoning ramble
// — "Let me write." / "Let me go." / "OK." / "Writing." — megabytes long,
// while every gate stayed green. Aborting the TCP connection made clients
// retry (STREAM_CLOSED). Injecting stop+[DONE] made WorkBuddy kill the turn.
// The bytes originate upstream, so this tap only fingerprints them.
//
// Scope is the inbound OpenAI chat surface only (shouldInspectStream).
const (
	// guardWindowLines is how many consecutive short model-text lines fill
	// the window. A tight phrase cycle (observed: 3-cycle, thousands of
	// repeats) trips within seconds of the collapse starting.
	guardWindowLines = 32
	// guardMaxDistinct bounds distinct values in a full window. Varied prose
	// or code never gets near this.
	guardMaxDistinct = 4
	// guardMaxLineRepeats bounds cumulative repeats of one short line in a
	// whole response. A later collapse spread 7 filler phrases across 1300
	// lines (top phrase 115x, 538 distinct overall) sailed through the
	// window rule; no legitimate answer repeats one identical short line
	// fifty times in a short span.
	guardMaxLineRepeats = 50
	// guardMaxRepeatSpan bounds the density of those repeats: the 50th
	// occurrence must land within this many text lines of the first
	// (measured: 393–541 in the observed collapse). Sparse repetition —
	// e.g. fences across a long document — stays silent.
	guardMaxRepeatSpan = 600
	// guardMaxLineLength keeps long code/data lines out of both rules: the
	// attractor is short filler, and ignoring long lines is what keeps a
	// legit long-form answer from false-firing.
	guardMaxLineLength = 160
	// guardTrackedLines caps the cumulative map so a pathological stream of
	// unique lines cannot grow memory without bound.
	guardTrackedLines = 2048
	// guardLineCap bounds one undecoded line so a newline-free flood cannot
	// grow memory without bound; past it the tap goes blind (passthrough).
	guardLineCap = 4096
)

func shouldInspectStream(r *http.Request) bool {
	return r.Method == http.MethodPost &&
		(r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/completions")
}

// degenerateTap observes one response passively. Zero shared state: one tap
// per request, diagnostics via proxyLogf only.
type degenerateTap struct {
	w http.ResponseWriter

	sniffed bool
	isSSE   bool
	fired   bool
	blind   bool

	raw  []byte // undecoded SSE tail across Write calls
	text []byte // undecoded model-text tail across chunks

	window     []string
	counts     map[string]lineStat
	textLines  int
	sseBytes   int64
	statusCode int
}

// lineStat tracks cumulative repeats of one short line with its first
// position, so sparse repetition across a long answer never trips.
type lineStat struct {
	n     int
	first int
}

func newDegenerateTap(w http.ResponseWriter) *degenerateTap {
	return &degenerateTap{w: w, statusCode: http.StatusOK}
}

func (t *degenerateTap) Header() http.Header { return t.w.Header() }

func (t *degenerateTap) WriteHeader(status int) {
	t.statusCode = status
	t.sniff()
	t.w.WriteHeader(status)
}

// Flush must exist or ReverseProxy stops streaming through the tap: the
// embedded ResponseWriter interface does not promote it (see StatusRecorder).
func (t *degenerateTap) Flush() {
	if f, ok := t.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *degenerateTap) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if !t.fired && !t.blind {
		if !t.sniffed {
			t.sniff()
		}
		if t.isSSE {
			t.sseBytes += int64(len(p))
			t.feed(p)
		}
	}
	return n, err
}

func (t *degenerateTap) sniff() {
	t.sniffed = true
	// WriteHeader may not have run yet on the first Write; Header() still
	// carries what the upstream (via ReverseProxy) set.
	t.isSSE = strings.Contains(t.w.Header().Get("Content-Type"), "text/event-stream")
}

// feed consumes raw SSE bytes, extracts model text, and fires once when a
// full window of short lines collapses to a handful of distinct values.
func (t *degenerateTap) feed(p []byte) {
	t.raw = append(t.raw, p...)
	// Extract every complete line before capping: a single bursty write full
	// of short lines must not blind the tap. Only the unterminated tail is a
	// memory risk, and it is the only part the cap applies to.
	for {
		i := bytes.IndexByte(t.raw, '\n')
		if i < 0 {
			break
		}
		line := t.raw[:i]
		t.raw = t.raw[i+1:]
		t.sseLine(line)
		if t.fired || t.blind {
			return
		}
	}
	if len(t.raw) > guardLineCap*4 {
		// No newlines for megabytes: not SSE text anymore, stay passthrough.
		t.blind = true
		t.raw = nil
		return
	}
	if len(t.raw) > guardLineCap {
		// One line overran the cap: drop it and resync on the next newline.
		// The tail may misparse once; JSON rejects it.
		t.raw = nil
	}
}

func (t *degenerateTap) sseLine(line []byte) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" || trimmed[0] != 'd' || !strings.HasPrefix(trimmed, "data:") {
		return // comments, event: fields, blanks
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	// Cheap prefilter before JSON: only text-carrying chunks matter.
	if !strings.Contains(payload, "content") {
		return
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return
	}
	for _, c := range chunk.Choices {
		var part string
		_ = json.Unmarshal(c.Delta.Content, &part)
		t.addText(part + c.Delta.ReasoningContent)
		if t.fired || t.blind {
			return
		}
	}
}

// addText reassembles chunked text into lines and slides the window. Long
// lines are ignored without resetting: substantive output between filler
// still breaks a *consecutive* collapse, and ignoring (rather than
// resetting on) long lines keeps code-heavy answers from false-firing.
func (t *degenerateTap) addText(s string) {
	t.text = append(t.text, s...)
	// Same rule as feed: complete lines are consumed first, and only the
	// unterminated tail can trip the memory caps.
	for {
		i := bytes.IndexByte(t.text, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(t.text[:i]))
		t.text = t.text[i+1:]
		if line == "" || len(line) > guardMaxLineLength {
			continue
		}
		t.textLines++
		if len(t.window) == guardWindowLines {
			copy(t.window, t.window[1:])
			t.window[guardWindowLines-1] = line
		} else {
			t.window = append(t.window, line)
		}
		if len(t.window) == guardWindowLines && distinctLines(t.window) <= guardMaxDistinct {
			t.fire(topCount(t.window))
			return
		}
		if n := t.count(line); n >= guardMaxLineRepeats &&
			t.textLines-t.counts[line].first <= guardMaxRepeatSpan {
			t.fire(n)
			return
		}
	}
	if len(t.text) > guardLineCap*4 {
		t.blind = true
		t.text = nil
		return
	}
	if len(t.text) > guardLineCap {
		// Overlong line with no terminator yet: drop the head so a
		// newline-free flood cannot grow memory; the tail resyncs.
		t.text = nil
	}
}

// count records one short line cumulatively, bounded so a stream of unique
// lines cannot grow the map without bound; past the cap new lines are only
// window-tracked, never map-tracked.
func (t *degenerateTap) count(line string) int {
	if t.counts == nil {
		t.counts = make(map[string]lineStat)
	}
	if st, ok := t.counts[line]; ok {
		st.n++
		t.counts[line] = st
		return st.n
	}
	if len(t.counts) >= guardTrackedLines {
		return 0
	}
	t.counts[line] = lineStat{n: 1, first: t.textLines}
	return 1
}

func topCount(window []string) int {
	best := 0
	seen := make(map[string]int, len(window))
	for _, l := range window {
		seen[l]++
		if seen[l] > best {
			best = seen[l]
		}
	}
	return best
}

func distinctLines(window []string) int {
	seen := make(map[string]struct{}, len(window))
	for _, l := range window {
		seen[l] = struct{}{}
	}
	return len(seen)
}

func (t *degenerateTap) fire(repeat int) {
	t.fired = true
	// Fingerprint only: model text can echo prompts and file contents, and
	// proxy.log gets attached to bug reports. The hash correlates repeat
	// occurrences of one attractor without persisting any of its text.
	h := fnv.New32a()
	for _, l := range t.window {
		_, _ = h.Write([]byte(l))
		_, _ = h.Write([]byte{0})
	}
	proxyLogf("degenerate stream suspected: distinct=%d window=%d text_lines=%d sse_bytes=%d line_hash=%x repeat=%d",
		distinctLines(t.window), len(t.window), t.textLines, t.sseBytes, h.Sum32(), repeat)
}
