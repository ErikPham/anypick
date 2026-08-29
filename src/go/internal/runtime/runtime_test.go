package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/catalog"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/db"
	"github.com/outof0/anypick/go/internal/profile"
	"github.com/outof0/anypick/go/internal/transport"
)

func testService(t *testing.T) *Service {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	home := t.TempDir()
	reg := client.NewRegistry()
	if err := client.RegisterBuiltin(reg, home); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANYPICK_CLAUDE_NO_KEYCHAIN", "1")
	return &Service{
		Root:     t.TempDir(),
		Home:     home,
		Clients:  reg,
		Profiles: &profile.Store{DB: database.DB},
		Catalog:  catalog.NewIndex(catalog.Builtin()),
	}
}

// THE FIX: claude (Anthropic protocol) must not receive an OpenAI-style /v1
// base — the SDK appends /v1/messages itself.
func TestSmartEndpointStripsV1ForAnthropicClients(t *testing.T) {
	s := testService(t)
	ctx := context.Background()

	if _, err := s.Profiles.Upsert(profile.Runtime{
		Meta: profile.Meta{
			Name: "or-work", Provider: "openrouter",
			Endpoint: "https://openrouter.ai/api/v1",
		},
		Secrets: profile.Secrets{APIKey: "sk-or"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ApplyGatewayProfile(ctx, "or-work", "claude"); err != nil {
		t.Fatalf("apply claude: %v", err)
	}

	env := readSettingsEnv(t, filepath.Join(s.Home, ".claude", "settings.json"))
	got := env["ANTHROPIC_BASE_URL"]
	if got == "https://openrouter.ai/api/v1" {
		t.Fatalf("STILL BROKEN: %q would make Claude Code call /api/v1/v1/messages", got)
	}
	if got != "https://openrouter.ai/api" {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want https://openrouter.ai/api", got)
	}
}

// The OpenAI-protocol client keeps the vendor's canonical /v1 surface.
func TestSmartEndpointKeepsV1ForOpenAIClients(t *testing.T) {
	s := testService(t)

	if _, err := s.Profiles.Upsert(profile.Runtime{
		Meta: profile.Meta{
			Name: "or-codex", Provider: "openrouter",
			Endpoint: "https://openrouter.ai/api/v1",
		},
		Secrets: profile.Secrets{APIKey: "sk-or"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyGatewayProfile(context.Background(), "or-codex", "codex"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(codexConfigPath(s))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "https://openrouter.ai/api/v1") {
		t.Errorf("codex config lost the /v1 surface:\n%s", raw)
	}
}

// Unknown vendors without a trailing /v1 pass through untouched, and explicit
// catalog entries win over the heuristic.
func TestSmartEndpointEdgeCases(t *testing.T) {
	caps := &client.Capabilities{ProtocolPreference: transport.Anthropic}

	// No /v1 → untouched.
	if got := SmartEndpointFor(caps, "custom", "https://relay.example/x", catalog.NewIndex(catalog.Builtin())); got != "https://relay.example/x" {
		t.Errorf("got %q", got)
	}
	// Unknown vendor with /v1 → heuristic strips it.
	if got := SmartEndpointFor(caps, "unknown-vendor", "https://gw.example/v1/", catalog.NewIndex(catalog.Builtin())); got != "https://gw.example" {
		t.Errorf("got %q", got)
	}
	// Non-anthropic client → never rewritten.
	openaiCaps := &client.Capabilities{ProtocolPreference: transport.OpenAI}
	if got := SmartEndpointFor(openaiCaps, "openrouter", "https://openrouter.ai/api/v1", catalog.NewIndex(catalog.Builtin())); got != "https://openrouter.ai/api/v1" {
		t.Errorf("got %q", got)
	}
	// Nil caps → untouched.
	if got := SmartEndpointFor(nil, "openrouter", "https://openrouter.ai/api/v1", nil); got != "https://openrouter.ai/api/v1" {
		t.Errorf("got %q", got)
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

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
