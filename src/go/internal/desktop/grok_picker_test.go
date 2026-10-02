package desktop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/discovery"
	"github.com/outof0/anypick/go/internal/refs"
)

// writeGrokOIDCLive points GROK_AUTH_PATH at a usable OIDC session: the
// token-only shape in writeGrokLive carries no key/refresh token, so
// grokUpstream refuses it before discovery is even reached.
func writeGrokOIDCLive(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".grok", "auth.json")
	t.Setenv("GROK_AUTH_PATH", path)
	data := `{"https://auth.x.ai::client":{"key":"sk-live","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client","email":"grok-a@example.com"}}`
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A warmed Grok catalog must reach the Claude picker: cold cache publishes
// zero options (editable=false, "No models were discovered"), which hides the
// Models section and leaves the binding with no model pin — every request
// then fails upstream on Claude's native ids.
func TestClaudeGrokPickerShowsModelsAfterWarm(t *testing.T) {
	env := newTrayEnv(t)
	env.Deps.Proxy.Resolver = &delegatingUpstreamResolver{env: env}
	env.Deps.Discovery = &discovery.Service{Root: env.Root, Cache: env.Deps.ModelCache}
	writeGrokOIDCLive(t)
	ctx := context.Background()

	if _, err := env.Svc.Save(ctx, "grok", "a", true); err != nil {
		t.Fatalf("save grok: %v", err)
	}

	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"grok-4.5"},{"id":"grok-4.3"}]}`))
	}))
	defer upSrv.Close()
	t.Setenv("GROK_MODELS_ENDPOINT", upSrv.URL)

	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	if n, err := srv.warmAccountModelCatalog(ctx, "grok", "a"); err != nil || n == 0 {
		t.Fatalf("warm grok catalog: n=%d err=%v", n, err)
	}
	models := srv.accountCatalogList(ctx, refs.AccountRef("grok", "a"))
	if len(models) == 0 {
		t.Fatal("account catalog still empty after warm")
	}
	// CLI/desktop parity: `anypick models` serves DiscoverModels first, the
	// picker serves the warmed cache — both must publish the same set, or
	// one surface invents models the other cannot see.
	live, err := env.Deps.Proxy.DiscoverModels(ctx, "grok", "a")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if !sameModelSet(models, live) {
		t.Fatalf("cached %v != discovered %v", models, live)
	}

	// Bind Claude to the Grok account the way the app-route chip does, then
	// read the picker row out of a real snapshot.
	if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
		Client: "claude",
		Spec: bindings.Spec{
			Client: "claude",
			Source: refs.AccountRef("grok", "a"),
			Model:  bindings.OmittedModel(),
		},
	}); err != nil {
		t.Fatalf("bind claude: %v", err)
	}
	snap, err := srv.buildSnapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, row := range snap.ClientModelConfigs {
		if row["clientId"] != "claude" {
			continue
		}
		opts, _ := row["options"].([]map[string]any)
		if len(opts) == 0 {
			t.Fatalf("claude picker options empty: %+v", row)
		}
		if editable, _ := row["editable"].(bool); !editable {
			t.Fatalf("claude picker not editable: %+v", row)
		}
		found := false
		for _, opt := range opts {
			if id, _ := opt["modelId"].(string); id == "grok-4.5" {
				found = true
			}
		}
		if !found {
			t.Fatalf("grok-4.5 missing from claude picker: %+v", row)
		}
		return
	}
	t.Fatal("no claude row in ClientModelConfigs")
}

// Route switches for a client with no default-model requirement must still
// warm a proxy-capable source: otherwise claude × grok stays cold forever
// and the picker above never appears.
func TestWarmRouteModelCatalogCoversClaudeGrok(t *testing.T) {
	env := newTrayEnv(t)
	env.Deps.Proxy.Resolver = &delegatingUpstreamResolver{env: env}
	writeGrokOIDCLive(t)
	ctx := context.Background()

	if _, err := env.Svc.Save(ctx, "grok", "a", true); err != nil {
		t.Fatalf("save grok: %v", err)
	}
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"grok-4.5"}]}`))
	}))
	defer upSrv.Close()
	t.Setenv("GROK_MODELS_ENDPOINT", upSrv.URL)

	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	if got := srv.accountCatalogList(ctx, refs.AccountRef("grok", "a")); len(got) != 0 {
		t.Fatalf("expected cold cache, got %v", got)
	}
	srv.warmRouteModelCatalog(ctx, "claude", refs.AccountRef("grok", "a"))
	srv.Wait()
	if got := srv.accountCatalogList(ctx, refs.AccountRef("grok", "a")); len(got) == 0 {
		t.Fatal("route warm did not populate the grok catalog for claude")
	}
}

// Native same-family routes have no proxy catalog to warm: the provider
// cannot proxy, so warming would only spend egress and log a failure.
func TestWarmRouteModelCatalogSkipsNativeClaudeRoute(t *testing.T) {
	env := newTrayEnv(t)
	env.Deps.Proxy.Resolver = &delegatingUpstreamResolver{env: env}
	ctx := context.Background()

	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	srv.warmRouteModelCatalog(ctx, "claude", refs.AccountRef("claude", "a"))
	srv.Wait()
	if got := srv.accountCatalogList(ctx, refs.AccountRef("claude", "a")); len(got) != 0 {
		t.Fatalf("native route must not warm a catalog, got %v", got)
	}
}

func sameModelSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, m := range a {
		seen[m] = true
	}
	for _, m := range b {
		if !seen[m] {
			return false
		}
	}
	return true
}

// The picker shape is provider-agnostic: given a warmed (here seeded)
// account catalog, every proxy provider must publish editable options
// carrying its own provider id. codex is covered by
// TestSnapshotPublishesModelConfigForBoundClient; gemini and opencode follow
// the same template so the next provider with an empty picker fails here.
func TestSnapshotPickerShowsSeededCatalogForProviders(t *testing.T) {
	cases := []struct {
		provider string
		account  string
		catalog  string
		models   []string
	}{
		{"gemini", "g", "gemini-api", []string{"gemini-3.6-flash", "gemini-3.1-pro-preview"}},
		{"opencode", "o", "opencode", []string{"opencode-zen-model", "opencode-go-model"}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			env := newTrayEnv(t)
			env.Deps.Discovery = &discovery.Service{Root: env.Root, Cache: env.Deps.ModelCache}
			ctx := context.Background()
			ref := refs.AccountRef(tc.provider, tc.account)
			if err := env.Deps.Discovery.StoreLive(ctx, tc.catalog, accountCatalogCacheKey(ref), tc.models); err != nil {
				t.Fatalf("seed catalog: %v", err)
			}
			if _, err := env.Deps.Bindings.UpsertGlobalContext(ctx, bindings.Global{
				Client: "claude",
				Spec: bindings.Spec{
					Client: "claude",
					Source: ref,
					Model:  bindings.OmittedModel(),
				},
			}); err != nil {
				t.Fatalf("bind claude: %v", err)
			}
			srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
			snap, err := srv.buildSnapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			row := modelConfigFor(snap, "claude")
			if row == nil {
				t.Fatal("no claude row in ClientModelConfigs")
			}
			options, _ := row["options"].([]map[string]any)
			if editable, _ := row["editable"].(bool); !editable || len(options) == 0 {
				t.Fatalf("picker not editable/empty: %+v", row)
			}
			ids := map[string]bool{}
			for _, opt := range options {
				id, _ := opt["modelId"].(string)
				ids[id] = true
				if provider, _ := opt["providerId"].(string); provider != tc.provider {
					t.Fatalf("option providerId = %q, want %q: %+v", provider, tc.provider, opt)
				}
			}
			for _, want := range tc.models {
				if !ids[want] {
					t.Fatalf("model %q missing from picker %v", want, ids)
				}
			}
		})
	}
}
