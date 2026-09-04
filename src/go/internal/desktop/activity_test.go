package desktop

import (
	"context"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/journal"
	"github.com/outof0/anypick/go/internal/store"
)

// Activity must surface the operation journal — the single source of truth
// for mutation history — and never fabricate events.
func TestBuildActivityFromJournal(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()

	jrnl := &journal.OperationJournal{DB: env.Deps.Store.DB.DB}
	if _, err := jrnl.CreateContext(ctx, "activate:persistent", journal.CreateOptions{
		AffectedResources: []string{"client/codex", "account/grok/a"},
		State:             journal.StateCommitted,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := jrnl.CreateContext(ctx, "activate:persistent", journal.CreateOptions{
		AffectedResources: []string{"client/claude"},
		State:             journal.StateFailed,
	}); err != nil {
		t.Fatal(err)
	}
	env.Deps.Journal = jrnl

	snap, err := New(env.Deps, strings.NewReader(""), &strings.Builder{}).buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Activity) != 2 {
		t.Fatalf("activity rows = %d, want 2: %+v", len(snap.Activity), snap.Activity)
	}
	var committed, failed map[string]any
	for _, row := range snap.Activity {
		if strings.Contains(row["message"].(string), "Codex") {
			committed = row
		} else {
			failed = row
		}
	}
	if committed == nil || failed == nil {
		t.Fatalf("activity rows misattributed: %+v", snap.Activity)
	}
	if got := committed["message"].(string); !strings.Contains(got, "grok/a") {
		t.Fatalf("committed message = %q, want client + source", got)
	}
	if committed["isError"] != false {
		t.Fatalf("committed isError = %v", committed["isError"])
	}
	if failed["isError"] != true {
		t.Fatalf("failed isError = %v", failed["isError"])
	}
	for _, row := range snap.Activity {
		for _, key := range []string{"id", "createdAt", "message", "isError", "kind"} {
			if _, ok := row[key]; !ok {
				t.Fatalf("activity row missing %q: %+v", key, row)
			}
		}
		if row["createdAt"].(string) == "" {
			t.Fatalf("activity row has empty createdAt: %+v", row)
		}
	}
	// Params must never leak: only display-safe keys cross the boundary.
	for _, row := range snap.Activity {
		if _, ok := row["params"]; ok {
			t.Fatalf("journal params crossed the snapshot boundary: %+v", row)
		}
	}
}

// A nil journal (or a read failure) yields an honest empty list.
func TestBuildActivityEmptyWithoutJournal(t *testing.T) {
	env := newTrayEnv(t)
	snap, err := New(env.Deps, strings.NewReader(""), &strings.Builder{}).
		buildSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Activity) != 0 {
		t.Fatalf("activity without journal = %+v, want empty", snap.Activity)
	}
}

// Log sources must mirror the proxies that can actually serve ProxyLogs.
// The old per-provider "__provider__" placeholder never matched a proxy.log
// file, so every log request returned empty.
func TestLogSourcesMirrorProxies(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()

	env.writeCodexLive("logs@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "work", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Store.SetProxyConfigContext(ctx, "codex", "work",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	snap, err := New(env.Deps, strings.NewReader(""), &strings.Builder{}).buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Proxies) == 0 {
		t.Fatal("expected an enabled codex/work proxy")
	}
	if len(snap.LogSources) != len(snap.Proxies) {
		t.Fatalf("log sources = %+v, want one per proxy %+v", snap.LogSources, snap.Proxies)
	}
	for _, src := range snap.LogSources {
		if strings.Contains(src.ID, "__provider__") {
			t.Fatalf("placeholder log source survived: %+v", src)
		}
		if src.Name == "" || src.ProviderID == "" {
			t.Fatalf("incomplete log source: %+v", src)
		}
		wantID := src.ProviderID + "/" + src.Name
		if src.ID != wantID {
			t.Fatalf("log source id = %q, want %q", src.ID, wantID)
		}
	}
	// And the framed logs round-trip against that source id.
	found := false
	for _, src := range snap.LogSources {
		if src.ID == "codex/work" {
			found = true
		}
	}
	if !found {
		t.Fatalf("codex/work missing from log sources: %+v", snap.LogSources)
	}
}

// Proxy rows must exist for every saved account behind a proxy-capable
// provider — including never-enabled ones — or the Proxies pane can never
// offer the first Start and the toggle action is never registered.
func TestProxyRowsIncludeDisabledAccounts(t *testing.T) {
	env := newTrayEnv(t)
	ctx := context.Background()

	env.writeCodexLive("disabled@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "off", true); err != nil {
		t.Fatal(err)
	}
	// No SetProxyConfigContext: the account was never enabled.
	env.writeCodexLive("enabled@example.com")
	if _, err := env.Svc.Save(ctx, "codex", "on", true); err != nil {
		t.Fatal(err)
	}
	if err := env.Deps.Store.SetProxyConfigContext(ctx, "codex", "on",
		store.ProxyConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}

	snap, err := New(env.Deps, strings.NewReader(""), &strings.Builder{}).buildSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ProxyRow{}
	for _, p := range snap.Proxies {
		byID[p.ID] = p
	}
	off, ok := byID["codex/off"]
	if !ok {
		t.Fatalf("disabled account has no proxy row: %+v", snap.Proxies)
	}
	if off.Enabled {
		t.Fatalf("codex/off Enabled = true, want false: %+v", off)
	}
	if off.ToggleActionID == "" || off.RestartActionID == "" {
		t.Fatalf("codex/off has no actions: %+v", off)
	}
	on, ok := byID["codex/on"]
	if !ok || !on.Enabled {
		t.Fatalf("enabled account row wrong: %+v", snap.Proxies)
	}
	if snap.ProxyCount != 1 {
		t.Fatalf("ProxyCount = %d, want enabled-only 1", snap.ProxyCount)
	}
	for _, row := range snap.AccountProviders {
		if row.ClientID == "" {
			t.Fatalf("provider row %q has no clientId: %+v", row.ID, row)
		}
	}
}
