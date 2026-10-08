package activation

import (
	"testing"

	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/refs"
)

// The drift case the hardcoded map missed: zai owns zcode via NativeClientID.
// The map had no zai->zcode entry, so binding zcode to a zai account never
// warned about the unpinned model even though the account is the app's own
// login. The capability scan now agrees with the TUI and the desktop.
func TestZZZcodeNativeOwnershipFromZai(t *testing.T) {
	env := newTestEnv(t)
	resolved := &ResolvedSource{
		Kind: "account",
		Ref:  refs.Ref{Kind: refs.KindAccount, Provider: "zai", Name: "g"},
	}
	var warnings []Warning
	planWarningsFromUnpinnedModel(env.Deps, "zcode", resolved, bindings.OmittedModel(), nil, &warnings)
	if hasWarning(warnings, "MODEL_NOT_PINNED") {
		t.Fatalf("zcode bound to its OWN provider zai must not warn (native ownership): %+v", warnings)
	}
	// The contrast: the same zai account bound to a DIFFERENT client warns.
	var cross []Warning
	planWarningsFromUnpinnedModel(env.Deps, "claude", resolved, bindings.OmittedModel(), nil, &cross)
	if !hasWarning(cross, "MODEL_NOT_PINNED") {
		t.Fatal("claude bound to a zai account with no model must warn")
	}
}
