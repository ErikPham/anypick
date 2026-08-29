package catalog

import (
	"sort"
	"testing"
)

func TestBuiltinIDsSortedAndUnique(t *testing.T) {
	ids := IDs()
	if len(ids) != 8 {
		t.Fatalf("IDs len=%d want 8, got %v", len(ids), ids)
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for i := range ids {
		if ids[i] != sorted[i] {
			t.Fatalf("IDs not sorted: %v", ids)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestBuiltinEntriesValid(t *testing.T) {
	builtin := Builtin()
	for id, p := range builtin {
		if p.ID != id {
			t.Errorf("%q: Provider.ID=%q mismatch key", id, p.ID)
		}
		if p.Name == "" {
			t.Errorf("%q: empty Name", id)
		}
		if p.APIStyle != StyleOpenAI && p.APIStyle != StyleAnthropic && p.APIStyle != StyleCustom {
			t.Errorf("%q: invalid APIStyle %q", id, p.APIStyle)
		}
		if len(p.Protocols) == 0 {
			t.Errorf("%q: no Protocols", id)
		}
		if id != "custom" && p.DefaultEndpoint == "" {
			t.Errorf("%q: empty DefaultEndpoint (only custom may be empty)", id)
		}
		if p.AnthropicEndpoint != "" && p.AnthropicEndpoint == p.DefaultEndpoint {
			t.Errorf("%q: AnthropicEndpoint same as DefaultEndpoint %q", id, p.DefaultEndpoint)
		}
	}
}

func TestBuiltinOpenRouterAnthropicSurface(t *testing.T) {
	or := Builtin()["openrouter"]
	if or.AnthropicEndpoint != "https://openrouter.ai/api" {
		t.Fatalf("openrouter AnthropicEndpoint=%q", or.AnthropicEndpoint)
	}
	// Anthropic surface must NOT end with /v1 (SDK appends /v1/messages).
	if len(or.AnthropicEndpoint) >= 3 && or.AnthropicEndpoint[len(or.AnthropicEndpoint)-3:] == "/v1" {
		t.Fatalf("openrouter AnthropicEndpoint must not end with /v1: %q", or.AnthropicEndpoint)
	}
}
