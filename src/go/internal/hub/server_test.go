package hub

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/proxy"
)

// TestRewriteModelPreservesLargeIntegers pins the reason rewriteModel decodes
// with UseNumber. Unmarshalling into interface{} turns every JSON number into
// a float64, so re-marshalling rewrites integers that cannot survive the round
// trip. A proxy that only relabels a model has no business resizing the
// caller's numbers.
func TestRewriteModelPreservesLargeIntegers(t *testing.T) {
	route := &Route{Models: []ModelRoute{{Model: "alias", SourceIndex: 0, UpstreamModel: "real"}}}
	body := []byte(`{"model":"alias","seed":9007199254740993,"max_tokens":4096}`)

	got := rewriteModel(body, route, "alias")
	if !strings.Contains(got, `"model":"real"`) {
		t.Fatalf("model was not rewritten: %s", got)
	}
	if !strings.Contains(got, "9007199254740993") {
		t.Fatalf("large integer did not survive the rewrite: %s", got)
	}
	if !strings.Contains(got, "4096") {
		t.Fatalf("ordinary integer lost: %s", got)
	}
}

func TestRewriteModelLeavesUnmappedBodyAlone(t *testing.T) {
	route := &Route{Models: []ModelRoute{{Model: "alias", SourceIndex: 0, UpstreamModel: "real"}}}
	body := []byte(`{"model":"other","seed":9007199254740993}`)
	if got := rewriteModel(body, route, "other"); got != string(body) {
		t.Fatalf("body for an unmapped model was rewritten: %s", got)
	}
}

// TestStartPublishEndpointRaceFree exercises the lifecycle fields from two
// goroutines. Start used to publish s.endpoint and s.httpSrv without holding
// mu while Endpoint() read s.endpoint without holding it either.
func TestStartPublishEndpointRaceFree(t *testing.T) {
	srv := &Server{Config: Config{Name: "race", Enabled: true}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := srv.Start(); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = srv.Endpoint()
			_ = srv.Err()
		}
	}()
	wg.Wait()
	_ = srv.Stop()
}

// TestReadCappedBodyRejectsOversizeInsteadOfTruncating pins the reason the cap
// is enforced with an explicit length check. io.LimitReader alone truncates in
// silence, which turns an oversized upload into a syntactically broken JSON
// document forwarded upstream — the vendor then answers 400 for a body we
// mangled.
func TestReadCappedBodyRejectsOversizeInsteadOfTruncating(t *testing.T) {
	oversize := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", 9)))
	if _, err := proxy.ReadCappedBody(oversize, 8); !errors.Is(err, proxy.ErrBodyTooLarge) {
		t.Fatalf("oversize body: err = %v, want ErrBodyTooLarge", err)
	}

	exact := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Repeat("x", 8)))
	got, err := proxy.ReadCappedBody(exact, 8)
	if err != nil {
		t.Fatalf("body exactly at the cap was rejected: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("body at the cap: read %d bytes, want 8", len(got))
	}
}

// blockingShutdown reports when Shutdown is entered and waits to be released,
// standing in for a server with a request still in flight.
type blockingShutdown struct {
	entered chan struct{}
	release chan struct{}
	calls   *int
}

func (b blockingShutdown) Shutdown() error {
	*b.calls++
	close(b.entered)
	<-b.release
	return nil
}

// TestStopDoesNotHoldTheLockAcrossShutdown pins a deadlock: Stop used to hold
// mu while calling Shutdown, and Shutdown waits for in-flight requests — which
// call config() under that same mu. Every request then stalled until the 5s
// shutdown context expired, and Endpoint() stalled behind them.
func TestStopDoesNotHoldTheLockAcrossShutdown(t *testing.T) {
	calls := 0
	sd := blockingShutdown{entered: make(chan struct{}), release: make(chan struct{}), calls: &calls}
	srv := &Server{Config: Config{Name: "stop", Enabled: true}}
	srv.httpSrv = sd

	stopped := make(chan error, 1)
	go func() { stopped <- srv.Stop() }()
	<-sd.entered

	readable := make(chan struct{})
	go func() { _ = srv.Endpoint(); _ = srv.Err(); close(readable) }()
	select {
	case <-readable:
	case <-time.After(2 * time.Second):
		t.Fatal("Endpoint()/Err() blocked while Stop() was shutting down: mu is held across Shutdown")
	}

	close(sd.release)
	if err := <-stopped; err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Shutdown called %d times, want 1", calls)
	}
	// Idempotent: a second Stop must not shut down again.
	if err := srv.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Shutdown called %d times after a second Stop, want 1", calls)
	}
}
