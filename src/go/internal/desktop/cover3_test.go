package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/paths"
	"github.com/outof0/anypick/go/internal/store"
)

type namedOnly struct{}

func (namedOnly) Name() string { return "BareName" }

func TestShortNameOfBothBranches(t *testing.T) {
	env := newTrayEnv(t)
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})

	pv, err := env.Deps.Providers.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got := shortNameOf(srv, pv); got != "Codex" {
		t.Fatalf("provider branch = %q want Codex", got)
	}
	if got := shortNameOf(srv, namedOnly{}); got != "BareName" {
		t.Fatalf("fallback branch = %q want BareName", got)
	}
}

// fakeHealthyProxy persists runtime state whose endpoint answers /health with
// the recorded instance id — enough for Status to report Running.
func fakeHealthyProxy(t *testing.T, env *trayEnv, providerID, name, instanceID string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"instanceId":"` + instanceID + `"}`))
	}))
	t.Cleanup(ts.Close)

	ctx := context.Background()
	err := env.Deps.Store.WriteProxyStateContext(ctx, providerID, name, store.ProxyRuntimeState{
		AccountName: name, Endpoint: ts.URL,
		PID: 0, InstanceID: instanceID, StartedAt: "now", Token: "tok-" + name,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestHandleLogsRunningTailAndEmpty(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)

	fakeHealthyProxy(t, env, "codex", "logged", "inst-log")
	// Running but never written to → exercises the empty branch.
	fakeHealthyProxy(t, env, "codex", "nolog", "inst-nolog")
	logPath := paths.ProxyLogPath(env.Deps.Root, "codex", "logged")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("l1\nl2\nl3\nl4\nl5\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	request := func(name string, lines int) ProxyLogsResult {
		t.Helper()
		out.Reset()
		raw, _ := json.Marshal(logsPayload{Version: 1, RequestID: "r-" + name,
			ProviderID: "codex", Name: name, Lines: lines})
		if err := srv.handleLogs(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		decoded := decodeFramePayload(t, firstFrameLine(out.String()))
		var res ProxyLogsResult
		if err := json.Unmarshal(decoded, &res); err != nil {
			t.Fatalf("decode %s: %v", decoded, err)
		}
		return res
	}

	res := request("logged", 2)
	if res.State != "ready" || res.Text != "l4\nl5" {
		t.Fatalf("tail: state=%q text=%q", res.State, res.Text)
	}
	if res.ProxyID != "codex/logged" {
		t.Fatalf("proxy id %q", res.ProxyID)
	}

	// Lines <= 0 keeps the whole file.
	res = request("logged", 0)
	if res.State != "ready" || !strings.Contains(res.Text, "l1") {
		t.Fatalf("full read: state=%q text=%q", res.State, res.Text)
	}

	// Running but no log file yet → empty.
	res = request("nolog", 5)
	if res.State != "empty" || res.Text != "" {
		t.Fatalf("no log: state=%q text=%q", res.State, res.Text)
	}

	// Corrupt payload surfaces as a decode error.
	if err := srv.handleLogs(context.Background(), json.RawMessage(`{oops`)); err == nil {
		t.Fatal("corrupt logs payload must error")
	}
}

func TestHandleMutateRemainingOperations(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	ctx := context.Background()

	run := func(m Mutate) Result {
		t.Helper()
		out.Reset()
		raw, _ := json.Marshal(m)
		if err := srv.handleMutate(ctx, raw); err != nil {
			t.Fatal(err)
		}
		decoded := decodeFramePayload(t, firstFrameLine(out.String()))
		var res Result
		if err := json.Unmarshal(decoded, &res); err != nil {
			t.Fatalf("decode %s: %v", decoded, err)
		}
		return res
	}

	// account-refresh on a missing account errors cleanly.
	res := run(Mutate{Version: 1, RequestID: "ar0", Operation: "account-refresh",
		ProviderID: "codex", Name: "ghost"})
	if res.Status != "error" || !strings.Contains(res.Message, "account not found") {
		t.Fatalf("refresh missing: %+v", res)
	}

	// Seeded live auth makes refresh succeed.
	env.writeCodexLive("rf@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "rf-acc", true); err != nil {
		t.Fatal(err)
	}
	res = run(Mutate{Version: 1, RequestID: "ar1", Operation: "account-refresh",
		ProviderID: "codex", Name: "rf-acc"})
	if res.Status != "success" {
		t.Fatalf("refresh ok: %+v", res)
	}

	// Stop/restart-all succeed trivially with nothing enabled.
	for _, op := range []string{"proxy-stop-all", "proxy-restart-all"} {
		res = run(Mutate{Version: 1, RequestID: "op-" + op, Operation: op})
		if res.Status != "success" {
			t.Fatalf("%s: %+v", op, res)
		}
	}

	// Unknown operations are reported, never fatal.
	res = run(Mutate{Version: 1, RequestID: "unk", Operation: "warp-drive"})
	if res.Status != "error" || !strings.Contains(res.Message, "unknown mutation operation") {
		t.Fatalf("unknown op: %+v", res)
	}
}

func TestHandleMutateErrorBranches(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	ctx := context.Background()

	run := func(m Mutate) Result {
		t.Helper()
		out.Reset()
		raw, _ := json.Marshal(m)
		if err := srv.handleMutate(ctx, raw); err != nil {
			t.Fatal(err)
		}
		return decodeMutateResult(t, out.String())
	}

	// Corrupt payload surfaces as a decode error.
	if err := srv.handleMutate(ctx, json.RawMessage(`{oops`)); err == nil {
		t.Fatal("corrupt mutate payload must error")
	}

	// save-current without any live login fails honestly.
	res := run(Mutate{Version: 1, RequestID: "e1", Operation: "account-save-current",
		ProviderID: "codex"})
	if res.Status != "error" {
		t.Fatalf("save-current expected failure: %+v", res)
	}

	// account-save follows the wire contract and never invents a name; the
	// save-current operation may derive one from the detected identity.
	env.writeCodexLive("br@example.com")
	res = run(Mutate{Version: 1, RequestID: "e2", Operation: "account-save", ProviderID: "codex"})
	if res.Status != "error" {
		t.Fatalf("unnamed account-save must fail: %+v", res)
	}
	res = run(Mutate{Version: 1, RequestID: "e3", Operation: "account-save-current", ProviderID: "codex"})
	if res.Status != "success" {
		t.Fatalf("save-current: %+v", res)
	}

	// Refresh against an unknown provider reports the registry error.
	res = run(Mutate{Version: 1, RequestID: "e4", Operation: "account-refresh",
		ProviderID: "nope", Name: "x"})
	if res.Status != "error" || strings.Contains(res.Message, "account not found") {
		t.Fatalf("unknown provider refresh: %+v", res)
	}

	// Validation guards.
	res = run(Mutate{Version: 1, RequestID: "e5", Operation: "account-remove", Name: "x"})
	if res.Status != "error" || !strings.Contains(res.Message, "providerId") {
		t.Fatalf("remove guard: %+v", res)
	}
	res = run(Mutate{Version: 1, RequestID: "e6", Operation: "gateway-remove"})
	if res.Status != "error" || !strings.Contains(res.Message, "name") {
		t.Fatalf("gateway-remove guard: %+v", res)
	}
	res = run(Mutate{Version: 1, RequestID: "e7", Operation: "gateway-edit", Name: "ghost-gw"})
	if res.Status != "error" || !strings.Contains(res.Message, "gateway not found") {
		t.Fatalf("gateway-edit guard: %+v", res)
	}
	res = run(Mutate{Version: 1, RequestID: "e8", Operation: "client-reset", ProviderID: "nope"})
	if res.Status != "error" {
		t.Fatalf("client-reset guard: %+v", res)
	}

	// restart-all with an enabled config walks the real stop/start loop.
	if _, err := env.Svc.Save(ctx, "codex", "rf-acc", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Store.SetProxyConfigContext(ctx, "codex", "rf-acc",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	res = run(Mutate{Version: 1, RequestID: "e9", Operation: "proxy-restart-all"})
	if res.Status != "success" {
		t.Fatalf("restart-all: %+v", res)
	}
}

func decodeMutateResult(t *testing.T, out string) Result {
	t.Helper()
	decoded := decodeFramePayload(t, firstFrameLine(out))
	var res Result
	if err := json.Unmarshal(decoded, &res); err != nil {
		t.Fatalf("decode %s: %v", decoded, err)
	}
	return res
}

func TestRunProcessesMalformedAndShutdownLines(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()
	env.writeCodexLive("run@example.com")

	in := strings.NewReader(strings.Join([]string{
		"this is not a frame",
		"refresh\t" + base64.StdEncoding.EncodeToString([]byte(`{}`)),
		"",
	}, "\n") + "\n")

	var out bytes.Buffer
	srv := New(env.Deps, in, &out)
	// EOF on stdin ends the loop cleanly.
	if err := srv.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "snapshot") {
		t.Fatalf("expected snapshot frames, got %q", out.String())
	}
}

func TestRunStopsOnQuitCommand(t *testing.T) {
	env := newTrayEnv(t)
	in := strings.NewReader("quit\t\n")
	var out bytes.Buffer
	srv := New(env.Deps, in, &out)
	if err := srv.Run(context.Background()); err != nil {
		t.Fatalf("Run with quit: %v", err)
	}
}
