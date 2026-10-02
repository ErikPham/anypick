package activation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

func TestExecuteCommandCodeManagedProxyActivation(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	var upstreamAuth string
	upstream := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	})
	env.UpstreamURL = mustParseURL(t, upstream.URL)

	env.writeOpenCodeLive(t, "command@example.com")
	if _, err := env.Svc.Save(ctx, "opencode", "command", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Accounts.Svc.Store.SetProxyConfigContext(ctx, "opencode", "command",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "commandcode",
		Model:  "deepseek-4.1-flash",
		Source: refPtr(refs.AccountRef("opencode", "command")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Transport.Capability != "managed_builtin_proxy" {
		t.Fatalf("capability = %s, want managed_builtin_proxy", plan.Transport.Capability)
	}

	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = env.Px.Stop(ctx, "opencode", "command") }()

	assertCommandCodeActivationFiles(t, env.Home, result.ProxyEndpoint)
	token := plan.Transport.ManagedProxy.Token
	req, _ := http.NewRequest("POST", result.ProxyEndpoint+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-4.1-flash","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxied status = %d", resp.StatusCode)
	}
	if upstreamAuth != "Bearer "+env.UpstreamAuth {
		t.Fatalf("upstream auth = %q, want bearer upstream credential", upstreamAuth)
	}
}

func assertCommandCodeActivationFiles(t *testing.T, home, endpoint string) {
	t.Helper()
	dir := filepath.Join(home, ".commandcode")
	providers := readJSONFile(t, filepath.Join(dir, "providers.json"))
	providerMap, ok := providers["provider"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers.json provider = %#v", providers["provider"])
	}
	managed, ok := providerMap["anypick-managed"].(map[string]interface{})
	if !ok || managed["baseURL"] != endpoint+"/v1" {
		t.Fatalf("managed provider = %#v", providerMap["anypick-managed"])
	}
	if managed["apiKey"] != false {
		t.Fatalf("providers.json must not contain a raw proxy key: %#v", managed["apiKey"])
	}
	auth := readJSONFile(t, filepath.Join(dir, "auth.json"))
	credential, ok := auth["anypick-managed"].(map[string]interface{})
	if !ok || credential["key"] == "" {
		t.Fatalf("managed auth = %#v", auth["anypick-managed"])
	}
	config := readJSONFile(t, filepath.Join(dir, "config.json"))
	if config["model"] != "anypick-managed/deepseek-4.1-flash" {
		t.Fatalf("config model = %#v", config["model"])
	}
}

func readJSONFile(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return doc
}

func newTestHTTPServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}
