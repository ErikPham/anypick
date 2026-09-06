package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/plugins"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/proxy"
)

type lockedOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *lockedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *lockedOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func runCorrectnessMutation(t *testing.T, srv *Server, out *bytes.Buffer,
	payload map[string]interface{}) Result {
	t.Helper()
	out.Reset()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.handleMutate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	return decodeMutateResult(t, out.String())
}

func mutationPayload(requestID, operation, name string) map[string]interface{} {
	payload := map[string]interface{}{
		"version": 1, "requestId": requestID, "operation": operation,
	}
	if name != "" {
		payload["name"] = name
	}
	return payload
}

func TestDesktopAccountMutationsDoNotSilentlySucceed(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)

	detect := mutationPayload("detect-missing", "account-detect", "detect")
	detect["providerId"] = "codex"
	if res := runCorrectnessMutation(t, srv, &out, detect); res.Status != "error" {
		t.Fatalf("detect without a live login = %+v", res)
	}

	env.writeCodexLive("desktop@example.com")
	detect["requestId"] = "detect-present"
	res := runCorrectnessMutation(t, srv, &out, detect)
	if res.Status != "success" || !strings.Contains(res.Message, "desktop@example.com") {
		t.Fatalf("detect live login = %+v", res)
	}

	save := mutationPayload("save-first", "account-save", "work")
	save["providerId"] = "codex"
	save["label"] = "Desktop Work"
	res = runCorrectnessMutation(t, srv, &out, save)
	if res.Status != "success" {
		t.Fatalf("first save = %+v", res)
	}
	account, err := env.Deps.Store.Get("codex", "work")
	if err != nil || account == nil || account.Meta.Label != "Desktop Work" {
		t.Fatalf("saved account = %+v, %v", account, err)
	}

	env.writeCodexLive("replacement@example.com")
	save["requestId"] = "save-no-overwrite"
	res = runCorrectnessMutation(t, srv, &out, save)
	if res.Status != "error" {
		t.Fatalf("save over existing account without overwrite = %+v", res)
	}
	account, err = env.Deps.Store.Get("codex", "work")
	if err != nil || account == nil || account.Meta.Identity != "desktop@example.com" {
		t.Fatalf("failed overwrite changed the snapshot: %+v, %v", account, err)
	}

	save["requestId"] = "save-overwrite"
	save["overwrite"] = true
	res = runCorrectnessMutation(t, srv, &out, save)
	if res.Status != "success" {
		t.Fatalf("confirmed overwrite = %+v", res)
	}
	account, err = env.Deps.Store.Get("codex", "work")
	if err != nil || account == nil || account.Meta.Identity != "replacement@example.com" {
		t.Fatalf("confirmed overwrite did not update snapshot: %+v, %v", account, err)
	}

	edit := mutationPayload("edit", "account-edit", "work")
	edit["providerId"] = "codex"
	edit["label"] = "Renamed"
	res = runCorrectnessMutation(t, srv, &out, edit)
	if res.Status != "success" {
		t.Fatalf("account edit = %+v", res)
	}
	account, err = env.Deps.Store.Get("codex", "work")
	if err != nil || account == nil || account.Meta.Label != "Renamed" {
		t.Fatalf("edited account = %+v, %v", account, err)
	}

	clear := mutationPayload("clear", "account-clear", "live")
	clear["providerId"] = "codex"
	res = runCorrectnessMutation(t, srv, &out, clear)
	if res.Status != "success" || !strings.Contains(res.Message, "cleared local auth") {
		t.Fatalf("account clear = %+v", res)
	}
	pv, err := env.Deps.Providers.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	live, err := pv.DetectLive()
	if err != nil || live.Present {
		t.Fatalf("clear left live auth behind: %+v, %v", live, err)
	}

	unsupported := mutationPayload("clear-unsupported", "account-clear", "live")
	unsupported["providerId"] = "openrouter"
	if res := runCorrectnessMutation(t, srv, &out, unsupported); res.Status != "error" {
		t.Fatalf("unsupported clear claimed success: %+v", res)
	}
}

func TestDesktopAccountSaveUsesExplicitCredentialAndOverwrite(t *testing.T) {
	env := newTrayEnv(t)
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	payload := mutationPayload("credential-save", "account-save", "router")
	payload["providerId"] = "openrouter"
	payload["apiKey"] = "sk-desktop-test"
	payload["region"] = "global"
	payload["label"] = "Router Key"

	res := runCorrectnessMutation(t, srv, &out, payload)
	if res.Status != "success" {
		t.Fatalf("credential save = %+v", res)
	}
	account, err := env.Deps.Store.Get("openrouter", "router")
	if err != nil || account == nil {
		t.Fatalf("credential account = %+v, %v", account, err)
	}
	if account.Meta.CredentialKind != "proxy-only" || account.Meta.Label != "Router Key" {
		t.Fatalf("credential metadata = %+v", account.Meta)
	}

	payload["requestId"] = "credential-no-overwrite"
	payload["apiKey"] = "sk-replacement"
	if res := runCorrectnessMutation(t, srv, &out, payload); res.Status != "error" {
		t.Fatalf("credential overwrite without confirmation = %+v", res)
	}
	payload["requestId"] = "credential-overwrite"
	payload["overwrite"] = true
	if res := runCorrectnessMutation(t, srv, &out, payload); res.Status != "success" {
		t.Fatalf("credential overwrite = %+v", res)
	}
}

func TestDesktopGatewayRefreshUsesStoredSecretWithoutEchoingIt(t *testing.T) {
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("authorization"); got != "Bearer sk-never-echo" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`))
	}))
	t.Cleanup(models.Close)

	env := newTrayEnv(t)
	if _, err := env.Deps.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta:    profile.Meta{Name: "live-gateway", Provider: "openai", Endpoint: models.URL + "/v1"},
		Secrets: profile.Secrets{APIKey: "sk-never-echo"},
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	payload := mutationPayload("gateway-refresh", "gateway-refresh", "live-gateway")
	res := runCorrectnessMutation(t, srv, &out, payload)
	if res.Status != "success" || !strings.Contains(res.Message, "2 models") {
		t.Fatalf("gateway refresh = %+v", res)
	}
	if strings.Contains(out.String(), "sk-never-echo") {
		t.Fatal("gateway secret crossed the desktop output boundary")
	}
}

func TestSnapshotPublishesProviderLoginMethods(t *testing.T) {
	env := newTrayEnv(t)
	env.writeCodexLive("login-method@example.com")
	srv := New(env.Deps, strings.NewReader(""), &bytes.Buffer{})
	snapshot, err := srv.buildSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.AccountProviders {
		if row.ProviderID != "codex" {
			continue
		}
		if len(row.LoginMethods) == 0 {
			t.Fatal("codex login methods were omitted")
		}
		method := row.LoginMethods[0]
		if method.ID == "" || method.Label == "" || method.Provenance == "" {
			t.Fatalf("incomplete login method: %+v", method)
		}
		if !row.Installed || !row.LivePresent || row.LiveIdentity != "login-method@example.com" {
			t.Fatalf("provider availability = %+v", row)
		}
		return
	}
	t.Fatal("codex provider row missing")
}

func TestSnapshotPublishesPluginDegradedConditions(t *testing.T) {
	env := newTrayEnv(t)
	env.Deps.PluginDiagnostics = func(context.Context) plugins.Diagnostics {
		return plugins.Diagnostics{
			Host: plugins.HostDiagnostic{
				State: "degraded", LoadedPlugins: 1, Error: "pluginhost: host crashed",
			},
			Failures: []plugins.LoadFailure{{
				Name: "changed-plugin", Reason: "package digest changed", Untrusted: true,
			}},
		}
	}
	snapshot, err := New(env.Deps, strings.NewReader(""), &bytes.Buffer{}).
		buildSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.DegradedConditions) != 2 {
		t.Fatalf("degraded conditions = %+v", snapshot.DegradedConditions)
	}
	if snapshot.DegradedConditions[0].ID != "plugin-host" ||
		snapshot.DegradedConditions[1].ID != "plugin:changed-plugin" ||
		snapshot.DegradedConditions[1].Title != "Plugin changed after trust" {
		t.Fatalf("degraded conditions = %+v", snapshot.DegradedConditions)
	}
}

func TestProxySnapshotStateHandlesNilAndStatusErrors(t *testing.T) {
	if running, address := proxySnapshotState(nil, nil); running || address != "" {
		t.Fatalf("nil status = %v, %q", running, address)
	}
	status := &proxy.Status{Running: true, Endpoint: "http://127.0.0.1:43123"}
	if running, address := proxySnapshotState(status, errors.New("status failed")); running || address != "" {
		t.Fatalf("errored status = %v, %q", running, address)
	}
	if running, address := proxySnapshotState(status, nil); !running || address != status.Endpoint {
		t.Fatalf("healthy status = %v, %q", running, address)
	}
}

func TestInvokePublishesExactlyOneTerminalResult(t *testing.T) {
	env := newTrayEnv(t)
	var out lockedOutput
	srv := New(env.Deps, strings.NewReader(""), &out)
	srv.revision = 1
	var calls atomic.Int32
	srv.actions = map[string]func(context.Context) error{
		"once": func(context.Context) error {
			calls.Add(1)
			return nil
		},
	}
	raw, err := json.Marshal(invokePayload{
		Version: 1, RequestID: "invoke-once", Revision: 1, ActionID: "once",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.handle(context.Background(), &Command{Kind: "invoke", Payload: raw}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() == 0 || !strings.Contains(out.String(), "result\t") {
		if time.Now().After(deadline) {
			t.Fatalf("invoke did not finish: calls=%d output=%q", calls.Load(), out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Give the goroutine time to complete its following snapshot write. A
	// second terminal result, if present, is emitted before that refresh.
	time.Sleep(25 * time.Millisecond)
	resultCount := 0
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.HasPrefix(line, "result\t") {
			continue
		}
		var result Result
		if err := json.Unmarshal(decodeFramePayload(t, line), &result); err != nil {
			t.Fatal(err)
		}
		if result.RequestID == "invoke-once" {
			resultCount++
		}
	}
	if calls.Load() != 1 || resultCount != 1 {
		t.Fatalf("invoke calls=%d terminal results=%d output=%q",
			calls.Load(), resultCount, out.String())
	}
}
