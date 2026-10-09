package client

import (
	"strings"
	"testing"
)

// The plain marker family is a strict prefix of the native one, so stripping
// the native block with the plain markers cut len(codexEndMarker) into the
// longer native END line and left that line's tail as a bare line at the
// document root. Codex then refuses the file outright:
//
//	key with no value, expected `=`
//	1 | -native-profile
//
// The property below is the one that was missing: stripping must not
// manufacture a bare line. It does NOT repair a file that already carries one
// — that stray line is indistinguishable from user content to a stripper, which
// is exactly why it survived every re-apply and made five named profiles
// unloadable.
func TestStripNativeBlockLeavesNoMarkerTail(t *testing.T) {
	fresh := codexNativeProfileBlock("m")

	// The defect, as it first appeared: the plain strip used to match the
	// native block's marker lines and hand back a bare "-native-profile".
	// Requiring a whole-line match means it now declines the native block
	// outright, leaving the native stripper to own it.
	if stripped, ok := stripManagedBlock(fresh); ok || stripped != fresh {
		t.Errorf("plain strip touched a native block: ok=%v %q", ok, stripped)
	}

	stripped, native := stripCodexNativeProfileBlock(fresh)
	if !native || stripped != "" {
		t.Errorf("native strip: ok=%v left %q; expected empty", native, stripped)
	}

	// A file that already carries the stray line keeps it: the strippers remove
	// their own block and nothing else. Repair is deliberately out of scope here
	// so the write path stays a pure function of the document it reads.
	corrupted := "-native-profile\n\n" + fresh
	stripped, _ = stripManagedBlock(corrupted)
	stripped, native = stripCodexNativeProfileBlock(stripped)
	if !native {
		t.Fatal("native block in a corrupted file was not recognised")
	}
	if strings.TrimSpace(stripped) != "-native-profile" {
		t.Errorf("strip changed content it does not own: %q", stripped)
	}
}

// The two families must stay separable: neither stripper may consume the other
// family's block.
func TestMarkerFamiliesDoNotCollide(t *testing.T) {
	plain := codexManagedBlock("m", "", false, "http://127.0.0.1:1/v1", "K", "")
	native := codexNativeProfileBlock("m")

	if _, ok := stripManagedBlock(native); ok {
		t.Error("plain strip consumed the native block")
	}
	if _, ok := stripCodexNativeProfileBlock(plain); ok {
		t.Error("native strip consumed the plain block")
	}
	if stripped, ok := stripManagedBlock(plain); !ok || stripped != "" {
		t.Errorf("plain block: ok=%v stripped to %q, want empty", ok, stripped)
	}
	// The provider table is inside the plain block, so an empty remainder is
	// the correct result, not a lost table.
	if !strings.Contains(plain, "[model_providers.anypick]") {
		t.Fatal("fixture no longer carries the provider table")
	}
}

// A re-apply must be a fixed point: running writeCodexNativeProfileConfig's
// strip-and-reinsert sequence twice produces the same document both times. Its
// absence is what made the corruption permanent across activations.
func TestNativeProfileStripReinsertIsAFixedPoint(t *testing.T) {
	round := func(content string) string {
		stripped, managed := stripManagedBlock(content)
		stripped, native := stripCodexNativeProfileBlock(stripped)
		return reinsertManagedBlock(stripped, codexNativeProfileBlock("m"), managed || native)
	}

	first := round("")
	if strings.Contains(first, "-native-profile") && !strings.Contains(first, "# END") {
		t.Fatalf("first write leaked a marker tail:\n%q", first)
	}
	if second := round(first); second != first {
		t.Errorf("re-apply changed the document\nfirst:  %q\nsecond: %q", first, second)
	}
}
