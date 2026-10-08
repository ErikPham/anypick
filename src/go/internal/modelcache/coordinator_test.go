package modelcache

import (
	"testing"

	"github.com/outof0/anypick/go/internal/testgate"
)

// TestMutationsAreGatedWithTheCallerHoldsLockContract puts modelcache in the
// same coordinator gate as store/pools/profile/presets, with the deliberate
// no-self-lock shape allowlisted per method and the reason written where the
// other seven packages write theirs. Before this test, the caller-holds-lock
// contract lived only in prose (the package doc and a behaviour test that
// pins the unsafe direction); a new caller reaching Clear with no lock would
// repeat the doctor defect and nothing would fail the build.
func TestMutationsAreGatedWithTheCallerHoldsLockContract(t *testing.T) {
	testgate.CheckUncoordinatedMutations(t, ".", []string{"Store"}, map[string]string{
		"Store.Write": "callers hold model-cache (ADR 0009): discovery.Service.StoreLive and doctor.fixRebuildCaches are the two callers; the store itself takes no lock by design",
		"Store.Clear": "same contract as Store.Write; a bare Clear without the scope was the doctor defect this gate exists to prevent",
	})
}
