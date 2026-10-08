package doctor

import (
	"testing"

	"github.com/outof0/anypick/go/internal/refs"
)

// TestNamesPairProtectsPoolBindings pins the pool half of the orphan guard. A
// pool binding stores {Kind: account-pool, Provider: gemini, Name: ""}; its
// runtime state lives under the pool's primary account slot, so doctor
// scanning gemini/<primary> found "no reference" ("" != "work") and
// stop_orphan_proxy killed a live pool proxy as a "repair". A provider match
// on an account-pool ref must count as a reference.
func TestNamesPairProtectsPoolBindings(t *testing.T) {
	pool := refs.AccountPoolRef("gemini")
	if !namesPair(pool, "gemini", "work") {
		t.Fatal("a pool binding on gemini must protect gemini/<any-account> from stop_orphan_proxy; the guard would kill a live pool")
	}
	if namesPair(pool, "openrouter", "work") {
		t.Fatal("a gemini pool binding must not protect a different provider")
	}
	account := refs.AccountRef("gemini", "work")
	if !namesPair(account, "gemini", "work") {
		t.Fatal("an exact account binding must protect its own pair")
	}
	if namesPair(account, "gemini", "other") {
		t.Fatal("an exact account binding must not protect a different account")
	}
	if namesPair(refs.GatewayRef("gw"), "gemini", "work") {
		t.Fatal("a gateway ref is not a proxy source and protects nothing here")
	}
}
