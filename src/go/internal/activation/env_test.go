package activation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/outof0/anypick/go/internal/accounts"
	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/pools"
	"github.com/outof0/anypick/go/internal/presets"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/proxy"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/runtime"
	"github.com/outof0/anypick/go/internal/store"
)

// testEnv isolates every test from the developer's real machine: fake HOME,
// fake credential paths, isolated data root. Tests must never touch the real
// keychain or ~/.codex — the env overrides below guarantee that.
type testEnv struct {
	Root        string // anypick data root
	Home        string // fake $HOME
	CodexAuth   string // live ~/.codex/auth.json
	ClaudeCreds string

	UpstreamURL        *url.URL
	UpstreamAuth       string
	UpstreamAuthHeader string // empty = Authorization
	UpstreamAuthPrefix string // empty = "Bearer "

	Px *proxy.Service
	Rt *runtime.Service

	Deps *Deps
	Svc  *accounts.Service
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()

	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	t.Setenv("ANYPICK_CLAUDE_CREDENTIALS_PATH", filepath.Join(home, ".claude", ".credentials.json"))
	codexAuth := filepath.Join(home, ".codex", "auth.json")
	t.Setenv("CODEX_AUTH_PATH", codexAuth)

	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	st := store.New(root, database)
	provReg := provider.NewRegistry()
	if err := provider.RegisterBuiltin(provReg, home); err != nil {
		t.Fatal(err)
	}
	// Compatible vendors participate in tests too (fault injection may swap
	// adapters, never the pairing rules).
	if err := provider.RegisterBuiltinCompatibles(provReg); err != nil {
		t.Fatal(err)
	}
	clientReg := client.NewRegistry()
	if err := client.RegisterBuiltin(clientReg, home); err != nil {
		t.Fatal(err)
	}
	svc := accounts.New(st, provReg)

	env := &testEnv{
		Root: root, Home: home, CodexAuth: codexAuth,
		ClaudeCreds: filepath.Join(home, ".claude", ".credentials.json"),
	}
	env.UpstreamURL = nil
	env.UpstreamAuth = "upstream-secret"
	rt := &runtime.Service{
		Root:     root,
		Home:     home,
		Clients:  clientReg,
		Profiles: &profile.Store{DB: database.DB},
	}
	leases := &proxy.LeaseStore{DB: database.DB}
	px := proxy.New(st, leases)
	px.Resolver = &testUpstreamResolver{env: env}

	env.Deps = &Deps{
		Root:      root,
		Accounts:  &accountsView{Svc: svc},
		Providers: provReg,
		Clients:   clientReg,
		Bindings:  &bindings.Store{DB: database.DB},
		Presets:   &presetsView{Store: &presets.Store{DB: database.DB}},
		Profiles:  &profile.Store{DB: database.DB},
		Pools:     &poolsView{Store: &pools.Store{DB: database.DB}},
		Journal:   &journal.OperationJournal{DB: database.DB},
		Runtime:   rt,
		Proxy:     px,
		Catalog:   NewCatalogIndex(map[string]CatalogEntry{}),
		Hub:       &hub.Store{DB: database.DB},
		// Unit tests have no detached child to spawn; hub activations fail at
		// the spawn step, which is exactly the rollback seam they exercise.
		SpawnHubChild: func(string) (int, error) { return 0, errors.New("no live hub child in unit tests") },
	}
	env.Px = px
	env.Rt = rt
	env.Svc = svc
	return env
}

// testUpstreamResolver points the builtin proxy at a per-test upstream.
type testUpstreamResolver struct{ env *testEnv }

func (r *testUpstreamResolver) ResolveUpstream(_ context.Context, providerID, _ string) (*proxy.Upstream, error) {
	if r.env.UpstreamURL == nil {
		return nil, fmt.Errorf("no upstream configured for %s in this test", providerID)
	}
	return &proxy.Upstream{
		BaseURL:         r.env.UpstreamURL,
		Auth:            r.env.UpstreamAuth,
		AuthHeader:      r.env.UpstreamAuthHeader,
		AuthValuePrefix: r.env.UpstreamAuthPrefix,
	}, nil
}

// withFailingCodex swaps the codex client adapter for one whose
// RestoreNative always fails — injecting a fault into WriteClientConfig.
func (e *testEnv) withFailingCodex(t *testing.T) {
	t.Helper()
	reg := client.NewRegistry()
	if err := client.RegisterBuiltin(reg, e.Home); err != nil {
		t.Fatal(err)
	}
	inner, err := reg.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&failingCodexClient{inner: inner}); err != nil {
		t.Fatal(err)
	}
	e.Deps.Clients = reg
}

// writeCodexLive fabricates a signed-in Codex login (auth.json).
func (e *testEnv) writeCodexLive(t *testing.T, email, accessToken string) {
	t.Helper()
	data, _ := json.Marshal(map[string]interface{}{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"id_token":     fakeJWT(map[string]string{"email": email}),
			"access_token": accessToken,
			"account_id":   "acct-" + email,
		},
	})
	if err := os.MkdirAll(filepath.Dir(e.CodexAuth), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.CodexAuth, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) readCodexLive(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(e.CodexAuth)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func fakeJWT(claims map[string]string) string {
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) +
		"." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func decodeJWTClaims(segment string) map[string]interface{} {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return nil
	}
	var claims map[string]interface{}
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}

// identityOf extracts the email claim from a fabricated live auth document.
func identityOf(live map[string]interface{}) string {
	tokens, _ := live["tokens"].(map[string]interface{})
	if tokens == nil {
		return ""
	}
	idToken, _ := tokens["id_token"].(string)
	parts := splitDot(idToken)
	if len(parts) < 2 {
		return ""
	}
	claims := decodeJWTClaims(parts[1])
	email, _ := claims["email"].(string)
	return email
}

func splitDot(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// errorsAsAny mirrors TS isAnyPickError checks in tests.
func errorsAsAny(err error, target **anyerr.Error) bool {
	for err != nil {
		if e, ok := err.(*anyerr.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ---- shared plan-building helpers ----

func mustResolve(t *testing.T, env *testEnv, providerID, name string) *ResolvedSource {
	t.Helper()
	rs, err := MaterializeResolvedSource(
		context.Background(), refs.AccountRef(providerID, name), env.Deps)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func (e *testEnv) writeOpenCodeLive(t *testing.T, email string) {
	t.Helper()
	path := filepath.Join(e.Home, ".opencode", "auth.json")
	t.Setenv("OPENCODE_AUTH_PATH", path)
	data, _ := json.Marshal(map[string]interface{}{
		"opencode": map[string]string{"email": email},
	})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
