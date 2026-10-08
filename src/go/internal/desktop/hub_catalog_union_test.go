package desktop

import (
	"context"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/hub"
)

// TestHubCatalogListPinsLeadUnion pins the desktop's half of the hub model
// union. hubCatalogList used to return ONLY the route pins when any existed,
// so a hub with one pin advertised 7 models while its accounts publish 23 —
// and the macOS/Tauri apps read this path, not the CLI's. Pins lead, in pin
// order; every enabled source's catalog follows, exactly like
// cmd/anypick/models.go hubActivationModels.
func TestHubCatalogListPinsLeadUnion(t *testing.T) {
	env := hubTestEnv(t)
	ctx := context.Background()

	// A source row needs a real account behind it (the FK refuses ghosts), so
	// save one and toggle it in — the same path the app itself takes.
	env.writeCodexLive("hub-union@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "a", true); err != nil {
		t.Fatal(err)
	}
	srv := New(env.Deps, strings.NewReader(""), &strings.Builder{})
	if res := toggleHub(t, srv, "hu-1", "test", "codex", "a", true); res.Status != "success" {
		t.Fatalf("hub source enable = %+v", res)
	}
	if err := env.Deps.Hub.AttachRouteContext(ctx, hub.Route{
		RouteID: hub.DefaultRouteID("test"), HubName: "test", ClientID: "claude",
		Models: []hub.ModelRoute{
			{Model: "zz-pinned-first", SourceIndex: 0},
			{Model: "aa-pinned-second", SourceIndex: 0},
		},
	}); err != nil {
		t.Fatal(err)
	}

	got := New(env.Deps, strings.NewReader(""), &strings.Builder{}).hubCatalogList(ctx, "test")
	if len(got) < 2 {
		t.Fatalf("catalog = %v, want at least the two pins", got)
	}
	if got[0] != "zz-pinned-first" || got[1] != "aa-pinned-second" {
		t.Fatalf("pins must lead in pin order: %v", got)
	}
	// The union half: deepseek's static catalog must appear after the pins.
	seen := map[string]bool{}
	for _, m := range got {
		if seen[m] {
			t.Fatalf("duplicate model %q in %v", m, got)
		}
		seen[m] = true
	}
	if len(got) == 2 {
		t.Fatalf("pins only, no union with the source's catalog: %v", got)
	}
}
