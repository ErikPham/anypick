package client

import (
	"sort"
	"testing"

	"github.com/outof0/anypick/go/internal/transport"
)

func TestRegistryBasic(t *testing.T) {
	r := NewRegistry()
	a := &fakeAdapter{id: "claude", name: "Claude Code"}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if !r.Has("claude") {
		t.Fatalf("Has want true")
	}
	got, err := r.Get("claude")
	if err != nil || got.ID() != "claude" {
		t.Fatalf("Get = %#v %v", got, err)
	}
	if _, err := r.Get("missing"); err == nil {
		t.Fatalf("Get missing want error")
	}
	// Re-registering replaces the value without duplicating its order entry.
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if len(r.IDs()) != 1 {
		t.Fatalf("IDs len=%d want 1", len(r.IDs()))
	}
}

func TestRegistryIDsSorted(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"codex", "claude", "zzz"} {
		_ = r.Register(&fakeAdapter{id: id})
	}
	ids := r.IDs()
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for i := range ids {
		if ids[i] != sorted[i] {
			t.Fatalf("IDs not sorted: %v", ids)
		}
	}
}

func TestRegisterBuiltin(t *testing.T) {
	r := NewRegistry()
	home := t.TempDir()
	if err := RegisterBuiltin(r, home); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"claude", "codex", "gemini", "kiro", "opencode", "workbuddy", "zcode", "dsh", "commandcode", "pi", "omp"} {
		if !r.Has(id) {
			t.Fatalf("missing builtin %q", id)
		}
		a, _ := r.Get(id)
		caps := a.Capabilities()
		if caps == nil || caps.ID != id {
			t.Fatalf("%q capabilities = %#v", id, caps)
		}
	}
	r.Seal()
	if err := r.Register(&fakeAdapter{id: "late"}); err == nil {
		t.Fatal("registration after seal should fail")
	}
}

func TestCapabilitiesAcceptProtocols(t *testing.T) {
	r := NewRegistry()
	home := t.TempDir()
	_ = RegisterBuiltin(r, home)
	claude, _ := r.Get("claude")
	if !hasProtocol(claude.Capabilities().AcceptedProtocols, transport.Anthropic) {
		t.Fatalf("claude should accept anthropic")
	}
	codex, _ := r.Get("codex")
	if !hasProtocol(codex.Capabilities().AcceptedProtocols, transport.OpenAI) {
		t.Fatalf("codex should accept openai")
	}
	zcode, _ := r.Get("zcode")
	if !hasProtocol(zcode.Capabilities().AcceptedProtocols, transport.Anthropic) {
		t.Fatalf("zcode should accept anthropic")
	}
	if zcode.Capabilities().ProtocolPreference != transport.Anthropic {
		t.Fatalf("zcode protocol preference = %q, want anthropic",
			zcode.Capabilities().ProtocolPreference)
	}
}

func hasProtocol(ps []transport.Protocol, want transport.Protocol) bool {
	for _, p := range ps {
		if p == want {
			return true
		}
	}
	return false
}

type fakeAdapter struct {
	id   string
	name string
}

func (f *fakeAdapter) ID() string                                  { return f.id }
func (f *fakeAdapter) Name() string                                { return f.name }
func (f *fakeAdapter) Capabilities() *Capabilities                 { return &Capabilities{ID: f.id} }
func (f *fakeAdapter) Apply(_ *ApplyContext) (*ApplyResult, error) { return &ApplyResult{}, nil }
func (f *fakeAdapter) Reset() error                                { return nil }
func (f *fakeAdapter) Inspect() (*InspectResult, error)            { return &InspectResult{Present: false}, nil }
