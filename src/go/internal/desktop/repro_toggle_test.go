package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// syncBuffer is a mutex-guarded output buffer for tests. Mutation handlers run
// off the server's select loop (server.go) and emit their terminal result and
// refresh asynchronously, so a plain strings.Builder shared with the polling
// test goroutine is a data race. Every method is safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

// invokeAndAwait runs one snapshot action and waits for its terminal result
// line, the way the app reads the tray's stdout stream.
func invokeAndAwait(t *testing.T, srv *Server, out *syncBuffer, rev int, actionID, reqID string) Result {
	t.Helper()
	out.Reset()
	raw, _ := json.Marshal(invokePayload{Version: 1, RequestID: reqID, Revision: rev, ActionID: actionID})
	if err := srv.handle(context.Background(), &Command{Kind: "invoke", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(out.String(), "\n") {
			if !strings.HasPrefix(line, "result\t") {
				continue
			}
			payload, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "result\t"))
			var res Result
			if json.Unmarshal(payload, &res) == nil && res.RequestID == reqID {
				// Drain the handler's trailing refresh before returning:
				// without this the goroutine keeps touching the store after
				// the test closes the database (sql: database is closed).
				srv.Wait()
				return res
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no terminal result for %s, output:\n%s", reqID, out.String())
	return Result{}
}

// seedTrayProxy saves a workbuddy account behind an enabled proxy so the
// snapshot emits a "workbuddy/main" row carrying a toggle action.
func seedTrayProxy(t *testing.T, ctx context.Context, env *trayEnv) {
	t.Helper()
	authPath := filepath.Join(t.TempDir(), "auth.json")
	doc, _ := json.Marshal(map[string]any{
		"version": 1, "kind": "subscription", "methodId": "workbuddy-international",
		"base": "https://www.codebuddy.ai", "domain": "www.codebuddy.ai",
		"accessToken": "tok-repro", "userId": "repro@example.com",
	})
	if err := os.WriteFile(authPath, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_AUTH_PATH", authPath)
	if _, err := env.Svc.Save(ctx, "workbuddy", "main", true); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := env.Deps.Store.SetProxyConfigContext(ctx, "workbuddy", "main",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

// toggleActionIDFor returns the toggle action id carried by one proxy row.
func toggleActionIDFor(snap *Snapshot, id string) string {
	for _, p := range snap.Proxies {
		if p.ID == id {
			return p.ToggleActionID
		}
	}
	return ""
}

// Full app path Start then Stop of a proxy through snapshot action IDs,
// exactly like the Proxies pane does (snapshot → toggleActionId → invoke).
//
// It deliberately refreshes the snapshot between the two invokes, which is
// what the UI does — so it is NOT the PX2 regression. The stale-snapshot case
// is TestToggleReusesFreshLivenessNotSnapshotTime.
func TestToggleStartStopRoundTrip(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()

	// Seed a workbuddy live login via the test escape hatch.
	authPath := filepath.Join(t.TempDir(), "auth.json")
	doc, _ := json.Marshal(map[string]any{
		"version": 1, "kind": "subscription", "methodId": "workbuddy-international",
		"base": "https://www.codebuddy.ai", "domain": "www.codebuddy.ai",
		"accessToken": "tok-repro", "userId": "repro@example.com",
	})
	if err := os.WriteFile(authPath, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_AUTH_PATH", authPath)

	if _, err := env.Svc.Save(ctx, "workbuddy", "main", true); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := env.Deps.Store.SetProxyConfigContext(ctx, "workbuddy", "main",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	var out syncBuffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var toggleID string
	for _, p := range snap.Proxies {
		if p.ID == "workbuddy/main" {
			toggleID = p.ToggleActionID
		}
	}
	if toggleID == "" {
		t.Fatalf("no proxy row: %+v", snap.Proxies)
	}

	invoke := func(rev int, actionID, reqID string) Result {
		return invokeAndAwait(t, srv, &out, rev, actionID, reqID)
	}

	rev := snap.Revision
	if res := invoke(rev, toggleID, "start-1"); res.Status != "success" {
		t.Fatalf("start: %+v", res)
	}
	st, _ := env.Deps.Proxy.Status("workbuddy", "main")
	t.Logf("after start: running=%v", st.Running)

	// Fresh snapshot like the UI would hold after refresh.
	snap2, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var toggleID2 string
	for _, p := range snap2.Proxies {
		if p.ID == "workbuddy/main" {
			toggleID2 = p.ToggleActionID
		}
	}
	if res := invoke(snap2.Revision, toggleID2, "stop-1"); res.Status != "success" {
		t.Fatalf("stop: %+v", res)
	}
	st, _ = env.Deps.Proxy.Status("workbuddy", "main")
	if st.Running {
		t.Fatal("proxy still running after stop via app invoke path")
	}
	cfg, _ := env.Deps.Store.GetProxyConfig(context.Background(), "workbuddy", "main")
	t.Logf("config after stop: %+v", cfg)
}

// Starting from the Proxies pane must rewrite bound client configs. Otherwise
// Claude Code keeps the previous proxy token and every POST /v1/messages 401s.
func TestStartReappliesBoundClaudeToken(t *testing.T) {
	env := newTrayEnv(t)
	wireActivation(t, env)
	ctx := context.Background()

	authPath := filepath.Join(t.TempDir(), "auth.json")
	doc, _ := json.Marshal(map[string]any{
		"version": 1, "kind": "subscription", "methodId": "workbuddy-international",
		"base": "https://www.codebuddy.ai", "domain": "www.codebuddy.ai",
		"accessToken": "tok-repro", "userId": "repro@example.com",
	})
	if err := os.WriteFile(authPath, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBUDDY_AUTH_PATH", authPath)
	if _, err := env.Svc.Save(ctx, "workbuddy", "main", true); err != nil {
		t.Fatal(err)
	}

	source := refs.AccountRef("workbuddy", "main")
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client: "claude",
		Spec: bindings.Spec{
			Client: "claude", Source: source, Model: bindings.OmittedModel(),
			TransportPolicy: "auto",
		},
		Provenance: bindings.Provenance{Kind: "direct"},
	}); err != nil {
		t.Fatal(err)
	}

	settings := filepath.Join(env.Home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	stale, _ := json.Marshal(map[string]any{
		"env": map[string]string{
			"ANTHROPIC_BASE_URL":   "http://127.0.0.1:1",
			"ANTHROPIC_AUTH_TOKEN": "stale-token-from-previous-start",
		},
	})
	if err := os.WriteFile(settings, stale, 0o600); err != nil {
		t.Fatal(err)
	}

	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	if _, err := env.Deps.Proxy.StartProxy(ctx, "workbuddy", "main"); err != nil {
		t.Fatal(err)
	}
	if err := srv.reapplyClientsBoundTo(ctx, source); err != nil {
		t.Fatal(err)
	}

	state, err := env.Deps.Store.ReadProxyState(ctx, "workbuddy", "main")
	if err != nil || state == nil || state.Token == "" {
		t.Fatalf("proxy state: %+v %v", state, err)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var doc2 struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(raw, &doc2) != nil {
		t.Fatalf("settings unreadable: %s", raw)
	}
	if doc2.Env["ANTHROPIC_AUTH_TOKEN"] != state.Token {
		t.Fatalf("claude token was not rewritten to the live proxy secret")
	}
	if !strings.Contains(doc2.Env["ANTHROPIC_BASE_URL"], "127.0.0.1") {
		t.Fatalf("base url = %q", doc2.Env["ANTHROPIC_BASE_URL"])
	}
}

// TestToggleReusesFreshLivenessNotSnapshotTime is the PX2 regression.
//
// The toggle action is a closure over the liveness the snapshot captured when
// it was built, and the UI holds that snapshot for as long as the pane is
// open. A child that since died — or was started from another surface — makes
// the captured value wrong, and branching on it ran the wrong verb and still
// reported success: the pane claimed to start a proxy that was already up,
// and to stop one that was never running.
//
// Invoking the SAME stale action id twice is the cheapest way to force the
// divergence. After the first invoke the child is up, but the closure still
// believes it is down. Revert the handler to the captured value and the
// second invoke starts an already-running proxy instead of stopping it.
func TestToggleReusesFreshLivenessNotSnapshotTime(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()
	seedTrayProxy(t, ctx, env)

	var out syncBuffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	toggleID := toggleActionIDFor(snap, "workbuddy/main")
	if toggleID == "" {
		t.Fatalf("no proxy row: %+v", snap.Proxies)
	}

	// First invoke: the child is down, so this starts it.
	if res := invokeAndAwait(t, srv, &out, snap.Revision, toggleID, "start-1"); res.Status != "success" {
		t.Fatalf("start: %+v", res)
	}

	// Second invoke on the SAME stale action id. The snapshot behind it still
	// says "not running"; the live child disagrees.
	if res := invokeAndAwait(t, srv, &out, snap.Revision, toggleID, "stop-1"); res.Status != "success" {
		t.Fatalf("stop: %+v", res)
	}
	st, _ := env.Deps.Proxy.Status("workbuddy", "main")
	if st.Running {
		t.Fatal("proxy still running: the toggle branched on the snapshot's captured liveness instead of re-checking")
	}
}
