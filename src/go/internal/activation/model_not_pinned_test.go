package activation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/store"
)

// hasWarning reports whether a plan carries the given warning code.
func hasWarning(warnings []Warning, code string) bool {
	for _, w := range warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// A client bound to an account from another vendor with nothing pinning a
// model must say so instead of quietly letting the client's own ids reach the
// upstream. See planWarningsFromUnpinnedModel for why no default is invented.
func TestModelNotPinnedWarningMatrix(t *testing.T) {
	cases := []struct {
		name     string
		client   string
		provider string
		kind     string
		model    bindings.ModelSelection
		roles    map[string]string
		want     bool
	}{
		{
			name: "claude on gemini with nothing pinned warns",
			// The case Erik hit: Gemini has no Haiku, so Claude Code's own
			// haiku tier reaches Code Assist verbatim and 404s.
			client: "claude", provider: "gemini", kind: "account",
			model: bindings.OmittedModel(), want: true,
		},
		{
			name:   "claude on its own provider does not warn",
			client: "claude", provider: "claude", kind: "account",
			model: bindings.OmittedModel(), want: false,
		},
		{
			name:   "an explicit model silences it",
			client: "claude", provider: "gemini", kind: "account",
			model: bindings.ExplicitModel("gemini-2.5-pro"), want: false,
		},
		{
			name:   "pinned model roles silence it",
			client: "claude", provider: "gemini", kind: "account",
			model: bindings.OmittedModel(),
			roles: map[string]string{"haiku": "gemini-2.5-flash"}, want: false,
		},
		{
			name: "gateway sources are not warned",
			// A gateway carries its own endpoint and model on the saved
			// profile; the plan cannot see them yet.
			client: "claude", provider: "gemini", kind: "gateway",
			model: bindings.OmittedModel(), want: false,
		},
		{
			name:   "a client with no provider of its own is not warned",
			client: "dsh", provider: "gemini", kind: "account",
			model: bindings.OmittedModel(), want: false,
		},
		{
			name:   "an unknown provider is not warned",
			client: "claude", provider: "", kind: "account",
			model: bindings.OmittedModel(), want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer func() { _ = env }()
			var warnings []Warning
			planWarningsFromUnpinnedModel(env.Deps, tc.client, &ResolvedSource{
				Kind: tc.kind,
				Ref:  refs.Ref{Provider: tc.provider},
			}, tc.model, tc.roles, &warnings)
			got := hasWarning(warnings, "MODEL_NOT_PINNED")
			if got != tc.want {
				t.Fatalf("warned = %v, want %v (%+v)", got, tc.want, warnings)
			}
		})
	}
}

// The planner has to actually wire the warning in — a unit test on the helper
// alone would still pass if PlanActivation forgot to call it.
func TestPlannerWarnsWhenModelIsNotPinnedAcrossVendors(t *testing.T) {
	env := newTestEnv(t)
	writeGeminiLive(t, env.Home)
	if _, err := env.Svc.Save(context.Background(), "gemini", "g", true); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(context.Background(), Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.AccountRef("gemini", "g")),
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !hasWarning(plan.Warnings, "MODEL_NOT_PINNED") {
		t.Fatalf("expected MODEL_NOT_PINNED, got %+v", plan.Warnings)
	}

	// Pinning a model is the documented fix and must clear the warning.
	pinned, err := PlanActivation(context.Background(), Request{
		Mode:   ModePersistent,
		Client: "claude",
		Source: refPtr(refs.AccountRef("gemini", "g")),
		Model:  "gemini-2.5-pro",
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("pinned plan: %v", err)
	}
	if hasWarning(pinned.Warnings, "MODEL_NOT_PINNED") {
		t.Fatalf("warning survived --model: %+v", pinned.Warnings)
	}
	if got := explicitModelID(pinned.Model); got != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want gemini-2.5-pro", got)
	}
}

// Plan-time warnings used to be dropped: Execute built its warning list from
// scratch, so nothing the planner said ever reached the CLI.
func TestPlanWarningsSurviveIntoTheExecuteResult(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	env.UpstreamURL = u

	writeGeminiLive(t, env.Home)
	if _, err := env.Svc.Save(ctx, "gemini", "g", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Accounts.Svc.Store.SetProxyConfigContext(ctx, "gemini", "g",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanActivation(ctx, Request{
		Mode:            ModeEphemeral,
		Client:          "claude",
		Source:          refPtr(refs.AccountRef("gemini", "g")),
		TransportPolicy: "proxy",
	}, env.Deps, Options{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	result, err := Execute(ctx, plan, env.Deps, ExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "MODEL_NOT_PINNED") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warning lost between plan and result: %v", result.Warnings)
	}
}

// writeGeminiLive fabricates a gemini-cli OAuth login in ~/.gemini. The
// provider was registered with the test home, so it reads this dir.
func writeGeminiLive(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	creds, _ := json.Marshal(map[string]any{
		"access_token": "ya29.test", "refresh_token": "1//test", "token_type": "Bearer",
		"expiry_date": time.Now().Add(time.Hour).UnixMilli(),
	})
	if err := os.WriteFile(filepath.Join(dir, "oauth_creds.json"), creds, 0o600); err != nil {
		t.Fatal(err)
	}
}
