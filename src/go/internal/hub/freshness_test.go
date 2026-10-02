package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/ownership"
)

// ChildSynced matches only the stored revision: anything else (older child,
// pre-revision child, garbage endpoint) restarts.
func TestChildSyncedMatrix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"instanceId":"i","revision":7}`))
	}))
	defer srv.Close()
	if !ChildSynced(srv.URL, 7) {
		t.Error("same revision must adopt")
	}
	if ChildSynced(srv.URL, 8) {
		t.Error("older child must restart")
	}
	if ChildSynced(srv.URL, 0) {
		t.Error("revision 0 must never match")
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	if ChildSynced(broken.URL, 7) {
		t.Error("unhealthy endpoint must restart")
	}
	if ChildSynced("http://127.0.0.1:1", 7) {
		t.Error("unreachable endpoint must restart")
	}
	if rev, ok := ChildRevision(srv.URL); !ok || rev != 7 {
		t.Errorf("ChildRevision = %d, %v", rev, ok)
	}
}

// A running child the pidfile does not own is never signalled: the refusal
// is an error, not a silent pass.
func TestRestartStaleChildRefusesStrangers(t *testing.T) {
	root := t.TempDir()
	if _, err := RestartStaleChild(root, "h", "http://127.0.0.1:1", 9, time.Second); err == nil {
		t.Fatal("want refusal when no trusted record exists")
	}
}

// A stale but owned child is stopped so the caller spawns fresh: a real hub
// Server answers /health while a spawned sleeper stands in for the child
// process (same pid the record owns, same instance the endpoint echoes).
func TestRestartStaleChildStopsOwnedStale(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	root := t.TempDir()
	pidFile := filepath.Join(root, "runtime", "hub", "h", "service.pid")
	rec, err := ownership.Spawn(sleep, []string{"60"}, pidFile, filepath.Join(root, "sleep.log"))
	if err != nil {
		t.Fatal(err)
	}
	// Serve the stale revision through a mux shaped like the child's: the
	// /health revision is what RestartStaleChild decides on.
	muxUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"instanceId":"` + rec.InstanceID + `","hub":"h","revision":3}`))
	}))
	defer muxUpstream.Close()

	restarted, err := RestartStaleChild(root, "h", muxUpstream.URL, 9, 5*time.Second)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !restarted {
		t.Fatal("stale owned child must restart")
	}
	if _, err := os.FindProcess(rec.PID); err == nil {
		if procErr := func() error {
			p, _ := os.FindProcess(rec.PID)
			return p.Signal(os.Interrupt)
		}(); procErr == nil {
			t.Fatalf("child pid %d still alive", rec.PID)
		}
	}
	// Fresh child with the stored revision is adopted untouched.
	if restarted, err := RestartStaleChild(root, "h", muxUpstream.URL, 3, time.Second); err != nil || restarted {
		t.Fatalf("fresh child: restarted=%v err=%v", restarted, err)
	}
}

// /health carries the config revision the child started with: adopters
// compare it against the stored revision instead of trusting liveness.
func TestHealthCarriesConfigRevision(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	srv := &Server{
		Config: Config{Name: "h", Enabled: true, Revision: 11},
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	resp, err := http.Get(srv.Endpoint() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload struct {
		Revision int `json:"revision"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Revision != 11 {
		t.Fatalf("health revision = %d, want 11", payload.Revision)
	}
}
