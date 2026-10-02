package desktop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/hub"
)

func TestSnapshotOmitsRunningProxyToken(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()
	env.writeCodexLive("desk@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "desk", true); err != nil {
		t.Fatal(err)
	}
	fakeHealthyProxy(t, env, "codex", "desk", "inst-desk")
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tok-desk") || strings.Contains(string(raw), `"apiKey"`) {
		t.Fatal("proxy snapshot carried a bearer")
	}
	var row *ProxyRow
	for i := range snap.Proxies {
		if snap.Proxies[i].ID == "codex/desk" {
			row = &snap.Proxies[i]
			break
		}
	}
	if row == nil || !row.Running || !row.HasToken {
		t.Fatalf("proxy row = %+v", row)
	}
}

func TestHubSnapshotDoesNotMintOrCarryToken(t *testing.T) {
	env := hubTestEnv(t)
	ctx := context.Background()
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{Name: "default", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	if _, err := srv.buildSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	cfg, err := env.Deps.Hub.GetConfigContext(ctx, "default")
	if err != nil || cfg == nil || cfg.Token != "" {
		t.Fatalf("snapshot minted a hub token: %+v %v", cfg, err)
	}

	token, err := env.Deps.Hub.EnsureHubTokenContext(ctx, "default")
	if err != nil || token == "" {
		t.Fatal(err)
	}
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), `"apiKey"`) {
		t.Fatal("hub snapshot carried the bearer")
	}
	var row *ProxyRow
	for i := range snap.Proxies {
		if snap.Proxies[i].ID == "proxy-hub/default" {
			row = &snap.Proxies[i]
			break
		}
	}
	if row == nil || !row.HasToken {
		t.Fatalf("hub row = %+v", row)
	}
}

func TestMutateProxyTokenPayloadOmitsMessage(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()
	env.writeCodexLive("copy@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "copy", true); err != nil {
		t.Fatal(err)
	}
	fakeHealthyProxy(t, env, "codex", "copy", "inst-copy")
	if _, err := DecodeLine("mutate\t" + mustB64(t, map[string]any{
		"version": 1, "requestId": "r", "operation": "proxy-token",
		"providerId": "codex", "name": "copy",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLine("mutate\t" + mustB64(t, map[string]any{
		"version": 1, "requestId": "r", "operation": "proxy-token", "name": "copy",
	})); err == nil {
		t.Fatal("proxy-token without providerId was accepted")
	}

	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	res := runProxyToken(t, srv, Mutate{
		Version: 1, RequestID: "pt", Operation: "proxy-token",
		ProviderID: "codex", Name: "copy",
	})
	if res.Status != "success" || res.Message != "API key ready." || strings.Contains(res.Message, "tok-copy") {
		t.Fatalf("result = %+v", res)
	}
	var payload struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.Unmarshal(res.Payload, &payload); err != nil || payload.APIKey != "tok-copy" {
		t.Fatalf("payload = %s %v", res.Payload, err)
	}
	assertNonResultFramesOmit(t, srv.Out.(*strings.Builder).String(), "tok-copy")

	idle := runProxyToken(t, srv, Mutate{
		Version: 1, RequestID: "pt-idle", Operation: "proxy-token",
		ProviderID: "codex", Name: "missing",
	})
	if idle.Status != "error" || idle.Code != "PROXY_NOT_RUNNING" || len(idle.Payload) != 0 {
		t.Fatalf("idle = %+v", idle)
	}
	if strings.Contains(idle.Message, "tok-") {
		t.Fatalf("idle message leaked a token: %q", idle.Message)
	}
}

func TestMutateHubTokenMintsOnlyOnCommand(t *testing.T) {
	env := hubTestEnv(t)
	ctx := context.Background()
	if _, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{Name: "default", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	res := runProxyToken(t, srv, Mutate{
		Version: 1, RequestID: "hub-pt", Operation: "proxy-token",
		ProviderID: "proxy-hub", Name: "default",
	})
	var payload struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.Unmarshal(res.Payload, &payload); err != nil || payload.APIKey == "" {
		t.Fatalf("payload = %s %v", res.Payload, err)
	}
	if res.Message != "API key ready." || strings.Contains(res.Message, payload.APIKey) {
		t.Fatalf("message = %q", res.Message)
	}
	cfg, err := env.Deps.Hub.GetConfigContext(ctx, "default")
	if err != nil || cfg == nil || cfg.Token != payload.APIKey {
		t.Fatalf("stored token = %+v %v", cfg, err)
	}
	assertNonResultFramesOmit(t, srv.Out.(*strings.Builder).String(), payload.APIKey)
}

func runProxyToken(t *testing.T, srv *Server, m Mutate) Result {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	out := &strings.Builder{}
	srv.Out = out
	if err := srv.handleMutate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	return decodeMutateResult(t, out.String())
}

func assertNonResultFramesOmit(t *testing.T, out, secret string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "result\t") {
			continue
		}
		decoded := string(decodeFramePayload(t, line))
		if strings.Contains(decoded, secret) || strings.Contains(decoded, `"apiKey"`) {
			t.Fatal("a non-result frame carried the proxy bearer")
		}
	}
}
