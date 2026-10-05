package activation

import (
	"slices"
	"testing"

	"github.com/outof0/anypick/go/internal/journal"
)

func TestRecoveryScopesIncludeClientProfileBeforeClient(t *testing.T) {
	scopes := recoveryScopes(journal.Entry{
		AffectedResources: []string{
			"client/codex",
			"client-profile/codex/work",
			"account/codex/work",
		},
	})
	want := []string{"client-profile/codex/work", "client/codex", "provider/codex"}
	if !slices.Equal(scopes, want) {
		t.Fatalf("scopes = %v, want %v", scopes, want)
	}
}
