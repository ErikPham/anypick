package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
)

func TestCompatibleDefinitionValidation(t *testing.T) {
	if _, err := NewCompatible(CompatibleDefinition{ID: "x"}); err == nil {
		t.Fatal("missing credentials must be rejected")
	}
	if _, err := NewCompatible(CompatibleDefinition{
		ID: "x", Name: "X",
		Credentials: []CredentialDefinition{{Kind: "api-key"}},
	}); err == nil {
		t.Fatal("credential without endpoint profile must be rejected")
	}
	if _, err := NewCompatible(CompatibleDefinition{
		ID: "x", Name: "X",
		Credentials: []CredentialDefinition{
			{Kind: "api-key", Profiles: []EndpointProfile{{ID: "g"}}},
			{Kind: "api-key", Profiles: []EndpointProfile{{ID: "g"}}},
		},
	}); err == nil {
		t.Fatal("duplicate credential kinds must be rejected")
	}
}

// TestEveryBuiltinCompatibleDefinitionIsValid guards the compiled-in vendor
// table. Every entry is data, so a bad one — a duplicated credential kind, a
// missing profile, an insecure endpoint — only surfaces at app startup without
// this, and startup failures are the worst place to find out.
func TestEveryBuiltinCompatibleDefinitionIsValid(t *testing.T) {
	definitions := BuiltinDefinitions()
	if len(definitions) == 0 {
		t.Fatal("no built-in compatible providers")
	}
	seenID := make(map[string]bool, len(definitions))
	seenName := make(map[string]string, len(definitions))
	for _, def := range definitions {
		if seenID[def.ID] {
			t.Fatalf("duplicate built-in compatible provider id %q", def.ID)
		}
		seenID[def.ID] = true
		// Two rows with the same display name are indistinguishable in every
		// list, picker and account label the product renders. Porting the
		// popular-providers pack verbatim shipped exactly that (together-ai
		// alongside together), so the table is checked for it.
		if other, dup := seenName[def.Name]; dup {
			t.Errorf("duplicate display name %q on ids %s and %s: "+
				"qualify the name or drop one of them", def.Name, other, def.ID)
		}
		seenName[def.Name] = def.ID
		if _, err := NewCompatible(def); err != nil {
			t.Fatalf("built-in %q does not validate: %v", def.ID, err)
		}
		for _, cred := range def.Credentials {
			if cred.Label == "" {
				t.Errorf("%s: credential %q has no label", def.ID, cred.Kind)
			}
			for _, profile := range cred.Profiles {
				if !strings.HasPrefix(profile.OpenAIBase, "https://") {
					t.Errorf("%s/%s/%s: openaiBase must be HTTPS, got %q",
						def.ID, cred.Kind, profile.ID, profile.OpenAIBase)
				}
			}
		}
	}
}

func TestCompatibleBackupInputAndAdapter(t *testing.T) {
	def := BuiltinDefinitions()[1] // deepseek
	pv, err := NewCompatible(def)
	if err != nil {
		t.Fatal(err)
	}

	snap := t.TempDir()
	// Wrong kind refused.
	if _, err := pv.BackupInput("nope", "sk", "global", snap); err == nil {
		t.Fatal("unknown kind must fail")
	} else if ae, ok := err.(*anyerr.Error); !ok || ae.Code != "UNSUPPORTED_CREDENTIAL_INPUT" {
		t.Fatalf("err = %v", err)
	}
	// Unknown profile refused.
	if _, err := pv.BackupInput("api-key", "sk", "moon", snap); err == nil {
		t.Fatal("unknown profile must fail")
	}
	// Empty secret refused.
	if _, err := pv.BackupInput("api-key", "", "global", snap); err == nil {
		t.Fatal("empty secret must fail")
	}

	info, err := pv.BackupInput("api-key", "sk-ds-123", "global", snap)
	if err != nil {
		t.Fatal(err)
	}
	if info.CredentialKind != "proxy-only" || info.CredentialClass != "provider_api_key" {
		t.Errorf("info = %+v", info)
	}
	raw, _ := os.ReadFile(filepath.Join(snap, "compatible-credential.json"))
	var stored struct {
		Version int    `json:"version"`
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
		Secret  string `json:"secret"`
	}
	if json.Unmarshal(raw, &stored) != nil || stored.Version != 1 ||
		stored.Profile != "global" || stored.Secret != "sk-ds-123" {
		t.Errorf("stored = %s", raw)
	}

	// Native backup is always NO_LIVE_AUTH for compatible vendors.
	if _, err := pv.Backup(snap); err == nil {
		t.Fatal("native Backup must fail with NO_LIVE_AUTH")
	}

	// Pairing: only claude/codex ride the builtin proxy (proxySource).
	adapter := pv.AccountAdapter("work", false)
	if got := adapter.TransportFor("claude"); got != "managed_builtin_proxy" {
		t.Errorf("claude = %s", got)
	}
	if got := adapter.TransportFor("codex"); got != "managed_builtin_proxy" {
		t.Errorf("codex = %s", got)
	}
	if got := adapter.TransportFor("gemini"); got != "unsupported" {
		t.Errorf("gemini = %s, want unsupported", got)
	}
	if got := adapter.TransportFor("opencode"); got != "unsupported" {
		t.Errorf("opencode = %s, want unsupported", got)
	}
}

func TestCompatibleResolveUpstreamSchemes(t *testing.T) {
	cases := []struct {
		scheme     AuthScheme
		wantHeader string
		wantPrefix string
	}{
		{"", "Authorization", "Bearer "},
		{AuthBearer, "Authorization", "Bearer "},
		{AuthXAPIKey, "x-api-key", ""},
		{AuthAPIKey, "api-key", ""},
	}
	for _, tc := range cases {
		def := CompatibleDefinition{
			ID: "x", Name: "X",
			Credentials: []CredentialDefinition{{
				Kind: "api-key",
				Profiles: []EndpointProfile{{
					ID: "p", OpenAIBase: "https://api.example/v1", OpenAIAuth: tc.scheme,
				}},
			}},
		}
		pv, _ := NewCompatible(def)
		snap := t.TempDir()
		if _, err := pv.BackupInput("api-key", "sekrit", "p", snap); err != nil {
			t.Fatal(err)
		}
		up, err := pv.ResolveUpstream(snap)
		if err != nil {
			t.Fatalf("scheme %q: %v", tc.scheme, err)
		}
		if up.BaseURL.String() != "https://api.example/v1" ||
			up.AuthHeader != tc.wantHeader || up.AuthValuePrefix != tc.wantPrefix {
			t.Errorf("scheme %q → %+v", tc.scheme, up)
		}
		// No anthropicBase on these profiles → translation path.
		if !up.TranslateAnthropic || up.Anthropic != nil {
			t.Errorf("expected translate mode, got %+v", up.Anthropic)
		}
	}

	// Missing credential file errors clearly.
	pv, _ := NewCompatible(BuiltinDefinitions()[0])
	if _, err := pv.ResolveUpstream(t.TempDir()); err == nil {
		t.Fatal("expected NO_CREDENTIAL error")
	} else if !strings.Contains(err.Error(), "compatible-credential.json") {
		t.Errorf("err = %v", err)
	}
}

// Vendors with a native /anthropic surface resolve an Anthropic target and
// never enter translation mode.
func TestResolveUpstreamNativeAnthropicTargets(t *testing.T) {
	want := map[string]struct{ anthropic string }{
		"zai":          {"https://api.z.ai/api/anthropic"},
		"kimi":         {"https://api.moonshot.ai/anthropic"},
		"minimax":      {"https://api.minimax.io/anthropic"},
		"stepfun":      {"https://api.stepfun.com/step_plan"},
		"siliconflow":  {"https://api.siliconflow.cn"},
		"mimo":         {"https://token-plan-cn.xiaomimimo.com/anthropic"},
		"opencode-zen": {"https://opencode.ai/zen"},
		"opencode-go":  {"https://opencode.ai/zen/go"},
	}
	for _, def := range BuiltinDefinitions() {
		check, ok := want[def.ID]
		if !ok {
			continue
		}
		pv, err := NewCompatible(def)
		if err != nil {
			t.Fatalf("%s: %v", def.ID, err)
		}
		snap := t.TempDir()
		// Pick any credential+profile that declares a native Anthropic base.
		var kind, profile string
		for _, c := range def.Credentials {
			for _, p := range c.Profiles {
				if p.AnthropicBase != "" && kind == "" {
					kind, profile = c.Kind, p.ID
				}
			}
		}
		if kind == "" {
			t.Fatalf("%s: no anthropic profile found", def.ID)
		}
		if _, err := pv.BackupInput(kind, "sk-test", profile, snap); err != nil {
			t.Fatalf("%s: %v", def.ID, err)
		}
		up, err := pv.ResolveUpstream(snap)
		if err != nil {
			t.Fatalf("%s: %v", def.ID, err)
		}
		if up.Anthropic == nil {
			t.Errorf("%s: expected native Anthropic target", def.ID)
		} else if up.Anthropic.BaseURL.String() != check.anthropic {
			t.Errorf("%s: anthropic = %s, want %s", def.ID,
				up.Anthropic.BaseURL, check.anthropic)
		}
		if up.TranslateAnthropic {
			t.Errorf("%s: native target must disable translation", def.ID)
		}
		// Anthropic scheme defaults to x-api-key unless overridden.
		if up.Anthropic != nil && up.Anthropic.AuthHeader == "" {
			t.Errorf("%s: anthropic auth header empty", def.ID)
		}
	}
}

func TestBuiltinDefinitionsAreValid(t *testing.T) {
	ids := map[string]bool{}
	for _, def := range BuiltinDefinitions() {
		if ids[def.ID] {
			t.Errorf("duplicate builtin id %q", def.ID)
		}
		ids[def.ID] = true
		if _, err := NewCompatible(def); err != nil {
			t.Errorf("builtin %q invalid: %v", def.ID, err)
		}
	}
	for _, want := range []string{"openrouter", "deepseek", "kimi", "zai", "qwen"} {
		if !ids[want] {
			t.Errorf("builtin %q missing", want)
		}
	}
}
