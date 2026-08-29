package catalog

import (
	"testing"

	"github.com/outof0/anypick/go/internal/transport"
)

func TestIndexRecoverSwapAndRemove(t *testing.T) {
	idx := NewIndex(map[string]Provider{
		"openrouter": Builtin()["openrouter"],
	})
	plugin := Provider{
		ID: "fixture-catalog", Name: "Fixture", APIStyle: StyleOpenAI,
		Protocols: []transport.Protocol{transport.OpenAI},
	}
	if err := idx.RecoverSwap(nil, plugin); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, ok := idx.Lookup("fixture-catalog")
	if !ok || got.Name != "Fixture" {
		t.Fatalf("lookup after insert = %+v, %v", got, ok)
	}
	replacement := plugin
	replacement.Name = "Fixture B"
	if err := idx.RecoverSwap(&plugin, replacement); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if err := idx.RecoverSwap(&plugin, plugin); err == nil {
		t.Fatal("stale previous was allowed to overwrite")
	}
	if err := idx.RecoverRemove(Builtin()["openrouter"]); err != nil {
		t.Fatalf("remove matching built-in copy: %v", err)
	}
	if _, ok := idx.Lookup("openrouter"); ok {
		t.Fatal("openrouter remained after matching RecoverRemove")
	}
	if err := idx.RecoverRemove(plugin); err == nil {
		t.Fatal("stale catalog value was removed")
	}
	if err := idx.RecoverRemove(replacement); err != nil {
		t.Fatalf("remove replacement: %v", err)
	}
	if _, ok := idx.Lookup("fixture-catalog"); ok {
		t.Fatal("plugin catalog remained after RecoverRemove")
	}
}
