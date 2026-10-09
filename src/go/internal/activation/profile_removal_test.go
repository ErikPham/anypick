package activation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/outof0/anypick/go/internal/anyerr"
	"github.com/outof0/anypick/go/internal/bindings"
	"github.com/outof0/anypick/go/internal/client"
	"github.com/outof0/anypick/go/internal/hub"
	"github.com/outof0/anypick/go/internal/refs"
	"github.com/outof0/anypick/go/internal/transport"
)

// profileFixture builds one named Codex profile on the shared test env: an
// account (optional), the stored route, and the native files an activation
// would have written.
type profileFixture struct {
	name       string
	profileDir string
	authPath   string
	configPath string
	envPath    string
}

func (e *testEnv) makeCodexProfile(t *testing.T, name string, source refs.Ref, policy string) profileFixture {
	t.Helper()
	if _, err := e.ClientProfiles.UpsertContext(context.Background(), "codex", name, bindings.Spec{
		Client: "codex", Source: source,
		Model: bindings.ExplicitModel("model-" + name), TransportPolicy: policy,
	}); err != nil {
		t.Fatal(err)
	}
	home := client.CodexProfileHomePath(e.Root, name)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := client.CodexProfileAuthPath(e.Root, name)
	if err := os.WriteFile(authPath, []byte(`{"OPENAI_API_KEY":"sk-`+name+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := client.CodexProfileConfigPathForRoot(e.Root, name)
	body := "# BEGIN anypick-managed\nmodel = \"model-" + name + "\"\n# END anypick-managed\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	envPath := client.CodexProfileEnvPath(e.Root, name)
	if err := os.WriteFile(envPath, []byte("export X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return profileFixture{name: name, profileDir: home, authPath: authPath,
		configPath: configPath, envPath: envPath}
}

func (f profileFixture) assertGone(t *testing.T) {
	t.Helper()
	for _, path := range []string{f.profileDir, f.authPath, f.configPath, f.envPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("profile %s left %s behind (stat err = %v)", f.name, path, err)
		}
	}
}

func TestAccountProfileRemovalsSelectsOnlyTheDeletedAccount(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.writeCodexLive(t, "gone@example.com", "token-gone")
	if _, err := env.Svc.Save(ctx, "codex", "gone", true); err != nil {
		t.Fatal(err)
	}
	env.writeCodexLive(t, "kept@example.com", "token-kept")
	if _, err := env.Svc.Save(ctx, "codex", "kept", true); err != nil {
		t.Fatal(err)
	}

	env.makeCodexProfile(t, "native-gone", refs.AccountRef("codex", "gone"), "auto")
	env.makeCodexProfile(t, "native-kept", refs.AccountRef("codex", "kept"), "auto")
	env.makeCodexProfile(t, "gateway", refs.GatewayRef("openrouter"), "direct")

	removals, err := AccountProfileRemovals(ctx, env.Deps, "codex", "gone")
	if err != nil {
		t.Fatal(err)
	}
	if len(removals) != 1 || removals[0].Name != "native-gone" {
		t.Fatalf("cascade selected %#v, want exactly native-gone", removals)
	}
	if !removals[0].PurgeCredentials {
		t.Fatal("cascade must purge the credential home, not just the route files")
	}
}

func TestCleanOrphanedProfilesRemovesOnlyProfilesWithMissingAccounts(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.writeCodexLive(t, "alive@example.com", "token-alive")
	if _, err := env.Svc.Save(ctx, "codex", "alive", true); err != nil {
		t.Fatal(err)
	}
	orphan := env.makeCodexProfile(t, "orphan", refs.AccountRef("codex", "deleted"), "auto")
	history := filepath.Join(orphan.profileDir, "history.jsonl")
	if err := os.WriteFile(history, []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := env.makeCodexProfile(t, "alive", refs.AccountRef("codex", "alive"), "auto")
	gateway := env.makeCodexProfile(t, "gateway", refs.GatewayRef("openrouter"), "direct")

	removed, err := CleanOrphanedProfiles(ctx, env.Deps)
	if err != nil {
		t.Fatalf("clean orphaned profiles: %v", err)
	}
	if len(removed) != 1 || removed[0].Name != "orphan" {
		t.Fatalf("swept %#v, want exactly the orphan", removed)
	}
	if removed[0].PurgeCredentials {
		t.Fatal("unattended orphan sweep set PurgeCredentials")
	}
	if _, err := os.Stat(orphan.profileDir); err != nil {
		t.Fatalf("orphan sweep deleted the credential home: %v", err)
	}
	if _, err := os.Stat(history); err != nil {
		t.Fatalf("orphan sweep deleted history in the credential home: %v", err)
	}
	if _, err := env.ClientProfiles.GetContext(ctx, "codex", "orphan"); err != nil {
		t.Fatal(err)
	}
	if row, err := env.ClientProfiles.GetContext(ctx, "codex", "orphan"); err != nil || row != nil {
		t.Fatalf("orphan row survived the sweep: %#v, %v", row, err)
	}
	for _, path := range []string{kept.authPath, kept.configPath, kept.envPath} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("sweep touched a live account's profile: %s: %v", path, err)
		}
	}
	// A gateway-backed profile is not an account profile; it must survive even
	// though it has no account row to point at.
	if _, err := os.Stat(gateway.authPath); err != nil {
		t.Errorf("sweep removed a gateway-backed profile: %v", err)
	}
}

// The whole point of the cascade is that a deleted account cannot leave its
// credential behind. ResetProfile (the explicit-removal path) keeps auth.json
// on purpose, so the sweep must use the purge path instead.
func TestPurgeRemovesTheCredentialHomeExplicitRemovalKeeps(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	fixture := env.makeCodexProfile(t, "purge-me", refs.GatewayRef("openrouter"), "direct")

	if err := RemoveNamedProfiles(ctx, []ProfileRemoval{
		{Client: "codex", Name: "purge-me", PurgeCredentials: true},
	}, env.Deps, nil); err != nil {
		t.Fatal(err)
	}
	fixture.assertGone(t)

	explicit := env.makeCodexProfile(t, "keep-auth", refs.GatewayRef("openrouter"), "direct")
	if err := RemoveNamedProfiles(ctx, []ProfileRemoval{
		{Client: "codex", Name: "keep-auth"},
	}, env.Deps, nil); err != nil {
		t.Fatal(err)
	}
	// Both removal paths drop auth.json: a profile whose route is gone must
	// not keep authenticating Codex on its own. What the explicit path keeps
	// is the directory itself, so a fresh `profile login` under the same name
	// reuses it instead of racing a half-removed home.
	if _, err := os.Stat(explicit.authPath); !os.IsNotExist(err) {
		t.Fatalf("explicit removal left the profile auth.json: %v", err)
	}
	if _, err := os.Stat(explicit.profileDir); err != nil {
		t.Fatalf("explicit removal must keep the credential home directory: %v", err)
	}
	if _, err := os.Stat(explicit.configPath); !os.IsNotExist(err) {
		t.Fatalf("explicit removal left the generated config: %v", err)
	}
}

func TestRemoveNamedProfilesStopsBeforeRemovingWhenStopFails(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	fixture := env.makeCodexProfile(t, "blocked", refs.GatewayRef("openrouter"), "direct")

	stopErr := errors.New("proxy refused to stop")
	err := RemoveNamedProfiles(ctx, []ProfileRemoval{
		{Client: "codex", Name: "blocked", PurgeCredentials: true},
	}, env.Deps, func(context.Context) error { return stopErr })
	if !errors.Is(err, stopErr) {
		t.Fatalf("stop failure = %v, want it propagated", err)
	}
	if _, err := os.Stat(fixture.configPath); err != nil {
		t.Fatalf("removal ran despite a failed stop: %v", err)
	}
	row, err := env.ClientProfiles.GetContext(ctx, "codex", "blocked")
	if err != nil || row == nil {
		t.Fatalf("row was removed despite a failed stop: %#v, %v", row, err)
	}
}

func TestRemoveNamedProfilesDetachesTheHubRoute(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	// The hub source must name a real account: proxy_hub_sources carries a
	// foreign key onto accounts, and the route detach below is what this test
	// is about, not referential integrity.
	env.writeCodexLive(t, "work@example.com", "token-work")
	if _, err := env.Svc.Save(ctx, "codex", "work", true); err != nil {
		t.Fatal(err)
	}
	cfg, err := env.Deps.Hub.UpsertConfigContext(ctx, hub.Config{
		Name: "default", Enabled: true, Port: 45999,
		Sources: []hub.Source{{ProviderID: "codex", Account: "work"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	routeID := hub.ClientProfileRouteID(cfg.Name, "codex", "hub-work")
	if err := env.Deps.Hub.AttachRouteContext(ctx, hub.Route{
		RouteID: routeID, HubName: cfg.Name, ClientID: "codex", Token: "tok",
	}); err != nil {
		t.Fatal(err)
	}
	env.makeCodexProfile(t, "hub-work", refs.ProxyHubRef(cfg.Name), "auto")

	// Build the removal the way every real caller does, so the hub mutation
	// scope the route detach needs is part of what is under test.
	stored, err := env.ClientProfiles.GetContext(ctx, "codex", "hub-work")
	if err != nil || stored == nil {
		t.Fatalf("stored profile = %#v, %v", stored, err)
	}
	removal := ProfileRemovalFor(stored, true)
	if removal.HubName != cfg.Name {
		t.Fatalf("removal lost the hub scope: %#v", removal)
	}
	if err := RemoveNamedProfiles(ctx, []ProfileRemoval{removal}, env.Deps, nil); err != nil {
		t.Fatal(err)
	}
	row, err := env.Deps.Hub.SnapshotRouteContext(ctx, routeID)
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("hub route %s survived profile removal: %#v", routeID, row)
	}
}

// A profile that no longer exists is not an error for the cascade: the desired
// state (no profile) already holds, and a concurrent removal or a second sweep
// must not turn that into a failed account deletion.
func TestRemoveNamedProfilesIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.makeCodexProfile(t, "twice", refs.GatewayRef("openrouter"), "direct")
	removal := []ProfileRemoval{{Client: "codex", Name: "twice", PurgeCredentials: true}}
	if err := RemoveNamedProfiles(ctx, removal, env.Deps, nil); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNamedProfiles(ctx, removal, env.Deps, nil); err != nil {
		t.Fatalf("second removal = %v, want success", err)
	}
}

// An account row that cannot be found is not an error for the sweep: the
// profile's source is unreachable either way, so it is an orphan. Only a read
// that FAILS (the store is down) is reported instead of swept.
func TestOrphanedAccountProfilesSweepsUnresolvableSources(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.makeCodexProfile(t, "unreadable", refs.AccountRef("ghost-provider", "x"), "auto")

	orphans, err := OrphanedAccountProfiles(ctx, env.Deps)
	if err != nil {
		t.Fatalf("an unknown provider must not abort the sweep: %v", err)
	}
	if len(orphans) != 1 || orphans[0].Name != "unreadable" {
		t.Fatalf("orphans = %#v, want the profile of an unknown provider", orphans)
	}
}

func TestNamedProfileChangedDetectsRevisionAndSourceDrift(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	planned, _, err := PlanNamedProfileRemoval(ctx, "codex", "missing", env.Deps)
	if err == nil || planned != nil {
		t.Fatalf("planning a missing profile = %#v, %v; want CODEX_PROFILE_NOT_FOUND", planned, err)
	}
	var ae *anyerr.Error
	if !errors.As(err, &ae) || ae.Code != "CODEX_PROFILE_NOT_FOUND" {
		t.Fatalf("missing profile error = %v, want CODEX_PROFILE_NOT_FOUND", err)
	}

	if _, err := env.ClientProfiles.UpsertContext(ctx, "codex", "drift", bindings.Spec{
		Client: "codex", Source: refs.GatewayRef("one"), TransportPolicy: string(transport.PolicyAuto),
	}); err != nil {
		t.Fatal(err)
	}
	planned, removal, err := PlanNamedProfileRemoval(ctx, "codex", "drift", env.Deps)
	if err != nil {
		t.Fatal(err)
	}
	if removal.Client != "codex" || removal.Name != "drift" || removal.PurgeCredentials {
		t.Fatalf("planned removal = %#v, want the explicit path (no credential purge)", removal)
	}
	if removal.HubName != "" {
		t.Fatalf("a gateway-backed profile must not claim a hub scope: %#v", removal)
	}
	current, err := env.ClientProfiles.GetContext(ctx, "codex", "drift")
	if err != nil {
		t.Fatal(err)
	}
	if NamedProfileChanged(planned, current) {
		t.Fatal("an unchanged profile reported drift")
	}
	if _, err := env.ClientProfiles.UpsertContext(ctx, "codex", "drift", bindings.Spec{
		Client: "codex", Source: refs.GatewayRef("two"), TransportPolicy: string(transport.PolicyAuto),
	}); err != nil {
		t.Fatal(err)
	}
	moved, err := env.ClientProfiles.GetContext(ctx, "codex", "drift")
	if err != nil {
		t.Fatal(err)
	}
	if !NamedProfileChanged(planned, moved) {
		t.Fatal("a profile re-pointed at another source must report drift")
	}
	if !strings.HasPrefix(refs.DisplayRef(moved.Spec.Source), "two") {
		t.Fatalf("fixture did not move the source: %#v", moved.Spec.Source)
	}
}

func TestProfileRemovalLabelsAreClientScoped(t *testing.T) {
	labels := ProfileRemovalLabels([]ProfileRemoval{
		{Client: "codex", Name: "a"}, {Client: "codex", Name: "b"},
	})
	if strings.Join(labels, ",") != "codex/a,codex/b" {
		t.Fatalf("labels = %#v", labels)
	}
}
