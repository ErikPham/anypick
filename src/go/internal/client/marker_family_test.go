package client

import (
	"strings"
	"testing"
)

// knownMarkerOverlaps records every prefix collision that already exists in a
// managed-block family, with the reason it is safe. The markers are an on-disk
// contract — renaming one strands the block in every profile already written —
// so the collisions cannot be removed, only contained.
//
// They are safe because each stripper matches whole lines: a marker that only
// occurs as a prefix of a longer marker never occupies a line by itself, so
// the substring match that caused the defect cannot fire. TestStrippersMatchWholeLines
// is what holds that in place.
var knownMarkerOverlaps = map[[2]string]string{
	{codexBeginMarker, codexNativeBeginMarker}: "native profile block nests the plain family's begin marker",
	{codexEndMarker, codexNativeEndMarker}:     "native profile block nests the plain family's end marker",
	{dshPatchBegin, dshPatchEnd}:               "the patch end marker extends its own begin marker",
	{dshPatchBegin, dshSettingsBegin}:          "the settings markers sit under the patch begin marker",
	{dshPatchBegin, dshSettingsEnd}:            "the settings end marker sits under the patch begin marker",
	{dshSettingsBegin, dshSettingsEnd}:         "the settings end marker extends its own begin marker",
}

// The managed-block defect was a naming collision, not a coding slip: a stripper
// matching one marker matched a longer one that shared its prefix, then wrote
// the difference out as a bare line. Codex refused the file with "key with no
// value" and the operator saw nothing about which write broke it.
//
// A strip failure here is silent, so the naming rule is enforced here instead
// of trusted. A new nested marker has to either avoid the prefix collision or
// be listed above with its reason, which turns an accident into a decision.
func TestManagedMarkerOverlapsAreAllKnown(t *testing.T) {
	families := map[string][]string{
		"codex": {
			codexBeginMarker, codexEndMarker,
			codexNativeBeginMarker, codexNativeEndMarker,
		},
		"dsh": {
			dshPatchBegin, dshPatchEnd,
			dshSettingsBegin, dshSettingsEnd,
		},
		"omp": {
			ompModelsBegin, ompModelsEnd,
			ompConfigBegin, ompConfigEnd,
		},
	}

	for family, markers := range families {
		for i, a := range markers {
			for j, b := range markers {
				if i == j || !strings.HasPrefix(b, a) {
					continue
				}
				if _, ok := knownMarkerOverlaps[[2]string{a, b}]; !ok {
					t.Errorf("%s: %q is a prefix of %q and the overlap is unrecorded.\n"+
						"Either spell the nested marker so no stripper can confuse them, or add it "+
						"to knownMarkerOverlaps with the reason it is safe.", family, a, b)
				}
			}
		}
	}

	// A listed overlap that no longer exists is stale: it hides the reason a
	// marker was renamed and it would mask a future collision with the same pair.
	for pair, reason := range knownMarkerOverlaps {
		if reason == "" {
			t.Errorf("knownMarkerOverlaps has %q -> %q with no reason", pair[0], pair[1])
		}
		if !strings.HasPrefix(pair[1], pair[0]) {
			t.Errorf("knownMarkerOverlaps lists %q -> %q, which is no longer a prefix pair", pair[0], pair[1])
		}
	}
}

// Every stripper has to go through the whole-line matcher. One reaching for
// strings.Index or strings.Contains directly reintroduces the defect with no
// other test failing, because the fixtures all happen to be well-formed.
func TestStrippersMatchWholeLines(t *testing.T) {
	// A marker that only matches as a substring: the plain family inside the
	// native one. A substring search finds it; a whole-line search does not.
	decoy := codexNativeBeginMarker
	if findMarkerLine(decoy, codexBeginMarker) >= 0 {
		t.Errorf("findMarkerLine matched %q inside %q as a substring", codexBeginMarker, decoy)
	}
	if hasMarkerLine(decoy, codexBeginMarker) {
		t.Errorf("hasMarkerLine matched %q inside %q as a substring", codexBeginMarker, decoy)
	}
	if hasMarkerLine("x "+codexBeginMarker+"\n", codexBeginMarker) {
		t.Error("hasMarkerLine matched a marker that is not at the start of its line")
	}
	if findMarkerLine(codexBeginMarker+" trailing\n", codexBeginMarker) >= 0 {
		t.Error("findMarkerLine matched a marker with trailing text on its line")
	}

	for _, marker := range []string{codexBeginMarker, codexNativeEndMarker, dshPatchBegin, dshSettingsEnd} {
		if !hasMarkerLine(marker+"\n", marker) {
			t.Errorf("hasMarkerLine rejected a bare marker line %q", marker)
		}
		if !hasMarkerLine("preamble\n"+marker, marker) {
			t.Errorf("hasMarkerLine rejected a trailing marker line %q", marker)
		}
	}
}
