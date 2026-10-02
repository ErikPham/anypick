package desktop

// The Add Gateway sheet probes supplied credentials before anything is
// saved, and Account Detail re-probes the stored ones (C7). Only statuses
// cross the boundary — the key never appears in output.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/discovery"
	"github.com/outof0/anypick/go/internal/profile"
)

func gatewayTestServer(t *testing.T, status int, seenAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seenAuth != nil {
			*seenAuth = r.Header.Get("authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
}

func runGatewayTest(t *testing.T, env *trayEnv, m Mutate) Result {
	t.Helper()
	var out bytes.Buffer
	srv := New(env.Deps, strings.NewReader(""), &out)
	raw, _ := json.Marshal(m)
	if err := srv.handleMutate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var res Result
	if err := json.Unmarshal(decodeFramePayload(t, firstFrameLine(out.String())), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestGatewayTestProbesSuppliedCredentials(t *testing.T) {
	env := newTrayEnv(t)

	var auth string
	ok := gatewayTestServer(t, http.StatusOK, &auth)
	defer ok.Close()

	res := runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt1", Operation: "gateway-test",
		Name: "test", ProviderID: "custom",
		Endpoint: ok.URL, APIKey: "sk-live",
	})
	if res.Status != "success" {
		t.Fatalf("probe ok: %+v", res)
	}
	if auth != "Bearer sk-live" {
		t.Fatalf("authorization header = %q", auth)
	}

	// An auth rejection fails the test with the status, never the key (C7).
	denied := gatewayTestServer(t, http.StatusUnauthorized, nil)
	defer denied.Close()
	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt2", Operation: "gateway-test",
		Name: "test", ProviderID: "custom",
		Endpoint: denied.URL, APIKey: "sk-dead",
	})
	if res.Status != "error" || !strings.Contains(res.Message, "401") {
		t.Fatalf("probe denied: %+v", res)
	}
	if strings.Contains(res.Message, "sk-dead") {
		t.Fatalf("key crossed the output boundary: %+v", res)
	}

	// Nothing listening is unreachable, not a silent success.
	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt3", Operation: "gateway-test",
		Name: "test", ProviderID: "custom",
		Endpoint: "http://127.0.0.1:9", APIKey: "sk-live",
	})
	if res.Status != "error" || !strings.Contains(res.Message, "unreachable") {
		t.Fatalf("probe unreachable: %+v", res)
	}
}

// A saved gateway re-probes with its stored endpoint and key (C7): the
// Account Detail Test button uses this instead of a catalog refresh.
func TestGatewayTestUsesStoredProfile(t *testing.T) {
	env := newTrayEnv(t)

	var auth string
	ok := gatewayTestServer(t, http.StatusOK, &auth)
	defer ok.Close()

	if _, err := env.Deps.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta:    profile.Meta{Name: "gw1", Provider: "custom", Endpoint: ok.URL},
		Secrets: profile.Secrets{APIKey: "sk-stored"},
	}); err != nil {
		t.Fatal(err)
	}
	res := runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt4", Operation: "gateway-test",
		Name: "gw1", ProviderID: "custom",
	})
	if res.Status != "success" {
		t.Fatalf("stored probe: %+v", res)
	}
	if auth != "Bearer sk-stored" {
		t.Fatalf("stored authorization header = %q", auth)
	}
}

// Add Gateway's pre-create probe uses the sentinel name "test". A saved
// gateway of that name must not replace the endpoint and key the user typed.
// A retest that sends only the name still uses the stored credential.
func TestGatewayTestTypedFieldsWinOverSavedName(t *testing.T) {
	env := newTrayEnv(t)

	var storedAuth, typedAuth string
	stored := gatewayTestServer(t, http.StatusOK, &storedAuth)
	defer stored.Close()
	typed := gatewayTestServer(t, http.StatusOK, &typedAuth)
	defer typed.Close()

	if _, err := env.Deps.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta:    profile.Meta{Name: "test", Provider: "custom", Endpoint: stored.URL},
		Secrets: profile.Secrets{APIKey: "sk-stored"},
	}); err != nil {
		t.Fatal(err)
	}

	res := runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt-typed", Operation: "gateway-test",
		Name: "test", ProviderID: "custom",
		Endpoint: typed.URL, APIKey: "sk-typed",
	})
	if res.Status != "success" {
		t.Fatalf("typed probe: %+v", res)
	}
	if typedAuth != "Bearer sk-typed" {
		t.Fatalf("typed authorization = %q", typedAuth)
	}
	if storedAuth != "" {
		t.Fatalf("stored gateway was probed: %q", storedAuth)
	}

	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gt-saved", Operation: "gateway-test",
		Name: "test", ProviderID: "custom",
	})
	if res.Status != "success" {
		t.Fatalf("saved probe: %+v", res)
	}
	if storedAuth != "Bearer sk-stored" {
		t.Fatalf("stored authorization = %q", storedAuth)
	}
}

func TestGatewayRefreshDiscoversModelsFromRuntimeEndpoint(t *testing.T) {
	env := newTrayEnv(t)
	env.Deps.Discovery = &discovery.Service{Root: env.Root, Cache: env.Deps.ModelCache}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "model-added-after-release"}},
		})
	}))
	defer server.Close()

	if _, err := env.Deps.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta:    profile.Meta{Name: "dynamic", Provider: "future-provider", Endpoint: server.URL + "/v1"},
		Secrets: profile.Secrets{APIKey: "sk-dynamic"},
	}); err != nil {
		t.Fatal(err)
	}
	res := runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "gr1", Operation: "gateway-refresh", Name: "dynamic",
	})
	if res.Status != "success" || !strings.Contains(res.Message, "1 models") {
		t.Fatalf("gateway refresh: %+v", res)
	}
	entry, err := env.Deps.ModelCache.Get(context.Background(), "future-provider", server.URL+"/v1")
	if err != nil || entry == nil || len(entry.Models) != 1 || entry.Models[0] != "model-added-after-release" {
		t.Fatalf("dynamic gateway cache = %+v, err=%v", entry, err)
	}
}

// Cloudflare lists in the Add Gateway picker with a separately collected
// gateway id: its endpoint is per-account, so the provider cannot supply it,
// and several gateways share one account id.
func TestGatewayProvidersIncludeCloudflareWithGatewayID(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	found := false
	for _, row := range snap.GatewayProviders {
		if row.ID != "cloudflare-aigateway" {
			continue
		}
		found = true
		if !row.RequiresGatewayID {
			t.Error("cloudflare requires a separately collected gateway id")
		}
		if !row.ProvidesEndpoint {
			t.Error("cloudflare endpoint is derived per-account; the picker must not collect a URL")
		}
		if !row.RequiresAccountID {
			t.Error("cloudflare needs a separately collected account id")
		}
		if !row.RequiresApiKey {
			t.Error("cloudflare gateway needs an API token")
		}
	}
	if !found {
		t.Fatal("cloudflare-aigateway missing from gateway providers")
	}
}

// Compatible vendors publish their default base with the row: without it
// the sheet sends an empty endpoint and test and save fail closed.
func TestGatewayProvidersPublishCompatibleDefaultEndpoint(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	found := false
	for _, row := range snap.GatewayProviders {
		if row.ID != "openrouter" {
			continue
		}
		found = true
		if row.DefaultEndpoint != "https://openrouter.ai/api/v1" {
			t.Errorf("openrouter default endpoint = %q", row.DefaultEndpoint)
		}
		if !row.ProvidesEndpoint {
			t.Error("openrouter provides its endpoint")
		}
	}
	if !found {
		t.Fatal("openrouter missing from gateway providers")
	}
}

// gateway-create for Cloudflare derives and stores the endpoint from the
// account id: the sheet collects account id + gateway id + key, never a URL.
func TestGatewayCreateCloudflareDerivesEndpoint(t *testing.T) {
	env := newTrayEnv(t)
	account := "f4b3c2a1e5d647a8b9c0d1e2f3a4b5c6"
	wantEP := "https://api.cloudflare.com/client/v4/accounts/" + account + "/ai/v1"
	res := runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "cf-gw-1", Operation: "gateway-create",
		Name: "cf-prod", ProviderID: "cloudflare-aigateway",
		APIKey: "tok-123", Profile: account, GatewayID: "prod-gw",
	})
	if res.Status != "success" {
		t.Fatalf("gateway-create: %+v", res)
	}
	pr, err := env.Deps.Profiles.GetContext(context.Background(), "cf-prod")
	if err != nil || pr == nil {
		t.Fatalf("profile: %v", pr)
	}
	if pr.Meta.Endpoint != wantEP || pr.Secrets.APIKey != "tok-123" {
		t.Fatalf("endpoint/key not stored: %+v", pr.Meta)
	}
	if pr.Secrets.Headers["gatewayId"] != "prod-gw" {
		t.Fatalf("gateway id header = %+v", pr.Secrets.Headers)
	}

	// A pair in the account field fails closed with a hint instead of
	// silently picking a side between the pair and Gateway ID.
	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "cf-gw-2", Operation: "gateway-create",
		Name: "cf-pair", ProviderID: "cloudflare-aigateway",
		APIKey: "tok-123", Profile: account + "/other-gw", GatewayID: "prod-gw",
	})
	if res.Status != "error" {
		t.Fatalf("pair profile must fail: %+v", res)
	}

	// Missing account id fails closed.
	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "cf-gw-3", Operation: "gateway-create",
		Name: "cf-noacct", ProviderID: "cloudflare-aigateway",
		APIKey: "tok-123", GatewayID: "prod-gw",
	})
	if res.Status != "error" {
		t.Fatalf("missing account id must fail: %+v", res)
	}

	// Empty gateway id selects the provider default, never an error.
	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "cf-gw-4", Operation: "gateway-create",
		Name: "cf-default", ProviderID: "cloudflare-aigateway",
		APIKey: "tok-123", Profile: account,
	})
	if res.Status != "success" {
		t.Fatalf("empty gateway id: %+v", res)
	}
	pr, err = env.Deps.Profiles.GetContext(context.Background(), "cf-default")
	if err != nil || pr == nil || pr.Secrets.Headers["gatewayId"] != "default" {
		t.Fatalf("default gateway id not stored: %+v", pr)
	}
	if pr.Meta.Endpoint != wantEP {
		t.Fatalf("endpoint not derived: %q", pr.Meta.Endpoint)
	}
}

// gateway-test for Cloudflare derives the probe endpoint from the supplied
// profile: the sheet probes before anything is saved. A saved gateway
// re-probes its stored endpoint like any other vendor.
func TestGatewayTestCloudflareDerivesEndpoint(t *testing.T) {
	env := newTrayEnv(t)
	// Missing account id fails before any network, with guidance.
	res := runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "cf-gt-1", Operation: "gateway-test",
		Name: "unsaved", ProviderID: "cloudflare-aigateway", APIKey: "tok-123",
	})
	if res.Status != "error" || !strings.Contains(res.Message, "account id") {
		t.Fatalf("missing account id: %+v", res)
	}

	// A saved gateway re-probes its stored endpoint and key.
	ok := gatewayTestServer(t, http.StatusOK, nil)
	defer ok.Close()
	if _, err := env.Deps.Profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta:    profile.Meta{Name: "saved-cf", Provider: "cloudflare-aigateway", Endpoint: ok.URL},
		Secrets: profile.Secrets{APIKey: "tok-123"},
	}); err != nil {
		t.Fatal(err)
	}
	res = runGatewayTest(t, env, Mutate{
		Version: 1, RequestID: "cf-gt-2", Operation: "gateway-test",
		Name: "saved-cf", ProviderID: "cloudflare-aigateway",
	})
	if res.Status != "success" {
		t.Fatalf("saved probe: %+v", res)
	}
}
