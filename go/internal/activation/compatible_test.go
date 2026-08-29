package activation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/refs"
)

// Compatible vendor e2e: save an API-key credential, activate claude through
// the builtin proxy, and verify scheme-aware upstream injection.
func TestExecuteCompatibleVendorProxyActivation(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	var upstreamHeaders []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHeaders = append(upstreamHeaders, r.Header.Get("x-api-key"))
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	env.UpstreamURL = mustParseURL(t, upstream.URL)
	env.UpstreamAuth = "sk-vendor-secret"
	// The vendor expects x-api-key; the resolver mirrors the definition's
	// scheme so the builtin proxy presents credentials correctly.
	env.UpstreamAuthHeader = "x-api-key"
	env.UpstreamAuthPrefix = ""

	// Register a custom x-api-key vendor under the test's own registry.
	def := provider.CompatibleDefinition{
		ID: "vendorx", Name: "Vendor X",
		Description: "test vendor",
		Credentials: []provider.CredentialDefinition{{
			Kind: "api-key", Label: "Vendor key",
			Profiles: []provider.EndpointProfile{{
				ID: "global", OpenAIBase: upstream.URL + "/v1",
				OpenAIAuth: provider.AuthXAPIKey,
			}},
		}},
	}
	cp, err := provider.NewCompatible(def)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Providers.Register(cp); err != nil {
		t.Fatal(err)
	}

	if _, err := env.Svc.SaveInput(ctx, "vendorx", "work",
		accounts.CredentialInput{Kind: "api-key", Secret: env.UpstreamAuth, Profile: "global"},
		true); err != nil {
		t.Fatalf("save input: %v", err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.AccountRef("vendorx", "work")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Transport.Capability != "managed_builtin_proxy" {
		t.Fatalf("capability = %s, want managed_builtin_proxy", plan.Transport.Capability)
	}
	for _, s := range plan.Steps {
		if s.Kind == StepWriteNativeAuth {
			t.Fatal("compatible vendors must never write native auth")
		}
	}

	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	defer func() { _ = env.Px.Stop(ctx, "vendorx", "work") }()

	// Claude settings.json carries the proxy endpoint + proxy token as
	// AUTH_TOKEN (loopback is not an official Anthropic endpoint).
	envJSON := readClaudeEnv(t, env)
	if !strings.HasPrefix(result.ProxyEndpoint, "http://127.0.0.1:") {
		t.Fatalf("proxy endpoint = %q", result.ProxyEndpoint)
	}
	if envJSON["ANTHROPIC_BASE_URL"] != result.ProxyEndpoint {
		t.Errorf("BASE_URL = %q, want %q", envJSON["ANTHROPIC_BASE_URL"], result.ProxyEndpoint)
	}
	token := plan.Transport.ManagedProxy.Token
	if envJSON["ANTHROPIC_AUTH_TOKEN"] != token {
		t.Errorf("AUTH_TOKEN = %q, want the per-instance proxy token", envJSON["ANTHROPIC_AUTH_TOKEN"])
	}
	if strings.Contains(envJSON["ANTHROPIC_AUTH_TOKEN"], env.UpstreamAuth) {
		t.Error("UPSTREAM SECRET LEAKED INTO CLIENT CONFIG")
	}

	// A request through the proxy reaches upstream with x-api-key injection.
	req, _ := http.NewRequest("POST", result.ProxyEndpoint+"/v1/messages",
		strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("proxied status = %d", resp.StatusCode)
	}
	if len(upstreamHeaders) != 1 || upstreamHeaders[0] != env.UpstreamAuth {
		t.Errorf("upstream saw x-api-key %v, want %q", upstreamHeaders, env.UpstreamAuth)
	}
}

func readClaudeEnv(t *testing.T, env *testEnv) map[string]string {
	t.Helper()
	return readSettingsEnv(t, filepath.Join(env.Home, ".claude", "settings.json"))
}

func mustParseURL(t *testing.T, raw string) *urlType { return parseURLForTest(t, raw) }

type urlType = url.URL

func parseURLForTest(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func readSettingsEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Env
}
