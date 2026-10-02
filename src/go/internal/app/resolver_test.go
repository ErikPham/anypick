package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/provider"
	"github.com/outof0/anypick/go/internal/store"
)

// A saved native OpenCode account must resolve through the provider instead of
// falling through to the resolver's "no builtin upstream for provider" error:
// `use claude --with opencode/<name>` reaches this path, and the adapter
// promises ManagedBuiltinProxy for claude and codex.
func TestSnapshotResolverResolvesOpenCodeAccount(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	// The live lookup reads the developer's real opencode.db otherwise.
	t.Setenv("OPENCODE_DATA_DIR", filepath.Join(t.TempDir(), "absent"))

	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(root, database)
	reg := provider.NewRegistry()
	if err := provider.RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}

	snap, _, err := st.PrepareSnapshot("opencode", "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "auth.json"),
		[]byte(`{"opencode-go":{"type":"api","key":"sk-saved-go"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMetaContext(context.Background(),
		store.Meta{Provider: "opencode", Name: "work"}, snap); err != nil {
		t.Fatal(err)
	}

	r := &snapshotUpstreamResolver{store: st, providers: reg, profiles: nil}
	up, err := r.ResolveUpstream(context.Background(), "opencode", "work")
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if up.Auth != "sk-saved-go" {
		t.Errorf("Auth = %q, want the snapshot's key", up.Auth)
	}
	if got, want := up.BaseURL.String(), "https://opencode.ai/zen/go/v1"; got != want {
		t.Errorf("BaseURL = %s, want %s", got, want)
	}
}

// A gemini gateway created via gateway-create carries the key in the profile
// store, not an account snapshot. The resolver must resolve the Gemini
// translation upstream so the shared proxy actually serves it (the "no builtin
// upstream" error would be a dead gateway entry in the UI) — and it must do so
// WITHOUT persisting the key to a snapshot .env.
//
// ResolveUpstream runs once per request, so a cleartext write there races a
// concurrent `anypick backup` with a half-written file and leaves a stale copy
// that keeps resolving after the key is rotated or deleted (invariants #2, #4).
func TestSnapshotResolverResolvesGeminiGatewayProfile(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()

	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(root, database)
	reg := provider.NewRegistry()
	if err := provider.RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	profiles := &profile.Store{Root: root, DB: database.DB}
	if _, err := profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta:    profile.Meta{Name: "prod", Provider: "gemini"},
		Secrets: profile.Secrets{APIKey: "AIza-gemini-gateway-key"},
	}); err != nil {
		t.Fatal(err)
	}

	r := &snapshotUpstreamResolver{store: st, providers: reg, profiles: profiles}
	up, err := r.ResolveUpstream(context.Background(), "gemini", "prod")
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if !up.TranslateGemini {
		t.Error("TranslateGemini = false, want true for gemini gateway")
	}
	if up.AuthHeader != "x-goog-api-key" || up.Auth != "AIza-gemini-gateway-key" {
		t.Errorf("Auth = %q/%q, want x-goog-api-key/AIza-gemini-gateway-key", up.AuthHeader, up.Auth)
	}

	// The regression: resolving a gateway must not write the key to disk. The
	// previous implementation seeded <snapshot>/.env on every call.
	//
	// Match on the filename rather than on the key's contents, because the
	// profile store's own database legitimately holds the key — it is the
	// owning store (invariant #6).
	var written []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && filepath.Base(p) == ".env" {
			written = append(written, p)
		}
		return nil
	})
	if len(written) > 0 {
		t.Fatalf("resolver persisted the gateway credential to disk: %v", written)
	}
}

// A grok gateway created via gateway-create carries no account snapshot and no
// API key — it reuses the live x.ai OIDC session (~/.grok/auth.json) shared
// with the account flow. The resolver must resolve it from that session so the
// gateway actually serves (the "no snapshot" error would be a dead gateway).
func TestSnapshotResolverResolvesGrokGatewaySession(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()

	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "auth.json")
	session := map[string]any{
		"key":           "grok-access-token",
		"refresh_token": "grok-refresh-token",
		// one hour out so the resolver does not attempt an OIDC refresh at load.
		"expires_at":     time.Now().Add(time.Hour).Format(time.RFC3339),
		"oidc_issuer":    "https://auth.x.ai",
		"oidc_client_id": "grok-cli",
	}
	data := map[string]any{"https://auth.x.ai::grok-cli": session}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_AUTH_PATH", authPath)

	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(root, database)
	reg := provider.NewRegistry()
	if err := provider.RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}

	r := &snapshotUpstreamResolver{store: st, providers: reg, profiles: nil}
	up, err := r.ResolveUpstream(context.Background(), "grok", "gw")
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if got, want := up.BaseURL.String(), "https://cli-chat-proxy.grok.com"; got != want {
		t.Errorf("BaseURL = %s, want %s", got, want)
	}
	// grok refreshes its short-lived OIDC token per request, so the upstream
	// must carry an AuthProvider rather than a frozen auth string.
	if up.AuthProvider == nil {
		t.Error("AuthProvider = nil, want per-request OIDC refresh")
	}
}

// A Cloudflare profile gateway (gateway-create) carries no account snapshot:
// the per-account endpoint plus the profile key and gateway id must resolve
// through the provider, or the gateway is dead on arrival.
func TestSnapshotResolverResolvesCloudflareGatewayProfile(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()

	database, err := db.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	st := store.New(root, database)
	reg := provider.NewRegistry()
	if err := provider.RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	ep := "https://api.cloudflare.com/client/v4/accounts/f4b3c2a1e5d647a8b9c0d1e2f3a4b5c6/ai/v1"
	profiles := &profile.Store{Root: root, DB: database.DB}
	if _, err := profiles.UpsertContext(context.Background(), profile.Runtime{
		Meta: profile.Meta{
			Name: "cf-prod", Provider: "cloudflare-aigateway", Endpoint: ep,
		},
		Secrets: profile.Secrets{
			APIKey:  "tok-123",
			Headers: map[string]string{"gatewayId": "prod"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	r := &snapshotUpstreamResolver{store: st, providers: reg, profiles: profiles}
	up, err := r.ResolveUpstream(context.Background(), "cloudflare-aigateway", "cf-prod")
	if err != nil {
		t.Fatalf("ResolveUpstream: %v", err)
	}
	if got := up.BaseURL.String(); got != ep {
		t.Errorf("base = %q, want endpoint", got)
	}
	if up.Auth != "tok-123" || up.AuthValuePrefix != "Bearer " {
		t.Errorf("auth = %q/%q", up.AuthValuePrefix, up.Auth)
	}
	found := false
	for _, h := range up.ExtraHeaders {
		if h.Name == "cf-aig-gateway-id" && h.Value == "prod" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing cf-aig-gateway-id header: %+v", up.ExtraHeaders)
	}
}
